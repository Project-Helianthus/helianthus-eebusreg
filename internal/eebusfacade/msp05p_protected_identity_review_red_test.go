//go:build linux || darwin

package eebusfacade

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMSP05PProtectedIdentityReviewDurableAnchorSurvivesProviderRestart(t *testing.T) {
	stateRoot := filepath.Join(canonicalRuntimeTempDir(t), "state")
	if _, err := loadProtectedRuntimeMaterial(context.Background(), stateRoot); err != nil {
		t.Fatalf("bootstrap protected material: %v", err)
	}

	storeInstance := msp05pReviewDigest("store-instance")
	provider1, err := newNativeProtectedIdentityProvider(stateRoot)
	if err != nil {
		t.Fatalf("construct first native provider: %v", err)
	}
	created, outcome := provider1.Create(context.Background(), firstTrustAnchorVersion, storeInstance)
	if outcome != "anchor_durable" {
		t.Fatalf("create anchor outcome = %q, want anchor_durable", outcome)
	}
	pending := msp05pReviewPendingPublication(storeInstance)
	if outcome := provider1.CompareAndStage(context.Background(), created, pending); outcome != "anchor_durable" {
		t.Fatalf("stage anchor outcome = %q, want anchor_durable", outcome)
	}

	provider2, err := newNativeProtectedIdentityProvider(stateRoot)
	if err != nil {
		t.Fatalf("construct restarted native provider: %v", err)
	}
	opened, outcome := provider2.Open(context.Background())
	if outcome != "opened_anchor" {
		t.Fatalf("restarted anchor open outcome = %q, want opened_anchor", outcome)
	}
	if opened.pending == nil || !firstTrustPendingPublicationEqual(*opened.pending, pending) {
		t.Fatal("restarted anchor did not retain the staged pending publication")
	}
	if outcome := provider2.CompareAndFinalize(context.Background(), pending); outcome != "anchor_durable" {
		t.Fatalf("finalize restarted anchor outcome = %q, want anchor_durable", outcome)
	}

	provider3, err := newNativeProtectedIdentityProvider(stateRoot)
	if err != nil {
		t.Fatalf("construct finalized native provider: %v", err)
	}
	opened, outcome = provider3.Open(context.Background())
	if outcome != "opened_anchor" {
		t.Fatalf("finalized anchor open outcome = %q, want opened_anchor", outcome)
	}
	if opened.pending != nil {
		t.Fatal("finalized anchor retained a pending publication")
	}
	if opened.manifestGenerationHighWater != pending.targetManifest.current.sequence ||
		opened.controlEpochHighWater != pending.targetControlEpoch {
		t.Fatal("finalized anchor did not retain target high-water marks")
	}
}

func TestMSP05PProtectedIdentityReviewSamePathDirectoryRecreationRetainsIdentity(t *testing.T) {
	root := canonicalRuntimeTempDir(t)
	stateRoot := filepath.Join(root, "state")
	first, err := loadProtectedRuntimeMaterial(context.Background(), stateRoot)
	if err != nil {
		t.Fatalf("bootstrap protected material: %v", err)
	}
	firstCertificate := bytes.Clone(first.certificate.Certificate[0])
	firstSKI := first.localSKI

	replacement := filepath.Join(root, "replacement")
	msp05pReviewCopyStateTree(t, stateRoot, replacement)
	if err := os.Rename(stateRoot, filepath.Join(root, "original")); err != nil {
		t.Fatalf("move original protected state aside: %v", err)
	}
	if err := os.Rename(replacement, stateRoot); err != nil {
		t.Fatalf("replace protected state at same path: %v", err)
	}

	recreated, err := loadProtectedRuntimeMaterial(context.Background(), stateRoot)
	if err != nil {
		t.Fatalf("recreated same-path protected state did not reload: %v", err)
	}
	if recreated.localSKI != firstSKI {
		t.Fatal("recreated same-path protected state changed the local SKI")
	}
	if !bytes.Equal(recreated.certificate.Certificate[0], firstCertificate) {
		t.Fatal("recreated same-path protected state changed the leaf certificate")
	}
}

func TestMSP05PProtectedIdentityReviewInPlaceRollbackFailsClosed(t *testing.T) {
	root := canonicalRuntimeTempDir(t)
	stateRoot := filepath.Join(root, "state")
	material, err := loadProtectedRuntimeMaterial(context.Background(), stateRoot)
	if err != nil {
		t.Fatalf("bootstrap protected material: %v", err)
	}
	snapshot := filepath.Join(root, "snapshot")
	msp05pReviewCopyStateTree(t, stateRoot, snapshot)
	stateRootInfo, err := os.Lstat(stateRoot)
	if err != nil {
		t.Fatalf("inspect protected state root: %v", err)
	}

	resources := acquireMSP05PTrustResources(t, stateRoot, filepath.Join(root, "admin"), material)
	remoteSKI := "3333333333333333333333333333333333333333"
	pairRuntimeRemote(t, resources, remoteSKI, 5_005)
	if err := resources.Close(); err != nil {
		t.Fatalf("close paired runtime resources: %v", err)
	}
	paired, err := loadProtectedRuntimeMaterial(context.Background(), stateRoot)
	if err != nil {
		t.Fatalf("reload paired protected material: %v", err)
	}
	if !paired.pretrusted[remoteSKI] {
		t.Fatal("paired protected material did not retain remote admission")
	}

	msp05pReviewReplaceStateTreeContents(t, snapshot, stateRoot)
	restoredRootInfo, err := os.Lstat(stateRoot)
	if err != nil {
		t.Fatalf("inspect restored protected state root: %v", err)
	}
	if !os.SameFile(stateRootInfo, restoredRootInfo) {
		t.Fatal("rollback test replaced the protected state root directory")
	}
	if _, err := loadProtectedRuntimeMaterial(context.Background(), stateRoot); err == nil {
		t.Fatal("in-place protected-state rollback unexpectedly loaded")
	}
}

func TestMSP05PProtectedIdentityReviewRejectsVolatileBootIdentity(t *testing.T) {
	source, err := os.ReadFile("runtime_machine_identity_linux.go")
	if err != nil {
		t.Fatalf("read Linux runtime identity source: %v", err)
	}
	if strings.Contains(string(source), "/proc/sys/kernel/random/boot_id") || strings.Contains(string(source), "boot-id") {
		t.Fatal("Linux runtime identity permits a volatile boot identity")
	}
}

func msp05pReviewPendingPublication(storeInstance [sha256.Size]byte) firstTrustPendingPublication {
	return firstTrustPendingPublication{
		operationID:          msp05pReviewDigest("operation"),
		operationClass:       "review-publication",
		storeInstance:        storeInstance,
		previousControlEpoch: 41,
		targetControlEpoch:   42,
		previousManifest: firstTrustManifestBinding{
			epoch: 7, sha256: msp05pReviewDigest("previous-manifest"),
			current: firstTrustGenerationBinding{
				sequence: 17, filename: "GEN-000017", sha256: msp05pReviewDigest("previous-generation"), schemaVersion: 1,
			},
		},
		targetManifest: firstTrustManifestBinding{
			epoch: 8, sha256: msp05pReviewDigest("target-manifest"),
			current: firstTrustGenerationBinding{
				sequence: 18, filename: "GEN-000018", sha256: msp05pReviewDigest("target-generation"), schemaVersion: 1,
			},
		},
	}
}

func msp05pReviewDigest(label string) [sha256.Size]byte {
	return sha256.Sum256([]byte("msp05p-protected-identity-review/" + label))
}

func msp05pReviewCopyStateTree(t *testing.T, source, target string) {
	t.Helper()
	msp05pReviewCopyStateTreeInto(t, source, target, false)
}

func msp05pReviewReplaceStateTreeContents(t *testing.T, source, target string) {
	t.Helper()
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatalf("read protected state for rollback: %v", err)
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(target, entry.Name())); err != nil {
			t.Fatalf("remove protected state entry for rollback: %v", err)
		}
	}
	msp05pReviewCopyStateTreeInto(t, source, target, true)
}

func msp05pReviewCopyStateTreeInto(t *testing.T, source, target string, targetExists bool) {
	t.Helper()
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("protected state source contains symlink")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			if info.Mode().Perm() != 0o700 {
				return errors.New("protected state source directory has unexpected mode")
			}
			if relative == "." && targetExists {
				return os.Chmod(target, 0o700)
			}
			if err := os.Mkdir(destination, 0o700); err != nil {
				return err
			}
			return os.Chmod(destination, 0o700)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return errors.New("protected state source file has unexpected mode")
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			_ = input.Close()
			return err
		}
		_, copyErr := io.Copy(output, input)
		closeOutputErr := output.Close()
		closeInputErr := input.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeOutputErr != nil {
			return closeOutputErr
		}
		if closeInputErr != nil {
			return closeInputErr
		}
		return os.Chmod(destination, 0o600)
	})
	if err != nil {
		t.Fatalf("copy protected state tree: %v", err)
	}
}
