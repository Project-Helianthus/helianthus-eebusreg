package eebusstore

import (
	"bytes"
	"crypto"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"fmt"
)

type providerKey struct {
	id      string
	version uint64
}

type protectedKeyProvider interface {
	probe(providerID string, version uint64) error
	validate(sealedBlob, expectedSPKI []byte) error
	unseal(sealedBlob []byte) (crypto.Signer, error)
}

func validateProtectedKeys(state stateV1, providers map[providerKey]protectedKeyProvider) error {
	if state.localIdentity == nil {
		return nil
	}
	identity := state.localIdentity
	if len(identity.certificateChainDER) == 0 {
		return keyMaterialError("validate_certificate", errors.New("missing certificate"))
	}
	var leaf *x509.Certificate
	for index, encoded := range identity.certificateChainDER {
		certificate, err := x509.ParseCertificate(encoded)
		if err != nil {
			return keyMaterialError("validate_certificate", err)
		}
		if index == 0 {
			leaf = certificate
		}
	}
	spki := bytes.Clone(leaf.RawSubjectPublicKeyInfo)
	digest := sha256.Sum256(spki)
	if fmt.Sprintf("%x", digest[:]) != identity.keyReference.certificateSPKISHA256 {
		return keyMaterialError("validate_certificate", errors.New("SPKI digest mismatch"))
	}
	key := providerKey{id: identity.keyReference.providerID, version: identity.keyReference.providerVersion}
	provider := providers[key]
	if provider == nil {
		return newStoreError(outcomeKeyProviderUnavailable, "probe_provider", errors.New("provider missing"))
	}
	if err := provider.probe(key.id, key.version); err != nil {
		return newStoreError(outcomeKeyProviderUnavailable, "probe_provider", err)
	}
	if err := provider.validate(bytes.Clone(identity.keyReference.sealedBlob), spki); err != nil {
		return keyMaterialError("validate_key", err)
	}
	signer, err := provider.unseal(bytes.Clone(identity.keyReference.sealedBlob))
	if err != nil || signer == nil {
		return keyMaterialError("unseal_key", err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil || !bytes.Equal(publicDER, spki) {
		return keyMaterialError("bind_key", err)
	}
	return nil
}

func keyMaterialError(operation string, cause error) *storeError {
	return newStoreError(outcomeKeyMaterialUnavailable, operation, cause)
}
