//go:build linux || darwin

package eebusstore

import (
	"bytes"
	"context"
	"encoding/hex"
	"math"
	"sort"
)

type ControlGenerationBinding struct {
	Sequence      uint64
	Filename      string
	SHA256        [32]byte
	SchemaVersion uint64
}

type ControlManifestBinding struct {
	Epoch   uint64
	SHA256  [32]byte
	Current ControlGenerationBinding
	Parent  *ControlGenerationBinding
}

type ControlAssociation struct {
	Reference     [32]byte
	Lineage       [32]byte
	Subject       []byte
	Service       string
	Active        bool
	Trusted       bool
	Allowlisted   bool
	Reconnectable bool
}

type ControlTombstone struct {
	AssociationRef      [32]byte
	RevocationEpoch     uint64
	OperationID         [32]byte
	EffectiveGeneration ControlGenerationBinding
}

type ControlQuarantine struct {
	Scope            [32]byte
	ReasonCode       uint64
	StateCode        uint64
	AttemptCount     uint64
	BackoffStep      uint64
	RemainingDelay   int64
	RetentionBudget  int64
	LastControlEpoch uint64
}

type ControlReceipt struct {
	OperationID    [32]byte
	OperationClass uint64
	BindingSHA256  [32]byte
	ResultCode     uint64
	Terminal       bool
}

type ControlAttempt struct {
	StateCode              uint64
	AttemptID              [32]uint8
	RemoteSKI              []uint8
	Scope                  [32]uint8
	ControlEpoch           uint64
	AssociationLineage     [32]uint8
	EndpointHost           string
	EndpointPort           uint16
	Path                   string
	CancellationGeneration uint64
	ReservationOrder       uint64
	ReservationTimestamp   int64
	AttemptCountBefore     uint64
}

type ControlPublication struct {
	OperationID          [32]byte
	OperationClass       uint64
	StoreInstance        [32]byte
	PreviousControlEpoch uint64
	TargetControlEpoch   uint64
	PreviousGeneration   ControlGenerationBinding
	TargetGeneration     ControlGenerationBinding
}

type ControlRecord struct {
	Present                    bool
	StoreInstance              [32]byte
	ControlEpoch               uint64
	AssociationLineage         [32]byte
	Tombstones                 []ControlTombstone
	Quarantines                []ControlQuarantine
	Receipts                   []ControlReceipt
	Attempts                   []ControlAttempt
	OperationHighWater         uint64
	RepairSequence             uint64
	Publication                *ControlPublication
	ReplaceLocalIdentity       bool
	LocalCertificateChainDER   [][]byte
	LocalProviderID            string
	LocalProviderVersion       uint64
	LocalSealedBlob            []byte
	LocalCertificateSPKISHA256 [32]byte
	LocalSKI                   []byte
}

type ControlView struct {
	Manifest           ControlManifestBinding
	Control            ControlRecord
	Associations       []ControlAssociation
	ParentAssociations []ControlAssociation
}

type ControlPendingPublication struct {
	OperationID          [32]byte
	OperationClass       string
	StoreInstance        [32]byte
	PreviousControlEpoch uint64
	TargetControlEpoch   uint64
	PreviousManifest     ControlManifestBinding
	TargetManifest       ControlManifestBinding
}

type PreparedControlPublication struct {
	Previous       ControlView
	Target         ControlView
	OperationID    [32]byte
	OperationClass string
	state          stateV1
}

func (bridge *AssociationBridge) ReloadControl(ctx context.Context) (ControlView, string) {
	if ctx == nil {
		ctx = context.Background()
	}
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	if ctx.Err() != nil {
		return ControlView{}, string(outcomeIOFailed)
	}
	if bridge.opened != nil {
		_ = bridge.opened.close()
		bridge.opened = nil
	}
	result := openStore(bridge.config)
	if result.store == nil || result.state == nil {
		return ControlView{}, string(result.outcome)
	}
	bridge.opened = result.store
	view, ok := controlViewFromStore(bridge.opened)
	if !ok {
		return ControlView{}, string(outcomeMalformedState)
	}
	return view, string(result.outcome)
}

func (bridge *AssociationBridge) PrepareControl(ctx context.Context, previous ControlView, target ControlRecord, operationID [32]byte, operationClass string) (PreparedControlPublication, string) {
	if ctx == nil {
		ctx = context.Background()
	}
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	if ctx.Err() != nil || bridge.opened == nil || bridge.opened.selected == nil || bridge.opened.selected.epoch == math.MaxInt64 || operationID == [32]byte{} || len(operationClass) == 0 || len(operationClass) > 64 || !controlManifestEqual(previous.Manifest, controlManifestFromStore(bridge.opened)) || !controlRecordEqual(previous.Control, controlRecordFromStore(bridge.opened.state)) || previous.Control.ControlEpoch == math.MaxUint64 || target.ControlEpoch != previous.Control.ControlEpoch+1 {
		return PreparedControlPublication{}, "validation_failed"
	}
	bindPreparedControlTombstones(&target, operationID, previous.Manifest.Current)
	state := stateFromControlView(previous, bridge.opened.state)
	if target.ReplaceLocalIdentity {
		identity, valid := localIdentityFromControlTarget(target)
		if !valid {
			return PreparedControlPublication{}, "validation_failed"
		}
		state.localIdentity = identity
	}
	decoded, ok := controlRecordToInternal(target)
	if !ok {
		return PreparedControlPublication{}, "validation_failed"
	}
	state = withControlRecordV3(state, decoded)
	sequence, err := bridge.opened.nextSequence()
	if err != nil {
		return PreparedControlPublication{}, string(outcomeCommitNotPublished)
	}
	metadata := generationMetadata{sequence: sequence}
	parent := bridge.opened.manifest.current
	parentSequence, parentDigest := parent.generation, parent.generationSHA256
	metadata.parentSequence, metadata.parentSHA256 = &parentSequence, &parentDigest
	generation := generationV1{metadata: metadata, state: cloneStateV1(state), schemaVersion: bridge.opened.migrations.current}
	generationBytes, err := encodeGenerationV1(generation)
	if err != nil {
		return PreparedControlPublication{}, "validation_failed"
	}
	reference := generationReference{generation: sequence, generationFile: generationFilename(sequence), generationSHA256: sha256Hex(generationBytes), schemaVersion: bridge.opened.migrations.current}
	manifest := manifestPayloadV1{manifestVersion: currentManifestVersion, current: reference, parent: &parent}
	manifestBytes, err := encodeManifestPayloadV1(manifest)
	if err != nil {
		return PreparedControlPublication{}, "validation_failed"
	}
	targetView := controlViewFromParts(previous, state, manifest, bridge.opened.selected.epoch+1, sha256Hex(manifestBytes))
	return PreparedControlPublication{
		Previous: cloneControlView(previous), Target: targetView, OperationID: operationID,
		OperationClass: operationClass, state: cloneStateV1(state),
	}, "prepared"
}

func (bridge *AssociationBridge) CommitControl(ctx context.Context, publication PreparedControlPublication) string {
	if ctx == nil {
		ctx = context.Background()
	}
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	if ctx.Err() != nil || bridge.opened == nil || !controlManifestEqual(publication.Previous.Manifest, controlManifestFromStore(bridge.opened)) || !controlRecordEqual(publication.Previous.Control, controlRecordFromStore(bridge.opened.state)) {
		return string(outcomeCommitNotPublished)
	}
	result := bridge.opened.commit(cloneStateV1(publication.state))
	if result.outcome == outcomeCommitDurable && !controlManifestEqual(publication.Target.Manifest, controlManifestFromStore(bridge.opened)) {
		bridge.opened.poisoned = true
		return string(outcomeCommitDurabilityUnknown)
	}
	return string(result.outcome)
}

func (bridge *AssociationBridge) ObserveControlPublication(_ context.Context, pending ControlPendingPublication) string {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	if bridge.opened == nil {
		return "other_or_ambiguous"
	}
	selected, ok := controlViewFromStore(bridge.opened)
	if !ok {
		return "other_or_ambiguous"
	}
	if controlManifestEqual(selected.Manifest, pending.TargetManifest) && selected.Control.ControlEpoch == pending.TargetControlEpoch {
		return "exact_target_selected"
	}
	if controlManifestEqual(selected.Manifest, pending.PreviousManifest) && selected.Control.ControlEpoch == pending.PreviousControlEpoch {
		return "exact_previous_selected_and_target_absent"
	}
	if selected.Manifest.Epoch == pending.TargetManifest.Epoch || selected.Manifest.Current.Sequence == pending.TargetManifest.Current.Sequence {
		return "same_number_different_digest_or_reference"
	}
	return "other_or_ambiguous"
}

func controlViewFromStore(opened *store) (ControlView, bool) {
	if opened == nil {
		return ControlView{}, false
	}
	view := ControlView{Manifest: controlManifestFromStore(opened), Control: controlRecordFromStore(opened.state)}
	var ok bool
	view.Associations, ok = controlAssociationsFromState(opened.state, view.Control.AssociationLineage)
	if !ok {
		return ControlView{}, false
	}
	if opened.manifest != nil && opened.manifest.parent != nil {
		parentState, loaded := loadControlGenerationState(opened, *opened.manifest.parent)
		if !loaded {
			return ControlView{}, false
		}
		parentLineage := view.Control.AssociationLineage
		if parentControl := controlRecordFromStore(parentState); parentControl.Present {
			parentLineage = parentControl.AssociationLineage
		}
		view.ParentAssociations, ok = controlAssociationsFromState(parentState, parentLineage)
		if !ok {
			return ControlView{}, false
		}
	}
	return view, true
}

func controlViewFromParts(previous ControlView, state stateV1, manifest manifestPayloadV1, epoch uint64, digest string) ControlView {
	view := ControlView{Manifest: controlManifestFromParts(manifest, epoch, digest), Control: controlRecordFromStore(state)}
	view.Associations = cloneControlAssociations(previous.Associations)
	view.ParentAssociations = cloneControlAssociations(previous.ParentAssociations)
	return view
}

func controlManifestFromStore(opened *store) ControlManifestBinding {
	if opened == nil || opened.manifest == nil || opened.selected == nil {
		return ControlManifestBinding{}
	}
	return controlManifestFromParts(*opened.manifest, opened.selected.epoch, opened.selected.envelope.manifestSHA256)
}

func controlManifestFromParts(manifest manifestPayloadV1, epoch uint64, digest string) ControlManifestBinding {
	result := ControlManifestBinding{Epoch: epoch, SHA256: digestArray(digest), Current: controlGenerationFromInternal(manifest.current)}
	if manifest.parent != nil {
		parent := controlGenerationFromInternal(*manifest.parent)
		result.Parent = &parent
	}
	return result
}

func controlGenerationFromInternal(reference generationReference) ControlGenerationBinding {
	return ControlGenerationBinding{Sequence: reference.generation, Filename: reference.generationFile, SHA256: digestArray(reference.generationSHA256), SchemaVersion: reference.schemaVersion}
}

func controlGenerationToInternal(reference ControlGenerationBinding) generationReference {
	return generationReference{generation: reference.Sequence, generationFile: reference.Filename, generationSHA256: hex.EncodeToString(reference.SHA256[:]), schemaVersion: reference.SchemaVersion}
}

func controlRecordFromStore(state stateV1) ControlRecord {
	record, ok := controlRecordV3FromStateV1(state)
	if !ok {
		legacy, legacyOK := controlRecordFromStateV1(state)
		if legacyOK {
			record, ok = controlRecordV3FromV2(legacy), true
		}
	}
	if !ok {
		return ControlRecord{}
	}
	result := ControlRecord{Present: true, ControlEpoch: record.controlEpoch, OperationHighWater: record.operationHighWater, RepairSequence: record.repairSequence}
	copy(result.StoreInstance[:], record.storeInstance)
	copy(result.AssociationLineage[:], record.associationLineage)
	result.Tombstones = make([]ControlTombstone, len(record.tombstones))
	for index, value := range record.tombstones {
		copy(result.Tombstones[index].AssociationRef[:], value.associationRef)
		copy(result.Tombstones[index].OperationID[:], value.operationID)
		result.Tombstones[index].RevocationEpoch = value.revocationEpoch
		result.Tombstones[index].EffectiveGeneration = controlGenerationFromInternal(value.effectiveGeneration)
	}
	result.Quarantines = make([]ControlQuarantine, len(record.quarantines))
	for index, value := range record.quarantines {
		copy(result.Quarantines[index].Scope[:], value.scope)
		result.Quarantines[index].ReasonCode, result.Quarantines[index].StateCode = value.reasonCode, value.stateCode
		result.Quarantines[index].AttemptCount, result.Quarantines[index].BackoffStep = value.attemptCount, value.backoffStep
		result.Quarantines[index].RemainingDelay, result.Quarantines[index].RetentionBudget = value.remainingDelay, value.retentionBudget
		result.Quarantines[index].LastControlEpoch = value.lastControlEpoch
	}
	result.Receipts = make([]ControlReceipt, len(record.receipts))
	for index, value := range record.receipts {
		copy(result.Receipts[index].OperationID[:], value.operationID)
		copy(result.Receipts[index].BindingSHA256[:], value.bindingSHA256)
		result.Receipts[index].OperationClass, result.Receipts[index].ResultCode, result.Receipts[index].Terminal = value.operationClass, value.resultCode, value.terminal
	}
	result.Attempts = make([]ControlAttempt, len(record.attempts))
	for index, value := range record.attempts {
		result.Attempts[index] = ControlAttempt{
			StateCode: value.stateCode, RemoteSKI: bytes.Clone(value.remoteSKI), ControlEpoch: value.controlEpoch,
			EndpointHost: value.endpointHost, EndpointPort: value.endpointPort, Path: value.path,
			CancellationGeneration: value.cancellationGeneration, ReservationOrder: value.reservationOrder,
			ReservationTimestamp: value.reservationTimestamp, AttemptCountBefore: value.attemptCountBefore,
		}
		copy(result.Attempts[index].AttemptID[:], value.attemptID)
		copy(result.Attempts[index].Scope[:], value.scope)
		copy(result.Attempts[index].AssociationLineage[:], value.associationLineage)
	}
	if record.publication != nil {
		publication := ControlPublication{
			OperationClass: record.publication.operationClass, PreviousControlEpoch: record.publication.previousControlEpoch,
			TargetControlEpoch: record.publication.targetControlEpoch, PreviousGeneration: controlGenerationFromInternal(record.publication.previousGeneration),
			TargetGeneration: controlGenerationFromInternal(record.publication.targetGeneration),
		}
		copy(publication.OperationID[:], record.publication.operationID)
		copy(publication.StoreInstance[:], record.publication.storeInstance)
		result.Publication = &publication
	}
	if state.localIdentity != nil {
		identity := state.localIdentity
		result.LocalCertificateChainDER = make([][]byte, len(identity.certificateChainDER))
		for index, certificate := range identity.certificateChainDER {
			result.LocalCertificateChainDER[index] = bytes.Clone(certificate)
		}
		result.LocalProviderID = identity.keyReference.providerID
		result.LocalProviderVersion = identity.keyReference.providerVersion
		result.LocalSealedBlob = bytes.Clone(identity.keyReference.sealedBlob)
		result.LocalCertificateSPKISHA256 = digestArray(identity.keyReference.certificateSPKISHA256)
		result.LocalSKI = bytes.Clone(identity.localSKI)
	}
	return result
}

func controlRecordToInternal(record ControlRecord) (controlRecordV3, bool) {
	if !record.Present {
		return controlRecordV3{}, false
	}
	result := controlRecordV3{
		storeInstance: append([]byte(nil), record.StoreInstance[:]...), controlEpoch: record.ControlEpoch,
		associationLineage: append([]byte(nil), record.AssociationLineage[:]...), operationHighWater: record.OperationHighWater,
		repairSequence: record.RepairSequence, tombstones: make([]controlTombstoneV2, len(record.Tombstones)),
		quarantines: make([]controlQuarantineV2, len(record.Quarantines)), receipts: make([]controlReceiptV2, len(record.Receipts)),
		attempts: make([]controlAttemptV3, len(record.Attempts)),
	}
	for index, value := range record.Tombstones {
		result.tombstones[index] = controlTombstoneV2{associationRef: append([]byte(nil), value.AssociationRef[:]...), revocationEpoch: value.RevocationEpoch, operationID: append([]byte(nil), value.OperationID[:]...), effectiveGeneration: controlGenerationToInternal(value.EffectiveGeneration)}
	}
	for index, value := range record.Quarantines {
		result.quarantines[index] = controlQuarantineV2{scope: append([]byte(nil), value.Scope[:]...), reasonCode: value.ReasonCode, stateCode: value.StateCode, attemptCount: value.AttemptCount, backoffStep: value.BackoffStep, remainingDelay: value.RemainingDelay, retentionBudget: value.RetentionBudget, lastControlEpoch: value.LastControlEpoch}
	}
	for index, value := range record.Receipts {
		result.receipts[index] = controlReceiptV2{operationID: append([]byte(nil), value.OperationID[:]...), operationClass: value.OperationClass, bindingSHA256: append([]byte(nil), value.BindingSHA256[:]...), resultCode: value.ResultCode, terminal: value.Terminal}
	}
	for index, value := range record.Attempts {
		result.attempts[index] = controlAttemptV3{
			stateCode: value.StateCode, attemptID: append([]byte(nil), value.AttemptID[:]...), remoteSKI: bytes.Clone(value.RemoteSKI),
			scope: append([]byte(nil), value.Scope[:]...), controlEpoch: value.ControlEpoch,
			associationLineage: append([]byte(nil), value.AssociationLineage[:]...),
			endpointHost:       value.EndpointHost, endpointPort: value.EndpointPort, path: value.Path,
			cancellationGeneration: value.CancellationGeneration, reservationOrder: value.ReservationOrder,
			reservationTimestamp: value.ReservationTimestamp, attemptCountBefore: value.AttemptCountBefore,
		}
	}
	if record.Publication != nil {
		result.publication = &controlPublicationV2{
			operationID: append([]byte(nil), record.Publication.OperationID[:]...), operationClass: record.Publication.OperationClass,
			storeInstance: append([]byte(nil), record.Publication.StoreInstance[:]...), previousControlEpoch: record.Publication.PreviousControlEpoch,
			targetControlEpoch: record.Publication.TargetControlEpoch, previousGeneration: controlGenerationToInternal(record.Publication.PreviousGeneration),
			targetGeneration: controlGenerationToInternal(record.Publication.TargetGeneration),
		}
	}
	return result, validateControlRecordV3(result) == nil
}

func controlAssociationsFromState(state stateV1, lineage [32]byte) ([]ControlAssociation, bool) {
	result := make([]ControlAssociation, 0, len(state.remoteIdentities))
	seen := make(map[[32]byte]struct{}, len(state.remoteIdentities))
	for _, identity := range state.remoteIdentities {
		if len(identity.recordID) == 0 || len(identity.remoteSKI) != 20 || identity.remoteSHIPID == "" {
			return nil, false
		}
		reference := [32]byte{}
		if len(identity.recordID) == len(reference) {
			copy(reference[:], identity.recordID)
		} else {
			reference = sha256Bytes(identity.recordID)
		}
		if reference == [32]byte{} {
			return nil, false
		}
		if _, duplicate := seen[reference]; duplicate {
			return nil, false
		}
		seen[reference] = struct{}{}
		result = append(result, ControlAssociation{
			Reference: reference, Lineage: lineage, Subject: bytes.Clone(identity.remoteSKI), Service: identity.remoteSHIPID,
			Active: true, Trusted: true, Allowlisted: true, Reconnectable: true,
		})
	}
	return result, true
}

func loadControlGenerationState(opened *store, reference generationReference) (stateV1, bool) {
	if opened == nil || reference.generation == 0 || reference.schemaVersion > opened.migrations.current {
		return stateV1{}, false
	}
	payload, err := readVerifiedFile(opened.generations, reference.generationFile, maxGenerationBytes)
	if err != nil || sha256Hex(payload) != reference.generationSHA256 {
		return stateV1{}, false
	}
	generation, err := decodeGenerationV1(payload)
	if err != nil || generation.metadata.sequence != reference.generation || generationSchemaVersion(payload) != reference.schemaVersion {
		return stateV1{}, false
	}
	state, err := opened.migrations.apply(reference.schemaVersion, generation.state)
	if err != nil || validateStateV1(state) != nil {
		return stateV1{}, false
	}
	return state, true
}

func bindPreparedControlTombstones(record *ControlRecord, operationID [32]byte, effective ControlGenerationBinding) {
	for index := range record.Tombstones {
		tombstone := &record.Tombstones[index]
		if tombstone.OperationID == operationID && tombstone.EffectiveGeneration == (ControlGenerationBinding{}) {
			tombstone.EffectiveGeneration = effective
		}
	}
}

func stateFromControlView(view ControlView, source stateV1) stateV1 {
	result := cloneStateV1(source)
	result.remoteIdentities = result.remoteIdentities[:0]
	for _, association := range view.Associations {
		if !association.Active && !association.Trusted && !association.Allowlisted && !association.Reconnectable {
			continue
		}
		result.remoteIdentities = append(result.remoteIdentities, remoteIdentityV1{recordID: append([]byte(nil), association.Reference[:]...), remoteSKI: bytes.Clone(association.Subject), remoteSHIPID: association.Service})
	}
	sort.Slice(result.remoteIdentities, func(left, right int) bool {
		return bytes.Compare(result.remoteIdentities[left].recordID, result.remoteIdentities[right].recordID) < 0
	})
	return result
}

func cloneControlView(source ControlView) ControlView {
	result := source
	result.Associations = cloneControlAssociations(source.Associations)
	result.ParentAssociations = cloneControlAssociations(source.ParentAssociations)
	if source.Manifest.Parent != nil {
		parent := *source.Manifest.Parent
		result.Manifest.Parent = &parent
	}
	result.Control.Tombstones = append([]ControlTombstone(nil), source.Control.Tombstones...)
	result.Control.Quarantines = append([]ControlQuarantine(nil), source.Control.Quarantines...)
	result.Control.Receipts = append([]ControlReceipt(nil), source.Control.Receipts...)
	result.Control.Attempts = make([]ControlAttempt, len(source.Control.Attempts))
	for index, attempt := range source.Control.Attempts {
		result.Control.Attempts[index] = attempt
		result.Control.Attempts[index].RemoteSKI = bytes.Clone(attempt.RemoteSKI)
	}
	result.Control.LocalCertificateChainDER = make([][]byte, len(source.Control.LocalCertificateChainDER))
	for index, certificate := range source.Control.LocalCertificateChainDER {
		result.Control.LocalCertificateChainDER[index] = bytes.Clone(certificate)
	}
	result.Control.LocalSealedBlob = bytes.Clone(source.Control.LocalSealedBlob)
	result.Control.LocalSKI = bytes.Clone(source.Control.LocalSKI)
	if source.Control.Publication != nil {
		publication := *source.Control.Publication
		result.Control.Publication = &publication
	}
	return result
}

func localIdentityFromControlTarget(record ControlRecord) (*localIdentityV1, bool) {
	if len(record.LocalCertificateChainDER) == 0 || record.LocalProviderID == "" || record.LocalProviderVersion == 0 ||
		len(record.LocalSealedBlob) == 0 || record.LocalCertificateSPKISHA256 == [32]byte{} || len(record.LocalSKI) == 0 {
		return nil, false
	}
	identity := &localIdentityV1{
		certificateChainDER: make([][]byte, len(record.LocalCertificateChainDER)),
		keyReference: protectedKeyReference{
			providerID: record.LocalProviderID, providerVersion: record.LocalProviderVersion,
			sealedBlob: bytes.Clone(record.LocalSealedBlob), certificateSPKISHA256: hex.EncodeToString(record.LocalCertificateSPKISHA256[:]),
		},
		localSKI: bytes.Clone(record.LocalSKI),
	}
	for index, certificate := range record.LocalCertificateChainDER {
		identity.certificateChainDER[index] = bytes.Clone(certificate)
	}
	return identity, validateStateV1(stateV1{localIdentity: identity}) == nil
}

func cloneControlAssociations(source []ControlAssociation) []ControlAssociation {
	result := make([]ControlAssociation, len(source))
	for index, association := range source {
		result[index] = association
		result[index].Subject = bytes.Clone(association.Subject)
	}
	return result
}

func controlManifestEqual(left, right ControlManifestBinding) bool {
	if left.Epoch != right.Epoch || left.SHA256 != right.SHA256 || left.Current != right.Current || (left.Parent == nil) != (right.Parent == nil) {
		return false
	}
	return left.Parent == nil || *left.Parent == *right.Parent
}

func controlRecordEqual(left, right ControlRecord) bool {
	leftView, leftOK := controlRecordToInternal(left)
	rightView, rightOK := controlRecordToInternal(right)
	return leftOK == rightOK && (!leftOK || bytes.Equal(mustEncodeControl(leftView), mustEncodeControl(rightView)))
}

func mustEncodeControl(record controlRecordV3) []byte {
	payload, _ := encodeControlRecordV3(record)
	return payload
}

func digestArray(value string) [32]byte {
	var result [32]byte
	decoded, err := hex.DecodeString(value)
	if err == nil {
		copy(result[:], decoded)
	}
	return result
}

func sha256Bytes(value []byte) [32]byte {
	return digestArray(sha256Hex(value))
}
