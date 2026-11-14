package eebusfacade

import (
	"bytes"
	"context"
	"math"
	"strings"
	"testing"
)

type outgoingAttemptPrepareStepCase struct {
	name     string
	request  func(remote []byte) firstTrustOutgoingAttemptRequest
	context  func() context.Context
	arrange  func(t *testing.T, fixture *msp04cFixture, coordinator *firstTrustCoordinator, remote []byte, scope [32]byte)
	fallback bool
	advanced bool
	want     string
	verify   func(t *testing.T, fixture *msp04cFixture, coordinator *firstTrustCoordinator, scope [32]byte)
}

func TestOutgoingAttemptPrepareStepsDecideOutcomeFromAdmissionReservationAndCommit(t *testing.T) {
	for _, test := range outgoingAttemptPrepareStepCases() {
		t.Run(test.name, func(t *testing.T) {
			fixture, coordinator, remote, scope := newMSP04CR2AttemptFixture(t)
			request := msp04cr2Request(remote, "peer.invalid", 4712, "/ship/")
			if test.request != nil {
				request = test.request(remote)
			}
			var failed *firstTrustOutgoingAttemptMetadata
			if test.fallback {
				failed = authorizedOutgoingAttemptForFallback(t, coordinator, request)
			}
			if test.arrange != nil {
				test.arrange(t, fixture, coordinator, remote, scope)
			}
			ctx := context.Background()
			if test.context != nil {
				ctx = test.context()
			}
			before := outgoingAttemptPrepareCounters(coordinator, scope)
			unlock := coordinator.lockOutgoingAttemptLane(request.remoteSKI)
			handle, outcome := coordinator.prepareOutgoingAttemptLocked(ctx, request, failed)
			unlock()
			if outcome != test.want || (handle != nil) != (test.want == "attempt_reserved") {
				t.Fatalf("prepare = %q/%v, want %q", outcome, handle, test.want)
			}
			if test.want == "attempt_denied" && !test.fallback {
				assertOutgoingAttemptPrepareDenialUnchanged(t, coordinator, scope, before, test.advanced)
			}
			if test.verify != nil {
				test.verify(t, fixture, coordinator, scope)
			}
		})
	}
}

func outgoingAttemptPrepareStepCases() []outgoingAttemptPrepareStepCase {
	locked := func(change func(coordinator *firstTrustCoordinator, remote []byte, scope [32]byte)) func(
		*testing.T, *msp04cFixture, *firstTrustCoordinator, []byte, [32]byte,
	) {
		return func(_ *testing.T, _ *msp04cFixture, coordinator *firstTrustCoordinator, remote []byte, scope [32]byte) {
			coordinator.mu.Lock()
			defer coordinator.mu.Unlock()
			change(coordinator, remote, scope)
		}
	}
	return []outgoingAttemptPrepareStepCase{
		{name: "canceled context", want: "attempt_denied", context: func() context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}},
		{name: "invalid endpoint", want: "attempt_denied", request: func(remote []byte) firstTrustOutgoingAttemptRequest {
			return msp04cr2Request(remote, "", 4712, "/ship/")
		}},
		{name: "relative path", want: "attempt_denied", request: func(remote []byte) firstTrustOutgoingAttemptRequest {
			return msp04cr2Request(remote, "peer.invalid", 4712, "ship")
		}},
		{name: "no lease", want: "attempt_denied", arrange: locked(func(coordinator *firstTrustCoordinator, _ []byte, _ [32]byte) {
			coordinator.outgoingAttemptLease = 0
		})},
		{name: "no lease scheduler", want: "attempt_denied", arrange: locked(func(coordinator *firstTrustCoordinator, _ []byte, _ [32]byte) {
			coordinator.outgoingAttemptSchedule = nil
		})},
		{name: "control epoch exhausted", want: "attempt_denied", arrange: locked(func(coordinator *firstTrustCoordinator, _ []byte, _ [32]byte) {
			coordinator.controlView.control.controlEpoch = math.MaxUint64
		})},
		{name: "successful owner for remote", want: "attempt_denied", arrange: locked(func(coordinator *firstTrustCoordinator, remote []byte, _ [32]byte) {
			coordinator.outgoingAttemptContexts[[32]byte{0xee}] = firstTrustOutgoingAttemptRuntime{remoteSKI: bytes.Clone(remote), settlement: "success"}
		})},
		{name: "scope already reserved", want: "attempt_denied", arrange: func(t *testing.T, _ *msp04cFixture, coordinator *firstTrustCoordinator, remote []byte, scope [32]byte) {
			if _, outcome := coordinator.prepareOutgoingAttempt(context.Background(), msp04cr2Request(remote, "peer.invalid", 4712, "/ship/")); outcome != "attempt_reserved" {
				t.Fatalf("first prepare = %q", outcome)
			}
			coordinator.mu.Lock()
			delete(coordinator.retryInflight, scope)
			coordinator.mu.Unlock()
		}},
		{name: "owner capacity exhausted", want: "attempt_denied", arrange: locked(func(coordinator *firstTrustCoordinator, _ []byte, _ [32]byte) {
			for index := 0; index < firstTrustMaximumOutgoingAttempts; index++ {
				coordinator.outgoingAttemptContexts[[32]byte{0xf0, byte(index)}] = firstTrustOutgoingAttemptRuntime{}
			}
		})},
		{name: "quarantine on administrative hold", want: "attempt_denied", arrange: locked(func(coordinator *firstTrustCoordinator, _ []byte, scope [32]byte) {
			coordinator.controlView.control.quarantines[0].state = "ADMIN_HOLD"
		})},
		{name: "retry already in flight", want: "attempt_denied", arrange: locked(func(coordinator *firstTrustCoordinator, _ []byte, scope [32]byte) {
			coordinator.retryInflight[scope] = true
		})},
		{name: "random source exhausted", want: "attempt_denied", arrange: locked(func(coordinator *firstTrustCoordinator, _ []byte, _ [32]byte) {
			coordinator.random = strings.NewReader("")
		})},
		{name: "publication ordinal exhausted", want: "attempt_denied", arrange: locked(func(coordinator *firstTrustCoordinator, _ []byte, _ [32]byte) {
			coordinator.random = bytes.NewReader(bytes.Repeat([]byte{0x5a}, 32))
		})},
		{name: "cancellation generation exhausted", want: "attempt_denied", arrange: locked(func(coordinator *firstTrustCoordinator, _ []byte, _ [32]byte) {
			coordinator.outgoingAttemptCancellationGeneration = math.MaxUint64
		})},
		{name: "reservation order exhausted", want: "attempt_denied", arrange: locked(func(coordinator *firstTrustCoordinator, _ []byte, _ [32]byte) {
			coordinator.outgoingAttemptReservationOrder = math.MaxUint64
		})},
		{name: "attempt identifier collides", want: "attempt_denied", arrange: locked(func(coordinator *firstTrustCoordinator, _ []byte, _ [32]byte) {
			collision := [32]byte{0x01, 0x5a}
			coordinator.random = bytes.NewReader(append(collision[:], bytes.Repeat([]byte{0x6b}, 32)...))
			coordinator.controlView.control.attempts = append(coordinator.controlView.control.attempts,
				firstTrustOutgoingAttemptRecord{attemptID: collision, scope: [32]byte{0x99}})
			coordinator.outgoingAttemptContexts[collision] = firstTrustOutgoingAttemptRuntime{}
		})},
		{name: "quarantine table full", want: "attempt_denied", arrange: locked(func(coordinator *firstTrustCoordinator, _ []byte, _ [32]byte) {
			coordinator.controlView.control.quarantines = nil
			for index := 0; index < firstTrustMaximumQuarantineRecords; index++ {
				coordinator.controlView.control.quarantines = append(coordinator.controlView.control.quarantines,
					firstTrustQuarantineRecord{scope: [32]byte{0xa0, byte(index)}, reason: "RETRYABLE_FAILURE", state: "RETRY_READY"})
			}
		})},
		{name: "durable reservation fails", want: "attempt_denied", advanced: true, arrange: func(_ *testing.T, fixture *msp04cFixture, _ *firstTrustCoordinator, _ []byte, _ [32]byte) {
			fixture.store.commitOutcome = "commit_not_published"
		}},
		{name: "reserved with existing quarantine", want: "attempt_reserved", verify: verifyOutgoingAttemptReserved("attempt_prepare", 1)},
		{name: "reserved with new quarantine", want: "attempt_reserved", arrange: locked(func(coordinator *firstTrustCoordinator, _ []byte, _ [32]byte) {
			coordinator.controlView.control.quarantines = nil
		}), verify: verifyOutgoingAttemptReserved("attempt_prepare", 1)},
		{name: "fallback replaces authorized attempt", fallback: true, want: "attempt_reserved", verify: verifyOutgoingAttemptReserved("attempt_fallback_prepare", 1)},
		{name: "fallback at owner capacity releases the failed owner", fallback: true, want: "attempt_reserved", arrange: locked(func(coordinator *firstTrustCoordinator, _ []byte, _ [32]byte) {
			for index := 1; index < firstTrustMaximumOutgoingAttempts; index++ {
				coordinator.outgoingAttemptContexts[[32]byte{0xf0, byte(index)}] = firstTrustOutgoingAttemptRuntime{}
			}
		})},
		{name: "fallback beyond owner capacity", fallback: true, want: "attempt_denied", arrange: locked(func(coordinator *firstTrustCoordinator, _ []byte, _ [32]byte) {
			for index := 0; index < firstTrustMaximumOutgoingAttempts; index++ {
				coordinator.outgoingAttemptContexts[[32]byte{0xf0, byte(index)}] = firstTrustOutgoingAttemptRuntime{}
			}
		})},
		{name: "fallback for unknown metadata", fallback: true, want: "attempt_denied", arrange: locked(func(coordinator *firstTrustCoordinator, _ []byte, _ [32]byte) {
			coordinator.controlView.control.attempts[0].attemptID[0] ^= 0xff
		})},
		{name: "fallback for reserved attempt", fallback: true, want: "attempt_denied", arrange: locked(func(coordinator *firstTrustCoordinator, _ []byte, _ [32]byte) {
			coordinator.controlView.control.attempts[0].state = firstTrustAttemptReserved
		})},
		{name: "fallback without in-flight retry", fallback: true, want: "attempt_denied", arrange: locked(func(coordinator *firstTrustCoordinator, _ []byte, scope [32]byte) {
			delete(coordinator.retryInflight, scope)
		})},
	}
}

type outgoingAttemptPrepareCounterState struct {
	cancellationGeneration uint64
	reservationOrder       uint64
	controlEpoch           uint64
	attempts               int
	contexts               int
	inflight               bool
}

func outgoingAttemptPrepareCounters(coordinator *firstTrustCoordinator, scope [32]byte) outgoingAttemptPrepareCounterState {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return outgoingAttemptPrepareCounterState{
		cancellationGeneration: coordinator.outgoingAttemptCancellationGeneration,
		reservationOrder:       coordinator.outgoingAttemptReservationOrder,
		controlEpoch:           coordinator.controlView.control.controlEpoch,
		attempts:               len(coordinator.controlView.control.attempts),
		contexts:               len(coordinator.outgoingAttemptContexts),
		inflight:               coordinator.retryInflight[scope],
	}
}

func assertOutgoingAttemptPrepareDenialUnchanged(
	t *testing.T,
	coordinator *firstTrustCoordinator,
	scope [32]byte,
	before outgoingAttemptPrepareCounterState,
	advanced bool,
) {
	t.Helper()
	want := before
	if advanced {
		want.cancellationGeneration++
		want.reservationOrder++
	}
	if after := outgoingAttemptPrepareCounters(coordinator, scope); after != want {
		t.Fatalf("denied prepare state = %#v, want %#v", after, want)
	}
}

func authorizedOutgoingAttemptForFallback(
	t *testing.T,
	coordinator *firstTrustCoordinator,
	request firstTrustOutgoingAttemptRequest,
) *firstTrustOutgoingAttemptMetadata {
	t.Helper()
	handle, outcome := coordinator.prepareOutgoingAttempt(context.Background(), request)
	if outcome != "attempt_reserved" || handle == nil {
		t.Fatalf("fallback seed prepare = %q/%v", outcome, handle)
	}
	if _, outcome := coordinator.authorizeOutgoingAttempt(context.Background(), handle); outcome != "attempt_permitted" {
		t.Fatalf("fallback seed authorization = %q", outcome)
	}
	metadata := handle.metadata
	return &metadata
}

func verifyOutgoingAttemptReserved(
	operationClass string,
	wantAttempts int,
) func(*testing.T, *msp04cFixture, *firstTrustCoordinator, [32]byte) {
	return func(t *testing.T, fixture *msp04cFixture, coordinator *firstTrustCoordinator, scope [32]byte) {
		t.Helper()
		if fixture.store.prepared.operationClass != operationClass {
			t.Fatalf("operation class = %q, want %q", fixture.store.prepared.operationClass, operationClass)
		}
		coordinator.mu.Lock()
		defer coordinator.mu.Unlock()
		attempts := coordinator.controlView.control.attempts
		if len(attempts) != wantAttempts || attempts[len(attempts)-1].state != firstTrustAttemptReserved ||
			attempts[len(attempts)-1].scope != scope || !coordinator.retryInflight[scope] ||
			len(coordinator.outgoingAttemptContexts) != wantAttempts {
			t.Fatalf("reservation state = %#v/%d contexts", attempts, len(coordinator.outgoingAttemptContexts))
		}
		if index, quarantine, ok := coordinator.firstTrustQuarantineLocked(scope); index < 0 || !ok || quarantine.state != "RETRY_READY" {
			t.Fatalf("reservation quarantine = %d/%#v/%t, want RETRY_READY", index, quarantine, ok)
		}
	}
}
