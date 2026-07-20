package eebusfacade

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"

	eebusapi "github.com/Project-Helianthus/helianthus-eebus-go/api"
	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
)

type msp04bCoordinatorContract interface {
	reopen(context.Context) string
	openPairingWindow(context.Context, string, time.Duration) string
	closePairingWindow(context.Context, string) string
	admit([]byte, uint64) string
	serviceShipIDUpdate([]byte, uint64, string) string
	connectionClosed([]byte, uint64) string
	confirm(context.Context, string, string, string, time.Time, uint64, uint64) string
	cancel(context.Context, string, string, uint64, uint64) string
	candidate() (string, string, time.Time, uint64, uint64, bool, bool)
	state() string
	trusted([]byte) bool
}

type msp04bFixture struct {
	coordinator msp04bCoordinatorContract
	store       *msp04bStoreSpy
	effects     *msp04bEffectsSpy
	clock       *msp04bClock
}

type msp04bBindings struct {
	fingerprint string
	nonce       string
	expiresAt   time.Time
	connection  uint64
	store       uint64
}

func TestMSP04BG02G03G04ClosedOpenSingleCandidateAndRace(t *testing.T) {
	fixture := newMSP04BFixture(t, "commit_durable")
	remoteA := msp04bRemote(t)
	remoteB := msp04bRemote(t)

	if got := fixture.coordinator.admit(remoteA, 1); got != "pairing_closed" {
		t.Fatalf("G02 closed admission outcome = %q", got)
	}
	assertMSP04BNoCandidate(t, fixture.coordinator)
	assertMSP04BCommitCount(t, fixture.store, 0)

	if got := fixture.coordinator.openPairingWindow(context.Background(), msp04bLabel(t), time.Minute); got != "open_empty" {
		t.Fatalf("open outcome = %q", got)
	}

	start := make(chan struct{})
	results := make(chan string, 2)
	var wait sync.WaitGroup
	for index, remote := range [][]byte{remoteA, remoteB} {
		wait.Add(1)
		go func(remote []byte, generation uint64) {
			defer wait.Done()
			<-start
			results <- fixture.coordinator.admit(remote, generation)
		}(remote, uint64(index+2))
	}
	close(start)
	wait.Wait()
	close(results)

	counts := map[string]int{}
	for result := range results {
		counts[result]++
	}
	if counts["candidate_pending"] != 1 || counts["candidate_busy"] != 1 {
		t.Fatalf("G04 race outcomes = %#v", counts)
	}
	if fixture.effects.cancelCount() < 2 {
		t.Fatal("closed-window peer and deterministic race loser were not cancelled")
	}
	_, _, _, _, _, complete, ok := fixture.coordinator.candidate()
	if !ok || complete {
		t.Fatal("G03 expected one association-incomplete volatile candidate")
	}
	assertMSP04BCommitCount(t, fixture.store, 0)
}

func TestMSP04BConfirmationBindsExactFingerprintNonceExpiryAndGenerations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*msp04bBindings)
	}{
		{name: "wrong fingerprint", mutate: func(binding *msp04bBindings) { binding.fingerprint = msp04bDifferentFingerprint(binding.fingerprint) }},
		{name: "uppercase fingerprint", mutate: func(binding *msp04bBindings) {
			binding.fingerprint = string(bytes.ToUpper([]byte(binding.fingerprint)))
		}},
		{name: "stale nonce", mutate: func(binding *msp04bBindings) { binding.nonce += "x" }},
		{name: "changed expiry", mutate: func(binding *msp04bBindings) { binding.expiresAt = binding.expiresAt.Add(time.Nanosecond) }},
		{name: "stale connection generation", mutate: func(binding *msp04bBindings) { binding.connection++ }},
		{name: "stale starting store generation", mutate: func(binding *msp04bBindings) { binding.store++ }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMSP04BFixture(t, "commit_durable")
			remote := msp04bRemote(t)
			binding := openMSP04BCandidate(t, fixture, remote, 7, true)
			attempt := binding
			test.mutate(&attempt)
			if got := confirmMSP04B(fixture.coordinator, msp04bLabel(t), attempt); got == "trusted" {
				t.Fatal("mismatched exact binding was trusted")
			}
			assertMSP04BCommitCount(t, fixture.store, 0)
			if _, nonce, expiry, generation, storeGeneration, _, ok := fixture.coordinator.candidate(); !ok || nonce != binding.nonce || expiry != binding.expiresAt || generation != binding.connection || storeGeneration != binding.store {
				t.Fatal("rejected confirmation cleared or replaced the candidate")
			}
		})
	}
}

func TestMSP04BAssociationIncompleteUntilMatchingSHIPIDAndNoEarlyPublication(t *testing.T) {
	fixture := newMSP04BFixture(t, "commit_durable")
	remote := msp04bRemote(t)
	binding := openMSP04BCandidate(t, fixture, remote, 11, false)
	key := msp04bLabel(t)

	if got := confirmMSP04B(fixture.coordinator, key, binding); got != "association_incomplete" {
		t.Fatalf("incomplete association outcome = %q", got)
	}
	assertMSP04BCommitCount(t, fixture.store, 0)

	fixture.coordinator.serviceShipIDUpdate(msp04bRemote(t), binding.connection, msp04bLabel(t))
	fixture.coordinator.serviceShipIDUpdate(remote, binding.connection+1, msp04bLabel(t))
	if got := confirmMSP04B(fixture.coordinator, key, binding); got != "association_incomplete" {
		t.Fatalf("mismatched SHIP update outcome = %q", got)
	}
	assertMSP04BCommitCount(t, fixture.store, 0)

	fixture.coordinator.serviceShipIDUpdate(remote, binding.connection, msp04bLabel(t))
	if got := confirmMSP04B(fixture.coordinator, key, binding); got != "trusted" {
		t.Fatalf("complete exact confirmation outcome = %q", got)
	}
	assertMSP04BCommitCount(t, fixture.store, 1)
	fixture.store.assertCompleteAssociation(t, remote)
}

func TestMSP04BStoreOutcomeMappingNoRetryAndApprovalOrdering(t *testing.T) {
	tests := []struct {
		storeOutcome string
		want         string
		state        string
		register     bool
	}{
		{storeOutcome: "commit_durable", want: "trusted", state: "PAIRING_CLOSED", register: true},
		{storeOutcome: "commit_not_published", want: "failed_closed_unchanged", state: "PAIRING_CLOSED"},
		{storeOutcome: "validation_failed", want: "failed_closed_unchanged", state: "PAIRING_CLOSED"},
		{storeOutcome: "key_provider_unavailable", want: "failed_closed_unchanged", state: "PAIRING_CLOSED"},
		{storeOutcome: "commit_applied_maintenance_failed", want: "applied_reopen_required", state: "DISABLED"},
		{storeOutcome: "commit_durability_unknown", want: "trust_outcome_unknown", state: "DISABLED"},
	}

	for _, test := range tests {
		t.Run(test.storeOutcome, func(t *testing.T) {
			fixture := newMSP04BFixture(t, test.storeOutcome)
			remote := msp04bRemote(t)
			binding := openMSP04BCandidate(t, fixture, remote, 17, true)
			key := msp04bLabel(t)

			if got := confirmMSP04B(fixture.coordinator, key, binding); got != test.want {
				t.Fatalf("mapped outcome = %q, want %q", got, test.want)
			}
			assertMSP04BCommitCount(t, fixture.store, 1)
			if got := fixture.coordinator.state(); got != test.state {
				t.Fatalf("terminal state = %q, want %q", got, test.state)
			}
			if got := fixture.effects.registerCount(); (got == 1) != test.register {
				t.Fatalf("RegisterRemoteSKI count = %d", got)
			}
			if test.register {
				fixture.effects.assertOrder(t, "commit", "waiting:false", "register")
			}

			_ = confirmMSP04B(fixture.coordinator, key, binding)
			assertMSP04BCommitCount(t, fixture.store, 1)
		})
	}
}

func TestMSP04BCommitClosesAdmissionWhileWaitingRemainsTrue(t *testing.T) {
	fixture := newMSP04BFixture(t, "commit_durable")
	fixture.store.blockCommit()
	remote := msp04bRemote(t)
	binding := openMSP04BCandidate(t, fixture, remote, 23, true)
	result := make(chan string, 1)
	go func() {
		result <- confirmMSP04B(fixture.coordinator, msp04bLabel(t), binding)
	}()
	waitMSP04BSignal(t, fixture.store.commitEntered, "Commit entry")

	if got := fixture.coordinator.state(); got != "COMMITTING" {
		t.Fatalf("state during blocked Commit = %q", got)
	}
	if !fixture.effects.waitingValue() {
		t.Fatal("winner lost waiting permission before durable publication")
	}
	if got := fixture.coordinator.admit(msp04bRemote(t), binding.connection+1); got == "candidate_pending" {
		t.Fatal("new candidate admitted after logical close")
	}
	fixture.store.releaseCommit()
	if got := waitMSP04BResult(t, result, "confirmation"); got != "trusted" {
		t.Fatalf("blocked confirmation outcome = %q", got)
	}
	fixture.effects.assertOrder(t, "commit", "waiting:false", "register")
}

func TestMSP04BRestartDropsVolatileStateAndReloadsOnlyDurableTrust(t *testing.T) {
	fixture := newMSP04BFixture(t, "commit_durable")
	remote := msp04bRemote(t)
	binding := openMSP04BCandidate(t, fixture, remote, 29, true)
	key := msp04bLabel(t)
	if got := confirmMSP04B(fixture.coordinator, key, binding); got != "trusted" {
		t.Fatalf("initial confirmation outcome = %q", got)
	}

	restartedEffects := newMSP04BEffectsSpy(fixture.store.events)
	restarted := newFirstTrustCoordinator(fixture.clock.Now, rand.Reader, fixture.store, restartedEffects)
	if got := restarted.state(); got != "DISABLED" {
		t.Fatalf("restart initial state = %q", got)
	}
	assertMSP04BNoCandidate(t, restarted)
	if got := restarted.reopen(context.Background()); got != "pairing_closed" {
		t.Fatalf("restart reopen outcome = %q", got)
	}
	if !restarted.trusted(remote) {
		t.Fatal("durable association was not reloaded")
	}
	_ = confirmMSP04B(restarted, key, binding)
	assertMSP04BCommitCount(t, fixture.store, 1)
}

func TestMSP04BFacadeAssignsOnlyAnUnambiguousInitialGeneration(t *testing.T) {
	fixture := newMSP04BFixture(t, "commit_durable")
	service := &msp04bServiceSpy{}
	adapter, err := newFirstTrustFacade(service, fixture.coordinator)
	if err != nil {
		t.Fatal(err)
	}
	var _ eebusapi.ServiceReaderInterface = adapter

	remote := msp04bRemote(t)
	ski := hex.EncodeToString(remote)
	shipID := msp04bLabel(t)
	if got := fixture.coordinator.openPairingWindow(context.Background(), msp04bLabel(t), time.Minute); got != "open_empty" {
		t.Fatalf("open outcome = %q", got)
	}
	adapter.ServiceShipIDUpdate(ski, shipID)
	adapter.ServicePairingDetailUpdate(ski, shipapi.NewConnectionStateDetail(shipapi.ConnectionStateReceivedPairingRequest, nil))
	_, _, _, firstGeneration, _, complete, ok := fixture.coordinator.candidate()
	if !ok || !complete || firstGeneration == 0 {
		t.Fatal("pre-pair SHIP update was not bound to one facade-assigned generation")
	}

	adapter.ServicePairingDetailUpdate(hex.EncodeToString(msp04bRemote(t)), shipapi.NewConnectionStateDetail(shipapi.ConnectionStateReceivedPairingRequest, nil))
	if service.cancelCount() != 1 {
		t.Fatal("facade did not cancel the candidate_busy peer")
	}
	adapter.RemoteSKIDisconnected(nil, ski)
	adapter.ServicePairingDetailUpdate(ski, shipapi.NewConnectionStateDetail(shipapi.ConnectionStateReceivedPairingRequest, nil))
	if _, _, _, _, _, _, ok := fixture.coordinator.candidate(); ok {
		t.Fatal("same-SKI reconnect created a replacement generation from tokenless callbacks")
	}
	if service.cancelCount() != 2 {
		t.Fatalf("facade cancellation count = %d, want busy peer and ambiguous reconnect", service.cancelCount())
	}
}

func TestFirstTrustFacadePropagatesInitialPairingRegistrationFailure(t *testing.T) {
	fixture := newMSP04BFixture(t, "commit_durable")
	wantErr := errors.New("registration unavailable")
	service := &msp04bServiceSpy{waitingErr: wantErr}
	if _, err := newFirstTrustFacade(service, fixture.coordinator); !errors.Is(err, wantErr) {
		t.Fatalf("constructor error = %v, want %v", err, wantErr)
	}
	assertMSP04BPairingRegistrationFault(t, fixture.coordinator)
	if got := fixture.coordinator.reopen(context.Background()); got != "pairing_registration_failed" {
		t.Fatalf("reopen after constructor fault = %q", got)
	}
	assertMSP04BPairingRegistrationFault(t, fixture.coordinator)
}

func TestFirstTrustPairingRegistrationFailuresAreFailClosed(t *testing.T) {
	t.Run("open", func(t *testing.T) {
		fixture := newMSP04BFixture(t, "commit_durable")
		fixture.effects.enableErr = errors.New("announce failed")
		if got := fixture.coordinator.openPairingWindow(context.Background(), msp04bLabel(t), time.Minute); got != "pairing_registration_failed" {
			t.Fatalf("open outcome = %q", got)
		}
		if got := fixture.coordinator.state(); got != "DISABLED" {
			t.Fatalf("state = %q, want DISABLED", got)
		}
		assertMSP04BPairingRegistrationFault(t, fixture.coordinator)
	})

	t.Run("close", func(t *testing.T) {
		fixture := newMSP04BFixture(t, "commit_durable")
		if got := fixture.coordinator.openPairingWindow(context.Background(), msp04bLabel(t), time.Minute); got != "open_empty" {
			t.Fatalf("open outcome = %q", got)
		}
		fixture.effects.disableErr = errors.New("withdraw failed")
		if got := fixture.coordinator.closePairingWindow(context.Background(), msp04bLabel(t)); got != "pairing_registration_failed" {
			t.Fatalf("close outcome = %q", got)
		}
		if got := fixture.coordinator.state(); got != "DISABLED" {
			t.Fatalf("state = %q, want DISABLED", got)
		}
		assertMSP04BPairingRegistrationFault(t, fixture.coordinator)
	})

	t.Run("durable confirmation close", func(t *testing.T) {
		fixture := newMSP04BFixture(t, "commit_durable")
		binding := openMSP04BCandidate(t, fixture, msp04bRemote(t), 43, true)
		fixture.effects.disableErr = errors.New("withdraw after commit failed")
		if got := confirmMSP04B(fixture.coordinator, msp04bLabel(t), binding); got != "pairing_registration_failed" {
			t.Fatalf("confirmation outcome = %q", got)
		}
		assertMSP04BPairingRegistrationFault(t, fixture.coordinator)
		if got := fixture.effects.registerCount(); got != 0 {
			t.Fatalf("registration count = %d after withdrawal failure", got)
		}
	})
}

func assertMSP04BPairingRegistrationFault(t *testing.T, contract msp04bCoordinatorContract) {
	t.Helper()
	coordinator, ok := contract.(*firstTrustCoordinator)
	if !ok {
		t.Fatal("coordinator implementation changed")
	}
	if got := coordinator.state(); got != "DISABLED" {
		t.Fatalf("pairing registration fault phase = %q, want DISABLED", got)
	}
	if got := coordinator.recoveryState(); got != "QUARANTINED" {
		t.Fatalf("pairing registration fault recovery = %q, want QUARANTINED", got)
	}
	if got := coordinator.recoveryReason(); got != "PAIRING_REGISTRATION_FAILED" {
		t.Fatalf("pairing registration fault reason = %q, want PAIRING_REGISTRATION_FAILED", got)
	}
}

func newMSP04BFixture(t *testing.T, commitOutcome string) msp04bFixture {
	t.Helper()
	events := &msp04bEventLog{}
	store := &msp04bStoreSpy{
		generation:    41,
		associations:  map[string]string{},
		commitOutcome: commitOutcome,
		events:        events,
		commitEntered: make(chan struct{}),
	}
	effects := newMSP04BEffectsSpy(events)
	clock := &msp04bClock{now: time.Unix(1_900_000_000, 0)}
	coordinator := newFirstTrustCoordinator(clock.Now, rand.Reader, store, effects)
	if got := coordinator.state(); got != "DISABLED" {
		t.Fatalf("initial state = %q", got)
	}
	if got := coordinator.reopen(context.Background()); got != "pairing_closed" {
		t.Fatalf("reopen outcome = %q", got)
	}
	return msp04bFixture{coordinator: coordinator, store: store, effects: effects, clock: clock}
}

func openMSP04BCandidate(t *testing.T, fixture msp04bFixture, remote []byte, generation uint64, complete bool) msp04bBindings {
	t.Helper()
	if got := fixture.coordinator.openPairingWindow(context.Background(), msp04bLabel(t), time.Minute); got != "open_empty" {
		t.Fatalf("open outcome = %q", got)
	}
	if got := fixture.coordinator.admit(remote, generation); got != "candidate_pending" {
		t.Fatalf("admit outcome = %q", got)
	}
	if complete {
		fixture.coordinator.serviceShipIDUpdate(remote, generation, msp04bLabel(t))
	}
	fingerprint, nonce, expiresAt, connection, storeGeneration, _, ok := fixture.coordinator.candidate()
	if !ok || fingerprint != hex.EncodeToString(remote) || nonce == "" || !expiresAt.After(fixture.clock.Now()) || connection != generation || storeGeneration != fixture.store.SelectedGeneration() {
		t.Fatal("candidate bindings were incomplete or not exact")
	}
	return msp04bBindings{fingerprint: fingerprint, nonce: nonce, expiresAt: expiresAt, connection: connection, store: storeGeneration}
}

func confirmMSP04B(coordinator msp04bCoordinatorContract, key string, binding msp04bBindings) string {
	return coordinator.confirm(context.Background(), key, binding.fingerprint, binding.nonce, binding.expiresAt, binding.connection, binding.store)
}

func assertMSP04BNoCandidate(t *testing.T, coordinator msp04bCoordinatorContract) {
	t.Helper()
	if _, _, _, _, _, _, ok := coordinator.candidate(); ok {
		t.Fatal("unexpected candidate")
	}
}

func msp04bRemote(t *testing.T) []byte {
	t.Helper()
	value := make([]byte, 20)
	if _, err := rand.Read(value); err != nil {
		t.Fatal(err)
	}
	return value
}

func msp04bLabel(t *testing.T) string {
	t.Helper()
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(value)
}

func msp04bDifferentFingerprint(value string) string {
	if value[0] == '0' {
		return "1" + value[1:]
	}
	return "0" + value[1:]
}

type msp04bClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *msp04bClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

type msp04bEventLog struct {
	mu     sync.Mutex
	events []string
}

func (log *msp04bEventLog) add(event string) {
	log.mu.Lock()
	log.events = append(log.events, event)
	log.mu.Unlock()
}

func (log *msp04bEventLog) snapshot() []string {
	log.mu.Lock()
	defer log.mu.Unlock()
	return append([]string(nil), log.events...)
}

type msp04bStoreSpy struct {
	mu             sync.Mutex
	generation     uint64
	associations   map[string]string
	commitOutcome  string
	commitCalls    int
	commitExpected uint64
	commitRemote   []byte
	commitSHIPID   string
	events         *msp04bEventLog
	commitEntered  chan struct{}
	enteredOnce    sync.Once
	release        chan struct{}
}

func (store *msp04bStoreSpy) Reload(context.Context) (uint64, map[string]string, string) {
	store.mu.Lock()
	defer store.mu.Unlock()
	associations := make(map[string]string, len(store.associations))
	for key, value := range store.associations {
		associations[key] = value
	}
	return store.generation, associations, "opened_current"
}

func (store *msp04bStoreSpy) SelectedGeneration() uint64 {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.generation
}

func (store *msp04bStoreSpy) Commit(ctx context.Context, expected uint64, remote []byte, shipID string) string {
	store.mu.Lock()
	store.commitCalls++
	store.commitExpected = expected
	store.commitRemote = bytes.Clone(remote)
	store.commitSHIPID = shipID
	store.mu.Unlock()
	store.events.add("commit")
	store.enteredOnce.Do(func() { close(store.commitEntered) })
	if store.release != nil {
		select {
		case <-store.release:
		case <-ctx.Done():
			return "commit_durability_unknown"
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.commitOutcome == "commit_durable" {
		store.associations[string(remote)] = shipID
		store.generation++
	}
	return store.commitOutcome
}

func (store *msp04bStoreSpy) blockCommit() {
	store.release = make(chan struct{})
}

func (store *msp04bStoreSpy) releaseCommit() {
	close(store.release)
}

func (store *msp04bStoreSpy) assertCompleteAssociation(t *testing.T, remote []byte) {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	if !bytes.Equal(store.commitRemote, remote) || store.commitSHIPID == "" || store.commitExpected == 0 {
		t.Fatal("store did not receive one complete generation-bound association")
	}
}

func assertMSP04BCommitCount(t *testing.T, store *msp04bStoreSpy, want int) {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.commitCalls != want {
		t.Fatalf("Commit calls = %d, want %d", store.commitCalls, want)
	}
}

type msp04bEffectsSpy struct {
	mu         sync.Mutex
	waiting    bool
	cancels    int
	registers  int
	live       bool
	eventLog   *msp04bEventLog
	enableErr  error
	disableErr error
}

func newMSP04BEffectsSpy(events *msp04bEventLog) *msp04bEffectsSpy {
	return &msp04bEffectsSpy{live: true, eventLog: events}
}

func (effects *msp04bEffectsSpy) setWaiting(value bool) error {
	effects.mu.Lock()
	effects.waiting = value
	effects.mu.Unlock()
	effects.eventLog.add("waiting:" + map[bool]string{true: "true", false: "false"}[value])
	if value {
		return effects.enableErr
	}
	return effects.disableErr
}

func (effects *msp04bEffectsSpy) cancelRemote([]byte, uint64) {
	effects.mu.Lock()
	effects.cancels++
	effects.mu.Unlock()
	effects.eventLog.add("cancel")
}

func (effects *msp04bEffectsSpy) connectionAlive([]byte, uint64) bool {
	effects.mu.Lock()
	defer effects.mu.Unlock()
	return effects.live
}

func (effects *msp04bEffectsSpy) registerRemoteSKI([]byte, uint64) {
	effects.mu.Lock()
	effects.registers++
	effects.mu.Unlock()
	effects.eventLog.add("register")
}

func (effects *msp04bEffectsSpy) cancelCount() int {
	effects.mu.Lock()
	defer effects.mu.Unlock()
	return effects.cancels
}

func (effects *msp04bEffectsSpy) registerCount() int {
	effects.mu.Lock()
	defer effects.mu.Unlock()
	return effects.registers
}

func (effects *msp04bEffectsSpy) waitingValue() bool {
	effects.mu.Lock()
	defer effects.mu.Unlock()
	return effects.waiting
}

func (effects *msp04bEffectsSpy) assertOrder(t *testing.T, ordered ...string) {
	t.Helper()
	events := effects.eventLog.snapshot()
	position := -1
	for _, want := range ordered {
		found := -1
		for index := position + 1; index < len(events); index++ {
			if events[index] == want {
				found = index
				break
			}
		}
		if found < 0 {
			t.Fatalf("event %q missing from ordered event categories %v", want, events)
		}
		position = found
	}
}

type msp04bServiceSpy struct {
	mu         sync.Mutex
	cancels    int
	registers  int
	waiting    []bool
	auto       []bool
	waitingErr error
}

func (*msp04bServiceSpy) Setup() error                               { return nil }
func (*msp04bServiceSpy) Start()                                     {}
func (*msp04bServiceSpy) Shutdown()                                  {}
func (*msp04bServiceSpy) LocalService() *shipapi.ServiceDetails      { return nil }
func (*msp04bServiceSpy) LocalDevice() spineapi.DeviceLocalInterface { return nil }

func (service *msp04bServiceSpy) SetAutoAccept(value bool) {
	service.mu.Lock()
	service.auto = append(service.auto, value)
	service.mu.Unlock()
}

func (service *msp04bServiceSpy) RegisterRemoteSKI(string) {
	service.mu.Lock()
	service.registers++
	service.mu.Unlock()
}

func (service *msp04bServiceSpy) CancelPairingWithSKI(string) {
	service.mu.Lock()
	service.cancels++
	service.mu.Unlock()
}

func (service *msp04bServiceSpy) SetPairingRegistration(value bool) error {
	service.mu.Lock()
	service.waiting = append(service.waiting, value)
	service.mu.Unlock()
	return service.waitingErr
}

func (service *msp04bServiceSpy) cancelCount() int {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.cancels
}

func waitMSP04BSignal(t *testing.T, signal <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func waitMSP04BResult(t *testing.T, result <-chan string, label string) string {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
		return ""
	}
}
