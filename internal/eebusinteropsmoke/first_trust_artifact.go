package eebusinteropsmoke

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

type firstTrustArtifactCommand uint8

const (
	firstTrustCommandAPIFreeze firstTrustArtifactCommand = iota + 1
	firstTrustCommandDiffCheck
	firstTrustCommandUnit
)

type firstTrustSubcase uint8

const (
	firstTrustG10Clone firstTrustSubcase = iota + 1
	firstTrustG10HostBindingMismatch
	firstTrustG10HostKeyUnavailable
	firstTrustG10ManifestRollback
	firstTrustG10ControlRollback
	firstTrustG10DurabilityUnknown
	firstTrustG10CopiedCurrentRepair
	firstTrustG10InactiveParentRepair
	firstTrustG10SameNumberBranchConflict
	firstTrustG11Vector0
	firstTrustG11Vector1
	firstTrustG11Vector2
	firstTrustG11Vector3
	firstTrustG11Vector4
	firstTrustG11CheckpointSix
	firstTrustG11CheckpointFour
	firstTrustG11RestartFour
	firstTrustG11PreDeadline
	firstTrustG11DeadlineReady
	firstTrustG11Bounds
	firstTrustG11NoActiveEviction
	firstTrustG16APIFreeze
	firstTrustG16SuccessRedaction
	firstTrustG16FailureRedaction
)

type firstTrustSubcaseObservation struct {
	Subcase      firstTrustSubcase
	State        string
	Reason       string
	Outcome      string
	Count        uint64
	DelaySeconds uint64
}

type firstTrustArtifactInput struct {
	Repo        string
	Branch      string
	Commit      string
	Issue       string
	GoVersion   string
	GoWork      string
	GoToolchain string
	RunSeed     [32]byte
	Commands    []firstTrustArtifactCommand
	Transcript  []firstTrustSubcaseObservation
}

type firstTrustArtifactToolchain struct {
	GoVersion   string `json:"go_version"`
	GoWork      string `json:"go_work"`
	GoToolchain string `json:"go_toolchain"`
}

type firstTrustArtifactCase struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	CaseLabel    string `json:"case_label"`
	State        string `json:"state"`
	Reason       string `json:"reason,omitempty"`
	Outcome      string `json:"outcome"`
	Count        uint64 `json:"count,omitempty"`
	DelaySeconds uint64 `json:"delay_seconds,omitempty"`
}

type firstTrustArtifact struct {
	AuthScope      string                      `json:"auth_scope"`
	Cases          []firstTrustArtifactCase    `json:"cases"`
	Commands       []firstTrustArtifactCommand `json:"commands"`
	Issue          string                      `json:"issue"`
	Repo           string                      `json:"repo"`
	RepoBranch     string                      `json:"repo_branch"`
	RepoCommit     string                      `json:"repo_commit"`
	Result         string                      `json:"result"`
	RunLabel       string                      `json:"run_label"`
	TemporaryPaths string                      `json:"temporary_paths"`
	Toolchain      firstTrustArtifactToolchain `json:"toolchain"`
	Topology       string                      `json:"topology"`
	replaySeed     [32]byte
}

type firstTrustExpectedSubcase struct {
	gate         string
	state        string
	reason       string
	outcome      string
	count        uint64
	delaySeconds uint64
}

var firstTrustCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

var firstTrustRequiredSubcases = map[firstTrustSubcase]firstTrustExpectedSubcase{
	firstTrustG10Clone:                    {gate: "EEBUS-G10", state: "QUARANTINED", reason: "CLONE_DETECTED", outcome: "TRUST_DENIED"},
	firstTrustG10HostBindingMismatch:      {gate: "EEBUS-G10", state: "QUARANTINED", reason: "HOST_BINDING_MISMATCH", outcome: "TRUST_DENIED"},
	firstTrustG10HostKeyUnavailable:       {gate: "EEBUS-G10", state: "NO_LOCAL_IDENTITY", reason: "HOST_KEY_UNAVAILABLE", outcome: "TRUST_DENIED"},
	firstTrustG10ManifestRollback:         {gate: "EEBUS-G10", state: "QUARANTINED", reason: "MANIFEST_GENERATION_ROLLBACK", outcome: "TRUST_DENIED"},
	firstTrustG10ControlRollback:          {gate: "EEBUS-G10", state: "QUARANTINED", reason: "CONTROL_EPOCH_ROLLBACK", outcome: "TRUST_DENIED"},
	firstTrustG10DurabilityUnknown:        {gate: "EEBUS-G10", state: "QUARANTINED", reason: "DURABILITY_UNKNOWN", outcome: "TRUST_DENIED"},
	firstTrustG10CopiedCurrentRepair:      {gate: "EEBUS-G10", state: "UNPAIRED_LOCKED", outcome: "TRUST_DENIED"},
	firstTrustG10InactiveParentRepair:     {gate: "EEBUS-G10", state: "UNPAIRED_LOCKED", outcome: "TRUST_DENIED"},
	firstTrustG10SameNumberBranchConflict: {gate: "EEBUS-G10", state: "QUARANTINED", reason: "DURABILITY_UNKNOWN", outcome: "TRUST_DENIED"},
	firstTrustG11Vector0:                  {gate: "EEBUS-G11", state: "RETRY_READY", outcome: "RETRY_DENIED"},
	firstTrustG11Vector1:                  {gate: "EEBUS-G11", state: "BACKOFF_ACTIVE", outcome: "RETRY_DENIED", count: 1, delaySeconds: 3},
	firstTrustG11Vector2:                  {gate: "EEBUS-G11", state: "BACKOFF_ACTIVE", outcome: "RETRY_DENIED", count: 2, delaySeconds: 6},
	firstTrustG11Vector3:                  {gate: "EEBUS-G11", state: "BACKOFF_ACTIVE", outcome: "RETRY_DENIED", count: 3, delaySeconds: 10},
	firstTrustG11Vector4:                  {gate: "EEBUS-G11", state: "ADMIN_HOLD", reason: "HANDSHAKE_ATTEMPT_LIMIT", outcome: "RETRY_DENIED", count: 4},
	firstTrustG11CheckpointSix:            {gate: "EEBUS-G11", state: "BACKOFF_ACTIVE", outcome: "RETRY_DENIED", count: 2, delaySeconds: 6},
	firstTrustG11CheckpointFour:           {gate: "EEBUS-G11", state: "BACKOFF_ACTIVE", outcome: "RETRY_DENIED", count: 2, delaySeconds: 4},
	firstTrustG11RestartFour:              {gate: "EEBUS-G11", state: "BACKOFF_ACTIVE", outcome: "RETRY_DENIED", count: 2, delaySeconds: 4},
	firstTrustG11PreDeadline:              {gate: "EEBUS-G11", state: "BACKOFF_ACTIVE", outcome: "RETRY_DENIED", count: 2, delaySeconds: 4},
	firstTrustG11DeadlineReady:            {gate: "EEBUS-G11", state: "RETRY_READY", outcome: "RETRY_DENIED", count: 2},
	firstTrustG11Bounds:                   {gate: "EEBUS-G11", state: "ADMIN_HOLD", reason: "ADMIN_HOLD", outcome: "RETRY_DENIED"},
	firstTrustG11NoActiveEviction:         {gate: "EEBUS-G11", state: "ADMIN_HOLD", reason: "ADMIN_HOLD", outcome: "RETRY_DENIED"},
	firstTrustG16APIFreeze:                {gate: "EEBUS-G16", state: "UNPAIRED_LOCKED", outcome: "PUBLIC_API_FROZEN"},
	firstTrustG16SuccessRedaction:         {gate: "EEBUS-G16", state: "UNPAIRED_LOCKED", outcome: "PUBLIC_API_FROZEN"},
	firstTrustG16FailureRedaction:         {gate: "EEBUS-G16", state: "UNPAIRED_LOCKED", outcome: "PUBLIC_API_FROZEN"},
}

func newFirstTrustArtifact(input firstTrustArtifactInput) (firstTrustArtifact, error) {
	input.Repo = strings.TrimSpace(input.Repo)
	input.Branch = strings.TrimSpace(input.Branch)
	input.Commit = strings.TrimSpace(input.Commit)
	input.Issue = strings.TrimSpace(input.Issue)
	input.GoVersion = strings.TrimSpace(input.GoVersion)
	input.GoWork = strings.TrimSpace(input.GoWork)
	input.GoToolchain = strings.TrimSpace(input.GoToolchain)
	if input.Repo == "" || input.Branch == "" || input.Issue != "first_trust_interop" || !firstTrustCommitPattern.MatchString(input.Commit) || input.GoVersion == "" || input.GoWork == "" || input.GoToolchain == "" {
		return firstTrustArtifact{}, errors.New("invalid first-trust interop artifact metadata")
	}
	seed := input.RunSeed
	if seed == [32]byte{} {
		var err error
		seed, err = newFirstTrustRunSeed(rand.Reader)
		if err != nil {
			return firstTrustArtifact{}, errors.New("first-trust interop run seed unavailable")
		}
	}
	commands := append([]firstTrustArtifactCommand(nil), input.Commands...)
	if len(commands) == 0 {
		return firstTrustArtifact{}, errors.New("missing first-trust interop artifact commands")
	}
	for _, command := range commands {
		if !firstTrustCommandAllowed(command) {
			return firstTrustArtifact{}, errors.New("invalid first-trust interop artifact command")
		}
	}
	sort.Slice(commands, func(left, right int) bool { return commands[left] < commands[right] })
	commands = compactFirstTrustCommands(commands)

	gatePass, err := validateFirstTrustTranscript(input.Transcript)
	if err != nil {
		return firstTrustArtifact{}, err
	}
	cases := []firstTrustArtifactCase{
		{ID: "EEBUS-G10", Status: firstTrustPassStatus(gatePass["EEBUS-G10"]), State: "QUARANTINED", Reason: "CLONE_DETECTED", Outcome: "TRUST_DENIED"},
		{ID: "EEBUS-G11", Status: firstTrustPassStatus(gatePass["EEBUS-G11"]), State: "ADMIN_HOLD", Reason: "HANDSHAKE_ATTEMPT_LIMIT", Outcome: "RETRY_DENIED", Count: 4},
		{ID: "EEBUS-G16", Status: firstTrustPassStatus(gatePass["EEBUS-G16"]), State: "UNPAIRED_LOCKED", Outcome: "PUBLIC_API_FROZEN"},
	}
	result := "PASS"
	for index := range cases {
		cases[index].CaseLabel = firstTrustSeededLabel(seed, "case:"+cases[index].ID)
		if cases[index].Status == "FAIL" {
			result = "FAIL"
		}
	}
	return firstTrustArtifact{
		AuthScope: "not_applicable_synthetic", Cases: cases, Commands: commands, Issue: input.Issue,
		Repo: input.Repo, RepoBranch: input.Branch, RepoCommit: input.Commit, Result: result,
		RunLabel: firstTrustSeededLabel(seed, "run"), TemporaryPaths: "redacted",
		Toolchain: firstTrustArtifactToolchain{GoVersion: input.GoVersion, GoWork: input.GoWork, GoToolchain: input.GoToolchain},
		Topology:  "not_applicable_synthetic", replaySeed: seed,
	}, nil
}

func validateFirstTrustTranscript(transcript []firstTrustSubcaseObservation) (map[string]bool, error) {
	if len(transcript) != len(firstTrustRequiredSubcases) {
		return nil, errors.New("incomplete first-trust interop execution transcript")
	}
	seen := make(map[firstTrustSubcase]struct{}, len(transcript))
	gatePass := map[string]bool{"EEBUS-G10": true, "EEBUS-G11": true, "EEBUS-G16": true}
	for _, observed := range transcript {
		expected, required := firstTrustRequiredSubcases[observed.Subcase]
		if !required {
			return nil, errors.New("unexpected first-trust interop execution subcase")
		}
		if _, duplicate := seen[observed.Subcase]; duplicate {
			return nil, errors.New("duplicate first-trust interop execution subcase")
		}
		seen[observed.Subcase] = struct{}{}
		if observed.State != expected.state || observed.Reason != expected.reason || observed.Outcome != expected.outcome || observed.Count != expected.count || observed.DelaySeconds != expected.delaySeconds {
			gatePass[expected.gate] = false
		}
	}
	return gatePass, nil
}

func newFirstTrustRunSeed(reader io.Reader) ([32]byte, error) {
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

func firstTrustSeededLabel(seed [32]byte, scope string) string {
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

func (artifact firstTrustArtifact) validate() error {
	if artifact.AuthScope != "not_applicable_synthetic" || artifact.TemporaryPaths != "redacted" || artifact.Topology != "not_applicable_synthetic" || artifact.Issue != "first_trust_interop" || artifact.RunLabel == "" || artifact.replaySeed == [32]byte{} || !firstTrustCommitPattern.MatchString(artifact.RepoCommit) {
		return errors.New("invalid first-trust interop artifact envelope")
	}
	if artifact.Result != "PASS" && artifact.Result != "FAIL" {
		return errors.New("invalid first-trust interop artifact result")
	}
	if artifact.Repo == "" || artifact.RepoBranch == "" || artifact.Toolchain.GoVersion == "" || artifact.Toolchain.GoWork == "" || artifact.Toolchain.GoToolchain == "" || len(artifact.Cases) != 3 || len(artifact.Commands) == 0 {
		return errors.New("incomplete first-trust interop artifact")
	}
	wantResult := "PASS"
	seen := make(map[string]struct{}, 3)
	labels := make(map[string]struct{}, 3)
	for _, item := range artifact.Cases {
		if item.CaseLabel == "" || !firstTrustCaseIDAllowed(item.ID) || !firstTrustStatusAllowed(item.Status) || !firstTrustStateAllowed(item.State) || !firstTrustReasonAllowed(item.Reason) || !firstTrustOutcomeAllowed(item.Outcome) {
			return errors.New("invalid first-trust interop artifact case")
		}
		if _, duplicate := seen[item.ID]; duplicate {
			return errors.New("duplicate first-trust interop artifact case")
		}
		if _, duplicate := labels[item.CaseLabel]; duplicate {
			return errors.New("duplicate first-trust interop artifact case label")
		}
		seen[item.ID], labels[item.CaseLabel] = struct{}{}, struct{}{}
		if item.Status == "FAIL" {
			wantResult = "FAIL"
		}
	}
	if len(seen) != 3 || wantResult != artifact.Result {
		return errors.New("inconsistent first-trust interop artifact result")
	}
	return nil
}

func (artifact firstTrustArtifact) jsonBytes() ([]byte, error) {
	if err := artifact.validate(); err != nil {
		return nil, err
	}
	payload, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(payload, '\n'), nil
}

func firstTrustPassStatus(pass bool) string {
	if pass {
		return "PASS"
	}
	return "FAIL"
}

func compactFirstTrustCommands(source []firstTrustArtifactCommand) []firstTrustArtifactCommand {
	result := source[:0]
	for _, command := range source {
		if len(result) == 0 || result[len(result)-1] != command {
			result = append(result, command)
		}
	}
	return result
}

func firstTrustCommandAllowed(command firstTrustArtifactCommand) bool {
	return command == firstTrustCommandAPIFreeze || command == firstTrustCommandDiffCheck || command == firstTrustCommandUnit
}

func (command firstTrustArtifactCommand) MarshalJSON() ([]byte, error) {
	return json.Marshal(command.label())
}

func (command firstTrustArtifactCommand) label() string {
	switch command {
	case firstTrustCommandAPIFreeze:
		return "api_freeze"
	case firstTrustCommandDiffCheck:
		return "diff_check"
	case firstTrustCommandUnit:
		return "unit"
	default:
		return "invalid"
	}
}

func firstTrustCaseIDAllowed(value string) bool {
	return value == "EEBUS-G10" || value == "EEBUS-G11" || value == "EEBUS-G16"
}

func firstTrustStatusAllowed(value string) bool { return value == "PASS" || value == "FAIL" }

func firstTrustStateAllowed(value string) bool {
	switch value {
	case "NO_LOCAL_IDENTITY", "UNPAIRED_LOCKED", "PAIRED_TRUSTED", "REVOKED", "QUARANTINED", "CORRUPT_STORE", "BACKOFF_ACTIVE", "RETRY_READY", "ADMIN_HOLD":
		return true
	default:
		return false
	}
}

func firstTrustReasonAllowed(value string) bool {
	switch value {
	case "", "CORRUPT_STORE", "DURABILITY_UNKNOWN", "HOST_KEY_UNAVAILABLE", "HOST_BINDING_MISMATCH", "CLONE_DETECTED", "MANIFEST_GENERATION_ROLLBACK", "CONTROL_EPOCH_ROLLBACK", "REVOKED_ASSOCIATION", "ADMIN_HOLD", "RETRYABLE_FAILURE", "HANDSHAKE_ATTEMPT_LIMIT":
		return true
	default:
		return false
	}
}

func firstTrustOutcomeAllowed(value string) bool {
	return value == "TRUST_DENIED" || value == "RETRY_DENIED" || value == "PUBLIC_API_FROZEN"
}
