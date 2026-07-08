package eebusraw

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// ContractVersion identifies a reviewable raw contract shape.
type ContractVersion string

// IdentityContractV1Alpha1 is the first raw runtime identity contract.
const IdentityContractV1Alpha1 ContractVersion = "helianthus.eebus.raw.identity.v1alpha1"

// MaskTier describes the exposure level of stable identifiers in a document.
type MaskTier string

const (
	// MaskTierRedacted permits only masked identifiers plus optional redacted hashes.
	MaskTierRedacted MaskTier = "redacted"
)

const redactedValue = "[redacted]"

// IdentityDocument is the top-level raw runtime identity contract.
//
// It is intentionally read-only. It does not model trust-store mutation,
// pairing-window mutation, listeners, command routing, snapshots, or promoted
// facts.
type IdentityDocument struct {
	Contract   ContractVersion    `json:"contract"`
	MaskTier   MaskTier           `json:"mask_tier"`
	CapturedAt time.Time          `json:"captured_at"`
	Local      EndpointIdentity   `json:"local"`
	Remotes    []EndpointIdentity `json:"remotes,omitempty"`
	Sessions   []SessionIdentity  `json:"sessions,omitempty"`
	Unknown    []UnknownField     `json:"unknown,omitempty"`
}

// NewIdentityDocument returns an identity document with the MSP-02A contract.
func NewIdentityDocument(capturedAt time.Time, local EndpointIdentity) IdentityDocument {
	return IdentityDocument{
		Contract:   IdentityContractV1Alpha1,
		MaskTier:   MaskTierRedacted,
		CapturedAt: capturedAt.UTC(),
		Local:      local,
	}
}

// Validate rejects malformed or unredacted public identity documents.
func (d IdentityDocument) Validate() error {
	if d.Contract != IdentityContractV1Alpha1 {
		return fmt.Errorf("identity contract must be %q", IdentityContractV1Alpha1)
	}
	if d.MaskTier != MaskTierRedacted {
		return fmt.Errorf("identity mask tier must be %q", MaskTierRedacted)
	}
	if d.CapturedAt.IsZero() {
		return errors.New("identity captured_at is required")
	}
	if err := d.Local.Validate(); err != nil {
		return fmt.Errorf("local identity: %w", err)
	}
	if d.Local.Role != EndpointRoleLocal {
		return errors.New("local identity role must be local")
	}
	for i, remote := range d.Remotes {
		if err := remote.Validate(); err != nil {
			return fmt.Errorf("remote identity %d: %w", i, err)
		}
		if remote.Role != EndpointRoleRemote {
			return fmt.Errorf("remote identity %d role must be remote", i)
		}
	}
	for i, session := range d.Sessions {
		if err := session.Validate(); err != nil {
			return fmt.Errorf("session identity %d: %w", i, err)
		}
	}
	for i, unknown := range d.Unknown {
		if err := unknown.Validate(); err != nil {
			return fmt.Errorf("document unknown field %d: %w", i, err)
		}
	}
	return nil
}

// MarshalJSON validates the document before exposing it as JSON.
func (d IdentityDocument) MarshalJSON() ([]byte, error) {
	type alias IdentityDocument
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(alias(d))
}

// String returns a safe display value.
func (d IdentityDocument) String() string {
	return "identity_document:" + redactedValue
}

// GoString returns a safe display value for %#v.
func (d IdentityDocument) GoString() string {
	return d.String()
}

// Format writes a safe display value for logging-style formatting.
func (d IdentityDocument) Format(s fmt.State, verb rune) {
	io.WriteString(s, d.String())
}

// EndpointRole identifies whether an endpoint is local or remote.
type EndpointRole string

const (
	EndpointRoleLocal  EndpointRole = "local"
	EndpointRoleRemote EndpointRole = "remote"
)

// EndpointIdentity describes one local or remote runtime identity endpoint.
type EndpointIdentity struct {
	Role    EndpointRole       `json:"role"`
	ID      RedactedID         `json:"id"`
	Pairing PairingObservation `json:"pairing,omitempty"`
	Unknown []UnknownField     `json:"unknown,omitempty"`
}

// Validate rejects malformed or unredacted endpoint identity data.
func (e EndpointIdentity) Validate() error {
	if e.Role != EndpointRoleLocal && e.Role != EndpointRoleRemote {
		return errors.New("endpoint role must be local or remote")
	}
	if err := e.ID.Validate(); err != nil {
		return fmt.Errorf("endpoint id: %w", err)
	}
	if err := e.Pairing.Validate(); err != nil {
		return fmt.Errorf("pairing: %w", err)
	}
	for i, unknown := range e.Unknown {
		if err := unknown.Validate(); err != nil {
			return fmt.Errorf("unknown field %d: %w", i, err)
		}
	}
	return nil
}

// PairingState is an observed pairing state, not a mutation command.
type PairingState string

const (
	PairingStateUnknown  PairingState = "unknown"
	PairingStateUnpaired PairingState = "unpaired"
	PairingStatePaired   PairingState = "paired"
	PairingStateDenied   PairingState = "denied"
)

// PairingObservation is a read-only view of pairing state.
type PairingObservation struct {
	State PairingState `json:"state,omitempty"`
}

// Validate rejects unknown pairing observation values.
func (p PairingObservation) Validate() error {
	switch p.State {
	case "", PairingStateUnknown, PairingStateUnpaired, PairingStatePaired, PairingStateDenied:
		return nil
	default:
		return fmt.Errorf("unsupported pairing state %q", p.State)
	}
}

// SessionState is an observed runtime session state.
type SessionState string

const (
	SessionStateUnknown      SessionState = "unknown"
	SessionStateObserved     SessionState = "observed"
	SessionStateDisconnected SessionState = "disconnected"
	SessionStateDegraded     SessionState = "degraded"
)

// SessionIdentity describes an observed runtime session without exposing raw
// stable identifiers.
type SessionIdentity struct {
	ID       RedactedID     `json:"id"`
	RemoteID RedactedID     `json:"remote_id"`
	State    SessionState   `json:"state"`
	Unknown  []UnknownField `json:"unknown,omitempty"`
}

// Validate rejects malformed or unredacted session identity data.
func (s SessionIdentity) Validate() error {
	if err := s.ID.Validate(); err != nil {
		return fmt.Errorf("session id: %w", err)
	}
	if err := s.RemoteID.Validate(); err != nil {
		return fmt.Errorf("session remote_id: %w", err)
	}
	switch s.State {
	case SessionStateUnknown, SessionStateObserved, SessionStateDisconnected, SessionStateDegraded:
	default:
		return fmt.Errorf("unsupported session state %q", s.State)
	}
	for i, unknown := range s.Unknown {
		if err := unknown.Validate(); err != nil {
			return fmt.Errorf("unknown field %d: %w", i, err)
		}
	}
	return nil
}

// IDKind identifies a non-sensitive category of stable identifier.
type IDKind string

const (
	IDKindLocalSKI               IDKind = "local-ski"
	IDKindRemoteSKI              IDKind = "remote-ski"
	IDKindCertificateFingerprint IDKind = "certificate-fingerprint"
	IDKindPeer                   IDKind = "peer"
	IDKindSession                IDKind = "session"
)

// Validate rejects caller-controlled identifier labels.
func (k IDKind) Validate() error {
	switch k {
	case IDKindLocalSKI, IDKindRemoteSKI, IDKindCertificateFingerprint, IDKindPeer, IDKindSession:
		return nil
	default:
		return errors.New("unsupported id kind")
	}
}

// String returns a safe display value even for invalid kind values.
func (k IDKind) String() string {
	if err := k.Validate(); err != nil {
		return "invalid-kind"
	}
	return string(k)
}

// MarshalJSON validates the identifier kind before exposing it as JSON.
func (k IDKind) MarshalJSON() ([]byte, error) {
	if err := k.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(string(k))
}

// GoString returns a safe display value for %#v.
func (k IDKind) GoString() string {
	return k.String()
}

// Format writes a safe display value for logging-style formatting.
func (k IDKind) Format(s fmt.State, verb rune) {
	io.WriteString(s, k.String())
}

// RedactedID represents a stable identity after masking.
type RedactedID struct {
	Kind   IDKind `json:"kind"`
	Masked string `json:"masked"`
	Digest string `json:"digest,omitempty"`
}

// RedactID converts a raw stable identifier to a redaction-safe public value.
// Callers must not log or persist raw before conversion.
func RedactID(kind IDKind, raw string) (RedactedID, error) {
	if err := kind.Validate(); err != nil {
		return RedactedID{}, err
	}
	if strings.TrimSpace(raw) == "" {
		return RedactedID{}, errors.New("raw id value is required")
	}
	sum := sha256.Sum256([]byte(raw))
	return RedactedID{
		Kind:   kind,
		Masked: redactedValue,
		Digest: "sha256:" + hex.EncodeToString(sum[:]),
	}, nil
}

// Validate rejects identifiers that expose unmasked stable values.
func (r RedactedID) Validate() error {
	if err := r.Kind.Validate(); err != nil {
		return err
	}
	if r.Masked != redactedValue {
		return errors.New("id masked value must be redacted")
	}
	if r.Digest != "" && !validSHA256Digest(r.Digest) {
		return errors.New("id digest must use sha256:<64 hex chars>")
	}
	return nil
}

// MarshalJSON validates the redacted identifier before exposing it as JSON.
func (r RedactedID) MarshalJSON() ([]byte, error) {
	type alias RedactedID
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(alias(r))
}

// String returns a safe display value.
func (r RedactedID) String() string {
	return r.Kind.String() + ":" + redactedValue
}

// GoString returns a safe display value for %#v.
func (r RedactedID) GoString() string {
	return r.String()
}

// Format writes a safe display value for logging-style formatting.
func (r RedactedID) Format(s fmt.State, verb rune) {
	io.WriteString(s, r.String())
}

// UnknownPath identifies a static, non-identity-bearing raw field path.
type UnknownPath string

const (
	UnknownPathDocument UnknownPath = "/document/unknown"
	UnknownPathDevice   UnknownPath = "/device/unknown"
	UnknownPathLocal    UnknownPath = "/local/unknown"
	UnknownPathRemote   UnknownPath = "/remote/unknown"
	UnknownPathSession  UnknownPath = "/session/unknown"
)

// Validate rejects caller-controlled unknown field paths.
func (p UnknownPath) Validate() error {
	switch p {
	case UnknownPathDocument, UnknownPathDevice, UnknownPathLocal, UnknownPathRemote, UnknownPathSession:
		return nil
	default:
		return errors.New("unsupported unknown field path")
	}
}

// String returns a safe display value even for invalid path values.
func (p UnknownPath) String() string {
	if err := p.Validate(); err != nil {
		return "/invalid/unknown"
	}
	return string(p)
}

// MarshalJSON validates the unknown path before exposing it as JSON.
func (p UnknownPath) MarshalJSON() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(string(p))
}

// GoString returns a safe display value for %#v.
func (p UnknownPath) GoString() string {
	return p.String()
}

// Format writes a safe display value for logging-style formatting.
func (p UnknownPath) Format(s fmt.State, verb rune) {
	io.WriteString(s, p.String())
}

// UnknownField carries an unrecognized protocol value as opaque redacted
// evidence instead of normalizing it into a higher-level meaning.
type UnknownField struct {
	Path  UnknownPath `json:"path"`
	Value OpaqueValue `json:"value"`
}

// Validate rejects malformed unknown field evidence.
func (u UnknownField) Validate() error {
	if err := u.Path.Validate(); err != nil {
		return err
	}
	if err := u.Value.Validate(); err != nil {
		return fmt.Errorf("unknown field value: %w", err)
	}
	return nil
}

// MarshalJSON validates the unknown field before exposing it as JSON.
func (u UnknownField) MarshalJSON() ([]byte, error) {
	type alias UnknownField
	if err := u.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(alias(u))
}

// String returns a safe display value.
func (u UnknownField) String() string {
	return "unknown_field:" + redactedValue
}

// GoString returns a safe display value for %#v.
func (u UnknownField) GoString() string {
	return u.String()
}

// Format writes a safe display value for logging-style formatting.
func (u UnknownField) Format(s fmt.State, verb rune) {
	io.WriteString(s, u.String())
}

// OpaqueValue is a redacted representation of raw bytes.
type OpaqueValue struct {
	Masked string `json:"masked"`
	Digest string `json:"digest,omitempty"`
	Size   int    `json:"size,omitempty"`
}

// OpaqueBytes converts raw bytes to an opaque public value. Callers must not
// log or persist raw before conversion.
func OpaqueBytes(raw []byte) OpaqueValue {
	sum := sha256.Sum256(raw)
	return OpaqueValue{
		Masked: redactedValue,
		Digest: "sha256:" + hex.EncodeToString(sum[:]),
		Size:   len(raw),
	}
}

// Validate rejects opaque values that expose raw bytes.
func (o OpaqueValue) Validate() error {
	if o.Masked != redactedValue {
		return errors.New("opaque value must be redacted")
	}
	if o.Digest != "" && !validSHA256Digest(o.Digest) {
		return errors.New("opaque digest must use sha256:<64 hex chars>")
	}
	if o.Size < 0 {
		return errors.New("opaque size must not be negative")
	}
	return nil
}

// MarshalJSON validates the opaque value before exposing it as JSON.
func (o OpaqueValue) MarshalJSON() ([]byte, error) {
	type alias OpaqueValue
	if err := o.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(alias(o))
}

// String returns a safe display value.
func (o OpaqueValue) String() string {
	return "opaque:" + redactedValue
}

// GoString returns a safe display value for %#v.
func (o OpaqueValue) GoString() string {
	return o.String()
}

// Format writes a safe display value for logging-style formatting.
func (o OpaqueValue) Format(s fmt.State, verb rune) {
	io.WriteString(s, o.String())
}

func validSHA256Digest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}
