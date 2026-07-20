package eebusfacade

import (
	"encoding/hex"
	"sync"
	"testing"
	"time"

	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
)

func TestIssue48ExpiryCancellationAllowsSynchronousLivenessReentry(t *testing.T) {
	fixture := newMSP04BFixture(t, "commit_durable")
	coordinator := fixture.coordinator.(*firstTrustCoordinator)
	remote := msp04bRemote(t)
	const connection = uint64(48)
	openMSP04BCandidate(t, fixture, remote, connection, false)

	service := &issue48ReentrantService{}
	facade, err := newFirstTrustFacade(service, coordinator)
	if err != nil {
		t.Fatal(err)
	}
	service.onCancel = func(ski string) {
		coordinator.trustAdminLivenessAllowed(ski)
	}
	facade.connections[hex.EncodeToString(remote)] = &firstTrustConnection{
		generation: connection,
		active:     true,
	}
	coordinator.effects = facade
	advanceMSP04BClock(fixture.clock, firstTrustMaximumCandidate)

	result := make(chan string, 1)
	go func() { result <- coordinator.state() }()
	select {
	case got := <-result:
		if got != "PAIRING_CLOSED" {
			t.Fatalf("state after candidate expiry = %q, want PAIRING_CLOSED", got)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("candidate expiry deadlocked on synchronous cancellation callback reentry")
	}
	if got := service.cancelCount(); got != 1 {
		t.Fatalf("CancelPairingWithSKI calls = %d, want 1", got)
	}
}

func TestIssue48DurableRegistrationAllowsSynchronousLivenessReentry(t *testing.T) {
	fixture := newMSP04BFixture(t, "commit_durable")
	coordinator := fixture.coordinator.(*firstTrustCoordinator)
	remote := msp04bRemote(t)
	const connection = uint64(49)
	binding := openMSP04BCandidate(t, fixture, remote, connection, true)

	service := &issue48ReentrantService{}
	facade, err := newFirstTrustFacade(service, coordinator)
	if err != nil {
		t.Fatal(err)
	}
	service.onRegister = func(ski string) {
		coordinator.trustAdminLivenessAllowed(ski)
	}
	facade.connections[hex.EncodeToString(remote)] = &firstTrustConnection{
		generation: connection,
		active:     true,
	}
	coordinator.effects = facade
	key := msp04bLabel(t)

	result := make(chan string, 1)
	go func() {
		result <- confirmMSP04B(coordinator, key, binding)
	}()
	select {
	case got := <-result:
		if got != "trusted" {
			t.Fatalf("confirmation outcome = %q, want trusted", got)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("durable confirmation deadlocked on synchronous registration callback reentry")
	}
	if got := service.registerCount(); got != 1 {
		t.Fatalf("RegisterRemoteSKI calls = %d, want 1", got)
	}
}

func TestIssue48CloseCancelsPendingPairingBeforeServiceShutdown(t *testing.T) {
	fixture := newMSP04BFixture(t, "commit_durable")
	coordinator := fixture.coordinator.(*firstTrustCoordinator)
	remote := msp04bRemote(t)
	const connection = uint64(50)
	openMSP04BCandidate(t, fixture, remote, connection, false)

	service := &issue48ReentrantService{}
	facade, err := newFirstTrustFacade(service, coordinator)
	if err != nil {
		t.Fatal(err)
	}
	facade.connections[hex.EncodeToString(remote)] = &firstTrustConnection{
		generation: connection,
		active:     true,
	}
	coordinator.effects = facade
	backend := &serviceBackend{
		service: service,
		firstTrust: &runtimeFirstTrustResources{
			coordinator: coordinator,
			facade:      facade,
		},
	}

	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
	if got, want := service.eventsSnapshot(), []string{"cancel", "shutdown"}; !issue48StringsEqual(got, want) {
		t.Fatalf("close effects = %v, want %v", got, want)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
	if got, want := service.eventsSnapshot(), []string{"cancel", "shutdown"}; !issue48StringsEqual(got, want) {
		t.Fatalf("idempotent close effects = %v, want %v", got, want)
	}
}

type issue48ReentrantService struct {
	mu         sync.Mutex
	events     []string
	cancels    int
	registers  int
	onCancel   func(string)
	onRegister func(string)
}

func (*issue48ReentrantService) SetAutoAccept(bool) {}

func (service *issue48ReentrantService) RegisterRemoteSKI(ski string) {
	service.mu.Lock()
	service.registers++
	callback := service.onRegister
	service.mu.Unlock()
	if callback != nil {
		callback(ski)
	}
}

func (service *issue48ReentrantService) CancelPairingWithSKI(ski string) {
	service.mu.Lock()
	service.cancels++
	service.events = append(service.events, "cancel")
	callback := service.onCancel
	service.mu.Unlock()
	if callback != nil {
		callback(ski)
	}
}

func (*issue48ReentrantService) SetPairingRegistration(bool) error { return nil }

func (service *issue48ReentrantService) Shutdown() {
	service.mu.Lock()
	service.events = append(service.events, "shutdown")
	service.mu.Unlock()
}

func (service *issue48ReentrantService) cancelCount() int {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.cancels
}

func (service *issue48ReentrantService) registerCount() int {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.registers
}

func (service *issue48ReentrantService) eventsSnapshot() []string {
	service.mu.Lock()
	defer service.mu.Unlock()
	return append([]string(nil), service.events...)
}

func issue48StringsEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

var _ firstTrustService = (*issue48ReentrantService)(nil)
var _ runtimeService = (*issue48ReentrantService)(nil)

func (*issue48ReentrantService) Setup() error { return nil }
func (*issue48ReentrantService) Start()       {}

func (*issue48ReentrantService) LocalService() *shipapi.ServiceDetails { return nil }
func (*issue48ReentrantService) LocalDevice() spineapi.DeviceLocalInterface {
	return nil
}
