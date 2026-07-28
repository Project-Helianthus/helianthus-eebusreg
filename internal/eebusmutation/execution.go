package eebusmutation

import (
	"context"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

func (coordinator *rawMutationCoordinator) completeOriginalWrite(
	ctx context.Context,
	entry *rawMutationEntry,
	result rawMutationWriteResult,
	writeTerminal *eebusraw.ErrorV1,
) (eebusraw.MutationV1, *eebusraw.ErrorV1) {
	if result.Correlated {
		accepted := result.Accepted
		entry.mutation.ProtocolAccepted = &accepted
		if terminal := coordinator.transition(entry, eebusraw.MutationStateV1ReplyObserved); terminal != nil {
			return cloneMutation(entry.mutation), terminal
		}
		if terminal := coordinator.transition(entry, eebusraw.MutationStateV1VerifyPending); terminal != nil {
			return cloneMutation(entry.mutation), terminal
		}
		readback, terminal := coordinator.readCurrent(ctx, entry.mutation.Target)
		return coordinator.resolveCorrelatedReadback(entry, readback, terminal, writeTerminal)
	}
	if result.FrameSent {
		entry.mutation.ProtocolAccepted = nil
		entry.mutation.OutcomeEvidence = &eebusraw.OutcomeEvidenceV1{
			PossibleSideEffect:  true,
			BlindRetryForbidden: true,
			LastDurableState:    eebusraw.MutationStateV1DispatchIntent,
			RecordedAt:          normalizedNow(coordinator.config.Now),
		}
		entry.mutation.Error = mutationError(eebusraw.ErrorCodeV1OutcomeUnknown, false)
		if terminal := coordinator.transition(entry, eebusraw.MutationStateV1OutcomeUnknown); terminal != nil {
			return cloneMutation(entry.mutation), terminal
		}
		readback, terminal := coordinator.readCurrent(ctx, entry.mutation.Target)
		return coordinator.resolvePossibleSendReadback(entry, readback, terminal)
	}
	failure := sanitizeMutationError(writeTerminal)
	if failure == nil {
		failure = mutationError(eebusraw.ErrorCodeV1Disconnected, true)
	}
	entry.mutation.Error = failure
	entry.mutation.NoContactEvidence = &eebusraw.NoContactEvidenceV1{
		RemoteFramesSent:   0,
		LastCompletedPhase: "dispatch_intent_persisted",
		VerifiedAt:         normalizedNow(coordinator.config.Now),
	}
	if terminal := coordinator.transition(entry, eebusraw.MutationStateV1FailedNoContact); terminal != nil {
		return cloneMutation(entry.mutation), terminal
	}
	return cloneMutation(entry.mutation), cloneTerminal(entry.mutation.Error)
}

func (coordinator *rawMutationCoordinator) recoverEntry(
	ctx context.Context,
	entry *rawMutationEntry,
) (eebusraw.MutationV1, *eebusraw.ErrorV1) {
	switch entry.mutation.State {
	case eebusraw.MutationStateV1DispatchIntent:
		entry.mutation.ProtocolAccepted = nil
		entry.mutation.OutcomeEvidence = &eebusraw.OutcomeEvidenceV1{
			PossibleSideEffect:  true,
			BlindRetryForbidden: true,
			LastDurableState:    eebusraw.MutationStateV1DispatchIntent,
			RecordedAt:          normalizedNow(coordinator.config.Now),
		}
		entry.mutation.Error = mutationError(eebusraw.ErrorCodeV1OutcomeUnknown, false)
		if terminal := coordinator.transition(entry, eebusraw.MutationStateV1OutcomeUnknown); terminal != nil {
			return cloneMutation(entry.mutation), terminal
		}
		readback, terminal := coordinator.readCurrent(ctx, entry.mutation.Target)
		return coordinator.resolvePossibleSendReadback(entry, readback, terminal)
	case eebusraw.MutationStateV1OutcomeUnknown:
		readback, terminal := coordinator.readCurrent(ctx, entry.mutation.Target)
		if entry.mutation.OutcomeEvidence != nil &&
			entry.mutation.OutcomeEvidence.LastDurableState ==
				eebusraw.MutationStateV1RollbackDispatchIntent {
			return coordinator.resolveUncertainRollbackReadback(entry, readback, terminal)
		}
		return coordinator.resolvePossibleSendReadback(entry, readback, terminal)
	case eebusraw.MutationStateV1ReplyObserved,
		eebusraw.MutationStateV1VerifyPending:
		readback, terminal := coordinator.readCurrent(ctx, entry.mutation.Target)
		return coordinator.resolveCorrelatedReadback(entry, readback, terminal, entry.mutation.Error)
	case eebusraw.MutationStateV1RollbackIntent,
		eebusraw.MutationStateV1RollbackDispatchIntent,
		eebusraw.MutationStateV1RollbackReplyObserved,
		eebusraw.MutationStateV1RollbackVerifyPending:
		return coordinator.recoverRollback(ctx, entry)
	default:
		return cloneMutation(entry.mutation), terminalForMutation(entry.mutation)
	}
}

func (coordinator *rawMutationCoordinator) readCurrent(
	ctx context.Context,
	target eebusraw.FeatureTargetV1,
) (rawMutationReadResult, *eebusraw.ErrorV1) {
	readTarget := target.Clone()
	readTarget.Operation = eebusraw.OperationV1Read
	current, terminal := coordinator.deps.BindingAuthority.CurrentRuntimeBinding(readTarget)
	if terminal != nil {
		return rawMutationReadResult{}, sanitizeMutationError(terminal)
	}
	return coordinator.deps.Executor.FullReadIfCurrent(ctx, readTarget, current)
}

func (coordinator *rawMutationCoordinator) resolveCorrelatedReadback(
	entry *rawMutationEntry,
	readback rawMutationReadResult,
	readTerminal *eebusraw.ErrorV1,
	writeTerminal *eebusraw.ErrorV1,
) (eebusraw.MutationV1, *eebusraw.ErrorV1) {
	if !coordinator.readbackIsTrustworthy(entry, readback, readTerminal, false) {
		entry.mutation.OutcomeEvidence = &eebusraw.OutcomeEvidenceV1{
			PossibleSideEffect:  true,
			BlindRetryForbidden: true,
			LastDurableState:    eebusraw.MutationStateV1ReplyObserved,
			RecordedAt:          normalizedNow(coordinator.config.Now),
		}
		entry.mutation.Error = mutationError(eebusraw.ErrorCodeV1OutcomeUnknown, false)
		if terminal := coordinator.transition(entry, eebusraw.MutationStateV1OutcomeUnknown); terminal != nil {
			return cloneMutation(entry.mutation), terminal
		}
		return cloneMutation(entry.mutation), cloneTerminal(entry.mutation.Error)
	}
	entry.mutation.ObservedAfter = cloneValue(&readback.Value)
	if entry.mutation.ProtocolAccepted != nil && !*entry.mutation.ProtocolAccepted {
		if typedValuesEqual(readback.Value, entry.mutation.Before) {
			hash, _ := entry.mutation.Before.ComputeHash()
			entry.mutation.RejectionVerification = &eebusraw.RejectionVerificationV1{
				Relation:            "observed_after_equals_before",
				Verified:            true,
				CorrelatedRejection: true,
				EqualValueHash:      hash,
				VerifiedAt:          normalizedNow(coordinator.config.Now),
			}
			entry.mutation.Error = sanitizeMutationError(writeTerminal)
			if entry.mutation.Error == nil {
				entry.mutation.Error = mutationError(eebusraw.ErrorCodeV1RemoteError, false)
			}
			entry.mutation.Error.Retriable = false
			if terminal := coordinator.transition(entry, eebusraw.MutationStateV1Rejected); terminal != nil {
				return cloneMutation(entry.mutation), terminal
			}
			return cloneMutation(entry.mutation), cloneTerminal(entry.mutation.Error)
		}
		if typedValuesEqual(readback.Value, entry.mutation.Requested) {
			return coordinator.persistCorrelatedContradiction(entry, readback.Value)
		}
		return coordinator.persistConflict(entry, readback.Value)
	}
	if typedValuesEqual(readback.Value, entry.mutation.Requested) {
		return coordinator.persistApplied(entry, readback.Value)
	}
	if typedValuesEqual(readback.Value, entry.mutation.Before) {
		return coordinator.persistCorrelatedContradiction(entry, readback.Value)
	}
	return coordinator.persistConflict(entry, readback.Value)
}

func (coordinator *rawMutationCoordinator) persistCorrelatedContradiction(
	entry *rawMutationEntry,
	observed eebusraw.TypedValueV1,
) (eebusraw.MutationV1, *eebusraw.ErrorV1) {
	entry.mutation.ObservedAfter = cloneValue(&observed)
	entry.mutation.ConflictEvidence = nil
	entry.mutation.ApplyVerification = nil
	entry.mutation.NoEffectVerification = nil
	entry.mutation.RejectionVerification = nil
	entry.mutation.OutcomeEvidence = &eebusraw.OutcomeEvidenceV1{
		PossibleSideEffect:  true,
		BlindRetryForbidden: true,
		LastDurableState:    eebusraw.MutationStateV1ReplyObserved,
		RecordedAt:          normalizedNow(coordinator.config.Now),
	}
	entry.mutation.Error = mutationError(eebusraw.ErrorCodeV1OutcomeUnknown, false)
	if terminal := coordinator.transition(entry, eebusraw.MutationStateV1OutcomeUnknown); terminal != nil {
		return cloneMutation(entry.mutation), terminal
	}
	return cloneMutation(entry.mutation), cloneTerminal(entry.mutation.Error)
}

func (coordinator *rawMutationCoordinator) resolvePossibleSendReadback(
	entry *rawMutationEntry,
	readback rawMutationReadResult,
	readTerminal *eebusraw.ErrorV1,
) (eebusraw.MutationV1, *eebusraw.ErrorV1) {
	if !coordinator.readbackIsTrustworthy(entry, readback, readTerminal, true) {
		entry.mutation.Error = mutationError(eebusraw.ErrorCodeV1OutcomeUnknown, false)
		return cloneMutation(entry.mutation), cloneTerminal(entry.mutation.Error)
	}
	entry.mutation.Runtime = readback.Runtime
	entry.mutation.ProtocolAccepted = nil
	entry.mutation.ObservedAfter = cloneValue(&readback.Value)
	if typedValuesEqual(readback.Value, entry.mutation.Before) {
		hash, _ := entry.mutation.Before.ComputeHash()
		entry.mutation.NoEffectVerification = &eebusraw.NoEffectVerificationV1{
			Relation:       "observed_after_equals_before",
			Verified:       true,
			EqualValueHash: hash,
			VerifiedAt:     normalizedNow(coordinator.config.Now),
		}
		entry.mutation.ApplyVerification = nil
		entry.mutation.Error = mutationError(eebusraw.ErrorCodeV1NoEffect, false)
		if terminal := coordinator.transition(entry, eebusraw.MutationStateV1NoEffect); terminal != nil {
			return cloneMutation(entry.mutation), terminal
		}
		return cloneMutation(entry.mutation), cloneTerminal(entry.mutation.Error)
	}
	if typedValuesEqual(readback.Value, entry.mutation.Requested) {
		return coordinator.persistApplied(entry, readback.Value)
	}
	return coordinator.persistConflict(entry, readback.Value)
}

func (coordinator *rawMutationCoordinator) persistApplied(
	entry *rawMutationEntry,
	observed eebusraw.TypedValueV1,
) (eebusraw.MutationV1, *eebusraw.ErrorV1) {
	hash, err := observed.ComputeHash()
	if err != nil {
		return cloneMutation(entry.mutation), internalMutationError()
	}
	entry.mutation.ObservedAfter = cloneValue(&observed)
	entry.mutation.ApplyVerification = &eebusraw.ApplyVerificationV1{
		Relation:       "observed_after_equals_requested",
		Verified:       true,
		EqualValueHash: hash,
		VerifiedAt:     normalizedNow(coordinator.config.Now),
	}
	entry.mutation.ConflictEvidence = nil
	entry.mutation.NoEffectVerification = nil
	entry.mutation.Error = nil
	state := eebusraw.MutationStateV1Applied
	if entry.mutation.Mode == eebusraw.ModeV1Probe {
		state = eebusraw.MutationStateV1ProbeActive
	}
	if terminal := coordinator.transition(entry, state); terminal != nil {
		return cloneMutation(entry.mutation), terminal
	}
	if state == eebusraw.MutationStateV1ProbeActive {
		coordinator.scheduleProbe(entry)
	}
	return cloneMutation(entry.mutation), nil
}

func (coordinator *rawMutationCoordinator) persistConflict(
	entry *rawMutationEntry,
	observed eebusraw.TypedValueV1,
) (eebusraw.MutationV1, *eebusraw.ErrorV1) {
	if typedValuesEqual(observed, entry.mutation.Before) ||
		typedValuesEqual(observed, entry.mutation.Requested) {
		return coordinator.persistCorrelatedContradiction(entry, observed)
	}
	beforeHash, beforeErr := entry.mutation.Before.ComputeHash()
	requestedHash, requestedErr := entry.mutation.Requested.ComputeHash()
	observedHash, observedErr := observed.ComputeHash()
	if beforeErr != nil || requestedErr != nil || observedErr != nil {
		return cloneMutation(entry.mutation), internalMutationError()
	}
	entry.mutation.ObservedAfter = cloneValue(&observed)
	entry.mutation.ConflictEvidence = &eebusraw.ConflictEvidenceV1{
		Relation:          "observed_after_differs_from_before_and_requested",
		Verified:          true,
		BeforeHash:        beforeHash,
		RequestedHash:     requestedHash,
		ObservedAfterHash: observedHash,
		VerifiedAt:        normalizedNow(coordinator.config.Now),
	}
	entry.mutation.ApplyVerification = nil
	entry.mutation.NoEffectVerification = nil
	entry.mutation.Error = mutationError(eebusraw.ErrorCodeV1Conflict, false)
	if terminal := coordinator.transition(entry, eebusraw.MutationStateV1Conflict); terminal != nil {
		return cloneMutation(entry.mutation), terminal
	}
	return cloneMutation(entry.mutation), cloneTerminal(entry.mutation.Error)
}

func (coordinator *rawMutationCoordinator) readbackIsTrustworthy(
	entry *rawMutationEntry,
	readback rawMutationReadResult,
	terminal *eebusraw.ErrorV1,
	allowGenerationRebind bool,
) bool {
	if terminal != nil ||
		!readback.Full ||
		!readback.Trustworthy ||
		readback.Value.Validate() != nil ||
		readback.Runtime.RuntimeEpoch != coordinator.config.RuntimeEpoch() ||
		readback.Runtime.ConnectionGeneration == 0 {
		return false
	}
	if !allowGenerationRebind &&
		readback.Runtime.ConnectionGeneration != entry.mutation.Runtime.ConnectionGeneration {
		return false
	}
	return true
}
