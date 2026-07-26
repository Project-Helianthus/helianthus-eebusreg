package eebusfacade

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
	shipmodel "github.com/Project-Helianthus/helianthus-ship-go/model"
)

var errOutgoingAttemptDenied = errors.New("outgoing attempt denied")

const outgoingAttemptFallbackSettleDelay = 25 * time.Millisecond

type firstTrustOutgoingAttemptLifecycle interface {
	RemoteSKIDisconnected(string)
	ServicePairingDetailUpdate(string, *shipapi.ConnectionStateDetail)
}

type firstTrustOutgoingAttemptTLSLifecycle interface {
	outgoingAttemptTLSBound([]byte, uint64) bool
}

type firstTrustOutgoingAttemptTerminalLifecycle interface {
	outgoingAttemptTerminated([]byte, uint64)
}

type firstTrustOutgoingAttemptBridge struct {
	coordinator *firstTrustCoordinator

	mu                sync.RWMutex
	lifecycle         firstTrustOutgoingAttemptLifecycle
	tlsLifecycle      firstTrustOutgoingAttemptTLSLifecycle
	terminalLifecycle firstTrustOutgoingAttemptTerminalLifecycle
	observer          firstTrustOutgoingAttemptObserver

	attemptMu         sync.Mutex
	pendingFailures   map[string]firstTrustPendingOutgoingFailure
	handshakeObserved map[[32]byte]bool
}

type firstTrustOutgoingAttemptObserver interface {
	prepared(firstTrustOutgoingAttemptMetadata, string, context.Context)
	authorized(firstTrustOutgoingAttemptMetadata, context.Context)
}

type firstTrustPendingOutgoingFailure struct {
	metadata firstTrustOutgoingAttemptMetadata
	deadline time.Duration
	timer    firstTrustOutgoingAttemptTimer
}

type runtimeOutgoingAttemptHandle struct {
	owner  *firstTrustOutgoingAttemptBridge
	handle *firstTrustOutgoingAttemptHandle
}

var _ shipapi.OutgoingAttemptGate = (*firstTrustOutgoingAttemptBridge)(nil)
var _ shipapi.OutgoingAttemptHubReaderInterface = (*firstTrustOutgoingAttemptBridge)(nil)
var _ shipapi.OutgoingAttemptHandle = (*runtimeOutgoingAttemptHandle)(nil)

func newFirstTrustOutgoingAttemptBridge(resources *runtimeFirstTrustResources) *firstTrustOutgoingAttemptBridge {
	var coordinator *firstTrustCoordinator
	if resources != nil {
		coordinator = resources.coordinator
	}
	return &firstTrustOutgoingAttemptBridge{
		coordinator:       coordinator,
		pendingFailures:   make(map[string]firstTrustPendingOutgoingFailure),
		handshakeObserved: make(map[[32]byte]bool),
	}
}

func (bridge *firstTrustOutgoingAttemptBridge) bindLifecycle(value any) {
	if bridge == nil {
		return
	}
	lifecycle, ok := value.(firstTrustOutgoingAttemptLifecycle)
	bridge.mu.Lock()
	if ok {
		bridge.lifecycle = lifecycle
	} else {
		bridge.lifecycle = nil
	}
	bridge.mu.Unlock()
}

func (bridge *firstTrustOutgoingAttemptBridge) bindTLSLifecycle(lifecycle firstTrustOutgoingAttemptTLSLifecycle) {
	if bridge == nil {
		return
	}
	terminal, _ := lifecycle.(firstTrustOutgoingAttemptTerminalLifecycle)
	bridge.mu.Lock()
	bridge.tlsLifecycle = lifecycle
	bridge.terminalLifecycle = terminal
	bridge.mu.Unlock()
}

func (bridge *firstTrustOutgoingAttemptBridge) Prepare(request shipapi.OutgoingAttemptRequest) (shipapi.OutgoingAttemptHandle, error) {
	if bridge == nil || bridge.coordinator == nil {
		return nil, errOutgoingAttemptDenied
	}
	remote, normalized, ok := decodeFirstTrustSKI(strings.ToLower(strings.TrimSpace(request.RemoteSKI)))
	if !ok || normalized != strings.ToLower(strings.TrimSpace(request.RemoteSKI)) {
		return nil, errOutgoingAttemptDenied
	}
	prepared := firstTrustOutgoingAttemptRequest{
		remoteSKI: remote,
		endpoint:  firstTrustOutgoingAttemptEndpoint{host: strings.TrimSpace(request.Endpoint.Host), port: request.Endpoint.Port},
		path:      request.Path,
	}
	unlock := bridge.coordinator.lockOutgoingAttemptLane(remote)
	defer unlock()
	failed, hasFailed := bridge.pendingFailure(normalized)
	var failedMetadata *firstTrustOutgoingAttemptMetadata
	if hasFailed {
		failedMetadata = &failed.metadata
	}
	handle, outcome := bridge.coordinator.prepareOutgoingAttemptLocked(context.Background(), prepared, failedMetadata)
	if outcome != "attempt_reserved" || handle == nil {
		return nil, errOutgoingAttemptDenied
	}
	if hasFailed {
		bridge.clearPendingFailure(normalized, failed.metadata)
	}
	bridge.observePrepared(handle.metadata, prepared.path, handle.context)
	return &runtimeOutgoingAttemptHandle{owner: bridge, handle: handle}, nil
}

func (bridge *firstTrustOutgoingAttemptBridge) AuthorizeLaunch(handle shipapi.OutgoingAttemptHandle) (shipapi.OutgoingAttemptPermit, error) {
	denied := shipapi.OutgoingAttemptPermit{
		Decision: shipapi.OutgoingAttemptDecisionDeny,
		Reason:   shipapi.OutgoingAttemptReasonStaleHandle,
	}
	runtimeHandle, ok := handle.(*runtimeOutgoingAttemptHandle)
	if bridge == nil || bridge.coordinator == nil || !ok || runtimeHandle == nil || runtimeHandle.owner != bridge || runtimeHandle.handle == nil {
		return denied, nil
	}
	permit, outcome := bridge.coordinator.authorizeOutgoingAttempt(context.Background(), runtimeHandle.handle)
	if outcome != "attempt_permitted" || permit.decision != "PERMIT" {
		if permit.reason == "POLICY_DENIED" {
			denied.Reason = shipapi.OutgoingAttemptReasonPolicyDenied
		}
		return denied, nil
	}
	bridge.observeAuthorized(permit.metadata, permit.context)
	return shipapi.OutgoingAttemptPermit{
		Decision: shipapi.OutgoingAttemptDecisionPermit,
		Reason:   shipapi.OutgoingAttemptReasonAuthorized,
		Metadata: runtimeOutgoingAttemptMetadataToSHIP(permit.metadata),
		Context:  permit.context,
	}, nil
}

func (bridge *firstTrustOutgoingAttemptBridge) bindObserver(observer firstTrustOutgoingAttemptObserver) {
	if bridge == nil {
		return
	}
	bridge.mu.Lock()
	bridge.observer = observer
	bridge.mu.Unlock()
}

func (bridge *firstTrustOutgoingAttemptBridge) observePrepared(
	metadata firstTrustOutgoingAttemptMetadata,
	path string,
	attemptContext context.Context,
) {
	bridge.mu.RLock()
	observer := bridge.observer
	bridge.mu.RUnlock()
	if observer != nil {
		observer.prepared(metadata, path, attemptContext)
	}
}

func (bridge *firstTrustOutgoingAttemptBridge) observeAuthorized(
	metadata firstTrustOutgoingAttemptMetadata,
	attemptContext context.Context,
) {
	bridge.mu.RLock()
	observer := bridge.observer
	bridge.mu.RUnlock()
	if observer != nil {
		observer.authorized(metadata, attemptContext)
	}
}

func (bridge *firstTrustOutgoingAttemptBridge) AbortPrepared(handle shipapi.OutgoingAttemptHandle) (shipapi.OutgoingAttemptAbortResult, error) {
	runtimeHandle, ok := handle.(*runtimeOutgoingAttemptHandle)
	if bridge == nil || bridge.coordinator == nil || !ok || runtimeHandle == nil || runtimeHandle.owner != bridge || runtimeHandle.handle == nil {
		return shipapi.OutgoingAttemptAbortStaleNoOp, nil
	}
	if outcome := bridge.coordinator.abortPreparedOutgoingAttempt(context.Background(), runtimeHandle.handle); outcome == "attempt_aborted" {
		return shipapi.OutgoingAttemptAbortConsumed, nil
	}
	return shipapi.OutgoingAttemptAbortStaleNoOp, nil
}

func (bridge *firstTrustOutgoingAttemptBridge) OutgoingAttemptConnectionClosed(
	remoteSKI string,
	complete bool,
	metadata shipapi.OutgoingAttemptMetadata,
) {
	if bridge == nil || bridge.coordinator == nil {
		return
	}
	converted, ok := runtimeOutgoingAttemptMetadataFromSHIP(metadata)
	if !ok {
		return
	}
	remote, normalized, ok := decodeFirstTrustSKI(strings.ToLower(strings.TrimSpace(remoteSKI)))
	if !ok || normalized != strings.ToLower(strings.TrimSpace(remoteSKI)) {
		return
	}
	unlock := bridge.coordinator.lockOutgoingAttemptLane(remote)
	defer unlock()
	if bridge.finishSuccessfulOutgoingAttemptClose(normalized, remote, converted) {
		return
	}
	if !bridge.coordinator.outgoingAttemptCallbackExactLocked(converted, remote) {
		return
	}
	if !complete && !bridge.attemptObservedHandshake(converted) {
		if !bridge.markPendingFailure(normalized, remote, converted) {
			return
		}
		bridge.mutateLifecycle(func(lifecycle firstTrustOutgoingAttemptLifecycle) {
			lifecycle.RemoteSKIDisconnected(normalized)
		})
		return
	}
	result := bridge.coordinator.completeOutgoingAttemptLocked(context.Background(), converted, remote, complete)
	if !firstTrustOutgoingAttemptCompletionDurable(result) {
		return
	}
	if result == "attempt_succeeded" {
		bridge.finishSuccessfulOutgoingAttemptClose(normalized, remote, converted)
		return
	}
	bridge.clearPendingFailure(normalized, converted)
	bridge.clearHandshakeObservation(converted)
	bridge.mutateLifecycle(func(lifecycle firstTrustOutgoingAttemptLifecycle) {
		lifecycle.RemoteSKIDisconnected(normalized)
	})
}

func (bridge *firstTrustOutgoingAttemptBridge) OutgoingAttemptHandshakeStateUpdate(
	remoteSKI string,
	state shipmodel.ShipState,
	metadata shipapi.OutgoingAttemptMetadata,
) {
	if bridge == nil || bridge.coordinator == nil {
		return
	}
	converted, ok := runtimeOutgoingAttemptMetadataFromSHIP(metadata)
	if !ok {
		return
	}
	remote, normalized, ok := decodeFirstTrustSKI(strings.ToLower(strings.TrimSpace(remoteSKI)))
	if !ok || normalized != strings.ToLower(strings.TrimSpace(remoteSKI)) {
		return
	}
	stateName := "in_progress"
	if state.State == shipmodel.SmeStateComplete {
		stateName = "complete"
	} else if state.State == shipmodel.SmeStateError || state.Error != nil {
		stateName = "error"
	}
	unlock := bridge.coordinator.lockOutgoingAttemptLane(remote)
	defer unlock()
	candidateConnection, exact := bridge.coordinator.outgoingAttemptCallbackExactConnectionLocked(converted, remote)
	if !exact {
		return
	}
	if stateName != "error" && candidateConnection != 0 {
		bridge.mutateTLSLifecycle(func(lifecycle firstTrustOutgoingAttemptTLSLifecycle) {
			lifecycle.outgoingAttemptTLSBound(remote, candidateConnection)
		})
	}
	bridge.recordHandshakeObservation(converted)
	if stateName == "error" {
		result := bridge.coordinator.completeOutgoingAttemptLocked(context.Background(), converted, remote, false)
		if !firstTrustOutgoingAttemptCompletionDurable(result) {
			return
		}
		bridge.clearPendingFailure(normalized, converted)
		bridge.clearHandshakeObservation(converted)
		if candidateConnection != 0 {
			bridge.mutateTerminalLifecycle(func(lifecycle firstTrustOutgoingAttemptTerminalLifecycle) {
				lifecycle.outgoingAttemptTerminated(remote, candidateConnection)
			})
		}
	}
	bridge.mutateLifecycle(func(lifecycle firstTrustOutgoingAttemptLifecycle) {
		lifecycle.ServicePairingDetailUpdate(normalized, runtimeOutgoingAttemptPairingDetail(state))
	})
	if stateName == "complete" {
		result := bridge.coordinator.completeOutgoingAttemptLocked(context.Background(), converted, remote, true)
		if firstTrustOutgoingAttemptCompletionDurable(result) || result == "failure_state_failed_closed" {
			bridge.clearPendingFailure(normalized, converted)
			bridge.clearHandshakeObservation(converted)
		}
	}
}

func (bridge *firstTrustOutgoingAttemptBridge) finishSuccessfulOutgoingAttemptClose(
	normalized string,
	remote []byte,
	metadata firstTrustOutgoingAttemptMetadata,
) bool {
	candidateConnection, consumed := bridge.coordinator.consumeSuccessfulOutgoingAttemptLocked(metadata, remote)
	if !consumed {
		return false
	}
	bridge.clearPendingFailure(normalized, metadata)
	bridge.clearHandshakeObservation(metadata)
	if candidateConnection != 0 {
		bridge.mutateTerminalLifecycle(func(lifecycle firstTrustOutgoingAttemptTerminalLifecycle) {
			lifecycle.outgoingAttemptTerminated(remote, candidateConnection)
		})
	}
	bridge.mutateLifecycle(func(lifecycle firstTrustOutgoingAttemptLifecycle) {
		lifecycle.RemoteSKIDisconnected(normalized)
	})
	return true
}

func (bridge *firstTrustOutgoingAttemptBridge) mutateLifecycle(mutate func(firstTrustOutgoingAttemptLifecycle)) {
	bridge.mu.RLock()
	lifecycle := bridge.lifecycle
	bridge.mu.RUnlock()
	if lifecycle != nil {
		mutate(lifecycle)
	}
}

func (bridge *firstTrustOutgoingAttemptBridge) mutateTLSLifecycle(mutate func(firstTrustOutgoingAttemptTLSLifecycle)) {
	bridge.mu.RLock()
	lifecycle := bridge.tlsLifecycle
	bridge.mu.RUnlock()
	if lifecycle != nil {
		mutate(lifecycle)
	}
}

func (bridge *firstTrustOutgoingAttemptBridge) mutateTerminalLifecycle(mutate func(firstTrustOutgoingAttemptTerminalLifecycle)) {
	bridge.mu.RLock()
	lifecycle := bridge.terminalLifecycle
	bridge.mu.RUnlock()
	if lifecycle != nil {
		mutate(lifecycle)
	}
}

func (bridge *firstTrustOutgoingAttemptBridge) pendingFailure(remoteSKI string) (firstTrustPendingOutgoingFailure, bool) {
	bridge.attemptMu.Lock()
	defer bridge.attemptMu.Unlock()
	pending, ok := bridge.pendingFailures[remoteSKI]
	return pending, ok
}

func (bridge *firstTrustOutgoingAttemptBridge) markPendingFailure(
	remoteSKI string,
	remote []byte,
	metadata firstTrustOutgoingAttemptMetadata,
) bool {
	bridge.attemptMu.Lock()
	defer bridge.attemptMu.Unlock()
	if _, ok := bridge.pendingFailures[remoteSKI]; ok {
		return false
	}
	remoteCopy := bytes.Clone(remote)
	deadline := firstTrustSaturatingDurationAdd(bridge.coordinator.monotonicNow(), outgoingAttemptFallbackSettleDelay)
	pending := firstTrustPendingOutgoingFailure{metadata: metadata, deadline: deadline}
	pending.timer = bridge.coordinator.outgoingAttemptSchedule(outgoingAttemptFallbackSettleDelay, func() {
		bridge.settlePendingFailure(remoteSKI, remoteCopy, metadata)
	})
	if pending.timer == nil {
		return false
	}
	bridge.pendingFailures[remoteSKI] = pending
	return true
}

func (bridge *firstTrustOutgoingAttemptBridge) clearPendingFailure(
	remoteSKI string,
	metadata firstTrustOutgoingAttemptMetadata,
) {
	bridge.attemptMu.Lock()
	pending, ok := bridge.pendingFailures[remoteSKI]
	if ok && pending.metadata == metadata {
		delete(bridge.pendingFailures, remoteSKI)
	}
	bridge.attemptMu.Unlock()
	if ok && pending.metadata == metadata && pending.timer != nil {
		pending.timer.Stop()
	}
}

func (bridge *firstTrustOutgoingAttemptBridge) settlePendingFailure(
	remoteSKI string,
	remote []byte,
	metadata firstTrustOutgoingAttemptMetadata,
) {
	if bridge == nil || bridge.coordinator == nil {
		return
	}
	unlock := bridge.coordinator.lockOutgoingAttemptLane(remote)
	defer unlock()
	pending, ok := bridge.pendingFailure(remoteSKI)
	if !ok || pending.metadata != metadata {
		return
	}
	if remaining := pending.deadline - bridge.coordinator.monotonicNow(); remaining > 0 {
		bridge.attemptMu.Lock()
		current, exact := bridge.pendingFailures[remoteSKI]
		if exact && current.metadata == metadata && current.deadline == pending.deadline {
			current.timer = bridge.coordinator.outgoingAttemptSchedule(remaining, func() {
				bridge.settlePendingFailure(remoteSKI, bytes.Clone(remote), metadata)
			})
			if current.timer == nil {
				delete(bridge.pendingFailures, remoteSKI)
			} else {
				bridge.pendingFailures[remoteSKI] = current
			}
		}
		bridge.attemptMu.Unlock()
		return
	}
	if !bridge.coordinator.outgoingAttemptCallbackExactLocked(metadata, remote) {
		bridge.clearPendingFailure(remoteSKI, metadata)
		bridge.clearHandshakeObservation(metadata)
		return
	}
	result := bridge.coordinator.completeOutgoingAttemptLocked(context.Background(), metadata, remote, false)
	if firstTrustOutgoingAttemptCompletionDurable(result) || result == "stale_attempt" {
		bridge.clearPendingFailure(remoteSKI, metadata)
		bridge.clearHandshakeObservation(metadata)
	}
}

func (bridge *firstTrustOutgoingAttemptBridge) shutdown() error {
	if bridge == nil || bridge.coordinator == nil {
		return nil
	}
	bridge.attemptMu.Lock()
	for key, pending := range bridge.pendingFailures {
		if pending.timer != nil {
			pending.timer.Stop()
		}
		delete(bridge.pendingFailures, key)
	}
	bridge.handshakeObserved = make(map[[32]byte]bool)
	bridge.attemptMu.Unlock()
	err := bridge.coordinator.settleOutgoingAttemptsForShutdown(context.Background())
	bridge.coordinator.cancelAllOutgoingAttemptContexts()
	return err
}

func (bridge *firstTrustOutgoingAttemptBridge) recordHandshakeObservation(metadata firstTrustOutgoingAttemptMetadata) {
	bridge.attemptMu.Lock()
	bridge.handshakeObserved[metadata.attemptID] = true
	bridge.attemptMu.Unlock()
}

func (bridge *firstTrustOutgoingAttemptBridge) attemptObservedHandshake(metadata firstTrustOutgoingAttemptMetadata) bool {
	bridge.attemptMu.Lock()
	defer bridge.attemptMu.Unlock()
	return bridge.handshakeObserved[metadata.attemptID]
}

func (bridge *firstTrustOutgoingAttemptBridge) clearHandshakeObservation(metadata firstTrustOutgoingAttemptMetadata) {
	bridge.attemptMu.Lock()
	delete(bridge.handshakeObserved, metadata.attemptID)
	bridge.attemptMu.Unlock()
}

func firstTrustOutgoingAttemptCompletionDurable(result string) bool {
	switch result {
	case "attempt_succeeded", "backoff_active", "admin_hold":
		return true
	default:
		return false
	}
}

func (handle *runtimeOutgoingAttemptHandle) AttemptID() string {
	if handle == nil || handle.handle == nil {
		return ""
	}
	return hex.EncodeToString(handle.handle.metadata.attemptID[:])
}

func (handle *runtimeOutgoingAttemptHandle) Scope() string {
	if handle == nil || handle.handle == nil {
		return ""
	}
	return hex.EncodeToString(handle.handle.metadata.scope[:])
}

func (handle *runtimeOutgoingAttemptHandle) ControlEpoch() uint64 {
	if handle == nil || handle.handle == nil {
		return 0
	}
	return handle.handle.metadata.controlEpoch
}

func (handle *runtimeOutgoingAttemptHandle) Context() context.Context {
	if handle == nil || handle.handle == nil {
		return nil
	}
	return handle.handle.context
}

func runtimeOutgoingAttemptMetadataToSHIP(metadata firstTrustOutgoingAttemptMetadata) shipapi.OutgoingAttemptMetadata {
	return shipapi.OutgoingAttemptMetadata{
		AttemptID:    hex.EncodeToString(metadata.attemptID[:]),
		Scope:        hex.EncodeToString(metadata.scope[:]),
		ControlEpoch: metadata.controlEpoch,
	}
}

func runtimeOutgoingAttemptMetadataFromSHIP(metadata shipapi.OutgoingAttemptMetadata) (firstTrustOutgoingAttemptMetadata, bool) {
	if metadata.ControlEpoch == 0 {
		return firstTrustOutgoingAttemptMetadata{}, false
	}
	attemptID, attemptOK := runtimeDecodeOutgoingAttemptOpaque(metadata.AttemptID)
	scope, scopeOK := runtimeDecodeOutgoingAttemptOpaque(metadata.Scope)
	if !attemptOK || !scopeOK {
		return firstTrustOutgoingAttemptMetadata{}, false
	}
	return firstTrustOutgoingAttemptMetadata{attemptID: attemptID, scope: scope, controlEpoch: metadata.ControlEpoch}, true
}

func runtimeDecodeOutgoingAttemptOpaque(value string) ([32]byte, bool) {
	var result [32]byte
	if len(value) != hex.EncodedLen(len(result)) || value != strings.ToLower(value) {
		return result, false
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(result) {
		return result, false
	}
	copy(result[:], decoded)
	return result, result != [32]byte{}
}

func runtimeOutgoingAttemptPairingDetail(state shipmodel.ShipState) *shipapi.ConnectionStateDetail {
	pairingState := shipapi.ConnectionStateInProgress
	switch state.State {
	case shipmodel.CmiStateInitStart:
		pairingState = shipapi.ConnectionStateQueued
	case shipmodel.CmiStateClientSend, shipmodel.CmiStateClientWait, shipmodel.CmiStateClientEvaluate,
		shipmodel.CmiStateServerWait, shipmodel.CmiStateServerEvaluate:
		pairingState = shipapi.ConnectionStateInitiated
	case shipmodel.SmeHelloStatePendingListen:
		pairingState = shipapi.ConnectionStateReceivedPairingRequest
	case shipmodel.SmeHelloStateOk:
		pairingState = shipapi.ConnectionStateTrusted
	case shipmodel.SmeHelloStateAbort, shipmodel.SmeHelloStateAbortDone:
		pairingState = shipapi.ConnectionStateNone
	case shipmodel.SmeHelloStateRemoteAbortDone, shipmodel.SmeHelloStateRejected:
		pairingState = shipapi.ConnectionStateRemoteDeniedTrust
	case shipmodel.SmePinStateCheckInit, shipmodel.SmePinStateCheckListen, shipmodel.SmePinStateCheckError,
		shipmodel.SmePinStateCheckBusyInit, shipmodel.SmePinStateCheckBusyWait, shipmodel.SmePinStateCheckOk,
		shipmodel.SmePinStateAskInit, shipmodel.SmePinStateAskProcess, shipmodel.SmePinStateAskRestricted,
		shipmodel.SmePinStateAskOk:
		pairingState = shipapi.ConnectionStatePin
	case shipmodel.SmeStateComplete:
		pairingState = shipapi.ConnectionStateCompleted
	case shipmodel.SmeStateError:
		pairingState = shipapi.ConnectionStateError
	}
	if state.Error != nil {
		pairingState = shipapi.ConnectionStateError
	}
	return shipapi.NewConnectionStateDetail(pairingState, state.Error)
}
