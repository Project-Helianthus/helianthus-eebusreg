package eebusraw_test

import (
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

func TestIssue105FeatureDataValidatorRejectsMalformedTypedEmptyErrors(t *testing.T) {
	now := time.Date(2026, 7, 30, 19, 15, 0, 0, time.UTC)
	binding := eebusraw.RuntimeBindingV1{
		RuntimeEpoch:         7,
		ConnectionGeneration: 3,
	}
	request := eebusraw.FeatureDataGetRequestV1{
		Targets: []eebusraw.FeatureTargetV1{
			issue87Target(eebusraw.OperationV1Read),
			issue87Target(eebusraw.OperationV1Read),
		},
	}
	request.Targets[1].FeatureAddress = 3

	data := eebusraw.FeatureDataGetDataV1{
		Results: []eebusraw.ReadObservationV1{
			issue87Observation(t, request.Targets[0], binding, now),
		},
		Failures: []eebusraw.ReadFailureV1{{
			TargetIndex: 1,
			Target:      request.Targets[1],
			Error: *eebusraw.NewErrorV1(
				eebusraw.ErrorCodeV1TypedEmpty,
				"valid typed-empty reply",
				false,
				eebusraw.SourceLayerV1Remote,
			),
		}},
		Complete: false,
	}
	terminal := eebusraw.NewErrorV1(
		eebusraw.ErrorCodeV1PartialResult,
		"fixed partial result",
		false,
		eebusraw.SourceLayerV1Runtime,
	)

	if validation := eebusraw.ValidateFeatureDataGetDataV1(request, data, terminal); validation != nil {
		t.Fatalf("valid typed_empty failure rejected: %+v", validation)
	}

	tests := []struct {
		name   string
		mutate func(*eebusraw.ErrorV1)
	}{
		{
			name: "retriable",
			mutate: func(err *eebusraw.ErrorV1) {
				err.Retriable = true
			},
		},
		{
			name: "non-remote source",
			mutate: func(err *eebusraw.ErrorV1) {
				err.SourceLayer = eebusraw.SourceLayerV1Decode
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := data.Clone()
			test.mutate(&candidate.Failures[0].Error)

			if validation := eebusraw.ValidateFeatureDataGetDataV1(request, candidate, terminal); validation == nil {
				t.Fatal("malformed typed_empty error unexpectedly validated")
			}
		})
	}
}
