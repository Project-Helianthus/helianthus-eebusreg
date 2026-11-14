//go:build linux || darwin

package eebusstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// filesystemCapabilityGap is the typed result of a protected-store preflight
// that ended in filesystem_capability_unavailable. capability carries the
// bounded category from filesystemCapabilityDiagnostic and nothing else, so
// the skip reason never contains a path, nested cause or store content.
type filesystemCapabilityGap struct {
	capability string
}

func (gap filesystemCapabilityGap) skipReason() string {
	return fmt.Sprintf(
		"protected store filesystem preflight reported %s: missing capability %s (see the protected-store host requirements in README.md)",
		outcomeFilesystemCapabilityUnavailable,
		gap.capability,
	)
}

// capabilityGate is the subset of testing.TB the preflight gate needs; tests
// substitute a recorder to observe the skip or failure decision.
type capabilityGate interface {
	Helper()
	Skip(args ...any)
	Fatal(args ...any)
}

// probeFilesystemCapabilities opens a fresh store in a private directory
// created under base and removes it again. A capable filesystem yields no gap;
// a missing capability yields its named category; any other outcome or a
// setup failure is an error so that it fails the caller instead of skipping.
func probeFilesystemCapabilities(base string, hook syscallHook) (gap *filesystemCapabilityGap, err error) {
	directory, err := os.MkdirTemp(base, "preflight-")
	if err != nil {
		return nil, errors.New("create preflight directory failed")
	}
	defer func() {
		if removeErr := os.RemoveAll(directory); removeErr != nil && err == nil {
			gap, err = nil, errors.New("remove preflight directory failed")
		}
	}()
	backend, err := newNativeSyscallBackend(hook)
	if err != nil {
		return nil, errors.New("native backend unavailable")
	}
	result := openStore(storeConfig{root: filepath.Join(directory, "store"), backend: backend})
	if result.store != nil {
		if closeErr := result.store.close(); closeErr != nil {
			return nil, errors.New("close preflight store failed")
		}
	}
	switch result.outcome {
	case outcomeOpenedEmpty:
		return nil, nil
	case outcomeFilesystemCapabilityUnavailable:
		return &filesystemCapabilityGap{capability: filesystemCapabilityDiagnostic(result.err)}, nil
	default:
		return nil, fmt.Errorf("unexpected preflight outcome %s", result.outcome)
	}
}

func skipOnFilesystemCapabilityGap(gate capabilityGate, gap *filesystemCapabilityGap, err error) {
	gate.Helper()
	if err != nil {
		gate.Fatal("protected store filesystem preflight could not run: ", err)
		return
	}
	if gap != nil {
		gate.Skip(gap.skipReason())
	}
}

// storeFilesystemPreflight probes the filesystem behind os.TempDir, where
// every t.TempDir store root lives, once per test process.
var storeFilesystemPreflight = sync.OnceValues(func() (*filesystemCapabilityGap, error) {
	return probeFilesystemCapabilities(os.TempDir(), nil)
})

// requireStoreFilesystemCapabilities skips the calling test with the named
// missing capability when the host filesystem cannot hold a protected store.
func requireStoreFilesystemCapabilities(gate capabilityGate) {
	gate.Helper()
	gap, err := storeFilesystemPreflight()
	skipOnFilesystemCapabilityGap(gate, gap, err)
}

type recordingCapabilityGate struct {
	skips  []string
	fatals []string
}

func (gate *recordingCapabilityGate) Helper() {}

func (gate *recordingCapabilityGate) Skip(args ...any) {
	gate.skips = append(gate.skips, fmt.Sprint(args...))
}

func (gate *recordingCapabilityGate) Fatal(args ...any) {
	gate.fatals = append(gate.fatals, fmt.Sprint(args...))
}

func TestFilesystemPreflightGapNamesTheMissingCapability(t *testing.T) {
	requireStoreFilesystemCapabilities(t)
	tests := []struct {
		point syscallPoint
		want  string
	}{
		{pointCapabilityBackendPolicy, "backend_policy"},
		{pointCapabilityStableIdentity, "stable_identity"},
		{pointCapabilityAtomicReplacement, "atomic_replacement"},
		{pointCapabilityProcessLock, "process_lock"},
		{pointCapabilityNoFollowCreate, "nofollow_create"},
		{pointCapabilityFileFsync, "file_fsync"},
		{pointCapabilityDirectoryFsync, "directory_fsync"},
		{pointCapabilityACL, "acl_inspection"},
		{pointCapabilityCleanup, "cleanup"},
	}
	for _, test := range tests {
		t.Run(test.want, func(t *testing.T) {
			base := filepath.Join(t.TempDir(), "synthetic-private-base")
			if err := os.Mkdir(base, 0o700); err != nil {
				t.Fatal(err)
			}
			gap, err := probeFilesystemCapabilities(base, func(call syscallCall) error {
				if call.point == test.point {
					return errors.New("synthetic-private-cause")
				}
				return nil
			})
			if err != nil {
				t.Fatalf("preflight setup failed: %v", err)
			}
			if gap == nil || gap.capability != test.want {
				t.Fatalf("gap = %+v, want capability %q", gap, test.want)
			}
			gate := &recordingCapabilityGate{}
			skipOnFilesystemCapabilityGap(gate, gap, nil)
			if len(gate.fatals) != 0 || len(gate.skips) != 1 {
				t.Fatalf("gate skips = %q, fatals = %q", gate.skips, gate.fatals)
			}
			reason := gate.skips[0]
			if !strings.Contains(reason, "capability "+test.want+" ") || !strings.Contains(reason, string(outcomeFilesystemCapabilityUnavailable)) {
				t.Fatalf("skip reason %q does not name capability %q", reason, test.want)
			}
			for _, private := range []string{base, "synthetic-private-base", "synthetic-private-cause"} {
				if strings.Contains(reason, private) {
					t.Fatal("skip reason exposed private preflight details")
				}
			}
			assertPreflightBaseEmpty(t, base)
		})
	}
}

func TestFilesystemPreflightCapableHostDoesNotSkip(t *testing.T) {
	requireStoreFilesystemCapabilities(t)
	base := t.TempDir()
	gap, err := probeFilesystemCapabilities(base, nil)
	if err != nil || gap != nil {
		t.Fatalf("capable preflight = %+v, %v", gap, err)
	}
	gate := &recordingCapabilityGate{}
	skipOnFilesystemCapabilityGap(gate, gap, err)
	if len(gate.skips) != 0 || len(gate.fatals) != 0 {
		t.Fatalf("capable host gated: skips = %q, fatals = %q", gate.skips, gate.fatals)
	}
	assertPreflightBaseEmpty(t, base)
}

func TestFilesystemPreflightSetupFailureFailsInsteadOfSkipping(t *testing.T) {
	base := filepath.Join(t.TempDir(), "synthetic-missing-base")
	gap, err := probeFilesystemCapabilities(base, nil)
	if err == nil || gap != nil {
		t.Fatalf("missing base preflight = %+v, %v", gap, err)
	}
	gate := &recordingCapabilityGate{}
	skipOnFilesystemCapabilityGap(gate, gap, err)
	if len(gate.skips) != 0 || len(gate.fatals) != 1 {
		t.Fatalf("setup failure gated: skips = %q, fatals = %q", gate.skips, gate.fatals)
	}
	if strings.Contains(gate.fatals[0], base) {
		t.Fatal("setup failure exposed the preflight base")
	}
}

func TestFilesystemPreflightGapSkipsDependentStoreTests(t *testing.T) {
	previous := storeFilesystemPreflight
	t.Cleanup(func() { storeFilesystemPreflight = previous })
	for _, capability := range []string{"acl_inspection", "backend_policy", "directory_fsync"} {
		storeFilesystemPreflight = func() (*filesystemCapabilityGap, error) {
			return &filesystemCapabilityGap{capability: capability}, nil
		}
		var dependent *testing.T
		reached := false
		t.Run(capability, func(t *testing.T) {
			dependent = t
			_ = testStoreConfig(t, filepath.Join(t.TempDir(), "store"), nil, nil)
			reached = true
		})
		if dependent == nil || !dependent.Skipped() || reached {
			t.Fatalf("dependent store test was not skipped for missing %s", capability)
		}
	}
}

func assertPreflightBaseEmpty(t *testing.T, base string) {
	t.Helper()
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("preflight left %d entries behind", len(entries))
	}
}
