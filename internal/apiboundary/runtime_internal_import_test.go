package main_test

import "testing"

func TestMSP055RootImplementationMayUseOnlyInternalFacade(t *testing.T) {
	tool := buildAPIBoundary(t)

	t.Run("unexported facade implementation use is allowed", func(t *testing.T) {
		root := newMSP055InternalImportFixture(t, "eebusfacade")
		writeFile(t, root, "runtime.go", `package eebusruntime

import "example.test/registry/internal/eebusfacade"

func runtimeBackend() eebusfacade.Backend { return eebusfacade.Backend{} }
`)
		runGit(t, root, nil, "add", "--", ".")
		if output, err := runTool(t, tool, root); err != nil {
			t.Fatalf("API boundary rejected internal facade implementation use: %v\n%s", err, output)
		}
	})

	t.Run("different internal package remains forbidden", func(t *testing.T) {
		root := newMSP055InternalImportFixture(t, "eebusstore")
		writeFile(t, root, "runtime.go", `package eebusruntime

import _ "example.test/registry/internal/eebusstore"
`)
		runGit(t, root, nil, "add", "--", ".")
		expectRejected(t, tool, root, "internal implementation")
	})

	t.Run("facade implementation type cannot escape", func(t *testing.T) {
		root := newMSP055InternalImportFixture(t, "eebusfacade")
		writeFile(t, root, "runtime.go", `package eebusruntime

import "example.test/registry/internal/eebusfacade"

type Backend = eebusfacade.Backend
`)
		runGit(t, root, nil, "add", "--", ".")
		expectRejected(t, tool, root, "internal implementation")
	})
}

func newMSP055InternalImportFixture(t *testing.T, packageName string) string {
	t.Helper()
	root := newSyntheticRepository(t)
	writeFile(t, root, "doc.go", "// Package eebusruntime exposes raw runtime contracts.\npackage eebusruntime\n")
	writeFile(
		t,
		root,
		"internal/"+packageName+"/backend.go",
		"// Package "+packageName+" is an internal fixture.\npackage "+packageName+"\n\ntype Backend struct{}\n",
	)
	return root
}
