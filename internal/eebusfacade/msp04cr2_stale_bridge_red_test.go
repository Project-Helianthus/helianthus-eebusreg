package eebusfacade

import (
	"context"
	"encoding/hex"
	"reflect"
	"sync"
	"testing"
	"time"

	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
	shipmodel "github.com/Project-Helianthus/helianthus-ship-go/model"
)

func TestMSP04CR2RuntimeBridgeSerializesStaleTerminalBeforeLifecycleMutation(t *testing.T) {
	fixture, coordinator, bridge, lifecycle, remoteSKI, staleMetadata, current := newMSP04CR2StaleBridgeFixture(t)
	before, ok := soleMSP04CR2Attempt(coordinator)
	if !ok {
		t.Fatal("current attempt is absent before the stale callback race")
	}

	fixture.store.block()
	authorization := make(chan shipapi.OutgoingAttemptPermit, 1)
	go func() {
		permit, _ := bridge.AuthorizeLaunch(current)
		authorization <- permit
	}()
	waitMSP04CSignal(t, fixture.store.commitEntered)

	callbackDone := make(chan struct{})
	go func() {
		bridge.OutgoingAttemptConnectionClosed(remoteSKI, false, staleMetadata)
		close(callbackDone)
	}()

	mutatedBeforeLinearization := lifecycle.waitForMutation(100 * time.Millisecond)
	fixture.store.release()
	permit := waitMSP04CR2SHIPPermit(t, authorization)
	waitMSP04CR2Callback(t, callbackDone)

	if mutatedBeforeLinearization {
		t.Fatal("stale terminal callback mutated lifecycle while the current attempt owned the per-SKI lane")
	}
	if disconnected, pairing := lifecycle.counts(); disconnected != 0 || pairing != 0 {
		t.Fatalf("stale terminal lifecycle effects = disconnected:%d pairing:%d, want zero", disconnected, pairing)
	}
	if permit.Decision != shipapi.OutgoingAttemptDecisionPermit {
		t.Fatalf("current attempt authorization = %#v, want permit", permit)
	}
	after, ok := soleMSP04CR2Attempt(coordinator)
	if !ok || after.attemptID != before.attemptID || after.attemptID != permitMetadataAttemptID(t, permit.Metadata) ||
		after.state != firstTrustAttemptLaunchAuthorized {
		t.Fatalf("stale terminal callback changed the current attempt: before=%#v after=%#v present=%t", before, after, ok)
	}
}

func TestMSP04CR2RuntimeBridgeRejectsStaleHandshakeBeforeLifecycleMutation(t *testing.T) {
	_, coordinator, bridge, lifecycle, remoteSKI, staleMetadata, current := newMSP04CR2StaleBridgeFixture(t)
	permit, err := bridge.AuthorizeLaunch(current)
	if err != nil || permit.Decision != shipapi.OutgoingAttemptDecisionPermit {
		t.Fatalf("authorize current attempt = %#v/%v", permit, err)
	}
	before, ok := soleMSP04CR2Attempt(coordinator)
	if !ok {
		t.Fatal("current attempt is absent before stale handshake callback")
	}

	bridge.OutgoingAttemptHandshakeStateUpdate(remoteSKI, shipmodel.ShipState{State: shipmodel.SmeStateError}, staleMetadata)

	if disconnected, pairing := lifecycle.counts(); disconnected != 0 || pairing != 0 {
		t.Fatalf("stale handshake lifecycle effects = disconnected:%d pairing:%d, want zero", disconnected, pairing)
	}
	after, ok := soleMSP04CR2Attempt(coordinator)
	if !ok || !reflect.DeepEqual(after, before) {
		t.Fatalf("stale handshake callback mutated current attempt: before=%#v after=%#v present=%t", before, after, ok)
	}
}

func newMSP04CR2StaleBridgeFixture(t *testing.T) (
	*msp04cFixture,
	*firstTrustCoordinator,
	*firstTrustOutgoingAttemptBridge,
	*msp04cr2LifecycleSpy,
	string,
	shipapi.OutgoingAttemptMetadata,
	shipapi.OutgoingAttemptHandle,
) {
	t.Helper()
	fixture, coordinator, remote, _ := newMSP04CR2AttemptFixture(t)
	bridge := newFirstTrustOutgoingAttemptBridge(&runtimeFirstTrustResources{coordinator: coordinator})
	lifecycle := newMSP04CR2LifecycleSpy()
	bridge.bindLifecycle(lifecycle)
	remoteSKI := hex.EncodeToString(remote)
	request := shipapi.OutgoingAttemptRequest{
		RemoteSKI: remoteSKI,
		Endpoint:  shipapi.OutgoingAttemptEndpoint{Host: "peer.invalid", Port: 4712},
		Path:      "/ship/",
	}

	stale, err := bridge.Prepare(request)
	if err != nil || stale == nil {
		t.Fatalf("prepare stale-source attempt = %v/%v", stale, err)
	}
	stalePermit, err := bridge.AuthorizeLaunch(stale)
	if err != nil || stalePermit.Decision != shipapi.OutgoingAttemptDecisionPermit {
		t.Fatalf("authorize stale-source attempt = %#v/%v", stalePermit, err)
	}
	runtimeStale, ok := stale.(*runtimeOutgoingAttemptHandle)
	if !ok || runtimeStale.handle == nil {
		t.Fatal("runtime bridge returned an unexpected stale-source handle")
	}
	if got := coordinator.completeOutgoingAttempt(context.Background(), runtimeStale.handle.metadata, true); got != "attempt_succeeded" {
		t.Fatalf("retire stale-source attempt = %q", got)
	}

	request.Endpoint.Host = "2001:db8::1"
	request.Path = ""
	current, err := bridge.Prepare(request)
	if err != nil || current == nil {
		t.Fatalf("prepare current attempt = %v/%v", current, err)
	}
	return fixture, coordinator, bridge, lifecycle, remoteSKI, stalePermit.Metadata, current
}

type msp04cr2LifecycleSpy struct {
	mu           sync.Mutex
	disconnected []string
	pairing      []string
	mutation     chan struct{}
}

func newMSP04CR2LifecycleSpy() *msp04cr2LifecycleSpy {
	return &msp04cr2LifecycleSpy{mutation: make(chan struct{}, 4)}
}

func (spy *msp04cr2LifecycleSpy) RemoteSKIDisconnected(ski string) {
	spy.mu.Lock()
	spy.disconnected = append(spy.disconnected, ski)
	spy.mu.Unlock()
	spy.signalMutation()
}

func (spy *msp04cr2LifecycleSpy) ServicePairingDetailUpdate(ski string, _ *shipapi.ConnectionStateDetail) {
	spy.mu.Lock()
	spy.pairing = append(spy.pairing, ski)
	spy.mu.Unlock()
	spy.signalMutation()
}

func (spy *msp04cr2LifecycleSpy) signalMutation() {
	select {
	case spy.mutation <- struct{}{}:
	default:
	}
}

func (spy *msp04cr2LifecycleSpy) waitForMutation(timeout time.Duration) bool {
	select {
	case <-spy.mutation:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (spy *msp04cr2LifecycleSpy) counts() (int, int) {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	return len(spy.disconnected), len(spy.pairing)
}

func waitMSP04CR2SHIPPermit(t *testing.T, result <-chan shipapi.OutgoingAttemptPermit) shipapi.OutgoingAttemptPermit {
	t.Helper()
	select {
	case permit := <-result:
		return permit
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for current attempt authorization")
		return shipapi.OutgoingAttemptPermit{}
	}
}

func waitMSP04CR2Callback(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stale callback completion")
	}
}

func permitMetadataAttemptID(t *testing.T, metadata shipapi.OutgoingAttemptMetadata) [32]byte {
	t.Helper()
	decoded, ok := runtimeDecodeOutgoingAttemptOpaque(metadata.AttemptID)
	if !ok {
		t.Fatalf("permit returned invalid attempt id %q", metadata.AttemptID)
	}
	return decoded
}
