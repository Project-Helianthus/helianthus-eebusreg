package main

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func TestMSP04CRGatesConsumeExecutedProductionComposition(t *testing.T) {
	command := exec.Command(
		"go", "test", "../eebusfacade", "-count=1", "-v",
		"-run", "TestMSP04CR",
	)
	output, runErr := command.CombinedOutput()
	marker := []byte("MSP04CR_ARTIFACT ")
	artifacts := make(map[string]msp04crExecutedArtifact)
	for _, line := range bytes.Split(output, []byte{'\n'}) {
		index := bytes.Index(line, marker)
		if index < 0 {
			continue
		}
		payload := bytes.TrimSpace(line[index+len(marker):])
		if err := validatePublicRedaction(payload); err != nil {
			t.Fatalf("executed MSP-04C-R artifact failed redaction: %v", err)
		}
		var artifact msp04crExecutedArtifact
		if err := json.Unmarshal(payload, &artifact); err != nil {
			t.Fatalf("executed MSP-04C-R artifact is malformed: %v", err)
		}
		if artifact.Gate != "EEBUS-G10" && artifact.Gate != "EEBUS-G11" && artifact.Gate != "EEBUS-G16" {
			t.Fatalf("unexpected executed MSP-04C-R gate %q", artifact.Gate)
		}
		if _, duplicate := artifacts[artifact.Gate]; duplicate {
			t.Fatalf("duplicate executed MSP-04C-R gate %q", artifact.Gate)
		}
		artifacts[artifact.Gate] = artifact
	}

	for _, gate := range []string{"EEBUS-G10", "EEBUS-G11", "EEBUS-G16"} {
		artifact, ok := artifacts[gate]
		if !ok || artifact.Status != "PASS" {
			t.Errorf("executed production composition did not derive %s PASS: %+v", gate, artifact)
		}
	}
	if runErr != nil {
		t.Fatalf("production-composition execution remained RED: %v\n%s", runErr, redactMSP04CRTestOutput(output))
	}
}

type msp04crExecutedArtifact struct {
	Gate   string `json:"gate"`
	Status string `json:"status"`
}

func redactMSP04CRTestOutput(output []byte) string {
	lines := strings.Split(string(output), "\n")
	redacted := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.Contains(line, "MSP04CR_ARTIFACT ") || strings.HasPrefix(line, "--- FAIL:") || strings.HasPrefix(line, "FAIL") {
			redacted = append(redacted, line)
		}
	}
	return strings.Join(redacted, "\n")
}
