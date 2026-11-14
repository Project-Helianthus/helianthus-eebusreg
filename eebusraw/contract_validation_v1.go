package eebusraw

import (
	"errors"
	"reflect"
	"time"
)

const (
	maximumProbeTTLSecondsV1 = 900
	maximumAuditEntriesV1    = 256
	maximumSafeIntegerV1     = uint64(typedValueMaximumSafeInteger)
)

// ValidateFeatureDataSetRequestV1 is a pure check of a governed write request that touches no
// runtime state. It returns nil for a valid request; otherwise secret_detected for
// secret-classified content, unsupported_operation for a non-WRITE target, and invalid_argument
// for a malformed target, value, read token or idempotency key, a probe TTL that is set in apply
// mode or outside 1 to 900 seconds in probe mode, or an incomplete constraints override.
func ValidateFeatureDataSetRequestV1(request FeatureDataSetRequestV1) *ErrorV1 {
	if terminal := validateCanonicalDocumentV1(request); terminal != nil {
		return terminal
	}
	if request.Target.Operation != OperationV1Write {
		return contractValidationErrorV1(ErrorCodeV1UnsupportedOperation)
	}
	if err := validateFeatureTargetV1(request.Target); err != nil ||
		request.Value.Validate() != nil ||
		!opaqueReferenceV1(request.ReadToken) ||
		!idempotencyKeyV1(request.IdempotencyKey) {
		return contractValidationErrorV1(ErrorCodeV1InvalidArgument)
	}
	if request.ExpectedCurrent != nil && request.ExpectedCurrent.Validate() != nil {
		return contractValidationErrorV1(ErrorCodeV1InvalidArgument)
	}
	switch request.Mode {
	case ModeV1Apply:
		if request.ProbeTTLSeconds != 0 {
			return contractValidationErrorV1(ErrorCodeV1InvalidArgument)
		}
	case ModeV1Probe:
		if request.ProbeTTLSeconds == 0 ||
			request.ProbeTTLSeconds > maximumProbeTTLSecondsV1 {
			return contractValidationErrorV1(ErrorCodeV1InvalidArgument)
		}
	default:
		return contractValidationErrorV1(ErrorCodeV1InvalidArgument)
	}
	if override := request.ConstraintsOverride; override != nil {
		if !boundedStringV1(override.ProfileID, 128) ||
			!boundedStringV1(override.Justification, 1024) ||
			!timestampV1(override.ExpiresAt) {
			return contractValidationErrorV1(ErrorCodeV1InvalidArgument)
		}
	}
	return nil
}

// ValidateMutationGetRequestV1 is a pure check that returns nil when MutationRef is a well-formed
// opaque reference, secret_detected for secret-classified content and invalid_argument otherwise.
// It does not check that the referenced mutation exists.
func ValidateMutationGetRequestV1(request MutationGetRequestV1) *ErrorV1 {
	if terminal := validateCanonicalDocumentV1(request); terminal != nil {
		return terminal
	}
	if !opaqueReferenceV1(request.MutationRef) {
		return contractValidationErrorV1(ErrorCodeV1InvalidArgument)
	}
	return nil
}

// ValidateMutationRollbackRequestV1 is a pure check that returns nil when MutationRef is a
// well-formed opaque reference and IdempotencyKey is 16 to 128 unreserved URL characters,
// secret_detected for secret-classified content and invalid_argument otherwise. Whether the
// mutation may still be rolled back is decided by the runtime, not here.
func ValidateMutationRollbackRequestV1(request MutationRollbackRequestV1) *ErrorV1 {
	if terminal := validateCanonicalDocumentV1(request); terminal != nil {
		return terminal
	}
	if !opaqueReferenceV1(request.MutationRef) ||
		!idempotencyKeyV1(request.IdempotencyKey) {
		return contractValidationErrorV1(ErrorCodeV1InvalidArgument)
	}
	return nil
}

// ValidateFeaturesGetDataV1 checks a feature-description reply against the request that produced
// it, so a consumer can refuse a reply before using it. It first validates the request as
// ValidateFeaturesGetRequestV1 does, then returns decode_error unless the reply names the
// requested feature, carries a valid runtime binding, a UTC timestamp and a live or cache source,
// lists at most 512 uniquely named functions with valid constraints, and its DataHash matches
// its content.
func ValidateFeaturesGetDataV1(
	request FeaturesGetRequestV1,
	data FeaturesGetDataV1,
) *ErrorV1 {
	if terminal := ValidateFeaturesGetRequestV1(request); terminal != nil {
		return terminal
	}
	if terminal := validateCanonicalDocumentV1(data); terminal != nil {
		return terminal
	}
	if !reflect.DeepEqual(request.Target, data.Feature) ||
		!runtimeBindingV1(data.Runtime) ||
		!timestampV1(data.DataTimestamp) ||
		(data.Source != ObservationSourceV1Live &&
			data.Source != ObservationSourceV1Cache) ||
		len(data.Functions) > 512 {
		return contractValidationErrorV1(ErrorCodeV1DecodeError)
	}
	seen := make(map[string]struct{}, len(data.Functions))
	for _, function := range data.Functions {
		if !boundedStringV1(function.Function, 256) ||
			!optionalBoundedStringV1(function.Description, 4096) ||
			!validChangeabilityV1(function.Changeable) ||
			validateConstraintSetV1(function.Constraints) != nil {
			return contractValidationErrorV1(ErrorCodeV1DecodeError)
		}
		if _, duplicate := seen[function.Function]; duplicate {
			return contractValidationErrorV1(ErrorCodeV1DecodeError)
		}
		seen[function.Function] = struct{}{}
	}
	computed, err := data.ComputeDataHash()
	if err != nil || !hashV1(data.DataHash) || computed != data.DataHash {
		return contractValidationErrorV1(ErrorCodeV1DecodeError)
	}
	return nil
}

// ValidateFeatureDataGetDataV1 checks a read result and its terminal error against the request
// that produced them. Every requested target is answered exactly once, by a result in request
// order or by a failure at its index. A complete result has no terminal and no failure; an
// incomplete one has at least one result and one failure and a partial_result terminal. All
// results share one runtime binding. Any breach returns decode_error, after the request has been
// validated as ValidateFeatureDataGetRequestV1 does.
func ValidateFeatureDataGetDataV1(
	request FeatureDataGetRequestV1,
	data FeatureDataGetDataV1,
	terminal *ErrorV1,
) *ErrorV1 {
	if requestTerminal := ValidateFeatureDataGetRequestV1(request); requestTerminal != nil {
		return requestTerminal
	}
	if documentTerminal := validateCanonicalDocumentV1(struct {
		Data     FeatureDataGetDataV1 `json:"data"`
		Terminal *ErrorV1             `json:"terminal"`
	}{Data: data, Terminal: terminal}); documentTerminal != nil {
		return documentTerminal
	}
	if len(data.Results) > len(request.Targets) ||
		len(data.Failures) > len(request.Targets) ||
		len(data.Results)+len(data.Failures) != len(request.Targets) {
		return contractValidationErrorV1(ErrorCodeV1DecodeError)
	}
	if data.Complete {
		if terminal != nil || len(data.Failures) != 0 ||
			len(data.Results) != len(request.Targets) {
			return contractValidationErrorV1(ErrorCodeV1DecodeError)
		}
	} else if terminal == nil ||
		terminal.Code != ErrorCodeV1PartialResult ||
		len(data.Results) == 0 ||
		len(data.Failures) == 0 {
		return contractValidationErrorV1(ErrorCodeV1DecodeError)
	}
	if terminal != nil && validateErrorV1(*terminal) != nil {
		return contractValidationErrorV1(ErrorCodeV1DecodeError)
	}

	failures := make(map[uint64]ReadFailureV1, len(data.Failures))
	for _, failure := range data.Failures {
		if failure.TargetIndex >= uint64(len(request.Targets)) ||
			!reflect.DeepEqual(failure.Target, request.Targets[failure.TargetIndex]) ||
			validateErrorV1(failure.Error) != nil ||
			failure.Error.Code == ErrorCodeV1PartialResult ||
			failure.Error.Details != nil &&
				failure.Error.Details.TargetIndex != nil &&
				*failure.Error.Details.TargetIndex != failure.TargetIndex {
			return contractValidationErrorV1(ErrorCodeV1DecodeError)
		}
		if _, duplicate := failures[failure.TargetIndex]; duplicate {
			return contractValidationErrorV1(ErrorCodeV1DecodeError)
		}
		failures[failure.TargetIndex] = failure
	}

	var binding RuntimeBindingV1
	resultIndex := 0
	for targetIndex, target := range request.Targets {
		if _, failed := failures[uint64(targetIndex)]; failed {
			continue
		}
		if resultIndex >= len(data.Results) {
			return contractValidationErrorV1(ErrorCodeV1DecodeError)
		}
		observation := data.Results[resultIndex]
		if !reflect.DeepEqual(observation.Target, target) ||
			validateReadObservationV1(observation) != nil {
			return contractValidationErrorV1(ErrorCodeV1DecodeError)
		}
		if resultIndex == 0 {
			binding = observation.Runtime
		} else if observation.Runtime != binding {
			return contractValidationErrorV1(ErrorCodeV1DecodeError)
		}
		resultIndex++
	}
	if resultIndex != len(data.Results) {
		return contractValidationErrorV1(ErrorCodeV1DecodeError)
	}
	return nil
}

// ValidateMutationV1 checks a mutation record as a consumer receives it and returns decode_error
// when the record is inconsistent: identity, binding, timestamps or mode are malformed, the audit
// trail is empty, longer than 256 entries, not a hash-chained sequence of legal transitions or
// does not end in State, the rollback before-image differs from Before, or the evidence present does not match
// what State requires. Secret-classified content returns secret_detected.
func ValidateMutationV1(mutation MutationV1) *ErrorV1 {
	if terminal := validateCanonicalDocumentV1(mutation); terminal != nil {
		return terminal
	}
	if !opaqueReferenceV1(mutation.MutationRef) ||
		mutation.Target.Operation != OperationV1Write ||
		validateFeatureTargetV1(mutation.Target) != nil ||
		!runtimeBindingV1(mutation.Runtime) ||
		mutation.Before.Validate() != nil ||
		mutation.Requested.Validate() != nil ||
		!timestampV1(mutation.CreatedAt) ||
		!timestampV1(mutation.UpdatedAt) ||
		mutation.UpdatedAt.Before(mutation.CreatedAt) ||
		len(mutation.Audit) == 0 ||
		len(mutation.Audit) > maximumAuditEntriesV1 ||
		validateAuditV1(mutation.Audit, mutation.CreatedAt, mutation.UpdatedAt) != nil ||
		mutation.Audit[len(mutation.Audit)-1].State != mutation.State {
		return contractValidationErrorV1(ErrorCodeV1DecodeError)
	}
	if mutation.ObservedAfter != nil && mutation.ObservedAfter.Validate() != nil {
		return contractValidationErrorV1(ErrorCodeV1DecodeError)
	}
	if mutation.Error != nil && validateErrorV1(*mutation.Error) != nil {
		return contractValidationErrorV1(ErrorCodeV1DecodeError)
	}
	switch mutation.Mode {
	case ModeV1Apply:
		if mutation.ProbeDeadline != nil {
			return contractValidationErrorV1(ErrorCodeV1DecodeError)
		}
	case ModeV1Probe:
		if mutation.ProbeDeadline == nil || !timestampV1(*mutation.ProbeDeadline) {
			return contractValidationErrorV1(ErrorCodeV1DecodeError)
		}
	default:
		return contractValidationErrorV1(ErrorCodeV1DecodeError)
	}

	beforeHash, beforeErr := mutation.Before.ComputeHash()
	requestedHash, requestedErr := mutation.Requested.ComputeHash()
	if beforeErr != nil || requestedErr != nil {
		return contractValidationErrorV1(ErrorCodeV1DecodeError)
	}
	if mutation.Rollback != nil &&
		validateRollbackBeforeV1(mutation.Rollback, beforeHash) != nil {
		return contractValidationErrorV1(ErrorCodeV1DecodeError)
	}
	return validateMutationStateV1(mutation, beforeHash, requestedHash)
}

func validateMutationStateV1(
	mutation MutationV1,
	beforeHash HashV1,
	requestedHash HashV1,
) *ErrorV1 {
	noCommonEvidence := mutation.Rollback == nil &&
		mutation.Error == nil &&
		mutation.ApplyVerification == nil &&
		mutation.ConflictEvidence == nil &&
		mutation.NoContactEvidence == nil &&
		mutation.RejectionVerification == nil &&
		mutation.NoEffectVerification == nil &&
		mutation.OutcomeEvidence == nil
	switch mutation.State {
	case MutationStateV1Prepared, MutationStateV1DispatchIntent:
		if mutation.ProtocolAccepted != nil ||
			mutation.ObservedAfter != nil ||
			!noCommonEvidence {
			return contractValidationErrorV1(ErrorCodeV1DecodeError)
		}
	case MutationStateV1ReplyObserved, MutationStateV1VerifyPending:
		if mutation.ProtocolAccepted == nil ||
			mutation.ObservedAfter != nil ||
			!noCommonEvidence {
			return contractValidationErrorV1(ErrorCodeV1DecodeError)
		}
	case MutationStateV1Applied, MutationStateV1ProbeActive:
		if mutation.ObservedAfter == nil ||
			validateApplyVerificationV1(
				mutation.ApplyVerification,
				mutation.ObservedAfter,
				requestedHash,
			) != nil ||
			mutation.Rollback != nil ||
			mutation.Error != nil ||
			mutation.ConflictEvidence != nil ||
			mutation.NoContactEvidence != nil ||
			mutation.RejectionVerification != nil ||
			mutation.NoEffectVerification != nil ||
			!acceptedOrRecoveredV1(mutation.ProtocolAccepted, mutation.OutcomeEvidence) ||
			(mutation.State == MutationStateV1ProbeActive &&
				mutation.Mode != ModeV1Probe) {
			return contractValidationErrorV1(ErrorCodeV1DecodeError)
		}
	case MutationStateV1RollbackIntent,
		MutationStateV1RollbackDispatchIntent,
		MutationStateV1RollbackReplyObserved,
		MutationStateV1RollbackVerifyPending,
		MutationStateV1RolledBack:
		if !acceptedOrRecoveredV1(
			mutation.ProtocolAccepted,
			mutation.OutcomeEvidence,
		) ||
			mutation.ObservedAfter == nil ||
			validateApplyVerificationV1(
				mutation.ApplyVerification,
				mutation.ObservedAfter,
				requestedHash,
			) != nil ||
			validateRollbackV1(mutation.Rollback, mutation.State, beforeHash) != nil ||
			mutation.Error != nil ||
			mutation.ConflictEvidence != nil ||
			mutation.NoContactEvidence != nil ||
			mutation.RejectionVerification != nil ||
			mutation.NoEffectVerification != nil {
			return contractValidationErrorV1(ErrorCodeV1DecodeError)
		}
	case MutationStateV1OutcomeUnknown:
		if validateOutcomeUnknownV1(
			mutation,
			beforeHash,
			requestedHash,
		) != nil {
			return contractValidationErrorV1(ErrorCodeV1DecodeError)
		}
	case MutationStateV1Conflict:
		if mutation.ObservedAfter == nil ||
			mutation.Error == nil ||
			mutation.Error.Code != ErrorCodeV1Conflict ||
			validateConflictEvidenceV1(
				mutation.ConflictEvidence,
				mutation.ObservedAfter,
				beforeHash,
				requestedHash,
			) != nil ||
			!conflictDispositionV1(mutation.ProtocolAccepted, mutation.OutcomeEvidence) ||
			mutation.NoContactEvidence != nil ||
			mutation.RejectionVerification != nil ||
			mutation.NoEffectVerification != nil {
			return contractValidationErrorV1(ErrorCodeV1DecodeError)
		}
	case MutationStateV1FailedNoContact:
		if mutation.ProtocolAccepted != nil ||
			mutation.ObservedAfter != nil ||
			mutation.Error == nil ||
			validateNoContactEvidenceV1(mutation.NoContactEvidence) != nil ||
			mutation.Rollback != nil ||
			mutation.ApplyVerification != nil ||
			mutation.ConflictEvidence != nil ||
			mutation.RejectionVerification != nil ||
			mutation.NoEffectVerification != nil ||
			mutation.OutcomeEvidence != nil {
			return contractValidationErrorV1(ErrorCodeV1DecodeError)
		}
	case MutationStateV1Rejected:
		if !boolPointerV1(mutation.ProtocolAccepted, false) ||
			mutation.ObservedAfter == nil ||
			mutation.Error == nil ||
			mutation.Error.Code != ErrorCodeV1RemoteError ||
			validateRejectionVerificationV1(
				mutation.RejectionVerification,
				mutation.ObservedAfter,
				beforeHash,
			) != nil ||
			mutation.Rollback != nil ||
			mutation.ApplyVerification != nil ||
			mutation.ConflictEvidence != nil ||
			mutation.NoContactEvidence != nil ||
			mutation.NoEffectVerification != nil ||
			mutation.OutcomeEvidence != nil {
			return contractValidationErrorV1(ErrorCodeV1DecodeError)
		}
	case MutationStateV1NoEffect:
		if mutation.ProtocolAccepted != nil ||
			mutation.ObservedAfter == nil ||
			mutation.Error == nil ||
			mutation.Error.Code != ErrorCodeV1NoEffect ||
			mutation.Error.Retriable ||
			validateOutcomeEvidenceV1(
				mutation.OutcomeEvidence,
				MutationStateV1DispatchIntent,
			) != nil ||
			validateNoEffectVerificationV1(
				mutation.NoEffectVerification,
				mutation.ObservedAfter,
				beforeHash,
			) != nil ||
			mutation.Rollback != nil ||
			mutation.ApplyVerification != nil ||
			mutation.ConflictEvidence != nil ||
			mutation.NoContactEvidence != nil ||
			mutation.RejectionVerification != nil {
			return contractValidationErrorV1(ErrorCodeV1DecodeError)
		}
	default:
		return contractValidationErrorV1(ErrorCodeV1DecodeError)
	}
	return nil
}

func validateConstraintSetV1(constraints ConstraintSetV1) error {
	switch constraints.Status {
	case ConstraintStatusV1Unknown:
		if len(constraints.EnumValues) != 0 ||
			constraints.Minimum != nil ||
			constraints.Maximum != nil ||
			constraints.Step != nil ||
			constraints.Unit != "" ||
			constraints.MinCardinality != nil ||
			constraints.MaxCardinality != nil ||
			len(constraints.CrossFieldRules) != 0 {
			return errors.New("unknown constraints contain values")
		}
	case ConstraintStatusV1Known:
	default:
		return errors.New("constraint status is invalid")
	}
	if len(constraints.EnumValues) > 256 ||
		!optionalBoundedStringV1(constraints.Unit, 128) ||
		len(constraints.CrossFieldRules) > 64 {
		return errors.New("constraint bounds are invalid")
	}
	seen := make(map[HashV1]struct{}, len(constraints.EnumValues))
	for _, value := range constraints.EnumValues {
		if !typedScalarV1(value) {
			return errors.New("constraint enum is invalid")
		}
		hash, err := value.ComputeHash()
		if err != nil {
			return errors.New("constraint enum is invalid")
		}
		if _, duplicate := seen[hash]; duplicate {
			return errors.New("constraint enum contains duplicates")
		}
		seen[hash] = struct{}{}
	}
	for _, value := range []*TypedValueV1{
		constraints.Minimum, constraints.Maximum, constraints.Step,
	} {
		if value != nil && !typedScalarV1(*value) {
			return errors.New("constraint bound is invalid")
		}
	}
	if constraints.MinCardinality != nil &&
		*constraints.MinCardinality > maximumSafeIntegerV1 ||
		constraints.MaxCardinality != nil &&
			*constraints.MaxCardinality > maximumSafeIntegerV1 {
		return errors.New("constraint cardinality is invalid")
	}
	if constraints.MinCardinality != nil &&
		constraints.MaxCardinality != nil &&
		*constraints.MinCardinality > *constraints.MaxCardinality {
		return errors.New("constraint cardinality is invalid")
	}
	for _, rule := range constraints.CrossFieldRules {
		if !boundedStringV1(rule, 512) {
			return errors.New("constraint rule is invalid")
		}
	}
	return nil
}

func validateReadObservationV1(observation ReadObservationV1) error {
	if observation.Target.Operation != OperationV1Read ||
		validateFeatureTargetV1(observation.Target) != nil ||
		!runtimeBindingV1(observation.Runtime) ||
		observation.Value.Validate() != nil ||
		!timestampV1(observation.RequestedAt) ||
		!timestampV1(observation.ReceivedAt) ||
		!timestampV1(observation.DataTimestamp) ||
		observation.ReceivedAt.Before(observation.RequestedAt) ||
		observation.DataTimestamp.Before(observation.ReceivedAt) ||
		observation.Source != ObservationSourceV1Live ||
		!opaqueReferenceV1(observation.ReadToken.ReadToken) ||
		!timestampV1(observation.ReadToken.ExpiresAt) ||
		!observation.ReadToken.ExpiresAt.After(observation.ReceivedAt) ||
		!hashV1(observation.ReadToken.BindingHash) ||
		validateProtocolMessageV1(observation.RawRequest) != nil ||
		validateProtocolMessageV1(observation.RawResponse) != nil ||
		observation.RawRequest.Classifier != "READ" ||
		observation.RawRequest.ErrorNumber != nil ||
		observation.RawResponse.Classifier != "REPLY" ||
		observation.RawResponse.ErrorNumber != nil ||
		observation.RawRequest.CorrelationKey != observation.RawResponse.CorrelationKey ||
		observation.RawRequest.Function != observation.Target.Function ||
		observation.RawResponse.Function != observation.Target.Function ||
		observation.RawResponse.Data == nil ||
		!typedValuesEqualV1(*observation.RawResponse.Data, observation.Value) {
		return errors.New("read observation is invalid")
	}
	for _, unknown := range observation.Unknown {
		if validateOpaqueObservationV1(unknown) != nil {
			return errors.New("read unknown observation is invalid")
		}
	}
	computed, err := observation.ComputeDataHash()
	if err != nil || !hashV1(observation.DataHash) || computed != observation.DataHash {
		return errors.New("read observation commitment is invalid")
	}
	return nil
}

func validateProtocolMessageV1(message ProtocolMessageV1) error {
	if !validProtocolClassifierV1(message.Classifier) ||
		message.CorrelationKey == 0 ||
		message.CorrelationKey > maximumSafeIntegerV1 ||
		!boundedStringV1(message.Function, 256) ||
		(message.Data != nil && message.Data.Validate() != nil) ||
		message.ErrorNumber != nil &&
			*message.ErrorNumber > maximumSafeIntegerV1 ||
		message.Data != nil && message.ErrorNumber != nil ||
		len(message.Unknown) > 256 {
		return errors.New("protocol message is invalid")
	}
	for _, unknown := range message.Unknown {
		if validateOpaqueObservationV1(unknown) != nil {
			return errors.New("protocol unknown observation is invalid")
		}
	}
	return nil
}

func validateOpaqueObservationV1(observation OpaqueObservationV1) error {
	if !boundedStringV1(observation.Path, 1024) ||
		!boundedStringV1(observation.Source, 256) ||
		observation.Value.Validate() != nil {
		return errors.New("opaque observation is invalid")
	}
	return nil
}

func validateErrorV1(value ErrorV1) error {
	if !validErrorCodeV1(value.Code) ||
		!boundedStringV1(value.Message, 512) ||
		!validSourceLayerV1(value.SourceLayer) ||
		(value.Code == ErrorCodeV1TypedEmpty &&
			(value.Retriable || value.SourceLayer != SourceLayerV1Remote)) {
		return errors.New("structured error is invalid")
	}
	if value.Details != nil {
		if value.Details.TargetIndex != nil && *value.Details.TargetIndex > 15 ||
			!optionalBoundedStringV1(value.Details.Classification, 128) ||
			len(value.Details.Unknown) > 256 {
			return errors.New("structured error details are invalid")
		}
		for _, unknown := range value.Details.Unknown {
			if validateOpaqueObservationV1(unknown) != nil {
				return errors.New("structured error unknown detail is invalid")
			}
		}
	}
	return nil
}

func validateAuditV1(
	audit []AuditTransitionV1,
	createdAt time.Time,
	updatedAt time.Time,
) error {
	var previous *HashV1
	var previousState MutationStateV1
	var previousTime time.Time
	for index, transition := range audit {
		if transition.Sequence != uint64(index+1) ||
			!validMutationStateV1(transition.State) ||
			!timestampV1(transition.TransitionedAt) ||
			!optionalBoundedStringV1(transition.Classification, 128) ||
			transition.TransitionedAt.Before(createdAt) ||
			transition.TransitionedAt.After(updatedAt) ||
			index > 0 && transition.TransitionedAt.Before(previousTime) ||
			index == 0 && !validInitialMutationStateV1(transition.State) ||
			index > 0 && !validMutationTransitionV1(previousState, transition.State) ||
			!hashPointersEqualV1(transition.PreviousHash, previous) {
			return errors.New("mutation audit chain is invalid")
		}
		computed, err := CanonicalSHA256V1(struct {
			Sequence       uint64          `json:"sequence"`
			State          MutationStateV1 `json:"state"`
			TransitionedAt time.Time       `json:"transitioned_at"`
			Classification string          `json:"classification,omitempty"`
			PreviousHash   *HashV1         `json:"previous_hash"`
		}{
			Sequence: transition.Sequence, State: transition.State,
			TransitionedAt: transition.TransitionedAt,
			Classification: transition.Classification,
			PreviousHash:   transition.PreviousHash,
		})
		if err != nil || !hashV1(transition.TransitionHash) ||
			computed != transition.TransitionHash {
			return errors.New("mutation audit commitment is invalid")
		}
		value := transition.TransitionHash
		previous = &value
		previousState = transition.State
		previousTime = transition.TransitionedAt
	}
	return nil
}

func validateApplyVerificationV1(
	verification *ApplyVerificationV1,
	observed *TypedValueV1,
	requestedHash HashV1,
) error {
	if verification == nil || observed == nil ||
		!typedValueNonNullV1(*observed) ||
		verification.Relation != "observed_after_equals_requested" ||
		!verification.Verified ||
		!timestampV1(verification.VerifiedAt) ||
		verification.EqualValueHash != requestedHash {
		return errors.New("apply verification is invalid")
	}
	observedHash, err := observed.ComputeHash()
	if err != nil || observedHash != requestedHash {
		return errors.New("apply observed value is invalid")
	}
	return nil
}

func validateRollbackV1(
	rollback *RollbackV1,
	state MutationStateV1,
	beforeHash HashV1,
) error {
	if rollback == nil ||
		rollback.State != state ||
		validateRollbackBeforeV1(rollback, beforeHash) != nil {
		return errors.New("rollback evidence is invalid")
	}
	switch state {
	case MutationStateV1RollbackIntent, MutationStateV1RollbackDispatchIntent:
		if rollback.ProtocolAccepted != nil ||
			rollback.ObservedAfter != nil ||
			rollback.Error != nil ||
			rollback.Verification != nil {
			return errors.New("rollback pre-dispatch evidence is invalid")
		}
	case MutationStateV1RollbackReplyObserved, MutationStateV1RollbackVerifyPending:
		if rollback.ProtocolAccepted == nil ||
			rollback.ObservedAfter != nil ||
			rollback.Error != nil ||
			rollback.Verification != nil {
			return errors.New("rollback pending evidence is invalid")
		}
	case MutationStateV1RolledBack:
		if rollback.ObservedAfter == nil ||
			!typedValueNonNullV1(*rollback.ObservedAfter) ||
			rollback.Error != nil ||
			rollback.Verification == nil ||
			rollback.Verification.Relation !=
				"rollback_observed_after_equals_before" ||
			!rollback.Verification.Verified ||
			!timestampV1(rollback.Verification.VerifiedAt) ||
			rollback.Verification.EqualValueHash != beforeHash {
			return errors.New("rollback verification is invalid")
		}
		observedHash, err := rollback.ObservedAfter.ComputeHash()
		if err != nil || observedHash != beforeHash {
			return errors.New("rollback observed value is invalid")
		}
	default:
		return errors.New("rollback state is invalid")
	}
	return nil
}

func validateConflictEvidenceV1(
	evidence *ConflictEvidenceV1,
	observed *TypedValueV1,
	beforeHash HashV1,
	requestedHash HashV1,
) error {
	if evidence == nil ||
		observed == nil ||
		!typedValueNonNullV1(*observed) ||
		evidence.Relation != "observed_after_differs_from_before_and_requested" ||
		!evidence.Verified ||
		!timestampV1(evidence.VerifiedAt) ||
		evidence.BeforeHash != beforeHash ||
		evidence.RequestedHash != requestedHash {
		return errors.New("conflict evidence is invalid")
	}
	observedHash, err := observed.ComputeHash()
	if err != nil ||
		observedHash != evidence.ObservedAfterHash ||
		observedHash == beforeHash ||
		observedHash == requestedHash {
		return errors.New("conflict observation is invalid")
	}
	return nil
}

func validateNoContactEvidenceV1(evidence *NoContactEvidenceV1) error {
	if evidence == nil ||
		evidence.RemoteFramesSent != 0 ||
		!timestampV1(evidence.VerifiedAt) {
		return errors.New("no-contact evidence is invalid")
	}
	switch evidence.LastCompletedPhase {
	case "shape_validation", "scope_validation", "authentication",
		"provider_lookup", "routing", "runtime_selection", "lease",
		"constraints", "cas", "waiter_registration", "send_setup",
		"read_token_verified", "dispatch_intent_persisted":
		return nil
	default:
		return errors.New("no-contact phase is invalid")
	}
}

func validateOutcomeUnknownV1(
	mutation MutationV1,
	beforeHash HashV1,
	requestedHash HashV1,
) error {
	if mutation.Error == nil ||
		mutation.Error.Retriable ||
		mutation.ConflictEvidence != nil ||
		mutation.NoContactEvidence != nil ||
		mutation.RejectionVerification != nil ||
		mutation.NoEffectVerification != nil {
		return errors.New("outcome-unknown evidence is invalid")
	}
	if mutation.Rollback != nil {
		return validateRollbackFailureV1(mutation, beforeHash, requestedHash)
	}
	if mutation.Error.Code != ErrorCodeV1OutcomeUnknown ||
		mutation.ApplyVerification != nil ||
		validateOriginalOutcomeEvidenceV1(
			mutation.OutcomeEvidence,
			mutation.ProtocolAccepted,
			mutation.ObservedAfter,
		) != nil {
		return errors.New("original outcome-unknown evidence is invalid")
	}
	return nil
}

func validateOriginalOutcomeEvidenceV1(
	evidence *OutcomeEvidenceV1,
	accepted *bool,
	observed *TypedValueV1,
) error {
	if evidence == nil ||
		!evidence.PossibleSideEffect ||
		!evidence.BlindRetryForbidden ||
		!timestampV1(evidence.RecordedAt) {
		return errors.New("original outcome evidence is invalid")
	}
	switch evidence.LastDurableState {
	case MutationStateV1DispatchIntent:
		if accepted != nil || observed != nil {
			return errors.New("uncorrelated outcome evidence is invalid")
		}
	case MutationStateV1ReplyObserved:
		if accepted == nil ||
			observed != nil && !typedValueNonNullV1(*observed) {
			return errors.New("correlated outcome evidence is invalid")
		}
	default:
		return errors.New("original outcome durable state is invalid")
	}
	return nil
}

func validateRollbackFailureV1(
	mutation MutationV1,
	beforeHash HashV1,
	requestedHash HashV1,
) error {
	evidence := mutation.OutcomeEvidence
	rollback := mutation.Rollback
	if mutation.Error.Code != ErrorCodeV1RollbackFailed ||
		mutation.ProtocolAccepted != nil && !*mutation.ProtocolAccepted ||
		mutation.ObservedAfter == nil ||
		validateApplyVerificationV1(
			mutation.ApplyVerification,
			mutation.ObservedAfter,
			requestedHash,
		) != nil ||
		evidence == nil ||
		!timestampV1(evidence.RecordedAt) ||
		validateRollbackBeforeV1(rollback, beforeHash) != nil ||
		rollback.State != evidence.LastDurableState ||
		rollback.ObservedAfter != nil ||
		rollback.Verification != nil ||
		rollback.Error == nil ||
		rollback.Error.Code != ErrorCodeV1RollbackFailed ||
		rollback.Error.Retriable ||
		validateErrorV1(*rollback.Error) != nil {
		return errors.New("rollback failure evidence is invalid")
	}
	switch evidence.LastDurableState {
	case MutationStateV1RollbackIntent:
		if evidence.PossibleSideEffect ||
			evidence.BlindRetryForbidden ||
			rollback.ProtocolAccepted != nil {
			return errors.New("pre-send rollback failure evidence is invalid")
		}
	case MutationStateV1RollbackDispatchIntent:
		if !evidence.PossibleSideEffect ||
			!evidence.BlindRetryForbidden ||
			rollback.ProtocolAccepted != nil {
			return errors.New("uncorrelated rollback failure evidence is invalid")
		}
	case MutationStateV1RollbackReplyObserved,
		MutationStateV1RollbackVerifyPending:
		if !evidence.PossibleSideEffect ||
			!evidence.BlindRetryForbidden ||
			rollback.ProtocolAccepted == nil {
			return errors.New("correlated rollback failure evidence is invalid")
		}
	default:
		return errors.New("rollback failure durable state is invalid")
	}
	return nil
}

func validateRejectionVerificationV1(
	verification *RejectionVerificationV1,
	observed *TypedValueV1,
	beforeHash HashV1,
) error {
	if verification == nil ||
		observed == nil ||
		!typedValueNonNullV1(*observed) ||
		verification.Relation != "observed_after_equals_before" ||
		!verification.Verified ||
		!verification.CorrelatedRejection ||
		!timestampV1(verification.VerifiedAt) ||
		verification.EqualValueHash != beforeHash {
		return errors.New("rejection verification is invalid")
	}
	observedHash, err := observed.ComputeHash()
	if err != nil || observedHash != beforeHash {
		return errors.New("rejection observation is invalid")
	}
	return nil
}

func validateNoEffectVerificationV1(
	verification *NoEffectVerificationV1,
	observed *TypedValueV1,
	beforeHash HashV1,
) error {
	if verification == nil ||
		observed == nil ||
		!typedValueNonNullV1(*observed) ||
		verification.Relation != "observed_after_equals_before" ||
		!verification.Verified ||
		!timestampV1(verification.VerifiedAt) ||
		verification.EqualValueHash != beforeHash {
		return errors.New("no-effect verification is invalid")
	}
	observedHash, err := observed.ComputeHash()
	if err != nil || observedHash != beforeHash {
		return errors.New("no-effect observation is invalid")
	}
	return nil
}

func validateOutcomeEvidenceV1(
	evidence *OutcomeEvidenceV1,
	requiredState MutationStateV1,
) error {
	if evidence == nil ||
		!evidence.PossibleSideEffect ||
		!evidence.BlindRetryForbidden ||
		!timestampV1(evidence.RecordedAt) {
		return errors.New("outcome evidence is invalid")
	}
	if requiredState != "" && evidence.LastDurableState != requiredState {
		return errors.New("outcome durable state is invalid")
	}
	if evidence.LastDurableState != MutationStateV1DispatchIntent &&
		evidence.LastDurableState != MutationStateV1RollbackDispatchIntent {
		return errors.New("outcome durable state is invalid")
	}
	return nil
}

func acceptedOrRecoveredV1(accepted *bool, outcome *OutcomeEvidenceV1) bool {
	if boolPointerV1(accepted, true) {
		return outcome == nil
	}
	return accepted == nil && validateOutcomeEvidenceV1(outcome, "") == nil
}

func conflictDispositionV1(accepted *bool, outcome *OutcomeEvidenceV1) bool {
	if accepted == nil {
		return validateOutcomeEvidenceV1(outcome, "") == nil
	}
	return outcome == nil
}
