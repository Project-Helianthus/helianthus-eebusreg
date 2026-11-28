//go:build !eebusreg_second_host

package eebusservicebridge_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestSecondHostReadsAndWrites rebuilds the driver-backed proof. That file
// imports the driver tree, and the preflight self-test moves that tree aside
// before it typechecks this module, so the import is compiled only with the tag.
func TestSecondHostReadsAndWrites(t *testing.T) {
	args := []string{"test", "-count=1", "-timeout", "20m", "-v", "-tags", "eebusreg_second_host"}
	if secondHostRaceEnabled() && !strings.Contains(os.Getenv("GOFLAGS"), "-race") {
		args = append(args, "-race")
	}
	args = append(args, "./internal/eebusservicebridge", "-run", "^TestSecondHostReadsAndWrites$")
	cmd := exec.Command("go", args...)
	cmd.Dir = moduleRoot(t)
	cmd.Env = withoutGowork(os.Environ())
	out, err := cmd.CombinedOutput()
	text := string(out)
	if err != nil || strings.Contains(text, "no tests to run") || !strings.Contains(text, "--- PASS: TestSecondHostReadsAndWrites") {
		t.Fatalf("second host proof: %v\n%s", err, text)
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test file path is unavailable")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("module root is unavailable")
		}
		dir = parent
	}
}

func withoutGowork(env []string) []string {
	kept := make([]string, 0, len(env)+1)
	for _, item := range env {
		if strings.HasPrefix(item, "GOWORK=") {
			continue
		}
		kept = append(kept, item)
	}
	return append(kept, "GOWORK=off")
}
