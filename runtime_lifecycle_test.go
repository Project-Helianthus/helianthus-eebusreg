package eebusruntime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusevidence"
	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusfacade"
)

func TestRuntimeDisabledNeverAcquiresBackend(t *testing.T) {
	var acquisitions atomic.Int32
	factory := runtimeBackendFactory(func(context.Context, Config) (runtimeBackend, error) {
		acquisitions.Add(1)
		return newFakeRuntimeBackend(), nil
	})
	instance, err := newRuntime(Config{}, factory)
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := instance.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if got := acquisitions.Load(); got != 0 {
		t.Fatalf("disabled backend acquisitions = %d, want 0", got)
	}
}

func TestRuntimeConcurrentLifecycleAcquiresOnceCancelsJoinsAndClosesOnce(t *testing.T) {
	backend := newFakeRuntimeBackend()
	var acquisitions atomic.Int32
	var wrongConfig atomic.Bool
	factory := runtimeBackendFactory(func(_ context.Context, config Config) (runtimeBackend, error) {
		acquisitions.Add(1)
		if config.Interface != "test-interface" || len(config.Remotes) != 1 {
			wrongConfig.Store(true)
		}
		return backend, nil
	})
	instance, err := newRuntime(validRuntimeConfig(t.TempDir()), factory)
	if err != nil {
		t.Fatal(err)
	}
	if got := acquisitions.Load(); got != 0 {
		t.Fatalf("New acquired backend %d times, want 0", got)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := instance.Start(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start(canceled context) error = %v, want context.Canceled", err)
	}
	if got := acquisitions.Load(); got != 0 {
		t.Fatalf("canceled Start acquired backend %d times, want 0", got)
	}

	startErrors := callRuntimeConcurrently(t, 32, "concurrent Start", func() error {
		return instance.Start(context.Background())
	})
	for err := range startErrors {
		if err != nil {
			t.Fatalf("concurrent Start() error = %v", err)
		}
	}
	waitRuntimeSignal(t, backend.runStarted, "backend Run")
	if got := acquisitions.Load(); got != 1 {
		t.Fatalf("backend acquisitions = %d, want 1", got)
	}
	if got := backend.runCalls.Load(); got != 1 {
		t.Fatalf("backend Run calls = %d, want 1", got)
	}
	if wrongConfig.Load() {
		t.Fatal("backend factory did not receive the validated explicit configuration")
	}

	first := lifecycleRuntimeSnapshot(t, "session-before-reconnect")
	backend.publish(t, first)
	first.Pairing[0].Unknown[0].Value = eebusraw.OpaqueBytes([]byte("source mutation"))
	first.Services[0].Unknown[0].Value = eebusraw.OpaqueBytes([]byte("source mutation"))
	assertRuntimeSnapshotHashIntact(t, instance)

	second := lifecycleRuntimeSnapshot(t, "session-after-reconnect")
	backend.publish(t, second)
	assertRuntimeReconnectGraph(t, instance, "session-after-reconnect")

	shutdownErrors := callRuntimeConcurrently(t, 32, "concurrent Shutdown", instance.Shutdown)
	for err := range shutdownErrors {
		if err != nil {
			t.Fatalf("concurrent Shutdown() error = %v", err)
		}
	}
	waitRuntimeSignal(t, backend.cancelled, "backend cancellation")
	waitRuntimeSignal(t, backend.runExited, "backend join")
	if got := backend.closeCalls.Load(); got != 1 {
		t.Fatalf("backend Close calls = %d, want 1", got)
	}
	if err := instance.Start(context.Background()); !errors.Is(err, ErrRuntimeShutdown) {
		t.Fatalf("Start after shutdown error = %v, want ErrRuntimeShutdown", err)
	}
	terminal, err := instance.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Status.State != ObservedRuntimeStateV1Shutdown {
		t.Fatalf("terminal state = %q, want shutdown", terminal.Status.State)
	}
	assertRuntimeFeatureGraphCounts(t, terminal)
}

func TestRuntimeConcurrentStartWaiterCanCancelIndependently(t *testing.T) {
	backend := newFakeRuntimeBackend()
	factoryEntered := make(chan struct{})
	releaseFactory := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFactory) }) }
	t.Cleanup(release)

	factory := runtimeBackendFactory(func(context.Context, Config) (runtimeBackend, error) {
		close(factoryEntered)
		<-releaseFactory
		return backend, nil
	})
	instance, err := newRuntime(validRuntimeConfig(t.TempDir()), factory)
	if err != nil {
		t.Fatal(err)
	}

	firstResult := make(chan error, 1)
	go func() { firstResult <- instance.Start(context.Background()) }()
	waitRuntimeSignal(t, factoryEntered, "first backend acquisition")

	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	waiterResult := make(chan error, 1)
	go func() { waiterResult <- instance.Start(waiterCtx) }()
	cancelWaiter()
	if err := waitRuntimeResult(t, waiterResult, "canceled Start waiter"); !errors.Is(err, context.Canceled) {
		t.Fatalf("concurrent Start waiter error = %v, want context.Canceled", err)
	}
	select {
	case err := <-firstResult:
		t.Fatalf("first Start returned before acquisition completed: %v", err)
	default:
	}

	release()
	if err := waitRuntimeResult(t, firstResult, "first Start"); err != nil {
		t.Fatalf("first Start error = %v", err)
	}
	waitRuntimeSignal(t, backend.runStarted, "backend Run")
	if err := instance.Shutdown(); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeRunErrorStopsLiveSuccessAndIsReturnedByShutdown(t *testing.T) {
	backend := newFakeRuntimeBackend()
	instance, err := newRuntime(validRuntimeConfig(t.TempDir()), func(context.Context, Config) (runtimeBackend, error) {
		return backend, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitRuntimeSignal(t, backend.runStarted, "backend Run")
	backend.publish(t, lifecycleRuntimeSnapshot(t, "before-worker-failure"))

	runErr := errors.New("fixture backend Run failed")
	backend.fail(t, runErr)
	waitRuntimeWorkerExit(t, instance)

	if _, err := instance.Snapshot(); !errors.Is(err, runErr) {
		t.Fatalf("Snapshot() after Run failure error = %v, want backend failure", err)
	}
	if err := instance.Start(context.Background()); !errors.Is(err, runErr) {
		t.Fatalf("Start() after Run failure error = %v, want backend failure", err)
	}
	if err := instance.Shutdown(); !errors.Is(err, runErr) {
		t.Fatalf("Shutdown() after Run failure error = %v, want backend failure", err)
	}
	if got := backend.closeCalls.Load(); got != 1 {
		t.Fatalf("backend Close calls = %d, want 1", got)
	}
}

func TestRuntimeInvalidPublicationBecomesWorkerError(t *testing.T) {
	backend := newFakeRuntimeBackend()
	instance, err := newRuntime(validRuntimeConfig(t.TempDir()), func(context.Context, Config) (runtimeBackend, error) {
		return backend, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitRuntimeSignal(t, backend.runStarted, "backend Run")
	backend.publish(t, lifecycleRuntimeSnapshot(t, "before-invalid-publication"))

	invalid := lifecycleRuntimeSnapshot(t, "invalid-publication")
	invalid.Meta.Contract = ""
	backend.publish(t, invalid)
	waitRuntimeWorkerExit(t, instance)

	if _, err := instance.Snapshot(); err == nil || !strings.Contains(err.Error(), "publish runtime snapshot") {
		t.Fatalf("Snapshot() after invalid publication error = %v, want publication error", err)
	}
	if err := instance.Shutdown(); err == nil || !strings.Contains(err.Error(), "publish runtime snapshot") {
		t.Fatalf("Shutdown() after invalid publication error = %v, want publication error", err)
	}
	if got := backend.closeCalls.Load(); got != 1 {
		t.Fatalf("backend Close calls = %d, want 1", got)
	}
}

func TestFacadeRuntimeBackendRejectsMalformedInternalPayload(t *testing.T) {
	backend := &facadeRuntimeBackend{backend: malformedFacadeBackend{}}
	err := backend.Run(context.Background(), func(SnapshotV1) {
		t.Fatal("malformed payload reached the public snapshot callback")
	})
	if err == nil || !strings.Contains(err.Error(), "decode internal runtime snapshot") {
		t.Fatalf("facade Run() error = %v", err)
	}
}

func TestRuntimeShutdownClosesBackendBeforeJoiningCancellationIgnoringRun(t *testing.T) {
	backend := newCloseReleasedRuntimeBackend()
	instance, err := newRuntime(validRuntimeConfig(t.TempDir()), func(context.Context, Config) (runtimeBackend, error) {
		return backend, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitRuntimeSignal(t, backend.runStarted, "cancellation-ignoring backend Run")

	shutdownErrors := callRuntimeConcurrently(t, 16, "cancellation-ignoring Shutdown", instance.Shutdown)
	for err := range shutdownErrors {
		if err != nil {
			t.Fatalf("Shutdown() error = %v", err)
		}
	}
	waitRuntimeSignal(t, backend.runExited, "backend exit after Close")
	if got := backend.closeCalls.Load(); got != 1 {
		t.Fatalf("backend Close calls = %d, want 1", got)
	}
}

func TestRuntimeStartPretrustDenialDoesNotLaunchOrLatchStarted(t *testing.T) {
	backend := newFakeRuntimeBackend()
	denied := errors.New("fixture runtime admission denied")
	var acquisitions atomic.Int32
	factory := runtimeBackendFactory(func(context.Context, Config) (runtimeBackend, error) {
		if acquisitions.Add(1) == 1 {
			return nil, denied
		}
		return backend, nil
	})
	instance, err := newRuntime(validRuntimeConfig(t.TempDir()), factory)
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.Start(context.Background()); !errors.Is(err, denied) {
		t.Fatalf("Start() admission error = %v, want denied", err)
	}
	if got := backend.runCalls.Load(); got != 0 {
		t.Fatalf("denied Start launched backend %d times", got)
	}
	if err := instance.Start(context.Background()); err != nil {
		t.Fatalf("Start() after admission became valid error = %v", err)
	}
	waitRuntimeSignal(t, backend.runStarted, "backend Run after admission")
	if got := acquisitions.Load(); got != 2 {
		t.Fatalf("backend acquisitions = %d, want 2", got)
	}
	if err := instance.Shutdown(); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeSnapshotAndPairingResultsAreDeeplyDetached(t *testing.T) {
	backend := newFakeRuntimeBackend()
	factory := runtimeBackendFactory(func(context.Context, Config) (runtimeBackend, error) {
		return backend, nil
	})
	instance, err := newRuntime(validRuntimeConfig(t.TempDir()), factory)
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Shutdown() })
	waitRuntimeSignal(t, backend.runStarted, "backend Run")
	backend.publish(t, lifecycleRuntimeSnapshot(t, "detached-session"))

	first, err := instance.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	wantHash := first.Meta.DataHash
	first.Pairing[0].Unknown[0].Value = eebusraw.OpaqueBytes([]byte("caller mutation"))
	first.Services[0].Unknown[0].Value = eebusraw.OpaqueBytes([]byte("caller mutation"))
	first.Topology.Devices[0].Entities[0].Features = nil
	first.Raw[0].Unknown[0].Value = eebusraw.OpaqueBytes([]byte("caller mutation"))

	pairing, err := instance.PairingState()
	if err != nil {
		t.Fatal(err)
	}
	pairing[0].Unknown[0].Value = eebusraw.OpaqueBytes([]byte("pairing mutation"))
	pairing[0].Raw[0].Unknown[0].Value = eebusraw.OpaqueBytes([]byte("pairing raw mutation"))

	again, err := instance.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	computed, err := again.ComputeDataHash()
	if err != nil {
		t.Fatal(err)
	}
	if computed != wantHash || again.Meta.DataHash != wantHash {
		t.Fatal("Snapshot returned caller-owned nested storage")
	}
	assertRuntimeFeatureGraphCounts(t, again)

	pairingAgain, err := instance.PairingState()
	if err != nil {
		t.Fatal(err)
	}
	if len(pairingAgain) != 1 || len(pairingAgain[0].Unknown) != 1 || len(pairingAgain[0].Raw) != 1 {
		t.Fatal("PairingState returned caller-owned nested storage")
	}
}

func TestRuntimeShutdownBeforeStartIsTerminalWithoutAcquisition(t *testing.T) {
	var acquisitions atomic.Int32
	factory := runtimeBackendFactory(func(context.Context, Config) (runtimeBackend, error) {
		acquisitions.Add(1)
		return newFakeRuntimeBackend(), nil
	})
	instance, err := newRuntime(validRuntimeConfig(t.TempDir()), factory)
	if err != nil {
		t.Fatal(err)
	}
	shutdownErrors := callRuntimeConcurrently(t, 16, "pre-start Shutdown", instance.Shutdown)
	for err := range shutdownErrors {
		if err != nil {
			t.Fatalf("Shutdown() before Start() error = %v", err)
		}
	}
	if got := acquisitions.Load(); got != 0 {
		t.Fatalf("Shutdown() before Start() acquired backend %d times", got)
	}
	if err := instance.Start(context.Background()); !errors.Is(err, ErrRuntimeShutdown) {
		t.Fatalf("Start() after pre-start Shutdown() error = %v, want ErrRuntimeShutdown", err)
	}
}

type fakeRuntimeUpdate struct {
	snapshot SnapshotV1
	applied  chan struct{}
}

type fakeRuntimeBackend struct {
	updates        chan fakeRuntimeUpdate
	failures       chan error
	runStarted     chan struct{}
	cancelled      chan struct{}
	runExited      chan struct{}
	runStartedOnce sync.Once
	cancelledOnce  sync.Once
	runExitedOnce  sync.Once
	runCalls       atomic.Int32
	closeCalls     atomic.Int32
}

type malformedFacadeBackend struct{}

func (malformedFacadeBackend) Run(ctx context.Context, publish func([]byte)) error {
	publish([]byte("{"))
	<-ctx.Done()
	return nil
}

func (malformedFacadeBackend) Close() error { return nil }

var _ eebusfacade.Backend = malformedFacadeBackend{}

func newFakeRuntimeBackend() *fakeRuntimeBackend {
	return &fakeRuntimeBackend{
		updates:    make(chan fakeRuntimeUpdate),
		failures:   make(chan error),
		runStarted: make(chan struct{}),
		cancelled:  make(chan struct{}),
		runExited:  make(chan struct{}),
	}
}

func (backend *fakeRuntimeBackend) Run(ctx context.Context, publish func(SnapshotV1)) error {
	backend.runCalls.Add(1)
	backend.runStartedOnce.Do(func() { close(backend.runStarted) })
	defer backend.runExitedOnce.Do(func() { close(backend.runExited) })
	for {
		select {
		case <-ctx.Done():
			backend.cancelledOnce.Do(func() { close(backend.cancelled) })
			return ctx.Err()
		case err := <-backend.failures:
			return err
		case update := <-backend.updates:
			publish(update.snapshot)
			close(update.applied)
		}
	}
}

func (backend *fakeRuntimeBackend) Close() error {
	backend.closeCalls.Add(1)
	return nil
}

func (backend *fakeRuntimeBackend) publish(t *testing.T, snapshot SnapshotV1) {
	t.Helper()
	update := fakeRuntimeUpdate{snapshot: snapshot, applied: make(chan struct{})}
	select {
	case backend.updates <- update:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out sending backend snapshot")
	}
	waitRuntimeSignal(t, update.applied, "snapshot publication")
}

func (backend *fakeRuntimeBackend) fail(t *testing.T, err error) {
	t.Helper()
	select {
	case backend.failures <- err:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out sending backend failure")
	}
}

type closeReleasedRuntimeBackend struct {
	runStarted chan struct{}
	closed     chan struct{}
	runExited  chan struct{}
	closeOnce  sync.Once
	closeCalls atomic.Int32
}

func newCloseReleasedRuntimeBackend() *closeReleasedRuntimeBackend {
	return &closeReleasedRuntimeBackend{
		runStarted: make(chan struct{}),
		closed:     make(chan struct{}),
		runExited:  make(chan struct{}),
	}
}

func (backend *closeReleasedRuntimeBackend) Run(context.Context, func(SnapshotV1)) error {
	close(backend.runStarted)
	<-backend.closed
	close(backend.runExited)
	return nil
}

func (backend *closeReleasedRuntimeBackend) Close() error {
	backend.closeCalls.Add(1)
	backend.closeOnce.Do(func() { close(backend.closed) })
	return nil
}

func lifecycleRuntimeSnapshot(t *testing.T, sessionName string) SnapshotV1 {
	t.Helper()
	draft := rawSnapshotV1(t, false)
	draft.Pairing = append([]PairingObservationV1(nil), draft.Pairing[:1]...)
	draft.Services = append([]ServiceV1(nil), draft.Services[:1]...)
	draft.Sessions = []SessionV1{{
		ID:     rawSnapshotID(t, eebusraw.IDKindSession, sessionName),
		Remote: draft.Pairing[0].Remote,
		State:  ObservedSessionStateV1Connected,
		Since:  draft.Meta.DataTimestamp,
	}}
	for _, device := range draft.Topology.Devices {
		if len(device.Entities) != 0 {
			draft.Topology.Devices = []DeviceV1{device}
			break
		}
	}
	evidence := eebusevidence.NewObjectV1(
		eebusevidence.ObjectKindIdentity,
		rawSnapshotDigest("f"),
		1,
		draft.Meta.DataTimestamp,
	)
	evidence.Unknown = []eebusraw.UnknownField{rawSnapshotUnknown("pairing evidence")}
	draft.Pairing[0].Raw = []eebusevidence.ObjectV1{evidence}
	draft.Pairing[0].Unknown = []eebusraw.UnknownField{rawSnapshotUnknown("pairing")}

	snapshot, err := NewSnapshotV1(draft)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func assertRuntimeSnapshotHashIntact(t *testing.T, instance Runtime) {
	t.Helper()
	snapshot, err := instance.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	computed, err := snapshot.ComputeDataHash()
	if err != nil {
		t.Fatal(err)
	}
	if computed != snapshot.Meta.DataHash {
		t.Fatal("runtime retained storage owned by the backend publisher")
	}
}

func assertRuntimeReconnectGraph(t *testing.T, instance Runtime, sessionName string) {
	t.Helper()
	snapshot, err := instance.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Sessions) != 1 {
		t.Fatalf("sessions after reconnect = %d, want 1", len(snapshot.Sessions))
	}
	wantSession := rawSnapshotID(t, eebusraw.IDKindSession, sessionName)
	if snapshot.Sessions[0].ID != wantSession {
		t.Fatal("reconnect retained the superseded session")
	}
	assertRuntimeFeatureGraphCounts(t, snapshot)
}

func assertRuntimeFeatureGraphCounts(t *testing.T, snapshot SnapshotV1) {
	t.Helper()
	if len(snapshot.Services) != 1 || len(snapshot.Sessions) != 1 || len(snapshot.Topology.Devices) != 1 {
		t.Fatalf("runtime graph counts services=%d sessions=%d devices=%d, want 1/1/1", len(snapshot.Services), len(snapshot.Sessions), len(snapshot.Topology.Devices))
	}
	device := snapshot.Topology.Devices[0]
	if len(device.Entities) != 1 || len(device.UseCaseClaims) != 2 {
		t.Fatalf("device graph counts entities=%d usecases=%d, want 1/2", len(device.Entities), len(device.UseCaseClaims))
	}
	if len(device.Entities[0].Features) != 2 {
		t.Fatalf("entity feature count = %d, want 2", len(device.Entities[0].Features))
	}
}

func callRuntimeConcurrently(t *testing.T, count int, label string, call func() error) <-chan error {
	t.Helper()
	errorsOut := make(chan error, count)
	start := make(chan struct{})
	done := make(chan struct{})
	var wait sync.WaitGroup
	for index := 0; index < count; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			errorsOut <- call()
		}()
	}
	close(start)
	go func() {
		wait.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
	close(errorsOut)
	return errorsOut
}

func waitRuntimeSignal(t *testing.T, signal <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func waitRuntimeResult(t *testing.T, result <-chan error, label string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
		return nil
	}
}

func waitRuntimeWorkerExit(t *testing.T, instance Runtime) {
	t.Helper()
	implementation, ok := instance.(*runtimeImplementation)
	if !ok {
		t.Fatal("runtime implementation has unexpected concrete type")
	}
	implementation.mu.Lock()
	done := implementation.done
	implementation.mu.Unlock()
	if done == nil {
		t.Fatal("runtime worker was not started")
	}
	waitRuntimeSignal(t, done, "runtime worker exit")
}
