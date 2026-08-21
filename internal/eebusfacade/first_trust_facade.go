package eebusfacade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	eebusapi "github.com/Project-Helianthus/helianthus-eebus-go/api"
	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
	shipmodel "github.com/Project-Helianthus/helianthus-ship-go/model"
)

const firstTrustMaximumConnections = 128

type firstTrustService interface {
	SetAutoAccept(bool)
	RegisterRemoteSKI(string)
	CancelPairingWithSKI(string)
	SetPairingRegistration(bool) error
}

type firstTrustCandidateService interface {
	QueuePairingCandidate(string, string) error
}

type firstTrustWithdrawalService interface {
	DisconnectSKI(string, string)
	UnregisterRemoteSKI(string)
}

type firstTrustRetryEventSink interface {
	retryRuntimeEnabled() bool
	admitRetry(context.Context, [32]byte) string
	recordRetryFailure(context.Context, [32]byte) string
	checkpointRetry(context.Context, [32]byte) string
	completeRetry([32]byte)
}

type firstTrustAttemptAuthorizer interface {
	authorizeRuntimeAttempt([]byte) string
}

type firstTrustOutgoingAttemptRetryOwner interface {
	outgoingAttemptOwnsRetry([]byte) bool
}

type firstTrustEventSink interface {
	admit([]byte, uint64) string
	serviceShipIDUpdate([]byte, uint64, string) string
	connectionClosed([]byte, uint64) string
}

type firstTrustCompletionSink interface {
	connectionCompleted([]byte, uint64) string
}

type firstTrustPairingRegistrationSink interface {
	pairingRegistrationInitialized(bool)
	pairingRegistrationFailed()
}

type firstTrustConnection struct {
	generation      uint64
	pairingEpoch    uint64
	retryScope      [32]byte
	shipID          string
	attemptClass    string
	tlsBound        bool
	active          bool
	connected       bool
	attemptStarted  bool
	retryAdmitted   bool
	outgoingRetry   bool
	failureRecorded bool
	cancelled       bool
	blocked         bool
	registered      bool
	transient       bool
	unregistered    bool
}

type firstTrustWithdrawal struct {
	acknowledgment chan struct{}
	connection     *firstTrustConnection
	generation     uint64
}

type firstTrustActiveAction struct {
	actionID        string
	target          string
	kind            string
	state           string
	outcome         string
	retryable       bool
	expiresAt       time.Time
	pinSupplied     bool
	generation      uint64
	generationFloor uint64
}

type firstTrustFacade struct {
	mu         sync.Mutex
	attemptMu  sync.Mutex
	callbackMu sync.Mutex

	service             firstTrustService
	candidateService    firstTrustCandidateService
	coordinator         firstTrustEventSink
	next                uint64
	pairingEpoch        uint64
	pairingRegistration bool
	connections         map[string]*firstTrustConnection
	withdrawals         map[string]*firstTrustWithdrawal
	remoteMetadata      map[string]operatorAdminV1BridgeRawMetadata
	activeAction        *firstTrustActiveAction

	pairingRegistrationFault bool
}

var _ eebusapi.ServiceReaderInterface = (*firstTrustFacade)(nil)

func newFirstTrustFacade(service firstTrustService, coordinator firstTrustEventSink) (*firstTrustFacade, error) {
	facade := &firstTrustFacade{
		service:        service,
		coordinator:    coordinator,
		connections:    make(map[string]*firstTrustConnection),
		withdrawals:    make(map[string]*firstTrustWithdrawal),
		remoteMetadata: make(map[string]operatorAdminV1BridgeRawMetadata),
	}
	if service != nil {
		service.SetAutoAccept(false)
		if err := service.SetPairingRegistration(false); err != nil {
			if sink, ok := coordinator.(firstTrustPairingRegistrationSink); ok {
				sink.pairingRegistrationFailed()
			}
			return nil, err
		}
		if sink, ok := coordinator.(firstTrustPairingRegistrationSink); ok {
			sink.pairingRegistrationInitialized(false)
		}
	}
	if candidateService, ok := service.(firstTrustCandidateService); ok {
		facade.candidateService = candidateService
		if sink, ok := coordinator.(firstTrustCandidateSelectionSink); ok {
			sink.configureCandidateSelection(facade)
		}
	}
	return facade, nil
}

func (facade *firstTrustFacade) RemoteSKIConnected(_ eebusapi.ServiceInterface, ski string) {
	facade.callbackMu.Lock()
	defer facade.callbackMu.Unlock()
	remote, normalized, ok := decodeFirstTrustSKI(ski)
	if !ok {
		return
	}
	if facade.beginAttempt(remote, normalized, false) == nil {
		facade.cancelBySKI(normalized)
		return
	}
	var stale *firstTrustConnection
	cancel := false
	bindCandidateTLS := false
	var generation uint64
	facade.mu.Lock()
	connection := facade.connections[normalized]
	switch {
	case connection == nil:
		cancel = true
	case connection.blocked || connection.cancelled:
		cancel = true
	case connection.connected:
		connection.active = false
		connection.cancelled = true
		connection.blocked = true
		connection.shipID = ""
		stale = connection
		cancel = true
	default:
		connection.connected = true
		connection.active = true
		generation = connection.generation
		bindCandidateTLS = connection.attemptClass == "pairing_authorized" && !connection.tlsBound
	}
	facade.mu.Unlock()
	if stale != nil && facade.coordinator != nil {
		facade.coordinator.connectionClosed(remote, stale.generation)
	}
	if !cancel && bindCandidateTLS {
		cancel = !facade.bindAttemptTLSUnderCallbackLock(remote, normalized, generation)
	}
	if cancel {
		facade.cancelBySKI(normalized)
	}
}

func (facade *firstTrustFacade) outgoingAttemptTLSBound(remote []byte, generation uint64) bool {
	if facade == nil || len(remote) != 20 || generation == 0 {
		return false
	}
	facade.callbackMu.Lock()
	defer facade.callbackMu.Unlock()
	normalized := hex.EncodeToString(remote)
	return facade.bindAttemptTLSUnderCallbackLock(remote, normalized, generation)
}

func (facade *firstTrustFacade) bindAttemptTLSUnderCallbackLock(remote []byte, normalized string, generation uint64) bool {
	facade.mu.Lock()
	connection := facade.connections[normalized]
	if connection == nil || connection.generation != generation || connection.attemptClass != "pairing_authorized" ||
		!connection.active || connection.cancelled || connection.blocked {
		facade.mu.Unlock()
		return false
	}
	if connection.tlsBound {
		facade.mu.Unlock()
		return true
	}
	facade.mu.Unlock()

	sink, ok := facade.coordinator.(firstTrustTLSBindingSink)
	if !ok || sink.remoteSKIConnected(remote, generation) != "tls_bound" {
		return false
	}
	facade.mu.Lock()
	defer facade.mu.Unlock()
	connection = facade.connections[normalized]
	if connection == nil || connection.generation != generation || !connection.active || connection.cancelled || connection.blocked {
		return false
	}
	connection.tlsBound = true
	return true
}

func (facade *firstTrustFacade) outgoingAttemptTerminated(remote []byte, generation uint64) {
	if facade == nil || len(remote) != 20 || generation == 0 {
		return
	}
	facade.callbackMu.Lock()
	normalized := hex.EncodeToString(remote)
	facade.mu.Lock()
	connection := facade.connections[normalized]
	if connection == nil || connection.generation != generation || connection.attemptClass != "pairing_authorized" ||
		connection.failureRecorded || connection.cancelled || connection.blocked {
		facade.mu.Unlock()
		facade.callbackMu.Unlock()
		return
	}
	effectConnection := connection
	wasActive := connection.active
	wasConnected := connection.connected
	shipID := connection.shipID
	connection.failureRecorded = true
	connection.retryAdmitted = false
	connection.outgoingRetry = false
	connection.active = false
	connection.cancelled = true
	connection.blocked = true
	connection.shipID = ""
	facade.mu.Unlock()
	facade.callbackMu.Unlock()

	if facade.coordinator != nil && facade.coordinator.connectionClosed(remote, generation) == "commit_in_progress" {
		facade.callbackMu.Lock()
		facade.mu.Lock()
		connection = facade.connections[normalized]
		if connection == effectConnection && connection.generation == generation && connection.connected == wasConnected {
			connection.active = wasActive
			connection.cancelled = false
			connection.blocked = false
			connection.shipID = shipID
		}
		facade.mu.Unlock()
		facade.callbackMu.Unlock()
		return
	}

	facade.callbackMu.Lock()
	facade.mu.Lock()
	connection = facade.connections[normalized]
	if connection != effectConnection || connection.generation != generation {
		facade.mu.Unlock()
		facade.callbackMu.Unlock()
		return
	}
	connected := connection.connected
	facade.mu.Unlock()
	facade.cancelBySKI(normalized)
	if connected {
		facade.disconnectCancelledBySKI(normalized)
	}

	facade.mu.Lock()
	connection = facade.connections[normalized]
	if connection == effectConnection && connection.generation == generation && !connection.connected {
		delete(facade.connections, normalized)
	}
	facade.mu.Unlock()
	facade.callbackMu.Unlock()
}

func (facade *firstTrustFacade) RemoteSKIDisconnected(_ eebusapi.ServiceInterface, ski string) {
	remote, normalized, ok := decodeFirstTrustSKI(ski)
	if !ok {
		return
	}
	if facade.acknowledgeBlockedDisconnect(normalized) {
		return
	}
	facade.callbackMu.Lock()
	defer facade.callbackMu.Unlock()
	if facade.acknowledgeBlockedDisconnect(normalized) {
		return
	}
	var retryScope [32]byte
	releaseRetry := false
	facade.mu.Lock()
	if withdrawal := facade.withdrawals[normalized]; withdrawal != nil {
		connection := facade.connections[normalized]
		if connection != withdrawal.connection || connection == nil || connection.generation != withdrawal.generation {
			facade.mu.Unlock()
			return
		}
		delete(facade.withdrawals, normalized)
		close(withdrawal.acknowledgment)
	}
	connection := facade.connections[normalized]
	if connection != nil {
		facade.completeOperatorAdminV1ActiveActionLocked(normalized, connection.generation, "disconnected", true)
	}
	if connection != nil && (connection.registered && !connection.transient || connection.attemptClass == "reconnect_authorized") {
		retryScope = connection.retryScope
		releaseRetry = connection.retryAdmitted
		connection.retryAdmitted = false
		delete(facade.connections, normalized)
		connection = nil
	} else if connection != nil && !connection.blocked {
		connection.active = false
		connection.cancelled = true
		connection.blocked = true
		connection.shipID = ""
	} else {
		connection = nil
	}
	facade.mu.Unlock()
	if releaseRetry {
		if retry, enabled := facade.retrySink(); enabled {
			retry.completeRetry(retryScope)
		}
	}
	if connection != nil && facade.coordinator != nil {
		facade.coordinator.connectionClosed(remote, connection.generation)
	}
}

func (facade *firstTrustFacade) acknowledgeBlockedDisconnect(normalized string) bool {
	facade.mu.Lock()
	defer facade.mu.Unlock()
	connection := facade.connections[normalized]
	if connection == nil || !connection.cancelled || !connection.blocked {
		return false
	}
	if withdrawal := facade.withdrawals[normalized]; withdrawal != nil &&
		withdrawal.connection == connection && withdrawal.generation == connection.generation {
		delete(facade.withdrawals, normalized)
		close(withdrawal.acknowledgment)
	}
	connection.connected = false
	if !facade.pairingRegistration || connection.pairingEpoch != facade.pairingEpoch {
		delete(facade.connections, normalized)
	}
	return true
}

func (facade *firstTrustFacade) VisibleRemoteServicesUpdated(_ eebusapi.ServiceInterface, services []shipapi.RemoteService) {
	if facade == nil || len(services) > operatorAdminV1BridgeMaximumRows {
		return
	}
	facade.mu.Lock()
	defer facade.mu.Unlock()
	if facade.remoteMetadata == nil {
		facade.remoteMetadata = make(map[string]operatorAdminV1BridgeRawMetadata)
	}
	for _, service := range services {
		ski := strings.ToLower(strings.TrimSpace(service.Ski))
		if !validOperatorAdminV1BridgeSKI(ski) {
			continue
		}
		if _, exists := facade.remoteMetadata[ski]; !exists && len(facade.remoteMetadata) >= operatorAdminV1BridgeMaximumRows {
			continue
		}
		metadata := facade.remoteMetadata[ski]
		mergeOperatorAdminV1BridgeMetadata(&metadata, operatorAdminV1BridgeRawMetadata{
			name: strings.TrimSpace(service.Name), identifier: strings.TrimSpace(service.Identifier),
			brand: strings.TrimSpace(service.Brand), typeName: strings.TrimSpace(service.Type),
			model: strings.TrimSpace(service.Model),
		})
		if validOperatorAdminV1BridgeMetadata(metadata) {
			facade.remoteMetadata[ski] = metadata
		}
	}
}

func (facade *firstTrustFacade) VisiblePairingCandidatesUpdated(_ eebusapi.ServiceInterface, candidates []shipapi.PairingCandidateRef) {
	if sink, ok := facade.coordinator.(firstTrustCandidateSelectionSink); ok {
		sink.visiblePairingCandidatesUpdated(candidates)
	}
}

func (facade *firstTrustFacade) ServiceShipIDUpdate(ski string, shipID string) {
	remote, normalized, ok := decodeFirstTrustSKI(ski)
	if !ok || shipID == "" {
		return
	}
	if facade.connectionCallbackBlocked(normalized) {
		return
	}
	facade.callbackMu.Lock()
	defer facade.callbackMu.Unlock()
	if facade.connectionCallbackBlocked(normalized) {
		return
	}
	connection := facade.beginAttempt(remote, normalized, false)
	facade.mu.Lock()
	connection = facade.connections[normalized]
	if connection == nil || connection.cancelled || connection.blocked {
		facade.mu.Unlock()
		return
	}
	if connection.transient && (!connection.active || !connection.connected) {
		facade.mu.Unlock()
		return
	}
	connection.shipID = shipID
	generation := connection.generation
	facade.mu.Unlock()
	if facade.coordinator != nil {
		facade.coordinator.serviceShipIDUpdate(remote, generation, shipID)
	}
}

func (facade *firstTrustFacade) ServicePairingDetailUpdate(ski string, detail *shipapi.ConnectionStateDetail) {
	facade.servicePairingDetailUpdateForGeneration(ski, 0, detail)
}

func (facade *firstTrustFacade) servicePairingDetailUpdateForGeneration(
	ski string,
	generation uint64,
	detail *shipapi.ConnectionStateDetail,
) {
	remote, normalized, ok := decodeFirstTrustSKI(ski)
	if !ok || detail == nil {
		return
	}
	state := detail.State()
	if facade.connectionCallbackBlocked(normalized) {
		facade.rejectBlockedPairingCallback(normalized, state)
		return
	}
	facade.callbackMu.Lock()
	if facade.connectionCallbackBlocked(normalized) {
		facade.callbackMu.Unlock()
		if firstTrustPairingCallbackRequiresCancellation(state) {
			facade.cancelBySKI(normalized)
		}
		return
	}
	defer facade.callbackMu.Unlock()
	if state == shipapi.ConnectionStatePin {
		facade.beginAttempt(remote, normalized, false)
	}
	if generation != 0 && !facade.operatorAdminV1ConnectionGenerationCurrent(normalized, generation) {
		return
	}
	facade.recordOperatorAdminV1PairingDetail(normalized, generation, detail)
	switch state {
	case shipapi.ConnectionStateQueued, shipapi.ConnectionStateInitiated, shipapi.ConnectionStateInProgress:
		facade.beginAttempt(remote, normalized, false)
	case shipapi.ConnectionStateReceivedPairingRequest:
		facade.handlePairingRequest(remote, normalized)
	case shipapi.ConnectionStateError, shipapi.ConnectionStateRemoteDeniedTrust:
		facade.handlePairingFailure(remote, normalized)
	case shipapi.ConnectionStateTrusted:
		facade.handlePairingSuccess(remote, normalized, false)
	case shipapi.ConnectionStateCompleted:
		facade.handlePairingSuccess(remote, normalized, true)
	}
}

func (facade *firstTrustFacade) operatorAdminV1ConnectionGenerationCurrent(target string, generation uint64) bool {
	facade.mu.Lock()
	defer facade.mu.Unlock()
	connection := facade.connections[target]
	return connection != nil && connection.generation == generation
}

func (facade *firstTrustFacade) rejectBlockedPairingCallback(normalized string, state shipapi.ConnectionState) {
	if !firstTrustPairingCallbackRequiresCancellation(state) || !facade.callbackMu.TryLock() {
		return
	}
	facade.callbackMu.Unlock()
	facade.cancelBySKI(normalized)
}

func firstTrustPairingCallbackRequiresCancellation(state shipapi.ConnectionState) bool {
	switch state {
	case shipapi.ConnectionStateQueued, shipapi.ConnectionStateInitiated, shipapi.ConnectionStateInProgress,
		shipapi.ConnectionStateReceivedPairingRequest:
		return true
	default:
		return false
	}
}

func (facade *firstTrustFacade) connectionCallbackBlocked(normalized string) bool {
	facade.mu.Lock()
	defer facade.mu.Unlock()
	connection := facade.connections[normalized]
	return connection != nil && (connection.cancelled || connection.blocked)
}

func (facade *firstTrustFacade) handlePairingRequest(remote []byte, normalized string) {
	connection := facade.beginAttempt(remote, normalized, false)
	facade.mu.Lock()
	connection = facade.connections[normalized]
	if connection == nil || connection.cancelled || connection.blocked {
		facade.mu.Unlock()
		facade.cancelBySKI(normalized)
		return
	}
	connection.active = true
	shipID := connection.shipID
	generation := connection.generation
	attemptClass := connection.attemptClass
	tlsBound := connection.tlsBound
	facade.mu.Unlock()
	if attemptClass == "pairing_authorized" && !tlsBound &&
		!facade.bindAttemptTLSUnderCallbackLock(remote, normalized, generation) {
		facade.cancelBySKI(normalized)
		return
	}
	if attemptClass == "pairing_authorized" && shipID != "" {
		facade.coordinator.serviceShipIDUpdate(remote, generation, shipID)
	}
}

func (facade *firstTrustFacade) beginAttempt(remote []byte, normalized string, connected bool) *firstTrustConnection {
	facade.attemptMu.Lock()
	defer facade.attemptMu.Unlock()
	facade.mu.Lock()
	connection := facade.connections[normalized]
	if connection != nil && connection.attemptStarted {
		if connected {
			connection.connected = true
		}
		facade.mu.Unlock()
		return connection
	}
	facade.mu.Unlock()

	authorizer, ok := facade.coordinator.(firstTrustAttemptAuthorizer)
	if !ok {
		return nil
	}
	attemptClass := authorizer.authorizeRuntimeAttempt(remote)
	if attemptClass != "pairing_authorized" && attemptClass != "reconnect_authorized" {
		return nil
	}

	facade.mu.Lock()
	connection = facade.connections[normalized]
	if connection == nil {
		connection = facade.newConnectionLocked(normalized, connected)
	}
	if connection == nil || connection.cancelled || connection.blocked {
		facade.mu.Unlock()
		return nil
	}
	if connection.attemptStarted {
		if connected {
			connection.connected = true
		}
		facade.mu.Unlock()
		return connection
	}
	connection.attemptStarted = true
	connection.attemptClass = attemptClass
	connection.retryScope = firstTrustRuntimeRetryScope(normalized)
	generation := connection.generation
	facade.bindOperatorAdminV1ActiveActionLocked(normalized, generation)
	scope := connection.retryScope
	facade.mu.Unlock()

	outgoingOwnsRetry := false
	if owner, ok := facade.coordinator.(firstTrustOutgoingAttemptRetryOwner); ok {
		outgoingOwnsRetry = attemptClass == "pairing_authorized" && owner.outgoingAttemptOwnsRetry(remote)
	}
	if retry, ok := facade.retrySink(); ok && !outgoingOwnsRetry {
		if result := retry.admitRetry(context.Background(), scope); result != "retry_admitted" {
			facade.discardRetryGeneration(remote, normalized, generation)
			return nil
		}
	}
	if attemptClass == "pairing_authorized" {
		result := facade.coordinator.admit(remote, generation)
		if result != "candidate_pending" && result != "commit_in_progress" {
			if retry, ok := facade.retrySink(); ok {
				retry.completeRetry(scope)
			}
			facade.discardRetryGeneration(remote, normalized, generation)
			return nil
		}
	}
	facade.mu.Lock()
	connection = facade.connections[normalized]
	if connection != nil && connection.generation == generation {
		connection.retryAdmitted = !outgoingOwnsRetry
		connection.outgoingRetry = outgoingOwnsRetry
	}
	facade.mu.Unlock()
	return connection
}

func (facade *firstTrustFacade) discardRetryGeneration(remote []byte, normalized string, generation uint64) {
	facade.mu.Lock()
	connection := facade.connections[normalized]
	if connection == nil || connection.generation != generation {
		facade.mu.Unlock()
		return
	}
	delete(facade.connections, normalized)
	facade.mu.Unlock()
	if facade.coordinator != nil {
		facade.coordinator.connectionClosed(remote, generation)
	}
	facade.cancelBySKI(normalized)
}

func (facade *firstTrustFacade) handlePairingFailure(remote []byte, normalized string) {
	facade.mu.Lock()
	connection := facade.connections[normalized]
	if connection == nil || (!connection.retryAdmitted && !connection.outgoingRetry) || connection.failureRecorded {
		facade.mu.Unlock()
		return
	}
	connection.failureRecorded = true
	generation := connection.generation
	scope := connection.retryScope
	outgoingRetry := connection.outgoingRetry
	connection.retryAdmitted = false
	connection.outgoingRetry = false
	facade.mu.Unlock()

	if outgoingRetry {
		if facade.coordinator != nil {
			facade.coordinator.connectionClosed(remote, generation)
		}
		facade.cancelGeneration(remote, generation)
		return
	}
	retry, ok := facade.retrySink()
	if !ok {
		if facade.coordinator != nil {
			facade.coordinator.connectionClosed(remote, generation)
		}
		facade.cancelGeneration(remote, generation)
		return
	}
	result := retry.recordRetryFailure(context.Background(), scope)
	if result == "backoff_active" {
		if checkpoint := retry.checkpointRetry(context.Background(), scope); checkpoint != "checkpoint_durable" {
			facade.cancelGeneration(remote, generation)
			return
		}
	} else if result != "admin_hold" {
		facade.cancelGeneration(remote, generation)
		return
	}
	if facade.coordinator != nil {
		facade.coordinator.connectionClosed(remote, generation)
	}
	facade.mu.Lock()
	delete(facade.connections, normalized)
	facade.mu.Unlock()
	facade.cancelBySKI(normalized)
}

func (facade *firstTrustFacade) handlePairingSuccess(remote []byte, normalized string, completed bool) {
	facade.mu.Lock()
	connection := facade.connections[normalized]
	if connection == nil || connection.cancelled || connection.blocked {
		facade.mu.Unlock()
		return
	}
	scope := connection.retryScope
	admitted := connection.retryAdmitted
	generation := connection.generation
	pairingAuthorized := connection.attemptClass == "pairing_authorized"
	live := connection.active && connection.connected
	connection.retryAdmitted = false
	facade.mu.Unlock()
	if completed && pairingAuthorized && live {
		if sink, ok := facade.coordinator.(firstTrustCompletionSink); ok {
			sink.connectionCompleted(remote, generation)
		}
	}
	if admitted {
		if retry, ok := facade.retrySink(); ok {
			retry.completeRetry(scope)
		}
	}
}

func (facade *firstTrustFacade) retrySink() (firstTrustRetryEventSink, bool) {
	retry, ok := facade.coordinator.(firstTrustRetryEventSink)
	return retry, ok && retry.retryRuntimeEnabled()
}

func (facade *firstTrustFacade) operatorAdminV1ConnectedSnapshot() []operatorAdminV1BridgeRawConnected {
	if facade == nil {
		return nil
	}
	facade.mu.Lock()
	defer facade.mu.Unlock()
	connected := make([]operatorAdminV1BridgeRawConnected, 0, len(facade.connections))
	for normalized, connection := range facade.connections {
		if connection == nil || !connection.active || !connection.connected || connection.cancelled || connection.blocked ||
			!validOperatorAdminV1BridgeSKI(normalized) {
			continue
		}
		trustState := "untrusted"
		if connection.registered {
			trustState = "trusted"
			if connection.transient {
				trustState = "transient"
			}
		}
		connected = append(connected, operatorAdminV1BridgeRawConnected{
			ski: normalized, trustState: trustState, connectionState: "connected", shipID: connection.shipID,
			metadata: facade.remoteMetadata[normalized],
		})
	}
	return connected
}

func (facade *firstTrustFacade) armOperatorAdminV1ActiveAction(
	actionID,
	target string,
	pinSupplied bool,
	expiresAt time.Time,
) bool {
	if facade == nil || !validOperatorAdminV1BridgeActionID(actionID) || !validOperatorAdminV1BridgeSKI(target) || expiresAt.IsZero() {
		return false
	}
	facade.mu.Lock()
	defer facade.mu.Unlock()
	if facade.activeAction != nil && facade.activeAction.actionID == actionID {
		return false
	}
	generation := uint64(0)
	generationFloor := uint64(0)
	if connection := facade.connections[target]; connection != nil && connection.active && connection.attemptStarted &&
		!connection.cancelled && !connection.blocked && connection.pairingEpoch == facade.pairingEpoch {
		generation = connection.generation
		generationFloor = connection.generation
	} else {
		if facade.next == ^uint64(0) {
			return false
		}
		generationFloor = facade.next + 1
	}
	facade.activeAction = &firstTrustActiveAction{
		actionID: actionID, target: target, kind: "connect", state: "pending",
		expiresAt: expiresAt, pinSupplied: pinSupplied, generation: generation, generationFloor: generationFloor,
	}
	return true
}

func (facade *firstTrustFacade) operatorAdminV1ActiveActionID(now time.Time) string {
	if facade == nil {
		return ""
	}
	facade.mu.Lock()
	defer facade.mu.Unlock()
	facade.pruneOperatorAdminV1ActiveActionLocked(now)
	if facade.activeAction == nil {
		return ""
	}
	return facade.activeAction.actionID
}

func (facade *firstTrustFacade) operatorAdminV1ActiveActionSnapshot(now time.Time) *operatorAdminV1BridgeActiveAction {
	if facade == nil {
		return nil
	}
	facade.mu.Lock()
	defer facade.mu.Unlock()
	facade.pruneOperatorAdminV1ActiveActionLocked(now)
	if facade.activeAction == nil {
		return nil
	}
	action := facade.activeAction
	result := &operatorAdminV1BridgeActiveAction{
		actionID: action.actionID, kind: action.kind, state: action.state,
		outcome: action.outcome, retryable: action.retryable, expiresAt: action.expiresAt,
	}
	return result
}

func (facade *firstTrustFacade) clearOperatorAdminV1ActiveAction(actionID string) {
	if facade == nil {
		return
	}
	facade.mu.Lock()
	if facade.activeAction != nil && (actionID == "" || facade.activeAction.actionID == actionID) {
		facade.activeAction = nil
	}
	facade.mu.Unlock()
}

func (facade *firstTrustFacade) clearOperatorAdminV1ActiveActionGenerationLocked(target string, generation uint64) {
	if facade.activeAction != nil && facade.activeAction.target == target && facade.activeAction.generation == generation {
		facade.activeAction = nil
	}
}

func (facade *firstTrustFacade) completeOperatorAdminV1ActiveActionLocked(
	target string,
	generation uint64,
	outcome string,
	retryable bool,
) {
	action := facade.activeAction
	if action == nil || action.target != target || action.generation != generation || action.state == "terminal" {
		return
	}
	action.state = "terminal"
	action.outcome = outcome
	action.retryable = retryable
}

func (facade *firstTrustFacade) pruneOperatorAdminV1ActiveActionLocked(now time.Time) {
	if facade.activeAction != nil && (!now.IsZero() && !now.Before(facade.activeAction.expiresAt)) {
		facade.activeAction = nil
	}
}

func (facade *firstTrustFacade) bindOperatorAdminV1ActiveActionLocked(target string, generation uint64) {
	action := facade.activeAction
	if action == nil || action.target != target || generation < action.generationFloor {
		return
	}
	if action.generation == 0 {
		action.generation = generation
		return
	}
	if action.generation != generation {
		facade.activeAction = nil
	}
}

func (facade *firstTrustFacade) recordOperatorAdminV1PairingDetail(
	normalized string,
	generation uint64,
	detail *shipapi.ConnectionStateDetail,
) {
	if facade == nil || detail == nil {
		return
	}
	facade.mu.Lock()
	defer facade.mu.Unlock()
	connection := facade.connections[normalized]
	if connection == nil || generation != 0 && connection.generation != generation {
		return
	}
	facade.bindOperatorAdminV1ActiveActionLocked(normalized, connection.generation)
	action := facade.activeAction
	if action == nil || action.target != normalized || action.generation != connection.generation || action.state == "terminal" {
		return
	}
	outcome, retryable, terminal := operatorAdminV1ActionOutcomeForPairingDetail(detail, action.pinSupplied)
	if !terminal {
		return
	}
	action.state = "terminal"
	action.outcome = outcome
	action.retryable = retryable
}

func operatorAdminV1ActionOutcomeForPairingDetail(
	detail *shipapi.ConnectionStateDetail,
	pinSupplied bool,
) (string, bool, bool) {
	if detail == nil {
		return "", false, false
	}
	state := detail.State()
	if state == shipapi.ConnectionStateCompleted {
		return "connection_completed", false, true
	}
	pin := detail.PINHandshakeDetail()
	if pin != nil && pin.Category != nil {
		switch *pin.Category {
		case shipmodel.PINCategoryRequired:
			if !pinSupplied && (pin.Phase == shipmodel.PINPhaseWaitingPeer || pin.Phase == shipmodel.PINPhaseFailed) {
				return "pin_required", pin.Retryable, true
			}
		case shipmodel.PINCategoryOptional:
			if !pinSupplied && pin.Phase == shipmodel.PINPhaseRestricted {
				return "pin_optional", pin.Retryable, true
			}
		case shipmodel.PINCategoryBusy:
			return "pin_busy", pin.Retryable, true
		case shipmodel.PINCategoryRejected:
			if pin.Phase == shipmodel.PINPhaseFailed || state == shipapi.ConnectionStateError {
				return "pin_rejected", pin.Retryable, true
			}
		case shipmodel.PINCategoryUnavailable:
			if pin.Phase == shipmodel.PINPhaseFailed || state == shipapi.ConnectionStateError {
				return "pin_unavailable", pin.Retryable, true
			}
		case shipmodel.PINCategoryProtocol:
			if pin.Phase == shipmodel.PINPhaseFailed || state == shipapi.ConnectionStateError {
				return "pin_protocol_error", pin.Retryable, true
			}
		}
	}
	if state == shipapi.ConnectionStateRemoteDeniedTrust {
		return "trust_denied", false, true
	}
	if state == shipapi.ConnectionStateError {
		err := detail.Error()
		var timeout interface{ Timeout() bool }
		if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timeout) && timeout.Timeout() {
			return "attempt_timeout", true, true
		}
		return "unknown_state", false, true
	}
	return "", false, false
}

func validOperatorAdminV1BridgeActionID(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}

func (facade *firstTrustFacade) operatorAdminV1MetadataSnapshot() map[string]operatorAdminV1BridgeRawMetadata {
	if facade == nil {
		return nil
	}
	facade.mu.Lock()
	defer facade.mu.Unlock()
	result := make(map[string]operatorAdminV1BridgeRawMetadata, len(facade.remoteMetadata))
	for ski, metadata := range facade.remoteMetadata {
		result[ski] = metadata
	}
	return result
}

func (facade *firstTrustFacade) setWaiting(value bool) error {
	if facade.service != nil {
		if err := facade.service.SetPairingRegistration(value); err != nil {
			facade.mu.Lock()
			facade.pairingRegistrationFault = true
			facade.mu.Unlock()
			return err
		}
	}
	facade.mu.Lock()
	facade.pairingRegistration = value
	if value {
		facade.pairingEpoch++
		if facade.pairingEpoch == 0 {
			facade.pairingEpoch++
		}
	} else {
		facade.activeAction = nil
		for normalized, connection := range facade.connections {
			if connection.cancelled && connection.blocked && !connection.connected {
				delete(facade.connections, normalized)
			}
		}
	}
	facade.mu.Unlock()
	return nil
}

func (facade *firstTrustFacade) cancelRemote(remote []byte, generation uint64) {
	facade.cancelGeneration(remote, generation)
}

func (facade *firstTrustFacade) cancelCandidate(remote []byte) {
	if len(remote) != 20 {
		return
	}
	facade.cancelBySKI(hex.EncodeToString(remote))
}

func (facade *firstTrustFacade) queuePairingCandidate(candidateRef, expectedSKI string) error {
	if facade == nil || facade.candidateService == nil {
		return errors.New("pairing candidate queue is unavailable")
	}
	return facade.candidateService.QueuePairingCandidate(candidateRef, expectedSKI)
}

func (facade *firstTrustFacade) connectionAlive(remote []byte, generation uint64) bool {
	normalized := hex.EncodeToString(remote)
	facade.mu.Lock()
	defer facade.mu.Unlock()
	connection := facade.connections[normalized]
	return !facade.pairingRegistrationFault && connection != nil && connection.generation == generation && connection.active && !connection.cancelled && !connection.blocked
}

func (facade *firstTrustFacade) registerRemoteSKI(remote []byte, generation uint64) {
	normalized := hex.EncodeToString(remote)
	facade.mu.Lock()
	connection := facade.connections[normalized]
	if facade.pairingRegistrationFault || connection == nil || connection.generation != generation || !connection.active || connection.cancelled || connection.blocked || connection.registered {
		facade.mu.Unlock()
		return
	}
	connection.registered = true
	service := facade.service
	facade.mu.Unlock()
	if service != nil {
		service.RegisterRemoteSKI(normalized)
	}
}

func (facade *firstTrustFacade) registerTransientRemoteSKI(remote []byte, generation uint64) bool {
	normalized := hex.EncodeToString(remote)
	facade.mu.Lock()
	connection := facade.connections[normalized]
	if facade.pairingRegistrationFault || connection == nil || connection.generation != generation ||
		!connection.active || connection.cancelled || connection.blocked || connection.registered {
		facade.mu.Unlock()
		return false
	}
	connection.registered = true
	connection.transient = true
	connection.unregistered = false
	connection.shipID = ""
	service := facade.service
	facade.mu.Unlock()
	if service != nil {
		service.RegisterRemoteSKI(normalized)
		return true
	}
	return false
}

func (facade *firstTrustFacade) unregisterTransientRemoteSKI(remote []byte, generation uint64) {
	normalized := hex.EncodeToString(remote)
	facade.mu.Lock()
	connection := facade.connections[normalized]
	if connection == nil || connection.generation != generation || connection.unregistered {
		facade.mu.Unlock()
		return
	}
	connection.unregistered = true
	connection.registered = false
	connection.transient = false
	service, ok := facade.service.(firstTrustWithdrawalService)
	facade.mu.Unlock()
	if !ok {
		return
	}
	defer func() {
		_ = recover()
	}()
	service.UnregisterRemoteSKI(normalized)
}

func (facade *firstTrustFacade) finalizeTransientRemoteSKI(remote []byte, generation uint64) {
	normalized := hex.EncodeToString(remote)
	facade.mu.Lock()
	defer facade.mu.Unlock()
	connection := facade.connections[normalized]
	if connection == nil || connection.generation != generation || connection.unregistered || !connection.registered {
		return
	}
	connection.transient = false
}

func (facade *firstTrustFacade) disconnectRemote(remote []byte) (acknowledged <-chan struct{}, started bool) {
	if len(remote) != 20 {
		return nil, false
	}
	normalized := hex.EncodeToString(remote)
	facade.mu.Lock()
	if facade.withdrawals[normalized] != nil {
		facade.mu.Unlock()
		return nil, false
	}
	connection := facade.connections[normalized]
	if connection == nil || !connection.connected {
		facade.mu.Unlock()
		acknowledgment := make(chan struct{})
		close(acknowledgment)
		return acknowledgment, true
	}
	service, ok := facade.service.(firstTrustWithdrawalService)
	if !ok {
		facade.mu.Unlock()
		return nil, false
	}
	acknowledgment := make(chan struct{})
	withdrawal := &firstTrustWithdrawal{
		acknowledgment: acknowledgment,
		connection:     connection,
		generation:     connection.generation,
	}
	facade.withdrawals[normalized] = withdrawal
	facade.mu.Unlock()
	defer func() {
		if recover() != nil {
			facade.mu.Lock()
			if facade.withdrawals[normalized] == withdrawal {
				delete(facade.withdrawals, normalized)
			}
			facade.mu.Unlock()
			acknowledged = nil
			started = false
		}
	}()
	service.DisconnectSKI(normalized, "revoked")
	return acknowledgment, true
}

func (facade *firstTrustFacade) cancelDisconnect(remote []byte, acknowledgment <-chan struct{}) {
	if len(remote) != 20 || acknowledgment == nil {
		return
	}
	normalized := hex.EncodeToString(remote)
	facade.mu.Lock()
	withdrawal := facade.withdrawals[normalized]
	if withdrawal != nil && withdrawal.acknowledgment == acknowledgment {
		delete(facade.withdrawals, normalized)
	}
	facade.mu.Unlock()
}

func (facade *firstTrustFacade) unregisterRemote(remote []byte) (completed bool) {
	if facade.service == nil || len(remote) != 20 {
		return false
	}
	service, ok := facade.service.(firstTrustWithdrawalService)
	if !ok {
		return false
	}
	defer func() {
		if recover() != nil {
			completed = false
		}
	}()
	service.UnregisterRemoteSKI(hex.EncodeToString(remote))
	return true
}

func (facade *firstTrustFacade) cancelGeneration(remote []byte, generation uint64) {
	normalized := hex.EncodeToString(remote)
	facade.mu.Lock()
	connection := facade.connections[normalized]
	if connection == nil || connection.generation != generation || connection.cancelled {
		facade.mu.Unlock()
		return
	}
	facade.clearOperatorAdminV1ActiveActionGenerationLocked(normalized, generation)
	connection.cancelled = true
	connection.active = false
	connection.blocked = true
	connection.shipID = ""
	connected := connection.connected
	if !connected {
		delete(facade.connections, normalized)
	}
	facade.mu.Unlock()
	facade.cancelBySKI(normalized)
	if connected {
		facade.disconnectCancelledBySKI(normalized)
	}
}

func (facade *firstTrustFacade) cancelBySKI(normalized string) {
	if facade.service != nil {
		facade.service.CancelPairingWithSKI(normalized)
	}
}

func (facade *firstTrustFacade) disconnectCancelledBySKI(normalized string) {
	service, ok := facade.service.(firstTrustWithdrawalService)
	if !ok {
		return
	}
	defer func() {
		_ = recover()
	}()
	service.DisconnectSKI(normalized, "pairing cancelled")
}

func (facade *firstTrustFacade) newConnectionLocked(normalized string, connected bool) *firstTrustConnection {
	if len(facade.connections) >= firstTrustMaximumConnections {
		return nil
	}
	facade.next++
	if facade.next == 0 {
		facade.next++
	}
	connection := &firstTrustConnection{
		generation:   facade.next,
		pairingEpoch: facade.pairingEpoch,
		active:       true,
		connected:    connected,
	}
	facade.connections[normalized] = connection
	return connection
}

func firstTrustRuntimeRetryScope(normalized string) [32]byte {
	return sha256.Sum256([]byte("helianthus:first-trust:runtime-retry:v1:" + normalized))
}

func decodeFirstTrustSKI(value string) ([]byte, string, bool) {
	if len(value) != 40 {
		return nil, "", false
	}
	for _, character := range []byte(value) {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return nil, "", false
		}
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 20 {
		return nil, "", false
	}
	return decoded, value, true
}
