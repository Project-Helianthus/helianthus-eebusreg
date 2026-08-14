package eebusruntime

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
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
	assertAdminV1Method(t, admin, "OpenPairingWindow", reflect.TypeOf(func(context.Context, time.Duration, uint64) (ActionHandleV1, error) { return ActionHandleV1{}, nil }))
	assertAdminV1Method(t, admin, "ClosePairingWindow", reflect.TypeOf(func(context.Context, uint64) (ActionHandleV1, error) { return ActionHandleV1{}, nil }))
	assertAdminV1Method(t, admin, "Select", reflect.TypeOf(func(context.Context, ObservationHandleV1, string, uint64) (SelectionHandleV1, error) {
		return SelectionHandleV1{}, nil
	}))
	assertAdminV1Method(t, admin, "Connect", reflect.TypeOf(func(context.Context, SelectionHandleV1, uint64) (ActionHandleV1, error) { return ActionHandleV1{}, nil }))
	assertAdminV1Method(t, admin, "Confirm", reflect.TypeOf(func(context.Context, CandidateHandleV1, string, uint64) (ActionHandleV1, error) {
		return ActionHandleV1{}, nil
	}))
	assertAdminV1Method(t, admin, "Cancel", reflect.TypeOf(func(context.Context, CandidateHandleV1, uint64) (ActionHandleV1, error) { return ActionHandleV1{}, nil }))
	assertAdminV1Method(t, admin, "Retry", reflect.TypeOf(func(context.Context, PartnerHandleV1, uint64) (ActionHandleV1, error) { return ActionHandleV1{}, nil }))
	assertAdminV1Method(t, admin, "Untrust", reflect.TypeOf(func(context.Context, PartnerHandleV1, uint64) (ActionHandleV1, error) { return ActionHandleV1{}, nil }))
}

func TestAdminV1SnapshotIsSanitizedAndDoesNotPublishPrivateBindings(t *testing.T) {
	for _, view := range []reflect.Type{
		reflect.TypeOf(AdminSnapshotV1{}),
		reflect.TypeOf(TrustedPartnerV1{}),
		reflect.TypeOf(ConnectedPartnerV1{}),
		reflect.TypeOf(DiscoveredPartnerV1{}),
		reflect.TypeOf(CandidateV1{}),
	} {
		forbiddenAdminV1Fields(t, view)
	}

	for _, handle := range []reflect.Type{
		reflect.TypeOf(PartnerHandleV1{}),
		reflect.TypeOf(ObservationHandleV1{}),
		reflect.TypeOf(SelectionHandleV1{}),
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

func TestAdminV1DisabledAndZeroOrStaleHandlesFailClosed(t *testing.T) {
	runtime, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	admin := runtime.AdminV1()
	if admin == nil {
		t.Fatal("disabled runtime returned nil AdminV1 facade")
	}

	ctx := context.Background()
	if _, err := admin.Snapshot(ctx); err == nil {
		t.Fatal("disabled runtime exposed an admin snapshot")
	}
	if _, err := admin.OpenPairingWindow(ctx, time.Minute, 0); err == nil {
		t.Fatal("zero expected revision opened pairing")
	}
	if _, err := admin.ClosePairingWindow(ctx, 0); err == nil {
		t.Fatal("zero expected revision closed pairing")
	}
	if _, err := admin.Select(ctx, ObservationHandleV1{}, "", 0); err == nil {
		t.Fatal("zero observation handle selected a discovery")
	}
	if _, err := admin.Connect(ctx, SelectionHandleV1{}, 0); err == nil {
		t.Fatal("zero selection handle connected")
	}
	if _, err := admin.Confirm(ctx, CandidateHandleV1{}, "", 0); err == nil {
		t.Fatal("zero candidate handle confirmed pairing")
	}
	if _, err := admin.Cancel(ctx, CandidateHandleV1{}, 0); err == nil {
		t.Fatal("zero candidate handle cancelled pairing")
	}
	if _, err := admin.Retry(ctx, PartnerHandleV1{}, 0); err == nil {
		t.Fatal("zero partner handle retried")
	}
	if _, err := admin.Untrust(ctx, PartnerHandleV1{}, 0); err == nil {
		t.Fatal("zero partner handle untrusted")
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
	forbidden := []string{
		"candidate_ref", "candidate-ref", "candidate ref", "ref",
		"nonce", "generation", "store", "association", "control", "manifest",
		"path", "endpoint", "address", "host", "port", "private", "pem", "token",
		"key bytes", "key_bytes", "key-bytes",
	}
	for index := 0; index < view.NumField(); index++ {
		field := view.Field(index)
		if field.Name == "SKI" && field.Type.Kind() != reflect.String {
			t.Fatalf("%s.SKI = %s, want canonical public identity string", view.Name(), field.Type)
		}
		published := strings.ToLower(field.Name + " " + string(field.Tag))
		for _, fragment := range forbidden {
			if strings.Contains(published, fragment) {
				t.Fatalf("%s leaks private binding %q", view.Name(), field.Name)
			}
		}
	}
}
