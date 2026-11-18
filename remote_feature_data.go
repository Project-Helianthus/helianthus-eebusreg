package eebusruntime

import (
	"context"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusfacade"
)

type remoteFeatureDataRuntime interface {
	FeatureNotifiedDataGet(
		context.Context,
		eebusraw.ReadAuthorizationV1,
		eebusraw.FeatureTargetV1,
	) (eebusraw.ReadObservationV1, *eebusraw.ErrorV1)
}

var _ remoteFeatureDataRuntime = (*runtimeImplementation)(nil)
var _ eebusfacade.RemoteFeatureDataBackend = (*facadeRuntimeBackend)(nil)

// FeatureNotifiedDataGet returns the data a trusted remote last notified for one READ target.
// The runtime subscribes to the readable Measurement, Setpoint, HVAC, DeviceClassification,
// ElectricalConnection, DeviceConfiguration, DeviceDiagnosis and LoadControl server features of
// each trusted remote once detailed discovery lists them, and drops the subscription and its data
// when the remote disappears or is no longer trusted. The result is a live observation whose
// received and data timestamps are the notification time; its raw response carries the NOTIFY
// classifier, it has no raw request and no read token, so a write still needs a full READ first.
// The call takes the FeaturesDataGet authorization and answers a retriable not_found error when
// no notification arrived on the current connection. FeaturesDataGet keeps performing full
// READs. The notification model is specified in
// https://github.com/Project-Helianthus/helianthus-docs-eebus/tree/main/protocols.
func (runtime *runtimeImplementation) FeatureNotifiedDataGet(
	ctx context.Context,
	auth eebusraw.ReadAuthorizationV1,
	target eebusraw.FeatureTargetV1,
) (eebusraw.ReadObservationV1, *eebusraw.ErrorV1) {
	if terminal := eebusraw.ValidateReadAuthorizationV1(auth, eebusraw.ToolV1FeaturesDataGet); terminal != nil {
		return eebusraw.ReadObservationV1{}, terminal
	}
	if terminal := eebusraw.ValidateFeatureDataGetRequestV1(eebusraw.FeatureDataGetRequestV1{
		Targets: []eebusraw.FeatureTargetV1{target},
	}); terminal != nil {
		return eebusraw.ReadObservationV1{}, terminal
	}
	backend, terminal := runtime.remoteFeatureDataBackend()
	if terminal != nil {
		return eebusraw.ReadObservationV1{}, terminal
	}
	observation, terminal := backend.FeatureNotifiedDataGet(ctx, auth, target.Clone())
	observation = observation.Clone()
	if terminal != nil {
		cloned := terminal.Clone()
		return observation, &cloned
	}
	return observation, nil
}

func (runtime *runtimeImplementation) remoteFeatureDataBackend() (
	eebusfacade.RemoteFeatureDataBackend,
	*eebusraw.ErrorV1,
) {
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
	backend, ok := runtime.backend.(eebusfacade.RemoteFeatureDataBackend)
	if !ok || backend == nil {
		return nil, eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1Disconnected,
			"notified feature data capability is unavailable",
			true,
			eebusraw.SourceLayerV1Runtime,
		)
	}
	return backend, nil
}

func (backend *facadeRuntimeBackend) FeatureNotifiedDataGet(
	ctx context.Context,
	auth eebusraw.ReadAuthorizationV1,
	target eebusraw.FeatureTargetV1,
) (eebusraw.ReadObservationV1, *eebusraw.ErrorV1) {
	notified, ok := backend.backend.(eebusfacade.RemoteFeatureDataBackend)
	if !ok || notified == nil {
		return eebusraw.ReadObservationV1{}, eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1Disconnected,
			"notified feature data facade capability is unavailable",
			true,
			eebusraw.SourceLayerV1Runtime,
		)
	}
	return notified.FeatureNotifiedDataGet(ctx, auth, target)
}
