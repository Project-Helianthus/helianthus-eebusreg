package eebusraw

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// MutationLabProfileContractV1 is the only Contract value ValidateMutationLabProfileV1 accepts;
// a profile carrying any other value is rejected rather than migrated.
const MutationLabProfileContractV1 = "helianthus.eebus.raw-mutation-lab-profile.v1"

const (
	maximumMutationLabAllowedValuesV1 = 32
	maximumMutationLabPredicatesV1    = 16
	maximumMutationLabEvidenceV1      = 32
)

// MutationLabProfileV1 is owner-loaded runtime configuration that stands in for complete native
// constraints on one exact WRITE target and an allowlist of values. It is never accepted from a
// request: a request names it only through ConstraintOverrideV1, and the mutation coordinator
// rejects the write with constraints_unknown unless exactly one loaded profile matches. Hashes
// commit canonical TypedValueV1 values; MaximumProbeTTLSeconds is in seconds (1..900) and
// ExpiresAt is an absolute UTC deadline for new forward writes, not for rollback or recovery.
// The contract is in https://github.com/Project-Helianthus/helianthus-docs-eebus/tree/main/api.
type MutationLabProfileV1 struct {
	Contract               string          `json:"contract"`
	ProfileID              string          `json:"profile_id"`
	Target                 FeatureTargetV1 `json:"target"`
	AllowedValueHashes     []HashV1        `json:"allowed_value_hashes"`
	RollbackValueHash      HashV1          `json:"rollback_value_hash"`
	MaximumProbeTTLSeconds uint64          `json:"maximum_probe_ttl_seconds"`
	SafetyPredicates       []string        `json:"safety_predicates"`
	EvidenceHashes         []HashV1        `json:"evidence_hashes"`
	ExpiresAt              time.Time       `json:"expires_at"`
}

// Clone returns a deep copy whose slices and target address share no memory with profile.
func (profile MutationLabProfileV1) Clone() MutationLabProfileV1 {
	profile.Target = profile.Target.Clone()
	profile.AllowedValueHashes = append([]HashV1(nil), profile.AllowedValueHashes...)
	profile.SafetyPredicates = append([]string(nil), profile.SafetyPredicates...)
	profile.EvidenceHashes = append([]HashV1(nil), profile.EvidenceHashes...)
	return profile
}

// String returns a fixed redacted marker so that logging a profile never prints its target.
func (MutationLabProfileV1) String() string {
	return "mutation_lab_profile_v1:[redacted]"
}

// GoString returns the same redacted marker as String, so %#v does not expose fields either.
func (profile MutationLabProfileV1) GoString() string {
	return profile.String()
}

// Format writes the redacted marker for every verb and flag, overriding fmt's field printing.
func (profile MutationLabProfileV1) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, profile.String())
}

// ValidateMutationLabProfileV1 checks the closed profile shape and returns an invalid_argument
// error, or secret_detected for secret-classified content, on the first violation. It requires a
// WRITE target, 1..32 allowed value hashes, 1..16 safety predicates and 1..32 evidence hashes,
// each list free of duplicates, a profile ID and predicates of 1..128 characters without
// surrounding space, and a UTC expiry. It does not compare the expiry with the current time or
// match the profile against a request.
func ValidateMutationLabProfileV1(profile MutationLabProfileV1) *ErrorV1 {
	if terminal := validateCanonicalDocumentV1(profile); terminal != nil {
		return terminal
	}
	if profile.Contract != MutationLabProfileContractV1 ||
		!exactBoundedMutationLabStringV1(profile.ProfileID, 128) ||
		profile.Target.Operation != OperationV1Write ||
		validateFeatureTargetV1(profile.Target) != nil ||
		!hashV1(profile.RollbackValueHash) ||
		profile.MaximumProbeTTLSeconds == 0 ||
		profile.MaximumProbeTTLSeconds > maximumProbeTTLSecondsV1 ||
		!timestampV1(profile.ExpiresAt) ||
		!uniqueMutationLabHashesV1(
			profile.AllowedValueHashes,
			maximumMutationLabAllowedValuesV1,
		) ||
		!uniqueMutationLabStringsV1(
			profile.SafetyPredicates,
			maximumMutationLabPredicatesV1,
		) ||
		!uniqueMutationLabHashesV1(
			profile.EvidenceHashes,
			maximumMutationLabEvidenceV1,
		) {
		return contractValidationErrorV1(ErrorCodeV1InvalidArgument)
	}
	return nil
}

func uniqueMutationLabHashesV1(values []HashV1, maximum int) bool {
	if len(values) == 0 || len(values) > maximum {
		return false
	}
	seen := make(map[HashV1]struct{}, len(values))
	for _, value := range values {
		if !hashV1(value) {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func uniqueMutationLabStringsV1(values []string, maximum int) bool {
	if len(values) == 0 || len(values) > maximum {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !exactBoundedMutationLabStringV1(value, 128) {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func exactBoundedMutationLabStringV1(value string, maximum int) bool {
	return value == strings.TrimSpace(value) && boundedStringV1(value, maximum)
}
