package main

import "testing"

func TestIssue93LabProfileIsInClosedPublicAndStableInventories(t *testing.T) {
	required := []manifestExport{
		{Kind: "type", Name: "MutationLabProfileV1"},
		{Kind: "const", Name: "MutationLabProfileContractV1"},
		{Kind: "func", Name: "MutationLabProfileV1.Clone"},
		{Kind: "func", Name: "MutationLabProfileV1.Format"},
		{Kind: "func", Name: "MutationLabProfileV1.GoString"},
		{Kind: "func", Name: "MutationLabProfileV1.String"},
		{Kind: "func", Name: "ValidateMutationLabProfileV1"},
	}
	for _, export := range required {
		if _, ok := allowedCurrentRawExports[export]; !ok {
			t.Errorf("closed eebusraw inventory is missing %s %s", export.Kind, export.Name)
		}
	}

	var found bool
	for _, spec := range stableContractSpecs(canonicalModulePath) {
		if spec.importPath != canonicalModulePath+"/eebusraw" ||
			spec.root != "ReadAuthorizationV1" {
			continue
		}
		for _, stableType := range spec.types {
			if stableType.Name == "MutationLabProfileV1" {
				found = true
				if stableType.Underlying != "struct" || len(stableType.Fields) != 9 {
					t.Fatalf("MutationLabProfileV1 stable shape = %#v", stableType)
				}
			}
		}
	}
	if !found {
		t.Fatal("stable eebusraw contract omitted MutationLabProfileV1")
	}
}
