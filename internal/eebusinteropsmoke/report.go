package eebusinteropsmoke

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	reportContract = "helianthus.eebus.transport-gate.v0"
	reportIssue    = "MSP-03D-R"
	reportRepo     = "Project-Helianthus/helianthus-eebusreg"

	caseFakePeer = "EEBUS-G01"
	caseLive     = "EEBUS-G17"

	resultPass    = "PASS"
	resultFail    = "FAIL"
	resultBlocked = "BLOCKED"
)

var (
	ipv4CandidatePattern = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	pemPattern           = regexp.MustCompile(`-----BEGIN [A-Z ]+-----|PRIVATE KEY`)
	secretPattern        = regexp.MustCompile(`(?i)(password|secret|token=|bearer\s+[a-z0-9._-]+|gh[pousr]_[a-z0-9_]+|eyj[a-z0-9_-]+\.[a-z0-9_-]+\.[a-z0-9_-]+)`)
	referenceKey         = mustRandomBytes(32)
)

type report struct {
	Contract      string            `json:"contract"`
	Issue         string            `json:"issue"`
	Repo          string            `json:"repo"`
	GeneratedAt   time.Time         `json:"generated_at"`
	Mode          string            `json:"mode"`
	Result        string            `json:"result"`
	RequiredCases []string          `json:"required_cases"`
	RepoBranch    string            `json:"repo_branch"`
	RepoCommit    string            `json:"repo_commit"`
	Toolchain     toolchainEvidence `json:"toolchain"`
	Module        moduleEvidence    `json:"module"`
	Security      securityEvidence  `json:"security"`
	Disposable    disposableMode    `json:"disposable"`
	Cases         []caseResult      `json:"cases"`
	LiveEvidence  *liveGateEvidence `json:"live_evidence,omitempty"`
	Notes         []string          `json:"notes,omitempty"`
}

type toolchainEvidence struct {
	GoVersion   string `json:"go_version"`
	GoWork      string `json:"go_work"`
	GoToolchain string `json:"go_toolchain"`
}

type moduleEvidence struct {
	Path    string `json:"path"`
	Version string `json:"version"`
}

type securityEvidence struct {
	PublicRedacted            bool `json:"public_redacted"`
	ProductionTrustWritten    bool `json:"production_trust_written"`
	PublicAPISurfaceAdded     bool `json:"public_api_surface_added"`
	GatewayImportAdded        bool `json:"gateway_import_added"`
	SemanticOrConsumerSurface bool `json:"semantic_or_consumer_surface"`
}

type disposableMode struct {
	IdentityMaterial string `json:"identity_material"`
	Store            string `json:"store"`
}

type caseResult struct {
	ID       string            `json:"id"`
	Status   string            `json:"status"`
	Evidence []string          `json:"evidence"`
	Details  map[string]string `json:"details,omitempty"`
	Error    string            `json:"error,omitempty"`
}

func newReport(mode string, required []string, cases []caseResult, notes []string) report {
	result, _ := deriveReportResult(cases)
	return report{
		Contract:      reportContract,
		Issue:         reportIssue,
		Repo:          reportRepo,
		GeneratedAt:   time.Now().UTC().Truncate(time.Second),
		Mode:          mode,
		Result:        result,
		RequiredCases: append([]string(nil), required...),
		RepoBranch:    commandString("git", "rev-parse", "--abbrev-ref", "HEAD"),
		RepoCommit:    commandString("git", "rev-parse", "HEAD"),
		Toolchain: toolchainEvidence{
			GoVersion:   commandString("go", "env", "GOVERSION"),
			GoWork:      commandString("go", "env", "GOWORK"),
			GoToolchain: commandString("go", "env", "GOTOOLCHAIN"),
		},
		Module: moduleEvidence{
			Path:    "github.com/Project-Helianthus/helianthus-eebus-go",
			Version: "v0.7.1-helianthus.1",
		},
		Security: securityEvidence{
			PublicRedacted:            true,
			ProductionTrustWritten:    false,
			PublicAPISurfaceAdded:     false,
			GatewayImportAdded:        false,
			SemanticOrConsumerSurface: false,
		},
		Disposable: disposableMode{
			IdentityMaterial: "in_memory_disposable",
			Store:            "none",
		},
		Cases: append([]caseResult(nil), cases...),
		Notes: append([]string(nil), notes...),
	}
}

func (r report) validate() error {
	r = r.normalized()
	if r.Contract != reportContract {
		return errors.New("unsupported contract")
	}
	if r.Issue != reportIssue {
		return errors.New("issue mismatch")
	}
	if r.Repo != reportRepo {
		return errors.New("repo mismatch")
	}
	if r.GeneratedAt.IsZero() {
		return errors.New("generated_at is required")
	}
	if r.Mode != "fake-peer" && r.Mode != "live-vr940f" && r.Mode != "all" {
		return errors.New("unsupported report mode")
	}
	trustedRepo := currentRepoEvidence()
	if r.RepoBranch == "" || !gitCommitPattern.MatchString(r.RepoCommit) || r.RepoBranch != trustedRepo.Branch || r.RepoCommit != trustedRepo.Commit {
		return errors.New("report provenance does not match the current checkout")
	}
	if r.Module.Path != "github.com/Project-Helianthus/helianthus-eebus-go" || r.Module.Version != "v0.7.1-helianthus.1" {
		return errors.New("eebus-go module evidence mismatch")
	}
	if !r.Security.PublicRedacted || r.Security.ProductionTrustWritten || r.Security.PublicAPISurfaceAdded || r.Security.GatewayImportAdded || r.Security.SemanticOrConsumerSurface {
		return errors.New("security evidence mismatch")
	}
	if r.Disposable.IdentityMaterial != "in_memory_disposable" || r.Disposable.Store != "none" {
		return errors.New("disposable identity evidence mismatch")
	}
	if len(r.RequiredCases) == 0 || len(r.Cases) == 0 {
		return errors.New("required cases and cases are required")
	}
	derivedResult, err := deriveReportResult(r.Cases)
	if err != nil {
		return err
	}
	if r.Result != derivedResult {
		return fmt.Errorf("report result %s does not match derived result %s", r.Result, derivedResult)
	}
	seen := map[string]bool{}
	var g19Case *caseResult
	for _, c := range r.Cases {
		if c.ID == "" {
			return errors.New("case id is required")
		}
		if seen[c.ID] {
			return fmt.Errorf("duplicate case %s", c.ID)
		}
		seen[c.ID] = true
		switch c.Status {
		case resultPass, resultFail, resultBlocked:
		default:
			return fmt.Errorf("unsupported status for %s", c.ID)
		}
		if len(c.Evidence) == 0 {
			return fmt.Errorf("case %s evidence is required", c.ID)
		}
		if c.ID == caseDirectAccess {
			item := c
			g19Case = &item
		}
	}
	for _, id := range r.RequiredCases {
		if !seen[id] {
			return fmt.Errorf("required case %s missing", id)
		}
	}
	if r.LiveEvidence != nil {
		payload, marshalErr := r.LiveEvidence.jsonBytes()
		if marshalErr != nil {
			return marshalErr
		}
		if redactionErr := validateLiveRedaction(payload); redactionErr != nil {
			return fmt.Errorf("G19 live evidence redaction: %w", redactionErr)
		}
		if g19Case == nil {
			return errors.New("live evidence requires a G19 case")
		}
	}
	if g19Case != nil && g19Case.Status == resultPass {
		if r.LiveEvidence == nil {
			return errors.New("G19 requires canonical live evidence")
		}
		if err := r.LiveEvidence.validateForCase(*g19Case, trustedRepo); err != nil {
			return fmt.Errorf("G19 live evidence: %w", err)
		}
	} else if r.LiveEvidence != nil {
		return errors.New("canonical live evidence is only valid for a passing G19 case")
	}
	publicReport := r
	publicReport.LiveEvidence = nil
	payload, err := publicReport.jsonBytes()
	if err != nil {
		return err
	}
	return validatePublicRedaction(payload)
}

func (r report) jsonBytes() ([]byte, error) {
	normalized := r.normalized()
	buf := &bytes.Buffer{}
	enc := json.NewEncoder(buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(normalized); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

func validatePublicRedaction(payload []byte) error {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("public report JSON is invalid: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("public report contains trailing JSON data")
	}
	return validatePublicValue(value, nil)
}

func digestRef(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func refLabel(prefix string, value string) string {
	prefix = strings.TrimSpace(prefix)
	if value == "" {
		return prefix + "-absent"
	}
	return prefix + "-" + keyedDigestRef(referenceKey, []byte(value))
}

func commandString(name string, args ...string) string {
	cmd := exec.Command(name, args...)
	cmd.Env = append(cmd.Environ(), "GOWORK=off")
	out, err := cmd.Output()
	if err != nil {
		return "unavailable"
	}
	return strings.TrimSpace(string(out))
}

func deriveReportResult(cases []caseResult) (string, error) {
	result := resultPass
	for _, item := range cases {
		switch strings.TrimSpace(item.Status) {
		case resultFail:
			result = resultFail
		case resultBlocked:
			if result != resultFail {
				result = resultBlocked
			}
		case resultPass:
		default:
			return "", fmt.Errorf("unsupported status for %s", strings.TrimSpace(item.ID))
		}
	}
	return result, nil
}

func (r report) normalized() report {
	normalized := r
	normalized.Contract = strings.TrimSpace(r.Contract)
	normalized.Issue = strings.TrimSpace(r.Issue)
	normalized.Repo = strings.TrimSpace(r.Repo)
	normalized.Mode = strings.TrimSpace(r.Mode)
	normalized.Result = strings.TrimSpace(r.Result)
	normalized.RepoBranch = strings.TrimSpace(r.RepoBranch)
	normalized.RepoCommit = strings.TrimSpace(r.RepoCommit)
	normalized.RequiredCases = sortedUnique(r.RequiredCases)
	normalized.Notes = sortedUnique(r.Notes)
	normalized.Toolchain.GoVersion = strings.TrimSpace(r.Toolchain.GoVersion)
	normalized.Toolchain.GoWork = strings.TrimSpace(r.Toolchain.GoWork)
	normalized.Toolchain.GoToolchain = strings.TrimSpace(r.Toolchain.GoToolchain)
	normalized.Module.Path = strings.TrimSpace(r.Module.Path)
	normalized.Module.Version = strings.TrimSpace(r.Module.Version)
	normalized.Disposable.IdentityMaterial = strings.TrimSpace(r.Disposable.IdentityMaterial)
	normalized.Disposable.Store = strings.TrimSpace(r.Disposable.Store)
	normalized.Cases = make([]caseResult, len(r.Cases))
	for i, item := range r.Cases {
		normalized.Cases[i] = item.normalized()
	}
	sort.SliceStable(normalized.Cases, func(i, j int) bool {
		return normalized.Cases[i].ID < normalized.Cases[j].ID
	})
	if r.LiveEvidence != nil {
		live := r.LiveEvidence.normalized()
		normalized.LiveEvidence = &live
	}
	return normalized
}

func (c caseResult) normalized() caseResult {
	normalized := c
	normalized.ID = strings.TrimSpace(c.ID)
	normalized.Status = strings.TrimSpace(c.Status)
	normalized.Error = strings.TrimSpace(c.Error)
	normalized.Evidence = sortedUnique(c.Evidence)
	if len(c.Details) == 0 {
		normalized.Details = nil
		return normalized
	}
	normalized.Details = make(map[string]string, len(c.Details))
	for _, sourceKey := range sortedStringMapKeys(c.Details) {
		key := strings.TrimSpace(sourceKey)
		if key == "" {
			continue
		}
		normalized.Details[key] = strings.TrimSpace(c.Details[sourceKey])
	}
	if len(normalized.Details) == 0 {
		normalized.Details = nil
	}
	return normalized
}

func sortedStringMapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (c caseResult) dataHash() string {
	payload, err := json.Marshal(c.normalized())
	if err != nil {
		return "sha256:invalid"
	}
	return fullDigestRef(payload)
}

func currentRepoEvidence() evidenceRepo {
	return evidenceRepo{
		Name:   "helianthus-eebusreg",
		Branch: commandString("git", "rev-parse", "--abbrev-ref", "HEAD"),
		Commit: commandString("git", "rev-parse", "HEAD"),
	}
}

func validatePublicValue(value any, path []string) error {
	switch typed := value.(type) {
	case map[string]any:
		for _, childKey := range sortedStringMapKeys(typed) {
			if credentialLikeKey(childKey) {
				return fmt.Errorf("public report contains credential-like key %q", childKey)
			}
			child := typed[childKey]
			if err := validatePublicValue(child, appendJSONPath(path, childKey)); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := validatePublicValue(child, path); err != nil {
				return err
			}
		}
	case string:
		return validatePublicString(typed, path)
	}
	return nil
}

func credentialLikeKey(key string) bool {
	compact := strings.NewReplacer("_", "", "-", "", ".", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(key)))
	for _, token := range []string{"password", "passwd", "secret", "token", "credential", "authorization", "privatekey", "apikey", "bearer"} {
		if strings.Contains(compact, token) {
			return true
		}
	}
	return false
}

func validatePublicString(value string, path []string) error {
	value = strings.TrimSpace(value)
	if pemPattern.MatchString(value) {
		return errors.New("public report contains PEM or private key material")
	}
	if secretPattern.MatchString(value) {
		return errors.New("public report contains secret-like material")
	}
	if len(hex40Pattern.FindAllString(value, -1)) != 0 {
		if allowedGitCommitPath(path) && gitCommitPattern.MatchString(value) {
			return nil
		}
		return fmt.Errorf("public report contains raw 40-hex identity at %s", strings.Join(path, "."))
	}
	for _, candidate := range networkCandidates(value) {
		if net.ParseIP(candidate) != nil {
			return errors.New("public report contains IP address")
		}
		if _, err := net.ParseMAC(candidate); err == nil {
			return errors.New("public report contains MAC address")
		}
	}
	for _, candidate := range ipv4CandidatePattern.FindAllString(value, -1) {
		if net.ParseIP(candidate) != nil {
			return errors.New("public report contains IP address")
		}
	}
	return nil
}

func appendJSONPath(path []string, key string) []string {
	result := make([]string, len(path)+1)
	copy(result, path)
	result[len(path)] = key
	return result
}

func allowedGitCommitPath(path []string) bool {
	if len(path) == 1 && path[0] == "repo_commit" {
		return true
	}
	if len(path) == 2 && path[0] == "repo" && path[1] == "commit" {
		return true
	}
	return false
}

func networkCandidates(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\r', '=', ',', ';', '(', ')', '[', ']', '{', '}', '<', '>', '"', '\'':
			return true
		default:
			return false
		}
	})
	result := make([]string, 0, len(parts)+1)
	result = append(result, strings.Trim(value, "[]"))
	for _, part := range parts {
		result = append(result, strings.Trim(part, "[]"))
		if host, _, err := net.SplitHostPort(strings.Trim(part, "[]")); err == nil {
			result = append(result, strings.Trim(host, "[]"))
		}
	}
	return sortedUnique(result)
}

func mustRandomBytes(size int) []byte {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		panic(fmt.Sprintf("secure random source unavailable: %v", err))
	}
	return value
}

func keyedDigestRef(key, payload []byte) string {
	digest := hmac.New(sha256.New, key)
	_, _ = digest.Write(payload)
	return "hmac-sha256:" + hex.EncodeToString(digest.Sum(nil))
}
