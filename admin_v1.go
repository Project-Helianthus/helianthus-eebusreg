package eebusruntime

import (
	"context"
	"time"
)

type AdminV1 interface {
	Snapshot(context.Context, AdminSnapshotRequestV1) (AdminSnapshotV1, *AdminErrorV1)
	OpenPairingWindow(context.Context, OpenPairingWindowRequestV1) (AdminMutationResultV1, *AdminErrorV1)
	ClosePairingWindow(context.Context, ClosePairingWindowRequestV1) (AdminMutationResultV1, *AdminErrorV1)
	Select(context.Context, SelectRequestV1) (AdminSelectionResultV1, *AdminErrorV1)
	Connect(context.Context, ConnectRequestV1) (AdminMutationResultV1, *AdminErrorV1)
	Confirm(context.Context, ConfirmRequestV1) (AdminMutationResultV1, *AdminErrorV1)
	Cancel(context.Context, CancelRequestV1) (AdminMutationResultV1, *AdminErrorV1)
	RetryTrusted(context.Context, RetryTrustedRequestV1) (AdminMutationResultV1, *AdminErrorV1)
	Untrust(context.Context, UntrustRequestV1) (AdminMutationResultV1, *AdminErrorV1)
}

type MutationPreconditionV1 struct {
	IdempotencyKey        string
	ExpectedStateRevision uint64
}

type AdminSnapshotRequestV1 struct {
	View AdminViewV1
}

type OpenPairingWindowRequestV1 struct {
	MutationPreconditionV1
	Duration time.Duration
}

type ClosePairingWindowRequestV1 struct {
	MutationPreconditionV1
}

type SelectRequestV1 struct {
	MutationPreconditionV1
	Observation ObservationHandleV1
	ExpectedSKI string
}

type ConnectRequestV1 struct {
	MutationPreconditionV1
	Selection SelectionHandleV1
}

type ConfirmRequestV1 struct {
	MutationPreconditionV1
	Candidate   CandidateHandleV1
	ExpectedSKI string
}

type CancelRequestV1 struct {
	MutationPreconditionV1
	Candidate CandidateHandleV1
}

type RetryTrustedRequestV1 struct {
	MutationPreconditionV1
	Partner PartnerHandleV1
}

type UntrustRequestV1 struct {
	MutationPreconditionV1
	Partner PartnerHandleV1
}

type AdminMutationResultV1 struct {
	StateRevision uint64
	Outcome       AdminOutcomeV1
	Replayed      bool
}

type AdminSelectionResultV1 struct {
	AdminMutationResultV1
	Selection SelectionHandleV1
}

type AdminOutcomeV1 string

type AdminViewV1 string

const (
	AdminViewV1Trusted    AdminViewV1 = "trusted"
	AdminViewV1Connected  AdminViewV1 = "connected"
	AdminViewV1Discovered AdminViewV1 = "discovered"
	AdminViewV1Candidate  AdminViewV1 = "candidate"
)

type AdminErrorCodeV1 string

const (
	AdminErrorCodeV1AdminBoundaryUnavailable AdminErrorCodeV1 = "admin_boundary_unavailable"
	AdminErrorCodeV1Unauthenticated          AdminErrorCodeV1 = "unauthenticated"
	AdminErrorCodeV1Forbidden                AdminErrorCodeV1 = "forbidden"
	AdminErrorCodeV1CSRFRejected             AdminErrorCodeV1 = "csrf_rejected"
	AdminErrorCodeV1InvalidRequest           AdminErrorCodeV1 = "invalid_request"
	AdminErrorCodeV1StateConflict            AdminErrorCodeV1 = "state_conflict"
	AdminErrorCodeV1SnapshotExpired          AdminErrorCodeV1 = "snapshot_expired"
	AdminErrorCodeV1IdempotencyConflict      AdminErrorCodeV1 = "idempotency_conflict"
	AdminErrorCodeV1PairingClosed            AdminErrorCodeV1 = "pairing_closed"
	AdminErrorCodeV1ObservationStale         AdminErrorCodeV1 = "observation_stale"
	AdminErrorCodeV1IdentityMismatch         AdminErrorCodeV1 = "identity_mismatch"
	AdminErrorCodeV1AssociationIncomplete    AdminErrorCodeV1 = "association_incomplete"
	AdminErrorCodeV1CandidateExpired         AdminErrorCodeV1 = "candidate_expired"
	AdminErrorCodeV1CandidateBusy            AdminErrorCodeV1 = "candidate_busy"
	AdminErrorCodeV1TrustDenied              AdminErrorCodeV1 = "trust_denied"
	AdminErrorCodeV1ListenerUnavailable      AdminErrorCodeV1 = "listener_unavailable"
	AdminErrorCodeV1DiscoveryUnavailable     AdminErrorCodeV1 = "discovery_unavailable"
	AdminErrorCodeV1AttemptTimeout           AdminErrorCodeV1 = "attempt_timeout"
	AdminErrorCodeV1Disconnected             AdminErrorCodeV1 = "disconnected"
	AdminErrorCodeV1BackoffActive            AdminErrorCodeV1 = "backoff_active"
	AdminErrorCodeV1TerminalQuarantine       AdminErrorCodeV1 = "terminal_quarantine"
	AdminErrorCodeV1PersistenceFailure       AdminErrorCodeV1 = "persistence_failure"
	AdminErrorCodeV1UnknownState             AdminErrorCodeV1 = "unknown_state"
)

type AdminErrorV1 struct {
	Code AdminErrorCodeV1
}

func (failure AdminErrorV1) Error() string {
	if failure.Code == "" {
		return string(AdminErrorCodeV1UnknownState)
	}
	return string(failure.Code)
}

type PartnerHandleV1 struct{ token [16]byte }
type ObservationHandleV1 struct{ token [16]byte }
type SelectionHandleV1 struct{ token [16]byte }
type CandidateHandleV1 struct{ token [16]byte }

type AdminSnapshotV1 struct {
	StateRevision uint64
	Trusted       []TrustedPartnerV1
	Connected     []ConnectedPartnerV1
	Discovered    []DiscoveredPartnerV1
	Candidates    []CandidateV1
}

type TrustedPartnerV1 struct {
	SKI string
}

type ConnectedPartnerV1 struct {
	SKI      string
	Endpoint string
}

type DiscoveredPartnerV1 struct {
	SKI      string
	Endpoint string
}

type CandidateV1 struct{}

type unavailableAdminV1 struct {
	runtime *runtimeImplementation
}

func newUnavailableAdminV1(runtime *runtimeImplementation) AdminV1 {
	return &unavailableAdminV1{runtime: runtime}
}

func newAdminBoundaryUnavailableV1() *AdminErrorV1 {
	return &AdminErrorV1{Code: AdminErrorCodeV1AdminBoundaryUnavailable}
}

func (admin *unavailableAdminV1) unavailable() *AdminErrorV1 {
	if admin != nil && admin.runtime != nil {
		admin.runtime.mu.Lock()
		defer admin.runtime.mu.Unlock()
	}
	return newAdminBoundaryUnavailableV1()
}

func (admin *unavailableAdminV1) Snapshot(context.Context, AdminSnapshotRequestV1) (AdminSnapshotV1, *AdminErrorV1) {
	return AdminSnapshotV1{}, admin.unavailable()
}

func (admin *unavailableAdminV1) OpenPairingWindow(context.Context, OpenPairingWindowRequestV1) (AdminMutationResultV1, *AdminErrorV1) {
	return AdminMutationResultV1{}, admin.unavailable()
}

func (admin *unavailableAdminV1) ClosePairingWindow(context.Context, ClosePairingWindowRequestV1) (AdminMutationResultV1, *AdminErrorV1) {
	return AdminMutationResultV1{}, admin.unavailable()
}

func (admin *unavailableAdminV1) Select(context.Context, SelectRequestV1) (AdminSelectionResultV1, *AdminErrorV1) {
	return AdminSelectionResultV1{}, admin.unavailable()
}

func (admin *unavailableAdminV1) Connect(context.Context, ConnectRequestV1) (AdminMutationResultV1, *AdminErrorV1) {
	return AdminMutationResultV1{}, admin.unavailable()
}

func (admin *unavailableAdminV1) Confirm(context.Context, ConfirmRequestV1) (AdminMutationResultV1, *AdminErrorV1) {
	return AdminMutationResultV1{}, admin.unavailable()
}

func (admin *unavailableAdminV1) Cancel(context.Context, CancelRequestV1) (AdminMutationResultV1, *AdminErrorV1) {
	return AdminMutationResultV1{}, admin.unavailable()
}

func (admin *unavailableAdminV1) RetryTrusted(context.Context, RetryTrustedRequestV1) (AdminMutationResultV1, *AdminErrorV1) {
	return AdminMutationResultV1{}, admin.unavailable()
}

func (admin *unavailableAdminV1) Untrust(context.Context, UntrustRequestV1) (AdminMutationResultV1, *AdminErrorV1) {
	return AdminMutationResultV1{}, admin.unavailable()
}
