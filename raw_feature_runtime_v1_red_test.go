package eebusruntime

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

func TestIssue83RuntimeRejectsAuthorizationAndShapeBeforeBackendContact(t *testing.T) {
	backend := &issue83RawBackend{}
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

	target := issue83RuntimeTarget()
	validAuth := issue83ReadAuthorization(eebusraw.ToolV1FeaturesDataGet)
	validRequest := eebusraw.FeatureDataGetRequestV1{Targets: []eebusraw.FeatureTargetV1{target}, TimeoutMS: 1000}
	tests := []struct {
		name    string
		auth    eebusraw.ReadAuthorizationV1
		request eebusraw.FeatureDataGetRequestV1
		want    eebusraw.ErrorCodeV1
	}{
		{
			name:    "missing principal",
			auth:    func() eebusraw.ReadAuthorizationV1 { value := validAuth; value.PrincipalClass = ""; return value }(),
			request: validRequest,
			want:    eebusraw.ErrorCodeV1PermissionDenied,
		},
		{
			name:    "wrong scope",
			auth:    func() eebusraw.ReadAuthorizationV1 { value := validAuth; value.Scope = "eebus.raw.write"; return value }(),
			request: validRequest,
			want:    eebusraw.ErrorCodeV1PermissionDenied,
		},
		{
			name: "wrong tool",
			auth: func() eebusraw.ReadAuthorizationV1 {
				value := validAuth
				value.Tool = eebusraw.ToolV1FeaturesGet
				return value
			}(),
			request: validRequest,
			want:    eebusraw.ErrorCodeV1PermissionDenied,
		},
		{
			name: "wrong tier",
			auth: func() eebusraw.ReadAuthorizationV1 {
				value := validAuth
				value.MaskTier = eebusraw.MaskTierRedacted
				return value
			}(),
			request: validRequest,
			want:    eebusraw.ErrorCodeV1PermissionDenied,
		},
		{
			name:    "empty targets",
			auth:    validAuth,
			request: eebusraw.FeatureDataGetRequestV1{},
			want:    eebusraw.ErrorCodeV1InvalidArgument,
		},
		{
			name: "too many targets",
			auth: validAuth,
			request: eebusraw.FeatureDataGetRequestV1{
				Targets:   make([]eebusraw.FeatureTargetV1, eebusraw.MaximumReadTargetsV1+1),
				TimeoutMS: 1000,
			},
			want: eebusraw.ErrorCodeV1InvalidArgument,
		},
		{
			name: "write operation",
			auth: validAuth,
			request: eebusraw.FeatureDataGetRequestV1{
				Targets: []eebusraw.FeatureTargetV1{func() eebusraw.FeatureTargetV1 {
					value := target
					value.Operation = eebusraw.OperationV1Write
					return value
				}()},
				TimeoutMS: 1000,
			},
			want: eebusraw.ErrorCodeV1UnsupportedOperation,
		},
		{
			name:    "timeout above policy",
			auth:    validAuth,
			request: eebusraw.FeatureDataGetRequestV1{Targets: []eebusraw.FeatureTargetV1{target}, TimeoutMS: 30001},
			want:    eebusraw.ErrorCodeV1InvalidArgument,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := backend.dataCalls.Load()
			_, terminal := instance.FeaturesDataGet(context.Background(), test.auth, test.request)
			if terminal == nil || terminal.Code != test.want {
				t.Fatalf("FeaturesDataGet() error = %+v, want code %q", terminal, test.want)
			}
			if after := backend.dataCalls.Load(); after != before {
				t.Fatalf("backend calls changed from %d to %d", before, after)
			}
		})
	}
}

func TestIssue83RuntimeRequiresStartedBackendAndPreservesPartialResult(t *testing.T) {
	backend := &issue83RawBackend{}
	instance, err := newRuntime(validRuntimeConfig(t.TempDir()), func(context.Context, Config) (runtimeBackend, error) {
		return backend, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	request := eebusraw.FeatureDataGetRequestV1{
		Targets:   []eebusraw.FeatureTargetV1{issue83RuntimeTarget(), issue83RuntimeTarget()},
		TimeoutMS: 1000,
	}
	request.Targets[1].FeatureAddress = 12
	auth := issue83ReadAuthorization(eebusraw.ToolV1FeaturesDataGet)

	if _, terminal := instance.FeaturesDataGet(context.Background(), auth, request); terminal == nil ||
		terminal.Code != eebusraw.ErrorCodeV1Disconnected {
		t.Fatalf("pre-start error = %+v, want disconnected", terminal)
	}
	if backend.dataCalls.Load() != 0 {
		t.Fatalf("pre-start backend calls = %d, want 0", backend.dataCalls.Load())
	}

	if err := instance.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	backend.data = eebusraw.FeatureDataGetDataV1{
		Results: []eebusraw.ReadObservationV1{{
			Target: request.Targets[0],
		}},
		Failures: []eebusraw.ReadFailureV1{{
			TargetIndex: 1,
			Target:      request.Targets[1],
			Error: eebusraw.ErrorV1{
				Code:        eebusraw.ErrorCodeV1Timeout,
				Message:     "raw READ timed out",
				Retriable:   true,
				SourceLayer: eebusraw.SourceLayerV1SpineRoundTrip,
			},
		}},
		Complete: false,
	}
	backend.terminal = &eebusraw.ErrorV1{
		Code:        eebusraw.ErrorCodeV1PartialResult,
		Message:     "one or more raw READ targets failed",
		Retriable:   true,
		SourceLayer: eebusraw.SourceLayerV1Runtime,
	}

	data, terminal := instance.FeaturesDataGet(context.Background(), auth, request)
	if terminal == nil || terminal.Code != eebusraw.ErrorCodeV1PartialResult {
		t.Fatalf("partial error = %+v", terminal)
	}
	if data.Complete || len(data.Results) != 1 || len(data.Failures) != 1 || data.Failures[0].TargetIndex != 1 {
		t.Fatalf("partial data = %+v", data)
	}
	backend.data.Results[0].Target.EntityAddress[0] = 99
	backend.data.Failures[0].Target.EntityAddress[0] = 99
	if data.Results[0].Target.EntityAddress[0] != 1 || data.Failures[0].Target.EntityAddress[0] != 1 {
		t.Fatalf("runtime returned backend-owned target slices: %+v", data)
	}

	if err := instance.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if _, terminal := instance.FeaturesDataGet(context.Background(), auth, request); terminal == nil ||
		terminal.Code != eebusraw.ErrorCodeV1Disconnected {
		t.Fatalf("post-shutdown error = %+v, want disconnected", terminal)
	}
}

func TestIssue83RuntimeFeaturesGetUsesClosedReadTool(t *testing.T) {
	backend := &issue83RawBackend{
		inventory: eebusraw.FeaturesGetDataV1{
			Feature:       issue83RuntimeLocator(),
			Functions:     []eebusraw.FunctionDescriptorV1{},
			Source:        eebusraw.ObservationSourceV1Cache,
			DataTimestamp: time.Unix(300, 0).UTC(),
			Runtime:       eebusraw.RuntimeBindingV1{RuntimeEpoch: 8, ConnectionGeneration: 3},
			DataHash:      "sha256:" + strings.Repeat("4", 64),
		},
	}
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

	request := eebusraw.FeaturesGetRequestV1{Target: issue83RuntimeLocator()}
	if _, terminal := instance.FeaturesGet(
		context.Background(),
		issue83ReadAuthorization(eebusraw.ToolV1FeaturesDataGet),
		request,
	); terminal == nil || terminal.Code != eebusraw.ErrorCodeV1PermissionDenied {
		t.Fatalf("wrong-tool inventory error = %+v", terminal)
	}
	if backend.inventoryCalls.Load() != 0 {
		t.Fatalf("wrong-tool inventory calls = %d, want 0", backend.inventoryCalls.Load())
	}

	data, terminal := instance.FeaturesGet(
		context.Background(),
		issue83ReadAuthorization(eebusraw.ToolV1FeaturesGet),
		request,
	)
	if terminal != nil {
		t.Fatalf("FeaturesGet() error = %+v", terminal)
	}
	if data.Source != eebusraw.ObservationSourceV1Cache ||
		data.Runtime != (eebusraw.RuntimeBindingV1{RuntimeEpoch: 8, ConnectionGeneration: 3}) {
		t.Fatalf("FeaturesGet() data = %+v", data)
	}
}

type issue83RawBackend struct {
	inventoryCalls atomic.Int64
	dataCalls      atomic.Int64
	inventory      eebusraw.FeaturesGetDataV1
	data           eebusraw.FeatureDataGetDataV1
	terminal       *eebusraw.ErrorV1
}

func (backend *issue83RawBackend) Run(ctx context.Context, _ func(SnapshotV1)) error {
	<-ctx.Done()
	return nil
}

func (backend *issue83RawBackend) Close() error {
	return nil
}

func (backend *issue83RawBackend) FeaturesGet(
	context.Context,
	eebusraw.ReadAuthorizationV1,
	eebusraw.FeaturesGetRequestV1,
) (eebusraw.FeaturesGetDataV1, *eebusraw.ErrorV1) {
	backend.inventoryCalls.Add(1)
	return backend.inventory, backend.terminal
}

func (backend *issue83RawBackend) FeaturesDataGet(
	context.Context,
	eebusraw.ReadAuthorizationV1,
	eebusraw.FeatureDataGetRequestV1,
) (eebusraw.FeatureDataGetDataV1, *eebusraw.ErrorV1) {
	backend.dataCalls.Add(1)
	return backend.data, backend.terminal
}

func issue83ReadAuthorization(tool eebusraw.ToolV1) eebusraw.ReadAuthorizationV1 {
	return eebusraw.ReadAuthorizationV1{
		PrincipalClass: "owner-local",
		Scope:          eebusraw.AuthScopeV1RawRead,
		Tool:           tool,
		MaskTier:       eebusraw.MaskTierRaw,
	}
}

func issue83RuntimeTarget() eebusraw.FeatureTargetV1 {
	locator := issue83RuntimeLocator()
	return eebusraw.FeatureTargetV1{
		RemoteSKI:      locator.RemoteSKI,
		SHIPID:         locator.SHIPID,
		DeviceAddress:  locator.DeviceAddress,
		EntityAddress:  append([]uint64(nil), locator.EntityAddress...),
		FeatureAddress: locator.FeatureAddress,
		FeatureType:    locator.FeatureType,
		FeatureRole:    locator.FeatureRole,
		Function:       "measurementListData",
		Operation:      eebusraw.OperationV1Read,
	}
}

func issue83RuntimeLocator() eebusraw.FeatureLocatorV1 {
	return eebusraw.FeatureLocatorV1{
		RemoteSKI:      strings.Repeat("a", 40),
		SHIPID:         "vr940-ship-id",
		DeviceAddress:  "remote-device",
		EntityAddress:  []uint64{1},
		FeatureAddress: 11,
		FeatureType:    "measurement",
		FeatureRole:    eebusraw.FeatureRoleV1Server,
	}
}
