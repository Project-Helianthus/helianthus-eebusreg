package eebusmutation

import (
	"sync"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

func TestCorrelatedContradictionsRemainOutcomeUnknownWithoutConflictQuarantine(t *testing.T) {
	for _, test := range []struct {
		name     string
		accepted bool
		observed func(*issue85Harness) eebusraw.TypedValueV1
	}{
		{
			name:     "accepted but unchanged",
			accepted: true,
			observed: func(harness *issue85Harness) eebusraw.TypedValueV1 {
				return harness.before
			},
		},
		{
			name:     "rejected but requested observed",
			accepted: false,
			observed: func(harness *issue85Harness) eebusraw.TypedValueV1 {
				return harness.requested
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newIssue85Harness(t)
			harness.executor.setSteps(
				[]issue85ReadStep{
					harness.readStep(harness.before),
					harness.readStep(test.observed(harness)),
				},
				[]issue85WriteStep{
					harness.writeStep(harness.requested, rawMutationWriteResult{
						FrameSent: true, Correlated: true, Accepted: test.accepted,
					}),
				},
			)
			mutation, terminal := harness.set()
			issue85AssertError(t, terminal, eebusraw.ErrorCodeV1OutcomeUnknown)
			issue85AssertState(t, mutation, eebusraw.MutationStateV1OutcomeUnknown)
			if mutation.ConflictEvidence != nil {
				t.Fatalf("contradictory correlated outcome emitted conflict evidence: %+v", mutation.ConflictEvidence)
			}
			coordinator := harness.coordinator.(*rawMutationCoordinator)
			coordinator.mu.Lock()
			quarantined := coordinator.quarantined
			coordinator.mu.Unlock()
			if quarantined {
				t.Fatal("contradictory correlated outcome triggered global conflict quarantine")
			}
		})
	}
}

type remediationBindingAuthority struct {
	mu      sync.Mutex
	binding eebusraw.RuntimeBindingV1
	targets []eebusraw.FeatureTargetV1
}

func (authority *remediationBindingAuthority) CurrentRuntimeBinding(
	target eebusraw.FeatureTargetV1,
) (eebusraw.RuntimeBindingV1, *eebusraw.ErrorV1) {
	authority.mu.Lock()
	authority.targets = append(authority.targets, target.Clone())
	authority.mu.Unlock()
	return authority.binding, nil
}

func TestRecoveryReadBindingAuthorityUsesReadTarget(t *testing.T) {
	harness := issue85HarnessDraft(t)
	authority := &remediationBindingAuthority{
		binding: eebusraw.RuntimeBindingV1{
			RuntimeEpoch:         harness.epoch,
			ConnectionGeneration: harness.generation,
		},
	}
	harness.deps.BindingAuthority = authority
	harness.open()
	mutation, terminal := harness.set()
	issue85AssertNoError(t, terminal)
	issue85AssertState(t, mutation, eebusraw.MutationStateV1Applied)

	authority.mu.Lock()
	targets := append([]eebusraw.FeatureTargetV1(nil), authority.targets...)
	authority.mu.Unlock()
	if len(targets) < 2 {
		t.Fatalf("binding authority calls = %d, want precontact WRITE and readback READ", len(targets))
	}
	if targets[0].Operation != eebusraw.OperationV1Write {
		t.Fatalf("precontact binding operation = %q, want WRITE", targets[0].Operation)
	}
	if targets[len(targets)-1].Operation != eebusraw.OperationV1Read {
		t.Fatalf("readback binding operation = %q, want READ", targets[len(targets)-1].Operation)
	}
}

func TestConcurrentSameIdempotencyKeyJoinsReservedMutationIdentity(t *testing.T) {
	harness := newIssue85Harness(t)
	releaseRead := make(chan struct{})
	harness.executor.setSteps(
		[]issue85ReadStep{
			func() issue85ReadStep {
				step := harness.readStep(harness.before)
				step.block = releaseRead
				return step
			}(),
			harness.readStep(harness.requested),
		},
		[]issue85WriteStep{
			harness.writeStep(harness.requested, rawMutationWriteResult{
				FrameSent: true, Correlated: true, Accepted: true,
			}),
		},
	)
	type result struct {
		mutation eebusraw.MutationV1
		terminal *eebusraw.ErrorV1
	}
	firstDone := make(chan result, 1)
	go func() {
		mutation, terminal := harness.set()
		firstDone <- result{mutation: mutation, terminal: terminal}
	}()
	waitForIssue85Calls(t, harness.executor, 1, 0)

	joined, terminal := harness.set()
	issue85AssertNoError(t, terminal)
	if joined.MutationRef == "" {
		t.Fatal("same-key in-flight join omitted the reserved mutation identity")
	}
	close(releaseRead)
	select {
	case first := <-firstDone:
		issue85AssertNoError(t, first.terminal)
		if first.mutation.MutationRef != joined.MutationRef {
			t.Fatalf("joined mutation ref = %q, first = %q", joined.MutationRef, first.mutation.MutationRef)
		}
	case <-time.After(time.Second):
		t.Fatal("first same-key mutation did not complete")
	}
}

func TestCoordinatorCloseCancelsSilentPeerBeforeWriterWait(t *testing.T) {
	harness := newIssue85Harness(t)
	neverReply := make(chan struct{})
	step := harness.readStep(harness.before)
	step.block = neverReply
	harness.executor.setSteps([]issue85ReadStep{step}, nil)

	setDone := make(chan struct{})
	go func() {
		_, _ = harness.set()
		close(setDone)
	}()
	waitForIssue85Calls(t, harness.executor, 1, 0)

	closeDone := make(chan *eebusraw.ErrorV1, 1)
	go func() {
		closeDone <- harness.coordinator.Close()
	}()
	select {
	case terminal := <-closeDone:
		issue85AssertNoError(t, terminal)
	case <-time.After(time.Second):
		t.Fatal("coordinator Close remained blocked on a silent peer")
	}
	select {
	case <-setDone:
	case <-time.After(time.Second):
		t.Fatal("coordinator-owned cancellation did not unblock the active writer")
	}
	harness.coordinator = nil
}

func TestFailedAutomaticProbeRollbackDurablyQuarantinesAllWriters(t *testing.T) {
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
				FrameSent: true, Correlated: true, Accepted: true,
			}),
		},
	)
	probe, terminal := harness.set()
	issue85AssertNoError(t, terminal)
	issue85AssertState(t, probe, eebusraw.MutationStateV1ProbeActive)
	harness.clock.Advance(time.Second)
	if fired := harness.scheduler.FireDue(); fired != 1 {
		t.Fatalf("probe rollback callbacks fired = %d, want 1", fired)
	}
	status, terminal := harness.status(probe.MutationRef)
	issue85AssertNoError(t, terminal)
	issue85AssertState(t, status, eebusraw.MutationStateV1OutcomeUnknown)
	if status.OutcomeEvidence == nil ||
		status.OutcomeEvidence.LastDurableState != eebusraw.MutationStateV1RollbackIntent {
		t.Fatalf("failed automatic rollback lacks durable quarantine evidence: %+v", status)
	}

	_, terminal = harness.rollback(probe.MutationRef, "quarantined-explicit-rollback")
	issue85AssertError(t, terminal, eebusraw.ErrorCodeV1Conflict)
	coordinator := harness.coordinator.(*rawMutationCoordinator)
	if coordinator.acquireInternalWriter() {
		coordinator.releaseWriter()
		t.Fatal("durable write quarantine admitted an internal recovery writer")
	}
}

func TestCoordinatorActivationRearmsAndReconcilesRollbackReplyRecovery(t *testing.T) {
	first := newIssue85Harness(
		t,
		issue85WithCrash(eebusraw.MutationStateV1RollbackReplyObserved),
	)
	first.executor.setSteps(
		[]issue85ReadStep{
			first.readStep(first.before),
			first.readStep(first.requested),
			first.readStep(first.requested),
		},
		[]issue85WriteStep{
			first.writeStep(first.requested, rawMutationWriteResult{
				FrameSent: true, Correlated: true, Accepted: true,
			}),
			first.writeStep(first.before, rawMutationWriteResult{
				FrameSent: true, Correlated: true, Accepted: true,
			}),
		},
	)
	applied, terminal := first.set()
	issue85AssertNoError(t, terminal)
	_, terminal = first.rollback(applied.MutationRef, "activation-recovery-rollback")
	issue85AssertError(t, terminal, eebusraw.ErrorCodeV1Internal)
	first.closeClean()

	restarted := newIssue85Harness(t, issue85WithRoot(first.root))
	restarted.executor.setSteps(
		[]issue85ReadStep{restarted.readStep(restarted.before)},
		nil,
	)
	if pending := restarted.scheduler.pendingCount(); pending == 0 {
		t.Fatal("coordinator activation did not rearm durable rollback recovery")
	}
	if fired := restarted.scheduler.FireDue(); fired != 1 {
		t.Fatalf("activation recovery callbacks fired = %d, want 1", fired)
	}
	status, terminal := restarted.status(applied.MutationRef)
	issue85AssertNoError(t, terminal)
	issue85AssertState(t, status, eebusraw.MutationStateV1RolledBack)
	if status.ConflictEvidence != nil {
		t.Fatalf("rollback reply recovery created false conflict evidence: %+v", status.ConflictEvidence)
	}
}

func waitForIssue85Calls(
	t *testing.T,
	executor *issue85Executor,
	reads int,
	writes int,
) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		gotReads, gotWrites, _, _ := executor.counts()
		if gotReads >= reads && gotWrites >= writes {
			return
		}
		time.Sleep(time.Millisecond)
	}
	gotReads, gotWrites, _, _ := executor.counts()
	t.Fatalf("executor calls = reads:%d writes:%d, want at least reads:%d writes:%d", gotReads, gotWrites, reads, writes)
}
