package eebusruntime

import (
	"context"
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

func TestIssue120AdminV1CarriesTruthfulRetryAdmissionAndDeadline(t *testing.T) {
	tests := []struct {
		name          string
		state         string
		admitted      bool
		deadlineDelta time.Duration
	}{
		{name: "retry ready", state: "RETRY_READY"},
		{name: "retry admitted", state: "RETRY_READY", admitted: true},
		{name: "backoff active", state: "BACKOFF_ACTIVE", deadlineDelta: 3 * time.Second},
		{name: "terminal quarantine", state: "ADMIN_HOLD"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			facts := operatorAdminV1TestFacts()
			facts.trusted[0].retryState = test.state
			facts.trusted[0].retryAdmitted = test.admitted
			if test.deadlineDelta > 0 {
				facts.trusted[0].retryDeadline = facts.capturedAt.Add(test.deadlineDelta)
			}
			admin := newOperatorAdminV1Reducer(
				newOperatorAdminV1TestClock().Now,
				newOperatorAdminV1TestEntropy(),
				newOperatorAdminV1TestLifecycle(true, true, false),
				newOperatorAdminV1TestBackend(facts),
			)
			snapshot, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Trusted})
			requireAdminV1Success(t, failure)
			if len(snapshot.Trusted) != 1 || snapshot.Trusted[0].RetryState != test.state ||
				snapshot.Trusted[0].RetryAdmitted != test.admitted {
				t.Fatalf("trusted retry row = %+v, want state/admitted %s/%t", snapshot.Trusted, test.state, test.admitted)
			}
			wantDeadline := facts.trusted[0].retryDeadline
			if !snapshot.Trusted[0].RetryDeadline.Equal(wantDeadline) {
				t.Fatalf("retry deadline = %s, want %s", snapshot.Trusted[0].RetryDeadline, wantDeadline)
			}
		})
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
