package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestG17PassesForConfiguredLocalAdvertisementAndOperatorProof(t *testing.T) {
	result := evaluateG17(g17Observation{
		Direction:                 accessDirectionInboundFromVR940,
		SelectedInterface:         "lab-lan",
		SelectedPort:              4712,
		LocalAdvertisementSeen:    true,
		LANObserverConfirmed:      true,
		OperatorTrustVisible:      true,
		TTLWithdrawalObserved:     true,
		NoConnectionAfterWithdraw: true,
	})

	if result.Status != resultPass {
		t.Fatalf("G17 result = %+v, want PASS", result)
	}
	for _, want := range []string{
		"g17-local-ship-advertisement-observed",
		"g17-lan-observer-confirmed",
		"g17-myvaillant-trust-visible",
		"g17-ttl-withdrawal-observed",
		"g17-post-withdrawal-negative-observed",
	} {
		if !containsString(result.Evidence, want) {
			t.Fatalf("G17 evidence %v does not contain %q", result.Evidence, want)
		}
	}
	if got := result.Details["direction"]; got != accessDirectionInboundFromVR940 {
		t.Fatalf("direction = %q, want %q", got, accessDirectionInboundFromVR940)
	}
	if strings.Contains(strings.Join(result.Evidence, " "), "vr940-server") {
		t.Fatalf("G17 evidence claims the forbidden VR940 server role: %v", result.Evidence)
	}
}

func TestG17FailsClosedForMissingProofOrWrongDirection(t *testing.T) {
	base := g17Observation{
		Direction:                 accessDirectionInboundFromVR940,
		SelectedInterface:         "lab-lan",
		SelectedPort:              4712,
		LocalAdvertisementSeen:    true,
		LANObserverConfirmed:      true,
		OperatorTrustVisible:      true,
		TTLWithdrawalObserved:     true,
		NoConnectionAfterWithdraw: true,
	}

	tests := []struct {
		name   string
		mutate func(*g17Observation)
		error  string
	}{
		{name: "selected interface", mutate: func(v *g17Observation) { v.SelectedInterface = "" }, error: "selected_interface_required"},
		{name: "selected port", mutate: func(v *g17Observation) { v.SelectedPort = 0 }, error: "selected_port_required"},
		{name: "local advertisement", mutate: func(v *g17Observation) { v.LocalAdvertisementSeen = false }, error: "local_advertisement_not_observed"},
		{name: "LAN observer", mutate: func(v *g17Observation) { v.LANObserverConfirmed = false }, error: "lan_observer_confirmation_required"},
		{name: "operator trust", mutate: func(v *g17Observation) { v.OperatorTrustVisible = false }, error: "operator_trust_visibility_required"},
		{name: "TTL withdrawal", mutate: func(v *g17Observation) { v.TTLWithdrawalObserved = false }, error: "ttl_withdrawal_not_observed"},
		{name: "post withdrawal negative", mutate: func(v *g17Observation) { v.NoConnectionAfterWithdraw = false }, error: "post_withdrawal_negative_not_observed"},
		{name: "wrong direction", mutate: func(v *g17Observation) { v.Direction = "outbound-to-vr940-server" }, error: "vr940_server_role_claim_forbidden"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observation := base
			tt.mutate(&observation)
			result := evaluateG17(observation)
			if result.Status != resultFail || result.Error != tt.error {
				t.Fatalf("G17 result = %+v, want FAIL/%s", result, tt.error)
			}
		})
	}
}

func TestG17FailurePreservesCompletedPrerequisiteEvidence(t *testing.T) {
	result := evaluateG17(g17Observation{
		Direction:              accessDirectionInboundFromVR940,
		SelectedInterface:      "lab-lan",
		SelectedPort:           4712,
		LocalAdvertisementSeen: true,
	})
	if result.Error != "lan_observer_confirmation_required" {
		t.Fatalf("G17 result = %+v", result)
	}
	if !containsString(result.Evidence, "g17-local-ship-advertisement-observed") {
		t.Fatalf("G17 dropped completed evidence: %+v", result)
	}
}

func TestG19RequiresOrderedInboundTransportAndFirstSPINEData(t *testing.T) {
	observation := passingG19Observation()
	result := evaluateG19(observation)
	if result.Status != resultPass {
		t.Fatalf("G19 result = %+v, want PASS", result)
	}
	if got := result.Details["direction"]; got != accessDirectionInboundFromVR940 {
		t.Fatalf("direction = %q, want %q", got, accessDirectionInboundFromVR940)
	}
	if result.Details["spine_data_hash"] == "" {
		t.Fatal("G19 did not bind first post-access SPINE data")
	}
	if _, ok := result.Details["feature_graph_complete"]; ok {
		t.Fatalf("G19 incorrectly requires deferred feature graph completeness: %+v", result.Details)
	}
}

func TestG19FailsClosedForMissingOrOutOfOrderStage(t *testing.T) {
	required := []transportStage{
		transportStageTCPAccepted,
		transportStageTLSCompleted,
		transportStageWebSocketUpgraded,
		transportStageSHIPCompleted,
		transportStageFirstSPINEData,
	}
	for i, missing := range required {
		t.Run(string(missing), func(t *testing.T) {
			observation := passingG19Observation()
			observation.Stages = append([]transportStage(nil), required[:i]...)
			observation.Stages = append(observation.Stages, required[i+1:]...)
			result := evaluateG19(observation)
			if result.Status != resultFail || result.Error != "transport_stage_sequence_incomplete" {
				t.Fatalf("G19 without %s = %+v", missing, result)
			}
		})
	}

	observation := passingG19Observation()
	observation.Stages[1], observation.Stages[2] = observation.Stages[2], observation.Stages[1]
	result := evaluateG19(observation)
	if result.Status != resultFail || result.Error != "transport_stage_sequence_invalid" {
		t.Fatalf("G19 out of order = %+v", result)
	}
}

func TestG19FailurePreservesCompletedTransportStages(t *testing.T) {
	observation := passingG19Observation()
	observation.Stages = observation.Stages[:4]
	result := evaluateG19(observation)
	if result.Error != "transport_stage_sequence_incomplete" {
		t.Fatalf("G19 result = %+v", result)
	}
	for _, stage := range observation.Stages {
		if !containsString(result.Evidence, "g19-stage-"+string(stage)) {
			t.Fatalf("G19 dropped completed stage %s: %+v", stage, result)
		}
	}
}

func TestG19NegativeCasesAreTerminalAndDeterministic(t *testing.T) {
	observation := passingG19Observation()
	observation.DeniedAccess.Satisfied = false
	result := evaluateG19(observation)
	if result.Status != resultFail || result.Error != "denied_access_negative_required" {
		t.Fatalf("missing denied-access negative = %+v", result)
	}

	observation = passingG19Observation()
	observation.ReconnectFailure.Satisfied = false
	result = evaluateG19(observation)
	if result.Status != resultFail || result.Error != "reconnect_failure_negative_required" {
		t.Fatalf("missing reconnect-failure negative = %+v", result)
	}
}

func TestG19RequiresFirstSPINEFromCurrentConnectionGeneration(t *testing.T) {
	observation := passingG19Observation()
	observation.FirstSPINEGeneration++
	result := evaluateG19(observation)
	if result.Status != resultFail || result.Error != "first_spine_generation_mismatch" {
		t.Fatalf("generation mismatch = %+v", result)
	}

	observation = passingG19Observation()
	observation.CurrentConnection = false
	result = evaluateG19(observation)
	if result.Status != resultFail || result.Error != "current_connection_generation_required" {
		t.Fatalf("stale connection = %+v", result)
	}
}

func TestLiveEvidenceSeparatesOperatorProofFromCIReplay(t *testing.T) {
	evidence := passingLiveGateEvidence()
	if err := evidence.validate(); err != nil {
		t.Fatalf("validate passing evidence: %v", err)
	}

	evidence.OperatorLiveProof.Result = resultFail
	evidence.OperatorLiveProof.TrustVisible = false
	evidence.CIReplayAuthority.Result = resultPass
	if err := evidence.validate(); err == nil || !strings.Contains(err.Error(), "operator live proof") {
		t.Fatalf("CI replay substituted for operator proof: %v", err)
	}
}

func TestLiveEvidenceBindsFirstSPINEDataHash(t *testing.T) {
	evidence := passingLiveGateEvidence()
	evidence.OperatorLiveProof.FirstSPINEDataHash = "sha256:" + strings.Repeat("0", 64)
	if err := evidence.validate(); err == nil || !strings.Contains(err.Error(), "operator live proof evidence") {
		t.Fatalf("accepted mismatched SPINE data hash: %v", err)
	}
}

func TestLiveEvidenceJSONAndHashAreDeterministic(t *testing.T) {
	left := passingLiveGateEvidence()
	right := passingLiveGateEvidence()
	right.Commands[0], right.Commands[1] = right.Commands[1], right.Commands[0]
	right.Commands = append(right.Commands, right.Commands[0])
	right.CIReplayAuthority.Fixtures[0], right.CIReplayAuthority.Fixtures[1] = right.CIReplayAuthority.Fixtures[1], right.CIReplayAuthority.Fixtures[0]
	right.CIReplayAuthority.Fixtures = append(right.CIReplayAuthority.Fixtures, right.CIReplayAuthority.Fixtures[0])
	right.OperatorLiveProof.TranscriptHashes = append(right.OperatorLiveProof.TranscriptHashes, right.OperatorLiveProof.TranscriptHashes[0])
	right.OperatorLiveProof.FirstSPINEData.EntityTypes = append(right.OperatorLiveProof.FirstSPINEData.EntityTypes, right.OperatorLiveProof.FirstSPINEData.EntityTypes[0])

	leftJSON, err := left.jsonBytes()
	if err != nil {
		t.Fatal(err)
	}
	rightJSON, err := right.jsonBytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(leftJSON, rightJSON) {
		t.Fatalf("canonical evidence is order-dependent:\nleft=%s\nright=%s", leftJSON, rightJSON)
	}
	if left.dataHash() != right.dataHash() {
		t.Fatalf("data hash differs: %s != %s", left.dataHash(), right.dataHash())
	}
	if left.Commands[0] == right.Commands[0] {
		t.Fatal("test precondition lost distinct input ordering")
	}
}

func TestLiveEvidenceRejectsReplayClaimedAsLive(t *testing.T) {
	evidence := passingLiveGateEvidence()
	evidence.NegativeCases.DeniedAccess.LiveObserved = true
	if err := evidence.validate(); err == nil || !strings.Contains(err.Error(), "denied-access") {
		t.Fatalf("accepted CI replay as live observation: %v", err)
	}
}

func TestLiveEvidenceBindsCIReplayResultToFixtureHash(t *testing.T) {
	evidence := passingLiveGateEvidence()
	evidence.NegativeCases.DeniedAccess.EvidenceHash = "sha256:" + strings.Repeat("f", 64)
	if err := evidence.validate(); err == nil || !strings.Contains(err.Error(), "denied-access") {
		t.Fatalf("accepted replay evidence not bound to a fixture: %v", err)
	}
}

func TestLiveEvidenceRejectsRawIdentityAndCaptureMaterial(t *testing.T) {
	tests := map[string]string{
		"ski":             "0123456789abcdef0123456789abcdef01234567",
		"fingerprint":     "fingerprint=aa:bb:cc:dd:ee:ff:00:11:22:33:44:55:66:77:88:99",
		"ship-id":         "shipid=private-device-id",
		"ip":              "192.0.2.44",
		"mac":             "02:00:00:00:00:01",
		"serial":          "serial=private-serial-value",
		"private-key":     "-----BEGIN PRIVATE KEY-----",
		"raw-transcript":  "raw_transcript=handshake payload",
		"packet-capture":  "capture.pcap",
		"pairing-history": "pairing_history=trusted-peer",
	}
	for name, leaked := range tests {
		t.Run(name, func(t *testing.T) {
			evidence := passingLiveGateEvidence()
			evidence.OwnerAcceptance.Notes = leaked
			if err := evidence.validate(); err == nil {
				t.Fatalf("accepted public evidence containing %q", leaked)
			}
		})
	}
}

func passingG19Observation() g19Observation {
	return g19Observation{
		Direction:            accessDirectionInboundFromVR940,
		CurrentConnection:    true,
		ConnectionGeneration: 7,
		FirstSPINEGeneration: 7,
		Stages: []transportStage{
			transportStageTCPAccepted,
			transportStageTLSCompleted,
			transportStageWebSocketUpgraded,
			transportStageSHIPCompleted,
			transportStageFirstSPINEData,
		},
		FirstSPINEData: spineEvidence{
			EntityTypes:  []string{"DeviceInformation", "CEM"},
			FeatureTypes: []string{"NodeManagement", "DeviceConfiguration"},
			UseCaseRefs: []string{
				"usecase-sha256:" + strings.Repeat("b", 64),
				"usecase-sha256:" + strings.Repeat("a", 64),
			},
		},
		DeniedAccess: negativeObservation{
			Satisfied:    true,
			Authority:    negativeAuthorityCIReplay,
			EvidenceHash: "sha256:" + strings.Repeat("c", 64),
		},
		ReconnectFailure: negativeObservation{
			Satisfied:    true,
			Authority:    negativeAuthorityCIReplay,
			EvidenceHash: "sha256:" + strings.Repeat("d", 64),
		},
	}
}

func passingLiveGateEvidence() liveGateEvidence {
	g19 := evaluateG19(passingG19Observation())
	startedAt := time.Date(2026, 7, 13, 21, 0, 0, 0, time.UTC)
	observedAt := time.Date(2026, 7, 13, 21, 4, 0, 0, time.UTC)
	acceptedAt := time.Date(2026, 7, 13, 21, 5, 0, 0, time.UTC)
	return liveGateEvidence{
		SchemaVersion: 1,
		Gate:          caseDirectAccess,
		CaseBinding: liveCaseBinding{
			ID:         g19.ID,
			Status:     g19.Status,
			ResultHash: g19.dataHash(),
		},
		Repo:     currentRepoEvidence(),
		Commands: []string{"go test ./internal/eebusinteropsmoke", "./scripts/ci_local.sh"},
		Environment: evidenceEnvironment{
			TimestampUTC: acceptedAt,
			GoVersion:    "go1.24.5",
			TopologyRef:  "hmac-sha256:" + strings.Repeat("1", 64),
		},
		TrustPreconditions: trustPreconditions{
			LocalIdentityState:     "disposable-in-memory",
			ExpectedRemoteApproved: true,
			AutoAcceptEnabled:      false,
			DiscoveryIsolation:     "loopback",
			OperatorWindow:         "opened",
		},
		OperatorLiveProof: operatorLiveProof{
			Result:                  resultPass,
			TrustVisible:            true,
			RunNonceRef:             "sha256:" + strings.Repeat("0", 64),
			RunRef:                  "sha256:" + strings.Repeat("1", 64),
			ChallengeRef:            "hmac-sha256:" + strings.Repeat("2", 64),
			ExpectedRemoteDigest:    "hmac-sha256:" + strings.Repeat("3", 64),
			InterfaceRef:            "hmac-sha256:" + strings.Repeat("4", 64),
			PortRef:                 "hmac-sha256:" + strings.Repeat("5", 64),
			ConnectionGenerationRef: "hmac-sha256:" + strings.Repeat("6", 64),
			ChallengeIssuedAt:       time.Date(2026, 7, 13, 21, 4, 30, 0, time.UTC),
			FirstSPINECapturedAt:    time.Date(2026, 7, 13, 21, 3, 0, 0, time.UTC),
			RunStartedAt:            startedAt,
			RunExpiresAt:            startedAt.Add(10 * time.Minute),
			ObservedAt:              observedAt,
			AcceptedAt:              acceptedAt,
			EvidenceRef:             "sha256:" + strings.Repeat("e", 64),
			TransportHash:           "sha256:" + strings.Repeat("f", 64),
			TranscriptHashes:        []string{"sha256:" + strings.Repeat("b", 64)},
			FirstSPINEData:          passingG19Observation().FirstSPINEData,
			FirstSPINEDataHash:      passingG19Observation().FirstSPINEData.dataHash(),
		},
		CIReplayAuthority: ciReplayAuthority{
			Result: resultPass,
			Fixtures: []replayArtifact{
				{Path: "testdata/replay-b.json", SHA256: "sha256:" + strings.Repeat("8", 64)},
				{Path: "testdata/replay-a.json", SHA256: "sha256:" + strings.Repeat("9", 64)},
			},
			ReplayCommand: "go test ./internal/eebusinteropsmoke -run Replay",
		},
		NegativeCases: negativeCaseEvidence{
			DeniedAccess:     evidenceResult{Result: resultPass, Authority: negativeAuthorityCIReplay, EvidenceHash: "sha256:" + strings.Repeat("8", 64)},
			ReconnectFailure: evidenceResult{Result: resultPass, Authority: negativeAuthorityCIReplay, EvidenceHash: "sha256:" + strings.Repeat("8", 64)},
		},
		PublicRedaction: publicRedactionEvidence{
			NoPacketCaptures:    true,
			NoRawTranscripts:    true,
			NoSensitiveMaterial: true,
			NoRawIdentity:       true,
		},
		OwnerAcceptance: ownerAcceptance{
			Accepted:   true,
			AcceptedAt: acceptedAt,
			Notes:      "accepted from redacted operator observation",
		},
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
