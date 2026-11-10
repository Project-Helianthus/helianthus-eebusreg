package eebusraw

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

// EndpointRoleV1 identifies the local or remote endpoint in a versioned identity.
type EndpointRoleV1 string

const (
	// EndpointRoleV1Local marks the local endpoint in v1.
	EndpointRoleV1Local EndpointRoleV1 = "local"
	// EndpointRoleV1Remote marks a remote endpoint in v1.
	EndpointRoleV1Remote EndpointRoleV1 = "remote"
)

// Validate checks the value against its contract and returns an error on invalid input.
func (r EndpointRoleV1) Validate() error {
	switch r {
	case EndpointRoleV1Local, EndpointRoleV1Remote:
		return nil
	default:
		return errors.New("unsupported endpoint role")
	}
}

// String returns a safe representation for display.
func (r EndpointRoleV1) String() string {
	if err := r.Validate(); err != nil {
		return "invalid-role"
	}
	return string(r)
}

// MarshalJSON validates the value before encoding it as JSON.
func (r EndpointRoleV1) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(string(r))
}

// GoString returns the same safe representation as String.
func (r EndpointRoleV1) GoString() string {
	return r.String()
}

// Format writes the safe representation for any formatting verb.
func (r EndpointRoleV1) Format(s fmt.State, verb rune) {
	io.WriteString(s, r.String())
}

// EndpointIdentityV1 holds a redacted endpoint and unknown fields for the v1 contract.
// Its identifier requires a lowercase SHA-256 digest.
type EndpointIdentityV1 struct {
	// Role identifies the endpoint as local or remote.
	Role EndpointRoleV1 `json:"role"`
	// ID contains the redacted identifier.
	ID RedactedID `json:"id"`
	// Unknown retains unknown fields in masked form.
	Unknown []UnknownField `json:"unknown,omitempty"`
}

// Validate checks the value against its contract and returns an error on invalid input.
func (e EndpointIdentityV1) Validate() error {
	if err := e.Role.Validate(); err != nil {
		return errors.New("endpoint role must be local or remote")
	}
	if err := validateRedactedIDV1(e.ID); err != nil {
		return fmt.Errorf("endpoint id: %w", err)
	}
	for i, unknown := range e.Unknown {
		if err := validateUnknownFieldV1(unknown); err != nil {
			return fmt.Errorf("unknown field %d: %w", i, err)
		}
	}
	return nil
}

// MarshalJSON validates the endpoint and sorts its unknown fields before encoding.
func (e EndpointIdentityV1) MarshalJSON() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	type endpointIdentityV1JSON struct {
		Role    EndpointRoleV1 `json:"role"`
		ID      RedactedID     `json:"id"`
		Unknown []UnknownField `json:"unknown,omitempty"`
	}
	return json.Marshal(endpointIdentityV1JSON{
		Role:    e.Role,
		ID:      e.ID,
		Unknown: sortedIdentityUnknownFieldsV1(e.Unknown),
	})
}

// String returns a safe representation for display.
func (e EndpointIdentityV1) String() string {
	return "endpoint_identity_v1:" + redactedValue
}

// GoString returns the same safe representation as String.
func (e EndpointIdentityV1) GoString() string {
	return e.String()
}

// Format writes the safe representation for any formatting verb.
func (e EndpointIdentityV1) Format(s fmt.State, verb rune) {
	io.WriteString(s, e.String())
}

// SessionIdentityV1 holds redacted session and remote identifiers for the v1 contract.
// Both identifiers require lowercase SHA-256 digests.
type SessionIdentityV1 struct {
	// ID contains the redacted identifier.
	ID RedactedID `json:"id"`
	// RemoteID contains the redacted remote identifier.
	RemoteID RedactedID `json:"remote_id"`
	// Unknown retains unknown fields in masked form.
	Unknown []UnknownField `json:"unknown,omitempty"`
}

// Validate checks the value against its contract and returns an error on invalid input.
func (s SessionIdentityV1) Validate() error {
	if err := validateRedactedIDV1(s.ID); err != nil {
		return fmt.Errorf("session id: %w", err)
	}
	if err := validateRedactedIDV1(s.RemoteID); err != nil {
		return fmt.Errorf("session remote_id: %w", err)
	}
	for i, unknown := range s.Unknown {
		if err := validateUnknownFieldV1(unknown); err != nil {
			return fmt.Errorf("unknown field %d: %w", i, err)
		}
	}
	return nil
}

// MarshalJSON validates the session and sorts its unknown fields before encoding.
func (s SessionIdentityV1) MarshalJSON() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	type sessionIdentityV1JSON struct {
		ID       RedactedID     `json:"id"`
		RemoteID RedactedID     `json:"remote_id"`
		Unknown  []UnknownField `json:"unknown,omitempty"`
	}
	return json.Marshal(sessionIdentityV1JSON{
		ID:       s.ID,
		RemoteID: s.RemoteID,
		Unknown:  sortedIdentityUnknownFieldsV1(s.Unknown),
	})
}

// String returns a safe representation for display.
func (s SessionIdentityV1) String() string {
	return "session_identity_v1:" + redactedValue
}

// GoString returns the same safe representation as String.
func (s SessionIdentityV1) GoString() string {
	return s.String()
}

// Format writes the safe representation for any formatting verb.
func (s SessionIdentityV1) Format(state fmt.State, verb rune) {
	io.WriteString(state, s.String())
}

// IdentityDocumentV1 holds a redacted v1 snapshot with canonical JSON ordering.
type IdentityDocumentV1 struct {
	// Contract selects the document contract.
	Contract ContractVersion `json:"contract"`
	// MaskTier specifies the required redaction tier.
	MaskTier MaskTier `json:"mask_tier"`
	// CapturedAt records when the snapshot was captured.
	CapturedAt time.Time `json:"captured_at"`
	// Local contains the local endpoint identity.
	Local EndpointIdentityV1 `json:"local"`
	// Remotes contains observed remote endpoint identities.
	Remotes []EndpointIdentityV1 `json:"remotes,omitempty"`
	// Sessions contains observed session identities.
	Sessions []SessionIdentityV1 `json:"sessions,omitempty"`
	// Unknown retains unknown fields in masked form.
	Unknown []UnknownField `json:"unknown,omitempty"`
}

// NewIdentityDocumentV1 constructs a v1 document with a UTC timestamp and a copy of the local unknown fields.
func NewIdentityDocumentV1(capturedAt time.Time, local EndpointIdentityV1) IdentityDocumentV1 {
	return IdentityDocumentV1{
		Contract:   IdentityContractV1,
		MaskTier:   MaskTierRedacted,
		CapturedAt: capturedAt.UTC(),
		Local:      copyEndpointIdentityV1(local),
	}
}

// Validate checks roles, timestamps, nested values and duplicate identity keys.
func (d IdentityDocumentV1) Validate() error {
	if d.Contract != IdentityContractV1 {
		return fmt.Errorf("identity contract must be %q", IdentityContractV1)
	}
	if d.MaskTier != MaskTierRedacted {
		return fmt.Errorf("identity mask tier must be %q", MaskTierRedacted)
	}
	if err := validateTimestampV1(d.CapturedAt); err != nil {
		return fmt.Errorf("identity captured_at: %w", err)
	}
	if err := d.Local.Validate(); err != nil {
		return fmt.Errorf("local identity: %w", err)
	}
	if d.Local.Role != EndpointRoleV1Local {
		return errors.New("local identity role must be local")
	}
	remoteKeys := make(map[string]int, len(d.Remotes))
	for i, remote := range d.Remotes {
		if err := remote.Validate(); err != nil {
			return fmt.Errorf("remote identity %d: %w", i, err)
		}
		if remote.Role != EndpointRoleV1Remote {
			return fmt.Errorf("remote identity %d role must be remote", i)
		}
		key := identityKeyV1(remote.ID)
		if prior, exists := remoteKeys[key]; exists {
			return fmt.Errorf("remote identity %d duplicates identity key from remote identity %d", i, prior)
		}
		remoteKeys[key] = i
	}
	sessionKeys := make(map[string]int, len(d.Sessions))
	for i, session := range d.Sessions {
		if err := session.Validate(); err != nil {
			return fmt.Errorf("session identity %d: %w", i, err)
		}
		key := identityKeyV1(session.ID)
		if prior, exists := sessionKeys[key]; exists {
			return fmt.Errorf("session identity %d duplicates identity key from session identity %d", i, prior)
		}
		sessionKeys[key] = i
	}
	for i, unknown := range d.Unknown {
		if err := validateUnknownFieldV1(unknown); err != nil {
			return fmt.Errorf("document unknown field %d: %w", i, err)
		}
	}
	return nil
}

// MarshalJSON validates the snapshot, sorts its collections and encodes the timestamp in UTC.
func (d IdentityDocumentV1) MarshalJSON() ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	type identityDocumentV1JSON struct {
		Contract   ContractVersion      `json:"contract"`
		MaskTier   MaskTier             `json:"mask_tier"`
		CapturedAt time.Time            `json:"captured_at"`
		Local      EndpointIdentityV1   `json:"local"`
		Remotes    []EndpointIdentityV1 `json:"remotes,omitempty"`
		Sessions   []SessionIdentityV1  `json:"sessions,omitempty"`
		Unknown    []UnknownField       `json:"unknown,omitempty"`
	}
	return json.Marshal(identityDocumentV1JSON{
		Contract:   d.Contract,
		MaskTier:   d.MaskTier,
		CapturedAt: d.CapturedAt.UTC(),
		Local:      canonicalEndpointIdentityV1(d.Local),
		Remotes:    sortedEndpointIdentitiesV1(d.Remotes),
		Sessions:   sortedSessionIdentitiesV1(d.Sessions),
		Unknown:    sortedIdentityUnknownFieldsV1(d.Unknown),
	})
}

// String returns a safe representation for display.
func (d IdentityDocumentV1) String() string {
	return "identity_document_v1:" + redactedValue
}

// GoString returns the same safe representation as String.
func (d IdentityDocumentV1) GoString() string {
	return d.String()
}

// Format writes the safe representation for any formatting verb.
func (d IdentityDocumentV1) Format(s fmt.State, verb rune) {
	io.WriteString(s, d.String())
}

func sortedEndpointIdentitiesV1(identities []EndpointIdentityV1) []EndpointIdentityV1 {
	sorted := make([]EndpointIdentityV1, len(identities))
	for i, identity := range identities {
		sorted[i] = canonicalEndpointIdentityV1(identity)
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		return endpointIdentitySortKeyV1(sorted[i]) < endpointIdentitySortKeyV1(sorted[j])
	})
	return sorted
}

func canonicalEndpointIdentityV1(identity EndpointIdentityV1) EndpointIdentityV1 {
	identity.Unknown = sortedIdentityUnknownFieldsV1(identity.Unknown)
	return identity
}

func copyEndpointIdentityV1(identity EndpointIdentityV1) EndpointIdentityV1 {
	identity.Unknown = append([]UnknownField(nil), identity.Unknown...)
	return identity
}

func endpointIdentitySortKeyV1(identity EndpointIdentityV1) string {
	return string(identity.Role) + "\x00" + identityKeyV1(identity.ID) + "\x00" + identityUnknownFieldsSortKeyV1(identity.Unknown)
}

func sortedSessionIdentitiesV1(sessions []SessionIdentityV1) []SessionIdentityV1 {
	sorted := make([]SessionIdentityV1, len(sessions))
	for i, session := range sessions {
		sorted[i] = session
		sorted[i].Unknown = sortedIdentityUnknownFieldsV1(session.Unknown)
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		return sessionIdentitySortKeyV1(sorted[i]) < sessionIdentitySortKeyV1(sorted[j])
	})
	return sorted
}

func sessionIdentitySortKeyV1(session SessionIdentityV1) string {
	return identityKeyV1(session.ID) + "\x00" + identityKeyV1(session.RemoteID) + "\x00" + identityUnknownFieldsSortKeyV1(session.Unknown)
}

func identityKeyV1(id RedactedID) string {
	return string(id.Kind) + "\x00" + id.Digest
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

func validateRedactedIDV1(id RedactedID) error {
	if err := id.Validate(); err != nil {
		return err
	}
	if id.Digest == "" {
		return errors.New("id digest is required")
	}
	if !validIdentityDigestV1(id.Digest) {
		return errors.New("id digest must use lowercase sha256:<64 hex chars>")
	}
	return nil
}

func validateUnknownFieldV1(unknown UnknownField) error {
	if err := unknown.Path.Validate(); err != nil {
		return err
	}
	if unknown.Value.Masked != redactedValue {
		return errors.New("unknown field value: opaque value must be redacted")
	}
	if unknown.Value.Digest != "" && !validIdentityDigestV1(unknown.Value.Digest) {
		return errors.New("unknown field value: opaque digest must use lowercase sha256:<64 hex chars>")
	}
	if unknown.Value.Size < 0 {
		return errors.New("unknown field value: opaque size must not be negative")
	}
	return nil
}

func validIdentityDigestV1(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
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
