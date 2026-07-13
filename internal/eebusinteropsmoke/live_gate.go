package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	caseDirectAccess                = "EEBUS-G19"
	accessDirectionInboundFromVR940 = "inbound-from-vr940-client"
)

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
	failure := func(code string) caseResult {
		return caseResult{ID: caseLive, Status: resultFail, Evidence: []string{"g17-evaluation-completed"}, Error: code}
	}
	switch {
	case observation.Direction != accessDirectionInboundFromVR940:
		return failure("vr940_server_role_claim_forbidden")
	case observation.SelectedInterface == "":
		return failure("selected_interface_required")
	case observation.SelectedPort < 1 || observation.SelectedPort > 65535:
		return failure("selected_port_required")
	case !observation.LocalAdvertisementSeen:
		return failure("local_advertisement_not_observed")
	case !observation.LANObserverConfirmed:
		return failure("lan_observer_confirmation_required")
	case !observation.OperatorTrustVisible:
		return failure("operator_trust_visibility_required")
	case !observation.TTLWithdrawalObserved:
		return failure("ttl_withdrawal_not_observed")
	case !observation.NoConnectionAfterWithdraw:
		return failure("post_withdrawal_negative_not_observed")
	}

	return caseResult{
		ID:     caseLive,
		Status: resultPass,
		Evidence: []string{
			"g17-lan-observer-confirmed",
			"g17-local-ship-advertisement-observed",
			"g17-myvaillant-trust-visible",
			"g17-post-withdrawal-negative-observed",
			"g17-ttl-withdrawal-observed",
		},
		Details: map[string]string{
			"direction":     accessDirectionInboundFromVR940,
			"interface_ref": refLabel("iface", observation.SelectedInterface),
			"port_ref":      refLabel("port", fmt.Sprintf("%d", observation.SelectedPort)),
		},
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
	Direction                string           `json:"direction"`
	Stages                   []transportStage `json:"stages"`
	FirstSPINEData           spineEvidence    `json:"first_spine_data"`
	DeniedAccessObserved     bool             `json:"denied_access_observed"`
	ReconnectFailureObserved bool             `json:"reconnect_failure_observed"`
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
	return len(e.EntityTypes) == 0 && len(e.FeatureTypes) == 0 && len(e.UseCaseRefs) == 0
}

func (e spineEvidence) dataHash() string {
	payload, _ := json.Marshal(e.normalized())
	return fullDigestRef(payload)
}

func evaluateG19(observation g19Observation) caseResult {
	failure := func(code string) caseResult {
		return caseResult{ID: caseDirectAccess, Status: resultFail, Evidence: []string{"g19-evaluation-completed"}, Error: code}
	}
	if observation.Direction != accessDirectionInboundFromVR940 {
		return failure("vr940_server_role_claim_forbidden")
	}
	if len(observation.Stages) != len(requiredTransportStages) {
		return failure("transport_stage_sequence_incomplete")
	}
	for i, required := range requiredTransportStages {
		if observation.Stages[i] != required {
			return failure("transport_stage_sequence_invalid")
		}
	}
	if observation.FirstSPINEData.empty() {
		return failure("first_spine_data_required")
	}
	if !observation.DeniedAccessObserved {
		return failure("denied_access_negative_required")
	}
	if !observation.ReconnectFailureObserved {
		return failure("reconnect_failure_negative_required")
	}

	return caseResult{
		ID:     caseDirectAccess,
		Status: resultPass,
		Evidence: []string{
			"g19-denied-access-negative-observed",
			"g19-first-post-access-spine-data-captured",
			"g19-inbound-transport-sequence-completed",
			"g19-reconnect-failure-negative-observed",
		},
		Details: map[string]string{
			"direction":       accessDirectionInboundFromVR940,
			"spine_data_hash": observation.FirstSPINEData.dataHash(),
		},
	}
}

type liveGateEvidence struct {
	SchemaVersion      int                     `json:"schema_version"`
	Gate               string                  `json:"gate"`
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
	LocalIdentityState string `json:"local_identity_state"`
	PreseededAllowlist bool   `json:"preseeded_trust_or_allowlist"`
	OperatorWindow     string `json:"operator_window"`
}

type operatorLiveProof struct {
	Result           string        `json:"result"`
	TrustVisible     bool          `json:"trust_visible"`
	EvidenceRef      string        `json:"redacted_json_ref,omitempty"`
	TranscriptHashes []string      `json:"transcript_hashes"`
	FirstSPINEData   spineEvidence `json:"first_post_access_spine_data"`
}

type ciReplayAuthority struct {
	Result        string   `json:"result"`
	Fixtures      []string `json:"deterministic_replay_fixtures"`
	ReplayCommand string   `json:"replay_command"`
}

type negativeCaseEvidence struct {
	DeniedAccess     evidenceResult `json:"denied_access"`
	ReconnectFailure evidenceResult `json:"reconnect_failure"`
}

type evidenceResult struct {
	Result       string `json:"result"`
	EvidenceHash string `json:"evidence_hash"`
}

type publicRedactionEvidence struct {
	NoPacketCaptures       bool `json:"no_packet_captures"`
	NoRawTranscripts       bool `json:"no_raw_transcripts"`
	NoSecretsOrTrustStores bool `json:"no_keys_pem_tokens_trust_stores"`
	NoRawIdentity          bool `json:"no_raw_ski_shipid_ip_mac_serial"`
}

type ownerAcceptance struct {
	Accepted   bool      `json:"accepted"`
	AcceptedAt time.Time `json:"accepted_at"`
	Notes      string    `json:"notes,omitempty"`
}

var (
	hex40Pattern          = regexp.MustCompile(`(?i)\b[0-9a-f]{40}\b`)
	sha256RefPattern      = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	shipIDPattern         = regexp.MustCompile(`(?i)\bship[_-]?id\s*[:=]`)
	serialPattern         = regexp.MustCompile(`(?i)\bserial\s*[:=]`)
	rawTranscriptPattern  = regexp.MustCompile(`(?i)raw[_-]?transcript\s*[:=]|"transcript"\s*:`)
	packetCapturePattern  = regexp.MustCompile(`(?i)\.(pcap|pcapng)\b|packet[_-]?capture\s*[:=]`)
	pairingHistoryPattern = regexp.MustCompile(`(?i)pairing[_-]?history\s*[:=]`)
)

func (e liveGateEvidence) validate() error {
	switch {
	case e.SchemaVersion != 1:
		return errors.New("unsupported live evidence schema")
	case e.Gate != caseDirectAccess:
		return errors.New("live evidence gate mismatch")
	case e.Repo.Name != "helianthus-eebusreg" || e.Repo.Branch == "" || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(e.Repo.Commit):
		return errors.New("repo evidence incomplete")
	case len(e.Commands) == 0:
		return errors.New("command evidence required")
	case e.Environment.TimestampUTC.IsZero() || e.Environment.GoVersion == "" || e.Environment.TopologyRef == "":
		return errors.New("environment evidence incomplete")
	case e.TrustPreconditions.LocalIdentityState == "" || e.TrustPreconditions.OperatorWindow == "":
		return errors.New("trust preconditions incomplete")
	case e.OperatorLiveProof.Result != resultPass || !e.OperatorLiveProof.TrustVisible:
		return errors.New("operator live proof must pass independently")
	case !validSHA256Ref(e.OperatorLiveProof.EvidenceRef) || !validSHA256Refs(e.OperatorLiveProof.TranscriptHashes) || e.OperatorLiveProof.FirstSPINEData.empty():
		return errors.New("operator live proof evidence incomplete")
	case e.CIReplayAuthority.Result != resultPass || len(e.CIReplayAuthority.Fixtures) == 0 || e.CIReplayAuthority.ReplayCommand == "":
		return errors.New("CI replay authority incomplete")
	case e.NegativeCases.DeniedAccess.Result != resultPass || !validSHA256Ref(e.NegativeCases.DeniedAccess.EvidenceHash):
		return errors.New("denied-access evidence incomplete")
	case e.NegativeCases.ReconnectFailure.Result != resultPass || !validSHA256Ref(e.NegativeCases.ReconnectFailure.EvidenceHash):
		return errors.New("reconnect-failure evidence incomplete")
	case !e.PublicRedaction.NoPacketCaptures || !e.PublicRedaction.NoRawTranscripts || !e.PublicRedaction.NoSecretsOrTrustStores || !e.PublicRedaction.NoRawIdentity:
		return errors.New("public redaction declaration incomplete")
	case !e.OwnerAcceptance.Accepted || e.OwnerAcceptance.AcceptedAt.IsZero():
		return errors.New("owner acceptance required")
	}

	payload, err := e.jsonBytes()
	if err != nil {
		return err
	}
	return validateLiveRedaction(payload, e.Repo.Commit)
}

func (e liveGateEvidence) normalized() liveGateEvidence {
	normalized := e
	normalized.Commands = sortedUnique(e.Commands)
	normalized.CIReplayAuthority.Fixtures = sortedUnique(e.CIReplayAuthority.Fixtures)
	normalized.OperatorLiveProof.TranscriptHashes = sortedUnique(e.OperatorLiveProof.TranscriptHashes)
	normalized.OperatorLiveProof.FirstSPINEData = e.OperatorLiveProof.FirstSPINEData.normalized()
	if len(e.Environment.ToolVersions) != 0 {
		normalized.Environment.ToolVersions = make(map[string]string, len(e.Environment.ToolVersions))
		for key, value := range e.Environment.ToolVersions {
			normalized.Environment.ToolVersions[key] = value
		}
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

func validateLiveRedaction(payload []byte, allowedCommit string) error {
	if err := validatePublicRedaction(payload); err != nil {
		return err
	}
	text := string(payload)
	for _, value := range hex40Pattern.FindAllString(text, -1) {
		if !strings.EqualFold(value, allowedCommit) {
			return errors.New("public evidence contains raw SKI-like identity")
		}
	}
	switch {
	case shipIDPattern.MatchString(text):
		return errors.New("public evidence contains raw SHIP ID")
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
	return sha256RefPattern.MatchString(value)
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
