package eebusfacade

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
	shipmodel "github.com/Project-Helianthus/helianthus-ship-go/model"
)

func TestIssue60ExactOutgoingHandshakeBindsTransientTrustBeforeAccessMethods(t *testing.T) {
	product := newMSP04CFixture(t)
	coordinator := product.newCoordinator()
	if got := coordinator.reopen(context.Background()); got != "pairing_closed" {
		t.Fatalf("reopen = %q, want pairing_closed", got)
	}
	service := &issue60Service{}
	facade, err := newFirstTrustFacade(service, coordinator)
	if err != nil {
		t.Fatal(err)
	}
	coordinator.mu.Lock()
	coordinator.effects = facade
	coordinator.mu.Unlock()
	bridge := newFirstTrustOutgoingAttemptBridge(&runtimeFirstTrustResources{coordinator: coordinator})
	bridge.bindTLSLifecycle(facade)
	if got := coordinator.openPairingWindow(context.Background(), "open-exact-outgoing", firstTrustMaximumWindow); got != "open_empty" {
		t.Fatalf("open = %q, want open_empty", got)
	}
	var permit shipapi.OutgoingAttemptPermit
	service.queue = func(_ string, expectedSKI string) error {
		facade.ServicePairingDetailUpdate(
			expectedSKI,
			shipapi.NewConnectionStateDetail(shipapi.ConnectionStateQueued, nil),
		)
		handle, prepareErr := bridge.Prepare(shipapi.OutgoingAttemptRequest{
			RemoteSKI: expectedSKI,
			Endpoint:  shipapi.OutgoingAttemptEndpoint{Host: "peer.invalid", Port: 4712},
			Path:      "/ship/",
		})
		if prepareErr != nil {
			return prepareErr
		}
		var authorizeErr error
		permit, authorizeErr = bridge.AuthorizeLaunch(handle)
		if authorizeErr != nil {
			return authorizeErr
		}
		if permit.Decision != shipapi.OutgoingAttemptDecisionPermit {
			return errors.New("outgoing attempt was denied")
		}
		return nil
	}
	facade.VisiblePairingCandidatesUpdated(nil, []shipapi.PairingCandidateRef{{
		CandidateRef: "candidate-exact-outgoing",
		SKI:          issue56SKIA,
	}})
	if got := coordinator.selectCandidate(
		context.Background(), "select-exact-outgoing", "candidate-exact-outgoing", issue56SKIA,
	); got != "candidate_queued" {
		t.Fatalf("select = %q, want candidate_queued", got)
	}
	remote, remoteSKI, ok := decodeFirstTrustSKI(issue56SKIA)
	if !ok {
		t.Fatal("fixture SKI is invalid")
	}
	if permit.Decision != shipapi.OutgoingAttemptDecisionPermit {
		t.Fatal("normal candidate queue did not authorize an outgoing attempt")
	}

	fingerprint, nonce, expiresAt, candidateConnection, generation, _, ok := coordinator.candidate()
	if !ok || candidateConnection == 0 {
		t.Fatal("exact outgoing candidate binding is unavailable")
	}
	confirm := func() string {
		return coordinator.confirm(
			context.Background(), "confirm-exact-outgoing", fingerprint, nonce, expiresAt, candidateConnection, generation,
		)
	}
	if got := confirm(); got != "association_incomplete" {
		t.Fatalf("confirmation before exact TLS callback = %q, want association_incomplete", got)
	}

	stale := permit.Metadata
	stale.ControlEpoch++
	tlsReady := shipmodel.ShipState{State: shipmodel.CmiStateInitStart}
	bridge.OutgoingAttemptHandshakeStateUpdate(remoteSKI, tlsReady, stale)
	bridge.OutgoingAttemptHandshakeStateUpdate(issue56SKIB, tlsReady, permit.Metadata)
	if got := confirm(); got != "association_incomplete" {
		t.Fatalf("confirmation after stale TLS callbacks = %q, want association_incomplete", got)
	}

	bridge.OutgoingAttemptHandshakeStateUpdate(remoteSKI, tlsReady, permit.Metadata)
	bridge.OutgoingAttemptHandshakeStateUpdate(remoteSKI, tlsReady, permit.Metadata)
	if got := confirm(); got != "transient_trusted" {
		t.Fatalf("confirmation after exact TLS callback = %q, want transient_trusted", got)
	}
	if got := service.registerCount(); got != 1 {
		t.Fatalf("transient registration calls = %d, want 1", got)
	}

	facade.RemoteSKIConnected(nil, remoteSKI)
	facade.ServiceShipIDUpdate(remoteSKI, "ship-id-after-access")
	facade.ServicePairingDetailUpdate(
		remoteSKI,
		shipapi.NewConnectionStateDetail(shipapi.ConnectionStateCompleted, nil),
	)
	if got := confirm(); got != "trusted" {
		t.Fatalf("terminal confirmation replay = %q, want trusted", got)
	}
	if !coordinator.trusted(remote) {
		t.Fatal("Access Methods evidence did not publish durable trust")
	}
	if got := service.registerCount(); got != 1 {
		t.Fatalf("durable trust registration calls = %d, want 1", got)
	}
	if got := service.unregisterCount(); got != 0 {
		t.Fatalf("successful trust unregistration calls = %d, want 0", got)
	}
}

func TestIssue60OutgoingAttemptDoesNotAdoptUnrelatedGeneration(t *testing.T) {
	product := newMSP04CFixture(t)
	coordinator := product.newCoordinator()
	if got := coordinator.reopen(context.Background()); got != "pairing_closed" {
		t.Fatalf("reopen = %q, want pairing_closed", got)
	}
	eventSink := &issue60OutgoingGateCoordinator{firstTrustCoordinator: coordinator}
	service := &issue60Service{}
	facade, err := newFirstTrustFacade(service, eventSink)
	if err != nil {
		t.Fatal(err)
	}
	coordinator.mu.Lock()
	coordinator.effects = facade
	coordinator.mu.Unlock()
	bridge := newFirstTrustOutgoingAttemptBridge(&runtimeFirstTrustResources{coordinator: coordinator})
	bridge.bindTLSLifecycle(facade)
	if got := coordinator.openPairingWindow(context.Background(), "open-stale-generation", firstTrustMaximumWindow); got != "open_empty" {
		t.Fatalf("open = %q, want open_empty", got)
	}

	var permit shipapi.OutgoingAttemptPermit
	var generationAfterPrepare uint64
	service.queue = func(_ string, expectedSKI string) error {
		facade.ServicePairingDetailUpdate(
			expectedSKI,
			shipapi.NewConnectionStateDetail(shipapi.ConnectionStateQueued, nil),
		)
		handle, prepareErr := bridge.Prepare(shipapi.OutgoingAttemptRequest{
			RemoteSKI: expectedSKI,
			Endpoint:  shipapi.OutgoingAttemptEndpoint{Host: "peer.invalid", Port: 4712},
			Path:      "/ship/",
		})
		if prepareErr != nil {
			return prepareErr
		}
		_, _, _, _, generationAfterPrepare, _, _ = coordinator.candidate()
		product.store.mu.Lock()
		product.store.view.manifest.current.sequence = generationAfterPrepare + 100
		product.store.mu.Unlock()
		var authorizeErr error
		permit, authorizeErr = bridge.AuthorizeLaunch(handle)
		if authorizeErr != nil {
			return authorizeErr
		}
		if permit.Decision != shipapi.OutgoingAttemptDecisionPermit {
			return errors.New("outgoing attempt was denied")
		}
		return nil
	}
	facade.VisiblePairingCandidatesUpdated(nil, []shipapi.PairingCandidateRef{{
		CandidateRef: "candidate-stale-generation",
		SKI:          issue56SKIA,
	}})
	if got := coordinator.selectCandidate(
		context.Background(), "select-stale-generation", "candidate-stale-generation", issue56SKIA,
	); got != "candidate_queued" {
		t.Fatalf("select = %q, want candidate_queued", got)
	}

	fingerprint, nonce, expiresAt, connection, candidateGeneration, _, ok := coordinator.candidate()
	if !ok || generationAfterPrepare == 0 || candidateGeneration != generationAfterPrepare {
		t.Fatalf("candidate generation = %d, want exact prepared generation %d", candidateGeneration, generationAfterPrepare)
	}
	_, remoteSKI, ok := decodeFirstTrustSKI(issue56SKIA)
	if !ok {
		t.Fatal("fixture SKI is invalid")
	}
	bridge.OutgoingAttemptHandshakeStateUpdate(
		remoteSKI,
		shipmodel.ShipState{State: shipmodel.CmiStateInitStart},
		permit.Metadata,
	)
	if got := coordinator.confirm(
		context.Background(), "confirm-stale-generation", fingerprint, nonce, expiresAt, connection, candidateGeneration,
	); got != "store_generation_conflict" {
		t.Fatalf("confirmation after unrelated generation = %q, want store_generation_conflict", got)
	}
	if got := service.registerCount(); got != 0 {
		t.Fatalf("unrelated generation transient registrations = %d, want 0", got)
	}
}

func TestIssue60ExactOutgoingErrorRevokesTransientTrustExactlyOnceWithSynchronousUnregisterReentry(t *testing.T) {
	product := newMSP04CFixture(t)
	coordinator := product.newCoordinator()
	if got := coordinator.reopen(context.Background()); got != "pairing_closed" {
		t.Fatalf("reopen = %q, want pairing_closed", got)
	}
	eventSink := &issue60OutgoingGateCoordinator{firstTrustCoordinator: coordinator}
	service := &issue60Service{}
	facade, err := newFirstTrustFacade(service, eventSink)
	if err != nil {
		t.Fatal(err)
	}
	coordinator.mu.Lock()
	coordinator.effects = facade
	coordinator.mu.Unlock()
	bridge := newFirstTrustOutgoingAttemptBridge(&runtimeFirstTrustResources{coordinator: coordinator})
	bridge.bindLifecycle(facade)
	bridge.bindTLSLifecycle(facade)
	if got := coordinator.openPairingWindow(context.Background(), "open-exact-error", firstTrustMaximumWindow); got != "open_empty" {
		t.Fatalf("open = %q, want open_empty", got)
	}

	var permit shipapi.OutgoingAttemptPermit
	service.queue = func(_ string, expectedSKI string) error {
		facade.ServicePairingDetailUpdate(
			expectedSKI,
			shipapi.NewConnectionStateDetail(shipapi.ConnectionStateQueued, nil),
		)
		handle, prepareErr := bridge.Prepare(shipapi.OutgoingAttemptRequest{
			RemoteSKI: expectedSKI,
			Endpoint:  shipapi.OutgoingAttemptEndpoint{Host: "peer.invalid", Port: 4712},
			Path:      "/ship/",
		})
		if prepareErr != nil {
			return prepareErr
		}
		var authorizeErr error
		permit, authorizeErr = bridge.AuthorizeLaunch(handle)
		if authorizeErr != nil {
			return authorizeErr
		}
		if permit.Decision != shipapi.OutgoingAttemptDecisionPermit {
			return errors.New("outgoing attempt was denied")
		}
		return nil
	}
	facade.VisiblePairingCandidatesUpdated(nil, []shipapi.PairingCandidateRef{{
		CandidateRef: "candidate-exact-error",
		SKI:          issue56SKIA,
	}})
	if got := coordinator.selectCandidate(
		context.Background(), "select-exact-error", "candidate-exact-error", issue56SKIA,
	); got != "candidate_queued" {
		t.Fatalf("select = %q, want candidate_queued", got)
	}

	fingerprint, nonce, expiresAt, connection, generation, _, ok := coordinator.candidate()
	if !ok || connection == 0 {
		t.Fatal("exact outgoing candidate binding is unavailable")
	}
	_, remoteSKI, ok := decodeFirstTrustSKI(issue56SKIA)
	if !ok {
		t.Fatal("fixture SKI is invalid")
	}
	bridge.OutgoingAttemptHandshakeStateUpdate(
		remoteSKI,
		shipmodel.ShipState{State: shipmodel.CmiStateInitStart},
		permit.Metadata,
	)
	confirm := func() string {
		return coordinator.confirm(
			context.Background(), "confirm-exact-error", fingerprint, nonce, expiresAt, connection, generation,
		)
	}
	if got := confirm(); got != "transient_trusted" {
		t.Fatalf("confirm = %q, want transient_trusted", got)
	}
	if got := service.registerCount(); got != 1 {
		t.Fatalf("transient registrations = %d, want 1", got)
	}

	service.onUnregister = func(ski string) {
		facade.ServicePairingDetailUpdate(
			ski,
			shipapi.NewConnectionStateDetail(
				shipapi.ConnectionStateError,
				errors.New("synchronous unregister reentry"),
			),
		)
	}
	terminal := shipmodel.ShipState{State: shipmodel.SmeStateError, Error: errors.New("exact TLS terminal")}
	terminalDone := make(chan struct{})
	go func() {
		bridge.OutgoingAttemptHandshakeStateUpdate(remoteSKI, terminal, permit.Metadata)
		bridge.OutgoingAttemptHandshakeStateUpdate(remoteSKI, terminal, permit.Metadata)
		close(terminalDone)
	}()
	waitIssue60Done(t, terminalDone, "exact terminal")
	if got := service.unregisterCount(); got != 1 {
		t.Fatalf("exact terminal unregistrations = %d, want 1", got)
	}
	if got := confirm(); got != "connection_closed" {
		t.Fatalf("terminal confirmation replay = %q, want connection_closed", got)
	}
	if _, _, _, _, _, _, ok := coordinator.candidate(); ok {
		t.Fatal("exact terminal retained transient candidate")
	}
}

func TestIssue60MismatchedTransientCancelsDoNotConsumeIdempotencyCapacity(t *testing.T) {
	fixture := newIssue60Fixture(t)
	if got := fixture.confirm("confirm-cancel-capacity"); got != "transient_trusted" {
		t.Fatalf("confirm = %q, want transient_trusted", got)
	}

	for index := 0; index < 32; index++ {
		key := fmt.Sprintf("cancel-mismatch-%02d", index)
		if got := fixture.coordinator.cancel(
			context.Background(), key, "mismatched-nonce", fixture.binding.connection, fixture.binding.store,
		); got != "confirmation_mismatch" {
			t.Fatalf("mismatched cancel %d = %q, want confirmation_mismatch", index, got)
		}
	}

	const validKey = "cancel-after-mismatches"
	if got := fixture.coordinator.cancel(
		context.Background(), validKey, fixture.binding.nonce, fixture.binding.connection, fixture.binding.store,
	); got != "cancelled" {
		t.Fatalf("valid cancel after mismatches = %q, want cancelled", got)
	}
	if got := fixture.coordinator.cancel(
		context.Background(), validKey, fixture.binding.nonce, fixture.binding.connection, fixture.binding.store,
	); got != "cancelled" {
		t.Fatalf("valid cancel replay = %q, want cancelled", got)
	}
	if got := fixture.coordinator.cancel(
		context.Background(), validKey, "different-nonce", fixture.binding.connection, fixture.binding.store,
	); got != "idempotency_conflict" {
		t.Fatalf("valid cancel key conflict = %q, want idempotency_conflict", got)
	}
}

type issue60OutgoingGateCoordinator struct {
	*firstTrustCoordinator
}

func (*issue60OutgoingGateCoordinator) retryRuntimeEnabled() bool {
	return false
}

func TestIssue60EvidenceAndDisconnectAreSerialized(t *testing.T) {
	tests := []struct {
		name       string
		prime      func(*issue60Fixture)
		send       func(*issue60Fixture)
		setBarrier func(*issue60CoordinatorBarrier, *issue60CallBarrier)
	}{
		{
			name:  "SHIP ID",
			prime: func(fixture *issue60Fixture) { fixture.completed() },
			send:  func(fixture *issue60Fixture) { fixture.shipID() },
			setBarrier: func(coordinator *issue60CoordinatorBarrier, barrier *issue60CallBarrier) {
				coordinator.shipID = barrier
			},
		},
		{
			name:  "Completed",
			prime: func(fixture *issue60Fixture) { fixture.shipID() },
			send:  func(fixture *issue60Fixture) { fixture.completed() },
			setBarrier: func(coordinator *issue60CoordinatorBarrier, barrier *issue60CallBarrier) {
				coordinator.completed = barrier
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name+" evidence first", func(t *testing.T) {
			fixture := newIssue60Fixture(t)
			if got := fixture.confirm("confirm"); got != "transient_trusted" {
				t.Fatalf("confirm = %q, want transient_trusted", got)
			}
			fixture.connectAccessMethods()
			test.prime(fixture)

			evidenceBarrier := newIssue60CallBarrier()
			closedBarrier := newIssue60CallBarrier()
			coordinator := &issue60CoordinatorBarrier{
				firstTrustCoordinator: fixture.coordinator,
				closed:                closedBarrier,
			}
			test.setBarrier(coordinator, evidenceBarrier)
			fixture.facade.coordinator = coordinator

			evidenceDone := make(chan struct{})
			go func() {
				test.send(fixture)
				close(evidenceDone)
			}()
			evidenceBarrier.waitEntered(t)
			disconnectDone := make(chan struct{})
			go func() {
				fixture.facade.RemoteSKIDisconnected(nil, issue56SKIA)
				close(disconnectDone)
			}()
			select {
			case <-closedBarrier.entered:
				t.Fatal("disconnect overtook evidence after the evidence callback was ordered first")
			case <-time.After(50 * time.Millisecond):
			}

			evidenceBarrier.releaseCall()
			waitIssue60Done(t, evidenceDone, "evidence")
			waitIssue60Done(t, disconnectDone, "disconnect")
			if got := fixture.confirm("confirm"); got != "trusted" {
				t.Fatalf("evidence-first terminal replay = %q, want trusted", got)
			}
		})

		t.Run(test.name+" disconnect first", func(t *testing.T) {
			fixture := newIssue60Fixture(t)
			if got := fixture.confirm("confirm"); got != "transient_trusted" {
				t.Fatalf("confirm = %q, want transient_trusted", got)
			}
			fixture.connectAccessMethods()
			test.prime(fixture)

			evidenceBarrier := newIssue60CallBarrier()
			closedBarrier := newIssue60CallBarrier()
			coordinator := &issue60CoordinatorBarrier{
				firstTrustCoordinator: fixture.coordinator,
				closed:                closedBarrier,
			}
			test.setBarrier(coordinator, evidenceBarrier)
			fixture.facade.coordinator = coordinator

			disconnectDone := make(chan struct{})
			go func() {
				fixture.facade.RemoteSKIDisconnected(nil, issue56SKIA)
				close(disconnectDone)
			}()
			closedBarrier.waitEntered(t)
			evidenceDone := make(chan struct{})
			go func() {
				test.send(fixture)
				close(evidenceDone)
			}()
			waitIssue60Done(t, evidenceDone, "stale evidence")
			select {
			case <-evidenceBarrier.entered:
				t.Fatal("stale evidence reached the coordinator after disconnect was ordered first")
			default:
			}
			closedBarrier.releaseCall()
			waitIssue60Done(t, disconnectDone, "disconnect")
			assertMSP04BCommitCount(t, fixture.base.store, 0)
			if got := fixture.confirm("confirm"); got != "connection_closed" {
				t.Fatalf("disconnect-first terminal replay = %q, want connection_closed", got)
			}
		})
	}
}

type issue60CoordinatorBarrier struct {
	*firstTrustCoordinator
	shipID    *issue60CallBarrier
	completed *issue60CallBarrier
	closed    *issue60CallBarrier
}

func (coordinator *issue60CoordinatorBarrier) serviceShipIDUpdate(remote []byte, connection uint64, shipID string) string {
	coordinator.shipID.block()
	return coordinator.firstTrustCoordinator.serviceShipIDUpdate(remote, connection, shipID)
}

func (coordinator *issue60CoordinatorBarrier) connectionCompleted(remote []byte, connection uint64) string {
	coordinator.completed.block()
	return coordinator.firstTrustCoordinator.connectionCompleted(remote, connection)
}

func (coordinator *issue60CoordinatorBarrier) connectionClosed(remote []byte, connection uint64) string {
	coordinator.closed.block()
	return coordinator.firstTrustCoordinator.connectionClosed(remote, connection)
}

type issue60CallBarrier struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newIssue60CallBarrier() *issue60CallBarrier {
	return &issue60CallBarrier{entered: make(chan struct{}), release: make(chan struct{})}
}

func (barrier *issue60CallBarrier) block() {
	if barrier == nil {
		return
	}
	barrier.once.Do(func() { close(barrier.entered) })
	<-barrier.release
}

func (barrier *issue60CallBarrier) waitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-barrier.entered:
	case <-time.After(time.Second):
		t.Fatal("callback did not reach the forced interleaving barrier")
	}
}

func (barrier *issue60CallBarrier) releaseCall() {
	close(barrier.release)
}

func waitIssue60Done(t *testing.T, done <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("%s callback did not terminate", name)
	}
}

func TestIssue60ExactTLSBoundConfirmationStagesTransientTrust(t *testing.T) {
	fixture := newIssue60Fixture(t)
	generation := fixture.base.store.SelectedGeneration()

	if got := fixture.confirm("confirm"); got != "transient_trusted" {
		t.Fatalf("exact TLS-bound OOB confirmation = %q, want transient_trusted", got)
	}
	if got := fixture.service.registerCount(); got != 1 {
		t.Fatalf("RegisterRemoteSKI calls = %d, want 1", got)
	}
	if got := fixture.coordinator.state(); got != "TRANSIENT_TRUSTED" {
		t.Fatalf("coordinator state = %q, want TRANSIENT_TRUSTED", got)
	}
	if got := fixture.confirm("confirm"); got != "transient_trusted" {
		t.Fatalf("transient confirmation replay = %q, want transient_trusted", got)
	}
	if got := fixture.service.registerCount(); got != 1 {
		t.Fatalf("RegisterRemoteSKI replay calls = %d, want 1", got)
	}
	assertMSP04BCommitCount(t, fixture.base.store, 0)
	if got := fixture.base.store.SelectedGeneration(); got != generation {
		t.Fatalf("transient confirmation advanced durable generation to %d, want %d", got, generation)
	}
	if fixture.coordinator.trusted(fixture.remote) {
		t.Fatal("transient confirmation published final durable trust")
	}
}

func TestIssue60ConfirmationRequiresExactTLSBindingButNotSHIPID(t *testing.T) {
	fixture := newIssue60FixtureWithOptions(t, "commit_durable", false)
	if got := fixture.confirm("confirm"); got != "association_incomplete" {
		t.Fatalf("confirmation before TLS binding = %q, want association_incomplete", got)
	}
	assertMSP04BCommitCount(t, fixture.base.store, 0)
	if got := fixture.service.registerCount(); got != 0 {
		t.Fatalf("RegisterRemoteSKI before TLS binding = %d, want 0", got)
	}

	fixture.bindTLS(t)
	if got := fixture.confirm("confirm"); got != "transient_trusted" {
		t.Fatalf("confirmation after TLS binding = %q, want transient_trusted", got)
	}
	if got := fixture.service.registerCount(); got != 1 {
		t.Fatalf("RegisterRemoteSKI after TLS binding = %d, want 1", got)
	}
	assertMSP04BCommitCount(t, fixture.base.store, 0)
}

func TestIssue60PostAuthorizationEvidenceCommitsInEitherOrder(t *testing.T) {
	tests := []struct {
		name  string
		first func(*issue60Fixture)
		last  func(*issue60Fixture)
	}{
		{
			name:  "SHIP ID then completed",
			first: func(fixture *issue60Fixture) { fixture.shipID() },
			last:  func(fixture *issue60Fixture) { fixture.completed() },
		},
		{
			name:  "completed then SHIP ID",
			first: func(fixture *issue60Fixture) { fixture.completed() },
			last:  func(fixture *issue60Fixture) { fixture.shipID() },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newIssue60Fixture(t)
			generation := fixture.base.store.SelectedGeneration()
			if got := fixture.confirm("confirm"); got != "transient_trusted" {
				t.Fatalf("confirm = %q", got)
			}
			fixture.facade.ServicePairingDetailUpdate(
				issue56SKIA,
				shipapi.NewConnectionStateDetail(shipapi.ConnectionStateTrusted, nil),
			)
			test.first(fixture)
			assertMSP04BCommitCount(t, fixture.base.store, 0)
			if got := fixture.base.store.SelectedGeneration(); got != generation {
				t.Fatalf("single evidence advanced generation to %d, want %d", got, generation)
			}
			test.last(fixture)
			assertMSP04BCommitCount(t, fixture.base.store, 1)
			if !fixture.coordinator.trusted(fixture.remote) {
				t.Fatal("both evidence events did not publish final trust")
			}
			if got := fixture.confirm("confirm"); got != "trusted" {
				t.Fatalf("terminal confirmation replay = %q, want trusted", got)
			}
			if got := fixture.service.registerCount(); got != 1 {
				t.Fatalf("RegisterRemoteSKI calls = %d, want 1", got)
			}
			if got := fixture.service.unregisterCount(); got != 0 {
				t.Fatalf("UnregisterRemoteSKI calls = %d, want 0", got)
			}
		})
	}
}

func TestIssue60PreAuthorizationSHIPIDRequiresExactCompletion(t *testing.T) {
	fixture := newIssue60Fixture(t)
	fixture.facade.ServiceShipIDUpdate(issue56SKIA, "pre-authorization")
	if got := fixture.confirm("confirm"); got != "transient_trusted" {
		t.Fatalf("confirm = %q", got)
	}
	assertMSP04BCommitCount(t, fixture.base.store, 0)

	fixture.coordinator.serviceShipIDUpdate(fixture.remote, fixture.binding.connection+1, "stale")
	fixture.coordinator.connectionCompleted(fixture.remote, fixture.binding.connection+1)
	assertMSP04BCommitCount(t, fixture.base.store, 0)
	if got := fixture.service.unregisterCount(); got != 0 {
		t.Fatalf("stale generation revoked live transient trust %d times", got)
	}

	fixture.completed()
	assertMSP04BCommitCount(t, fixture.base.store, 1)
}

func TestIssue60TransientTerminalPathsRollbackExactlyOnce(t *testing.T) {
	tests := []struct {
		name      string
		terminate func(*testing.T, *issue60Fixture)
	}{
		{name: "candidate expiry", terminate: func(_ *testing.T, fixture *issue60Fixture) {
			advanceMSP04BClock(fixture.base.clock, firstTrustMaximumCandidate)
			_ = fixture.coordinator.state()
		}},
		{name: "disconnect", terminate: func(_ *testing.T, fixture *issue60Fixture) {
			fixture.facade.RemoteSKIDisconnected(nil, issue56SKIA)
		}},
		{name: "pairing error", terminate: func(_ *testing.T, fixture *issue60Fixture) {
			fixture.facade.ServicePairingDetailUpdate(
				issue56SKIA,
				shipapi.NewConnectionStateDetail(shipapi.ConnectionStateError, errors.New("terminal")),
			)
		}},
		{name: "cancel", terminate: func(t *testing.T, fixture *issue60Fixture) {
			if got := fixture.coordinator.cancel(
				context.Background(),
				"cancel",
				fixture.binding.nonce,
				fixture.binding.connection,
				fixture.binding.store,
			); got != "cancelled" {
				t.Fatalf("cancel = %q", got)
			}
		}},
		{name: "close", terminate: func(t *testing.T, fixture *issue60Fixture) {
			if got := fixture.coordinator.closePairingWindow(context.Background(), "close"); got != "pairing_closed" {
				t.Fatalf("close = %q", got)
			}
		}},
		{name: "shutdown", terminate: func(t *testing.T, fixture *issue60Fixture) {
			if err := fixture.coordinator.shutdown(); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newIssue60Fixture(t)
			generation := fixture.base.store.SelectedGeneration()
			if got := fixture.confirm("confirm"); got != "transient_trusted" {
				t.Fatalf("confirm = %q", got)
			}
			test.terminate(t, fixture)
			assertMSP04BCommitCount(t, fixture.base.store, 0)
			if got := fixture.base.store.SelectedGeneration(); got != generation {
				t.Fatalf("rollback advanced generation to %d, want %d", got, generation)
			}
			if fixture.coordinator.trusted(fixture.remote) {
				t.Fatal("rollback published durable trust")
			}
			if got := fixture.service.unregisterCount(); got != 1 {
				t.Fatalf("UnregisterRemoteSKI calls = %d, want 1", got)
			}
			if got := fixture.confirm("confirm"); got == "trust_outcome_unknown" {
				t.Fatal("known pre-store terminal returned trust_outcome_unknown")
			}
		})
	}
}

func TestIssue60SynchronousRegistrationReentryCommitsOnce(t *testing.T) {
	fixture := newIssue60Fixture(t)
	fixture.service.onRegister = func(string) {
		fixture.shipID()
		fixture.completed()
	}
	result := make(chan string, 1)
	go func() {
		result <- fixture.confirm("confirm")
	}()
	select {
	case got := <-result:
		if got != "transient_trusted" {
			t.Fatalf("reentrant confirmation = %q, want transient_trusted", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("synchronous RegisterRemoteSKI callback deadlocked")
	}
	assertMSP04BCommitCount(t, fixture.base.store, 1)
	if got := fixture.service.registerCount(); got != 1 {
		t.Fatalf("RegisterRemoteSKI calls = %d, want 1", got)
	}
	if got := fixture.confirm("confirm"); got != "trusted" {
		t.Fatalf("terminal replay = %q, want trusted", got)
	}
}

func TestIssue60ConcurrentConfirmAndEvidenceRemainAtMostOnce(t *testing.T) {
	fixture := newIssue60Fixture(t)
	const workers = 32
	results := make(chan string, workers)
	var confirms sync.WaitGroup
	for range workers {
		confirms.Add(1)
		go func() {
			defer confirms.Done()
			results <- fixture.confirm("confirm")
		}()
	}
	confirms.Wait()
	close(results)
	for result := range results {
		if result != "transient_trusted" {
			t.Fatalf("concurrent confirm = %q, want transient_trusted", result)
		}
	}
	if got := fixture.service.registerCount(); got != 1 {
		t.Fatalf("concurrent RegisterRemoteSKI calls = %d, want 1", got)
	}

	var evidence sync.WaitGroup
	for range workers {
		evidence.Add(2)
		go func() {
			defer evidence.Done()
			fixture.shipID()
		}()
		go func() {
			defer evidence.Done()
			fixture.completed()
		}()
	}
	evidence.Wait()
	assertMSP04BCommitCount(t, fixture.base.store, 1)
	if got := fixture.confirm("confirm"); got != "trusted" {
		t.Fatalf("terminal replay = %q, want trusted", got)
	}
	if got := fixture.service.unregisterCount(); got != 0 {
		t.Fatalf("successful concurrent completion unregistered %d times", got)
	}
}

func TestIssue60DeterministicStoreFailureRollsBackTransientTrust(t *testing.T) {
	fixture := newIssue60FixtureWithOptions(t, "commit_not_published", true)
	generation := fixture.base.store.SelectedGeneration()
	if got := fixture.confirm("confirm"); got != "transient_trusted" {
		t.Fatalf("confirm = %q", got)
	}
	fixture.shipID()
	fixture.completed()
	assertMSP04BCommitCount(t, fixture.base.store, 1)
	if got := fixture.confirm("confirm"); got != "failed_closed_unchanged" {
		t.Fatalf("failure replay = %q, want failed_closed_unchanged", got)
	}
	if got := fixture.base.store.SelectedGeneration(); got != generation {
		t.Fatalf("failed store advanced generation to %d, want %d", got, generation)
	}
	if got := fixture.service.unregisterCount(); got != 1 {
		t.Fatalf("UnregisterRemoteSKI calls = %d, want 1", got)
	}
	if fixture.coordinator.trusted(fixture.remote) {
		t.Fatal("deterministic store failure published trust")
	}
}

func TestIssue60StoreGenerationConflictRollsBackBeforePublication(t *testing.T) {
	fixture := newIssue60Fixture(t)
	generation := fixture.base.store.SelectedGeneration()
	if got := fixture.confirm("confirm"); got != "transient_trusted" {
		t.Fatalf("confirm = %q", got)
	}
	fixture.base.store.mu.Lock()
	fixture.base.store.selected = generation + 1
	fixture.base.store.mu.Unlock()
	fixture.shipID()
	fixture.completed()

	assertMSP04BCommitCount(t, fixture.base.store, 0)
	fixture.base.store.mu.Lock()
	durableGeneration := fixture.base.store.generation
	fixture.base.store.mu.Unlock()
	if durableGeneration != generation {
		t.Fatalf("generation conflict changed durable generation to %d, want %d", durableGeneration, generation)
	}
	if got := fixture.confirm("confirm"); got != "store_generation_conflict" {
		t.Fatalf("generation-conflict replay = %q, want store_generation_conflict", got)
	}
	if got := fixture.service.unregisterCount(); got != 1 {
		t.Fatalf("UnregisterRemoteSKI calls = %d, want 1", got)
	}
}

func TestIssue60RestartDoesNotReconstructVolatileTransientTrust(t *testing.T) {
	fixture := newIssue60Fixture(t)
	if got := fixture.confirm("confirm"); got != "transient_trusted" {
		t.Fatalf("confirm = %q", got)
	}
	restartedEffects := newMSP04BEffectsSpy(fixture.base.store.events)
	restarted := newFirstTrustCoordinator(fixture.base.clock.Now, nil, fixture.base.store, restartedEffects)
	if got := restarted.reopen(context.Background()); got != "pairing_closed" {
		t.Fatalf("restart reopen = %q", got)
	}
	if restarted.trusted(fixture.remote) {
		t.Fatal("restart reconstructed volatile transient trust")
	}
	assertMSP04BCommitCount(t, fixture.base.store, 0)
	if err := fixture.coordinator.shutdown(); err != nil {
		t.Fatal(err)
	}
	if got := fixture.service.unregisterCount(); got != 1 {
		t.Fatalf("shutdown UnregisterRemoteSKI calls = %d, want 1", got)
	}
}

func TestIssue60RecoveryStorePublishesAfterTransientEvidence(t *testing.T) {
	product := newMSP04CFixture(t)
	coordinator := product.newCoordinator()
	if got := coordinator.reopen(context.Background()); got != "pairing_closed" {
		t.Fatalf("recovery reopen = %q", got)
	}
	service := &issue60Service{}
	facade, err := newFirstTrustFacade(service, coordinator)
	if err != nil {
		t.Fatal(err)
	}
	coordinator.mu.Lock()
	coordinator.effects = facade
	coordinator.mu.Unlock()
	if got := coordinator.openPairingWindow(context.Background(), "open", firstTrustMaximumWindow); got != "open_empty" {
		t.Fatalf("open = %q", got)
	}
	service.queue = func(_ string, expectedSKI string) error {
		facade.ServicePairingDetailUpdate(
			expectedSKI,
			shipapi.NewConnectionStateDetail(shipapi.ConnectionStateQueued, nil),
		)
		return nil
	}
	facade.VisiblePairingCandidatesUpdated(nil, []shipapi.PairingCandidateRef{{
		CandidateRef: "candidate-a",
		SKI:          issue56SKIA,
	}})
	if got := coordinator.selectCandidate(context.Background(), "select", "candidate-a", issue56SKIA); got != "candidate_queued" {
		t.Fatalf("select = %q", got)
	}
	fingerprint, nonce, expiresAt, connection, generation, _, ok := coordinator.candidate()
	if !ok {
		t.Fatal("recovery candidate unavailable")
	}
	remote, _, ok := decodeFirstTrustSKI(issue56SKIA)
	if !ok || !facade.outgoingAttemptTLSBound(remote, connection) {
		t.Fatal("recovery candidate TLS binding failed")
	}
	if got := coordinator.confirm(
		context.Background(),
		"confirm",
		fingerprint,
		nonce,
		expiresAt,
		connection,
		generation,
	); got != "transient_trusted" {
		t.Fatalf("recovery confirm = %q", got)
	}
	if product.store.SelectedGeneration() != generation {
		t.Fatal("transient recovery confirmation advanced durable generation")
	}
	product.store.mu.Lock()
	commitsBeforeEvidence := product.store.commitCalls
	product.store.mu.Unlock()
	facade.RemoteSKIConnected(nil, issue56SKIA)
	facade.ServiceShipIDUpdate(issue56SKIA, "ship-id-a")
	facade.ServicePairingDetailUpdate(
		issue56SKIA,
		shipapi.NewConnectionStateDetail(shipapi.ConnectionStateCompleted, nil),
	)
	product.store.mu.Lock()
	commits := product.store.commitCalls
	product.store.mu.Unlock()
	if commits != commitsBeforeEvidence+1 {
		t.Fatalf("recovery CommitControl calls after evidence = %d, want %d", commits, commitsBeforeEvidence+1)
	}
	if got := coordinator.confirm(
		context.Background(),
		"confirm",
		fingerprint,
		nonce,
		expiresAt,
		connection,
		generation,
	); got != "trusted" {
		t.Fatalf("recovery terminal replay = %q, want trusted", got)
	}
	assertMSP04CState(t, coordinator, "PAIRED_TRUSTED", "")
	if got := service.registerCount(); got != 1 {
		t.Fatalf("recovery RegisterRemoteSKI calls = %d, want 1", got)
	}
}

type issue60Fixture struct {
	base        msp04bFixture
	coordinator *firstTrustCoordinator
	facade      *firstTrustFacade
	service     *issue60Service
	remote      []byte
	binding     msp04bBindings
	accessOnce  sync.Once
}

func newIssue60Fixture(t *testing.T) *issue60Fixture {
	return newIssue60FixtureWithOptions(t, "commit_durable", true)
}

func newIssue60FixtureWithOptions(t *testing.T, storeOutcome string, tlsBound bool) *issue60Fixture {
	t.Helper()
	base := newMSP04BFixture(t, storeOutcome)
	coordinator := base.coordinator.(*firstTrustCoordinator)
	service := &issue60Service{}
	facade, err := newFirstTrustFacade(service, coordinator)
	if err != nil {
		t.Fatal(err)
	}
	coordinator.mu.Lock()
	coordinator.effects = facade
	coordinator.mu.Unlock()
	if got := coordinator.openPairingWindow(context.Background(), "open", firstTrustMaximumWindow); got != "open_empty" {
		t.Fatalf("open pairing window = %q", got)
	}
	service.queue = func(_ string, expectedSKI string) error {
		facade.ServicePairingDetailUpdate(
			expectedSKI,
			shipapi.NewConnectionStateDetail(shipapi.ConnectionStateQueued, nil),
		)
		return nil
	}
	facade.VisiblePairingCandidatesUpdated(nil, []shipapi.PairingCandidateRef{{
		CandidateRef: "candidate-a",
		SKI:          issue56SKIA,
	}})
	if got := coordinator.selectCandidate(context.Background(), "select", "candidate-a", issue56SKIA); got != "candidate_queued" {
		t.Fatalf("select candidate = %q", got)
	}
	remote, _, ok := decodeFirstTrustSKI(issue56SKIA)
	if !ok {
		t.Fatal("test SKI is invalid")
	}
	fingerprint, nonce, expiresAt, connection, storeGeneration, complete, ok := coordinator.candidate()
	if !ok || complete || fingerprint != issue56SKIA || connection == 0 || storeGeneration == 0 {
		t.Fatalf("TLS-bound candidate = fingerprint=%q connection=%d store=%d complete=%t ok=%t", fingerprint, connection, storeGeneration, complete, ok)
	}
	fixture := &issue60Fixture{
		base:        base,
		coordinator: coordinator,
		facade:      facade,
		service:     service,
		remote:      remote,
		binding: msp04bBindings{
			fingerprint: fingerprint,
			nonce:       nonce,
			expiresAt:   expiresAt,
			connection:  connection,
			store:       storeGeneration,
		},
	}
	if tlsBound {
		fixture.bindTLS(t)
	}
	return fixture
}

func (fixture *issue60Fixture) confirm(key string) string {
	return confirmMSP04B(fixture.coordinator, key, fixture.binding)
}

func (fixture *issue60Fixture) shipID() {
	fixture.connectAccessMethods()
	fixture.facade.ServiceShipIDUpdate(issue56SKIA, "ship-id-a")
}

func (fixture *issue60Fixture) completed() {
	fixture.connectAccessMethods()
	fixture.facade.ServicePairingDetailUpdate(
		issue56SKIA,
		shipapi.NewConnectionStateDetail(shipapi.ConnectionStateCompleted, nil),
	)
}

func (fixture *issue60Fixture) connectAccessMethods() {
	fixture.accessOnce.Do(func() {
		fixture.facade.RemoteSKIConnected(nil, issue56SKIA)
	})
}

func (fixture *issue60Fixture) bindTLS(t *testing.T) {
	t.Helper()
	if !fixture.facade.outgoingAttemptTLSBound(fixture.remote, fixture.binding.connection) {
		t.Fatal("candidate TLS binding failed")
	}
}

type issue60Service struct {
	msp04bServiceSpy
	queue        func(string, string) error
	onRegister   func(string)
	onUnregister func(string)
	unregisters  int
	disconnects  int
}

func (service *issue60Service) QueuePairingCandidate(candidateRef, expectedSKI string) error {
	return service.queue(candidateRef, expectedSKI)
}

func (service *issue60Service) RegisterRemoteSKI(ski string) {
	service.mu.Lock()
	service.registers++
	callback := service.onRegister
	service.mu.Unlock()
	if callback != nil {
		callback(ski)
	}
}

func (service *issue60Service) DisconnectSKI(string, string) {
	service.mu.Lock()
	service.disconnects++
	service.mu.Unlock()
}

func (service *issue60Service) UnregisterRemoteSKI(ski string) {
	service.mu.Lock()
	service.unregisters++
	callback := service.onUnregister
	service.mu.Unlock()
	if callback != nil {
		callback(ski)
	}
}

func (service *issue60Service) registerCount() int {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.registers
}

func (service *issue60Service) unregisterCount() int {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.unregisters
}
