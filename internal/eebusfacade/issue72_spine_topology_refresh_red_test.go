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
)

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
		StateRoot:  "/tmp/helianthus-eebus-issue72",
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
	remoteService.EXPECT().LocalDevice().Return(localDevice)
	localDevice.EXPECT().RemoteDeviceForSki(remoteSKI).Return(nil).Once()

	clock.Advance(time.Second)
	reader.RemoteSKIConnected(remoteService, remoteSKI)
	connected := decodeRuntimePayload(t, waitRuntimePayload(t, updates))
	if len(connected.Sessions) != 1 || connected.Sessions[0].State != "connected" {
		t.Fatalf("connected sessions = %+v", connected.Sessions)
	}
	if len(connected.Topology.Devices) != 0 {
		t.Fatalf("pre-discovery topology = %+v, want empty", connected.Topology)
	}

	remoteService.EXPECT().LocalDevice().Return(localDevice)
	localDevice.EXPECT().RemoteDeviceForSki(remoteSKI).Return(remoteDevice)
	clock.Advance(time.Second)
	eventHandler.HandleEvent(spineapi.EventPayload{
		Ski:        remoteSKI,
		EventType:  spineapi.EventTypeDeviceChange,
		ChangeType: spineapi.ElementChangeAdd,
		Device:     remoteDevice,
	})
	discovered := decodeRuntimePayload(t, waitRuntimePayload(t, updates))
	if len(discovered.Topology.Devices) != 1 ||
		len(discovered.Topology.Devices[0].Entities) != 1 ||
		len(discovered.Topology.Devices[0].Entities[0].Features) != 1 {
		t.Fatalf("post-discovery topology = %+v", discovered.Topology)
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

func issue72RemoteDevice(t *testing.T, ski string) spineapi.DeviceRemoteInterface {
	t.Helper()
	remote := spinemocks.NewDeviceRemoteInterface(t)
	entity := spinemocks.NewEntityRemoteInterface(t)
	feature := spinemocks.NewFeatureRemoteInterface(t)
	deviceAddress := spinemodel.AddressDeviceType("d:_n:Vaillant_VR940")
	featureAddress := spinemodel.AddressFeatureType(1)

	remote.EXPECT().Ski().Return(ski).Maybe()
	remote.EXPECT().Address().Return(&deviceAddress)
	remote.EXPECT().Entities().Return([]spineapi.EntityRemoteInterface{entity})
	remote.EXPECT().UseCases().Return([]spinemodel.UseCaseInformationDataType{{}})
	entity.EXPECT().Address().Return(&spinemodel.EntityAddressType{
		Device: &deviceAddress,
		Entity: []spinemodel.AddressEntityType{1},
	})
	entity.EXPECT().Features().Return([]spineapi.FeatureRemoteInterface{feature})
	feature.EXPECT().Address().Return(&spinemodel.FeatureAddressType{
		Device:  &deviceAddress,
		Entity:  []spinemodel.AddressEntityType{1},
		Feature: &featureAddress,
	})
	feature.EXPECT().Role().Return(spinemodel.RoleTypeClient)
	return remote
}
