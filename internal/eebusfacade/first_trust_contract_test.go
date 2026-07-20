package eebusfacade

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"reflect"
	"sync"
	"testing"
	"time"

	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
)

func TestFirstTrustExpiryAndRetiredReplayNeverReopen(t *testing.T) {
	fixture := newMSP04BFixture(t, "commit_durable")
	windowKey := msp04bLabel(t)
	if got := fixture.coordinator.openPairingWindow(context.Background(), windowKey, 3*time.Minute); got != "open_empty" {
		t.Fatalf("open outcome = %q", got)
	}
	if got := fixture.coordinator.closePairingWindow(context.Background(), windowKey); got != "idempotency_conflict" {
		t.Fatalf("cross-command active key reuse = %q", got)
	}
	remote := msp04bRemote(t)
	if got := fixture.coordinator.admit(remote, 101); got != "candidate_pending" {
		t.Fatalf("admit outcome = %q", got)
	}
	fingerprint, nonce, expiresAt, connection, storeGeneration, _, ok := fixture.coordinator.candidate()
	if !ok {
		t.Fatal("candidate missing")
	}
	requestKey := msp04bLabel(t)
	binding := msp04bBindings{fingerprint: fingerprint, nonce: nonce, expiresAt: expiresAt, connection: connection, store: storeGeneration}
	if got := confirmMSP04B(fixture.coordinator, requestKey, binding); got != "association_incomplete" {
		t.Fatalf("incomplete outcome = %q", got)
	}

	advanceMSP04BClock(fixture.clock, firstTrustMaximumCandidate)
	if got := fixture.coordinator.state(); got != "OPEN_EMPTY" {
		t.Fatalf("candidate expiry state = %q", got)
	}
	if got := confirmMSP04B(fixture.coordinator, requestKey, binding); got != "candidate_expired" {
		t.Fatalf("candidate replay outcome = %q", got)
	}
	if got := fixture.coordinator.openPairingWindow(context.Background(), windowKey, 3*time.Minute); got != "open_empty" {
		t.Fatalf("active open replay outcome = %q", got)
	}

	advanceMSP04BClock(fixture.clock, time.Minute)
	if got := fixture.coordinator.state(); got != "PAIRING_CLOSED" {
		t.Fatalf("window expiry state = %q", got)
	}
	if got := fixture.coordinator.openPairingWindow(context.Background(), windowKey, 3*time.Minute); got != "pairing_closed" {
		t.Fatalf("terminal open replay outcome = %q", got)
	}
	advanceMSP04BClock(fixture.clock, firstTrustReplayTTL+time.Nanosecond)
	if got := fixture.coordinator.openPairingWindow(context.Background(), windowKey, 3*time.Minute); got != "stale_request" {
		t.Fatalf("retired open replay outcome = %q", got)
	}
	assertMSP04BCommitCount(t, fixture.store, 0)
	if fixture.effects.waitingValue() {
		t.Fatal("waiting permission remained enabled after window expiry")
	}
}

func TestFirstTrustConfirmCancelLinearizationAndDisconnectFence(t *testing.T) {
	t.Run("confirm first", func(t *testing.T) {
		fixture := newMSP04BFixture(t, "commit_durable")
		fixture.store.blockCommit()
		remote := msp04bRemote(t)
		binding := openMSP04BCandidate(t, fixture, remote, 111, true)
		confirmKey := msp04bLabel(t)
		result := make(chan string, 1)
		go func() { result <- confirmMSP04B(fixture.coordinator, confirmKey, binding) }()
		waitMSP04BSignal(t, fixture.store.commitEntered, "Commit entry")
		if got := fixture.coordinator.cancel(context.Background(), confirmKey, binding.nonce, binding.connection, binding.store); got != "idempotency_conflict" {
			t.Fatalf("cross-command in-flight key reuse = %q", got)
		}
		if got := fixture.coordinator.cancel(context.Background(), msp04bLabel(t), binding.nonce, binding.connection, binding.store); got != "commit_in_progress" {
			t.Fatalf("losing cancel outcome = %q", got)
		}
		fixture.store.releaseCommit()
		if got := waitMSP04BResult(t, result, "confirmation"); got != "trusted" {
			t.Fatalf("confirmation outcome = %q", got)
		}
		assertMSP04BCommitCount(t, fixture.store, 1)
	})

	t.Run("cancel first", func(t *testing.T) {
		fixture := newMSP04BFixture(t, "commit_durable")
		remote := msp04bRemote(t)
		binding := openMSP04BCandidate(t, fixture, remote, 112, true)
		if got := fixture.coordinator.cancel(context.Background(), msp04bLabel(t), binding.nonce, binding.connection, binding.store); got != "cancelled" {
			t.Fatalf("cancel outcome = %q", got)
		}
		if got := confirmMSP04B(fixture.coordinator, msp04bLabel(t), binding); got != "stale_request" {
			t.Fatalf("losing confirmation outcome = %q", got)
		}
		assertMSP04BCommitCount(t, fixture.store, 0)
	})

	t.Run("disconnect during commit", func(t *testing.T) {
		fixture := newMSP04BFixture(t, "commit_durable")
		fixture.store.blockCommit()
		remote := msp04bRemote(t)
		binding := openMSP04BCandidate(t, fixture, remote, 113, true)
		result := make(chan string, 1)
		go func() { result <- confirmMSP04B(fixture.coordinator, msp04bLabel(t), binding) }()
		waitMSP04BSignal(t, fixture.store.commitEntered, "Commit entry")
		fixture.effects.mu.Lock()
		fixture.effects.live = false
		fixture.effects.mu.Unlock()
		if got := fixture.coordinator.connectionClosed(remote, binding.connection); got != "commit_in_progress" {
			t.Fatalf("disconnect outcome = %q", got)
		}
		fixture.store.releaseCommit()
		if got := waitMSP04BResult(t, result, "confirmation"); got != "trusted" {
			t.Fatalf("confirmation outcome = %q", got)
		}
		if fixture.effects.registerCount() != 0 {
			t.Fatal("disconnected generation was registered")
		}
	})
}

func TestFirstTrustTerminalRetentionExpiresAndRecoversCapacity(t *testing.T) {
	fixture := newMSP04BFixture(t, "commit_durable")
	coordinator, ok := fixture.coordinator.(*firstTrustCoordinator)
	if !ok {
		t.Fatal("fixture coordinator type changed")
	}
	var firstOpenKey string
	for index := 0; index < firstTrustMaximumIdempotency/2; index++ {
		openKey := msp04bLabel(t)
		if index == 0 {
			firstOpenKey = openKey
		}
		if got := fixture.coordinator.openPairingWindow(context.Background(), openKey, time.Minute); got != "open_empty" {
			t.Fatalf("open at capacity index %d = %q", index, got)
		}
		if got := fixture.coordinator.closePairingWindow(context.Background(), msp04bLabel(t)); got != "pairing_closed" {
			t.Fatalf("close at capacity index %d = %q", index, got)
		}
	}
	coordinator.mu.Lock()
	if len(coordinator.replays) > firstTrustMaximumReplayEntries || len(coordinator.retired) > firstTrustMaximumRetiredKeys {
		coordinator.mu.Unlock()
		t.Fatal("terminal retention exceeded its entry bounds")
	}
	coordinator.mu.Unlock()
	retiredType := reflect.TypeOf(firstTrustRetired{})
	if retiredType.NumField() != 2 || retiredType.Field(0).Name != "expiresAt" || retiredType.Field(0).Type != reflect.TypeOf(time.Time{}) || retiredType.Field(1).Name != "sequence" || retiredType.Field(1).Type.Kind() != reflect.Uint64 {
		t.Fatal("expired terminal key retains more than expiry and eviction metadata")
	}

	advanceMSP04BClock(fixture.clock, firstTrustReplayTTL+time.Nanosecond)
	if got := fixture.coordinator.openPairingWindow(context.Background(), firstOpenKey, time.Minute); got != "stale_request" {
		t.Fatalf("oldest retired replay = %q", got)
	}
	if got := fixture.coordinator.openPairingWindow(context.Background(), msp04bLabel(t), time.Minute); got != "open_empty" {
		t.Fatalf("new request after terminal expiry = %q", got)
	}
	if got := fixture.coordinator.closePairingWindow(context.Background(), msp04bLabel(t)); got != "pairing_closed" {
		t.Fatalf("close after capacity recovery = %q", got)
	}

	advanceMSP04BClock(fixture.clock, firstTrustRetiredTTL+time.Nanosecond)
	if got := fixture.coordinator.openPairingWindow(context.Background(), firstOpenKey, time.Minute); got != "open_empty" {
		t.Fatalf("fully expired key remained locked out with outcome %q", got)
	}
}

func TestFirstTrustLateCommitIsFencedUntilReopen(t *testing.T) {
	store := newMSP04BLateStore()
	effects := newMSP04BEffectsSpy(store.events)
	clock := &msp04bClock{now: time.Unix(1_900_000_000, 0)}
	coordinator := newFirstTrustCoordinator(clock.Now, rand.Reader, store, effects)
	coordinator.commitWait = 20 * time.Millisecond
	if got := coordinator.reopen(context.Background()); got != "pairing_closed" {
		t.Fatalf("reopen outcome = %q", got)
	}
	remote := msp04bRemote(t)
	if got := coordinator.openPairingWindow(context.Background(), msp04bLabel(t), time.Minute); got != "open_empty" {
		t.Fatalf("open outcome = %q", got)
	}
	if got := coordinator.admit(remote, 121); got != "candidate_pending" {
		t.Fatalf("admit outcome = %q", got)
	}
	coordinator.serviceShipIDUpdate(remote, 121, msp04bLabel(t))
	fingerprint, nonce, expiresAt, connection, generation, _, ok := coordinator.candidate()
	if !ok {
		t.Fatal("candidate missing")
	}
	binding := msp04bBindings{fingerprint: fingerprint, nonce: nonce, expiresAt: expiresAt, connection: connection, store: generation}
	result := make(chan string, 1)
	go func() { result <- confirmMSP04B(coordinator, msp04bLabel(t), binding) }()
	waitMSP04BSignal(t, store.entered, "late Commit entry")
	if got := waitMSP04BResult(t, result, "commit timeout"); got != "trust_outcome_unknown" {
		t.Fatalf("timeout outcome = %q", got)
	}
	if coordinator.state() != "DISABLED" || effects.registerCount() != 0 {
		t.Fatal("timed-out commit did not disable mutation without registration")
	}
	if got := coordinator.reopen(context.Background()); got != "reopen_pending" {
		t.Fatalf("unfenced reopen outcome = %q", got)
	}
	close(store.release)
	waitMSP04BSignal(t, store.finished, "late Commit completion")
	deadline := time.Now().Add(2 * time.Second)
	for {
		if got := coordinator.reopen(context.Background()); got == "pairing_closed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reopen did not observe the fenced commit completion")
		}
		time.Sleep(time.Millisecond)
	}
	if !coordinator.trusted(remote) || effects.registerCount() != 0 {
		t.Fatal("reopen did not reload durable trust without late registration")
	}
}

func TestFirstTrustFacadeForcesAutoAcceptFalse(t *testing.T) {
	fixture := newMSP04BFixture(t, "commit_durable")
	service := &msp04bServiceSpy{}
	if _, err := newFirstTrustFacade(service, fixture.coordinator); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if len(service.auto) != 1 || service.auto[0] {
		t.Fatal("facade did not force auto-accept false")
	}
}

func TestFirstTrustFacadeKeepsWinnerOnDuplicateCallbackDuringCommit(t *testing.T) {
	fixture := newMSP04BFixture(t, "commit_durable")
	fixture.store.blockCommit()
	service := &msp04bServiceSpy{}
	adapter, err := newFirstTrustFacade(service, fixture.coordinator)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, ok := fixture.coordinator.(*firstTrustCoordinator)
	if !ok {
		t.Fatal("fixture coordinator type changed")
	}
	coordinator.effects = adapter
	remote := msp04bRemote(t)
	ski := hex.EncodeToString(remote)
	shipID := msp04bLabel(t)
	if got := fixture.coordinator.openPairingWindow(context.Background(), msp04bLabel(t), time.Minute); got != "open_empty" {
		t.Fatalf("open outcome = %q", got)
	}
	adapter.ServiceShipIDUpdate(ski, shipID)
	detail := shipapi.NewConnectionStateDetail(shipapi.ConnectionStateReceivedPairingRequest, nil)
	adapter.ServicePairingDetailUpdate(ski, detail)
	fingerprint, nonce, expiresAt, connection, storeGeneration, _, ok := fixture.coordinator.candidate()
	if !ok {
		t.Fatal("candidate missing")
	}
	binding := msp04bBindings{fingerprint: fingerprint, nonce: nonce, expiresAt: expiresAt, connection: connection, store: storeGeneration}
	result := make(chan string, 1)
	go func() { result <- confirmMSP04B(fixture.coordinator, msp04bLabel(t), binding) }()
	waitMSP04BSignal(t, fixture.store.commitEntered, "Commit entry")

	adapter.ServicePairingDetailUpdate(ski, detail)
	if service.cancelCount() != 0 {
		t.Fatal("duplicate winner callback cancelled the in-flight peer")
	}
	fixture.store.releaseCommit()
	if got := waitMSP04BResult(t, result, "confirmation"); got != "trusted" {
		t.Fatalf("confirmation outcome = %q", got)
	}
	service.mu.Lock()
	registers := service.registers
	service.mu.Unlock()
	if registers != 1 {
		t.Fatalf("RegisterRemoteSKI calls = %d, want 1", registers)
	}
}

func TestFirstTrustFacadeSameSKIOverlapAndDelayedCallbacksFailClosed(t *testing.T) {
	tests := []struct {
		name    string
		delayed func(*firstTrustFacade, string)
	}{
		{name: "reconnect"},
		{
			name: "delayed old SHIP ID",
			delayed: func(adapter *firstTrustFacade, ski string) {
				adapter.ServiceShipIDUpdate(ski, "delayed-old-ship-id")
			},
		},
		{
			name: "delayed old disconnect",
			delayed: func(adapter *firstTrustFacade, ski string) {
				adapter.RemoteSKIDisconnected(nil, ski)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMSP04BFixture(t, "commit_durable")
			service := &msp04bServiceSpy{}
			adapter, err := newFirstTrustFacade(service, fixture.coordinator)
			if err != nil {
				t.Fatal(err)
			}
			coordinator, ok := fixture.coordinator.(*firstTrustCoordinator)
			if !ok {
				t.Fatal("fixture coordinator type changed")
			}
			coordinator.effects = adapter
			remote := msp04bRemote(t)
			ski := hex.EncodeToString(remote)
			detail := shipapi.NewConnectionStateDetail(shipapi.ConnectionStateReceivedPairingRequest, nil)

			if got := fixture.coordinator.openPairingWindow(context.Background(), msp04bLabel(t), time.Minute); got != "open_empty" {
				t.Fatalf("open outcome = %q", got)
			}
			adapter.RemoteSKIConnected(nil, ski)
			adapter.ServicePairingDetailUpdate(ski, detail)
			_, _, _, firstGeneration, _, _, candidate := fixture.coordinator.candidate()
			if !candidate || firstGeneration == 0 {
				t.Fatal("initial unambiguous connection did not create a candidate")
			}

			adapter.RemoteSKIConnected(nil, ski)
			assertMSP04BNoCandidate(t, fixture.coordinator)
			if got := fixture.coordinator.state(); got != "OPEN_EMPTY" {
				t.Fatalf("same-SKI overlap state = %q, want OPEN_EMPTY", got)
			}
			if test.delayed != nil {
				test.delayed(adapter, ski)
			}
			adapter.ServicePairingDetailUpdate(ski, detail)
			assertMSP04BNoCandidate(t, fixture.coordinator)
			assertMSP04BCommitCount(t, fixture.store, 0)
			if service.cancelCount() < 2 {
				t.Fatalf("ambiguous lifecycle cancellation count = %d, want at least 2", service.cancelCount())
			}
		})
	}
}

func advanceMSP04BClock(clock *msp04bClock, duration time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	clock.mu.Unlock()
}

type msp04bLateStore struct {
	mu           sync.Mutex
	generation   uint64
	associations map[string]string
	events       *msp04bEventLog
	entered      chan struct{}
	release      chan struct{}
	finished     chan struct{}
	enteredOnce  sync.Once
	finishedOnce sync.Once
}

func newMSP04BLateStore() *msp04bLateStore {
	return &msp04bLateStore{
		generation:   71,
		associations: make(map[string]string),
		events:       &msp04bEventLog{},
		entered:      make(chan struct{}),
		release:      make(chan struct{}),
		finished:     make(chan struct{}),
	}
}

func (store *msp04bLateStore) Reload(context.Context) (uint64, map[string]string, string) {
	store.mu.Lock()
	defer store.mu.Unlock()
	associations := make(map[string]string, len(store.associations))
	for remote, shipID := range store.associations {
		associations[remote] = shipID
	}
	return store.generation, associations, "opened_current"
}

func (store *msp04bLateStore) SelectedGeneration() uint64 {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.generation
}

func (store *msp04bLateStore) Commit(_ context.Context, expected uint64, remote []byte, shipID string) string {
	store.events.add("commit")
	store.enteredOnce.Do(func() { close(store.entered) })
	<-store.release
	store.mu.Lock()
	if expected == store.generation {
		store.associations[string(bytes.Clone(remote))] = shipID
		store.generation++
	}
	store.mu.Unlock()
	store.finishedOnce.Do(func() { close(store.finished) })
	return "commit_durable"
}
