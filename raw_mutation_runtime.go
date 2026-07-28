package eebusruntime

import (
	"context"
	"strings"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusfacade"
)

type RawMutationRuntimeV1 interface {
	FeaturesDataSet(
		context.Context,
		eebusraw.WriteAuthorizationV1,
		eebusraw.FeatureDataSetRequestV1,
	) (eebusraw.MutationV1, *eebusraw.ErrorV1)
	MutationsGet(
		context.Context,
		eebusraw.ReadAuthorizationV1,
		eebusraw.MutationGetRequestV1,
	) (eebusraw.MutationV1, *eebusraw.ErrorV1)
	MutationsRollback(
		context.Context,
		eebusraw.WriteAuthorizationV1,
		eebusraw.MutationRollbackRequestV1,
	) (eebusraw.MutationV1, *eebusraw.ErrorV1)
}

type rawMutationRuntimeBackend interface {
	FeaturesDataSet(
		context.Context,
		eebusraw.WriteAuthorizationV1,
		eebusraw.FeatureDataSetRequestV1,
	) (eebusraw.MutationV1, *eebusraw.ErrorV1)
	MutationsGet(
		context.Context,
		eebusraw.ReadAuthorizationV1,
		eebusraw.MutationGetRequestV1,
	) (eebusraw.MutationV1, *eebusraw.ErrorV1)
	MutationsRollback(
		context.Context,
		eebusraw.WriteAuthorizationV1,
		eebusraw.MutationRollbackRequestV1,
	) (eebusraw.MutationV1, *eebusraw.ErrorV1)
}

var _ RawMutationRuntimeV1 = (*runtimeImplementation)(nil)
var _ rawMutationRuntimeBackend = (*facadeRuntimeBackend)(nil)

func (backend *facadeRuntimeBackend) FeaturesDataSet(
	ctx context.Context,
	auth eebusraw.WriteAuthorizationV1,
	request eebusraw.FeatureDataSetRequestV1,
) (eebusraw.MutationV1, *eebusraw.ErrorV1) {
	raw, ok := backend.backend.(eebusfacade.RawMutationBackend)
	if !ok || raw == nil {
		return eebusraw.MutationV1{}, eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1Disconnected,
			"raw mutation facade capability is unavailable",
			true,
			eebusraw.SourceLayerV1Runtime,
		)
	}
	return raw.FeaturesDataSet(ctx, auth, request)
}

func (backend *facadeRuntimeBackend) MutationsGet(
	ctx context.Context,
	auth eebusraw.ReadAuthorizationV1,
	request eebusraw.MutationGetRequestV1,
) (eebusraw.MutationV1, *eebusraw.ErrorV1) {
	raw, ok := backend.backend.(eebusfacade.RawMutationBackend)
	if !ok || raw == nil {
		return eebusraw.MutationV1{}, eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1Disconnected,
			"raw mutation facade capability is unavailable",
			true,
			eebusraw.SourceLayerV1Runtime,
		)
	}
	return raw.MutationsGet(ctx, auth, request)
}

func (backend *facadeRuntimeBackend) MutationsRollback(
	ctx context.Context,
	auth eebusraw.WriteAuthorizationV1,
	request eebusraw.MutationRollbackRequestV1,
) (eebusraw.MutationV1, *eebusraw.ErrorV1) {
	raw, ok := backend.backend.(eebusfacade.RawMutationBackend)
	if !ok || raw == nil {
		return eebusraw.MutationV1{}, eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1Disconnected,
			"raw mutation facade capability is unavailable",
			true,
			eebusraw.SourceLayerV1Runtime,
		)
	}
	return raw.MutationsRollback(ctx, auth, request)
}

func (runtime *runtimeImplementation) FeaturesDataSet(
	ctx context.Context,
	auth eebusraw.WriteAuthorizationV1,
	request eebusraw.FeatureDataSetRequestV1,
) (eebusraw.MutationV1, *eebusraw.ErrorV1) {
	if terminal := eebusraw.ValidateWriteAuthorizationV1(auth, eebusraw.ToolV1FeaturesDataSet); terminal != nil {
		return eebusraw.MutationV1{}, terminal
	}
	if terminal := validateFeatureDataSetRequestV1(request); terminal != nil {
		return eebusraw.MutationV1{}, terminal
	}
	backend, terminal := runtime.rawMutationBackend()
	if terminal != nil {
		return eebusraw.MutationV1{}, terminal
	}
	mutation, terminal := backend.FeaturesDataSet(ctx, auth, cloneFeatureDataSetRequestV1(request))
	return cloneMutationResultV1(mutation, terminal)
}

func (runtime *runtimeImplementation) MutationsGet(
	ctx context.Context,
	auth eebusraw.ReadAuthorizationV1,
	request eebusraw.MutationGetRequestV1,
) (eebusraw.MutationV1, *eebusraw.ErrorV1) {
	if terminal := eebusraw.ValidateReadAuthorizationV1(auth, eebusraw.ToolV1MutationsGet); terminal != nil {
		return eebusraw.MutationV1{}, terminal
	}
	if strings.TrimSpace(request.MutationRef) == "" {
		return eebusraw.MutationV1{}, eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1InvalidArgument,
			"mutation reference is required",
			false,
			eebusraw.SourceLayerV1Validation,
		)
	}
	backend, terminal := runtime.rawMutationBackend()
	if terminal != nil {
		return eebusraw.MutationV1{}, terminal
	}
	mutation, terminal := backend.MutationsGet(ctx, auth, request)
	return cloneMutationResultV1(mutation, terminal)
}

func (runtime *runtimeImplementation) MutationsRollback(
	ctx context.Context,
	auth eebusraw.WriteAuthorizationV1,
	request eebusraw.MutationRollbackRequestV1,
) (eebusraw.MutationV1, *eebusraw.ErrorV1) {
	if terminal := eebusraw.ValidateWriteAuthorizationV1(auth, eebusraw.ToolV1MutationsRollback); terminal != nil {
		return eebusraw.MutationV1{}, terminal
	}
	if strings.TrimSpace(request.MutationRef) == "" ||
		strings.TrimSpace(request.IdempotencyKey) == "" {
		return eebusraw.MutationV1{}, eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1InvalidArgument,
			"mutation reference and idempotency key are required",
			false,
			eebusraw.SourceLayerV1Validation,
		)
	}
	backend, terminal := runtime.rawMutationBackend()
	if terminal != nil {
		return eebusraw.MutationV1{}, terminal
	}
	mutation, terminal := backend.MutationsRollback(ctx, auth, request)
	return cloneMutationResultV1(mutation, terminal)
}

func (runtime *runtimeImplementation) rawMutationBackend() (rawMutationRuntimeBackend, *eebusraw.ErrorV1) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if !runtime.enabled || runtime.shutdown || !runtime.started ||
		runtime.backend == nil || runtime.workerErr != nil {
		return nil, eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1Disconnected,
			"raw mutation runtime is not connected",
			true,
			eebusraw.SourceLayerV1Runtime,
		)
	}
	backend, ok := runtime.backend.(rawMutationRuntimeBackend)
	if !ok || backend == nil {
		return nil, eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1Disconnected,
			"raw mutation runtime capability is unavailable",
			true,
			eebusraw.SourceLayerV1Runtime,
		)
	}
	return backend, nil
}

func validateFeatureDataSetRequestV1(request eebusraw.FeatureDataSetRequestV1) *eebusraw.ErrorV1 {
	if request.Target.Operation != eebusraw.OperationV1Write {
		return eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1UnsupportedOperation,
			"raw mutation requires a full WRITE target",
			false,
			eebusraw.SourceLayerV1Validation,
		)
	}
	readTarget := request.Target.Clone()
	readTarget.Operation = eebusraw.OperationV1Read
	if terminal := eebusraw.ValidateFeatureDataGetRequestV1(eebusraw.FeatureDataGetRequestV1{
		Targets: []eebusraw.FeatureTargetV1{readTarget},
	}); terminal != nil {
		return terminal
	}
	if request.Value.Validate() != nil ||
		strings.TrimSpace(request.ReadToken) == "" ||
		strings.TrimSpace(request.IdempotencyKey) == "" {
		return eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1InvalidArgument,
			"complete value, read token, and idempotency key are required",
			false,
			eebusraw.SourceLayerV1Validation,
		)
	}
	if request.ExpectedCurrent != nil && request.ExpectedCurrent.Validate() != nil {
		return eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1InvalidArgument,
			"expected current value is invalid",
			false,
			eebusraw.SourceLayerV1Validation,
		)
	}
	switch request.Mode {
	case eebusraw.ModeV1Apply:
		if request.ProbeTTLSeconds != 0 {
			return eebusraw.NewErrorV1(
				eebusraw.ErrorCodeV1InvalidArgument,
				"apply mode forbids a probe TTL",
				false,
				eebusraw.SourceLayerV1Validation,
			)
		}
	case eebusraw.ModeV1Probe:
		if request.ProbeTTLSeconds == 0 {
			return eebusraw.NewErrorV1(
				eebusraw.ErrorCodeV1InvalidArgument,
				"probe mode requires a positive TTL",
				false,
				eebusraw.SourceLayerV1Validation,
			)
		}
	default:
		return eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1InvalidArgument,
			"mutation mode is invalid",
			false,
			eebusraw.SourceLayerV1Validation,
		)
	}
	if override := request.ConstraintsOverride; override != nil {
		if strings.TrimSpace(override.ProfileID) == "" ||
			strings.TrimSpace(override.Justification) == "" ||
			override.ExpiresAt.IsZero() {
			return eebusraw.NewErrorV1(
				eebusraw.ErrorCodeV1InvalidArgument,
				"constraint override is incomplete",
				false,
				eebusraw.SourceLayerV1Validation,
			)
		}
	}
	return nil
}

func cloneFeatureDataSetRequestV1(request eebusraw.FeatureDataSetRequestV1) eebusraw.FeatureDataSetRequestV1 {
	request.Target = request.Target.Clone()
	request.Value = request.Value.Clone()
	if request.ExpectedCurrent != nil {
		value := request.ExpectedCurrent.Clone()
		request.ExpectedCurrent = &value
	}
	if request.ConstraintsOverride != nil {
		override := *request.ConstraintsOverride
		request.ConstraintsOverride = &override
	}
	return request
}

func cloneMutationResultV1(
	mutation eebusraw.MutationV1,
	terminal *eebusraw.ErrorV1,
) (eebusraw.MutationV1, *eebusraw.ErrorV1) {
	mutation = cloneMutationV1(mutation)
	if terminal == nil {
		return mutation, nil
	}
	cloned := terminal.Clone()
	return mutation, &cloned
}

func cloneMutationV1(mutation eebusraw.MutationV1) eebusraw.MutationV1 {
	mutation.Target = mutation.Target.Clone()
	mutation.Before = mutation.Before.Clone()
	mutation.Requested = mutation.Requested.Clone()
	mutation.ProtocolAccepted = cloneBoolPointer(mutation.ProtocolAccepted)
	mutation.ObservedAfter = cloneTypedValuePointer(mutation.ObservedAfter)
	if mutation.Rollback != nil {
		rollback := *mutation.Rollback
		rollback.Before = rollback.Before.Clone()
		rollback.ProtocolAccepted = cloneBoolPointer(rollback.ProtocolAccepted)
		rollback.ObservedAfter = cloneTypedValuePointer(rollback.ObservedAfter)
		if rollback.Error != nil {
			value := rollback.Error.Clone()
			rollback.Error = &value
		}
		if rollback.Verification != nil {
			value := *rollback.Verification
			rollback.Verification = &value
		}
		mutation.Rollback = &rollback
	}
	if mutation.ProbeDeadline != nil {
		value := *mutation.ProbeDeadline
		mutation.ProbeDeadline = &value
	}
	if mutation.Error != nil {
		value := mutation.Error.Clone()
		mutation.Error = &value
	}
	if mutation.ApplyVerification != nil {
		value := *mutation.ApplyVerification
		mutation.ApplyVerification = &value
	}
	if mutation.ConflictEvidence != nil {
		value := *mutation.ConflictEvidence
		mutation.ConflictEvidence = &value
	}
	if mutation.NoContactEvidence != nil {
		value := *mutation.NoContactEvidence
		mutation.NoContactEvidence = &value
	}
	if mutation.RejectionVerification != nil {
		value := *mutation.RejectionVerification
		mutation.RejectionVerification = &value
	}
	if mutation.NoEffectVerification != nil {
		value := *mutation.NoEffectVerification
		mutation.NoEffectVerification = &value
	}
	if mutation.OutcomeEvidence != nil {
		value := *mutation.OutcomeEvidence
		mutation.OutcomeEvidence = &value
	}
	if mutation.Audit != nil {
		mutation.Audit = append([]eebusraw.AuditTransitionV1(nil), mutation.Audit...)
		for index := range mutation.Audit {
			if mutation.Audit[index].PreviousHash != nil {
				value := *mutation.Audit[index].PreviousHash
				mutation.Audit[index].PreviousHash = &value
			}
		}
	}
	return mutation
}

func cloneTypedValuePointer(value *eebusraw.TypedValueV1) *eebusraw.TypedValueV1 {
	if value == nil {
		return nil
	}
	cloned := value.Clone()
	return &cloned
}

func cloneBoolPointer(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
