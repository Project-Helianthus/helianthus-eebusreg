package eebusruntime

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusfacade"
)

type remoteFeatureDataBackendStub struct {
	issue83RawBackend
	notifiedCalls atomic.Int64
	notified      eebusraw.ReadObservationV1
	notifiedError *eebusraw.ErrorV1
	lastTarget    eebusraw.FeatureTargetV1
}

func (backend *remoteFeatureDataBackendStub) FeatureNotifiedDataGet(
	_ context.Context,
	_ eebusraw.ReadAuthorizationV1,
	target eebusraw.FeatureTargetV1,
) (eebusraw.ReadObservationV1, *eebusraw.ErrorV1) {
	backend.notifiedCalls.Add(1)
	backend.lastTarget = target
	return backend.notified, backend.notifiedError
}

type remoteFeatureDataFacadeStub struct {
	malformedFacadeBackend
	stub *remoteFeatureDataBackendStub
}

func (backend remoteFeatureDataFacadeStub) FeatureNotifiedDataGet(
	ctx context.Context,
	auth eebusraw.ReadAuthorizationV1,
	target eebusraw.FeatureTargetV1,
) (eebusraw.ReadObservationV1, *eebusraw.ErrorV1) {
	return backend.stub.FeatureNotifiedDataGet(ctx, auth, target)
}

var _ eebusfacade.RemoteFeatureDataBackend = remoteFeatureDataFacadeStub{}

func startRemoteFeatureDataRuntime(t *testing.T, backend runtimeBackend) Runtime {
	t.Helper()
	instance, err := newRuntime(validRuntimeConfig(t.TempDir()), func(context.Context, Config) (runtimeBackend, error) {
		return backend, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := instance.Shutdown(); err != nil {
			t.Error(err)
		}
	})
	return instance
}

func remoteFeatureDataObservation() eebusraw.ReadObservationV1 {
	notifiedAt := time.Date(2030, 1, 2, 3, 5, 0, 0, time.UTC)
	value, err := eebusraw.DecodeTypedValueV1([]byte(`{"measurementData":[{"measurementId":3}]}`))
	if err != nil {
		panic(err)
	}
	return eebusraw.ReadObservationV1{
		Target:  issue83RuntimeTarget(),
		Runtime: eebusraw.RuntimeBindingV1{RuntimeEpoch: 9, ConnectionGeneration: 4},
		RawResponse: eebusraw.ProtocolMessageV1{
			Classifier: "NOTIFY", Function: "measurementListData", Data: &value,
		},
		Value:         value,
		RequestedAt:   notifiedAt,
		ReceivedAt:    notifiedAt,
		DataTimestamp: notifiedAt,
		Source:        eebusraw.ObservationSourceV1Live,
	}
}

func TestRemoteFeatureDataRuntimeServesNotifiedObservationAsCopies(t *testing.T) {
	backend := &remoteFeatureDataBackendStub{notified: remoteFeatureDataObservation()}
	instance := startRemoteFeatureDataRuntime(t, backend)
	reader, ok := instance.(interface {
		FeatureNotifiedDataGet(
			context.Context,
			eebusraw.ReadAuthorizationV1,
			eebusraw.FeatureTargetV1,
		) (eebusraw.ReadObservationV1, *eebusraw.ErrorV1)
	})
	if !ok {
		t.Fatal("runtime does not serve notified remote feature data")
	}
	target := issue83RuntimeTarget()
	observation, terminal := reader.FeatureNotifiedDataGet(
		context.Background(), issue83ReadAuthorization(eebusraw.ToolV1FeaturesDataGet), target,
	)
	if terminal != nil {
		t.Fatalf("FeatureNotifiedDataGet() error = %+v", terminal)
	}
	if !reflect.DeepEqual(observation, remoteFeatureDataObservation()) {
		t.Fatalf("observation = %#v", observation)
	}
	if !observation.ReceivedAt.Equal(time.Date(2030, 1, 2, 3, 5, 0, 0, time.UTC)) {
		t.Fatalf("notification time = %v", observation.ReceivedAt)
	}
	if !reflect.DeepEqual(backend.lastTarget, target) || backend.notifiedCalls.Load() != 1 {
		t.Fatalf("backend target = %#v calls = %d", backend.lastTarget, backend.notifiedCalls.Load())
	}
	observation.Target.EntityAddress[0] = 99
	if backend.notified.Target.EntityAddress[0] != 1 {
		t.Fatal("caller mutation reached the backend observation")
	}
	if backend.dataCalls.Load() != 0 {
		t.Fatalf("notified read performed %d full READs", backend.dataCalls.Load())
	}
}

func TestRemoteFeatureDataRuntimeAnswersNotAvailable(t *testing.T) {
	backend := &remoteFeatureDataBackendStub{notifiedError: eebusraw.NewErrorV1(
		eebusraw.ErrorCodeV1NotFound,
		"notified raw feature data is not available",
		true,
		eebusraw.SourceLayerV1Runtime,
	)}
	reader := startRemoteFeatureDataRuntime(t, backend).(remoteFeatureDataRuntime)
	observation, terminal := reader.FeatureNotifiedDataGet(
		context.Background(), issue83ReadAuthorization(eebusraw.ToolV1FeaturesDataGet), issue83RuntimeTarget(),
	)
	if terminal == nil || terminal.Code != eebusraw.ErrorCodeV1NotFound || !terminal.Retriable ||
		!reflect.DeepEqual(observation, eebusraw.ReadObservationV1{}) {
		t.Fatalf("not available = %#v / %+v", observation, terminal)
	}
	if terminal == backend.notifiedError {
		t.Fatal("runtime returned the backend error pointer")
	}
}

func TestRemoteFeatureDataRuntimeRejectsBeforeBackendContact(t *testing.T) {
	backend := &remoteFeatureDataBackendStub{notified: remoteFeatureDataObservation()}
	reader := startRemoteFeatureDataRuntime(t, backend).(remoteFeatureDataRuntime)
	write := issue83RuntimeTarget()
	write.Operation = eebusraw.OperationV1Write
	for name, call := range map[string]struct {
		auth   eebusraw.ReadAuthorizationV1
		target eebusraw.FeatureTargetV1
		want   eebusraw.ErrorCodeV1
	}{
		"inventory tool": {issue83ReadAuthorization(eebusraw.ToolV1FeaturesGet), issue83RuntimeTarget(), eebusraw.ErrorCodeV1PermissionDenied},
		"write target":   {issue83ReadAuthorization(eebusraw.ToolV1FeaturesDataGet), write, eebusraw.ErrorCodeV1UnsupportedOperation},
		"empty target":   {issue83ReadAuthorization(eebusraw.ToolV1FeaturesDataGet), eebusraw.FeatureTargetV1{Operation: eebusraw.OperationV1Read}, eebusraw.ErrorCodeV1InvalidArgument},
	} {
		t.Run(name, func(t *testing.T) {
			_, terminal := reader.FeatureNotifiedDataGet(context.Background(), call.auth, call.target)
			if terminal == nil || terminal.Code != call.want {
				t.Fatalf("terminal = %+v, want %s", terminal, call.want)
			}
		})
	}
	if backend.notifiedCalls.Load() != 0 {
		t.Fatalf("backend contacted %d times", backend.notifiedCalls.Load())
	}
}

func TestRemoteFeatureDataRuntimeIsDisconnectedWithoutCapability(t *testing.T) {
	reader := startRemoteFeatureDataRuntime(t, &issue83RawBackend{}).(remoteFeatureDataRuntime)
	_, terminal := reader.FeatureNotifiedDataGet(
		context.Background(), issue83ReadAuthorization(eebusraw.ToolV1FeaturesDataGet), issue83RuntimeTarget(),
	)
	if terminal == nil || terminal.Code != eebusraw.ErrorCodeV1Disconnected || !terminal.Retriable {
		t.Fatalf("missing capability = %+v, want retriable disconnected", terminal)
	}
}

func TestRemoteFeatureDataFacadeBackendDelegates(t *testing.T) {
	stub := &remoteFeatureDataBackendStub{notified: remoteFeatureDataObservation()}
	backend := &facadeRuntimeBackend{backend: remoteFeatureDataFacadeStub{stub: stub}}
	observation, terminal := backend.FeatureNotifiedDataGet(
		context.Background(), issue83ReadAuthorization(eebusraw.ToolV1FeaturesDataGet), issue83RuntimeTarget(),
	)
	if terminal != nil || !reflect.DeepEqual(observation, remoteFeatureDataObservation()) || stub.notifiedCalls.Load() != 1 {
		t.Fatalf("facade delegation = %#v / %+v", observation, terminal)
	}
	missing := &facadeRuntimeBackend{backend: malformedFacadeBackend{}}
	if _, terminal := missing.FeatureNotifiedDataGet(
		context.Background(), issue83ReadAuthorization(eebusraw.ToolV1FeaturesDataGet), issue83RuntimeTarget(),
	); terminal == nil || terminal.Code != eebusraw.ErrorCodeV1Disconnected {
		t.Fatalf("facade without capability = %+v", terminal)
	}
}
