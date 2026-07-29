package eebusmutation

import (
	"testing"

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
	rollbackReadback := first.readStep(first.before)
	rollbackReadback.terminal = issue85ErrorWith(
		eebusraw.ErrorCodeV1Disconnected,
		"rollback readback unavailable",
		true,
	)
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
	uncertain, terminal := first.rollback(
		applied.MutationRef,
		"issue97-uncertain-rollback",
	)
	issue85AssertError(t, terminal, eebusraw.ErrorCodeV1RollbackFailed)
	issue85AssertState(t, uncertain, eebusraw.MutationStateV1OutcomeUnknown)
	issue87AssertCanonicalMutation(t, uncertain)
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
