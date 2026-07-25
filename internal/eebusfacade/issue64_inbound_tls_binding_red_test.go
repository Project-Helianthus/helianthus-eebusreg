package eebusfacade

import (
	"sync"
	"testing"

	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
)

func TestIssue64SelectedInboundPairingRequestBindsTLSBeforeTransientTrust(t *testing.T) {
	fixture := newIssue60FixtureWithOptions(t, "commit_durable", false)

	if got := fixture.confirm("confirm-inbound"); got != "association_incomplete" {
		t.Fatalf("confirmation before inbound TLS evidence = %q, want association_incomplete", got)
	}

	fixture.facade.ServicePairingDetailUpdate(
		issue56SKIA,
		shipapi.NewConnectionStateDetail(shipapi.ConnectionStateReceivedPairingRequest, nil),
	)

	if got := fixture.confirm("confirm-inbound"); got != "transient_trusted" {
		t.Fatalf("confirmation after inbound TLS evidence = %q, want transient_trusted", got)
	}
	if got := fixture.service.registerCount(); got != 1 {
		t.Fatalf("transient registration calls = %d, want 1", got)
	}
	if fixture.coordinator.trusted(fixture.remote) {
		t.Fatal("inbound TLS evidence published durable trust before SHIP completion")
	}

	fixture.shipID()
	fixture.completed()

	if got := fixture.confirm("confirm-inbound"); got != "trusted" {
		t.Fatalf("terminal confirmation replay = %q, want trusted", got)
	}
	if !fixture.coordinator.trusted(fixture.remote) {
		t.Fatal("SHIP completion did not publish durable trust")
	}
}

func TestIssue64InboundPairingRequestRejectsWrongSKIWithoutBindingSelectedCandidate(t *testing.T) {
	fixture := newIssue60FixtureWithOptions(t, "commit_durable", false)

	fixture.facade.ServicePairingDetailUpdate(
		issue56SKIB,
		shipapi.NewConnectionStateDetail(shipapi.ConnectionStateReceivedPairingRequest, nil),
	)

	if got := fixture.confirm("confirm-wrong-ski"); got != "association_incomplete" {
		t.Fatalf("confirmation after wrong SKI = %q, want association_incomplete", got)
	}
	if got := fixture.service.registerCount(); got != 0 {
		t.Fatalf("registration calls after wrong SKI = %d, want 0", got)
	}

	fixture.facade.ServicePairingDetailUpdate(
		issue56SKIA,
		shipapi.NewConnectionStateDetail(shipapi.ConnectionStateReceivedPairingRequest, nil),
	)
	if got := fixture.confirm("confirm-wrong-ski"); got != "transient_trusted" {
		t.Fatalf("confirmation after exact SKI = %q, want transient_trusted", got)
	}
}

func TestIssue64DuplicateAndConcurrentInboundPairingRequestsBindExactlyOnce(t *testing.T) {
	fixture := newIssue60FixtureWithOptions(t, "commit_durable", false)
	const callbacks = 32

	var group sync.WaitGroup
	group.Add(callbacks)
	for range callbacks {
		go func() {
			defer group.Done()
			fixture.facade.ServicePairingDetailUpdate(
				issue56SKIA,
				shipapi.NewConnectionStateDetail(shipapi.ConnectionStateReceivedPairingRequest, nil),
			)
		}()
	}
	group.Wait()

	const confirmations = 32
	results := make(chan string, confirmations)
	group.Add(confirmations)
	for range confirmations {
		go func() {
			defer group.Done()
			results <- fixture.confirm("confirm-concurrent-inbound")
		}()
	}
	group.Wait()
	close(results)

	for result := range results {
		if result != "transient_trusted" {
			t.Fatalf("concurrent confirmation = %q, want transient_trusted", result)
		}
	}
	if got := fixture.service.registerCount(); got != 1 {
		t.Fatalf("concurrent transient registration calls = %d, want 1", got)
	}
	if fixture.coordinator.trusted(fixture.remote) {
		t.Fatal("duplicate inbound TLS callbacks published durable trust")
	}
}
