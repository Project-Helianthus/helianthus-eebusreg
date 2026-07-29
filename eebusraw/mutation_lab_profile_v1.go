package eebusraw

import (
	"fmt"
	"io"
	"strings"
	"time"
)

const MutationLabProfileContractV1 = "helianthus.eebus.raw-mutation-lab-profile.v1"

const (
	maximumMutationLabAllowedValuesV1 = 32
	maximumMutationLabPredicatesV1    = 16
	maximumMutationLabEvidenceV1      = 32
)

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

func (profile MutationLabProfileV1) Clone() MutationLabProfileV1 {
	profile.Target = profile.Target.Clone()
	profile.AllowedValueHashes = append([]HashV1(nil), profile.AllowedValueHashes...)
	profile.SafetyPredicates = append([]string(nil), profile.SafetyPredicates...)
	profile.EvidenceHashes = append([]HashV1(nil), profile.EvidenceHashes...)
	return profile
}

func (MutationLabProfileV1) String() string {
	return "mutation_lab_profile_v1:[redacted]"
}

func (profile MutationLabProfileV1) GoString() string {
	return profile.String()
}

func (profile MutationLabProfileV1) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, profile.String())
}

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
