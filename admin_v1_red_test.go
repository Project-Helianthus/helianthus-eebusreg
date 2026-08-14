package eebusruntime

import (
	"context"
	"encoding/binary"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
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
	if trusted.StateRevision != 1 || adminV1HandleField[PartnerHandleV1](t, trusted.Trusted[0], "Partner") == (PartnerHandleV1{}) {
		t.Fatalf("trusted snapshot = %#v, want revision 1 with one partner handle", trusted)
	}

	connected, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Connected})
	requireAdminV1Success(t, failure)
	assertOperatorAdminV1ViewLengths(t, connected, 0, 1, 0, 0)
	if connected.StateRevision != 1 || connected.Connected[0].Endpoint != "192.0.2.10:4712" {
		t.Fatalf("connected snapshot = %#v, want only owner endpoint row at revision 1", connected)
	}

	discovered, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Discovered})
	requireAdminV1Success(t, failure)
	assertOperatorAdminV1ViewLengths(t, discovered, 0, 0, 1, 0)
	if discovered.StateRevision != 1 || discovered.Discovered[0].Endpoint != "192.0.2.20:4712" ||
		adminV1HandleField[ObservationHandleV1](t, discovered.Discovered[0], "Observation") == (ObservationHandleV1{}) {
		t.Fatalf("discovered snapshot = %#v, want one owner endpoint observation at revision 1", discovered)
	}

	candidate, failure := admin.Snapshot(context.Background(), AdminSnapshotRequestV1{View: AdminViewV1Candidate})
	requireAdminV1Success(t, failure)
	assertOperatorAdminV1ViewLengths(t, candidate, 0, 0, 0, 1)
	if candidate.StateRevision != 1 || adminV1HandleField[CandidateHandleV1](t, candidate.Candidates[0], "Candidate") == (CandidateHandleV1{}) ||
		adminV1StringField(t, candidate.Candidates[0], "SKI") != operatorAdminV1TestSKI {
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
	}}
	changed.candidates = []operatorAdminV1CandidateFact{{
		reference: "candidate-replacement", ski: operatorAdminV1TestSKI,
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
}

func newOperatorAdminV1TestBackend(facts operatorAdminV1SnapshotFacts) *operatorAdminV1TestBackend {
	return &operatorAdminV1TestBackend{facts: facts, callsByOp: make(map[string]int), references: make(map[string]string)}
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
	backend.mu.Unlock()
	if blocked {
		backend.enterOnce.Do(func() { close(entered) })
		<-release
	}
	return operatorAdminV1Transition{outcome: AdminOutcomeV1(operation + "_complete"), changed: true}, nil
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
		trusted: []operatorAdminV1TrustedFact{{reference: "partner-reference", ski: operatorAdminV1TestSKI}},
		connected: []operatorAdminV1ConnectedFact{{
			ski: operatorAdminV1TestSKI, endpoint: "192.0.2.10:4712",
		}},
		discovered: []operatorAdminV1DiscoveredFact{{
			reference: "observation-reference", ski: operatorAdminV1TestSKI, endpoint: "192.0.2.20:4712",
		}},
		candidates: []operatorAdminV1CandidateFact{{reference: "candidate-reference", ski: operatorAdminV1TestSKI}},
	}
}

func operatorAdminV1TestDiscoveredFacts(count int) []operatorAdminV1DiscoveredFact {
	result := make([]operatorAdminV1DiscoveredFact, count)
	for index := range result {
		result[index] = operatorAdminV1DiscoveredFact{
			reference: "observation-" + strings.Repeat("x", index/26) + string(rune('a'+index%26)),
			ski:       operatorAdminV1TestSKI,
			endpoint:  "192.0.2.20:4712",
		}
	}
	return result
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
