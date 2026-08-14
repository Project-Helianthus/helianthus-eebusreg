package eebusruntime

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestAdminV1FacadeHasClosedRequestResultOperations(t *testing.T) {
	admin := reflect.TypeOf((*AdminV1)(nil)).Elem()
	want := map[string]adminV1MethodShape{
		"Snapshot":           {request: "AdminSnapshotRequestV1", result: "AdminSnapshotV1"},
		"OpenPairingWindow":  {request: "OpenPairingWindowRequestV1", result: "AdminMutationResultV1"},
		"ClosePairingWindow": {request: "ClosePairingWindowRequestV1", result: "AdminMutationResultV1"},
		"Select":             {request: "SelectRequestV1", result: "AdminSelectionResultV1"},
		"Connect":            {request: "ConnectRequestV1", result: "AdminMutationResultV1"},
		"Confirm":            {request: "ConfirmRequestV1", result: "AdminMutationResultV1"},
		"Cancel":             {request: "CancelRequestV1", result: "AdminMutationResultV1"},
		"RetryTrusted":       {request: "RetryTrustedRequestV1", result: "AdminMutationResultV1"},
		"Untrust":            {request: "UntrustRequestV1", result: "AdminMutationResultV1"},
	}

	if admin.NumMethod() != len(want) {
		t.Errorf("AdminV1 method count = %d, want closed operation count %d", admin.NumMethod(), len(want))
	}
	for name, shape := range want {
		assertAdminV1MethodShape(t, admin, name, shape)
	}
	for index := 0; index < admin.NumMethod(); index++ {
		method := admin.Method(index)
		if _, ok := want[method.Name]; !ok {
			t.Errorf("AdminV1 exposes unexpected method %s", method.Name)
		}
	}
}

func TestAdminV1SnapshotIsSanitizedAndHandlesAreOpaque(t *testing.T) {
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
		reflect.TypeOf(CandidateHandleV1{}),
	} {
		if handle.Kind() != reflect.Struct || handle.NumField() == 0 {
			t.Fatalf("%s must be a non-empty runtime-owned opaque struct handle", handle.Name())
		}
		if handle.Size() > 64 {
			t.Fatalf("%s size = %d, want bounded opaque handle <= 64 bytes", handle.Name(), handle.Size())
		}
		for index := 0; index < handle.NumField(); index++ {
			if handle.Field(index).IsExported() {
				t.Fatalf("%s leaks exported handle binding %q", handle.Name(), handle.Field(index).Name)
			}
		}
	}
}

type adminV1MethodShape struct {
	request string
	result  string
}

func assertAdminV1MethodShape(t *testing.T, admin reflect.Type, name string, want adminV1MethodShape) {
	t.Helper()
	method, ok := admin.MethodByName(name)
	if !ok {
		t.Errorf("AdminV1 lacks %s", name)
		return
	}
	if method.Type.NumIn() != 2 {
		t.Errorf("AdminV1.%s input count = %d, want context plus one closed request", name, method.Type.NumIn())
		return
	}
	contextType := reflect.TypeOf((*context.Context)(nil)).Elem()
	if method.Type.In(0) != contextType {
		t.Errorf("AdminV1.%s first input = %s, want context.Context", name, method.Type.In(0))
	}
	assertAdminV1NamedType(t, "AdminV1."+name+" request", method.Type.In(1), want.request)

	if method.Type.NumOut() != 2 {
		t.Errorf("AdminV1.%s output count = %d, want typed result plus *AdminErrorV1", name, method.Type.NumOut())
		return
	}
	assertAdminV1NamedType(t, "AdminV1."+name+" result", method.Type.Out(0), want.result)
	errorType := method.Type.Out(1)
	if errorType.Kind() != reflect.Pointer {
		t.Errorf("AdminV1.%s error = %s, want *AdminErrorV1", name, errorType)
		return
	}
	assertAdminV1NamedType(t, "AdminV1."+name+" error", errorType.Elem(), "AdminErrorV1")
}

func assertAdminV1NamedType(t *testing.T, label string, typ reflect.Type, name string) {
	t.Helper()
	if typ.PkgPath() != reflect.TypeOf(AdminSnapshotV1{}).PkgPath() || typ.Name() != name {
		t.Errorf("%s = %s, want eebusruntime.%s", label, typ, name)
	}
}

func forbiddenAdminV1Fields(t *testing.T, view reflect.Type) {
	t.Helper()
	forbidden := []string{
		"candidate_ref", "candidate-ref", "candidate ref",
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
