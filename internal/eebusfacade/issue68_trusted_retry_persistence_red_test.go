package eebusfacade

import (
	"context"
	"testing"
)

func TestIssue68FirstTrustCommitConsumesExactRetryHold(t *testing.T) {
	fixture := newMSP04CFixture(t)
	coordinator := fixture.newCoordinator()
	if got := coordinator.reopen(context.Background()); got != "pairing_closed" {
		t.Fatalf("startup outcome = %q", got)
	}
	binding := openMSP04CCandidate(t, coordinator, 220)
	scope := firstTrustRuntimeRetryScope(firstTrustNormalizedSKI(binding.subject))
	hold := issue68RetryHold(scope, fixture.store.view.control.controlEpoch)

	coordinator.mu.Lock()
	coordinator.controlView.control.quarantines = []firstTrustQuarantineRecord{hold}
	coordinator.mu.Unlock()
	fixture.store.view.control.quarantines = []firstTrustQuarantineRecord{hold}

	if got := confirmMSP04C(coordinator, binding); got != "trusted" {
		t.Fatalf("confirmation outcome = %q", got)
	}
	issue68AssertRetryReady(t, fixture.store.view.control, scope)

	restarted := fixture.newCoordinator()
	if got := restarted.reopen(context.Background()); got != "pairing_closed" {
		t.Fatalf("restart outcome = %q", got)
	}
	assertMSP04CState(t, restarted, "PAIRED_TRUSTED", "")
}

func TestIssue68StartupDurablyReconcilesOnlyTrustedRemoteRetryHold(t *testing.T) {
	fixture := newMSP04CFixture(t)
	lineage := fixture.store.view.control.associationLineage
	association := msp04cAssociation(1, lineage, true, true, true, true)
	scope := firstTrustRuntimeRetryScope(firstTrustNormalizedSKI(association.subject))
	fixture.store.view.associations = []firstTrustAssociationRecord{association}
	fixture.store.view.control.quarantines = []firstTrustQuarantineRecord{
		issue68RetryHold(scope, fixture.store.view.control.controlEpoch),
	}
	initialGeneration := fixture.store.view.manifest.current.sequence
	initialControlEpoch := fixture.store.view.control.controlEpoch

	coordinator := fixture.newCoordinator()
	if got := coordinator.reopen(context.Background()); got != "pairing_closed" {
		t.Fatalf("startup recovery outcome = %q", got)
	}
	assertMSP04CState(t, coordinator, "PAIRED_TRUSTED", "")
	issue68AssertRetryReady(t, fixture.store.view.control, scope)
	if fixture.store.view.manifest.current.sequence == initialGeneration {
		t.Fatal("startup recovery did not publish a new durable generation")
	}
	if fixture.store.view.control.controlEpoch != initialControlEpoch+1 {
		t.Fatalf("control epoch = %d, want %d", fixture.store.view.control.controlEpoch, initialControlEpoch+1)
	}
	if fixture.anchor.record.manifestGenerationHighWater != fixture.store.view.manifest.current.sequence ||
		fixture.anchor.record.controlEpochHighWater != fixture.store.view.control.controlEpoch {
		t.Fatal("startup recovery did not advance the protected high-water marks")
	}

	restarted := fixture.newCoordinator()
	if got := restarted.reopen(context.Background()); got != "pairing_closed" {
		t.Fatalf("second restart outcome = %q", got)
	}
	assertMSP04CState(t, restarted, "PAIRED_TRUSTED", "")
	issue68AssertRetryReady(t, fixture.store.view.control, scope)
}

func TestIssue68StartupKeepsUnrelatedRetryHoldFailClosed(t *testing.T) {
	fixture := newMSP04CFixture(t)
	lineage := fixture.store.view.control.associationLineage
	fixture.store.view.associations = []firstTrustAssociationRecord{
		msp04cAssociation(1, lineage, true, true, true, true),
	}
	unrelatedScope := firstTrustRuntimeRetryScope(firstTrustNormalizedSKI(msp04cSubject(2)))
	fixture.store.view.control.quarantines = []firstTrustQuarantineRecord{
		issue68RetryHold(unrelatedScope, fixture.store.view.control.controlEpoch),
	}
	initialGeneration := fixture.store.view.manifest.current.sequence

	coordinator := fixture.newCoordinator()
	_ = coordinator.reopen(context.Background())
	assertMSP04CState(t, coordinator, "QUARANTINED", "HANDSHAKE_ATTEMPT_LIMIT")
	if fixture.store.view.manifest.current.sequence != initialGeneration {
		t.Fatal("unrelated retry hold produced a startup publication")
	}
	if fixture.store.view.control.quarantines[0].state != "ADMIN_HOLD" {
		t.Fatal("unrelated retry hold was relaxed")
	}
}

func TestIssue68StartupReconciliationPublicationFailureStaysFailClosed(t *testing.T) {
	tests := []struct {
		name          string
		commitOutcome string
		wantReason    string
		wantPending   bool
	}{
		{name: "not published", commitOutcome: "commit_not_published", wantReason: "HANDSHAKE_ATTEMPT_LIMIT"},
		{name: "durability unknown", commitOutcome: "commit_durability_unknown", wantReason: "DURABILITY_UNKNOWN", wantPending: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMSP04CFixture(t)
			lineage := fixture.store.view.control.associationLineage
			association := msp04cAssociation(1, lineage, true, true, true, true)
			scope := firstTrustRuntimeRetryScope(firstTrustNormalizedSKI(association.subject))
			fixture.store.view.associations = []firstTrustAssociationRecord{association}
			fixture.store.view.control.quarantines = []firstTrustQuarantineRecord{
				issue68RetryHold(scope, fixture.store.view.control.controlEpoch),
			}
			fixture.store.commitOutcome = test.commitOutcome

			coordinator := fixture.newCoordinator()
			_ = coordinator.reopen(context.Background())
			assertMSP04CState(t, coordinator, "QUARANTINED", test.wantReason)
			if got := fixture.anchor.record.pending != nil; got != test.wantPending {
				t.Fatalf("pending publication retained = %t, want %t", got, test.wantPending)
			}
		})
	}
}

func issue68RetryHold(scope [32]byte, controlEpoch uint64) firstTrustQuarantineRecord {
	return firstTrustQuarantineRecord{
		scope:            scope,
		reason:           "HANDSHAKE_ATTEMPT_LIMIT",
		state:            "ADMIN_HOLD",
		attemptCount:     4,
		retentionBudget:  firstTrustQuarantineRetention,
		lastControlEpoch: controlEpoch,
	}
}

func issue68AssertRetryReady(t *testing.T, control firstTrustControlRecord, scope [32]byte) {
	t.Helper()
	for _, quarantine := range control.quarantines {
		if quarantine.scope != scope {
			continue
		}
		if quarantine.state != "RETRY_READY" || quarantine.reason != "RETRYABLE_FAILURE" ||
			quarantine.attemptCount != 0 || quarantine.backoffStep != 0 || quarantine.remainingDelay != 0 {
			t.Fatalf("retry tuple = %#v, want reset RETRY_READY", quarantine)
		}
		return
	}
	t.Fatal("retry scope is missing")
}
