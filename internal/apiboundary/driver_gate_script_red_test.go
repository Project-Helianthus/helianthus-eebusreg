package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func gateScriptEnbilitySection(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "scripts", "api_boundary_gate.sh"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	helperStart, blockStart, blockEnd := -1, -1, -1
	for index, line := range lines {
		switch {
		case line == "git_grep_checked() {":
			helperStart = index
		case line == `echo "==> enbility import boundary"`:
			blockStart = index
		case line == `echo "==> public API AST boundary"`:
			blockEnd = index
		}
	}
	if helperStart < 0 || blockStart < 0 || blockEnd < 0 || blockEnd <= blockStart {
		t.Fatalf("gate script sections not found: helper=%d block=%d end=%d", helperStart, blockStart, blockEnd)
	}
	helperEnd := -1
	for index := helperStart + 1; index < len(lines); index++ {
		if lines[index] == "}" {
			helperEnd = index
			break
		}
	}
	if helperEnd < 0 {
		t.Fatal("git_grep_checked helper end not found")
	}
	return "set -euo pipefail\n" +
		strings.Join(lines[helperStart:helperEnd+1], "\n") + "\n" +
		strings.Join(lines[blockStart:blockEnd], "\n") + "\n"
}

func runGateEnbilitySection(t *testing.T, root string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", gateScriptEnbilitySection(t))
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func TestGateScriptEnbilityGrepAdmitsPinnedStackOnlyUnderDriver(t *testing.T) {
	const sample = "package sample\n\nimport \"%s\"\n"
	cases := []struct {
		name       string
		path       string
		importPath string
		admitted   bool
	}{
		{"driver admits pinned eebus-go", "driver/sample/a.go", "github.com/enbility/eebus-go/api", true},
		{"driver admits pinned ship-go", "driver/sample/b.go", "github.com/enbility/ship-go/api", true},
		{"driver admits pinned spine-go model", "driver/sample/c.go", "github.com/enbility/spine-go/model", true},
		{"driver rejects zeroconf", "driver/sample/d.go", "github.com/enbility/zeroconf/v2", false},
		{"driver rejects a prefix-alike root", "driver/sample/e.go", "github.com/enbility/eebus-go-extra/api", false},
		{"root rejects pinned eebus-go", "sample.go", "github.com/enbility/eebus-go/api", false},
		{"eebusraw rejects pinned eebus-go", "eebusraw/sample.go", "github.com/enbility/eebus-go/api", false},
		{"internal keeps pinned eebus-go", "internal/eebusstore/sample.go", "github.com/enbility/eebus-go/api", true},
		{"internal keeps zeroconf", "internal/eebusstore/zeroconf.go", "github.com/enbility/zeroconf/v2", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := newDriverRepository(t)
			writeDriverFile(t, root, tc.path, strings.Replace(sample, "%s", tc.importPath, 1))
			initDriverGitRepository(t, root)
			output, err := runGateEnbilitySection(t, root)
			if tc.admitted && err != nil {
				t.Fatalf("enbility grep rejected %s: %v\n%s", tc.importPath, err, output)
			}
			if !tc.admitted && err == nil {
				t.Fatalf("enbility grep admitted %s in %s\n%s", tc.importPath, tc.path, output)
			}
		})
	}
}
