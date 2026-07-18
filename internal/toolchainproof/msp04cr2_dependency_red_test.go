package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

func TestMSP05PDependencyClosureUsesReviewedHelianthusReleases(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatalf("read repository go.mod: %v", err)
	}
	file, err := modfile.Parse("go.mod", payload, nil)
	if err != nil {
		t.Fatalf("parse repository go.mod: %v", err)
	}

	want := []struct {
		path    string
		version string
	}{
		{path: "github.com/Project-Helianthus/helianthus-eebus-go", version: "v0.7.1-helianthus.2"},
		{path: "github.com/Project-Helianthus/helianthus-ship-go", version: "v0.6.1-helianthus.3"},
		{path: "github.com/Project-Helianthus/helianthus-spine-go", version: "v0.7.1-helianthus.1"},
	}
	got := make(map[string]string, len(file.Require))
	for _, required := range file.Require {
		got[required.Mod.Path] = required.Mod.Version
	}
	for _, requirement := range want {
		if got[requirement.path] != requirement.version {
			t.Errorf("go.mod requirement %s = %q, want reviewed release %q", requirement.path, got[requirement.path], requirement.version)
		}
		if !semver.IsValid(requirement.version) || module.IsPseudoVersion(requirement.version) {
			t.Fatalf("test contract contains non-release version %q for %s", requirement.version, requirement.path)
		}
	}
	for _, requirement := range want[:2] {
		command := exec.Command("go", "list", "-m", "-json", requirement.path)
		command.Dir = filepath.Join("..", "..")
		command.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local")
		output, err := command.Output()
		if err != nil {
			t.Fatalf("resolve selected module %s: %v", requirement.path, err)
		}
		var selected moduleInfo
		if err := json.Unmarshal(output, &selected); err != nil {
			t.Fatalf("parse selected module %s: %v", requirement.path, err)
		}
		if selected.Path != requirement.path || selected.Version != requirement.version || selected.Replace != nil {
			t.Errorf("selected module = %+v, want %s@%s without replacement", selected, requirement.path, requirement.version)
		}
	}

	forbidden := []string{
		"github.com/enbility/eebus-go",
		"github.com/enbility/ship-go",
		"github.com/enbility/spine-go",
	}
	for _, path := range forbidden {
		if version, exists := got[path]; exists {
			t.Errorf("go.mod retains forbidden upstream identity %s@%s", path, version)
		}
	}
	if len(file.Replace) != 0 {
		for _, replacement := range file.Replace {
			t.Errorf("go.mod replace is forbidden: %s => %s", replacement.Old.Path, replacement.New.Path)
		}
	}
	for _, required := range file.Require {
		for _, requirement := range want {
			if required.Mod.Path == requirement.path && module.IsPseudoVersion(required.Mod.Version) {
				t.Errorf("reviewed fork %s uses forbidden pseudo-version %s", required.Mod.Path, required.Mod.Version)
			}
		}
	}

	sumPayload, err := os.ReadFile(filepath.Join("..", "..", "go.sum"))
	if err != nil {
		t.Fatalf("read repository go.sum: %v", err)
	}
	sums := string(sumPayload)
	for _, requirement := range want[:2] {
		for _, suffix := range []string{" ", "/go.mod "} {
			entry := requirement.path + " " + requirement.version + suffix
			if !strings.Contains(sums, entry) {
				t.Errorf("go.sum is missing reviewed dependency entry prefix %q", entry)
			}
		}
	}
	for _, stale := range []string{
		"github.com/Project-Helianthus/helianthus-eebus-go v0.7.1-helianthus.1 ",
		"github.com/Project-Helianthus/helianthus-ship-go v0.6.1-helianthus.2 ",
	} {
		if strings.Contains(sums, stale) {
			t.Errorf("go.sum retains stale current dependency entry %q", stale)
		}
	}
}
