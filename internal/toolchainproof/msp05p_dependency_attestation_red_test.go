package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMSP05PCommandDefaultPinsReviewedEEBusGo(t *testing.T) {
	root := writeGoMod(t, `module github.com/Project-Helianthus/helianthus-eebusreg

go 1.22.0

require github.com/Project-Helianthus/helianthus-eebus-go v0.7.1-helianthus.20
`)
	command := exec.Command("go", "run", ".", "-repo-root", root, "-max-go", "1.22")
	command.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("toolchain proof default rejected reviewed eebus-go: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "github.com/Project-Helianthus/helianthus-eebus-go@v0.7.1-helianthus.20") {
		t.Fatalf("toolchain proof output omitted reviewed eebus-go pin:\n%s", output)
	}
}

func TestMSP05PToolchainScriptDeclaresReviewedRuntimeClosure(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("..", "..", "scripts", "toolchain_boundary_proof.sh"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(payload)
	for _, required := range []struct {
		name    string
		path    string
		version string
	}{
		{
			name:    "eebus",
			path:    "github.com/Project-Helianthus/helianthus-eebus-go",
			version: "v0.7.1-helianthus.20",
		},
		{
			name:    "ship",
			path:    "github.com/Project-Helianthus/helianthus-ship-go",
			version: "v0.6.1-helianthus.18",
		},
		{
			name:    "spine",
			path:    "github.com/Project-Helianthus/helianthus-spine-go",
			version: "v0.7.1-helianthus.9",
		},
	} {
		for _, assignment := range []string{
			required.name + `_module_path="` + required.path + `"`,
			required.name + `_module_version="` + required.version + `"`,
		} {
			if !strings.Contains(source, assignment) {
				t.Errorf("toolchain boundary proof omits current dependency assignment %q", assignment)
			}
		}
	}
	for _, stale := range []struct {
		name     string
		versions []string
	}{
		{
			name:     "eebus",
			versions: []string{"v0.7.1-helianthus.1", "v0.7.1-helianthus.2", "v0.7.1-helianthus.3", "v0.7.1-helianthus.7", "v0.7.1-helianthus.8", "v0.7.1-helianthus.11"},
		},
		{
			name:     "ship",
			versions: []string{"v0.6.1-helianthus.2", "v0.6.1-helianthus.4", "v0.6.1-helianthus.5", "v0.6.1-helianthus.7", "v0.6.1-helianthus.8"},
		},
		{
			name:     "spine",
			versions: []string{"v0.7.1-helianthus.1", "v0.7.1-helianthus.2", "v0.7.1-helianthus.3", "v0.7.1-helianthus.6", "v0.7.1-helianthus.7"},
		},
	} {
		for _, version := range stale.versions {
			assignment := stale.name + `_module_version="` + version + `"`
			if strings.Contains(source, assignment) {
				t.Errorf("toolchain boundary proof retains stale current dependency assignment %q", assignment)
			}
		}
	}
}
