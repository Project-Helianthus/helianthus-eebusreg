package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestIssue137ParticipationManifestRejectsFieldAndEnumDrift(t *testing.T) {
	var spec stableContractSpec
	for _, candidate := range stableContractSpecs(canonicalModulePath) {
		if candidate.root == "ParticipationContributionV1" {
			spec = candidate
		}
	}
	if spec.root == "" {
		t.Fatal("participation data manifest missing")
	}
	var loss manifestStableType
	for _, typ := range spec.types {
		if typ.Name == "ParticipationLossV1" {
			loss = typ
		}
	}
	for _, test := range []struct {
		name, body string
		valid      bool
	}{
		{"exact", "NativePath string `json:\"native_path\"`; Code ParticipationLossCodeV1 `json:\"code\"`; ControlSafe bool `json:\"control_safe\"`", true},
		{"missing control safety", "NativePath string `json:\"native_path\"`; Code ParticipationLossCodeV1 `json:\"code\"`", false},
		{"changed wire tag", "NativePath string `json:\"native_path\"`; Code ParticipationLossCodeV1 `json:\"code\"`; ControlSafe bool `json:\"safe\"`", false},
		{"secret carrier", "NativePath string `json:\"native_path\"`; Code ParticipationLossCodeV1 `json:\"code\"`; ControlSafe bool `json:\"control_safe\"`; PIN []byte", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", "package fixture\ntype ParticipationLossV1 struct {"+test.body+"}", 0)
			if err != nil {
				t.Fatal(err)
			}
			declaration := file.Decls[0].(*ast.GenDecl).Specs[0].(*ast.TypeSpec)
			got, err := inspectStableType(typeDeclaration{spec: declaration})
			if err != nil {
				t.Fatal(err)
			}
			if stableContractTypeMatches(got, loss) != test.valid {
				t.Fatal("exact data shape boundary drifted")
			}
		})
	}
	for _, enum := range spec.enums {
		if !enum.exact {
			t.Fatalf("participation enum %s is not closed", enum.typeName)
		}
	}
}
