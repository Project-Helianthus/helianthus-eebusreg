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

const NativeSnapshotContractV2 = "helianthus.eebus.runtime.native-snapshot.v2"

const (
	nativeSnapshotV2MaximumIdentifierBytes = 4096
	nativeSnapshotV2MaximumPathBytes       = 512
	nativeSnapshotV2MaximumSourceBytes     = 128
	nativeSnapshotV2MaximumPayloadBytes    = 65536
)

type NativeSnapshotV2 struct {
	Meta         NativeSnapshotMetaV2         `json:"meta"`
	Status       NativeRuntimeObservationV2   `json:"status"`
	Pairing      []NativePairingObservationV2 `json:"pairing"`
	Services     []NativeServiceV2            `json:"services"`
	Sessions     []NativeSessionV2            `json:"sessions"`
	Devices      []NativeDeviceV2             `json:"devices"`
	Entities     []NativeEntityV2             `json:"entities"`
	Features     []NativeFeatureV2            `json:"features"`
	UseCases     []NativeUseCaseV2            `json:"usecases"`
	Observations []NativeObservationV2        `json:"observations"`
}

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

type NativeRuntimeObservationV2 struct {
	State       string               `json:"state"`
	Degradation *NativeDegradationV2 `json:"degradation,omitempty"`
}

type NativeDegradationV2 struct {
	Reason string    `json:"reason"`
	Since  time.Time `json:"since"`
}

type NativePairingObservationV2 struct {
	RemoteSKI string    `json:"remote_ski"`
	State     string    `json:"state"`
	Since     time.Time `json:"since"`
}

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

type NativeSessionV2 struct {
	ID        string    `json:"id"`
	RemoteSKI string    `json:"remote_ski"`
	State     string    `json:"state"`
	Since     time.Time `json:"since"`
}

type NativeDeviceV2 struct {
	SKI          string                `json:"ski"`
	SHIPID       *string               `json:"ship_id,omitempty"`
	Address      string                `json:"address"`
	Type         string                `json:"type"`
	Description  *string               `json:"description,omitempty"`
	Metadata     map[string]string     `json:"metadata,omitempty"`
	Observations []NativeObservationV2 `json:"observations,omitempty"`
}

type NativeEntityV2 struct {
	DeviceAddress string  `json:"device_address"`
	EntityAddress string  `json:"entity_address"`
	Type          string  `json:"type"`
	Description   *string `json:"description,omitempty"`
}

type NativeFeatureV2 struct {
	DeviceAddress  string  `json:"device_address"`
	EntityAddress  string  `json:"entity_address"`
	FeatureAddress string  `json:"feature_address"`
	Type           string  `json:"type"`
	Role           string  `json:"role"`
	Description    *string `json:"description,omitempty"`
}

type NativeUseCaseV2 struct {
	ContextAddress      string   `json:"context_address"`
	Name                string   `json:"name"`
	Actor               string   `json:"actor"`
	ResolvedRole        *string  `json:"resolved_role,omitempty"`
	Scenarios           []string `json:"scenarios"`
	Version             *string  `json:"version,omitempty"`
	Availability        *bool    `json:"availability,omitempty"`
	DocumentSubrevision *string  `json:"document_subrevision,omitempty"`
}

type NativeObservationV2 struct {
	Path            string        `json:"path"`
	Source          string        `json:"source"`
	ObservedAt      time.Time     `json:"observed_at"`
	ProtocolVersion *string       `json:"protocol_version,omitempty"`
	Value           NativeValueV2 `json:"value"`
}

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

func NewNativeSnapshotV2(draft NativeSnapshotV2) (NativeSnapshotV2, error) {
	snapshot := draft.Clone()
	if err := snapshot.Validate(); err != nil {
		return NativeSnapshotV2{}, err
	}
	return snapshot, nil
}

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
	return nil
}

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
		result.Entities[index].Description = nativeSnapshotV2CloneString(result.Entities[index].Description)
	}
	result.Features = append([]NativeFeatureV2(nil), snapshot.Features...)
	for index := range result.Features {
		result.Features[index].Description = nativeSnapshotV2CloneString(result.Features[index].Description)
	}
	result.UseCases = append([]NativeUseCaseV2(nil), snapshot.UseCases...)
	for index := range result.UseCases {
		result.UseCases[index].ResolvedRole = nativeSnapshotV2CloneString(result.UseCases[index].ResolvedRole)
		result.UseCases[index].Scenarios = append([]string(nil), result.UseCases[index].Scenarios...)
		result.UseCases[index].Version = nativeSnapshotV2CloneString(result.UseCases[index].Version)
		result.UseCases[index].Availability = nativeSnapshotV2CloneBool(result.UseCases[index].Availability)
		result.UseCases[index].DocumentSubrevision = nativeSnapshotV2CloneString(result.UseCases[index].DocumentSubrevision)
	}
	result.Observations = nativeSnapshotV2CloneObservations(snapshot.Observations)
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
