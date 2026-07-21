package eebusstore

import (
	"bytes"
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

type controlTombstone struct {
	associationRef      []byte
	revocationEpoch     uint64
	operationID         []byte
	effectiveGeneration generationReference
}

type controlQuarantine struct {
	scope            []byte
	reasonCode       uint64
	stateCode        uint64
	attemptCount     uint64
	backoffStep      uint64
	remainingDelay   int64
	retentionBudget  int64
	lastControlEpoch uint64
}

type controlReceipt struct {
	operationID    []byte
	operationClass uint64
	bindingSHA256  []byte
	resultCode     uint64
	terminal       bool
}

type controlPublication struct {
	operationID          []byte
	operationClass       uint64
	storeInstance        []byte
	previousControlEpoch uint64
	targetControlEpoch   uint64
	previousGeneration   generationReference
	targetGeneration     generationReference
}

type controlTombstoneWire struct {
	AssociationRef      string                  `json:"association_ref"`
	EffectiveGeneration generationReferenceWire `json:"effective_generation"`
	OperationID         string                  `json:"operation_id"`
	RevocationEpoch     uint64                  `json:"revocation_epoch"`
}

type controlQuarantineWire struct {
	AttemptCount     uint64 `json:"attempt_count"`
	BackoffStep      uint64 `json:"backoff_step"`
	LastControlEpoch uint64 `json:"last_control_epoch"`
	ReasonCode       uint64 `json:"reason_code"`
	RemainingDelay   int64  `json:"remaining_delay"`
	RetentionBudget  int64  `json:"retention_budget"`
	Scope            string `json:"scope"`
	StateCode        uint64 `json:"state_code"`
}

type controlReceiptWire struct {
	BindingSHA256  string `json:"binding_sha256"`
	OperationClass uint64 `json:"operation_class"`
	OperationID    string `json:"operation_id"`
	ResultCode     uint64 `json:"result_code"`
	Terminal       bool   `json:"terminal"`
}

type controlPublicationWire struct {
	OperationClass       uint64                  `json:"operation_class"`
	OperationID          string                  `json:"operation_id"`
	PreviousControlEpoch uint64                  `json:"previous_control_epoch"`
	PreviousGeneration   generationReferenceWire `json:"previous_generation"`
	StoreInstance        string                  `json:"store_instance"`
	TargetControlEpoch   uint64                  `json:"target_control_epoch"`
	TargetGeneration     generationReferenceWire `json:"target_generation"`
}

func cloneControlRecordV3(source controlRecordV3) controlRecordV3 {
	result := source
	result.storeInstance = bytes.Clone(source.storeInstance)
	result.associationLineage = bytes.Clone(source.associationLineage)
	result.tombstones = make([]controlTombstone, len(source.tombstones))
	for index, tombstone := range source.tombstones {
		result.tombstones[index] = tombstone
		result.tombstones[index].associationRef = bytes.Clone(tombstone.associationRef)
		result.tombstones[index].operationID = bytes.Clone(tombstone.operationID)
	}
	result.quarantines = make([]controlQuarantine, len(source.quarantines))
	for index, quarantine := range source.quarantines {
		result.quarantines[index] = quarantine
		result.quarantines[index].scope = bytes.Clone(quarantine.scope)
	}
	result.receipts = make([]controlReceipt, len(source.receipts))
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

func validateControlRecordBase(record controlRecordV3) error {
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

func decodeControlRecordFields(record *controlRecordV3, wire controlRecordWireV3) error {
	for index, item := range wire.Tombstones {
		reference, err := decodeControlOpaque(item.AssociationRef)
		if err != nil {
			return err
		}
		operationID, err := decodeControlOpaque(item.OperationID)
		if err != nil {
			return err
		}
		generation, err := decodeGenerationReference(item.EffectiveGeneration)
		if err != nil {
			return err
		}
		record.tombstones[index] = controlTombstone{associationRef: reference, effectiveGeneration: generation, operationID: operationID, revocationEpoch: item.RevocationEpoch}
	}
	for index, item := range wire.Quarantines {
		scope, err := decodeControlOpaque(item.Scope)
		if err != nil {
			return err
		}
		record.quarantines[index] = controlQuarantine{
			scope: scope, reasonCode: item.ReasonCode, stateCode: item.StateCode, attemptCount: item.AttemptCount,
			backoffStep: item.BackoffStep, remainingDelay: item.RemainingDelay, retentionBudget: item.RetentionBudget, lastControlEpoch: item.LastControlEpoch,
		}
	}
	for index, item := range wire.Receipts {
		operationID, err := decodeControlOpaque(item.OperationID)
		if err != nil {
			return err
		}
		binding, err := decodeControlOpaque(item.BindingSHA256)
		if err != nil {
			return err
		}
		record.receipts[index] = controlReceipt{operationID: operationID, operationClass: item.OperationClass, bindingSHA256: binding, resultCode: item.ResultCode, terminal: item.Terminal}
	}
	if wire.Publication != nil {
		operationID, err := decodeControlOpaque(wire.Publication.OperationID)
		if err != nil {
			return err
		}
		instance, err := decodeControlOpaque(wire.Publication.StoreInstance)
		if err != nil {
			return err
		}
		previous, err := decodeGenerationReference(wire.Publication.PreviousGeneration)
		if err != nil {
			return err
		}
		target, err := decodeGenerationReference(wire.Publication.TargetGeneration)
		if err != nil {
			return err
		}
		record.publication = &controlPublication{
			operationID: operationID, operationClass: wire.Publication.OperationClass, storeInstance: instance,
			previousControlEpoch: wire.Publication.PreviousControlEpoch, targetControlEpoch: wire.Publication.TargetControlEpoch,
			previousGeneration: previous, targetGeneration: target,
		}
	}
	return nil
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
