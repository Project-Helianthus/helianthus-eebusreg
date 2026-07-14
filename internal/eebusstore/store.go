//go:build linux || darwin

package eebusstore

import (
	"errors"
	"math"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

type storeConfig struct {
	root       string
	backend    *nativeSyscallBackend
	providers  map[providerKey]protectedKeyProvider
	migrations migrationGraph
}

type recoveryCandidate struct {
	sequence uint64
}

type openResult struct {
	outcome  outcome
	err      error
	store    *store
	state    *stateV1
	recovery *recoveryCandidate
}

type commitResult struct {
	outcome outcome
	err     error
}

type store struct {
	mu          sync.Mutex
	backend     *nativeSyscallBackend
	providers   map[providerKey]protectedKeyProvider
	migrations  migrationGraph
	root        *os.File
	generations *os.File
	lock        *os.File
	identity    fileIdentity
	selected    *selectedManifest
	manifest    *manifestPayloadV1
	state       stateV1
	closed      bool
	poisoned    bool
}

func openStore(config storeConfig) openResult {
	if config.backend == nil {
		return openFailure(newStoreError(outcomeFilesystemCapabilityUnavailable, "select_backend", errors.New("backend missing")))
	}
	prepared, err := config.backend.prepareRoot(config.root)
	if err != nil {
		return openFailure(err)
	}
	if !acquireLocalWriter(prepared.identity) {
		_ = prepared.lock.Close()
		_ = prepared.root.Close()
		return openFailure(newStoreError(outcomeWriterBusy, "acquire_local_lock", errors.New("writer busy")))
	}
	if err := acquireProcessLock(prepared.lock); err != nil {
		releaseLocalWriter(prepared.identity)
		_ = prepared.lock.Close()
		_ = prepared.root.Close()
		return openFailure(err)
	}

	opened := &store{
		backend:    config.backend,
		providers:  config.providers,
		migrations: config.migrations,
		root:       prepared.root,
		lock:       prepared.lock,
		identity:   prepared.identity,
	}
	if opened.migrations.current == 0 {
		opened.migrations, err = newMigrationGraph(currentSchemaVersion, nil)
		if err != nil {
			opened.abort()
			return openFailure(err)
		}
	}
	if err := opened.openGenerationDirectory(); err != nil {
		opened.abort()
		return openFailure(err)
	}
	layout, err := opened.verifyLayout()
	if err != nil {
		opened.abort()
		return openFailure(err)
	}
	if len(layout.slotA) == 0 && len(layout.slotB) == 0 {
		if layout.generationEntries != 0 || layout.temporaryEntries != 0 {
			opened.abort()
			return openFailure(newStoreError(outcomeNoValidManifest, "open_manifest", errors.New("existing state has no manifest")))
		}
		committed := opened.commit(stateV1{})
		if committed.outcome != outcomeCommitDurable {
			opened.abort()
			return openResult{outcome: committed.outcome, err: committed.err}
		}
		state := cloneStateV1(opened.state)
		return openResult{outcome: outcomeOpenedEmpty, store: opened, state: &state}
	}

	selected, err := selectManifestSlots(layout.slotA, layout.slotB)
	if err != nil {
		opened.abort()
		return openFailure(err)
	}
	if selected.envelope.slotFormatVersion != currentSlotVersion {
		opened.abort()
		return openFailure(versionError("slot_version", selected.envelope.slotFormatVersion, currentSlotVersion))
	}
	manifest, err := decodeSelectedManifest(selected.payload)
	if err != nil {
		opened.abort()
		return openFailure(err)
	}
	path, err := opened.migrations.pathFrom(manifest.current.schemaVersion)
	if err != nil {
		opened.abort()
		return openFailure(err)
	}
	current, err := opened.loadCurrentGeneration(manifest)
	if err != nil {
		return opened.classifyRecovery(manifest, err)
	}
	if err := validateProtectedKeys(current.state, opened.providers); err != nil {
		opened.abort()
		return openFailure(err)
	}
	opened.selected = &selected
	opened.manifest = &manifest
	opened.state = cloneStateV1(current.state)
	if len(path) != 0 {
		migrated, err := opened.migrations.apply(manifest.current.schemaVersion, current.state)
		if err != nil {
			opened.abort()
			return openFailure(err)
		}
		committed := opened.commit(migrated)
		if committed.outcome != outcomeCommitDurable {
			opened.abort()
			return openResult{outcome: committed.outcome, err: committed.err}
		}
		state := cloneStateV1(opened.state)
		return openResult{outcome: outcomeOpenedMigrated, store: opened, state: &state}
	}
	state := cloneStateV1(current.state)
	return openResult{outcome: outcomeOpenedCurrent, store: opened, state: &state}
}

func openFailure(err error) openResult {
	return openResult{outcome: outcomeOf(err), err: err}
}

func versionError(operation string, found, current uint64) error {
	if found > current {
		return newStoreError(outcomeUnsupportedFutureVersion, operation, errors.New("future version"))
	}
	return newStoreError(outcomeUnsupportedLegacyVersion, operation, errors.New("legacy version"))
}

func decodeSelectedManifest(payload []byte) (manifestPayloadV1, error) {
	var wire manifestPayloadWire
	if err := validateCanonicalJSON(payload, maxManifestPayloadBytes, maxJSONDepth); err != nil {
		return manifestPayloadV1{}, err
	}
	if err := decodeClosedJSON(payload, &wire); err != nil {
		return manifestPayloadV1{}, malformed("decode_manifest", err)
	}
	if _, err := decodeGenerationReference(wire.Current); err != nil {
		return manifestPayloadV1{}, err
	}
	if wire.Parent != nil {
		if _, err := decodeGenerationReference(*wire.Parent); err != nil {
			return manifestPayloadV1{}, err
		}
	}
	if wire.ManifestVersion != currentManifestVersion {
		return manifestPayloadV1{}, versionError("manifest_version", wire.ManifestVersion, currentManifestVersion)
	}
	return decodeManifestPayloadV1(payload)
}

func (opened *store) openGenerationDirectory() error {
	directory, err := openVerifiedAtOptional(opened.root, "generations", true, false)
	if err != nil {
		return err
	}
	if directory == nil {
		names, err := directoryNames(opened.root)
		if err != nil || len(names) != 1 || names[0] != "LOCK" {
			return newStoreError(outcomeLayoutRejected, "resume_bootstrap", err)
		}
		if err := unix.Mkdirat(int(opened.root.Fd()), "generations", 0o700); err != nil {
			return newStoreError(outcomeIOFailed, "create_generations", err)
		}
		if err := opened.backend.syncDirectory(opened.root, pointBootstrapRootFsync, directoryRoot); err != nil {
			return newStoreError(outcomeBootstrapDurabilityUnknown, "bootstrap_root_fsync", err)
		}
		directory, _, err = openVerifiedAt(opened.root, "generations", true, false)
		if err != nil {
			return err
		}
	}
	opened.generations = directory
	return nil
}

type layoutSnapshot struct {
	slotA             []byte
	slotB             []byte
	generationEntries int
	temporaryEntries  int
}

func (opened *store) verifyLayout() (layoutSnapshot, error) {
	var snapshot layoutSnapshot
	currentDirectory, _, err := openVerifiedAt(opened.root, "generations", true, false)
	if err != nil {
		return snapshot, err
	}
	currentIdentity, identityErr := descriptorIdentity(currentDirectory)
	heldIdentity, heldErr := descriptorIdentity(opened.generations)
	_ = currentDirectory.Close()
	if identityErr != nil || heldErr != nil || currentIdentity != heldIdentity {
		return snapshot, newStoreError(outcomeLayoutRejected, "verify_generations", errors.New("directory identity mismatch"))
	}

	rootNames, err := directoryNames(opened.root)
	if err != nil {
		return snapshot, newStoreError(outcomeIOFailed, "enumerate_root", err)
	}
	for _, name := range rootNames {
		switch {
		case name == "LOCK", name == "generations":
			continue
		case name == "MANIFEST.A", name == "MANIFEST.B":
			payload, err := readVerifiedFile(opened.root, name, maxManifestEnvelopeBytes)
			if err != nil {
				return snapshot, err
			}
			if name == "MANIFEST.A" {
				snapshot.slotA = payload
			} else {
				snapshot.slotB = payload
			}
		case manifestTempPattern.MatchString(name):
			file, _, err := openVerifiedAt(opened.root, name, false, false)
			if err != nil {
				return snapshot, err
			}
			_ = file.Close()
			snapshot.temporaryEntries++
		default:
			return snapshot, newStoreError(outcomeLayoutRejected, "enumerate_root", errors.New("unknown root entry"))
		}
	}

	generationNames, err := directoryNames(opened.generations)
	if err != nil {
		return snapshot, newStoreError(outcomeIOFailed, "enumerate_generations", err)
	}
	for _, name := range generationNames {
		if _, valid := parseGenerationName(name); !valid && !generationTempPattern.MatchString(name) {
			return snapshot, newStoreError(outcomeLayoutRejected, "enumerate_generations", errors.New("unknown generation entry"))
		}
		file, _, err := openVerifiedAt(opened.generations, name, false, false)
		if err != nil {
			return snapshot, err
		}
		_ = file.Close()
		if generationTempPattern.MatchString(name) {
			snapshot.temporaryEntries++
		} else {
			snapshot.generationEntries++
		}
	}
	if snapshot.temporaryEntries+snapshot.generationEntries > 130 {
		return snapshot, newStoreError(outcomeLayoutRejected, "enumerate_layout", errors.New("entry bound"))
	}
	return snapshot, nil
}

func (opened *store) loadCurrentGeneration(manifest manifestPayloadV1) (generationV1, error) {
	reference := manifest.current
	if reference.schemaVersion != currentSchemaVersion {
		return generationV1{}, versionError("generation_version", reference.schemaVersion, currentSchemaVersion)
	}
	exists, err := objectExistsAt(opened.generations, reference.generationFile)
	if err != nil {
		return generationV1{}, err
	}
	if !exists {
		return generationV1{}, newStoreError(outcomeNoValidCurrent, "read_current", errors.New("current generation missing"))
	}
	payload, err := readVerifiedFile(opened.generations, reference.generationFile, maxGenerationBytes)
	if err != nil {
		return generationV1{}, err
	}
	if sha256Hex(payload) != reference.generationSHA256 {
		return generationV1{}, newStoreError(outcomeNoValidCurrent, "read_current", errors.New("current digest mismatch"))
	}
	generation, err := decodeGenerationV1(payload)
	if err != nil || !generationMatchesManifest(generation, manifest) {
		return generationV1{}, newStoreError(outcomeNoValidCurrent, "validate_current", err)
	}
	return generation, nil
}

func generationMatchesManifest(generation generationV1, manifest manifestPayloadV1) bool {
	if generation.metadata.sequence != manifest.current.generation {
		return false
	}
	if manifest.parent == nil {
		return generation.metadata.parentSequence == nil && generation.metadata.parentSHA256 == nil
	}
	return generation.metadata.parentSequence != nil && generation.metadata.parentSHA256 != nil &&
		*generation.metadata.parentSequence == manifest.parent.generation &&
		*generation.metadata.parentSHA256 == manifest.parent.generationSHA256
}

func (opened *store) classifyRecovery(manifest manifestPayloadV1, currentErr error) openResult {
	if outcomeOf(currentErr) != outcomeNoValidCurrent {
		opened.abort()
		return openFailure(currentErr)
	}
	if manifest.parent == nil || manifest.parent.schemaVersion != currentSchemaVersion {
		opened.abort()
		return openFailure(newStoreError(outcomeNoValidCurrent, "classify_recovery", currentErr))
	}
	exists, err := objectExistsAt(opened.generations, manifest.parent.generationFile)
	if err != nil {
		opened.abort()
		return openFailure(err)
	}
	if !exists {
		opened.abort()
		return openFailure(newStoreError(outcomeNoValidCurrent, "validate_recovery", errors.New("parent generation missing")))
	}
	payload, err := readVerifiedFile(opened.generations, manifest.parent.generationFile, maxGenerationBytes)
	if err != nil {
		opened.abort()
		return openFailure(err)
	}
	if sha256Hex(payload) != manifest.parent.generationSHA256 {
		opened.abort()
		return openFailure(newStoreError(outcomeNoValidCurrent, "validate_recovery", errors.New("parent digest mismatch")))
	}
	parent, err := decodeGenerationV1(payload)
	if err != nil || parent.metadata.sequence != manifest.parent.generation {
		opened.abort()
		return openFailure(newStoreError(outcomeNoValidCurrent, "validate_recovery", err))
	}
	sequence := manifest.parent.generation
	opened.abort()
	return openResult{
		outcome:  outcomeRecoveryCandidateAvailable,
		err:      newStoreError(outcomeRecoveryCandidateAvailable, "classify_recovery", currentErr),
		recovery: &recoveryCandidate{sequence: sequence},
	}
}

func (opened *store) close() error {
	opened.mu.Lock()
	defer opened.mu.Unlock()
	return opened.closeLocked()
}

func (opened *store) closeLocked() error {
	if opened.closed {
		return nil
	}
	opened.closed = true
	var first error
	if err := releaseProcessLock(opened.lock); err != nil {
		first = err
	}
	for _, file := range []*os.File{opened.generations, opened.lock, opened.root} {
		if file != nil {
			if err := file.Close(); err != nil && first == nil {
				first = newStoreError(outcomeIOFailed, "close_store", err)
			}
		}
	}
	releaseLocalWriter(opened.identity)
	return first
}

func (opened *store) abort() {
	opened.mu.Lock()
	_ = opened.closeLocked()
	opened.mu.Unlock()
}

func (opened *store) nextSequence() (uint64, error) {
	if opened.manifest == nil {
		return 1, nil
	}
	if opened.manifest.current.generation == math.MaxInt64 {
		return 0, newStoreError(outcomeCommitNotPublished, "next_generation", errors.New("sequence exhausted"))
	}
	return opened.manifest.current.generation + 1, nil
}
