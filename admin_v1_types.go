package eebusruntime

import (
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

const operatorAdminV1RedactedRendering = "admin_v1{redacted}"

var errOperatorAdminV1Serialization = errors.New("AdminV1 owner value cannot be serialized")

func operatorAdminV1Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, operatorAdminV1RedactedRendering)
}
func operatorAdminV1String() string               { return operatorAdminV1RedactedRendering }
func operatorAdminV1MarshalJSON() ([]byte, error) { return nil, errOperatorAdminV1Serialization }

// String returns the redacted rendering, never reducer state.
func (*operatorAdminV1Reducer) String() string { return operatorAdminV1String() }

// GoString returns the redacted rendering, never reducer state.
func (*operatorAdminV1Reducer) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (*operatorAdminV1Reducer) Format(state fmt.State, verb rune) {
	operatorAdminV1Format(state, verb)
}

// MarshalJSON always fails; the reducer has no serialized form.
func (*operatorAdminV1Reducer) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the PartnerHandleV1 contents.
func (PartnerHandleV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (PartnerHandleV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (PartnerHandleV1) Format(state fmt.State, verb rune) { operatorAdminV1Format(state, verb) }

// MarshalJSON always fails: a PartnerHandleV1 is an in-process value with no serialized form.
func (PartnerHandleV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the ObservationHandleV1 contents.
func (ObservationHandleV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (ObservationHandleV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (ObservationHandleV1) Format(state fmt.State, verb rune) {
	operatorAdminV1Format(state, verb)
}

// MarshalJSON always fails: an ObservationHandleV1 is an in-process value with no serialized form.
func (ObservationHandleV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the SelectionHandleV1 contents.
func (SelectionHandleV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (SelectionHandleV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (SelectionHandleV1) Format(state fmt.State, verb rune) {
	operatorAdminV1Format(state, verb)
}

// MarshalJSON always fails: a SelectionHandleV1 is an in-process value with no serialized form.
func (SelectionHandleV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the CandidateHandleV1 contents.
func (CandidateHandleV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (CandidateHandleV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (CandidateHandleV1) Format(state fmt.State, verb rune) {
	operatorAdminV1Format(state, verb)
}

// MarshalJSON always fails: a CandidateHandleV1 is an in-process value with no serialized form.
func (CandidateHandleV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the MutationPreconditionV1 contents.
func (MutationPreconditionV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (MutationPreconditionV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (MutationPreconditionV1) Format(state fmt.State, verb rune) { operatorAdminV1Format(state, verb) }

// MarshalJSON always fails: a MutationPreconditionV1 is an in-process value with no serialized form.
func (MutationPreconditionV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the AdminSnapshotRequestV1 contents.
func (AdminSnapshotRequestV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (AdminSnapshotRequestV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (AdminSnapshotRequestV1) Format(state fmt.State, verb rune) { operatorAdminV1Format(state, verb) }

// MarshalJSON always fails: an AdminSnapshotRequestV1 is an in-process value with no serialized form.
func (AdminSnapshotRequestV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the OpenPairingWindowRequestV1 contents.
func (OpenPairingWindowRequestV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (OpenPairingWindowRequestV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (OpenPairingWindowRequestV1) Format(state fmt.State, verb rune) {
	operatorAdminV1Format(state, verb)
}

// MarshalJSON always fails: an OpenPairingWindowRequestV1 is an in-process value with no serialized form.
func (OpenPairingWindowRequestV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the ClosePairingWindowRequestV1 contents.
func (ClosePairingWindowRequestV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (ClosePairingWindowRequestV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (ClosePairingWindowRequestV1) Format(state fmt.State, verb rune) {
	operatorAdminV1Format(state, verb)
}

// MarshalJSON always fails: a ClosePairingWindowRequestV1 is an in-process value with no serialized form.
func (ClosePairingWindowRequestV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the SelectRequestV1 contents.
func (SelectRequestV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (SelectRequestV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (SelectRequestV1) Format(state fmt.State, verb rune) { operatorAdminV1Format(state, verb) }

// MarshalJSON always fails: a SelectRequestV1 is an in-process value with no serialized form.
func (SelectRequestV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the ConnectRequestV1 contents.
func (ConnectRequestV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (ConnectRequestV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (ConnectRequestV1) Format(state fmt.State, verb rune) { operatorAdminV1Format(state, verb) }

// MarshalJSON always fails: a ConnectRequestV1 is an in-process value with no serialized form.
func (ConnectRequestV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the ConfirmRequestV1 contents.
func (ConfirmRequestV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (ConfirmRequestV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (ConfirmRequestV1) Format(state fmt.State, verb rune) { operatorAdminV1Format(state, verb) }

// MarshalJSON always fails: a ConfirmRequestV1 is an in-process value with no serialized form.
func (ConfirmRequestV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the CancelRequestV1 contents.
func (CancelRequestV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (CancelRequestV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (CancelRequestV1) Format(state fmt.State, verb rune) { operatorAdminV1Format(state, verb) }

// MarshalJSON always fails: a CancelRequestV1 is an in-process value with no serialized form.
func (CancelRequestV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the RetryTrustedRequestV1 contents.
func (RetryTrustedRequestV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (RetryTrustedRequestV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (RetryTrustedRequestV1) Format(state fmt.State, verb rune) { operatorAdminV1Format(state, verb) }

// MarshalJSON always fails: a RetryTrustedRequestV1 is an in-process value with no serialized form.
func (RetryTrustedRequestV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the UntrustRequestV1 contents.
func (UntrustRequestV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (UntrustRequestV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (UntrustRequestV1) Format(state fmt.State, verb rune) { operatorAdminV1Format(state, verb) }

// MarshalJSON always fails: an UntrustRequestV1 is an in-process value with no serialized form.
func (UntrustRequestV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the AdminMutationResultV1 contents.
func (AdminMutationResultV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (AdminMutationResultV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (AdminMutationResultV1) Format(state fmt.State, verb rune) { operatorAdminV1Format(state, verb) }

// MarshalJSON always fails: an AdminMutationResultV1 is an in-process value with no serialized form.
func (AdminMutationResultV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the ConnectResultV1 contents.
func (ConnectResultV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (ConnectResultV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (ConnectResultV1) Format(state fmt.State, verb rune) { operatorAdminV1Format(state, verb) }

// MarshalJSON always fails: a ConnectResultV1 is an in-process value with no serialized form.
func (ConnectResultV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the AdminSelectionResultV1 contents.
func (AdminSelectionResultV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (AdminSelectionResultV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (AdminSelectionResultV1) Format(state fmt.State, verb rune) {
	operatorAdminV1Format(state, verb)
}

// MarshalJSON always fails: an AdminSelectionResultV1 is an in-process value with no serialized form.
func (AdminSelectionResultV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the AdminSnapshotV1 contents.
func (AdminSnapshotV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (AdminSnapshotV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (AdminSnapshotV1) Format(state fmt.State, verb rune) { operatorAdminV1Format(state, verb) }

// MarshalJSON always fails: an AdminSnapshotV1 is an in-process value with no serialized form.
func (AdminSnapshotV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the ActiveActionV1 contents.
func (ActiveActionV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (ActiveActionV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (ActiveActionV1) Format(state fmt.State, verb rune) { operatorAdminV1Format(state, verb) }

// MarshalJSON always fails: an ActiveActionV1 is an in-process value with no serialized form.
func (ActiveActionV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the TrustedPartnerV1 contents.
func (TrustedPartnerV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (TrustedPartnerV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (TrustedPartnerV1) Format(state fmt.State, verb rune) { operatorAdminV1Format(state, verb) }

// MarshalJSON always fails: a TrustedPartnerV1 is an in-process value with no serialized form.
func (TrustedPartnerV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the ConnectedPartnerV1 contents.
func (ConnectedPartnerV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (ConnectedPartnerV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (ConnectedPartnerV1) Format(state fmt.State, verb rune) {
	operatorAdminV1Format(state, verb)
}

// MarshalJSON always fails: a ConnectedPartnerV1 is an in-process value with no serialized form.
func (ConnectedPartnerV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the DiscoveredPartnerV1 contents.
func (DiscoveredPartnerV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (DiscoveredPartnerV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (DiscoveredPartnerV1) Format(state fmt.State, verb rune) {
	operatorAdminV1Format(state, verb)
}

// MarshalJSON always fails: a DiscoveredPartnerV1 is an in-process value with no serialized form.
func (DiscoveredPartnerV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the CandidateV1 contents.
func (CandidateV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (CandidateV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (CandidateV1) Format(state fmt.State, verb rune) { operatorAdminV1Format(state, verb) }

// MarshalJSON always fails: a CandidateV1 is an in-process value with no serialized form.
func (CandidateV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

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
