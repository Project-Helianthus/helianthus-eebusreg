package eebusfacade

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"
)

var msp04cCombinedStates = []string{
	"NO_LOCAL_IDENTITY",
	"UNPAIRED_LOCKED",
	"PAIRED_TRUSTED",
	"REVOKED",
	"QUARANTINED",
	"CORRUPT_STORE",
}

func TestMSP04CCombinedStateCrossProductIsClosed(t *testing.T) {
	allowed := map[string][]string{
		"DISABLED":          {"NO_LOCAL_IDENTITY", "QUARANTINED", "CORRUPT_STORE"},
		"PAIRING_CLOSED":    {"UNPAIRED_LOCKED", "PAIRED_TRUSTED", "REVOKED"},
		"OPEN_EMPTY":        {"UNPAIRED_LOCKED", "REVOKED"},
		"CANDIDATE_PENDING": {"UNPAIRED_LOCKED", "REVOKED"},
		"COMMITTING":        {"UNPAIRED_LOCKED", "REVOKED"},
	}

	for phase, recoveryStates := range allowed {
		for _, recoveryState := range msp04cCombinedStates {
			want := slices.Contains(recoveryStates, recoveryState)
			if got := firstTrustProductAllowed(phase, recoveryState); got != want {
				t.Fatalf("product decision for %s/%s = %t, want %t", phase, recoveryState, got, want)
			}
		}
	}
	for _, product := range [][2]string{{"", "UNPAIRED_LOCKED"}, {"PAIRING_CLOSED", ""}, {"UNKNOWN", "QUARANTINED"}, {"DISABLED", "UNKNOWN"}} {
		if firstTrustProductAllowed(product[0], product[1]) {
			t.Fatalf("unknown product %s/%s was admitted", product[0], product[1])
		}
	}

	for _, test := range []struct {
		phase, recovery, structural string
		wantPhase, wantRecovery     string
	}{
		{phase: "OPEN_EMPTY", recovery: "PAIRED_TRUSTED", wantPhase: "DISABLED", wantRecovery: "QUARANTINED"},
		{phase: "COMMITTING", recovery: "NO_LOCAL_IDENTITY", wantPhase: "DISABLED", wantRecovery: "QUARANTINED"},
		{phase: "PAIRING_CLOSED", recovery: "CORRUPT_STORE", structural: "CORRUPT_STORE", wantPhase: "DISABLED", wantRecovery: "CORRUPT_STORE"},
	} {
		phase, recovery := normalizeFirstTrustProduct(test.phase, test.recovery, test.structural)
		if phase != test.wantPhase || recovery != test.wantRecovery {
			t.Fatalf("normalized product = %s/%s, want %s/%s", phase, recovery, test.wantPhase, test.wantRecovery)
		}
	}
}

func TestMSP04CRecoveryTransitionGraphIsClosed(t *testing.T) {
	allowed := map[string][]string{
		"NO_LOCAL_IDENTITY": {"UNPAIRED_LOCKED", "QUARANTINED"},
		"UNPAIRED_LOCKED":   {"PAIRED_TRUSTED", "QUARANTINED", "CORRUPT_STORE"},
		"PAIRED_TRUSTED":    {"REVOKED", "QUARANTINED", "CORRUPT_STORE"},
		"REVOKED":           {"PAIRED_TRUSTED", "QUARANTINED"},
		"QUARANTINED":       {"UNPAIRED_LOCKED", "REVOKED", "CORRUPT_STORE"},
		"CORRUPT_STORE":     {"UNPAIRED_LOCKED", "REVOKED", "QUARANTINED"},
	}

	for from, next := range allowed {
		for _, to := range msp04cCombinedStates {
			want := slices.Contains(next, to)
			if got := firstTrustRecoveryTransitionAllowed(from, to); got != want {
				t.Fatalf("transition decision for %s to %s = %t, want %t", from, to, got, want)
			}
		}
	}
}

func TestMSP04CG10StartupClassificationPrecedenceAndRestartDenial(t *testing.T) {
	tests := []struct {
		name       string
		configure  func(*msp04cFixture)
		wantState  string
		wantReason string
	}{
		{
			name: "structural failure",
			configure: func(fixture *msp04cFixture) {
				fixture.store.reloadOutcome = "no_valid_manifest"
			},
			wantState: "CORRUPT_STORE", wantReason: "CORRUPT_STORE",
		},
		{
			name: "durability unknown pending publication",
			configure: func(fixture *msp04cFixture) {
				pending := msp04cPending(fixture.store.view, fixture.store.nextView())
				fixture.anchor.record.pending = &pending
			},
			wantState: "QUARANTINED", wantReason: "DURABILITY_UNKNOWN",
		},
		{
			name: "restored state with anchor absent",
			configure: func(fixture *msp04cFixture) {
				fixture.anchor.openOutcome = "anchor_unavailable"
			},
			wantState: "NO_LOCAL_IDENTITY", wantReason: "HOST_KEY_UNAVAILABLE",
		},
		{
			name: "wrong host binding",
			configure: func(fixture *msp04cFixture) {
				fixture.anchor.openOutcome = "host_binding_mismatch"
			},
			wantState: "QUARANTINED", wantReason: "HOST_BINDING_MISMATCH",
		},
		{
			name: "copied instance conflict",
			configure: func(fixture *msp04cFixture) {
				fixture.anchor.record.storeInstance = msp04cOrdinal(91)
			},
			wantState: "QUARANTINED", wantReason: "CLONE_DETECTED",
		},
		{
			name: "manifest generation rollback",
			configure: func(fixture *msp04cFixture) {
				fixture.anchor.record.manifestGenerationHighWater = fixture.store.view.manifest.current.sequence + 1
			},
			wantState: "QUARANTINED", wantReason: "MANIFEST_GENERATION_ROLLBACK",
		},
		{
			name: "control epoch rollback",
			configure: func(fixture *msp04cFixture) {
				fixture.anchor.record.controlEpochHighWater = fixture.store.view.control.controlEpoch + 1
			},
			wantState: "QUARANTINED", wantReason: "CONTROL_EPOCH_ROLLBACK",
		},
		{
			name: "effective tombstone",
			configure: func(fixture *msp04cFixture) {
				fixture.store.view.control.tombstones = []firstTrustRevocationTombstone{msp04cTombstone(1, 8, fixture.store.view.manifest.current)}
			},
			wantState: "REVOKED", wantReason: "REVOKED_ASSOCIATION",
		},
		{
			name: "persisted hold",
			configure: func(fixture *msp04cFixture) {
				fixture.store.view.control.quarantines = []firstTrustQuarantineRecord{{
					scope: msp04cOrdinal(1), reason: "ADMIN_HOLD", state: "ADMIN_HOLD", lastControlEpoch: fixture.store.view.control.controlEpoch,
				}}
			},
			wantState: "QUARANTINED", wantReason: "ADMIN_HOLD",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMSP04CFixture(t)
			fixture.store.view.associations = []firstTrustAssociationRecord{msp04cAssociation(1, fixture.store.view.control.associationLineage, true, true, true, true)}
			test.configure(fixture)
			for restart := 0; restart < 2; restart++ {
				coordinator := fixture.newCoordinator()
				_ = coordinator.reopen(context.Background())
				assertMSP04CState(t, coordinator, test.wantState, test.wantReason)
				if got := fixture.effects.registerCount(); got != 0 {
					t.Fatalf("restart %d trust-registration count = %d, want 0", restart, got)
				}
				fixture.effects = newMSP04CEffectsSpy(fixture.events)
			}
		})
	}
}

func TestMSP04CStartupClassificationUsesFirstMatchingReason(t *testing.T) {
	fixture := newMSP04CFixture(t)
	fixture.store.view.associations = []firstTrustAssociationRecord{msp04cAssociation(1, fixture.store.view.control.associationLineage, true, true, true, true)}
	pending := msp04cPending(fixture.store.view, fixture.store.nextView())
	fixture.anchor.record.pending = &pending
	fixture.anchor.openOutcome = "host_binding_mismatch"
	fixture.anchor.record.storeInstance = msp04cOrdinal(91)
	fixture.anchor.record.manifestGenerationHighWater = fixture.store.view.manifest.current.sequence + 1
	fixture.anchor.record.controlEpochHighWater = fixture.store.view.control.controlEpoch + 1
	fixture.store.view.control.tombstones = []firstTrustRevocationTombstone{msp04cTombstone(1, 8, fixture.store.view.manifest.current)}
	fixture.store.view.control.quarantines = []firstTrustQuarantineRecord{{scope: msp04cOrdinal(1), reason: "ADMIN_HOLD", state: "ADMIN_HOLD"}}

	coordinator := fixture.newCoordinator()
	_ = coordinator.reopen(context.Background())
	assertMSP04CState(t, coordinator, "QUARANTINED", "DURABILITY_UNKNOWN")

	fixture.anchor.record.pending = nil
	coordinator = fixture.newCoordinator()
	_ = coordinator.reopen(context.Background())
	assertMSP04CState(t, coordinator, "QUARANTINED", "HOST_BINDING_MISMATCH")

	fixture.anchor.openOutcome = "opened_anchor"
	coordinator = fixture.newCoordinator()
	_ = coordinator.reopen(context.Background())
	assertMSP04CState(t, coordinator, "QUARANTINED", "CLONE_DETECTED")

	fixture.anchor.record.storeInstance = fixture.store.view.control.storeInstance
	coordinator = fixture.newCoordinator()
	_ = coordinator.reopen(context.Background())
	assertMSP04CState(t, coordinator, "QUARANTINED", "MANIFEST_GENERATION_ROLLBACK")

	fixture.anchor.record.manifestGenerationHighWater = fixture.store.view.manifest.current.sequence
	coordinator = fixture.newCoordinator()
	_ = coordinator.reopen(context.Background())
	assertMSP04CState(t, coordinator, "QUARANTINED", "CONTROL_EPOCH_ROLLBACK")
}

func TestMSP04CActiveReopenRegistrationResetFailureStaysQuarantined(t *testing.T) {
	fixture := newMSP04CFixture(t)
	coordinator := fixture.newCoordinator()
	if got := coordinator.reopen(context.Background()); got != "pairing_closed" {
		t.Fatalf("initial reopen = %q", got)
	}
	if got := coordinator.openPairingWindow(context.Background(), msp04cText(210), time.Minute); got != "open_empty" {
		t.Fatalf("open pairing window = %q", got)
	}

	coordinator.mu.Lock()
	coordinator.phase = firstTrustDisabled
	coordinator.mu.Unlock()
	fixture.effects.setPairingRegistrationError(errors.New("registration reset failed"))

	if got := coordinator.reopen(context.Background()); got != "pairing_registration_failed" {
		t.Fatalf("active reopen = %q, want pairing_registration_failed", got)
	}
	if got := coordinator.state(); got != "DISABLED" {
		t.Fatalf("active reopen phase = %q, want DISABLED", got)
	}
	assertMSP04CState(t, coordinator, "QUARANTINED", "PAIRING_REGISTRATION_FAILED")

	fixture.effects.setPairingRegistrationError(nil)
	if got := coordinator.reopen(context.Background()); got != "pairing_registration_failed" {
		t.Fatalf("later reopen replaced registration fault: %q", got)
	}
	assertMSP04CState(t, coordinator, "QUARANTINED", "PAIRING_REGISTRATION_FAILED")
}

func TestMSP04CRecoveryConfirmationPropagatesPairingRegistrationFailure(t *testing.T) {
	fixture := newMSP04CFixture(t)
	coordinator := fixture.newCoordinator()
	if got := coordinator.reopen(context.Background()); got != "pairing_closed" {
		t.Fatalf("reopen = %q", got)
	}
	binding := openMSP04CCandidate(t, coordinator, 220)
	fixture.effects.setPairingRegistrationError(errors.New("registration close failed"))

	if got := confirmMSP04C(coordinator, binding); got != "pairing_registration_failed" {
		t.Fatalf("recovery confirmation = %q", got)
	}
	if got := coordinator.state(); got != "DISABLED" {
		t.Fatalf("recovery confirmation phase = %q, want DISABLED", got)
	}
	assertMSP04CState(t, coordinator, "QUARANTINED", "PAIRING_REGISTRATION_FAILED")
	if got := fixture.effects.registerCount(); got != 0 {
		t.Fatalf("registration count = %d after recovery withdrawal failure", got)
	}
}

func TestMSP04CConfirmationPublishesOnlyAfterBothDurabilityDomains(t *testing.T) {
	tests := []struct {
		name            string
		commitOutcome   string
		finalizeOutcome string
		clearOutcome    string
		wantOutcome     string
		wantPhase       string
		wantRecovery    string
		wantReason      string
		wantRegister    int
		wantActions     []string
	}{
		{
			name: "durable", commitOutcome: "commit_durable", finalizeOutcome: "anchor_durable",
			wantOutcome: "trusted", wantPhase: "PAIRING_CLOSED", wantRecovery: "PAIRED_TRUSTED", wantRegister: 1,
			wantActions: []string{"anchor_stage", "store_commit", "anchor_finalize", "waiting:false", "register"},
		},
		{
			name: "not published and cleared", commitOutcome: "commit_not_published", clearOutcome: "anchor_durable",
			wantOutcome: "failed_closed_unchanged", wantPhase: "PAIRING_CLOSED", wantRecovery: "UNPAIRED_LOCKED", wantRegister: 0,
			wantActions: []string{"anchor_stage", "store_commit", "anchor_clear", "waiting:false"},
		},
		{
			name: "not published clear ambiguous", commitOutcome: "commit_not_published", clearOutcome: "anchor_durability_unknown",
			wantOutcome: "trust_outcome_unknown", wantPhase: "DISABLED", wantRecovery: "QUARANTINED", wantReason: "DURABILITY_UNKNOWN", wantRegister: 0,
			wantActions: []string{"anchor_stage", "store_commit", "anchor_clear", "waiting:false"},
		},
		{
			name: "not published clear mismatch", commitOutcome: "commit_not_published", clearOutcome: "anchor_compare_mismatch",
			wantOutcome: "trust_outcome_unknown", wantPhase: "DISABLED", wantRecovery: "QUARANTINED", wantReason: "DURABILITY_UNKNOWN", wantRegister: 0,
			wantActions: []string{"anchor_stage", "store_commit", "anchor_clear", "waiting:false"},
		},
		{
			name: "not published interrupted before clear", commitOutcome: "commit_not_published", clearOutcome: "anchor_interrupted",
			wantOutcome: "trust_outcome_unknown", wantPhase: "DISABLED", wantRecovery: "QUARANTINED", wantReason: "DURABILITY_UNKNOWN", wantRegister: 0,
			wantActions: []string{"anchor_stage", "store_commit", "anchor_clear", "waiting:false"},
		},
		{
			name: "applied maintenance failed", commitOutcome: "commit_applied_maintenance_failed",
			wantOutcome: "trust_outcome_unknown", wantPhase: "DISABLED", wantRecovery: "QUARANTINED", wantReason: "DURABILITY_UNKNOWN", wantRegister: 0,
			wantActions: []string{"anchor_stage", "store_commit", "waiting:false"},
		},
		{
			name: "store durability unknown", commitOutcome: "commit_durability_unknown",
			wantOutcome: "trust_outcome_unknown", wantPhase: "DISABLED", wantRecovery: "QUARANTINED", wantReason: "DURABILITY_UNKNOWN", wantRegister: 0,
			wantActions: []string{"anchor_stage", "store_commit", "waiting:false"},
		},
		{
			name: "finalize ambiguous", commitOutcome: "commit_durable", finalizeOutcome: "anchor_durability_unknown",
			wantOutcome: "trust_outcome_unknown", wantPhase: "DISABLED", wantRecovery: "QUARANTINED", wantReason: "DURABILITY_UNKNOWN", wantRegister: 0,
			wantActions: []string{"anchor_stage", "store_commit", "anchor_finalize", "waiting:false"},
		},
		{
			name: "finalize mismatch", commitOutcome: "commit_durable", finalizeOutcome: "anchor_compare_mismatch",
			wantOutcome: "trust_outcome_unknown", wantPhase: "DISABLED", wantRecovery: "QUARANTINED", wantReason: "DURABILITY_UNKNOWN", wantRegister: 0,
			wantActions: []string{"anchor_stage", "store_commit", "anchor_finalize", "waiting:false"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMSP04CFixture(t)
			fixture.store.commitOutcome = test.commitOutcome
			fixture.anchor.finalizeOutcome = test.finalizeOutcome
			fixture.anchor.clearOutcome = test.clearOutcome
			coordinator := fixture.newCoordinator()
			if got := coordinator.reopen(context.Background()); got != "pairing_closed" {
				t.Fatalf("startup outcome = %q", got)
			}
			assertMSP04CState(t, coordinator, "UNPAIRED_LOCKED", "")
			binding := openMSP04CCandidate(t, coordinator, 11)
			if got := confirmMSP04C(coordinator, binding); got != test.wantOutcome {
				t.Fatalf("confirmation outcome = %q, want %q", got, test.wantOutcome)
			}
			if got := coordinator.state(); got != test.wantPhase {
				t.Fatalf("coordinator phase = %q, want %q", got, test.wantPhase)
			}
			assertMSP04CState(t, coordinator, test.wantRecovery, test.wantReason)
			if got := fixture.effects.registerCount(); got != test.wantRegister {
				t.Fatalf("trust-registration count = %d, want %d", got, test.wantRegister)
			}
			if got := coordinator.trusted(binding.subject); got != (test.wantRecovery == "PAIRED_TRUSTED") {
				t.Fatalf("terminal durable trust = %t, want %t", got, test.wantRecovery == "PAIRED_TRUSTED")
			}
			fixture.events.assertOrdered(t, test.wantActions...)
			assertMSP04CPendingDescriptor(t, fixture.anchor.staged, fixture.store.prepared)
			if test.wantRecovery == "QUARANTINED" && fixture.anchor.record.pending == nil {
				t.Fatal("ambiguous publication discarded its pending descriptor")
			}
		})
	}
}

func TestMSP04CConfirmationKeepsTheUntrustedAxisUntilDurableFinalize(t *testing.T) {
	tests := []struct {
		name          string
		configure     func(*msp04cFixture)
		wantUntrusted string
	}{
		{name: "unpaired", wantUntrusted: "UNPAIRED_LOCKED"},
		{
			name: "revoked", wantUntrusted: "REVOKED",
			configure: func(fixture *msp04cFixture) {
				fixture.store.view.control.tombstones = []firstTrustRevocationTombstone{msp04cTombstone(1, 7, fixture.store.view.manifest.current)}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMSP04CFixture(t)
			if test.configure != nil {
				test.configure(fixture)
			}
			oldLineage := fixture.store.view.control.associationLineage
			fixture.store.block()
			coordinator := fixture.newCoordinator()
			_ = coordinator.reopen(context.Background())
			assertMSP04CState(t, coordinator, test.wantUntrusted, map[bool]string{true: "REVOKED_ASSOCIATION"}[test.wantUntrusted == "REVOKED"])
			binding := openMSP04CCandidate(t, coordinator, 401)
			result := make(chan string, 1)
			go func() { result <- confirmMSP04C(coordinator, binding) }()
			waitMSP04CSignal(t, fixture.store.commitEntered)
			if coordinator.state() != "COMMITTING" {
				t.Fatalf("phase before durable finalize = %q", coordinator.state())
			}
			assertMSP04CState(t, coordinator, test.wantUntrusted, map[bool]string{true: "REVOKED_ASSOCIATION"}[test.wantUntrusted == "REVOKED"])
			if fixture.effects.registerCount() != 0 {
				t.Fatal("trust registered before durable finalize")
			}
			fixture.store.release()
			if got := waitMSP04CResult(t, result); got != "trusted" {
				t.Fatalf("terminal outcome = %q", got)
			}
			assertMSP04CState(t, coordinator, "PAIRED_TRUSTED", "")
			if fixture.effects.registerCount() != 1 {
				t.Fatal("durably finalized confirmation did not register exactly once")
			}
			if test.wantUntrusted == "REVOKED" {
				if fixture.store.view.control.associationLineage == oldLineage {
					t.Fatal("post-revocation confirmation reused a tombstoned lineage")
				}
				if !msp04cContainsTombstone(fixture.store.view.control.tombstones, msp04cOrdinal(1)) {
					t.Fatal("post-revocation confirmation removed the old tombstone")
				}
			}
		})
	}
}

func TestMSP04CCoordinatedPublicationRequiresDurableStageBeforeStoreCommit(t *testing.T) {
	tests := []struct {
		name        string
		stage       string
		wantOutcome string
		wantPhase   string
		wantState   string
		wantReason  string
	}{
		{name: "known not staged", stage: "anchor_not_published", wantOutcome: "failed_closed_unchanged", wantPhase: "PAIRING_CLOSED", wantState: "UNPAIRED_LOCKED"},
		{name: "stage mismatch", stage: "anchor_compare_mismatch", wantOutcome: "trust_outcome_unknown", wantPhase: "DISABLED", wantState: "QUARANTINED", wantReason: "DURABILITY_UNKNOWN"},
		{name: "stage ambiguous", stage: "anchor_durability_unknown", wantOutcome: "trust_outcome_unknown", wantPhase: "DISABLED", wantState: "QUARANTINED", wantReason: "DURABILITY_UNKNOWN"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMSP04CFixture(t)
			fixture.anchor.stageOutcome = test.stage
			coordinator := fixture.newCoordinator()
			_ = coordinator.reopen(context.Background())
			binding := openMSP04CCandidate(t, coordinator, 421)
			if got := confirmMSP04C(coordinator, binding); got != test.wantOutcome {
				t.Fatalf("stage outcome = %q, want %q", got, test.wantOutcome)
			}
			if coordinator.state() != test.wantPhase {
				t.Fatalf("phase = %q, want %q", coordinator.state(), test.wantPhase)
			}
			assertMSP04CState(t, coordinator, test.wantState, test.wantReason)
			if fixture.store.calls() != 0 {
				t.Fatal("store commit ran before durable anchor staging")
			}
			if fixture.effects.registerCount() != 0 {
				t.Fatal("non-durable staging registered trust")
			}
		})
	}
}

func TestMSP04CMechanicalPreparationFailureLeavesBothDomainsUnchanged(t *testing.T) {
	fixture := newMSP04CFixture(t)
	fixture.store.prepareOutcome = "validation_failed"
	coordinator := fixture.newCoordinator()
	_ = coordinator.reopen(context.Background())
	binding := openMSP04CCandidate(t, coordinator, 431)
	if got := confirmMSP04C(coordinator, binding); got != "failed_closed_unchanged" {
		t.Fatalf("preparation outcome = %q", got)
	}
	assertMSP04CState(t, coordinator, "UNPAIRED_LOCKED", "")
	if fixture.anchor.stageCalls != 0 || fixture.store.calls() != 0 {
		t.Fatal("mechanical preparation failure crossed a durability boundary")
	}
	if fixture.effects.registerCount() != 0 {
		t.Fatal("mechanical preparation failure registered trust")
	}
}

func TestMSP04CUnresolvedPublicationPrecedesEveryNewMutation(t *testing.T) {
	fixture := newMSP04CFixture(t)
	target := fixture.store.nextView()
	pending := msp04cPending(fixture.store.view, target)
	fixture.anchor.record.pending = &pending
	coordinator := fixture.newCoordinator()
	_ = coordinator.reopen(context.Background())
	assertMSP04CState(t, coordinator, "QUARANTINED", "DURABILITY_UNKNOWN")
	if fixture.anchor.finalizeCalls != 0 || fixture.anchor.clearCalls != 0 {
		t.Fatal("startup reconciled a pending publication automatically")
	}
	if got := coordinator.openPairingWindow(context.Background(), msp04cText(411), time.Minute); got != "reconciliation_required" {
		t.Fatalf("pairing precedence outcome = %q", got)
	}
	if got := coordinator.admitRetry(context.Background(), msp04cOrdinal(412)); got != "reconciliation_required" {
		t.Fatalf("retry precedence outcome = %q", got)
	}
	if got := coordinator.revoke(context.Background(), msp04cRevocationRequest(fixture, 413, 1)); got != "reconciliation_required" {
		t.Fatalf("revocation precedence outcome = %q", got)
	}
	request := msp04cRepairRequest(fixture, coordinator, "release_retry_quarantine", 414)
	if got := coordinator.repair(context.Background(), request); got != "reconciliation_required" {
		t.Fatalf("repair precedence outcome = %q", got)
	}
	if fixture.store.calls() != 0 || fixture.anchor.stageCalls != 0 || fixture.anchor.finalizeCalls != 0 || fixture.anchor.clearCalls != 0 {
		t.Fatal("blocked mutation crossed a durability boundary")
	}
}

func TestMSP04CPendingDescriptorBindsEveryBranchField(t *testing.T) {
	fixture := newMSP04CFixture(t)
	fixture.store.commitOutcome = "commit_not_published"
	fixture.anchor.clearOutcome = "anchor_compare_mismatch"
	coordinator := fixture.newCoordinator()
	_ = coordinator.reopen(context.Background())
	binding := openMSP04CCandidate(t, coordinator, 21)
	if got := confirmMSP04C(coordinator, binding); got != "trust_outcome_unknown" {
		t.Fatalf("confirmation outcome = %q", got)
	}
	assertMSP04CPendingDescriptor(t, fixture.anchor.staged, fixture.store.prepared)

	mutations := []func(*firstTrustPendingPublication){
		func(value *firstTrustPendingPublication) { value.operationID = msp04cOrdinal(201) },
		func(value *firstTrustPendingPublication) { value.operationClass = "revocation" },
		func(value *firstTrustPendingPublication) { value.storeInstance = msp04cOrdinal(202) },
		func(value *firstTrustPendingPublication) { value.previousControlEpoch++ },
		func(value *firstTrustPendingPublication) { value.targetControlEpoch++ },
		func(value *firstTrustPendingPublication) { value.previousManifest.epoch++ },
		func(value *firstTrustPendingPublication) { value.previousManifest.sha256 = msp04cDigest(203) },
		func(value *firstTrustPendingPublication) { value.previousManifest.current.sha256 = msp04cDigest(204) },
		func(value *firstTrustPendingPublication) { value.targetManifest.epoch++ },
		func(value *firstTrustPendingPublication) { value.targetManifest.sha256 = msp04cDigest(205) },
		func(value *firstTrustPendingPublication) { value.targetManifest.current.sha256 = msp04cDigest(206) },
	}
	for index, mutate := range mutations {
		mismatch := fixture.anchor.staged
		mutate(&mismatch)
		if firstTrustPendingPublicationEqual(fixture.anchor.staged, mismatch) {
			t.Fatalf("descriptor mutation %d was not detected", index)
		}
	}
}

func TestMSP04CPendingAndAnchorRecordsHaveClosedNonIdentityShape(t *testing.T) {
	tests := []struct {
		value any
		want  []string
	}{
		{
			value: firstTrustPendingPublication{},
			want: []string{
				"operationID", "operationClass", "storeInstance", "previousControlEpoch", "targetControlEpoch", "previousManifest", "targetManifest",
			},
		},
		{
			value: firstTrustAnchorRecord{},
			want: []string{
				"version", "anchorIdentity", "storeInstance", "manifestGenerationHighWater", "controlEpochHighWater", "pending",
			},
		},
		{
			value: firstTrustManifestBinding{},
			want:  []string{"epoch", "sha256", "current", "parent"},
		},
		{
			value: firstTrustGenerationBinding{},
			want:  []string{"sequence", "filename", "sha256", "schemaVersion"},
		},
	}
	for _, test := range tests {
		typeOf := reflect.TypeOf(test.value)
		got := make([]string, 0, typeOf.NumField())
		for index := 0; index < typeOf.NumField(); index++ {
			got = append(got, typeOf.Field(index).Name)
		}
		if !slices.Equal(got, test.want) {
			t.Fatalf("%s fields = %v, want %v", typeOf.Name(), got, test.want)
		}
	}
}

func TestMSP04CReconciliationRequiresExactCompleteBranch(t *testing.T) {
	tests := []struct {
		name         string
		observation  string
		mutate       func(*msp04cFixture)
		wantOutcome  string
		wantAction   string
		wantRecovery string
		wantPending  bool
	}{
		{name: "exact target", observation: "exact_target_selected", wantOutcome: "operation_terminal", wantAction: "anchor_finalize", wantRecovery: "UNPAIRED_LOCKED"},
		{name: "exact previous target absent", observation: "exact_previous_selected_and_target_absent", wantOutcome: "failed_closed_unchanged", wantAction: "anchor_clear", wantRecovery: "UNPAIRED_LOCKED"},
		{
			name: "same sequence different digest", observation: "same_number_different_digest_or_reference",
			mutate:      func(fixture *msp04cFixture) { fixture.store.view.manifest.sha256 = msp04cDigest(301) },
			wantOutcome: "repair_outcome_unknown", wantRecovery: "QUARANTINED", wantPending: true,
		},
		{name: "ambiguous", observation: "other_or_ambiguous", wantOutcome: "repair_outcome_unknown", wantRecovery: "QUARANTINED", wantPending: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMSP04CFixture(t)
			target := fixture.store.nextView()
			pending := msp04cPending(fixture.store.view, target)
			fixture.anchor.record.pending = &pending
			fixture.store.reconcileObservation = test.observation
			if test.mutate != nil {
				test.mutate(fixture)
			}
			coordinator := fixture.newCoordinator()
			_ = coordinator.reopen(context.Background())
			request := msp04cRepairRequest(fixture, coordinator, "reconcile_pending_publication", 31)
			if got := coordinator.repair(context.Background(), request); got != test.wantOutcome {
				t.Fatalf("reconciliation outcome = %q, want %q", got, test.wantOutcome)
			}
			assertMSP04CState(t, coordinator, test.wantRecovery, map[bool]string{true: "DURABILITY_UNKNOWN"}[test.wantPending])
			if test.wantAction != "" {
				fixture.events.assertOrdered(t, test.wantAction)
			}
			if got := fixture.anchor.record.pending != nil; got != test.wantPending {
				t.Fatalf("pending retained = %t, want %t", got, test.wantPending)
			}
			if test.wantAction != "" {
				receipt, found := coordinator.durableReceiptLocked(request.operationID)
				if !found || !receipt.terminal || receipt.operationClass != request.kind || receipt.bindingSHA256 != firstTrustHashRepair(request) || receipt.result != test.wantOutcome {
					t.Fatal("reconciliation did not durably publish its exact terminal receipt")
				}
				if fixture.store.view.control.repairSequence != request.nextRepairSequence {
					t.Fatal("reconciliation did not durably advance the repair sequence")
				}
				restarted := fixture.newCoordinator()
				_ = restarted.reopen(context.Background())
				if got := restarted.repair(context.Background(), request); got != test.wantOutcome {
					t.Fatalf("reconciliation replay after restart = %q, want %q", got, test.wantOutcome)
				}
				if fixture.store.calls() != 1 {
					t.Fatal("reconciliation replay performed a second store publication")
				}
			}
		})
	}
}

type msp04cCandidateBinding struct {
	proof      string
	nonce      string
	expiresAt  time.Time
	connection uint64
	generation uint64
	subject    []byte
}

type msp04cFixture struct {
	clock   *msp04cClock
	store   *msp04cStoreSpy
	anchor  *msp04cAnchorSpy
	effects *msp04cEffectsSpy
	events  *msp04cEventLog
	policy  firstTrustBackoffPolicy
}

func newMSP04CFixture(t *testing.T) *msp04cFixture {
	t.Helper()
	events := &msp04cEventLog{}
	lineage := msp04cOrdinal(3)
	view := firstTrustControlView{
		manifest: msp04cManifest(11, 17),
		control: firstTrustControlRecord{
			storeInstance:      msp04cOrdinal(1),
			controlEpoch:       7,
			associationLineage: lineage,
		},
	}
	store := &msp04cStoreSpy{
		view:                 view,
		reloadOutcome:        "opened_current",
		prepareOutcome:       "prepared",
		commitOutcome:        "commit_durable",
		reconcileObservation: "other_or_ambiguous",
		events:               events,
	}
	anchor := &msp04cAnchorSpy{
		record: firstTrustAnchorRecord{
			version:                     1,
			anchorIdentity:              msp04cOrdinal(2),
			storeInstance:               view.control.storeInstance,
			manifestGenerationHighWater: view.manifest.current.sequence,
			controlEpochHighWater:       view.control.controlEpoch,
		},
		openOutcome:     "opened_anchor",
		stageOutcome:    "anchor_durable",
		finalizeOutcome: "anchor_durable",
		clearOutcome:    "anchor_durable",
		createOutcome:   "anchor_durable",
		events:          events,
	}
	return &msp04cFixture{
		clock:   &msp04cClock{wall: time.Unix(1_900_000_000, 0), monotonic: 20 * time.Second},
		store:   store,
		anchor:  anchor,
		effects: newMSP04CEffectsSpy(events),
		events:  events,
		policy: firstTrustBackoffPolicy{
			base: 3 * time.Second, exponentCap: 2, maximum: 10 * time.Second, attemptMaximum: 4,
		},
	}
}

func (fixture *msp04cFixture) newCoordinator() *firstTrustCoordinator {
	return newFirstTrustCoordinatorWithRecovery(
		fixture.clock.WallNow,
		fixture.clock.MonotonicNow,
		&msp04cOrdinalReader{next: 500},
		fixture.store,
		fixture.anchor,
		fixture.effects,
		fixture.policy,
	)
}

func openMSP04CCandidate(t *testing.T, coordinator *firstTrustCoordinator, ordinal uint64) msp04cCandidateBinding {
	t.Helper()
	if got := coordinator.openPairingWindow(context.Background(), msp04cText(ordinal), time.Minute); got != "open_empty" {
		t.Fatalf("open outcome = %q", got)
	}
	subject := msp04cSubject(ordinal)
	if got := coordinator.admit(subject, ordinal); got != "candidate_pending" {
		t.Fatalf("admission outcome = %q", got)
	}
	if got := coordinator.serviceShipIDUpdate(subject, ordinal, msp04cText(ordinal+1)); got != "association_complete" {
		t.Fatalf("association completion outcome = %q", got)
	}
	proof, nonce, expiresAt, connection, generation, complete, ok := coordinator.candidate()
	if !ok || !complete {
		t.Fatal("complete candidate binding unavailable")
	}
	return msp04cCandidateBinding{proof: proof, nonce: nonce, expiresAt: expiresAt, connection: connection, generation: generation, subject: bytes.Clone(subject)}
}

func confirmMSP04C(coordinator *firstTrustCoordinator, binding msp04cCandidateBinding) string {
	return coordinator.confirm(
		context.Background(),
		msp04cText(binding.connection+100),
		binding.proof,
		binding.nonce,
		binding.expiresAt,
		binding.connection,
		binding.generation,
	)
}

func assertMSP04CState(t *testing.T, coordinator *firstTrustCoordinator, wantState, wantReason string) {
	t.Helper()
	if got := coordinator.recoveryState(); got != wantState {
		t.Fatalf("recovery state = %q, want %q", got, wantState)
	}
	if got := coordinator.recoveryReason(); got != wantReason {
		t.Fatalf("recovery reason = %q, want %q", got, wantReason)
	}
	if !firstTrustProductAllowed(coordinator.state(), coordinator.recoveryState()) {
		t.Fatalf("published product %s/%s is forbidden", coordinator.state(), coordinator.recoveryState())
	}
}

func assertMSP04CPendingDescriptor(t *testing.T, got firstTrustPendingPublication, publication firstTrustPreparedPublication) {
	t.Helper()
	want := msp04cPending(publication.previous, publication.target)
	want.operationID = publication.operationID
	want.operationClass = publication.operationClass
	if !firstTrustPendingPublicationEqual(got, want) {
		t.Fatal("pending publication does not bind the complete prepared branch")
	}
	if got.targetControlEpoch != got.previousControlEpoch+1 {
		t.Fatal("pending publication does not advance the control epoch exactly once")
	}
	if got.targetManifest.epoch != got.previousManifest.epoch+1 {
		t.Fatal("pending publication does not advance the manifest epoch exactly once")
	}
	if got.targetManifest.current.sequence == got.previousManifest.current.sequence {
		t.Fatal("pending publication reused the selected generation sequence")
	}
}

func msp04cPending(previous, target firstTrustControlView) firstTrustPendingPublication {
	return firstTrustPendingPublication{
		operationID:          msp04cOrdinal(70),
		operationClass:       "first_trust",
		storeInstance:        previous.control.storeInstance,
		previousControlEpoch: previous.control.controlEpoch,
		targetControlEpoch:   target.control.controlEpoch,
		previousManifest:     previous.manifest,
		targetManifest:       target.manifest,
	}
}

func msp04cRepairRequest(fixture *msp04cFixture, coordinator *firstTrustCoordinator, kind string, ordinal uint64) firstTrustRepairRequest {
	return firstTrustRepairRequest{
		operationID:               msp04cOrdinal(ordinal),
		kind:                      kind,
		scope:                     msp04cOrdinal(ordinal + 1),
		expectedState:             coordinator.recoveryState(),
		expectedReason:            coordinator.recoveryReason(),
		expectedManifest:          fixture.store.view.manifest,
		expectedControlEpoch:      fixture.store.view.control.controlEpoch,
		expectedAnchorVersion:     fixture.anchor.record.version,
		expectedManifestHighWater: fixture.anchor.record.manifestGenerationHighWater,
		expectedControlHighWater:  fixture.anchor.record.controlEpochHighWater,
		nextRepairSequence:        fixture.store.view.control.repairSequence + 1,
	}
}

func msp04cManifest(sequence, epoch uint64) firstTrustManifestBinding {
	current := firstTrustGenerationBinding{
		sequence: sequence, filename: msp04cText(sequence), sha256: msp04cDigest(sequence), schemaVersion: 2,
	}
	parent := firstTrustGenerationBinding{
		sequence: sequence - 1, filename: msp04cText(sequence - 1), sha256: msp04cDigest(sequence - 1), schemaVersion: 2,
	}
	return firstTrustManifestBinding{epoch: epoch, sha256: msp04cDigest(epoch + 100), current: current, parent: &parent}
}

func msp04cAssociation(ordinal uint64, lineage [32]byte, active, trusted, allowlisted, reconnectable bool) firstTrustAssociationRecord {
	return firstTrustAssociationRecord{
		reference: msp04cOrdinal(ordinal), lineage: lineage, subject: msp04cSubject(ordinal), service: msp04cText(ordinal),
		active: active, trusted: trusted, allowlisted: allowlisted, reconnectable: reconnectable,
	}
}

func msp04cTombstone(ordinal, epoch uint64, effective firstTrustGenerationBinding) firstTrustRevocationTombstone {
	return firstTrustRevocationTombstone{
		associationRef: msp04cOrdinal(ordinal), revocationEpoch: epoch, operationID: msp04cOrdinal(ordinal + 100), effectiveGeneration: effective,
	}
}

func msp04cOrdinal(value uint64) [32]byte {
	var result [32]byte
	for index := 0; index < 8; index++ {
		result[len(result)-1-index] = byte(value >> (index * 8))
	}
	return result
}

func msp04cDigest(value uint64) [32]byte {
	result := msp04cOrdinal(value)
	result[0] = 1
	return result
}

func msp04cSubject(value uint64) []byte {
	ordinal := msp04cOrdinal(value)
	return bytes.Clone(ordinal[len(ordinal)-20:])
}

func msp04cText(value uint64) string {
	ordinal := msp04cOrdinal(value)
	return hex.EncodeToString(ordinal[24:])
}

type msp04cOrdinalReader struct {
	mu   sync.Mutex
	next uint64
}

func (reader *msp04cOrdinalReader) Read(payload []byte) (int, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	for offset := 0; offset < len(payload); {
		value := msp04cOrdinal(reader.next)
		reader.next++
		offset += copy(payload[offset:], value[:])
	}
	return len(payload), nil
}

var _ io.Reader = (*msp04cOrdinalReader)(nil)

type msp04cClock struct {
	mu        sync.Mutex
	wall      time.Time
	monotonic time.Duration
}

func (clock *msp04cClock) WallNow() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.wall
}

func (clock *msp04cClock) MonotonicNow() time.Duration {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.monotonic
}

func (clock *msp04cClock) advanceMonotonic(delta time.Duration) {
	clock.mu.Lock()
	clock.monotonic += delta
	clock.mu.Unlock()
}

func (clock *msp04cClock) changeWall(delta time.Duration) {
	clock.mu.Lock()
	clock.wall = clock.wall.Add(delta)
	clock.mu.Unlock()
}

type msp04cEventLog struct {
	mu     sync.Mutex
	events []string
}

func (log *msp04cEventLog) add(event string) {
	log.mu.Lock()
	log.events = append(log.events, event)
	log.mu.Unlock()
}

func (log *msp04cEventLog) assertOrdered(t *testing.T, wanted ...string) {
	t.Helper()
	log.mu.Lock()
	events := append([]string(nil), log.events...)
	log.mu.Unlock()
	position := -1
	for _, want := range wanted {
		found := -1
		for index := position + 1; index < len(events); index++ {
			if events[index] == want {
				found = index
				break
			}
		}
		if found < 0 {
			t.Fatalf("ordered event %q missing from %v", want, events)
		}
		position = found
	}
}

type msp04cStoreSpy struct {
	mu                   sync.Mutex
	view                 firstTrustControlView
	reloadOutcome        string
	prepareOutcome       string
	commitOutcome        string
	reconcileObservation string
	prepared             firstTrustPreparedPublication
	commitCalls          int
	commitEntered        chan struct{}
	enteredOnce          sync.Once
	releaseCommit        chan struct{}
	events               *msp04cEventLog
}

func (store *msp04cStoreSpy) ReloadControl(context.Context) (firstTrustControlView, string) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return cloneFirstTrustControlView(store.view), store.reloadOutcome
}

func (store *msp04cStoreSpy) SelectedGeneration() uint64 {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.view.manifest.current.sequence
}

func (store *msp04cStoreSpy) PrepareControl(_ context.Context, previous firstTrustControlView, target firstTrustControlRecord, operationID [32]byte, operationClass string) (firstTrustPreparedPublication, string) {
	store.mu.Lock()
	defer store.mu.Unlock()
	publication := firstTrustPreparedPublication{
		previous: cloneFirstTrustControlView(previous), target: cloneFirstTrustControlView(previous), operationID: operationID, operationClass: operationClass,
	}
	publication.target.control = cloneFirstTrustControlRecord(target)
	publication.target.manifest = msp04cManifest(previous.manifest.current.sequence+1, previous.manifest.epoch+1)
	for index := range publication.target.control.tombstones {
		if publication.target.control.tombstones[index].operationID == operationID && publication.target.control.tombstones[index].effectiveGeneration.sequence == 0 {
			publication.target.control.tombstones[index].effectiveGeneration = publication.target.manifest.current
		}
	}
	store.prepared = publication
	return publication, store.prepareOutcome
}

func (store *msp04cStoreSpy) CommitControl(_ context.Context, publication firstTrustPreparedPublication) string {
	store.mu.Lock()
	store.commitCalls++
	entered := store.commitEntered
	release := store.releaseCommit
	store.mu.Unlock()
	store.events.add("store_commit")
	if entered != nil {
		store.enteredOnce.Do(func() { close(entered) })
	}
	if release != nil {
		<-release
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.commitOutcome == "commit_durable" || store.commitOutcome == "commit_applied_maintenance_failed" {
		store.view = cloneFirstTrustControlView(publication.target)
	}
	return store.commitOutcome
}

func (store *msp04cStoreSpy) ObserveControlPublication(context.Context, firstTrustPendingPublication) string {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.reconcileObservation
}

func (store *msp04cStoreSpy) nextView() firstTrustControlView {
	store.mu.Lock()
	defer store.mu.Unlock()
	next := cloneFirstTrustControlView(store.view)
	next.manifest = msp04cManifest(store.view.manifest.current.sequence+1, store.view.manifest.epoch+1)
	next.control.controlEpoch++
	return next
}

func (store *msp04cStoreSpy) block() {
	store.mu.Lock()
	store.commitEntered = make(chan struct{})
	store.releaseCommit = make(chan struct{})
	store.mu.Unlock()
}

func (store *msp04cStoreSpy) release() {
	store.mu.Lock()
	release := store.releaseCommit
	store.mu.Unlock()
	close(release)
}

func (store *msp04cStoreSpy) calls() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.commitCalls
}

type msp04cAnchorSpy struct {
	mu              sync.Mutex
	record          firstTrustAnchorRecord
	openOutcome     string
	stageOutcome    string
	finalizeOutcome string
	clearOutcome    string
	createOutcome   string
	staged          firstTrustPendingPublication
	stageCalls      int
	finalizeCalls   int
	clearCalls      int
	createCalls     int
	events          *msp04cEventLog
}

func (anchor *msp04cAnchorSpy) Open(context.Context) (firstTrustAnchorRecord, string) {
	anchor.mu.Lock()
	defer anchor.mu.Unlock()
	return cloneFirstTrustAnchorRecord(anchor.record), anchor.openOutcome
}

func (anchor *msp04cAnchorSpy) CompareAndStage(_ context.Context, expected firstTrustAnchorRecord, pending firstTrustPendingPublication) string {
	anchor.mu.Lock()
	defer anchor.mu.Unlock()
	anchor.stageCalls++
	anchor.events.add("anchor_stage")
	anchor.staged = pending
	if anchor.stageOutcome == "anchor_durable" && firstTrustAnchorRecordEqual(anchor.record, expected) {
		copy := pending
		anchor.record.pending = &copy
	}
	return anchor.stageOutcome
}

func (anchor *msp04cAnchorSpy) CompareAndFinalize(_ context.Context, pending firstTrustPendingPublication) string {
	anchor.mu.Lock()
	defer anchor.mu.Unlock()
	anchor.finalizeCalls++
	anchor.events.add("anchor_finalize")
	if anchor.finalizeOutcome == "anchor_durable" && anchor.record.pending != nil && firstTrustPendingPublicationEqual(*anchor.record.pending, pending) {
		anchor.record.manifestGenerationHighWater = pending.targetManifest.current.sequence
		anchor.record.controlEpochHighWater = pending.targetControlEpoch
		anchor.record.pending = nil
	}
	return anchor.finalizeOutcome
}

func (anchor *msp04cAnchorSpy) CompareAndClear(_ context.Context, pending firstTrustPendingPublication) string {
	anchor.mu.Lock()
	defer anchor.mu.Unlock()
	anchor.clearCalls++
	anchor.events.add("anchor_clear")
	if anchor.clearOutcome == "anchor_durable" && anchor.record.pending != nil && firstTrustPendingPublicationEqual(*anchor.record.pending, pending) {
		anchor.record.pending = nil
	}
	return anchor.clearOutcome
}

func (anchor *msp04cAnchorSpy) Create(_ context.Context, version uint64, storeInstance [32]byte) (firstTrustAnchorRecord, string) {
	anchor.mu.Lock()
	defer anchor.mu.Unlock()
	anchor.createCalls++
	anchor.events.add("anchor_create")
	if anchor.createOutcome == "anchor_durable" {
		anchor.record = firstTrustAnchorRecord{version: version, anchorIdentity: msp04cOrdinal(700), storeInstance: storeInstance}
	}
	return cloneFirstTrustAnchorRecord(anchor.record), anchor.createOutcome
}

type msp04cEffectsSpy struct {
	mu                     sync.Mutex
	waiting                bool
	pairingRegistrationErr error
	pairingRegistrationBad bool
	cancels                int
	registers              int
	events                 *msp04cEventLog
}

func newMSP04CEffectsSpy(events *msp04cEventLog) *msp04cEffectsSpy {
	return &msp04cEffectsSpy{events: events}
}

func (effects *msp04cEffectsSpy) setWaiting(value bool) error {
	effects.mu.Lock()
	effects.waiting = value
	err := effects.pairingRegistrationErr
	if err != nil {
		effects.pairingRegistrationBad = true
	}
	effects.mu.Unlock()
	effects.events.add("waiting:" + map[bool]string{true: "true", false: "false"}[value])
	return err
}

func (effects *msp04cEffectsSpy) setPairingRegistrationError(err error) {
	effects.mu.Lock()
	effects.pairingRegistrationErr = err
	effects.mu.Unlock()
}

func (effects *msp04cEffectsSpy) cancelRemote([]byte, uint64) {
	effects.mu.Lock()
	effects.cancels++
	effects.mu.Unlock()
	effects.events.add("cancel")
}

func (effects *msp04cEffectsSpy) connectionAlive([]byte, uint64) bool {
	effects.mu.Lock()
	defer effects.mu.Unlock()
	return !effects.pairingRegistrationBad
}

func (effects *msp04cEffectsSpy) registerRemoteSKI([]byte, uint64) {
	effects.mu.Lock()
	effects.registers++
	effects.mu.Unlock()
	effects.events.add("register")
}

func (effects *msp04cEffectsSpy) disconnectRemote([]byte) (<-chan struct{}, bool) {
	effects.events.add("disconnect")
	acknowledged := make(chan struct{})
	close(acknowledged)
	return acknowledged, true
}

func (*msp04cEffectsSpy) cancelDisconnect([]byte, <-chan struct{}) {}

func (effects *msp04cEffectsSpy) unregisterRemote([]byte) bool {
	effects.events.add("unregister")
	return true
}

func (effects *msp04cEffectsSpy) registerCount() int {
	effects.mu.Lock()
	defer effects.mu.Unlock()
	return effects.registers
}
