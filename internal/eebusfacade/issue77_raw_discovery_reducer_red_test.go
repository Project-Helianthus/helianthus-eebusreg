package eebusfacade

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	eebusmocks "github.com/Project-Helianthus/helianthus-eebus-go/mocks"
	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	spinemocks "github.com/Project-Helianthus/helianthus-spine-go/mocks"
	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
)

func TestIssue77DiscoveryReducerRetainsMDNSAndDetailedSPINEFacts(t *testing.T) {
	handler, updates := issue77RawHandler(t)
	handler.VisibleRemoteServicesUpdated(nil, []shipapi.RemoteService{issue77RemoteService()})
	_ = issue77WaitPayload(t, updates)
	handler.ServiceShipIDUpdate(issue77ReducerRemoteSKI, "vaillant-vr940f-ship-id")
	_ = issue77WaitPayload(t, updates)

	service := eebusmocks.NewServiceInterface(t)
	local := spinemocks.NewDeviceLocalInterface(t)
	remote := issue77DetailedVR940(t)
	service.EXPECT().LocalDevice().Return(local).Twice()
	local.EXPECT().RemoteDeviceForSki(issue77ReducerRemoteSKI).Return(remote)
	handler.RemoteSKIConnected(service, issue77ReducerRemoteSKI)
	_ = issue77WaitPayload(t, updates)
	handler.ServiceShipIDUpdate(issue77ReducerRemoteSKI, "vaillant-vr940f-ship-id")

	raw := issue77DecodeObject(t, issue77WaitPayload(t, updates))
	issue77AssertRawGraph(t, raw)
}

func TestIssue77SparseDiscoveryEventsDoNotEraseRawFacts(t *testing.T) {
	handler, updates := issue77RawHandler(t)
	handler.VisibleRemoteServicesUpdated(nil, []shipapi.RemoteService{issue77RemoteService()})
	_ = issue77WaitPayload(t, updates)
	handler.ServiceShipIDUpdate(issue77ReducerRemoteSKI, "vaillant-vr940f-ship-id")
	_ = issue77WaitPayload(t, updates)

	handler.VisibleRemoteServicesUpdated(nil, []shipapi.RemoteService{{Ski: issue77ReducerRemoteSKI}})
	handler.ServiceShipIDUpdate(issue77ReducerRemoteSKI, "vaillant-vr940f-ship-id")
	raw := issue77DecodeObject(t, issue77LatestPayload(t, updates))
	service := issue77Objects(t, raw, "services")[0]
	for field, want := range map[string]any{
		"ski":        issue77ReducerRemoteSKI,
		"ship_id":    "vaillant-vr940f-ship-id",
		"kind":       "remote",
		"visible":    true,
		"paired":     false,
		"name":       "Vaillant VR940f eeBUS",
		"identifier": "vr940f-lab-service",
		"brand":      "Vaillant",
		"type":       "eeBUS",
		"model":      "VR940f",
	} {
		if got := service[field]; got != want {
			t.Errorf("sparse mDNS merge service.%s = %#v, want %#v", field, got, want)
		}
	}
}

func TestIssue77ConcurrentPartialEventsPreserveCompleteRawService(t *testing.T) {
	handler, updates := issue77RawHandler(t)
	handler.VisibleRemoteServicesUpdated(nil, []shipapi.RemoteService{issue77RemoteService()})
	_ = issue77WaitPayload(t, updates)

	var workers sync.WaitGroup
	for index := 0; index < 32; index++ {
		workers.Add(2)
		go func() {
			defer workers.Done()
			handler.VisibleRemoteServicesUpdated(nil, []shipapi.RemoteService{issue77RemoteService()})
		}()
		go func() {
			defer workers.Done()
			handler.ServiceShipIDUpdate(issue77ReducerRemoteSKI, "vaillant-vr940f-ship-id")
		}()
	}
	workers.Wait()
	handler.ServiceShipIDUpdate(issue77ReducerRemoteSKI, "vaillant-vr940f-ship-id")

	raw := issue77DecodeObject(t, issue77LatestPayload(t, updates))
	service := issue77Objects(t, raw, "services")[0]
	if service["ski"] != issue77ReducerRemoteSKI ||
		service["ship_id"] != "vaillant-vr940f-ship-id" ||
		service["kind"] != "remote" ||
		service["visible"] != true ||
		service["paired"] != false ||
		service["name"] != "Vaillant VR940f eeBUS" ||
		service["identifier"] != "vr940f-lab-service" ||
		service["brand"] != "Vaillant" ||
		service["type"] != "eeBUS" ||
		service["model"] != "VR940f" {
		t.Fatalf("concurrent partial reducer lost raw service facts: %+v", service)
	}
}

func TestIssue77PartialSPINEEventMergePreservesDetailedRawFacts(t *testing.T) {
	description := "VR940f gateway"
	left := []runtimeDeviceObservation{{
		ID: "device-1", SKI: issue77ReducerRemoteSKI, SHIPID: "ship-1",
		Address: "d:_n:Vaillant_VR940", Type: "EnergyManagementSystem",
		Description: &description,
		Metadata:    map[string]string{"network_feature_set": "gateway"},
		Opaque: []runtimeOpaquePayload{{
			Path: "/devices/d:_n:Vaillant_VR940/destination_data", Source: "spine.detailed-discovery",
			Value: map[string]any{"deviceDescription": map[string]any{"label": "VR940f"}},
		}},
		Entities: []runtimeEntityObservation{{
			ID: "entity-1", DeviceAddress: "d:_n:Vaillant_VR940",
			EntityAddress: "[0]", Type: "DeviceInformation",
			Features: []runtimeFeatureObservation{{
				ID: "feature-1", DeviceAddress: "d:_n:Vaillant_VR940",
				EntityAddress: "[0]", FeatureAddress: "[0]:0",
				Type: "DeviceClassification", Role: "server",
			}},
		}},
	}}
	right := []runtimeDeviceObservation{{
		ID: "device-1",
		Entities: []runtimeEntityObservation{{
			ID: "entity-2", DeviceAddress: "d:_n:Vaillant_VR940",
			EntityAddress: "[1]", Type: "HvacController",
		}},
	}}

	merged, err := mergeRuntimeDeviceCollections(left, right)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged) != 1 || merged[0].SKI != issue77ReducerRemoteSKI ||
		merged[0].Address != "d:_n:Vaillant_VR940" ||
		merged[0].Description == nil || *merged[0].Description != description ||
		merged[0].Metadata["network_feature_set"] != "gateway" ||
		len(merged[0].Opaque) != 1 || len(merged[0].Entities) != 2 {
		t.Fatalf("partial SPINE merge lost detailed raw facts: %+v", merged)
	}
}

func TestIssue77ReconnectAndSparseSameIDMergeRetainDetailedFacts(t *testing.T) {
	description := "feature description"
	resolvedRole := "server"
	version := "1.0.0"
	available := true
	subrevision := "release"
	rich := []runtimeDeviceObservation{{
		ID: "device-1", SKI: issue77ReducerRemoteSKI,
		Address: "d:_n:Vaillant_VR940", Type: "EnergyManagementSystem",
		Entities: []runtimeEntityObservation{{
			ID: "entity-1", DeviceAddress: "d:_n:Vaillant_VR940",
			EntityAddress: "[1]", Type: "HvacController",
			Features: []runtimeFeatureObservation{{
				ID: "feature-1", DeviceAddress: "d:_n:Vaillant_VR940",
				EntityAddress: "[1]", FeatureAddress: "[1]:0", Type: "Hvac",
				Role: "server", Description: &description,
			}},
		}},
		UseCaseIDs: []string{"usecase-1"},
		UseCases: []runtimeUseCaseObservation{{
			ID: "usecase-1", ContextAddress: "d:_n:Vaillant_VR940:[1]:0",
			Name: "monitoring", Actor: "heatingZone", ResolvedRole: &resolvedRole,
			Scenarios: []string{"1", "2"}, Version: &version, Availability: &available,
			DocumentSubrevision: &subrevision,
		}},
	}}
	sparse := []runtimeDeviceObservation{{
		ID: "device-1",
		Entities: []runtimeEntityObservation{{
			ID:       "entity-1",
			Features: []runtimeFeatureObservation{{ID: "feature-1"}},
		}},
		UseCaseIDs: []string{"usecase-1"},
		UseCases:   []runtimeUseCaseObservation{{ID: "usecase-1"}},
	}}
	merged, err := mergeRuntimeDeviceCollections(rich, sparse)
	if err != nil {
		t.Fatal(err)
	}
	issue77AssertRichDeviceFacts(t, merged)

	handler, _ := issue77RawHandler(t)
	observation := handler.newRemoteObservation(issue77ReducerRemoteSKI)
	observation.SessionID = "session-before-reconnect"
	observation.SessionState = "connected"
	observation.SessionIndex = 1
	observation.Devices = rich
	if err := handler.reducer.Replace(observation); err != nil {
		t.Fatal(err)
	}
	handler.observations[issue77ReducerRemoteSKI] = observation
	handler.RemoteSKIConnected(nil, issue77ReducerRemoteSKI)
	handler.mu.Lock()
	reconnected := cloneRuntimeGraphObservation(handler.observations[issue77ReducerRemoteSKI])
	handler.mu.Unlock()
	issue77AssertRichDeviceFacts(t, reconnected.Devices)
}

func TestIssue77PendingSPINERefreshMergesRichAndSparseInBothOrders(t *testing.T) {
	description := "retained"
	rich := runtimeSPINERefresh{
		generation: 1, sessionIndex: 7,
		devices: []runtimeDeviceObservation{{
			ID: "device-1", SKI: issue77ReducerRemoteSKI,
			Address: "device-1", Type: "gateway", Description: &description,
		}},
	}
	sparse := runtimeSPINERefresh{
		generation: 1, sessionIndex: 7,
		devices: []runtimeDeviceObservation{{ID: "device-1"}},
	}
	for _, order := range []struct {
		name          string
		first, second runtimeSPINERefresh
	}{
		{name: "rich-then-sparse", first: rich, second: sparse},
		{name: "sparse-then-rich", first: sparse, second: rich},
	} {
		t.Run(order.name, func(t *testing.T) {
			handler, _ := issue77RawHandler(t)
			handler.spineEventsActive = true
			handler.spineGeneration = 1
			handler.spinePending = make(map[string]runtimeSPINERefresh)
			handler.spineWake = make(chan struct{}, 1)
			handler.observations[issue77ReducerRemoteSKI] = runtimeGraphObservation{
				RemoteSKI: issue77ReducerRemoteSKI, SessionState: "connected", SessionIndex: 7,
			}
			handler.enqueueSPINERefresh(issue77ReducerRemoteSKI, order.first)
			handler.enqueueSPINERefresh(issue77ReducerRemoteSKI, order.second)
			got, ok := handler.takeSPINERefresh(issue77ReducerRemoteSKI)
			if !ok || len(got.devices) != 1 || got.devices[0].Description == nil ||
				*got.devices[0].Description != description || got.devices[0].Address != "device-1" ||
				got.devices[0].Type != "gateway" {
				t.Fatalf("pending refresh lost richer facts: ok=%t refresh=%+v", ok, got)
			}
		})
	}
}

func TestIssue77SKIOnlyServiceEmitsThenEnrichesWithoutLosingState(t *testing.T) {
	handler, updates := issue77RawHandler(t)
	handler.VisibleRemoteServicesUpdated(nil, []shipapi.RemoteService{{Ski: issue77ReducerRemoteSKI}})
	sparse := issue77Objects(t, issue77DecodeObject(t, issue77WaitPayload(t, updates)), "services")
	if len(sparse) != 1 || sparse[0]["kind"] != "remote" || sparse[0]["visible"] != true ||
		sparse[0]["paired"] != false {
		t.Fatalf("SKI-only service state = %+v, want remote/visible/unpaired", sparse)
	}
	for _, field := range []string{"name", "identifier", "brand", "type", "model"} {
		if _, present := sparse[0][field]; present {
			t.Errorf("SKI-only service fabricated optional metadata %q", field)
		}
	}

	handler.VisibleRemoteServicesUpdated(nil, []shipapi.RemoteService{issue77RemoteService()})
	_ = issue77WaitPayload(t, updates)
	handler.updateRemote(issue77ReducerRemoteSKI, false, func(observation *runtimeGraphObservation) {
		observation.PairingState = "paired"
		observation.Paired = true
	})
	_ = issue77WaitPayload(t, updates)
	handler.VisibleRemoteServicesUpdated(nil, []shipapi.RemoteService{{Ski: issue77ReducerRemoteSKI}})
	if err := handler.publishCurrent(); err != nil {
		t.Fatal(err)
	}
	enriched := issue77Objects(t, issue77DecodeObject(t, issue77WaitPayload(t, updates)), "services")
	if len(enriched) != 1 || enriched[0]["name"] != "Vaillant VR940f eeBUS" ||
		enriched[0]["identifier"] != "vr940f-lab-service" || enriched[0]["visible"] != true ||
		enriched[0]["paired"] != true {
		t.Fatalf("sparse service update erased enrichment/state: %+v", enriched)
	}
}

func TestIssue77SPINEObservationIDsArePermutationStable(t *testing.T) {
	first, err := runtimeDevicesForRemoteDevice(issue77DetailedVR940WithOrder(t, false), issue77ReducerRemoteSKI)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runtimeDevicesForRemoteDevice(issue77DetailedVR940WithOrder(t, true), issue77ReducerRemoteSKI)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := issue77ObservationIDs(first), issue77ObservationIDs(second); !reflect.DeepEqual(got, want) {
		t.Fatalf("SPINE IDs depend on source slice order:\nfirst=%v\nsecond=%v", got, want)
	}
}

func issue77AssertRichDeviceFacts(t *testing.T, devices []runtimeDeviceObservation) {
	t.Helper()
	if len(devices) != 1 || len(devices[0].Entities) != 1 ||
		len(devices[0].Entities[0].Features) != 1 || len(devices[0].UseCases) != 1 {
		t.Fatalf("merged graph cardinality = %+v", devices)
	}
	feature := devices[0].Entities[0].Features[0]
	useCase := devices[0].UseCases[0]
	if feature.Description == nil || *feature.Description != "feature description" ||
		feature.Role != "server" || useCase.ResolvedRole == nil || *useCase.ResolvedRole != "server" ||
		!reflect.DeepEqual(useCase.Scenarios, []string{"1", "2"}) ||
		useCase.Version == nil || *useCase.Version != "1.0.0" ||
		useCase.Availability == nil || !*useCase.Availability ||
		useCase.DocumentSubrevision == nil || *useCase.DocumentSubrevision != "release" {
		t.Fatalf("sparse merge erased feature/use-case facts: feature=%+v usecase=%+v", feature, useCase)
	}
}

func issue77ObservationIDs(devices []runtimeDeviceObservation) map[string]string {
	result := make(map[string]string)
	for _, device := range devices {
		result["device:"+device.Address] = device.ID
		for _, entity := range device.Entities {
			result["entity:"+entity.EntityAddress] = entity.ID
			for _, feature := range entity.Features {
				result["feature:"+feature.FeatureAddress] = feature.ID
			}
		}
		for _, useCase := range device.UseCases {
			key := strings.Join([]string{useCase.ContextAddress, useCase.Name, useCase.Actor}, "\x00")
			result["usecase:"+key] = useCase.ID
		}
	}
	return result
}

func issue77RawHandler(t *testing.T) (*runtimeServiceHandler, chan []byte) {
	t.Helper()
	handler, err := newRuntimeServiceHandler(
		RuntimeConfig{Remotes: []RuntimeRemote{{SKI: issue77ReducerRemoteSKI}}},
		"1111111111111111111111111111111111111111",
		func() time.Time { return time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC) },
	)
	if err != nil {
		t.Fatal(err)
	}
	updates := make(chan []byte, 256)
	handler.setPublisher(func(payload []byte) {
		updates <- append([]byte(nil), payload...)
	})
	return handler, updates
}

func issue77RemoteService() shipapi.RemoteService {
	return shipapi.RemoteService{
		Name:       "Vaillant VR940f eeBUS",
		Ski:        issue77ReducerRemoteSKI,
		Identifier: "vr940f-lab-service",
		Brand:      "Vaillant",
		Type:       "eeBUS",
		Model:      "VR940f",
	}
}

func issue77DetailedVR940(t *testing.T) spineapi.DeviceRemoteInterface {
	return issue77DetailedVR940WithOrder(t, false)
}

func issue77DetailedVR940WithOrder(t *testing.T, reverse bool) spineapi.DeviceRemoteInterface {
	t.Helper()
	deviceAddress := spinemodel.AddressDeviceType("d:_n:Vaillant_VR940")
	deviceType := spinemodel.DeviceTypeTypeEnergyManagementSystem
	remote := spinemocks.NewDeviceRemoteInterface(t)
	remote.EXPECT().Ski().Return(issue77ReducerRemoteSKI).Maybe()
	remote.EXPECT().Address().Return(&deviceAddress)
	remote.EXPECT().DeviceType().Return(&deviceType).Maybe()
	deviceDescription := spinemodel.DescriptionType("VR940f gateway")
	featureSet := spinemodel.NetworkManagementFeatureSetTypeGateway
	nativeSetup := spinemodel.NetworkManagementNativeSetupType("factory")
	technologyAddress := spinemodel.NetworkManagementTechnologyAddressType("vr940f")
	communications := spinemodel.NetworkManagementCommunicationsTechnologyInformationType("ethernet")
	label := spinemodel.LabelType("VR940f lab gateway")
	remote.EXPECT().FeatureSet().Return(&featureSet)
	remote.EXPECT().DestinationData().Return(spinemodel.NodeManagementDestinationDataType{
		DeviceDescription: &spinemodel.NetworkManagementDeviceDescriptionDataType{
			Description:                         &deviceDescription,
			NetworkFeatureSet:                   &featureSet,
			NativeSetup:                         &nativeSetup,
			TechnologyAddress:                   &technologyAddress,
			CommunicationsTechnologyInformation: &communications,
			Label:                               &label,
		},
	})

	entityTypes := []spinemodel.EntityTypeType{
		spinemodel.EntityTypeTypeDeviceInformation,
		spinemodel.EntityTypeTypeHvacController,
		spinemodel.EntityTypeTypeHeatingZone,
		spinemodel.EntityTypeTypeHeatingZone,
		spinemodel.EntityTypeTypeHeatingCircuit,
		spinemodel.EntityTypeTypeHeatingCircuit,
		spinemodel.EntityTypeTypeDHWCircuit,
		spinemodel.EntityTypeTypeTemperatureSensor,
		spinemodel.EntityTypeTypeHeatSourceUnit,
		spinemodel.EntityTypeTypePump,
		spinemodel.EntityTypeTypeGeneric,
	}
	featureTypes := []spinemodel.FeatureTypeType{
		spinemodel.FeatureTypeTypeDeviceClassification,
		spinemodel.FeatureTypeTypeDeviceDiagnosis,
		spinemodel.FeatureTypeTypeNodeManagement,
		spinemodel.FeatureTypeTypeHvac,
		spinemodel.FeatureTypeTypeSensing,
		spinemodel.FeatureTypeTypeSetpoint,
		spinemodel.FeatureTypeTypeSensing,
		spinemodel.FeatureTypeTypeSetpoint,
		spinemodel.FeatureTypeTypeHvac,
		spinemodel.FeatureTypeTypeSetpoint,
		spinemodel.FeatureTypeTypeHvac,
		spinemodel.FeatureTypeTypeSetpoint,
		spinemodel.FeatureTypeTypeSensing,
		spinemodel.FeatureTypeTypeSetpoint,
		spinemodel.FeatureTypeTypeSensing,
		spinemodel.FeatureTypeTypeMeasurement,
		spinemodel.FeatureTypeTypeMeasurement,
		spinemodel.FeatureTypeTypeOperatingConstraints,
		spinemodel.FeatureTypeTypeActuatorSwitch,
		spinemodel.FeatureTypeTypeGeneric,
	}
	entities := make([]spineapi.EntityRemoteInterface, 0, len(entityTypes))
	featureIndex := 0
	for entityIndex, entityType := range entityTypes {
		entity := spinemocks.NewEntityRemoteInterface(t)
		entityAddress := &spinemodel.EntityAddressType{
			Device: &deviceAddress,
			Entity: []spinemodel.AddressEntityType{spinemodel.AddressEntityType(entityIndex)},
		}
		description := spinemodel.DescriptionType(fmt.Sprintf("VR940 entity %02d", entityIndex))
		entity.EXPECT().Address().Return(entityAddress)
		entity.EXPECT().EntityType().Return(entityType).Maybe()
		entity.EXPECT().Description().Return(&description).Maybe()

		count := 2
		if entityIndex >= 9 {
			count = 1
		}
		features := make([]spineapi.FeatureRemoteInterface, 0, count)
		for localIndex := 0; localIndex < count; localIndex++ {
			feature := spinemocks.NewFeatureRemoteInterface(t)
			addressValue := spinemodel.AddressFeatureType(localIndex)
			address := &spinemodel.FeatureAddressType{
				Device:  &deviceAddress,
				Entity:  []spinemodel.AddressEntityType{spinemodel.AddressEntityType(entityIndex)},
				Feature: &addressValue,
			}
			role := spinemodel.RoleTypeServer
			if featureIndex%2 == 1 {
				role = spinemodel.RoleTypeClient
			}
			featureDescription := spinemodel.DescriptionType(fmt.Sprintf("VR940 feature %02d", featureIndex))
			feature.EXPECT().Address().Return(address)
			feature.EXPECT().Type().Return(featureTypes[featureIndex]).Maybe()
			feature.EXPECT().Role().Return(role)
			feature.EXPECT().Description().Return(&featureDescription).Maybe()
			features = append(features, feature)
			featureIndex++
		}
		entity.EXPECT().Features().Return(features)
		entities = append(entities, entity)
	}
	useCases := issue77UseCases(deviceAddress)
	if reverse {
		slices.Reverse(entities)
		slices.Reverse(useCases)
	}
	remote.EXPECT().Entities().Return(entities)
	remote.EXPECT().UseCases().Return(useCases)
	return remote
}

func issue77UseCases(deviceAddress spinemodel.AddressDeviceType) []spinemodel.UseCaseInformationDataType {
	names := []spinemodel.UseCaseNameType{
		spinemodel.UseCaseNameTypeMonitoringOfRoomTemperature,
		spinemodel.UseCaseNameTypeConfigurationOfRoomHeatingTemperature,
		spinemodel.UseCaseNameTypeMonitoringOfRoomHeatingSystemFunction,
		spinemodel.UseCaseNameTypeConfigurationOfRoomHeatingSystemFunction,
		spinemodel.UseCaseNameTypeMonitoringOfPowerConsumption,
		spinemodel.UseCaseNameTypeConfigurationOfRoomHeatingTemperature,
		spinemodel.UseCaseNameTypeMonitoringOfDhwTemperature,
		spinemodel.UseCaseNameTypeConfigurationOfDhwTemperature,
		spinemodel.UseCaseNameTypeMonitoringOfOutdoorTemperature,
		spinemodel.UseCaseNameTypeMonitoringOfPowerConsumption,
	}
	actors := []spinemodel.UseCaseActorType{
		spinemodel.UseCaseActorTypeHeatingZone,
		spinemodel.UseCaseActorTypeHeatingZone,
		spinemodel.UseCaseActorTypeHeatingZone,
		spinemodel.UseCaseActorTypeHeatingZone,
		spinemodel.UseCaseActorTypeHeatingCircuit,
		spinemodel.UseCaseActorTypeHeatingCircuit,
		spinemodel.UseCaseActorTypeDHWCircuit,
		spinemodel.UseCaseActorTypeDHWCircuit,
		spinemodel.UseCaseActorTypeOutdoorTemperatureSensor,
		spinemodel.UseCaseActorTypeHeatPump,
	}
	result := make([]spinemodel.UseCaseInformationDataType, len(names))
	for index := range names {
		entity := spinemodel.AddressEntityType(index % 9)
		feature := spinemodel.AddressFeatureType(index % 2)
		address := &spinemodel.FeatureAddressType{
			Device:  &deviceAddress,
			Entity:  []spinemodel.AddressEntityType{entity},
			Feature: &feature,
		}
		version := spinemodel.SpecificationVersionType("1.0.0")
		available := true
		subrevision := spinemodel.UseCaseDocumentSubRevisionRelease
		name := names[index]
		actor := actors[index]
		result[index] = spinemodel.UseCaseInformationDataType{
			Address: address,
			Actor:   &actor,
			UseCaseSupport: []spinemodel.UseCaseSupportType{{
				UseCaseName:                &name,
				UseCaseVersion:             &version,
				UseCaseAvailable:           &available,
				ScenarioSupport:            []spinemodel.UseCaseScenarioSupportType{1, 2},
				UseCaseDocumentSubRevision: &subrevision,
			}},
		}
	}
	return result
}

func issue77AssertRawGraph(t *testing.T, raw map[string]any) {
	t.Helper()
	services := issue77Objects(t, raw, "services")
	devices := issue77Objects(t, raw, "devices")
	entities := issue77Objects(t, raw, "entities")
	features := issue77Objects(t, raw, "features")
	usecases := issue77Objects(t, raw, "usecases")
	if len(services) != 1 || len(devices) != 1 || len(entities) != 11 || len(features) != 20 || len(usecases) != 10 {
		t.Fatalf(
			"raw reducer counts services/devices/entities/features/usecases = %d/%d/%d/%d/%d, want 1/1/11/20/10",
			len(services), len(devices), len(entities), len(features), len(usecases),
		)
	}
	service := services[0]
	for field, want := range map[string]any{
		"ski":        issue77ReducerRemoteSKI,
		"ship_id":    "vaillant-vr940f-ship-id",
		"kind":       "remote",
		"visible":    true,
		"paired":     false,
		"name":       "Vaillant VR940f eeBUS",
		"identifier": "vr940f-lab-service",
		"brand":      "Vaillant",
		"type":       "eeBUS",
		"model":      "VR940f",
	} {
		if service[field] != want {
			t.Errorf("service.%s = %#v, want %#v", field, service[field], want)
		}
	}
	if devices[0]["ski"] != issue77ReducerRemoteSKI ||
		devices[0]["address"] != "d:_n:Vaillant_VR940" ||
		devices[0]["type"] != "EnergyManagementSystem" ||
		devices[0]["description"] != "VR940f gateway" {
		t.Errorf("raw device facts = %+v", devices[0])
	}
	metadata, ok := devices[0]["metadata"].(map[string]any)
	if !ok || metadata["network_feature_set"] != "gateway" ||
		metadata["native_setup"] != "factory" ||
		metadata["technology_address"] != "vr940f" ||
		metadata["communications_technology_information"] != "ethernet" ||
		metadata["label"] != "VR940f lab gateway" {
		t.Errorf("raw detailed-discovery metadata = %+v", devices[0]["metadata"])
	}
	if opaque, ok := devices[0]["opaque"].([]any); !ok || len(opaque) != 1 {
		t.Errorf("raw detailed-discovery opaque value = %+v", devices[0]["opaque"])
	}
	for index, entity := range entities {
		if entity["device_address"] == "" || entity["entity_address"] == "" ||
			entity["type"] == "" || entity["description"] == nil {
			t.Errorf("entity %d lost detailed-discovery facts: %+v", index, entity)
		}
	}
	for index, feature := range features {
		if feature["device_address"] == "" || feature["entity_address"] == "" ||
			feature["feature_address"] == "" || feature["type"] == "" ||
			feature["role"] == "" || feature["description"] == nil {
			t.Errorf("feature %d lost detailed-discovery facts: %+v", index, feature)
		}
	}
	for index, usecase := range usecases {
		if usecase["context_address"] == "" || usecase["name"] == "" || usecase["actor"] == "" ||
			usecase["resolved_role"] == nil || usecase["scenarios"] == nil ||
			usecase["version"] == nil || usecase["availability"] == nil ||
			usecase["document_subrevision"] == nil {
			t.Errorf("usecase %d lost named claim facts: %+v", index, usecase)
		}
	}
}

func issue77Objects(t *testing.T, object map[string]any, field string) []map[string]any {
	t.Helper()
	values, ok := object[field].([]any)
	if !ok {
		t.Fatalf("%s = %T, want array", field, object[field])
	}
	result := make([]map[string]any, len(values))
	for index, value := range values {
		item, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("%s[%d] = %T, want object", field, index, value)
		}
		result[index] = item
	}
	return result
}

func issue77DecodeObject(t *testing.T, payload []byte) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func issue77WaitPayload(t *testing.T, updates <-chan []byte) []byte {
	t.Helper()
	select {
	case payload := <-updates:
		return payload
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for issue 77 runtime snapshot")
		return nil
	}
}

func issue77LatestPayload(t *testing.T, updates <-chan []byte) []byte {
	t.Helper()
	latest := issue77WaitPayload(t, updates)
	for {
		select {
		case latest = <-updates:
		default:
			return latest
		}
	}
}

const issue77ReducerRemoteSKI = "2222222222222222222222222222222222222222"
