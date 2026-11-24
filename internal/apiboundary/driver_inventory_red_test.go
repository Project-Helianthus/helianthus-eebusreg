package main

import (
	"slices"
	"strings"
	"testing"
)

const driverSampleInventoryName = "synthetic_driver_sample.txt"

const driverSampleSourceForInventory = `package sample

type Sample struct{}

type OpenPairingWindowRequestV1 struct{}

func (Sample) OpenPairingWindow(OpenPairingWindowRequestV1) error { return nil }

func NewSample() Sample { return Sample{} }
`

func registerSyntheticDriverInventory(t *testing.T, content string) {
	t.Helper()
	previousNames := exportInventoryNames
	previousSource := exportInventorySource
	exportInventoryNames = append(slices.Clone(previousNames), driverSampleInventoryName)
	exportInventoryPackages[driverSampleInventoryName] = "driver/sample"
	exportInventorySource = func(name string) ([]byte, error) {
		if name == driverSampleInventoryName {
			return []byte(content), nil
		}
		return previousSource(name)
	}
	t.Cleanup(func() {
		exportInventoryNames = previousNames
		exportInventorySource = previousSource
		delete(exportInventoryPackages, driverSampleInventoryName)
	})
}

func productionInventoryViolations(t *testing.T, root string) []string {
	t.Helper()
	_, violations, err := inspectGoBoundary(root)
	if err != nil {
		t.Fatal(err)
	}
	return violations
}

func writeSyntheticDriverSample(t *testing.T, root string) {
	t.Helper()
	writeDriverFile(t, root, "driver/sample/sample.go", driverSampleSourceForInventory)
}

const correctDriverSampleInventory = "func NewSample\nfunc Sample.OpenPairingWindow\ntype OpenPairingWindowRequestV1\ntype Sample\n"

func requireInventoryDriftOutcome(t *testing.T, root string, wantFailure bool, fragment string) {
	t.Helper()
	for name, violations := range map[string][]string{
		"test drift":         observeDriverDriftViolations(t, root),
		"production command": productionInventoryViolations(t, root),
	} {
		failed := false
		for _, violation := range violations {
			if strings.Contains(violation, fragment) {
				failed = true
			}
		}
		if failed != wantFailure {
			t.Fatalf("%s: failure=%v want %v (fragment %q); violations: %v", name, failed, wantFailure, fragment, violations)
		}
		if !wantFailure && len(violations) != 0 {
			t.Fatalf("%s: unexpected violations: %v", name, violations)
		}
	}
}

func TestSyntheticDriverInventoryDriftEntryPoints(t *testing.T) {
	t.Run("registered correct inventory passes", func(t *testing.T) {
		root := newDriverRepository(t)
		writeSyntheticDriverSample(t, root)
		registerSyntheticDriverInventory(t, correctDriverSampleInventory)
		requireInventoryDriftOutcome(t, root, false, "")
	})
	t.Run("nonexistent line fails", func(t *testing.T) {
		root := newDriverRepository(t)
		writeSyntheticDriverSample(t, root)
		registerSyntheticDriverInventory(t, correctDriverSampleInventory+"func Sample.Missing\n")
		requireInventoryDriftOutcome(t, root, true, "func Sample.Missing")
	})
	t.Run("export listed in no inventory fails", func(t *testing.T) {
		root := newDriverRepository(t)
		writeSyntheticDriverSample(t, root)
		registerSyntheticDriverInventory(t, "func NewSample\nfunc Sample.OpenPairingWindow\ntype Sample\n")
		requireInventoryDriftOutcome(t, root, true, "OpenPairingWindowRequestV1")
	})
	t.Run("every registration omitted fails", func(t *testing.T) {
		root := newDriverRepository(t)
		writeSyntheticDriverSample(t, root)
		requireInventoryDriftOutcome(t, root, true, "NewSample")
	})
	t.Run("no driver package passes", func(t *testing.T) {
		root := newDriverRepository(t)
		requireInventoryDriftOutcome(t, root, false, "")
	})
}
