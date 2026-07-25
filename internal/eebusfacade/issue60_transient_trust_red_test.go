package eebusfacade

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
)

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

	fixture.facade.RemoteSKIConnected(nil, issue56SKIA)
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

func TestIssue60PreAuthorizationSHIPIDAndStaleGenerationDoNotCommit(t *testing.T) {
	fixture := newIssue60Fixture(t)
	fixture.facade.ServiceShipIDUpdate(issue56SKIA, "pre-authorization")
	if got := fixture.confirm("confirm"); got != "transient_trusted" {
		t.Fatalf("confirm = %q", got)
	}
	fixture.completed()
	assertMSP04BCommitCount(t, fixture.base.store, 0)

	fixture.coordinator.serviceShipIDUpdate(fixture.remote, fixture.binding.connection+1, "stale")
	fixture.coordinator.connectionCompleted(fixture.remote, fixture.binding.connection+1)
	assertMSP04BCommitCount(t, fixture.base.store, 0)
	if got := fixture.service.unregisterCount(); got != 0 {
		t.Fatalf("stale generation revoked live transient trust %d times", got)
	}

	fixture.shipID()
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
		facade.RemoteSKIConnected(nil, expectedSKI)
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
		if tlsBound {
			facade.RemoteSKIConnected(nil, expectedSKI)
		} else {
			facade.ServicePairingDetailUpdate(
				expectedSKI,
				shipapi.NewConnectionStateDetail(shipapi.ConnectionStateQueued, nil),
			)
		}
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
	return &issue60Fixture{
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
}

func (fixture *issue60Fixture) confirm(key string) string {
	return confirmMSP04B(fixture.coordinator, key, fixture.binding)
}

func (fixture *issue60Fixture) shipID() {
	fixture.facade.ServiceShipIDUpdate(issue56SKIA, "ship-id-a")
}

func (fixture *issue60Fixture) completed() {
	fixture.facade.ServicePairingDetailUpdate(
		issue56SKIA,
		shipapi.NewConnectionStateDetail(shipapi.ConnectionStateCompleted, nil),
	)
}

type issue60Service struct {
	msp04bServiceSpy
	queue       func(string, string) error
	onRegister  func(string)
	unregisters int
	disconnects int
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

func (service *issue60Service) UnregisterRemoteSKI(string) {
	service.mu.Lock()
	service.unregisters++
	service.mu.Unlock()
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
