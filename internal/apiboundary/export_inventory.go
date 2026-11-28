package main

import (
	"embed"
	"fmt"
	"maps"
	"path"
	"sort"
	"strings"
)

const exportInventoryDirectory = "testdata/exports"

const exportInventoryRepositoryDirectory = "internal/apiboundary/" + exportInventoryDirectory

//go:embed testdata/exports/*.txt
var exportInventoryFiles embed.FS

var exportInventoryNames = []string{
	"driver.txt",
	"evidence.txt",
	"lifecycle_native_runtime_second.txt",
	"lifecycle_operator_admin_first.txt",
	"lifecycle_runtime.txt",
	"native_runtime_second.txt",
	"operator_admin_runtime.txt",
	"participation.txt",
	"participation_operator.txt",
	"peer_driver.txt",
	"peer_inbound.txt",
	"peer_output_base.txt",
	"peer_output_charger.txt",
	"raw.txt",
	"raw_feature.txt",
	"raw_mutation.txt",
}

var exportInventoryPackages = map[string]string{
	"driver.txt":                          "driver/examples/peer",
	"evidence.txt":                        "eebusevidence",
	"lifecycle_native_runtime_second.txt": ".",
	"lifecycle_operator_admin_first.txt":  ".",
	"lifecycle_runtime.txt":               ".",
	"native_runtime_second.txt":           ".",
	"operator_admin_runtime.txt":          ".",
	"participation.txt":                   ".",
	"participation_operator.txt":          ".",
	"peer_driver.txt":                     "driver",
	"peer_inbound.txt":                    "driver/inbound",
	"peer_output_base.txt":                "driver/output/base",
	"peer_output_charger.txt":             "driver/output/charger",
	"raw.txt":                             "eebusraw",
	"raw_feature.txt":                     "eebusraw",
	"raw_mutation.txt":                    "eebusraw",
}

var exportInventorySource = func(name string) ([]byte, error) {
	return exportInventoryFiles.ReadFile(path.Join(exportInventoryDirectory, name))
}

func driverInventoryDrift(observed map[string]map[manifestExport]struct{}) ([]string, error) {
	var violations []string
	covered := make(map[string]map[manifestExport]struct{})
	for _, name := range exportInventoryNames {
		directory := exportInventoryPackages[name]
		if !isDriverDirectory(directory) {
			continue
		}
		inventory, err := readExportInventory(name)
		if err != nil {
			return nil, err
		}
		// An inventory names a package. A snapshot that does not contain that
		// package is not a stale listing; a snapshot that contains it still
		// fails closed when the listing and the exports disagree.
		if _, present := observed[directory]; !present {
			continue
		}
		if stale := exportDifference(inventory, observed[directory]); len(stale) > 0 {
			violations = append(violations, fmt.Sprintf("%s lists exports that %s does not export: %s", name, directory, strings.Join(stale, ", ")))
		}
		if covered[directory] == nil {
			covered[directory] = make(map[manifestExport]struct{})
		}
		maps.Copy(covered[directory], inventory)
	}
	directories := make([]string, 0, len(observed))
	for directory := range observed {
		if isDriverDirectory(directory) {
			directories = append(directories, directory)
		}
	}
	sort.Strings(directories)
	for _, directory := range directories {
		if unlisted := exportDifference(observed[directory], covered[directory]); len(unlisted) > 0 {
			violations = append(violations, fmt.Sprintf("%s exports are missing from every inventory: %s", directory, strings.Join(unlisted, ", ")))
		}
	}
	return violations, nil
}

func exportDifference(left, right map[manifestExport]struct{}) []string {
	var difference []string
	for exported := range left {
		if _, ok := right[exported]; !ok {
			difference = append(difference, exported.Kind+" "+exported.Name)
		}
	}
	sort.Strings(difference)
	return difference
}

func loadExportInventory(name string) map[manifestExport]struct{} {
	inventory, err := readExportInventory(name)
	if err != nil {
		panic(err)
	}
	return inventory
}

func readExportInventory(name string) (map[manifestExport]struct{}, error) {
	if !isExportInventoryName(name) {
		return nil, fmt.Errorf("unknown frozen public export inventory %q", name)
	}
	data, err := exportInventorySource(name)
	if err != nil {
		return nil, fmt.Errorf("read frozen public export inventory %s: %w", name, err)
	}
	return parseExportInventory(name, string(data))
}

func parseExportInventory(name, entries string) (map[manifestExport]struct{}, error) {
	inventory := make(map[manifestExport]struct{})
	for index, line := range strings.Split(strings.TrimSpace(entries), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("invalid frozen public export inventory entry in %s line %d", name, index+1)
		}
		inventory[manifestExport{Kind: fields[0], Name: fields[1]}] = struct{}{}
	}
	return inventory, nil
}

func isExportInventoryName(name string) bool {
	for _, known := range exportInventoryNames {
		if name == known {
			return true
		}
	}
	return false
}

func isExportInventoryPath(repositoryPath string) bool {
	directory, name := path.Split(repositoryPath)
	return directory == exportInventoryRepositoryDirectory+"/" && isExportInventoryName(name)
}

var lifecycleRuntimeExports = map[manifestExport]struct{}{
	{Kind: "const", Name: "PairingPolicyClosed"}:       {},
	{Kind: "func", Name: "New"}:                        {},
	{Kind: "func", Name: "RawMutationOutcomeV1.Clone"}: {},
	{Kind: "type", Name: "Config"}:                     {},
	{Kind: "type", Name: "PairingPolicy"}:              {},
	{Kind: "type", Name: "RawFeatureRuntimeV1"}:        {},
	{Kind: "type", Name: "RawMutationOutcomeV1"}:       {},
	{Kind: "type", Name: "RawMutationRuntimeV1"}:       {},
	{Kind: "type", Name: "Remote"}:                     {},
	{Kind: "type", Name: "Runtime"}:                    {},
	{Kind: "var", Name: "ErrRuntimeDisabled"}:          {},
	{Kind: "var", Name: "ErrRuntimeShutdown"}:          {},
}

var outputPowerLimitRuntimeExports = map[manifestExport]struct{}{
	{Kind: "const", Name: "PowerLimitDirectionConsumption"}: {},
	{Kind: "const", Name: "PowerLimitDirectionProduction"}:  {},
	{Kind: "type", Name: "OutputPowerLimitFallback"}:        {},
	{Kind: "type", Name: "PowerLimitDirection"}:             {},
}
