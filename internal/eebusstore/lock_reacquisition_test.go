package eebusstore

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestK10LockReacquisition(t *testing.T) {
	for _, cancelOwner := range []bool{false, true} {
		name := "graceful release"
		if cancelOwner {
			name = "cancelled owner"
		}
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "store")
			created := openForTest(t, root, nil, nil)
			defer closeStore(t, created)
			k10RequireSupported(t, created)
			assertOutcome(t, created.outcome, outcomeOpenedEmpty)
			closeStore(t, created)
			before := testSnapshotTree(t, root)

			first := k10StartOwner(t, root, "hold")
			first.expect(t, string(outcomeOpenedCurrent))
			contender := k10StartOwner(t, root, "commit")
			contender.expect(t, string(outcomeWriterBusy))
			contender.join(t, false)
			assertTreeEqual(t, testSnapshotTree(t, root), before)

			if cancelOwner {
				first.cancel()
			} else if err := first.input.Close(); err != nil {
				t.Fatal(err)
			}
			first.join(t, cancelOwner)
			assertTreeEqual(t, testSnapshotTree(t, root), before)

			second := k10StartOwner(t, root, "commit")
			second.expect(t, string(outcomeOpenedCurrent))
			second.expect(t, string(outcomeCommitDurable))
			second.join(t, false)
			reopened := openForTest(t, root, nil, nil)
			defer closeStore(t, reopened)
			assertOutcome(t, reopened.outcome, outcomeOpenedCurrent)
			if reopened.store.manifest.current.generation != 2 {
				t.Fatalf("generation = %d, want 2 after new owner's commit", reopened.store.manifest.current.generation)
			}
		})
	}
}

func TestK10CapabilityFailureReleasesOwnership(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	created := openForTest(t, root, nil, nil)
	defer closeStore(t, created)
	k10RequireSupported(t, created)
	assertOutcome(t, created.outcome, outcomeOpenedEmpty)
	closeStore(t, created)
	before := testSnapshotTree(t, root)
	var injected bool
	failed := openForTest(t, root, func(call syscallCall) error {
		if call.point == pointCapabilityProcessLock {
			injected = true
			return errors.New("synthetic unavailable process-lock capability")
		}
		return nil
	}, nil)
	defer closeStore(t, failed)
	assertOutcome(t, failed.outcome, outcomeFilesystemCapabilityUnavailable)
	if !injected || failed.store != nil || failed.state != nil {
		t.Fatal("capability failure did not fail closed at injected probe")
	}
	assertTreeEqual(t, testSnapshotTree(t, root), before)
	reopened := openForTest(t, root, nil, nil)
	defer closeStore(t, reopened)
	assertOutcome(t, reopened.outcome, outcomeOpenedCurrent)
}

func k10RequireSupported(t *testing.T, result openResult) {
	t.Helper()
	if result.outcome == outcomeFilesystemCapabilityUnavailable || result.outcome == outcomeLockUnavailable {
		t.Fatalf("UNSUPPORTED runner filesystem: real store lifecycle unverified (outcome %s)", result.outcome)
	}
}

func TestK10OwnerHelper(t *testing.T) {
	if os.Getenv("HELIANTHUS_K10_OWNER") != "1" {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	root, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	action, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	result := openForTest(t, strings.TrimSuffix(root, "\n"), nil, nil)
	defer closeStore(t, result)
	fmt.Fprintln(os.Stdout, result.outcome)
	if result.store == nil {
		if result.state != nil {
			t.Fatal("failed acquisition returned state")
		}
		return
	}
	switch strings.TrimSpace(action) {
	case "hold":
		if _, err := io.Copy(io.Discard, reader); err != nil {
			t.Fatal(err)
		}
	case "commit":
		committed := result.store.commit(cloneStateV1(*result.state))
		fmt.Fprintln(os.Stdout, committed.outcome)
		assertOutcome(t, committed.outcome, outcomeCommitDurable)
	default:
		t.Fatal("unknown helper action")
	}
}

type k10Owner struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	input  io.WriteCloser
	output *bufio.Reader
	stderr bytes.Buffer
	joined bool
}

func k10StartOwner(t *testing.T, root, action string) *k10Owner {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	owner := &k10Owner{cancel: cancel}
	owner.cmd = exec.CommandContext(ctx, os.Args[0], "-test.run=^TestK10OwnerHelper$", "-test.timeout=15s")
	owner.cmd.Env = append(os.Environ(), "HELIANTHUS_K10_OWNER=1")
	owner.cmd.Stderr = &owner.stderr
	input, err := owner.cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	owner.input = input
	output, err := owner.cmd.StdoutPipe()
	if err != nil {
		cancel()
		_ = input.Close()
		t.Fatal(err)
	}
	owner.output = bufio.NewReader(output)
	if err := owner.cmd.Start(); err != nil {
		cancel()
		_ = input.Close()
		_ = output.Close()
		t.Fatal(err)
	}
	// Registered after TempDir: kill and join before the temporary store is deleted,
	// including assertion failures and a helper that never announces readiness.
	t.Cleanup(func() {
		cancel()
		_ = input.Close()
		if !owner.joined {
			_ = owner.cmd.Wait()
			owner.joined = true
		}
	})
	if _, err := fmt.Fprintf(input, "%s\n%s\n", root, action); err != nil {
		t.Fatal(err)
	}
	return owner
}

func (owner *k10Owner) expect(t *testing.T, want string) {
	t.Helper()
	status, err := owner.output.ReadString('\n')
	if err != nil {
		t.Fatalf("helper status: %v (want %s)", err, want)
	}
	if strings.TrimSpace(status) != want {
		t.Fatalf("helper status = %q, want %q", status, want)
	}
}

func (owner *k10Owner) join(t *testing.T, cancelled bool) {
	t.Helper()
	err := owner.cmd.Wait()
	owner.joined = true
	owner.cancel()
	_ = owner.input.Close()
	if cancelled {
		if err == nil {
			t.Fatal("cancelled holder exited successfully")
		}
	} else if err != nil {
		t.Fatalf("helper exit: %v: %s", err, owner.stderr.String())
	}
}
