package eebusfacade

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMSP04CR2ProductionFacadeUsesOnlyReviewedForkIdentities(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var production strings.Builder
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		production.WriteString(readMSP04CR2ProductionFile(t, entry.Name()))
	}
	source := production.String()
	for _, path := range []string{
		"github.com/enbility/eebus-go",
		"github.com/enbility/ship-go",
		"github.com/enbility/spine-go",
	} {
		if strings.Contains(source, path) {
			t.Errorf("production facade retains forbidden upstream identity %s", path)
		}
	}
	for _, path := range []string{
		"github.com/Project-Helianthus/helianthus-eebus-go",
		"github.com/Project-Helianthus/helianthus-ship-go",
		"github.com/Project-Helianthus/helianthus-spine-go",
	} {
		if !strings.Contains(source, path) {
			t.Errorf("production facade lacks reviewed fork identity %s", path)
		}
	}
	for _, forbidden := range []string{"graphql", "mcp", "semantic"} {
		if strings.Contains(strings.ToLower(source), forbidden) {
			t.Errorf("pre-dial lifecycle leaked into forbidden consumer surface %q", forbidden)
		}
	}
}

func readMSP04CR2ProductionFile(t *testing.T, name string) string {
	t.Helper()
	payload, err := os.ReadFile(filepath.Clean(name))
	if err != nil {
		t.Fatalf("read production facade %s: %v", name, err)
	}
	return string(payload)
}
