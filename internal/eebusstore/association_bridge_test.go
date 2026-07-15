package eebusstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"path/filepath"
	"testing"
)

func TestAssociationBridgeCommitsOneCompleteExpectedGeneration(t *testing.T) {
	bridge, outcome := OpenAssociationBridge(filepath.Join(t.TempDir(), "state"), nil)
	if bridge == nil || outcome != "opened_empty" {
		t.Fatalf("open association bridge outcome = %q", outcome)
	}
	t.Cleanup(func() {
		if err := bridge.Close(); err != nil {
			t.Errorf("close association bridge: %v", err)
		}
	})

	remote := make([]byte, 20)
	if _, err := rand.Read(remote); err != nil {
		t.Fatal(err)
	}
	shipBytes := make([]byte, 12)
	if _, err := rand.Read(shipBytes); err != nil {
		t.Fatal(err)
	}
	shipID := hex.EncodeToString(shipBytes)
	generation := bridge.SelectedGeneration()
	if generation == 0 {
		t.Fatal("selected generation is zero")
	}
	if got := bridge.Commit(context.Background(), generation+1, remote, shipID); got != "commit_not_published" {
		t.Fatalf("stale-generation outcome = %q", got)
	}
	if bridge.SelectedGeneration() != generation {
		t.Fatal("stale generation changed selected state")
	}
	if got := bridge.Commit(context.Background(), generation, remote, shipID); got != "commit_durable" {
		t.Fatalf("commit outcome = %q", got)
	}
	if bridge.SelectedGeneration() != generation+1 {
		t.Fatal("durable commit did not advance one generation")
	}
	if got := bridge.Commit(context.Background(), generation+1, remote, shipID); got != "commit_not_published" {
		t.Fatalf("duplicate association outcome = %q", got)
	}

	reloadedGeneration, associations, reloadOutcome := bridge.Reload(context.Background())
	if reloadOutcome != "opened_current" || reloadedGeneration != generation+1 {
		t.Fatalf("reload outcome = %q generation = %d", reloadOutcome, reloadedGeneration)
	}
	if len(associations) != 1 || associations[string(remote)] != shipID {
		t.Fatal("reload did not return exactly one complete association")
	}
}
