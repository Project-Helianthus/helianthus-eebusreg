package eebusfacade

import (
	"bytes"
	"context"
	"encoding/hex"
	"reflect"
	"testing"
	"time"
)

func TestMSP04CR2PrepareDurablyBindsConcreteAttemptBeforeReturningHandle(t *testing.T) {
	fixture, coordinator, remote, scope := newMSP04CR2AttemptFixture(t)
	request := msp04cr2Request(remote, "peer.invalid", 4712, "/ship/")
	fixture.events.mu.Lock()
	fixture.events.events = nil
	fixture.events.mu.Unlock()

	handle, outcome := coordinator.prepareOutgoingAttempt(context.Background(), request)
	if outcome != "attempt_reserved" || handle == nil {
		t.Fatalf("prepare outcome/handle = %q/%v, want attempt_reserved/non-nil", outcome, handle)
	}
	fixture.events.assertOrdered(t, "anchor_stage", "store_commit", "anchor_finalize")
	if fixture.store.prepared.operationClass != "attempt_prepare" {
		t.Fatalf("reservation operation class = %q, want attempt_prepare", fixture.store.prepared.operationClass)
	}
	metadata := handle.metadata
	if metadata.attemptID == [32]byte{} || metadata.scope != scope || metadata.controlEpoch == 0 || handle.context == nil {
		t.Fatalf("handle binding = %#v, want nonzero id/exact scope/epoch/context", metadata)
	}
	if handle.context.Err() != nil {
		t.Fatal("fresh attempt context is already canceled")
	}

	record, ok := soleMSP04CR2Attempt(coordinator)
	if !ok {
		t.Fatal("durable reservation is absent after Prepare returned")
	}
	if record.state != "ATTEMPT_RESERVED" || record.attemptID != metadata.attemptID ||
		!bytes.Equal(record.remoteSKI, remote) || record.scope != scope || record.controlEpoch != metadata.controlEpoch ||
		record.associationLineage != coordinator.controlView.control.associationLineage || record.endpoint != request.endpoint ||
		record.path != request.path || record.cancellationGeneration == 0 || record.reservationOrder == 0 ||
		record.attemptCountBefore != 0 {
		t.Fatalf("durable reservation does not bind the concrete attempt: %#v", record)
	}
}

func TestMSP04CR2ReserveFailureAndIneligibleStatesReturnNoHandle(t *testing.T) {
	t.Run("durable reserve fails", func(t *testing.T) {
		fixture, coordinator, remote, _ := newMSP04CR2AttemptFixture(t)
		fixture.store.commitOutcome = "commit_not_published"
		handle, outcome := coordinator.prepareOutgoingAttempt(context.Background(), msp04cr2Request(remote, "peer.invalid", 4712, "/ship/"))
		if handle != nil || outcome != "attempt_denied" {
			t.Fatalf("failed reserve returned %q/%v, want attempt_denied/nil", outcome, handle)
		}
		if _, ok := soleMSP04CR2Attempt(coordinator); ok {
			t.Fatal("failed reserve left an active attempt")
		}
	})

	for _, state := range []string{"BACKOFF_ACTIVE", "ADMIN_HOLD"} {
		t.Run(state, func(t *testing.T) {
			_, coordinator, remote, scope := newMSP04CR2AttemptFixture(t)
			coordinator.mu.Lock()
			coordinator.controlView.control.quarantines[0].state = state
			coordinator.controlView.control.quarantines[0].remainingDelay = 3 * time.Second
			if state == "ADMIN_HOLD" {
				coordinator.controlView.control.quarantines[0].remainingDelay = 0
			}
			coordinator.retryArms[scope] = firstTrustRetryArm{armedAt: 20 * time.Second, deadline: 23 * time.Second}
			coordinator.mu.Unlock()
			handle, outcome := coordinator.prepareOutgoingAttempt(context.Background(), msp04cr2Request(remote, "peer.invalid", 4712, "/ship/"))
			if handle != nil || outcome != "attempt_denied" {
				t.Fatalf("%s prepare = %q/%v, want attempt_denied/nil", state, outcome, handle)
			}
		})
	}
}

func TestMSP04CR2PermitIsSingleUseAndFallbackNeedsFreshReservation(t *testing.T) {
	_, coordinator, remote, _ := newMSP04CR2AttemptFixture(t)
	first, outcome := coordinator.prepareOutgoingAttempt(context.Background(), msp04cr2Request(remote, "peer.invalid", 4712, "/ship/"))
	if outcome != "attempt_reserved" || first == nil {
		t.Fatalf("first prepare = %q/%v", outcome, first)
	}
	permit, outcome := coordinator.authorizeOutgoingAttempt(context.Background(), first)
	if outcome != "attempt_permitted" || permit.decision != "PERMIT" || permit.reason != "AUTHORIZED" {
		t.Fatalf("first authorization = %q/%#v", outcome, permit)
	}
	if permit.metadata != first.metadata || !sameMSP04CR2Context(permit.context, first.context) {
		t.Fatal("permit changed attempt metadata or context")
	}
	if record, ok := soleMSP04CR2Attempt(coordinator); !ok || record.state != "ATTEMPT_LAUNCH_AUTHORIZED" {
		t.Fatalf("launch authorization was not durable: %#v/%t", record, ok)
	}

	reused, outcome := coordinator.authorizeOutgoingAttempt(context.Background(), first)
	if outcome != "attempt_denied" || reused.decision != "DENY" || reused.reason != "STALE_HANDLE" {
		t.Fatalf("reused handle authorization = %q/%#v, want stale denial", outcome, reused)
	}
	if got := coordinator.completeOutgoingAttempt(context.Background(), first.metadata, true); got != "attempt_succeeded" {
		t.Fatalf("first terminal success = %q", got)
	}

	second, outcome := coordinator.prepareOutgoingAttempt(context.Background(), msp04cr2Request(remote, "peer.invalid", 4712, ""))
	if outcome != "attempt_reserved" || second == nil {
		t.Fatalf("fallback prepare = %q/%v", outcome, second)
	}
	if second.metadata.attemptID == first.metadata.attemptID || second.cancellationGeneration == first.cancellationGeneration {
		t.Fatal("fallback reused the previous reservation token or cancellation generation")
	}
	if record, ok := soleMSP04CR2Attempt(coordinator); !ok || record.path != "" || record.endpoint.host != "peer.invalid" || record.endpoint.port != 4712 {
		t.Fatalf("fallback reservation binding = %#v/%t", record, ok)
	}
}

func TestMSP04CR2RestartChargesUnresolvedReservationBeforeAnotherAttempt(t *testing.T) {
	for _, test := range []struct {
		name      string
		authorize bool
	}{
		{name: "reserved"},
		{name: "launch authorized", authorize: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, coordinator, remote, scope := newMSP04CR2AttemptFixture(t)
			handle, outcome := coordinator.prepareOutgoingAttempt(context.Background(), msp04cr2Request(remote, "peer.invalid", 4712, "/ship/"))
			if outcome != "attempt_reserved" || handle == nil {
				t.Fatalf("prepare before restart = %q/%v", outcome, handle)
			}
			if test.authorize {
				permit, result := coordinator.authorizeOutgoingAttempt(context.Background(), handle)
				if result != "attempt_permitted" || permit.decision != "PERMIT" {
					t.Fatalf("authorize before restart = %q/%#v", result, permit)
				}
			}

			restarted := fixture.newCoordinator()
			if got := restarted.reopen(context.Background()); got != "pairing_closed" {
				t.Fatalf("restart outcome = %q, want pairing_closed after synthetic failure", got)
			}
			if _, ok := soleMSP04CR2Attempt(restarted); ok {
				t.Fatal("restart retained an unresolved attempt reservation")
			}
			state, count, remaining, ok := restarted.retryState(scope)
			if !ok || state != "BACKOFF_ACTIVE" || count != 1 || remaining != 3*time.Second {
				t.Fatalf("restart retry tuple = %s/%d/%s/%t, want BACKOFF_ACTIVE/1/3s/true", state, count, remaining, ok)
			}
			if fixture.store.prepared.operationClass != "attempt_restart_synthetic_failure" {
				t.Fatalf("restart operation class = %q", fixture.store.prepared.operationClass)
			}
			if next, result := restarted.prepareOutgoingAttempt(context.Background(), msp04cr2Request(remote, "peer.invalid", 4712, "/ship/")); next != nil || result != "attempt_denied" {
				t.Fatalf("restart backoff admitted another attempt: %q/%v", result, next)
			}
		})
	}
}

func TestMSP04CR2StaleCallbackCannotCompleteNewerPath(t *testing.T) {
	fixture, coordinator, remote, _ := newMSP04CR2AttemptFixture(t)
	first, _ := coordinator.prepareOutgoingAttempt(context.Background(), msp04cr2Request(remote, "peer.invalid", 4712, "/ship/"))
	_, _ = coordinator.authorizeOutgoingAttempt(context.Background(), first)
	if got := coordinator.completeOutgoingAttempt(context.Background(), first.metadata, false); got != "backoff_active" {
		t.Fatalf("first terminal failure = %q", got)
	}
	fixture.clock.advanceMonotonic(3 * time.Second)
	second, outcome := coordinator.prepareOutgoingAttempt(context.Background(), msp04cr2Request(remote, "2001:db8::1", 4712, ""))
	if outcome != "attempt_reserved" || second == nil {
		t.Fatalf("newer path prepare = %q/%v", outcome, second)
	}
	before, ok := soleMSP04CR2Attempt(coordinator)
	if !ok {
		t.Fatal("newer reservation is absent")
	}
	remoteSKI := hex.EncodeToString(remote)
	if got := coordinator.outgoingAttemptConnectionClosed(context.Background(), remoteSKI, false, first.metadata); got != "stale_attempt" {
		t.Fatalf("delayed older closed callback = %q, want stale_attempt", got)
	}
	if got := coordinator.outgoingAttemptHandshakeStateUpdate(context.Background(), remoteSKI, "complete", first.metadata); got != "stale_attempt" {
		t.Fatalf("delayed older handshake callback = %q, want stale_attempt", got)
	}
	after, ok := soleMSP04CR2Attempt(coordinator)
	if !ok || !reflect.DeepEqual(after, before) || after.attemptID != second.metadata.attemptID {
		t.Fatal("delayed callback mutated or completed the newer attempt")
	}
}

func TestMSP04CR2RevocationSerializationPreventsLatePermit(t *testing.T) {
	fixture, coordinator, remote, _ := newMSP04CR2AttemptFixture(t)
	handle, outcome := coordinator.prepareOutgoingAttempt(context.Background(), msp04cr2Request(remote, "peer.invalid", 4712, "/ship/"))
	if outcome != "attempt_reserved" || handle == nil {
		t.Fatalf("prepare before race = %q/%v", outcome, handle)
	}
	fixture.store.block()
	revocationResult := make(chan string, 1)
	request := exactMSP04CR2RevocationRequest(coordinator, msp04cOrdinal(980))
	go func() { revocationResult <- coordinator.revoke(context.Background(), request) }()
	waitMSP04CSignal(t, fixture.store.commitEntered)

	authorizationStarted := make(chan struct{})
	authorizationResult := make(chan firstTrustOutgoingAttemptPermit, 1)
	go func() {
		close(authorizationStarted)
		permit, _ := coordinator.authorizeOutgoingAttempt(context.Background(), handle)
		authorizationResult <- permit
	}()
	waitMSP04CSignal(t, authorizationStarted)
	if calls := fixture.store.calls(); calls != 2 {
		t.Fatalf("authorization crossed per-SKI revocation serialization: store commits = %d, want prepare+revocation only", calls)
	}
	select {
	case permit := <-authorizationResult:
		t.Fatalf("authorization returned before revocation linearized: %#v", permit)
	default:
	}
	fixture.store.release()
	if got := waitMSP04CResult(t, revocationResult); got != "revoked" {
		t.Fatalf("serialized revocation = %q", got)
	}
	select {
	case permit := <-authorizationResult:
		if permit.decision != "DENY" || permit.reason != "STALE_HANDLE" {
			t.Fatalf("post-revocation authorization = %#v, want stale denial", permit)
		}
	case <-time.After(time.Second):
		t.Fatal("post-revocation authorization did not terminate")
	}
}

func TestMSP04CR2RevocationCancelsExactInFlightContextBeforeWithdrawal(t *testing.T) {
	fixture, coordinator, remote, _ := newMSP04CR2AttemptFixture(t)
	handle, _ := coordinator.prepareOutgoingAttempt(context.Background(), msp04cr2Request(remote, "peer.invalid", 4712, "/ship/"))
	permit, outcome := coordinator.authorizeOutgoingAttempt(context.Background(), handle)
	if outcome != "attempt_permitted" {
		t.Fatalf("authorize before revocation = %q", outcome)
	}
	request := exactMSP04CR2RevocationRequest(coordinator, msp04cOrdinal(990))
	if got := coordinator.revoke(context.Background(), request); got != "revoked" {
		t.Fatalf("revocation result = %q", got)
	}
	select {
	case <-permit.context.Done():
	default:
		t.Fatal("revocation did not cancel the exact in-flight permit context")
	}
	fixture.events.assertOrdered(t, "anchor_finalize", "disconnect", "unregister")
	if _, ok := soleMSP04CR2Attempt(coordinator); ok {
		t.Fatal("matching revocation retained its active reservation")
	}
	if got := coordinator.completeOutgoingAttempt(context.Background(), handle.metadata, true); got != "stale_attempt" {
		t.Fatalf("post-revocation callback = %q, want stale_attempt", got)
	}
	if reused, result := coordinator.authorizeOutgoingAttempt(context.Background(), handle); result != "attempt_denied" || reused.decision != "DENY" {
		t.Fatalf("revoked handle was permitted late: %q/%#v", result, reused)
	}
}

func newMSP04CR2AttemptFixture(t *testing.T) (*msp04cFixture, *firstTrustCoordinator, []byte, [32]byte) {
	t.Helper()
	fixture := newMSP04CFixture(t)
	remote := msp04cSubject(901)
	normalized := hex.EncodeToString(remote)
	scope := firstTrustRuntimeRetryScope(normalized)
	lineage := fixture.store.view.control.associationLineage
	fixture.store.view.associations = []firstTrustAssociationRecord{
		msp04cAssociation(901, lineage, true, true, true, true),
	}
	fixture.store.view.associations[0].subject = bytes.Clone(remote)
	fixture.store.view.control.quarantines = []firstTrustQuarantineRecord{{
		scope: scope, reason: "RETRYABLE_FAILURE", state: "RETRY_READY",
		retentionBudget: firstTrustQuarantineRetention, lastControlEpoch: fixture.store.view.control.controlEpoch,
	}}
	coordinator := fixture.newCoordinator()
	if got := coordinator.reopen(context.Background()); got != "pairing_closed" {
		t.Fatalf("open paired attempt fixture = %q", got)
	}
	if coordinator.recoveryState() != "PAIRED_TRUSTED" {
		t.Fatalf("attempt fixture recovery = %q, want PAIRED_TRUSTED", coordinator.recoveryState())
	}
	return fixture, coordinator, remote, scope
}

func msp04cr2Request(remote []byte, host string, port uint16, path string) firstTrustOutgoingAttemptRequest {
	return firstTrustOutgoingAttemptRequest{
		remoteSKI: bytes.Clone(remote),
		endpoint:  firstTrustOutgoingAttemptEndpoint{host: host, port: port},
		path:      path,
	}
}

func soleMSP04CR2Attempt(coordinator *firstTrustCoordinator) (firstTrustOutgoingAttemptRecord, bool) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if len(coordinator.controlView.control.attempts) != 1 {
		return firstTrustOutgoingAttemptRecord{}, false
	}
	return cloneFirstTrustOutgoingAttemptRecord(coordinator.controlView.control.attempts[0]), true
}

func exactMSP04CR2RevocationRequest(coordinator *firstTrustCoordinator, operationID [32]byte) firstTrustRevocationRequest {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	association := coordinator.controlView.associations[0]
	return firstTrustRevocationRequest{
		operationID: operationID, associationRef: association.reference,
		associationLineage:     coordinator.controlView.control.associationLineage,
		expectedGeneration:     coordinator.controlView.manifest.current,
		expectedManifestEpoch:  coordinator.controlView.manifest.epoch,
		expectedManifestSHA256: coordinator.controlView.manifest.sha256,
		expectedControlEpoch:   coordinator.controlView.control.controlEpoch,
	}
}

func sameMSP04CR2Context(left, right context.Context) bool {
	if left == nil || right == nil {
		return false
	}
	return reflect.ValueOf(left).Pointer() == reflect.ValueOf(right).Pointer()
}
