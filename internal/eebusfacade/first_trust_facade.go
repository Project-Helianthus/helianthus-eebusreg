package eebusfacade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"

	eebusapi "github.com/Project-Helianthus/helianthus-eebus-go/api"
	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
)

const firstTrustMaximumConnections = 128

type firstTrustService interface {
	SetAutoAccept(bool)
	RegisterRemoteSKI(string)
	CancelPairingWithSKI(string)
	UserIsAbleToApproveOrCancelPairingRequests(bool)
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

type firstTrustEventSink interface {
	admit([]byte, uint64) string
	serviceShipIDUpdate([]byte, uint64, string) string
	connectionClosed([]byte, uint64) string
}

type firstTrustConnection struct {
	generation      uint64
	retryScope      [32]byte
	shipID          string
	attemptClass    string
	active          bool
	connected       bool
	attemptStarted  bool
	retryAdmitted   bool
	failureRecorded bool
	cancelled       bool
	blocked         bool
	registered      bool
}

type firstTrustFacade struct {
	mu        sync.Mutex
	attemptMu sync.Mutex

	service     firstTrustService
	coordinator firstTrustEventSink
	next        uint64
	connections map[string]*firstTrustConnection
	withdrawals map[string]chan struct{}
}

var _ eebusapi.ServiceReaderInterface = (*firstTrustFacade)(nil)

func newFirstTrustFacade(service firstTrustService, coordinator firstTrustEventSink) *firstTrustFacade {
	facade := &firstTrustFacade{
		service:     service,
		coordinator: coordinator,
		connections: make(map[string]*firstTrustConnection),
		withdrawals: make(map[string]chan struct{}),
	}
	if service != nil {
		service.SetAutoAccept(false)
		service.UserIsAbleToApproveOrCancelPairingRequests(false)
	}
	return facade
}

func (facade *firstTrustFacade) RemoteSKIConnected(_ eebusapi.ServiceInterface, ski string) {
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
	}
	facade.mu.Unlock()
	if stale != nil && facade.coordinator != nil {
		facade.coordinator.connectionClosed(remote, stale.generation)
	}
	if cancel {
		facade.cancelBySKI(normalized)
	}
}

func (facade *firstTrustFacade) RemoteSKIDisconnected(_ eebusapi.ServiceInterface, ski string) {
	remote, normalized, ok := decodeFirstTrustSKI(ski)
	if !ok {
		return
	}
	facade.mu.Lock()
	if acknowledgment := facade.withdrawals[normalized]; acknowledgment != nil {
		delete(facade.withdrawals, normalized)
		close(acknowledgment)
	}
	connection := facade.connections[normalized]
	if connection != nil && !connection.blocked {
		connection.active = false
		connection.cancelled = true
		connection.blocked = true
		connection.shipID = ""
	} else {
		connection = nil
	}
	facade.mu.Unlock()
	if connection != nil && facade.coordinator != nil {
		facade.coordinator.connectionClosed(remote, connection.generation)
	}
}

func (*firstTrustFacade) VisibleRemoteServicesUpdated(eebusapi.ServiceInterface, []shipapi.RemoteService) {
}

func (facade *firstTrustFacade) ServiceShipIDUpdate(ski string, shipID string) {
	remote, normalized, ok := decodeFirstTrustSKI(ski)
	if !ok || shipID == "" {
		return
	}
	connection := facade.beginAttempt(remote, normalized, false)
	facade.mu.Lock()
	connection = facade.connections[normalized]
	if connection == nil || connection.cancelled || connection.blocked {
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
	remote, normalized, ok := decodeFirstTrustSKI(ski)
	if !ok || detail == nil {
		return
	}
	switch detail.State() {
	case shipapi.ConnectionStateQueued, shipapi.ConnectionStateInitiated, shipapi.ConnectionStateInProgress:
		facade.beginAttempt(remote, normalized, false)
	case shipapi.ConnectionStateReceivedPairingRequest:
		facade.handlePairingRequest(remote, normalized)
	case shipapi.ConnectionStateError, shipapi.ConnectionStateRemoteDeniedTrust:
		facade.handlePairingFailure(remote, normalized)
	case shipapi.ConnectionStateTrusted, shipapi.ConnectionStateCompleted:
		facade.handlePairingSuccess(normalized)
	}
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
	facade.mu.Unlock()
	if connection.attemptClass == "pairing_authorized" && shipID != "" {
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
	scope := connection.retryScope
	facade.mu.Unlock()

	if retry, ok := facade.retrySink(); ok {
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
		connection.retryAdmitted = true
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
	if connection == nil || !connection.retryAdmitted || connection.failureRecorded {
		facade.mu.Unlock()
		return
	}
	connection.failureRecorded = true
	generation := connection.generation
	scope := connection.retryScope
	facade.mu.Unlock()

	retry, ok := facade.retrySink()
	if !ok {
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

func (facade *firstTrustFacade) handlePairingSuccess(normalized string) {
	facade.mu.Lock()
	connection := facade.connections[normalized]
	if connection == nil {
		facade.mu.Unlock()
		return
	}
	scope := connection.retryScope
	admitted := connection.retryAdmitted
	connection.retryAdmitted = false
	facade.mu.Unlock()
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

func (facade *firstTrustFacade) setWaiting(value bool) {
	if facade.service != nil {
		facade.service.UserIsAbleToApproveOrCancelPairingRequests(value)
	}
}

func (facade *firstTrustFacade) cancelRemote(remote []byte, generation uint64) {
	facade.cancelGeneration(remote, generation)
}

func (facade *firstTrustFacade) connectionAlive(remote []byte, generation uint64) bool {
	normalized := hex.EncodeToString(remote)
	facade.mu.Lock()
	defer facade.mu.Unlock()
	connection := facade.connections[normalized]
	return connection != nil && connection.generation == generation && connection.active && !connection.cancelled && !connection.blocked
}

func (facade *firstTrustFacade) registerRemoteSKI(remote []byte, generation uint64) {
	normalized := hex.EncodeToString(remote)
	facade.mu.Lock()
	connection := facade.connections[normalized]
	if connection == nil || connection.generation != generation || !connection.active || connection.cancelled || connection.blocked || connection.registered {
		facade.mu.Unlock()
		return
	}
	connection.registered = true
	if facade.service != nil {
		facade.service.RegisterRemoteSKI(normalized)
	}
	facade.mu.Unlock()
}

func (facade *firstTrustFacade) disconnectRemote(remote []byte) (acknowledged <-chan struct{}, started bool) {
	if facade.service == nil || len(remote) != 20 {
		return nil, false
	}
	service, ok := facade.service.(firstTrustWithdrawalService)
	if !ok {
		return nil, false
	}
	normalized := hex.EncodeToString(remote)
	facade.mu.Lock()
	if facade.withdrawals[normalized] != nil {
		facade.mu.Unlock()
		return nil, false
	}
	acknowledgment := make(chan struct{})
	facade.withdrawals[normalized] = acknowledgment
	facade.mu.Unlock()
	defer func() {
		if recover() != nil {
			facade.mu.Lock()
			if facade.withdrawals[normalized] == acknowledgment {
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
	if facade.withdrawals[normalized] == acknowledgment {
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
	connection.cancelled = true
	connection.active = false
	connection.blocked = true
	connection.shipID = ""
	facade.mu.Unlock()
	facade.cancelBySKI(normalized)
}

func (facade *firstTrustFacade) cancelBySKI(normalized string) {
	if facade.service != nil {
		facade.service.CancelPairingWithSKI(normalized)
	}
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
		generation: facade.next,
		active:     true,
		connected:  connected,
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
