package eebusfacade

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"
)

const nativeRuntimeV2Source = "eebusfacade.runtime"

type nativeRuntimeSnapshotV2Payload struct {
	Meta         nativeRuntimeSnapshotMetaV2Payload  `json:"meta"`
	Status       nativeRuntimeStatusV2Payload        `json:"status"`
	Pairing      []nativeRuntimePairingV2Payload     `json:"pairing"`
	Services     []nativeRuntimeServiceV2Payload     `json:"services"`
	Sessions     []nativeRuntimeSessionV2Payload     `json:"sessions"`
	Devices      []nativeRuntimeDeviceV2Payload      `json:"devices"`
	Entities     []nativeRuntimeEntityV2Payload      `json:"entities"`
	Features     []nativeRuntimeFeatureV2Payload     `json:"features"`
	UseCases     []nativeRuntimeUseCaseV2Payload     `json:"usecases"`
	Observations []nativeRuntimeObservationV2Payload `json:"observations"`
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
	DeviceAddress string  `json:"device_address"`
	EntityAddress string  `json:"entity_address"`
	Type          string  `json:"type"`
	Description   *string `json:"description,omitempty"`
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
			devicePayload, entities, features, useCases, err := marshalNativeRuntimeDeviceV2(device, now)
			if err != nil {
				return nil, err
			}
			if devicePayload.Address == "" {
				continue
			}
			payload.Devices = append(payload.Devices, devicePayload)
			payload.Entities = append(payload.Entities, entities...)
			payload.Features = append(payload.Features, features...)
			payload.UseCases = append(payload.UseCases, useCases...)
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
	return json.Marshal(payload)
}

func marshalNativeRuntimeDeviceV2(source runtimeDeviceObservation, observedAt time.Time) (nativeRuntimeDeviceV2Payload, []nativeRuntimeEntityV2Payload, []nativeRuntimeFeatureV2Payload, []nativeRuntimeUseCaseV2Payload, error) {
	if source.SKI == "" || source.Address == "" || source.Type == "" {
		return nativeRuntimeDeviceV2Payload{}, nil, nil, nil, nil
	}
	result := nativeRuntimeDeviceV2Payload{SKI: source.SKI, SHIPID: runtimeOptionalString(source.SHIPID), Address: source.Address, Type: source.Type, Description: cloneRuntimeString(source.Description), Metadata: cloneRuntimeMetadata(source.Metadata)}
	observations, err := nativeRuntimeObservationsV2(source.Opaque, observedAt)
	if err != nil {
		return nativeRuntimeDeviceV2Payload{}, nil, nil, nil, err
	}
	result.Observations = observations
	var entities []nativeRuntimeEntityV2Payload
	var features []nativeRuntimeFeatureV2Payload
	for _, entity := range source.Entities {
		if entity.DeviceAddress == "" || entity.EntityAddress == "" || entity.Type == "" {
			continue
		}
		entities = append(entities, nativeRuntimeEntityV2Payload{DeviceAddress: entity.DeviceAddress, EntityAddress: entity.EntityAddress, Type: entity.Type, Description: cloneRuntimeString(entity.Description)})
		for _, feature := range entity.Features {
			if feature.DeviceAddress == "" || feature.EntityAddress == "" || feature.FeatureAddress == "" || feature.Type == "" || feature.Role == "" {
				continue
			}
			features = append(features, nativeRuntimeFeatureV2Payload{DeviceAddress: feature.DeviceAddress, EntityAddress: feature.EntityAddress, FeatureAddress: feature.FeatureAddress, Type: feature.Type, Role: feature.Role, Description: cloneRuntimeString(feature.Description)})
		}
	}
	useCases := make([]nativeRuntimeUseCaseV2Payload, 0, len(source.UseCases))
	for _, useCase := range source.UseCases {
		if useCase.ContextAddress == "" || useCase.Name == "" || useCase.Actor == "" {
			continue
		}
		useCases = append(useCases, nativeRuntimeUseCaseV2Payload{ContextAddress: useCase.ContextAddress, Name: useCase.Name, Actor: useCase.Actor, ResolvedRole: cloneRuntimeString(useCase.ResolvedRole), Scenarios: append([]string(nil), useCase.Scenarios...), Version: cloneRuntimeString(useCase.Version), Availability: cloneRuntimeBool(useCase.Availability), DocumentSubrevision: cloneRuntimeString(useCase.DocumentSubrevision)})
	}
	return result, entities, features, useCases, nil
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
		integer, err := value.Int64()
		if err == nil {
			return nativeRuntimeValueV2Payload{Integer: &integer}, nil
		}
		number := value.String()
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
