package eebusfacade

import (
	"context"
	"time"
)

func (coordinator *firstTrustCoordinator) retryRuntimeEnabled() bool {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return coordinator.recoveryStore != nil && coordinator.anchor != nil
}

func (coordinator *firstTrustCoordinator) retryState(scope [32]byte) (string, uint64, time.Duration, bool) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	_, record, ok := coordinator.firstTrustQuarantineLocked(scope)
	if !ok {
		return "", 0, 0, false
	}
	return record.state, record.attemptCount, record.remainingDelay, true
}

func (coordinator *firstTrustCoordinator) admitRetry(ctx context.Context, scope [32]byte) string {
	ctx = firstTrustContext(ctx)
	if ctx.Err() != nil {
		return "request_cancelled"
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.recoveryStore == nil || coordinator.anchor == nil {
		return "mutation_disabled"
	}
	if coordinator.reconciliationRequiredLocked() {
		return "reconciliation_required"
	}
	if coordinator.recoveryOperation != nil {
		return "operation_in_progress"
	}
	index, record, exists := coordinator.firstTrustQuarantineLocked(scope)
	if !exists {
		if len(coordinator.controlView.control.quarantines) >= firstTrustMaximumQuarantineRecords {
			return "quarantine_capacity"
		}
		record = firstTrustQuarantineRecord{
			scope: scope, reason: "RETRYABLE_FAILURE", state: "RETRY_READY",
			retentionBudget: firstTrustQuarantineRetention, lastControlEpoch: coordinator.controlView.control.controlEpoch + 1,
		}
		target := cloneFirstTrustControlRecord(coordinator.controlView.control)
		target.controlEpoch++
		target.quarantines = append(target.quarantines, record)
		if coordinator.publishFirstTrustRetryLocked(ctx, target) != "durable" {
			return "retry_state_failed_closed"
		}
		coordinator.retryInflight[scope] = true
		return "retry_admitted"
	}
	if coordinator.retryInflight[scope] {
		return "attempt_in_progress"
	}
	switch record.state {
	case "ADMIN_HOLD":
		return "admin_hold"
	case "RETRY_READY":
		coordinator.retryInflight[scope] = true
		return "retry_admitted"
	case "BACKOFF_ACTIVE":
		arm, armed := coordinator.retryArms[scope]
		if !armed {
			now := coordinator.monotonicNow()
			arm = firstTrustRetryArm{armedAt: now, deadline: firstTrustSaturatingDurationAdd(now, record.remainingDelay)}
			coordinator.retryArms[scope] = arm
		}
		if coordinator.monotonicNow() < arm.deadline {
			return "backoff_active"
		}
		target := cloneFirstTrustControlRecord(coordinator.controlView.control)
		target.controlEpoch++
		record.state = "RETRY_READY"
		record.remainingDelay = 0
		record.lastControlEpoch = target.controlEpoch
		target.quarantines[index] = record
		if coordinator.publishFirstTrustRetryLocked(ctx, target) != "durable" {
			return "ready_transition_failed_closed"
		}
		delete(coordinator.retryArms, scope)
		coordinator.retryInflight[scope] = true
		return "retry_admitted"
	default:
		return "admin_hold"
	}
}

func (coordinator *firstTrustCoordinator) recordRetryFailure(ctx context.Context, scope [32]byte) string {
	ctx = firstTrustContext(ctx)
	if ctx.Err() != nil {
		return "request_cancelled"
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.recoveryStore == nil || coordinator.anchor == nil {
		return "mutation_disabled"
	}
	if coordinator.reconciliationRequiredLocked() {
		return "reconciliation_required"
	}
	if !coordinator.retryInflight[scope] {
		return "attempt_not_in_progress"
	}
	index, record, ok := coordinator.firstTrustQuarantineLocked(scope)
	if !ok || record.state != "RETRY_READY" {
		return "attempt_not_in_progress"
	}
	nextCount, delay, valid := firstTrustNextBackoff(coordinator.backoffPolicy, record.attemptCount)
	if !valid {
		return "admin_hold"
	}
	target := cloneFirstTrustControlRecord(coordinator.controlView.control)
	if target.controlEpoch == ^uint64(0) {
		return "admin_hold"
	}
	target.controlEpoch++
	record.reason = "RETRYABLE_FAILURE"
	record.attemptCount = nextCount
	if nextCount == coordinator.backoffPolicy.attemptMaximum {
		record.reason = "HANDSHAKE_ATTEMPT_LIMIT"
		record.state = "ADMIN_HOLD"
		record.remainingDelay = 0
	} else {
		record.state = "BACKOFF_ACTIVE"
		record.remainingDelay = delay
	}
	record.backoffStep = nextCount - 1
	if record.backoffStep > uint64(coordinator.backoffPolicy.exponentCap) {
		record.backoffStep = uint64(coordinator.backoffPolicy.exponentCap)
	}
	record.retentionBudget = firstTrustQuarantineRetention
	record.lastControlEpoch = target.controlEpoch
	target.quarantines[index] = record
	publicationOutcome := coordinator.publishFirstTrustRetryLocked(ctx, target)
	if publicationOutcome != "durable" {
		if publicationOutcome == "unchanged" {
			coordinator.stageFirstTrustRetryFailureHoldLocked(ctx, scope)
		}
		delete(coordinator.retryInflight, scope)
		return "failure_state_failed_closed"
	}
	delete(coordinator.retryInflight, scope)
	if record.state == "ADMIN_HOLD" {
		delete(coordinator.retryArms, scope)
		return "admin_hold"
	}
	now := coordinator.monotonicNow()
	coordinator.retryArms[scope] = firstTrustRetryArm{armedAt: now, deadline: firstTrustSaturatingDurationAdd(now, delay)}
	return "backoff_active"
}

func (coordinator *firstTrustCoordinator) completeRetry(scope [32]byte) {
	coordinator.mu.Lock()
	delete(coordinator.retryInflight, scope)
	coordinator.mu.Unlock()
}

func (coordinator *firstTrustCoordinator) checkpointRetry(ctx context.Context, scope [32]byte) string {
	ctx = firstTrustContext(ctx)
	if ctx.Err() != nil {
		return "request_cancelled"
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.recoveryStore == nil || coordinator.anchor == nil {
		return "mutation_disabled"
	}
	if coordinator.reconciliationRequiredLocked() {
		return "reconciliation_required"
	}
	index, record, ok := coordinator.firstTrustQuarantineLocked(scope)
	if !ok || record.state != "BACKOFF_ACTIVE" {
		return "checkpoint_not_applicable"
	}
	arm, armed := coordinator.retryArms[scope]
	if !armed {
		return "checkpoint_not_applicable"
	}
	now := coordinator.monotonicNow()
	remaining := time.Duration(0)
	if now < arm.deadline {
		remaining = arm.deadline - now
	}
	if remaining >= record.remainingDelay {
		return "checkpoint_durable"
	}
	target := cloneFirstTrustControlRecord(coordinator.controlView.control)
	target.controlEpoch++
	if remaining == 0 {
		record.state = "RETRY_READY"
	}
	record.remainingDelay = remaining
	record.lastControlEpoch = target.controlEpoch
	target.quarantines[index] = record
	if coordinator.publishFirstTrustRetryLocked(ctx, target) != "durable" {
		return "checkpoint_failed_closed"
	}
	if remaining == 0 {
		delete(coordinator.retryArms, scope)
	} else {
		coordinator.retryArms[scope] = firstTrustRetryArm{armedAt: now, deadline: firstTrustSaturatingDurationAdd(now, remaining)}
	}
	return "checkpoint_durable"
}

func (coordinator *firstTrustCoordinator) firstTrustQuarantineLocked(scope [32]byte) (int, firstTrustQuarantineRecord, bool) {
	for index, record := range coordinator.controlView.control.quarantines {
		if record.scope == scope {
			return index, record, true
		}
	}
	return -1, firstTrustQuarantineRecord{}, false
}

func (coordinator *firstTrustCoordinator) publishFirstTrustRetryLocked(ctx context.Context, target firstTrustControlRecord) string {
	operationID, ok := firstTrustReadOrdinal(coordinator.random)
	if !ok {
		return "prepare_failed"
	}
	working := cloneFirstTrustControlView(coordinator.controlView)
	publication, outcome, anchor := coordinator.publishFirstTrustControl(
		ctx, working, target, operationID, "first_trust", cloneFirstTrustControlView(coordinator.controlView), cloneFirstTrustAnchorRecord(coordinator.anchorRecord),
	)
	coordinator.anchorRecord = cloneFirstTrustAnchorRecord(anchor)
	if outcome == "durable" {
		coordinator.controlView = cloneFirstTrustControlView(publication.target)
		coordinator.storeGeneration = publication.target.manifest.current.sequence
		return "durable"
	}
	if outcome == "unknown" {
		coordinator.phase = firstTrustDisabled
		coordinator.recovery = "QUARANTINED"
		coordinator.recoveryReasonCode = "DURABILITY_UNKNOWN"
		coordinator.trustedRemotes = make(map[string]string)
	}
	return outcome
}

func (coordinator *firstTrustCoordinator) stageFirstTrustRetryFailureHoldLocked(ctx context.Context, scope [32]byte) bool {
	operationID, ok := firstTrustReadOrdinal(coordinator.random)
	if !ok || coordinator.controlView.control.controlEpoch == ^uint64(0) {
		coordinator.enterFirstTrustQuarantineLocked(nil)
		return false
	}
	target := cloneFirstTrustControlRecord(coordinator.controlView.control)
	target.controlEpoch++
	index, record, exists := coordinator.firstTrustQuarantineLocked(scope)
	if !exists {
		if len(target.quarantines) >= firstTrustMaximumQuarantineRecords {
			coordinator.enterFirstTrustQuarantineLocked(nil)
			return false
		}
		record = firstTrustQuarantineRecord{scope: scope, retentionBudget: firstTrustQuarantineRetention}
		target.quarantines = append(target.quarantines, record)
		index = len(target.quarantines) - 1
	}
	record.state = "ADMIN_HOLD"
	record.reason = "DURABILITY_UNKNOWN"
	record.remainingDelay = 0
	record.lastControlEpoch = target.controlEpoch
	target.quarantines[index] = record
	selected := cloneFirstTrustControlView(coordinator.controlView)
	publication, outcome := coordinator.recoveryStore.PrepareControl(ctx, selected, target, operationID, "first_trust")
	if outcome != "prepared" || !firstTrustPreparedPublicationValid(publication, selected, operationID, "first_trust") {
		coordinator.enterFirstTrustQuarantineLocked(nil)
		return false
	}
	pending := firstTrustPendingFromPrepared(publication)
	stageOutcome := coordinator.anchor.CompareAndStage(ctx, cloneFirstTrustAnchorRecord(coordinator.anchorRecord), pending)
	if stageOutcome != "anchor_durable" {
		coordinator.enterFirstTrustQuarantineLocked(nil)
		return false
	}
	coordinator.enterFirstTrustQuarantineLocked(&pending)
	return true
}
