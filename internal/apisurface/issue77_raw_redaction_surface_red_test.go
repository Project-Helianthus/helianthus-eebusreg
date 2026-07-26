package main

import (
	"os/exec"
	"sort"
	"strings"
	"testing"
)

func TestIssue77InitialV1ExportsRawSourceAndSeparateRedactedProjection(t *testing.T) {
	doc, err := extract(moduleRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	root := msp05pRootSurface(t, doc)
	required := map[string]bool{
		"BuildRedactedSnapshotV1": false,
		"DeviceV1":                false,
		"EntityV1":                false,
		"FeatureV1":               false,
		"MetadataV1":              false,
		"MetadataValueV1":         false,
		"OpaqueObservationV1":     false,
		"OpaqueScalarV1":          false,
		"OpaqueValueV1":           false,
		"PairingObservationV1":    false,
		"RedactedDeviceV1":        false,
		"RedactedEntityV1":        false,
		"RedactedFeatureV1":       false,
		"RedactedServiceV1":       false,
		"RedactedSessionV1":       false,
		"RedactedSnapshotV1":      false,
		"RedactedUseCaseV1":       false,
		"ServiceV1":               false,
		"SessionV1":               false,
		"SnapshotV1":              false,
		"UseCaseV1":               false,
	}
	forbidden := map[string]struct{}{
		"RawSnapshotV1":        {},
		"RawSnapshotV2":        {},
		"SnapshotV2":           {},
		"RedactedSnapshotV2":   {},
		"TopologyV1":           {},
		"UseCaseClaimV1":       {},
		"FeatureRoleV1Special": {},
		"CandidateRef":         {},
	}
	var leaked []string
	for _, symbol := range root.Symbols {
		if _, ok := required[symbol.Name]; ok {
			required[symbol.Name] = true
		}
		if _, ok := forbidden[symbol.Name]; ok {
			leaked = append(leaked, symbol.Name)
		}
		lower := strings.ToLower(symbol.Name)
		if strings.Contains(lower, "legacy") || strings.Contains(lower, "alias") ||
			strings.Contains(lower, "candidate_ref") || strings.Contains(lower, "candidateref") {
			leaked = append(leaked, symbol.Name)
		}
	}
	sort.Strings(leaked)
	if len(leaked) != 0 {
		t.Errorf("initial unpublished v1 leaked forbidden compatibility surface: %v", leaked)
	}
	var missing []string
	for name, found := range required {
		if !found {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) != 0 {
		t.Errorf("initial unpublished v1 is missing raw/redacted value exports: %v", missing)
	}
}

func TestIssue77PublicSignaturesContainNoProtocolImplementationTypes(t *testing.T) {
	doc, err := extract(moduleRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range doc.Packages {
		for _, imported := range pkg.Imports {
			for _, forbidden := range []string{
				"github.com/enbility/",
				"github.com/Project-Helianthus/helianthus-eebus-go/",
				"github.com/Project-Helianthus/helianthus-ship-go/",
				"github.com/Project-Helianthus/helianthus-spine-go/",
			} {
				if strings.HasPrefix(imported.Path, forbidden) {
					t.Errorf("%s exposes protocol implementation import %q", pkg.Path, imported.Path)
				}
			}
		}
		for _, symbol := range pkg.Symbols {
			for _, forbidden := range []string{
				"github.com/enbility/",
				"github.com/Project-Helianthus/helianthus-eebus-go/",
				"github.com/Project-Helianthus/helianthus-ship-go/",
				"github.com/Project-Helianthus/helianthus-spine-go/",
			} {
				if strings.Contains(symbol.Signature, forbidden) {
					t.Errorf("%s.%s exposes protocol implementation type %q", pkg.Path, symbol.Name, forbidden)
				}
			}
		}
	}
}

func TestIssue77DependencyClosureDoesNotReachSemanticOrEBusRegistries(t *testing.T) {
	command := exec.Command("go", "list", "-deps", "./...")
	command.Dir = moduleRoot(t)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps ./...: %v\n%s", err, output)
	}
	for _, forbidden := range []string{
		"Project-Helianthus/helianthus-ebusreg",
		"Project-Helianthus/helianthus-modbusreg",
		"Project-Helianthus/helianthus-ebusgateway/registry",
		"Project-Helianthus/helianthus-ebusgateway/semantic",
	} {
		if strings.Contains(string(output), forbidden) {
			t.Errorf("raw eeBUS dependency closure reached forbidden registry %q", forbidden)
		}
	}
}
