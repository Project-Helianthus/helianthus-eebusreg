package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		"github.com/Project-Helianthus/helianthus-eebus-go": "v0.7.1-helianthus.10",
		"github.com/Project-Helianthus/helianthus-ship-go":  "v0.6.1-helianthus.9",
		"github.com/Project-Helianthus/helianthus-spine-go": "v0.7.1-helianthus.5",
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
