package eebusfacade

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	eebusapi "github.com/enbility/eebus-go/api"
	eebusmocks "github.com/enbility/eebus-go/mocks"
	shipapi "github.com/enbility/ship-go/api"
	shipcert "github.com/enbility/ship-go/cert"
	spineapi "github.com/enbility/spine-go/api"
	spinemocks "github.com/enbility/spine-go/mocks"
	spinemodel "github.com/enbility/spine-go/model"
)

func TestAcquireRuntimeUsesProtectedMaterialAndPublishesEEBusCallbacks(t *testing.T) {
	certificate, err := shipcert.CreateCertificate("", "Helianthus", "RO", "runtime-test")
	if err != nil {
		t.Fatal(err)
	}
	localSKI := certificateSKI(t, certificate)
	remoteSKI := "0000000000000000000000000000000000000002"
	clock := &runtimeTestClock{value: time.Unix(1_700_000_000, 0).UTC()}
	service := &fakeRuntimeService{started: make(chan struct{})}
	var handler eebusapi.ServiceReaderInterface
	dependencies := runtimeDependencies{
		loadMaterial: func(context.Context, string) (runtimeMaterial, error) {
			return runtimeMaterial{
				certificate: certificate,
				localSKI:    localSKI,
				pretrusted:  map[string]bool{remoteSKI: true},
			}, nil
		},
		newService: func(_ RuntimeConfig, _ runtimeMaterial, reader eebusapi.ServiceReaderInterface) (runtimeService, error) {
			handler = reader
			return service, nil
		},
		now: clock.Now,
	}
	config := RuntimeConfig{
		StateRoot:  "/tmp/helianthus-eebus-runtime-test",
		Interface:  "fixture-interface",
		ListenPort: 4711,
		Remotes:    []RuntimeRemote{{SKI: remoteSKI}},
	}

	backend, err := acquireRuntime(context.Background(), config, dependencies)
	if err != nil {
		t.Fatalf("acquireRuntime() error = %v", err)
	}
	if !service.setup || len(service.registered) != 1 || service.registered[0] != remoteSKI {
		t.Fatalf("service setup=%t registered=%v", service.setup, service.registered)
	}
	if handler == nil {
		t.Fatal("service reader callback was not installed")
	}

	ctx, cancel := context.WithCancel(context.Background())
	updates := make(chan []byte, 8)
	runDone := make(chan error, 1)
	go func() {
		runDone <- backend.Run(ctx, func(payload []byte) {
			updates <- append([]byte(nil), payload...)
		})
	}()
	initial := decodeRuntimePayload(t, waitRuntimePayload(t, updates))
	select {
	case <-service.started:
	case <-time.After(time.Second):
		t.Fatal("runtime service did not start")
	}
	if initial.Status.State != "degraded" || initial.Status.Degradation == nil || initial.Status.Degradation.Reason != "no-visible-services" {
		t.Fatalf("initial status = %+v", initial.Status)
	}
	initialSessionID := initial.Sessions[0].ID

	clock.Advance(time.Second)
	handler.ServiceShipIDUpdate(remoteSKI, "fixture-ship-id")
	shipIDUpdate := decodeRuntimePayload(t, waitRuntimePayload(t, updates))
	if shipIDUpdate.Sessions[0].ID == initialSessionID {
		t.Fatal("SHIP ID callback did not replace the session identity")
	}
	if strings.Contains(shipIDUpdate.Sessions[0].ID.Digest, "fixture-ship-id") {
		t.Fatal("SHIP ID escaped redaction")
	}

	clock.Advance(time.Second)
	handler.VisibleRemoteServicesUpdated(nil, []shipapi.RemoteService{{Ski: remoteSKI}})
	visible := decodeRuntimePayload(t, waitRuntimePayload(t, updates))
	if len(visible.Services) != 1 || !visible.Services[0].Visible || !visible.Services[0].Paired {
		t.Fatalf("visible services = %+v", visible.Services)
	}

	remoteService := eebusServiceWithFeatureGraph(t, remoteSKI)
	clock.Advance(time.Second)
	handler.RemoteSKIConnected(remoteService, remoteSKI)
	connected := decodeRuntimePayload(t, waitRuntimePayload(t, updates))
	if connected.Status.State != "ready" || len(connected.Sessions) != 1 || connected.Sessions[0].State != "connected" {
		t.Fatalf("connected status=%+v sessions=%+v", connected.Status, connected.Sessions)
	}
	if len(connected.Topology.Devices) != 1 || len(connected.Topology.Devices[0].Entities) != 1 || len(connected.Topology.Devices[0].Entities[0].Features) != 1 {
		t.Fatalf("connected topology = %+v", connected.Topology)
	}
	connectedSessionID := connected.Sessions[0].ID
	if connectedSessionID == shipIDUpdate.Sessions[0].ID {
		t.Fatal("connected callback did not create a new session generation")
	}

	clock.Advance(time.Second)
	handler.RemoteSKIDisconnected(remoteService, remoteSKI)
	disconnected := decodeRuntimePayload(t, waitRuntimePayload(t, updates))
	if disconnected.Status.State != "degraded" || disconnected.Status.Degradation == nil || disconnected.Status.Degradation.Reason != "remote-disconnect" {
		t.Fatalf("disconnected status = %+v", disconnected.Status)
	}
	if len(disconnected.Topology.Devices) != 1 {
		t.Fatal("disconnect discarded the last observed feature graph")
	}

	reconnectedService := eebusServiceWithFeatureGraph(t, remoteSKI)
	clock.Advance(time.Second)
	handler.RemoteSKIConnected(reconnectedService, remoteSKI)
	reconnected := decodeRuntimePayload(t, waitRuntimePayload(t, updates))
	if reconnected.Sessions[0].ID == connectedSessionID {
		t.Fatal("reconnect reused the previous session identity")
	}

	cancel()
	if err := <-runDone; err != nil {
		t.Fatalf("Run() cancellation error = %v", err)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
	if service.shutdowns != 1 {
		t.Fatalf("service shutdown count = %d, want 1", service.shutdowns)
	}
}

func TestAcquireRuntimeKeepsFirstTrustDisabledWithoutInternalAuthorization(t *testing.T) {
	certificate, err := shipcert.CreateCertificate("", "Helianthus", "RO", "runtime-disabled-test")
	if err != nil {
		t.Fatal(err)
	}
	localSKI := certificateSKI(t, certificate)
	remoteSKI := "0000000000000000000000000000000000000002"
	stateRoot := filepath.Join(t.TempDir(), "state")
	service := &fakeRuntimeService{started: make(chan struct{})}
	backend, err := acquireRuntime(context.Background(), RuntimeConfig{
		StateRoot:  stateRoot,
		Interface:  "fixture-interface",
		ListenPort: 4711,
		Remotes:    []RuntimeRemote{{SKI: remoteSKI}},
	}, runtimeDependencies{
		loadMaterial: func(context.Context, string) (runtimeMaterial, error) {
			return runtimeMaterial{
				certificate: certificate,
				localSKI:    localSKI,
				pretrusted:  map[string]bool{remoteSKI: true},
			}, nil
		},
		newService: func(RuntimeConfig, runtimeMaterial, eebusapi.ServiceReaderInterface) (runtimeService, error) {
			return service, nil
		},
		now: time.Now,
	})
	if err != nil {
		t.Fatalf("acquireRuntime() error = %v", err)
	}
	implementation, ok := backend.(*serviceBackend)
	if !ok {
		t.Fatal("runtime backend implementation changed")
	}
	if implementation.firstTrust != nil {
		t.Fatal("first trust activated without internal authorization")
	}
	service.mu.Lock()
	autoAcceptCalls := len(service.autoAccept)
	waitingCalls := len(service.waiting)
	service.mu.Unlock()
	if autoAcceptCalls != 0 || waitingCalls != 0 {
		t.Fatal("disabled first trust changed service pairing controls")
	}
	if _, err := os.Lstat(stateRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disabled first trust touched StateRoot: %v", err)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireRuntimeComposesAndOwnsAuthorizedFirstTrustResources(t *testing.T) {
	certificate, err := shipcert.CreateCertificate("", "Helianthus", "RO", "runtime-first-trust-test")
	if err != nil {
		t.Fatal(err)
	}
	localSKI := certificateSKI(t, certificate)
	remoteSKI := "0000000000000000000000000000000000000002"
	candidateSKI := "0000000000000000000000000000000000000003"
	root := canonicalRuntimeTempDir(t)
	stateRoot := filepath.Join(root, "state")
	adminRuntimeDir := filepath.Join(root, "admin-runtime")
	service := &fakeRuntimeService{started: make(chan struct{})}
	var reader eebusapi.ServiceReaderInterface
	acquireContext, cancelAcquire := context.WithCancel(context.Background())
	defer cancelAcquire()
	dependencies := defaultRuntimeDependencies
	dependencies.loadMaterial = func(context.Context, string) (runtimeMaterial, error) {
		return runtimeMaterial{
			certificate: certificate,
			localSKI:    localSKI,
			pretrusted:  map[string]bool{remoteSKI: true},
			firstTrust: &runtimeFirstTrustAuthorization{
				adminRuntimeDir: adminRuntimeDir,
			},
		}, nil
	}
	dependencies.newService = func(_ RuntimeConfig, _ runtimeMaterial, callback eebusapi.ServiceReaderInterface) (runtimeService, error) {
		reader = callback
		return service, nil
	}
	dependencies.now = time.Now
	backend, err := acquireRuntime(acquireContext, RuntimeConfig{
		StateRoot:  stateRoot,
		Interface:  "fixture-interface",
		ListenPort: 4711,
		Remotes:    []RuntimeRemote{{SKI: remoteSKI}},
	}, dependencies)
	if err != nil {
		t.Fatalf("acquireRuntime() error = %v", err)
	}
	cancelAcquire()
	implementation, ok := backend.(*serviceBackend)
	if !ok || implementation.firstTrust == nil {
		t.Fatal("authorized runtime did not compose first trust resources")
	}
	resources := implementation.firstTrust
	if resources.coordinator.state() != "PAIRING_CLOSED" || resources.store.SelectedGeneration() == 0 {
		t.Fatal("authorized first trust did not reopen the selected durable generation")
	}
	if reader == nil {
		t.Fatal("runtime service reader was not composed")
	}
	if got := resources.coordinator.openPairingWindow(context.Background(), "runtime-composition-window", time.Minute); got != "open_empty" {
		t.Fatalf("open pairing window outcome = %q", got)
	}
	reader.ServiceShipIDUpdate(candidateSKI, "runtime-composition-ship-id")
	reader.RemoteSKIConnected(nil, candidateSKI)
	reader.ServicePairingDetailUpdate(candidateSKI, shipapi.NewConnectionStateDetail(shipapi.ConnectionStateReceivedPairingRequest, nil))
	if _, _, _, _, _, complete, candidate := resources.coordinator.candidate(); !candidate || !complete {
		t.Fatal("runtime callback composition did not reach the first trust coordinator")
	}

	adminAddress := resources.admin.Address()
	connection, err := net.Dial("unix", adminAddress)
	if err != nil {
		t.Fatalf("dial composed AF_UNIX admin endpoint: %v", err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	autoAccept := append([]bool(nil), service.autoAccept...)
	waiting := append([]bool(nil), service.waiting...)
	service.mu.Unlock()
	if len(autoAccept) != 1 || autoAccept[0] || len(waiting) == 0 {
		t.Fatalf("service pairing controls auto=%v waiting=%v", autoAccept, waiting)
	}
	for _, value := range waiting {
		if value && resources.coordinator.state() == "PAIRING_CLOSED" {
			t.Fatal("pairing waiting was enabled while the coordinator was closed")
		}
	}

	if err := backend.Close(); err != nil {
		t.Fatalf("close authorized runtime: %v", err)
	}
	if _, err := os.Lstat(adminAddress); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("admin socket survived backend close: %v", err)
	}
	if resources.store.SelectedGeneration() != 0 {
		t.Fatal("association bridge remained open after backend close")
	}
	service.mu.Lock()
	shutdowns := service.shutdowns
	service.mu.Unlock()
	if shutdowns != 1 {
		t.Fatalf("service shutdown count = %d, want 1", shutdowns)
	}
	if err := backend.Close(); err != nil {
		t.Fatalf("second backend close: %v", err)
	}
}

func TestAcquireRuntimeFailsClosedAndReleasesStoreWhenAdminStartFails(t *testing.T) {
	certificate, err := shipcert.CreateCertificate("", "Helianthus", "RO", "runtime-first-trust-failure-test")
	if err != nil {
		t.Fatal(err)
	}
	localSKI := certificateSKI(t, certificate)
	remoteSKI := "0000000000000000000000000000000000000002"
	root := canonicalRuntimeTempDir(t)
	stateRoot := filepath.Join(root, "state")
	adminRuntimeDir := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(adminRuntimeDir, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := &fakeRuntimeService{started: make(chan struct{})}
	dependencies := defaultRuntimeDependencies
	dependencies.loadMaterial = func(context.Context, string) (runtimeMaterial, error) {
		return runtimeMaterial{
			certificate: certificate,
			localSKI:    localSKI,
			pretrusted:  map[string]bool{remoteSKI: true},
			firstTrust: &runtimeFirstTrustAuthorization{
				adminRuntimeDir: adminRuntimeDir,
			},
		}, nil
	}
	dependencies.newService = func(RuntimeConfig, runtimeMaterial, eebusapi.ServiceReaderInterface) (runtimeService, error) {
		return service, nil
	}
	dependencies.now = time.Now
	backend, err := acquireRuntime(context.Background(), RuntimeConfig{
		StateRoot:  stateRoot,
		Interface:  "fixture-interface",
		ListenPort: 4711,
		Remotes:    []RuntimeRemote{{SKI: remoteSKI}},
	}, dependencies)
	if err == nil || backend != nil {
		t.Fatal("runtime acquisition did not fail closed when admin startup failed")
	}
	service.mu.Lock()
	shutdowns := service.shutdowns
	service.mu.Unlock()
	if shutdowns != 1 {
		t.Fatalf("failed acquisition service shutdown count = %d, want 1", shutdowns)
	}
	store, outcome := openRuntimeAssociationBridge(stateRoot, nil)
	if store == nil || outcome != "opened_current" {
		t.Fatalf("store remained owned after failed acquisition: %q", outcome)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireRuntimeFailsClosedBeforeServiceSetupWithoutProtectedMaterial(t *testing.T) {
	serviceCreated := false
	_, err := acquireRuntime(context.Background(), RuntimeConfig{
		StateRoot:  "/tmp/helianthus-eebus-runtime-test",
		Interface:  "fixture-interface",
		ListenPort: 4711,
		Remotes:    []RuntimeRemote{{SKI: "0000000000000000000000000000000000000002", Allowlisted: true}},
	}, runtimeDependencies{
		loadMaterial: func(context.Context, string) (runtimeMaterial, error) {
			return runtimeMaterial{}, errProtectedRuntimeCredentials
		},
		newService: func(RuntimeConfig, runtimeMaterial, eebusapi.ServiceReaderInterface) (runtimeService, error) {
			serviceCreated = true
			return &fakeRuntimeService{started: make(chan struct{})}, nil
		},
		now: time.Now,
	})
	if err == nil || !containsRuntimeError(err, errProtectedRuntimeCredentials.Error()) {
		t.Fatalf("acquireRuntime() error = %v", err)
	}
	if serviceCreated {
		t.Fatal("runtime service was created before protected material loaded")
	}
}

func TestPinnedEEBusServiceFailsClosedWithoutScopedSHIPListener(t *testing.T) {
	_, err := newEEBusService(RuntimeConfig{}, runtimeMaterial{}, nil)
	if !errors.Is(err, errScopedSHIPListenerUnavailable) {
		t.Fatalf("newEEBusService() error = %v, want scoped-listener failure", err)
	}
}

func TestServiceBackendCloseBeforeStartCannotReopenTransport(t *testing.T) {
	localSKI := "0000000000000000000000000000000000000001"
	remoteSKI := "0000000000000000000000000000000000000002"
	handler, err := newRuntimeServiceHandler(RuntimeConfig{
		Remotes: []RuntimeRemote{{SKI: remoteSKI, Allowlisted: true}},
	}, localSKI, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	service := &fakeRuntimeService{started: make(chan struct{})}
	backend := &serviceBackend{service: service, handler: handler}
	publishEntered := make(chan struct{})
	releasePublish := make(chan struct{})
	runDone := make(chan error, 1)
	go func() {
		runDone <- backend.Run(context.Background(), func([]byte) {
			close(publishEntered)
			<-releasePublish
		})
	}()
	select {
	case <-publishEntered:
	case <-time.After(time.Second):
		t.Fatal("Run did not reach initial publication")
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
	close(releasePublish)
	if err := <-runDone; err != nil {
		t.Fatalf("Run after Close error = %v", err)
	}
	select {
	case <-service.started:
		t.Fatal("service started after backend was closed")
	default:
	}
	if service.shutdowns != 1 {
		t.Fatalf("service shutdown count = %d, want 1", service.shutdowns)
	}
}

type fakeRuntimeService struct {
	mu         sync.Mutex
	setup      bool
	started    chan struct{}
	shutdowns  int
	registered []string
	autoAccept []bool
	waiting    []bool
	cancels    int
}

func (service *fakeRuntimeService) Setup() error {
	service.setup = true
	return nil
}

func (service *fakeRuntimeService) Start() { close(service.started) }

func (service *fakeRuntimeService) Shutdown() {
	service.mu.Lock()
	service.shutdowns++
	service.mu.Unlock()
}

func (service *fakeRuntimeService) RegisterRemoteSKI(ski string) {
	service.registered = append(service.registered, ski)
}

func (service *fakeRuntimeService) SetAutoAccept(value bool) {
	service.mu.Lock()
	service.autoAccept = append(service.autoAccept, value)
	service.mu.Unlock()
}

func (service *fakeRuntimeService) CancelPairingWithSKI(string) {
	service.mu.Lock()
	service.cancels++
	service.mu.Unlock()
}

func (service *fakeRuntimeService) UserIsAbleToApproveOrCancelPairingRequests(value bool) {
	service.mu.Lock()
	service.waiting = append(service.waiting, value)
	service.mu.Unlock()
}

func (*fakeRuntimeService) LocalService() *shipapi.ServiceDetails { return nil }

func (*fakeRuntimeService) LocalDevice() spineapi.DeviceLocalInterface { return nil }

type runtimeTestClock struct {
	mu    sync.Mutex
	value time.Time
}

func (clock *runtimeTestClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.value
}

func (clock *runtimeTestClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	clock.value = clock.value.Add(duration)
	clock.mu.Unlock()
}

func certificateSKI(t *testing.T, certificate tls.Certificate) string {
	t.Helper()
	parsed, err := x509Certificate(certificate)
	if err != nil {
		t.Fatal(err)
	}
	ski, err := shipcert.SkiFromCertificate(parsed)
	if err != nil {
		t.Fatal(err)
	}
	return ski
}

func x509Certificate(certificate tls.Certificate) (*x509.Certificate, error) {
	return x509.ParseCertificate(certificate.Certificate[0])
}

func eebusServiceWithFeatureGraph(t *testing.T, ski string) eebusapi.ServiceInterface {
	t.Helper()
	service := eebusmocks.NewServiceInterface(t)
	local := spinemocks.NewDeviceLocalInterface(t)
	remote := spinemocks.NewDeviceRemoteInterface(t)
	entity := spinemocks.NewEntityRemoteInterface(t)
	feature := spinemocks.NewFeatureRemoteInterface(t)
	deviceAddress := spinemodel.AddressDeviceType("d:_n:Vaillant_VR940")
	featureAddress := spinemodel.AddressFeatureType(1)

	service.EXPECT().LocalDevice().Return(local)
	local.EXPECT().RemoteDeviceForSki(ski).Return(remote)
	remote.EXPECT().Address().Return(&deviceAddress)
	remote.EXPECT().Entities().Return([]spineapi.EntityRemoteInterface{entity})
	remote.EXPECT().UseCases().Return([]spinemodel.UseCaseInformationDataType{{}})
	entity.EXPECT().Address().Return(&spinemodel.EntityAddressType{Device: &deviceAddress, Entity: []spinemodel.AddressEntityType{1}})
	entity.EXPECT().Features().Return([]spineapi.FeatureRemoteInterface{feature})
	feature.EXPECT().Address().Return(&spinemodel.FeatureAddressType{Device: &deviceAddress, Entity: []spinemodel.AddressEntityType{1}, Feature: &featureAddress})
	feature.EXPECT().Role().Return(spinemodel.RoleTypeClient)
	return service
}

func decodeRuntimePayload(t *testing.T, payload []byte) runtimeSnapshotPayload {
	t.Helper()
	var decoded runtimeSnapshotPayload
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func waitRuntimePayload(t *testing.T, updates <-chan []byte) []byte {
	t.Helper()
	select {
	case payload := <-updates:
		return payload
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for runtime snapshot")
		return nil
	}
}

func containsRuntimeError(err error, text string) bool {
	return err != nil && strings.Contains(err.Error(), text)
}

func canonicalRuntimeTempDir(t *testing.T) string {
	t.Helper()
	base := os.TempDir()
	if short, err := filepath.EvalSymlinks(filepath.Join(string(filepath.Separator), "tmp")); err == nil {
		base = short
	}
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(base, "e4b-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove runtime temp directory: %v", err)
		}
	})
	return root
}
