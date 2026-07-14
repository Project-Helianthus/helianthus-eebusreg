//go:build darwin && cgo

package eebusstore

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDarwinExtendedACLInspection(t *testing.T) {
	t.Run("no ACL is accepted", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "store")
		created := openForTest(t, root, nil, nil)
		assertOutcome(t, created.outcome, outcomeOpenedEmpty)
		closeStore(t, created)

		reopened := openForTest(t, root, nil, nil)
		defer closeStore(t, reopened)
		assertOutcome(t, reopened.outcome, outcomeOpenedCurrent)
	})

	t.Run("reopen rejects additional access ACL", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "store")
		created := openForTest(t, root, nil, nil)
		assertOutcome(t, created.outcome, outcomeOpenedEmpty)
		closeStore(t, created)
		addDarwinAdditionalAccessACL(t, root)

		reopened := openForTest(t, root, nil, nil)
		assertOutcome(t, reopened.outcome, outcomePermissionsRejected)
		if reopened.store != nil || reopened.state != nil {
			t.Fatal("ACL-bearing root returned active state")
		}
	})

	t.Run("commit preserves ACL rejection", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "store")
		opened := openForTest(t, root, nil, nil)
		assertOutcome(t, opened.outcome, outcomeOpenedEmpty)
		addDarwinAdditionalAccessACL(t, root)

		committed := opened.store.commit(emptyLogicalState(t))
		assertOutcome(t, committed.outcome, outcomePermissionsRejected)
		assertErrorOutcome(t, committed.err, outcomePermissionsRejected)
		closeStore(t, opened)
	})
}

func addDarwinAdditionalAccessACL(t *testing.T, path string) {
	t.Helper()
	if output, err := exec.Command("/bin/chmod", "+a", "everyone allow read", path).CombinedOutput(); err != nil {
		t.Fatalf("add Darwin ACL: %v: %s", err, output)
	}
	t.Cleanup(func() {
		if output, err := exec.Command("/bin/chmod", "-a#", "0", path).CombinedOutput(); err != nil {
			t.Errorf("remove Darwin ACL: %v: %s", err, output)
		}
	})
}
