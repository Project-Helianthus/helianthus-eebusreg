package eebusfacade

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"time"
)

const (
	firstTrustMaximumWindow        = 5 * time.Minute
	firstTrustMaximumCandidate     = 2 * time.Minute
	firstTrustCommitWait           = 10 * time.Second
	firstTrustReplayTTL            = 5 * time.Minute
	firstTrustRetiredTTL           = 5 * time.Minute
	firstTrustMaximumReplayEntries = 128
	firstTrustMaximumIdempotency   = 256
	firstTrustMaximumRetiredKeys   = firstTrustMaximumIdempotency
	firstTrustMaximumActiveKeys    = 32
	firstTrustMaximumKeyBytes      = 128
	firstTrustWithdrawalWait       = 2 * time.Second
)

var errFirstTrustPairingRegistrationFailed = errors.New("pairing registration failed")

type firstTrustPersistence interface {
	Reload(context.Context) (uint64, map[string]string, string)
	SelectedGeneration() uint64
	Commit(context.Context, uint64, []byte, string) string
}

type firstTrustEffects interface {
	setWaiting(bool) error
	cancelRemote([]byte, uint64)
	connectionAlive([]byte, uint64) bool
	registerRemoteSKI([]byte, uint64)
}

type firstTrustEffectKind uint8

const (
	firstTrustEffectCancel firstTrustEffectKind = iota
	firstTrustEffectRegister
)

type firstTrustEffect struct {
	kind       firstTrustEffectKind
	target     firstTrustEffects
	remote     []byte
	connection uint64
}

type firstTrustCoordinatorMutex struct {
	sync.Mutex
	coordinator *firstTrustCoordinator
}

func (mutex *firstTrustCoordinatorMutex) Unlock() {
	coordinator := mutex.coordinator
	if coordinator == nil {
		mutex.Mutex.Unlock()
		return
	}

	effects := coordinator.pendingEffects
	coordinator.pendingEffects = nil
	startDrain := false
	if len(effects) != 0 {
		coordinator.effectMu.Lock()
		coordinator.effectQueue = append(coordinator.effectQueue, effects...)
		if !coordinator.effectDraining {
			coordinator.effectDraining = true
			startDrain = true
		}
		coordinator.effectMu.Unlock()
	}
	mutex.Mutex.Unlock()
	if startDrain {
		coordinator.drainEffects()
	}
}

type firstTrustWithdrawalEffects interface {
	disconnectRemote([]byte) (<-chan struct{}, bool)
	cancelDisconnect([]byte, <-chan struct{})
	unregisterRemote([]byte) bool
}

type firstTrustPhase uint8

const (
	firstTrustDisabled firstTrustPhase = iota
	firstTrustPairingClosed
	firstTrustOpenEmpty
	firstTrustCandidatePending
	firstTrustCommitting
)

type firstTrustWindow struct {
	key      string
	duration time.Duration
	deadline time.Time
}

type firstTrustCandidate struct {
	remote          []byte
	shipID          string
	nonce           string
	expiresAt       time.Time
	connection      uint64
	storeGeneration uint64
	requests        map[string]firstTrustRequest
}

type firstTrustRequest struct {
	operation       string
	duration        time.Duration
	fingerprint     string
	nonce           string
	expiresAt       time.Time
	connection      uint64
	storeGeneration uint64
}

func (request firstTrustRequest) equal(other firstTrustRequest) bool {
	return request.operation == other.operation &&
		request.duration == other.duration &&
		request.fingerprint == other.fingerprint &&
		request.nonce == other.nonce &&
		request.expiresAt.Equal(other.expiresAt) &&
		request.connection == other.connection &&
		request.storeGeneration == other.storeGeneration
}

type firstTrustReplay struct {
	request   firstTrustRequest
	result    string
	expiresAt time.Time
	sequence  uint64
}

type firstTrustRetired struct {
	expiresAt time.Time
	sequence  uint64
}

type firstTrustInflight struct {
	key     string
	request firstTrustRequest
	done    chan struct{}
}

type firstTrustCoordinator struct {
	mu firstTrustCoordinatorMutex

	pendingEffects []firstTrustEffect
	effectMu       sync.Mutex
	effectQueue    []firstTrustEffect
	effectDraining bool

	now            func() time.Time
	random         io.Reader
	store          firstTrustPersistence
	effects        firstTrustEffects
	commitWait     time.Duration
	withdrawalWait time.Duration

	phase            firstTrustPhase
	window           *firstTrustWindow
	currentCandidate *firstTrustCandidate
	trustedRemotes   map[string]string
	storeGeneration  uint64
	replays          map[string]firstTrustReplay
	retired          map[string]firstTrustRetired
	replaySequence   uint64
	inflight         *firstTrustInflight
	commitToken      uint64
	commitFence      <-chan struct{}
	reopening        bool
	timer            *time.Timer
	timerToken       uint64
	retentionTimer   *time.Timer
	retentionToken   uint64

	pairingRegistrationKnown   bool
	pairingRegistrationEnabled bool
	pairingRegistrationFault   bool

	monotonicNow         func() time.Duration
	recoveryStore        firstTrustControlPersistence
	anchor               firstTrustAnchorProvider
	identityProvider     firstTrustIdentityProvider
	backoffPolicy        firstTrustBackoffPolicy
	recovery             string
	recoveryReasonCode   string
	controlView          firstTrustControlView
	anchorRecord         firstTrustAnchorRecord
	retryArms            map[[32]byte]firstTrustRetryArm
	retryInflight        map[[32]byte]bool
	recoveryOperation    *firstTrustRecoveryOperation
	trustAdminProjection *trustAdminProjectionBinding
	trustAdminRevision   uint64
}

func newFirstTrustCoordinator(now func() time.Time, random io.Reader, store firstTrustPersistence, effects firstTrustEffects) *firstTrustCoordinator {
	if now == nil {
		now = time.Now
	}
	if random == nil {
		random = rand.Reader
	}
	coordinator := &firstTrustCoordinator{
		now:            now,
		random:         random,
		store:          store,
		effects:        effects,
		commitWait:     firstTrustCommitWait,
		withdrawalWait: firstTrustWithdrawalWait,
		phase:          firstTrustDisabled,
		trustedRemotes: make(map[string]string),
		replays:        make(map[string]firstTrustReplay),
		retired:        make(map[string]firstTrustRetired),
	}
	coordinator.mu.coordinator = coordinator
	return coordinator
}

func (coordinator *firstTrustCoordinator) reopen(ctx context.Context) string {
	if coordinator.recoveryStore != nil {
		if outcome := coordinator.preparePairingRegistrationReopen(); outcome != "" {
			return outcome
		}
		outcome := coordinator.reopenWithRecovery(ctx)
		coordinator.mu.Lock()
		defer coordinator.mu.Unlock()
		if coordinator.pairingRegistrationFault {
			coordinator.failPairingRegistrationLocked()
			return "pairing_registration_failed"
		}
		return outcome
	}
	ctx = firstTrustContext(ctx)
	coordinator.mu.Lock()
	if coordinator.reopening {
		coordinator.mu.Unlock()
		return "reopen_in_progress"
	}
	if coordinator.phase != firstTrustDisabled {
		coordinator.mu.Unlock()
		return "reopen_not_required"
	}
	if coordinator.pairingRegistrationFault {
		coordinator.failPairingRegistrationLocked()
		coordinator.mu.Unlock()
		return "pairing_registration_failed"
	}
	if coordinator.commitFence != nil {
		select {
		case <-coordinator.commitFence:
			coordinator.commitFence = nil
		default:
			coordinator.mu.Unlock()
			return "reopen_pending"
		}
	}
	if coordinator.store == nil {
		coordinator.mu.Unlock()
		return "store_unavailable"
	}
	coordinator.reopening = true
	coordinator.phase = firstTrustDisabled
	coordinator.window = nil
	coordinator.currentCandidate = nil
	coordinator.inflight = nil
	coordinator.replays = make(map[string]firstTrustReplay)
	coordinator.retired = make(map[string]firstTrustRetired)
	coordinator.stopTimerLocked()
	coordinator.stopRetentionTimerLocked()
	if err := coordinator.setWaitingLocked(false); err != nil {
		coordinator.reopening = false
		coordinator.mu.Unlock()
		return "pairing_registration_failed"
	}
	coordinator.mu.Unlock()

	generation, associations, outcome := coordinator.store.Reload(ctx)

	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	coordinator.reopening = false
	if coordinator.pairingRegistrationFault {
		coordinator.failPairingRegistrationLocked()
		return "pairing_registration_failed"
	}
	if ctx.Err() != nil {
		return "reopen_cancelled"
	}
	if outcome != "opened_empty" && outcome != "opened_current" {
		return outcome
	}
	trusted := make(map[string]string, len(associations))
	for remote, shipID := range associations {
		if len(remote) == 20 && shipID != "" {
			trusted[remote] = shipID
		}
	}
	coordinator.trustedRemotes = trusted
	coordinator.storeGeneration = generation
	coordinator.phase = firstTrustPairingClosed
	return "pairing_closed"
}

func (coordinator *firstTrustCoordinator) openPairingWindow(ctx context.Context, key string, duration time.Duration) string {
	ctx = firstTrustContext(ctx)
	if ctx.Err() != nil {
		return "request_cancelled"
	}
	if !validFirstTrustKey(key) {
		return "invalid_idempotency_key"
	}
	if duration <= 0 || duration > firstTrustMaximumWindow {
		return "duration_out_of_range"
	}

	coordinator.mu.Lock()
	result := coordinator.openPairingWindowLocked(key, duration)
	coordinator.mu.Unlock()
	return result
}

func (coordinator *firstTrustCoordinator) openPairingWindowLocked(key string, duration time.Duration) string {
	now := coordinator.now()
	coordinator.expireLocked(now)
	request := firstTrustRequest{operation: "open", duration: duration}
	if result, ok := coordinator.replayLocked(key, request, now); ok {
		return result
	}
	if coordinator.activeKeyConflictLocked(key, request) {
		return "idempotency_conflict"
	}
	if coordinator.recoveryStore != nil {
		if coordinator.recoveryOperation != nil {
			return "operation_in_progress"
		}
		if coordinator.reconciliationRequiredLocked() {
			return "reconciliation_required"
		}
	}
	if coordinator.phase == firstTrustDisabled || coordinator.reopening {
		return "mutation_disabled"
	}
	if coordinator.idempotencyCapacityLocked(key, 1) {
		return "idempotency_capacity"
	}
	if coordinator.window != nil {
		if coordinator.window.key == key && coordinator.window.duration == duration {
			return coordinator.openStateLocked()
		}
		return "idempotency_conflict"
	}
	if coordinator.phase != firstTrustPairingClosed {
		return "window_conflict"
	}
	coordinator.window = &firstTrustWindow{key: key, duration: duration, deadline: now.Add(duration)}
	coordinator.phase = firstTrustOpenEmpty
	if err := coordinator.setWaitingLocked(true); err != nil {
		coordinator.recordReplayLocked(key, request, "pairing_registration_failed", now)
		return "pairing_registration_failed"
	}
	coordinator.scheduleExpiryLocked(coordinator.window.deadline)
	return "open_empty"
}

func (coordinator *firstTrustCoordinator) closePairingWindow(ctx context.Context, key string) string {
	ctx = firstTrustContext(ctx)
	if ctx.Err() != nil {
		return "request_cancelled"
	}
	if !validFirstTrustKey(key) {
		return "invalid_idempotency_key"
	}
	request := firstTrustRequest{operation: "close"}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	now := coordinator.now()
	coordinator.expireLocked(now)
	if result, ok := coordinator.replayLocked(key, request, now); ok {
		return result
	}
	if coordinator.activeKeyConflictLocked(key, request) {
		return "idempotency_conflict"
	}
	if coordinator.phase == firstTrustCommitting {
		return "commit_in_progress"
	}
	if coordinator.phase == firstTrustDisabled {
		return "mutation_disabled"
	}
	if coordinator.idempotencyCapacityLocked(key, 0) {
		return "idempotency_capacity"
	}
	if coordinator.window == nil {
		coordinator.recordReplayLocked(key, request, "pairing_closed", now)
		return "pairing_closed"
	}
	if err := coordinator.closeWindowLocked("pairing_closed", now, true); err != nil {
		coordinator.recordReplayLocked(key, request, "pairing_registration_failed", now)
		return "pairing_registration_failed"
	}
	coordinator.recordReplayLocked(key, request, "pairing_closed", now)
	return "pairing_closed"
}

func (coordinator *firstTrustCoordinator) admit(remote []byte, connection uint64) string {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	coordinator.expireLocked(coordinator.now())
	if coordinator.recoveryStore != nil {
		if coordinator.recoveryOperation != nil {
			coordinator.cancelRemoteLocked(remote, connection)
			return "operation_in_progress"
		}
		if coordinator.reconciliationRequiredLocked() {
			coordinator.cancelRemoteLocked(remote, connection)
			return "reconciliation_required"
		}
		if coordinator.recovery != "REVOKED" && firstTrustSubjectTombstoned(coordinator.controlView, remote) {
			coordinator.cancelRemoteLocked(remote, connection)
			return "association_revoked"
		}
	}
	if len(remote) != 20 || connection == 0 {
		coordinator.cancelRemoteLocked(remote, connection)
		return "candidate_ineligible"
	}
	if coordinator.currentCandidate != nil && connection == coordinator.currentCandidate.connection && bytes.Equal(remote, coordinator.currentCandidate.remote) {
		if coordinator.phase == firstTrustCandidatePending {
			return "candidate_pending"
		}
		if coordinator.phase == firstTrustCommitting {
			return "commit_in_progress"
		}
	}
	if coordinator.phase != firstTrustOpenEmpty || coordinator.window == nil {
		coordinator.cancelRemoteLocked(remote, connection)
		if coordinator.phase == firstTrustCandidatePending || coordinator.phase == firstTrustCommitting {
			return "candidate_busy"
		}
		if coordinator.phase == firstTrustDisabled {
			return "mutation_disabled"
		}
		return "pairing_closed"
	}
	if _, exists := coordinator.trustedRemotes[string(remote)]; exists {
		coordinator.cancelRemoteLocked(remote, connection)
		return "already_trusted"
	}
	nonceBytes := make([]byte, 32)
	if _, err := io.ReadFull(coordinator.random, nonceBytes); err != nil {
		coordinator.cancelRemoteLocked(remote, connection)
		return "candidate_unavailable"
	}
	now := coordinator.now()
	expiresAt := now.Add(firstTrustMaximumCandidate)
	if expiresAt.After(coordinator.window.deadline) {
		expiresAt = coordinator.window.deadline
	}
	selectedGeneration := coordinator.selectedFirstTrustGenerationLocked()
	if selectedGeneration == 0 {
		coordinator.cancelRemoteLocked(remote, connection)
		return "candidate_unavailable"
	}
	coordinator.currentCandidate = &firstTrustCandidate{
		remote:          bytes.Clone(remote),
		nonce:           hex.EncodeToString(nonceBytes),
		expiresAt:       expiresAt,
		connection:      connection,
		storeGeneration: selectedGeneration,
		requests:        make(map[string]firstTrustRequest),
	}
	coordinator.phase = firstTrustCandidatePending
	coordinator.scheduleExpiryLocked(expiresAt)
	return "candidate_pending"
}

func (coordinator *firstTrustCoordinator) serviceShipIDUpdate(remote []byte, connection uint64, shipID string) string {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	coordinator.expireLocked(coordinator.now())
	if coordinator.phase != firstTrustCandidatePending || coordinator.currentCandidate == nil {
		return "ignored"
	}
	if shipID == "" || connection != coordinator.currentCandidate.connection || !bytes.Equal(remote, coordinator.currentCandidate.remote) {
		return "ignored"
	}
	coordinator.currentCandidate.shipID = shipID
	return "association_complete"
}

func (coordinator *firstTrustCoordinator) connectionClosed(remote []byte, connection uint64) string {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	now := coordinator.now()
	coordinator.expireLocked(now)
	if coordinator.currentCandidate == nil || connection != coordinator.currentCandidate.connection || !bytes.Equal(remote, coordinator.currentCandidate.remote) {
		return "ignored"
	}
	if coordinator.phase == firstTrustCommitting {
		return "commit_in_progress"
	}
	coordinator.finishCandidateRequestsLocked("connection_closed", now)
	coordinator.currentCandidate = nil
	if coordinator.window != nil && now.Before(coordinator.window.deadline) {
		coordinator.phase = firstTrustOpenEmpty
		coordinator.scheduleExpiryLocked(coordinator.window.deadline)
		return "open_empty"
	}
	if err := coordinator.closeWindowLocked("pairing_closed", now, false); err != nil {
		return "pairing_registration_failed"
	}
	return "pairing_closed"
}

func (coordinator *firstTrustCoordinator) confirm(ctx context.Context, key, fingerprint, nonce string, expiresAt time.Time, connection, storeGeneration uint64) string {
	ctx = firstTrustContext(ctx)
	if ctx.Err() != nil {
		return "request_cancelled"
	}
	if !validFirstTrustKey(key) {
		return "invalid_idempotency_key"
	}
	request := firstTrustRequest{
		operation:       "confirm",
		fingerprint:     fingerprint,
		nonce:           nonce,
		expiresAt:       expiresAt,
		connection:      connection,
		storeGeneration: storeGeneration,
	}

	coordinator.mu.Lock()
	now := coordinator.now()
	coordinator.expireLocked(now)
	if result, ok := coordinator.replayLocked(key, request, now); ok {
		coordinator.mu.Unlock()
		return result
	}
	if coordinator.inflight != nil {
		if coordinator.inflight.key != key || !coordinator.inflight.request.equal(request) {
			coordinator.mu.Unlock()
			return "idempotency_conflict"
		}
		done := coordinator.inflight.done
		coordinator.mu.Unlock()
		select {
		case <-done:
			coordinator.mu.Lock()
			result, ok := coordinator.replayLocked(key, request, coordinator.now())
			coordinator.mu.Unlock()
			if ok {
				return result
			}
			return "stale_request"
		case <-ctx.Done():
			return "request_cancelled"
		}
	}
	if coordinator.phase == firstTrustDisabled {
		coordinator.mu.Unlock()
		return "mutation_disabled"
	}
	if coordinator.phase != firstTrustCandidatePending || coordinator.currentCandidate == nil {
		coordinator.mu.Unlock()
		return "stale_request"
	}
	if coordinator.activeKeyConflictLocked(key, request) {
		coordinator.mu.Unlock()
		return "idempotency_conflict"
	}
	if coordinator.idempotencyCapacityLocked(key, 1) {
		coordinator.mu.Unlock()
		return "idempotency_capacity"
	}
	if result := coordinator.bindCandidateRequestLocked(key, request); result != "" {
		coordinator.mu.Unlock()
		return result
	}
	candidate := coordinator.currentCandidate
	bindingsMatch := nonce == candidate.nonce &&
		expiresAt.Equal(candidate.expiresAt) &&
		connection == candidate.connection &&
		storeGeneration == candidate.storeGeneration &&
		constantTimeFingerprintMatch(fingerprint, candidate.remote)
	if !bindingsMatch {
		coordinator.mu.Unlock()
		return "confirmation_mismatch"
	}
	if candidate.shipID == "" {
		coordinator.mu.Unlock()
		return "association_incomplete"
	}
	if coordinator.selectedFirstTrustGenerationLocked() != candidate.storeGeneration {
		coordinator.mu.Unlock()
		return "store_generation_conflict"
	}

	coordinator.phase = firstTrustCommitting
	coordinator.window = nil
	coordinator.stopTimerLocked()
	coordinator.commitToken++
	token := coordinator.commitToken
	inflight := &firstTrustInflight{key: key, request: request, done: make(chan struct{})}
	coordinator.inflight = inflight
	remote := bytes.Clone(candidate.remote)
	shipID := candidate.shipID
	if coordinator.recoveryStore != nil {
		result := coordinator.confirmWithRecoveryLocked(ctx, token, inflight, remote, shipID, connection)
		coordinator.mu.Lock()
		defer coordinator.mu.Unlock()
		if coordinator.pairingRegistrationFault {
			coordinator.failPairingRegistrationLocked()
			coordinator.recordReplayLocked(key, request, "pairing_registration_failed", coordinator.now())
			return "pairing_registration_failed"
		}
		return result
	}
	coordinator.mu.Unlock()

	commitContext, cancelCommit := context.WithTimeout(ctx, coordinator.commitWait)
	defer cancelCommit()
	result := make(chan string, 1)
	go func() {
		result <- coordinator.store.Commit(commitContext, storeGeneration, remote, shipID)
	}()

	select {
	case outcome := <-result:
		return coordinator.finishCommit(token, inflight, remote, connection, outcome)
	case <-commitContext.Done():
		select {
		case outcome := <-result:
			return coordinator.finishCommit(token, inflight, remote, connection, outcome)
		default:
		}
		fence := make(chan struct{})
		coordinator.mu.Lock()
		terminal := false
		terminalResult := "trust_outcome_unknown"
		if coordinator.commitToken == token && coordinator.inflight == inflight {
			coordinator.phase = firstTrustDisabled
			coordinator.window = nil
			coordinator.finishCandidateRequestsExceptLocked(key, "stale_request", coordinator.now())
			coordinator.currentCandidate = nil
			coordinator.stopTimerLocked()
			if err := coordinator.setWaitingLocked(false); err != nil {
				terminalResult = "pairing_registration_failed"
			}
			coordinator.cancelRemoteLocked(remote, connection)
			coordinator.recordReplayLocked(key, request, terminalResult, coordinator.now())
			coordinator.commitFence = fence
			coordinator.inflight = nil
			close(inflight.done)
			terminal = true
		}
		coordinator.mu.Unlock()
		if terminal {
			coordinator.notifyTrustAdminProjection()
		}
		go func() {
			<-result
			close(fence)
		}()
		return terminalResult
	}
}

func (coordinator *firstTrustCoordinator) cancel(ctx context.Context, key, nonce string, connection, storeGeneration uint64) string {
	ctx = firstTrustContext(ctx)
	if ctx.Err() != nil {
		return "request_cancelled"
	}
	if !validFirstTrustKey(key) {
		return "invalid_idempotency_key"
	}
	request := firstTrustRequest{operation: "cancel", nonce: nonce, connection: connection, storeGeneration: storeGeneration}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	now := coordinator.now()
	coordinator.expireLocked(now)
	if result, ok := coordinator.replayLocked(key, request, now); ok {
		return result
	}
	if coordinator.activeKeyConflictLocked(key, request) {
		return "idempotency_conflict"
	}
	if coordinator.phase == firstTrustCommitting {
		return "commit_in_progress"
	}
	if coordinator.phase != firstTrustCandidatePending || coordinator.currentCandidate == nil {
		return "stale_request"
	}
	if coordinator.idempotencyCapacityLocked(key, 0) {
		return "idempotency_capacity"
	}
	if result := coordinator.bindCandidateRequestLocked(key, request); result != "" {
		return result
	}
	candidate := coordinator.currentCandidate
	if nonce != candidate.nonce || connection != candidate.connection || storeGeneration != candidate.storeGeneration {
		return "confirmation_mismatch"
	}
	result := "cancelled"
	coordinator.finishCandidateRequestsLocked(result, now)
	coordinator.cancelRemoteLocked(candidate.remote, candidate.connection)
	coordinator.currentCandidate = nil
	if coordinator.window != nil && now.Before(coordinator.window.deadline) {
		coordinator.phase = firstTrustOpenEmpty
		coordinator.scheduleExpiryLocked(coordinator.window.deadline)
	} else {
		if err := coordinator.closeWindowLocked("pairing_closed", now, false); err != nil {
			result = "pairing_registration_failed"
		}
	}
	coordinator.recordReplayLocked(key, request, result, now)
	return result
}

func (coordinator *firstTrustCoordinator) candidate() (string, string, time.Time, uint64, uint64, bool, bool) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	coordinator.expireLocked(coordinator.now())
	if coordinator.currentCandidate == nil || coordinator.phase != firstTrustCandidatePending {
		return "", "", time.Time{}, 0, 0, false, false
	}
	candidate := coordinator.currentCandidate
	return hex.EncodeToString(candidate.remote), candidate.nonce, candidate.expiresAt, candidate.connection, candidate.storeGeneration, candidate.shipID != "", true
}

func (coordinator *firstTrustCoordinator) state() string {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	coordinator.expireLocked(coordinator.now())
	if coordinator.pairingRegistrationFault {
		return "DISABLED"
	}
	switch coordinator.phase {
	case firstTrustPairingClosed:
		return "PAIRING_CLOSED"
	case firstTrustOpenEmpty:
		return "OPEN_EMPTY"
	case firstTrustCandidatePending:
		return "CANDIDATE_PENDING"
	case firstTrustCommitting:
		return "COMMITTING"
	default:
		return "DISABLED"
	}
}

func (coordinator *firstTrustCoordinator) trusted(remote []byte) bool {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	_, ok := coordinator.trustedRemotes[string(remote)]
	return ok
}

func (coordinator *firstTrustCoordinator) shutdown() error {
	if coordinator == nil {
		return nil
	}
	coordinator.mu.Lock()
	before := coordinator.captureTrustAdminProjectionLocked()
	coordinator.stopTimerLocked()
	coordinator.stopRetentionTimerLocked()
	if coordinator.currentCandidate != nil {
		coordinator.cancelRemoteLocked(coordinator.currentCandidate.remote, coordinator.currentCandidate.connection)
	}
	registrationErr := coordinator.setWaitingLocked(false)
	coordinator.phase = firstTrustDisabled
	coordinator.window = nil
	coordinator.currentCandidate = nil
	coordinator.trustedRemotes = make(map[string]string)
	coordinator.storeGeneration = 0
	coordinator.replays = make(map[string]firstTrustReplay)
	coordinator.retired = make(map[string]firstTrustRetired)
	coordinator.inflight = nil
	coordinator.commitFence = nil
	coordinator.reopening = false
	coordinator.recoveryOperation = nil
	coordinator.retryArms = nil
	coordinator.retryInflight = nil
	coordinator.effects = nil
	coordinator.unlockAndNotifyTrustAdminProjectionChange(before)
	return registrationErr
}

func (coordinator *firstTrustCoordinator) selectedFirstTrustGenerationLocked() uint64 {
	if coordinator.recoveryStore != nil {
		return coordinator.recoveryStore.SelectedGeneration()
	}
	if coordinator.store == nil {
		return 0
	}
	return coordinator.store.SelectedGeneration()
}

func (coordinator *firstTrustCoordinator) finishCommit(token uint64, inflight *firstTrustInflight, remote []byte, connection uint64, storeOutcome string) string {
	coordinator.mu.Lock()
	if coordinator.commitToken != token || coordinator.inflight != inflight {
		coordinator.mu.Unlock()
		return "trust_outcome_unknown"
	}

	result := "failed_closed_unchanged"
	coordinator.phase = firstTrustPairingClosed
	switch storeOutcome {
	case "commit_durable":
		result = "trusted"
		coordinator.trustedRemotes[string(remote)] = coordinator.currentCandidate.shipID
		coordinator.storeGeneration = coordinator.store.SelectedGeneration()
	case "commit_applied_maintenance_failed":
		result = "applied_reopen_required"
		coordinator.phase = firstTrustDisabled
	case "commit_durability_unknown":
		result = "trust_outcome_unknown"
		coordinator.phase = firstTrustDisabled
	case "commit_not_published", "validation_failed", "key_provider_unavailable", "key_material_unavailable", "maintenance_failed",
		"path_rejected", "filesystem_capability_unavailable", "permissions_rejected", "layout_rejected", "writer_busy",
		"lock_unavailable", "malformed_state", "io_failed":
		result = "failed_closed_unchanged"
	default:
		result = "trust_outcome_unknown"
		coordinator.phase = firstTrustDisabled
	}

	now := coordinator.now()
	coordinator.finishCandidateRequestsExceptLocked(inflight.key, "stale_request", now)
	coordinator.currentCandidate = nil
	coordinator.window = nil
	coordinator.stopTimerLocked()
	if err := coordinator.setWaitingLocked(false); err != nil {
		result = "pairing_registration_failed"
	}
	coordinator.recordReplayLocked(inflight.key, inflight.request, result, now)
	if result == "trusted" {
		coordinator.registerRemoteLocked(remote, connection)
	} else {
		coordinator.cancelRemoteLocked(remote, connection)
	}
	coordinator.inflight = nil
	close(inflight.done)
	coordinator.mu.Unlock()
	coordinator.notifyTrustAdminProjection()
	return result
}

func (coordinator *firstTrustCoordinator) bindCandidateRequestLocked(key string, request firstTrustRequest) string {
	if existing, ok := coordinator.currentCandidate.requests[key]; ok {
		if existing.equal(request) {
			return ""
		}
		return "idempotency_conflict"
	}
	if len(coordinator.currentCandidate.requests) >= firstTrustMaximumActiveKeys {
		return "idempotency_capacity"
	}
	coordinator.currentCandidate.requests[key] = request
	return ""
}

func (coordinator *firstTrustCoordinator) activeKeyConflictLocked(key string, request firstTrustRequest) bool {
	if coordinator.window != nil && coordinator.window.key == key {
		openRequest := firstTrustRequest{operation: "open", duration: coordinator.window.duration}
		return !openRequest.equal(request)
	}
	if coordinator.currentCandidate != nil {
		if existing, ok := coordinator.currentCandidate.requests[key]; ok {
			return !existing.equal(request)
		}
	}
	return false
}

func (coordinator *firstTrustCoordinator) idempotencyCapacityLocked(key string, reserve int) bool {
	if _, ok := coordinator.replays[key]; ok {
		return false
	}
	count := len(coordinator.replays)
	if coordinator.window != nil {
		if coordinator.window.key == key {
			return false
		}
		count++
	}
	if coordinator.currentCandidate != nil {
		if _, ok := coordinator.currentCandidate.requests[key]; ok {
			return false
		}
		count += len(coordinator.currentCandidate.requests)
	}
	return count+1+reserve > firstTrustMaximumIdempotency
}

func (coordinator *firstTrustCoordinator) replayLocked(key string, request firstTrustRequest, now time.Time) (string, bool) {
	coordinator.pruneReplaysLocked(now)
	entry, ok := coordinator.replays[key]
	if ok {
		if !entry.request.equal(request) {
			return "idempotency_conflict", true
		}
		return entry.result, true
	}
	if _, ok := coordinator.retired[key]; !ok {
		return "", false
	}
	return "stale_request", true
}

func (coordinator *firstTrustCoordinator) recordReplayLocked(key string, request firstTrustRequest, result string, now time.Time) {
	if !validFirstTrustKey(key) {
		return
	}
	coordinator.pruneReplaysLocked(now)
	coordinator.replaySequence++
	coordinator.replays[key] = firstTrustReplay{request: request, result: result, expiresAt: now.Add(firstTrustReplayTTL), sequence: coordinator.replaySequence}
	for len(coordinator.replays) > firstTrustMaximumReplayEntries {
		var oldestKey string
		var oldestSequence uint64
		for candidateKey, entry := range coordinator.replays {
			if oldestKey == "" || entry.sequence < oldestSequence || entry.sequence == oldestSequence && candidateKey < oldestKey {
				oldestKey = candidateKey
				oldestSequence = entry.sequence
			}
		}
		coordinator.retireReplayLocked(oldestKey, coordinator.replays[oldestKey], now)
		delete(coordinator.replays, oldestKey)
	}
	coordinator.scheduleRetentionExpiryLocked()
}

func (coordinator *firstTrustCoordinator) pruneReplaysLocked(now time.Time) {
	for key, entry := range coordinator.replays {
		if !now.Before(entry.expiresAt) {
			coordinator.retireReplayLocked(key, entry, now)
			delete(coordinator.replays, key)
		}
	}
	for key, entry := range coordinator.retired {
		if !now.Before(entry.expiresAt) {
			delete(coordinator.retired, key)
		}
	}
	coordinator.scheduleRetentionExpiryLocked()
}

func (coordinator *firstTrustCoordinator) retireReplayLocked(key string, replay firstTrustReplay, now time.Time) {
	expiresAt := replay.expiresAt.Add(firstTrustRetiredTTL)
	if !now.Before(expiresAt) {
		return
	}
	coordinator.retired[key] = firstTrustRetired{expiresAt: expiresAt, sequence: replay.sequence}
	for len(coordinator.retired) > firstTrustMaximumRetiredKeys {
		var oldestKey string
		var oldestSequence uint64
		for candidateKey, entry := range coordinator.retired {
			if oldestKey == "" || entry.sequence < oldestSequence || entry.sequence == oldestSequence && candidateKey < oldestKey {
				oldestKey = candidateKey
				oldestSequence = entry.sequence
			}
		}
		delete(coordinator.retired, oldestKey)
	}
}

func (coordinator *firstTrustCoordinator) scheduleRetentionExpiryLocked() {
	coordinator.stopRetentionTimerLocked()
	var deadline time.Time
	for _, entry := range coordinator.replays {
		if deadline.IsZero() || entry.expiresAt.Before(deadline) {
			deadline = entry.expiresAt
		}
	}
	for _, entry := range coordinator.retired {
		if deadline.IsZero() || entry.expiresAt.Before(deadline) {
			deadline = entry.expiresAt
		}
	}
	if deadline.IsZero() {
		return
	}
	coordinator.retentionToken++
	token := coordinator.retentionToken
	delay := deadline.Sub(coordinator.now())
	if delay < 0 {
		delay = 0
	}
	coordinator.retentionTimer = time.AfterFunc(delay, func() {
		coordinator.mu.Lock()
		defer coordinator.mu.Unlock()
		if coordinator.retentionToken != token {
			return
		}
		coordinator.retentionTimer = nil
		coordinator.pruneReplaysLocked(coordinator.now())
	})
}

func (coordinator *firstTrustCoordinator) stopRetentionTimerLocked() {
	coordinator.retentionToken++
	if coordinator.retentionTimer != nil {
		coordinator.retentionTimer.Stop()
		coordinator.retentionTimer = nil
	}
}

func (coordinator *firstTrustCoordinator) expireLocked(now time.Time) {
	coordinator.pruneReplaysLocked(now)
	if coordinator.phase == firstTrustCandidatePending && coordinator.currentCandidate != nil && !now.Before(coordinator.currentCandidate.expiresAt) {
		candidate := coordinator.currentCandidate
		coordinator.finishCandidateRequestsLocked("candidate_expired", now)
		coordinator.cancelRemoteLocked(candidate.remote, candidate.connection)
		coordinator.currentCandidate = nil
		if coordinator.window != nil && now.Before(coordinator.window.deadline) {
			coordinator.phase = firstTrustOpenEmpty
			coordinator.scheduleExpiryLocked(coordinator.window.deadline)
		} else {
			_ = coordinator.closeWindowLocked("pairing_closed", now, false)
		}
	}
	if coordinator.window != nil && !now.Before(coordinator.window.deadline) && coordinator.phase != firstTrustCommitting {
		_ = coordinator.closeWindowLocked("pairing_closed", now, true)
	}
}

func (coordinator *firstTrustCoordinator) closeWindowLocked(result string, now time.Time, cancelCandidate bool) error {
	if coordinator.window != nil {
		openRequest := firstTrustRequest{operation: "open", duration: coordinator.window.duration}
		coordinator.recordReplayLocked(coordinator.window.key, openRequest, result, now)
	}
	if cancelCandidate && coordinator.currentCandidate != nil {
		candidate := coordinator.currentCandidate
		coordinator.finishCandidateRequestsLocked(result, now)
		coordinator.cancelRemoteLocked(candidate.remote, candidate.connection)
	}
	coordinator.window = nil
	coordinator.currentCandidate = nil
	coordinator.phase = firstTrustPairingClosed
	coordinator.stopTimerLocked()
	registrationErr := coordinator.setWaitingLocked(false)
	return registrationErr
}

func (coordinator *firstTrustCoordinator) scheduleExpiryLocked(deadline time.Time) {
	coordinator.stopTimerLocked()
	coordinator.timerToken++
	token := coordinator.timerToken
	delay := deadline.Sub(coordinator.now())
	if delay < 0 {
		delay = 0
	}
	coordinator.timer = time.AfterFunc(delay, func() {
		coordinator.mu.Lock()
		defer coordinator.mu.Unlock()
		if coordinator.timerToken != token {
			return
		}
		coordinator.timer = nil
		coordinator.expireLocked(deadline)
	})
}

func (coordinator *firstTrustCoordinator) stopTimerLocked() {
	coordinator.timerToken++
	if coordinator.timer != nil {
		coordinator.timer.Stop()
		coordinator.timer = nil
	}
}

func (coordinator *firstTrustCoordinator) finishCandidateRequestsLocked(result string, now time.Time) {
	if coordinator.currentCandidate == nil {
		return
	}
	for key, request := range coordinator.currentCandidate.requests {
		coordinator.recordReplayLocked(key, request, result, now)
	}
}

func (coordinator *firstTrustCoordinator) finishCandidateRequestsExceptLocked(excludedKey, result string, now time.Time) {
	if coordinator.currentCandidate == nil {
		return
	}
	for key, request := range coordinator.currentCandidate.requests {
		if key != excludedKey {
			coordinator.recordReplayLocked(key, request, result, now)
		}
	}
}

func (coordinator *firstTrustCoordinator) openStateLocked() string {
	if coordinator.phase == firstTrustCandidatePending {
		return "candidate_pending"
	}
	return "open_empty"
}

func (coordinator *firstTrustCoordinator) setWaitingLocked(value bool) error {
	if coordinator.pairingRegistrationFault {
		coordinator.failPairingRegistrationLocked()
		return errFirstTrustPairingRegistrationFailed
	}
	if coordinator.effects == nil {
		return nil
	}
	if coordinator.pairingRegistrationKnown && coordinator.pairingRegistrationEnabled == value {
		return nil
	}
	if err := coordinator.effects.setWaiting(value); err != nil {
		coordinator.failPairingRegistrationLocked()
		return err
	}
	coordinator.pairingRegistrationKnown = true
	coordinator.pairingRegistrationEnabled = value
	return nil
}

func (coordinator *firstTrustCoordinator) pairingRegistrationInitialized(value bool) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.pairingRegistrationFault {
		return
	}
	coordinator.pairingRegistrationKnown = true
	coordinator.pairingRegistrationEnabled = value
}

func (coordinator *firstTrustCoordinator) pairingRegistrationFailed() {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	coordinator.failPairingRegistrationLocked()
}

func (coordinator *firstTrustCoordinator) preparePairingRegistrationReopen() string {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.pairingRegistrationFault {
		coordinator.failPairingRegistrationLocked()
		return "pairing_registration_failed"
	}
	if coordinator.reopening || coordinator.phase != firstTrustDisabled || coordinator.recoveryStore == nil {
		return ""
	}
	if err := coordinator.setWaitingLocked(false); err != nil {
		coordinator.reopening = false
		return "pairing_registration_failed"
	}
	return ""
}

func (coordinator *firstTrustCoordinator) failPairingRegistrationLocked() {
	coordinator.pairingRegistrationFault = true
	coordinator.phase = firstTrustDisabled
	coordinator.window = nil
	coordinator.currentCandidate = nil
	coordinator.reopening = false
	coordinator.recovery = "QUARANTINED"
	coordinator.recoveryReasonCode = "PAIRING_REGISTRATION_FAILED"
	coordinator.trustedRemotes = make(map[string]string)
	coordinator.stopTimerLocked()
}

func (coordinator *firstTrustCoordinator) cancelRemoteLocked(remote []byte, connection uint64) {
	if coordinator.effects != nil {
		coordinator.pendingEffects = append(coordinator.pendingEffects, firstTrustEffect{
			kind:       firstTrustEffectCancel,
			target:     coordinator.effects,
			remote:     bytes.Clone(remote),
			connection: connection,
		})
	}
}

func (coordinator *firstTrustCoordinator) registerRemoteLocked(remote []byte, connection uint64) {
	if coordinator.effects != nil {
		coordinator.pendingEffects = append(coordinator.pendingEffects, firstTrustEffect{
			kind:       firstTrustEffectRegister,
			target:     coordinator.effects,
			remote:     bytes.Clone(remote),
			connection: connection,
		})
	}
}

func (coordinator *firstTrustCoordinator) drainEffects() {
	for {
		coordinator.effectMu.Lock()
		if len(coordinator.effectQueue) == 0 {
			coordinator.effectDraining = false
			coordinator.effectMu.Unlock()
			return
		}
		effect := coordinator.effectQueue[0]
		coordinator.effectQueue[0] = firstTrustEffect{}
		coordinator.effectQueue = coordinator.effectQueue[1:]
		coordinator.effectMu.Unlock()

		switch effect.kind {
		case firstTrustEffectCancel:
			effect.target.cancelRemote(effect.remote, effect.connection)
		case firstTrustEffectRegister:
			if effect.target.connectionAlive(effect.remote, effect.connection) {
				effect.target.registerRemoteSKI(effect.remote, effect.connection)
			}
		}
	}
}

func constantTimeFingerprintMatch(value string, remote []byte) bool {
	if len(value) != 40 || len(remote) != 20 {
		return false
	}
	for _, character := range []byte(value) {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(remote) {
		return false
	}
	return subtle.ConstantTimeCompare(decoded, remote) == 1
}

func validFirstTrustKey(value string) bool {
	return len(value) > 0 && len(value) <= firstTrustMaximumKeyBytes
}

func firstTrustContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
