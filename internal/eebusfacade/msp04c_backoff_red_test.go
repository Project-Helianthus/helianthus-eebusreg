package eebusfacade

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestMSP04CG11ExactSaturatingVector(t *testing.T) {
	fixture := newMSP04CFixture(t)
	coordinator := fixture.newCoordinator()
	if got := coordinator.reopen(context.Background()); got != "pairing_closed" {
		t.Fatalf("startup outcome = %q", got)
	}
	scope := msp04cOrdinal(141)

	wantCounts := []uint64{0, 1, 2, 3, 4}
	wantDelays := []time.Duration{0, 3 * time.Second, 6 * time.Second, 10 * time.Second, 0}
	states := make([]string, 0, len(wantCounts))
	counts := make([]uint64, 0, len(wantCounts))
	delays := make([]time.Duration, 0, len(wantCounts))
	record := func() {
		state, count, remainder, ok := coordinator.retryState(scope)
		if !ok {
			t.Fatal("retry scope is unavailable")
		}
		states = append(states, state)
		counts = append(counts, count)
		delays = append(delays, remainder)
	}

	if got := coordinator.admitRetry(context.Background(), scope); got != "retry_admitted" {
		t.Fatalf("initial admission outcome = %q", got)
	}
	record()
	for step := 1; step < len(wantCounts); step++ {
		wantOutcome := "backoff_active"
		if step == len(wantCounts)-1 {
			wantOutcome = "admin_hold"
		}
		if got := coordinator.recordRetryFailure(context.Background(), scope); got != wantOutcome {
			t.Fatalf("step %d failure outcome = %q", step, got)
		}
		record()
		if step+1 < len(wantCounts) {
			fixture.clock.advanceMonotonic(wantDelays[step])
			if got := coordinator.admitRetry(context.Background(), scope); got != "retry_admitted" {
				t.Fatalf("step %d re-admission outcome = %q", step, got)
			}
		}
	}

	for index := range wantCounts {
		wantState := "BACKOFF_ACTIVE"
		if index == 0 {
			wantState = "RETRY_READY"
		} else if index == len(wantCounts)-1 {
			wantState = "ADMIN_HOLD"
		}
		if states[index] != wantState || counts[index] != wantCounts[index] || delays[index] != wantDelays[index] {
			t.Fatalf("step %d tuple = %s/%d/%s, want %s/%d/%s", index, states[index], counts[index], delays[index], wantState, wantCounts[index], wantDelays[index])
		}
	}
}

func TestMSP04CG11CountChangesOnlyAtFailedAttemptLinearization(t *testing.T) {
	fixture := newMSP04CFixture(t)
	coordinator := fixture.newCoordinator()
	_ = coordinator.reopen(context.Background())
	scope := msp04cOrdinal(151)

	if got := coordinator.admitRetry(context.Background(), scope); got != "retry_admitted" {
		t.Fatalf("admission outcome = %q", got)
	}
	assertMSP04CRetryTuple(t, coordinator, scope, "RETRY_READY", 0, 0)
	if got := coordinator.admitRetry(context.Background(), scope); got != "attempt_in_progress" {
		t.Fatalf("duplicate admission outcome = %q", got)
	}
	assertMSP04CRetryTuple(t, coordinator, scope, "RETRY_READY", 0, 0)
	if got := coordinator.recordRetryFailure(context.Background(), scope); got != "backoff_active" {
		t.Fatalf("failure outcome = %q", got)
	}
	assertMSP04CRetryTuple(t, coordinator, scope, "BACKOFF_ACTIVE", 1, 3*time.Second)

	if got := coordinator.admitRetry(context.Background(), scope); got != "backoff_active" {
		t.Fatalf("early admission outcome = %q", got)
	}
	assertMSP04CRetryTuple(t, coordinator, scope, "BACKOFF_ACTIVE", 1, 3*time.Second)
	fixture.clock.changeWall(365 * 24 * time.Hour)
	if got := coordinator.admitRetry(context.Background(), scope); got != "backoff_active" {
		t.Fatalf("wall-change admission outcome = %q", got)
	}
	assertMSP04CRetryTuple(t, coordinator, scope, "BACKOFF_ACTIVE", 1, 3*time.Second)
}

func TestMSP04CG11CheckpointRestartAndMonotonicRearm(t *testing.T) {
	fixture := newMSP04CFixture(t)
	scope := msp04cOrdinal(161)
	fixture.store.view.control.quarantines = []firstTrustQuarantineRecord{{
		scope: scope, reason: "RETRYABLE_FAILURE", state: "BACKOFF_ACTIVE", attemptCount: 2,
		backoffStep: 1, remainingDelay: 6 * time.Second, retentionBudget: 30 * time.Second,
		lastControlEpoch: fixture.store.view.control.controlEpoch,
	}}
	coordinator := fixture.newCoordinator()
	_ = coordinator.reopen(context.Background())
	assertMSP04CRetryTuple(t, coordinator, scope, "BACKOFF_ACTIVE", 2, 6*time.Second)

	fixture.clock.advanceMonotonic(2 * time.Second)
	if got := coordinator.checkpointRetry(context.Background(), scope); got != "checkpoint_durable" {
		t.Fatalf("checkpoint outcome = %q", got)
	}
	assertMSP04CRetryTuple(t, coordinator, scope, "BACKOFF_ACTIVE", 2, 4*time.Second)
	if fixture.store.view.control.quarantines[0].remainingDelay != 4*time.Second {
		t.Fatal("checkpoint did not persist the reduced remainder")
	}

	fixture.clock.changeWall(-730 * 24 * time.Hour)
	fixture.clock.mu.Lock()
	fixture.clock.monotonic = 100 * time.Second
	fixture.clock.mu.Unlock()
	restarted := fixture.newCoordinator()
	_ = restarted.reopen(context.Background())
	assertMSP04CRetryTuple(t, restarted, scope, "BACKOFF_ACTIVE", 2, 4*time.Second)

	fixture.clock.advanceMonotonic(3999 * time.Millisecond)
	if got := restarted.admitRetry(context.Background(), scope); got != "backoff_active" {
		t.Fatalf("pre-deadline admission outcome = %q", got)
	}
	assertMSP04CRetryTuple(t, restarted, scope, "BACKOFF_ACTIVE", 2, 4*time.Second)
	fixture.clock.advanceMonotonic(time.Millisecond)
	commitsBefore := fixture.store.calls()
	if got := restarted.admitRetry(context.Background(), scope); got != "retry_admitted" {
		t.Fatalf("deadline admission outcome = %q", got)
	}
	if fixture.store.calls() != commitsBefore+1 {
		t.Fatal("deadline did not durably publish RETRY_READY before admission")
	}
	assertMSP04CRetryTuple(t, restarted, scope, "RETRY_READY", 2, 0)
}

func TestMSP04CG11CheckpointFailureNeverShortensPersistedRemainder(t *testing.T) {
	fixture := newMSP04CFixture(t)
	scope := msp04cOrdinal(171)
	fixture.store.view.control.quarantines = []firstTrustQuarantineRecord{{
		scope: scope, reason: "RETRYABLE_FAILURE", state: "BACKOFF_ACTIVE", attemptCount: 2,
		remainingDelay: 6 * time.Second, retentionBudget: 30 * time.Second,
	}}
	coordinator := fixture.newCoordinator()
	_ = coordinator.reopen(context.Background())
	fixture.clock.advanceMonotonic(2 * time.Second)
	fixture.store.commitOutcome = "commit_not_published"
	if got := coordinator.checkpointRetry(context.Background(), scope); got != "checkpoint_failed_closed" {
		t.Fatalf("checkpoint outcome = %q", got)
	}
	if fixture.store.view.control.quarantines[0].remainingDelay != 6*time.Second {
		t.Fatal("failed checkpoint shortened the durable remainder")
	}
	restarted := fixture.newCoordinator()
	_ = restarted.reopen(context.Background())
	assertMSP04CRetryTuple(t, restarted, scope, "BACKOFF_ACTIVE", 2, 6*time.Second)
}

func TestMSP04CG11CheckpointAtDeadlinePersistsRetryReady(t *testing.T) {
	fixture := newMSP04CFixture(t)
	scope := msp04cOrdinal(176)
	fixture.store.view.control.quarantines = []firstTrustQuarantineRecord{{
		scope: scope, reason: "RETRYABLE_FAILURE", state: "BACKOFF_ACTIVE", attemptCount: 2,
		backoffStep: 1, remainingDelay: 4 * time.Second, retentionBudget: 30 * time.Second,
		lastControlEpoch: fixture.store.view.control.controlEpoch,
	}}
	coordinator := fixture.newCoordinator()
	_ = coordinator.reopen(context.Background())
	fixture.clock.advanceMonotonic(4 * time.Second)
	if got := coordinator.checkpointRetry(context.Background(), scope); got != "checkpoint_durable" {
		t.Fatalf("deadline checkpoint = %q", got)
	}
	assertMSP04CRetryTuple(t, coordinator, scope, "RETRY_READY", 2, 0)
	if fixture.store.view.control.quarantines[0].state != "RETRY_READY" || fixture.store.view.control.quarantines[0].remainingDelay != 0 {
		t.Fatal("deadline checkpoint persisted BACKOFF_ACTIVE with a zero remainder")
	}
	restarted := fixture.newCoordinator()
	_ = restarted.reopen(context.Background())
	assertMSP04CRetryTuple(t, restarted, scope, "RETRY_READY", 2, 0)
}

func TestMSP04CG11ReadyTransitionMustBeDurableBeforeAdmission(t *testing.T) {
	fixture := newMSP04CFixture(t)
	scope := msp04cOrdinal(181)
	fixture.store.view.control.quarantines = []firstTrustQuarantineRecord{{
		scope: scope, reason: "RETRYABLE_FAILURE", state: "BACKOFF_ACTIVE", attemptCount: 2,
		remainingDelay: time.Second, retentionBudget: 30 * time.Second,
	}}
	coordinator := fixture.newCoordinator()
	_ = coordinator.reopen(context.Background())
	fixture.clock.advanceMonotonic(time.Second)
	fixture.store.commitOutcome = "commit_not_published"
	if got := coordinator.admitRetry(context.Background(), scope); got != "ready_transition_failed_closed" {
		t.Fatalf("ready-transition outcome = %q", got)
	}
	state, count, remainder, ok := coordinator.retryState(scope)
	if !ok || state != "BACKOFF_ACTIVE" || count != 2 || remainder != time.Second {
		t.Fatalf("failed ready transition tuple = %s/%d/%s/%t", state, count, remainder, ok)
	}
}

func TestMSP04CRRetryFailureNotPublishedLeavesDurablePendingHold(t *testing.T) {
	fixture := newMSP04CFixture(t)
	scope := msp04cOrdinal(186)
	fixture.store.view.control.quarantines = []firstTrustQuarantineRecord{{
		scope: scope, reason: "RETRYABLE_FAILURE", state: "RETRY_READY", retentionBudget: firstTrustQuarantineRetention,
	}}
	coordinator := fixture.newCoordinator()
	_ = coordinator.reopen(context.Background())
	if got := coordinator.admitRetry(context.Background(), scope); got != "retry_admitted" {
		t.Fatalf("retry admission = %q", got)
	}
	fixture.store.commitOutcome = "commit_not_published"
	if got := coordinator.recordRetryFailure(context.Background(), scope); got != "failure_state_failed_closed" {
		t.Fatalf("failure publication = %q", got)
	}
	coordinator.mu.Lock()
	pending := coordinator.anchorRecord.pending != nil
	state, reason := coordinator.recovery, coordinator.recoveryReasonCode
	coordinator.mu.Unlock()
	if !pending || state != "QUARANTINED" || reason != "DURABILITY_UNKNOWN" {
		t.Fatalf("fail-closed state = pending:%t %s/%s", pending, state, reason)
	}

	restarted := fixture.newCoordinator()
	_ = restarted.reopen(context.Background())
	if restarted.recoveryState() != "QUARANTINED" || restarted.recoveryReason() != "DURABILITY_UNKNOWN" {
		t.Fatalf("restart classification = %s/%s", restarted.recoveryState(), restarted.recoveryReason())
	}
	if got := restarted.admitRetry(context.Background(), scope); got != "reconciliation_required" {
		t.Fatalf("restart admission = %q", got)
	}
}

func TestMSP04CG11PolicyBoundsAndCheckedSaturation(t *testing.T) {
	if firstTrustBackoffBase <= 0 || firstTrustBackoffMaximum < firstTrustBackoffBase || firstTrustAttemptMaximum < 1 || firstTrustBackoffExponentCap < 0 || firstTrustMaximumQuarantineRecords < 1 || firstTrustQuarantineRetention <= 0 {
		t.Fatal("production backoff constants violate the closed source bounds")
	}
	production := firstTrustBackoffPolicy{
		base: firstTrustBackoffBase, exponentCap: firstTrustBackoffExponentCap,
		maximum: firstTrustBackoffMaximum, attemptMaximum: firstTrustAttemptMaximum,
	}
	count, delay, ok := firstTrustNextBackoff(production, firstTrustAttemptMaximum)
	if !ok || count != firstTrustAttemptMaximum || delay != 0 {
		t.Fatalf("production saturation tuple = %d/%s/%t", count, delay, ok)
	}

	overflow := firstTrustBackoffPolicy{
		base: time.Duration(math.MaxInt64/2 + 1), exponentCap: 63,
		maximum: time.Duration(math.MaxInt64), attemptMaximum: math.MaxUint16,
	}
	count, delay, ok = firstTrustNextBackoff(overflow, 63)
	if !ok || count != 64 || delay != overflow.maximum {
		t.Fatalf("checked overflow tuple = %d/%s/%t", count, delay, ok)
	}

	invalid := []firstTrustBackoffPolicy{
		{base: 0, exponentCap: 2, maximum: 10 * time.Second, attemptMaximum: 4},
		{base: 3 * time.Second, exponentCap: 2, maximum: 2 * time.Second, attemptMaximum: 4},
		{base: 3 * time.Second, exponentCap: -1, maximum: 10 * time.Second, attemptMaximum: 4},
		{base: 3 * time.Second, exponentCap: 2, maximum: 10 * time.Second, attemptMaximum: 0},
	}
	for index, policy := range invalid {
		if _, _, valid := firstTrustNextBackoff(policy, 0); valid {
			t.Fatalf("invalid policy %d was accepted", index)
		}
	}
}

func TestMSP04CG11ActiveRecordsAreNeverEvicted(t *testing.T) {
	fixture := newMSP04CFixture(t)
	fixture.store.view.control.quarantines = make([]firstTrustQuarantineRecord, firstTrustMaximumQuarantineRecords)
	for index := range fixture.store.view.control.quarantines {
		fixture.store.view.control.quarantines[index] = firstTrustQuarantineRecord{
			scope: msp04cOrdinal(uint64(200 + index)), reason: "RETRYABLE_FAILURE", state: "BACKOFF_ACTIVE",
			attemptCount: 1, remainingDelay: time.Second, retentionBudget: firstTrustQuarantineRetention,
		}
	}
	coordinator := fixture.newCoordinator()
	_ = coordinator.reopen(context.Background())
	original := append([]firstTrustQuarantineRecord(nil), fixture.store.view.control.quarantines...)
	if got := coordinator.admitRetry(context.Background(), msp04cOrdinal(999)); got != "quarantine_capacity" {
		t.Fatalf("capacity outcome = %q", got)
	}
	if len(fixture.store.view.control.quarantines) != len(original) {
		t.Fatal("capacity handling changed the active-record count")
	}
	for index := range original {
		if fixture.store.view.control.quarantines[index] != original[index] {
			t.Fatalf("active record %d was changed or evicted", index)
		}
	}
	if fixture.effects.registerCount() != 0 {
		t.Fatal("capacity handling restored trust")
	}
}

func assertMSP04CRetryTuple(t *testing.T, coordinator *firstTrustCoordinator, scope [32]byte, wantState string, wantCount uint64, wantRemainder time.Duration) {
	t.Helper()
	state, count, remainder, ok := coordinator.retryState(scope)
	if !ok || state != wantState || count != wantCount || remainder != wantRemainder {
		t.Fatalf("retry tuple = %s/%d/%s/%t, want %s/%d/%s/true", state, count, remainder, ok, wantState, wantCount, wantRemainder)
	}
}
