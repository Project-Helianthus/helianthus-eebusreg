package eebusruntime

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
	"unicode/utf8"
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

type PartnerHandleV1 struct{ token [32]byte }
type ObservationHandleV1 struct{ token [32]byte }
type SelectionHandleV1 struct{ token [32]byte }
type CandidateHandleV1 struct{ token [32]byte }

type AdminSnapshotV1 struct {
	StateRevision   uint64
	CapturedAt      time.Time
	Status          string
	Window          string
	WindowDeadline  time.Time
	Register        string
	Listener        string
	Discovery       string
	DegradedCode    AdminErrorCodeV1
	TrustedCount    uint16
	ConnectedCount  uint16
	DiscoveredCount uint16
	CandidateCount  uint16
	Trusted         []TrustedPartnerV1
	Connected       []ConnectedPartnerV1
	Discovered      []DiscoveredPartnerV1
	Candidates      []CandidateV1
}

type TrustedPartnerV1 struct {
	Partner    PartnerHandleV1
	SKI        string
	TrustState string
	SHIPID     string
	LastSeen   time.Time
}

type ConnectedPartnerV1 struct {
	SKI             string
	Endpoint        string
	TrustState      string
	ConnectionState string
	SHIPID          string
	LastSeen        time.Time
}

type DiscoveredPartnerV1 struct {
	Observation         ObservationHandleV1
	SKI                 string
	Endpoint            string
	ObservationRevision uint64
	LastSeen            time.Time
	Name                string
	Identifier          string
	Brand               string
	Type                string
	Model               string
}

type CandidateV1 struct {
	Candidate           CandidateHandleV1
	SKI                 string
	State               string
	ExpiresAt           time.Time
	AssociationComplete bool
}

const operatorAdminV1RedactedRendering = "admin_v1{redacted}"

var errOperatorAdminV1Serialization = errors.New("AdminV1 owner value cannot be serialized")

func operatorAdminV1Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, operatorAdminV1RedactedRendering)
}
func operatorAdminV1String() string               { return operatorAdminV1RedactedRendering }
func operatorAdminV1MarshalJSON() ([]byte, error) { return nil, errOperatorAdminV1Serialization }

func (*operatorAdminV1Reducer) String() string   { return operatorAdminV1String() }
func (*operatorAdminV1Reducer) GoString() string { return operatorAdminV1String() }
func (*operatorAdminV1Reducer) Format(state fmt.State, verb rune) {
	operatorAdminV1Format(state, verb)
}
func (*operatorAdminV1Reducer) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

func (PartnerHandleV1) String() string                    { return operatorAdminV1String() }
func (PartnerHandleV1) GoString() string                  { return operatorAdminV1String() }
func (PartnerHandleV1) Format(state fmt.State, verb rune) { operatorAdminV1Format(state, verb) }
func (PartnerHandleV1) MarshalJSON() ([]byte, error)      { return operatorAdminV1MarshalJSON() }
func (ObservationHandleV1) String() string                { return operatorAdminV1String() }
func (ObservationHandleV1) GoString() string              { return operatorAdminV1String() }
func (ObservationHandleV1) Format(state fmt.State, verb rune) {
	operatorAdminV1Format(state, verb)
}
func (ObservationHandleV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }
func (SelectionHandleV1) String() string                 { return operatorAdminV1String() }
func (SelectionHandleV1) GoString() string               { return operatorAdminV1String() }
func (SelectionHandleV1) Format(state fmt.State, verb rune) {
	operatorAdminV1Format(state, verb)
}
func (SelectionHandleV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }
func (CandidateHandleV1) String() string               { return operatorAdminV1String() }
func (CandidateHandleV1) GoString() string             { return operatorAdminV1String() }
func (CandidateHandleV1) Format(state fmt.State, verb rune) {
	operatorAdminV1Format(state, verb)
}
func (CandidateHandleV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

func (MutationPreconditionV1) String() string                    { return operatorAdminV1String() }
func (MutationPreconditionV1) GoString() string                  { return operatorAdminV1String() }
func (MutationPreconditionV1) Format(state fmt.State, verb rune) { operatorAdminV1Format(state, verb) }
func (MutationPreconditionV1) MarshalJSON() ([]byte, error)      { return operatorAdminV1MarshalJSON() }
func (AdminSnapshotRequestV1) String() string                    { return operatorAdminV1String() }
func (AdminSnapshotRequestV1) GoString() string                  { return operatorAdminV1String() }
func (AdminSnapshotRequestV1) Format(state fmt.State, verb rune) { operatorAdminV1Format(state, verb) }
func (AdminSnapshotRequestV1) MarshalJSON() ([]byte, error)      { return operatorAdminV1MarshalJSON() }
func (OpenPairingWindowRequestV1) String() string                { return operatorAdminV1String() }
func (OpenPairingWindowRequestV1) GoString() string              { return operatorAdminV1String() }
func (OpenPairingWindowRequestV1) Format(state fmt.State, verb rune) {
	operatorAdminV1Format(state, verb)
}
func (OpenPairingWindowRequestV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }
func (ClosePairingWindowRequestV1) String() string              { return operatorAdminV1String() }
func (ClosePairingWindowRequestV1) GoString() string            { return operatorAdminV1String() }
func (ClosePairingWindowRequestV1) Format(state fmt.State, verb rune) {
	operatorAdminV1Format(state, verb)
}
func (ClosePairingWindowRequestV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }
func (SelectRequestV1) String() string                           { return operatorAdminV1String() }
func (SelectRequestV1) GoString() string                         { return operatorAdminV1String() }
func (SelectRequestV1) Format(state fmt.State, verb rune)        { operatorAdminV1Format(state, verb) }
func (SelectRequestV1) MarshalJSON() ([]byte, error)             { return operatorAdminV1MarshalJSON() }
func (ConnectRequestV1) String() string                          { return operatorAdminV1String() }
func (ConnectRequestV1) GoString() string                        { return operatorAdminV1String() }
func (ConnectRequestV1) Format(state fmt.State, verb rune)       { operatorAdminV1Format(state, verb) }
func (ConnectRequestV1) MarshalJSON() ([]byte, error)            { return operatorAdminV1MarshalJSON() }
func (ConfirmRequestV1) String() string                          { return operatorAdminV1String() }
func (ConfirmRequestV1) GoString() string                        { return operatorAdminV1String() }
func (ConfirmRequestV1) Format(state fmt.State, verb rune)       { operatorAdminV1Format(state, verb) }
func (ConfirmRequestV1) MarshalJSON() ([]byte, error)            { return operatorAdminV1MarshalJSON() }
func (CancelRequestV1) String() string                           { return operatorAdminV1String() }
func (CancelRequestV1) GoString() string                         { return operatorAdminV1String() }
func (CancelRequestV1) Format(state fmt.State, verb rune)        { operatorAdminV1Format(state, verb) }
func (CancelRequestV1) MarshalJSON() ([]byte, error)             { return operatorAdminV1MarshalJSON() }
func (RetryTrustedRequestV1) String() string                     { return operatorAdminV1String() }
func (RetryTrustedRequestV1) GoString() string                   { return operatorAdminV1String() }
func (RetryTrustedRequestV1) Format(state fmt.State, verb rune)  { operatorAdminV1Format(state, verb) }
func (RetryTrustedRequestV1) MarshalJSON() ([]byte, error)       { return operatorAdminV1MarshalJSON() }
func (UntrustRequestV1) String() string                          { return operatorAdminV1String() }
func (UntrustRequestV1) GoString() string                        { return operatorAdminV1String() }
func (UntrustRequestV1) Format(state fmt.State, verb rune)       { operatorAdminV1Format(state, verb) }
func (UntrustRequestV1) MarshalJSON() ([]byte, error)            { return operatorAdminV1MarshalJSON() }

func (AdminMutationResultV1) String() string                    { return operatorAdminV1String() }
func (AdminMutationResultV1) GoString() string                  { return operatorAdminV1String() }
func (AdminMutationResultV1) Format(state fmt.State, verb rune) { operatorAdminV1Format(state, verb) }
func (AdminMutationResultV1) MarshalJSON() ([]byte, error)      { return operatorAdminV1MarshalJSON() }
func (AdminSelectionResultV1) String() string                   { return operatorAdminV1String() }
func (AdminSelectionResultV1) GoString() string                 { return operatorAdminV1String() }
func (AdminSelectionResultV1) Format(state fmt.State, verb rune) {
	operatorAdminV1Format(state, verb)
}
func (AdminSelectionResultV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }
func (AdminSnapshotV1) String() string                      { return operatorAdminV1String() }
func (AdminSnapshotV1) GoString() string                    { return operatorAdminV1String() }
func (AdminSnapshotV1) Format(state fmt.State, verb rune)   { operatorAdminV1Format(state, verb) }
func (AdminSnapshotV1) MarshalJSON() ([]byte, error)        { return operatorAdminV1MarshalJSON() }

func (TrustedPartnerV1) String() string                    { return operatorAdminV1String() }
func (TrustedPartnerV1) GoString() string                  { return operatorAdminV1String() }
func (TrustedPartnerV1) Format(state fmt.State, verb rune) { operatorAdminV1Format(state, verb) }
func (TrustedPartnerV1) MarshalJSON() ([]byte, error)      { return operatorAdminV1MarshalJSON() }
func (ConnectedPartnerV1) String() string                  { return operatorAdminV1String() }
func (ConnectedPartnerV1) GoString() string                { return operatorAdminV1String() }
func (ConnectedPartnerV1) Format(state fmt.State, verb rune) {
	operatorAdminV1Format(state, verb)
}
func (ConnectedPartnerV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }
func (DiscoveredPartnerV1) String() string              { return operatorAdminV1String() }
func (DiscoveredPartnerV1) GoString() string            { return operatorAdminV1String() }
func (DiscoveredPartnerV1) Format(state fmt.State, verb rune) {
	operatorAdminV1Format(state, verb)
}
func (DiscoveredPartnerV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }
func (CandidateV1) String() string                       { return operatorAdminV1String() }
func (CandidateV1) GoString() string                     { return operatorAdminV1String() }
func (CandidateV1) Format(state fmt.State, verb rune)    { operatorAdminV1Format(state, verb) }
func (CandidateV1) MarshalJSON() ([]byte, error)         { return operatorAdminV1MarshalJSON() }

func newAdminBoundaryUnavailableV1() *AdminErrorV1 {
	return &AdminErrorV1{Code: AdminErrorCodeV1AdminBoundaryUnavailable}
}

const (
	operatorAdminV1MaximumHandleTTL      = 2 * time.Minute
	operatorAdminV1MaximumHandlesPerKind = 128
	operatorAdminV1MaximumHandlesTotal   = 512
	operatorAdminV1MaximumReplayEntries  = 128
	operatorAdminV1MaximumReferenceBytes = 256
	operatorAdminV1MaximumEndpointBytes  = 512
	operatorAdminV1MaximumWindow         = 5 * time.Minute
	operatorAdminV1TokenAttempts         = 32
)

type operatorAdminV1Lifecycle interface {
	operatorAdminV1Lifecycle() (enabled, started, shutdown bool)
}

type operatorAdminV1Backend interface {
	snapshotOperatorAdminV1(context.Context) (operatorAdminV1SnapshotFacts, *AdminErrorV1)
	openOperatorAdminV1(context.Context, time.Duration) (operatorAdminV1Transition, *AdminErrorV1)
	closeOperatorAdminV1(context.Context) (operatorAdminV1Transition, *AdminErrorV1)
	selectOperatorAdminV1(context.Context, string, string) (string, operatorAdminV1Transition, *AdminErrorV1)
	connectOperatorAdminV1(context.Context, string) (operatorAdminV1Transition, *AdminErrorV1)
	confirmOperatorAdminV1(context.Context, string, string) (operatorAdminV1Transition, *AdminErrorV1)
	cancelOperatorAdminV1(context.Context, string) (operatorAdminV1Transition, *AdminErrorV1)
	retryTrustedOperatorAdminV1(context.Context, string) (operatorAdminV1Transition, *AdminErrorV1)
	untrustOperatorAdminV1(context.Context, string) (operatorAdminV1Transition, *AdminErrorV1)
}

type operatorAdminV1Transition struct {
	outcome AdminOutcomeV1
	changed bool
}

type operatorAdminV1SnapshotFacts struct {
	capturedAt     time.Time
	status         string
	window         string
	windowDeadline time.Time
	register       string
	listener       string
	discovery      string
	degraded       AdminErrorCodeV1
	trusted        []operatorAdminV1TrustedFact
	connected      []operatorAdminV1ConnectedFact
	discovered     []operatorAdminV1DiscoveredFact
	candidates     []operatorAdminV1CandidateFact
}

type operatorAdminV1TrustedFact struct {
	reference  string
	ski        string
	trustState string
	shipID     string
	lastSeen   time.Time
}

type operatorAdminV1ConnectedFact struct {
	ski             string
	endpoint        string
	trustState      string
	connectionState string
	shipID          string
	lastSeen        time.Time
}

type operatorAdminV1DiscoveredFact struct {
	reference           string
	ski                 string
	endpoint            string
	observationRevision uint64
	lastSeen            time.Time
	name                string
	identifier          string
	brand               string
	typeName            string
	model               string
	expiresAt           time.Time
}

type operatorAdminV1CandidateFact struct {
	reference           string
	ski                 string
	state               string
	expiresAt           time.Time
	associationComplete bool
}

type operatorAdminV1HandleKind uint8

const (
	operatorAdminV1PartnerHandle operatorAdminV1HandleKind = iota + 1
	operatorAdminV1ObservationHandle
	operatorAdminV1SelectionHandle
	operatorAdminV1CandidateHandle
)

type operatorAdminV1HandleRecord struct {
	kind      operatorAdminV1HandleKind
	target    string
	revision  uint64
	expiresAt time.Time
}

type operatorAdminV1Replay struct {
	binding   [32]byte
	result    AdminMutationResultV1
	selection SelectionHandleV1
	failure   AdminErrorCodeV1
	sequence  uint64
}

type operatorAdminV1Reducer struct {
	serial chan struct{}
	mu     sync.Mutex

	now       func() time.Time
	random    io.Reader
	lifecycle operatorAdminV1Lifecycle
	backend   operatorAdminV1Backend

	initialized    bool
	revision       uint64
	facts          operatorAdminV1SnapshotFacts
	handles        map[[32]byte]operatorAdminV1HandleRecord
	byTarget       map[operatorAdminV1HandleKind]map[string][32]byte
	replays        map[string]operatorAdminV1Replay
	replaySequence uint64
}

func newOperatorAdminV1Reducer(
	now func() time.Time,
	random io.Reader,
	lifecycle operatorAdminV1Lifecycle,
	backend operatorAdminV1Backend,
) *operatorAdminV1Reducer {
	if now == nil {
		now = time.Now
	}
	serial := make(chan struct{}, 1)
	serial <- struct{}{}
	return &operatorAdminV1Reducer{
		serial:    serial,
		now:       now,
		random:    random,
		lifecycle: lifecycle,
		backend:   backend,
		handles:   make(map[[32]byte]operatorAdminV1HandleRecord),
		byTarget:  make(map[operatorAdminV1HandleKind]map[string][32]byte),
		replays:   make(map[string]operatorAdminV1Replay),
	}
}

func (admin *operatorAdminV1Reducer) Snapshot(ctx context.Context, request AdminSnapshotRequestV1) (AdminSnapshotV1, *AdminErrorV1) {
	ctx = operatorAdminV1Context(ctx)
	if failure := admin.acquire(ctx); failure != nil {
		return AdminSnapshotV1{}, failure
	}
	defer admin.release()
	if failure := admin.available(); failure != nil {
		return AdminSnapshotV1{}, failure
	}
	if !validOperatorAdminV1View(request.View) {
		return AdminSnapshotV1{}, operatorAdminV1Error(AdminErrorCodeV1InvalidRequest)
	}
	facts, failure := admin.backend.snapshotOperatorAdminV1(ctx)
	if failure != nil {
		return AdminSnapshotV1{}, normalizeOperatorAdminV1Error(failure)
	}
	if failure := validateOperatorAdminV1Facts(facts); failure != nil {
		return AdminSnapshotV1{}, failure
	}
	if failure := admin.available(); failure != nil {
		return AdminSnapshotV1{}, failure
	}

	admin.mu.Lock()
	defer admin.mu.Unlock()
	if failure := admin.acceptFactsLocked(facts); failure != nil {
		return AdminSnapshotV1{}, failure
	}
	admin.pruneHandlesLocked(admin.now())
	return admin.snapshotLocked(request.View)
}

func (admin *operatorAdminV1Reducer) OpenPairingWindow(ctx context.Context, request OpenPairingWindowRequestV1) (AdminMutationResultV1, *AdminErrorV1) {
	var requestFailure *AdminErrorV1
	if request.Duration <= 0 || request.Duration > operatorAdminV1MaximumWindow {
		requestFailure = operatorAdminV1Error(AdminErrorCodeV1InvalidRequest)
	}
	binding := operatorAdminV1Binding("open", operatorAdminV1Uint64(uint64(request.Duration)))
	result, _, failure := admin.execute(
		ctx, request.MutationPreconditionV1, binding, requestFailure, nil,
		func(ctx context.Context, _ string) (string, operatorAdminV1Transition, *AdminErrorV1) {
			transition, failure := admin.backend.openOperatorAdminV1(ctx, request.Duration)
			return "", transition, failure
		},
		false,
	)
	return result, failure
}

func (admin *operatorAdminV1Reducer) ClosePairingWindow(ctx context.Context, request ClosePairingWindowRequestV1) (AdminMutationResultV1, *AdminErrorV1) {
	binding := operatorAdminV1Binding("close")
	result, _, failure := admin.execute(
		ctx, request.MutationPreconditionV1, binding, nil, nil,
		func(ctx context.Context, _ string) (string, operatorAdminV1Transition, *AdminErrorV1) {
			transition, failure := admin.backend.closeOperatorAdminV1(ctx)
			return "", transition, failure
		},
		false,
	)
	return result, failure
}

func (admin *operatorAdminV1Reducer) Select(ctx context.Context, request SelectRequestV1) (AdminSelectionResultV1, *AdminErrorV1) {
	var requestFailure *AdminErrorV1
	if !validOperatorAdminV1SKI(request.ExpectedSKI) {
		requestFailure = operatorAdminV1Error(AdminErrorCodeV1InvalidRequest)
	}
	binding := operatorAdminV1Binding("select", request.Observation.token[:], []byte(request.ExpectedSKI))
	result, selection, failure := admin.execute(
		ctx, request.MutationPreconditionV1, binding, requestFailure,
		func(now time.Time) (string, *AdminErrorV1) {
			return admin.resolveHandleLocked(operatorAdminV1ObservationHandle, request.Observation.token, now)
		},
		func(ctx context.Context, target string) (string, operatorAdminV1Transition, *AdminErrorV1) {
			return admin.backend.selectOperatorAdminV1(ctx, target, request.ExpectedSKI)
		},
		true,
	)
	if failure != nil {
		return AdminSelectionResultV1{}, failure
	}
	return AdminSelectionResultV1{AdminMutationResultV1: result, Selection: selection}, nil
}

func (admin *operatorAdminV1Reducer) Connect(ctx context.Context, request ConnectRequestV1) (AdminMutationResultV1, *AdminErrorV1) {
	binding := operatorAdminV1Binding("connect", request.Selection.token[:])
	result, _, failure := admin.execute(
		ctx, request.MutationPreconditionV1, binding, nil,
		func(now time.Time) (string, *AdminErrorV1) {
			return admin.resolveHandleLocked(operatorAdminV1SelectionHandle, request.Selection.token, now)
		},
		func(ctx context.Context, target string) (string, operatorAdminV1Transition, *AdminErrorV1) {
			transition, failure := admin.backend.connectOperatorAdminV1(ctx, target)
			return "", transition, failure
		},
		false,
	)
	return result, failure
}

func (admin *operatorAdminV1Reducer) Confirm(ctx context.Context, request ConfirmRequestV1) (AdminMutationResultV1, *AdminErrorV1) {
	var requestFailure *AdminErrorV1
	if !validOperatorAdminV1SKI(request.ExpectedSKI) {
		requestFailure = operatorAdminV1Error(AdminErrorCodeV1InvalidRequest)
	}
	binding := operatorAdminV1Binding("confirm", request.Candidate.token[:], []byte(request.ExpectedSKI))
	result, _, failure := admin.execute(
		ctx, request.MutationPreconditionV1, binding, requestFailure,
		func(now time.Time) (string, *AdminErrorV1) {
			return admin.resolveHandleLocked(operatorAdminV1CandidateHandle, request.Candidate.token, now)
		},
		func(ctx context.Context, target string) (string, operatorAdminV1Transition, *AdminErrorV1) {
			transition, failure := admin.backend.confirmOperatorAdminV1(ctx, target, request.ExpectedSKI)
			return "", transition, failure
		},
		false,
	)
	return result, failure
}

func (admin *operatorAdminV1Reducer) Cancel(ctx context.Context, request CancelRequestV1) (AdminMutationResultV1, *AdminErrorV1) {
	binding := operatorAdminV1Binding("cancel", request.Candidate.token[:])
	result, _, failure := admin.execute(
		ctx, request.MutationPreconditionV1, binding, nil,
		func(now time.Time) (string, *AdminErrorV1) {
			return admin.resolveHandleLocked(operatorAdminV1CandidateHandle, request.Candidate.token, now)
		},
		func(ctx context.Context, target string) (string, operatorAdminV1Transition, *AdminErrorV1) {
			transition, failure := admin.backend.cancelOperatorAdminV1(ctx, target)
			return "", transition, failure
		},
		false,
	)
	return result, failure
}

func (admin *operatorAdminV1Reducer) RetryTrusted(ctx context.Context, request RetryTrustedRequestV1) (AdminMutationResultV1, *AdminErrorV1) {
	binding := operatorAdminV1Binding("retry_trusted", request.Partner.token[:])
	result, _, failure := admin.execute(
		ctx, request.MutationPreconditionV1, binding, nil,
		func(now time.Time) (string, *AdminErrorV1) {
			return admin.resolveHandleLocked(operatorAdminV1PartnerHandle, request.Partner.token, now)
		},
		func(ctx context.Context, target string) (string, operatorAdminV1Transition, *AdminErrorV1) {
			transition, failure := admin.backend.retryTrustedOperatorAdminV1(ctx, target)
			return "", transition, failure
		},
		false,
	)
	return result, failure
}

func (admin *operatorAdminV1Reducer) Untrust(ctx context.Context, request UntrustRequestV1) (AdminMutationResultV1, *AdminErrorV1) {
	binding := operatorAdminV1Binding("untrust", request.Partner.token[:])
	result, _, failure := admin.execute(
		ctx, request.MutationPreconditionV1, binding, nil,
		func(now time.Time) (string, *AdminErrorV1) {
			return admin.resolveHandleLocked(operatorAdminV1PartnerHandle, request.Partner.token, now)
		},
		func(ctx context.Context, target string) (string, operatorAdminV1Transition, *AdminErrorV1) {
			transition, failure := admin.backend.untrustOperatorAdminV1(ctx, target)
			return "", transition, failure
		},
		false,
	)
	return result, failure
}

type operatorAdminV1Resolver func(time.Time) (string, *AdminErrorV1)
type operatorAdminV1Effect func(context.Context, string) (string, operatorAdminV1Transition, *AdminErrorV1)

func (admin *operatorAdminV1Reducer) execute(
	ctx context.Context,
	precondition MutationPreconditionV1,
	binding [32]byte,
	requestFailure *AdminErrorV1,
	resolve operatorAdminV1Resolver,
	effect operatorAdminV1Effect,
	selectionResult bool,
) (AdminMutationResultV1, SelectionHandleV1, *AdminErrorV1) {
	ctx = operatorAdminV1Context(ctx)
	if failure := admin.acquire(ctx); failure != nil {
		return AdminMutationResultV1{}, SelectionHandleV1{}, failure
	}
	defer admin.release()
	if failure := admin.available(); failure != nil {
		return AdminMutationResultV1{}, SelectionHandleV1{}, failure
	}
	if requestFailure != nil {
		return AdminMutationResultV1{}, SelectionHandleV1{}, requestFailure
	}
	if failure := validateOperatorAdminV1Precondition(precondition); failure != nil {
		return AdminMutationResultV1{}, SelectionHandleV1{}, failure
	}
	binding = operatorAdminV1Binding(
		"mutation",
		binding[:],
		operatorAdminV1Uint64(precondition.ExpectedStateRevision),
	)

	admin.mu.Lock()
	if replay, exists := admin.replays[precondition.IdempotencyKey]; exists {
		admin.mu.Unlock()
		if replay.binding != binding {
			return AdminMutationResultV1{}, SelectionHandleV1{}, operatorAdminV1Error(AdminErrorCodeV1IdempotencyConflict)
		}
		if replay.failure != "" {
			return AdminMutationResultV1{}, SelectionHandleV1{}, operatorAdminV1Error(replay.failure)
		}
		result := replay.result
		result.Replayed = true
		return result, replay.selection, nil
	}
	admin.mu.Unlock()

	facts, failure := admin.backend.snapshotOperatorAdminV1(ctx)
	if failure != nil {
		return AdminMutationResultV1{}, SelectionHandleV1{}, normalizeOperatorAdminV1Error(failure)
	}
	if failure := validateOperatorAdminV1Facts(facts); failure != nil {
		return AdminMutationResultV1{}, SelectionHandleV1{}, failure
	}
	if failure := admin.available(); failure != nil {
		return AdminMutationResultV1{}, SelectionHandleV1{}, failure
	}

	now := admin.now()
	admin.mu.Lock()
	if failure := admin.acceptFactsLocked(facts); failure != nil {
		admin.mu.Unlock()
		return AdminMutationResultV1{}, SelectionHandleV1{}, failure
	}
	if precondition.ExpectedStateRevision != admin.revision {
		admin.mu.Unlock()
		return AdminMutationResultV1{}, SelectionHandleV1{}, operatorAdminV1Error(AdminErrorCodeV1StateConflict)
	}
	admin.pruneHandlesLocked(now)
	target := ""
	if resolve != nil {
		var resolveFailure *AdminErrorV1
		target, resolveFailure = resolve(now)
		if resolveFailure != nil {
			admin.mu.Unlock()
			return AdminMutationResultV1{}, SelectionHandleV1{}, resolveFailure
		}
	}
	if selectionResult && !admin.canIssueHandleLocked(operatorAdminV1SelectionHandle, 1) {
		admin.mu.Unlock()
		return AdminMutationResultV1{}, SelectionHandleV1{}, newAdminBoundaryUnavailableV1()
	}
	if !admin.reserveReplaySlotLocked() {
		admin.mu.Unlock()
		return AdminMutationResultV1{}, SelectionHandleV1{}, newAdminBoundaryUnavailableV1()
	}
	admin.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return AdminMutationResultV1{}, SelectionHandleV1{}, operatorAdminV1Error(AdminErrorCodeV1UnknownState)
	}
	if failure := admin.available(); failure != nil {
		return AdminMutationResultV1{}, SelectionHandleV1{}, failure
	}

	selectionReference, transition, effectFailure := effect(ctx, target)
	effectFailure = normalizeOperatorAdminV1Error(effectFailure)
	postFacts, postFailure := admin.backend.snapshotOperatorAdminV1(ctx)
	postFactsValid := postFailure == nil && validateOperatorAdminV1Facts(postFacts) == nil

	admin.mu.Lock()
	changedFacts := postFactsValid && (!admin.initialized || !equalOperatorAdminV1Facts(admin.facts, postFacts))
	changed := transition.changed || changedFacts
	if changed {
		if failure := admin.advanceRevisionLocked(); failure != nil {
			effectFailure = failure
		}
	}
	if postFactsValid {
		admin.facts = cloneOperatorAdminV1Facts(postFacts)
		admin.initialized = true
	}
	if effectFailure == nil && transition.outcome == "" {
		effectFailure = operatorAdminV1Error(AdminErrorCodeV1UnknownState)
	}
	result := AdminMutationResultV1{}
	selection := SelectionHandleV1{}
	if effectFailure == nil {
		result = AdminMutationResultV1{StateRevision: admin.revision, Outcome: transition.outcome}
		if selectionResult {
			if selectionReference == "" {
				effectFailure = operatorAdminV1Error(AdminErrorCodeV1UnknownState)
			} else {
				tokens, handleFailure := admin.issueHandlesLocked(operatorAdminV1SelectionHandle, []string{selectionReference}, admin.now())
				if handleFailure != nil {
					effectFailure = handleFailure
				} else {
					selection = SelectionHandleV1{token: tokens[0]}
				}
			}
		}
	}
	admin.replaySequence++
	if admin.replaySequence == 0 {
		admin.replaySequence++
	}
	replay := operatorAdminV1Replay{binding: binding, result: result, selection: selection, sequence: admin.replaySequence}
	if effectFailure != nil {
		replay.failure = effectFailure.Code
	}
	admin.replays[precondition.IdempotencyKey] = replay
	admin.mu.Unlock()

	if effectFailure != nil {
		return AdminMutationResultV1{}, SelectionHandleV1{}, effectFailure
	}
	return result, selection, nil
}

func (admin *operatorAdminV1Reducer) snapshotLocked(view AdminViewV1) (AdminSnapshotV1, *AdminErrorV1) {
	snapshot := AdminSnapshotV1{
		StateRevision:   admin.revision,
		CapturedAt:      admin.facts.capturedAt,
		Status:          admin.facts.status,
		Window:          admin.facts.window,
		WindowDeadline:  admin.facts.windowDeadline,
		Register:        admin.facts.register,
		Listener:        admin.facts.listener,
		Discovery:       admin.facts.discovery,
		DegradedCode:    admin.facts.degraded,
		TrustedCount:    uint16(len(admin.facts.trusted)),
		ConnectedCount:  uint16(len(admin.facts.connected)),
		DiscoveredCount: uint16(len(admin.facts.discovered)),
		CandidateCount:  uint16(len(admin.facts.candidates)),
	}
	switch view {
	case AdminViewV1Trusted:
		targets := make([]string, len(admin.facts.trusted))
		for index, fact := range admin.facts.trusted {
			targets[index] = fact.reference
		}
		tokens, failure := admin.issueHandlesLocked(operatorAdminV1PartnerHandle, targets, admin.now())
		if failure != nil {
			return AdminSnapshotV1{}, failure
		}
		snapshot.Trusted = make([]TrustedPartnerV1, len(admin.facts.trusted))
		for index, fact := range admin.facts.trusted {
			snapshot.Trusted[index] = TrustedPartnerV1{
				Partner: PartnerHandleV1{token: tokens[index]}, SKI: fact.ski, TrustState: fact.trustState,
				SHIPID: fact.shipID, LastSeen: fact.lastSeen,
			}
		}
	case AdminViewV1Connected:
		snapshot.Connected = make([]ConnectedPartnerV1, len(admin.facts.connected))
		for index, fact := range admin.facts.connected {
			snapshot.Connected[index] = ConnectedPartnerV1{
				SKI: fact.ski, Endpoint: fact.endpoint, TrustState: fact.trustState,
				ConnectionState: fact.connectionState, SHIPID: fact.shipID, LastSeen: fact.lastSeen,
			}
		}
	case AdminViewV1Discovered:
		targets := make([]string, len(admin.facts.discovered))
		for index, fact := range admin.facts.discovered {
			targets[index] = fact.reference
		}
		tokens, failure := admin.issueHandlesLocked(operatorAdminV1ObservationHandle, targets, admin.now())
		if failure != nil {
			return AdminSnapshotV1{}, failure
		}
		snapshot.Discovered = make([]DiscoveredPartnerV1, len(admin.facts.discovered))
		for index, fact := range admin.facts.discovered {
			snapshot.Discovered[index] = DiscoveredPartnerV1{
				Observation: ObservationHandleV1{token: tokens[index]}, SKI: fact.ski, Endpoint: fact.endpoint,
				ObservationRevision: fact.observationRevision, LastSeen: fact.lastSeen, Name: fact.name,
				Identifier: fact.identifier, Brand: fact.brand, Type: fact.typeName, Model: fact.model,
			}
		}
	case AdminViewV1Candidate:
		targets := make([]string, len(admin.facts.candidates))
		for index, fact := range admin.facts.candidates {
			targets[index] = fact.reference
		}
		tokens, failure := admin.issueHandlesLocked(operatorAdminV1CandidateHandle, targets, admin.now())
		if failure != nil {
			return AdminSnapshotV1{}, failure
		}
		snapshot.Candidates = make([]CandidateV1, len(admin.facts.candidates))
		for index, fact := range admin.facts.candidates {
			snapshot.Candidates[index] = CandidateV1{
				Candidate: CandidateHandleV1{token: tokens[index]}, SKI: fact.ski, State: fact.state,
				ExpiresAt: fact.expiresAt, AssociationComplete: fact.associationComplete,
			}
		}
	default:
		return AdminSnapshotV1{}, operatorAdminV1Error(AdminErrorCodeV1InvalidRequest)
	}
	return snapshot, nil
}

func (admin *operatorAdminV1Reducer) acceptFactsLocked(facts operatorAdminV1SnapshotFacts) *AdminErrorV1 {
	if !admin.initialized {
		admin.facts = cloneOperatorAdminV1Facts(facts)
		admin.initialized = true
		admin.revision = 1
		admin.invalidateHandlesLocked()
		return nil
	}
	if equalOperatorAdminV1Facts(admin.facts, facts) {
		admin.facts = cloneOperatorAdminV1Facts(facts)
		return nil
	}
	if failure := admin.advanceRevisionLocked(); failure != nil {
		return failure
	}
	admin.facts = cloneOperatorAdminV1Facts(facts)
	return nil
}

func (admin *operatorAdminV1Reducer) reserveReplaySlotLocked() bool {
	if len(admin.replays) < operatorAdminV1MaximumReplayEntries {
		return true
	}
	oldestKey := ""
	oldestSequence := ^uint64(0)
	for key, replay := range admin.replays {
		if replay.failure != "" || replay.result.StateRevision == 0 || replay.result.StateRevision >= admin.revision ||
			replay.sequence >= oldestSequence {
			continue
		}
		oldestKey = key
		oldestSequence = replay.sequence
	}
	if oldestKey == "" {
		return false
	}
	delete(admin.replays, oldestKey)
	return true
}

func (admin *operatorAdminV1Reducer) advanceRevisionLocked() *AdminErrorV1 {
	if admin.revision == ^uint64(0) {
		return newAdminBoundaryUnavailableV1()
	}
	admin.revision++
	if admin.revision == 0 {
		return newAdminBoundaryUnavailableV1()
	}
	admin.invalidateHandlesLocked()
	return nil
}

func (admin *operatorAdminV1Reducer) invalidateHandlesLocked() {
	admin.handles = make(map[[32]byte]operatorAdminV1HandleRecord)
	admin.byTarget = make(map[operatorAdminV1HandleKind]map[string][32]byte)
}

func (admin *operatorAdminV1Reducer) issueHandlesLocked(kind operatorAdminV1HandleKind, targets []string, now time.Time) ([][32]byte, *AdminErrorV1) {
	admin.pruneHandlesLocked(now)
	result := make([][32]byte, len(targets))
	targetMap := admin.byTarget[kind]
	newTargets := make([]int, 0, len(targets))
	for index, target := range targets {
		if token, exists := targetMap[target]; exists {
			if record, valid := admin.handles[token]; valid && record.kind == kind && record.revision == admin.revision && now.Before(record.expiresAt) {
				result[index] = token
				continue
			}
		}
		newTargets = append(newTargets, index)
	}
	if !admin.canIssueHandleLocked(kind, len(newTargets)) {
		return nil, newAdminBoundaryUnavailableV1()
	}
	generated := make(map[[32]byte]struct{}, len(newTargets))
	for _, index := range newTargets {
		token, ok := admin.generateTokenLocked(generated)
		if !ok {
			return nil, newAdminBoundaryUnavailableV1()
		}
		generated[token] = struct{}{}
		result[index] = token
	}
	if targetMap == nil {
		targetMap = make(map[string][32]byte)
		admin.byTarget[kind] = targetMap
	}
	for _, index := range newTargets {
		token := result[index]
		target := targets[index]
		expiresAt := admin.handleExpiryLocked(kind, target, now)
		if !now.Before(expiresAt) {
			return nil, newAdminBoundaryUnavailableV1()
		}
		admin.handles[token] = operatorAdminV1HandleRecord{
			kind: kind, target: target, revision: admin.revision, expiresAt: expiresAt,
		}
		targetMap[target] = token
	}
	return result, nil
}

func (admin *operatorAdminV1Reducer) handleExpiryLocked(kind operatorAdminV1HandleKind, target string, now time.Time) time.Time {
	expiresAt := now.Add(operatorAdminV1MaximumHandleTTL)
	if kind != operatorAdminV1PartnerHandle && admin.facts.window == "open" &&
		!admin.facts.windowDeadline.IsZero() && admin.facts.windowDeadline.Before(expiresAt) {
		expiresAt = admin.facts.windowDeadline
	}
	switch kind {
	case operatorAdminV1ObservationHandle:
		for _, fact := range admin.facts.discovered {
			if fact.reference == target && !fact.expiresAt.IsZero() && fact.expiresAt.Before(expiresAt) {
				expiresAt = fact.expiresAt
				break
			}
		}
	case operatorAdminV1CandidateHandle:
		for _, fact := range admin.facts.candidates {
			if fact.reference == target && fact.expiresAt.Before(expiresAt) {
				expiresAt = fact.expiresAt
				break
			}
		}
	}
	return expiresAt
}

func (admin *operatorAdminV1Reducer) generateTokenLocked(pending map[[32]byte]struct{}) ([32]byte, bool) {
	for attempt := 0; attempt < operatorAdminV1TokenAttempts; attempt++ {
		var token [32]byte
		if admin.random == nil {
			return [32]byte{}, false
		}
		if _, err := io.ReadFull(admin.random, token[:]); err != nil || token == [32]byte{} {
			continue
		}
		if _, collision := admin.handles[token]; collision {
			continue
		}
		if _, collision := pending[token]; collision {
			continue
		}
		return token, true
	}
	return [32]byte{}, false
}

func (admin *operatorAdminV1Reducer) canIssueHandleLocked(kind operatorAdminV1HandleKind, additional int) bool {
	if additional < 0 || len(admin.handles)+additional > operatorAdminV1MaximumHandlesTotal {
		return false
	}
	count := 0
	for _, record := range admin.handles {
		if record.kind == kind {
			count++
		}
	}
	return count+additional <= operatorAdminV1MaximumHandlesPerKind
}

func (admin *operatorAdminV1Reducer) pruneHandlesLocked(now time.Time) {
	for token, record := range admin.handles {
		if record.revision == admin.revision && now.Before(record.expiresAt) {
			continue
		}
		delete(admin.handles, token)
		if targets := admin.byTarget[record.kind]; targets != nil && targets[record.target] == token {
			delete(targets, record.target)
		}
	}
}

func (admin *operatorAdminV1Reducer) resolveHandleLocked(kind operatorAdminV1HandleKind, token [32]byte, now time.Time) (string, *AdminErrorV1) {
	if token == [32]byte{} {
		return "", operatorAdminV1HandleError(kind)
	}
	record, exists := admin.handles[token]
	if !exists || record.kind != kind || record.revision != admin.revision || !now.Before(record.expiresAt) {
		return "", operatorAdminV1HandleError(kind)
	}
	return record.target, nil
}

func operatorAdminV1HandleError(kind operatorAdminV1HandleKind) *AdminErrorV1 {
	switch kind {
	case operatorAdminV1ObservationHandle, operatorAdminV1SelectionHandle:
		return operatorAdminV1Error(AdminErrorCodeV1ObservationStale)
	case operatorAdminV1CandidateHandle:
		return operatorAdminV1Error(AdminErrorCodeV1CandidateExpired)
	case operatorAdminV1PartnerHandle:
		return operatorAdminV1Error(AdminErrorCodeV1SnapshotExpired)
	default:
		return operatorAdminV1Error(AdminErrorCodeV1UnknownState)
	}
}

func (admin *operatorAdminV1Reducer) acquire(ctx context.Context) *AdminErrorV1 {
	if admin == nil || admin.serial == nil {
		return newAdminBoundaryUnavailableV1()
	}
	select {
	case <-admin.serial:
		return nil
	case <-ctx.Done():
		return operatorAdminV1Error(AdminErrorCodeV1UnknownState)
	}
}

func (admin *operatorAdminV1Reducer) release() {
	admin.serial <- struct{}{}
}

func (admin *operatorAdminV1Reducer) available() *AdminErrorV1 {
	if admin == nil || admin.lifecycle == nil || admin.backend == nil {
		return newAdminBoundaryUnavailableV1()
	}
	enabled, started, shutdown := admin.lifecycle.operatorAdminV1Lifecycle()
	if !enabled || !started || shutdown {
		return newAdminBoundaryUnavailableV1()
	}
	return nil
}

func validateOperatorAdminV1Precondition(precondition MutationPreconditionV1) *AdminErrorV1 {
	if len(precondition.IdempotencyKey) < 1 || len(precondition.IdempotencyKey) > 128 ||
		!utf8.ValidString(precondition.IdempotencyKey) || precondition.ExpectedStateRevision == 0 {
		return operatorAdminV1Error(AdminErrorCodeV1InvalidRequest)
	}
	return nil
}

func validateOperatorAdminV1Facts(facts operatorAdminV1SnapshotFacts) *AdminErrorV1 {
	if facts.capturedAt.IsZero() || !validOperatorAdminV1Status(facts.status) ||
		!validOperatorAdminV1Status(facts.window) || !validOperatorAdminV1Status(facts.register) ||
		!validOperatorAdminV1Status(facts.listener) || !validOperatorAdminV1Status(facts.discovery) ||
		facts.window == "open" && facts.windowDeadline.IsZero() || !validOperatorAdminV1DegradedCode(facts.degraded) {
		return newAdminBoundaryUnavailableV1()
	}
	if len(facts.trusted) > operatorAdminV1MaximumHandlesPerKind ||
		len(facts.connected) > operatorAdminV1MaximumHandlesPerKind ||
		len(facts.discovered) > operatorAdminV1MaximumHandlesPerKind ||
		len(facts.candidates) > operatorAdminV1MaximumHandlesPerKind {
		return newAdminBoundaryUnavailableV1()
	}
	seenTrusted := make(map[string]struct{}, len(facts.trusted))
	for _, fact := range facts.trusted {
		if !validOperatorAdminV1Reference(fact.reference) || !validOperatorAdminV1SKI(fact.ski) ||
			!validOperatorAdminV1Status(fact.trustState) || !validOperatorAdminV1OptionalText(fact.shipID) {
			return newAdminBoundaryUnavailableV1()
		}
		if _, duplicate := seenTrusted[fact.reference]; duplicate {
			return newAdminBoundaryUnavailableV1()
		}
		seenTrusted[fact.reference] = struct{}{}
	}
	for _, fact := range facts.connected {
		if !validOperatorAdminV1SKI(fact.ski) || !validOperatorAdminV1Endpoint(fact.endpoint) ||
			!validOperatorAdminV1Status(fact.connectionState) || !validOperatorAdminV1Status(fact.trustState) ||
			!validOperatorAdminV1OptionalText(fact.shipID) {
			return newAdminBoundaryUnavailableV1()
		}
	}
	seenDiscovered := make(map[string]struct{}, len(facts.discovered))
	for _, fact := range facts.discovered {
		if !validOperatorAdminV1Reference(fact.reference) || fact.ski != "" && !validOperatorAdminV1SKI(fact.ski) ||
			!validOperatorAdminV1Endpoint(fact.endpoint) || fact.observationRevision == 0 ||
			!validOperatorAdminV1OptionalText(fact.name) || !validOperatorAdminV1OptionalText(fact.identifier) ||
			!validOperatorAdminV1OptionalText(fact.brand) || !validOperatorAdminV1OptionalText(fact.typeName) ||
			!validOperatorAdminV1OptionalText(fact.model) || !fact.expiresAt.IsZero() && !facts.capturedAt.Before(fact.expiresAt) {
			return newAdminBoundaryUnavailableV1()
		}
		if _, duplicate := seenDiscovered[fact.reference]; duplicate {
			return newAdminBoundaryUnavailableV1()
		}
		seenDiscovered[fact.reference] = struct{}{}
	}
	seenCandidates := make(map[string]struct{}, len(facts.candidates))
	for _, fact := range facts.candidates {
		if !validOperatorAdminV1Reference(fact.reference) || !validOperatorAdminV1SKI(fact.ski) ||
			!validOperatorAdminV1Status(fact.state) || fact.expiresAt.IsZero() || !facts.capturedAt.Before(fact.expiresAt) {
			return newAdminBoundaryUnavailableV1()
		}
		if _, duplicate := seenCandidates[fact.reference]; duplicate {
			return newAdminBoundaryUnavailableV1()
		}
		seenCandidates[fact.reference] = struct{}{}
	}
	return nil
}

func validOperatorAdminV1Status(value string) bool {
	return len(value) >= 1 && len(value) <= 128 && utf8.ValidString(value)
}

func validOperatorAdminV1OptionalText(value string) bool {
	return len(value) <= 256 && utf8.ValidString(value)
}

func validOperatorAdminV1DegradedCode(value AdminErrorCodeV1) bool {
	if value == "" {
		return true
	}
	return normalizeOperatorAdminV1Error(operatorAdminV1Error(value)).Code == value
}

func validOperatorAdminV1Reference(value string) bool {
	return len(value) >= 1 && len(value) <= operatorAdminV1MaximumReferenceBytes && utf8.ValidString(value)
}

func validOperatorAdminV1Endpoint(value string) bool {
	return len(value) <= operatorAdminV1MaximumEndpointBytes && utf8.ValidString(value)
}

func validOperatorAdminV1SKI(value string) bool {
	if len(value) != 40 {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			if value[index] < 'a' || value[index] > 'f' {
				return false
			}
		}
	}
	return true
}

func validOperatorAdminV1View(view AdminViewV1) bool {
	switch view {
	case AdminViewV1Trusted, AdminViewV1Connected, AdminViewV1Discovered, AdminViewV1Candidate:
		return true
	default:
		return false
	}
}

func cloneOperatorAdminV1Facts(source operatorAdminV1SnapshotFacts) operatorAdminV1SnapshotFacts {
	result := source
	result.trusted = append([]operatorAdminV1TrustedFact(nil), source.trusted...)
	result.connected = append([]operatorAdminV1ConnectedFact(nil), source.connected...)
	result.discovered = append([]operatorAdminV1DiscoveredFact(nil), source.discovered...)
	result.candidates = append([]operatorAdminV1CandidateFact(nil), source.candidates...)
	return result
}

func equalOperatorAdminV1Facts(left, right operatorAdminV1SnapshotFacts) bool {
	if left.status != right.status || left.window != right.window || !left.windowDeadline.Equal(right.windowDeadline) ||
		left.register != right.register || left.listener != right.listener || left.discovery != right.discovery ||
		left.degraded != right.degraded {
		return false
	}
	if len(left.trusted) != len(right.trusted) || len(left.connected) != len(right.connected) ||
		len(left.discovered) != len(right.discovered) || len(left.candidates) != len(right.candidates) {
		return false
	}
	for index := range left.trusted {
		if left.trusted[index] != right.trusted[index] {
			return false
		}
	}
	for index := range left.connected {
		if left.connected[index] != right.connected[index] {
			return false
		}
	}
	for index := range left.discovered {
		if left.discovered[index] != right.discovered[index] {
			return false
		}
	}
	for index := range left.candidates {
		if left.candidates[index] != right.candidates[index] {
			return false
		}
	}
	return true
}

func operatorAdminV1Binding(operation string, parts ...[]byte) [32]byte {
	hash := sha256.New()
	operatorAdminV1WriteBindingPart(hash, []byte(operation))
	for _, part := range parts {
		operatorAdminV1WriteBindingPart(hash, part)
	}
	var result [32]byte
	copy(result[:], hash.Sum(nil))
	return result
}

func operatorAdminV1WriteBindingPart(writer io.Writer, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = writer.Write(length[:])
	_, _ = writer.Write(value)
}

func operatorAdminV1Uint64(value uint64) []byte {
	var result [8]byte
	binary.BigEndian.PutUint64(result[:], value)
	return result[:]
}

func operatorAdminV1Context(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func operatorAdminV1Error(code AdminErrorCodeV1) *AdminErrorV1 {
	return &AdminErrorV1{Code: code}
}

func normalizeOperatorAdminV1Error(failure *AdminErrorV1) *AdminErrorV1 {
	if failure == nil {
		return nil
	}
	switch failure.Code {
	case AdminErrorCodeV1AdminBoundaryUnavailable,
		AdminErrorCodeV1Unauthenticated,
		AdminErrorCodeV1Forbidden,
		AdminErrorCodeV1CSRFRejected,
		AdminErrorCodeV1InvalidRequest,
		AdminErrorCodeV1StateConflict,
		AdminErrorCodeV1SnapshotExpired,
		AdminErrorCodeV1IdempotencyConflict,
		AdminErrorCodeV1PairingClosed,
		AdminErrorCodeV1ObservationStale,
		AdminErrorCodeV1IdentityMismatch,
		AdminErrorCodeV1AssociationIncomplete,
		AdminErrorCodeV1CandidateExpired,
		AdminErrorCodeV1CandidateBusy,
		AdminErrorCodeV1TrustDenied,
		AdminErrorCodeV1ListenerUnavailable,
		AdminErrorCodeV1DiscoveryUnavailable,
		AdminErrorCodeV1AttemptTimeout,
		AdminErrorCodeV1Disconnected,
		AdminErrorCodeV1BackoffActive,
		AdminErrorCodeV1TerminalQuarantine,
		AdminErrorCodeV1PersistenceFailure,
		AdminErrorCodeV1UnknownState:
		return operatorAdminV1Error(failure.Code)
	default:
		return operatorAdminV1Error(AdminErrorCodeV1UnknownState)
	}
}

type operatorAdminV1BackendSlot struct {
	mu      sync.RWMutex
	backend operatorAdminV1Backend
}

func (slot *operatorAdminV1BackendSlot) attach(backend operatorAdminV1Backend) {
	slot.mu.Lock()
	slot.backend = backend
	slot.mu.Unlock()
}

func (slot *operatorAdminV1BackendSlot) detach(backend operatorAdminV1Backend) {
	slot.mu.Lock()
	if slot.backend == backend {
		slot.backend = nil
	}
	slot.mu.Unlock()
}

func (slot *operatorAdminV1BackendSlot) current() operatorAdminV1Backend {
	if slot == nil {
		return nil
	}
	slot.mu.RLock()
	defer slot.mu.RUnlock()
	return slot.backend
}

func (slot *operatorAdminV1BackendSlot) snapshotOperatorAdminV1(ctx context.Context) (operatorAdminV1SnapshotFacts, *AdminErrorV1) {
	if backend := slot.current(); backend != nil {
		return backend.snapshotOperatorAdminV1(ctx)
	}
	return operatorAdminV1SnapshotFacts{}, newAdminBoundaryUnavailableV1()
}

func (slot *operatorAdminV1BackendSlot) openOperatorAdminV1(ctx context.Context, duration time.Duration) (operatorAdminV1Transition, *AdminErrorV1) {
	if backend := slot.current(); backend != nil {
		return backend.openOperatorAdminV1(ctx, duration)
	}
	return operatorAdminV1Transition{}, newAdminBoundaryUnavailableV1()
}

func (slot *operatorAdminV1BackendSlot) closeOperatorAdminV1(ctx context.Context) (operatorAdminV1Transition, *AdminErrorV1) {
	if backend := slot.current(); backend != nil {
		return backend.closeOperatorAdminV1(ctx)
	}
	return operatorAdminV1Transition{}, newAdminBoundaryUnavailableV1()
}

func (slot *operatorAdminV1BackendSlot) selectOperatorAdminV1(ctx context.Context, reference, ski string) (string, operatorAdminV1Transition, *AdminErrorV1) {
	if backend := slot.current(); backend != nil {
		return backend.selectOperatorAdminV1(ctx, reference, ski)
	}
	return "", operatorAdminV1Transition{}, newAdminBoundaryUnavailableV1()
}

func (slot *operatorAdminV1BackendSlot) connectOperatorAdminV1(ctx context.Context, reference string) (operatorAdminV1Transition, *AdminErrorV1) {
	if backend := slot.current(); backend != nil {
		return backend.connectOperatorAdminV1(ctx, reference)
	}
	return operatorAdminV1Transition{}, newAdminBoundaryUnavailableV1()
}

func (slot *operatorAdminV1BackendSlot) confirmOperatorAdminV1(ctx context.Context, reference, ski string) (operatorAdminV1Transition, *AdminErrorV1) {
	if backend := slot.current(); backend != nil {
		return backend.confirmOperatorAdminV1(ctx, reference, ski)
	}
	return operatorAdminV1Transition{}, newAdminBoundaryUnavailableV1()
}

func (slot *operatorAdminV1BackendSlot) cancelOperatorAdminV1(ctx context.Context, reference string) (operatorAdminV1Transition, *AdminErrorV1) {
	if backend := slot.current(); backend != nil {
		return backend.cancelOperatorAdminV1(ctx, reference)
	}
	return operatorAdminV1Transition{}, newAdminBoundaryUnavailableV1()
}

func (slot *operatorAdminV1BackendSlot) retryTrustedOperatorAdminV1(ctx context.Context, reference string) (operatorAdminV1Transition, *AdminErrorV1) {
	if backend := slot.current(); backend != nil {
		return backend.retryTrustedOperatorAdminV1(ctx, reference)
	}
	return operatorAdminV1Transition{}, newAdminBoundaryUnavailableV1()
}

func (slot *operatorAdminV1BackendSlot) untrustOperatorAdminV1(ctx context.Context, reference string) (operatorAdminV1Transition, *AdminErrorV1) {
	if backend := slot.current(); backend != nil {
		return backend.untrustOperatorAdminV1(ctx, reference)
	}
	return operatorAdminV1Transition{}, newAdminBoundaryUnavailableV1()
}
