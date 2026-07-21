package eebusfacade

import (
	"context"
	"net/netip"
	"reflect"
	"testing"
	"time"

	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
)

var msp05pOutboundEndpoint = netip.MustParseAddrPort("192.0.2.201:54981")

const msp05pOutboundPath = "/ship/"

func TestIssue54OpeningPairingOnlyChangesLocalRegistration(t *testing.T) {
	harness := newMSP05POutboundHarness(t, true, false)

	if got := harness.resources.coordinator.openPairingWindow(context.Background(), msp045RunToken(t, "registration-only-open"), time.Minute); got != "open_empty" {
		t.Fatalf("open pairing window = %q", got)
	}
	time.Sleep(20 * time.Millisecond)
	state := snapshotMSP05POutboundState(harness.service)
	if len(state.pairingRegistration) == 0 || !state.pairingRegistration[len(state.pairingRegistration)-1] {
		t.Fatalf("pairing registration events = %v, want final register=true", state.pairingRegistration)
	}
	if len(state.queued) != 0 || len(state.reported) != 0 || len(state.registered) != 0 || len(state.cancelled) != 0 {
		t.Fatalf("opening pairing performed remote effects: queued=%v reported=%v registered=%v cancelled=%v", state.queued, state.reported, state.registered, state.cancelled)
	}
	if _, _, _, _, _, _, ok := harness.resources.coordinator.candidate(); ok {
		t.Fatal("opening pairing fabricated a candidate")
	}
	snapshot, _ := msp045Capture(t, harness.handler)
	issue54AssertNoRemoteEvidence(t, snapshot)
}

func TestIssue54ConfiguredTrustedEndpointIsPolicyWithoutSyntheticObservation(t *testing.T) {
	harness := newMSP045ProductHarness(t, func(setup *msp045ProductSetup) {
		setup.configureRemote = configureMSP05POutboundEndpoint
	})
	time.Sleep(20 * time.Millisecond)
	state := snapshotMSP05POutboundState(harness.service)
	if len(state.registered) != 1 || state.registered[0] != harness.remoteSKI {
		t.Fatalf("durable trust policy registrations = %v", state.registered)
	}
	if len(state.queued) != 0 || len(state.reported) != 0 {
		t.Fatalf("configured endpoint caused outbound observation effects: queued=%v reported=%v", state.queued, state.reported)
	}
	snapshot, _ := msp045Capture(t, harness.handler)
	issue54AssertNoRemoteEvidence(t, snapshot)
	if harness.resources.coordinator.recoveryState() != "PAIRED_TRUSTED" {
		t.Fatalf("durable trust recovery = %q", harness.resources.coordinator.recoveryState())
	}
}

type msp05pServiceWithoutOutbound struct {
	service *msp045Service
}

func (service *msp05pServiceWithoutOutbound) Setup() error { return service.service.Setup() }
func (service *msp05pServiceWithoutOutbound) Start()       { service.service.Start() }
func (service *msp05pServiceWithoutOutbound) Shutdown()    { service.service.Shutdown() }
func (service *msp05pServiceWithoutOutbound) RegisterRemoteSKI(ski string) {
	service.service.RegisterRemoteSKI(ski)
}
func (service *msp05pServiceWithoutOutbound) LocalService() *shipapi.ServiceDetails {
	return service.service.LocalService()
}
func (service *msp05pServiceWithoutOutbound) LocalDevice() spineapi.DeviceLocalInterface {
	return service.service.LocalDevice()
}
func (service *msp05pServiceWithoutOutbound) SetAutoAccept(value bool) {
	service.service.SetAutoAccept(value)
}
func (service *msp05pServiceWithoutOutbound) CancelPairingWithSKI(ski string) {
	service.service.CancelPairingWithSKI(ski)
}
func (service *msp05pServiceWithoutOutbound) SetPairingRegistration(value bool) error {
	return service.service.SetPairingRegistration(value)
}

func newMSP05POutboundHarness(t *testing.T, endpoint, discovery bool) *msp045RuntimeHarness {
	t.Helper()
	pretrusted := false
	return newMSP045ProductHarness(t, func(setup *msp045ProductSetup) {
		setup.view.associations = nil
		setup.remotePretrusted = &pretrusted
		setup.discoveryEnabled = discovery
		if endpoint {
			setup.configureRemote = configureMSP05POutboundEndpoint
		}
	})
}

func configureMSP05POutboundEndpoint(remote *RuntimeRemote) {
	value := reflect.ValueOf(remote).Elem()
	endpoint := value.FieldByName("Endpoint")
	path := value.FieldByName("SHIPPath")
	if !endpoint.IsValid() || endpoint.Type() != reflect.TypeOf(netip.AddrPort{}) || !path.IsValid() || path.Kind() != reflect.String {
		return
	}
	endpoint.Set(reflect.ValueOf(msp05pOutboundEndpoint))
	path.SetString(msp05pOutboundPath)
}

type msp05pOutboundState struct {
	registered          []string
	queued              []string
	reported            []msp045EndpointReport
	cancelled           []string
	pairingRegistration []bool
}

func snapshotMSP05POutboundState(service *msp045Service) msp05pOutboundState {
	service.mu.Lock()
	defer service.mu.Unlock()
	return msp05pOutboundState{
		registered:          append([]string(nil), service.registered...),
		queued:              append([]string(nil), service.queued...),
		reported:            append([]msp045EndpointReport(nil), service.reported...),
		cancelled:           append([]string(nil), service.cancelled...),
		pairingRegistration: append([]bool(nil), service.pairingRegistration...),
	}
}
