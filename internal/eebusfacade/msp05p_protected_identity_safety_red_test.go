package eebusfacade

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	eebusapi "github.com/Project-Helianthus/helianthus-eebus-go/api"
)

func TestMSP05PProtectedRuntimeMaterialUsesOnlyTheProtectedStoreTree(t *testing.T) {
	stateRoot := filepath.Join(canonicalRuntimeTempDir(t), "state")
	if _, err := loadProtectedRuntimeMaterial(context.Background(), stateRoot); err != nil {
		t.Fatalf("bootstrap protected material: %v", err)
	}

	allowedRoot := map[string]bool{
		"LOCK": true, "MANIFEST.A": true, "MANIFEST.B": true, "generations": true,
	}
	err := filepath.WalkDir(stateRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if path == stateRoot || entry.IsDir() {
			if info.Mode().Perm() != 0o700 {
				t.Fatalf("protected directory %q mode = %04o, want 0700", path, info.Mode().Perm())
			}
			return nil
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("protected object %q mode = %v, want regular 0600", path, info.Mode())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !allowedRoot[entry.Name()] {
			t.Fatalf("protected loader created parallel state object %q", entry.Name())
		}
	}
}

func TestMSP05PProtectedRuntimeMaterialRejectsUnsafeRootsWithRedactedDeterministicErrors(t *testing.T) {
	parent := canonicalRuntimeTempDir(t)
	secret := "msp05p-private-fixture-value"
	tests := map[string]func(*testing.T) string{
		"symlink": func(t *testing.T) string {
			target := filepath.Join(parent, "symlink-target")
			if err := os.Mkdir(target, 0o700); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(parent, "symlink-root")
			if err := os.Symlink(target, root); err != nil {
				t.Fatal(err)
			}
			return root
		},
		"regular file": func(t *testing.T) string {
			root := filepath.Join(parent, "regular-root")
			if err := os.WriteFile(root, []byte(secret), 0o600); err != nil {
				t.Fatal(err)
			}
			return root
		},
		"fifo": func(t *testing.T) string {
			root := filepath.Join(parent, "fifo-root")
			if err := syscall.Mkfifo(root, 0o600); err != nil {
				t.Fatal(err)
			}
			return root
		},
		"weak permissions": func(t *testing.T) string {
			root := filepath.Join(parent, "weak-root")
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(root, 0o750); err != nil {
				t.Fatal(err)
			}
			return root
		},
	}
	for name, prepare := range tests {
		t.Run(name, func(t *testing.T) {
			root := prepare(t)
			first := msp05pLoadError(t, root)
			second := msp05pLoadError(t, root)
			if first != second {
				t.Fatalf("unsafe-root errors differ: first=%q second=%q", first, second)
			}
			if strings.Contains(first, root) || strings.Contains(first, secret) {
				t.Fatalf("unsafe-root error exposes protected input: %q", first)
			}
		})
	}
}

func TestMSP05PProtectedRuntimeMaterialRejectsWeakProtectedFile(t *testing.T) {
	stateRoot := filepath.Join(canonicalRuntimeTempDir(t), "state")
	if _, err := loadProtectedRuntimeMaterial(context.Background(), stateRoot); err != nil {
		t.Fatalf("bootstrap protected material: %v", err)
	}
	lockPath := filepath.Join(stateRoot, "LOCK")
	if err := os.Chmod(lockPath, 0o640); err != nil {
		t.Fatal(err)
	}
	msp05pLoadError(t, stateRoot)
}

func TestMSP05PClonedStateFailsBeforeServiceConstruction(t *testing.T) {
	root := canonicalRuntimeTempDir(t)
	stateRoot := filepath.Join(root, "state")
	material, err := loadProtectedRuntimeMaterial(context.Background(), stateRoot)
	if err != nil {
		t.Fatalf("bootstrap protected material: %v", err)
	}
	cloneRoot := filepath.Join(root, "clone")
	copyMSP05PStoreTree(t, stateRoot, cloneRoot)

	serviceConstructions := 0
	dependencies := defaultRuntimeDependencies
	dependencies.newService = func(RuntimeConfig, runtimeMaterial, eebusapi.ServiceReaderInterface) (runtimeService, error) {
		serviceConstructions++
		return nil, errors.New("network-effect service spy must not be called")
	}
	dependencies.now = time.Now
	_, err = acquireRuntime(context.Background(), RuntimeConfig{
		StateRoot: cloneRoot, Interface: "fixture-interface", ListenPort: 4711,
		Remotes: []RuntimeRemote{{SKI: "2222222222222222222222222222222222222222", Allowlisted: true}},
	}, dependencies)
	if !errors.Is(err, errProtectedRuntimeCredentials) {
		t.Fatalf("cloned state error = %v, want protected-credentials failure", err)
	}
	if serviceConstructions != 0 {
		t.Fatalf("cloned state constructed network-capable service %d times", serviceConstructions)
	}
	if text := err.Error(); strings.Contains(text, cloneRoot) || strings.Contains(text, stateRoot) || strings.Contains(text, material.localSKI) {
		t.Fatalf("cloned state error exposes protected identity or paths: %q", text)
	}
}

func msp05pLoadError(t *testing.T, stateRoot string) string {
	t.Helper()
	_, err := loadProtectedRuntimeMaterial(context.Background(), stateRoot)
	if err == nil {
		t.Fatal("unsafe protected state unexpectedly loaded")
	}
	return err.Error()
}

func copyMSP05PStoreTree(t *testing.T, source, target string) {
	t.Helper()
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Mkdir(destination, info.Mode().Perm())
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		defer input.Close()
		output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, input)
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		t.Fatalf("copy protected store tree: %v", err)
	}
}
