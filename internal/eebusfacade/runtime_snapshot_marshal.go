package eebusfacade

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

type runtimeSnapshotSerializer func(string, string, []runtimeGraphObservation, time.Time) ([]byte, error)

type runtimeSnapshotPayload struct {
	Meta     runtimeSnapshotMetaPayload `json:"meta"`
	Status   runtimeStatusPayload       `json:"status"`
	Pairing  []runtimePairingPayload    `json:"pairing"`
	Services []runtimeServicePayload    `json:"services"`
	Sessions []runtimeSessionPayload    `json:"sessions"`
	Devices  []runtimeDevicePayload     `json:"devices"`
	Entities []runtimeEntityPayload     `json:"entities"`
	Features []runtimeFeaturePayload    `json:"features"`
	UseCases []runtimeUseCasePayload    `json:"use_cases"`
	Opaque   []runtimeOpaquePayload     `json:"opaque"`
}

type runtimeSnapshotMetaPayload struct {
	Contract      string              `json:"contract"`
	Runtime       eebusraw.RedactedID `json:"runtime"`
	LocalSKI      string              `json:"local_ski"`
	MaskTier      eebusraw.MaskTier   `json:"mask_tier"`
	CapturedAt    time.Time           `json:"captured_at"`
	DataTimestamp time.Time           `json:"data_timestamp"`
}

type runtimeStatusPayload struct {
	State       string                     `json:"state"`
	Degradation *runtimeDegradationPayload `json:"degradation,omitempty"`
}

type runtimeDegradationPayload struct {
	Reason string    `json:"reason"`
	Since  time.Time `json:"since"`
}

type runtimePairingPayload struct {
	RemoteSKI string    `json:"remote_ski"`
	State     string    `json:"state"`
	Since     time.Time `json:"since"`
}

type runtimeServicePayload struct {
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

type runtimeSessionPayload struct {
	ID        string    `json:"id"`
	RemoteSKI string    `json:"remote_ski"`
	State     string    `json:"state"`
	Since     time.Time `json:"since"`
}

type runtimeDevicePayload struct {
	SKI         string                  `json:"ski"`
	SHIPID      *string                 `json:"ship_id,omitempty"`
	Address     string                  `json:"address"`
	Type        string                  `json:"type"`
	Description *string                 `json:"description,omitempty"`
	Metadata    *map[string]string      `json:"metadata,omitempty"`
	Opaque      *[]runtimeOpaquePayload `json:"opaque,omitempty"`
}

type runtimeEntityPayload struct {
	DeviceAddress string  `json:"device_address"`
	EntityAddress string  `json:"entity_address"`
	Type          string  `json:"type"`
	Description   *string `json:"description,omitempty"`
}

type runtimeFeaturePayload struct {
	DeviceAddress  string  `json:"device_address"`
	EntityAddress  string  `json:"entity_address"`
	FeatureAddress string  `json:"feature_address"`
	Type           string  `json:"type"`
	Role           string  `json:"role"`
	Description    *string `json:"description,omitempty"`
}

type runtimeUseCasePayload struct {
	ContextAddress      string   `json:"context_address"`
	Name                string   `json:"name"`
	Actor               string   `json:"actor"`
	ResolvedRole        *string  `json:"resolved_role,omitempty"`
	Scenarios           []string `json:"scenarios"`
	Version             *string  `json:"version,omitempty"`
	Availability        *bool    `json:"availability,omitempty"`
	DocumentSubrevision *string  `json:"document_subrevision,omitempty"`
}

type runtimeOpaquePayload struct {
	Path   string `json:"path"`
	Source string `json:"source"`
	Value  any    `json:"value"`
}

func marshalRuntimeSnapshot(graph []runtimeGraphObservation, now time.Time) ([]byte, error) {
	if len(graph) == 0 {
		return nil, errors.New("runtime graph is empty")
	}
	first := graph[0]
	return marshalRuntimeSnapshotWithIdentity(first.RuntimeID, first.LocalSKI, graph, now)
}

func marshalRuntimeSnapshotWithIdentity(runtimeIdentity, localIdentity string, graph []runtimeGraphObservation, now time.Time) ([]byte, error) {
	runtimeID, err := eebusraw.RedactID(eebusraw.IDKindPeer, runtimeIdentity)
	if err != nil {
		return nil, err
	}
	now = now.UTC()
	payload := runtimeSnapshotPayload{
		Meta: runtimeSnapshotMetaPayload{
			Contract:      "helianthus.eebus.runtime.raw-snapshot.v1",
			Runtime:       runtimeID,
			LocalSKI:      localIdentity,
			MaskTier:      eebusraw.MaskTier("raw"),
			CapturedAt:    now,
			DataTimestamp: now,
		},
		Status:   runtimeStatusPayload{State: "starting"},
		Pairing:  []runtimePairingPayload{},
		Services: []runtimeServicePayload{},
		Sessions: []runtimeSessionPayload{},
		Devices:  []runtimeDevicePayload{},
		Entities: []runtimeEntityPayload{},
		Features: []runtimeFeaturePayload{},
		UseCases: []runtimeUseCasePayload{},
		Opaque:   []runtimeOpaquePayload{},
	}
	visible := false
	connected := false
	disconnected := false
	trustDegradation := ""
	for _, remote := range graph {
		if remote.PairingState != "" {
			payload.Pairing = append(payload.Pairing, runtimePairingPayload{
				RemoteSKI: remote.RemoteSKI, State: remote.PairingState, Since: remote.Since,
			})
		}
		if remote.RemoteSKI != "" {
			payload.Services = append(payload.Services, runtimeServicePayload{
				SKI: remote.RemoteSKI, SHIPID: runtimeOptionalString(remote.ShipID), Kind: "remote",
				Visible: remote.Visible, Paired: remote.Paired, Name: runtimeOptionalString(remote.ServiceName),
				Identifier: runtimeOptionalString(remote.ServiceIdentifier), Brand: runtimeOptionalString(remote.ServiceBrand),
				Type: runtimeOptionalString(remote.ServiceType), Model: runtimeOptionalString(remote.ServiceModel),
			})
		}
		if remote.SessionID != "" && remote.SessionState != "" {
			payload.Sessions = append(payload.Sessions, runtimeSessionPayload{
				ID: remote.SessionID, RemoteSKI: remote.RemoteSKI,
				State: remote.SessionState, Since: remote.Since,
			})
		}
		for _, device := range remote.Devices {
			if device.SHIPID == "" {
				device.SHIPID = remote.ShipID
			}
			if device.SKI == "" {
				device.SKI = remote.RemoteSKI
			}
			devicePayload, entities, features, useCases := marshalRuntimeDevice(device)
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
		payload.Status.State = "degraded"
		payload.Status.Degradation = &runtimeDegradationPayload{Reason: trustDegradation, Since: now}
	} else if connected {
		payload.Status.State = "ready"
	} else if disconnected {
		payload.Status.State = "degraded"
		payload.Status.Degradation = &runtimeDegradationPayload{Reason: "remote-disconnect", Since: now}
	} else if !visible {
		payload.Status.State = "degraded"
		payload.Status.Degradation = &runtimeDegradationPayload{Reason: "no-visible-services", Since: now}
	}
	return json.Marshal(payload)
}

func marshalRuntimeDevice(source runtimeDeviceObservation) (
	runtimeDevicePayload,
	[]runtimeEntityPayload,
	[]runtimeFeaturePayload,
	[]runtimeUseCasePayload,
) {
	if source.SKI == "" || source.Address == "" || source.Type == "" {
		return runtimeDevicePayload{}, nil, nil, nil
	}
	result := runtimeDevicePayload{
		SKI: source.SKI, SHIPID: runtimeOptionalString(source.SHIPID),
		Address: source.Address, Type: source.Type, Description: cloneRuntimeString(source.Description),
	}
	if source.Metadata != nil {
		metadata := cloneRuntimeMetadata(source.Metadata)
		result.Metadata = &metadata
	}
	if source.Opaque != nil {
		opaque := cloneRuntimeOpaqueV1(source.Opaque)
		result.Opaque = &opaque
	}
	var entities []runtimeEntityPayload
	var features []runtimeFeaturePayload
	for _, entity := range source.Entities {
		if entity.DeviceAddress == "" || entity.EntityAddress == "" || entity.Type == "" {
			continue
		}
		entities = append(entities, runtimeEntityPayload{
			DeviceAddress: entity.DeviceAddress, EntityAddress: entity.EntityAddress,
			Type: entity.Type, Description: cloneRuntimeString(entity.Description),
		})
		for _, feature := range entity.Features {
			if feature.DeviceAddress == "" || feature.EntityAddress == "" ||
				feature.FeatureAddress == "" || feature.Type == "" || feature.Role == "" {
				continue
			}
			features = append(features, runtimeFeaturePayload{
				DeviceAddress: feature.DeviceAddress, EntityAddress: feature.EntityAddress,
				FeatureAddress: feature.FeatureAddress, Type: feature.Type, Role: feature.Role,
				Description: cloneRuntimeString(feature.Description),
			})
		}
	}
	useCases := make([]runtimeUseCasePayload, 0, len(source.UseCases))
	for _, useCase := range source.UseCases {
		if useCase.ContextAddress == "" || useCase.Name == "" || useCase.Actor == "" {
			continue
		}
		useCases = append(useCases, runtimeUseCasePayload{
			ContextAddress: useCase.ContextAddress, Name: useCase.Name, Actor: useCase.Actor,
			ResolvedRole:        cloneRuntimeString(useCase.ResolvedRole),
			Scenarios:           append([]string(nil), useCase.Scenarios...),
			Version:             cloneRuntimeString(useCase.Version),
			Availability:        cloneRuntimeBool(useCase.Availability),
			DocumentSubrevision: cloneRuntimeString(useCase.DocumentSubrevision),
		})
	}
	return result, entities, features, useCases
}

func runtimeOptionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func detachedRuntimeJSONValueV1(value any) (any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, errors.New("encode detailed discovery value")
	}
	var detached any
	if err := json.Unmarshal(encoded, &detached); err != nil {
		return nil, errors.New("decode detailed discovery value")
	}
	return detached, nil
}

func cloneRuntimeOpaqueV1(source []runtimeOpaquePayload) []runtimeOpaquePayload {
	if source == nil {
		return nil
	}
	result := make([]runtimeOpaquePayload, len(source))
	for index, observation := range source {
		result[index] = observation
		value, err := detachedRuntimeJSONValueV1(observation.Value)
		if err == nil {
			result[index].Value = value
		} else {
			result[index].Value = nil
		}
	}
	return result
}
