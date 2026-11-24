package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const driverSemregModule = "github.com/Project-Helianthus/helianthus-semreg"

func driverSampleSource(imports []string, body string) string {
	var builder strings.Builder
	builder.WriteString("package sample\n\nimport (\n")
	for _, imported := range imports {
		fmt.Fprintf(&builder, "\t%q\n", imported)
	}
	builder.WriteString(")\n\n")
	builder.WriteString(body)
	return builder.String()
}

func driverBoundaryViolations(t *testing.T, source string) []string {
	t.Helper()
	root := newDriverRepository(t)
	writeDriverFile(t, root, "driver/sample/sample.go", source)
	_, violations, err := inspectGoBoundary(root)
	if err != nil {
		t.Fatal(err)
	}
	return withoutInventoryDrift(violations)
}

func withoutInventoryDrift(violations []string) []string {
	var kept []string
	for _, violation := range violations {
		if !strings.Contains(violation, "exports are missing from every inventory") {
			kept = append(kept, violation)
		}
	}
	return kept
}

func requireDriverAccepted(t *testing.T, source string) {
	t.Helper()
	if violations := driverBoundaryViolations(t, source); len(violations) != 0 {
		t.Fatalf("driver package was rejected: %v\n%s", violations, source)
	}
}

func requireDriverRejected(t *testing.T, source, fragment string) {
	t.Helper()
	violations := driverBoundaryViolations(t, source)
	for _, violation := range violations {
		if strings.Contains(violation, fragment) {
			return
		}
	}
	t.Fatalf("driver package was not rejected with %q; violations: %v\n%s", fragment, violations, source)
}

func TestDriverPackageAdmittedWithContractAndFacadeImports(t *testing.T) {
	source := driverSampleSource([]string{
		"context",
		driverSemregModule + "/semreg/v1",
		driverSemregModule + "/semreg/v1/catalog",
		driverSemregModule + "/semreg/v1/driver",
		driverSemregModule + "/semreg/v1/graph",
		driverSemregModule + "/semreg/v1/operation",
		driverSemregModule + "/semreg/v1/roweval",
		"example.test/registry/internal/eebusfacade",
	}, `type Instance struct{}

func NewInstance() Instance { return Instance{} }

func StartOutput(input driver.SemanticInput) error { return nil }
`)
	requireDriverAccepted(t, source)
}

func TestDriverPackageSemregImportPolicyIsExactLeafSet(t *testing.T) {
	for _, leaf := range []string{"semreg/v1", "semreg/v1/driver", "semreg/v1/roweval", "semreg/v1/catalog", "semreg/v1/operation", "semreg/v1/graph"} {
		t.Run("accepts "+leaf, func(t *testing.T) {
			requireDriverAccepted(t, driverSampleSource([]string{driverSemregModule + "/" + leaf}, "type Instance struct{}\n"))
		})
	}
	for _, other := range []string{"semreg/host", "semreg/runtime", "semreg/v1x", "semreg/v1/driverkit", "semreg/v1/rowevalx", "semreg/v1/graph/lens", "semreg/v1/catalog/internal", "semreg"} {
		t.Run("rejects "+other, func(t *testing.T) {
			requireDriverRejected(t, driverSampleSource([]string{driverSemregModule + "/" + other}, "type Instance struct{}\n"), "semreg")
		})
	}
}

func TestDriverPackagePinnedEEBusStackImportsAreAdmitted(t *testing.T) {
	requireDriverAccepted(t, driverSampleSource([]string{
		"github.com/Project-Helianthus/helianthus-eebus-go/api",
		"github.com/Project-Helianthus/helianthus-ship-go/logging",
		"github.com/Project-Helianthus/helianthus-spine-go/model",
		"github.com/enbility/eebus-go/api",
		"github.com/enbility/ship-go/api",
		"github.com/enbility/spine-go/model",
	}, "type Instance struct{}\n"))
}

func TestDriverPackageRejectsGatewayAndInternalImplementationImports(t *testing.T) {
	requireDriverRejected(t, driverSampleSource([]string{"github.com/Project-Helianthus/helianthus-gateway/internal/x"}, "type Instance struct{}\n"), "gateway")
	requireDriverRejected(t, driverSampleSource([]string{"example.test/registry/internal/eebusstore"}, "type Instance struct{}\n"), "internal implementation")
}

func TestDriverPackageFacadeImportRejectsExportedInternalType(t *testing.T) {
	requireDriverRejected(t, driverSampleSource([]string{"example.test/registry/internal/eebusfacade"}, "func Open(facade eebusfacade.Facade) {}\n"), "internal implementation")
}

func TestDriverPackageStartOutputMaySignSemanticInputOnlyFromDriverContract(t *testing.T) {
	requireDriverAccepted(t, driverSampleSource([]string{driverSemregModule + "/semreg/v1/driver"},
		"func StartOutput(input driver.SemanticInput) error { return nil }\n"))
	requireDriverRejected(t, driverSampleSource([]string{driverSemregModule + "/semreg/v1/driver"},
		"type SemanticInput interface{}\n"), "Semantic")
	requireDriverRejected(t, driverSampleSource([]string{driverSemregModule + "/semreg/v1/driver"},
		"type SemanticAlias = driver.SemanticInput\n"), "Semantic")
	requireDriverRejected(t, driverSampleSource([]string{driverSemregModule + "/semreg/v1/driver"},
		"func Other(input driver.SemanticInput) error { return nil }\n"), "Semantic")
	requireDriverRejected(t, driverSampleSource([]string{driverSemregModule + "/semreg/v1/driver"},
		"func StartOutput(input SemanticInput) error { return nil }\n"), "Semantic")
}

func TestDriverPackageRejectsPackageVariableInstance(t *testing.T) {
	requireDriverRejected(t, driverSampleSource(nil, "type Sample struct{}\n\nvar Default = &Sample{}\n"), "instance")
	requireDriverRejected(t, driverSampleSource(nil, "type Sample struct{}\n\nvar primary Sample\n"), "instance")
	requireDriverRejected(t, driverSampleSource(nil, "type Sample struct{}\n\nfunc NewSample() *Sample { return &Sample{} }\n\nvar Primary = NewSample()\n"), "instance")
}

func TestDriverPackageAcceptsExportedErrorVariable(t *testing.T) {
	requireDriverAccepted(t, driverSampleSource([]string{"errors"}, "var ErrClosed = errors.New(\"closed\")\n\nvar ErrDisabled error = errors.New(\"disabled\")\n"))
}

func TestDriverPackageRejectsTrustMutationExport(t *testing.T) {
	requireDriverRejected(t, driverSampleSource(nil, "func SetTrustMutation(value string) error { return nil }\n"), "TrustMutation")
}

func TestDriverPackageAcceptsPairingHookOnDriverType(t *testing.T) {
	requireDriverAccepted(t, driverSampleSource(nil, "type Sample struct{}\n\ntype OpenPairingWindowRequestV1 struct{}\n\nfunc (Sample) OpenPairingWindow(OpenPairingWindowRequestV1) error { return nil }\n"))
}

func newDriverRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeDriverFile(t, root, "go.mod", "module example.test/registry\n\ngo 1.22.0\n")
	writeDriverFile(t, root, "eebusraw/doc.go", "package eebusraw\n")
	return root
}

func writeDriverFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func initDriverGitRepository(t *testing.T, root string) {
	t.Helper()
	for _, args := range [][]string{{"init", "-q"}, {"add", "--", "."}} {
		command := exec.Command("git", args...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
}
