package eebusfacade

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"time"

	executor "github.com/Project-Helianthus/helianthus-eebus-go/features/executor"
	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusmutation"
	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
	spine "github.com/Project-Helianthus/helianthus-spine-go/spine"
)

type RawMutationBackend interface {
	FeaturesDataSet(
		context.Context,
		eebusraw.WriteAuthorizationV1,
		eebusraw.FeatureDataSetRequestV1,
	) (RawMutationOutcomeV1, *eebusraw.ErrorV1)
	MutationsGet(
		context.Context,
		eebusraw.ReadAuthorizationV1,
		eebusraw.MutationGetRequestV1,
	) (RawMutationOutcomeV1, *eebusraw.ErrorV1)
	MutationsRollback(
		context.Context,
		eebusraw.WriteAuthorizationV1,
		eebusraw.MutationRollbackRequestV1,
	) (RawMutationOutcomeV1, *eebusraw.ErrorV1)
}

type RawMutationOutcomeV1 struct {
	Mutation eebusraw.MutationV1
	Runtime  *eebusraw.RuntimeBindingV1
}

func (issuer *rawReadTokenIssuer) VerifyReadToken(
	ctx context.Context,
	token string,
) (eebusmutation.ReadTokenBinding, *eebusraw.ErrorV1) {
	if issuer == nil || ctx == nil || ctx.Err() != nil {
		return eebusmutation.ReadTokenBinding{}, rawMutationFacadeError(
			eebusraw.ErrorCodeV1Cancelled,
			true,
		)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != sha256.Size {
		return eebusmutation.ReadTokenBinding{}, rawMutationFacadeError(
			eebusraw.ErrorCodeV1StaleReadToken,
			false,
		)
	}
	issuer.mu.Lock()
	binding, exists := issuer.bindings[token]
	issuer.mu.Unlock()
	if !exists {
		return eebusmutation.ReadTokenBinding{}, rawMutationFacadeError(
			eebusraw.ErrorCodeV1StaleReadToken,
			false,
		)
	}
	bindingHash, err := eebusraw.CanonicalSHA256V1(binding)
	if err != nil {
		return eebusmutation.ReadTokenBinding{}, rawMutationFacadeError(
			eebusraw.ErrorCodeV1StaleReadToken,
			false,
		)
	}
	mac := hmac.New(sha256.New, issuer.key[:])
	_, _ = mac.Write([]byte(rawReadTokenDomainV1))
	_, _ = mac.Write([]byte(bindingHash))
	if !hmac.Equal(decoded, mac.Sum(nil)) {
		return eebusmutation.ReadTokenBinding{}, rawMutationFacadeError(
			eebusraw.ErrorCodeV1StaleReadToken,
			false,
		)
	}
	return eebusmutation.ReadTokenBinding{
		Runtime:         binding.Runtime,
		Target:          binding.Target.Clone(),
		RequestHash:     binding.RequestHash,
		BeforeImageHash: binding.BeforeImageHash,
		PrincipalClass:  binding.PrincipalClass,
		Scope:           binding.Scope,
		Tool:            binding.Tool,
		MaskTier:        binding.MaskTier,
		ExpiresAt:       binding.ExpiresAt,
		Reusable:        binding.Reusable,
	}, nil
}

func (issuer *rawReadTokenIssuer) ConsumeReadToken(
	ctx context.Context,
	token string,
) *eebusraw.ErrorV1 {
	if issuer == nil || ctx == nil || ctx.Err() != nil {
		return rawMutationFacadeError(eebusraw.ErrorCodeV1Cancelled, true)
	}
	issuer.mu.Lock()
	defer issuer.mu.Unlock()
	binding, exists := issuer.bindings[token]
	if !exists {
		return rawMutationFacadeError(eebusraw.ErrorCodeV1StaleReadToken, false)
	}
	now := time.Now()
	if issuer.now != nil {
		now = issuer.now()
	}
	if binding.ExpiresAt.IsZero() || !binding.ExpiresAt.After(now) {
		delete(issuer.bindings, token)
		delete(issuer.consumed, token)
		return rawMutationFacadeError(eebusraw.ErrorCodeV1StaleReadToken, false)
	}
	if _, consumed := issuer.consumed[token]; consumed && !binding.Reusable {
		return rawMutationFacadeError(eebusraw.ErrorCodeV1StaleReadToken, false)
	}
	if !binding.Reusable {
		issuer.consumed[token] = struct{}{}
	}
	return nil
}

func (bridge *rawFeatureRuntimeBridge) CurrentRuntimeBinding(
	target eebusraw.FeatureTargetV1,
) (eebusraw.RuntimeBindingV1, *eebusraw.ErrorV1) {
	if bridge == nil {
		return eebusraw.RuntimeBindingV1{}, rawMutationFacadeError(
			eebusraw.ErrorCodeV1Disconnected,
			true,
		)
	}
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	lease := bridge.leasesBySKI[strings.ToLower(target.RemoteSKI)]
	if lease == nil || lease.retired {
		return eebusraw.RuntimeBindingV1{}, rawMutationFacadeError(
			eebusraw.ErrorCodeV1Disconnected,
			true,
		)
	}
	if bridge.currentRuntimeEpoch() != lease.runtimeEpoch {
		return eebusraw.RuntimeBindingV1{}, rawMutationFacadeError(
			eebusraw.ErrorCodeV1RuntimeEpochMismatch,
			false,
		)
	}
	feature, err := exactRawRemoteFeature(lease, target.Locator())
	if err != nil {
		return eebusraw.RuntimeBindingV1{}, rawMutationFacadeError(
			eebusraw.ErrorCodeV1NotFound,
			false,
		)
	}
	operations := feature.Operations()[spinemodel.FunctionType(target.Function)]
	if operations == nil ||
		(target.Operation == eebusraw.OperationV1Read && !operations.Read()) ||
		(target.Operation == eebusraw.OperationV1Write && !operations.Write()) {
		return eebusraw.RuntimeBindingV1{}, rawMutationFacadeError(
			eebusraw.ErrorCodeV1UnsupportedOperation,
			false,
		)
	}
	return eebusraw.RuntimeBindingV1{
		RuntimeEpoch:         lease.runtimeEpoch,
		ConnectionGeneration: uint64(lease.generation),
	}, nil
}

func (bridge *rawFeatureRuntimeBridge) FullReadIfCurrent(
	ctx context.Context,
	target eebusraw.FeatureTargetV1,
	expected eebusraw.RuntimeBindingV1,
) (eebusmutation.ReadResult, *eebusraw.ErrorV1) {
	request, runtime, terminal := bridge.exactReadRequest(target)
	if terminal != nil {
		return eebusmutation.ReadResult{}, terminal
	}
	if terminal := requireExpectedMutationBinding(runtime, expected); terminal != nil {
		return eebusmutation.ReadResult{}, terminal
	}
	result, err := executor.NewExactFeatureExecutor(bridge.local, bridge).Execute(ctx, request)
	if err != nil {
		return eebusmutation.ReadResult{}, translateRawMutationExecutorError(err)
	}
	value, err := rawCommandValue(result.Response, true)
	if err != nil {
		return eebusmutation.ReadResult{}, rawMutationFacadeError(
			eebusraw.ErrorCodeV1DecodeError,
			false,
		)
	}
	return eebusmutation.ReadResult{
		Value:       value,
		Runtime:     runtime,
		Full:        true,
		Trustworthy: true,
	}, nil
}

func (bridge *rawFeatureRuntimeBridge) FullWriteIfCurrent(
	ctx context.Context,
	target eebusraw.FeatureTargetV1,
	value eebusraw.TypedValueV1,
	expected eebusraw.RuntimeBindingV1,
) (eebusmutation.WriteResult, *eebusraw.ErrorV1) {
	request, runtime, terminal := bridge.exactMutationWriteRequest(target, value)
	if terminal != nil {
		return eebusmutation.WriteResult{}, terminal
	}
	if terminal := requireExpectedMutationBinding(runtime, expected); terminal != nil {
		return eebusmutation.WriteResult{}, terminal
	}
	result, err := executor.NewExactFeatureExecutor(bridge.local, bridge).Execute(ctx, request)
	frameSent := mutationFrameSent(result, err)
	if err != nil {
		var remote *spineapi.CorrelatedRemoteError
		if errors.As(err, &remote) {
			return eebusmutation.WriteResult{
				FrameSent:  true,
				Correlated: true,
				Accepted:   false,
			}, translateRawMutationExecutorError(err)
		}
		return eebusmutation.WriteResult{FrameSent: frameSent},
			translateRawMutationExecutorError(err)
	}
	if result.RemoteError != nil {
		return eebusmutation.WriteResult{
			FrameSent:  true,
			Correlated: true,
			Accepted:   false,
		}, translateRawMutationExecutorError(result.RemoteError)
	}
	return eebusmutation.WriteResult{
		FrameSent:  true,
		Correlated: true,
		Accepted:   true,
	}, nil
}

func (bridge *rawFeatureRuntimeBridge) MutationPolicy(
	ctx context.Context,
	request eebusraw.FeatureDataSetRequestV1,
	before eebusraw.TypedValueV1,
) (eebusmutation.PolicyDecision, *eebusraw.ErrorV1) {
	if ctx == nil || ctx.Err() != nil {
		return eebusmutation.PolicyDecision{}, rawMutationFacadeError(
			eebusraw.ErrorCodeV1Cancelled,
			true,
		)
	}
	current, terminal := bridge.CurrentRuntimeBinding(request.Target)
	if terminal != nil || current.RuntimeEpoch == 0 {
		return eebusmutation.PolicyDecision{}, terminal
	}
	beforeValid := before.Validate() == nil
	requestedValid := request.Value.Validate() == nil
	decision := eebusmutation.PolicyDecision{
		FullWrite:             true,
		Changeability:         eebusraw.ChangeabilityV1Unknown,
		ConstraintsKnown:      false,
		LabAllowlisted:        false,
		RollbackRepresentable: beforeValid && requestedValid,
	}
	matches := bridge.matchingMutationLabProfiles(request, before)
	if len(matches) != 1 {
		return decision, nil
	}
	profile := matches[0]
	failures := mutationLabSafetyFailures(
		profile.SafetyPredicates,
		beforeValid && requestedValid,
	)
	if len(failures) != 0 {
		decision.SafetyFailures = failures
		return decision, nil
	}
	decision.Changeability = eebusraw.ChangeabilityV1True
	decision.LabAllowlisted = true
	decision.LabProfileID = profile.ProfileID
	decision.EvidenceHashes = append(
		[]eebusraw.HashV1(nil),
		profile.EvidenceHashes...,
	)
	decision.SafetyPredicates = append(
		[]string(nil),
		profile.SafetyPredicates...,
	)
	return decision, nil
}

func (bridge *rawFeatureRuntimeBridge) matchingMutationLabProfiles(
	request eebusraw.FeatureDataSetRequestV1,
	before eebusraw.TypedValueV1,
) []eebusmutation.LabProfile {
	if bridge == nil || bridge.now == nil || request.ConstraintsOverride == nil {
		return nil
	}
	now := bridge.now().UTC()
	override := request.ConstraintsOverride
	if now.IsZero() ||
		strings.TrimSpace(override.Justification) == "" ||
		!override.ExpiresAt.After(now) {
		return nil
	}
	requestedHash, err := request.Value.ComputeHash()
	if err != nil {
		return nil
	}
	beforeHash, err := before.ComputeHash()
	if err != nil {
		return nil
	}
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	matches := make([]eebusmutation.LabProfile, 0, 1)
	for _, profile := range bridge.mutationLabProfiles {
		if eebusraw.ValidateMutationLabProfileV1(
			publicRuntimeMutationLabProfile(profile),
		) != nil ||
			profile.ProfileID != override.ProfileID ||
			!profile.ExpiresAt.After(now) ||
			override.ExpiresAt.After(profile.ExpiresAt) ||
			!reflect.DeepEqual(profile.Target, request.Target) ||
			profile.RollbackValueHash != beforeHash ||
			(request.Mode == eebusraw.ModeV1Probe &&
				request.ProbeTTLSeconds > profile.MaximumProbeTTLSeconds) ||
			!mutationLabAllowsHash(profile.AllowedValueHashes, requestedHash) {
			continue
		}
		matches = append(matches, cloneRuntimeMutationLabProfile(profile))
	}
	return matches
}

func mutationLabSafetyFailures(
	predicates []string,
	rollbackRepresentable bool,
) []string {
	var failures []string
	for _, predicate := range predicates {
		switch predicate {
		case "exact-target-capability-current":
		case "rollback-representable":
			if !rollbackRepresentable {
				failures = append(failures, predicate)
			}
		default:
			failures = append(failures, predicate)
		}
	}
	return failures
}

func mutationLabAllowsHash(
	allowed []eebusraw.HashV1,
	requested eebusraw.HashV1,
) bool {
	for _, candidate := range allowed {
		if candidate == requested {
			return true
		}
	}
	return false
}

func publicRuntimeMutationLabProfile(
	profile eebusmutation.LabProfile,
) eebusraw.MutationLabProfileV1 {
	return eebusraw.MutationLabProfileV1{
		Contract:               profile.Contract,
		ProfileID:              profile.ProfileID,
		Target:                 profile.Target.Clone(),
		AllowedValueHashes:     append([]eebusraw.HashV1(nil), profile.AllowedValueHashes...),
		RollbackValueHash:      profile.RollbackValueHash,
		MaximumProbeTTLSeconds: profile.MaximumProbeTTLSeconds,
		SafetyPredicates:       append([]string(nil), profile.SafetyPredicates...),
		EvidenceHashes:         append([]eebusraw.HashV1(nil), profile.EvidenceHashes...),
		ExpiresAt:              profile.ExpiresAt,
	}
}

func cloneRuntimeMutationLabProfiles(
	profiles []eebusmutation.LabProfile,
) []eebusmutation.LabProfile {
	if profiles == nil {
		return nil
	}
	cloned := make([]eebusmutation.LabProfile, len(profiles))
	for index, profile := range profiles {
		cloned[index] = cloneRuntimeMutationLabProfile(profile)
	}
	return cloned
}

func mutationLabProfilesForRuntime(
	profiles []RuntimeLabProfile,
) []eebusmutation.LabProfile {
	if profiles == nil {
		return nil
	}
	converted := make([]eebusmutation.LabProfile, len(profiles))
	for index, profile := range profiles {
		converted[index] = eebusmutation.LabProfile{
			Contract:               profile.Contract,
			ProfileID:              profile.ProfileID,
			Target:                 profile.Target.Clone(),
			AllowedValueHashes:     append([]eebusraw.HashV1(nil), profile.AllowedValueHashes...),
			RollbackValueHash:      profile.RollbackValueHash,
			MaximumProbeTTLSeconds: profile.MaximumProbeTTLSeconds,
			SafetyPredicates:       append([]string(nil), profile.SafetyPredicates...),
			EvidenceHashes:         append([]eebusraw.HashV1(nil), profile.EvidenceHashes...),
			ExpiresAt:              profile.ExpiresAt,
		}
	}
	return converted
}

func cloneRuntimeMutationLabProfile(
	profile eebusmutation.LabProfile,
) eebusmutation.LabProfile {
	profile.Target = profile.Target.Clone()
	profile.AllowedValueHashes = append([]eebusraw.HashV1(nil), profile.AllowedValueHashes...)
	profile.SafetyPredicates = append([]string(nil), profile.SafetyPredicates...)
	profile.EvidenceHashes = append([]eebusraw.HashV1(nil), profile.EvidenceHashes...)
	return profile
}

func (bridge *rawFeatureRuntimeBridge) exactMutationWriteRequest(
	target eebusraw.FeatureTargetV1,
	value eebusraw.TypedValueV1,
) (executor.ExactFeatureRequest, eebusraw.RuntimeBindingV1, *eebusraw.ErrorV1) {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	lease := bridge.leasesBySKI[strings.ToLower(target.RemoteSKI)]
	if lease == nil || lease.retired {
		return executor.ExactFeatureRequest{}, eebusraw.RuntimeBindingV1{},
			rawMutationFacadeError(eebusraw.ErrorCodeV1Disconnected, true)
	}
	if bridge.currentRuntimeEpoch() != lease.runtimeEpoch {
		return executor.ExactFeatureRequest{}, eebusraw.RuntimeBindingV1{},
			rawMutationFacadeError(eebusraw.ErrorCodeV1RuntimeEpochMismatch, false)
	}
	feature, err := exactRawRemoteFeature(lease, target.Locator())
	if err != nil {
		return executor.ExactFeatureRequest{}, eebusraw.RuntimeBindingV1{},
			rawMutationFacadeError(eebusraw.ErrorCodeV1NotFound, false)
	}
	operations := feature.Operations()[spinemodel.FunctionType(target.Function)]
	if operations == nil || !operations.Write() {
		return executor.ExactFeatureRequest{}, eebusraw.RuntimeBindingV1{},
			rawMutationFacadeError(eebusraw.ErrorCodeV1UnsupportedOperation, false)
	}
	source, found := exactRawSourceAddress(bridge.local, feature)
	if !found {
		return executor.ExactFeatureRequest{}, eebusraw.RuntimeBindingV1{},
			rawMutationFacadeError(eebusraw.ErrorCodeV1Disconnected, true)
	}
	command, err := rawMutationWriteCommand(
		feature.Type(),
		spinemodel.FunctionType(target.Function),
		value,
	)
	if err != nil {
		return executor.ExactFeatureRequest{}, eebusraw.RuntimeBindingV1{},
			rawMutationFacadeError(eebusraw.ErrorCodeV1InvalidArgument, false)
	}
	runtime := eebusraw.RuntimeBindingV1{
		RuntimeEpoch:         lease.runtimeEpoch,
		ConnectionGeneration: uint64(lease.generation),
	}
	return executor.ExactFeatureRequest{
		Source: source,
		Target: executor.ExactFeatureTarget{
			Address:              cloneRawFeatureAddress(*feature.Address()),
			FeatureType:          feature.Type(),
			Role:                 feature.Role(),
			Function:             spinemodel.FunctionType(target.Function),
			RemoteIdentity:       exactRawDispatchIdentity(lease, feature, spinemodel.FunctionType(target.Function)),
			ConnectionGeneration: lease.generation,
		},
		Operation: executor.ExactFeatureOperationWrite,
		Commands:  []spinemodel.CmdType{command},
	}, runtime, nil
}

func rawMutationWriteCommand(
	featureType spinemodel.FeatureTypeType,
	function spinemodel.FunctionType,
	value eebusraw.TypedValueV1,
) (command spinemodel.CmdType, err error) {
	defer func() {
		if recover() != nil {
			command = spinemodel.CmdType{}
			err = errors.New("typed full WRITE function is unavailable")
		}
	}()
	var functionData spineapi.FunctionDataCmdInterface
	for _, candidate := range spine.CreateFunctionData[spineapi.FunctionDataCmdInterface](featureType) {
		if candidate.FunctionType() == function {
			functionData = candidate
			break
		}
	}
	if functionData == nil {
		return spinemodel.CmdType{}, errors.New("typed full WRITE function is unavailable")
	}
	command = functionData.ReadCmdType(nil, nil)
	data, err := command.Data()
	if err != nil || data == nil || data.Value == nil {
		return spinemodel.CmdType{}, errors.New("typed full WRITE data is unavailable")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return spinemodel.CmdType{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(data.Value); err != nil {
		return spinemodel.CmdType{}, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return spinemodel.CmdType{}, errors.New("typed full WRITE data has trailing content")
	}
	return command, nil
}

func requireExpectedMutationBinding(
	current eebusraw.RuntimeBindingV1,
	expected eebusraw.RuntimeBindingV1,
) *eebusraw.ErrorV1 {
	if current.RuntimeEpoch != expected.RuntimeEpoch {
		return rawMutationFacadeError(eebusraw.ErrorCodeV1RuntimeEpochMismatch, false)
	}
	if current.ConnectionGeneration == 0 ||
		current.ConnectionGeneration != expected.ConnectionGeneration {
		return rawMutationFacadeError(
			eebusraw.ErrorCodeV1ConnectionGenerationMismatch,
			false,
		)
	}
	return nil
}

func mutationFrameSent(_ executor.ExactFeatureResult, err error) bool {
	var roundTrip *spineapi.CorrelatedRoundTripError
	if errors.As(err, &roundTrip) && roundTrip != nil {
		return roundTrip.Disposition != spineapi.NoTransportHandoff
	}
	return true
}

func translateRawMutationExecutorError(err error) *eebusraw.ErrorV1 {
	translationErr := err
	var binding *executor.ExactRemoteBindingError
	if errors.As(err, &binding) && binding != nil {
		translationErr = binding
	}
	terminal := translateRawExecutorError(translationErr)
	if terminal == nil {
		return rawMutationFacadeError(eebusraw.ErrorCodeV1Internal, false)
	}
	terminal.Retriable = false
	return terminal
}

func rawMutationFacadeError(
	code eebusraw.ErrorCodeV1,
	retriable bool,
) *eebusraw.ErrorV1 {
	return eebusraw.NewErrorV1(
		code,
		"raw mutation operation failed",
		retriable,
		eebusraw.SourceLayerV1Runtime,
	)
}
