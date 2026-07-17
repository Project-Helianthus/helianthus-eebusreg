package eebusfacade

import (
	"bytes"
	"context"
	"crypto/sha256"
	"math"
	"strings"
)

const (
	firstTrustAttemptReserved         = "ATTEMPT_RESERVED"
	firstTrustAttemptLaunchAuthorized = "ATTEMPT_LAUNCH_AUTHORIZED"
)

func (coordinator *firstTrustCoordinator) prepareOutgoingAttempt(
	ctx context.Context,
	request firstTrustOutgoingAttemptRequest,
) (*firstTrustOutgoingAttemptHandle, string) {
	ctx = firstTrustContext(ctx)
	request.remoteSKI = bytes.Clone(request.remoteSKI)
	request.endpoint.host = strings.TrimSpace(request.endpoint.host)
	if ctx.Err() != nil || !firstTrustOutgoingAttemptRequestValid(request) {
		return nil, "attempt_denied"
	}
	unlock := coordinator.lockOutgoingAttemptLane(request.remoteSKI)
	defer unlock()
	return coordinator.prepareOutgoingAttemptLocked(ctx, request, nil)
}

func (coordinator *firstTrustCoordinator) prepareOutgoingAttemptLocked(
	ctx context.Context,
	request firstTrustOutgoingAttemptRequest,
	failed *firstTrustOutgoingAttemptMetadata,
) (*firstTrustOutgoingAttemptHandle, string) {
	if ctx.Err() != nil || !firstTrustOutgoingAttemptRequestValid(request) {
		return nil, "attempt_denied"
	}

	coordinator.mu.Lock()
	if !coordinator.firstTrustOutgoingAttemptEligibleLocked(request.remoteSKI) ||
		(failed == nil && len(coordinator.controlView.control.attempts) >= firstTrustMaximumOutgoingAttempts) ||
		coordinator.controlView.control.controlEpoch == math.MaxUint64 {
		coordinator.mu.Unlock()
		return nil, "attempt_denied"
	}
	scope := firstTrustRuntimeRetryScope(firstTrustNormalizedSKI(request.remoteSKI))
	failedIndex := -1
	var failedRecord firstTrustOutgoingAttemptRecord
	if failed != nil {
		failedIndex = coordinator.firstTrustOutgoingAttemptMetadataLocked(*failed)
		if failedIndex < 0 {
			coordinator.mu.Unlock()
			return nil, "attempt_denied"
		}
		failedRecord = coordinator.controlView.control.attempts[failedIndex]
		failedRuntime, runtimeOK := coordinator.outgoingAttemptContexts[failed.attemptID]
		if failedRecord.state != firstTrustAttemptLaunchAuthorized || failedRecord.scope != scope ||
			!bytes.Equal(failedRecord.remoteSKI, request.remoteSKI) || !runtimeOK ||
			failedRuntime.metadata != *failed || failedRuntime.cancellationGeneration != failedRecord.cancellationGeneration {
			coordinator.mu.Unlock()
			return nil, "attempt_denied"
		}
	} else if coordinator.firstTrustOutgoingAttemptForScopeLocked(scope) >= 0 {
		coordinator.mu.Unlock()
		return nil, "attempt_denied"
	}
	quarantineIndex, quarantine, quarantineExists := coordinator.firstTrustQuarantineLocked(scope)
	if quarantineExists && !coordinator.firstTrustOutgoingAttemptRetryReadyLocked(quarantine) {
		coordinator.mu.Unlock()
		return nil, "attempt_denied"
	}
	if coordinator.retryInflight[scope] != (failed != nil) {
		coordinator.mu.Unlock()
		return nil, "attempt_denied"
	}
	attemptID, idOK := firstTrustReadOrdinal(coordinator.random)
	publicationID, publicationOK := firstTrustReadOrdinal(coordinator.random)
	if !idOK || !publicationOK || coordinator.outgoingAttemptCancellationGeneration == math.MaxUint64 ||
		coordinator.outgoingAttemptReservationOrder == math.MaxUint64 {
		coordinator.mu.Unlock()
		return nil, "attempt_denied"
	}
	for _, existing := range coordinator.controlView.control.attempts {
		if existing.attemptID == attemptID {
			coordinator.mu.Unlock()
			return nil, "attempt_denied"
		}
	}
	target := cloneFirstTrustControlRecord(coordinator.controlView.control)
	target.controlEpoch++
	if !quarantineExists {
		if len(target.quarantines) >= firstTrustMaximumQuarantineRecords {
			coordinator.mu.Unlock()
			return nil, "attempt_denied"
		}
		quarantine = firstTrustQuarantineRecord{
			scope: scope, reason: "RETRYABLE_FAILURE", state: "RETRY_READY",
			retentionBudget: firstTrustQuarantineRetention, lastControlEpoch: target.controlEpoch,
		}
		target.quarantines = append(target.quarantines, quarantine)
	} else if quarantine.state == "BACKOFF_ACTIVE" {
		quarantine.state = "RETRY_READY"
		quarantine.remainingDelay = 0
		quarantine.lastControlEpoch = target.controlEpoch
		target.quarantines[quarantineIndex] = quarantine
	}
	coordinator.outgoingAttemptCancellationGeneration++
	coordinator.outgoingAttemptReservationOrder++
	timestamp := coordinator.now().UnixNano()
	if timestamp < 0 {
		timestamp = 0
	}
	record := firstTrustOutgoingAttemptRecord{
		state: firstTrustAttemptReserved, attemptID: attemptID, remoteSKI: bytes.Clone(request.remoteSKI), scope: scope,
		controlEpoch: target.controlEpoch, associationLineage: target.associationLineage, endpoint: request.endpoint,
		path: request.path, cancellationGeneration: coordinator.outgoingAttemptCancellationGeneration,
		reservationOrder: coordinator.outgoingAttemptReservationOrder, reservationTimestamp: timestamp,
		attemptCountBefore: quarantine.attemptCount,
	}
	if failedIndex >= 0 {
		target.attempts = append(target.attempts[:failedIndex], target.attempts[failedIndex+1:]...)
	}
	target.attempts = append(target.attempts, record)
	attemptContext, cancel := context.WithCancel(ctx)
	expectedEpoch := coordinator.controlView.control.controlEpoch
	coordinator.mu.Unlock()

	operationClass := "attempt_prepare"
	if failed != nil {
		operationClass = "attempt_fallback_prepare"
	}
	publication, outcome := coordinator.publishOutgoingAttemptControl(ctx, expectedEpoch, target, publicationID, operationClass)
	if outcome != "durable" {
		cancel()
		return nil, "attempt_denied"
	}
	if failed != nil {
		coordinator.cancelOutgoingAttemptRuntime(failedRecord.attemptID)
	}
	metadata := firstTrustOutgoingAttemptMetadata{attemptID: attemptID, scope: scope, controlEpoch: record.controlEpoch}
	handle := &firstTrustOutgoingAttemptHandle{
		metadata: metadata, context: attemptContext, cancellationGeneration: record.cancellationGeneration,
	}
	coordinator.mu.Lock()
	if coordinator.firstTrustOutgoingAttemptExactLocked(metadata, record.cancellationGeneration) < 0 ||
		len(coordinator.outgoingAttemptContexts) >= firstTrustMaximumOutgoingAttempts {
		coordinator.mu.Unlock()
		cancel()
		return nil, "attempt_denied"
	}
	coordinator.outgoingAttemptContexts[attemptID] = firstTrustOutgoingAttemptRuntime{
		metadata: metadata, context: attemptContext, cancel: cancel, cancellationGeneration: record.cancellationGeneration,
	}
	coordinator.retryInflight[scope] = true
	coordinator.storeGeneration = publication.target.manifest.current.sequence
	coordinator.mu.Unlock()
	return handle, "attempt_reserved"
}

func (coordinator *firstTrustCoordinator) authorizeOutgoingAttempt(
	ctx context.Context,
	handle *firstTrustOutgoingAttemptHandle,
) (firstTrustOutgoingAttemptPermit, string) {
	denied := firstTrustOutgoingAttemptPermit{decision: "DENY", reason: "STALE_HANDLE"}
	ctx = firstTrustContext(ctx)
	if ctx.Err() != nil || handle == nil || handle.context == nil {
		return denied, "attempt_denied"
	}
	remote, ok := coordinator.firstTrustOutgoingAttemptRemote(handle.metadata)
	if !ok {
		return denied, "attempt_denied"
	}
	unlock := coordinator.lockOutgoingAttemptLane(remote)
	defer unlock()

	coordinator.mu.Lock()
	index := coordinator.firstTrustOutgoingAttemptExactLocked(handle.metadata, handle.cancellationGeneration)
	if index < 0 || coordinator.controlView.control.attempts[index].state != firstTrustAttemptReserved {
		coordinator.mu.Unlock()
		return denied, "attempt_denied"
	}
	runtime, runtimeOK := coordinator.outgoingAttemptContexts[handle.metadata.attemptID]
	if !runtimeOK || runtime.cancellationGeneration != handle.cancellationGeneration ||
		runtime.context != handle.context || runtime.metadata != handle.metadata {
		coordinator.mu.Unlock()
		return denied, "attempt_denied"
	}
	record := coordinator.controlView.control.attempts[index]
	if !coordinator.firstTrustOutgoingAttemptEligibleLocked(record.remoteSKI) || !coordinator.firstTrustOutgoingAttemptRecordEligibleLocked(record) ||
		coordinator.controlView.control.controlEpoch == math.MaxUint64 {
		coordinator.mu.Unlock()
		coordinator.cancelOutgoingAttemptRuntime(handle.metadata.attemptID)
		denied.reason = "POLICY_DENIED"
		return denied, "attempt_denied"
	}
	publicationID, publicationOK := firstTrustReadOrdinal(coordinator.random)
	if !publicationOK {
		coordinator.mu.Unlock()
		coordinator.cancelOutgoingAttemptRuntime(handle.metadata.attemptID)
		denied.reason = "POLICY_DENIED"
		return denied, "attempt_denied"
	}
	target := cloneFirstTrustControlRecord(coordinator.controlView.control)
	target.controlEpoch++
	target.attempts[index].state = firstTrustAttemptLaunchAuthorized
	expectedEpoch := coordinator.controlView.control.controlEpoch
	coordinator.mu.Unlock()

	_, outcome := coordinator.publishOutgoingAttemptControl(ctx, expectedEpoch, target, publicationID, "attempt_authorize")
	if outcome != "durable" {
		coordinator.cancelOutgoingAttemptRuntime(handle.metadata.attemptID)
		denied.reason = "POLICY_DENIED"
		return denied, "attempt_denied"
	}
	return firstTrustOutgoingAttemptPermit{
		decision: "PERMIT", reason: "AUTHORIZED", metadata: handle.metadata, context: handle.context,
	}, "attempt_permitted"
}

func (coordinator *firstTrustCoordinator) abortPreparedOutgoingAttempt(
	ctx context.Context,
	handle *firstTrustOutgoingAttemptHandle,
) string {
	ctx = firstTrustContext(ctx)
	if ctx.Err() != nil || handle == nil {
		return "stale_attempt"
	}
	remote, ok := coordinator.firstTrustOutgoingAttemptRemote(handle.metadata)
	if !ok {
		return "stale_attempt"
	}
	unlock := coordinator.lockOutgoingAttemptLane(remote)
	defer unlock()

	coordinator.mu.Lock()
	index := coordinator.firstTrustOutgoingAttemptExactLocked(handle.metadata, handle.cancellationGeneration)
	if index < 0 || coordinator.controlView.control.attempts[index].state != firstTrustAttemptReserved ||
		coordinator.controlView.control.controlEpoch == math.MaxUint64 {
		coordinator.mu.Unlock()
		return "stale_attempt"
	}
	publicationID, publicationOK := firstTrustReadOrdinal(coordinator.random)
	if !publicationOK {
		coordinator.mu.Unlock()
		coordinator.cancelOutgoingAttemptRuntime(handle.metadata.attemptID)
		return "attempt_abort_failed_closed"
	}
	target := cloneFirstTrustControlRecord(coordinator.controlView.control)
	target.controlEpoch++
	target.attempts = append(target.attempts[:index], target.attempts[index+1:]...)
	expectedEpoch := coordinator.controlView.control.controlEpoch
	coordinator.mu.Unlock()

	_, outcome := coordinator.publishOutgoingAttemptControl(ctx, expectedEpoch, target, publicationID, "attempt_abort")
	coordinator.cancelOutgoingAttemptRuntime(handle.metadata.attemptID)
	if outcome != "durable" {
		return "attempt_abort_failed_closed"
	}
	coordinator.mu.Lock()
	delete(coordinator.retryInflight, handle.metadata.scope)
	coordinator.mu.Unlock()
	return "attempt_aborted"
}

func (coordinator *firstTrustCoordinator) completeOutgoingAttempt(
	ctx context.Context,
	metadata firstTrustOutgoingAttemptMetadata,
	succeeded bool,
) string {
	ctx = firstTrustContext(ctx)
	if ctx.Err() != nil {
		return "stale_attempt"
	}
	remote, ok := coordinator.firstTrustOutgoingAttemptRemote(metadata)
	if !ok {
		return "stale_attempt"
	}
	unlock := coordinator.lockOutgoingAttemptLane(remote)
	defer unlock()
	return coordinator.completeOutgoingAttemptLocked(ctx, metadata, remote, succeeded)
}

func (coordinator *firstTrustCoordinator) completeOutgoingAttemptLocked(
	ctx context.Context,
	metadata firstTrustOutgoingAttemptMetadata,
	remote []byte,
	succeeded bool,
) string {
	coordinator.mu.Lock()
	index := coordinator.firstTrustOutgoingAttemptMetadataLocked(metadata)
	if index < 0 || coordinator.controlView.control.attempts[index].state != firstTrustAttemptLaunchAuthorized ||
		!bytes.Equal(coordinator.controlView.control.attempts[index].remoteSKI, remote) ||
		coordinator.controlView.control.controlEpoch == math.MaxUint64 {
		coordinator.mu.Unlock()
		return "stale_attempt"
	}
	record := cloneFirstTrustOutgoingAttemptRecord(coordinator.controlView.control.attempts[index])
	publicationID, publicationOK := firstTrustReadOrdinal(coordinator.random)
	if !publicationOK {
		coordinator.mu.Unlock()
		coordinator.cancelOutgoingAttemptRuntime(metadata.attemptID)
		return "failure_state_failed_closed"
	}
	target := cloneFirstTrustControlRecord(coordinator.controlView.control)
	target.controlEpoch++
	target.attempts = append(target.attempts[:index], target.attempts[index+1:]...)
	result := "attempt_succeeded"
	operationClass := "attempt_complete_success"
	if succeeded {
		coordinator.firstTrustResetOutgoingAttemptRetryLocked(&target, record.scope)
	} else {
		operationClass = "attempt_complete_failure"
		var charged bool
		result, charged = coordinator.firstTrustChargeOutgoingAttemptFailureLocked(&target, record)
		if !charged {
			coordinator.mu.Unlock()
			coordinator.cancelOutgoingAttemptRuntime(metadata.attemptID)
			return "failure_state_failed_closed"
		}
	}
	expectedEpoch := coordinator.controlView.control.controlEpoch
	coordinator.mu.Unlock()

	_, outcome := coordinator.publishOutgoingAttemptControl(ctx, expectedEpoch, target, publicationID, operationClass)
	coordinator.cancelOutgoingAttemptRuntime(metadata.attemptID)
	coordinator.mu.Lock()
	delete(coordinator.retryInflight, record.scope)
	if outcome == "durable" {
		coordinator.updateOutgoingAttemptRetryArmLocked(record.scope)
	}
	coordinator.mu.Unlock()
	if outcome != "durable" {
		return "failure_state_failed_closed"
	}
	return result
}

func (coordinator *firstTrustCoordinator) outgoingAttemptCallbackExactLocked(
	metadata firstTrustOutgoingAttemptMetadata,
	remote []byte,
) bool {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	index := coordinator.firstTrustOutgoingAttemptMetadataLocked(metadata)
	if index < 0 {
		return false
	}
	record := coordinator.controlView.control.attempts[index]
	runtime, ok := coordinator.outgoingAttemptContexts[metadata.attemptID]
	return record.state == firstTrustAttemptLaunchAuthorized && bytes.Equal(record.remoteSKI, remote) && ok &&
		runtime.metadata == metadata && runtime.cancellationGeneration == record.cancellationGeneration && runtime.context != nil &&
		runtime.context.Err() == nil
}

func (coordinator *firstTrustCoordinator) outgoingAttemptConnectionClosed(
	ctx context.Context,
	remoteSKI string,
	complete bool,
	metadata firstTrustOutgoingAttemptMetadata,
) string {
	remote, normalized, ok := decodeFirstTrustSKI(strings.ToLower(strings.TrimSpace(remoteSKI)))
	if !ok || normalized != firstTrustNormalizedSKI(remote) {
		return "stale_attempt"
	}
	ctx = firstTrustContext(ctx)
	if ctx.Err() != nil {
		return "stale_attempt"
	}
	unlock := coordinator.lockOutgoingAttemptLane(remote)
	defer unlock()
	if !coordinator.outgoingAttemptCallbackExactLocked(metadata, remote) {
		return "stale_attempt"
	}
	return coordinator.completeOutgoingAttemptLocked(ctx, metadata, remote, complete)
}

func (coordinator *firstTrustCoordinator) outgoingAttemptHandshakeStateUpdate(
	ctx context.Context,
	remoteSKI string,
	state string,
	metadata firstTrustOutgoingAttemptMetadata,
) string {
	remote, normalized, ok := decodeFirstTrustSKI(strings.ToLower(strings.TrimSpace(remoteSKI)))
	if !ok || normalized != firstTrustNormalizedSKI(remote) {
		return "stale_attempt"
	}
	ctx = firstTrustContext(ctx)
	if ctx.Err() != nil {
		return "stale_attempt"
	}
	unlock := coordinator.lockOutgoingAttemptLane(remote)
	defer unlock()
	if !coordinator.outgoingAttemptCallbackExactLocked(metadata, remote) {
		return "stale_attempt"
	}
	if strings.EqualFold(state, "error") {
		return coordinator.completeOutgoingAttemptLocked(ctx, metadata, remote, false)
	}
	return "attempt_observed"
}

func (coordinator *firstTrustCoordinator) publishOutgoingAttemptControl(
	ctx context.Context,
	expectedEpoch uint64,
	target firstTrustControlRecord,
	operationID [32]byte,
	operationClass string,
) (firstTrustPreparedPublication, string) {
	coordinator.mu.Lock()
	if coordinator.recoveryStore == nil || coordinator.anchor == nil || coordinator.controlView.control.controlEpoch != expectedEpoch {
		coordinator.mu.Unlock()
		return firstTrustPreparedPublication{}, "unchanged"
	}
	working := cloneFirstTrustControlView(coordinator.controlView)
	selected := cloneFirstTrustControlView(coordinator.controlView)
	anchor := cloneFirstTrustAnchorRecord(coordinator.anchorRecord)
	coordinator.mu.Unlock()

	publication, outcome, anchor := coordinator.publishFirstTrustControl(
		ctx, working, target, operationID, operationClass, selected, anchor,
	)
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	coordinator.anchorRecord = cloneFirstTrustAnchorRecord(anchor)
	switch outcome {
	case "durable":
		coordinator.controlView = cloneFirstTrustControlView(publication.target)
		coordinator.storeGeneration = publication.target.manifest.current.sequence
	case "unknown":
		coordinator.phase = firstTrustDisabled
		coordinator.recovery = "QUARANTINED"
		coordinator.recoveryReasonCode = "DURABILITY_UNKNOWN"
		coordinator.trustedRemotes = make(map[string]string)
	}
	return publication, outcome
}

func (coordinator *firstTrustCoordinator) firstTrustOutgoingAttemptEligibleLocked(remote []byte) bool {
	if len(remote) != 20 || coordinator.recoveryStore == nil || coordinator.anchor == nil || coordinator.reopening ||
		coordinator.recoveryOperation != nil || coordinator.reconciliationRequiredLocked() || coordinator.phase == firstTrustDisabled ||
		coordinator.recovery == "REVOKED" || coordinator.recovery == "QUARANTINED" || coordinator.recovery == "CORRUPT_STORE" ||
		coordinator.recovery == "NO_LOCAL_IDENTITY" {
		return false
	}
	for _, association := range coordinator.controlView.associations {
		if bytes.Equal(association.subject, remote) && firstTrustAssociationUsable(association, coordinator.controlView.control.associationLineage) &&
			!coordinator.firstTrustTombstonedLocked(association) && coordinator.recovery == "PAIRED_TRUSTED" {
			_, trusted := coordinator.trustedRemotes[string(remote)]
			return trusted
		}
	}
	if coordinator.recovery != "UNPAIRED_LOCKED" {
		return false
	}
	return coordinator.phase == firstTrustOpenEmpty && coordinator.window != nil ||
		coordinator.currentCandidate != nil && bytes.Equal(coordinator.currentCandidate.remote, remote) &&
			(coordinator.phase == firstTrustCandidatePending || coordinator.phase == firstTrustCommitting)
}

func (coordinator *firstTrustCoordinator) firstTrustOutgoingAttemptRecordEligibleLocked(record firstTrustOutgoingAttemptRecord) bool {
	if record.associationLineage != coordinator.controlView.control.associationLineage ||
		record.scope != firstTrustRuntimeRetryScope(firstTrustNormalizedSKI(record.remoteSKI)) {
		return false
	}
	_, quarantine, ok := coordinator.firstTrustQuarantineLocked(record.scope)
	return ok && quarantine.state == "RETRY_READY" && quarantine.remainingDelay == 0
}

func (coordinator *firstTrustCoordinator) firstTrustOutgoingAttemptRetryReadyLocked(record firstTrustQuarantineRecord) bool {
	switch record.state {
	case "RETRY_READY":
		return record.remainingDelay == 0
	case "BACKOFF_ACTIVE":
		arm, armed := coordinator.retryArms[record.scope]
		if !armed {
			now := coordinator.monotonicNow()
			arm = firstTrustRetryArm{armedAt: now, deadline: firstTrustSaturatingDurationAdd(now, record.remainingDelay)}
			coordinator.retryArms[record.scope] = arm
		}
		return coordinator.monotonicNow() >= arm.deadline
	default:
		return false
	}
}

func (coordinator *firstTrustCoordinator) firstTrustChargeOutgoingAttemptFailureLocked(
	target *firstTrustControlRecord,
	attempt firstTrustOutgoingAttemptRecord,
) (string, bool) {
	index := -1
	var quarantine firstTrustQuarantineRecord
	for candidate, record := range target.quarantines {
		if record.scope == attempt.scope {
			index, quarantine = candidate, record
			break
		}
	}
	if index < 0 {
		if len(target.quarantines) >= firstTrustMaximumQuarantineRecords {
			return "failure_state_failed_closed", false
		}
		quarantine = firstTrustQuarantineRecord{
			scope: attempt.scope, reason: "RETRYABLE_FAILURE", state: "RETRY_READY",
			retentionBudget: firstTrustQuarantineRetention,
		}
		target.quarantines = append(target.quarantines, quarantine)
		index = len(target.quarantines) - 1
	}
	current := quarantine.attemptCount
	if current < attempt.attemptCountBefore {
		current = attempt.attemptCountBefore
	}
	next, delay, valid := firstTrustNextBackoff(coordinator.backoffPolicy, current)
	if !valid {
		return "failure_state_failed_closed", false
	}
	quarantine.reason = "RETRYABLE_FAILURE"
	quarantine.attemptCount = next
	quarantine.backoffStep = next - 1
	if quarantine.backoffStep > uint64(coordinator.backoffPolicy.exponentCap) {
		quarantine.backoffStep = uint64(coordinator.backoffPolicy.exponentCap)
	}
	quarantine.remainingDelay = delay
	quarantine.retentionBudget = firstTrustQuarantineRetention
	quarantine.lastControlEpoch = target.controlEpoch
	result := "backoff_active"
	if next == coordinator.backoffPolicy.attemptMaximum {
		quarantine.reason = "HANDSHAKE_ATTEMPT_LIMIT"
		quarantine.state = "ADMIN_HOLD"
		quarantine.remainingDelay = 0
		result = "admin_hold"
	} else {
		quarantine.state = "BACKOFF_ACTIVE"
	}
	target.quarantines[index] = quarantine
	return result, true
}

func (coordinator *firstTrustCoordinator) firstTrustResetOutgoingAttemptRetryLocked(target *firstTrustControlRecord, scope [32]byte) {
	for index, quarantine := range target.quarantines {
		if quarantine.scope != scope {
			continue
		}
		quarantine.reason = "RETRYABLE_FAILURE"
		quarantine.state = "RETRY_READY"
		quarantine.attemptCount = 0
		quarantine.backoffStep = 0
		quarantine.remainingDelay = 0
		quarantine.retentionBudget = firstTrustQuarantineRetention
		quarantine.lastControlEpoch = target.controlEpoch
		target.quarantines[index] = quarantine
		return
	}
}

func (coordinator *firstTrustCoordinator) updateOutgoingAttemptRetryArmLocked(scope [32]byte) {
	_, quarantine, ok := coordinator.firstTrustQuarantineLocked(scope)
	if !ok || quarantine.state != "BACKOFF_ACTIVE" {
		delete(coordinator.retryArms, scope)
		return
	}
	now := coordinator.monotonicNow()
	coordinator.retryArms[scope] = firstTrustRetryArm{armedAt: now, deadline: firstTrustSaturatingDurationAdd(now, quarantine.remainingDelay)}
}

func (coordinator *firstTrustCoordinator) firstTrustOutgoingAttemptForScopeLocked(scope [32]byte) int {
	for index, attempt := range coordinator.controlView.control.attempts {
		if attempt.scope == scope {
			return index
		}
	}
	return -1
}

func (coordinator *firstTrustCoordinator) firstTrustOutgoingAttemptMetadataLocked(metadata firstTrustOutgoingAttemptMetadata) int {
	for index, attempt := range coordinator.controlView.control.attempts {
		if attempt.attemptID == metadata.attemptID && attempt.scope == metadata.scope && attempt.controlEpoch == metadata.controlEpoch {
			return index
		}
	}
	return -1
}

func (coordinator *firstTrustCoordinator) firstTrustOutgoingAttemptExactLocked(
	metadata firstTrustOutgoingAttemptMetadata,
	cancellationGeneration uint64,
) int {
	index := coordinator.firstTrustOutgoingAttemptMetadataLocked(metadata)
	if index < 0 || coordinator.controlView.control.attempts[index].cancellationGeneration != cancellationGeneration {
		return -1
	}
	return index
}

func (coordinator *firstTrustCoordinator) firstTrustOutgoingAttemptRemote(metadata firstTrustOutgoingAttemptMetadata) ([]byte, bool) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	index := coordinator.firstTrustOutgoingAttemptMetadataLocked(metadata)
	if index < 0 {
		return nil, false
	}
	return bytes.Clone(coordinator.controlView.control.attempts[index].remoteSKI), true
}

func (coordinator *firstTrustCoordinator) firstTrustOutgoingAttemptMatchesRemote(
	metadata firstTrustOutgoingAttemptMetadata,
	remote []byte,
) bool {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	index := coordinator.firstTrustOutgoingAttemptMetadataLocked(metadata)
	return index >= 0 && bytes.Equal(coordinator.controlView.control.attempts[index].remoteSKI, remote)
}

func (coordinator *firstTrustCoordinator) cancelOutgoingAttemptRuntime(attemptID [32]byte) {
	coordinator.mu.Lock()
	runtime, ok := coordinator.outgoingAttemptContexts[attemptID]
	if ok {
		delete(coordinator.outgoingAttemptContexts, attemptID)
	}
	coordinator.mu.Unlock()
	if ok && runtime.cancel != nil {
		runtime.cancel()
	}
}

func (coordinator *firstTrustCoordinator) cancelAllOutgoingAttemptContextsLocked() {
	for attemptID, runtime := range coordinator.outgoingAttemptContexts {
		if runtime.cancel != nil {
			runtime.cancel()
		}
		delete(coordinator.outgoingAttemptContexts, attemptID)
	}
	coordinator.outgoingAttemptContexts = make(map[[32]byte]firstTrustOutgoingAttemptRuntime)
}

func (coordinator *firstTrustCoordinator) lockOutgoingAttemptLane(remote []byte) func() {
	digest := sha256.Sum256(remote)
	lane := &coordinator.outgoingAttemptLanes[int(digest[0])%len(coordinator.outgoingAttemptLanes)]
	lane.Lock()
	return lane.Unlock
}

func firstTrustOutgoingAttemptRequestValid(request firstTrustOutgoingAttemptRequest) bool {
	return len(request.remoteSKI) == 20 && request.endpoint.host != "" && len(request.endpoint.host) <= 255 &&
		request.endpoint.port != 0 && len(request.path) <= 1024 && (request.path == "" || strings.HasPrefix(request.path, "/"))
}

func firstTrustNormalizedSKI(remote []byte) string {
	const hexadecimal = "0123456789abcdef"
	result := make([]byte, len(remote)*2)
	for index, value := range remote {
		result[index*2] = hexadecimal[value>>4]
		result[index*2+1] = hexadecimal[value&0x0f]
	}
	return string(result)
}

func (coordinator *firstTrustCoordinator) chargeRestartedOutgoingAttempts(ctx context.Context) string {
	coordinator.mu.Lock()
	if len(coordinator.controlView.control.attempts) == 0 {
		coordinator.mu.Unlock()
		return "not_required"
	}
	if coordinator.recoveryStore == nil || coordinator.anchor == nil || coordinator.anchorRecord.pending != nil ||
		coordinator.controlView.control.controlEpoch == math.MaxUint64 {
		coordinator.mu.Unlock()
		return "failed_closed"
	}
	publicationID, ok := firstTrustReadOrdinal(coordinator.random)
	if !ok {
		coordinator.mu.Unlock()
		return "failed_closed"
	}
	target := cloneFirstTrustControlRecord(coordinator.controlView.control)
	target.controlEpoch++
	attempts := append([]firstTrustOutgoingAttemptRecord(nil), target.attempts...)
	target.attempts = nil
	for _, attempt := range attempts {
		if _, charged := coordinator.firstTrustChargeOutgoingAttemptFailureLocked(&target, attempt); !charged {
			coordinator.mu.Unlock()
			return "failed_closed"
		}
		if coordinator.outgoingAttemptReservationOrder < attempt.reservationOrder {
			coordinator.outgoingAttemptReservationOrder = attempt.reservationOrder
		}
		if coordinator.outgoingAttemptCancellationGeneration < attempt.cancellationGeneration {
			coordinator.outgoingAttemptCancellationGeneration = attempt.cancellationGeneration
		}
	}
	expectedEpoch := coordinator.controlView.control.controlEpoch
	coordinator.mu.Unlock()

	_, outcome := coordinator.publishOutgoingAttemptControl(
		ctx, expectedEpoch, target, publicationID, "attempt_restart_synthetic_failure",
	)
	if outcome != "durable" {
		return "failed_closed"
	}
	return "charged"
}

func (coordinator *firstTrustCoordinator) removeRevokedOutgoingAttemptsLocked(
	target *firstTrustControlRecord,
	remote []byte,
) [][32]byte {
	removed := make([][32]byte, 0, len(target.attempts))
	retained := target.attempts[:0]
	for _, attempt := range target.attempts {
		if bytes.Equal(attempt.remoteSKI, remote) {
			removed = append(removed, attempt.attemptID)
			continue
		}
		retained = append(retained, attempt)
	}
	target.attempts = retained
	return removed
}

func (coordinator *firstTrustCoordinator) cancelRevokedOutgoingAttemptsLocked(attemptIDs [][32]byte) {
	for _, attemptID := range attemptIDs {
		runtime, ok := coordinator.outgoingAttemptContexts[attemptID]
		if !ok {
			continue
		}
		delete(coordinator.outgoingAttemptContexts, attemptID)
		delete(coordinator.retryInflight, runtime.metadata.scope)
		if runtime.cancel != nil {
			runtime.cancel()
		}
	}
}
