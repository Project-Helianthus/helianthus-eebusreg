package eebusraw

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

type IdentityDocumentV1 struct {
	Contract   ContractVersion    `json:"contract"`
	MaskTier   MaskTier           `json:"mask_tier"`
	CapturedAt time.Time          `json:"captured_at"`
	Local      EndpointIdentity   `json:"local"`
	Remotes    []EndpointIdentity `json:"remotes,omitempty"`
	Sessions   []SessionIdentity  `json:"sessions,omitempty"`
	Unknown    []UnknownField     `json:"unknown,omitempty"`
}

func NewIdentityDocumentV1(capturedAt time.Time, local EndpointIdentity) IdentityDocumentV1 {
	return IdentityDocumentV1{
		Contract:   IdentityContractV1,
		MaskTier:   MaskTierRedacted,
		CapturedAt: capturedAt.UTC(),
		Local:      copyEndpointIdentityV1(local),
	}
}

func (d IdentityDocumentV1) Validate() error {
	if d.Contract != IdentityContractV1 {
		return fmt.Errorf("identity contract must be %q", IdentityContractV1)
	}
	if d.MaskTier != MaskTierRedacted {
		return fmt.Errorf("identity mask tier must be %q", MaskTierRedacted)
	}
	if d.CapturedAt.IsZero() {
		return errors.New("identity captured_at is required")
	}
	if err := validateEndpointIdentityV1(d.Local); err != nil {
		return fmt.Errorf("local identity: %w", err)
	}
	if d.Local.Role != EndpointRoleLocal {
		return errors.New("local identity role must be local")
	}
	for i, remote := range d.Remotes {
		if err := validateEndpointIdentityV1(remote); err != nil {
			return fmt.Errorf("remote identity %d: %w", i, err)
		}
		if remote.Role != EndpointRoleRemote {
			return fmt.Errorf("remote identity %d role must be remote", i)
		}
	}
	for i, session := range d.Sessions {
		if err := validateSessionIdentityV1(session); err != nil {
			return fmt.Errorf("session identity %d: %w", i, err)
		}
	}
	for i, unknown := range d.Unknown {
		if err := validateUnknownFieldV1(unknown); err != nil {
			return fmt.Errorf("document unknown field %d: %w", i, err)
		}
	}
	return nil
}

func (d IdentityDocumentV1) MarshalJSON() ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(identityDocumentV1JSON{
		Contract:   d.Contract,
		MaskTier:   d.MaskTier,
		CapturedAt: d.CapturedAt.UTC(),
		Local:      newEndpointIdentityV1JSON(canonicalEndpointIdentityV1(d.Local)),
		Remotes:    endpointIdentitiesV1JSON(sortedEndpointIdentitiesV1(d.Remotes)),
		Sessions:   sessionIdentitiesV1JSON(sortedSessionIdentitiesV1(d.Sessions)),
		Unknown:    unknownFieldsV1JSON(sortedIdentityUnknownFieldsV1(d.Unknown)),
	})
}

func (d IdentityDocumentV1) String() string {
	return "identity_document_v1:" + redactedValue
}

func (d IdentityDocumentV1) GoString() string {
	return d.String()
}

func (d IdentityDocumentV1) Format(s fmt.State, verb rune) {
	io.WriteString(s, d.String())
}

func validateEndpointIdentityV1(e EndpointIdentity) error {
	if e.Role != EndpointRoleLocal && e.Role != EndpointRoleRemote {
		return errors.New("endpoint role must be local or remote")
	}
	if err := e.ID.Validate(); err != nil {
		return fmt.Errorf("endpoint id: %w", err)
	}
	switch e.Pairing.State {
	case "", PairingStateUnknown, PairingStateUnpaired, PairingStatePaired, PairingStateDenied:
	default:
		return errors.New("pairing: unsupported pairing state")
	}
	for i, unknown := range e.Unknown {
		if err := validateUnknownFieldV1(unknown); err != nil {
			return fmt.Errorf("unknown field %d: %w", i, err)
		}
	}
	return nil
}

func validateSessionIdentityV1(s SessionIdentity) error {
	if err := s.ID.Validate(); err != nil {
		return fmt.Errorf("session id: %w", err)
	}
	if err := s.RemoteID.Validate(); err != nil {
		return fmt.Errorf("session remote_id: %w", err)
	}
	switch s.State {
	case SessionStateUnknown, SessionStateObserved, SessionStateDisconnected, SessionStateDegraded:
	default:
		return errors.New("unsupported session state")
	}
	for i, unknown := range s.Unknown {
		if err := validateUnknownFieldV1(unknown); err != nil {
			return fmt.Errorf("unknown field %d: %w", i, err)
		}
	}
	return nil
}

func sortedEndpointIdentitiesV1(identities []EndpointIdentity) []EndpointIdentity {
	sorted := make([]EndpointIdentity, len(identities))
	for i, identity := range identities {
		sorted[i] = canonicalEndpointIdentityV1(identity)
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		return endpointIdentitySortKeyV1(sorted[i]) < endpointIdentitySortKeyV1(sorted[j])
	})
	return sorted
}

func canonicalEndpointIdentityV1(identity EndpointIdentity) EndpointIdentity {
	identity.Unknown = sortedIdentityUnknownFieldsV1(identity.Unknown)
	return identity
}

func copyEndpointIdentityV1(identity EndpointIdentity) EndpointIdentity {
	identity.Unknown = append([]UnknownField(nil), identity.Unknown...)
	return identity
}

func endpointIdentitySortKeyV1(identity EndpointIdentity) string {
	var b strings.Builder
	b.WriteString(string(identity.Role))
	b.WriteByte(0)
	b.WriteString(string(identity.ID.Kind))
	b.WriteByte(0)
	b.WriteString(identity.ID.Masked)
	b.WriteByte(0)
	b.WriteString(identity.ID.Digest)
	b.WriteByte(0)
	b.WriteString(string(identity.Pairing.State))
	b.WriteByte(0)
	b.WriteString(identityUnknownFieldsSortKeyV1(identity.Unknown))
	return b.String()
}

func sortedSessionIdentitiesV1(sessions []SessionIdentity) []SessionIdentity {
	sorted := make([]SessionIdentity, len(sessions))
	for i, session := range sessions {
		sorted[i] = session
		sorted[i].Unknown = sortedIdentityUnknownFieldsV1(session.Unknown)
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		return sessionIdentitySortKeyV1(sorted[i]) < sessionIdentitySortKeyV1(sorted[j])
	})
	return sorted
}

func sessionIdentitySortKeyV1(session SessionIdentity) string {
	var b strings.Builder
	b.WriteString(string(session.ID.Kind))
	b.WriteByte(0)
	b.WriteString(session.ID.Masked)
	b.WriteByte(0)
	b.WriteString(session.ID.Digest)
	b.WriteByte(0)
	b.WriteString(string(session.RemoteID.Kind))
	b.WriteByte(0)
	b.WriteString(session.RemoteID.Masked)
	b.WriteByte(0)
	b.WriteString(session.RemoteID.Digest)
	b.WriteByte(0)
	b.WriteString(string(session.State))
	b.WriteByte(0)
	b.WriteString(identityUnknownFieldsSortKeyV1(session.Unknown))
	return b.String()
}

func sortedIdentityUnknownFieldsV1(unknown []UnknownField) []UnknownField {
	sorted := append([]UnknownField(nil), unknown...)
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

func identityUnknownFieldsSortKeyV1(unknown []UnknownField) string {
	var b strings.Builder
	for _, field := range sortedIdentityUnknownFieldsV1(unknown) {
		b.WriteString(string(field.Path))
		b.WriteByte(0)
		b.WriteString(field.Value.Digest)
		b.WriteByte(0)
		b.WriteString(field.Value.Masked)
		b.WriteByte(0)
		b.WriteString(strconv.Itoa(field.Value.Size))
		b.WriteByte(0)
	}
	return b.String()
}

type identityDocumentV1JSON struct {
	Contract   ContractVersion          `json:"contract"`
	MaskTier   MaskTier                 `json:"mask_tier"`
	CapturedAt time.Time                `json:"captured_at"`
	Local      endpointIdentityV1JSON   `json:"local"`
	Remotes    []endpointIdentityV1JSON `json:"remotes,omitempty"`
	Sessions   []sessionIdentityV1JSON  `json:"sessions,omitempty"`
	Unknown    []unknownFieldV1JSON     `json:"unknown,omitempty"`
}

type endpointIdentityV1JSON struct {
	Role    EndpointRole         `json:"role"`
	ID      RedactedID           `json:"id"`
	Pairing PairingObservation   `json:"pairing,omitempty"`
	Unknown []unknownFieldV1JSON `json:"unknown,omitempty"`
}

type sessionIdentityV1JSON struct {
	ID       RedactedID           `json:"id"`
	RemoteID RedactedID           `json:"remote_id"`
	State    SessionState         `json:"state"`
	Unknown  []unknownFieldV1JSON `json:"unknown,omitempty"`
}

type unknownFieldV1JSON struct {
	Path  UnknownPath       `json:"path"`
	Value opaqueValueV1JSON `json:"value"`
}

type opaqueValueV1JSON struct {
	Masked string `json:"masked"`
	Digest string `json:"digest,omitempty"`
	Size   int    `json:"size,omitempty"`
}

func newEndpointIdentityV1JSON(identity EndpointIdentity) endpointIdentityV1JSON {
	return endpointIdentityV1JSON{
		Role:    identity.Role,
		ID:      identity.ID,
		Pairing: identity.Pairing,
		Unknown: unknownFieldsV1JSON(identity.Unknown),
	}
}

func endpointIdentitiesV1JSON(identities []EndpointIdentity) []endpointIdentityV1JSON {
	converted := make([]endpointIdentityV1JSON, len(identities))
	for i, identity := range identities {
		converted[i] = newEndpointIdentityV1JSON(identity)
	}
	return converted
}

func sessionIdentitiesV1JSON(sessions []SessionIdentity) []sessionIdentityV1JSON {
	converted := make([]sessionIdentityV1JSON, len(sessions))
	for i, session := range sessions {
		converted[i] = sessionIdentityV1JSON{
			ID:       session.ID,
			RemoteID: session.RemoteID,
			State:    session.State,
			Unknown:  unknownFieldsV1JSON(session.Unknown),
		}
	}
	return converted
}

func unknownFieldsV1JSON(unknown []UnknownField) []unknownFieldV1JSON {
	converted := make([]unknownFieldV1JSON, len(unknown))
	for i, field := range unknown {
		converted[i] = unknownFieldV1JSON{
			Path: field.Path,
			Value: opaqueValueV1JSON{
				Masked: field.Value.Masked,
				Digest: field.Value.Digest,
				Size:   field.Value.Size,
			},
		}
	}
	return converted
}

func validateUnknownFieldV1(unknown UnknownField) error {
	if err := unknown.Path.Validate(); err != nil {
		return err
	}
	if unknown.Value.Masked != redactedValue {
		return errors.New("unknown field value: opaque value must be redacted")
	}
	if unknown.Value.Digest != "" && !validIdentityDigestV1(unknown.Value.Digest) {
		return errors.New("unknown field value: opaque digest must use lowercase sha256:<64 chars>")
	}
	if unknown.Value.Size < 0 {
		return errors.New("unknown field value: opaque size must not be negative")
	}
	return nil
}

func validIdentityDigestV1(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, char := range strings.TrimPrefix(value, "sha256:") {
		if (char < '0' || char > '9') && (char < 'a' || char > 'z') {
			return false
		}
	}
	return true
}
