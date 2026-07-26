package eebusfacade

import (
	"context"
	"testing"
	"time"
)

func TestIssue74TrustedOutboundLeaseCoversBoundedSHIPHandshake(t *testing.T) {
	fixture, coordinator, remote, scope := newMSP04CR2AttemptFixture(t)
	scheduler := &msp04cr2Scheduler{now: fixture.clock.MonotonicNow}
	coordinator.outgoingAttemptSchedule = scheduler.afterFunc
	handle, outcome := coordinator.prepareOutgoingAttempt(
		context.Background(),
		msp04cr2Request(remote, "peer.invalid", 4712, "/ship/"),
	)
	if outcome != "attempt_reserved" || handle == nil {
		t.Fatalf("prepare = %q/%v", outcome, handle)
	}
	if _, outcome := coordinator.authorizeOutgoingAttempt(context.Background(), handle); outcome != "attempt_permitted" {
		t.Fatalf("authorize = %q", outcome)
	}

	fixture.clock.advanceMonotonic(70*time.Second - time.Nanosecond)
	scheduler.runDue()
	if _, ok := soleMSP04CR2Attempt(coordinator); !ok {
		t.Fatal("trusted outbound attempt expired inside the bounded SHIP handshake budget")
	}

	fixture.clock.advanceMonotonic(firstTrustOutgoingAttemptLease - (70*time.Second - time.Nanosecond))
	scheduler.runDue()
	if _, ok := soleMSP04CR2Attempt(coordinator); ok {
		t.Fatal("outbound attempt survived its finite lease")
	}
	state, count, remaining, ok := coordinator.retryState(scope)
	if !ok || state != "BACKOFF_ACTIVE" || count != 1 || remaining != 3*time.Second {
		t.Fatalf("lease expiry retry tuple = %s/%d/%s/%t, want BACKOFF_ACTIVE/1/3s/true", state, count, remaining, ok)
	}
}
