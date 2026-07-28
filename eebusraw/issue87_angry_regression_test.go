package eebusraw_test

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

func TestIssue87ReadRequestsRejectSecretBearingTargets(t *testing.T) {
	locator := issue87Target(eebusraw.OperationV1Read).Locator()
	locator.SHIPID = "Bearer target-secret"
	terminal := eebusraw.ValidateFeaturesGetRequestV1(
		eebusraw.FeaturesGetRequestV1{Target: locator},
	)
	if terminal == nil || terminal.Code != eebusraw.ErrorCodeV1SecretDetected {
		t.Fatalf("features.get secret terminal = %+v, want secret_detected", terminal)
	}

	target := issue87Target(eebusraw.OperationV1Read)
	target.DeviceAddress = "Bearer nested-secret"
	terminal = eebusraw.ValidateFeatureDataGetRequestV1(
		eebusraw.FeatureDataGetRequestV1{
			Targets: []eebusraw.FeatureTargetV1{target},
		},
	)
	if terminal == nil || terminal.Code != eebusraw.ErrorCodeV1SecretDetected {
		t.Fatalf("features.data.get secret terminal = %+v, want secret_detected", terminal)
	}
}

func TestIssue87ProtocolMessagesHaveClosedPortableShape(t *testing.T) {
	request, data := issue87ValidReadResult(t)
	tests := map[string]func(*eebusraw.ReadObservationV1){
		"unknown classifier": func(observation *eebusraw.ReadObservationV1) {
			observation.RawRequest.Classifier = "NOT_A_CLASSIFIER"
		},
		"correlation overflow": func(observation *eebusraw.ReadObservationV1) {
			observation.RawRequest.CorrelationKey = math.MaxUint64
			observation.RawResponse.CorrelationKey = math.MaxUint64
		},
		"error number overflow": func(observation *eebusraw.ReadObservationV1) {
			overflow := uint64(math.MaxUint64)
			observation.RawResponse.Data = nil
			observation.RawResponse.ErrorNumber = &overflow
		},
		"data and error contradiction": func(observation *eebusraw.ReadObservationV1) {
			number := uint64(1)
			observation.RawResponse.ErrorNumber = &number
		},
		"wrong read classifier": func(observation *eebusraw.ReadObservationV1) {
			observation.RawRequest.Classifier = "WRITE"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := data.Clone()
			mutate(&candidate.Results[0])
			issue87RehashObservation(t, &candidate.Results[0])
			if terminal := eebusraw.ValidateFeatureDataGetDataV1(
				request,
				candidate,
				nil,
			); terminal == nil {
				t.Fatal("malformed protocol message was accepted")
			}
		})
	}
}

func TestIssue87ConstraintsAreScalarBoundedAndUnique(t *testing.T) {
	request, data := issue87ValidFeaturesResult(t)
	scalar := issue87TypedValue(t, int64(7))
	data.Functions = []eebusraw.FunctionDescriptorV1{{
		Function:   "measurementListData",
		Changeable: eebusraw.ChangeabilityV1True,
		Constraints: eebusraw.ConstraintSetV1{
			Status:     eebusraw.ConstraintStatusV1Known,
			EnumValues: []eebusraw.TypedValueV1{scalar},
		},
	}}
	issue87RehashFeatures(t, &data)
	if terminal := eebusraw.ValidateFeaturesGetDataV1(request, data); terminal != nil {
		t.Fatalf("valid scalar constraints rejected: %+v", terminal)
	}

	array := issue87TypedValue(t, []any{int64(1)})
	overflow := uint64(math.MaxUint64)
	tests := map[string]func(*eebusraw.ConstraintSetV1){
		"duplicate enum": func(value *eebusraw.ConstraintSetV1) {
			value.EnumValues = append(value.EnumValues, scalar.Clone())
		},
		"non scalar enum": func(value *eebusraw.ConstraintSetV1) {
			value.EnumValues = []eebusraw.TypedValueV1{array}
		},
		"non scalar bound": func(value *eebusraw.ConstraintSetV1) {
			value.Minimum = &array
		},
		"cardinality overflow": func(value *eebusraw.ConstraintSetV1) {
			value.MinCardinality = &overflow
		},
		"too many rules": func(value *eebusraw.ConstraintSetV1) {
			value.CrossFieldRules = make([]string, 65)
			for index := range value.CrossFieldRules {
				value.CrossFieldRules[index] = "rule"
			}
		},
		"oversized rule": func(value *eebusraw.ConstraintSetV1) {
			value.CrossFieldRules = []string{strings.Repeat("r", 513)}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := data.Clone()
			mutate(&candidate.Functions[0].Constraints)
			issue87RehashFeatures(t, &candidate)
			if terminal := eebusraw.ValidateFeaturesGetDataV1(
				request,
				candidate,
			); terminal == nil {
				t.Fatal("malformed constraints were accepted")
			}
		})
	}
}

func TestIssue87PartialFailuresBindNestedIndexAndPublicBounds(t *testing.T) {
	request, data, terminal := issue87ValidPartialResult(t)
	if validation := eebusraw.ValidateFeatureDataGetDataV1(
		request,
		data,
		terminal,
	); validation != nil {
		t.Fatalf("valid partial result rejected: %+v", validation)
	}

	tests := map[string]func(*eebusraw.ReadFailureV1){
		"nested index mismatch": func(failure *eebusraw.ReadFailureV1) {
			index := uint64(0)
			failure.Error.Details.TargetIndex = &index
		},
		"nested partial result": func(failure *eebusraw.ReadFailureV1) {
			failure.Error.Code = eebusraw.ErrorCodeV1PartialResult
		},
		"oversized message": func(failure *eebusraw.ReadFailureV1) {
			failure.Error.Message = strings.Repeat("m", 513)
		},
		"oversized classification": func(failure *eebusraw.ReadFailureV1) {
			failure.Error.Details.Classification = strings.Repeat("c", 129)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := data.Clone()
			mutate(&candidate.Failures[0])
			if validation := eebusraw.ValidateFeatureDataGetDataV1(
				request,
				candidate,
				terminal,
			); validation == nil {
				t.Fatal("malformed partial failure was accepted")
			}
		})
	}
}

func TestIssue87MutationAuditAndVerifiedValuesAreClosed(t *testing.T) {
	applied := issue87AppliedMutation(t)
	if terminal := eebusraw.ValidateMutationV1(applied); terminal != nil {
		t.Fatalf("valid applied mutation rejected: %+v", terminal)
	}

	nullValue := issue87TypedValue(t, (*int)(nil))
	nullApplied := applied
	nullApplied.Requested = nullValue
	nullApplied.ObservedAfter = &nullValue
	nullHash, err := nullValue.ComputeHash()
	if err != nil {
		t.Fatal(err)
	}
	nullApplied.ApplyVerification.EqualValueHash = nullHash
	if terminal := eebusraw.ValidateMutationV1(nullApplied); terminal == nil {
		t.Fatal("verified canonical null was accepted")
	}

	oneStepApplied := applied
	oneStepApplied.Audit = []eebusraw.AuditTransitionV1{
		issue87AuditTransition(
			t,
			1,
			eebusraw.MutationStateV1Applied,
			applied.CreatedAt,
			nil,
		),
	}
	if terminal := eebusraw.ValidateMutationV1(oneStepApplied); terminal == nil {
		t.Fatal("applied was accepted as an initial audit state")
	}

	backwards := applied
	backwards.Audit = issue87AuditHistory(
		t,
		applied.CreatedAt,
		[]eebusraw.MutationStateV1{
			eebusraw.MutationStateV1Prepared,
			eebusraw.MutationStateV1DispatchIntent,
			eebusraw.MutationStateV1ReplyObserved,
			eebusraw.MutationStateV1VerifyPending,
			eebusraw.MutationStateV1Applied,
		},
		[]time.Duration{0, time.Second, 3 * time.Second, 2 * time.Second, 4 * time.Second},
	)
	backwards.UpdatedAt = applied.CreatedAt.Add(4 * time.Second)
	if terminal := eebusraw.ValidateMutationV1(backwards); terminal == nil {
		t.Fatal("decreasing audit chronology was accepted")
	}

	outside := applied
	outside.UpdatedAt = applied.CreatedAt
	outside.Audit[len(outside.Audit)-1].TransitionedAt = applied.CreatedAt.Add(time.Second)
	outside.Audit = issue87RehashAudit(t, outside.Audit)
	if terminal := eebusraw.ValidateMutationV1(outside); terminal == nil {
		t.Fatal("audit transition outside mutation interval was accepted")
	}
}

func TestIssue87RollbackBeforeAlwaysBindsTopLevelBefore(t *testing.T) {
	mutation := issue87AppliedMutation(t)
	mutation.State = eebusraw.MutationStateV1RollbackIntent
	mutation.Rollback = &eebusraw.RollbackV1{
		State:  eebusraw.MutationStateV1RollbackIntent,
		Before: issue87TypedValue(t, int64(999)),
	}
	mutation.Audit = append(
		mutation.Audit,
		issue87AuditTransition(
			t,
			uint64(len(mutation.Audit)+1),
			eebusraw.MutationStateV1RollbackIntent,
			mutation.UpdatedAt,
			&mutation.Audit[len(mutation.Audit)-1].TransitionHash,
		),
	)
	if terminal := eebusraw.ValidateMutationV1(mutation); terminal == nil {
		t.Fatal("substituted intermediate rollback before-image was accepted")
	}
}

func issue87ValidReadResult(
	t *testing.T,
) (eebusraw.FeatureDataGetRequestV1, eebusraw.FeatureDataGetDataV1) {
	t.Helper()
	now := time.Date(2026, 7, 28, 20, 0, 0, 0, time.UTC)
	request := eebusraw.FeatureDataGetRequestV1{
		Targets: []eebusraw.FeatureTargetV1{
			issue87Target(eebusraw.OperationV1Read),
		},
	}
	return request, eebusraw.FeatureDataGetDataV1{
		Results: []eebusraw.ReadObservationV1{
			issue87Observation(
				t,
				request.Targets[0],
				eebusraw.RuntimeBindingV1{
					RuntimeEpoch: 7, ConnectionGeneration: 3,
				},
				now,
			),
		},
		Complete: true,
	}
}

func issue87ValidFeaturesResult(
	t *testing.T,
) (eebusraw.FeaturesGetRequestV1, eebusraw.FeaturesGetDataV1) {
	t.Helper()
	now := time.Date(2026, 7, 28, 20, 0, 0, 0, time.UTC)
	target := issue87Target(eebusraw.OperationV1Read).Locator()
	request := eebusraw.FeaturesGetRequestV1{Target: target}
	data := eebusraw.FeaturesGetDataV1{
		Feature: target,
		Runtime: eebusraw.RuntimeBindingV1{
			RuntimeEpoch: 7, ConnectionGeneration: 3,
		},
		DataTimestamp: now,
		Source:        eebusraw.ObservationSourceV1Live,
	}
	issue87RehashFeatures(t, &data)
	return request, data
}

func issue87ValidPartialResult(
	t *testing.T,
) (
	eebusraw.FeatureDataGetRequestV1,
	eebusraw.FeatureDataGetDataV1,
	*eebusraw.ErrorV1,
) {
	t.Helper()
	request, complete := issue87ValidReadResult(t)
	second := issue87Target(eebusraw.OperationV1Read)
	second.FeatureAddress++
	request.Targets = append(request.Targets, second)
	index := uint64(1)
	failure := eebusraw.ReadFailureV1{
		TargetIndex: index,
		Target:      second,
		Error: *eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1RemoteError,
			"remote rejected read",
			false,
			eebusraw.SourceLayerV1Remote,
		),
	}
	failure.Error.Details = &eebusraw.ErrorDetailsV1{
		TargetIndex:    &index,
		Classification: "remote_reply",
	}
	data := eebusraw.FeatureDataGetDataV1{
		Results:  complete.Results,
		Failures: []eebusraw.ReadFailureV1{failure},
		Complete: false,
	}
	terminal := eebusraw.NewErrorV1(
		eebusraw.ErrorCodeV1PartialResult,
		"aggregate partial result",
		true,
		eebusraw.SourceLayerV1Runtime,
	)
	return request, data, terminal
}

func issue87AppliedMutation(t *testing.T) eebusraw.MutationV1 {
	t.Helper()
	now := time.Date(2026, 7, 28, 20, 0, 0, 0, time.UTC)
	before := issue87TypedValue(t, int64(1))
	requested := issue87TypedValue(t, int64(2))
	requestedHash, err := requested.ComputeHash()
	if err != nil {
		t.Fatal(err)
	}
	accepted := true
	mutation := eebusraw.MutationV1{
		MutationRef: issue87Reference(20),
		State:       eebusraw.MutationStateV1Applied,
		Mode:        eebusraw.ModeV1Apply,
		Target:      issue87Target(eebusraw.OperationV1Write),
		Runtime: eebusraw.RuntimeBindingV1{
			RuntimeEpoch: 7, ConnectionGeneration: 3,
		},
		Before:           before,
		Requested:        requested,
		ProtocolAccepted: &accepted,
		ObservedAfter:    &requested,
		CreatedAt:        now,
		UpdatedAt:        now,
		ApplyVerification: &eebusraw.ApplyVerificationV1{
			Relation:       "observed_after_equals_requested",
			Verified:       true,
			EqualValueHash: requestedHash,
			VerifiedAt:     now,
		},
	}
	mutation.Audit = issue87AuditHistory(
		t,
		now,
		[]eebusraw.MutationStateV1{
			eebusraw.MutationStateV1Prepared,
			eebusraw.MutationStateV1DispatchIntent,
			eebusraw.MutationStateV1ReplyObserved,
			eebusraw.MutationStateV1VerifyPending,
			eebusraw.MutationStateV1Applied,
		},
		make([]time.Duration, 5),
	)
	return mutation
}

func issue87AuditHistory(
	t *testing.T,
	start time.Time,
	states []eebusraw.MutationStateV1,
	offsets []time.Duration,
) []eebusraw.AuditTransitionV1 {
	t.Helper()
	audit := make([]eebusraw.AuditTransitionV1, 0, len(states))
	var previous *eebusraw.HashV1
	for index, state := range states {
		transition := issue87AuditTransition(
			t,
			uint64(index+1),
			state,
			start.Add(offsets[index]),
			previous,
		)
		audit = append(audit, transition)
		value := transition.TransitionHash
		previous = &value
	}
	return audit
}

func issue87AuditTransition(
	t *testing.T,
	sequence uint64,
	state eebusraw.MutationStateV1,
	at time.Time,
	previous *eebusraw.HashV1,
) eebusraw.AuditTransitionV1 {
	t.Helper()
	transition := eebusraw.AuditTransitionV1{
		Sequence: sequence, State: state, TransitionedAt: at,
		PreviousHash: previous,
	}
	transition.TransitionHash = issue87TransitionHash(t, transition)
	return transition
}

func issue87RehashAudit(
	t *testing.T,
	audit []eebusraw.AuditTransitionV1,
) []eebusraw.AuditTransitionV1 {
	t.Helper()
	var previous *eebusraw.HashV1
	for index := range audit {
		audit[index].PreviousHash = previous
		audit[index].TransitionHash = issue87TransitionHash(t, audit[index])
		value := audit[index].TransitionHash
		previous = &value
	}
	return audit
}

func issue87RehashObservation(
	t *testing.T,
	observation *eebusraw.ReadObservationV1,
) {
	t.Helper()
	hash, err := observation.ComputeDataHash()
	if err != nil {
		t.Fatal(err)
	}
	observation.DataHash = hash
}

func issue87RehashFeatures(t *testing.T, data *eebusraw.FeaturesGetDataV1) {
	t.Helper()
	hash, err := data.ComputeDataHash()
	if err != nil {
		t.Fatal(err)
	}
	data.DataHash = hash
}
