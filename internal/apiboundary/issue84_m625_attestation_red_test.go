package main

import (
	"reflect"
	"sort"
	"testing"
)

func TestIssue84RawStableTypeAttestationIsMeaningful(t *testing.T) {
	if len(msp0625RawStableTypes) == 0 {
		t.Fatal("raw stable type attestation is empty")
	}
	if _, ok := msp0625RawStableTypes["OpaqueObservationV1"]; !ok {
		t.Fatal("raw stable type attestation omits OpaqueObservationV1")
	}

	var got []string
	for _, spec := range stableContractSpecs(canonicalModulePath) {
		if spec.importPath != canonicalModulePath+"/eebusraw" || spec.root != "ReadAuthorizationV1" {
			continue
		}
		for _, stableType := range spec.types {
			got = append(got, stableType.Name)
		}
	}
	sort.Strings(got)
	want := make([]string, 0, len(msp0625RawStableTypes))
	for name := range msp0625RawStableTypes {
		want = append(want, name)
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stable type attestation = %v, want frozen contract %v", want, got)
	}
}
