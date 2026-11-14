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

// SnapshotContractV1 is the contract identifier carried in SnapshotMetaV1.Contract; validation
// rejects any other value, and RedactedSnapshotV1 carries the same identifier.
const SnapshotContractV1 = "helianthus.eebus.runtime.raw-snapshot.v1"

const snapshotMaskTierRawV1 eebusraw.MaskTier = "raw"

// ObservedRuntimeStateV1 is the lifecycle state of the eeBUS runtime as the snapshot observed
// it. The set is closed: validation rejects a value outside the constants below.
type ObservedRuntimeStateV1 string

// Runtime states. Only ObservedRuntimeStateV1Degraded carries a DegradationV1.
const (
	// ObservedRuntimeStateV1Unknown records that the runtime state could not be established.
	ObservedRuntimeStateV1Unknown ObservedRuntimeStateV1 = "unknown"
	// ObservedRuntimeStateV1Stopped records a runtime that is not running and not starting.
	ObservedRuntimeStateV1Stopped ObservedRuntimeStateV1 = "stopped"
	// ObservedRuntimeStateV1Starting records a runtime that is starting and not yet serving.
	ObservedRuntimeStateV1Starting ObservedRuntimeStateV1 = "starting"
	// ObservedRuntimeStateV1Ready records a runtime that is serving without a known degradation.
	ObservedRuntimeStateV1Ready ObservedRuntimeStateV1 = "ready"
	// ObservedRuntimeStateV1Degraded records a serving runtime with a known degradation; it
	// requires RuntimeObservationV1.Degradation.
	ObservedRuntimeStateV1Degraded ObservedRuntimeStateV1 = "degraded"
	// ObservedRuntimeStateV1Shutdown records a runtime that has shut down and will not restart.
	ObservedRuntimeStateV1Shutdown ObservedRuntimeStateV1 = "shutdown"
)

// DegradationReasonV1 names why a runtime is degraded. The set is closed: validation rejects a
// value outside the constants below.
type DegradationReasonV1 string

// Degradation reasons.
const (
	// DegradationReasonV1MissingDiscovery records that service discovery is unavailable.
	DegradationReasonV1MissingDiscovery DegradationReasonV1 = "missing-discovery"
	// DegradationReasonV1DeniedTrust records that a remote peer denied or withdrew trust.
	DegradationReasonV1DeniedTrust DegradationReasonV1 = "denied-trust"
	// DegradationReasonV1RemoteDisconnect records that a connected remote peer went away.
	DegradationReasonV1RemoteDisconnect DegradationReasonV1 = "remote-disconnect"
	// DegradationReasonV1CertificateUnavailable records that the local certificate could not be
	// loaded or used.
	DegradationReasonV1CertificateUnavailable DegradationReasonV1 = "certificate-unavailable"
	// DegradationReasonV1NoVisibleServices records that discovery runs but sees no service.
	DegradationReasonV1NoVisibleServices DegradationReasonV1 = "no-visible-services"
	// DegradationReasonV1NoData records that peers are reachable but supplied no native data.
	DegradationReasonV1NoData DegradationReasonV1 = "no-data"
)

// ServiceKindV1 tells the runtime's own advertised service apart from services of other peers;
// validation accepts only the two constants below.
type ServiceKindV1 string

// Service kinds.
const (
	// ServiceKindV1Local marks the service this runtime advertises itself.
	ServiceKindV1Local ServiceKindV1 = "local"
	// ServiceKindV1Remote marks a service advertised by another peer.
	ServiceKindV1Remote ServiceKindV1 = "remote"
)

// ObservedSessionStateV1 is the connection state of one session with a remote peer. The set is
// closed: validation rejects a value outside the constants below.
type ObservedSessionStateV1 string

// Session states.
const (
	// ObservedSessionStateV1Unknown records that the session state could not be established.
	ObservedSessionStateV1Unknown ObservedSessionStateV1 = "unknown"
	// ObservedSessionStateV1Connecting records a session whose connection is being set up.
	ObservedSessionStateV1Connecting ObservedSessionStateV1 = "connecting"
	// ObservedSessionStateV1Connected records an established session.
	ObservedSessionStateV1Connected ObservedSessionStateV1 = "connected"
	// ObservedSessionStateV1Disconnected records a session that has ended; it stays listed as
	// evidence until the runtime drops it.
	ObservedSessionStateV1Disconnected ObservedSessionStateV1 = "disconnected"
	// ObservedSessionStateV1Degraded records an established session that is not fully usable.
	ObservedSessionStateV1Degraded ObservedSessionStateV1 = "degraded"
)

// FeatureRoleV1 names the feature roles a consumer can rely on. FeatureV1.Role keeps the native
// role as a plain string that raw validation does not restrict to these values; the redacted
// view reduces every other role to FeatureRoleV1Unspecified.
type FeatureRoleV1 string

// Feature roles.
const (
	// FeatureRoleV1Unspecified is the empty role, used for any role other than client or server.
	FeatureRoleV1Unspecified FeatureRoleV1 = ""
	// FeatureRoleV1Client is the client role as the native topology reports it.
	FeatureRoleV1Client FeatureRoleV1 = "client"
	// FeatureRoleV1Server is the server role as the native topology reports it.
	FeatureRoleV1Server FeatureRoleV1 = "server"
)

// SnapshotV1 is the raw, unmasked view of what the runtime observed: runtime status, pairing,
// services, sessions and the native device, entity, feature and use-case topology, plus opaque
// native values kept as evidence. It carries raw identities such as SKIs and native addresses,
// so it is meant only for consumers with raw authority; RedactedSnapshotV1 is the masked view.
// Pairing, services, sessions, devices, entities, features and use cases are unique by their
// identity and sorted in canonical form; services sort by identity plus their Visible and Paired
// flags, while Opaque observations are canonicalized on their own. Entities,
// features and scoped use cases must be contained by listed devices and entities under the
// containment rule of NativeSnapshotV2. Limits: 256 services, 1024 devices, 4096 entities, 16384
// features and 4096 use cases. Build it with NewSnapshotV1. String, GoString and Format print a
// fixed placeholder, so formatting never reveals the content.
type SnapshotV1 struct {
	Meta     SnapshotMetaV1         `json:"meta"`
	Status   RuntimeObservationV1   `json:"status"`
	Pairing  []PairingObservationV1 `json:"pairing"`
	Services []ServiceV1            `json:"services"`
	Sessions []SessionV1            `json:"sessions"`
	Devices  []DeviceV1             `json:"devices"`
	Entities []EntityV1             `json:"entities"`
	Features []FeatureV1            `json:"features"`
	UseCases []UseCaseV1            `json:"use_cases"`
	Opaque   []OpaqueObservationV1  `json:"opaque"`
}

// SnapshotMetaV1 identifies a raw snapshot. Contract equals SnapshotContractV1, Runtime is a
// peer or local-SKI identifier, LocalSKI is the runtime's own SKI as 40 lowercase hexadecimal
// characters, and MaskTier must be raw. CapturedAt and DataTimestamp are required and normalized
// to UTC. DataHash is "sha256:" followed by the lowercase hexadecimal SHA-256 of the canonical
// JSON of the content; it excludes CapturedAt, so captures that differ only in capture time share
// the hash.
type SnapshotMetaV1 struct {
	Contract      string              `json:"contract"`
	Runtime       eebusraw.RedactedID `json:"runtime"`
	LocalSKI      string              `json:"local_ski"`
	MaskTier      eebusraw.MaskTier   `json:"mask_tier"`
	CapturedAt    time.Time           `json:"captured_at"`
	DataTimestamp time.Time           `json:"data_timestamp"`
	DataHash      string              `json:"data_hash,omitempty"`
}

// RuntimeObservationV1 is the runtime state at capture time. Degradation is set exactly when
// State is ObservedRuntimeStateV1Degraded; validation rejects either side without the other.
type RuntimeObservationV1 struct {
	State       ObservedRuntimeStateV1 `json:"state"`
	Degradation *DegradationV1         `json:"degradation,omitempty"`
}

// DegradationV1 explains a degraded runtime: Reason is one of the DegradationReasonV1 constants
// and Since, required and normalized to UTC, is when the degradation was first observed.
type DegradationV1 struct {
	Reason DegradationReasonV1 `json:"reason"`
	Since  time.Time           `json:"since"`
}

// PairingObservationV1 is the pairing state of one remote peer, keyed by RemoteSKI, which must be
// unique within the snapshot. State is an eebusraw.PairingState, Since is required and normalized
// to UTC, and Opaque keeps native values that the contract does not model.
type PairingObservationV1 struct {
	RemoteSKI string                 `json:"remote_ski"`
	State     eebusraw.PairingState  `json:"state"`
	Since     time.Time              `json:"since"`
	Opaque    *[]OpaqueObservationV1 `json:"opaque,omitempty"`
}

// ServiceV1 is one advertised service as discovery reported it, with its native descriptive
// fields copied without interpretation; nil means the field was not reported. Its identity is the
// SKI with SHIPID, Identifier and Kind, and it must be unique. SecondaryDigest, when set, is a
// lowercase "sha256:" digest.
type ServiceV1 struct {
	SKI             string                 `json:"ski"`
	SHIPID          *string                `json:"ship_id,omitempty"`
	Kind            ServiceKindV1          `json:"kind"`
	Visible         bool                   `json:"visible"`
	Paired          bool                   `json:"paired"`
	Name            *string                `json:"name,omitempty"`
	Identifier      *string                `json:"identifier,omitempty"`
	Brand           *string                `json:"brand,omitempty"`
	Type            *string                `json:"type,omitempty"`
	Model           *string                `json:"model,omitempty"`
	SecondaryDigest *string                `json:"secondary_digest,omitempty"`
	Opaque          *[]OpaqueObservationV1 `json:"opaque,omitempty"`
}

// SessionV1 is one session with a remote peer. ID is the runtime's session identifier and must be
// unique; RemoteSKI names the peer and Since, required and normalized to UTC, is when State was
// entered.
type SessionV1 struct {
	ID        string                 `json:"id"`
	RemoteSKI string                 `json:"remote_ski"`
	State     ObservedSessionStateV1 `json:"state"`
	Since     time.Time              `json:"since"`
	Opaque    *[]OpaqueObservationV1 `json:"opaque,omitempty"`
}

// DeviceV1 is one native device of the remote topology. Address is its native device address,
// Type its native device type, and Metadata its native descriptive pairs. Its identity is Address
// with SKI and SHIPID, and it must be unique.
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

// EntityV1 is one native entity, keyed by DeviceAddress and EntityAddress, which must be unique.
// ParentEntityAddress names the enclosing entity of the same device and is nil for a top-level
// entity; the device and any parent must be listed in the snapshot.
type EntityV1 struct {
	DeviceAddress       string                 `json:"device_address"`
	EntityAddress       string                 `json:"entity_address"`
	ParentEntityAddress *string                `json:"parent_entity_address,omitempty"`
	Type                string                 `json:"type"`
	Description         *string                `json:"description,omitempty"`
	SecondaryDigest     *string                `json:"secondary_digest,omitempty"`
	Opaque              *[]OpaqueObservationV1 `json:"opaque,omitempty"`
}

// FeatureV1 is one native feature, keyed by its device, entity and feature addresses, which must
// be unique; its entity must be listed in the snapshot. Role keeps the native role text unchanged;
// FeatureRoleV1 names the roles a consumer can rely on.
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

// UseCaseV1 is one use-case claim as a peer advertised it. Its identity is ContextAddress, Name,
// Actor, Version and DocumentSubrevision, and it must be unique. DeviceAddress, EntityAddress and
// FeatureAddress scope the claim; when any is set the scope must resolve to listed topology, and
// the context address never places it. Scenarios must be free of duplicates, at most 128;
// canonicalization sorts them.
type UseCaseV1 struct {
	ContextAddress      string                 `json:"context_address"`
	DeviceAddress       *string                `json:"device_address,omitempty"`
	EntityAddress       *string                `json:"entity_address,omitempty"`
	FeatureAddress      *string                `json:"feature_address,omitempty"`
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

// NewSnapshotV1 returns draft in canonical form with Meta.DataHash computed. It normalizes times
// to UTC and LocalSKI to lowercase, sorts every collection by identity and replaces nil
// collections with empty ones; draft is not modified. A DataHash already set in draft must match
// the computed one. It fails, and never returns a partial result, when the draft breaks any rule
// of SnapshotV1 or contains secret material.
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

// Validate reports the first rule of SnapshotV1 that the snapshot breaks. Unlike NewSnapshotV1
// it requires Meta.DataHash and checks it against the content. Field names in key position that
// name secrets and embedded PEM material are rejected anywhere in the snapshot.
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
	if !validSnapshotSKIV1(snapshot.Meta.LocalSKI) {
		return errors.New("local_ski must contain 40 lowercase hexadecimal characters")
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
	if err := validateSpineContainment(rawContainmentGraph(snapshot)); err != nil {
		return err
	}
	if err := validateSnapshotOpaqueV1(snapshot); err != nil {
		return err
	}
	if err := validateSnapshotSecretsV1(snapshot); err != nil {
		return err
	}
	if checkHash && snapshot.Meta.DataHash == "" {
		return errors.New("data_hash is required")
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

// Clone returns a deep copy that shares no slice, map or pointer with the receiver, so the
// caller may modify either without affecting the other. It does not validate or canonicalize.
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
		result.Services[index].Name = cloneStringPointerV1(value.Name)
		result.Services[index].Identifier = cloneStringPointerV1(value.Identifier)
		result.Services[index].Brand = cloneStringPointerV1(value.Brand)
		result.Services[index].Type = cloneStringPointerV1(value.Type)
		result.Services[index].Model = cloneStringPointerV1(value.Model)
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
		result.Entities[index].ParentEntityAddress = cloneStringPointerV1(value.ParentEntityAddress)
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
		result.UseCases[index].DeviceAddress = cloneStringPointerV1(value.DeviceAddress)
		result.UseCases[index].EntityAddress = cloneStringPointerV1(value.EntityAddress)
		result.UseCases[index].FeatureAddress = cloneStringPointerV1(value.FeatureAddress)
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

// ComputeDataHash validates the snapshot without requiring Meta.DataHash and returns the content
// hash in the form described by SnapshotMetaV1. The hash is computed over the canonical form, so
// collection order and time zones do not change it.
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
		LocalSKI      string                 `json:"local_ski"`
		MaskTier      eebusraw.MaskTier      `json:"mask_tier"`
		DataTimestamp time.Time              `json:"data_timestamp"`
		Status        RuntimeObservationV1   `json:"status"`
		Pairing       []PairingObservationV1 `json:"pairing"`
		Services      []ServiceV1            `json:"services"`
		Sessions      []SessionV1            `json:"sessions"`
		Devices       []DeviceV1             `json:"devices"`
		Entities      []EntityV1             `json:"entities"`
		Features      []FeatureV1            `json:"features"`
		UseCases      []UseCaseV1            `json:"use_cases"`
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

// MarshalJSON validates the snapshot as Validate does and encodes its canonical form as
// canonical JSON; an invalid snapshot is never encoded.
func (snapshot SnapshotV1) MarshalJSON() ([]byte, error) {
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	canonical := canonicalSnapshotV1(snapshot)
	type wire SnapshotV1
	return marshalJCSV1(wire(canonical))
}

// String returns a fixed placeholder and never the raw content.
func (SnapshotV1) String() string {
	return "snapshot_v1:[redacted]"
}

// GoString returns the String placeholder, so %#v never reveals the raw content.
func (snapshot SnapshotV1) GoString() string {
	return snapshot.String()
}

// Format writes the String placeholder for every verb and flag, so no fmt verb reveals the raw
// content.
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
		if err := validateRequiredTextV1(value.SKI, 4096); err != nil {
			return errors.New("service contains a missing or invalid raw identity")
		}
		for _, optional := range []*string{
			value.SHIPID, value.Name, value.Identifier, value.Brand, value.Type, value.Model,
		} {
			if err := validateOptionalTextV1(optional, 4096); err != nil {
				return errors.New("service contains an invalid optional raw field")
			}
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
		if !validOptionalAddress(value.ParentEntityAddress) {
			return errors.New("entity parent address is invalid")
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
		if !validOptionalAddress(value.DeviceAddress) || !validOptionalAddress(value.EntityAddress) ||
			!validOptionalAddress(value.FeatureAddress) {
			return errors.New("use-case scope address is invalid")
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

func validOptionalAddress(value *string) bool {
	return value == nil || validateRequiredTextV1(*value, 512) == nil
}

func rawContainmentGraph(snapshot SnapshotV1) spineContainment {
	var graph spineContainment
	for _, device := range snapshot.Devices {
		graph.devices = append(graph.devices, device.Address)
	}
	for _, entity := range snapshot.Entities {
		graph.entities = append(graph.entities, spineContainedEntity{entity.DeviceAddress, entity.EntityAddress, entity.ParentEntityAddress})
	}
	for _, feature := range snapshot.Features {
		graph.features = append(graph.features, spineContainedFeature{feature.DeviceAddress, feature.EntityAddress, feature.FeatureAddress})
	}
	for _, claim := range snapshot.UseCases {
		if claim.DeviceAddress == nil && claim.EntityAddress == nil && claim.FeatureAddress == nil {
			continue
		}
		graph.claims = append(graph.claims, spineContainedClaim{
			device: optionalStringV1(claim.DeviceAddress), entity: claim.EntityAddress, feature: claim.FeatureAddress,
			identity: spineClaimIdentity(claim.Name, claim.Actor, claim.Version, claim.DocumentSubrevision),
		})
	}
	return graph
}

func canonicalSnapshotV1(source SnapshotV1) SnapshotV1 {
	result := source.Clone()
	result.Meta.LocalSKI = strings.ToLower(strings.TrimSpace(result.Meta.LocalSKI))
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

func validSnapshotSKIV1(value string) bool {
	if len(value) != 40 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
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
		value.SKI, optionalStringV1(value.SHIPID), optionalStringV1(value.Identifier), string(value.Kind),
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

func optionalStringV1(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
