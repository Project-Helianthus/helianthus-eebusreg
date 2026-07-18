package main

import (
	"reflect"
	"testing"
)

func TestMSP05PDependencyAdoptionPreservesImmutableG17G19Evidence(t *testing.T) {
	report := newReport("fake-peer", []string{caseFakePeer}, []caseResult{{
		ID:       caseFakePeer,
		Status:   resultBlocked,
		Evidence: []string{"historical-fixture"},
	}}, nil)
	if report.Module.Path != "github.com/Project-Helianthus/helianthus-eebus-go" || report.Module.Version != "v0.7.1-helianthus.1" {
		t.Fatalf("G17 report module evidence changed: %+v", report.Module)
	}

	evidence := buildLiveGateEvidence(
		liveOptions{},
		liveRunBinding{},
		operatorProofInput{},
		spineCapture{},
		nil,
		caseResult{ID: caseDirectAccess},
		negativeObservation{},
		negativeObservation{},
		replayArtifact{},
	)
	want := map[string]string{
		"eebus-go": "v0.7.1-helianthus.1",
		"ship-go":  "v0.6.1-helianthus.1",
		"spine-go": "v0.7.1-helianthus.1",
	}
	if !reflect.DeepEqual(evidence.Environment.ToolVersions, want) {
		t.Fatalf("G19 historical tool versions = %v, want %v", evidence.Environment.ToolVersions, want)
	}
}
