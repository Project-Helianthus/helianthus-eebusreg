package eebusstore

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
)

type controlRecordV3 struct {
	storeInstance      []byte
	controlEpoch       uint64
	associationLineage []byte
	tombstones         []controlTombstone
	quarantines        []controlQuarantine
	receipts           []controlReceipt
	operationHighWater uint64
	repairSequence     uint64
	publication        *controlPublication
}

type controlRecordWireV3 struct {
	AssociationLineage string                  `json:"association_lineage"`
	Attempts           []json.RawMessage       `json:"attempts"`
	ControlEpoch       uint64                  `json:"control_epoch"`
	OperationHighWater uint64                  `json:"operation_high_water"`
	Publication        *controlPublicationWire `json:"publication"`
	Quarantines        []controlQuarantineWire `json:"quarantines"`
	Receipts           []controlReceiptWire    `json:"receipts"`
	RepairSequence     uint64                  `json:"repair_sequence"`
	StoreInstance      string                  `json:"store_instance"`
	Tombstones         []controlTombstoneWire  `json:"tombstones"`
}

func withControlRecordV3(source stateV1, record controlRecordV3) stateV1 {
	result := cloneStateV1(source)
	payload, _ := encodeControlRecordV3Unchecked(record)
	result.controlEnvelope = payload
	return result
}

func controlRecordV3FromStateV1(source stateV1) (controlRecordV3, bool) {
	if len(source.controlEnvelope) == 0 {
		return controlRecordV3{}, false
	}
	record, err := decodeControlRecordV3(source.controlEnvelope)
	return record, err == nil
}

func validControlRecordV3Envelope(payload []byte) bool {
	_, err := decodeControlRecordV3(payload)
	return err == nil
}

func validateControlRecordV3(record controlRecordV3) error {
	return validateControlRecordBase(record)
}

func encodeControlRecordV3(record controlRecordV3) ([]byte, error) {
	if err := validateControlRecordV3(record); err != nil {
		return nil, err
	}
	return encodeControlRecordV3Unchecked(record)
}

func encodeControlRecordV3Unchecked(record controlRecordV3) ([]byte, error) {
	record = cloneControlRecordV3(record)
	wire := controlRecordWireV3{
		AssociationLineage: base64.StdEncoding.EncodeToString(record.associationLineage),
		Attempts:           []json.RawMessage{},
		ControlEpoch:       record.controlEpoch, OperationHighWater: record.operationHighWater,
		Quarantines: make([]controlQuarantineWire, len(record.quarantines)),
		Receipts:    make([]controlReceiptWire, len(record.receipts)), RepairSequence: record.repairSequence,
		StoreInstance: base64.StdEncoding.EncodeToString(record.storeInstance),
		Tombstones:    make([]controlTombstoneWire, len(record.tombstones)),
	}
	for index, tombstone := range record.tombstones {
		wire.Tombstones[index] = controlTombstoneWire{
			AssociationRef:      base64.StdEncoding.EncodeToString(tombstone.associationRef),
			EffectiveGeneration: generationReferenceToWire(tombstone.effectiveGeneration),
			OperationID:         base64.StdEncoding.EncodeToString(tombstone.operationID), RevocationEpoch: tombstone.revocationEpoch,
		}
	}
	for index, quarantine := range record.quarantines {
		wire.Quarantines[index] = controlQuarantineWire{
			AttemptCount: quarantine.attemptCount, BackoffStep: quarantine.backoffStep, LastControlEpoch: quarantine.lastControlEpoch,
			ReasonCode: quarantine.reasonCode, RemainingDelay: quarantine.remainingDelay, RetentionBudget: quarantine.retentionBudget,
			Scope: base64.StdEncoding.EncodeToString(quarantine.scope), StateCode: quarantine.stateCode,
		}
	}
	for index, receipt := range record.receipts {
		wire.Receipts[index] = controlReceiptWire{
			BindingSHA256: base64.StdEncoding.EncodeToString(receipt.bindingSHA256), OperationClass: receipt.operationClass,
			OperationID: base64.StdEncoding.EncodeToString(receipt.operationID), ResultCode: receipt.resultCode, Terminal: receipt.terminal,
		}
	}
	if record.publication != nil {
		wire.Publication = &controlPublicationWire{
			OperationClass: record.publication.operationClass, OperationID: base64.StdEncoding.EncodeToString(record.publication.operationID),
			PreviousControlEpoch: record.publication.previousControlEpoch, PreviousGeneration: generationReferenceToWire(record.publication.previousGeneration),
			StoreInstance: base64.StdEncoding.EncodeToString(record.publication.storeInstance), TargetControlEpoch: record.publication.targetControlEpoch,
			TargetGeneration: generationReferenceToWire(record.publication.targetGeneration),
		}
	}
	return json.Marshal(wire)
}

func decodeControlRecordV3(payload []byte) (controlRecordV3, error) {
	var wire controlRecordWireV3
	if err := decodeClosedJSON(payload, &wire); err != nil {
		return controlRecordV3{}, malformed("decode_control", err)
	}
	storeInstance, err := decodeControlOpaque(wire.StoreInstance)
	if err != nil {
		return controlRecordV3{}, err
	}
	lineage, err := decodeControlOpaque(wire.AssociationLineage)
	if err != nil {
		return controlRecordV3{}, err
	}
	record := controlRecordV3{
		storeInstance: storeInstance, controlEpoch: wire.ControlEpoch, associationLineage: lineage,
		tombstones:  make([]controlTombstone, len(wire.Tombstones)),
		quarantines: make([]controlQuarantine, len(wire.Quarantines)), receipts: make([]controlReceipt, len(wire.Receipts)),
		operationHighWater: wire.OperationHighWater, repairSequence: wire.RepairSequence,
	}
	if len(wire.Attempts) != 0 {
		return controlRecordV3{}, malformed("decode_control", errors.New("attempts must be empty"))
	}
	if err := decodeControlRecordFields(&record, wire); err != nil {
		return controlRecordV3{}, err
	}
	if err := validateControlRecordV3(record); err != nil {
		return controlRecordV3{}, err
	}
	canonical, err := encodeControlRecordV3Unchecked(record)
	if err != nil || !bytes.Equal(canonical, payload) {
		return controlRecordV3{}, malformed("decode_control", errors.New("noncanonical bytes"))
	}
	return record, nil
}
