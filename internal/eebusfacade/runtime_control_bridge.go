package eebusfacade

import (
	"context"
	"sync"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusstore"
)

type runtimeControlBridge struct {
	mu       sync.Mutex
	bridge   *eebusstore.AssociationBridge
	prepared map[[32]byte]eebusstore.PreparedControlPublication
}

func (bridge *runtimeControlBridge) Reload(ctx context.Context) (uint64, map[string]string, string) {
	return bridge.bridge.Reload(ctx)
}

func (bridge *runtimeControlBridge) SelectedGeneration() uint64 {
	return bridge.bridge.SelectedGeneration()
}

func (bridge *runtimeControlBridge) Commit(ctx context.Context, expected uint64, remote []byte, shipID string) string {
	return bridge.bridge.Commit(ctx, expected, remote, shipID)
}

func (bridge *runtimeControlBridge) Close() error {
	return bridge.bridge.Close()
}

func (bridge *runtimeControlBridge) ReloadControl(ctx context.Context) (firstTrustControlView, string) {
	view, outcome := bridge.bridge.ReloadControl(ctx)
	converted, ok := runtimeControlViewFromStore(view)
	if !ok {
		return firstTrustControlView{}, "malformed_state"
	}
	return converted, outcome
}

func (bridge *runtimeControlBridge) PrepareControl(ctx context.Context, previous firstTrustControlView, target firstTrustControlRecord, operationID [32]byte, operationClass string) (firstTrustPreparedPublication, string) {
	previousStore, ok := runtimeControlViewToStore(previous)
	if !ok {
		return firstTrustPreparedPublication{}, "validation_failed"
	}
	targetStore, ok := runtimeControlRecordToStore(target)
	if !ok {
		return firstTrustPreparedPublication{}, "validation_failed"
	}
	publication, outcome := bridge.bridge.PrepareControl(ctx, previousStore, targetStore, operationID, operationClass)
	if outcome != "prepared" {
		return firstTrustPreparedPublication{}, outcome
	}
	convertedPrevious, previousOK := runtimeControlViewFromStore(publication.Previous)
	convertedTarget, targetOK := runtimeControlViewFromStore(publication.Target)
	if !previousOK || !targetOK {
		return firstTrustPreparedPublication{}, "validation_failed"
	}
	bridge.mu.Lock()
	if bridge.prepared == nil {
		bridge.prepared = make(map[[32]byte]eebusstore.PreparedControlPublication)
	}
	bridge.prepared[operationID] = publication
	bridge.mu.Unlock()
	return firstTrustPreparedPublication{
		previous: convertedPrevious, target: convertedTarget,
		operationID: publication.OperationID, operationClass: publication.OperationClass,
	}, "prepared"
}

func (bridge *runtimeControlBridge) CommitControl(ctx context.Context, publication firstTrustPreparedPublication) string {
	bridge.mu.Lock()
	prepared, ok := bridge.prepared[publication.operationID]
	delete(bridge.prepared, publication.operationID)
	bridge.mu.Unlock()
	if !ok || prepared.OperationClass != publication.operationClass {
		return "commit_not_published"
	}
	previous, previousOK := runtimeControlViewFromStore(prepared.Previous)
	target, targetOK := runtimeControlViewFromStore(prepared.Target)
	if !previousOK || !targetOK || !firstTrustManifestEqual(previous.manifest, publication.previous.manifest) ||
		!firstTrustManifestEqual(target.manifest, publication.target.manifest) || target.control.controlEpoch != publication.target.control.controlEpoch {
		return "commit_not_published"
	}
	return bridge.bridge.CommitControl(ctx, prepared)
}

func (bridge *runtimeControlBridge) ObserveControlPublication(ctx context.Context, pending firstTrustPendingPublication) string {
	return bridge.bridge.ObserveControlPublication(ctx, runtimePendingToStore(pending))
}

func runtimeControlViewFromStore(source eebusstore.ControlView) (firstTrustControlView, bool) {
	control, ok := runtimeControlRecordFromStore(source.Control)
	if !ok {
		return firstTrustControlView{}, false
	}
	return firstTrustControlView{
		manifest:           runtimeManifestFromStore(source.Manifest),
		control:            control,
		associations:       runtimeAssociationsFromStore(source.Associations),
		parentAssociations: runtimeAssociationsFromStore(source.ParentAssociations),
	}, true
}

func runtimeControlViewToStore(source firstTrustControlView) (eebusstore.ControlView, bool) {
	control, ok := runtimeControlRecordToStore(source.control)
	if !ok {
		return eebusstore.ControlView{}, false
	}
	return eebusstore.ControlView{
		Manifest:           runtimeManifestToStore(source.manifest),
		Control:            control,
		Associations:       runtimeAssociationsToStore(source.associations),
		ParentAssociations: runtimeAssociationsToStore(source.parentAssociations),
	}, true
}

func runtimeControlRecordFromStore(source eebusstore.ControlRecord) (firstTrustControlRecord, bool) {
	if !source.Present {
		return firstTrustControlRecord{}, true
	}
	result := firstTrustControlRecord{
		storeInstance: source.StoreInstance, controlEpoch: source.ControlEpoch,
		associationLineage: source.AssociationLineage, operationHighWater: source.OperationHighWater,
		repairSequence: source.RepairSequence,
		tombstones:     make([]firstTrustRevocationTombstone, len(source.Tombstones)),
		quarantines:    make([]firstTrustQuarantineRecord, len(source.Quarantines)),
		receipts:       make([]firstTrustDurableReceipt, len(source.Receipts)),
		attempts:       make([]firstTrustOutgoingAttemptRecord, len(source.Attempts)),
	}
	for index, value := range source.Tombstones {
		result.tombstones[index] = firstTrustRevocationTombstone{
			associationRef: value.AssociationRef, revocationEpoch: value.RevocationEpoch,
			operationID: value.OperationID, effectiveGeneration: runtimeGenerationFromStore(value.EffectiveGeneration),
		}
	}
	for index, value := range source.Quarantines {
		reason, reasonOK := runtimeQuarantineReasonFromCode(value.ReasonCode)
		state, stateOK := runtimeQuarantineStateFromCode(value.StateCode)
		if !reasonOK || !stateOK || value.RemainingDelay < 0 || value.RetentionBudget < 0 {
			return firstTrustControlRecord{}, false
		}
		result.quarantines[index] = firstTrustQuarantineRecord{
			scope: value.Scope, reason: reason, state: state, attemptCount: value.AttemptCount,
			backoffStep: value.BackoffStep, remainingDelay: time.Duration(value.RemainingDelay),
			retentionBudget: time.Duration(value.RetentionBudget), lastControlEpoch: value.LastControlEpoch,
		}
	}
	for index, value := range source.Receipts {
		operationClass, operationOK := runtimeOperationClassFromCode(value.OperationClass)
		resultCode, resultOK := runtimeResultFromCode(value.ResultCode)
		if !operationOK || !resultOK {
			return firstTrustControlRecord{}, false
		}
		result.receipts[index] = firstTrustDurableReceipt{
			operationID: value.OperationID, operationClass: operationClass,
			bindingSHA256: value.BindingSHA256, result: resultCode, terminal: value.Terminal,
		}
	}
	for index, value := range source.Attempts {
		state, stateOK := runtimeOutgoingAttemptStateFromCode(value.StateCode)
		if !stateOK {
			return firstTrustControlRecord{}, false
		}
		result.attempts[index] = firstTrustOutgoingAttemptRecord{
			state: state, attemptID: value.AttemptID, remoteSKI: append([]byte(nil), value.RemoteSKI...),
			scope: value.Scope, controlEpoch: value.ControlEpoch, associationLineage: value.AssociationLineage,
			endpoint: firstTrustOutgoingAttemptEndpoint{host: value.EndpointHost, port: value.EndpointPort}, path: value.Path,
			cancellationGeneration: value.CancellationGeneration, reservationOrder: value.ReservationOrder,
			reservationTimestamp: value.ReservationTimestamp, attemptCountBefore: value.AttemptCountBefore,
		}
	}
	return result, true
}

func runtimeControlRecordToStore(source firstTrustControlRecord) (eebusstore.ControlRecord, bool) {
	if source.controlEpoch == 0 {
		return eebusstore.ControlRecord{}, true
	}
	result := eebusstore.ControlRecord{
		Present: true, StoreInstance: source.storeInstance, ControlEpoch: source.controlEpoch,
		AssociationLineage: source.associationLineage, OperationHighWater: source.operationHighWater,
		RepairSequence: source.repairSequence,
		Tombstones:     make([]eebusstore.ControlTombstone, len(source.tombstones)),
		Quarantines:    make([]eebusstore.ControlQuarantine, len(source.quarantines)),
		Receipts:       make([]eebusstore.ControlReceipt, len(source.receipts)),
		Attempts:       make([]eebusstore.ControlAttempt, len(source.attempts)),
	}
	if source.replacementIdentity != nil {
		identity := source.replacementIdentity
		result.ReplaceLocalIdentity = true
		result.LocalCertificateChainDER = make([][]byte, len(identity.certificateChainDER))
		for index, certificate := range identity.certificateChainDER {
			result.LocalCertificateChainDER[index] = append([]byte(nil), certificate...)
		}
		result.LocalProviderID = identity.providerID
		result.LocalProviderVersion = identity.providerVersion
		result.LocalSealedBlob = append([]byte(nil), identity.sealedBlob...)
		result.LocalCertificateSPKISHA256 = identity.certificateSPKIHash
		result.LocalSKI = append([]byte(nil), identity.localSKI...)
	}
	for index, value := range source.tombstones {
		result.Tombstones[index] = eebusstore.ControlTombstone{
			AssociationRef: value.associationRef, RevocationEpoch: value.revocationEpoch,
			OperationID: value.operationID, EffectiveGeneration: runtimeGenerationToStore(value.effectiveGeneration),
		}
	}
	for index, value := range source.quarantines {
		reason, reasonOK := runtimeQuarantineReasonCode(value.reason)
		state, stateOK := runtimeQuarantineStateCode(value.state)
		if !reasonOK || !stateOK || value.remainingDelay < 0 || value.retentionBudget < 0 {
			return eebusstore.ControlRecord{}, false
		}
		result.Quarantines[index] = eebusstore.ControlQuarantine{
			Scope: value.scope, ReasonCode: reason, StateCode: state, AttemptCount: value.attemptCount,
			BackoffStep: value.backoffStep, RemainingDelay: int64(value.remainingDelay),
			RetentionBudget: int64(value.retentionBudget), LastControlEpoch: value.lastControlEpoch,
		}
	}
	for index, value := range source.receipts {
		operationClass, operationOK := runtimeOperationClassCode(value.operationClass)
		resultCode, resultOK := runtimeResultCode(value.result)
		if !operationOK || !resultOK {
			return eebusstore.ControlRecord{}, false
		}
		result.Receipts[index] = eebusstore.ControlReceipt{
			OperationID: value.operationID, OperationClass: operationClass,
			BindingSHA256: value.bindingSHA256, ResultCode: resultCode, Terminal: value.terminal,
		}
	}
	for index, value := range source.attempts {
		state, stateOK := runtimeOutgoingAttemptStateCode(value.state)
		if !stateOK {
			return eebusstore.ControlRecord{}, false
		}
		result.Attempts[index] = eebusstore.ControlAttempt{
			StateCode: state, AttemptID: value.attemptID, RemoteSKI: append([]byte(nil), value.remoteSKI...),
			Scope: value.scope, ControlEpoch: value.controlEpoch, AssociationLineage: value.associationLineage,
			EndpointHost: value.endpoint.host, EndpointPort: value.endpoint.port, Path: value.path,
			CancellationGeneration: value.cancellationGeneration, ReservationOrder: value.reservationOrder,
			ReservationTimestamp: value.reservationTimestamp, AttemptCountBefore: value.attemptCountBefore,
		}
	}
	return result, true
}

func runtimeManifestFromStore(source eebusstore.ControlManifestBinding) firstTrustManifestBinding {
	result := firstTrustManifestBinding{epoch: source.Epoch, sha256: source.SHA256, current: runtimeGenerationFromStore(source.Current)}
	if source.Parent != nil {
		parent := runtimeGenerationFromStore(*source.Parent)
		result.parent = &parent
	}
	return result
}

func runtimeManifestToStore(source firstTrustManifestBinding) eebusstore.ControlManifestBinding {
	result := eebusstore.ControlManifestBinding{Epoch: source.epoch, SHA256: source.sha256, Current: runtimeGenerationToStore(source.current)}
	if source.parent != nil {
		parent := runtimeGenerationToStore(*source.parent)
		result.Parent = &parent
	}
	return result
}

func runtimeGenerationFromStore(source eebusstore.ControlGenerationBinding) firstTrustGenerationBinding {
	return firstTrustGenerationBinding{sequence: source.Sequence, filename: source.Filename, sha256: source.SHA256, schemaVersion: source.SchemaVersion}
}

func runtimeGenerationToStore(source firstTrustGenerationBinding) eebusstore.ControlGenerationBinding {
	return eebusstore.ControlGenerationBinding{Sequence: source.sequence, Filename: source.filename, SHA256: source.sha256, SchemaVersion: source.schemaVersion}
}

func runtimeAssociationsFromStore(source []eebusstore.ControlAssociation) []firstTrustAssociationRecord {
	result := make([]firstTrustAssociationRecord, len(source))
	for index, value := range source {
		result[index] = firstTrustAssociationRecord{
			reference: value.Reference, lineage: value.Lineage, subject: append([]byte(nil), value.Subject...),
			service: value.Service, active: value.Active, trusted: value.Trusted,
			allowlisted: value.Allowlisted, reconnectable: value.Reconnectable,
		}
	}
	return result
}

func runtimeAssociationsToStore(source []firstTrustAssociationRecord) []eebusstore.ControlAssociation {
	result := make([]eebusstore.ControlAssociation, len(source))
	for index, value := range source {
		result[index] = eebusstore.ControlAssociation{
			Reference: value.reference, Lineage: value.lineage, Subject: append([]byte(nil), value.subject...),
			Service: value.service, Active: value.active, Trusted: value.trusted,
			Allowlisted: value.allowlisted, Reconnectable: value.reconnectable,
		}
	}
	return result
}

func runtimePendingToStore(source firstTrustPendingPublication) eebusstore.ControlPendingPublication {
	return eebusstore.ControlPendingPublication{
		OperationID: source.operationID, OperationClass: source.operationClass, StoreInstance: source.storeInstance,
		PreviousControlEpoch: source.previousControlEpoch, TargetControlEpoch: source.targetControlEpoch,
		PreviousManifest: runtimeManifestToStore(source.previousManifest), TargetManifest: runtimeManifestToStore(source.targetManifest),
	}
}

var runtimeOperationClasses = []string{"first_trust", "revocation", "reconcile_pending_publication", "publish_inactive_parent", "adopt_copied_current", "recover_unavailable_host_key", "release_retry_quarantine", "attempt_prepare", "attempt_authorize", "attempt_complete_success", "attempt_complete_failure", "attempt_abort", "attempt_restart_synthetic_failure", "attempt_fallback_prepare"}
var runtimeReceiptResults = []string{"trusted", "revoked", "repaired_unpaired", "operation_terminal", "failed_closed_unchanged", "revocation_withdrawal_incomplete"}
var runtimeQuarantineReasons = []string{"RETRYABLE_FAILURE", "ADMIN_HOLD", "HANDSHAKE_ATTEMPT_LIMIT"}
var runtimeQuarantineStates = []string{"BACKOFF_ACTIVE", "RETRY_READY", "ADMIN_HOLD"}
var runtimeOutgoingAttemptStates = []string{"ATTEMPT_RESERVED", "ATTEMPT_LAUNCH_AUTHORIZED"}

func runtimeOperationClassCode(value string) (uint64, bool) {
	return runtimeClosedCode(runtimeOperationClasses, value)
}
func runtimeOperationClassFromCode(value uint64) (string, bool) {
	return runtimeClosedValue(runtimeOperationClasses, value)
}
func runtimeResultCode(value string) (uint64, bool) {
	return runtimeClosedCode(runtimeReceiptResults, value)
}
func runtimeResultFromCode(value uint64) (string, bool) {
	return runtimeClosedValue(runtimeReceiptResults, value)
}
func runtimeQuarantineReasonCode(value string) (uint64, bool) {
	return runtimeClosedCode(runtimeQuarantineReasons, value)
}
func runtimeQuarantineReasonFromCode(value uint64) (string, bool) {
	return runtimeClosedValue(runtimeQuarantineReasons, value)
}
func runtimeQuarantineStateCode(value string) (uint64, bool) {
	return runtimeClosedCode(runtimeQuarantineStates, value)
}
func runtimeQuarantineStateFromCode(value uint64) (string, bool) {
	return runtimeClosedValue(runtimeQuarantineStates, value)
}
func runtimeOutgoingAttemptStateCode(value string) (uint64, bool) {
	return runtimeClosedCode(runtimeOutgoingAttemptStates, value)
}
func runtimeOutgoingAttemptStateFromCode(value uint64) (string, bool) {
	return runtimeClosedValue(runtimeOutgoingAttemptStates, value)
}

func runtimeClosedCode(values []string, value string) (uint64, bool) {
	for index, candidate := range values {
		if candidate == value {
			return uint64(index + 1), true
		}
	}
	return 0, false
}

func runtimeClosedValue(values []string, code uint64) (string, bool) {
	if code == 0 || code > uint64(len(values)) {
		return "", false
	}
	return values[code-1], true
}
