package eebusfacade

import (
	"context"
	"slices"
	"testing"
	"time"
)

func TestMSP04CRevocationPublishesDurableTombstoneAndSurvivesRestart(t *testing.T) {
	fixture := newMSP04CFixture(t)
	lineage := fixture.store.view.control.associationLineage
	fixture.store.view.associations = []firstTrustAssociationRecord{
		msp04cAssociation(1, lineage, true, true, true, true),
	}
	coordinator := fixture.newCoordinator()
	if got := coordinator.reopen(context.Background()); got != "pairing_closed" {
		t.Fatalf("startup outcome = %q", got)
	}
	assertMSP04CState(t, coordinator, "PAIRED_TRUSTED", "")

	request := msp04cRevocationRequest(fixture, 41, 1)
	if got := coordinator.revoke(context.Background(), request); got != "revoked" {
		t.Fatalf("revocation outcome = %q", got)
	}
	assertMSP04CState(t, coordinator, "REVOKED", "REVOKED_ASSOCIATION")
	if got := fixture.store.calls(); got != 1 {
		t.Fatalf("publication count = %d, want 1", got)
	}
	fixture.events.assertOrdered(t, "anchor_stage", "store_commit", "anchor_finalize")
	assertMSP04CRevokedGeneration(t, fixture.store.view, request)
	if got := fixture.effects.registerCount(); got != 0 {
		t.Fatalf("trust-registration count = %d, want 0", got)
	}

	restarted := fixture.newCoordinator()
	if got := restarted.reopen(context.Background()); got != "pairing_closed" {
		t.Fatalf("restart outcome = %q", got)
	}
	assertMSP04CState(t, restarted, "REVOKED", "REVOKED_ASSOCIATION")
	if restarted.trusted(msp04cSubject(1)) {
		t.Fatal("tombstoned association reloaded trust")
	}
	if got := restarted.admit(msp04cSubject(1), 51); got == "candidate_pending" {
		t.Fatal("tombstoned association was admitted as a candidate")
	}
	if got := fixture.effects.registerCount(); got != 0 {
		t.Fatalf("restart trust-registration count = %d, want 0", got)
	}
	if got := restarted.revoke(context.Background(), request); got != "revoked" {
		t.Fatalf("terminal replay outcome = %q", got)
	}
	if got := fixture.store.calls(); got != 1 {
		t.Fatalf("terminal replay publication count = %d, want 1", got)
	}
}

func TestMSP04CRevocationRequiresCompleteExactBinding(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(*firstTrustRevocationRequest)
	}{
		{name: "association", mutate: func(request *firstTrustRevocationRequest) { request.associationRef = msp04cOrdinal(62) }},
		{name: "lineage", mutate: func(request *firstTrustRevocationRequest) { request.associationLineage = msp04cOrdinal(63) }},
		{name: "generation sequence", mutate: func(request *firstTrustRevocationRequest) { request.expectedGeneration.sequence++ }},
		{name: "generation filename", mutate: func(request *firstTrustRevocationRequest) { request.expectedGeneration.filename = msp04cText(64) }},
		{name: "generation digest", mutate: func(request *firstTrustRevocationRequest) { request.expectedGeneration.sha256 = msp04cDigest(65) }},
		{name: "generation schema", mutate: func(request *firstTrustRevocationRequest) { request.expectedGeneration.schemaVersion++ }},
		{name: "manifest epoch", mutate: func(request *firstTrustRevocationRequest) { request.expectedManifestEpoch++ }},
		{name: "manifest digest", mutate: func(request *firstTrustRevocationRequest) { request.expectedManifestSHA256 = msp04cDigest(66) }},
		{name: "control epoch", mutate: func(request *firstTrustRevocationRequest) { request.expectedControlEpoch++ }},
	}

	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMSP04CFixture(t)
			lineage := fixture.store.view.control.associationLineage
			fixture.store.view.associations = []firstTrustAssociationRecord{msp04cAssociation(1, lineage, true, true, true, true)}
			coordinator := fixture.newCoordinator()
			_ = coordinator.reopen(context.Background())
			request := msp04cRevocationRequest(fixture, 60, 1)
			test.mutate(&request)
			if got := coordinator.revoke(context.Background(), request); got != "revocation_conflict" {
				t.Fatalf("binding mismatch outcome = %q", got)
			}
			if fixture.store.calls() != 0 || fixture.anchor.stageCalls != 0 {
				t.Fatal("binding mismatch reached a durability boundary")
			}
			assertMSP04CState(t, coordinator, "PAIRED_TRUSTED", "")
		})
	}
}

func TestMSP04CRevocationIdempotencyIsDurableAndBindingExact(t *testing.T) {
	fixture := newMSP04CFixture(t)
	lineage := fixture.store.view.control.associationLineage
	fixture.store.view.associations = []firstTrustAssociationRecord{msp04cAssociation(1, lineage, true, true, true, true)}
	coordinator := fixture.newCoordinator()
	_ = coordinator.reopen(context.Background())
	request := msp04cRevocationRequest(fixture, 71, 1)
	if got := coordinator.revoke(context.Background(), request); got != "revoked" {
		t.Fatalf("initial outcome = %q", got)
	}

	changed := request
	changed.expectedControlEpoch++
	if got := coordinator.revoke(context.Background(), changed); got != "idempotency_conflict" {
		t.Fatalf("changed-binding replay outcome = %q", got)
	}
	if fixture.store.calls() != 1 {
		t.Fatal("changed-binding replay mutated durable state")
	}

	restarted := fixture.newCoordinator()
	_ = restarted.reopen(context.Background())
	if got := restarted.revoke(context.Background(), request); got != "revoked" {
		t.Fatalf("restart replay outcome = %q", got)
	}
	if fixture.store.calls() != 1 {
		t.Fatal("restart replay produced a second publication")
	}

	fixture.store.view.control.receipts = nil
	fixture.store.view.control.operationHighWater = 71
	restarted = fixture.newCoordinator()
	_ = restarted.reopen(context.Background())
	if got := restarted.revoke(context.Background(), request); got != "idempotency_expired" {
		t.Fatalf("compacted replay outcome = %q", got)
	}
	if fixture.store.calls() != 1 {
		t.Fatal("compacted replay produced a publication")
	}
}

func TestMSP04CRevocationLinearizesDenialBeforePublication(t *testing.T) {
	fixture := newMSP04CFixture(t)
	lineage := fixture.store.view.control.associationLineage
	fixture.store.view.associations = []firstTrustAssociationRecord{msp04cAssociation(1, lineage, true, true, true, true)}
	fixture.store.block()
	coordinator := fixture.newCoordinator()
	_ = coordinator.reopen(context.Background())
	request := msp04cRevocationRequest(fixture, 81, 1)
	result := make(chan string, 1)
	go func() { result <- coordinator.revoke(context.Background(), request) }()
	waitMSP04CSignal(t, fixture.store.commitEntered)

	if coordinator.trusted(msp04cSubject(1)) {
		t.Fatal("association remained trusted after revocation linearized")
	}
	if got := coordinator.openPairingWindow(context.Background(), msp04cText(82), time.Minute); got != "operation_in_progress" {
		t.Fatalf("concurrent mutation outcome = %q", got)
	}
	if got := coordinator.revoke(context.Background(), request); got != "operation_in_progress" {
		t.Fatalf("in-flight replay outcome = %q", got)
	}
	changed := request
	changed.associationRef = msp04cOrdinal(83)
	if got := coordinator.revoke(context.Background(), changed); got != "idempotency_conflict" {
		t.Fatalf("in-flight changed-binding outcome = %q", got)
	}
	fixture.store.release()
	if got := waitMSP04CResult(t, result); got != "revoked" {
		t.Fatalf("terminal outcome = %q", got)
	}
}

func TestMSP04CRevocationOutcomeMapNeverRestoresTrust(t *testing.T) {
	tests := []struct {
		name            string
		commitOutcome   string
		finalizeOutcome string
		clearOutcome    string
		wantOutcome     string
		wantState       string
		wantReason      string
		wantPending     bool
	}{
		{name: "durable", commitOutcome: "commit_durable", finalizeOutcome: "anchor_durable", wantOutcome: "revoked", wantState: "REVOKED", wantReason: "REVOKED_ASSOCIATION"},
		{name: "not published cleared", commitOutcome: "commit_not_published", clearOutcome: "anchor_durable", wantOutcome: "failed_closed_unchanged", wantState: "PAIRED_TRUSTED"},
		{name: "not published clear ambiguous", commitOutcome: "commit_not_published", clearOutcome: "anchor_durability_unknown", wantOutcome: "revocation_outcome_unknown", wantState: "QUARANTINED", wantReason: "DURABILITY_UNKNOWN", wantPending: true},
		{name: "applied maintenance failed", commitOutcome: "commit_applied_maintenance_failed", wantOutcome: "revocation_outcome_unknown", wantState: "QUARANTINED", wantReason: "DURABILITY_UNKNOWN", wantPending: true},
		{name: "durability unknown", commitOutcome: "commit_durability_unknown", wantOutcome: "revocation_outcome_unknown", wantState: "QUARANTINED", wantReason: "DURABILITY_UNKNOWN", wantPending: true},
		{name: "finalize ambiguous", commitOutcome: "commit_durable", finalizeOutcome: "anchor_durability_unknown", wantOutcome: "revocation_outcome_unknown", wantState: "QUARANTINED", wantReason: "DURABILITY_UNKNOWN", wantPending: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMSP04CFixture(t)
			lineage := fixture.store.view.control.associationLineage
			fixture.store.view.associations = []firstTrustAssociationRecord{msp04cAssociation(1, lineage, true, true, true, true)}
			fixture.store.commitOutcome = test.commitOutcome
			fixture.anchor.finalizeOutcome = test.finalizeOutcome
			fixture.anchor.clearOutcome = test.clearOutcome
			coordinator := fixture.newCoordinator()
			_ = coordinator.reopen(context.Background())
			if got := coordinator.revoke(context.Background(), msp04cRevocationRequest(fixture, 91, 1)); got != test.wantOutcome {
				t.Fatalf("mapped outcome = %q, want %q", got, test.wantOutcome)
			}
			assertMSP04CState(t, coordinator, test.wantState, test.wantReason)
			if got := fixture.anchor.record.pending != nil; got != test.wantPending {
				t.Fatalf("pending retained = %t, want %t", got, test.wantPending)
			}
			if fixture.effects.registerCount() != 0 {
				t.Fatal("revocation path registered trust")
			}
		})
	}
}

func TestMSP04CInheritedTrustRepairCreatesFreshCompleteUntrustedLineage(t *testing.T) {
	tests := []struct {
		kind       string
		fromParent bool
		anchorOpen string
	}{
		{kind: "publish_inactive_parent", fromParent: true},
		{kind: "adopt_copied_current"},
		{kind: "recover_unavailable_host_key", anchorOpen: "anchor_unavailable"},
	}

	for index, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			fixture := newMSP04CFixture(t)
			oldLineage := fixture.store.view.control.associationLineage
			inherited := []firstTrustAssociationRecord{
				msp04cAssociation(1, oldLineage, false, true, false, false),
				msp04cAssociation(2, oldLineage, true, false, false, false),
				msp04cAssociation(3, oldLineage, false, false, true, false),
				msp04cAssociation(4, oldLineage, false, false, false, true),
			}
			inert := msp04cAssociation(5, oldLineage, false, false, false, false)
			if test.fromParent {
				fixture.store.view.parentAssociations = append(append([]firstTrustAssociationRecord(nil), inherited...), inert)
				fixture.store.view.associations = []firstTrustAssociationRecord{msp04cAssociation(9, oldLineage, true, true, true, true)}
			} else {
				fixture.store.view.associations = append(append([]firstTrustAssociationRecord(nil), inherited...), inert)
			}
			fixture.store.view.control.tombstones = []firstTrustRevocationTombstone{msp04cTombstone(8, 6, fixture.store.view.manifest.current)}
			switch test.kind {
			case "publish_inactive_parent":
				fixture.anchor.record.manifestGenerationHighWater = fixture.store.view.manifest.current.sequence + 1
			case "adopt_copied_current":
				fixture.anchor.record.storeInstance = msp04cOrdinal(99)
			}
			if test.anchorOpen != "" {
				fixture.anchor.openOutcome = test.anchorOpen
			}
			coordinator := fixture.newCoordinator()
			_ = coordinator.reopen(context.Background())
			request := msp04cExactRepairRequest(fixture, coordinator, test.kind, uint64(110+index))
			if got := coordinator.repair(context.Background(), request); got != "repaired_unpaired" {
				t.Fatalf("repair outcome = %q", got)
			}
			assertMSP04CState(t, coordinator, "UNPAIRED_LOCKED", "")
			if fixture.store.view.control.associationLineage == oldLineage {
				t.Fatal("repair reused the inherited lineage")
			}
			assertMSP04CInheritedSetInactiveAndTombstoned(t, fixture.store.view, inherited)
			if !msp04cContainsTombstone(fixture.store.view.control.tombstones, msp04cOrdinal(8)) {
				t.Fatal("repair removed an existing tombstone")
			}
			if test.kind == "recover_unavailable_host_key" && fixture.anchor.createCalls != 1 {
				t.Fatalf("anchor create count = %d, want 1", fixture.anchor.createCalls)
			}
			if fixture.effects.registerCount() != 0 {
				t.Fatal("repair registered trust")
			}

			restarted := fixture.newCoordinator()
			_ = restarted.reopen(context.Background())
			assertMSP04CState(t, restarted, "UNPAIRED_LOCKED", "")
			for _, association := range inherited {
				if restarted.trusted(association.subject) {
					t.Fatal("inherited association reloaded trust after repair")
				}
			}
			if fixture.effects.registerCount() != 0 {
				t.Fatal("restart after repair registered trust")
			}
		})
	}
}

func TestMSP04CRepairBindingReplayAndPublicationOutcomesFailClosed(t *testing.T) {
	fixture := newMSP04CFixture(t)
	fixture.anchor.openOutcome = "anchor_unavailable"
	fixture.store.view.associations = []firstTrustAssociationRecord{
		msp04cAssociation(1, fixture.store.view.control.associationLineage, true, true, true, true),
	}
	coordinator := fixture.newCoordinator()
	_ = coordinator.reopen(context.Background())
	request := msp04cExactRepairRequest(fixture, coordinator, "recover_unavailable_host_key", 121)
	stale := request
	stale.expectedManifest.sha256 = msp04cDigest(122)
	if got := coordinator.repair(context.Background(), stale); got != "repair_conflict" {
		t.Fatalf("stale binding outcome = %q", got)
	}
	if fixture.store.calls() != 0 || fixture.anchor.stageCalls != 0 {
		t.Fatal("stale repair reached a durability boundary")
	}

	fixture.store.commitOutcome = "commit_not_published"
	fixture.anchor.clearOutcome = "anchor_durability_unknown"
	if got := coordinator.repair(context.Background(), request); got != "repair_outcome_unknown" {
		t.Fatalf("ambiguous repair outcome = %q", got)
	}
	assertMSP04CState(t, coordinator, "QUARANTINED", "DURABILITY_UNKNOWN")
	if fixture.anchor.record.pending == nil {
		t.Fatal("ambiguous repair discarded its pending descriptor")
	}
	if fixture.effects.registerCount() != 0 {
		t.Fatal("ambiguous repair registered trust")
	}
}

func TestMSP04CRepairIdempotencySurvivesRestartAndCompaction(t *testing.T) {
	fixture := newMSP04CFixture(t)
	fixture.anchor.record.storeInstance = msp04cOrdinal(501)
	fixture.store.view.associations = []firstTrustAssociationRecord{
		msp04cAssociation(1, fixture.store.view.control.associationLineage, true, true, true, true),
	}
	coordinator := fixture.newCoordinator()
	_ = coordinator.reopen(context.Background())
	request := msp04cExactRepairRequest(fixture, coordinator, "adopt_copied_current", 502)
	if got := coordinator.repair(context.Background(), request); got != "repaired_unpaired" {
		t.Fatalf("initial repair outcome = %q", got)
	}
	if fixture.store.calls() != 1 {
		t.Fatalf("initial publication count = %d, want 1", fixture.store.calls())
	}

	changed := request
	changed.scope = msp04cOrdinal(503)
	if got := coordinator.repair(context.Background(), changed); got != "idempotency_conflict" {
		t.Fatalf("changed-binding replay outcome = %q", got)
	}
	if fixture.store.calls() != 1 {
		t.Fatal("changed-binding replay produced a publication")
	}

	restarted := fixture.newCoordinator()
	_ = restarted.reopen(context.Background())
	if got := restarted.repair(context.Background(), request); got != "repaired_unpaired" {
		t.Fatalf("restart replay outcome = %q", got)
	}
	if fixture.store.calls() != 1 {
		t.Fatal("restart replay produced a second publication")
	}

	fixture.store.view.control.receipts = nil
	restarted = fixture.newCoordinator()
	_ = restarted.reopen(context.Background())
	if got := restarted.repair(context.Background(), request); got != "idempotency_expired" {
		t.Fatalf("compacted replay outcome = %q", got)
	}
	if fixture.store.calls() != 1 {
		t.Fatal("compacted replay produced a publication")
	}
}

func TestMSP04CTombstoneCapacityNeverEvictsDurableDenial(t *testing.T) {
	if firstTrustMaximumTombstones < 1 {
		t.Fatal("tombstone capacity bound must be positive")
	}
	fixture := newMSP04CFixture(t)
	fixture.store.view.associations = []firstTrustAssociationRecord{
		msp04cAssociation(1, fixture.store.view.control.associationLineage, true, true, true, true),
	}
	fixture.store.view.control.tombstones = make([]firstTrustRevocationTombstone, firstTrustMaximumTombstones)
	for index := range fixture.store.view.control.tombstones {
		fixture.store.view.control.tombstones[index] = msp04cTombstone(uint64(600+index), 4, fixture.store.view.manifest.current)
	}
	original := append([]firstTrustRevocationTombstone(nil), fixture.store.view.control.tombstones...)
	coordinator := fixture.newCoordinator()
	_ = coordinator.reopen(context.Background())
	if got := coordinator.revoke(context.Background(), msp04cRevocationRequest(fixture, 700, 1)); got != "tombstone_capacity" {
		t.Fatalf("revocation capacity outcome = %q", got)
	}
	if fixture.store.calls() != 0 || fixture.anchor.stageCalls != 0 {
		t.Fatal("revocation capacity failure reached a durability boundary")
	}
	if !slices.Equal(fixture.store.view.control.tombstones, original) {
		t.Fatal("revocation capacity failure changed existing tombstones")
	}

	fixture.anchor.record.storeInstance = msp04cOrdinal(701)
	coordinator = fixture.newCoordinator()
	_ = coordinator.reopen(context.Background())
	request := msp04cExactRepairRequest(fixture, coordinator, "adopt_copied_current", 702)
	if got := coordinator.repair(context.Background(), request); got != "repair_conflict" {
		t.Fatalf("repair capacity outcome = %q", got)
	}
	if fixture.store.calls() != 0 || fixture.anchor.stageCalls != 0 {
		t.Fatal("repair capacity failure reached a durability boundary")
	}
	if !slices.Equal(fixture.store.view.control.tombstones, original) {
		t.Fatal("repair capacity failure changed existing tombstones")
	}
}

func TestMSP04CRepairClosesVolatileFirstTrustBeforeMutation(t *testing.T) {
	fixture := newMSP04CFixture(t)
	fixture.store.view.control.tombstones = []firstTrustRevocationTombstone{msp04cTombstone(1, 7, fixture.store.view.manifest.current)}
	coordinator := fixture.newCoordinator()
	_ = coordinator.reopen(context.Background())
	if got := coordinator.openPairingWindow(context.Background(), msp04cText(131), time.Minute); got != "open_empty" {
		t.Fatalf("open outcome = %q", got)
	}
	if got := coordinator.admit(msp04cSubject(2), 132); got != "candidate_pending" {
		t.Fatalf("admission outcome = %q", got)
	}
	request := msp04cExactRepairRequest(fixture, coordinator, "release_retry_quarantine", 133)
	if got := coordinator.repair(context.Background(), request); got != "repair_conflict" {
		t.Fatalf("non-applicable repair outcome = %q", got)
	}
	if coordinator.state() != "PAIRING_CLOSED" {
		t.Fatalf("repair did not close pairing: %s", coordinator.state())
	}
	if _, _, _, _, _, _, ok := coordinator.candidate(); ok {
		t.Fatal("repair left a volatile candidate")
	}
	if fixture.effects.cancels == 0 {
		t.Fatal("repair did not cancel the volatile candidate")
	}
}

func msp04cRevocationRequest(fixture *msp04cFixture, operation, association uint64) firstTrustRevocationRequest {
	return firstTrustRevocationRequest{
		operationID:            msp04cOrdinal(operation),
		associationRef:         msp04cOrdinal(association),
		associationLineage:     fixture.store.view.control.associationLineage,
		expectedGeneration:     fixture.store.view.manifest.current,
		expectedManifestEpoch:  fixture.store.view.manifest.epoch,
		expectedManifestSHA256: fixture.store.view.manifest.sha256,
		expectedControlEpoch:   fixture.store.view.control.controlEpoch,
	}
}

func msp04cExactRepairRequest(fixture *msp04cFixture, coordinator *firstTrustCoordinator, kind string, operation uint64) firstTrustRepairRequest {
	return firstTrustRepairRequest{
		operationID:               msp04cOrdinal(operation),
		kind:                      kind,
		scope:                     msp04cOrdinal(operation + 1),
		expectedState:             coordinator.recoveryState(),
		expectedReason:            coordinator.recoveryReason(),
		expectedManifest:          fixture.store.view.manifest,
		expectedControlEpoch:      fixture.store.view.control.controlEpoch,
		expectedAnchorVersion:     fixture.anchor.record.version,
		expectedManifestHighWater: fixture.anchor.record.manifestGenerationHighWater,
		expectedControlHighWater:  fixture.anchor.record.controlEpochHighWater,
		nextRepairSequence:        fixture.store.view.control.repairSequence + 1,
	}
}

func assertMSP04CRevokedGeneration(t *testing.T, view firstTrustControlView, request firstTrustRevocationRequest) {
	t.Helper()
	for _, association := range view.associations {
		if association.reference == request.associationRef && (association.active || association.trusted || association.allowlisted || association.reconnectable) {
			t.Fatal("revoked association retained an active trust capability")
		}
	}
	for _, tombstone := range view.control.tombstones {
		if tombstone.associationRef == request.associationRef && tombstone.operationID == request.operationID && tombstone.revocationEpoch == request.expectedControlEpoch+1 && tombstone.effectiveGeneration == view.manifest.current {
			return
		}
	}
	t.Fatal("revoked generation lacks its exact effective tombstone")
}

func assertMSP04CInheritedSetInactiveAndTombstoned(t *testing.T, view firstTrustControlView, inherited []firstTrustAssociationRecord) {
	t.Helper()
	for _, source := range inherited {
		found := false
		for _, target := range view.associations {
			if target.reference != source.reference {
				continue
			}
			found = true
			if target.active || target.trusted || target.allowlisted || target.reconnectable {
				t.Fatal("inherited association retained an active trust capability")
			}
			if target.lineage == source.lineage {
				t.Fatal("inherited association retained its old lineage")
			}
			if target.lineage != view.control.associationLineage {
				t.Fatal("inherited association is not bound to the fresh target lineage")
			}
		}
		if !found || !msp04cContainsTombstone(view.control.tombstones, source.reference) {
			t.Fatal("inherited set was not completely deactivated and tombstoned")
		}
	}
}

func msp04cContainsTombstone(tombstones []firstTrustRevocationTombstone, reference [32]byte) bool {
	return slices.ContainsFunc(tombstones, func(value firstTrustRevocationTombstone) bool {
		return value.associationRef == reference
	})
}

func waitMSP04CSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the durability boundary")
	}
}

func waitMSP04CResult(t *testing.T, result <-chan string) string {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the terminal outcome")
		return ""
	}
}
