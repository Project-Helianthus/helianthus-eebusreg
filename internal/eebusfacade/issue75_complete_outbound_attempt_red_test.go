package eebusfacade

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
	shipmodel "github.com/Project-Helianthus/helianthus-ship-go/model"
)

func TestIssue75ExactCompletePublishesBeforeDurableRetirementWithoutCancelling(t *testing.T) {
	attempt := newIssue75TrustedAttempt(t)
	var callbackSawReservation atomic.Bool
	attempt.lifecycle.onPairing = func(_ string, state shipapi.ConnectionState) {
		if state != shipapi.ConnectionStateCompleted {
			return
		}
		if record, ok := soleMSP04CR2Attempt(attempt.coordinator); ok &&
			record.attemptID == permitMetadataAttemptID(t, attempt.permit.Metadata) {
			callbackSawReservation.Store(true)
		}
	}

	attempt.complete()

	if !callbackSawReservation.Load() {
		t.Fatal("ConnectionStateCompleted was not published while the exact reservation still owned success")
	}
	issue75AssertCompletedLifecycle(t, attempt.lifecycle, 1, 0)
	issue75AssertSuccessfulRetirement(t, attempt.coordinator, attempt.scope, attempt.permit.Context)
	if classes := attempt.store.classesFor("attempt_complete_success"); len(classes) != 1 {
		t.Fatalf("successful retirement publications = %v, want one attempt_complete_success", classes)
	}

	attempt.fixture.clock.advanceMonotonic(firstTrustOutgoingAttemptLease + time.Second)
	attempt.scheduler.runDue()
	issue75AssertSuccessfulRetirement(t, attempt.coordinator, attempt.scope, attempt.permit.Context)
	issue75AssertCompletedLifecycle(t, attempt.lifecycle, 1, 0)
	if classes := attempt.store.classesContaining("failure"); len(classes) != 0 {
		t.Fatalf("delayed lease synthesized failure after success: %v", classes)
	}
}

func TestIssue75CompleteResnapshotsAfterSynchronousTrustGenerationAdvance(t *testing.T) {
	attempt := newIssue75CandidateAttempt(t)
	attempt.bindTLS()
	if got := attempt.confirm("confirm-before-complete"); got != "transient_trusted" {
		t.Fatalf("confirm = %q, want transient_trusted", got)
	}
	attempt.facade.RemoteSKIConnected(nil, attempt.remoteSKI)
	attempt.facade.ServiceShipIDUpdate(attempt.remoteSKI, "issue75-remote-ship-id")

	attempt.coordinator.mu.Lock()
	generationBefore := attempt.coordinator.controlView.manifest.current.sequence
	attempt.coordinator.mu.Unlock()
	attempt.complete()

	issue75AssertSuccessfulRetirement(t, attempt.coordinator, attempt.scope, attempt.permit.Context)
	if !attempt.coordinator.trusted(attempt.remote) {
		t.Fatal("post-publication retirement discarded the synchronously committed durable association")
	}
	attempt.coordinator.mu.Lock()
	generationAfter := attempt.coordinator.controlView.manifest.current.sequence
	associations := append([]firstTrustAssociationRecord(nil), attempt.coordinator.controlView.associations...)
	lineage := attempt.coordinator.controlView.control.associationLineage
	attempt.coordinator.mu.Unlock()
	if generationAfter <= generationBefore {
		t.Fatalf("control generation did not advance across handoff and retirement: before=%d after=%d", generationBefore, generationAfter)
	}
	if issue75UsableAssociationCount(associations, attempt.remote, lineage) != 1 {
		t.Fatalf("post-retirement durable associations = %#v, want one exact usable association", associations)
	}
	if !attempt.store.classBefore("first_trust", "attempt_complete_success") {
		t.Fatalf("publication classes = %v, want first_trust before attempt_complete_success", attempt.store.classesSnapshot())
	}
	if got := attempt.service.unregisterCount(); got != 0 {
		t.Fatalf("durable success unregistered transient trust %d times", got)
	}
	issue75AssertCompletedLifecycle(t, attempt.lifecycle, 2, 0)
}

func TestIssue75KnownUnappliedRetirementRetriesFromFreshSnapshot(t *testing.T) {
	attempt := newIssue75TrustedAttempt(t)
	attempt.store.setOutcomes("attempt_complete_success", "commit_not_published", "commit_durable")

	attempt.complete()

	issue75AssertSuccessfulRetirement(t, attempt.coordinator, attempt.scope, attempt.permit.Context)
	if classes := attempt.store.classesFor("attempt_complete_success"); len(classes) != 2 {
		t.Fatalf("known-unapplied retirement attempts = %v, want two", classes)
	}
	issue75AssertCompletedLifecycle(t, attempt.lifecycle, 1, 0)
	if classes := attempt.store.classesContaining("failure"); len(classes) != 0 {
		t.Fatalf("known-unapplied retirement charged failure: %v", classes)
	}
}

func TestIssue75AmbiguousRetirementFailsClosedWithoutReinterpretingSuccess(t *testing.T) {
	attempt := newIssue75CandidateAttempt(t)
	cancelCalls := issue75InstallCancelCounter(t, attempt.coordinator, attempt.permit.Metadata)
	attempt.bindTLS()
	if got := attempt.confirm("confirm-before-ambiguous-retirement"); got != "transient_trusted" {
		t.Fatalf("confirm = %q, want transient_trusted", got)
	}
	attempt.facade.RemoteSKIConnected(nil, attempt.remoteSKI)
	attempt.facade.ServiceShipIDUpdate(attempt.remoteSKI, "issue75-durable-ship-id")
	attempt.store.setOutcomes("attempt_complete_success", "commit_durability_unknown")

	attempt.complete()

	if got, reason := attempt.coordinator.recoveryState(), attempt.coordinator.recoveryReason(); got != "QUARANTINED" || reason != "DURABILITY_UNKNOWN" {
		t.Fatalf("ambiguous retirement recovery = %s/%s, want QUARANTINED/DURABILITY_UNKNOWN", got, reason)
	}
	if attempt.permit.Context.Err() != nil || cancelCalls.Load() != 0 {
		t.Fatalf("ambiguous retirement cancelled the live permit: err=%v calls=%d", attempt.permit.Context.Err(), cancelCalls.Load())
	}
	if state, count, remaining, ok := attempt.coordinator.retryState(attempt.scope); !ok ||
		state != "RETRY_READY" || count != 0 || remaining != 0 {
		t.Fatalf("ambiguous retirement retry tuple = %s/%d/%s/%t, want RETRY_READY/0/0/true", state, count, remaining, ok)
	}
	attempt.coordinator.mu.Lock()
	associationsBefore := append([]firstTrustAssociationRecord(nil), attempt.coordinator.controlView.associations...)
	lineage := attempt.coordinator.controlView.control.associationLineage
	attempt.coordinator.mu.Unlock()
	if issue75UsableAssociationCount(associationsBefore, attempt.remote, lineage) != 1 {
		t.Fatalf("ambiguous retirement lost the newer durable association: %#v", associationsBefore)
	}

	attempt.bridge.OutgoingAttemptHandshakeStateUpdate(
		attempt.remoteSKI,
		shipmodel.ShipState{State: shipmodel.SmeStateError},
		attempt.permit.Metadata,
	)
	attempt.complete()
	attempt.fixture.clock.advanceMonotonic(firstTrustOutgoingAttemptLease + time.Second)
	attempt.scheduler.runDue()

	if attempt.permit.Context.Err() != nil || cancelCalls.Load() != 0 {
		t.Fatalf("post-ambiguity callback cancelled the live permit: err=%v calls=%d", attempt.permit.Context.Err(), cancelCalls.Load())
	}
	if classes := attempt.store.classesContaining("failure"); len(classes) != 0 {
		t.Fatalf("post-ambiguity callback synthesized failure: %v", classes)
	}
	attempt.coordinator.mu.Lock()
	associationsAfter := append([]firstTrustAssociationRecord(nil), attempt.coordinator.controlView.associations...)
	attempt.coordinator.mu.Unlock()
	if !reflect.DeepEqual(associationsAfter, associationsBefore) {
		t.Fatal("post-ambiguity callbacks overwrote the newer durable association")
	}
	if handle, err := attempt.bridge.Prepare(issue75SHIPRequest(attempt.remoteSKI, "blocked.invalid")); err == nil || handle != nil {
		t.Fatalf("DURABILITY_UNKNOWN admitted a new launch: handle=%v err=%v", handle, err)
	}
	issue75AssertCompletedLifecycle(t, attempt.lifecycle, 2, 0)
}

func TestIssue75ExactLaterCloseConsumesMarkerAfterCloseAfterFuncIsDisabled(t *testing.T) {
	attempt := newIssue75CandidateAttempt(t)
	cancelCalls := issue75InstallCancelCounter(t, attempt.coordinator, attempt.permit.Metadata)
	attempt.bindTLS()
	attempt.complete()
	issue75AssertSuccessfulRetirement(t, attempt.coordinator, attempt.scope, attempt.permit.Context)

	if got := attempt.confirm("confirm-after-preconfirm-complete"); got != "transient_trusted" {
		t.Fatalf("post-complete confirm = %q, want transient_trusted", got)
	}
	if got := attempt.service.registerCount(); got != 1 {
		t.Fatalf("transient registrations = %d, want 1", got)
	}

	var closeAfterFuncCalls atomic.Int32
	stopCloseAfterFunc := context.AfterFunc(attempt.permit.Context, func() {
		closeAfterFuncCalls.Add(1)
	})
	if !stopCloseAfterFunc() {
		t.Fatal("fixture could not disable the SHIP close AfterFunc before the close callback")
	}
	attempt.close(true)

	issue75AssertContextCancelled(t, attempt.permit.Context)
	if got := cancelCalls.Load(); got != 1 {
		t.Fatalf("exact later close cancel calls = %d, want 1", got)
	}
	if got := closeAfterFuncCalls.Load(); got != 0 {
		t.Fatalf("disabled close AfterFunc ran %d times", got)
	}
	if got := attempt.service.unregisterCount(); got != 1 {
		t.Fatalf("transient cleanup unregister calls = %d, want 1", got)
	}
	if _, _, _, _, _, _, ok := attempt.coordinator.candidate(); ok {
		t.Fatal("exact later close retained the transient candidate")
	}
	issue75AssertCompletedLifecycle(t, attempt.lifecycle, 2, 1)
	issue75AssertRetryReset(t, attempt.coordinator, attempt.scope)

	attempt.close(true)
	attempt.bridge.OutgoingAttemptHandshakeStateUpdate(
		attempt.remoteSKI,
		shipmodel.ShipState{State: shipmodel.SmeStateError},
		attempt.permit.Metadata,
	)
	if got := cancelCalls.Load(); got != 1 {
		t.Fatalf("duplicate close/error cancel calls = %d, want 1", got)
	}
	if got := attempt.service.unregisterCount(); got != 1 {
		t.Fatalf("duplicate close/error unregister calls = %d, want 1", got)
	}
	issue75AssertCompletedLifecycle(t, attempt.lifecycle, 2, 1)
	if classes := attempt.store.classesContaining("failure"); len(classes) != 0 {
		t.Fatalf("later close charged retry failure: %v", classes)
	}
}

func TestIssue75CompleteWinsLeaseRaceAndDelayedLeaseNoOps(t *testing.T) {
	attempt := newIssue75TrustedAttempt(t)
	completionEntered := make(chan struct{})
	releaseCompletion := make(chan struct{})
	var enteredOnce sync.Once
	attempt.lifecycle.onPairing = func(_ string, state shipapi.ConnectionState) {
		if state != shipapi.ConnectionStateCompleted {
			return
		}
		enteredOnce.Do(func() { close(completionEntered) })
		<-releaseCompletion
	}

	completeDone := make(chan struct{})
	go func() {
		attempt.complete()
		close(completeDone)
	}()
	waitMSP04CSignal(t, completionEntered)
	attempt.fixture.clock.advanceMonotonic(firstTrustOutgoingAttemptLease)
	leaseDone := make(chan struct{})
	go func() {
		attempt.scheduler.runDue()
		close(leaseDone)
	}()
	close(releaseCompletion)
	waitMSP04CSignal(t, completeDone)
	waitMSP04CSignal(t, leaseDone)

	issue75AssertSuccessfulRetirement(t, attempt.coordinator, attempt.scope, attempt.permit.Context)
	issue75AssertCompletedLifecycle(t, attempt.lifecycle, 1, 0)
	if classes := attempt.store.classesContaining("failure"); len(classes) != 0 {
		t.Fatalf("lease callback ordered behind completion charged failure: %v", classes)
	}
}

func TestIssue75RetiredCallbacksCannotMutateNewerAttempt(t *testing.T) {
	attempt := newIssue75TrustedAttempt(t)
	oldRuntime := attempt.handle.(*runtimeOutgoingAttemptHandle)
	oldCancellationGeneration := oldRuntime.handle.cancellationGeneration
	oldMetadata, ok := runtimeOutgoingAttemptMetadataFromSHIP(attempt.permit.Metadata)
	if !ok {
		t.Fatal("fixture permit metadata is invalid")
	}
	attempt.complete()
	attempt.close(true)

	current, err := attempt.bridge.Prepare(issue75SHIPRequest(attempt.remoteSKI, "newer.invalid"))
	if err != nil || current == nil {
		t.Fatalf("prepare newer attempt = %v/%v", current, err)
	}
	currentPermit, err := attempt.bridge.AuthorizeLaunch(current)
	if err != nil || currentPermit.Decision != shipapi.OutgoingAttemptDecisionPermit {
		t.Fatalf("authorize newer attempt = %#v/%v", currentPermit, err)
	}
	before, ok := soleMSP04CR2Attempt(attempt.coordinator)
	if !ok {
		t.Fatal("newer attempt is absent")
	}

	var callbacks sync.WaitGroup
	callbacks.Add(4)
	go func() {
		defer callbacks.Done()
		attempt.complete()
	}()
	go func() {
		defer callbacks.Done()
		attempt.bridge.OutgoingAttemptHandshakeStateUpdate(
			attempt.remoteSKI,
			shipmodel.ShipState{State: shipmodel.SmeStateError},
			attempt.permit.Metadata,
		)
	}()
	go func() {
		defer callbacks.Done()
		attempt.close(true)
	}()
	go func() {
		defer callbacks.Done()
		attempt.coordinator.expireOutgoingAttemptLease(
			attempt.remote,
			oldMetadata,
			oldCancellationGeneration,
		)
	}()
	callbacks.Wait()

	after, ok := soleMSP04CR2Attempt(attempt.coordinator)
	if !ok || !reflect.DeepEqual(after, before) ||
		after.attemptID != permitMetadataAttemptID(t, currentPermit.Metadata) {
		t.Fatalf("retired callbacks changed newer attempt: before=%#v after=%#v present=%t", before, after, ok)
	}
	if currentPermit.Context.Err() != nil {
		t.Fatal("retired callback cancelled the newer permit context")
	}
	issue75AssertCompletedLifecycle(t, attempt.lifecycle, 1, 1)
}

func TestIssue75ShutdownConsumesSuccessfulMarkerWithoutChargingRetry(t *testing.T) {
	attempt := newIssue75TrustedAttempt(t)
	cancelCalls := issue75InstallCancelCounter(t, attempt.coordinator, attempt.permit.Metadata)
	attempt.complete()
	issue75AssertSuccessfulRetirement(t, attempt.coordinator, attempt.scope, attempt.permit.Context)

	if err := attempt.bridge.shutdown(); err != nil {
		t.Fatal(err)
	}

	issue75AssertContextCancelled(t, attempt.permit.Context)
	if got := cancelCalls.Load(); got != 1 {
		t.Fatalf("shutdown cancel calls = %d, want 1", got)
	}
	issue75AssertRetryReset(t, attempt.coordinator, attempt.scope)
	attempt.close(true)
	issue75AssertCompletedLifecycle(t, attempt.lifecycle, 1, 0)
	if classes := attempt.store.classesContaining("failure"); len(classes) != 0 {
		t.Fatalf("shutdown reinterpreted successful marker as failure: %v", classes)
	}
}

func TestIssue75RevocationConsumesSuccessfulMarkerAndLaterCloseNoOps(t *testing.T) {
	attempt := newIssue75TrustedAttempt(t)
	cancelCalls := issue75InstallCancelCounter(t, attempt.coordinator, attempt.permit.Metadata)
	attempt.complete()
	request := exactMSP04CR2RevocationRequest(attempt.coordinator, msp04cOrdinal(7_501))

	if got := attempt.coordinator.revoke(context.Background(), request); got != "revoked" {
		t.Fatalf("revocation = %q, want revoked", got)
	}

	issue75AssertContextCancelled(t, attempt.permit.Context)
	if got := cancelCalls.Load(); got != 1 {
		t.Fatalf("revocation cancel calls = %d, want 1", got)
	}
	attempt.close(true)
	issue75AssertCompletedLifecycle(t, attempt.lifecycle, 1, 0)
	if classes := attempt.store.classesContaining("failure"); len(classes) != 0 {
		t.Fatalf("revocation/later close charged retry failure: %v", classes)
	}
}

func TestIssue75PostSuccessMarkersAndActiveContextsShareBoundedCapacity(t *testing.T) {
	fixture := newMSP04CFixture(t)
	lineage := fixture.store.view.control.associationLineage
	remotes := make([][]byte, firstTrustMaximumOutgoingAttempts)
	fixture.store.view.associations = make([]firstTrustAssociationRecord, 0, firstTrustMaximumOutgoingAttempts)
	fixture.store.view.control.quarantines = make([]firstTrustQuarantineRecord, 0, firstTrustMaximumOutgoingAttempts)
	for index := range remotes {
		ordinal := uint64(8_000 + index)
		remote := msp04cSubject(ordinal)
		remotes[index] = remote
		fixture.store.view.associations = append(
			fixture.store.view.associations,
			msp04cAssociation(ordinal, lineage, true, true, true, true),
		)
		fixture.store.view.control.quarantines = append(
			fixture.store.view.control.quarantines,
			firstTrustQuarantineRecord{
				scope:            firstTrustRuntimeRetryScope(firstTrustNormalizedSKI(remote)),
				reason:           "RETRYABLE_FAILURE",
				state:            "RETRY_READY",
				retentionBudget:  firstTrustQuarantineRetention,
				lastControlEpoch: fixture.store.view.control.controlEpoch,
			},
		)
	}
	coordinator := fixture.newCoordinator()
	if got := coordinator.reopen(context.Background()); got != "pairing_closed" ||
		coordinator.recoveryState() != "PAIRED_TRUSTED" {
		t.Fatalf("bounded fixture reopen = %q/%q", got, coordinator.recoveryState())
	}
	scheduler := &msp04cr2Scheduler{now: fixture.clock.MonotonicNow}
	coordinator.outgoingAttemptSchedule = scheduler.afterFunc
	store := newIssue75RecordingStore(fixture.store)
	coordinator.recoveryStore = store
	bridge := newFirstTrustOutgoingAttemptBridge(&runtimeFirstTrustResources{coordinator: coordinator})
	lifecycle := &issue75LifecycleSpy{}
	bridge.bindLifecycle(lifecycle)

	permits := make([]shipapi.OutgoingAttemptPermit, len(remotes))
	for index, remote := range remotes {
		remoteSKI := hex.EncodeToString(remote)
		handle, err := bridge.Prepare(issue75SHIPRequest(remoteSKI, fmt.Sprintf("peer-%03d.invalid", index)))
		if err != nil || handle == nil {
			t.Fatalf("prepare marker %d = %v/%v", index, handle, err)
		}
		permit, err := bridge.AuthorizeLaunch(handle)
		if err != nil || permit.Decision != shipapi.OutgoingAttemptDecisionPermit {
			t.Fatalf("authorize marker %d = %#v/%v", index, permit, err)
		}
		permits[index] = permit
		bridge.OutgoingAttemptHandshakeStateUpdate(
			remoteSKI,
			shipmodel.ShipState{State: shipmodel.SmeStateComplete},
			permit.Metadata,
		)
	}

	extraOrdinal := uint64(9_000)
	extraRemote := msp04cSubject(extraOrdinal)
	extraSKI := hex.EncodeToString(extraRemote)
	extraAssociation := msp04cAssociation(extraOrdinal, lineage, true, true, true, true)
	coordinator.mu.Lock()
	coordinator.controlView.control.quarantines = append(
		[]firstTrustQuarantineRecord(nil),
		coordinator.controlView.control.quarantines[1:]...,
	)
	coordinator.controlView.associations = append(coordinator.controlView.associations, extraAssociation)
	coordinator.trustedRemotes[string(extraRemote)] = extraAssociation.service
	current := cloneFirstTrustControlView(coordinator.controlView)
	coordinator.mu.Unlock()
	fixture.store.mu.Lock()
	fixture.store.view = cloneFirstTrustControlView(current)
	fixture.store.mu.Unlock()

	if handle, err := bridge.Prepare(issue75SHIPRequest(extraSKI, "capacity.invalid")); err == nil || handle != nil {
		t.Fatalf("129th live owner was admitted: handle=%v err=%v", handle, err)
	}
	bridge.OutgoingAttemptConnectionClosed(
		hex.EncodeToString(remotes[0]),
		true,
		permits[0].Metadata,
	)
	extraHandle, err := bridge.Prepare(issue75SHIPRequest(extraSKI, "capacity.invalid"))
	if err != nil || extraHandle == nil {
		t.Fatalf("released marker capacity did not admit one replacement: handle=%v err=%v", extraHandle, err)
	}

	if err := bridge.shutdown(); err != nil {
		t.Fatal(err)
	}
	for index, permit := range permits {
		select {
		case <-permit.Context.Done():
		default:
			t.Fatalf("shutdown retained marker context %d", index)
		}
	}
	for index := 1; index < len(remotes); index++ {
		issue75AssertRetryReset(
			t,
			coordinator,
			firstTrustRuntimeRetryScope(firstTrustNormalizedSKI(remotes[index])),
		)
	}
	extraScope := firstTrustRuntimeRetryScope(firstTrustNormalizedSKI(extraRemote))
	if state, count, _, ok := coordinator.retryState(extraScope); !ok || state != "BACKOFF_ACTIVE" || count != 1 {
		t.Fatalf("shutdown active replacement retry tuple = %s/%d/%t, want BACKOFF_ACTIVE/1/true", state, count, ok)
	}
}

type issue75TrustedAttempt struct {
	fixture     *msp04cFixture
	coordinator *firstTrustCoordinator
	bridge      *firstTrustOutgoingAttemptBridge
	lifecycle   *issue75LifecycleSpy
	store       *issue75RecordingStore
	scheduler   *msp04cr2Scheduler
	remote      []byte
	remoteSKI   string
	scope       [32]byte
	handle      shipapi.OutgoingAttemptHandle
	permit      shipapi.OutgoingAttemptPermit
}

func newIssue75TrustedAttempt(t *testing.T) *issue75TrustedAttempt {
	t.Helper()
	fixture, coordinator, remote, scope := newMSP04CR2AttemptFixture(t)
	scheduler := &msp04cr2Scheduler{now: fixture.clock.MonotonicNow}
	coordinator.outgoingAttemptSchedule = scheduler.afterFunc
	store := newIssue75RecordingStore(fixture.store)
	coordinator.recoveryStore = store
	bridge := newFirstTrustOutgoingAttemptBridge(&runtimeFirstTrustResources{coordinator: coordinator})
	lifecycle := &issue75LifecycleSpy{}
	bridge.bindLifecycle(lifecycle)
	remoteSKI := hex.EncodeToString(remote)
	handle, err := bridge.Prepare(issue75SHIPRequest(remoteSKI, "peer.invalid"))
	if err != nil || handle == nil {
		t.Fatalf("prepare = %v/%v", handle, err)
	}
	permit, err := bridge.AuthorizeLaunch(handle)
	if err != nil || permit.Decision != shipapi.OutgoingAttemptDecisionPermit {
		t.Fatalf("authorize = %#v/%v", permit, err)
	}
	return &issue75TrustedAttempt{
		fixture: fixture, coordinator: coordinator, bridge: bridge, lifecycle: lifecycle,
		store: store, scheduler: scheduler, remote: remote, remoteSKI: remoteSKI,
		scope: scope, handle: handle, permit: permit,
	}
}

func (attempt *issue75TrustedAttempt) complete() {
	attempt.bridge.OutgoingAttemptHandshakeStateUpdate(
		attempt.remoteSKI,
		shipmodel.ShipState{State: shipmodel.SmeStateComplete},
		attempt.permit.Metadata,
	)
}

func (attempt *issue75TrustedAttempt) close(complete bool) {
	attempt.bridge.OutgoingAttemptConnectionClosed(attempt.remoteSKI, complete, attempt.permit.Metadata)
}

type issue75CandidateAttempt struct {
	fixture     *msp04cFixture
	coordinator *firstTrustCoordinator
	bridge      *firstTrustOutgoingAttemptBridge
	facade      *firstTrustFacade
	service     *issue60Service
	lifecycle   *issue75LifecycleSpy
	store       *issue75RecordingStore
	scheduler   *msp04cr2Scheduler
	remote      []byte
	remoteSKI   string
	scope       [32]byte
	permit      shipapi.OutgoingAttemptPermit
}

func newIssue75CandidateAttempt(t *testing.T) *issue75CandidateAttempt {
	t.Helper()
	fixture := newMSP04CFixture(t)
	coordinator := fixture.newCoordinator()
	if got := coordinator.reopen(context.Background()); got != "pairing_closed" {
		t.Fatalf("reopen = %q", got)
	}
	store := newIssue75RecordingStore(fixture.store)
	coordinator.recoveryStore = store
	scheduler := &msp04cr2Scheduler{now: fixture.clock.MonotonicNow}
	coordinator.outgoingAttemptSchedule = scheduler.afterFunc
	service := &issue60Service{}
	facade, err := newFirstTrustFacade(service, coordinator)
	if err != nil {
		t.Fatal(err)
	}
	coordinator.mu.Lock()
	coordinator.effects = facade
	coordinator.mu.Unlock()
	bridge := newFirstTrustOutgoingAttemptBridge(&runtimeFirstTrustResources{coordinator: coordinator})
	lifecycle := &issue75LifecycleSpy{target: issue75FacadeLifecycle{facade: facade}}
	bridge.bindLifecycle(lifecycle)
	bridge.bindTLSLifecycle(facade)
	if got := coordinator.openPairingWindow(context.Background(), "issue75-open", firstTrustMaximumWindow); got != "open_empty" {
		t.Fatalf("open pairing window = %q", got)
	}

	var permit shipapi.OutgoingAttemptPermit
	service.queue = func(_ string, expectedSKI string) error {
		facade.ServicePairingDetailUpdate(
			expectedSKI,
			shipapi.NewConnectionStateDetail(shipapi.ConnectionStateQueued, nil),
		)
		handle, prepareErr := bridge.Prepare(issue75SHIPRequest(expectedSKI, "peer.invalid"))
		if prepareErr != nil {
			return prepareErr
		}
		var authorizeErr error
		permit, authorizeErr = bridge.AuthorizeLaunch(handle)
		if authorizeErr != nil {
			return authorizeErr
		}
		if permit.Decision != shipapi.OutgoingAttemptDecisionPermit {
			return fmt.Errorf("outgoing attempt decision = %v", permit.Decision)
		}
		return nil
	}
	facade.VisiblePairingCandidatesUpdated(nil, []shipapi.PairingCandidateRef{{
		CandidateRef: "issue75-candidate",
		SKI:          issue56SKIA,
	}})
	if got := coordinator.selectCandidate(
		context.Background(),
		"issue75-select",
		"issue75-candidate",
		issue56SKIA,
	); got != "candidate_queued" {
		t.Fatalf("select candidate = %q", got)
	}
	remote, remoteSKI, ok := decodeFirstTrustSKI(issue56SKIA)
	if !ok {
		t.Fatal("candidate fixture SKI is invalid")
	}
	if permit.Decision != shipapi.OutgoingAttemptDecisionPermit {
		t.Fatal("candidate selection did not produce an outbound permit")
	}
	return &issue75CandidateAttempt{
		fixture: fixture, coordinator: coordinator, bridge: bridge, facade: facade,
		service: service, lifecycle: lifecycle, store: store, scheduler: scheduler,
		remote: remote, remoteSKI: remoteSKI,
		scope:  firstTrustRuntimeRetryScope(firstTrustNormalizedSKI(remote)),
		permit: permit,
	}
}

func (attempt *issue75CandidateAttempt) bindTLS() {
	attempt.bridge.OutgoingAttemptHandshakeStateUpdate(
		attempt.remoteSKI,
		shipmodel.ShipState{State: shipmodel.CmiStateInitStart},
		attempt.permit.Metadata,
	)
}

func (attempt *issue75CandidateAttempt) confirm(key string) string {
	fingerprint, nonce, expiresAt, connection, generation, _, ok := attempt.coordinator.candidate()
	if !ok {
		return "candidate_absent"
	}
	return attempt.coordinator.confirm(
		context.Background(),
		key,
		fingerprint,
		nonce,
		expiresAt,
		connection,
		generation,
	)
}

func (attempt *issue75CandidateAttempt) complete() {
	attempt.bridge.OutgoingAttemptHandshakeStateUpdate(
		attempt.remoteSKI,
		shipmodel.ShipState{State: shipmodel.SmeStateComplete},
		attempt.permit.Metadata,
	)
}

func (attempt *issue75CandidateAttempt) close(complete bool) {
	attempt.bridge.OutgoingAttemptConnectionClosed(attempt.remoteSKI, complete, attempt.permit.Metadata)
}

type issue75LifecycleSpy struct {
	mu           sync.Mutex
	target       firstTrustOutgoingAttemptLifecycle
	pairing      []shipapi.ConnectionState
	disconnected []string
	onPairing    func(string, shipapi.ConnectionState)
}

type issue75FacadeLifecycle struct {
	facade *firstTrustFacade
}

func (lifecycle issue75FacadeLifecycle) RemoteSKIDisconnected(ski string) {
	lifecycle.facade.RemoteSKIDisconnected(nil, ski)
}

func (lifecycle issue75FacadeLifecycle) ServicePairingDetailUpdate(
	ski string,
	detail *shipapi.ConnectionStateDetail,
) {
	lifecycle.facade.ServicePairingDetailUpdate(ski, detail)
}

func (spy *issue75LifecycleSpy) RemoteSKIDisconnected(ski string) {
	spy.mu.Lock()
	spy.disconnected = append(spy.disconnected, ski)
	target := spy.target
	spy.mu.Unlock()
	if target != nil {
		target.RemoteSKIDisconnected(ski)
	}
}

func (spy *issue75LifecycleSpy) ServicePairingDetailUpdate(ski string, detail *shipapi.ConnectionStateDetail) {
	state := shipapi.ConnectionStateNone
	if detail != nil {
		state = detail.State()
	}
	spy.mu.Lock()
	spy.pairing = append(spy.pairing, state)
	onPairing := spy.onPairing
	target := spy.target
	spy.mu.Unlock()
	if onPairing != nil {
		onPairing(ski, state)
	}
	if target != nil {
		target.ServicePairingDetailUpdate(ski, detail)
	}
}

func (spy *issue75LifecycleSpy) counts() (int, int) {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	return len(spy.pairing), len(spy.disconnected)
}

type issue75RecordingStore struct {
	delegate *msp04cStoreSpy

	mu       sync.Mutex
	classes  []string
	outcomes map[string][]string
}

func newIssue75RecordingStore(delegate *msp04cStoreSpy) *issue75RecordingStore {
	return &issue75RecordingStore{
		delegate: delegate,
		outcomes: make(map[string][]string),
	}
}

func (store *issue75RecordingStore) ReloadControl(ctx context.Context) (firstTrustControlView, string) {
	return store.delegate.ReloadControl(ctx)
}

func (store *issue75RecordingStore) SelectedGeneration() uint64 {
	return store.delegate.SelectedGeneration()
}

func (store *issue75RecordingStore) PrepareControl(
	ctx context.Context,
	previous firstTrustControlView,
	target firstTrustControlRecord,
	operationID [32]byte,
	operationClass string,
) (firstTrustPreparedPublication, string) {
	store.mu.Lock()
	store.classes = append(store.classes, operationClass)
	store.mu.Unlock()
	return store.delegate.PrepareControl(ctx, previous, target, operationID, operationClass)
}

func (store *issue75RecordingStore) CommitControl(ctx context.Context, publication firstTrustPreparedPublication) string {
	store.mu.Lock()
	var outcome string
	if queued := store.outcomes[publication.operationClass]; len(queued) != 0 {
		outcome = queued[0]
		store.outcomes[publication.operationClass] = queued[1:]
	}
	store.mu.Unlock()
	if outcome != "" {
		store.delegate.mu.Lock()
		store.delegate.commitOutcome = outcome
		store.delegate.mu.Unlock()
	}
	return store.delegate.CommitControl(ctx, publication)
}

func (store *issue75RecordingStore) ObserveControlPublication(
	ctx context.Context,
	pending firstTrustPendingPublication,
) string {
	return store.delegate.ObserveControlPublication(ctx, pending)
}

func (store *issue75RecordingStore) setOutcomes(operationClass string, outcomes ...string) {
	store.mu.Lock()
	store.outcomes[operationClass] = append([]string(nil), outcomes...)
	store.mu.Unlock()
}

func (store *issue75RecordingStore) classesSnapshot() []string {
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]string(nil), store.classes...)
}

func (store *issue75RecordingStore) classesFor(operationClass string) []string {
	store.mu.Lock()
	defer store.mu.Unlock()
	var matched []string
	for _, class := range store.classes {
		if class == operationClass {
			matched = append(matched, class)
		}
	}
	return matched
}

func (store *issue75RecordingStore) classesContaining(fragment string) []string {
	store.mu.Lock()
	defer store.mu.Unlock()
	var matched []string
	for _, class := range store.classes {
		if strings.Contains(class, fragment) {
			matched = append(matched, class)
		}
	}
	return matched
}

func (store *issue75RecordingStore) classBefore(first, second string) bool {
	store.mu.Lock()
	defer store.mu.Unlock()
	firstIndex, secondIndex := -1, -1
	for index, class := range store.classes {
		if class == first && firstIndex < 0 {
			firstIndex = index
		}
		if class == second && secondIndex < 0 {
			secondIndex = index
		}
	}
	return firstIndex >= 0 && secondIndex > firstIndex
}

func issue75SHIPRequest(remoteSKI, host string) shipapi.OutgoingAttemptRequest {
	return shipapi.OutgoingAttemptRequest{
		RemoteSKI: remoteSKI,
		Endpoint: shipapi.OutgoingAttemptEndpoint{
			Host: host,
			Port: 4712,
		},
		Path: "/ship/",
	}
}

func issue75InstallCancelCounter(
	t *testing.T,
	coordinator *firstTrustCoordinator,
	metadata shipapi.OutgoingAttemptMetadata,
) *atomic.Int32 {
	t.Helper()
	converted, ok := runtimeOutgoingAttemptMetadataFromSHIP(metadata)
	if !ok {
		t.Fatal("permit metadata is invalid")
	}
	counter := &atomic.Int32{}
	coordinator.mu.Lock()
	runtime, ok := coordinator.outgoingAttemptContexts[converted.attemptID]
	if !ok || runtime.cancel == nil {
		coordinator.mu.Unlock()
		t.Fatal("attempt runtime cancel owner is absent")
	}
	cancel := runtime.cancel
	runtime.cancel = func() {
		counter.Add(1)
		cancel()
	}
	coordinator.outgoingAttemptContexts[converted.attemptID] = runtime
	coordinator.mu.Unlock()
	return counter
}

func issue75AssertSuccessfulRetirement(
	t *testing.T,
	coordinator *firstTrustCoordinator,
	scope [32]byte,
	permitContext context.Context,
) {
	t.Helper()
	if _, ok := soleMSP04CR2Attempt(coordinator); ok {
		t.Fatal("successful completion retained the exact durable reservation")
	}
	issue75AssertRetryReset(t, coordinator, scope)
	if permitContext == nil || permitContext.Err() != nil {
		t.Fatalf("successful completion cancelled the live permit context: %v", permitContext)
	}
}

func issue75AssertRetryReset(t *testing.T, coordinator *firstTrustCoordinator, scope [32]byte) {
	t.Helper()
	state, count, remaining, ok := coordinator.retryState(scope)
	if !ok || state != "RETRY_READY" || count != 0 || remaining != 0 {
		t.Fatalf("successful retry tuple = %s/%d/%s/%t, want RETRY_READY/0/0/true", state, count, remaining, ok)
	}
}

func issue75AssertCompletedLifecycle(
	t *testing.T,
	lifecycle *issue75LifecycleSpy,
	wantPairing int,
	wantDisconnected int,
) {
	t.Helper()
	pairing, disconnected := lifecycle.counts()
	if pairing != wantPairing || disconnected != wantDisconnected {
		t.Fatalf(
			"lifecycle counts = pairing:%d disconnected:%d, want %d/%d",
			pairing,
			disconnected,
			wantPairing,
			wantDisconnected,
		)
	}
}

func issue75AssertContextCancelled(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-ctx.Done():
	default:
		t.Fatal("permit context remains live")
	}
}

func issue75UsableAssociationCount(
	associations []firstTrustAssociationRecord,
	remote []byte,
	lineage [32]byte,
) int {
	count := 0
	for _, association := range associations {
		if bytes.Equal(association.subject, remote) && firstTrustAssociationUsable(association, lineage) {
			count++
		}
	}
	return count
}
