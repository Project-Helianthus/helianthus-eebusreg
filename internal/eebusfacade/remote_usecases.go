package eebusfacade

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	eebusapi "github.com/Project-Helianthus/helianthus-eebus-go/api"
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
	EntityType    string
	UseCase       string
	Actor         string
	Scenarios     []uint
	Observations  []RemoteMonitoringObservation
}

// RemoteMonitoringObservation is one value with its unit, update time, source message and stale flag.
type RemoteMonitoringObservation struct {
	Scenario uint
	Name     string
	Phase    string
	Number   float64
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
}

var remoteUseCaseRules = []remoteUseCaseRule{
	{useCase: spinemodel.UseCaseNameTypeMonitoringOfGridConnectionPoint, actor: spinemodel.UseCaseActorTypeGridConnectionPoint,
		scenario: 1, name: "powerLimitationFactor", unit: "%", configKey: spinemodel.DeviceConfigurationKeyNameTypePvCurtailmentLimitFactor},
	{useCase: spinemodel.UseCaseNameTypeMonitoringOfGridConnectionPoint, actor: spinemodel.UseCaseActorTypeGridConnectionPoint,
		scenario: 2, name: "power", measurement: spinemodel.MeasurementTypeTypePower, scope: spinemodel.ScopeTypeTypeACPowerTotal, unit: "W"},
	{useCase: spinemodel.UseCaseNameTypeMonitoringOfGridConnectionPoint, actor: spinemodel.UseCaseActorTypeGridConnectionPoint,
		scenario: 3, name: "energyFeedIn", measurement: spinemodel.MeasurementTypeTypeEnergy, scope: spinemodel.ScopeTypeTypeGridFeedIn, unit: "Wh"},
	{useCase: spinemodel.UseCaseNameTypeMonitoringOfGridConnectionPoint, actor: spinemodel.UseCaseActorTypeGridConnectionPoint,
		scenario: 4, name: "energyConsumed", measurement: spinemodel.MeasurementTypeTypeEnergy, scope: spinemodel.ScopeTypeTypeGridConsumption, unit: "Wh"},
	{useCase: spinemodel.UseCaseNameTypeMonitoringOfGridConnectionPoint, actor: spinemodel.UseCaseActorTypeGridConnectionPoint,
		scenario: 5, name: "currentPerPhase", measurement: spinemodel.MeasurementTypeTypeCurrent, scope: spinemodel.ScopeTypeTypeACCurrent, unit: "A", perPhase: true},
	{useCase: spinemodel.UseCaseNameTypeMonitoringOfGridConnectionPoint, actor: spinemodel.UseCaseActorTypeGridConnectionPoint,
		scenario: 6, name: "voltagePerPhase", measurement: spinemodel.MeasurementTypeTypeVoltage, scope: spinemodel.ScopeTypeTypeACVoltage, unit: "V", perPhase: true},
	{useCase: spinemodel.UseCaseNameTypeMonitoringOfGridConnectionPoint, actor: spinemodel.UseCaseActorTypeGridConnectionPoint,
		scenario: 7, name: "frequency", measurement: spinemodel.MeasurementTypeTypeFrequency, scope: spinemodel.ScopeTypeTypeACFrequency, unit: "Hz"},
	{useCase: spinemodel.UseCaseNameTypeMonitoringOfPowerConsumption, actor: spinemodel.UseCaseActorTypeMonitoredUnit,
		scenario: 1, name: "power", measurement: spinemodel.MeasurementTypeTypePower, scope: spinemodel.ScopeTypeTypeACPowerTotal, unit: "W"},
	{useCase: spinemodel.UseCaseNameTypeMonitoringOfPowerConsumption, actor: spinemodel.UseCaseActorTypeMonitoredUnit,
		scenario: 1, name: "powerPerPhase", measurement: spinemodel.MeasurementTypeTypePower, scope: spinemodel.ScopeTypeTypeACPower, unit: "W", perPhase: true},
	{useCase: spinemodel.UseCaseNameTypeMonitoringOfPowerConsumption, actor: spinemodel.UseCaseActorTypeMonitoredUnit,
		scenario: 2, name: "energyConsumed", measurement: spinemodel.MeasurementTypeTypeEnergy, scope: spinemodel.ScopeTypeTypeACEnergyConsumed, unit: "Wh"},
	{useCase: spinemodel.UseCaseNameTypeMonitoringOfPowerConsumption, actor: spinemodel.UseCaseActorTypeMonitoredUnit,
		scenario: 2, name: "energyProduced", measurement: spinemodel.MeasurementTypeTypeEnergy, scope: spinemodel.ScopeTypeTypeACEnergyProduced, unit: "Wh"},
	{useCase: spinemodel.UseCaseNameTypeMonitoringOfPowerConsumption, actor: spinemodel.UseCaseActorTypeMonitoredUnit,
		scenario: 3, name: "currentPerPhase", measurement: spinemodel.MeasurementTypeTypeCurrent, scope: spinemodel.ScopeTypeTypeACCurrent, unit: "A", perPhase: true},
	{useCase: spinemodel.UseCaseNameTypeMonitoringOfPowerConsumption, actor: spinemodel.UseCaseActorTypeMonitoredUnit,
		scenario: 4, name: "voltagePerPhase", measurement: spinemodel.MeasurementTypeTypeVoltage, scope: spinemodel.ScopeTypeTypeACVoltage, unit: "V", perPhase: true},
	{useCase: spinemodel.UseCaseNameTypeMonitoringOfPowerConsumption, actor: spinemodel.UseCaseActorTypeMonitoredUnit,
		scenario: 5, name: "frequency", measurement: spinemodel.MeasurementTypeTypeFrequency, scope: spinemodel.ScopeTypeTypeACFrequency, unit: "Hz"},
}

type remoteRetainedValue struct {
	number  float64
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
	grid := mgcp.NewMGCP(cem, callback)
	consumption := mpc.NewMPC(cem, callback)
	useCases := &remoteUseCases{
		local: local, now: now, revoked: revoked, session: session,
		remotes: make(map[string]*remoteRetainedRemote),
		clients: []remoteUseCaseClient{
			{client: grid, base: grid.UseCaseBase, stop: grid.Stop},
			{client: consumption, base: consumption.UseCaseBase, stop: consumption.Stop},
		},
	}
	adder.AddUseCase(grid)
	adder.AddUseCase(consumption)
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
	at := useCases.now()
	useCases.mu.Lock()
	defer useCases.mu.Unlock()
	if useCases.stopped {
		return
	}
	remote := useCases.remoteLocked(ski, payload.Device)
	if nodeData, ok := payload.Data.(*spinemodel.NodeManagementUseCaseDataType); ok {
		useCases.recordAnnouncements(remote, payload.Device, nodeData)
		return
	}
	if isNilRawRuntimeValue(payload.Entity) {
		return
	}
	address := payload.Entity.Address()
	if address == nil {
		return
	}
	entity := remote.entityLocked(address.Entity, string(payload.Entity.EntityType()))
	value := func(number float64) remoteRetainedValue {
		return remoteRetainedValue{number: number, at: at, source: source, session: index}
	}
	switch data := payload.Data.(type) {
	case *spinemodel.MeasurementDescriptionListDataType:
		for _, item := range data.MeasurementDescriptionData {
			if item.MeasurementId == nil {
				continue
			}
			description := remoteRetainedDescription{}
			if item.MeasurementType != nil {
				description.measurement = *item.MeasurementType
			}
			if item.CommodityType != nil {
				description.commodity = *item.CommodityType
			}
			if item.ScopeType != nil {
				description.scope = *item.ScopeType
			}
			if item.Unit != nil {
				description.unit = string(*item.Unit)
			}
			entity.descriptions[uint(*item.MeasurementId)] = description
		}
	case *spinemodel.ElectricalConnectionParameterDescriptionListDataType:
		for _, item := range data.ElectricalConnectionParameterDescriptionData {
			if item.MeasurementId == nil || item.AcMeasuredPhases == nil {
				continue
			}
			entity.phases[uint(*item.MeasurementId)] = string(*item.AcMeasuredPhases)
		}
	case *spinemodel.DeviceConfigurationKeyValueDescriptionListDataType:
		for _, item := range data.DeviceConfigurationKeyValueDescriptionData {
			if item.KeyId == nil || item.KeyName == nil {
				continue
			}
			entity.keyNames[uint(*item.KeyId)] = *item.KeyName
		}
	case *spinemodel.MeasurementListDataType:
		for _, item := range data.MeasurementData {
			if item.MeasurementId == nil || item.Value == nil || item.Value.Number == nil {
				continue
			}
			if item.ValueType != nil && *item.ValueType != spinemodel.MeasurementValueTypeTypeValue {
				continue
			}
			entity.measurements[uint(*item.MeasurementId)] = value(item.Value.GetValue())
		}
	case *spinemodel.DeviceConfigurationKeyValueListDataType:
		for _, item := range data.DeviceConfigurationKeyValueData {
			if item.KeyId == nil || item.Value == nil || item.Value.ScaledNumber == nil ||
				item.Value.ScaledNumber.Number == nil {
				continue
			}
			entity.config[uint(*item.KeyId)] = value(item.Value.ScaledNumber.GetValue())
		}
	}
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
) {
	for _, entity := range remote.entities {
		clear(entity.announcements)
	}
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
			entityType := ""
			if known := device.Entity(information.Address.Entity); !isNilRawRuntimeValue(known) {
				entityType = string(known.EntityType())
			}
			entity := remote.entityLocked(information.Address.Entity, entityType)
			scenarios := make([]uint, 0, len(support.ScenarioSupport))
			for _, scenario := range support.ScenarioSupport {
				scenarios = append(scenarios, uint(scenario))
			}
			sort.Slice(scenarios, func(i, j int) bool { return scenarios[i] < scenarios[j] })
			entity.announcements[*support.UseCaseName] = remoteRetainedAnnouncement{
				actor: *information.Actor, scenarios: scenarios,
			}
		}
	}
}

func remoteUseCaseKnown(name spinemodel.UseCaseNameType, actor spinemodel.UseCaseActorType) bool {
	for _, rule := range remoteUseCaseRules {
		if rule.useCase == name && rule.actor == actor {
			return true
		}
	}
	return false
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
	sort.Strings(keys)
	var out []RemoteMonitoringEntity
	for _, key := range keys {
		entity := remote.entities[key]
		names := make([]string, 0, len(entity.announcements))
		for name := range entity.announcements {
			names = append(names, string(name))
		}
		sort.Strings(names)
		for _, name := range names {
			announcement := entity.announcements[spinemodel.UseCaseNameType(name)]
			out = append(out, RemoteMonitoringEntity{
				DeviceAddress: remote.deviceAddress,
				DeviceType:    remote.deviceType,
				EntityAddress: append([]uint64(nil), entity.address...),
				EntityType:    entity.entityType,
				UseCase:       name,
				Actor:         string(announcement.actor),
				Scenarios:     append([]uint(nil), announcement.scenarios...),
				Observations:  entity.observations(spinemodel.UseCaseNameType(name), announcement, sessionIndex, connected),
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
	observation := func(rule remoteUseCaseRule, phase string, retained remoteRetainedValue) RemoteMonitoringObservation {
		return RemoteMonitoringObservation{
			Scenario: rule.scenario, Name: rule.name, Phase: phase, Number: retained.number, Unit: rule.unit,
			Updated: retained.at, Source: retained.source,
			Stale: !connected || retained.session != sessionIndex,
		}
	}
	var out []RemoteMonitoringObservation
	for _, rule := range remoteUseCaseRules {
		if rule.useCase != useCase || rule.actor != announcement.actor || !announced[rule.scenario] {
			continue
		}
		if rule.configKey != "" {
			ids := make([]uint, 0)
			for id, name := range entity.keyNames {
				if name == rule.configKey {
					ids = append(ids, id)
				}
			}
			sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
			for _, id := range ids {
				if retained, ok := entity.config[id]; ok {
					out = append(out, observation(rule, "", retained))
					break
				}
			}
			continue
		}
		ids := make([]uint, 0)
		for id, description := range entity.descriptions {
			matches := description.measurement == rule.measurement &&
				description.commodity == spinemodel.CommodityTypeTypeElectricity &&
				description.scope == rule.scope &&
				(description.unit == "" || description.unit == rule.unit)
			if _, has := entity.measurements[id]; matches && has {
				ids = append(ids, id)
			}
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, id := range ids {
			phase := ""
			if rule.perPhase {
				var known bool
				if phase, known = entity.phases[id]; !known {
					continue
				}
			}
			out = append(out, observation(rule, phase, entity.measurements[id]))
			if !rule.perPhase {
				break
			}
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
