package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	reportContract = "helianthus.eebus.transport-gate.v0"
	reportIssue    = "MSP-03D"
	reportRepo     = "Project-Helianthus/helianthus-eebusreg"

	caseFakePeer = "EEBUS-G01"
	caseLive     = "EEBUS-G17"

	resultPass    = "PASS"
	resultFail    = "FAIL"
	resultBlocked = "BLOCKED"
)

var (
	rawIPv4Pattern = regexp.MustCompile(`\b(?:(?:\d{1,3})\.){3}(?:\d{1,3})\b`)
	rawMACPattern  = regexp.MustCompile(`\b[0-9a-fA-F]{2}(?::[0-9a-fA-F]{2}){5}\b`)
	pemPattern     = regexp.MustCompile(`-----BEGIN [A-Z ]+-----|PRIVATE KEY`)
	secretPattern  = regexp.MustCompile(`(?i)(password|secret|token=|bearer\s+[a-z0-9._-]+|gh[pousr]_[a-z0-9_]+|eyj[a-z0-9_-]+\.[a-z0-9_-]+\.[a-z0-9_-]+)`)
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
	Credentials string `json:"credentials"`
	Store       string `json:"store"`
}

type caseResult struct {
	ID       string            `json:"id"`
	Status   string            `json:"status"`
	Evidence []string          `json:"evidence"`
	Details  map[string]string `json:"details,omitempty"`
	Error    string            `json:"error,omitempty"`
}

func newReport(mode string, required []string, cases []caseResult, notes []string) report {
	result := resultPass
	for _, c := range cases {
		switch c.Status {
		case resultFail:
			result = resultFail
		case resultBlocked:
			if result != resultFail {
				result = resultBlocked
			}
		}
	}
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
			Path:    "github.com/enbility/eebus-go",
			Version: "v0.7.0",
		},
		Security: securityEvidence{
			PublicRedacted:            true,
			ProductionTrustWritten:    false,
			PublicAPISurfaceAdded:     false,
			GatewayImportAdded:        false,
			SemanticOrConsumerSurface: false,
		},
		Disposable: disposableMode{
			Credentials: "in_memory_disposable",
			Store:       "none",
		},
		Cases: cases,
		Notes: append([]string(nil), notes...),
	}
}

func (r report) validate() error {
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
	if r.Module.Path != "github.com/enbility/eebus-go" || r.Module.Version != "v0.7.0" {
		return errors.New("eebus-go module evidence mismatch")
	}
	if !r.Security.PublicRedacted || r.Security.ProductionTrustWritten || r.Security.PublicAPISurfaceAdded || r.Security.GatewayImportAdded || r.Security.SemanticOrConsumerSurface {
		return errors.New("security evidence mismatch")
	}
	if len(r.RequiredCases) == 0 || len(r.Cases) == 0 {
		return errors.New("required cases and cases are required")
	}
	seen := map[string]bool{}
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
	}
	for _, id := range r.RequiredCases {
		if !seen[id] {
			return fmt.Errorf("required case %s missing", id)
		}
	}
	payload, err := r.jsonBytes()
	if err != nil {
		return err
	}
	return validatePublicRedaction(payload)
}

func (r report) jsonBytes() ([]byte, error) {
	normalized := r
	sort.SliceStable(normalized.Cases, func(i, j int) bool {
		return normalized.Cases[i].ID < normalized.Cases[j].ID
	})
	for i := range normalized.Cases {
		sort.Strings(normalized.Cases[i].Evidence)
		if len(normalized.Cases[i].Details) == 0 {
			normalized.Cases[i].Details = nil
		}
	}
	buf := &bytes.Buffer{}
	enc := json.NewEncoder(buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(normalized); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

func validatePublicRedaction(payload []byte) error {
	text := string(payload)
	switch {
	case rawIPv4Pattern.MatchString(text):
		return errors.New("public report contains raw IPv4 address")
	case rawMACPattern.MatchString(text):
		return errors.New("public report contains MAC address")
	case pemPattern.MatchString(text):
		return errors.New("public report contains PEM or private key material")
	case secretPattern.MatchString(text):
		return errors.New("public report contains secret-like material")
	}
	return nil
}

func digestRef(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])[:12]
}

func refLabel(prefix string, value string) string {
	if value == "" {
		return prefix + "-absent"
	}
	return prefix + "-" + digestRef(value)
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
