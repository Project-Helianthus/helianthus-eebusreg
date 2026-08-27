package main

import (
	"crypto/sha256"
	"fmt"
	"reflect"
	"testing"
)

func TestStableContractSpecsInventoryOrderAndCopies(t *testing.T) {
	first := stableContractSpecs(canonicalModulePath)
	want := []struct {
		importPath string
		root       string
	}{
		{canonicalModulePath, "SnapshotV1"},
		{canonicalModulePath, "NativeSnapshotV2"},
		{canonicalModulePath + "/eebusraw", "IdentityDocumentV1"},
		{canonicalModulePath + "/eebusraw", "ReadAuthorizationV1"},
		{canonicalModulePath + "/eebusevidence", "EnvelopeV1"},
	}
	if len(first) != len(want) {
		t.Fatalf("stable contract count = %d, want %d", len(first), len(want))
	}
	for index, expected := range want {
		if first[index].importPath != expected.importPath || first[index].root != expected.root {
			t.Fatalf("stable contract[%d] = (%q, %q), want (%q, %q)", index, first[index].importPath, first[index].root, expected.importPath, expected.root)
		}
	}

	second := stableContractSpecs(canonicalModulePath)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("stable contract specifications are not deterministic")
	}
	first[0].root = "mutated"
	if second[0].root != "SnapshotV1" {
		t.Fatal("stable contract specifications share mutable state across calls")
	}
	got := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%#v", second))))
	const wantFingerprint = "9d56379f5f4fffb4ffb87b186b52953e1205f3a7b7928c196bc4d806cb9ff470"
	if got != wantFingerprint {
		t.Fatalf("stable contract fingerprint = %s, want %s", got, wantFingerprint)
	}
}
