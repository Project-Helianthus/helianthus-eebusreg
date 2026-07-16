package eebusfacade

import (
	"bytes"
	"context"
	"encoding/hex"
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

	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	coordinator.reopening = false
	if ctx.Err() != nil {
		return "reopen_cancelled"
	}
	coordinator.controlView = cloneFirstTrustControlView(view)
	coordinator.anchorRecord = cloneFirstTrustAnchorRecord(anchor)
	coordinator.storeGeneration = view.manifest.current.sequence
	coordinator.retryArms = make(map[[32]byte]firstTrustRetryArm)
	coordinator.retryInflight = make(map[[32]byte]bool)
	coordinator.trustedRemotes = make(map[string]string)

	state, reason := coordinator.classifyFirstTrustStartupLocked(storeOutcome, anchorOutcome)
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
	if phase == "PAIRING_CLOSED" {
		return "pairing_closed"
	}
	return storeOutcome
}

func (coordinator *firstTrustCoordinator) classifyFirstTrustStartupLocked(storeOutcome, anchorOutcome string) (string, string) {
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
	if coordinator.anchorRecord.storeInstance != coordinator.controlView.control.storeInstance {
		return "QUARANTINED", "CLONE_DETECTED"
	}
	if coordinator.controlView.manifest.current.sequence < coordinator.anchorRecord.manifestGenerationHighWater {
		return "QUARANTINED", "MANIFEST_GENERATION_ROLLBACK"
	}
	if coordinator.controlView.control.controlEpoch < coordinator.anchorRecord.controlEpochHighWater {
		return "QUARANTINED", "CONTROL_EPOCH_ROLLBACK"
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
		return "PAIRED_TRUSTED", ""
	}
	return "UNPAIRED_LOCKED", ""
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
	case "opened_empty", "opened_current", "opened_migrated", "commit_applied_maintenance_failed", "commit_durability_unknown", "bootstrap_durability_unknown", "key_provider_unavailable", "key_material_unavailable":
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
		coordinator.cancelRemoteLocked(coordinator.currentCandidate.remote, coordinator.currentCandidate.connection)
	}
	coordinator.phase = firstTrustDisabled
	coordinator.window = nil
	coordinator.currentCandidate = nil
	coordinator.inflight = nil
	coordinator.recoveryOperation = nil
	coordinator.replays = make(map[string]firstTrustReplay)
	coordinator.retired = make(map[string]firstTrustRetired)
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
	if coordinator.phase == firstTrustOpenEmpty && coordinator.window != nil {
		return "pairing_authorized"
	}
	if coordinator.currentCandidate != nil && bytes.Equal(coordinator.currentCandidate.remote, remote) &&
		(coordinator.phase == firstTrustCandidatePending || coordinator.phase == firstTrustCommitting) {
		return "pairing_authorized"
	}
	return "attempt_denied"
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
	return coordinator.recovery == "UNPAIRED_LOCKED" || coordinator.recovery == "PAIRED_TRUSTED"
}

func (coordinator *firstTrustCoordinator) registerConfiguredRemote(ski string, register func(string)) string {
	remote, err := hex.DecodeString(ski)
	if err != nil || len(remote) != 20 || register == nil {
		return "registration_denied"
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.phase != firstTrustPairingClosed || coordinator.recovery != "PAIRED_TRUSTED" || coordinator.recoveryOperation != nil || coordinator.reconciliationRequiredLocked() {
		return "registration_denied"
	}
	for _, association := range coordinator.controlView.associations {
		if !bytes.Equal(association.subject, remote) || !firstTrustAssociationUsable(association, coordinator.controlView.control.associationLineage) || coordinator.firstTrustTombstonedLocked(association) {
			continue
		}
		if _, trusted := coordinator.trustedRemotes[string(remote)]; !trusted {
			return "registration_denied"
		}
		register(ski)
		return "registered"
	}
	return "registration_denied"
}

func (coordinator *firstTrustCoordinator) phaseNameLocked() string {
	switch coordinator.phase {
	case firstTrustPairingClosed:
		return "PAIRING_CLOSED"
	case firstTrustOpenEmpty:
		return "OPEN_EMPTY"
	case firstTrustCandidatePending:
		return "CANDIDATE_PENDING"
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
		coordinator.cancelRemoteLocked(coordinator.currentCandidate.remote, coordinator.currentCandidate.connection)
	}
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
	if outcome != "prepared" || !firstTrustPreparedPublicationValid(publication, selected, operationID, operationClass) {
		return publication, "prepare_failed", anchor
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
	coordinator.currentCandidate = nil
	coordinator.window = nil
	coordinator.stopTimerLocked()
	coordinator.setWaitingLocked(false)
	if result == "trusted" {
		if coordinator.effects != nil && coordinator.effects.connectionAlive(remote, connection) {
			coordinator.effects.registerRemoteSKI(remote, connection)
		}
	} else {
		coordinator.cancelRemoteLocked(remote, connection)
	}
	coordinator.recoveryOperation = nil
	coordinator.inflight = nil
	close(inflight.done)
	coordinator.mu.Unlock()
	return result
}
