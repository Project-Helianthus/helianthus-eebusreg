package eebusfacade

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
)

func TestOperatorAdminV1BridgeSelectReservesWithoutDialAndConnectsAtMostOnce(t *testing.T) {
	fixture := newMSP04BFixture(t, "commit_durable")
	coordinator := fixture.coordinator.(*firstTrustCoordinator)
	service := newOperatorAdminV1BridgeServiceSpy()
	bridge := newOperatorAdminV1Bridge(coordinator, service, &msp04cOrdinalReader{next: 700})

	opened, failure := bridge.openOperatorAdminV1(context.Background(), time.Minute)
	requireOperatorAdminV1BridgeSuccess(t, failure)
	if !opened.changed || coordinator.state() != "OPEN_EMPTY" {
		t.Fatalf("open transition = %#v state=%q, want changed OPEN_EMPTY", opened, coordinator.state())
	}

	const candidateRef = "ship-owner-private-observation"
	coordinator.visiblePairingCandidatesUpdated([]shipapi.PairingCandidateRef{{
		CandidateRef: candidateRef,
		SKI:          operatorAdminV1BridgeTestSKI,
		Name:         "owner-visible peer",
		Identifier:   "owner-identifier",
		Brand:        "owner-brand",
		Type:         "owner-type",
		Model:        "owner-model",
	}})
	snapshot, snapshotFailure := bridge.snapshotOperatorAdminV1(context.Background())
	requireOperatorAdminV1BridgeSuccess(t, snapshotFailure)
	if len(snapshot.discovered) != 1 {
		t.Fatalf("discovered rows = %d, want 1", len(snapshot.discovered))
	}
	observation := snapshot.discovered[0].reference
	if observation == "" || observation == candidateRef || strings.Contains(observation, candidateRef) {
		t.Fatalf("observation reference %q exposes or omits its private binding", observation)
	}
	row := snapshot.discovered[0]
	if row.ski != operatorAdminV1BridgeTestSKI || row.observationRevision == 0 || row.lastSeen.IsZero() ||
		row.name != "owner-visible peer" || row.identifier != "owner-identifier" || row.brand != "owner-brand" ||
		row.typeName != "owner-type" || row.model != "owner-model" || row.endpoint != "" ||
		snapshot.capturedAt.IsZero() || snapshot.status == "" || snapshot.window != "open" ||
		snapshot.windowDeadline.IsZero() || snapshot.register == "" || snapshot.listener == "" || snapshot.discovery == "" {
		t.Fatalf("discovered owner facts or status incomplete: row=%#v snapshot=%#v", row, snapshot)
	}

	selection, selected, selectFailure := bridge.selectOperatorAdminV1(
		context.Background(),
		observation,
		operatorAdminV1BridgeTestSKI,
	)
	requireOperatorAdminV1BridgeSuccess(t, selectFailure)
	if selection == "" || !selected.changed {
		t.Fatalf("selection = %q transition=%#v, want opaque changed reservation", selection, selected)
	}
	selectCalls, connectCalls, retryCalls, selectedRef, selectedSKI, _ := service.snapshot()
	if selectCalls != 1 || connectCalls != 0 || retryCalls != 0 || selectedRef != candidateRef || selectedSKI != operatorAdminV1BridgeTestSKI {
		t.Fatalf("service after select calls=%d/%d/%d ref=%q ski=%q, want one identity-bound select and no dial", selectCalls, connectCalls, retryCalls, selectedRef, selectedSKI)
	}

	connected, connectFailure := bridge.connectOperatorAdminV1(context.Background(), selection)
	requireOperatorAdminV1BridgeSuccess(t, connectFailure)
	if !connected.changed {
		t.Fatalf("connect transition = %#v, want changed", connected)
	}
	selectCalls, connectCalls, retryCalls, _, _, reservation := service.snapshot()
	if selectCalls != 1 || connectCalls != 1 || retryCalls != 0 || !reservation.Matches(service.reservation) {
		t.Fatalf("service after connect calls=%d/%d/%d reservation=%#v, want exact reservation once", selectCalls, connectCalls, retryCalls, reservation)
	}

	_, repeatedFailure := bridge.connectOperatorAdminV1(context.Background(), selection)
	if repeatedFailure == "" {
		t.Fatal("consumed selection was accepted a second time")
	}
	_, connectCalls, _, _, _, _ = service.snapshot()
	if connectCalls != 1 {
		t.Fatalf("duplicate connect effects = %d, want 1", connectCalls)
	}
}

func TestOperatorAdminV1BridgeDiscoveryRevisionReplacesSameCandidateIdentityObservation(t *testing.T) {
	fixture := newMSP04BFixture(t, "commit_durable")
	coordinator := fixture.coordinator.(*firstTrustCoordinator)
	service := newOperatorAdminV1BridgeServiceSpy()
	bridge := newOperatorAdminV1Bridge(coordinator, service, &msp04cOrdinalReader{next: 710})
	if transition, failure := bridge.openOperatorAdminV1(context.Background(), time.Minute); failure != "" || !transition.changed {
		t.Fatalf("open transition=%#v failure=%q", transition, failure)
	}

	candidate := shipapi.PairingCandidateRef{
		CandidateRef: "same-private-candidate-ref",
		SKI:          operatorAdminV1BridgeTestSKI,
	}
	coordinator.visiblePairingCandidatesUpdated([]shipapi.PairingCandidateRef{candidate})
	first, failure := bridge.snapshotOperatorAdminV1(context.Background())
	requireOperatorAdminV1BridgeSuccess(t, failure)
	if len(first.discovered) != 1 {
		t.Fatalf("first discovered rows=%d, want 1", len(first.discovered))
	}
	oldObservation := first.discovered[0].reference
	firstRevision := coordinator.candidateSnapshotRevision

	coordinator.visiblePairingCandidatesUpdated([]shipapi.PairingCandidateRef{candidate})
	second, failure := bridge.snapshotOperatorAdminV1(context.Background())
	requireOperatorAdminV1BridgeSuccess(t, failure)
	if len(second.discovered) != 1 {
		t.Fatalf("second discovered rows=%d, want 1", len(second.discovered))
	}
	newObservation := second.discovered[0].reference
	if coordinator.candidateSnapshotRevision != firstRevision+1 {
		t.Fatalf("coordinator discovery revision=%d -> %d, want exactly +1", firstRevision, coordinator.candidateSnapshotRevision)
	}
	if second.discovered[0].ski != first.discovered[0].ski || second.discovered[0].ski != operatorAdminV1BridgeTestSKI {
		t.Fatalf("fixture changed identity across discovery revisions: first=%#v second=%#v", first.discovered[0], second.discovered[0])
	}
	if oldObservation == "" || newObservation == "" || oldObservation == newObservation {
		t.Fatalf("same candidate identity retained observation %q across discovery revision", oldObservation)
	}

	selection, transition, staleFailure := bridge.selectOperatorAdminV1(
		context.Background(), oldObservation, operatorAdminV1BridgeTestSKI,
	)
	if staleFailure != "observation_stale" || selection != "" || transition.changed {
		t.Fatalf("old discovery revision select=%q/%#v/%q, want zero-effect observation_stale", selection, transition, staleFailure)
	}
	selectCalls, connectCalls, retryCalls, _, _, _ := service.snapshot()
	if selectCalls != 0 || connectCalls != 0 || retryCalls != 0 {
		t.Fatalf("old discovery revision reached service calls %d/%d/%d", selectCalls, connectCalls, retryCalls)
	}

	selection, transition, currentFailure := bridge.selectOperatorAdminV1(
		context.Background(), newObservation, operatorAdminV1BridgeTestSKI,
	)
	requireOperatorAdminV1BridgeSuccess(t, currentFailure)
	if selection == "" || !transition.changed {
		t.Fatalf("current discovery revision select=%q/%#v, want changed reservation", selection, transition)
	}
	selectCalls, connectCalls, retryCalls, selectedRef, selectedSKI, _ := service.snapshot()
	if selectCalls != 1 || connectCalls != 0 || retryCalls != 0 || selectedRef != candidate.CandidateRef || selectedSKI != candidate.SKI {
		t.Fatalf("current discovery revision service calls=%d/%d/%d ref=%q ski=%q", selectCalls, connectCalls, retryCalls, selectedRef, selectedSKI)
	}
}

func TestOperatorAdminV1BridgeMapsUnknownServiceOutcomesFailClosed(t *testing.T) {
	fixture := newMSP04BFixture(t, "commit_durable")
	coordinator := fixture.coordinator.(*firstTrustCoordinator)
	service := newOperatorAdminV1BridgeServiceSpy()
	service.selectErr = errors.New("future select outcome")
	bridge := newOperatorAdminV1Bridge(coordinator, service, &msp04cOrdinalReader{next: 720})

	if transition, failure := bridge.openOperatorAdminV1(context.Background(), time.Minute); failure != "" || !transition.changed {
		t.Fatalf("open transition=%#v failure=%q", transition, failure)
	}
	coordinator.visiblePairingCandidatesUpdated([]shipapi.PairingCandidateRef{{
		CandidateRef: "unknown-outcome-private-ref",
		SKI:          operatorAdminV1BridgeTestSKI,
	}})
	snapshot, failure := bridge.snapshotOperatorAdminV1(context.Background())
	requireOperatorAdminV1BridgeSuccess(t, failure)
	selection, transition, selectFailure := bridge.selectOperatorAdminV1(
		context.Background(), snapshot.discovered[0].reference, operatorAdminV1BridgeTestSKI,
	)
	if selectFailure != "unknown_state" || selection != "" || !transition.changed {
		t.Fatalf("unknown select result = %q/%#v/%q, want fail-closed unknown_state that reports consumed internal state", selection, transition, selectFailure)
	}
	_, connectCalls, _, _, _, _ := service.snapshot()
	if connectCalls != 0 {
		t.Fatalf("unknown select outcome caused %d dial effects", connectCalls)
	}
}

func TestOperatorAdminV1BridgeConnectFailureReportsConsumedSelectionChange(t *testing.T) {
	fixture := newMSP04BFixture(t, "commit_durable")
	coordinator := fixture.coordinator.(*firstTrustCoordinator)
	service := newOperatorAdminV1BridgeServiceSpy()
	service.connectErr = shipapi.ErrPairingCandidateReservationUnavailable
	bridge := newOperatorAdminV1Bridge(coordinator, service, &msp04cOrdinalReader{next: 740})
	if transition, failure := bridge.openOperatorAdminV1(context.Background(), time.Minute); failure != "" || !transition.changed {
		t.Fatalf("open transition=%#v failure=%q", transition, failure)
	}
	coordinator.visiblePairingCandidatesUpdated([]shipapi.PairingCandidateRef{{
		CandidateRef: "connect-error-private-ref",
		SKI:          operatorAdminV1BridgeTestSKI,
	}})
	snapshot, failure := bridge.snapshotOperatorAdminV1(context.Background())
	requireOperatorAdminV1BridgeSuccess(t, failure)
	selection, transition, failure := bridge.selectOperatorAdminV1(
		context.Background(), snapshot.discovered[0].reference, operatorAdminV1BridgeTestSKI,
	)
	requireOperatorAdminV1BridgeSuccess(t, failure)
	if selection == "" || !transition.changed {
		t.Fatalf("selection=%q transition=%#v, want current reservation", selection, transition)
	}

	connected, connectFailure := bridge.connectOperatorAdminV1(context.Background(), selection)
	if connectFailure != "observation_stale" || !connected.changed {
		t.Fatalf("failed connect transition=%#v failure=%q, want observation_stale with consumed-state changed=true", connected, connectFailure)
	}
	_, repeatedFailure := bridge.connectOperatorAdminV1(context.Background(), selection)
	if repeatedFailure != "observation_stale" {
		t.Fatalf("consumed selection retry failure=%q, want observation_stale", repeatedFailure)
	}
	_, connectCalls, _, _, _, _ := service.snapshot()
	if connectCalls != 1 {
		t.Fatalf("failed consumed connect effects=%d, want at-most-once 1", connectCalls)
	}
}

func TestOperatorAdminV1BridgeRetryUsesOnlyIdentityAndUntrustResolvesCurrentBindings(t *testing.T) {
	fixture := newMSP04CFixture(t)
	lineage := fixture.store.view.control.associationLineage
	association := msp04cAssociation(1, lineage, true, true, true, true)
	fixture.store.view.associations = []firstTrustAssociationRecord{association}
	retryScope := firstTrustRuntimeRetryScope(firstTrustNormalizedSKI(association.subject))
	fixture.store.view.control.quarantines = []firstTrustQuarantineRecord{
		issue68RetryHold(retryScope, fixture.store.view.control.controlEpoch),
	}
	coordinator := fixture.newCoordinator()
	if got := coordinator.reopen(context.Background()); got != "pairing_closed" {
		t.Fatalf("startup outcome = %q", got)
	}
	issue68AssertRetryReady(t, fixture.store.view.control, retryScope)
	publicationCallsBeforeUntrust := fixture.store.calls()
	service := newOperatorAdminV1BridgeServiceSpy()
	bridge := newOperatorAdminV1Bridge(coordinator, service, &msp04cOrdinalReader{next: 800})

	snapshot, failure := bridge.snapshotOperatorAdminV1(context.Background())
	requireOperatorAdminV1BridgeSuccess(t, failure)
	if len(snapshot.trusted) != 1 {
		t.Fatalf("trusted rows = %d, want 1", len(snapshot.trusted))
	}
	partner := snapshot.trusted[0].reference
	wantSKI := hex.EncodeToString(association.subject)
	associationRef := hex.EncodeToString(association.reference[:])
	if partner == "" || partner == associationRef || strings.Contains(partner, associationRef) {
		t.Fatalf("partner reference %q exposes or omits the durable association binding", partner)
	}
	if snapshot.trusted[0].ski != wantSKI {
		t.Fatalf("trusted SKI = %q, want %q", snapshot.trusted[0].ski, wantSKI)
	}
	if snapshot.trusted[0].shipID != association.service {
		t.Fatalf("trusted SHIP ID = %q, want source-owned %q", snapshot.trusted[0].shipID, association.service)
	}

	retried, retryFailure := bridge.retryTrustedOperatorAdminV1(context.Background(), partner)
	requireOperatorAdminV1BridgeSuccess(t, retryFailure)
	if !retried.changed {
		t.Fatalf("retry transition = %#v, want changed", retried)
	}
	selectCalls, connectCalls, retryCalls, _, _, _ := service.snapshot()
	if selectCalls != 0 || connectCalls != 0 || retryCalls != 1 || service.retrySKI != wantSKI {
		t.Fatalf("retry calls=%d/%d/%d ski=%q, want identity-only RetryTrustedRemote(%q)", selectCalls, connectCalls, retryCalls, service.retrySKI, wantSKI)
	}

	// Advance every durable owner binding after the partner snapshot. The
	// bridge must resolve these current values internally at untrust time; the
	// caller still supplies only its opaque partner reference.
	advanceOperatorAdminV1BridgeControlView(t, fixture, coordinator)
	untrusted, untrustFailure := bridge.untrustOperatorAdminV1(context.Background(), partner)
	requireOperatorAdminV1BridgeSuccess(t, untrustFailure)
	if !untrusted.changed {
		t.Fatalf("untrust transition = %#v, want changed", untrusted)
	}
	assertOperatorAdminV1BridgeAssociationRevoked(t, fixture.store.view, association.reference)
	if fixture.store.calls() != publicationCallsBeforeUntrust+2 {
		t.Fatalf("untrust durable publication calls = %d, want %d", fixture.store.calls(), publicationCallsBeforeUntrust+2)
	}
	if len(bridge.operationIDs) != 0 {
		t.Fatalf("terminal untrust retained %d bridge operation IDs", len(bridge.operationIDs))
	}
}

func TestOperatorAdminV1BridgeRoutesWindowConfirmAndCancelThroughCurrentCoordinatorBindings(t *testing.T) {
	t.Run("open and close", func(t *testing.T) {
		fixture := newMSP04BFixture(t, "commit_durable")
		coordinator := fixture.coordinator.(*firstTrustCoordinator)
		bridge := newOperatorAdminV1Bridge(coordinator, newOperatorAdminV1BridgeServiceSpy(), &msp04cOrdinalReader{next: 900})

		opened, failure := bridge.openOperatorAdminV1(context.Background(), time.Minute)
		requireOperatorAdminV1BridgeSuccess(t, failure)
		if !opened.changed || coordinator.state() != "OPEN_EMPTY" {
			t.Fatalf("open transition=%#v state=%q", opened, coordinator.state())
		}
		closed, failure := bridge.closeOperatorAdminV1(context.Background())
		requireOperatorAdminV1BridgeSuccess(t, failure)
		if !closed.changed || coordinator.state() != "PAIRING_CLOSED" {
			t.Fatalf("close transition=%#v state=%q", closed, coordinator.state())
		}
	})

	t.Run("confirm", func(t *testing.T) {
		fixture := newMSP04BFixture(t, "commit_durable")
		coordinator := fixture.coordinator.(*firstTrustCoordinator)
		bridge := newOperatorAdminV1Bridge(coordinator, newOperatorAdminV1BridgeServiceSpy(), &msp04cOrdinalReader{next: 920})
		remote := msp04cSubject(920)
		_ = openMSP04BCandidate(t, fixture, remote, 921, true)
		snapshot, failure := bridge.snapshotOperatorAdminV1(context.Background())
		requireOperatorAdminV1BridgeSuccess(t, failure)
		if len(snapshot.candidates) != 1 {
			t.Fatalf("candidate rows = %d, want 1", len(snapshot.candidates))
		}
		candidate := snapshot.candidates[0].reference
		confirmed, failure := bridge.confirmOperatorAdminV1(context.Background(), candidate, hex.EncodeToString(remote))
		requireOperatorAdminV1BridgeSuccess(t, failure)
		if !confirmed.changed || !coordinator.trusted(remote) {
			t.Fatalf("confirm transition=%#v trusted=%t", confirmed, coordinator.trusted(remote))
		}
	})

	t.Run("cancel", func(t *testing.T) {
		fixture := newMSP04BFixture(t, "commit_durable")
		coordinator := fixture.coordinator.(*firstTrustCoordinator)
		bridge := newOperatorAdminV1Bridge(coordinator, newOperatorAdminV1BridgeServiceSpy(), &msp04cOrdinalReader{next: 940})
		remote := msp04cSubject(940)
		_ = openMSP04BCandidate(t, fixture, remote, 941, true)
		snapshot, failure := bridge.snapshotOperatorAdminV1(context.Background())
		requireOperatorAdminV1BridgeSuccess(t, failure)
		candidate := snapshot.candidates[0].reference
		cancelled, failure := bridge.cancelOperatorAdminV1(context.Background(), candidate)
		requireOperatorAdminV1BridgeSuccess(t, failure)
		if !cancelled.changed {
			t.Fatalf("cancel transition=%#v, want changed", cancelled)
		}
		if _, _, _, _, _, _, ok := coordinator.candidate(); ok {
			t.Fatal("cancel left a coordinator candidate")
		}
	})
}

func TestOperatorAdminV1BridgeSnapshotReportsInvalidDiscoveryAsDegraded(t *testing.T) {
	fixture := newMSP04BFixture(t, "commit_durable")
	coordinator := fixture.coordinator.(*firstTrustCoordinator)
	service := newOperatorAdminV1BridgeServiceSpy()
	bridge := newOperatorAdminV1Bridge(coordinator, service, &msp04cOrdinalReader{next: 955})

	coordinator.visiblePairingCandidatesUpdated([]shipapi.PairingCandidateRef{{
		CandidateRef: "",
		SKI:          operatorAdminV1BridgeTestSKI,
	}})
	snapshot, failure := bridge.snapshotOperatorAdminV1(context.Background())
	requireOperatorAdminV1BridgeSuccess(t, failure)
	if snapshot.discovery != "invalid" || snapshot.degraded != "discovery_unavailable" || len(snapshot.discovered) != 0 {
		t.Fatalf("invalid discovery snapshot=%#v, want sanitized degraded status and no discovered rows", snapshot)
	}

	selection, transition, selectFailure := bridge.selectOperatorAdminV1(
		context.Background(), "unknown-observation", operatorAdminV1BridgeTestSKI,
	)
	if selectFailure != "observation_stale" || selection != "" || transition.changed {
		t.Fatalf("invalid discovery select=%q/%#v/%q, want zero-effect observation_stale", selection, transition, selectFailure)
	}
	selectCalls, connectCalls, retryCalls, _, _, _ := service.snapshot()
	if selectCalls != 0 || connectCalls != 0 || retryCalls != 0 {
		t.Fatalf("invalid discovery reached service calls %d/%d/%d", selectCalls, connectCalls, retryCalls)
	}
}

func TestOperatorAdminV1BridgeTLSBoundIncompleteCandidateRemainsCancellable(t *testing.T) {
	fixture := newIssue60Fixture(t)
	bridge := newOperatorAdminV1Bridge(fixture.coordinator, newOperatorAdminV1BridgeServiceSpy(), &msp04cOrdinalReader{next: 958})

	snapshot, failure := bridge.snapshotOperatorAdminV1(context.Background())
	requireOperatorAdminV1BridgeSuccess(t, failure)
	if len(snapshot.candidates) != 1 || snapshot.candidates[0].state != "association_incomplete" ||
		snapshot.candidates[0].associationComplete || snapshot.candidates[0].expiresAt.IsZero() {
		t.Fatalf("TLS-bound incomplete candidate facts=%#v, want cancellable association_incomplete row", snapshot.candidates)
	}

	cancelled, cancelFailure := bridge.cancelOperatorAdminV1(context.Background(), snapshot.candidates[0].reference)
	requireOperatorAdminV1BridgeSuccess(t, cancelFailure)
	if !cancelled.changed || cancelled.outcome != "cancelled" {
		t.Fatalf("incomplete candidate cancel=%#v, want cancelled changed", cancelled)
	}
	if fixture.coordinator.trusted(fixture.remote) {
		t.Fatal("incomplete candidate cancel published durable trust")
	}
	if fixture.service.registerCount() != 0 || fixture.service.unregisterCount() != 0 {
		t.Fatalf("incomplete candidate cancel register/unregister=%d/%d, want 0/0", fixture.service.registerCount(), fixture.service.unregisterCount())
	}
	if _, _, _, _, _, _, ok := fixture.coordinator.candidate(); ok {
		t.Fatal("incomplete candidate cancel retained coordinator candidate")
	}
	assertMSP04BCommitCount(t, fixture.base.store, 0)
}

func TestOperatorAdminV1BridgeCancelAfterTransientConfirmRevokesTargetWithoutDurableTrust(t *testing.T) {
	fixture := newIssue60Fixture(t)
	fixture.shipID()
	bridge := newOperatorAdminV1Bridge(fixture.coordinator, newOperatorAdminV1BridgeServiceSpy(), &msp04cOrdinalReader{next: 960})
	snapshot, failure := bridge.snapshotOperatorAdminV1(context.Background())
	requireOperatorAdminV1BridgeSuccess(t, failure)
	if len(snapshot.candidates) != 1 {
		t.Fatalf("pre-confirm candidate rows=%d, want 1", len(snapshot.candidates))
	}
	candidate := snapshot.candidates[0].reference
	confirmed, confirmFailure := bridge.confirmOperatorAdminV1(context.Background(), candidate, issue56SKIA)
	requireOperatorAdminV1BridgeSuccess(t, confirmFailure)
	if !confirmed.changed || confirmed.outcome != "transient_trusted" {
		t.Fatalf("confirm transition=%#v, want transient_trusted changed", confirmed)
	}
	if fixture.service.registerCount() != 1 || fixture.coordinator.trusted(fixture.remote) {
		t.Fatalf("transient confirm registers=%d durable-trusted=%t, want 1/false", fixture.service.registerCount(), fixture.coordinator.trusted(fixture.remote))
	}
	assertMSP04BCommitCount(t, fixture.base.store, 0)
	postConfirm, snapshotFailure := bridge.snapshotOperatorAdminV1(context.Background())
	requireOperatorAdminV1BridgeSuccess(t, snapshotFailure)
	if len(postConfirm.candidates) != 1 || postConfirm.candidates[0].state != "transient_trusted" ||
		postConfirm.candidates[0].expiresAt.IsZero() || !postConfirm.candidates[0].associationComplete {
		t.Fatalf("post-confirm transient candidate facts=%#v, want fresh cancellable association-complete row", postConfirm.candidates)
	}
	candidate = postConfirm.candidates[0].reference

	cancelled, cancelFailure := bridge.cancelOperatorAdminV1(context.Background(), candidate)
	requireOperatorAdminV1BridgeSuccess(t, cancelFailure)
	if !cancelled.changed || cancelled.outcome != "cancelled" {
		t.Fatalf("targeted post-confirm cancel transition=%#v, want cancelled changed", cancelled)
	}
	if fixture.service.unregisterCount() != 1 {
		t.Fatalf("transient post-confirm cancel unregistrations=%d, want 1", fixture.service.unregisterCount())
	}
	if fixture.coordinator.trusted(fixture.remote) {
		t.Fatal("post-confirm cancel published durable trust")
	}
	if _, _, _, _, _, _, ok := fixture.coordinator.candidate(); ok {
		t.Fatal("post-confirm cancel retained the transient candidate")
	}
	assertMSP04BCommitCount(t, fixture.base.store, 0)
}

func TestOperatorAdminV1BridgeMapsEveryReleasedTrustedRemoteRetryError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "invalid remote SKI", err: shipapi.ErrInvalidRemoteSKI, want: "invalid_request"},
		{name: "retry unavailable", err: shipapi.ErrTrustedRemoteRetryUnavailable, want: "discovery_unavailable"},
		{name: "not trusted", err: shipapi.ErrTrustedRemoteRetryNotTrusted, want: "trust_denied"},
		{name: "already connected", err: shipapi.ErrTrustedRemoteRetryConnected, want: "candidate_busy"},
		{name: "retry busy", err: shipapi.ErrTrustedRemoteRetryBusy, want: "candidate_busy"},
		{name: "observation stale", err: shipapi.ErrTrustedRemoteObservationStale, want: "observation_stale"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, err := range []error{test.err, fmt.Errorf("released retry error: %w", test.err)} {
				if got := mapOperatorAdminV1RetryError(err); got != test.want {
					t.Fatalf("mapOperatorAdminV1RetryError(%v)=%q, want closed category %q", err, got, test.want)
				}
			}
		})
	}
	if got := mapOperatorAdminV1RetryError(errors.New("future retry failure")); got != "unknown_state" {
		t.Fatalf("unknown retry error mapping=%q, want fail-closed unknown_state", got)
	}
}

func TestOperatorAdminV1BridgeRetiresTerminalOperationReferencesBeyondCapacity(t *testing.T) {
	fixture := newMSP04BFixture(t, "commit_durable")
	coordinator := fixture.coordinator.(*firstTrustCoordinator)
	bridge := newOperatorAdminV1Bridge(coordinator, newOperatorAdminV1BridgeServiceSpy(), &msp04cOrdinalReader{next: 1_200})
	for index := 0; index < 160; index++ {
		opened, openFailure := bridge.openOperatorAdminV1(context.Background(), time.Minute)
		requireOperatorAdminV1BridgeSuccess(t, openFailure)
		if !opened.changed {
			t.Fatalf("open %d transition=%#v, want changed", index, opened)
		}
		closed, closeFailure := bridge.closeOperatorAdminV1(context.Background())
		requireOperatorAdminV1BridgeSuccess(t, closeFailure)
		if !closed.changed {
			t.Fatalf("close %d transition=%#v, want changed", index, closed)
		}
		if len(bridge.operations) != 0 {
			t.Fatalf("terminal operation %d retained %d bridge references", index, len(bridge.operations))
		}
	}
}

func TestOperatorAdminV1BridgeSnapshotIsBoundedSanitizedAndNeverPartial(t *testing.T) {
	fixture := newMSP04BFixture(t, "commit_durable")
	coordinator := fixture.coordinator.(*firstTrustCoordinator)
	bridge := newOperatorAdminV1Bridge(coordinator, newOperatorAdminV1BridgeServiceSpy(), &msp04cOrdinalReader{next: 1_000})
	if transition, failure := bridge.openOperatorAdminV1(context.Background(), time.Minute); failure != "" || !transition.changed {
		t.Fatalf("open transition=%#v failure=%q", transition, failure)
	}

	candidates := make([]shipapi.PairingCandidateRef, firstTrustMaximumDiscoveredCandidates)
	for index := range candidates {
		candidates[index] = shipapi.PairingCandidateRef{
			CandidateRef: "owner-private-ref-" + operatorAdminV1BridgeIndex(index),
			SKI:          operatorAdminV1BridgeTestSKI,
		}
	}
	coordinator.visiblePairingCandidatesUpdated(candidates)
	snapshot, failure := bridge.snapshotOperatorAdminV1(context.Background())
	requireOperatorAdminV1BridgeSuccess(t, failure)
	if len(snapshot.discovered) != firstTrustMaximumDiscoveredCandidates {
		t.Fatalf("bounded snapshot rows = %d, want %d", len(snapshot.discovered), firstTrustMaximumDiscoveredCandidates)
	}
	assertOperatorAdminV1BridgeSnapshotSanitized(t, snapshot)
	privateRefs := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		privateRefs[candidate.CandidateRef] = struct{}{}
	}
	for index, row := range snapshot.discovered {
		_, exactLeak := privateRefs[row.reference]
		containsLeak := false
		for privateRef := range privateRefs {
			if strings.Contains(row.reference, privateRef) {
				containsLeak = true
				break
			}
		}
		if row.reference == "" || exactLeak || containsLeak {
			t.Fatalf("row %d reference %q exposes or omits candidate_ref", index, row.reference)
		}
	}

	overflow := append(append([]shipapi.PairingCandidateRef(nil), candidates...), shipapi.PairingCandidateRef{
		CandidateRef: "owner-private-ref-overflow",
		SKI:          operatorAdminV1BridgeTestSKI,
	})
	coordinator.visiblePairingCandidatesUpdated(overflow)
	degraded, overflowFailure := bridge.snapshotOperatorAdminV1(context.Background())
	requireOperatorAdminV1BridgeSuccess(t, overflowFailure)
	if degraded.discovery != "invalid" || degraded.degraded != "discovery_unavailable" || len(degraded.discovered) != 0 {
		t.Fatalf("capacity-invalid discovery snapshot=%#v, want sanitized degraded status and no partial rows", degraded)
	}
}

const operatorAdminV1BridgeTestSKI = "0123456789abcdef0123456789abcdef01234567"

type operatorAdminV1BridgeServiceSpy struct {
	mu sync.Mutex

	reservation shipapi.PairingCandidateReservation
	selectErr   error
	connectErr  error
	retryErr    error

	selectCalls  int
	connectCalls int
	retryCalls   int
	selectedRef  string
	selectedSKI  string
	retrySKI     string
	connected    shipapi.PairingCandidateReservation
}

func newOperatorAdminV1BridgeServiceSpy() *operatorAdminV1BridgeServiceSpy {
	var token [32]byte
	token[0] = 1
	return &operatorAdminV1BridgeServiceSpy{reservation: shipapi.NewPairingCandidateReservation(token)}
}

func (service *operatorAdminV1BridgeServiceSpy) SelectPairingCandidate(candidateRef, expectedSKI string) (shipapi.PairingCandidateReservation, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.selectCalls++
	service.selectedRef = candidateRef
	service.selectedSKI = expectedSKI
	if service.selectErr != nil {
		return shipapi.PairingCandidateReservation{}, service.selectErr
	}
	return service.reservation, nil
}

func (service *operatorAdminV1BridgeServiceSpy) ConnectPairingCandidate(reservation shipapi.PairingCandidateReservation) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.connectCalls++
	service.connected = reservation
	return service.connectErr
}

func (service *operatorAdminV1BridgeServiceSpy) RetryTrustedRemote(expectedSKI string) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.retryCalls++
	service.retrySKI = expectedSKI
	return service.retryErr
}

func (service *operatorAdminV1BridgeServiceSpy) snapshot() (int, int, int, string, string, shipapi.PairingCandidateReservation) {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.selectCalls, service.connectCalls, service.retryCalls, service.selectedRef, service.selectedSKI, service.connected
}

func requireOperatorAdminV1BridgeSuccess[T ~string](t *testing.T, failure T) {
	t.Helper()
	if failure != "" {
		t.Fatalf("unexpected operator AdminV1 bridge failure %q", failure)
	}
}

func advanceOperatorAdminV1BridgeControlView(t *testing.T, fixture *msp04cFixture, coordinator *firstTrustCoordinator) {
	t.Helper()
	next := fixture.store.nextView()
	fixture.store.mu.Lock()
	fixture.store.view = cloneFirstTrustControlView(next)
	fixture.store.mu.Unlock()

	fixture.anchor.mu.Lock()
	fixture.anchor.record.manifestGenerationHighWater = next.manifest.current.sequence
	fixture.anchor.record.controlEpochHighWater = next.control.controlEpoch
	anchor := cloneFirstTrustAnchorRecord(fixture.anchor.record)
	fixture.anchor.mu.Unlock()

	coordinator.mu.Lock()
	coordinator.controlView = cloneFirstTrustControlView(next)
	coordinator.anchorRecord = anchor
	coordinator.mu.Unlock()
}

func assertOperatorAdminV1BridgeAssociationRevoked(t *testing.T, view firstTrustControlView, reference [32]byte) {
	t.Helper()
	for _, association := range view.associations {
		if association.reference == reference && (association.active || association.trusted || association.allowlisted || association.reconnectable) {
			t.Fatal("untrust retained an active durable association capability")
		}
	}
	for _, tombstone := range view.control.tombstones {
		if tombstone.associationRef == reference && tombstone.effectiveGeneration.sequence != 0 {
			return
		}
	}
	t.Fatal("untrust did not publish a generation-bound tombstone")
}

func assertOperatorAdminV1BridgeSnapshotSanitized(t *testing.T, snapshot any) {
	t.Helper()
	forbidden := []string{
		"candidate_ref", "candidateref", "nonce", "store", "generation", "association",
		"control", "manifest", "path", "private", "pem", "token", "keybytes",
	}
	var visit func(reflect.Value, string)
	visit = func(value reflect.Value, path string) {
		if !value.IsValid() {
			return
		}
		for value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
			if value.IsNil() {
				return
			}
			value = value.Elem()
		}
		switch value.Kind() {
		case reflect.Struct:
			typ := value.Type()
			for index := 0; index < value.NumField(); index++ {
				field := typ.Field(index)
				normalized := strings.ToLower(strings.ReplaceAll(field.Name, "_", ""))
				if normalized == "associationcomplete" && field.Type.Kind() == reflect.Bool {
					visit(value.Field(index), path+"."+field.Name)
					continue
				}
				for _, fragment := range forbidden {
					fragment = strings.ReplaceAll(fragment, "_", "")
					if strings.Contains(normalized, fragment) {
						t.Fatalf("snapshot fact %s.%s leaks private binding", path, field.Name)
					}
				}
				visit(value.Field(index), path+"."+field.Name)
			}
		case reflect.Slice:
			if value.Type().Elem().Kind() == reflect.Uint8 {
				t.Fatalf("snapshot fact %s exposes private bytes", path)
			}
			for index := 0; index < value.Len(); index++ {
				visit(value.Index(index), path)
			}
		case reflect.Array:
			if value.Type().Elem().Kind() == reflect.Uint8 {
				t.Fatalf("snapshot fact %s exposes private bytes", path)
			}
		}
	}
	visit(reflect.ValueOf(snapshot), "snapshot")
}

func operatorAdminV1BridgeIndex(index int) string {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	if index == 0 {
		return "0"
	}
	var encoded [8]byte
	position := len(encoded)
	for index > 0 {
		position--
		encoded[position] = alphabet[index%len(alphabet)]
		index /= len(alphabet)
	}
	return string(encoded[position:])
}
