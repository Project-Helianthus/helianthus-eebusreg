package eebusruntime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

// RedactedSnapshotV1 is the masked view of a SnapshotV1 for consumers without raw authority.
// Identities become eebusraw.RedactedID values, pairing keeps only its states, feature roles
// other than client and server become unspecified, and native addresses, metadata and opaque
// observations are dropped. Devices nest their entities, features and use-case claims, and each
// top-level entity, feature and use case must be referenced exactly once through them. Limits:
// 256 services, 1024 devices, 4096 entities, 16384 features and 4096 use cases. Build it with
// BuildRedactedSnapshotV1. String, GoString and Format print a fixed placeholder, so formatting
// never reveals the content.
type RedactedSnapshotV1 struct {
	Meta     RedactedSnapshotMetaV1  `json:"meta"`
	Status   RuntimeObservationV1    `json:"status"`
	Pairing  []eebusraw.PairingState `json:"pairing"`
	Services []RedactedServiceV1     `json:"services"`
	Sessions []RedactedSessionV1     `json:"sessions"`
	Devices  []RedactedDeviceV1      `json:"devices"`
	Entities []RedactedEntityV1      `json:"entities"`
	Features []RedactedFeatureV1     `json:"features"`
	UseCases []RedactedUseCaseV1     `json:"use_cases"`
}

// RedactedSnapshotMetaV1 identifies a redacted snapshot. Contract equals SnapshotContractV1 and
// MaskTier must be eebusraw.MaskTierRedacted. DataHash is "sha256:" followed by the lowercase
// hexadecimal SHA-256 of the canonical JSON of the content; CapturedAt is excluded, so captures
// that differ only in capture time share the hash. Times are normalized to UTC.
type RedactedSnapshotMetaV1 struct {
	Contract      string              `json:"contract"`
	Runtime       eebusraw.RedactedID `json:"runtime"`
	LocalSKI      eebusraw.RedactedID `json:"local_ski"`
	MaskTier      eebusraw.MaskTier   `json:"mask_tier"`
	CapturedAt    time.Time           `json:"captured_at"`
	DataTimestamp time.Time           `json:"data_timestamp"`
	DataHash      string              `json:"data_hash,omitempty"`
}

// RedactedServiceV1 is one service with its identity masked as a peer identifier. Kind is local
// or remote; Visible and Paired are copied from the raw observation.
type RedactedServiceV1 struct {
	ID      eebusraw.RedactedID `json:"id"`
	Kind    ServiceKindV1       `json:"kind"`
	Visible bool                `json:"visible"`
	Paired  bool                `json:"paired"`
}

// RedactedSessionV1 is one session with its ID masked as a session identifier and its remote
// masked as a remote SKI; State and Since, in UTC, are copied.
type RedactedSessionV1 struct {
	ID     eebusraw.RedactedID    `json:"id"`
	Remote eebusraw.RedactedID    `json:"remote"`
	State  ObservedSessionStateV1 `json:"state"`
	Since  time.Time              `json:"since"`
}

// RedactedDeviceV1 is one remote device with its masked identity, the entities addressed under
// it, and the use cases whose context address is the device address or begins with it and a
// colon. ID has kind device and is derived from the device identity basis described by
// NativeIdentityBasis, never from the SKI, so it survives a certificate replacement.
type RedactedDeviceV1 struct {
	ID            eebusraw.RedactedID `json:"id"`
	Entities      []RedactedEntityV1  `json:"entities"`
	UseCaseClaims []RedactedUseCaseV1 `json:"usecase_claims"`
}

// RedactedEntityV1 is one entity with its masked identity and the features addressed under it;
// the same value appears at top level and inside its device. ID has kind entity and is derived from
// the basis of its device and its entity address.
type RedactedEntityV1 struct {
	ID       eebusraw.RedactedID `json:"id"`
	Features []RedactedFeatureV1 `json:"features"`
}

// RedactedFeatureV1 is one feature with its masked identity and its role reduced to client,
// server or unspecified. ID has kind feature and is derived from the basis of its entity and its
// feature address.
type RedactedFeatureV1 struct {
	ID   eebusraw.RedactedID `json:"id"`
	Role FeatureRoleV1       `json:"role"`
}

// RedactedUseCaseV1 is one use-case claim reduced to its masked identity.
type RedactedUseCaseV1 struct {
	ID eebusraw.RedactedID `json:"id"`
}

// BuildRedactedSnapshotV1 validates source as NewSnapshotV1 does, masks it, sorts every
// collection by masked identity, computes Meta.DataHash and validates the result. It fails, and
// never returns a partial result, when the source is invalid or a relationship cannot be
// represented, such as an entity or use case that matches no device, an entity or feature whose
// device address names more than one device, or two devices that share one identity basis.
func BuildRedactedSnapshotV1(source SnapshotV1) (RedactedSnapshotV1, error) {
	raw, err := NewSnapshotV1(source)
	if err != nil {
		return RedactedSnapshotV1{}, err
	}
	localSKI, err := eebusraw.RedactID(eebusraw.IDKindLocalSKI, raw.Meta.LocalSKI)
	if err != nil {
		return RedactedSnapshotV1{}, errors.New("redact local SKI")
	}
	result := RedactedSnapshotV1{
		Meta: RedactedSnapshotMetaV1{
			Contract:      raw.Meta.Contract,
			Runtime:       raw.Meta.Runtime,
			LocalSKI:      localSKI,
			MaskTier:      eebusraw.MaskTierRedacted,
			CapturedAt:    raw.Meta.CapturedAt,
			DataTimestamp: raw.Meta.DataTimestamp,
		},
		Status:   raw.Status,
		Pairing:  make([]eebusraw.PairingState, 0, len(raw.Pairing)),
		Services: make([]RedactedServiceV1, 0, len(raw.Services)),
		Sessions: make([]RedactedSessionV1, 0, len(raw.Sessions)),
		Devices:  make([]RedactedDeviceV1, 0, len(raw.Devices)),
		Entities: make([]RedactedEntityV1, 0, len(raw.Entities)),
		Features: make([]RedactedFeatureV1, 0, len(raw.Features)),
		UseCases: make([]RedactedUseCaseV1, 0, len(raw.UseCases)),
	}
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
	if err := buildRedactedGraph(raw, &result); err != nil {
		return RedactedSnapshotV1{}, err
	}
	result = canonicalRedactedSnapshotV1(result)
	hash, err := result.computeDataHash()
	if err != nil {
		return RedactedSnapshotV1{}, err
	}
	result.Meta.DataHash = hash
	if err := result.Validate(); err != nil {
		return RedactedSnapshotV1{}, err
	}
	return result, nil
}

func buildRedactedGraph(raw SnapshotV1, result *RedactedSnapshotV1) error {
	bases, ambiguous := redactedDeviceBases(raw.Devices)
	entitiesByDevice := make(map[string][]RedactedEntityV1, len(raw.Devices))
	featuresByEntity := make(map[string][]RedactedFeatureV1, len(raw.Entities))
	useCasesByDevice := make(map[string][]RedactedUseCaseV1, len(raw.Devices))
	for _, feature := range raw.Features {
		parent, err := redactedParentBasis(bases, ambiguous, feature.DeviceAddress)
		if err != nil {
			return err
		}
		basis := parent.entity(feature.EntityAddress).feature(feature.FeatureAddress)
		id, err := eebusraw.RedactID(eebusraw.IDKindFeature, basis.preimage())
		if err != nil {
			return errors.New("redact feature identity")
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
		parent, err := redactedParentBasis(bases, ambiguous, entity.DeviceAddress)
		if err != nil {
			return err
		}
		id, err := eebusraw.RedactID(eebusraw.IDKindEntity, parent.entity(entity.EntityAddress).preimage())
		if err != nil {
			return errors.New("redact entity identity")
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
			return errors.New("redact use-case identity")
		}
		value := RedactedUseCaseV1{ID: id}
		result.UseCases = append(result.UseCases, value)
		for _, device := range raw.Devices {
			if useCase.ContextAddress == device.Address ||
				strings.HasPrefix(useCase.ContextAddress, device.Address+":") {
				useCasesByDevice[device.Address] = append(useCasesByDevice[device.Address], value)
				break
			}
		}
	}
	for _, device := range raw.Devices {
		id, err := eebusraw.RedactID(eebusraw.IDKindDevice, spineDeviceBasis(device.SHIPID, device.Address).preimage())
		if err != nil {
			return errors.New("redact device identity")
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
	return nil
}

const (
	spineBasisSourceSHIPID     = "ship_id"
	spineBasisSourceUnresolved = "unresolved"
)

type spineIdentityBasis struct {
	kind, source, value           string
	entityAddress, featureAddress *string
}

func spineDeviceBasis(shipID *string, address string) spineIdentityBasis {
	if shipID != nil && strings.TrimSpace(*shipID) != "" {
		return spineIdentityBasis{kind: "device", source: spineBasisSourceSHIPID, value: *shipID}
	}
	return spineIdentityBasis{kind: "device", source: spineBasisSourceUnresolved, value: address}
}

func (basis spineIdentityBasis) entity(address string) spineIdentityBasis {
	return spineIdentityBasis{kind: "entity", source: basis.source, value: basis.value, entityAddress: &address}
}

func (basis spineIdentityBasis) feature(address string) spineIdentityBasis {
	entityAddress := ""
	if basis.entityAddress != nil {
		entityAddress = *basis.entityAddress
	}
	return spineIdentityBasis{
		kind: "feature", source: basis.source, value: basis.value,
		entityAddress: &entityAddress, featureAddress: &address,
	}
}

// preimage encodes the basis as a JSON array of its present members, so that no two bases with
// valid UTF-8 members share a preimage whatever separators their text contains.
func (basis spineIdentityBasis) preimage() string {
	members := []string{basis.kind, basis.source, basis.value}
	if basis.entityAddress != nil {
		members = append(members, *basis.entityAddress)
	}
	if basis.featureAddress != nil {
		members = append(members, *basis.featureAddress)
	}
	encoded, _ := json.Marshal(members)
	return string(encoded)
}

func redactedDeviceBases(devices []DeviceV1) (map[string]spineIdentityBasis, map[string]struct{}) {
	bases := make(map[string]spineIdentityBasis, len(devices))
	ambiguous := make(map[string]struct{})
	for _, device := range devices {
		if _, found := bases[device.Address]; found {
			ambiguous[device.Address] = struct{}{}
			continue
		}
		bases[device.Address] = spineDeviceBasis(device.SHIPID, device.Address)
	}
	return bases, ambiguous
}

func redactedParentBasis(bases map[string]spineIdentityBasis, ambiguous map[string]struct{}, address string) (spineIdentityBasis, error) {
	if _, found := ambiguous[address]; found {
		return spineIdentityBasis{}, errors.New("redacted node matches more than one device")
	}
	basis, found := bases[address]
	if !found {
		return spineIdentityBasis{}, errors.New("redacted node matches no device")
	}
	return basis, nil
}

// MarshalJSON validates the snapshot, including its data hash, and writes it as canonical JSON
// with sorted collections; an invalid snapshot is never encoded.
func (snapshot RedactedSnapshotV1) MarshalJSON() ([]byte, error) {
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	type wire RedactedSnapshotV1
	return marshalJCSV1(wire(canonicalRedactedSnapshotV1(snapshot)))
}

// String returns a fixed placeholder instead of the content.
func (RedactedSnapshotV1) String() string {
	return "redacted_snapshot_v1:[redacted]"
}

// GoString returns the String placeholder, so %#v reveals nothing either.
func (snapshot RedactedSnapshotV1) GoString() string {
	return snapshot.String()
}

// Format writes the String placeholder for every verb and flag.
func (snapshot RedactedSnapshotV1) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, snapshot.String())
}

// UnmarshalJSON accepts exactly one JSON object without unknown fields, validates it including
// the data hash, and stores it in canonical order with UTC times. The receiver is unchanged on
// error.
func (snapshot *RedactedSnapshotV1) UnmarshalJSON(data []byte) error {
	type wire RedactedSnapshotV1
	var decoded wire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("multiple JSON values are not allowed")
	}
	result := RedactedSnapshotV1(decoded)
	if err := result.Validate(); err != nil {
		return err
	}
	*snapshot = canonicalRedactedSnapshotV1(result)
	return nil
}

// Validate checks the contract and mask tier, the kind of every masked identity, the closed state
// values, uniqueness and collection limits, the device graph with matching feature roles, and that
// Meta.DataHash matches the content. It reports the first violation.
func (snapshot RedactedSnapshotV1) Validate() error {
	if snapshot.Meta.Contract != SnapshotContractV1 {
		return errors.New("redacted snapshot contract is invalid")
	}
	if snapshot.Meta.MaskTier != eebusraw.MaskTierRedacted {
		return errors.New("redacted snapshot mask tier is invalid")
	}
	if err := validateSnapshotIDV1(snapshot.Meta.Runtime, eebusraw.IDKindPeer, eebusraw.IDKindLocalSKI); err != nil {
		return errors.New("redacted runtime identity is invalid")
	}
	if err := validateSnapshotIDV1(snapshot.Meta.LocalSKI, eebusraw.IDKindLocalSKI); err != nil {
		return errors.New("redacted local identity is invalid")
	}
	if err := validateSnapshotTimestampV1(snapshot.Meta.CapturedAt, true); err != nil {
		return errors.New("redacted captured_at is invalid")
	}
	if err := validateSnapshotTimestampV1(snapshot.Meta.DataTimestamp, true); err != nil {
		return errors.New("redacted data_timestamp is invalid")
	}
	if err := validateRuntimeObservationV1(snapshot.Status); err != nil {
		return err
	}
	for _, state := range snapshot.Pairing {
		switch state {
		case eebusraw.PairingStateUnknown, eebusraw.PairingStateUnpaired,
			eebusraw.PairingStatePaired, eebusraw.PairingStateDenied:
		default:
			return errors.New("redacted pairing state is invalid")
		}
	}
	if err := validateRedactedServicesV1(snapshot.Services); err != nil {
		return err
	}
	if err := validateRedactedSessionsV1(snapshot.Sessions); err != nil {
		return err
	}
	if err := validateRedactedGraphV1(snapshot); err != nil {
		return err
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
	return nil
}

func validateRedactedServicesV1(values []RedactedServiceV1) error {
	if len(values) > 256 {
		return errors.New("redacted service count exceeds the contract limit")
	}
	seen := make(map[string]struct{}, len(values))
	for _, service := range values {
		if err := validateSnapshotIDV1(service.ID, eebusraw.IDKindPeer); err != nil {
			return errors.New("redacted service identity is invalid")
		}
		if service.Kind != ServiceKindV1Local && service.Kind != ServiceKindV1Remote {
			return errors.New("redacted service kind is invalid")
		}
		key := redactedIdentityKeyV1(service.ID)
		if _, exists := seen[key]; exists {
			return errors.New("redacted services contain a duplicate identity")
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateRedactedSessionsV1(values []RedactedSessionV1) error {
	seen := make(map[string]struct{}, len(values))
	for _, session := range values {
		if err := validateSnapshotIDV1(session.ID, eebusraw.IDKindSession); err != nil {
			return errors.New("redacted session identity is invalid")
		}
		if err := validateSnapshotIDV1(session.Remote, eebusraw.IDKindRemoteSKI); err != nil {
			return errors.New("redacted session remote identity is invalid")
		}
		switch session.State {
		case ObservedSessionStateV1Unknown, ObservedSessionStateV1Connecting,
			ObservedSessionStateV1Connected, ObservedSessionStateV1Disconnected,
			ObservedSessionStateV1Degraded:
		default:
			return errors.New("redacted session state is invalid")
		}
		if err := validateSnapshotTimestampV1(session.Since, true); err != nil {
			return errors.New("redacted session timestamp is invalid")
		}
		key := redactedIdentityKeyV1(session.ID)
		if _, exists := seen[key]; exists {
			return errors.New("redacted sessions contain a duplicate identity")
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateRedactedGraphV1(snapshot RedactedSnapshotV1) error {
	devices, err := redactedDeviceIndexV1(snapshot.Devices)
	if err != nil {
		return err
	}
	entities, err := redactedEntityIndexV1(snapshot.Entities)
	if err != nil {
		return err
	}
	features, err := redactedFeatureIndexV1(snapshot.Features)
	if err != nil {
		return err
	}
	useCases, err := redactedUseCaseIndexV1(snapshot.UseCases)
	if err != nil {
		return err
	}
	referencedEntities := make(map[string]struct{}, len(entities))
	referencedFeatures := make(map[string]struct{}, len(features))
	referencedUseCases := make(map[string]struct{}, len(useCases))
	for _, device := range devices {
		for _, entity := range device.Entities {
			key := redactedIdentityKeyV1(entity.ID)
			top, exists := entities[key]
			if !exists || !equalRedactedEntityV1(top, entity) {
				return errors.New("redacted device contains an invalid entity relationship")
			}
			if _, duplicate := referencedEntities[key]; duplicate {
				return errors.New("redacted entity relationship is duplicated")
			}
			referencedEntities[key] = struct{}{}
			for _, feature := range entity.Features {
				featureKey := redactedIdentityKeyV1(feature.ID)
				topFeature, exists := features[featureKey]
				if !exists || topFeature.Role != feature.Role {
					return errors.New("redacted entity contains an invalid feature relationship")
				}
				if _, duplicate := referencedFeatures[featureKey]; duplicate {
					return errors.New("redacted feature relationship is duplicated")
				}
				referencedFeatures[featureKey] = struct{}{}
			}
		}
		for _, useCase := range device.UseCaseClaims {
			key := redactedIdentityKeyV1(useCase.ID)
			if _, exists := useCases[key]; !exists {
				return errors.New("redacted device contains an invalid use-case relationship")
			}
			if _, duplicate := referencedUseCases[key]; duplicate {
				return errors.New("redacted use-case relationship is duplicated")
			}
			referencedUseCases[key] = struct{}{}
		}
	}
	if len(referencedEntities) != len(entities) ||
		len(referencedFeatures) != len(features) ||
		len(referencedUseCases) != len(useCases) {
		return errors.New("redacted graph contains an unreferenced relationship")
	}
	return nil
}

func redactedDeviceIndexV1(values []RedactedDeviceV1) (map[string]RedactedDeviceV1, error) {
	if len(values) > 1024 {
		return nil, errors.New("redacted device count exceeds the contract limit")
	}
	result := make(map[string]RedactedDeviceV1, len(values))
	for _, value := range values {
		if err := validateSnapshotIDV1(value.ID, eebusraw.IDKindDevice); err != nil {
			return nil, errors.New("redacted device identity is invalid")
		}
		key := redactedIdentityKeyV1(value.ID)
		if _, exists := result[key]; exists {
			return nil, errors.New("redacted devices contain a duplicate identity")
		}
		result[key] = value
	}
	return result, nil
}

func redactedEntityIndexV1(values []RedactedEntityV1) (map[string]RedactedEntityV1, error) {
	if len(values) > 4096 {
		return nil, errors.New("redacted entity count exceeds the contract limit")
	}
	result := make(map[string]RedactedEntityV1, len(values))
	for _, value := range values {
		if err := validateSnapshotIDV1(value.ID, eebusraw.IDKindEntity); err != nil {
			return nil, errors.New("redacted entity identity is invalid")
		}
		key := redactedIdentityKeyV1(value.ID)
		if _, exists := result[key]; exists {
			return nil, errors.New("redacted entities contain a duplicate identity")
		}
		result[key] = value
	}
	return result, nil
}

func redactedFeatureIndexV1(values []RedactedFeatureV1) (map[string]RedactedFeatureV1, error) {
	if len(values) > 16384 {
		return nil, errors.New("redacted feature count exceeds the contract limit")
	}
	result := make(map[string]RedactedFeatureV1, len(values))
	for _, value := range values {
		if err := validateSnapshotIDV1(value.ID, eebusraw.IDKindFeature); err != nil {
			return nil, errors.New("redacted feature identity is invalid")
		}
		switch value.Role {
		case FeatureRoleV1Unspecified, FeatureRoleV1Client, FeatureRoleV1Server:
		default:
			return nil, errors.New("redacted feature role is invalid")
		}
		key := redactedIdentityKeyV1(value.ID)
		if _, exists := result[key]; exists {
			return nil, errors.New("redacted features contain a duplicate identity")
		}
		result[key] = value
	}
	return result, nil
}

func redactedUseCaseIndexV1(values []RedactedUseCaseV1) (map[string]RedactedUseCaseV1, error) {
	if len(values) > 4096 {
		return nil, errors.New("redacted use-case count exceeds the contract limit")
	}
	result := make(map[string]RedactedUseCaseV1, len(values))
	for _, value := range values {
		if err := validateSnapshotIDV1(value.ID, eebusraw.IDKindPeer); err != nil {
			return nil, errors.New("redacted use-case identity is invalid")
		}
		key := redactedIdentityKeyV1(value.ID)
		if _, exists := result[key]; exists {
			return nil, errors.New("redacted use cases contain a duplicate identity")
		}
		result[key] = value
	}
	return result, nil
}

func equalRedactedEntityV1(left, right RedactedEntityV1) bool {
	if redactedIdentityKeyV1(left.ID) != redactedIdentityKeyV1(right.ID) ||
		len(left.Features) != len(right.Features) {
		return false
	}
	leftFeatures := append([]RedactedFeatureV1(nil), left.Features...)
	rightFeatures := append([]RedactedFeatureV1(nil), right.Features...)
	sortRedactedFeaturesV1(leftFeatures)
	sortRedactedFeaturesV1(rightFeatures)
	for index := range leftFeatures {
		if redactedIdentityKeyV1(leftFeatures[index].ID) != redactedIdentityKeyV1(rightFeatures[index].ID) ||
			leftFeatures[index].Role != rightFeatures[index].Role {
			return false
		}
	}
	return true
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
		UseCases      []RedactedUseCaseV1     `json:"use_cases"`
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
