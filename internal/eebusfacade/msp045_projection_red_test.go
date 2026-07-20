package eebusfacade

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"go/ast"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	eebusapi "github.com/Project-Helianthus/helianthus-eebus-go/api"
	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusstore"
	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
	shipcert "github.com/Project-Helianthus/helianthus-ship-go/cert"
	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
)

func TestMSP045AdmissionInputsNeverProveDurablePairing(t *testing.T) {
	tests := []struct {
		name        string
		pretrusted  bool
		allowlisted bool
	}{
		{name: "allowlisted", allowlisted: true},
		{name: "pretrusted", pretrusted: true},
		{name: "both", pretrusted: true, allowlisted: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, err := newRuntimeServiceHandler(RuntimeConfig{Remotes: []RuntimeRemote{{
				SKI:         msp045RandomSKI(t),
				Pretrusted:  test.pretrusted,
				Allowlisted: test.allowlisted,
			}}}, msp045RandomSKI(t), msp045Clock().Now)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, _ := msp045Capture(t, handler)
			msp045AssertTrust(t, snapshot, "unpaired", false, "no-visible-services")
		})
	}
}

func TestMSP045PairingRequiresEveryDurableEligibilityGate(t *testing.T) {
	tests := []struct {
		name         string
		mutate       func(*msp045ProductSetup)
		wantState    string
		wantPaired   bool
		wantReason   string
		wantRecovery string
	}{
		{name: "all gates", wantState: "paired", wantPaired: true, wantReason: "no-visible-services", wantRecovery: "PAIRED_TRUSTED"},
		{name: "inactive", mutate: func(setup *msp045ProductSetup) { setup.view.associations[0].active = false }, wantState: "unpaired", wantReason: "no-visible-services", wantRecovery: "UNPAIRED_LOCKED"},
		{name: "untrusted", mutate: func(setup *msp045ProductSetup) { setup.view.associations[0].trusted = false }, wantState: "unpaired", wantReason: "no-visible-services", wantRecovery: "UNPAIRED_LOCKED"},
		{name: "not allowlisted", mutate: func(setup *msp045ProductSetup) { setup.view.associations[0].allowlisted = false }, wantState: "unpaired", wantReason: "no-visible-services", wantRecovery: "UNPAIRED_LOCKED"},
		{name: "not reconnectable", mutate: func(setup *msp045ProductSetup) { setup.view.associations[0].reconnectable = false }, wantState: "unpaired", wantReason: "no-visible-services", wantRecovery: "UNPAIRED_LOCKED"},
		{name: "stale lineage", mutate: func(setup *msp045ProductSetup) { setup.view.associations[0].lineage = msp045Opaque(t) }, wantState: "unpaired", wantReason: "no-visible-services", wantRecovery: "UNPAIRED_LOCKED"},
		{name: "tombstoned", mutate: func(setup *msp045ProductSetup) { msp045AddTombstone(t, setup) }, wantState: "denied", wantReason: "denied-trust", wantRecovery: "REVOKED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newMSP045ProductHarness(t, test.mutate)
			if got := harness.resources.coordinator.recoveryState(); got != test.wantRecovery {
				t.Fatalf("coordinator recovery = %q, want %q", got, test.wantRecovery)
			}
			snapshot, _ := msp045Capture(t, harness.handler)
			msp045AssertTrust(t, snapshot, test.wantState, test.wantPaired, test.wantReason)
		})
	}
}

func TestMSP045ClosedDegradationPrecedence(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*msp045ProductSetup)
		disconnect bool
		wantState  string
		wantPaired bool
		wantReason string
	}{
		{
			name: "unknown store product outranks every lower class",
			mutate: func(setup *msp045ProductSetup) {
				setup.storeOutcome = "future_store_product"
				setup.anchorOutcome = "anchor_unavailable"
				msp045AddTombstone(t, setup)
			},
			wantState: "unknown", wantReason: "denied-trust",
		},
		{name: "corrupt store is structural indeterminate", mutate: func(setup *msp045ProductSetup) {
			setup.storeOutcome = "no_valid_manifest"
		}, wantState: "unknown", wantReason: "denied-trust"},
		{name: "maintenance failure", mutate: func(setup *msp045ProductSetup) { setup.storeOutcome = "commit_applied_maintenance_failed" }, wantState: "unknown", wantReason: "denied-trust"},
		{name: "durability unknown", mutate: func(setup *msp045ProductSetup) { setup.storeOutcome = "commit_durability_unknown" }, wantState: "unknown", wantReason: "denied-trust"},
		{name: "host binding mismatch", mutate: func(setup *msp045ProductSetup) { setup.anchorOutcome = "host_binding_mismatch" }, wantState: "unknown", wantReason: "denied-trust"},
		{name: "clone detected", mutate: func(setup *msp045ProductSetup) { setup.anchorRecord.storeInstance = msp045Opaque(t) }, wantState: "unknown", wantReason: "denied-trust"},
		{name: "manifest rollback", mutate: func(setup *msp045ProductSetup) {
			setup.anchorRecord.manifestGenerationHighWater = setup.view.manifest.current.sequence + 1
		}, wantState: "unknown", wantReason: "denied-trust"},
		{name: "control rollback", mutate: func(setup *msp045ProductSetup) {
			setup.anchorRecord.controlEpochHighWater = setup.view.control.controlEpoch + 1
		}, wantState: "unknown", wantReason: "denied-trust"},
		{name: "anchor pending reconciliation", mutate: func(setup *msp045ProductSetup) {
			setup.anchorRecord.pending = &firstTrustPendingPublication{operationID: msp045Opaque(t)}
		}, wantState: "unknown", wantReason: "denied-trust"},
		{
			name: "terminal denial outranks missing identity",
			mutate: func(setup *msp045ProductSetup) {
				setup.storeOutcome = "key_material_unavailable"
				msp045AddTombstone(t, setup)
			},
			wantState: "denied", wantReason: "denied-trust",
		},
		{
			name: "admin hold outranks missing identity",
			mutate: func(setup *msp045ProductSetup) {
				setup.anchorOutcome = "anchor_unavailable"
				setup.view.control.quarantines = []firstTrustQuarantineRecord{{
					scope: msp045Opaque(t), reason: "ADMIN_HOLD", state: "ADMIN_HOLD",
					retentionBudget: time.Minute, lastControlEpoch: setup.view.control.controlEpoch,
				}}
			},
			wantState: "denied", wantReason: "denied-trust",
		},
		{
			name: "backoff hold",
			mutate: func(setup *msp045ProductSetup) {
				setup.view.associations = nil
				setup.view.control.quarantines = []firstTrustQuarantineRecord{{
					scope: msp045Opaque(t), reason: "RETRYABLE_FAILURE", state: "BACKOFF_ACTIVE",
					attemptCount: 1, remainingDelay: time.Second, retentionBudget: time.Minute,
					lastControlEpoch: setup.view.control.controlEpoch,
				}}
			},
			wantState: "denied", wantReason: "denied-trust",
		},
		{
			name: "invalid quarantine record closes as admin hold",
			mutate: func(setup *msp045ProductSetup) {
				setup.view.control.quarantines = []firstTrustQuarantineRecord{{
					reason: "synthetic_invalid_record", state: "ADMIN_HOLD",
				}}
			},
			wantState: "unknown", wantReason: "denied-trust",
		},
		{name: "missing protected identity", mutate: func(setup *msp045ProductSetup) {
			setup.storeOutcome = "key_provider_unavailable"
			setup.view.associations = nil
		}, wantState: "unknown", wantReason: "certificate-unavailable"},
		{name: "paired disconnect", disconnect: true, wantState: "paired", wantPaired: true, wantReason: "remote-disconnect"},
		{name: "unpaired liveness", mutate: func(setup *msp045ProductSetup) { setup.view.associations = nil }, wantState: "unpaired", wantReason: "no-visible-services"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newMSP045ProductHarness(t, test.mutate)
			if test.disconnect {
				harness.reader.RemoteSKIDisconnected(nil, harness.remoteSKI)
			}
			snapshot, _ := msp045Capture(t, harness.handler)
			msp045AssertTrust(t, snapshot, test.wantState, test.wantPaired, test.wantReason)
		})
	}
}

func TestMSP045EphemeralCandidateNeverChangesPublicShapeOrTiming(t *testing.T) {
	harness := newMSP045ProductHarness(t, func(setup *msp045ProductSetup) {
		setup.view.associations = nil
	})
	baseline, baselinePayload := msp045Capture(t, harness.handler)
	if got := harness.resources.coordinator.openPairingWindow(context.Background(), msp045RunToken(t, "window"), time.Minute); got != "open_empty" {
		t.Fatalf("open pairing window = %q", got)
	}
	open, openPayload := msp045Capture(t, harness.handler)
	msp045AssertSamePublicSnapshot(t, baseline, baselinePayload, open, openPayload)

	candidateSKI := msp045RandomSKI(t)
	harness.reader.ServicePairingDetailUpdate(candidateSKI, shipapi.NewConnectionStateDetail(shipapi.ConnectionStateReceivedPairingRequest, nil))
	proof, nonce, _, _, _, complete, exists := harness.resources.coordinator.candidate()
	if !exists || complete {
		t.Fatalf("incomplete candidate = exists:%t complete:%t", exists, complete)
	}
	incomplete, incompletePayload := msp045Capture(t, harness.handler)
	msp045AssertSamePublicSnapshot(t, baseline, baselinePayload, incomplete, incompletePayload)

	harness.reader.ServiceShipIDUpdate(candidateSKI, msp045RunToken(t, "service"))
	proof, nonce, _, _, _, complete, exists = harness.resources.coordinator.candidate()
	if !exists || !complete {
		t.Fatalf("complete candidate = exists:%t complete:%t", exists, complete)
	}
	completeSnapshot, completePayload := msp045Capture(t, harness.handler)
	msp045AssertSamePublicSnapshot(t, baseline, baselinePayload, completeSnapshot, completePayload)

	requestMarker := msp045RunToken(t, "idem"+"potency")
	harness.resources.coordinator.mu.Lock()
	harness.resources.coordinator.phase = firstTrustCommitting
	harness.resources.coordinator.currentCandidate.requests[requestMarker] = firstTrustRequest{
		operation:   "confirm",
		fingerprint: msp045RunToken(t, "proof"),
		nonce:       msp045RunToken(t, "challenge"),
	}
	harness.resources.coordinator.mu.Unlock()
	committing, committingPayload := msp045Capture(t, harness.handler)
	msp045AssertSamePublicSnapshot(t, baseline, baselinePayload, committing, committingPayload)

	redacted, err := eebusraw.RedactID(eebusraw.IDKindRemoteSKI, candidateSKI)
	if err != nil {
		t.Fatal(err)
	}
	for label, value := range map[string]string{
		"raw candidate":       candidateSKI,
		"redacted candidate":  redacted.Digest,
		"candidate proof":     proof,
		"candidate challenge": nonce,
		"request marker":      requestMarker,
	} {
		if value != "" && bytes.Contains(committingPayload, []byte(value)) {
			t.Fatalf("runtime snapshot leaks %s", label)
		}
	}
}

func TestMSP045StaleTrustedAndCompletedCallbacksAreLivenessOnly(t *testing.T) {
	for _, test := range []struct {
		name  string
		state shipapi.ConnectionState
	}{
		{name: "trusted", state: shipapi.ConnectionStateTrusted},
		{name: "completed", state: shipapi.ConnectionStateCompleted},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newMSP045ProductHarness(t, func(setup *msp045ProductSetup) {
				msp045AddTombstone(t, setup)
			})
			harness.reader.RemoteSKIConnected(nil, harness.remoteSKI)
			harness.reader.ServicePairingDetailUpdate(harness.remoteSKI, shipapi.NewConnectionStateDetail(test.state, nil))
			snapshot, _ := msp045Capture(t, harness.handler)
			msp045AssertTrust(t, snapshot, "denied", false, "denied-trust")
			if len(snapshot.Sessions) != 1 || snapshot.Sessions[0].State != "connected" {
				t.Fatalf("callback liveness = %+v", snapshot.Sessions)
			}
		})
	}
}

func TestMSP045DurablePairingPublishesWithoutNetworkCallback(t *testing.T) {
	harness := newMSP045ProductHarness(t, func(setup *msp045ProductSetup) {
		setup.view.associations = nil
	})
	updates := msp045InstallPublisher(harness.handler)
	pairRuntimeRemote(t, harness.resources, harness.remoteSKI, 4_501)
	snapshot := msp045WaitForTrust(t, updates, "paired")
	msp045AssertTrust(t, snapshot, "paired", true, "no-visible-services")
}

func TestMSP045CommitFailureProductsFailClosed(t *testing.T) {
	tests := []struct {
		name       string
		configure  func(*msp045RuntimeHarness)
		wantState  string
		wantReason string
	}{
		{name: "store durable anchor finalization unknown", configure: func(harness *msp045RuntimeHarness) { msp045SetAnchorOutcomes(harness, "", "anchor_not_published", "") }, wantState: "unknown", wantReason: "denied-trust"},
		{name: "store maintenance failure", configure: func(harness *msp045RuntimeHarness) {
			msp045SetBridgeOutcome(harness, "commit_applied_maintenance_failed", false, false)
		}, wantState: "unknown", wantReason: "denied-trust"},
		{name: "store durability unknown", configure: func(harness *msp045RuntimeHarness) {
			msp045SetBridgeOutcome(harness, "commit_durability_unknown", false, false)
		}, wantState: "unknown", wantReason: "denied-trust"},
		{name: "anchor stage unknown", configure: func(harness *msp045RuntimeHarness) {
			msp045SetAnchorOutcomes(harness, "anchor_outcome_unknown", "", "")
		}, wantState: "unknown", wantReason: "denied-trust"},
		{name: "commit interruption", configure: func(harness *msp045RuntimeHarness) { msp045SetBridgeOutcome(harness, "commit_durable", false, true) }, wantState: "unknown", wantReason: "denied-trust"},
		{name: "prepared descriptor mismatch", configure: func(harness *msp045RuntimeHarness) { msp045SetBridgeOutcome(harness, "commit_durable", true, false) }, wantState: "unknown", wantReason: "denied-trust"},
		{name: "commit not published and anchor cleared", configure: func(harness *msp045RuntimeHarness) {
			msp045SetBridgeOutcome(harness, "commit_not_published", false, false)
		}, wantState: "unpaired", wantReason: "no-visible-services"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newMSP045ProductHarness(t, func(setup *msp045ProductSetup) {
				setup.view.associations = nil
			})
			candidate := msp045PrepareCandidate(t, harness, 4_600)
			test.configure(harness)
			harness.bridge.mu.Lock()
			blocked := harness.bridge.blockCommit
			harness.bridge.mu.Unlock()
			if blocked {
				harness.resources.coordinator.mu.Lock()
				harness.resources.coordinator.commitWait = 20 * time.Millisecond
				harness.resources.coordinator.mu.Unlock()
			}
			result := harness.resources.coordinator.confirm(
				context.Background(), msp045RunToken(t, "confirm"), candidate.proof, candidate.nonce,
				candidate.expiry, candidate.connection, candidate.generation,
			)
			if result == "trusted" {
				t.Fatal("incomplete durability product returned trusted")
			}
			snapshot, _ := msp045Capture(t, harness.handler)
			msp045AssertTrust(t, snapshot, test.wantState, false, test.wantReason)
		})
	}
}

func TestMSP045FuturePrepareOutcomeFailsClosedAcrossCallers(t *testing.T) {
	setFuture := func(harness *msp045RuntimeHarness) {
		harness.bridge.mu.Lock()
		harness.bridge.prepareOutcome = "future_prepare_product"
		harness.bridge.mu.Unlock()
	}

	t.Run("confirmation", func(t *testing.T) {
		harness := newMSP045ProductHarness(t, func(setup *msp045ProductSetup) { setup.view.associations = nil })
		candidate := msp045PrepareCandidate(t, harness, 4_650)
		setFuture(harness)
		if got := harness.resources.coordinator.confirm(context.Background(), msp045RunToken(t, "future-confirm"), candidate.proof, candidate.nonce, candidate.expiry, candidate.connection, candidate.generation); got != "trust_outcome_unknown" {
			t.Fatalf("confirmation = %q, want trust_outcome_unknown", got)
		}
		snapshot, _ := msp045Capture(t, harness.handler)
		msp045AssertTrust(t, snapshot, "unknown", false, "denied-trust")
	})

	t.Run("revocation", func(t *testing.T) {
		harness := newMSP045ProductHarness(t, nil)
		setFuture(harness)
		if got := harness.resources.coordinator.revoke(context.Background(), msp045RevocationRequest(t, harness.resources.coordinator)); got != "revocation_outcome_unknown" {
			t.Fatalf("revocation = %q, want revocation_outcome_unknown", got)
		}
		snapshot, _ := msp045Capture(t, harness.handler)
		msp045AssertTrust(t, snapshot, "unknown", false, "denied-trust")
	})

	t.Run("repair", func(t *testing.T) {
		harness := newMSP045ProductHarness(t, func(setup *msp045ProductSetup) {
			setup.storeOutcome = "key_provider_unavailable"
			setup.view.associations = nil
		})
		setFuture(harness)
		request := exactRuntimeRepairRequest(harness.resources.coordinator, "recover_unavailable_host_key", msp045Opaque(t))
		if got := harness.resources.coordinator.repair(context.Background(), request); got != "repair_outcome_unknown" {
			t.Fatalf("repair = %q, want repair_outcome_unknown", got)
		}
		snapshot, _ := msp045Capture(t, harness.handler)
		msp045AssertTrust(t, snapshot, "unknown", false, "denied-trust")
	})

	t.Run("retry", func(t *testing.T) {
		harness := newMSP045ProductHarness(t, nil)
		setFuture(harness)
		coordinator := harness.resources.coordinator
		coordinator.mu.Lock()
		target := cloneFirstTrustControlRecord(coordinator.controlView.control)
		target.controlEpoch++
		outcome := coordinator.publishFirstTrustRetryLocked(context.Background(), target)
		state, reason := coordinator.recovery, coordinator.recoveryReasonCode
		coordinator.mu.Unlock()
		if outcome != "unknown" || state != "QUARANTINED" || reason != "DURABILITY_UNKNOWN" {
			t.Fatalf("retry = %q %s/%s, want unknown QUARANTINED/DURABILITY_UNKNOWN", outcome, state, reason)
		}
	})

	t.Run("predial", func(t *testing.T) {
		harness := newMSP045ProductHarness(t, nil)
		setFuture(harness)
		coordinator := harness.resources.coordinator
		coordinator.mu.Lock()
		epoch := coordinator.controlView.control.controlEpoch
		target := cloneFirstTrustControlRecord(coordinator.controlView.control)
		target.controlEpoch++
		coordinator.mu.Unlock()
		_, outcome := coordinator.publishOutgoingAttemptControl(context.Background(), epoch, target, msp045Opaque(t), "attempt_prepare")
		if outcome != "unknown" || coordinator.recoveryState() != "QUARANTINED" || coordinator.recoveryReason() != "DURABILITY_UNKNOWN" {
			t.Fatalf("predial = %q %s/%s, want unknown QUARANTINED/DURABILITY_UNKNOWN", outcome, coordinator.recoveryState(), coordinator.recoveryReason())
		}
	})
}

func TestMSP045RepairAndRevocationPublishWithoutNetworkCallback(t *testing.T) {
	t.Run("repair", func(t *testing.T) {
		environment := newMSP045RealEnvironment(t)
		harness := environment.acquire(t, "repair")
		updates := msp045InstallPublisher(harness.handler)
		request := exactRuntimeRepairRequest(harness.resources.coordinator, "recover_unavailable_host_key", msp045Opaque(t))
		if got := harness.resources.coordinator.repair(context.Background(), request); got != "repaired_unpaired" {
			t.Fatalf("repair = %q", got)
		}
		snapshot := msp045WaitForTrust(t, updates, "unpaired")
		msp045AssertTrust(t, snapshot, "unpaired", false, "no-visible-services")
	})

	t.Run("revocation", func(t *testing.T) {
		harness := newMSP045ProductHarness(t, nil)
		updates := msp045InstallPublisher(harness.handler)
		result := harness.resources.coordinator.revoke(context.Background(), msp045RevocationRequest(t, harness.resources.coordinator))
		if result != "revocation_withdrawal_incomplete" && result != "revoked" {
			t.Fatalf("revocation = %q", result)
		}
		snapshot := msp045WaitForTrust(t, updates, "denied")
		msp045AssertTrust(t, snapshot, "denied", false, "denied-trust")
	})
}

func TestMSP045StartupPublishesAuthoritativeProjectionWithoutNetworkCallback(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*msp045ProductSetup)
		wantState  string
		wantPaired bool
		wantReason string
	}{
		{name: "durably trusted", wantState: "paired", wantPaired: true, wantReason: "no-visible-services"},
		{name: "not yet trusted", mutate: func(setup *msp045ProductSetup) {
			setup.view.associations = nil
		}, wantState: "unpaired", wantReason: "no-visible-services"},
		{name: "terminal denial", mutate: func(setup *msp045ProductSetup) {
			msp045AddTombstone(t, setup)
		}, wantState: "denied", wantReason: "denied-trust"},
		{name: "identity unavailable", mutate: func(setup *msp045ProductSetup) {
			setup.storeOutcome = "key_provider_unavailable"
			setup.view.associations = nil
		}, wantState: "unknown", wantReason: "certificate-unavailable"},
		{name: "structural indeterminate", mutate: func(setup *msp045ProductSetup) {
			setup.storeOutcome = "no_valid_manifest"
		}, wantState: "unknown", wantReason: "denied-trust"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newMSP045ProductHarness(t, test.mutate)
			snapshot := msp045RunInitialPublication(t, harness.backend)
			msp045AssertTrust(t, snapshot, test.wantState, test.wantPaired, test.wantReason)
		})
	}
}

func TestMSP045UnknownAndImpossibleProductsAreDeterministic(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*firstTrustCoordinator)
	}{
		{name: "unknown phase", mutate: func(coordinator *firstTrustCoordinator) { coordinator.phase = firstTrustPhase(255) }},
		{name: "unknown recovery", mutate: func(coordinator *firstTrustCoordinator) { coordinator.recovery = "FUTURE_RECOVERY" }},
		{name: "unknown reason", mutate: func(coordinator *firstTrustCoordinator) {
			coordinator.recovery = "QUARANTINED"
			coordinator.recoveryReasonCode = "FUTURE_REASON"
		}},
		{name: "unknown quarantine", mutate: func(coordinator *firstTrustCoordinator) {
			coordinator.recovery = "QUARANTINED"
			coordinator.controlView.control.quarantines = []firstTrustQuarantineRecord{{scope: msp045Opaque(t), state: "FUTURE_QUARANTINE"}}
		}},
		{name: "unknown attempt", mutate: func(coordinator *firstTrustCoordinator) {
			coordinator.controlView.control.attempts = []firstTrustOutgoingAttemptRecord{{state: "FUTURE_ATTEMPT", attemptID: msp045Opaque(t)}}
		}},
		{name: "reopen in progress", mutate: func(coordinator *firstTrustCoordinator) { coordinator.reopening = true }},
		{name: "repair in progress", mutate: func(coordinator *firstTrustCoordinator) {
			coordinator.recoveryOperation = &firstTrustRecoveryOperation{operationID: msp045Opaque(t), operationClass: "repair"}
		}},
		{name: "reconciliation in progress", mutate: func(coordinator *firstTrustCoordinator) {
			coordinator.anchorRecord.pending = &firstTrustPendingPublication{operationID: msp045Opaque(t)}
		}},
		{name: "impossible phase recovery", mutate: func(coordinator *firstTrustCoordinator) {
			coordinator.phase = firstTrustOpenEmpty
			coordinator.recovery = "PAIRED_TRUSTED"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newMSP045ProductHarness(t, nil)
			harness.resources.coordinator.mu.Lock()
			test.mutate(harness.resources.coordinator)
			harness.resources.coordinator.mu.Unlock()
			var digest [32]byte
			var last runtimeSnapshotPayload
			for iteration := 0; iteration < 16; iteration++ {
				snapshot, payload := msp045Capture(t, harness.handler)
				last = snapshot
				current := sha256.Sum256(payload)
				if iteration == 0 {
					digest = current
				} else if current != digest {
					t.Fatal("identical impossible product produced a different snapshot hash")
				}
			}
			msp045AssertTrust(t, last, "unknown", false, "denied-trust")
		})
	}
}

func TestMSP045ProjectionOrderAndHashAreDeterministic(t *testing.T) {
	remotes := []RuntimeRemote{
		{SKI: msp045RandomSKI(t), Allowlisted: true},
		{SKI: msp045RandomSKI(t), Pretrusted: true},
		{SKI: msp045RandomSKI(t), Allowlisted: true, Pretrusted: true},
	}
	wantOrder := []string{remotes[0].SKI, remotes[1].SKI, remotes[2].SKI}
	sort.Strings(wantOrder)
	handler, err := newRuntimeServiceHandler(RuntimeConfig{Remotes: remotes}, msp045RandomSKI(t), msp045Clock().Now)
	if err != nil {
		t.Fatal(err)
	}
	graph := handler.reducer.Snapshot()
	if len(graph) != len(wantOrder) {
		t.Fatalf("graph cardinality = %d, want %d", len(graph), len(wantOrder))
	}
	for index, remote := range graph {
		if remote.RemoteSKI != wantOrder[index] {
			t.Fatalf("graph order[%d] = %q, want %q", index, remote.RemoteSKI, wantOrder[index])
		}
	}

	fixedTime := msp045Clock().Now()
	var wantPayload []byte
	var wantHash [32]byte
	for iteration := 0; iteration < 32; iteration++ {
		payload, marshalErr := marshalRuntimeSnapshot(handler.reducer.Snapshot(), fixedTime)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		digest := sha256.Sum256(payload)
		if iteration == 0 {
			wantPayload = bytes.Clone(payload)
			wantHash = digest
			continue
		}
		if !bytes.Equal(payload, wantPayload) || digest != wantHash {
			t.Fatal("unchanged projection produced unstable bytes or hash")
		}
	}
	snapshot := decodeRuntimePayload(t, wantPayload)
	if len(snapshot.Pairing) != len(wantOrder) || len(snapshot.Services) != len(wantOrder) {
		t.Fatalf("projection cardinality = pairing:%d services:%d, want %d each", len(snapshot.Pairing), len(snapshot.Services), len(wantOrder))
	}
	for index, remote := range wantOrder {
		redacted, redactErr := eebusraw.RedactID(eebusraw.IDKindRemoteSKI, remote)
		if redactErr != nil {
			t.Fatal(redactErr)
		}
		if snapshot.Pairing[index].Remote != redacted {
			t.Fatalf("pairing order[%d] = %+v, want %+v", index, snapshot.Pairing[index].Remote, redacted)
		}
		if snapshot.Pairing[index].State != "unpaired" || snapshot.Services[index].Paired {
			t.Fatalf("admission-derived row[%d] = pairing:%q paired:%t, want unpaired/false", index, snapshot.Pairing[index].State, snapshot.Services[index].Paired)
		}
	}
}

func TestMSP045ProjectionIsCloneSafeOrderedAndRaceFree(t *testing.T) {
	harness := newMSP045ProductHarness(t, func(setup *msp045ProductSetup) {
		msp045AddTombstone(t, setup)
	})
	baseline, baselinePayload := msp045Capture(t, harness.handler)
	graph := harness.handler.reducer.Snapshot()
	graph[0].PairingState = "paired"
	graph[0].Paired = true
	graph[0].ServiceIDs[0] = "mutated-test-copy"
	cloneCheck, clonePayload := msp045Capture(t, harness.handler)
	if !reflect.DeepEqual(baseline, cloneCheck) || !bytes.Equal(baselinePayload, clonePayload) {
		t.Fatal("mutating a returned graph clone changed the reducer-owned snapshot")
	}

	var payloadsMu sync.Mutex
	payloads := make([][]byte, 0, 256)
	harness.handler.setPublisher(func(payload []byte) {
		payloadsMu.Lock()
		payloads = append(payloads, bytes.Clone(payload))
		payloadsMu.Unlock()
	})
	var workers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		go func(offset int) {
			defer workers.Done()
			for iteration := 0; iteration < 16; iteration++ {
				if (offset+iteration)%2 == 0 {
					harness.reader.ServicePairingDetailUpdate(harness.remoteSKI, shipapi.NewConnectionStateDetail(shipapi.ConnectionStateTrusted, nil))
				} else {
					harness.reader.ServicePairingDetailUpdate(harness.remoteSKI, shipapi.NewConnectionStateDetail(shipapi.ConnectionStateCompleted, nil))
				}
				harness.reader.VisibleRemoteServicesUpdated(nil, []shipapi.RemoteService{{Ski: harness.remoteSKI}})
				_ = harness.handler.publishCurrent()
			}
		}(worker)
	}
	workers.Wait()
	payloadsMu.Lock()
	observed := append([][]byte(nil), payloads...)
	payloadsMu.Unlock()
	if len(observed) == 0 {
		t.Fatal("concurrent projection produced no snapshots")
	}
	for _, payload := range observed {
		snapshot := decodeRuntimePayload(t, payload)
		msp045AssertTrust(t, snapshot, "denied", false, "denied-trust")
	}
	final, finalPayload := msp045Capture(t, harness.handler)
	msp045AssertTrust(t, final, "denied", false, "denied-trust")
	if bytes.Equal(baselinePayload, finalPayload) && !final.Services[0].Visible {
		t.Fatal("liveness callbacks did not update visible state")
	}
}

func TestMSP045OlderProjectionCannotOverwriteNewerDenial(t *testing.T) {
	harness := newMSP045ProductHarness(t, nil)
	paired := harness.resources.coordinator.captureTrustAdminProjection()
	if len(paired.remotes) != 1 || !paired.remotes[0].paired {
		t.Fatalf("paired projection = %+v", paired)
	}
	denied := cloneTrustAdminProjection(paired)
	denied.revision++
	denied.degradation = "denied-trust"
	denied.remotes[0].state = "denied"
	denied.remotes[0].paired = false

	var payloads [][]byte
	harness.handler.setPublisher(func(payload []byte) {
		payloads = append(payloads, bytes.Clone(payload))
	})
	if err := harness.handler.publishTrustAdminProjection(denied); err != nil {
		t.Fatal(err)
	}
	if err := harness.handler.publishTrustAdminProjection(paired); err != nil {
		t.Fatal(err)
	}
	if len(payloads) != 1 {
		t.Fatalf("ordered publisher emitted %d snapshots, want only the newer denial", len(payloads))
	}
	msp045AssertTrust(t, decodeRuntimePayload(t, payloads[0]), "denied", false, "denied-trust")
}

func TestMSP045ConfiguredRemoteCandidateCannotMutatePublicLiveness(t *testing.T) {
	harness := newMSP045ProductHarness(t, func(setup *msp045ProductSetup) {
		setup.view.associations = nil
	})
	baseline, baselinePayload := msp045Capture(t, harness.handler)
	if got := harness.resources.coordinator.openPairingWindow(context.Background(), msp045RunToken(t, "same-ski-window"), time.Minute); got != "open_empty" {
		t.Fatalf("open pairing window = %q", got)
	}
	harness.reader.ServicePairingDetailUpdate(harness.remoteSKI, shipapi.NewConnectionStateDetail(shipapi.ConnectionStateReceivedPairingRequest, nil))
	harness.reader.ServiceShipIDUpdate(harness.remoteSKI, msp045RunToken(t, "candidate-service"))
	harness.reader.RemoteSKIConnected(nil, harness.remoteSKI)
	harness.reader.VisibleRemoteServicesUpdated(nil, []shipapi.RemoteService{{Ski: harness.remoteSKI}})
	candidate, candidatePayload := msp045Capture(t, harness.handler)
	msp045AssertSamePublicSnapshot(t, baseline, baselinePayload, candidate, candidatePayload)
}

func TestMSP045UnknownRecoveryReasonOutranksTerminalHold(t *testing.T) {
	for _, state := range []string{"ADMIN_HOLD", "BACKOFF_ACTIVE"} {
		t.Run(state, func(t *testing.T) {
			harness := newMSP045ProductHarness(t, func(setup *msp045ProductSetup) {
				record := firstTrustQuarantineRecord{
					scope: msp045Opaque(t), state: state, reason: state,
					retentionBudget: time.Minute, lastControlEpoch: setup.view.control.controlEpoch,
				}
				if state == "BACKOFF_ACTIVE" {
					record.attemptCount = 1
					record.remainingDelay = time.Second
				}
				setup.view.associations = nil
				setup.view.control.quarantines = []firstTrustQuarantineRecord{record}
			})
			harness.resources.coordinator.mu.Lock()
			harness.resources.coordinator.recoveryReasonCode = "FUTURE_REASON"
			harness.resources.coordinator.mu.Unlock()
			snapshot, _ := msp045Capture(t, harness.handler)
			msp045AssertTrust(t, snapshot, "unknown", false, "denied-trust")
		})
	}
}

func TestMSP045MalformedOpenedAnchorNeverProjectsPaired(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*msp045ProductSetup)
	}{
		{name: "zero version", mutate: func(setup *msp045ProductSetup) { setup.anchorRecord.version = 0 }},
		{name: "zero identity", mutate: func(setup *msp045ProductSetup) { setup.anchorRecord.anchorIdentity = [32]byte{} }},
		{name: "anchor behind store", mutate: func(setup *msp045ProductSetup) { setup.anchorRecord.manifestGenerationHighWater-- }},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newMSP045ProductHarness(t, test.mutate)
			snapshot, _ := msp045Capture(t, harness.handler)
			msp045AssertTrust(t, snapshot, "unknown", false, "denied-trust")
		})
	}
}

func TestMSP045RestartCannotRetainStalePairedState(t *testing.T) {
	environment := newMSP045RealEnvironment(t)
	first := environment.acquire(t, "first")
	request := exactRuntimeRepairRequest(first.resources.coordinator, "recover_unavailable_host_key", msp045Opaque(t))
	if got := first.resources.coordinator.repair(context.Background(), request); got != "repaired_unpaired" {
		t.Fatalf("repair = %q", got)
	}
	pairRuntimeRemote(t, first.resources, first.remoteSKI, 4_800)
	result := first.resources.coordinator.revoke(context.Background(), msp045RevocationRequest(t, first.resources.coordinator))
	if result != "revocation_withdrawal_incomplete" && result != "revoked" {
		t.Fatalf("revocation = %q", result)
	}
	if err := first.backend.Close(); err != nil {
		t.Fatal(err)
	}
	first.reader.RemoteSKIConnected(nil, first.remoteSKI)
	first.reader.ServicePairingDetailUpdate(first.remoteSKI, shipapi.NewConnectionStateDetail(shipapi.ConnectionStateCompleted, nil))

	second := environment.acquire(t, "second")
	if got := second.resources.coordinator.recoveryState(); got != "REVOKED" {
		t.Fatalf("restart recovery = %q, want REVOKED", got)
	}
	snapshot, _ := msp045Capture(t, second.handler)
	msp045AssertTrust(t, snapshot, "denied", false, "denied-trust")
}

func TestMSP045ProjectionPrivacyCanariesStayOutOfObservationChannels(t *testing.T) {
	harness := newMSP045ProductHarness(t, func(setup *msp045ProductSetup) {
		setup.view.associations = nil
	})
	if got := harness.resources.coordinator.openPairingWindow(context.Background(), msp045RunToken(t, "privacy-window"), time.Minute); got != "open_empty" {
		t.Fatalf("open pairing window = %q", got)
	}
	candidateSKI := msp045RandomSKI(t)
	harness.reader.ServicePairingDetailUpdate(candidateSKI, shipapi.NewConnectionStateDetail(shipapi.ConnectionStateReceivedPairingRequest, nil))
	privateKey := msp045RunToken(t, "idem"+"potency")
	privateEndpoint := msp045RunToken(t, "endpoint") + ".invalid"
	privatePath := "/" + msp045RunToken(t, "admin") + "/" + msp045RunToken(t, "operation")
	privateOperation := msp045Opaque(t)
	harness.resources.coordinator.mu.Lock()
	candidate := harness.resources.coordinator.currentCandidate
	candidate.shipID = msp045RunToken(t, "candidate-service")
	candidate.requests[privateKey] = firstTrustRequest{
		operation:   "confirm",
		fingerprint: msp045RunToken(t, "candidate-proof"),
		nonce:       msp045RunToken(t, "candidate-challenge"),
	}
	harness.resources.coordinator.controlView.control.attempts = []firstTrustOutgoingAttemptRecord{{
		state: "RESERVED", attemptID: privateOperation, remoteSKI: append([]byte(nil), candidate.remote...),
		endpoint: firstTrustOutgoingAttemptEndpoint{host: privateEndpoint, port: 9}, path: privatePath,
	}}
	privateValues := []string{
		candidateSKI,
		candidate.shipID,
		candidate.nonce,
		privateKey,
		candidate.requests[privateKey].fingerprint,
		candidate.requests[privateKey].nonce,
		privateEndpoint,
		privatePath,
		hex.EncodeToString(privateOperation[:]),
		harness.resources.adminDir,
	}
	harness.resources.coordinator.mu.Unlock()

	_, snapshotPayload := msp045Capture(t, harness.handler)
	_, diagnosticErr := newEEBusService(RuntimeConfig{Interface: privateEndpoint}, runtimeMaterial{}, nil)
	channels := map[string][]byte{
		"snapshot": snapshotPayload,
		"error":    []byte(diagnosticErr.Error()),
	}
	for channel, payload := range channels {
		for _, value := range privateValues {
			if value != "" && bytes.Contains(payload, []byte(value)) {
				t.Fatalf("%s leaks a private per-run canary", channel)
			}
		}
	}

	root := repoRoot(t)
	for _, relative := range []string{
		"internal/eebusstore/testdata/generation-v1-empty.json",
		"internal/eebusinteropsmoke/testdata/g19-replay-v1.json",
	} {
		payload, err := readRuntimeFile(filepath.Join(root, relative))
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range privateValues {
			if value != "" && bytes.Contains(payload, []byte(value)) {
				t.Fatalf("fixture %s leaks a private per-run canary", relative)
			}
		}
	}
}

func TestMSP045AdminWireAndInternalDeclarationBoundaryRemainUnchanged(t *testing.T) {
	if firstTrustAdminVersion != 1 {
		t.Fatalf("admin wire version = %d, want unchanged version 1", firstTrustAdminVersion)
	}
	msp045AssertStructShape(t, reflect.TypeOf(firstTrustAdminReply{}), []string{
		"Correlation:correlation",
		"Outcome:outcome",
		"RecoveryReason:recovery_reason,omitempty",
		"RecoveryState:recovery_state,omitempty",
		"State:state",
	})
	msp045AssertStructShape(t, reflect.TypeOf(firstTrustCandidateReply{}), []string{
		"Finger" + "print:fingerprint_v1",
		"No" + "nce:candidate_nonce",
		"ExpiresAt:expires_at",
		"Connection:connection_generation",
		"StoreGeneration:starting_store_generation",
		"Complete:association_complete",
	})

	projectionName := "Trust" + "Admin" + "Projection"
	for filename, file := range parseImplementationFiles(t) {
		ast.Inspect(file, func(node ast.Node) bool {
			switch declaration := node.(type) {
			case *ast.TypeSpec:
				if ast.IsExported(declaration.Name.Name) && strings.Contains(declaration.Name.Name, projectionName) {
					t.Errorf("%s exports internal projection type %s", filename, declaration.Name.Name)
				}
			case *ast.FuncDecl:
				if ast.IsExported(declaration.Name.Name) && strings.Contains(declaration.Name.Name, projectionName) {
					t.Errorf("%s exports internal projection function %s", filename, declaration.Name.Name)
				}
			}
			return true
		})
	}
}

func TestMSP045ScopedListenerLifecycleIsAvailableThroughTheInternalFacade(t *testing.T) {
	certificate, err := shipcert.CreateCertificate("", "Helianthus", "RO", "scoped-listener-contract")
	if err != nil {
		t.Fatal(err)
	}
	service, err := newEEBusService(RuntimeConfig{
		Interface: "fixture-interface", ListenPort: 4711,
		ListenAddress: netip.MustParseAddrPort("127.0.0.1:4711"),
	}, runtimeMaterial{certificate: certificate, localSKI: certificateSKI(t, certificate)}, nil)
	if err != nil {
		t.Fatalf("construct scoped listener service: %v", err)
	}
	scoped, ok := service.(runtimeScopedService)
	if !ok || scoped.ListenerTerminal() == nil {
		t.Fatal("released service lacks the internal scoped lifecycle contract")
	}
	service.Shutdown()
}

type msp045ProductSetup struct {
	view                  firstTrustControlView
	storeOutcome          string
	anchorRecord          firstTrustAnchorRecord
	anchorOutcome         string
	prepareOutcome        string
	commitOutcome         string
	observeOutcome        string
	invalidPrepared       bool
	blockCommit           bool
	anchorStageOutcome    string
	anchorFinalizeOutcome string
	anchorClearOutcome    string
	remote                []byte
	remoteSKI             string
	remotePretrusted      *bool
	configureRemote       func(*RuntimeRemote)
	discoveryEnabled      bool
}

type msp045RuntimeHarness struct {
	backend   *serviceBackend
	resources *runtimeFirstTrustResources
	handler   *runtimeServiceHandler
	reader    *runtimeServiceReader
	service   *msp045Service
	bridge    *msp045Bridge
	anchor    firstTrustAnchorProvider
	remote    []byte
	remoteSKI string
	clock     *runtimeTestClock
}

func newMSP045ProductHarness(t *testing.T, mutate func(*msp045ProductSetup)) *msp045RuntimeHarness {
	t.Helper()
	remote := msp045RandomBytes(t, 20)
	remoteSKI := hex.EncodeToString(remote)
	storeInstance := msp045Opaque(t)
	lineage := msp045Opaque(t)
	manifest := msp045Manifest(t, 17, 23)
	control := firstTrustControlRecord{
		storeInstance: storeInstance, controlEpoch: 11, associationLineage: lineage,
	}
	association := firstTrustAssociationRecord{
		reference: msp045Opaque(t), lineage: lineage, subject: append([]byte(nil), remote...),
		service: msp045RunToken(t, "durable-service"), active: true, trusted: true,
		allowlisted: true, reconnectable: true,
	}
	setup := msp045ProductSetup{
		view: firstTrustControlView{
			manifest: manifest, control: control, associations: []firstTrustAssociationRecord{association},
		},
		storeOutcome: "opened_current",
		anchorRecord: firstTrustAnchorRecord{
			version: firstTrustAnchorVersion, anchorIdentity: msp045Opaque(t), storeInstance: storeInstance,
			manifestGenerationHighWater: manifest.current.sequence, controlEpochHighWater: control.controlEpoch,
		},
		anchorOutcome:         "opened_anchor",
		prepareOutcome:        "prepared",
		commitOutcome:         "commit_durable",
		observeOutcome:        "published_target",
		anchorStageOutcome:    "anchor_durable",
		anchorFinalizeOutcome: "anchor_durable",
		anchorClearOutcome:    "anchor_durable",
		remote:                remote,
		remoteSKI:             remoteSKI,
	}
	if mutate != nil {
		mutate(&setup)
	}
	bridge := &msp045Bridge{
		view: setup.view, reloadOutcome: setup.storeOutcome, prepareOutcome: setup.prepareOutcome,
		commitOutcome: setup.commitOutcome, observeOutcome: setup.observeOutcome,
		invalidPrepared: setup.invalidPrepared, blockCommit: setup.blockCommit,
	}
	anchor := &msp045Anchor{
		record: setup.anchorRecord, openOutcome: setup.anchorOutcome,
		stageOutcome: setup.anchorStageOutcome, finalizeOutcome: setup.anchorFinalizeOutcome,
		clearOutcome: setup.anchorClearOutcome,
	}
	root := canonicalRuntimeTempDir(t)
	return msp045AcquireHarness(t, msp045AcquireOptions{
		stateRoot: filepath.Join(root, "state"), adminRoot: filepath.Join(root, "admin"),
		remote: setup.remote, bridge: bridge, anchor: anchor, identityProvider: anchor,
		remotePretrusted: setup.remotePretrusted, configureRemote: setup.configureRemote,
		discoveryEnabled: setup.discoveryEnabled,
	})
}

type msp045AcquireOptions struct {
	stateRoot        string
	adminRoot        string
	remote           []byte
	certificate      tls.Certificate
	localSKI         string
	bridge           runtimeAssociationBridge
	anchor           firstTrustAnchorProvider
	identityProvider firstTrustIdentityProvider
	keyProviders     []eebusstore.KeyProviderBinding
	remotePretrusted *bool
	configureRemote  func(*RuntimeRemote)
	discoveryEnabled bool
}

func msp045AcquireHarness(t *testing.T, options msp045AcquireOptions) *msp045RuntimeHarness {
	t.Helper()
	if len(options.certificate.Certificate) == 0 {
		certificate, err := shipcert.CreateCertificate("", "Helianthus Test", "ZZ", msp045RunToken(t, "runtime"))
		if err != nil {
			t.Fatal(err)
		}
		options.certificate = certificate
		options.localSKI = certificateSKI(t, certificate)
	}
	remoteSKI := hex.EncodeToString(options.remote)
	service := &msp045Service{}
	clock := msp045Clock()
	var reader *runtimeServiceReader
	dependencies := defaultRuntimeDependencies
	dependencies.now = clock.Now
	pretrusted := true
	if options.remotePretrusted != nil {
		pretrusted = *options.remotePretrusted
	}
	dependencies.loadMaterial = func(context.Context, string) (runtimeMaterial, error) {
		trusted := make(map[string]bool)
		if pretrusted {
			trusted[remoteSKI] = true
		}
		return runtimeMaterial{
			certificate: options.certificate,
			localSKI:    options.localSKI,
			pretrusted:  trusted,
			firstTrust: &runtimeFirstTrustAuthorization{
				adminRuntimeDir: options.adminRoot, keyProviders: options.keyProviders,
				hostAnchor: options.anchor, identityProvider: options.identityProvider,
			},
		}, nil
	}
	dependencies.newService = func(_ RuntimeConfig, _ runtimeMaterial, callback eebusapi.ServiceReaderInterface) (runtimeService, error) {
		reader = callback.(*runtimeServiceReader)
		return service, nil
	}
	if options.bridge != nil {
		dependencies.openAssociationBridge = func(string, []eebusstore.KeyProviderBinding) (runtimeAssociationBridge, string) {
			return options.bridge, "opened_current"
		}
	}
	dependencies.startFirstTrustAdmin = func(context.Context, string, *firstTrustCoordinator) (firstTrustAdminEndpoint, error) {
		return msp045AdminEndpoint{}, nil
	}
	remoteConfig := RuntimeRemote{SKI: remoteSKI, Pretrusted: pretrusted, Allowlisted: true}
	if options.configureRemote != nil {
		options.configureRemote(&remoteConfig)
	}
	backendInterface, err := acquireRuntime(context.Background(), RuntimeConfig{
		StateRoot: options.stateRoot, Interface: "synthetic-interface", ListenPort: 47_11,
		DiscoveryEnabled: options.discoveryEnabled,
		Remotes:          []RuntimeRemote{remoteConfig},
	}, dependencies)
	if err != nil {
		t.Fatalf("acquire MSP-045 runtime: %v", err)
	}
	backend := backendInterface.(*serviceBackend)
	harness := &msp045RuntimeHarness{
		backend: backend, resources: backend.firstTrust, handler: backend.handler, reader: reader,
		service: service, remote: append([]byte(nil), options.remote...), remoteSKI: remoteSKI,
		bridge: nil, anchor: options.anchor, clock: clock,
	}
	if bridge, ok := options.bridge.(*msp045Bridge); ok {
		harness.bridge = bridge
	}
	t.Cleanup(func() {
		if err := backend.Close(); err != nil {
			t.Errorf("close MSP-045 runtime: %v", err)
		}
	})
	return harness
}

type msp045RealEnvironment struct {
	root        string
	stateRoot   string
	anchor      *runtimeStrictAnchor
	certificate tls.Certificate
	localSKI    string
	remote      []byte
}

func newMSP045RealEnvironment(t *testing.T) *msp045RealEnvironment {
	t.Helper()
	certificate, err := shipcert.CreateCertificate("", "Helianthus Test", "ZZ", msp045RunToken(t, "restart"))
	if err != nil {
		t.Fatal(err)
	}
	root := canonicalRuntimeTempDir(t)
	return &msp045RealEnvironment{
		root: root, stateRoot: filepath.Join(root, "state"), anchor: &runtimeStrictAnchor{},
		certificate: certificate, localSKI: certificateSKI(t, certificate), remote: msp045RandomBytes(t, 20),
	}
}

func (environment *msp045RealEnvironment) acquire(t *testing.T, suffix string) *msp045RuntimeHarness {
	t.Helper()
	return msp045AcquireHarness(t, msp045AcquireOptions{
		stateRoot: environment.stateRoot, adminRoot: filepath.Join(environment.root, "admin-"+suffix),
		remote: environment.remote, certificate: environment.certificate, localSKI: environment.localSKI,
		anchor: environment.anchor, identityProvider: environment.anchor,
		keyProviders: []eebusstore.KeyProviderBinding{environment.anchor.keyBinding()},
	})
}

type msp045Bridge struct {
	mu              sync.Mutex
	view            firstTrustControlView
	reloadOutcome   string
	prepareOutcome  string
	commitOutcome   string
	observeOutcome  string
	invalidPrepared bool
	blockCommit     bool
	closed          bool
}

func (bridge *msp045Bridge) Reload(context.Context) (uint64, map[string]string, string) {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	trusted := make(map[string]string)
	for _, association := range bridge.view.associations {
		if firstTrustAssociationUsable(association, bridge.view.control.associationLineage) {
			trusted[string(association.subject)] = association.service
		}
	}
	return bridge.view.manifest.current.sequence, trusted, bridge.reloadOutcome
}

func (bridge *msp045Bridge) SelectedGeneration() uint64 {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	if bridge.closed {
		return 0
	}
	return bridge.view.manifest.current.sequence
}

func (bridge *msp045Bridge) Commit(context.Context, uint64, []byte, string) string {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	return bridge.commitOutcome
}

func (bridge *msp045Bridge) ReloadControl(context.Context) (firstTrustControlView, string) {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	return cloneFirstTrustControlView(bridge.view), bridge.reloadOutcome
}

func (bridge *msp045Bridge) PrepareControl(_ context.Context, previous firstTrustControlView, target firstTrustControlRecord, operationID [32]byte, operationClass string) (firstTrustPreparedPublication, string) {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	publication := firstTrustPreparedPublication{
		previous: cloneFirstTrustControlView(previous), target: cloneFirstTrustControlView(previous),
		operationID: operationID, operationClass: operationClass,
	}
	publication.target.control = cloneFirstTrustControlRecord(target)
	publication.target.manifest = msp045ManifestFrom(previous.manifest.current.sequence+1, previous.manifest.epoch+1)
	if bridge.invalidPrepared {
		publication.target.control.controlEpoch++
	}
	return publication, bridge.prepareOutcome
}

func (bridge *msp045Bridge) CommitControl(ctx context.Context, publication firstTrustPreparedPublication) string {
	bridge.mu.Lock()
	blocked := bridge.blockCommit
	outcome := bridge.commitOutcome
	bridge.mu.Unlock()
	if blocked {
		<-ctx.Done()
		return "commit_durability_unknown"
	}
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	if outcome == "commit_durable" || outcome == "commit_applied_maintenance_failed" {
		bridge.view = cloneFirstTrustControlView(publication.target)
	}
	return outcome
}

func (bridge *msp045Bridge) ObserveControlPublication(context.Context, firstTrustPendingPublication) string {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	return bridge.observeOutcome
}

func (bridge *msp045Bridge) Close() error {
	bridge.mu.Lock()
	bridge.closed = true
	bridge.mu.Unlock()
	return nil
}

type msp045Anchor struct {
	mu              sync.Mutex
	record          firstTrustAnchorRecord
	openOutcome     string
	stageOutcome    string
	finalizeOutcome string
	clearOutcome    string
}

func (anchor *msp045Anchor) Open(context.Context) (firstTrustAnchorRecord, string) {
	anchor.mu.Lock()
	defer anchor.mu.Unlock()
	return cloneFirstTrustAnchorRecord(anchor.record), anchor.openOutcome
}

func (anchor *msp045Anchor) CompareAndStage(_ context.Context, expected firstTrustAnchorRecord, pending firstTrustPendingPublication) string {
	anchor.mu.Lock()
	defer anchor.mu.Unlock()
	if anchor.stageOutcome == "anchor_durable" && firstTrustAnchorRecordEqual(anchor.record, expected) {
		anchor.record.pending = firstTrustPendingPointer(pending)
	}
	return anchor.stageOutcome
}

func (anchor *msp045Anchor) CompareAndFinalize(_ context.Context, pending firstTrustPendingPublication) string {
	anchor.mu.Lock()
	defer anchor.mu.Unlock()
	if anchor.finalizeOutcome == "anchor_durable" && anchor.record.pending != nil && firstTrustPendingPublicationEqual(*anchor.record.pending, pending) {
		anchor.record.manifestGenerationHighWater = pending.targetManifest.current.sequence
		anchor.record.controlEpochHighWater = pending.targetControlEpoch
		anchor.record.pending = nil
	}
	return anchor.finalizeOutcome
}

func (anchor *msp045Anchor) CompareAndClear(_ context.Context, pending firstTrustPendingPublication) string {
	anchor.mu.Lock()
	defer anchor.mu.Unlock()
	if anchor.clearOutcome == "anchor_durable" && anchor.record.pending != nil && firstTrustPendingPublicationEqual(*anchor.record.pending, pending) {
		anchor.record.pending = nil
	}
	return anchor.clearOutcome
}

func (anchor *msp045Anchor) Create(_ context.Context, version uint64, storeInstance [32]byte) (firstTrustAnchorRecord, string) {
	anchor.mu.Lock()
	defer anchor.mu.Unlock()
	anchor.record = firstTrustAnchorRecord{version: version, anchorIdentity: msp045StaticOpaque(901), storeInstance: storeInstance}
	return cloneFirstTrustAnchorRecord(anchor.record), "anchor_durable"
}

func (*msp045Anchor) CreateSigningIdentity(context.Context) (firstTrustLocalIdentityBinding, string) {
	return firstTrustLocalIdentityBinding{}, "identity_not_published"
}

type msp045Service struct {
	mu                      sync.Mutex
	startOnce               sync.Once
	started                 chan struct{}
	shutdowns               int
	registered              []string
	queued                  []string
	reported                []msp045EndpointReport
	cancelled               []string
	pairingRegistration     []bool
	endpointOperationLocked bool
	coordinatorLockIsHeld   func() bool
	events                  []string
}

type msp045EndpointReport struct {
	ski      string
	endpoint shipapi.RemoteEndpoint
}

func (service *msp045Service) Setup() error {
	service.mu.Lock()
	if service.started == nil {
		service.started = make(chan struct{})
	}
	service.mu.Unlock()
	return nil
}

func (service *msp045Service) Start() {
	service.startOnce.Do(func() {
		service.mu.Lock()
		started := service.started
		service.mu.Unlock()
		close(started)
	})
}

func (service *msp045Service) Shutdown() {
	service.mu.Lock()
	service.shutdowns++
	service.mu.Unlock()
}

func (service *msp045Service) RegisterRemoteSKI(ski string) {
	service.mu.Lock()
	service.registered = append(service.registered, ski)
	service.events = append(service.events, "register")
	service.mu.Unlock()
}

func (service *msp045Service) QueueRemoteSKI(ski string) error {
	service.recordEndpointLockState()
	service.mu.Lock()
	service.queued = append(service.queued, ski)
	service.events = append(service.events, "queue")
	service.mu.Unlock()
	return nil
}

func (service *msp045Service) ReportRemoteEndpoint(ski string, endpoint shipapi.RemoteEndpoint) error {
	service.recordEndpointLockState()
	service.mu.Lock()
	service.reported = append(service.reported, msp045EndpointReport{ski: ski, endpoint: endpoint})
	service.events = append(service.events, "report")
	service.mu.Unlock()
	return nil
}

func (*msp045Service) LocalService() *shipapi.ServiceDetails      { return nil }
func (*msp045Service) LocalDevice() spineapi.DeviceLocalInterface { return nil }
func (*msp045Service) SetAutoAccept(bool)                         {}

func (service *msp045Service) CancelPairingWithSKI(ski string) {
	service.mu.Lock()
	service.cancelled = append(service.cancelled, ski)
	service.events = append(service.events, "cancel")
	retained := service.queued[:0]
	for _, queued := range service.queued {
		if queued != ski {
			retained = append(retained, queued)
		}
	}
	service.queued = retained
	service.mu.Unlock()
}

func (service *msp045Service) SetPairingRegistration(value bool) error {
	service.mu.Lock()
	service.pairingRegistration = append(service.pairingRegistration, value)
	if value {
		service.events = append(service.events, "pairing-registration-open")
	} else {
		service.events = append(service.events, "pairing-registration-closed")
	}
	service.mu.Unlock()
	return nil
}

func (service *msp045Service) recordEndpointLockState() {
	service.mu.Lock()
	probe := service.coordinatorLockIsHeld
	service.mu.Unlock()
	if probe == nil || !probe() {
		return
	}
	service.mu.Lock()
	service.endpointOperationLocked = true
	service.mu.Unlock()
}

type msp045AdminEndpoint struct{}

func (msp045AdminEndpoint) Address() string { return "synthetic-admin-endpoint" }
func (msp045AdminEndpoint) Close() error    { return nil }

type msp045CandidateRequest struct {
	proof      string
	nonce      string
	expiry     time.Time
	connection uint64
	generation uint64
}

func msp045ConfirmCandidate(t *testing.T, harness *msp045RuntimeHarness, connection uint64) string {
	t.Helper()
	candidate := msp045PrepareCandidate(t, harness, connection)
	return harness.resources.coordinator.confirm(
		context.Background(), msp045RunToken(t, "confirm"), candidate.proof, candidate.nonce,
		candidate.expiry, candidate.connection, candidate.generation,
	)
}

func msp045PrepareCandidate(t *testing.T, harness *msp045RuntimeHarness, connection uint64) msp045CandidateRequest {
	t.Helper()
	if got := harness.resources.coordinator.openPairingWindow(context.Background(), msp045RunToken(t, "confirm-window"), time.Minute); got != "open_empty" {
		t.Fatalf("open pairing window = %q", got)
	}
	harness.resources.facade.ServiceShipIDUpdate(harness.remoteSKI, msp045RunToken(t, "confirm-service"))
	harness.resources.facade.RemoteSKIConnected(nil, harness.remoteSKI)
	harness.resources.facade.ServicePairingDetailUpdate(harness.remoteSKI, shipapi.NewConnectionStateDetail(shipapi.ConnectionStateReceivedPairingRequest, nil))
	proof, nonce, expiry, candidateConnection, generation, complete, ok := harness.resources.coordinator.candidate()
	if !ok || !complete {
		t.Fatal("confirmation did not produce a complete candidate")
	}
	return msp045CandidateRequest{proof: proof, nonce: nonce, expiry: expiry, connection: candidateConnection, generation: generation}
}

func msp045SetBridgeOutcome(harness *msp045RuntimeHarness, outcome string, invalidPrepared, blockCommit bool) {
	harness.bridge.mu.Lock()
	harness.bridge.commitOutcome = outcome
	harness.bridge.invalidPrepared = invalidPrepared
	harness.bridge.blockCommit = blockCommit
	harness.bridge.mu.Unlock()
}

func msp045SetAnchorOutcomes(harness *msp045RuntimeHarness, stage, finalize, clear string) {
	anchor := harness.anchor.(*msp045Anchor)
	anchor.mu.Lock()
	if stage != "" {
		anchor.stageOutcome = stage
	}
	if finalize != "" {
		anchor.finalizeOutcome = finalize
	}
	if clear != "" {
		anchor.clearOutcome = clear
	}
	anchor.mu.Unlock()
}

func msp045RevocationRequest(t *testing.T, coordinator *firstTrustCoordinator) firstTrustRevocationRequest {
	t.Helper()
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if len(coordinator.controlView.associations) != 1 {
		t.Fatalf("association count = %d, want 1", len(coordinator.controlView.associations))
	}
	association := coordinator.controlView.associations[0]
	operationID := msp045Opaque(t)
	binary.BigEndian.PutUint64(operationID[24:], coordinator.controlView.control.operationHighWater+1)
	return firstTrustRevocationRequest{
		operationID: operationID, associationRef: association.reference,
		associationLineage:     coordinator.controlView.control.associationLineage,
		expectedGeneration:     coordinator.controlView.manifest.current,
		expectedManifestEpoch:  coordinator.controlView.manifest.epoch,
		expectedManifestSHA256: coordinator.controlView.manifest.sha256,
		expectedControlEpoch:   coordinator.controlView.control.controlEpoch,
	}
}

func msp045InstallPublisher(handler *runtimeServiceHandler) <-chan []byte {
	updates := make(chan []byte, 64)
	handler.setPublisher(func(payload []byte) {
		updates <- bytes.Clone(payload)
	})
	return updates
}

func msp045RunInitialPublication(t *testing.T, backend *serviceBackend) runtimeSnapshotPayload {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var payload []byte
	if err := backend.Run(ctx, func(current []byte) {
		payload = bytes.Clone(current)
		cancel()
	}); err != nil {
		t.Fatalf("runtime startup did not publish its authoritative classification: %v", err)
	}
	if len(payload) == 0 {
		t.Fatal("runtime startup returned without an initial publication")
	}
	return decodeRuntimePayload(t, payload)
}

func msp045WaitForTrust(t *testing.T, updates <-chan []byte, state string) runtimeSnapshotPayload {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	var last runtimeSnapshotPayload
	for {
		select {
		case payload := <-updates:
			last = decodeRuntimePayload(t, payload)
			if len(last.Pairing) == 1 && last.Pairing[0].State == state {
				return last
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for trust state %q; last snapshot = %+v", state, last)
		}
	}
}

func msp045Capture(t *testing.T, handler *runtimeServiceHandler) (runtimeSnapshotPayload, []byte) {
	t.Helper()
	updates := make(chan []byte, 1)
	handler.setPublisher(func(payload []byte) { updates <- bytes.Clone(payload) })
	if err := handler.publishCurrent(); err != nil {
		t.Fatal(err)
	}
	payload := <-updates
	return decodeRuntimePayload(t, payload), payload
}

func msp045AssertTrust(t *testing.T, snapshot runtimeSnapshotPayload, state string, paired bool, reason string) {
	t.Helper()
	if len(snapshot.Pairing) != 1 || snapshot.Pairing[0].State != state {
		t.Fatalf("pairing = %+v, want one %q row", snapshot.Pairing, state)
	}
	if len(snapshot.Services) != 1 || snapshot.Services[0].Paired != paired {
		t.Fatalf("services = %+v, want paired=%t", snapshot.Services, paired)
	}
	if reason == "" {
		return
	}
	if snapshot.Status.State != "degraded" || snapshot.Status.Degradation == nil || snapshot.Status.Degradation.Reason != reason {
		t.Fatalf("status = %+v, want degradation %q", snapshot.Status, reason)
	}
}

func msp045AssertSamePublicSnapshot(t *testing.T, want runtimeSnapshotPayload, wantPayload []byte, got runtimeSnapshotPayload, gotPayload []byte) {
	t.Helper()
	if !reflect.DeepEqual(got, want) || !bytes.Equal(gotPayload, wantPayload) {
		t.Fatalf("ephemeral candidate changed public snapshot\nwant: %s\n got: %s", wantPayload, gotPayload)
	}
}

func msp045AssertStructShape(t *testing.T, typ reflect.Type, want []string) {
	t.Helper()
	got := make([]string, 0, typ.NumField())
	for index := 0; index < typ.NumField(); index++ {
		field := typ.Field(index)
		got = append(got, field.Name+":"+field.Tag.Get("json"))
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s wire fields = %v, want %v", typ.Name(), got, want)
	}
}

func msp045AddTombstone(t *testing.T, setup *msp045ProductSetup) {
	t.Helper()
	association := setup.view.associations[0]
	setup.view.control.tombstones = append(setup.view.control.tombstones, firstTrustRevocationTombstone{
		associationRef: association.reference, revocationEpoch: setup.view.control.controlEpoch,
		operationID: msp045Opaque(t), effectiveGeneration: setup.view.manifest.current,
	})
}

func msp045Manifest(t *testing.T, sequence, epoch uint64) firstTrustManifestBinding {
	t.Helper()
	manifest := msp045ManifestFrom(sequence, epoch)
	manifest.sha256 = msp045Opaque(t)
	manifest.current.sha256 = msp045Opaque(t)
	if manifest.parent != nil {
		manifest.parent.sha256 = msp045Opaque(t)
	}
	return manifest
}

func msp045ManifestFrom(sequence, epoch uint64) firstTrustManifestBinding {
	currentName := msp045StaticOpaque(sequence)
	current := firstTrustGenerationBinding{
		sequence: sequence, filename: "g-" + hex.EncodeToString(currentName[24:]) + ".json",
		sha256: msp045StaticOpaque(sequence + 100), schemaVersion: 3,
	}
	var parent *firstTrustGenerationBinding
	if sequence > 1 {
		parentName := msp045StaticOpaque(sequence - 1)
		value := firstTrustGenerationBinding{
			sequence: sequence - 1, filename: "g-" + hex.EncodeToString(parentName[24:]) + ".json",
			sha256: msp045StaticOpaque(sequence + 99), schemaVersion: 3,
		}
		parent = &value
	}
	return firstTrustManifestBinding{epoch: epoch, sha256: msp045StaticOpaque(epoch + 200), current: current, parent: parent}
}

func msp045StaticOpaque(value uint64) [32]byte {
	var result [32]byte
	binary.BigEndian.PutUint64(result[24:], value)
	result[0] = 1
	return result
}

func msp045Opaque(t *testing.T) [32]byte {
	t.Helper()
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		t.Fatal(err)
	}
	return value
}

func msp045RandomBytes(t *testing.T, size int) []byte {
	t.Helper()
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		t.Fatal(err)
	}
	return value
}

func msp045RandomSKI(t *testing.T) string {
	t.Helper()
	return hex.EncodeToString(msp045RandomBytes(t, 20))
}

func msp045RunToken(t *testing.T, prefix string) string {
	t.Helper()
	return prefix + "-" + hex.EncodeToString(msp045RandomBytes(t, 12))
}

func msp045Clock() *runtimeTestClock {
	return &runtimeTestClock{value: time.Unix(1_900_000_000, 123_000_000).UTC()}
}

func readRuntimeFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

var _ runtimeAssociationBridge = (*msp045Bridge)(nil)
var _ firstTrustAnchorProvider = (*msp045Anchor)(nil)
var _ firstTrustIdentityProvider = (*msp045Anchor)(nil)
var _ runtimeService = (*msp045Service)(nil)
var _ firstTrustService = (*msp045Service)(nil)
