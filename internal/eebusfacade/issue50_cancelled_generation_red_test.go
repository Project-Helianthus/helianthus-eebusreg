package eebusfacade

import (
	"bytes"
	"context"
	"encoding/hex"
	"strconv"
	"testing"
	"time"

	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
)

func TestIssue50IncompleteGenerationRetiresBeforeCallbackAndCancelIsIdempotent(t *testing.T) {
	remote := issue50Remote(t)
	ski := hex.EncodeToString(remote)
	service := &issue48ReentrantService{}
	facade, err := newFirstTrustFacade(service, nil)
	if err != nil {
		t.Fatal(err)
	}
	const generation = uint64(50)
	facade.connections[ski] = &firstTrustConnection{
		generation:     generation,
		active:         true,
		attemptStarted: true,
	}

	retiredBeforeCallback := false
	service.onCancel = func(callbackSKI string) {
		facade.mu.Lock()
		_, retained := facade.connections[callbackSKI]
		facade.mu.Unlock()
		retiredBeforeCallback = !retained
	}

	facade.cancelRemote(remote, generation)
	facade.cancelRemote(remote, generation)

	if !retiredBeforeCallback {
		t.Fatal("cancelled generation remained visible during synchronous cancellation callback")
	}
	if got := service.cancelCount(); got != 1 {
		t.Fatalf("idempotent CancelPairingWithSKI calls = %d, want 1", got)
	}
	if _, retained := facade.connections[ski]; retained {
		t.Fatal("cancelled generation remained retained after cancellation")
	}
}

func TestIssue50ConnectedGenerationWaitsForDisconnectAckBeforeReplacement(t *testing.T) {
	harness := newIssue50Harness(t)
	first := harness.openAndAdmit(t, "issue50-connected-open", time.Minute)
	harness.facade.RemoteSKIConnected(nil, harness.ski)
	connection := harness.facade.connections[harness.ski]
	if connection == nil || !connection.connected {
		t.Fatalf("connected generation state = %+v, want connected", connection)
	}

	blockedDuringCancel := false
	harness.service.onCancel = func(callbackSKI string) {
		harness.facade.mu.Lock()
		current := harness.facade.connections[callbackSKI]
		harness.facade.mu.Unlock()
		blockedDuringCancel = current != nil && current.generation == first && current.cancelled && current.blocked
	}
	if got := harness.coordinator.closePairingWindow(context.Background(), "issue50-connected-close"); got != "pairing_closed" {
		t.Fatalf("close outcome = %q, want pairing_closed", got)
	}
	if !blockedDuringCancel {
		t.Fatal("connected generation was not fenced while awaiting disconnect acknowledgement")
	}

	if got := harness.coordinator.openPairingWindow(context.Background(), "issue50-connected-reopen", time.Minute); got != "open_empty" {
		t.Fatalf("reopen outcome = %q, want open_empty", got)
	}
	harness.facade.ServicePairingDetailUpdate(harness.ski, harness.pairingRequest)
	assertMSP04BNoCandidate(t, harness.fixture.coordinator)
	connection = harness.facade.connections[harness.ski]
	if connection == nil || connection.generation != first || !connection.cancelled || !connection.blocked {
		t.Fatalf("connected generation retired before acknowledgement: %+v", connection)
	}

	harness.facade.RemoteSKIDisconnected(nil, harness.ski)
	if _, retained := harness.facade.connections[harness.ski]; retained {
		t.Fatal("disconnect acknowledgement did not retire connected generation")
	}
	harness.service.onCancel = nil
	harness.facade.ServicePairingDetailUpdate(harness.ski, harness.pairingRequest)
	_, _, _, second, _, _, ok := harness.coordinator.candidate()
	if !ok || second <= first {
		t.Fatalf("post-ack generation = %d, want greater than %d", second, first)
	}

	if got := harness.coordinator.closePairingWindow(context.Background(), "issue50-connected-cleanup"); got != "pairing_closed" {
		t.Fatalf("cleanup close outcome = %q, want pairing_closed", got)
	}
}

func TestIssue50RepeatedExpiryReopenCreatesFreshGeneration(t *testing.T) {
	harness := newIssue50Harness(t)
	const window = 15 * time.Second
	var previous uint64

	for cycle := 1; cycle <= 3; cycle++ {
		generation := harness.openAndAdmit(t, "issue50-expiry-open-"+strconv.Itoa(cycle), window)
		if generation <= previous {
			t.Fatalf("cycle %d generation = %d, want greater than %d", cycle, generation, previous)
		}
		previous = generation

		advanceMSP04BClock(harness.fixture.clock, window)
		if got := harness.coordinator.state(); got != "PAIRING_CLOSED" {
			t.Fatalf("cycle %d expiry state = %q, want PAIRING_CLOSED", cycle, got)
		}
		if _, retained := harness.facade.connections[harness.ski]; retained {
			t.Fatalf("cycle %d retained cancelled generation", cycle)
		}
		cancels := harness.service.cancelCount()
		harness.facade.cancelRemote(harness.remote, generation)
		if got := harness.service.cancelCount(); got != cancels {
			t.Fatalf("cycle %d repeated cancellation calls = %d, want %d", cycle, got, cancels)
		}
	}
}

func TestIssue50ExplicitCloseReopenFailsClosedAndFencesStaleGeneration(t *testing.T) {
	harness := newIssue50Harness(t)
	first := harness.openAndAdmit(t, "issue50-explicit-open", time.Minute)
	pairingDetailReentered := false
	harness.service.onCancel = func(ski string) {
		pairingDetailReentered = true
		harness.facade.ServicePairingDetailUpdate(ski, shipapi.NewConnectionStateDetail(shipapi.ConnectionStateError, nil))
	}
	if got := harness.coordinator.closePairingWindow(context.Background(), "issue50-explicit-close"); got != "pairing_closed" {
		t.Fatalf("close outcome = %q, want pairing_closed", got)
	}
	if !pairingDetailReentered {
		t.Fatal("cancellation did not exercise synchronous pairing-detail reentry")
	}
	harness.service.onCancel = nil
	if _, retained := harness.facade.connections[harness.ski]; retained {
		t.Fatal("explicit close retained cancelled generation")
	}

	harness.facade.ServicePairingDetailUpdate(harness.ski, harness.pairingRequest)
	assertMSP04BNoCandidate(t, harness.fixture.coordinator)
	if _, created := harness.facade.connections[harness.ski]; created {
		t.Fatal("closed-window callback created a connection generation")
	}

	second := harness.openAndAdmit(t, "issue50-reopen", time.Minute)
	if second <= first {
		t.Fatalf("reopened generation = %d, want greater than retired generation %d", second, first)
	}
	cancels := harness.service.cancelCount()
	registers := harness.service.registerCount()
	harness.facade.cancelRemote(harness.remote, first)
	harness.facade.registerRemoteSKI(harness.remote, first)
	if got := harness.service.cancelCount(); got != cancels {
		t.Fatalf("stale generation cancellation calls = %d, want %d", got, cancels)
	}
	if got := harness.service.registerCount(); got != registers {
		t.Fatalf("stale generation registration calls = %d, want %d", got, registers)
	}
	connection := harness.facade.connections[harness.ski]
	if connection == nil || connection.generation != second || connection.cancelled || connection.blocked {
		t.Fatalf("stale callback mutated fresh generation: %+v", connection)
	}

	if got := harness.coordinator.closePairingWindow(context.Background(), "issue50-cleanup-close"); got != "pairing_closed" {
		t.Fatalf("cleanup close outcome = %q, want pairing_closed", got)
	}
}

type issue50Harness struct {
	fixture        msp04bFixture
	coordinator    *firstTrustCoordinator
	facade         *firstTrustFacade
	service        *issue48ReentrantService
	remote         []byte
	ski            string
	pairingRequest *shipapi.ConnectionStateDetail
}

func newIssue50Harness(t *testing.T) *issue50Harness {
	t.Helper()
	fixture := newMSP04BFixture(t, "commit_durable")
	coordinator, ok := fixture.coordinator.(*firstTrustCoordinator)
	if !ok {
		t.Fatal("fixture coordinator type changed")
	}
	coordinator.random = bytes.NewReader(bytes.Repeat([]byte{0x50}, 32*8))
	service := &issue48ReentrantService{}
	facade, err := newFirstTrustFacade(service, coordinator)
	if err != nil {
		t.Fatal(err)
	}
	coordinator.effects = facade
	remote := issue50Remote(t)
	return &issue50Harness{
		fixture:        fixture,
		coordinator:    coordinator,
		facade:         facade,
		service:        service,
		remote:         remote,
		ski:            hex.EncodeToString(remote),
		pairingRequest: shipapi.NewConnectionStateDetail(shipapi.ConnectionStateReceivedPairingRequest, nil),
	}
}

func (harness *issue50Harness) openAndAdmit(t *testing.T, key string, duration time.Duration) uint64 {
	t.Helper()
	if got := harness.coordinator.openPairingWindow(context.Background(), key, duration); got != "open_empty" {
		t.Fatalf("open outcome = %q, want open_empty", got)
	}
	harness.facade.ServicePairingDetailUpdate(harness.ski, harness.pairingRequest)
	_, _, _, generation, _, _, ok := harness.coordinator.candidate()
	if !ok || generation == 0 {
		t.Fatal("pairing window did not admit a candidate")
	}
	connection := harness.facade.connections[harness.ski]
	if connection == nil || connection.generation != generation {
		t.Fatalf("facade connection = %+v, want generation %d", connection, generation)
	}
	return generation
}

func issue50Remote(t *testing.T) []byte {
	t.Helper()
	remote, err := hex.DecodeString("00112233445566778899aabbccddeeff00112233")
	if err != nil {
		t.Fatal(err)
	}
	return remote
}
