package eebusruntime

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

func TestRawMutationRuntimeExposesSeparateMutationCapability(t *testing.T) {
	backend := &issue85RootMutationBackend{}
	instance, err := newRuntime(
		validRuntimeConfig(t.TempDir()),
		func(context.Context, Config) (runtimeBackend, error) {
			return backend, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	mutations, ok := instance.(RawMutationRuntimeV1)
	if !ok {
		t.Fatal("runtime concrete value does not expose separate RawMutationRuntimeV1")
	}

	request := issue85RootSetRequest(t)
	if _, terminal := mutations.FeaturesDataSet(
		context.Background(),
		issue85RootWriteAuthorization(eebusraw.ToolV1FeaturesDataSet),
		request,
	); terminal == nil || terminal.Code != eebusraw.ErrorCodeV1Disconnected {
		t.Fatalf("pre-start mutation error = %+v, want disconnected", terminal)
	}
	if backend.setCalls.Load() != 0 {
		t.Fatalf("pre-start mutation reached backend %d times", backend.setCalls.Load())
	}

	if err := instance.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := instance.Shutdown(); err != nil {
			t.Error(err)
		}
	})
	backend.setResult = eebusraw.MutationV1{
		MutationRef: "issue85-root-ref",
		State:       eebusraw.MutationStateV1Applied,
	}
	got, terminal := mutations.FeaturesDataSet(
		context.Background(),
		issue85RootWriteAuthorization(eebusraw.ToolV1FeaturesDataSet),
		request,
	)
	if terminal != nil {
		t.Fatalf("FeaturesDataSet() error = %+v", terminal)
	}
	if got.Mutation.MutationRef != "issue85-root-ref" ||
		got.Mutation.State != eebusraw.MutationStateV1Applied ||
		backend.setCalls.Load() != 1 {
		t.Fatalf("root mutation result=%+v calls=%d", got, backend.setCalls.Load())
	}

	backend.rollbackResult = eebusraw.MutationV1{
		MutationRef: "issue85-root-ref",
		State:       eebusraw.MutationStateV1RolledBack,
	}
	rolledBack, terminal := mutations.MutationsRollback(
		context.Background(),
		issue85RootWriteAuthorization(eebusraw.ToolV1MutationsRollback),
		eebusraw.MutationRollbackRequestV1{
			MutationRef:    "issue85-root-ref",
			IdempotencyKey: "issue85-root-rollback",
		},
	)
	if terminal != nil ||
		rolledBack.Mutation.State != eebusraw.MutationStateV1RolledBack ||
		backend.rollbackCalls.Load() != 1 {
		t.Fatalf("root rollback result=%+v error=%+v calls=%d", rolledBack, terminal, backend.rollbackCalls.Load())
	}
}

func TestRawMutationRuntimeAuthorizesBeforeBackendContact(t *testing.T) {
	backend := &issue85RootMutationBackend{}
	instance, err := newRuntime(
		validRuntimeConfig(t.TempDir()),
		func(context.Context, Config) (runtimeBackend, error) {
			return backend, nil
		},
	)
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
	mutations, ok := instance.(RawMutationRuntimeV1)
	if !ok {
		t.Fatal("runtime concrete value does not expose RawMutationRuntimeV1")
	}

	validSetAuth := issue85RootWriteAuthorization(eebusraw.ToolV1FeaturesDataSet)
	validSetRequest := issue85RootSetRequest(t)
	setTests := []struct {
		name       string
		mutateAuth func(*eebusraw.WriteAuthorizationV1)
		mutate     func(*eebusraw.FeatureDataSetRequestV1)
		want       eebusraw.ErrorCodeV1
	}{
		{
			name: "missing principal",
			mutateAuth: func(auth *eebusraw.WriteAuthorizationV1) {
				auth.PrincipalClass = ""
			},
			want: eebusraw.ErrorCodeV1PermissionDenied,
		},
		{
			name: "raw read scope",
			mutateAuth: func(auth *eebusraw.WriteAuthorizationV1) {
				auth.Scope = eebusraw.AuthScopeV1RawRead
			},
			want: eebusraw.ErrorCodeV1PermissionDenied,
		},
		{
			name: "rollback tool",
			mutateAuth: func(auth *eebusraw.WriteAuthorizationV1) {
				auth.Tool = eebusraw.ToolV1MutationsRollback
			},
			want: eebusraw.ErrorCodeV1PermissionDenied,
		},
		{
			name: "redacted tier",
			mutateAuth: func(auth *eebusraw.WriteAuthorizationV1) {
				auth.MaskTier = eebusraw.MaskTierRedacted
			},
			want: eebusraw.ErrorCodeV1PermissionDenied,
		},
		{
			name: "empty idempotency key",
			mutate: func(request *eebusraw.FeatureDataSetRequestV1) {
				request.IdempotencyKey = ""
			},
			want: eebusraw.ErrorCodeV1InvalidArgument,
		},
	}
	for _, test := range setTests {
		t.Run("set/"+test.name, func(t *testing.T) {
			auth := validSetAuth
			request := validSetRequest
			request.Target = request.Target.Clone()
			request.Value = request.Value.Clone()
			if test.mutateAuth != nil {
				test.mutateAuth(&auth)
			}
			if test.mutate != nil {
				test.mutate(&request)
			}
			before := backend.setCalls.Load()
			_, terminal := mutations.FeaturesDataSet(context.Background(), auth, request)
			if terminal == nil || terminal.Code != test.want {
				t.Fatalf("FeaturesDataSet() error = %+v, want %q", terminal, test.want)
			}
			if backend.setCalls.Load() != before {
				t.Fatalf("invalid set reached backend: before=%d after=%d", before, backend.setCalls.Load())
			}
		})
	}

	validReadAuth := eebusraw.ReadAuthorizationV1{
		PrincipalClass: "owner",
		Scope:          eebusraw.AuthScopeV1RawRead,
		Tool:           eebusraw.ToolV1MutationsGet,
		MaskTier:       eebusraw.MaskTierRaw,
	}
	for _, mutate := range []func(*eebusraw.ReadAuthorizationV1){
		func(auth *eebusraw.ReadAuthorizationV1) { auth.PrincipalClass = "" },
		func(auth *eebusraw.ReadAuthorizationV1) { auth.Scope = eebusraw.AuthScopeV1RawWrite },
		func(auth *eebusraw.ReadAuthorizationV1) { auth.Tool = eebusraw.ToolV1FeaturesDataGet },
		func(auth *eebusraw.ReadAuthorizationV1) { auth.MaskTier = eebusraw.MaskTierRedacted },
	} {
		auth := validReadAuth
		mutate(&auth)
		before := backend.getCalls.Load()
		_, terminal := mutations.MutationsGet(
			context.Background(),
			auth,
			eebusraw.MutationGetRequestV1{MutationRef: "issue85-root-ref"},
		)
		if terminal == nil || terminal.Code != eebusraw.ErrorCodeV1PermissionDenied {
			t.Fatalf("MutationsGet() error = %+v, want permission_denied", terminal)
		}
		if backend.getCalls.Load() != before {
			t.Fatalf("invalid get reached backend: before=%d after=%d", before, backend.getCalls.Load())
		}
	}

	validRollbackAuth := issue85RootWriteAuthorization(eebusraw.ToolV1MutationsRollback)
	for _, mutate := range []func(*eebusraw.WriteAuthorizationV1){
		func(auth *eebusraw.WriteAuthorizationV1) { auth.PrincipalClass = "" },
		func(auth *eebusraw.WriteAuthorizationV1) { auth.Scope = eebusraw.AuthScopeV1RawRead },
		func(auth *eebusraw.WriteAuthorizationV1) { auth.Tool = eebusraw.ToolV1FeaturesDataSet },
		func(auth *eebusraw.WriteAuthorizationV1) { auth.MaskTier = eebusraw.MaskTierRedacted },
	} {
		auth := validRollbackAuth
		mutate(&auth)
		before := backend.rollbackCalls.Load()
		_, terminal := mutations.MutationsRollback(
			context.Background(),
			auth,
			eebusraw.MutationRollbackRequestV1{
				MutationRef:    "issue85-root-ref",
				IdempotencyKey: "issue85-root-rollback",
			},
		)
		if terminal == nil || terminal.Code != eebusraw.ErrorCodeV1PermissionDenied {
			t.Fatalf("MutationsRollback() error = %+v, want permission_denied", terminal)
		}
		if backend.rollbackCalls.Load() != before {
			t.Fatalf("invalid rollback reached backend: before=%d after=%d", before, backend.rollbackCalls.Load())
		}
	}
}

func TestRawMutationStatusUsesReadAuthorizationOnly(t *testing.T) {
	backend := &issue85RootMutationBackend{
		getResult: eebusraw.MutationV1{
			MutationRef: "issue85-status-ref",
			State:       eebusraw.MutationStateV1NoEffect,
		},
	}
	instance, err := newRuntime(
		validRuntimeConfig(t.TempDir()),
		func(context.Context, Config) (runtimeBackend, error) {
			return backend, nil
		},
	)
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
	mutations := instance.(RawMutationRuntimeV1)
	got, terminal := mutations.MutationsGet(
		context.Background(),
		eebusraw.ReadAuthorizationV1{
			PrincipalClass: "owner",
			Scope:          eebusraw.AuthScopeV1RawRead,
			Tool:           eebusraw.ToolV1MutationsGet,
			MaskTier:       eebusraw.MaskTierRaw,
		},
		eebusraw.MutationGetRequestV1{MutationRef: "issue85-status-ref"},
	)
	if terminal != nil ||
		got.Mutation.MutationRef != "issue85-status-ref" ||
		backend.getCalls.Load() != 1 {
		t.Fatalf("read-authorized status result=%+v error=%+v calls=%d", got, terminal, backend.getCalls.Load())
	}
}

type issue85RootMutationBackend struct {
	setCalls       atomic.Int64
	getCalls       atomic.Int64
	rollbackCalls  atomic.Int64
	setResult      eebusraw.MutationV1
	getResult      eebusraw.MutationV1
	rollbackResult eebusraw.MutationV1
	terminal       *eebusraw.ErrorV1
}

func (backend *issue85RootMutationBackend) Run(ctx context.Context, _ func(SnapshotV1)) error {
	<-ctx.Done()
	return nil
}

func (backend *issue85RootMutationBackend) Close() error {
	return nil
}

func (backend *issue85RootMutationBackend) FeaturesDataSet(
	context.Context,
	eebusraw.WriteAuthorizationV1,
	eebusraw.FeatureDataSetRequestV1,
) (RawMutationOutcomeV1, *eebusraw.ErrorV1) {
	backend.setCalls.Add(1)
	return RawMutationOutcomeV1{Mutation: backend.setResult}, backend.terminal
}

func (backend *issue85RootMutationBackend) MutationsGet(
	context.Context,
	eebusraw.ReadAuthorizationV1,
	eebusraw.MutationGetRequestV1,
) (RawMutationOutcomeV1, *eebusraw.ErrorV1) {
	backend.getCalls.Add(1)
	return RawMutationOutcomeV1{Mutation: backend.getResult}, backend.terminal
}

func (backend *issue85RootMutationBackend) MutationsRollback(
	context.Context,
	eebusraw.WriteAuthorizationV1,
	eebusraw.MutationRollbackRequestV1,
) (RawMutationOutcomeV1, *eebusraw.ErrorV1) {
	backend.rollbackCalls.Add(1)
	return RawMutationOutcomeV1{Mutation: backend.rollbackResult}, backend.terminal
}

func issue85RootWriteAuthorization(tool eebusraw.ToolV1) eebusraw.WriteAuthorizationV1 {
	return eebusraw.WriteAuthorizationV1{
		PrincipalClass: "owner",
		Scope:          eebusraw.AuthScopeV1RawWrite,
		Tool:           tool,
		MaskTier:       eebusraw.MaskTierRaw,
	}
}

func issue85RootSetRequest(t testing.TB) eebusraw.FeatureDataSetRequestV1 {
	t.Helper()
	value, err := eebusraw.NewTypedValueV1(map[string]any{
		"limit": int64(20),
		"unit":  "degC",
	})
	if err != nil {
		t.Fatal(err)
	}
	return eebusraw.FeatureDataSetRequestV1{
		Target: eebusraw.FeatureTargetV1{
			RemoteSKI:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			SHIPID:         "vr940-runtime",
			DeviceAddress:  "remote-device",
			EntityAddress:  []uint64{1},
			FeatureAddress: 11,
			FeatureType:    "measurement",
			FeatureRole:    eebusraw.FeatureRoleV1Server,
			Function:       "measurementListData",
			Operation:      eebusraw.OperationV1Write,
		},
		Value:          value,
		ReadToken:      "issue85-root-read-token",
		IdempotencyKey: "issue85-root-idempotency",
		Mode:           eebusraw.ModeV1Apply,
	}
}
