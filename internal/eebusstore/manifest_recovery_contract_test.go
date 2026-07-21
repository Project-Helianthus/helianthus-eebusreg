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

func TestCurrentAcceptsMonotonicNoncontiguousExactParent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	first := readFixture(t, "generation-v1-empty.json")
	parentSequence := uint64(1)
	parentSHA256 := testDigestHex(first)
	third, err := encodeGenerationV1(generationV1{
		metadata: generationMetadata{
			sequence:       3,
			parentSequence: &parentSequence,
			parentSHA256:   &parentSHA256,
		},
		state:         emptyLogicalState(t),
		schemaVersion: currentSchemaVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	firstRef := testGenerationRef{sequence: 1, sha256: parentSHA256, schema: currentSchemaVersion}
	thirdRef := testGenerationRef{sequence: 3, sha256: testDigestHex(third), schema: currentSchemaVersion}
	installStoreLayout(
		t,
		root,
		map[uint64][]byte{1: first, 3: third},
		testManifestSlotBytes(1, 1, testManifestPayloadBytes(thirdRef, &firstRef, 1)),
		nil,
	)

	result := openForTest(t, root, nil, nil)
	defer closeStore(t, result)
	assertOutcome(t, result.outcome, outcomeOpenedCurrent)
}

func TestRecoveryCandidateRejectsParentAtOrAfterCurrent(t *testing.T) {
	for _, parentSequence := range []uint64{2, 3} {
		t.Run(fmt.Sprintf("parent_%d", parentSequence), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "store")
			parentGeneration := bytes.Replace(
				readFixture(t, "generation-v1-empty.json"),
				[]byte(`"sequence":1`),
				[]byte(fmt.Sprintf(`"sequence":%d`, parentSequence)),
				1,
			)
			missingCurrentRef := testGenerationRef{sequence: 2, sha256: strings.Repeat("a", 64), schema: currentSchemaVersion}
			parentRef := testGenerationRef{sequence: parentSequence, sha256: testDigestHex(parentGeneration), schema: currentSchemaVersion}
			installStoreLayout(
				t,
				root,
				map[uint64][]byte{parentSequence: parentGeneration},
				testManifestSlotBytes(1, 1, testManifestPayloadBytes(missingCurrentRef, &parentRef, 1)),
				nil,
			)

			result := openForTest(t, root, nil, nil)
			assertOutcome(t, result.outcome, outcomeNoValidCurrent)
			if result.store != nil || result.state != nil || result.recovery != nil {
				t.Fatal("non-chronological parent produced active state or recovery evidence")
			}
		})
	}
}

func TestOversizedExactParentIsInvalidRecoveryEvidence(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	parent := bytes.Repeat([]byte{'x'}, maxGenerationBytes+1)
	currentRef := testGenerationRef{sequence: 2, sha256: strings.Repeat("a", 64), schema: currentSchemaVersion}
	parentRef := testGenerationRef{sequence: 1, sha256: testDigestHex(parent), schema: currentSchemaVersion}
	installStoreLayout(
		t,
		root,
		map[uint64][]byte{1: parent},
		testManifestSlotBytes(1, 1, testManifestPayloadBytes(currentRef, &parentRef, 1)),
		nil,
	)

	result := openForTest(t, root, nil, nil)
	assertOutcome(t, result.outcome, outcomeNoValidCurrent)
	if result.store != nil || result.state != nil || result.recovery != nil {
		t.Fatal("oversized exact parent produced active state or recovery evidence")
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
	firstRef := testGenerationRef{sequence: 1, sha256: testDigestHex(first), schema: currentSchemaVersion}
	futureRef := testGenerationRef{sequence: 99, sha256: strings.Repeat("a", 64), schema: currentSchemaVersion}
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

func TestSelectedFutureManifestVersionPrecedesV1Validation(t *testing.T) {
	first := readFixture(t, "generation-v1-empty.json")
	firstRef := testGenerationRef{sequence: 1, sha256: testDigestHex(first), schema: currentSchemaVersion}
	malformedRef := testGenerationRef{sequence: 0, sha256: "not-a-sha256-digest", schema: currentSchemaVersion}
	tests := []struct {
		name    string
		payload []byte
	}{
		{
			name:    "malformed v1 references",
			payload: testManifestPayloadBytes(malformedRef, &malformedRef, 2),
		},
		{
			name: "unknown canonical v2 field",
			payload: []byte(fmt.Sprintf(
				"{\"current\":%s,\"manifest_version\":2,\"parent\":null,\"v2_extension\":true}\n",
				testGenerationRefJSON(firstRef),
			)),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateCanonicalJSON(test.payload, maxManifestPayloadBytes, maxJSONDepth); err != nil {
				t.Fatalf("future manifest test payload is not canonical: %v", err)
			}
			root := filepath.Join(t.TempDir(), "store")
			installStoreLayout(
				t,
				root,
				map[uint64][]byte{1: first},
				testManifestSlotBytes(1, 1, testManifestPayloadBytes(firstRef, nil, 1)),
				testManifestSlotBytes(2, 1, test.payload),
			)

			result := openForTest(t, root, nil, nil)
			assertOutcome(t, result.outcome, outcomeUnsupportedFutureVersion)
			if result.store != nil || result.state != nil || result.recovery != nil {
				t.Fatal("future manifest v1 validation returned active state")
			}
		})
	}
}

func TestFutureSlotEnvelopeIsTerminalAndCannotBeBypassed(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	first := readFixture(t, "generation-v1-empty.json")
	firstRef := testGenerationRef{sequence: 1, sha256: testDigestHex(first), schema: currentSchemaVersion}
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
		"legacy": {version: 2, want: outcomeUnsupportedLegacyVersion},
		"future": {version: 4, want: outcomeUnsupportedFutureVersion},
	} {
		t.Run(name, func(t *testing.T) {
			first := readFixture(t, "generation-v1-empty.json")
			second := bytes.Replace(
				readFixture(t, "generation-v1-child-empty.json"),
				[]byte(`"schema_version":3}`),
				[]byte(fmt.Sprintf(`"schema_version":%d}`, schema.version)),
				1,
			)
			firstRef := testGenerationRef{sequence: 1, sha256: testDigestHex(first), schema: currentSchemaVersion}
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

func TestOpenAccepts128ArtifactsIndependentOfReferencedGenerations(t *testing.T) {
	tests := []struct {
		name       string
		referenced int
	}{
		{name: "two referenced generations", referenced: 2},
		{name: "three referenced generations", referenced: 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "store")
			if test.referenced == 2 {
				installTwoGenerationStore(t, root, readFixture(t, "generation-v1-child-empty.json"))
			} else {
				installThreeGenerationStore(t, root)
			}
			addUnreferencedGenerations(t, root, uint64(test.referenced+1), 64)
			addTemporaryGenerationArtifacts(t, root, 64)

			result := openForTest(t, root, nil, nil)
			defer closeStore(t, result)
			assertOutcome(t, result.outcome, outcomeOpenedCurrent)
		})
	}
}

func TestOpenRejects129UnreferencedGenerationsAtArtifactBound(t *testing.T) {
	first := readFixture(t, "generation-v1-empty.json")
	firstRef := testGenerationRef{sequence: 1, sha256: testDigestHex(first), schema: currentSchemaVersion}
	root := filepath.Join(t.TempDir(), "store")
	installStoreLayout(
		t,
		root,
		map[uint64][]byte{1: first},
		testManifestSlotBytes(1, 1, testManifestPayloadBytes(firstRef, nil, 1)),
		nil,
	)
	addUnreferencedGenerations(t, root, 2, 129)

	result := openForTest(t, root, nil, nil)
	defer closeStore(t, result)
	assertOutcome(t, result.outcome, outcomeLayoutRejected)
	if result.store != nil || result.state != nil || result.recovery != nil {
		t.Fatal("129 unreferenced generations returned active state")
	}
}

func TestFutureSelectedSlotArtifactLimitPrecedesVersionClassification(t *testing.T) {
	for _, test := range []struct {
		name      string
		artifacts int
		want      outcome
	}{
		{name: "at limit", artifacts: 128, want: outcomeUnsupportedFutureVersion},
		{name: "over limit", artifacts: 129, want: outcomeLayoutRejected},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "store")
			first := readFixture(t, "generation-v1-empty.json")
			future := testGenerationWithParent(t, 50, 1, testDigestHex(first))
			firstRef := testGenerationRef{sequence: 1, sha256: testDigestHex(first), schema: currentSchemaVersion}
			futureRef := testGenerationRef{sequence: 50, sha256: testDigestHex(future), schema: currentSchemaVersion}
			installStoreLayout(
				t,
				root,
				map[uint64][]byte{1: first, 50: future},
				testManifestSlotBytes(1, 1, testManifestPayloadBytes(firstRef, nil, 1)),
				testManifestSlotBytes(2, 1, testFutureManifestPayload(futureRef, &firstRef)),
			)
			addTemporaryGenerationArtifacts(t, root, test.artifacts)

			result := openForTest(t, root, nil, nil)
			assertOutcome(t, result.outcome, test.want)
			if result.store != nil || result.state != nil || result.recovery != nil {
				t.Fatal("future selected slot returned active state")
			}
		})
	}
}

func TestLowerEpochFutureSlotReferencesAreExcludedFromArtifactCount(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	first := readFixture(t, "generation-v1-empty.json")
	second := readFixture(t, "generation-v1-child-empty.json")
	future := testGenerationWithParent(t, 50, 1, testDigestHex(first))
	firstRef := testGenerationRef{sequence: 1, sha256: testDigestHex(first), schema: currentSchemaVersion}
	secondRef := testGenerationRef{sequence: 2, sha256: testDigestHex(second), schema: currentSchemaVersion}
	futureRef := testGenerationRef{sequence: 50, sha256: testDigestHex(future), schema: currentSchemaVersion}
	installStoreLayout(
		t,
		root,
		map[uint64][]byte{1: first, 2: second, 50: future},
		testManifestSlotBytes(2, 1, testManifestPayloadBytes(secondRef, &firstRef, 1)),
		testManifestSlotBytes(1, 1, testFutureManifestPayload(futureRef, &firstRef)),
	)
	addTemporaryGenerationArtifacts(t, root, 128)

	result := openForTest(t, root, nil, nil)
	defer closeStore(t, result)
	assertOutcome(t, result.outcome, outcomeOpenedCurrent)
}

func testFutureManifestPayload(current testGenerationRef, parent *testGenerationRef) []byte {
	parentJSON := "null"
	if parent != nil {
		parentJSON = testFutureGenerationRefJSON(*parent)
	}
	return []byte(fmt.Sprintf(
		"{\"current\":%s,\"manifest_version\":2,\"parent\":%s,\"v2_extension\":true}\n",
		testFutureGenerationRefJSON(current),
		parentJSON,
	))
}

func testFutureGenerationRefJSON(ref testGenerationRef) string {
	return fmt.Sprintf(
		"{\"generation\":%d,\"generation_file\":%q,\"generation_sha256\":%q,\"reference_extension\":true,\"schema_version\":%d}",
		ref.sequence,
		testGenerationFilename(ref.sequence),
		ref.sha256,
		ref.schema,
	)
}

func testGenerationWithParent(t *testing.T, sequence, parentSequence uint64, parentSHA256 string) []byte {
	t.Helper()
	payload, err := encodeGenerationV1(generationV1{
		metadata: generationMetadata{
			sequence:       sequence,
			parentSequence: &parentSequence,
			parentSHA256:   &parentSHA256,
		},
		state:         emptyLogicalState(t),
		schemaVersion: currentSchemaVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func installThreeGenerationStore(t *testing.T, root string) {
	t.Helper()
	first := readFixture(t, "generation-v1-empty.json")
	second := readFixture(t, "generation-v1-child-empty.json")
	parentSequence := uint64(2)
	parentSHA256 := testDigestHex(second)
	third, err := encodeGenerationV1(generationV1{
		metadata: generationMetadata{
			sequence:       3,
			parentSequence: &parentSequence,
			parentSHA256:   &parentSHA256,
		},
		state:         emptyLogicalState(t),
		schemaVersion: currentSchemaVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	firstRef := testGenerationRef{sequence: 1, sha256: testDigestHex(first), schema: currentSchemaVersion}
	secondRef := testGenerationRef{sequence: 2, sha256: testDigestHex(second), schema: currentSchemaVersion}
	thirdRef := testGenerationRef{sequence: 3, sha256: testDigestHex(third), schema: currentSchemaVersion}
	installStoreLayout(
		t,
		root,
		map[uint64][]byte{1: first, 2: second, 3: third},
		testManifestSlotBytes(3, 1, testManifestPayloadBytes(thirdRef, &secondRef, 1)),
		testManifestSlotBytes(2, 1, testManifestPayloadBytes(secondRef, &firstRef, 1)),
	)
}

func addUnreferencedGenerations(t *testing.T, root string, firstSequence uint64, count int) {
	t.Helper()
	for offset := 0; offset < count; offset++ {
		sequence := firstSequence + uint64(offset)
		testWritePrivateFile(
			t,
			filepath.Join(root, "generations", testGenerationFilename(sequence)),
			[]byte("{\"unreferenced\":true}\n"),
		)
	}
}

func addTemporaryGenerationArtifacts(t *testing.T, root string, count int) {
	t.Helper()
	for index := 0; index < count; index++ {
		testWritePrivateFile(
			t,
			filepath.Join(root, "generations", fmt.Sprintf(".tmp-generation-%04d", index)),
			[]byte("temporary\n"),
		)
	}
}
