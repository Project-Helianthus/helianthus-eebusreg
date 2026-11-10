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

// ContractVersion identifies the identity document contract used for validation and encoding.
type ContractVersion string

// IdentityContractV1Alpha1 selects the alpha identity contract.
const IdentityContractV1Alpha1 ContractVersion = "helianthus.eebus.raw.identity.v1alpha1"

// IdentityContractV1 selects the v1 identity contract.
const IdentityContractV1 ContractVersion = "helianthus.eebus.raw.identity.v1"

// MaskTier identifies the permitted representation of sensitive values.
type MaskTier string

const (
	// MaskTierRedacted requires sensitive values to be masked.
	MaskTierRedacted MaskTier = "redacted"
)

const redactedValue = "[redacted]"

// IdentityDocument holds a redacted alpha identity snapshot and validates its endpoint roles.
type IdentityDocument struct {
	// Contract selects the document contract.
	Contract ContractVersion `json:"contract"`
	// MaskTier specifies the required redaction tier.
	MaskTier MaskTier `json:"mask_tier"`
	// CapturedAt records when the snapshot was captured.
	CapturedAt time.Time `json:"captured_at"`
	// Local contains the local endpoint identity.
	Local EndpointIdentity `json:"local"`
	// Remotes contains observed remote endpoint identities.
	Remotes []EndpointIdentity `json:"remotes,omitempty"`
	// Sessions contains observed session identities.
	Sessions []SessionIdentity `json:"sessions,omitempty"`
	// Unknown retains unknown fields in masked form.
	Unknown []UnknownField `json:"unknown,omitempty"`
}

// NewIdentityDocument constructs an alpha document with a UTC timestamp and redacted mask tier.
func NewIdentityDocument(capturedAt time.Time, local EndpointIdentity) IdentityDocument {
	return IdentityDocument{
		Contract:   IdentityContractV1Alpha1,
		MaskTier:   MaskTierRedacted,
		CapturedAt: capturedAt.UTC(),
		Local:      local,
	}
}

// Validate checks the contract, mask tier, timestamp, endpoint roles and nested values.
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

// MarshalJSON validates the value before encoding it as JSON.
func (d IdentityDocument) MarshalJSON() ([]byte, error) {
	type alias IdentityDocument
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(alias(d))
}

// String returns a safe representation for display.
func (d IdentityDocument) String() string {
	return "identity_document:" + redactedValue
}

// GoString returns the same safe representation as String.
func (d IdentityDocument) GoString() string {
	return d.String()
}

// Format writes the safe representation for any formatting verb.
func (d IdentityDocument) Format(s fmt.State, verb rune) {
	io.WriteString(s, d.String())
}

// EndpointRole identifies whether an endpoint is local or remote.
type EndpointRole string

const (
	// EndpointRoleLocal marks the local endpoint.
	EndpointRoleLocal EndpointRole = "local"
	// EndpointRoleRemote marks a remote endpoint.
	EndpointRoleRemote EndpointRole = "remote"
)

// EndpointIdentity holds a redacted endpoint, its pairing observation and unknown fields.
type EndpointIdentity struct {
	// Role identifies the endpoint as local or remote.
	Role EndpointRole `json:"role"`
	// ID contains the redacted identifier.
	ID RedactedID `json:"id"`
	// Pairing records the pairing observation.
	Pairing PairingObservation `json:"pairing,omitempty"`
	// Unknown retains unknown fields in masked form.
	Unknown []UnknownField `json:"unknown,omitempty"`
}

// Validate checks the value against its contract and returns an error on invalid input.
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

// PairingState records an observed pairing state, not qualification.
type PairingState string

const (
	// PairingStateUnknown records an unknown pairing state.
	PairingStateUnknown PairingState = "unknown"
	// PairingStateUnpaired records an unpaired observation.
	PairingStateUnpaired PairingState = "unpaired"
	// PairingStatePaired records a paired observation.
	PairingStatePaired PairingState = "paired"
	// PairingStateDenied records a denied pairing observation.
	PairingStateDenied PairingState = "denied"
)

// PairingObservation records a pairing state without asserting qualification.
type PairingObservation struct {
	// State records the observed state.
	State PairingState `json:"state,omitempty"`
}

// Validate accepts an empty or recognized observation and rejects other states.
func (p PairingObservation) Validate() error {
	switch p.State {
	case "", PairingStateUnknown, PairingStateUnpaired, PairingStatePaired, PairingStateDenied:
		return nil
	default:
		return fmt.Errorf("unsupported pairing state %q", p.State)
	}
}

// SessionState identifies an observed session state.
type SessionState string

const (
	// SessionStateUnknown records an unknown session state.
	SessionStateUnknown SessionState = "unknown"
	// SessionStateObserved records an observed session.
	SessionStateObserved SessionState = "observed"
	// SessionStateDisconnected records a disconnected session.
	SessionStateDisconnected SessionState = "disconnected"
	// SessionStateDegraded records a degraded session.
	SessionStateDegraded SessionState = "degraded"
)

// SessionIdentity holds a redacted session and its remote identity.
type SessionIdentity struct {
	// ID contains the redacted identifier.
	ID RedactedID `json:"id"`
	// RemoteID contains the redacted remote identifier.
	RemoteID RedactedID `json:"remote_id"`
	// State records the observed state.
	State SessionState `json:"state"`
	// Unknown retains unknown fields in masked form.
	Unknown []UnknownField `json:"unknown,omitempty"`
}

// Validate checks the value against its contract and returns an error on invalid input.
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

// IDKind identifies the category of a redacted identifier.
type IDKind string

const (
	// IDKindLocalSKI labels a local SKI.
	IDKindLocalSKI IDKind = "local-ski"
	// IDKindRemoteSKI labels a remote SKI.
	IDKindRemoteSKI IDKind = "remote-ski"
	// IDKindCertificateFingerprint labels a certificate fingerprint.
	IDKindCertificateFingerprint IDKind = "certificate-fingerprint"
	// IDKindPeer labels a peer identifier.
	IDKindPeer IDKind = "peer"
	// IDKindSession labels a session identifier.
	IDKindSession IDKind = "session"
)

// Validate checks the value against its contract and returns an error on invalid input.
func (k IDKind) Validate() error {
	switch k {
	case IDKindLocalSKI, IDKindRemoteSKI, IDKindCertificateFingerprint, IDKindPeer, IDKindSession:
		return nil
	default:
		return errors.New("unsupported id kind")
	}
}

// String returns a safe representation for display.
func (k IDKind) String() string {
	if err := k.Validate(); err != nil {
		return "invalid-kind"
	}
	return string(k)
}

// MarshalJSON validates the value before encoding it as JSON.
func (k IDKind) MarshalJSON() ([]byte, error) {
	if err := k.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(string(k))
}

// GoString returns the same safe representation as String.
func (k IDKind) GoString() string {
	return k.String()
}

// Format writes the safe representation for any formatting verb.
func (k IDKind) Format(s fmt.State, verb rune) {
	io.WriteString(s, k.String())
}

// RedactedID holds a masked identifier and an optional comparison digest.
type RedactedID struct {
	// Kind identifies the category of identifier.
	Kind IDKind `json:"kind"`
	// Masked contains the required redaction marker.
	Masked string `json:"masked"`
	// Digest holds an optional SHA-256 comparison digest.
	Digest string `json:"digest,omitempty"`
}

// RedactID masks a nonblank identifier and computes its SHA-256 digest.
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

// Validate checks the value against its contract and returns an error on invalid input.
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

// MarshalJSON validates the value before encoding it as JSON.
func (r RedactedID) MarshalJSON() ([]byte, error) {
	type alias RedactedID
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(alias(r))
}

// String returns a safe representation for display.
func (r RedactedID) String() string {
	return r.Kind.String() + ":" + redactedValue
}

// GoString returns the same safe representation as String.
func (r RedactedID) GoString() string {
	return r.String()
}

// Format writes the safe representation for any formatting verb.
func (r RedactedID) Format(s fmt.State, verb rune) {
	io.WriteString(s, r.String())
}

// UnknownPath identifies an allowed location for an unknown field.
type UnknownPath string

const (
	// UnknownPathDocument locates document-level unknown fields.
	UnknownPathDocument UnknownPath = "/document/unknown"
	// UnknownPathDevice locates device-level unknown fields.
	UnknownPathDevice UnknownPath = "/device/unknown"
	// UnknownPathLocal locates local endpoint unknown fields.
	UnknownPathLocal UnknownPath = "/local/unknown"
	// UnknownPathRemote locates remote endpoint unknown fields.
	UnknownPathRemote UnknownPath = "/remote/unknown"
	// UnknownPathSession locates session unknown fields.
	UnknownPathSession UnknownPath = "/session/unknown"
)

// Validate checks the value against its contract and returns an error on invalid input.
func (p UnknownPath) Validate() error {
	switch p {
	case UnknownPathDocument, UnknownPathDevice, UnknownPathLocal, UnknownPathRemote, UnknownPathSession:
		return nil
	default:
		return errors.New("unsupported unknown field path")
	}
}

// String returns a safe representation for display.
func (p UnknownPath) String() string {
	if err := p.Validate(); err != nil {
		return "/invalid/unknown"
	}
	return string(p)
}

// MarshalJSON validates the value before encoding it as JSON.
func (p UnknownPath) MarshalJSON() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(string(p))
}

// GoString returns the same safe representation as String.
func (p UnknownPath) GoString() string {
	return p.String()
}

// Format writes the safe representation for any formatting verb.
func (p UnknownPath) Format(s fmt.State, verb rune) {
	io.WriteString(s, p.String())
}

// UnknownField retains an unrecognized value in masked form at an allowed path.
type UnknownField struct {
	// Path identifies the allowed unknown-field location.
	Path UnknownPath `json:"path"`
	// Value contains the masked unknown value.
	Value OpaqueValue `json:"value"`
}

// Validate checks the value against its contract and returns an error on invalid input.
func (u UnknownField) Validate() error {
	if err := u.Path.Validate(); err != nil {
		return err
	}
	if err := u.Value.Validate(); err != nil {
		return fmt.Errorf("unknown field value: %w", err)
	}
	return nil
}

// MarshalJSON validates the value before encoding it as JSON.
func (u UnknownField) MarshalJSON() ([]byte, error) {
	type alias UnknownField
	if err := u.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(alias(u))
}

// String returns a safe representation for display.
func (u UnknownField) String() string {
	return "unknown_field:" + redactedValue
}

// GoString returns the same safe representation as String.
func (u UnknownField) GoString() string {
	return u.String()
}

// Format writes the safe representation for any formatting verb.
func (u UnknownField) Format(s fmt.State, verb rune) {
	io.WriteString(s, u.String())
}

// OpaqueValue retains a masked value with optional digest and size.
type OpaqueValue struct {
	// Masked contains the required redaction marker.
	Masked string `json:"masked"`
	// Digest holds an optional SHA-256 comparison digest.
	Digest string `json:"digest,omitempty"`
	// Size records the original byte count.
	Size int `json:"size,omitempty"`
}

// OpaqueBytes masks bytes and records their SHA-256 digest and length.
func OpaqueBytes(raw []byte) OpaqueValue {
	sum := sha256.Sum256(raw)
	return OpaqueValue{
		Masked: redactedValue,
		Digest: "sha256:" + hex.EncodeToString(sum[:]),
		Size:   len(raw),
	}
}

// Validate checks the value against its contract and returns an error on invalid input.
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

// MarshalJSON validates the value before encoding it as JSON.
func (o OpaqueValue) MarshalJSON() ([]byte, error) {
	type alias OpaqueValue
	if err := o.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(alias(o))
}

// String returns a safe representation for display.
func (o OpaqueValue) String() string {
	return "opaque:" + redactedValue
}

// GoString returns the same safe representation as String.
func (o OpaqueValue) GoString() string {
	return o.String()
}

// Format writes the safe representation for any formatting verb.
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
