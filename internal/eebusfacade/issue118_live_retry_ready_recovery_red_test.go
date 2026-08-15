package eebusfacade

import (
	"context"
	"testing"
)

func TestIssue118PersistedRetryReadyReopenClearsKnownUnappliedAttempt(t *testing.T) {
	fixture, association, scope := issue118PersistedRetryReadyFixture(t)
	pending := msp04cPending(fixture.store.view, fixture.store.nextView())
	pending.operationClass = "attempt_prepare"
	fixture.anchor.record.pending = &pending
	fixture.store.reconcileObservation = "exact_previous_selected_and_target_absent"

	coordinator := fixture.newCoordinator()
	_ = coordinator.reopenWithRecovery(context.Background())

	if fixture.anchor.clearCalls != 1 || fixture.anchor.record.pending != nil {
		t.Fatalf("known-unapplied clear calls=%d pending=%v, want one durable clear", fixture.anchor.clearCalls, fixture.anchor.record.pending != nil)
	}
	if fixture.store.calls() != 0 {
		t.Fatalf("known-unapplied reopen committed store %d times, want unchanged selected store", fixture.store.calls())
	}
	if got := coordinator.recoveryState(); got != "QUARANTINED" {
		t.Fatalf("recovery=%q, want QUARANTINED retry-control product", got)
	}
	if got := coordinator.recoveryReason(); got != "RETRYABLE_FAILURE" {
		t.Fatalf("recovery reason=%q, want RETRYABLE_FAILURE", got)
	}
	if !coordinator.runtimeStartAuthorized() {
		t.Fatal("persisted retry-ready recovery denied listener/operator boundary")
	}

	coordinator.mu.Lock()
	automaticEligible := coordinator.firstTrustOutgoingAttemptEligibleLocked(association.subject)
	coordinator.mu.Unlock()
	if automaticEligible {
		t.Fatal("known-unapplied reconciliation granted automatic outbound authority")
	}
	if outcome := coordinator.authorizeRuntimeAttempt(association.subject); outcome != "attempt_denied" {
		t.Fatalf("automatic runtime callback=%q, want attempt_denied", outcome)
	}
	if got := coordinator.admitRetry(context.Background(), scope); got != "retry_admitted" {
		t.Fatalf("explicit retry admission=%q, want retry_admitted", got)
	}
}

func TestIssue118PersistedRetryReadyWithoutPendingUsesRecoveryOnlyProduct(t *testing.T) {
	fixture, association, _ := issue118PersistedRetryReadyFixture(t)
	coordinator := fixture.newCoordinator()
	_ = coordinator.reopenWithRecovery(context.Background())

	if got := coordinator.recoveryState(); got != "QUARANTINED" {
		t.Fatalf("persisted retry-ready recovery=%q, want QUARANTINED", got)
	}
	if got := coordinator.recoveryReason(); got != "RETRYABLE_FAILURE" {
		t.Fatalf("persisted retry-ready reason=%q, want RETRYABLE_FAILURE", got)
	}
	if !coordinator.runtimeStartAuthorized() {
		t.Fatal("persisted retry-ready product denied boundary startup")
	}
	coordinator.mu.Lock()
	automaticEligible := coordinator.firstTrustOutgoingAttemptEligibleLocked(association.subject)
	coordinator.mu.Unlock()
	if automaticEligible {
		t.Fatal("persisted retry-ready product admitted generic reconnect")
	}
}

func TestIssue118PendingReopenKeepsEveryNonExactOutcomeFailClosed(t *testing.T) {
	tests := []struct {
		name               string
		observation        string
		clearOutcome       string
		wantClearCallCount int
	}{
		{name: "target selected", observation: "exact_target_selected", clearOutcome: "anchor_durable"},
		{name: "ambiguous", observation: "other_or_ambiguous", clearOutcome: "anchor_durable"},
		{name: "clear failure", observation: "exact_previous_selected_and_target_absent", clearOutcome: "anchor_unavailable", wantClearCallCount: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, association, _ := issue118PersistedRetryReadyFixture(t)
			pending := msp04cPending(fixture.store.view, fixture.store.nextView())
			pending.operationClass = "attempt_prepare"
			fixture.anchor.record.pending = &pending
			fixture.anchor.clearOutcome = test.clearOutcome
			fixture.store.reconcileObservation = test.observation

			coordinator := fixture.newCoordinator()
			_ = coordinator.reopenWithRecovery(context.Background())

			if fixture.anchor.clearCalls != test.wantClearCallCount {
				t.Fatalf("clear calls=%d, want %d", fixture.anchor.clearCalls, test.wantClearCallCount)
			}
			if fixture.anchor.record.pending == nil {
				t.Fatal("non-exact pending outcome cleared protected descriptor")
			}
			if got := coordinator.recoveryState(); got != "QUARANTINED" {
				t.Fatalf("recovery=%q, want QUARANTINED", got)
			}
			if got := coordinator.recoveryReason(); got != "DURABILITY_UNKNOWN" {
				t.Fatalf("reason=%q, want DURABILITY_UNKNOWN", got)
			}
			if coordinator.runtimeStartAuthorized() {
				t.Fatal("non-exact pending outcome authorized runtime startup")
			}
			if outcome := coordinator.authorizeRuntimeAttempt(association.subject); outcome != "attempt_denied" {
				t.Fatalf("non-exact runtime callback=%q, want attempt_denied", outcome)
			}
		})
	}
}

func issue118PersistedRetryReadyFixture(t *testing.T) (*msp04cFixture, firstTrustAssociationRecord, [32]byte) {
	t.Helper()
	fixture := newMSP04CFixture(t)
	lineage := fixture.store.view.control.associationLineage
	association := msp04cAssociation(118_001, lineage, true, true, true, true)
	scope := firstTrustRuntimeRetryScope(firstTrustNormalizedSKI(association.subject))
	fixture.store.view.associations = []firstTrustAssociationRecord{association}
	fixture.store.view.control.quarantines = []firstTrustQuarantineRecord{{
		scope:            scope,
		reason:           "RETRYABLE_FAILURE",
		state:            "RETRY_READY",
		attemptCount:     0,
		backoffStep:      0,
		remainingDelay:   0,
		retentionBudget:  firstTrustQuarantineRetention,
		lastControlEpoch: fixture.store.view.control.controlEpoch,
	}}
	fixture.store.view.control.receipts = []firstTrustDurableReceipt{{
		operationID:    msp04cOrdinal(118_010),
		operationClass: "release_retry_quarantine",
		bindingSHA256:  msp04cDigest(118_011),
		result:         "repaired_unpaired",
		terminal:       true,
	}}
	return fixture, association, scope
}
