package eebusinteropsmoke

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestMSP04CG16ArtifactIsDeterministicClosedAndRedacted(t *testing.T) {
	input := validMSP04CArtifactInput(1)
	input.Commands = []firstTrustArtifactCommand{firstTrustCommandDiffCheck, firstTrustCommandUnit, firstTrustCommandAPIFreeze}
	artifact, err := newFirstTrustArtifact(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := artifact.validate(); err != nil {
		t.Fatal(err)
	}
	if artifact.Result != "PASS" || artifact.RunLabel == "" {
		t.Fatal("complete exact transcript did not derive a labeled PASS artifact")
	}
	caseLabels := map[string]struct{}{}
	for _, item := range artifact.Cases {
		if item.CaseLabel == "" || item.Status != "PASS" {
			t.Fatal("artifact did not derive a PASS case from its required subcases")
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
	reordered.Commands = []firstTrustArtifactCommand{firstTrustCommandAPIFreeze, firstTrustCommandUnit, firstTrustCommandDiffCheck}
	reordered.Transcript = append([]firstTrustSubcaseObservation(nil), input.Transcript...)
	slices.Reverse(reordered.Transcript)
	artifactAgain, err := newFirstTrustArtifact(reordered)
	if err != nil {
		t.Fatal(err)
	}
	second, err := artifactAgain.jsonBytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("artifact bytes depend on command or execution ordering for one captured run seed")
	}
	if err := validatePublicRedaction(first); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(first, []byte("subcase")) || bytes.Contains(first, []byte("run_seed")) || bytes.Contains(first, input.RunSeed[:]) {
		t.Fatal("artifact exposed its execution transcript or replay seed")
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
}

func TestMSP04CG16ArtifactRequiresExactlyG10G11G16(t *testing.T) {
	base := validMSP04CArtifactInput(2)
	invalid := []struct {
		name   string
		mutate func(*firstTrustArtifactInput)
	}{
		{name: "missing subcase", mutate: func(input *firstTrustArtifactInput) { input.Transcript = input.Transcript[:len(input.Transcript)-1] }},
		{name: "duplicate subcase", mutate: func(input *firstTrustArtifactInput) { input.Transcript[len(input.Transcript)-1] = input.Transcript[0] }},
		{name: "unexpected subcase", mutate: func(input *firstTrustArtifactInput) { input.Transcript[0].Subcase = 255 }},
		{name: "unlisted command", mutate: func(input *firstTrustArtifactInput) { input.Commands = []firstTrustArtifactCommand{99} }},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			input := base
			input.Transcript = append([]firstTrustSubcaseObservation(nil), base.Transcript...)
			test.mutate(&input)
			if _, err := newFirstTrustArtifact(input); err == nil {
				t.Fatal("incomplete or non-closed execution transcript was accepted")
			}
		})
	}
	if len(firstTrustRequiredSubcases) != 24 {
		t.Fatalf("required execution inventory changed without an exhaustive contract update: %d", len(firstTrustRequiredSubcases))
	}
	seenGates := map[string]int{}
	for _, expected := range firstTrustRequiredSubcases {
		seenGates[expected.gate]++
	}
	if seenGates["EEBUS-G10"] != 9 || seenGates["EEBUS-G11"] != 12 || seenGates["EEBUS-G16"] != 3 {
		t.Fatalf("required subcase inventory = %#v", seenGates)
	}
}

func TestMSP04CG16ArtifactTypesHaveNoOpenEndedPayloadFields(t *testing.T) {
	for _, value := range []any{firstTrustArtifact{}, firstTrustArtifactCase{}, firstTrustArtifactToolchain{}} {
		typeOf := reflect.TypeOf(value)
		for index := 0; index < typeOf.NumField(); index++ {
			field := typeOf.Field(index)
			switch field.Type.Kind() {
			case reflect.Interface, reflect.Map, reflect.Pointer:
				t.Fatalf("artifact type %s contains open-ended field %s", typeOf.Name(), field.Name)
			}
		}
	}
	observationType := reflect.TypeOf(firstTrustSubcaseObservation{})
	if _, callerStatus := observationType.FieldByName("Status"); callerStatus {
		t.Fatal("execution transcript permits caller-supplied PASS/FAIL")
	}
}

func TestMSP04CG16ArtifactFailureRowsRemainDeterministic(t *testing.T) {
	input := validMSP04CArtifactInput(3)
	for index := range input.Transcript {
		if input.Transcript[index].Subcase == firstTrustG10Clone {
			input.Transcript[index].Reason = "DURABILITY_UNKNOWN"
		}
	}
	first, err := newFirstTrustArtifact(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newFirstTrustArtifact(input)
	if err != nil {
		t.Fatal(err)
	}
	firstBytes, _ := first.jsonBytes()
	secondBytes, _ := second.jsonBytes()
	if !bytes.Equal(firstBytes, secondBytes) || first.Result != "FAIL" {
		t.Fatal("failed executed subcase did not deterministically derive artifact FAIL")
	}
	for _, item := range first.Cases {
		if item.ID == "EEBUS-G10" && item.Status != "FAIL" {
			t.Fatal("G10 aggregate did not fail when an executed required subcase mismatched")
		}
		if item.ID != "EEBUS-G10" && item.Status != "PASS" {
			t.Fatal("one gate failure contaminated an independently complete gate")
		}
	}
}

func TestMSP04CG16RunAndCaseLabelsAreRandomAcrossRunsAndReplayable(t *testing.T) {
	firstInput := validMSP04CArtifactInput(4)
	secondInput := validMSP04CArtifactInput(4)
	firstInput.RunSeed = [32]byte{}
	secondInput.RunSeed = [32]byte{}
	first, err := newFirstTrustArtifact(firstInput)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newFirstTrustArtifact(secondInput)
	if err != nil {
		t.Fatal(err)
	}
	if first.RunLabel == second.RunLabel {
		t.Fatal("independent runs reused a run label")
	}
	for index := range first.Cases {
		if first.Cases[index].CaseLabel == second.Cases[index].CaseLabel {
			t.Fatal("independent runs reused a case label")
		}
	}

	replayInput := firstInput
	replayInput.RunSeed = first.replaySeed
	replay, err := newFirstTrustArtifact(replayInput)
	if err != nil {
		t.Fatal(err)
	}
	firstBytes, _ := first.jsonBytes()
	replayBytes, _ := replay.jsonBytes()
	if !bytes.Equal(firstBytes, replayBytes) {
		t.Fatal("captured run seed did not deterministically replay labels and artifact bytes")
	}
}

func validMSP04CArtifactInput(seedByte byte) firstTrustArtifactInput {
	input := firstTrustArtifactInput{
		Repo: "Project-Helianthus/helianthus-eebusreg", Branch: "issue/28-msp04c-restore-quarantine",
		Commit: strings.Repeat("1", 40), Issue: "MSP-04C", GoVersion: "go1.25.0", GoWork: "off", GoToolchain: "auto",
		Commands: []firstTrustArtifactCommand{firstTrustCommandAPIFreeze, firstTrustCommandDiffCheck, firstTrustCommandUnit},
	}
	input.RunSeed[0] = seedByte
	ids := make([]int, 0, len(firstTrustRequiredSubcases))
	for subcase := range firstTrustRequiredSubcases {
		ids = append(ids, int(subcase))
	}
	slices.Sort(ids)
	for _, raw := range ids {
		subcase := firstTrustSubcase(raw)
		expected := firstTrustRequiredSubcases[subcase]
		input.Transcript = append(input.Transcript, firstTrustSubcaseObservation{
			Subcase: subcase, State: expected.state, Reason: expected.reason, Outcome: expected.outcome,
			Count: expected.count, DelaySeconds: expected.delaySeconds,
		})
	}
	return input
}
