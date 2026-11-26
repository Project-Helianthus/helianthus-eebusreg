package eebusfacade

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	eebusapi "github.com/Project-Helianthus/helianthus-eebus-go/api"
	"github.com/Project-Helianthus/helianthus-eebus-go/features/client"
	"github.com/Project-Helianthus/helianthus-eebus-go/usecases/cem/evcc"
	"github.com/Project-Helianthus/helianthus-eebus-go/usecases/cem/evcem"
	"github.com/Project-Helianthus/helianthus-eebus-go/usecases/cem/evsecc"
	"github.com/Project-Helianthus/helianthus-eebus-go/usecases/cem/evsoc"
	"github.com/Project-Helianthus/helianthus-eebus-go/usecases/cem/vabd"
	"github.com/Project-Helianthus/helianthus-eebus-go/usecases/cem/vapd"
	"github.com/Project-Helianthus/helianthus-eebus-go/usecases/ma/mgcp"
	"github.com/Project-Helianthus/helianthus-eebus-go/usecases/ma/mpc"
	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
	"github.com/Project-Helianthus/helianthus-spine-go/spine"
)

// RemoteMonitoringSourceReply and RemoteMonitoringSourceNotify name the message that carried an observation.
const (
	RemoteMonitoringSourceReply  = "reply"
	RemoteMonitoringSourceNotify = "notify"
)

// RemoteMonitoringKindNumber, RemoteMonitoringKindText and RemoteMonitoringKindBoolean name the source type of an observation.
const (
	RemoteMonitoringKindNumber  = "number"
	RemoteMonitoringKindText    = "text"
	RemoteMonitoringKindBoolean = "boolean"
)

var errRemoteMonitoringUnavailable = errors.New("remote monitoring is unavailable")

// RemoteMonitoringBackend serves every known remote with its SHIP state and the monitoring observations of its entities.
type RemoteMonitoringBackend interface {
	RemoteMonitoringGet(context.Context) (RemoteMonitoringSnapshot, error)
}

// RemoteMonitoringSnapshot lists the remotes the runtime knows, sorted by SKI.
type RemoteMonitoringSnapshot struct {
	Remotes []RemoteMonitoringRemote
}

// RemoteMonitoringRemote is one remote with its discovery data, SHIP state and served entities.
type RemoteMonitoringRemote struct {
	SKI               string
	SHIPID            string
	SHIPState         string
	PairingState      string
	Visible           bool
	Paired            bool
	ServiceName       string
	ServiceIdentifier string
	ServiceBrand      string
	ServiceType       string
	ServiceModel      string
	Entities          []RemoteMonitoringEntity
}

// RemoteMonitoringEntity is one remote entity serving one registered monitoring use case.
type RemoteMonitoringEntity struct {
	DeviceAddress string
	DeviceType    string
	EntityAddress []uint64
	ParentAddress []uint64
	EntityType    string
	UseCase       string
	Actor         string
	Scenarios     []uint
	Observations  []RemoteMonitoringObservation
}

// RemoteMonitoringObservation is one value in its source type with its unit, update time, source message and stale flag.
type RemoteMonitoringObservation struct {
	Scenario uint
	Name     string
	Phase    string
	Kind     string
	Number   float64
	Text     string
	Boolean  bool
	Unit     string
	Updated  time.Time
	Source   string
	Stale    bool
}

type runtimeUseCaseService interface {
	AddUseCase(eebusapi.UseCaseInterface)
}

type remoteUseCaseClient struct {
	client spineapi.EventHandlerInterface
	base   spineapi.EventHandlerInterface
	stop   func()
}

type remoteUseCaseRule struct {
	useCase     spinemodel.UseCaseNameType
	actor       spinemodel.UseCaseActorType
	scenario    uint
	name        string
	measurement spinemodel.MeasurementTypeType
	scope       spinemodel.ScopeTypeType
	unit        string
	perPhase    bool
	configKey   spinemodel.DeviceConfigurationKeyNameType
	kind        string
	named       string
	derive      string
	limit       string
	sourceUnit  string
	anyCommod   bool
}

var monitoringUseCaseRules = []remoteUseCaseRule{
	{useCase: spinemodel.UseCaseNameTypeMonitoringOfGridConnectionPoint, actor: spinemodel.UseCaseActorTypeGridConnectionPoint, scenario: 1, name: "powerLimitationFactor", unit: "%", configKey: spinemodel.DeviceConfigurationKeyNameTypePvCurtailmentLimitFactor},
	{useCase: spinemodel.UseCaseNameTypeMonitoringOfGridConnectionPoint, actor: spinemodel.UseCaseActorTypeGridConnectionPoint, scenario: 2, name: "power", measurement: spinemodel.MeasurementTypeTypePower, scope: spinemodel.ScopeTypeTypeACPowerTotal, unit: "W"},
	{useCase: spinemodel.UseCaseNameTypeMonitoringOfGridConnectionPoint, actor: spinemodel.UseCaseActorTypeGridConnectionPoint, scenario: 3, name: "energyFeedIn", measurement: spinemodel.MeasurementTypeTypeEnergy, scope: spinemodel.ScopeTypeTypeGridFeedIn, unit: "Wh"},
	{useCase: spinemodel.UseCaseNameTypeMonitoringOfGridConnectionPoint, actor: spinemodel.UseCaseActorTypeGridConnectionPoint, scenario: 4, name: "energyConsumed", measurement: spinemodel.MeasurementTypeTypeEnergy, scope: spinemodel.ScopeTypeTypeGridConsumption, unit: "Wh"},
	{useCase: spinemodel.UseCaseNameTypeMonitoringOfGridConnectionPoint, actor: spinemodel.UseCaseActorTypeGridConnectionPoint, scenario: 5, name: "currentPerPhase", measurement: spinemodel.MeasurementTypeTypeCurrent, scope: spinemodel.ScopeTypeTypeACCurrent, unit: "A", perPhase: true},
	{useCase: spinemodel.UseCaseNameTypeMonitoringOfGridConnectionPoint, actor: spinemodel.UseCaseActorTypeGridConnectionPoint, scenario: 6, name: "voltagePerPhase", measurement: spinemodel.MeasurementTypeTypeVoltage, scope: spinemodel.ScopeTypeTypeACVoltage, unit: "V", perPhase: true},
	{useCase: spinemodel.UseCaseNameTypeMonitoringOfGridConnectionPoint, actor: spinemodel.UseCaseActorTypeGridConnectionPoint, scenario: 7, name: "frequency", measurement: spinemodel.MeasurementTypeTypeFrequency, scope: spinemodel.ScopeTypeTypeACFrequency, unit: "Hz"},
	{useCase: spinemodel.UseCaseNameTypeMonitoringOfPowerConsumption, actor: spinemodel.UseCaseActorTypeMonitoredUnit, scenario: 1, name: "power", measurement: spinemodel.MeasurementTypeTypePower, scope: spinemodel.ScopeTypeTypeACPowerTotal, unit: "W"},
	{useCase: spinemodel.UseCaseNameTypeMonitoringOfPowerConsumption, actor: spinemodel.UseCaseActorTypeMonitoredUnit, scenario: 1, name: "powerPerPhase", measurement: spinemodel.MeasurementTypeTypePower, scope: spinemodel.ScopeTypeTypeACPower, unit: "W", perPhase: true},
	{useCase: spinemodel.UseCaseNameTypeMonitoringOfPowerConsumption, actor: spinemodel.UseCaseActorTypeMonitoredUnit, scenario: 2, name: "energyConsumed", measurement: spinemodel.MeasurementTypeTypeEnergy, scope: spinemodel.ScopeTypeTypeACEnergyConsumed, unit: "Wh"},
	{useCase: spinemodel.UseCaseNameTypeMonitoringOfPowerConsumption, actor: spinemodel.UseCaseActorTypeMonitoredUnit, scenario: 2, name: "energyProduced", measurement: spinemodel.MeasurementTypeTypeEnergy, scope: spinemodel.ScopeTypeTypeACEnergyProduced, unit: "Wh"},
	{useCase: spinemodel.UseCaseNameTypeMonitoringOfPowerConsumption, actor: spinemodel.UseCaseActorTypeMonitoredUnit, scenario: 3, name: "currentPerPhase", measurement: spinemodel.MeasurementTypeTypeCurrent, scope: spinemodel.ScopeTypeTypeACCurrent, unit: "A", perPhase: true},
	{useCase: spinemodel.UseCaseNameTypeMonitoringOfPowerConsumption, actor: spinemodel.UseCaseActorTypeMonitoredUnit, scenario: 4, name: "voltagePerPhase", measurement: spinemodel.MeasurementTypeTypeVoltage, scope: spinemodel.ScopeTypeTypeACVoltage, unit: "V", perPhase: true},
	{useCase: spinemodel.UseCaseNameTypeMonitoringOfPowerConsumption, actor: spinemodel.UseCaseActorTypeMonitoredUnit, scenario: 5, name: "frequency", measurement: spinemodel.MeasurementTypeTypeFrequency, scope: spinemodel.ScopeTypeTypeACFrequency, unit: "Hz"},
}

var manufacturerFields = []string{
	"deviceName", "deviceCode", "serialNumber", "softwareRevision", "hardwareRevision",
	"vendorName", "vendorCode", "brandName", "manufacturerLabel", "manufacturerDescription",
}

func chargingUseCaseRules() []remoteUseCaseRule {
	evse, ev := spinemodel.UseCaseActorTypeEVSE, spinemodel.UseCaseActorTypeEV
	evsecc, evcc := spinemodel.UseCaseNameTypeEVSECommissioningAndConfiguration, spinemodel.UseCaseNameTypeEVCommissioningAndConfiguration
	evcem, evsoc := spinemodel.UseCaseNameTypeMeasurementOfElectricityDuringEVCharging, spinemodel.UseCaseNameTypeEVStateOfCharge
	text := RemoteMonitoringKindText
	rules := []remoteUseCaseRule{
		{useCase: evsecc, actor: evse, scenario: 2, name: "operatingState", named: "operatingState", kind: text},
		{useCase: evsecc, actor: evse, scenario: 2, name: "lastErrorCode", named: "lastErrorCode", kind: text},
		{useCase: evcc, actor: ev, scenario: 1, name: "connected", derive: "connected", kind: RemoteMonitoringKindBoolean},
		{useCase: evcc, actor: ev, scenario: 2, name: "communicationStandard", kind: text,
			configKey: spinemodel.DeviceConfigurationKeyNameTypeCommunicationsStandard},
		{useCase: evcc, actor: ev, scenario: 3, name: "asymmetricChargingSupported", kind: RemoteMonitoringKindBoolean,
			configKey: spinemodel.DeviceConfigurationKeyNameTypeAsymmetricChargingSupported},
		{useCase: evcc, actor: ev, scenario: 4, name: "identification", named: "identification", kind: text},
		{useCase: evcc, actor: ev, scenario: 6, name: "minimumChargingPower", limit: "min", unit: "W"},
		{useCase: evcc, actor: ev, scenario: 6, name: "maximumChargingPower", limit: "max", unit: "W"},
		{useCase: evcc, actor: ev, scenario: 6, name: "standbyChargingPower", limit: "standby", unit: "W"},
		{useCase: evcc, actor: ev, scenario: 7, name: "chargeState", named: "operatingState", kind: text},
		{useCase: evcc, actor: ev, scenario: 7, name: "sleepMode", derive: "sleepMode", kind: RemoteMonitoringKindBoolean},
		{useCase: evcem, actor: ev, scenario: 1, name: "phasesConnected", named: "phasesConnected"},
		{useCase: evcem, actor: ev, scenario: 1, name: "currentPerPhase", measurement: spinemodel.MeasurementTypeTypeCurrent,
			scope: spinemodel.ScopeTypeTypeACCurrent, unit: "A", perPhase: true},
		{useCase: evcem, actor: ev, scenario: 2, name: "powerPerPhase", measurement: spinemodel.MeasurementTypeTypePower,
			scope: spinemodel.ScopeTypeTypeACPower, unit: "W", perPhase: true},
		{useCase: evcem, actor: ev, scenario: 3, name: "energyCharged", measurement: spinemodel.MeasurementTypeTypeEnergy,
			scope: spinemodel.ScopeTypeTypeCharge, unit: "Wh"},
		{useCase: evsoc, actor: ev, scenario: 1, name: "stateOfCharge", measurement: spinemodel.MeasurementTypeTypePercentage,
			scope: spinemodel.ScopeTypeTypeStateOfCharge, unit: "%", sourceUnit: "pct"},
		{useCase: evsoc, actor: ev, scenario: 2, name: "nominalCapacity", named: "nominalCapacity", unit: "Wh"},
		{useCase: evsoc, actor: ev, scenario: 3, name: "stateOfHealth", measurement: spinemodel.MeasurementTypeTypePercentage,
			scope: spinemodel.ScopeTypeTypeStateOfHealth, unit: "%", sourceUnit: "pct", anyCommod: true},
		{useCase: evsoc, actor: ev, scenario: 4, name: "travelRange", measurement: spinemodel.MeasurementTypeTypeDistance,
			scope: spinemodel.ScopeTypeTypeTravelRange, unit: "m", anyCommod: true},
	}
	for _, field := range manufacturerFields {
		rules = append(rules,
			remoteUseCaseRule{useCase: evsecc, actor: evse, scenario: 1, name: field, named: field, kind: text},
			remoteUseCaseRule{useCase: evcc, actor: ev, scenario: 5, name: field, named: field, kind: text})
	}
	return rules
}

func visualizationUseCaseRules() []remoteUseCaseRule {
	battery, pv := spinemodel.UseCaseActorTypeBatterySystem, spinemodel.UseCaseActorTypePVSystem
	vabdName, vapdName := spinemodel.UseCaseNameTypeVisualizationOfAggregatedBatteryData, spinemodel.UseCaseNameTypeVisualizationOfAggregatedPhotovoltaicData
	return []remoteUseCaseRule{
		{useCase: vabdName, actor: battery, scenario: 1, name: "power", measurement: spinemodel.MeasurementTypeTypePower, scope: spinemodel.ScopeTypeTypeACPowerTotal, unit: "W"},
		{useCase: vabdName, actor: battery, scenario: 2, name: "energyCharged", measurement: spinemodel.MeasurementTypeTypeEnergy, scope: spinemodel.ScopeTypeTypeCharge, unit: "Wh"},
		{useCase: vabdName, actor: battery, scenario: 3, name: "energyDischarged", measurement: spinemodel.MeasurementTypeTypeEnergy, scope: spinemodel.ScopeTypeTypeDischarge, unit: "Wh"},
		{useCase: vabdName, actor: battery, scenario: 4, name: "stateOfCharge", measurement: spinemodel.MeasurementTypeTypePercentage, scope: spinemodel.ScopeTypeTypeStateOfCharge, unit: "%", sourceUnit: "pct"},
		{useCase: vapdName, actor: pv, scenario: 1, name: "nominalPeakPower", unit: "W", configKey: spinemodel.DeviceConfigurationKeyNameTypePeakPowerOfPVSystem},
		{useCase: vapdName, actor: pv, scenario: 2, name: "power", measurement: spinemodel.MeasurementTypeTypePower, scope: spinemodel.ScopeTypeTypeACPowerTotal, unit: "W"},
		{useCase: vapdName, actor: pv, scenario: 3, name: "yieldTotal", measurement: spinemodel.MeasurementTypeTypeEnergy, scope: spinemodel.ScopeTypeTypeACYieldTotal, unit: "Wh"},
	}
}

var remoteUseCaseRules = append(append(append([]remoteUseCaseRule(nil), monitoringUseCaseRules...), chargingUseCaseRules()...), visualizationUseCaseRules()...)

type remoteRetainedValue struct {
	kind    string
	number  float64
	text    string
	flag    bool
	at      time.Time
	source  string
	session uint64
}

type remoteRetainedDescription struct {
	measurement spinemodel.MeasurementTypeType
	commodity   spinemodel.CommodityTypeType
	scope       spinemodel.ScopeTypeType
	unit        string
}

type remoteRetainedAnnouncement struct {
	actor     spinemodel.UseCaseActorType
	scenarios []uint
	announced remoteRetainedValue
}

type remoteRetainedEntity struct {
	address       []uint64
	entityType    string
	announcements map[spinemodel.UseCaseNameType]remoteRetainedAnnouncement
	descriptions  map[uint]remoteRetainedDescription
	phases        map[uint]string
	keyNames      map[uint]spinemodel.DeviceConfigurationKeyNameType
	measurements  map[uint]remoteRetainedValue
	config        map[uint]remoteRetainedValue
	named         map[string]remoteRetainedValue
	scopes        map[uint]spinemodel.ScopeTypeType
	limits        map[uint]map[string]remoteRetainedValue
	requested     uint64
}

type remoteRetainedRemote struct {
	deviceAddress string
	deviceType    string
	entities      map[string]*remoteRetainedEntity
}

type remoteUseCases struct {
	mu      sync.Mutex
	stopped bool
	local   spineapi.DeviceLocalInterface
	cem     spineapi.EntityLocalInterface
	now     func() time.Time
	revoked func(string) bool
	session func(string) (uint64, bool)
	clients []remoteUseCaseClient
	remotes map[string]*remoteRetainedRemote
}

func registerRemoteUseCases(
	service runtimeService,
	now func() time.Time,
	revoked func(string) bool,
	session func(string) (uint64, bool),
) (*remoteUseCases, error) {
	adder, ok := service.(runtimeUseCaseService)
	if !ok || service == nil || isNilRawRuntimeValue(service.LocalDevice()) {
		return nil, nil
	}
	local := service.LocalDevice()
	cem := local.EntityForType(spinemodel.EntityTypeTypeCEM)
	if isNilRawRuntimeValue(cem) {
		return nil, errors.New("local CEM entity is unavailable for use case registration")
	}
	callback := func(string, spineapi.DeviceRemoteInterface, spineapi.EntityRemoteInterface, eebusapi.EventType) {}
	libraryService, _ := service.(eebusapi.ServiceInterface)
	grid := mgcp.NewMGCP(cem, callback)
	consumption := mpc.NewMPC(cem, callback)
	evseConfiguration := evsecc.NewEVSECC(cem, callback)
	evConfiguration := evcc.NewEVCC(libraryService, cem, callback)
	evMeasurement := evcem.NewEVCEM(libraryService, cem, callback)
	evCharge := evsoc.NewEVSOC(cem, callback)
	batteryData := vabd.NewVABD(cem, callback)
	photovoltaicData := vapd.NewVAPD(cem, callback)
	useCases := &remoteUseCases{
		local: local, cem: cem, now: now, revoked: revoked, session: session,
		remotes: make(map[string]*remoteRetainedRemote),
		clients: []remoteUseCaseClient{
			{client: grid, base: grid.UseCaseBase, stop: grid.Stop},
			{client: consumption, base: consumption.UseCaseBase, stop: consumption.Stop},
			{client: evseConfiguration, base: evseConfiguration.UseCaseBase, stop: evseConfiguration.Stop},
			{client: evConfiguration, base: evConfiguration.UseCaseBase, stop: evConfiguration.Stop},
			{client: evMeasurement, base: evMeasurement.UseCaseBase, stop: evMeasurement.Stop},
			{client: evCharge, base: evCharge.UseCaseBase, stop: evCharge.Stop},
			{client: batteryData, base: batteryData.UseCaseBase, stop: batteryData.Stop},
			{client: photovoltaicData, base: photovoltaicData.UseCaseBase, stop: photovoltaicData.Stop},
		},
	}
	for _, client := range useCases.clients {
		adder.AddUseCase(client.client.(eebusapi.UseCaseInterface))
	}
	if err := spine.Events.Subscribe(useCases); err != nil {
		useCases.stop()
		return nil, err
	}
	return useCases, nil
}

func (useCases *remoteUseCases) stop() {
	if useCases == nil {
		return
	}
	useCases.mu.Lock()
	if useCases.stopped {
		useCases.mu.Unlock()
		return
	}
	useCases.stopped = true
	clients := useCases.clients
	useCases.mu.Unlock()
	_ = spine.Events.Unsubscribe(useCases)
	for _, client := range clients {
		client.stop()
		_ = spine.Events.Unsubscribe(client.client)
		_ = spine.Events.Unsubscribe(client.base)
	}
}

func (useCases *remoteUseCases) owns(payload spineapi.EventPayload) bool {
	ski := strings.ToLower(strings.TrimSpace(payload.Ski))
	if ski == "" || isNilRawRuntimeValue(payload.Device) || isNilRawRuntimeValue(useCases.local) {
		return false
	}
	if !sameRuntimeRemoteDevice(useCases.local.RemoteDeviceForSki(ski), payload.Device) {
		return false
	}
	return isNilRawRuntimeValue(payload.Entity) || remoteEntityDeviceMatches(payload.Entity, ski)
}

func remoteEntityKey(address []spinemodel.AddressEntityType) ([]uint64, string) {
	out := make([]uint64, 0, len(address))
	parts := make([]string, 0, len(address))
	for _, part := range address {
		out = append(out, uint64(part))
		parts = append(parts, strconv.FormatUint(uint64(part), 10))
	}
	return out, strings.Join(parts, ".")
}

func (useCases *remoteUseCases) HandleEvent(payload spineapi.EventPayload) {
	if payload.EventType == spineapi.EventTypeEntityChange && payload.ChangeType == spineapi.ElementChangeRemove {
		useCases.entityRemoved(payload)
		return
	}
	if payload.EventType != spineapi.EventTypeDataChange || payload.CmdClassifier == nil {
		return
	}
	source := ""
	switch *payload.CmdClassifier {
	case spinemodel.CmdClassifierTypeReply:
		source = RemoteMonitoringSourceReply
	case spinemodel.CmdClassifierTypeNotify:
		source = RemoteMonitoringSourceNotify
	default:
		return
	}
	useCases.mu.Lock()
	stopped := useCases.stopped
	useCases.mu.Unlock()
	if stopped || !useCases.owns(payload) {
		return
	}
	ski := strings.ToLower(strings.TrimSpace(payload.Ski))
	if useCases.revoked != nil && useCases.revoked(ski) {
		useCases.mu.Lock()
		delete(useCases.remotes, ski)
		useCases.mu.Unlock()
		return
	}
	var index uint64
	if useCases.session != nil {
		index, _ = useCases.session(ski)
	}
	value := remoteRetainedValue{at: useCases.now(), source: source, session: index}
	for _, entity := range useCases.record(ski, payload, value) {
		useCases.requestCharacteristics(entity)
	}
}

func (useCases *remoteUseCases) entityRemoved(payload spineapi.EventPayload) {
	ski := strings.ToLower(strings.TrimSpace(payload.Ski))
	if !useCases.owns(payload) || isNilRawRuntimeValue(payload.Entity) || payload.Entity.Address() == nil {
		return
	}
	_, key := remoteEntityKey(payload.Entity.Address().Entity)
	useCases.mu.Lock()
	defer useCases.mu.Unlock()
	remote := useCases.remotes[ski]
	if useCases.stopped || remote == nil {
		return
	}
	for existing := range remote.entities {
		if existing == key || strings.HasPrefix(existing, key+".") {
			delete(remote.entities, existing)
		}
	}
}

func (useCases *remoteUseCases) record(
	ski string, payload spineapi.EventPayload, value remoteRetainedValue,
) []spineapi.EntityRemoteInterface {
	useCases.mu.Lock()
	defer useCases.mu.Unlock()
	if useCases.stopped {
		return nil
	}
	remote := useCases.remoteLocked(ski, payload.Device)
	if nodeData, ok := payload.Data.(*spinemodel.NodeManagementUseCaseDataType); ok {
		return useCases.recordAnnouncements(remote, payload.Device, nodeData, value)
	}
	if isNilRawRuntimeValue(payload.Entity) || payload.Entity.Address() == nil {
		return nil
	}
	entity := remote.entityLocked(payload.Entity.Address().Entity, string(payload.Entity.EntityType()))
	entity.record(payload.Data, value)
	return nil
}

func (value remoteRetainedValue) asNumber(number float64) remoteRetainedValue {
	value.kind, value.number = RemoteMonitoringKindNumber, number
	return value
}

func (value remoteRetainedValue) asText(text string) remoteRetainedValue {
	value.kind, value.text = RemoteMonitoringKindText, text
	return value
}

func (entity *remoteRetainedEntity) record(data any, value remoteRetainedValue) {
	switch data := data.(type) {
	case *spinemodel.MeasurementDescriptionListDataType:
		entity.recordMeasurementDescriptions(data)
	case *spinemodel.MeasurementListDataType:
		for _, item := range data.MeasurementData {
			if item.MeasurementId == nil || item.Value == nil || item.Value.Number == nil {
				continue
			}
			if item.ValueType != nil && *item.ValueType != spinemodel.MeasurementValueTypeTypeValue {
				continue
			}
			entity.measurements[uint(*item.MeasurementId)] = value.asNumber(item.Value.GetValue())
		}
	case *spinemodel.DeviceConfigurationKeyValueDescriptionListDataType:
		for _, item := range data.DeviceConfigurationKeyValueDescriptionData {
			if item.KeyId != nil && item.KeyName != nil {
				entity.keyNames[uint(*item.KeyId)] = *item.KeyName
			}
		}
	case *spinemodel.DeviceConfigurationKeyValueListDataType:
		entity.recordConfiguration(data, value)
	default:
		entity.recordElectricalAndText(data, value)
	}
}

func remoteSortedKeys[K cmp.Ordered, V any](items map[K]V) []K {
	keys := make([]K, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func remoteDeref[T any](item *T) (zero T) {
	if item == nil {
		return zero
	}
	return *item
}

func (entity *remoteRetainedEntity) recordMeasurementDescriptions(data *spinemodel.MeasurementDescriptionListDataType) {
	for _, item := range data.MeasurementDescriptionData {
		if item.MeasurementId != nil {
			entity.descriptions[uint(*item.MeasurementId)] = remoteRetainedDescription{
				measurement: remoteDeref(item.MeasurementType), commodity: remoteDeref(item.CommodityType),
				scope: remoteDeref(item.ScopeType), unit: string(remoteDeref(item.Unit)),
			}
		}
	}
}

func (entity *remoteRetainedEntity) recordConfiguration(
	data *spinemodel.DeviceConfigurationKeyValueListDataType, value remoteRetainedValue,
) {
	for _, item := range data.DeviceConfigurationKeyValueData {
		if item.KeyId == nil || item.Value == nil {
			continue
		}
		id := uint(*item.KeyId)
		switch {
		case item.Value.ScaledNumber != nil && item.Value.ScaledNumber.Number != nil:
			entity.config[id] = value.asNumber(item.Value.ScaledNumber.GetValue())
		case item.Value.String != nil:
			entity.config[id] = value.asText(string(*item.Value.String))
		case item.Value.Boolean != nil:
			stored := value
			stored.kind, stored.flag = RemoteMonitoringKindBoolean, *item.Value.Boolean
			entity.config[id] = stored
		}
	}
}

func (entity *remoteRetainedEntity) recordElectricalAndText(data any, value remoteRetainedValue) {
	switch data := data.(type) {
	case *spinemodel.ElectricalConnectionParameterDescriptionListDataType:
		for _, item := range data.ElectricalConnectionParameterDescriptionData {
			if item.MeasurementId != nil && item.AcMeasuredPhases != nil {
				entity.phases[uint(*item.MeasurementId)] = string(*item.AcMeasuredPhases)
			}
			if item.ParameterId != nil && item.ScopeType != nil {
				entity.scopes[uint(*item.ParameterId)] = *item.ScopeType
			}
		}
	case *spinemodel.ElectricalConnectionPermittedValueSetListDataType:
		entity.recordPermittedValues(data, value)
	case *spinemodel.ElectricalConnectionDescriptionListDataType:
		for _, item := range data.ElectricalConnectionDescriptionData {
			if item.AcConnectedPhases != nil {
				entity.named["phasesConnected"] = value.asNumber(float64(*item.AcConnectedPhases))
			}
		}
	case *spinemodel.ElectricalConnectionCharacteristicListDataType:
		for _, item := range data.ElectricalConnectionCharacteristicData {
			if item.CharacteristicContext == nil || *item.CharacteristicContext != spinemodel.ElectricalConnectionCharacteristicContextTypeEntity ||
				item.CharacteristicType == nil || *item.CharacteristicType != spinemodel.ElectricalConnectionCharacteristicTypeTypeEnergyCapacityNominalMax ||
				item.Value == nil || item.Value.Number == nil || (item.Unit != nil && *item.Unit != spinemodel.UnitOfMeasurementTypeWh) {
				continue
			}
			entity.named["nominalCapacity"] = value.asNumber(item.Value.GetValue())
		}
	case *spinemodel.DeviceClassificationManufacturerDataType:
		entity.recordManufacturer(data, value)
	case *spinemodel.DeviceDiagnosisStateDataType:
		entity.recordState(data, value)
	case *spinemodel.IdentificationListDataType:
		for _, item := range data.IdentificationData {
			if item.IdentificationId == nil || item.IdentificationType == nil || item.IdentificationValue == nil {
				continue
			}
			key := fmt.Sprintf("identification/%s/%010d", *item.IdentificationType, uint64(*item.IdentificationId))
			entity.named[key] = value.asText(string(*item.IdentificationValue))
		}
	}
}

func (entity *remoteRetainedEntity) recordPermittedValues(
	data *spinemodel.ElectricalConnectionPermittedValueSetListDataType, value remoteRetainedValue,
) {
	for _, item := range data.ElectricalConnectionPermittedValueSetData {
		if item.ParameterId == nil || len(item.PermittedValueSet) == 0 {
			continue
		}
		set, limits := item.PermittedValueSet[0], map[string]*spinemodel.ScaledNumberType{}
		if len(set.Range) > 0 {
			limits["min"], limits["max"] = set.Range[0].Min, set.Range[0].Max
		}
		if len(set.Value) > 0 {
			limits["standby"] = &set.Value[0]
		}
		if entity.limits[uint(*item.ParameterId)] == nil {
			entity.limits[uint(*item.ParameterId)] = make(map[string]remoteRetainedValue)
		}
		for name, number := range limits {
			if number != nil && number.Number != nil {
				entity.limits[uint(*item.ParameterId)][name] = value.asNumber(number.GetValue())
			}
		}
	}
}

func remoteText[T ~string](item *T) *string {
	if item == nil {
		return nil
	}
	text := string(*item)
	return &text
}

func (entity *remoteRetainedEntity) recordManufacturer(data *spinemodel.DeviceClassificationManufacturerDataType, value remoteRetainedValue) {
	for name, text := range map[string]*string{
		"deviceName": remoteText(data.DeviceName), "deviceCode": remoteText(data.DeviceCode),
		"serialNumber": remoteText(data.SerialNumber), "softwareRevision": remoteText(data.SoftwareRevision),
		"hardwareRevision": remoteText(data.HardwareRevision), "vendorName": remoteText(data.VendorName),
		"vendorCode": remoteText(data.VendorCode), "brandName": remoteText(data.BrandName),
		"manufacturerLabel": remoteText(data.ManufacturerLabel), "manufacturerDescription": remoteText(data.ManufacturerDescription),
	} {
		if text != nil {
			entity.named[name] = value.asText(*text)
		}
	}
}

func (entity *remoteRetainedEntity) recordState(data *spinemodel.DeviceDiagnosisStateDataType, value remoteRetainedValue) {
	if value.source == RemoteMonitoringSourceReply {
		delete(entity.named, "operatingState")
		delete(entity.named, "lastErrorCode")
	}
	if data.OperatingState != nil {
		entity.named["operatingState"] = value.asText(string(*data.OperatingState))
	}
	if data.LastErrorCode != nil {
		entity.named["lastErrorCode"] = value.asText(string(*data.LastErrorCode))
	}
}

func (useCases *remoteUseCases) requestCharacteristics(entity spineapi.EntityRemoteInterface) {
	electrical, err := client.NewElectricalConnection(useCases.cem, entity)
	if err != nil || electrical == nil {
		return
	}
	_, _ = electrical.RequestCharacteristics(nil, nil)
}

func (useCases *remoteUseCases) remoteLocked(ski string, device spineapi.DeviceRemoteInterface) *remoteRetainedRemote {
	remote := useCases.remotes[ski]
	if remote == nil {
		remote = &remoteRetainedRemote{entities: make(map[string]*remoteRetainedEntity)}
		useCases.remotes[ski] = remote
	}
	if address := device.Address(); address != nil {
		remote.deviceAddress = string(*address)
	}
	if deviceType := device.DeviceType(); deviceType != nil {
		remote.deviceType = string(*deviceType)
	}
	return remote
}

func (remote *remoteRetainedRemote) entityLocked(address []spinemodel.AddressEntityType, entityType string) *remoteRetainedEntity {
	numbers, key := remoteEntityKey(address)
	entity := remote.entities[key]
	if entity == nil {
		entity = &remoteRetainedEntity{
			address:       numbers,
			announcements: make(map[spinemodel.UseCaseNameType]remoteRetainedAnnouncement),
			descriptions:  make(map[uint]remoteRetainedDescription),
			phases:        make(map[uint]string),
			keyNames:      make(map[uint]spinemodel.DeviceConfigurationKeyNameType),
			measurements:  make(map[uint]remoteRetainedValue),
			config:        make(map[uint]remoteRetainedValue),
			named:         make(map[string]remoteRetainedValue),
			scopes:        make(map[uint]spinemodel.ScopeTypeType),
			limits:        make(map[uint]map[string]remoteRetainedValue),
		}
		remote.entities[key] = entity
	}
	if entityType != "" {
		entity.entityType = entityType
	}
	return entity
}

func (useCases *remoteUseCases) recordAnnouncements(
	remote *remoteRetainedRemote,
	device spineapi.DeviceRemoteInterface,
	data *spinemodel.NodeManagementUseCaseDataType,
	value remoteRetainedValue,
) []spineapi.EntityRemoteInterface {
	previous := make(map[string]remoteRetainedValue)
	for key, entity := range remote.entities {
		for name, announcement := range entity.announcements {
			previous[key+"/"+string(name)] = announcement.announced
		}
		clear(entity.announcements)
	}
	var pending []spineapi.EntityRemoteInterface
	for _, information := range data.UseCaseInformation {
		if information.Address == nil || information.Actor == nil {
			continue
		}
		for _, support := range information.UseCaseSupport {
			if support.UseCaseName == nil || !remoteUseCaseKnown(*support.UseCaseName, *information.Actor) {
				continue
			}
			if support.UseCaseAvailable != nil && !*support.UseCaseAvailable {
				continue
			}
			known := device.Entity(information.Address.Entity)
			entityType := ""
			if !isNilRawRuntimeValue(known) {
				entityType = string(known.EntityType())
			}
			entity := remote.entityLocked(information.Address.Entity, entityType)
			scenarios := make([]uint, 0, len(support.ScenarioSupport))
			for _, scenario := range support.ScenarioSupport {
				scenarios = append(scenarios, uint(scenario))
			}
			sort.Slice(scenarios, func(i, j int) bool { return scenarios[i] < scenarios[j] })
			_, key := remoteEntityKey(information.Address.Entity)
			announced, seen := previous[key+"/"+string(*support.UseCaseName)]
			if !seen || announced.session != value.session {
				announced = value
			}
			entity.announcements[*support.UseCaseName] = remoteRetainedAnnouncement{
				actor: *information.Actor, scenarios: scenarios, announced: announced,
			}
			if *support.UseCaseName == spinemodel.UseCaseNameTypeEVStateOfCharge && !isNilRawRuntimeValue(known) &&
				slices.Contains(scenarios, 2) && entity.requested != value.session+1 {
				entity.requested = value.session + 1
				pending = append(pending, known)
			}
		}
	}
	return pending
}

func remoteUseCaseKnown(name spinemodel.UseCaseNameType, actor spinemodel.UseCaseActorType) bool {
	for _, rule := range remoteUseCaseRules {
		if rule.useCase == name && rule.actor == actor {
			return true
		}
	}
	return false
}

func (entity *remoteRetainedEntity) parentAddress() []uint64 {
	if len(entity.address) < 2 {
		return nil
	}
	return append([]uint64(nil), entity.address[:len(entity.address)-1]...)
}

func (useCases *remoteUseCases) entitiesFor(
	ski string,
	sessionIndex uint64,
	connected bool,
) []RemoteMonitoringEntity {
	remote := useCases.remotes[ski]
	if remote == nil {
		return nil
	}
	keys := make([]string, 0, len(remote.entities))
	for key := range remote.entities {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return slices.Compare(remote.entities[keys[i]].address, remote.entities[keys[j]].address) < 0
	})
	var out []RemoteMonitoringEntity
	for _, key := range keys {
		entity := remote.entities[key]
		for _, name := range remoteSortedKeys(entity.announcements) {
			announcement := entity.announcements[name]
			out = append(out, RemoteMonitoringEntity{
				DeviceAddress: remote.deviceAddress,
				DeviceType:    remote.deviceType,
				EntityAddress: append([]uint64(nil), entity.address...),
				ParentAddress: entity.parentAddress(),
				EntityType:    entity.entityType,
				UseCase:       string(name),
				Actor:         string(announcement.actor),
				Scenarios:     append([]uint(nil), announcement.scenarios...),
				Observations:  entity.observations(name, announcement, sessionIndex, connected),
			})
		}
	}
	return out
}

func (entity *remoteRetainedEntity) observations(
	useCase spinemodel.UseCaseNameType,
	announcement remoteRetainedAnnouncement,
	sessionIndex uint64,
	connected bool,
) []RemoteMonitoringObservation {
	announced := make(map[uint]bool, len(announcement.scenarios))
	for _, scenario := range announcement.scenarios {
		announced[scenario] = true
	}
	var out []RemoteMonitoringObservation
	for _, rule := range remoteUseCaseRules {
		if rule.useCase != useCase || rule.actor != announcement.actor || !announced[rule.scenario] {
			continue
		}
		for _, found := range entity.matches(rule, announcement) {
			observation := rule.observation(found.phase, found.value, sessionIndex, connected)
			if found.name != "" {
				observation.Name = rule.name + strings.ToUpper(found.name[:1]) + found.name[1:]
			}
			out = append(out, observation)
		}
	}
	return out
}

type remoteRuleMatch struct {
	phase string
	value remoteRetainedValue
	name  string
}

func (rule remoteUseCaseRule) observation(
	phase string, retained remoteRetainedValue, sessionIndex uint64, connected bool,
) RemoteMonitoringObservation {
	kind := retained.kind
	if kind == "" {
		kind = RemoteMonitoringKindNumber
	}
	return RemoteMonitoringObservation{
		Scenario: rule.scenario, Name: rule.name, Phase: phase, Kind: kind, Number: retained.number,
		Text: retained.text, Boolean: retained.flag, Unit: rule.unit, Updated: retained.at, Source: retained.source,
		Stale: !connected || retained.session != sessionIndex,
	}
}

func (entity *remoteRetainedEntity) matches(rule remoteUseCaseRule, announcement remoteRetainedAnnouncement) []remoteRuleMatch {
	switch {
	case rule.derive != "":
		return entity.derived(rule, announcement)
	case rule.limit != "":
		return entity.limitMatches(rule.limit)
	case rule.named == "identification":
		return entity.identifications()
	case rule.named != "":
		if retained, ok := entity.named[rule.named]; ok {
			return []remoteRuleMatch{{value: retained}}
		}
		return nil
	case rule.configKey != "":
		return entity.configMatches(rule)
	}
	return entity.measurementMatches(rule)
}

func (entity *remoteRetainedEntity) derived(rule remoteUseCaseRule, announcement remoteRetainedAnnouncement) []remoteRuleMatch {
	switch rule.derive {
	case "connected":
		connected := announcement.announced
		connected.kind, connected.flag = RemoteMonitoringKindBoolean, true
		return []remoteRuleMatch{{value: connected}}
	case "sleepMode":
		if state, ok := entity.named["operatingState"]; ok {
			state.kind, state.flag, state.text = RemoteMonitoringKindBoolean, state.text == string(spinemodel.DeviceDiagnosisOperatingStateTypeStandby), ""
			return []remoteRuleMatch{{value: state}}
		}
	}
	return nil
}

func (entity *remoteRetainedEntity) limitMatches(limit string) []remoteRuleMatch {
	for _, id := range remoteSortedKeys(entity.scopes) {
		if retained, ok := entity.limits[id][limit]; ok && entity.scopes[id] == spinemodel.ScopeTypeTypeACPowerTotal {
			return []remoteRuleMatch{{value: retained}}
		}
	}
	return nil
}

func (entity *remoteRetainedEntity) identifications() []remoteRuleMatch {
	var out []remoteRuleMatch
	for _, key := range remoteSortedKeys(entity.named) {
		if strings.HasPrefix(key, "identification/") {
			out = append(out, remoteRuleMatch{value: entity.named[key], name: strings.Split(key, "/")[1]})
		}
	}
	return out
}

func (entity *remoteRetainedEntity) configMatches(rule remoteUseCaseRule) []remoteRuleMatch {
	for _, id := range remoteSortedKeys(entity.keyNames) {
		if retained, ok := entity.config[id]; ok && entity.keyNames[id] == rule.configKey {
			if rule.kind != "" && retained.kind != rule.kind {
				return nil
			}
			return []remoteRuleMatch{{value: retained}}
		}
	}
	return nil
}

func (entity *remoteRetainedEntity) measurementMatches(rule remoteUseCaseRule) []remoteRuleMatch {
	sourceUnit := rule.sourceUnit
	if sourceUnit == "" {
		sourceUnit = rule.unit
	}
	var out []remoteRuleMatch
	for _, id := range remoteSortedKeys(entity.descriptions) {
		description := entity.descriptions[id]
		if _, has := entity.measurements[id]; !has || description.measurement != rule.measurement || description.scope != rule.scope ||
			(!rule.anyCommod && description.commodity != spinemodel.CommodityTypeTypeElectricity) ||
			(description.unit != "" && description.unit != sourceUnit) {
			continue
		}
		phase := ""
		if rule.perPhase {
			var known bool
			if phase, known = entity.phases[id]; !known {
				continue
			}
		}
		out = append(out, remoteRuleMatch{phase: phase, value: entity.measurements[id]})
		if !rule.perPhase {
			break
		}
	}
	return out
}

func (useCases *remoteUseCases) snapshot(observations []runtimeGraphObservation) RemoteMonitoringSnapshot {
	useCases.mu.Lock()
	defer useCases.mu.Unlock()
	seen := make(map[string]bool, len(observations))
	remotes := make([]RemoteMonitoringRemote, 0, len(observations))
	for _, observation := range observations {
		ski := strings.ToLower(strings.TrimSpace(observation.RemoteSKI))
		if ski == "" || seen[ski] {
			continue
		}
		seen[ski] = true
		remote := RemoteMonitoringRemote{
			SKI: ski, SHIPID: observation.ShipID, SHIPState: observation.SessionState,
			PairingState: observation.PairingState, Visible: observation.Visible, Paired: observation.Paired,
			ServiceName: observation.ServiceName, ServiceIdentifier: observation.ServiceIdentifier,
			ServiceBrand: observation.ServiceBrand, ServiceType: observation.ServiceType,
			ServiceModel: observation.ServiceModel,
		}
		if useCases.revoked != nil && useCases.revoked(ski) {
			delete(useCases.remotes, ski)
		} else {
			remote.Entities = useCases.entitiesFor(ski, observation.SessionIndex, observation.SessionState == "connected")
		}
		remotes = append(remotes, remote)
	}
	sort.Slice(remotes, func(i, j int) bool { return remotes[i].SKI < remotes[j].SKI })
	return RemoteMonitoringSnapshot{Remotes: remotes}
}

func (handler *runtimeServiceHandler) remoteMonitoringObservations() []runtimeGraphObservation {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	out := make([]runtimeGraphObservation, 0, len(handler.observations))
	for _, observation := range handler.observations {
		out = append(out, observation)
	}
	return out
}

func (handler *runtimeServiceHandler) remoteMonitoringSession(ski string) (uint64, bool) {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	observation, ok := handler.observations[ski]
	return observation.SessionIndex, ok && observation.SessionState == "connected"
}

func (backend *serviceBackend) RemoteMonitoringGet(context.Context) (RemoteMonitoringSnapshot, error) {
	backend.mu.Lock()
	closed := backend.closed
	useCases := backend.useCases
	handler := backend.handler
	backend.mu.Unlock()
	if closed || useCases == nil || handler == nil {
		return RemoteMonitoringSnapshot{}, errRemoteMonitoringUnavailable
	}
	return useCases.snapshot(handler.remoteMonitoringObservations()), nil
}

var _ RemoteMonitoringBackend = (*serviceBackend)(nil)

func remoteEntityDeviceMatches(entity spineapi.EntityRemoteInterface, ski string) bool {
	device := entity.Device()
	return !isNilRawRuntimeValue(device) && strings.EqualFold(device.Ski(), ski)
}

func (acquisition *runtimeAcquisition) registerUseCases() error {
	bridge := acquisition.rawFeatures
	revoked := func(ski string) bool {
		bridge.mu.Lock()
		predicate := bridge.remoteSubscriptions.revoked
		bridge.mu.Unlock()
		return predicate != nil && predicate(ski)
	}
	useCases, err := registerRemoteUseCases(
		acquisition.service, acquisition.dependencies.now, revoked, acquisition.handler.remoteMonitoringSession,
	)
	if err != nil {
		return acquisition.closeRuntimeWithSPINE(fmt.Errorf("register eebus use cases: %w", err))
	}
	acquisition.useCases = useCases
	unsubscribe := acquisition.unsubscribeSPINE
	acquisition.unsubscribeSPINE = func() error {
		useCases.stop()
		return unsubscribe()
	}
	return nil
}
