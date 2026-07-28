package eebusmutation

import (
	"context"
	"reflect"
	"testing"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

func TestIssue85PossibleSendRecoveryToBeforeIsExactNoEffect(t *testing.T) {
	harness := newIssue85Harness(t)
	write := harness.writeStep(harness.requested, rawMutationWriteResult{
		FrameSent:  true,
		Correlated: false,
	})
	write.terminal = issue85ErrorWith(
		eebusraw.ErrorCodeV1Timeout,
		"possible-send timeout",
		true,
	)
	harness.executor.setSteps(
		[]issue85ReadStep{
			harness.readStep(harness.before),
			harness.readStep(harness.before),
		},
		[]issue85WriteStep{write},
	)

	mutation, terminal := harness.set()
	issue85AssertError(t, terminal, eebusraw.ErrorCodeV1NoEffect)
	if terminal.Retriable {
		t.Fatalf("no_effect terminal is retriable: %+v", terminal)
	}
	issue85AssertState(t, mutation, eebusraw.MutationStateV1NoEffect)
	if mutation.ProtocolAccepted != nil {
		t.Fatalf("no_effect protocol_accepted = %v, want nil", mutation.ProtocolAccepted)
	}
	if mutation.ObservedAfter == nil || !issue85ValuesEqual(*mutation.ObservedAfter, harness.before) {
		t.Fatalf("no_effect observed_after = %+v, want verified before-image", mutation.ObservedAfter)
	}
	if mutation.Error == nil ||
		mutation.Error.Code != eebusraw.ErrorCodeV1NoEffect ||
		mutation.Error.Retriable {
		t.Fatalf("no_effect error = %+v", mutation.Error)
	}
	beforeHash, err := harness.before.ComputeHash()
	if err != nil {
		t.Fatal(err)
	}
	verification := mutation.NoEffectVerification
	if verification == nil ||
		verification.Relation != "observed_after_equals_before" ||
		!verification.Verified ||
		verification.EqualValueHash != beforeHash ||
		verification.VerifiedAt.IsZero() {
		t.Fatalf("no_effect verification = %+v", verification)
	}
	evidence := mutation.OutcomeEvidence
	if evidence == nil ||
		!evidence.PossibleSideEffect ||
		!evidence.BlindRetryForbidden ||
		evidence.LastDurableState != eebusraw.MutationStateV1DispatchIntent ||
		evidence.RecordedAt.IsZero() {
		t.Fatalf("no_effect uncertainty evidence = %+v", evidence)
	}
	if mutation.ApplyVerification != nil {
		t.Fatalf("no_effect claims apply verification: %+v", mutation.ApplyVerification)
	}
	reads, writes, _, exhausted := harness.executor.counts()
	if reads != 2 || writes != 1 || exhausted != 0 {
		t.Fatalf("no_effect calls = reads:%d writes:%d exhausted:%d", reads, writes, exhausted)
	}
}

func TestIssue85PossibleSendRequestedRecoveryRequiresUncertaintyAndEquality(t *testing.T) {
	for _, mode := range []eebusraw.ModeV1{eebusraw.ModeV1Apply, eebusraw.ModeV1Probe} {
		t.Run(string(mode), func(t *testing.T) {
			harness := newIssue85Harness(t)
			harness.request.Mode = mode
			if mode == eebusraw.ModeV1Probe {
				harness.request.ProbeTTLSeconds = 60
			}
			write := harness.writeStep(harness.requested, rawMutationWriteResult{
				FrameSent:  true,
				Correlated: false,
			})
			write.terminal = issue85ErrorWith(
				eebusraw.ErrorCodeV1Disconnected,
				"possible-send disconnect",
				true,
			)
			harness.executor.setSteps(
				[]issue85ReadStep{
					harness.readStep(harness.before),
					harness.readStep(harness.requested),
				},
				[]issue85WriteStep{write},
			)

			mutation, terminal := harness.set()
			issue85AssertNoError(t, terminal)
			wantState := eebusraw.MutationStateV1Applied
			if mode == eebusraw.ModeV1Probe {
				wantState = eebusraw.MutationStateV1ProbeActive
			}
			issue85AssertState(t, mutation, wantState)
			if mutation.ProtocolAccepted != nil {
				t.Fatalf("uncertain requested recovery protocol_accepted = %v, want nil", mutation.ProtocolAccepted)
			}
			if mutation.ObservedAfter == nil ||
				!issue85ValuesEqual(*mutation.ObservedAfter, harness.requested) {
				t.Fatalf("requested recovery observed_after = %+v", mutation.ObservedAfter)
			}
			requestedHash, err := harness.requested.ComputeHash()
			if err != nil {
				t.Fatal(err)
			}
			if mutation.ApplyVerification == nil ||
				mutation.ApplyVerification.Relation != "observed_after_equals_requested" ||
				!mutation.ApplyVerification.Verified ||
				mutation.ApplyVerification.EqualValueHash != requestedHash {
				t.Fatalf("requested recovery verification = %+v", mutation.ApplyVerification)
			}
			if mutation.OutcomeEvidence == nil ||
				!mutation.OutcomeEvidence.PossibleSideEffect ||
				!mutation.OutcomeEvidence.BlindRetryForbidden ||
				mutation.OutcomeEvidence.LastDurableState != eebusraw.MutationStateV1DispatchIntent {
				t.Fatalf("requested recovery omitted uncertainty evidence: %+v", mutation.OutcomeEvidence)
			}
			_, writes, _, exhausted := harness.executor.counts()
			if writes != 1 || exhausted != 0 {
				t.Fatalf("requested recovery blindly retried: writes=%d exhausted=%d", writes, exhausted)
			}
		})
	}
}

func TestIssue85CorrelatedRejectionIsDurablyVerifiedAgainstBeforeImage(t *testing.T) {
	harness := newIssue85Harness(t)
	write := harness.writeStep(harness.requested, rawMutationWriteResult{
		FrameSent:  true,
		Correlated: true,
		Accepted:   false,
	})
	write.terminal = issue85ErrorWith(
		eebusraw.ErrorCodeV1RemoteError,
		"correlated remote rejection",
		false,
	)
	harness.executor.setSteps(
		[]issue85ReadStep{
			harness.readStep(harness.before),
			harness.readStep(harness.before),
		},
		[]issue85WriteStep{write},
	)

	mutation, terminal := harness.set()
	issue85AssertError(t, terminal, eebusraw.ErrorCodeV1RemoteError)
	if terminal.Retriable {
		t.Fatalf("correlated rejection terminal is retriable: %+v", terminal)
	}
	issue85AssertCorrelatedRejection(t, mutation, harness.before)
	harness.closeClean()

	events := &issue85EventLog{}
	executor := &issue85Executor{t: t, events: events}
	executor.setSteps(nil, nil)
	scheduler := newIssue85Scheduler(harness.clock, events)
	restarted := newIssue85Harness(
		t,
		issue85WithRoot(harness.root),
		issue85WithExecutor(executor),
		issue85WithClock(harness.clock),
		issue85WithScheduler(scheduler),
		issue85WithEvents(events),
	)
	durable, statusError := restarted.status(mutation.MutationRef)
	issue85AssertNoError(t, statusError)
	issue85AssertCorrelatedRejection(t, durable, harness.before)
	if !reflect.DeepEqual(durable.ObservedAfter, mutation.ObservedAfter) ||
		!reflect.DeepEqual(durable.RejectionVerification, mutation.RejectionVerification) ||
		!reflect.DeepEqual(durable.Error, mutation.Error) {
		t.Fatalf(
			"restart changed durable rejection evidence:\noriginal=%+v\nrestarted=%+v",
			mutation,
			durable,
		)
	}
	reads, writes, _, exhausted := executor.counts()
	if reads != 0 || writes != 0 || exhausted != 0 {
		t.Fatalf("durable rejection recovery contacted remote: reads:%d writes:%d exhausted:%d", reads, writes, exhausted)
	}
}

func TestIssue85CorrelatedRejectionCannotBecomeAppliedFromRequestedReadback(t *testing.T) {
	harness := newIssue85Harness(t)
	write := harness.writeStep(harness.requested, rawMutationWriteResult{
		FrameSent:  true,
		Correlated: true,
		Accepted:   false,
	})
	write.terminal = issue85ErrorWith(
		eebusraw.ErrorCodeV1RemoteError,
		"correlated remote rejection with contradictory readback",
		false,
	)
	harness.executor.setSteps(
		[]issue85ReadStep{
			harness.readStep(harness.before),
			harness.readStep(harness.requested),
		},
		[]issue85WriteStep{write},
	)

	mutation, terminal := harness.set()
	if terminal == nil {
		t.Fatalf("correlated rejection with changed readback returned success: %+v", mutation)
	}
	if mutation.ProtocolAccepted == nil || *mutation.ProtocolAccepted {
		t.Fatalf("rejected protocol_accepted = %v, want false", mutation.ProtocolAccepted)
	}
	if mutation.State == eebusraw.MutationStateV1Applied ||
		mutation.State == eebusraw.MutationStateV1ProbeActive ||
		mutation.ApplyVerification != nil {
		t.Fatalf("correlated rejection was rewritten as success: %+v", mutation)
	}
	_, writes, _, exhausted := harness.executor.counts()
	if writes != 1 || exhausted != 0 {
		t.Fatalf("correlated rejection writes=%d exhausted=%d", writes, exhausted)
	}
}

func issue85AssertCorrelatedRejection(
	t testing.TB,
	mutation eebusraw.MutationV1,
	before eebusraw.TypedValueV1,
) {
	t.Helper()
	issue85AssertState(t, mutation, eebusraw.MutationStateV1Rejected)
	if mutation.ProtocolAccepted == nil || *mutation.ProtocolAccepted {
		t.Fatalf("rejected protocol_accepted = %v, want false", mutation.ProtocolAccepted)
	}
	if mutation.ObservedAfter == nil || !issue85ValuesEqual(*mutation.ObservedAfter, before) {
		t.Fatalf("rejected observed_after = %+v, want verified before-image", mutation.ObservedAfter)
	}
	beforeHash, err := before.ComputeHash()
	if err != nil {
		t.Fatal(err)
	}
	verification := mutation.RejectionVerification
	if verification == nil ||
		verification.Relation != "observed_after_equals_before" ||
		!verification.Verified ||
		!verification.CorrelatedRejection ||
		verification.EqualValueHash != beforeHash ||
		verification.VerifiedAt.IsZero() {
		t.Fatalf("rejection verification = %+v", verification)
	}
	if mutation.Error == nil ||
		mutation.Error.Code != eebusraw.ErrorCodeV1RemoteError ||
		mutation.Error.Retriable {
		t.Fatalf("durable rejection error = %+v", mutation.Error)
	}
	if mutation.ApplyVerification != nil ||
		mutation.NoEffectVerification != nil ||
		mutation.OutcomeEvidence != nil {
		t.Fatalf("rejected mutation carries incompatible evidence: %+v", mutation)
	}
}

func TestIssue85PossibleSendThirdValueConflictsAndQuarantines(t *testing.T) {
	harness := newIssue85Harness(t)
	write := harness.writeStep(harness.requested, rawMutationWriteResult{
		FrameSent:  true,
		Correlated: false,
	})
	write.terminal = issue85ErrorWith(
		eebusraw.ErrorCodeV1Cancelled,
		"possible-send cancellation",
		true,
	)
	harness.executor.setSteps(
		[]issue85ReadStep{
			harness.readStep(harness.before),
			harness.readStep(harness.third),
		},
		[]issue85WriteStep{write},
	)

	mutation, terminal := harness.set()
	issue85AssertError(t, terminal, eebusraw.ErrorCodeV1Conflict)
	issue85AssertState(t, mutation, eebusraw.MutationStateV1Conflict)
	if mutation.ProtocolAccepted != nil ||
		mutation.ConflictEvidence == nil ||
		mutation.ConflictEvidence.Relation != "observed_after_differs_from_before_and_requested" ||
		!mutation.ConflictEvidence.Verified {
		t.Fatalf("conflict evidence = %+v", mutation)
	}
	if mutation.ObservedAfter == nil || !issue85ValuesEqual(*mutation.ObservedAfter, harness.third) {
		t.Fatalf("conflict omitted exact third value: %+v", mutation.ObservedAfter)
	}

	distinct := harness.requestForTarget(
		harness.distinctTarget(),
		"issue85-after-conflict-distinct-target-token",
		"issue85-after-conflict-distinct-target",
	)
	beforeVerify, beforeConsume := harness.tokens.counts()
	beforePolicy := harness.policy.count()
	_, blocked := harness.coordinator.FeaturesDataSet(
		context.Background(),
		harness.auth,
		distinct,
	)
	issue85AssertError(t, blocked, eebusraw.ErrorCodeV1Conflict)
	reads, writes, _, exhausted := harness.executor.counts()
	if reads != 2 || writes != 1 || exhausted != 0 {
		t.Fatalf("quarantined write contacted remote: reads:%d writes:%d exhausted:%d", reads, writes, exhausted)
	}
	if _, statusError := harness.status(mutation.MutationRef); statusError != nil {
		t.Fatalf("quarantine blocked status read: %+v", statusError)
	}
	afterVerify, afterConsume := harness.tokens.counts()
	if afterVerify != beforeVerify ||
		afterConsume != beforeConsume ||
		harness.policy.count() != beforePolicy {
		t.Fatalf(
			"global quarantine consulted distinct-target dependencies: token verify %d->%d consume %d->%d policy %d->%d",
			beforeVerify,
			afterVerify,
			beforeConsume,
			afterConsume,
			beforePolicy,
			harness.policy.count(),
		)
	}

	harness.closeClean()
	restartEvents := &issue85EventLog{}
	restartExecutor := &issue85Executor{t: t, events: restartEvents}
	restartExecutor.setSteps(nil, nil)
	restartScheduler := newIssue85Scheduler(harness.clock, restartEvents)
	restarted := newIssue85Harness(
		t,
		issue85WithRoot(harness.root),
		issue85WithExecutor(restartExecutor),
		issue85WithTokenVerifier(harness.tokens),
		issue85WithPolicy(harness.policy),
		issue85WithClock(harness.clock),
		issue85WithScheduler(restartScheduler),
		issue85WithEvents(restartEvents),
	)
	durable, statusError := restarted.status(mutation.MutationRef)
	issue85AssertNoError(t, statusError)
	issue85AssertState(t, durable, eebusraw.MutationStateV1Conflict)
	restartRequest := restarted.requestForTarget(
		restarted.targetVariant(3, 13),
		"issue85-restart-quarantine-token",
		"issue85-restart-quarantine-key",
	)
	restartVerify, restartConsume := harness.tokens.counts()
	restartPolicy := harness.policy.count()
	_, blocked = restarted.coordinator.FeaturesDataSet(
		context.Background(),
		restarted.auth,
		restartRequest,
	)
	issue85AssertError(t, blocked, eebusraw.ErrorCodeV1Conflict)
	reads, writes, _, exhausted = restartExecutor.counts()
	if reads != 0 || writes != 0 || exhausted != 0 {
		t.Fatalf("restarted quarantine contacted remote: reads:%d writes:%d exhausted:%d", reads, writes, exhausted)
	}
	afterRestartVerify, afterRestartConsume := harness.tokens.counts()
	if afterRestartVerify != restartVerify ||
		afterRestartConsume != restartConsume ||
		harness.policy.count() != restartPolicy {
		t.Fatalf(
			"restarted quarantine consulted dependencies: token verify %d->%d consume %d->%d policy %d->%d",
			restartVerify,
			afterRestartVerify,
			restartConsume,
			afterRestartConsume,
			restartPolicy,
			harness.policy.count(),
		)
	}
}

func TestIssue85UnreadableOrUntrustworthyRecoveryRemainsOutcomeUnknown(t *testing.T) {
	tests := []struct {
		name     string
		readback func(*issue85Harness) issue85ReadStep
	}{
		{
			name: "unreadable",
			readback: func(harness *issue85Harness) issue85ReadStep {
				step := harness.readStep(harness.requested)
				step.terminal = issue85ErrorWith(
					eebusraw.ErrorCodeV1DecodeError,
					"readback unavailable",
					false,
				)
				return step
			},
		},
		{
			name: "untrustworthy requested value",
			readback: func(harness *issue85Harness) issue85ReadStep {
				return harness.untrustworthyReadStep(harness.requested)
			},
		},
		{
			name: "incomplete full read",
			readback: func(harness *issue85Harness) issue85ReadStep {
				step := harness.readStep(harness.before)
				step.result.Full = false
				return step
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newIssue85Harness(t)
			write := harness.writeStep(harness.requested, rawMutationWriteResult{
				FrameSent:  true,
				Correlated: false,
			})
			write.terminal = issue85ErrorWith(
				eebusraw.ErrorCodeV1Timeout,
				"possible-send timeout",
				true,
			)
			harness.executor.setSteps(
				[]issue85ReadStep{
					harness.readStep(harness.before),
					test.readback(harness),
				},
				[]issue85WriteStep{write},
			)

			mutation, terminal := harness.set()
			issue85AssertError(t, terminal, eebusraw.ErrorCodeV1OutcomeUnknown)
			issue85AssertState(t, mutation, eebusraw.MutationStateV1OutcomeUnknown)
			if mutation.ProtocolAccepted != nil ||
				mutation.OutcomeEvidence == nil ||
				!mutation.OutcomeEvidence.PossibleSideEffect ||
				!mutation.OutcomeEvidence.BlindRetryForbidden {
				t.Fatalf("outcome_unknown evidence = %+v", mutation)
			}
			if mutation.ApplyVerification != nil ||
				mutation.NoEffectVerification != nil {
				t.Fatalf("untrustworthy readback produced equality proof: %+v", mutation)
			}
			_, writes, _, exhausted := harness.executor.counts()
			if writes != 1 || exhausted != 0 {
				t.Fatalf("outcome_unknown blindly retried: writes=%d exhausted=%d", writes, exhausted)
			}
		})
	}
}
