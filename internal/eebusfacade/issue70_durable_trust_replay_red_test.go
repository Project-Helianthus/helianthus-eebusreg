package eebusfacade

import (
	"context"
	"encoding/hex"
	"slices"
	"testing"
	"time"
)

func TestIssue70ReplaysDurableTrustAfterSetupBeforeListenerStart(t *testing.T) {
	fixture := newMSP04CRRuntimeFixture(t, 70_001)
	firstService := newMSP04CRService()
	first, reader := fixture.acquire(t, firstService, "issue70-first")
	fixture.recoverUnavailableHostKey(t, first)
	fixture.pairRemote(t, first, reader, 70_002)
	if err := first.Close(); err != nil {
		t.Fatalf("close paired runtime: %v", err)
	}

	fixture.events.clear()
	restartedService := newMSP04CRService()
	restarted, _ := fixture.acquire(t, restartedService, "issue70-restart")
	defer restarted.Close()

	events := fixture.events.snapshot()
	setup := slices.Index(events, "listener_setup")
	register := slices.Index(events, "register_remote")
	if setup < 0 || register <= setup {
		t.Fatalf("restart event order = %v, want setup before durable trust replay", events)
	}
	if count := countIssue70Events(events, "register_remote"); count != 1 {
		t.Fatalf("restart registrations = %d, want exactly 1: %v", count, events)
	}
	if slices.Contains(events, "listener_start") {
		t.Fatalf("listener started during acquire: %v", events)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- restarted.Run(ctx, func([]byte) {})
	}()
	waitIssue70Event(t, fixture.events, "listener_start")
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run restarted runtime: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("restarted runtime did not stop")
	}

	events = fixture.events.snapshot()
	register = slices.Index(events, "register_remote")
	start := slices.Index(events, "listener_start")
	if register < 0 || start <= register {
		t.Fatalf("runtime event order = %v, want durable trust replay before listener start", events)
	}
}

func TestIssue70DoesNotPromoteMaterialAdmissionWithoutDurableTrust(t *testing.T) {
	fixture := newMSP04CRRuntimeFixture(t, 70_010)
	service := newMSP04CRService()
	backend, _ := fixture.acquire(t, service, "issue70-unpaired")
	defer backend.Close()

	if events := service.eventsSnapshot(); slices.Contains(events, "register_remote") {
		t.Fatalf("unpaired material admission registered SHIP trust: %v", events)
	}
}

func TestIssue70RecoveredTrustReplayIsConfiguredDeterministicAndFailClosed(t *testing.T) {
	first := msp04cSubject(70_020)
	second := msp04cSubject(70_021)
	unconfigured := msp04cSubject(70_022)
	coordinator := &firstTrustCoordinator{
		phase:    firstTrustPairingClosed,
		recovery: "PAIRED_TRUSTED",
		trustedRemotes: map[string]string{
			string(second):       "ship-second",
			string(unconfigured): "ship-unconfigured",
			string(first):        "ship-first",
		},
	}
	configured := map[string]struct{}{
		hex.EncodeToString(second): {},
		hex.EncodeToString(first):  {},
	}
	got, err := recoveredRuntimeTrustSKIs(configured, coordinator)
	if err != nil {
		t.Fatalf("recover configured durable trust: %v", err)
	}
	want := []string{hex.EncodeToString(first), hex.EncodeToString(second)}
	if !slices.Equal(got, want) {
		t.Fatalf("recovered SKIs = %v, want %v", got, want)
	}

	coordinator.trustedRemotes["short"] = "ship-invalid"
	if _, err := recoveredRuntimeTrustSKIs(configured, coordinator); err == nil {
		t.Fatal("malformed durable trust was not rejected")
	}
}

func countIssue70Events(events []string, target string) int {
	count := 0
	for _, event := range events {
		if event == target {
			count++
		}
	}
	return count
}

func waitIssue70Event(t *testing.T, events *msp04crEventLog, target string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if slices.Contains(events.snapshot(), target) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("event %q was not observed: %v", target, events.snapshot())
}
