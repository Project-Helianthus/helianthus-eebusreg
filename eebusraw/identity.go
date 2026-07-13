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

type ContractVersion string

const IdentityContractV1Alpha1 ContractVersion = "helianthus.eebus.raw.identity.v1alpha1"

type MaskTier string

const (
	MaskTierRedacted MaskTier = "redacted"
)

const redactedValue = "[redacted]"

type IdentityDocument struct {
	Contract   ContractVersion    `json:"contract"`
	MaskTier   MaskTier           `json:"mask_tier"`
	CapturedAt time.Time          `json:"captured_at"`
	Local      EndpointIdentity   `json:"local"`
	Remotes    []EndpointIdentity `json:"remotes,omitempty"`
	Sessions   []SessionIdentity  `json:"sessions,omitempty"`
	Unknown    []UnknownField     `json:"unknown,omitempty"`
}

func NewIdentityDocument(capturedAt time.Time, local EndpointIdentity) IdentityDocument {
	return IdentityDocument{
		Contract:   IdentityContractV1Alpha1,
		MaskTier:   MaskTierRedacted,
		CapturedAt: capturedAt.UTC(),
		Local:      local,
	}
}

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

func (d IdentityDocument) MarshalJSON() ([]byte, error) {
	type alias IdentityDocument
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(alias(d))
}

func (d IdentityDocument) String() string {
	return "identity_document:" + redactedValue
}

func (d IdentityDocument) GoString() string {
	return d.String()
}

func (d IdentityDocument) Format(s fmt.State, verb rune) {
	io.WriteString(s, d.String())
}

type EndpointRole string

const (
	EndpointRoleLocal  EndpointRole = "local"
	EndpointRoleRemote EndpointRole = "remote"
)

type EndpointIdentity struct {
	Role    EndpointRole       `json:"role"`
	ID      RedactedID         `json:"id"`
	Pairing PairingObservation `json:"pairing,omitempty"`
	Unknown []UnknownField     `json:"unknown,omitempty"`
}

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

type PairingState string

const (
	PairingStateUnknown  PairingState = "unknown"
	PairingStateUnpaired PairingState = "unpaired"
	PairingStatePaired   PairingState = "paired"
	PairingStateDenied   PairingState = "denied"
)

type PairingObservation struct {
	State PairingState `json:"state,omitempty"`
}

func (p PairingObservation) Validate() error {
	switch p.State {
	case "", PairingStateUnknown, PairingStateUnpaired, PairingStatePaired, PairingStateDenied:
		return nil
	default:
		return fmt.Errorf("unsupported pairing state %q", p.State)
	}
}

type SessionState string

const (
	SessionStateUnknown      SessionState = "unknown"
	SessionStateObserved     SessionState = "observed"
	SessionStateDisconnected SessionState = "disconnected"
	SessionStateDegraded     SessionState = "degraded"
)

type SessionIdentity struct {
	ID       RedactedID     `json:"id"`
	RemoteID RedactedID     `json:"remote_id"`
	State    SessionState   `json:"state"`
	Unknown  []UnknownField `json:"unknown,omitempty"`
}

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

type IDKind string

const (
	IDKindLocalSKI               IDKind = "local-ski"
	IDKindRemoteSKI              IDKind = "remote-ski"
	IDKindCertificateFingerprint IDKind = "certificate-fingerprint"
	IDKindPeer                   IDKind = "peer"
	IDKindSession                IDKind = "session"
)

func (k IDKind) Validate() error {
	switch k {
	case IDKindLocalSKI, IDKindRemoteSKI, IDKindCertificateFingerprint, IDKindPeer, IDKindSession:
		return nil
	default:
		return errors.New("unsupported id kind")
	}
}

func (k IDKind) String() string {
	if err := k.Validate(); err != nil {
		return "invalid-kind"
	}
	return string(k)
}

func (k IDKind) MarshalJSON() ([]byte, error) {
	if err := k.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(string(k))
}

func (k IDKind) GoString() string {
	return k.String()
}

func (k IDKind) Format(s fmt.State, verb rune) {
	io.WriteString(s, k.String())
}

type RedactedID struct {
	Kind   IDKind `json:"kind"`
	Masked string `json:"masked"`
	Digest string `json:"digest,omitempty"`
}

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

func (r RedactedID) MarshalJSON() ([]byte, error) {
	type alias RedactedID
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(alias(r))
}

func (r RedactedID) String() string {
	return r.Kind.String() + ":" + redactedValue
}

func (r RedactedID) GoString() string {
	return r.String()
}

func (r RedactedID) Format(s fmt.State, verb rune) {
	io.WriteString(s, r.String())
}

type UnknownPath string

const (
	UnknownPathDocument UnknownPath = "/document/unknown"
	UnknownPathDevice   UnknownPath = "/device/unknown"
	UnknownPathLocal    UnknownPath = "/local/unknown"
	UnknownPathRemote   UnknownPath = "/remote/unknown"
	UnknownPathSession  UnknownPath = "/session/unknown"
)

func (p UnknownPath) Validate() error {
	switch p {
	case UnknownPathDocument, UnknownPathDevice, UnknownPathLocal, UnknownPathRemote, UnknownPathSession:
		return nil
	default:
		return errors.New("unsupported unknown field path")
	}
}

func (p UnknownPath) String() string {
	if err := p.Validate(); err != nil {
		return "/invalid/unknown"
	}
	return string(p)
}

func (p UnknownPath) MarshalJSON() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(string(p))
}

func (p UnknownPath) GoString() string {
	return p.String()
}

func (p UnknownPath) Format(s fmt.State, verb rune) {
	io.WriteString(s, p.String())
}

type UnknownField struct {
	Path  UnknownPath `json:"path"`
	Value OpaqueValue `json:"value"`
}

func (u UnknownField) Validate() error {
	if err := u.Path.Validate(); err != nil {
		return err
	}
	if err := u.Value.Validate(); err != nil {
		return fmt.Errorf("unknown field value: %w", err)
	}
	return nil
}

func (u UnknownField) MarshalJSON() ([]byte, error) {
	type alias UnknownField
	if err := u.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(alias(u))
}

func (u UnknownField) String() string {
	return "unknown_field:" + redactedValue
}

func (u UnknownField) GoString() string {
	return u.String()
}

func (u UnknownField) Format(s fmt.State, verb rune) {
	io.WriteString(s, u.String())
}

type OpaqueValue struct {
	Masked string `json:"masked"`
	Digest string `json:"digest,omitempty"`
	Size   int    `json:"size,omitempty"`
}

func OpaqueBytes(raw []byte) OpaqueValue {
	sum := sha256.Sum256(raw)
	return OpaqueValue{
		Masked: redactedValue,
		Digest: "sha256:" + hex.EncodeToString(sum[:]),
		Size:   len(raw),
	}
}

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

func (o OpaqueValue) MarshalJSON() ([]byte, error) {
	type alias OpaqueValue
	if err := o.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(alias(o))
}

func (o OpaqueValue) String() string {
	return "opaque:" + redactedValue
}

func (o OpaqueValue) GoString() string {
	return o.String()
}

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
