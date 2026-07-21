//go:build linux || darwin

package eebusfacade

import (
	"bytes"
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusstore"
	shipcert "github.com/Project-Helianthus/helianthus-ship-go/cert"
)

const (
	nativeProtectedProviderID      = "native-host-root-aesgcm"
	nativeProtectedProviderVersion = uint64(1)
	nativeProtectedBlobVersion     = byte(1)
	nativeProtectedSPKISize        = sha256.Size
)

var (
	errNativeProtectedStateUnavailable    = errors.New("protected runtime material: state unavailable")
	errNativeProtectedBindingUnavailable  = errors.New("protected runtime material: host binding unavailable")
	errNativeProtectedIdentityUnavailable = errors.New("protected runtime material: identity unavailable")
	errNativeProtectedKeyUnavailable      = errors.New("native protected key unavailable")
)

type nativeProtectedIdentityProvider struct {
	mu sync.Mutex

	key            [sha256.Size]byte
	rootBinding    [sha256.Size]byte
	anchorIdentity [sha256.Size]byte
	anchor         firstTrustAnchorRecord
	anchorReady    bool
	witness        *nativeProtectedWitness
}

type nativeProtectedSigner struct {
	delegate crypto.Signer
}

var (
	_ crypto.Signer              = (*nativeProtectedSigner)(nil)
	_ eebusstore.KeyProvider     = (*nativeProtectedIdentityProvider)(nil)
	_ firstTrustAnchorProvider   = (*nativeProtectedIdentityProvider)(nil)
	_ firstTrustIdentityProvider = (*nativeProtectedIdentityProvider)(nil)
)

func loadNativeProtectedRuntimeMaterial(ctx context.Context, stateRoot string) (runtimeMaterial, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return runtimeMaterial{}, err
	}

	probe, _ := eebusstore.OpenAssociationBridge(stateRoot, nil)
	if probe == nil {
		return runtimeMaterial{}, errNativeProtectedStateUnavailable
	}
	if err := probe.Close(); err != nil {
		return runtimeMaterial{}, errNativeProtectedStateUnavailable
	}

	provider, err := newNativeProtectedIdentityProvider(stateRoot)
	if err != nil {
		return runtimeMaterial{}, errNativeProtectedBindingUnavailable
	}
	binding := eebusstore.KeyProviderBinding{
		ID: nativeProtectedProviderID, Version: nativeProtectedProviderVersion, Provider: provider,
	}
	bridge, outcome := eebusstore.OpenAssociationBridge(stateRoot, []eebusstore.KeyProviderBinding{binding})
	if bridge == nil {
		return runtimeMaterial{}, errNativeProtectedStateUnavailable
	}
	closed := false
	closeBridge := func() error {
		if closed {
			return nil
		}
		closed = true
		return bridge.Close()
	}
	defer closeBridge()
	if !nativeProtectedOpenOutcome(outcome) || !provider.boundToStateRoot(stateRoot) {
		return runtimeMaterial{}, errNativeProtectedIdentityUnavailable
	}

	view, outcome := bridge.ReloadControl(ctx)
	if !nativeProtectedOpenOutcome(outcome) {
		return runtimeMaterial{}, errNativeProtectedIdentityUnavailable
	}
	if !nativeProtectedControlHasIdentity(view.Control) {
		if view.Control.Present || len(view.Associations) != 0 {
			return runtimeMaterial{}, errNativeProtectedIdentityUnavailable
		}
		if err := bootstrapNativeProtectedIdentity(ctx, bridge, provider); err != nil {
			return runtimeMaterial{}, errNativeProtectedIdentityUnavailable
		}
		view, outcome = bridge.ReloadControl(ctx)
		if !nativeProtectedOpenOutcome(outcome) || !nativeProtectedControlHasIdentity(view.Control) {
			return runtimeMaterial{}, errNativeProtectedIdentityUnavailable
		}
	}
	if !provider.boundToStateRoot(stateRoot) || !provider.restoreAnchor(view) {
		return runtimeMaterial{}, errNativeProtectedIdentityUnavailable
	}

	material, err := nativeProtectedMaterialFromControl(stateRoot, view, provider, binding)
	if err != nil {
		return runtimeMaterial{}, errNativeProtectedIdentityUnavailable
	}
	if err := closeBridge(); err != nil {
		return runtimeMaterial{}, errNativeProtectedStateUnavailable
	}
	return material, nil
}

func nativeProtectedOpenOutcome(outcome string) bool {
	switch outcome {
	case "opened_empty", "opened_current", "opened_migrated":
		return true
	default:
		return false
	}
}

func nativeProtectedControlHasIdentity(record eebusstore.ControlRecord) bool {
	return record.Present && len(record.LocalCertificateChainDER) != 0 &&
		record.LocalProviderID != "" && record.LocalProviderVersion != 0 &&
		len(record.LocalSealedBlob) != 0 && record.LocalCertificateSPKISHA256 != [sha256.Size]byte{} &&
		len(record.LocalSKI) != 0
}

func bootstrapNativeProtectedIdentity(
	ctx context.Context,
	bridge *eebusstore.AssociationBridge,
	provider *nativeProtectedIdentityProvider,
) error {
	if bridge == nil || provider == nil {
		return errNativeProtectedIdentityUnavailable
	}
	origin := time.Now()
	coordinator := newFirstTrustCoordinatorWithRecovery(
		time.Now,
		func() time.Duration { return time.Since(origin) },
		rand.Reader,
		&runtimeControlBridge{bridge: bridge},
		provider,
		nil,
		firstTrustBackoffPolicy{
			base: firstTrustBackoffBase, exponentCap: firstTrustBackoffExponentCap,
			maximum: firstTrustBackoffMaximum, attemptMaximum: firstTrustAttemptMaximum,
		},
	)
	defer coordinator.shutdown()
	coordinator.identityProvider = provider
	if outcome := coordinator.reopenWithRecovery(ctx); outcome == "reopen_cancelled" || outcome == "reopen_in_progress" || outcome == "store_unavailable" {
		return errNativeProtectedIdentityUnavailable
	}
	coordinator.mu.Lock()
	if coordinator.recovery != "NO_LOCAL_IDENTITY" || coordinator.recoveryReasonCode != "HOST_KEY_UNAVAILABLE" {
		coordinator.mu.Unlock()
		return errNativeProtectedIdentityUnavailable
	}
	operationID, ok := firstTrustReadOrdinal(rand.Reader)
	if !ok {
		coordinator.mu.Unlock()
		return errNativeProtectedIdentityUnavailable
	}
	request := firstTrustRepairRequest{
		operationID: operationID, kind: "recover_unavailable_host_key",
		expectedState: coordinator.recovery, expectedReason: coordinator.recoveryReasonCode,
		expectedManifest:          cloneFirstTrustManifest(coordinator.controlView.manifest),
		expectedControlEpoch:      coordinator.controlView.control.controlEpoch,
		expectedAnchorVersion:     coordinator.anchorRecord.version,
		expectedManifestHighWater: coordinator.anchorRecord.manifestGenerationHighWater,
		expectedControlHighWater:  coordinator.anchorRecord.controlEpochHighWater,
		nextRepairSequence:        coordinator.controlView.control.repairSequence + 1,
	}
	coordinator.mu.Unlock()
	if outcome := coordinator.repair(ctx, request); outcome != "repaired_unpaired" {
		return errNativeProtectedIdentityUnavailable
	}
	if coordinator.state() != "PAIRING_CLOSED" || coordinator.recoveryState() != "UNPAIRED_LOCKED" {
		return errNativeProtectedIdentityUnavailable
	}
	return nil
}

func nativeProtectedMaterialFromControl(
	stateRoot string,
	view eebusstore.ControlView,
	provider *nativeProtectedIdentityProvider,
	binding eebusstore.KeyProviderBinding,
) (runtimeMaterial, error) {
	record := view.Control
	if record.LocalProviderID != binding.ID || record.LocalProviderVersion != binding.Version || len(record.LocalSKI) != 20 {
		return runtimeMaterial{}, errNativeProtectedIdentityUnavailable
	}
	leaf, err := x509.ParseCertificate(record.LocalCertificateChainDER[0])
	if err != nil || sha256.Sum256(leaf.RawSubjectPublicKeyInfo) != record.LocalCertificateSPKISHA256 {
		return runtimeMaterial{}, errNativeProtectedIdentityUnavailable
	}
	signer, err := provider.Unseal(record.LocalSealedBlob)
	if err != nil || signer == nil {
		return runtimeMaterial{}, errNativeProtectedIdentityUnavailable
	}
	certificate := newProtectedTLSCertificate(cloneNativeProtectedByteSlices(record.LocalCertificateChainDER), signer)
	pretrusted := make(map[string]bool, len(view.Associations))
	for _, association := range view.Associations {
		if len(association.Subject) == 20 && association.Active && association.Trusted {
			pretrusted[hex.EncodeToString(association.Subject)] = true
		}
	}
	return runtimeMaterial{
		certificate: certificate,
		localSKI:    hex.EncodeToString(record.LocalSKI),
		nodeToken:   canonicalRuntimeNodeToken(record.StoreInstance),
		pretrusted:  pretrusted,
		firstTrust: &runtimeFirstTrustAuthorization{
			adminRuntimeDir:  nativeProtectedAdminRuntimeDir(stateRoot),
			hostAnchor:       provider,
			identityProvider: provider,
			keyProviders:     []eebusstore.KeyProviderBinding{binding},
		},
	}, nil
}

func nativeProtectedAdminRuntimeDir(stateRoot string) string {
	parent, name := filepath.Dir(filepath.Clean(stateRoot)), filepath.Base(filepath.Clean(stateRoot))
	return filepath.Join(parent, "."+name+"-first-trust-runtime")
}

func newNativeProtectedIdentityProvider(stateRoot string) (*nativeProtectedIdentityProvider, error) {
	machineIdentity, err := nativeMachineIdentity()
	if err != nil {
		return nil, errNativeProtectedBindingUnavailable
	}
	rootBinding, err := nativeProtectedRootBinding(stateRoot)
	if err != nil {
		return nil, errNativeProtectedBindingUnavailable
	}
	witness, err := openNativeProtectedWitness(stateRoot, machineIdentity)
	if err != nil {
		clear(machineIdentity[:])
		return nil, errNativeProtectedBindingUnavailable
	}
	key := witness.keyMaterial()
	anchorIdentity := nativeProtectedDerive(key[:], "anchor", rootBinding[:])
	clear(machineIdentity[:])
	provider := &nativeProtectedIdentityProvider{
		key: key, rootBinding: rootBinding, anchorIdentity: anchorIdentity, witness: witness,
	}
	anchor, found, err := witness.loadAnchor()
	if err != nil || found && anchor.anchorIdentity != anchorIdentity {
		return nil, errNativeProtectedBindingUnavailable
	}
	if found {
		provider.anchor = cloneFirstTrustAnchorRecord(anchor)
		provider.anchorReady = true
	}
	return provider, nil
}

func nativeProtectedRootBinding(stateRoot string) ([sha256.Size]byte, error) {
	var zero [sha256.Size]byte
	clean := filepath.Clean(strings.TrimSpace(stateRoot))
	if clean == "." || clean == "" || !filepath.IsAbs(clean) {
		return zero, errNativeProtectedBindingUnavailable
	}
	volumeRoot := filepath.VolumeName(clean) + string(filepath.Separator)
	if clean == volumeRoot {
		return zero, errNativeProtectedBindingUnavailable
	}
	info, err := os.Lstat(clean)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return zero, errNativeProtectedBindingUnavailable
	}
	canonical, err := filepath.EvalSymlinks(clean)
	if err != nil || !filepath.IsAbs(canonical) {
		return zero, errNativeProtectedBindingUnavailable
	}
	canonicalInfo, err := os.Lstat(canonical)
	if err != nil || canonicalInfo.Mode()&os.ModeSymlink != 0 || !canonicalInfo.IsDir() ||
		canonicalInfo.Mode().Perm() != 0o700 || !os.SameFile(info, canonicalInfo) {
		return zero, errNativeProtectedBindingUnavailable
	}
	hash := sha256.New()
	_, _ = io.WriteString(hash, "helianthus-eebusreg/native-root/v1\x00")
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(len(canonical)))
	_, _ = hash.Write(encoded[:])
	_, _ = io.WriteString(hash, canonical)
	copy(zero[:], hash.Sum(nil))
	return zero, nil
}

func nativeProtectedDerive(key []byte, purpose string, binding []byte) [sha256.Size]byte {
	mac := hmac.New(sha256.New, key)
	_, _ = io.WriteString(mac, "helianthus-eebusreg/native-protected/v1\x00")
	_, _ = io.WriteString(mac, purpose)
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(binding)
	var result [sha256.Size]byte
	copy(result[:], mac.Sum(nil))
	return result
}

func (provider *nativeProtectedIdentityProvider) Probe(id string, version uint64) error {
	if provider == nil || id != nativeProtectedProviderID || version != nativeProtectedProviderVersion || provider.key == [sha256.Size]byte{} {
		return errNativeProtectedKeyUnavailable
	}
	return nil
}

func (provider *nativeProtectedIdentityProvider) boundToStateRoot(stateRoot string) bool {
	if provider == nil {
		return false
	}
	binding, err := nativeProtectedRootBinding(stateRoot)
	return err == nil && hmac.Equal(binding[:], provider.rootBinding[:])
}

func (provider *nativeProtectedIdentityProvider) Validate(sealedBlob, expectedSPKI []byte) error {
	if provider == nil || len(expectedSPKI) == 0 {
		return errNativeProtectedKeyUnavailable
	}
	wantDigest := sha256.Sum256(expectedSPKI)
	digest, signer, err := provider.openSigner(sealedBlob)
	if err != nil || digest != wantDigest || signer == nil {
		return errNativeProtectedKeyUnavailable
	}
	publicDER, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil || !bytes.Equal(publicDER, expectedSPKI) {
		return errNativeProtectedKeyUnavailable
	}
	return nil
}

func (provider *nativeProtectedIdentityProvider) Unseal(sealedBlob []byte) (crypto.Signer, error) {
	_, signer, err := provider.openSigner(sealedBlob)
	if err != nil || signer == nil {
		return nil, errNativeProtectedKeyUnavailable
	}
	return signer, nil
}

func (provider *nativeProtectedIdentityProvider) openSigner(sealedBlob []byte) ([sha256.Size]byte, crypto.Signer, error) {
	var digest [sha256.Size]byte
	if provider == nil {
		return digest, nil, errNativeProtectedKeyUnavailable
	}
	block, err := aes.NewCipher(provider.key[:])
	if err != nil {
		return digest, nil, errNativeProtectedKeyUnavailable
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return digest, nil, errNativeProtectedKeyUnavailable
	}
	headerSize := 1 + nativeProtectedSPKISize + aead.NonceSize()
	if len(sealedBlob) <= headerSize || sealedBlob[0] != nativeProtectedBlobVersion {
		return digest, nil, errNativeProtectedKeyUnavailable
	}
	copy(digest[:], sealedBlob[1:1+nativeProtectedSPKISize])
	nonceStart := 1 + nativeProtectedSPKISize
	nonce := sealedBlob[nonceStart:headerSize]
	plaintext, err := aead.Open(nil, nonce, sealedBlob[headerSize:], provider.sealAAD(digest))
	if err != nil {
		return digest, nil, errNativeProtectedKeyUnavailable
	}
	defer clear(plaintext)
	key, err := x509.ParsePKCS8PrivateKey(plaintext)
	if err != nil {
		return digest, nil, errNativeProtectedKeyUnavailable
	}
	signer, ok := key.(crypto.Signer)
	if !ok || signer == nil {
		return digest, nil, errNativeProtectedKeyUnavailable
	}
	publicDER, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil || sha256.Sum256(publicDER) != digest {
		return digest, nil, errNativeProtectedKeyUnavailable
	}
	return digest, &nativeProtectedSigner{delegate: signer}, nil
}

func (provider *nativeProtectedIdentityProvider) sealSigner(signer crypto.Signer, spki []byte) ([]byte, error) {
	if provider == nil || signer == nil || len(spki) == 0 {
		return nil, errNativeProtectedKeyUnavailable
	}
	plaintext, err := x509.MarshalPKCS8PrivateKey(signer)
	if err != nil {
		return nil, errNativeProtectedKeyUnavailable
	}
	defer clear(plaintext)
	block, err := aes.NewCipher(provider.key[:])
	if err != nil {
		return nil, errNativeProtectedKeyUnavailable
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errNativeProtectedKeyUnavailable
	}
	digest := sha256.Sum256(spki)
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, errNativeProtectedKeyUnavailable
	}
	sealed := make([]byte, 1+nativeProtectedSPKISize+len(nonce))
	sealed[0] = nativeProtectedBlobVersion
	copy(sealed[1:], digest[:])
	copy(sealed[1+nativeProtectedSPKISize:], nonce)
	return aead.Seal(sealed, nonce, plaintext, provider.sealAAD(digest)), nil
}

func (provider *nativeProtectedIdentityProvider) sealAAD(digest [sha256.Size]byte) []byte {
	aad := make([]byte, 0, 96)
	aad = append(aad, "helianthus-eebusreg/native-sealed-key/v1\x00"...)
	aad = append(aad, nativeProtectedProviderID...)
	var version [8]byte
	binary.BigEndian.PutUint64(version[:], nativeProtectedProviderVersion)
	aad = append(aad, version[:]...)
	aad = append(aad, provider.rootBinding[:]...)
	aad = append(aad, digest[:]...)
	return aad
}

func (signer *nativeProtectedSigner) Public() crypto.PublicKey {
	if signer == nil || signer.delegate == nil {
		return nil
	}
	return signer.delegate.Public()
}

func (signer *nativeProtectedSigner) Sign(random io.Reader, digest []byte, options crypto.SignerOpts) ([]byte, error) {
	if signer == nil || signer.delegate == nil {
		return nil, errNativeProtectedKeyUnavailable
	}
	return signer.delegate.Sign(random, digest, options)
}

func (provider *nativeProtectedIdentityProvider) CreateSigningIdentity(ctx context.Context) (firstTrustLocalIdentityBinding, string) {
	if provider == nil || nativeProtectedContextCancelled(ctx) {
		return firstTrustLocalIdentityBinding{}, "identity_not_published"
	}
	certificate, err := shipcert.CreateCertificate("", "Project-Helianthus", "RO", "helianthus-eebusreg")
	if err != nil || len(certificate.Certificate) == 0 {
		return firstTrustLocalIdentityBinding{}, "identity_not_published"
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return firstTrustLocalIdentityBinding{}, "identity_not_published"
	}
	signer, ok := certificate.PrivateKey.(crypto.Signer)
	if !ok || signer == nil {
		return firstTrustLocalIdentityBinding{}, "identity_not_published"
	}
	spki, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil {
		return firstTrustLocalIdentityBinding{}, "identity_not_published"
	}
	sealed, err := provider.sealSigner(signer, spki)
	if err != nil {
		return firstTrustLocalIdentityBinding{}, "identity_not_published"
	}
	ski, err := shipcert.SkiFromCertificate(leaf)
	if err != nil {
		return firstTrustLocalIdentityBinding{}, "identity_not_published"
	}
	localSKI, err := hex.DecodeString(ski)
	if err != nil || len(localSKI) != 20 {
		return firstTrustLocalIdentityBinding{}, "identity_not_published"
	}
	return firstTrustLocalIdentityBinding{
		certificateChainDER: cloneNativeProtectedByteSlices(certificate.Certificate),
		providerID:          nativeProtectedProviderID,
		providerVersion:     nativeProtectedProviderVersion,
		sealedBlob:          sealed,
		certificateSPKIHash: sha256.Sum256(spki),
		localSKI:            localSKI,
	}, "identity_durable"
}

func cloneNativeProtectedByteSlices(source [][]byte) [][]byte {
	result := make([][]byte, len(source))
	for index, value := range source {
		result[index] = bytes.Clone(value)
	}
	return result
}

func (provider *nativeProtectedIdentityProvider) restoreAnchor(view eebusstore.ControlView) bool {
	if provider == nil || !view.Control.Present || view.Control.Publication != nil ||
		view.Control.StoreInstance == [sha256.Size]byte{} || view.Control.ControlEpoch == 0 ||
		view.Manifest.Current.Sequence == 0 {
		return false
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if !provider.anchorReady || provider.anchor.anchorIdentity != provider.anchorIdentity ||
		provider.anchor.storeInstance != view.Control.StoreInstance {
		return false
	}
	converted, ok := runtimeControlViewFromStore(view)
	if !ok {
		return false
	}
	anchor := provider.anchor
	if anchor.pending == nil {
		return converted.manifest.current.sequence == anchor.manifestGenerationHighWater &&
			converted.control.controlEpoch == anchor.controlEpochHighWater
	}
	pending := anchor.pending
	previousSelected := firstTrustManifestEqual(converted.manifest, pending.previousManifest) &&
		converted.control.controlEpoch == pending.previousControlEpoch
	targetSelected := firstTrustManifestEqual(converted.manifest, pending.targetManifest) &&
		converted.control.controlEpoch == pending.targetControlEpoch
	return previousSelected || targetSelected
}

func (provider *nativeProtectedIdentityProvider) Open(ctx context.Context) (firstTrustAnchorRecord, string) {
	if provider == nil || provider.witness == nil || nativeProtectedContextCancelled(ctx) {
		return firstTrustAnchorRecord{}, "anchor_unavailable"
	}
	durable, found, err := provider.witness.loadAnchor()
	if err != nil || !found || durable.anchorIdentity != provider.anchorIdentity {
		return firstTrustAnchorRecord{}, "anchor_unavailable"
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.anchor = cloneFirstTrustAnchorRecord(durable)
	provider.anchorReady = true
	return cloneFirstTrustAnchorRecord(provider.anchor), "opened_anchor"
}

func (provider *nativeProtectedIdentityProvider) CompareAndStage(
	ctx context.Context,
	expected firstTrustAnchorRecord,
	pending firstTrustPendingPublication,
) string {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if nativeProtectedContextCancelled(ctx) || !provider.anchorReady || provider.anchor.pending != nil ||
		!firstTrustAnchorRecordEqual(provider.anchor, expected) || pending.storeInstance != provider.anchor.storeInstance {
		return "anchor_not_published"
	}
	target := cloneFirstTrustAnchorRecord(provider.anchor)
	target.pending = firstTrustPendingPointer(pending)
	if provider.witness == nil || provider.witness.compareAndStoreAnchor(&provider.anchor, target) != nil {
		return "anchor_not_published"
	}
	provider.anchor = target
	return "anchor_durable"
}

func (provider *nativeProtectedIdentityProvider) CompareAndFinalize(ctx context.Context, pending firstTrustPendingPublication) string {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if nativeProtectedContextCancelled(ctx) || !provider.anchorReady || provider.anchor.pending == nil ||
		!firstTrustPendingPublicationEqual(*provider.anchor.pending, pending) {
		return "anchor_not_published"
	}
	target := cloneFirstTrustAnchorRecord(provider.anchor)
	target.manifestGenerationHighWater = pending.targetManifest.current.sequence
	target.controlEpochHighWater = pending.targetControlEpoch
	target.pending = nil
	if provider.witness == nil || provider.witness.compareAndStoreAnchor(&provider.anchor, target) != nil {
		return "anchor_not_published"
	}
	provider.anchor = target
	return "anchor_durable"
}

func (provider *nativeProtectedIdentityProvider) CompareAndClear(ctx context.Context, pending firstTrustPendingPublication) string {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if nativeProtectedContextCancelled(ctx) || !provider.anchorReady || provider.anchor.pending == nil ||
		!firstTrustPendingPublicationEqual(*provider.anchor.pending, pending) {
		return "anchor_not_published"
	}
	target := cloneFirstTrustAnchorRecord(provider.anchor)
	target.pending = nil
	if provider.witness == nil || provider.witness.compareAndStoreAnchor(&provider.anchor, target) != nil {
		return "anchor_not_published"
	}
	provider.anchor = target
	return "anchor_durable"
}

func (provider *nativeProtectedIdentityProvider) Create(
	ctx context.Context,
	version uint64,
	storeInstance [sha256.Size]byte,
) (firstTrustAnchorRecord, string) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if nativeProtectedContextCancelled(ctx) || version != firstTrustAnchorVersion ||
		storeInstance == [sha256.Size]byte{} || provider.anchorIdentity == [sha256.Size]byte{} {
		return firstTrustAnchorRecord{}, "anchor_not_published"
	}
	target := firstTrustAnchorRecord{
		version: version, anchorIdentity: provider.anchorIdentity, storeInstance: storeInstance,
	}
	var expected *firstTrustAnchorRecord
	if provider.anchorReady {
		if provider.anchor.pending != nil || provider.anchor.manifestGenerationHighWater != 0 || provider.anchor.controlEpochHighWater != 0 {
			return firstTrustAnchorRecord{}, "anchor_not_published"
		}
		current := cloneFirstTrustAnchorRecord(provider.anchor)
		expected = &current
	}
	if provider.witness == nil || provider.witness.compareAndStoreAnchor(expected, target) != nil {
		return firstTrustAnchorRecord{}, "anchor_not_published"
	}
	provider.anchor = target
	provider.anchorReady = true
	return cloneFirstTrustAnchorRecord(provider.anchor), "anchor_durable"
}

func nativeProtectedContextCancelled(ctx context.Context) bool {
	return ctx != nil && ctx.Err() != nil
}
