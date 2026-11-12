package main

import (
	"flag"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// The frozen public export inventories live in testdata/exports as one
// "kind name" line per export, sorted and unique. To add an export, edit the
// golden file that owns it. To regenerate the goldens after exports were
// removed or a file was reordered, run:
//
//	GOWORK=off go test ./internal/apiboundary -run TestExportInventoryDrift -update-export-inventories
//
// Regeneration rewrites every golden in canonical order and drops entries the
// gate no longer observes in the owning public package. It never assigns a
// newly observed export to an inventory; that stays a reviewed edit.
var updateExportInventories = flag.Bool(
	"update-export-inventories",
	false,
	"rewrite testdata/exports goldens from the exports the API boundary gate observes",
)

var exportInventoryPackages = map[string]string{
	"evidence.txt":                        "eebusevidence",
	"lifecycle_native_runtime_second.txt": ".",
	"lifecycle_operator_admin_first.txt":  ".",
	"lifecycle_runtime.txt":               ".",
	"native_runtime_second.txt":           ".",
	"operator_admin_runtime.txt":          ".",
	"participation.txt":                   ".",
	"participation_operator.txt":          ".",
	"raw.txt":                             "eebusraw",
	"raw_feature.txt":                     "eebusraw",
	"raw_mutation.txt":                    "eebusraw",
}

func TestExportInventoryDrift(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	observed := make(map[string]map[manifestExport]struct{})
	for directory := range allowedPublicPackages {
		observed[directory] = observePublicPackageExports(t, root, directory)
	}

	covered := make(map[string]map[manifestExport]struct{})
	for _, name := range exportInventoryNames {
		directory := exportInventoryPackages[name]
		inventory, err := readExportInventory(name)
		if err != nil {
			t.Fatal(err)
		}
		if *updateExportInventories {
			inventory = retainObservedExports(inventory, observed[directory])
			writeExportInventory(t, name, inventory)
		}
		if stale := exportDifference(inventory, observed[directory]); len(stale) > 0 {
			t.Errorf("%s lists exports the gate does not observe in %s: %s", name, directory, strings.Join(stale, ", "))
		}
		if covered[directory] == nil {
			covered[directory] = make(map[manifestExport]struct{})
		}
		maps.Copy(covered[directory], inventory)
	}

	maps.Copy(covered["."], allowedRuntimeExports)
	for directory, exports := range observed {
		if unlisted := exportDifference(exports, covered[directory]); len(unlisted) > 0 {
			t.Errorf("%s exports are missing from every frozen inventory: %s", directory, strings.Join(unlisted, ", "))
		}
	}
}

func TestExportInventoryGoldensAreCanonical(t *testing.T) {
	for _, name := range exportInventoryNames {
		data, err := exportInventoryFiles.ReadFile(path.Join(exportInventoryDirectory, name))
		if err != nil {
			t.Fatal(err)
		}
		inventory, err := parseExportInventory(name, string(data))
		if err != nil {
			t.Fatal(err)
		}
		if got, want := string(data), renderExportInventory(inventory); got != want {
			t.Errorf("%s is not in canonical sorted unique form; regenerate it", name)
		}
		for exported := range inventory {
			if !slices.Contains([]string{"const", "func", "type", "var"}, exported.Kind) {
				t.Errorf("%s has unknown export kind %q for %s", name, exported.Kind, exported.Name)
			}
		}
	}
}

func TestExportInventoryGoldensMatchGateTables(t *testing.T) {
	tables := map[string]map[manifestExport]struct{}{
		"evidence.txt":               allowedMSP035EvidenceExports,
		"native_runtime_second.txt":  issue129NativeRuntimeV2Exports,
		"operator_admin_runtime.txt": postM9OperatorAdminRuntimeExports,
		"participation.txt":          issue137ParticipationExports,
		"participation_operator.txt": issue137ParticipationOperatorExports,
		"raw.txt":                    allowedMSP035RawExports,
		"raw_feature.txt":            allowedMSP0625RawExports,
		"raw_mutation.txt":           allowedMSP0625MutationRawExports,
	}
	for name, table := range tables {
		inventory, err := readExportInventory(name)
		if err != nil {
			t.Fatal(err)
		}
		if len(inventory) == 0 || !maps.Equal(table, inventory) {
			t.Errorf("gate table loaded from %s differs from the golden file", name)
		}
	}
	for exported := range mergeExportInventories(allowedMSP035RawExports, allowedMSP0625RawExports, allowedMSP0625MutationRawExports) {
		if _, ok := allowedCurrentRawExports[exported]; !ok {
			t.Errorf("current raw allowlist is missing %s %s", exported.Kind, exported.Name)
		}
	}
}

func TestExportInventoryFilesAreExactlyTheEmbeddedSet(t *testing.T) {
	entries, err := os.ReadDir(exportInventoryDirectory)
	if err != nil {
		t.Fatal(err)
	}
	var onDisk []string
	for _, entry := range entries {
		onDisk = append(onDisk, entry.Name())
	}
	embedded, err := exportInventoryFiles.ReadDir(exportInventoryDirectory)
	if err != nil {
		t.Fatal(err)
	}
	var embeddedNames []string
	for _, entry := range embedded {
		embeddedNames = append(embeddedNames, entry.Name())
	}
	want := make([]string, 0, len(exportInventoryPackages))
	for name := range exportInventoryPackages {
		want = append(want, name)
	}
	sort.Strings(want)
	if !slices.Equal(onDisk, want) || !slices.Equal(embeddedNames, want) || !slices.Equal(exportInventoryNames, want) {
		t.Fatalf("export inventory files drifted: disk=%v embedded=%v names=%v want=%v", onDisk, embeddedNames, exportInventoryNames, want)
	}
}

func TestExportInventoryLoaderFailsClosed(t *testing.T) {
	rejected := map[string]string{
		"empty":             "",
		"whitespace only":   " \n\t\n",
		"single field":      "type Envelope\nfunc\n",
		"three fields":      "type Envelope extra\n",
		"blank inner line":  "type Envelope\n\nfunc New\n",
		"comment-like line": "# type Envelope\n",
	}
	for name, entries := range rejected {
		t.Run(name, func(t *testing.T) {
			if inventory, err := parseExportInventory("synthetic.txt", entries); err == nil {
				t.Fatalf("malformed inventory was accepted: %v", inventory)
			}
		})
	}

	inventory, err := parseExportInventory("synthetic.txt", "  type Envelope \nfunc\tNew\nfunc New\n")
	if err != nil {
		t.Fatal(err)
	}
	want := map[manifestExport]struct{}{
		{Kind: "type", Name: "Envelope"}: {},
		{Kind: "func", Name: "New"}:      {},
	}
	if !maps.Equal(inventory, want) {
		t.Fatalf("parsed inventory = %v, want %v", inventory, want)
	}

	if _, err := readExportInventory("unknown.txt"); err == nil {
		t.Fatal("unknown inventory name was accepted")
	}
	if _, err := readExportInventory("../exports/raw.txt"); err == nil {
		t.Fatal("inventory name with a path was accepted")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("loading an unknown inventory did not panic")
		}
	}()
	loadExportInventory("unknown.txt")
}

func TestExportInventoryPathsAreTheOnlyDocumentationExemption(t *testing.T) {
	for _, name := range exportInventoryNames {
		repositoryPath := exportInventoryRepositoryDirectory + "/" + name
		if !isDocumentationPath(repositoryPath) || !isExportInventoryPath(repositoryPath) {
			t.Errorf("golden %s is not an exact documentation-rule exemption", repositoryPath)
		}
	}
	for _, repositoryPath := range []string{
		"AUTHORITY.txt",
		"raw.txt",
		"testdata/exports/raw.txt",
		"internal/testdata/exports/raw.txt",
		"internal/apiboundary/testdata/raw.txt",
		"internal/apiboundary/testdata/exports/notes.txt",
		"internal/apiboundary/testdata/exports/RAW.TXT",
		"internal/apiboundary/testdata/exports/nested/raw.txt",
		"internal/apiboundary/testdata/exports/raw.txt/README.md",
		"internal/apiboundary/testdata/exports/raw.md",
		"eebusraw/testdata/exports/raw.txt",
	} {
		if isExportInventoryPath(repositoryPath) {
			t.Errorf("path outside the golden set was exempted: %s", repositoryPath)
		}
	}
}

func observePublicPackageExports(t *testing.T, root, directory string) map[manifestExport]struct{} {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(directory)))
	if err != nil {
		t.Fatal(err)
	}
	exports := make(map[manifestExport]struct{})
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(root, filepath.FromSlash(directory), name), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		if file.Name.Name != allowedPublicPackages[directory] {
			continue
		}
		collectExports(file, exports)
	}
	if len(exports) == 0 {
		t.Fatalf("the gate observes no exports in public package %s", directory)
	}
	return exports
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

func retainObservedExports(inventory, observed map[manifestExport]struct{}) map[manifestExport]struct{} {
	retained := make(map[manifestExport]struct{}, len(inventory))
	for exported := range inventory {
		if _, ok := observed[exported]; ok {
			retained[exported] = struct{}{}
		}
	}
	return retained
}

func renderExportInventory(inventory map[manifestExport]struct{}) string {
	lines := make([]string, 0, len(inventory))
	for exported := range inventory {
		lines = append(lines, exported.Kind+" "+exported.Name)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n") + "\n"
}

func writeExportInventory(t *testing.T, name string, inventory map[manifestExport]struct{}) {
	t.Helper()
	if len(inventory) == 0 {
		t.Fatalf("regeneration would leave %s empty; remove the inventory deliberately instead", name)
	}
	target := filepath.Join(exportInventoryDirectory, name)
	if err := os.WriteFile(target, []byte(renderExportInventory(inventory)), 0o644); err != nil {
		t.Fatal(err)
	}
}
