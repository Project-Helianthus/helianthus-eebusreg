package eebusfacade

import (
	"encoding/hex"
	"sync"

	eebusapi "github.com/enbility/eebus-go/api"
	shipapi "github.com/enbility/ship-go/api"
)

const firstTrustMaximumConnections = 128

type firstTrustService interface {
	SetAutoAccept(bool)
	RegisterRemoteSKI(string)
	CancelPairingWithSKI(string)
	UserIsAbleToApproveOrCancelPairingRequests(bool)
}

type firstTrustEventSink interface {
	admit([]byte, uint64) string
	serviceShipIDUpdate([]byte, uint64, string) string
	connectionClosed([]byte, uint64) string
}

type firstTrustConnection struct {
	generation uint64
	shipID     string
	active     bool
	connected  bool
	cancelled  bool
	blocked    bool
	registered bool
}

type firstTrustFacade struct {
	mu sync.Mutex

	service     firstTrustService
	coordinator firstTrustEventSink
	next        uint64
	connections map[string]*firstTrustConnection
}

var _ eebusapi.ServiceReaderInterface = (*firstTrustFacade)(nil)

func newFirstTrustFacade(service firstTrustService, coordinator firstTrustEventSink) *firstTrustFacade {
	facade := &firstTrustFacade{
		service:     service,
		coordinator: coordinator,
		connections: make(map[string]*firstTrustConnection),
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
	var stale *firstTrustConnection
	cancel := false
	facade.mu.Lock()
	connection := facade.connections[normalized]
	switch {
	case connection == nil:
		connection = facade.newConnectionLocked(normalized, true)
		cancel = connection == nil
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
	facade.mu.Lock()
	connection := facade.connections[normalized]
	if connection == nil {
		connection = facade.newConnectionLocked(normalized, false)
	}
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
	if !ok || detail == nil || detail.State() != shipapi.ConnectionStateReceivedPairingRequest {
		return
	}
	facade.mu.Lock()
	connection := facade.connections[normalized]
	if connection == nil {
		connection = facade.newConnectionLocked(normalized, false)
	}
	if connection == nil || connection.cancelled || connection.blocked {
		facade.mu.Unlock()
		facade.cancelBySKI(normalized)
		return
	}
	connection.active = true
	generation := connection.generation
	shipID := connection.shipID
	facade.mu.Unlock()

	if facade.coordinator == nil {
		facade.cancelGeneration(remote, generation)
		return
	}
	result := facade.coordinator.admit(remote, generation)
	if result != "candidate_pending" && result != "commit_in_progress" {
		facade.cancelGeneration(remote, generation)
		return
	}
	if result == "candidate_pending" && shipID != "" {
		facade.coordinator.serviceShipIDUpdate(remote, generation, shipID)
	}
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
