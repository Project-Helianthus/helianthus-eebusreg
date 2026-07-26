package eebusfacade

import (
	"bytes"
	"testing"
	"time"
)

func TestIssue75RetryTrustProjectionRequiresExactUsableAssociation(t *testing.T) {
	tests := []struct {
		name        string
		retryState  string
		association string
		scope       string
		malformed   bool
		wantState   string
		wantPaired  bool
		wantReason  string
	}{
		{
			name:       "backoff with exact usable association",
			retryState: "BACKOFF_ACTIVE", association: "exact", scope: "exact",
			wantState: "paired", wantPaired: true,
		},
		{
			name:       "retry ready with exact usable association",
			retryState: "RETRY_READY", association: "exact", scope: "exact",
			wantState: "paired", wantPaired: true,
		},
		{
			name:       "backoff without durable association",
			retryState: "BACKOFF_ACTIVE", association: "absent", scope: "exact",
			wantState: "unpaired",
		},
		{
			name:       "retry ready without durable association",
			retryState: "RETRY_READY", association: "absent", scope: "exact",
			wantState: "unpaired",
		},
		{
			name:       "backoff with mismatched association",
			retryState: "BACKOFF_ACTIVE", association: "mismatched", scope: "exact",
			wantState: "unpaired",
		},
		{
			name:       "retry ready with mismatched association",
			retryState: "RETRY_READY", association: "mismatched", scope: "exact",
			wantState: "unpaired",
		},
		{
			name:       "backoff scope unmatched to otherwise usable association",
			retryState: "BACKOFF_ACTIVE", association: "exact", scope: "unmatched",
			wantState: "unknown", wantReason: "denied-trust",
		},
		{
			name:       "retry ready scope unmatched to otherwise usable association",
			retryState: "RETRY_READY", association: "exact", scope: "unmatched",
			wantState: "unknown", wantReason: "denied-trust",
		},
		{
			name:       "duplicate exact usable associations are structural uncertainty",
			retryState: "BACKOFF_ACTIVE", association: "duplicate", scope: "exact",
			wantState: "unknown", wantReason: "denied-trust",
		},
		{
			name:       "malformed matching backoff is structural uncertainty",
			retryState: "BACKOFF_ACTIVE", association: "exact", scope: "exact", malformed: true,
			wantState: "unknown", wantReason: "denied-trust",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newMSP045ProductHarness(t, nil)
			coordinator := harness.resources.coordinator
			baseline := issue75CaptureProjectionCoordinatorState(coordinator)
			exactScope := firstTrustRuntimeRetryScope(harness.remoteSKI)
			retryScope := exactScope
			if test.scope == "unmatched" {
				retryScope = msp04cOrdinal(75_001)
			}
			record := issue75RetryProjectionRecord(retryScope, test.retryState)
			if test.malformed {
				record.remainingDelay = 0
			}

			coordinator.mu.Lock()
			coordinator.phase = firstTrustPairingClosed
			coordinator.recovery = "PAIRED_TRUSTED"
			coordinator.recoveryReasonCode = ""
			coordinator.controlView.control.quarantines = []firstTrustQuarantineRecord{record}
			switch test.association {
			case "absent":
				coordinator.controlView.associations = nil
				coordinator.trustedRemotes = make(map[string]string)
				coordinator.recovery = "UNPAIRED_LOCKED"
			case "mismatched":
				coordinator.controlView.associations[0].subject = msp04cSubject(75_002)
				coordinator.trustedRemotes = map[string]string{
					string(coordinator.controlView.associations[0].subject): coordinator.controlView.associations[0].service,
				}
			case "duplicate":
				duplicate := coordinator.controlView.associations[0]
				duplicate.reference = msp04cOrdinal(75_003)
				duplicate.service = "duplicate-service"
				coordinator.controlView.associations = append(coordinator.controlView.associations, duplicate)
			}
			coordinator.mu.Unlock()

			projection := coordinator.captureTrustAdminProjection()
			issue75RestoreProjectionCoordinatorState(coordinator, baseline)
			issue75AssertRemoteProjection(
				t,
				projection,
				test.wantState,
				test.wantPaired,
				test.wantReason,
			)
		})
	}
}

func TestIssue75RetryProjectionFailClosedPrecedenceCannotBeExempted(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*firstTrustCoordinator)
		wantState  string
		wantReason string
	}{
		{
			name: "admin hold",
			mutate: func(coordinator *firstTrustCoordinator) {
				scope := firstTrustRuntimeRetryScope(firstTrustNormalizedSKI(coordinator.trustAdminProjection.remotes[0]))
				coordinator.controlView.control.quarantines = []firstTrustQuarantineRecord{{
					scope: scope, reason: "HANDSHAKE_ATTEMPT_LIMIT", state: "ADMIN_HOLD",
					attemptCount:     coordinator.backoffPolicy.attemptMaximum,
					backoffStep:      uint64(coordinator.backoffPolicy.exponentCap),
					retentionBudget:  firstTrustQuarantineRetention,
					lastControlEpoch: coordinator.controlView.control.controlEpoch,
				}}
			},
			wantState: "denied", wantReason: "denied-trust",
		},
		{
			name: "revocation",
			mutate: func(coordinator *firstTrustCoordinator) {
				coordinator.recovery = "REVOKED"
				coordinator.recoveryReasonCode = "REVOKED_ASSOCIATION"
			},
			wantState: "denied", wantReason: "denied-trust",
		},
		{
			name: "current lineage tombstone",
			mutate: func(coordinator *firstTrustCoordinator) {
				association := coordinator.controlView.associations[0]
				coordinator.controlView.control.tombstones = append(
					coordinator.controlView.control.tombstones,
					firstTrustRevocationTombstone{
						associationRef:      association.reference,
						revocationEpoch:     1,
						operationID:         msp04cOrdinal(75_010),
						effectiveGeneration: coordinator.controlView.manifest.current,
					},
				)
			},
			wantState: "denied", wantReason: "denied-trust",
		},
		{
			name: "pending durability",
			mutate: func(coordinator *firstTrustCoordinator) {
				coordinator.anchorRecord.pending = &firstTrustPendingPublication{
					operationID: msp04cOrdinal(75_011),
				}
			},
			wantState: "unknown", wantReason: "denied-trust",
		},
		{
			name: "malformed recovery product",
			mutate: func(coordinator *firstTrustCoordinator) {
				coordinator.recovery = "FUTURE_RECOVERY"
			},
			wantState: "unknown", wantReason: "denied-trust",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newMSP045ProductHarness(t, nil)
			coordinator := harness.resources.coordinator
			baseline := issue75CaptureProjectionCoordinatorState(coordinator)
			coordinator.mu.Lock()
			remote := bytes.Clone(coordinator.trustAdminProjection.remotes[0])
			coordinator.controlView.control.quarantines = []firstTrustQuarantineRecord{
				issue75RetryProjectionRecord(
					firstTrustRuntimeRetryScope(firstTrustNormalizedSKI(remote)),
					"BACKOFF_ACTIVE",
				),
			}
			test.mutate(coordinator)
			coordinator.mu.Unlock()

			projection := coordinator.captureTrustAdminProjection()
			issue75RestoreProjectionCoordinatorState(coordinator, baseline)
			issue75AssertRemoteProjection(t, projection, test.wantState, false, test.wantReason)
		})
	}
}

type issue75ProjectionCoordinatorState struct {
	phase              firstTrustPhase
	recovery           string
	recoveryReasonCode string
	controlView        firstTrustControlView
	anchorRecord       firstTrustAnchorRecord
	trustedRemotes     map[string]string
}

func issue75CaptureProjectionCoordinatorState(
	coordinator *firstTrustCoordinator,
) issue75ProjectionCoordinatorState {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return issue75ProjectionCoordinatorState{
		phase:              coordinator.phase,
		recovery:           coordinator.recovery,
		recoveryReasonCode: coordinator.recoveryReasonCode,
		controlView:        cloneFirstTrustControlView(coordinator.controlView),
		anchorRecord:       cloneFirstTrustAnchorRecord(coordinator.anchorRecord),
		trustedRemotes:     issue75CloneTrustedRemotes(coordinator.trustedRemotes),
	}
}

func issue75RestoreProjectionCoordinatorState(
	coordinator *firstTrustCoordinator,
	state issue75ProjectionCoordinatorState,
) {
	coordinator.mu.Lock()
	coordinator.phase = state.phase
	coordinator.recovery = state.recovery
	coordinator.recoveryReasonCode = state.recoveryReasonCode
	coordinator.controlView = cloneFirstTrustControlView(state.controlView)
	coordinator.anchorRecord = cloneFirstTrustAnchorRecord(state.anchorRecord)
	coordinator.trustedRemotes = issue75CloneTrustedRemotes(state.trustedRemotes)
	coordinator.mu.Unlock()
}

func issue75CloneTrustedRemotes(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for remote, service := range source {
		result[remote] = service
	}
	return result
}

func issue75RetryProjectionRecord(scope [32]byte, state string) firstTrustQuarantineRecord {
	record := firstTrustQuarantineRecord{
		scope:            scope,
		reason:           "RETRYABLE_FAILURE",
		state:            state,
		retentionBudget:  firstTrustQuarantineRetention,
		lastControlEpoch: 1,
	}
	if state == "BACKOFF_ACTIVE" {
		record.attemptCount = 1
		record.remainingDelay = time.Second
	}
	return record
}

func issue75AssertRemoteProjection(
	t *testing.T,
	projection trustAdminProjection,
	wantState string,
	wantPaired bool,
	wantReason string,
) {
	t.Helper()
	if projection.contract != trustAdminProjectionContract || projection.revision == 0 ||
		len(projection.remotes) != 1 {
		t.Fatalf("projection envelope = %+v", projection)
	}
	remote := projection.remotes[0]
	if remote.state != wantState || remote.paired != wantPaired || projection.degradation != wantReason {
		t.Fatalf(
			"projection = state:%s paired:%t degradation:%q, want %s/%t/%q",
			remote.state,
			remote.paired,
			projection.degradation,
			wantState,
			wantPaired,
			wantReason,
		)
	}
}
