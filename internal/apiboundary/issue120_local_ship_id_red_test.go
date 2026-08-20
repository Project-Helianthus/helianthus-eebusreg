package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestIssue120BoundaryAllowsOnlyExactLocalSHIPIDField(t *testing.T) {
	tests := []struct {
		name          string
		field         string
		wantViolation bool
	}{
		{name: "local operator identifier", field: "LocalSHIPID"},
		{name: "broader secret surface remains denied", field: "LocalSHIPSecret", wantViolation: true},
		{name: "broader state surface remains denied", field: "LocalSHIPState", wantViolation: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(
				fset,
				"issue120.go",
				"package issue120\n\ntype Snapshot struct { "+test.field+" string }\n",
				0,
			)
			if err != nil {
				t.Fatal(err)
			}
			var field *ast.Ident
			ast.Inspect(file, func(node ast.Node) bool {
				candidate, ok := node.(*ast.Field)
				if ok && len(candidate.Names) == 1 && candidate.Names[0].Name == test.field {
					field = candidate.Names[0]
					return false
				}
				return true
			})
			if field == nil {
				t.Fatal("synthetic field was not found")
			}
			violations := []string{}
			checkExportedName(fset, "issue120.go", field, &violations)
			if got := len(violations) != 0; got != test.wantViolation {
				t.Fatalf("field %s violation=%t diagnostics=%v, want violation=%t", test.field, got, violations, test.wantViolation)
			}
			if test.wantViolation && !strings.Contains(strings.Join(violations, "\n"), "forbidden boundary term SHIP") {
				t.Fatalf("field %s lost exact SHIP diagnostic: %v", test.field, violations)
			}
		})
	}
}
