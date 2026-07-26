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
	"unicode/utf8"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

const SnapshotContractV1 = "helianthus.eebus.runtime.raw-snapshot.v1"

const snapshotMaskTierRawV1 eebusraw.MaskTier = "raw"

type ObservedRuntimeStateV1 string

const (
	ObservedRuntimeStateV1Unknown  ObservedRuntimeStateV1 = "unknown"
	ObservedRuntimeStateV1Stopped  ObservedRuntimeStateV1 = "stopped"
	ObservedRuntimeStateV1Starting ObservedRuntimeStateV1 = "starting"
	ObservedRuntimeStateV1Ready    ObservedRuntimeStateV1 = "ready"
	ObservedRuntimeStateV1Degraded ObservedRuntimeStateV1 = "degraded"
	ObservedRuntimeStateV1Shutdown ObservedRuntimeStateV1 = "shutdown"
)

type DegradationReasonV1 string

const (
	DegradationReasonV1MissingDiscovery       DegradationReasonV1 = "missing-discovery"
	DegradationReasonV1DeniedTrust            DegradationReasonV1 = "denied-trust"
	DegradationReasonV1RemoteDisconnect       DegradationReasonV1 = "remote-disconnect"
	DegradationReasonV1CertificateUnavailable DegradationReasonV1 = "certificate-unavailable"
	DegradationReasonV1NoVisibleServices      DegradationReasonV1 = "no-visible-services"
	DegradationReasonV1NoData                 DegradationReasonV1 = "no-data"
)

type ServiceKindV1 string

const (
	ServiceKindV1Local  ServiceKindV1 = "local"
	ServiceKindV1Remote ServiceKindV1 = "remote"
)

type ObservedSessionStateV1 string

const (
	ObservedSessionStateV1Unknown      ObservedSessionStateV1 = "unknown"
	ObservedSessionStateV1Connecting   ObservedSessionStateV1 = "connecting"
	ObservedSessionStateV1Connected    ObservedSessionStateV1 = "connected"
	ObservedSessionStateV1Disconnected ObservedSessionStateV1 = "disconnected"
	ObservedSessionStateV1Degraded     ObservedSessionStateV1 = "degraded"
)

type FeatureRoleV1 string

const (
	FeatureRoleV1Unspecified FeatureRoleV1 = ""
	FeatureRoleV1Client      FeatureRoleV1 = "client"
	FeatureRoleV1Server      FeatureRoleV1 = "server"
)

type SnapshotV1 struct {
	Meta     SnapshotMetaV1         `json:"meta"`
	Status   RuntimeObservationV1   `json:"status"`
	Pairing  []PairingObservationV1 `json:"pairing"`
	Services []ServiceV1            `json:"services"`
	Sessions []SessionV1            `json:"sessions"`
	Devices  []DeviceV1             `json:"devices"`
	Entities []EntityV1             `json:"entities"`
	Features []FeatureV1            `json:"features"`
	UseCases []UseCaseV1            `json:"usecases"`
	Opaque   []OpaqueObservationV1  `json:"opaque"`
}

type SnapshotMetaV1 struct {
	Contract      string              `json:"contract"`
	Runtime       eebusraw.RedactedID `json:"runtime"`
	LocalSKI      eebusraw.RedactedID `json:"local_ski"`
	MaskTier      eebusraw.MaskTier   `json:"mask_tier"`
	CapturedAt    time.Time           `json:"captured_at"`
	DataTimestamp time.Time           `json:"data_timestamp"`
	DataHash      string              `json:"data_hash,omitempty"`
}

type RuntimeObservationV1 struct {
	State       ObservedRuntimeStateV1 `json:"state"`
	Degradation *DegradationV1         `json:"degradation,omitempty"`
}

type DegradationV1 struct {
	Reason DegradationReasonV1 `json:"reason"`
	Since  time.Time           `json:"since"`
}

type PairingObservationV1 struct {
	RemoteSKI string                 `json:"remote_ski"`
	State     eebusraw.PairingState  `json:"state"`
	Since     time.Time              `json:"since"`
	Opaque    *[]OpaqueObservationV1 `json:"opaque,omitempty"`
}

type ServiceV1 struct {
	SKI             string                 `json:"ski"`
	SHIPID          *string                `json:"ship_id,omitempty"`
	Kind            ServiceKindV1          `json:"kind"`
	Visible         bool                   `json:"visible"`
	Paired          bool                   `json:"paired"`
	Name            string                 `json:"name"`
	Identifier      string                 `json:"identifier"`
	Brand           string                 `json:"brand"`
	Type            string                 `json:"type"`
	Model           string                 `json:"model"`
	SecondaryDigest *string                `json:"secondary_digest,omitempty"`
	Opaque          *[]OpaqueObservationV1 `json:"opaque,omitempty"`
}

type SessionV1 struct {
	ID        string                 `json:"id"`
	RemoteSKI string                 `json:"remote_ski"`
	State     ObservedSessionStateV1 `json:"state"`
	Since     time.Time              `json:"since"`
	Opaque    *[]OpaqueObservationV1 `json:"opaque,omitempty"`
}

type DeviceV1 struct {
	SKI             string                 `json:"ski"`
	SHIPID          *string                `json:"ship_id,omitempty"`
	Address         string                 `json:"address"`
	Type            string                 `json:"type"`
	Description     *string                `json:"description,omitempty"`
	Metadata        *MetadataV1            `json:"metadata,omitempty"`
	SecondaryDigest *string                `json:"secondary_digest,omitempty"`
	Opaque          *[]OpaqueObservationV1 `json:"opaque,omitempty"`
}

type EntityV1 struct {
	DeviceAddress   string                 `json:"device_address"`
	EntityAddress   string                 `json:"entity_address"`
	Type            string                 `json:"type"`
	Description     *string                `json:"description,omitempty"`
	SecondaryDigest *string                `json:"secondary_digest,omitempty"`
	Opaque          *[]OpaqueObservationV1 `json:"opaque,omitempty"`
}

type FeatureV1 struct {
	DeviceAddress   string                 `json:"device_address"`
	EntityAddress   string                 `json:"entity_address"`
	FeatureAddress  string                 `json:"feature_address"`
	Type            string                 `json:"type"`
	Role            string                 `json:"role"`
	Description     *string                `json:"description,omitempty"`
	SecondaryDigest *string                `json:"secondary_digest,omitempty"`
	Opaque          *[]OpaqueObservationV1 `json:"opaque,omitempty"`
}

type UseCaseV1 struct {
	ContextAddress      string                 `json:"context_address"`
	Name                string                 `json:"name"`
	Actor               string                 `json:"actor"`
	ResolvedRole        *string                `json:"resolved_role,omitempty"`
	Scenarios           *[]string              `json:"scenarios,omitempty"`
	Version             *string                `json:"version,omitempty"`
	Availability        *bool                  `json:"availability,omitempty"`
	DocumentSubrevision *string                `json:"document_subrevision,omitempty"`
	SecondaryDigest     *string                `json:"secondary_digest,omitempty"`
	Opaque              *[]OpaqueObservationV1 `json:"opaque,omitempty"`
}

func NewSnapshotV1(draft SnapshotV1) (SnapshotV1, error) {
	snapshot := canonicalSnapshotV1(draft)
	if err := snapshot.validate(false); err != nil {
		return SnapshotV1{}, err
	}
	computed, err := snapshot.computeDataHash()
	if err != nil {
		return SnapshotV1{}, err
	}
	if snapshot.Meta.DataHash != "" && snapshot.Meta.DataHash != computed {
		return SnapshotV1{}, errors.New("data_hash does not match snapshot content")
	}
	snapshot.Meta.DataHash = computed
	return snapshot, nil
}

func (snapshot SnapshotV1) Validate() error {
	return snapshot.validate(true)
}

func (snapshot SnapshotV1) validate(checkHash bool) error {
	if snapshot.Meta.Contract != SnapshotContractV1 {
		return fmt.Errorf("contract must be %q", SnapshotContractV1)
	}
	if err := validateSnapshotIDV1(snapshot.Meta.Runtime, eebusraw.IDKindPeer, eebusraw.IDKindLocalSKI); err != nil {
		return fmt.Errorf("runtime identity is invalid: %w", err)
	}
	if err := validateSnapshotIDV1(snapshot.Meta.LocalSKI, eebusraw.IDKindLocalSKI); err != nil {
		return fmt.Errorf("local identity is invalid: %w", err)
	}
	if snapshot.Meta.MaskTier != snapshotMaskTierRawV1 {
		return errors.New("mask_tier must be raw")
	}
	if err := validateSnapshotTimestampV1(snapshot.Meta.CapturedAt, true); err != nil {
		return fmt.Errorf("captured_at is invalid: %w", err)
	}
	if err := validateSnapshotTimestampV1(snapshot.Meta.DataTimestamp, true); err != nil {
		return fmt.Errorf("data_timestamp is invalid: %w", err)
	}
	if err := validateRuntimeObservationV1(snapshot.Status); err != nil {
		return err
	}
	if err := validatePairingV1(snapshot.Pairing); err != nil {
		return err
	}
	if err := validateServicesV1(snapshot.Services); err != nil {
		return err
	}
	if err := validateSessionsV1(snapshot.Sessions); err != nil {
		return err
	}
	if err := validateDevicesV1(snapshot.Devices); err != nil {
		return err
	}
	if err := validateEntitiesV1(snapshot.Entities); err != nil {
		return err
	}
	if err := validateFeaturesV1(snapshot.Features); err != nil {
		return err
	}
	if err := validateUseCasesV1(snapshot.UseCases); err != nil {
		return err
	}
	if err := validateSnapshotOpaqueV1(snapshot); err != nil {
		return err
	}
	if err := validateSnapshotSecretsV1(snapshot); err != nil {
		return err
	}
	if snapshot.Meta.DataHash != "" && !validSnapshotDigestV1(snapshot.Meta.DataHash) {
		return errors.New("data_hash must use lowercase sha256:<64 hex chars>")
	}
	if checkHash && snapshot.Meta.DataHash != "" {
		expected, err := snapshot.computeDataHash()
		if err != nil {
			return err
		}
		if snapshot.Meta.DataHash != expected {
			return errors.New("data_hash does not match snapshot content")
		}
	}
	return nil
}

func (snapshot SnapshotV1) Clone() SnapshotV1 {
	result := snapshot
	if snapshot.Status.Degradation != nil {
		value := *snapshot.Status.Degradation
		result.Status.Degradation = &value
	}
	result.Pairing = make([]PairingObservationV1, len(snapshot.Pairing))
	for index, value := range snapshot.Pairing {
		result.Pairing[index] = value
		result.Pairing[index].Opaque = cloneOpaquePointerV1(value.Opaque)
	}
	result.Services = make([]ServiceV1, len(snapshot.Services))
	for index, value := range snapshot.Services {
		result.Services[index] = value
		result.Services[index].SHIPID = cloneStringPointerV1(value.SHIPID)
		result.Services[index].SecondaryDigest = cloneStringPointerV1(value.SecondaryDigest)
		result.Services[index].Opaque = cloneOpaquePointerV1(value.Opaque)
	}
	result.Sessions = make([]SessionV1, len(snapshot.Sessions))
	for index, value := range snapshot.Sessions {
		result.Sessions[index] = value
		result.Sessions[index].Opaque = cloneOpaquePointerV1(value.Opaque)
	}
	result.Devices = make([]DeviceV1, len(snapshot.Devices))
	for index, value := range snapshot.Devices {
		result.Devices[index] = value
		result.Devices[index].SHIPID = cloneStringPointerV1(value.SHIPID)
		result.Devices[index].Description = cloneStringPointerV1(value.Description)
		result.Devices[index].Metadata = cloneMetadataV1(value.Metadata)
		result.Devices[index].SecondaryDigest = cloneStringPointerV1(value.SecondaryDigest)
		result.Devices[index].Opaque = cloneOpaquePointerV1(value.Opaque)
	}
	result.Entities = make([]EntityV1, len(snapshot.Entities))
	for index, value := range snapshot.Entities {
		result.Entities[index] = value
		result.Entities[index].Description = cloneStringPointerV1(value.Description)
		result.Entities[index].SecondaryDigest = cloneStringPointerV1(value.SecondaryDigest)
		result.Entities[index].Opaque = cloneOpaquePointerV1(value.Opaque)
	}
	result.Features = make([]FeatureV1, len(snapshot.Features))
	for index, value := range snapshot.Features {
		result.Features[index] = value
		result.Features[index].Description = cloneStringPointerV1(value.Description)
		result.Features[index].SecondaryDigest = cloneStringPointerV1(value.SecondaryDigest)
		result.Features[index].Opaque = cloneOpaquePointerV1(value.Opaque)
	}
	result.UseCases = make([]UseCaseV1, len(snapshot.UseCases))
	for index, value := range snapshot.UseCases {
		result.UseCases[index] = value
		result.UseCases[index].ResolvedRole = cloneStringPointerV1(value.ResolvedRole)
		result.UseCases[index].Scenarios = cloneStringsPointerV1(value.Scenarios)
		result.UseCases[index].Version = cloneStringPointerV1(value.Version)
		result.UseCases[index].Availability = cloneBoolPointerV1(value.Availability)
		result.UseCases[index].DocumentSubrevision = cloneStringPointerV1(value.DocumentSubrevision)
		result.UseCases[index].SecondaryDigest = cloneStringPointerV1(value.SecondaryDigest)
		result.UseCases[index].Opaque = cloneOpaquePointerV1(value.Opaque)
	}
	result.Opaque = cloneOpaqueObservationsV1(snapshot.Opaque)
	return result
}

func (snapshot SnapshotV1) ComputeDataHash() (string, error) {
	if err := snapshot.validate(false); err != nil {
		return "", err
	}
	return snapshot.computeDataHash()
}

func (snapshot SnapshotV1) computeDataHash() (string, error) {
	canonical := canonicalSnapshotV1(snapshot)
	payload := struct {
		Contract      string                 `json:"contract"`
		Runtime       eebusraw.RedactedID    `json:"runtime"`
		LocalSKI      eebusraw.RedactedID    `json:"local_ski"`
		MaskTier      eebusraw.MaskTier      `json:"mask_tier"`
		DataTimestamp time.Time              `json:"data_timestamp"`
		Status        RuntimeObservationV1   `json:"status"`
		Pairing       []PairingObservationV1 `json:"pairing"`
		Services      []ServiceV1            `json:"services"`
		Sessions      []SessionV1            `json:"sessions"`
		Devices       []DeviceV1             `json:"devices"`
		Entities      []EntityV1             `json:"entities"`
		Features      []FeatureV1            `json:"features"`
		UseCases      []UseCaseV1            `json:"usecases"`
		Opaque        []OpaqueObservationV1  `json:"opaque"`
	}{
		Contract: canonical.Meta.Contract, Runtime: canonical.Meta.Runtime,
		LocalSKI: canonical.Meta.LocalSKI, MaskTier: canonical.Meta.MaskTier,
		DataTimestamp: canonical.Meta.DataTimestamp, Status: canonical.Status,
		Pairing: canonical.Pairing, Services: canonical.Services, Sessions: canonical.Sessions,
		Devices: canonical.Devices, Entities: canonical.Entities, Features: canonical.Features,
		UseCases: canonical.UseCases, Opaque: canonical.Opaque,
	}
	encoded, err := marshalJCSV1(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (snapshot SnapshotV1) MarshalJSON() ([]byte, error) {
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	canonical := canonicalSnapshotV1(snapshot)
	type wire SnapshotV1
	return marshalJCSV1(wire(canonical))
}

func (SnapshotV1) String() string {
	return "snapshot_v1:[redacted]"
}

func (snapshot SnapshotV1) GoString() string {
	return snapshot.String()
}

func (snapshot SnapshotV1) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, snapshot.String())
}

func validateRuntimeObservationV1(observation RuntimeObservationV1) error {
	switch observation.State {
	case ObservedRuntimeStateV1Unknown, ObservedRuntimeStateV1Stopped, ObservedRuntimeStateV1Starting,
		ObservedRuntimeStateV1Ready, ObservedRuntimeStateV1Degraded, ObservedRuntimeStateV1Shutdown:
	default:
		return errors.New("runtime state is unsupported")
	}
	if observation.State == ObservedRuntimeStateV1Degraded && observation.Degradation == nil {
		return errors.New("degraded runtime state requires details")
	}
	if observation.State != ObservedRuntimeStateV1Degraded && observation.Degradation != nil {
		return errors.New("runtime degradation details require degraded state")
	}
	if observation.Degradation == nil {
		return nil
	}
	switch observation.Degradation.Reason {
	case DegradationReasonV1MissingDiscovery, DegradationReasonV1DeniedTrust,
		DegradationReasonV1RemoteDisconnect, DegradationReasonV1CertificateUnavailable,
		DegradationReasonV1NoVisibleServices, DegradationReasonV1NoData:
	default:
		return errors.New("runtime degradation reason is unsupported")
	}
	return validateSnapshotTimestampV1(observation.Degradation.Since, true)
}

func validatePairingV1(values []PairingObservationV1) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := validateRequiredTextV1(value.RemoteSKI, 4096); err != nil {
			return errors.New("pairing remote_ski is invalid")
		}
		switch value.State {
		case eebusraw.PairingStateUnknown, eebusraw.PairingStateUnpaired,
			eebusraw.PairingStatePaired, eebusraw.PairingStateDenied:
		default:
			return errors.New("pairing state is unsupported")
		}
		if err := validateSnapshotTimestampV1(value.Since, true); err != nil {
			return errors.New("pairing timestamp is invalid")
		}
		if _, exists := seen[value.RemoteSKI]; exists {
			return errors.New("pairing observations contain a duplicate identity")
		}
		seen[value.RemoteSKI] = struct{}{}
	}
	return nil
}

func validateServicesV1(values []ServiceV1) error {
	if len(values) > 256 {
		return errors.New("service count exceeds the contract limit")
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		for _, required := range []string{value.SKI, value.Name, value.Identifier, value.Brand, value.Type, value.Model} {
			if err := validateRequiredTextV1(required, 4096); err != nil {
				return errors.New("service contains a missing or invalid raw field")
			}
		}
		if err := validateOptionalTextV1(value.SHIPID, 4096); err != nil {
			return errors.New("service ship_id is invalid")
		}
		if value.Kind != ServiceKindV1Local && value.Kind != ServiceKindV1Remote {
			return errors.New("service kind is unsupported")
		}
		if err := validateSecondaryDigestV1(value.SecondaryDigest); err != nil {
			return err
		}
		key := serviceIdentityV1(value)
		if _, exists := seen[key]; exists {
			return errors.New("services contain a duplicate identity")
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateSessionsV1(values []SessionV1) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if validateRequiredTextV1(value.ID, 4096) != nil || validateRequiredTextV1(value.RemoteSKI, 4096) != nil {
			return errors.New("session contains a missing or invalid raw identity")
		}
		switch value.State {
		case ObservedSessionStateV1Unknown, ObservedSessionStateV1Connecting,
			ObservedSessionStateV1Connected, ObservedSessionStateV1Disconnected, ObservedSessionStateV1Degraded:
		default:
			return errors.New("session state is unsupported")
		}
		if err := validateSnapshotTimestampV1(value.Since, true); err != nil {
			return errors.New("session timestamp is invalid")
		}
		if _, exists := seen[value.ID]; exists {
			return errors.New("sessions contain a duplicate identity")
		}
		seen[value.ID] = struct{}{}
	}
	return nil
}

func validateDevicesV1(values []DeviceV1) error {
	if len(values) > 1024 {
		return errors.New("device count exceeds the contract limit")
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if validateRequiredTextV1(value.SKI, 4096) != nil ||
			validateRequiredTextV1(value.Address, 512) != nil ||
			validateRequiredTextV1(value.Type, 4096) != nil {
			return errors.New("device contains a missing or invalid raw field")
		}
		if validateOptionalTextV1(value.SHIPID, 4096) != nil ||
			validateOptionalTextV1(value.Description, 4096) != nil {
			return errors.New("device contains an invalid optional field")
		}
		if err := validateMetadataV1(value.Metadata); err != nil {
			return err
		}
		if err := validateSecondaryDigestV1(value.SecondaryDigest); err != nil {
			return err
		}
		key := deviceIdentityV1(value)
		if _, exists := seen[key]; exists {
			return errors.New("devices contain a duplicate identity")
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateEntitiesV1(values []EntityV1) error {
	if len(values) > 4096 {
		return errors.New("entity count exceeds the contract limit")
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if validateRequiredTextV1(value.DeviceAddress, 512) != nil ||
			validateRequiredTextV1(value.EntityAddress, 512) != nil ||
			validateRequiredTextV1(value.Type, 4096) != nil {
			return errors.New("entity contains a missing or invalid raw field")
		}
		if validateOptionalTextV1(value.Description, 4096) != nil {
			return errors.New("entity description is invalid")
		}
		if err := validateSecondaryDigestV1(value.SecondaryDigest); err != nil {
			return err
		}
		key := entityIdentityV1(value)
		if _, exists := seen[key]; exists {
			return errors.New("entities contain a duplicate identity")
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateFeaturesV1(values []FeatureV1) error {
	if len(values) > 16384 {
		return errors.New("feature count exceeds the contract limit")
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if validateRequiredTextV1(value.DeviceAddress, 512) != nil ||
			validateRequiredTextV1(value.EntityAddress, 512) != nil ||
			validateRequiredTextV1(value.FeatureAddress, 512) != nil ||
			validateRequiredTextV1(value.Type, 4096) != nil ||
			validateRequiredTextV1(value.Role, 4096) != nil {
			return errors.New("feature contains a missing or invalid raw field")
		}
		if validateOptionalTextV1(value.Description, 4096) != nil {
			return errors.New("feature description is invalid")
		}
		if err := validateSecondaryDigestV1(value.SecondaryDigest); err != nil {
			return err
		}
		key := featureIdentityV1(value)
		if _, exists := seen[key]; exists {
			return errors.New("features contain a duplicate identity")
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateUseCasesV1(values []UseCaseV1) error {
	if len(values) > 4096 {
		return errors.New("use-case count exceeds the contract limit")
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if validateRequiredTextV1(value.ContextAddress, 512) != nil ||
			validateRequiredTextV1(value.Name, 4096) != nil ||
			validateRequiredTextV1(value.Actor, 4096) != nil {
			return errors.New("use case contains a missing or invalid raw field")
		}
		for _, optional := range []*string{
			value.ResolvedRole, value.Version, value.DocumentSubrevision,
		} {
			if validateOptionalTextV1(optional, 4096) != nil {
				return errors.New("use case contains an invalid optional field")
			}
		}
		if value.Scenarios != nil {
			if len(*value.Scenarios) > 128 {
				return errors.New("use-case scenarios exceed the member limit")
			}
			seenScenarios := make(map[string]struct{}, len(*value.Scenarios))
			for _, scenario := range *value.Scenarios {
				if validateRequiredTextV1(scenario, 4096) != nil {
					return errors.New("use-case scenario is invalid")
				}
				if _, exists := seenScenarios[scenario]; exists {
					return errors.New("use-case scenarios contain a duplicate")
				}
				seenScenarios[scenario] = struct{}{}
			}
		}
		if err := validateSecondaryDigestV1(value.SecondaryDigest); err != nil {
			return err
		}
		key := useCaseIdentityV1(value)
		if _, exists := seen[key]; exists {
			return errors.New("use cases contain a duplicate identity")
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateSnapshotOpaqueV1(snapshot SnapshotV1) error {
	sets := [][]OpaqueObservationV1{snapshot.Opaque}
	for _, value := range snapshot.Pairing {
		sets = appendOpaqueSetV1(sets, value.Opaque)
	}
	for _, value := range snapshot.Services {
		sets = appendOpaqueSetV1(sets, value.Opaque)
	}
	for _, value := range snapshot.Sessions {
		sets = appendOpaqueSetV1(sets, value.Opaque)
	}
	for _, value := range snapshot.Devices {
		sets = appendOpaqueSetV1(sets, value.Opaque)
	}
	for _, value := range snapshot.Entities {
		sets = appendOpaqueSetV1(sets, value.Opaque)
	}
	for _, value := range snapshot.Features {
		sets = appendOpaqueSetV1(sets, value.Opaque)
	}
	for _, value := range snapshot.UseCases {
		sets = appendOpaqueSetV1(sets, value.Opaque)
	}
	count, aggregate := 0, 0
	for _, values := range sets {
		setCount, setBytes, err := validateOpaqueObservationSetV1(values)
		if err != nil {
			return err
		}
		count += setCount
		aggregate += setBytes
	}
	if count > opaqueMaxObservations {
		return errors.New("opaque observation count exceeds the snapshot limit")
	}
	if aggregate > opaqueMaxAggregateBytes {
		return errors.New("opaque values exceed the snapshot aggregate byte limit")
	}
	return nil
}

func validateSnapshotSecretsV1(snapshot SnapshotV1) error {
	type alias SnapshotV1
	encoded, err := marshalJCSV1(alias(snapshot))
	if err != nil {
		return err
	}
	decoder, err := decodeJSONValueV1(encoded)
	if err != nil {
		return err
	}
	if containsSecretJSONV1(decoder) {
		return errors.New("snapshot contains forbidden secret material")
	}
	return nil
}

func containsSecretJSONV1(value any) bool {
	switch typed := value.(type) {
	case string:
		return containsPEMMaterialV1(typed)
	case []any:
		for _, item := range typed {
			if containsSecretJSONV1(item) {
				return true
			}
		}
	case map[string]any:
		for key, item := range typed {
			if _, forbidden := snapshotSecretKeysV1[strings.ToLower(key)]; forbidden {
				return true
			}
			if containsSecretJSONV1(item) {
				return true
			}
		}
	}
	return false
}

func canonicalSnapshotV1(source SnapshotV1) SnapshotV1 {
	result := source.Clone()
	result.Meta.CapturedAt = result.Meta.CapturedAt.UTC()
	result.Meta.DataTimestamp = result.Meta.DataTimestamp.UTC()
	if result.Status.Degradation != nil {
		result.Status.Degradation.Since = result.Status.Degradation.Since.UTC()
	}
	for index := range result.Pairing {
		result.Pairing[index].Since = result.Pairing[index].Since.UTC()
		result.Pairing[index].Opaque = canonicalOpaquePointerV1(result.Pairing[index].Opaque)
	}
	sort.Slice(result.Pairing, func(left, right int) bool {
		return result.Pairing[left].RemoteSKI < result.Pairing[right].RemoteSKI
	})
	for index := range result.Services {
		result.Services[index].Opaque = canonicalOpaquePointerV1(result.Services[index].Opaque)
	}
	sort.Slice(result.Services, func(left, right int) bool {
		return serviceOrderKeyV1(result.Services[left]) < serviceOrderKeyV1(result.Services[right])
	})
	for index := range result.Sessions {
		result.Sessions[index].Since = result.Sessions[index].Since.UTC()
		result.Sessions[index].Opaque = canonicalOpaquePointerV1(result.Sessions[index].Opaque)
	}
	sort.Slice(result.Sessions, func(left, right int) bool {
		return result.Sessions[left].ID < result.Sessions[right].ID
	})
	for index := range result.Devices {
		result.Devices[index].Opaque = canonicalOpaquePointerV1(result.Devices[index].Opaque)
	}
	sort.Slice(result.Devices, func(left, right int) bool {
		return deviceIdentityV1(result.Devices[left]) < deviceIdentityV1(result.Devices[right])
	})
	for index := range result.Entities {
		result.Entities[index].Opaque = canonicalOpaquePointerV1(result.Entities[index].Opaque)
	}
	sort.Slice(result.Entities, func(left, right int) bool {
		return entityIdentityV1(result.Entities[left]) < entityIdentityV1(result.Entities[right])
	})
	for index := range result.Features {
		result.Features[index].Opaque = canonicalOpaquePointerV1(result.Features[index].Opaque)
	}
	sort.Slice(result.Features, func(left, right int) bool {
		return featureIdentityV1(result.Features[left]) < featureIdentityV1(result.Features[right])
	})
	for index := range result.UseCases {
		if result.UseCases[index].Scenarios != nil {
			sort.Strings(*result.UseCases[index].Scenarios)
		}
		result.UseCases[index].Opaque = canonicalOpaquePointerV1(result.UseCases[index].Opaque)
	}
	sort.Slice(result.UseCases, func(left, right int) bool {
		return useCaseIdentityV1(result.UseCases[left]) < useCaseIdentityV1(result.UseCases[right])
	})
	result.Opaque = canonicalOpaqueObservationsV1(result.Opaque)
	normalizeEmptyCollectionsV1(&result)
	return result
}

func normalizeEmptyCollectionsV1(snapshot *SnapshotV1) {
	if snapshot.Pairing == nil {
		snapshot.Pairing = []PairingObservationV1{}
	}
	if snapshot.Services == nil {
		snapshot.Services = []ServiceV1{}
	}
	if snapshot.Sessions == nil {
		snapshot.Sessions = []SessionV1{}
	}
	if snapshot.Devices == nil {
		snapshot.Devices = []DeviceV1{}
	}
	if snapshot.Entities == nil {
		snapshot.Entities = []EntityV1{}
	}
	if snapshot.Features == nil {
		snapshot.Features = []FeatureV1{}
	}
	if snapshot.UseCases == nil {
		snapshot.UseCases = []UseCaseV1{}
	}
	if snapshot.Opaque == nil {
		snapshot.Opaque = []OpaqueObservationV1{}
	}
}

func serviceIdentityV1(value ServiceV1) string {
	return strings.Join([]string{
		value.SKI, optionalStringV1(value.SHIPID), value.Identifier, string(value.Kind),
	}, "\x00")
}

func serviceOrderKeyV1(value ServiceV1) string {
	return strings.Join([]string{
		serviceIdentityV1(value), fmt.Sprint(value.Visible), fmt.Sprint(value.Paired),
	}, "\x00")
}

func deviceIdentityV1(value DeviceV1) string {
	return strings.Join([]string{value.Address, value.SKI, optionalStringV1(value.SHIPID)}, "\x00")
}

func entityIdentityV1(value EntityV1) string {
	return value.DeviceAddress + "\x00" + value.EntityAddress
}

func featureIdentityV1(value FeatureV1) string {
	return value.DeviceAddress + "\x00" + value.EntityAddress + "\x00" + value.FeatureAddress
}

func useCaseIdentityV1(value UseCaseV1) string {
	return strings.Join([]string{
		value.ContextAddress, value.Name, value.Actor,
		optionalStringV1(value.Version), optionalStringV1(value.DocumentSubrevision),
	}, "\x00")
}

func validateSnapshotIDV1(id eebusraw.RedactedID, kinds ...eebusraw.IDKind) error {
	if err := id.Validate(); err != nil {
		return err
	}
	if !validSnapshotDigestV1(id.Digest) {
		return errors.New("identity digest is invalid")
	}
	for _, kind := range kinds {
		if id.Kind == kind {
			return nil
		}
	}
	return errors.New("identity kind is invalid")
}

func validateSnapshotTimestampV1(value time.Time, required bool) error {
	if value.IsZero() {
		if required {
			return errors.New("timestamp is required")
		}
		return nil
	}
	_, err := value.UTC().MarshalJSON()
	return err
}

func validateRequiredTextV1(value string, maximum int) error {
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > maximum {
		return errors.New("required text is invalid")
	}
	return nil
}

func validateOptionalTextV1(value *string, maximum int) error {
	if value == nil {
		return nil
	}
	if !utf8.ValidString(*value) || utf8.RuneCountInString(*value) > maximum {
		return errors.New("optional text is invalid")
	}
	return nil
}

func validateSecondaryDigestV1(value *string) error {
	if value != nil && !validSnapshotDigestV1(*value) {
		return errors.New("secondary digest must use lowercase sha256:<64 hex chars>")
	}
	return nil
}

func validSnapshotDigestV1(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") ||
		value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func cloneStringPointerV1(source *string) *string {
	if source == nil {
		return nil
	}
	value := *source
	return &value
}

func cloneBoolPointerV1(source *bool) *bool {
	if source == nil {
		return nil
	}
	value := *source
	return &value
}

func cloneStringsPointerV1(source *[]string) *[]string {
	if source == nil {
		return nil
	}
	value := append([]string(nil), (*source)...)
	if value == nil {
		value = []string{}
	}
	return &value
}

func cloneOpaquePointerV1(source *[]OpaqueObservationV1) *[]OpaqueObservationV1 {
	if source == nil {
		return nil
	}
	value := cloneOpaqueObservationsV1(*source)
	if value == nil {
		value = []OpaqueObservationV1{}
	}
	return &value
}

func canonicalOpaquePointerV1(source *[]OpaqueObservationV1) *[]OpaqueObservationV1 {
	if source == nil {
		return nil
	}
	value := canonicalOpaqueObservationsV1(*source)
	if value == nil {
		value = []OpaqueObservationV1{}
	}
	return &value
}

func appendOpaqueSetV1(sets [][]OpaqueObservationV1, source *[]OpaqueObservationV1) [][]OpaqueObservationV1 {
	if source != nil {
		sets = append(sets, *source)
	}
	return sets
}

func optionalStringV1(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
