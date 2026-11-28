package charger_test

import (
	"reflect"
	"testing"

	eebusruntime "github.com/Project-Helianthus/helianthus-eebusreg"
	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusservicebridge"
	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
	"github.com/Project-Helianthus/helianthus-spine-go/spine"
)

// wantRow is one measurement row joined to its electrical parameter.
type wantRow struct {
	scope, measure, unit              string
	number                            int64
	scale                             int8
	calculated                        bool
	acType, variant, phase, reference string
}

func TestChargerOutputMeasurementRows(t *testing.T) {
	// Each row is scope, measurement type, unit, scaled value and the electrical parameter.
	// 3600000 J is 1000 Wh consumed and -1000 Wh produced. The reported total is kept
	// ahead of the member sum. A second apply updates the value and does not add a row.
	binding, node, service := newChargerBinding(t)
	result := measurementLens()
	if loss := binding.Apply(result); len(loss) != 0 {
		t.Fatalf("apply loss %v", loss)
	}
	entity := entityAt(t, service, []spinemodel.AddressEntityType{2})
	assertConnection(t, entity)
	assertRows(t, entity, fullRows())
	assertChargerSupport(t, entity)
	if !node.HasUseCaseSupport("a", spinemodel.UseCaseActorTypeMonitoredUnit, spinemodel.UseCaseNameTypeMonitoringOfPowerConsumption) {
		t.Fatal("power monitoring is not registered")
	}
	if !node.HasUseCaseSupport("a", spinemodel.UseCaseActorTypeEVSE, spinemodel.UseCaseNameTypeEVSECommissioningAndConfiguration) {
		t.Fatal("commissioning is not registered")
	}
	result.Facts[0].Value.Coefficient = "18000"
	binding.Apply(result)
	updated := fullRows()
	updated[0].number = 18000
	assertRows(t, entity, updated)
}

func TestChargerOutputDerivedTotal(t *testing.T) {
	// Member-sum fields are withheld, coefficient, exponent and unit.
	// coefficient: "4" is the served number. A copied zero would serve 0.
	// exponent: 3 is the served scale. Dropping it would serve scale 0.
	// unit: unit.watt is required. unit.volt serves no total.
	// A later reported total replaces the calculated source.
	binding, _, service := newChargerBinding(t)
	result := eebusruntime.LensResult{
		Family: "charger", ResourceID: "bay", LensAdmitted: true, RuntimeEnabled: true,
		Facts: []eebusruntime.LensFact{
			lensQuantity("evse.ac.active_power", "a", "15", 2, "unit.watt"),
			lensQuantity("evse.ac.active_power", "b", "25", 2, "unit.watt"),
		},
		MemberSum: &eebusruntime.LensMemberSum{Coefficient: "4", Exponent: 3, Unit: "unit.watt"},
	}
	binding.Apply(result)
	entity := entityAt(t, service, []spinemodel.AddressEntityType{2})
	assertRows(t, entity, []wantRow{
		{scope: "acPowerTotal", measure: "power", unit: "W", number: 4, scale: 3, calculated: true, acType: "real"},
		{scope: "acPower", measure: "power", unit: "W", number: 15, scale: 2, acType: "real", variant: "rms", phase: "a", reference: "neutral"},
		{scope: "acPower", measure: "power", unit: "W", number: 25, scale: 2, acType: "real", variant: "rms", phase: "b", reference: "neutral"},
	})
	result.MemberSum.Coefficient = "8"
	result.MemberSum.Exponent = 2
	binding.Apply(result)
	if !hasRow(readRows(t, entity), wantRow{scope: "acPowerTotal", measure: "power", unit: "W", number: 8, scale: 2, calculated: true, acType: "real"}) {
		t.Fatalf("updated sum %v", readRows(t, entity))
	}
	result.Facts = append(result.Facts, lensQuantity("evse.ac.total_active_power", "", "9000", 0, "unit.watt"))
	binding.Apply(result)
	if !hasRow(readRows(t, entity), wantRow{scope: "acPowerTotal", measure: "power", unit: "W", number: 9000, scale: 0, acType: "real"}) {
		t.Fatalf("reported total kept the calculated source %v", readRows(t, entity))
	}
	volts := result
	volts.ResourceID = "other"
	volts.Facts = volts.Facts[:2]
	volts.MemberSum = &eebusruntime.LensMemberSum{Coefficient: "4", Exponent: 3, Unit: "unit.volt"}
	binding.Apply(volts)
	if rowScope(readRows(t, entityAt(t, service, []spinemodel.AddressEntityType{3})), "acPowerTotal") {
		t.Fatal("a volt member sum was served as the watt total")
	}
}

func TestChargerOutputWithheldTotal(t *testing.T) {
	// Withheld-sum fields are the reason, the not-included count and each asset reason.
	// reason: partial_cut is the named loss. An empty reason leaves it unnamed.
	// count: 3 is the served count. Using the gap length would serve 1.
	// asset and reason: the gap text is the asset and its reason.
	binding, _, service := newChargerBinding(t)
	result := eebusruntime.LensResult{
		Family: "charger", ResourceID: "bay", LensAdmitted: true, RuntimeEnabled: true,
		Facts: []eebusruntime.LensFact{lensQuantity("evse.ac.active_power", "a", "15", 0, "unit.watt")},
		MemberSum: &eebusruntime.LensMemberSum{
			Withheld: true, Reason: "partial_cut", NotIncludedCount: 3,
			NotIncluded: []eebusruntime.LensNotIncluded{{Asset: "asset-phase-c", Reason: "source_unavailable"}},
		},
	}
	binding.Apply(result)
	if rowScope(readRows(t, entityAt(t, service, []spinemodel.AddressEntityType{2})), "acPowerTotal") {
		t.Fatal("a withheld sum was served as a total")
	}
	gaps := binding.MonitoringLoss("bay")
	for _, want := range []string{"total active power", "partial_cut", "not included 3", "asset-phase-c source_unavailable"} {
		if !chargerHas(gaps, want) {
			t.Fatalf("missing %s in %v", want, gaps)
		}
	}
}

func TestChargerOutputFactFields(t *testing.T) {
	// Fact fields are element, phase, unit, kind, coefficient and exponent.
	// element: evse.ac.other is not a power row.
	// phase: n is not phase b.
	// unit: unit.volt is not watt.
	// kind: text is not a quantity.
	// coefficient: bad is not a scaled number.
	// exponent: 1000 does not fit the scale.
	// Phase a stays, so the mutation removes only the field it names.
	cases := []struct {
		name   string
		mutate func(*eebusruntime.LensFact)
	}{
		{"element", func(fact *eebusruntime.LensFact) { fact.Element = "evse.ac.other" }},
		{"phase", func(fact *eebusruntime.LensFact) { fact.Phase = "n" }},
		{"unit", func(fact *eebusruntime.LensFact) { fact.Unit = "unit.volt" }},
		{"kind", func(fact *eebusruntime.LensFact) { fact.Value.Kind = "text" }},
		{"coefficient", func(fact *eebusruntime.LensFact) { fact.Value.Coefficient = "bad" }},
		{"exponent", func(fact *eebusruntime.LensFact) { fact.Value.Exponent = 1000 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			binding, _, service := newChargerBinding(t)
			result := measurementLens()
			fact := phaseFact(&result, "b")
			tc.mutate(fact)
			binding.Apply(result)
			rows := readRows(t, entityAt(t, service, []spinemodel.AddressEntityType{2}))
			if hasRow(rows, wantRow{scope: "acPower", measure: "power", unit: "W", number: 25, scale: 2, acType: "real", variant: "rms", phase: "b", reference: "neutral"}) {
				t.Fatal("mutated phase b was served")
			}
			if !hasRow(rows, wantRow{scope: "acPower", measure: "power", unit: "W", number: 15, scale: 2, acType: "real", variant: "rms", phase: "a", reference: "neutral"}) {
				t.Fatal("phase a was dropped")
			}
		})
	}
}

func TestChargerOutputCommissioningState(t *testing.T) {
	// Table rows are fault present, idle without fault, not ready without fault,
	// charging without fault, and fault unknown. lastErrorCode stays unset.
	// idle would be standby on the generic table. not ready would be temporarilyNotReady.
	cases := []struct {
		name  string
		facts []eebusruntime.LensFact
		state string
	}{
		{"fault", []eebusruntime.LensFact{lensSymbol("evse.status.fault", "present"), lensSymbol("evse.status.readiness", "not_ready"), lensSymbol("evse.status.charging", "idle")}, "failure"},
		{"idle", []eebusruntime.LensFact{lensSymbol("evse.status.fault", "clear"), lensSymbol("evse.status.readiness", "ready"), lensSymbol("evse.status.charging", "idle"), lensSymbol("evse.status.connection", "connected")}, "normalOperation"},
		{"not ready", []eebusruntime.LensFact{lensSymbol("evse.status.fault", "clear"), lensSymbol("evse.status.readiness", "not_ready"), lensSymbol("evse.status.charging", "charging")}, "normalOperation"},
		{"charging", []eebusruntime.LensFact{lensSymbol("evse.status.fault", "clear"), lensSymbol("evse.status.readiness", "ready"), lensSymbol("evse.status.charging", "charging")}, "normalOperation"},
		{"unknown", []eebusruntime.LensFact{lensSymbol("evse.status.charging", "idle"), lensSymbol("evse.status.connection", "connected")}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			binding, _, service := newChargerBinding(t)
			result := eebusruntime.LensResult{
				Family: "charger", ResourceID: "bay", LensAdmitted: true, RuntimeEnabled: true,
				Elements: []string{"operating state"}, Facts: tc.facts,
			}
			binding.Apply(result)
			state := diagnosisState(t, entityAt(t, service, []spinemodel.AddressEntityType{2}))
			if state == nil || state.LastErrorCode != nil {
				t.Fatalf("state %+v", state)
			}
			if tc.state == "" {
				if state.OperatingState != nil {
					t.Fatalf("unknown fault served %s", *state.OperatingState)
				}
				return
			}
			if state.OperatingState == nil || string(*state.OperatingState) != tc.state {
				t.Fatalf("state %+v", state)
			}
		})
	}
}

func measurementLens() eebusruntime.LensResult {
	return eebusruntime.LensResult{
		Family: "charger", ResourceID: "bay", LensAdmitted: true, RuntimeEnabled: true,
		Facts: []eebusruntime.LensFact{
			lensQuantity("evse.ac.total_active_power", "", "9000", 0, "unit.watt"),
			lensQuantity("evse.ac.active_power", "a", "15", 2, "unit.watt"),
			lensQuantity("evse.ac.active_power", "b", "25", 2, "unit.watt"),
			lensQuantity("evse.ac.active_power", "c", "5", 0, "unit.watt"),
			lensQuantity("evse.ac.current", "a", "11", 0, "unit.ampere"),
			lensQuantity("evse.ac.current", "b", "12", 0, "unit.ampere"),
			lensQuantity("evse.ac.current", "c", "13", 0, "unit.ampere"),
			lensQuantity("evse.ac.voltage", "a", "230", 0, "unit.volt"),
			lensQuantity("evse.ac.voltage", "b", "231", 0, "unit.volt"),
			lensQuantity("evse.ac.voltage", "c", "232", 0, "unit.volt"),
			lensQuantity("evse.ac.frequency", "", "50", 0, "unit.hertz"),
			lensQuantity("evse.energy.lifetime", "", "3600000", 0, "unit.joule"),
			lensQuantity("evse.energy.lifetime_exported", "", "3600000", 0, "unit.joule"),
			lensSymbol("evse.status.fault", "clear"),
		},
		MemberSum: &eebusruntime.LensMemberSum{Coefficient: "4", Exponent: 3, Unit: "unit.watt"},
	}
}

func fullRows() []wantRow {
	return []wantRow{
		{scope: "acPowerTotal", measure: "power", unit: "W", number: 9000, acType: "real"},
		{scope: "acPower", measure: "power", unit: "W", number: 15, scale: 2, acType: "real", variant: "rms", phase: "a", reference: "neutral"},
		{scope: "acPower", measure: "power", unit: "W", number: 25, scale: 2, acType: "real", variant: "rms", phase: "b", reference: "neutral"},
		{scope: "acPower", measure: "power", unit: "W", number: 5, acType: "real", variant: "rms", phase: "c", reference: "neutral"},
		{scope: "acCurrent", measure: "current", unit: "A", number: 11, acType: "real", variant: "rms", phase: "a"},
		{scope: "acCurrent", measure: "current", unit: "A", number: 12, acType: "real", variant: "rms", phase: "b"},
		{scope: "acCurrent", measure: "current", unit: "A", number: 13, acType: "real", variant: "rms", phase: "c"},
		{scope: "acVoltage", measure: "voltage", unit: "V", number: 230, acType: "apparent", variant: "rms", phase: "a", reference: "neutral"},
		{scope: "acVoltage", measure: "voltage", unit: "V", number: 231, acType: "apparent", variant: "rms", phase: "b", reference: "neutral"},
		{scope: "acVoltage", measure: "voltage", unit: "V", number: 232, acType: "apparent", variant: "rms", phase: "c", reference: "neutral"},
		{scope: "acFrequency", measure: "frequency", unit: "Hz", number: 50},
		{scope: "acEnergyConsumed", measure: "energy", unit: "Wh", number: 1000, acType: "real"},
		{scope: "acEnergyProduced", measure: "energy", unit: "Wh", number: -1000, acType: "real"},
	}
}

func lensQuantity(element, phase, coefficient string, exponent int32, unit string) eebusruntime.LensFact {
	return eebusruntime.LensFact{
		Element: element, Phase: phase, Unit: unit,
		Value: eebusruntime.LensValue{Kind: "quantity", Coefficient: coefficient, Exponent: exponent},
	}
}

func lensSymbol(element, symbol string) eebusruntime.LensFact {
	return eebusruntime.LensFact{Element: element, Value: eebusruntime.LensValue{Kind: "symbol", Symbol: symbol}}
}

func phaseFact(result *eebusruntime.LensResult, phase string) *eebusruntime.LensFact {
	for index := range result.Facts {
		if result.Facts[index].Element == "evse.ac.active_power" && result.Facts[index].Phase == phase {
			return &result.Facts[index]
		}
	}
	return nil
}

type measuredRow struct {
	scope, measure, unit, commodity, valueType, source string
	number                                             int64
	scale                                              int8
	acType, variant, phase, reference, voltage         string
	connection                                         int64
}

func entityAt(t *testing.T, service *eebusservicebridge.Service, numbers []spinemodel.AddressEntityType) spineapi.EntityLocalInterface {
	t.Helper()
	device := service.LocalDevice()
	if device == nil {
		t.Fatal("local device is absent")
	}
	for _, entity := range device.Entities() {
		address := entity.Address()
		if address != nil && reflect.DeepEqual([]spinemodel.AddressEntityType(address.Entity), numbers) {
			return entity
		}
	}
	t.Fatalf("no entity %v", numbers)
	return nil
}

func featureData[T any](t *testing.T, entity spineapi.EntityLocalInterface, featureType spinemodel.FeatureTypeType, function spinemodel.FunctionType) T {
	t.Helper()
	feature := entity.FeatureOfTypeAndRole(featureType, spinemodel.RoleTypeServer)
	if feature == nil {
		t.Fatalf("no %s feature", featureType)
	}
	data, err := spine.LocalFeatureDataCopyOfType[T](feature, function)
	if err != nil || any(data) == nil {
		t.Fatalf("%s %s: %v", featureType, function, err)
	}
	return data
}

func assertConnection(t *testing.T, entity spineapi.EntityLocalInterface) {
	t.Helper()
	list := featureData[*spinemodel.ElectricalConnectionDescriptionListDataType](t, entity, spinemodel.FeatureTypeTypeElectricalConnection, spinemodel.FunctionTypeElectricalConnectionDescriptionListData)
	if len(list.ElectricalConnectionDescriptionData) != 1 {
		t.Fatalf("connections %+v", list.ElectricalConnectionDescriptionData)
	}
	got := list.ElectricalConnectionDescriptionData[0]
	if got.ElectricalConnectionId == nil || *got.ElectricalConnectionId != 1 || got.PowerSupplyType == nil || *got.PowerSupplyType != spinemodel.ElectricalConnectionVoltageTypeTypeAc || got.PositiveEnergyDirection == nil || *got.PositiveEnergyDirection != spinemodel.EnergyDirectionTypeConsume {
		t.Fatalf("connection %+v", got)
	}
}

func assertRows(t *testing.T, entity spineapi.EntityLocalInterface, want []wantRow) {
	t.Helper()
	rows := readRows(t, entity)
	if len(rows) != len(want) {
		t.Fatalf("rows %d want %d: %+v", len(rows), len(want), rows)
	}
	for _, item := range want {
		if !hasRow(rows, item) {
			t.Fatalf("missing %+v in %+v", item, rows)
		}
	}
}

func hasRow(rows []measuredRow, want wantRow) bool {
	source := ""
	if want.calculated {
		source = string(spinemodel.MeasurementValueSourceTypeCalculatedValue)
	}
	for _, got := range rows {
		if got.scope == want.scope && got.measure == want.measure && got.unit == want.unit && got.number == want.number && got.scale == want.scale && got.source == source && got.commodity == string(spinemodel.CommodityTypeTypeElectricity) && got.valueType == string(spinemodel.MeasurementValueTypeTypeValue) && got.acType == want.acType && got.variant == want.variant && got.phase == want.phase && got.reference == want.reference && got.voltage == string(spinemodel.ElectricalConnectionVoltageTypeTypeAc) && got.connection == 1 {
			return true
		}
	}
	return false
}

func rowScope(rows []measuredRow, scope string) bool {
	for _, got := range rows {
		if got.scope == scope {
			return true
		}
	}
	return false
}

func readRows(t *testing.T, entity spineapi.EntityLocalInterface) []measuredRow {
	t.Helper()
	descriptions := featureData[*spinemodel.MeasurementDescriptionListDataType](t, entity, spinemodel.FeatureTypeTypeMeasurement, spinemodel.FunctionTypeMeasurementDescriptionListData)
	parameters := featureData[*spinemodel.ElectricalConnectionParameterDescriptionListDataType](t, entity, spinemodel.FeatureTypeTypeElectricalConnection, spinemodel.FunctionTypeElectricalConnectionParameterDescriptionListData)
	values := featureData[*spinemodel.MeasurementListDataType](t, entity, spinemodel.FeatureTypeTypeMeasurement, spinemodel.FunctionTypeMeasurementListData)
	byParameter := map[spinemodel.MeasurementIdType]spinemodel.ElectricalConnectionParameterDescriptionDataType{}
	for _, parameter := range parameters.ElectricalConnectionParameterDescriptionData {
		if parameter.MeasurementId == nil {
			t.Fatalf("parameter without a measurement %+v", parameter)
		}
		byParameter[*parameter.MeasurementId] = parameter
	}
	byValue := map[spinemodel.MeasurementIdType]spinemodel.MeasurementDataType{}
	for _, value := range values.MeasurementData {
		if value.MeasurementId == nil {
			t.Fatalf("value without a measurement %+v", value)
		}
		byValue[*value.MeasurementId] = value
	}
	rows := make([]measuredRow, 0, len(descriptions.MeasurementDescriptionData))
	for _, description := range descriptions.MeasurementDescriptionData {
		if description.MeasurementId == nil {
			t.Fatalf("description without an id %+v", description)
		}
		parameter, ok := byParameter[*description.MeasurementId]
		value, valueOK := byValue[*description.MeasurementId]
		if !ok || !valueOK || value.Value == nil || value.Value.Number == nil || value.Value.Scale == nil {
			t.Fatalf("unjoined measurement %d", *description.MeasurementId)
		}
		row := measuredRow{
			scope: textOf(description.ScopeType), measure: textOf(description.MeasurementType), unit: textOf(description.Unit),
			commodity: textOf(description.CommodityType), valueType: textOf(value.ValueType), source: textOf(value.ValueSource),
			number: int64(*value.Value.Number), scale: int8(*value.Value.Scale),
			acType: textOf(parameter.AcMeasurementType), variant: textOf(parameter.AcMeasurementVariant),
			phase: textOf(parameter.AcMeasuredPhases), reference: textOf(parameter.AcMeasuredInReferenceTo),
			voltage: textOf(parameter.VoltageType),
		}
		if parameter.ElectricalConnectionId != nil {
			row.connection = int64(*parameter.ElectricalConnectionId)
		}
		rows = append(rows, row)
	}
	return rows
}

func textOf[T ~string](value *T) string {
	if value == nil {
		return ""
	}
	return string(*value)
}

func diagnosisState(t *testing.T, entity spineapi.EntityLocalInterface) *spinemodel.DeviceDiagnosisStateDataType {
	t.Helper()
	return featureData[*spinemodel.DeviceDiagnosisStateDataType](t, entity, spinemodel.FeatureTypeTypeDeviceDiagnosis, spinemodel.FunctionTypeDeviceDiagnosisStateData)
}

func assertChargerSupport(t *testing.T, entity spineapi.EntityLocalInterface) {
	t.Helper()
	device := entity.Device()
	if device == nil || device.NodeManagement() == nil {
		t.Fatal("node management is absent")
	}
	data, err := spine.LocalFeatureDataCopyOfType[*spinemodel.NodeManagementUseCaseDataType](device.NodeManagement(), spinemodel.FunctionTypeNodeManagementUseCaseData)
	if err != nil || data == nil {
		t.Fatal(err)
	}
	assertSupport(t, data, entity, spinemodel.UseCaseActorTypeMonitoredUnit, spinemodel.UseCaseNameTypeMonitoringOfPowerConsumption, "1.0.0", []spinemodel.UseCaseScenarioSupportType{1, 2, 3, 4, 5})
	assertSupport(t, data, entity, spinemodel.UseCaseActorTypeEVSE, spinemodel.UseCaseNameTypeEVSECommissioningAndConfiguration, "1.0.1", []spinemodel.UseCaseScenarioSupportType{1, 2})
}

func assertSupport(t *testing.T, data *spinemodel.NodeManagementUseCaseDataType, entity spineapi.EntityLocalInterface, actor spinemodel.UseCaseActorType, name spinemodel.UseCaseNameType, version string, scenarios []spinemodel.UseCaseScenarioSupportType) {
	t.Helper()
	for _, info := range data.UseCaseInformation {
		if info.Actor == nil || *info.Actor != actor || info.Address == nil || !reflect.DeepEqual(info.Address.Entity, entity.Address().Entity) {
			continue
		}
		for _, support := range info.UseCaseSupport {
			if support.UseCaseName == nil || *support.UseCaseName != name {
				continue
			}
			if support.UseCaseVersion == nil || string(*support.UseCaseVersion) != version || support.UseCaseDocumentSubRevision == nil || *support.UseCaseDocumentSubRevision != spinemodel.UseCaseDocumentSubRevisionRelease || support.UseCaseAvailable == nil || !*support.UseCaseAvailable || !reflect.DeepEqual(support.ScenarioSupport, scenarios) {
				t.Fatalf("support %+v", support)
			}
			return
		}
	}
	t.Fatalf("missing support %s %s", actor, name)
}
