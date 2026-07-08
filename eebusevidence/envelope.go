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

// ContractVersion identifies a reviewable raw evidence contract shape.
type ContractVersion string

// EnvelopeContractV1Alpha1 is the first raw evidence envelope contract.
const EnvelopeContractV1Alpha1 ContractVersion = "helianthus.eebus.raw.evidence-envelope.v1alpha1"

// Validate rejects unsupported evidence envelope contracts.
func (c ContractVersion) Validate() error {
	if c != EnvelopeContractV1Alpha1 {
		return errors.New("unsupported evidence contract")
	}
	return nil
}

// MarshalJSON validates the contract before exposing it as JSON.
func (c ContractVersion) MarshalJSON() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(string(c))
}

// String returns a safe display value.
func (c ContractVersion) String() string {
	if err := c.Validate(); err != nil {
		return "invalid-contract"
	}
	return string(c)
}

// GoString returns a safe display value for %#v.
func (c ContractVersion) GoString() string {
	return c.String()
}

// Format writes a safe display value for logging-style formatting.
func (c ContractVersion) Format(s fmt.State, verb rune) {
	io.WriteString(s, c.String())
}

// ToolID identifies the raw MCP tool or equivalent read scope that created a
// reference.
type ToolID string

const (
	ToolRuntimeStatus ToolID = "eebus.v1.runtime.status.get"
	ToolServicesList  ToolID = "eebus.v1.services.list"
	ToolServicesGet   ToolID = "eebus.v1.services.get"
	ToolSessionsList  ToolID = "eebus.v1.sessions.list"
	ToolSessionsGet   ToolID = "eebus.v1.sessions.get"
	ToolTopologyGet   ToolID = "eebus.v1.topology.get"
	ToolCapture       ToolID = "eebus.v1.snapshot.capture"
	ToolPairingStatus ToolID = "eebus.v1.pairing.status.get"
)

// Validate rejects caller-controlled tool labels.
func (t ToolID) Validate() error {
	switch t {
	case ToolRuntimeStatus, ToolServicesList, ToolServicesGet, ToolSessionsList, ToolSessionsGet, ToolTopologyGet, ToolCapture, ToolPairingStatus:
		return nil
	default:
		return errors.New("unsupported tool id")
	}
}

// MarshalJSON validates the tool id before exposing it as JSON.
func (t ToolID) MarshalJSON() ([]byte, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(string(t))
}

// String returns a safe display value.
func (t ToolID) String() string {
	if err := t.Validate(); err != nil {
		return "invalid-tool"
	}
	return string(t)
}

// GoString returns a safe display value for %#v.
func (t ToolID) GoString() string {
	return t.String()
}

// Format writes a safe display value for logging-style formatting.
func (t ToolID) Format(s fmt.State, verb rune) {
	io.WriteString(s, t.String())
}

// Scope identifies the static raw scope covered by an envelope.
type Scope string

const (
	ScopeWholeRoot     Scope = "whole-root"
	ScopeRuntimeStatus Scope = "runtime-status"
	ScopeServices      Scope = "services"
	ScopeService       Scope = "service"
	ScopeSessions      Scope = "sessions"
	ScopeSession       Scope = "session"
	ScopeTopology      Scope = "topology"
	ScopePairingStatus Scope = "pairing-status"
)

// Validate rejects caller-controlled scope labels.
func (s Scope) Validate() error {
	switch s {
	case ScopeWholeRoot, ScopeRuntimeStatus, ScopeServices, ScopeService, ScopeSessions, ScopeSession, ScopeTopology, ScopePairingStatus:
		return nil
	default:
		return errors.New("unsupported scope")
	}
}

// MarshalJSON validates the scope before exposing it as JSON.
func (s Scope) MarshalJSON() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(string(s))
}

// String returns a safe display value.
func (s Scope) String() string {
	if err := s.Validate(); err != nil {
		return "invalid-scope"
	}
	return string(s)
}

// GoString returns a safe display value for %#v.
func (s Scope) GoString() string {
	return s.String()
}

// Format writes a safe display value for logging-style formatting.
func (s Scope) Format(st fmt.State, verb rune) {
	io.WriteString(st, s.String())
}

// AuthScope identifies the effective authorization scope used at capture time.
type AuthScope string

const (
	AuthScopeReadRaw AuthScope = "eebus.raw.read"
)

// Validate rejects caller-controlled authorization labels.
func (a AuthScope) Validate() error {
	if a != AuthScopeReadRaw {
		return errors.New("unsupported auth scope")
	}
	return nil
}

// MarshalJSON validates the authorization scope before exposing it as JSON.
func (a AuthScope) MarshalJSON() ([]byte, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(string(a))
}

// String returns a safe display value.
func (a AuthScope) String() string {
	if err := a.Validate(); err != nil {
		return "invalid-auth-scope"
	}
	return string(a)
}

// GoString returns a safe display value for %#v.
func (a AuthScope) GoString() string {
	return a.String()
}

// Format writes a safe display value for logging-style formatting.
func (a AuthScope) Format(s fmt.State, verb rune) {
	io.WriteString(s, a.String())
}

// ObjectKind identifies the category of immutable evidence descriptor.
type ObjectKind string

const (
	ObjectKindIdentity ObjectKind = "identity"
	ObjectKindTopology ObjectKind = "topology"
	ObjectKindService  ObjectKind = "service"
	ObjectKindSession  ObjectKind = "session"
	ObjectKindUnknown  ObjectKind = "unknown"
)

// Validate rejects caller-controlled object-kind labels.
func (k ObjectKind) Validate() error {
	switch k {
	case ObjectKindIdentity, ObjectKindTopology, ObjectKindService, ObjectKindSession, ObjectKindUnknown:
		return nil
	default:
		return errors.New("unsupported object kind")
	}
}

// MarshalJSON validates the object kind before exposing it as JSON.
func (k ObjectKind) MarshalJSON() ([]byte, error) {
	if err := k.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(string(k))
}

// String returns a safe display value.
func (k ObjectKind) String() string {
	if err := k.Validate(); err != nil {
		return "invalid-object-kind"
	}
	return string(k)
}

// GoString returns a safe display value for %#v.
func (k ObjectKind) GoString() string {
	return k.String()
}

// Format writes a safe display value for logging-style formatting.
func (k ObjectKind) Format(s fmt.State, verb rune) {
	io.WriteString(s, k.String())
}

// Reference binds a raw envelope to runtime, contract, tool/scope, mask tier,
// and effective authorization scope.
type Reference struct {
	Runtime   eebusraw.RedactedID `json:"runtime"`
	Contract  ContractVersion     `json:"contract"`
	Tool      ToolID              `json:"tool"`
	Scope     Scope               `json:"scope"`
	MaskTier  eebusraw.MaskTier   `json:"mask_tier"`
	AuthScope AuthScope           `json:"auth_scope"`
}

// NewReference returns an MSP-02B reference.
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

// Validate rejects malformed or unredacted reference bindings.
func (r Reference) Validate() error {
	if err := r.Runtime.Validate(); err != nil {
		return fmt.Errorf("runtime: %w", err)
	}
	if err := r.Contract.Validate(); err != nil {
		return fmt.Errorf("contract: %w", err)
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

// MarshalJSON validates the reference before exposing it as JSON.
func (r Reference) MarshalJSON() ([]byte, error) {
	type alias Reference
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(alias(r))
}

// Matches returns true only when all binding fields are identical.
func (r Reference) Matches(other Reference) bool {
	return r.Runtime == other.Runtime &&
		r.Contract == other.Contract &&
		r.Tool == other.Tool &&
		r.Scope == other.Scope &&
		r.MaskTier == other.MaskTier &&
		r.AuthScope == other.AuthScope
}

// String returns a safe display value.
func (r Reference) String() string {
	return "reference:" + redactedValue
}

// GoString returns a safe display value for %#v.
func (r Reference) GoString() string {
	return r.String()
}

// Format writes a safe display value for logging-style formatting.
func (r Reference) Format(s fmt.State, verb rune) {
	io.WriteString(s, r.String())
}

// Object describes immutable raw evidence by digest, size, and optional
// redacted unknown-field material.
type Object struct {
	Kind          ObjectKind              `json:"kind"`
	Digest        string                  `json:"digest"`
	Size          int                     `json:"size"`
	DataTimestamp time.Time               `json:"data_timestamp"`
	Unknown       []eebusraw.UnknownField `json:"unknown,omitempty"`
}

// NewObject returns an object descriptor for already-redacted payload evidence.
func NewObject(kind ObjectKind, digest string, size int, dataTimestamp time.Time) Object {
	return Object{
		Kind:          kind,
		Digest:        digest,
		Size:          size,
		DataTimestamp: dataTimestamp.UTC(),
	}
}

// DigestBytes returns a sha256 digest string for a raw payload. Callers must
// not log or persist raw before conversion.
func DigestBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Validate rejects malformed object descriptors.
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
	}
	return nil
}

// MarshalJSON validates and normalizes the object descriptor before exposing it
// as JSON.
func (o Object) MarshalJSON() ([]byte, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(newObjectJSON(o))
}

// String returns a safe display value.
func (o Object) String() string {
	return "object:" + redactedValue
}

// GoString returns a safe display value for %#v.
func (o Object) GoString() string {
	return o.String()
}

// Format writes a safe display value for logging-style formatting.
func (o Object) Format(s fmt.State, verb rune) {
	io.WriteString(s, o.String())
}

// Envelope is an immutable raw snapshot/evidence envelope descriptor.
type Envelope struct {
	Reference     Reference `json:"ref"`
	CapturedAt    time.Time `json:"captured_at"`
	DataTimestamp time.Time `json:"data_timestamp"`
	Objects       []Object  `json:"objects,omitempty"`
	DataHash      string    `json:"data_hash,omitempty"`
}

// NewEnvelope returns a raw envelope descriptor.
func NewEnvelope(ref Reference, capturedAt time.Time, dataTimestamp time.Time, objects []Object) Envelope {
	return Envelope{
		Reference:     ref,
		CapturedAt:    capturedAt.UTC(),
		DataTimestamp: dataTimestamp.UTC(),
		Objects:       append([]Object(nil), objects...),
	}
}

// Validate rejects malformed envelope descriptors.
func (e Envelope) Validate() error {
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
	return nil
}

// ComputeDataHash returns the deterministic hash for replay comparison.
//
// The hash input includes reference binding, data_timestamp, and sorted object
// descriptors. It intentionally excludes captured_at and data_hash. The input
// is encoded as restricted RFC 8785 canonical JSON: no maps from callers,
// lexicographic object keys, no insignificant whitespace, UTC timestamps, and
// decimal integer sizes.
func (e Envelope) ComputeDataHash() (string, error) {
	if err := e.Validate(); err != nil {
		return "", err
	}
	payload := canonicalHashPayload(e)
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// WithDataHash returns a copy of the envelope with DataHash populated.
func (e Envelope) WithDataHash() (Envelope, error) {
	hash, err := e.ComputeDataHash()
	if err != nil {
		return Envelope{}, err
	}
	e.DataHash = hash
	return e, nil
}

// MarshalJSON validates and normalizes the envelope before exposing it as JSON.
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

// String returns a safe display value.
func (e Envelope) String() string {
	return "envelope:" + redactedValue
}

// GoString returns a safe display value for %#v.
func (e Envelope) GoString() string {
	return e.String()
}

// Format writes a safe display value for logging-style formatting.
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
	sorted := append([]Object(nil), objects...)
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
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}
