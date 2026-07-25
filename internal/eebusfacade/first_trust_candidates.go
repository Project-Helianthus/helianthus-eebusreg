package eebusfacade

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"time"
	"unicode/utf8"

	eebusapi "github.com/Project-Helianthus/helianthus-eebus-go/api"
	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
)

const (
	firstTrustMaximumDiscoveredCandidates = 128
	firstTrustMaximumCandidateRefBytes    = 128
)

type firstTrustCandidateQueuer interface {
	queuePairingCandidate(string, string) error
}

type firstTrustCandidateSelectionSink interface {
	configureCandidateSelection(firstTrustCandidateQueuer)
	visiblePairingCandidatesUpdated([]shipapi.PairingCandidateRef)
}

type firstTrustTLSBindingSink interface {
	remoteSKIConnected([]byte, uint64) string
}

type firstTrustDiscoveredCandidate struct {
	ref           string
	claimedSKI    string
	firstReceived time.Time
	lastReceived  time.Time
	revision      uint64
	lifecycle     string
}

type firstTrustDiscoveredCandidateView struct {
	ref           string
	firstReceived time.Time
	lastReceived  time.Time
	revision      uint64
	lifecycle     string
}

type firstTrustCandidateSnapshotView struct {
	state         string
	invalidReason string
	revision      uint64
	candidates    []firstTrustDiscoveredCandidateView
}

type firstTrustCandidateSelection struct {
	key          string
	request      firstTrustRequest
	candidateRef string
	expectedSKI  string
	remote       []byte
	done         chan struct{}
	completed    bool
	cancelled    bool
	cancelIssued bool
	abortOutcome string
	outcome      string
}

func (coordinator *firstTrustCoordinator) configureCandidateSelection(queuer firstTrustCandidateQueuer) {
	if coordinator == nil || queuer == nil {
		return
	}
	coordinator.mu.Lock()
	coordinator.candidateQueuer = queuer
	coordinator.candidateSelectionRequired = true
	if coordinator.candidateSnapshotState == "" {
		coordinator.candidateSnapshotState = "empty"
	}
	coordinator.mu.Unlock()
}

func (coordinator *firstTrustCoordinator) visiblePairingCandidatesUpdated(candidates []shipapi.PairingCandidateRef) {
	if coordinator == nil {
		return
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()

	coordinator.candidateSnapshotRevision++
	if coordinator.candidateSnapshotRevision == 0 {
		coordinator.candidateSnapshotRevision++
	}
	revision := coordinator.candidateSnapshotRevision
	now := coordinator.now()
	if len(candidates) == 0 {
		coordinator.discoveredCandidates = nil
		coordinator.candidateSnapshotState = "empty"
		coordinator.candidateSnapshotInvalidReason = ""
		return
	}
	if len(candidates) > firstTrustMaximumDiscoveredCandidates {
		coordinator.invalidateCandidateSnapshotLocked("candidate_snapshot_overflow")
		return
	}

	next := make(map[string]firstTrustDiscoveredCandidate, len(candidates))
	for _, candidate := range candidates {
		if !validFirstTrustCandidateRef(candidate.CandidateRef) {
			coordinator.invalidateCandidateSnapshotLocked("invalid_candidate_ref")
			return
		}
		remote, _, ok := decodeFirstTrustSKI(candidate.SKI)
		if !ok || len(remote) != 20 {
			coordinator.invalidateCandidateSnapshotLocked("invalid_claimed_ski")
			return
		}
		if _, duplicate := next[candidate.CandidateRef]; duplicate {
			coordinator.invalidateCandidateSnapshotLocked("duplicate_candidate_ref")
			return
		}
		entry := firstTrustDiscoveredCandidate{
			ref:           candidate.CandidateRef,
			claimedSKI:    candidate.SKI,
			firstReceived: now,
			lastReceived:  now,
			revision:      revision,
			lifecycle:     "visible",
		}
		if previous, exists := coordinator.discoveredCandidates[candidate.CandidateRef]; exists && previous.claimedSKI == candidate.SKI {
			entry.firstReceived = previous.firstReceived
			if previous.lifecycle == "consumed" {
				entry.lifecycle = "consumed"
			}
		}
		if selection := coordinator.candidateSelection; selection != nil &&
			selection.candidateRef == candidate.CandidateRef && selection.expectedSKI == candidate.SKI {
			if selection.completed {
				entry.lifecycle = "consumed"
			} else {
				entry.lifecycle = "reserved"
			}
		}
		next[candidate.CandidateRef] = entry
	}
	coordinator.discoveredCandidates = next
	coordinator.candidateSnapshotState = "valid"
	coordinator.candidateSnapshotInvalidReason = ""
}

func (coordinator *firstTrustCoordinator) invalidateCandidateSnapshotLocked(reason string) {
	coordinator.discoveredCandidates = nil
	coordinator.candidateSnapshotState = "invalid"
	coordinator.candidateSnapshotInvalidReason = reason
}

func (coordinator *firstTrustCoordinator) discoveredCandidateSnapshot() firstTrustCandidateSnapshotView {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	state := coordinator.candidateSnapshotState
	if state == "" {
		state = "empty"
	}
	view := firstTrustCandidateSnapshotView{
		state:         state,
		invalidReason: coordinator.candidateSnapshotInvalidReason,
		revision:      coordinator.candidateSnapshotRevision,
		candidates:    make([]firstTrustDiscoveredCandidateView, 0, len(coordinator.discoveredCandidates)),
	}
	for _, candidate := range coordinator.discoveredCandidates {
		view.candidates = append(view.candidates, firstTrustDiscoveredCandidateView{
			ref:           candidate.ref,
			firstReceived: candidate.firstReceived,
			lastReceived:  candidate.lastReceived,
			revision:      candidate.revision,
			lifecycle:     candidate.lifecycle,
		})
	}
	sort.Slice(view.candidates, func(left, right int) bool {
		return view.candidates[left].ref < view.candidates[right].ref
	})
	return view
}

func (coordinator *firstTrustCoordinator) selectCandidate(ctx context.Context, key, candidateRef, expectedSKI string) string {
	ctx = firstTrustContext(ctx)
	if ctx.Err() != nil {
		return "request_cancelled"
	}
	if !validFirstTrustKey(key) {
		return "invalid_idempotency_key"
	}
	if !validFirstTrustCandidateRef(candidateRef) {
		return "invalid_candidate_ref"
	}
	remote, _, ok := decodeFirstTrustSKI(expectedSKI)
	if !ok {
		return "invalid_expected_ski"
	}
	request := firstTrustRequest{
		operation:    "select_candidate",
		candidateRef: candidateRef,
		expectedSKI:  expectedSKI,
	}

	for {
		coordinator.mu.Lock()
		now := coordinator.now()
		coordinator.expireLocked(now)
		if result, replayed := coordinator.replayLocked(key, request, now); replayed {
			coordinator.mu.Unlock()
			return result
		}
		if selection := coordinator.candidateSelection; selection != nil {
			if selection.key == key {
				if !selection.request.equal(request) {
					coordinator.mu.Unlock()
					return "idempotency_conflict"
				}
				if selection.completed {
					coordinator.mu.Unlock()
					return selection.outcome
				}
				done := selection.done
				coordinator.mu.Unlock()
				select {
				case <-done:
					continue
				case <-ctx.Done():
					return "request_cancelled"
				}
			}
			if selection.completed && selection.candidateRef == candidateRef {
				coordinator.mu.Unlock()
				return "candidate_consumed"
			}
			coordinator.mu.Unlock()
			return "candidate_busy"
		}
		if coordinator.activeKeyConflictLocked(key, request) {
			coordinator.mu.Unlock()
			return "idempotency_conflict"
		}
		if coordinator.phase == firstTrustDisabled || coordinator.reopening {
			coordinator.mu.Unlock()
			return "mutation_disabled"
		}
		if coordinator.phase != firstTrustOpenEmpty || coordinator.window == nil {
			coordinator.mu.Unlock()
			return "pairing_closed"
		}
		if coordinator.candidateSnapshotState == "invalid" {
			coordinator.mu.Unlock()
			return "candidate_snapshot_invalid"
		}
		candidate, exists := coordinator.discoveredCandidates[candidateRef]
		if !exists {
			coordinator.mu.Unlock()
			return "candidate_unavailable"
		}
		if candidate.lifecycle == "consumed" {
			coordinator.mu.Unlock()
			return "candidate_consumed"
		}
		if candidate.lifecycle != "visible" {
			coordinator.mu.Unlock()
			return "candidate_busy"
		}
		if candidate.claimedSKI != expectedSKI || !constantTimeFingerprintMatch(candidate.claimedSKI, remote) {
			coordinator.mu.Unlock()
			return "candidate_ski_mismatch"
		}
		if coordinator.idempotencyCapacityLocked(key, 1) {
			coordinator.mu.Unlock()
			return "idempotency_capacity"
		}
		if coordinator.candidateQueuer == nil {
			coordinator.mu.Unlock()
			return "candidate_queue_unavailable"
		}
		selection := &firstTrustCandidateSelection{
			key:          key,
			request:      request,
			candidateRef: candidateRef,
			expectedSKI:  expectedSKI,
			remote:       bytes.Clone(remote),
			done:         make(chan struct{}),
		}
		candidate.lifecycle = "reserved"
		coordinator.discoveredCandidates[candidateRef] = candidate
		coordinator.candidateSelection = selection
		queuer := coordinator.candidateQueuer
		coordinator.mu.Unlock()

		queueErr := callFirstTrustCandidateQueue(queuer, candidateRef, expectedSKI)

		coordinator.mu.Lock()
		result := mapFirstTrustCandidateQueueError(queueErr)
		if ctx.Err() != nil && selection.abortOutcome == "" {
			selection.abortOutcome = "request_cancelled"
		}
		if selection.abortOutcome != "" {
			result = selection.abortOutcome
		}
		selection.outcome = result
		selection.completed = true
		if result == "candidate_queued" {
			if candidate, exists := coordinator.discoveredCandidates[candidateRef]; exists && candidate.claimedSKI == expectedSKI {
				candidate.lifecycle = "consumed"
				coordinator.discoveredCandidates[candidateRef] = candidate
			}
		} else {
			coordinator.failCandidateQueueLocked(selection, result)
			coordinator.invalidateCandidateSnapshotLocked("candidate_queue_failed")
			if coordinator.candidateSelection == selection {
				coordinator.candidateSelection = nil
			}
		}
		coordinator.recordReplayLocked(key, request, result, coordinator.now())
		close(selection.done)
		coordinator.mu.Unlock()
		return result
	}
}

func (coordinator *firstTrustCoordinator) failCandidateQueueLocked(selection *firstTrustCandidateSelection, result string) {
	candidate := coordinator.currentCandidate
	if candidate != nil && bytes.Equal(candidate.remote, selection.remote) {
		now := coordinator.now()
		coordinator.finishCandidateRequestsLocked(result, now)
		coordinator.cancelRemoteLocked(candidate.remote, candidate.connection)
		coordinator.currentCandidate = nil
		selection.cancelled = true
		selection.cancelIssued = true
		if coordinator.window != nil && now.Before(coordinator.window.deadline) {
			coordinator.phase = firstTrustOpenEmpty
			coordinator.scheduleExpiryLocked(coordinator.window.deadline)
		}
		return
	}
	coordinator.cancelCandidateSelectionLocked(selection)
}

func callFirstTrustCandidateQueue(queuer firstTrustCandidateQueuer, candidateRef, expectedSKI string) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("pairing candidate queue panicked")
		}
	}()
	return queuer.queuePairingCandidate(candidateRef, expectedSKI)
}

func mapFirstTrustCandidateQueueError(err error) string {
	switch {
	case err == nil:
		return "candidate_queued"
	case errors.Is(err, shipapi.ErrPairingCandidateUnavailable):
		return "candidate_unavailable"
	case errors.Is(err, shipapi.ErrPairingCandidateSKIMismatch):
		return "candidate_ski_mismatch"
	case errors.Is(err, shipapi.ErrPairingCandidateConsumed):
		return "candidate_consumed"
	case errors.Is(err, shipapi.ErrPairingCandidateActive):
		return "candidate_active"
	case err != nil && err.Error() == "outgoing attempt gate is required":
		return "transport_gate_unavailable"
	case errors.Is(err, shipapi.ErrRemoteAlreadyTrusted):
		return "already_trusted"
	default:
		return "candidate_queue_failed"
	}
}

func (coordinator *firstTrustCoordinator) selectedCandidateMatchesLocked(remote []byte) bool {
	selection := coordinator.candidateSelection
	return selection != nil && !selection.cancelled && len(remote) == 20 &&
		bytes.Equal(selection.remote, remote) &&
		(coordinator.phase == firstTrustOpenEmpty ||
			coordinator.phase == firstTrustCandidatePending ||
			coordinator.phase == firstTrustCommitting)
}

func (coordinator *firstTrustCoordinator) remoteSKIConnected(remote []byte, connection uint64) string {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	coordinator.expireLocked(coordinator.now())
	candidate := coordinator.currentCandidate
	if candidate == nil || coordinator.phase != firstTrustCandidatePending ||
		connection == 0 || connection != candidate.connection ||
		!bytes.Equal(remote, candidate.remote) {
		return "ignored"
	}
	if candidate.tlsRequired && !coordinator.selectedCandidateMatchesLocked(remote) {
		return "ignored"
	}
	candidate.tlsBound = true
	return "tls_bound"
}

func (coordinator *firstTrustCoordinator) finishCandidateSelectionLocked(outcome string, cancel bool) {
	selection := coordinator.candidateSelection
	if selection == nil {
		return
	}
	if cancel {
		coordinator.cancelCandidateSelectionLocked(selection)
	}
	if !selection.completed {
		if selection.abortOutcome == "" {
			selection.abortOutcome = outcome
		}
		return
	}
	coordinator.candidateSelection = nil
}

func (coordinator *firstTrustCoordinator) cancelCandidateSelectionLocked(selection *firstTrustCandidateSelection) {
	if selection == nil || selection.cancelIssued {
		return
	}
	selection.cancelled = true
	selection.cancelIssued = true
	coordinator.cancelUnboundRemoteLocked(selection.remote)
}

func (coordinator *firstTrustCoordinator) resetCandidateDiscoveryLocked(state string) {
	if state == "" {
		state = "empty"
	}
	coordinator.discoveredCandidates = nil
	coordinator.candidateSnapshotState = state
	coordinator.candidateSnapshotInvalidReason = ""
	coordinator.candidateSnapshotRevision = 0
	coordinator.finishCandidateSelectionLocked("stale_request", true)
	if coordinator.candidateSelection != nil && coordinator.candidateSelection.completed {
		coordinator.candidateSelection = nil
	}
}

func validFirstTrustCandidateRef(value string) bool {
	if len(value) < 1 || len(value) > firstTrustMaximumCandidateRefBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range []byte(value) {
		if character < '!' || character > '~' ||
			character == '"' || character == '&' || character == '<' ||
			character == '>' || character == '\\' {
			return false
		}
	}
	return true
}

var _ eebusapi.PairingCandidateReader = (*firstTrustFacade)(nil)
