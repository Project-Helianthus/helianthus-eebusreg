package eebusruntime

import (
	"context"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusfacade"
)

type RawFeatureRuntimeV1 interface {
	FeaturesGet(
		context.Context,
		eebusraw.ReadAuthorizationV1,
		eebusraw.FeaturesGetRequestV1,
	) (eebusraw.FeaturesGetDataV1, *eebusraw.ErrorV1)
	FeaturesDataGet(
		context.Context,
		eebusraw.ReadAuthorizationV1,
		eebusraw.FeatureDataGetRequestV1,
	) (eebusraw.FeatureDataGetDataV1, *eebusraw.ErrorV1)
}

type rawFeatureRuntimeBackend interface {
	FeaturesGet(
		context.Context,
		eebusraw.ReadAuthorizationV1,
		eebusraw.FeaturesGetRequestV1,
	) (eebusraw.FeaturesGetDataV1, *eebusraw.ErrorV1)
	FeaturesDataGet(
		context.Context,
		eebusraw.ReadAuthorizationV1,
		eebusraw.FeatureDataGetRequestV1,
	) (eebusraw.FeatureDataGetDataV1, *eebusraw.ErrorV1)
}

var _ RawFeatureRuntimeV1 = (*runtimeImplementation)(nil)
var _ rawFeatureRuntimeBackend = (*facadeRuntimeBackend)(nil)

func (runtime *runtimeImplementation) FeaturesGet(
	ctx context.Context,
	auth eebusraw.ReadAuthorizationV1,
	request eebusraw.FeaturesGetRequestV1,
) (eebusraw.FeaturesGetDataV1, *eebusraw.ErrorV1) {
	if terminal := eebusraw.ValidateReadAuthorizationV1(auth, eebusraw.ToolV1FeaturesGet); terminal != nil {
		return eebusraw.FeaturesGetDataV1{}, terminal
	}
	if terminal := eebusraw.ValidateFeaturesGetRequestV1(request); terminal != nil {
		return eebusraw.FeaturesGetDataV1{}, terminal
	}
	backend, terminal := runtime.rawFeatureBackend()
	if terminal != nil {
		return eebusraw.FeaturesGetDataV1{}, terminal
	}
	data, terminal := backend.FeaturesGet(ctx, auth, request.Clone())
	data = data.Clone()
	if terminal != nil {
		cloned := terminal.Clone()
		return data, &cloned
	}
	return data, nil
}

func (runtime *runtimeImplementation) FeaturesDataGet(
	ctx context.Context,
	auth eebusraw.ReadAuthorizationV1,
	request eebusraw.FeatureDataGetRequestV1,
) (eebusraw.FeatureDataGetDataV1, *eebusraw.ErrorV1) {
	if terminal := eebusraw.ValidateReadAuthorizationV1(auth, eebusraw.ToolV1FeaturesDataGet); terminal != nil {
		return eebusraw.FeatureDataGetDataV1{}, terminal
	}
	if terminal := eebusraw.ValidateFeatureDataGetRequestV1(request); terminal != nil {
		return eebusraw.FeatureDataGetDataV1{}, terminal
	}
	backend, terminal := runtime.rawFeatureBackend()
	if terminal != nil {
		return eebusraw.FeatureDataGetDataV1{}, terminal
	}
	data, terminal := backend.FeaturesDataGet(ctx, auth, request.Clone())
	data = data.Clone()
	if terminal != nil {
		cloned := terminal.Clone()
		return data, &cloned
	}
	return data, nil
}

func (runtime *runtimeImplementation) rawFeatureBackend() (rawFeatureRuntimeBackend, *eebusraw.ErrorV1) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if !runtime.enabled || runtime.shutdown || !runtime.started ||
		runtime.backend == nil || runtime.workerErr != nil {
		return nil, eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1Disconnected,
			"raw feature runtime is not connected",
			true,
			eebusraw.SourceLayerV1Runtime,
		)
	}
	backend, ok := runtime.backend.(rawFeatureRuntimeBackend)
	if !ok || backend == nil {
		return nil, eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1Disconnected,
			"raw feature runtime capability is unavailable",
			true,
			eebusraw.SourceLayerV1Runtime,
		)
	}
	return backend, nil
}

func (backend *facadeRuntimeBackend) FeaturesGet(
	ctx context.Context,
	auth eebusraw.ReadAuthorizationV1,
	request eebusraw.FeaturesGetRequestV1,
) (eebusraw.FeaturesGetDataV1, *eebusraw.ErrorV1) {
	raw, ok := backend.backend.(eebusfacade.RawFeatureBackend)
	if !ok || raw == nil {
		return eebusraw.FeaturesGetDataV1{}, eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1Disconnected,
			"raw feature facade capability is unavailable",
			true,
			eebusraw.SourceLayerV1Runtime,
		)
	}
	return raw.FeaturesGet(ctx, auth, request)
}

func (backend *facadeRuntimeBackend) FeaturesDataGet(
	ctx context.Context,
	auth eebusraw.ReadAuthorizationV1,
	request eebusraw.FeatureDataGetRequestV1,
) (eebusraw.FeatureDataGetDataV1, *eebusraw.ErrorV1) {
	raw, ok := backend.backend.(eebusfacade.RawFeatureBackend)
	if !ok || raw == nil {
		return eebusraw.FeatureDataGetDataV1{}, eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1Disconnected,
			"raw feature facade capability is unavailable",
			true,
			eebusraw.SourceLayerV1Runtime,
		)
	}
	return raw.FeaturesDataGet(ctx, auth, request)
}
