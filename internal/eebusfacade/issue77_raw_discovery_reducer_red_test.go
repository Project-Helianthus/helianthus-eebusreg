package eebusfacade

import (
	"encoding/json"
	"fmt"
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
	t.Helper()
	deviceAddress := spinemodel.AddressDeviceType("d:_n:Vaillant_VR940")
	deviceType := spinemodel.DeviceTypeTypeEnergyManagementSystem
	remote := spinemocks.NewDeviceRemoteInterface(t)
	remote.EXPECT().Ski().Return(issue77ReducerRemoteSKI).Maybe()
	remote.EXPECT().Address().Return(&deviceAddress)
	remote.EXPECT().DeviceType().Return(&deviceType).Maybe()

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
	remote.EXPECT().Entities().Return(entities)
	remote.EXPECT().UseCases().Return(issue77UseCases(deviceAddress))
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
		devices[0]["type"] != "EnergyManagementSystem" {
		t.Errorf("raw device facts = %+v", devices[0])
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
