package eebusfacade

import (
	"sync/atomic"
	"testing"
	"time"

	eebusmocks "github.com/Project-Helianthus/helianthus-eebus-go/mocks"
	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	spinemocks "github.com/Project-Helianthus/helianthus-spine-go/mocks"
	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
)

func TestIssue99TopologyEventBeforeConnectedConvergesIntoNewSession(t *testing.T) {
	fixture := newIssue99TopologyFixture(t)
	fixture.detailed.Store(true)

	fixture.handler.HandleEvent(fixture.event())
	fixture.detailed.Store(false)
	fixture.connect()
	fixture.waitForDetailedTopology(1)
}

func TestIssue99ConcurrentEventCaptureAndConnectedPublicationConverge(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	fixture := newIssue99TopologyFixture(t)
	fixture.entities = func() []spineapi.EntityRemoteInterface {
		if fixture.entityCalls.Add(1) == 1 {
			close(entered)
			<-release
			return fixture.detailedEntities
		}
		return nil
	}
	fixture.useCases = func() []spinemodel.UseCaseInformationDataType {
		if fixture.useCaseCalls.Add(1) == 2 {
			return fixture.detailedUseCases
		}
		return nil
	}

	eventDone := make(chan struct{})
	go func() {
		defer close(eventDone)
		fixture.handler.HandleEvent(fixture.event())
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("topology event was discarded before capturing the remote graph")
	}

	fixture.connect()
	close(release)
	<-eventDone
	fixture.waitForDetailedTopology(1)
}

func TestIssue99DisconnectRetiresUnconsumedPreSessionTopology(t *testing.T) {
	fixture := newIssue99TopologyFixture(t)
	fixture.detailed.Store(true)
	fixture.handler.HandleEvent(fixture.event())

	fixture.handler.RemoteSKIDisconnected(nil, fixture.remoteSKI)
	fixture.detailed.Store(false)
	fixture.connect()
	fixture.waitForDetailedTopology(0)
}

func TestIssue99ConcurrentDisconnectInvalidatesInFlightPreSessionCapture(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	fixture := newIssue99TopologyFixture(t)
	fixture.entities = func() []spineapi.EntityRemoteInterface {
		close(entered)
		<-release
		return fixture.detailedEntities
	}
	fixture.useCases = func() []spinemodel.UseCaseInformationDataType {
		return fixture.detailedUseCases
	}

	eventDone := make(chan struct{})
	go func() {
		defer close(eventDone)
		fixture.handler.HandleEvent(fixture.event())
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("in-flight topology capture did not enter")
	}
	fixture.handler.RemoteSKIDisconnected(nil, fixture.remoteSKI)
	close(release)
	<-eventDone

	fixture.entities = func() []spineapi.EntityRemoteInterface { return nil }
	fixture.useCases = func() []spinemodel.UseCaseInformationDataType { return nil }
	fixture.connect()
	fixture.waitForDetailedTopology(0)
}

func TestIssue99RuntimeGenerationRetiresUnconsumedPreSessionTopology(t *testing.T) {
	fixture := newIssue99TopologyFixture(t)
	fixture.detailed.Store(true)
	fixture.handler.HandleEvent(fixture.event())

	fixture.handler.deactivateSPINEEvents()
	fixture.handler.waitForSPINEEvents()
	fixture.handler.activateSPINEEvents(fixture.service)
	fixture.detailed.Store(false)
	fixture.connect()
	fixture.waitForDetailedTopology(0)
}

func TestIssue99ReplacedRemoteCannotPopulateConnectedSession(t *testing.T) {
	fixture := newIssue99TopologyFixture(t)
	fixture.detailed.Store(true)
	fixture.handler.HandleEvent(fixture.event())

	replacement := fixture.newRemote(false)
	fixture.remote = replacement
	fixture.detailed.Store(false)
	fixture.connect()
	fixture.waitForDetailedTopology(0)
}

type issue99TopologyFixture struct {
	t                *testing.T
	remoteSKI        string
	handler          *runtimeServiceHandler
	service          *fakeRuntimeService
	localDevice      *spinemocks.DeviceLocalInterface
	remote           spineapi.DeviceRemoteInterface
	detailed         atomic.Bool
	entityCalls      atomic.Uint64
	useCaseCalls     atomic.Uint64
	entities         func() []spineapi.EntityRemoteInterface
	useCases         func() []spinemodel.UseCaseInformationDataType
	detailedEntities []spineapi.EntityRemoteInterface
	detailedUseCases []spinemodel.UseCaseInformationDataType
}

func newIssue99TopologyFixture(t *testing.T) *issue99TopologyFixture {
	t.Helper()
	fixture := &issue99TopologyFixture{
		t:         t,
		remoteSKI: "9999999999999999999999999999999999999999",
	}
	handler, err := newRuntimeServiceHandler(RuntimeConfig{
		Remotes: []RuntimeRemote{{SKI: fixture.remoteSKI}},
	}, "1111111111111111111111111111111111111111", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	fixture.handler = handler
	fixture.localDevice = spinemocks.NewDeviceLocalInterface(t)
	fixture.service = &fakeRuntimeService{
		started:     make(chan struct{}),
		localDevice: fixture.localDevice,
	}
	fixture.remote = fixture.newRemote(true)
	fixture.localDevice.EXPECT().RemoteDeviceForSki(fixture.remoteSKI).
		RunAndReturn(func(string) spineapi.DeviceRemoteInterface {
			return fixture.remote
		}).
		Maybe()
	fixture.handler.activateSPINEEvents(fixture.service)
	t.Cleanup(func() {
		fixture.handler.deactivateSPINEEvents()
		fixture.handler.waitForSPINEEvents()
	})
	return fixture
}

func (fixture *issue99TopologyFixture) newRemote(withDetails bool) spineapi.DeviceRemoteInterface {
	fixture.t.Helper()
	remote := spinemocks.NewDeviceRemoteInterface(fixture.t)
	deviceAddress := spinemodel.AddressDeviceType("d:_n:issue99")
	deviceType := spinemodel.DeviceTypeTypeEnergyManagementSystem
	featureSet := spinemodel.NetworkManagementFeatureSetTypeSmart
	deviceDescription := spinemodel.DescriptionType("issue99 remote")

	entity := spinemocks.NewEntityRemoteInterface(fixture.t)
	entityAddress := &spinemodel.EntityAddressType{
		Device: &deviceAddress,
		Entity: []spinemodel.AddressEntityType{1},
	}
	entityType := spinemodel.EntityTypeTypeHvacController
	entityDescription := spinemodel.DescriptionType("issue99 entity")

	feature := spinemocks.NewFeatureRemoteInterface(fixture.t)
	featureAddressValue := spinemodel.AddressFeatureType(1)
	featureAddress := &spinemodel.FeatureAddressType{
		Device:  &deviceAddress,
		Entity:  []spinemodel.AddressEntityType{1},
		Feature: &featureAddressValue,
	}
	featureType := spinemodel.FeatureTypeTypeSetpoint
	featureDescription := spinemodel.DescriptionType("issue99 feature")
	feature.EXPECT().Address().Return(featureAddress).Maybe()
	feature.EXPECT().Type().Return(featureType).Maybe()
	feature.EXPECT().Role().Return(spinemodel.RoleTypeServer).Maybe()
	feature.EXPECT().Description().Return(&featureDescription).Maybe()

	entity.EXPECT().Address().Return(entityAddress).Maybe()
	entity.EXPECT().EntityType().Return(entityType).Maybe()
	entity.EXPECT().Description().Return(&entityDescription).Maybe()
	entity.EXPECT().Features().Return([]spineapi.FeatureRemoteInterface{feature}).Maybe()

	useCaseName := spinemodel.UseCaseNameTypeConfigurationOfRoomHeatingTemperature
	useCaseActor := spinemodel.UseCaseActorTypeHeatingZone
	useCaseVersion := spinemodel.SpecificationVersionType("1.0.0")
	useCaseAvailable := true
	useCaseSubrevision := spinemodel.UseCaseDocumentSubRevisionRelease
	useCases := []spinemodel.UseCaseInformationDataType{{
		Address: featureAddress,
		Actor:   &useCaseActor,
		UseCaseSupport: []spinemodel.UseCaseSupportType{{
			UseCaseName:                &useCaseName,
			UseCaseVersion:             &useCaseVersion,
			UseCaseAvailable:           &useCaseAvailable,
			ScenarioSupport:            []spinemodel.UseCaseScenarioSupportType{1},
			UseCaseDocumentSubRevision: &useCaseSubrevision,
		}},
	}}

	remote.EXPECT().Ski().Return(fixture.remoteSKI).Maybe()
	remote.EXPECT().Address().Return(&deviceAddress).Maybe()
	remote.EXPECT().DeviceType().Return(&deviceType).Maybe()
	remote.EXPECT().FeatureSet().Return(&featureSet).Maybe()
	remote.EXPECT().DestinationData().Return(spinemodel.NodeManagementDestinationDataType{
		DeviceDescription: &spinemodel.NetworkManagementDeviceDescriptionDataType{
			Description:       &deviceDescription,
			NetworkFeatureSet: &featureSet,
		},
	}).Maybe()
	if withDetails {
		fixture.detailedEntities = []spineapi.EntityRemoteInterface{entity}
		fixture.detailedUseCases = useCases
		fixture.entities = func() []spineapi.EntityRemoteInterface {
			if fixture.detailed.Load() {
				return fixture.detailedEntities
			}
			return nil
		}
		fixture.useCases = func() []spinemodel.UseCaseInformationDataType {
			if fixture.detailed.Load() {
				return fixture.detailedUseCases
			}
			return nil
		}
	}
	remote.EXPECT().Entities().RunAndReturn(func() []spineapi.EntityRemoteInterface {
		if fixture.entities == nil {
			return nil
		}
		return fixture.entities()
	}).Maybe()
	remote.EXPECT().UseCases().RunAndReturn(func() []spinemodel.UseCaseInformationDataType {
		if fixture.useCases == nil {
			return nil
		}
		return fixture.useCases()
	}).Maybe()
	return remote
}

func (fixture *issue99TopologyFixture) event() spineapi.EventPayload {
	return spineapi.EventPayload{
		Ski:        fixture.remoteSKI,
		EventType:  spineapi.EventTypeDeviceChange,
		ChangeType: spineapi.ElementChangeAdd,
		Device:     fixture.remote,
	}
}

func (fixture *issue99TopologyFixture) connect() {
	fixture.t.Helper()
	service := eebusmocks.NewServiceInterface(fixture.t)
	service.EXPECT().LocalDevice().Return(fixture.localDevice).Maybe()
	fixture.handler.RemoteSKIConnected(service, fixture.remoteSKI)
}

func (fixture *issue99TopologyFixture) waitForDetailedTopology(want int) {
	fixture.t.Helper()
	if want == 0 {
		time.Sleep(50 * time.Millisecond)
		fixture.assertDetailedTopology(0)
		return
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		graph := fixture.handler.reducer.Snapshot()
		if len(graph) == 1 && len(graph[0].Devices) == 1 &&
			len(graph[0].Devices[0].Entities) == want &&
			len(graph[0].Devices[0].UseCases) == want {
			fixture.assertDetailedTopology(want)
			return
		}
		time.Sleep(time.Millisecond)
	}
	fixture.assertDetailedTopology(want)
}

func (fixture *issue99TopologyFixture) assertDetailedTopology(want int) {
	fixture.t.Helper()
	graph := fixture.handler.reducer.Snapshot()
	if len(graph) != 1 || len(graph[0].Devices) != 1 {
		fixture.t.Fatalf("runtime graph = %+v, want one connected device", graph)
	}
	gotEntities := len(graph[0].Devices[0].Entities)
	gotUseCases := len(graph[0].Devices[0].UseCases)
	gotFeatures := 0
	for _, entity := range graph[0].Devices[0].Entities {
		gotFeatures += len(entity.Features)
	}
	if gotEntities != want || gotFeatures != want || gotUseCases != want {
		fixture.t.Fatalf(
			"detailed topology entities/features/usecases = %d/%d/%d, want %d/%d/%d",
			gotEntities, gotFeatures, gotUseCases, want, want, want,
		)
	}
}

var _ spineapi.EventHandlerInterface = (*runtimeServiceHandler)(nil)
