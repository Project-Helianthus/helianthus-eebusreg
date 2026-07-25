package eebusfacade

import (
	"context"
	"testing"

	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
)

func TestIssue60ExactTLSBoundConfirmationStagesTransientTrust(t *testing.T) {
	fixture := newIssue60Fixture(t)
	generation := fixture.base.store.SelectedGeneration()

	if got := fixture.confirm("confirm"); got != "transient_trusted" {
		t.Fatalf("exact TLS-bound OOB confirmation = %q, want transient_trusted", got)
	}
	if got := fixture.service.registerCount(); got != 1 {
		t.Fatalf("RegisterRemoteSKI calls = %d, want 1", got)
	}
	assertMSP04BCommitCount(t, fixture.base.store, 0)
	if got := fixture.base.store.SelectedGeneration(); got != generation {
		t.Fatalf("transient confirmation advanced durable generation to %d, want %d", got, generation)
	}
	if fixture.coordinator.trusted(fixture.remote) {
		t.Fatal("transient confirmation published final durable trust")
	}
}

type issue60Fixture struct {
	base        msp04bFixture
	coordinator *firstTrustCoordinator
	facade      *firstTrustFacade
	service     *issue60Service
	remote      []byte
	binding     msp04bBindings
}

func newIssue60Fixture(t *testing.T) *issue60Fixture {
	t.Helper()
	base := newMSP04BFixture(t, "commit_durable")
	coordinator := base.coordinator.(*firstTrustCoordinator)
	service := &issue60Service{}
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
	service.queue = func(_ string, expectedSKI string) error {
		facade.RemoteSKIConnected(nil, expectedSKI)
		return nil
	}
	facade.VisiblePairingCandidatesUpdated(nil, []shipapi.PairingCandidateRef{{
		CandidateRef: "candidate-a",
		SKI:          issue56SKIA,
	}})
	if got := coordinator.selectCandidate(context.Background(), "select", "candidate-a", issue56SKIA); got != "candidate_queued" {
		t.Fatalf("select candidate = %q", got)
	}
	remote, _, ok := decodeFirstTrustSKI(issue56SKIA)
	if !ok {
		t.Fatal("test SKI is invalid")
	}
	fingerprint, nonce, expiresAt, connection, storeGeneration, complete, ok := coordinator.candidate()
	if !ok || complete || fingerprint != issue56SKIA || connection == 0 || storeGeneration == 0 {
		t.Fatalf("TLS-bound candidate = fingerprint=%q connection=%d store=%d complete=%t ok=%t", fingerprint, connection, storeGeneration, complete, ok)
	}
	return &issue60Fixture{
		base:        base,
		coordinator: coordinator,
		facade:      facade,
		service:     service,
		remote:      remote,
		binding: msp04bBindings{
			fingerprint: fingerprint,
			nonce:       nonce,
			expiresAt:   expiresAt,
			connection:  connection,
			store:       storeGeneration,
		},
	}
}

func (fixture *issue60Fixture) confirm(key string) string {
	return confirmMSP04B(fixture.coordinator, key, fixture.binding)
}

type issue60Service struct {
	msp04bServiceSpy
	queue func(string, string) error
}

func (service *issue60Service) QueuePairingCandidate(candidateRef, expectedSKI string) error {
	return service.queue(candidateRef, expectedSKI)
}

func (service *issue60Service) registerCount() int {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.registers
}
