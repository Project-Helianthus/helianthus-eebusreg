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
		{canonicalModulePath, "ParticipationContributionV1"},
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
	const wantFingerprint = "b5cdec16a5d576213dda30a201ddc0b25df0cff8425cda57472936732e71b50d"
	if got != wantFingerprint {
		t.Fatalf("stable contract fingerprint = %s, want %s", got, wantFingerprint)
	}
}
