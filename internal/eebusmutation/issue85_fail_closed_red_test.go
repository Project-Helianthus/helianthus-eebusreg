package eebusmutation

import (
	"context"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

func TestIssue85AuthorizationAndClosedShapeRejectBeforeDependencyContact(t *testing.T) {
	tests := []struct {
		name       string
		mutateAuth func(*eebusraw.WriteAuthorizationV1)
		mutate     func(*eebusraw.FeatureDataSetRequestV1)
		want       eebusraw.ErrorCodeV1
	}{
		{
			name: "missing principal",
			mutateAuth: func(auth *eebusraw.WriteAuthorizationV1) {
				auth.PrincipalClass = ""
			},
			want: eebusraw.ErrorCodeV1PermissionDenied,
		},
		{
			name: "read scope cannot authorize write",
			mutateAuth: func(auth *eebusraw.WriteAuthorizationV1) {
				auth.Scope = eebusraw.AuthScopeV1RawRead
			},
			want: eebusraw.ErrorCodeV1PermissionDenied,
		},
		{
			name: "wrong tool",
			mutateAuth: func(auth *eebusraw.WriteAuthorizationV1) {
				auth.Tool = eebusraw.ToolV1MutationsRollback
			},
			want: eebusraw.ErrorCodeV1PermissionDenied,
		},
		{
			name: "redacted tier",
			mutateAuth: func(auth *eebusraw.WriteAuthorizationV1) {
				auth.MaskTier = eebusraw.MaskTierRedacted
			},
			want: eebusraw.ErrorCodeV1PermissionDenied,
		},
		{
			name: "read operation",
			mutate: func(request *eebusraw.FeatureDataSetRequestV1) {
				request.Target.Operation = eebusraw.OperationV1Read
			},
			want: eebusraw.ErrorCodeV1UnsupportedOperation,
		},
		{
			name: "empty read token",
			mutate: func(request *eebusraw.FeatureDataSetRequestV1) {
				request.ReadToken = ""
			},
			want: eebusraw.ErrorCodeV1InvalidArgument,
		},
		{
			name: "empty idempotency key",
			mutate: func(request *eebusraw.FeatureDataSetRequestV1) {
				request.IdempotencyKey = ""
			},
			want: eebusraw.ErrorCodeV1InvalidArgument,
		},
		{
			name: "unknown mode",
			mutate: func(request *eebusraw.FeatureDataSetRequestV1) {
				request.Mode = eebusraw.ModeV1("future")
			},
			want: eebusraw.ErrorCodeV1InvalidArgument,
		},
		{
			name: "apply carries probe ttl",
			mutate: func(request *eebusraw.FeatureDataSetRequestV1) {
				request.ProbeTTLSeconds = 30
			},
			want: eebusraw.ErrorCodeV1InvalidArgument,
		},
		{
			name: "probe omits ttl",
			mutate: func(request *eebusraw.FeatureDataSetRequestV1) {
				request.Mode = eebusraw.ModeV1Probe
			},
			want: eebusraw.ErrorCodeV1InvalidArgument,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newIssue85Harness(t)
			auth := harness.auth
			request := harness.request
			request.Target = request.Target.Clone()
			request.Value = request.Value.Clone()
			if test.mutateAuth != nil {
				test.mutateAuth(&auth)
			}
			if test.mutate != nil {
				test.mutate(&request)
			}

			_, terminal := harness.coordinator.FeaturesDataSet(
				context.Background(),
				auth,
				request,
			)
			issue85AssertError(t, terminal, test.want)
			reads, writes, _, exhausted := harness.executor.counts()
			if reads != 0 || writes != 0 || exhausted != 0 {
				t.Fatalf("pre-contact rejection remote calls = reads:%d writes:%d exhausted:%d", reads, writes, exhausted)
			}
			verify, consume := harness.tokens.counts()
			if verify != 0 || consume != 0 || harness.policy.count() != 0 {
				t.Fatalf("pre-contact rejection dependencies = token verify:%d consume:%d policy:%d", verify, consume, harness.policy.count())
			}
		})
	}
}

func TestIssue85PolicyGatesFailClosedWithZeroWriteFrames(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*rawMutationPolicyDecision)
		want   eebusraw.ErrorCodeV1
	}{
		{
			name: "full write unavailable",
			mutate: func(decision *rawMutationPolicyDecision) {
				decision.FullWrite = false
			},
			want: eebusraw.ErrorCodeV1UnsupportedOperation,
		},
		{
			name: "changeability false",
			mutate: func(decision *rawMutationPolicyDecision) {
				decision.Changeability = eebusraw.ChangeabilityV1False
			},
			want: eebusraw.ErrorCodeV1ConstraintFailure,
		},
		{
			name: "changeability unknown",
			mutate: func(decision *rawMutationPolicyDecision) {
				decision.Changeability = eebusraw.ChangeabilityV1Unknown
			},
			want: eebusraw.ErrorCodeV1ConstraintFailure,
		},
		{
			name: "constraints unknown",
			mutate: func(decision *rawMutationPolicyDecision) {
				decision.ConstraintsKnown = false
			},
			want: eebusraw.ErrorCodeV1ConstraintsUnknown,
		},
		{
			name: "target not lab allowlisted",
			mutate: func(decision *rawMutationPolicyDecision) {
				decision.LabAllowlisted = false
			},
			want: eebusraw.ErrorCodeV1ConstraintFailure,
		},
		{
			name: "rollback shape incomplete",
			mutate: func(decision *rawMutationPolicyDecision) {
				decision.RollbackRepresentable = false
			},
			want: eebusraw.ErrorCodeV1ConstraintFailure,
		},
		{
			name: "known constraint failure",
			mutate: func(decision *rawMutationPolicyDecision) {
				decision.ConstraintFailures = []string{"range"}
			},
			want: eebusraw.ErrorCodeV1ConstraintFailure,
		},
		{
			name: "safety predicate failure",
			mutate: func(decision *rawMutationPolicyDecision) {
				decision.SafetyFailures = []string{"rollback"}
			},
			want: eebusraw.ErrorCodeV1ConstraintFailure,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newIssue85Harness(t)
			decision := harness.policy.decision
			test.mutate(&decision)
			harness.policy.decision = decision
			harness.executor.setSteps([]issue85ReadStep{harness.readStep(harness.before)}, nil)

			_, terminal := harness.set()
			issue85AssertError(t, terminal, test.want)
			reads, writes, _, exhausted := harness.executor.counts()
			if writes != 0 || reads > 1 || exhausted != 0 {
				t.Fatalf("policy rejection remote calls = reads:%d writes:%d exhausted:%d", reads, writes, exhausted)
			}
		})
	}
}

func TestIssue85UnknownConstraintsRequireOneExactVersionedLabProfile(t *testing.T) {
	template := issue85HarnessDraft(t)
	profile := template.exactLabProfile()
	harness := newIssue85Harness(t, issue85WithProfile(profile))
	harness.policy.decision.ConstraintsKnown = false
	harness.request.ConstraintsOverride = &eebusraw.ConstraintOverrideV1{
		ProfileID:     profile.ProfileID,
		Justification: "bounded issue85 probe",
		ExpiresAt:     harness.clock.Now().Add(5 * time.Minute),
	}

	mutation, terminal := harness.set()
	issue85AssertNoError(t, terminal)
	issue85AssertState(t, mutation, eebusraw.MutationStateV1Applied)
}

func TestIssue85LabProfileNeverInfersWildcardFamilySiblingOrWiderTTL(t *testing.T) {
	tests := []struct {
		name    string
		profile func(*issue85Harness) rawMutationLabProfile
		mutate  func(*issue85Harness, *eebusraw.ConstraintOverrideV1)
	}{
		{
			name: "sibling feature",
			profile: func(template *issue85Harness) rawMutationLabProfile {
				profile := template.exactLabProfile()
				profile.Target.FeatureAddress++
				return profile
			},
		},
		{
			name: "different value commitment",
			profile: func(template *issue85Harness) rawMutationLabProfile {
				profile := template.exactLabProfile()
				thirdHash, err := template.third.ComputeHash()
				if err != nil {
					template.t.Fatal(err)
				}
				profile.AllowedValueHashes = []eebusraw.HashV1{thirdHash}
				return profile
			},
		},
		{
			name: "different rollback shape",
			profile: func(template *issue85Harness) rawMutationLabProfile {
				profile := template.exactLabProfile()
				thirdHash, err := template.third.ComputeHash()
				if err != nil {
					template.t.Fatal(err)
				}
				profile.RollbackValueHash = thirdHash
				return profile
			},
		},
		{
			name: "wrong profile id",
			profile: func(template *issue85Harness) rawMutationLabProfile {
				return template.exactLabProfile()
			},
			mutate: func(_ *issue85Harness, override *eebusraw.ConstraintOverrideV1) {
				override.ProfileID = "other-profile"
			},
		},
		{
			name: "expired request authority",
			profile: func(template *issue85Harness) rawMutationLabProfile {
				return template.exactLabProfile()
			},
			mutate: func(harness *issue85Harness, override *eebusraw.ConstraintOverrideV1) {
				override.ExpiresAt = harness.clock.Now().Add(-time.Second)
			},
		},
		{
			name: "probe ttl exceeds profile",
			profile: func(template *issue85Harness) rawMutationLabProfile {
				return template.exactLabProfile()
			},
			mutate: func(harness *issue85Harness, _ *eebusraw.ConstraintOverrideV1) {
				harness.request.Mode = eebusraw.ModeV1Probe
				harness.request.ProbeTTLSeconds = 61
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			template := issue85HarnessDraft(t)
			profile := test.profile(template)
			harness := newIssue85Harness(t, issue85WithProfile(profile))
			harness.policy.decision.ConstraintsKnown = false
			override := eebusraw.ConstraintOverrideV1{
				ProfileID:     "issue85-exact-profile",
				Justification: "bounded issue85 probe",
				ExpiresAt:     harness.clock.Now().Add(5 * time.Minute),
			}
			harness.request.ConstraintsOverride = &override
			if test.mutate != nil {
				test.mutate(harness, harness.request.ConstraintsOverride)
			}
			harness.executor.setSteps([]issue85ReadStep{harness.readStep(harness.before)}, nil)

			_, terminal := harness.set()
			issue85AssertError(t, terminal, eebusraw.ErrorCodeV1ConstraintsUnknown)
			_, writes, _, exhausted := harness.executor.counts()
			if writes != 0 || exhausted != 0 {
				t.Fatalf("inexact lab profile emitted writes=%d exhausted=%d", writes, exhausted)
			}
		})
	}
}

func TestIssue85MalformedLabProfilesFailCoordinatorOpenWithoutContact(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*rawMutationLabProfile)
	}{
		{
			name: "unknown contract version",
			mutate: func(profile *rawMutationLabProfile) {
				profile.Contract = "helianthus.eebus.raw-mutation-lab-profile.v2"
			},
		},
		{
			name: "expired profile",
			mutate: func(profile *rawMutationLabProfile) {
				profile.ExpiresAt = time.Time{}
			},
		},
		{
			name: "missing safety predicates",
			mutate: func(profile *rawMutationLabProfile) {
				profile.SafetyPredicates = nil
			},
		},
		{
			name: "missing allowed values",
			mutate: func(profile *rawMutationLabProfile) {
				profile.AllowedValueHashes = nil
			},
		},
		{
			name: "family wildcard target",
			mutate: func(profile *rawMutationLabProfile) {
				profile.Target.RemoteSKI = ""
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			draft := issue85HarnessDraft(t)
			profile := draft.exactLabProfile()
			test.mutate(&profile)
			draft.config.LabProfiles = []rawMutationLabProfile{profile}
			draft.executor.setSteps(nil, nil)

			coordinator, terminal := draft.tryOpen()
			if coordinator != nil {
				_ = coordinator.Close()
				t.Fatal("malformed lab profile returned a live coordinator")
			}
			issue85AssertError(t, terminal, eebusraw.ErrorCodeV1InvalidArgument)
			reads, writes, _, exhausted := draft.executor.counts()
			if reads != 0 || writes != 0 || exhausted != 0 {
				t.Fatalf("malformed profile open contacted remote: reads:%d writes:%d exhausted:%d", reads, writes, exhausted)
			}
		})
	}
}

func TestIssue85ReadTokenBindingFailsBeforeRemoteContact(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*issue85Harness, *rawMutationReadTokenBinding)
		want   eebusraw.ErrorCodeV1
	}{
		{
			name: "expired",
			mutate: func(harness *issue85Harness, binding *rawMutationReadTokenBinding) {
				binding.ExpiresAt = harness.clock.Now().Add(-time.Second)
			},
			want: eebusraw.ErrorCodeV1StaleReadToken,
		},
		{
			name: "target substitution",
			mutate: func(_ *issue85Harness, binding *rawMutationReadTokenBinding) {
				binding.Target.FeatureAddress++
			},
			want: eebusraw.ErrorCodeV1StaleReadToken,
		},
		{
			name: "request hash substitution",
			mutate: func(_ *issue85Harness, binding *rawMutationReadTokenBinding) {
				binding.RequestHash = eebusraw.HashV1("sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
			},
			want: eebusraw.ErrorCodeV1StaleReadToken,
		},
		{
			name: "principal substitution",
			mutate: func(_ *issue85Harness, binding *rawMutationReadTokenBinding) {
				binding.PrincipalClass = "other-owner"
			},
			want: eebusraw.ErrorCodeV1PermissionDenied,
		},
		{
			name: "tier substitution",
			mutate: func(_ *issue85Harness, binding *rawMutationReadTokenBinding) {
				binding.MaskTier = eebusraw.MaskTierRedacted
			},
			want: eebusraw.ErrorCodeV1PermissionDenied,
		},
		{
			name: "runtime epoch substitution",
			mutate: func(_ *issue85Harness, binding *rawMutationReadTokenBinding) {
				binding.Runtime.RuntimeEpoch++
			},
			want: eebusraw.ErrorCodeV1RuntimeEpochMismatch,
		},
		{
			name: "connection generation substitution",
			mutate: func(_ *issue85Harness, binding *rawMutationReadTokenBinding) {
				binding.Runtime.ConnectionGeneration++
			},
			want: eebusraw.ErrorCodeV1ConnectionGenerationMismatch,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newIssue85Harness(t)
			binding := harness.tokens.bindings[harness.request.ReadToken]
			binding.Target = binding.Target.Clone()
			test.mutate(harness, &binding)
			harness.tokens.bindings[harness.request.ReadToken] = binding

			_, terminal := harness.set()
			issue85AssertError(t, terminal, test.want)
			reads, writes, _, exhausted := harness.executor.counts()
			if reads != 0 || writes != 0 || exhausted != 0 {
				t.Fatalf("token rejection contacted remote: reads:%d writes:%d exhausted:%d", reads, writes, exhausted)
			}
			if harness.policy.count() != 0 {
				t.Fatalf("token rejection reached policy %d times", harness.policy.count())
			}
		})
	}
}

func TestIssue85FreshReadCASAndRuntimeBindingMismatchEmitZeroWrites(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*issue85Harness) issue85ReadStep
		expected  *eebusraw.TypedValueV1
		want      eebusraw.ErrorCodeV1
	}{
		{
			name: "before image changed",
			configure: func(harness *issue85Harness) issue85ReadStep {
				return harness.readStep(harness.third)
			},
			want: eebusraw.ErrorCodeV1CASMismatch,
		},
		{
			name: "expected current changed",
			configure: func(harness *issue85Harness) issue85ReadStep {
				return harness.readStep(harness.before)
			},
			expected: func() *eebusraw.TypedValueV1 {
				return &eebusraw.TypedValueV1{}
			}(),
			want: eebusraw.ErrorCodeV1CASMismatch,
		},
		{
			name: "runtime epoch changed",
			configure: func(harness *issue85Harness) issue85ReadStep {
				step := harness.readStep(harness.before)
				step.result.Runtime.RuntimeEpoch++
				return step
			},
			want: eebusraw.ErrorCodeV1RuntimeEpochMismatch,
		},
		{
			name: "connection generation changed",
			configure: func(harness *issue85Harness) issue85ReadStep {
				step := harness.readStep(harness.before)
				step.result.Runtime.ConnectionGeneration++
				return step
			},
			want: eebusraw.ErrorCodeV1ConnectionGenerationMismatch,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newIssue85Harness(t)
			if test.expected != nil {
				expected := harness.requested.Clone()
				harness.request.ExpectedCurrent = &expected
			}
			harness.executor.setSteps([]issue85ReadStep{test.configure(harness)}, nil)

			_, terminal := harness.set()
			issue85AssertError(t, terminal, test.want)
			reads, writes, _, exhausted := harness.executor.counts()
			if reads != 1 || writes != 0 || exhausted != 0 {
				t.Fatalf("guard result remote calls = reads:%d writes:%d exhausted:%d", reads, writes, exhausted)
			}
		})
	}
}

func TestIssue85PreSendTransportFailureIsDurableFailedNoContact(t *testing.T) {
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
	issue85AssertState(t, mutation, eebusraw.MutationStateV1FailedNoContact)
	if mutation.NoContactEvidence == nil ||
		mutation.NoContactEvidence.RemoteFramesSent != 0 ||
		mutation.Error == nil ||
		mutation.Error.Code != eebusraw.ErrorCodeV1Disconnected {
		t.Fatalf("failed-no-contact evidence = %+v", mutation)
	}
	_, writes, _, exhausted := harness.executor.counts()
	if writes != 0 || exhausted != 0 {
		t.Fatalf("pre-send failure writes=%d exhausted=%d", writes, exhausted)
	}
}
