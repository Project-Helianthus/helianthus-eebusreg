package eebusfacade

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

type runtimeFeatureObservation struct {
	ID             string
	DeviceAddress  string
	EntityAddress  string
	FeatureAddress string
	Type           string
	Role           string
	Description    *string
}

type runtimeEntityObservation struct {
	ID                  string
	DeviceAddress       string
	EntityAddress       string
	ParentEntityAddress string
	Type                string
	Description         *string
	Features            []runtimeFeatureObservation
}

type runtimeUseCaseObservation struct {
	ID                  string
	ContextAddress      string
	DeviceAddress       string
	EntityAddress       string
	FeatureAddress      string
	Name                string
	Actor               string
	ResolvedRole        *string
	Scenarios           []string
	Version             *string
	Availability        *bool
	DocumentSubrevision *string
}

type runtimeDeviceObservation struct {
	ID          string
	SKI         string
	SHIPID      string
	Address     string
	Type        string
	Description *string
	Metadata    map[string]string
	Opaque      []runtimeOpaquePayload
	Entities    []runtimeEntityObservation
	UseCaseIDs  []string
	UseCases    []runtimeUseCaseObservation
	Absences    []runtimeAbsenceObservation
}

type runtimeAbsenceObservation struct {
	Kind           string
	Reason         string
	DeviceAddress  string
	EntityAddress  string
	FeatureAddress string
}

type runtimeGraphObservation struct {
	RuntimeID         string
	LocalSKI          string
	RemoteSKI         string
	SessionID         string
	SessionState      string
	PairingState      string
	Visible           bool
	Paired            bool
	Since             time.Time
	ServiceIDs        []string
	Devices           []runtimeDeviceObservation
	ShipID            string
	ServiceName       string
	ServiceIdentifier string
	ServiceBrand      string
	ServiceType       string
	ServiceModel      string
	SessionIndex      uint64
	TrustDegradation  string
}

type runtimeObservationReducer struct {
	mu sync.RWMutex

	initialized bool
	runtimeID   string
	localSKI    string
	remotes     map[string]runtimeGraphObservation
}

func newRuntimeObservationReducer() *runtimeObservationReducer {
	return &runtimeObservationReducer{remotes: make(map[string]runtimeGraphObservation)}
}

func (reducer *runtimeObservationReducer) Replace(source runtimeGraphObservation) error {
	observation, err := normalizeRuntimeGraphObservation(source)
	if err != nil {
		return err
	}

	reducer.mu.Lock()
	defer reducer.mu.Unlock()
	if reducer.initialized {
		if observation.RuntimeID != reducer.runtimeID {
			return errors.New("runtime observation changed the stable runtime identity")
		}
		if observation.LocalSKI != reducer.localSKI {
			return errors.New("runtime observation changed the persisted local identity")
		}
	} else {
		reducer.initialized = true
		reducer.runtimeID = observation.RuntimeID
		reducer.localSKI = observation.LocalSKI
	}
	reducer.remotes[observation.RemoteSKI] = cloneRuntimeGraphObservation(observation)
	return nil
}

func (reducer *runtimeObservationReducer) Snapshot() []runtimeGraphObservation {
	reducer.mu.RLock()
	result := make([]runtimeGraphObservation, 0, len(reducer.remotes))
	for _, observation := range reducer.remotes {
		result = append(result, cloneRuntimeGraphObservation(observation))
	}
	reducer.mu.RUnlock()
	sort.Slice(result, func(left, right int) bool {
		return result[left].RemoteSKI < result[right].RemoteSKI
	})
	return result
}

func normalizeRuntimeGraphObservation(source runtimeGraphObservation) (runtimeGraphObservation, error) {
	result := source
	result.RuntimeID = strings.TrimSpace(result.RuntimeID)
	result.LocalSKI = strings.TrimSpace(result.LocalSKI)
	result.RemoteSKI = strings.TrimSpace(result.RemoteSKI)
	result.SessionID = strings.TrimSpace(result.SessionID)
	result.SessionState = strings.TrimSpace(result.SessionState)
	result.PairingState = strings.TrimSpace(result.PairingState)
	result.TrustDegradation = strings.TrimSpace(result.TrustDegradation)
	result.ShipID = strings.TrimSpace(result.ShipID)
	result.ServiceName = strings.TrimSpace(result.ServiceName)
	result.ServiceIdentifier = strings.TrimSpace(result.ServiceIdentifier)
	result.ServiceBrand = strings.TrimSpace(result.ServiceBrand)
	result.ServiceType = strings.TrimSpace(result.ServiceType)
	result.ServiceModel = strings.TrimSpace(result.ServiceModel)
	if result.RuntimeID == "" || result.LocalSKI == "" || result.RemoteSKI == "" {
		return runtimeGraphObservation{}, errors.New("runtime graph identities are required")
	}
	if !validRuntimeSKI(result.LocalSKI) || !validRuntimeSKI(result.RemoteSKI) {
		return runtimeGraphObservation{}, errors.New("runtime graph SKIs must contain 40 hexadecimal characters")
	}
	switch result.SessionState {
	case "", "unknown", "connecting", "connected", "disconnected", "degraded":
	default:
		return runtimeGraphObservation{}, errors.New("runtime session state is unsupported")
	}
	if (result.SessionID == "") != (result.SessionState == "") {
		return runtimeGraphObservation{}, errors.New("runtime session identity and state must be observed together")
	}
	switch result.PairingState {
	case "", string(eebusraw.PairingStateUnknown), string(eebusraw.PairingStateUnpaired), string(eebusraw.PairingStatePaired), string(eebusraw.PairingStateDenied):
	default:
		return runtimeGraphObservation{}, errors.New("runtime pairing state is unsupported")
	}
	switch result.TrustDegradation {
	case "", "denied-trust", "certificate-unavailable":
	default:
		return runtimeGraphObservation{}, errors.New("runtime trust degradation is unsupported")
	}
	if result.Since.IsZero() {
		return runtimeGraphObservation{}, errors.New("runtime observation timestamp is required")
	}
	result.Since = result.Since.UTC()

	serviceIDs, err := uniqueRuntimeStrings(result.ServiceIDs, "service")
	if err != nil {
		return runtimeGraphObservation{}, err
	}
	result.ServiceIDs = serviceIDs

	devices := make(map[string]runtimeDeviceObservation, len(result.Devices))
	for _, sourceDevice := range result.Devices {
		device, err := normalizeRuntimeDeviceObservation(sourceDevice)
		if err != nil {
			return runtimeGraphObservation{}, err
		}
		if existing, ok := devices[device.ID]; ok {
			device, err = mergeRuntimeDeviceObservations(existing, device)
			if err != nil {
				return runtimeGraphObservation{}, err
			}
		}
		devices[device.ID] = device
	}
	result.Devices = make([]runtimeDeviceObservation, 0, len(devices))
	for _, device := range devices {
		result.Devices = append(result.Devices, device)
	}
	sort.Slice(result.Devices, func(left, right int) bool {
		return result.Devices[left].ID < result.Devices[right].ID
	})
	return result, nil
}

func normalizeRuntimeDeviceObservation(source runtimeDeviceObservation) (runtimeDeviceObservation, error) {
	result := source
	result.ID = strings.TrimSpace(result.ID)
	result.SKI = strings.TrimSpace(result.SKI)
	result.SHIPID = strings.TrimSpace(result.SHIPID)
	result.Address = strings.TrimSpace(result.Address)
	result.Type = strings.TrimSpace(result.Type)
	result.Description = cloneRuntimeString(result.Description)
	result.Metadata = cloneRuntimeMetadata(result.Metadata)
	opaque, err := normalizeRuntimeOpaque(result.Opaque)
	if err != nil {
		return runtimeDeviceObservation{}, err
	}
	result.Opaque = opaque
	if result.ID == "" {
		return runtimeDeviceObservation{}, errors.New("runtime device identity is required")
	}
	useCaseIDs, err := uniqueRuntimeStrings(result.UseCaseIDs, "use case")
	if err != nil {
		return runtimeDeviceObservation{}, err
	}
	result.UseCaseIDs = useCaseIDs
	useCases := make(map[string]runtimeUseCaseObservation, len(result.UseCases))
	for _, value := range result.UseCases {
		value.ID = strings.TrimSpace(value.ID)
		value.ContextAddress = strings.TrimSpace(value.ContextAddress)
		value.DeviceAddress = strings.TrimSpace(value.DeviceAddress)
		value.EntityAddress = strings.TrimSpace(value.EntityAddress)
		value.FeatureAddress = strings.TrimSpace(value.FeatureAddress)
		value.Name = strings.TrimSpace(value.Name)
		value.Actor = strings.TrimSpace(value.Actor)
		value.ResolvedRole = cloneRuntimeString(value.ResolvedRole)
		value.Version = cloneRuntimeString(value.Version)
		value.Availability = cloneRuntimeBool(value.Availability)
		value.DocumentSubrevision = cloneRuntimeString(value.DocumentSubrevision)
		value.Scenarios = append([]string(nil), value.Scenarios...)
		for index := range value.Scenarios {
			value.Scenarios[index] = strings.TrimSpace(value.Scenarios[index])
		}
		sort.Strings(value.Scenarios)
		if value.ID == "" {
			return runtimeDeviceObservation{}, errors.New("runtime use-case identity is required")
		}
		if existing, ok := useCases[value.ID]; ok {
			value = mergeRuntimeUseCaseObservations(existing, value)
		}
		useCases[value.ID] = value
	}
	result.UseCases = make([]runtimeUseCaseObservation, 0, len(useCases))
	for _, value := range useCases {
		result.UseCases = append(result.UseCases, value)
	}
	sort.Slice(result.UseCases, func(left, right int) bool {
		return result.UseCases[left].ID < result.UseCases[right].ID
	})

	entities := make(map[string]runtimeEntityObservation, len(result.Entities))
	for _, sourceEntity := range result.Entities {
		entity, err := normalizeRuntimeEntityObservation(sourceEntity)
		if err != nil {
			return runtimeDeviceObservation{}, err
		}
		if existing, ok := entities[entity.ID]; ok {
			entity, err = mergeRuntimeEntityObservations(existing, entity)
			if err != nil {
				return runtimeDeviceObservation{}, err
			}
		}
		entities[entity.ID] = entity
	}
	result.Entities = make([]runtimeEntityObservation, 0, len(entities))
	for _, entity := range entities {
		result.Entities = append(result.Entities, entity)
	}
	sort.Slice(result.Entities, func(left, right int) bool {
		return result.Entities[left].ID < result.Entities[right].ID
	})
	result.Absences = unionRuntimeAbsences(result.Absences, nil)
	return result, nil
}

func normalizeRuntimeEntityObservation(source runtimeEntityObservation) (runtimeEntityObservation, error) {
	result := source
	result.ID = strings.TrimSpace(result.ID)
	result.DeviceAddress = strings.TrimSpace(result.DeviceAddress)
	result.EntityAddress = strings.TrimSpace(result.EntityAddress)
	result.ParentEntityAddress = strings.TrimSpace(result.ParentEntityAddress)
	result.Type = strings.TrimSpace(result.Type)
	result.Description = cloneRuntimeString(result.Description)
	if result.ID == "" {
		return runtimeEntityObservation{}, errors.New("runtime entity identity is required")
	}
	features := make(map[string]runtimeFeatureObservation, len(result.Features))
	for _, sourceFeature := range result.Features {
		feature := sourceFeature
		feature.ID = strings.TrimSpace(feature.ID)
		feature.DeviceAddress = strings.TrimSpace(feature.DeviceAddress)
		feature.EntityAddress = strings.TrimSpace(feature.EntityAddress)
		feature.FeatureAddress = strings.TrimSpace(feature.FeatureAddress)
		feature.Type = strings.TrimSpace(feature.Type)
		feature.Role = strings.TrimSpace(feature.Role)
		feature.Description = cloneRuntimeString(feature.Description)
		if feature.ID == "" {
			return runtimeEntityObservation{}, errors.New("runtime feature identity is required")
		}
		if existing, ok := features[feature.ID]; ok {
			var err error
			feature, err = mergeRuntimeFeatureObservations(existing, feature)
			if err != nil {
				return runtimeEntityObservation{}, err
			}
		}
		features[feature.ID] = feature
	}
	result.Features = make([]runtimeFeatureObservation, 0, len(features))
	for _, feature := range features {
		result.Features = append(result.Features, feature)
	}
	sort.Slice(result.Features, func(left, right int) bool {
		if result.Features[left].ID == result.Features[right].ID {
			return result.Features[left].Role < result.Features[right].Role
		}
		return result.Features[left].ID < result.Features[right].ID
	})
	return result, nil
}

func mergeRuntimeDeviceObservations(left, right runtimeDeviceObservation) (runtimeDeviceObservation, error) {
	result := mergeRuntimeDeviceFields(left, right)
	useCaseIDs, err := uniqueRuntimeStrings(append(append([]string(nil), left.UseCaseIDs...), right.UseCaseIDs...), "use case")
	if err != nil {
		return runtimeDeviceObservation{}, err
	}
	result.UseCaseIDs = useCaseIDs
	useCases := make(map[string]runtimeUseCaseObservation, len(left.UseCases)+len(right.UseCases))
	for _, value := range left.UseCases {
		useCases[value.ID] = value
	}
	for _, value := range right.UseCases {
		if existing, ok := useCases[value.ID]; ok {
			value = mergeRuntimeUseCaseObservations(existing, value)
		}
		useCases[value.ID] = value
	}
	result.UseCases = make([]runtimeUseCaseObservation, 0, len(useCases))
	for _, value := range useCases {
		result.UseCases = append(result.UseCases, value)
	}
	sort.Slice(result.UseCases, func(left, right int) bool {
		return result.UseCases[left].ID < result.UseCases[right].ID
	})
	entities := make(map[string]runtimeEntityObservation, len(left.Entities)+len(right.Entities))
	for _, entity := range left.Entities {
		entities[entity.ID] = entity
	}
	for _, entity := range right.Entities {
		if existing, ok := entities[entity.ID]; ok {
			entity, err = mergeRuntimeEntityObservations(existing, entity)
			if err != nil {
				return runtimeDeviceObservation{}, err
			}
		}
		entities[entity.ID] = entity
	}
	result.Entities = make([]runtimeEntityObservation, 0, len(entities))
	for _, entity := range entities {
		result.Entities = append(result.Entities, entity)
	}
	sort.Slice(result.Entities, func(left, right int) bool {
		return result.Entities[left].ID < result.Entities[right].ID
	})
	result.Absences = unionRuntimeAbsences(left.Absences, right.Absences)
	return result, nil
}

func mergeRuntimeDeviceCollections(
	left, right []runtimeDeviceObservation,
) ([]runtimeDeviceObservation, error) {
	devices := make(map[string]runtimeDeviceObservation, len(left)+len(right))
	for _, source := range left {
		device, err := normalizeRuntimeDeviceObservation(source)
		if err != nil {
			return nil, err
		}
		devices[device.ID] = device
	}
	for _, source := range right {
		device, err := normalizeRuntimeDeviceObservation(source)
		if err != nil {
			return nil, err
		}
		if existing, ok := devices[device.ID]; ok {
			device, err = mergeRuntimeDeviceObservations(existing, device)
			if err != nil {
				return nil, err
			}
		}
		devices[device.ID] = device
	}
	result := make([]runtimeDeviceObservation, 0, len(devices))
	for _, device := range devices {
		result = append(result, device)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].ID < result[right].ID
	})
	return result, nil
}

func exactRuntimeDeviceCollection(source []runtimeDeviceObservation) ([]runtimeDeviceObservation, error) {
	return mergeRuntimeDeviceCollections(nil, source)
}

func mergeRuntimeEntityObservations(left, right runtimeEntityObservation) (runtimeEntityObservation, error) {
	result := left
	if right.DeviceAddress != "" {
		result.DeviceAddress = right.DeviceAddress
	}
	if right.EntityAddress != "" {
		result.EntityAddress = right.EntityAddress
	}
	if right.ParentEntityAddress != "" {
		result.ParentEntityAddress = right.ParentEntityAddress
	}
	if right.Type != "" {
		result.Type = right.Type
	}
	if right.Description != nil {
		result.Description = cloneRuntimeString(right.Description)
	}
	features := make(map[string]runtimeFeatureObservation, len(left.Features)+len(right.Features))
	for _, feature := range left.Features {
		features[feature.ID] = feature
	}
	for _, feature := range right.Features {
		if existing, ok := features[feature.ID]; ok {
			var err error
			feature, err = mergeRuntimeFeatureObservations(existing, feature)
			if err != nil {
				return runtimeEntityObservation{}, err
			}
		}
		features[feature.ID] = feature
	}
	result.Features = make([]runtimeFeatureObservation, 0, len(features))
	for _, feature := range features {
		result.Features = append(result.Features, feature)
	}
	sort.Slice(result.Features, func(left, right int) bool {
		if result.Features[left].ID == result.Features[right].ID {
			return result.Features[left].Role < result.Features[right].Role
		}
		return result.Features[left].ID < result.Features[right].ID
	})
	return result, nil
}

func mergeRuntimeFeatureObservations(
	left, right runtimeFeatureObservation,
) (runtimeFeatureObservation, error) {
	result := left
	if right.DeviceAddress != "" {
		result.DeviceAddress = right.DeviceAddress
	}
	if right.EntityAddress != "" {
		result.EntityAddress = right.EntityAddress
	}
	if right.FeatureAddress != "" {
		result.FeatureAddress = right.FeatureAddress
	}
	if right.Type != "" {
		result.Type = right.Type
	}
	if right.Role != "" {
		if result.Role != "" && result.Role != right.Role {
			return runtimeFeatureObservation{}, errors.New("runtime feature identity has conflicting roles")
		}
		result.Role = right.Role
	}
	if right.Description != nil {
		result.Description = cloneRuntimeString(right.Description)
	}
	return result, nil
}

func mergeRuntimeUseCaseObservations(
	left, right runtimeUseCaseObservation,
) runtimeUseCaseObservation {
	result := left
	if right.ContextAddress != "" {
		result.ContextAddress = right.ContextAddress
	}
	if right.DeviceAddress != "" {
		result.DeviceAddress = right.DeviceAddress
	}
	if right.EntityAddress != "" {
		result.EntityAddress = right.EntityAddress
	}
	if right.FeatureAddress != "" {
		result.FeatureAddress = right.FeatureAddress
	}
	if right.Name != "" {
		result.Name = right.Name
	}
	if right.Actor != "" {
		result.Actor = right.Actor
	}
	if right.ResolvedRole != nil {
		result.ResolvedRole = cloneRuntimeString(right.ResolvedRole)
	}
	scenarios := make(map[string]struct{}, len(left.Scenarios)+len(right.Scenarios))
	for _, value := range append(append([]string(nil), left.Scenarios...), right.Scenarios...) {
		value = strings.TrimSpace(value)
		if value != "" {
			scenarios[value] = struct{}{}
		}
	}
	result.Scenarios = make([]string, 0, len(scenarios))
	for value := range scenarios {
		result.Scenarios = append(result.Scenarios, value)
	}
	sort.Strings(result.Scenarios)
	if right.Version != nil {
		result.Version = cloneRuntimeString(right.Version)
	}
	if right.Availability != nil {
		result.Availability = cloneRuntimeBool(right.Availability)
	}
	if right.DocumentSubrevision != nil {
		result.DocumentSubrevision = cloneRuntimeString(right.DocumentSubrevision)
	}
	return result
}

func mergeRuntimeDeviceFields(left, right runtimeDeviceObservation) runtimeDeviceObservation {
	result := left
	if right.SKI != "" {
		result.SKI = right.SKI
	}
	if right.SHIPID != "" {
		result.SHIPID = right.SHIPID
	}
	if right.Address != "" {
		result.Address = right.Address
	}
	if right.Type != "" {
		result.Type = right.Type
	}
	if right.Description != nil {
		result.Description = cloneRuntimeString(right.Description)
	}
	if right.Metadata != nil {
		if result.Metadata == nil {
			result.Metadata = make(map[string]string, len(right.Metadata))
		}
		for key, value := range right.Metadata {
			result.Metadata[key] = value
		}
	}
	result.Opaque = mergeRuntimeOpaque(result.Opaque, right.Opaque)
	return result
}

func uniqueRuntimeStrings(values []string, label string) ([]string, error) {
	set := make(map[string]struct{}, len(values))
	for _, source := range values {
		value := strings.TrimSpace(source)
		if value == "" {
			return nil, fmt.Errorf("runtime %s identity is required", label)
		}
		set[value] = struct{}{}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func cloneRuntimeGraphObservation(source runtimeGraphObservation) runtimeGraphObservation {
	result := source
	result.ServiceIDs = append([]string(nil), source.ServiceIDs...)
	result.Devices = make([]runtimeDeviceObservation, len(source.Devices))
	for deviceIndex, device := range source.Devices {
		result.Devices[deviceIndex] = device
		result.Devices[deviceIndex].Description = cloneRuntimeString(device.Description)
		result.Devices[deviceIndex].Metadata = cloneRuntimeMetadata(device.Metadata)
		result.Devices[deviceIndex].Opaque = cloneRuntimeOpaque(device.Opaque)
		result.Devices[deviceIndex].UseCaseIDs = append([]string(nil), device.UseCaseIDs...)
		result.Devices[deviceIndex].Absences = append([]runtimeAbsenceObservation(nil), device.Absences...)
		result.Devices[deviceIndex].UseCases = make([]runtimeUseCaseObservation, len(device.UseCases))
		for useCaseIndex, useCase := range device.UseCases {
			result.Devices[deviceIndex].UseCases[useCaseIndex] = useCase
			result.Devices[deviceIndex].UseCases[useCaseIndex].ResolvedRole = cloneRuntimeString(useCase.ResolvedRole)
			result.Devices[deviceIndex].UseCases[useCaseIndex].Scenarios = append([]string(nil), useCase.Scenarios...)
			result.Devices[deviceIndex].UseCases[useCaseIndex].Version = cloneRuntimeString(useCase.Version)
			result.Devices[deviceIndex].UseCases[useCaseIndex].Availability = cloneRuntimeBool(useCase.Availability)
			result.Devices[deviceIndex].UseCases[useCaseIndex].DocumentSubrevision = cloneRuntimeString(useCase.DocumentSubrevision)
		}
		result.Devices[deviceIndex].Entities = make([]runtimeEntityObservation, len(device.Entities))
		for entityIndex, entity := range device.Entities {
			result.Devices[deviceIndex].Entities[entityIndex] = entity
			result.Devices[deviceIndex].Entities[entityIndex].Description = cloneRuntimeString(entity.Description)
			result.Devices[deviceIndex].Entities[entityIndex].Features = make([]runtimeFeatureObservation, len(entity.Features))
			for featureIndex, feature := range entity.Features {
				result.Devices[deviceIndex].Entities[entityIndex].Features[featureIndex] = feature
				result.Devices[deviceIndex].Entities[entityIndex].Features[featureIndex].Description =
					cloneRuntimeString(feature.Description)
			}
		}
	}
	return result
}

func unionRuntimeAbsences(left, right []runtimeAbsenceObservation) []runtimeAbsenceObservation {
	if len(left) == 0 && len(right) == 0 {
		return nil
	}
	set := make(map[runtimeAbsenceObservation]struct{}, len(left)+len(right))
	for _, source := range append(append([]runtimeAbsenceObservation(nil), left...), right...) {
		set[runtimeAbsenceObservation{
			Kind: strings.TrimSpace(source.Kind), Reason: strings.TrimSpace(source.Reason),
			DeviceAddress: strings.TrimSpace(source.DeviceAddress), EntityAddress: strings.TrimSpace(source.EntityAddress),
			FeatureAddress: strings.TrimSpace(source.FeatureAddress),
		}] = struct{}{}
	}
	result := make([]runtimeAbsenceObservation, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sortRuntimeAbsences(result)
	return result
}

func sortRuntimeAbsences(values []runtimeAbsenceObservation) {
	sort.Slice(values, func(left, right int) bool {
		a, b := values[left], values[right]
		for _, pair := range [][2]string{{a.Kind, b.Kind}, {a.DeviceAddress, b.DeviceAddress}, {a.EntityAddress, b.EntityAddress}, {a.FeatureAddress, b.FeatureAddress}} {
			if pair[0] != pair[1] {
				return pair[0] < pair[1]
			}
		}
		return a.Reason < b.Reason
	})
}

func cloneRuntimeMetadata(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func cloneRuntimeOpaque(source []runtimeOpaquePayload) []runtimeOpaquePayload {
	if source == nil {
		return nil
	}
	result := make([]runtimeOpaquePayload, len(source))
	for index, observation := range source {
		result[index] = observation
		value, err := detachedRuntimeJSONValue(observation.Value)
		if err == nil {
			result[index].Value = value
		} else {
			result[index].Value = nil
		}
	}
	return result
}

func normalizeRuntimeOpaque(source []runtimeOpaquePayload) ([]runtimeOpaquePayload, error) {
	if source == nil {
		return nil, nil
	}
	result := make([]runtimeOpaquePayload, len(source))
	for index, observation := range source {
		value, err := detachedRuntimeJSONValue(observation.Value)
		if err != nil {
			return nil, errors.New("runtime opaque observation is not JSON")
		}
		result[index] = runtimeOpaquePayload{
			Path: strings.TrimSpace(observation.Path), Source: strings.TrimSpace(observation.Source), Value: value,
		}
	}
	return result, nil
}

func mergeRuntimeOpaque(left, right []runtimeOpaquePayload) []runtimeOpaquePayload {
	if right == nil {
		return cloneRuntimeOpaque(left)
	}
	values := make(map[string]runtimeOpaquePayload, len(left)+len(right))
	for _, observation := range append(cloneRuntimeOpaque(left), cloneRuntimeOpaque(right)...) {
		encoded, _ := json.Marshal(observation.Value)
		key := observation.Path + "\x00" + observation.Source + "\x00" + string(encoded)
		values[key] = observation
	}
	result := make([]runtimeOpaquePayload, 0, len(values))
	for _, observation := range values {
		result = append(result, observation)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Path != result[right].Path {
			return result[left].Path < result[right].Path
		}
		if result[left].Source != result[right].Source {
			return result[left].Source < result[right].Source
		}
		leftValue, _ := json.Marshal(result[left].Value)
		rightValue, _ := json.Marshal(result[right].Value)
		return string(leftValue) < string(rightValue)
	})
	return result
}

func detachedRuntimeJSONValue(value any) (any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, errors.New("encode detailed discovery value")
	}
	var detached any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&detached); err != nil {
		return nil, errors.New("decode detailed discovery value")
	}
	return detached, nil
}

func cloneRuntimeString(value *string) *string {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneRuntimeBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}
