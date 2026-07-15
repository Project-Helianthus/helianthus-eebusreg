package eebusruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusevidence"
	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

const SnapshotContractV1 = "helianthus.eebus.runtime.raw-snapshot.v1"

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
	Meta     SnapshotMetaV1           `json:"meta"`
	Status   RuntimeObservationV1     `json:"status"`
	Pairing  []PairingObservationV1   `json:"pairing,omitempty"`
	Services []ServiceV1              `json:"services,omitempty"`
	Sessions []SessionV1              `json:"sessions,omitempty"`
	Topology TopologyV1               `json:"topology"`
	Raw      []eebusevidence.ObjectV1 `json:"raw,omitempty"`
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
	Remote  eebusraw.RedactedID      `json:"remote"`
	State   eebusraw.PairingState    `json:"state"`
	Since   time.Time                `json:"since,omitempty"`
	Raw     []eebusevidence.ObjectV1 `json:"raw,omitempty"`
	Unknown []eebusraw.UnknownField  `json:"unknown,omitempty"`
}

type ServiceV1 struct {
	ID      eebusraw.RedactedID      `json:"id"`
	Kind    ServiceKindV1            `json:"kind"`
	Visible bool                     `json:"visible"`
	Paired  bool                     `json:"paired"`
	Raw     []eebusevidence.ObjectV1 `json:"raw,omitempty"`
	Unknown []eebusraw.UnknownField  `json:"unknown,omitempty"`
}

type SessionV1 struct {
	ID      eebusraw.RedactedID      `json:"id"`
	Remote  eebusraw.RedactedID      `json:"remote"`
	State   ObservedSessionStateV1   `json:"state"`
	Since   time.Time                `json:"since,omitempty"`
	Raw     []eebusevidence.ObjectV1 `json:"raw,omitempty"`
	Unknown []eebusraw.UnknownField  `json:"unknown,omitempty"`
}

type TopologyV1 struct {
	Devices []DeviceV1 `json:"devices,omitempty"`
}

type DeviceV1 struct {
	ID            eebusraw.RedactedID      `json:"id"`
	Entities      []EntityV1               `json:"entities,omitempty"`
	UseCaseClaims []UseCaseClaimV1         `json:"usecase_claims,omitempty"`
	Raw           []eebusevidence.ObjectV1 `json:"raw,omitempty"`
	Unknown       []eebusraw.UnknownField  `json:"unknown,omitempty"`
}

type EntityV1 struct {
	ID       eebusraw.RedactedID      `json:"id"`
	Features []FeatureV1              `json:"features,omitempty"`
	Raw      []eebusevidence.ObjectV1 `json:"raw,omitempty"`
	Unknown  []eebusraw.UnknownField  `json:"unknown,omitempty"`
}

type FeatureV1 struct {
	ID      eebusraw.RedactedID      `json:"id"`
	Role    FeatureRoleV1            `json:"role"`
	Raw     []eebusevidence.ObjectV1 `json:"raw,omitempty"`
	Unknown []eebusraw.UnknownField  `json:"unknown,omitempty"`
}

type UseCaseClaimV1 struct {
	ID      eebusraw.RedactedID      `json:"id"`
	Raw     []eebusevidence.ObjectV1 `json:"raw,omitempty"`
	Unknown []eebusraw.UnknownField  `json:"unknown,omitempty"`
}

func NewSnapshotV1(draft SnapshotV1) (SnapshotV1, error) {
	snapshot := canonicalSnapshotV1(draft)
	computed, err := snapshot.ComputeDataHash()
	if err != nil {
		return SnapshotV1{}, err
	}
	if snapshot.Meta.DataHash != "" && snapshot.Meta.DataHash != computed {
		return SnapshotV1{}, errors.New("data_hash does not match snapshot content")
	}
	snapshot.Meta.DataHash = computed
	if err := snapshot.Validate(); err != nil {
		return SnapshotV1{}, err
	}
	return snapshot, nil
}

func (s SnapshotV1) Validate() error {
	return s.validate(true)
}

func (s SnapshotV1) validate(checkHash bool) error {
	if s.Meta.Contract != SnapshotContractV1 {
		return fmt.Errorf("contract must be %q", SnapshotContractV1)
	}
	if err := validateSnapshotIDV1(s.Meta.Runtime, eebusraw.IDKindPeer, eebusraw.IDKindLocalSKI); err != nil {
		return fmt.Errorf("runtime: %w", err)
	}
	if err := validateSnapshotIDV1(s.Meta.LocalSKI, eebusraw.IDKindLocalSKI); err != nil {
		return fmt.Errorf("local_ski: %w", err)
	}
	if s.Meta.MaskTier != eebusraw.MaskTierRedacted {
		return errors.New("mask_tier must be redacted")
	}
	if err := validateSnapshotTimestampV1(s.Meta.CapturedAt, true); err != nil {
		return fmt.Errorf("captured_at: %w", err)
	}
	if err := validateSnapshotTimestampV1(s.Meta.DataTimestamp, true); err != nil {
		return fmt.Errorf("data_timestamp: %w", err)
	}
	if err := validateRuntimeObservationV1(s.Status); err != nil {
		return fmt.Errorf("status: %w", err)
	}
	if err := validatePairingV1(s.Pairing); err != nil {
		return err
	}
	if err := validateServicesV1(s.Services); err != nil {
		return err
	}
	if err := validateSessionsV1(s.Sessions); err != nil {
		return err
	}
	if err := validateDevicesV1(s.Topology.Devices); err != nil {
		return err
	}
	if err := validateSnapshotRawV1(s.Raw); err != nil {
		return fmt.Errorf("raw: %w", err)
	}
	if s.Meta.DataHash != "" && !validSnapshotDigestV1(s.Meta.DataHash) {
		return errors.New("data_hash must use lowercase sha256:<64 hex chars>")
	}
	if checkHash && s.Meta.DataHash != "" {
		expected, err := s.computeDataHash()
		if err != nil {
			return err
		}
		if s.Meta.DataHash != expected {
			return errors.New("data_hash does not match snapshot content")
		}
	}
	return nil
}

func (s SnapshotV1) Clone() SnapshotV1 {
	clone := s
	if s.Status.Degradation != nil {
		degradation := *s.Status.Degradation
		clone.Status.Degradation = &degradation
	}
	clone.Pairing = copyPairingV1(s.Pairing)
	clone.Services = copyServicesV1(s.Services)
	clone.Sessions = copySessionsV1(s.Sessions)
	clone.Topology.Devices = copyDevicesV1(s.Topology.Devices)
	clone.Raw = copySnapshotRawV1(s.Raw)
	return clone
}

func (s SnapshotV1) ComputeDataHash() (string, error) {
	if err := s.validate(false); err != nil {
		return "", err
	}
	return s.computeDataHash()
}

func (s SnapshotV1) computeDataHash() (string, error) {
	canonical := canonicalSnapshotV1(s)
	payload := struct {
		Contract      string                   `json:"contract"`
		Runtime       eebusraw.RedactedID      `json:"runtime"`
		LocalSKI      eebusraw.RedactedID      `json:"local_ski"`
		MaskTier      eebusraw.MaskTier        `json:"mask_tier"`
		DataTimestamp time.Time                `json:"data_timestamp"`
		Status        RuntimeObservationV1     `json:"status"`
		Pairing       []PairingObservationV1   `json:"pairing,omitempty"`
		Services      []ServiceV1              `json:"services,omitempty"`
		Sessions      []SessionV1              `json:"sessions,omitempty"`
		Topology      TopologyV1               `json:"topology"`
		Raw           []eebusevidence.ObjectV1 `json:"raw,omitempty"`
	}{
		Contract:      canonical.Meta.Contract,
		Runtime:       canonical.Meta.Runtime,
		LocalSKI:      canonical.Meta.LocalSKI,
		MaskTier:      canonical.Meta.MaskTier,
		DataTimestamp: canonical.Meta.DataTimestamp,
		Status:        canonical.Status,
		Pairing:       canonical.Pairing,
		Services:      canonical.Services,
		Sessions:      canonical.Sessions,
		Topology:      canonical.Topology,
		Raw:           canonical.Raw,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (s SnapshotV1) MarshalJSON() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	canonical := canonicalSnapshotV1(s)
	type snapshotV1JSON SnapshotV1
	return json.Marshal(snapshotV1JSON(canonical))
}

func (s SnapshotV1) String() string {
	return "snapshot_v1:[redacted]"
}

func (s SnapshotV1) GoString() string {
	return s.String()
}

func (s SnapshotV1) Format(state fmt.State, verb rune) {
	_, _ = io.WriteString(state, s.String())
}

func validateRuntimeObservationV1(observation RuntimeObservationV1) error {
	switch observation.State {
	case ObservedRuntimeStateV1Unknown, ObservedRuntimeStateV1Stopped, ObservedRuntimeStateV1Starting,
		ObservedRuntimeStateV1Ready, ObservedRuntimeStateV1Degraded, ObservedRuntimeStateV1Shutdown:
	default:
		return errors.New("unsupported runtime state")
	}
	if observation.State == ObservedRuntimeStateV1Degraded {
		if observation.Degradation == nil {
			return errors.New("degraded state requires degradation details")
		}
	} else if observation.Degradation != nil {
		return errors.New("degradation details require degraded state")
	}
	if observation.Degradation == nil {
		return nil
	}
	switch observation.Degradation.Reason {
	case DegradationReasonV1MissingDiscovery, DegradationReasonV1DeniedTrust,
		DegradationReasonV1RemoteDisconnect, DegradationReasonV1CertificateUnavailable,
		DegradationReasonV1NoVisibleServices, DegradationReasonV1NoData:
	default:
		return errors.New("unsupported degradation reason")
	}
	if err := validateSnapshotTimestampV1(observation.Degradation.Since, true); err != nil {
		return fmt.Errorf("degradation since: %w", err)
	}
	return nil
}

func validatePairingV1(values []PairingObservationV1) error {
	seen := make(map[string]int, len(values))
	for i, value := range values {
		if err := validateSnapshotIDV1(value.Remote, eebusraw.IDKindRemoteSKI, eebusraw.IDKindPeer); err != nil {
			return fmt.Errorf("pairing %d remote: %w", i, err)
		}
		switch value.State {
		case eebusraw.PairingStateUnknown, eebusraw.PairingStateUnpaired, eebusraw.PairingStatePaired, eebusraw.PairingStateDenied:
		default:
			return fmt.Errorf("pairing %d state: unsupported pairing state", i)
		}
		if err := validateSnapshotTimestampV1(value.Since, false); err != nil {
			return fmt.Errorf("pairing %d since: %w", i, err)
		}
		if err := validateSnapshotRawV1(value.Raw); err != nil {
			return fmt.Errorf("pairing %d raw: %w", i, err)
		}
		if err := validateSnapshotUnknownV1(value.Unknown); err != nil {
			return fmt.Errorf("pairing %d unknown: %w", i, err)
		}
		if err := recordSnapshotIdentityV1(seen, value.Remote, i); err != nil {
			return fmt.Errorf("pairing %d: %w", i, err)
		}
	}
	return nil
}

func validateServicesV1(values []ServiceV1) error {
	seen := make(map[string]int, len(values))
	for i, value := range values {
		if err := validateSnapshotIDV1(value.ID, eebusraw.IDKindPeer); err != nil {
			return fmt.Errorf("service %d id: %w", i, err)
		}
		if value.Kind != ServiceKindV1Local && value.Kind != ServiceKindV1Remote {
			return fmt.Errorf("service %d: unsupported kind", i)
		}
		if err := validateSnapshotRawV1(value.Raw); err != nil {
			return fmt.Errorf("service %d raw: %w", i, err)
		}
		if err := validateSnapshotUnknownV1(value.Unknown); err != nil {
			return fmt.Errorf("service %d unknown: %w", i, err)
		}
		if err := recordSnapshotIdentityV1(seen, value.ID, i); err != nil {
			return fmt.Errorf("service %d: %w", i, err)
		}
	}
	return nil
}

func validateSessionsV1(values []SessionV1) error {
	seen := make(map[string]int, len(values))
	for i, value := range values {
		if err := validateSnapshotIDV1(value.ID, eebusraw.IDKindSession); err != nil {
			return fmt.Errorf("session %d id: %w", i, err)
		}
		if err := validateSnapshotIDV1(value.Remote, eebusraw.IDKindRemoteSKI, eebusraw.IDKindPeer); err != nil {
			return fmt.Errorf("session %d remote: %w", i, err)
		}
		switch value.State {
		case ObservedSessionStateV1Unknown, ObservedSessionStateV1Connecting,
			ObservedSessionStateV1Connected, ObservedSessionStateV1Disconnected, ObservedSessionStateV1Degraded:
		default:
			return fmt.Errorf("session %d: unsupported state", i)
		}
		if err := validateSnapshotTimestampV1(value.Since, false); err != nil {
			return fmt.Errorf("session %d since: %w", i, err)
		}
		if err := validateSnapshotRawV1(value.Raw); err != nil {
			return fmt.Errorf("session %d raw: %w", i, err)
		}
		if err := validateSnapshotUnknownV1(value.Unknown); err != nil {
			return fmt.Errorf("session %d unknown: %w", i, err)
		}
		if err := recordSnapshotIdentityV1(seen, value.ID, i); err != nil {
			return fmt.Errorf("session %d: %w", i, err)
		}
	}
	return nil
}

func validateDevicesV1(values []DeviceV1) error {
	seen := make(map[string]int, len(values))
	for i, value := range values {
		if err := validateSnapshotIDV1(value.ID, eebusraw.IDKindPeer); err != nil {
			return fmt.Errorf("device %d id: %w", i, err)
		}
		if err := validateEntitiesV1(value.Entities); err != nil {
			return fmt.Errorf("device %d: %w", i, err)
		}
		if err := validateUseCaseClaimsV1(value.UseCaseClaims); err != nil {
			return fmt.Errorf("device %d: %w", i, err)
		}
		if err := validateSnapshotRawV1(value.Raw); err != nil {
			return fmt.Errorf("device %d raw: %w", i, err)
		}
		if err := validateSnapshotUnknownV1(value.Unknown); err != nil {
			return fmt.Errorf("device %d unknown: %w", i, err)
		}
		if err := recordSnapshotIdentityV1(seen, value.ID, i); err != nil {
			return fmt.Errorf("device %d: %w", i, err)
		}
	}
	return nil
}

func validateEntitiesV1(values []EntityV1) error {
	seen := make(map[string]int, len(values))
	for i, value := range values {
		if err := validateSnapshotIDV1(value.ID, eebusraw.IDKindPeer); err != nil {
			return fmt.Errorf("entity %d id: %w", i, err)
		}
		if err := validateFeaturesV1(value.Features); err != nil {
			return fmt.Errorf("entity %d: %w", i, err)
		}
		if err := validateSnapshotRawV1(value.Raw); err != nil {
			return fmt.Errorf("entity %d raw: %w", i, err)
		}
		if err := validateSnapshotUnknownV1(value.Unknown); err != nil {
			return fmt.Errorf("entity %d unknown: %w", i, err)
		}
		if err := recordSnapshotIdentityV1(seen, value.ID, i); err != nil {
			return fmt.Errorf("entity %d: %w", i, err)
		}
	}
	return nil
}

func validateFeaturesV1(values []FeatureV1) error {
	seen := make(map[string]int, len(values))
	for i, value := range values {
		if err := validateSnapshotIDV1(value.ID, eebusraw.IDKindPeer); err != nil {
			return fmt.Errorf("feature %d id: %w", i, err)
		}
		if value.Role != FeatureRoleV1Unspecified && value.Role != FeatureRoleV1Client && value.Role != FeatureRoleV1Server {
			return fmt.Errorf("feature %d: unsupported role", i)
		}
		if err := validateSnapshotRawV1(value.Raw); err != nil {
			return fmt.Errorf("feature %d raw: %w", i, err)
		}
		if err := validateSnapshotUnknownV1(value.Unknown); err != nil {
			return fmt.Errorf("feature %d unknown: %w", i, err)
		}
		if err := recordSnapshotIdentityV1(seen, value.ID, i); err != nil {
			return fmt.Errorf("feature %d: %w", i, err)
		}
	}
	return nil
}

func validateUseCaseClaimsV1(values []UseCaseClaimV1) error {
	seen := make(map[string]int, len(values))
	for i, value := range values {
		if err := validateSnapshotIDV1(value.ID, eebusraw.IDKindPeer); err != nil {
			return fmt.Errorf("usecase claim %d id: %w", i, err)
		}
		if err := validateSnapshotRawV1(value.Raw); err != nil {
			return fmt.Errorf("usecase claim %d raw: %w", i, err)
		}
		if err := validateSnapshotUnknownV1(value.Unknown); err != nil {
			return fmt.Errorf("usecase claim %d unknown: %w", i, err)
		}
		if err := recordSnapshotIdentityV1(seen, value.ID, i); err != nil {
			return fmt.Errorf("usecase claim %d: %w", i, err)
		}
	}
	return nil
}

func validateSnapshotIDV1(id eebusraw.RedactedID, kinds ...eebusraw.IDKind) error {
	if err := id.Validate(); err != nil {
		return err
	}
	for _, kind := range kinds {
		if id.Kind == kind {
			if !validSnapshotDigestV1(id.Digest) {
				return errors.New("id digest must use lowercase sha256:<64 hex chars>")
			}
			return nil
		}
	}
	if len(kinds) == 1 {
		return fmt.Errorf("id kind must be %q", kinds[0])
	}
	values := make([]string, len(kinds))
	for i, kind := range kinds {
		values[i] = strconv.Quote(string(kind))
	}
	return fmt.Errorf("id kind must be one of %s", strings.Join(values, ", "))
}

func validateSnapshotRawV1(objects []eebusevidence.ObjectV1) error {
	seen := make(map[string]int, len(objects))
	for i, object := range objects {
		if err := object.Validate(); err != nil {
			return fmt.Errorf("object %d: %w", i, err)
		}
		if err := validateSnapshotUnknownV1(object.Unknown); err != nil {
			return fmt.Errorf("object %d unknown: %w", i, err)
		}
		key := snapshotRawKeyV1(object)
		if prior, ok := seen[key]; ok {
			return fmt.Errorf("object %d duplicates raw evidence object from item %d", i, prior)
		}
		seen[key] = i
	}
	return nil
}

func validateSnapshotUnknownV1(fields []eebusraw.UnknownField) error {
	seen := make(map[string]int, len(fields))
	for i, field := range fields {
		if err := field.Validate(); err != nil {
			return fmt.Errorf("field %d: %w", i, err)
		}
		if field.Value.Digest != "" && !validSnapshotDigestV1(field.Value.Digest) {
			return fmt.Errorf("field %d digest must use lowercase sha256:<64 hex chars>", i)
		}
		key := snapshotUnknownFieldsKeyV1([]eebusraw.UnknownField{field})
		if prior, ok := seen[key]; ok {
			return fmt.Errorf("field %d duplicates unknown field from item %d", i, prior)
		}
		seen[key] = i
	}
	return nil
}

func validateSnapshotTimestampV1(value time.Time, required bool) error {
	if value.IsZero() {
		if required {
			return errors.New("timestamp is required")
		}
		return nil
	}
	if _, err := value.UTC().MarshalJSON(); err != nil {
		return errors.New("timestamp must marshal as RFC3339 JSON")
	}
	return nil
}

func validSnapshotDigestV1(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func recordSnapshotIdentityV1(seen map[string]int, id eebusraw.RedactedID, index int) error {
	key := snapshotIdentityKeyV1(id)
	if prior, ok := seen[key]; ok {
		return fmt.Errorf("duplicates identity key from item %d", prior)
	}
	seen[key] = index
	return nil
}

func snapshotIdentityKeyV1(id eebusraw.RedactedID) string {
	return string(id.Kind) + "\x00" + id.Digest
}

func canonicalSnapshotV1(source SnapshotV1) SnapshotV1 {
	result := source.Clone()
	result.Meta.CapturedAt = result.Meta.CapturedAt.UTC()
	result.Meta.DataTimestamp = result.Meta.DataTimestamp.UTC()
	if result.Status.Degradation != nil {
		result.Status.Degradation.Since = result.Status.Degradation.Since.UTC()
	}
	for i := range result.Pairing {
		result.Pairing[i].Since = result.Pairing[i].Since.UTC()
		result.Pairing[i].Raw = canonicalSnapshotRawV1(result.Pairing[i].Raw)
		result.Pairing[i].Unknown = canonicalSnapshotUnknownV1(result.Pairing[i].Unknown)
	}
	sort.Slice(result.Pairing, func(i, j int) bool {
		return snapshotIdentityKeyV1(result.Pairing[i].Remote) < snapshotIdentityKeyV1(result.Pairing[j].Remote)
	})
	for i := range result.Services {
		result.Services[i].Raw = canonicalSnapshotRawV1(result.Services[i].Raw)
		result.Services[i].Unknown = canonicalSnapshotUnknownV1(result.Services[i].Unknown)
	}
	sort.Slice(result.Services, func(i, j int) bool {
		return snapshotIdentityKeyV1(result.Services[i].ID) < snapshotIdentityKeyV1(result.Services[j].ID)
	})
	for i := range result.Sessions {
		result.Sessions[i].Since = result.Sessions[i].Since.UTC()
		result.Sessions[i].Raw = canonicalSnapshotRawV1(result.Sessions[i].Raw)
		result.Sessions[i].Unknown = canonicalSnapshotUnknownV1(result.Sessions[i].Unknown)
	}
	sort.Slice(result.Sessions, func(i, j int) bool {
		return snapshotIdentityKeyV1(result.Sessions[i].ID) < snapshotIdentityKeyV1(result.Sessions[j].ID)
	})
	for i := range result.Topology.Devices {
		device := &result.Topology.Devices[i]
		for j := range device.Entities {
			entity := &device.Entities[j]
			for k := range entity.Features {
				entity.Features[k].Raw = canonicalSnapshotRawV1(entity.Features[k].Raw)
				entity.Features[k].Unknown = canonicalSnapshotUnknownV1(entity.Features[k].Unknown)
			}
			sort.Slice(entity.Features, func(a, b int) bool {
				return snapshotIdentityKeyV1(entity.Features[a].ID) < snapshotIdentityKeyV1(entity.Features[b].ID)
			})
			entity.Raw = canonicalSnapshotRawV1(entity.Raw)
			entity.Unknown = canonicalSnapshotUnknownV1(entity.Unknown)
		}
		sort.Slice(device.Entities, func(a, b int) bool {
			return snapshotIdentityKeyV1(device.Entities[a].ID) < snapshotIdentityKeyV1(device.Entities[b].ID)
		})
		for j := range device.UseCaseClaims {
			device.UseCaseClaims[j].Raw = canonicalSnapshotRawV1(device.UseCaseClaims[j].Raw)
			device.UseCaseClaims[j].Unknown = canonicalSnapshotUnknownV1(device.UseCaseClaims[j].Unknown)
		}
		sort.Slice(device.UseCaseClaims, func(a, b int) bool {
			return snapshotIdentityKeyV1(device.UseCaseClaims[a].ID) < snapshotIdentityKeyV1(device.UseCaseClaims[b].ID)
		})
		device.Raw = canonicalSnapshotRawV1(device.Raw)
		device.Unknown = canonicalSnapshotUnknownV1(device.Unknown)
	}
	sort.Slice(result.Topology.Devices, func(i, j int) bool {
		return snapshotIdentityKeyV1(result.Topology.Devices[i].ID) < snapshotIdentityKeyV1(result.Topology.Devices[j].ID)
	})
	result.Raw = canonicalSnapshotRawV1(result.Raw)
	return result
}

func canonicalSnapshotRawV1(source []eebusevidence.ObjectV1) []eebusevidence.ObjectV1 {
	result := copySnapshotRawV1(source)
	for i := range result {
		result[i].DataTimestamp = result[i].DataTimestamp.UTC()
		result[i].Unknown = canonicalSnapshotUnknownV1(result[i].Unknown)
	}
	sort.SliceStable(result, func(i, j int) bool { return snapshotRawLessV1(result[i], result[j]) })
	return result
}

func snapshotRawLessV1(left, right eebusevidence.ObjectV1) bool {
	if left.Kind != right.Kind {
		return left.Kind < right.Kind
	}
	if left.Digest != right.Digest {
		return left.Digest < right.Digest
	}
	leftTime := left.DataTimestamp.UTC().Format(time.RFC3339Nano)
	rightTime := right.DataTimestamp.UTC().Format(time.RFC3339Nano)
	if leftTime != rightTime {
		return leftTime < rightTime
	}
	if left.Size != right.Size {
		return left.Size < right.Size
	}
	return snapshotUnknownFieldsKeyV1(left.Unknown) < snapshotUnknownFieldsKeyV1(right.Unknown)
}

func snapshotRawKeyV1(object eebusevidence.ObjectV1) string {
	var b strings.Builder
	b.WriteString(string(object.Kind))
	b.WriteByte(0)
	b.WriteString(object.Digest)
	b.WriteByte(0)
	b.WriteString(object.DataTimestamp.UTC().Format(time.RFC3339Nano))
	b.WriteByte(0)
	b.WriteString(strconv.Itoa(object.Size))
	b.WriteByte(0)
	b.WriteString(snapshotUnknownFieldsKeyV1(object.Unknown))
	return b.String()
}

func canonicalSnapshotUnknownV1(source []eebusraw.UnknownField) []eebusraw.UnknownField {
	result := append([]eebusraw.UnknownField(nil), source...)
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i], result[j]
		if left.Path != right.Path {
			return left.Path < right.Path
		}
		if left.Value.Digest != right.Value.Digest {
			return left.Value.Digest < right.Value.Digest
		}
		if left.Value.Masked != right.Value.Masked {
			return left.Value.Masked < right.Value.Masked
		}
		return left.Value.Size < right.Value.Size
	})
	return result
}

func snapshotUnknownFieldsKeyV1(source []eebusraw.UnknownField) string {
	fields := canonicalSnapshotUnknownV1(source)
	var b strings.Builder
	b.WriteByte('[')
	for i, field := range fields {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"path":`)
		snapshotWriteJSONStringV1(&b, field.Path.String())
		b.WriteString(`,"value":{`)
		if field.Value.Digest != "" {
			b.WriteString(`"digest":`)
			snapshotWriteJSONStringV1(&b, field.Value.Digest)
			b.WriteByte(',')
		}
		b.WriteString(`"masked":`)
		snapshotWriteJSONStringV1(&b, field.Value.Masked)
		if field.Value.Size != 0 {
			b.WriteString(`,"size":`)
			b.WriteString(strconv.Itoa(field.Value.Size))
		}
		b.WriteString(`}}`)
	}
	b.WriteByte(']')
	return b.String()
}

func snapshotWriteJSONStringV1(b *strings.Builder, value string) {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	b.Write(encoded)
}

func copySnapshotRawV1(source []eebusevidence.ObjectV1) []eebusevidence.ObjectV1 {
	if len(source) == 0 {
		return nil
	}
	result := make([]eebusevidence.ObjectV1, len(source))
	for i, object := range source {
		result[i] = object
		result[i].Unknown = append([]eebusraw.UnknownField(nil), object.Unknown...)
	}
	return result
}

func copyPairingV1(source []PairingObservationV1) []PairingObservationV1 {
	if len(source) == 0 {
		return nil
	}
	result := make([]PairingObservationV1, len(source))
	for i, value := range source {
		result[i] = value
		result[i].Raw = copySnapshotRawV1(value.Raw)
		result[i].Unknown = append([]eebusraw.UnknownField(nil), value.Unknown...)
	}
	return result
}

func copyServicesV1(source []ServiceV1) []ServiceV1 {
	if len(source) == 0 {
		return nil
	}
	result := make([]ServiceV1, len(source))
	for i, value := range source {
		result[i] = value
		result[i].Raw = copySnapshotRawV1(value.Raw)
		result[i].Unknown = append([]eebusraw.UnknownField(nil), value.Unknown...)
	}
	return result
}

func copySessionsV1(source []SessionV1) []SessionV1 {
	if len(source) == 0 {
		return nil
	}
	result := make([]SessionV1, len(source))
	for i, value := range source {
		result[i] = value
		result[i].Raw = copySnapshotRawV1(value.Raw)
		result[i].Unknown = append([]eebusraw.UnknownField(nil), value.Unknown...)
	}
	return result
}

func copyDevicesV1(source []DeviceV1) []DeviceV1 {
	if len(source) == 0 {
		return nil
	}
	result := make([]DeviceV1, len(source))
	for i, value := range source {
		result[i] = value
		result[i].Entities = make([]EntityV1, len(value.Entities))
		for j, entity := range value.Entities {
			result[i].Entities[j] = entity
			result[i].Entities[j].Features = make([]FeatureV1, len(entity.Features))
			for k, feature := range entity.Features {
				result[i].Entities[j].Features[k] = feature
				result[i].Entities[j].Features[k].Raw = copySnapshotRawV1(feature.Raw)
				result[i].Entities[j].Features[k].Unknown = append([]eebusraw.UnknownField(nil), feature.Unknown...)
			}
			result[i].Entities[j].Raw = copySnapshotRawV1(entity.Raw)
			result[i].Entities[j].Unknown = append([]eebusraw.UnknownField(nil), entity.Unknown...)
		}
		result[i].UseCaseClaims = make([]UseCaseClaimV1, len(value.UseCaseClaims))
		for j, claim := range value.UseCaseClaims {
			result[i].UseCaseClaims[j] = claim
			result[i].UseCaseClaims[j].Raw = copySnapshotRawV1(claim.Raw)
			result[i].UseCaseClaims[j].Unknown = append([]eebusraw.UnknownField(nil), claim.Unknown...)
		}
		result[i].Raw = copySnapshotRawV1(value.Raw)
		result[i].Unknown = append([]eebusraw.UnknownField(nil), value.Unknown...)
	}
	return result
}
