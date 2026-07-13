package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestG19ReplayFixtureIsDeterministicAndPublicSafe(t *testing.T) {
	payload, err := os.ReadFile("testdata/g19-replay-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var observation g19Observation
	if err := json.Unmarshal(payload, &observation); err != nil {
		t.Fatal(err)
	}
	result := evaluateG19(observation)
	if result.Status != resultPass {
		t.Fatalf("replay result = %+v", result)
	}
	const expected = "sha256:cd630f7c3685c0d29e4aaa395575873a9d3be0a3e18732f0bdf043eb128e83f0"
	if got := observation.FirstSPINEData.dataHash(); got != expected {
		t.Fatalf("SPINE replay hash = %s, want %s", got, expected)
	}
	if err := validateLiveRedaction(payload, ""); err != nil {
		t.Fatalf("fixture is not public-safe: %v", err)
	}
}

func TestProtectedInputsRequireRegularPrivateFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "remote-ski")
	if err := os.WriteFile(path, []byte("0123456789abcdef0123456789abcdef01234567\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readSecureTextFile(path); err == nil {
		t.Fatal("accepted group/world-readable protected input")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := readSecureTextFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if normalizeSKI(value) != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("unexpected protected input %q", value)
	}

	symlink := filepath.Join(dir, "remote-ski-link")
	if err := os.Symlink(path, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := readSecureTextFile(symlink); err == nil {
		t.Fatal("accepted symlink protected input")
	}
}

func TestOperatorProofRequiresPrivateKnownFields(t *testing.T) {
	dir := t.TempDir()
	validPath := filepath.Join(dir, "operator.json")
	valid := []byte(`{"lan_observer_confirmed":true,"trust_visible":true,"inbound_transport_observed":true,"owner_accepted":true,"accepted_at":"2026-07-13T21:05:00Z","evidence_ref":"sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee","transport_evidence_ref":"sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"}`)
	if err := os.WriteFile(validPath, valid, 0o600); err != nil {
		t.Fatal(err)
	}
	proof, err := readOperatorProof(validPath)
	if err != nil {
		t.Fatal(err)
	}
	if !proof.LANObserverConfirmed || !proof.TrustVisible || !proof.InboundTransportObserved || !proof.OwnerAccepted {
		t.Fatalf("unexpected proof: %+v", proof)
	}

	unknownPath := filepath.Join(dir, "operator-unknown.json")
	unknown := []byte(`{"lan_observer_confirmed":true,"trust_visible":true,"inbound_transport_observed":true,"owner_accepted":true,"accepted_at":"2026-07-13T21:05:00Z","evidence_ref":"sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee","transport_evidence_ref":"sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff","raw_ski":"forbidden"}`)
	if err := os.WriteFile(unknownPath, unknown, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readOperatorProof(unknownPath); err == nil {
		t.Fatal("accepted unknown operator proof field")
	}
}

func TestLiveHandlerAllowlistDeniesCompetingRemoteDeterministically(t *testing.T) {
	expected := "0123456789abcdef0123456789abcdef01234567"
	handler := &liveServiceHandler{
		expectedSKI: normalizeSKI(expected),
		denied:      make(map[string]struct{}),
	}
	if !handler.allowRemote(expected) {
		t.Fatal("allowlisted remote was denied")
	}
	competing := "fedcba9876543210fedcba9876543210fedcba98"
	if handler.allowRemote(competing) || handler.allowRemote(competing) {
		t.Fatal("competing remote was not deterministically denied")
	}
	if len(handler.denied) != 1 {
		t.Fatalf("denial ledger size = %d, want 1", len(handler.denied))
	}
}

func TestInboundStagesRequireCorrelatedExternalTransportEvidence(t *testing.T) {
	proof := operatorProofInput{
		InboundTransportObserved: true,
		TransportEvidenceRef:     "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
	}
	spine := passingG19Observation().FirstSPINEData
	if got := completedInboundStages(true, proof, spine); len(got) != len(requiredTransportStages) {
		t.Fatalf("correlated inbound stages = %v", got)
	}

	proof.InboundTransportObserved = false
	if got := completedInboundStages(true, proof, spine); len(got) != 1 || got[0] != transportStageFirstSPINEData {
		t.Fatalf("uncorrelated connection claimed inbound stages: %v", got)
	}
}
