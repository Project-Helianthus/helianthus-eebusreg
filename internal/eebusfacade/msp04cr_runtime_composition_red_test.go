package eebusfacade

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusstore"
	eebusapi "github.com/enbility/eebus-go/api"
	shipapi "github.com/enbility/ship-go/api"
	shipcert "github.com/enbility/ship-go/cert"
	spineapi "github.com/enbility/spine-go/api"
)

func TestMSP04CRPairingCallbackPersistsRetryCheckpointAndRestartArm(t *testing.T) {
	fixture := newMSP04CRRuntimeFixture(t, 301)
	clock := &msp04crMonotonicClock{now: 20 * time.Second}
	service := newMSP04CRService()
	backend, reader := fixture.acquire(t, service, "retry-one")
	fixture.recoverUnavailableHostKey(t, backend)

	coordinator := backend.firstTrust.coordinator
	coordinator.mu.Lock()
	coordinator.monotonicNow = clock.Now
	coordinator.backoffPolicy = firstTrustBackoffPolicy{
		base: 3 * time.Second, exponentCap: 2, maximum: 10 * time.Second, attemptMaximum: 4,
	}
	coordinator.mu.Unlock()
	if got := coordinator.openPairingWindow(context.Background(), msp04cText(302), time.Minute); got != "open_empty" {
		t.Fatalf("open pairing window = %q", got)
	}

	service.clearEvents()
	reader.RemoteSKIConnected(nil, fixture.remoteSKI)
	reader.ServicePairingDetailUpdate(fixture.remoteSKI, shipapi.NewConnectionStateDetail(shipapi.ConnectionStateReceivedPairingRequest, nil))
	scope, state, reason, count, remaining, ok := soleMSP04CRRetryRecord(coordinator)
	if !ok || state != "RETRY_READY" || reason != "RETRYABLE_FAILURE" || count != 0 || remaining != 0 {
		t.Fatalf("real callback admission retry tuple = %s,%d,%s,%t, want RETRY_READY,0,0,true", state, count, remaining, ok)
	}
	if _, _, _, _, _, _, candidate := coordinator.candidate(); !candidate {
		t.Fatal("pairing side effect ran without a coordinator-authorized candidate")
	}

	reader.ServicePairingDetailUpdate(fixture.remoteSKI, shipapi.NewConnectionStateDetail(shipapi.ConnectionStateError, errors.New("terminal")))
	_, state, reason, count, remaining, ok = soleMSP04CRRetryRecord(coordinator)
	if !ok || state != "BACKOFF_ACTIVE" || reason != "RETRYABLE_FAILURE" || count != 1 || remaining != 3*time.Second {
		t.Fatalf("real callback failure retry tuple = %s,%d,%s,%t, want BACKOFF_ACTIVE,1,3s,true", state, count, remaining, ok)
	}
	clock.Advance(2 * time.Second)
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}

	restartedService := newMSP04CRService()
	restarted, restartedReader := fixture.acquire(t, restartedService, "retry-two")
	defer restarted.Close()
	restartedCoordinator := restarted.firstTrust.coordinator
	restartedScope, state, reason, count, remaining, ok := soleMSP04CRRetryRecord(restartedCoordinator)
	if !ok || restartedScope != scope || state != "BACKOFF_ACTIVE" || reason != "RETRYABLE_FAILURE" || count != 1 || remaining != time.Second {
		t.Fatalf("restart retry tuple = %s,%d,%s,%t, want BACKOFF_ACTIVE,1,1s,true", state, count, remaining, ok)
	}
	restartedCoordinator.mu.Lock()
	arm, armed := restartedCoordinator.retryArms[scope]
	restartedCoordinator.mu.Unlock()
	if !armed || arm.deadline-arm.armedAt != time.Second {
		t.Fatalf("restart monotonic arm = %+v,%t, want one-second rearm", arm, armed)
	}

	restartedService.clearEvents()
	restartedReader.ServicePairingDetailUpdate(fixture.remoteSKI, shipapi.NewConnectionStateDetail(shipapi.ConnectionStateReceivedPairingRequest, nil))
	_, state, reason, count, remaining, _ = soleMSP04CRRetryRecord(restartedCoordinator)
	if state != "BACKOFF_ACTIVE" || reason != "RETRYABLE_FAILURE" || count != 1 || remaining != time.Second {
		t.Fatal("pre-deadline callback changed durable retry state")
	}
	if !slices.Contains(restartedService.eventsSnapshot(), "cancel_pairing") {
		t.Fatal("pre-deadline callback was not denied before handshake side effects")
	}
	if slices.Contains(restartedService.eventsSnapshot(), "register_remote") {
		t.Fatal("backoff restart registered the configured remote")
	}

}

func TestMSP04CRPairingCallbackEntersTerminalHoldAtAttemptLimit(t *testing.T) {
	fixture := newMSP04CRRuntimeFixture(t, 311)
	service := newMSP04CRService()
	backend, reader := fixture.acquire(t, service, "terminal")
	defer backend.Close()
	fixture.recoverUnavailableHostKey(t, backend)

	coordinator := backend.firstTrust.coordinator
	clock := &msp04crMonotonicClock{}
	coordinator.mu.Lock()
	coordinator.monotonicNow = clock.Now
	coordinator.backoffPolicy = firstTrustBackoffPolicy{
		base: 3 * time.Second, exponentCap: 2, maximum: 10 * time.Second, attemptMaximum: 4,
	}
	coordinator.mu.Unlock()
	if got := coordinator.openPairingWindow(context.Background(), msp04cText(312), time.Minute); got != "open_empty" {
		t.Fatalf("open pairing window = %q", got)
	}

	wantStates := []string{"BACKOFF_ACTIVE", "BACKOFF_ACTIVE", "BACKOFF_ACTIVE", "ADMIN_HOLD"}
	wantReasons := []string{"RETRYABLE_FAILURE", "RETRYABLE_FAILURE", "RETRYABLE_FAILURE", "HANDSHAKE_ATTEMPT_LIMIT"}
	wantDelays := []time.Duration{3 * time.Second, 6 * time.Second, 10 * time.Second, 0}
	advance := []time.Duration{0, 3 * time.Second, 6 * time.Second, 10 * time.Second}
	vector := []string{"retry_ready_0"}
	postFailureLabels := []string{"backoff_1_3s", "backoff_2_6s", "backoff_3_10s", "admin_hold_4"}
	readyLabels := []string{"", "retry_ready_1", "retry_ready_2", "retry_ready_3"}
	for index := range wantStates {
		if advance[index] != 0 {
			clock.Advance(advance[index])
			reader.RemoteSKIDisconnected(nil, fixture.remoteSKI)
		}
		reader.RemoteSKIConnected(nil, fixture.remoteSKI)
		reader.ServicePairingDetailUpdate(fixture.remoteSKI, shipapi.NewConnectionStateDetail(shipapi.ConnectionStateReceivedPairingRequest, nil))
		_, readyState, _, readyCount, readyRemaining, ready := soleMSP04CRRetryRecord(coordinator)
		if !ready || readyState != "RETRY_READY" || readyCount != uint64(index) || readyRemaining != 0 {
			t.Fatalf("callback admission %d tuple = %s,%d,%s,%t", index+1, readyState, readyCount, readyRemaining, ready)
		}
		if index != 0 {
			vector = append(vector, readyLabels[index])
		}
		reader.ServicePairingDetailUpdate(fixture.remoteSKI, shipapi.NewConnectionStateDetail(shipapi.ConnectionStateError, errors.New("terminal")))
		_, state, reason, count, remaining, ok := soleMSP04CRRetryRecord(coordinator)
		if !ok || state != wantStates[index] || reason != wantReasons[index] || count != uint64(index+1) || remaining != wantDelays[index] {
			t.Fatalf("callback failure %d tuple = %s/%s,%d,%s,%t", index+1, state, reason, count, remaining, ok)
		}
		vector = append(vector, postFailureLabels[index])
	}

	service.clearEvents()
	reader.ServicePairingDetailUpdate(fixture.remoteSKI, shipapi.NewConnectionStateDetail(shipapi.ConnectionStateReceivedPairingRequest, nil))
	_, state, reason, count, remaining, _ := soleMSP04CRRetryRecord(coordinator)
	if state != "ADMIN_HOLD" || reason != "HANDSHAKE_ATTEMPT_LIMIT" || count != 4 || remaining != 0 || !slices.Contains(service.eventsSnapshot(), "cancel_pairing") {
		t.Fatal("terminal hold admitted another callback or changed its saturated tuple")
	}
	vector = append(vector, "terminal_denied")
	artifact := deriveMSP04CRArtifact("EEBUS-G11", state, reason, count, remaining, vector)
	assertMSP04CRArtifactRedacted(t, artifact, fixture)
}

func TestMSP04CRStartupClassifiesBeforeListenerAndBlocksTombstonedConfiguredPeer(t *testing.T) {
	fixture := newMSP04CRRuntimeFixture(t, 321)
	firstService := newMSP04CRService()
	first, reader := fixture.acquire(t, firstService, "startup-one")
	fixture.recoverUnavailableHostKey(t, first)
	fixture.pairRemote(t, first, reader, 322)
	request := exactMSP04CRRevocationRequest(first.firstTrust.coordinator, msp04cOrdinal(323))
	if got := first.firstTrust.coordinator.revoke(context.Background(), request); got != "revoked" {
		t.Fatalf("setup revocation = %q", got)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	fixture.events.clear()
	restartedService := newMSP04CRService()
	restarted, _ := fixture.acquire(t, restartedService, "startup-two")
	defer restarted.Close()
	state := restarted.firstTrust.coordinator.recoveryState()
	reason := restarted.firstTrust.coordinator.recoveryReason()
	events := fixture.events.snapshot()
	bridgeIndex := slices.Index(events, "store_open")
	factoryIndex := slices.Index(events, "service_factory")
	setupIndex := slices.Index(events, "listener_setup")
	if state != "REVOKED" || reason != "REVOKED_ASSOCIATION" {
		t.Errorf("startup classification = %s/%s", state, reason)
	}
	if bridgeIndex < 0 || factoryIndex < 0 || setupIndex < 0 || bridgeIndex > factoryIndex || bridgeIndex > setupIndex {
		t.Errorf("startup order = %v, want store classification before service factory/listener setup", events)
	}
	if slices.Contains(events, "register_remote") || slices.Contains(events, "reconnect") {
		t.Errorf("tombstoned startup effects = %v", events)
	}

	artifact := deriveMSP04CRArtifact("EEBUS-G10", state, reason, 0, 0, events)
	assertMSP04CRArtifactRedacted(t, artifact, fixture)
}

func TestMSP04CRRevocationReturnsOnlyAfterDurableDisconnectAndUnregister(t *testing.T) {
	t.Run("success ordering", func(t *testing.T) {
		fixture := newMSP04CRRuntimeFixture(t, 331)
		service := newMSP04CRService()
		backend, reader := fixture.acquire(t, service, "revoke-success")
		defer backend.Close()
		fixture.recoverUnavailableHostKey(t, backend)
		fixture.pairRemote(t, backend, reader, 332)
		fixture.events.clear()
		service.clearEvents()

		request := exactMSP04CRRevocationRequest(backend.firstTrust.coordinator, msp04cOrdinal(333))
		result := backend.firstTrust.coordinator.revoke(context.Background(), request)
		events := fixture.events.snapshot()
		if result != "revoked" {
			t.Errorf("revocation result = %q", result)
		}
		finalize := slices.Index(events, "anchor_finalize")
		disconnect := slices.Index(events, "disconnect")
		unregister := slices.Index(events, "unregister")
		if finalize < 0 || disconnect <= finalize || unregister <= disconnect {
			t.Errorf("revocation order = %v, want anchor_finalize, disconnect, unregister", events)
		}

		artifact := deriveMSP04CRArtifact("EEBUS-G16", backend.firstTrust.coordinator.recoveryState(), backend.firstTrust.coordinator.recoveryReason(), 0, 0, events)
		assertMSP04CRArtifactRedacted(t, artifact, fixture)
	})

	t.Run("withdrawal failure", func(t *testing.T) {
		fixture := newMSP04CRRuntimeFixture(t, 341)
		service := newMSP04CRService()
		service.panicUnregister = true
		backend, reader := fixture.acquire(t, service, "revoke-failure")
		defer backend.Close()
		fixture.recoverUnavailableHostKey(t, backend)
		fixture.pairRemote(t, backend, reader, 342)
		request := exactMSP04CRRevocationRequest(backend.firstTrust.coordinator, msp04cOrdinal(343))

		result, panicked := invokeMSP04CRRevocation(backend.firstTrust.coordinator, request)
		if panicked {
			t.Fatal("runtime withdrawal failure escaped the coordinator")
		}
		if result == "revoked" {
			t.Fatal("runtime withdrawal failure reported revocation success")
		}
		backend.firstTrust.coordinator.mu.Lock()
		terminalRevoked := slices.ContainsFunc(backend.firstTrust.coordinator.controlView.control.receipts, func(receipt firstTrustDurableReceipt) bool {
			return receipt.operationID == request.operationID && receipt.terminal && receipt.result == "revoked"
		})
		backend.firstTrust.coordinator.mu.Unlock()
		if terminalRevoked {
			t.Fatal("incomplete runtime withdrawal persisted a terminal revoked receipt")
		}
	})
}

type msp04crRuntimeFixture struct {
	root        string
	stateRoot   string
	remoteSKI   string
	certificate tls.Certificate
	localSKI    string
	anchor      *runtimeStrictAnchor
	hostAnchor  *msp04crRecordingAnchor
	events      *msp04crEventLog
}

func newMSP04CRRuntimeFixture(t *testing.T, ordinal uint64) *msp04crRuntimeFixture {
	t.Helper()
	certificate, err := shipcert.CreateCertificate("", "Helianthus", "RO", "runtime-composition")
	if err != nil {
		t.Fatal(err)
	}
	events := &msp04crEventLog{}
	anchor := &runtimeStrictAnchor{}
	return &msp04crRuntimeFixture{
		root: canonicalRuntimeTempDir(t), remoteSKI: hex.EncodeToString(msp04cSubject(ordinal)),
		certificate: certificate, localSKI: certificateSKI(t, certificate), anchor: anchor,
		hostAnchor: &msp04crRecordingAnchor{delegate: anchor, events: events}, events: events,
	}
}

func (fixture *msp04crRuntimeFixture) acquire(t *testing.T, service *msp04crService, admin string) (*serviceBackend, *runtimeServiceReader) {
	t.Helper()
	if fixture.stateRoot == "" {
		fixture.stateRoot = filepath.Join(fixture.root, "state")
	}
	var reader *runtimeServiceReader
	dependencies := defaultRuntimeDependencies
	dependencies.loadMaterial = func(context.Context, string) (runtimeMaterial, error) {
		return runtimeMaterial{
			certificate: fixture.certificate, localSKI: fixture.localSKI, pretrusted: map[string]bool{fixture.remoteSKI: true},
			firstTrust: &runtimeFirstTrustAuthorization{
				adminRuntimeDir: filepath.Join(fixture.root, admin), hostAnchor: fixture.hostAnchor,
				identityProvider: fixture.anchor, keyProviders: []eebusstore.KeyProviderBinding{fixture.anchor.keyBinding()},
			},
		}, nil
	}
	dependencies.newService = func(_ RuntimeConfig, _ runtimeMaterial, callback eebusapi.ServiceReaderInterface) (runtimeService, error) {
		fixture.events.add("service_factory")
		reader = callback.(*runtimeServiceReader)
		service.outerEvents = fixture.events
		service.expectedSKI = fixture.remoteSKI
		return service, nil
	}
	dependencies.openAssociationBridge = func(root string, bindings []eebusstore.KeyProviderBinding) (runtimeAssociationBridge, string) {
		fixture.events.add("store_open")
		return openRuntimeAssociationBridge(root, bindings)
	}
	backend, err := acquireRuntime(context.Background(), RuntimeConfig{
		StateRoot: fixture.stateRoot, Interface: "fixture-interface", ListenPort: 4711,
		Remotes: []RuntimeRemote{{SKI: fixture.remoteSKI}},
	}, dependencies)
	if err != nil {
		t.Fatalf("acquire production runtime: %v", err)
	}
	implementation, ok := backend.(*serviceBackend)
	if !ok || implementation.firstTrust == nil || reader == nil {
		t.Fatal("production runtime did not compose first trust resources and callbacks")
	}
	return implementation, reader
}

func (fixture *msp04crRuntimeFixture) recoverUnavailableHostKey(t *testing.T, backend *serviceBackend) {
	t.Helper()
	request := exactRuntimeRepairRequest(backend.firstTrust.coordinator, "recover_unavailable_host_key", msp04cOrdinal(30_000))
	if got := backend.firstTrust.coordinator.repair(context.Background(), request); got != "repaired_unpaired" {
		t.Fatalf("recover unavailable host key = %q", got)
	}
}

func (fixture *msp04crRuntimeFixture) pairRemote(t *testing.T, backend *serviceBackend, reader *runtimeServiceReader, ordinal uint64) {
	t.Helper()
	coordinator := backend.firstTrust.coordinator
	if got := coordinator.openPairingWindow(context.Background(), msp04cText(ordinal), time.Minute); got != "open_empty" {
		t.Fatalf("open pairing window = %q", got)
	}
	reader.ServiceShipIDUpdate(fixture.remoteSKI, msp04cText(ordinal+1))
	reader.RemoteSKIConnected(nil, fixture.remoteSKI)
	reader.ServicePairingDetailUpdate(fixture.remoteSKI, shipapi.NewConnectionStateDetail(shipapi.ConnectionStateReceivedPairingRequest, nil))
	proof, nonce, expiry, connection, generation, complete, ok := coordinator.candidate()
	if !ok || !complete {
		t.Fatal("production callback did not produce a complete candidate")
	}
	if got := coordinator.confirm(context.Background(), msp04cText(ordinal+2), proof, nonce, expiry, connection, generation); got != "trusted" {
		t.Fatalf("confirm production callback = %q", got)
	}
}

func exactMSP04CRRevocationRequest(coordinator *firstTrustCoordinator, operationID [32]byte) firstTrustRevocationRequest {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	association := coordinator.controlView.associations[0]
	return firstTrustRevocationRequest{
		operationID: operationID, associationRef: association.reference,
		associationLineage:     coordinator.controlView.control.associationLineage,
		expectedGeneration:     coordinator.controlView.manifest.current,
		expectedManifestEpoch:  coordinator.controlView.manifest.epoch,
		expectedManifestSHA256: coordinator.controlView.manifest.sha256,
		expectedControlEpoch:   coordinator.controlView.control.controlEpoch,
	}
}

func soleMSP04CRRetryRecord(coordinator *firstTrustCoordinator) ([32]byte, string, string, uint64, time.Duration, bool) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if len(coordinator.controlView.control.quarantines) != 1 {
		return [32]byte{}, "", "", 0, 0, false
	}
	record := coordinator.controlView.control.quarantines[0]
	return record.scope, record.state, record.reason, record.attemptCount, record.remainingDelay, true
}

func invokeMSP04CRRevocation(coordinator *firstTrustCoordinator, request firstTrustRevocationRequest) (result string, panicked bool) {
	defer func() {
		panicked = recover() != nil
	}()
	return coordinator.revoke(context.Background(), request), false
}

type msp04crService struct {
	mu              sync.Mutex
	events          []string
	outerEvents     *msp04crEventLog
	expectedSKI     string
	panicUnregister bool
}

func newMSP04CRService() *msp04crService { return &msp04crService{} }

func (service *msp04crService) Setup() error {
	service.record("listener_setup")
	return nil
}

func (service *msp04crService) Start()                                          { service.record("listener_start") }
func (service *msp04crService) Shutdown()                                       { service.record("shutdown") }
func (service *msp04crService) SetAutoAccept(bool)                              {}
func (service *msp04crService) UserIsAbleToApproveOrCancelPairingRequests(bool) {}
func (*msp04crService) LocalService() *shipapi.ServiceDetails                   { return nil }
func (*msp04crService) LocalDevice() spineapi.DeviceLocalInterface              { return nil }

func (service *msp04crService) RegisterRemoteSKI(ski string) {
	service.recordExactSKI("register_remote", ski)
}

func (service *msp04crService) CancelPairingWithSKI(ski string) {
	service.recordExactSKI("cancel_pairing", ski)
}

func (service *msp04crService) DisconnectSKI(ski string, _ string) {
	service.recordExactSKI("disconnect", ski)
}

func (service *msp04crService) UnregisterRemoteSKI(ski string) {
	service.recordExactSKI("unregister", ski)
	if service.panicUnregister {
		panic("withdrawal unavailable")
	}
}

func (service *msp04crService) recordExactSKI(event, ski string) {
	if ski != service.expectedSKI {
		service.record(event + "_wrong_peer")
		return
	}
	service.record(event)
}

func (service *msp04crService) record(event string) {
	service.mu.Lock()
	service.events = append(service.events, event)
	service.mu.Unlock()
	if service.outerEvents != nil {
		service.outerEvents.add(event)
	}
}

func (service *msp04crService) clearEvents() {
	service.mu.Lock()
	service.events = nil
	service.mu.Unlock()
}

func (service *msp04crService) eventsSnapshot() []string {
	service.mu.Lock()
	defer service.mu.Unlock()
	return append([]string(nil), service.events...)
}

type msp04crRecordingAnchor struct {
	delegate *runtimeStrictAnchor
	events   *msp04crEventLog
}

func (anchor *msp04crRecordingAnchor) Open(ctx context.Context) (firstTrustAnchorRecord, string) {
	anchor.events.add("anchor_open")
	return anchor.delegate.Open(ctx)
}

func (anchor *msp04crRecordingAnchor) CompareAndStage(ctx context.Context, expected firstTrustAnchorRecord, pending firstTrustPendingPublication) string {
	return anchor.delegate.CompareAndStage(ctx, expected, pending)
}

func (anchor *msp04crRecordingAnchor) CompareAndFinalize(ctx context.Context, pending firstTrustPendingPublication) string {
	result := anchor.delegate.CompareAndFinalize(ctx, pending)
	if result == "anchor_durable" {
		anchor.events.add("anchor_finalize")
	}
	return result
}

func (anchor *msp04crRecordingAnchor) CompareAndClear(ctx context.Context, pending firstTrustPendingPublication) string {
	return anchor.delegate.CompareAndClear(ctx, pending)
}

func (anchor *msp04crRecordingAnchor) Create(ctx context.Context, version uint64, storeInstance [32]byte) (firstTrustAnchorRecord, string) {
	return anchor.delegate.Create(ctx, version, storeInstance)
}

type msp04crEventLog struct {
	mu     sync.Mutex
	events []string
}

func (log *msp04crEventLog) add(event string) {
	log.mu.Lock()
	log.events = append(log.events, event)
	log.mu.Unlock()
}

func (log *msp04crEventLog) clear() {
	log.mu.Lock()
	log.events = nil
	log.mu.Unlock()
}

func (log *msp04crEventLog) snapshot() []string {
	log.mu.Lock()
	defer log.mu.Unlock()
	return append([]string(nil), log.events...)
}

type msp04crMonotonicClock struct {
	mu  sync.Mutex
	now time.Duration
}

func (clock *msp04crMonotonicClock) Now() time.Duration {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *msp04crMonotonicClock) Advance(delta time.Duration) {
	clock.mu.Lock()
	clock.now += delta
	clock.mu.Unlock()
}

type msp04crArtifact struct {
	Gate             string   `json:"gate"`
	Status           string   `json:"status"`
	State            string   `json:"state"`
	Reason           string   `json:"reason,omitempty"`
	Count            uint64   `json:"count,omitempty"`
	RemainingSeconds uint64   `json:"remaining_seconds,omitempty"`
	ObservedEffects  []string `json:"observed_effects"`
}

func deriveMSP04CRArtifact(gate, state, reason string, count uint64, remaining time.Duration, events []string) []byte {
	status := "FAIL"
	switch gate {
	case "EEBUS-G10":
		storeOpen := slices.Index(events, "store_open")
		serviceFactory := slices.Index(events, "service_factory")
		listenerSetup := slices.Index(events, "listener_setup")
		if state == "REVOKED" && reason == "REVOKED_ASSOCIATION" && storeOpen >= 0 && serviceFactory > storeOpen && listenerSetup > storeOpen && !slices.Contains(events, "register_remote") && !slices.Contains(events, "reconnect") {
			status = "PASS"
		}
	case "EEBUS-G11":
		wantVector := []string{
			"retry_ready_0", "backoff_1_3s", "retry_ready_1", "backoff_2_6s",
			"retry_ready_2", "backoff_3_10s", "retry_ready_3", "admin_hold_4", "terminal_denied",
		}
		if state == "ADMIN_HOLD" && reason == "HANDSHAKE_ATTEMPT_LIMIT" && count == 4 && remaining == 0 && slices.Equal(events, wantVector) {
			status = "PASS"
		}
	case "EEBUS-G16":
		finalize := slices.Index(events, "anchor_finalize")
		disconnect := slices.Index(events, "disconnect")
		unregister := slices.Index(events, "unregister")
		if state == "REVOKED" && reason == "REVOKED_ASSOCIATION" && finalize >= 0 && disconnect > finalize && unregister > disconnect {
			status = "PASS"
		}
	}
	payload, _ := json.Marshal(msp04crArtifact{
		Gate: gate, Status: status, State: state, Reason: reason, Count: count,
		RemainingSeconds: uint64(remaining / time.Second), ObservedEffects: append([]string(nil), events...),
	})
	return payload
}

func assertMSP04CRArtifactRedacted(t *testing.T, payload []byte, fixture *msp04crRuntimeFixture) {
	t.Helper()
	t.Logf("MSP04CR_ARTIFACT %s", payload)
	if !strings.Contains(string(payload), `"status":"PASS"`) {
		t.Errorf("executed runtime artifact = %s", payload)
	}
	for _, forbidden := range []string{fixture.remoteSKI, fixture.localSKI, fixture.root, fixture.stateRoot, "terminal", "withdrawal unavailable"} {
		if forbidden != "" && strings.Contains(string(payload), forbidden) {
			t.Fatalf("executed runtime artifact leaked a restricted value category: %s", payload)
		}
	}
}
