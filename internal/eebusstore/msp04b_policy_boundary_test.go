package eebusstore

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestMSP04BStoreRemainsMechanicalAndPolicyFree(t *testing.T) {
	forbiddenIdentifiers := map[string]struct{}{
		"adminsocket":             {},
		"candidatenonce":          {},
		"confirmtrust":            {},
		"fingerprintv1":           {},
		"idempotencykey":          {},
		"pairingcandidate":        {},
		"pairingwindow":           {},
		"startingstoregeneration": {},
	}
	forbiddenImports := map[string]struct{}{
		"net":      {},
		"net/http": {},
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	files := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(files, entry.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imported := range parsed.Imports {
			path, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if _, forbidden := forbiddenImports[path]; forbidden {
				t.Fatalf("policy-free store imports transport package %q", path)
			}
		}

		parsed, err = parser.ParseFile(files, entry.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			identifier, ok := node.(*ast.Ident)
			if !ok {
				return true
			}
			if _, forbidden := forbiddenIdentifiers[strings.ToLower(identifier.Name)]; forbidden {
				t.Errorf("store owns coordinator policy identifier %q", identifier.Name)
			}
			return true
		})
	}
}
