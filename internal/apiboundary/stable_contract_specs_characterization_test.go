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
	got := sha256.Sum256([]byte(fmt.Sprintf("%#v", second)))
	// Pin the complete inventory, including the supported use_cases JSON tags.
	wantFingerprint := [sha256.Size]byte{
		72, 125, 104, 185, 35, 76, 91, 226,
		101, 160, 223, 57, 245, 59, 245, 206,
		53, 103, 45, 252, 53, 58, 193, 170,
		27, 145, 40, 162, 146, 62, 94, 88,
	}
	if got != wantFingerprint {
		t.Fatalf("stable contract fingerprint = %x, want %x", got, wantFingerprint)
	}
}
