package main

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/mod/modfile"
)

func TestIssue122PINDependenciesAreExactReviewedReleases(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatalf("read repository go.mod: %v", err)
	}
	file, err := modfile.Parse("go.mod", payload, nil)
	if err != nil {
		t.Fatalf("parse repository go.mod: %v", err)
	}
	want := map[string]string{
		"github.com/Project-Helianthus/helianthus-eebus-go": "v0.7.1-helianthus.18",
		"github.com/Project-Helianthus/helianthus-ship-go":  "v0.6.1-helianthus.16",
	}
	got := make(map[string]string)
	for _, requirement := range file.Require {
		got[requirement.Mod.Path] = requirement.Mod.Version
	}
	for path, version := range want {
		if got[path] != version {
			t.Errorf("go.mod %s = %q, want exact reviewed release %q", path, got[path], version)
		}
	}
	if len(file.Replace) != 0 {
		t.Fatalf("go.mod contains %d forbidden replacement directives", len(file.Replace))
	}
}
