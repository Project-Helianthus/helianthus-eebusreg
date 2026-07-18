package eebusfacade

import (
	"bytes"
	"context"
	"crypto"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	shipcert "github.com/Project-Helianthus/helianthus-ship-go/cert"
)

func TestMSP05PProtectedRuntimeMaterialFirstBootAndRestartIdentityContinuity(t *testing.T) {
	root := canonicalRuntimeTempDir(t)
	stateRoot := filepath.Join(root, "state")

	first, err := loadProtectedRuntimeMaterial(context.Background(), stateRoot)
	if err != nil {
		t.Fatalf("first boot protected material: %v", err)
	}
	assertMSP05PIdentityBinding(t, first)

	restarted, err := loadProtectedRuntimeMaterial(context.Background(), stateRoot)
	if err != nil {
		t.Fatalf("restart protected material: %v", err)
	}
	assertMSP05PIdentityBinding(t, restarted)

	if restarted.localSKI != first.localSKI {
		t.Fatalf("local SKI changed across restart: first=%q restarted=%q", first.localSKI, restarted.localSKI)
	}
	if got, want := restarted.certificate.Certificate[0], first.certificate.Certificate[0]; !bytes.Equal(got, want) {
		t.Fatal("leaf certificate changed across same-host restart")
	}
}

func TestMSP05PProtectedRuntimeMaterialPreservesRemoteSHIPAndPairingState(t *testing.T) {
	root := canonicalRuntimeTempDir(t)
	stateRoot := filepath.Join(root, "state")
	remoteSKI := "1111111111111111111111111111111111111111"

	firstMaterial, err := loadProtectedRuntimeMaterial(context.Background(), stateRoot)
	if err != nil {
		t.Fatalf("first boot protected material: %v", err)
	}
	first := acquireMSP05PTrustResources(t, stateRoot, filepath.Join(root, "admin-first"), firstMaterial)
	if got := first.coordinator.state(); got != "PAIRING_CLOSED" {
		t.Fatalf("first boot pairing state = %q, want PAIRING_CLOSED", got)
	}
	if got := first.coordinator.recoveryState(); got != "UNPAIRED_LOCKED" {
		t.Fatalf("first boot recovery state = %q, want UNPAIRED_LOCKED", got)
	}
	pairRuntimeRemote(t, first, remoteSKI, 40)
	first.coordinator.mu.Lock()
	if len(first.coordinator.controlView.associations) != 1 {
		first.coordinator.mu.Unlock()
		t.Fatal("pairing did not publish exactly one durable association")
	}
	wantSHIPID := first.coordinator.controlView.associations[0].service
	first.coordinator.mu.Unlock()
	if err := first.Close(); err != nil {
		t.Fatalf("close first runtime trust resources: %v", err)
	}

	restartedMaterial, err := loadProtectedRuntimeMaterial(context.Background(), stateRoot)
	if err != nil {
		t.Fatalf("restart protected material: %v", err)
	}
	if !restartedMaterial.pretrusted[remoteSKI] {
		t.Fatalf("restart did not retain durable admission for remote SKI %q", remoteSKI)
	}
	restarted := acquireMSP05PTrustResources(t, stateRoot, filepath.Join(root, "admin-restart"), restartedMaterial)
	t.Cleanup(func() {
		if err := restarted.Close(); err != nil {
			t.Errorf("close restarted runtime trust resources: %v", err)
		}
	})
	if got := restarted.coordinator.state(); got != "PAIRING_CLOSED" {
		t.Fatalf("restart pairing state = %q, want PAIRING_CLOSED", got)
	}
	if got := restarted.coordinator.recoveryState(); got != "PAIRED_TRUSTED" {
		t.Fatalf("restart recovery state = %q, want PAIRED_TRUSTED", got)
	}
	restarted.coordinator.mu.Lock()
	defer restarted.coordinator.mu.Unlock()
	associations := restarted.coordinator.controlView.associations
	if len(associations) != 1 || hex.EncodeToString(associations[0].subject) != remoteSKI || associations[0].service != wantSHIPID {
		t.Fatalf("restart association = %#v, want remote SKI and SHIP ID continuity", associations)
	}
}

func TestMSP05PProtectedRuntimeMaterialFormattingIsAlwaysRedacted(t *testing.T) {
	stateRoot := filepath.Join(canonicalRuntimeTempDir(t), "state")
	material, err := loadProtectedRuntimeMaterial(context.Background(), stateRoot)
	if err != nil {
		t.Fatalf("load protected material: %v", err)
	}
	want := "eebusfacade.runtime_material{redacted}"
	formats := map[string]string{
		"%v":  want,
		"%+v": want,
		"%#v": want,
		"%s":  want,
		"%q":  strconv.Quote(want),
	}
	for format, expected := range formats {
		if got := fmt.Sprintf(format, material); got != expected {
			t.Fatalf("format %q = %q, want %q", format, got, expected)
		}
	}
}

func assertMSP05PIdentityBinding(t *testing.T, material runtimeMaterial) {
	t.Helper()
	if err := validateRuntimeMaterial(material); err != nil {
		t.Fatalf("invalid protected runtime material: %v", err)
	}
	leaf, err := x509.ParseCertificate(material.certificate.Certificate[0])
	if err != nil {
		t.Fatalf("parse protected leaf certificate: %v", err)
	}
	signer, ok := material.certificate.PrivateKey.(crypto.Signer)
	if !ok {
		t.Fatalf("protected private key type %T is not a signer", material.certificate.PrivateKey)
	}
	certificatePublic, err := x509.MarshalPKIXPublicKey(leaf.PublicKey)
	if err != nil {
		t.Fatalf("marshal certificate public key: %v", err)
	}
	signerPublic, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil {
		t.Fatalf("marshal signer public key: %v", err)
	}
	if !bytes.Equal(certificatePublic, signerPublic) {
		t.Fatal("protected signer is not bound to the leaf certificate")
	}
	ski, err := shipcert.SkiFromCertificate(leaf)
	if err != nil {
		t.Fatalf("derive certificate SKI: %v", err)
	}
	if material.localSKI != ski {
		t.Fatalf("local SKI %q does not match certificate SKI %q", material.localSKI, ski)
	}
	if _, err := x509.MarshalPKCS8PrivateKey(signer); err == nil {
		t.Fatal("protected signer is exportable as portable PKCS#8 private-key bytes")
	}
	if material.firstTrust == nil || material.firstTrust.hostAnchor == nil || material.firstTrust.identityProvider == nil || len(material.firstTrust.keyProviders) != 1 {
		t.Fatal("protected material is not composed with the existing host anchor and eebusstore key-reference infrastructure")
	}
}

func acquireMSP05PTrustResources(t *testing.T, stateRoot, adminRoot string, material runtimeMaterial) *runtimeFirstTrustResources {
	t.Helper()
	if material.firstTrust == nil {
		t.Fatal("protected material omitted first-trust authorization")
	}
	authorization := *material.firstTrust
	authorization.adminRuntimeDir = adminRoot
	material.firstTrust = &authorization
	service := &fakeRuntimeService{started: make(chan struct{})}
	reader := newRuntimeServiceReader(nil)
	service.disconnected = func(ski string) { reader.RemoteSKIDisconnected(nil, ski) }
	dependencies := defaultRuntimeDependencies
	dependencies.now = time.Now
	resources, err := acquireRuntimeFirstTrust(
		context.Background(), RuntimeConfig{StateRoot: stateRoot}, material, service, reader, dependencies,
	)
	if err != nil {
		t.Fatalf("acquire protected runtime trust resources: %v", err)
	}
	return resources
}
