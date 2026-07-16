//go:build linux || darwin

package eebusstore

import (
	"errors"
	"math"
)

func (opened *store) commit(state stateV1) commitResult {
	opened.mu.Lock()
	defer opened.mu.Unlock()
	if opened.closed || opened.poisoned {
		return commitFailure(outcomeCommitNotPublished, "commit_state", errors.New("store is not usable"))
	}
	if err := opened.revalidateWriterIdentity(); err != nil {
		return commitResult{outcome: outcomeOf(err), err: err}
	}
	if err := validateStateV1(state); err != nil {
		return commitFailure(outcomeCommitNotPublished, "commit_validate", err)
	}
	if err := validateProtectedKeys(state, opened.providers); err != nil {
		return commitResult{outcome: outcomeOf(err), err: err}
	}
	sequence, err := opened.nextSequence()
	if err != nil {
		return commitResult{outcome: outcomeOf(err), err: err}
	}
	epoch := uint64(1)
	if opened.selected != nil {
		if opened.selected.epoch == math.MaxInt64 {
			return commitFailure(outcomeCommitNotPublished, "commit_validate", errors.New("manifest epoch exhausted"))
		}
		epoch = opened.selected.epoch + 1
	}
	if err := opened.maintenance(pointPreMaintenanceRemove); err != nil {
		return commitFailure(outcomeMaintenanceFailed, "commit_pre_maintenance", err)
	}

	metadata := generationMetadata{sequence: sequence}
	var parent *generationReference
	if opened.manifest != nil {
		parentValue := opened.manifest.current
		parent = &parentValue
		parentSequence := parentValue.generation
		parentSHA := parentValue.generationSHA256
		metadata.parentSequence = &parentSequence
		metadata.parentSHA256 = &parentSHA
	}
	schemaVersion := opened.migrations.current
	if schemaVersion == 0 {
		schemaVersion = currentSchemaVersion
	}
	generation := generationV1{metadata: metadata, state: cloneStateV1(state), schemaVersion: schemaVersion}
	generationBytes, err := encodeGenerationV1(generation)
	if err != nil {
		return commitFailure(outcomeCommitNotPublished, "commit_validate", err)
	}
	generationReference := generationReference{
		generation:       sequence,
		generationFile:   generationFilename(sequence),
		generationSHA256: sha256Hex(generationBytes),
		schemaVersion:    schemaVersion,
	}
	if result := opened.publishGeneration(generationReference, generationBytes); result.err != nil {
		return result
	}

	manifest := manifestPayloadV1{
		manifestVersion: currentManifestVersion,
		current:         generationReference,
		parent:          parent,
	}
	manifestBytes, err := encodeManifestPayloadV1(manifest)
	if err != nil {
		return commitFailure(outcomeCommitNotPublished, "encode_manifest", err)
	}
	envelope := manifestEnvelope{
		slotFormatVersion: currentSlotVersion,
		manifestEpoch:     epoch,
		manifestPayload:   manifestBytes,
		manifestSHA256:    sha256Hex(manifestBytes),
	}
	envelopeBytes, err := encodeManifestSlot(envelope)
	if err != nil {
		return commitFailure(outcomeCommitNotPublished, "encode_manifest_slot", err)
	}
	target := manifestSlotA
	if opened.selected != nil {
		target = otherManifestSlot(opened.selected.slot)
	}
	if result := opened.publishManifest(target, envelopeBytes); result.err != nil {
		return result
	}
	if err := opened.backend.syncDirectory(opened.root, pointPublicationRootFsync, directoryRoot); err != nil {
		opened.poisoned = true
		return commitFailure(outcomeCommitDurabilityUnknown, "commit_root_fsync", err)
	}

	selected := selectedManifest{
		slot:        target,
		epoch:       epoch,
		payload:     append([]byte(nil), manifestBytes...),
		envelope:    envelope,
		envelopeRaw: append([]byte(nil), envelopeBytes...),
	}
	opened.selected = &selected
	opened.manifest = &manifest
	opened.state = cloneStateV1(state)
	if err := opened.maintenance(pointPostMaintenanceRemove); err != nil {
		opened.poisoned = true
		return commitFailure(outcomeCommitAppliedMaintenanceFailed, "commit_post_maintenance", err)
	}
	return commitResult{outcome: outcomeCommitDurable}
}

func (opened *store) publishGeneration(reference generationReference, payload []byte) commitResult {
	temporary, name, err := opened.backend.createTemporary(opened.generations, directoryGenerations, ".tmp-generation-")
	if err != nil {
		return commitFailure(outcomeCommitNotPublished, "create_generation", err)
	}
	defer temporary.Close()
	if err := opened.backend.writeAll(temporary, directoryGenerations, name, payload, pointGenerationWrite); err != nil {
		return commitFailure(outcomeCommitNotPublished, "write_generation", err)
	}
	if err := opened.backend.syncFile(temporary, pointGenerationFileFsync, directoryGenerations, name); err != nil {
		return commitFailure(outcomeCommitNotPublished, "fsync_generation", err)
	}
	if err := opened.backend.rename(opened.generations, directoryGenerations, name, reference.generationFile, pointGenerationRename); err != nil {
		return commitFailure(outcomeCommitNotPublished, "rename_generation", err)
	}
	if err := opened.backend.syncDirectory(opened.generations, pointGenerationsFsync, directoryGenerations); err != nil {
		return commitFailure(outcomeCommitNotPublished, "fsync_generations", err)
	}
	return commitResult{outcome: outcomeCommitDurable}
}

func (opened *store) publishManifest(target manifestSlot, payload []byte) commitResult {
	temporary, name, err := opened.backend.createTemporary(opened.root, directoryRoot, ".tmp-manifest-")
	if err != nil {
		return commitFailure(outcomeCommitNotPublished, "create_manifest", err)
	}
	defer temporary.Close()
	if err := opened.backend.writeAll(temporary, directoryRoot, name, payload, pointManifestWrite); err != nil {
		return commitFailure(outcomeCommitNotPublished, "write_manifest", err)
	}
	if err := opened.backend.syncFile(temporary, pointManifestFileFsync, directoryRoot, name); err != nil {
		return commitFailure(outcomeCommitNotPublished, "fsync_manifest", err)
	}
	if err := opened.backend.rename(opened.root, directoryRoot, name, manifestSlotFilename(target), pointManifestRename); err != nil {
		return commitFailure(outcomeCommitNotPublished, "rename_manifest", err)
	}
	return commitResult{outcome: outcomeCommitDurable}
}

func (opened *store) maintenance(point syscallPoint) error {
	layout, err := opened.verifyLayout()
	if err != nil {
		return err
	}
	if err := enforceArtifactBound(layout); err != nil {
		return err
	}
	preserve, err := publicationGenerationReferences(layout, true)
	if err != nil {
		return err
	}
	rootChanged := false
	rootNames, err := directoryNames(opened.root)
	if err != nil {
		return err
	}
	for _, name := range rootNames {
		if !manifestTempPattern.MatchString(name) {
			continue
		}
		if err := opened.backend.remove(opened.root, directoryRoot, name, point); err != nil {
			return err
		}
		rootChanged = true
	}
	generationChanged := false
	names, err := directoryNames(opened.generations)
	if err != nil {
		return err
	}
	for _, name := range names {
		_, isGeneration := parseGenerationName(name)
		_, isPreserved := preserve[name]
		if !generationTempPattern.MatchString(name) && (!isGeneration || isPreserved) {
			continue
		}
		if err := opened.backend.remove(opened.generations, directoryGenerations, name, point); err != nil {
			return err
		}
		generationChanged = true
	}
	fsyncPoint := pointPreMaintenanceFsync
	if point == pointPostMaintenanceRemove {
		fsyncPoint = pointPostMaintenanceFsync
	}
	if rootChanged {
		if err := opened.backend.syncDirectory(opened.root, fsyncPoint, directoryRoot); err != nil {
			return err
		}
	}
	if generationChanged {
		if err := opened.backend.syncDirectory(opened.generations, fsyncPoint, directoryGenerations); err != nil {
			return err
		}
	}
	return nil
}

func commitFailure(result outcome, operation string, cause error) commitResult {
	err := newStoreError(result, operation, cause)
	return commitResult{outcome: result, err: err}
}
