package main

import (
	"embed"
	"fmt"
	"path"
	"strings"
)

const exportInventoryDirectory = "testdata/exports"

const exportInventoryRepositoryDirectory = "internal/apiboundary/" + exportInventoryDirectory

//go:embed testdata/exports/*.txt
var exportInventoryFiles embed.FS

var exportInventoryNames = []string{
	"evidence.txt",
	"lifecycle_native_runtime_second.txt",
	"lifecycle_operator_admin_first.txt",
	"lifecycle_runtime.txt",
	"native_runtime_second.txt",
	"operator_admin_runtime.txt",
	"participation.txt",
	"participation_operator.txt",
	"raw.txt",
	"raw_feature.txt",
	"raw_mutation.txt",
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
	data, err := exportInventoryFiles.ReadFile(path.Join(exportInventoryDirectory, name))
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
