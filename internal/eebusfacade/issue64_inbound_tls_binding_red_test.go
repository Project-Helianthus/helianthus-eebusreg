package eebusfacade

import (
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
