package eebusruntime

import (
	"context"
	"reflect"
	"testing"
)

// This is deliberately a compile-time contract test. The public facade must
// keep the runtime-owned handles opaque: callers can carry them between the
// bounded views and action methods, but cannot manufacture transport, store,
// association, or endpoint bindings.
var _ interface {
	AdminV1() AdminV1
} = (Runtime)(nil)

func TestAdminV1FacadeHasBoundedTypedOperations(t *testing.T) {
	admin := reflect.TypeOf((*AdminV1)(nil)).Elem()
	assertAdminV1Method(t, admin, "Snapshot", reflect.TypeOf(func(context.Context) (AdminSnapshotV1, error) { return AdminSnapshotV1{}, nil }))
	assertAdminV1Method(t, admin, "Select", reflect.TypeOf(func(context.Context, CandidateHandleV1, uint64) (ActionHandleV1, error) { return ActionHandleV1{}, nil }))
	assertAdminV1Method(t, admin, "Connect", reflect.TypeOf(func(context.Context, PartnerHandleV1, uint64) (ActionHandleV1, error) { return ActionHandleV1{}, nil }))
	assertAdminV1Method(t, admin, "Retry", reflect.TypeOf(func(context.Context, PartnerHandleV1, uint64) (ActionHandleV1, error) { return ActionHandleV1{}, nil }))
	assertAdminV1Method(t, admin, "Untrust", reflect.TypeOf(func(context.Context, PartnerHandleV1, uint64) (ActionHandleV1, error) { return ActionHandleV1{}, nil }))
}

func TestAdminV1SnapshotIsSanitizedAndDoesNotPublishPrivateBindings(t *testing.T) {
	for _, view := range []reflect.Type{
		reflect.TypeOf(AdminSnapshotV1{}),
		reflect.TypeOf(TrustedPartnerV1{}),
		reflect.TypeOf(ConnectedPartnerV1{}),
		reflect.TypeOf(DiscoveredPartnerV1{}),
		reflect.TypeOf(PairingCandidateV1{}),
	} {
		forbiddenAdminV1Fields(t, view)
	}

	for _, handle := range []reflect.Type{
		reflect.TypeOf(PartnerHandleV1{}),
		reflect.TypeOf(ObservationHandleV1{}),
		reflect.TypeOf(ActionHandleV1{}),
	} {
		if handle.NumField() == 0 {
			t.Fatalf("%s must be a runtime-owned opaque handle, not a scalar capability", handle.Name())
		}
		for index := 0; index < handle.NumField(); index++ {
			if handle.Field(index).IsExported() {
				t.Fatalf("%s leaks exported handle binding %q", handle.Name(), handle.Field(index).Name)
			}
		}
	}
}

func assertAdminV1Method(t *testing.T, admin reflect.Type, name string, want reflect.Type) {
	t.Helper()
	method, ok := admin.MethodByName(name)
	if !ok {
		t.Fatalf("AdminV1 lacks %s", name)
	}
	if method.Type != want {
		t.Fatalf("AdminV1.%s = %s, want %s", name, method.Type, want)
	}
}

func forbiddenAdminV1Fields(t *testing.T, view reflect.Type) {
	t.Helper()
	forbidden := map[string]struct{}{
		"CandidateRef": {},
		"Nonce":        {},
		"Generation":   {},
		"Store":        {},
		"Path":         {},
		"Endpoint":     {},
		"Private":      {},
	}
	for index := 0; index < view.NumField(); index++ {
		field := view.Field(index)
		if _, forbidden := forbidden[field.Name]; forbidden {
			t.Fatalf("%s leaks private field %q", view.Name(), field.Name)
		}
	}
}
