package eebusfacade

import "testing"

func TestIssue62PreConfirmEvidenceCommitsAfterTransientRegistration(t *testing.T) {
	fixture := newIssue60Fixture(t)
	fixture.service.onRegister = func(string) {
		fixture.base.store.events.add("transient_register")
	}

	fixture.coordinator.serviceShipIDUpdate(fixture.remote, fixture.binding.connection+1, "stale-ship-id")
	fixture.coordinator.connectionCompleted(fixture.remote, fixture.binding.connection+1)
	fixture.shipID()
	fixture.completed()

	assertMSP04BCommitCount(t, fixture.base.store, 0)
	if fixture.coordinator.trusted(fixture.remote) {
		t.Fatal("pre-confirm evidence published durable trust")
	}
	if got := fixture.confirm("confirm"); got != "transient_trusted" {
		t.Fatalf("exact confirmation = %q, want transient_trusted", got)
	}

	if got := fixture.service.registerCount(); got != 1 {
		t.Fatalf("RegisterRemoteSKI calls = %d, want 1", got)
	}
	assertMSP04BCommitCount(t, fixture.base.store, 1)
	fixture.base.effects.assertOrder(t, "transient_register", "commit")
	if !fixture.coordinator.trusted(fixture.remote) {
		t.Fatal("latched exact-connection evidence did not publish durable trust")
	}
	if got := fixture.confirm("confirm"); got != "trusted" {
		t.Fatalf("terminal confirmation replay = %q, want trusted", got)
	}

	fixture.shipID()
	fixture.completed()
	fixture.coordinator.serviceShipIDUpdate(fixture.remote, fixture.binding.connection+1, "stale-ship-id")
	fixture.coordinator.connectionCompleted(fixture.remote, fixture.binding.connection+1)
	assertMSP04BCommitCount(t, fixture.base.store, 1)
}
