package eebusruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

type RedactedSnapshotV1 struct {
	Meta     SnapshotMetaV1          `json:"meta"`
	Status   RuntimeObservationV1    `json:"status"`
	Pairing  []eebusraw.PairingState `json:"pairing"`
	Services []RedactedServiceV1     `json:"services"`
	Sessions []RedactedSessionV1     `json:"sessions"`
	Devices  []RedactedDeviceV1      `json:"devices"`
	Entities []RedactedEntityV1      `json:"entities"`
	Features []RedactedFeatureV1     `json:"features"`
	UseCases []RedactedUseCaseV1     `json:"usecases"`
}

type RedactedServiceV1 struct {
	ID      eebusraw.RedactedID `json:"id"`
	Kind    ServiceKindV1       `json:"kind"`
	Visible bool                `json:"visible"`
	Paired  bool                `json:"paired"`
}

type RedactedSessionV1 struct {
	ID     eebusraw.RedactedID    `json:"id"`
	Remote eebusraw.RedactedID    `json:"remote"`
	State  ObservedSessionStateV1 `json:"state"`
	Since  time.Time              `json:"since"`
}

type RedactedDeviceV1 struct {
	ID            eebusraw.RedactedID `json:"id"`
	Entities      []RedactedEntityV1  `json:"entities"`
	UseCaseClaims []RedactedUseCaseV1 `json:"usecase_claims"`
}

type RedactedEntityV1 struct {
	ID       eebusraw.RedactedID `json:"id"`
	Features []RedactedFeatureV1 `json:"features"`
}

type RedactedFeatureV1 struct {
	ID   eebusraw.RedactedID `json:"id"`
	Role FeatureRoleV1       `json:"role"`
}

type RedactedUseCaseV1 struct {
	ID eebusraw.RedactedID `json:"id"`
}

func BuildRedactedSnapshotV1(source SnapshotV1) (RedactedSnapshotV1, error) {
	raw, err := NewSnapshotV1(source)
	if err != nil {
		return RedactedSnapshotV1{}, err
	}
	result := RedactedSnapshotV1{
		Meta:     raw.Meta,
		Status:   raw.Status,
		Pairing:  make([]eebusraw.PairingState, 0, len(raw.Pairing)),
		Services: make([]RedactedServiceV1, 0, len(raw.Services)),
		Sessions: make([]RedactedSessionV1, 0, len(raw.Sessions)),
		Devices:  make([]RedactedDeviceV1, 0, len(raw.Devices)),
		Entities: make([]RedactedEntityV1, 0, len(raw.Entities)),
		Features: make([]RedactedFeatureV1, 0, len(raw.Features)),
		UseCases: make([]RedactedUseCaseV1, 0, len(raw.UseCases)),
	}
	result.Meta.MaskTier = eebusraw.MaskTierRedacted
	result.Meta.DataHash = ""
	for _, pairing := range raw.Pairing {
		result.Pairing = append(result.Pairing, pairing.State)
	}
	for _, service := range raw.Services {
		id, err := eebusraw.RedactID(eebusraw.IDKindPeer, serviceIdentityV1(service))
		if err != nil {
			return RedactedSnapshotV1{}, errors.New("redact service identity")
		}
		result.Services = append(result.Services, RedactedServiceV1{
			ID: id, Kind: service.Kind, Visible: service.Visible, Paired: service.Paired,
		})
	}
	for _, session := range raw.Sessions {
		id, err := eebusraw.RedactID(eebusraw.IDKindSession, session.ID)
		if err != nil {
			return RedactedSnapshotV1{}, errors.New("redact session identity")
		}
		remote, err := eebusraw.RedactID(eebusraw.IDKindRemoteSKI, session.RemoteSKI)
		if err != nil {
			return RedactedSnapshotV1{}, errors.New("redact session remote identity")
		}
		result.Sessions = append(result.Sessions, RedactedSessionV1{
			ID: id, Remote: remote, State: session.State, Since: session.Since.UTC(),
		})
	}
	entitiesByDevice := make(map[string][]RedactedEntityV1, len(raw.Devices))
	featuresByEntity := make(map[string][]RedactedFeatureV1, len(raw.Entities))
	useCasesByDevice := make(map[string][]RedactedUseCaseV1, len(raw.Devices))
	for _, feature := range raw.Features {
		id, err := eebusraw.RedactID(eebusraw.IDKindPeer, featureIdentityV1(feature))
		if err != nil {
			return RedactedSnapshotV1{}, errors.New("redact feature identity")
		}
		role := FeatureRoleV1Unspecified
		switch FeatureRoleV1(strings.ToLower(feature.Role)) {
		case FeatureRoleV1Client:
			role = FeatureRoleV1Client
		case FeatureRoleV1Server:
			role = FeatureRoleV1Server
		}
		value := RedactedFeatureV1{ID: id, Role: role}
		result.Features = append(result.Features, value)
		featuresByEntity[feature.DeviceAddress+"\x00"+feature.EntityAddress] =
			append(featuresByEntity[feature.DeviceAddress+"\x00"+feature.EntityAddress], value)
	}
	for _, entity := range raw.Entities {
		id, err := eebusraw.RedactID(eebusraw.IDKindPeer, entityIdentityV1(entity))
		if err != nil {
			return RedactedSnapshotV1{}, errors.New("redact entity identity")
		}
		value := RedactedEntityV1{
			ID:       id,
			Features: append([]RedactedFeatureV1(nil), featuresByEntity[entityIdentityV1(entity)]...),
		}
		sortRedactedFeaturesV1(value.Features)
		result.Entities = append(result.Entities, value)
		entitiesByDevice[entity.DeviceAddress] = append(entitiesByDevice[entity.DeviceAddress], value)
	}
	for _, useCase := range raw.UseCases {
		id, err := eebusraw.RedactID(eebusraw.IDKindPeer, useCaseIdentityV1(useCase))
		if err != nil {
			return RedactedSnapshotV1{}, errors.New("redact use-case identity")
		}
		value := RedactedUseCaseV1{ID: id}
		result.UseCases = append(result.UseCases, value)
		for _, device := range raw.Devices {
			if strings.Contains(useCase.ContextAddress, device.Address) {
				useCasesByDevice[device.Address] = append(useCasesByDevice[device.Address], value)
				break
			}
		}
	}
	for _, device := range raw.Devices {
		id, err := eebusraw.RedactID(eebusraw.IDKindPeer, deviceIdentityV1(device))
		if err != nil {
			return RedactedSnapshotV1{}, errors.New("redact device identity")
		}
		value := RedactedDeviceV1{
			ID:            id,
			Entities:      append([]RedactedEntityV1(nil), entitiesByDevice[device.Address]...),
			UseCaseClaims: append([]RedactedUseCaseV1(nil), useCasesByDevice[device.Address]...),
		}
		sortRedactedEntitiesV1(value.Entities)
		sortRedactedUseCasesV1(value.UseCaseClaims)
		result.Devices = append(result.Devices, value)
	}
	result = canonicalRedactedSnapshotV1(result)
	hash, err := result.computeDataHash()
	if err != nil {
		return RedactedSnapshotV1{}, err
	}
	result.Meta.DataHash = hash
	if err := result.validate(); err != nil {
		return RedactedSnapshotV1{}, err
	}
	return result, nil
}

func (snapshot RedactedSnapshotV1) MarshalJSON() ([]byte, error) {
	if err := snapshot.validate(); err != nil {
		return nil, err
	}
	type wire RedactedSnapshotV1
	return marshalJCSV1(wire(canonicalRedactedSnapshotV1(snapshot)))
}

func (RedactedSnapshotV1) String() string {
	return "redacted_snapshot_v1:[redacted]"
}

func (snapshot RedactedSnapshotV1) GoString() string {
	return snapshot.String()
}

func (snapshot RedactedSnapshotV1) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, snapshot.String())
}

func (snapshot RedactedSnapshotV1) validate() error {
	if snapshot.Meta.Contract != SnapshotContractV1 || snapshot.Meta.MaskTier != eebusraw.MaskTierRedacted {
		return errors.New("redacted snapshot metadata is invalid")
	}
	if !validSnapshotDigestV1(snapshot.Meta.DataHash) {
		return errors.New("redacted data_hash is invalid")
	}
	expected, err := snapshot.computeDataHash()
	if err != nil {
		return err
	}
	if expected != snapshot.Meta.DataHash {
		return errors.New("redacted data_hash does not match snapshot content")
	}
	for _, service := range snapshot.Services {
		if err := service.ID.Validate(); err != nil {
			return errors.New("redacted service identity is invalid")
		}
		if service.Kind != ServiceKindV1Local && service.Kind != ServiceKindV1Remote {
			return errors.New("redacted service kind is invalid")
		}
	}
	return nil
}

func (snapshot RedactedSnapshotV1) computeDataHash() (string, error) {
	canonical := canonicalRedactedSnapshotV1(snapshot)
	payload := struct {
		Contract      string                  `json:"contract"`
		Runtime       eebusraw.RedactedID     `json:"runtime"`
		LocalSKI      eebusraw.RedactedID     `json:"local_ski"`
		MaskTier      eebusraw.MaskTier       `json:"mask_tier"`
		DataTimestamp time.Time               `json:"data_timestamp"`
		Status        RuntimeObservationV1    `json:"status"`
		Pairing       []eebusraw.PairingState `json:"pairing"`
		Services      []RedactedServiceV1     `json:"services"`
		Sessions      []RedactedSessionV1     `json:"sessions"`
		Devices       []RedactedDeviceV1      `json:"devices"`
		Entities      []RedactedEntityV1      `json:"entities"`
		Features      []RedactedFeatureV1     `json:"features"`
		UseCases      []RedactedUseCaseV1     `json:"usecases"`
	}{
		Contract: canonical.Meta.Contract, Runtime: canonical.Meta.Runtime,
		LocalSKI: canonical.Meta.LocalSKI, MaskTier: canonical.Meta.MaskTier,
		DataTimestamp: canonical.Meta.DataTimestamp, Status: canonical.Status,
		Pairing: canonical.Pairing, Services: canonical.Services, Sessions: canonical.Sessions,
		Devices: canonical.Devices, Entities: canonical.Entities, Features: canonical.Features,
		UseCases: canonical.UseCases,
	}
	encoded, err := marshalJCSV1(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func canonicalRedactedSnapshotV1(source RedactedSnapshotV1) RedactedSnapshotV1 {
	result := source
	result.Meta.CapturedAt = result.Meta.CapturedAt.UTC()
	result.Meta.DataTimestamp = result.Meta.DataTimestamp.UTC()
	if result.Status.Degradation != nil {
		degradation := *result.Status.Degradation
		degradation.Since = degradation.Since.UTC()
		result.Status.Degradation = &degradation
	}
	result.Pairing = append([]eebusraw.PairingState(nil), source.Pairing...)
	sort.Slice(result.Pairing, func(left, right int) bool { return result.Pairing[left] < result.Pairing[right] })
	result.Services = append([]RedactedServiceV1(nil), source.Services...)
	sort.Slice(result.Services, func(left, right int) bool {
		return redactedIdentityKeyV1(result.Services[left].ID) < redactedIdentityKeyV1(result.Services[right].ID)
	})
	result.Sessions = append([]RedactedSessionV1(nil), source.Sessions...)
	for index := range result.Sessions {
		result.Sessions[index].Since = result.Sessions[index].Since.UTC()
	}
	sort.Slice(result.Sessions, func(left, right int) bool {
		return redactedIdentityKeyV1(result.Sessions[left].ID) < redactedIdentityKeyV1(result.Sessions[right].ID)
	})
	result.Devices = cloneRedactedDevicesV1(source.Devices)
	result.Entities = cloneRedactedEntitiesV1(source.Entities)
	result.Features = append([]RedactedFeatureV1(nil), source.Features...)
	result.UseCases = append([]RedactedUseCaseV1(nil), source.UseCases...)
	sortRedactedDevicesV1(result.Devices)
	sortRedactedEntitiesV1(result.Entities)
	sortRedactedFeaturesV1(result.Features)
	sortRedactedUseCasesV1(result.UseCases)
	if result.Pairing == nil {
		result.Pairing = []eebusraw.PairingState{}
	}
	if result.Services == nil {
		result.Services = []RedactedServiceV1{}
	}
	if result.Sessions == nil {
		result.Sessions = []RedactedSessionV1{}
	}
	if result.Devices == nil {
		result.Devices = []RedactedDeviceV1{}
	}
	if result.Entities == nil {
		result.Entities = []RedactedEntityV1{}
	}
	if result.Features == nil {
		result.Features = []RedactedFeatureV1{}
	}
	if result.UseCases == nil {
		result.UseCases = []RedactedUseCaseV1{}
	}
	return result
}

func cloneRedactedDevicesV1(source []RedactedDeviceV1) []RedactedDeviceV1 {
	result := make([]RedactedDeviceV1, len(source))
	for index, device := range source {
		result[index] = device
		result[index].Entities = cloneRedactedEntitiesV1(device.Entities)
		result[index].UseCaseClaims = append([]RedactedUseCaseV1(nil), device.UseCaseClaims...)
	}
	return result
}

func cloneRedactedEntitiesV1(source []RedactedEntityV1) []RedactedEntityV1 {
	result := make([]RedactedEntityV1, len(source))
	for index, entity := range source {
		result[index] = entity
		result[index].Features = append([]RedactedFeatureV1(nil), entity.Features...)
	}
	return result
}

func sortRedactedDevicesV1(values []RedactedDeviceV1) {
	for index := range values {
		sortRedactedEntitiesV1(values[index].Entities)
		sortRedactedUseCasesV1(values[index].UseCaseClaims)
	}
	sort.Slice(values, func(left, right int) bool {
		return redactedIdentityKeyV1(values[left].ID) < redactedIdentityKeyV1(values[right].ID)
	})
}

func sortRedactedEntitiesV1(values []RedactedEntityV1) {
	for index := range values {
		sortRedactedFeaturesV1(values[index].Features)
	}
	sort.Slice(values, func(left, right int) bool {
		return redactedIdentityKeyV1(values[left].ID) < redactedIdentityKeyV1(values[right].ID)
	})
}

func sortRedactedFeaturesV1(values []RedactedFeatureV1) {
	sort.Slice(values, func(left, right int) bool {
		return redactedIdentityKeyV1(values[left].ID) < redactedIdentityKeyV1(values[right].ID)
	})
}

func sortRedactedUseCasesV1(values []RedactedUseCaseV1) {
	sort.Slice(values, func(left, right int) bool {
		return redactedIdentityKeyV1(values[left].ID) < redactedIdentityKeyV1(values[right].ID)
	})
}

func redactedIdentityKeyV1(id eebusraw.RedactedID) string {
	return string(id.Kind) + "\x00" + id.Digest
}
