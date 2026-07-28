package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	executor "github.com/Project-Helianthus/helianthus-eebus-go/features/executor"
	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	"golang.org/x/mod/modfile"
)

func TestIssue83DependencyClosurePinsExactExecutorRelease(t *testing.T) {
	// Module and script closure are build inputs rather than runtime behavior, so
	// exact source attestation is the only direct test for the reviewed release.
	payload, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	file, err := modfile.Parse("go.mod", payload, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]string, len(file.Require))
	for _, requirement := range file.Require {
		got[requirement.Mod.Path] = requirement.Mod.Version
	}
	want := map[string]string{
		"github.com/Project-Helianthus/helianthus-eebus-go": "v0.7.1-helianthus.11",
		"github.com/Project-Helianthus/helianthus-ship-go":  "v0.6.1-helianthus.9",
		"github.com/Project-Helianthus/helianthus-spine-go": "v0.7.1-helianthus.7",
	}
	for path, version := range want {
		if got[path] != version {
			t.Errorf("go.mod %s = %q, want %q", path, got[path], version)
		}
	}
	if len(file.Replace) != 0 {
		t.Fatalf("go.mod contains %d forbidden replacement directives", len(file.Replace))
	}

	script, err := os.ReadFile(filepath.Join("..", "..", "scripts", "toolchain_boundary_proof.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for path, version := range want {
		for _, token := range []string{path, version} {
			if !strings.Contains(string(script), token) {
				t.Errorf("toolchain boundary proof omits %q", token)
			}
		}
	}
}

func TestIssue83PinnedExecutorUnknownFieldLossBoundary(t *testing.T) {
	// This is necessarily a dependency-shape proof: behavior after decoding
	// cannot recover extension members that the generated dependency types did
	// not retain.
	response := reflect.TypeOf(spineapi.CorrelatedResponse{})
	if _, found := response.FieldByName("Header"); !found {
		t.Fatal("spine-go correlated response no longer exposes typed header metadata")
	}
	for _, forbidden := range []string{"Raw", "Unknown", "Extensions"} {
		if _, found := response.FieldByName(forbidden); found {
			t.Fatalf("spine-go correlated response now exposes %s; revisit the narrow metadata bridge", forbidden)
		}
	}
	result := reflect.TypeOf(executor.ExactFeatureResult{})
	if _, found := result.FieldByName("Header"); found {
		t.Fatal("eebus-go executor now retains response header; revisit the sidecar bridge")
	}
	for _, forbidden := range []string{"Raw", "Unknown", "Extensions"} {
		if _, found := result.FieldByName(forbidden); found {
			t.Fatalf("eebus-go executor now exposes %s; revisit the narrow metadata bridge", forbidden)
		}
	}
}
