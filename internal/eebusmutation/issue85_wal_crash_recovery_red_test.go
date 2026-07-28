package eebusmutation

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

const (
	issue85CrashHelperEnvironment   = "HELIANTHUS_ISSUE85_CRASH_HELPER"
	issue85CrashRootEnvironment     = "HELIANTHUS_ISSUE85_CRASH_ROOT"
	issue85CrashStateEnvironment    = "HELIANTHUS_ISSUE85_CRASH_STATE"
	issue85CrashMarkerEnvironment   = "HELIANTHUS_ISSUE85_CRASH_MARKER"
	issue85CrashFailureEnvironment  = "HELIANTHUS_ISSUE85_CRASH_FAILURE"
	issue85CrashExitCode            = 85
	issue85CrashHardFailureExitCode = 86
	issue85CrashHardFailureMarker   = "harness-hard-failure"
)

func TestIssue85AbruptCrashHelper(t *testing.T) {
	if os.Getenv(issue85CrashHelperEnvironment) != "1" {
		t.Skip("subprocess helper")
	}
	root := os.Getenv(issue85CrashRootEnvironment)
	marker := os.Getenv(issue85CrashMarkerEnvironment)
	state := eebusraw.MutationStateV1(os.Getenv(issue85CrashStateEnvironment))
	if root == "" || marker == "" || state == "" {
		t.Fatal("crash helper environment is incomplete")
	}

	harness := newIssue85Harness(
		t,
		issue85WithRoot(root),
		issue85WithMarkerPath(marker),
		issue85WithAbruptCrash(state, issue85CrashExitCode),
	)
	hardFailure := func() {
		harness.events.add(issue85CrashHardFailureMarker)
		os.Exit(issue85CrashHardFailureExitCode)
	}
	harness.executor.setHardFailure(hardFailure)
	harness.policy.setHardFailure(hardFailure)
	issue85ConfigureCrashScenario(harness, state)
	switch os.Getenv(issue85CrashFailureEnvironment) {
	case "":
	case "exhausted-script":
		harness.executor.setSteps(nil, nil)
	case "wrong-target":
		read := harness.readStep(harness.before)
		read.wantTarget.FeatureAddress++
		harness.executor.setSteps([]issue85ReadStep{read}, nil)
	case "policy-exhausted":
		harness.policy.setMaxCalls(0)
	case "policy-wrong-target":
		wrongTarget := harness.target.Clone()
		wrongTarget.FeatureAddress++
		harness.policy.setExpectedTarget(wrongTarget)
	default:
		t.Fatalf("unknown forced crash-helper failure")
	}
	mutation, terminal := harness.set()
	if issue85RollbackCrashState(state) {
		if terminal != nil {
			t.Fatalf("apply before rollback crash failed: %+v", terminal)
		}
		_, terminal = harness.rollback(mutation.MutationRef, "issue85-crash-rollback")
	}
	t.Fatalf("crash state %q was not reached; terminal=%+v mutation=%+v", state, terminal, mutation)
}

func TestIssue85CrashHelperFailuresCannotMasqueradeAsExpectedCrash(t *testing.T) {
	for _, failure := range []string{
		"exhausted-script",
		"wrong-target",
		"policy-exhausted",
		"policy-wrong-target",
	} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			marker := filepath.Join(t.TempDir(), "crash-events.log")
			command := issue85CrashCommand(
				root,
				marker,
				eebusraw.MutationStateV1Prepared,
				failure,
			)
			output, err := command.CombinedOutput()
			var exitError *exec.ExitError
			if !errors.As(err, &exitError) ||
				exitError.ExitCode() != issue85CrashHardFailureExitCode {
				t.Fatalf(
					"forced helper failure %q error = %v, want exit %d\n%s",
					failure,
					err,
					issue85CrashHardFailureExitCode,
					output,
				)
			}
			events := issue85ReadMarker(t, marker)
			if issue85Index(events, issue85CrashHardFailureMarker) < 0 {
				t.Fatalf("forced helper failure %q omitted durable hard-failure marker: %v", failure, events)
			}
			if issue85Index(events, "durable-state:"+string(eebusraw.MutationStateV1Prepared)) >= 0 {
				t.Fatalf("forced helper failure %q reached the accepted crash state: %v", failure, events)
			}
		})
	}
}

func TestIssue85EveryDurableTransitionHasAnAbruptCrashBoundary(t *testing.T) {
	tests := []struct {
		state      eebusraw.MutationStateV1
		wantWrites int
	}{
		{eebusraw.MutationStateV1Prepared, 0},
		{eebusraw.MutationStateV1DispatchIntent, 0},
		{eebusraw.MutationStateV1ReplyObserved, 1},
		{eebusraw.MutationStateV1VerifyPending, 1},
		{eebusraw.MutationStateV1Applied, 1},
		{eebusraw.MutationStateV1ProbeActive, 1},
		{eebusraw.MutationStateV1RollbackIntent, 1},
		{eebusraw.MutationStateV1RollbackDispatchIntent, 1},
		{eebusraw.MutationStateV1RollbackReplyObserved, 2},
		{eebusraw.MutationStateV1RollbackVerifyPending, 2},
		{eebusraw.MutationStateV1RolledBack, 2},
		{eebusraw.MutationStateV1OutcomeUnknown, 1},
		{eebusraw.MutationStateV1Conflict, 1},
		{eebusraw.MutationStateV1FailedNoContact, 0},
		{eebusraw.MutationStateV1Rejected, 1},
		{eebusraw.MutationStateV1NoEffect, 1},
	}
	for _, test := range tests {
		t.Run(string(test.state), func(t *testing.T) {
			_, marker := issue85RunCrashProcess(t, test.state)
			events := issue85ReadMarker(t, marker)
			wantDurable := "durable-state:" + string(test.state)
			if issue85Index(events, wantDurable) < 0 {
				t.Fatalf("abrupt crash marker omitted %q: %v", wantDurable, events)
			}
			writes := 0
			for _, event := range events {
				if strings.HasPrefix(event, "remote:WRITE:") {
					writes++
				}
			}
			if writes != test.wantWrites {
				t.Fatalf("crash after %s observed %d writes, want %d: %v", test.state, writes, test.wantWrites, events)
			}
		})
	}
}

func TestIssue85JournalPersistenceSyncsEveryTransitionAndItsDirectory(t *testing.T) {
	harness := newIssue85Harness(t)
	_, terminal := harness.set()
	issue85AssertNoError(t, terminal)

	events := harness.events.snapshot()
	previousDurable := -1
	durableTransitions := 0
	for index, event := range events {
		if !strings.HasPrefix(event, "durable-state:") {
			continue
		}
		durableTransitions++
		fileSynced := false
		for candidate := previousDurable + 1; candidate < index; candidate++ {
			if events[candidate] == "persistence:synced:"+issue85PersistenceFileSync {
				fileSynced = true
				break
			}
		}
		if !fileSynced {
			t.Fatalf("durable transition %q lacks a preceding injected file sync: %v", event, events)
		}
		previousDurable = index
	}
	if durableTransitions == 0 {
		t.Fatalf("persistence trace has no durable transitions: %v", events)
	}
	directorySync := issue85Index(events, "persistence:synced:"+issue85PersistenceDirectorySync)
	firstDurable := issue85Index(events, "durable-state:"+string(eebusraw.MutationStateV1Prepared))
	if directorySync < 0 || firstDurable < 0 || directorySync > firstDurable {
		t.Fatalf("journal directory was not synced before its first durable transition: %v", events)
	}
}

func TestIssue85PersistenceSyncFaultsFailBeforeAnyWriteSideEffect(t *testing.T) {
	for _, operation := range []string{
		issue85PersistenceFileSync,
		issue85PersistenceDirectorySync,
	} {
		t.Run(operation, func(t *testing.T) {
			harness := issue85HarnessDraft(t)
			harness.persistence.failNextSync(operation)
			coordinator, openError := harness.tryOpen()
			if openError != nil {
				if coordinator != nil {
					_ = coordinator.Close()
					t.Fatal("persistence-failed open returned a live coordinator")
				}
				issue85AssertError(t, openError, eebusraw.ErrorCodeV1Internal)
			} else {
				harness.coordinator = coordinator
				_, terminal := harness.set()
				issue85AssertError(t, terminal, eebusraw.ErrorCodeV1Internal)
				if closeError := coordinator.Close(); closeError != nil {
					t.Errorf("Close() after persistence fault = %+v", closeError)
				}
				harness.coordinator = nil
			}
			events := harness.events.snapshot()
			if issue85Index(events, "persistence:attempt:"+operation) < 0 {
				t.Fatalf("persistence fault %q was not reached through the injected seam: %v", operation, events)
			}
			for _, event := range events {
				if strings.HasPrefix(event, "remote:WRITE:") {
					t.Fatalf("persistence fault %q allowed a remote side effect: %v", operation, events)
				}
			}
			_, writes, _, _ := harness.executor.counts()
			if writes != 0 {
				t.Fatalf("persistence fault %q emitted %d writes", operation, writes)
			}
		})
	}
}

func TestIssue85AbruptDispatchIntentRecoveryUsesReadbackAndNeverResends(t *testing.T) {
	tests := []struct {
		name      string
		readback  func(*issue85Harness) issue85ReadStep
		wantState eebusraw.MutationStateV1
		wantError eebusraw.ErrorCodeV1
		wantValue func(*issue85Harness) eebusraw.TypedValueV1
	}{
		{
			name: "before becomes no effect",
			readback: func(harness *issue85Harness) issue85ReadStep {
				return harness.readStep(harness.before)
			},
			wantState: eebusraw.MutationStateV1NoEffect,
			wantError: eebusraw.ErrorCodeV1NoEffect,
			wantValue: func(harness *issue85Harness) eebusraw.TypedValueV1 {
				return harness.before
			},
		},
		{
			name: "requested becomes applied with nil protocol acceptance",
			readback: func(harness *issue85Harness) issue85ReadStep {
				return harness.readStep(harness.requested)
			},
			wantState: eebusraw.MutationStateV1Applied,
			wantValue: func(harness *issue85Harness) eebusraw.TypedValueV1 {
				return harness.requested
			},
		},
		{
			name: "third value conflicts",
			readback: func(harness *issue85Harness) issue85ReadStep {
				return harness.readStep(harness.third)
			},
			wantState: eebusraw.MutationStateV1Conflict,
			wantError: eebusraw.ErrorCodeV1Conflict,
			wantValue: func(harness *issue85Harness) eebusraw.TypedValueV1 {
				return harness.third
			},
		},
		{
			name: "unreadable remains unknown",
			readback: func(harness *issue85Harness) issue85ReadStep {
				step := harness.readStep(harness.before)
				step.terminal = issue85Error(eebusraw.ErrorCodeV1DecodeError)
				return step
			},
			wantState: eebusraw.MutationStateV1OutcomeUnknown,
			wantError: eebusraw.ErrorCodeV1OutcomeUnknown,
		},
		{
			name: "untrustworthy remains unknown",
			readback: func(harness *issue85Harness) issue85ReadStep {
				return harness.untrustworthyReadStep(harness.requested)
			},
			wantState: eebusraw.MutationStateV1OutcomeUnknown,
			wantError: eebusraw.ErrorCodeV1OutcomeUnknown,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, _ := issue85RunCrashProcess(t, eebusraw.MutationStateV1DispatchIntent)
			template := issue85HarnessDraft(t)
			events := &issue85EventLog{}
			executor := &issue85Executor{t: t, events: events}
			executor.setSteps([]issue85ReadStep{test.readback(template)}, nil)
			scheduler := newIssue85Scheduler(template.clock, events)
			recovered := newIssue85Harness(
				t,
				issue85WithRoot(root),
				issue85WithExecutor(executor),
				issue85WithClock(template.clock),
				issue85WithScheduler(scheduler),
				issue85WithEvents(events),
			)

			mutation, terminal := recovered.set()
			if test.wantError == "" {
				issue85AssertNoError(t, terminal)
			} else {
				issue85AssertError(t, terminal, test.wantError)
			}
			issue85AssertState(t, mutation, test.wantState)
			if mutation.ProtocolAccepted != nil {
				t.Fatalf("dispatch-intent recovery protocol_accepted = %v, want nil", mutation.ProtocolAccepted)
			}
			if test.wantValue != nil {
				want := test.wantValue(template)
				if mutation.ObservedAfter == nil ||
					!issue85ValuesEqual(*mutation.ObservedAfter, want) {
					t.Fatalf("recovery observed_after = %+v, want %s", mutation.ObservedAfter, issue85ValueJSON(want))
				}
			}
			if mutation.OutcomeEvidence == nil ||
				!mutation.OutcomeEvidence.PossibleSideEffect ||
				!mutation.OutcomeEvidence.BlindRetryForbidden ||
				mutation.OutcomeEvidence.LastDurableState != eebusraw.MutationStateV1DispatchIntent {
				t.Fatalf("dispatch-intent recovery evidence = %+v", mutation.OutcomeEvidence)
			}
			reads, writes, _, exhausted := executor.counts()
			if reads != 1 || writes != 0 || exhausted != 0 {
				t.Fatalf("dispatch-intent recovery calls = reads:%d writes:%d exhausted:%d", reads, writes, exhausted)
			}
		})
	}
}

func TestIssue85DispatchIntentRecoveryAfterReconnectNeverBlindlyResends(t *testing.T) {
	root, _ := issue85RunCrashProcess(t, eebusraw.MutationStateV1DispatchIntent)
	template := issue85HarnessDraft(t)
	events := &issue85EventLog{}
	executor := &issue85Executor{t: t, events: events}
	readback := template.readStep(template.requested)
	readback.result.Runtime.ConnectionGeneration++
	executor.setSteps([]issue85ReadStep{readback}, nil)
	scheduler := newIssue85Scheduler(template.clock, events)
	recovered := newIssue85Harness(
		t,
		issue85WithRoot(root),
		issue85WithExecutor(executor),
		issue85WithClock(template.clock),
		issue85WithScheduler(scheduler),
		issue85WithEvents(events),
	)

	mutation, terminal := recovered.set()
	issue85AssertNoError(t, terminal)
	issue85AssertState(t, mutation, eebusraw.MutationStateV1Applied)
	if mutation.ProtocolAccepted != nil {
		t.Fatalf("reconnect recovery protocol_accepted = %v, want nil", mutation.ProtocolAccepted)
	}
	wantGeneration := template.generation + 1
	if mutation.Runtime.ConnectionGeneration != wantGeneration {
		t.Fatalf(
			"reconnect recovery generation = %d, want rebound generation %d",
			mutation.Runtime.ConnectionGeneration,
			wantGeneration,
		)
	}
	if mutation.ObservedAfter == nil ||
		!issue85ValuesEqual(*mutation.ObservedAfter, template.requested) ||
		mutation.ApplyVerification == nil ||
		!mutation.ApplyVerification.Verified {
		t.Fatalf("reconnect recovery omitted requested-value proof: %+v", mutation)
	}
	if mutation.OutcomeEvidence == nil ||
		!mutation.OutcomeEvidence.PossibleSideEffect ||
		!mutation.OutcomeEvidence.BlindRetryForbidden {
		t.Fatalf("reconnect recovery omitted uncertainty evidence: %+v", mutation.OutcomeEvidence)
	}
	reads, writes, maxActive, exhausted := executor.counts()
	if reads != 1 || writes != 0 || maxActive != 1 || exhausted != 0 {
		t.Fatalf("reconnect recovery calls = reads:%d writes:%d max_active:%d exhausted:%d", reads, writes, maxActive, exhausted)
	}
}

func TestIssue85AbruptRollbackIntentRecoveryReadsBeforeRollbackDispatch(t *testing.T) {
	root, _ := issue85RunCrashProcess(t, eebusraw.MutationStateV1RollbackIntent)
	template := issue85HarnessDraft(t)
	events := &issue85EventLog{}
	executor := &issue85Executor{t: t, events: events}
	executor.setSteps(
		[]issue85ReadStep{
			template.readStep(template.requested),
			template.readStep(template.before),
		},
		[]issue85WriteStep{
			template.writeStep(template.before, rawMutationWriteResult{
				FrameSent:  true,
				Correlated: true,
				Accepted:   true,
			}),
		},
	)
	scheduler := newIssue85Scheduler(template.clock, events)
	recovered := newIssue85Harness(
		t,
		issue85WithRoot(root),
		issue85WithExecutor(executor),
		issue85WithClock(template.clock),
		issue85WithScheduler(scheduler),
		issue85WithEvents(events),
	)
	mutation, terminal := recovered.set()
	issue85AssertNoError(t, terminal)
	issue85AssertState(t, mutation, eebusraw.MutationStateV1RolledBack)
	beforeHash, err := template.before.ComputeHash()
	if err != nil {
		t.Fatal(err)
	}
	issue85RequireOrder(
		t,
		events.snapshot(),
		"remote:READ:"+template.target.Function,
		"durable-state:"+string(eebusraw.MutationStateV1RollbackDispatchIntent),
		"remote:WRITE:"+string(beforeHash),
	)
	reads, writes, _, exhausted := executor.counts()
	if reads != 2 || writes != 1 || exhausted != 0 {
		t.Fatalf("rollback-intent recovery calls = reads:%d writes:%d exhausted:%d", reads, writes, exhausted)
	}
}

func TestIssue85AbruptRollbackDispatchRecoveryNeverBlindlyResends(t *testing.T) {
	root, _ := issue85RunCrashProcess(t, eebusraw.MutationStateV1RollbackDispatchIntent)
	template := issue85HarnessDraft(t)
	events := &issue85EventLog{}
	executor := &issue85Executor{t: t, events: events}
	executor.setSteps([]issue85ReadStep{template.readStep(template.before)}, nil)
	scheduler := newIssue85Scheduler(template.clock, events)
	recovered := newIssue85Harness(
		t,
		issue85WithRoot(root),
		issue85WithExecutor(executor),
		issue85WithClock(template.clock),
		issue85WithScheduler(scheduler),
		issue85WithEvents(events),
	)
	mutation, terminal := recovered.set()
	issue85AssertNoError(t, terminal)
	issue85AssertState(t, mutation, eebusraw.MutationStateV1RolledBack)
	reads, writes, _, exhausted := executor.counts()
	if reads != 1 || writes != 0 || exhausted != 0 {
		t.Fatalf("rollback-dispatch recovery calls = reads:%d writes:%d exhausted:%d", reads, writes, exhausted)
	}
}

func issue85ConfigureCrashScenario(harness *issue85Harness, state eebusraw.MutationStateV1) {
	switch {
	case state == eebusraw.MutationStateV1ProbeActive:
		harness.request.Mode = eebusraw.ModeV1Probe
		harness.request.ProbeTTLSeconds = 60
	case issue85RollbackCrashState(state):
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
				harness.writeStep(harness.before, rawMutationWriteResult{
					FrameSent:  true,
					Correlated: true,
					Accepted:   true,
				}),
			},
		)
	case state == eebusraw.MutationStateV1OutcomeUnknown:
		write := harness.writeStep(harness.requested, rawMutationWriteResult{
			FrameSent:  true,
			Correlated: false,
		})
		write.terminal = issue85Error(eebusraw.ErrorCodeV1Timeout)
		readback := harness.readStep(harness.before)
		readback.terminal = issue85Error(eebusraw.ErrorCodeV1DecodeError)
		harness.executor.setSteps(
			[]issue85ReadStep{harness.readStep(harness.before), readback},
			[]issue85WriteStep{write},
		)
	case state == eebusraw.MutationStateV1Conflict:
		write := harness.writeStep(harness.requested, rawMutationWriteResult{
			FrameSent:  true,
			Correlated: false,
		})
		write.terminal = issue85Error(eebusraw.ErrorCodeV1Timeout)
		harness.executor.setSteps(
			[]issue85ReadStep{harness.readStep(harness.before), harness.readStep(harness.third)},
			[]issue85WriteStep{write},
		)
	case state == eebusraw.MutationStateV1FailedNoContact:
		read := harness.readStep(harness.before)
		read.terminal = issue85Error(eebusraw.ErrorCodeV1Disconnected)
		harness.executor.setSteps([]issue85ReadStep{read}, nil)
	case state == eebusraw.MutationStateV1Rejected:
		write := harness.writeStep(harness.requested, rawMutationWriteResult{
			FrameSent:  true,
			Correlated: true,
			Accepted:   false,
		})
		write.terminal = issue85Error(eebusraw.ErrorCodeV1RemoteError)
		harness.executor.setSteps(
			[]issue85ReadStep{harness.readStep(harness.before), harness.readStep(harness.before)},
			[]issue85WriteStep{write},
		)
	case state == eebusraw.MutationStateV1NoEffect:
		write := harness.writeStep(harness.requested, rawMutationWriteResult{
			FrameSent:  true,
			Correlated: false,
		})
		write.terminal = issue85Error(eebusraw.ErrorCodeV1Timeout)
		harness.executor.setSteps(
			[]issue85ReadStep{harness.readStep(harness.before), harness.readStep(harness.before)},
			[]issue85WriteStep{write},
		)
	}
}

func issue85RollbackCrashState(state eebusraw.MutationStateV1) bool {
	switch state {
	case eebusraw.MutationStateV1RollbackIntent,
		eebusraw.MutationStateV1RollbackDispatchIntent,
		eebusraw.MutationStateV1RollbackReplyObserved,
		eebusraw.MutationStateV1RollbackVerifyPending,
		eebusraw.MutationStateV1RolledBack:
		return true
	default:
		return false
	}
}

func issue85RunCrashProcess(
	t *testing.T,
	state eebusraw.MutationStateV1,
) (root string, marker string) {
	t.Helper()
	root = t.TempDir()
	marker = filepath.Join(t.TempDir(), "crash-events.log")
	command := issue85CrashCommand(root, marker, state, "")
	output, err := command.CombinedOutput()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != issue85CrashExitCode {
		t.Fatalf("crash helper state %q error = %v\n%s", state, err, output)
	}
	events := issue85ReadMarker(t, marker)
	if issue85Index(events, issue85CrashHardFailureMarker) >= 0 {
		t.Fatalf("crash helper state %q hid an earlier harness failure: %v", state, events)
	}
	return root, marker
}

func issue85CrashCommand(
	root string,
	marker string,
	state eebusraw.MutationStateV1,
	failure string,
) *exec.Cmd {
	command := exec.Command(os.Args[0], "-test.run=^TestIssue85AbruptCrashHelper$")
	command.Env = append(
		os.Environ(),
		issue85CrashHelperEnvironment+"=1",
		issue85CrashRootEnvironment+"="+root,
		issue85CrashStateEnvironment+"="+string(state),
		issue85CrashMarkerEnvironment+"="+marker,
		issue85CrashFailureEnvironment+"="+failure,
	)
	return command
}

func TestIssue85CrashHelperEnvironmentNamesContainNoRuntimeSecrets(t *testing.T) {
	for name, value := range map[string]string{
		"helper":  issue85CrashHelperEnvironment,
		"root":    issue85CrashRootEnvironment,
		"state":   issue85CrashStateEnvironment,
		"marker":  issue85CrashMarkerEnvironment,
		"failure": issue85CrashFailureEnvironment,
	} {
		lower := strings.ToLower(value)
		for _, forbidden := range []string{"token", "key", "secret", "credential", "identity"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("%s crash environment name %q carries forbidden secret class %q", name, value, forbidden)
			}
		}
	}
}
