package eebusruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// AdminSnapshotRequestV1 names the single partner list a snapshot fills and issues handles
// for; the other lists stay nil and only their counts are set. An unknown View fails with
// AdminErrorCodeV1InvalidRequest.
type AdminSnapshotRequestV1 struct {
	View AdminViewV1
}

// OpenPairingWindowRequestV1 requests a pairing window; Duration must be positive and at most
// five minutes, otherwise the request fails with AdminErrorCodeV1InvalidRequest.
type OpenPairingWindowRequestV1 struct {
	MutationPreconditionV1
	Duration time.Duration
}

// ClosePairingWindowRequestV1 closes the pairing window and carries only the mutation
// precondition.
type ClosePairingWindowRequestV1 struct {
	MutationPreconditionV1
}

// SelectRequestV1 selects one discovered partner. Observation must come from the discovered
// view of the current revision and be unexpired, otherwise the request fails with
// AdminErrorCodeV1ObservationStale. ExpectedSKI is exactly 40 lowercase hexadecimal
// characters and is handed to the backend for comparison with the observed identity.
type SelectRequestV1 struct {
	MutationPreconditionV1
	Observation ObservationHandleV1
	ExpectedSKI string
}

// ConnectRequestV1 connects to a selection from a Select result of the current revision. A nil
// PIN connects without one; otherwise PIN is 8 to 16 hexadecimal characters. The gateway
// copies the PIN into a buffer it zeroes after use or failure, so the caller keeps its slice,
// and idempotent replay compares the PIN only through a process-local keyed digest.
type ConnectRequestV1 struct {
	MutationPreconditionV1
	Selection SelectionHandleV1
	PIN       []byte
}

// ConfirmRequestV1 accepts one inbound candidate. Candidate must come from the candidate view
// of the current revision and be unexpired, otherwise the request fails with
// AdminErrorCodeV1CandidateExpired. ExpectedSKI is exactly 40 lowercase hexadecimal characters.
type ConfirmRequestV1 struct {
	MutationPreconditionV1
	Candidate   CandidateHandleV1
	ExpectedSKI string
}

// CancelRequestV1 rejects one inbound candidate; a stale or expired Candidate fails with
// AdminErrorCodeV1CandidateExpired.
type CancelRequestV1 struct {
	MutationPreconditionV1
	Candidate CandidateHandleV1
}

// RetryTrustedRequestV1 retries one trusted partner. Partner must come from the trusted view of
// the current revision, otherwise the request fails with AdminErrorCodeV1SnapshotExpired.
type RetryTrustedRequestV1 struct {
	MutationPreconditionV1
	Partner PartnerHandleV1
}

// UntrustRequestV1 withdraws trust from one partner. Partner must come from the trusted view of
// the current revision, otherwise the request fails with AdminErrorCodeV1SnapshotExpired.
type UntrustRequestV1 struct {
	MutationPreconditionV1
	Partner PartnerHandleV1
}

// AdminMutationResultV1 reports a successful mutation. StateRevision is the revision after the
// effect, advanced when the backend reported a change or the re-read facts differ; Replayed is
// set when the result comes from an earlier call with the same idempotency key.
type AdminMutationResultV1 struct {
	StateRevision uint64
	Outcome       AdminOutcomeV1
	Replayed      bool
}

// ConnectResultV1 adds the action ID of the started connection: 64 lowercase hexadecimal
// characters, matched by AdminSnapshotV1.ActiveAction.ActionID.
type ConnectResultV1 struct {
	AdminMutationResultV1
	ActionID string
}

// AdminSelectionResultV1 adds the selection handle that ConnectRequestV1 consumes; the handle
// expires with the revision, after two minutes or at the deadline of an open pairing window,
// whichever is first.
type AdminSelectionResultV1 struct {
	AdminMutationResultV1
	Selection SelectionHandleV1
}

// AdminOutcomeV1 is the backend-reported outcome of a mutation or connection attempt. The
// gateway forwards it unchanged except for terminal connection actions, whose outcome must be
// one of a closed set or the whole fact set is refused.
type AdminOutcomeV1 string

// AdminViewV1 selects the partner list an AdminSnapshotV1 fills and issues handles for.
type AdminViewV1 string

// Snapshot views; exactly one list is filled per snapshot.
const (
	// AdminViewV1Trusted fills Trusted and issues partner handles.
	AdminViewV1Trusted AdminViewV1 = "trusted"
	// AdminViewV1Connected fills Connected; it issues no handles.
	AdminViewV1Connected AdminViewV1 = "connected"
	// AdminViewV1Discovered fills Discovered and issues observation handles.
	AdminViewV1Discovered AdminViewV1 = "discovered"
	// AdminViewV1Candidate fills Candidates and issues candidate handles.
	AdminViewV1Candidate AdminViewV1 = "candidate"
)

// AdminErrorCodeV1 is the closed failure vocabulary of AdminV1. A code the backend reports
// outside this set is replaced by AdminErrorCodeV1UnknownState.
type AdminErrorCodeV1 string

// Failure codes. The reducer raises the commented codes itself, and the backend may report
// them too; the other codes reach the caller only when the backend reports them.
const (
	// AdminErrorCodeV1AdminBoundaryUnavailable fails closed: the runtime is not serving,
	// backend facts are malformed, entropy is unavailable, or a handle, replay or revision
	// limit is reached.
	AdminErrorCodeV1AdminBoundaryUnavailable AdminErrorCodeV1 = "admin_boundary_unavailable"
	AdminErrorCodeV1Unauthenticated          AdminErrorCodeV1 = "unauthenticated"
	AdminErrorCodeV1Forbidden                AdminErrorCodeV1 = "forbidden"
	AdminErrorCodeV1CSRFRejected             AdminErrorCodeV1 = "csrf_rejected"
	// AdminErrorCodeV1InvalidRequest rejects a malformed request before any effect.
	AdminErrorCodeV1InvalidRequest AdminErrorCodeV1 = "invalid_request"
	// AdminErrorCodeV1StateConflict rejects a stale ExpectedStateRevision.
	AdminErrorCodeV1StateConflict AdminErrorCodeV1 = "state_conflict"
	// AdminErrorCodeV1SnapshotExpired rejects a stale or expired partner handle.
	AdminErrorCodeV1SnapshotExpired AdminErrorCodeV1 = "snapshot_expired"
	// AdminErrorCodeV1IdempotencyConflict rejects a reused key bound to another request.
	AdminErrorCodeV1IdempotencyConflict AdminErrorCodeV1 = "idempotency_conflict"
	AdminErrorCodeV1PairingClosed       AdminErrorCodeV1 = "pairing_closed"
	// AdminErrorCodeV1ObservationStale rejects a stale observation or selection handle.
	AdminErrorCodeV1ObservationStale      AdminErrorCodeV1 = "observation_stale"
	AdminErrorCodeV1IdentityMismatch      AdminErrorCodeV1 = "identity_mismatch"
	AdminErrorCodeV1AssociationIncomplete AdminErrorCodeV1 = "association_incomplete"
	// AdminErrorCodeV1CandidateExpired rejects a stale or expired candidate handle.
	AdminErrorCodeV1CandidateExpired     AdminErrorCodeV1 = "candidate_expired"
	AdminErrorCodeV1CandidateBusy        AdminErrorCodeV1 = "candidate_busy"
	AdminErrorCodeV1TrustDenied          AdminErrorCodeV1 = "trust_denied"
	AdminErrorCodeV1ListenerUnavailable  AdminErrorCodeV1 = "listener_unavailable"
	AdminErrorCodeV1DiscoveryUnavailable AdminErrorCodeV1 = "discovery_unavailable"
	AdminErrorCodeV1AttemptTimeout       AdminErrorCodeV1 = "attempt_timeout"
	AdminErrorCodeV1Disconnected         AdminErrorCodeV1 = "disconnected"
	AdminErrorCodeV1BackoffActive        AdminErrorCodeV1 = "backoff_active"
	AdminErrorCodeV1TerminalQuarantine   AdminErrorCodeV1 = "terminal_quarantine"
	AdminErrorCodeV1PersistenceFailure   AdminErrorCodeV1 = "persistence_failure"
	AdminErrorCodeV1PINRequired          AdminErrorCodeV1 = "pin_required"
	AdminErrorCodeV1PINOptional          AdminErrorCodeV1 = "pin_optional"
	AdminErrorCodeV1PINBusy              AdminErrorCodeV1 = "pin_busy"
	AdminErrorCodeV1PINRejected          AdminErrorCodeV1 = "pin_rejected"
	AdminErrorCodeV1PINUnavailable       AdminErrorCodeV1 = "pin_unavailable"
	AdminErrorCodeV1PINProtocolError     AdminErrorCodeV1 = "pin_protocol_error"
	// AdminErrorCodeV1UnknownState reports that the outcome cannot be known: the context
	// ended, the backend reported no outcome, or it reported a code outside this set.
	AdminErrorCodeV1UnknownState AdminErrorCodeV1 = "unknown_state"
)

// AdminErrorV1 is the only failure AdminV1 returns. It carries a code and never a partner
// identity, address or backend detail.
type AdminErrorV1 struct {
	Code AdminErrorCodeV1
}

// Error returns the code, or unknown_state for an empty code.
func (failure AdminErrorV1) Error() string {
	if failure.Code == "" {
		return string(AdminErrorCodeV1UnknownState)
	}
	return string(failure.Code)
}

// PartnerHandleV1 is an opaque, random reference to a trusted partner, issued by the trusted
// view. Every handle kind is valid only within the revision that issued it and for at most two
// minutes; the zero value never resolves. Handles render redacted and do not serialize.
type PartnerHandleV1 struct{ token [32]byte }

// ObservationHandleV1 is an opaque reference to a discovered partner; it also ends at the
// deadline of an open pairing window or when the observation expires.
type ObservationHandleV1 struct{ token [32]byte }

// SelectionHandleV1 is an opaque reference returned by Select; it also ends at the deadline of
// an open pairing window.
type SelectionHandleV1 struct{ token [32]byte }

// CandidateHandleV1 is an opaque reference to an inbound candidate; it also ends at the
// deadline of an open pairing window or when the candidate expires.
type CandidateHandleV1 struct{ token [32]byte }

// AdminSnapshotV1 is the redacted operator view at StateRevision. Status, window, register,
// listener and discovery states are backend vocabulary of 1 to 128 bytes; WindowDeadline is set
// while the window is open. Counts cover every list, but only the requested view's list is
// filled. DegradedCode is empty or a known AdminErrorCodeV1.
type AdminSnapshotV1 struct {
	StateRevision   uint64
	CapturedAt      time.Time
	LocalSKI        string
	LocalSHIPID     string
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
	ActiveAction    *ActiveActionV1
}

// ActiveActionV1 tracks the current connect action. State is pending, with no Outcome, or
// terminal, with an Outcome; Kind is always connect and Expiry is after the capture time. A
// snapshot that returns a terminal action reports it to the backend as observed.
type ActiveActionV1 struct {
	ActionID  string
	Kind      string
	State     string
	Outcome   *AdminOutcomeV1
	Retryable bool
	Expiry    time.Time
}

// TrustedPartnerV1 is one trusted partner. SKI is 40 lowercase hexadecimal characters;
// descriptive text is at most 256 bytes and may be empty. RetryState is empty, RETRY_READY,
// BACKOFF_ACTIVE with a RetryDeadline not before the capture time, or ADMIN_HOLD; only
// RETRY_READY may carry RetryAdmitted.
type TrustedPartnerV1 struct {
	Partner         PartnerHandleV1
	SKI             string
	Endpoint        string
	TrustState      string
	ConnectionState string
	SHIPID          string
	LastSeen        time.Time
	Name            string
	Identifier      string
	Brand           string
	Type            string
	Model           string
	RetryState      string
	RetryDeadline   time.Time
	RetryAdmitted   bool
}

// ConnectedPartnerV1 is one connected partner. It has no handle because no mutation targets a
// connection directly.
type ConnectedPartnerV1 struct {
	SKI             string
	Endpoint        string
	TrustState      string
	ConnectionState string
	SHIPID          string
	LastSeen        time.Time
	Name            string
	Identifier      string
	Brand           string
	Type            string
	Model           string
}

// DiscoveredPartnerV1 is one discovered partner. SKI is empty when the backend has not learned
// it; ObservationRevision is the nonzero backend revision of this observation.
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

// CandidateV1 is one inbound pairing candidate awaiting Confirm or Cancel. ExpiresAt is after
// the capture time; AssociationComplete reports whether the backend finished the association.
type CandidateV1 struct {
	Candidate           CandidateHandleV1
	SKI                 string
	State               string
	ExpiresAt           time.Time
	AssociationComplete bool
}

type operatorAdminV1TransientPIN struct{ bytes []byte }

func newOperatorAdminV1TransientPIN(value []byte) (operatorAdminV1TransientPIN, bool) {
	if !validOperatorAdminV1PIN(value) {
		return operatorAdminV1TransientPIN{}, false
	}
	return operatorAdminV1TransientPIN{bytes: append([]byte(nil), value...)}, true
}

// WithTransientPIN passes the PIN bytes to consume exactly once and zeroes them afterwards,
// whatever consume returns; a cleared or nil PIN fails without calling consume.
func (pin *operatorAdminV1TransientPIN) WithTransientPIN(_ string, consume func([]byte) error) (bool, error) {
	if pin == nil || pin.cleared() || consume == nil {
		return false, errors.New("transient PIN provider invalid")
	}
	defer pin.clear()
	return true, consume(pin.bytes)
}

// DiscardTransientPIN zeroes the PIN bytes without using them.
func (pin *operatorAdminV1TransientPIN) DiscardTransientPIN(string) { pin.clear() }

func (pin *operatorAdminV1TransientPIN) clear() {
	if pin == nil {
		return
	}
	for index := range pin.bytes {
		pin.bytes[index] = 0
	}
	pin.bytes = nil
}

func (pin *operatorAdminV1TransientPIN) cleared() bool { return pin == nil || len(pin.bytes) == 0 }

// String returns the redacted rendering, never PIN bytes.
func (operatorAdminV1TransientPIN) String() string { return operatorAdminV1String() }

// GoString returns the redacted rendering, never PIN bytes.
func (operatorAdminV1TransientPIN) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (operatorAdminV1TransientPIN) Format(state fmt.State, verb rune) {
	operatorAdminV1Format(state, verb)
}

// MarshalJSON always fails; a PIN has no serialized form.
func (operatorAdminV1TransientPIN) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

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

type operatorAdminV1PINProvider interface {
	WithTransientPIN(string, func([]byte) error) (bool, error)
}

type operatorAdminV1Transition struct {
	outcome  AdminOutcomeV1
	changed  bool
	actionID string
}

type operatorAdminV1ActiveActionFact struct {
	actionID  string
	kind      string
	state     string
	outcome   AdminOutcomeV1
	retryable bool
	expiresAt time.Time
}

type operatorAdminV1SnapshotFacts struct {
	capturedAt     time.Time
	localSKI       string
	localSHIPID    string
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
	activeAction   *operatorAdminV1ActiveActionFact
}

type operatorAdminV1TrustedFact struct {
	reference       string
	ski             string
	endpoint        string
	trustState      string
	connectionState string
	shipID          string
	lastSeen        time.Time
	name            string
	identifier      string
	brand           string
	typeName        string
	model           string
	retryState      string
	retryDeadline   time.Time
	retryAdmitted   bool
}

type operatorAdminV1ConnectedFact struct {
	ski             string
	endpoint        string
	trustState      string
	connectionState string
	shipID          string
	lastSeen        time.Time
	name            string
	identifier      string
	brand           string
	typeName        string
	model           string
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
	actionID  string
	failure   AdminErrorCodeV1
	sequence  uint64
}

type operatorAdminV1Reducer struct {
	serial chan struct{}
	mu     sync.Mutex

	now             func() time.Time
	random          io.Reader
	bindingKey      [32]byte
	bindingKeyReady bool
	lifecycle       operatorAdminV1Lifecycle
	backend         operatorAdminV1Backend

	initialized    bool
	revision       uint64
	facts          operatorAdminV1SnapshotFacts
	handles        map[[32]byte]operatorAdminV1HandleRecord
	byTarget       map[operatorAdminV1HandleKind]map[string][32]byte
	replays        map[string]operatorAdminV1Replay
	replaySequence uint64
}

type operatorAdminV1Backend interface {
	snapshotOperatorAdminV1(context.Context) (operatorAdminV1SnapshotFacts, *AdminErrorV1)
	observeOperatorAdminV1ActiveAction(context.Context, string)
	openOperatorAdminV1(context.Context, time.Duration) (operatorAdminV1Transition, *AdminErrorV1)
	closeOperatorAdminV1(context.Context) (operatorAdminV1Transition, *AdminErrorV1)
	selectOperatorAdminV1(context.Context, string, string) (string, operatorAdminV1Transition, *AdminErrorV1)
	connectOperatorAdminV1(context.Context, string) (operatorAdminV1Transition, *AdminErrorV1)
	connectOperatorAdminV1WithPIN(context.Context, string, operatorAdminV1PINProvider) (operatorAdminV1Transition, *AdminErrorV1)
	confirmOperatorAdminV1(context.Context, string, string) (operatorAdminV1Transition, *AdminErrorV1)
	cancelOperatorAdminV1(context.Context, string) (operatorAdminV1Transition, *AdminErrorV1)
	retryTrustedOperatorAdminV1(context.Context, string) (operatorAdminV1Transition, *AdminErrorV1)
	untrustOperatorAdminV1(context.Context, string) (operatorAdminV1Transition, *AdminErrorV1)
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

func (slot *operatorAdminV1BackendSlot) observeOperatorAdminV1ActiveAction(ctx context.Context, actionID string) {
	if backend := slot.current(); backend != nil {
		backend.observeOperatorAdminV1ActiveAction(ctx, actionID)
	}
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

func (slot *operatorAdminV1BackendSlot) connectOperatorAdminV1WithPIN(ctx context.Context, reference string, provider operatorAdminV1PINProvider) (operatorAdminV1Transition, *AdminErrorV1) {
	if backend := slot.current(); backend != nil && provider != nil {
		return backend.connectOperatorAdminV1WithPIN(ctx, reference, provider)
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
