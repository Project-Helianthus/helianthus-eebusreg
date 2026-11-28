package eebusfacade

import (
	"errors"
	"reflect"
	"sort"
	"testing"

	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
	"github.com/Project-Helianthus/helianthus-spine-go/spine"
)

func openOutputNode(t *testing.T) (*OutputNode, Backend) {
	t.Helper()
	service, _, backend := newOutputNodeRuntime(t)
	if err := service.Setup(); err != nil {
		t.Fatal(err)
	}
	node, err := OpenOutputNode(backend)
	if err != nil {
		t.Fatal(err)
	}
	return node, backend
}

func outputNodePtr[T any](value T) *T {
	return &value
}

func outputNodeScaled(number int64, scale int8) *spinemodel.ScaledNumberType {
	return &spinemodel.ScaledNumberType{
		Number: outputNodePtr(spinemodel.NumberType(number)),
		Scale:  outputNodePtr(spinemodel.ScaleType(scale)),
	}
}

func outputNodeFeatures(t *testing.T, node *OutputNode, letter string, want []string) {
	t.Helper()
	got := node.ServerFeatures(letter)
	copied := append([]string(nil), want...)
	sort.Strings(copied)
	if !reflect.DeepEqual(got, copied) {
		t.Fatalf("features %v, want %v", got, copied)
	}
}

func outputNodeData[T any](t *testing.T, node *OutputNode, letter string, featureType spinemodel.FeatureTypeType, function spinemodel.FunctionType) T {
	t.Helper()
	entity := node.byLetter[letter]
	if entity == nil {
		t.Fatalf("no entity %s", letter)
	}
	feature := entity.FeatureOfTypeAndRole(featureType, spinemodel.RoleTypeServer)
	if feature == nil {
		t.Fatalf("no feature %s on %s", featureType, letter)
	}
	data, err := spine.LocalFeatureDataCopyOfType[T](feature, function)
	if err != nil {
		t.Fatalf("%s %s: %v", letter, function, err)
	}
	return data
}

func TestOutputNodeAddEntityKinds(t *testing.T) {
	// Each row names the public kind, the SPINE constant, and the constant's
	// text. A swapped constant fails the constant check. A constant whose text
	// is no longer the specification name fails the text check. A second add of
	// the same address and kind adds nothing; a different kind returns the
	// existing error and leaves the type. Nesting is the parent's live address
	// plus the child's own number.
	node, _ := openOutputNode(t)
	roots := []struct {
		letter   string
		public   string
		spine    spinemodel.EntityTypeType
		observed string
		number   spinemodel.AddressEntityType
	}{
		{"a", "Inverter", spinemodel.EntityTypeTypeInverter, "Inverter", 2},
		{"b", "PV String", spinemodel.EntityTypeTypePVString, "PVString", 3},
		{"c", "EVSE", spinemodel.EntityTypeTypeEVSE, "EVSE", 4},
		{"d", "PV", spinemodel.EntityTypeTypePV, "PV", 5},
		{"e", "PVESHybrid", spinemodel.EntityTypeTypePVESHybrid, "PVESHybrid", 6},
		{"f", "ElectricalStorage", spinemodel.EntityTypeTypeElectricalStorage, "ElectricalStorage", 7},
		{"g", "Battery", spinemodel.EntityTypeTypeBattery, "Battery", 8},
		{"h", "HVACRoom", spinemodel.EntityTypeTypeHvacRoom, "HVACRoom", 9},
		{"i", "HeatingCircuit", spinemodel.EntityTypeTypeHeatingCircuit, "HeatingCircuit", 10},
		{"j", "HeatingZone", spinemodel.EntityTypeTypeHeatingZone, "HeatingZone", 11},
		{"k", "DHWCircuit", spinemodel.EntityTypeTypeDHWCircuit, "DHWCircuit", 12},
		{"l", "TemperatureSensor", spinemodel.EntityTypeTypeTemperatureSensor, "TemperatureSensor", 13},
		{"m", "GridConnectionPointOfPremises", spinemodel.EntityTypeTypeGridConnectionPointOfPremises, "GridConnectionPointOfPremises", 14},
		{"n", "SubMeterElectricity", spinemodel.EntityTypeTypeSubMeterElectricity, "SubMeterElectricity", 15},
		{"o", "HeatPumpAppliance", spinemodel.EntityTypeTypeHeatPumpAppliance, "HeatPumpAppliance", 16},
		{"p", "EV", spinemodel.EntityTypeTypeEV, "EV", 17},
	}
	for _, item := range roots {
		if err := node.AddEntity(item.public, item.letter, ""); err != nil {
			t.Fatalf("add %s: %v", item.public, err)
		}
	}
	entities := len(node.device().Entities())
	for _, item := range roots {
		if err := node.AddEntity(item.public, item.letter, "absent"); err != nil {
			t.Fatalf("second add %s: %v", item.public, err)
		}
		got := node.byLetter[item.letter]
		if got.EntityType() != item.spine || string(got.EntityType()) != item.observed {
			t.Fatalf("%s type %q", item.letter, got.EntityType())
		}
		if publicEntityName(got.EntityType()) != item.public {
			t.Fatalf("%s public name %q", item.letter, publicEntityName(got.EntityType()))
		}
		if !reflect.DeepEqual(got.Address().Entity, []spinemodel.AddressEntityType{item.number}) {
			t.Fatalf("%s address %v", item.letter, got.Address().Entity)
		}
	}
	if len(node.device().Entities()) != entities {
		t.Fatal("second add duplicated an entity")
	}
	if err := node.AddEntity("Battery", "a", ""); !errors.Is(err, errOutputNode) {
		t.Fatalf("different kind err %v", err)
	}
	if node.byLetter["a"].EntityType() != spinemodel.EntityTypeTypeInverter {
		t.Fatal("different kind replaced the inverter")
	}
	before := len(node.byLetter)
	if err := node.AddEntity("Battery", "aa", "absent"); !errors.Is(err, errOutputNode) {
		t.Fatalf("missing parent err %v", err)
	}
	if err := node.AddEntity("Compressor", "aa", ""); !errors.Is(err, errOutputNode) {
		t.Fatalf("unknown kind err %v", err)
	}
	if len(node.byLetter) != before {
		t.Fatal("a rejected add created an entity")
	}
	nests := []struct {
		letter   string
		public   string
		parent   string
		spine    spinemodel.EntityTypeType
		observed string
		address  []spinemodel.AddressEntityType
	}{
		{"q", "PVESHybrid", "", spinemodel.EntityTypeTypePVESHybrid, "PVESHybrid", []spinemodel.AddressEntityType{18}},
		{"r", "Inverter", "q", spinemodel.EntityTypeTypeInverter, "Inverter", []spinemodel.AddressEntityType{18, 19}},
		{"s", "Battery", "r", spinemodel.EntityTypeTypeBattery, "Battery", []spinemodel.AddressEntityType{18, 19, 20}},
		{"t", "ElectricalStorage", "", spinemodel.EntityTypeTypeElectricalStorage, "ElectricalStorage", []spinemodel.AddressEntityType{21}},
		{"u", "Inverter", "t", spinemodel.EntityTypeTypeInverter, "Inverter", []spinemodel.AddressEntityType{21, 22}},
		{"v", "Battery", "u", spinemodel.EntityTypeTypeBattery, "Battery", []spinemodel.AddressEntityType{21, 22, 23}},
		{"w", "Inverter", "", spinemodel.EntityTypeTypeInverter, "Inverter", []spinemodel.AddressEntityType{24}},
		{"x", "PV String", "w", spinemodel.EntityTypeTypePVString, "PVString", []spinemodel.AddressEntityType{24, 25}},
		{"y", "EVSE", "", spinemodel.EntityTypeTypeEVSE, "EVSE", []spinemodel.AddressEntityType{26}},
		{"z", "EV", "y", spinemodel.EntityTypeTypeEV, "EV", []spinemodel.AddressEntityType{26, 27}},
	}
	for _, item := range nests {
		if err := node.AddEntity(item.public, item.letter, item.parent); err != nil {
			t.Fatalf("nest %s: %v", item.letter, err)
		}
		got := node.byLetter[item.letter]
		if got.EntityType() != item.spine || string(got.EntityType()) != item.observed {
			t.Fatalf("%s type %q", item.letter, got.EntityType())
		}
		if !reflect.DeepEqual(got.Address().Entity, item.address) {
			t.Fatalf("%s address %v", item.letter, got.Address().Entity)
		}
	}
	if err := node.AddEntity("Battery", "s", "w"); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"s", "r"}, {"s", "q"}, {"r", "q"}, {"v", "u"}, {"v", "t"}, {"u", "t"}, {"x", "w"}, {"z", "y"}} {
		if !node.NestedUnder(pair[0], pair[1]) {
			t.Fatalf("%s is not under %s", pair[0], pair[1])
		}
	}
	if node.NestedUnder("z", "w") || node.NestedUnder("q", "r") || node.NestedUnder("s", "w") {
		t.Fatal("nesting crossed families")
	}
	wantCounts := map[string]int{
		"CEM": 1, "Inverter": 4, "PV String": 2, "EVSE": 2, "PV": 1,
		"PVESHybrid": 2, "ElectricalStorage": 2, "Battery": 3, "HVACRoom": 1,
		"HeatingCircuit": 1, "HeatingZone": 1, "DHWCircuit": 1, "TemperatureSensor": 1,
		"GridConnectionPointOfPremises": 1, "SubMeterElectricity": 1, "HeatPumpAppliance": 1,
		"EV": 2,
	}
	gotCounts := map[string]int{}
	for _, name := range node.EntityTypes() {
		if name == "PVString" || name == "DeviceInformation" {
			t.Fatalf("public entity list contains %q", name)
		}
		gotCounts[name]++
	}
	if !reflect.DeepEqual(gotCounts, wantCounts) {
		t.Fatalf("entity types %v", gotCounts)
	}
	wantObserved := []string{"DeviceInformation", "CEM"}
	for _, item := range roots {
		wantObserved = append(wantObserved, item.observed)
	}
	for _, item := range nests {
		wantObserved = append(wantObserved, item.observed)
	}
	if !reflect.DeepEqual(node.ObservedEntityTypes(), wantObserved) {
		t.Fatalf("observed %v", node.ObservedEntityTypes())
	}
}

func TestOutputNodeMeasurementValues(t *testing.T) {
	// Description fields are measurementType, commodityType, unit, scopeType,
	// label, description and calibrationValue. The value key is measurement id
	// plus value type: dropping either collapses two stored values. Number,
	// scale, valueSource, valueState and valueTendency are stored as given.
	// Constraint key is measurement id; min, max and step stay with that id.
	node, _ := openOutputNode(t)
	if err := node.AddEntity("Inverter", "na", ""); err != nil {
		t.Fatal(err)
	}
	preset := spinemodel.MeasurementIdType(7)
	if _, err := node.AddMeasurementDescription("na", spinemodel.MeasurementDescriptionDataType{MeasurementId: &preset}); !errors.Is(err, errOutputNode) {
		t.Fatalf("preset id err %v", err)
	}
	if _, err := node.AddMeasurementDescription("nb", spinemodel.MeasurementDescriptionDataType{}); !errors.Is(err, errOutputNode) {
		t.Fatal("description accepted without a measurement feature")
	}
	if err := node.AddServerFeature("na", "measurement"); err != nil {
		t.Fatal(err)
	}
	power := spinemodel.MeasurementDescriptionDataType{
		MeasurementType:  outputNodePtr(spinemodel.MeasurementTypeTypePower),
		CommodityType:    outputNodePtr(spinemodel.CommodityTypeTypeElectricity),
		Unit:             outputNodePtr(spinemodel.UnitOfMeasurementTypeW),
		ScopeType:        outputNodePtr(spinemodel.ScopeTypeTypeACPowerTotal),
		Label:            outputNodePtr(spinemodel.LabelType("total")),
		Description:      outputNodePtr(spinemodel.DescriptionType("active")),
		CalibrationValue: outputNodeScaled(1, 0),
	}
	current := spinemodel.MeasurementDescriptionDataType{
		MeasurementType: outputNodePtr(spinemodel.MeasurementTypeTypeCurrent),
		CommodityType:   outputNodePtr(spinemodel.CommodityTypeTypeGas),
		Unit:            outputNodePtr(spinemodel.UnitOfMeasurementTypeA),
		ScopeType:       outputNodePtr(spinemodel.ScopeTypeTypeACCurrent),
		Label:           outputNodePtr(spinemodel.LabelType("phase")),
		Description:     outputNodePtr(spinemodel.DescriptionType("line")),
	}
	powerID, err := node.AddMeasurementDescription("na", power)
	if err != nil {
		t.Fatal(err)
	}
	currentID, err := node.AddMeasurementDescription("na", current)
	if err != nil {
		t.Fatal(err)
	}
	if powerID == currentID {
		t.Fatal("two descriptions share one measurement id")
	}
	power.MeasurementId = &powerID
	current.MeasurementId = &currentID
	descriptions := outputNodeData[*spinemodel.MeasurementDescriptionListDataType](t, node, "na", spinemodel.FeatureTypeTypeMeasurement, spinemodel.FunctionTypeMeasurementDescriptionListData)
	if !outputNodeSameDescription(descriptions.MeasurementDescriptionData, power) || !outputNodeSameDescription(descriptions.MeasurementDescriptionData, current) {
		t.Fatalf("descriptions %+v", descriptions.MeasurementDescriptionData)
	}
	measured := spinemodel.MeasurementValueSourceTypeMeasuredValue
	calculated := spinemodel.MeasurementValueSourceTypeCalculatedValue
	normal := spinemodel.MeasurementValueStateTypeNormal
	outOfRange := spinemodel.MeasurementValueStateTypeOutofrange
	rising := spinemodel.MeasurementValueTendencyTypeRising
	falling := spinemodel.MeasurementValueTendencyTypeFalling
	instant := spinemodel.MeasurementValueTypeTypeValue
	average := spinemodel.MeasurementValueTypeTypeAverageValue
	writes := []spinemodel.MeasurementDataType{
		{MeasurementId: &powerID, ValueType: &instant, Value: outputNodeScaled(1500, -1), ValueSource: &measured, ValueState: &normal, ValueTendency: &rising},
		{MeasurementId: &powerID, ValueType: &average, Value: outputNodeScaled(15, 2), ValueSource: &calculated, ValueState: &outOfRange, ValueTendency: &falling},
		{MeasurementId: &currentID, ValueType: &instant, Value: outputNodeScaled(8, 0), ValueSource: &measured, ValueState: &normal, ValueTendency: &rising},
	}
	for _, write := range writes {
		if err := node.UpdateMeasurementValue("na", *write.MeasurementId, write, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := node.UpdateMeasurementValue("na", currentID, spinemodel.MeasurementDataType{
		ValueType: &instant, Value: outputNodeScaled(8, 1), ValueState: &normal, ValueTendency: &rising,
	}, &spinemodel.MeasurementDataElementsType{ValueSource: &spinemodel.ElementTagType{}}); err != nil {
		t.Fatal(err)
	}
	writes[2].Value = outputNodeScaled(8, 1)
	writes[2].ValueSource = nil
	values := outputNodeData[*spinemodel.MeasurementListDataType](t, node, "na", spinemodel.FeatureTypeTypeMeasurement, spinemodel.FunctionTypeMeasurementListData)
	if len(values.MeasurementData) != len(writes) {
		t.Fatalf("values %+v", values.MeasurementData)
	}
	for _, want := range writes {
		if !outputNodeSameValue(values.MeasurementData, want) {
			t.Fatalf("missing %+v in %+v", want, values.MeasurementData)
		}
	}
	if err := node.AddMeasurementConstraints("na", spinemodel.MeasurementConstraintsDataType{}); !errors.Is(err, errOutputNode) {
		t.Fatal("constraint without an id was stored")
	}
	unknown := spinemodel.MeasurementIdType(99)
	if err := node.AddMeasurementConstraints("na", spinemodel.MeasurementConstraintsDataType{MeasurementId: &unknown}); !errors.Is(err, errOutputNode) {
		t.Fatal("constraint for an unknown measurement was stored")
	}
	constraints := []spinemodel.MeasurementConstraintsDataType{
		{MeasurementId: &powerID, ValueRangeMin: outputNodeScaled(1, 0), ValueRangeMax: outputNodeScaled(9, 0), ValueStepSize: outputNodeScaled(1, 0)},
		{MeasurementId: &currentID, ValueRangeMin: outputNodeScaled(2, -1), ValueRangeMax: outputNodeScaled(8, 1), ValueStepSize: outputNodeScaled(5, 0)},
	}
	for _, item := range constraints {
		if err := node.AddMeasurementConstraints("na", item); err != nil {
			t.Fatal(err)
		}
	}
	stored := outputNodeData[*spinemodel.MeasurementConstraintsListDataType](t, node, "na", spinemodel.FeatureTypeTypeMeasurement, spinemodel.FunctionTypeMeasurementConstraintsListData)
	if len(stored.MeasurementConstraintsData) != len(constraints) {
		t.Fatalf("constraints %+v", stored.MeasurementConstraintsData)
	}
	for _, want := range constraints {
		if !outputNodeSameConstraint(stored.MeasurementConstraintsData, want) {
			t.Fatalf("missing %+v in %+v", want, stored.MeasurementConstraintsData)
		}
	}
}

func outputNodeSameDescription(got []spinemodel.MeasurementDescriptionDataType, want spinemodel.MeasurementDescriptionDataType) bool {
	for _, item := range got {
		if reflect.DeepEqual(item, want) {
			return true
		}
	}
	return false
}

func outputNodeSameValue(got []spinemodel.MeasurementDataType, want spinemodel.MeasurementDataType) bool {
	for _, item := range got {
		if reflect.DeepEqual(item, want) {
			return true
		}
	}
	return false
}

func outputNodeSameConstraint(got []spinemodel.MeasurementConstraintsDataType, want spinemodel.MeasurementConstraintsDataType) bool {
	for _, item := range got {
		if reflect.DeepEqual(item, want) {
			return true
		}
	}
	return false
}

func TestOutputNodeElectricalCharacteristics(t *testing.T) {
	// Connection id is the description key the caller supplies. Parameter key
	// is connection id plus parameter id. Characteristic key is connection id,
	// parameter id and characteristic id: an update that ignores one of those
	// changes the other characteristic or accepts a mismatched id.
	node, _ := openOutputNode(t)
	if err := node.AddEntity("EVSE", "na", ""); err != nil {
		t.Fatal(err)
	}
	grid := spinemodel.ElectricalConnectionIdType(4)
	home := spinemodel.ElectricalConnectionIdType(9)
	if err := node.AddElectricalDescription("na", spinemodel.ElectricalConnectionDescriptionDataType{ElectricalConnectionId: &grid}); !errors.Is(err, errOutputNode) {
		t.Fatal("description accepted without the feature")
	}
	if err := node.AddServerFeature("na", "electrical connection"); err != nil {
		t.Fatal(err)
	}
	if err := node.AddElectricalDescription("na", spinemodel.ElectricalConnectionDescriptionDataType{}); !errors.Is(err, errOutputNode) {
		t.Fatal("description without a connection id was stored")
	}
	descriptions := []spinemodel.ElectricalConnectionDescriptionDataType{
		{ElectricalConnectionId: &grid, PowerSupplyType: outputNodePtr(spinemodel.ElectricalConnectionVoltageTypeTypeAc), PositiveEnergyDirection: outputNodePtr(spinemodel.EnergyDirectionTypeConsume), AcConnectedPhases: outputNodePtr(uint(3)), Label: outputNodePtr(spinemodel.LabelType("grid"))},
		{ElectricalConnectionId: &home, PowerSupplyType: outputNodePtr(spinemodel.ElectricalConnectionVoltageTypeTypeDc), PositiveEnergyDirection: outputNodePtr(spinemodel.EnergyDirectionTypeProduce), AcConnectedPhases: outputNodePtr(uint(1)), Label: outputNodePtr(spinemodel.LabelType("home"))},
	}
	for _, item := range descriptions {
		if err := node.AddElectricalDescription("na", item); err != nil {
			t.Fatal(err)
		}
	}
	storedDescriptions := outputNodeData[*spinemodel.ElectricalConnectionDescriptionListDataType](t, node, "na", spinemodel.FeatureTypeTypeElectricalConnection, spinemodel.FunctionTypeElectricalConnectionDescriptionListData)
	if len(storedDescriptions.ElectricalConnectionDescriptionData) != len(descriptions) {
		t.Fatalf("descriptions %+v", storedDescriptions.ElectricalConnectionDescriptionData)
	}
	for _, want := range descriptions {
		found := false
		for _, item := range storedDescriptions.ElectricalConnectionDescriptionData {
			if reflect.DeepEqual(item, want) {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing %+v in %+v", want, storedDescriptions.ElectricalConnectionDescriptionData)
		}
	}
	preset := spinemodel.ElectricalConnectionParameterIdType(3)
	if _, err := node.AddElectricalParameter("na", spinemodel.ElectricalConnectionParameterDescriptionDataType{ElectricalConnectionId: &grid, ParameterId: &preset}); !errors.Is(err, errOutputNode) {
		t.Fatal("preset parameter id was accepted")
	}
	powerID := spinemodel.MeasurementIdType(3)
	currentID := spinemodel.MeasurementIdType(8)
	parameters := []spinemodel.ElectricalConnectionParameterDescriptionDataType{
		{ElectricalConnectionId: &grid, MeasurementId: &powerID, VoltageType: outputNodePtr(spinemodel.ElectricalConnectionVoltageTypeTypeAc), AcMeasurementType: outputNodePtr(spinemodel.ElectricalConnectionAcMeasurementTypeTypeReal), AcMeasurementVariant: outputNodePtr(spinemodel.ElectricalConnectionMeasurandVariantTypeRms), AcMeasuredPhases: outputNodePtr(spinemodel.ElectricalConnectionPhaseNameTypeA), AcMeasuredInReferenceTo: outputNodePtr(spinemodel.ElectricalConnectionPhaseNameTypeNeutral)},
		{ElectricalConnectionId: &home, MeasurementId: &currentID, VoltageType: outputNodePtr(spinemodel.ElectricalConnectionVoltageTypeTypeDc), AcMeasurementType: outputNodePtr(spinemodel.ElectricalConnectionAcMeasurementTypeTypeApparent), AcMeasurementVariant: outputNodePtr(spinemodel.ElectricalConnectionMeasurandVariantTypeAmplitude), AcMeasuredPhases: outputNodePtr(spinemodel.ElectricalConnectionPhaseNameTypeB), AcMeasuredInReferenceTo: outputNodePtr(spinemodel.ElectricalConnectionPhaseNameTypeGround)},
	}
	for i := range parameters {
		id, err := node.AddElectricalParameter("na", parameters[i])
		if err != nil {
			t.Fatal(err)
		}
		if id != 0 {
			t.Fatalf("parameter id %d, each connection starts at zero", id)
		}
		parameters[i].ParameterId = &id
	}
	storedParameters := outputNodeData[*spinemodel.ElectricalConnectionParameterDescriptionListDataType](t, node, "na", spinemodel.FeatureTypeTypeElectricalConnection, spinemodel.FunctionTypeElectricalConnectionParameterDescriptionListData)
	for _, want := range parameters {
		found := false
		for _, item := range storedParameters.ElectricalConnectionParameterDescriptionData {
			if reflect.DeepEqual(item, want) {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing %+v in %+v", want, storedParameters.ElectricalConnectionParameterDescriptionData)
		}
	}
	presetCharacteristic := spinemodel.ElectricalConnectionCharacteristicIdType(2)
	if _, err := node.AddElectricalCharacteristic("na", spinemodel.ElectricalConnectionCharacteristicDataType{ElectricalConnectionId: &grid, ParameterId: parameters[0].ParameterId, CharacteristicId: &presetCharacteristic}); !errors.Is(err, errOutputNode) {
		t.Fatal("preset characteristic id was accepted")
	}
	characteristics := []spinemodel.ElectricalConnectionCharacteristicDataType{
		{ElectricalConnectionId: &grid, ParameterId: parameters[0].ParameterId, CharacteristicContext: outputNodePtr(spinemodel.ElectricalConnectionCharacteristicContextTypeDevice), CharacteristicType: outputNodePtr(spinemodel.ElectricalConnectionCharacteristicTypeTypePowerConsumptionMin), Value: outputNodeScaled(10, 0), Unit: outputNodePtr(spinemodel.UnitOfMeasurementTypeW)},
		{ElectricalConnectionId: &home, ParameterId: parameters[1].ParameterId, CharacteristicContext: outputNodePtr(spinemodel.ElectricalConnectionCharacteristicContextTypeEntity), CharacteristicType: outputNodePtr(spinemodel.ElectricalConnectionCharacteristicTypeTypePowerProductionMax), Value: outputNodeScaled(20, -1), Unit: outputNodePtr(spinemodel.UnitOfMeasurementTypeA)},
	}
	for i := range characteristics {
		id, err := node.AddElectricalCharacteristic("na", characteristics[i])
		if err != nil {
			t.Fatal(err)
		}
		characteristics[i].CharacteristicId = &id
	}
	if characteristics[0].CharacteristicId == nil || characteristics[1].CharacteristicId == nil || *characteristics[0].CharacteristicId == *characteristics[1].CharacteristicId {
		t.Fatal("characteristic ids were not assigned per stored row")
	}
	updated := characteristics[0]
	updated.Value = outputNodeScaled(11, 0)
	updated.Unit = nil
	if err := node.UpdateElectricalCharacteristic("na", updated, &spinemodel.ElectricalConnectionCharacteristicDataElementsType{Unit: &spinemodel.ElementTagType{}}); err != nil {
		t.Fatal(err)
	}
	mismatched := updated
	mismatched.ElectricalConnectionId = &home
	if err := node.UpdateElectricalCharacteristic("na", mismatched, nil); !errors.Is(err, errOutputNode) {
		t.Fatal("update with the other connection id was accepted")
	}
	characteristics[0] = updated
	storedCharacteristics := outputNodeData[*spinemodel.ElectricalConnectionCharacteristicListDataType](t, node, "na", spinemodel.FeatureTypeTypeElectricalConnection, spinemodel.FunctionTypeElectricalConnectionCharacteristicListData)
	if len(storedCharacteristics.ElectricalConnectionCharacteristicData) != len(characteristics) {
		t.Fatalf("characteristics %+v", storedCharacteristics.ElectricalConnectionCharacteristicData)
	}
	for _, want := range characteristics {
		found := false
		for _, item := range storedCharacteristics.ElectricalConnectionCharacteristicData {
			if reflect.DeepEqual(item, want) {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing %+v in %+v", want, storedCharacteristics.ElectricalConnectionCharacteristicData)
		}
	}
}

func TestOutputNodeDiagnosisAndHeartbeat(t *testing.T) {
	// Operating state and last error code are both stored. Replacing the
	// service stops the heartbeat of the entity that was serving diagnosis.
	// Setpoint and HVAC have no server helper and stay rejected.
	node, backend := openOutputNode(t)
	if err := node.AddEntity("EVSE", "na", ""); err != nil {
		t.Fatal(err)
	}
	entity := node.byLetter["na"]
	if entity.HeartbeatManager() == nil || entity.HeartbeatManager().IsHeartbeatRunning() {
		t.Fatal("heartbeat ran before device diagnosis")
	}
	for _, name := range []string{"setpoint", "hvac"} {
		rejected, err := node.AddServerFeatures("na", []string{name})
		if err == nil || rejected != name {
			t.Fatalf("rejected %q err %v", rejected, err)
		}
	}
	outputNodeFeatures(t, node, "na", nil)
	failure := spinemodel.DeviceDiagnosisOperatingStateTypeFailure
	if err := node.SetDeviceDiagnosisState("na", &spinemodel.DeviceDiagnosisStateDataType{OperatingState: &failure}); !errors.Is(err, errOutputNode) {
		t.Fatal("state written without device diagnosis")
	}
	if err := node.AddServerFeature("na", "device diagnosis"); err != nil {
		t.Fatal(err)
	}
	if !entity.HeartbeatManager().IsHeartbeatRunning() {
		t.Fatal("device diagnosis did not start the heartbeat")
	}
	if err := node.SetDeviceDiagnosisState("na", nil); !errors.Is(err, errOutputNode) {
		t.Fatal(err)
	}
	if err := node.SetDeviceDiagnosisState("na", &spinemodel.DeviceDiagnosisStateDataType{OperatingState: &failure}); err != nil {
		t.Fatal(err)
	}
	state := outputNodeData[*spinemodel.DeviceDiagnosisStateDataType](t, node, "na", spinemodel.FeatureTypeTypeDeviceDiagnosis, spinemodel.FunctionTypeDeviceDiagnosisStateData)
	if state.OperatingState == nil || *state.OperatingState != failure || state.LastErrorCode != nil {
		t.Fatalf("state %+v", state)
	}
	normal := spinemodel.DeviceDiagnosisOperatingStateTypeNormalOperation
	code := spinemodel.LastErrorCodeType("open-circuit")
	if err := node.SetDeviceDiagnosisState("na", &spinemodel.DeviceDiagnosisStateDataType{OperatingState: &normal, LastErrorCode: &code}); err != nil {
		t.Fatal(err)
	}
	state = outputNodeData[*spinemodel.DeviceDiagnosisStateDataType](t, node, "na", spinemodel.FeatureTypeTypeDeviceDiagnosis, spinemodel.FunctionTypeDeviceDiagnosisStateData)
	if state.OperatingState == nil || *state.OperatingState != normal || state.LastErrorCode == nil || *state.LastErrorCode != code {
		t.Fatalf("state %+v", state)
	}
	manager := entity.HeartbeatManager()
	_, replacement := newOutputNodeService(t, 4713, outputNodeThirdToken)
	ReplaceOutputService(backend, replacement)
	if manager.IsHeartbeatRunning() {
		t.Fatal("replacing the service left the heartbeat running")
	}
	ReplaceOutputService(backend, node.service)
}

func TestOutputNodeConfigurationAndClassification(t *testing.T) {
	// Classification is attached and has no data writer. Configuration key id
	// is assigned, not the caller's preset. Key name, value type and unit
	// distinguish the two keys; a boolean value and a scaled value stay on
	// their own key.
	node, _ := openOutputNode(t)
	if err := node.AddEntity("Battery", "na", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := node.AddConfigurationDescription("na", spinemodel.DeviceConfigurationKeyValueDescriptionDataType{}); !errors.Is(err, errOutputNode) {
		t.Fatal("configuration accepted without the feature")
	}
	if _, err := node.AddServerFeatures("na", []string{"device classification", "device configuration"}); err != nil {
		t.Fatal(err)
	}
	outputNodeFeatures(t, node, "na", []string{"device classification", "device configuration"})
	if observed := node.ObservedServerFeatures("na"); !reflect.DeepEqual(observed, []string{"DeviceClassification", "DeviceConfiguration"}) {
		t.Fatalf("observed features %v", observed)
	}
	preset := spinemodel.DeviceConfigurationKeyIdType(7)
	phaseName := spinemodel.DeviceConfigurationKeyNameType("phaseCount")
	exportName := spinemodel.DeviceConfigurationKeyNameType("exportEnabled")
	phaseID, err := node.AddConfigurationDescription("na", spinemodel.DeviceConfigurationKeyValueDescriptionDataType{
		KeyId: &preset, KeyName: &phaseName, ValueType: outputNodePtr(spinemodel.DeviceConfigurationKeyValueTypeTypeScaledNumber), Unit: outputNodePtr(spinemodel.UnitOfMeasurementTypeW),
	})
	if err != nil {
		t.Fatal(err)
	}
	if phaseID == preset {
		t.Fatal("preset configuration key id was kept")
	}
	exportID, err := node.AddConfigurationDescription("na", spinemodel.DeviceConfigurationKeyValueDescriptionDataType{
		KeyName: &exportName, ValueType: outputNodePtr(spinemodel.DeviceConfigurationKeyValueTypeTypeBoolean), Unit: outputNodePtr(spinemodel.UnitOfMeasurementTypeWh),
	})
	if err != nil {
		t.Fatal(err)
	}
	if exportID == phaseID {
		t.Fatal("two configuration keys share one id")
	}
	descriptions := outputNodeData[*spinemodel.DeviceConfigurationKeyValueDescriptionListDataType](t, node, "na", spinemodel.FeatureTypeTypeDeviceConfiguration, spinemodel.FunctionTypeDeviceConfigurationKeyValueDescriptionListData)
	if !outputNodeConfigurationDescription(descriptions, phaseID, phaseName, spinemodel.DeviceConfigurationKeyValueTypeTypeScaledNumber, spinemodel.UnitOfMeasurementTypeW) {
		t.Fatalf("phase description %+v", descriptions.DeviceConfigurationKeyValueDescriptionData)
	}
	if !outputNodeConfigurationDescription(descriptions, exportID, exportName, spinemodel.DeviceConfigurationKeyValueTypeTypeBoolean, spinemodel.UnitOfMeasurementTypeWh) {
		t.Fatalf("export description %+v", descriptions.DeviceConfigurationKeyValueDescriptionData)
	}
	if err := node.UpdateConfigurationValue("na", phaseID, spinemodel.DeviceConfigurationKeyValueDataType{Value: &spinemodel.DeviceConfigurationKeyValueValueType{ScaledNumber: outputNodeScaled(12, -1)}, IsValueChangeable: outputNodePtr(true)}, nil); err != nil {
		t.Fatal(err)
	}
	if err := node.UpdateConfigurationValue("na", exportID, spinemodel.DeviceConfigurationKeyValueDataType{Value: &spinemodel.DeviceConfigurationKeyValueValueType{Boolean: outputNodePtr(false)}, IsValueChangeable: outputNodePtr(false)}, nil); err != nil {
		t.Fatal(err)
	}
	if err := node.UpdateConfigurationValue("na", phaseID, spinemodel.DeviceConfigurationKeyValueDataType{Value: &spinemodel.DeviceConfigurationKeyValueValueType{ScaledNumber: outputNodeScaled(13, -1)}, IsValueChangeable: outputNodePtr(true)}, nil); err != nil {
		t.Fatal(err)
	}
	unknown := spinemodel.DeviceConfigurationKeyIdType(99)
	if err := node.UpdateConfigurationValue("na", unknown, spinemodel.DeviceConfigurationKeyValueDataType{Value: &spinemodel.DeviceConfigurationKeyValueValueType{Boolean: outputNodePtr(true)}}, nil); !errors.Is(err, errOutputNode) {
		t.Fatal("unknown configuration key was updated")
	}
	values := outputNodeData[*spinemodel.DeviceConfigurationKeyValueListDataType](t, node, "na", spinemodel.FeatureTypeTypeDeviceConfiguration, spinemodel.FunctionTypeDeviceConfigurationKeyValueListData)
	phase := outputNodeConfigurationValue(values, phaseID)
	export := outputNodeConfigurationValue(values, exportID)
	if phase == nil || phase.Value == nil || phase.Value.ScaledNumber == nil || !reflect.DeepEqual(phase.Value.ScaledNumber, outputNodeScaled(13, -1)) || phase.Value.Boolean != nil || phase.IsValueChangeable == nil || !*phase.IsValueChangeable {
		t.Fatalf("phase value %+v", phase)
	}
	if export == nil || export.Value == nil || export.Value.Boolean == nil || *export.Value.Boolean || export.Value.ScaledNumber != nil || export.IsValueChangeable == nil || *export.IsValueChangeable {
		t.Fatalf("export value %+v", export)
	}
}

func outputNodeConfigurationDescription(data *spinemodel.DeviceConfigurationKeyValueDescriptionListDataType, id spinemodel.DeviceConfigurationKeyIdType, name spinemodel.DeviceConfigurationKeyNameType, valueType spinemodel.DeviceConfigurationKeyValueTypeType, unit spinemodel.UnitOfMeasurementType) bool {
	if data == nil {
		return false
	}
	for _, item := range data.DeviceConfigurationKeyValueDescriptionData {
		if item.KeyId != nil && *item.KeyId == id && item.KeyName != nil && *item.KeyName == name && item.ValueType != nil && *item.ValueType == valueType && item.Unit != nil && *item.Unit == unit {
			return true
		}
	}
	return false
}

func outputNodeConfigurationValue(data *spinemodel.DeviceConfigurationKeyValueListDataType, id spinemodel.DeviceConfigurationKeyIdType) *spinemodel.DeviceConfigurationKeyValueDataType {
	if data == nil {
		return nil
	}
	for i := range data.DeviceConfigurationKeyValueData {
		item := &data.DeviceConfigurationKeyValueData[i]
		if item.KeyId != nil && *item.KeyId == id {
			return item
		}
	}
	return nil
}

func TestOutputNodeUseCaseFields(t *testing.T) {
	// Support identity is actor plus name. Version, sub-revision, availability
	// and scenarios are stored with that pair. A copy keeps a later edit of the
	// caller's slices from changing the registered support. Withdrawing a
	// different actor or a different name leaves the entry.
	node, _ := openOutputNode(t)
	if err := node.AddEntity("Inverter", "na", ""); err != nil {
		t.Fatal(err)
	}
	for _, support := range []OutputUseCase{
		{Name: spinemodel.UseCaseNameTypeMonitoringOfInverter, Version: "1.0.0"},
		{Actor: spinemodel.UseCaseActorTypeInverter, Version: "1.0.0"},
		{Actor: spinemodel.UseCaseActorTypeInverter, Name: spinemodel.UseCaseNameTypeMonitoringOfInverter},
	} {
		support.Features = []string{"measurement"}
		if _, err := node.RegisterUseCase("na", support); !errors.Is(err, errOutputNode) {
			t.Fatalf("incomplete support registered: %+v", support)
		}
	}
	outputNodeFeatures(t, node, "na", nil)
	inverterScenarios := []spinemodel.UseCaseScenarioSupportType{1, 2}
	inverterFeatures := []string{"measurement", "device diagnosis"}
	evseScenarios := []spinemodel.UseCaseScenarioSupportType{3}
	evseFeatures := []string{"measurement", "electrical connection"}
	monitoredScenarios := []spinemodel.UseCaseScenarioSupportType{4, 5}
	monitoredFeatures := []string{"load control"}
	supports := []OutputUseCase{
		{Actor: spinemodel.UseCaseActorTypeInverter, Name: spinemodel.UseCaseNameTypeMonitoringOfInverter, Version: "1.0.0", SubRevision: spinemodel.UseCaseDocumentSubRevisionRelease, Available: true, Scenarios: inverterScenarios, Features: inverterFeatures},
		{Actor: spinemodel.UseCaseActorTypeEVSE, Name: spinemodel.UseCaseNameTypeMonitoringOfInverter, Version: "1.0.1", SubRevision: "draft", Available: false, Scenarios: evseScenarios, Features: evseFeatures},
		{Actor: spinemodel.UseCaseActorTypeInverter, Name: spinemodel.UseCaseNameTypeMonitoringOfPowerConsumption, Version: "1.2.0", SubRevision: spinemodel.UseCaseDocumentSubRevisionRelease, Available: true, Scenarios: monitoredScenarios, Features: monitoredFeatures},
	}
	for _, support := range supports {
		if _, err := node.RegisterUseCase("na", support); err != nil {
			t.Fatal(err)
		}
	}
	registered := append([]OutputUseCase(nil), supports...)
	registered[0].Scenarios = append([]spinemodel.UseCaseScenarioSupportType(nil), inverterScenarios...)
	inverterScenarios[0] = 9
	inverterFeatures[1] = "load control"
	for _, support := range registered {
		outputNodeExpectSupport(t, node, "na", support)
	}
	if err := node.WithdrawUseCase("na", spinemodel.UseCaseActorTypeMonitoredUnit, spinemodel.UseCaseNameTypeMonitoringOfInverter); err != nil {
		t.Fatal(err)
	}
	if err := node.WithdrawUseCase("na", spinemodel.UseCaseActorTypeInverter, spinemodel.UseCaseNameTypeMonitoringOfBattery); err != nil {
		t.Fatal(err)
	}
	outputNodeExpectSupport(t, node, "na", registered[0])
	outputNodeExpectSupport(t, node, "na", registered[1])
	if err := node.WithdrawUseCase("na", "", spinemodel.UseCaseNameTypeMonitoringOfInverter); !errors.Is(err, errOutputNode) {
		t.Fatal("withdraw accepted an empty actor")
	}
	if err := node.WithdrawUseCase("na", spinemodel.UseCaseActorTypeInverter, ""); !errors.Is(err, errOutputNode) {
		t.Fatal("withdraw accepted an empty name")
	}
	if err := node.WithdrawUseCase("na", spinemodel.UseCaseActorTypeInverter, spinemodel.UseCaseNameTypeMonitoringOfInverter); err != nil {
		t.Fatal(err)
	}
	outputNodeFeatures(t, node, "na", []string{"electrical connection", "load control", "measurement"})
}

func TestOutputNodeUseCaseWithdrawal(t *testing.T) {
	// Withdrawing a use case removes its support and the features only that
	// use case listed. A shared feature stays. A feature added on its own
	// stays. A rejected feature does not register that use case and does not
	// remove another use case's features. Re-registering with a smaller
	// feature list drops the feature that list no longer names. Diagnosis
	// keeps the heartbeat until that feature is gone.
	node, _ := openOutputNode(t)
	if err := node.AddEntity("EVSE", "na", ""); err != nil {
		t.Fatal(err)
	}
	RejectOutputServerFeatureForTest(t, "load control")
	rejected, err := node.RegisterUseCase("na", OutputUseCase{
		Actor: spinemodel.UseCaseActorTypeBattery, Name: spinemodel.UseCaseNameTypeMonitoringOfBattery, Version: "1.0.0", SubRevision: spinemodel.UseCaseDocumentSubRevisionRelease, Available: true,
		Features: []string{"device diagnosis", "load control"},
	})
	if err == nil || rejected != "load control" {
		t.Fatalf("rejected %q err %v", rejected, err)
	}
	if node.byLetter["na"].HasUseCaseSupport(spinemodel.UseCaseActorTypeBattery, spinemodel.UseCaseNameTypeMonitoringOfBattery) {
		t.Fatal("rejected use case was registered")
	}
	outputNodeFeatures(t, node, "na", nil)
	if node.byLetter["na"].HeartbeatManager().IsHeartbeatRunning() {
		t.Fatal("rejected diagnosis started the heartbeat")
	}
	if err := node.AddServerFeature("na", "device configuration"); err != nil {
		t.Fatal(err)
	}
	owned := OutputUseCase{Actor: spinemodel.UseCaseActorTypeEVSE, Name: spinemodel.UseCaseNameTypeEVSECommissioningAndConfiguration, Version: "1.0.1", SubRevision: spinemodel.UseCaseDocumentSubRevisionRelease, Available: true, Scenarios: []spinemodel.UseCaseScenarioSupportType{2}, Features: []string{"measurement", "device diagnosis"}}
	shared := OutputUseCase{Actor: spinemodel.UseCaseActorTypeMonitoredUnit, Name: spinemodel.UseCaseNameTypeMonitoringOfPowerConsumption, Version: "1.0.0", SubRevision: "draft", Available: false, Scenarios: []spinemodel.UseCaseScenarioSupportType{1, 5}, Features: []string{"measurement", "electrical connection"}}
	empty := OutputUseCase{Actor: spinemodel.UseCaseActorTypeConfigurationAppliance, Name: spinemodel.UseCaseNameTypeConfigurationOfDhwTemperature, Version: "1.0.0", SubRevision: spinemodel.UseCaseDocumentSubRevisionRelease, Available: true}
	for _, support := range []OutputUseCase{owned, shared, empty} {
		if _, err := node.RegisterUseCase("na", support); err != nil {
			t.Fatal(err)
		}
	}
	if !node.byLetter["na"].HeartbeatManager().IsHeartbeatRunning() {
		t.Fatal("diagnosis use case did not start the heartbeat")
	}
	outputNodeFeatures(t, node, "na", []string{"device configuration", "device diagnosis", "electrical connection", "measurement"})
	shared.Features = []string{"measurement"}
	shared.Version = "1.1.0"
	if _, err := node.RegisterUseCase("na", shared); err != nil {
		t.Fatal(err)
	}
	outputNodeExpectSupport(t, node, "na", shared)
	outputNodeFeatures(t, node, "na", []string{"device configuration", "device diagnosis", "measurement"})
	if err := node.WithdrawUseCase("na", owned.Actor, owned.Name); err != nil {
		t.Fatal(err)
	}
	if node.byLetter["na"].HasUseCaseSupport(owned.Actor, owned.Name) {
		t.Fatal("withdrawn support is still registered")
	}
	outputNodeExpectSupport(t, node, "na", shared)
	outputNodeExpectSupport(t, node, "na", empty)
	outputNodeFeatures(t, node, "na", []string{"device configuration", "measurement"})
	if node.byLetter["na"].HeartbeatManager().IsHeartbeatRunning() {
		t.Fatal("heartbeat kept running after diagnosis was withdrawn")
	}
	if err := node.WithdrawUseCase("na", shared.Actor, shared.Name); err != nil {
		t.Fatal(err)
	}
	outputNodeFeatures(t, node, "na", []string{"device configuration"})
	if err := node.WithdrawUseCase("na", empty.Actor, empty.Name); err != nil {
		t.Fatal(err)
	}
	if node.byLetter["na"].HasUseCaseSupport(empty.Actor, empty.Name) {
		t.Fatal("empty use case support remained")
	}
	if err := node.WithdrawUseCase("na", empty.Actor, empty.Name); err != nil {
		t.Fatal("second withdraw")
	}
	outputNodeFeatures(t, node, "na", []string{"device configuration"})
}

func outputNodeExpectSupport(t *testing.T, node *OutputNode, letter string, want OutputUseCase) {
	t.Helper()
	entity := node.byLetter[letter]
	data, err := spine.LocalFeatureDataCopyOfType[*spinemodel.NodeManagementUseCaseDataType](entity.Device().NodeManagement(), spinemodel.FunctionTypeNodeManagementUseCaseData)
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range data.UseCaseInformation {
		if info.Actor == nil || *info.Actor != want.Actor || info.Address == nil || !reflect.DeepEqual(info.Address.Entity, entity.Address().Entity) {
			continue
		}
		for _, support := range info.UseCaseSupport {
			if support.UseCaseName == nil || *support.UseCaseName != want.Name {
				continue
			}
			if support.UseCaseVersion == nil || string(*support.UseCaseVersion) != string(want.Version) || support.UseCaseDocumentSubRevision == nil || *support.UseCaseDocumentSubRevision != want.SubRevision || support.UseCaseAvailable == nil || *support.UseCaseAvailable != want.Available || !reflect.DeepEqual(support.ScenarioSupport, want.Scenarios) {
				t.Fatalf("support %+v want %+v", support, want)
			}
			return
		}
	}
	t.Fatalf("missing support %s %s", want.Actor, want.Name)
}
