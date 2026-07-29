package eebusmutation

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

func TestIssue85SuccessfulApplyIsVerifiedAndDurablyOrdered(t *testing.T) {
	harness := newIssue85Harness(t)

	mutation, terminal := harness.set()
	issue85AssertNoError(t, terminal)
	issue85AssertState(t, mutation, eebusraw.MutationStateV1Applied)
	if mutation.ProtocolAccepted == nil || !*mutation.ProtocolAccepted {
		t.Fatalf("successful apply protocol_accepted = %v", mutation.ProtocolAccepted)
	}
	if mutation.ObservedAfter == nil ||
		!issue85ValuesEqual(*mutation.ObservedAfter, harness.requested) ||
		mutation.ApplyVerification == nil ||
		!mutation.ApplyVerification.Verified {
		t.Fatalf("successful apply lacks verified readback: %+v", mutation)
	}
	events := harness.events.snapshot()
	writeHash, err := harness.requested.ComputeHash()
	if err != nil {
		t.Fatal(err)
	}
	issue85RequireOrder(
		t,
		events,
		"durable-state:"+string(eebusraw.MutationStateV1Prepared),
		"durable-state:"+string(eebusraw.MutationStateV1DispatchIntent),
		"remote:WRITE:"+string(writeHash),
		"durable-state:"+string(eebusraw.MutationStateV1ReplyObserved),
		"durable-state:"+string(eebusraw.MutationStateV1VerifyPending),
		"remote:READ:"+harness.target.Function,
		"durable-state:"+string(eebusraw.MutationStateV1Applied),
	)
	reads, writes, maxActive, exhausted := harness.executor.counts()
	if reads != 2 || writes != 1 || maxActive != 1 || exhausted != 0 {
		t.Fatalf("successful apply calls = reads:%d writes:%d max_active:%d exhausted:%d", reads, writes, maxActive, exhausted)
	}
}

func TestIssue85IdempotencyReplayAndConflictNeverSendAgain(t *testing.T) {
	harness := newIssue85Harness(t)
	first, terminal := harness.set()
	issue85AssertNoError(t, terminal)
	beforeVerify, beforeConsume := harness.tokens.counts()
	beforePolicy := harness.policy.count()

	replayed, terminal := harness.set()
	issue85AssertNoError(t, terminal)
	if replayed.MutationRef != first.MutationRef ||
		replayed.State != first.State {
		t.Fatalf("same request replay = %+v, want original %+v", replayed, first)
	}

	changed := harness.request
	changed.Value = harness.third.Clone()
	originalHash, err := eebusraw.CanonicalSHA256V1(harness.request)
	if err != nil {
		t.Fatal(err)
	}
	changedHash, err := eebusraw.CanonicalSHA256V1(changed)
	if err != nil {
		t.Fatal(err)
	}
	if originalHash == changedHash {
		t.Fatal("different canonical SET requests produced the same commitment")
	}
	_, terminal = harness.coordinator.FeaturesDataSet(
		context.Background(),
		harness.auth,
		changed,
	)
	issue85AssertError(t, terminal, eebusraw.ErrorCodeV1IdempotencyConflict)
	distinct := harness.requestForTarget(
		harness.distinctTarget(),
		"issue85-idempotency-distinct-target-token",
		harness.request.IdempotencyKey,
	)
	_, terminal = harness.coordinator.FeaturesDataSet(
		context.Background(),
		harness.auth,
		distinct,
	)
	issue85AssertError(t, terminal, eebusraw.ErrorCodeV1IdempotencyConflict)
	reads, writes, _, exhausted := harness.executor.counts()
	if reads != 2 || writes != 1 || exhausted != 0 {
		t.Fatalf("idempotency replay/conflict contacted remote: reads:%d writes:%d exhausted:%d", reads, writes, exhausted)
	}
	afterVerify, afterConsume := harness.tokens.counts()
	if afterVerify != beforeVerify ||
		afterConsume != beforeConsume ||
		harness.policy.count() != beforePolicy {
		t.Fatalf(
			"idempotency replay/conflicts reused dependencies: token verify %d->%d consume %d->%d policy %d->%d",
			beforeVerify,
			afterVerify,
			beforeConsume,
			afterConsume,
			beforePolicy,
			harness.policy.count(),
		)
	}
}

func TestIssue85IdempotencyIdentityPartitionsRuntimeEpochAndPrincipal(t *testing.T) {
	t.Run("runtime epoch", func(t *testing.T) {
		firstHarness := newIssue85Harness(t)
		first, terminal := firstHarness.set()
		issue85AssertNoError(t, terminal)
		firstHarness.closeClean()

		secondHarness := newIssue85Harness(
			t,
			issue85WithRoot(firstHarness.root),
			issue85WithRuntimeBinding(firstHarness.epoch+1, firstHarness.generation),
		)
		target := secondHarness.distinctTarget()
		request := secondHarness.requestForTarget(
			target,
			"issue85-idempotency-next-epoch-token",
			firstHarness.request.IdempotencyKey,
		)
		secondHarness.executor.setSteps(
			[]issue85ReadStep{
				secondHarness.readStepForTarget(target, secondHarness.before),
				secondHarness.readStepForTarget(target, secondHarness.requested),
			},
			[]issue85WriteStep{
				secondHarness.writeStepForTarget(target, secondHarness.requested, rawMutationWriteResult{
					FrameSent:  true,
					Correlated: true,
					Accepted:   true,
				}),
			},
		)

		second, terminal := secondHarness.coordinator.FeaturesDataSet(
			context.Background(),
			secondHarness.auth,
			request,
		)
		issue85AssertNoError(t, terminal)
		issue85AssertState(t, second, eebusraw.MutationStateV1Applied)
		if second.MutationRef == first.MutationRef {
			t.Fatalf("different runtime epochs reused mutation identity %q", first.MutationRef)
		}
		if second.Runtime.RuntimeEpoch != secondHarness.epoch {
			t.Fatalf("second mutation epoch = %d, want %d", second.Runtime.RuntimeEpoch, secondHarness.epoch)
		}
		reads, writes, maxActive, exhausted := secondHarness.executor.counts()
		if reads != 2 || writes != 1 || maxActive != 1 || exhausted != 0 {
			t.Fatalf("next-epoch identity calls = reads:%d writes:%d max_active:%d exhausted:%d", reads, writes, maxActive, exhausted)
		}
	})

	t.Run("principal", func(t *testing.T) {
		harness := newIssue85Harness(t)
		first, terminal := harness.set()
		issue85AssertNoError(t, terminal)

		target := harness.distinctTarget()
		const principal = "automation-owner"
		request := harness.requestForTargetAndPrincipal(
			target,
			"issue85-idempotency-other-principal-token",
			harness.request.IdempotencyKey,
			principal,
		)
		auth := harness.auth
		auth.PrincipalClass = principal
		harness.executor.setSteps(
			[]issue85ReadStep{
				harness.readStepForTarget(target, harness.before),
				harness.readStepForTarget(target, harness.requested),
			},
			[]issue85WriteStep{
				harness.writeStepForTarget(target, harness.requested, rawMutationWriteResult{
					FrameSent:  true,
					Correlated: true,
					Accepted:   true,
				}),
			},
		)

		second, terminal := harness.coordinator.FeaturesDataSet(
			context.Background(),
			auth,
			request,
		)
		issue85AssertNoError(t, terminal)
		issue85AssertState(t, second, eebusraw.MutationStateV1Applied)
		if second.MutationRef == first.MutationRef {
			t.Fatalf("different principals reused mutation identity %q", first.MutationRef)
		}
		reads, writes, maxActive, exhausted := harness.executor.counts()
		if reads != 2 || writes != 1 || maxActive != 1 || exhausted != 0 {
			t.Fatalf("other-principal identity calls = reads:%d writes:%d max_active:%d exhausted:%d", reads, writes, maxActive, exhausted)
		}
	})
}

func TestIssue85SetAndRollbackToolsHaveDistinctIdempotencyNamespaces(t *testing.T) {
	harness := newIssue85Harness(t)
	first, terminal := harness.set()
	issue85AssertNoError(t, terminal)

	secondTarget := harness.distinctTarget()
	secondRequest := harness.requestForTarget(
		secondTarget,
		"issue85-second-mutation-for-rollback-token",
		"issue85-second-mutation-for-rollback",
	)
	harness.executor.setSteps(
		[]issue85ReadStep{
			harness.readStepForTarget(secondTarget, harness.before),
			harness.readStepForTarget(secondTarget, harness.requested),
		},
		[]issue85WriteStep{
			harness.writeStepForTarget(secondTarget, harness.requested, rawMutationWriteResult{
				FrameSent:  true,
				Correlated: true,
				Accepted:   true,
			}),
		},
	)
	second, terminal := harness.coordinator.FeaturesDataSet(
		context.Background(),
		harness.auth,
		secondRequest,
	)
	issue85AssertNoError(t, terminal)
	if second.MutationRef == first.MutationRef {
		t.Fatalf("independent SET mutations reused ref %q", first.MutationRef)
	}

	harness.executor.setSteps(
		[]issue85ReadStep{
			harness.readStepForTarget(harness.target, harness.requested),
			harness.readStepForTarget(harness.target, harness.before),
		},
		[]issue85WriteStep{
			harness.writeStepForTarget(harness.target, harness.before, rawMutationWriteResult{
				FrameSent:  true,
				Correlated: true,
				Accepted:   true,
			}),
		},
	)
	rollbackKey := harness.request.IdempotencyKey
	firstRollbackRequest := eebusraw.MutationRollbackRequestV1{
		MutationRef:    first.MutationRef,
		IdempotencyKey: rollbackKey,
	}
	secondRollbackRequest := eebusraw.MutationRollbackRequestV1{
		MutationRef:    second.MutationRef,
		IdempotencyKey: rollbackKey,
	}
	firstRollbackHash, err := eebusraw.CanonicalSHA256V1(firstRollbackRequest)
	if err != nil {
		t.Fatal(err)
	}
	secondRollbackHash, err := eebusraw.CanonicalSHA256V1(secondRollbackRequest)
	if err != nil {
		t.Fatal(err)
	}
	if firstRollbackHash == secondRollbackHash {
		t.Fatal("different canonical rollback requests produced the same commitment")
	}
	rolledBack, terminal := harness.rollback(first.MutationRef, rollbackKey)
	issue85AssertNoError(t, terminal)
	issue85AssertState(t, rolledBack, eebusraw.MutationStateV1RolledBack)

	beforeReads, beforeWrites, _, beforeExhausted := harness.executor.counts()
	if beforeReads != 2 || beforeWrites != 1 || beforeExhausted != 0 {
		t.Fatalf("cross-tool rollback calls = reads:%d writes:%d exhausted:%d", beforeReads, beforeWrites, beforeExhausted)
	}
	_, terminal = harness.rollback(second.MutationRef, rollbackKey)
	issue85AssertError(t, terminal, eebusraw.ErrorCodeV1IdempotencyConflict)
	afterReads, afterWrites, _, afterExhausted := harness.executor.counts()
	if afterReads != beforeReads || afterWrites != beforeWrites || afterExhausted != 0 {
		t.Fatalf(
			"same rollback tuple with different request contacted remote: before=%d/%d after=%d/%d exhausted=%d",
			beforeReads,
			beforeWrites,
			afterReads,
			afterWrites,
			afterExhausted,
		)
	}
}

func TestIssue85GlobalWriterLeaseSerializesIndependentlyValidDistinctTargets(t *testing.T) {
	harness := newIssue85Harness(t)
	release := make(chan struct{})
	firstRead := harness.readStep(harness.before)
	firstRead.block = release
	harness.executor.setSteps(
		[]issue85ReadStep{
			firstRead,
			harness.readStep(harness.requested),
		},
		[]issue85WriteStep{harness.writeStep(harness.requested, rawMutationWriteResult{
			FrameSent:  true,
			Correlated: true,
			Accepted:   true,
		})},
	)

	type result struct {
		mutation eebusraw.MutationV1
		terminal *eebusraw.ErrorV1
	}
	firstResult := make(chan result, 1)
	go func() {
		mutation, terminal := harness.set()
		firstResult <- result{mutation: mutation, terminal: terminal}
	}()
	issue85Eventually(t, func() bool {
		reads, _, active, _ := harness.executor.counts()
		return reads == 1 && active == 1
	}, "first writer to hold the lease during guard READ")

	secondRequest := harness.requestForTarget(
		harness.distinctTarget(),
		"issue85-second-writer-distinct-target-token",
		"issue85-second-writer",
	)
	_, secondTerminal := harness.coordinator.FeaturesDataSet(
		context.Background(),
		harness.auth,
		secondRequest,
	)
	issue85AssertError(t, secondTerminal, eebusraw.ErrorCodeV1WriterBusy)

	close(release)
	first := <-firstResult
	issue85AssertNoError(t, first.terminal)
	issue85AssertState(t, first.mutation, eebusraw.MutationStateV1Applied)
	reads, writes, maxActive, exhausted := harness.executor.counts()
	if reads != 2 || writes != 1 || maxActive != 1 || exhausted != 0 {
		t.Fatalf("writer serialization = reads:%d writes:%d max_active:%d exhausted:%d", reads, writes, maxActive, exhausted)
	}

	harness.target = secondRequest.Target.Clone()
	harness.executor.setSteps(
		[]issue85ReadStep{
			harness.readStep(harness.before),
			harness.readStep(harness.requested),
		},
		[]issue85WriteStep{harness.writeStep(harness.requested, rawMutationWriteResult{
			FrameSent:  true,
			Correlated: true,
			Accepted:   true,
		})},
	)
	second, secondTerminal := harness.coordinator.FeaturesDataSet(
		context.Background(),
		harness.auth,
		secondRequest,
	)
	issue85AssertNoError(t, secondTerminal)
	issue85AssertState(t, second, eebusraw.MutationStateV1Applied)
	if second.MutationRef == first.mutation.MutationRef {
		t.Fatalf("different idempotency keys reused mutation identity %q", second.MutationRef)
	}
	reads, writes, maxActive, exhausted = harness.executor.counts()
	if reads != 2 || writes != 1 || maxActive != 1 || exhausted != 0 {
		t.Fatalf("distinct target validity = reads:%d writes:%d max_active:%d exhausted:%d", reads, writes, maxActive, exhausted)
	}
}

func TestIssue85GlobalWriterLeaseSerializesSetAgainstExplicitRollback(t *testing.T) {
	harness := newIssue85Harness(t)
	applied, terminal := harness.set()
	issue85AssertNoError(t, terminal)

	target := harness.distinctTarget()
	request := harness.requestForTarget(
		target,
		"issue85-set-versus-explicit-rollback-token",
		"issue85-set-versus-explicit-rollback",
	)
	release := make(chan struct{})
	firstRead := harness.readStepForTarget(target, harness.before)
	firstRead.block = release
	harness.executor.setSteps(
		[]issue85ReadStep{
			firstRead,
			harness.readStepForTarget(target, harness.requested),
		},
		[]issue85WriteStep{
			harness.writeStepForTarget(target, harness.requested, rawMutationWriteResult{
				FrameSent:  true,
				Correlated: true,
				Accepted:   true,
			}),
		},
	)

	type result struct {
		mutation eebusraw.MutationV1
		terminal *eebusraw.ErrorV1
	}
	setResult := make(chan result, 1)
	go func() {
		mutation, setError := harness.coordinator.FeaturesDataSet(
			context.Background(),
			harness.auth,
			request,
		)
		setResult <- result{mutation: mutation, terminal: setError}
	}()
	issue85Eventually(t, func() bool {
		reads, _, active, _ := harness.executor.counts()
		return reads == 1 && active == 1
	}, "SET to hold the lease before explicit rollback")

	_, rollbackError := harness.rollback(
		applied.MutationRef,
		"issue85-explicit-rollback-contender",
	)
	issue85AssertError(t, rollbackError, eebusraw.ErrorCodeV1WriterBusy)
	close(release)
	setOutcome := <-setResult
	issue85AssertNoError(t, setOutcome.terminal)
	issue85AssertState(t, setOutcome.mutation, eebusraw.MutationStateV1Applied)

	reads, writes, maxActive, exhausted := harness.executor.counts()
	if reads != 2 || writes != 1 || maxActive != 1 || exhausted != 0 {
		t.Fatalf("SET/explicit rollback lease calls = reads:%d writes:%d max_active:%d exhausted:%d", reads, writes, maxActive, exhausted)
	}
}

func TestIssue85GlobalWriterLeaseSerializesSetAgainstProbeRollback(t *testing.T) {
	harness := newIssue85Harness(t)
	harness.request.Mode = eebusraw.ModeV1Probe
	harness.request.ProbeTTLSeconds = 60
	probe, terminal := harness.set()
	issue85AssertNoError(t, terminal)
	issue85AssertState(t, probe, eebusraw.MutationStateV1ProbeActive)
	if probe.ProbeDeadline == nil {
		t.Fatal("probe rollback lease fixture omitted probe deadline")
	}

	target := harness.distinctTarget()
	request := harness.requestForTarget(
		target,
		"issue85-set-versus-probe-rollback-token",
		"issue85-set-versus-probe-rollback",
	)
	request.Mode = eebusraw.ModeV1Apply
	request.ProbeTTLSeconds = 0
	release := make(chan struct{})
	firstRead := harness.readStepForTarget(target, harness.before)
	firstRead.block = release
	harness.executor.setSteps(
		[]issue85ReadStep{
			firstRead,
			harness.readStepForTarget(target, harness.requested),
			harness.readStepForTarget(harness.target, harness.requested),
			harness.readStepForTarget(harness.target, harness.before),
		},
		[]issue85WriteStep{
			harness.writeStepForTarget(target, harness.requested, rawMutationWriteResult{
				FrameSent:  true,
				Correlated: true,
				Accepted:   true,
			}),
			harness.writeStepForTarget(harness.target, harness.before, rawMutationWriteResult{
				FrameSent:  true,
				Correlated: true,
				Accepted:   true,
			}),
		},
	)

	type result struct {
		mutation eebusraw.MutationV1
		terminal *eebusraw.ErrorV1
	}
	setResult := make(chan result, 1)
	go func() {
		mutation, setError := harness.coordinator.FeaturesDataSet(
			context.Background(),
			harness.auth,
			request,
		)
		setResult <- result{mutation: mutation, terminal: setError}
	}()
	issue85Eventually(t, func() bool {
		reads, _, active, _ := harness.executor.counts()
		return reads == 1 && active == 1
	}, "SET to hold the lease before probe expiry")

	harness.clock.Advance(60 * time.Second)
	fired := make(chan int, 1)
	go func() {
		fired <- harness.scheduler.FireDue()
	}()
	issue85Eventually(t, func() bool {
		for _, event := range harness.events.snapshot() {
			if event == "timer:fired:"+probe.ProbeDeadline.UTC().Format(time.RFC3339Nano) {
				return true
			}
		}
		return false
	}, "probe rollback to contend for the held lease")
	close(release)

	setOutcome := <-setResult
	issue85AssertNoError(t, setOutcome.terminal)
	issue85AssertState(t, setOutcome.mutation, eebusraw.MutationStateV1Applied)
	if count := <-fired; count != 1 {
		t.Fatalf("probe expiry fired %d callbacks, want 1", count)
	}
	issue85Eventually(t, func() bool {
		status, statusError := harness.status(probe.MutationRef)
		return statusError == nil && status.State == eebusraw.MutationStateV1RolledBack
	}, "probe rollback after contending SET")
	reads, writes, maxActive, exhausted := harness.executor.counts()
	if reads != 4 || writes != 2 || maxActive != 1 || exhausted != 0 {
		t.Fatalf("SET/probe rollback lease calls = reads:%d writes:%d max_active:%d exhausted:%d", reads, writes, maxActive, exhausted)
	}
}

func TestIssue85GlobalWriterLeaseSerializesSetAgainstExpiredProbeRecovery(t *testing.T) {
	firstHarness := newIssue85Harness(t)
	firstHarness.request.Mode = eebusraw.ModeV1Probe
	firstHarness.request.ProbeTTLSeconds = 30
	probe, terminal := firstHarness.set()
	issue85AssertNoError(t, terminal)
	if probe.ProbeDeadline == nil {
		t.Fatal("expired recovery lease fixture omitted probe deadline")
	}
	firstHarness.closeClean()
	firstHarness.clock.Advance(31 * time.Second)

	events := &issue85EventLog{}
	executor := &issue85Executor{t: t, events: events}
	scheduler := newIssue85Scheduler(firstHarness.clock, events)
	restarted := newIssue85Harness(
		t,
		issue85WithRoot(firstHarness.root),
		issue85WithExecutor(executor),
		issue85WithTokenVerifier(firstHarness.tokens),
		issue85WithPolicy(firstHarness.policy),
		issue85WithClock(firstHarness.clock),
		issue85WithScheduler(scheduler),
		issue85WithEvents(events),
	)

	target := restarted.distinctTarget()
	request := restarted.requestForTarget(
		target,
		"issue85-set-versus-recovery-token",
		"issue85-set-versus-recovery",
	)
	release := make(chan struct{})
	firstRead := restarted.readStepForTarget(target, restarted.before)
	firstRead.block = release
	executor.setSteps(
		[]issue85ReadStep{
			firstRead,
			restarted.readStepForTarget(target, restarted.requested),
			restarted.readStepForTarget(restarted.target, restarted.requested),
			restarted.readStepForTarget(restarted.target, restarted.before),
		},
		[]issue85WriteStep{
			restarted.writeStepForTarget(target, restarted.requested, rawMutationWriteResult{
				FrameSent:  true,
				Correlated: true,
				Accepted:   true,
			}),
			restarted.writeStepForTarget(restarted.target, restarted.before, rawMutationWriteResult{
				FrameSent:  true,
				Correlated: true,
				Accepted:   true,
			}),
		},
	)

	type result struct {
		mutation eebusraw.MutationV1
		terminal *eebusraw.ErrorV1
	}
	setResult := make(chan result, 1)
	go func() {
		mutation, setError := restarted.coordinator.FeaturesDataSet(
			context.Background(),
			restarted.auth,
			request,
		)
		setResult <- result{mutation: mutation, terminal: setError}
	}()
	issue85Eventually(t, func() bool {
		reads, _, active, _ := executor.counts()
		return reads == 1 && active == 1
	}, "SET to hold the lease before recovered probe expiry")

	fired := make(chan int, 1)
	go func() {
		fired <- scheduler.FireDue()
	}()
	issue85Eventually(t, func() bool {
		for _, event := range events.snapshot() {
			if event == "timer:fired:"+probe.ProbeDeadline.UTC().Format(time.RFC3339Nano) {
				return true
			}
		}
		return false
	}, "expired-probe recovery to contend for the held lease")
	close(release)

	setOutcome := <-setResult
	issue85AssertNoError(t, setOutcome.terminal)
	issue85AssertState(t, setOutcome.mutation, eebusraw.MutationStateV1Applied)
	if count := <-fired; count != 1 {
		t.Fatalf("expired recovery fired %d callbacks, want 1", count)
	}
	issue85Eventually(t, func() bool {
		status, statusError := restarted.status(probe.MutationRef)
		return statusError == nil && status.State == eebusraw.MutationStateV1RolledBack
	}, "expired probe recovery after contending SET")
	reads, writes, maxActive, exhausted := executor.counts()
	if reads != 4 || writes != 2 || maxActive != 1 || exhausted != 0 {
		t.Fatalf("SET/recovery lease calls = reads:%d writes:%d max_active:%d exhausted:%d", reads, writes, maxActive, exhausted)
	}
}

func TestIssue85ConcurrentSameKeyReturnsOneIdentityAndOneWrite(t *testing.T) {
	harness := newIssue85Harness(t)
	release := make(chan struct{})
	write := harness.writeStep(harness.requested, rawMutationWriteResult{
		FrameSent:  true,
		Correlated: true,
		Accepted:   true,
	})
	write.block = release
	harness.executor.setSteps(
		[]issue85ReadStep{
			harness.readStep(harness.before),
			harness.readStep(harness.requested),
		},
		[]issue85WriteStep{write},
	)

	type result struct {
		mutation eebusraw.MutationV1
		terminal *eebusraw.ErrorV1
	}
	firstResult := make(chan result, 1)
	go func() {
		mutation, terminal := harness.set()
		firstResult <- result{mutation: mutation, terminal: terminal}
	}()
	issue85Eventually(t, func() bool {
		_, writes, active, _ := harness.executor.counts()
		return writes == 1 && active == 1
	}, "first same-key writer to reach the scripted WRITE")

	replay, replayTerminal := harness.set()
	issue85AssertNoError(t, replayTerminal)
	if replay.MutationRef == "" {
		t.Fatal("in-flight same-key replay omitted the durable mutation identity")
	}
	close(release)
	first := <-firstResult
	issue85AssertNoError(t, first.terminal)
	if first.mutation.MutationRef != replay.MutationRef {
		t.Fatalf("same-key concurrent refs = %q and %q", first.mutation.MutationRef, replay.MutationRef)
	}
	_, writes, maxActive, exhausted := harness.executor.counts()
	if writes != 1 || maxActive != 1 || exhausted != 0 {
		t.Fatalf("same-key concurrency writes=%d max_active=%d exhausted=%d", writes, maxActive, exhausted)
	}
}

func TestIssue85RestartReplaysDurableIdempotencyWithoutTokenOrRemoteReuse(t *testing.T) {
	firstHarness := newIssue85Harness(t)
	first, terminal := firstHarness.set()
	issue85AssertNoError(t, terminal)
	beforeVerify, beforeConsume := firstHarness.tokens.counts()
	beforePolicy := firstHarness.policy.count()
	firstHarness.closeClean()
	firstHarness.tokens.terminal = issue85Error(eebusraw.ErrorCodeV1Internal)
	firstHarness.policy.terminal = issue85Error(eebusraw.ErrorCodeV1Internal)

	recoveryExecutor := &issue85Executor{t: t, events: firstHarness.events}
	recoveryExecutor.setSteps(nil, nil)
	recoveryScheduler := newIssue85Scheduler(firstHarness.clock, firstHarness.events)
	restarted := newIssue85Harness(
		t,
		issue85WithRoot(firstHarness.root),
		issue85WithExecutor(recoveryExecutor),
		issue85WithTokenVerifier(firstHarness.tokens),
		issue85WithPolicy(firstHarness.policy),
		issue85WithClock(firstHarness.clock),
		issue85WithScheduler(recoveryScheduler),
		issue85WithEvents(firstHarness.events),
	)

	replayed, terminal := restarted.set()
	issue85AssertNoError(t, terminal)
	if replayed.MutationRef != first.MutationRef ||
		replayed.State != eebusraw.MutationStateV1Applied {
		t.Fatalf("restart replay = %+v, want applied ref %q", replayed, first.MutationRef)
	}
	reads, writes, _, exhausted := recoveryExecutor.counts()
	if reads != 0 || writes != 0 || exhausted != 0 {
		t.Fatalf("restart replay contacted remote: reads:%d writes:%d exhausted:%d", reads, writes, exhausted)
	}
	afterVerify, afterConsume := firstHarness.tokens.counts()
	afterPolicy := firstHarness.policy.count()
	if afterVerify != beforeVerify ||
		afterConsume != beforeConsume ||
		afterPolicy != beforePolicy {
		t.Fatalf(
			"restart replay reused dependencies: token verify %d->%d consume %d->%d policy %d->%d",
			beforeVerify,
			afterVerify,
			beforeConsume,
			afterConsume,
			beforePolicy,
			afterPolicy,
		)
	}
}

func TestIssue85MutationStatusIsReadAuthorizedAndNeverContactsRemote(t *testing.T) {
	harness := newIssue85Harness(t)
	mutation, terminal := harness.set()
	issue85AssertNoError(t, terminal)
	beforeReads, beforeWrites, _, _ := harness.executor.counts()

	got, terminal := harness.status(mutation.MutationRef)
	issue85AssertNoError(t, terminal)
	if got.MutationRef != mutation.MutationRef || got.State != mutation.State {
		t.Fatalf("status = %+v, want mutation %+v", got, mutation)
	}

	tests := []struct {
		name   string
		mutate func(*eebusraw.ReadAuthorizationV1)
	}{
		{
			name: "wrong principal",
			mutate: func(auth *eebusraw.ReadAuthorizationV1) {
				auth.PrincipalClass = "other-owner"
			},
		},
		{
			name: "write scope on read method",
			mutate: func(auth *eebusraw.ReadAuthorizationV1) {
				auth.Scope = eebusraw.AuthScopeV1RawWrite
			},
		},
		{
			name: "wrong read tool",
			mutate: func(auth *eebusraw.ReadAuthorizationV1) {
				auth.Tool = eebusraw.ToolV1FeaturesDataGet
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			auth := harness.readAuth
			test.mutate(&auth)
			_, denied := harness.coordinator.MutationsGet(
				context.Background(),
				auth,
				eebusraw.MutationGetRequestV1{MutationRef: mutation.MutationRef},
			)
			issue85AssertError(t, denied, eebusraw.ErrorCodeV1PermissionDenied)
		})
	}

	afterReads, afterWrites, _, exhausted := harness.executor.counts()
	if afterReads != beforeReads || afterWrites != beforeWrites || exhausted != 0 {
		t.Fatalf("status contacted remote: before=%d/%d after=%d/%d exhausted=%d", beforeReads, beforeWrites, afterReads, afterWrites, exhausted)
	}
}

func TestIssue85ManyConcurrentWritersNeverOverlapRemoteCalls(t *testing.T) {
	harness := newIssue85Harness(t)
	release := make(chan struct{})
	firstRead := harness.readStep(harness.before)
	firstRead.block = release
	harness.executor.setSteps(
		[]issue85ReadStep{
			firstRead,
			harness.readStep(harness.requested),
		},
		[]issue85WriteStep{harness.writeStep(harness.requested, rawMutationWriteResult{
			FrameSent:  true,
			Correlated: true,
			Accepted:   true,
		})},
	)

	firstResult := make(chan *eebusraw.ErrorV1, 1)
	go func() {
		_, terminal := harness.set()
		firstResult <- terminal
	}()
	issue85Eventually(t, func() bool {
		reads, _, active, _ := harness.executor.counts()
		return reads == 1 && active == 1
	}, "first stress writer to hold the lease")

	var workers sync.WaitGroup
	results := make(chan *eebusraw.ErrorV1, 7)
	for index := 0; index < 7; index++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			request := harness.request
			request.IdempotencyKey = "issue85-competing-writer-" + string(rune('a'+index))
			_, terminal := harness.coordinator.FeaturesDataSet(
				context.Background(),
				harness.auth,
				request,
			)
			results <- terminal
		}(index)
	}
	workers.Wait()
	close(results)

	busy := 0
	for terminal := range results {
		if terminal != nil && terminal.Code == eebusraw.ErrorCodeV1WriterBusy {
			busy++
		} else {
			t.Errorf("concurrent writer error = %+v", terminal)
		}
	}
	if busy != 7 {
		t.Fatalf("concurrent competing writers busy=%d, want 7", busy)
	}
	close(release)
	issue85AssertNoError(t, <-firstResult)
	_, writes, maxActive, exhausted := harness.executor.counts()
	if writes != 1 || maxActive != 1 || exhausted != 0 {
		t.Fatalf("concurrent writer remote calls writes=%d max_active=%d exhausted=%d", writes, maxActive, exhausted)
	}
}

func TestIssue85ProbeExpiryUsesDeterministicSchedulerAndVerifiedRollback(t *testing.T) {
	harness := newIssue85Harness(t)
	harness.request.Mode = eebusraw.ModeV1Probe
	harness.request.ProbeTTLSeconds = 60
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

	mutation, terminal := harness.set()
	issue85AssertNoError(t, terminal)
	issue85AssertState(t, mutation, eebusraw.MutationStateV1ProbeActive)
	wantDeadline := harness.clock.Now().Add(60 * time.Second)
	if mutation.ProbeDeadline == nil || !mutation.ProbeDeadline.Equal(wantDeadline) {
		t.Fatalf("probe deadline = %v, want %v", mutation.ProbeDeadline, wantDeadline)
	}
	if harness.scheduler.pendingCount() != 1 {
		t.Fatalf("scheduled probe callbacks = %d, want 1", harness.scheduler.pendingCount())
	}

	harness.clock.Advance(59 * time.Second)
	if fired := harness.scheduler.FireDue(); fired != 0 {
		t.Fatalf("probe fired early %d times", fired)
	}
	_, writesBefore, _, _ := harness.executor.counts()
	if writesBefore != 1 {
		t.Fatalf("probe wrote %d times before expiry", writesBefore)
	}

	harness.clock.Advance(time.Second)
	if fired := harness.scheduler.FireDue(); fired != 1 {
		t.Fatalf("probe expiry fired %d callbacks, want 1", fired)
	}
	issue85Eventually(t, func() bool {
		status, statusError := harness.status(mutation.MutationRef)
		return statusError == nil && status.State == eebusraw.MutationStateV1RolledBack
	}, "probe expiry to reach rolled_back")
	rolledBack, statusError := harness.status(mutation.MutationRef)
	issue85AssertNoError(t, statusError)
	if rolledBack.Rollback == nil ||
		rolledBack.Rollback.State != eebusraw.MutationStateV1RolledBack ||
		rolledBack.Rollback.ObservedAfter == nil ||
		!issue85ValuesEqual(*rolledBack.Rollback.ObservedAfter, harness.before) ||
		rolledBack.Rollback.Verification == nil ||
		rolledBack.Rollback.Verification.Relation != "rollback_observed_after_equals_before" ||
		!rolledBack.Rollback.Verification.Verified {
		t.Fatalf("probe rollback = %+v", rolledBack.Rollback)
	}
	reads, writes, maxActive, exhausted := harness.executor.counts()
	if reads != 4 || writes != 2 || maxActive != 1 || exhausted != 0 {
		t.Fatalf("probe rollback calls = reads:%d writes:%d max_active:%d exhausted:%d", reads, writes, maxActive, exhausted)
	}
}

func TestIssue85ManualRollbackCancelsProbeTimerAndIsIdempotent(t *testing.T) {
	harness := newIssue85Harness(t)
	harness.request.Mode = eebusraw.ModeV1Probe
	harness.request.ProbeTTLSeconds = 60
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

	applied, terminal := harness.set()
	issue85AssertNoError(t, terminal)
	rolledBack, terminal := harness.rollback(applied.MutationRef, "issue85-manual-rollback")
	issue85AssertNoError(t, terminal)
	issue85AssertState(t, rolledBack, eebusraw.MutationStateV1RolledBack)
	if harness.scheduler.pendingCount() != 0 {
		t.Fatalf("manual rollback left %d live probe timers", harness.scheduler.pendingCount())
	}

	replayed, terminal := harness.rollback(applied.MutationRef, "issue85-manual-rollback")
	issue85AssertNoError(t, terminal)
	if replayed.MutationRef != rolledBack.MutationRef ||
		replayed.State != eebusraw.MutationStateV1RolledBack {
		t.Fatalf("rollback idempotency replay = %+v, want %+v", replayed, rolledBack)
	}
	harness.clock.Advance(2 * time.Minute)
	if fired := harness.scheduler.FireDue(); fired != 0 {
		t.Fatalf("cancelled probe timer fired %d times", fired)
	}
	_, writes, _, exhausted := harness.executor.counts()
	if writes != 2 || exhausted != 0 {
		t.Fatalf("manual rollback replay/timer wrote %d times exhausted=%d", writes, exhausted)
	}
}

func TestIssue85RollbackAlreadyRestoredConvergesWithoutWrite(t *testing.T) {
	harness := newIssue85Harness(t)
	harness.executor.setSteps(
		[]issue85ReadStep{
			harness.readStep(harness.before),
			harness.readStep(harness.requested),
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
	applied, terminal := harness.set()
	issue85AssertNoError(t, terminal)

	rolledBack, terminal := harness.rollback(applied.MutationRef, "issue85-already-restored")
	issue85AssertNoError(t, terminal)
	issue85AssertState(t, rolledBack, eebusraw.MutationStateV1RolledBack)
	if rolledBack.Rollback == nil ||
		rolledBack.Rollback.ProtocolAccepted != nil ||
		rolledBack.Rollback.ObservedAfter == nil ||
		!issue85ValuesEqual(*rolledBack.Rollback.ObservedAfter, harness.before) {
		t.Fatalf("already-restored rollback = %+v", rolledBack.Rollback)
	}
	reads, writes, _, exhausted := harness.executor.counts()
	if reads != 3 || writes != 1 || exhausted != 0 {
		t.Fatalf("already-restored rollback calls = reads:%d writes:%d exhausted:%d", reads, writes, exhausted)
	}
}

func TestIssue85ManualRollbackThirdValueConflictsWithoutRollbackWrite(t *testing.T) {
	harness := newIssue85Harness(t)
	harness.executor.setSteps(
		[]issue85ReadStep{
			harness.readStep(harness.before),
			harness.readStep(harness.requested),
			harness.readStep(harness.third),
		},
		[]issue85WriteStep{
			harness.writeStep(harness.requested, rawMutationWriteResult{
				FrameSent:  true,
				Correlated: true,
				Accepted:   true,
			}),
		},
	)
	applied, terminal := harness.set()
	issue85AssertNoError(t, terminal)

	conflict, terminal := harness.rollback(applied.MutationRef, "issue85-conflict-rollback")
	issue85AssertError(t, terminal, eebusraw.ErrorCodeV1Conflict)
	issue85AssertState(t, conflict, eebusraw.MutationStateV1Conflict)
	reads, writes, _, exhausted := harness.executor.counts()
	if reads != 3 || writes != 1 || exhausted != 0 {
		t.Fatalf("third-value rollback calls = reads:%d writes:%d exhausted:%d", reads, writes, exhausted)
	}
}

func TestIssue85ProbeDeadlineRearmsAcrossCleanRestart(t *testing.T) {
	firstHarness := newIssue85Harness(t)
	firstHarness.request.Mode = eebusraw.ModeV1Probe
	firstHarness.request.ProbeTTLSeconds = 60
	probe, terminal := firstHarness.set()
	issue85AssertNoError(t, terminal)
	issue85AssertState(t, probe, eebusraw.MutationStateV1ProbeActive)
	firstHarness.closeClean()

	recoveryExecutor := &issue85Executor{t: t, events: firstHarness.events}
	recoveryScheduler := newIssue85Scheduler(firstHarness.clock, firstHarness.events)
	restartedDraft := issue85HarnessDraft(t)
	restartedDraft.request.Mode = eebusraw.ModeV1Probe
	restartedDraft.request.ProbeTTLSeconds = 60
	recoveryExecutor.setSteps(
		[]issue85ReadStep{
			restartedDraft.readStep(restartedDraft.requested),
			restartedDraft.readStep(restartedDraft.before),
		},
		[]issue85WriteStep{
			restartedDraft.writeStep(restartedDraft.before, rawMutationWriteResult{
				FrameSent:  true,
				Correlated: true,
				Accepted:   true,
			}),
		},
	)
	restarted := newIssue85Harness(
		t,
		issue85WithRoot(firstHarness.root),
		issue85WithExecutor(recoveryExecutor),
		issue85WithTokenVerifier(firstHarness.tokens),
		issue85WithPolicy(firstHarness.policy),
		issue85WithClock(firstHarness.clock),
		issue85WithScheduler(recoveryScheduler),
		issue85WithEvents(firstHarness.events),
	)
	if recoveryScheduler.pendingCount() != 1 {
		t.Fatalf("restart scheduled probe callbacks = %d, want 1", recoveryScheduler.pendingCount())
	}
	reads, writes, _, exhausted := recoveryExecutor.counts()
	if reads != 0 || writes != 0 || exhausted != 0 {
		t.Fatalf("unexpired probe restart contacted remote: reads:%d writes:%d exhausted:%d", reads, writes, exhausted)
	}

	firstHarness.clock.Advance(60 * time.Second)
	if fired := recoveryScheduler.FireDue(); fired != 1 {
		t.Fatalf("rearmed probe fired %d callbacks, want 1", fired)
	}
	issue85Eventually(t, func() bool {
		status, statusError := restarted.status(probe.MutationRef)
		return statusError == nil && status.State == eebusraw.MutationStateV1RolledBack
	}, "restarted probe to roll back")
	reads, writes, _, exhausted = recoveryExecutor.counts()
	if reads != 2 || writes != 1 || exhausted != 0 {
		t.Fatalf("restarted probe rollback calls = reads:%d writes:%d exhausted:%d", reads, writes, exhausted)
	}
}

func TestIssue85ExpiredProbeRecoveryWaitsForDeterministicTrigger(t *testing.T) {
	firstHarness := newIssue85Harness(t)
	firstHarness.request.Mode = eebusraw.ModeV1Probe
	firstHarness.request.ProbeTTLSeconds = 30
	probe, terminal := firstHarness.set()
	issue85AssertNoError(t, terminal)
	firstHarness.closeClean()
	firstHarness.clock.Advance(31 * time.Second)

	recoveryExecutor := &issue85Executor{t: t, events: firstHarness.events}
	recoveryScheduler := newIssue85Scheduler(firstHarness.clock, firstHarness.events)
	draft := issue85HarnessDraft(t)
	expiredProfile := draft.exactLabProfile()
	expiredProfile.ExpiresAt = firstHarness.clock.Now().Add(-time.Second)
	recoveryExecutor.setSteps(
		[]issue85ReadStep{
			draft.readStep(draft.requested),
			draft.readStep(draft.before),
		},
		[]issue85WriteStep{
			draft.writeStep(draft.before, rawMutationWriteResult{
				FrameSent:  true,
				Correlated: true,
				Accepted:   true,
			}),
		},
	)
	restarted := newIssue85Harness(
		t,
		issue85WithRoot(firstHarness.root),
		issue85WithExecutor(recoveryExecutor),
		issue85WithTokenVerifier(firstHarness.tokens),
		issue85WithPolicy(firstHarness.policy),
		issue85WithClock(firstHarness.clock),
		issue85WithScheduler(recoveryScheduler),
		issue85WithEvents(firstHarness.events),
		issue85WithProfile(expiredProfile),
	)
	reads, writes, _, exhausted := recoveryExecutor.counts()
	if reads != 0 || writes != 0 || exhausted != 0 {
		t.Fatalf("constructor bypassed deterministic expiry trigger: reads:%d writes:%d exhausted:%d", reads, writes, exhausted)
	}
	if fired := recoveryScheduler.FireDue(); fired != 1 {
		t.Fatalf("expired restart fired %d callbacks, want 1", fired)
	}
	issue85Eventually(t, func() bool {
		status, statusError := restarted.status(probe.MutationRef)
		return statusError == nil && status.State == eebusraw.MutationStateV1RolledBack
	}, "expired probe recovery to roll back")
}
