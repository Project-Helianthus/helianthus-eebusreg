package eebusfacade

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	executor "github.com/Project-Helianthus/helianthus-eebus-go/features/executor"
	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusmutation"
	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
)

func TestRawMutationDispatchDispositionClassificationPreservesNestedErrors(t *testing.T) {
	cause := errors.New("transport cause")
	tests := []struct {
		name        string
		disposition *spineapi.DispatchDisposition
		wantSent    bool
	}{
		{
			name:        "explicit no transport handoff",
			disposition: rawMutationDisposition(spineapi.NoTransportHandoff),
			wantSent:    false,
		},
		{
			name:        "transport handoff possible",
			disposition: rawMutationDisposition(spineapi.TransportHandoffPossible),
			wantSent:    true,
		},
		{
			name:        "zero disposition",
			disposition: rawMutationDisposition(0),
			wantSent:    true,
		},
		{
			name:        "unknown disposition",
			disposition: rawMutationDisposition(spineapi.DispatchDisposition(0xff)),
			wantSent:    true,
		},
		{
			name:     "untyped error",
			wantSent: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newIssue83RawBridgeFixture(t)
			target := issue83TargetFromLocator(fixture.locators[0])
			binding, request, err := issue83ExactBindingAndRequest(fixture.bridge, target)
			if err != nil {
				t.Fatal(err)
			}
			nested := fmt.Errorf("nested cause: %w", cause)
			dispatchCause := error(nested)
			var wantRoundTrip *spineapi.CorrelatedRoundTripError
			if test.disposition != nil {
				wantRoundTrip = &spineapi.CorrelatedRoundTripError{
					Cause:       nested,
					Disposition: *test.disposition,
				}
				dispatchCause = wantRoundTrip
			}
			fixture.sender.roundTrip = func(
				context.Context,
				spineapi.CorrelatedRequest,
			) (spineapi.CorrelatedResponse, error) {
				return spineapi.CorrelatedResponse{}, fmt.Errorf(
					"sender dispatch: %w",
					dispatchCause,
				)
			}
			_, err = fixture.bridge.RoundTripIfCurrent(
				context.Background(),
				binding,
				request,
			)

			if got := mutationFrameSent(executor.ExactFeatureResult{}, err); got != test.wantSent {
				t.Fatalf("mutationFrameSent() = %t, want %t", got, test.wantSent)
			}
			if !errors.Is(err, cause) {
				t.Fatalf("dispatch error no longer preserves errors.Is for %v", cause)
			}
			var gotDispatch *rawDispatchError
			if !errors.As(err, &gotDispatch) || gotDispatch != err {
				t.Fatalf("dispatch error no longer preserves errors.As: %#v", gotDispatch)
			}
			var gotRoundTrip *spineapi.CorrelatedRoundTripError
			if got := errors.As(err, &gotRoundTrip); got != (test.disposition != nil) {
				t.Fatalf("CorrelatedRoundTripError errors.As = %t", got)
			}
			if test.disposition != nil &&
				(gotRoundTrip != wantRoundTrip || gotRoundTrip.Disposition != *test.disposition) {
				t.Fatalf("nested disposition = %#v, want %d", gotRoundTrip, *test.disposition)
			}
			if calls := fixture.sender.calls.Load(); calls != 1 {
				t.Fatalf("RoundTripIfCurrent() calls = %d, want one", calls)
			}
		})
	}
}

func TestRawMutationConcurrentRetirementPreservesRoundTripErrorChain(t *testing.T) {
	tests := []struct {
		name        string
		disposition spineapi.DispatchDisposition
		wantSent    bool
	}{
		{
			name:        "explicit no transport handoff",
			disposition: spineapi.NoTransportHandoff,
			wantSent:    false,
		},
		{
			name:        "transport handoff possible",
			disposition: spineapi.TransportHandoffPossible,
			wantSent:    true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newIssue83RawBridgeFixture(t)
			target := issue83TargetFromLocator(fixture.locators[0])
			binding, request, err := issue83ExactBindingAndRequest(fixture.bridge, target)
			if err != nil {
				t.Fatal(err)
			}
			cause := errors.New("original correlated round-trip cause")
			roundTripErr := &spineapi.CorrelatedRoundTripError{
				Cause:       fmt.Errorf("sender failure: %w", cause),
				Disposition: test.disposition,
			}
			started := make(chan struct{})
			fixture.sender.roundTrip = func(
				ctx context.Context,
				_ spineapi.CorrelatedRequest,
			) (spineapi.CorrelatedResponse, error) {
				close(started)
				<-ctx.Done()
				return spineapi.CorrelatedResponse{}, roundTripErr
			}
			done := make(chan error, 1)
			go func() {
				_, dispatchErr := fixture.bridge.RoundTripIfCurrent(
					context.Background(),
					binding,
					request,
				)
				done <- dispatchErr
			}()

			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("round trip did not start")
			}
			fixture.bridge.retireRemote(fixture.remoteSKI, 4)

			var dispatchErr error
			select {
			case dispatchErr = <-done:
			case <-time.After(time.Second):
				t.Fatal("concurrent retirement did not release the round trip")
			}
			if !errors.Is(dispatchErr, executor.ErrExactRemoteBindingMismatch) {
				t.Fatalf("dispatch error lost generation mismatch: %v", dispatchErr)
			}
			if !errors.Is(dispatchErr, cause) {
				t.Fatalf("dispatch error lost original cause: %v", dispatchErr)
			}
			var gotRoundTrip *spineapi.CorrelatedRoundTripError
			if !errors.As(dispatchErr, &gotRoundTrip) || gotRoundTrip != roundTripErr {
				t.Fatalf("dispatch error lost correlated error: %#v", gotRoundTrip)
			}
			var bindingErr *executor.ExactRemoteBindingError
			if !errors.As(dispatchErr, &bindingErr) ||
				bindingErr.Failure != executor.ExactRemoteBindingGenerationMismatch {
				t.Fatalf("dispatch binding error = %#v", bindingErr)
			}
			if got := mutationFrameSent(executor.ExactFeatureResult{}, dispatchErr); got != test.wantSent {
				t.Fatalf("mutationFrameSent() = %t, want %t", got, test.wantSent)
			}
			if calls := fixture.sender.calls.Load(); calls != 1 {
				t.Fatalf("concurrent retirement round trips = %d, want one", calls)
			}
		})
	}
}

func TestRawMutationFullWriteConcurrentRetirementUsesTypedDispositionOnce(t *testing.T) {
	tests := []struct {
		name        string
		disposition spineapi.DispatchDisposition
		wantSent    bool
	}{
		{
			name:        "explicit no transport handoff",
			disposition: spineapi.NoTransportHandoff,
			wantSent:    false,
		},
		{
			name:        "transport handoff possible",
			disposition: spineapi.TransportHandoffPossible,
			wantSent:    true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newIssue83RawBridgeFixture(t)
			target := issue83TargetFromLocator(fixture.locators[0])
			target.Operation = eebusraw.OperationV1Write
			binding, terminal := fixture.bridge.CurrentRuntimeBinding(target)
			if terminal != nil {
				t.Fatalf("CurrentRuntimeBinding() error = %+v", terminal)
			}
			value, err := rawMutationMeasurementValue(215)
			if err != nil {
				t.Fatal(err)
			}
			started := make(chan struct{})
			fixture.sender.roundTrip = func(
				ctx context.Context,
				_ spineapi.CorrelatedRequest,
			) (spineapi.CorrelatedResponse, error) {
				close(started)
				<-ctx.Done()
				return spineapi.CorrelatedResponse{}, &spineapi.CorrelatedRoundTripError{
					Cause:       fmt.Errorf("retired write: %w", context.Canceled),
					Disposition: test.disposition,
				}
			}
			type writeOutcome struct {
				result   eebusmutation.WriteResult
				terminal *eebusraw.ErrorV1
			}
			done := make(chan writeOutcome, 1)
			go func() {
				result, writeTerminal := fixture.bridge.FullWriteIfCurrent(
					context.Background(),
					target,
					value,
					binding,
				)
				done <- writeOutcome{result: result, terminal: writeTerminal}
			}()

			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("WRITE round trip did not start")
			}
			fixture.bridge.retireRemote(fixture.remoteSKI, 4)

			var outcome writeOutcome
			select {
			case outcome = <-done:
			case <-time.After(time.Second):
				t.Fatal("concurrent retirement did not release FullWriteIfCurrent")
			}
			if outcome.terminal == nil ||
				outcome.terminal.Code != eebusraw.ErrorCodeV1ConnectionGenerationMismatch {
				t.Fatalf(
					"FullWriteIfCurrent() error = %+v, want connection_generation_mismatch",
					outcome.terminal,
				)
			}
			if outcome.result.FrameSent != test.wantSent ||
				outcome.result.Correlated ||
				outcome.result.Accepted {
				t.Fatalf(
					"FullWriteIfCurrent() result = %+v, want FrameSent=%t",
					outcome.result,
					test.wantSent,
				)
			}
			if calls := fixture.sender.calls.Load(); calls != 1 {
				t.Fatalf("FullWriteIfCurrent() round trips = %d, want one with no blind retry", calls)
			}
		})
	}
}

func TestRawMutationOriginalAndRollbackShareConservativeOneWriteClassification(t *testing.T) {
	tests := []struct {
		name        string
		disposition *spineapi.DispatchDisposition
		wantSent    bool
	}{
		{
			name:        "explicit no transport handoff",
			disposition: rawMutationDisposition(spineapi.NoTransportHandoff),
			wantSent:    false,
		},
		{
			name:        "transport handoff possible",
			disposition: rawMutationDisposition(spineapi.TransportHandoffPossible),
			wantSent:    true,
		},
		{
			name:        "zero disposition",
			disposition: rawMutationDisposition(0),
			wantSent:    true,
		},
		{
			name:        "unknown disposition",
			disposition: rawMutationDisposition(spineapi.DispatchDisposition(0xff)),
			wantSent:    true,
		},
		{
			name:     "untyped error",
			wantSent: true,
		},
	}
	phases := []struct {
		name  string
		value int64
	}{
		{name: "original", value: 215},
		{name: "rollback", value: 185},
	}

	for _, test := range tests {
		for _, phase := range phases {
			t.Run(test.name+"/"+phase.name, func(t *testing.T) {
				fixture := newIssue83RawBridgeFixture(t)
				target := issue83TargetFromLocator(fixture.locators[0])
				target.Operation = eebusraw.OperationV1Write
				binding, terminal := fixture.bridge.CurrentRuntimeBinding(target)
				if terminal != nil {
					t.Fatalf("CurrentRuntimeBinding() error = %+v", terminal)
				}
				value, err := rawMutationMeasurementValue(phase.value)
				if err != nil {
					t.Fatal(err)
				}
				fixture.sender.roundTrip = func(
					context.Context,
					spineapi.CorrelatedRequest,
				) (spineapi.CorrelatedResponse, error) {
					cause := error(context.Canceled)
					if test.disposition != nil {
						cause = &spineapi.CorrelatedRoundTripError{
							Cause:       fmt.Errorf("nested transport: %w", context.Canceled),
							Disposition: *test.disposition,
						}
					}
					return spineapi.CorrelatedResponse{}, cause
				}

				result, terminal := fixture.bridge.FullWriteIfCurrent(
					context.Background(),
					target,
					value,
					binding,
				)
				if terminal == nil || terminal.Code != eebusraw.ErrorCodeV1Cancelled {
					t.Fatalf("FullWriteIfCurrent() error = %+v, want cancelled", terminal)
				}
				if result.FrameSent != test.wantSent || result.Correlated || result.Accepted {
					t.Fatalf("FullWriteIfCurrent() result = %+v, want FrameSent=%t", result, test.wantSent)
				}
				if calls := fixture.sender.calls.Load(); calls != 1 {
					t.Fatalf("FullWriteIfCurrent() round trips = %d, want one with no blind retry", calls)
				}
			})
		}
	}
}

func rawMutationDisposition(value spineapi.DispatchDisposition) *spineapi.DispatchDisposition {
	return &value
}

func rawMutationMeasurementValue(number int64) (eebusraw.TypedValueV1, error) {
	return eebusraw.NewTypedValueV1(map[string]any{
		"measurementData": []any{
			map[string]any{
				"measurementId": int64(11),
				"value": map[string]any{
					"number": number,
					"scale":  int64(-1),
				},
			},
		},
	})
}
