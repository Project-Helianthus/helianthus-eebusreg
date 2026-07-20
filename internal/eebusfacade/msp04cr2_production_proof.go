package eebusfacade

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	eebusapi "github.com/Project-Helianthus/helianthus-eebus-go/api"
	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusservicebridge"
	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusstore"
	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
	shipcert "github.com/Project-Helianthus/helianthus-ship-go/cert"
	shiphub "github.com/Project-Helianthus/helianthus-ship-go/hub"
)

type msp04cr2ProofWireBinding struct {
	AttemptID    string `json:"attempt_id"`
	Scope        string `json:"scope"`
	ControlEpoch uint64 `json:"control_epoch"`
	Path         string `json:"path"`
	ContextID    string `json:"context_id,omitempty"`
}

type msp04cr2ProofWireResult struct {
	Composition      string                     `json:"composition"`
	Scenario         string                     `json:"scenario"`
	Reservations     []msp04cr2ProofWireBinding `json:"reservations"`
	Permits          []msp04cr2ProofWireBinding `json:"permits"`
	Requests         []string                   `json:"requests"`
	Accepts          []string                   `json:"accepts"`
	CallbackRejected bool                       `json:"callback_rejected"`
}

func RunMSP04CR2ProductionProof(
	ctx context.Context,
	scenario string,
	stateRoot string,
	remoteSKI string,
	endpointHost string,
	endpointPort uint16,
	selectedPath string,
	observeNetwork func() ([]string, []string),
	releaseNetwork func(),
) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateMSP04CR2ProductionProof(
		scenario, stateRoot, remoteSKI, endpointHost, endpointPort, selectedPath, observeNetwork, releaseNetwork,
	); err != nil {
		return nil, err
	}
	canonicalStateRoot, err := filepath.EvalSymlinks(stateRoot)
	if err != nil {
		return nil, fmt.Errorf("canonicalize proof store root: %w", err)
	}
	baselineRequests, baselineAccepts := observeNetwork()
	observeProofNetwork := func() ([]string, []string) {
		requests, accepts := observeNetwork()
		if len(requests) < len(baselineRequests) || len(accepts) < len(baselineAccepts) {
			return nil, nil
		}
		return append([]string(nil), requests[len(baselineRequests):]...), append([]string(nil), accepts[len(baselineAccepts):]...)
	}

	anchor := &msp04cr2ProofAnchor{}
	storeBridge, storeOutcome := eebusstore.OpenAssociationBridge(
		canonicalStateRoot,
		[]eebusstore.KeyProviderBinding{{ID: msp04cr2ProofProviderID, Version: 1, Provider: anchor}},
	)
	if storeBridge == nil {
		return nil, fmt.Errorf("open canonical proof store: %s", storeOutcome)
	}
	store := &runtimeControlBridge{bridge: storeBridge}
	clock := &msp04cr2ProofClock{}
	coordinator := newFirstTrustCoordinatorWithRecovery(
		time.Now,
		clock.now,
		rand.Reader,
		store,
		anchor,
		nil,
		firstTrustBackoffPolicy{
			base: firstTrustBackoffBase, exponentCap: firstTrustBackoffExponentCap,
			maximum: firstTrustBackoffMaximum, attemptMaximum: firstTrustAttemptMaximum,
		},
	)
	coordinator.identityProvider = anchor
	closed := false
	closeComposition := func() error {
		if closed {
			return nil
		}
		closed = true
		coordinator.shutdown()
		return store.Close()
	}
	defer closeComposition()

	if outcome := coordinator.reopenWithRecovery(ctx); outcome == "reopen_cancelled" || outcome == "reopen_in_progress" || outcome == "store_unavailable" {
		return nil, fmt.Errorf("reopen canonical proof store: %s", outcome)
	}
	if outcome := coordinator.repair(ctx, msp04cr2ProofRepairRequest(
		coordinator, "recover_unavailable_host_key", msp04cr2ProofOrdinal(10_000), [32]byte{},
	)); outcome != "repaired_unpaired" {
		return nil, fmt.Errorf("initialize canonical proof identity: %s", outcome)
	}
	trustService := &msp04cr2ProofTrustService{}
	trustFacade, err := newFirstTrustFacade(trustService, coordinator)
	if err != nil {
		return nil, fmt.Errorf("initialize proof pairing registration: %w", err)
	}
	coordinator.mu.Lock()
	coordinator.effects = trustFacade
	coordinator.mu.Unlock()
	if err := msp04cr2PairProofRemote(ctx, coordinator, trustFacade, remoteSKI); err != nil {
		return nil, err
	}

	bridge := newFirstTrustOutgoingAttemptBridge(&runtimeFirstTrustResources{coordinator: coordinator})
	recorder := newMSP04CR2ProofRecorder()
	bridge.bindObserver(recorder)
	reader := eebusservicebridge.NewServiceWithOutgoingAttemptBridge(
		nil,
		&msp04cr2ProofServiceReader{},
		eebusservicebridge.OutgoingAttemptBridgeConfiguration{Gate: bridge, Sink: bridge},
	)
	if reader == nil {
		return nil, errors.New("released eebus-go proof composition is unavailable")
	}
	bridge.bindLifecycle(reader)

	localCertificate, err := shipcert.CreateCertificate("", "Helianthus", "RO", "msp04cr2-production-proof")
	if err != nil || len(localCertificate.Certificate) == 0 {
		return nil, errors.New("create proof SHIP identity")
	}
	localLeaf, err := x509.ParseCertificate(localCertificate.Certificate[0])
	if err != nil {
		return nil, errors.New("parse proof SHIP identity")
	}
	localSKI, err := shipcert.SkiFromCertificate(localLeaf)
	if err != nil {
		return nil, errors.New("derive proof SHIP identity")
	}
	localService := shipapi.NewServiceDetails(localSKI)
	localService.SetShipID("msp04cr2-production-proof")
	releasedHub := shiphub.NewHub(reader, &msp04cr2ProofMDNS{}, 0, localCertificate, localService)
	if err := releasedHub.SetOutgoingAttemptGate(bridge); err != nil {
		return nil, fmt.Errorf("install production outgoing gate: %w", err)
	}
	hubClosed := false
	closeHub := func() {
		if !hubClosed {
			hubClosed = true
			releasedHub.Shutdown()
		}
	}
	defer closeHub()

	releasedHub.RegisterRemoteSKI(remoteSKI)
	releasedHub.ServiceForSKI(remoteSKI).ConnectionStateDetail().SetState(shipapi.ConnectionStateQueued)
	if err := msp04cr2ConfigureProofScenario(ctx, scenario, coordinator, clock, remoteSKI, endpointHost, endpointPort, selectedPath); err != nil {
		return nil, err
	}

	entry := &shipapi.MdnsEntry{
		Name:       "msp04cr2-production-peer",
		Ski:        remoteSKI,
		Identifier: "msp04cr2-production-peer",
		Path:       selectedPath,
		Host:       endpointHost,
		Port:       int(endpointPort),
	}
	callbackRejected := false
	switch scenario {
	case "callback_only":
		bridge.OutgoingAttemptConnectionClosed(remoteSKI, false, shipapi.OutgoingAttemptMetadata{
			AttemptID: strings.Repeat("0", 64), Scope: strings.Repeat("0", 64), ControlEpoch: 1,
		})
		callbackRejected = recorder.empty()
	case "permit":
		releasedHub.ReportMdnsEntries(map[string]*shipapi.MdnsEntry{remoteSKI: entry}, true)
		if err := msp04cr2WaitForNetwork(ctx, observeProofNetwork, 1, 1); err != nil {
			return nil, err
		}
	case "fallback":
		releasedHub.ReportMdnsEntries(map[string]*shipapi.MdnsEntry{remoteSKI: entry}, true)
		if err := msp04cr2WaitForNetwork(ctx, observeProofNetwork, 2, 1); err != nil {
			return nil, err
		}
	case "reconnect":
		releasedHub.ReportMdnsEntries(map[string]*shipapi.MdnsEntry{remoteSKI: entry}, true)
		if err := msp04cr2WaitForNetwork(ctx, observeProofNetwork, 1, 1); err != nil {
			return nil, err
		}
		if err := msp04cr2WaitForAttemptTerminal(ctx, coordinator); err != nil {
			return nil, err
		}
		clock.advance(firstTrustBackoffBase)
		releasedHub.ServiceForSKI(remoteSKI).ConnectionStateDetail().SetState(shipapi.ConnectionStateQueued)
		releasedHub.ReportMdnsEntries(map[string]*shipapi.MdnsEntry{remoteSKI: entry}, true)
		if err := msp04cr2WaitForNetwork(ctx, observeProofNetwork, 2, 2); err != nil {
			return nil, err
		}
	default:
		releasedHub.ReportMdnsEntries(map[string]*shipapi.MdnsEntry{remoteSKI: entry}, true)
		if err := msp04cr2ObserveDeniedWindow(ctx, observeProofNetwork); err != nil {
			return nil, err
		}
	}
	_, observedAccepts := observeProofNetwork()
	if len(observedAccepts) > 0 {
		releaseNetwork()
		if err := msp04cr2WaitForAttemptTerminal(ctx, coordinator); err != nil {
			return nil, err
		}
	}

	requests, accepts := observeProofNetwork()
	result := recorder.result(scenario, requests, accepts, callbackRejected)
	closeHub()
	bridge.bindObserver(nil)
	if err := closeComposition(); err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

func validateMSP04CR2ProductionProof(
	scenario, stateRoot, remoteSKI, endpointHost string,
	endpointPort uint16,
	selectedPath string,
	observeNetwork func() ([]string, []string),
	releaseNetwork func(),
) error {
	allowed := map[string]bool{
		"permit": true, "reserve_failure": true, "policy_denied": true, "backoff": true,
		"quarantined": true, "revoked": true, "fallback": true, "reconnect": true, "callback_only": true,
	}
	decoded, decodeErr := hex.DecodeString(remoteSKI)
	cleanRoot := filepath.Clean(strings.TrimSpace(stateRoot))
	if !allowed[scenario] || cleanRoot == "." || !filepath.IsAbs(cleanRoot) || decodeErr != nil || len(decoded) != 20 ||
		remoteSKI != strings.ToLower(remoteSKI) || strings.TrimSpace(endpointHost) == "" || endpointPort == 0 ||
		(selectedPath != "" && !strings.HasPrefix(selectedPath, "/")) || len(selectedPath) > 2048 ||
		observeNetwork == nil || releaseNetwork == nil {
		return errors.New("invalid MSP-04C-R2 production proof binding")
	}
	return nil
}

func msp04cr2PairProofRemote(
	ctx context.Context,
	coordinator *firstTrustCoordinator,
	facade *firstTrustFacade,
	remoteSKI string,
) error {
	if outcome := coordinator.openPairingWindow(ctx, "msp04cr2-proof-window", time.Minute); outcome != "open_empty" {
		return fmt.Errorf("open production proof pairing window: %s", outcome)
	}
	facade.ServiceShipIDUpdate(remoteSKI, "msp04cr2-production-peer")
	facade.RemoteSKIConnected(nil, remoteSKI)
	facade.ServicePairingDetailUpdate(remoteSKI, shipapi.NewConnectionStateDetail(shipapi.ConnectionStateReceivedPairingRequest, nil))
	proof, nonce, expiry, connection, generation, complete, ok := coordinator.candidate()
	if !ok || !complete {
		return errors.New("production proof pairing candidate is incomplete")
	}
	if outcome := coordinator.confirm(
		ctx, "msp04cr2-proof-confirm", proof, nonce, expiry, connection, generation,
	); outcome != "trusted" {
		return fmt.Errorf("confirm production proof pairing: %s", outcome)
	}
	facade.ServicePairingDetailUpdate(remoteSKI, shipapi.NewConnectionStateDetail(shipapi.ConnectionStateCompleted, nil))
	return nil
}

func msp04cr2ConfigureProofScenario(
	ctx context.Context,
	scenario string,
	coordinator *firstTrustCoordinator,
	clock *msp04cr2ProofClock,
	remoteSKI, endpointHost string,
	endpointPort uint16,
	selectedPath string,
) error {
	remote, _, _ := decodeFirstTrustSKI(remoteSKI)
	switch scenario {
	case "reserve_failure":
		coordinator.mu.Lock()
		coordinator.random = strings.NewReader("")
		coordinator.mu.Unlock()
	case "policy_denied":
		coordinator.mu.Lock()
		delete(coordinator.trustedRemotes, string(remote))
		coordinator.mu.Unlock()
	case "backoff":
		handle, outcome := coordinator.prepareOutgoingAttempt(ctx, firstTrustOutgoingAttemptRequest{
			remoteSKI: remote,
			endpoint:  firstTrustOutgoingAttemptEndpoint{host: endpointHost, port: endpointPort},
			path:      selectedPath,
		})
		if outcome != "attempt_reserved" || handle == nil {
			return fmt.Errorf("prepare durable proof backoff: %s", outcome)
		}
		if permit, outcome := coordinator.authorizeOutgoingAttempt(ctx, handle); outcome != "attempt_permitted" || permit.decision != "PERMIT" {
			return fmt.Errorf("authorize durable proof backoff: %s", outcome)
		}
		if outcome := coordinator.completeOutgoingAttempt(ctx, handle.metadata, false); outcome != "backoff_active" {
			return fmt.Errorf("charge durable proof backoff: %s", outcome)
		}
	case "quarantined":
		coordinator.mu.Lock()
		coordinator.phase = firstTrustDisabled
		coordinator.recovery = "QUARANTINED"
		coordinator.recoveryReasonCode = "DURABILITY_UNKNOWN"
		coordinator.trustedRemotes = make(map[string]string)
		coordinator.mu.Unlock()
	case "revoked":
		request, ok := msp04cr2ProofRevocationRequest(coordinator, msp04cr2ProofOrdinal(30_000))
		if !ok {
			return errors.New("production proof association is absent before revocation")
		}
		outcome := coordinator.revoke(ctx, request)
		if outcome != "revocation_withdrawal_incomplete" && outcome != "revoked" {
			return fmt.Errorf("durable production proof revocation: %s", outcome)
		}
	}
	return nil
}

func msp04cr2ProofRevocationRequest(
	coordinator *firstTrustCoordinator,
	operationID [32]byte,
) (firstTrustRevocationRequest, bool) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if len(coordinator.controlView.associations) != 1 {
		return firstTrustRevocationRequest{}, false
	}
	association := coordinator.controlView.associations[0]
	return firstTrustRevocationRequest{
		operationID: operationID, associationRef: association.reference, associationLineage: association.lineage,
		expectedGeneration:     coordinator.controlView.manifest.current,
		expectedManifestEpoch:  coordinator.controlView.manifest.epoch,
		expectedManifestSHA256: coordinator.controlView.manifest.sha256,
		expectedControlEpoch:   coordinator.controlView.control.controlEpoch,
	}, true
}

func msp04cr2ProofRepairRequest(
	coordinator *firstTrustCoordinator,
	kind string,
	operationID [32]byte,
	scope [32]byte,
) firstTrustRepairRequest {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return firstTrustRepairRequest{
		operationID: operationID, kind: kind, scope: scope,
		expectedState: coordinator.recovery, expectedReason: coordinator.recoveryReasonCode,
		expectedManifest:          cloneFirstTrustManifest(coordinator.controlView.manifest),
		expectedControlEpoch:      coordinator.controlView.control.controlEpoch,
		expectedAnchorVersion:     coordinator.anchorRecord.version,
		expectedManifestHighWater: coordinator.anchorRecord.manifestGenerationHighWater,
		expectedControlHighWater:  coordinator.anchorRecord.controlEpochHighWater,
		nextRepairSequence:        coordinator.controlView.control.repairSequence + 1,
	}
}

func msp04cr2WaitForNetwork(
	ctx context.Context,
	observe func() ([]string, []string),
	wantRequests, wantAccepts int,
) error {
	return msp04cr2WaitFor(ctx, func() bool {
		requests, accepts := observe()
		return len(requests) >= wantRequests && len(accepts) >= wantAccepts
	}, "production DialContext/accept observations")
}

func msp04cr2WaitForAttemptTerminal(ctx context.Context, coordinator *firstTrustCoordinator) error {
	return msp04cr2WaitFor(ctx, func() bool {
		coordinator.mu.Lock()
		defer coordinator.mu.Unlock()
		return len(coordinator.controlView.control.attempts) == 0
	}, "first production connection terminal callback")
}

func msp04cr2WaitFor(ctx context.Context, ready func() bool, label string) error {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ready() {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for %s: %w", label, ctx.Err())
		case <-ticker.C:
		}
	}
}

func msp04cr2ObserveDeniedWindow(ctx context.Context, observe func() ([]string, []string)) error {
	timer := time.NewTimer(40 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}
	requests, accepts := observe()
	if len(requests) != 0 || len(accepts) != 0 {
		return errors.New("denied production attempt reached the network")
	}
	return nil
}

type msp04cr2ProofRecorder struct {
	mu           sync.Mutex
	reservations []msp04cr2ProofWireBinding
	permits      []msp04cr2ProofWireBinding
	contexts     map[[32]byte]context.Context
	paths        map[[32]byte]string
}

func newMSP04CR2ProofRecorder() *msp04cr2ProofRecorder {
	return &msp04cr2ProofRecorder{
		contexts: make(map[[32]byte]context.Context),
		paths:    make(map[[32]byte]string),
	}
}

func (recorder *msp04cr2ProofRecorder) prepared(
	metadata firstTrustOutgoingAttemptMetadata,
	path string,
	attemptContext context.Context,
) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.contexts[metadata.attemptID] = attemptContext
	recorder.paths[metadata.attemptID] = path
	recorder.reservations = append(recorder.reservations, msp04cr2ProofWireBinding{
		AttemptID: hex.EncodeToString(metadata.attemptID[:]), Scope: hex.EncodeToString(metadata.scope[:]),
		ControlEpoch: metadata.controlEpoch, Path: path,
	})
}

func (recorder *msp04cr2ProofRecorder) authorized(
	metadata firstTrustOutgoingAttemptMetadata,
	attemptContext context.Context,
) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	preparedContext := recorder.contexts[metadata.attemptID]
	contextID := ""
	if preparedContext != nil && preparedContext == attemptContext {
		contextID = fmt.Sprintf("context_%d", len(recorder.permits)+1)
	}
	recorder.permits = append(recorder.permits, msp04cr2ProofWireBinding{
		AttemptID: hex.EncodeToString(metadata.attemptID[:]), Scope: hex.EncodeToString(metadata.scope[:]),
		ControlEpoch: metadata.controlEpoch, Path: recorder.paths[metadata.attemptID], ContextID: contextID,
	})
}

func (recorder *msp04cr2ProofRecorder) empty() bool {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return len(recorder.reservations) == 0 && len(recorder.permits) == 0
}

func (recorder *msp04cr2ProofRecorder) result(
	scenario string,
	requests, accepts []string,
	callbackRejected bool,
) msp04cr2ProofWireResult {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return msp04cr2ProofWireResult{
		Composition:      "released-eebus-go+ship-go+canonical-store",
		Scenario:         scenario,
		Reservations:     append([]msp04cr2ProofWireBinding(nil), recorder.reservations...),
		Permits:          append([]msp04cr2ProofWireBinding(nil), recorder.permits...),
		Requests:         append([]string(nil), requests...),
		Accepts:          append([]string(nil), accepts...),
		CallbackRejected: callbackRejected,
	}
}

type msp04cr2ProofClock struct {
	mu      sync.Mutex
	elapsed time.Duration
}

func (clock *msp04cr2ProofClock) now() time.Duration {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.elapsed
}

func (clock *msp04cr2ProofClock) advance(duration time.Duration) {
	clock.mu.Lock()
	clock.elapsed += duration
	clock.mu.Unlock()
}

type msp04cr2ProofTrustService struct{}

func (*msp04cr2ProofTrustService) SetAutoAccept(bool)                {}
func (*msp04cr2ProofTrustService) RegisterRemoteSKI(string)          {}
func (*msp04cr2ProofTrustService) CancelPairingWithSKI(string)       {}
func (*msp04cr2ProofTrustService) SetPairingRegistration(bool) error { return nil }

type msp04cr2ProofServiceReader struct{}

func (*msp04cr2ProofServiceReader) RemoteSKIConnected(eebusapi.ServiceInterface, string)    {}
func (*msp04cr2ProofServiceReader) RemoteSKIDisconnected(eebusapi.ServiceInterface, string) {}
func (*msp04cr2ProofServiceReader) VisibleRemoteServicesUpdated(eebusapi.ServiceInterface, []shipapi.RemoteService) {
}
func (*msp04cr2ProofServiceReader) ServiceShipIDUpdate(string, string) {}
func (*msp04cr2ProofServiceReader) ServicePairingDetailUpdate(string, *shipapi.ConnectionStateDetail) {
}

type msp04cr2ProofMDNS struct{}

func (*msp04cr2ProofMDNS) Start(shipapi.MdnsReportInterface) error { return nil }
func (*msp04cr2ProofMDNS) Shutdown()                               {}
func (*msp04cr2ProofMDNS) AnnounceMdnsEntry() error                { return nil }
func (*msp04cr2ProofMDNS) UnannounceMdnsEntry()                    {}
func (*msp04cr2ProofMDNS) SetAutoAccept(bool)                      {}
func (*msp04cr2ProofMDNS) RequestMdnsEntries()                     {}

const msp04cr2ProofProviderID = "msp04cr2-proof-anchor"

type msp04cr2ProofAnchor struct {
	mu         sync.Mutex
	record     firstTrustAnchorRecord
	available  bool
	sequence   uint64
	signer     crypto.Signer
	sealedBlob []byte
	spki       []byte
}

func (anchor *msp04cr2ProofAnchor) Open(context.Context) (firstTrustAnchorRecord, string) {
	anchor.mu.Lock()
	defer anchor.mu.Unlock()
	if !anchor.available {
		return firstTrustAnchorRecord{}, "anchor_unavailable"
	}
	return cloneFirstTrustAnchorRecord(anchor.record), "opened_anchor"
}

func (anchor *msp04cr2ProofAnchor) CompareAndStage(
	_ context.Context,
	expected firstTrustAnchorRecord,
	pending firstTrustPendingPublication,
) string {
	anchor.mu.Lock()
	defer anchor.mu.Unlock()
	if !anchor.available || !firstTrustAnchorRecordEqual(anchor.record, expected) || anchor.record.pending != nil ||
		pending.storeInstance != anchor.record.storeInstance {
		return "anchor_not_published"
	}
	anchor.record.pending = firstTrustPendingPointer(pending)
	return "anchor_durable"
}

func (anchor *msp04cr2ProofAnchor) CompareAndFinalize(
	_ context.Context,
	pending firstTrustPendingPublication,
) string {
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

func (anchor *msp04cr2ProofAnchor) CompareAndClear(
	_ context.Context,
	pending firstTrustPendingPublication,
) string {
	anchor.mu.Lock()
	defer anchor.mu.Unlock()
	if !anchor.available || anchor.record.pending == nil || !firstTrustPendingPublicationEqual(*anchor.record.pending, pending) {
		return "anchor_not_published"
	}
	anchor.record.pending = nil
	return "anchor_durable"
}

func (anchor *msp04cr2ProofAnchor) Create(
	_ context.Context,
	version uint64,
	storeInstance [32]byte,
) (firstTrustAnchorRecord, string) {
	anchor.mu.Lock()
	defer anchor.mu.Unlock()
	if version != firstTrustAnchorVersion || storeInstance == [32]byte{} || anchor.record.pending != nil {
		return firstTrustAnchorRecord{}, "anchor_not_published"
	}
	anchor.sequence++
	anchor.record = firstTrustAnchorRecord{
		version: version, anchorIdentity: msp04cr2ProofOrdinal(40_000 + anchor.sequence), storeInstance: storeInstance,
	}
	anchor.available = true
	return cloneFirstTrustAnchorRecord(anchor.record), "anchor_durable"
}

func (anchor *msp04cr2ProofAnchor) CreateSigningIdentity(context.Context) (firstTrustLocalIdentityBinding, string) {
	anchor.mu.Lock()
	defer anchor.mu.Unlock()
	certificate, err := shipcert.CreateCertificate("", "Helianthus", "RO", "msp04cr2-proof-store")
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
	sealed := msp04cr2ProofOrdinal(50_000 + anchor.sequence)
	digest := sha256.Sum256(spki)
	anchor.signer = signer
	anchor.sealedBlob = append([]byte(nil), sealed[:]...)
	anchor.spki = append([]byte(nil), spki...)
	return firstTrustLocalIdentityBinding{
		certificateChainDER: certificate.Certificate,
		providerID:          msp04cr2ProofProviderID,
		providerVersion:     1,
		sealedBlob:          append([]byte(nil), anchor.sealedBlob...),
		certificateSPKIHash: digest,
		localSKI:            localSKI,
	}, "identity_durable"
}

func (anchor *msp04cr2ProofAnchor) Probe(providerID string, version uint64) error {
	anchor.mu.Lock()
	defer anchor.mu.Unlock()
	if providerID != msp04cr2ProofProviderID || version != 1 || anchor.signer == nil {
		return errors.New("proof provider unavailable")
	}
	return nil
}

func (anchor *msp04cr2ProofAnchor) Validate(sealedBlob, expectedSPKI []byte) error {
	anchor.mu.Lock()
	defer anchor.mu.Unlock()
	if anchor.signer == nil || !bytes.Equal(sealedBlob, anchor.sealedBlob) || !bytes.Equal(expectedSPKI, anchor.spki) {
		return errors.New("proof key binding mismatch")
	}
	return nil
}

func (anchor *msp04cr2ProofAnchor) Unseal(sealedBlob []byte) (crypto.Signer, error) {
	anchor.mu.Lock()
	defer anchor.mu.Unlock()
	if anchor.signer == nil || !bytes.Equal(sealedBlob, anchor.sealedBlob) {
		return nil, errors.New("proof key unavailable")
	}
	return anchor.signer, nil
}

func msp04cr2ProofOrdinal(value uint64) [32]byte {
	var result [32]byte
	for index := 0; index < 8; index++ {
		result[len(result)-1-index] = byte(value >> (index * 8))
	}
	return result
}

var _ firstTrustOutgoingAttemptObserver = (*msp04cr2ProofRecorder)(nil)
var _ firstTrustService = (*msp04cr2ProofTrustService)(nil)
var _ eebusapi.ServiceReaderInterface = (*msp04cr2ProofServiceReader)(nil)
var _ shipapi.MdnsInterface = (*msp04cr2ProofMDNS)(nil)
var _ firstTrustAnchorProvider = (*msp04cr2ProofAnchor)(nil)
var _ firstTrustIdentityProvider = (*msp04cr2ProofAnchor)(nil)
var _ eebusstore.KeyProvider = (*msp04cr2ProofAnchor)(nil)
