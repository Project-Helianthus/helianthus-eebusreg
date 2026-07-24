package eebusfacade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	eebusapi "github.com/Project-Helianthus/helianthus-eebus-go/api"
	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
)

const (
	issue56SKIA = "1111111111111111111111111111111111111111"
	issue56SKIB = "2222222222222222222222222222222222222222"
)

type issue56Fixture struct {
	base        msp04bFixture
	coordinator *firstTrustCoordinator
	facade      *firstTrustFacade
	service     *issue56CandidateService
	handler     *firstTrustAdminHandler
}

type issue56CandidateService struct {
	msp04bServiceSpy

	queueMu    sync.Mutex
	queueCalls [][2]string
	queue      func(string, string) error
}

func (service *issue56CandidateService) QueuePairingCandidate(candidateRef, expectedSKI string) error {
	service.queueMu.Lock()
	service.queueCalls = append(service.queueCalls, [2]string{candidateRef, expectedSKI})
	hook := service.queue
	service.queueMu.Unlock()
	if hook != nil {
		return hook(candidateRef, expectedSKI)
	}
	return nil
}

func (service *issue56CandidateService) queueSnapshot() [][2]string {
	service.queueMu.Lock()
	defer service.queueMu.Unlock()
	return append([][2]string(nil), service.queueCalls...)
}

func (service *issue56CandidateService) registerCount() int {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.registers
}

func TestIssue56CandidateSnapshotsReplaceWithdrawAndDiscardClaims(t *testing.T) {
	fixture := newIssue56Fixture(t)
	first := fixture.base.clock.Now()
	fixture.facade.VisiblePairingCandidatesUpdated(nil, []shipapi.PairingCandidateRef{
		{
			CandidateRef: "candidate-b",
			SKI:          issue56SKIB,
			Name:         "secret-name",
			Identifier:   "secret-identifier",
			Brand:        "secret-brand",
			Type:         "secret-type",
			Model:        "secret-model",
		},
		{CandidateRef: "candidate-a", SKI: issue56SKIA},
	})

	reply := issue56Admin(t, fixture.handler, map[string]any{"version": 1, "command": "candidates"})
	if issue56String(t, reply, "outcome") != "candidates" || issue56String(t, reply, "snapshot_state") != "valid" {
		t.Fatalf("initial candidates reply = %s", issue56JSON(t, reply))
	}
	if got := issue56Uint(t, reply, "revision"); got != 1 {
		t.Fatalf("initial revision = %d, want 1", got)
	}
	candidates := issue56Candidates(t, reply)
	if len(candidates) != 2 || issue56String(t, candidates[0], "candidate_ref") != "candidate-a" || issue56String(t, candidates[1], "candidate_ref") != "candidate-b" {
		t.Fatalf("candidate order = %s", issue56JSON(t, candidates))
	}
	for _, candidate := range candidates {
		if issue56String(t, candidate, "lifecycle") != "visible" ||
			issue56String(t, candidate, "first_received_at") != first.Format(time.RFC3339Nano) ||
			issue56String(t, candidate, "last_received_at") != first.Format(time.RFC3339Nano) ||
			issue56Uint(t, candidate, "revision") != 1 {
			t.Fatalf("initial candidate timestamps = %s", issue56JSON(t, candidate))
		}
	}
	encoded := string(fixture.handler.handle(context.Background(), []byte(`{"version":1,"command":"candidates"}`)))
	for _, secret := range []string{issue56SKIA, issue56SKIB, "secret-name", "secret-identifier", "secret-brand", "secret-type", "secret-model"} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("candidate admin reply leaked discarded claim %q: %s", secret, encoded)
		}
	}

	issue56Advance(fixture.base.clock, time.Second)
	second := fixture.base.clock.Now()
	fixture.facade.VisiblePairingCandidatesUpdated(nil, []shipapi.PairingCandidateRef{
		{CandidateRef: "candidate-c", SKI: issue56SKIA},
		{CandidateRef: "candidate-b", SKI: issue56SKIB},
	})
	reply = issue56Admin(t, fixture.handler, map[string]any{"version": 1, "command": "candidates"})
	candidates = issue56Candidates(t, reply)
	if len(candidates) != 2 || issue56String(t, candidates[0], "candidate_ref") != "candidate-b" || issue56String(t, candidates[1], "candidate_ref") != "candidate-c" {
		t.Fatalf("replacement candidates = %s", issue56JSON(t, candidates))
	}
	if issue56String(t, candidates[0], "first_received_at") != first.Format(time.RFC3339Nano) ||
		issue56String(t, candidates[0], "last_received_at") != second.Format(time.RFC3339Nano) ||
		issue56String(t, candidates[1], "first_received_at") != second.Format(time.RFC3339Nano) ||
		issue56Uint(t, reply, "revision") != 2 {
		t.Fatalf("replacement timestamps/revision = %s", issue56JSON(t, reply))
	}

	fixture.facade.VisiblePairingCandidatesUpdated(nil, nil)
	reply = issue56Admin(t, fixture.handler, map[string]any{"version": 1, "command": "candidates"})
	if issue56String(t, reply, "snapshot_state") != "empty" || len(issue56Candidates(t, reply)) != 0 || issue56Uint(t, reply, "revision") != 3 {
		t.Fatalf("withdrawal reply = %s", issue56JSON(t, reply))
	}
}

func TestIssue56MalformedCandidateSnapshotsAtomicallyClearSelection(t *testing.T) {
	overflow := make([]shipapi.PairingCandidateRef, 129)
	for index := range overflow {
		overflow[index] = shipapi.PairingCandidateRef{
			CandidateRef: fmt.Sprintf("candidate-%03d", index),
			SKI:          fmt.Sprintf("%040x", index+1),
		}
	}
	tests := []struct {
		name       string
		candidates []shipapi.PairingCandidateRef
		reason     string
	}{
		{name: "overflow", candidates: overflow, reason: "candidate_snapshot_overflow"},
		{name: "empty ref", candidates: []shipapi.PairingCandidateRef{{SKI: issue56SKIA}}, reason: "invalid_candidate_ref"},
		{name: "long ref", candidates: []shipapi.PairingCandidateRef{{CandidateRef: strings.Repeat("x", 129), SKI: issue56SKIA}}, reason: "invalid_candidate_ref"},
		{name: "uppercase ski", candidates: []shipapi.PairingCandidateRef{{CandidateRef: "candidate-a", SKI: strings.ToUpper(issue56SKIA)}}, reason: "invalid_claimed_ski"},
		{name: "short ski", candidates: []shipapi.PairingCandidateRef{{CandidateRef: "candidate-a", SKI: "11"}}, reason: "invalid_claimed_ski"},
		{name: "duplicate ref", candidates: []shipapi.PairingCandidateRef{
			{CandidateRef: "candidate-a", SKI: issue56SKIA},
			{CandidateRef: "candidate-a", SKI: issue56SKIB},
		}, reason: "duplicate_candidate_ref"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newIssue56Fixture(t)
			fixture.facade.VisiblePairingCandidatesUpdated(nil, []shipapi.PairingCandidateRef{{CandidateRef: "seed", SKI: issue56SKIA}})
			fixture.facade.VisiblePairingCandidatesUpdated(nil, test.candidates)
			reply := issue56Admin(t, fixture.handler, map[string]any{"version": 1, "command": "candidates"})
			if issue56String(t, reply, "snapshot_state") != "invalid" ||
				issue56String(t, reply, "invalid_reason") != test.reason ||
				len(issue56Candidates(t, reply)) != 0 ||
				issue56Uint(t, reply, "revision") != 2 {
				t.Fatalf("invalid snapshot reply = %s", issue56JSON(t, reply))
			}
			selectReply := issue56Admin(t, fixture.handler, issue56SelectCommand("select-invalid", "seed", issue56SKIA))
			if issue56String(t, selectReply, "outcome") != "candidate_snapshot_invalid" {
				t.Fatalf("selection after invalid snapshot = %s", issue56JSON(t, selectReply))
			}
			if got := len(fixture.service.queueSnapshot()); got != 0 {
				t.Fatalf("queue calls after invalid snapshot = %d", got)
			}
		})
	}
}

func TestIssue56CandidatesReplyIsBoundedAndDeterministic(t *testing.T) {
	fixture := newIssue56Fixture(t)
	candidates := make([]shipapi.PairingCandidateRef, 128)
	for index := range candidates {
		ref := strings.Repeat("x", 122) + fmt.Sprintf("%06d", index)
		candidates[127-index] = shipapi.PairingCandidateRef{
			CandidateRef: ref,
			SKI:          fmt.Sprintf("%040x", index+1),
		}
	}
	fixture.facade.VisiblePairingCandidatesUpdated(nil, candidates)
	payload := fixture.handler.handle(context.Background(), []byte(`{"version":1,"command":"candidates"}`))
	if len(payload) >= 64<<10 {
		t.Fatalf("bounded candidate reply size = %d, want below admin frame maximum", len(payload))
	}
	var reply map[string]json.RawMessage
	if err := json.Unmarshal(payload, &reply); err != nil {
		t.Fatal(err)
	}
	refs := issue56Candidates(t, reply)
	if len(refs) != 128 {
		t.Fatalf("candidate count = %d, want 128", len(refs))
	}
	for index := 1; index < len(refs); index++ {
		if issue56String(t, refs[index-1], "candidate_ref") >= issue56String(t, refs[index], "candidate_ref") {
			t.Fatal("candidate reply is not candidate_ref sorted")
		}
	}
}

func TestIssue56SelectCandidateCommandIsStrictIdempotentAndEndpointFree(t *testing.T) {
	fixture := newIssue56Fixture(t)
	fixture.facade.VisiblePairingCandidatesUpdated(nil, []shipapi.PairingCandidateRef{{CandidateRef: "candidate-a", SKI: issue56SKIA}})

	for _, command := range []map[string]any{
		{"version": 1, "command": "select_candidate", "idempotency_key": "unknown", "candidate_ref": "candidate-a", "expected_ski": issue56SKIA, "host": "peer.local"},
		{"version": 1, "command": "select_candidate", "idempotency_key": "unknown", "candidate_ref": "candidate-a", "expected_ski": issue56SKIA, "port": 4712},
		{"version": 1, "command": "select_candidate", "idempotency_key": "unknown", "candidate_ref": "candidate-a", "expected_ski": issue56SKIA, "path": "/ship/"},
		{"version": 1, "command": "select_candidate", "idempotency_key": "unknown", "candidate_ref": "candidate-a", "expected_ski": strings.ToUpper(issue56SKIA)},
	} {
		reply := issue56Admin(t, fixture.handler, command)
		if issue56String(t, reply, "outcome") != "invalid_command" {
			t.Fatalf("non-strict select command accepted: %s", issue56JSON(t, reply))
		}
	}

	command := issue56SelectCommand("select-a", "candidate-a", issue56SKIA)
	reply := issue56Admin(t, fixture.handler, command)
	if issue56String(t, reply, "outcome") != "candidate_queued" {
		t.Fatalf("select outcome = %s", issue56JSON(t, reply))
	}
	replay := issue56Admin(t, fixture.handler, command)
	if issue56String(t, replay, "outcome") != "candidate_queued" {
		t.Fatalf("select replay = %s", issue56JSON(t, replay))
	}
	if calls := fixture.service.queueSnapshot(); !reflect.DeepEqual(calls, [][2]string{{"candidate-a", issue56SKIA}}) {
		t.Fatalf("queue calls = %#v", calls)
	}
	assertMSP04BCommitCount(t, fixture.base.store, 0)
	if got := fixture.service.registerCount(); got != 0 {
		t.Fatalf("RegisterRemoteSKI count before confirm = %d", got)
	}

	conflict := issue56Admin(t, fixture.handler, issue56SelectCommand("select-a", "candidate-a", issue56SKIB))
	if issue56String(t, conflict, "outcome") != "idempotency_conflict" {
		t.Fatalf("select idempotency conflict = %s", issue56JSON(t, conflict))
	}
	consumed := issue56Admin(t, fixture.handler, issue56SelectCommand("select-b", "candidate-a", issue56SKIA))
	if issue56String(t, consumed, "outcome") != "candidate_consumed" {
		t.Fatalf("consumed select = %s", issue56JSON(t, consumed))
	}
}

func TestIssue56SelectRejectsStaleReplacedWrongAndCompetingCandidates(t *testing.T) {
	fixture := newIssue56Fixture(t)
	fixture.facade.VisiblePairingCandidatesUpdated(nil, []shipapi.PairingCandidateRef{{CandidateRef: "candidate-a", SKI: issue56SKIA}})
	wrong := issue56Admin(t, fixture.handler, issue56SelectCommand("wrong", "candidate-a", issue56SKIB))
	if issue56String(t, wrong, "outcome") != "candidate_ski_mismatch" {
		t.Fatalf("wrong SKI outcome = %s", issue56JSON(t, wrong))
	}
	stale := issue56Admin(t, fixture.handler, issue56SelectCommand("stale", "missing", issue56SKIA))
	if issue56String(t, stale, "outcome") != "candidate_unavailable" {
		t.Fatalf("stale ref outcome = %s", issue56JSON(t, stale))
	}

	fixture.facade.VisiblePairingCandidatesUpdated(nil, []shipapi.PairingCandidateRef{{CandidateRef: "candidate-a", SKI: issue56SKIB}})
	replaced := issue56Admin(t, fixture.handler, issue56SelectCommand("replaced", "candidate-a", issue56SKIA))
	if issue56String(t, replaced, "outcome") != "candidate_ski_mismatch" {
		t.Fatalf("replaced candidate outcome = %s", issue56JSON(t, replaced))
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	fixture.service.queue = func(string, string) error {
		close(entered)
		<-release
		return nil
	}
	firstResult := make(chan map[string]json.RawMessage, 1)
	go func() {
		firstResult <- issue56Admin(t, fixture.handler, issue56SelectCommand("winner", "candidate-a", issue56SKIB))
	}()
	waitMSP04BSignal(t, entered, "candidate queue entry")
	loser := issue56Admin(t, fixture.handler, issue56SelectCommand("loser", "candidate-a", issue56SKIB))
	if issue56String(t, loser, "outcome") != "candidate_busy" {
		t.Fatalf("competing selection outcome = %s", issue56JSON(t, loser))
	}
	conflict := issue56Admin(t, fixture.handler, issue56SelectCommand("winner", "candidate-a", issue56SKIA))
	if issue56String(t, conflict, "outcome") != "idempotency_conflict" {
		t.Fatalf("inflight conflict outcome = %s", issue56JSON(t, conflict))
	}
	close(release)
	select {
	case result := <-firstResult:
		if issue56String(t, result, "outcome") != "candidate_queued" {
			t.Fatalf("winner outcome = %s", issue56JSON(t, result))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("candidate queue did not complete")
	}
}

func TestIssue56QueueErrorsMapDeterministicallyAndNeverTrust(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "unavailable", err: shipapi.ErrPairingCandidateUnavailable, want: "candidate_unavailable"},
		{name: "ski mismatch", err: shipapi.ErrPairingCandidateSKIMismatch, want: "candidate_ski_mismatch"},
		{name: "consumed", err: shipapi.ErrPairingCandidateConsumed, want: "candidate_consumed"},
		{name: "active", err: shipapi.ErrPairingCandidateActive, want: "candidate_active"},
		{name: "gate", err: shipapi.ErrOutgoingAttemptGateRequired, want: "transport_gate_unavailable"},
		{name: "trusted", err: shipapi.ErrRemoteAlreadyTrusted, want: "already_trusted"},
		{name: "generic", err: errors.New("queue unavailable"), want: "candidate_queue_failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newIssue56Fixture(t)
			fixture.service.queue = func(string, string) error { return test.err }
			fixture.facade.VisiblePairingCandidatesUpdated(nil, []shipapi.PairingCandidateRef{{CandidateRef: "candidate-a", SKI: issue56SKIA}})
			reply := issue56Admin(t, fixture.handler, issue56SelectCommand("select", "candidate-a", issue56SKIA))
			if issue56String(t, reply, "outcome") != test.want {
				t.Fatalf("queue mapping = %s, want %q", issue56JSON(t, reply), test.want)
			}
			assertMSP04BCommitCount(t, fixture.base.store, 0)
			if got := fixture.service.registerCount(); got != 0 {
				t.Fatalf("RegisterRemoteSKI count = %d", got)
			}
		})
	}
}

func TestIssue56ReentrantCallbacksRequireTLSAndSHIPIDBeforeDurableConfirm(t *testing.T) {
	fixture := newIssue56Fixture(t)
	fixture.service.queue = func(_ string, expectedSKI string) error {
		fixture.facade.ServiceShipIDUpdate(expectedSKI, "ship-id-a")
		fixture.facade.RemoteSKIConnected(nil, expectedSKI)
		return nil
	}
	fixture.facade.VisiblePairingCandidatesUpdated(nil, []shipapi.PairingCandidateRef{{CandidateRef: "candidate-a", SKI: issue56SKIA}})

	done := make(chan map[string]json.RawMessage, 1)
	go func() {
		done <- issue56Admin(t, fixture.handler, issue56SelectCommand("select", "candidate-a", issue56SKIA))
	}()
	select {
	case reply := <-done:
		if issue56String(t, reply, "outcome") != "candidate_queued" {
			t.Fatalf("reentrant queue outcome = %s", issue56JSON(t, reply))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reentrant QueuePairingCandidate deadlocked")
	}
	assertMSP04BCommitCount(t, fixture.base.store, 0)
	if got := fixture.service.registerCount(); got != 0 {
		t.Fatalf("registration before confirm = %d", got)
	}

	binding := issue56CurrentBinding(t, fixture)
	if got := confirmMSP04B(fixture.coordinator, "confirm", binding); got != "trusted" {
		t.Fatalf("durable confirm outcome = %q", got)
	}
	assertMSP04BCommitCount(t, fixture.base.store, 1)
	if got := fixture.service.registerCount(); got != 1 {
		t.Fatalf("registration after durable confirm = %d, want 1", got)
	}
	if got := confirmMSP04B(fixture.coordinator, "confirm", binding); got != "trusted" {
		t.Fatalf("confirm replay outcome = %q", got)
	}
	assertMSP04BCommitCount(t, fixture.base.store, 1)
	if got := fixture.service.registerCount(); got != 1 {
		t.Fatalf("registration replay count = %d, want 1", got)
	}
}

func TestIssue56TLSBoundAndSHIPIDFencesAreIndependent(t *testing.T) {
	tests := []struct {
		name     string
		callback func(*issue56Fixture)
	}{
		{name: "SHIP ID only", callback: func(fixture *issue56Fixture) {
			fixture.facade.ServiceShipIDUpdate(issue56SKIA, "ship-id-a")
		}},
		{name: "TLS only", callback: func(fixture *issue56Fixture) {
			fixture.facade.RemoteSKIConnected(nil, issue56SKIA)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newIssue56Fixture(t)
			fixture.service.queue = func(string, string) error {
				test.callback(fixture)
				return nil
			}
			fixture.facade.VisiblePairingCandidatesUpdated(nil, []shipapi.PairingCandidateRef{{CandidateRef: "candidate-a", SKI: issue56SKIA}})
			reply := issue56Admin(t, fixture.handler, issue56SelectCommand("select", "candidate-a", issue56SKIA))
			if issue56String(t, reply, "outcome") != "candidate_queued" {
				t.Fatalf("select outcome = %s", issue56JSON(t, reply))
			}
			binding := issue56CurrentBinding(t, fixture)
			if got := confirmMSP04B(fixture.coordinator, "confirm", binding); got != "association_incomplete" {
				t.Fatalf("partial evidence confirm = %q", got)
			}
			assertMSP04BCommitCount(t, fixture.base.store, 0)
			if got := fixture.service.registerCount(); got != 0 {
				t.Fatalf("registration with partial evidence = %d", got)
			}
		})
	}
}

func TestIssue56MdnsChurnAfterQueueDoesNotCancelFrozenAttempt(t *testing.T) {
	fixture := newIssue56Fixture(t)
	fixture.service.queue = func(_ string, expectedSKI string) error {
		fixture.facade.RemoteSKIConnected(nil, expectedSKI)
		fixture.facade.ServiceShipIDUpdate(expectedSKI, "ship-id-a")
		return nil
	}
	fixture.facade.VisiblePairingCandidatesUpdated(nil, []shipapi.PairingCandidateRef{{CandidateRef: "candidate-a", SKI: issue56SKIA}})
	reply := issue56Admin(t, fixture.handler, issue56SelectCommand("select", "candidate-a", issue56SKIA))
	if issue56String(t, reply, "outcome") != "candidate_queued" {
		t.Fatalf("select outcome = %s", issue56JSON(t, reply))
	}
	fixture.facade.VisiblePairingCandidatesUpdated(nil, nil)
	binding := issue56CurrentBinding(t, fixture)
	if got := confirmMSP04B(fixture.coordinator, "confirm", binding); got != "trusted" {
		t.Fatalf("confirm after mDNS withdrawal = %q", got)
	}
	assertMSP04BCommitCount(t, fixture.base.store, 1)
}

func TestIssue56TerminalPathsFailClosed(t *testing.T) {
	tests := []struct {
		name      string
		terminate func(*issue56Fixture, msp04bBindings)
	}{
		{name: "disconnect", terminate: func(fixture *issue56Fixture, _ msp04bBindings) {
			fixture.facade.RemoteSKIDisconnected(nil, issue56SKIA)
		}},
		{name: "pairing error", terminate: func(fixture *issue56Fixture, _ msp04bBindings) {
			fixture.facade.ServicePairingDetailUpdate(issue56SKIA, shipapi.NewConnectionStateDetail(shipapi.ConnectionStateError, errors.New("failed")))
		}},
		{name: "cancel", terminate: func(fixture *issue56Fixture, binding msp04bBindings) {
			if got := fixture.coordinator.cancel(context.Background(), "cancel", binding.nonce, binding.connection, binding.store); got != "cancelled" {
				t.Fatalf("cancel outcome = %q", got)
			}
		}},
		{name: "close", terminate: func(fixture *issue56Fixture, _ msp04bBindings) {
			if got := fixture.coordinator.closePairingWindow(context.Background(), "close"); got != "pairing_closed" {
				t.Fatalf("close outcome = %q", got)
			}
		}},
		{name: "timeout", terminate: func(fixture *issue56Fixture, _ msp04bBindings) {
			issue56Advance(fixture.base.clock, firstTrustMaximumWindow+time.Second)
			_ = fixture.coordinator.state()
		}},
		{name: "shutdown", terminate: func(fixture *issue56Fixture, _ msp04bBindings) {
			if err := fixture.coordinator.shutdown(); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newIssue56Fixture(t)
			fixture.service.queue = func(_ string, expectedSKI string) error {
				fixture.facade.RemoteSKIConnected(nil, expectedSKI)
				fixture.facade.ServiceShipIDUpdate(expectedSKI, "ship-id-a")
				return nil
			}
			fixture.facade.VisiblePairingCandidatesUpdated(nil, []shipapi.PairingCandidateRef{{CandidateRef: "candidate-a", SKI: issue56SKIA}})
			reply := issue56Admin(t, fixture.handler, issue56SelectCommand("select", "candidate-a", issue56SKIA))
			if issue56String(t, reply, "outcome") != "candidate_queued" {
				t.Fatalf("select outcome = %s", issue56JSON(t, reply))
			}
			binding := issue56CurrentBinding(t, fixture)
			test.terminate(fixture, binding)
			if got := confirmMSP04B(fixture.coordinator, "confirm", binding); got == "trusted" {
				t.Fatal("terminal candidate was trusted")
			}
			assertMSP04BCommitCount(t, fixture.base.store, 0)
			if got := fixture.service.registerCount(); got != 0 {
				t.Fatalf("registration after terminal path = %d", got)
			}
		})
	}
}

func TestIssue56CancelledContextAndRestartReconstructNoCandidates(t *testing.T) {
	fixture := newIssue56Fixture(t)
	fixture.facade.VisiblePairingCandidatesUpdated(nil, []shipapi.PairingCandidateRef{{CandidateRef: "candidate-a", SKI: issue56SKIA}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	payload, err := json.Marshal(issue56SelectCommand("select", "candidate-a", issue56SKIA))
	if err != nil {
		t.Fatal(err)
	}
	var reply map[string]json.RawMessage
	if err := json.Unmarshal(fixture.handler.handle(ctx, payload), &reply); err != nil {
		t.Fatal(err)
	}
	if issue56String(t, reply, "outcome") != "request_cancelled" || len(fixture.service.queueSnapshot()) != 0 {
		t.Fatalf("cancelled selection = %s", issue56JSON(t, reply))
	}

	restarted := newFirstTrustCoordinator(fixture.base.clock.Now, nil, fixture.base.store, fixture.base.effects)
	if got := restarted.reopen(context.Background()); got != "pairing_closed" {
		t.Fatalf("restart outcome = %q", got)
	}
	restartedHandler := &firstTrustAdminHandler{coordinator: restarted, random: strings.NewReader(strings.Repeat("x", 64))}
	candidates := issue56Admin(t, restartedHandler, map[string]any{"version": 1, "command": "candidates"})
	if len(issue56Candidates(t, candidates)) != 0 || issue56Uint(t, candidates, "revision") != 0 {
		t.Fatalf("restart reconstructed volatile candidates: %s", issue56JSON(t, candidates))
	}
}

func TestIssue56CandidateCallbackNeverFeedsRuntimeObservation(t *testing.T) {
	handler, err := newRuntimeServiceHandler(RuntimeConfig{}, issue56SKIB, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	published := 0
	handler.setPublisher(func([]byte) { published++ })
	reader := newRuntimeServiceReader(handler)
	fixture := newIssue56Fixture(t)
	if err := reader.attachFirstTrust(fixture.facade); err != nil {
		t.Fatal(err)
	}
	reader.VisiblePairingCandidatesUpdated(nil, []shipapi.PairingCandidateRef{{
		CandidateRef: "candidate-a",
		SKI:          issue56SKIA,
		Name:         "must-not-publish",
	}})
	if published != 0 || len(handler.observations) != 0 {
		t.Fatal("private candidate callback reached runtime observation/publication")
	}
}

func TestIssue56CandidateCapabilityRemainsOptionalAndPrivate(t *testing.T) {
	var _ eebusapi.PairingCandidateReader = (*runtimeServiceReader)(nil)
	var _ eebusapi.PairingCandidateReader = (*firstTrustFacade)(nil)
	if _, leaked := any((*runtimeServiceHandler)(nil)).(eebusapi.PairingCandidateReader); leaked {
		t.Fatal("runtimeServiceHandler implements private candidate reader")
	}
	for _, typ := range []reflect.Type{
		reflect.TypeOf(RuntimeConfig{}),
		reflect.TypeOf(RuntimeRemote{}),
		reflect.TypeOf(runtimeGraphObservation{}),
	} {
		for index := 0; index < typ.NumField(); index++ {
			name := strings.ToLower(typ.Field(index).Name)
			if strings.Contains(name, "candidate") || strings.Contains(name, "endpoint") {
				t.Fatalf("%s leaks candidate/static endpoint field %s", typ, typ.Field(index).Name)
			}
		}
	}
}

func newIssue56Fixture(t *testing.T) *issue56Fixture {
	t.Helper()
	base := newMSP04BFixture(t, "commit_durable")
	coordinator := base.coordinator.(*firstTrustCoordinator)
	service := &issue56CandidateService{}
	facade, err := newFirstTrustFacade(service, coordinator)
	if err != nil {
		t.Fatal(err)
	}
	coordinator.mu.Lock()
	coordinator.effects = facade
	coordinator.mu.Unlock()
	if got := coordinator.openPairingWindow(context.Background(), "open", firstTrustMaximumWindow); got != "open_empty" {
		t.Fatalf("open pairing window = %q", got)
	}
	return &issue56Fixture{
		base:        base,
		coordinator: coordinator,
		facade:      facade,
		service:     service,
		handler:     &firstTrustAdminHandler{coordinator: coordinator, random: strings.NewReader(strings.Repeat("x", 4096))},
	}
}

func issue56CurrentBinding(t *testing.T, fixture *issue56Fixture) msp04bBindings {
	t.Helper()
	fingerprint, nonce, expiresAt, connection, storeGeneration, complete, ok := fixture.coordinator.candidate()
	if !ok || fingerprint != issue56SKIA || !complete {
		t.Fatalf("current candidate incomplete: fingerprint=%q complete=%t ok=%t", fingerprint, complete, ok)
	}
	return msp04bBindings{
		fingerprint: fingerprint,
		nonce:       nonce,
		expiresAt:   expiresAt,
		connection:  connection,
		store:       storeGeneration,
	}
}

func issue56SelectCommand(key, candidateRef, expectedSKI string) map[string]any {
	return map[string]any{
		"version":         1,
		"command":         "select_candidate",
		"idempotency_key": key,
		"candidate_ref":   candidateRef,
		"expected_ski":    expectedSKI,
	}
}

func issue56Admin(t *testing.T, handler *firstTrustAdminHandler, command map[string]any) map[string]json.RawMessage {
	t.Helper()
	return callMSP04BAdmin(t, handler, command)
}

func issue56Candidates(t *testing.T, reply map[string]json.RawMessage) []map[string]json.RawMessage {
	t.Helper()
	var candidates []map[string]json.RawMessage
	if err := json.Unmarshal(reply["candidates"], &candidates); err != nil {
		t.Fatalf("candidate array malformed: %v; reply=%s", err, issue56JSON(t, reply))
	}
	return candidates
}

func issue56String(t *testing.T, fields map[string]json.RawMessage, key string) string {
	t.Helper()
	return adminString(t, fields, key)
}

func issue56Uint(t *testing.T, fields map[string]json.RawMessage, key string) uint64 {
	t.Helper()
	var value uint64
	if err := json.Unmarshal(fields[key], &value); err != nil {
		t.Fatalf("admin uint field %q malformed: %v", key, err)
	}
	return value
}

func issue56JSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func issue56Advance(clock *msp04bClock, duration time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	clock.mu.Unlock()
}
