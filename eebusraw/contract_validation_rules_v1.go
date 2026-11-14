package eebusraw

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

func validateCanonicalDocumentV1(value any) *ErrorV1 {
	if _, err := CanonicalSHA256V1(value); err != nil {
		if errors.Is(err, ErrSecretDetected) {
			return contractValidationErrorV1(ErrorCodeV1SecretDetected)
		}
		return contractValidationErrorV1(ErrorCodeV1InvalidArgument)
	}
	return nil
}

func contractValidationErrorV1(code ErrorCodeV1) *ErrorV1 {
	message := "raw eeBUS contract validation failed"
	if code == ErrorCodeV1SecretDetected {
		message = "secret-classified raw eeBUS value was rejected"
	}
	return NewErrorV1(code, message, false, SourceLayerV1Validation)
}

func opaqueReferenceV1(value string) bool {
	if len(value) != 43 {
		return false
	}
	for _, current := range value {
		if current >= 'A' && current <= 'Z' ||
			current >= 'a' && current <= 'z' ||
			current >= '0' && current <= '9' ||
			current == '_' ||
			current == '-' {
			continue
		}
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return err == nil &&
		len(decoded) == sha256.Size &&
		base64.RawURLEncoding.EncodeToString(decoded) == value
}

func idempotencyKeyV1(value string) bool {
	if len(value) < 16 || len(value) > 128 {
		return false
	}
	for _, current := range value {
		if current >= 'A' && current <= 'Z' ||
			current >= 'a' && current <= 'z' ||
			current >= '0' && current <= '9' ||
			strings.ContainsRune("._~-", current) {
			continue
		}
		return false
	}
	return true
}

func hashV1(value HashV1) bool {
	text := string(value)
	if len(text) != len("sha256:")+64 || !strings.HasPrefix(text, "sha256:") {
		return false
	}
	for _, current := range strings.TrimPrefix(text, "sha256:") {
		if current < '0' || current > '9' && current < 'a' || current > 'f' {
			return false
		}
	}
	return true
}

func runtimeBindingV1(value RuntimeBindingV1) bool {
	return value.RuntimeEpoch != 0 &&
		value.ConnectionGeneration != 0 &&
		value.RuntimeEpoch <= uint64(typedValueMaximumSafeInteger) &&
		value.ConnectionGeneration <= uint64(typedValueMaximumSafeInteger)
}

func timestampV1(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC
}

func boundedStringV1(value string, maximum int) bool {
	value = strings.TrimSpace(value)
	return value != "" &&
		utf8.ValidString(value) &&
		utf8.RuneCountInString(value) <= maximum
}

func optionalBoundedStringV1(value string, maximum int) bool {
	return value == "" || boundedStringV1(value, maximum)
}

func typedValueNonNullV1(value TypedValueV1) bool {
	return value.Validate() == nil && value.Value() != nil
}

func typedScalarV1(value TypedValueV1) bool {
	if !typedValueNonNullV1(value) {
		return false
	}
	switch value.Value().(type) {
	case bool, string, int64:
		return true
	default:
		return false
	}
}

func validProtocolClassifierV1(value string) bool {
	switch value {
	case "READ", "WRITE", "REPLY", "RESULT":
		return true
	default:
		return false
	}
}

func validChangeabilityV1(value ChangeabilityV1) bool {
	return value == ChangeabilityV1Unknown ||
		value == ChangeabilityV1False ||
		value == ChangeabilityV1True
}

func validInitialMutationStateV1(value MutationStateV1) bool {
	return value == MutationStateV1Prepared ||
		value == MutationStateV1FailedNoContact
}

func validMutationTransitionV1(from, to MutationStateV1) bool {
	switch from {
	case MutationStateV1Prepared:
		return to == MutationStateV1DispatchIntent
	case MutationStateV1DispatchIntent:
		return to == MutationStateV1ReplyObserved ||
			to == MutationStateV1OutcomeUnknown ||
			to == MutationStateV1FailedNoContact
	case MutationStateV1ReplyObserved:
		return to == MutationStateV1VerifyPending ||
			to == MutationStateV1OutcomeUnknown
	case MutationStateV1VerifyPending:
		return to == MutationStateV1Applied ||
			to == MutationStateV1ProbeActive ||
			to == MutationStateV1Rejected ||
			to == MutationStateV1Conflict ||
			to == MutationStateV1OutcomeUnknown
	case MutationStateV1Applied, MutationStateV1ProbeActive:
		return to == MutationStateV1RollbackIntent
	case MutationStateV1RollbackIntent:
		return to == MutationStateV1RollbackDispatchIntent ||
			to == MutationStateV1RolledBack ||
			to == MutationStateV1Conflict ||
			to == MutationStateV1OutcomeUnknown
	case MutationStateV1RollbackDispatchIntent:
		return to == MutationStateV1RollbackReplyObserved ||
			to == MutationStateV1RolledBack ||
			to == MutationStateV1Conflict ||
			to == MutationStateV1OutcomeUnknown
	case MutationStateV1RollbackReplyObserved:
		return to == MutationStateV1RollbackVerifyPending ||
			to == MutationStateV1RolledBack ||
			to == MutationStateV1Conflict ||
			to == MutationStateV1OutcomeUnknown
	case MutationStateV1RollbackVerifyPending:
		return to == MutationStateV1RolledBack ||
			to == MutationStateV1Conflict ||
			to == MutationStateV1OutcomeUnknown
	case MutationStateV1OutcomeUnknown:
		return to == MutationStateV1Applied ||
			to == MutationStateV1ProbeActive ||
			to == MutationStateV1NoEffect ||
			to == MutationStateV1Conflict ||
			to == MutationStateV1RolledBack
	default:
		return false
	}
}

func validateRollbackBeforeV1(rollback *RollbackV1, beforeHash HashV1) error {
	if rollback == nil || rollback.Before.Validate() != nil {
		return errors.New("rollback before-image is invalid")
	}
	hash, err := rollback.Before.ComputeHash()
	if err != nil || hash != beforeHash {
		return errors.New("rollback before-image is not top-level bound")
	}
	return nil
}

func validMutationStateV1(value MutationStateV1) bool {
	switch value {
	case MutationStateV1Prepared,
		MutationStateV1DispatchIntent,
		MutationStateV1ReplyObserved,
		MutationStateV1VerifyPending,
		MutationStateV1Applied,
		MutationStateV1ProbeActive,
		MutationStateV1RollbackIntent,
		MutationStateV1RollbackDispatchIntent,
		MutationStateV1RollbackReplyObserved,
		MutationStateV1RollbackVerifyPending,
		MutationStateV1RolledBack,
		MutationStateV1OutcomeUnknown,
		MutationStateV1Conflict,
		MutationStateV1FailedNoContact,
		MutationStateV1Rejected,
		MutationStateV1NoEffect:
		return true
	default:
		return false
	}
}

func validErrorCodeV1(value ErrorCodeV1) bool {
	switch value {
	case ErrorCodeV1PermissionDenied,
		ErrorCodeV1InvalidArgument,
		ErrorCodeV1UnsupportedOperation,
		ErrorCodeV1PartialOperationForbidden,
		ErrorCodeV1ConstraintsUnknown,
		ErrorCodeV1ConstraintFailure,
		ErrorCodeV1StaleReadToken,
		ErrorCodeV1CASMismatch,
		ErrorCodeV1RuntimeEpochMismatch,
		ErrorCodeV1ConnectionGenerationMismatch,
		ErrorCodeV1IdempotencyConflict,
		ErrorCodeV1WriterBusy,
		ErrorCodeV1Disconnected,
		ErrorCodeV1Timeout,
		ErrorCodeV1Cancelled,
		ErrorCodeV1RemoteError,
		ErrorCodeV1TypedEmpty,
		ErrorCodeV1DecodeError,
		ErrorCodeV1PartialResult,
		ErrorCodeV1OutcomeUnknown,
		ErrorCodeV1Conflict,
		ErrorCodeV1RollbackFailed,
		ErrorCodeV1NoEffect,
		ErrorCodeV1NotFound,
		ErrorCodeV1SecretDetected,
		ErrorCodeV1Internal:
		return true
	default:
		return false
	}
}

func validSourceLayerV1(value SourceLayerV1) bool {
	switch value {
	case SourceLayerV1Authorization,
		SourceLayerV1Executor,
		SourceLayerV1SpineRoundTrip,
		SourceLayerV1Remote:
		return true
	default:
		return false
	}
}

func boolPointerV1(value *bool, expected bool) bool {
	return value != nil && *value == expected
}

func hashPointersEqualV1(left, right *HashV1) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func typedValuesEqualV1(left, right TypedValueV1) bool {
	leftHash, leftErr := left.ComputeHash()
	rightHash, rightErr := right.ComputeHash()
	return leftErr == nil && rightErr == nil && leftHash == rightHash
}
