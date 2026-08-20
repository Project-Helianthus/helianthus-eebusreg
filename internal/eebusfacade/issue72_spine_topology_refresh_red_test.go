package eebusfacade

import (
	"context"
	"sync"
	"testing"
	"time"

	eebusapi "github.com/Project-Helianthus/helianthus-eebus-go/api"
	eebusmocks "github.com/Project-Helianthus/helianthus-eebus-go/mocks"
	shipcert "github.com/Project-Helianthus/helianthus-ship-go/cert"
	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	spinemocks "github.com/Project-Helianthus/helianthus-spine-go/mocks"
	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
	spine "github.com/Project-Helianthus/helianthus-spine-go/spine"
)

func TestIssue72ProductionSPINESubscriptionDeliversAndUnsubscribes(t *testing.T) {
	recorder := &issue72EventRecorder{called: make(chan struct{}, 2)}
	unsubscribe, err := subscribeRuntimeSPINEEvents(recorder)
	if err != nil {
		t.Fatal(err)
	}
	spine.Events.Publish(spineapi.EventPayload{})
	select {
	case <-recorder.called:
	case <-time.After(time.Second):
		t.Fatal("production SPINE subscription did not deliver")
	}
	if err := unsubscribe(); err != nil {
		t.Fatal(err)
	}
	spine.Events.Publish(spineapi.EventPayload{})
	select {
	case <-recorder.called:
		t.Fatal("production SPINE subscription delivered after unsubscribe")
	case <-time.After(20 * time.Millisecond):
	}
}

func TestIssue72RefreshesTopologyFromDetailedDiscoveryAndStopsBeforeShutdown(t *testing.T) {
	certificate, err := shipcert.CreateCertificate("", "Helianthus", "RO", "issue72")
	if err != nil {
		t.Fatal(err)
	}
	localSKI := certificateSKI(t, certificate)
	remoteSKI := "0000000000000000000000000000000000000072"
	clock := &runtimeTestClock{value: time.Unix(1_700_000_072, 0).UTC()}
	service := &fakeRuntimeService{started: make(chan struct{})}
	var reader eebusapi.ServiceReaderInterface
	var eventHandler spineapi.EventHandlerInterface
	var eventMu sync.Mutex
	events := make([]string, 0, 2)

	dependencies := runtimeDependencies{
		loadMaterial: func(context.Context, string) (runtimeMaterial, error) {
			return runtimeMaterial{
				certificate: certificate,
				localSKI:    localSKI,
				nodeToken:   runtimeTestNodeToken,
				pretrusted:  map[string]bool{remoteSKI: true},
			}, nil
		},
		newService: func(_ RuntimeConfig, _ runtimeMaterial, candidate eebusapi.ServiceReaderInterface) (runtimeService, error) {
			reader = candidate
			return service, nil
		},
		subscribeSPINEEvents: func(candidate spineapi.EventHandlerInterface) (func() error, error) {
			eventHandler = candidate
			return func() error {
				eventMu.Lock()
				events = append(events, "unsubscribe")
				eventMu.Unlock()
				return nil
			}, nil
		},
		now: clock.Now,
	}
	backend, err := acquireRuntime(context.Background(), RuntimeConfig{
		StateRoot:  runtimeTestStateRoot(t),
		Interface:  "fixture-interface",
		ListenPort: 4711,
		Remotes:    []RuntimeRemote{{SKI: remoteSKI}},
	}, dependencies)
	if err != nil {
		t.Fatalf("acquire runtime: %v", err)
	}
	if eventHandler == nil {
		t.Fatal("SPINE application event handler was not subscribed")
	}

	ctx, cancel := context.WithCancel(context.Background())
	updates := make(chan []byte, 8)
	runDone := make(chan error, 1)
	go func() {
		runDone <- backend.Run(ctx, func(payload []byte) {
			updates <- append([]byte(nil), payload...)
		})
	}()
	_ = waitRuntimePayload(t, updates)

	remoteService := eebusmocks.NewServiceInterface(t)
	localDevice := spinemocks.NewDeviceLocalInterface(t)
	remoteDevice := issue72RemoteDevice(t, remoteSKI)
	service.localDevice = localDevice
	remoteService.EXPECT().LocalDevice().Return(localDevice)
	localDevice.EXPECT().RemoteDeviceForSki(remoteSKI).Return(nil).Once()

	clock.Advance(time.Second)
	reader.RemoteSKIConnected(remoteService, remoteSKI)
	connected := decodeRuntimePayload(t, waitRuntimePayload(t, updates))
	if len(connected.Sessions) != 1 || connected.Sessions[0].State != "connected" {
		t.Fatalf("connected sessions = %+v", connected.Sessions)
	}
	if len(connected.Devices) != 0 {
		t.Fatalf("pre-discovery devices = %+v, want empty", connected.Devices)
	}

	remoteService.EXPECT().LocalDevice().Return(localDevice)
	localDevice.EXPECT().RemoteDeviceForSki(remoteSKI).Return(remoteDevice).Once()
	foreignRemote := spinemocks.NewDeviceRemoteInterface(t)
	eventHandler.HandleEvent(spineapi.EventPayload{
		Ski:        remoteSKI,
		EventType:  spineapi.EventTypeDeviceChange,
		ChangeType: spineapi.ElementChangeAdd,
		Device:     foreignRemote,
	})
	select {
	case payload := <-updates:
		t.Fatalf("foreign runtime event published %s", payload)
	case <-time.After(20 * time.Millisecond):
	}

	remoteService.EXPECT().LocalDevice().Return(localDevice)
	// Capture the graph while the native callback still serializes the SPINE
	// mutation. A background worker must not traverse the mutable remote again.
	localDevice.EXPECT().RemoteDeviceForSki(remoteSKI).Return(remoteDevice).Once()
	clock.Advance(time.Second)
	eventHandler.HandleEvent(spineapi.EventPayload{
		Ski:        remoteSKI,
		EventType:  spineapi.EventTypeDeviceChange,
		ChangeType: spineapi.ElementChangeAdd,
		Device:     remoteDevice,
	})
	discovered := decodeRuntimePayload(t, waitRuntimePayload(t, updates))
	if len(discovered.Devices) != 1 || len(discovered.Entities) != 1 || len(discovered.Features) != 1 {
		t.Fatalf("post-discovery graph = devices:%+v entities:%+v features:%+v", discovered.Devices, discovered.Entities, discovered.Features)
	}

	cancel()
	if err := <-runDone; err != nil {
		t.Fatalf("stop runtime: %v", err)
	}
	if err := backend.Close(); err != nil {
		t.Fatalf("close runtime: %v", err)
	}
	eventMu.Lock()
	gotEvents := append([]string(nil), events...)
	eventMu.Unlock()
	if len(gotEvents) != 1 || gotEvents[0] != "unsubscribe" {
		t.Fatalf("close events = %v, want one unsubscribe", gotEvents)
	}

	eventHandler.HandleEvent(spineapi.EventPayload{
		Ski:        remoteSKI,
		EventType:  spineapi.EventTypeDeviceChange,
		ChangeType: spineapi.ElementChangeAdd,
		Device:     remoteDevice,
	})
	select {
	case payload := <-updates:
		t.Fatalf("callback after close published %s", payload)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestIssue72CloseWaitsForInFlightSPINECallbackAfterUnsubscribe(t *testing.T) {
	certificate, err := shipcert.CreateCertificate("", "Helianthus", "RO", "issue72-close")
	if err != nil {
		t.Fatal(err)
	}
	localSKI := certificateSKI(t, certificate)
	remoteSKI := "0000000000000000000000000000000000000072"
	service := &fakeRuntimeService{started: make(chan struct{})}
	localDevice := spinemocks.NewDeviceLocalInterface(t)
	remoteDevice := spinemocks.NewDeviceRemoteInterface(t)
	remoteAddress := spinemodel.AddressDeviceType("d:_n:close")
	remoteDeviceType := spinemodel.DeviceTypeTypeEnergyManagementSystem
	remoteDevice.EXPECT().Address().Return(&remoteAddress)
	remoteDevice.EXPECT().DeviceType().Return(&remoteDeviceType)
	remoteDevice.EXPECT().DestinationData().Return(spinemodel.NodeManagementDestinationDataType{})
	remoteDevice.EXPECT().FeatureSet().Return(nil)
	remoteDevice.EXPECT().Entities().Return(nil)
	remoteDevice.EXPECT().UseCases().Return(nil)
	var reader eebusapi.ServiceReaderInterface
	var eventHandler spineapi.EventHandlerInterface
	unsubscribed := make(chan struct{})
	stateRoot := issue84PrivateRoot(t)

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
				nodeToken:   runtimeTestNodeToken,
				pretrusted:  map[string]bool{remoteSKI: true},
			}, nil
		},
		newService: func(_ RuntimeConfig, _ runtimeMaterial, candidate eebusapi.ServiceReaderInterface) (runtimeService, error) {
			reader = candidate
			return service, nil
		},
		subscribeSPINEEvents: func(candidate spineapi.EventHandlerInterface) (func() error, error) {
			eventHandler = candidate
			return func() error {
				close(unsubscribed)
				return nil
			}, nil
		},
		now: time.Now,
	})
	if err != nil {
		t.Fatalf("acquire runtime: %v", err)
	}
	service.mu.Lock()
	service.localDevice = localDevice
	service.mu.Unlock()

	connectedService := eebusmocks.NewServiceInterface(t)
	connectedService.EXPECT().LocalDevice().Return(localDevice)
	localDevice.EXPECT().RemoteDeviceForSki(remoteSKI).Return(nil).Once()
	reader.RemoteSKIConnected(connectedService, remoteSKI)

	callbackEntered := make(chan struct{})
	releaseCallback := make(chan struct{})
	localDevice.EXPECT().RemoteDeviceForSki(remoteSKI).Run(func(string) {
		close(callbackEntered)
		<-releaseCallback
	}).Return(remoteDevice).Once()
	callbackDone := make(chan struct{})
	go func() {
		defer close(callbackDone)
		eventHandler.HandleEvent(spineapi.EventPayload{
			Ski:        remoteSKI,
			EventType:  spineapi.EventTypeDeviceChange,
			ChangeType: spineapi.ElementChangeAdd,
			Device:     remoteDevice,
		})
	}()
	select {
	case <-callbackEntered:
	case runtimeErr := <-backend.(*serviceBackend).handler.errors:
		t.Fatalf("in-flight callback setup failed: %v", runtimeErr)
	case <-time.After(time.Second):
		t.Fatal("in-flight callback did not enter")
	}

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- backend.Close()
	}()
	<-unsubscribed
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before in-flight callback completed: %v", err)
	default:
	}
	close(releaseCallback)
	<-callbackDone
	if err := <-closeDone; err != nil {
		t.Fatalf("close runtime: %v", err)
	}
}

func TestIssue72TopologyEventsIncludeUseCaseDataAndExcludeDeviceRemoval(t *testing.T) {
	tests := []struct {
		name    string
		payload spineapi.EventPayload
		want    bool
	}{
		{
			name: "detailed discovery",
			payload: spineapi.EventPayload{
				EventType: spineapi.EventTypeDeviceChange, ChangeType: spineapi.ElementChangeAdd,
			},
			want: true,
		},
		{
			name: "entity removal",
			payload: spineapi.EventPayload{
				EventType: spineapi.EventTypeEntityChange, ChangeType: spineapi.ElementChangeRemove,
			},
			want: true,
		},
		{
			name: "use case data",
			payload: spineapi.EventPayload{
				EventType: spineapi.EventTypeDataChange,
				Data:      &spinemodel.NodeManagementUseCaseDataType{},
			},
			want: true,
		},
		{
			name: "unrelated data",
			payload: spineapi.EventPayload{
				EventType: spineapi.EventTypeDataChange,
				Data:      struct{}{},
			},
		},
		{
			name: "device disconnect",
			payload: spineapi.EventPayload{
				EventType: spineapi.EventTypeDeviceChange, ChangeType: spineapi.ElementChangeRemove,
			},
			want: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := runtimeTopologyEvent(test.payload); got != test.want {
				t.Fatalf("runtimeTopologyEvent() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestIssue72PreservesSpecialSPINEFeatureRole(t *testing.T) {
	remoteSKI := "0000000000000000000000000000000000000072"
	localDevice := spinemocks.NewDeviceLocalInterface(t)
	service := &fakeRuntimeService{
		started:     make(chan struct{}),
		localDevice: localDevice,
	}
	remote := spinemocks.NewDeviceRemoteInterface(t)
	entity := spinemocks.NewEntityRemoteInterface(t)
	feature := spinemocks.NewFeatureRemoteInterface(t)
	deviceAddress := spinemodel.AddressDeviceType("d:_n:Vaillant_VR940")
	deviceType := spinemodel.DeviceTypeTypeEnergyManagementSystem
	entityType := spinemodel.EntityTypeTypeCEM
	entityDescription := spinemodel.DescriptionType("special-role entity")
	featureAddress := spinemodel.AddressFeatureType(0)
	featureType := spinemodel.FeatureTypeTypeGeneric
	featureDescription := spinemodel.DescriptionType("special-role feature")

	localDevice.EXPECT().RemoteDeviceForSki(remoteSKI).Return(remote)
	remote.EXPECT().Address().Return(&deviceAddress)
	remote.EXPECT().DeviceType().Return(&deviceType)
	remote.EXPECT().DestinationData().Return(spinemodel.NodeManagementDestinationDataType{})
	remote.EXPECT().FeatureSet().Return(nil)
	remote.EXPECT().Entities().Return([]spineapi.EntityRemoteInterface{entity})
	remote.EXPECT().UseCases().Return(nil)
	entity.EXPECT().Address().Return(&spinemodel.EntityAddressType{
		Device: &deviceAddress,
		Entity: []spinemodel.AddressEntityType{0},
	})
	entity.EXPECT().EntityType().Return(entityType)
	entity.EXPECT().Description().Return(&entityDescription)
	entity.EXPECT().Features().Return([]spineapi.FeatureRemoteInterface{feature})
	feature.EXPECT().Address().Return(&spinemodel.FeatureAddressType{
		Device:  &deviceAddress,
		Entity:  []spinemodel.AddressEntityType{0},
		Feature: &featureAddress,
	})
	feature.EXPECT().Type().Return(featureType)
	feature.EXPECT().Role().Return(spinemodel.RoleTypeSpecial)
	feature.EXPECT().Description().Return(&featureDescription)

	devices, err := runtimeDevicesForRemote(service, remoteSKI)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 ||
		len(devices[0].Entities) != 1 ||
		len(devices[0].Entities[0].Features) != 1 {
		t.Fatalf("runtime devices = %+v", devices)
	}
	if got := devices[0].Entities[0].Features[0].Role; got != "special" {
		t.Fatalf("special SPINE feature role = %q, want special", got)
	}
}

func TestIssue72StaleDiscoveryCannotPopulateReconnectedSession(t *testing.T) {
	remoteSKI := "0000000000000000000000000000000000000072"
	handler, err := newRuntimeServiceHandler(RuntimeConfig{
		Remotes: []RuntimeRemote{{SKI: remoteSKI}},
	}, "0000000000000000000000000000000000000001", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	handler.activateSPINEEvents(&fakeRuntimeService{started: make(chan struct{})})
	defer func() {
		handler.deactivateSPINEEvents()
		handler.waitForSPINEEvents()
	}()

	firstService := eebusmocks.NewServiceInterface(t)
	firstLocal := spinemocks.NewDeviceLocalInterface(t)
	firstService.EXPECT().LocalDevice().Return(firstLocal)
	firstLocal.EXPECT().RemoteDeviceForSki(remoteSKI).Return(nil)
	handler.RemoteSKIConnected(firstService, remoteSKI)

	handler.mu.Lock()
	stale := runtimeSPINERefresh{
		generation:   handler.spineGeneration,
		sessionIndex: handler.observations[remoteSKI].SessionIndex,
	}
	handler.mu.Unlock()

	handler.RemoteSKIDisconnected(nil, remoteSKI)
	secondService := eebusmocks.NewServiceInterface(t)
	secondLocal := spinemocks.NewDeviceLocalInterface(t)
	secondService.EXPECT().LocalDevice().Return(secondLocal)
	secondLocal.EXPECT().RemoteDeviceForSki(remoteSKI).Return(nil)
	handler.RemoteSKIConnected(secondService, remoteSKI)
	handler.updateRemoteFromSPINEEvent(remoteSKI, stale, []runtimeDeviceObservation{{ID: "stale"}})

	graph := handler.reducer.Snapshot()
	if len(graph) != 1 {
		t.Fatalf("runtime graph = %+v", graph)
	}
	if len(graph[0].Devices) != 0 {
		t.Fatalf("stale discovery populated reconnected session: %+v", graph[0].Devices)
	}
}

func issue72RemoteDevice(t *testing.T, ski string) spineapi.DeviceRemoteInterface {
	t.Helper()
	remote := spinemocks.NewDeviceRemoteInterface(t)
	entity := spinemocks.NewEntityRemoteInterface(t)
	feature := spinemocks.NewFeatureRemoteInterface(t)
	deviceAddress := spinemodel.AddressDeviceType("d:_n:Vaillant_VR940")
	deviceType := spinemodel.DeviceTypeTypeEnergyManagementSystem
	entityType := spinemodel.EntityTypeTypeCEM
	entityDescription := spinemodel.DescriptionType("stable entity")
	featureAddress := spinemodel.AddressFeatureType(1)
	featureType := spinemodel.FeatureTypeTypeGeneric
	featureDescription := spinemodel.DescriptionType("stable feature")

	remote.EXPECT().Ski().Return(ski).Maybe()
	remote.EXPECT().Address().Return(&deviceAddress)
	remote.EXPECT().DeviceType().Return(&deviceType)
	remote.EXPECT().DestinationData().Return(spinemodel.NodeManagementDestinationDataType{})
	remote.EXPECT().FeatureSet().Return(nil)
	remote.EXPECT().Entities().Return([]spineapi.EntityRemoteInterface{entity})
	remote.EXPECT().UseCases().Return([]spinemodel.UseCaseInformationDataType{{}})
	entity.EXPECT().Address().Return(&spinemodel.EntityAddressType{
		Device: &deviceAddress,
		Entity: []spinemodel.AddressEntityType{1},
	})
	entity.EXPECT().EntityType().Return(entityType)
	entity.EXPECT().Description().Return(&entityDescription)
	entity.EXPECT().Features().Return([]spineapi.FeatureRemoteInterface{feature})
	feature.EXPECT().Address().Return(&spinemodel.FeatureAddressType{
		Device:  &deviceAddress,
		Entity:  []spinemodel.AddressEntityType{1},
		Feature: &featureAddress,
	})
	feature.EXPECT().Type().Return(featureType)
	feature.EXPECT().Role().Return(spinemodel.RoleTypeClient)
	feature.EXPECT().Description().Return(&featureDescription)
	return remote
}

type issue72EventRecorder struct {
	called chan struct{}
}

func (recorder *issue72EventRecorder) HandleEvent(spineapi.EventPayload) {
	recorder.called <- struct{}{}
}
