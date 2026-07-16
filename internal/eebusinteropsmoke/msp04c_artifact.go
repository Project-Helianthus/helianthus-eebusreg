package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"sort"
	"strings"
)

type msp04cArtifactCommand uint8

const (
	msp04cCommandAPIFreeze msp04cArtifactCommand = iota + 1
	msp04cCommandDiffCheck
	msp04cCommandUnit
)

type msp04cSubcase uint8

const (
	msp04cG10Clone msp04cSubcase = iota + 1
	msp04cG10HostBindingMismatch
	msp04cG10HostKeyUnavailable
	msp04cG10ManifestRollback
	msp04cG10ControlRollback
	msp04cG10DurabilityUnknown
	msp04cG10CopiedCurrentRepair
	msp04cG10InactiveParentRepair
	msp04cG10SameNumberBranchConflict
	msp04cG11Vector0
	msp04cG11Vector1
	msp04cG11Vector2
	msp04cG11Vector3
	msp04cG11Vector4
	msp04cG11CheckpointSix
	msp04cG11CheckpointFour
	msp04cG11RestartFour
	msp04cG11PreDeadline
	msp04cG11DeadlineReady
	msp04cG11Bounds
	msp04cG11NoActiveEviction
	msp04cG16APIFreeze
	msp04cG16SuccessRedaction
	msp04cG16FailureRedaction
)

type msp04cSubcaseObservation struct {
	Subcase      msp04cSubcase
	State        string
	Reason       string
	Outcome      string
	Count        uint64
	DelaySeconds uint64
}

type msp04cArtifactInput struct {
	Repo        string
	Branch      string
	Commit      string
	Issue       string
	GoVersion   string
	GoWork      string
	GoToolchain string
	RunSeed     [32]byte
	Commands    []msp04cArtifactCommand
	Transcript  []msp04cSubcaseObservation
}

type msp04cArtifactToolchain struct {
	GoVersion   string `json:"go_version"`
	GoWork      string `json:"go_work"`
	GoToolchain string `json:"go_toolchain"`
}

type msp04cArtifactCase struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	CaseLabel    string `json:"case_label"`
	State        string `json:"state"`
	Reason       string `json:"reason,omitempty"`
	Outcome      string `json:"outcome"`
	Count        uint64 `json:"count,omitempty"`
	DelaySeconds uint64 `json:"delay_seconds,omitempty"`
}

type msp04cArtifact struct {
	AuthScope      string                  `json:"auth_scope"`
	Cases          []msp04cArtifactCase    `json:"cases"`
	Commands       []msp04cArtifactCommand `json:"commands"`
	Issue          string                  `json:"issue"`
	Repo           string                  `json:"repo"`
	RepoBranch     string                  `json:"repo_branch"`
	RepoCommit     string                  `json:"repo_commit"`
	Result         string                  `json:"result"`
	RunLabel       string                  `json:"run_label"`
	TemporaryPaths string                  `json:"temporary_paths"`
	Toolchain      msp04cArtifactToolchain `json:"toolchain"`
	Topology       string                  `json:"topology"`
	replaySeed     [32]byte
}

type msp04cExpectedSubcase struct {
	gate         string
	state        string
	reason       string
	outcome      string
	count        uint64
	delaySeconds uint64
}

var msp04cCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

var msp04cRequiredSubcases = map[msp04cSubcase]msp04cExpectedSubcase{
	msp04cG10Clone:                    {gate: "EEBUS-G10", state: "QUARANTINED", reason: "CLONE_DETECTED", outcome: "TRUST_DENIED"},
	msp04cG10HostBindingMismatch:      {gate: "EEBUS-G10", state: "QUARANTINED", reason: "HOST_BINDING_MISMATCH", outcome: "TRUST_DENIED"},
	msp04cG10HostKeyUnavailable:       {gate: "EEBUS-G10", state: "NO_LOCAL_IDENTITY", reason: "HOST_KEY_UNAVAILABLE", outcome: "TRUST_DENIED"},
	msp04cG10ManifestRollback:         {gate: "EEBUS-G10", state: "QUARANTINED", reason: "MANIFEST_GENERATION_ROLLBACK", outcome: "TRUST_DENIED"},
	msp04cG10ControlRollback:          {gate: "EEBUS-G10", state: "QUARANTINED", reason: "CONTROL_EPOCH_ROLLBACK", outcome: "TRUST_DENIED"},
	msp04cG10DurabilityUnknown:        {gate: "EEBUS-G10", state: "QUARANTINED", reason: "DURABILITY_UNKNOWN", outcome: "TRUST_DENIED"},
	msp04cG10CopiedCurrentRepair:      {gate: "EEBUS-G10", state: "UNPAIRED_LOCKED", outcome: "TRUST_DENIED"},
	msp04cG10InactiveParentRepair:     {gate: "EEBUS-G10", state: "UNPAIRED_LOCKED", outcome: "TRUST_DENIED"},
	msp04cG10SameNumberBranchConflict: {gate: "EEBUS-G10", state: "QUARANTINED", reason: "DURABILITY_UNKNOWN", outcome: "TRUST_DENIED"},
	msp04cG11Vector0:                  {gate: "EEBUS-G11", state: "RETRY_READY", outcome: "RETRY_DENIED"},
	msp04cG11Vector1:                  {gate: "EEBUS-G11", state: "BACKOFF_ACTIVE", outcome: "RETRY_DENIED", count: 1, delaySeconds: 3},
	msp04cG11Vector2:                  {gate: "EEBUS-G11", state: "BACKOFF_ACTIVE", outcome: "RETRY_DENIED", count: 2, delaySeconds: 6},
	msp04cG11Vector3:                  {gate: "EEBUS-G11", state: "BACKOFF_ACTIVE", outcome: "RETRY_DENIED", count: 3, delaySeconds: 10},
	msp04cG11Vector4:                  {gate: "EEBUS-G11", state: "ADMIN_HOLD", reason: "HANDSHAKE_ATTEMPT_LIMIT", outcome: "RETRY_DENIED", count: 4},
	msp04cG11CheckpointSix:            {gate: "EEBUS-G11", state: "BACKOFF_ACTIVE", outcome: "RETRY_DENIED", count: 2, delaySeconds: 6},
	msp04cG11CheckpointFour:           {gate: "EEBUS-G11", state: "BACKOFF_ACTIVE", outcome: "RETRY_DENIED", count: 2, delaySeconds: 4},
	msp04cG11RestartFour:              {gate: "EEBUS-G11", state: "BACKOFF_ACTIVE", outcome: "RETRY_DENIED", count: 2, delaySeconds: 4},
	msp04cG11PreDeadline:              {gate: "EEBUS-G11", state: "BACKOFF_ACTIVE", outcome: "RETRY_DENIED", count: 2, delaySeconds: 4},
	msp04cG11DeadlineReady:            {gate: "EEBUS-G11", state: "RETRY_READY", outcome: "RETRY_DENIED", count: 2},
	msp04cG11Bounds:                   {gate: "EEBUS-G11", state: "ADMIN_HOLD", reason: "ADMIN_HOLD", outcome: "RETRY_DENIED"},
	msp04cG11NoActiveEviction:         {gate: "EEBUS-G11", state: "ADMIN_HOLD", reason: "ADMIN_HOLD", outcome: "RETRY_DENIED"},
	msp04cG16APIFreeze:                {gate: "EEBUS-G16", state: "UNPAIRED_LOCKED", outcome: "PUBLIC_API_FROZEN"},
	msp04cG16SuccessRedaction:         {gate: "EEBUS-G16", state: "UNPAIRED_LOCKED", outcome: "PUBLIC_API_FROZEN"},
	msp04cG16FailureRedaction:         {gate: "EEBUS-G16", state: "UNPAIRED_LOCKED", outcome: "PUBLIC_API_FROZEN"},
}

func newMSP04CArtifact(input msp04cArtifactInput) (msp04cArtifact, error) {
	input.Repo = strings.TrimSpace(input.Repo)
	input.Branch = strings.TrimSpace(input.Branch)
	input.Commit = strings.TrimSpace(input.Commit)
	input.Issue = strings.TrimSpace(input.Issue)
	input.GoVersion = strings.TrimSpace(input.GoVersion)
	input.GoWork = strings.TrimSpace(input.GoWork)
	input.GoToolchain = strings.TrimSpace(input.GoToolchain)
	if input.Repo == "" || input.Branch == "" || input.Issue != "MSP-04C" || !msp04cCommitPattern.MatchString(input.Commit) || input.GoVersion == "" || input.GoWork == "" || input.GoToolchain == "" {
		return msp04cArtifact{}, errors.New("invalid MSP-04C artifact metadata")
	}
	seed := input.RunSeed
	if seed == [32]byte{} {
		var err error
		seed, err = newMSP04CRunSeed(rand.Reader)
		if err != nil {
			return msp04cArtifact{}, errors.New("MSP-04C run seed unavailable")
		}
	}
	commands := append([]msp04cArtifactCommand(nil), input.Commands...)
	if len(commands) == 0 {
		return msp04cArtifact{}, errors.New("missing MSP-04C artifact commands")
	}
	for _, command := range commands {
		if !msp04cCommandAllowed(command) {
			return msp04cArtifact{}, errors.New("invalid MSP-04C artifact command")
		}
	}
	sort.Slice(commands, func(left, right int) bool { return commands[left] < commands[right] })
	commands = compactMSP04CCommands(commands)

	gatePass, err := validateMSP04CTranscript(input.Transcript)
	if err != nil {
		return msp04cArtifact{}, err
	}
	cases := []msp04cArtifactCase{
		{ID: "EEBUS-G10", Status: msp04cPassStatus(gatePass["EEBUS-G10"]), State: "QUARANTINED", Reason: "CLONE_DETECTED", Outcome: "TRUST_DENIED"},
		{ID: "EEBUS-G11", Status: msp04cPassStatus(gatePass["EEBUS-G11"]), State: "ADMIN_HOLD", Reason: "HANDSHAKE_ATTEMPT_LIMIT", Outcome: "RETRY_DENIED", Count: 4},
		{ID: "EEBUS-G16", Status: msp04cPassStatus(gatePass["EEBUS-G16"]), State: "UNPAIRED_LOCKED", Outcome: "PUBLIC_API_FROZEN"},
	}
	result := "PASS"
	for index := range cases {
		cases[index].CaseLabel = msp04cSeededLabel(seed, "case:"+cases[index].ID)
		if cases[index].Status == "FAIL" {
			result = "FAIL"
		}
	}
	return msp04cArtifact{
		AuthScope: "not_applicable_synthetic", Cases: cases, Commands: commands, Issue: input.Issue,
		Repo: input.Repo, RepoBranch: input.Branch, RepoCommit: input.Commit, Result: result,
		RunLabel: msp04cSeededLabel(seed, "run"), TemporaryPaths: "redacted",
		Toolchain: msp04cArtifactToolchain{GoVersion: input.GoVersion, GoWork: input.GoWork, GoToolchain: input.GoToolchain},
		Topology:  "not_applicable_synthetic", replaySeed: seed,
	}, nil
}

func validateMSP04CTranscript(transcript []msp04cSubcaseObservation) (map[string]bool, error) {
	if len(transcript) != len(msp04cRequiredSubcases) {
		return nil, errors.New("incomplete MSP-04C execution transcript")
	}
	seen := make(map[msp04cSubcase]struct{}, len(transcript))
	gatePass := map[string]bool{"EEBUS-G10": true, "EEBUS-G11": true, "EEBUS-G16": true}
	for _, observed := range transcript {
		expected, required := msp04cRequiredSubcases[observed.Subcase]
		if !required {
			return nil, errors.New("unexpected MSP-04C execution subcase")
		}
		if _, duplicate := seen[observed.Subcase]; duplicate {
			return nil, errors.New("duplicate MSP-04C execution subcase")
		}
		seen[observed.Subcase] = struct{}{}
		if observed.State != expected.state || observed.Reason != expected.reason || observed.Outcome != expected.outcome || observed.Count != expected.count || observed.DelaySeconds != expected.delaySeconds {
			gatePass[expected.gate] = false
		}
	}
	return gatePass, nil
}

func newMSP04CRunSeed(reader io.Reader) ([32]byte, error) {
	var seed [32]byte
	if reader == nil {
		return seed, errors.New("missing random source")
	}
	_, err := io.ReadFull(reader, seed[:])
	if err != nil || seed == [32]byte{} {
		return [32]byte{}, errors.New("invalid random seed")
	}
	return seed, nil
}

func msp04cSeededLabel(seed [32]byte, scope string) string {
	hash := sha256.New()
	hash.Write(seed[:])
	hash.Write([]byte{0})
	hash.Write([]byte(scope))
	digest := hash.Sum(nil)
	prefix := "case"
	if scope == "run" {
		prefix = "run"
	}
	return prefix + "-" + hex.EncodeToString(digest[:8])
}

func (artifact msp04cArtifact) validate() error {
	if artifact.AuthScope != "not_applicable_synthetic" || artifact.TemporaryPaths != "redacted" || artifact.Topology != "not_applicable_synthetic" || artifact.Issue != "MSP-04C" || artifact.RunLabel == "" || artifact.replaySeed == [32]byte{} || !msp04cCommitPattern.MatchString(artifact.RepoCommit) {
		return errors.New("invalid MSP-04C artifact envelope")
	}
	if artifact.Result != "PASS" && artifact.Result != "FAIL" {
		return errors.New("invalid MSP-04C artifact result")
	}
	if artifact.Repo == "" || artifact.RepoBranch == "" || artifact.Toolchain.GoVersion == "" || artifact.Toolchain.GoWork == "" || artifact.Toolchain.GoToolchain == "" || len(artifact.Cases) != 3 || len(artifact.Commands) == 0 {
		return errors.New("incomplete MSP-04C artifact")
	}
	wantResult := "PASS"
	seen := make(map[string]struct{}, 3)
	labels := make(map[string]struct{}, 3)
	for _, item := range artifact.Cases {
		if item.CaseLabel == "" || !msp04cCaseIDAllowed(item.ID) || !msp04cStatusAllowed(item.Status) || !msp04cStateAllowed(item.State) || !msp04cReasonAllowed(item.Reason) || !msp04cOutcomeAllowed(item.Outcome) {
			return errors.New("invalid MSP-04C artifact case")
		}
		if _, duplicate := seen[item.ID]; duplicate {
			return errors.New("duplicate MSP-04C artifact case")
		}
		if _, duplicate := labels[item.CaseLabel]; duplicate {
			return errors.New("duplicate MSP-04C artifact case label")
		}
		seen[item.ID], labels[item.CaseLabel] = struct{}{}, struct{}{}
		if item.Status == "FAIL" {
			wantResult = "FAIL"
		}
	}
	if len(seen) != 3 || wantResult != artifact.Result {
		return errors.New("inconsistent MSP-04C artifact result")
	}
	return nil
}

func (artifact msp04cArtifact) jsonBytes() ([]byte, error) {
	if err := artifact.validate(); err != nil {
		return nil, err
	}
	payload, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(payload, '\n'), nil
}

func msp04cPassStatus(pass bool) string {
	if pass {
		return "PASS"
	}
	return "FAIL"
}

func compactMSP04CCommands(source []msp04cArtifactCommand) []msp04cArtifactCommand {
	result := source[:0]
	for _, command := range source {
		if len(result) == 0 || result[len(result)-1] != command {
			result = append(result, command)
		}
	}
	return result
}

func msp04cCommandAllowed(command msp04cArtifactCommand) bool {
	return command == msp04cCommandAPIFreeze || command == msp04cCommandDiffCheck || command == msp04cCommandUnit
}

func (command msp04cArtifactCommand) MarshalJSON() ([]byte, error) {
	return json.Marshal(command.label())
}

func (command msp04cArtifactCommand) label() string {
	switch command {
	case msp04cCommandAPIFreeze:
		return "api_freeze"
	case msp04cCommandDiffCheck:
		return "diff_check"
	case msp04cCommandUnit:
		return "unit"
	default:
		return "invalid"
	}
}

func msp04cCaseIDAllowed(value string) bool {
	return value == "EEBUS-G10" || value == "EEBUS-G11" || value == "EEBUS-G16"
}

func msp04cStatusAllowed(value string) bool { return value == "PASS" || value == "FAIL" }

func msp04cStateAllowed(value string) bool {
	switch value {
	case "NO_LOCAL_IDENTITY", "UNPAIRED_LOCKED", "PAIRED_TRUSTED", "REVOKED", "QUARANTINED", "CORRUPT_STORE", "BACKOFF_ACTIVE", "RETRY_READY", "ADMIN_HOLD":
		return true
	default:
		return false
	}
}

func msp04cReasonAllowed(value string) bool {
	switch value {
	case "", "CORRUPT_STORE", "DURABILITY_UNKNOWN", "HOST_KEY_UNAVAILABLE", "HOST_BINDING_MISMATCH", "CLONE_DETECTED", "MANIFEST_GENERATION_ROLLBACK", "CONTROL_EPOCH_ROLLBACK", "REVOKED_ASSOCIATION", "ADMIN_HOLD", "RETRYABLE_FAILURE", "HANDSHAKE_ATTEMPT_LIMIT":
		return true
	default:
		return false
	}
}

func msp04cOutcomeAllowed(value string) bool {
	return value == "TRUST_DENIED" || value == "RETRY_DENIED" || value == "PUBLIC_API_FROZEN"
}
