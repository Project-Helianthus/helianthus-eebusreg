package eebusfacade

import (
	"context"
)

func (coordinator *firstTrustCoordinator) revoke(ctx context.Context, request firstTrustRevocationRequest) string {
	ctx = firstTrustContext(ctx)
	if ctx.Err() != nil {
		return "request_cancelled"
	}
	binding := firstTrustHashRevocation(request)
	coordinator.mu.Lock()
	if coordinator.recoveryStore == nil || coordinator.anchor == nil {
		coordinator.mu.Unlock()
		return "mutation_disabled"
	}
	if result, handled := coordinator.firstTrustOperationReplayLocked(request.operationID, "revocation", binding); handled {
		coordinator.mu.Unlock()
		return result
	}
	if coordinator.reconciliationRequiredLocked() {
		coordinator.mu.Unlock()
		return "reconciliation_required"
	}
	if request.operationID == [32]byte{} || firstTrustOperationOrdinal(request.operationID) <= coordinator.controlView.control.operationHighWater {
		coordinator.mu.Unlock()
		return "idempotency_expired"
	}
	if !firstTrustManifestRequestMatches(coordinator.controlView, request) {
		coordinator.mu.Unlock()
		return "revocation_conflict"
	}
	associationIndex := -1
	for index, association := range coordinator.controlView.associations {
		if association.reference == request.associationRef && association.lineage == request.associationLineage {
			associationIndex = index
			break
		}
	}
	if associationIndex < 0 {
		coordinator.mu.Unlock()
		return "revocation_conflict"
	}
	if len(coordinator.controlView.control.tombstones) >= firstTrustMaximumTombstones || len(coordinator.controlView.control.receipts) >= firstTrustMaximumDurableReceipts || coordinator.controlView.control.controlEpoch == ^uint64(0) {
		coordinator.mu.Unlock()
		return "tombstone_capacity"
	}
	working := cloneFirstTrustControlView(coordinator.controlView)
	working.associations[associationIndex].active = false
	working.associations[associationIndex].trusted = false
	working.associations[associationIndex].allowlisted = false
	working.associations[associationIndex].reconnectable = false
	target := cloneFirstTrustControlRecord(working.control)
	target.controlEpoch++
	target.operationHighWater = firstTrustOperationOrdinal(request.operationID)
	target.tombstones = append(target.tombstones, firstTrustRevocationTombstone{
		associationRef: request.associationRef, revocationEpoch: target.controlEpoch, operationID: request.operationID,
	})
	if !firstTrustAppendReceipt(&target, firstTrustDurableReceipt{
		operationID: request.operationID, operationClass: "revocation", bindingSHA256: binding, result: "revoked", terminal: true,
	}) {
		coordinator.mu.Unlock()
		return "idempotency_capacity"
	}
	previousRecovery := coordinator.recovery
	delete(coordinator.trustedRemotes, string(working.associations[associationIndex].subject))
	coordinator.closeVolatileFirstTrustLocked()
	coordinator.recoveryOperation = &firstTrustRecoveryOperation{operationID: request.operationID, operationClass: "revocation", bindingSHA256: binding}
	selected := cloneFirstTrustControlView(coordinator.controlView)
	anchor := cloneFirstTrustAnchorRecord(coordinator.anchorRecord)
	coordinator.mu.Unlock()

	target = coordinator.firstTrustBindEffectiveGeneration(ctx, working, target, request.operationID, "revocation")
	publication, publicationOutcome, anchor := coordinator.publishFirstTrustControl(ctx, working, target, request.operationID, "revocation", selected, anchor)

	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	coordinator.anchorRecord = cloneFirstTrustAnchorRecord(anchor)
	coordinator.recoveryOperation = nil
	switch publicationOutcome {
	case "durable":
		coordinator.controlView = cloneFirstTrustControlView(publication.target)
		coordinator.phase = firstTrustPairingClosed
		coordinator.recovery = "REVOKED"
		coordinator.recoveryReasonCode = "REVOKED_ASSOCIATION"
		return "revoked"
	case "unknown":
		coordinator.phase = firstTrustDisabled
		coordinator.recovery = "QUARANTINED"
		coordinator.recoveryReasonCode = "DURABILITY_UNKNOWN"
		coordinator.trustedRemotes = make(map[string]string)
		return "revocation_outcome_unknown"
	default:
		coordinator.phase = firstTrustPairingClosed
		coordinator.recovery = previousRecovery
		if previousRecovery == "PAIRED_TRUSTED" {
			coordinator.recoveryReasonCode = ""
		}
		return "failed_closed_unchanged"
	}
}

func (coordinator *firstTrustCoordinator) repair(ctx context.Context, request firstTrustRepairRequest) string {
	ctx = firstTrustContext(ctx)
	if ctx.Err() != nil {
		return "request_cancelled"
	}
	if !firstTrustRepairKindAllowed(request.kind) {
		return "repair_conflict"
	}
	binding := firstTrustHashRepair(request)
	coordinator.mu.Lock()
	if coordinator.recoveryStore == nil || coordinator.anchor == nil {
		coordinator.mu.Unlock()
		return "mutation_disabled"
	}
	coordinator.closeVolatileFirstTrustLocked()
	if coordinator.reconciliationRequiredLocked() && request.kind != "reconcile_pending_publication" {
		coordinator.mu.Unlock()
		return "reconciliation_required"
	}
	if result, handled := coordinator.firstTrustOperationReplayLocked(request.operationID, request.kind, binding); handled {
		coordinator.mu.Unlock()
		return result
	}
	if request.nextRepairSequence <= coordinator.controlView.control.repairSequence {
		coordinator.mu.Unlock()
		return "idempotency_expired"
	}
	if !coordinator.firstTrustRepairRequestMatchesLocked(request) {
		coordinator.mu.Unlock()
		return "repair_conflict"
	}
	if request.kind == "reconcile_pending_publication" {
		coordinator.recoveryOperation = &firstTrustRecoveryOperation{operationID: request.operationID, operationClass: request.kind, bindingSHA256: binding}
		coordinator.mu.Unlock()
		return coordinator.reconcileFirstTrustPublication(ctx, request, binding)
	}
	if !coordinator.firstTrustRepairApplicableLocked(request) {
		coordinator.mu.Unlock()
		return "repair_conflict"
	}
	working, target, ok := coordinator.prepareFirstTrustRepairLocked(request, binding)
	if !ok {
		coordinator.mu.Unlock()
		return "repair_conflict"
	}
	coordinator.recoveryOperation = &firstTrustRecoveryOperation{operationID: request.operationID, operationClass: request.kind, bindingSHA256: binding}
	selected := cloneFirstTrustControlView(coordinator.controlView)
	anchor := cloneFirstTrustAnchorRecord(coordinator.anchorRecord)
	coordinator.mu.Unlock()

	if request.kind == "recover_unavailable_host_key" {
		if coordinator.identityProvider != nil {
			identity, identityOutcome := coordinator.identityProvider.CreateSigningIdentity(ctx)
			if identityOutcome != "identity_durable" {
				coordinator.mu.Lock()
				coordinator.recoveryOperation = nil
				coordinator.phase = firstTrustDisabled
				coordinator.recovery = "QUARANTINED"
				coordinator.recoveryReasonCode = "DURABILITY_UNKNOWN"
				coordinator.mu.Unlock()
				return "repair_outcome_unknown"
			}
			cloned := cloneFirstTrustLocalIdentityBinding(identity)
			target.replacementIdentity = &cloned
		}
		created, outcome := coordinator.anchor.Create(ctx, firstTrustAnchorVersion, target.storeInstance)
		if outcome != "anchor_durable" {
			coordinator.mu.Lock()
			coordinator.recoveryOperation = nil
			coordinator.phase = firstTrustDisabled
			coordinator.recovery = "QUARANTINED"
			coordinator.recoveryReasonCode = "DURABILITY_UNKNOWN"
			coordinator.mu.Unlock()
			return "repair_outcome_unknown"
		}
		anchor = cloneFirstTrustAnchorRecord(created)
	}
	target = coordinator.firstTrustBindEffectiveGeneration(ctx, working, target, request.operationID, request.kind)
	publication, publicationOutcome, anchor := coordinator.publishFirstTrustControl(ctx, working, target, request.operationID, request.kind, selected, anchor)

	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	coordinator.anchorRecord = cloneFirstTrustAnchorRecord(anchor)
	coordinator.recoveryOperation = nil
	switch publicationOutcome {
	case "durable":
		coordinator.controlView = cloneFirstTrustControlView(publication.target)
		coordinator.phase = firstTrustPairingClosed
		coordinator.recovery = "UNPAIRED_LOCKED"
		coordinator.recoveryReasonCode = ""
		coordinator.trustedRemotes = make(map[string]string)
		return "repaired_unpaired"
	case "unknown":
		coordinator.phase = firstTrustDisabled
		coordinator.recovery = "QUARANTINED"
		coordinator.recoveryReasonCode = "DURABILITY_UNKNOWN"
		coordinator.trustedRemotes = make(map[string]string)
		return "repair_outcome_unknown"
	default:
		return "failed_closed_unchanged"
	}
}

func (coordinator *firstTrustCoordinator) reconcileFirstTrustPublication(ctx context.Context, request firstTrustRepairRequest, binding [32]byte) string {
	coordinator.mu.Lock()
	pending := coordinator.anchorRecord.pending
	anchor := cloneFirstTrustAnchorRecord(coordinator.anchorRecord)
	coordinator.mu.Unlock()
	if pending == nil {
		coordinator.mu.Lock()
		coordinator.recoveryOperation = nil
		coordinator.mu.Unlock()
		return "repair_conflict"
	}
	observation := coordinator.recoveryStore.ObserveControlPublication(ctx, cloneFirstTrustPendingPublication(*pending))
	result := "repair_outcome_unknown"
	resolved := false
	switch observation {
	case "exact_target_selected":
		if coordinator.anchor.CompareAndFinalize(ctx, *pending) == "anchor_durable" {
			result, resolved = "operation_terminal", true
			anchor.manifestGenerationHighWater = pending.targetManifest.current.sequence
			anchor.controlEpochHighWater = pending.targetControlEpoch
			anchor.pending = nil
		}
	case "exact_previous_selected_and_target_absent":
		if coordinator.anchor.CompareAndClear(ctx, *pending) == "anchor_durable" {
			result, resolved = "failed_closed_unchanged", true
			anchor.pending = nil
		}
	}
	if resolved {
		coordinator.mu.Lock()
		working := cloneFirstTrustControlView(coordinator.controlView)
		target := cloneFirstTrustControlRecord(working.control)
		if target.controlEpoch == ^uint64(0) || len(target.receipts) >= firstTrustMaximumDurableReceipts {
			coordinator.mu.Unlock()
			resolved = false
		} else {
			target.controlEpoch++
			target.repairSequence = request.nextRepairSequence
			resolved = firstTrustAppendReceipt(&target, firstTrustDurableReceipt{
				operationID: request.operationID, operationClass: request.kind,
				bindingSHA256: binding, result: result, terminal: true,
			})
			selected := cloneFirstTrustControlView(coordinator.controlView)
			coordinator.mu.Unlock()
			if resolved {
				publication, outcome, publishedAnchor := coordinator.publishFirstTrustControl(
					ctx, working, target, request.operationID, request.kind, selected, anchor,
				)
				anchor = cloneFirstTrustAnchorRecord(publishedAnchor)
				if outcome == "durable" {
					coordinator.mu.Lock()
					coordinator.controlView = cloneFirstTrustControlView(publication.target)
					coordinator.storeGeneration = publication.target.manifest.current.sequence
					coordinator.mu.Unlock()
				} else {
					resolved = false
				}
			}
		}
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	coordinator.recoveryOperation = nil
	coordinator.anchorRecord = cloneFirstTrustAnchorRecord(anchor)
	if resolved {
		coordinator.phase = firstTrustPairingClosed
		coordinator.recovery = "UNPAIRED_LOCKED"
		coordinator.recoveryReasonCode = ""
		return result
	}
	coordinator.phase = firstTrustDisabled
	coordinator.recovery = "QUARANTINED"
	coordinator.recoveryReasonCode = "DURABILITY_UNKNOWN"
	return result
}

func (coordinator *firstTrustCoordinator) firstTrustOperationReplayLocked(operationID [32]byte, operationClass string, binding [32]byte) (string, bool) {
	if receipt, ok := coordinator.durableReceiptLocked(operationID); ok {
		if receipt.operationClass != operationClass || receipt.bindingSHA256 != binding {
			return "idempotency_conflict", true
		}
		return receipt.result, true
	}
	if coordinator.recoveryOperation == nil {
		return "", false
	}
	if coordinator.recoveryOperation.operationID == operationID {
		if coordinator.recoveryOperation.operationClass != operationClass || coordinator.recoveryOperation.bindingSHA256 != binding {
			return "idempotency_conflict", true
		}
		return "operation_in_progress", true
	}
	return "operation_in_progress", true
}

func (coordinator *firstTrustCoordinator) firstTrustRepairRequestMatchesLocked(request firstTrustRepairRequest) bool {
	return request.operationID != [32]byte{} && request.expectedState == coordinator.recovery && request.expectedReason == coordinator.recoveryReasonCode &&
		firstTrustManifestEqual(request.expectedManifest, coordinator.controlView.manifest) && request.expectedControlEpoch == coordinator.controlView.control.controlEpoch &&
		request.expectedAnchorVersion == coordinator.anchorRecord.version && request.expectedManifestHighWater == coordinator.anchorRecord.manifestGenerationHighWater &&
		request.expectedControlHighWater == coordinator.anchorRecord.controlEpochHighWater && request.nextRepairSequence == coordinator.controlView.control.repairSequence+1
}

func (coordinator *firstTrustCoordinator) firstTrustRepairApplicableLocked(request firstTrustRepairRequest) bool {
	switch request.kind {
	case "publish_inactive_parent":
		return coordinator.recoveryReasonCode == "MANIFEST_GENERATION_ROLLBACK" && coordinator.controlView.manifest.parent != nil
	case "adopt_copied_current":
		return coordinator.recoveryReasonCode == "CLONE_DETECTED"
	case "recover_unavailable_host_key":
		return coordinator.recoveryReasonCode == "HOST_KEY_UNAVAILABLE"
	case "release_retry_quarantine":
		_, record, ok := coordinator.firstTrustQuarantineLocked(request.scope)
		return ok && (record.state == "BACKOFF_ACTIVE" || record.state == "ADMIN_HOLD")
	default:
		return false
	}
}

func (coordinator *firstTrustCoordinator) prepareFirstTrustRepairLocked(request firstTrustRepairRequest, binding [32]byte) (firstTrustControlView, firstTrustControlRecord, bool) {
	working := cloneFirstTrustControlView(coordinator.controlView)
	target := cloneFirstTrustControlRecord(working.control)
	if target.controlEpoch == ^uint64(0) || len(target.receipts) >= firstTrustMaximumDurableReceipts {
		return firstTrustControlView{}, firstTrustControlRecord{}, false
	}
	target.controlEpoch++
	target.repairSequence = request.nextRepairSequence
	if request.kind == "release_retry_quarantine" {
		index, record, ok := coordinator.firstTrustQuarantineLocked(request.scope)
		if !ok {
			return firstTrustControlView{}, firstTrustControlRecord{}, false
		}
		record.state = "RETRY_READY"
		record.remainingDelay = 0
		record.lastControlEpoch = target.controlEpoch
		target.quarantines[index] = record
	} else {
		lineage, ok := firstTrustReadOrdinal(coordinator.random)
		if !ok {
			return firstTrustControlView{}, firstTrustControlRecord{}, false
		}
		target.associationLineage = lineage
		if request.kind == "adopt_copied_current" {
			target.storeInstance = coordinator.anchorRecord.storeInstance
		}
		if request.kind == "recover_unavailable_host_key" {
			instance, available := firstTrustReadOrdinal(coordinator.random)
			if !available {
				return firstTrustControlView{}, firstTrustControlRecord{}, false
			}
			target.storeInstance = instance
		}
		source := working.associations
		if request.kind == "publish_inactive_parent" {
			source = working.parentAssociations
		}
		working.associations = cloneFirstTrustAssociations(source)
		inherited := 0
		for index := range working.associations {
			association := &working.associations[index]
			if association.active || association.trusted || association.allowlisted || association.reconnectable {
				inherited++
				association.active, association.trusted, association.allowlisted, association.reconnectable = false, false, false, false
				target.tombstones = append(target.tombstones, firstTrustRevocationTombstone{
					associationRef: association.reference, revocationEpoch: target.controlEpoch, operationID: request.operationID,
				})
			}
			association.lineage = lineage
		}
		if len(coordinator.controlView.control.tombstones)+inherited > firstTrustMaximumTombstones {
			return firstTrustControlView{}, firstTrustControlRecord{}, false
		}
	}
	if !firstTrustAppendReceipt(&target, firstTrustDurableReceipt{
		operationID: request.operationID, operationClass: request.kind, bindingSHA256: binding, result: "repaired_unpaired", terminal: true,
	}) {
		return firstTrustControlView{}, firstTrustControlRecord{}, false
	}
	return working, target, true
}

func (coordinator *firstTrustCoordinator) firstTrustBindEffectiveGeneration(ctx context.Context, working firstTrustControlView, target firstTrustControlRecord, operationID [32]byte, operationClass string) firstTrustControlRecord {
	draft, outcome := coordinator.recoveryStore.PrepareControl(ctx, cloneFirstTrustControlView(working), cloneFirstTrustControlRecord(target), operationID, operationClass)
	if outcome != "prepared" {
		return target
	}
	for index := range target.tombstones {
		if target.tombstones[index].operationID == operationID && target.tombstones[index].effectiveGeneration.sequence == 0 {
			for _, prepared := range draft.target.control.tombstones {
				if prepared.operationID == operationID && prepared.associationRef == target.tombstones[index].associationRef && prepared.effectiveGeneration.sequence != 0 {
					target.tombstones[index].effectiveGeneration = prepared.effectiveGeneration
					break
				}
			}
		}
	}
	return target
}

func firstTrustRepairKindAllowed(kind string) bool {
	switch kind {
	case "reconcile_pending_publication", "publish_inactive_parent", "adopt_copied_current", "recover_unavailable_host_key", "release_retry_quarantine":
		return true
	default:
		return false
	}
}
