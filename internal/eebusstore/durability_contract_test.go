package eebusstore

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBootstrapFsyncOrderingPrecedesInitialPublication(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	var calls []syscallCall
	hook := func(call syscallCall) error {
		calls = append(calls, call)
		return nil
	}
	result := openForTest(t, root, hook, nil)
	defer closeStore(t, result)
	assertOutcome(t, result.outcome, outcomeOpenedEmpty)

	assertPointOrder(t, calls,
		pointBootstrapParentFsync,
		pointBootstrapLockFsync,
		pointBootstrapRootFsync,
		pointGenerationFileFsync,
		pointGenerationRename,
		pointGenerationsFsync,
		pointManifestFileFsync,
		pointManifestRename,
		pointPublicationRootFsync,
	)
}

func TestBootstrapDirectoryFsyncFailuresNeverPublishState(t *testing.T) {
	tests := map[string]struct {
		point syscallPoint
		want  outcome
	}{
		"parent fsync":               {point: pointBootstrapParentFsync, want: outcomeBootstrapDurabilityUnknown},
		"root fsync":                 {point: pointBootstrapRootFsync, want: outcomeBootstrapDurabilityUnknown},
		"directory fsync capability": {point: pointCapabilityDirectoryFsync, want: outcomeFilesystemCapabilityUnavailable},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "store")
			hook := func(call syscallCall) error {
				if call.point == test.point {
					return errors.New("synthetic fsync failure")
				}
				return nil
			}
			result := openForTest(t, root, hook, nil)
			assertOutcome(t, result.outcome, test.want)
			if result.store != nil || result.state != nil {
				t.Fatal("bootstrap fsync failure returned active state")
			}
			for _, slot := range []string{"MANIFEST.A", "MANIFEST.B"} {
				if _, err := os.Lstat(filepath.Join(root, slot)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("bootstrap failure published %s: %v", slot, err)
				}
			}
			generationDir := filepath.Join(root, "generations")
			if entries, err := os.ReadDir(generationDir); err == nil && len(entries) != 0 {
				t.Fatalf("bootstrap failure wrote generation entries: %v", testDirectoryNames(entries))
			} else if err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
		})
	}
}

func TestInterruptedBootstrapReplaysLockDurabilityBarriers(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	failLockFsync := func(call syscallCall) error {
		if call.point == pointBootstrapLockFsync {
			return errors.New("synthetic interrupted lock fsync")
		}
		return nil
	}
	interrupted := openForTest(t, root, failLockFsync, nil)
	assertOutcome(t, interrupted.outcome, outcomeBootstrapDurabilityUnknown)
	if interrupted.store != nil || interrupted.state != nil {
		t.Fatal("interrupted bootstrap returned active state")
	}

	var retryCalls []syscallCall
	retryHook := func(call syscallCall) error {
		retryCalls = append(retryCalls, call)
		return nil
	}
	retried := openForTest(t, root, retryHook, nil)
	defer closeStore(t, retried)
	assertOutcome(t, retried.outcome, outcomeOpenedEmpty)
	assertPointOrder(t, retryCalls,
		pointBootstrapLockFsync,
		pointBootstrapRootFsync,
		pointGenerationFileFsync,
		pointGenerationRename,
		pointGenerationsFsync,
		pointManifestFileFsync,
		pointManifestRename,
		pointPublicationRootFsync,
	)
}

func TestCommitFailuresBeforePublicationPreserveSelectedSlot(t *testing.T) {
	tests := map[string]struct {
		point syscallPoint
		err   error
	}{
		"generation write error":      {point: pointGenerationWrite, err: errors.New("generation write")},
		"generation file fsync":       {point: pointGenerationFileFsync, err: errors.New("generation fsync")},
		"generation rename":           {point: pointGenerationRename, err: errors.New("generation rename")},
		"generations directory fsync": {point: pointGenerationsFsync, err: errors.New("generations fsync")},
		"manifest write error":        {point: pointManifestWrite, err: errors.New("manifest write")},
		"manifest file fsync":         {point: pointManifestFileFsync, err: errors.New("manifest fsync")},
		"manifest rename":             {point: pointManifestRename, err: errors.New("manifest rename")},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "store")
			var armed bool
			hook := func(call syscallCall) error {
				if armed && call.point == test.point {
					return test.err
				}
				return nil
			}
			opened := openForTest(t, root, hook, nil)
			assertOutcome(t, opened.outcome, outcomeOpenedEmpty)
			selectedBefore := existingManifestSlots(t, root)
			state := emptyLogicalState(t)
			armed = true

			committed := opened.store.commit(state)
			assertOutcome(t, committed.outcome, outcomeCommitNotPublished)
			assertManifestSlotsEqual(t, existingManifestSlots(t, root), selectedBefore)
			closeStore(t, opened)
		})
	}
}

func TestCommitRejectsTrueShortWriteCounts(t *testing.T) {
	for _, test := range []struct {
		name        string
		shortOnCall int
	}{
		{name: "generation", shortOnCall: 1},
		{name: "manifest", shortOnCall: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "store")
			opened := openForTest(t, root, nil, nil)
			assertOutcome(t, opened.outcome, outcomeOpenedEmpty)
			selectedBefore := existingManifestSlots(t, root)
			calls := 0
			opened.store.backend.writeCount = func(file *os.File, payload []byte) (int, error) {
				calls++
				if calls == test.shortOnCall {
					return len(payload) - 1, nil
				}
				return file.Write(payload)
			}

			committed := opened.store.commit(emptyLogicalState(t))
			assertOutcome(t, committed.outcome, outcomeCommitNotPublished)
			assertManifestSlotsEqual(t, existingManifestSlots(t, root), selectedBefore)
			closeStore(t, opened)
		})
	}
}

func TestCommitUsesSameDirectoryTempsAndFixedPublicationNames(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	var calls []syscallCall
	hook := func(call syscallCall) error {
		calls = append(calls, call)
		return nil
	}
	opened := openForTest(t, root, hook, nil)
	defer closeStore(t, opened)
	assertOutcome(t, opened.outcome, outcomeOpenedEmpty)
	calls = nil

	committed := opened.store.commit(emptyLogicalState(t))
	assertOutcome(t, committed.outcome, outcomeCommitDurable)

	var generationRename, manifestRename *syscallCall
	for i := range calls {
		call := &calls[i]
		switch call.point {
		case pointGenerationRename:
			generationRename = call
		case pointManifestRename:
			manifestRename = call
		}
	}
	if generationRename == nil || manifestRename == nil {
		t.Fatalf("rename calls missing: %+v", calls)
	}
	assertRenameCall(t, *generationRename, directoryGenerations, ".tmp-generation-", testGenerationFilename(2))
	if manifestRename.directory != directoryRoot || !strings.HasPrefix(manifestRename.oldName, ".tmp-manifest-") {
		t.Fatalf("manifest rename = %+v, want fixed root temp prefix", *manifestRename)
	}
	if manifestRename.newName != "MANIFEST.A" && manifestRename.newName != "MANIFEST.B" {
		t.Fatalf("manifest target = %q, want fixed A/B slot", manifestRename.newName)
	}
}

func TestManifestReplacementWithoutRootFsyncIsDurabilityUnknown(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	var armed bool
	hook := func(call syscallCall) error {
		if armed && call.point == pointPublicationRootFsync {
			return errors.New("synthetic root fsync failure after replacement")
		}
		return nil
	}
	opened := openForTest(t, root, hook, nil)
	assertOutcome(t, opened.outcome, outcomeOpenedEmpty)
	oldSlots := existingManifestSlots(t, root)
	armed = true

	committed := opened.store.commit(emptyLogicalState(t))
	assertOutcome(t, committed.outcome, outcomeCommitDurabilityUnknown)
	newSlots := existingManifestSlots(t, root)
	if reflect.DeepEqual(newSlots, oldSlots) {
		t.Fatal("durability-unknown path did not replace the non-selected slot")
	}
	assertPreviouslySelectedSlotUnchanged(t, oldSlots, newSlots)
	closeStore(t, opened)

	reopened := openForTest(t, root, nil, nil)
	defer closeStore(t, reopened)
	assertOutcome(t, reopened.outcome, outcomeOpenedCurrent)
}

func TestPostPublicationMaintenanceFailureReportsApplied(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	var failPostMaintenance bool
	hook := func(call syscallCall) error {
		if failPostMaintenance && call.point == pointPostMaintenanceRemove {
			return errors.New("synthetic post-publication cleanup failure")
		}
		return nil
	}
	opened := openForTest(t, root, hook, nil)
	assertOutcome(t, opened.outcome, outcomeOpenedEmpty)
	state := emptyLogicalState(t)
	for i := 0; i < 2; i++ {
		committed := opened.store.commit(state)
		assertOutcome(t, committed.outcome, outcomeCommitDurable)
	}
	failPostMaintenance = true

	committed := opened.store.commit(state)
	assertOutcome(t, committed.outcome, outcomeCommitAppliedMaintenanceFailed)
	closeStore(t, opened)

	reopened := openForTest(t, root, nil, nil)
	defer closeStore(t, reopened)
	assertOutcome(t, reopened.outcome, outcomeOpenedCurrent)
}

func TestPrePublicationMaintenanceFailureLeavesSelectionUnchanged(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	var armed bool
	hook := func(call syscallCall) error {
		if armed && call.point == pointPreMaintenanceRemove {
			return errors.New("synthetic pre-publication cleanup failure")
		}
		return nil
	}
	opened := openForTest(t, root, hook, nil)
	assertOutcome(t, opened.outcome, outcomeOpenedEmpty)
	testWritePrivateFile(t, filepath.Join(root, "generations", ".tmp-generation-stale"), nil)
	before := existingManifestSlots(t, root)
	armed = true

	committed := opened.store.commit(emptyLogicalState(t))
	assertOutcome(t, committed.outcome, outcomeMaintenanceFailed)
	assertManifestSlotsEqual(t, existingManifestSlots(t, root), before)
	closeStore(t, opened)
}

func TestPreMaintenancePreservesLowerEpochFutureSlotGeneration(t *testing.T) {
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
	var armed bool
	hook := func(call syscallCall) error {
		if armed && call.point == pointGenerationWrite {
			return errors.New("synthetic generation write failure")
		}
		return nil
	}
	opened := openForTest(t, root, hook, nil)
	assertOutcome(t, opened.outcome, outcomeOpenedCurrent)
	armed = true

	committed := opened.store.commit(emptyLogicalState(t))
	assertOutcome(t, committed.outcome, outcomeCommitNotPublished)
	if _, err := os.Stat(filepath.Join(root, "generations", testGenerationFilename(50))); err != nil {
		t.Fatalf("future-slot generation was removed before failed publication: %v", err)
	}
	closeStore(t, opened)
}

func TestMaintenanceFailsBeforeDeletionWhenFutureReferencesAreUnsafe(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	opened := openForTest(t, root, nil, nil)
	assertOutcome(t, opened.outcome, outcomeOpenedEmpty)
	malformedFuture := []byte("{\"current\":{\"generation\":50,\"generation_file\":\"not-a-generation\",\"generation_sha256\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"schema_version\":1},\"manifest_version\":2,\"parent\":null,\"v2_extension\":true}\n")
	if err := validateCanonicalJSON(malformedFuture, maxManifestPayloadBytes, maxJSONDepth); err != nil {
		t.Fatalf("future manifest test payload is not canonical: %v", err)
	}
	target := manifestSlotFilename(otherManifestSlot(opened.store.selected.slot))
	testWritePrivateFile(t, filepath.Join(root, target), testManifestSlotBytes(1, 1, malformedFuture))
	stale := filepath.Join(root, "generations", ".tmp-generation-stale")
	testWritePrivateFile(t, stale, []byte("temporary\n"))

	committed := opened.store.commit(emptyLogicalState(t))
	assertOutcome(t, committed.outcome, outcomeMaintenanceFailed)
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("maintenance deleted before proving publication references: %v", err)
	}
	closeStore(t, opened)
}

func TestMaintenanceDirectoryFsyncFailuresReportPhaseOutcome(t *testing.T) {
	const (
		pointPreMaintenanceFsyncForTest  syscallPoint = "pre_maintenance_fsync"
		pointPostMaintenanceFsyncForTest syscallPoint = "post_maintenance_fsync"
	)

	t.Run("pre-publication", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "store")
		var armed, injected bool
		hook := func(call syscallCall) error {
			if armed && call.point == pointPreMaintenanceFsyncForTest {
				injected = true
				return errors.New("synthetic pre-maintenance directory fsync failure")
			}
			return nil
		}
		opened := openForTest(t, root, hook, nil)
		t.Cleanup(func() { closeStore(t, opened) })
		assertOutcome(t, opened.outcome, outcomeOpenedEmpty)
		testWritePrivateFile(t, filepath.Join(root, "generations", ".tmp-generation-fsync"), nil)
		before := existingManifestSlots(t, root)
		armed = true

		committed := opened.store.commit(emptyLogicalState(t))
		if !injected {
			t.Error("pre-maintenance directory fsync was not hook-injectable")
		}
		if committed.outcome != outcomeMaintenanceFailed {
			t.Errorf("outcome = %q, want %q", committed.outcome, outcomeMaintenanceFailed)
		}
		assertManifestSlotsEqual(t, existingManifestSlots(t, root), before)
	})

	t.Run("post-publication", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "store")
		var armed, injected bool
		hook := func(call syscallCall) error {
			if armed && call.point == pointPostMaintenanceFsyncForTest {
				injected = true
				return errors.New("synthetic post-maintenance directory fsync failure")
			}
			return nil
		}
		opened := openForTest(t, root, hook, nil)
		t.Cleanup(func() { closeStore(t, opened) })
		assertOutcome(t, opened.outcome, outcomeOpenedEmpty)
		state := emptyLogicalState(t)
		for i := 0; i < 2; i++ {
			committed := opened.store.commit(state)
			assertOutcome(t, committed.outcome, outcomeCommitDurable)
		}
		armed = true

		committed := opened.store.commit(state)
		if !injected {
			t.Error("post-maintenance directory fsync was not hook-injectable")
		}
		if committed.outcome != outcomeCommitAppliedMaintenanceFailed {
			t.Errorf("outcome = %q, want %q", committed.outcome, outcomeCommitAppliedMaintenanceFailed)
		}
		closeStore(t, opened)

		reopened := openForTest(t, root, nil, nil)
		defer closeStore(t, reopened)
		assertOutcome(t, reopened.outcome, outcomeOpenedCurrent)
	})
}

func emptyLogicalState(t *testing.T) stateV1 {
	t.Helper()
	generation, err := decodeGenerationV1(readFixture(t, "generation-v1-empty.json"))
	if err != nil {
		t.Fatal(err)
	}
	return generation.state
}

func assertPointOrder(t *testing.T, calls []syscallCall, points ...syscallPoint) {
	t.Helper()
	position := -1
	for _, point := range points {
		found := -1
		for index := position + 1; index < len(calls); index++ {
			if calls[index].point == point {
				found = index
				break
			}
		}
		if found < 0 {
			t.Fatalf("point %q missing after index %d in calls %+v", point, position, calls)
		}
		position = found
	}
}

func existingManifestSlots(t *testing.T, root string) map[string][]byte {
	t.Helper()
	slots := map[string][]byte{}
	for _, name := range []string{"MANIFEST.A", "MANIFEST.B"} {
		payload, err := os.ReadFile(filepath.Join(root, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		slots[name] = payload
	}
	return slots
}

func assertManifestSlotsEqual(t *testing.T, got, want map[string][]byte) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("manifest slots changed before publication: got keys %v, want keys %v", mapKeys(got), mapKeys(want))
	}
}

func assertPreviouslySelectedSlotUnchanged(t *testing.T, before, after map[string][]byte) {
	t.Helper()
	if len(before) != 1 {
		t.Fatalf("bootstrap slot count = %d, want 1", len(before))
	}
	for name, payload := range before {
		if !reflect.DeepEqual(after[name], payload) {
			t.Fatalf("previously selected slot %s changed", name)
		}
	}
}

func mapKeys(values map[string][]byte) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func assertRenameCall(t *testing.T, call syscallCall, directory directoryRole, prefix, target string) {
	t.Helper()
	if call.directory != directory {
		t.Fatalf("rename directory = %q, want %q", call.directory, directory)
	}
	if !strings.HasPrefix(call.oldName, prefix) || strings.ContainsRune(call.oldName, os.PathSeparator) {
		t.Fatalf("rename temp name = %q, want fixed basename prefix %q", call.oldName, prefix)
	}
	if call.newName != target || strings.ContainsRune(call.newName, os.PathSeparator) {
		t.Fatalf("rename target = %q, want fixed basename %q", call.newName, target)
	}
}
