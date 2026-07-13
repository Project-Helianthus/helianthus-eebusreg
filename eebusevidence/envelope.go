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

type ContractVersion string

const EnvelopeContractV1Alpha1 ContractVersion = "helianthus.eebus.raw.evidence-envelope.v1alpha1"

func (c ContractVersion) Validate() error {
	if c != EnvelopeContractV1Alpha1 {
		return errors.New("unsupported evidence contract")
	}
	return nil
}

func (c ContractVersion) MarshalJSON() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(string(c))
}

func (c ContractVersion) String() string {
	if err := c.Validate(); err != nil {
		return "invalid-contract"
	}
	return string(c)
}

func (c ContractVersion) GoString() string {
	return c.String()
}

func (c ContractVersion) Format(s fmt.State, verb rune) {
	io.WriteString(s, c.String())
}

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

func (t ToolID) Validate() error {
	switch t {
	case ToolRuntimeStatus, ToolServicesList, ToolServicesGet, ToolSessionsList, ToolSessionsGet, ToolTopologyGet, ToolCapture, ToolPairingStatus:
		return nil
	default:
		return errors.New("unsupported tool id")
	}
}

func (t ToolID) MarshalJSON() ([]byte, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(string(t))
}

func (t ToolID) String() string {
	if err := t.Validate(); err != nil {
		return "invalid-tool"
	}
	return string(t)
}

func (t ToolID) GoString() string {
	return t.String()
}

func (t ToolID) Format(s fmt.State, verb rune) {
	io.WriteString(s, t.String())
}

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

func (s Scope) Validate() error {
	switch s {
	case ScopeWholeRoot, ScopeRuntimeStatus, ScopeServices, ScopeService, ScopeSessions, ScopeSession, ScopeTopology, ScopePairingStatus:
		return nil
	default:
		return errors.New("unsupported scope")
	}
}

func (s Scope) MarshalJSON() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(string(s))
}

func (s Scope) String() string {
	if err := s.Validate(); err != nil {
		return "invalid-scope"
	}
	return string(s)
}

func (s Scope) GoString() string {
	return s.String()
}

func (s Scope) Format(st fmt.State, verb rune) {
	io.WriteString(st, s.String())
}

type AuthScope string

const (
	AuthScopeReadRaw AuthScope = "eebus.raw.read"
)

func (a AuthScope) Validate() error {
	if a != AuthScopeReadRaw {
		return errors.New("unsupported auth scope")
	}
	return nil
}

func (a AuthScope) MarshalJSON() ([]byte, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(string(a))
}

func (a AuthScope) String() string {
	if err := a.Validate(); err != nil {
		return "invalid-auth-scope"
	}
	return string(a)
}

func (a AuthScope) GoString() string {
	return a.String()
}

func (a AuthScope) Format(s fmt.State, verb rune) {
	io.WriteString(s, a.String())
}

type ObjectKind string

const (
	ObjectKindIdentity ObjectKind = "identity"
	ObjectKindTopology ObjectKind = "topology"
	ObjectKindService  ObjectKind = "service"
	ObjectKindSession  ObjectKind = "session"
	ObjectKindUnknown  ObjectKind = "unknown"
)

func (k ObjectKind) Validate() error {
	switch k {
	case ObjectKindIdentity, ObjectKindTopology, ObjectKindService, ObjectKindSession, ObjectKindUnknown:
		return nil
	default:
		return errors.New("unsupported object kind")
	}
}

func (k ObjectKind) MarshalJSON() ([]byte, error) {
	if err := k.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(string(k))
}

func (k ObjectKind) String() string {
	if err := k.Validate(); err != nil {
		return "invalid-object-kind"
	}
	return string(k)
}

func (k ObjectKind) GoString() string {
	return k.String()
}

func (k ObjectKind) Format(s fmt.State, verb rune) {
	io.WriteString(s, k.String())
}

type Reference struct {
	Runtime   eebusraw.RedactedID `json:"runtime"`
	Contract  ContractVersion     `json:"contract"`
	Tool      ToolID              `json:"tool"`
	Scope     Scope               `json:"scope"`
	MaskTier  eebusraw.MaskTier   `json:"mask_tier"`
	AuthScope AuthScope           `json:"auth_scope"`
}

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

func (r Reference) MarshalJSON() ([]byte, error) {
	type alias Reference
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(alias(r))
}

func (r Reference) Matches(other Reference) bool {
	return r.Runtime == other.Runtime &&
		r.Contract == other.Contract &&
		r.Tool == other.Tool &&
		r.Scope == other.Scope &&
		r.MaskTier == other.MaskTier &&
		r.AuthScope == other.AuthScope
}

func (r Reference) String() string {
	return "reference:" + redactedValue
}

func (r Reference) GoString() string {
	return r.String()
}

func (r Reference) Format(s fmt.State, verb rune) {
	io.WriteString(s, r.String())
}

type Object struct {
	Kind          ObjectKind              `json:"kind"`
	Digest        string                  `json:"digest"`
	Size          int                     `json:"size"`
	DataTimestamp time.Time               `json:"data_timestamp"`
	Unknown       []eebusraw.UnknownField `json:"unknown,omitempty"`
}

func NewObject(kind ObjectKind, digest string, size int, dataTimestamp time.Time) Object {
	return Object{
		Kind:          kind,
		Digest:        digest,
		Size:          size,
		DataTimestamp: dataTimestamp.UTC(),
	}
}

func DigestBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

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

func (o Object) MarshalJSON() ([]byte, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(newObjectJSON(o))
}

func (o Object) String() string {
	return "object:" + redactedValue
}

func (o Object) GoString() string {
	return o.String()
}

func (o Object) Format(s fmt.State, verb rune) {
	io.WriteString(s, o.String())
}

type Envelope struct {
	Reference     Reference `json:"ref"`
	CapturedAt    time.Time `json:"captured_at"`
	DataTimestamp time.Time `json:"data_timestamp"`
	Objects       []Object  `json:"objects,omitempty"`
	DataHash      string    `json:"data_hash,omitempty"`
}

func NewEnvelope(ref Reference, capturedAt time.Time, dataTimestamp time.Time, objects []Object) Envelope {
	return Envelope{
		Reference:     ref,
		CapturedAt:    capturedAt.UTC(),
		DataTimestamp: dataTimestamp.UTC(),
		Objects:       copyObjects(objects),
	}
}

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

func (e Envelope) WithDataHash() (Envelope, error) {
	hash, err := e.ComputeDataHash()
	if err != nil {
		return Envelope{}, err
	}
	e.DataHash = hash
	e.Objects = copyObjects(e.Objects)
	return e, nil
}

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

func (e Envelope) String() string {
	return "envelope:" + redactedValue
}

func (e Envelope) GoString() string {
	return e.String()
}

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
