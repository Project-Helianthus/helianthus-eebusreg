package main

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

func TestMSP04CR2DependencyClosureUsesReviewedHelianthusReleases(t *testing.T) {
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
		{path: "github.com/Project-Helianthus/helianthus-eebus-go", version: "v0.7.1-helianthus.1"},
		{path: "github.com/Project-Helianthus/helianthus-ship-go", version: "v0.6.1-helianthus.1"},
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
}
