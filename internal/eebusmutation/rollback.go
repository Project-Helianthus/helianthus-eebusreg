package eebusmutation

import (
	"context"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

func (coordinator *rawMutationCoordinator) MutationsRollback(
	ctx context.Context,
	auth eebusraw.WriteAuthorizationV1,
	request eebusraw.MutationRollbackRequestV1,
) (eebusraw.MutationV1, *eebusraw.ErrorV1) {
	if ctx == nil {
		ctx = coordinator.ctx
	}
	ctx, cancel := coordinator.operationContext(ctx)
	defer cancel()
	if terminal := eebusraw.ValidateWriteAuthorizationV1(auth, eebusraw.ToolV1MutationsRollback); terminal != nil {
		return eebusraw.MutationV1{}, terminal
	}
	if !boundedPurposeValue(request.MutationRef) ||
		!boundedPurposeValue(request.IdempotencyKey) {
		return eebusraw.MutationV1{}, mutationError(eebusraw.ErrorCodeV1InvalidArgument, false)
	}
	epoch := coordinator.config.RuntimeEpoch()
	identityHash, err := rawMutationIdentityHash(
		coordinator.config.ReferenceKey,
		epoch,
		auth.PrincipalClass,
		eebusraw.ToolV1MutationsRollback,
		request.IdempotencyKey,
	)
	if err != nil {
		return eebusraw.MutationV1{}, internalMutationError()
	}
	requestHash, err := eebusraw.CanonicalSHA256V1(request)
	if err != nil {
		return eebusraw.MutationV1{}, internalMutationError()
	}
	principalHash, err := rawMutationPrincipalHash(auth.PrincipalClass)
	if err != nil {
		return eebusraw.MutationV1{}, internalMutationError()
	}

	entry, replay, terminal := coordinator.reserveRollback(
		request.MutationRef,
		principalHash,
		identityHash,
		requestHash,
	)
	if terminal != nil {
		return eebusraw.MutationV1{}, terminal
	}
	if replay {
		return entry.snapshot(), nil
	}
	defer coordinator.releaseWriter()
	coordinator.cancelProbe(entry.snapshot().MutationRef)
	if entry.durableTool == eebusraw.ToolV1MutationsRollback &&
		rawMutationStateNeedsRecovery(entry.snapshot().State) {
		return coordinator.recoverEntry(ctx, entry)
	}
	return coordinator.startRollback(ctx, entry)
}

func (coordinator *rawMutationCoordinator) reserveRollback(
	mutationRef string,
	principalHash eebusraw.HashV1,
	identityHash eebusraw.HashV1,
	requestHash eebusraw.HashV1,
) (*rawMutationEntry, bool, *eebusraw.ErrorV1) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.closed {
		return nil, false, mutationError(eebusraw.ErrorCodeV1Disconnected, true)
	}
	if coordinator.quarantined {
		return nil, false, mutationError(eebusraw.ErrorCodeV1Conflict, false)
	}
	if known, exists := coordinator.idempotency[identityHash]; exists {
		if known.requestHash != requestHash || known.mutationRef != mutationRef {
			return nil, false, mutationError(eebusraw.ErrorCodeV1IdempotencyConflict, false)
		}
		entry := coordinator.entries[known.mutationRef]
		if entry == nil {
			return nil, false, internalMutationError()
		}
		if coordinator.writer {
			return entry, true, nil
		}
		if rawMutationStateNeedsRecovery(entry.snapshot().State) {
			coordinator.writer = true
			coordinator.writerDone = make(chan struct{})
			return entry, false, nil
		}
		return entry, true, nil
	}
	entry := coordinator.entries[mutationRef]
	if entry == nil {
		return nil, false, mutationError(eebusraw.ErrorCodeV1NotFound, false)
	}
	mutation := entry.snapshot()
	if entry.principalHash != principalHash ||
		mutation.Runtime.RuntimeEpoch != coordinator.config.RuntimeEpoch() {
		return nil, false, mutationError(eebusraw.ErrorCodeV1PermissionDenied, false)
	}
	if coordinator.writer {
		return nil, false, mutationError(eebusraw.ErrorCodeV1WriterBusy, true)
	}
	coordinator.writer = true
	coordinator.writerDone = make(chan struct{})
	entry.durableIdentityHash = identityHash
	entry.durableRequestHash = requestHash
	entry.durableTool = eebusraw.ToolV1MutationsRollback
	return entry, false, nil
}

func (coordinator *rawMutationCoordinator) startRollback(
	ctx context.Context,
	entry *rawMutationEntry,
) (eebusraw.MutationV1, *eebusraw.ErrorV1) {
	entry.mutation.Rollback = &eebusraw.RollbackV1{
		State:  eebusraw.MutationStateV1RollbackIntent,
		Before: entry.mutation.Before.Clone(),
	}
	entry.mutation.Error = nil
	if terminal := coordinator.transition(entry, eebusraw.MutationStateV1RollbackIntent); terminal != nil {
		return cloneMutation(entry.mutation), terminal
	}
	return coordinator.continueRollbackFromIntent(ctx, entry)
}

func (coordinator *rawMutationCoordinator) continueRollbackFromIntent(
	ctx context.Context,
	entry *rawMutationEntry,
) (eebusraw.MutationV1, *eebusraw.ErrorV1) {
	readback, readTerminal := coordinator.readCurrent(ctx, entry.mutation.Target)
	if !coordinator.readbackIsTrustworthy(entry, readback, readTerminal, true) {
		return coordinator.persistRollbackUnknown(entry)
	}
	entry.mutation.Runtime = readback.Runtime
	if typedValuesEqual(readback.Value, entry.mutation.Before) {
		return coordinator.persistRolledBack(entry, readback.Value, nil)
	}
	if !typedValuesEqual(readback.Value, entry.mutation.Requested) {
		return coordinator.persistConflict(entry, readback.Value)
	}
	entry.mutation.Rollback.State = eebusraw.MutationStateV1RollbackDispatchIntent
	if terminal := coordinator.transition(entry, eebusraw.MutationStateV1RollbackDispatchIntent); terminal != nil {
		return cloneMutation(entry.mutation), terminal
	}
	result, writeTerminal := coordinator.deps.Executor.FullWriteIfCurrent(
		ctx,
		entry.mutation.Target.Clone(),
		entry.mutation.Before.Clone(),
		entry.mutation.Runtime,
	)
	return coordinator.completeRollbackWrite(ctx, entry, result, writeTerminal)
}

func (coordinator *rawMutationCoordinator) completeRollbackWrite(
	ctx context.Context,
	entry *rawMutationEntry,
	result rawMutationWriteResult,
	writeTerminal *eebusraw.ErrorV1,
) (eebusraw.MutationV1, *eebusraw.ErrorV1) {
	if result.Correlated {
		accepted := result.Accepted
		entry.mutation.Rollback.ProtocolAccepted = &accepted
		entry.mutation.Rollback.State = eebusraw.MutationStateV1RollbackReplyObserved
		if terminal := coordinator.transition(entry, eebusraw.MutationStateV1RollbackReplyObserved); terminal != nil {
			return cloneMutation(entry.mutation), terminal
		}
		entry.mutation.Rollback.State = eebusraw.MutationStateV1RollbackVerifyPending
		if terminal := coordinator.transition(entry, eebusraw.MutationStateV1RollbackVerifyPending); terminal != nil {
			return cloneMutation(entry.mutation), terminal
		}
		readback, terminal := coordinator.readCurrent(ctx, entry.mutation.Target)
		if !coordinator.readbackIsTrustworthy(entry, readback, terminal, false) {
			return coordinator.persistRollbackUnknown(entry)
		}
		if typedValuesEqual(readback.Value, entry.mutation.Before) && result.Accepted {
			return coordinator.persistRolledBack(entry, readback.Value, &accepted)
		}
		if typedValuesEqual(readback.Value, entry.mutation.Before) && !result.Accepted {
			return coordinator.persistRollbackUnknown(entry)
		}
		if typedValuesEqual(readback.Value, entry.mutation.Requested) && !result.Accepted {
			entry.mutation.Rollback.Error = sanitizeMutationError(writeTerminal)
			if entry.mutation.Rollback.Error == nil {
				entry.mutation.Rollback.Error = mutationError(eebusraw.ErrorCodeV1RemoteError, false)
			}
			return coordinator.persistRollbackUnknown(entry)
		}
		if typedValuesEqual(readback.Value, entry.mutation.Requested) && result.Accepted {
			return coordinator.persistRollbackUnknown(entry)
		}
		return coordinator.persistConflict(entry, readback.Value)
	}
	if result.FrameSent {
		entry.mutation.Rollback.ProtocolAccepted = nil
		entry.mutation.OutcomeEvidence = &eebusraw.OutcomeEvidenceV1{
			PossibleSideEffect:  true,
			BlindRetryForbidden: true,
			LastDurableState:    eebusraw.MutationStateV1RollbackDispatchIntent,
			RecordedAt:          normalizedNow(coordinator.config.Now),
		}
		entry.mutation.Error = mutationError(eebusraw.ErrorCodeV1OutcomeUnknown, false)
		if terminal := coordinator.transition(entry, eebusraw.MutationStateV1OutcomeUnknown); terminal != nil {
			return cloneMutation(entry.mutation), terminal
		}
		readback, terminal := coordinator.readCurrent(ctx, entry.mutation.Target)
		return coordinator.resolveUncertainRollbackReadback(entry, readback, terminal)
	}
	entry.mutation.Rollback.Error = sanitizeMutationError(writeTerminal)
	if entry.mutation.Rollback.Error == nil {
		entry.mutation.Rollback.Error = mutationError(eebusraw.ErrorCodeV1Disconnected, true)
	}
	return coordinator.persistRollbackUnknown(entry)
}

func (coordinator *rawMutationCoordinator) recoverRollback(
	ctx context.Context,
	entry *rawMutationEntry,
) (eebusraw.MutationV1, *eebusraw.ErrorV1) {
	switch entry.mutation.State {
	case eebusraw.MutationStateV1RollbackIntent:
		return coordinator.continueRollbackFromIntent(ctx, entry)
	case eebusraw.MutationStateV1RollbackDispatchIntent:
		readback, terminal := coordinator.readCurrent(ctx, entry.mutation.Target)
		return coordinator.resolveUncertainRollbackReadback(entry, readback, terminal)
	case eebusraw.MutationStateV1RollbackReplyObserved,
		eebusraw.MutationStateV1RollbackVerifyPending:
		readback, terminal := coordinator.readCurrent(ctx, entry.mutation.Target)
		if !coordinator.readbackIsTrustworthy(entry, readback, terminal, true) {
			return coordinator.persistRollbackUnknown(entry)
		}
		if typedValuesEqual(readback.Value, entry.mutation.Before) {
			return coordinator.persistRolledBack(entry, readback.Value, entry.mutation.Rollback.ProtocolAccepted)
		}
		if typedValuesEqual(readback.Value, entry.mutation.Requested) &&
			entry.mutation.Rollback != nil &&
			entry.mutation.Rollback.ProtocolAccepted != nil &&
			!*entry.mutation.Rollback.ProtocolAccepted {
			return coordinator.persistRollbackUnknown(entry)
		}
		return coordinator.persistConflict(entry, readback.Value)
	default:
		return cloneMutation(entry.mutation), terminalForMutation(entry.mutation)
	}
}

func (coordinator *rawMutationCoordinator) resolveUncertainRollbackReadback(
	entry *rawMutationEntry,
	readback rawMutationReadResult,
	terminal *eebusraw.ErrorV1,
) (eebusraw.MutationV1, *eebusraw.ErrorV1) {
	if !coordinator.readbackIsTrustworthy(entry, readback, terminal, true) {
		return coordinator.persistRollbackUnknown(entry)
	}
	entry.mutation.Runtime = readback.Runtime
	if typedValuesEqual(readback.Value, entry.mutation.Before) {
		return coordinator.persistRolledBack(entry, readback.Value, nil)
	}
	if typedValuesEqual(readback.Value, entry.mutation.Requested) {
		return coordinator.persistRollbackUnknown(entry)
	}
	return coordinator.persistConflict(entry, readback.Value)
}

func (coordinator *rawMutationCoordinator) persistRolledBack(
	entry *rawMutationEntry,
	observed eebusraw.TypedValueV1,
	protocolAccepted *bool,
) (eebusraw.MutationV1, *eebusraw.ErrorV1) {
	hash, err := observed.ComputeHash()
	if err != nil {
		return cloneMutation(entry.mutation), internalMutationError()
	}
	if entry.mutation.Rollback == nil {
		entry.mutation.Rollback = &eebusraw.RollbackV1{Before: entry.mutation.Before.Clone()}
	}
	entry.mutation.Rollback.State = eebusraw.MutationStateV1RolledBack
	entry.mutation.Rollback.ProtocolAccepted = cloneBool(protocolAccepted)
	entry.mutation.Rollback.ObservedAfter = cloneValue(&observed)
	entry.mutation.Rollback.Error = nil
	entry.mutation.Rollback.Verification = &eebusraw.RollbackVerificationV1{
		Relation:       "rollback_observed_after_equals_before",
		Verified:       true,
		EqualValueHash: hash,
		VerifiedAt:     normalizedNow(coordinator.config.Now),
	}
	entry.mutation.Error = nil
	if terminal := coordinator.transition(entry, eebusraw.MutationStateV1RolledBack); terminal != nil {
		return cloneMutation(entry.mutation), terminal
	}
	coordinator.cancelProbe(entry.mutation.MutationRef)
	return cloneMutation(entry.mutation), nil
}

func (coordinator *rawMutationCoordinator) persistRollbackUnknown(
	entry *rawMutationEntry,
) (eebusraw.MutationV1, *eebusraw.ErrorV1) {
	if entry.mutation.Rollback == nil {
		entry.mutation.Rollback = &eebusraw.RollbackV1{Before: entry.mutation.Before.Clone()}
	}
	entry.mutation.Rollback.Error = mutationError(eebusraw.ErrorCodeV1RollbackFailed, false)
	lastDurableState := entry.mutation.State
	if lastDurableState == eebusraw.MutationStateV1OutcomeUnknown &&
		entry.mutation.OutcomeEvidence != nil {
		lastDurableState = entry.mutation.OutcomeEvidence.LastDurableState
	}
	possibleSideEffect := lastDurableState != eebusraw.MutationStateV1RollbackIntent
	entry.mutation.OutcomeEvidence = &eebusraw.OutcomeEvidenceV1{
		PossibleSideEffect:  possibleSideEffect,
		BlindRetryForbidden: possibleSideEffect,
		LastDurableState:    lastDurableState,
		RecordedAt:          normalizedNow(coordinator.config.Now),
	}
	entry.mutation.Error = mutationError(eebusraw.ErrorCodeV1RollbackFailed, false)
	if entry.mutation.State != eebusraw.MutationStateV1OutcomeUnknown {
		if terminal := coordinator.transition(entry, eebusraw.MutationStateV1OutcomeUnknown); terminal != nil {
			return cloneMutation(entry.mutation), terminal
		}
	}
	return cloneMutation(entry.mutation), cloneTerminal(entry.mutation.Error)
}

func (coordinator *rawMutationCoordinator) rearmProbeTimers() {
	coordinator.mu.Lock()
	var entries []*rawMutationEntry
	for _, entry := range coordinator.entries {
		mutation := entry.snapshot()
		if mutation.Runtime.RuntimeEpoch == coordinator.config.RuntimeEpoch() &&
			mutation.State == eebusraw.MutationStateV1ProbeActive &&
			mutation.ProbeDeadline != nil {
			entries = append(entries, entry)
		}
	}
	coordinator.mu.Unlock()
	for _, entry := range entries {
		coordinator.scheduleProbe(entry)
	}
}

func (coordinator *rawMutationCoordinator) scheduleProbe(entry *rawMutationEntry) {
	mutation := entry.snapshot()
	if mutation.ProbeDeadline == nil {
		return
	}
	coordinator.scheduleProbeAt(entry, *mutation.ProbeDeadline)
}

func (coordinator *rawMutationCoordinator) scheduleProbeAt(
	entry *rawMutationEntry,
	deadline time.Time,
) {
	ref := entry.snapshot().MutationRef
	key := "probe:" + ref
	timer := coordinator.deps.Scheduler.Schedule(deadline, func() {
		if !coordinator.acquireInternalWriter() {
			coordinator.mu.Lock()
			current := coordinator.entries[ref]
			closed := coordinator.closed
			quarantined := coordinator.quarantined
			eligible := current != nil &&
				current.snapshot().State == eebusraw.MutationStateV1ProbeActive
			coordinator.mu.Unlock()
			if !closed && !quarantined && eligible {
				coordinator.scheduleProbeAt(
					current,
					normalizedNow(coordinator.config.Now).Add(coordinator.retryDelay()),
				)
			}
			return
		}
		defer coordinator.releaseWriter()
		coordinator.mu.Lock()
		current := coordinator.entries[ref]
		delete(coordinator.timers, key)
		eligible := current != nil &&
			current.snapshot().State == eebusraw.MutationStateV1ProbeActive
		coordinator.mu.Unlock()
		if !eligible {
			return
		}
		_, _ = coordinator.startRollback(coordinator.ctx, current)
	})
	coordinator.mu.Lock()
	if previous := coordinator.timers[key]; previous != nil {
		previous.Stop()
	}
	if !coordinator.closed && !coordinator.quarantined {
		coordinator.timers[key] = timer
	} else {
		timer.Stop()
	}
	coordinator.mu.Unlock()
}

func (coordinator *rawMutationCoordinator) cancelProbe(ref string) {
	key := "probe:" + ref
	coordinator.mu.Lock()
	timer := coordinator.timers[key]
	delete(coordinator.timers, key)
	coordinator.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
}
