package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	caseDirectAccess                = "EEBUS-G19"
	accessDirectionInboundFromVR940 = "inbound-from-vr940-client"
	negativeAuthorityCIReplay       = "ci-replay"
	negativeAuthorityLiveNetwork    = "live-network"
)

const g19ReplayFixturePath = "internal/eebusinteropsmoke/testdata/g19-replay-v1.json"

var g19ReplayFixture = []byte(`{
  "schema_version": 1,
  "negative_window_ms": 3000,
  "events": [
    {
      "at_ms": 0,
      "type": "inbound_attempt",
      "peer": "unexpected"
    },
    {
      "at_ms": 10,
      "type": "observer_ready"
    },
    {
      "at_ms": 20,
      "type": "advertisement_withdrawn"
    },
    {
      "at_ms": 3020,
      "type": "negative_window_elapsed"
    }
  ]
}
`)

type g17Observation struct {
	Direction                 string
	SelectedInterface         string
	SelectedPort              int
	LocalAdvertisementSeen    bool
	LANObserverConfirmed      bool
	OperatorTrustVisible      bool
	TTLWithdrawalObserved     bool
	NoConnectionAfterWithdraw bool
}

func evaluateG17(observation g17Observation) caseResult {
	observation.Direction = strings.TrimSpace(observation.Direction)
	observation.SelectedInterface = strings.TrimSpace(observation.SelectedInterface)
	evidence := []string{"g17-evaluation-completed"}
	details := map[string]string{}
	failure := func(code string) caseResult {
		return caseResult{ID: caseLive, Status: resultFail, Evidence: append([]string(nil), evidence...), Details: details, Error: code}
	}
	if observation.Direction != accessDirectionInboundFromVR940 {
		return failure("vr940_server_role_claim_forbidden")
	}
	details["direction"] = accessDirectionInboundFromVR940
	if observation.SelectedInterface == "" {
		return failure("selected_interface_required")
	}
	if observation.SelectedPort < 1 || observation.SelectedPort > 65535 {
		return failure("selected_port_required")
	}
	if !observation.LocalAdvertisementSeen {
		return failure("local_advertisement_not_observed")
	}
	evidence = append(evidence, "g17-local-ship-advertisement-observed")
	if !observation.LANObserverConfirmed {
		return failure("lan_observer_confirmation_required")
	}
	evidence = append(evidence, "g17-lan-observer-confirmed")
	if !observation.OperatorTrustVisible {
		return failure("operator_trust_visibility_required")
	}
	evidence = append(evidence, "g17-myvaillant-trust-visible")
	if !observation.TTLWithdrawalObserved {
		return failure("ttl_withdrawal_not_observed")
	}
	evidence = append(evidence, "g17-ttl-withdrawal-observed")
	if !observation.NoConnectionAfterWithdraw {
		return failure("post_withdrawal_negative_not_observed")
	}
	evidence = append(evidence, "g17-post-withdrawal-negative-observed")

	return caseResult{
		ID:       caseLive,
		Status:   resultPass,
		Evidence: evidence,
		Details:  details,
	}
}

type transportStage string

const (
	transportStageTCPAccepted       transportStage = "tcp_accepted"
	transportStageTLSCompleted      transportStage = "tls_completed"
	transportStageWebSocketUpgraded transportStage = "websocket_upgraded"
	transportStageSHIPCompleted     transportStage = "ship_completed"
	transportStageFirstSPINEData    transportStage = "first_spine_data"
)

var requiredTransportStages = []transportStage{
	transportStageTCPAccepted,
	transportStageTLSCompleted,
	transportStageWebSocketUpgraded,
	transportStageSHIPCompleted,
	transportStageFirstSPINEData,
}

type g19Observation struct {
	Direction             string              `json:"direction"`
	Stages                []transportStage    `json:"stages"`
	CurrentConnection     bool                `json:"current_connection"`
	ConnectionGeneration  uint64              `json:"connection_generation"`
	FirstSPINEGeneration  uint64              `json:"first_spine_generation"`
	FirstSPINEPayloadHash string              `json:"first_spine_payload_hash"`
	FirstSPINEData        spineEvidence       `json:"first_spine_data"`
	DeniedAccess          negativeObservation `json:"denied_access"`
	ReconnectFailure      negativeObservation `json:"reconnect_failure"`
}

type negativeObservation struct {
	Satisfied    bool   `json:"satisfied"`
	Authority    string `json:"authority"`
	EvidenceHash string `json:"evidence_hash"`
}

type spineEvidence struct {
	EntityTypes  []string `json:"entity_types"`
	FeatureTypes []string `json:"feature_types"`
	UseCaseRefs  []string `json:"usecase_refs"`
}

func (e spineEvidence) normalized() spineEvidence {
	return spineEvidence{
		EntityTypes:  sortedUnique(e.EntityTypes),
		FeatureTypes: sortedUnique(e.FeatureTypes),
		UseCaseRefs:  sortedUnique(e.UseCaseRefs),
	}
}

func (e spineEvidence) empty() bool {
	normalized := e.normalized()
	return len(normalized.EntityTypes) == 0 && len(normalized.FeatureTypes) == 0 && len(normalized.UseCaseRefs) == 0
}

func (e spineEvidence) dataHash() string {
	payload, err := json.Marshal(e.normalized())
	if err != nil {
		return "sha256:invalid"
	}
	return fullDigestRef(payload)
}

func evaluateG19(observation g19Observation) caseResult {
	observation.Direction = strings.TrimSpace(observation.Direction)
	observation.FirstSPINEData = observation.FirstSPINEData.normalized()
	observation.DeniedAccess = observation.DeniedAccess.normalized()
	observation.ReconnectFailure = observation.ReconnectFailure.normalized()
	evidence := []string{"g19-evaluation-completed"}
	details := map[string]string{}
	failure := func(code string) caseResult {
		return caseResult{ID: caseDirectAccess, Status: resultFail, Evidence: append([]string(nil), evidence...), Details: details, Error: code}
	}
	if observation.Direction != accessDirectionInboundFromVR940 {
		return failure("vr940_server_role_claim_forbidden")
	}
	details["direction"] = accessDirectionInboundFromVR940
	if !observation.CurrentConnection || observation.ConnectionGeneration == 0 {
		return failure("current_connection_generation_required")
	}
	if observation.FirstSPINEGeneration != observation.ConnectionGeneration {
		return failure("first_spine_generation_mismatch")
	}
	if !validSHA256Ref(observation.FirstSPINEPayloadHash) {
		return failure("first_spine_payload_hash_required")
	}
	details["spine_payload_hash"] = strings.TrimSpace(observation.FirstSPINEPayloadHash)
	for i, observed := range observation.Stages {
		observed = transportStage(strings.TrimSpace(string(observed)))
		if i >= len(requiredTransportStages) || observed != requiredTransportStages[i] {
			if len(observation.Stages) != len(requiredTransportStages) {
				return failure("transport_stage_sequence_incomplete")
			}
			return failure("transport_stage_sequence_invalid")
		}
		evidence = append(evidence, "g19-stage-"+string(observed))
	}
	if len(observation.Stages) != len(requiredTransportStages) {
		return failure("transport_stage_sequence_incomplete")
	}
	if observation.FirstSPINEData.empty() {
		return failure("first_spine_data_required")
	}
	if err := observation.FirstSPINEData.validate(); err != nil {
		return failure("first_spine_data_invalid")
	}
	details["spine_data_hash"] = observation.FirstSPINEData.dataHash()
	evidence = append(evidence, "g19-first-post-access-spine-data-captured")
	if err := observation.DeniedAccess.validate(); err != nil {
		return failure("denied_access_negative_required")
	}
	evidence = append(evidence, "g19-denied-access-"+observation.DeniedAccess.Authority+"-authority")
	if err := observation.ReconnectFailure.validate(); err != nil {
		return failure("reconnect_failure_negative_required")
	}
	evidence = append(evidence, "g19-reconnect-failure-"+observation.ReconnectFailure.Authority+"-authority")

	return caseResult{
		ID:       caseDirectAccess,
		Status:   resultPass,
		Evidence: append(evidence, "g19-inbound-transport-sequence-completed"),
		Details:  details,
	}
}

func (e spineEvidence) validate() error {
	normalized := e.normalized()
	for _, ref := range normalized.UseCaseRefs {
		if !strings.HasPrefix(ref, "usecase-") || !validSHA256Ref(strings.TrimPrefix(ref, "usecase-")) {
			return errors.New("invalid use-case reference")
		}
	}
	return nil
}

func (n negativeObservation) validate() error {
	n = n.normalized()
	if !n.Satisfied || !validSHA256Ref(n.EvidenceHash) {
		return errors.New("negative evidence is incomplete")
	}
	if n.Authority != negativeAuthorityCIReplay && n.Authority != negativeAuthorityLiveNetwork {
		return errors.New("negative evidence authority is unsupported")
	}
	return nil
}

func (n negativeObservation) normalized() negativeObservation {
	n.Authority = strings.TrimSpace(n.Authority)
	n.EvidenceHash = strings.TrimSpace(n.EvidenceHash)
	return n
}

const (
	replayEventInboundAttempt         = "inbound_attempt"
	replayEventObserverReady          = "observer_ready"
	replayEventAdvertisementWithdrawn = "advertisement_withdrawn"
	replayEventNegativeWindowElapsed  = "negative_window_elapsed"
	replayPeerExpected                = "expected"
	replayPeerUnexpected              = "unexpected"
	replayTransitionAccessDenied      = "access_denied"
	replayTransitionAccessAllowed     = "access_allowed"
	replayTransitionObserverReady     = "observer_ready"
	replayTransitionAdvertisementGone = "advertisement_withdrawn"
	replayTransitionInboundAttempt    = "inbound_attempt_observed"
	replayTransitionNoInboundAttempt  = "no_inbound_attempt_observed"
	replayTraceDeniedAccess           = "denied_access"
	replayTraceReconnectFailure       = "reconnect_failure"
)

type replayInput struct {
	SchemaVersion    int           `json:"schema_version"`
	NegativeWindowMS int64         `json:"negative_window_ms"`
	Events           []replayEvent `json:"events"`
}

type replayEvent struct {
	AtMS int64  `json:"at_ms"`
	Type string `json:"type"`
	Peer string `json:"peer,omitempty"`
}

type replayTransition struct {
	AtMS       int64  `json:"at_ms"`
	Transition string `json:"transition"`
}

type postWithdrawalTracker struct {
	mu          sync.Mutex
	ready       bool
	withdrawn   bool
	finished    bool
	readyAt     time.Time
	withdrawnAt time.Time
	attempts    int
	trace       []replayTransition
}

func (t *postWithdrawalTracker) observerReady(at time.Time) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ready || t.withdrawn || t.finished || at.IsZero() {
		return errors.New("post-withdrawal observer-ready transition invalid")
	}
	t.ready = true
	t.readyAt = at
	t.trace = append(t.trace, replayTransition{Transition: replayTransitionObserverReady})
	return nil
}

func (t *postWithdrawalTracker) advertisementWithdrawn(at time.Time) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.ready || t.withdrawn || t.finished || at.Before(t.readyAt) {
		return errors.New("post-withdrawal advertisement transition invalid")
	}
	t.withdrawn = true
	t.withdrawnAt = at
	t.trace = append(t.trace, replayTransition{
		AtMS:       at.Sub(t.readyAt).Milliseconds(),
		Transition: replayTransitionAdvertisementGone,
	})
	return nil
}

func (t *postWithdrawalTracker) recordInboundAttempt(at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.withdrawn || t.finished || at.Before(t.withdrawnAt) {
		return
	}
	t.attempts++
	t.trace = append(t.trace, replayTransition{
		AtMS:       at.Sub(t.readyAt).Milliseconds(),
		Transition: replayTransitionInboundAttempt,
	})
}

func (t *postWithdrawalTracker) finish(at time.Time, minimumWindow time.Duration) (bool, []replayTransition, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if minimumWindow <= 0 || !t.withdrawn || t.finished || at.Before(t.withdrawnAt) || at.Sub(t.withdrawnAt) < minimumWindow {
		return false, nil, errors.New("post-withdrawal negative window transition invalid")
	}
	t.finished = true
	transition := replayTransitionNoInboundAttempt
	if t.attempts != 0 {
		transition = replayTransitionInboundAttempt
	}
	t.trace = append(t.trace, replayTransition{
		AtMS:       at.Sub(t.readyAt).Milliseconds(),
		Transition: transition,
	})
	trace := append([]replayTransition(nil), t.trace...)
	return t.attempts == 0, trace, nil
}

func replayNegativeObservations() (negativeObservation, negativeObservation, replayArtifact, error) {
	return replayNegativeObservationsFrom(g19ReplayFixture)
}

func replayNegativeObservationsFrom(payload []byte) (negativeObservation, negativeObservation, replayArtifact, error) {
	input, err := decodeReplayInput(payload)
	if err != nil {
		return negativeObservation{}, negativeObservation{}, replayArtifact{}, err
	}

	const (
		replayExpectedSKI   = "1111111111111111111111111111111111111111"
		replayUnexpectedSKI = "2222222222222222222222222222222222222222"
	)
	base := time.Unix(0, 0).UTC()
	tracker := &postWithdrawalTracker{}
	denied := false
	windowFinished := false
	var denialTrace []replayTransition
	var reconnectTrace []replayTransition

	for _, event := range input.Events {
		if windowFinished {
			return negativeObservation{}, negativeObservation{}, replayArtifact{}, errors.New("G19 replay contains events after the terminal negative window")
		}
		at := base.Add(time.Duration(event.AtMS) * time.Millisecond)
		switch event.Type {
		case replayEventInboundAttempt:
			peerSKI := replayExpectedSKI
			if event.Peer == replayPeerUnexpected {
				peerSKI = replayUnexpectedSKI
			}
			allowed := strings.EqualFold(strings.TrimSpace(peerSKI), replayExpectedSKI)
			transition := replayTransitionAccessAllowed
			if !allowed {
				transition = replayTransitionAccessDenied
				denied = true
			}
			denialTrace = append(denialTrace, replayTransition{AtMS: event.AtMS, Transition: transition})
			tracker.recordInboundAttempt(at)
		case replayEventObserverReady:
			if err := tracker.observerReady(at); err != nil {
				return negativeObservation{}, negativeObservation{}, replayArtifact{}, err
			}
		case replayEventAdvertisementWithdrawn:
			if err := tracker.advertisementWithdrawn(at); err != nil {
				return negativeObservation{}, negativeObservation{}, replayArtifact{}, err
			}
		case replayEventNegativeWindowElapsed:
			if windowFinished {
				return negativeObservation{}, negativeObservation{}, replayArtifact{}, errors.New("G19 replay negative window completed more than once")
			}
			var satisfied bool
			satisfied, reconnectTrace, err = tracker.finish(at, time.Duration(input.NegativeWindowMS)*time.Millisecond)
			if err != nil {
				return negativeObservation{}, negativeObservation{}, replayArtifact{}, err
			}
			if !satisfied {
				return negativeObservation{}, negativeObservation{}, replayArtifact{}, errors.New("G19 replay observed a reconnect attempt after withdrawal")
			}
			windowFinished = true
		}
	}
	if !denied {
		return negativeObservation{}, negativeObservation{}, replayArtifact{}, errors.New("G19 replay did not derive a denied-access terminal state")
	}
	if !windowFinished {
		return negativeObservation{}, negativeObservation{}, replayArtifact{}, errors.New("G19 replay did not derive a reconnect-failure terminal state")
	}

	deniedHash := derivedReplayTraceHash(denialTrace)
	reconnectHash := derivedReplayTraceHash(reconnectTrace)
	artifact := replayArtifact{
		Path:                        g19ReplayFixturePath,
		SHA256:                      fullDigestRef(payload),
		DeniedAccessTraceSHA256:     deniedHash,
		ReconnectFailureTraceSHA256: reconnectHash,
	}
	return negativeObservation{Satisfied: true, Authority: negativeAuthorityCIReplay, EvidenceHash: deniedHash},
		negativeObservation{Satisfied: true, Authority: negativeAuthorityCIReplay, EvidenceHash: reconnectHash},
		artifact,
		nil
}

func decodeReplayInput(payload []byte) (replayInput, error) {
	var input replayInput
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return replayInput{}, fmt.Errorf("decode G19 replay input: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return replayInput{}, errors.New("G19 replay input contains trailing data")
	}
	if input.SchemaVersion != 1 || input.NegativeWindowMS <= 0 || input.NegativeWindowMS > 60_000 || len(input.Events) == 0 {
		return replayInput{}, errors.New("G19 replay input header invalid")
	}
	previousAt := int64(-1)
	for index := range input.Events {
		event := &input.Events[index]
		event.Type = strings.TrimSpace(event.Type)
		event.Peer = strings.TrimSpace(event.Peer)
		if event.AtMS < 0 || event.AtMS < previousAt {
			return replayInput{}, errors.New("G19 replay events are not time ordered")
		}
		previousAt = event.AtMS
		switch event.Type {
		case replayEventInboundAttempt:
			if event.Peer != replayPeerExpected && event.Peer != replayPeerUnexpected {
				return replayInput{}, errors.New("G19 replay inbound attempt peer invalid")
			}
		case replayEventObserverReady, replayEventAdvertisementWithdrawn, replayEventNegativeWindowElapsed:
			if event.Peer != "" {
				return replayInput{}, errors.New("G19 replay non-peer event includes a peer")
			}
		default:
			return replayInput{}, fmt.Errorf("G19 replay event type %q unsupported", event.Type)
		}
	}
	return input, nil
}

func derivedReplayTraceHash(trace []replayTransition) string {
	payload, err := json.Marshal(struct {
		SchemaVersion int                `json:"schema_version"`
		Transitions   []replayTransition `json:"transitions"`
	}{SchemaVersion: 1, Transitions: trace})
	if err != nil || len(trace) == 0 {
		return "sha256:invalid"
	}
	return fullDigestRef(payload)
}

type liveGateEvidence struct {
	SchemaVersion      int                     `json:"schema_version"`
	Gate               string                  `json:"gate"`
	CaseBinding        liveCaseBinding         `json:"case_binding"`
	Repo               evidenceRepo            `json:"repo"`
	Commands           []string                `json:"commands"`
	Environment        evidenceEnvironment     `json:"environment"`
	TrustPreconditions trustPreconditions      `json:"trust_preconditions"`
	OperatorLiveProof  operatorLiveProof       `json:"operator_live_proof"`
	CIReplayAuthority  ciReplayAuthority       `json:"ci_replay_authority"`
	NegativeCases      negativeCaseEvidence    `json:"negative_cases"`
	PublicRedaction    publicRedactionEvidence `json:"public_redaction"`
	OwnerAcceptance    ownerAcceptance         `json:"owner_acceptance"`
}

type liveCaseBinding struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	ResultHash string `json:"result_hash"`
}

type evidenceRepo struct {
	Name   string `json:"name"`
	Branch string `json:"branch"`
	Commit string `json:"commit"`
}

type evidenceEnvironment struct {
	TimestampUTC time.Time         `json:"timestamp_utc"`
	GoVersion    string            `json:"go_version"`
	ToolVersions map[string]string `json:"tool_versions,omitempty"`
	TopologyRef  string            `json:"topology_ref"`
}

type trustPreconditions struct {
	LocalIdentityState     string `json:"local_identity_state"`
	ExpectedRemoteApproved bool   `json:"expected_remote_approved_before_start"`
	AutoAcceptEnabled      bool   `json:"auto_accept_enabled"`
	DiscoveryIsolation     string `json:"eebus_discovery_isolation"`
	OperatorWindow         string `json:"operator_window"`
}

type operatorLiveProof struct {
	Result                  string        `json:"result"`
	TrustVisible            bool          `json:"trust_visible"`
	RunNonceRef             string        `json:"run_nonce_ref"`
	RunRef                  string        `json:"run_ref"`
	ChallengeRef            string        `json:"challenge_ref"`
	ExpectedRemoteDigest    string        `json:"expected_remote_digest"`
	InterfaceRef            string        `json:"interface_ref"`
	PortRef                 string        `json:"port_ref"`
	ConnectionGenerationRef string        `json:"connection_generation_ref"`
	ChallengeIssuedAt       time.Time     `json:"challenge_issued_at"`
	FirstSPINECapturedAt    time.Time     `json:"first_spine_captured_at"`
	RunStartedAt            time.Time     `json:"run_started_at"`
	RunExpiresAt            time.Time     `json:"run_expires_at"`
	ObservedAt              time.Time     `json:"observed_at"`
	AcceptedAt              time.Time     `json:"accepted_at"`
	EvidenceRef             string        `json:"redacted_json_ref,omitempty"`
	TransportHash           string        `json:"transport_hash"`
	TranscriptHashes        []string      `json:"transcript_hashes"`
	FirstSPINEPayloadHash   string        `json:"first_post_access_spine_payload_hash"`
	FirstSPINEData          spineEvidence `json:"first_post_access_spine_data"`
	FirstSPINEDataHash      string        `json:"first_post_access_spine_data_hash"`
}

type ciReplayAuthority struct {
	Result        string           `json:"result"`
	Fixtures      []replayArtifact `json:"deterministic_replay_fixtures"`
	ReplayCommand string           `json:"replay_command"`
}

type replayArtifact struct {
	Path                        string `json:"path"`
	SHA256                      string `json:"sha256"`
	DeniedAccessTraceSHA256     string `json:"denied_access_trace_sha256"`
	ReconnectFailureTraceSHA256 string `json:"reconnect_failure_trace_sha256"`
}

type negativeCaseEvidence struct {
	DeniedAccess     evidenceResult `json:"denied_access"`
	ReconnectFailure evidenceResult `json:"reconnect_failure"`
}

type evidenceResult struct {
	Result       string `json:"result"`
	Authority    string `json:"authority"`
	LiveObserved bool   `json:"live_observed"`
	EvidenceHash string `json:"evidence_hash"`
}

type publicRedactionEvidence struct {
	NoPacketCaptures    bool `json:"no_packet_artifacts"`
	NoRawTranscripts    bool `json:"no_transcript_material"`
	NoSensitiveMaterial bool `json:"no_sensitive_material"`
	NoRawIdentity       bool `json:"no_raw_identity"`
}

type ownerAcceptance struct {
	Accepted   bool      `json:"accepted"`
	AcceptedAt time.Time `json:"accepted_at"`
	Notes      string    `json:"notes,omitempty"`
}

var (
	hex40Pattern          = regexp.MustCompile(`(?i)\b[0-9a-f]{40}\b`)
	sha256RefPattern      = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	hmacSHA256RefPattern  = regexp.MustCompile(`^hmac-sha256:[0-9a-f]{64}$`)
	gitCommitPattern      = regexp.MustCompile(`^[0-9a-f]{40}$`)
	shipIDPattern         = regexp.MustCompile(`(?i)\bship[_-]?id\s*[:=]`)
	fingerprintPattern    = regexp.MustCompile(`(?i)\bfingerprint\s*[:=]`)
	serialPattern         = regexp.MustCompile(`(?i)\bserial\s*[:=]`)
	rawTranscriptPattern  = regexp.MustCompile(`(?i)raw[_-]?transcript\s*[:=]|"transcript"\s*:`)
	packetCapturePattern  = regexp.MustCompile(`(?i)\.(pcap|pcapng)\b|packet[_-]?capture\s*[:=]`)
	pairingHistoryPattern = regexp.MustCompile(`(?i)pairing[_-]?history\s*[:=]`)
)

func (e liveGateEvidence) validate() error {
	e = e.normalized()
	trustedRepo := currentRepoEvidence()
	switch {
	case e.SchemaVersion != 1:
		return errors.New("unsupported live evidence schema")
	case e.Gate != caseDirectAccess:
		return errors.New("live evidence gate mismatch")
	case e.CaseBinding.ID != caseDirectAccess || e.CaseBinding.Status != resultPass || !validSHA256Ref(e.CaseBinding.ResultHash):
		return errors.New("G19 case binding incomplete")
	case e.Repo != trustedRepo || e.Repo.Name != "helianthus-eebusreg" || e.Repo.Branch == "" || !gitCommitPattern.MatchString(e.Repo.Commit):
		return errors.New("repo evidence incomplete")
	case len(e.Commands) == 0:
		return errors.New("command evidence required")
	case e.Environment.TimestampUTC.IsZero() || e.Environment.GoVersion == "" || !validHMACSHA256Ref(e.Environment.TopologyRef):
		return errors.New("environment evidence incomplete")
	case e.TrustPreconditions.LocalIdentityState != "disposable-in-memory" || !e.TrustPreconditions.ExpectedRemoteApproved || e.TrustPreconditions.AutoAcceptEnabled || e.TrustPreconditions.DiscoveryIsolation != "internal-mdns-disabled" || e.TrustPreconditions.OperatorWindow == "":
		return errors.New("trust preconditions incomplete")
	case e.OperatorLiveProof.Result != resultPass || !e.OperatorLiveProof.TrustVisible:
		return errors.New("operator live proof must pass independently")
	case !validSHA256Ref(e.OperatorLiveProof.RunNonceRef) || !validSHA256Ref(e.OperatorLiveProof.RunRef) || !validHMACSHA256Ref(e.OperatorLiveProof.ChallengeRef) || !validHMACSHA256Ref(e.OperatorLiveProof.ExpectedRemoteDigest) || !validHMACSHA256Ref(e.OperatorLiveProof.InterfaceRef) || !validHMACSHA256Ref(e.OperatorLiveProof.PortRef) || !validHMACSHA256Ref(e.OperatorLiveProof.ConnectionGenerationRef):
		return errors.New("operator live proof run binding incomplete")
	case e.OperatorLiveProof.RunStartedAt.IsZero() || e.OperatorLiveProof.RunExpiresAt.IsZero() || e.OperatorLiveProof.ChallengeIssuedAt.IsZero() || e.OperatorLiveProof.FirstSPINECapturedAt.IsZero() || e.OperatorLiveProof.ObservedAt.IsZero() || e.OperatorLiveProof.AcceptedAt.IsZero() || e.OperatorLiveProof.RunExpiresAt.Before(e.OperatorLiveProof.RunStartedAt) || e.OperatorLiveProof.FirstSPINECapturedAt.Before(e.OperatorLiveProof.RunStartedAt) || e.OperatorLiveProof.ObservedAt.Before(e.OperatorLiveProof.RunStartedAt) || e.OperatorLiveProof.FirstSPINECapturedAt.After(e.OperatorLiveProof.ChallengeIssuedAt) || e.OperatorLiveProof.ObservedAt.After(e.OperatorLiveProof.ChallengeIssuedAt) || e.OperatorLiveProof.AcceptedAt.Before(e.OperatorLiveProof.ChallengeIssuedAt) || e.OperatorLiveProof.AcceptedAt.After(e.OperatorLiveProof.RunExpiresAt):
		return errors.New("operator live proof timestamps invalid")
	case !e.Environment.TimestampUTC.Equal(e.OperatorLiveProof.AcceptedAt) || e.Environment.TimestampUTC.Before(e.OperatorLiveProof.RunStartedAt) || e.Environment.TimestampUTC.After(e.OperatorLiveProof.RunExpiresAt):
		return errors.New("environment timestamp is outside the bound run")
	case !validSHA256Ref(e.OperatorLiveProof.EvidenceRef) || !validSHA256Ref(e.OperatorLiveProof.TransportHash) || !validSHA256Refs(e.OperatorLiveProof.TranscriptHashes) || !validSHA256Ref(e.OperatorLiveProof.FirstSPINEPayloadHash) || e.OperatorLiveProof.FirstSPINEData.empty() || e.OperatorLiveProof.FirstSPINEData.validate() != nil || e.OperatorLiveProof.FirstSPINEDataHash != e.OperatorLiveProof.FirstSPINEData.dataHash():
		return errors.New("operator live proof evidence incomplete")
	case e.CIReplayAuthority.Result != resultPass || !validReplayArtifacts(e.CIReplayAuthority.Fixtures) || e.CIReplayAuthority.ReplayCommand == "":
		return errors.New("CI replay authority incomplete")
	case e.NegativeCases.DeniedAccess.validate() != nil || !negativeEvidenceBoundToAuthority(e.NegativeCases.DeniedAccess, e.CIReplayAuthority.Fixtures, replayTraceDeniedAccess):
		return errors.New("denied-access evidence incomplete")
	case e.NegativeCases.ReconnectFailure.validate() != nil || !negativeEvidenceBoundToAuthority(e.NegativeCases.ReconnectFailure, e.CIReplayAuthority.Fixtures, replayTraceReconnectFailure):
		return errors.New("reconnect-failure evidence incomplete")
	case !e.PublicRedaction.NoPacketCaptures || !e.PublicRedaction.NoRawTranscripts || !e.PublicRedaction.NoSensitiveMaterial || !e.PublicRedaction.NoRawIdentity:
		return errors.New("public redaction declaration incomplete")
	case !e.OwnerAcceptance.Accepted || e.OwnerAcceptance.AcceptedAt.IsZero() || !e.OwnerAcceptance.AcceptedAt.Equal(e.OperatorLiveProof.AcceptedAt):
		return errors.New("owner acceptance required")
	}

	payload, err := e.jsonBytes()
	if err != nil {
		return err
	}
	return validateLiveRedaction(payload)
}

func (e liveGateEvidence) validateForCase(result caseResult, repo evidenceRepo) error {
	if err := e.validate(); err != nil {
		return err
	}
	result = result.normalized()
	e = e.normalized()
	if e.Repo != repo {
		return errors.New("live evidence provenance does not match report provenance")
	}
	if e.CaseBinding.ID != result.ID || e.CaseBinding.Status != result.Status || e.CaseBinding.ResultHash != result.dataHash() {
		return errors.New("live evidence is not bound to the reported G19 result")
	}
	return nil
}

func (e liveGateEvidence) normalized() liveGateEvidence {
	normalized := e
	normalized.Gate = strings.TrimSpace(e.Gate)
	normalized.CaseBinding.ID = strings.TrimSpace(e.CaseBinding.ID)
	normalized.CaseBinding.Status = strings.TrimSpace(e.CaseBinding.Status)
	normalized.CaseBinding.ResultHash = strings.TrimSpace(e.CaseBinding.ResultHash)
	normalized.Repo.Name = strings.TrimSpace(e.Repo.Name)
	normalized.Repo.Branch = strings.TrimSpace(e.Repo.Branch)
	normalized.Repo.Commit = strings.TrimSpace(e.Repo.Commit)
	normalized.Commands = sortedUnique(e.Commands)
	normalized.Environment.GoVersion = strings.TrimSpace(e.Environment.GoVersion)
	normalized.Environment.TopologyRef = strings.TrimSpace(e.Environment.TopologyRef)
	normalized.TrustPreconditions.LocalIdentityState = strings.TrimSpace(e.TrustPreconditions.LocalIdentityState)
	normalized.TrustPreconditions.DiscoveryIsolation = strings.TrimSpace(e.TrustPreconditions.DiscoveryIsolation)
	normalized.TrustPreconditions.OperatorWindow = strings.TrimSpace(e.TrustPreconditions.OperatorWindow)
	normalized.OperatorLiveProof.Result = strings.TrimSpace(e.OperatorLiveProof.Result)
	normalized.OperatorLiveProof.RunNonceRef = strings.TrimSpace(e.OperatorLiveProof.RunNonceRef)
	normalized.OperatorLiveProof.RunRef = strings.TrimSpace(e.OperatorLiveProof.RunRef)
	normalized.OperatorLiveProof.ChallengeRef = strings.TrimSpace(e.OperatorLiveProof.ChallengeRef)
	normalized.OperatorLiveProof.ExpectedRemoteDigest = strings.TrimSpace(e.OperatorLiveProof.ExpectedRemoteDigest)
	normalized.OperatorLiveProof.InterfaceRef = strings.TrimSpace(e.OperatorLiveProof.InterfaceRef)
	normalized.OperatorLiveProof.PortRef = strings.TrimSpace(e.OperatorLiveProof.PortRef)
	normalized.OperatorLiveProof.ConnectionGenerationRef = strings.TrimSpace(e.OperatorLiveProof.ConnectionGenerationRef)
	normalized.OperatorLiveProof.EvidenceRef = strings.TrimSpace(e.OperatorLiveProof.EvidenceRef)
	normalized.OperatorLiveProof.TransportHash = strings.TrimSpace(e.OperatorLiveProof.TransportHash)
	normalized.CIReplayAuthority.Result = strings.TrimSpace(e.CIReplayAuthority.Result)
	normalized.CIReplayAuthority.ReplayCommand = strings.TrimSpace(e.CIReplayAuthority.ReplayCommand)
	normalized.CIReplayAuthority.Fixtures = normalizedReplayArtifacts(e.CIReplayAuthority.Fixtures)
	normalized.OperatorLiveProof.TranscriptHashes = sortedUnique(e.OperatorLiveProof.TranscriptHashes)
	normalized.OperatorLiveProof.FirstSPINEPayloadHash = strings.TrimSpace(e.OperatorLiveProof.FirstSPINEPayloadHash)
	normalized.OperatorLiveProof.FirstSPINEData = e.OperatorLiveProof.FirstSPINEData.normalized()
	normalized.OperatorLiveProof.FirstSPINEDataHash = strings.TrimSpace(e.OperatorLiveProof.FirstSPINEDataHash)
	normalized.NegativeCases.DeniedAccess = e.NegativeCases.DeniedAccess.normalized()
	normalized.NegativeCases.ReconnectFailure = e.NegativeCases.ReconnectFailure.normalized()
	normalized.OwnerAcceptance.Notes = strings.TrimSpace(e.OwnerAcceptance.Notes)
	if len(e.Environment.ToolVersions) != 0 {
		normalized.Environment.ToolVersions = make(map[string]string, len(e.Environment.ToolVersions))
		for _, sourceKey := range sortedStringMapKeys(e.Environment.ToolVersions) {
			key := strings.TrimSpace(sourceKey)
			if key != "" {
				normalized.Environment.ToolVersions[key] = strings.TrimSpace(e.Environment.ToolVersions[sourceKey])
			}
		}
		if len(normalized.Environment.ToolVersions) == 0 {
			normalized.Environment.ToolVersions = nil
		}
	} else {
		normalized.Environment.ToolVersions = nil
	}
	return normalized
}

func (e liveGateEvidence) jsonBytes() ([]byte, error) {
	buf := &bytes.Buffer{}
	encoder := json.NewEncoder(buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(e.normalized()); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

func (e liveGateEvidence) dataHash() string {
	payload, err := e.jsonBytes()
	if err != nil {
		return "sha256:invalid"
	}
	return fullDigestRef(payload)
}

func validateLiveRedaction(payload []byte) error {
	if err := validatePublicRedaction(payload); err != nil {
		return err
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if err := validateLiveIdentityValue(value, nil); err != nil {
		return err
	}
	text := string(payload)
	switch {
	case shipIDPattern.MatchString(text):
		return errors.New("public evidence contains raw SHIP ID")
	case fingerprintPattern.MatchString(text):
		return errors.New("public evidence contains raw fingerprint")
	case serialPattern.MatchString(text):
		return errors.New("public evidence contains raw serial")
	case rawTranscriptPattern.MatchString(text):
		return errors.New("public evidence contains raw transcript")
	case packetCapturePattern.MatchString(text):
		return errors.New("public evidence contains packet capture")
	case pairingHistoryPattern.MatchString(text):
		return errors.New("public evidence contains pairing history")
	}
	return nil
}

func sortedUnique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func validSHA256Ref(value string) bool {
	return sha256RefPattern.MatchString(strings.TrimSpace(value))
}

func validHMACSHA256Ref(value string) bool {
	return hmacSHA256RefPattern.MatchString(strings.TrimSpace(value))
}

func validSHA256Refs(values []string) bool {
	if len(values) == 0 {
		return false
	}
	for _, value := range values {
		if !validSHA256Ref(value) {
			return false
		}
	}
	return true
}

func fullDigestRef(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (e evidenceResult) normalized() evidenceResult {
	e.Result = strings.TrimSpace(e.Result)
	e.Authority = strings.TrimSpace(e.Authority)
	e.EvidenceHash = strings.TrimSpace(e.EvidenceHash)
	return e
}

func (e evidenceResult) validate() error {
	e = e.normalized()
	if e.Result != resultPass || !validSHA256Ref(e.EvidenceHash) {
		return errors.New("negative result is incomplete")
	}
	switch e.Authority {
	case negativeAuthorityCIReplay:
		if e.LiveObserved {
			return errors.New("CI replay evidence cannot claim live observation")
		}
	case negativeAuthorityLiveNetwork:
		if !e.LiveObserved {
			return errors.New("live network evidence must declare live observation")
		}
	default:
		return errors.New("negative result authority is unsupported")
	}
	return nil
}

func normalizedReplayArtifacts(values []replayArtifact) []replayArtifact {
	seen := make(map[string]struct{}, len(values))
	result := make([]replayArtifact, 0, len(values))
	for _, value := range values {
		value.Path = strings.TrimSpace(value.Path)
		value.SHA256 = strings.TrimSpace(value.SHA256)
		value.DeniedAccessTraceSHA256 = strings.TrimSpace(value.DeniedAccessTraceSHA256)
		value.ReconnectFailureTraceSHA256 = strings.TrimSpace(value.ReconnectFailureTraceSHA256)
		if value.Path == "" || value.SHA256 == "" || value.DeniedAccessTraceSHA256 == "" || value.ReconnectFailureTraceSHA256 == "" {
			continue
		}
		key := strings.Join([]string{value.Path, value.SHA256, value.DeniedAccessTraceSHA256, value.ReconnectFailureTraceSHA256}, "\x00")
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Path == result[j].Path {
			if result[i].SHA256 == result[j].SHA256 {
				left := result[i].DeniedAccessTraceSHA256 + "\x00" + result[i].ReconnectFailureTraceSHA256
				right := result[j].DeniedAccessTraceSHA256 + "\x00" + result[j].ReconnectFailureTraceSHA256
				return left < right
			}
			return result[i].SHA256 < result[j].SHA256
		}
		return result[i].Path < result[j].Path
	})
	return result
}

func validReplayArtifacts(values []replayArtifact) bool {
	values = normalizedReplayArtifacts(values)
	if len(values) == 0 {
		return false
	}
	for _, value := range values {
		if !validSHA256Ref(value.SHA256) || !validSHA256Ref(value.DeniedAccessTraceSHA256) || !validSHA256Ref(value.ReconnectFailureTraceSHA256) {
			return false
		}
	}
	return true
}

func negativeEvidenceBoundToAuthority(result evidenceResult, fixtures []replayArtifact, traceKind string) bool {
	result = result.normalized()
	if result.Authority == negativeAuthorityLiveNetwork {
		return result.LiveObserved
	}
	if result.Authority != negativeAuthorityCIReplay || result.LiveObserved {
		return false
	}
	for _, fixture := range normalizedReplayArtifacts(fixtures) {
		var traceHash string
		switch traceKind {
		case replayTraceDeniedAccess:
			traceHash = fixture.DeniedAccessTraceSHA256
		case replayTraceReconnectFailure:
			traceHash = fixture.ReconnectFailureTraceSHA256
		default:
			return false
		}
		if traceHash == result.EvidenceHash {
			return true
		}
	}
	return false
}

func validateLiveIdentityValue(value any, path []string) error {
	switch typed := value.(type) {
	case map[string]any:
		for childKey, child := range typed {
			compact := strings.NewReplacer("_", "", "-", "", ".", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(childKey)))
			for _, token := range []string{"ski", "shipid", "serial", "fingerprint", "pairinghistory", "rawtranscript", "packetcapture"} {
				if strings.Contains(compact, token) {
					return fmt.Errorf("public evidence contains raw-identity key %q", childKey)
				}
			}
			if err := validateLiveIdentityValue(child, appendJSONPath(path, childKey)); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := validateLiveIdentityValue(child, path); err != nil {
				return err
			}
		}
	case string:
		matches := hex40Pattern.FindAllString(typed, -1)
		if len(matches) == 0 {
			return nil
		}
		if allowedGitCommitPath(path) && gitCommitPattern.MatchString(strings.TrimSpace(typed)) {
			return nil
		}
		return errors.New("public evidence contains raw SKI-like identity")
	}
	return nil
}
