package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-ship-go/cert"
)

func TestG19ReplayFixtureIsDeterministicAndPublicSafe(t *testing.T) {
	payload, err := os.ReadFile("testdata/g19-replay-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, g19ReplayFixture) {
		t.Fatal("runtime replay payload differs from the checked-in fixture")
	}
	denied, reconnect, artifact, err := replayNegativeObservationsFrom(payload)
	if err != nil {
		t.Fatal(err)
	}
	deniedAgain, reconnectAgain, artifactAgain, err := replayNegativeObservationsFrom(payload)
	if err != nil {
		t.Fatal(err)
	}
	if denied != deniedAgain || reconnect != reconnectAgain || !reflect.DeepEqual(artifact, artifactAgain) {
		t.Fatalf("replay derivation is not deterministic: first=%+v/%+v/%+v second=%+v/%+v/%+v", denied, reconnect, artifact, deniedAgain, reconnectAgain, artifactAgain)
	}
	if denied.validate() != nil || reconnect.validate() != nil || artifact.SHA256 != fullDigestRef(payload) || artifact.DeniedAccessTraceSHA256 != denied.EvidenceHash || artifact.ReconnectFailureTraceSHA256 != reconnect.EvidenceHash {
		t.Fatalf("replay did not derive canonical terminal evidence: denied=%+v reconnect=%+v artifact=%+v", denied, reconnect, artifact)
	}
	if err := validateLiveRedaction(payload); err != nil {
		t.Fatalf("fixture is not public-safe: %v", err)
	}
}

func TestG19ReplayRejectsPreassertedOutcomesAndInvalidSequences(t *testing.T) {
	preasserted := bytes.Replace(g19ReplayFixture, []byte(`"schema_version": 1,`), []byte(`"schema_version": 1, "satisfied": true,`), 1)
	if _, _, _, err := replayNegativeObservationsFrom(preasserted); err == nil {
		t.Fatal("accepted a replay fixture with a preasserted outcome")
	}

	invalidSequence := []byte(`{
  "schema_version": 1,
  "negative_window_ms": 3000,
  "events": [
    {"at_ms": 0, "type": "advertisement_withdrawn"},
    {"at_ms": 3000, "type": "negative_window_elapsed"},
    {"at_ms": 3001, "type": "inbound_attempt", "peer": "unexpected"}
  ]
}`)
	if _, _, _, err := replayNegativeObservationsFrom(invalidSequence); err == nil {
		t.Fatal("accepted an invalid replay transition sequence")
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
		FirstSPINEPayloadHash:    "sha256:" + strings.Repeat("4", 64),
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

func TestLiveServicePreapprovesOnlyExpectedSKIAndDisablesInternalMDNS(t *testing.T) {
	expected := "0123456789abcdef0123456789abcdef01234567"
	certificate, err := cert.CreateCertificate("Helianthus", "Project", "RO", "pairing-test")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := newLiveService(liveOptions{Port: 4712, RemoteSKI: expected}, certificate)
	if err != nil {
		t.Fatal(err)
	}
	defer handler.shutdown()
	if err := handler.approveExpectedRemote(); err != nil {
		t.Fatal(err)
	}
	if handler.service.IsAutoAcceptEnabled() {
		t.Fatal("live service enabled arbitrary-peer autoaccept")
	}
	if !handler.hub.ServiceForSKI(expected).Trusted() {
		t.Fatal("expected SKI was not approved before start")
	}
	competing := "fedcba9876543210fedcba9876543210fedcba98"
	if handler.hub.ServiceForSKI(competing).Trusted() {
		t.Fatal("competing SKI was preapproved")
	}
	interfaces := handler.service.Configuration().Interfaces()
	if len(interfaces) != 0 {
		t.Fatalf("eebus discovery interfaces = %v, want no internal discovery configuration", interfaces)
	}
	if _, ok := handler.server.discovery.(*disabledInternalMDNS); !ok {
		t.Fatalf("internal discovery = %T, want disabledInternalMDNS", handler.server.discovery)
	}
}

func TestLiveServiceStartsWithInternalMDNSDisabled(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	expected := "0123456789abcdef0123456789abcdef01234567"
	certificate, err := cert.CreateCertificate("Helianthus", "Project", "RO", "startup-test")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := newLiveService(liveOptions{Port: port, RemoteSKI: expected}, certificate)
	if err != nil {
		t.Fatal(err)
	}
	defer handler.shutdown()
	if err := handler.approveExpectedRemote(); err != nil {
		t.Fatal(err)
	}
	if err := handler.start(); err != nil {
		t.Fatalf("live service startup with internal mDNS disabled: %v", err)
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

type recordingPayloadReader struct {
	called bool
}

func (r *recordingPayloadReader) HandleShipPayloadMessage([]byte) {
	r.called = true
}

func TestG19RequiresActualInboundPayloadCallbackNotInitializedModel(t *testing.T) {
	expected := "0123456789abcdef0123456789abcdef01234567"
	connectedAt := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	handler := &liveServiceHandler{
		expectedSKI: expected,
		connected:   true,
		generation:  4,
		connectedAt: connectedAt,
		denied:      make(map[string]struct{}),
	}
	initializedProjection := passingG19Observation().FirstSPINEData

	withoutPayload := passingG19Observation()
	withoutPayload.FirstSPINEPayloadHash = ""
	withoutPayload.FirstSPINEData = initializedProjection
	if result := evaluateG19(withoutPayload); result.Status != resultFail || result.Error != "first_spine_payload_hash_required" {
		t.Fatalf("initialized model satisfied G19 without a payload callback: %+v", result)
	}
	if capture := handler.firstSPINESnapshot(); !capture.Evidence.empty() || capture.PayloadHash != "" {
		t.Fatalf("initialized projection was captured without a payload event: %+v", capture)
	}

	delegate := &recordingPayloadReader{}
	reader := &payloadCaptureReader{
		delegate: delegate,
		capture: func(message []byte) {
			handler.captureInboundSPINEPayload(expected, 4, message, deriveInboundSPINEProjection(message), connectedAt.Add(time.Second))
		},
	}
	payload := []byte(`{"datagram":{"header":{"msgCounter":1},"payload":{"cmd":[{"nodeManagementDetailedDiscoveryData":{}}]}}}`)
	reader.HandleShipPayloadMessage(payload)
	capture := handler.firstSPINESnapshot()
	if !delegate.called || capture.PayloadHash != fullDigestRef(payload) || capture.Generation != 4 || capture.Evidence.empty() {
		t.Fatalf("actual payload callback did not create generation-bound evidence: delegate=%t capture=%+v", delegate.called, capture)
	}
	if !reflect.DeepEqual(capture.Evidence.FeatureTypes, []string{"spine-cmd/nodeManagementDetailedDiscoveryData"}) {
		t.Fatalf("SPINE projection was not derived from the inbound command: %+v", capture.Evidence)
	}

	withPayload := passingG19Observation()
	withPayload.ConnectionGeneration = 4
	withPayload.FirstSPINEGeneration = capture.Generation
	withPayload.FirstSPINEPayloadHash = capture.PayloadHash
	withPayload.FirstSPINEData = capture.Evidence
	if result := evaluateG19(withPayload); result.Status != resultPass {
		t.Fatalf("actual inbound payload event did not satisfy G19: %+v", result)
	}
}

func TestInboundStagesRequireCurrentGenerationAndRunBoundProof(t *testing.T) {
	binding, err := newLiveRunBinding(liveOptions{Interface: "lab-lan", Port: 4712, Timeout: 10 * time.Minute}, "0123456789abcdef0123456789abcdef01234567", time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	connectedAt := time.Now().Add(-2 * time.Second)
	spine := spineCapture{Evidence: passingG19Observation().FirstSPINEData, PayloadHash: passingG19Observation().FirstSPINEPayloadHash, Generation: 3, CapturedAt: connectedAt.Add(time.Second)}
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

func TestG17OperatorProofIsValidWithoutG19TransportEvidence(t *testing.T) {
	startedAt := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	binding, err := newLiveRunBinding(liveOptions{Interface: "lab-lan", Port: 4712, Timeout: 10 * time.Minute}, "0123456789abcdef0123456789abcdef01234567", startedAt)
	if err != nil {
		t.Fatal(err)
	}
	proof := operatorProofInput{
		LANObserverConfirmed: true,
		TrustVisible:         true,
		OwnerAccepted:        true,
		RunNonce:             binding.nonce,
		RunRef:               binding.runRef,
		ExpectedRemoteDigest: binding.expectedRemoteDigest,
		InterfaceRef:         binding.interfaceRef,
		PortRef:              binding.portRef,
		RunStartedAt:         binding.startedAt,
		RunExpiresAt:         binding.expiresAt,
		ObservedAt:           startedAt.Add(time.Minute),
		AcceptedAt:           startedAt.Add(2 * time.Minute),
		EvidenceRef:          "sha256:" + strings.Repeat("a", 64),
	}
	if err := validateG17OperatorProof(proof, binding, startedAt.Add(3*time.Minute)); err != nil {
		t.Fatalf("valid G17-only proof rejected: %v", err)
	}
	if err := validateG19OperatorProof(proof, binding, spineCapture{}, connectionSnapshot{}, startedAt.Add(3*time.Minute)); err == nil {
		t.Fatal("G17-only proof incorrectly satisfied G19")
	}
}

func TestOperatorProofIsBoundToRunTransportAndFirstSPINE(t *testing.T) {
	binding, err := newLiveRunBinding(liveOptions{Interface: "lab-lan", Port: 4712, Timeout: 10 * time.Minute}, "0123456789abcdef0123456789abcdef01234567", time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	connectedAt := time.Now().Add(-2 * time.Second)
	spine := spineCapture{Evidence: passingG19Observation().FirstSPINEData, PayloadHash: passingG19Observation().FirstSPINEPayloadHash, Generation: 9, CapturedAt: connectedAt.Add(time.Second)}
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
		"payload":     func(value *operatorProofInput) { value.FirstSPINEPayloadHash = "sha256:" + strings.Repeat("0", 64) },
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
	spine := spineCapture{Evidence: passingG19Observation().FirstSPINEData, PayloadHash: passingG19Observation().FirstSPINEPayloadHash, Generation: 1, CapturedAt: binding.challengeIssuedAt.Add(-time.Second)}
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

type fakeNegativeWindowClock struct {
	now       time.Time
	afterCall chan time.Duration
	timer     chan time.Time
}

func (c *fakeNegativeWindowClock) Now() time.Time {
	return c.now
}

func (c *fakeNegativeWindowClock) After(duration time.Duration) <-chan time.Time {
	c.afterCall <- duration
	return c.timer
}

func TestPostWithdrawalNegativeWindowUsesInjectedDurationAndAttempts(t *testing.T) {
	start := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	tracker := &postWithdrawalTracker{}
	if err := tracker.observerReady(start); err != nil {
		t.Fatal(err)
	}
	if err := tracker.advertisementWithdrawn(start.Add(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	clock := &fakeNegativeWindowClock{
		now:       start,
		afterCall: make(chan time.Duration, 1),
		timer:     make(chan time.Time, 1),
	}
	resultCh := make(chan postWithdrawalWindowResult, 1)
	go func() {
		satisfied, err := waitPostWithdrawalWindow(context.Background(), tracker, 25*time.Millisecond, clock)
		resultCh <- postWithdrawalWindowResult{Satisfied: satisfied, Err: err}
	}()
	if duration := <-clock.afterCall; duration != 25*time.Millisecond {
		t.Fatalf("negative window duration = %s, want 25ms", duration)
	}
	clock.timer <- start.Add(time.Millisecond + 25*time.Millisecond)
	result := <-resultCh
	if result.Err != nil || !result.Satisfied {
		t.Fatalf("bounded no-attempt window failed: %+v", result)
	}

	tracker = &postWithdrawalTracker{}
	if err := tracker.observerReady(start); err != nil {
		t.Fatal(err)
	}
	if err := tracker.advertisementWithdrawn(start); err != nil {
		t.Fatal(err)
	}
	tracker.recordInboundAttempt(start.Add(time.Millisecond))
	satisfied, _, err := tracker.finish(start.Add(25*time.Millisecond), 25*time.Millisecond)
	if err != nil || satisfied {
		t.Fatalf("inbound listener attempt was not retained as negative evidence: satisfied=%t err=%v", satisfied, err)
	}
}

func TestLiveEvidencePreservesSubsecondAcceptanceTimestamp(t *testing.T) {
	startedAt := time.Date(2026, 7, 14, 8, 0, 0, 123456789, time.UTC)
	binding, err := newLiveRunBinding(liveOptions{Interface: "lab-lan", Port: 4712, Timeout: 10 * time.Minute}, "0123456789abcdef0123456789abcdef01234567", startedAt)
	if err != nil {
		t.Fatal(err)
	}
	binding.challengeIssuedAt = startedAt.Add(2 * time.Minute)
	spine := spineCapture{
		Evidence:    passingG19Observation().FirstSPINEData,
		PayloadHash: passingG19Observation().FirstSPINEPayloadHash,
		Generation:  5,
		CapturedAt:  binding.challengeIssuedAt.Add(-time.Second),
	}
	proof := passingOperatorProof(binding, spine)
	proof.AcceptedAt = binding.challengeIssuedAt.Add(987654321 * time.Nanosecond)
	denied, reconnect, artifact, err := replayNegativeObservations()
	if err != nil {
		t.Fatal(err)
	}
	observation := passingG19Observation()
	observation.ConnectionGeneration = spine.Generation
	observation.FirstSPINEGeneration = spine.Generation
	observation.FirstSPINEPayloadHash = spine.PayloadHash
	observation.FirstSPINEData = spine.Evidence
	observation.DeniedAccess = denied
	observation.ReconnectFailure = reconnect
	g19 := evaluateG19(observation)
	evidence := buildLiveGateEvidence(liveOptions{PairingWindow: true}, binding, proof, spine, []byte(`["completed"]`), g19, denied, reconnect, artifact)
	if !evidence.OwnerAcceptance.AcceptedAt.Equal(proof.AcceptedAt) || !evidence.Environment.TimestampUTC.Equal(proof.AcceptedAt) {
		t.Fatalf("acceptance timestamp lost precision: proof=%s owner=%s environment=%s", proof.AcceptedAt.Format(time.RFC3339Nano), evidence.OwnerAcceptance.AcceptedAt.Format(time.RFC3339Nano), evidence.Environment.TimestampUTC.Format(time.RFC3339Nano))
	}
	if err := evidence.validateForCase(g19, currentRepoEvidence()); err != nil {
		t.Fatalf("subsecond live evidence rejected: %v", err)
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
		FirstSPINEPayloadHash:    spine.PayloadHash,
	}
}
