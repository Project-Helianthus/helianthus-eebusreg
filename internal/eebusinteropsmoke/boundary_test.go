package eebusinteropsmoke

import (
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

func TestFakePeerImportsNoHelianthusFacadeUnderTest(t *testing.T) {
	const fixture = "fake_peer_test.go"
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), fixture, data, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range file.Imports {
		importPath := strings.Trim(imp.Path.Value, `"`)
		if strings.Contains(importPath, "internal/eebusfacade") {
			t.Fatalf("%s imports facade under test: %s", fixture, importPath)
		}
	}
}
