package eebusstore

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	maxControlAttemptCount     = 128
	maxControlAttemptStateCode = 2
	maxControlEndpointHost     = 255
	maxControlAttemptPath      = 1024
)

type controlRecordV3 struct {
	storeInstance      []byte
	controlEpoch       uint64
	associationLineage []byte
	tombstones         []controlTombstoneV2
	quarantines        []controlQuarantineV2
	receipts           []controlReceiptV2
	attempts           []controlAttemptV3
	operationHighWater uint64
	repairSequence     uint64
	publication        *controlPublicationV2
}

type controlAttemptV3 struct {
	stateCode              uint64
	attemptID              []byte
	remoteSKI              []byte
	scope                  []byte
	controlEpoch           uint64
	associationLineage     []byte
	endpointHost           string
	endpointPort           uint16
	path                   string
	cancellationGeneration uint64
	reservationOrder       uint64
	reservationTimestamp   int64
	attemptCountBefore     uint64
}

type controlRecordWireV3 struct {
	AssociationLineage string                    `json:"association_lineage"`
	Attempts           []controlAttemptWireV3    `json:"attempts"`
	ControlEpoch       uint64                    `json:"control_epoch"`
	OperationHighWater uint64                    `json:"operation_high_water"`
	Publication        *controlPublicationWireV2 `json:"publication"`
	Quarantines        []controlQuarantineWireV2 `json:"quarantines"`
	Receipts           []controlReceiptWireV2    `json:"receipts"`
	RepairSequence     uint64                    `json:"repair_sequence"`
	StoreInstance      string                    `json:"store_instance"`
	Tombstones         []controlTombstoneWireV2  `json:"tombstones"`
}

type controlAttemptWireV3 struct {
	AssociationLineage     string `json:"association_lineage"`
	AttemptCountBefore     uint64 `json:"attempt_count_before"`
	AttemptID              string `json:"attempt_id"`
	CancellationGeneration uint64 `json:"cancellation_generation"`
	ControlEpoch           uint64 `json:"control_epoch"`
	EndpointHost           string `json:"endpoint_host"`
	EndpointPort           uint16 `json:"endpoint_port"`
	Path                   string `json:"path"`
	RemoteSKI              string `json:"remote_ski"`
	ReservationOrder       uint64 `json:"reservation_order"`
	ReservationTimestamp   int64  `json:"reservation_timestamp"`
	Scope                  string `json:"scope"`
	StateCode              uint64 `json:"state_code"`
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

func validControlRecordV2Envelope(payload []byte) bool {
	_, err := decodeControlRecordV2(payload)
	return err == nil
}

func cloneControlRecordV3(source controlRecordV3) controlRecordV3 {
	result := controlRecordV3FromV2(controlRecordV2FromV3(source))
	result.attempts = make([]controlAttemptV3, len(source.attempts))
	for index, attempt := range source.attempts {
		result.attempts[index] = cloneControlAttemptV3(attempt)
	}
	return result
}

func cloneControlAttemptV3(source controlAttemptV3) controlAttemptV3 {
	result := source
	result.attemptID = bytes.Clone(source.attemptID)
	result.remoteSKI = bytes.Clone(source.remoteSKI)
	result.scope = bytes.Clone(source.scope)
	result.associationLineage = bytes.Clone(source.associationLineage)
	return result
}

func controlRecordV3FromV2(source controlRecordV2) controlRecordV3 {
	cloned := cloneControlRecordV2(source)
	return controlRecordV3{
		storeInstance: cloned.storeInstance, controlEpoch: cloned.controlEpoch,
		associationLineage: cloned.associationLineage, tombstones: cloned.tombstones,
		quarantines: cloned.quarantines, receipts: cloned.receipts,
		operationHighWater: cloned.operationHighWater, repairSequence: cloned.repairSequence,
		publication: cloned.publication,
	}
}

func controlRecordV2FromV3(source controlRecordV3) controlRecordV2 {
	return cloneControlRecordV2(controlRecordV2{
		storeInstance: source.storeInstance, controlEpoch: source.controlEpoch,
		associationLineage: source.associationLineage, tombstones: source.tombstones,
		quarantines: source.quarantines, receipts: source.receipts,
		operationHighWater: source.operationHighWater, repairSequence: source.repairSequence,
		publication: source.publication,
	})
}

func validateControlRecordV3(record controlRecordV3) error {
	if err := validateControlRecordV2(controlRecordV2FromV3(record)); err != nil {
		return err
	}
	if len(record.attempts) > maxControlAttemptCount {
		return malformed("validate_control", errors.New("attempt bounds"))
	}
	identities := make(map[string]struct{}, len(record.attempts))
	scopes := make(map[string]struct{}, len(record.attempts))
	orders := make(map[uint64]struct{}, len(record.attempts))
	for _, attempt := range record.attempts {
		if attempt.stateCode == 0 || attempt.stateCode > maxControlAttemptStateCode ||
			len(attempt.attemptID) != controlOpaqueBytes || len(attempt.remoteSKI) != 20 ||
			len(attempt.scope) != controlOpaqueBytes || attempt.controlEpoch == 0 || attempt.controlEpoch > record.controlEpoch ||
			len(attempt.associationLineage) != controlOpaqueBytes ||
			!validControlEndpointHost(attempt.endpointHost) || attempt.endpointPort == 0 || !validControlAttemptPath(attempt.path) ||
			attempt.cancellationGeneration == 0 || attempt.reservationOrder == 0 || attempt.reservationTimestamp < 0 ||
			attempt.attemptCountBefore > maxControlQuarantineValue {
			return malformed("validate_control", errors.New("attempt"))
		}
		identity := string(attempt.attemptID)
		if _, exists := identities[identity]; exists {
			return malformed("validate_control", errors.New("duplicate attempt"))
		}
		identities[identity] = struct{}{}
		scope := string(attempt.scope)
		if _, exists := scopes[scope]; exists {
			return malformed("validate_control", errors.New("duplicate attempt scope"))
		}
		scopes[scope] = struct{}{}
		if _, exists := orders[attempt.reservationOrder]; exists {
			return malformed("validate_control", errors.New("duplicate reservation order"))
		}
		orders[attempt.reservationOrder] = struct{}{}
	}
	return nil
}

func validControlEndpointHost(value string) bool {
	return value != "" && len(value) <= maxControlEndpointHost && utf8.ValidString(value) && strings.TrimSpace(value) == value && !strings.ContainsRune(value, '\x00')
}

func validControlAttemptPath(value string) bool {
	return len(value) <= maxControlAttemptPath && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00') && (value == "" || strings.HasPrefix(value, "/"))
}

func encodeControlRecordV3(record controlRecordV3) ([]byte, error) {
	if err := validateControlRecordV3(record); err != nil {
		return nil, err
	}
	return encodeControlRecordV3Unchecked(record)
}

func encodeControlRecordV3Unchecked(record controlRecordV3) ([]byte, error) {
	record = cloneControlRecordV3(record)
	sort.Slice(record.attempts, func(left, right int) bool {
		if record.attempts[left].reservationOrder != record.attempts[right].reservationOrder {
			return record.attempts[left].reservationOrder < record.attempts[right].reservationOrder
		}
		return bytes.Compare(record.attempts[left].attemptID, record.attempts[right].attemptID) < 0
	})
	wire := controlRecordWireV3{
		AssociationLineage: base64.StdEncoding.EncodeToString(record.associationLineage),
		Attempts:           make([]controlAttemptWireV3, len(record.attempts)),
		ControlEpoch:       record.controlEpoch, OperationHighWater: record.operationHighWater,
		Quarantines: make([]controlQuarantineWireV2, len(record.quarantines)),
		Receipts:    make([]controlReceiptWireV2, len(record.receipts)), RepairSequence: record.repairSequence,
		StoreInstance: base64.StdEncoding.EncodeToString(record.storeInstance),
		Tombstones:    make([]controlTombstoneWireV2, len(record.tombstones)),
	}
	for index, attempt := range record.attempts {
		wire.Attempts[index] = controlAttemptWireV3{
			AssociationLineage:     base64.StdEncoding.EncodeToString(attempt.associationLineage),
			AttemptCountBefore:     attempt.attemptCountBefore,
			AttemptID:              base64.StdEncoding.EncodeToString(attempt.attemptID),
			CancellationGeneration: attempt.cancellationGeneration, ControlEpoch: attempt.controlEpoch,
			EndpointHost: attempt.endpointHost, EndpointPort: attempt.endpointPort, Path: attempt.path,
			RemoteSKI: base64.StdEncoding.EncodeToString(attempt.remoteSKI), ReservationOrder: attempt.reservationOrder,
			ReservationTimestamp: attempt.reservationTimestamp, Scope: base64.StdEncoding.EncodeToString(attempt.scope), StateCode: attempt.stateCode,
		}
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
		attempts: make([]controlAttemptV3, len(wire.Attempts)), tombstones: make([]controlTombstoneV2, len(wire.Tombstones)),
		quarantines: make([]controlQuarantineV2, len(wire.Quarantines)), receipts: make([]controlReceiptV2, len(wire.Receipts)),
		operationHighWater: wire.OperationHighWater, repairSequence: wire.RepairSequence,
	}
	for index, item := range wire.Attempts {
		attemptID, decodeErr := decodeControlOpaque(item.AttemptID)
		if decodeErr != nil {
			return controlRecordV3{}, decodeErr
		}
		remoteSKI, decodeErr := decodeCanonicalBase64(item.RemoteSKI, 20, 20)
		if decodeErr != nil {
			return controlRecordV3{}, decodeErr
		}
		scope, decodeErr := decodeControlOpaque(item.Scope)
		if decodeErr != nil {
			return controlRecordV3{}, decodeErr
		}
		attemptLineage, decodeErr := decodeControlOpaque(item.AssociationLineage)
		if decodeErr != nil {
			return controlRecordV3{}, decodeErr
		}
		record.attempts[index] = controlAttemptV3{
			stateCode: item.StateCode, attemptID: attemptID, remoteSKI: remoteSKI, scope: scope,
			controlEpoch: item.ControlEpoch, associationLineage: attemptLineage,
			endpointHost: item.EndpointHost, endpointPort: item.EndpointPort, path: item.Path,
			cancellationGeneration: item.CancellationGeneration, reservationOrder: item.ReservationOrder,
			reservationTimestamp: item.ReservationTimestamp, attemptCountBefore: item.AttemptCountBefore,
		}
	}
	legacy, err := decodeControlRecordV2Parts(wire)
	if err != nil {
		return controlRecordV3{}, err
	}
	record.tombstones, record.quarantines, record.receipts, record.publication = legacy.tombstones, legacy.quarantines, legacy.receipts, legacy.publication
	if err := validateControlRecordV3(record); err != nil {
		return controlRecordV3{}, err
	}
	canonical, err := encodeControlRecordV3Unchecked(record)
	if err != nil || !bytes.Equal(canonical, payload) {
		return controlRecordV3{}, malformed("decode_control", errors.New("noncanonical bytes"))
	}
	return record, nil
}

func decodeControlRecordV2Parts(wire controlRecordWireV3) (controlRecordV2, error) {
	legacyWire := controlRecordWireV2{
		AssociationLineage: wire.AssociationLineage, ControlEpoch: wire.ControlEpoch,
		OperationHighWater: wire.OperationHighWater, Publication: wire.Publication,
		Quarantines: wire.Quarantines, Receipts: wire.Receipts, RepairSequence: wire.RepairSequence,
		StoreInstance: wire.StoreInstance, Tombstones: wire.Tombstones,
	}
	payload, err := json.Marshal(legacyWire)
	if err != nil {
		return controlRecordV2{}, malformed("decode_control", err)
	}
	return decodeControlRecordV2(payload)
}

func migrateMSP04CStateToMSP04CR2(source stateV1) (stateV1, error) {
	result := cloneStateV1(source)
	if len(result.controlEnvelope) == 0 {
		return result, nil
	}
	legacy, err := decodeControlRecordV2(result.controlEnvelope)
	if err != nil {
		return stateV1{}, err
	}
	payload, err := encodeControlRecordV3(controlRecordV3FromV2(legacy))
	if err != nil {
		return stateV1{}, err
	}
	result.controlEnvelope = payload
	return result, nil
}
