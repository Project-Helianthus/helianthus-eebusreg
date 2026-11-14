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
		90, 66, 30, 146, 107, 164, 21, 27,
		70, 30, 180, 193, 136, 178, 135, 190,
		19, 6, 32, 162, 90, 225, 123, 178,
		10, 171, 101, 198, 207, 172, 130, 199,
	}
	if got != wantFingerprint {
		t.Fatalf("stable contract fingerprint = %x, want %x", got, wantFingerprint)
	}
}
