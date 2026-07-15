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
}

type firstTrustAdminHandler struct {
	coordinator *firstTrustCoordinator
	random      io.Reader
}

type firstTrustAdminReply struct {
	Correlation string `json:"correlation"`
	Outcome     string `json:"outcome"`
	State       string `json:"state"`
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
	return marshalFirstTrustAdmin(firstTrustAdminReply{
		Correlation: correlation,
		Outcome:     outcome,
		State:       handler.coordinator.state(),
	})
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
