package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunAcceptsPinnedModuleWithoutReplace(t *testing.T) {
	root := writeGoMod(t, `module github.com/Project-Helianthus/helianthus-eebusreg

go 1.22.0

require github.com/enbility/eebus-go v0.7.0
`)
	if err := run(config{
		repoRoot:      root,
		maxGo:         "1.22",
		modulePath:    "github.com/enbility/eebus-go",
		moduleVersion: "v0.7.0",
	}); err != nil {
		t.Fatalf("run() error = %v", err)
	}
}

func TestRunRejectsExceededGoDirective(t *testing.T) {
	root := writeGoMod(t, `module github.com/Project-Helianthus/helianthus-eebusreg

go 1.23.0

require github.com/enbility/eebus-go v0.7.0
`)
	err := run(config{
		repoRoot:      root,
		maxGo:         "1.22",
		modulePath:    "github.com/enbility/eebus-go",
		moduleVersion: "v0.7.0",
	})
	if err == nil {
		t.Fatal("run() succeeded for exceeded go directive")
	}
}

func TestRunRejectsLocalReplace(t *testing.T) {
	root := writeGoMod(t, `module github.com/Project-Helianthus/helianthus-eebusreg

go 1.22.0

require github.com/enbility/eebus-go v0.7.0

replace github.com/example/dependency => ../dependency
`)
	err := run(config{
		repoRoot:      root,
		maxGo:         "1.22",
		modulePath:    "github.com/enbility/eebus-go",
		moduleVersion: "v0.7.0",
	})
	if err == nil {
		t.Fatal("run() succeeded for local replace")
	}
}

func TestRunRejectsRemoteReplace(t *testing.T) {
	root := writeGoMod(t, `module github.com/Project-Helianthus/helianthus-eebusreg

go 1.22.0

require github.com/enbility/eebus-go v0.7.0

replace github.com/example/dependency => github.com/example/fork v1.0.0
`)
	err := run(config{
		repoRoot:      root,
		maxGo:         "1.22",
		modulePath:    "github.com/enbility/eebus-go",
		moduleVersion: "v0.7.0",
	})
	if err == nil {
		t.Fatal("run() succeeded for remote replace")
	}
}

func TestRunRejectsProtectedModuleReplace(t *testing.T) {
	root := writeGoMod(t, `module github.com/Project-Helianthus/helianthus-eebusreg

go 1.22.0

require github.com/enbility/eebus-go v0.7.0

replace github.com/enbility/eebus-go => github.com/example/eebus-go v0.7.0
`)
	err := run(config{
		repoRoot:      root,
		maxGo:         "1.22",
		modulePath:    "github.com/enbility/eebus-go",
		moduleVersion: "v0.7.0",
	})
	if err == nil {
		t.Fatal("run() succeeded for protected module replace")
	}
}

func TestVerifyModuleJSONRejectsReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "module.json")
	if err := os.WriteFile(path, []byte(`{
  "Path": "github.com/enbility/eebus-go",
  "Version": "v0.7.0",
  "Replace": {"Path": "../eebus-go"}
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	err := verifyModuleJSON(path, "github.com/enbility/eebus-go", "v0.7.0")
	if err == nil {
		t.Fatal("verifyModuleJSON() succeeded for replaced module")
	}
}

func TestNormalizeGoVersionAcceptsToolchainName(t *testing.T) {
	got, err := normalizeGoVersion("go1.22.3")
	if err != nil {
		t.Fatal(err)
	}
	want := "go1.22.3"
	if got != want {
		t.Fatalf("normalizeGoVersion() = %v, want %v", got, want)
	}
}

func TestRequireVersionAtMostComparesLanguageVersion(t *testing.T) {
	if err := requireVersionAtMost("active Go binary", "go1.22.12", "1.22"); err != nil {
		t.Fatalf("requireVersionAtMost() rejected Go patch release: %v", err)
	}
	if err := requireVersionAtMost("active Go binary", "go1.26.2", "1.22"); err == nil {
		t.Fatal("requireVersionAtMost() accepted active Go above max language")
	}
}

func writeGoMod(t *testing.T, content string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}
