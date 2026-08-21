package eebusfacade

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
	shipmodel "github.com/Project-Helianthus/helianthus-ship-go/model"
)

func TestIssue124TypedPairingActionOutcomeMatrix(t *testing.T) {
	category := func(value shipmodel.PINCategory) *shipmodel.PINCategory {
		return shipmodel.PINCategoryPointer(value)
	}
	tests := []struct {
		name        string
		state       shipapi.ConnectionState
		err         error
		pin         *shipmodel.PINHandshakeDetail
		pinSupplied bool
		outcome     string
		retryable   bool
		terminal    bool
	}{
		{name: "required omitted", state: shipapi.ConnectionStatePin, pin: &shipmodel.PINHandshakeDetail{Requirement: shipmodel.PINRequirementRequired, Phase: shipmodel.PINPhaseWaitingPeer, Category: category(shipmodel.PINCategoryRequired), Retryable: true}, outcome: "pin_required", retryable: true, terminal: true},
		{name: "required supplied stays pending", state: shipapi.ConnectionStatePin, pin: &shipmodel.PINHandshakeDetail{Requirement: shipmodel.PINRequirementRequired, Phase: shipmodel.PINPhaseSubmitted, Category: category(shipmodel.PINCategoryRequired), Retryable: true}, pinSupplied: true},
		{name: "optional restricted", state: shipapi.ConnectionStatePin, pin: &shipmodel.PINHandshakeDetail{Requirement: shipmodel.PINRequirementOptional, Phase: shipmodel.PINPhaseRestricted, Category: category(shipmodel.PINCategoryOptional)}, outcome: "pin_optional", terminal: true},
		{name: "busy", state: shipapi.ConnectionStatePin, pin: &shipmodel.PINHandshakeDetail{Requirement: shipmodel.PINRequirementRequired, Phase: shipmodel.PINPhaseWaitingPeer, Category: category(shipmodel.PINCategoryBusy), Retryable: true}, outcome: "pin_busy", retryable: true, terminal: true},
		{name: "rejected", state: shipapi.ConnectionStateError, pin: &shipmodel.PINHandshakeDetail{Requirement: shipmodel.PINRequirementRequired, Phase: shipmodel.PINPhaseFailed, Category: category(shipmodel.PINCategoryRejected)}, outcome: "pin_rejected", terminal: true},
		{name: "unavailable", state: shipapi.ConnectionStateError, pin: &shipmodel.PINHandshakeDetail{Requirement: shipmodel.PINRequirementRequired, Phase: shipmodel.PINPhaseFailed, Category: category(shipmodel.PINCategoryUnavailable), Retryable: true}, outcome: "pin_unavailable", retryable: true, terminal: true},
		{name: "protocol", state: shipapi.ConnectionStateError, pin: &shipmodel.PINHandshakeDetail{Requirement: shipmodel.PINRequirementUnknown, Phase: shipmodel.PINPhaseFailed, Category: category(shipmodel.PINCategoryProtocol)}, outcome: "pin_protocol_error", terminal: true},
		{name: "completed", state: shipapi.ConnectionStateCompleted, outcome: "connection_completed", terminal: true},
		{name: "timeout", state: shipapi.ConnectionStateError, err: context.DeadlineExceeded, outcome: "attempt_timeout", retryable: true, terminal: true},
		{name: "untyped error", state: shipapi.ConnectionStateError, err: errors.New("peer-private-failure"), outcome: "unknown_state", terminal: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			detail := shipapi.NewConnectionStateDetail(test.state, test.err)
			detail.SetPINHandshakeDetail(test.pin)
			outcome, retryable, terminal := operatorAdminV1ActionOutcomeForPairingDetail(detail, test.pinSupplied)
			if outcome != test.outcome || retryable != test.retryable || terminal != test.terminal {
				t.Fatalf("mapping = %q/%t/%t, want %q/%t/%t", outcome, retryable, terminal, test.outcome, test.retryable, test.terminal)
			}
			if strings.Contains(outcome, "peer-private") {
				t.Fatalf("mapping leaked source error text in %q", outcome)
			}
		})
	}
}

func TestIssue124RealSelectConnectCallbackPreservesTypedPINAndExactGeneration(t *testing.T) {
	tests := []struct {
		name        string
		requirement shipmodel.PINRequirement
		phase       shipmodel.PINPhase
		category    shipmodel.PINCategory
		retryable   bool
		outcome     string
	}{
		{name: "required", requirement: shipmodel.PINRequirementRequired, phase: shipmodel.PINPhaseWaitingPeer, category: shipmodel.PINCategoryRequired, retryable: true, outcome: "pin_required"},
		{name: "optional", requirement: shipmodel.PINRequirementOptional, phase: shipmodel.PINPhaseRestricted, category: shipmodel.PINCategoryOptional, outcome: "pin_optional"},
		{name: "busy", requirement: shipmodel.PINRequirementRequired, phase: shipmodel.PINPhaseWaitingPeer, category: shipmodel.PINCategoryBusy, retryable: true, outcome: "pin_busy"},
		{name: "rejected", requirement: shipmodel.PINRequirementRequired, phase: shipmodel.PINPhaseFailed, category: shipmodel.PINCategoryRejected, outcome: "pin_rejected"},
		{name: "unavailable", requirement: shipmodel.PINRequirementRequired, phase: shipmodel.PINPhaseFailed, category: shipmodel.PINCategoryUnavailable, retryable: true, outcome: "pin_unavailable"},
		{name: "protocol", requirement: shipmodel.PINRequirementUnknown, phase: shipmodel.PINPhaseFailed, category: shipmodel.PINCategoryProtocol, outcome: "pin_protocol_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := newIssue124OperatorRealPath(t)
			category := test.category
			path.outgoing.OutgoingAttemptHandshakeStateUpdate(
				path.remoteSKI,
				shipmodel.ShipState{
					State: shipmodel.SmePinStateAskProcess,
					PIN: &shipmodel.PINHandshakeDetail{
						Requirement: test.requirement,
						Phase:       test.phase,
						Category:    &category,
						Retryable:   test.retryable,
					},
				},
				path.permit.Metadata,
			)
			action := path.facade.operatorAdminV1ActiveActionSnapshot(path.fixture.clock.WallNow())
			if action == nil || action.actionID != path.actionID || action.state != "terminal" ||
				action.outcome != test.outcome || action.retryable != test.retryable {
				t.Fatalf("real Select->Connect callback action = %#v, want terminal %q retryable=%t", action, test.outcome, test.retryable)
			}
		})
	}
}

func TestIssue124RealCallbackRejectsStaleSameSKIReplacementGeneration(t *testing.T) {
	path := newIssue124OperatorRealPath(t)
	path.facade.mu.Lock()
	delete(path.facade.connections, path.remoteSKI)
	replacement := path.facade.newConnectionLocked(path.remoteSKI, false)
	if replacement == nil {
		path.facade.mu.Unlock()
		t.Fatal("replacement generation was not created")
	}
	replacement.attemptStarted = true
	replacementGeneration := replacement.generation
	path.facade.mu.Unlock()

	const replacementActionID = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	if !path.facade.armOperatorAdminV1ActiveAction(
		replacementActionID,
		path.remoteSKI,
		false,
		path.fixture.clock.WallNow().Add(time.Minute),
	) {
		t.Fatal("replacement action was not armed")
	}
	category := shipmodel.PINCategoryRejected
	path.outgoing.OutgoingAttemptHandshakeStateUpdate(
		path.remoteSKI,
		shipmodel.ShipState{
			State: shipmodel.SmePinStateCheckError,
			PIN: &shipmodel.PINHandshakeDetail{
				Requirement: shipmodel.PINRequirementRequired,
				Phase:       shipmodel.PINPhaseFailed,
				Category:    &category,
			},
		},
		path.permit.Metadata,
	)
	path.facade.mu.Lock()
	action := path.facade.activeAction
	if action == nil || action.actionID != replacementActionID || action.generation != replacementGeneration || action.state != "pending" {
		path.facade.mu.Unlock()
		t.Fatalf("stale callback changed replacement generation action: %#v, want generation %d pending", action, replacementGeneration)
	}
	path.facade.mu.Unlock()

	completed := shipapi.NewConnectionStateDetail(shipapi.ConnectionStateCompleted, nil)
	path.facade.servicePairingDetailUpdateForGeneration(path.remoteSKI, replacementGeneration, completed)
	if action := path.facade.operatorAdminV1ActiveActionSnapshot(path.fixture.clock.WallNow()); action == nil ||
		action.actionID != replacementActionID || action.state != "terminal" || action.outcome != "connection_completed" {
		t.Fatalf("current replacement callback action = %#v", action)
	}
}

func TestIssue124RealTerminalErrorSurvivesGenerationTeardownUntilExactObservation(t *testing.T) {
	tests := []struct {
		name      string
		pin       *shipmodel.PINHandshakeDetail
		err       error
		outcome   string
		retryable bool
	}{
		{
			name: "pin rejected",
			pin: &shipmodel.PINHandshakeDetail{
				Requirement: shipmodel.PINRequirementRequired,
				Phase:       shipmodel.PINPhaseFailed,
				Category:    shipmodel.PINCategoryPointer(shipmodel.PINCategoryRejected),
			},
			outcome: "pin_rejected",
		},
		{
			name: "pin unavailable",
			pin: &shipmodel.PINHandshakeDetail{
				Requirement: shipmodel.PINRequirementRequired,
				Phase:       shipmodel.PINPhaseFailed,
				Category:    shipmodel.PINCategoryPointer(shipmodel.PINCategoryUnavailable),
				Retryable:   true,
			},
			outcome:   "pin_unavailable",
			retryable: true,
		},
		{
			name: "pin protocol",
			pin: &shipmodel.PINHandshakeDetail{
				Requirement: shipmodel.PINRequirementUnknown,
				Phase:       shipmodel.PINPhaseFailed,
				Category:    shipmodel.PINCategoryPointer(shipmodel.PINCategoryProtocol),
			},
			outcome: "pin_protocol_error",
		},
		{name: "untyped timeout", err: context.DeadlineExceeded, outcome: "attempt_timeout", retryable: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := newIssue124OperatorRealPath(t)
			path.outgoing.OutgoingAttemptHandshakeStateUpdate(
				path.remoteSKI,
				shipmodel.ShipState{State: shipmodel.SmeStateError, Error: test.err, PIN: test.pin},
				path.permit.Metadata,
			)
			assertIssue124TerminalActionObservedExactlyOnce(t, path, test.outcome, test.retryable)
		})
	}
}

func TestIssue124RealPINRequiredSurvivesLaterErrorTeardown(t *testing.T) {
	path := newIssue124OperatorRealPath(t)
	required := shipmodel.PINCategoryRequired
	path.outgoing.OutgoingAttemptHandshakeStateUpdate(
		path.remoteSKI,
		shipmodel.ShipState{
			State: shipmodel.SmePinStateAskProcess,
			PIN: &shipmodel.PINHandshakeDetail{
				Requirement: shipmodel.PINRequirementRequired,
				Phase:       shipmodel.PINPhaseWaitingPeer,
				Category:    &required,
				Retryable:   true,
			},
		},
		path.permit.Metadata,
	)
	if action := path.facade.operatorAdminV1ActiveActionSnapshot(path.fixture.clock.WallNow()); action == nil ||
		action.state != "terminal" || action.outcome != "pin_required" || !action.retryable {
		t.Fatalf("PIN-required action before teardown = %#v", action)
	}
	path.outgoing.OutgoingAttemptHandshakeStateUpdate(
		path.remoteSKI,
		shipmodel.ShipState{State: shipmodel.SmeStateError, Error: errors.New("closed owner error")},
		path.permit.Metadata,
	)
	assertIssue124TerminalActionObservedExactlyOnce(t, path, "pin_required", true)
}

func assertIssue124TerminalActionObservedExactlyOnce(
	t *testing.T,
	path *issue124OperatorRealPath,
	wantOutcome string,
	wantRetryable bool,
) {
	t.Helper()
	action := path.facade.operatorAdminV1ActiveActionSnapshot(path.fixture.clock.WallNow())
	if action == nil || action.actionID != path.actionID || action.state != "terminal" ||
		action.outcome != wantOutcome || action.retryable != wantRetryable {
		t.Fatalf("terminal action after generation teardown = %#v, want %q retryable=%t", action, wantOutcome, wantRetryable)
	}
	const wrongActionID = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	path.operator.observeOperatorAdminV1ActiveAction(wrongActionID)
	if action := path.facade.operatorAdminV1ActiveActionSnapshot(path.fixture.clock.WallNow()); action == nil ||
		action.actionID != path.actionID || action.state != "terminal" {
		t.Fatalf("non-matching observation cleared terminal action: %#v", action)
	}
	path.operator.observeOperatorAdminV1ActiveAction(path.actionID)
	if action := path.facade.operatorAdminV1ActiveActionSnapshot(path.fixture.clock.WallNow()); action != nil {
		t.Fatalf("exact observation retained terminal action: %#v", action)
	}
	path.operator.observeOperatorAdminV1ActiveAction(path.actionID)
	if action := path.facade.operatorAdminV1ActiveActionSnapshot(path.fixture.clock.WallNow()); action != nil {
		t.Fatalf("repeated exact observation recreated terminal action: %#v", action)
	}
}

type issue124OperatorRealPath struct {
	fixture   *msp04cFixture
	facade    *firstTrustFacade
	outgoing  *firstTrustOutgoingAttemptBridge
	operator  *operatorAdminV1Bridge
	remoteSKI string
	permit    shipapi.OutgoingAttemptPermit
	actionID  string
}

type issue124OperatorRealPathService struct {
	issue60Service
	reservation shipapi.PairingCandidateReservation
	selectHook  func(string, string) error
	connectHook func(shipapi.PairingCandidateReservation) error
}

func (service *issue124OperatorRealPathService) SelectPairingCandidate(candidateRef, expectedSKI string) (shipapi.PairingCandidateReservation, error) {
	if service.selectHook != nil {
		if err := service.selectHook(candidateRef, expectedSKI); err != nil {
			return shipapi.PairingCandidateReservation{}, err
		}
	}
	return service.reservation, nil
}

func (service *issue124OperatorRealPathService) ConnectPairingCandidate(reservation shipapi.PairingCandidateReservation) error {
	if !reservation.Matches(service.reservation) {
		return shipapi.ErrPairingCandidateReservationStale
	}
	if service.connectHook != nil {
		return service.connectHook(reservation)
	}
	return nil
}

func (service *issue124OperatorRealPathService) ConnectPairingCandidateWithPIN(
	reservation shipapi.PairingCandidateReservation,
	_ shipapi.TransientPINProvider,
) error {
	return service.ConnectPairingCandidate(reservation)
}

func (*issue124OperatorRealPathService) RetryTrustedRemote(string) error { return nil }

type issue124GenerationLifecycle struct{ facade *firstTrustFacade }

func (lifecycle issue124GenerationLifecycle) RemoteSKIDisconnected(ski string) {
	lifecycle.facade.RemoteSKIDisconnected(nil, ski)
}

func (lifecycle issue124GenerationLifecycle) ServicePairingDetailUpdate(ski string, detail *shipapi.ConnectionStateDetail) {
	lifecycle.facade.ServicePairingDetailUpdate(ski, detail)
}

func (lifecycle issue124GenerationLifecycle) servicePairingDetailUpdateForGeneration(
	ski string,
	generation uint64,
	detail *shipapi.ConnectionStateDetail,
) {
	lifecycle.facade.servicePairingDetailUpdateForGeneration(ski, generation, detail)
}

func newIssue124OperatorRealPath(t *testing.T) *issue124OperatorRealPath {
	t.Helper()
	fixture := newMSP04CFixture(t)
	coordinator := fixture.newCoordinator()
	if got := coordinator.reopen(context.Background()); got != "pairing_closed" {
		t.Fatalf("reopen = %q", got)
	}
	var reservationToken [32]byte
	reservationToken[0] = 0x7c
	service := &issue124OperatorRealPathService{
		reservation: shipapi.NewPairingCandidateReservation(reservationToken),
	}
	facade, err := newFirstTrustFacade(service, coordinator)
	if err != nil {
		t.Fatal(err)
	}
	coordinator.mu.Lock()
	coordinator.effects = facade
	coordinator.mu.Unlock()
	outgoing := newFirstTrustOutgoingAttemptBridge(&runtimeFirstTrustResources{coordinator: coordinator})
	outgoing.bindLifecycle(issue124GenerationLifecycle{facade: facade})
	outgoing.bindTLSLifecycle(facade)
	if got := coordinator.openPairingWindow(context.Background(), "issue124-real-open", firstTrustMaximumWindow); got != "open_empty" {
		t.Fatalf("open pairing window = %q", got)
	}

	var permit shipapi.OutgoingAttemptPermit
	service.selectHook = func(_ string, expectedSKI string) error {
		facade.ServicePairingDetailUpdate(expectedSKI, shipapi.NewConnectionStateDetail(shipapi.ConnectionStateQueued, nil))
		handle, prepareErr := outgoing.Prepare(issue75SHIPRequest(expectedSKI, "peer.invalid"))
		if prepareErr != nil {
			return prepareErr
		}
		permit, prepareErr = outgoing.AuthorizeLaunch(handle)
		if prepareErr != nil {
			return prepareErr
		}
		if permit.Decision != shipapi.OutgoingAttemptDecisionPermit {
			return fmt.Errorf("outgoing attempt decision = %v", permit.Decision)
		}
		return nil
	}
	const candidateRef = "issue124-real-candidate"
	facade.VisiblePairingCandidatesUpdated(nil, []shipapi.PairingCandidateRef{{
		CandidateRef: candidateRef,
		SKI:          issue56SKIA,
	}})
	operator := newOperatorAdminV1Bridge(coordinator, service, &msp04cOrdinalReader{next: 12_400})
	snapshot, failure := operator.snapshotOperatorAdminV1(context.Background())
	requireOperatorAdminV1BridgeSuccess(t, failure)
	if len(snapshot.discovered) != 1 {
		t.Fatalf("real-path discovery rows = %d, want 1", len(snapshot.discovered))
	}
	selection, transition, failure := operator.selectOperatorAdminV1(
		context.Background(),
		snapshot.discovered[0].reference,
		issue56SKIA,
	)
	if failure != "" || !transition.changed || selection == "" {
		t.Fatalf("real-path select = %q/%#v/%q", selection, transition, failure)
	}
	transition, failure = operator.connectOperatorAdminV1(context.Background(), selection)
	if failure != "" || !transition.changed || transition.actionID == "" || permit.Decision != shipapi.OutgoingAttemptDecisionPermit {
		t.Fatalf("real-path connect = %#v/%q permit=%#v", transition, failure, permit)
	}
	remote, remoteSKI, ok := decodeFirstTrustSKI(issue56SKIA)
	if !ok || len(remote) != 20 {
		t.Fatal("real-path remote SKI is invalid")
	}
	facade.mu.Lock()
	connection := facade.connections[remoteSKI]
	action := facade.activeAction
	if connection == nil || action == nil || action.actionID != transition.actionID || action.generation != connection.generation {
		facade.mu.Unlock()
		t.Fatalf("real-path action generation = %#v connection=%#v", action, connection)
	}
	facade.mu.Unlock()
	return &issue124OperatorRealPath{
		fixture: fixture, facade: facade, outgoing: outgoing, operator: operator, remoteSKI: remoteSKI,
		permit: permit, actionID: transition.actionID,
	}
}

func TestIssue124ActiveActionRejectsStaleGenerationAndClearsAtBoundaries(t *testing.T) {
	now := time.Unix(1_950_000_000, 0)
	const target = operatorAdminV1BridgeTestSKI
	const firstID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const secondID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	facade := &firstTrustFacade{connections: make(map[string]*firstTrustConnection)}

	if !facade.armOperatorAdminV1ActiveAction(firstID, target, false, now.Add(time.Minute)) {
		t.Fatal("first action was not armed")
	}
	facade.mu.Lock()
	firstConnection := facade.newConnectionLocked(target, false)
	firstGeneration := firstConnection.generation
	facade.bindOperatorAdminV1ActiveActionLocked(target, firstGeneration)
	facade.mu.Unlock()

	if !facade.armOperatorAdminV1ActiveAction(secondID, target, true, now.Add(time.Minute)) {
		t.Fatal("replacement action was not armed")
	}
	rejected := shipapi.NewConnectionStateDetail(shipapi.ConnectionStateError, shipapi.ErrPINRejected)
	rejected.SetPINHandshakeDetail(&shipmodel.PINHandshakeDetail{
		Requirement: shipmodel.PINRequirementRequired, Phase: shipmodel.PINPhaseFailed,
		Category: shipmodel.PINCategoryPointer(shipmodel.PINCategoryRejected),
	})
	facade.recordOperatorAdminV1PairingDetail(target, firstGeneration, rejected)
	if action := facade.operatorAdminV1ActiveActionSnapshot(now); action == nil || action.actionID != secondID || action.state != "pending" {
		t.Fatalf("stale generation changed replacement action: %#v", action)
	}

	facade.mu.Lock()
	delete(facade.connections, target)
	secondConnection := facade.newConnectionLocked(target, false)
	secondGeneration := secondConnection.generation
	facade.bindOperatorAdminV1ActiveActionLocked(target, secondGeneration)
	facade.mu.Unlock()
	facade.recordOperatorAdminV1PairingDetail(target, firstGeneration, rejected)
	completed := shipapi.NewConnectionStateDetail(shipapi.ConnectionStateCompleted, nil)
	facade.recordOperatorAdminV1PairingDetail(target, secondGeneration, completed)
	if action := facade.operatorAdminV1ActiveActionSnapshot(now); action == nil || action.actionID != secondID ||
		action.state != "terminal" || action.outcome != "connection_completed" {
		t.Fatalf("current generation terminal action = %#v", action)
	}
	facade.clearOperatorAdminV1ActiveAction(secondID)
	if action := facade.operatorAdminV1ActiveActionSnapshot(now); action != nil {
		t.Fatalf("terminal observation retained action %#v", action)
	}

	if !facade.armOperatorAdminV1ActiveAction(firstID, target, false, now.Add(time.Second)) {
		t.Fatal("expiry action was not armed")
	}
	if action := facade.operatorAdminV1ActiveActionSnapshot(now.Add(time.Second)); action != nil {
		t.Fatalf("expired action retained %#v", action)
	}
	if !facade.armOperatorAdminV1ActiveAction(secondID, target, false, now.Add(time.Minute)) {
		t.Fatal("cancel action was not armed")
	}
	facade.clearOperatorAdminV1ActiveAction("")
	if action := facade.operatorAdminV1ActiveActionSnapshot(now); action != nil {
		t.Fatalf("explicit abandonment retained action %#v", action)
	}
}

func TestIssue124ActiveActionCallbackSnapshotRaceIsLinearizable(t *testing.T) {
	now := time.Unix(1_960_000_000, 0)
	const actionID = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	facade := &firstTrustFacade{connections: make(map[string]*firstTrustConnection)}
	if !facade.armOperatorAdminV1ActiveAction(actionID, operatorAdminV1BridgeTestSKI, true, now.Add(time.Minute)) {
		t.Fatal("action was not armed")
	}
	facade.mu.Lock()
	connection := facade.newConnectionLocked(operatorAdminV1BridgeTestSKI, false)
	facade.bindOperatorAdminV1ActiveActionLocked(operatorAdminV1BridgeTestSKI, connection.generation)
	generation := connection.generation
	facade.mu.Unlock()

	completed := shipapi.NewConnectionStateDetail(shipapi.ConnectionStateCompleted, nil)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := 0; index < 8; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			for attempt := 0; attempt < 64; attempt++ {
				_ = facade.operatorAdminV1ActiveActionSnapshot(now)
			}
		}()
	}
	wait.Add(1)
	go func() {
		defer wait.Done()
		<-start
		facade.recordOperatorAdminV1PairingDetail(operatorAdminV1BridgeTestSKI, generation, completed)
	}()
	close(start)
	wait.Wait()
	action := facade.operatorAdminV1ActiveActionSnapshot(now)
	if action == nil || action.actionID != actionID || action.state != "terminal" || action.outcome != "connection_completed" {
		t.Fatalf("linearized action = %#v, want one terminal current-generation result", action)
	}
}

func TestOperatorAdminV1BridgeSelectReservesWithoutDialAndConnectsAtMostOnce(t *testing.T) {
	fixture := newMSP04BFixture(t, "commit_durable")
	coordinator := fixture.coordinator.(*firstTrustCoordinator)
	service := newOperatorAdminV1BridgeServiceSpy()
	bridge := newOperatorAdminV1Bridge(coordinator, service, &msp04cOrdinalReader{next: 700})
	attachOperatorAdminV1TestActionFacade(bridge)

	opened, failure := bridge.openOperatorAdminV1(context.Background(), time.Minute)
	requireOperatorAdminV1BridgeSuccess(t, failure)
	if !opened.changed || coordinator.state() != "OPEN_EMPTY" {
		t.Fatalf("open transition = %#v state=%q, want changed OPEN_EMPTY", opened, coordinator.state())
	}

	const candidateRef = "ship-owner-private-observation"
	coordinator.visiblePairingCandidatesUpdated([]shipapi.PairingCandidateRef{{
		CandidateRef: candidateRef,
		SKI:          operatorAdminV1BridgeTestSKI,
		Name:         "owner-visible peer",
		Identifier:   "owner-identifier",
		Brand:        "owner-brand",
		Type:         "owner-type",
		Model:        "owner-model",
	}})
	snapshot, snapshotFailure := bridge.snapshotOperatorAdminV1(context.Background())
	requireOperatorAdminV1BridgeSuccess(t, snapshotFailure)
	if len(snapshot.discovered) != 1 {
		t.Fatalf("discovered rows = %d, want 1", len(snapshot.discovered))
	}
	observation := snapshot.discovered[0].reference
	if observation == "" || observation == candidateRef || strings.Contains(observation, candidateRef) {
		t.Fatalf("observation reference %q exposes or omits its private binding", observation)
	}
	row := snapshot.discovered[0]
	if row.ski != operatorAdminV1BridgeTestSKI || row.observationRevision == 0 || row.lastSeen.IsZero() ||
		row.name != "owner-visible peer" || row.identifier != "owner-identifier" || row.brand != "owner-brand" ||
		row.typeName != "owner-type" || row.model != "owner-model" || row.endpoint != "" ||
		snapshot.capturedAt.IsZero() || snapshot.status == "" || snapshot.window != "open" ||
		snapshot.windowDeadline.IsZero() || snapshot.register == "" || snapshot.listener == "" || snapshot.discovery == "" {
		t.Fatalf("discovered owner facts or status incomplete: row=%#v snapshot=%#v", row, snapshot)
	}

	selection, selected, selectFailure := bridge.selectOperatorAdminV1(
		context.Background(),
		observation,
		operatorAdminV1BridgeTestSKI,
	)
	requireOperatorAdminV1BridgeSuccess(t, selectFailure)
	if selection == "" || !selected.changed {
		t.Fatalf("selection = %q transition=%#v, want opaque changed reservation", selection, selected)
	}
	selectCalls, connectCalls, retryCalls, selectedRef, selectedSKI, _ := service.snapshot()
	if selectCalls != 1 || connectCalls != 0 || retryCalls != 0 || selectedRef != candidateRef || selectedSKI != operatorAdminV1BridgeTestSKI {
		t.Fatalf("service after select calls=%d/%d/%d ref=%q ski=%q, want one identity-bound select and no dial", selectCalls, connectCalls, retryCalls, selectedRef, selectedSKI)
	}

	connected, connectFailure := bridge.connectOperatorAdminV1(context.Background(), selection)
	requireOperatorAdminV1BridgeSuccess(t, connectFailure)
	if !connected.changed {
		t.Fatalf("connect transition = %#v, want changed", connected)
	}
	selectCalls, connectCalls, retryCalls, _, _, reservation := service.snapshot()
	if selectCalls != 1 || connectCalls != 1 || retryCalls != 0 || !reservation.Matches(service.reservation) {
		t.Fatalf("service after connect calls=%d/%d/%d reservation=%#v, want exact reservation once", selectCalls, connectCalls, retryCalls, reservation)
	}

	_, repeatedFailure := bridge.connectOperatorAdminV1(context.Background(), selection)
	if repeatedFailure == "" {
		t.Fatal("consumed selection was accepted a second time")
	}
	_, connectCalls, _, _, _, _ = service.snapshot()
	if connectCalls != 1 {
		t.Fatalf("duplicate connect effects = %d, want 1", connectCalls)
	}
}

func TestIssue122BridgeForwardsPINReservationAndProviderExactlyOnce(t *testing.T) {
	fixture := newMSP04BFixture(t, "commit_durable")
	coordinator := fixture.coordinator.(*firstTrustCoordinator)
	service := newOperatorAdminV1BridgeServiceSpy()
	bridge := newOperatorAdminV1Bridge(coordinator, service, &msp04cOrdinalReader{next: 701})
	attachOperatorAdminV1TestActionFacade(bridge)
	if transition, failure := bridge.openOperatorAdminV1(context.Background(), time.Minute); failure != "" || !transition.changed {
		t.Fatalf("open=%#v/%q", transition, failure)
	}
	coordinator.visiblePairingCandidatesUpdated([]shipapi.PairingCandidateRef{{
		CandidateRef: "issue122-candidate", SKI: operatorAdminV1BridgeTestSKI,
	}})
	snapshot, failure := bridge.snapshotOperatorAdminV1(context.Background())
	requireOperatorAdminV1BridgeSuccess(t, failure)
	selection, transition, failure := bridge.selectOperatorAdminV1(context.Background(), snapshot.discovered[0].reference, operatorAdminV1BridgeTestSKI)
	if failure != "" || !transition.changed || selection == "" {
		t.Fatalf("select=%q/%#v/%q", selection, transition, failure)
	}
	provider := shipapi.TransientPINProviderFunc(func(_ string, consume func([]byte) error) (bool, error) {
		return true, consume([]byte("a1b2c3d4"))
	})
	transition, failure = bridge.connectOperatorAdminV1WithPIN(context.Background(), selection, provider)
	if failure != "" || !transition.changed || transition.outcome != "connection_started" {
		t.Fatalf("connect with PIN=%#v/%q", transition, failure)
	}
	selectCalls, connectCalls, pinCalls, _, _, reservation, received := service.snapshotPIN()
	if selectCalls != 1 || connectCalls != 0 || pinCalls != 1 || !reservation.Matches(service.reservation) || received == nil {
		t.Fatalf("PIN forwarding=%d/%d/%d reservation=%#v provider=%#v, want select 1/plain 0/PIN 1/exact/opaque", selectCalls, connectCalls, pinCalls, reservation, received)
	}
	if _, failure = bridge.connectOperatorAdminV1WithPIN(context.Background(), selection, provider); failure == "" {
		t.Fatal("consumed selection allowed a second PIN connect")
	}
	_, _, pinCalls, _, _, _, _ = service.snapshotPIN()
	if pinCalls != 1 {
		t.Fatalf("PIN connect calls=%d, want exactly one", pinCalls)
	}

	var typedNil *issue122PINProvider
	if err := callOperatorAdminV1ConnectWithPIN(service, service.reservation, typedNil); !errors.Is(err, shipapi.ErrPINProviderInvalid) {
		t.Fatalf("typed-nil provider error=%v, want %v", err, shipapi.ErrPINProviderInvalid)
	}
	_, _, pinCalls, _, _, _, _ = service.snapshotPIN()
	if pinCalls != 1 {
		t.Fatalf("typed-nil provider reached downstream PIN controller %d times", pinCalls)
	}
}

func TestOperatorAdminV1BridgeTerminalCandidateLifecycleRetiresSelections(t *testing.T) {
	fixture := newMSP04BFixture(t, "commit_durable")
	coordinator := fixture.coordinator.(*firstTrustCoordinator)
	service := newOperatorAdminV1BridgeServiceSpy()
	bridge := newOperatorAdminV1Bridge(coordinator, service, &msp04cOrdinalReader{next: 9_000})
	attachOperatorAdminV1TestActionFacade(bridge)

	for index := 0; index <= operatorAdminV1BridgeMaximumReferences; index++ {
		if transition, failure := bridge.openOperatorAdminV1(context.Background(), time.Second); failure != "" || !transition.changed {
			t.Fatalf("cycle %d open = %#v/%q", index, transition, failure)
		}
		candidateRef := fmt.Sprintf("terminal-candidate-%03d", index)
		coordinator.visiblePairingCandidatesUpdated([]shipapi.PairingCandidateRef{{
			CandidateRef: candidateRef,
			SKI:          operatorAdminV1BridgeTestSKI,
		}})
		snapshot, failure := bridge.snapshotOperatorAdminV1(context.Background())
		requireOperatorAdminV1BridgeSuccess(t, failure)
		if len(snapshot.discovered) != 1 {
			t.Fatalf("cycle %d discovered rows = %d, want 1", index, len(snapshot.discovered))
		}
		selection, transition, failure := bridge.selectOperatorAdminV1(
			context.Background(), snapshot.discovered[0].reference, operatorAdminV1BridgeTestSKI,
		)
		if failure != "" || !transition.changed || selection == "" {
			t.Fatalf("cycle %d select = %q/%#v/%q", index, selection, transition, failure)
		}
		if transition, failure = bridge.connectOperatorAdminV1(context.Background(), selection); failure != "" || !transition.changed {
			t.Fatalf("cycle %d connect = %#v/%q", index, transition, failure)
		}

		advanceMSP04BClock(fixture.clock, 2*time.Second)
		if _, failure := bridge.snapshotOperatorAdminV1(context.Background()); failure != "" {
			t.Fatalf("cycle %d terminal snapshot = %q", index, failure)
		}
		if retained := len(bridge.selections); retained != 0 {
			t.Fatalf("cycle %d terminal lifecycle retained %d selections", index, retained)
		}
	}

	selectCalls, connectCalls, _, _, _, _ := service.snapshot()
	if selectCalls != operatorAdminV1BridgeMaximumReferences+1 || connectCalls != operatorAdminV1BridgeMaximumReferences+1 {
		t.Fatalf("select/connect calls = %d/%d, want %d each", selectCalls, connectCalls, operatorAdminV1BridgeMaximumReferences+1)
	}
}

func TestOperatorAdminV1BridgeDiscoveryRevisionReplacesSameCandidateIdentityObservation(t *testing.T) {
	fixture := newMSP04BFixture(t, "commit_durable")
	coordinator := fixture.coordinator.(*firstTrustCoordinator)
	service := newOperatorAdminV1BridgeServiceSpy()
	bridge := newOperatorAdminV1Bridge(coordinator, service, &msp04cOrdinalReader{next: 710})
	if transition, failure := bridge.openOperatorAdminV1(context.Background(), time.Minute); failure != "" || !transition.changed {
		t.Fatalf("open transition=%#v failure=%q", transition, failure)
	}

	candidate := shipapi.PairingCandidateRef{
		CandidateRef: "same-private-candidate-ref",
		SKI:          operatorAdminV1BridgeTestSKI,
	}
	coordinator.visiblePairingCandidatesUpdated([]shipapi.PairingCandidateRef{candidate})
	first, failure := bridge.snapshotOperatorAdminV1(context.Background())
	requireOperatorAdminV1BridgeSuccess(t, failure)
	if len(first.discovered) != 1 {
		t.Fatalf("first discovered rows=%d, want 1", len(first.discovered))
	}
	oldObservation := first.discovered[0].reference
	firstRevision := coordinator.candidateSnapshotRevision

	coordinator.visiblePairingCandidatesUpdated([]shipapi.PairingCandidateRef{candidate})
	second, failure := bridge.snapshotOperatorAdminV1(context.Background())
	requireOperatorAdminV1BridgeSuccess(t, failure)
	if len(second.discovered) != 1 {
		t.Fatalf("second discovered rows=%d, want 1", len(second.discovered))
	}
	newObservation := second.discovered[0].reference
	if coordinator.candidateSnapshotRevision != firstRevision+1 {
		t.Fatalf("coordinator discovery revision=%d -> %d, want exactly +1", firstRevision, coordinator.candidateSnapshotRevision)
	}
	if second.discovered[0].ski != first.discovered[0].ski || second.discovered[0].ski != operatorAdminV1BridgeTestSKI {
		t.Fatalf("fixture changed identity across discovery revisions: first=%#v second=%#v", first.discovered[0], second.discovered[0])
	}
	if oldObservation == "" || newObservation == "" || oldObservation == newObservation {
		t.Fatalf("same candidate identity retained observation %q across discovery revision", oldObservation)
	}

	selection, transition, staleFailure := bridge.selectOperatorAdminV1(
		context.Background(), oldObservation, operatorAdminV1BridgeTestSKI,
	)
	if staleFailure != "observation_stale" || selection != "" || transition.changed {
		t.Fatalf("old discovery revision select=%q/%#v/%q, want zero-effect observation_stale", selection, transition, staleFailure)
	}
	selectCalls, connectCalls, retryCalls, _, _, _ := service.snapshot()
	if selectCalls != 0 || connectCalls != 0 || retryCalls != 0 {
		t.Fatalf("old discovery revision reached service calls %d/%d/%d", selectCalls, connectCalls, retryCalls)
	}

	selection, transition, currentFailure := bridge.selectOperatorAdminV1(
		context.Background(), newObservation, operatorAdminV1BridgeTestSKI,
	)
	requireOperatorAdminV1BridgeSuccess(t, currentFailure)
	if selection == "" || !transition.changed {
		t.Fatalf("current discovery revision select=%q/%#v, want changed reservation", selection, transition)
	}
	selectCalls, connectCalls, retryCalls, selectedRef, selectedSKI, _ := service.snapshot()
	if selectCalls != 1 || connectCalls != 0 || retryCalls != 0 || selectedRef != candidate.CandidateRef || selectedSKI != candidate.SKI {
		t.Fatalf("current discovery revision service calls=%d/%d/%d ref=%q ski=%q", selectCalls, connectCalls, retryCalls, selectedRef, selectedSKI)
	}
}

func TestOperatorAdminV1BridgeMapsUnknownServiceOutcomesFailClosed(t *testing.T) {
	fixture := newMSP04BFixture(t, "commit_durable")
	coordinator := fixture.coordinator.(*firstTrustCoordinator)
	service := newOperatorAdminV1BridgeServiceSpy()
	service.selectErr = errors.New("future select outcome")
	bridge := newOperatorAdminV1Bridge(coordinator, service, &msp04cOrdinalReader{next: 720})

	if transition, failure := bridge.openOperatorAdminV1(context.Background(), time.Minute); failure != "" || !transition.changed {
		t.Fatalf("open transition=%#v failure=%q", transition, failure)
	}
	coordinator.visiblePairingCandidatesUpdated([]shipapi.PairingCandidateRef{{
		CandidateRef: "unknown-outcome-private-ref",
		SKI:          operatorAdminV1BridgeTestSKI,
	}})
	snapshot, failure := bridge.snapshotOperatorAdminV1(context.Background())
	requireOperatorAdminV1BridgeSuccess(t, failure)
	selection, transition, selectFailure := bridge.selectOperatorAdminV1(
		context.Background(), snapshot.discovered[0].reference, operatorAdminV1BridgeTestSKI,
	)
	if selectFailure != "unknown_state" || selection != "" || !transition.changed {
		t.Fatalf("unknown select result = %q/%#v/%q, want fail-closed unknown_state that reports consumed internal state", selection, transition, selectFailure)
	}
	_, connectCalls, _, _, _, _ := service.snapshot()
	if connectCalls != 0 {
		t.Fatalf("unknown select outcome caused %d dial effects", connectCalls)
	}
}

func TestOperatorAdminV1BridgeConnectFailureReportsConsumedSelectionChange(t *testing.T) {
	fixture := newMSP04BFixture(t, "commit_durable")
	coordinator := fixture.coordinator.(*firstTrustCoordinator)
	service := newOperatorAdminV1BridgeServiceSpy()
	service.connectErr = shipapi.ErrPairingCandidateReservationUnavailable
	bridge := newOperatorAdminV1Bridge(coordinator, service, &msp04cOrdinalReader{next: 740})
	attachOperatorAdminV1TestActionFacade(bridge)
	if transition, failure := bridge.openOperatorAdminV1(context.Background(), time.Minute); failure != "" || !transition.changed {
		t.Fatalf("open transition=%#v failure=%q", transition, failure)
	}
	coordinator.visiblePairingCandidatesUpdated([]shipapi.PairingCandidateRef{{
		CandidateRef: "connect-error-private-ref",
		SKI:          operatorAdminV1BridgeTestSKI,
	}})
	snapshot, failure := bridge.snapshotOperatorAdminV1(context.Background())
	requireOperatorAdminV1BridgeSuccess(t, failure)
	selection, transition, failure := bridge.selectOperatorAdminV1(
		context.Background(), snapshot.discovered[0].reference, operatorAdminV1BridgeTestSKI,
	)
	requireOperatorAdminV1BridgeSuccess(t, failure)
	if selection == "" || !transition.changed {
		t.Fatalf("selection=%q transition=%#v, want current reservation", selection, transition)
	}

	connected, connectFailure := bridge.connectOperatorAdminV1(context.Background(), selection)
	if connectFailure != "observation_stale" || !connected.changed {
		t.Fatalf("failed connect transition=%#v failure=%q, want observation_stale with consumed-state changed=true", connected, connectFailure)
	}
	_, repeatedFailure := bridge.connectOperatorAdminV1(context.Background(), selection)
	if repeatedFailure != "observation_stale" {
		t.Fatalf("consumed selection retry failure=%q, want observation_stale", repeatedFailure)
	}
	_, connectCalls, _, _, _, _ := service.snapshot()
	if connectCalls != 1 {
		t.Fatalf("failed consumed connect effects=%d, want at-most-once 1", connectCalls)
	}
}

func TestOperatorAdminV1BridgeRetryUsesOnlyIdentityAndUntrustResolvesCurrentBindings(t *testing.T) {
	fixture := newMSP04CFixture(t)
	lineage := fixture.store.view.control.associationLineage
	association := msp04cAssociation(1, lineage, true, true, true, true)
	fixture.store.view.associations = []firstTrustAssociationRecord{association}
	retryScope := firstTrustRuntimeRetryScope(firstTrustNormalizedSKI(association.subject))
	fixture.store.view.control.quarantines = []firstTrustQuarantineRecord{
		issue68RetryHold(retryScope, fixture.store.view.control.controlEpoch),
	}
	coordinator := fixture.newCoordinator()
	if got := coordinator.reopen(context.Background()); got != "pairing_closed" {
		t.Fatalf("startup outcome = %q", got)
	}
	issue68AssertRetryReady(t, fixture.store.view.control, retryScope)
	publicationCallsBeforeUntrust := fixture.store.calls()
	service := newOperatorAdminV1BridgeServiceSpy()
	bridge := newOperatorAdminV1Bridge(coordinator, service, &msp04cOrdinalReader{next: 800})

	snapshot, failure := bridge.snapshotOperatorAdminV1(context.Background())
	requireOperatorAdminV1BridgeSuccess(t, failure)
	if len(snapshot.trusted) != 1 {
		t.Fatalf("trusted rows = %d, want 1", len(snapshot.trusted))
	}
	partner := snapshot.trusted[0].reference
	wantSKI := hex.EncodeToString(association.subject)
	associationRef := hex.EncodeToString(association.reference[:])
	if partner == "" || partner == associationRef || strings.Contains(partner, associationRef) {
		t.Fatalf("partner reference %q exposes or omits the durable association binding", partner)
	}
	if snapshot.trusted[0].ski != wantSKI {
		t.Fatalf("trusted SKI = %q, want %q", snapshot.trusted[0].ski, wantSKI)
	}
	if snapshot.trusted[0].shipID != association.service {
		t.Fatalf("trusted SHIP ID = %q, want source-owned %q", snapshot.trusted[0].shipID, association.service)
	}

	retried, retryFailure := bridge.retryTrustedOperatorAdminV1(context.Background(), partner)
	requireOperatorAdminV1BridgeSuccess(t, retryFailure)
	if !retried.changed {
		t.Fatalf("retry transition = %#v, want changed", retried)
	}
	selectCalls, connectCalls, retryCalls, _, _, _ := service.snapshot()
	if selectCalls != 0 || connectCalls != 0 || retryCalls != 1 || service.retrySKI != wantSKI {
		t.Fatalf("retry calls=%d/%d/%d ski=%q, want identity-only RetryTrustedRemote(%q)", selectCalls, connectCalls, retryCalls, service.retrySKI, wantSKI)
	}

	// Advance every durable owner binding after the partner snapshot. The
	// bridge must resolve these current values internally at untrust time; the
	// caller still supplies only its opaque partner reference.
	advanceOperatorAdminV1BridgeControlView(t, fixture, coordinator)
	untrusted, untrustFailure := bridge.untrustOperatorAdminV1(context.Background(), partner)
	requireOperatorAdminV1BridgeSuccess(t, untrustFailure)
	if !untrusted.changed {
		t.Fatalf("untrust transition = %#v, want changed", untrusted)
	}
	assertOperatorAdminV1BridgeAssociationRevoked(t, fixture.store.view, association.reference)
	if fixture.store.calls() != publicationCallsBeforeUntrust+2 {
		t.Fatalf("untrust durable publication calls = %d, want %d", fixture.store.calls(), publicationCallsBeforeUntrust+2)
	}
	if len(bridge.operationIDs) != 0 {
		t.Fatalf("terminal untrust retained %d bridge operation IDs", len(bridge.operationIDs))
	}
}

func TestOperatorAdminV1BridgeRoutesWindowConfirmAndCancelThroughCurrentCoordinatorBindings(t *testing.T) {
	t.Run("open and close", func(t *testing.T) {
		fixture := newMSP04BFixture(t, "commit_durable")
		coordinator := fixture.coordinator.(*firstTrustCoordinator)
		bridge := newOperatorAdminV1Bridge(coordinator, newOperatorAdminV1BridgeServiceSpy(), &msp04cOrdinalReader{next: 900})

		opened, failure := bridge.openOperatorAdminV1(context.Background(), time.Minute)
		requireOperatorAdminV1BridgeSuccess(t, failure)
		if !opened.changed || coordinator.state() != "OPEN_EMPTY" {
			t.Fatalf("open transition=%#v state=%q", opened, coordinator.state())
		}
		closed, failure := bridge.closeOperatorAdminV1(context.Background())
		requireOperatorAdminV1BridgeSuccess(t, failure)
		if !closed.changed || coordinator.state() != "PAIRING_CLOSED" {
			t.Fatalf("close transition=%#v state=%q", closed, coordinator.state())
		}
	})

	t.Run("confirm", func(t *testing.T) {
		fixture := newMSP04BFixture(t, "commit_durable")
		coordinator := fixture.coordinator.(*firstTrustCoordinator)
		bridge := newOperatorAdminV1Bridge(coordinator, newOperatorAdminV1BridgeServiceSpy(), &msp04cOrdinalReader{next: 920})
		remote := msp04cSubject(920)
		_ = openMSP04BCandidate(t, fixture, remote, 921, true)
		snapshot, failure := bridge.snapshotOperatorAdminV1(context.Background())
		requireOperatorAdminV1BridgeSuccess(t, failure)
		if len(snapshot.candidates) != 1 {
			t.Fatalf("candidate rows = %d, want 1", len(snapshot.candidates))
		}
		candidate := snapshot.candidates[0].reference
		confirmed, failure := bridge.confirmOperatorAdminV1(context.Background(), candidate, hex.EncodeToString(remote))
		requireOperatorAdminV1BridgeSuccess(t, failure)
		if !confirmed.changed || !coordinator.trusted(remote) {
			t.Fatalf("confirm transition=%#v trusted=%t", confirmed, coordinator.trusted(remote))
		}
	})

	t.Run("cancel", func(t *testing.T) {
		fixture := newMSP04BFixture(t, "commit_durable")
		coordinator := fixture.coordinator.(*firstTrustCoordinator)
		bridge := newOperatorAdminV1Bridge(coordinator, newOperatorAdminV1BridgeServiceSpy(), &msp04cOrdinalReader{next: 940})
		remote := msp04cSubject(940)
		_ = openMSP04BCandidate(t, fixture, remote, 941, true)
		snapshot, failure := bridge.snapshotOperatorAdminV1(context.Background())
		requireOperatorAdminV1BridgeSuccess(t, failure)
		candidate := snapshot.candidates[0].reference
		cancelled, failure := bridge.cancelOperatorAdminV1(context.Background(), candidate)
		requireOperatorAdminV1BridgeSuccess(t, failure)
		if !cancelled.changed {
			t.Fatalf("cancel transition=%#v, want changed", cancelled)
		}
		if _, _, _, _, _, _, ok := coordinator.candidate(); ok {
			t.Fatal("cancel left a coordinator candidate")
		}
	})
}

func TestOperatorAdminV1BridgeSnapshotReportsInvalidDiscoveryAsDegraded(t *testing.T) {
	fixture := newMSP04BFixture(t, "commit_durable")
	coordinator := fixture.coordinator.(*firstTrustCoordinator)
	service := newOperatorAdminV1BridgeServiceSpy()
	bridge := newOperatorAdminV1Bridge(coordinator, service, &msp04cOrdinalReader{next: 955})

	coordinator.visiblePairingCandidatesUpdated([]shipapi.PairingCandidateRef{{
		CandidateRef: "",
		SKI:          operatorAdminV1BridgeTestSKI,
	}})
	snapshot, failure := bridge.snapshotOperatorAdminV1(context.Background())
	requireOperatorAdminV1BridgeSuccess(t, failure)
	if snapshot.discovery != "invalid" || snapshot.degraded != "discovery_unavailable" || len(snapshot.discovered) != 0 {
		t.Fatalf("invalid discovery snapshot=%#v, want sanitized degraded status and no discovered rows", snapshot)
	}

	selection, transition, selectFailure := bridge.selectOperatorAdminV1(
		context.Background(), "unknown-observation", operatorAdminV1BridgeTestSKI,
	)
	if selectFailure != "observation_stale" || selection != "" || transition.changed {
		t.Fatalf("invalid discovery select=%q/%#v/%q, want zero-effect observation_stale", selection, transition, selectFailure)
	}
	selectCalls, connectCalls, retryCalls, _, _, _ := service.snapshot()
	if selectCalls != 0 || connectCalls != 0 || retryCalls != 0 {
		t.Fatalf("invalid discovery reached service calls %d/%d/%d", selectCalls, connectCalls, retryCalls)
	}
}

func TestOperatorAdminV1BridgeTLSBoundIncompleteCandidateRemainsCancellable(t *testing.T) {
	fixture := newIssue60Fixture(t)
	bridge := newOperatorAdminV1Bridge(fixture.coordinator, newOperatorAdminV1BridgeServiceSpy(), &msp04cOrdinalReader{next: 958})

	snapshot, failure := bridge.snapshotOperatorAdminV1(context.Background())
	requireOperatorAdminV1BridgeSuccess(t, failure)
	if len(snapshot.candidates) != 1 || snapshot.candidates[0].state != "association_incomplete" ||
		snapshot.candidates[0].associationComplete || snapshot.candidates[0].expiresAt.IsZero() {
		t.Fatalf("TLS-bound incomplete candidate facts=%#v, want cancellable association_incomplete row", snapshot.candidates)
	}

	cancelled, cancelFailure := bridge.cancelOperatorAdminV1(context.Background(), snapshot.candidates[0].reference)
	requireOperatorAdminV1BridgeSuccess(t, cancelFailure)
	if !cancelled.changed || cancelled.outcome != "cancelled" {
		t.Fatalf("incomplete candidate cancel=%#v, want cancelled changed", cancelled)
	}
	if fixture.coordinator.trusted(fixture.remote) {
		t.Fatal("incomplete candidate cancel published durable trust")
	}
	if fixture.service.registerCount() != 0 || fixture.service.unregisterCount() != 0 {
		t.Fatalf("incomplete candidate cancel register/unregister=%d/%d, want 0/0", fixture.service.registerCount(), fixture.service.unregisterCount())
	}
	if _, _, _, _, _, _, ok := fixture.coordinator.candidate(); ok {
		t.Fatal("incomplete candidate cancel retained coordinator candidate")
	}
	assertMSP04BCommitCount(t, fixture.base.store, 0)
}

func TestOperatorAdminV1BridgeCancelAfterTransientConfirmRevokesTargetWithoutDurableTrust(t *testing.T) {
	fixture := newIssue60Fixture(t)
	fixture.shipID()
	bridge := newOperatorAdminV1Bridge(fixture.coordinator, newOperatorAdminV1BridgeServiceSpy(), &msp04cOrdinalReader{next: 960})
	snapshot, failure := bridge.snapshotOperatorAdminV1(context.Background())
	requireOperatorAdminV1BridgeSuccess(t, failure)
	if len(snapshot.candidates) != 1 {
		t.Fatalf("pre-confirm candidate rows=%d, want 1", len(snapshot.candidates))
	}
	candidate := snapshot.candidates[0].reference
	confirmed, confirmFailure := bridge.confirmOperatorAdminV1(context.Background(), candidate, issue56SKIA)
	requireOperatorAdminV1BridgeSuccess(t, confirmFailure)
	if !confirmed.changed || confirmed.outcome != "transient_trusted" {
		t.Fatalf("confirm transition=%#v, want transient_trusted changed", confirmed)
	}
	if fixture.service.registerCount() != 1 || fixture.coordinator.trusted(fixture.remote) {
		t.Fatalf("transient confirm registers=%d durable-trusted=%t, want 1/false", fixture.service.registerCount(), fixture.coordinator.trusted(fixture.remote))
	}
	assertMSP04BCommitCount(t, fixture.base.store, 0)
	postConfirm, snapshotFailure := bridge.snapshotOperatorAdminV1(context.Background())
	requireOperatorAdminV1BridgeSuccess(t, snapshotFailure)
	if len(postConfirm.candidates) != 1 || postConfirm.candidates[0].state != "transient_trusted" ||
		postConfirm.candidates[0].expiresAt.IsZero() || !postConfirm.candidates[0].associationComplete {
		t.Fatalf("post-confirm transient candidate facts=%#v, want fresh cancellable association-complete row", postConfirm.candidates)
	}
	candidate = postConfirm.candidates[0].reference

	cancelled, cancelFailure := bridge.cancelOperatorAdminV1(context.Background(), candidate)
	requireOperatorAdminV1BridgeSuccess(t, cancelFailure)
	if !cancelled.changed || cancelled.outcome != "cancelled" {
		t.Fatalf("targeted post-confirm cancel transition=%#v, want cancelled changed", cancelled)
	}
	if fixture.service.unregisterCount() != 1 {
		t.Fatalf("transient post-confirm cancel unregistrations=%d, want 1", fixture.service.unregisterCount())
	}
	if fixture.coordinator.trusted(fixture.remote) {
		t.Fatal("post-confirm cancel published durable trust")
	}
	if _, _, _, _, _, _, ok := fixture.coordinator.candidate(); ok {
		t.Fatal("post-confirm cancel retained the transient candidate")
	}
	assertMSP04BCommitCount(t, fixture.base.store, 0)
}

func TestOperatorAdminV1BridgeMapsEveryReleasedTrustedRemoteRetryError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "invalid remote SKI", err: shipapi.ErrInvalidRemoteSKI, want: "invalid_request"},
		{name: "retry unavailable", err: shipapi.ErrTrustedRemoteRetryUnavailable, want: "discovery_unavailable"},
		{name: "not trusted", err: shipapi.ErrTrustedRemoteRetryNotTrusted, want: "trust_denied"},
		{name: "already connected", err: shipapi.ErrTrustedRemoteRetryConnected, want: "candidate_busy"},
		{name: "retry busy", err: shipapi.ErrTrustedRemoteRetryBusy, want: "candidate_busy"},
		{name: "observation stale", err: shipapi.ErrTrustedRemoteObservationStale, want: "observation_stale"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, err := range []error{test.err, fmt.Errorf("released retry error: %w", test.err)} {
				if got := mapOperatorAdminV1RetryError(err); got != test.want {
					t.Fatalf("mapOperatorAdminV1RetryError(%v)=%q, want closed category %q", err, got, test.want)
				}
			}
		})
	}
	if got := mapOperatorAdminV1RetryError(errors.New("future retry failure")); got != "unknown_state" {
		t.Fatalf("unknown retry error mapping=%q, want fail-closed unknown_state", got)
	}
}

func TestOperatorAdminV1BridgeRetiresTerminalOperationReferencesBeyondCapacity(t *testing.T) {
	fixture := newMSP04BFixture(t, "commit_durable")
	coordinator := fixture.coordinator.(*firstTrustCoordinator)
	bridge := newOperatorAdminV1Bridge(coordinator, newOperatorAdminV1BridgeServiceSpy(), &msp04cOrdinalReader{next: 1_200})
	for index := 0; index < 160; index++ {
		opened, openFailure := bridge.openOperatorAdminV1(context.Background(), time.Minute)
		requireOperatorAdminV1BridgeSuccess(t, openFailure)
		if !opened.changed {
			t.Fatalf("open %d transition=%#v, want changed", index, opened)
		}
		closed, closeFailure := bridge.closeOperatorAdminV1(context.Background())
		requireOperatorAdminV1BridgeSuccess(t, closeFailure)
		if !closed.changed {
			t.Fatalf("close %d transition=%#v, want changed", index, closed)
		}
		if len(bridge.operations) != 0 {
			t.Fatalf("terminal operation %d retained %d bridge references", index, len(bridge.operations))
		}
	}
}

func TestOperatorAdminV1BridgeSnapshotIsBoundedSanitizedAndNeverPartial(t *testing.T) {
	fixture := newMSP04BFixture(t, "commit_durable")
	coordinator := fixture.coordinator.(*firstTrustCoordinator)
	bridge := newOperatorAdminV1Bridge(coordinator, newOperatorAdminV1BridgeServiceSpy(), &msp04cOrdinalReader{next: 1_000})
	if transition, failure := bridge.openOperatorAdminV1(context.Background(), time.Minute); failure != "" || !transition.changed {
		t.Fatalf("open transition=%#v failure=%q", transition, failure)
	}

	candidates := make([]shipapi.PairingCandidateRef, firstTrustMaximumDiscoveredCandidates)
	for index := range candidates {
		candidates[index] = shipapi.PairingCandidateRef{
			CandidateRef: "owner-private-ref-" + operatorAdminV1BridgeIndex(index),
			SKI:          operatorAdminV1BridgeTestSKI,
		}
	}
	coordinator.visiblePairingCandidatesUpdated(candidates)
	snapshot, failure := bridge.snapshotOperatorAdminV1(context.Background())
	requireOperatorAdminV1BridgeSuccess(t, failure)
	if len(snapshot.discovered) != firstTrustMaximumDiscoveredCandidates {
		t.Fatalf("bounded snapshot rows = %d, want %d", len(snapshot.discovered), firstTrustMaximumDiscoveredCandidates)
	}
	assertOperatorAdminV1BridgeSnapshotSanitized(t, snapshot)
	privateRefs := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		privateRefs[candidate.CandidateRef] = struct{}{}
	}
	for index, row := range snapshot.discovered {
		_, exactLeak := privateRefs[row.reference]
		containsLeak := false
		for privateRef := range privateRefs {
			if strings.Contains(row.reference, privateRef) {
				containsLeak = true
				break
			}
		}
		if row.reference == "" || exactLeak || containsLeak {
			t.Fatalf("row %d reference %q exposes or omits candidate_ref", index, row.reference)
		}
	}

	overflow := append(append([]shipapi.PairingCandidateRef(nil), candidates...), shipapi.PairingCandidateRef{
		CandidateRef: "owner-private-ref-overflow",
		SKI:          operatorAdminV1BridgeTestSKI,
	})
	coordinator.visiblePairingCandidatesUpdated(overflow)
	degraded, overflowFailure := bridge.snapshotOperatorAdminV1(context.Background())
	requireOperatorAdminV1BridgeSuccess(t, overflowFailure)
	if degraded.discovery != "invalid" || degraded.degraded != "discovery_unavailable" || len(degraded.discovered) != 0 {
		t.Fatalf("capacity-invalid discovery snapshot=%#v, want sanitized degraded status and no partial rows", degraded)
	}
}

const operatorAdminV1BridgeTestSKI = "0123456789abcdef0123456789abcdef01234567"

func attachOperatorAdminV1TestActionFacade(bridge *operatorAdminV1Bridge) {
	bridge.actionFacade = &firstTrustFacade{connections: make(map[string]*firstTrustConnection)}
}

type operatorAdminV1BridgeServiceSpy struct {
	mu sync.Mutex

	reservation shipapi.PairingCandidateReservation
	selectErr   error
	connectErr  error
	retryErr    error

	selectCalls  int
	connectCalls int
	pinCalls     int
	retryCalls   int
	selectedRef  string
	selectedSKI  string
	retrySKI     string
	connected    shipapi.PairingCandidateReservation
	pinProvider  shipapi.TransientPINProvider
}

func newOperatorAdminV1BridgeServiceSpy() *operatorAdminV1BridgeServiceSpy {
	var token [32]byte
	token[0] = 1
	return &operatorAdminV1BridgeServiceSpy{reservation: shipapi.NewPairingCandidateReservation(token)}
}

func (service *operatorAdminV1BridgeServiceSpy) SelectPairingCandidate(candidateRef, expectedSKI string) (shipapi.PairingCandidateReservation, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.selectCalls++
	service.selectedRef = candidateRef
	service.selectedSKI = expectedSKI
	if service.selectErr != nil {
		return shipapi.PairingCandidateReservation{}, service.selectErr
	}
	return service.reservation, nil
}

func (service *operatorAdminV1BridgeServiceSpy) ConnectPairingCandidate(reservation shipapi.PairingCandidateReservation) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.connectCalls++
	service.connected = reservation
	return service.connectErr
}

func (service *operatorAdminV1BridgeServiceSpy) ConnectPairingCandidateWithPIN(reservation shipapi.PairingCandidateReservation, provider shipapi.TransientPINProvider) error {
	service.mu.Lock()
	service.pinCalls++
	service.connected = reservation
	service.pinProvider = provider
	service.mu.Unlock()
	if provider == nil {
		return shipapi.ErrPINProviderInvalid
	}
	return service.connectErr
}

func (service *operatorAdminV1BridgeServiceSpy) RetryTrustedRemote(expectedSKI string) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.retryCalls++
	service.retrySKI = expectedSKI
	return service.retryErr
}

func (service *operatorAdminV1BridgeServiceSpy) snapshot() (int, int, int, string, string, shipapi.PairingCandidateReservation) {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.selectCalls, service.connectCalls, service.retryCalls, service.selectedRef, service.selectedSKI, service.connected
}

func (service *operatorAdminV1BridgeServiceSpy) snapshotPIN() (int, int, int, string, string, shipapi.PairingCandidateReservation, shipapi.TransientPINProvider) {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.selectCalls, service.connectCalls, service.pinCalls, service.selectedRef, service.selectedSKI, service.connected, service.pinProvider
}

type issue122PINProvider struct{}

func (*issue122PINProvider) WithTransientPIN(string, func([]byte) error) (bool, error) {
	return false, nil
}

func requireOperatorAdminV1BridgeSuccess[T ~string](t *testing.T, failure T) {
	t.Helper()
	if failure != "" {
		t.Fatalf("unexpected operator AdminV1 bridge failure %q", failure)
	}
}

func advanceOperatorAdminV1BridgeControlView(t *testing.T, fixture *msp04cFixture, coordinator *firstTrustCoordinator) {
	t.Helper()
	next := fixture.store.nextView()
	fixture.store.mu.Lock()
	fixture.store.view = cloneFirstTrustControlView(next)
	fixture.store.mu.Unlock()

	fixture.anchor.mu.Lock()
	fixture.anchor.record.manifestGenerationHighWater = next.manifest.current.sequence
	fixture.anchor.record.controlEpochHighWater = next.control.controlEpoch
	anchor := cloneFirstTrustAnchorRecord(fixture.anchor.record)
	fixture.anchor.mu.Unlock()

	coordinator.mu.Lock()
	coordinator.controlView = cloneFirstTrustControlView(next)
	coordinator.anchorRecord = anchor
	coordinator.mu.Unlock()
}

func assertOperatorAdminV1BridgeAssociationRevoked(t *testing.T, view firstTrustControlView, reference [32]byte) {
	t.Helper()
	for _, association := range view.associations {
		if association.reference == reference && (association.active || association.trusted || association.allowlisted || association.reconnectable) {
			t.Fatal("untrust retained an active durable association capability")
		}
	}
	for _, tombstone := range view.control.tombstones {
		if tombstone.associationRef == reference && tombstone.effectiveGeneration.sequence != 0 {
			return
		}
	}
	t.Fatal("untrust did not publish a generation-bound tombstone")
}

func assertOperatorAdminV1BridgeSnapshotSanitized(t *testing.T, snapshot any) {
	t.Helper()
	forbidden := []string{
		"candidate_ref", "candidateref", "nonce", "store", "generation", "association",
		"control", "manifest", "path", "private", "pem", "token", "keybytes",
	}
	var visit func(reflect.Value, string)
	visit = func(value reflect.Value, path string) {
		if !value.IsValid() {
			return
		}
		for value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
			if value.IsNil() {
				return
			}
			value = value.Elem()
		}
		switch value.Kind() {
		case reflect.Struct:
			typ := value.Type()
			for index := 0; index < value.NumField(); index++ {
				field := typ.Field(index)
				normalized := strings.ToLower(strings.ReplaceAll(field.Name, "_", ""))
				if normalized == "associationcomplete" && field.Type.Kind() == reflect.Bool {
					visit(value.Field(index), path+"."+field.Name)
					continue
				}
				for _, fragment := range forbidden {
					fragment = strings.ReplaceAll(fragment, "_", "")
					if strings.Contains(normalized, fragment) {
						t.Fatalf("snapshot fact %s.%s leaks private binding", path, field.Name)
					}
				}
				visit(value.Field(index), path+"."+field.Name)
			}
		case reflect.Slice:
			if value.Type().Elem().Kind() == reflect.Uint8 {
				t.Fatalf("snapshot fact %s exposes private bytes", path)
			}
			for index := 0; index < value.Len(); index++ {
				visit(value.Index(index), path)
			}
		case reflect.Array:
			if value.Type().Elem().Kind() == reflect.Uint8 {
				t.Fatalf("snapshot fact %s exposes private bytes", path)
			}
		}
	}
	visit(reflect.ValueOf(snapshot), "snapshot")
}

func operatorAdminV1BridgeIndex(index int) string {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	if index == 0 {
		return "0"
	}
	var encoded [8]byte
	position := len(encoded)
	for index > 0 {
		position--
		encoded[position] = alphabet[index%len(alphabet)]
		index /= len(alphabet)
	}
	return string(encoded[position:])
}
