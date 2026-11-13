package eebusfacade

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	spinemocks "github.com/Project-Helianthus/helianthus-spine-go/mocks"
	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
)

const (
	nativeContainmentRemoteSKI = "5555555555555555555555555555555555555555"
	nativeContainmentLocalSKI  = "6666666666666666666666666666666666666666"
	nativeContainmentShipID    = "synthetic-ship-0001"
	nativeContainmentDevice    = "d:_n:synthetic_controller"
)

var nativeContainmentTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func nativeContainmentFeature(t *testing.T, device spinemodel.AddressDeviceType, entity []spinemodel.AddressEntityType, index uint, featureType spinemodel.FeatureTypeType, role spinemodel.RoleType) spineapi.FeatureRemoteInterface {
	t.Helper()
	feature := spinemocks.NewFeatureRemoteInterface(t)
	value := spinemodel.AddressFeatureType(index)
	address := &spinemodel.FeatureAddressType{Device: &device, Entity: entity, Feature: &value}
	feature.EXPECT().Address().Return(address).Maybe()
	feature.EXPECT().Type().Return(featureType).Maybe()
	feature.EXPECT().Role().Return(role).Maybe()
	feature.EXPECT().Description().Return(nil).Maybe()
	return feature
}

func nativeContainmentEntity(t *testing.T, device spinemodel.AddressDeviceType, address []spinemodel.AddressEntityType, entityType spinemodel.EntityTypeType, features ...spineapi.FeatureRemoteInterface) spineapi.EntityRemoteInterface {
	t.Helper()
	entity := spinemocks.NewEntityRemoteInterface(t)
	entity.EXPECT().Address().Return(&spinemodel.EntityAddressType{Device: &device, Entity: address}).Maybe()
	entity.EXPECT().EntityType().Return(entityType).Maybe()
	entity.EXPECT().Description().Return(nil).Maybe()
	entity.EXPECT().Features().Return(features).Maybe()
	return entity
}

func nativeContainmentUseCase(device spinemodel.AddressDeviceType, entity []spinemodel.AddressEntityType, feature *uint, actor *spinemodel.UseCaseActorType, name spinemodel.UseCaseNameType) spinemodel.UseCaseInformationDataType {
	address := &spinemodel.FeatureAddressType{Device: &device, Entity: entity}
	if feature != nil {
		value := spinemodel.AddressFeatureType(*feature)
		address.Feature = &value
	}
	version := spinemodel.SpecificationVersionType("1.0.0")
	available := true
	subrevision := spinemodel.UseCaseDocumentSubRevisionRelease
	return spinemodel.UseCaseInformationDataType{
		Address: address,
		Actor:   actor,
		UseCaseSupport: []spinemodel.UseCaseSupportType{{
			UseCaseName: &name, UseCaseVersion: &version, UseCaseAvailable: &available,
			ScenarioSupport: []spinemodel.UseCaseScenarioSupportType{1, 2}, UseCaseDocumentSubRevision: &subrevision,
		}},
	}
}

func nativeContainmentActor(value spinemodel.UseCaseActorType) *spinemodel.UseCaseActorType {
	return &value
}

func nativeContainmentRemote(t *testing.T) spineapi.DeviceRemoteInterface {
	t.Helper()
	device := spinemodel.AddressDeviceType(nativeContainmentDevice)
	deviceType := spinemodel.DeviceTypeTypeEnergyManagementSystem
	remote := spinemocks.NewDeviceRemoteInterface(t)
	remote.EXPECT().Ski().Return(nativeContainmentRemoteSKI).Maybe()
	remote.EXPECT().Address().Return(&device)
	remote.EXPECT().DeviceType().Return(&deviceType)
	remote.EXPECT().DestinationData().Return(spinemodel.NodeManagementDestinationDataType{})
	remote.EXPECT().FeatureSet().Return(nil).Maybe()
	one := []spinemodel.AddressEntityType{1}
	nested := []spinemodel.AddressEntityType{1, 1}
	two := []spinemodel.AddressEntityType{2}
	twoNested := []spinemodel.AddressEntityType{2, 1}
	remote.EXPECT().Entities().Return([]spineapi.EntityRemoteInterface{
		nativeContainmentEntity(t, device, one, spinemodel.EntityTypeTypeHeatingCircuit,
			nativeContainmentFeature(t, device, one, 0, spinemodel.FeatureTypeTypeHvac, spinemodel.RoleTypeServer),
			nativeContainmentFeature(t, device, one, 1, "", spinemodel.RoleTypeClient),
			nativeContainmentFeature(t, device, one, 2, spinemodel.FeatureTypeTypeMeasurement, ""),
		),
		nativeContainmentEntity(t, device, nested, spinemodel.EntityTypeTypeHeatingZone,
			nativeContainmentFeature(t, device, nested, 0, spinemodel.FeatureTypeTypeSetpoint, spinemodel.RoleTypeServer),
		),
		nativeContainmentEntity(t, device, two, "",
			nativeContainmentFeature(t, device, two, 0, spinemodel.FeatureTypeTypeSensing, spinemodel.RoleTypeServer),
		),
		nativeContainmentEntity(t, device, twoNested, spinemodel.EntityTypeTypeTemperatureSensor),
	})
	featureZero := uint(0)
	remote.EXPECT().UseCases().Return([]spinemodel.UseCaseInformationDataType{
		nativeContainmentUseCase(device, nil, nil, nativeContainmentActor(spinemodel.UseCaseActorTypeHeatPump), spinemodel.UseCaseNameTypeMonitoringOfPowerConsumption),
		nativeContainmentUseCase(device, one, nil, nativeContainmentActor(spinemodel.UseCaseActorTypeHeatingCircuit), spinemodel.UseCaseNameTypeConfigurationOfRoomHeatingTemperature),
		nativeContainmentUseCase(device, one, &featureZero, nativeContainmentActor(spinemodel.UseCaseActorTypeHeatingCircuit), spinemodel.UseCaseNameTypeMonitoringOfRoomTemperature),
		nativeContainmentUseCase(device, two, nil, nativeContainmentActor(spinemodel.UseCaseActorTypeHeatingCircuit), spinemodel.UseCaseNameTypeMonitoringOfDhwTemperature),
		nativeContainmentUseCase(device, nested, nil, nil, spinemodel.UseCaseNameTypeMonitoringOfOutdoorTemperature),
	})
	return remote
}

func nativeContainmentGraph(devices []runtimeDeviceObservation) []runtimeGraphObservation {
	return []runtimeGraphObservation{{
		RuntimeID: "runtime:" + nativeContainmentLocalSKI, LocalSKI: nativeContainmentLocalSKI, RemoteSKI: nativeContainmentRemoteSKI,
		SessionID: "session:" + nativeContainmentShipID + ":1", SessionState: "connected", PairingState: "paired",
		Visible: true, Paired: true, Since: nativeContainmentTime, ShipID: nativeContainmentShipID, SessionIndex: 1,
		Devices: devices,
	}}
}

func nativeContainmentDecode(t *testing.T, payload []byte) map[string]any {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func nativeContainmentMarshal(t *testing.T, graph []runtimeGraphObservation) map[string]any {
	t.Helper()
	payload, err := marshalNativeRuntimeSnapshotV2WithIdentity("runtime:"+nativeContainmentLocalSKI, nativeContainmentLocalSKI, graph, nativeContainmentTime)
	if err != nil {
		t.Fatal(err)
	}
	return nativeContainmentDecode(t, payload)
}

func TestNativeRuntimeContainmentMatchesVector(t *testing.T) {
	devices, err := runtimeDevicesForRemoteDevice(nativeContainmentRemote(t), nativeContainmentRemoteSKI)
	if err != nil {
		t.Fatal(err)
	}
	devices, err = exactRuntimeDeviceCollection(devices)
	if err != nil {
		t.Fatal(err)
	}
	got := nativeContainmentMarshal(t, nativeContainmentGraph(devices))
	vector, err := os.ReadFile(filepath.Join("..", "..", "testdata", "native_containment", "snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := nativeContainmentDecode(t, vector)
	if !reflect.DeepEqual(got, want) {
		gotJSON, _ := json.MarshalIndent(got, "", "  ")
		wantJSON, _ := json.MarshalIndent(want, "", "  ")
		t.Fatalf("facade payload differs from the vector:\n got %s\nwant %s", gotJSON, wantJSON)
	}
}

func nativeContainmentObservedDevice(address, ski string) runtimeDeviceObservation {
	entity := address + ":[1]:"
	return runtimeDeviceObservation{
		ID: "device:" + ski + ":" + address, SKI: ski, Address: address, Type: "EnergyManagementSystem",
		Entities: []runtimeEntityObservation{{
			ID: "entity:" + entity, DeviceAddress: address, EntityAddress: entity, Type: "HeatingCircuit",
			Features: []runtimeFeatureObservation{{ID: "feature:" + entity + "0", DeviceAddress: address, EntityAddress: entity, FeatureAddress: entity + "0", Type: "HVAC", Role: "server"}},
		}},
		UseCases: []runtimeUseCaseObservation{{ID: "usecase:" + entity, ContextAddress: entity, DeviceAddress: address, EntityAddress: entity, Name: "monitoringOfRoomTemperature", Actor: "HeatingCircuit", Scenarios: []string{}}},
	}
}

func TestNativeRuntimeContainmentWithholdsAmbiguousDevices(t *testing.T) {
	const other = "7777777777777777777777777777777777777777"
	graph := []runtimeGraphObservation{
		{RemoteSKI: nativeContainmentRemoteSKI, Visible: true, Devices: []runtimeDeviceObservation{nativeContainmentObservedDevice(nativeContainmentDevice, nativeContainmentRemoteSKI)}},
		{RemoteSKI: other, Visible: true, Devices: []runtimeDeviceObservation{nativeContainmentObservedDevice(nativeContainmentDevice, other), nativeContainmentObservedDevice("d:_n:synthetic_peer", other)}},
	}
	document := nativeContainmentMarshal(t, graph)
	devices, _ := document["devices"].([]any)
	if len(devices) != 1 || devices[0].(map[string]any)["address"] != "d:_n:synthetic_peer" {
		t.Fatalf("devices = %#v, want only the unambiguous device", document["devices"])
	}
	for _, key := range []string{"entities", "features", "use_cases"} {
		items, _ := document[key].([]any)
		if len(items) != 1 || items[0].(map[string]any)["device_address"] != "d:_n:synthetic_peer" {
			t.Fatalf("%s = %#v, want only the unambiguous device's node", key, document[key])
		}
	}
	want := []any{
		map[string]any{"kind": "device", "reason": "ambiguous_address", "device_address": nativeContainmentDevice},
		map[string]any{"kind": "device", "reason": "ambiguous_address", "device_address": nativeContainmentDevice},
	}
	if !reflect.DeepEqual(document["absences"], want) || document["absences_omitted"] != float64(0) {
		t.Fatalf("absences = %#v omitted %#v, want %#v", document["absences"], document["absences_omitted"], want)
	}
}

func TestNativeRuntimeContainmentWithholdsUnresolvedScopes(t *testing.T) {
	device := nativeContainmentObservedDevice(nativeContainmentDevice, nativeContainmentRemoteSKI)
	orphan := nativeContainmentDevice + ":[4,2]:"
	device.Entities = append(device.Entities,
		runtimeEntityObservation{ID: "entity:a", DeviceAddress: nativeContainmentDevice, EntityAddress: nativeContainmentDevice + ":[4]:", ParentEntityAddress: nativeContainmentDevice + ":[3]:", Type: "HeatingZone"},
		runtimeEntityObservation{ID: "entity:b", DeviceAddress: nativeContainmentDevice, EntityAddress: orphan, ParentEntityAddress: nativeContainmentDevice + ":[4]:", Type: "HeatingZone",
			Features: []runtimeFeatureObservation{{ID: "feature:b", DeviceAddress: nativeContainmentDevice, EntityAddress: orphan, FeatureAddress: orphan + "1", Type: "HVAC", Role: "server"}}},
	)
	device.UseCases = append(device.UseCases,
		runtimeUseCaseObservation{ID: "usecase:feature-only", ContextAddress: nativeContainmentDevice + ":[]:3", DeviceAddress: nativeContainmentDevice, FeatureAddress: nativeContainmentDevice + ":[]:3", Name: "monitoringOfRoomTemperature", Actor: "HeatPump"},
		runtimeUseCaseObservation{ID: "usecase:legacy", ContextAddress: nativeContainmentDevice + ":[1]:", Name: "monitoringOfPowerConsumption", Actor: "HeatPump"},
	)
	document := nativeContainmentMarshal(t, []runtimeGraphObservation{{RemoteSKI: nativeContainmentRemoteSKI, Visible: true, Devices: []runtimeDeviceObservation{device}}})
	entities, _ := document["entities"].([]any)
	features, _ := document["features"].([]any)
	useCases, _ := document["use_cases"].([]any)
	if len(entities) != 1 || len(features) != 1 || len(useCases) != 2 {
		t.Fatalf("emitted %d entities, %d features, %d claims, want 1, 1 and 2", len(entities), len(features), len(useCases))
	}
	legacy := useCases[1].(map[string]any)
	if legacy["name"] != "monitoringOfPowerConsumption" || legacy["device_address"] != nativeContainmentDevice || legacy["entity_address"] != nil {
		t.Fatalf("claim without a reported scope = %#v, want the containing device scope", legacy)
	}
	reasons := make([]string, 0)
	for _, value := range document["absences"].([]any) {
		record := value.(map[string]any)
		reasons = append(reasons, fmt.Sprintf("%s/%s/%v/%v", record["kind"], record["reason"], record["entity_address"], record["feature_address"]))
	}
	want := []string{
		"entity/missing_container/" + orphan + "/<nil>",
		"entity/missing_container/" + nativeContainmentDevice + ":[4]:/<nil>",
		"feature/missing_container/" + orphan + "/" + orphan + "1",
		"use_case/missing_container/<nil>/" + nativeContainmentDevice + ":[]:3",
	}
	if strings.Join(reasons, "\n") != strings.Join(want, "\n") {
		t.Fatalf("absences =\n%s\nwant\n%s", strings.Join(reasons, "\n"), strings.Join(want, "\n"))
	}
}

func TestNativeRuntimeContainmentBoundsAbsences(t *testing.T) {
	device := nativeContainmentObservedDevice(nativeContainmentDevice, nativeContainmentRemoteSKI)
	for index := 0; index < 1030; index++ {
		entity := fmt.Sprintf("%s:[%d]:", nativeContainmentDevice, index+10)
		device.Absences = append(device.Absences, runtimeAbsenceObservation{Kind: "entity", Reason: "missing_type", DeviceAddress: nativeContainmentDevice, EntityAddress: entity})
	}
	devices, err := exactRuntimeDeviceCollection([]runtimeDeviceObservation{device})
	if err != nil {
		t.Fatal(err)
	}
	document := nativeContainmentMarshal(t, []runtimeGraphObservation{{RemoteSKI: nativeContainmentRemoteSKI, Visible: true, Devices: devices}})
	absences, _ := document["absences"].([]any)
	if len(absences) != 1024 || document["absences_omitted"] != float64(6) {
		t.Fatalf("absences %d omitted %#v, want 1024 and 6", len(absences), document["absences_omitted"])
	}
	for index := 1; index < len(absences); index++ {
		previous := absences[index-1].(map[string]any)["entity_address"].(string)
		current := absences[index].(map[string]any)["entity_address"].(string)
		if previous >= current {
			t.Fatalf("absences are not sorted at %d: %q then %q", index, previous, current)
		}
	}
}

func TestNativeRuntimeContainmentReducerMergesNewFields(t *testing.T) {
	left := nativeContainmentObservedDevice(nativeContainmentDevice, nativeContainmentRemoteSKI)
	right := nativeContainmentObservedDevice(nativeContainmentDevice, nativeContainmentRemoteSKI)
	left.Entities[0].ParentEntityAddress = ""
	right.Entities[0].ParentEntityAddress = " " + nativeContainmentDevice + ":[0]: "
	left.UseCases[0].FeatureAddress = nativeContainmentDevice + ":[1]:0"
	right.UseCases[0].DeviceAddress = ""
	right.UseCases[0].EntityAddress = ""
	featureAbsence := runtimeAbsenceObservation{Kind: "feature", Reason: "missing_type", DeviceAddress: nativeContainmentDevice, EntityAddress: nativeContainmentDevice + ":[1]:", FeatureAddress: nativeContainmentDevice + ":[1]:7"}
	entityAbsence := runtimeAbsenceObservation{Kind: "entity", Reason: "missing_type", DeviceAddress: nativeContainmentDevice, EntityAddress: " " + nativeContainmentDevice + ":[9]:"}
	left.Absences = []runtimeAbsenceObservation{featureAbsence}
	right.Absences = []runtimeAbsenceObservation{entityAbsence, featureAbsence}
	merged, err := mergeRuntimeDeviceCollections([]runtimeDeviceObservation{left}, []runtimeDeviceObservation{right})
	if err != nil {
		t.Fatal(err)
	}
	if len(merged) != 1 {
		t.Fatalf("merged devices = %#v", merged)
	}
	if got := merged[0].Entities[0].ParentEntityAddress; got != nativeContainmentDevice+":[0]:" {
		t.Fatalf("parent = %q, want the trimmed non-empty side", got)
	}
	useCase := merged[0].UseCases[0]
	if useCase.DeviceAddress != nativeContainmentDevice || useCase.EntityAddress != nativeContainmentDevice+":[1]:" || useCase.FeatureAddress != nativeContainmentDevice+":[1]:0" {
		t.Fatalf("claim scope = %#v, want the non-empty side of each field", useCase)
	}
	entityAbsence.EntityAddress = nativeContainmentDevice + ":[9]:"
	if want := []runtimeAbsenceObservation{entityAbsence, featureAbsence}; !reflect.DeepEqual(merged[0].Absences, want) {
		t.Fatalf("absences = %#v, want sorted union %#v", merged[0].Absences, want)
	}
	graph := cloneRuntimeGraphObservation(runtimeGraphObservation{Devices: merged})
	graph.Devices[0].Absences[0].Reason = "changed"
	if merged[0].Absences[0].Reason == "changed" {
		t.Fatal("cloned graph shares absence records with its source")
	}
}
