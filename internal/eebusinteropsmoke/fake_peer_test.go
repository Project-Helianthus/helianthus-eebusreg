package eebusinteropsmoke

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	eebusapi "github.com/Project-Helianthus/helianthus-eebus-go/api"
	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusservicebridge"
	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
	shipcert "github.com/Project-Helianthus/helianthus-ship-go/cert"
	shipmodel "github.com/Project-Helianthus/helianthus-ship-go/model"
	"github.com/Project-Helianthus/helianthus-ship-go/ship"
	shipws "github.com/Project-Helianthus/helianthus-ship-go/ws"
	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
	"github.com/gorilla/websocket"
)

const (
	fakePeerNodeToken = "0123456789abcdef0123456789abcdef"
	fakePeerSHIPID    = "HLS-" + fakePeerNodeToken
)

func TestFakePeerHandshake(t *testing.T) {
	result := runFakePeerSmoke(fakePeerOptions{
		Endpoint: availableTestEndpoint(t),
		Timeout:  8 * time.Second,
	})
	if result.Status != resultPass {
		t.Fatalf("fake peer handshake failed: %+v", result)
	}
	wantEvidence := map[string]bool{
		"single-canonical-inbound-service":             false,
		"test-only-initiating-client-without-mdns":     false,
		"pairing-open-confirmed-ship-session":          false,
		"pairing-closed-rejected-without-ship-session": false,
	}
	for _, evidence := range result.Evidence {
		if _, ok := wantEvidence[evidence]; ok {
			wantEvidence[evidence] = true
		}
	}
	for evidence, found := range wantEvidence {
		if !found {
			t.Errorf("fake peer handshake evidence = %v, missing %q", result.Evidence, evidence)
		}
	}
}

func availableTestEndpoint(t *testing.T) netip.AddrPort {
	t.Helper()
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := listener.Addr().(*net.TCPAddr).AddrPort()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return endpoint
}

type fakePeerOptions struct {
	Endpoint netip.AddrPort
	Timeout  time.Duration
}

type peerHandler struct {
	service *eebusservicebridge.Service

	mu          sync.Mutex
	pairingOpen bool
	approvals   map[string]int
	connected   map[string]int
	states      map[string][]shipapi.ConnectionState
	shipIDRefs  []string
}

func newPeerHandler() *peerHandler {
	return &peerHandler{
		approvals: make(map[string]int),
		connected: make(map[string]int),
		states:    make(map[string][]shipapi.ConnectionState),
	}
}

func runFakePeerSmoke(opts fakePeerOptions) caseResult {
	if opts.Timeout <= 0 {
		opts.Timeout = 8 * time.Second
	}
	if !opts.Endpoint.IsValid() || !opts.Endpoint.Addr().IsLoopback() || opts.Endpoint.Port() == 0 {
		return fakePeerFail("endpoint", fmt.Errorf("exact loopback endpoint is required"))
	}

	serverCertificate, err := shipcert.CreateCertificate("Helianthus", "Project", "RO", "msp03d-server")
	if err != nil {
		return fakePeerFail("server-certificate", err)
	}
	serverSKI, err := testCertificateSKI(serverCertificate)
	if err != nil {
		return fakePeerFail("server-ski", err)
	}

	handler := newPeerHandler()
	server, err := newInboundPeerService(opts.Endpoint, serverCertificate, handler)
	if err != nil {
		return fakePeerFail("server-service", err)
	}
	handler.service = server
	defer server.Shutdown()

	handler.setPairingOpen(true)
	if err := server.SetPairingRegistration(true); err != nil {
		return fakePeerFail("open-pairing", err)
	}
	if err := server.StartWithPolicy(); err != nil {
		return fakePeerFail("start-inbound-service", err)
	}

	openCertificate, err := shipcert.CreateCertificate("Helianthus", "Project", "RO", "msp03d-open-client")
	if err != nil {
		return fakePeerFail("open-client-certificate", err)
	}
	openClient, err := newTestSHIPClient(opts.Endpoint, openCertificate, serverSKI, fakePeerSHIPID, opts.Timeout)
	if err != nil {
		return fakePeerFail("open-client-dial", err)
	}
	defer openClient.Close()
	if !openClient.waitForState(shipmodel.SmeStateComplete, opts.Timeout) {
		return fakePeerFail("open-client-handshake", fmt.Errorf("states=%v errors=%v", openClient.states(), openClient.errors()))
	}
	if !waitForFakePeer(opts.Timeout, func() bool { return handler.connectedCount(openClient.localSKI) == 1 }) {
		return fakePeerFail("open-server-session", fmt.Errorf("connected=%d", handler.connectedCount(openClient.localSKI)))
	}
	openApprovals := handler.approvalCount(openClient.localSKI)
	if openApprovals != 1 {
		return fakePeerFail("open-trust-confirmation", fmt.Errorf("approvals=%d", openApprovals))
	}

	if err := server.SetPairingRegistration(false); err != nil {
		return fakePeerFail("close-pairing", err)
	}
	handler.setPairingOpen(false)
	openClient.Close()

	closedCertificate, err := shipcert.CreateCertificate("Helianthus", "Project", "RO", "msp03d-closed-client")
	if err != nil {
		return fakePeerFail("closed-client-certificate", err)
	}
	closedClient, err := newTestSHIPClient(opts.Endpoint, closedCertificate, serverSKI, fakePeerSHIPID, opts.Timeout)
	if err != nil {
		return fakePeerFail("closed-client-dial", err)
	}
	defer closedClient.Close()
	if !closedClient.waitForAnyState(opts.Timeout,
		shipmodel.SmeHelloStateRemoteAbortDone,
		shipmodel.SmeHelloStateRejected,
		shipmodel.SmeHelloStateAbortDone,
		shipmodel.SmeStateError,
	) {
		return fakePeerFail("closed-client-rejection", fmt.Errorf("state=%v", closedClient.states()))
	}
	if closedClient.hasState(shipmodel.SmeStateComplete) {
		return fakePeerFail("closed-client-session", fmt.Errorf("closed window completed SHIP"))
	}
	if !handler.waitForState(closedClient.localSKI, shipapi.ConnectionStateReceivedPairingRequest, opts.Timeout) {
		return fakePeerFail("closed-server-pairing-observation", fmt.Errorf("states=%v", handler.statesFor(closedClient.localSKI)))
	}
	if approvals := handler.approvalCount(closedClient.localSKI); approvals != 0 {
		return fakePeerFail("closed-server-trust", fmt.Errorf("approvals=%d", approvals))
	}
	if connected := handler.connectedCount(closedClient.localSKI); connected != 0 {
		return fakePeerFail("closed-server-session", fmt.Errorf("connected=%d", connected))
	}

	return caseResult{
		ID:     caseFakePeer,
		Status: resultPass,
		Evidence: []string{
			"single-canonical-inbound-service",
			"test-only-initiating-client-without-mdns",
			"pairing-open-confirmed-ship-session",
			"pairing-closed-rejected-without-ship-session",
		},
		Details: map[string]string{
			"server_ski_ref":        digestRef(serverSKI),
			"open_client_ski_ref":   digestRef(openClient.localSKI),
			"closed_client_ski_ref": digestRef(closedClient.localSKI),
			"open_state_count_ref":  digestRef(fmt.Sprintf("%d", len(openClient.states()))),
		},
	}
}

func newInboundPeerService(endpoint netip.AddrPort, certificate tls.Certificate, handler *peerHandler) (*eebusservicebridge.Service, error) {
	configuration, err := eebusapi.NewConfiguration(
		"Project-Helianthus",
		"Helianthus",
		"eebusreg",
		fakePeerNodeToken,
		spinemodel.DeviceTypeTypeEnergyManagementSystem,
		[]spinemodel.EntityTypeType{spinemodel.EntityTypeTypeCEM},
		int(endpoint.Port()),
		certificate,
		2*time.Second,
	)
	if err != nil {
		return nil, err
	}
	configuration.SetAlternateIdentifier(fakePeerSHIPID)
	configuration.SetAlternateMdnsServiceName("Helianthus EnergyManagementSystem eebusreg")
	service := eebusservicebridge.NewServiceWithOptions(configuration, handler, eebusservicebridge.ServiceOptions{
		ListenerPolicy: &eebusservicebridge.ListenerPolicy{
			ListenAddress:    endpoint,
			DiscoveryEnabled: false,
		},
	})
	if service == nil {
		return nil, fmt.Errorf("canonical service construction returned nil")
	}
	if err := service.Setup(); err != nil {
		return nil, err
	}
	return service, nil
}

type testSHIPClient struct {
	localSKI   string
	connection shipapi.ShipConnectionInterface
	provider   *testSHIPClientProvider
}

func newTestSHIPClient(endpoint netip.AddrPort, certificate tls.Certificate, remoteSKI, remoteShipID string, timeout time.Duration) (*testSHIPClient, error) {
	localSKI, err := testCertificateSKI(certificate)
	if err != nil {
		return nil, err
	}
	dialer := websocket.Dialer{
		HandshakeTimeout: timeout,
		TLSClientConfig: &tls.Config{
			Certificates:       []tls.Certificate{certificate},
			CipherSuites:       shipcert.CipherSuites, // #nosec G402 -- SHIP 9.1 test peer
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: true, // #nosec G402 -- disposable self-signed test peer
		},
		Subprotocols: []string{shipapi.ShipWebsocketSubProtocol},
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	connection, response, err := dialer.DialContext(ctx, "wss://"+endpoint.String()+"/ship/", nil)
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	peerCertificates := connection.UnderlyingConn().(*tls.Conn).ConnectionState().PeerCertificates
	if len(peerCertificates) == 0 {
		_ = connection.Close()
		return nil, fmt.Errorf("server certificate is missing")
	}
	actualRemoteSKI, err := shipcert.SkiFromCertificate(peerCertificates[0])
	if err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("derive server SKI: %w", err)
	}
	if actualRemoteSKI != remoteSKI {
		_ = connection.Close()
		return nil, fmt.Errorf("server SKI = %q, want %q", actualRemoteSKI, remoteSKI)
	}
	provider := newTestSHIPClientProvider()
	dataHandler := shipws.NewWebsocketConnection(connection, remoteSKI)
	shipConnection := ship.NewConnectionHandler(
		provider,
		dataHandler,
		ship.ShipRoleClient,
		"test-client-"+localSKI,
		remoteSKI,
		remoteShipID,
	)
	client := &testSHIPClient{localSKI: localSKI, connection: shipConnection, provider: provider}
	shipConnection.Run()
	return client, nil
}

func (client *testSHIPClient) Close() {
	if client != nil && client.connection != nil {
		client.connection.CloseConnection(false, 0, "test fixture close")
	}
}

func (client *testSHIPClient) waitForState(state shipmodel.ShipMessageExchangeState, timeout time.Duration) bool {
	return waitForFakePeer(timeout, func() bool { return client.hasState(state) })
}

func (client *testSHIPClient) waitForAnyState(timeout time.Duration, states ...shipmodel.ShipMessageExchangeState) bool {
	return waitForFakePeer(timeout, func() bool {
		for _, state := range states {
			if client.hasState(state) {
				return true
			}
		}
		return false
	})
}

func (client *testSHIPClient) hasState(want shipmodel.ShipMessageExchangeState) bool {
	for _, state := range client.states() {
		if state == want {
			return true
		}
	}
	return false
}

func (client *testSHIPClient) states() []shipmodel.ShipMessageExchangeState {
	return client.provider.statesSnapshot()
}

func (client *testSHIPClient) errors() []string {
	return client.provider.errorsSnapshot()
}

type testSHIPClientProvider struct {
	mu     sync.Mutex
	states []shipmodel.ShipMessageExchangeState
	errors []string
}

func newTestSHIPClientProvider() *testSHIPClientProvider {
	return &testSHIPClientProvider{}
}

func (*testSHIPClientProvider) IsRemoteServiceForSKIPaired(string) bool { return true }
func (*testSHIPClientProvider) IsAutoAcceptEnabled() bool               { return true }
func (*testSHIPClientProvider) ReportServiceShipID(string, string)      {}
func (*testSHIPClientProvider) AllowWaitingForTrust(string) bool        { return true }
func (*testSHIPClientProvider) SetupRemoteDevice(string, shipapi.ShipConnectionDataWriterInterface) shipapi.ShipConnectionDataReaderInterface {
	return discardShipPayload{}
}
func (*testSHIPClientProvider) HandleConnectionClosed(shipapi.ShipConnectionInterface, bool) {}
func (provider *testSHIPClientProvider) HandleShipHandshakeStateUpdate(_ string, state shipmodel.ShipState) {
	provider.mu.Lock()
	provider.states = append(provider.states, state.State)
	if state.Error != nil {
		provider.errors = append(provider.errors, state.Error.Error())
	}
	provider.mu.Unlock()
}
func (provider *testSHIPClientProvider) statesSnapshot() []shipmodel.ShipMessageExchangeState {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return append([]shipmodel.ShipMessageExchangeState(nil), provider.states...)
}

func (provider *testSHIPClientProvider) errorsSnapshot() []string {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return append([]string(nil), provider.errors...)
}

type discardShipPayload struct{}

func (discardShipPayload) HandleShipPayloadMessage([]byte) {}

func fakePeerFail(stage string, err error) caseResult {
	return caseResult{
		ID:       caseFakePeer,
		Status:   resultFail,
		Evidence: []string{"single-canonical-inbound-service", "test-only-initiating-client-without-mdns"},
		Error:    stage + ":" + err.Error(),
	}
}

func testCertificateSKI(certificate tls.Certificate) (string, error) {
	if len(certificate.Certificate) == 0 {
		return "", fmt.Errorf("certificate chain is empty")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return "", err
	}
	return shipcert.SkiFromCertificate(leaf)
}

func waitForFakePeer(timeout time.Duration, predicate func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return predicate()
}

func (handler *peerHandler) setPairingOpen(open bool) {
	handler.mu.Lock()
	handler.pairingOpen = open
	handler.mu.Unlock()
}

func (handler *peerHandler) RemoteSKIConnected(_ eebusapi.ServiceInterface, ski string) {
	handler.mu.Lock()
	handler.connected[ski]++
	handler.mu.Unlock()
}

func (*peerHandler) RemoteSKIDisconnected(eebusapi.ServiceInterface, string) {}

func (*peerHandler) VisibleRemoteServicesUpdated(eebusapi.ServiceInterface, []shipapi.RemoteService) {
}

func (handler *peerHandler) ServiceShipIDUpdate(_ string, shipID string) {
	handler.mu.Lock()
	handler.shipIDRefs = append(handler.shipIDRefs, digestRef(shipID))
	handler.mu.Unlock()
}

func (handler *peerHandler) ServicePairingDetailUpdate(ski string, detail *shipapi.ConnectionStateDetail) {
	state := detail.State()
	handler.mu.Lock()
	handler.states[ski] = append(handler.states[ski], state)
	approve := handler.pairingOpen && state == shipapi.ConnectionStateReceivedPairingRequest && handler.approvals[ski] == 0
	service := handler.service
	if approve {
		handler.approvals[ski]++
	}
	handler.mu.Unlock()
	if approve && service != nil {
		service.RegisterRemoteSKI(ski)
	}
}

func (handler *peerHandler) approvalCount(ski string) int {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	return handler.approvals[ski]
}

func (handler *peerHandler) connectedCount(ski string) int {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	return handler.connected[ski]
}

func (handler *peerHandler) statesFor(ski string) []shipapi.ConnectionState {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	return append([]shipapi.ConnectionState(nil), handler.states[ski]...)
}

func (handler *peerHandler) waitForState(ski string, want shipapi.ConnectionState, timeout time.Duration) bool {
	return waitForFakePeer(timeout, func() bool {
		for _, state := range handler.statesFor(ski) {
			if state == want {
				return true
			}
		}
		return false
	})
}
