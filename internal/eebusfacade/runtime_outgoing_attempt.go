package eebusfacade

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"sync"

	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
	shipmodel "github.com/Project-Helianthus/helianthus-ship-go/model"
)

var errOutgoingAttemptDenied = errors.New("outgoing attempt denied")

type firstTrustOutgoingAttemptLifecycle interface {
	RemoteSKIDisconnected(string)
	ServicePairingDetailUpdate(string, *shipapi.ConnectionStateDetail)
}

type firstTrustOutgoingAttemptBridge struct {
	coordinator *firstTrustCoordinator

	mu        sync.RWMutex
	lifecycle firstTrustOutgoingAttemptLifecycle
}

type runtimeOutgoingAttemptHandle struct {
	owner  *firstTrustOutgoingAttemptBridge
	handle *firstTrustOutgoingAttemptHandle
}

var _ shipapi.OutgoingAttemptGate = (*firstTrustOutgoingAttemptBridge)(nil)
var _ shipapi.OutgoingAttemptHubReaderInterface = (*firstTrustOutgoingAttemptBridge)(nil)
var _ shipapi.OutgoingAttemptHandle = (*runtimeOutgoingAttemptHandle)(nil)

func newFirstTrustOutgoingAttemptBridge(resources *runtimeFirstTrustResources) *firstTrustOutgoingAttemptBridge {
	if resources == nil || resources.coordinator == nil {
		return nil
	}
	return &firstTrustOutgoingAttemptBridge{coordinator: resources.coordinator}
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

func (bridge *firstTrustOutgoingAttemptBridge) Prepare(request shipapi.OutgoingAttemptRequest) (shipapi.OutgoingAttemptHandle, error) {
	if bridge == nil || bridge.coordinator == nil {
		return nil, errOutgoingAttemptDenied
	}
	remote, normalized, ok := decodeFirstTrustSKI(strings.ToLower(strings.TrimSpace(request.RemoteSKI)))
	if !ok || normalized != strings.ToLower(strings.TrimSpace(request.RemoteSKI)) {
		return nil, errOutgoingAttemptDenied
	}
	handle, outcome := bridge.coordinator.prepareOutgoingAttempt(context.Background(), firstTrustOutgoingAttemptRequest{
		remoteSKI: remote,
		endpoint:  firstTrustOutgoingAttemptEndpoint{host: request.Endpoint.Host, port: request.Endpoint.Port},
		path:      request.Path,
	})
	if outcome != "attempt_reserved" || handle == nil {
		return nil, errOutgoingAttemptDenied
	}
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
	return shipapi.OutgoingAttemptPermit{
		Decision: shipapi.OutgoingAttemptDecisionPermit,
		Reason:   shipapi.OutgoingAttemptReasonAuthorized,
		Metadata: runtimeOutgoingAttemptMetadataToSHIP(permit.metadata),
		Context:  permit.context,
	}, nil
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
	bridge.mu.RLock()
	lifecycle := bridge.lifecycle
	bridge.mu.RUnlock()
	if lifecycle != nil {
		lifecycle.RemoteSKIDisconnected(remoteSKI)
	}
	converted, ok := runtimeOutgoingAttemptMetadataFromSHIP(metadata)
	if !ok {
		return
	}
	bridge.coordinator.outgoingAttemptConnectionClosed(context.Background(), remoteSKI, complete, converted)
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
	stateName := "in_progress"
	if state.State == shipmodel.SmeStateComplete {
		stateName = "complete"
	} else if state.State == shipmodel.SmeStateError || state.Error != nil {
		stateName = "error"
	}
	bridge.coordinator.outgoingAttemptHandshakeStateUpdate(context.Background(), remoteSKI, stateName, converted)
	bridge.mu.RLock()
	lifecycle := bridge.lifecycle
	bridge.mu.RUnlock()
	if lifecycle != nil {
		lifecycle.ServicePairingDetailUpdate(remoteSKI, runtimeOutgoingAttemptPairingDetail(state))
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
