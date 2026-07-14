package eebusstore

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

func TestRootPathMustBeAbsoluteCleanAndNative(t *testing.T) {
	parent := t.TempDir()
	tests := map[string]string{
		"empty":             "",
		"relative":          "store",
		"dot component":     parent + string(os.PathSeparator) + "." + string(os.PathSeparator) + "store",
		"dot-dot component": parent + string(os.PathSeparator) + "child" + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "store",
		"embedded NUL":      parent + string(os.PathSeparator) + "store\x00suffix",
	}
	if os.PathSeparator == '/' {
		tests["non-native separator"] = parent + `\store`
	}

	for name, root := range tests {
		t.Run(name, func(t *testing.T) {
			result := openForTest(t, root, nil, nil)
			assertOutcome(t, result.outcome, outcomePathRejected)
			if result.store != nil || result.state != nil {
				t.Fatal("rejected path returned active state")
			}
		})
	}
}

func TestBootstrapCreatesOnlyFixedPrivateRegularLayout(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	result := openForTest(t, root, nil, nil)
	defer closeStore(t, result)
	assertOutcome(t, result.outcome, outcomeOpenedEmpty)

	assertObjectMode(t, root, os.ModeDir|0o700)
	assertObjectMode(t, filepath.Join(root, "generations"), os.ModeDir|0o700)
	assertObjectMode(t, filepath.Join(root, "LOCK"), 0o600)
	lockBytes, err := os.ReadFile(filepath.Join(root, "LOCK"))
	if err != nil {
		t.Fatal(err)
	}
	if len(lockBytes) != 0 {
		t.Fatalf("LOCK contains %d bytes, want persistent empty file", len(lockBytes))
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	manifestCount := 0
	for _, entry := range entries {
		switch entry.Name() {
		case "LOCK", "generations":
		case "MANIFEST.A", "MANIFEST.B":
			manifestCount++
			assertObjectMode(t, filepath.Join(root, entry.Name()), 0o600)
		default:
			t.Fatalf("bootstrap created unknown root entry %q", entry.Name())
		}
	}
	if manifestCount != 1 {
		t.Fatalf("bootstrap manifest count = %d, want exactly one published slot", manifestCount)
	}
	generations, err := os.ReadDir(filepath.Join(root, "generations"))
	if err != nil {
		t.Fatal(err)
	}
	if len(generations) != 1 || generations[0].Name() != testGenerationFilename(1) {
		t.Fatalf("bootstrap generations = %v, want only generation 1", testDirectoryNames(generations))
	}
	assertObjectMode(t, filepath.Join(root, "generations", testGenerationFilename(1)), 0o600)
}

func TestFilesystemRejectsSymlinkHardLinkWrongModeAndUnknownEntry(t *testing.T) {
	tests := map[string]func(*testing.T, string){
		"generation symlink": func(t *testing.T, root string) {
			generation := filepath.Join(root, "generations", testGenerationFilename(1))
			target := filepath.Join(t.TempDir(), "target")
			payload, err := os.ReadFile(generation)
			if err != nil {
				t.Fatal(err)
			}
			testWritePrivateFile(t, target, payload)
			if err := os.Remove(generation); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, generation); err != nil {
				t.Fatal(err)
			}
		},
		"generation hard link": func(t *testing.T, root string) {
			generation := filepath.Join(root, "generations", testGenerationFilename(1))
			if err := os.Link(generation, filepath.Join(t.TempDir(), "alias")); err != nil {
				t.Fatal(err)
			}
		},
		"generation broader mode": func(t *testing.T, root string) {
			if err := os.Chmod(filepath.Join(root, "generations", testGenerationFilename(1)), 0o640); err != nil {
				t.Fatal(err)
			}
		},
		"unknown root entry": func(t *testing.T, root string) {
			testWritePrivateFile(t, filepath.Join(root, "identity-derived-name"), nil)
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "store")
			created := openForTest(t, root, nil, nil)
			assertOutcome(t, created.outcome, outcomeOpenedEmpty)
			closeStore(t, created)
			mutate(t, root)

			result := openForTest(t, root, nil, nil)
			if name == "generation broader mode" {
				assertOutcome(t, result.outcome, outcomePermissionsRejected)
			} else {
				assertOutcome(t, result.outcome, outcomeLayoutRejected)
			}
			if result.store != nil || result.state != nil {
				t.Fatal("unsafe layout returned active state")
			}
		})
	}
}

func TestRootSymlinkAndUnavailableACLProbeFailClosed(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	rootLink := filepath.Join(parent, "store")
	if err := os.Symlink(target, rootLink); err != nil {
		t.Fatal(err)
	}
	result := openForTest(t, rootLink, nil, nil)
	assertOutcome(t, result.outcome, outcomePathRejected)

	root := filepath.Join(t.TempDir(), "store")
	hook := func(call syscallCall) error {
		if call.point == pointCapabilityACL {
			return errors.New("synthetic unavailable ACL proof")
		}
		return nil
	}
	result = openForTest(t, root, hook, nil)
	assertOutcome(t, result.outcome, outcomeFilesystemCapabilityUnavailable)
	for _, slot := range []string{"MANIFEST.A", "MANIFEST.B"} {
		if _, err := os.Lstat(filepath.Join(root, slot)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("capability failure created %s: %v", slot, err)
		}
	}
}

func TestMissingLockInExistingStoreIsNeverRecreated(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	created := openForTest(t, root, nil, nil)
	assertOutcome(t, created.outcome, outcomeOpenedEmpty)
	closeStore(t, created)
	lockPath := filepath.Join(root, "LOCK")
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}

	result := openForTest(t, root, nil, nil)
	assertOutcome(t, result.outcome, outcomeLayoutRejected)
	if _, err := os.Lstat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("open recreated missing LOCK around existing state: %v", err)
	}
}

func TestInProcessWriterBusyPrecedesDeepLayoutParsing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	first := openForTest(t, root, nil, nil)
	assertOutcome(t, first.outcome, outcomeOpenedEmpty)
	testWritePrivateFile(t, filepath.Join(root, "unknown-deep-entry"), []byte("must remain untouched"))

	second := openForTest(t, root, nil, nil)
	assertOutcome(t, second.outcome, outcomeWriterBusy)
	if second.store != nil || second.state != nil {
		t.Fatal("second in-process open returned active state")
	}
	if payload, err := os.ReadFile(filepath.Join(root, "unknown-deep-entry")); err != nil {
		t.Fatal(err)
	} else if !bytes.Equal(payload, []byte("must remain untouched")) {
		t.Fatal("busy loser parsed or mutated deep layout")
	}
	closeStore(t, first)

	third := openForTest(t, root, nil, nil)
	assertOutcome(t, third.outcome, outcomeLayoutRejected)
}

func TestSubprocessContentionAndProcessExitLockRelease(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("MSP-04A native lock contract is Linux/Darwin only")
	}
	root := filepath.Join(t.TempDir(), "store")
	created := openForTest(t, root, nil, nil)
	assertOutcome(t, created.outcome, outcomeOpenedEmpty)
	closeStore(t, created)

	holder, stdin, stdout := startLockHelper(t)
	if _, err := io.WriteString(stdin, root+"\n"); err != nil {
		t.Fatal(err)
	}
	status, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("read holder status: %v", err)
	}
	if strings.TrimSpace(status) != string(outcomeOpenedCurrent) {
		t.Fatalf("holder status = %q, want %q", status, outcomeOpenedCurrent)
	}

	contender := exec.Command(os.Args[0], "-test.run=^TestStoreLockHelperProcess$", "--")
	contender.Env = append(os.Environ(), "HELIANTHUS_STORE_LOCK_HELPER=1")
	contender.Stdin = strings.NewReader(root + "\n")
	output, err := contender.CombinedOutput()
	if err != nil {
		t.Fatalf("contender process: %v: %s", err, output)
	}
	if strings.TrimSpace(string(output)) != string(outcomeWriterBusy) {
		t.Fatalf("contender status = %q, want %q", output, outcomeWriterBusy)
	}

	if err := holder.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := holder.Wait(); err == nil {
		t.Fatal("killed holder exited successfully")
	}
	_ = stdin.Close()

	lockPath := filepath.Join(root, "LOCK")
	if info, err := os.Lstat(lockPath); err != nil {
		t.Fatalf("LOCK removed after process exit: %v", err)
	} else if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("LOCK after process exit has mode %s", info.Mode())
	}
	if payload, err := os.ReadFile(lockPath); err != nil {
		t.Fatal(err)
	} else if len(payload) != 0 {
		t.Fatalf("LOCK contains stale ownership metadata: %q", payload)
	}

	reopened := openForTest(t, root, nil, nil)
	defer closeStore(t, reopened)
	assertOutcome(t, reopened.outcome, outcomeOpenedCurrent)
}

func TestStoreLockHelperProcess(t *testing.T) {
	if os.Getenv("HELIANTHUS_STORE_LOCK_HELPER") != "1" {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	root, err := reader.ReadString('\n')
	if err != nil {
		fmt.Fprintln(os.Stdout, "helper_input_failed")
		return
	}
	result := openForTest(t, strings.TrimSuffix(root, "\n"), nil, nil)
	fmt.Fprintln(os.Stdout, result.outcome)
	if result.store == nil {
		return
	}
	_, _ = io.Copy(io.Discard, reader)
	closeStore(t, result)
}

func startLockHelper(t *testing.T) (*exec.Cmd, io.WriteCloser, io.ReadCloser) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestStoreLockHelperProcess$", "--")
	cmd.Env = append(os.Environ(), "HELIANTHUS_STORE_LOCK_HELPER=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd, stdin, stdout
}

func assertObjectMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode() != want {
		t.Fatalf("%s mode = %s, want %s", filepath.Base(path), info.Mode(), want)
	}
	if info.Mode().IsRegular() {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			t.Fatalf("%s has unsupported stat type %T", filepath.Base(path), info.Sys())
		}
		if stat.Nlink != 1 {
			t.Fatalf("%s link count = %d, want 1", filepath.Base(path), stat.Nlink)
		}
	}
}

func testDirectoryNames(entries []os.DirEntry) []string {
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	return names
}
