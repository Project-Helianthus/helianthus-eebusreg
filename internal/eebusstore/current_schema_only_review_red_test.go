//go:build linux || darwin

package eebusstore

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestCurrentSchemaOnlyRejectsLegacyWithoutRewrite(t *testing.T) {
	for _, schemaVersion := range []uint64{1, 2} {
		t.Run(fmt.Sprintf("schema-%d", schemaVersion), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "store")
			first := []byte(fmt.Sprintf(
				`{"generation":{"parent_sequence":null,"parent_sha256":null,"sequence":1},"local_identity":null,"remote_identities":[],"schema_version":%d}`+"\n",
				schemaVersion,
			))
			if schemaVersion == 2 {
				first = []byte(`{"control":null,"generation":{"parent_sequence":null,"parent_sha256":null,"sequence":1},"local_identity":null,"remote_identities":[],"schema_version":2}` + "\n")
			}
			firstRef := testGenerationRef{sequence: 1, sha256: testDigestHex(first), schema: schemaVersion}
			installStoreLayout(
				t,
				root,
				map[uint64][]byte{1: first},
				testManifestSlotBytes(1, 1, testManifestPayloadBytes(firstRef, nil, 1)),
				nil,
			)
			before := testSnapshotTree(t, root)

			bridge, outcome := OpenAssociationBridge(root, nil)
			if bridge != nil {
				if err := bridge.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if outcome != string(outcomeUnsupportedLegacyVersion) {
				t.Fatalf("legacy open outcome = %q, want %q", outcome, outcomeUnsupportedLegacyVersion)
			}
			assertTreeEqual(t, testSnapshotTree(t, root), before)
		})
	}
}
