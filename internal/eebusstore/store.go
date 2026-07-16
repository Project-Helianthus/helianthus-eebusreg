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
	root                            string
	backend                         *nativeSyscallBackend
	providers                       map[providerKey]protectedKeyProvider
	migrations                      migrationGraph
	retainUnavailableProtectedState bool
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
	mu             sync.Mutex
	backend        *nativeSyscallBackend
	providers      map[providerKey]protectedKeyProvider
	migrations     migrationGraph
	configuredPath string
	parent         *os.File
	rootName       string
	root           *os.File
	generations    *os.File
	lock           *os.File
	rootIdentity   fileIdentity
	lockIdentity   fileIdentity
	selected       *selectedManifest
	manifest       *manifestPayloadV1
	state          stateV1
	closed         bool
	poisoned       bool
}

func openStore(config storeConfig) openResult {
	if config.backend == nil {
		return openFailure(newStoreError(outcomeFilesystemCapabilityUnavailable, "select_backend", errors.New("backend missing")))
	}
	prepared, err := config.backend.prepareRoot(config.root)
	if err != nil {
		return openFailure(err)
	}
	closePrepared := func() {
		_ = prepared.lock.Close()
		_ = prepared.root.Close()
		_ = prepared.parent.Close()
	}
	if !acquireLocalWriter(prepared.rootIdentity) {
		closePrepared()
		return openFailure(newStoreError(outcomeWriterBusy, "acquire_local_lock", errors.New("writer busy")))
	}
	if err := acquireProcessLock(prepared.root); err != nil {
		releaseLocalWriter(prepared.rootIdentity)
		closePrepared()
		return openFailure(err)
	}
	if err := acquireProcessLock(prepared.lock); err != nil {
		_ = releaseProcessLock(prepared.root)
		releaseLocalWriter(prepared.rootIdentity)
		closePrepared()
		return openFailure(err)
	}

	opened := &store{
		backend:        config.backend,
		providers:      config.providers,
		migrations:     config.migrations,
		configuredPath: prepared.configuredPath,
		parent:         prepared.parent,
		rootName:       prepared.rootName,
		root:           prepared.root,
		lock:           prepared.lock,
		rootIdentity:   prepared.rootIdentity,
		lockIdentity:   prepared.lockIdentity,
	}
	if err := opened.revalidateWriterIdentity(); err != nil {
		opened.abort()
		return openFailure(err)
	}
	if !prepared.bootstrapDurable {
		safe, err := inspectBootstrapSubset(opened.root)
		if err != nil {
			opened.abort()
			return openFailure(err)
		}
		if safe {
			lock, err := opened.backend.completeBootstrap(opened.parent, opened.root, opened.lock, false)
			if err != nil {
				opened.abort()
				return openFailure(err)
			}
			if lock != opened.lock {
				opened.abort()
				return openFailure(newStoreError(outcomeFilesystemCapabilityUnavailable, "resume_bootstrap", errors.New("lock identity changed")))
			}
		}
	}
	if opened.migrations.current == 0 {
		opened.migrations, err = currentMigrationGraph()
		if err != nil {
			opened.abort()
			return openFailure(err)
		}
	}
	if err := opened.openGenerationDirectory(); err != nil {
		opened.abort()
		return openFailure(err)
	}
	if err := opened.backend.probeCapabilities(opened.root, opened.generations); err != nil {
		opened.abort()
		return openFailure(err)
	}
	layout, err := opened.verifyLayout()
	if err != nil {
		opened.abort()
		return openFailure(err)
	}
	if len(layout.slotA) == 0 && len(layout.slotB) == 0 {
		if err := enforceArtifactBound(layout); err != nil {
			opened.abort()
			return openFailure(err)
		}
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
	if err := enforceArtifactBound(layout); err != nil {
		opened.abort()
		return openFailure(err)
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
		if config.retainUnavailableProtectedState && (outcomeOf(err) == outcomeKeyProviderUnavailable || outcomeOf(err) == outcomeKeyMaterialUnavailable) {
			opened.selected = &selected
			opened.manifest = &manifest
			opened.state = cloneStateV1(current.state)
			state := cloneStateV1(current.state)
			return openResult{outcome: outcomeOf(err), err: err, store: opened, state: &state}
		}
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
	if err := validateCanonicalJSON(payload, maxManifestPayloadBytes, maxJSONDepth); err != nil {
		return manifestPayloadV1{}, err
	}
	version, err := decodeManifestVersion(payload)
	if err != nil {
		return manifestPayloadV1{}, err
	}
	if version != currentManifestVersion {
		return manifestPayloadV1{}, versionError("manifest_version", version, currentManifestVersion)
	}
	return decodeManifestPayloadV1(payload)
}

func (opened *store) openGenerationDirectory() error {
	directory, err := openVerifiedAtOptional(opened.root, "generations", true, false)
	if err != nil {
		return err
	}
	if directory == nil {
		return newStoreError(outcomeLayoutRejected, "open_generations", errors.New("generations directory missing"))
	}
	opened.generations = directory
	return nil
}

func (opened *store) revalidateWriterIdentity() error {
	heldRootIdentity, err := descriptorIdentity(opened.root)
	if err != nil || heldRootIdentity != opened.rootIdentity {
		return newStoreError(outcomeLayoutRejected, "revalidate_root", errors.New("held root identity changed"))
	}
	heldLockIdentity, err := descriptorIdentity(opened.lock)
	if err != nil || heldLockIdentity != opened.lockIdentity {
		return newStoreError(outcomeLayoutRejected, "revalidate_lock", errors.New("held lock identity changed"))
	}
	var parentStat unix.Stat_t
	if err := unix.Fstatat(int(opened.parent.Fd()), opened.rootName, &parentStat, unix.AT_SYMLINK_NOFOLLOW); err != nil || opened.rootIdentity != (fileIdentity{device: uint64(parentStat.Dev), inode: uint64(parentStat.Ino)}) {
		return newStoreError(outcomeLayoutRejected, "revalidate_root", errors.New("parent root identity changed"))
	}

	configuredRoot, err := openAbsoluteDirectoryNoFollow(opened.configuredPath)
	if err != nil {
		return newStoreError(outcomeLayoutRejected, "revalidate_root", err)
	}
	defer configuredRoot.Close()
	if err := verifyOpenedDescriptor(configuredRoot, true); err != nil {
		return err
	}
	configuredIdentity, err := descriptorIdentity(configuredRoot)
	if err != nil || configuredIdentity != opened.rootIdentity {
		return newStoreError(outcomeLayoutRejected, "revalidate_root", errors.New("configured root identity changed"))
	}
	configuredLock, _, err := openVerifiedAt(configuredRoot, "LOCK", false, true)
	if err != nil {
		return err
	}
	defer configuredLock.Close()
	configuredLockIdentity, err := descriptorIdentity(configuredLock)
	if err != nil || configuredLockIdentity != opened.lockIdentity {
		return newStoreError(outcomeLayoutRejected, "revalidate_lock", errors.New("configured lock identity changed"))
	}
	if err := verifyEmptyLock(configuredLock); err != nil {
		return newStoreError(outcomeLayoutRejected, "revalidate_lock", err)
	}
	return nil
}

type layoutSnapshot struct {
	slotA             []byte
	slotB             []byte
	generationEntries int
	temporaryEntries  int
	generationNames   []string
}

const (
	maximumArtifactEntries           = 128
	maximumPublicationGenerationRefs = 4
)

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
			snapshot.generationNames = append(snapshot.generationNames, name)
		}
	}
	if snapshot.temporaryEntries+snapshot.generationEntries > maximumArtifactEntries+maximumPublicationGenerationRefs {
		return snapshot, newStoreError(outcomeLayoutRejected, "enumerate_layout", errors.New("entry bound"))
	}
	return snapshot, nil
}

func enforceArtifactBound(layout layoutSnapshot) error {
	referenced, _ := publicationGenerationReferences(layout, false)
	artifacts := layout.temporaryEntries
	for _, name := range layout.generationNames {
		if _, preserved := referenced[name]; !preserved {
			artifacts++
		}
	}
	if artifacts > maximumArtifactEntries {
		return newStoreError(outcomeLayoutRejected, "enumerate_layout", errors.New("artifact bound"))
	}
	return nil
}

func publicationGenerationReferences(layout layoutSnapshot, requireSafe bool) (map[string]struct{}, error) {
	referenced := make(map[string]struct{}, maximumPublicationGenerationRefs)
	for _, raw := range [][]byte{layout.slotA, layout.slotB} {
		if len(raw) == 0 {
			continue
		}
		envelope, err := decodeManifestSlot(raw)
		if err != nil {
			continue
		}
		references, err := extractManifestGenerationReferences(envelope.manifestPayload)
		if err != nil {
			if requireSafe {
				return nil, newStoreError(outcomeMalformedState, "extract_manifest_references", err)
			}
			continue
		}
		for _, reference := range references {
			referenced[reference.generationFile] = struct{}{}
		}
	}
	return referenced, nil
}

func (opened *store) loadCurrentGeneration(manifest manifestPayloadV1) (generationV1, error) {
	reference := manifest.current
	if _, err := opened.migrations.pathFrom(reference.schemaVersion); err != nil {
		return generationV1{}, err
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
	if err != nil || generationSchemaVersion(payload) != reference.schemaVersion || !generationMatchesManifest(generation, manifest) {
		return generationV1{}, newStoreError(outcomeNoValidCurrent, "validate_current", err)
	}
	return generation, nil
}

func generationMatchesManifest(generation generationV1, manifest manifestPayloadV1) bool {
	if generation.metadata.sequence != manifest.current.generation || !hasDirectParentChronology(manifest) {
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
	if !hasDirectParentChronology(manifest) || manifest.parent == nil || manifest.parent.schemaVersion > opened.migrations.current {
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
		if outcomeOf(err) == outcomeMalformedState {
			return openFailure(newStoreError(outcomeNoValidCurrent, "validate_recovery", err))
		}
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

func hasDirectParentChronology(manifest manifestPayloadV1) bool {
	if manifest.current.generation == 1 {
		return manifest.parent == nil
	}
	return manifest.parent != nil && manifest.parent.generation < manifest.current.generation
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
	if err := releaseProcessLock(opened.root); err != nil && first == nil {
		first = err
	}
	for _, file := range []*os.File{opened.generations, opened.lock, opened.root, opened.parent} {
		if file != nil {
			if err := file.Close(); err != nil && first == nil {
				first = newStoreError(outcomeIOFailed, "close_store", err)
			}
		}
	}
	releaseLocalWriter(opened.rootIdentity)
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
