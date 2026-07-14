package eebusstore

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManifestSelectionValidatesChecksumAndUsesHighestEpoch(t *testing.T) {
	slotA := readFixture(t, "manifest-a-v1.json")
	slotB := readFixture(t, "manifest-b-v1.json")

	selected, err := selectManifestSlots(slotA, slotB)
	if err != nil {
		t.Fatal(err)
	}
	if selected.slot != manifestSlotB || selected.epoch != 2 {
		t.Fatalf("selection = slot %q epoch %d, want B/2", selected.slot, selected.epoch)
	}
	if !bytes.Equal(selected.payload, readFixture(t, "manifest-v1-g2.json")) {
		t.Fatal("selected payload does not match the higher-epoch slot")
	}

	badB := bytes.Replace(slotB, []byte(childManifestSHA256), []byte("08eea78843a229f73def83719687b7bab328a265ee4357dbdb3bef72e3b8e2cb"), 1)
	selected, err = selectManifestSlots(slotA, badB)
	if err != nil {
		t.Fatal(err)
	}
	if selected.slot != manifestSlotA || selected.epoch != 1 {
		t.Fatalf("selection with corrupt B = slot %q epoch %d, want A/1", selected.slot, selected.epoch)
	}

	badA := bytes.Replace(slotA, []byte(emptyManifestSHA256), []byte("0733652f5a868d40212573fef13bad5b471010c51d040cf6ee27e42f34228880"), 1)
	_, err = selectManifestSlots(badA, badB)
	assertErrorOutcome(t, err, outcomeNoValidManifest)
}

func TestManifestEqualEpochTieIsDeterministicOrAmbiguous(t *testing.T) {
	slotA := readFixture(t, "manifest-a-v1.json")
	selected, err := selectManifestSlots(slotA, bytes.Clone(slotA))
	if err != nil {
		t.Fatal(err)
	}
	if selected.slot != manifestSlotA || selected.epoch != 1 {
		t.Fatalf("identical tie selected slot %q epoch %d, want A/1", selected.slot, selected.epoch)
	}

	differentPayload := readFixture(t, "manifest-v1-g2.json")
	differentAtSameEpoch := testManifestSlotBytes(1, 1, differentPayload)
	_, err = selectManifestSlots(slotA, differentAtSameEpoch)
	assertErrorOutcome(t, err, outcomeManifestAmbiguous)
}

func TestOpenRequiresCurrentAndExactParentBinding(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	installTwoGenerationStore(t, root, readFixture(t, "generation-v1-child-empty.json"))

	result := openForTest(t, root, nil, nil)
	defer closeStore(t, result)
	assertOutcome(t, result.outcome, outcomeOpenedCurrent)
	if result.store == nil || result.state == nil || result.recovery != nil {
		t.Fatalf("opened current returned store/state/recovery = %t/%t/%t", result.store != nil, result.state != nil, result.recovery != nil)
	}
}

func TestCurrentMetadataMustBindTheManifestExactParent(t *testing.T) {
	child := readFixture(t, "generation-v1-child-empty.json")
	tampered := bytes.Replace(child, []byte(emptyGenerationSHA256), []byte(strings.Repeat("b", 64)), 1)
	root := filepath.Join(t.TempDir(), "store")
	installTwoGenerationStore(t, root, tampered)
	before := testSnapshotTree(t, root)

	result := openForTest(t, root, nil, nil)
	assertOutcome(t, result.outcome, outcomeRecoveryCandidateAvailable)
	if result.store != nil || result.state != nil || result.recovery == nil || result.recovery.sequence != 1 {
		t.Fatal("parent-binding mismatch did not return only the exact inactive parent")
	}
	assertTreeEqual(t, testSnapshotTree(t, root), before)
}

func TestCorruptCurrentReturnsOnlyInactiveExactParentCandidate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	installTwoGenerationStore(t, root, readFixture(t, "generation-v1-child-empty.json"))
	currentPath := filepath.Join(root, "generations", testGenerationFilename(2))
	testWritePrivateFile(t, currentPath, []byte("{\"corrupt\":true}\n"))
	before := testSnapshotTree(t, root)

	result := openForTest(t, root, nil, nil)
	assertOutcome(t, result.outcome, outcomeRecoveryCandidateAvailable)
	if result.store != nil || result.state != nil {
		t.Fatal("corrupt current returned an active store or runtime state")
	}
	if result.recovery == nil || result.recovery.sequence != 1 {
		t.Fatalf("recovery metadata = %+v, want exact parent sequence 1", result.recovery)
	}
	assertTreeEqual(t, testSnapshotTree(t, root), before)

	if payload, err := os.ReadFile(currentPath); err != nil {
		t.Fatal(err)
	} else if !bytes.Equal(payload, []byte("{\"corrupt\":true}\n")) {
		t.Fatal("open quarantined, rewrote, or deleted corrupt current bytes")
	}
}

func TestInvalidExactParentDoesNotFallBackToLowerSlotOrScanOrphans(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	installTwoGenerationStore(t, root, readFixture(t, "generation-v1-child-empty.json"))
	testWritePrivateFile(t, filepath.Join(root, "generations", testGenerationFilename(2)), []byte("{\"corrupt\":true}\n"))
	testWritePrivateFile(t, filepath.Join(root, "generations", testGenerationFilename(1)), []byte("{\"also_corrupt\":true}\n"))
	testWritePrivateFile(t, filepath.Join(root, "generations", testGenerationFilename(77)), readFixture(t, "generation-v1-empty.json"))
	before := testSnapshotTree(t, root)

	result := openForTest(t, root, nil, nil)
	assertOutcome(t, result.outcome, outcomeNoValidCurrent)
	if result.store != nil || result.state != nil || result.recovery != nil {
		t.Fatal("invalid parent produced active state or an unbound recovery candidate")
	}
	assertTreeEqual(t, testSnapshotTree(t, root), before)
}

func TestFutureManifestVersionIsTerminalAfterEpochSelection(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	first := readFixture(t, "generation-v1-empty.json")
	firstRef := testGenerationRef{sequence: 1, sha256: testDigestHex(first), schema: 1}
	futureRef := testGenerationRef{sequence: 99, sha256: strings.Repeat("a", 64), schema: 1}
	installStoreLayout(
		t,
		root,
		map[uint64][]byte{1: first},
		testManifestSlotBytes(1, 1, testManifestPayloadBytes(firstRef, nil, 1)),
		testManifestSlotBytes(2, 1, testManifestPayloadBytes(futureRef, &firstRef, 2)),
	)
	before := testSnapshotTree(t, root)

	result := openForTest(t, root, nil, nil)
	assertOutcome(t, result.outcome, outcomeUnsupportedFutureVersion)
	if result.store != nil || result.state != nil || result.recovery != nil {
		t.Fatal("future manifest inspected or activated older content")
	}
	assertTreeEqual(t, testSnapshotTree(t, root), before)
}

func TestFutureSlotEnvelopeIsTerminalAndCannotBeBypassed(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	first := readFixture(t, "generation-v1-empty.json")
	firstRef := testGenerationRef{sequence: 1, sha256: testDigestHex(first), schema: 1}
	payload := testManifestPayloadBytes(firstRef, nil, 1)
	installStoreLayout(
		t,
		root,
		map[uint64][]byte{1: first},
		testManifestSlotBytes(1, 1, payload),
		testManifestSlotBytes(2, 2, payload),
	)

	result := openForTest(t, root, nil, nil)
	assertOutcome(t, result.outcome, outcomeUnsupportedFutureVersion)
	if result.store != nil || result.state != nil || result.recovery != nil {
		t.Fatal("future slot envelope fell back to the older v1 slot")
	}
}

func TestSelectedGenerationVersionClassifiesLegacyAndFutureWithoutFallback(t *testing.T) {
	for name, schema := range map[string]struct {
		version uint64
		want    outcome
	}{
		"legacy": {version: 0, want: outcomeUnsupportedLegacyVersion},
		"future": {version: 2, want: outcomeUnsupportedFutureVersion},
	} {
		t.Run(name, func(t *testing.T) {
			first := readFixture(t, "generation-v1-empty.json")
			second := bytes.Replace(
				readFixture(t, "generation-v1-child-empty.json"),
				[]byte(`"schema_version":1}`),
				[]byte(fmt.Sprintf(`"schema_version":%d}`, schema.version)),
				1,
			)
			firstRef := testGenerationRef{sequence: 1, sha256: testDigestHex(first), schema: 1}
			secondRef := testGenerationRef{sequence: 2, sha256: testDigestHex(second), schema: schema.version}
			root := filepath.Join(t.TempDir(), "store")
			installStoreLayout(
				t,
				root,
				map[uint64][]byte{1: first, 2: second},
				testManifestSlotBytes(1, 1, testManifestPayloadBytes(firstRef, nil, 1)),
				testManifestSlotBytes(2, 1, testManifestPayloadBytes(secondRef, &firstRef, 1)),
			)

			result := openForTest(t, root, nil, nil)
			assertOutcome(t, result.outcome, schema.want)
			if result.store != nil || result.state != nil || result.recovery != nil {
				t.Fatal("unsupported selected generation fell back to older state")
			}
		})
	}
}
