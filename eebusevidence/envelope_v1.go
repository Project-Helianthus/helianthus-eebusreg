package eebusevidence

import (
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

// CaptureProvenanceV1 identifies the source of a version 1 capture.
type CaptureProvenanceV1 string

const (
	// CaptureProvenanceRuntimeObservation selects the runtime-observation value.
	CaptureProvenanceRuntimeObservation CaptureProvenanceV1 = "runtime-observation"
)

// Validate accepts only runtime observation provenance.
func (p CaptureProvenanceV1) Validate() error {
	if p != CaptureProvenanceRuntimeObservation {
		return errors.New("unsupported capture provenance")
	}
	return nil
}

// MarshalJSON validates the value before encoding it as JSON.
func (p CaptureProvenanceV1) MarshalJSON() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(string(p))
}

// String returns the supported value or "invalid-capture-provenance" when validation fails.
func (p CaptureProvenanceV1) String() string {
	if err := p.Validate(); err != nil {
		return "invalid-capture-provenance"
	}
	return string(p)
}

// GoString returns the same safe representation as String.
func (p CaptureProvenanceV1) GoString() string {
	return p.String()
}

// Format writes the safe String representation for any format verb.
func (p CaptureProvenanceV1) Format(s fmt.State, verb rune) {
	io.WriteString(s, p.String())
}

// RawSnapshotScopeV1 identifies the raw snapshot scope represented by version 1.
type RawSnapshotScopeV1 string

const (
	// RawSnapshotScopeRoot selects the raw-root value.
	RawSnapshotScopeRoot RawSnapshotScopeV1 = "raw-root"
	// RawSnapshotScopeIdentity selects the raw-identity value.
	RawSnapshotScopeIdentity RawSnapshotScopeV1 = "raw-identity"
	// RawSnapshotScopeTopology selects the raw-topology value.
	RawSnapshotScopeTopology RawSnapshotScopeV1 = "raw-topology"
	// RawSnapshotScopeServices selects the raw-services value.
	RawSnapshotScopeServices RawSnapshotScopeV1 = "raw-services"
	// RawSnapshotScopeSessions selects the raw-sessions value.
	RawSnapshotScopeSessions RawSnapshotScopeV1 = "raw-sessions"
	// RawSnapshotScopeUnknown selects the raw-unknown value.
	RawSnapshotScopeUnknown RawSnapshotScopeV1 = "raw-unknown"
)

// Validate rejects scopes outside the supported version 1 set.
func (s RawSnapshotScopeV1) Validate() error {
	switch s {
	case RawSnapshotScopeRoot, RawSnapshotScopeIdentity, RawSnapshotScopeTopology, RawSnapshotScopeServices, RawSnapshotScopeSessions, RawSnapshotScopeUnknown:
		return nil
	default:
		return errors.New("unsupported raw snapshot scope")
	}
}

// MarshalJSON validates the value before encoding it as JSON.
func (s RawSnapshotScopeV1) MarshalJSON() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(string(s))
}

// String returns the supported value or "invalid-raw-snapshot-scope" when validation fails.
func (s RawSnapshotScopeV1) String() string {
	if err := s.Validate(); err != nil {
		return "invalid-raw-snapshot-scope"
	}
	return string(s)
}

// GoString returns the same safe representation as String.
func (s RawSnapshotScopeV1) GoString() string {
	return s.String()
}

// Format writes the safe String representation for any format verb.
func (s RawSnapshotScopeV1) Format(state fmt.State, verb rune) {
	io.WriteString(state, s.String())
}

// ReferenceV1 binds a version 1 envelope to a redacted runtime and read context.
type ReferenceV1 struct {
	// Runtime identifies the runtime using a redacted identifier with a required digest.
	Runtime eebusraw.RedactedID `json:"runtime"`
	// Contract selects the envelope contract version.
	Contract ContractVersion `json:"contract"`
	// CaptureProvenance identifies the source of the capture.
	CaptureProvenance CaptureProvenanceV1 `json:"capture_provenance"`
	// Scope limits the represented snapshot or operation.
	Scope RawSnapshotScopeV1 `json:"scope"`
	// MaskTier must be the redacted tier.
	MaskTier eebusraw.MaskTier `json:"mask_tier"`
	// AuthScope records the effective raw-read authorization scope.
	AuthScope AuthScope `json:"auth_scope"`
}

// NewReferenceV1 constructs a version 1 reference with the redacted tier; callers must validate it.
func NewReferenceV1(runtime eebusraw.RedactedID, provenance CaptureProvenanceV1, scope RawSnapshotScopeV1, authScope AuthScope) ReferenceV1 {
	return ReferenceV1{
		Runtime:           runtime,
		Contract:          EnvelopeContractV1,
		CaptureProvenance: provenance,
		Scope:             scope,
		MaskTier:          eebusraw.MaskTierRedacted,
		AuthScope:         authScope,
	}
}

// Validate checks the runtime digest, version 1 contract, provenance, scope, redacted tier and raw-read authorization.
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
	if err := r.CaptureProvenance.Validate(); err != nil {
		return fmt.Errorf("capture provenance: %w", err)
	}
	if err := r.Scope.Validate(); err != nil {
		return fmt.Errorf("scope: %w", err)
	}
	if r.MaskTier != eebusraw.MaskTierRedacted {
		return errors.New("mask tier must be redacted")
	}
	if r.AuthScope != AuthScopeReadRaw {
		return errors.New("auth scope must bind effective raw-read authorization")
	}
	return nil
}

// MarshalJSON validates the value before encoding it as JSON.
func (r ReferenceV1) MarshalJSON() ([]byte, error) {
	type alias ReferenceV1
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(alias(r))
}

// Matches compares every reference field for equality.
func (r ReferenceV1) Matches(other ReferenceV1) bool {
	return r.Runtime == other.Runtime &&
		r.Contract == other.Contract &&
		r.CaptureProvenance == other.CaptureProvenance &&
		r.Scope == other.Scope &&
		r.MaskTier == other.MaskTier &&
		r.AuthScope == other.AuthScope
}

// String returns a fixed redacted representation without exposing fields.
func (r ReferenceV1) String() string {
	return "reference_v1:" + redactedValue
}

// GoString returns the same safe representation as String.
func (r ReferenceV1) GoString() string {
	return r.String()
}

// Format writes the safe String representation for any format verb.
func (r ReferenceV1) Format(s fmt.State, verb rune) {
	io.WriteString(s, r.String())
}

// ObjectV1 holds redacted metadata for a version 1 evidence object.
type ObjectV1 struct {
	// Kind classifies the represented evidence object.
	Kind ObjectKind `json:"kind"`
	// Digest holds the lowercase SHA-256 digest of the object.
	Digest string `json:"digest"`
	// Size records the object size, which must not be negative.
	Size int `json:"size"`
	// DataTimestamp records the time associated with the data.
	DataTimestamp time.Time `json:"data_timestamp"`
	// Unknown preserves unknown fields as opaque values.
	Unknown []eebusraw.UnknownField `json:"unknown,omitempty"`
}

// NewObjectV1 constructs version 1 object metadata with its timestamp converted to UTC; callers must validate it.
func NewObjectV1(kind ObjectKind, digest string, size int, dataTimestamp time.Time) ObjectV1 {
	return ObjectV1{
		Kind:          kind,
		Digest:        digest,
		Size:          size,
		DataTimestamp: dataTimestamp.UTC(),
	}
}

// Validate checks the kind, digest, nonnegative size, JSON-compatible timestamp and unknown fields.
func (o ObjectV1) Validate() error {
	if err := o.Kind.Validate(); err != nil {
		return fmt.Errorf("kind: %w", err)
	}
	if !validSHA256Digest(o.Digest) {
		return errors.New("digest must use lowercase sha256:<64 hex chars>")
	}
	if o.Size < 0 {
		return errors.New("size must not be negative")
	}
	if err := validateTimestampV1(o.DataTimestamp); err != nil {
		return fmt.Errorf("data timestamp: %w", err)
	}
	for i, unknown := range o.Unknown {
		if err := unknown.Validate(); err != nil {
			return fmt.Errorf("unknown field %d: %w", i, err)
		}
		if unknown.Value.Digest != "" && !validSHA256Digest(unknown.Value.Digest) {
			return fmt.Errorf("unknown field %d digest must use lowercase sha256:<64 hex chars>", i)
		}
	}
	return nil
}

// MarshalJSON validates the value before encoding it as JSON.
func (o ObjectV1) MarshalJSON() ([]byte, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(newObjectJSON(objectV1AsObject(o)))
}

// String returns a fixed redacted representation without exposing fields.
func (o ObjectV1) String() string {
	return "object_v1:" + redactedValue
}

// GoString returns the same safe representation as String.
func (o ObjectV1) GoString() string {
	return o.String()
}

// Format writes the safe String representation for any format verb.
func (o ObjectV1) Format(s fmt.State, verb rune) {
	io.WriteString(s, o.String())
}

// EnvelopeV1 holds a version 1 reference and redacted object metadata.
type EnvelopeV1 struct {
	// Reference binds the envelope to its validated context.
	Reference ReferenceV1 `json:"ref"`
	// CapturedAt records when the envelope was captured.
	CapturedAt time.Time `json:"captured_at"`
	// DataTimestamp records the time associated with the data.
	DataTimestamp time.Time `json:"data_timestamp"`
	// Objects contains the represented object metadata.
	Objects []ObjectV1 `json:"objects,omitempty"`
	// DataHash optionally holds the canonical content hash.
	DataHash string `json:"data_hash,omitempty"`
}

// NewEnvelopeV1 constructs a version 1 envelope with UTC timestamps and detached object and unknown-field slices.
func NewEnvelopeV1(ref ReferenceV1, capturedAt time.Time, dataTimestamp time.Time, objects []ObjectV1) EnvelopeV1 {
	return EnvelopeV1{
		Reference:     ref,
		CapturedAt:    capturedAt.UTC(),
		DataTimestamp: dataTimestamp.UTC(),
		Objects:       copyObjectsV1(objects),
	}
}

// Validate checks the reference, JSON-compatible timestamps, objects and any supplied content hash.
func (e EnvelopeV1) Validate() error {
	return e.validate(true)
}

func (e EnvelopeV1) validate(checkDataHash bool) error {
	if err := e.Reference.Validate(); err != nil {
		return fmt.Errorf("ref: %w", err)
	}
	if err := validateTimestampV1(e.CapturedAt); err != nil {
		return fmt.Errorf("captured_at: %w", err)
	}
	if err := validateTimestampV1(e.DataTimestamp); err != nil {
		return fmt.Errorf("data_timestamp: %w", err)
	}
	for i, object := range e.Objects {
		if err := object.Validate(); err != nil {
			return fmt.Errorf("object %d: %w", i, err)
		}
	}
	if e.DataHash != "" && !validSHA256Digest(e.DataHash) {
		return errors.New("data_hash must use lowercase sha256:<64 hex chars>")
	}
	if checkDataHash && e.DataHash != "" {
		expected := e.computeDataHash()
		if e.DataHash != expected {
			return errors.New("data_hash does not match envelope content")
		}
	}
	return nil
}

// ComputeDataHash validates content and hashes the canonical reference, data timestamp and sorted objects; capture time and the existing hash are excluded.
func (e EnvelopeV1) ComputeDataHash() (string, error) {
	if err := e.validate(false); err != nil {
		return "", err
	}
	return e.computeDataHash(), nil
}

func (e EnvelopeV1) computeDataHash() string {
	sum := sha256.Sum256(canonicalHashPayloadV1(e))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// WithDataHash returns a detached object copy with the canonical data hash, or an error if content is invalid.
func (e EnvelopeV1) WithDataHash() (EnvelopeV1, error) {
	hash, err := e.ComputeDataHash()
	if err != nil {
		return EnvelopeV1{}, err
	}
	e.DataHash = hash
	e.Objects = copyObjectsV1(e.Objects)
	return e, nil
}

// MarshalJSON validates the value before encoding it as JSON.
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

// String returns a fixed redacted representation without exposing fields.
func (e EnvelopeV1) String() string {
	return "envelope_v1:" + redactedValue
}

// GoString returns the same safe representation as String.
func (e EnvelopeV1) GoString() string {
	return e.String()
}

// Format writes the safe String representation for any format verb.
func (e EnvelopeV1) Format(s fmt.State, verb rune) {
	io.WriteString(s, e.String())
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

func canonicalHashPayloadV1(e EnvelopeV1) []byte {
	var b strings.Builder
	b.WriteByte('{')
	writeCanonicalFieldName(&b, "data_timestamp")
	writeCanonicalString(&b, canonicalTime(e.DataTimestamp))
	b.WriteByte(',')
	writeCanonicalFieldName(&b, "objects")
	b.WriteByte('[')
	for i, object := range sortedObjectsV1(e.Objects) {
		if i > 0 {
			b.WriteByte(',')
		}
		writeCanonicalObject(&b, objectV1AsObject(object))
	}
	b.WriteByte(']')
	b.WriteByte(',')
	writeCanonicalFieldName(&b, "ref")
	writeCanonicalReferenceV1(&b, e.Reference)
	b.WriteByte('}')
	return []byte(b.String())
}

func writeCanonicalReferenceV1(b *strings.Builder, ref ReferenceV1) {
	b.WriteByte('{')
	writeCanonicalFieldName(b, "auth_scope")
	writeCanonicalString(b, ref.AuthScope.String())
	b.WriteByte(',')
	writeCanonicalFieldName(b, "capture_provenance")
	writeCanonicalString(b, ref.CaptureProvenance.String())
	b.WriteByte(',')
	writeCanonicalFieldName(b, "contract")
	writeCanonicalString(b, ref.Contract.String())
	b.WriteByte(',')
	writeCanonicalFieldName(b, "mask_tier")
	writeCanonicalString(b, string(ref.MaskTier))
	b.WriteByte(',')
	writeCanonicalFieldName(b, "runtime")
	writeCanonicalRedactedID(b, ref.Runtime)
	b.WriteByte(',')
	writeCanonicalFieldName(b, "scope")
	writeCanonicalString(b, ref.Scope.String())
	b.WriteByte('}')
}

func validateTimestampV1(value time.Time) error {
	if value.IsZero() {
		return errors.New("timestamp is required")
	}
	if _, err := value.UTC().MarshalJSON(); err != nil {
		return errors.New("timestamp must marshal as RFC3339 JSON")
	}
	return nil
}
