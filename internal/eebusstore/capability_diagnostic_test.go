//go:build linux || darwin

package eebusstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCapabilityDiagnosticInjectedFailures(t *testing.T) {
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
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	for _, test := range tests {
		t.Run(test.want, func(t *testing.T) {
			logs.Reset()
			root := filepath.Join(t.TempDir(), "store")
			config := testStoreConfig(t, root, func(call syscallCall) error {
				if call.point == test.point {
					return fmt.Errorf("synthetic-path/synthetic-argv/synthetic-env/synthetic-key: %w", errors.New("synthetic nested cause"))
				}
				return nil
			}, nil)
			result := openStore(config)
			assertOutcome(t, result.outcome, outcomeFilesystemCapabilityUnavailable)
			if result.store != nil || result.state != nil {
				t.Fatal("failed preflight admitted state")
			}
			if got := filesystemCapabilityDiagnostic(result.err); got != test.want {
				t.Fatalf("diagnostic = %q, want %q", got, test.want)
			}
			bridge := &AssociationBridge{config: config}
			_, state, outcome := bridge.Reload(context.Background())
			if state != nil || outcome != string(outcomeFilesystemCapabilityUnavailable) {
				t.Fatal("bridge did not preserve fail-closed outcome")
			}
			var entry map[string]any
			if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
				t.Fatalf("diagnostic log: %v", err)
			}
			if entry["capability"] != test.want || entry["msg"] != string(outcomeFilesystemCapabilityUnavailable) {
				t.Fatalf("unexpected diagnostic log: %s", logs.Bytes())
			}
			for _, secret := range []string{root, "synthetic-path", "synthetic-argv", "synthetic-env", "synthetic-key", "synthetic nested cause"} {
				if strings.Contains(logs.String()+fmt.Sprintf("%v %+v %#v %q", result.err, result.err, result.err, result.err), secret) {
					t.Fatal("diagnostic exposed synthetic sensitive data")
				}
			}
		})
	}
}

func TestCapabilityDiagnosticUnknownAndNonProbeOperations(t *testing.T) {
	for operation, want := range map[string]string{
		"select_backend": "backend_selection", "root_identity": "stable_identity", "lock_identity": "stable_identity",
		"stat_object": "stable_identity", "stat_created_object": "stable_identity", "resume_bootstrap": "stable_identity",
		"inspect_acl": "acl_inspection", "random_temp_name": "temporary_name", "probe_create": "file_create",
		"probe_close": "file_close", "probe_file_write": "file_write", "synthetic-private-operation": "unknown",
	} {
		err := newStoreError(outcomeFilesystemCapabilityUnavailable, operation, errors.New("synthetic-private-cause"))
		if got := filesystemCapabilityDiagnostic(err); got != want {
			t.Errorf("%s diagnostic = %q, want %q", operation, got, want)
		}
		if want == "unknown" && strings.Contains(err.Error(), operation) {
			t.Error("unknown operation escaped")
		}
		if errors.Unwrap(err) != nil {
			t.Error("cause retained")
		}
	}
	for _, err := range []error{nil, errors.New("probe_acl synthetic-private-cause"), newStoreError(outcomeIOFailed, "probe_acl", nil), (*storeError)(nil)} {
		if got := filesystemCapabilityDiagnostic(err); got != "unknown" {
			t.Errorf("unclassified diagnostic = %q", got)
		}
	}
}

func TestCapabilityDiagnosticSuccessfulBridgeUnchanged(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	bridge, outcome := OpenAssociationBridge(filepath.Join(t.TempDir(), "store"), nil)
	if bridge == nil || outcome != string(outcomeOpenedEmpty) {
		t.Fatalf("open outcome = %s", outcome)
	}
	t.Cleanup(func() {
		if err := bridge.Close(); err != nil {
			t.Error(err)
		}
	})
	_, _, outcome = bridge.Reload(context.Background())
	if outcome != string(outcomeOpenedCurrent) || logs.Len() != 0 {
		t.Fatalf("reload outcome = %s, logs = %s", outcome, logs.Bytes())
	}
}

func TestCapabilityDiagnosticCleanupPrecedencePreservesPublishedState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	created := openForTest(t, root, nil, nil)
	assertOutcome(t, created.outcome, outcomeOpenedEmpty)
	closeStore(t, created)
	snapshot := func() map[string]string {
		t.Helper()
		files := make(map[string]string)
		paths, err := filepath.Glob(filepath.Join(root, "MANIFEST.*"))
		if err != nil {
			t.Fatal(err)
		}
		generations, err := filepath.Glob(filepath.Join(root, "generations", "*"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range append(paths, generations...) {
			payload, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			files[path] = string(payload)
		}
		return files
	}
	before := snapshot()
	result := openForTest(t, root, func(call syscallCall) error {
		if call.point == pointCapabilityFileFsync || call.point == pointCapabilityCleanup {
			return errors.New("synthetic-private-cause")
		}
		return nil
	}, nil)
	assertOutcome(t, result.outcome, outcomeFilesystemCapabilityUnavailable)
	if result.store != nil || result.state != nil || filesystemCapabilityDiagnostic(result.err) != "cleanup" {
		t.Fatal("cleanup failure did not take precedence and fail closed")
	}
	after := snapshot()
	if len(before) != len(after) {
		t.Fatal("published file set changed")
	}
	for path, payload := range before {
		if after[path] != payload {
			t.Fatal("published bytes changed")
		}
	}
	reopened := openForTest(t, root, nil, nil)
	defer closeStore(t, reopened)
	assertOutcome(t, reopened.outcome, outcomeOpenedCurrent)
}

func TestCapabilityDiagnosticReadOnlyDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can bypass directory write permissions")
	}
	root := filepath.Join(t.TempDir(), "synthetic-private-directory")
	if err := os.Mkdir(root, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(root, 0o700); err != nil {
			t.Error(err)
		}
	})
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	probe := capabilityProbe{}
	err = probe.probeDirectory(directory, directoryRoot, ".synthetic-private-prefix-", false)
	assertOutcome(t, outcomeOf(err), outcomeFilesystemCapabilityUnavailable)
	if filesystemCapabilityDiagnostic(err) != "file_create" || len(probe.artifacts) != 0 {
		t.Fatal("read-only directory did not produce the bounded file-create diagnostic")
	}
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	logFilesystemCapabilityFailure(err)
	var entry map[string]any
	if decodeErr := json.Unmarshal(logs.Bytes(), &entry); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if len(entry) != 4 || entry["capability"] != "file_create" || entry["msg"] != string(outcomeFilesystemCapabilityUnavailable) || entry["level"] != "WARN" {
		t.Fatal("unexpected capability diagnostic fields")
	}
	for _, private := range []string{root, "synthetic-private-directory", ".synthetic-private-prefix-", "permission denied"} {
		if strings.Contains(logs.String()+fmt.Sprintf("%v %+v %#v %q", err, err, err, err), private) {
			t.Fatal("read-only diagnostic exposed private filesystem details")
		}
	}
}
