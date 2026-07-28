package eebusmutation

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

func TestIssue87CoordinatorOutputsPassCanonicalMutationValidation(t *testing.T) {
	harness := newIssue85Harness(t)

	applied, terminal := harness.set()
	issue85AssertNoError(t, terminal)
	issue87AssertCanonicalMutation(t, applied)

	status, terminal := harness.status(applied.MutationRef)
	issue85AssertNoError(t, terminal)
	issue87AssertCanonicalMutation(t, status)

	harness.executor.setSteps(
		[]issue85ReadStep{
			harness.readStep(harness.requested),
			harness.readStep(harness.before),
		},
		[]issue85WriteStep{
			harness.writeStep(harness.before, rawMutationWriteResult{
				FrameSent:  true,
				Correlated: true,
				Accepted:   true,
			}),
		},
	)
	rolledBack, terminal := harness.rollback(
		applied.MutationRef,
		"issue87-rollback-key",
	)
	issue85AssertNoError(t, terminal)
	issue87AssertCanonicalMutation(t, rolledBack)
}

func TestIssue87ExceptionalCoordinatorOutputsHaveClosedValidation(t *testing.T) {
	t.Run("failed no contact", func(t *testing.T) {
		harness := newIssue85Harness(t)
		step := harness.readStep(harness.before)
		step.terminal = issue85ErrorWith(
			eebusraw.ErrorCodeV1Disconnected,
			"guard read disconnected",
			true,
		)
		harness.executor.setSteps([]issue85ReadStep{step}, nil)

		mutation, terminal := harness.set()
		issue85AssertError(t, terminal, eebusraw.ErrorCodeV1Disconnected)
		issue87AssertCanonicalMutation(t, mutation)

		impossible := cloneMutation(mutation)
		impossible.NoContactEvidence.RemoteFramesSent = 1
		issue87AssertInvalidMutation(t, impossible)
		impossible = cloneMutation(mutation)
		impossible.NoContactEvidence.LastCompletedPhase = "unknown"
		issue87AssertInvalidMutation(t, impossible)
	})

	t.Run("correlated contradiction", func(t *testing.T) {
		harness := newIssue85Harness(t)
		harness.executor.setSteps(
			[]issue85ReadStep{
				harness.readStep(harness.before),
				harness.readStep(harness.before),
			},
			[]issue85WriteStep{
				harness.writeStep(harness.requested, rawMutationWriteResult{
					FrameSent:  true,
					Correlated: true,
					Accepted:   true,
				}),
			},
		)

		mutation, terminal := harness.set()
		issue85AssertError(t, terminal, eebusraw.ErrorCodeV1OutcomeUnknown)
		issue87AssertCanonicalMutation(t, mutation)

		impossible := cloneMutation(mutation)
		impossible.ProtocolAccepted = nil
		issue87AssertInvalidMutation(t, impossible)
		impossible = cloneMutation(mutation)
		impossible.OutcomeEvidence.PossibleSideEffect = false
		issue87AssertInvalidMutation(t, impossible)
	})

	t.Run("rollback failure", func(t *testing.T) {
		harness := newIssue85Harness(t)
		harness.request.Mode = eebusraw.ModeV1Probe
		harness.request.ProbeTTLSeconds = 1
		failedRead := harness.readStep(harness.requested)
		failedRead.terminal = issue85Error(eebusraw.ErrorCodeV1Disconnected)
		harness.executor.setSteps(
			[]issue85ReadStep{
				harness.readStep(harness.before),
				harness.readStep(harness.requested),
				failedRead,
			},
			[]issue85WriteStep{
				harness.writeStep(harness.requested, rawMutationWriteResult{
					FrameSent:  true,
					Correlated: true,
					Accepted:   true,
				}),
			},
		)

		probe, terminal := harness.set()
		issue85AssertNoError(t, terminal)
		harness.clock.Advance(time.Second)
		if fired := harness.scheduler.FireDue(); fired != 1 {
			t.Fatalf("probe rollback callbacks fired = %d, want 1", fired)
		}
		mutation, terminal := harness.status(probe.MutationRef)
		issue85AssertNoError(t, terminal)
		issue87AssertCanonicalMutation(t, mutation)

		impossible := cloneMutation(mutation)
		impossible.OutcomeEvidence.BlindRetryForbidden = true
		issue87AssertInvalidMutation(t, impossible)
		impossible = cloneMutation(mutation)
		accepted := true
		impossible.Rollback.ProtocolAccepted = &accepted
		issue87AssertInvalidMutation(t, impossible)
	})
}

func TestIssue87RollbackConflictBindsOriginalBeforeImage(t *testing.T) {
	harness := newIssue85Harness(t)
	applied, terminal := harness.set()
	issue85AssertNoError(t, terminal)
	harness.executor.setSteps(
		[]issue85ReadStep{harness.readStep(harness.third)},
		nil,
	)

	conflict, terminal := harness.rollback(
		applied.MutationRef,
		"issue87-rollback-conflict",
	)
	issue85AssertError(t, terminal, eebusraw.ErrorCodeV1Conflict)
	issue87AssertCanonicalMutation(t, conflict)
	if conflict.Rollback == nil {
		t.Fatal("rollback conflict did not retain rollback evidence")
	}

	substituted := cloneMutation(conflict)
	substituted.Rollback.Before = harness.third.Clone()
	issue87AssertInvalidMutation(t, substituted)
}

func issue87AssertCanonicalMutation(t *testing.T, mutation eebusraw.MutationV1) {
	t.Helper()
	if terminal := eebusraw.ValidateMutationV1(mutation); terminal != nil {
		t.Fatalf("coordinator mutation failed canonical validation: %+v\nmutation: %+v", terminal, mutation)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(mutation.MutationRef)
	if err != nil {
		t.Fatalf("mutation_ref %q is not unpadded base64url: %v", mutation.MutationRef, err)
	}
	if len(decoded) != 32 {
		t.Fatalf("mutation_ref decoded length = %d, want 32", len(decoded))
	}
}

func issue87AssertInvalidMutation(t *testing.T, mutation eebusraw.MutationV1) {
	t.Helper()
	if terminal := eebusraw.ValidateMutationV1(mutation); terminal == nil {
		t.Fatalf("impossible coordinator mutation passed canonical validation: %+v", mutation)
	}
}
