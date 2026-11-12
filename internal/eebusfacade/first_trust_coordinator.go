package eebusfacade

import (
	"bytes"
	"context"
	"time"
)

func (coordinator *firstTrustCoordinator) reopenWithRecovery(ctx context.Context) string {
	ctx = firstTrustContext(ctx)
	coordinator.mu.Lock()
	if coordinator.reopening {
		coordinator.mu.Unlock()
		return "reopen_in_progress"
	}
	if coordinator.phase != firstTrustDisabled {
		coordinator.mu.Unlock()
		return "reopen_not_required"
	}
	if coordinator.recoveryStore == nil {
		coordinator.mu.Unlock()
		return "store_unavailable"
	}
	coordinator.reopening = true
	coordinator.resetVolatileFirstTrustLocked()
	coordinator.mu.Unlock()

	view, storeOutcome := coordinator.recoveryStore.ReloadControl(ctx)
	var anchor firstTrustAnchorRecord
	anchorOutcome := "anchor_unavailable"
	if firstTrustStructuralStoreOutcome(storeOutcome) == "" && coordinator.anchor != nil {
		anchor, anchorOutcome = coordinator.anchor.Open(ctx)
	}
	anchor = coordinator.clearKnownUnappliedAttemptOnReopen(ctx, view, anchor, anchorOutcome)

	coordinator.mu.Lock()
	if ctx.Err() != nil {
		coordinator.reopening = false
		coordinator.mu.Unlock()
		return "reopen_cancelled"
	}
	coordinator.controlView = cloneFirstTrustControlView(view)
	coordinator.anchorRecord = cloneFirstTrustAnchorRecord(anchor)
	coordinator.storeGeneration = view.manifest.current.sequence
	coordinator.retryArms = make(map[[32]byte]firstTrustRetryArm)
	coordinator.retryInflight = make(map[[32]byte]bool)
	coordinator.trustedRemotes = make(map[string]string)
	coordinator.cancelAllOutgoingAttemptContextsLocked()
	_, _, retryReadyRecordAtReload := coordinator.persistedRetryReadyRecordAssociationLocked()
	coordinator.reconcileTrustedRetryQuarantinesLocked(ctx, storeOutcome, anchorOutcome)
	prechargeState, prechargeReason := coordinator.classifyFirstTrustStartupLocked(storeOutcome, anchorOutcome, retryReadyRecordAtReload)
	chargeAllowed := prechargeState == "PAIRED_TRUSTED" || prechargeState == "UNPAIRED_LOCKED" ||
		prechargeState == "QUARANTINED" && prechargeReason != "DURABILITY_UNKNOWN" && prechargeReason != "HOST_BINDING_MISMATCH" &&
			prechargeReason != "CLONE_DETECTED" && prechargeReason != "MANIFEST_GENERATION_ROLLBACK" && prechargeReason != "CONTROL_EPOCH_ROLLBACK"
	hasUnresolvedAttempts := len(coordinator.controlView.control.attempts) != 0
	coordinator.mu.Unlock()

	chargeOutcome := "not_required"
	if hasUnresolvedAttempts && chargeAllowed {
		chargeOutcome = coordinator.chargeRestartedOutgoingAttempts(ctx)
	}

	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	coordinator.reopening = false
	if hasUnresolvedAttempts && (!chargeAllowed || chargeOutcome != "charged") {
		coordinator.phase = firstTrustDisabled
		coordinator.recovery = "QUARANTINED"
		coordinator.recoveryReasonCode = "DURABILITY_UNKNOWN"
		return storeOutcome
	}

	state, reason := coordinator.classifyFirstTrustStartupLocked(storeOutcome, anchorOutcome, retryReadyRecordAtReload)
	coordinator.recovery, coordinator.recoveryReasonCode = state, reason
	if state == "UNPAIRED_LOCKED" || state == "PAIRED_TRUSTED" || state == "REVOKED" {
		coordinator.phase = firstTrustPairingClosed
	} else {
		coordinator.phase = firstTrustDisabled
	}
	coordinator.loadFirstTrustRetryArmsLocked()
	if state == "PAIRED_TRUSTED" {
		coordinator.loadFirstTrustAssociationsLocked()
	}
	phase, recovery := normalizeFirstTrustProduct(coordinator.phaseNameLocked(), coordinator.recovery, map[bool]string{true: "CORRUPT_STORE"}[state == "CORRUPT_STORE"])
	coordinator.phase = firstTrustPhaseFromName(phase)
	coordinator.recovery = recovery
	if chargeOutcome == "charged" {
		return "pairing_closed"
	}
	if phase == "PAIRING_CLOSED" {
		return "pairing_closed"
	}
	return storeOutcome
}

func (coordinator *firstTrustCoordinator) reconcileTrustedRetryQuarantinesLocked(
	ctx context.Context,
	storeOutcome, anchorOutcome string,
) {
	if firstTrustStructuralStoreOutcome(storeOutcome) != "" || firstTrustDurabilityUnknownOutcome(storeOutcome) ||
		anchorOutcome != "opened_anchor" || coordinator.anchorRecord.pending != nil ||
		coordinator.firstTrustAnchorProductReasonLocked() != "" || len(coordinator.controlView.control.attempts) != 0 ||
		coordinator.controlView.control.controlEpoch == ^uint64(0) {
		return
	}

	trustedScopes := make(map[[32]byte]struct{})
	for _, association := range coordinator.controlView.associations {
		if !firstTrustAssociationUsable(association, coordinator.controlView.control.associationLineage) ||
			coordinator.firstTrustTombstonedLocked(association) || len(association.subject) != 20 || association.service == "" {
			continue
		}
		scope := firstTrustRuntimeRetryScope(firstTrustNormalizedSKI(association.subject))
		trustedScopes[scope] = struct{}{}
	}
	if len(trustedScopes) == 0 {
		return
	}
	for _, quarantine := range coordinator.controlView.control.quarantines {
		if !firstTrustQuarantineRecordValid(quarantine, coordinator.backoffPolicy) {
			return
		}
	}

	target := cloneFirstTrustControlRecord(coordinator.controlView.control)
	target.controlEpoch++
	reset := false
	for _, quarantine := range target.quarantines {
		if _, trusted := trustedScopes[quarantine.scope]; !trusted ||
			quarantine.state != "ADMIN_HOLD" && quarantine.state != "BACKOFF_ACTIVE" {
			continue
		}
		coordinator.firstTrustResetOutgoingAttemptRetryLocked(&target, quarantine.scope)
		reset = true
	}
	if !reset {
		return
	}
	operationID, ok := firstTrustReadOrdinal(coordinator.random)
	if !ok {
		return
	}

	working := cloneFirstTrustControlView(coordinator.controlView)
	selected := cloneFirstTrustControlView(coordinator.controlView)
	anchor := cloneFirstTrustAnchorRecord(coordinator.anchorRecord)
	coordinator.recoveryOperation = &firstTrustRecoveryOperation{
		operationID: operationID, operationClass: "release_retry_quarantine",
	}
	coordinator.mu.Unlock()
	publication, outcome, anchor := coordinator.publishFirstTrustControl(
		ctx, working, target, operationID, "release_retry_quarantine", selected, anchor,
	)
	coordinator.mu.Lock()
	coordinator.anchorRecord = cloneFirstTrustAnchorRecord(anchor)
	if outcome == "durable" {
		coordinator.controlView = cloneFirstTrustControlView(publication.target)
		coordinator.storeGeneration = publication.target.manifest.current.sequence
	}
	coordinator.recoveryOperation = nil
}

func (coordinator *firstTrustCoordinator) classifyFirstTrustStartupLocked(
	storeOutcome, anchorOutcome string,
	retryReadyRecordAtReload bool,
) (string, string) {
	if reason := firstTrustStructuralStoreOutcome(storeOutcome); reason != "" {
		return "CORRUPT_STORE", reason
	}
	if firstTrustDurabilityUnknownOutcome(storeOutcome) || coordinator.anchorRecord.pending != nil {
		return "QUARANTINED", "DURABILITY_UNKNOWN"
	}
	if storeOutcome == "key_provider_unavailable" || storeOutcome == "key_material_unavailable" {
		return "NO_LOCAL_IDENTITY", "HOST_KEY_UNAVAILABLE"
	}
	if anchorOutcome == "anchor_unavailable" && !coordinator.firstTrustRecoveredAnchorLocked() || coordinator.controlView.control.controlEpoch == 0 {
		return "NO_LOCAL_IDENTITY", "HOST_KEY_UNAVAILABLE"
	}
	if anchorOutcome == "host_binding_mismatch" {
		return "QUARANTINED", "HOST_BINDING_MISMATCH"
	}
	if anchorOutcome != "opened_anchor" && !coordinator.firstTrustRecoveredAnchorLocked() {
		return "QUARANTINED", "DURABILITY_UNKNOWN"
	}
	if reason := coordinator.firstTrustAnchorProductReasonLocked(); reason != "" {
		return "QUARANTINED", reason
	}
	if coordinator.firstTrustInheritedRepairTerminalLocked() {
		return "UNPAIRED_LOCKED", ""
	}
	trustedCurrentLineage := false
	for _, association := range coordinator.controlView.associations {
		if !firstTrustAssociationUsable(association, coordinator.controlView.control.associationLineage) {
			continue
		}
		if coordinator.firstTrustTombstonedLocked(association) {
			return "REVOKED", "REVOKED_ASSOCIATION"
		}
		trustedCurrentLineage = true
	}
	if !trustedCurrentLineage && len(coordinator.controlView.control.tombstones) != 0 {
		return "REVOKED", "REVOKED_ASSOCIATION"
	}
	for _, quarantine := range coordinator.controlView.control.quarantines {
		if !firstTrustQuarantineRecordValid(quarantine, coordinator.backoffPolicy) {
			return "QUARANTINED", "ADMIN_HOLD"
		}
		if quarantine.state == "ADMIN_HOLD" || quarantine.state == "BACKOFF_ACTIVE" {
			return "QUARANTINED", quarantine.reason
		}
	}
	if trustedCurrentLineage {
		if retryReadyRecordAtReload {
			repairReceiptCount := coordinator.persistedRepairReceiptCountLocked()
			if uint64(repairReceiptCount) != coordinator.controlView.control.repairSequence {
				return "QUARANTINED", "DURABILITY_UNKNOWN"
			}
			if releasePresent, releaseExact := coordinator.persistedRetryReadyReleaseReceiptLocked(); releasePresent {
				if releaseExact {
					return "QUARANTINED", "RETRYABLE_FAILURE"
				}
				return "QUARANTINED", "DURABILITY_UNKNOWN"
			}
		}
		return "PAIRED_TRUSTED", ""
	}
	return "UNPAIRED_LOCKED", ""
}

func (coordinator *firstTrustCoordinator) clearKnownUnappliedAttemptOnReopen(
	ctx context.Context,
	view firstTrustControlView,
	anchor firstTrustAnchorRecord,
	anchorOutcome string,
) firstTrustAnchorRecord {
	if ctx.Err() != nil || coordinator.recoveryStore == nil || coordinator.anchor == nil ||
		anchorOutcome != "opened_anchor" || anchor.pending == nil {
		return anchor
	}
	pending := cloneFirstTrustPendingPublication(*anchor.pending)
	if pending.operationClass != "attempt_prepare" ||
		pending.storeInstance != view.control.storeInstance ||
		pending.previousControlEpoch != view.control.controlEpoch ||
		!firstTrustManifestEqual(pending.previousManifest, view.manifest) {
		return anchor
	}
	if coordinator.recoveryStore.ObserveControlPublication(ctx, pending) != "exact_previous_selected_and_target_absent" ||
		coordinator.anchor.CompareAndClear(ctx, pending) != "anchor_durable" {
		return anchor
	}
	anchor.pending = nil
	return anchor
}

func (coordinator *firstTrustCoordinator) firstTrustAnchorProductReasonLocked() string {
	anchor := coordinator.anchorRecord
	control := coordinator.controlView.control
	if anchor.pending != nil || anchor.version != firstTrustAnchorVersion || anchor.anchorIdentity == [32]byte{} ||
		anchor.storeInstance == [32]byte{} || control.storeInstance == [32]byte{} {
		return "DURABILITY_UNKNOWN"
	}
	if anchor.storeInstance != control.storeInstance {
		return "CLONE_DETECTED"
	}
	if coordinator.controlView.manifest.current.sequence < anchor.manifestGenerationHighWater {
		return "MANIFEST_GENERATION_ROLLBACK"
	}
	if coordinator.controlView.manifest.current.sequence != anchor.manifestGenerationHighWater {
		return "DURABILITY_UNKNOWN"
	}
	if control.controlEpoch < anchor.controlEpochHighWater {
		return "CONTROL_EPOCH_ROLLBACK"
	}
	if control.controlEpoch != anchor.controlEpochHighWater {
		return "DURABILITY_UNKNOWN"
	}
	return ""
}

func firstTrustQuarantineRecordValid(record firstTrustQuarantineRecord, policy firstTrustBackoffPolicy) bool {
	if policy.base <= 0 || policy.maximum < policy.base || policy.exponentCap < 0 || policy.attemptMaximum == 0 || record.scope == [32]byte{} || record.attemptCount > policy.attemptMaximum || record.backoffStep > uint64(policy.exponentCap) || record.remainingDelay < 0 || record.remainingDelay > policy.maximum || record.retentionBudget < 0 {
		return false
	}
	switch record.state {
	case "BACKOFF_ACTIVE":
		return record.remainingDelay > 0
	case "RETRY_READY":
		return record.remainingDelay == 0
	case "ADMIN_HOLD":
		return true
	default:
		return false
	}
}

func firstTrustStructuralStoreOutcome(outcome string) string {
	switch outcome {
	case "opened_empty", "opened_current", "commit_applied_maintenance_failed", "commit_durability_unknown", "bootstrap_durability_unknown", "key_provider_unavailable", "key_material_unavailable":
		return ""
	default:
		return "CORRUPT_STORE"
	}
}

func firstTrustDurabilityUnknownOutcome(outcome string) bool {
	return outcome == "commit_applied_maintenance_failed" || outcome == "commit_durability_unknown" || outcome == "bootstrap_durability_unknown"
}

func (coordinator *firstTrustCoordinator) firstTrustRecoveredAnchorLocked() bool {
	if coordinator.anchorRecord.version == 0 || coordinator.anchorRecord.storeInstance != coordinator.controlView.control.storeInstance {
		return false
	}
	for _, receipt := range coordinator.controlView.control.receipts {
		if receipt.terminal && receipt.operationClass == "recover_unavailable_host_key" && receipt.result == "repaired_unpaired" {
			return true
		}
	}
	return false
}

func (coordinator *firstTrustCoordinator) firstTrustInheritedRepairTerminalLocked() bool {
	terminal := false
	for _, receipt := range coordinator.controlView.control.receipts {
		if receipt.operationClass == "revocation" {
			terminal = false
			continue
		}
		if !receipt.terminal || receipt.result != "repaired_unpaired" {
			continue
		}
		switch receipt.operationClass {
		case "publish_inactive_parent", "adopt_copied_current", "recover_unavailable_host_key":
			terminal = true
		}
	}
	if !terminal {
		return false
	}
	for _, association := range coordinator.controlView.associations {
		if association.active || association.trusted || association.allowlisted || association.reconnectable {
			return false
		}
	}
	return true
}

func (coordinator *firstTrustCoordinator) resetVolatileFirstTrustLocked() {
	if coordinator.currentCandidate != nil {
		coordinator.revokeTransientTrustLocked(coordinator.currentCandidate)
		coordinator.cancelRemoteLocked(coordinator.currentCandidate.remote, coordinator.currentCandidate.connection)
	}
	coordinator.phase = firstTrustDisabled
	coordinator.window = nil
	coordinator.currentCandidate = nil
	coordinator.inflight = nil
	coordinator.recoveryOperation = nil
	coordinator.replays = make(map[string]firstTrustReplay)
	coordinator.retired = make(map[string]firstTrustRetired)
	coordinator.resetCandidateDiscoveryLocked("empty")
	coordinator.stopTimerLocked()
	coordinator.stopRetentionTimerLocked()
	coordinator.setWaitingLocked(false)
}

func (coordinator *firstTrustCoordinator) loadFirstTrustAssociationsLocked() {
	for _, association := range coordinator.controlView.associations {
		if !firstTrustAssociationUsable(association, coordinator.controlView.control.associationLineage) || coordinator.firstTrustTombstonedLocked(association) || len(association.subject) != 20 || association.service == "" {
			continue
		}
		coordinator.trustedRemotes[string(association.subject)] = association.service
	}
}

func (coordinator *firstTrustCoordinator) loadFirstTrustRetryArmsLocked() {
	if coordinator.monotonicNow == nil {
		return
	}
	now := coordinator.monotonicNow()
	for _, record := range coordinator.controlView.control.quarantines {
		if record.state != "BACKOFF_ACTIVE" || record.remainingDelay < 0 {
			continue
		}
		coordinator.retryArms[record.scope] = firstTrustRetryArm{armedAt: now, deadline: firstTrustSaturatingDurationAdd(now, record.remainingDelay)}
	}
}

func firstTrustAssociationUsable(association firstTrustAssociationRecord, lineage [32]byte) bool {
	return association.lineage == lineage && association.active && association.trusted && association.allowlisted && association.reconnectable
}

func (coordinator *firstTrustCoordinator) firstTrustTombstonedLocked(association firstTrustAssociationRecord) bool {
	if association.lineage != coordinator.controlView.control.associationLineage {
		return false
	}
	for _, tombstone := range coordinator.controlView.control.tombstones {
		if tombstone.associationRef == association.reference {
			return true
		}
	}
	return false
}

func (coordinator *firstTrustCoordinator) recoveryState() string {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return coordinator.recovery
}

func (coordinator *firstTrustCoordinator) recoveryReason() string {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return coordinator.recoveryReasonCode
}

func (coordinator *firstTrustCoordinator) authorizeRuntimeAttempt(remote []byte) string {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	coordinator.expireLocked(coordinator.now())
	if len(remote) != 20 || coordinator.reopening || coordinator.recoveryOperation != nil {
		return "attempt_denied"
	}
	if coordinator.recoveryStore != nil {
		if coordinator.reconciliationRequiredLocked() || coordinator.recovery != "REVOKED" && firstTrustSubjectTombstoned(coordinator.controlView, remote) {
			return "attempt_denied"
		}
		if coordinator.recovery == "QUARANTINED" {
			association, scope, ok := coordinator.retryReadyRecoveryAssociationLocked()
			attempt := coordinator.firstTrustOutgoingAttemptForScopeLocked(scope)
			if !ok || !coordinator.retryInflight[scope] || !bytes.Equal(association.subject, remote) || attempt < 0 {
				return "attempt_denied"
			}
			record := coordinator.controlView.control.attempts[attempt]
			if record.state != firstTrustAttemptLaunchAuthorized || !bytes.Equal(record.remoteSKI, remote) {
				return "attempt_denied"
			}
			return "reconnect_authorized"
		}
		if coordinator.recovery != "UNPAIRED_LOCKED" && coordinator.recovery != "PAIRED_TRUSTED" && coordinator.recovery != "REVOKED" {
			return "attempt_denied"
		}
	}
	if _, trusted := coordinator.trustedRemotes[string(remote)]; trusted {
		if coordinator.recoveryStore == nil || coordinator.recovery == "PAIRED_TRUSTED" {
			return "reconnect_authorized"
		}
		return "attempt_denied"
	}
	if coordinator.candidateSelectionRequired {
		if coordinator.selectedCandidateMatchesLocked(remote) {
			return "pairing_authorized"
		}
		return "attempt_denied"
	}
	if coordinator.phase == firstTrustOpenEmpty && coordinator.window != nil {
		return "pairing_authorized"
	}
	if coordinator.currentCandidate != nil && bytes.Equal(coordinator.currentCandidate.remote, remote) &&
		(coordinator.phase == firstTrustCandidatePending || coordinator.phase == firstTrustTransientTrusted ||
			coordinator.phase == firstTrustCommitting) {
		return "pairing_authorized"
	}
	return "attempt_denied"
}

func (coordinator *firstTrustCoordinator) outgoingAttemptOwnsRetry(remote []byte) bool {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return coordinator.candidateSelectionRequired && coordinator.selectedCandidateMatchesLocked(remote)
}

func (coordinator *firstTrustCoordinator) runtimeStartAuthorized() bool {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.recoveryStore == nil {
		return coordinator.phase != firstTrustDisabled && !coordinator.reopening
	}
	if coordinator.reopening || coordinator.recoveryOperation != nil || coordinator.reconciliationRequiredLocked() {
		return false
	}
	if coordinator.recovery == "UNPAIRED_LOCKED" || coordinator.recovery == "PAIRED_TRUSTED" {
		return true
	}
	_, _, ok := coordinator.retryReadyRecoveryAssociationLocked()
	return ok
}

func (coordinator *firstTrustCoordinator) retryReadyRecoveryAssociationLocked() (firstTrustAssociationRecord, [32]byte, bool) {
	if coordinator.phase != firstTrustDisabled || coordinator.recovery != "QUARANTINED" ||
		coordinator.recoveryReasonCode != "RETRYABLE_FAILURE" {
		return firstTrustAssociationRecord{}, [32]byte{}, false
	}
	return coordinator.persistedRetryReadyAssociationLocked()
}

func (coordinator *firstTrustCoordinator) persistedRetryReadyAssociationLocked() (firstTrustAssociationRecord, [32]byte, bool) {
	association, scope, ok := coordinator.persistedRetryReadyRecordAssociationLocked()
	_, releaseExact := coordinator.persistedRetryReadyReleaseReceiptLocked()
	if !ok || uint64(coordinator.persistedRepairReceiptCountLocked()) != coordinator.controlView.control.repairSequence ||
		!releaseExact {
		return firstTrustAssociationRecord{}, [32]byte{}, false
	}
	return association, scope, true
}

func (coordinator *firstTrustCoordinator) persistedRetryReadyRecordAssociationLocked() (firstTrustAssociationRecord, [32]byte, bool) {
	if len(coordinator.controlView.control.quarantines) != 1 {
		return firstTrustAssociationRecord{}, [32]byte{}, false
	}
	retry := coordinator.controlView.control.quarantines[0]
	if retry.state != "RETRY_READY" || retry.reason != "RETRYABLE_FAILURE" || retry.remainingDelay != 0 ||
		!firstTrustQuarantineRecordValid(retry, coordinator.backoffPolicy) {
		return firstTrustAssociationRecord{}, [32]byte{}, false
	}

	usable := 0
	matched := 0
	var result firstTrustAssociationRecord
	for _, association := range coordinator.controlView.associations {
		if !firstTrustAssociationUsable(association, coordinator.controlView.control.associationLineage) {
			continue
		}
		usable++
		if coordinator.firstTrustTombstonedLocked(association) || len(association.subject) != 20 || association.service == "" {
			return firstTrustAssociationRecord{}, [32]byte{}, false
		}
		if retry.scope == firstTrustRuntimeRetryScope(firstTrustNormalizedSKI(association.subject)) {
			matched++
			result = association
		}
	}
	if usable != 1 || matched != 1 {
		return firstTrustAssociationRecord{}, [32]byte{}, false
	}
	return result, retry.scope, true
}

func (coordinator *firstTrustCoordinator) persistedRepairReceiptCountLocked() int {
	count := 0
	for _, receipt := range coordinator.controlView.control.receipts {
		if firstTrustRepairKindAllowed(receipt.operationClass) {
			count++
		}
	}
	return count
}

func (coordinator *firstTrustCoordinator) persistedRetryReadyReleaseReceiptLocked() (bool, bool) {
	found := false
	for _, receipt := range coordinator.controlView.control.receipts {
		if receipt.operationClass != "release_retry_quarantine" {
			continue
		}
		if found || !receipt.terminal || receipt.operationID == [32]byte{} || receipt.bindingSHA256 == [32]byte{} ||
			receipt.result != "repaired_unpaired" {
			return true, false
		}
		found = true
	}
	return found, found
}

func (coordinator *firstTrustCoordinator) phaseNameLocked() string {
	switch coordinator.phase {
	case firstTrustPairingClosed:
		return "PAIRING_CLOSED"
	case firstTrustOpenEmpty:
		return "OPEN_EMPTY"
	case firstTrustCandidatePending:
		return "CANDIDATE_PENDING"
	case firstTrustTransientTrusted:
		return "TRANSIENT_TRUSTED"
	case firstTrustCommitting:
		return "COMMITTING"
	default:
		return "DISABLED"
	}
}

func firstTrustPhaseFromName(value string) firstTrustPhase {
	switch value {
	case "PAIRING_CLOSED":
		return firstTrustPairingClosed
	case "OPEN_EMPTY":
		return firstTrustOpenEmpty
	case "CANDIDATE_PENDING":
		return firstTrustCandidatePending
	case "TRANSIENT_TRUSTED":
		return firstTrustTransientTrusted
	case "COMMITTING":
		return firstTrustCommitting
	default:
		return firstTrustDisabled
	}
}

func (coordinator *firstTrustCoordinator) reconciliationRequiredLocked() bool {
	return coordinator.anchorRecord.pending != nil
}

func (coordinator *firstTrustCoordinator) closeVolatileFirstTrustLocked() {
	now := coordinator.now()
	if coordinator.currentCandidate != nil {
		coordinator.finishCandidateRequestsLocked("stale_request", now)
		coordinator.revokeTransientTrustLocked(coordinator.currentCandidate)
		coordinator.cancelRemoteLocked(coordinator.currentCandidate.remote, coordinator.currentCandidate.connection)
	}
	coordinator.finishCandidateSelectionLocked("stale_request", true)
	coordinator.window = nil
	coordinator.currentCandidate = nil
	coordinator.stopTimerLocked()
	coordinator.setWaitingLocked(false)
	if coordinator.recovery == "UNPAIRED_LOCKED" || coordinator.recovery == "PAIRED_TRUSTED" || coordinator.recovery == "REVOKED" {
		coordinator.phase = firstTrustPairingClosed
	} else {
		coordinator.phase = firstTrustDisabled
	}
}

func (coordinator *firstTrustCoordinator) publishFirstTrustControl(
	ctx context.Context,
	working firstTrustControlView,
	target firstTrustControlRecord,
	operationID [32]byte,
	operationClass string,
	selected firstTrustControlView,
	anchor firstTrustAnchorRecord,
) (firstTrustPreparedPublication, string, firstTrustAnchorRecord) {
	publication, outcome := coordinator.recoveryStore.PrepareControl(ctx, cloneFirstTrustControlView(working), cloneFirstTrustControlRecord(target), operationID, operationClass)
	switch outcome {
	case "prepared":
	case "validation_failed", "commit_not_published":
		return publication, "prepare_failed", anchor
	default:
		return publication, "unknown", anchor
	}
	if !firstTrustPreparedPublicationValid(publication, selected, operationID, operationClass) {
		return publication, "unknown", anchor
	}
	pending := firstTrustPendingFromPrepared(publication)
	expectedAnchor := cloneFirstTrustAnchorRecord(anchor)
	stageOutcome := coordinator.anchor.CompareAndStage(ctx, expectedAnchor, pending)
	if stageOutcome == "anchor_not_published" {
		return publication, "unchanged", anchor
	}
	if stageOutcome != "anchor_durable" {
		anchor.pending = firstTrustPendingPointer(pending)
		return publication, "unknown", anchor
	}
	anchor.pending = firstTrustPendingPointer(pending)
	commitContext, cancelCommit := context.WithTimeout(ctx, coordinator.commitWait)
	defer cancelCommit()
	commitResult := make(chan string, 1)
	go func() {
		commitResult <- coordinator.recoveryStore.CommitControl(commitContext, publication)
	}()
	var storeOutcome string
	select {
	case storeOutcome = <-commitResult:
	case <-commitContext.Done():
		return publication, "unknown", anchor
	}
	switch storeOutcome {
	case "commit_durable":
		if coordinator.anchor.CompareAndFinalize(ctx, pending) != "anchor_durable" {
			return publication, "unknown", anchor
		}
		anchor.manifestGenerationHighWater = pending.targetManifest.current.sequence
		anchor.controlEpochHighWater = pending.targetControlEpoch
		anchor.pending = nil
		return publication, "durable", anchor
	case "commit_not_published":
		if coordinator.anchor.CompareAndClear(ctx, pending) != "anchor_durable" {
			return publication, "unknown", anchor
		}
		anchor.pending = nil
		return publication, "unchanged", anchor
	default:
		return publication, "unknown", anchor
	}
}

func firstTrustPreparedPublicationValid(publication firstTrustPreparedPublication, selected firstTrustControlView, operationID [32]byte, operationClass string) bool {
	return publication.operationID == operationID && publication.operationClass == operationClass &&
		firstTrustManifestEqual(publication.previous.manifest, selected.manifest) &&
		publication.previous.control.controlEpoch == selected.control.controlEpoch &&
		publication.previous.control.storeInstance == selected.control.storeInstance &&
		publication.target.control.controlEpoch == publication.previous.control.controlEpoch+1 &&
		publication.target.manifest.epoch == publication.previous.manifest.epoch+1 &&
		publication.target.manifest.current.sequence != publication.previous.manifest.current.sequence
}

func firstTrustPendingPointer(value firstTrustPendingPublication) *firstTrustPendingPublication {
	cloned := cloneFirstTrustPendingPublication(value)
	return &cloned
}

func (coordinator *firstTrustCoordinator) enterFirstTrustQuarantineLocked(pending *firstTrustPendingPublication) {
	coordinator.phase = firstTrustDisabled
	coordinator.recovery = "QUARANTINED"
	coordinator.recoveryReasonCode = "DURABILITY_UNKNOWN"
	coordinator.trustedRemotes = make(map[string]string)
	if pending != nil {
		coordinator.anchorRecord.pending = firstTrustPendingPointer(*pending)
	}
	coordinator.closeVolatileFirstTrustLocked()
}

func (coordinator *firstTrustCoordinator) durableReceiptLocked(operationID [32]byte) (firstTrustDurableReceipt, bool) {
	for _, receipt := range coordinator.controlView.control.receipts {
		if receipt.operationID == operationID {
			return receipt, true
		}
	}
	return firstTrustDurableReceipt{}, false
}

func firstTrustAppendReceipt(control *firstTrustControlRecord, receipt firstTrustDurableReceipt) bool {
	if len(control.receipts) >= firstTrustMaximumDurableReceipts {
		return false
	}
	control.receipts = append(control.receipts, receipt)
	return true
}

func firstTrustManifestRequestMatches(view firstTrustControlView, request firstTrustRevocationRequest) bool {
	return request.associationLineage == view.control.associationLineage && request.expectedGeneration == view.manifest.current &&
		request.expectedManifestEpoch == view.manifest.epoch && request.expectedManifestSHA256 == view.manifest.sha256 &&
		request.expectedControlEpoch == view.control.controlEpoch
}

func firstTrustSaturatingDurationAdd(left, right time.Duration) time.Duration {
	if right > 0 && left > time.Duration(1<<63-1)-right {
		return time.Duration(1<<63 - 1)
	}
	return left + right
}

func firstTrustSubjectTombstoned(view firstTrustControlView, subject []byte) bool {
	for _, association := range view.associations {
		if !bytes.Equal(association.subject, subject) {
			continue
		}
		for _, tombstone := range view.control.tombstones {
			if tombstone.associationRef == association.reference {
				return true
			}
		}
	}
	return false
}

func (coordinator *firstTrustCoordinator) confirmWithRecoveryLocked(
	ctx context.Context,
	token uint64,
	inflight *firstTrustInflight,
	remote []byte,
	shipID string,
	connection uint64,
) string {
	operationID, ok := firstTrustReadOrdinal(coordinator.random)
	if !ok || coordinator.controlView.control.controlEpoch == ^uint64(0) {
		return coordinator.finishRecoveryConfirmationLocked(token, inflight, remote, connection, coordinator.recovery, "prepare_failed")
	}
	working := cloneFirstTrustControlView(coordinator.controlView)
	target := cloneFirstTrustControlRecord(working.control)
	previousRecovery := coordinator.recovery
	if previousRecovery == "REVOKED" {
		lineage, available := firstTrustReadOrdinal(coordinator.random)
		if !available {
			return coordinator.finishRecoveryConfirmationLocked(token, inflight, remote, connection, previousRecovery, "prepare_failed")
		}
		target.associationLineage = lineage
	}
	reference, available := firstTrustReadOrdinal(coordinator.random)
	if !available {
		return coordinator.finishRecoveryConfirmationLocked(token, inflight, remote, connection, previousRecovery, "prepare_failed")
	}
	target.controlEpoch++
	coordinator.firstTrustResetOutgoingAttemptRetryLocked(
		&target,
		firstTrustRuntimeRetryScope(firstTrustNormalizedSKI(remote)),
	)
	working.associations = append(working.associations, firstTrustAssociationRecord{
		reference: reference, lineage: target.associationLineage, subject: bytes.Clone(remote), service: shipID,
		active: true, trusted: true, allowlisted: true, reconnectable: true,
	})
	coordinator.recoveryOperation = &firstTrustRecoveryOperation{operationID: operationID, operationClass: "first_trust"}
	selected := cloneFirstTrustControlView(coordinator.controlView)
	anchor := cloneFirstTrustAnchorRecord(coordinator.anchorRecord)
	coordinator.mu.Unlock()

	publication, publicationOutcome, anchor := coordinator.publishFirstTrustControl(ctx, working, target, operationID, "first_trust", selected, anchor)

	coordinator.mu.Lock()
	coordinator.anchorRecord = cloneFirstTrustAnchorRecord(anchor)
	if publicationOutcome == "durable" {
		coordinator.controlView = cloneFirstTrustControlView(publication.target)
		coordinator.storeGeneration = publication.target.manifest.current.sequence
	}
	return coordinator.finishRecoveryConfirmationLocked(token, inflight, remote, connection, previousRecovery, publicationOutcome)
}

func (coordinator *firstTrustCoordinator) finishRecoveryConfirmationLocked(
	token uint64,
	inflight *firstTrustInflight,
	remote []byte,
	connection uint64,
	previousRecovery string,
	publicationOutcome string,
) string {
	if coordinator.commitToken != token || coordinator.inflight != inflight {
		coordinator.mu.Unlock()
		return "trust_outcome_unknown"
	}
	result := "failed_closed_unchanged"
	switch publicationOutcome {
	case "durable":
		result = "trusted"
		coordinator.phase = firstTrustPairingClosed
		coordinator.recovery = "PAIRED_TRUSTED"
		coordinator.recoveryReasonCode = ""
		if coordinator.currentCandidate != nil {
			coordinator.trustedRemotes[string(remote)] = coordinator.currentCandidate.shipID
		}
	case "unknown":
		result = "trust_outcome_unknown"
		coordinator.phase = firstTrustDisabled
		coordinator.recovery = "QUARANTINED"
		coordinator.recoveryReasonCode = "DURABILITY_UNKNOWN"
		coordinator.trustedRemotes = make(map[string]string)
	default:
		coordinator.phase = firstTrustPairingClosed
		coordinator.recovery = previousRecovery
		if previousRecovery != "REVOKED" {
			coordinator.recoveryReasonCode = ""
		}
	}
	now := coordinator.now()
	coordinator.finishCandidateRequestsExceptLocked(inflight.key, "stale_request", now)
	coordinator.recordReplayLocked(inflight.key, inflight.request, result, now)
	transient := coordinator.currentCandidate != nil && coordinator.currentCandidate.transientAuthorized
	if transient && result != "trusted" {
		coordinator.revokeTransientTrustLocked(coordinator.currentCandidate)
	}
	coordinator.currentCandidate = nil
	coordinator.finishCandidateSelectionLocked(result, result != "trusted")
	coordinator.window = nil
	coordinator.stopTimerLocked()
	coordinator.setWaitingLocked(false)
	if result == "trusted" && !transient {
		coordinator.registerRemoteLocked(remote, connection)
	} else if result == "trusted" {
		coordinator.finalizeTransientRemoteLocked(remote, connection)
	} else {
		coordinator.cancelRemoteLocked(remote, connection)
	}
	coordinator.recoveryOperation = nil
	coordinator.inflight = nil
	close(inflight.done)
	coordinator.mu.Unlock()
	coordinator.notifyTrustAdminProjection()
	return result
}
