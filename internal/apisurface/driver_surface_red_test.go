package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeDriverSurfaceModule(t *testing.T, driverSource, legacyRawSource string) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"go.mod", "go.sum"} {
		data, err := os.ReadFile(filepath.Join(moduleRoot(t), name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"eebusruntime.go":         "package eebusruntime\n\nconst Version = \"synthetic\"\n",
		"eebusraw/doc.go":         "package eebusraw\n\n" + legacyRawSource,
		"eebusevidence/doc.go":    "package eebusevidence\n\nconst Version = \"synthetic\"\n",
		"driver/sample/sample.go": driverSource,
	}
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestDriverPackageSurfaceRecordsPinnedModelDependency(t *testing.T) {
	driverSource := "package sample\n\nimport model \"" + pinnedSpineModelPath + "\"\n\ntype Selector struct {\n\tDeviceAddress model.AddressDeviceType\n}\n"
	root := writeDriverSurfaceModule(t, driverSource, "const Version = \"synthetic\"\n")
	doc, err := extract(root)
	if err != nil {
		t.Fatal(err)
	}
	var found *surface
	for index := range doc.Packages {
		if doc.Packages[index].Path == modulePath+"/driver/sample" {
			found = &doc.Packages[index]
		}
	}
	if found == nil {
		t.Fatalf("driver/sample package was not inventoried: %v", packagePaths(doc))
	}
	dependency := false
	for _, imported := range found.Imports {
		if imported.Path == pinnedSpineModelPath && imported.Qualifier == "model" && imported.DependencyKind == "public_contract" {
			dependency = true
		}
	}
	if !dependency {
		t.Fatalf("driver/sample does not record the pinned model dependency: %+v", found.Imports)
	}
	for _, sym := range found.Symbols {
		if sym.Kind == "type" && sym.Name == "Selector" {
			if !strings.Contains(sym.Type, "model.AddressDeviceType") {
				t.Fatalf("Selector field type = %q, want model.AddressDeviceType", sym.Type)
			}
			return
		}
	}
	t.Fatal("driver/sample Selector is not inventoried")
}

func TestLegacyPublicPackageStillRejectsModelDependency(t *testing.T) {
	legacy := "import model \"" + pinnedSpineModelPath + "\"\n\ntype Selector struct {\n\tDeviceAddress model.AddressDeviceType\n}\n"
	root := writeDriverSurfaceModule(t, "package sample\n\nconst Version = \"synthetic\"\n", legacy)
	if _, err := extract(root); err == nil || !strings.Contains(err.Error(), "unapproved public dependency") {
		t.Fatalf("legacy public package accepted the model dependency: %v", err)
	}
}

func packagePaths(doc document) []string {
	var paths []string
	for _, pkg := range doc.Packages {
		paths = append(paths, pkg.Path)
	}
	return paths
}
