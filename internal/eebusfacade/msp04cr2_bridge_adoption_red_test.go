package eebusfacade

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMSP04CR2RuntimeInstallsReleasedOutgoingBridgeBeforeServiceSetup(t *testing.T) {
	runtimeSource := readMSP04CR2ProductionFile(t, "runtime.go")
	allSource := runtimeSource + readMSP04CR2ProductionFile(t, "runtime_first_trust.go") + readOptionalMSP04CR2ProductionFile(t, "runtime_outgoing_attempt.go")

	bridge := strings.Index(runtimeSource, "newFirstTrustOutgoingAttemptBridge")
	serviceFactory := strings.Index(runtimeSource, "dependencies.newService")
	setup := strings.Index(runtimeSource, "service.Setup")
	if bridge < 0 {
		t.Fatal("production acquireRuntime lacks newFirstTrustOutgoingAttemptBridge")
	}
	if serviceFactory < 0 || setup < 0 || bridge > serviceFactory || serviceFactory > setup {
		t.Fatalf("runtime bridge/service/setup order = %d/%d/%d, want bridge < service < setup", bridge, serviceFactory, setup)
	}
	if !strings.Contains(allSource, "NewServiceWithOutgoingAttemptBridge") {
		t.Fatal("production runtime does not use the released eebus-go outgoing-attempt constructor")
	}
	if !strings.Contains(allSource, "OutgoingAttemptBridgeConfiguration") {
		t.Fatal("production runtime does not bind both the released gate and attempt-aware sink")
	}
	outgoingSource := readOptionalMSP04CR2ProductionFile(t, "runtime_outgoing_attempt.go")
	if strings.Contains(outgoingSource, "internal/eebusstore") {
		t.Fatal("released lifecycle adapter writes the store directly instead of using the coordinator")
	}
}

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

func readOptionalMSP04CR2ProductionFile(t *testing.T, name string) string {
	t.Helper()
	payload, err := os.ReadFile(filepath.Clean(name))
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatalf("read optional production facade %s: %v", name, err)
	}
	return string(payload)
}
