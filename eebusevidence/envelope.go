package eebusevidence

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

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

// ContractVersion identifies a supported evidence envelope contract.
type ContractVersion string

const (
	// EnvelopeContractV1Alpha1 selects the helianthus.eebus.raw.evidence-envelope.v1alpha1 value.
	EnvelopeContractV1Alpha1 ContractVersion = "helianthus.eebus.raw.evidence-envelope.v1alpha1"
	// EnvelopeContractV1 selects the helianthus.eebus.raw.evidence-envelope.v1 value.
	EnvelopeContractV1 ContractVersion = "helianthus.eebus.raw.evidence-envelope.v1"
)

// Validate accepts only the alpha and version 1 contract identifiers.
func (c ContractVersion) Validate() error {
	if c != EnvelopeContractV1Alpha1 && c != EnvelopeContractV1 {
		return errors.New("unsupported evidence contract")
	}
	return nil
}

// MarshalJSON validates the value before encoding it as JSON.
func (c ContractVersion) MarshalJSON() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(string(c))
}

// String returns the supported value or "invalid-contract" when validation fails.
func (c ContractVersion) String() string {
	if err := c.Validate(); err != nil {
		return "invalid-contract"
	}
	return string(c)
}

// GoString returns the same safe representation as String.
func (c ContractVersion) GoString() string {
	return c.String()
}

// Format writes the safe String representation for any format verb.
func (c ContractVersion) Format(s fmt.State, verb rune) {
	io.WriteString(s, c.String())
}

// ToolID identifies an allowed evidence tool.
type ToolID string

const (
	// ToolRuntimeStatus selects the eebus.v1.runtime.status.get value.
	ToolRuntimeStatus ToolID = "eebus.v1.runtime.status.get"
	// ToolServicesList selects the eebus.v1.services.list value.
	ToolServicesList ToolID = "eebus.v1.services.list"
	// ToolServicesGet selects the eebus.v1.services.get value.
	ToolServicesGet ToolID = "eebus.v1.services.get"
	// ToolSessionsList selects the eebus.v1.sessions.list value.
	ToolSessionsList ToolID = "eebus.v1.sessions.list"
	// ToolSessionsGet selects the eebus.v1.sessions.get value.
	ToolSessionsGet ToolID = "eebus.v1.sessions.get"
	// ToolTopologyGet selects the eebus.v1.topology.get value.
	ToolTopologyGet ToolID = "eebus.v1.topology.get"
	// ToolCapture selects the eebus.v1.snapshot.capture value.
	ToolCapture ToolID = "eebus.v1.snapshot.capture"
	// ToolPairingStatus selects the eebus.v1.pairing.status.get value.
	ToolPairingStatus ToolID = "eebus.v1.pairing.status.get"
)

// Validate rejects tool identifiers outside the supported set.
func (t ToolID) Validate() error {
	switch t {
	case ToolRuntimeStatus, ToolServicesList, ToolServicesGet, ToolSessionsList, ToolSessionsGet, ToolTopologyGet, ToolCapture, ToolPairingStatus:
		return nil
	default:
		return errors.New("unsupported tool id")
	}
}

// MarshalJSON validates the value before encoding it as JSON.
func (t ToolID) MarshalJSON() ([]byte, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(string(t))
}

// String returns the supported value or "invalid-tool" when validation fails.
func (t ToolID) String() string {
	if err := t.Validate(); err != nil {
		return "invalid-tool"
	}
	return string(t)
}

// GoString returns the same safe representation as String.
func (t ToolID) GoString() string {
	return t.String()
}

// Format writes the safe String representation for any format verb.
func (t ToolID) Format(s fmt.State, verb rune) {
	io.WriteString(s, t.String())
}

// Scope identifies an allowed alpha envelope scope.
type Scope string

const (
	// ScopeWholeRoot selects the whole-root value.
	ScopeWholeRoot Scope = "whole-root"
	// ScopeRuntimeStatus selects the runtime-status value.
	ScopeRuntimeStatus Scope = "runtime-status"
	// ScopeServices selects the services value.
	ScopeServices Scope = "services"
	// ScopeService selects the service value.
	ScopeService Scope = "service"
	// ScopeSessions selects the sessions value.
	ScopeSessions Scope = "sessions"
	// ScopeSession selects the session value.
	ScopeSession Scope = "session"
	// ScopeTopology selects the topology value.
	ScopeTopology Scope = "topology"
	// ScopePairingStatus selects the pairing-status value.
	ScopePairingStatus Scope = "pairing-status"
)

// Validate rejects scopes outside the supported alpha set.
func (s Scope) Validate() error {
	switch s {
	case ScopeWholeRoot, ScopeRuntimeStatus, ScopeServices, ScopeService, ScopeSessions, ScopeSession, ScopeTopology, ScopePairingStatus:
		return nil
	default:
		return errors.New("unsupported scope")
	}
}

// MarshalJSON validates the value before encoding it as JSON.
func (s Scope) MarshalJSON() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(string(s))
}

// String returns the supported value or "invalid-scope" when validation fails.
func (s Scope) String() string {
	if err := s.Validate(); err != nil {
		return "invalid-scope"
	}
	return string(s)
}

// GoString returns the same safe representation as String.
func (s Scope) GoString() string {
	return s.String()
}

// Format writes the safe String representation for any format verb.
func (s Scope) Format(st fmt.State, verb rune) {
	io.WriteString(st, s.String())
}

// AuthScope identifies the authorization scope bound to evidence.
type AuthScope string

const (
	// AuthScopeReadRaw selects the eebus.raw.read value.
	AuthScopeReadRaw AuthScope = "eebus.raw.read"
)

// Validate accepts only the raw-read scope.
func (a AuthScope) Validate() error {
	if a != AuthScopeReadRaw {
		return errors.New("unsupported auth scope")
	}
	return nil
}

// MarshalJSON validates the value before encoding it as JSON.
func (a AuthScope) MarshalJSON() ([]byte, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(string(a))
}

// String returns the supported value or "invalid-auth-scope" when validation fails.
func (a AuthScope) String() string {
	if err := a.Validate(); err != nil {
		return "invalid-auth-scope"
	}
	return string(a)
}

// GoString returns the same safe representation as String.
func (a AuthScope) GoString() string {
	return a.String()
}

// Format writes the safe String representation for any format verb.
func (a AuthScope) Format(s fmt.State, verb rune) {
	io.WriteString(s, a.String())
}

// ObjectKind classifies an evidence object.
type ObjectKind string

const (
	// ObjectKindIdentity selects the identity value.
	ObjectKindIdentity ObjectKind = "identity"
	// ObjectKindTopology selects the topology value.
	ObjectKindTopology ObjectKind = "topology"
	// ObjectKindService selects the service value.
	ObjectKindService ObjectKind = "service"
	// ObjectKindSession selects the session value.
	ObjectKindSession ObjectKind = "session"
	// ObjectKindUnknown selects the unknown value.
	ObjectKindUnknown ObjectKind = "unknown"
)

// Validate rejects kinds outside the supported set.
func (k ObjectKind) Validate() error {
	switch k {
	case ObjectKindIdentity, ObjectKindTopology, ObjectKindService, ObjectKindSession, ObjectKindUnknown:
		return nil
	default:
		return errors.New("unsupported object kind")
	}
}

// MarshalJSON validates the value before encoding it as JSON.
func (k ObjectKind) MarshalJSON() ([]byte, error) {
	if err := k.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(string(k))
}

// String returns the supported value or "invalid-object-kind" when validation fails.
func (k ObjectKind) String() string {
	if err := k.Validate(); err != nil {
		return "invalid-object-kind"
	}
	return string(k)
}

// GoString returns the same safe representation as String.
func (k ObjectKind) GoString() string {
	return k.String()
}

// Format writes the safe String representation for any format verb.
func (k ObjectKind) Format(s fmt.State, verb rune) {
	io.WriteString(s, k.String())
}

// Reference binds an alpha envelope to a redacted runtime and read context.
type Reference struct {
	// Runtime identifies the runtime using a redacted identifier with a required digest.
	Runtime eebusraw.RedactedID `json:"runtime"`
	// Contract selects the envelope contract version.
	Contract ContractVersion `json:"contract"`
	// Tool identifies the tool bound to this reference.
	Tool ToolID `json:"tool"`
	// Scope limits the represented snapshot or operation.
	Scope Scope `json:"scope"`
	// MaskTier must be the redacted tier.
	MaskTier eebusraw.MaskTier `json:"mask_tier"`
	// AuthScope records the effective raw-read authorization scope.
	AuthScope AuthScope `json:"auth_scope"`
}

// NewReference constructs an alpha reference with the redacted tier; callers must validate it.
func NewReference(runtime eebusraw.RedactedID, tool ToolID, scope Scope, authScope AuthScope) Reference {
	return Reference{
		Runtime:   runtime,
		Contract:  EnvelopeContractV1Alpha1,
		Tool:      tool,
		Scope:     scope,
		MaskTier:  eebusraw.MaskTierRedacted,
		AuthScope: authScope,
	}
}

// Validate checks the runtime digest, alpha contract, tool, scope, redacted tier and authorization.
func (r Reference) Validate() error {
	if err := r.Runtime.Validate(); err != nil {
		return fmt.Errorf("runtime: %w", err)
	}
	if r.Runtime.Digest == "" {
		return errors.New("runtime digest is required")
	}
	if !validSHA256Digest(r.Runtime.Digest) {
		return errors.New("runtime digest must use lowercase sha256:<64 hex chars>")
	}
	if r.Contract != EnvelopeContractV1Alpha1 {
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

// MarshalJSON validates the value before encoding it as JSON.
func (r Reference) MarshalJSON() ([]byte, error) {
	type alias Reference
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(alias(r))
}

// Matches compares every reference field for equality.
func (r Reference) Matches(other Reference) bool {
	return r.Runtime == other.Runtime &&
		r.Contract == other.Contract &&
		r.Tool == other.Tool &&
		r.Scope == other.Scope &&
		r.MaskTier == other.MaskTier &&
		r.AuthScope == other.AuthScope
}

// String returns a fixed redacted representation without exposing fields.
func (r Reference) String() string {
	return "reference:" + redactedValue
}

// GoString returns the same safe representation as String.
func (r Reference) GoString() string {
	return r.String()
}

// Format writes the safe String representation for any format verb.
func (r Reference) Format(s fmt.State, verb rune) {
	io.WriteString(s, r.String())
}

// Object holds redacted metadata for an alpha evidence object.
type Object struct {
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

// NewObject constructs object metadata with its timestamp converted to UTC; callers must validate it.
func NewObject(kind ObjectKind, digest string, size int, dataTimestamp time.Time) Object {
	return Object{
		Kind:          kind,
		Digest:        digest,
		Size:          size,
		DataTimestamp: dataTimestamp.UTC(),
	}
}

// DigestBytes returns the lowercase SHA-256 digest of raw bytes with the algorithm prefix.
func DigestBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Validate checks the kind, digest, nonnegative size, nonzero timestamp and unknown fields.
func (o Object) Validate() error {
	if err := o.Kind.Validate(); err != nil {
		return fmt.Errorf("kind: %w", err)
	}
	if !validSHA256Digest(o.Digest) {
		return errors.New("digest must use sha256:<64 hex chars>")
	}
	if o.Size < 0 {
		return errors.New("size must not be negative")
	}
	if o.DataTimestamp.IsZero() {
		return errors.New("data timestamp is required")
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
func (o Object) MarshalJSON() ([]byte, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(newObjectJSON(o))
}

// String returns a fixed redacted representation without exposing fields.
func (o Object) String() string {
	return "object:" + redactedValue
}

// GoString returns the same safe representation as String.
func (o Object) GoString() string {
	return o.String()
}

// Format writes the safe String representation for any format verb.
func (o Object) Format(s fmt.State, verb rune) {
	io.WriteString(s, o.String())
}

// Envelope holds an alpha reference and redacted object metadata.
type Envelope struct {
	// Reference binds the envelope to its validated context.
	Reference Reference `json:"ref"`
	// CapturedAt records when the envelope was captured.
	CapturedAt time.Time `json:"captured_at"`
	// DataTimestamp records the time associated with the data.
	DataTimestamp time.Time `json:"data_timestamp"`
	// Objects contains the represented object metadata.
	Objects []Object `json:"objects,omitempty"`
	// DataHash optionally holds the canonical content hash.
	DataHash string `json:"data_hash,omitempty"`
}

// NewEnvelope constructs an envelope with UTC timestamps and detached object and unknown-field slices.
func NewEnvelope(ref Reference, capturedAt time.Time, dataTimestamp time.Time, objects []Object) Envelope {
	return Envelope{
		Reference:     ref,
		CapturedAt:    capturedAt.UTC(),
		DataTimestamp: dataTimestamp.UTC(),
		Objects:       copyObjects(objects),
	}
}

// Validate checks the reference, nonzero timestamps, objects and any supplied content hash.
func (e Envelope) Validate() error {
	return e.validate(true)
}

func (e Envelope) validate(checkDataHash bool) error {
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

// ComputeDataHash validates content and hashes the canonical reference, data timestamp and sorted objects; capture time and the existing hash are excluded.
func (e Envelope) ComputeDataHash() (string, error) {
	if err := e.validate(false); err != nil {
		return "", err
	}
	return e.computeDataHash(), nil
}

func (e Envelope) computeDataHash() string {
	payload := canonicalHashPayload(e)
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// WithDataHash returns a detached object copy with the canonical data hash, or an error if content is invalid.
func (e Envelope) WithDataHash() (Envelope, error) {
	hash, err := e.ComputeDataHash()
	if err != nil {
		return Envelope{}, err
	}
	e.DataHash = hash
	e.Objects = copyObjects(e.Objects)
	return e, nil
}

// MarshalJSON validates the value before encoding it as JSON.
func (e Envelope) MarshalJSON() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	objects := sortedObjects(e.Objects)
	type envelopeJSON struct {
		Reference     Reference `json:"ref"`
		CapturedAt    time.Time `json:"captured_at"`
		DataTimestamp time.Time `json:"data_timestamp"`
		Objects       []Object  `json:"objects,omitempty"`
		DataHash      string    `json:"data_hash,omitempty"`
	}
	return json.Marshal(envelopeJSON{
		Reference:     e.Reference,
		CapturedAt:    e.CapturedAt.UTC(),
		DataTimestamp: e.DataTimestamp.UTC(),
		Objects:       objects,
		DataHash:      e.DataHash,
	})
}

// String returns a fixed redacted representation without exposing fields.
func (e Envelope) String() string {
	return "envelope:" + redactedValue
}

// GoString returns the same safe representation as String.
func (e Envelope) GoString() string {
	return e.String()
}

// Format writes the safe String representation for any format verb.
func (e Envelope) Format(s fmt.State, verb rune) {
	io.WriteString(s, e.String())
}

const redactedValue = "[redacted]"

type objectJSON struct {
	Kind          ObjectKind              `json:"kind"`
	Digest        string                  `json:"digest"`
	Size          int                     `json:"size"`
	DataTimestamp time.Time               `json:"data_timestamp"`
	Unknown       []eebusraw.UnknownField `json:"unknown,omitempty"`
}

func newObjectJSON(o Object) objectJSON {
	return objectJSON{
		Kind:          o.Kind,
		Digest:        o.Digest,
		Size:          o.Size,
		DataTimestamp: o.DataTimestamp.UTC(),
		Unknown:       sortedUnknownFields(o.Unknown),
	}
}

func sortedObjects(objects []Object) []Object {
	sorted := copyObjects(objects)
	sort.SliceStable(sorted, func(i, j int) bool {
		left := sorted[i]
		right := sorted[j]
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

func copyObjects(objects []Object) []Object {
	copied := make([]Object, len(objects))
	for i, object := range objects {
		copied[i] = object
		copied[i].DataTimestamp = object.DataTimestamp.UTC()
		copied[i].Unknown = sortedUnknownFields(object.Unknown)
	}
	return copied
}

func canonicalHashPayload(e Envelope) []byte {
	var b strings.Builder
	b.WriteByte('{')
	writeCanonicalFieldName(&b, "data_timestamp")
	writeCanonicalString(&b, canonicalTime(e.DataTimestamp))
	b.WriteByte(',')
	writeCanonicalFieldName(&b, "objects")
	b.WriteByte('[')
	for i, object := range sortedObjects(e.Objects) {
		if i > 0 {
			b.WriteByte(',')
		}
		writeCanonicalObject(&b, object)
	}
	b.WriteByte(']')
	b.WriteByte(',')
	writeCanonicalFieldName(&b, "ref")
	writeCanonicalReference(&b, e.Reference)
	b.WriteByte('}')
	return []byte(b.String())
}

func writeCanonicalReference(b *strings.Builder, ref Reference) {
	b.WriteByte('{')
	writeCanonicalFieldName(b, "auth_scope")
	writeCanonicalString(b, ref.AuthScope.String())
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
	b.WriteByte(',')
	writeCanonicalFieldName(b, "tool")
	writeCanonicalString(b, ref.Tool.String())
	b.WriteByte('}')
}

func writeCanonicalRedactedID(b *strings.Builder, id eebusraw.RedactedID) {
	b.WriteByte('{')
	if id.Digest != "" {
		writeCanonicalFieldName(b, "digest")
		writeCanonicalString(b, id.Digest)
		b.WriteByte(',')
	}
	writeCanonicalFieldName(b, "kind")
	writeCanonicalString(b, id.Kind.String())
	b.WriteByte(',')
	writeCanonicalFieldName(b, "masked")
	writeCanonicalString(b, id.Masked)
	b.WriteByte('}')
}

func writeCanonicalObject(b *strings.Builder, object Object) {
	b.WriteByte('{')
	writeCanonicalFieldName(b, "data_timestamp")
	writeCanonicalString(b, canonicalTime(object.DataTimestamp))
	b.WriteByte(',')
	writeCanonicalFieldName(b, "digest")
	writeCanonicalString(b, object.Digest)
	b.WriteByte(',')
	writeCanonicalFieldName(b, "kind")
	writeCanonicalString(b, object.Kind.String())
	b.WriteByte(',')
	writeCanonicalFieldName(b, "size")
	b.WriteString(strconv.Itoa(object.Size))
	if len(object.Unknown) > 0 {
		b.WriteByte(',')
		writeCanonicalFieldName(b, "unknown")
		writeCanonicalUnknownFields(b, object.Unknown)
	}
	b.WriteByte('}')
}

func writeCanonicalUnknownFields(b *strings.Builder, unknown []eebusraw.UnknownField) {
	sorted := sortedUnknownFields(unknown)
	b.WriteByte('[')
	for i, field := range sorted {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('{')
		writeCanonicalFieldName(b, "path")
		writeCanonicalString(b, field.Path.String())
		b.WriteByte(',')
		writeCanonicalFieldName(b, "value")
		writeCanonicalOpaqueValue(b, field.Value)
		b.WriteByte('}')
	}
	b.WriteByte(']')
}

func canonicalUnknownFields(unknown []eebusraw.UnknownField) string {
	var b strings.Builder
	writeCanonicalUnknownFields(&b, unknown)
	return b.String()
}

func sortedUnknownFields(unknown []eebusraw.UnknownField) []eebusraw.UnknownField {
	sorted := append([]eebusraw.UnknownField(nil), unknown...)
	sort.SliceStable(sorted, func(i, j int) bool {
		left := sorted[i]
		right := sorted[j]
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
	return sorted
}

func writeCanonicalOpaqueValue(b *strings.Builder, value eebusraw.OpaqueValue) {
	b.WriteByte('{')
	wrote := false
	if value.Digest != "" {
		writeCanonicalFieldName(b, "digest")
		writeCanonicalString(b, value.Digest)
		wrote = true
	}
	if wrote {
		b.WriteByte(',')
	}
	writeCanonicalFieldName(b, "masked")
	writeCanonicalString(b, value.Masked)
	wrote = true
	if value.Size != 0 {
		if wrote {
			b.WriteByte(',')
		}
		writeCanonicalFieldName(b, "size")
		b.WriteString(strconv.Itoa(value.Size))
	}
	b.WriteByte('}')
}

func writeCanonicalFieldName(b *strings.Builder, name string) {
	writeCanonicalString(b, name)
	b.WriteByte(':')
}

func writeCanonicalString(b *strings.Builder, value string) {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	b.Write(encoded)
}

func canonicalTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func validSHA256Digest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}
