package eebusmutation

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"reflect"
	"strings"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

func validateRawMutationConfiguration(
	config rawMutationCoordinatorConfig,
	dependencies rawMutationCoordinatorDependencies,
) *eebusraw.ErrorV1 {
	if strings.TrimSpace(config.StateRoot) == "" ||
		config.RuntimeEpoch == nil ||
		config.RuntimeEpoch() == 0 ||
		config.Now == nil ||
		len(config.ReferenceKey) < 32 ||
		dependencies.Executor == nil ||
		dependencies.BindingAuthority == nil ||
		dependencies.TokenVerifier == nil ||
		dependencies.Policy == nil ||
		dependencies.Scheduler == nil {
		return mutationError(eebusraw.ErrorCodeV1InvalidArgument, false)
	}
	now := config.Now()
	if now.IsZero() {
		return mutationError(eebusraw.ErrorCodeV1InvalidArgument, false)
	}
	if len(config.LabProfiles) > 16 {
		return mutationError(eebusraw.ErrorCodeV1InvalidArgument, false)
	}
	seenProfiles := make(map[string]struct{}, len(config.LabProfiles))
	seenCommitments := make(map[eebusraw.HashV1]struct{}, len(config.LabProfiles))
	for _, profile := range config.LabProfiles {
		public := publicRawMutationLabProfile(profile)
		if eebusraw.ValidateMutationLabProfileV1(public) != nil {
			return mutationError(eebusraw.ErrorCodeV1InvalidArgument, false)
		}
		if _, duplicate := seenProfiles[profile.ProfileID]; duplicate {
			return mutationError(eebusraw.ErrorCodeV1InvalidArgument, false)
		}
		commitment, err := eebusraw.CanonicalSHA256V1(public)
		if err != nil {
			return mutationError(eebusraw.ErrorCodeV1InvalidArgument, false)
		}
		if _, duplicate := seenCommitments[commitment]; duplicate {
			return mutationError(eebusraw.ErrorCodeV1InvalidArgument, false)
		}
		seenProfiles[profile.ProfileID] = struct{}{}
		seenCommitments[commitment] = struct{}{}
	}
	return nil
}

func validateRawMutationSetRequest(request eebusraw.FeatureDataSetRequestV1) *eebusraw.ErrorV1 {
	return eebusraw.ValidateFeatureDataSetRequestV1(request)
}

func validateWriteTarget(target eebusraw.FeatureTargetV1) *eebusraw.ErrorV1 {
	if target.Operation != eebusraw.OperationV1Write {
		return mutationError(eebusraw.ErrorCodeV1UnsupportedOperation, false)
	}
	readTarget := target.Clone()
	readTarget.Operation = eebusraw.OperationV1Read
	if terminal := eebusraw.ValidateFeatureDataGetRequestV1(eebusraw.FeatureDataGetRequestV1{
		Targets: []eebusraw.FeatureTargetV1{readTarget},
	}); terminal != nil {
		return mutationError(terminal.Code, terminal.Retriable)
	}
	return nil
}

func verifyRawMutationReadToken(
	ctx context.Context,
	config rawMutationCoordinatorConfig,
	verifier rawMutationReadTokenVerifier,
	auth eebusraw.WriteAuthorizationV1,
	request eebusraw.FeatureDataSetRequestV1,
) (rawMutationReadTokenBinding, *eebusraw.ErrorV1) {
	binding, terminal := verifier.VerifyReadToken(ctx, request.ReadToken)
	if terminal != nil {
		return rawMutationReadTokenBinding{}, sanitizeMutationError(terminal)
	}
	if binding.PrincipalClass != auth.PrincipalClass ||
		binding.Scope != eebusraw.AuthScopeV1RawRead ||
		binding.Tool != eebusraw.ToolV1FeaturesDataGet ||
		binding.MaskTier != eebusraw.MaskTierRaw {
		return rawMutationReadTokenBinding{}, mutationError(eebusraw.ErrorCodeV1PermissionDenied, false)
	}
	currentEpoch := config.RuntimeEpoch()
	if binding.Runtime.RuntimeEpoch != currentEpoch {
		return rawMutationReadTokenBinding{}, mutationError(eebusraw.ErrorCodeV1RuntimeEpochMismatch, false)
	}
	if binding.Runtime.ConnectionGeneration == 0 {
		return rawMutationReadTokenBinding{}, mutationError(eebusraw.ErrorCodeV1ConnectionGenerationMismatch, false)
	}
	if binding.ExpiresAt.IsZero() || !binding.ExpiresAt.After(config.Now()) {
		return rawMutationReadTokenBinding{}, mutationError(eebusraw.ErrorCodeV1StaleReadToken, false)
	}
	readTarget := request.Target.Clone()
	readTarget.Operation = eebusraw.OperationV1Read
	if !reflect.DeepEqual(binding.Target, readTarget) {
		return rawMutationReadTokenBinding{}, mutationError(eebusraw.ErrorCodeV1StaleReadToken, false)
	}
	requestHash, err := eebusraw.CanonicalSHA256V1(eebusraw.FeatureDataGetRequestV1{
		Targets: []eebusraw.FeatureTargetV1{readTarget},
	})
	if err != nil || binding.RequestHash != requestHash || binding.BeforeImageHash == "" {
		return rawMutationReadTokenBinding{}, mutationError(eebusraw.ErrorCodeV1StaleReadToken, false)
	}
	binding.Target = binding.Target.Clone()
	return binding, nil
}

func validateCurrentRawMutationBinding(
	authority rawMutationRuntimeBindingAuthority,
	target eebusraw.FeatureTargetV1,
	expected eebusraw.RuntimeBindingV1,
) *eebusraw.ErrorV1 {
	current, terminal := authority.CurrentRuntimeBinding(target.Clone())
	if terminal != nil {
		return sanitizeMutationError(terminal)
	}
	if current.RuntimeEpoch != expected.RuntimeEpoch {
		return mutationError(eebusraw.ErrorCodeV1RuntimeEpochMismatch, false)
	}
	if current.ConnectionGeneration == 0 ||
		current.ConnectionGeneration != expected.ConnectionGeneration {
		return mutationError(eebusraw.ErrorCodeV1ConnectionGenerationMismatch, false)
	}
	return nil
}

func validateGuardRead(
	result rawMutationReadResult,
	terminal *eebusraw.ErrorV1,
	binding rawMutationReadTokenBinding,
	request eebusraw.FeatureDataSetRequestV1,
) *eebusraw.ErrorV1 {
	if terminal != nil {
		return sanitizeMutationError(terminal)
	}
	if !result.Full || !result.Trustworthy || result.Value.Validate() != nil {
		return mutationError(eebusraw.ErrorCodeV1DecodeError, false)
	}
	if result.Runtime.RuntimeEpoch != binding.Runtime.RuntimeEpoch {
		return mutationError(eebusraw.ErrorCodeV1RuntimeEpochMismatch, false)
	}
	if result.Runtime.ConnectionGeneration != binding.Runtime.ConnectionGeneration {
		return mutationError(eebusraw.ErrorCodeV1ConnectionGenerationMismatch, false)
	}
	beforeHash, err := result.Value.ComputeHash()
	if err != nil || beforeHash != binding.BeforeImageHash {
		return mutationError(eebusraw.ErrorCodeV1CASMismatch, false)
	}
	if request.ExpectedCurrent != nil && !typedValuesEqual(result.Value, *request.ExpectedCurrent) {
		return mutationError(eebusraw.ErrorCodeV1CASMismatch, false)
	}
	return nil
}

func validateRawMutationPolicy(
	config rawMutationCoordinatorConfig,
	decision rawMutationPolicyDecision,
	request eebusraw.FeatureDataSetRequestV1,
	before eebusraw.TypedValueV1,
) *eebusraw.ErrorV1 {
	if !decision.FullWrite {
		return mutationError(eebusraw.ErrorCodeV1UnsupportedOperation, false)
	}
	if decision.Changeability != eebusraw.ChangeabilityV1True ||
		!decision.LabAllowlisted ||
		!decision.RollbackRepresentable ||
		len(decision.ConstraintFailures) != 0 ||
		len(decision.SafetyFailures) != 0 {
		return mutationError(eebusraw.ErrorCodeV1ConstraintFailure, false)
	}
	if decision.ConstraintsKnown {
		if decision.LabProfileID == "" &&
			len(decision.EvidenceHashes) == 0 &&
			len(decision.SafetyPredicates) == 0 {
			return nil
		}
	}
	profile, matches := exactRawMutationLabProfile(config, request, before)
	if matches != 1 ||
		decision.LabProfileID != profile.ProfileID ||
		!reflect.DeepEqual(decision.EvidenceHashes, profile.EvidenceHashes) ||
		!reflect.DeepEqual(decision.SafetyPredicates, profile.SafetyPredicates) {
		return mutationError(eebusraw.ErrorCodeV1ConstraintsUnknown, false)
	}
	return nil
}

func exactRawMutationLabProfile(
	config rawMutationCoordinatorConfig,
	request eebusraw.FeatureDataSetRequestV1,
	before eebusraw.TypedValueV1,
) (rawMutationLabProfile, int) {
	beforeHash, err := before.ComputeHash()
	if err != nil {
		return rawMutationLabProfile{}, 0
	}
	return exactRawMutationLabProfileForHash(config, request, beforeHash)
}

func exactRawMutationLabProfileForHash(
	config rawMutationCoordinatorConfig,
	request eebusraw.FeatureDataSetRequestV1,
	beforeHash eebusraw.HashV1,
) (rawMutationLabProfile, int) {
	override := request.ConstraintsOverride
	if override == nil ||
		strings.TrimSpace(override.Justification) == "" ||
		!override.ExpiresAt.After(config.Now()) {
		return rawMutationLabProfile{}, 0
	}
	requestedHash, err := request.Value.ComputeHash()
	if err != nil {
		return rawMutationLabProfile{}, 0
	}
	matches := 0
	var matched rawMutationLabProfile
	for _, profile := range config.LabProfiles {
		if profile.ProfileID != override.ProfileID ||
			!profile.ExpiresAt.After(config.Now()) ||
			override.ExpiresAt.After(profile.ExpiresAt) ||
			!reflect.DeepEqual(profile.Target, request.Target) ||
			profile.RollbackValueHash != beforeHash ||
			(request.Mode == eebusraw.ModeV1Probe &&
				request.ProbeTTLSeconds > profile.MaximumProbeTTLSeconds) {
			continue
		}
		allowed := false
		for _, hash := range profile.AllowedValueHashes {
			if hash == requestedHash {
				allowed = true
				break
			}
		}
		if allowed {
			matches++
			matched = cloneRawMutationLabProfile(profile)
		}
	}
	return matched, matches
}

func publicRawMutationLabProfile(
	profile rawMutationLabProfile,
) eebusraw.MutationLabProfileV1 {
	return eebusraw.MutationLabProfileV1{
		Contract:               profile.Contract,
		ProfileID:              profile.ProfileID,
		Target:                 profile.Target.Clone(),
		AllowedValueHashes:     append([]eebusraw.HashV1(nil), profile.AllowedValueHashes...),
		RollbackValueHash:      profile.RollbackValueHash,
		MaximumProbeTTLSeconds: profile.MaximumProbeTTLSeconds,
		SafetyPredicates:       append([]string(nil), profile.SafetyPredicates...),
		EvidenceHashes:         append([]eebusraw.HashV1(nil), profile.EvidenceHashes...),
		ExpiresAt:              profile.ExpiresAt,
	}
}

func rawMutationIdentityHash(
	referenceKey []byte,
	runtimeEpoch uint64,
	principal string,
	tool eebusraw.ToolV1,
	idempotencyKey string,
) (eebusraw.HashV1, error) {
	encoded, err := canonicalBytes(struct {
		RuntimeEpoch   uint64          `json:"runtime_epoch"`
		Principal      string          `json:"principal"`
		Tool           eebusraw.ToolV1 `json:"tool"`
		IdempotencyKey string          `json:"idempotency_key"`
	}{
		RuntimeEpoch:   runtimeEpoch,
		Principal:      principal,
		Tool:           tool,
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, referenceKey)
	_, _ = mac.Write(encoded)
	return eebusraw.HashV1("sha256:" + hex.EncodeToString(mac.Sum(nil))), nil
}

func rawMutationReference(referenceKey []byte, identity eebusraw.HashV1) string {
	mac := hmac.New(sha256.New, referenceKey)
	_, _ = mac.Write([]byte("mutation-reference-v1\x00"))
	_, _ = mac.Write([]byte(identity))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func rawMutationPrincipalHash(principal string) (eebusraw.HashV1, error) {
	return eebusraw.CanonicalSHA256V1(struct {
		Principal string `json:"principal"`
	}{Principal: principal})
}

func canonicalBytes(value any) ([]byte, error) {
	hash, err := eebusraw.CanonicalSHA256V1(value)
	if err != nil {
		return nil, err
	}
	return []byte(hash), nil
}

func typedValuesEqual(left, right eebusraw.TypedValueV1) bool {
	leftHash, leftErr := left.ComputeHash()
	rightHash, rightErr := right.ComputeHash()
	return leftErr == nil && rightErr == nil && leftHash == rightHash
}

func sanitizeMutationError(terminal *eebusraw.ErrorV1) *eebusraw.ErrorV1 {
	if terminal == nil {
		return nil
	}
	return mutationError(terminal.Code, terminal.Retriable)
}

func mutationError(code eebusraw.ErrorCodeV1, retriable bool) *eebusraw.ErrorV1 {
	message := "raw mutation request failed"
	switch code {
	case eebusraw.ErrorCodeV1PermissionDenied:
		message = "raw mutation authorization denied"
	case eebusraw.ErrorCodeV1InvalidArgument:
		message = "raw mutation request is invalid"
	case eebusraw.ErrorCodeV1UnsupportedOperation:
		message = "raw mutation operation is unsupported"
	case eebusraw.ErrorCodeV1ConstraintsUnknown:
		message = "raw mutation constraints are unknown"
	case eebusraw.ErrorCodeV1ConstraintFailure:
		message = "raw mutation safety constraint failed"
	case eebusraw.ErrorCodeV1StaleReadToken:
		message = "raw mutation read token is stale"
	case eebusraw.ErrorCodeV1CASMismatch:
		message = "raw mutation compare-and-swap guard failed"
	case eebusraw.ErrorCodeV1RuntimeEpochMismatch:
		message = "raw mutation runtime epoch changed"
	case eebusraw.ErrorCodeV1ConnectionGenerationMismatch:
		message = "raw mutation connection generation changed"
	case eebusraw.ErrorCodeV1IdempotencyConflict:
		message = "raw mutation idempotency key conflicts"
	case eebusraw.ErrorCodeV1WriterBusy:
		message = "raw mutation writer is busy"
	case eebusraw.ErrorCodeV1NoEffect:
		message = "raw mutation final value equals the before-image"
	case eebusraw.ErrorCodeV1OutcomeUnknown:
		message = "raw mutation outcome is unknown"
	case eebusraw.ErrorCodeV1Conflict:
		message = "raw mutation readback conflicts"
	case eebusraw.ErrorCodeV1RollbackFailed:
		message = "raw mutation rollback could not be verified"
	case eebusraw.ErrorCodeV1NotFound:
		message = "raw mutation was not found"
	case eebusraw.ErrorCodeV1SecretDetected:
		message = "secret-classified raw mutation input was rejected"
	case eebusraw.ErrorCodeV1Internal:
		message = "raw mutation internal failure"
	}
	return eebusraw.NewErrorV1(code, message, retriable, eebusraw.SourceLayerV1Runtime)
}

func internalMutationError() *eebusraw.ErrorV1 {
	return mutationError(eebusraw.ErrorCodeV1Internal, false)
}

func normalizedNow(now func() time.Time) time.Time {
	return now().UTC()
}
