package eebusmutation

import (
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

func TestIssue97UncertainRollbackPersistsRestartValidQuarantine(t *testing.T) {
	first := newIssue85Harness(t)
	rollbackWrite := first.writeStep(first.before, rawMutationWriteResult{
		FrameSent:  true,
		Correlated: false,
	})
	rollbackWrite.terminal = issue85ErrorWith(
		eebusraw.ErrorCodeV1Timeout,
		"rollback reply unavailable",
		true,
	)
	releaseRollbackReadback := make(chan struct{})
	rollbackReadback := first.readStep(first.before)
	rollbackReadback.terminal = issue85ErrorWith(
		eebusraw.ErrorCodeV1Disconnected,
		"rollback readback unavailable",
		true,
	)
	rollbackReadback.block = releaseRollbackReadback
	first.executor.setSteps(
		[]issue85ReadStep{
			first.readStep(first.before),
			first.readStep(first.requested),
			first.readStep(first.requested),
			rollbackReadback,
		},
		[]issue85WriteStep{
			first.writeStep(first.requested, rawMutationWriteResult{
				FrameSent:  true,
				Correlated: true,
				Accepted:   true,
			}),
			rollbackWrite,
		},
	)

	applied, terminal := first.set()
	issue85AssertNoError(t, terminal)
	type rollbackResult struct {
		mutation eebusraw.MutationV1
		terminal *eebusraw.ErrorV1
	}
	rollbackDone := make(chan rollbackResult, 1)
	go func() {
		mutation, rollbackTerminal := first.rollback(
			applied.MutationRef,
			"issue97-uncertain-rollback",
		)
		rollbackDone <- rollbackResult{
			mutation: mutation,
			terminal: rollbackTerminal,
		}
	}()
	waitForIssue85Calls(t, first.executor, 4, 2)
	first.clock.Advance(time.Second)
	close(releaseRollbackReadback)
	result := <-rollbackDone
	uncertain, terminal := result.mutation, result.terminal
	issue85AssertError(t, terminal, eebusraw.ErrorCodeV1RollbackFailed)
	issue85AssertState(t, uncertain, eebusraw.MutationStateV1OutcomeUnknown)
	issue87AssertCanonicalMutation(t, uncertain)
	durable, statusError := first.status(applied.MutationRef)
	issue85AssertNoError(t, statusError)
	if uncertain.OutcomeEvidence == nil ||
		durable.OutcomeEvidence == nil ||
		uncertain.OutcomeEvidence.RecordedAt != durable.OutcomeEvidence.RecordedAt {
		t.Fatalf(
			"rollback response diverged from durable status:\nresponse=%+v\nstatus=%+v",
			uncertain.OutcomeEvidence,
			durable.OutcomeEvidence,
		)
	}
	first.closeClean()

	restartExecutor := &issue85Executor{t: t, events: first.events}
	restartScheduler := newIssue85Scheduler(first.clock, first.events)
	restarted := newIssue85Harness(
		t,
		issue85WithRoot(first.root),
		issue85WithExecutor(restartExecutor),
		issue85WithTokenVerifier(first.tokens),
		issue85WithPolicy(first.policy),
		issue85WithClock(first.clock),
		issue85WithScheduler(restartScheduler),
		issue85WithEvents(first.events),
	)

	status, statusError := restarted.status(applied.MutationRef)
	issue85AssertNoError(t, statusError)
	issue85AssertState(t, status, eebusraw.MutationStateV1OutcomeUnknown)
	issue87AssertCanonicalMutation(t, status)
	if status.OutcomeEvidence == nil ||
		status.OutcomeEvidence.RecordedAt != durable.OutcomeEvidence.RecordedAt {
		t.Fatalf(
			"restart changed durable rollback evidence:\nbefore=%+v\nafter=%+v",
			durable.OutcomeEvidence,
			status.OutcomeEvidence,
		)
	}
	if status.Error == nil ||
		status.Error.Code != eebusraw.ErrorCodeV1RollbackFailed ||
		status.Rollback == nil ||
		status.Rollback.Error == nil ||
		status.Rollback.Error.Code != eebusraw.ErrorCodeV1RollbackFailed {
		t.Fatalf("restart changed rollback-failure evidence: %+v", status)
	}
	reads, writes, _, exhausted := restartExecutor.counts()
	if reads != 0 || writes != 0 || exhausted != 0 {
		t.Fatalf(
			"restart retried uncertain rollback: reads:%d writes:%d exhausted:%d",
			reads,
			writes,
			exhausted,
		)
	}
	if restartScheduler.pendingCount() != 0 {
		t.Fatalf(
			"quarantined rollback scheduled %d automatic recovery callbacks",
			restartScheduler.pendingCount(),
		)
	}
}

func TestIssue97ResolvedUncertainRollbackDoesNotRetainQuarantine(t *testing.T) {
	harness := newIssue85Harness(t)
	rollbackWrite := harness.writeStep(harness.before, rawMutationWriteResult{
		FrameSent:  true,
		Correlated: false,
	})
	rollbackWrite.terminal = issue85ErrorWith(
		eebusraw.ErrorCodeV1Timeout,
		"rollback reply unavailable",
		true,
	)
	harness.executor.setSteps(
		[]issue85ReadStep{
			harness.readStep(harness.before),
			harness.readStep(harness.requested),
			harness.readStep(harness.requested),
			harness.readStep(harness.before),
		},
		[]issue85WriteStep{
			harness.writeStep(harness.requested, rawMutationWriteResult{
				FrameSent:  true,
				Correlated: true,
				Accepted:   true,
			}),
			rollbackWrite,
		},
	)

	applied, terminal := harness.set()
	issue85AssertNoError(t, terminal)
	rolledBack, terminal := harness.rollback(
		applied.MutationRef,
		"issue97-resolved-rollback",
	)
	issue85AssertNoError(t, terminal)
	issue85AssertState(t, rolledBack, eebusraw.MutationStateV1RolledBack)
	issue87AssertCanonicalMutation(t, rolledBack)

	replayed, terminal := harness.rollback(
		applied.MutationRef,
		"issue97-resolved-rollback",
	)
	issue85AssertNoError(t, terminal)
	issue85AssertState(t, replayed, eebusraw.MutationStateV1RolledBack)
	reads, writes, _, exhausted := harness.executor.counts()
	if reads != 4 || writes != 2 || exhausted != 0 {
		t.Fatalf(
			"idempotent replay contacted remote: reads:%d writes:%d exhausted:%d",
			reads,
			writes,
			exhausted,
		)
	}
}
