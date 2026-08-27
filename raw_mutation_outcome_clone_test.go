package eebusruntime

import (
	"context"
	"testing"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

type issue95OutcomeRuntime struct{}

var _ RawMutationRuntimeV1 = (*issue95OutcomeRuntime)(nil)

func (*issue95OutcomeRuntime) FeaturesDataSet(
	context.Context,
	eebusraw.WriteAuthorizationV1,
	eebusraw.FeatureDataSetRequestV1,
) (RawMutationOutcomeV1, *eebusraw.ErrorV1) {
	return RawMutationOutcomeV1{}, nil
}

func (*issue95OutcomeRuntime) MutationsGet(
	context.Context,
	eebusraw.ReadAuthorizationV1,
	eebusraw.MutationGetRequestV1,
) (RawMutationOutcomeV1, *eebusraw.ErrorV1) {
	return RawMutationOutcomeV1{}, nil
}

func (*issue95OutcomeRuntime) MutationsRollback(
	context.Context,
	eebusraw.WriteAuthorizationV1,
	eebusraw.MutationRollbackRequestV1,
) (RawMutationOutcomeV1, *eebusraw.ErrorV1) {
	return RawMutationOutcomeV1{}, nil
}

func TestRawMutationOutcomeCloneDetachesRuntimeAndMutation(t *testing.T) {
	before, err := eebusraw.NewTypedValueV1(map[string]any{
		"limit": int64(23),
		"unit":  "degC",
	})
	if err != nil {
		t.Fatal(err)
	}
	requested, err := eebusraw.NewTypedValueV1(map[string]any{
		"limit": int64(22),
		"unit":  "degC",
	})
	if err != nil {
		t.Fatal(err)
	}
	binding := eebusraw.RuntimeBindingV1{
		RuntimeEpoch:         7,
		ConnectionGeneration: 3,
	}
	accepted := true
	previousHash := eebusraw.HashV1(
		"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	)
	expectedPreviousHash := previousHash
	outcome := RawMutationOutcomeV1{
		Mutation: eebusraw.MutationV1{
			Runtime: binding,
			Target: eebusraw.FeatureTargetV1{
				EntityAddress: []uint64{4, 1, 1},
			},
			Before:           before,
			Requested:        requested,
			ProtocolAccepted: &accepted,
			Rollback: &eebusraw.RollbackV1{
				State:  eebusraw.MutationStateV1RollbackIntent,
				Before: before,
			},
			Audit: []eebusraw.AuditTransitionV1{{
				Sequence:     1,
				PreviousHash: &previousHash,
			}},
		},
		Runtime: &binding,
	}
	cloned := outcome.Clone()
	outcome.Runtime.ConnectionGeneration = 9
	outcome.Mutation.Runtime.ConnectionGeneration = 9
	outcome.Mutation.Target.EntityAddress[0] = 99
	*outcome.Mutation.ProtocolAccepted = false
	outcome.Mutation.Rollback.Before = requested
	*outcome.Mutation.Audit[0].PreviousHash =
		"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	if cloned.Runtime == nil ||
		cloned.Runtime.ConnectionGeneration != 3 ||
		cloned.Mutation.Runtime.ConnectionGeneration != 3 ||
		cloned.Mutation.Target.EntityAddress[0] != 4 ||
		cloned.Mutation.ProtocolAccepted == nil ||
		!*cloned.Mutation.ProtocolAccepted ||
		cloned.Mutation.Rollback == nil ||
		cloned.Mutation.Audit[0].PreviousHash == nil ||
		*cloned.Mutation.Audit[0].PreviousHash != expectedPreviousHash ||
		cloned.Runtime == outcome.Runtime ||
		cloned.Mutation.ProtocolAccepted == outcome.Mutation.ProtocolAccepted ||
		cloned.Mutation.Rollback == outcome.Mutation.Rollback ||
		cloned.Mutation.Audit[0].PreviousHash ==
			outcome.Mutation.Audit[0].PreviousHash {
		t.Fatalf("Clone() aliased the mutation outcome: %+v", cloned)
	}
	clonedBeforeHash, err := cloned.Mutation.Rollback.Before.ComputeHash()
	if err != nil {
		t.Fatal(err)
	}
	beforeHash, err := before.ComputeHash()
	if err != nil {
		t.Fatal(err)
	}
	if clonedBeforeHash != beforeHash {
		t.Fatalf("Clone() changed rollback before image: got %s want %s", clonedBeforeHash, beforeHash)
	}
}
