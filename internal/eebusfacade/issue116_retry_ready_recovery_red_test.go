package eebusfacade

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"
)

func TestIssue116RetryReadyRecoveryStartsBoundaryButNotAutomaticOutbound(t *testing.T) {
	t.Run("exact recovery product starts the operator boundary", func(t *testing.T) {
		coordinator, _, _ := issue116RetryReadyCoordinator(t, 116_001)
		if !coordinator.runtimeStartAuthorized() {
			t.Fatal("exact RETRY_READY/RETRYABLE_FAILURE product denied recovery-only runtime startup")
		}
	})

	t.Run("fresh discovery has no outbound authority before explicit retry", func(t *testing.T) {
		coordinator, association, scope := issue116RetryReadyCoordinator(t, 116_010)
		coordinator.mu.Lock()
		automaticEligible := coordinator.firstTrustOutgoingAttemptEligibleLocked(association.subject)
		coordinator.mu.Unlock()
		if automaticEligible {
			t.Fatal("retry-ready recovery admitted automatic persisted-trusted reconnect")
		}

		if got := coordinator.admitRetry(context.Background(), scope); got != "retry_admitted" {
			t.Fatalf("explicit retry admission = %q, want retry_admitted", got)
		}
		coordinator.mu.Lock()
		explicitEligible := coordinator.firstTrustOutgoingAttemptEligibleLocked(association.subject)
		coordinator.mu.Unlock()
		if !explicitEligible {
			t.Fatal("explicit identity-bound retry did not authorize the exact trusted remote")
		}
		coordinator.completeRetry(scope)
		coordinator.mu.Lock()
		eligibleAfterRelease := coordinator.firstTrustOutgoingAttemptEligibleLocked(association.subject)
		coordinator.mu.Unlock()
		if eligibleAfterRelease {
			t.Fatal("released retry admission retained outbound authority")
		}
	})
}

func TestIssue116RetryReadyStartupRequiresOneExactUsableAssociation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*firstTrustCoordinator, firstTrustAssociationRecord, [32]byte)
	}{
		{
			name: "association absent",
			mutate: func(coordinator *firstTrustCoordinator, _ firstTrustAssociationRecord, _ [32]byte) {
				coordinator.controlView.associations = nil
			},
		},
		{
			name: "association duplicated",
			mutate: func(coordinator *firstTrustCoordinator, association firstTrustAssociationRecord, _ [32]byte) {
				coordinator.controlView.associations = append(coordinator.controlView.associations, association)
			},
		},
		{
			name: "retry scope mismatched",
			mutate: func(coordinator *firstTrustCoordinator, _ firstTrustAssociationRecord, _ [32]byte) {
				coordinator.controlView.control.quarantines[0].scope = msp04cOrdinal(116_099)
			},
		},
		{
			name: "backoff active",
			mutate: func(coordinator *firstTrustCoordinator, _ firstTrustAssociationRecord, _ [32]byte) {
				coordinator.controlView.control.quarantines[0].state = "BACKOFF_ACTIVE"
				coordinator.controlView.control.quarantines[0].remainingDelay = 1
			},
		},
		{
			name: "different recovery reason",
			mutate: func(coordinator *firstTrustCoordinator, _ firstTrustAssociationRecord, _ [32]byte) {
				coordinator.recoveryReasonCode = "ADMIN_HOLD"
			},
		},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			coordinator, association, scope := issue116RetryReadyCoordinator(t, uint64(116_100+index))
			coordinator.mu.Lock()
			test.mutate(coordinator, association, scope)
			coordinator.mu.Unlock()
			if coordinator.runtimeStartAuthorized() {
				t.Fatal("non-exact retry-ready product authorized runtime startup")
			}
		})
	}
}

func TestIssue116AdminRetryOwnsAndReleasesVolatileAdmission(t *testing.T) {
	t.Run("successful request arms exactly once before the service call", func(t *testing.T) {
		coordinator, association, scope := issue116RetryReadyCoordinator(t, 116_200)
		service := newIssue116RetryService(coordinator, scope)
		bridge := newOperatorAdminV1Bridge(coordinator, service, &msp04cOrdinalReader{next: 116_210})
		partner := issue116TrustedPartnerReference(t, bridge)

		transition, failure := bridge.retryTrustedOperatorAdminV1(context.Background(), partner)
		if failure != "" || !transition.changed || transition.outcome != "retry_requested" {
			t.Fatalf("retry transition=%#v failure=%q", transition, failure)
		}
		if !service.armedAtCall || service.retrySKI != hex.EncodeToString(association.subject) {
			t.Fatalf("service observed armed=%t ski=%q", service.armedAtCall, service.retrySKI)
		}
		if _, failure = bridge.retryTrustedOperatorAdminV1(context.Background(), partner); failure != "attempt_in_progress" {
			t.Fatalf("duplicate retry failure=%q, want attempt_in_progress", failure)
		}
		if service.retryCalls != 1 {
			t.Fatalf("retry service calls=%d, want exactly one", service.retryCalls)
		}
		coordinator.completeRetry(scope)
	})

	t.Run("synchronous service failure releases admission", func(t *testing.T) {
		coordinator, _, scope := issue116RetryReadyCoordinator(t, 116_300)
		service := newIssue116RetryService(coordinator, scope)
		service.retryErr = errors.New("synchronous retry rejection")
		bridge := newOperatorAdminV1Bridge(coordinator, service, &msp04cOrdinalReader{next: 116_310})
		partner := issue116TrustedPartnerReference(t, bridge)

		if _, failure := bridge.retryTrustedOperatorAdminV1(context.Background(), partner); failure == "" {
			t.Fatal("synchronous retry failure was reported as success")
		}
		if !service.armedAtCall {
			t.Fatal("service failure occurred before volatile retry admission")
		}
		coordinator.mu.Lock()
		inflight := coordinator.retryInflight[scope]
		coordinator.mu.Unlock()
		if inflight {
			t.Fatal("synchronous retry failure retained volatile admission")
		}
	})
}

type issue116RetryService struct {
	*operatorAdminV1BridgeServiceSpy
	coordinator *firstTrustCoordinator
	scope       [32]byte
	armedAtCall bool
}

func newIssue116RetryService(coordinator *firstTrustCoordinator, scope [32]byte) *issue116RetryService {
	return &issue116RetryService{
		operatorAdminV1BridgeServiceSpy: newOperatorAdminV1BridgeServiceSpy(),
		coordinator:                     coordinator,
		scope:                           scope,
	}
}

func (service *issue116RetryService) RetryTrustedRemote(expectedSKI string) error {
	service.coordinator.mu.Lock()
	service.armedAtCall = service.coordinator.retryInflight[service.scope]
	service.coordinator.mu.Unlock()
	return service.operatorAdminV1BridgeServiceSpy.RetryTrustedRemote(expectedSKI)
}

func issue116RetryReadyCoordinator(
	t *testing.T,
	ordinal uint64,
) (*firstTrustCoordinator, firstTrustAssociationRecord, [32]byte) {
	t.Helper()
	fixture := newMSP04CFixture(t)
	coordinator := fixture.newCoordinator()
	if got := coordinator.reopen(context.Background()); got != "pairing_closed" {
		t.Fatalf("initial coordinator reopen=%q", got)
	}

	lineage := coordinator.controlView.control.associationLineage
	association := msp04cAssociation(ordinal, lineage, true, true, true, true)
	scope := firstTrustRuntimeRetryScope(firstTrustNormalizedSKI(association.subject))
	retry := firstTrustQuarantineRecord{
		scope:            scope,
		reason:           "RETRYABLE_FAILURE",
		state:            "RETRY_READY",
		attemptCount:     1,
		retentionBudget:  firstTrustQuarantineRetention,
		lastControlEpoch: coordinator.controlView.control.controlEpoch,
	}
	coordinator.mu.Lock()
	coordinator.phase = firstTrustDisabled
	coordinator.recovery = "QUARANTINED"
	coordinator.recoveryReasonCode = "RETRYABLE_FAILURE"
	coordinator.controlView.associations = []firstTrustAssociationRecord{association}
	coordinator.controlView.control.quarantines = []firstTrustQuarantineRecord{retry}
	coordinator.trustedRemotes[string(association.subject)] = association.service
	coordinator.mu.Unlock()
	return coordinator, association, scope
}

func issue116TrustedPartnerReference(t *testing.T, bridge *operatorAdminV1Bridge) string {
	t.Helper()
	snapshot, failure := bridge.snapshotOperatorAdminV1(context.Background())
	if failure != "" || len(snapshot.trusted) != 1 || snapshot.trusted[0].reference == "" {
		t.Fatalf("trusted snapshot=%#v failure=%q", snapshot, failure)
	}
	return snapshot.trusted[0].reference
}
