package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestMSP04CG16ArtifactIsDeterministicClosedAndRedacted(t *testing.T) {
	input := msp04cArtifactInput{
		Repo:        "Project-Helianthus/helianthus-eebusreg",
		Branch:      "issue/28-msp04c-restore-quarantine",
		Commit:      strings.Repeat("1", 40),
		Issue:       "MSP-04C",
		GoVersion:   "go1.25.0",
		GoWork:      "off",
		GoToolchain: "auto",
		RunOrdinal:  1,
		Commands:    []msp04cArtifactCommand{msp04cCommandDiffCheck, msp04cCommandUnit, msp04cCommandAPIFreeze},
		Cases: []msp04cArtifactCase{
			{ID: "EEBUS-G16", Status: "PASS", CaseOrdinal: 3, State: "UNPAIRED_LOCKED", Outcome: "PUBLIC_API_FROZEN"},
			{ID: "EEBUS-G11", Status: "PASS", CaseOrdinal: 2, State: "BACKOFF_ACTIVE", Outcome: "RETRY_DENIED", Count: 4, DelaySeconds: 10},
			{ID: "EEBUS-G10", Status: "PASS", CaseOrdinal: 1, State: "QUARANTINED", Reason: "CLONE_DETECTED", Outcome: "TRUST_DENIED"},
		},
	}
	artifact, err := newMSP04CArtifact(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := artifact.validate(); err != nil {
		t.Fatal(err)
	}
	if artifact.RunLabel == "" {
		t.Fatal("artifact run label is empty")
	}
	caseLabels := map[string]struct{}{}
	for _, item := range artifact.Cases {
		if item.CaseLabel == "" {
			t.Fatal("artifact case label is empty")
		}
		caseLabels[item.CaseLabel] = struct{}{}
	}
	if len(caseLabels) != 3 {
		t.Fatal("artifact case labels are not unique within the run")
	}
	first, err := artifact.jsonBytes()
	if err != nil {
		t.Fatal(err)
	}

	reordered := input
	reordered.Commands = []msp04cArtifactCommand{msp04cCommandAPIFreeze, msp04cCommandUnit, msp04cCommandDiffCheck}
	reordered.Cases = []msp04cArtifactCase{input.Cases[1], input.Cases[0], input.Cases[2]}
	artifactAgain, err := newMSP04CArtifact(reordered)
	if err != nil {
		t.Fatal(err)
	}
	second, err := artifactAgain.jsonBytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("artifact bytes depend on command or case ordering")
	}
	if err := validatePublicRedaction(first); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(first, []byte("case_ordinal")) || bytes.Contains(first, []byte("run_ordinal")) {
		t.Fatal("artifact exposed internal synthetic ordinals")
	}

	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(first, &decoded); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(decoded))
	for name := range decoded {
		names = append(names, name)
	}
	slices.Sort(names)
	wantNames := []string{
		"auth_scope", "cases", "commands", "issue", "repo", "repo_branch", "repo_commit", "result", "run_label", "temporary_paths", "toolchain", "topology",
	}
	if !slices.Equal(names, wantNames) {
		t.Fatalf("artifact fields = %v, want %v", names, wantNames)
	}
	for field, want := range map[string]string{
		"auth_scope":      "not_applicable_synthetic",
		"issue":           "MSP-04C",
		"result":          "PASS",
		"temporary_paths": "redacted",
		"topology":        "not_applicable_synthetic",
	} {
		var got string
		if err := json.Unmarshal(decoded[field], &got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("artifact field %s = %q, want %q", field, got, want)
		}
	}
}

func TestMSP04CG16ArtifactRequiresExactlyG10G11G16(t *testing.T) {
	base := msp04cArtifactInput{
		Repo: "Project-Helianthus/helianthus-eebusreg", Branch: "issue/28-msp04c-restore-quarantine",
		Commit: strings.Repeat("2", 40), Issue: "MSP-04C", GoVersion: "go1.25.0", GoWork: "off", GoToolchain: "auto",
		RunOrdinal: 2, Commands: []msp04cArtifactCommand{msp04cCommandUnit},
		Cases: []msp04cArtifactCase{
			{ID: "EEBUS-G10", Status: "PASS", CaseOrdinal: 1, State: "QUARANTINED", Reason: "HOST_BINDING_MISMATCH", Outcome: "TRUST_DENIED"},
			{ID: "EEBUS-G11", Status: "PASS", CaseOrdinal: 2, State: "BACKOFF_ACTIVE", Outcome: "RETRY_DENIED", Count: 2, DelaySeconds: 6},
			{ID: "EEBUS-G16", Status: "PASS", CaseOrdinal: 3, State: "UNPAIRED_LOCKED", Outcome: "PUBLIC_API_FROZEN"},
		},
	}

	invalid := []struct {
		name   string
		mutate func(*msp04cArtifactInput)
	}{
		{name: "missing case", mutate: func(input *msp04cArtifactInput) { input.Cases = input.Cases[:2] }},
		{name: "duplicate case", mutate: func(input *msp04cArtifactInput) { input.Cases[2] = input.Cases[1] }},
		{name: "unexpected case", mutate: func(input *msp04cArtifactInput) { input.Cases[2].ID = "EEBUS-G12" }},
		{name: "zero run ordinal", mutate: func(input *msp04cArtifactInput) { input.RunOrdinal = 0 }},
		{name: "zero case ordinal", mutate: func(input *msp04cArtifactInput) { input.Cases[0].CaseOrdinal = 0 }},
		{name: "unlisted state", mutate: func(input *msp04cArtifactInput) { input.Cases[0].State = "UNLISTED" }},
		{name: "unlisted reason", mutate: func(input *msp04cArtifactInput) { input.Cases[0].Reason = "UNLISTED" }},
		{name: "unlisted outcome", mutate: func(input *msp04cArtifactInput) { input.Cases[0].Outcome = "UNLISTED" }},
		{name: "unlisted command", mutate: func(input *msp04cArtifactInput) { input.Commands = []msp04cArtifactCommand{99} }},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			input := base
			input.Cases = append([]msp04cArtifactCase(nil), base.Cases...)
			test.mutate(&input)
			if _, err := newMSP04CArtifact(input); err == nil {
				t.Fatal("invalid artifact input was accepted")
			}
		})
	}
}

func TestMSP04CG16ArtifactTypesHaveNoOpenEndedPayloadFields(t *testing.T) {
	for _, value := range []any{msp04cArtifact{}, msp04cArtifactCase{}, msp04cArtifactToolchain{}} {
		typeOf := reflect.TypeOf(value)
		for index := 0; index < typeOf.NumField(); index++ {
			field := typeOf.Field(index)
			switch field.Type.Kind() {
			case reflect.Interface, reflect.Map, reflect.Pointer:
				t.Fatalf("artifact type %s contains open-ended field %s", typeOf.Name(), field.Name)
			}
		}
	}
}

func TestMSP04CG16ArtifactFailureRowsRemainDeterministic(t *testing.T) {
	input := msp04cArtifactInput{
		Repo: "Project-Helianthus/helianthus-eebusreg", Branch: "issue/28-msp04c-restore-quarantine",
		Commit: strings.Repeat("3", 40), Issue: "MSP-04C", GoVersion: "go1.25.0", GoWork: "off", GoToolchain: "auto",
		RunOrdinal: 3, Commands: []msp04cArtifactCommand{msp04cCommandUnit},
		Cases: []msp04cArtifactCase{
			{ID: "EEBUS-G10", Status: "FAIL", CaseOrdinal: 1, State: "QUARANTINED", Reason: "DURABILITY_UNKNOWN", Outcome: "TRUST_DENIED"},
			{ID: "EEBUS-G11", Status: "PASS", CaseOrdinal: 2, State: "BACKOFF_ACTIVE", Outcome: "RETRY_DENIED", Count: 4, DelaySeconds: 10},
			{ID: "EEBUS-G16", Status: "PASS", CaseOrdinal: 3, State: "UNPAIRED_LOCKED", Outcome: "PUBLIC_API_FROZEN"},
		},
	}
	first, err := newMSP04CArtifact(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newMSP04CArtifact(input)
	if err != nil {
		t.Fatal(err)
	}
	firstBytes, _ := first.jsonBytes()
	secondBytes, _ := second.jsonBytes()
	if !bytes.Equal(firstBytes, secondBytes) || first.Result != "FAIL" {
		t.Fatal("failure artifact is not byte-deterministic with FAIL precedence")
	}
}
