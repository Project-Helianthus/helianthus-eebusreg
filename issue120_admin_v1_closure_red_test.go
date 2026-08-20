package eebusruntime

import (
	"reflect"
	"testing"
	"time"
)

func TestIssue120AdminV1PublishesOnlyStableOperatorIdentityAndPartnerState(t *testing.T) {
	issue120RequirePublicFields(t, reflect.TypeOf(AdminSnapshotV1{}), map[string]reflect.Type{
		"LocalSKI":    reflect.TypeOf(""),
		"LocalSHIPID": reflect.TypeOf(""),
	})
	issue120RequirePublicFields(t, reflect.TypeOf(TrustedPartnerV1{}), map[string]reflect.Type{
		"Endpoint":        reflect.TypeOf(""),
		"ConnectionState": reflect.TypeOf(""),
		"Name":            reflect.TypeOf(""),
		"Identifier":      reflect.TypeOf(""),
		"Brand":           reflect.TypeOf(""),
		"Type":            reflect.TypeOf(""),
		"Model":           reflect.TypeOf(""),
		"RetryState":      reflect.TypeOf(""),
		"RetryDeadline":   reflect.TypeOf(time.Time{}),
		"RetryAdmitted":   reflect.TypeOf(false),
	})
	issue120RequirePublicFields(t, reflect.TypeOf(ConnectedPartnerV1{}), map[string]reflect.Type{
		"Name":       reflect.TypeOf(""),
		"Identifier": reflect.TypeOf(""),
		"Brand":      reflect.TypeOf(""),
		"Type":       reflect.TypeOf(""),
		"Model":      reflect.TypeOf(""),
	})

	for _, typ := range []reflect.Type{
		reflect.TypeOf(AdminSnapshotV1{}),
		reflect.TypeOf(TrustedPartnerV1{}),
		reflect.TypeOf(ConnectedPartnerV1{}),
	} {
		forbiddenAdminV1Fields(t, typ)
	}
}

func issue120RequirePublicFields(t *testing.T, typ reflect.Type, fields map[string]reflect.Type) {
	t.Helper()
	for name, want := range fields {
		field, ok := typ.FieldByName(name)
		if !ok {
			t.Errorf("%s lacks required field %s", typ.Name(), name)
			continue
		}
		if !field.IsExported() {
			t.Errorf("%s.%s must be exported", typ.Name(), name)
		}
		if field.Type != want {
			t.Errorf("%s.%s = %s, want %s", typ.Name(), name, field.Type, want)
		}
	}
}
