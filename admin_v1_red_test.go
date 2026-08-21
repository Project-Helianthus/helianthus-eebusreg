package eebusruntime

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
	shipmodel "github.com/Project-Helianthus/helianthus-ship-go/model"
)

func TestAdminV1FacadeHasClosedRequestResultOperations(t *testing.T) {
	admin := reflect.TypeOf((*AdminV1)(nil)).Elem()
	want := map[string]adminV1MethodShape{
		"Snapshot":           {request: "AdminSnapshotRequestV1", result: "AdminSnapshotV1"},
		"OpenPairingWindow":  {request: "OpenPairingWindowRequestV1", result: "AdminMutationResultV1"},
		"ClosePairingWindow": {request: "ClosePairingWindowRequestV1", result: "AdminMutationResultV1"},
		"Select":             {request: "SelectRequestV1", result: "AdminSelectionResultV1"},
		"Connect":            {request: "ConnectRequestV1", result: "ConnectResultV1"},
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

func TestIssue122ConnectPINIsOpaqueAndCannotBeRenderedOrSerialized(t *testing.T) {
	// A pairing PIN enters only at the operator-admin boundary. It is not a
	// result field and no rendering/serialization path may retain it.
	request := ConnectRequestV1{PIN: []byte("a1b2c3d4")}
	for _, rendered := range []string{
		fmt.Sprintf("%v", request),
		fmt.Sprintf("%+v", request),
		fmt.Sprintf("%#v", request),
	} {
		if strings.Contains(rendered, "a1b2c3d4") {
			t.Fatalf("ConnectRequestV1 rendering leaked PIN: %q", rendered)
		}
	}
	if payload, err := json.Marshal(request); err == nil || len(payload) != 0 {
		t.Fatalf("ConnectRequestV1 JSON = %q/%v, want refusal", payload, err)
	}
}

func TestIssue124SHIPCallbackDistinguishesRequiredFromOptionalPIN(t *testing.T) {
	// A terminal operator action must report what SHIP actually observed.  A
	// shared ConnectionStatePin with the same error channel cannot truthfully
	// distinguish a required PIN from an optional continuation.
	required := shipapi.NewConnectionStateDetail(shipapi.ConnectionStatePin, nil)
	required.SetPINHandshakeDetail(&shipmodel.PINHandshakeDetail{
		Requirement: shipmodel.PINRequirementRequired,
		Phase:       shipmodel.PINPhaseWaitingPeer,
		Category:    shipmodel.PINCategoryPointer(shipmodel.PINCategoryRequired),
		Retryable:   true,
	})
	optional := shipapi.NewConnectionStateDetail(shipapi.ConnectionStatePin, nil)
	optional.SetPINHandshakeDetail(&shipmodel.PINHandshakeDetail{
		Requirement: shipmodel.PINRequirementOptional,
		Phase:       shipmodel.PINPhaseRestricted,
		Category:    shipmodel.PINCategoryPointer(shipmodel.PINCategoryOptional),
	})
	if required.PINHandshakeDetail().Equal(optional.PINHandshakeDetail()) {
		t.Fatal("SHIP callback collapses required and optional PIN into ConnectionStatePin; it needs a typed terminal PIN outcome before eebusreg can expose one")
	}
}

func TestIssue124ConnectReturnsStableActionAndSnapshotIsIdentityFree(t *testing.T) {
	clock := newOperatorAdminV1TestClock()
	facts := operatorAdminV1TestFacts()
	backend := newOperatorAdminV1TestBackend(facts)
	admin := newOperatorAdminV1Reducer(
		clock.Now,
		newOperatorAdminV1TestEntropy(),
		newOperatorAdminV1TestLifecycle(true, true, false),
		backend,
	)

	discovered, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Discovered})
	requireAdminV1Success(t, failure)
	selected, failure := admin.Select(context.Background(), SelectRequestV1{
		MutationPreconditionV1: MutationPreconditionV1{IdempotencyKey: "issue124-select", ExpectedStateRevision: discovered.StateRevision},
		Observation:            discovered.Discovered[0].Observation,
		ExpectedSKI:            discovered.Discovered[0].SKI,
	})
	requireAdminV1Success(t, failure)

	const actionID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	outcome := AdminOutcomeV1("pin_required")
	post := cloneOperatorAdminV1TestFacts(facts)
	post.activeAction = &operatorAdminV1ActiveActionFact{
		actionID:  actionID,
		kind:      "connect",
		state:     "terminal",
		outcome:   outcome,
		retryable: true,
		expiresAt: clock.Now().Add(time.Minute),
	}
	backend.setEffect("connect", operatorAdminV1Transition{
		outcome: AdminOutcomeV1("connection_started"), changed: true, actionID: actionID,
	}, "", post)

	request := ConnectRequestV1{
		MutationPreconditionV1: MutationPreconditionV1{IdempotencyKey: "issue124-connect", ExpectedStateRevision: selected.StateRevision},
		Selection:              selected.Selection,
	}
	connected, failure := admin.Connect(context.Background(), request)
	requireAdminV1Success(t, failure)
	if connected.ActionID != actionID || connected.Outcome != AdminOutcomeV1("connection_started") || connected.Replayed {
		t.Fatalf("connect result = %#v, want accepted opaque action", connected)
	}
	replayed, failure := admin.Connect(context.Background(), request)
	requireAdminV1Success(t, failure)
	if replayed.ActionID != actionID || !replayed.Replayed || backend.calls("connect") != 1 {
		t.Fatalf("replayed connect = %#v effects=%d, want same action/no relaunch", replayed, backend.calls("connect"))
	}

	status, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Trusted})
	requireAdminV1Success(t, failure)
	if status.ActiveAction == nil || status.ActiveAction.ActionID != actionID || status.ActiveAction.Kind != "connect" ||
		status.ActiveAction.State != "terminal" || status.ActiveAction.Outcome == nil || *status.ActiveAction.Outcome != outcome ||
		!status.ActiveAction.Retryable || status.ActiveAction.Expiry.IsZero() {
		t.Fatalf("active action = %#v, want closed terminal action", status.ActiveAction)
	}
	forbiddenAdminV1Fields(t, reflect.TypeOf(ActiveActionV1{}))
}

func TestIssue124PinsTypedCallbackDependenciesExactly(t *testing.T) {
	// This node consumes the two released typed-callback boundaries, not a
	// local recreation or a pseudo-version. Keeping their pins explicit makes
	// the source of every terminal action outcome auditable.
	module, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	for _, want := range []string{
		"github.com/Project-Helianthus/helianthus-eebus-go v0.7.1-helianthus.20",
		"github.com/Project-Helianthus/helianthus-ship-go v0.6.1-helianthus.18",
	} {
		if !strings.Contains(string(module), want) {
			t.Errorf("go.mod missing required typed-callback dependency %q", want)
		}
	}
}

func TestIssue124SHIPCallbackExportsTypedPINHandshakeDetail(t *testing.T) {
	// The required/optional/busy/rejected distinction must arrive through the
	// actual callback payload. Do not infer it from an error string.
	detailType := reflect.TypeOf(shipapi.NewConnectionStateDetail(shipapi.ConnectionStatePin, nil))
	method, ok := detailType.MethodByName("PINHandshakeDetail")
	if !ok || method.Type.NumIn() != 1 || method.Type.NumOut() != 1 {
		t.Fatalf("SHIP ConnectionStateDetail lacks typed PINHandshakeDetail callback source: %v", detailType)
	}
}

func TestIssue122InvalidConnectPINFailsBeforeSelectionOrServiceEffect(t *testing.T) {
	clock := newOperatorAdminV1TestClock()
	lifecycle := newOperatorAdminV1TestLifecycle(true, true, false)
	backend := newOperatorAdminV1TestBackend(operatorAdminV1TestFacts())
	admin := newOperatorAdminV1Reducer(clock.Now, newOperatorAdminV1TestEntropy(), lifecycle, backend)

	snapshot, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Trusted})
	requireAdminV1Success(t, failure)
	for _, pin := range [][]byte{
		{},                          // empty-but-present
		[]byte("a1b2c3d"),           // seven bytes
		[]byte("a1b2c3d4e5f607182"), // seventeen bytes
		[]byte("a1b2c3dg"),          // non-hex
		[]byte("a1b2 c3d"),          // whitespace
		[]byte("a1b2c3d\xc2\xa0"),   // non-ASCII whitespace
	} {
		_, failure := admin.Connect(context.Background(), ConnectRequestV1{
			MutationPreconditionV1: MutationPreconditionV1{
				IdempotencyKey:        "invalid-pin-" + hex.EncodeToString(pin),
				ExpectedStateRevision: snapshot.StateRevision,
			},
			PIN: pin,
		})
		if failure == nil || failure.Code != AdminErrorCodeV1InvalidRequest {
			t.Fatalf("PIN %q failure = %#v, want invalid_request", pin, failure)
		}
	}
	if backend.calls("select") != 0 || backend.calls("connect") != 0 {
		t.Fatalf("invalid PIN reached selection/connect effects = %d/%d, want 0/0", backend.calls("select"), backend.calls("connect"))
	}

	// A nil PIN remains omission, not an invalid supplied value.
	_, failure = admin.Connect(context.Background(), ConnectRequestV1{
		MutationPreconditionV1: MutationPreconditionV1{IdempotencyKey: "pin-omitted", ExpectedStateRevision: snapshot.StateRevision},
		PIN:                    nil,
	})
	if failure == nil || failure.Code == AdminErrorCodeV1InvalidRequest {
		t.Fatalf("omitted PIN failure = %#v, must not be invalid_request", failure)
	}
}

func TestIssue122TransientPINOwnsAndClearsMutableBytesWithoutRendering(t *testing.T) {
	raw := []byte("a1b2c3d4")
	pin, ok := newOperatorAdminV1TransientPIN(raw)
	if !ok {
		t.Fatal("valid transient PIN was rejected")
	}
	raw[0] = 'f'
	if pin.cleared() {
		t.Fatal("fresh transient PIN is unexpectedly cleared")
	}
	for _, rendered := range []string{fmt.Sprint(pin), fmt.Sprintf("%+v", pin), fmt.Sprintf("%#v", pin)} {
		if strings.Contains(rendered, "a1b2c3d4") || strings.Contains(rendered, "f1b2c3d4") {
			t.Fatalf("transient PIN formatting leaked secret: %q", rendered)
		}
	}
	if payload, err := json.Marshal(pin); err == nil || len(payload) != 0 {
		t.Fatalf("transient PIN JSON = %q/%v, want refusal", payload, err)
	}
	provided, err := pin.WithTransientPIN("", func(value []byte) error {
		if string(value) != "a1b2c3d4" {
			t.Fatalf("owned PIN = %q, want original copied value", value)
		}
		return nil
	})
	if !provided || err != nil {
		t.Fatalf("one-shot PIN provider=%t/%v, want true/nil", provided, err)
	}
	if !pin.cleared() {
		t.Fatal("transient PIN did not clear owned bytes after consume")
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
	for _, ownerRow := range []reflect.Type{
		reflect.TypeOf(ConnectedPartnerV1{}),
		reflect.TypeOf(DiscoveredPartnerV1{}),
	} {
		endpoint, ok := ownerRow.FieldByName("Endpoint")
		if !ok {
			t.Errorf("%s lacks the owner-only operational Endpoint field", ownerRow.Name())
			continue
		}
		if endpoint.Type.Kind() != reflect.String {
			t.Errorf("%s.Endpoint = %s, want owner-only operational string", ownerRow.Name(), endpoint.Type)
		}
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
		if handle.Size() != 32 {
			t.Fatalf("%s size = %d, want exact 32-byte opaque token", handle.Name(), handle.Size())
		}
		if handle.NumField() != 1 || handle.Field(0).Type != reflect.TypeOf([32]byte{}) {
			t.Fatalf("%s shape = %s, want one sealed [32]byte token", handle.Name(), handle)
		}
		for index := 0; index < handle.NumField(); index++ {
			if handle.Field(index).IsExported() {
				t.Fatalf("%s leaks exported handle binding %q", handle.Name(), handle.Field(index).Name)
			}
		}
	}
}

func TestNewOperatorRuntimeV1AdminValueNeverFormatsOrSerializesFacts(t *testing.T) {
	_, admin, err := NewOperatorRuntimeV1(Config{})
	if err != nil {
		t.Fatalf("NewOperatorRuntimeV1 disabled runtime: %v", err)
	}
	if admin == nil {
		t.Fatal("NewOperatorRuntimeV1 returned nil AdminV1")
	}

	for _, rendered := range []string{
		fmt.Sprintf("%v", admin),
		fmt.Sprintf("%+v", admin),
		fmt.Sprintf("%#v", admin),
		fmt.Sprintf("admin=%v", []any{admin}),
		fmt.Sprintf("admin=%v", map[string]any{"admin": admin}),
	} {
		if rendered != operatorAdminV1RedactedRendering &&
			rendered != "admin="+operatorAdminV1RedactedRendering &&
			rendered != "["+operatorAdminV1RedactedRendering+"]" &&
			rendered != "admin=["+operatorAdminV1RedactedRendering+"]" &&
			rendered != "map[admin:"+operatorAdminV1RedactedRendering+"]" &&
			rendered != "admin=map[admin:"+operatorAdminV1RedactedRendering+"]" {
			t.Fatalf("AdminV1 formatting leaked concrete value: %q", rendered)
		}
		for _, forbidden := range []string{"ski", "endpoint", "handle", "replay", "idempotency", "token"} {
			if strings.Contains(strings.ToLower(rendered), forbidden) {
				t.Fatalf("AdminV1 formatting leaked %q in %q", forbidden, rendered)
			}
		}
	}
	if payload, marshalErr := json.Marshal(admin); marshalErr == nil || len(payload) != 0 {
		t.Fatalf("AdminV1 JSON = %q/%v, want generic serialization refusal", payload, marshalErr)
	}
}

func TestAdminV1ReducerSerializesOneEffectAndReplaysBeforeRevision(t *testing.T) {
	clock := newOperatorAdminV1TestClock()
	lifecycle := newOperatorAdminV1TestLifecycle(true, true, false)
	backend := newOperatorAdminV1TestBackend(operatorAdminV1TestFacts())
	admin := newOperatorAdminV1Reducer(clock.Now, newOperatorAdminV1TestEntropy(), lifecycle, backend)

	snapshot, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Trusted})
	requireAdminV1Success(t, failure)
	if snapshot.StateRevision != 1 {
		t.Fatalf("initial active revision = %d, want 1", snapshot.StateRevision)
	}

	entered, release := backend.block("open")
	request := OpenPairingWindowRequestV1{
		MutationPreconditionV1: MutationPreconditionV1{IdempotencyKey: "same-open", ExpectedStateRevision: 1},
		Duration:               time.Minute,
	}
	results := make(chan operatorAdminV1CallResult, 2)
	started := make(chan struct{})
	go func() {
		result, callFailure := admin.OpenPairingWindow(context.Background(), request)
		results <- operatorAdminV1CallResult{result: result, failure: callFailure}
	}()
	waitOperatorAdminV1Signal(t, entered, "first serialized effect")
	go func() {
		close(started)
		result, callFailure := admin.OpenPairingWindow(context.Background(), request)
		results <- operatorAdminV1CallResult{result: result, failure: callFailure}
	}()
	waitOperatorAdminV1Signal(t, started, "concurrent replay caller")
	close(release)

	first := waitOperatorAdminV1Result(t, results)
	second := waitOperatorAdminV1Result(t, results)
	requireAdminV1Success(t, first.failure)
	requireAdminV1Success(t, second.failure)
	if first.result.StateRevision != 2 || second.result.StateRevision != 2 || first.result.Outcome != second.result.Outcome {
		t.Fatalf("serialized results = %#v and %#v, want the same terminal logical result at revision 2", first.result, second.result)
	}
	if first.result.Replayed == second.result.Replayed {
		t.Fatalf("replay flags = %t and %t, want exactly one replay", first.result.Replayed, second.result.Replayed)
	}
	if calls := backend.calls("open"); calls != 1 {
		t.Fatalf("open effects = %d, want exactly 1", calls)
	}

	_, operationConflict := admin.ClosePairingWindow(context.Background(), ClosePairingWindowRequestV1{
		MutationPreconditionV1: request.MutationPreconditionV1,
	})
	requireAdminV1Code(t, operationConflict, AdminErrorCodeV1IdempotencyConflict)

	changed := request
	changed.Duration = 90 * time.Second
	_, conflict := admin.OpenPairingWindow(context.Background(), changed)
	requireAdminV1Code(t, conflict, AdminErrorCodeV1IdempotencyConflict)
	changed = request
	changed.ExpectedStateRevision = 2
	_, conflict = admin.OpenPairingWindow(context.Background(), changed)
	requireAdminV1Code(t, conflict, AdminErrorCodeV1IdempotencyConflict)
	if calls := backend.calls("open"); calls != 1 {
		t.Fatalf("changed replay binding effects = %d, want 1", calls)
	}
	if calls := backend.calls("close"); calls != 0 {
		t.Fatalf("changed replay operation effects = %d, want 0", calls)
	}

	stale := OpenPairingWindowRequestV1{
		MutationPreconditionV1: MutationPreconditionV1{IdempotencyKey: "unseen-stale", ExpectedStateRevision: 1},
		Duration:               2 * time.Minute,
	}
	_, staleFailure := admin.OpenPairingWindow(context.Background(), stale)
	requireAdminV1Code(t, staleFailure, AdminErrorCodeV1StateConflict)
	if calls := backend.calls("open"); calls != 1 {
		t.Fatalf("unseen stale effects = %d, want 1", calls)
	}
	stale.ExpectedStateRevision = 2
	result, retryFailure := admin.OpenPairingWindow(context.Background(), stale)
	requireAdminV1Success(t, retryFailure)
	if result.StateRevision != 3 || result.Replayed {
		t.Fatalf("unseen stale key retry = %#v, want fresh revision 3 result", result)
	}
	if calls := backend.calls("open"); calls != 2 {
		t.Fatalf("fresh retry effects = %d, want 2", calls)
	}
}

func TestAdminV1ReducerValidatesEveryCommonPrecondition(t *testing.T) {
	clock := newOperatorAdminV1TestClock()
	backend := newOperatorAdminV1TestBackend(operatorAdminV1TestFacts())
	admin := newOperatorAdminV1Reducer(
		clock.Now,
		newOperatorAdminV1TestEntropy(),
		newOperatorAdminV1TestLifecycle(true, true, false),
		backend,
	)
	snapshot, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Trusted})
	requireAdminV1Success(t, failure)

	tests := []struct {
		name string
		key  string
		rev  uint64
	}{
		{name: "empty", key: "", rev: snapshot.StateRevision},
		{name: "malformed UTF-8", key: string([]byte{0xff}), rev: snapshot.StateRevision},
		{name: "over 128 bytes", key: strings.Repeat("k", 129), rev: snapshot.StateRevision},
		{name: "zero revision", key: "zero-revision", rev: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, callFailure := admin.ClosePairingWindow(context.Background(), ClosePairingWindowRequestV1{
				MutationPreconditionV1: MutationPreconditionV1{IdempotencyKey: test.key, ExpectedStateRevision: test.rev},
			})
			requireAdminV1Code(t, callFailure, AdminErrorCodeV1InvalidRequest)
		})
	}
	if calls := backend.calls("close"); calls != 0 {
		t.Fatalf("invalid precondition effects = %d, want 0", calls)
	}

	result, validFailure := admin.ClosePairingWindow(context.Background(), ClosePairingWindowRequestV1{
		MutationPreconditionV1: MutationPreconditionV1{
			IdempotencyKey:        strings.Repeat("k", 128),
			ExpectedStateRevision: snapshot.StateRevision,
		},
	})
	requireAdminV1Success(t, validFailure)
	if result.StateRevision != 2 || backend.calls("close") != 1 {
		t.Fatalf("128-byte precondition result = %#v calls=%d, want one revision-2 effect", result, backend.calls("close"))
	}
}

func TestAdminV1ReducerLifecycleAndExactSnapshotViews(t *testing.T) {
	clock := newOperatorAdminV1TestClock()
	lifecycle := newOperatorAdminV1TestLifecycle(false, false, false)
	backend := newOperatorAdminV1TestBackend(operatorAdminV1TestFacts())
	admin := newOperatorAdminV1Reducer(clock.Now, newOperatorAdminV1TestEntropy(), lifecycle, backend)

	assertOperatorAdminV1SnapshotUnavailable(t, admin, AdminViewV1Trusted)
	lifecycle.set(true, false, false)
	assertOperatorAdminV1SnapshotUnavailable(t, admin, AdminViewV1Trusted)
	lifecycle.set(true, true, false)

	trusted, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Trusted})
	requireAdminV1Success(t, failure)
	assertOperatorAdminV1ViewLengths(t, trusted, 1, 0, 0, 0)
	if trusted.StateRevision != 1 || adminV1HandleField[PartnerHandleV1](t, trusted.Trusted[0], "Partner") == (PartnerHandleV1{}) ||
		!adminV1TimeField(t, trusted, "CapturedAt").Equal(operatorAdminV1TestFacts().capturedAt) ||
		trusted.LocalSKI != operatorAdminV1TestSKI || trusted.LocalSHIPID != "HLS-operator-admin-v1-test" ||
		trusted.Status != "ready" || trusted.Window != "open" || trusted.WindowDeadline.IsZero() ||
		trusted.Register != "enabled" || trusted.Listener != "ready" || trusted.Discovery != "ready" ||
		trusted.DegradedCode != "" || trusted.TrustedCount != 1 || trusted.ConnectedCount != 1 ||
		trusted.DiscoveredCount != 1 || trusted.CandidateCount != 1 || trusted.Trusted[0].TrustState != "trusted" ||
		trusted.Trusted[0].ConnectionState != "idle" || trusted.Trusted[0].Name != "owner peer" ||
		trusted.Trusted[0].Identifier != "owner-id" || trusted.Trusted[0].Brand != "owner-brand" ||
		trusted.Trusted[0].Type != "owner-type" || trusted.Trusted[0].Model != "owner-model" {
		t.Fatalf("trusted snapshot = %#v, want revision 1 with one partner handle", trusted)
	}

	connected, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Connected})
	requireAdminV1Success(t, failure)
	assertOperatorAdminV1ViewLengths(t, connected, 0, 1, 0, 0)
	if connected.StateRevision != 1 || connected.Connected[0].Endpoint != "192.0.2.10:4712" ||
		connected.Connected[0].TrustState != "trusted" || connected.Connected[0].ConnectionState != "connected" ||
		connected.Connected[0].SHIPID != "ship-id-connected" {
		t.Fatalf("connected snapshot = %#v, want only owner endpoint row at revision 1", connected)
	}

	discovered, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Discovered})
	requireAdminV1Success(t, failure)
	assertOperatorAdminV1ViewLengths(t, discovered, 0, 0, 1, 0)
	if discovered.StateRevision != 1 || discovered.Discovered[0].Endpoint != "192.0.2.20:4712" ||
		adminV1HandleField[ObservationHandleV1](t, discovered.Discovered[0], "Observation") == (ObservationHandleV1{}) ||
		discovered.Discovered[0].ObservationRevision != 1 || discovered.Discovered[0].LastSeen.IsZero() ||
		discovered.Discovered[0].Name != "owner peer" || discovered.Discovered[0].Identifier != "owner-id" ||
		discovered.Discovered[0].Brand != "owner-brand" || discovered.Discovered[0].Type != "owner-type" ||
		discovered.Discovered[0].Model != "owner-model" {
		t.Fatalf("discovered snapshot = %#v, want one owner endpoint observation at revision 1", discovered)
	}

	candidate, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Candidate})
	requireAdminV1Success(t, failure)
	assertOperatorAdminV1ViewLengths(t, candidate, 0, 0, 0, 1)
	if candidate.StateRevision != 1 || adminV1HandleField[CandidateHandleV1](t, candidate.Candidates[0], "Candidate") == (CandidateHandleV1{}) ||
		adminV1StringField(t, candidate.Candidates[0], "SKI") != operatorAdminV1TestSKI ||
		candidate.Candidates[0].State != "association_complete" || candidate.Candidates[0].ExpiresAt.IsZero() ||
		!candidate.Candidates[0].AssociationComplete {
		t.Fatalf("candidate snapshot = %#v, want one complete-SKI candidate handle at revision 1", candidate)
	}

	invalid, invalidFailure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1("future")})
	requireAdminV1Code(t, invalidFailure, AdminErrorCodeV1InvalidRequest)
	if !reflect.DeepEqual(invalid, AdminSnapshotV1{}) {
		t.Fatalf("invalid view returned partial snapshot %#v", invalid)
	}

	lifecycle.set(true, true, true)
	assertOperatorAdminV1SnapshotUnavailable(t, admin, AdminViewV1Trusted)
}

func TestAdminV1ReducerAsyncFactsAdvanceRevisionAndInvalidateHandles(t *testing.T) {
	clock := newOperatorAdminV1TestClock()
	facts := operatorAdminV1TestFacts()
	backend := newOperatorAdminV1TestBackend(facts)
	admin := newOperatorAdminV1Reducer(
		clock.Now,
		newOperatorAdminV1TestEntropy(),
		newOperatorAdminV1TestLifecycle(true, true, false),
		backend,
	)

	first, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Discovered})
	requireAdminV1Success(t, failure)
	if first.StateRevision != 1 {
		t.Fatalf("first async-fact revision = %d, want 1", first.StateRevision)
	}
	oldObservation := adminV1HandleField[ObservationHandleV1](t, first.Discovered[0], "Observation")

	unchanged, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Discovered})
	requireAdminV1Success(t, failure)
	unchangedObservation := adminV1HandleField[ObservationHandleV1](t, unchanged.Discovered[0], "Observation")
	if unchanged.StateRevision != 1 || unchangedObservation != oldObservation {
		t.Fatalf("no-op snapshot revision/handle = %d/%#v, want unchanged revision 1 handle", unchanged.StateRevision, unchangedObservation)
	}

	changed := cloneOperatorAdminV1TestFacts(facts)
	changed.discovered = []operatorAdminV1DiscoveredFact{{
		reference: "observation-replacement", ski: operatorAdminV1TestSKI, endpoint: "192.0.2.21:4712",
		observationRevision: 2, lastSeen: clock.Now(), name: "replacement", expiresAt: clock.Now().Add(time.Minute),
	}}
	changed.candidates = []operatorAdminV1CandidateFact{{
		reference: "candidate-replacement", ski: operatorAdminV1TestSKI,
		state: "association_complete", expiresAt: clock.Now().Add(time.Minute), associationComplete: true,
	}}
	backend.setFacts(changed)

	second, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Discovered})
	requireAdminV1Success(t, failure)
	if second.StateRevision != 2 || len(second.Discovered) != 1 || second.Discovered[0].Endpoint != "192.0.2.21:4712" {
		t.Fatalf("changed async-fact snapshot = %#v, want replacement at revision 2", second)
	}
	newObservation := adminV1HandleField[ObservationHandleV1](t, second.Discovered[0], "Observation")
	if newObservation == (ObservationHandleV1{}) || newObservation == oldObservation {
		t.Fatal("owner-visible fact change did not replace its observation handle")
	}

	_, staleFailure := admin.Select(context.Background(), SelectRequestV1{
		MutationPreconditionV1: MutationPreconditionV1{IdempotencyKey: "async-old", ExpectedStateRevision: 2},
		Observation:            oldObservation,
		ExpectedSKI:            operatorAdminV1TestSKI,
	})
	if staleFailure == nil || backend.calls("select") != 0 {
		t.Fatal("pre-change observation handle reached a post-change effect")
	}

	selected, selectFailure := admin.Select(context.Background(), SelectRequestV1{
		MutationPreconditionV1: MutationPreconditionV1{IdempotencyKey: "async-new", ExpectedStateRevision: 2},
		Observation:            newObservation,
		ExpectedSKI:            operatorAdminV1TestSKI,
	})
	requireAdminV1Success(t, selectFailure)
	if selected.StateRevision != 3 || backend.calls("select") != 1 {
		t.Fatalf("current observation selection = %#v calls=%d, want revision 3 and one effect", selected, backend.calls("select"))
	}
}

func TestAdminV1ReducerDiscoveryRevisionChangeReplacesSameIdentityObservation(t *testing.T) {
	clock := newOperatorAdminV1TestClock()
	facts := operatorAdminV1TestFacts()
	backend := newOperatorAdminV1TestBackend(facts)
	admin := newOperatorAdminV1Reducer(
		clock.Now,
		newOperatorAdminV1TestEntropy(),
		newOperatorAdminV1TestLifecycle(true, true, false),
		backend,
	)

	first, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Discovered})
	requireAdminV1Success(t, failure)
	oldObservation := adminV1HandleField[ObservationHandleV1](t, first.Discovered[0], "Observation")

	newDiscoveryRevision := cloneOperatorAdminV1TestFacts(facts)
	newDiscoveryRevision.discovered[0].reference = "observation-reference-revision-2"
	backend.setFacts(newDiscoveryRevision)
	second, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Discovered})
	requireAdminV1Success(t, failure)
	newObservation := adminV1HandleField[ObservationHandleV1](t, second.Discovered[0], "Observation")
	if second.StateRevision != first.StateRevision+1 {
		t.Fatalf("same-identity discovery revision changed root revision %d -> %d, want exactly +1", first.StateRevision, second.StateRevision)
	}
	if second.Discovered[0].SKI != first.Discovered[0].SKI || second.Discovered[0].Endpoint != first.Discovered[0].Endpoint {
		t.Fatalf("fixture changed owner identity/endpoint across discovery revisions: first=%#v second=%#v", first.Discovered[0], second.Discovered[0])
	}
	if newObservation == (ObservationHandleV1{}) || newObservation == oldObservation {
		t.Fatal("new discovery revision retained the old observation capability")
	}

	_, staleFailure := admin.Select(context.Background(), SelectRequestV1{
		MutationPreconditionV1: MutationPreconditionV1{IdempotencyKey: "old-discovery-revision", ExpectedStateRevision: second.StateRevision},
		Observation:            oldObservation,
		ExpectedSKI:            operatorAdminV1TestSKI,
	})
	if staleFailure == nil || backend.calls("select") != 0 {
		t.Fatal("old same-identity discovery revision reached the selection effect")
	}

	selected, selectFailure := admin.Select(context.Background(), SelectRequestV1{
		MutationPreconditionV1: MutationPreconditionV1{IdempotencyKey: "new-discovery-revision", ExpectedStateRevision: second.StateRevision},
		Observation:            newObservation,
		ExpectedSKI:            operatorAdminV1TestSKI,
	})
	requireAdminV1Success(t, selectFailure)
	if selected.StateRevision != second.StateRevision+1 || backend.calls("select") != 1 {
		t.Fatalf("current discovery revision selection=%#v effects=%d, want one effect and revision +1", selected, backend.calls("select"))
	}
}

func TestAdminV1CapabilitiesRequestsResultsAndSnapshotsRenderFailClosed(t *testing.T) {
	clock := newOperatorAdminV1TestClock()
	backend := newOperatorAdminV1TestBackend(operatorAdminV1TestFacts())
	admin := newOperatorAdminV1Reducer(
		clock.Now,
		newOperatorAdminV1TestEntropy(),
		newOperatorAdminV1TestLifecycle(true, true, false),
		backend,
	)

	trusted, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Trusted})
	requireAdminV1Success(t, failure)
	partner := adminV1HandleField[PartnerHandleV1](t, trusted.Trusted[0], "Partner")
	connected, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Connected})
	requireAdminV1Success(t, failure)
	discovered, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Discovered})
	requireAdminV1Success(t, failure)
	observation := adminV1HandleField[ObservationHandleV1](t, discovered.Discovered[0], "Observation")
	candidateSnapshot, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Candidate})
	requireAdminV1Success(t, failure)
	candidate := adminV1HandleField[CandidateHandleV1](t, candidateSnapshot.Candidates[0], "Candidate")
	selectionResult, failure := admin.Select(context.Background(), SelectRequestV1{
		MutationPreconditionV1: MutationPreconditionV1{IdempotencyKey: "render-selection", ExpectedStateRevision: discovered.StateRevision},
		Observation:            observation,
		ExpectedSKI:            operatorAdminV1TestSKI,
	})
	requireAdminV1Success(t, failure)

	secrets := []string{
		operatorAdminV1TestSKI,
		"render-request",
		"render-selection",
		"secret-outcome",
		"192.0.2.10:4712",
		"192.0.2.20:4712",
		adminV1HandleTokenHex(t, partner),
		adminV1HandleTokenHex(t, observation),
		adminV1HandleTokenHex(t, selectionResult.Selection),
		adminV1HandleTokenHex(t, candidate),
	}
	precondition := MutationPreconditionV1{IdempotencyKey: "render-request", ExpectedStateRevision: selectionResult.StateRevision}
	values := []struct {
		name  string
		value any
	}{
		{name: "partner capability", value: partner},
		{name: "observation capability", value: observation},
		{name: "selection capability", value: selectionResult.Selection},
		{name: "candidate capability", value: candidate},
		{name: "snapshot request", value: AdminSnapshotRequestV1{View: AdminViewV1Discovered}},
		{name: "open request", value: OpenPairingWindowRequestV1{MutationPreconditionV1: precondition, Duration: time.Minute}},
		{name: "close request", value: ClosePairingWindowRequestV1{MutationPreconditionV1: precondition}},
		{name: "select request", value: SelectRequestV1{MutationPreconditionV1: precondition, Observation: observation, ExpectedSKI: operatorAdminV1TestSKI}},
		{name: "connect request", value: ConnectRequestV1{MutationPreconditionV1: precondition, Selection: selectionResult.Selection}},
		{name: "confirm request", value: ConfirmRequestV1{MutationPreconditionV1: precondition, Candidate: candidate, ExpectedSKI: operatorAdminV1TestSKI}},
		{name: "cancel request", value: CancelRequestV1{MutationPreconditionV1: precondition, Candidate: candidate}},
		{name: "retry request", value: RetryTrustedRequestV1{MutationPreconditionV1: precondition, Partner: partner}},
		{name: "untrust request", value: UntrustRequestV1{MutationPreconditionV1: precondition, Partner: partner}},
		{name: "mutation result", value: AdminMutationResultV1{StateRevision: 7, Outcome: AdminOutcomeV1("secret-outcome"), Replayed: true}},
		{name: "selection result", value: selectionResult},
		{name: "trusted snapshot", value: trusted},
		{name: "connected snapshot", value: connected},
		{name: "discovered snapshot", value: discovered},
		{name: "candidate snapshot", value: candidateSnapshot},
	}
	for _, value := range values {
		t.Run(value.name, func(t *testing.T) {
			requireAdminV1GenericRenderingRedacted(t, value.value, secrets)
		})
	}
}

func TestAdminV1ReducerFactsCoverOwnerStatusDeadlinesAndExactRows(t *testing.T) {
	base := operatorAdminV1TestFacts()
	changes := []struct {
		name   string
		change func(*operatorAdminV1SnapshotFacts)
	}{
		{name: "status", change: func(facts *operatorAdminV1SnapshotFacts) { facts.status = "degraded" }},
		{name: "window", change: func(facts *operatorAdminV1SnapshotFacts) { facts.window = "closed" }},
		{name: "window deadline", change: func(facts *operatorAdminV1SnapshotFacts) {
			facts.windowDeadline = facts.windowDeadline.Add(time.Second)
		}},
		{name: "register", change: func(facts *operatorAdminV1SnapshotFacts) { facts.register = "disabled" }},
		{name: "listener", change: func(facts *operatorAdminV1SnapshotFacts) { facts.listener = "degraded" }},
		{name: "discovery", change: func(facts *operatorAdminV1SnapshotFacts) { facts.discovery = "degraded" }},
		{name: "degraded", change: func(facts *operatorAdminV1SnapshotFacts) { facts.degraded = AdminErrorCodeV1DiscoveryUnavailable }},
		{name: "trusted row reference", change: func(facts *operatorAdminV1SnapshotFacts) { facts.trusted[0].reference = "partner-reference-2" }},
		{name: "trusted row SKI", change: func(facts *operatorAdminV1SnapshotFacts) { facts.trusted[0].ski = strings.Repeat("a", 40) }},
		{name: "trusted row state", change: func(facts *operatorAdminV1SnapshotFacts) { facts.trusted[0].trustState = "revoked" }},
		{name: "connected row SKI", change: func(facts *operatorAdminV1SnapshotFacts) { facts.connected[0].ski = strings.Repeat("a", 40) }},
		{name: "connected row endpoint", change: func(facts *operatorAdminV1SnapshotFacts) { facts.connected[0].endpoint = "192.0.2.11:4712" }},
		{name: "connected row state", change: func(facts *operatorAdminV1SnapshotFacts) { facts.connected[0].connectionState = "disconnected" }},
		{name: "discovered row reference", change: func(facts *operatorAdminV1SnapshotFacts) {
			facts.discovered[0].reference = "observation-row-revision-2"
		}},
		{name: "discovered row SKI", change: func(facts *operatorAdminV1SnapshotFacts) { facts.discovered[0].ski = strings.Repeat("a", 40) }},
		{name: "discovered row endpoint", change: func(facts *operatorAdminV1SnapshotFacts) { facts.discovered[0].endpoint = "192.0.2.21:4712" }},
		{name: "discovered row deadline", change: func(facts *operatorAdminV1SnapshotFacts) {
			facts.discovered[0].expiresAt = facts.discovered[0].expiresAt.Add(time.Second)
		}},
		{name: "discovered row observation revision", change: func(facts *operatorAdminV1SnapshotFacts) { facts.discovered[0].observationRevision++ }},
		{name: "discovered row last seen", change: func(facts *operatorAdminV1SnapshotFacts) {
			facts.discovered[0].lastSeen = facts.discovered[0].lastSeen.Add(time.Second)
		}},
		{name: "discovered row metadata", change: func(facts *operatorAdminV1SnapshotFacts) { facts.discovered[0].model = "owner-model-2" }},
		{name: "candidate row reference", change: func(facts *operatorAdminV1SnapshotFacts) { facts.candidates[0].reference = "candidate-row-revision-2" }},
		{name: "candidate row SKI", change: func(facts *operatorAdminV1SnapshotFacts) { facts.candidates[0].ski = strings.Repeat("a", 40) }},
		{name: "candidate row deadline", change: func(facts *operatorAdminV1SnapshotFacts) {
			facts.candidates[0].expiresAt = facts.candidates[0].expiresAt.Add(time.Second)
		}},
		{name: "candidate row state", change: func(facts *operatorAdminV1SnapshotFacts) { facts.candidates[0].state = "transient_trusted" }},
		{name: "candidate row association", change: func(facts *operatorAdminV1SnapshotFacts) { facts.candidates[0].associationComplete = false }},
	}
	t.Run("capture time is required but is not state equality", func(t *testing.T) {
		clock := newOperatorAdminV1TestClock()
		backend := newOperatorAdminV1TestBackend(base)
		admin := newOperatorAdminV1Reducer(clock.Now, newOperatorAdminV1TestEntropy(), newOperatorAdminV1TestLifecycle(true, true, false), backend)
		first, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Trusted})
		requireAdminV1Success(t, failure)
		laterCapture := cloneOperatorAdminV1TestFacts(base)
		laterCapture.capturedAt = laterCapture.capturedAt.Add(time.Second)
		backend.setFacts(laterCapture)
		second, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Trusted})
		requireAdminV1Success(t, failure)
		if second.StateRevision != first.StateRevision || adminV1HandleField[PartnerHandleV1](t, second.Trusted[0], "Partner") != adminV1HandleField[PartnerHandleV1](t, first.Trusted[0], "Partner") {
			t.Fatalf("capture-time-only read changed state revision/handle: first=%#v second=%#v", first, second)
		}
		if got := adminV1TimeField(t, second, "CapturedAt"); !got.Equal(laterCapture.capturedAt) {
			t.Fatalf("snapshot capture time=%s, want latest validated %s", got, laterCapture.capturedAt)
		}

		invalidCapture := cloneOperatorAdminV1TestFacts(base)
		invalidCapture.capturedAt = time.Time{}
		backend.setFacts(invalidCapture)
		partial, invalidFailure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Trusted})
		requireAdminV1Code(t, invalidFailure, AdminErrorCodeV1AdminBoundaryUnavailable)
		if !reflect.DeepEqual(partial, AdminSnapshotV1{}) {
			t.Fatalf("invalid capture time returned partial snapshot %#v", partial)
		}
	})
	for _, change := range changes {
		t.Run(change.name, func(t *testing.T) {
			clock := newOperatorAdminV1TestClock()
			backend := newOperatorAdminV1TestBackend(base)
			admin := newOperatorAdminV1Reducer(clock.Now, newOperatorAdminV1TestEntropy(), newOperatorAdminV1TestLifecycle(true, true, false), backend)
			first, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Trusted})
			requireAdminV1Success(t, failure)
			changed := cloneOperatorAdminV1TestFacts(base)
			change.change(&changed)
			backend.setFacts(changed)
			second, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Trusted})
			requireAdminV1Success(t, failure)
			if second.StateRevision != first.StateRevision+1 {
				t.Fatalf("%s change revision = %d -> %d, want exactly +1", change.name, first.StateRevision, second.StateRevision)
			}
		})
	}
}

func TestAdminV1ReducerExpiryAndFailedConsumedStateAdvanceRevision(t *testing.T) {
	t.Run("optional discovery deadline does not expire observation handle", func(t *testing.T) {
		clock := newOperatorAdminV1TestClock()
		facts := operatorAdminV1TestFacts()
		facts.window = "closed"
		facts.windowDeadline = time.Time{}
		facts.discovered[0].expiresAt = time.Time{}
		backend := newOperatorAdminV1TestBackend(facts)
		admin := newOperatorAdminV1Reducer(clock.Now, newOperatorAdminV1TestEntropy(), newOperatorAdminV1TestLifecycle(true, true, false), backend)
		snapshot, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Discovered})
		requireAdminV1Success(t, failure)
		observation := adminV1HandleField[ObservationHandleV1](t, snapshot.Discovered[0], "Observation")

		clock.Advance(time.Minute)
		_, selectFailure := admin.Select(context.Background(), SelectRequestV1{
			MutationPreconditionV1: MutationPreconditionV1{IdempotencyKey: "optional-target-deadline", ExpectedStateRevision: snapshot.StateRevision},
			Observation:            observation,
			ExpectedSKI:            operatorAdminV1TestSKI,
		})
		requireAdminV1Success(t, selectFailure)
		if backend.calls("select") != 1 {
			t.Fatalf("optional zero discovery deadline suppressed select effects=%d, want 1", backend.calls("select"))
		}
	})

	t.Run("target expiry caps observation and candidate handles", func(t *testing.T) {
		clock := newOperatorAdminV1TestClock()
		facts := operatorAdminV1TestFacts()
		facts.discovered[0].expiresAt = clock.Now().Add(30 * time.Second)
		facts.candidates[0].expiresAt = clock.Now().Add(30 * time.Second)
		backend := newOperatorAdminV1TestBackend(facts)
		admin := newOperatorAdminV1Reducer(clock.Now, newOperatorAdminV1TestEntropy(), newOperatorAdminV1TestLifecycle(true, true, false), backend)
		first, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Discovered})
		requireAdminV1Success(t, failure)
		observation := adminV1HandleField[ObservationHandleV1](t, first.Discovered[0], "Observation")
		candidateSnapshot, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Candidate})
		requireAdminV1Success(t, failure)
		candidate := adminV1HandleField[CandidateHandleV1](t, candidateSnapshot.Candidates[0], "Candidate")

		clock.Advance(30 * time.Second)
		_, expired := admin.Select(context.Background(), SelectRequestV1{
			MutationPreconditionV1: MutationPreconditionV1{IdempotencyKey: "target-expired", ExpectedStateRevision: first.StateRevision},
			Observation:            observation,
			ExpectedSKI:            operatorAdminV1TestSKI,
		})
		if expired == nil || backend.calls("select") != 0 {
			t.Fatal("observation handle outlived its 30-second discovery target")
		}
		_, expiredCandidate := admin.Cancel(context.Background(), CancelRequestV1{
			MutationPreconditionV1: MutationPreconditionV1{IdempotencyKey: "candidate-target-expired", ExpectedStateRevision: first.StateRevision},
			Candidate:              candidate,
		})
		if expiredCandidate == nil || backend.calls("cancel") != 0 {
			t.Fatal("candidate handle outlived its 30-second candidate target")
		}
	})

	t.Run("async window expiry advances revision without row change", func(t *testing.T) {
		clock := newOperatorAdminV1TestClock()
		facts := operatorAdminV1TestFacts()
		facts.window = "open"
		facts.windowDeadline = clock.Now().Add(time.Minute)
		backend := newOperatorAdminV1TestBackend(facts)
		admin := newOperatorAdminV1Reducer(clock.Now, newOperatorAdminV1TestEntropy(), newOperatorAdminV1TestLifecycle(true, true, false), backend)
		first, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Trusted})
		requireAdminV1Success(t, failure)
		oldPartner := adminV1HandleField[PartnerHandleV1](t, first.Trusted[0], "Partner")

		clock.Advance(time.Minute)
		expiredFacts := cloneOperatorAdminV1TestFacts(facts)
		expiredFacts.window = "closed"
		expiredFacts.windowDeadline = time.Time{}
		backend.setFacts(expiredFacts)
		second, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Trusted})
		requireAdminV1Success(t, failure)
		if second.StateRevision != first.StateRevision+1 || len(second.Trusted) != len(first.Trusted) {
			t.Fatalf("async window expiry snapshot=%#v, want revision +1 with exact rows unchanged", second)
		}
		_, stalePartner := admin.RetryTrusted(context.Background(), RetryTrustedRequestV1{
			MutationPreconditionV1: MutationPreconditionV1{IdempotencyKey: "pre-window-expiry-partner", ExpectedStateRevision: second.StateRevision},
			Partner:                oldPartner,
		})
		if stalePartner == nil || backend.calls("retry") != 0 {
			t.Fatal("async window expiry retained a pre-expiry handle")
		}
	})

	t.Run("select failure consumes internal observation", func(t *testing.T) {
		clock := newOperatorAdminV1TestClock()
		facts := operatorAdminV1TestFacts()
		backend := newOperatorAdminV1TestBackend(facts)
		admin := newOperatorAdminV1Reducer(clock.Now, newOperatorAdminV1TestEntropy(), newOperatorAdminV1TestLifecycle(true, true, false), backend)
		first, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Discovered})
		requireAdminV1Success(t, failure)
		observation := adminV1HandleField[ObservationHandleV1](t, first.Discovered[0], "Observation")
		postFailure := cloneOperatorAdminV1TestFacts(facts)
		postFailure.discovered = nil
		backend.setEffect("select", operatorAdminV1Transition{}, AdminErrorCodeV1UnknownState, postFailure)
		_, selectFailure := admin.Select(context.Background(), SelectRequestV1{
			MutationPreconditionV1: MutationPreconditionV1{IdempotencyKey: "failed-select-consumed", ExpectedStateRevision: first.StateRevision},
			Observation:            observation,
			ExpectedSKI:            operatorAdminV1TestSKI,
		})
		requireAdminV1Code(t, selectFailure, AdminErrorCodeV1UnknownState)
		after, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Discovered})
		requireAdminV1Success(t, failure)
		if after.StateRevision != first.StateRevision+1 || len(after.Discovered) != 0 || backend.calls("select") != 1 {
			t.Fatalf("failed consumed select snapshot=%#v effects=%d, want revision +1 and one effect", after, backend.calls("select"))
		}
	})

	t.Run("connect failure consumes internal selection", func(t *testing.T) {
		clock := newOperatorAdminV1TestClock()
		facts := operatorAdminV1TestFacts()
		backend := newOperatorAdminV1TestBackend(facts)
		admin := newOperatorAdminV1Reducer(clock.Now, newOperatorAdminV1TestEntropy(), newOperatorAdminV1TestLifecycle(true, true, false), backend)
		first, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Discovered})
		requireAdminV1Success(t, failure)
		selection, failure := admin.Select(context.Background(), SelectRequestV1{
			MutationPreconditionV1: MutationPreconditionV1{IdempotencyKey: "selection-for-failed-connect", ExpectedStateRevision: first.StateRevision},
			Observation:            adminV1HandleField[ObservationHandleV1](t, first.Discovered[0], "Observation"),
			ExpectedSKI:            operatorAdminV1TestSKI,
		})
		requireAdminV1Success(t, failure)
		postFailure := cloneOperatorAdminV1TestFacts(facts)
		postFailure.status = "connect-attempt-consumed"
		backend.setEffect("connect", operatorAdminV1Transition{}, AdminErrorCodeV1Disconnected, postFailure)
		_, connectFailure := admin.Connect(context.Background(), ConnectRequestV1{
			MutationPreconditionV1: MutationPreconditionV1{IdempotencyKey: "failed-connect-consumed", ExpectedStateRevision: selection.StateRevision},
			Selection:              selection.Selection,
		})
		requireAdminV1Code(t, connectFailure, AdminErrorCodeV1Disconnected)
		after, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Connected})
		requireAdminV1Success(t, failure)
		if after.StateRevision != selection.StateRevision+1 || backend.calls("connect") != 1 {
			t.Fatalf("failed consumed connect revision=%d effects=%d, want %d/1", after.StateRevision, backend.calls("connect"), selection.StateRevision+1)
		}
	})
}

func TestAdminV1ReducerCompletedMutationsRollReplayCapacityWithoutReexecution(t *testing.T) {
	clock := newOperatorAdminV1TestClock()
	backend := newOperatorAdminV1TestBackend(operatorAdminV1TestFacts())
	admin := newOperatorAdminV1Reducer(clock.Now, newOperatorAdminV1TestEntropy(), newOperatorAdminV1TestLifecycle(true, true, false), backend)
	first, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Trusted})
	requireAdminV1Success(t, failure)
	revision := first.StateRevision
	var liveRequest ClosePairingWindowRequestV1
	var liveResult AdminMutationResultV1
	for index := 0; index < 160; index++ {
		request := ClosePairingWindowRequestV1{MutationPreconditionV1: MutationPreconditionV1{
			IdempotencyKey:        fmt.Sprintf("completed-%03d", index),
			ExpectedStateRevision: revision,
		}}
		result, callFailure := admin.ClosePairingWindow(context.Background(), request)
		requireAdminV1Success(t, callFailure)
		if result.StateRevision != revision+1 || result.Replayed {
			t.Fatalf("completed mutation %d result=%#v, want fresh revision %d", index, result, revision+1)
		}
		revision = result.StateRevision
		if index == 140 {
			liveRequest = request
			liveResult = result
		}
	}
	if backend.calls("close") != 160 {
		t.Fatalf("completed mutation effects=%d, want 160", backend.calls("close"))
	}
	replayed, replayFailure := admin.ClosePairingWindow(context.Background(), liveRequest)
	requireAdminV1Success(t, replayFailure)
	if !replayed.Replayed || replayed.StateRevision != liveResult.StateRevision || replayed.Outcome != liveResult.Outcome {
		t.Fatalf("live replay=%#v, want logical terminal result %#v with Replayed=true", replayed, liveResult)
	}
	if backend.calls("close") != 160 {
		t.Fatalf("live replay re-executed effect: calls=%d, want 160", backend.calls("close"))
	}
	_, conflict := admin.OpenPairingWindow(context.Background(), OpenPairingWindowRequestV1{
		MutationPreconditionV1: liveRequest.MutationPreconditionV1,
		Duration:               time.Minute,
	})
	requireAdminV1Code(t, conflict, AdminErrorCodeV1IdempotencyConflict)
	if backend.calls("open") != 0 {
		t.Fatalf("live replay conflict executed open effect %d times", backend.calls("open"))
	}

	failureBackend := newOperatorAdminV1TestBackend(operatorAdminV1TestFacts())
	failureBackend.setEffect("close", operatorAdminV1Transition{}, AdminErrorCodeV1UnknownState, operatorAdminV1TestFacts())
	failureAdmin := newOperatorAdminV1Reducer(
		clock.Now, newOperatorAdminV1TestEntropyFrom(20_000), newOperatorAdminV1TestLifecycle(true, true, false), failureBackend,
	)
	failureSnapshot, snapshotFailure := failureAdmin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Trusted})
	requireAdminV1Success(t, snapshotFailure)
	var liveFailureRequest ClosePairingWindowRequestV1
	for index := 0; index < operatorAdminV1MaximumReplayEntries; index++ {
		request := ClosePairingWindowRequestV1{MutationPreconditionV1: MutationPreconditionV1{
			IdempotencyKey: fmt.Sprintf("terminal-failure-%03d", index), ExpectedStateRevision: failureSnapshot.StateRevision,
		}}
		_, callFailure := failureAdmin.ClosePairingWindow(context.Background(), request)
		requireAdminV1Code(t, callFailure, AdminErrorCodeV1UnknownState)
		if index == 0 {
			liveFailureRequest = request
		}
	}
	if failureBackend.calls("close") != operatorAdminV1MaximumReplayEntries {
		t.Fatalf("terminal failure effects=%d, want %d", failureBackend.calls("close"), operatorAdminV1MaximumReplayEntries)
	}
	_, capacityFailure := failureAdmin.ClosePairingWindow(context.Background(), ClosePairingWindowRequestV1{
		MutationPreconditionV1: MutationPreconditionV1{
			IdempotencyKey: "terminal-failure-overflow", ExpectedStateRevision: failureSnapshot.StateRevision,
		},
	})
	requireAdminV1Code(t, capacityFailure, AdminErrorCodeV1AdminBoundaryUnavailable)
	if failureBackend.calls("close") != operatorAdminV1MaximumReplayEntries {
		t.Fatalf("failure capacity overflow executed effect: calls=%d", failureBackend.calls("close"))
	}
	_, replayedFailure := failureAdmin.ClosePairingWindow(context.Background(), liveFailureRequest)
	requireAdminV1Code(t, replayedFailure, AdminErrorCodeV1UnknownState)
	if failureBackend.calls("close") != operatorAdminV1MaximumReplayEntries {
		t.Fatalf("live terminal failure replay re-executed effect: calls=%d", failureBackend.calls("close"))
	}
}

func TestAdminV1ReducerSelectConnectRetryAndUntrustUseOnlyTypedHandles(t *testing.T) {
	clock := newOperatorAdminV1TestClock()
	backend := newOperatorAdminV1TestBackend(operatorAdminV1TestFacts())
	admin := newOperatorAdminV1Reducer(
		clock.Now,
		newOperatorAdminV1TestEntropy(),
		newOperatorAdminV1TestLifecycle(true, true, false),
		backend,
	)
	discovered, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Discovered})
	requireAdminV1Success(t, failure)
	observation := adminV1HandleField[ObservationHandleV1](t, discovered.Discovered[0], "Observation")

	otherBackend := newOperatorAdminV1TestBackend(operatorAdminV1TestFacts())
	other := newOperatorAdminV1Reducer(clock.Now, newOperatorAdminV1TestEntropyFrom(10_000), newOperatorAdminV1TestLifecycle(true, true, false), otherBackend)
	otherSnapshot, otherFailure := other.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Discovered})
	requireAdminV1Success(t, otherFailure)
	if discovered.StateRevision != 1 || otherSnapshot.StateRevision != 1 {
		t.Fatalf("cross-runtime fixture revisions = %d/%d, want both current at 1", discovered.StateRevision, otherSnapshot.StateRevision)
	}
	crossRuntime := adminV1HandleField[ObservationHandleV1](t, otherSnapshot.Discovered[0], "Observation")
	_, crossFailure := admin.Select(context.Background(), SelectRequestV1{
		MutationPreconditionV1: MutationPreconditionV1{IdempotencyKey: "cross-runtime", ExpectedStateRevision: 1},
		Observation:            crossRuntime,
		ExpectedSKI:            operatorAdminV1TestSKI,
	})
	if crossFailure == nil || backend.calls("select") != 0 {
		t.Fatal("current-revision cross-runtime observation handle reached an effect")
	}

	for index, malformed := range []string{
		strings.ToUpper(operatorAdminV1TestSKI),
		operatorAdminV1TestSKI[:39],
		operatorAdminV1TestSKI + "0",
		strings.Repeat("z", 40),
		" " + operatorAdminV1TestSKI,
	} {
		_, callFailure := admin.Select(context.Background(), SelectRequestV1{
			MutationPreconditionV1: MutationPreconditionV1{IdempotencyKey: "bad-ski-" + string(rune('a'+index)), ExpectedStateRevision: 1},
			Observation:            observation,
			ExpectedSKI:            malformed,
		})
		requireAdminV1Code(t, callFailure, AdminErrorCodeV1InvalidRequest)
	}
	if calls := backend.calls("select"); calls != 0 {
		t.Fatalf("malformed SKI select effects = %d, want 0", calls)
	}

	selectionResult, selectFailure := admin.Select(context.Background(), SelectRequestV1{
		MutationPreconditionV1: MutationPreconditionV1{IdempotencyKey: "select", ExpectedStateRevision: 1},
		Observation:            observation,
		ExpectedSKI:            operatorAdminV1TestSKI,
	})
	requireAdminV1Success(t, selectFailure)
	if selectionResult.StateRevision != 2 || selectionResult.Selection == (SelectionHandleV1{}) {
		t.Fatalf("selection result = %#v, want revision 2 opaque selection", selectionResult)
	}
	if backend.calls("select") != 1 || backend.calls("connect") != 0 {
		t.Fatalf("select/connect effects = %d/%d, want 1/0", backend.calls("select"), backend.calls("connect"))
	}

	_, staleObservation := admin.Select(context.Background(), SelectRequestV1{
		MutationPreconditionV1: MutationPreconditionV1{IdempotencyKey: "stale-observation", ExpectedStateRevision: 2},
		Observation:            observation,
		ExpectedSKI:            operatorAdminV1TestSKI,
	})
	if staleObservation == nil || backend.calls("select") != 1 {
		t.Fatal("stale observation handle reached an effect")
	}

	connectRequest := ConnectRequestV1{
		MutationPreconditionV1: MutationPreconditionV1{IdempotencyKey: "connect", ExpectedStateRevision: 2},
		Selection:              selectionResult.Selection,
	}
	connected, connectFailure := admin.Connect(context.Background(), connectRequest)
	requireAdminV1Success(t, connectFailure)
	if connected.StateRevision != 3 || backend.calls("connect") != 1 || backend.lastReference("connect") != "selection-reference" {
		t.Fatalf("connect result=%#v calls=%d ref=%q", connected, backend.calls("connect"), backend.lastReference("connect"))
	}
	replayed, replayFailure := admin.Connect(context.Background(), connectRequest)
	requireAdminV1Success(t, replayFailure)
	if !replayed.Replayed || replayed.StateRevision != 3 || backend.calls("connect") != 1 {
		t.Fatalf("connect replay=%#v calls=%d, want same terminal result and one dial", replayed, backend.calls("connect"))
	}

	_, zeroSelection := admin.Connect(context.Background(), ConnectRequestV1{
		MutationPreconditionV1: MutationPreconditionV1{IdempotencyKey: "zero-selection", ExpectedStateRevision: 3},
	})
	if zeroSelection == nil || backend.calls("connect") != 1 {
		t.Fatal("zero selection handle reached a transport effect")
	}

	trusted, trustedFailure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Trusted})
	requireAdminV1Success(t, trustedFailure)
	partner := adminV1HandleField[PartnerHandleV1](t, trusted.Trusted[0], "Partner")
	retry, retryFailure := admin.RetryTrusted(context.Background(), RetryTrustedRequestV1{
		MutationPreconditionV1: MutationPreconditionV1{IdempotencyKey: "retry", ExpectedStateRevision: 3},
		Partner:                partner,
	})
	requireAdminV1Success(t, retryFailure)
	if retry.StateRevision != 4 || backend.lastReference("retry") != "partner-reference" {
		t.Fatalf("retry result=%#v ref=%q, want identity-only partner binding", retry, backend.lastReference("retry"))
	}

	trusted, trustedFailure = admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Trusted})
	requireAdminV1Success(t, trustedFailure)
	partner = adminV1HandleField[PartnerHandleV1](t, trusted.Trusted[0], "Partner")
	untrusted, untrustFailure := admin.Untrust(context.Background(), UntrustRequestV1{
		MutationPreconditionV1: MutationPreconditionV1{IdempotencyKey: "untrust", ExpectedStateRevision: 4},
		Partner:                partner,
	})
	requireAdminV1Success(t, untrustFailure)
	if untrusted.StateRevision != 5 || backend.lastReference("untrust") != "partner-reference" {
		t.Fatalf("untrust result=%#v ref=%q, want only internal partner reference", untrusted, backend.lastReference("untrust"))
	}
}

func TestAdminV1ReducerCandidateHandlesBindConfirmAndCancel(t *testing.T) {
	clock := newOperatorAdminV1TestClock()
	backend := newOperatorAdminV1TestBackend(operatorAdminV1TestFacts())
	admin := newOperatorAdminV1Reducer(clock.Now, newOperatorAdminV1TestEntropy(), newOperatorAdminV1TestLifecycle(true, true, false), backend)

	snapshot, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Candidate})
	requireAdminV1Success(t, failure)
	candidate := adminV1HandleField[CandidateHandleV1](t, snapshot.Candidates[0], "Candidate")
	confirmed, confirmFailure := admin.Confirm(context.Background(), ConfirmRequestV1{
		MutationPreconditionV1: MutationPreconditionV1{IdempotencyKey: "confirm", ExpectedStateRevision: 1},
		Candidate:              candidate,
		ExpectedSKI:            operatorAdminV1TestSKI,
	})
	requireAdminV1Success(t, confirmFailure)
	if confirmed.StateRevision != 2 || backend.lastReference("confirm") != "candidate-reference" || backend.lastSKI() != operatorAdminV1TestSKI {
		t.Fatalf("confirm result=%#v ref=%q ski=%q", confirmed, backend.lastReference("confirm"), backend.lastSKI())
	}

	_, stale := admin.Cancel(context.Background(), CancelRequestV1{
		MutationPreconditionV1: MutationPreconditionV1{IdempotencyKey: "stale-candidate", ExpectedStateRevision: 2},
		Candidate:              candidate,
	})
	if stale == nil || backend.calls("cancel") != 0 {
		t.Fatal("stale candidate handle reached cancel")
	}

	snapshot, failure = admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Candidate})
	requireAdminV1Success(t, failure)
	candidate = adminV1HandleField[CandidateHandleV1](t, snapshot.Candidates[0], "Candidate")
	cancelled, cancelFailure := admin.Cancel(context.Background(), CancelRequestV1{
		MutationPreconditionV1: MutationPreconditionV1{IdempotencyKey: "cancel", ExpectedStateRevision: 2},
		Candidate:              candidate,
	})
	requireAdminV1Success(t, cancelFailure)
	if cancelled.StateRevision != 3 || backend.lastReference("cancel") != "candidate-reference" {
		t.Fatalf("cancel result=%#v ref=%q", cancelled, backend.lastReference("cancel"))
	}
}

func TestAdminV1ReducerHandleTTLCapacityAndNoEviction(t *testing.T) {
	if operatorAdminV1MaximumHandleTTL > 2*time.Minute {
		t.Fatalf("handle TTL = %s, want at most 2m", operatorAdminV1MaximumHandleTTL)
	}
	if operatorAdminV1MaximumHandlesPerKind != 128 || operatorAdminV1MaximumHandlesTotal != 512 {
		t.Fatalf("handle capacities = %d/%d, want 128 per kind and 512 total", operatorAdminV1MaximumHandlesPerKind, operatorAdminV1MaximumHandlesTotal)
	}

	clock := newOperatorAdminV1TestClock()
	facts := operatorAdminV1TestFacts()
	facts.discovered = operatorAdminV1TestDiscoveredFacts(128)
	backend := newOperatorAdminV1TestBackend(facts)
	admin := newOperatorAdminV1Reducer(clock.Now, newOperatorAdminV1TestEntropy(), newOperatorAdminV1TestLifecycle(true, true, false), backend)
	snapshot, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Discovered})
	requireAdminV1Success(t, failure)
	if len(snapshot.Discovered) != 128 || snapshot.StateRevision != 1 {
		t.Fatalf("bounded discovered snapshot rows=%d revision=%d, want 128/1", len(snapshot.Discovered), snapshot.StateRevision)
	}
	first := adminV1HandleField[ObservationHandleV1](t, snapshot.Discovered[0], "Observation")

	facts.discovered = operatorAdminV1TestDiscoveredFacts(129)
	backend.setFacts(facts)
	overflow, overflowFailure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Discovered})
	requireAdminV1Code(t, overflowFailure, AdminErrorCodeV1AdminBoundaryUnavailable)
	if !reflect.DeepEqual(overflow, AdminSnapshotV1{}) {
		t.Fatalf("capacity failure returned partial output %#v", overflow)
	}

	facts.discovered = operatorAdminV1TestDiscoveredFacts(128)
	backend.setFacts(facts)
	after, afterFailure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Discovered})
	requireAdminV1Success(t, afterFailure)
	if got := adminV1HandleField[ObservationHandleV1](t, after.Discovered[0], "Observation"); got != first {
		t.Fatal("capacity exhaustion evicted a still-valid handle")
	}

	clock.Advance(2 * time.Minute)
	_, expired := admin.Select(context.Background(), SelectRequestV1{
		MutationPreconditionV1: MutationPreconditionV1{IdempotencyKey: "expired", ExpectedStateRevision: 1},
		Observation:            first,
		ExpectedSKI:            operatorAdminV1TestSKI,
	})
	if expired == nil || backend.calls("select") != 0 {
		t.Fatal("two-minute observation handle remained usable")
	}
}

const operatorAdminV1TestSKI = "0123456789abcdef0123456789abcdef01234567"

type operatorAdminV1TestClock struct {
	mu  sync.Mutex
	now time.Time
}

func newOperatorAdminV1TestClock() *operatorAdminV1TestClock {
	return &operatorAdminV1TestClock{now: time.Unix(2_000_000_000, 0)}
}

func (clock *operatorAdminV1TestClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *operatorAdminV1TestClock) Advance(delta time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(delta)
	clock.mu.Unlock()
}

type operatorAdminV1TestEntropy struct {
	mu   sync.Mutex
	next uint64
}

func newOperatorAdminV1TestEntropy() *operatorAdminV1TestEntropy {
	return newOperatorAdminV1TestEntropyFrom(1)
}

func newOperatorAdminV1TestEntropyFrom(next uint64) *operatorAdminV1TestEntropy {
	return &operatorAdminV1TestEntropy{next: next}
}

func (entropy *operatorAdminV1TestEntropy) Read(payload []byte) (int, error) {
	entropy.mu.Lock()
	defer entropy.mu.Unlock()
	for offset := 0; offset < len(payload); offset += 8 {
		var block [8]byte
		binary.LittleEndian.PutUint64(block[:], entropy.next)
		entropy.next++
		copy(payload[offset:], block[:])
	}
	return len(payload), nil
}

type operatorAdminV1TestLifecycle struct {
	mu       sync.Mutex
	enabled  bool
	started  bool
	shutdown bool
}

func newOperatorAdminV1TestLifecycle(enabled, started, shutdown bool) *operatorAdminV1TestLifecycle {
	return &operatorAdminV1TestLifecycle{enabled: enabled, started: started, shutdown: shutdown}
}

func (lifecycle *operatorAdminV1TestLifecycle) operatorAdminV1Lifecycle() (bool, bool, bool) {
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	return lifecycle.enabled, lifecycle.started, lifecycle.shutdown
}

func (lifecycle *operatorAdminV1TestLifecycle) set(enabled, started, shutdown bool) {
	lifecycle.mu.Lock()
	lifecycle.enabled = enabled
	lifecycle.started = started
	lifecycle.shutdown = shutdown
	lifecycle.mu.Unlock()
}

type operatorAdminV1TestBackend struct {
	mu         sync.Mutex
	facts      operatorAdminV1SnapshotFacts
	callsByOp  map[string]int
	references map[string]string
	lastSKIArg string
	blockOp    string
	entered    chan struct{}
	release    chan struct{}
	enterOnce  sync.Once
	effects    map[string]operatorAdminV1TestEffect
}

type operatorAdminV1TestEffect struct {
	transition operatorAdminV1Transition
	failure    AdminErrorCodeV1
	facts      operatorAdminV1SnapshotFacts
}

func newOperatorAdminV1TestBackend(facts operatorAdminV1SnapshotFacts) *operatorAdminV1TestBackend {
	return &operatorAdminV1TestBackend{
		facts:      cloneOperatorAdminV1TestFacts(facts),
		callsByOp:  make(map[string]int),
		references: make(map[string]string),
		effects:    make(map[string]operatorAdminV1TestEffect),
	}
}

func (backend *operatorAdminV1TestBackend) snapshotOperatorAdminV1(context.Context) (operatorAdminV1SnapshotFacts, *AdminErrorV1) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return cloneOperatorAdminV1TestFacts(backend.facts), nil
}

func (backend *operatorAdminV1TestBackend) openOperatorAdminV1(context.Context, time.Duration) (operatorAdminV1Transition, *AdminErrorV1) {
	return backend.effect("open", "")
}

func (backend *operatorAdminV1TestBackend) closeOperatorAdminV1(context.Context) (operatorAdminV1Transition, *AdminErrorV1) {
	return backend.effect("close", "")
}

func (backend *operatorAdminV1TestBackend) selectOperatorAdminV1(_ context.Context, reference, expectedSKI string) (string, operatorAdminV1Transition, *AdminErrorV1) {
	backend.mu.Lock()
	backend.lastSKIArg = expectedSKI
	backend.mu.Unlock()
	transition, failure := backend.effect("select", reference)
	return "selection-reference", transition, failure
}

func (backend *operatorAdminV1TestBackend) connectOperatorAdminV1(_ context.Context, reference string) (operatorAdminV1Transition, *AdminErrorV1) {
	return backend.effect("connect", reference)
}

func (backend *operatorAdminV1TestBackend) connectOperatorAdminV1WithPIN(_ context.Context, reference string, provider operatorAdminV1PINProvider) (operatorAdminV1Transition, *AdminErrorV1) {
	if provider == nil {
		return operatorAdminV1Transition{}, newAdminBoundaryUnavailableV1()
	}
	return backend.effect("connect_pin", reference)
}

func (backend *operatorAdminV1TestBackend) confirmOperatorAdminV1(_ context.Context, reference, expectedSKI string) (operatorAdminV1Transition, *AdminErrorV1) {
	backend.mu.Lock()
	backend.lastSKIArg = expectedSKI
	backend.mu.Unlock()
	return backend.effect("confirm", reference)
}

func (backend *operatorAdminV1TestBackend) cancelOperatorAdminV1(_ context.Context, reference string) (operatorAdminV1Transition, *AdminErrorV1) {
	return backend.effect("cancel", reference)
}

func (backend *operatorAdminV1TestBackend) retryTrustedOperatorAdminV1(_ context.Context, reference string) (operatorAdminV1Transition, *AdminErrorV1) {
	return backend.effect("retry", reference)
}

func (backend *operatorAdminV1TestBackend) untrustOperatorAdminV1(_ context.Context, reference string) (operatorAdminV1Transition, *AdminErrorV1) {
	return backend.effect("untrust", reference)
}

func (backend *operatorAdminV1TestBackend) effect(operation, reference string) (operatorAdminV1Transition, *AdminErrorV1) {
	backend.mu.Lock()
	backend.callsByOp[operation]++
	backend.references[operation] = reference
	blocked := backend.blockOp == operation
	entered := backend.entered
	release := backend.release
	override, overridden := backend.effects[operation]
	if overridden {
		backend.facts = cloneOperatorAdminV1TestFacts(override.facts)
	}
	backend.mu.Unlock()
	if blocked {
		backend.enterOnce.Do(func() { close(entered) })
		<-release
	}
	if overridden {
		if override.failure != "" {
			return override.transition, &AdminErrorV1{Code: override.failure}
		}
		return override.transition, nil
	}
	return operatorAdminV1Transition{outcome: AdminOutcomeV1(operation + "_complete"), changed: true}, nil
}

func (backend *operatorAdminV1TestBackend) setEffect(
	operation string,
	transition operatorAdminV1Transition,
	failure AdminErrorCodeV1,
	facts operatorAdminV1SnapshotFacts,
) {
	backend.mu.Lock()
	backend.effects[operation] = operatorAdminV1TestEffect{
		transition: transition,
		failure:    failure,
		facts:      cloneOperatorAdminV1TestFacts(facts),
	}
	backend.mu.Unlock()
}

func (backend *operatorAdminV1TestBackend) block(operation string) (<-chan struct{}, chan struct{}) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.blockOp = operation
	backend.entered = make(chan struct{})
	backend.release = make(chan struct{})
	backend.enterOnce = sync.Once{}
	return backend.entered, backend.release
}

func (backend *operatorAdminV1TestBackend) calls(operation string) int {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.callsByOp[operation]
}

func (backend *operatorAdminV1TestBackend) lastReference(operation string) string {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.references[operation]
}

func (backend *operatorAdminV1TestBackend) lastSKI() string {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.lastSKIArg
}

func (backend *operatorAdminV1TestBackend) setFacts(facts operatorAdminV1SnapshotFacts) {
	backend.mu.Lock()
	backend.facts = cloneOperatorAdminV1TestFacts(facts)
	backend.mu.Unlock()
}

func operatorAdminV1TestFacts() operatorAdminV1SnapshotFacts {
	return operatorAdminV1SnapshotFacts{
		capturedAt:     time.Unix(2_000_000_000, 0),
		localSKI:       operatorAdminV1TestSKI,
		localSHIPID:    "HLS-operator-admin-v1-test",
		status:         "ready",
		window:         "open",
		windowDeadline: time.Unix(2_000_000_000, 0).Add(time.Minute),
		register:       "enabled",
		listener:       "ready",
		discovery:      "ready",
		trusted: []operatorAdminV1TrustedFact{{
			reference: "partner-reference", ski: operatorAdminV1TestSKI, trustState: "trusted", connectionState: "idle",
			name: "owner peer", identifier: "owner-id", brand: "owner-brand", typeName: "owner-type", model: "owner-model",
		}},
		connected: []operatorAdminV1ConnectedFact{{
			ski: operatorAdminV1TestSKI, endpoint: "192.0.2.10:4712", trustState: "trusted",
			connectionState: "connected", shipID: "ship-id-connected",
		}},
		discovered: []operatorAdminV1DiscoveredFact{{
			reference: "observation-reference", ski: operatorAdminV1TestSKI, endpoint: "192.0.2.20:4712",
			observationRevision: 1, lastSeen: time.Unix(2_000_000_000, 0), name: "owner peer",
			identifier: "owner-id", brand: "owner-brand", typeName: "owner-type", model: "owner-model",
			expiresAt: time.Unix(2_000_000_000, 0).Add(time.Minute),
		}},
		candidates: []operatorAdminV1CandidateFact{{
			reference: "candidate-reference", ski: operatorAdminV1TestSKI,
			state: "association_complete", expiresAt: time.Unix(2_000_000_000, 0).Add(time.Minute), associationComplete: true,
		}},
	}
}

func operatorAdminV1TestDiscoveredFacts(count int) []operatorAdminV1DiscoveredFact {
	result := make([]operatorAdminV1DiscoveredFact, count)
	for index := range result {
		result[index] = operatorAdminV1DiscoveredFact{
			reference:           "observation-" + strings.Repeat("x", index/26) + string(rune('a'+index%26)),
			ski:                 operatorAdminV1TestSKI,
			endpoint:            "192.0.2.20:4712",
			observationRevision: uint64(index + 1),
			lastSeen:            time.Unix(2_000_000_000, 0),
			expiresAt:           time.Unix(2_000_000_000, 0).Add(time.Minute),
		}
	}
	return result
}

func adminV1HandleTokenHex[T any](t *testing.T, handle T) string {
	t.Helper()
	value := reflect.ValueOf(handle)
	if value.Kind() != reflect.Struct || value.NumField() != 1 {
		t.Fatalf("%T is not a sealed one-field handle", handle)
	}
	token := value.Field(0)
	if token.Kind() != reflect.Array || token.Type().Elem().Kind() != reflect.Uint8 {
		t.Fatalf("%T token field has type %s, want byte array", handle, token.Type())
	}
	raw := make([]byte, token.Len())
	for index := range raw {
		raw[index] = byte(token.Index(index).Uint())
	}
	if len(raw) == 0 {
		t.Fatalf("%T has an empty token", handle)
	}
	return hex.EncodeToString(raw)
}

func requireAdminV1GenericRenderingRedacted(t *testing.T, value any, secrets []string) {
	t.Helper()
	stringer, ok := value.(fmt.Stringer)
	if !ok {
		t.Fatalf("%T lacks fail-closed String", value)
	}
	goStringer, ok := value.(fmt.GoStringer)
	if !ok {
		t.Fatalf("%T lacks fail-closed GoString", value)
	}
	renderings := []string{stringer.String(), goStringer.GoString()}
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		renderings = append(renderings,
			fmt.Sprintf(format, value),
			fmt.Sprintf(format, []any{value}),
			fmt.Sprintf(format, struct{ Value any }{Value: value}),
			fmt.Sprintf(format, map[string]any{"value": value}),
		)
	}
	for _, rendered := range renderings {
		lower := strings.ToLower(rendered)
		if !strings.Contains(lower, "redacted") {
			t.Fatalf("%T generic rendering %q is not explicitly fail-closed", value, rendered)
		}
		for _, secret := range secrets {
			if secret != "" && strings.Contains(lower, strings.ToLower(secret)) {
				t.Fatalf("%T generic rendering leaked %q in %q", value, secret, rendered)
			}
		}
	}
	for _, jsonValue := range []any{value, []any{value}, struct{ Value any }{Value: value}, map[string]any{"value": value}} {
		payload, err := json.Marshal(jsonValue)
		if err == nil {
			t.Fatalf("%T generic JSON unexpectedly succeeded with %s", value, payload)
		}
		for _, secret := range secrets {
			if secret != "" && strings.Contains(strings.ToLower(string(payload)), strings.ToLower(secret)) {
				t.Fatalf("%T failed JSON leaked %q in %s", value, secret, payload)
			}
		}
	}
}

func cloneOperatorAdminV1TestFacts(source operatorAdminV1SnapshotFacts) operatorAdminV1SnapshotFacts {
	result := source
	result.trusted = append([]operatorAdminV1TrustedFact(nil), source.trusted...)
	result.connected = append([]operatorAdminV1ConnectedFact(nil), source.connected...)
	result.discovered = append([]operatorAdminV1DiscoveredFact(nil), source.discovered...)
	result.candidates = append([]operatorAdminV1CandidateFact(nil), source.candidates...)
	return result
}

func requireAdminV1Success(t *testing.T, failure *AdminErrorV1) {
	t.Helper()
	if failure != nil {
		t.Fatalf("unexpected AdminV1 failure: %v", failure)
	}
}

func requireAdminV1Code(t *testing.T, failure *AdminErrorV1, want AdminErrorCodeV1) {
	t.Helper()
	if failure == nil || failure.Code != want {
		t.Fatalf("AdminV1 failure = %#v, want code %q", failure, want)
	}
}

func assertOperatorAdminV1SnapshotUnavailable(t *testing.T, admin AdminV1, view AdminViewV1) {
	t.Helper()
	snapshot, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: view})
	requireAdminV1Code(t, failure, AdminErrorCodeV1AdminBoundaryUnavailable)
	if !reflect.DeepEqual(snapshot, AdminSnapshotV1{}) {
		t.Fatalf("unavailable AdminV1 returned partial snapshot %#v", snapshot)
	}
}

func assertOperatorAdminV1ViewLengths(t *testing.T, snapshot AdminSnapshotV1, trusted, connected, discovered, candidates int) {
	t.Helper()
	if len(snapshot.Trusted) != trusted || len(snapshot.Connected) != connected || len(snapshot.Discovered) != discovered || len(snapshot.Candidates) != candidates {
		t.Fatalf("snapshot view lengths = %d/%d/%d/%d, want %d/%d/%d/%d", len(snapshot.Trusted), len(snapshot.Connected), len(snapshot.Discovered), len(snapshot.Candidates), trusted, connected, discovered, candidates)
	}
}

func adminV1HandleField[Handle comparable](t *testing.T, row any, name string) Handle {
	t.Helper()
	field := reflect.ValueOf(row).FieldByName(name)
	if !field.IsValid() || !field.CanInterface() {
		t.Fatalf("%T lacks exported %s handle", row, name)
	}
	handle, ok := field.Interface().(Handle)
	if !ok {
		t.Fatalf("%T.%s = %s, want requested handle kind", row, name, field.Type())
	}
	return handle
}

func adminV1StringField(t *testing.T, row any, name string) string {
	t.Helper()
	field := reflect.ValueOf(row).FieldByName(name)
	if !field.IsValid() || field.Kind() != reflect.String {
		t.Fatalf("%T lacks string field %s", row, name)
	}
	return field.String()
}

func adminV1TimeField(t *testing.T, row any, name string) time.Time {
	t.Helper()
	field := reflect.ValueOf(row).FieldByName(name)
	if !field.IsValid() || !field.CanInterface() {
		t.Fatalf("%T lacks exported time field %s", row, name)
	}
	value, ok := field.Interface().(time.Time)
	if !ok {
		t.Fatalf("%T.%s = %s, want time.Time", row, name, field.Type())
	}
	return value
}

func waitOperatorAdminV1Signal(t *testing.T, signal <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

type operatorAdminV1CallResult struct {
	result  AdminMutationResultV1
	failure *AdminErrorV1
}

func waitOperatorAdminV1Result(t *testing.T, results <-chan operatorAdminV1CallResult) operatorAdminV1CallResult {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for serialized AdminV1 result")
		return operatorAdminV1CallResult{}
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
	if name != "Snapshot" {
		assertAdminV1MutationRequestHasNoTransportCoordinates(t, name, method.Type.In(1), map[reflect.Type]bool{})
	}

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

func assertAdminV1MutationRequestHasNoTransportCoordinates(t *testing.T, operation string, request reflect.Type, seen map[reflect.Type]bool) {
	t.Helper()
	for request.Kind() == reflect.Pointer {
		request = request.Elem()
	}
	if request.Kind() != reflect.Struct || seen[request] {
		return
	}
	seen[request] = true

	for index := 0; index < request.NumField(); index++ {
		field := request.Field(index)
		if !field.IsExported() {
			continue
		}
		name := strings.ToLower(field.Name)
		switch name {
		case "endpoint", "host", "port", "address":
			t.Errorf("AdminV1.%s request publishes caller-controlled transport coordinate %s", operation, field.Name)
		}
		assertAdminV1MutationRequestHasNoTransportCoordinates(t, operation, field.Type, seen)
	}
}

func forbiddenAdminV1Fields(t *testing.T, view reflect.Type) {
	t.Helper()
	forbidden := []string{
		"candidate_ref", "candidate-ref", "candidate ref",
		"nonce", "generation", "store", "association", "control", "manifest",
		"path", "private", "pem", "token",
		"key bytes", "key_bytes", "key-bytes",
	}
	for index := 0; index < view.NumField(); index++ {
		field := view.Field(index)
		if view == reflect.TypeOf(CandidateV1{}) && field.Name == "AssociationComplete" && field.Type.Kind() == reflect.Bool {
			continue
		}
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
