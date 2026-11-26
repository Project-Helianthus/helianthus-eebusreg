package eebusfacade

import (
	"strings"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-eebus-go/usecases/cem/vabd"
	"github.com/Project-Helianthus/helianthus-eebus-go/usecases/cem/vapd"
	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
	"github.com/Project-Helianthus/helianthus-spine-go/spine"
)

type visualizationRig struct {
	*useCaseRig
	battery, pv *spine.EntityRemote
	feed        func(entity spineapi.EntityRemoteInterface, function spinemodel.FunctionType, data any, classifier spinemodel.CmdClassifierType)
	entities    func() map[string]RemoteMonitoringEntity
	fill        func(classifier spinemodel.CmdClassifierType)
}

func TestRemoteUseCasesVisualization(t *testing.T) {
	const battery, pv = "BatterySystem/visualizationOfAggregatedBatteryData", "PVSystem/visualizationOfAggregatedPhotovoltaicData"
	newRig := func(t *testing.T, name, ski string, batteryScenarios, pvScenarios []spinemodel.UseCaseScenarioSupportType) *visualizationRig {
		t.Helper()
		base := newUseCaseRig(t, name, ski, 9, nil)
		rig := &visualizationRig{useCaseRig: base}
		rig.battery = spine.NewEntityRemote(base.peer, spinemodel.EntityTypeTypeBatterySystem, []spinemodel.AddressEntityType{1})
		base.peer.AddEntity(rig.battery)
		rig.pv = spine.NewEntityRemote(base.peer, spinemodel.EntityTypeTypePVSystem, []spinemodel.AddressEntityType{2})
		base.peer.AddEntity(rig.pv)
		batteryFeature := spine.NewFeatureRemote(7, rig.battery, spinemodel.FeatureTypeTypeMeasurement, spinemodel.RoleTypeServer)
		rig.battery.AddFeature(batteryFeature)
		pvFeature := spine.NewFeatureRemote(8, rig.pv, spinemodel.FeatureTypeTypeMeasurement, spinemodel.RoleTypeServer)
		rig.pv.AddFeature(pvFeature)
		data := &spinemodel.NodeManagementUseCaseDataType{}
		if batteryScenarios != nil {
			data.AddUseCaseSupport(*batteryFeature.Address(), spinemodel.UseCaseActorTypeBatterySystem,
				spinemodel.UseCaseNameTypeVisualizationOfAggregatedBatteryData, "1.0.1", "RC1", true, batteryScenarios)
		}
		if pvScenarios != nil {
			data.AddUseCaseSupport(*pvFeature.Address(), spinemodel.UseCaseActorTypePVSystem,
				spinemodel.UseCaseNameTypeVisualizationOfAggregatedPhotovoltaicData, "1.0.1", "RC1", true, pvScenarios)
		}
		base.publishDirect(t, base.useCaseEvent(data, spinemodel.CmdClassifierTypeReply))
		rig.feed = func(entity spineapi.EntityRemoteInterface, function spinemodel.FunctionType, data any, classifier spinemodel.CmdClassifierType) {
			payload := base.dataEvent(function, data, classifier)
			payload.Entity = entity
			base.publishDirect(t, payload)
		}
		rig.entities = func() map[string]RemoteMonitoringEntity {
			out := map[string]RemoteMonitoringEntity{}
			for _, entity := range base.snapshot().Entities {
				out[entity.EntityType+"/"+entity.UseCase] = entity
			}
			return out
		}
		rig.fill = func(classifier spinemodel.CmdClassifierType) {
			rig.feed(rig.battery, spinemodel.FunctionTypeMeasurementDescriptionListData, &spinemodel.MeasurementDescriptionListDataType{
				MeasurementDescriptionData: []spinemodel.MeasurementDescriptionDataType{
					useCaseDescription(1, spinemodel.MeasurementTypeTypePower, spinemodel.ScopeTypeTypeACPowerTotal, spinemodel.UnitOfMeasurementTypeW),
					useCaseDescription(2, spinemodel.MeasurementTypeTypeEnergy, spinemodel.ScopeTypeTypeCharge, spinemodel.UnitOfMeasurementTypeWh),
					useCaseDescription(3, spinemodel.MeasurementTypeTypeEnergy, spinemodel.ScopeTypeTypeDischarge, spinemodel.UnitOfMeasurementTypeWh),
					useCaseDescription(4, spinemodel.MeasurementTypeTypePercentage, spinemodel.ScopeTypeTypeStateOfCharge, spinemodel.UnitOfMeasurementTypepct),
				}}, classifier)
			rig.feed(rig.battery, spinemodel.FunctionTypeMeasurementListData, &spinemodel.MeasurementListDataType{MeasurementData: []spinemodel.MeasurementDataType{
				useCaseValue(1, -2500, 0), useCaseValue(2, 8100, 0), useCaseValue(3, 7300, 0), useCaseValue(4, 64, 0),
			}}, classifier)
			rig.feed(rig.pv, spinemodel.FunctionTypeMeasurementDescriptionListData, &spinemodel.MeasurementDescriptionListDataType{
				MeasurementDescriptionData: []spinemodel.MeasurementDescriptionDataType{
					useCaseDescription(1, spinemodel.MeasurementTypeTypePower, spinemodel.ScopeTypeTypeACPowerTotal, spinemodel.UnitOfMeasurementTypeW),
					useCaseDescription(2, spinemodel.MeasurementTypeTypeEnergy, spinemodel.ScopeTypeTypeACYieldTotal, spinemodel.UnitOfMeasurementTypeWh),
				}}, classifier)
			rig.feed(rig.pv, spinemodel.FunctionTypeMeasurementListData, &spinemodel.MeasurementListDataType{MeasurementData: []spinemodel.MeasurementDataType{
				useCaseValue(1, 4200, 0), useCaseValue(2, 91, 6),
			}}, classifier)
			key, name := spinemodel.DeviceConfigurationKeyIdType(1), spinemodel.DeviceConfigurationKeyNameTypePeakPowerOfPVSystem
			rig.feed(rig.pv, spinemodel.FunctionTypeDeviceConfigurationKeyValueDescriptionListData,
				&spinemodel.DeviceConfigurationKeyValueDescriptionListDataType{DeviceConfigurationKeyValueDescriptionData: []spinemodel.DeviceConfigurationKeyValueDescriptionDataType{{KeyId: &key, KeyName: &name}}}, classifier)
			number, scale := spinemodel.NumberType(10), spinemodel.ScaleType(3)
			rig.feed(rig.pv, spinemodel.FunctionTypeDeviceConfigurationKeyValueListData,
				&spinemodel.DeviceConfigurationKeyValueListDataType{DeviceConfigurationKeyValueData: []spinemodel.DeviceConfigurationKeyValueDataType{{
					KeyId: &key, Value: &spinemodel.DeviceConfigurationKeyValueValueType{ScaledNumber: &spinemodel.ScaledNumberType{Number: &number, Scale: &scale}},
				}}}, classifier)
		}
		return rig
	}

	t.Run("serves every battery and photovoltaic scenario", func(t *testing.T) {
		rig := newRig(t, "v1", strings.Repeat("e", 40), scenarioRange(4), scenarioRange(3))
		rig.fill(spinemodel.CmdClassifierTypeReply)
		entities := rig.entities()
		if len(entities) != 2 || entities[battery].Actor != "BatterySystem" || entities[pv].Actor != "PVSystem" ||
			len(entities[battery].Scenarios) != 4 || len(entities[pv].Scenarios) != 3 {
			t.Fatalf("entities = %+v", entities)
		}
		wants := []struct {
			entity   string
			scenario uint
			name     string
			number   float64
			unit     string
		}{
			{battery, 1, "power", -2500, "W"}, {battery, 2, "energyCharged", 8100, "Wh"},
			{battery, 3, "energyDischarged", 7300, "Wh"}, {battery, 4, "stateOfCharge", 64, "%"},
			{pv, 1, "nominalPeakPower", 10000, "W"}, {pv, 2, "power", 4200, "W"}, {pv, 3, "yieldTotal", 91000000, "Wh"},
		}
		for _, expected := range wants {
			observation, ok := observationNamed(entities[expected.entity], expected.scenario, expected.name, "")
			if !ok {
				t.Errorf("%s scenario %d %s missing", expected.entity, expected.scenario, expected.name)
				continue
			}
			if observation.Kind != RemoteMonitoringKindNumber || observation.Number != expected.number || observation.Unit != expected.unit ||
				observation.Stale || observation.Source != RemoteMonitoringSourceReply || !observation.Updated.Equal(rig.clock.Now()) {
				t.Errorf("%s %s = %+v, want %+v", expected.entity, expected.name, observation, expected)
			}
		}
		if _, ok := observationNamed(entities[pv], 1, "power", ""); ok {
			t.Errorf("PV scenario 1 served a power value; it serves the nominal peak power only")
		}
	})

	t.Run("an absent or unannounced value is absent never zero", func(t *testing.T) {
		rig := newRig(t, "v2", strings.Repeat("f", 40), []spinemodel.UseCaseScenarioSupportType{1, 4}, scenarioRange(3))
		rig.fill(spinemodel.CmdClassifierTypeNotify)
		entities := rig.entities()
		for _, missing := range []struct {
			scenario uint
			name     string
		}{{2, "energyCharged"}, {3, "energyDischarged"}} {
			if _, ok := observationNamed(entities[battery], missing.scenario, missing.name, ""); ok {
				t.Errorf("battery %s served though scenario %d is not announced", missing.name, missing.scenario)
			}
		}
		if got, ok := observationNamed(entities[battery], 4, "stateOfCharge", ""); !ok || got.Source != RemoteMonitoringSourceNotify {
			t.Errorf("announced battery state of charge = %+v, %v", got, ok)
		}
		bare := newRig(t, "v3", strings.Repeat("a", 40), nil, scenarioRange(3))
		if got := bare.entities()[pv]; len(got.Observations) != 0 || len(got.Scenarios) != 3 {
			t.Fatalf("PV without data = %+v, want its scenarios and no observations", got)
		}
	})

	t.Run("values turn stale and keep their reply time", func(t *testing.T) {
		rig := newRig(t, "v4", strings.Repeat("b", 40), scenarioRange(4), scenarioRange(3))
		rig.fill(spinemodel.CmdClassifierTypeReply)
		repliedAt := rig.clock.Now()
		rig.clock.set(repliedAt.Add(time.Hour))
		rig.sessions.set(1, false)
		count := 0
		for _, entity := range rig.useCases.snapshot([]runtimeGraphObservation{rig.observation(false)}).Remotes[0].Entities {
			for _, observation := range entity.Observations {
				count++
				if !observation.Stale || !observation.Updated.Equal(repliedAt) || observation.Source != RemoteMonitoringSourceReply {
					t.Errorf("%s %s = %+v, want stale with the reply time", entity.UseCase, observation.Name, observation)
				}
			}
		}
		if count != 7 {
			t.Fatalf("retained observations = %d, want 7", count)
		}
	})

	t.Run("key varies entity use case scenario and remote", func(t *testing.T) {
		rig := newRig(t, "v5", strings.Repeat("c", 40), []spinemodel.UseCaseScenarioSupportType{1}, []spinemodel.UseCaseScenarioSupportType{2})
		rig.fill(spinemodel.CmdClassifierTypeReply)
		entities := rig.entities()
		batteryPower, _ := observationNamed(entities[battery], 1, "power", "")
		pvPower, _ := observationNamed(entities[pv], 2, "power", "")
		if batteryPower.Number != -2500 || pvPower.Number != 4200 {
			t.Errorf("same-named power values = battery %+v, pv %+v", batteryPower, pvPower)
		}
		if _, ok := observationNamed(entities[pv], 3, "yieldTotal", ""); ok {
			t.Errorf("PV yield served though scenario 3 is not announced")
		}
		other := newRig(t, "v6", strings.Repeat("d", 40), []spinemodel.UseCaseScenarioSupportType{1}, nil)
		other.feed(other.battery, spinemodel.FunctionTypeMeasurementDescriptionListData, &spinemodel.MeasurementDescriptionListDataType{
			MeasurementDescriptionData: []spinemodel.MeasurementDescriptionDataType{
				useCaseDescription(1, spinemodel.MeasurementTypeTypePower, spinemodel.ScopeTypeTypeACPowerTotal, spinemodel.UnitOfMeasurementTypeW)}}, spinemodel.CmdClassifierTypeReply)
		other.feed(other.battery, spinemodel.FunctionTypeMeasurementListData,
			&spinemodel.MeasurementListDataType{MeasurementData: []spinemodel.MeasurementDataType{useCaseValue(1, 777, 0)}}, spinemodel.CmdClassifierTypeReply)
		if got, _ := observationNamed(other.entities()[battery], 1, "power", ""); got.Number != 777 {
			t.Errorf("second remote battery power = %+v", got)
		}
		if got, _ := observationNamed(rig.entities()[battery], 1, "power", ""); got.Number != -2500 {
			t.Errorf("first remote battery power changed to %+v", got)
		}
	})

	t.Run("registers the battery and photovoltaic clients through the service", func(t *testing.T) {
		rig := newUseCaseRig(t, "v7", strings.Repeat("7", 40), 3, nil)
		var batteryClient, photovoltaicClient bool
		for _, useCase := range rig.service.added {
			switch useCase.(type) {
			case *vabd.VABD:
				batteryClient = true
			case *vapd.VAPD:
				photovoltaicClient = true
			}
		}
		if !batteryClient || !photovoltaicClient {
			t.Fatalf("registered battery = %v, photovoltaic = %v, want both", batteryClient, photovoltaicClient)
		}
	})
}
