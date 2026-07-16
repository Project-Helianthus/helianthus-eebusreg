package eebusstore

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
)

const (
	controlOpaqueBytes        = 32
	maxControlTombstoneCount  = 128
	maxControlQuarantineCount = 128
	maxControlReceiptCount    = 128
	maxControlOperationCode   = math.MaxUint16
	maxControlQuarantineValue = math.MaxUint16
)

type controlRecordV2 struct {
	storeInstance      []byte
	controlEpoch       uint64
	associationLineage []byte
	tombstones         []controlTombstoneV2
	quarantines        []controlQuarantineV2
	receipts           []controlReceiptV2
	operationHighWater uint64
	repairSequence     uint64
	publication        *controlPublicationV2
}

type controlTombstoneV2 struct {
	associationRef      []byte
	revocationEpoch     uint64
	operationID         []byte
	effectiveGeneration generationReference
}

type controlQuarantineV2 struct {
	scope            []byte
	reasonCode       uint64
	stateCode        uint64
	attemptCount     uint64
	backoffStep      uint64
	remainingDelay   int64
	retentionBudget  int64
	lastControlEpoch uint64
}

type controlReceiptV2 struct {
	operationID    []byte
	operationClass uint64
	bindingSHA256  []byte
	resultCode     uint64
	terminal       bool
}

type controlPublicationV2 struct {
	operationID          []byte
	operationClass       uint64
	storeInstance        []byte
	previousControlEpoch uint64
	targetControlEpoch   uint64
	previousGeneration   generationReference
	targetGeneration     generationReference
}

type controlRecordWireV2 struct {
	AssociationLineage string                    `json:"association_lineage"`
	ControlEpoch       uint64                    `json:"control_epoch"`
	OperationHighWater uint64                    `json:"operation_high_water"`
	Publication        *controlPublicationWireV2 `json:"publication"`
	Quarantines        []controlQuarantineWireV2 `json:"quarantines"`
	Receipts           []controlReceiptWireV2    `json:"receipts"`
	RepairSequence     uint64                    `json:"repair_sequence"`
	StoreInstance      string                    `json:"store_instance"`
	Tombstones         []controlTombstoneWireV2  `json:"tombstones"`
}

type controlTombstoneWireV2 struct {
	AssociationRef      string                  `json:"association_ref"`
	EffectiveGeneration generationReferenceWire `json:"effective_generation"`
	OperationID         string                  `json:"operation_id"`
	RevocationEpoch     uint64                  `json:"revocation_epoch"`
}

type controlQuarantineWireV2 struct {
	AttemptCount     uint64 `json:"attempt_count"`
	BackoffStep      uint64 `json:"backoff_step"`
	LastControlEpoch uint64 `json:"last_control_epoch"`
	ReasonCode       uint64 `json:"reason_code"`
	RemainingDelay   int64  `json:"remaining_delay"`
	RetentionBudget  int64  `json:"retention_budget"`
	Scope            string `json:"scope"`
	StateCode        uint64 `json:"state_code"`
}

type controlReceiptWireV2 struct {
	BindingSHA256  string `json:"binding_sha256"`
	OperationClass uint64 `json:"operation_class"`
	OperationID    string `json:"operation_id"`
	ResultCode     uint64 `json:"result_code"`
	Terminal       bool   `json:"terminal"`
}

type controlPublicationWireV2 struct {
	OperationClass       uint64                  `json:"operation_class"`
	OperationID          string                  `json:"operation_id"`
	PreviousControlEpoch uint64                  `json:"previous_control_epoch"`
	PreviousGeneration   generationReferenceWire `json:"previous_generation"`
	StoreInstance        string                  `json:"store_instance"`
	TargetControlEpoch   uint64                  `json:"target_control_epoch"`
	TargetGeneration     generationReferenceWire `json:"target_generation"`
}

func withControlRecordV2(source stateV1, record controlRecordV2) stateV1 {
	result := cloneStateV1(source)
	payload, _ := encodeControlRecordV2Unchecked(record)
	result.controlEnvelope = payload
	return result
}

func controlRecordFromStateV1(source stateV1) (controlRecordV2, bool) {
	if len(source.controlEnvelope) == 0 {
		return controlRecordV2{}, false
	}
	record, err := decodeControlRecordV2(source.controlEnvelope)
	return record, err == nil
}

func cloneControlRecordV2(source controlRecordV2) controlRecordV2 {
	result := source
	result.storeInstance = bytes.Clone(source.storeInstance)
	result.associationLineage = bytes.Clone(source.associationLineage)
	result.tombstones = make([]controlTombstoneV2, len(source.tombstones))
	for index, tombstone := range source.tombstones {
		result.tombstones[index] = tombstone
		result.tombstones[index].associationRef = bytes.Clone(tombstone.associationRef)
		result.tombstones[index].operationID = bytes.Clone(tombstone.operationID)
	}
	result.quarantines = make([]controlQuarantineV2, len(source.quarantines))
	for index, quarantine := range source.quarantines {
		result.quarantines[index] = quarantine
		result.quarantines[index].scope = bytes.Clone(quarantine.scope)
	}
	result.receipts = make([]controlReceiptV2, len(source.receipts))
	for index, receipt := range source.receipts {
		result.receipts[index] = receipt
		result.receipts[index].operationID = bytes.Clone(receipt.operationID)
		result.receipts[index].bindingSHA256 = bytes.Clone(receipt.bindingSHA256)
	}
	if source.publication != nil {
		publication := *source.publication
		publication.operationID = bytes.Clone(source.publication.operationID)
		publication.storeInstance = bytes.Clone(source.publication.storeInstance)
		result.publication = &publication
	}
	return result
}

func validateControlRecordV2(record controlRecordV2) error {
	if len(record.storeInstance) != controlOpaqueBytes || len(record.associationLineage) != controlOpaqueBytes || record.controlEpoch == 0 || record.controlEpoch >= math.MaxUint64 {
		return malformed("validate_control", errors.New("record binding"))
	}
	if len(record.tombstones) > maxControlTombstoneCount || len(record.quarantines) > maxControlQuarantineCount || len(record.receipts) > maxControlReceiptCount || record.operationHighWater > math.MaxInt64 || record.repairSequence > math.MaxInt64 {
		return malformed("validate_control", errors.New("record bounds"))
	}
	tombstones := make(map[string]struct{}, len(record.tombstones))
	for _, tombstone := range record.tombstones {
		if len(tombstone.associationRef) != controlOpaqueBytes || len(tombstone.operationID) != controlOpaqueBytes || tombstone.revocationEpoch == 0 || tombstone.revocationEpoch > record.controlEpoch || validateGenerationReference(tombstone.effectiveGeneration) != nil {
			return malformed("validate_control", errors.New("tombstone"))
		}
		key := string(tombstone.associationRef)
		if _, exists := tombstones[key]; exists {
			return malformed("validate_control", errors.New("duplicate tombstone"))
		}
		tombstones[key] = struct{}{}
	}
	quarantines := make(map[string]struct{}, len(record.quarantines))
	for _, quarantine := range record.quarantines {
		if len(quarantine.scope) != controlOpaqueBytes || quarantine.reasonCode == 0 || quarantine.reasonCode > maxControlQuarantineValue || quarantine.stateCode == 0 || quarantine.stateCode > maxControlQuarantineValue || quarantine.attemptCount > maxControlQuarantineValue || quarantine.backoffStep > maxControlQuarantineValue || quarantine.remainingDelay < 0 || quarantine.retentionBudget < 0 || quarantine.lastControlEpoch > record.controlEpoch {
			return malformed("validate_control", errors.New("quarantine"))
		}
		key := string(quarantine.scope)
		if _, exists := quarantines[key]; exists {
			return malformed("validate_control", errors.New("duplicate quarantine"))
		}
		quarantines[key] = struct{}{}
	}
	receipts := make(map[string]struct{}, len(record.receipts))
	for _, receipt := range record.receipts {
		if len(receipt.operationID) != controlOpaqueBytes || len(receipt.bindingSHA256) != controlOpaqueBytes || receipt.operationClass == 0 || receipt.operationClass > maxControlOperationCode || receipt.resultCode == 0 || receipt.resultCode > maxControlOperationCode {
			return malformed("validate_control", errors.New("receipt"))
		}
		key := string(receipt.operationID)
		if _, exists := receipts[key]; exists {
			return malformed("validate_control", errors.New("duplicate receipt"))
		}
		receipts[key] = struct{}{}
	}
	if record.publication != nil {
		publication := record.publication
		if len(publication.operationID) != controlOpaqueBytes || publication.operationClass == 0 || publication.operationClass > maxControlOperationCode || !bytes.Equal(publication.storeInstance, record.storeInstance) || publication.previousControlEpoch == math.MaxUint64 || publication.targetControlEpoch != publication.previousControlEpoch+1 || publication.targetControlEpoch != record.controlEpoch || validateGenerationReference(publication.previousGeneration) != nil || validateGenerationReference(publication.targetGeneration) != nil || publication.previousGeneration.generation == publication.targetGeneration.generation {
			return malformed("validate_control", errors.New("publication"))
		}
	}
	return nil
}

func encodeControlRecordV2(record controlRecordV2) ([]byte, error) {
	if err := validateControlRecordV2(record); err != nil {
		return nil, err
	}
	return encodeControlRecordV2Unchecked(record)
}

func encodeControlRecordV2Unchecked(record controlRecordV2) ([]byte, error) {
	wire := controlRecordWireV2{
		AssociationLineage: base64.StdEncoding.EncodeToString(record.associationLineage),
		ControlEpoch:       record.controlEpoch, OperationHighWater: record.operationHighWater,
		Quarantines: make([]controlQuarantineWireV2, len(record.quarantines)),
		Receipts:    make([]controlReceiptWireV2, len(record.receipts)), RepairSequence: record.repairSequence,
		StoreInstance: base64.StdEncoding.EncodeToString(record.storeInstance),
		Tombstones:    make([]controlTombstoneWireV2, len(record.tombstones)),
	}
	for index, tombstone := range record.tombstones {
		wire.Tombstones[index] = controlTombstoneWireV2{
			AssociationRef:      base64.StdEncoding.EncodeToString(tombstone.associationRef),
			EffectiveGeneration: generationReferenceToWire(tombstone.effectiveGeneration),
			OperationID:         base64.StdEncoding.EncodeToString(tombstone.operationID), RevocationEpoch: tombstone.revocationEpoch,
		}
	}
	for index, quarantine := range record.quarantines {
		wire.Quarantines[index] = controlQuarantineWireV2{
			AttemptCount: quarantine.attemptCount, BackoffStep: quarantine.backoffStep, LastControlEpoch: quarantine.lastControlEpoch,
			ReasonCode: quarantine.reasonCode, RemainingDelay: quarantine.remainingDelay, RetentionBudget: quarantine.retentionBudget,
			Scope: base64.StdEncoding.EncodeToString(quarantine.scope), StateCode: quarantine.stateCode,
		}
	}
	for index, receipt := range record.receipts {
		wire.Receipts[index] = controlReceiptWireV2{
			BindingSHA256: base64.StdEncoding.EncodeToString(receipt.bindingSHA256), OperationClass: receipt.operationClass,
			OperationID: base64.StdEncoding.EncodeToString(receipt.operationID), ResultCode: receipt.resultCode, Terminal: receipt.terminal,
		}
	}
	if record.publication != nil {
		wire.Publication = &controlPublicationWireV2{
			OperationClass: record.publication.operationClass, OperationID: base64.StdEncoding.EncodeToString(record.publication.operationID),
			PreviousControlEpoch: record.publication.previousControlEpoch, PreviousGeneration: generationReferenceToWire(record.publication.previousGeneration),
			StoreInstance: base64.StdEncoding.EncodeToString(record.publication.storeInstance), TargetControlEpoch: record.publication.targetControlEpoch,
			TargetGeneration: generationReferenceToWire(record.publication.targetGeneration),
		}
	}
	return json.Marshal(wire)
}

func decodeControlRecordV2(payload []byte) (controlRecordV2, error) {
	var wire controlRecordWireV2
	if err := decodeClosedJSON(payload, &wire); err != nil {
		return controlRecordV2{}, malformed("decode_control", err)
	}
	storeInstance, err := decodeControlOpaque(wire.StoreInstance)
	if err != nil {
		return controlRecordV2{}, err
	}
	lineage, err := decodeControlOpaque(wire.AssociationLineage)
	if err != nil {
		return controlRecordV2{}, err
	}
	record := controlRecordV2{
		storeInstance: storeInstance, controlEpoch: wire.ControlEpoch, associationLineage: lineage,
		tombstones: make([]controlTombstoneV2, len(wire.Tombstones)), quarantines: make([]controlQuarantineV2, len(wire.Quarantines)),
		receipts: make([]controlReceiptV2, len(wire.Receipts)), operationHighWater: wire.OperationHighWater, repairSequence: wire.RepairSequence,
	}
	for index, item := range wire.Tombstones {
		reference, decodeErr := decodeControlOpaque(item.AssociationRef)
		if decodeErr != nil {
			return controlRecordV2{}, decodeErr
		}
		operationID, decodeErr := decodeControlOpaque(item.OperationID)
		if decodeErr != nil {
			return controlRecordV2{}, decodeErr
		}
		generation, decodeErr := decodeGenerationReference(item.EffectiveGeneration)
		if decodeErr != nil {
			return controlRecordV2{}, decodeErr
		}
		record.tombstones[index] = controlTombstoneV2{associationRef: reference, effectiveGeneration: generation, operationID: operationID, revocationEpoch: item.RevocationEpoch}
	}
	for index, item := range wire.Quarantines {
		scope, decodeErr := decodeControlOpaque(item.Scope)
		if decodeErr != nil {
			return controlRecordV2{}, decodeErr
		}
		record.quarantines[index] = controlQuarantineV2{
			scope: scope, reasonCode: item.ReasonCode, stateCode: item.StateCode, attemptCount: item.AttemptCount,
			backoffStep: item.BackoffStep, remainingDelay: item.RemainingDelay, retentionBudget: item.RetentionBudget, lastControlEpoch: item.LastControlEpoch,
		}
	}
	for index, item := range wire.Receipts {
		operationID, decodeErr := decodeControlOpaque(item.OperationID)
		if decodeErr != nil {
			return controlRecordV2{}, decodeErr
		}
		binding, decodeErr := decodeControlOpaque(item.BindingSHA256)
		if decodeErr != nil {
			return controlRecordV2{}, decodeErr
		}
		record.receipts[index] = controlReceiptV2{operationID: operationID, operationClass: item.OperationClass, bindingSHA256: binding, resultCode: item.ResultCode, terminal: item.Terminal}
	}
	if wire.Publication != nil {
		operationID, decodeErr := decodeControlOpaque(wire.Publication.OperationID)
		if decodeErr != nil {
			return controlRecordV2{}, decodeErr
		}
		instance, decodeErr := decodeControlOpaque(wire.Publication.StoreInstance)
		if decodeErr != nil {
			return controlRecordV2{}, decodeErr
		}
		previous, decodeErr := decodeGenerationReference(wire.Publication.PreviousGeneration)
		if decodeErr != nil {
			return controlRecordV2{}, decodeErr
		}
		target, decodeErr := decodeGenerationReference(wire.Publication.TargetGeneration)
		if decodeErr != nil {
			return controlRecordV2{}, decodeErr
		}
		record.publication = &controlPublicationV2{
			operationID: operationID, operationClass: wire.Publication.OperationClass, storeInstance: instance,
			previousControlEpoch: wire.Publication.PreviousControlEpoch, targetControlEpoch: wire.Publication.TargetControlEpoch,
			previousGeneration: previous, targetGeneration: target,
		}
	}
	if err := validateControlRecordV2(record); err != nil {
		return controlRecordV2{}, err
	}
	return record, nil
}

func decodeControlOpaque(value string) ([]byte, error) {
	return decodeCanonicalBase64(value, controlOpaqueBytes, controlOpaqueBytes)
}

func generationReferenceToWire(reference generationReference) generationReferenceWire {
	return generationReferenceWire{
		Generation: reference.generation, GenerationFile: reference.generationFile,
		GenerationSHA256: reference.generationSHA256, SchemaVersion: reference.schemaVersion,
	}
}
