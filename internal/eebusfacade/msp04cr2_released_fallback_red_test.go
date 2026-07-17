package eebusfacade

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	eebusapi "github.com/Project-Helianthus/helianthus-eebus-go/api"
	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusservicebridge"
	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
	shipcert "github.com/Project-Helianthus/helianthus-ship-go/cert"
	shiphub "github.com/Project-Helianthus/helianthus-ship-go/hub"
	"github.com/gorilla/websocket"
)

func TestMSP04CR2ReleasedHubExhaustsImmediateFallbackChainBeforeBackoff(t *testing.T) {
	tests := []struct {
		name           string
		mode           msp04cr2ReleasedPeerMode
		wantRequests   int
		wantAccepts    int
		wantRetry      string
		wantRetryCount uint64
	}{
		{name: "root fallback succeeds", mode: msp04cr2ReleasedAcceptRoot, wantRequests: 2, wantAccepts: 1, wantRetry: "RETRY_READY"},
		{name: "address fallback succeeds", mode: msp04cr2ReleasedAcceptAddress, wantRequests: 3, wantAccepts: 1, wantRetry: "RETRY_READY"},
		{name: "chain exhaustion charges once", mode: msp04cr2ReleasedRejectAll, wantRequests: 4, wantRetry: "BACKOFF_ACTIVE", wantRetryCount: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			peer := newMSP04CR2ReleasedPeer(t, test.mode)
			resources := newMSP04CR2RealAttemptResources(t, peer.remoteSKI)
			bridge := newFirstTrustOutgoingAttemptBridge(resources)
			reader := eebusservicebridge.NewServiceWithOutgoingAttemptBridge(
				nil,
				&msp04cr2ReleasedServiceReader{},
				eebusservicebridge.OutgoingAttemptBridgeConfiguration{Gate: bridge, Sink: bridge},
			)
			if reader == nil {
				t.Fatal("released eebus-go bridge constructor returned nil")
			}
			bridge.bindLifecycle(reader)

			localCertificate, err := shipcert.CreateCertificate("", "Helianthus", "RO", "msp04cr2-released-chain")
			if err != nil {
				t.Fatal(err)
			}
			localService := shipapi.NewServiceDetails(certificateSKI(t, localCertificate))
			localService.SetShipID("msp04cr2-released-chain")
			releasedHub := shiphub.NewHub(reader, &msp04cr2NoopMDNS{}, 0, localCertificate, localService)
			if err := releasedHub.SetOutgoingAttemptGate(bridge); err != nil {
				t.Fatalf("install runtime gate on released Hub: %v", err)
			}
			t.Cleanup(releasedHub.Shutdown)
			t.Cleanup(peer.releaseConnection)

			releasedHub.RegisterRemoteSKI(peer.remoteSKI)
			releasedHub.ServiceForSKI(peer.remoteSKI).ConnectionStateDetail().SetState(shipapi.ConnectionStateQueued)
			entry := peer.mdnsEntry()
			releasedHub.ReportMdnsEntries(map[string]*shipapi.MdnsEntry{peer.remoteSKI: entry}, true)

			peer.waitForOutcome(resources.coordinator, test.wantAccepts > 0)
			requests, accepts := peer.counts()
			scope := firstTrustRuntimeRetryScope(peer.remoteSKI)
			state, retryCount, _, retryExists := resources.coordinator.retryState(scope)
			resources.coordinator.mu.Lock()
			activeAttempts := len(resources.coordinator.controlView.control.attempts)
			resources.coordinator.mu.Unlock()
			if requests != test.wantRequests || accepts != test.wantAccepts {
				t.Fatalf(
					"released Hub request/accept observations = %d/%d, want %d/%d; retry=%s/%d/%t active_attempts=%d",
					requests, accepts, test.wantRequests, test.wantAccepts, state, retryCount, retryExists, activeAttempts,
				)
			}
			if !retryExists || state != test.wantRetry || retryCount != test.wantRetryCount {
				t.Fatalf("retry after released fallback chain = %s/%d/%t, want %s/%d/true", state, retryCount, retryExists, test.wantRetry, test.wantRetryCount)
			}
		})
	}
}

func newMSP04CR2RealAttemptResources(t *testing.T, remoteSKI string) *runtimeFirstTrustResources {
	t.Helper()
	root := canonicalRuntimeTempDir(t)
	anchor := &runtimeStrictAnchor{}
	service := &fakeRuntimeService{started: make(chan struct{})}
	resources := acquireMSP04CRuntimeResources(
		t,
		filepath.Join(root, "state"),
		filepath.Join(root, "admin"),
		anchor,
		service,
	)
	t.Cleanup(func() {
		if err := resources.Close(); err != nil {
			t.Errorf("close real attempt resources: %v", err)
		}
	})
	request := exactRuntimeRepairRequest(resources.coordinator, "recover_unavailable_host_key", msp04cOrdinal(1_300))
	if got := resources.coordinator.repair(context.Background(), request); got != "repaired_unpaired" {
		t.Fatalf("prepare real durable store = %q", got)
	}
	pairRuntimeRemote(t, resources, remoteSKI, 1_301)
	resources.facade.ServicePairingDetailUpdate(remoteSKI, shipapi.NewConnectionStateDetail(shipapi.ConnectionStateCompleted, nil))
	if resources.coordinator.recoveryState() != "PAIRED_TRUSTED" {
		t.Fatalf("real attempt resources recovery = %q", resources.coordinator.recoveryState())
	}
	remote, err := hex.DecodeString(remoteSKI)
	if err != nil {
		t.Fatal(err)
	}
	resources.coordinator.mu.Lock()
	eligible := resources.coordinator.firstTrustOutgoingAttemptEligibleLocked(remote)
	resources.coordinator.mu.Unlock()
	if !eligible {
		t.Fatal("real coordinator does not consider the paired fake peer eligible")
	}
	return resources
}

type msp04cr2ReleasedServiceReader struct{}

func (*msp04cr2ReleasedServiceReader) RemoteSKIConnected(eebusapi.ServiceInterface, string)    {}
func (*msp04cr2ReleasedServiceReader) RemoteSKIDisconnected(eebusapi.ServiceInterface, string) {}
func (*msp04cr2ReleasedServiceReader) VisibleRemoteServicesUpdated(eebusapi.ServiceInterface, []shipapi.RemoteService) {
}
func (*msp04cr2ReleasedServiceReader) ServiceShipIDUpdate(string, string) {}
func (*msp04cr2ReleasedServiceReader) ServicePairingDetailUpdate(string, *shipapi.ConnectionStateDetail) {
}

type msp04cr2NoopMDNS struct{}

func (*msp04cr2NoopMDNS) Start(shipapi.MdnsReportInterface) error { return nil }
func (*msp04cr2NoopMDNS) Shutdown()                               {}
func (*msp04cr2NoopMDNS) AnnounceMdnsEntry() error                { return nil }
func (*msp04cr2NoopMDNS) UnannounceMdnsEntry()                    {}
func (*msp04cr2NoopMDNS) SetAutoAccept(bool)                      {}
func (*msp04cr2NoopMDNS) RequestMdnsEntries()                     {}

type msp04cr2ReleasedPeerMode uint8

const (
	msp04cr2ReleasedAcceptRoot msp04cr2ReleasedPeerMode = iota + 1
	msp04cr2ReleasedAcceptAddress
	msp04cr2ReleasedRejectAll
)

type msp04cr2ReleasedPeer struct {
	server    *httptest.Server
	remoteSKI string
	host      string
	port      int
	mode      msp04cr2ReleasedPeerMode
	release   chan struct{}
	once      sync.Once
	mu        sync.Mutex
	requests  int
	accepts   int
}

func newMSP04CR2ReleasedPeer(t *testing.T, mode msp04cr2ReleasedPeerMode) *msp04cr2ReleasedPeer {
	t.Helper()
	certificate, err := shipcert.CreateCertificate("", "Helianthus", "RO", "msp04cr2-released-peer-"+strconv.Itoa(int(mode)))
	if err != nil {
		t.Fatal(err)
	}
	peer := &msp04cr2ReleasedPeer{
		remoteSKI: certificateSKI(t, certificate),
		mode:      mode,
		release:   make(chan struct{}),
	}
	peer.server = httptest.NewUnstartedServer(http.HandlerFunc(peer.handle))
	peer.server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}
	peer.server.StartTLS()
	parsed, err := url.Parse(peer.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	host, portText, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	peer.host = host
	peer.port, err = strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		peer.once.Do(func() { close(peer.release) })
		peer.server.Close()
	})
	return peer
}

func (peer *msp04cr2ReleasedPeer) mdnsEntry() *shipapi.MdnsEntry {
	return &shipapi.MdnsEntry{
		Name:       "msp04cr2-released-peer",
		Ski:        peer.remoteSKI,
		Identifier: "msp04cr2-released-peer",
		Path:       "/ship/",
		Host:       "localhost",
		Port:       peer.port,
		Addresses:  []net.IP{net.ParseIP(peer.host)},
	}
}

func (peer *msp04cr2ReleasedPeer) handle(writer http.ResponseWriter, request *http.Request) {
	peer.mu.Lock()
	peer.requests++
	peer.mu.Unlock()
	if !peer.acceptsRequest(request) {
		http.Error(writer, "released fallback rejection", http.StatusServiceUnavailable)
		return
	}
	connection, err := (&websocket.Upgrader{
		CheckOrigin:  func(*http.Request) bool { return true },
		Subprotocols: []string{shipapi.ShipWebsocketSubProtocol},
	}).Upgrade(writer, request, nil)
	if err != nil {
		return
	}
	defer connection.Close()
	peer.mu.Lock()
	peer.accepts++
	peer.mu.Unlock()
	<-peer.release
}

func (peer *msp04cr2ReleasedPeer) acceptsRequest(request *http.Request) bool {
	switch peer.mode {
	case msp04cr2ReleasedAcceptRoot:
		return request.URL.Path == "/"
	case msp04cr2ReleasedAcceptAddress:
		host, _, err := net.SplitHostPort(request.Host)
		return err == nil && host == peer.host && request.URL.Path == "/ship/"
	default:
		return false
	}
}

func (peer *msp04cr2ReleasedPeer) counts() (int, int) {
	peer.mu.Lock()
	defer peer.mu.Unlock()
	return peer.requests, peer.accepts
}

func (peer *msp04cr2ReleasedPeer) releaseConnection() {
	peer.once.Do(func() { close(peer.release) })
}

func (peer *msp04cr2ReleasedPeer) waitForOutcome(coordinator *firstTrustCoordinator, wantAccept bool) {
	deadline := time.Now().Add(2 * time.Second)
	scope := firstTrustRuntimeRetryScope(peer.remoteSKI)
	for time.Now().Before(deadline) {
		_, accepts := peer.counts()
		state, _, _, ok := coordinator.retryState(scope)
		if (wantAccept && accepts > 0) || (!wantAccept && ok && state == "BACKOFF_ACTIVE") || (wantAccept && ok && state == "BACKOFF_ACTIVE") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

var _ shipapi.MdnsInterface = (*msp04cr2NoopMDNS)(nil)
