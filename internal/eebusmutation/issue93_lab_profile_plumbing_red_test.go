package eebusmutation

import (
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

func TestIssue93ProfileMismatchFailsBeforeGuardReadOrWriteContact(t *testing.T) {
	template := issue85HarnessDraft(t)
	profile := template.exactLabProfile()
	harness := newIssue85Harness(t, issue85WithProfile(profile))
	harness.policy.decision.ConstraintsKnown = false
	harness.request.ConstraintsOverride = &eebusraw.ConstraintOverrideV1{
		ProfileID:     profile.ProfileID,
		Justification: "bounded issue93 probe",
		ExpiresAt:     harness.clock.Now().Add(5 * time.Minute),
	}
	harness.request.ProbeTTLSeconds = profile.MaximumProbeTTLSeconds + 1
	harness.request.Mode = eebusraw.ModeV1Probe

	_, terminal := harness.set()
	issue85AssertError(t, terminal, eebusraw.ErrorCodeV1ConstraintsUnknown)
	reads, writes, _, exhausted := harness.executor.counts()
	if reads != 0 || writes != 0 || exhausted != 0 {
		t.Fatalf("inexact profile remote calls = reads:%d writes:%d exhausted:%d", reads, writes, exhausted)
	}
}

func TestIssue93ProfileExpiryBlocksNewForwardWriteWithoutContact(t *testing.T) {
	template := issue85HarnessDraft(t)
	profile := template.exactLabProfile()
	profile.ExpiresAt = template.clock.Now().Add(time.Minute)
	harness := newIssue85Harness(t, issue85WithProfile(profile))
	harness.policy.decision.ConstraintsKnown = false
	harness.request.ConstraintsOverride = &eebusraw.ConstraintOverrideV1{
		ProfileID:     profile.ProfileID,
		Justification: "bounded issue93 probe",
		ExpiresAt:     profile.ExpiresAt,
	}
	harness.clock.Advance(2 * time.Minute)

	_, terminal := harness.set()
	issue85AssertError(t, terminal, eebusraw.ErrorCodeV1ConstraintsUnknown)
	reads, writes, _, exhausted := harness.executor.counts()
	if reads != 0 || writes != 0 || exhausted != 0 {
		t.Fatalf("expired profile remote calls = reads:%d writes:%d exhausted:%d", reads, writes, exhausted)
	}
}

func TestIssue93ForwardWriteRechecksEffectiveExpiryAfterDispatchIntent(t *testing.T) {
	template := issue85HarnessDraft(t)
	profile := template.exactLabProfile()
	profile.ExpiresAt = template.clock.Now().Add(2 * time.Minute)
	harness := newIssue85Harness(
		t,
		issue85WithProfile(profile),
		func(harness *issue85Harness) {
			harness.deps.CrashAfterDurable = func(state eebusraw.MutationStateV1) error {
				harness.events.add("durable-state:" + string(state))
				if state == eebusraw.MutationStateV1DispatchIntent {
					harness.clock.Advance(90 * time.Second)
				}
				return nil
			}
		},
	)
	harness.policy.decision.ConstraintsKnown = false
	harness.request.ConstraintsOverride = &eebusraw.ConstraintOverrideV1{
		ProfileID:     profile.ProfileID,
		Justification: "bounded issue93 probe",
		ExpiresAt:     harness.clock.Now().Add(time.Minute),
	}

	mutation, terminal := harness.set()
	issue85AssertError(t, terminal, eebusraw.ErrorCodeV1ConstraintsUnknown)
	issue85AssertState(t, mutation, eebusraw.MutationStateV1FailedNoContact)
	if mutation.NoContactEvidence == nil ||
		mutation.NoContactEvidence.RemoteFramesSent != 0 ||
		mutation.NoContactEvidence.LastCompletedPhase != "dispatch_intent_persisted" {
		t.Fatalf("expired forward write no-contact evidence = %+v", mutation.NoContactEvidence)
	}
	reads, writes, _, exhausted := harness.executor.counts()
	if reads != 1 || writes != 0 || exhausted != 0 {
		t.Fatalf("expired post-intent calls = reads:%d writes:%d exhausted:%d", reads, writes, exhausted)
	}
}

func TestIssue93ExpiredProfileDoesNotBlockDurableProbeRecovery(t *testing.T) {
	template := issue85HarnessDraft(t)
	profile := template.exactLabProfile()
	profile.ExpiresAt = template.clock.Now().Add(time.Minute)
	first := newIssue85Harness(t, issue85WithProfile(profile))
	first.policy.decision.ConstraintsKnown = false
	first.request.Mode = eebusraw.ModeV1Probe
	first.request.ProbeTTLSeconds = 30
	first.request.ConstraintsOverride = &eebusraw.ConstraintOverrideV1{
		ProfileID:     profile.ProfileID,
		Justification: "bounded issue93 probe",
		ExpiresAt:     profile.ExpiresAt,
	}

	probe, terminal := first.set()
	issue85AssertNoError(t, terminal)
	issue85AssertState(t, probe, eebusraw.MutationStateV1ProbeActive)
	first.closeClean()
	first.clock.Advance(61 * time.Second)

	recoveryExecutor := &issue85Executor{t: t, events: first.events}
	recoveryScheduler := newIssue85Scheduler(first.clock, first.events)
	recoveryExecutor.setSteps(
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
	restarted := newIssue85Harness(
		t,
		issue85WithRoot(first.root),
		issue85WithExecutor(recoveryExecutor),
		issue85WithTokenVerifier(first.tokens),
		issue85WithPolicy(first.policy),
		issue85WithClock(first.clock),
		issue85WithScheduler(recoveryScheduler),
		issue85WithEvents(first.events),
		issue85WithProfile(profile),
	)
	if fired := recoveryScheduler.FireDue(); fired != 1 {
		t.Fatalf("expired profile recovery fired %d callbacks, want 1", fired)
	}
	issue85Eventually(t, func() bool {
		status, statusError := restarted.status(probe.MutationRef)
		return statusError == nil && status.State == eebusraw.MutationStateV1RolledBack
	}, "expired-profile probe recovery to roll back")
	reads, writes, _, exhausted := recoveryExecutor.counts()
	if reads != 2 || writes != 1 || exhausted != 0 {
		t.Fatalf("expired profile recovery calls = reads:%d writes:%d exhausted:%d", reads, writes, exhausted)
	}
}

func TestIssue93CoordinatorRequiresExactPolicyEvidenceAndSafetyAttestation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*rawMutationPolicyDecision)
	}{
		{name: "profile id", mutate: func(decision *rawMutationPolicyDecision) {
			decision.LabProfileID = "other-profile"
		}},
		{name: "evidence", mutate: func(decision *rawMutationPolicyDecision) {
			decision.EvidenceHashes[0] = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
		}},
		{name: "safety", mutate: func(decision *rawMutationPolicyDecision) {
			decision.SafetyPredicates[0] = "other-predicate"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			template := issue85HarnessDraft(t)
			profile := template.exactLabProfile()
			harness := newIssue85Harness(t, issue85WithProfile(profile))
			harness.policy.decision.ConstraintsKnown = false
			harness.policy.decision.LabProfileID = profile.ProfileID
			harness.policy.decision.EvidenceHashes = append(
				[]eebusraw.HashV1(nil),
				profile.EvidenceHashes...,
			)
			harness.policy.decision.SafetyPredicates = append(
				[]string(nil),
				profile.SafetyPredicates...,
			)
			test.mutate(&harness.policy.decision)
			harness.request.ConstraintsOverride = &eebusraw.ConstraintOverrideV1{
				ProfileID:     profile.ProfileID,
				Justification: "bounded issue93 probe",
				ExpiresAt:     harness.clock.Now().Add(5 * time.Minute),
			}
			harness.executor.setSteps([]issue85ReadStep{harness.readStep(harness.before)}, nil)

			_, terminal := harness.set()
			issue85AssertError(t, terminal, eebusraw.ErrorCodeV1ConstraintsUnknown)
			_, writes, _, exhausted := harness.executor.counts()
			if writes != 0 || exhausted != 0 {
				t.Fatalf("mismatched policy attestation emitted writes=%d exhausted=%d", writes, exhausted)
			}
		})
	}
}
