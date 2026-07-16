package eebusfacade

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"
	"unicode/utf8"

	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusadmin"
)

const (
	firstTrustAdminVersion        = 1
	firstTrustMaximumCommandBytes = 24
	firstTrustMaximumNonceBytes   = 64
	firstTrustMaximumExpiryBytes  = 64
)

type firstTrustAdminEndpoint interface {
	Address() string
	Close() error
}

type firstTrustAdminCommand struct {
	name            string
	key             string
	duration        time.Duration
	fingerprint     string
	nonce           string
	expiresAt       time.Time
	connection      uint64
	storeGeneration uint64
	revocation      *firstTrustRevocationRequest
	repair          *firstTrustRepairRequest
}

type firstTrustAdminHandler struct {
	coordinator *firstTrustCoordinator
	random      io.Reader
}

type firstTrustAdminReply struct {
	Correlation    string `json:"correlation"`
	Outcome        string `json:"outcome"`
	RecoveryReason string `json:"recovery_reason,omitempty"`
	RecoveryState  string `json:"recovery_state,omitempty"`
	State          string `json:"state"`
}

type firstTrustCandidateReply struct {
	Fingerprint     string `json:"fingerprint_v1"`
	Nonce           string `json:"candidate_nonce"`
	ExpiresAt       string `json:"expires_at"`
	Connection      uint64 `json:"connection_generation"`
	StoreGeneration uint64 `json:"starting_store_generation"`
	Complete        bool   `json:"association_complete"`
}

func startFirstTrustAdmin(ctx context.Context, runtimeDir string, coordinator *firstTrustCoordinator) (firstTrustAdminEndpoint, error) {
	if coordinator == nil {
		return nil, errors.New("first_trust_coordinator_unavailable")
	}
	handler := &firstTrustAdminHandler{coordinator: coordinator, random: rand.Reader}
	return eebusadmin.Start(ctx, runtimeDir, handler.handle)
}

func (handler *firstTrustAdminHandler) handle(ctx context.Context, payload []byte) []byte {
	if handler == nil || handler.coordinator == nil {
		return handler.failure("mutation_disabled")
	}
	command, err := decodeFirstTrustAdminCommand(payload)
	if err != nil {
		return handler.failure("invalid_command")
	}
	switch command.name {
	case "open":
		return handler.reply(handler.coordinator.openPairingWindow(ctx, command.key, command.duration))
	case "close":
		return handler.reply(handler.coordinator.closePairingWindow(ctx, command.key))
	case "confirm":
		return handler.reply(handler.coordinator.confirm(ctx, command.key, command.fingerprint, command.nonce, command.expiresAt, command.connection, command.storeGeneration))
	case "cancel":
		return handler.reply(handler.coordinator.cancel(ctx, command.key, command.nonce, command.connection, command.storeGeneration))
	case "revoke_association":
		request, ok := command.revocationRequest()
		if !ok {
			return handler.failure("invalid_command")
		}
		return handler.reply(handler.coordinator.revoke(ctx, request))
	case "repair":
		request, ok := command.repairRequest()
		if !ok {
			return handler.failure("invalid_command")
		}
		return handler.reply(handler.coordinator.repair(ctx, request))
	case "status":
		return handler.reply("status")
	case "candidate":
		fingerprint, nonce, expiresAt, connection, storeGeneration, complete, ok := handler.coordinator.candidate()
		if !ok {
			return handler.reply("candidate_unavailable")
		}
		return marshalFirstTrustAdmin(firstTrustCandidateReply{
			Fingerprint:     fingerprint,
			Nonce:           nonce,
			ExpiresAt:       expiresAt.Format(time.RFC3339Nano),
			Connection:      connection,
			StoreGeneration: storeGeneration,
			Complete:        complete,
		})
	default:
		return handler.failure("invalid_command")
	}
}

func (handler *firstTrustAdminHandler) reply(outcome string) []byte {
	correlation, ok := handler.correlation()
	if !ok {
		return handler.failure("internal_error")
	}
	reply := firstTrustAdminReply{
		Correlation: correlation,
		Outcome:     outcome,
		State:       handler.coordinator.state(),
	}
	if handler.coordinator.recoveryStore != nil {
		reply.RecoveryState = handler.coordinator.recoveryState()
		reply.RecoveryReason = handler.coordinator.recoveryReason()
	}
	return marshalFirstTrustAdmin(reply)
}

func (handler *firstTrustAdminHandler) failure(outcome string) []byte {
	correlation, _ := handler.correlation()
	type failureReply struct {
		Correlation string `json:"correlation,omitempty"`
		Outcome     string `json:"outcome"`
	}
	return marshalFirstTrustAdmin(failureReply{Correlation: correlation, Outcome: outcome})
}

func (handler *firstTrustAdminHandler) correlation() (string, bool) {
	if handler == nil || handler.random == nil {
		return "", false
	}
	value := make([]byte, 16)
	if _, err := io.ReadFull(handler.random, value); err != nil {
		return "", false
	}
	return hex.EncodeToString(value), true
}

func decodeFirstTrustAdminCommand(payload []byte) (firstTrustAdminCommand, error) {
	fields, err := decodeFirstTrustAdminFields(payload)
	if err != nil {
		return firstTrustAdminCommand{}, err
	}
	version, err := decodeFirstTrustAdminUint(fields, "version")
	if err != nil || version != firstTrustAdminVersion {
		return firstTrustAdminCommand{}, errAdminCommand
	}
	name, err := decodeFirstTrustAdminString(fields, "command", firstTrustMaximumCommandBytes)
	if err != nil {
		return firstTrustAdminCommand{}, err
	}
	command := firstTrustAdminCommand{name: name}
	required := map[string]struct{}{"version": {}, "command": {}}
	switch name {
	case "open":
		command.key, err = decodeFirstTrustAdminString(fields, "idempotency_key", firstTrustMaximumKeyBytes)
		if err != nil {
			return firstTrustAdminCommand{}, err
		}
		milliseconds, decodeErr := decodeFirstTrustAdminUint(fields, "duration_milliseconds")
		if decodeErr != nil || milliseconds == 0 || milliseconds > uint64(firstTrustMaximumWindow/time.Millisecond) {
			return firstTrustAdminCommand{}, errAdminCommand
		}
		command.duration = time.Duration(milliseconds) * time.Millisecond
		required["idempotency_key"] = struct{}{}
		required["duration_milliseconds"] = struct{}{}
	case "close":
		command.key, err = decodeFirstTrustAdminString(fields, "idempotency_key", firstTrustMaximumKeyBytes)
		required["idempotency_key"] = struct{}{}
	case "confirm":
		err = decodeFirstTrustAdminBinding(fields, &command, true)
		for _, field := range []string{"idempotency_key", "fingerprint_v1", "candidate_nonce", "expires_at", "connection_generation", "starting_store_generation"} {
			required[field] = struct{}{}
		}
	case "cancel":
		err = decodeFirstTrustAdminBinding(fields, &command, false)
		for _, field := range []string{"idempotency_key", "candidate_nonce", "connection_generation", "starting_store_generation"} {
			required[field] = struct{}{}
		}
	case "revoke_association":
		request, decodeErr := decodeFirstTrustRevocationCommand(fields)
		err = decodeErr
		command.revocation = &request
		for _, field := range []string{
			"operation_id", "association_ref", "association_lineage", "expected_generation_sequence", "expected_generation_filename",
			"expected_generation_sha256", "expected_generation_schema", "expected_manifest_epoch", "expected_manifest_sha256", "expected_control_epoch",
		} {
			required[field] = struct{}{}
		}
	case "repair":
		request, repairFields, decodeErr := decodeFirstTrustRepairCommand(fields)
		err = decodeErr
		command.repair = &request
		for _, field := range repairFields {
			required[field] = struct{}{}
		}
	case "status", "candidate":
	default:
		return firstTrustAdminCommand{}, errAdminCommand
	}
	if err != nil || len(fields) != len(required) {
		return firstTrustAdminCommand{}, errAdminCommand
	}
	for field := range fields {
		if _, ok := required[field]; !ok {
			return firstTrustAdminCommand{}, errAdminCommand
		}
	}
	return command, nil
}

func (command firstTrustAdminCommand) revocationRequest() (firstTrustRevocationRequest, bool) {
	if command.revocation == nil {
		return firstTrustRevocationRequest{}, false
	}
	return *command.revocation, true
}

func (command firstTrustAdminCommand) repairRequest() (firstTrustRepairRequest, bool) {
	if command.repair == nil {
		return firstTrustRepairRequest{}, false
	}
	return *command.repair, true
}

func decodeFirstTrustRevocationCommand(fields map[string]json.RawMessage) (firstTrustRevocationRequest, error) {
	operationID, err := decodeFirstTrustAdminOpaque(fields, "operation_id")
	if err != nil {
		return firstTrustRevocationRequest{}, err
	}
	associationRef, err := decodeFirstTrustAdminOpaque(fields, "association_ref")
	if err != nil {
		return firstTrustRevocationRequest{}, err
	}
	lineage, err := decodeFirstTrustAdminOpaque(fields, "association_lineage")
	if err != nil {
		return firstTrustRevocationRequest{}, err
	}
	generation, err := decodeFirstTrustAdminGeneration(fields, "expected_generation")
	if err != nil {
		return firstTrustRevocationRequest{}, err
	}
	manifestEpoch, err := decodeFirstTrustAdminUint(fields, "expected_manifest_epoch")
	if err != nil || manifestEpoch == 0 {
		return firstTrustRevocationRequest{}, errAdminCommand
	}
	manifestDigest, err := decodeFirstTrustAdminOpaque(fields, "expected_manifest_sha256")
	if err != nil {
		return firstTrustRevocationRequest{}, err
	}
	controlEpoch, err := decodeFirstTrustAdminUint(fields, "expected_control_epoch")
	if err != nil || controlEpoch == 0 {
		return firstTrustRevocationRequest{}, errAdminCommand
	}
	return firstTrustRevocationRequest{
		operationID: operationID, associationRef: associationRef, associationLineage: lineage, expectedGeneration: generation,
		expectedManifestEpoch: manifestEpoch, expectedManifestSHA256: manifestDigest, expectedControlEpoch: controlEpoch,
	}, nil
}

func decodeFirstTrustRepairCommand(fields map[string]json.RawMessage) (firstTrustRepairRequest, []string, error) {
	required := []string{
		"operation_id", "repair_kind", "scope", "expected_state", "expected_reason", "expected_manifest_epoch", "expected_manifest_sha256",
		"expected_current_sequence", "expected_current_filename", "expected_current_sha256", "expected_current_schema", "expected_control_epoch",
		"expected_anchor_version", "expected_manifest_high_water", "expected_control_high_water", "next_repair_sequence",
	}
	operationID, err := decodeFirstTrustAdminOpaque(fields, "operation_id")
	if err != nil {
		return firstTrustRepairRequest{}, required, err
	}
	kind, err := decodeFirstTrustAdminString(fields, "repair_kind", 40)
	if err != nil || !firstTrustRepairKindAllowed(kind) {
		return firstTrustRepairRequest{}, required, errAdminCommand
	}
	scope, err := decodeFirstTrustAdminOpaque(fields, "scope")
	if err != nil {
		return firstTrustRepairRequest{}, required, err
	}
	state, err := decodeFirstTrustAdminString(fields, "expected_state", 32)
	if err != nil {
		return firstTrustRepairRequest{}, required, err
	}
	reason, err := decodeFirstTrustAdminOptionalString(fields, "expected_reason", 48)
	if err != nil {
		return firstTrustRepairRequest{}, required, err
	}
	epoch, err := decodeFirstTrustAdminUint(fields, "expected_manifest_epoch")
	if err != nil || epoch == 0 {
		return firstTrustRepairRequest{}, required, errAdminCommand
	}
	digest, err := decodeFirstTrustAdminOpaque(fields, "expected_manifest_sha256")
	if err != nil {
		return firstTrustRepairRequest{}, required, err
	}
	current, err := decodeFirstTrustAdminGeneration(fields, "expected_current")
	if err != nil {
		return firstTrustRepairRequest{}, required, err
	}
	parentNames := []string{"expected_parent_sequence", "expected_parent_filename", "expected_parent_sha256", "expected_parent_schema"}
	parentCount := 0
	for _, name := range parentNames {
		if _, ok := fields[name]; ok {
			parentCount++
		}
	}
	var parent *firstTrustGenerationBinding
	if parentCount != 0 {
		if parentCount != len(parentNames) {
			return firstTrustRepairRequest{}, required, errAdminCommand
		}
		decoded, decodeErr := decodeFirstTrustAdminGeneration(fields, "expected_parent")
		if decodeErr != nil {
			return firstTrustRepairRequest{}, required, decodeErr
		}
		parent = &decoded
		required = append(required, parentNames...)
	}
	controlEpoch, err := decodeFirstTrustAdminUint(fields, "expected_control_epoch")
	if err != nil || controlEpoch == 0 {
		return firstTrustRepairRequest{}, required, errAdminCommand
	}
	anchorVersion, err := decodeFirstTrustAdminUint(fields, "expected_anchor_version")
	if err != nil || anchorVersion == 0 {
		return firstTrustRepairRequest{}, required, errAdminCommand
	}
	manifestHighWater, err := decodeFirstTrustAdminUint(fields, "expected_manifest_high_water")
	if err != nil {
		return firstTrustRepairRequest{}, required, err
	}
	controlHighWater, err := decodeFirstTrustAdminUint(fields, "expected_control_high_water")
	if err != nil {
		return firstTrustRepairRequest{}, required, err
	}
	nextSequence, err := decodeFirstTrustAdminUint(fields, "next_repair_sequence")
	if err != nil || nextSequence == 0 {
		return firstTrustRepairRequest{}, required, errAdminCommand
	}
	return firstTrustRepairRequest{
		operationID: operationID, kind: kind, scope: scope, expectedState: state, expectedReason: reason,
		expectedManifest:     firstTrustManifestBinding{epoch: epoch, sha256: digest, current: current, parent: parent},
		expectedControlEpoch: controlEpoch, expectedAnchorVersion: anchorVersion, expectedManifestHighWater: manifestHighWater,
		expectedControlHighWater: controlHighWater, nextRepairSequence: nextSequence,
	}, required, nil
}

func decodeFirstTrustAdminGeneration(fields map[string]json.RawMessage, prefix string) (firstTrustGenerationBinding, error) {
	sequence, err := decodeFirstTrustAdminUint(fields, prefix+"_sequence")
	if err != nil || sequence == 0 {
		return firstTrustGenerationBinding{}, errAdminCommand
	}
	filename, err := decodeFirstTrustAdminString(fields, prefix+"_filename", 128)
	if err != nil {
		return firstTrustGenerationBinding{}, err
	}
	digest, err := decodeFirstTrustAdminOpaque(fields, prefix+"_sha256")
	if err != nil {
		return firstTrustGenerationBinding{}, err
	}
	schema, err := decodeFirstTrustAdminUint(fields, prefix+"_schema")
	if err != nil || schema == 0 {
		return firstTrustGenerationBinding{}, errAdminCommand
	}
	return firstTrustGenerationBinding{sequence: sequence, filename: filename, sha256: digest, schemaVersion: schema}, nil
}

func decodeFirstTrustAdminOpaque(fields map[string]json.RawMessage, key string) ([32]byte, error) {
	var result [32]byte
	encoded, err := decodeFirstTrustAdminString(fields, key, 64)
	if err != nil || len(encoded) != 64 {
		return result, errAdminCommand
	}
	decoded, err := hex.DecodeString(encoded)
	if err != nil || hex.EncodeToString(decoded) != encoded || len(decoded) != len(result) {
		return result, errAdminCommand
	}
	copy(result[:], decoded)
	if result == [32]byte{} {
		return result, errAdminCommand
	}
	return result, nil
}

func decodeFirstTrustAdminOptionalString(fields map[string]json.RawMessage, key string, maximum int) (string, error) {
	raw, ok := fields[key]
	if !ok {
		return "", errAdminCommand
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || len(value) > maximum {
		return "", errAdminCommand
	}
	return value, nil
}

func decodeFirstTrustAdminBinding(fields map[string]json.RawMessage, command *firstTrustAdminCommand, includeFingerprint bool) error {
	var err error
	command.key, err = decodeFirstTrustAdminString(fields, "idempotency_key", firstTrustMaximumKeyBytes)
	if err != nil {
		return err
	}
	command.nonce, err = decodeFirstTrustAdminString(fields, "candidate_nonce", firstTrustMaximumNonceBytes)
	if err != nil {
		return err
	}
	command.connection, err = decodeFirstTrustAdminUint(fields, "connection_generation")
	if err != nil || command.connection == 0 {
		return errAdminCommand
	}
	command.storeGeneration, err = decodeFirstTrustAdminUint(fields, "starting_store_generation")
	if err != nil || command.storeGeneration == 0 {
		return errAdminCommand
	}
	if !includeFingerprint {
		return nil
	}
	command.fingerprint, err = decodeFirstTrustAdminString(fields, "fingerprint_v1", 40)
	if err != nil {
		return err
	}
	expiry, err := decodeFirstTrustAdminString(fields, "expires_at", firstTrustMaximumExpiryBytes)
	if err != nil {
		return err
	}
	command.expiresAt, err = time.Parse(time.RFC3339Nano, expiry)
	if err != nil {
		return errAdminCommand
	}
	return nil
}

var errAdminCommand = &firstTrustAdminDecodeError{}

type firstTrustAdminDecodeError struct{}

func (*firstTrustAdminDecodeError) Error() string { return "invalid_command" }

func decodeFirstTrustAdminFields(payload []byte) (map[string]json.RawMessage, error) {
	if !utf8.Valid(payload) {
		return nil, errAdminCommand
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errAdminCommand
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, errAdminCommand
		}
		key, ok := keyToken.(string)
		if !ok || len(key) == 0 || len(key) > 64 {
			return nil, errAdminCommand
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, errAdminCommand
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, errAdminCommand
		}
		fields[key] = value
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return nil, errAdminCommand
	}
	if token, err := decoder.Token(); err != io.EOF || token != nil {
		return nil, errAdminCommand
	}
	return fields, nil
}

func decodeFirstTrustAdminString(fields map[string]json.RawMessage, key string, maximum int) (string, error) {
	raw, ok := fields[key]
	if !ok {
		return "", errAdminCommand
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || len(value) == 0 || len(value) > maximum {
		return "", errAdminCommand
	}
	return value, nil
}

func decodeFirstTrustAdminUint(fields map[string]json.RawMessage, key string) (uint64, error) {
	raw, ok := fields[key]
	if !ok {
		return 0, errAdminCommand
	}
	var value uint64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, errAdminCommand
	}
	return value, nil
}

func marshalFirstTrustAdmin(value any) []byte {
	payload, err := json.Marshal(value)
	if err != nil {
		return []byte(`{"outcome":"internal_error"}`)
	}
	return payload
}
