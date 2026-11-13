package eebusfacade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	eebusapi "github.com/Project-Helianthus/helianthus-eebus-go/api"
	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
)

type runtimeDeviceProvider interface {
	LocalDevice() spineapi.DeviceLocalInterface
}

type runtimeServiceHandler struct {
	mu        sync.Mutex
	publishMu sync.Mutex
	spineWG   sync.WaitGroup
	spineWork sync.WaitGroup

	runtimeID                 string
	localSKI                  string
	reducer                   *runtimeObservationReducer
	observations              map[string]runtimeGraphObservation
	projectionCapture         func() trustAdminProjection
	projectionLivenessAllowed func(string) bool
	projectionRemotes         []string
	policyRemotes             []string
	runtimeRevision           uint64
	publishedRuntimeRevision  uint64
	publishedTrustRevision    uint64
	now                       func() time.Time
	snapshotSerializer        runtimeSnapshotSerializer
	publish                   func([]byte)
	errors                    chan error
	spineService              runtimeService
	spineEventsActive         bool
	spineGeneration           uint64
	spineCancel               context.CancelFunc
	spineWake                 chan struct{}
	spinePending              map[string]runtimeSPINERefresh
	rawFeatures               *rawFeatureRuntimeBridge
}

func newRuntimeServiceHandler(config RuntimeConfig, localSKI string, now func() time.Time) (*runtimeServiceHandler, error) {
	localSKI = strings.ToLower(strings.TrimSpace(localSKI))
	if !validRuntimeSKI(localSKI) {
		return nil, errors.New("runtime service local SKI is invalid")
	}
	if now == nil {
		return nil, errors.New("runtime service clock is required")
	}
	handler := &runtimeServiceHandler{
		runtimeID:          "runtime:" + localSKI,
		localSKI:           localSKI,
		reducer:            newRuntimeObservationReducer(),
		observations:       make(map[string]runtimeGraphObservation, len(config.Remotes)),
		now:                now,
		snapshotSerializer: marshalRuntimeSnapshotWithIdentity,
		errors:             make(chan error, 1),
	}
	for _, remote := range config.Remotes {
		handler.policyRemotes = append(handler.policyRemotes, strings.ToLower(strings.TrimSpace(remote.SKI)))
	}
	sort.Strings(handler.policyRemotes)
	return handler, nil
}

func (handler *runtimeServiceHandler) setSnapshotSerializer(serializer runtimeSnapshotSerializer) {
	if serializer == nil {
		serializer = marshalRuntimeSnapshotWithIdentity
	}
	handler.mu.Lock()
	handler.snapshotSerializer = serializer
	handler.mu.Unlock()
}

func (handler *runtimeServiceHandler) setPublisher(publish func([]byte)) {
	handler.mu.Lock()
	handler.publish = publish
	handler.mu.Unlock()
}

func (handler *runtimeServiceHandler) bindRawFeatureRuntime(bridge *rawFeatureRuntimeBridge) {
	handler.mu.Lock()
	handler.rawFeatures = bridge
	handler.mu.Unlock()
}

func (handler *runtimeServiceHandler) RemoteSKIConnected(service eebusapi.ServiceInterface, ski string) {
	ski = strings.ToLower(strings.TrimSpace(ski))
	if !handler.remoteLivenessAllowed(ski) {
		return
	}
	devices, err := runtimeDevicesForRemote(service, ski)
	if err != nil {
		handler.report(err)
		return
	}
	currentDevices, err := exactRuntimeDeviceCollection(devices)
	if err != nil {
		handler.report(err)
		return
	}
	generation, allocationErr := handler.allocateRawFeatureConnectionGeneration(ski)
	if allocationErr != nil {
		handler.report(allocationErr)
		return
	}
	if generation == 0 {
		handler.report(errors.New("runtime connection generation is exhausted"))
		return
	}
	handler.updateRemote(ski, true, func(observation *runtimeGraphObservation) {
		if len(observation.ServiceIDs) == 0 {
			observation.ServiceIDs = []string{"service:" + ski}
		}
		observation.SessionIndex = generation
		observation.ShipID = ""
		observation.SessionID = runtimeSessionIdentity(*observation)
		observation.SessionState = "connected"
		observation.Visible = true
		observation.Since = handler.timestamp()
		observation.Devices = currentDevices
	})
	handler.refreshRawFeatureRemote(ski)
}

func (handler *runtimeServiceHandler) allocateRawFeatureConnectionGeneration(ski string) (uint64, error) {
	handler.mu.Lock()
	bridge := handler.rawFeatures
	observation := handler.observations[ski]
	handler.mu.Unlock()
	if observation.SessionIndex == ^uint64(0) {
		return 0, errors.New("runtime connection generation is exhausted")
	}
	candidate := observation.SessionIndex + 1
	if bridge == nil {
		return candidate, nil
	}
	generation, err := bridge.allocateConnectionGeneration(ski, candidate)
	if err != nil {
		return 0, err
	}
	return generation, nil
}

func (handler *runtimeServiceHandler) RemoteSKIDisconnected(_ eebusapi.ServiceInterface, ski string) {
	ski = strings.ToLower(strings.TrimSpace(ski))
	if !handler.remoteLivenessAllowed(ski) {
		return
	}
	handler.updateRemote(ski, false, func(observation *runtimeGraphObservation) {
		if observation.SessionID == "" {
			return
		}
		observation.SessionState = "disconnected"
		observation.Since = handler.timestamp()
		observation.Devices = nil
	})
	handler.retireRawFeatureRemote(ski)
}

func (handler *runtimeServiceHandler) VisibleRemoteServicesUpdated(_ eebusapi.ServiceInterface, entries []shipapi.RemoteService) {
	visible := make(map[string]shipapi.RemoteService, len(entries))
	for _, entry := range entries {
		ski := strings.ToLower(strings.TrimSpace(entry.Ski))
		if validRuntimeSKI(ski) {
			entry.Ski = ski
			visible[ski] = entry
		}
	}

	handler.mu.Lock()
	changed := false
	for ski, entry := range visible {
		if !handler.remoteLivenessAllowedLocked(ski) {
			continue
		}
		observation, exists := handler.observations[ski]
		if !exists {
			observation = handler.newRemoteObservation(ski)
			observation.Visible = true
			observation.ServiceIDs = []string{"service:" + ski}
		}
		before := observation
		mergeRuntimeRemoteService(&observation, entry)
		observation.Visible = true
		if exists && runtimeServiceObservationEqual(before, observation) {
			continue
		}
		observation.Since = handler.timestamp()
		if err := handler.reducer.Replace(observation); err != nil {
			handler.mu.Unlock()
			handler.report(err)
			return
		}
		handler.observations[ski] = observation
		handler.runtimeRevision++
		changed = true
	}
	for ski, observation := range handler.observations {
		if !handler.remoteLivenessAllowedLocked(ski) {
			continue
		}
		_, isVisible := visible[ski]
		if observation.Visible == isVisible {
			continue
		}
		observation.Visible = isVisible
		observation.Since = handler.timestamp()
		if err := handler.reducer.Replace(observation); err != nil {
			handler.mu.Unlock()
			handler.report(err)
			return
		}
		handler.observations[ski] = observation
		handler.runtimeRevision++
		changed = true
	}
	handler.mu.Unlock()
	if changed {
		handler.publishOrReport()
	}
}

func mergeRuntimeRemoteService(observation *runtimeGraphObservation, service shipapi.RemoteService) {
	if value := strings.TrimSpace(service.Name); value != "" {
		observation.ServiceName = value
	}
	if value := strings.TrimSpace(service.Identifier); value != "" {
		observation.ServiceIdentifier = value
	}
	if value := strings.TrimSpace(service.Brand); value != "" {
		observation.ServiceBrand = value
	}
	if value := strings.TrimSpace(service.Type); value != "" {
		observation.ServiceType = value
	}
	if value := strings.TrimSpace(service.Model); value != "" {
		observation.ServiceModel = value
	}
}

func runtimeServiceObservationEqual(left, right runtimeGraphObservation) bool {
	return left.Visible == right.Visible &&
		left.ServiceName == right.ServiceName &&
		left.ServiceIdentifier == right.ServiceIdentifier &&
		left.ServiceBrand == right.ServiceBrand &&
		left.ServiceType == right.ServiceType &&
		left.ServiceModel == right.ServiceModel
}

func (handler *runtimeServiceHandler) ServiceShipIDUpdate(ski string, shipID string) {
	ski = strings.ToLower(strings.TrimSpace(ski))
	if !handler.remoteLivenessAllowed(ski) {
		return
	}
	shipID = strings.TrimSpace(shipID)
	if shipID == "" {
		return
	}
	handler.updateRemote(ski, false, func(observation *runtimeGraphObservation) {
		observation.ShipID = shipID
		if observation.SessionIndex != 0 {
			observation.SessionID = runtimeSessionIdentity(*observation)
		}
		observation.Since = handler.timestamp()
	})
	handler.refreshRawFeatureRemote(ski)
}

func (handler *runtimeServiceHandler) refreshRawFeatureRemote(ski string) {
	handler.mu.Lock()
	bridge := handler.rawFeatures
	service := handler.spineService
	observation, ok := handler.observations[ski]
	handler.mu.Unlock()
	if bridge == nil || service == nil || !ok ||
		observation.SessionState != "connected" ||
		observation.ShipID == "" ||
		observation.SessionIndex == 0 ||
		service.LocalDevice() == nil {
		return
	}
	remote := service.LocalDevice().RemoteDeviceForSki(ski)
	if isNilRawRuntimeValue(remote) || remote.Address() == nil {
		return
	}
	err := bridge.refreshRemote(ski, observation.ShipID, observation.SessionIndex, remote)
	if errors.Is(err, errRawRemoteNotAdmitted) {
		err = bridge.admitRemote(ski, observation.ShipID, observation.SessionIndex, remote)
	}
	if err != nil {
		handler.report(err)
	}
}

func (handler *runtimeServiceHandler) retireRawFeatureRemote(ski string) {
	handler.mu.Lock()
	bridge := handler.rawFeatures
	observation, ok := handler.observations[ski]
	handler.mu.Unlock()
	if bridge != nil && ok && observation.SessionIndex > 0 {
		bridge.retireRemote(ski, observation.SessionIndex)
	}
}

func runtimeSessionIdentity(observation runtimeGraphObservation) string {
	seed := observation.RemoteSKI
	if observation.ShipID != "" {
		seed = observation.ShipID
	}
	return fmt.Sprintf("session:%s:%d", seed, observation.SessionIndex)
}

func (handler *runtimeServiceHandler) ServicePairingDetailUpdate(ski string, detail *shipapi.ConnectionStateDetail) {
	ski = strings.ToLower(strings.TrimSpace(ski))
	if !handler.remoteLivenessAllowed(ski) {
		return
	}
	sessionState := ""
	if detail != nil {
		switch detail.State() {
		case shipapi.ConnectionStateRemoteDeniedTrust:
			sessionState = "degraded"
		case shipapi.ConnectionStateError:
			sessionState = "degraded"
		}
	}
	handler.updateRemote(ski, false, func(observation *runtimeGraphObservation) {
		if sessionState == "degraded" && observation.SessionID != "" {
			observation.SessionState = sessionState
		}
		observation.Since = handler.timestamp()
	})
}

func (handler *runtimeServiceHandler) updateRemote(ski string, create bool, update func(*runtimeGraphObservation)) {
	handler.mu.Lock()
	observation, ok := handler.observations[ski]
	if !ok {
		if !create {
			handler.mu.Unlock()
			return
		}
		observation = handler.newRemoteObservation(ski)
	}
	update(&observation)
	if err := handler.reducer.Replace(observation); err != nil {
		handler.mu.Unlock()
		handler.report(err)
		return
	}
	handler.observations[ski] = observation
	handler.runtimeRevision++
	handler.mu.Unlock()
	handler.publishOrReport()
}

func (handler *runtimeServiceHandler) newRemoteObservation(ski string) runtimeGraphObservation {
	return runtimeGraphObservation{
		RuntimeID: handler.runtimeID,
		LocalSKI:  handler.localSKI,
		RemoteSKI: ski,
		Since:     handler.timestamp(),
	}
}

func (handler *runtimeServiceHandler) publishOrReport() {
	if err := handler.publishCurrent(); err != nil {
		handler.report(err)
	}
}

func (handler *runtimeServiceHandler) publishCurrent() error {
	handler.mu.Lock()
	capture := handler.projectionCapture
	revision := handler.runtimeRevision
	graph := handler.reducer.Snapshot()
	handler.mu.Unlock()
	if capture != nil {
		return handler.publishTrustAdminProjection(capture())
	}
	return handler.publishRuntimeGraphAtRevision(graph, revision)
}

func (handler *runtimeServiceHandler) publishTrustAdminProjection(projection trustAdminProjection) error {
	graph, remotes, runtimeRevision := handler.runtimeGraphAndProjectionRemotes()
	handler.publishMu.Lock()
	defer handler.publishMu.Unlock()
	if projection.revision < handler.publishedTrustRevision ||
		runtimeRevision < handler.publishedRuntimeRevision {
		return nil
	}
	applyTrustAdminProjection(graph, remotes, projection)
	if err := handler.publishRuntimeGraph(graph); err != nil {
		return err
	}
	handler.publishedRuntimeRevision = runtimeRevision
	handler.publishedTrustRevision = projection.revision
	return nil
}

func (handler *runtimeServiceHandler) remoteLivenessAllowed(ski string) bool {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	return handler.remoteLivenessAllowedLocked(ski)
}

func (handler *runtimeServiceHandler) remoteLivenessAllowedLocked(ski string) bool {
	return handler.projectionLivenessAllowed == nil || handler.projectionLivenessAllowed(ski)
}

func (handler *runtimeServiceHandler) publishRuntimeGraph(graph []runtimeGraphObservation) error {
	handler.mu.Lock()
	publish := handler.publish
	serializer := handler.snapshotSerializer
	handler.mu.Unlock()
	if publish == nil {
		return nil
	}
	if serializer == nil {
		serializer = marshalRuntimeSnapshotWithIdentity
	}
	payload, err := serializer(handler.runtimeID, handler.localSKI, graph, handler.timestamp())
	if err != nil {
		return err
	}
	publish(payload)
	return nil
}

func (handler *runtimeServiceHandler) publishRuntimeGraphAtRevision(graph []runtimeGraphObservation, revision uint64) error {
	handler.publishMu.Lock()
	defer handler.publishMu.Unlock()
	if revision < handler.publishedRuntimeRevision {
		return nil
	}
	if err := handler.publishRuntimeGraph(graph); err != nil {
		return err
	}
	handler.publishedRuntimeRevision = revision
	return nil
}

func (handler *runtimeServiceHandler) runtimeGraphAndProjectionRemotes() ([]runtimeGraphObservation, []string, uint64) {
	handler.mu.Lock()
	remotes := append([]string(nil), handler.projectionRemotes...)
	revision := handler.runtimeRevision
	graph := handler.reducer.Snapshot()
	handler.mu.Unlock()
	return graph, remotes, revision
}

func (handler *runtimeServiceHandler) report(err error) {
	if err == nil {
		return
	}
	select {
	case handler.errors <- err:
	default:
	}
}

func (handler *runtimeServiceHandler) timestamp() time.Time {
	value := handler.now().UTC()
	if value.IsZero() {
		return time.Unix(0, 0).UTC()
	}
	return value
}

func runtimeDevicesForRemote(service runtimeDeviceProvider, ski string) ([]runtimeDeviceObservation, error) {
	if service == nil || service.LocalDevice() == nil {
		return nil, nil
	}
	remote := service.LocalDevice().RemoteDeviceForSki(ski)
	return runtimeDevicesForRemoteDevice(remote, ski)
}

func runtimeDevicesForRemoteDevice(remote spineapi.DeviceRemoteInterface, ski string) ([]runtimeDeviceObservation, error) {
	if remote == nil {
		return nil, nil
	}
	address := remote.Address()
	deviceType := remote.DeviceType()
	if address == nil || deviceType == nil {
		return nil, nil
	}
	deviceID, err := runtimeIdentity("device", ski, remote.Address())
	if err != nil {
		return nil, err
	}
	device := runtimeDeviceObservation{
		ID: deviceID, SKI: ski, Address: string(*address), Type: string(*deviceType),
	}
	destination := remote.DestinationData()
	if description := destination.DeviceDescription; description != nil {
		device.Description = cloneRuntimeDescription(description.Description)
		device.Metadata = runtimeDeviceMetadata(remote.FeatureSet(), description)
		value, err := detachedRuntimeJSONValue(destination)
		if err != nil {
			return nil, err
		}
		device.Opaque = []runtimeOpaquePayload{{
			Path: "/devices/" + device.Address + "/destination_data", Source: "spine.detailed-discovery", Value: value,
		}}
	} else if featureSet := remote.FeatureSet(); featureSet != nil {
		device.Metadata = map[string]string{"network_feature_set": string(*featureSet)}
	}
	rolesByAddress := make(map[string]string)
	for _, entity := range remote.Entities() {
		observation, absences, err := runtimeEntityForRemote(entity, device.Address, ski, rolesByAddress)
		if err != nil {
			return nil, err
		}
		device.Absences = append(device.Absences, absences...)
		if observation != nil {
			device.Entities = append(device.Entities, *observation)
		}
	}
	for _, information := range remote.UseCases() {
		useCases, absences, err := runtimeUseCasesForInformation(information, device.Address, ski, rolesByAddress)
		if err != nil {
			return nil, err
		}
		device.Absences = append(device.Absences, absences...)
		for _, observation := range useCases {
			device.UseCaseIDs = append(device.UseCaseIDs, observation.ID)
			device.UseCases = append(device.UseCases, observation)
		}
	}
	return []runtimeDeviceObservation{device}, nil
}

func runtimeEntityForRemote(entity spineapi.EntityRemoteInterface, deviceAddress, ski string, rolesByAddress map[string]string) (*runtimeEntityObservation, []runtimeAbsenceObservation, error) {
	if entity == nil {
		return nil, []runtimeAbsenceObservation{{Kind: "entity", Reason: "missing_address", DeviceAddress: deviceAddress}}, nil
	}
	entityAddress := entity.Address()
	entityType := entity.EntityType()
	if entityAddress == nil || entityType == "" {
		absence := runtimeAbsenceObservation{Kind: "entity", Reason: "missing_address", DeviceAddress: deviceAddress}
		if entityAddress != nil {
			absence.Reason, absence.EntityAddress = "missing_type", entityAddress.String()
		}
		absences := []runtimeAbsenceObservation{absence}
		for _, feature := range entity.Features() {
			absences = append(absences, runtimeFeatureAbsence(feature, deviceAddress, absence.EntityAddress, "missing_container"))
		}
		return nil, absences, nil
	}
	entityID, err := runtimeIdentity("entity", ski, entityAddress.String())
	if err != nil {
		return nil, nil, err
	}
	entityObservation := runtimeEntityObservation{
		ID: entityID, DeviceAddress: deviceAddress, EntityAddress: entityAddress.String(),
		ParentEntityAddress: runtimeParentEntityAddress(entityAddress),
		Type:                string(entityType), Description: cloneRuntimeDescription(entity.Description()),
	}
	var absences []runtimeAbsenceObservation
	for _, feature := range entity.Features() {
		if feature == nil {
			absences = append(absences, runtimeFeatureAbsence(nil, deviceAddress, entityObservation.EntityAddress, "missing_address"))
			continue
		}
		featureAddress := feature.Address()
		featureType := feature.Type()
		if featureAddress == nil || featureType == "" {
			reason := "missing_type"
			if featureAddress == nil {
				reason = "missing_address"
			}
			absences = append(absences, runtimeFeatureAbsence(feature, deviceAddress, entityObservation.EntityAddress, reason))
			continue
		}
		featureID, err := runtimeIdentity("feature", ski, featureAddress.String())
		if err != nil {
			return nil, nil, err
		}
		role := strings.ToLower(string(feature.Role()))
		rolesByAddress[featureAddress.String()] = role
		entityObservation.Features = append(entityObservation.Features, runtimeFeatureObservation{
			ID: featureID, DeviceAddress: deviceAddress, EntityAddress: entityObservation.EntityAddress,
			FeatureAddress: featureAddress.String(), Type: string(featureType), Role: role,
			Description: cloneRuntimeDescription(feature.Description()),
		})
	}
	return &entityObservation, absences, nil
}

func runtimeFeatureAbsence(feature spineapi.FeatureRemoteInterface, deviceAddress, entityAddress, reason string) runtimeAbsenceObservation {
	absence := runtimeAbsenceObservation{Kind: "feature", Reason: reason, DeviceAddress: deviceAddress, EntityAddress: entityAddress}
	if feature != nil {
		if address := feature.Address(); address != nil {
			absence.FeatureAddress = address.String()
		}
	}
	return absence
}

func runtimeParentEntityAddress(address *spinemodel.EntityAddressType) string {
	if address == nil || len(address.Entity) < 2 {
		return ""
	}
	parent := spinemodel.EntityAddressType{Device: address.Device, Entity: address.Entity[:len(address.Entity)-1]}
	return parent.String()
}

func runtimeUseCaseScope(address *spinemodel.FeatureAddressType, deviceAddress string) runtimeAbsenceObservation {
	scope := runtimeAbsenceObservation{Kind: "use_case", DeviceAddress: deviceAddress}
	if address == nil {
		return scope
	}
	device := spinemodel.AddressDeviceType(deviceAddress)
	if address.Device != nil {
		device = *address.Device
		scope.DeviceAddress = string(device)
	}
	if len(address.Entity) > 0 {
		entity := spinemodel.EntityAddressType{Device: &device, Entity: address.Entity}
		scope.EntityAddress = entity.String()
	}
	if address.Feature != nil {
		feature := spinemodel.FeatureAddressType{Device: &device, Entity: address.Entity, Feature: address.Feature}
		scope.FeatureAddress = feature.String()
	}
	return scope
}

func runtimeUseCasesForInformation(information spinemodel.UseCaseInformationDataType, deviceAddress, ski string, rolesByAddress map[string]string) ([]runtimeUseCaseObservation, []runtimeAbsenceObservation, error) {
	scope := runtimeUseCaseScope(information.Address, deviceAddress)
	if information.Address == nil || information.Actor == nil {
		scope.Reason = "missing_actor"
		if information.Address == nil {
			scope.Reason = "missing_address"
		}
		return nil, []runtimeAbsenceObservation{scope}, nil
	}
	contextAddress := information.Address.String()
	actor := string(*information.Actor)
	var result []runtimeUseCaseObservation
	var absences []runtimeAbsenceObservation
	for _, support := range information.UseCaseSupport {
		if support.UseCaseName == nil {
			absence := scope
			absence.Reason = "missing_name"
			absences = append(absences, absence)
			continue
		}
		useCaseID, err := runtimeIdentity(
			"usecase", ski, contextAddress, actor, string(*support.UseCaseName),
			cloneRuntimeSpecificationVersion(support.UseCaseVersion),
			cloneRuntimeString(support.UseCaseDocumentSubRevision),
		)
		if err != nil {
			return nil, nil, err
		}
		scenarios := make([]string, len(support.ScenarioSupport))
		for scenarioIndex, scenario := range support.ScenarioSupport {
			scenarios[scenarioIndex] = fmt.Sprint(scenario)
		}
		var resolvedRole *string
		if role, ok := rolesByAddress[contextAddress]; ok {
			resolvedRole = runtimeStringPointer(role)
		}
		result = append(result, runtimeUseCaseObservation{
			ID: useCaseID, ContextAddress: contextAddress, DeviceAddress: scope.DeviceAddress,
			EntityAddress: scope.EntityAddress, FeatureAddress: scope.FeatureAddress,
			Name: string(*support.UseCaseName), Actor: actor, ResolvedRole: resolvedRole, Scenarios: scenarios,
			Version:             cloneRuntimeSpecificationVersion(support.UseCaseVersion),
			Availability:        cloneRuntimeBool(support.UseCaseAvailable),
			DocumentSubrevision: cloneRuntimeString(support.UseCaseDocumentSubRevision),
		})
	}
	return result, absences, nil
}

func runtimeDeviceMetadata(
	featureSet *spinemodel.NetworkManagementFeatureSetType,
	description *spinemodel.NetworkManagementDeviceDescriptionDataType,
) map[string]string {
	result := make(map[string]string)
	if featureSet != nil {
		result["network_feature_set"] = string(*featureSet)
	}
	if description.NetworkFeatureSet != nil {
		result["network_feature_set"] = string(*description.NetworkFeatureSet)
	}
	if description.NativeSetup != nil {
		result["native_setup"] = string(*description.NativeSetup)
	}
	if description.TechnologyAddress != nil {
		result["technology_address"] = string(*description.TechnologyAddress)
	}
	if description.CommunicationsTechnologyInformation != nil {
		result["communications_technology_information"] = string(*description.CommunicationsTechnologyInformation)
	}
	if description.LastStateChange != nil {
		result["last_state_change"] = string(*description.LastStateChange)
	}
	if description.MinimumTrustLevel != nil {
		result["minimum_trust_level"] = string(*description.MinimumTrustLevel)
	}
	if description.Label != nil {
		result["label"] = string(*description.Label)
	}
	if description.NetworkManagementResponsibleAddress != nil {
		result["network_management_responsible_address"] = description.NetworkManagementResponsibleAddress.String()
	}
	return result
}

func cloneRuntimeDescription(value *spinemodel.DescriptionType) *string {
	if value == nil {
		return nil
	}
	result := string(*value)
	return &result
}

func cloneRuntimeSpecificationVersion(value *spinemodel.SpecificationVersionType) *string {
	if value == nil {
		return nil
	}
	result := string(*value)
	return &result
}

func runtimeStringPointer(value string) *string {
	return &value
}

func runtimeIdentity(kind string, values ...any) (string, error) {
	payload, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("encode runtime %s identity: %w", kind, err)
	}
	return kind + ":" + string(payload), nil
}
