package eebusruntime

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

// NativeSnapshotContractV2 is the only Meta.Contract value NativeSnapshotV2.Validate accepts.
const NativeSnapshotContractV2 = "helianthus.eebus.runtime.native-snapshot.v2"

const (
	nativeSnapshotV2MaximumIdentifierBytes = 4096
	nativeSnapshotV2MaximumPathBytes       = 512
	nativeSnapshotV2MaximumSourceBytes     = 128
	nativeSnapshotV2MaximumPayloadBytes    = 65536
)

// NativeSnapshotV2 is one bounded capture of a native runtime: its state, pairing, services,
// sessions, and the remote device topology with native observations. Native text is kept as
// reported instead of being mapped to closed enumerations, and identities (SKIs, SHIP IDs,
// addresses) are not redacted, so the snapshot belongs behind an authorization boundary that
// admits them. Validate requires each identifier to be non-blank and at most 4096 bytes after
// trimming, and each state, kind, type, role, actor, source or version label at most 128 bytes.
// Devices, Entities, Features and UseCases form one containment graph: every relation endpoint
// names a device address, optionally with an entity address and a feature address, and resolves
// to exactly one node of the snapshot. Absences lists, in a deterministic order, at most 1024
// native nodes the runtime observed but withheld; AbsencesOmitted counts further withheld nodes
// and is nonzero only when Absences holds exactly 1024 records.
// Field meanings are documented in
// https://github.com/Project-Helianthus/helianthus-docs-eebus/tree/main/api.
type NativeSnapshotV2 struct {
	Meta            NativeSnapshotMetaV2         `json:"meta"`
	Status          NativeRuntimeObservationV2   `json:"status"`
	Pairing         []NativePairingObservationV2 `json:"pairing"`
	Services        []NativeServiceV2            `json:"services"`
	Sessions        []NativeSessionV2            `json:"sessions"`
	Devices         []NativeDeviceV2             `json:"devices"`
	Entities        []NativeEntityV2             `json:"entities"`
	Features        []NativeFeatureV2            `json:"features"`
	UseCases        []NativeUseCaseV2            `json:"use_cases"`
	Observations    []NativeObservationV2        `json:"observations"`
	Absences        []NativeAbsence              `json:"absences"`
	AbsencesOmitted uint64                       `json:"absences_omitted"`
}

// NativeSnapshotMetaV2 identifies a native snapshot and its capture. Contract must equal
// NativeSnapshotContractV2 and LocalSKI must be 40 lowercase hexadecimal characters. ObservedAt,
// CapturedAt and DataTimestamp are required; a nil ProtocolVersion means not reported.
type NativeSnapshotMetaV2 struct {
	Contract        string    `json:"contract"`
	Runtime         string    `json:"runtime"`
	LocalSKI        string    `json:"local_ski"`
	Source          string    `json:"source"`
	ObservedAt      time.Time `json:"observed_at"`
	ProtocolVersion *string   `json:"protocol_version,omitempty"`
	CapturedAt      time.Time `json:"captured_at"`
	DataTimestamp   time.Time `json:"data_timestamp"`
}

// NativeRuntimeObservationV2 is the reported runtime State; Degradation is nil when none is
// reported. Shutdown freezes the last snapshot with State "shutdown" and no degradation.
type NativeRuntimeObservationV2 struct {
	State       string               `json:"state"`
	Degradation *NativeDegradationV2 `json:"degradation,omitempty"`
}

// NativeDegradationV2 is a reported degradation Reason that has held since a required time.
type NativeDegradationV2 struct {
	Reason string    `json:"reason"`
	Since  time.Time `json:"since"`
}

// NativePairingObservationV2 is the observed pairing State of the remote with RemoteSKI since a
// required time. It records an observation, not trust or qualification.
type NativePairingObservationV2 struct {
	RemoteSKI string    `json:"remote_ski"`
	State     string    `json:"state"`
	Since     time.Time `json:"since"`
}

// NativeServiceV2 is one service known to the runtime, keyed by SKI, with its reported Kind.
// Visible and Paired are the runtime's observations. The pointer fields are announced
// descriptive values, nil when not announced, and Validate does not check them.
type NativeServiceV2 struct {
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

// NativeSessionV2 is one session with the remote that has RemoteSKI, in its reported State since
// a required time.
type NativeSessionV2 struct {
	ID        string    `json:"id"`
	RemoteSKI string    `json:"remote_ski"`
	State     string    `json:"state"`
	Since     time.Time `json:"since"`
}

// NativeDeviceV2 is one remote device of the service with SKI, identified by its native Address.
// Description and Metadata are reported as is and not validated; Observations follow the rules
// of NativeSnapshotV2.Observations.
type NativeDeviceV2 struct {
	SKI          string                `json:"ski"`
	SHIPID       *string               `json:"ship_id,omitempty"`
	Address      string                `json:"address"`
	Type         string                `json:"type"`
	Description  *string               `json:"description,omitempty"`
	Metadata     map[string]string     `json:"metadata,omitempty"`
	Observations []NativeObservationV2 `json:"observations,omitempty"`
}

// NativeEntityV2 is one entity of the device with DeviceAddress, with its reported Type.
// ParentEntityAddress is the address of the enclosing entity of the same device and nil for a
// top-level entity. Validate requires the device to exist, the device and entity address pair to
// be unique, the parent to be another entity of the same device, and parent chains to end.
type NativeEntityV2 struct {
	DeviceAddress       string  `json:"device_address"`
	EntityAddress       string  `json:"entity_address"`
	ParentEntityAddress *string `json:"parent_entity_address,omitempty"`
	Type                string  `json:"type"`
	Description         *string `json:"description,omitempty"`
}

// NativeFeatureV2 is one feature of the entity at DeviceAddress and EntityAddress. Type and Role
// are reported text; Role is not mapped to FeatureRoleV1. Validate requires the entity to exist
// and the device, entity and feature address triple to be unique.
type NativeFeatureV2 struct {
	DeviceAddress  string  `json:"device_address"`
	EntityAddress  string  `json:"entity_address"`
	FeatureAddress string  `json:"feature_address"`
	Type           string  `json:"type"`
	Role           string  `json:"role"`
	Description    *string `json:"description,omitempty"`
}

// NativeUseCaseV2 is one use-case claim announced at ContextAddress, the native address kept as
// reported. Its scope is the device at DeviceAddress, narrowed to the entity at EntityAddress when
// set and to the feature at FeatureAddress when set; a feature scope requires an entity scope, and
// every scope endpoint must exist in the snapshot. A claim is unique by its scope, Name, Actor,
// Version and DocumentSubrevision. Name and Actor are required; the other pointer fields are nil
// when not announced, so a nil Availability means unknown rather than unavailable. Scenarios are
// not validated.
type NativeUseCaseV2 struct {
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

// NativeAbsence records one native node the runtime observed but did not emit. Kind is device,
// entity, feature or use_case. Reason is missing_address, missing_type, missing_role,
// missing_name or missing_actor when a required native value was not reported,
// missing_container when the enclosing device, entity or claim scope was not emitted, and
// ambiguous_address when the node's address is not unique. DeviceAddress is required; the
// pointer fields carry the entity and feature addresses where they are known. Absence records
// are not part of the containment graph.
type NativeAbsence struct {
	Kind           string  `json:"kind"`
	Reason         string  `json:"reason"`
	DeviceAddress  string  `json:"device_address"`
	EntityAddress  *string `json:"entity_address,omitempty"`
	FeatureAddress *string `json:"feature_address,omitempty"`
}

// NativeObservationV2 is one native value read at Path (at most 512 bytes) from Source at the
// required time ObservedAt, retained as evidence without interpretation. A nil ProtocolVersion
// means not reported.
type NativeObservationV2 struct {
	Path            string        `json:"path"`
	Source          string        `json:"source"`
	ObservedAt      time.Time     `json:"observed_at"`
	ProtocolVersion *string       `json:"protocol_version,omitempty"`
	Value           NativeValueV2 `json:"value"`
}

// NativeValueV2 is a JSON-shaped native value with exactly one field set; a non-nil Null marks a
// JSON null whatever it points to. Float must be finite. Number keeps a JSON number as text so
// that values outside int64 and float64 keep their exact digits. String and Number hold at most
// 65536 bytes, Array at most 256 elements, Object at most 32 keys of at most 128 bytes, and values
// nest at most three deep, counting the outermost.
type NativeValueV2 struct {
	Null    *bool                     `json:"null,omitempty"`
	Boolean *bool                     `json:"boolean,omitempty"`
	Integer *int64                    `json:"integer,omitempty"`
	Float   *float64                  `json:"float,omitempty"`
	Number  *string                   `json:"number,omitempty"`
	String  *string                   `json:"string,omitempty"`
	Array   *[]NativeValueV2          `json:"array,omitempty"`
	Object  *map[string]NativeValueV2 `json:"object,omitempty"`
}

var nativeSnapshotV2JSONNumber = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`)

// NewNativeSnapshotV2 returns a deep copy of draft once the copy passes Validate; draft itself is
// neither modified nor retained.
func NewNativeSnapshotV2(draft NativeSnapshotV2) (NativeSnapshotV2, error) {
	snapshot := draft.Clone()
	if err := snapshot.Validate(); err != nil {
		return NativeSnapshotV2{}, err
	}
	return snapshot, nil
}

// Validate reports the first violation of the contract value, the local SKI form, the required
// text and timestamps, the value bounds, the containment of devices, entities, features and
// use-case claims, or the absence records. Containment requires unique device addresses, entity
// and feature addresses unique within their container, every entity, parent entity, feature
// container and claim scope to resolve to exactly one node, and parent chains to end. It does not
// check the fields documented as not validated.
func (snapshot NativeSnapshotV2) Validate() error {
	if snapshot.Meta.Contract != NativeSnapshotContractV2 {
		return fmt.Errorf("contract must be %q", NativeSnapshotContractV2)
	}
	if !nativeSnapshotV2ValidText(snapshot.Meta.Runtime, nativeSnapshotV2MaximumIdentifierBytes) {
		return errors.New("runtime identity is invalid")
	}
	if !nativeSnapshotV2ValidSKI(snapshot.Meta.LocalSKI) {
		return errors.New("local_ski must contain 40 lowercase hexadecimal characters")
	}
	if !nativeSnapshotV2ValidText(snapshot.Meta.Source, nativeSnapshotV2MaximumSourceBytes) {
		return errors.New("snapshot source is invalid")
	}
	if snapshot.Meta.ObservedAt.IsZero() || snapshot.Meta.CapturedAt.IsZero() || snapshot.Meta.DataTimestamp.IsZero() {
		return errors.New("snapshot observation timestamps are required")
	}
	if snapshot.Meta.ProtocolVersion != nil && !nativeSnapshotV2ValidText(*snapshot.Meta.ProtocolVersion, nativeSnapshotV2MaximumSourceBytes) {
		return errors.New("snapshot protocol_version is invalid")
	}
	if !nativeSnapshotV2ValidText(snapshot.Status.State, nativeSnapshotV2MaximumSourceBytes) {
		return errors.New("runtime state is invalid")
	}
	if snapshot.Status.Degradation != nil {
		if !nativeSnapshotV2ValidText(snapshot.Status.Degradation.Reason, nativeSnapshotV2MaximumSourceBytes) || snapshot.Status.Degradation.Since.IsZero() {
			return errors.New("runtime degradation is invalid")
		}
	}
	for _, pairing := range snapshot.Pairing {
		if !nativeSnapshotV2ValidText(pairing.RemoteSKI, nativeSnapshotV2MaximumIdentifierBytes) || !nativeSnapshotV2ValidText(pairing.State, nativeSnapshotV2MaximumSourceBytes) || pairing.Since.IsZero() {
			return errors.New("pairing observation is invalid")
		}
	}
	for _, service := range snapshot.Services {
		if !nativeSnapshotV2ValidText(service.SKI, nativeSnapshotV2MaximumIdentifierBytes) || !nativeSnapshotV2ValidText(service.Kind, nativeSnapshotV2MaximumSourceBytes) {
			return errors.New("service observation is invalid")
		}
	}
	for _, session := range snapshot.Sessions {
		if !nativeSnapshotV2ValidText(session.ID, nativeSnapshotV2MaximumIdentifierBytes) || !nativeSnapshotV2ValidText(session.RemoteSKI, nativeSnapshotV2MaximumIdentifierBytes) || !nativeSnapshotV2ValidText(session.State, nativeSnapshotV2MaximumSourceBytes) || session.Since.IsZero() {
			return errors.New("session observation is invalid")
		}
	}
	for _, device := range snapshot.Devices {
		if !nativeSnapshotV2ValidText(device.SKI, nativeSnapshotV2MaximumIdentifierBytes) || !nativeSnapshotV2ValidText(device.Address, nativeSnapshotV2MaximumIdentifierBytes) || !nativeSnapshotV2ValidText(device.Type, nativeSnapshotV2MaximumSourceBytes) {
			return errors.New("device observation is invalid")
		}
		if err := nativeSnapshotV2ValidateObservations(device.Observations); err != nil {
			return fmt.Errorf("device observations: %w", err)
		}
	}
	for _, entity := range snapshot.Entities {
		if !nativeSnapshotV2ValidText(entity.DeviceAddress, nativeSnapshotV2MaximumIdentifierBytes) || !nativeSnapshotV2ValidText(entity.EntityAddress, nativeSnapshotV2MaximumIdentifierBytes) || !nativeSnapshotV2ValidText(entity.Type, nativeSnapshotV2MaximumSourceBytes) {
			return errors.New("entity observation is invalid")
		}
	}
	for _, feature := range snapshot.Features {
		if !nativeSnapshotV2ValidText(feature.DeviceAddress, nativeSnapshotV2MaximumIdentifierBytes) || !nativeSnapshotV2ValidText(feature.EntityAddress, nativeSnapshotV2MaximumIdentifierBytes) || !nativeSnapshotV2ValidText(feature.FeatureAddress, nativeSnapshotV2MaximumIdentifierBytes) || !nativeSnapshotV2ValidText(feature.Type, nativeSnapshotV2MaximumSourceBytes) || !nativeSnapshotV2ValidText(feature.Role, nativeSnapshotV2MaximumSourceBytes) {
			return errors.New("feature observation is invalid")
		}
	}
	for _, useCase := range snapshot.UseCases {
		if !nativeSnapshotV2ValidText(useCase.ContextAddress, nativeSnapshotV2MaximumIdentifierBytes) || !nativeSnapshotV2ValidText(useCase.Name, nativeSnapshotV2MaximumIdentifierBytes) || !nativeSnapshotV2ValidText(useCase.Actor, nativeSnapshotV2MaximumSourceBytes) {
			return errors.New("use-case observation is invalid")
		}
	}
	if err := nativeSnapshotV2ValidateObservations(snapshot.Observations); err != nil {
		return fmt.Errorf("native observations: %w", err)
	}
	if err := validateNativeContainment(snapshot); err != nil {
		return err
	}
	return validateNativeAbsences(snapshot.Absences, snapshot.AbsencesOmitted)
}

// Clone returns a deep copy that shares no slice, map or pointer with snapshot. A nil top-level
// Observations or Absences list comes back empty, so it encodes as [] rather than null.
func (snapshot NativeSnapshotV2) Clone() NativeSnapshotV2 {
	result := snapshot
	result.Meta.ProtocolVersion = nativeSnapshotV2CloneString(snapshot.Meta.ProtocolVersion)
	result.Status.Degradation = nativeSnapshotV2CloneDegradation(snapshot.Status.Degradation)
	result.Pairing = append([]NativePairingObservationV2(nil), snapshot.Pairing...)
	result.Services = append([]NativeServiceV2(nil), snapshot.Services...)
	for index := range result.Services {
		result.Services[index].SHIPID = nativeSnapshotV2CloneString(result.Services[index].SHIPID)
		result.Services[index].Name = nativeSnapshotV2CloneString(result.Services[index].Name)
		result.Services[index].Identifier = nativeSnapshotV2CloneString(result.Services[index].Identifier)
		result.Services[index].Brand = nativeSnapshotV2CloneString(result.Services[index].Brand)
		result.Services[index].Type = nativeSnapshotV2CloneString(result.Services[index].Type)
		result.Services[index].Model = nativeSnapshotV2CloneString(result.Services[index].Model)
	}
	result.Sessions = append([]NativeSessionV2(nil), snapshot.Sessions...)
	result.Devices = append([]NativeDeviceV2(nil), snapshot.Devices...)
	for index := range result.Devices {
		result.Devices[index].SHIPID = nativeSnapshotV2CloneString(result.Devices[index].SHIPID)
		result.Devices[index].Description = nativeSnapshotV2CloneString(result.Devices[index].Description)
		result.Devices[index].Metadata = nativeSnapshotV2CloneMetadata(result.Devices[index].Metadata)
		result.Devices[index].Observations = nativeSnapshotV2CloneObservations(result.Devices[index].Observations)
	}
	result.Entities = append([]NativeEntityV2(nil), snapshot.Entities...)
	for index := range result.Entities {
		result.Entities[index].ParentEntityAddress = nativeSnapshotV2CloneString(result.Entities[index].ParentEntityAddress)
		result.Entities[index].Description = nativeSnapshotV2CloneString(result.Entities[index].Description)
	}
	result.Features = append([]NativeFeatureV2(nil), snapshot.Features...)
	for index := range result.Features {
		result.Features[index].Description = nativeSnapshotV2CloneString(result.Features[index].Description)
	}
	result.UseCases = append([]NativeUseCaseV2(nil), snapshot.UseCases...)
	for index := range result.UseCases {
		result.UseCases[index].EntityAddress = nativeSnapshotV2CloneString(result.UseCases[index].EntityAddress)
		result.UseCases[index].FeatureAddress = nativeSnapshotV2CloneString(result.UseCases[index].FeatureAddress)
		result.UseCases[index].ResolvedRole = nativeSnapshotV2CloneString(result.UseCases[index].ResolvedRole)
		result.UseCases[index].Scenarios = append([]string(nil), result.UseCases[index].Scenarios...)
		result.UseCases[index].Version = nativeSnapshotV2CloneString(result.UseCases[index].Version)
		result.UseCases[index].Availability = nativeSnapshotV2CloneBool(result.UseCases[index].Availability)
		result.UseCases[index].DocumentSubrevision = nativeSnapshotV2CloneString(result.UseCases[index].DocumentSubrevision)
	}
	result.Observations = nativeSnapshotV2CloneObservations(snapshot.Observations)
	result.Absences = make([]NativeAbsence, len(snapshot.Absences))
	for index, absence := range snapshot.Absences {
		result.Absences[index] = absence
		result.Absences[index].EntityAddress = nativeSnapshotV2CloneString(absence.EntityAddress)
		result.Absences[index].FeatureAddress = nativeSnapshotV2CloneString(absence.FeatureAddress)
	}
	return result
}

func nativeSnapshotV2ValidateObservations(values []NativeObservationV2) error {
	for _, value := range values {
		if !nativeSnapshotV2ValidText(value.Path, nativeSnapshotV2MaximumPathBytes) || !nativeSnapshotV2ValidText(value.Source, nativeSnapshotV2MaximumSourceBytes) || value.ObservedAt.IsZero() {
			return errors.New("native observation context is invalid")
		}
		if value.ProtocolVersion != nil && !nativeSnapshotV2ValidText(*value.ProtocolVersion, nativeSnapshotV2MaximumSourceBytes) {
			return errors.New("native observation protocol_version is invalid")
		}
		if err := nativeSnapshotV2ValidateValue(value.Value, 1); err != nil {
			return errors.New("native observation value is invalid")
		}
	}
	return nil
}

func nativeSnapshotV2ValidateValue(value NativeValueV2, depth int) error {
	if depth > 3 {
		return errors.New("native value nesting exceeds the limit")
	}
	variants := 0
	if value.Null != nil {
		variants++
	}
	if value.Boolean != nil {
		variants++
	}
	if value.Integer != nil {
		variants++
	}
	if value.Float != nil {
		variants++
		if math.IsNaN(*value.Float) || math.IsInf(*value.Float, 0) {
			return errors.New("native float is invalid")
		}
	}
	if value.Number != nil {
		variants++
		if len(*value.Number) > nativeSnapshotV2MaximumPayloadBytes || !nativeSnapshotV2JSONNumber.MatchString(*value.Number) {
			return errors.New("native number is invalid")
		}
	}
	if value.String != nil {
		variants++
		if len(*value.String) > nativeSnapshotV2MaximumPayloadBytes {
			return errors.New("native string exceeds the limit")
		}
	}
	if value.Array != nil {
		variants++
		if len(*value.Array) > 256 {
			return errors.New("native array exceeds the limit")
		}
		for _, child := range *value.Array {
			if err := nativeSnapshotV2ValidateValue(child, depth+1); err != nil {
				return err
			}
		}
	}
	if value.Object != nil {
		variants++
		if len(*value.Object) > 32 {
			return errors.New("native object exceeds the limit")
		}
		for key, child := range *value.Object {
			if !nativeSnapshotV2ValidText(key, nativeSnapshotV2MaximumSourceBytes) {
				return errors.New("native object key is invalid")
			}
			if err := nativeSnapshotV2ValidateValue(child, depth+1); err != nil {
				return err
			}
		}
	}
	if variants != 1 {
		return errors.New("native value must select exactly one variant")
	}
	return nil
}

func nativeSnapshotV2ValidText(value string, maximum int) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= maximum
}
func nativeSnapshotV2ValidSKI(value string) bool {
	if len(value) != 40 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
func nativeSnapshotV2CloneString(value *string) *string {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}
func nativeSnapshotV2CloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}
func nativeSnapshotV2CloneDegradation(value *NativeDegradationV2) *NativeDegradationV2 {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}
func nativeSnapshotV2CloneMetadata(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
func nativeSnapshotV2CloneObservations(source []NativeObservationV2) []NativeObservationV2 {
	result := make([]NativeObservationV2, len(source))
	for index, value := range source {
		result[index] = value
		result[index].ProtocolVersion = nativeSnapshotV2CloneString(value.ProtocolVersion)
		result[index].Value = nativeSnapshotV2CloneValue(value.Value)
	}
	return result
}

func nativeSnapshotV2CloneValue(source NativeValueV2) NativeValueV2 {
	result := source
	result.Null = nativeSnapshotV2CloneBool(source.Null)
	result.Boolean = nativeSnapshotV2CloneBool(source.Boolean)
	if source.Integer != nil {
		value := *source.Integer
		result.Integer = &value
	}
	if source.Float != nil {
		value := *source.Float
		result.Float = &value
	}
	result.String = nativeSnapshotV2CloneString(source.String)
	result.Number = nativeSnapshotV2CloneString(source.Number)
	if source.Array != nil {
		values := make([]NativeValueV2, len(*source.Array))
		for index, value := range *source.Array {
			values[index] = nativeSnapshotV2CloneValue(value)
		}
		result.Array = &values
	}
	if source.Object != nil {
		values := make(map[string]NativeValueV2, len(*source.Object))
		for key, value := range *source.Object {
			values[key] = nativeSnapshotV2CloneValue(value)
		}
		result.Object = &values
	}
	return result
}

const nativeAbsenceLimit = 1024

var (
	nativeAbsenceKinds   = map[string]struct{}{"device": {}, "entity": {}, "feature": {}, "use_case": {}}
	nativeAbsenceReasons = map[string]struct{}{
		"missing_address": {}, "missing_type": {}, "missing_role": {}, "missing_name": {},
		"missing_actor": {}, "missing_container": {}, "ambiguous_address": {},
	}
)

type nativeEntityKey struct{ device, entity string }

type nativeFeatureKey struct{ device, entity, feature string }

type nativeOptionalText struct {
	set   bool
	value string
}

type nativeClaimKey struct {
	device, name, actor                   string
	entity, feature, version, subrevision nativeOptionalText
}

func nativeOptional(value *string) nativeOptionalText {
	if value == nil {
		return nativeOptionalText{}
	}
	return nativeOptionalText{set: true, value: *value}
}

func validateNativeContainment(snapshot NativeSnapshotV2) error {
	devices := make(map[string]struct{}, len(snapshot.Devices))
	for _, device := range snapshot.Devices {
		if _, found := devices[device.Address]; found {
			return errors.New("device address is ambiguous")
		}
		devices[device.Address] = struct{}{}
	}
	entities, err := validateNativeEntities(snapshot.Entities, devices)
	if err != nil {
		return err
	}
	features := make(map[nativeFeatureKey]struct{}, len(snapshot.Features))
	for _, feature := range snapshot.Features {
		if _, found := entities[nativeEntityKey{feature.DeviceAddress, feature.EntityAddress}]; !found {
			return errors.New("feature references a missing entity")
		}
		key := nativeFeatureKey{feature.DeviceAddress, feature.EntityAddress, feature.FeatureAddress}
		if _, found := features[key]; found {
			return errors.New("duplicate feature")
		}
		features[key] = struct{}{}
	}
	return validateNativeClaims(snapshot.UseCases, devices, entities, features)
}

func validateNativeEntities(source []NativeEntityV2, devices map[string]struct{}) (map[nativeEntityKey]*string, error) {
	parents := make(map[nativeEntityKey]*string, len(source))
	for _, entity := range source {
		if _, found := devices[entity.DeviceAddress]; !found {
			return nil, errors.New("entity references a missing device")
		}
		key := nativeEntityKey{entity.DeviceAddress, entity.EntityAddress}
		if _, found := parents[key]; found {
			return nil, errors.New("duplicate entity")
		}
		parents[key] = entity.ParentEntityAddress
	}
	for key, parent := range parents {
		if parent == nil {
			continue
		}
		if _, found := parents[nativeEntityKey{key.device, *parent}]; !found || *parent == key.entity {
			return nil, errors.New("entity references a missing parent entity")
		}
	}
	const visiting, visited = 1, 2
	state := make(map[nativeEntityKey]uint8, len(parents))
	for key := range parents {
		var path []nativeEntityKey
		for current := key; state[current] != visited; {
			if state[current] == visiting {
				return nil, errors.New("entity containment has a cycle")
			}
			state[current] = visiting
			path = append(path, current)
			parent := parents[current]
			if parent == nil {
				break
			}
			current = nativeEntityKey{current.device, *parent}
		}
		for _, value := range path {
			state[value] = visited
		}
	}
	return parents, nil
}

func validateNativeClaims(source []NativeUseCaseV2, devices map[string]struct{}, entities map[nativeEntityKey]*string, features map[nativeFeatureKey]struct{}) error {
	claims := make(map[nativeClaimKey]struct{}, len(source))
	for _, claim := range source {
		if !nativeClaimScopeResolves(claim, devices, entities, features) {
			return errors.New("use-case claim references a missing scope")
		}
		key := nativeClaimKey{
			device: claim.DeviceAddress, name: claim.Name, actor: claim.Actor,
			entity: nativeOptional(claim.EntityAddress), feature: nativeOptional(claim.FeatureAddress),
			version: nativeOptional(claim.Version), subrevision: nativeOptional(claim.DocumentSubrevision),
		}
		if _, found := claims[key]; found {
			return errors.New("duplicate use-case claim")
		}
		claims[key] = struct{}{}
	}
	return nil
}

func nativeClaimScopeResolves(claim NativeUseCaseV2, devices map[string]struct{}, entities map[nativeEntityKey]*string, features map[nativeFeatureKey]struct{}) bool {
	if _, found := devices[claim.DeviceAddress]; !found {
		return false
	}
	if claim.EntityAddress == nil {
		return claim.FeatureAddress == nil
	}
	if _, found := entities[nativeEntityKey{claim.DeviceAddress, *claim.EntityAddress}]; !found {
		return false
	}
	if claim.FeatureAddress == nil {
		return true
	}
	_, found := features[nativeFeatureKey{claim.DeviceAddress, *claim.EntityAddress, *claim.FeatureAddress}]
	return found
}

func validateNativeAbsences(absences []NativeAbsence, omitted uint64) error {
	if len(absences) > nativeAbsenceLimit {
		return errors.New("absence records exceed the limit")
	}
	if omitted != 0 && len(absences) != nativeAbsenceLimit {
		return errors.New("absence count is inconsistent")
	}
	for _, absence := range absences {
		_, knownKind := nativeAbsenceKinds[absence.Kind]
		_, knownReason := nativeAbsenceReasons[absence.Reason]
		if !knownKind || !knownReason || !nativeSnapshotV2ValidText(absence.DeviceAddress, nativeSnapshotV2MaximumIdentifierBytes) ||
			!nativeAbsenceValidAddress(absence.EntityAddress) || !nativeAbsenceValidAddress(absence.FeatureAddress) {
			return errors.New("absence record is invalid")
		}
	}
	return nil
}

func nativeAbsenceValidAddress(value *string) bool {
	return value == nil || nativeSnapshotV2ValidText(*value, nativeSnapshotV2MaximumIdentifierBytes)
}
