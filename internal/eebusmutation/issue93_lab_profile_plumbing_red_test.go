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
