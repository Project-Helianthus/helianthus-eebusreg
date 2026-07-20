package eebusfacade

import (
	"bytes"
	"context"
	"net/netip"
	"reflect"
	"strconv"
	"testing"
	"time"

	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
)

var msp05pOutboundEndpoint = netip.MustParseAddrPort("192.0.2.201:54981")

const msp05pOutboundPath = "/ship/"

func TestMSP05PFirstTrustOpenQueuesConfiguredUntrustedEndpointOutsideCoordinatorLock(t *testing.T) {
	harness := newMSP05POutboundHarness(t, true, false)
	installMSP05PCoordinatorLockProbe(harness)

	if got := harness.resources.coordinator.openPairingWindow(context.Background(), msp045RunToken(t, "outbound-open"), time.Minute); got != "open_empty" {
		t.Fatalf("open pairing window = %q", got)
	}
	state := waitMSP05POutboundState(t, harness.service, func(state msp05pOutboundState) bool {
		return len(state.queued) == 1 && len(state.reported) == 1
	}, "queued endpoint report")

	wantEndpoint := shipapi.RemoteEndpoint{
		Host: msp05pOutboundEndpoint.Addr().String(),
		Port: msp05pOutboundEndpoint.Port(),
		Path: msp05pOutboundPath,
	}
	if state.queued[0] != harness.remoteSKI || state.reported[0].ski != harness.remoteSKI || state.reported[0].endpoint != wantEndpoint {
		t.Fatal("outbound calls did not preserve the exact configured binding")
	}
	if state.endpointOperationLocked {
		t.Fatal("QueueRemoteSKI or ReportRemoteEndpoint ran while the coordinator lock was held")
	}
	if len(state.registered) != 0 || harness.resources.coordinator.trusted(harness.remote) {
		t.Fatal("manual endpoint admission pretrusted or registered the remote")
	}
	harness.bridge.mu.Lock()
	associations := len(harness.bridge.view.associations)
	harness.bridge.mu.Unlock()
	if associations != 0 {
		t.Fatal("manual endpoint admission persisted trust")
	}
}

func TestMSP05PFirstTrustOpenWithoutEndpointKeepsDiscoveryFallback(t *testing.T) {
	harness := newMSP05POutboundHarness(t, false, true)
	installMSP05PCoordinatorLockProbe(harness)

	if got := harness.resources.coordinator.openPairingWindow(context.Background(), msp045RunToken(t, "discovery-open"), time.Minute); got != "open_empty" {
		t.Fatalf("open pairing window = %q", got)
	}
	state := waitMSP05POutboundState(t, harness.service, func(state msp05pOutboundState) bool {
		return len(state.queued) == 1
	}, "discovery queue")
	time.Sleep(20 * time.Millisecond)
	state = snapshotMSP05POutboundState(harness.service)
	if len(state.queued) != 1 || state.queued[0] != harness.remoteSKI || len(state.reported) != 0 {
		t.Fatal("discovery fallback did not queue only the exact configured SKI")
	}
	if state.endpointOperationLocked {
		t.Fatal("discovery queue ran while the coordinator lock was held")
	}
}

func TestMSP05PFirstTrustCloseAndExpiryRemoveEphemeralOutboundState(t *testing.T) {
	for _, test := range []struct {
		name  string
		close func(*testing.T, *msp045RuntimeHarness)
	}{
		{
			name: "close",
			close: func(t *testing.T, harness *msp045RuntimeHarness) {
				if got := harness.resources.coordinator.closePairingWindow(context.Background(), msp045RunToken(t, "outbound-close")); got != "pairing_closed" {
					t.Fatalf("close pairing window = %q", got)
				}
			},
		},
		{
			name: "expiry",
			close: func(t *testing.T, harness *msp045RuntimeHarness) {
				harness.clock.Advance(time.Minute)
				if got := harness.resources.coordinator.state(); got != "PAIRING_CLOSED" {
					t.Fatalf("expired pairing state = %q", got)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newMSP05POutboundHarness(t, true, false)
			if got := harness.resources.coordinator.openPairingWindow(context.Background(), msp045RunToken(t, "cleanup-open"), time.Minute); got != "open_empty" {
				t.Fatalf("open pairing window = %q", got)
			}
			waitMSP05POutboundState(t, harness.service, func(state msp05pOutboundState) bool {
				return len(state.queued) == 1 && len(state.reported) == 1
			}, "outbound admission")

			test.close(t, harness)
			state := waitMSP05POutboundState(t, harness.service, func(state msp05pOutboundState) bool {
				return len(state.cancelled) == 1 && len(state.queued) == 0 &&
					len(state.pairingRegistration) > 0 && !state.pairingRegistration[len(state.pairingRegistration)-1]
			}, "outbound cleanup")
			if state.cancelled[0] != harness.remoteSKI || len(state.registered) != 0 || harness.resources.coordinator.trusted(harness.remote) {
				t.Fatal("cleanup retained registration or failed to cancel the exact configured SKI")
			}
			harness.bridge.mu.Lock()
			associations := len(harness.bridge.view.associations)
			harness.bridge.mu.Unlock()
			if associations != 0 {
				t.Fatal("close or expiry persisted endpoint-only trust")
			}
		})
	}
}

func TestMSP05PDurableOOBRemoteReportsConfiguredEndpointAfterRestart(t *testing.T) {
	first := newMSP05POutboundHarness(t, true, false)
	pairRuntimeRemote(t, first.resources, first.remoteSKI, 7_301)
	firstState := snapshotMSP05POutboundState(first.service)
	if len(firstState.registered) != 1 || firstState.registered[0] != first.remoteSKI {
		t.Fatal("exact OOB confirmation did not register the exact remote once")
	}

	first.bridge.mu.Lock()
	durableView := cloneFirstTrustControlView(first.bridge.view)
	first.bridge.mu.Unlock()
	anchor := first.anchor.(*msp045Anchor)
	anchor.mu.Lock()
	durableAnchor := cloneFirstTrustAnchorRecord(anchor.record)
	anchor.mu.Unlock()

	restarted := newMSP045ProductHarness(t, func(setup *msp045ProductSetup) {
		setup.view = durableView
		setup.anchorRecord = durableAnchor
		setup.remote = append([]byte(nil), first.remote...)
		setup.configureRemote = configureMSP05POutboundEndpoint
	})
	restartedState := waitMSP05POutboundState(t, restarted.service, func(state msp05pOutboundState) bool {
		return len(state.registered) == 1 && len(state.reported) == 1
	}, "trusted restart endpoint")
	if len(restartedState.queued) != 0 || restartedState.registered[0] != first.remoteSKI || restartedState.reported[0].ski != first.remoteSKI {
		t.Fatal("trusted restart did not bind registration and endpoint to the exact durable remote")
	}
	want := shipapi.RemoteEndpoint{Host: msp05pOutboundEndpoint.Addr().String(), Port: msp05pOutboundEndpoint.Port(), Path: msp05pOutboundPath}
	if restartedState.reported[0].endpoint != want {
		t.Fatal("trusted restart changed the configured endpoint")
	}
	registerIndex := msp05pEventIndex(restartedState.events, "register")
	reportIndex := msp05pEventIndex(restartedState.events, "report")
	if registerIndex < 0 || reportIndex < 0 || registerIndex > reportIndex {
		t.Fatalf("trusted restart event order = %v", restartedState.events)
	}
	if restarted.resources.coordinator.recoveryState() != "PAIRED_TRUSTED" {
		t.Fatalf("restart recovery = %q", restarted.resources.coordinator.recoveryState())
	}
}

func TestMSP05PConfiguredEndpointNeverReachesSnapshotOrRawEvidence(t *testing.T) {
	harness := newMSP05POutboundHarness(t, true, false)
	if got := harness.resources.coordinator.openPairingWindow(context.Background(), msp045RunToken(t, "redaction-open"), time.Minute); got != "open_empty" {
		t.Fatalf("open pairing window = %q", got)
	}
	waitMSP05POutboundState(t, harness.service, func(state msp05pOutboundState) bool {
		return len(state.reported) == 1
	}, "redaction endpoint report")
	_, payload := msp045Capture(t, harness.handler)
	for label, value := range map[string]string{
		"endpoint host": msp05pOutboundEndpoint.Addr().String(),
		"endpoint port": strconv.Itoa(int(msp05pOutboundEndpoint.Port())),
		"SHIP path":     msp05pOutboundPath,
	} {
		if bytes.Contains(payload, []byte(value)) {
			t.Fatalf("runtime snapshot/raw evidence leaks %s", label)
		}
	}
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

func installMSP05PCoordinatorLockProbe(harness *msp045RuntimeHarness) {
	harness.service.mu.Lock()
	harness.service.coordinatorLockIsHeld = func() bool {
		if harness.resources.coordinator.mu.TryLock() {
			harness.resources.coordinator.mu.Unlock()
			return false
		}
		return true
	}
	harness.service.mu.Unlock()
}

type msp05pOutboundState struct {
	registered              []string
	queued                  []string
	reported                []msp045EndpointReport
	cancelled               []string
	pairingRegistration     []bool
	endpointOperationLocked bool
	events                  []string
}

func snapshotMSP05POutboundState(service *msp045Service) msp05pOutboundState {
	service.mu.Lock()
	defer service.mu.Unlock()
	return msp05pOutboundState{
		registered:              append([]string(nil), service.registered...),
		queued:                  append([]string(nil), service.queued...),
		reported:                append([]msp045EndpointReport(nil), service.reported...),
		cancelled:               append([]string(nil), service.cancelled...),
		pairingRegistration:     append([]bool(nil), service.pairingRegistration...),
		endpointOperationLocked: service.endpointOperationLocked,
		events:                  append([]string(nil), service.events...),
	}
}

func waitMSP05POutboundState(
	t *testing.T,
	service *msp045Service,
	ready func(msp05pOutboundState) bool,
	label string,
) msp05pOutboundState {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		state := snapshotMSP05POutboundState(service)
		if ready(state) {
			return state
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s: queued=%d reported=%d cancelled=%d registered=%d events=%v", label, len(state.queued), len(state.reported), len(state.cancelled), len(state.registered), state.events)
		}
		time.Sleep(time.Millisecond)
	}
}

func msp05pEventIndex(events []string, want string) int {
	for index, event := range events {
		if event == want {
			return index
		}
	}
	return -1
}
