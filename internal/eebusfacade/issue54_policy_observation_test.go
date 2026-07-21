package eebusfacade

import (
	"context"
	"testing"
	"time"
)

func TestIssue54OpeningPairingOnlyChangesLocalRegistration(t *testing.T) {
	harness := newIssue54PolicyHarness(t, false)

	if got := harness.resources.coordinator.openPairingWindow(context.Background(), msp045RunToken(t, "registration-only-open"), time.Minute); got != "open_empty" {
		t.Fatalf("open pairing window = %q", got)
	}
	time.Sleep(20 * time.Millisecond)
	state := snapshotIssue54PolicyState(harness.service)
	if len(state.pairingRegistration) == 0 || !state.pairingRegistration[len(state.pairingRegistration)-1] {
		t.Fatalf("pairing registration events = %v, want final register=true", state.pairingRegistration)
	}
	if len(state.registered) != 0 || len(state.cancelled) != 0 {
		t.Fatalf("opening pairing performed remote effects: registered=%v cancelled=%v", state.registered, state.cancelled)
	}
	if _, _, _, _, _, _, ok := harness.resources.coordinator.candidate(); ok {
		t.Fatal("opening pairing fabricated a candidate")
	}
	snapshot, _ := msp045Capture(t, harness.handler)
	issue54AssertNoRemoteEvidence(t, snapshot)
}

func TestIssue54ConfiguredTrustedSKIIsPolicyWithoutSyntheticObservation(t *testing.T) {
	harness := newMSP045ProductHarness(t, func(setup *msp045ProductSetup) {
		setup.suppressVisible = true
	})
	time.Sleep(20 * time.Millisecond)
	state := snapshotIssue54PolicyState(harness.service)
	if len(state.registered) != 0 {
		t.Fatalf("durable trust policy produced registration observations = %v", state.registered)
	}
	snapshot, _ := msp045Capture(t, harness.handler)
	issue54AssertNoRemoteEvidence(t, snapshot)
	if harness.resources.coordinator.recoveryState() != "PAIRED_TRUSTED" {
		t.Fatalf("durable trust recovery = %q", harness.resources.coordinator.recoveryState())
	}
}

func newIssue54PolicyHarness(t *testing.T, discovery bool) *msp045RuntimeHarness {
	t.Helper()
	pretrusted := false
	return newMSP045ProductHarness(t, func(setup *msp045ProductSetup) {
		setup.view.associations = nil
		setup.remotePretrusted = &pretrusted
		setup.discoveryEnabled = discovery
		setup.suppressVisible = true
	})
}

type issue54PolicyState struct {
	registered          []string
	cancelled           []string
	pairingRegistration []bool
}

func snapshotIssue54PolicyState(service *msp045Service) issue54PolicyState {
	service.mu.Lock()
	defer service.mu.Unlock()
	return issue54PolicyState{
		registered:          append([]string(nil), service.registered...),
		cancelled:           append([]string(nil), service.cancelled...),
		pairingRegistration: append([]bool(nil), service.pairingRegistration...),
	}
}
