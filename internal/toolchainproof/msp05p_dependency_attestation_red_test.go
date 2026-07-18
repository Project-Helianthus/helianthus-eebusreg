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

require github.com/Project-Helianthus/helianthus-eebus-go v0.7.1-helianthus.2
`)
	command := exec.Command("go", "run", ".", "-repo-root", root, "-max-go", "1.22")
	command.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("toolchain proof default rejected reviewed eebus-go: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "github.com/Project-Helianthus/helianthus-eebus-go@v0.7.1-helianthus.2") {
		t.Fatalf("toolchain proof output omitted reviewed eebus-go pin:\n%s", output)
	}
}

func TestMSP05PToolchainScriptDeclaresReviewedRuntimeClosure(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("..", "..", "scripts", "toolchain_boundary_proof.sh"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(payload)
	for _, required := range []string{
		"github.com/Project-Helianthus/helianthus-eebus-go",
		"v0.7.1-helianthus.2",
		"github.com/Project-Helianthus/helianthus-ship-go",
		"v0.6.1-helianthus.3",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("toolchain boundary proof omits current dependency token %q", required)
		}
	}
	for _, stale := range []string{"v0.7.1-helianthus.1", "v0.6.1-helianthus.2"} {
		if strings.Contains(source, stale) {
			t.Errorf("toolchain boundary proof retains stale current pin %q", stale)
		}
	}
}
