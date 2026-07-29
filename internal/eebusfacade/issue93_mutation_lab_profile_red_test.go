package eebusfacade

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusmutation"
)

func TestIssue93ProductionPolicyAttestsOneExactCurrentLabProfile(t *testing.T) {
	fixture := newIssue83RawBridgeFixture(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	fixture.bridge.now = func() time.Time { return now }
	before := issue93FacadeValue(t, 20)
	requested := issue93FacadeValue(t, 21)
	profile := issue93FacadeProfile(t, fixture, before, requested, now.Add(time.Hour))
	fixture.bridge.mutationLabProfiles = []eebusmutation.LabProfile{profile}
	request := issue93FacadeSetRequest(profile, requested, now.Add(30*time.Minute))

	decision, terminal := fixture.bridge.MutationPolicy(
		context.Background(),
		request,
		before,
	)
	if terminal != nil {
		t.Fatalf("MutationPolicy(exact) = %+v", terminal)
	}
	if !decision.FullWrite ||
		decision.Changeability != eebusraw.ChangeabilityV1True ||
		decision.ConstraintsKnown ||
		!decision.LabAllowlisted ||
		!decision.RollbackRepresentable ||
		decision.LabProfileID != profile.ProfileID ||
		!reflect.DeepEqual(decision.EvidenceHashes, profile.EvidenceHashes) ||
		!reflect.DeepEqual(decision.SafetyPredicates, profile.SafetyPredicates) ||
		len(decision.ConstraintFailures) != 0 ||
		len(decision.SafetyFailures) != 0 {
		t.Fatalf("exact profile policy decision = %+v", decision)
	}
	if fixture.sender.calls.Load() != 0 {
		t.Fatalf("policy attestation contacted remote %d times", fixture.sender.calls.Load())
	}
}

func TestIssue93ProductionCompositionBindsIndependentProfileCopies(t *testing.T) {
	var source RuntimeLabProfile
	var coordinatorProfiles []eebusmutation.LabProfile
	var policyBridge *rawFeatureRuntimeBridge
	harness := newMSP045ProductHarness(t, func(setup *msp045ProductSetup) {
		target := eebusraw.FeatureTargetV1{
			RemoteSKI:      setup.remoteSKI,
			SHIPID:         "issue93-composition-ship",
			DeviceAddress:  "issue93-composition-device",
			EntityAddress:  []uint64{1},
			FeatureAddress: 7,
			FeatureType:    "measurement",
			FeatureRole:    eebusraw.FeatureRoleV1Server,
			Function:       "measurementListData",
			Operation:      eebusraw.OperationV1Write,
		}
		before := issue93FacadeValue(t, 20)
		requested := issue93FacadeValue(t, 21)
		beforeHash, err := before.ComputeHash()
		if err != nil {
			t.Fatal(err)
		}
		requestedHash, err := requested.ComputeHash()
		if err != nil {
			t.Fatal(err)
		}
		source = RuntimeLabProfile{
			Contract:               eebusmutation.LabProfileContract,
			ProfileID:              "issue93-composition-profile",
			Target:                 target,
			AllowedValueHashes:     []eebusraw.HashV1{requestedHash},
			RollbackValueHash:      beforeHash,
			MaximumProbeTTLSeconds: 60,
			SafetyPredicates: []string{
				"exact-target-capability-current",
				"rollback-representable",
			},
			EvidenceHashes: []eebusraw.HashV1{
				"sha256:3333333333333333333333333333333333333333333333333333333333333333",
			},
			ExpiresAt: time.Unix(1_900_000_000, 0).UTC(),
		}
		setup.configureRuntime = func(config *RuntimeConfig) {
			config.LabProfiles = []RuntimeLabProfile{source}
		}
		setup.configureDependencies = func(dependencies *runtimeDependencies) {
			productionFactory := dependencies.newMutationCoordinator
			dependencies.newMutationCoordinator = func(
				config eebusmutation.CoordinatorConfig,
				deps eebusmutation.CoordinatorDependencies,
			) (*eebusmutation.Coordinator, *eebusraw.ErrorV1) {
				coordinatorProfiles = cloneRuntimeMutationLabProfiles(config.LabProfiles)
				policyBridge, _ = deps.Policy.(*rawFeatureRuntimeBridge)
				return productionFactory(config, deps)
			}
		}
	})
	if harness.backend.rawMutations == nil || policyBridge == nil {
		t.Fatal("production composition omitted mutation coordinator or policy bridge")
	}
	source.Target.EntityAddress[0] = 99
	source.AllowedValueHashes[0] =
		"sha256:4444444444444444444444444444444444444444444444444444444444444444"
	source.SafetyPredicates[0] = "changed"
	source.EvidenceHashes[0] =
		"sha256:5555555555555555555555555555555555555555555555555555555555555555"

	if len(coordinatorProfiles) != 1 || len(policyBridge.mutationLabProfiles) != 1 {
		t.Fatalf(
			"production profile counts coordinator=%d policy=%d",
			len(coordinatorProfiles),
			len(policyBridge.mutationLabProfiles),
		)
	}
	for owner, profile := range map[string]eebusmutation.LabProfile{
		"coordinator": coordinatorProfiles[0],
		"policy":      policyBridge.mutationLabProfiles[0],
	} {
		if profile.Target.EntityAddress[0] != 1 ||
			profile.AllowedValueHashes[0] == source.AllowedValueHashes[0] ||
			profile.SafetyPredicates[0] == source.SafetyPredicates[0] ||
			profile.EvidenceHashes[0] == source.EvidenceHashes[0] {
			t.Fatalf("%s retained mutable profile input: %+v", owner, profile)
		}
	}
}

func TestIssue93ProductionPolicyWithoutProfilesRemainsFailClosed(t *testing.T) {
	fixture := newIssue83RawBridgeFixture(t)
	before := issue93FacadeValue(t, 20)
	requested := issue93FacadeValue(t, 21)
	target := issue83TargetFromLocator(fixture.locators[0])
	target.Operation = eebusraw.OperationV1Write

	decision, terminal := fixture.bridge.MutationPolicy(
		context.Background(),
		eebusraw.FeatureDataSetRequestV1{
			Target: target,
			Value:  requested,
			Mode:   eebusraw.ModeV1Apply,
		},
		before,
	)
	if terminal != nil {
		t.Fatalf("MutationPolicy(no profiles) = %+v", terminal)
	}
	if !decision.FullWrite ||
		decision.Changeability != eebusraw.ChangeabilityV1Unknown ||
		decision.ConstraintsKnown ||
		decision.LabAllowlisted {
		t.Fatalf("no-profile policy decision = %+v", decision)
	}
	if fixture.sender.calls.Load() != 0 {
		t.Fatalf("no-profile policy contacted remote %d times", fixture.sender.calls.Load())
	}
}

func TestIssue93ProductionPolicyFailsClosedForInexactOrExpiredProfiles(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*eebusmutation.LabProfile, *eebusraw.FeatureDataSetRequestV1, *eebusraw.TypedValueV1)
	}{
		{name: "target", mutate: func(profile *eebusmutation.LabProfile, _ *eebusraw.FeatureDataSetRequestV1, _ *eebusraw.TypedValueV1) {
			profile.Target.FeatureAddress++
		}},
		{name: "requested hash", mutate: func(profile *eebusmutation.LabProfile, _ *eebusraw.FeatureDataSetRequestV1, _ *eebusraw.TypedValueV1) {
			profile.AllowedValueHashes[0] = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
		}},
		{name: "rollback hash", mutate: func(profile *eebusmutation.LabProfile, _ *eebusraw.FeatureDataSetRequestV1, _ *eebusraw.TypedValueV1) {
			profile.RollbackValueHash = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
		}},
		{name: "ttl", mutate: func(_ *eebusmutation.LabProfile, request *eebusraw.FeatureDataSetRequestV1, _ *eebusraw.TypedValueV1) {
			request.Mode = eebusraw.ModeV1Probe
			request.ProbeTTLSeconds = 61
		}},
		{name: "expiry", mutate: func(profile *eebusmutation.LabProfile, _ *eebusraw.FeatureDataSetRequestV1, _ *eebusraw.TypedValueV1) {
			profile.ExpiresAt = time.Unix(1_799_999_999, 0).UTC()
		}},
		{name: "override expiry widening", mutate: func(_ *eebusmutation.LabProfile, request *eebusraw.FeatureDataSetRequestV1, _ *eebusraw.TypedValueV1) {
			request.ConstraintsOverride.ExpiresAt = time.Unix(1_800_004_000, 0).UTC()
		}},
		{name: "unsupported safety predicate", mutate: func(profile *eebusmutation.LabProfile, _ *eebusraw.FeatureDataSetRequestV1, _ *eebusraw.TypedValueV1) {
			profile.SafetyPredicates[0] = "unimplemented-safety-predicate"
		}},
		{name: "duplicate exact profile", mutate: func(_ *eebusmutation.LabProfile, _ *eebusraw.FeatureDataSetRequestV1, _ *eebusraw.TypedValueV1) {}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newIssue83RawBridgeFixture(t)
			now := time.Unix(1_800_000_000, 0).UTC()
			fixture.bridge.now = func() time.Time { return now }
			before := issue93FacadeValue(t, 20)
			requested := issue93FacadeValue(t, 21)
			profile := issue93FacadeProfile(t, fixture, before, requested, now.Add(time.Hour))
			request := issue93FacadeSetRequest(profile, requested, now.Add(30*time.Minute))
			test.mutate(&profile, &request, &before)
			fixture.bridge.mutationLabProfiles = []eebusmutation.LabProfile{profile}
			if test.name == "duplicate exact profile" {
				fixture.bridge.mutationLabProfiles = append(
					fixture.bridge.mutationLabProfiles,
					profile,
				)
			}

			decision, terminal := fixture.bridge.MutationPolicy(
				context.Background(),
				request,
				before,
			)
			if terminal != nil {
				t.Fatalf("MutationPolicy(inexact) = %+v", terminal)
			}
			if decision.Changeability != eebusraw.ChangeabilityV1Unknown ||
				decision.ConstraintsKnown ||
				decision.LabAllowlisted ||
				decision.LabProfileID != "" {
				t.Fatalf("inexact profile policy decision = %+v", decision)
			}
			if fixture.sender.calls.Load() != 0 {
				t.Fatalf("inexact profile policy contacted remote %d times", fixture.sender.calls.Load())
			}
		})
	}
}

func issue93FacadeProfile(
	t *testing.T,
	fixture issue83RawBridgeFixture,
	before eebusraw.TypedValueV1,
	requested eebusraw.TypedValueV1,
	expiresAt time.Time,
) eebusmutation.LabProfile {
	t.Helper()
	beforeHash, err := before.ComputeHash()
	if err != nil {
		t.Fatal(err)
	}
	requestedHash, err := requested.ComputeHash()
	if err != nil {
		t.Fatal(err)
	}
	target := issue83TargetFromLocator(fixture.locators[0])
	target.Operation = eebusraw.OperationV1Write
	return eebusmutation.LabProfile{
		Contract:               eebusmutation.LabProfileContract,
		ProfileID:              "issue93-facade-profile",
		Target:                 target,
		AllowedValueHashes:     []eebusraw.HashV1{requestedHash},
		RollbackValueHash:      beforeHash,
		MaximumProbeTTLSeconds: 60,
		SafetyPredicates: []string{
			"exact-target-capability-current",
			"rollback-representable",
		},
		EvidenceHashes: []eebusraw.HashV1{
			"sha256:3333333333333333333333333333333333333333333333333333333333333333",
		},
		ExpiresAt: expiresAt,
	}
}

func issue93FacadeSetRequest(
	profile eebusmutation.LabProfile,
	requested eebusraw.TypedValueV1,
	overrideExpiry time.Time,
) eebusraw.FeatureDataSetRequestV1 {
	return eebusraw.FeatureDataSetRequestV1{
		Target:         profile.Target.Clone(),
		Value:          requested.Clone(),
		ReadToken:      "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		IdempotencyKey: "issue93-facade-request",
		Mode:           eebusraw.ModeV1Apply,
		ConstraintsOverride: &eebusraw.ConstraintOverrideV1{
			ProfileID:     profile.ProfileID,
			Justification: "bounded production lab proof",
			ExpiresAt:     overrideExpiry,
		},
	}
}

func issue93FacadeValue(t *testing.T, value int64) eebusraw.TypedValueV1 {
	t.Helper()
	typed, err := eebusraw.NewTypedValueV1(value)
	if err != nil {
		t.Fatal(err)
	}
	return typed
}
