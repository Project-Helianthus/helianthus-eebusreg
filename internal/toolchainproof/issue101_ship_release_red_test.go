package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
)

const issue101SHIPRelease = "v0.6.1-helianthus.11"

func TestIssue101PinsReleasedReconnectCollisionSHIP(t *testing.T) {
	// The exact module version is a reviewed supply-chain input; runtime tests
	// cannot distinguish the released source selected by the Go module graph.
	root := filepath.Join("..", "..")
	payload, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	file, err := modfile.Parse("go.mod", payload, nil)
	if err != nil {
		t.Fatal(err)
	}

	var selected string
	for _, requirement := range file.Require {
		if requirement.Mod.Path == "github.com/Project-Helianthus/helianthus-ship-go" {
			selected = requirement.Mod.Version
			break
		}
	}
	if selected != issue101SHIPRelease {
		t.Fatalf("SHIP release = %q, want %q", selected, issue101SHIPRelease)
	}
	if len(file.Replace) != 0 {
		t.Fatalf("go.mod contains %d forbidden replacement directives", len(file.Replace))
	}

	script, err := os.ReadFile(filepath.Join(root, "scripts", "toolchain_boundary_proof.sh"))
	if err != nil {
		t.Fatal(err)
	}
	assignment := `ship_module_version="` + issue101SHIPRelease + `"`
	if !strings.Contains(string(script), assignment) {
		t.Fatalf("toolchain proof omits %q", assignment)
	}

	sum, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{" ", "/go.mod "} {
		entry := "github.com/Project-Helianthus/helianthus-ship-go " + issue101SHIPRelease + suffix
		if !strings.Contains(string(sum), entry) {
			t.Errorf("go.sum omits entry prefix %q", entry)
		}
	}
}
