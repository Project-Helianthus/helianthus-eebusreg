package eebusevidence

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

type ReferenceV1 struct {
	Runtime   eebusraw.RedactedID `json:"runtime"`
	Contract  ContractVersion     `json:"contract"`
	Tool      ToolID              `json:"tool"`
	Scope     Scope               `json:"scope"`
	MaskTier  eebusraw.MaskTier   `json:"mask_tier"`
	AuthScope AuthScope           `json:"auth_scope"`
}

func NewReferenceV1(runtime eebusraw.RedactedID, tool ToolID, scope Scope, authScope AuthScope) ReferenceV1 {
	return ReferenceV1{
		Runtime:   runtime,
		Contract:  EnvelopeContractV1,
		Tool:      tool,
		Scope:     scope,
		MaskTier:  eebusraw.MaskTierRedacted,
		AuthScope: authScope,
	}
}

func (r ReferenceV1) Validate() error {
	if err := r.Runtime.Validate(); err != nil {
		return fmt.Errorf("runtime: %w", err)
	}
	if r.Runtime.Digest == "" {
		return errors.New("runtime digest is required")
	}
	if !validSHA256Digest(r.Runtime.Digest) {
		return errors.New("runtime digest must use lowercase sha256:<64 hex chars>")
	}
	if r.Contract != EnvelopeContractV1 {
		return errors.New("contract: unsupported evidence contract")
	}
	if err := r.Tool.Validate(); err != nil {
		return fmt.Errorf("tool: %w", err)
	}
	if err := r.Scope.Validate(); err != nil {
		return fmt.Errorf("scope: %w", err)
	}
	if r.MaskTier != eebusraw.MaskTierRedacted {
		return errors.New("mask tier must be redacted")
	}
	if err := r.AuthScope.Validate(); err != nil {
		return fmt.Errorf("auth scope: %w", err)
	}
	return nil
}

func (r ReferenceV1) MarshalJSON() ([]byte, error) {
	type alias ReferenceV1
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(alias(r))
}

func (r ReferenceV1) Matches(other ReferenceV1) bool {
	return r.Runtime == other.Runtime &&
		r.Contract == other.Contract &&
		r.Tool == other.Tool &&
		r.Scope == other.Scope &&
		r.MaskTier == other.MaskTier &&
		r.AuthScope == other.AuthScope
}

func (r ReferenceV1) String() string {
	return "reference_v1:" + redactedValue
}

func (r ReferenceV1) GoString() string {
	return r.String()
}

func (r ReferenceV1) Format(s fmt.State, verb rune) {
	io.WriteString(s, r.String())
}

type ObjectV1 struct {
	Kind          ObjectKind              `json:"kind"`
	Digest        string                  `json:"digest"`
	Size          int                     `json:"size"`
	DataTimestamp time.Time               `json:"data_timestamp"`
	Unknown       []eebusraw.UnknownField `json:"unknown,omitempty"`
}

func NewObjectV1(kind ObjectKind, digest string, size int, dataTimestamp time.Time) ObjectV1 {
	return ObjectV1{
		Kind:          kind,
		Digest:        digest,
		Size:          size,
		DataTimestamp: dataTimestamp.UTC(),
	}
}

func (o ObjectV1) Validate() error {
	return objectV1AsObject(o).Validate()
}

func (o ObjectV1) MarshalJSON() ([]byte, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(newObjectJSON(objectV1AsObject(o)))
}

func (o ObjectV1) String() string {
	return "object_v1:" + redactedValue
}

func (o ObjectV1) GoString() string {
	return o.String()
}

func (o ObjectV1) Format(s fmt.State, verb rune) {
	io.WriteString(s, o.String())
}

type EnvelopeV1 struct {
	Reference     ReferenceV1 `json:"ref"`
	CapturedAt    time.Time   `json:"captured_at"`
	DataTimestamp time.Time   `json:"data_timestamp"`
	Objects       []ObjectV1  `json:"objects,omitempty"`
	DataHash      string      `json:"data_hash,omitempty"`
}

func NewEnvelopeV1(ref ReferenceV1, capturedAt time.Time, dataTimestamp time.Time, objects []ObjectV1) EnvelopeV1 {
	return EnvelopeV1{
		Reference:     ref,
		CapturedAt:    capturedAt.UTC(),
		DataTimestamp: dataTimestamp.UTC(),
		Objects:       copyObjectsV1(objects),
	}
}

func (e EnvelopeV1) Validate() error {
	return e.validate(true)
}

func (e EnvelopeV1) validate(checkDataHash bool) error {
	if err := e.Reference.Validate(); err != nil {
		return fmt.Errorf("ref: %w", err)
	}
	if e.CapturedAt.IsZero() {
		return errors.New("captured_at is required")
	}
	if e.DataTimestamp.IsZero() {
		return errors.New("data_timestamp is required")
	}
	for i, object := range e.Objects {
		if err := object.Validate(); err != nil {
			return fmt.Errorf("object %d: %w", i, err)
		}
	}
	if e.DataHash != "" && !validSHA256Digest(e.DataHash) {
		return errors.New("data_hash must use sha256:<64 hex chars>")
	}
	if checkDataHash && e.DataHash != "" {
		expected := e.computeDataHash()
		if e.DataHash != expected {
			return errors.New("data_hash does not match envelope content")
		}
	}
	return nil
}

func (e EnvelopeV1) ComputeDataHash() (string, error) {
	if err := e.validate(false); err != nil {
		return "", err
	}
	return e.computeDataHash(), nil
}

func (e EnvelopeV1) computeDataHash() string {
	return Envelope{
		Reference:     referenceV1AsReference(e.Reference),
		DataTimestamp: e.DataTimestamp,
		Objects:       objectsV1AsObjects(e.Objects),
	}.computeDataHash()
}

func (e EnvelopeV1) WithDataHash() (EnvelopeV1, error) {
	hash, err := e.ComputeDataHash()
	if err != nil {
		return EnvelopeV1{}, err
	}
	e.DataHash = hash
	e.Objects = copyObjectsV1(e.Objects)
	return e, nil
}

func (e EnvelopeV1) MarshalJSON() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	type envelopeV1JSON struct {
		Reference     ReferenceV1 `json:"ref"`
		CapturedAt    time.Time   `json:"captured_at"`
		DataTimestamp time.Time   `json:"data_timestamp"`
		Objects       []ObjectV1  `json:"objects,omitempty"`
		DataHash      string      `json:"data_hash,omitempty"`
	}
	return json.Marshal(envelopeV1JSON{
		Reference:     e.Reference,
		CapturedAt:    e.CapturedAt.UTC(),
		DataTimestamp: e.DataTimestamp.UTC(),
		Objects:       sortedObjectsV1(e.Objects),
		DataHash:      e.DataHash,
	})
}

func (e EnvelopeV1) String() string {
	return "envelope_v1:" + redactedValue
}

func (e EnvelopeV1) GoString() string {
	return e.String()
}

func (e EnvelopeV1) Format(s fmt.State, verb rune) {
	io.WriteString(s, e.String())
}

func referenceV1AsReference(ref ReferenceV1) Reference {
	return Reference{
		Runtime:   ref.Runtime,
		Contract:  ref.Contract,
		Tool:      ref.Tool,
		Scope:     ref.Scope,
		MaskTier:  ref.MaskTier,
		AuthScope: ref.AuthScope,
	}
}

func objectV1AsObject(object ObjectV1) Object {
	return Object{
		Kind:          object.Kind,
		Digest:        object.Digest,
		Size:          object.Size,
		DataTimestamp: object.DataTimestamp,
		Unknown:       object.Unknown,
	}
}

func objectsV1AsObjects(objects []ObjectV1) []Object {
	converted := make([]Object, len(objects))
	for i, object := range objects {
		converted[i] = objectV1AsObject(object)
	}
	return converted
}

func sortedObjectsV1(objects []ObjectV1) []ObjectV1 {
	sorted := copyObjectsV1(objects)
	sort.SliceStable(sorted, func(i, j int) bool {
		left := objectV1AsObject(sorted[i])
		right := objectV1AsObject(sorted[j])
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		if left.Digest != right.Digest {
			return left.Digest < right.Digest
		}
		leftTime := canonicalTime(left.DataTimestamp)
		rightTime := canonicalTime(right.DataTimestamp)
		if leftTime != rightTime {
			return leftTime < rightTime
		}
		if left.Size != right.Size {
			return left.Size < right.Size
		}
		return canonicalUnknownFields(left.Unknown) < canonicalUnknownFields(right.Unknown)
	})
	return sorted
}

func copyObjectsV1(objects []ObjectV1) []ObjectV1 {
	copied := make([]ObjectV1, len(objects))
	for i, object := range objects {
		copied[i] = object
		copied[i].DataTimestamp = object.DataTimestamp.UTC()
		copied[i].Unknown = sortedUnknownFields(object.Unknown)
	}
	return copied
}
