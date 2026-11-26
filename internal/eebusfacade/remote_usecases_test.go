package eebusfacade

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	eebusapi "github.com/Project-Helianthus/helianthus-eebus-go/api"
	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
	"github.com/Project-Helianthus/helianthus-spine-go/spine"
	"github.com/Project-Helianthus/helianthus-spine-go/util"
)

type useCaseService struct {
	*fakeRuntimeService
	added []eebusapi.UseCaseInterface
}

func (service *useCaseService) AddUseCase(useCase eebusapi.UseCaseInterface) {
	service.added = append(service.added, useCase)
	useCase.AddFeatures()
	useCase.AddUseCase()
}

type useCaseCall struct {
	class       spinemodel.CmdClassifierType
	source      string
	destination string
}

type useCaseSender struct {
	spineapi.SenderInterface
	mu    sync.Mutex
	calls []useCaseCall
}

func (sender *useCaseSender) record(class spinemodel.CmdClassifierType, source, destination *spinemodel.FeatureAddressType) *spinemodel.MsgCounterType {
	sender.mu.Lock()
	defer sender.mu.Unlock()
	sender.calls = append(sender.calls, useCaseCall{class: class, source: source.String(), destination: destination.String()})
	return util.Ptr(spinemodel.MsgCounterType(len(sender.calls)))
}

func (sender *useCaseSender) Request(
	class spinemodel.CmdClassifierType, source, destination *spinemodel.FeatureAddressType, _ bool, _ []spinemodel.CmdType,
) (*spinemodel.MsgCounterType, error) {
	return sender.record(class, source, destination), nil
}

func (sender *useCaseSender) Subscribe(
	source, destination *spinemodel.FeatureAddressType, _ spinemodel.FeatureTypeType,
) (*spinemodel.MsgCounterType, error) {
	return sender.record(spinemodel.CmdClassifierTypeCall, source, destination), nil
}

func (sender *useCaseSender) snapshot() []useCaseCall {
	sender.mu.Lock()
	defer sender.mu.Unlock()
	return append([]useCaseCall(nil), sender.calls...)
}

type useCaseSessions struct {
	mu    sync.Mutex
	index uint64
	up    bool
}

func (sessions *useCaseSessions) get(string) (uint64, bool) {
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	return sessions.index, sessions.up
}

func (sessions *useCaseSessions) set(index uint64, up bool) {
	sessions.mu.Lock()
	sessions.index, sessions.up = index, up
	sessions.mu.Unlock()
}

type useCaseRig struct {
	local    *spine.DeviceLocal
	service  *useCaseService
	peer     *spine.DeviceRemote
	entity   *spine.EntityRemote
	sender   *useCaseSender
	sessions *useCaseSessions
	clock    *remoteSubscriptionClock
	useCases *remoteUseCases
	ski      string
	address  string
}

func newUseCaseRig(t *testing.T, name, ski string, entityID spinemodel.AddressEntityType, announce func(*spinemodel.NodeManagementUseCaseDataType, spinemodel.FeatureAddressType)) *useCaseRig {
	t.Helper()
	rig := &useCaseRig{
		sender: &useCaseSender{}, sessions: &useCaseSessions{index: 1, up: true}, ski: ski, address: "peer-" + name,
		clock: &remoteSubscriptionClock{now: time.Date(2030, 5, 6, 7, 8, 9, 0, time.UTC)},
	}
	rig.local = spine.NewDeviceLocal("", "", "", "", "local-"+name, spinemodel.DeviceTypeTypeEnergyManagementSystem, "")
	cem := spine.NewEntityLocal(rig.local, spinemodel.EntityTypeTypeCEM, []spinemodel.AddressEntityType{1}, 0)
	rig.local.AddEntity(cem)
	rig.service = &useCaseService{fakeRuntimeService: &fakeRuntimeService{started: make(chan struct{}), localDevice: rig.local}}
	rig.peer = spine.NewDeviceRemote(rig.local, ski, rig.sender)
	rig.peer.UpdateDevice(&spinemodel.NetworkManagementDeviceDescriptionDataType{
		DeviceAddress: &spinemodel.DeviceAddressType{Device: util.Ptr(spinemodel.AddressDeviceType(rig.address))},
		DeviceType:    util.Ptr(spinemodel.DeviceTypeTypeElectricitySupplySystem),
	})
	rig.entity = spine.NewEntityRemote(rig.peer, spinemodel.EntityTypeTypeGridConnectionPointOfPremises, []spinemodel.AddressEntityType{entityID})
	rig.peer.AddEntity(rig.entity)
	rig.local.AddRemoteDeviceForSki(ski, rig.peer)
	server := spine.NewFeatureRemote(5, rig.entity, spinemodel.FeatureTypeTypeDeviceConfiguration, spinemodel.RoleTypeServer)
	server.SetOperations([]spinemodel.FunctionPropertyType{
		{
			Function:           util.Ptr(spinemodel.FunctionTypeDeviceConfigurationKeyValueDescriptionListData),
			PossibleOperations: &spinemodel.PossibleOperationsType{Read: &spinemodel.PossibleOperationsReadType{}},
		},
		{
			Function:           util.Ptr(spinemodel.FunctionTypeDeviceConfigurationKeyValueListData),
			PossibleOperations: &spinemodel.PossibleOperationsType{Read: &spinemodel.PossibleOperationsReadType{}},
		},
	})
	rig.entity.AddFeature(server)
	useCases, err := registerRemoteUseCases(rig.service, rig.clock.Now, nil, rig.sessions.get)
	if err != nil || useCases == nil {
		t.Fatalf("registerRemoteUseCases() = %v, %v", useCases, err)
	}
	rig.useCases = useCases
	t.Cleanup(useCases.stop)
	if announce != nil {
		data := &spinemodel.NodeManagementUseCaseDataType{}
		announce(data, *server.Address())
		rig.publishDirect(t, rig.useCaseEvent(data, spinemodel.CmdClassifierTypeReply))
	}
	return rig
}

func (rig *useCaseRig) useCaseEvent(data *spinemodel.NodeManagementUseCaseDataType, classifier spinemodel.CmdClassifierType) spineapi.EventPayload {
	node := rig.peer.Entity(spine.DeviceInformationAddressEntity).FeatureOfTypeAndRole(
		spinemodel.FeatureTypeTypeNodeManagement, spinemodel.RoleTypeSpecial,
	)
	return spineapi.EventPayload{
		Ski: rig.ski, EventType: spineapi.EventTypeDataChange, ChangeType: spineapi.ElementChangeUpdate,
		Device: rig.peer, Entity: node.Entity(), Feature: node, CmdClassifier: &classifier, Data: data,
	}
}

func (rig *useCaseRig) dataEvent(function spinemodel.FunctionType, data any, classifier spinemodel.CmdClassifierType) spineapi.EventPayload {
	return spineapi.EventPayload{
		Ski: rig.ski, EventType: spineapi.EventTypeDataChange, ChangeType: spineapi.ElementChangeUpdate,
		Device: rig.peer, Entity: rig.entity, Function: function, CmdClassifier: &classifier, Data: data,
	}
}

func (rig *useCaseRig) publishDirect(t *testing.T, payload spineapi.EventPayload) {
	t.Helper()
	rig.useCases.HandleEvent(payload)
}

func (rig *useCaseRig) observation(connected bool) runtimeGraphObservation {
	index, up := rig.sessions.get(rig.ski)
	state := "disconnected"
	if connected && up {
		state = "connected"
	}
	return runtimeGraphObservation{
		RemoteSKI: rig.ski, SessionState: state, SessionIndex: index, Paired: true, PairingState: "paired",
		ShipID: "ship-" + rig.ski[:4],
	}
}

func (rig *useCaseRig) snapshot() RemoteMonitoringRemote {
	snapshot := rig.useCases.snapshot([]runtimeGraphObservation{rig.observation(true)})
	return snapshot.Remotes[0]
}

func useCaseDescription(id uint, measurement spinemodel.MeasurementTypeType, scope spinemodel.ScopeTypeType, unit spinemodel.UnitOfMeasurementType) spinemodel.MeasurementDescriptionDataType {
	identifier := spinemodel.MeasurementIdType(id)
	commodity := spinemodel.CommodityTypeTypeElectricity
	description := spinemodel.MeasurementDescriptionDataType{
		MeasurementId: &identifier, MeasurementType: &measurement, CommodityType: &commodity, ScopeType: &scope,
	}
	if unit != "" {
		description.Unit = &unit
	}
	return description
}

func useCaseValue(id uint, number int64, scale int) spinemodel.MeasurementDataType {
	identifier := spinemodel.MeasurementIdType(id)
	n, s := spinemodel.NumberType(number), spinemodel.ScaleType(scale)
	return spinemodel.MeasurementDataType{MeasurementId: &identifier, Value: &spinemodel.ScaledNumberType{Number: &n, Scale: &s}}
}

func useCasePhase(id uint, phase spinemodel.ElectricalConnectionPhaseNameType) spinemodel.ElectricalConnectionParameterDescriptionDataType {
	identifier := spinemodel.MeasurementIdType(id)
	return spinemodel.ElectricalConnectionParameterDescriptionDataType{MeasurementId: &identifier, AcMeasuredPhases: &phase}
}

func announceBoth(scenariosGrid, scenariosPower []spinemodel.UseCaseScenarioSupportType) func(*spinemodel.NodeManagementUseCaseDataType, spinemodel.FeatureAddressType) {
	return func(data *spinemodel.NodeManagementUseCaseDataType, address spinemodel.FeatureAddressType) {
		if scenariosGrid != nil {
			data.AddUseCaseSupport(address, spinemodel.UseCaseActorTypeGridConnectionPoint,
				spinemodel.UseCaseNameTypeMonitoringOfGridConnectionPoint, "1.0.0", "release", true, scenariosGrid)
		}
		if scenariosPower != nil {
			data.AddUseCaseSupport(address, spinemodel.UseCaseActorTypeMonitoredUnit,
				spinemodel.UseCaseNameTypeMonitoringOfPowerConsumption, "1.0.0", "release", true, scenariosPower)
		}
	}
}

func scenarioRange(count int) []spinemodel.UseCaseScenarioSupportType {
	out := make([]spinemodel.UseCaseScenarioSupportType, 0, count)
	for scenario := 1; scenario <= count; scenario++ {
		out = append(out, spinemodel.UseCaseScenarioSupportType(scenario))
	}
	return out
}

func feedUseCaseData(t *testing.T, rig *useCaseRig, classifier spinemodel.CmdClassifierType) {
	t.Helper()
	descriptions := &spinemodel.MeasurementDescriptionListDataType{MeasurementDescriptionData: []spinemodel.MeasurementDescriptionDataType{
		useCaseDescription(1, spinemodel.MeasurementTypeTypePower, spinemodel.ScopeTypeTypeACPowerTotal, spinemodel.UnitOfMeasurementTypeW),
		useCaseDescription(2, spinemodel.MeasurementTypeTypeEnergy, spinemodel.ScopeTypeTypeGridFeedIn, spinemodel.UnitOfMeasurementTypeWh),
		useCaseDescription(3, spinemodel.MeasurementTypeTypeEnergy, spinemodel.ScopeTypeTypeGridConsumption, spinemodel.UnitOfMeasurementTypeWh),
		useCaseDescription(4, spinemodel.MeasurementTypeTypeCurrent, spinemodel.ScopeTypeTypeACCurrent, spinemodel.UnitOfMeasurementTypeA),
		useCaseDescription(5, spinemodel.MeasurementTypeTypeCurrent, spinemodel.ScopeTypeTypeACCurrent, spinemodel.UnitOfMeasurementTypeA),
		useCaseDescription(6, spinemodel.MeasurementTypeTypeVoltage, spinemodel.ScopeTypeTypeACVoltage, spinemodel.UnitOfMeasurementTypeV),
		useCaseDescription(7, spinemodel.MeasurementTypeTypeFrequency, spinemodel.ScopeTypeTypeACFrequency, spinemodel.UnitOfMeasurementTypeHz),
		useCaseDescription(8, spinemodel.MeasurementTypeTypePower, spinemodel.ScopeTypeTypeACPower, spinemodel.UnitOfMeasurementTypeW),
		useCaseDescription(9, spinemodel.MeasurementTypeTypeEnergy, spinemodel.ScopeTypeTypeACEnergyConsumed, spinemodel.UnitOfMeasurementTypeWh),
		useCaseDescription(10, spinemodel.MeasurementTypeTypeEnergy, spinemodel.ScopeTypeTypeACEnergyProduced, spinemodel.UnitOfMeasurementTypeWh),
	}}
	phases := &spinemodel.ElectricalConnectionParameterDescriptionListDataType{
		ElectricalConnectionParameterDescriptionData: []spinemodel.ElectricalConnectionParameterDescriptionDataType{
			useCasePhase(4, spinemodel.ElectricalConnectionPhaseNameTypeA),
			useCasePhase(5, spinemodel.ElectricalConnectionPhaseNameTypeB),
			useCasePhase(6, spinemodel.ElectricalConnectionPhaseNameTypeC),
			useCasePhase(8, spinemodel.ElectricalConnectionPhaseNameTypeA),
		},
	}
	values := &spinemodel.MeasurementListDataType{MeasurementData: []spinemodel.MeasurementDataType{
		useCaseValue(1, 1500, 0), useCaseValue(2, 70, 1), useCaseValue(3, 4200, 0), useCaseValue(4, 12, -1),
		useCaseValue(5, 34, -1), useCaseValue(6, 2301, -1), useCaseValue(7, 5001, -2), useCaseValue(8, 480, 0),
		useCaseValue(9, 9000, 0), useCaseValue(10, 15, 2),
	}}
	key := spinemodel.DeviceConfigurationKeyIdType(3)
	keyName := spinemodel.DeviceConfigurationKeyNameTypePvCurtailmentLimitFactor
	keys := &spinemodel.DeviceConfigurationKeyValueDescriptionListDataType{
		DeviceConfigurationKeyValueDescriptionData: []spinemodel.DeviceConfigurationKeyValueDescriptionDataType{{KeyId: &key, KeyName: &keyName}},
	}
	number, scale := spinemodel.NumberType(70), spinemodel.ScaleType(0)
	factor := &spinemodel.DeviceConfigurationKeyValueListDataType{
		DeviceConfigurationKeyValueData: []spinemodel.DeviceConfigurationKeyValueDataType{{
			KeyId: &key, Value: &spinemodel.DeviceConfigurationKeyValueValueType{ScaledNumber: &spinemodel.ScaledNumberType{Number: &number, Scale: &scale}},
		}},
	}
	rig.publishDirect(t, rig.dataEvent(spinemodel.FunctionTypeMeasurementDescriptionListData, descriptions, classifier))
	rig.publishDirect(t, rig.dataEvent(spinemodel.FunctionTypeElectricalConnectionParameterDescriptionListData, phases, classifier))
	rig.publishDirect(t, rig.dataEvent(spinemodel.FunctionTypeDeviceConfigurationKeyValueDescriptionListData, keys, classifier))
	rig.publishDirect(t, rig.dataEvent(spinemodel.FunctionTypeMeasurementListData, values, classifier))
	rig.publishDirect(t, rig.dataEvent(spinemodel.FunctionTypeDeviceConfigurationKeyValueListData, factor, classifier))
}

type useCaseKey struct {
	useCase  string
	scenario uint
	name     string
	phase    string
}

func useCaseObservationsByKey(t *testing.T, remote RemoteMonitoringRemote) map[useCaseKey]RemoteMonitoringObservation {
	t.Helper()
	out := map[useCaseKey]RemoteMonitoringObservation{}
	for _, entity := range remote.Entities {
		for _, observation := range entity.Observations {
			key := useCaseKey{useCase: entity.UseCase, scenario: observation.Scenario, name: observation.Name, phase: observation.Phase}
			if _, duplicate := out[key]; duplicate {
				t.Fatalf("duplicate observation %+v", key)
			}
			out[key] = observation
		}
	}
	return out
}

func TestRemoteUseCasesServeEveryGridAndPowerScenario(t *testing.T) {
	rig := newUseCaseRig(t, "a", strings.Repeat("a", 40), 3, announceBoth(scenarioRange(7), scenarioRange(5)))
	feedUseCaseData(t, rig, spinemodel.CmdClassifierTypeReply)
	remote := rig.snapshot()
	if len(remote.Entities) != 2 {
		t.Fatalf("entities = %+v, want one per announced use case", remote.Entities)
	}
	grid := string(spinemodel.UseCaseNameTypeMonitoringOfGridConnectionPoint)
	power := string(spinemodel.UseCaseNameTypeMonitoringOfPowerConsumption)
	want := []struct {
		key    useCaseKey
		number float64
		unit   string
	}{
		{useCaseKey{grid, 1, "powerLimitationFactor", ""}, 70, "%"},
		{useCaseKey{grid, 2, "power", ""}, 1500, "W"},
		{useCaseKey{grid, 3, "energyFeedIn", ""}, 700, "Wh"},
		{useCaseKey{grid, 4, "energyConsumed", ""}, 4200, "Wh"},
		{useCaseKey{grid, 5, "currentPerPhase", "a"}, 1.2, "A"},
		{useCaseKey{grid, 5, "currentPerPhase", "b"}, 3.4, "A"},
		{useCaseKey{grid, 6, "voltagePerPhase", "c"}, 230.1, "V"},
		{useCaseKey{grid, 7, "frequency", ""}, 50.01, "Hz"},
		{useCaseKey{power, 1, "power", ""}, 1500, "W"},
		{useCaseKey{power, 1, "powerPerPhase", "a"}, 480, "W"},
		{useCaseKey{power, 2, "energyConsumed", ""}, 9000, "Wh"},
		{useCaseKey{power, 2, "energyProduced", ""}, 1500, "Wh"},
		{useCaseKey{power, 3, "currentPerPhase", "a"}, 1.2, "A"},
		{useCaseKey{power, 4, "voltagePerPhase", "c"}, 230.1, "V"},
		{useCaseKey{power, 5, "frequency", ""}, 50.01, "Hz"},
	}
	got := useCaseObservationsByKey(t, remote)
	if len(got) != len(want)+1 {
		t.Fatalf("observation count = %d, want %d (power-use-case current phase b is served too): %+v", len(got), len(want)+1, got)
	}
	for _, expected := range want {
		observation, ok := got[expected.key]
		if !ok {
			t.Errorf("%+v missing", expected.key)
			continue
		}
		if diff := observation.Number - expected.number; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("%+v number = %v, want %v", expected.key, observation.Number, expected.number)
		}
		if observation.Unit != expected.unit || observation.Source != RemoteMonitoringSourceReply ||
			!observation.Updated.Equal(rig.clock.Now()) || observation.Stale {
			t.Errorf("%+v = %+v, want unit %s, reply source, update time %v, not stale", expected.key, observation, expected.unit, rig.clock.Now())
		}
	}
	entity := remote.Entities[0]
	if entity.DeviceAddress != rig.address || entity.DeviceType != string(spinemodel.DeviceTypeTypeElectricitySupplySystem) ||
		len(entity.EntityAddress) != 1 || entity.EntityAddress[0] != 3 ||
		entity.EntityType != string(spinemodel.EntityTypeTypeGridConnectionPointOfPremises) || len(entity.Scenarios) == 0 {
		t.Fatalf("entity identity = %+v", entity)
	}
}

func TestRemoteUseCasesConsumeOnlyAnnouncedScenarios(t *testing.T) {
	rig := newUseCaseRig(t, "b", strings.Repeat("b", 40), 3,
		announceBoth([]spinemodel.UseCaseScenarioSupportType{2}, []spinemodel.UseCaseScenarioSupportType{5}))
	feedUseCaseData(t, rig, spinemodel.CmdClassifierTypeNotify)
	got := useCaseObservationsByKey(t, rig.snapshot())
	grid := string(spinemodel.UseCaseNameTypeMonitoringOfGridConnectionPoint)
	power := string(spinemodel.UseCaseNameTypeMonitoringOfPowerConsumption)
	if len(got) != 2 {
		t.Fatalf("observations = %+v, want only the announced scenarios", got)
	}
	if observation := got[useCaseKey{grid, 2, "power", ""}]; observation.Source != RemoteMonitoringSourceNotify {
		t.Fatalf("grid power = %+v, want a notify source", observation)
	}
	if _, ok := got[useCaseKey{power, 5, "frequency", ""}]; !ok {
		t.Fatalf("power frequency missing: %+v", got)
	}
}

func TestRemoteUseCasesAbsentValueIsAbsentNeverZero(t *testing.T) {
	rig := newUseCaseRig(t, "c", strings.Repeat("c", 40), 3, announceBoth(scenarioRange(7), nil))
	descriptions := &spinemodel.MeasurementDescriptionListDataType{MeasurementDescriptionData: []spinemodel.MeasurementDescriptionDataType{
		useCaseDescription(1, spinemodel.MeasurementTypeTypePower, spinemodel.ScopeTypeTypeACPowerTotal, spinemodel.UnitOfMeasurementTypeW),
		useCaseDescription(7, spinemodel.MeasurementTypeTypeFrequency, spinemodel.ScopeTypeTypeACFrequency, spinemodel.UnitOfMeasurementTypeHz),
		useCaseDescription(4, spinemodel.MeasurementTypeTypeCurrent, spinemodel.ScopeTypeTypeACCurrent, spinemodel.UnitOfMeasurementTypeA),
		useCaseDescription(3, spinemodel.MeasurementTypeTypeEnergy, spinemodel.ScopeTypeTypeGridConsumption, "kWh"),
	}}
	values := &spinemodel.MeasurementListDataType{MeasurementData: []spinemodel.MeasurementDataType{
		useCaseValue(1, 0, 0), useCaseValue(4, 5, 0), useCaseValue(3, 9, 0),
		{MeasurementId: util.Ptr(spinemodel.MeasurementIdType(7))},
	}}
	rig.publishDirect(t, rig.dataEvent(spinemodel.FunctionTypeMeasurementDescriptionListData, descriptions, spinemodel.CmdClassifierTypeReply))
	rig.publishDirect(t, rig.dataEvent(spinemodel.FunctionTypeMeasurementListData, values, spinemodel.CmdClassifierTypeReply))
	got := useCaseObservationsByKey(t, rig.snapshot())
	grid := string(spinemodel.UseCaseNameTypeMonitoringOfGridConnectionPoint)
	if len(got) != 1 {
		t.Fatalf("observations = %+v, want only the real zero power value", got)
	}
	if observation, ok := got[useCaseKey{grid, 2, "power", ""}]; !ok || observation.Number != 0 {
		t.Fatalf("power = %+v, want the value the remote sent", observation)
	}
}

func TestRemoteUseCasesStaleAfterDisconnectKeepsValueAndTime(t *testing.T) {
	rig := newUseCaseRig(t, "d", strings.Repeat("d", 40), 3, announceBoth(scenarioRange(7), nil))
	grid := string(spinemodel.UseCaseNameTypeMonitoringOfGridConnectionPoint)
	descriptions := &spinemodel.MeasurementDescriptionListDataType{MeasurementDescriptionData: []spinemodel.MeasurementDescriptionDataType{
		useCaseDescription(1, spinemodel.MeasurementTypeTypePower, spinemodel.ScopeTypeTypeACPowerTotal, spinemodel.UnitOfMeasurementTypeW),
	}}
	rig.publishDirect(t, rig.dataEvent(spinemodel.FunctionTypeMeasurementDescriptionListData, descriptions, spinemodel.CmdClassifierTypeReply))
	notifiedAt := rig.clock.Now()
	rig.publishDirect(t, rig.dataEvent(spinemodel.FunctionTypeMeasurementListData,
		&spinemodel.MeasurementListDataType{MeasurementData: []spinemodel.MeasurementDataType{useCaseValue(1, 1500, 0)}},
		spinemodel.CmdClassifierTypeNotify))
	rig.clock.set(notifiedAt.Add(time.Minute))
	repliedAt := rig.clock.Now()
	key := spinemodel.DeviceConfigurationKeyIdType(3)
	name := spinemodel.DeviceConfigurationKeyNameTypePvCurtailmentLimitFactor
	rig.publishDirect(t, rig.dataEvent(spinemodel.FunctionTypeDeviceConfigurationKeyValueDescriptionListData,
		&spinemodel.DeviceConfigurationKeyValueDescriptionListDataType{DeviceConfigurationKeyValueDescriptionData: []spinemodel.DeviceConfigurationKeyValueDescriptionDataType{{KeyId: &key, KeyName: &name}}},
		spinemodel.CmdClassifierTypeReply))
	number, scale := spinemodel.NumberType(60), spinemodel.ScaleType(0)
	rig.publishDirect(t, rig.dataEvent(spinemodel.FunctionTypeDeviceConfigurationKeyValueListData,
		&spinemodel.DeviceConfigurationKeyValueListDataType{DeviceConfigurationKeyValueData: []spinemodel.DeviceConfigurationKeyValueDataType{{
			KeyId: &key, Value: &spinemodel.DeviceConfigurationKeyValueValueType{ScaledNumber: &spinemodel.ScaledNumberType{Number: &number, Scale: &scale}},
		}}}, spinemodel.CmdClassifierTypeReply))
	rig.clock.set(repliedAt.Add(time.Hour))

	before := useCaseObservationsByKey(t, rig.snapshot())
	rig.sessions.set(1, false)
	after := useCaseObservationsByKey(t, rig.useCases.snapshot([]runtimeGraphObservation{rig.observation(false)}).Remotes[0])
	for _, key := range []useCaseKey{{grid, 2, "power", ""}, {grid, 1, "powerLimitationFactor", ""}} {
		current, retained := before[key], after[key]
		if current.Stale || !retained.Stale {
			t.Errorf("%+v stale before = %v, after = %v; want false then true", key, current.Stale, retained.Stale)
		}
		if retained.Number != current.Number || !retained.Updated.Equal(current.Updated) || retained.Source != current.Source {
			t.Errorf("%+v after disconnect = %+v, before = %+v; want the same value, time and source", key, retained, current)
		}
	}
	if before[useCaseKey{grid, 2, "power", ""}].Source != RemoteMonitoringSourceNotify ||
		!before[useCaseKey{grid, 2, "power", ""}].Updated.Equal(notifiedAt) {
		t.Errorf("notified value = %+v, want notify at %v", before[useCaseKey{grid, 2, "power", ""}], notifiedAt)
	}
	if before[useCaseKey{grid, 1, "powerLimitationFactor", ""}].Source != RemoteMonitoringSourceReply ||
		!before[useCaseKey{grid, 1, "powerLimitationFactor", ""}].Updated.Equal(repliedAt) {
		t.Errorf("reply-only value = %+v, want reply at %v", before[useCaseKey{grid, 1, "powerLimitationFactor", ""}], repliedAt)
	}

	rig.sessions.set(2, true)
	reconnected := useCaseObservationsByKey(t, rig.snapshot())
	if !reconnected[useCaseKey{grid, 2, "power", ""}].Stale {
		t.Fatalf("a value from the previous connection must stay stale until the remote replies again")
	}
	rig.clock.set(repliedAt.Add(2 * time.Hour))
	rig.publishDirect(t, rig.dataEvent(spinemodel.FunctionTypeMeasurementListData,
		&spinemodel.MeasurementListDataType{MeasurementData: []spinemodel.MeasurementDataType{useCaseValue(1, 1600, 0)}},
		spinemodel.CmdClassifierTypeReply))
	refreshed := useCaseObservationsByKey(t, rig.snapshot())
	if power := refreshed[useCaseKey{grid, 2, "power", ""}]; power.Stale || power.Number != 1600 || power.Source != RemoteMonitoringSourceReply {
		t.Fatalf("power after the new reply = %+v", power)
	}
	if !refreshed[useCaseKey{grid, 1, "powerLimitationFactor", ""}].Stale {
		t.Fatalf("the factor was not replied again and stays stale")
	}
}

func TestRemoteUseCasesNotifiedAccessorStaysUnavailableForReplyOnlyValue(t *testing.T) {
	specs := []remoteSubscriptionFeatureSpec{
		{address: 11, featureType: spinemodel.FeatureTypeTypeMeasurement, role: spinemodel.RoleTypeServer,
			function: spinemodel.FunctionTypeMeasurementListData, readable: true},
		{address: 13, featureType: spinemodel.FeatureTypeTypeDeviceConfiguration, role: spinemodel.RoleTypeServer,
			function: spinemodel.FunctionTypeDeviceConfigurationKeyValueListData, readable: true},
	}
	fixture := newRemoteSubscriptionFixture(t, strings.Repeat("e", 40), specs)
	handler := newRemoteSubscriptionHandler(t, fixture, remoteSubscriptionTrustAll)
	sessions := &useCaseSessions{index: 4, up: true}
	service := &useCaseService{fakeRuntimeService: &fakeRuntimeService{started: make(chan struct{}), localDevice: fixture.local}}
	useCases, err := registerRemoteUseCases(service, fixture.clock.Now, nil, sessions.get)
	if err != nil || useCases == nil {
		t.Fatalf("registerRemoteUseCases() = %v, %v", useCases, err)
	}
	t.Cleanup(useCases.stop)
	backend := &serviceBackend{rawFeatures: fixture.bridge, handler: handler, useCases: useCases}
	observe := func(state string, index uint64) {
		handler.mu.Lock()
		handler.observations[fixture.remoteSKI] = runtimeGraphObservation{
			RemoteSKI: fixture.remoteSKI, SessionState: state, SessionIndex: index, Paired: true, PairingState: "paired",
		}
		handler.mu.Unlock()
	}
	observe("connected", 4)
	fixture.admit(t, 4)
	fixture.establish(t, 2)

	classifierReply := spinemodel.CmdClassifierTypeReply
	data := &spinemodel.NodeManagementUseCaseDataType{}
	data.AddUseCaseSupport(*fixture.feature(11).Address(), spinemodel.UseCaseActorTypeGridConnectionPoint,
		spinemodel.UseCaseNameTypeMonitoringOfGridConnectionPoint, "1.0.0", "release", true,
		[]spinemodel.UseCaseScenarioSupportType{1, 2})
	node := fixture.base.Entity(spine.DeviceInformationAddressEntity).FeatureOfTypeAndRole(
		spinemodel.FeatureTypeTypeNodeManagement, spinemodel.RoleTypeSpecial)
	useCases.HandleEvent(spineapi.EventPayload{
		Ski: fixture.remoteSKI, EventType: spineapi.EventTypeDataChange, ChangeType: spineapi.ElementChangeUpdate,
		Device: fixture.remote, Entity: node.Entity(), Feature: node, CmdClassifier: &classifierReply, Data: data,
	})
	descriptionID := spinemodel.MeasurementIdType(3)
	power := spinemodel.MeasurementTypeTypePower
	scope := spinemodel.ScopeTypeTypeACPowerTotal
	commodity := spinemodel.CommodityTypeTypeElectricity
	reply := func(function spinemodel.FunctionType, payload any) {
		useCases.HandleEvent(spineapi.EventPayload{
			Ski: fixture.remoteSKI, EventType: spineapi.EventTypeDataChange, ChangeType: spineapi.ElementChangeUpdate,
			Device: fixture.remote, Entity: fixture.entity, Feature: fixture.feature(11), Function: function,
			CmdClassifier: &classifierReply, Data: payload,
		})
	}
	reply(spinemodel.FunctionTypeMeasurementDescriptionListData, &spinemodel.MeasurementDescriptionListDataType{
		MeasurementDescriptionData: []spinemodel.MeasurementDescriptionDataType{{
			MeasurementId: &descriptionID, MeasurementType: &power, ScopeType: &scope, CommodityType: &commodity,
		}},
	})
	key := spinemodel.DeviceConfigurationKeyIdType(5)
	keyName := spinemodel.DeviceConfigurationKeyNameTypePvCurtailmentLimitFactor
	reply(spinemodel.FunctionTypeDeviceConfigurationKeyValueDescriptionListData, &spinemodel.DeviceConfigurationKeyValueDescriptionListDataType{
		DeviceConfigurationKeyValueDescriptionData: []spinemodel.DeviceConfigurationKeyValueDescriptionDataType{{KeyId: &key, KeyName: &keyName}},
	})
	number, plainScale := spinemodel.NumberType(55), spinemodel.ScaleType(0)
	reply(spinemodel.FunctionTypeDeviceConfigurationKeyValueListData, &spinemodel.DeviceConfigurationKeyValueListDataType{
		DeviceConfigurationKeyValueData: []spinemodel.DeviceConfigurationKeyValueDataType{{
			KeyId: &key, Value: &spinemodel.DeviceConfigurationKeyValueValueType{ScaledNumber: &spinemodel.ScaledNumberType{Number: &number, Scale: &plainScale}},
		}},
	})
	repliedAt := fixture.clock.Now()
	fixture.clock.set(repliedAt.Add(time.Minute))
	notified := remoteSubscriptionNotify(t, fixture, fixture.feature(11), spinemodel.FunctionTypeMeasurementListData,
		&spinemodel.MeasurementListDataType{MeasurementData: []spinemodel.MeasurementDataType{useCaseValue(3, 1500, 0)}})
	handler.HandleEvent(notified)
	useCases.HandleEvent(notified)
	notifiedAt := fixture.clock.Now()

	read := func() map[useCaseKey]RemoteMonitoringObservation {
		snapshot, err := backend.RemoteMonitoringGet(context.Background())
		if err != nil || len(snapshot.Remotes) != 1 {
			t.Fatalf("RemoteMonitoringGet() = %+v, %v", snapshot, err)
		}
		return useCaseObservationsByKey(t, snapshot.Remotes[0])
	}
	grid := string(spinemodel.UseCaseNameTypeMonitoringOfGridConnectionPoint)
	factorKey, powerKey := useCaseKey{grid, 1, "powerLimitationFactor", ""}, useCaseKey{grid, 2, "power", ""}
	connected := read()
	factorTarget := fixture.target(13, spinemodel.FeatureTypeTypeDeviceConfiguration, spinemodel.FunctionTypeDeviceConfigurationKeyValueListData)
	powerTarget := fixture.target(11, spinemodel.FeatureTypeTypeMeasurement, spinemodel.FunctionTypeMeasurementListData)
	if _, terminal := backend.FeatureNotifiedDataGet(context.Background(), remoteSubscriptionAuthorization(), powerTarget); terminal != nil {
		t.Fatalf("notified read of the notified value = %+v", terminal)
	}
	if _, terminal := backend.FeatureNotifiedDataGet(
		context.Background(), remoteSubscriptionAuthorization(), factorTarget,
	); terminal == nil || terminal.Code != eebusraw.ErrorCodeV1NotFound {
		t.Fatalf("last-notified accessor for the reply-only value = %+v, want not_found", terminal)
	}
	subscribed, _ := fixture.sender.calls()

	fixture.bridge.retireRemote(fixture.remoteSKI, 4)
	observe("disconnected", 4)
	disconnected := read()
	for _, key := range []useCaseKey{factorKey, powerKey} {
		if connected[key].Stale || !disconnected[key].Stale ||
			disconnected[key].Number != connected[key].Number || !disconnected[key].Updated.Equal(connected[key].Updated) {
			t.Errorf("%+v before = %+v, after disconnect = %+v", key, connected[key], disconnected[key])
		}
	}
	if !disconnected[factorKey].Updated.Equal(repliedAt) || disconnected[factorKey].Number != 55 ||
		!disconnected[powerKey].Updated.Equal(notifiedAt) || disconnected[powerKey].Number != 1500 {
		t.Errorf("retained values = %+v / %+v", disconnected[factorKey], disconnected[powerKey])
	}
	for _, target := range []eebusraw.FeatureTargetV1{factorTarget, powerTarget} {
		if _, terminal := backend.FeatureNotifiedDataGet(
			context.Background(), remoteSubscriptionAuthorization(), target,
		); terminal == nil || terminal.Code != eebusraw.ErrorCodeV1NotFound {
			t.Errorf("last-notified accessor after disconnect = %+v, want not_found", terminal)
		}
	}

	observe("connected", 5)
	fixture.admit(t, 5)
	again, unsubscribed := fixture.sender.calls()
	if len(again) != len(subscribed) || len(unsubscribed) != 0 {
		t.Fatalf("reconnect sent subscriptions %+v (before %+v) and unsubscriptions %+v", again, subscribed, unsubscribed)
	}
}

func TestRemoteUseCasesRevokedRemoteLosesRetainedValues(t *testing.T) {
	rig := newUseCaseRig(t, "f", strings.Repeat("f", 40), 3, announceBoth(scenarioRange(7), nil))
	feedUseCaseData(t, rig, spinemodel.CmdClassifierTypeReply)
	if len(rig.snapshot().Entities) == 0 {
		t.Fatal("no entities before revocation")
	}
	revoked := false
	rig.useCases.revoked = func(string) bool { return revoked }
	revoked = true
	snapshot := rig.useCases.snapshot([]runtimeGraphObservation{rig.observation(false)})
	if len(snapshot.Remotes) != 1 || len(snapshot.Remotes[0].Entities) != 0 {
		t.Fatalf("revoked remote = %+v, want it listed without values", snapshot.Remotes)
	}
	revoked = false
	if after := rig.useCases.snapshot([]runtimeGraphObservation{rig.observation(true)}); len(after.Remotes[0].Entities) != 0 {
		t.Fatalf("values returned after revocation: %+v", after.Remotes[0].Entities)
	}
}

func TestRemoteUseCasesUnpairedRemoteIsListedWithItsStateAndNoValues(t *testing.T) {
	rig := newUseCaseRig(t, "g", strings.Repeat("0", 40), 3, announceBoth(scenarioRange(7), nil))
	other := strings.Repeat("9", 40)
	snapshot := rig.useCases.snapshot([]runtimeGraphObservation{{
		RemoteSKI: other, SessionState: "disconnected", PairingState: "unpaired", Visible: true,
		ServiceName: "synthetic", ServiceBrand: "brand", ServiceModel: "model", ServiceType: "type", ServiceIdentifier: "id",
	}})
	if len(snapshot.Remotes) != 1 {
		t.Fatalf("remotes = %+v", snapshot.Remotes)
	}
	remote := snapshot.Remotes[0]
	if remote.SKI != other || remote.SHIPState != "disconnected" || remote.PairingState != "unpaired" || !remote.Visible ||
		remote.Paired || remote.ServiceName != "synthetic" || remote.ServiceBrand != "brand" ||
		remote.ServiceModel != "model" || remote.ServiceType != "type" || remote.ServiceIdentifier != "id" || len(remote.Entities) != 0 {
		t.Fatalf("unpaired remote = %+v", remote)
	}
}

func TestRemoteUseCasesKeyVariesEverySkiEntityUseCaseAndMeasurement(t *testing.T) {
	rigA := newUseCaseRig(t, "k1", strings.Repeat("1", 40), 3, announceBoth(scenarioRange(7), scenarioRange(5)))
	rigB := newUseCaseRig(t, "k2", strings.Repeat("2", 40), 4, announceBoth(scenarioRange(7), nil))
	for index, rig := range []*useCaseRig{rigA, rigB} {
		descriptions := &spinemodel.MeasurementDescriptionListDataType{MeasurementDescriptionData: []spinemodel.MeasurementDescriptionDataType{
			useCaseDescription(1, spinemodel.MeasurementTypeTypePower, spinemodel.ScopeTypeTypeACPowerTotal, spinemodel.UnitOfMeasurementTypeW),
			useCaseDescription(2, spinemodel.MeasurementTypeTypePower, spinemodel.ScopeTypeTypeACPowerTotal, spinemodel.UnitOfMeasurementTypeW),
		}}
		rig.publishDirect(t, rig.dataEvent(spinemodel.FunctionTypeMeasurementDescriptionListData, descriptions, spinemodel.CmdClassifierTypeReply))
		rig.publishDirect(t, rig.dataEvent(spinemodel.FunctionTypeMeasurementListData,
			&spinemodel.MeasurementListDataType{MeasurementData: []spinemodel.MeasurementDataType{useCaseValue(1, int64(100*(index+1)), 0)}},
			spinemodel.CmdClassifierTypeReply))
	}
	a, b := rigA.snapshot(), rigB.snapshot()
	if len(a.Entities) != 2 || len(b.Entities) != 1 {
		t.Fatalf("entities a = %+v, b = %+v", a.Entities, b.Entities)
	}
	for _, entity := range a.Entities {
		if len(entity.EntityAddress) != 1 || entity.EntityAddress[0] != 3 {
			t.Errorf("remote a entity address = %v, want 3", entity.EntityAddress)
		}
		if len(entity.Observations) == 0 || entity.Observations[0].Number != 100 {
			t.Errorf("remote a %s observations = %+v, want value 100", entity.UseCase, entity.Observations)
		}
	}
	if len(b.Entities[0].EntityAddress) != 1 || b.Entities[0].EntityAddress[0] != 4 ||
		len(b.Entities[0].Observations) != 1 || b.Entities[0].Observations[0].Number != 200 {
		t.Fatalf("remote b entity = %+v, want address 4 and value 200 only", b.Entities[0])
	}
}

func TestRemoteUseCasesRegistrationStopsClientsAndDropsQueuedEvents(t *testing.T) {
	rig := newUseCaseRig(t, "h", strings.Repeat("3", 40), 3, announceBoth(scenarioRange(7), scenarioRange(5)))
	if len(rig.service.added) != 2 {
		t.Fatalf("registered use cases = %d, want 2", len(rig.service.added))
	}
	added := spineapi.EventPayload{
		Ski: rig.ski, Device: rig.peer, Entity: rig.entity,
		EventType: spineapi.EventTypeEntityChange, ChangeType: spineapi.ElementChangeAdd,
		Data: &spinemodel.NodeManagementDetailedDiscoveryDataType{},
	}
	rig.useCases.clients[0].client.HandleEvent(added)
	if calls := rig.sender.snapshot(); len(calls) == 0 {
		t.Fatalf("a running client sent no request for its own remote")
	}
	before := len(rig.sender.snapshot())
	rig.useCases.stop()
	rig.useCases.stop()
	for _, client := range rig.useCases.clients {
		client.client.HandleEvent(added)
		client.base.HandleEvent(rig.useCaseEvent(&spinemodel.NodeManagementUseCaseDataType{}, spinemodel.CmdClassifierTypeReply))
	}
	if after := len(rig.sender.snapshot()); after != before {
		t.Fatalf("stopped clients sent %d requests", after-before)
	}
	feedUseCaseData(t, rig, spinemodel.CmdClassifierTypeReply)
	if len(rig.useCases.remotes[rig.ski].entities[strings.TrimSpace("3")].measurements) != 0 {
		t.Fatalf("a stopped instance recorded an event")
	}
}

func TestRemoteUseCasesTwoRuntimesKeepTheirRequestsAndEventsApart(t *testing.T) {
	announce := announceBoth([]spinemodel.UseCaseScenarioSupportType{1}, nil)
	a := newUseCaseRig(t, "ra", strings.Repeat("4", 40), 3, announce)
	b := newUseCaseRig(t, "rb", strings.Repeat("5", 40), 3, announce)
	connected := func(rig *useCaseRig) spineapi.EventPayload {
		return spineapi.EventPayload{
			Ski: rig.ski, Device: rig.peer, Entity: rig.entity,
			EventType: spineapi.EventTypeEntityChange, ChangeType: spineapi.ElementChangeAdd,
			Data: &spinemodel.NodeManagementDetailedDiscoveryDataType{},
		}
	}
	described := func(rig *useCaseRig) spineapi.EventPayload {
		return rig.dataEvent(spinemodel.FunctionTypeDeviceConfigurationKeyValueDescriptionListData,
			&spinemodel.DeviceConfigurationKeyValueDescriptionListDataType{}, spinemodel.CmdClassifierTypeReply)
	}
	for _, rig := range []*useCaseRig{a, b} {
		spine.Events.Publish(connected(rig))
		spine.Events.Publish(described(rig))
	}
	waitCalls := func(rig *useCaseRig, count int) {
		t.Helper()
		remoteSubscriptionEventually(t, "peer requests for "+rig.address, func() bool { return len(rig.sender.snapshot()) >= count })
	}
	waitCalls(a, 3)
	waitCalls(b, 3)
	time.Sleep(100 * time.Millisecond)
	local := func(rig *useCaseRig) string { return "local-" + strings.TrimPrefix(rig.address, "peer-") }
	for _, rig := range []*useCaseRig{a, b} {
		calls := rig.sender.snapshot()
		if len(calls) != 3 {
			t.Fatalf("%s peer saw %d requests, want 3: %+v", rig.address, len(calls), calls)
		}
		for _, call := range calls {
			if !strings.Contains(call.source, local(rig)) || !strings.Contains(call.destination, rig.address) {
				t.Errorf("%s peer saw a request from %s to %s", rig.address, call.source, call.destination)
			}
		}
	}

	number := useCaseValue(1, 77, 0)
	descriptions := &spinemodel.MeasurementDescriptionListDataType{MeasurementDescriptionData: []spinemodel.MeasurementDescriptionDataType{
		useCaseDescription(1, spinemodel.MeasurementTypeTypePower, spinemodel.ScopeTypeTypeACPowerTotal, spinemodel.UnitOfMeasurementTypeW),
	}}
	announceMeasurement := announceBoth(scenarioRange(7), nil)
	for _, rig := range []*useCaseRig{a, b} {
		data := &spinemodel.NodeManagementUseCaseDataType{}
		announceMeasurement(data, *rig.entity.FeatureOfTypeAndRole(spinemodel.FeatureTypeTypeDeviceConfiguration, spinemodel.RoleTypeServer).Address())
		rig.publishDirect(t, rig.useCaseEvent(data, spinemodel.CmdClassifierTypeReply))
		rig.publishDirect(t, rig.dataEvent(spinemodel.FunctionTypeMeasurementDescriptionListData, descriptions, spinemodel.CmdClassifierTypeReply))
	}
	update := a.dataEvent(spinemodel.FunctionTypeMeasurementListData,
		&spinemodel.MeasurementListDataType{MeasurementData: []spinemodel.MeasurementDataType{number}}, spinemodel.CmdClassifierTypeNotify)
	spine.Events.Publish(update)
	remoteSubscriptionEventually(t, "instance a records its update", func() bool {
		return len(useCaseObservationsByKey(t, a.snapshot())) > 0
	})
	time.Sleep(50 * time.Millisecond)
	if got := useCaseObservationsByKey(t, b.snapshot()); len(got) != 0 {
		t.Fatalf("instance b read an update of instance a: %+v", got)
	}
	b.useCases.mu.Lock()
	foreign := b.useCases.remotes[a.ski]
	b.useCases.mu.Unlock()
	if foreign != nil {
		t.Fatalf("instance b retained data of the remote of instance a")
	}

	b.useCases.stop()
	spine.Events.Publish(connected(b))
	spine.Events.Publish(b.dataEvent(spinemodel.FunctionTypeMeasurementListData,
		&spinemodel.MeasurementListDataType{MeasurementData: []spinemodel.MeasurementDataType{number}}, spinemodel.CmdClassifierTypeNotify))
	bBefore := len(b.sender.snapshot())
	aBefore := len(a.sender.snapshot())
	spine.Events.Publish(described(a))
	remoteSubscriptionEventually(t, "instance a still works after b stopped", func() bool { return len(a.sender.snapshot()) > aBefore })
	time.Sleep(50 * time.Millisecond)
	if after := len(b.sender.snapshot()); after != bBefore {
		t.Fatalf("stopped instance b sent %d requests", after-bBefore)
	}
	if got := useCaseObservationsByKey(t, b.snapshot()); len(got) != 0 {
		t.Fatalf("stopped instance b recorded %+v", got)
	}
}
