package eebusfacade

import (
	"bytes"
	"context"
	"crypto"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusstore"
	shipapi "github.com/enbility/ship-go/api"
	shipcert "github.com/enbility/ship-go/cert"
)

func TestMSP04CRuntimeRecoveryRevocationAndFreshLineageSurviveRealStoreRestart(t *testing.T) {
	root := canonicalRuntimeTempDir(t)
	stateRoot := filepath.Join(root, "state")
	anchor := &runtimeStrictAnchor{}
	remote := msp04cSubject(77)
	remoteSKI := hex.EncodeToString(remote)

	firstService := &fakeRuntimeService{started: make(chan struct{})}
	first := acquireMSP04CRuntimeResources(t, stateRoot, filepath.Join(root, "admin-one"), anchor, firstService)
	if first.coordinator.recoveryState() != "NO_LOCAL_IDENTITY" || first.coordinator.recoveryReason() != "HOST_KEY_UNAVAILABLE" {
		t.Fatal("legacy store did not require unavailable-host-key recovery")
	}
	request := exactRuntimeRepairRequest(first.coordinator, "recover_unavailable_host_key", msp04cOrdinal(800))
	if got := first.coordinator.repair(context.Background(), request); got != "repaired_unpaired" {
		t.Fatalf("legacy host-key repair = %q", got)
	}
	if anchor.createdIdentities() != 1 {
		t.Fatal("host-key recovery did not create exactly one fresh protected identity")
	}
	first.coordinator.mu.Lock()
	if first.coordinator.controlView.control.storeInstance != first.coordinator.anchorRecord.storeInstance || first.coordinator.controlView.control.controlEpoch == 0 {
		first.coordinator.mu.Unlock()
		t.Fatal("host-key recovery did not transactionally bind control and anchor state")
	}
	first.coordinator.mu.Unlock()

	pairRuntimeRemote(t, first, remoteSKI, 81)
	first.coordinator.mu.Lock()
	if len(first.coordinator.controlView.associations) != 1 {
		first.coordinator.mu.Unlock()
		t.Fatal("real store pairing did not persist exactly one association")
	}
	association := first.coordinator.controlView.associations[0]
	manifest := cloneFirstTrustManifest(first.coordinator.controlView.manifest)
	controlEpoch := first.coordinator.controlView.control.controlEpoch
	lineage := first.coordinator.controlView.control.associationLineage
	first.coordinator.mu.Unlock()
	revocation := firstTrustRevocationRequest{
		operationID: msp04cOrdinal(900), associationRef: association.reference, associationLineage: lineage,
		expectedGeneration: manifest.current, expectedManifestEpoch: manifest.epoch,
		expectedManifestSHA256: manifest.sha256, expectedControlEpoch: controlEpoch,
	}
	if got := first.coordinator.revoke(context.Background(), revocation); got != "revoked" {
		t.Fatalf("real AssociationBridge revocation = %q", got)
	}
	first.coordinator.mu.Lock()
	if len(first.coordinator.controlView.control.tombstones) != 1 || first.coordinator.controlView.control.tombstones[0].effectiveGeneration.sequence == 0 {
		first.coordinator.mu.Unlock()
		t.Fatal("real store did not mechanically bind the effective tombstone generation")
	}
	first.coordinator.mu.Unlock()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	revokedService := &fakeRuntimeService{started: make(chan struct{})}
	revoked := acquireMSP04CRuntimeResources(t, stateRoot, filepath.Join(root, "admin-two"), anchor, revokedService)
	if revoked.coordinator.recoveryState() != "REVOKED" || revoked.coordinator.recoveryReason() != "REVOKED_ASSOCIATION" {
		t.Fatalf("revocation restart = %s/%s", revoked.coordinator.recoveryState(), revoked.coordinator.recoveryReason())
	}
	if len(revokedService.registered) != 0 {
		t.Fatal("revoked restart registered a remote")
	}
	pairRuntimeRemote(t, revoked, remoteSKI, 82)
	revoked.coordinator.mu.Lock()
	freshLineage := revoked.coordinator.controlView.control.associationLineage
	revoked.coordinator.mu.Unlock()
	if freshLineage == lineage {
		t.Fatal("post-revocation OOB confirmation reused the revoked lineage")
	}
	if err := revoked.Close(); err != nil {
		t.Fatal(err)
	}

	trustedService := &fakeRuntimeService{started: make(chan struct{})}
	trusted := acquireMSP04CRuntimeResources(t, stateRoot, filepath.Join(root, "admin-three"), anchor, trustedService)
	if trusted.coordinator.recoveryState() != "PAIRED_TRUSTED" {
		t.Fatal("historical tombstone dominated a valid fresh-lineage association")
	}
	if err := trusted.Close(); err != nil {
		t.Fatal(err)
	}

	anchor.loseSigningIdentity()
	unavailableService := &fakeRuntimeService{started: make(chan struct{})}
	unavailable := acquireMSP04CRuntimeResources(t, stateRoot, filepath.Join(root, "admin-four"), anchor, unavailableService)
	if unavailable.coordinator.recoveryState() != "NO_LOCAL_IDENTITY" || unavailable.coordinator.recoveryReason() != "HOST_KEY_UNAVAILABLE" {
		t.Fatal("real store did not remain available for exact unavailable-key recovery")
	}
	keyRepair := exactRuntimeRepairRequest(unavailable.coordinator, "recover_unavailable_host_key", msp04cOrdinal(950))
	if got := unavailable.coordinator.repair(context.Background(), keyRepair); got != "repaired_unpaired" {
		t.Fatalf("unavailable protected-key repair = %q", got)
	}
	if anchor.createdIdentities() != 2 {
		t.Fatal("unavailable-key repair did not create a second fresh protected identity")
	}
	if err := unavailable.Close(); err != nil {
		t.Fatal(err)
	}
	recoveredService := &fakeRuntimeService{started: make(chan struct{})}
	recovered := acquireMSP04CRuntimeResources(t, stateRoot, filepath.Join(root, "admin-five"), anchor, recoveredService)
	if recovered.coordinator.recoveryState() != "UNPAIRED_LOCKED" || len(recoveredService.registered) != 0 {
		t.Fatal("unavailable-key recovery reloaded inherited trust")
	}
	if err := recovered.Close(); err != nil {
		t.Fatal(err)
	}

	anchor.forceStoreInstance(msp04cOrdinal(999))
	cloneService := &fakeRuntimeService{started: make(chan struct{})}
	cloned := acquireMSP04CRuntimeResources(t, stateRoot, filepath.Join(root, "admin-six"), anchor, cloneService)
	if cloned.coordinator.recoveryState() != "QUARANTINED" || cloned.coordinator.recoveryReason() != "CLONE_DETECTED" {
		t.Fatal("restored store did not classify the host-anchor binding conflict as a clone")
	}
	if len(cloneService.registered) != 0 {
		t.Fatal("clone-classified restart registered a remote")
	}
	if err := cloned.Close(); err != nil {
		t.Fatal(err)
	}
}

func acquireMSP04CRuntimeResources(t *testing.T, stateRoot, adminRoot string, anchor *runtimeStrictAnchor, service *fakeRuntimeService) *runtimeFirstTrustResources {
	t.Helper()
	reader := newRuntimeServiceReader(nil)
	dependencies := defaultRuntimeDependencies
	dependencies.now = time.Now
	resources, err := acquireRuntimeFirstTrust(
		context.Background(), RuntimeConfig{StateRoot: stateRoot},
		runtimeMaterial{firstTrust: &runtimeFirstTrustAuthorization{
			adminRuntimeDir: adminRoot, hostAnchor: anchor, identityProvider: anchor,
			keyProviders: []eebusstore.KeyProviderBinding{anchor.keyBinding()},
		}},
		service, reader, dependencies,
	)
	if err != nil {
		t.Fatalf("acquireRuntimeFirstTrust(%s): %v", filepath.Base(adminRoot), err)
	}
	return resources
}

func exactRuntimeRepairRequest(coordinator *firstTrustCoordinator, kind string, operationID [32]byte) firstTrustRepairRequest {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return firstTrustRepairRequest{
		operationID: operationID, kind: kind, scope: msp04cOrdinal(firstTrustOperationOrdinal(operationID) + 1),
		expectedState: coordinator.recovery, expectedReason: coordinator.recoveryReasonCode,
		expectedManifest: cloneFirstTrustManifest(coordinator.controlView.manifest), expectedControlEpoch: coordinator.controlView.control.controlEpoch,
		expectedAnchorVersion:     coordinator.anchorRecord.version,
		expectedManifestHighWater: coordinator.anchorRecord.manifestGenerationHighWater,
		expectedControlHighWater:  coordinator.anchorRecord.controlEpochHighWater,
		nextRepairSequence:        coordinator.controlView.control.repairSequence + 1,
	}
}

func pairRuntimeRemote(t *testing.T, resources *runtimeFirstTrustResources, remoteSKI string, connection uint64) {
	t.Helper()
	if got := resources.coordinator.openPairingWindow(context.Background(), msp04cText(connection+100), time.Minute); got != "open_empty" {
		t.Fatalf("open pairing window = %q", got)
	}
	resources.facade.ServiceShipIDUpdate(remoteSKI, msp04cText(connection+200))
	resources.facade.RemoteSKIConnected(nil, remoteSKI)
	resources.facade.ServicePairingDetailUpdate(remoteSKI, shipapi.NewConnectionStateDetail(shipapi.ConnectionStateReceivedPairingRequest, nil))
	proof, nonce, expiry, candidateConnection, generation, complete, ok := resources.coordinator.candidate()
	if !ok || !complete {
		t.Fatal("runtime pairing did not produce a complete candidate")
	}
	if got := resources.coordinator.confirm(context.Background(), msp04cText(connection+300), proof, nonce, expiry, candidateConnection, generation); got != "trusted" {
		t.Fatalf("runtime OOB confirmation = %q", got)
	}
}

type runtimeStrictAnchor struct {
	mu              sync.Mutex
	record          firstTrustAnchorRecord
	available       bool
	anchorSequence  uint64
	signingSequence uint64
	signer          crypto.Signer
	sealedBlob      []byte
	spki            []byte
}

func (anchor *runtimeStrictAnchor) Open(context.Context) (firstTrustAnchorRecord, string) {
	anchor.mu.Lock()
	defer anchor.mu.Unlock()
	if !anchor.available {
		return firstTrustAnchorRecord{}, "anchor_unavailable"
	}
	return cloneFirstTrustAnchorRecord(anchor.record), "opened_anchor"
}

func (anchor *runtimeStrictAnchor) CompareAndStage(_ context.Context, expected firstTrustAnchorRecord, pending firstTrustPendingPublication) string {
	anchor.mu.Lock()
	defer anchor.mu.Unlock()
	if !anchor.available || !firstTrustAnchorRecordEqual(anchor.record, expected) || anchor.record.pending != nil || pending.storeInstance != anchor.record.storeInstance {
		return "anchor_not_published"
	}
	anchor.record.pending = firstTrustPendingPointer(pending)
	return "anchor_durable"
}

func (anchor *runtimeStrictAnchor) CompareAndFinalize(_ context.Context, pending firstTrustPendingPublication) string {
	anchor.mu.Lock()
	defer anchor.mu.Unlock()
	if !anchor.available || anchor.record.pending == nil || !firstTrustPendingPublicationEqual(*anchor.record.pending, pending) {
		return "anchor_not_published"
	}
	anchor.record.manifestGenerationHighWater = pending.targetManifest.current.sequence
	anchor.record.controlEpochHighWater = pending.targetControlEpoch
	anchor.record.pending = nil
	return "anchor_durable"
}

func (anchor *runtimeStrictAnchor) CompareAndClear(_ context.Context, pending firstTrustPendingPublication) string {
	anchor.mu.Lock()
	defer anchor.mu.Unlock()
	if !anchor.available || anchor.record.pending == nil || !firstTrustPendingPublicationEqual(*anchor.record.pending, pending) {
		return "anchor_not_published"
	}
	anchor.record.pending = nil
	return "anchor_durable"
}

func (anchor *runtimeStrictAnchor) Create(_ context.Context, version uint64, storeInstance [32]byte) (firstTrustAnchorRecord, string) {
	anchor.mu.Lock()
	defer anchor.mu.Unlock()
	if version != firstTrustAnchorVersion || storeInstance == [32]byte{} || anchor.record.pending != nil {
		return firstTrustAnchorRecord{}, "anchor_not_published"
	}
	anchor.anchorSequence++
	anchor.record = firstTrustAnchorRecord{
		version: version, anchorIdentity: msp04cOrdinal(10_000 + anchor.anchorSequence), storeInstance: storeInstance,
	}
	anchor.available = true
	return cloneFirstTrustAnchorRecord(anchor.record), "anchor_durable"
}

func (anchor *runtimeStrictAnchor) CreateSigningIdentity(context.Context) (firstTrustLocalIdentityBinding, string) {
	anchor.mu.Lock()
	defer anchor.mu.Unlock()
	anchor.signingSequence++
	certificate, err := shipcert.CreateCertificate("", "Helianthus", "RO", "runtime-recovery")
	if err != nil || len(certificate.Certificate) == 0 {
		return firstTrustLocalIdentityBinding{}, "identity_not_published"
	}
	parsed, err := x509.ParseCertificate(certificate.Certificate[0])
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
	ski, err := shipcert.SkiFromCertificate(parsed)
	if err != nil {
		return firstTrustLocalIdentityBinding{}, "identity_not_published"
	}
	localSKI, err := hex.DecodeString(ski)
	if err != nil {
		return firstTrustLocalIdentityBinding{}, "identity_not_published"
	}
	sealed := msp04cOrdinal(20_000 + anchor.signingSequence)
	digest := sha256.Sum256(spki)
	anchor.signer = signer
	anchor.sealedBlob = append([]byte(nil), sealed[:]...)
	anchor.spki = append([]byte(nil), spki...)
	return firstTrustLocalIdentityBinding{
		certificateChainDER: certificate.Certificate, providerID: "runtime-test-anchor", providerVersion: 1,
		sealedBlob: append([]byte(nil), anchor.sealedBlob...), certificateSPKIHash: digest, localSKI: localSKI,
	}, "identity_durable"
}

func (anchor *runtimeStrictAnchor) Probe(providerID string, version uint64) error {
	anchor.mu.Lock()
	defer anchor.mu.Unlock()
	if providerID != "runtime-test-anchor" || version != 1 || anchor.signer == nil {
		return errors.New("provider unavailable")
	}
	return nil
}

func (anchor *runtimeStrictAnchor) Validate(sealedBlob, expectedSPKI []byte) error {
	anchor.mu.Lock()
	defer anchor.mu.Unlock()
	if anchor.signer == nil || !bytes.Equal(sealedBlob, anchor.sealedBlob) || !bytes.Equal(expectedSPKI, anchor.spki) {
		return errors.New("binding mismatch")
	}
	return nil
}

func (anchor *runtimeStrictAnchor) Unseal(sealedBlob []byte) (crypto.Signer, error) {
	anchor.mu.Lock()
	defer anchor.mu.Unlock()
	if anchor.signer == nil || !bytes.Equal(sealedBlob, anchor.sealedBlob) {
		return nil, errors.New("key unavailable")
	}
	return anchor.signer, nil
}

func (anchor *runtimeStrictAnchor) keyBinding() eebusstore.KeyProviderBinding {
	return eebusstore.KeyProviderBinding{ID: "runtime-test-anchor", Version: 1, Provider: anchor}
}

func (anchor *runtimeStrictAnchor) createdIdentities() uint64 {
	anchor.mu.Lock()
	defer anchor.mu.Unlock()
	return anchor.signingSequence
}

func (anchor *runtimeStrictAnchor) forceStoreInstance(storeInstance [32]byte) {
	anchor.mu.Lock()
	anchor.record.storeInstance = storeInstance
	anchor.mu.Unlock()
}

func (anchor *runtimeStrictAnchor) loseSigningIdentity() {
	anchor.mu.Lock()
	anchor.signer = nil
	anchor.sealedBlob = nil
	anchor.spki = nil
	anchor.mu.Unlock()
}
