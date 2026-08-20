package eebusfacade

import (
	"context"
	"encoding/hex"
	"reflect"
	"sync"
	"testing"
	"time"

	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
)

func TestIssue120OfflineAdminUntrustFinalizesWithoutDisconnectCallbackAndSurvivesRestart(t *testing.T) {
	fixture := newMSP04CFixture(t)
	lineage := fixture.store.view.control.associationLineage
	association := msp04cAssociation(1, lineage, true, true, true, true)
	fixture.store.view.associations = []firstTrustAssociationRecord{association}
	service := newIssue120WithdrawalService()
	facade, err := newFirstTrustFacade(service, nil)
	if err != nil {
		t.Fatal(err)
	}
	coordinator := issue120Coordinator(fixture, facade)
	facade.coordinator = coordinator
	coordinator.withdrawalWait = 20 * time.Millisecond
	if got := coordinator.reopen(context.Background()); got != "pairing_closed" {
		t.Fatalf("startup outcome = %q", got)
	}
	bridge := newOperatorAdminV1Bridge(coordinator, service, &msp04cOrdinalReader{next: 1_200})
	snapshot, failure := bridge.snapshotOperatorAdminV1(context.Background())
	requireOperatorAdminV1BridgeSuccess(t, failure)
	if len(snapshot.trusted) != 1 {
		t.Fatalf("trusted rows = %d, want 1", len(snapshot.trusted))
	}

	transition, failure := bridge.untrustOperatorAdminV1(context.Background(), snapshot.trusted[0].reference)
	requireOperatorAdminV1BridgeSuccess(t, failure)
	if !transition.changed || transition.outcome != "revoked" {
		t.Fatalf("offline untrust = %#v, want changed revoked", transition)
	}
	disconnects, unregisters := service.withdrawalCalls()
	if disconnects != 0 || unregisters != 1 {
		t.Fatalf("offline withdrawal calls disconnect/unregister = %d/%d, want 0/1", disconnects, unregisters)
	}
	assertOperatorAdminV1BridgeAssociationRevoked(t, fixture.store.view, association.reference)
	issue120AssertTerminalRevocationReceipt(t, fixture.store.view, association.reference)

	restartedService := newIssue120WithdrawalService()
	restartedFacade, err := newFirstTrustFacade(restartedService, nil)
	if err != nil {
		t.Fatal(err)
	}
	restarted := issue120Coordinator(fixture, restartedFacade)
	restartedFacade.coordinator = restarted
	if got := restarted.reopen(context.Background()); got != "pairing_closed" {
		t.Fatalf("restart outcome = %q", got)
	}
	assertMSP04CState(t, restarted, "REVOKED", "REVOKED_ASSOCIATION")
	if restarted.trusted(association.subject) {
		t.Fatal("offline untrust reloaded durable trust after restart")
	}
}

func TestIssue120ConnectedAdminUntrustStillWaitsForBoundedDisconnectACK(t *testing.T) {
	fixture := newMSP04CFixture(t)
	lineage := fixture.store.view.control.associationLineage
	association := msp04cAssociation(1, lineage, true, true, true, true)
	fixture.store.view.associations = []firstTrustAssociationRecord{association}
	service := newIssue120WithdrawalService()
	facade, err := newFirstTrustFacade(service, nil)
	if err != nil {
		t.Fatal(err)
	}
	coordinator := issue120Coordinator(fixture, facade)
	facade.coordinator = coordinator
	coordinator.withdrawalWait = time.Second
	if got := coordinator.reopen(context.Background()); got != "pairing_closed" {
		t.Fatalf("startup outcome = %q", got)
	}
	normalized := hex.EncodeToString(association.subject)
	facade.mu.Lock()
	facade.connections[normalized] = &firstTrustConnection{
		generation: 1, active: true, connected: true, registered: true, shipID: association.service,
	}
	facade.mu.Unlock()
	bridge := newOperatorAdminV1Bridge(coordinator, service, &msp04cOrdinalReader{next: 1_300})
	snapshot, failure := bridge.snapshotOperatorAdminV1(context.Background())
	requireOperatorAdminV1BridgeSuccess(t, failure)

	result := make(chan struct {
		transition operatorAdminV1BridgeTransition
		failure    string
	}, 1)
	go func() {
		transition, failure := bridge.untrustOperatorAdminV1(context.Background(), snapshot.trusted[0].reference)
		result <- struct {
			transition operatorAdminV1BridgeTransition
			failure    string
		}{transition: transition, failure: failure}
	}()
	select {
	case <-service.disconnectStarted:
	case <-time.After(time.Second):
		t.Fatal("connected untrust did not request disconnect")
	}
	select {
	case early := <-result:
		t.Fatalf("connected untrust completed before disconnect ACK: %#v", early)
	case <-time.After(20 * time.Millisecond):
	}
	facade.RemoteSKIDisconnected(nil, normalized)
	select {
	case completed := <-result:
		requireOperatorAdminV1BridgeSuccess(t, completed.failure)
		if !completed.transition.changed || completed.transition.outcome != "revoked" {
			t.Fatalf("connected untrust = %#v, want changed revoked", completed.transition)
		}
	case <-time.After(time.Second):
		t.Fatal("connected untrust did not complete after disconnect ACK")
	}
	disconnects, unregisters := service.withdrawalCalls()
	if disconnects != 1 || unregisters != 1 {
		t.Fatalf("connected withdrawal calls disconnect/unregister = %d/%d, want 1/1", disconnects, unregisters)
	}
}

func TestIssue120ConnectedAdminUntrustTimeoutKeepsDurableDenialAcrossRestart(t *testing.T) {
	fixture := newMSP04CFixture(t)
	lineage := fixture.store.view.control.associationLineage
	association := msp04cAssociation(1, lineage, true, true, true, true)
	fixture.store.view.associations = []firstTrustAssociationRecord{association}
	service := newIssue120WithdrawalService()
	facade, err := newFirstTrustFacade(service, nil)
	if err != nil {
		t.Fatal(err)
	}
	coordinator := issue120Coordinator(fixture, facade)
	facade.coordinator = coordinator
	coordinator.withdrawalWait = 20 * time.Millisecond
	if got := coordinator.reopen(context.Background()); got != "pairing_closed" {
		t.Fatalf("startup outcome = %q", got)
	}
	normalized := hex.EncodeToString(association.subject)
	facade.mu.Lock()
	facade.connections[normalized] = &firstTrustConnection{
		generation: 1, active: true, connected: true, registered: true, shipID: association.service,
	}
	facade.mu.Unlock()
	bridge := newOperatorAdminV1Bridge(coordinator, service, &msp04cOrdinalReader{next: 1_310})
	snapshot, failure := bridge.snapshotOperatorAdminV1(context.Background())
	requireOperatorAdminV1BridgeSuccess(t, failure)

	transition, failure := bridge.untrustOperatorAdminV1(context.Background(), snapshot.trusted[0].reference)
	if transition.changed || failure != "disconnect_ack_timeout" {
		t.Errorf("unacknowledged connected untrust = %#v/%q, want unchanged disconnect_ack_timeout", transition, failure)
	}
	disconnects, unregisters := service.withdrawalCalls()
	if disconnects != 1 || unregisters != 0 {
		t.Fatalf("timed-out withdrawal calls disconnect/unregister = %d/%d, want 1/0", disconnects, unregisters)
	}
	if fixture.store.calls() != 1 || len(fixture.store.view.control.tombstones) != 1 ||
		len(fixture.store.view.control.receipts) != 1 || fixture.store.view.control.receipts[0].terminal ||
		fixture.store.view.control.receipts[0].result != "revocation_withdrawal_incomplete" ||
		len(fixture.store.view.associations) != 1 ||
		firstTrustAssociationUsable(fixture.store.view.associations[0], lineage) {
		t.Fatalf("timed-out withdrawal did not retain durable denial: calls=%d view=%#v", fixture.store.calls(), fixture.store.view)
	}

	restarted := fixture.newCoordinator()
	if got := restarted.reopen(context.Background()); got != "pairing_closed" {
		t.Fatalf("restart outcome = %q", got)
	}
	if restarted.trusted(association.subject) || restarted.recoveryState() != "REVOKED" {
		t.Fatalf("restart trust = %t state=%q, want denied REVOKED", restarted.trusted(association.subject), restarted.recoveryState())
	}
}

func TestIssue120DisconnectACKRequiresExactAdmittedConnectionGeneration(t *testing.T) {
	service := newIssue120WithdrawalService()
	facade, err := newFirstTrustFacade(service, nil)
	if err != nil {
		t.Fatal(err)
	}
	remote := msp04cSubject(1_320)
	normalized := hex.EncodeToString(remote)
	exact := &firstTrustConnection{generation: 7, active: true, connected: true, registered: true}
	facade.connections[normalized] = exact
	acknowledgment, started := facade.disconnectRemote(remote)
	if !started || acknowledgment == nil {
		t.Fatal("exact connected withdrawal was not started")
	}

	reconnected := &firstTrustConnection{generation: 8, active: true, connected: true, registered: true}
	facade.mu.Lock()
	facade.connections[normalized] = reconnected
	facade.mu.Unlock()
	facade.RemoteSKIDisconnected(nil, normalized)
	select {
	case <-acknowledgment:
		t.Fatal("different-generation callback acknowledged the admitted generation")
	default:
	}
	facade.mu.Lock()
	if facade.connections[normalized] != reconnected {
		facade.mu.Unlock()
		t.Fatal("stale disconnect callback retired the reconnected generation")
	}
	facade.connections[normalized] = exact
	facade.mu.Unlock()

	facade.RemoteSKIDisconnected(nil, normalized)
	select {
	case <-acknowledgment:
	case <-time.After(time.Second):
		t.Fatal("exact-generation disconnect callback did not acknowledge withdrawal")
	}
	facade.RemoteSKIDisconnected(nil, normalized)
}

func TestIssue120CurrentGenerationSPINETopologyExactlyRemovesAbsentFacts(t *testing.T) {
	const remoteSKI = "0000000000000000000000000000000000000120"
	handler, err := newRuntimeServiceHandler(
		RuntimeConfig{Remotes: []RuntimeRemote{{SKI: remoteSKI}}},
		"0000000000000000000000000000000000000001",
		func() time.Time { return time.Unix(2_100_000_000, 0).UTC() },
	)
	if err != nil {
		t.Fatal(err)
	}
	handler.spineEventsActive = true
	handler.spineGeneration = 12
	base := handler.newRemoteObservation(remoteSKI)
	base.SessionID = "session:issue120:7"
	base.SessionState = "connected"
	base.SessionIndex = 7
	base.Devices = issue120RichTopology(remoteSKI)
	if err := handler.reducer.Replace(base); err != nil {
		t.Fatal(err)
	}
	handler.observations[remoteSKI] = base
	refresh := runtimeSPINERefresh{generation: 12, sessionIndex: 7}

	handler.updateRemoteFromSPINEEvent(remoteSKI, refresh, issue120ReducedTopology(remoteSKI))
	graph := handler.reducer.Snapshot()
	if len(graph) != 1 || len(graph[0].Devices) != 1 || graph[0].Devices[0].ID != "device-current" ||
		len(graph[0].Devices[0].Entities) != 1 || graph[0].Devices[0].Entities[0].ID != "entity-current" ||
		len(graph[0].Devices[0].Entities[0].Features) != 1 || graph[0].Devices[0].Entities[0].Features[0].ID != "feature-current" {
		t.Fatalf("reduced current-generation topology retained absent facts: %+v", graph)
	}

	handler.updateRemoteFromSPINEEvent(remoteSKI, refresh, nil)
	graph = handler.reducer.Snapshot()
	if len(graph) != 1 || len(graph[0].Devices) != 0 {
		t.Fatalf("empty current-generation topology retained removed devices: %+v", graph)
	}
}

func TestIssue120DisconnectWithdrawsConnectionOwnedRawTopology(t *testing.T) {
	const remoteSKI = "0000000000000000000000000000000000000120"
	handler, err := newRuntimeServiceHandler(
		RuntimeConfig{Remotes: []RuntimeRemote{{SKI: remoteSKI}}},
		"0000000000000000000000000000000000000001",
		func() time.Time { return time.Unix(2_100_000_000, 0).UTC() },
	)
	if err != nil {
		t.Fatal(err)
	}
	observation := handler.newRemoteObservation(remoteSKI)
	observation.SessionID = "session:issue120:9"
	observation.SessionState = "connected"
	observation.SessionIndex = 9
	observation.Devices = issue120RichTopology(remoteSKI)
	if err := handler.reducer.Replace(observation); err != nil {
		t.Fatal(err)
	}
	handler.observations[remoteSKI] = observation

	handler.RemoteSKIDisconnected(nil, remoteSKI)
	graph := handler.reducer.Snapshot()
	if len(graph) != 1 || graph[0].SessionState != "disconnected" || len(graph[0].Devices) != 0 {
		t.Fatalf("disconnect retained connection-owned raw topology: %+v", graph)
	}
}

func TestIssue120AdminBridgeSurfaceCarriesStableIdentityMetadataAndRetryAdmission(t *testing.T) {
	issue120RequireFields(t, reflect.TypeOf(operatorAdminV1BridgeSnapshot{}), map[string]reflect.Type{
		"localSKI":    reflect.TypeOf(""),
		"localSHIPID": reflect.TypeOf(""),
	})
	issue120RequireFields(t, reflect.TypeOf(operatorAdminV1BridgeFact{}), map[string]reflect.Type{
		"connectionState": reflect.TypeOf(""),
		"name":            reflect.TypeOf(""),
		"identifier":      reflect.TypeOf(""),
		"brand":           reflect.TypeOf(""),
		"typeName":        reflect.TypeOf(""),
		"model":           reflect.TypeOf(""),
		"retryState":      reflect.TypeOf(""),
		"retryDeadline":   reflect.TypeOf(time.Time{}),
		"retryAdmitted":   reflect.TypeOf(false),
	})
}

func TestIssue120AdminBridgePublishesConfiguredStableLocalSHIPIdentity(t *testing.T) {
	fixture := newMSP04CFixture(t)
	service := newIssue120WithdrawalService()
	coordinator := fixture.newCoordinator()
	if got := coordinator.reopen(context.Background()); got != "pairing_closed" {
		t.Fatalf("startup outcome = %q", got)
	}
	const localSKI = "1111111111111111111111111111111111111111"
	const localSHIPID = "HLS-0123456789abcdef0123456789abcdef"
	bridge := newOperatorAdminV1Bridge(
		coordinator,
		service,
		&msp04cOrdinalReader{next: 1_350},
		operatorAdminV1BridgeLocalIdentity{ski: localSKI, shipID: localSHIPID},
	)
	snapshot, failure := bridge.snapshotOperatorAdminV1(context.Background())
	requireOperatorAdminV1BridgeSuccess(t, failure)
	if snapshot.localSKI != localSKI || snapshot.localSHIPID != localSHIPID {
		t.Fatalf("local SHIP identity = %q/%q, want %q/%q", snapshot.localSKI, snapshot.localSHIPID, localSKI, localSHIPID)
	}
	assertOperatorAdminV1BridgeSnapshotSanitized(t, snapshot)
}

func TestIssue120AdminBridgeReportsConnectedIdleAndRetryFSMStates(t *testing.T) {
	tests := []struct {
		name            string
		connected       bool
		retryState      string
		retryInflight   bool
		wantConnection  string
		wantDeadlineSet bool
		wantAdmitted    bool
	}{
		{name: "trusted connected", connected: true, wantConnection: "connected"},
		{name: "trusted idle", wantConnection: "idle"},
		{name: "retry ready", retryState: "RETRY_READY", wantConnection: "idle", wantAdmitted: true},
		{name: "paired retry remains executable while connection owns admission", retryState: "RETRY_READY", retryInflight: true, wantConnection: "idle", wantAdmitted: true},
		{name: "backoff active", retryState: "BACKOFF_ACTIVE", wantConnection: "idle", wantDeadlineSet: true},
		{name: "terminal quarantine", retryState: "ADMIN_HOLD", wantConnection: "idle"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMSP04CFixture(t)
			lineage := fixture.store.view.control.associationLineage
			association := msp04cAssociation(1, lineage, true, true, true, true)
			fixture.store.view.associations = []firstTrustAssociationRecord{association}
			service := newIssue120WithdrawalService()
			facade, err := newFirstTrustFacade(service, nil)
			if err != nil {
				t.Fatal(err)
			}
			coordinator := issue120Coordinator(fixture, facade)
			facade.coordinator = coordinator
			if got := coordinator.reopen(context.Background()); got != "pairing_closed" {
				t.Fatalf("startup outcome = %q", got)
			}
			ski := hex.EncodeToString(association.subject)
			facade.VisibleRemoteServicesUpdated(nil, []shipapi.RemoteService{{
				Ski: ski, Name: "myVaillant Connect", Identifier: "vr940-lab", Brand: "Vaillant", Type: "gateway", Model: "VR940f",
			}})
			if test.connected {
				facade.mu.Lock()
				facade.connections[ski] = &firstTrustConnection{
					generation: 1, active: true, connected: true, registered: true, shipID: association.service,
				}
				facade.mu.Unlock()
			}
			if test.retryState != "" {
				scope := firstTrustRuntimeRetryScope(ski)
				remaining := time.Duration(0)
				if test.retryState == "BACKOFF_ACTIVE" {
					remaining = 3 * time.Second
				}
				coordinator.mu.Lock()
				coordinator.controlView.control.quarantines = []firstTrustQuarantineRecord{{
					scope: scope, reason: "RETRYABLE_FAILURE", state: test.retryState,
					remainingDelay: remaining, retentionBudget: firstTrustQuarantineRetention,
					lastControlEpoch: coordinator.controlView.control.controlEpoch,
				}}
				if test.retryState == "BACKOFF_ACTIVE" {
					coordinator.retryArms[scope] = firstTrustRetryArm{
						armedAt: fixture.clock.MonotonicNow(), deadline: fixture.clock.MonotonicNow() + 3*time.Second,
					}
				}
				coordinator.retryInflight[scope] = test.retryInflight
				coordinator.mu.Unlock()
			}
			bridge := newOperatorAdminV1Bridge(coordinator, service, &msp04cOrdinalReader{next: 1_400})
			snapshot, failure := bridge.snapshotOperatorAdminV1(context.Background())
			requireOperatorAdminV1BridgeSuccess(t, failure)
			if len(snapshot.trusted) != 1 {
				t.Fatalf("trusted rows = %d, want 1", len(snapshot.trusted))
			}
			fact := reflect.ValueOf(snapshot.trusted[0])
			if got := issue120ReflectedString(t, fact, "connectionState"); got != test.wantConnection {
				t.Errorf("connection state = %q, want %q", got, test.wantConnection)
			}
			if got := issue120ReflectedString(t, fact, "retryState"); got != test.retryState {
				t.Errorf("retry state = %q, want %q", got, test.retryState)
			}
			if got := issue120ReflectedBool(t, fact, "retryAdmitted"); got != test.wantAdmitted {
				t.Errorf("retry admitted = %t, want %t", got, test.wantAdmitted)
			}
			deadline := issue120ReflectedField(t, fact, "retryDeadline")
			if deadline.IsValid() && deadline.IsZero() == test.wantDeadlineSet {
				t.Errorf("retry deadline zero = %t, want deadline set %t", deadline.IsZero(), test.wantDeadlineSet)
			}
			for field, want := range map[string]string{
				"name": "myVaillant Connect", "identifier": "vr940-lab", "brand": "Vaillant", "typeName": "gateway", "model": "VR940f",
			} {
				if got := issue120ReflectedString(t, fact, field); got != want {
					t.Errorf("partner %s = %q, want %q", field, got, want)
				}
			}
		})
	}
}

func TestIssue120AdminRetryAdmissionMatchesExecutableEligibility(t *testing.T) {
	coordinator, _, scope := issue116RetryReadyCoordinator(t, 120_500)
	service := newIssue116RetryService(coordinator, scope)
	bridge := newOperatorAdminV1Bridge(coordinator, service, &msp04cOrdinalReader{next: 120_510})

	snapshot, failure := bridge.snapshotOperatorAdminV1(context.Background())
	requireOperatorAdminV1BridgeSuccess(t, failure)
	if len(snapshot.trusted) != 1 || !snapshot.trusted[0].retryAdmitted || snapshot.trusted[0].retryState != "RETRY_READY" {
		t.Fatalf("eligible retry-ready snapshot = %#v, want retry_admitted=true", snapshot.trusted)
	}
	partner := snapshot.trusted[0].reference

	coordinator.mu.Lock()
	coordinator.retryInflight[scope] = true
	coordinator.mu.Unlock()
	snapshot, failure = bridge.snapshotOperatorAdminV1(context.Background())
	requireOperatorAdminV1BridgeSuccess(t, failure)
	if len(snapshot.trusted) != 1 || snapshot.trusted[0].retryAdmitted {
		t.Fatalf("busy retry-ready snapshot = %#v, want retry_admitted=false", snapshot.trusted)
	}
	transition, failure := bridge.retryTrustedOperatorAdminV1(context.Background(), partner)
	if transition.changed || failure != "candidate_busy" {
		t.Fatalf("busy retry = %#v/%q, want unchanged candidate_busy", transition, failure)
	}
	_, _, retryCalls, _, _, _ := service.snapshot()
	if retryCalls != 0 {
		t.Fatalf("non-admitted retry reached service %d times", retryCalls)
	}
}

func issue120Coordinator(fixture *msp04cFixture, effects firstTrustEffects) *firstTrustCoordinator {
	return newFirstTrustCoordinatorWithRecovery(
		fixture.clock.WallNow,
		fixture.clock.MonotonicNow,
		&msp04cOrdinalReader{next: 500},
		fixture.store,
		fixture.anchor,
		effects,
		fixture.policy,
	)
}

func issue120AssertTerminalRevocationReceipt(t *testing.T, view firstTrustControlView, association [32]byte) {
	t.Helper()
	for _, tombstone := range view.control.tombstones {
		if tombstone.associationRef != association {
			continue
		}
		for _, receipt := range view.control.receipts {
			if receipt.operationID == tombstone.operationID && receipt.operationClass == "revocation" &&
				receipt.result == "revoked" && receipt.terminal {
				return
			}
		}
	}
	t.Fatal("offline untrust did not persist a terminal revoked receipt")
}

func issue120RichTopology(ski string) []runtimeDeviceObservation {
	return []runtimeDeviceObservation{
		{
			ID: "device-current", SKI: ski, Address: "device-current", Type: "gateway",
			Entities: []runtimeEntityObservation{
				{
					ID: "entity-current", DeviceAddress: "device-current", EntityAddress: "[1]", Type: "current",
					Features: []runtimeFeatureObservation{
						{ID: "feature-current", DeviceAddress: "device-current", EntityAddress: "[1]", FeatureAddress: "[1]:1", Type: "current"},
						{ID: "feature-absent", DeviceAddress: "device-current", EntityAddress: "[1]", FeatureAddress: "[1]:2", Type: "absent"},
					},
				},
				{ID: "entity-absent", DeviceAddress: "device-current", EntityAddress: "[2]", Type: "absent"},
			},
		},
		{ID: "device-absent", SKI: ski, Address: "device-absent", Type: "absent"},
	}
}

func issue120ReducedTopology(ski string) []runtimeDeviceObservation {
	return []runtimeDeviceObservation{{
		ID: "device-current", SKI: ski, Address: "device-current", Type: "gateway",
		Entities: []runtimeEntityObservation{{
			ID: "entity-current", DeviceAddress: "device-current", EntityAddress: "[1]", Type: "current",
			Features: []runtimeFeatureObservation{{
				ID: "feature-current", DeviceAddress: "device-current", EntityAddress: "[1]", FeatureAddress: "[1]:1", Type: "current",
			}},
		}},
	}}
}

func issue120RequireFields(t *testing.T, typ reflect.Type, fields map[string]reflect.Type) {
	t.Helper()
	for name, want := range fields {
		field, ok := typ.FieldByName(name)
		if !ok {
			t.Errorf("%s lacks required field %s", typ.Name(), name)
			continue
		}
		if field.Type != want {
			t.Errorf("%s.%s = %s, want %s", typ.Name(), name, field.Type, want)
		}
	}
}

func issue120ReflectedField(t *testing.T, value reflect.Value, name string) reflect.Value {
	t.Helper()
	field := value.FieldByName(name)
	if !field.IsValid() {
		t.Errorf("%s lacks required field %s", value.Type(), name)
	}
	return field
}

func issue120ReflectedString(t *testing.T, value reflect.Value, name string) string {
	t.Helper()
	field := issue120ReflectedField(t, value, name)
	if !field.IsValid() || field.Kind() != reflect.String {
		return ""
	}
	return field.String()
}

func issue120ReflectedBool(t *testing.T, value reflect.Value, name string) bool {
	t.Helper()
	field := issue120ReflectedField(t, value, name)
	if !field.IsValid() || field.Kind() != reflect.Bool {
		return false
	}
	return field.Bool()
}

type issue120WithdrawalService struct {
	mu                sync.Mutex
	disconnects       int
	unregisters       int
	disconnectStarted chan struct{}
	disconnectOnce    sync.Once
}

func newIssue120WithdrawalService() *issue120WithdrawalService {
	return &issue120WithdrawalService{disconnectStarted: make(chan struct{})}
}

func (*issue120WithdrawalService) SetAutoAccept(bool)                {}
func (*issue120WithdrawalService) RegisterRemoteSKI(string)          {}
func (*issue120WithdrawalService) CancelPairingWithSKI(string)       {}
func (*issue120WithdrawalService) SetPairingRegistration(bool) error { return nil }

func (service *issue120WithdrawalService) DisconnectSKI(string, string) {
	service.mu.Lock()
	service.disconnects++
	service.mu.Unlock()
	service.disconnectOnce.Do(func() { close(service.disconnectStarted) })
}

func (service *issue120WithdrawalService) UnregisterRemoteSKI(string) {
	service.mu.Lock()
	service.unregisters++
	service.mu.Unlock()
}

func (*issue120WithdrawalService) SelectPairingCandidate(string, string) (shipapi.PairingCandidateReservation, error) {
	return shipapi.NewPairingCandidateReservation([32]byte{1}), nil
}

func (*issue120WithdrawalService) ConnectPairingCandidate(shipapi.PairingCandidateReservation) error {
	return nil
}

func (*issue120WithdrawalService) RetryTrustedRemote(string) error { return nil }

func (service *issue120WithdrawalService) withdrawalCalls() (int, int) {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.disconnects, service.unregisters
}
