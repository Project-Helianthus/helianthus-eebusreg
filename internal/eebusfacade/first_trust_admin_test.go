package eebusfacade

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestFirstTrustAdminCommandsAreStrictRedactedAndPrivilegedReadOnly(t *testing.T) {
	fixture := newMSP04BFixture(t, "commit_durable")
	coordinator, ok := fixture.coordinator.(*firstTrustCoordinator)
	if !ok {
		t.Fatal("fixture coordinator type changed")
	}
	handler := &firstTrustAdminHandler{coordinator: coordinator, random: rand.Reader}
	windowKey := msp04bLabel(t)
	openReply := callMSP04BAdmin(t, handler, map[string]any{
		"version":               1,
		"command":               "open",
		"idempotency_key":       windowKey,
		"duration_milliseconds": 60_000,
	})
	if adminString(t, openReply, "outcome") != "open_empty" || adminString(t, openReply, "state") != "OPEN_EMPTY" {
		t.Fatal("open reply did not expose only the expected outcome and state")
	}
	assertMSP04BOrdinaryAdminRedacted(t, openReply)

	remote := msp04bRemote(t)
	if got := fixture.coordinator.admit(remote, 131); got != "candidate_pending" {
		t.Fatalf("admit outcome = %q", got)
	}
	fixture.coordinator.serviceShipIDUpdate(remote, 131, msp04bLabel(t))
	candidateReply := callMSP04BAdmin(t, handler, map[string]any{"version": 1, "command": "candidate"})
	allowedCandidateFields := map[string]struct{}{
		"fingerprint_v1":            {},
		"candidate_nonce":           {},
		"expires_at":                {},
		"connection_generation":     {},
		"starting_store_generation": {},
		"association_complete":      {},
	}
	if len(candidateReply) != len(allowedCandidateFields) {
		t.Fatal("privileged candidate reply field count changed")
	}
	for field := range candidateReply {
		if _, allowed := allowedCandidateFields[field]; !allowed {
			t.Fatal("privileged candidate reply exposed a forbidden field")
		}
	}
	if adminString(t, candidateReply, "fingerprint_v1") != hex.EncodeToString(remote) {
		t.Fatal("privileged candidate fingerprint was not exact")
	}
	if complete := adminBool(t, candidateReply, "association_complete"); !complete {
		t.Fatal("privileged candidate reply lost association completion")
	}

	malformed := handler.handle(context.Background(), []byte(`{"version":1,"version":1,"command":"status"}`))
	var malformedReply map[string]json.RawMessage
	if err := json.Unmarshal(malformed, &malformedReply); err != nil || adminString(t, malformedReply, "outcome") != "invalid_command" {
		t.Fatal("duplicate command field was not rejected")
	}
	if _, exists := malformedReply["state"]; exists {
		t.Fatal("malformed command reached coordinator status")
	}
	unknownReply := callMSP04BAdmin(t, handler, map[string]any{"version": 1, "command": "status", "unknown": true})
	if adminString(t, unknownReply, "outcome") != "invalid_command" {
		t.Fatal("unknown command field was not rejected")
	}
	assertMSP04BCommitCount(t, fixture.store, 0)

	fingerprint, nonce, expiresAt, connection, storeGeneration, _, ok := fixture.coordinator.candidate()
	if !ok {
		t.Fatal("candidate disappeared")
	}
	wrongReply := callMSP04BAdmin(t, handler, map[string]any{
		"version":                   1,
		"command":                   "confirm",
		"idempotency_key":           msp04bLabel(t),
		"fingerprint_v1":            strings.ToUpper(fingerprint),
		"candidate_nonce":           nonce,
		"expires_at":                expiresAt.Format(time.RFC3339Nano),
		"connection_generation":     connection,
		"starting_store_generation": storeGeneration,
	})
	if adminString(t, wrongReply, "outcome") != "confirmation_mismatch" {
		t.Fatal("non-normalized fingerprint was not rejected")
	}
	assertMSP04BCommitCount(t, fixture.store, 0)

	confirmKey := msp04bLabel(t)
	confirmCommand := map[string]any{
		"version":                   1,
		"command":                   "confirm",
		"idempotency_key":           confirmKey,
		"fingerprint_v1":            fingerprint,
		"candidate_nonce":           nonce,
		"expires_at":                expiresAt.Format(time.RFC3339Nano),
		"connection_generation":     connection,
		"starting_store_generation": storeGeneration,
	}
	confirmReply := callMSP04BAdmin(t, handler, confirmCommand)
	if adminString(t, confirmReply, "outcome") != "trusted" {
		t.Fatal("exact private confirmation was not trusted")
	}
	assertMSP04BOrdinaryAdminRedacted(t, confirmReply)
	replayReply := callMSP04BAdmin(t, handler, confirmCommand)
	if adminString(t, replayReply, "outcome") != "trusted" {
		t.Fatal("identical terminal replay changed outcome")
	}
	assertMSP04BCommitCount(t, fixture.store, 1)
}

func callMSP04BAdmin(t *testing.T, handler *firstTrustAdminHandler, command map[string]any) map[string]json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	response := handler.handle(context.Background(), payload)
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(response, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func adminString(t *testing.T, fields map[string]json.RawMessage, key string) string {
	t.Helper()
	var value string
	if err := json.Unmarshal(fields[key], &value); err != nil {
		t.Fatal("admin string field is malformed")
	}
	return value
}

func adminBool(t *testing.T, fields map[string]json.RawMessage, key string) bool {
	t.Helper()
	var value bool
	if err := json.Unmarshal(fields[key], &value); err != nil {
		t.Fatal("admin boolean field is malformed")
	}
	return value
}

func assertMSP04BOrdinaryAdminRedacted(t *testing.T, fields map[string]json.RawMessage) {
	t.Helper()
	allowed := map[string]struct{}{"correlation": {}, "outcome": {}, "state": {}}
	if len(fields) != len(allowed) {
		t.Fatal("ordinary admin reply field count changed")
	}
	for field := range fields {
		if _, ok := allowed[field]; !ok {
			t.Fatal("ordinary admin reply exposed a forbidden field")
		}
	}
}
