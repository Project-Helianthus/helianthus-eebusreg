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

func TestIssue95MutationOutcomeClonesRuntimeAndMutation(t *testing.T) {
	binding := eebusraw.RuntimeBindingV1{
		RuntimeEpoch:         7,
		ConnectionGeneration: 3,
	}
	outcome := RawMutationOutcomeV1{
		Mutation: eebusraw.MutationV1{Runtime: binding},
		Runtime:  &binding,
	}
	cloned := outcome.Clone()
	outcome.Runtime.ConnectionGeneration = 9
	outcome.Mutation.Runtime.ConnectionGeneration = 9

	if cloned.Runtime == nil ||
		cloned.Runtime.ConnectionGeneration != 3 ||
		cloned.Mutation.Runtime.ConnectionGeneration != 3 {
		t.Fatalf("Clone() aliased the mutation outcome: %+v", cloned)
	}
}
