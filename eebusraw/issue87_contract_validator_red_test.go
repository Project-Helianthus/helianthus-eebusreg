package eebusraw_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

func TestIssue87RequestValidatorsEnforceClosedContract(t *testing.T) {
	now := time.Date(2026, 7, 28, 18, 30, 0, 0, time.UTC)
	value := issue87TypedValue(t, int64(21))
	validSet := eebusraw.FeatureDataSetRequestV1{
		Target:         issue87Target(eebusraw.OperationV1Write),
		Value:          value,
		ReadToken:      strings.Repeat("A", 43),
		IdempotencyKey: "issue87-key-0001",
		Mode:           eebusraw.ModeV1Apply,
	}
	validGet := eebusraw.MutationGetRequestV1{
		MutationRef: issue87Reference(1),
	}
	validRollback := eebusraw.MutationRollbackRequestV1{
		MutationRef:    issue87Reference(2),
		IdempotencyKey: "issue87-key-0002",
	}

	if terminal := eebusraw.ValidateFeatureDataSetRequestV1(validSet); terminal != nil {
		t.Fatalf("valid SET request rejected: %+v", terminal)
	}
	if terminal := eebusraw.ValidateMutationGetRequestV1(validGet); terminal != nil {
		t.Fatalf("valid mutation GET rejected: %+v", terminal)
	}
	if terminal := eebusraw.ValidateMutationRollbackRequestV1(validRollback); terminal != nil {
		t.Fatalf("valid rollback rejected: %+v", terminal)
	}

	tests := []struct {
		name string
		call func() *eebusraw.ErrorV1
		code eebusraw.ErrorCodeV1
	}{
		{
			name: "read target on set",
			call: func() *eebusraw.ErrorV1 {
				request := validSet
				request.Target.Operation = eebusraw.OperationV1Read
				return eebusraw.ValidateFeatureDataSetRequestV1(request)
			},
			code: eebusraw.ErrorCodeV1UnsupportedOperation,
		},
		{
			name: "short read token",
			call: func() *eebusraw.ErrorV1 {
				request := validSet
				request.ReadToken = "short"
				return eebusraw.ValidateFeatureDataSetRequestV1(request)
			},
			code: eebusraw.ErrorCodeV1InvalidArgument,
		},
		{
			name: "short idempotency key",
			call: func() *eebusraw.ErrorV1 {
				request := validSet
				request.IdempotencyKey = "short"
				return eebusraw.ValidateFeatureDataSetRequestV1(request)
			},
			code: eebusraw.ErrorCodeV1InvalidArgument,
		},
		{
			name: "unknown mode",
			call: func() *eebusraw.ErrorV1 {
				request := validSet
				request.Mode = "unknown"
				return eebusraw.ValidateFeatureDataSetRequestV1(request)
			},
			code: eebusraw.ErrorCodeV1InvalidArgument,
		},
		{
			name: "probe ttl above contract",
			call: func() *eebusraw.ErrorV1 {
				request := validSet
				request.Mode = eebusraw.ModeV1Probe
				request.ProbeTTLSeconds = 901
				return eebusraw.ValidateFeatureDataSetRequestV1(request)
			},
			code: eebusraw.ErrorCodeV1InvalidArgument,
		},
		{
			name: "secret override",
			call: func() *eebusraw.ErrorV1 {
				request := validSet
				request.ConstraintsOverride = &eebusraw.ConstraintOverrideV1{
					ProfileID:     "issue87-profile",
					Justification: "Bearer fixture-secret",
					ExpiresAt:     now.Add(time.Hour),
				}
				return eebusraw.ValidateFeatureDataSetRequestV1(request)
			},
			code: eebusraw.ErrorCodeV1SecretDetected,
		},
		{
			name: "noncanonical mutation ref",
			call: func() *eebusraw.ErrorV1 {
				return eebusraw.ValidateMutationGetRequestV1(
					eebusraw.MutationGetRequestV1{MutationRef: "mutation:v1:" + strings.Repeat("a", 64)},
				)
			},
			code: eebusraw.ErrorCodeV1InvalidArgument,
		},
		{
			name: "rollback key outside contract",
			call: func() *eebusraw.ErrorV1 {
				request := validRollback
				request.IdempotencyKey = strings.Repeat("x", 129)
				return eebusraw.ValidateMutationRollbackRequestV1(request)
			},
			code: eebusraw.ErrorCodeV1InvalidArgument,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			terminal := test.call()
			if terminal == nil || terminal.Code != test.code {
				t.Fatalf("terminal = %+v, want %s", terminal, test.code)
			}
		})
	}
}

func TestIssue87FeatureResultValidatorsRejectImpossibleEvidence(t *testing.T) {
	now := time.Date(2026, 7, 28, 18, 30, 0, 0, time.UTC)
	binding := eebusraw.RuntimeBindingV1{RuntimeEpoch: 7, ConnectionGeneration: 3}
	locator := issue87Target(eebusraw.OperationV1Read).Locator()
	featuresRequest := eebusraw.FeaturesGetRequestV1{Target: locator}
	featuresData := eebusraw.FeaturesGetDataV1{
		Feature: locator, Functions: []eebusraw.FunctionDescriptorV1{},
		Runtime: binding, DataTimestamp: now, Source: eebusraw.ObservationSourceV1Live,
	}
	hash, err := featuresData.ComputeDataHash()
	if err != nil {
		t.Fatal(err)
	}
	featuresData.DataHash = hash
	if terminal := eebusraw.ValidateFeaturesGetDataV1(featuresRequest, featuresData); terminal != nil {
		t.Fatalf("valid features result rejected: %+v", terminal)
	}

	featuresBefore, err := eebusraw.CanonicalSHA256V1(featuresData)
	if err != nil {
		t.Fatal(err)
	}
	tamperedFeatures := featuresData.Clone()
	tamperedFeatures.Description = "tampered"
	if terminal := eebusraw.ValidateFeaturesGetDataV1(featuresRequest, tamperedFeatures); terminal == nil {
		t.Fatal("tampered features data hash accepted")
	}
	zeroRuntimeFeatures := featuresData.Clone()
	zeroRuntimeFeatures.Runtime = eebusraw.RuntimeBindingV1{}
	if terminal := eebusraw.ValidateFeaturesGetDataV1(featuresRequest, zeroRuntimeFeatures); terminal == nil {
		t.Fatal("zero runtime binding accepted")
	}
	featuresAfter, err := eebusraw.CanonicalSHA256V1(featuresData)
	if err != nil || featuresAfter != featuresBefore {
		t.Fatal("features validator mutated its input")
	}

	readRequest := eebusraw.FeatureDataGetRequestV1{
		Targets: []eebusraw.FeatureTargetV1{issue87Target(eebusraw.OperationV1Read)},
	}
	observation := issue87Observation(t, readRequest.Targets[0], binding, now)
	readData := eebusraw.FeatureDataGetDataV1{
		Results: []eebusraw.ReadObservationV1{observation}, Complete: true,
	}
	if terminal := eebusraw.ValidateFeatureDataGetDataV1(readRequest, readData, nil); terminal != nil {
		t.Fatalf("valid READ result rejected: %+v", terminal)
	}

	partialRequest := readRequest.Clone()
	partialRequest.Targets = append(
		partialRequest.Targets,
		issue87Target(eebusraw.OperationV1Read),
	)
	partialRequest.Targets[1].FeatureAddress = 3
	partial := eebusraw.FeatureDataGetDataV1{
		Results: []eebusraw.ReadObservationV1{observation},
		Failures: []eebusraw.ReadFailureV1{{
			TargetIndex: 1,
			Target:      partialRequest.Targets[1],
			Error: *eebusraw.NewErrorV1(
				eebusraw.ErrorCodeV1RemoteError,
				"fixed remote rejection",
				false,
				eebusraw.SourceLayerV1Remote,
			),
		}},
		Complete: false,
	}
	partialTerminal := eebusraw.NewErrorV1(
		eebusraw.ErrorCodeV1PartialResult,
		"fixed partial result",
		true,
		eebusraw.SourceLayerV1Runtime,
	)
	if terminal := eebusraw.ValidateFeatureDataGetDataV1(
		partialRequest,
		partial,
		partialTerminal,
	); terminal != nil {
		t.Fatalf("valid partial result rejected: %+v", terminal)
	}

	impossible := partial.Clone()
	impossible.Complete = true
	if terminal := eebusraw.ValidateFeatureDataGetDataV1(
		partialRequest,
		impossible,
		partialTerminal,
	); terminal == nil {
		t.Fatal("complete result with failures accepted")
	}
	mixedRuntime := partial.Clone()
	second := issue87Observation(t, partialRequest.Targets[1], binding, now)
	second.Runtime.ConnectionGeneration++
	second.DataHash, err = second.ComputeDataHash()
	if err != nil {
		t.Fatal(err)
	}
	mixedRuntime.Results = append(mixedRuntime.Results, second)
	mixedRuntime.Failures = nil
	mixedRuntime.Complete = true
	if terminal := eebusraw.ValidateFeatureDataGetDataV1(
		partialRequest,
		mixedRuntime,
		nil,
	); terminal == nil {
		t.Fatal("mixed connection-generation result accepted")
	}
}

func TestIssue87MutationValidatorRejectsImpossibleStateEvidence(t *testing.T) {
	now := time.Date(2026, 7, 28, 18, 30, 0, 0, time.UTC)
	before := issue87TypedValue(t, int64(18))
	requested := issue87TypedValue(t, int64(21))
	transition := eebusraw.AuditTransitionV1{
		Sequence: 1, State: eebusraw.MutationStateV1Prepared, TransitionedAt: now,
	}
	transition.TransitionHash = issue87TransitionHash(t, transition)
	valid := eebusraw.MutationV1{
		MutationRef: issue87Reference(3),
		State:       eebusraw.MutationStateV1Prepared,
		Mode:        eebusraw.ModeV1Apply,
		Target:      issue87Target(eebusraw.OperationV1Write),
		Runtime:     eebusraw.RuntimeBindingV1{RuntimeEpoch: 7, ConnectionGeneration: 3},
		Before:      before,
		Requested:   requested,
		CreatedAt:   now,
		UpdatedAt:   now,
		Audit:       []eebusraw.AuditTransitionV1{transition},
	}
	if terminal := eebusraw.ValidateMutationV1(valid); terminal != nil {
		t.Fatalf("valid prepared mutation rejected: %+v", terminal)
	}

	tests := map[string]eebusraw.MutationV1{
		"zero runtime": func() eebusraw.MutationV1 {
			value := valid
			value.Runtime = eebusraw.RuntimeBindingV1{}
			return value
		}(),
		"broken audit hash": func() eebusraw.MutationV1 {
			value := valid
			value.Audit = append([]eebusraw.AuditTransitionV1(nil), valid.Audit...)
			value.Audit[0].TransitionHash = eebusraw.HashV1(
				"sha256:" + strings.Repeat("0", 64),
			)
			return value
		}(),
		"audit state mismatch": func() eebusraw.MutationV1 {
			value := valid
			value.State = eebusraw.MutationStateV1Applied
			return value
		}(),
		"applied without acceptance or verification": func() eebusraw.MutationV1 {
			value := valid
			value.State = eebusraw.MutationStateV1Applied
			value.Audit = append([]eebusraw.AuditTransitionV1(nil), valid.Audit...)
			value.Audit[0].State = eebusraw.MutationStateV1Applied
			value.Audit[0].TransitionHash = issue87TransitionHash(t, value.Audit[0])
			return value
		}(),
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			if terminal := eebusraw.ValidateMutationV1(value); terminal == nil {
				t.Fatalf("impossible mutation accepted: %+v", value)
			}
		})
	}
}

func issue87Reference(fill byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{fill}, sha256.Size))
}

func TestIssue87OpaqueReferenceShapeIsExactlyRawBase64URL256Bit(t *testing.T) {
	for _, value := range []string{
		strings.Repeat("A", 43),
		base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
	} {
		if terminal := eebusraw.ValidateMutationGetRequestV1(
			eebusraw.MutationGetRequestV1{MutationRef: value},
		); terminal != nil {
			t.Fatalf("valid opaque reference %q rejected: %+v", value, terminal)
		}
	}
	nonCanonical := strings.Repeat("A", 42) + "B"
	if terminal := eebusraw.ValidateMutationGetRequestV1(
		eebusraw.MutationGetRequestV1{MutationRef: nonCanonical},
	); terminal == nil {
		t.Fatalf("non-canonical base64url reference %q was accepted", nonCanonical)
	}
}

func issue87Target(operation eebusraw.OperationV1) eebusraw.FeatureTargetV1 {
	return eebusraw.FeatureTargetV1{
		RemoteSKI: strings.Repeat("a", 40), SHIPID: "issue87-ship",
		DeviceAddress: "issue87-device", EntityAddress: []uint64{1},
		FeatureAddress: 2, FeatureType: "Measurement",
		FeatureRole: eebusraw.FeatureRoleV1Server,
		Function:    "measurementListData", Operation: operation,
	}
}

func issue87TypedValue(t *testing.T, value any) eebusraw.TypedValueV1 {
	t.Helper()
	typed, err := eebusraw.NewTypedValueV1(value)
	if err != nil {
		t.Fatal(err)
	}
	return typed
}

func issue87Observation(
	t *testing.T,
	target eebusraw.FeatureTargetV1,
	runtime eebusraw.RuntimeBindingV1,
	now time.Time,
) eebusraw.ReadObservationV1 {
	t.Helper()
	value := issue87TypedValue(t, int64(18))
	observation := eebusraw.ReadObservationV1{
		Target: target, Runtime: runtime,
		RawRequest: eebusraw.ProtocolMessageV1{
			Classifier: "READ", CorrelationKey: 1, Function: target.Function,
		},
		RawResponse: eebusraw.ProtocolMessageV1{
			Classifier: "REPLY", CorrelationKey: 1, Function: target.Function,
			Data: &value,
		},
		Value: value, RequestedAt: now, ReceivedAt: now.Add(time.Millisecond),
		DataTimestamp: now.Add(time.Millisecond), Source: eebusraw.ObservationSourceV1Live,
		ReadToken: eebusraw.ReadTokenV1{
			ReadToken: strings.Repeat("E", 43), ExpiresAt: now.Add(time.Minute),
			BindingHash: eebusraw.HashV1("sha256:" + strings.Repeat("1", 64)),
		},
	}
	hash, err := observation.ComputeDataHash()
	if err != nil {
		t.Fatal(err)
	}
	observation.DataHash = hash
	return observation
}

func issue87TransitionHash(
	t *testing.T,
	transition eebusraw.AuditTransitionV1,
) eebusraw.HashV1 {
	t.Helper()
	hash, err := eebusraw.CanonicalSHA256V1(struct {
		Sequence       uint64                   `json:"sequence"`
		State          eebusraw.MutationStateV1 `json:"state"`
		TransitionedAt time.Time                `json:"transitioned_at"`
		Classification string                   `json:"classification,omitempty"`
		PreviousHash   *eebusraw.HashV1         `json:"previous_hash"`
	}{
		Sequence: transition.Sequence, State: transition.State,
		TransitionedAt: transition.TransitionedAt,
		Classification: transition.Classification,
		PreviousHash:   transition.PreviousHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	return hash
}
