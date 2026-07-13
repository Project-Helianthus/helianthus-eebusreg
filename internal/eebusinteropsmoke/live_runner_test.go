package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/enbility/ship-go/cert"
)

func TestG19ReplayFixtureIsDeterministicAndPublicSafe(t *testing.T) {
	payload, err := os.ReadFile("testdata/g19-replay-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, g19ReplayFixture) {
		t.Fatal("runtime replay payload differs from the checked-in fixture")
	}
	var observation g19Observation
	if err := json.Unmarshal(payload, &observation); err != nil {
		t.Fatal(err)
	}
	result := evaluateG19(observation)
	if result.Status != resultPass {
		t.Fatalf("replay result = %+v", result)
	}
	const expected = "sha256:5966b10a61c3fb617000ab33a3122823fa253d6e1d41a468ed346e109dc55450"
	if got := observation.FirstSPINEData.dataHash(); got != expected {
		t.Fatalf("SPINE replay hash = %s, want %s", got, expected)
	}
	if err := validateLiveRedaction(payload); err != nil {
		t.Fatalf("fixture is not public-safe: %v", err)
	}
}

func TestExpectedWithdrawalRequiresExactPTRServiceWithTTLZero(t *testing.T) {
	expected := "Helianthus._ship._tcp.local."
	records := []mdnsRecord{
		{Name: "_ship._tcp.local.", Type: 12, TTL: 0, Value: expected},
		{Name: "_ship._tcp.local.", Type: 12, TTL: 120, Value: expected},
		{Name: expected, Type: 33, TTL: 0, Value: expected},
		{Name: "_ship._tcp.local.", Type: 12, TTL: 0, Value: "Other._ship._tcp.local."},
	}
	var discovery liveDiscovery
	accountMDNSRecords(&discovery, records, expected)
	if discovery.ExpectedGoodbye != 1 || discovery.ExpectedActive != 1 {
		t.Fatalf("exact service counts = %+v, want one active and one goodbye", discovery)
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
	} else if err.Error() != "protected input must be a regular non-symlink file" {
		t.Fatalf("unstable symlink error: %v", err)
	}

	directory := filepath.Join(dir, "directory")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := readSecureTextFile(directory); err == nil || err.Error() != "protected input must be a regular non-symlink file" {
		t.Fatalf("directory was not rejected through fstat: %v", err)
	}
}

func TestOperatorProofRequiresPrivateKnownFields(t *testing.T) {
	dir := t.TempDir()
	validPath := filepath.Join(dir, "operator.json")
	valid, err := json.Marshal(operatorProofInput{
		LANObserverConfirmed:     true,
		TrustVisible:             true,
		InboundTransportObserved: true,
		OwnerAccepted:            true,
		RunNonce:                 strings.Repeat("a", 64),
		RunRef:                   "sha256:" + strings.Repeat("b", 64),
		ChallengeRef:             "hmac-sha256:" + strings.Repeat("c", 64),
		ExpectedRemoteDigest:     "hmac-sha256:" + strings.Repeat("d", 64),
		InterfaceRef:             "hmac-sha256:" + strings.Repeat("e", 64),
		PortRef:                  "hmac-sha256:" + strings.Repeat("f", 64),
		ConnectionGenerationRef:  "hmac-sha256:" + strings.Repeat("0", 64),
		ChallengeIssuedAt:        time.Date(2026, 7, 13, 21, 3, 0, 0, time.UTC),
		FirstSPINECapturedAt:     time.Date(2026, 7, 13, 21, 2, 0, 0, time.UTC),
		RunStartedAt:             time.Date(2026, 7, 13, 21, 0, 0, 0, time.UTC),
		RunExpiresAt:             time.Date(2026, 7, 13, 21, 10, 0, 0, time.UTC),
		ObservedAt:               time.Date(2026, 7, 13, 21, 4, 0, 0, time.UTC),
		AcceptedAt:               time.Date(2026, 7, 13, 21, 5, 0, 0, time.UTC),
		EvidenceRef:              "sha256:" + strings.Repeat("1", 64),
		TransportHash:            "sha256:" + strings.Repeat("2", 64),
		FirstSPINEHash:           "sha256:" + strings.Repeat("3", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
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
	unknown := append(bytes.TrimSuffix(valid, []byte("}")), []byte(`,"raw_ski":"forbidden"}`)...)
	if err := os.WriteFile(unknownPath, unknown, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readOperatorProof(unknownPath); err == nil {
		t.Fatal("accepted unknown operator proof field")
	}
}

func TestLiveServicePreapprovesOnlyExpectedSKIAndIsolatesDiscovery(t *testing.T) {
	expected := "0123456789abcdef0123456789abcdef01234567"
	certificate, err := cert.CreateCertificate("Helianthus", "Project", "RO", "pairing-test")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := newLiveService(liveOptions{Port: 4712, RemoteSKI: expected}, certificate)
	if err != nil {
		t.Fatal(err)
	}
	defer handler.service.Shutdown()
	if err := handler.approveExpectedRemote(); err != nil {
		t.Fatal(err)
	}
	if handler.service.IsAutoAcceptEnabled() {
		t.Fatal("live service enabled arbitrary-peer autoaccept")
	}
	if !handler.service.RemoteServiceForSKI(expected).Trusted() {
		t.Fatal("expected SKI was not approved before start")
	}
	competing := "fedcba9876543210fedcba9876543210fedcba98"
	if handler.service.RemoteServiceForSKI(competing).Trusted() {
		t.Fatal("competing SKI was preapproved")
	}
	interfaces := handler.service.Configuration().Interfaces()
	if len(interfaces) != 1 || interfaces[0] != defaultLoopbackInterface() {
		t.Fatalf("eebus discovery interfaces = %v, want loopback isolation", interfaces)
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

func TestInboundStagesRequireCurrentGenerationAndRunBoundProof(t *testing.T) {
	binding, err := newLiveRunBinding(liveOptions{Interface: "lab-lan", Port: 4712, Timeout: 10 * time.Minute}, "0123456789abcdef0123456789abcdef01234567", time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	connectedAt := time.Now().Add(-2 * time.Second)
	spine := spineCapture{Evidence: passingG19Observation().FirstSPINEData, Generation: 3, CapturedAt: connectedAt.Add(time.Second)}
	connection := connectionSnapshot{Connected: true, Generation: 3, ConnectedAt: connectedAt}
	binding.challengeIssuedAt = time.Now().Add(-500 * time.Millisecond)
	proof := passingOperatorProof(binding, spine)
	if got := completedInboundStages(connection, proof, spine, binding); len(got) != len(requiredTransportStages) {
		t.Fatalf("correlated inbound stages = %v", got)
	}

	spine.Generation++
	if got := completedInboundStages(connection, proof, spine, binding); len(got) != 0 {
		t.Fatalf("stale generation claimed inbound stages: %v", got)
	}
}

func TestOperatorProofIsBoundToRunTransportAndFirstSPINE(t *testing.T) {
	binding, err := newLiveRunBinding(liveOptions{Interface: "lab-lan", Port: 4712, Timeout: 10 * time.Minute}, "0123456789abcdef0123456789abcdef01234567", time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	connectedAt := time.Now().Add(-2 * time.Second)
	spine := spineCapture{Evidence: passingG19Observation().FirstSPINEData, Generation: 9, CapturedAt: connectedAt.Add(time.Second)}
	connection := connectionSnapshot{Connected: true, Generation: 9, ConnectedAt: connectedAt}
	binding.challengeIssuedAt = time.Now().Add(-500 * time.Millisecond)
	proof := passingOperatorProof(binding, spine)
	if err := validateOperatorProof(proof, binding, spine, connection, time.Now()); err != nil {
		t.Fatalf("valid proof rejected: %v", err)
	}

	tests := map[string]func(*operatorProofInput){
		"run":       func(value *operatorProofInput) { value.RunRef = "sha256:" + strings.Repeat("0", 64) },
		"remote":    func(value *operatorProofInput) { value.ExpectedRemoteDigest = "hmac-sha256:" + strings.Repeat("0", 64) },
		"interface": func(value *operatorProofInput) { value.InterfaceRef = "hmac-sha256:" + strings.Repeat("0", 64) },
		"port":      func(value *operatorProofInput) { value.PortRef = "hmac-sha256:" + strings.Repeat("0", 64) },
		"generation": func(value *operatorProofInput) {
			value.ConnectionGenerationRef = "hmac-sha256:" + strings.Repeat("0", 64)
		},
		"transport":   func(value *operatorProofInput) { value.TransportHash = "sha256:" + strings.Repeat("0", 64) },
		"first spine": func(value *operatorProofInput) { value.FirstSPINEHash = "sha256:" + strings.Repeat("0", 64) },
		"challenge":   func(value *operatorProofInput) { value.ChallengeRef = "hmac-sha256:" + strings.Repeat("0", 64) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := proof
			mutate(&candidate)
			if err := validateOperatorProof(candidate, binding, spine, connection, time.Now()); err == nil {
				t.Fatal("accepted mismatched operator proof")
			}
		})
	}
}

func TestOperatorChallengeUsesOpaqueGenerationBindingAndNoRawIdentity(t *testing.T) {
	expectedSKI := "0123456789abcdef0123456789abcdef01234567"
	binding, err := newLiveRunBinding(liveOptions{Interface: "lab-lan", Port: 4712, Timeout: 10 * time.Minute}, expectedSKI, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	transportHash := "sha256:" + strings.Repeat("f", 64)
	binding.challengeIssuedAt = time.Now().UTC()
	spine := spineCapture{Evidence: passingG19Observation().FirstSPINEData, Generation: 1, CapturedAt: binding.challengeIssuedAt.Add(-time.Second)}
	first := binding.operatorChallenge(transportHash, spine)
	spine.Generation = 2
	second := binding.operatorChallenge(transportHash, spine)
	if first.ConnectionGenerationRef == second.ConnectionGenerationRef || first.ChallengeRef == second.ChallengeRef {
		t.Fatal("connection generation did not change the opaque challenge binding")
	}
	if !validHMACSHA256Ref(first.ConnectionGenerationRef) || !validHMACSHA256Ref(first.ChallengeRef) || !validSHA256Ref(binding.runRef) || !validSHA256Ref(binding.runNonceRef) {
		t.Fatalf("challenge contains non-canonical integrity references: %+v", first)
	}
	payload, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), expectedSKI) {
		t.Fatalf("challenge leaked raw expected identity: %s", payload)
	}
}

func passingOperatorProof(binding liveRunBinding, spine spineCapture) operatorProofInput {
	transportHash := "sha256:" + strings.Repeat("f", 64)
	observedAt := binding.challengeIssuedAt.Add(-100 * time.Millisecond)
	return operatorProofInput{
		LANObserverConfirmed:     true,
		TrustVisible:             true,
		InboundTransportObserved: true,
		OwnerAccepted:            true,
		RunNonce:                 binding.nonce,
		RunRef:                   binding.runRef,
		ChallengeRef:             binding.challenge(transportHash, spine),
		ExpectedRemoteDigest:     binding.expectedRemoteDigest,
		InterfaceRef:             binding.interfaceRef,
		PortRef:                  binding.portRef,
		ConnectionGenerationRef:  binding.generationRef(spine.Generation),
		ChallengeIssuedAt:        binding.challengeIssuedAt,
		FirstSPINECapturedAt:     spine.CapturedAt.UTC(),
		RunStartedAt:             binding.startedAt,
		RunExpiresAt:             binding.expiresAt,
		ObservedAt:               observedAt,
		AcceptedAt:               binding.challengeIssuedAt.Add(100 * time.Millisecond),
		EvidenceRef:              "sha256:" + strings.Repeat("e", 64),
		TransportHash:            transportHash,
		FirstSPINEHash:           spine.Evidence.dataHash(),
	}
}
