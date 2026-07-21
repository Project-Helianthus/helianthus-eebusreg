package eebusfacade

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"

	eebusapi "github.com/Project-Helianthus/helianthus-eebus-go/api"
	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
)

func TestIssue52ConnectedCancellationUsesWithdrawalAndRetiresExactGeneration(t *testing.T) {
	t.Run("synchronous disconnect retires before cancellation returns", func(t *testing.T) {
		harness := newIssue52FacadeHarness(t)
		first := harness.openAndConnect(t, "issue52-sync-open")
		retiredDuringDisconnect := false
		harness.service.onDisconnect = func(ski string) {
			harness.facade.mu.Lock()
			connection := harness.facade.connections[ski]
			harness.facade.mu.Unlock()
			if connection == nil || connection.generation != first || !connection.cancelled || !connection.blocked {
				t.Fatalf("disconnect callback saw unfenced generation: %+v", connection)
			}
			harness.facade.RemoteSKIDisconnected(nil, ski)
			harness.facade.mu.Lock()
			_, retained := harness.facade.connections[ski]
			harness.facade.mu.Unlock()
			retiredDuringDisconnect = !retained
		}

		if got := harness.coordinator.closePairingWindow(context.Background(), "issue52-sync-close"); got != "pairing_closed" {
			t.Fatalf("close outcome = %q, want pairing_closed", got)
		}
		if !retiredDuringDisconnect {
			t.Fatal("synchronous disconnect did not retire the exact cancelled generation")
		}
		if got, want := harness.service.withdrawalEvents(), []string{"cancel", "disconnect"}; !issue48StringsEqual(got, want) {
			t.Fatalf("withdrawal order = %v, want %v", got, want)
		}

		second := harness.openAndAdmit(t, "issue52-sync-reopen")
		if second <= first {
			t.Fatalf("replacement generation = %d, want greater than %d", second, first)
		}
	})

	t.Run("delayed disconnect fences replacement and stale generation effects", func(t *testing.T) {
		harness := newIssue52FacadeHarness(t)
		first := harness.openAndConnect(t, "issue52-delayed-open")
		if got := harness.coordinator.closePairingWindow(context.Background(), "issue52-delayed-close"); got != "pairing_closed" {
			t.Fatalf("close outcome = %q, want pairing_closed", got)
		}
		if got := harness.service.disconnectCount(); got != 1 {
			t.Fatalf("DisconnectSKI calls = %d, want 1", got)
		}

		if got := harness.coordinator.openPairingWindow(context.Background(), "issue52-delayed-reopen", time.Minute); got != "open_empty" {
			t.Fatalf("reopen outcome = %q, want open_empty", got)
		}
		harness.facade.ServicePairingDetailUpdate(harness.ski, harness.pairingRequest)
		assertMSP04BNoCandidate(t, harness.fixture.coordinator)
		connection := harness.facade.connections[harness.ski]
		if connection == nil || connection.generation != first || !connection.cancelled || !connection.blocked {
			t.Fatalf("delayed disconnect failed to fence cancelled generation: %+v", connection)
		}

		harness.facade.RemoteSKIDisconnected(nil, harness.ski)
		second := harness.admitCurrentWindow(t)
		if second <= first {
			t.Fatalf("post-ack generation = %d, want greater than %d", second, first)
		}
		harness.facade.cancelRemote(harness.remote, first)
		harness.facade.registerRemoteSKI(harness.remote, first)
		connection = harness.facade.connections[harness.ski]
		if connection == nil || connection.generation != second || connection.cancelled || connection.blocked {
			t.Fatalf("stale generation effect mutated replacement: %+v", connection)
		}
	})
}

func TestIssue52RuntimeImmediateExpiryReopenRetiresEveryGeneration(t *testing.T) {
	var service *issue52RuntimeService
	pretrusted := false
	harness := newMSP045ProductHarness(t, func(setup *msp045ProductSetup) {
		setup.view.associations = nil
		setup.remotePretrusted = &pretrusted
		setup.wrapRuntime = func(base *msp045Service, reader eebusapi.ServiceReaderInterface) runtimeService {
			service = newIssue52RuntimeService(base, reader)
			return service
		}
	})
	if service == nil {
		t.Fatal("runtime service wrapper was not installed")
	}

	const window = 15 * time.Second
	var previous uint64
	for cycle := 1; cycle <= 3; cycle++ {
		key := "issue52-runtime-open-" + string(rune('0'+cycle))
		if got := harness.resources.coordinator.openPairingWindow(context.Background(), key, window); got != "open_empty" {
			t.Fatalf("cycle %d open outcome = %q, want open_empty", cycle, got)
		}
		service.connect(harness.remoteSKI)
		_, _, _, generation, _, complete, ok := harness.resources.coordinator.candidate()
		if !ok || complete || generation <= previous {
			t.Fatalf("cycle %d candidate generation=%d complete=%t present=%t previous=%d", cycle, generation, complete, ok, previous)
		}
		previous = generation

		harness.clock.Advance(window)
		if got := harness.resources.coordinator.state(); got != "PAIRING_CLOSED" {
			t.Fatalf("cycle %d expiry state = %q, want PAIRING_CLOSED", cycle, got)
		}
		if service.connected(harness.remoteSKI) {
			t.Fatalf("cycle %d expiry retained the SHIP connection", cycle)
		}
	}

	if got := service.disconnectCount(); got != 3 {
		t.Fatalf("DisconnectSKI calls = %d, want 3", got)
	}
	if state, recovery := harness.resources.coordinator.state(), harness.resources.coordinator.recoveryState(); state != "PAIRING_CLOSED" || recovery != "UNPAIRED_LOCKED" {
		t.Fatalf("final runtime state = %s/%s, want PAIRING_CLOSED/UNPAIRED_LOCKED", state, recovery)
	}
}

type issue52FacadeHarness struct {
	fixture        msp04bFixture
	coordinator    *firstTrustCoordinator
	facade         *firstTrustFacade
	service        *issue52WithdrawalService
	remote         []byte
	ski            string
	pairingRequest *shipapi.ConnectionStateDetail
}

func newIssue52FacadeHarness(t *testing.T) *issue52FacadeHarness {
	t.Helper()
	fixture := newMSP04BFixture(t, "commit_durable")
	coordinator := fixture.coordinator.(*firstTrustCoordinator)
	coordinator.random = bytes.NewReader(bytes.Repeat([]byte{0x52}, 32*8))
	service := &issue52WithdrawalService{issue48ReentrantService: &issue48ReentrantService{}}
	facade, err := newFirstTrustFacade(service, coordinator)
	if err != nil {
		t.Fatal(err)
	}
	coordinator.effects = facade
	remote := issue50Remote(t)
	return &issue52FacadeHarness{
		fixture: fixture, coordinator: coordinator, facade: facade, service: service,
		remote: remote, ski: hex.EncodeToString(remote),
		pairingRequest: shipapi.NewConnectionStateDetail(shipapi.ConnectionStateReceivedPairingRequest, nil),
	}
}

func (harness *issue52FacadeHarness) openAndAdmit(t *testing.T, key string) uint64 {
	t.Helper()
	if got := harness.coordinator.openPairingWindow(context.Background(), key, time.Minute); got != "open_empty" {
		t.Fatalf("open outcome = %q, want open_empty", got)
	}
	return harness.admitCurrentWindow(t)
}

func (harness *issue52FacadeHarness) admitCurrentWindow(t *testing.T) uint64 {
	t.Helper()
	harness.facade.ServicePairingDetailUpdate(harness.ski, harness.pairingRequest)
	_, _, _, generation, _, _, ok := harness.coordinator.candidate()
	if !ok || generation == 0 {
		t.Fatal("pairing window did not admit a candidate")
	}
	return generation
}

func (harness *issue52FacadeHarness) openAndConnect(t *testing.T, key string) uint64 {
	t.Helper()
	generation := harness.openAndAdmit(t, key)
	harness.facade.RemoteSKIConnected(nil, harness.ski)
	return generation
}

type issue52WithdrawalService struct {
	*issue48ReentrantService
	withdrawalMu sync.Mutex
	disconnects  int
	events       []string
	onDisconnect func(string)
}

func (service *issue52WithdrawalService) CancelPairingWithSKI(ski string) {
	service.issue48ReentrantService.CancelPairingWithSKI(ski)
	service.withdrawalMu.Lock()
	service.events = append(service.events, "cancel")
	service.withdrawalMu.Unlock()
}

func (service *issue52WithdrawalService) DisconnectSKI(ski string, _ string) {
	service.withdrawalMu.Lock()
	service.disconnects++
	service.events = append(service.events, "disconnect")
	callback := service.onDisconnect
	service.withdrawalMu.Unlock()
	if callback != nil {
		callback(ski)
	}
}

func (*issue52WithdrawalService) UnregisterRemoteSKI(string) {}

func (service *issue52WithdrawalService) disconnectCount() int {
	service.withdrawalMu.Lock()
	defer service.withdrawalMu.Unlock()
	return service.disconnects
}

func (service *issue52WithdrawalService) withdrawalEvents() []string {
	service.withdrawalMu.Lock()
	defer service.withdrawalMu.Unlock()
	return append([]string(nil), service.events...)
}

type issue52RuntimeService struct {
	*msp045Service
	reader eebusapi.ServiceReaderInterface

	stateMu       sync.Mutex
	connections   map[string]bool
	disconnects   int
	queueFailures int
}

func newIssue52RuntimeService(base *msp045Service, reader eebusapi.ServiceReaderInterface) *issue52RuntimeService {
	return &issue52RuntimeService{msp045Service: base, reader: reader, connections: make(map[string]bool)}
}

func (service *issue52RuntimeService) connect(ski string) {
	service.stateMu.Lock()
	service.connections[ski] = true
	service.stateMu.Unlock()
	service.reader.RemoteSKIConnected(nil, ski)
	service.reader.ServicePairingDetailUpdate(
		ski,
		shipapi.NewConnectionStateDetail(shipapi.ConnectionStateCompleted, nil),
	)
}

func (service *issue52RuntimeService) connected(ski string) bool {
	service.stateMu.Lock()
	defer service.stateMu.Unlock()
	return service.connections[ski]
}

func (service *issue52RuntimeService) QueueRemoteSKI(ski string) error {
	service.stateMu.Lock()
	if service.connections[ski] {
		service.queueFailures++
		service.stateMu.Unlock()
		return errors.New("outbound endpoint still has a connected SHIP transport")
	}
	service.stateMu.Unlock()
	return service.msp045Service.QueueRemoteSKI(ski)
}

func (service *issue52RuntimeService) DisconnectSKI(ski string, _ string) {
	service.stateMu.Lock()
	service.connections[ski] = false
	service.disconnects++
	service.stateMu.Unlock()
	service.reader.RemoteSKIDisconnected(nil, ski)
}

func (*issue52RuntimeService) UnregisterRemoteSKI(string) {}

func (service *issue52RuntimeService) disconnectCount() int {
	service.stateMu.Lock()
	defer service.stateMu.Unlock()
	return service.disconnects
}

func (service *issue52RuntimeService) queueFailureCount() int {
	service.stateMu.Lock()
	defer service.stateMu.Unlock()
	return service.queueFailures
}
