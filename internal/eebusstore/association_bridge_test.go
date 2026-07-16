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

func TestAssociationBridgePreparesEffectiveTombstoneAndReloadsAuthoritativeParent(t *testing.T) {
	bridge, outcome := OpenAssociationBridge(filepath.Join(t.TempDir(), "state"), nil)
	if bridge == nil || outcome != "opened_empty" {
		t.Fatalf("open bridge = %q", outcome)
	}
	t.Cleanup(func() { _ = bridge.Close() })

	firstRemote := make([]byte, 20)
	firstRemote[19] = 1
	secondRemote := make([]byte, 20)
	secondRemote[19] = 2
	if got := bridge.Commit(context.Background(), bridge.SelectedGeneration(), firstRemote, "parent-service"); got != "commit_durable" {
		t.Fatalf("first association commit = %q", got)
	}
	if got := bridge.Commit(context.Background(), bridge.SelectedGeneration(), secondRemote, "current-service"); got != "commit_durable" {
		t.Fatalf("second association commit = %q", got)
	}

	view, outcome := bridge.ReloadControl(context.Background())
	if outcome != "opened_current" || len(view.Associations) != 2 || len(view.ParentAssociations) != 1 {
		t.Fatalf("control reload outcome=%q current=%d parent=%d", outcome, len(view.Associations), len(view.ParentAssociations))
	}
	if view.ParentAssociations[0].Service != "parent-service" {
		t.Fatal("parent associations were not loaded from the exact parent generation")
	}

	operationID := [32]byte{31: 9}
	target := ControlRecord{
		Present: true, StoreInstance: [32]byte{31: 10}, ControlEpoch: 1, AssociationLineage: [32]byte{31: 11},
		Tombstones: []ControlTombstone{{
			AssociationRef: view.Associations[0].Reference, RevocationEpoch: 1, OperationID: operationID,
		}},
	}
	prepared, outcome := bridge.PrepareControl(context.Background(), view, target, operationID, "revocation")
	if outcome != "prepared" {
		t.Fatalf("prepare control = %q", outcome)
	}
	if len(prepared.Target.Control.Tombstones) != 1 || prepared.Target.Control.Tombstones[0].EffectiveGeneration != view.Manifest.Current {
		t.Fatal("store did not atomically bind the tombstone to the authoritative source generation")
	}
	if got := bridge.CommitControl(context.Background(), prepared); got != "commit_durable" {
		t.Fatalf("commit control = %q", got)
	}
	reloaded, outcome := bridge.ReloadControl(context.Background())
	if outcome != "opened_current" || len(reloaded.Control.Tombstones) != 1 || reloaded.Control.Tombstones[0].EffectiveGeneration != view.Manifest.Current {
		t.Fatal("effective tombstone binding did not survive real store restart")
	}
}

func TestAssociationBridgeFailsClosedWhenAssociationCannotEnterControlDTO(t *testing.T) {
	bridge, outcome := OpenAssociationBridge(filepath.Join(t.TempDir(), "state"), nil)
	if bridge == nil || outcome != "opened_empty" {
		t.Fatalf("open bridge = %q", outcome)
	}
	t.Cleanup(func() { _ = bridge.Close() })
	if got := bridge.Commit(context.Background(), bridge.SelectedGeneration(), []byte{1, 2, 3}, "unrepresentable-service"); got != "commit_durable" {
		t.Fatalf("mechanical legacy commit = %q", got)
	}
	bridge.mu.Lock()
	state := cloneStateV1(bridge.opened.state)
	validRemote := make([]byte, 20)
	validRemote[19] = 4
	state.remoteIdentities = []remoteIdentityV1{{recordID: validRemote, remoteSKI: validRemote, remoteSHIPID: "current-service"}}
	commit := bridge.opened.commit(state)
	bridge.mu.Unlock()
	if commit.outcome != outcomeCommitDurable {
		t.Fatalf("mechanical current generation = %q", commit.outcome)
	}
	view, got := bridge.ReloadControl(context.Background())
	if got != "malformed_state" || view.Manifest.Epoch != 0 || view.Control.Present || len(view.Associations) != 0 || len(view.ParentAssociations) != 0 {
		t.Fatalf("unrepresentable association returned outcome=%q", got)
	}
}
