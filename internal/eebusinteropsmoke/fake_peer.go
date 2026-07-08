package main

import (
	"crypto/tls"
	"fmt"
	"net"
	"sync"
	"time"

	eebusapi "github.com/enbility/eebus-go/api"
	"github.com/enbility/eebus-go/service"
	shipapi "github.com/enbility/ship-go/api"
	"github.com/enbility/ship-go/cert"
	shipmdns "github.com/enbility/ship-go/mdns"
	"github.com/enbility/spine-go/model"
)

type fakePeerOptions struct {
	Interface string
	Timeout   time.Duration
	PortA     int
	PortB     int
}

type peerHandler struct {
	name       string
	service    *service.Service
	remoteSKI  string
	connected  []string
	states     []string
	visible    int
	shipIDRefs []string
	mu         sync.Mutex
}

func runFakePeerSmoke(opts fakePeerOptions) caseResult {
	if opts.Timeout <= 0 {
		opts.Timeout = 45 * time.Second
	}
	if opts.PortA == 0 {
		opts.PortA = 54711
	}
	if opts.PortB == 0 {
		opts.PortB = 54712
	}
	if opts.Interface == "" {
		opts.Interface = defaultLoopbackInterface()
	}

	certA, err := cert.CreateCertificate("Helianthus", "Project", "RO", "msp03d-a")
	if err != nil {
		return fakePeerFail("certificate-a", err)
	}
	certB, err := cert.CreateCertificate("Helianthus", "Project", "RO", "msp03d-b")
	if err != nil {
		return fakePeerFail("certificate-b", err)
	}

	a, err := newPeerService("MSP03D-A", opts.PortA, certA, opts.Interface)
	if err != nil {
		return fakePeerFail("service-a", err)
	}
	defer a.service.Shutdown()
	b, err := newPeerService("MSP03D-B", opts.PortB, certB, opts.Interface)
	if err != nil {
		return fakePeerFail("service-b", err)
	}
	defer b.service.Shutdown()

	a.remoteSKI = b.service.LocalService().SKI()
	b.remoteSKI = a.service.LocalService().SKI()
	a.service.RegisterRemoteSKI(a.remoteSKI)
	b.service.RegisterRemoteSKI(b.remoteSKI)
	a.service.Start()
	b.service.Start()

	deadline := time.Now().Add(opts.Timeout)
	for time.Now().Before(deadline) {
		if a.connectedCount() > 0 && b.connectedCount() > 0 {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}

	if a.connectedCount() == 0 || b.connectedCount() == 0 {
		return caseResult{
			ID:     caseFakePeer,
			Status: resultFail,
			Evidence: []string{
				"fake-peer-disposable-certificates-created",
				"fake-peer-import-boundary-checked",
				"fake-peer-handshake-timeout",
			},
			Details: map[string]string{
				"local_endpoint_ref":  refLabel("endpoint-a", a.service.LocalService().SKI()),
				"remote_endpoint_ref": refLabel("endpoint-b", b.service.LocalService().SKI()),
				"interface_ref":       refLabel("iface", opts.Interface),
			},
			Error: "handshake_timeout",
		}
	}

	return caseResult{
		ID:     caseFakePeer,
		Status: resultPass,
		Evidence: []string{
			"fake-peer-disposable-certificates-created",
			"fake-peer-import-boundary-checked",
			"fake-peer-ship-session-connected-both-directions",
		},
		Details: map[string]string{
			"local_endpoint_ref":  refLabel("endpoint-a", a.service.LocalService().SKI()),
			"remote_endpoint_ref": refLabel("endpoint-b", b.service.LocalService().SKI()),
			"interface_ref":       refLabel("iface", opts.Interface),
			"state_count_ref":     digestRef(fmt.Sprintf("%d", len(a.states)+len(b.states))),
			"visible_count_ref":   digestRef(fmt.Sprintf("%d", a.visibleCount()+b.visibleCount())),
		},
	}
}

func newPeerService(name string, port int, certificate tls.Certificate, iface string) (*peerHandler, error) {
	h := &peerHandler{name: name}
	cfg, err := eebusapi.NewConfiguration(
		"Helianthus",
		"Helianthus",
		name,
		name+"-serial",
		model.DeviceTypeTypeEnergyManagementSystem,
		[]model.EntityTypeType{model.EntityTypeTypeCEM},
		port,
		certificate,
		2*time.Second,
	)
	if err != nil {
		return nil, err
	}
	cfg.SetAlternateIdentifier("Helianthus-" + name)
	cfg.SetAlternateMdnsServiceName("Helianthus-" + name)
	if iface != "" {
		cfg.SetInterfaces([]string{iface})
	}
	cfg.SetMdnsProviderSelection(shipmdns.MdnsProviderSelectionGoZeroConfOnly)

	h.service = service.NewService(cfg, h)
	if err := h.service.Setup(); err != nil {
		return nil, err
	}
	h.service.SetAutoAccept(true)
	h.service.UserIsAbleToApproveOrCancelPairingRequests(true)
	return h, nil
}

func fakePeerFail(stage string, err error) caseResult {
	return caseResult{
		ID:       caseFakePeer,
		Status:   resultFail,
		Evidence: []string{"fake-peer-import-boundary-checked"},
		Error:    stage + ":" + err.Error(),
	}
}

func defaultLoopbackInterface() string {
	for _, name := range []string{"lo0", "lo"} {
		if _, err := net.InterfaceByName(name); err == nil {
			return name
		}
	}
	return ""
}

func (h *peerHandler) RemoteSKIConnected(_ eebusapi.ServiceInterface, ski string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.connected = append(h.connected, digestRef(ski))
}

func (h *peerHandler) RemoteSKIDisconnected(_ eebusapi.ServiceInterface, ski string) {}

func (h *peerHandler) VisibleRemoteServicesUpdated(_ eebusapi.ServiceInterface, entries []shipapi.RemoteService) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.visible += len(entries)
}

func (h *peerHandler) ServiceShipIDUpdate(_ string, shipID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.shipIDRefs = append(h.shipIDRefs, digestRef(shipID))
}

func (h *peerHandler) ServicePairingDetailUpdate(_ string, detail *shipapi.ConnectionStateDetail) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.states = append(h.states, connectionStateName(detail.State()))
}

func (h *peerHandler) connectedCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.connected)
}

func (h *peerHandler) visibleCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.visible
}

func connectionStateName(state shipapi.ConnectionState) string {
	switch state {
	case shipapi.ConnectionStateNone:
		return "none"
	case shipapi.ConnectionStateQueued:
		return "queued"
	case shipapi.ConnectionStateInitiated:
		return "initiated"
	case shipapi.ConnectionStateReceivedPairingRequest:
		return "received_pairing_request"
	case shipapi.ConnectionStateInProgress:
		return "in_progress"
	case shipapi.ConnectionStateTrusted:
		return "trusted"
	case shipapi.ConnectionStatePin:
		return "pin"
	case shipapi.ConnectionStateCompleted:
		return "completed"
	case shipapi.ConnectionStateRemoteDeniedTrust:
		return "remote_denied_trust"
	case shipapi.ConnectionStateError:
		return "error"
	default:
		return "unknown"
	}
}
