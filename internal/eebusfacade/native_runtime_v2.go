package eebusfacade

import (
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const nativeRuntimeV2Source = "eebusfacade.runtime"

var nativeRuntimeV2JSONNumber = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`)

type nativeRuntimeSnapshotV2Payload struct {
	Meta            nativeRuntimeSnapshotMetaV2Payload  `json:"meta"`
	Status          nativeRuntimeStatusV2Payload        `json:"status"`
	Pairing         []nativeRuntimePairingV2Payload     `json:"pairing"`
	Services        []nativeRuntimeServiceV2Payload     `json:"services"`
	Sessions        []nativeRuntimeSessionV2Payload     `json:"sessions"`
	Devices         []nativeRuntimeDeviceV2Payload      `json:"devices"`
	Entities        []nativeRuntimeEntityV2Payload      `json:"entities"`
	Features        []nativeRuntimeFeatureV2Payload     `json:"features"`
	UseCases        []nativeRuntimeUseCaseV2Payload     `json:"use_cases"`
	Observations    []nativeRuntimeObservationV2Payload `json:"observations"`
	Absences        []nativeRuntimeAbsencePayload       `json:"absences"`
	AbsencesOmitted uint64                              `json:"absences_omitted"`
}

type nativeRuntimeAbsencePayload struct {
	Kind           string  `json:"kind"`
	Reason         string  `json:"reason"`
	DeviceAddress  string  `json:"device_address"`
	EntityAddress  *string `json:"entity_address,omitempty"`
	FeatureAddress *string `json:"feature_address,omitempty"`
}

type nativeRuntimeSnapshotMetaV2Payload struct {
	Contract      string    `json:"contract"`
	Runtime       string    `json:"runtime"`
	LocalSKI      string    `json:"local_ski"`
	Source        string    `json:"source"`
	ObservedAt    time.Time `json:"observed_at"`
	CapturedAt    time.Time `json:"captured_at"`
	DataTimestamp time.Time `json:"data_timestamp"`
}

type nativeRuntimeStatusV2Payload struct {
	State       string                             `json:"state"`
	Degradation *nativeRuntimeDegradationV2Payload `json:"degradation,omitempty"`
}

type nativeRuntimeDegradationV2Payload struct {
	Reason string    `json:"reason"`
	Since  time.Time `json:"since"`
}

type nativeRuntimePairingV2Payload struct {
	RemoteSKI string    `json:"remote_ski"`
	State     string    `json:"state"`
	Since     time.Time `json:"since"`
}

type nativeRuntimeServiceV2Payload struct {
	SKI        string  `json:"ski"`
	SHIPID     *string `json:"ship_id,omitempty"`
	Kind       string  `json:"kind"`
	Visible    bool    `json:"visible"`
	Paired     bool    `json:"paired"`
	Name       *string `json:"name,omitempty"`
	Identifier *string `json:"identifier,omitempty"`
	Brand      *string `json:"brand,omitempty"`
	Type       *string `json:"type,omitempty"`
	Model      *string `json:"model,omitempty"`
}

type nativeRuntimeSessionV2Payload struct {
	ID        string    `json:"id"`
	RemoteSKI string    `json:"remote_ski"`
	State     string    `json:"state"`
	Since     time.Time `json:"since"`
}

type nativeRuntimeDeviceV2Payload struct {
	SKI          string                              `json:"ski"`
	SHIPID       *string                             `json:"ship_id,omitempty"`
	Address      string                              `json:"address"`
	Type         string                              `json:"type"`
	Description  *string                             `json:"description,omitempty"`
	Metadata     map[string]string                   `json:"metadata,omitempty"`
	Observations []nativeRuntimeObservationV2Payload `json:"observations,omitempty"`
}

type nativeRuntimeEntityV2Payload struct {
	DeviceAddress       string  `json:"device_address"`
	EntityAddress       string  `json:"entity_address"`
	ParentEntityAddress *string `json:"parent_entity_address,omitempty"`
	Type                string  `json:"type"`
	Description         *string `json:"description,omitempty"`
}

type nativeRuntimeFeatureV2Payload struct {
	DeviceAddress  string  `json:"device_address"`
	EntityAddress  string  `json:"entity_address"`
	FeatureAddress string  `json:"feature_address"`
	Type           string  `json:"type"`
	Role           string  `json:"role"`
	Description    *string `json:"description,omitempty"`
}

type nativeRuntimeUseCaseV2Payload struct {
	ContextAddress      string   `json:"context_address"`
	DeviceAddress       string   `json:"device_address"`
	EntityAddress       *string  `json:"entity_address,omitempty"`
	FeatureAddress      *string  `json:"feature_address,omitempty"`
	Name                string   `json:"name"`
	Actor               string   `json:"actor"`
	ResolvedRole        *string  `json:"resolved_role,omitempty"`
	Scenarios           []string `json:"scenarios"`
	Version             *string  `json:"version,omitempty"`
	Availability        *bool    `json:"availability,omitempty"`
	DocumentSubrevision *string  `json:"document_subrevision,omitempty"`
}

type nativeRuntimeObservationV2Payload struct {
	Path       string                      `json:"path"`
	Source     string                      `json:"source"`
	ObservedAt time.Time                   `json:"observed_at"`
	Value      nativeRuntimeValueV2Payload `json:"value"`
}

type nativeRuntimeValueV2Payload struct {
	Null    *bool                                   `json:"null,omitempty"`
	Boolean *bool                                   `json:"boolean,omitempty"`
	Integer *int64                                  `json:"integer,omitempty"`
	Float   *float64                                `json:"float,omitempty"`
	Number  *string                                 `json:"number,omitempty"`
	String  *string                                 `json:"string,omitempty"`
	Array   *[]nativeRuntimeValueV2Payload          `json:"array,omitempty"`
	Object  *map[string]nativeRuntimeValueV2Payload `json:"object,omitempty"`
}

func marshalNativeRuntimeSnapshotV2WithIdentity(runtimeIdentity, localIdentity string, graph []runtimeGraphObservation, now time.Time) ([]byte, error) {
	if strings.TrimSpace(runtimeIdentity) == "" {
		return nil, errors.New("runtime identity is required")
	}
	now = now.UTC()
	payload := nativeRuntimeSnapshotV2Payload{
		Meta: nativeRuntimeSnapshotMetaV2Payload{
			Contract: "helianthus.eebus.runtime.native-snapshot.v2", Runtime: runtimeIdentity,
			LocalSKI: localIdentity, Source: nativeRuntimeV2Source, ObservedAt: now,
			CapturedAt: now, DataTimestamp: now,
		},
		Status:  nativeRuntimeStatusV2Payload{State: "starting"},
		Pairing: []nativeRuntimePairingV2Payload{}, Services: []nativeRuntimeServiceV2Payload{},
		Sessions: []nativeRuntimeSessionV2Payload{}, Devices: []nativeRuntimeDeviceV2Payload{},
		Entities: []nativeRuntimeEntityV2Payload{}, Features: []nativeRuntimeFeatureV2Payload{},
		UseCases: []nativeRuntimeUseCaseV2Payload{}, Observations: []nativeRuntimeObservationV2Payload{},
	}
	var candidates []nativeRuntimeDeviceParts
	visible, connected, disconnected := false, false, false
	trustDegradation := ""
	for _, remote := range graph {
		if remote.PairingState != "" {
			payload.Pairing = append(payload.Pairing, nativeRuntimePairingV2Payload{RemoteSKI: remote.RemoteSKI, State: remote.PairingState, Since: remote.Since})
		}
		if remote.RemoteSKI != "" {
			payload.Services = append(payload.Services, nativeRuntimeServiceV2Payload{
				SKI: remote.RemoteSKI, SHIPID: runtimeOptionalString(remote.ShipID), Kind: "remote", Visible: remote.Visible, Paired: remote.Paired,
				Name: runtimeOptionalString(remote.ServiceName), Identifier: runtimeOptionalString(remote.ServiceIdentifier), Brand: runtimeOptionalString(remote.ServiceBrand),
				Type: runtimeOptionalString(remote.ServiceType), Model: runtimeOptionalString(remote.ServiceModel),
			})
		}
		if remote.SessionID != "" && remote.SessionState != "" {
			payload.Sessions = append(payload.Sessions, nativeRuntimeSessionV2Payload{ID: remote.SessionID, RemoteSKI: remote.RemoteSKI, State: remote.SessionState, Since: remote.Since})
		}
		for _, device := range remote.Devices {
			if device.SHIPID == "" {
				device.SHIPID = remote.ShipID
			}
			if device.SKI == "" {
				device.SKI = remote.RemoteSKI
			}
			parts, err := marshalNativeRuntimeDeviceV2(device, now)
			if err != nil {
				return nil, err
			}
			if parts.device.Address == "" {
				continue
			}
			candidates = append(candidates, parts)
		}
		visible = visible || remote.Visible
		connected = connected || remote.SessionState == "connected"
		disconnected = disconnected || remote.SessionState == "disconnected" || remote.SessionState == "degraded"
		if remote.TrustDegradation == "denied-trust" || trustDegradation == "" && remote.TrustDegradation == "certificate-unavailable" {
			trustDegradation = remote.TrustDegradation
		}
	}
	if trustDegradation != "" {
		payload.Status = nativeRuntimeStatusV2Payload{State: "degraded", Degradation: &nativeRuntimeDegradationV2Payload{Reason: trustDegradation, Since: now}}
	} else if connected {
		payload.Status.State = "ready"
	} else if disconnected {
		payload.Status = nativeRuntimeStatusV2Payload{State: "degraded", Degradation: &nativeRuntimeDegradationV2Payload{Reason: "remote-disconnect", Since: now}}
	} else if !visible {
		payload.Status = nativeRuntimeStatusV2Payload{State: "degraded", Degradation: &nativeRuntimeDegradationV2Payload{Reason: "no-visible-services", Since: now}}
	}
	payload.Absences, payload.AbsencesOmitted = boundNativeRuntimeAbsences(containNativeRuntimeNodes(&payload, candidates))
	return json.Marshal(payload)
}

type nativeRuntimeDeviceParts struct {
	device   nativeRuntimeDeviceV2Payload
	entities []nativeRuntimeEntityV2Payload
	features []nativeRuntimeFeatureV2Payload
	useCases []nativeRuntimeUseCaseV2Payload
	absences []nativeRuntimeAbsencePayload
}

func marshalNativeRuntimeDeviceV2(source runtimeDeviceObservation, observedAt time.Time) (nativeRuntimeDeviceParts, error) {
	if source.SKI == "" || source.Address == "" || source.Type == "" {
		return nativeRuntimeDeviceParts{}, nil
	}
	result := nativeRuntimeDeviceParts{device: nativeRuntimeDeviceV2Payload{SKI: source.SKI, SHIPID: runtimeOptionalString(source.SHIPID), Address: source.Address, Type: source.Type, Description: cloneRuntimeString(source.Description), Metadata: cloneRuntimeMetadata(source.Metadata)}}
	observations, err := nativeRuntimeObservationsV2(source.Opaque, observedAt)
	if err != nil {
		return nativeRuntimeDeviceParts{}, err
	}
	result.device.Observations = observations
	for _, absence := range source.Absences {
		if record, ok := nativeRuntimeAbsence(absence.Kind, absence.Reason, absence.DeviceAddress, absence.EntityAddress, absence.FeatureAddress, source.Address); ok {
			result.absences = append(result.absences, record)
		}
	}
	for _, entity := range source.Entities {
		marshalNativeRuntimeEntity(&result, entity, source.Address)
	}
	result.useCases = make([]nativeRuntimeUseCaseV2Payload, 0, len(source.UseCases))
	for _, useCase := range source.UseCases {
		deviceAddress := useCase.DeviceAddress
		if deviceAddress == "" {
			deviceAddress = source.Address
		}
		reason := ""
		switch {
		case useCase.ContextAddress == "":
			reason = "missing_address"
		case useCase.Name == "":
			reason = "missing_name"
		case useCase.Actor == "":
			reason = "missing_actor"
		}
		if reason != "" {
			record, _ := nativeRuntimeAbsence("use_case", reason, deviceAddress, useCase.EntityAddress, useCase.FeatureAddress, source.Address)
			result.absences = append(result.absences, record)
			continue
		}
		result.useCases = append(result.useCases, nativeRuntimeUseCaseV2Payload{ContextAddress: useCase.ContextAddress, DeviceAddress: deviceAddress, EntityAddress: nativeRuntimeOptionalAddress(useCase.EntityAddress), FeatureAddress: nativeRuntimeOptionalAddress(useCase.FeatureAddress), Name: useCase.Name, Actor: useCase.Actor, ResolvedRole: cloneRuntimeString(useCase.ResolvedRole), Scenarios: append([]string(nil), useCase.Scenarios...), Version: cloneRuntimeString(useCase.Version), Availability: cloneRuntimeBool(useCase.Availability), DocumentSubrevision: cloneRuntimeString(useCase.DocumentSubrevision)})
	}
	return result, nil
}

func marshalNativeRuntimeEntity(result *nativeRuntimeDeviceParts, entity runtimeEntityObservation, deviceAddress string) {
	reason := ""
	switch {
	case entity.DeviceAddress == "" || entity.EntityAddress == "":
		reason = "missing_address"
	case entity.Type == "":
		reason = "missing_type"
	}
	if reason != "" {
		record, _ := nativeRuntimeAbsence("entity", reason, deviceAddress, entity.EntityAddress, "", deviceAddress)
		result.absences = append(result.absences, record)
		for _, feature := range entity.Features {
			record, _ := nativeRuntimeAbsence("feature", "missing_container", deviceAddress, entity.EntityAddress, feature.FeatureAddress, deviceAddress)
			result.absences = append(result.absences, record)
		}
		return
	}
	result.entities = append(result.entities, nativeRuntimeEntityV2Payload{DeviceAddress: entity.DeviceAddress, EntityAddress: entity.EntityAddress, ParentEntityAddress: nativeRuntimeOptionalAddress(entity.ParentEntityAddress), Type: entity.Type, Description: cloneRuntimeString(entity.Description)})
	for _, feature := range entity.Features {
		switch {
		case feature.DeviceAddress == "" || feature.EntityAddress == "" || feature.FeatureAddress == "":
			reason = "missing_address"
		case feature.Type == "":
			reason = "missing_type"
		case feature.Role == "":
			reason = "missing_role"
		default:
			result.features = append(result.features, nativeRuntimeFeatureV2Payload{DeviceAddress: feature.DeviceAddress, EntityAddress: feature.EntityAddress, FeatureAddress: feature.FeatureAddress, Type: feature.Type, Role: feature.Role, Description: cloneRuntimeString(feature.Description)})
			continue
		}
		record, _ := nativeRuntimeAbsence("feature", reason, deviceAddress, entity.EntityAddress, feature.FeatureAddress, deviceAddress)
		result.absences = append(result.absences, record)
	}
}

func nativeRuntimeObservationsV2(source []runtimeOpaquePayload, observedAt time.Time) ([]nativeRuntimeObservationV2Payload, error) {
	if source == nil {
		return nil, nil
	}
	result := make([]nativeRuntimeObservationV2Payload, 0, len(source))
	for _, observation := range source {
		value, err := nativeRuntimeValueV2FromAny(observation.Value, 1)
		if err != nil {
			return nil, err
		}
		result = append(result, nativeRuntimeObservationV2Payload{Path: observation.Path, Source: observation.Source, ObservedAt: observedAt, Value: value})
	}
	return result, nil
}

func nativeRuntimeValueV2FromAny(source any, depth int) (nativeRuntimeValueV2Payload, error) {
	if depth > 3 {
		return nativeRuntimeValueV2Payload{}, errors.New("native value nesting exceeds the limit")
	}
	switch value := source.(type) {
	case nil:
		marker := true
		return nativeRuntimeValueV2Payload{Null: &marker}, nil
	case bool:
		return nativeRuntimeValueV2Payload{Boolean: &value}, nil
	case string:
		if len(value) > 65536 {
			return nativeRuntimeValueV2Payload{}, errors.New("native string exceeds the limit")
		}
		return nativeRuntimeValueV2Payload{String: &value}, nil
	case json.Number:
		number := value.String()
		if len(number) > 65536 || !nativeRuntimeV2JSONNumber.MatchString(number) {
			return nativeRuntimeValueV2Payload{}, errors.New("native number is invalid")
		}
		return nativeRuntimeValueV2Payload{Number: &number}, nil
	case int:
		integer := int64(value)
		return nativeRuntimeValueV2Payload{Integer: &integer}, nil
	case int8:
		integer := int64(value)
		return nativeRuntimeValueV2Payload{Integer: &integer}, nil
	case int16:
		integer := int64(value)
		return nativeRuntimeValueV2Payload{Integer: &integer}, nil
	case int32:
		integer := int64(value)
		return nativeRuntimeValueV2Payload{Integer: &integer}, nil
	case int64:
		return nativeRuntimeValueV2Payload{Integer: &value}, nil
	case float32:
		floating := float64(value)
		return nativeRuntimeValueV2Payload{Float: &floating}, nil
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nativeRuntimeValueV2Payload{}, errors.New("native float is invalid")
		}
		return nativeRuntimeValueV2Payload{Float: &value}, nil
	case uint:
		if uint64(value) > uint64(^uint64(0)>>1) {
			number := strconv.FormatUint(uint64(value), 10)
			return nativeRuntimeValueV2Payload{Number: &number}, nil
		}
		integer := int64(value)
		return nativeRuntimeValueV2Payload{Integer: &integer}, nil
	case uint8:
		integer := int64(value)
		return nativeRuntimeValueV2Payload{Integer: &integer}, nil
	case uint16:
		integer := int64(value)
		return nativeRuntimeValueV2Payload{Integer: &integer}, nil
	case uint32:
		integer := int64(value)
		return nativeRuntimeValueV2Payload{Integer: &integer}, nil
	case uint64:
		if value > uint64(^uint64(0)>>1) {
			number := strconv.FormatUint(value, 10)
			return nativeRuntimeValueV2Payload{Number: &number}, nil
		}
		integer := int64(value)
		return nativeRuntimeValueV2Payload{Integer: &integer}, nil
	case []any:
		if len(value) > 256 {
			return nativeRuntimeValueV2Payload{}, errors.New("native array exceeds the limit")
		}
		values := make([]nativeRuntimeValueV2Payload, len(value))
		result := nativeRuntimeValueV2Payload{Array: &values}
		for index, child := range value {
			converted, err := nativeRuntimeValueV2FromAny(child, depth+1)
			if err != nil {
				return nativeRuntimeValueV2Payload{}, err
			}
			(*result.Array)[index] = converted
		}
		return result, nil
	case map[string]any:
		if len(value) > 32 {
			return nativeRuntimeValueV2Payload{}, errors.New("native object exceeds the limit")
		}
		values := make(map[string]nativeRuntimeValueV2Payload, len(value))
		result := nativeRuntimeValueV2Payload{Object: &values}
		for key, child := range value {
			converted, err := nativeRuntimeValueV2FromAny(child, depth+1)
			if err != nil {
				return nativeRuntimeValueV2Payload{}, err
			}
			(*result.Object)[key] = converted
		}
		return result, nil
	default:
		return nativeRuntimeValueV2Payload{}, errors.New("native value type is unsupported")
	}
}

const nativeRuntimeAbsenceLimit = 1024

var (
	nativeRuntimeAbsenceKinds   = map[string]struct{}{"device": {}, "entity": {}, "feature": {}, "use_case": {}}
	nativeRuntimeAbsenceReasons = map[string]struct{}{
		"missing_address": {}, "missing_type": {}, "missing_role": {}, "missing_name": {},
		"missing_actor": {}, "missing_container": {}, "ambiguous_address": {},
	}
)

type nativeRuntimeEntityKey struct{ device, entity string }

type nativeRuntimeFeatureKey struct{ device, entity, feature string }

type nativeRuntimeOptionalText struct {
	set   bool
	value string
}

type nativeRuntimeClaimKey struct {
	device, name, actor                   string
	entity, feature, version, subrevision nativeRuntimeOptionalText
}

func nativeRuntimeOptional(value *string) nativeRuntimeOptionalText {
	if value == nil {
		return nativeRuntimeOptionalText{}
	}
	return nativeRuntimeOptionalText{set: true, value: *value}
}

func nativeRuntimeOptionalAddress(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func nativeRuntimeAddressText(value string) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed != "" && len(trimmed) <= 4096
}

func nativeRuntimeAbsence(kind, reason, deviceAddress, entityAddress, featureAddress, containerAddress string) (nativeRuntimeAbsencePayload, bool) {
	kind, reason = strings.TrimSpace(kind), strings.TrimSpace(reason)
	_, knownKind := nativeRuntimeAbsenceKinds[kind]
	_, knownReason := nativeRuntimeAbsenceReasons[reason]
	if strings.TrimSpace(deviceAddress) == "" {
		deviceAddress = containerAddress
	}
	return nativeRuntimeAbsencePayload{
		Kind: kind, Reason: reason, DeviceAddress: deviceAddress,
		EntityAddress: nativeRuntimeOptionalAddress(entityAddress), FeatureAddress: nativeRuntimeOptionalAddress(featureAddress),
	}, knownKind && knownReason
}

func nativeRuntimeWithheld(kind, reason, deviceAddress string, entityAddress, featureAddress *string) nativeRuntimeAbsencePayload {
	return nativeRuntimeAbsencePayload{Kind: kind, Reason: reason, DeviceAddress: deviceAddress, EntityAddress: cloneRuntimeString(entityAddress), FeatureAddress: cloneRuntimeString(featureAddress)}
}

func containNativeRuntimeNodes(payload *nativeRuntimeSnapshotV2Payload, candidates []nativeRuntimeDeviceParts) []nativeRuntimeAbsencePayload {
	counts := make(map[string]int, len(candidates))
	for _, candidate := range candidates {
		counts[candidate.device.Address]++
	}
	devices := make(map[string]struct{}, len(candidates))
	var entities []nativeRuntimeEntityV2Payload
	var features []nativeRuntimeFeatureV2Payload
	var useCases []nativeRuntimeUseCaseV2Payload
	var absences []nativeRuntimeAbsencePayload
	for _, candidate := range candidates {
		if counts[candidate.device.Address] > 1 {
			absences = append(absences, nativeRuntimeWithheld("device", "ambiguous_address", candidate.device.Address, nil, nil))
			continue
		}
		devices[candidate.device.Address] = struct{}{}
		payload.Devices = append(payload.Devices, candidate.device)
		entities = append(entities, candidate.entities...)
		features = append(features, candidate.features...)
		useCases = append(useCases, candidate.useCases...)
		absences = append(absences, candidate.absences...)
	}
	emitted, withheld := containNativeRuntimeEntities(devices, entities)
	absences = append(absences, withheld...)
	for _, entity := range entities {
		if _, ok := emitted[nativeRuntimeEntityKey{entity.DeviceAddress, entity.EntityAddress}]; ok {
			payload.Entities = append(payload.Entities, entity)
		}
	}
	emittedFeatures := make(map[nativeRuntimeFeatureKey]struct{}, len(features))
	for _, feature := range features {
		key := nativeRuntimeFeatureKey{feature.DeviceAddress, feature.EntityAddress, feature.FeatureAddress}
		reason := ""
		if _, ok := emitted[nativeRuntimeEntityKey{feature.DeviceAddress, feature.EntityAddress}]; !ok {
			reason = "missing_container"
		} else if _, duplicate := emittedFeatures[key]; duplicate {
			reason = "ambiguous_address"
		}
		if reason != "" {
			absences = append(absences, nativeRuntimeWithheld("feature", reason, feature.DeviceAddress, &feature.EntityAddress, &feature.FeatureAddress))
			continue
		}
		emittedFeatures[key] = struct{}{}
		payload.Features = append(payload.Features, feature)
	}
	return append(absences, containNativeRuntimeClaims(payload, useCases, devices, emitted, emittedFeatures)...)
}

func containNativeRuntimeEntities(devices map[string]struct{}, entities []nativeRuntimeEntityV2Payload) (map[nativeRuntimeEntityKey]*string, []nativeRuntimeAbsencePayload) {
	parents := make(map[nativeRuntimeEntityKey]*string, len(entities))
	var absences []nativeRuntimeAbsencePayload
	order := make([]nativeRuntimeEntityKey, 0, len(entities))
	for _, entity := range entities {
		key := nativeRuntimeEntityKey{entity.DeviceAddress, entity.EntityAddress}
		if _, ok := devices[entity.DeviceAddress]; !ok {
			absences = append(absences, nativeRuntimeWithheld("entity", "missing_container", entity.DeviceAddress, &entity.EntityAddress, nil))
			continue
		}
		if _, duplicate := parents[key]; duplicate {
			absences = append(absences, nativeRuntimeWithheld("entity", "ambiguous_address", entity.DeviceAddress, &entity.EntityAddress, nil))
			continue
		}
		parents[key] = entity.ParentEntityAddress
		order = append(order, key)
	}
	const resolving, resolved, withheld = 1, 2, 3
	state := make(map[nativeRuntimeEntityKey]uint8, len(parents))
	var resolve func(nativeRuntimeEntityKey) bool
	resolve = func(key nativeRuntimeEntityKey) bool {
		switch state[key] {
		case resolved:
			return true
		case resolving, withheld:
			return false
		}
		parent, known := parents[key]
		if !known {
			return false
		}
		state[key] = resolving
		ok := parent == nil || *parent != key.entity && resolve(nativeRuntimeEntityKey{key.device, *parent})
		state[key] = withheld
		if ok {
			state[key] = resolved
		}
		return ok
	}
	emitted := make(map[nativeRuntimeEntityKey]*string, len(parents))
	for _, key := range order {
		if resolve(key) {
			emitted[key] = parents[key]
			continue
		}
		entity := key.entity
		absences = append(absences, nativeRuntimeWithheld("entity", "missing_container", key.device, &entity, nil))
	}
	return emitted, absences
}

func containNativeRuntimeClaims(payload *nativeRuntimeSnapshotV2Payload, useCases []nativeRuntimeUseCaseV2Payload, devices map[string]struct{}, entities map[nativeRuntimeEntityKey]*string, features map[nativeRuntimeFeatureKey]struct{}) []nativeRuntimeAbsencePayload {
	var absences []nativeRuntimeAbsencePayload
	claims := make(map[nativeRuntimeClaimKey]struct{}, len(useCases))
	for _, useCase := range useCases {
		key := nativeRuntimeClaimKey{
			device: useCase.DeviceAddress, name: useCase.Name, actor: useCase.Actor,
			entity: nativeRuntimeOptional(useCase.EntityAddress), feature: nativeRuntimeOptional(useCase.FeatureAddress),
			version: nativeRuntimeOptional(useCase.Version), subrevision: nativeRuntimeOptional(useCase.DocumentSubrevision),
		}
		reason := ""
		if !nativeRuntimeClaimResolves(useCase, devices, entities, features) {
			reason = "missing_container"
		} else if _, duplicate := claims[key]; duplicate {
			reason = "ambiguous_address"
		}
		if reason != "" {
			absences = append(absences, nativeRuntimeWithheld("use_case", reason, useCase.DeviceAddress, useCase.EntityAddress, useCase.FeatureAddress))
			continue
		}
		claims[key] = struct{}{}
		payload.UseCases = append(payload.UseCases, useCase)
	}
	return absences
}

func nativeRuntimeClaimResolves(useCase nativeRuntimeUseCaseV2Payload, devices map[string]struct{}, entities map[nativeRuntimeEntityKey]*string, features map[nativeRuntimeFeatureKey]struct{}) bool {
	if _, ok := devices[useCase.DeviceAddress]; !ok {
		return false
	}
	if useCase.EntityAddress == nil {
		return useCase.FeatureAddress == nil
	}
	if _, ok := entities[nativeRuntimeEntityKey{useCase.DeviceAddress, *useCase.EntityAddress}]; !ok {
		return false
	}
	if useCase.FeatureAddress == nil {
		return true
	}
	_, ok := features[nativeRuntimeFeatureKey{useCase.DeviceAddress, *useCase.EntityAddress, *useCase.FeatureAddress}]
	return ok
}

func boundNativeRuntimeAbsences(values []nativeRuntimeAbsencePayload) ([]nativeRuntimeAbsencePayload, uint64) {
	text := func(value *string) string {
		if value == nil {
			return ""
		}
		return *value
	}
	representable := make([]nativeRuntimeAbsencePayload, 0, len(values))
	for _, value := range values {
		if !nativeRuntimeAddressText(value.DeviceAddress) {
			continue
		}
		if value.EntityAddress != nil && !nativeRuntimeAddressText(*value.EntityAddress) {
			value.EntityAddress = nil
		}
		if value.FeatureAddress != nil && !nativeRuntimeAddressText(*value.FeatureAddress) {
			value.FeatureAddress = nil
		}
		representable = append(representable, value)
	}
	values = representable
	sort.SliceStable(values, func(left, right int) bool {
		a, b := values[left], values[right]
		for _, pair := range [][2]string{{a.Kind, b.Kind}, {a.DeviceAddress, b.DeviceAddress}, {text(a.EntityAddress), text(b.EntityAddress)}, {text(a.FeatureAddress), text(b.FeatureAddress)}} {
			if pair[0] != pair[1] {
				return pair[0] < pair[1]
			}
		}
		return a.Reason < b.Reason
	})
	if len(values) > nativeRuntimeAbsenceLimit {
		return values[:nativeRuntimeAbsenceLimit:nativeRuntimeAbsenceLimit], uint64(len(values) - nativeRuntimeAbsenceLimit)
	}
	return values, 0
}
