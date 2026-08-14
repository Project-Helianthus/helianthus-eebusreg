package eebusfacade

import (
	"context"
	"encoding/hex"
	"errors"
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
	if snapshot.discovered[0].ski != operatorAdminV1BridgeTestSKI {
		t.Fatalf("discovered SKI = %q, want complete owner identity", snapshot.discovered[0].ski)
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
	if selectFailure != "unknown_state" || selection != "" || transition.changed {
		t.Fatalf("unknown select result = %q/%#v/%q, want zero fail-closed unknown_state", selection, transition, selectFailure)
	}
	_, connectCalls, _, _, _, _ := service.snapshot()
	if connectCalls != 0 {
		t.Fatalf("unknown select outcome caused %d dial effects", connectCalls)
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
	partial, overflowFailure := bridge.snapshotOperatorAdminV1(context.Background())
	if overflowFailure == "" {
		t.Fatal("invalid over-capacity discovery snapshot was accepted")
	}
	if !reflect.ValueOf(partial).IsZero() {
		t.Fatalf("capacity failure returned partial facts %#v", partial)
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
