package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMSP04CR2PermittedAttemptIsObservedOnceByDialerAndFakePeer(t *testing.T) {
	peer := newMSP04CR2FakePeer(t, "")
	proof, err := executeMSP04CR2Proof(t, peer.options(t, "permit"))
	if err != nil {
		t.Fatalf("execute permitted proof: %v", err)
	}
	peer.requireAccept(t)
	if proof.evidenceStatus != "PASS" {
		t.Fatalf("permitted proof evidence status = %q, want PASS", proof.evidenceStatus)
	}
	assertMSP04CR2Counts(t, proof, 1, 1, 1, 1)
	assertMSP04CR2OrderedAttempt(t, proof.events,
		"reservation_committed", "handle_returned", "launch_committed", "permit_returned", "dial_context", "peer_accept",
	)
	if len(proof.dials) != 1 || proof.dials[0].contextID == "" || proof.dials[0].contextID != proof.permits[0].contextID {
		t.Fatal("DialContext did not receive the exact permit context")
	}
}

func TestMSP04CR2DeniedBackoffQuarantineRevocationAndReserveFailureNeverDial(t *testing.T) {
	for _, scenario := range []string{"reserve_failure", "policy_denied", "backoff", "quarantined", "revoked"} {
		t.Run(scenario, func(t *testing.T) {
			peer := newMSP04CR2FakePeer(t, "")
			proof, err := executeMSP04CR2Proof(t, peer.options(t, scenario))
			if err != nil {
				t.Fatalf("execute %s proof: %v", scenario, err)
			}
			assertMSP04CR2Counts(t, proof, 0, 0, 0, 0)
			select {
			case request := <-peer.requests:
				t.Fatalf("%s produced network request %q", scenario, request)
			default:
			}
		})
	}
}

func TestMSP04CR2FallbackAndReconnectUseFreshTokensPerConcreteDial(t *testing.T) {
	tests := []struct {
		name         string
		rejectPath   string
		wantAccepts  int
		wantRequests int
	}{
		{name: "fallback", rejectPath: "/ship/", wantAccepts: 1, wantRequests: 2},
		{name: "reconnect", wantAccepts: 2, wantRequests: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			peer := newMSP04CR2FakePeer(t, test.rejectPath)
			if test.name == "reconnect" {
				peer.setCloseAfterAccepts(1)
			}
			proof, err := executeMSP04CR2Proof(t, peer.options(t, test.name))
			if err != nil {
				t.Fatalf("execute %s proof: %v", test.name, err)
			}
			assertMSP04CR2Counts(t, proof, 2, 2, 2, test.wantAccepts)
			if len(proof.reservations) != 2 || proof.reservations[0].attemptID == proof.reservations[1].attemptID {
				t.Fatal("fallback or reconnect reused a reservation token")
			}
			if proof.reservations[0].path == proof.reservations[1].path && test.name == "fallback" {
				t.Fatal("fallback reservations do not bind distinct selected/root paths")
			}
			for index := range proof.permits {
				if proof.permits[index].attemptID != proof.dials[index].attemptID {
					t.Fatalf("permit %d does not bind dial %d", index+1, index+1)
				}
			}
			if got := peer.requestCount(); got != test.wantRequests {
				t.Fatalf("fake-peer request count = %d, want %d", got, test.wantRequests)
			}
		})
	}
}

func TestMSP04CR2ExecutedArtifactRejectsCallbackOnlyAccountingAndRedactsPrivateBindings(t *testing.T) {
	peer := newMSP04CR2FakePeer(t, "")
	permit, err := executeMSP04CR2Proof(t, peer.options(t, "permit"))
	if err != nil {
		t.Fatal(err)
	}
	peer.requireAccept(t)
	if len(permit.reservations) != 1 {
		t.Fatalf("permit proof reservations = %d, want 1", len(permit.reservations))
	}
	callbackOnly, err := executeMSP04CR2Proof(t, peer.options(t, "callback_only"))
	if err != nil {
		t.Fatal(err)
	}
	if callbackOnly.evidenceStatus != "FAIL" || len(callbackOnly.dials) != 0 || len(callbackOnly.accepts) != 0 {
		t.Fatalf("callback-only proof = %q/%d/%d, want FAIL/0/0", callbackOnly.evidenceStatus, len(callbackOnly.dials), len(callbackOnly.accepts))
	}

	payload, err := buildMSP04CR2ExecutedArtifact([]msp04cr2SyntheticProof{permit, callbackOnly})
	if err != nil {
		t.Fatalf("build executed artifact: %v", err)
	}
	var decoded struct {
		Gates []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"gates"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode executed artifact: %v", err)
	}
	if len(decoded.Gates) != 3 {
		t.Fatalf("executed artifact gate count = %d, want G10/G11/G16", len(decoded.Gates))
	}
	for _, gate := range []string{"EEBUS-G10", "EEBUS-G11", "EEBUS-G16"} {
		index := slices.IndexFunc(decoded.Gates, func(value struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}) bool {
			return value.ID == gate
		})
		if index < 0 {
			t.Fatalf("executed artifact lacks %s", gate)
		}
		if decoded.Gates[index].Status != "PASS" {
			t.Fatalf("executed artifact %s status = %q, want PASS", gate, decoded.Gates[index].Status)
		}
	}
	text := string(payload)
	for _, private := range []string{peer.remoteSKI, peer.host, peer.selectedPath, peer.root, hex.EncodeToString(permit.reservations[0].attemptID[:])} {
		if private != "" && strings.Contains(text, private) {
			t.Fatalf("executed artifact leaked private attempt binding category: %s", text)
		}
	}
	for _, required := range []string{"reservation_1", "permit_1", "dial_1", "accept_1"} {
		if !strings.Contains(text, required) {
			t.Fatalf("executed artifact lacks redacted observed label %q: %s", required, text)
		}
	}
}

func assertMSP04CR2Counts(t *testing.T, proof msp04cr2SyntheticProof, reservations, permits, dials, accepts int) {
	t.Helper()
	if len(proof.reservations) != reservations || len(proof.permits) != permits || len(proof.dials) != dials || len(proof.accepts) != accepts {
		t.Fatalf("reservation/permit/dial/accept = %d/%d/%d/%d, want %d/%d/%d/%d",
			len(proof.reservations), len(proof.permits), len(proof.dials), len(proof.accepts), reservations, permits, dials, accepts)
	}
}

func executeMSP04CR2Proof(t *testing.T, options msp04cr2SyntheticProofOptions) (msp04cr2SyntheticProof, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return executeMSP04CR2SyntheticProof(ctx, options)
}

func assertMSP04CR2OrderedAttempt(t *testing.T, events []msp04cr2PrivateEvent, kinds ...string) {
	t.Helper()
	position := -1
	var attemptID [32]byte
	for _, kind := range kinds {
		found := -1
		for index := position + 1; index < len(events); index++ {
			if events[index].kind == kind {
				found = index
				break
			}
		}
		if found < 0 {
			t.Fatalf("event %q missing from %#v", kind, events)
		}
		if attemptID == [32]byte{} {
			attemptID = events[found].attemptID
		} else if events[found].attemptID != attemptID {
			t.Fatalf("event %q changed attempt binding", kind)
		}
		position = found
	}
}

type msp04cr2FakePeer struct {
	root              string
	host              string
	port              uint16
	remoteSKI         string
	selectedPath      string
	rejectPath        string
	clientTLS         *tls.Config
	server            *http.Server
	listener          net.Listener
	requests          chan string
	accepted          chan string
	release           chan struct{}
	releaseOnce       sync.Once
	mu                sync.Mutex
	requestTotal      int
	acceptTotal       int
	closeAfterAccepts int
}

func newMSP04CR2FakePeer(t *testing.T, rejectPath string) *msp04cr2FakePeer {
	t.Helper()
	certificate, leaf := newMSP04CR2Certificate(t)
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	peer := &msp04cr2FakePeer{
		root: t.TempDir(), host: host, port: uint16(port), remoteSKI: hex.EncodeToString(leaf.SubjectKeyId),
		selectedPath: "/ship/", rejectPath: rejectPath, clientTLS: &tls.Config{
			MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: "localhost",
		},
		listener: listener, requests: make(chan string, 8), accepted: make(chan string, 8), release: make(chan struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", peer.handle)
	peer.server = &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	tlsListener := tls.NewListener(listener, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}})
	go func() { _ = peer.server.Serve(tlsListener) }()
	t.Cleanup(func() {
		peer.releaseOnce.Do(func() { close(peer.release) })
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = peer.server.Shutdown(ctx)
		_ = peer.listener.Close()
	})
	return peer
}

func (peer *msp04cr2FakePeer) options(t *testing.T, scenario string) msp04cr2SyntheticProofOptions {
	t.Helper()
	stateRoot := filepath.Join(peer.root, scenario)
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	return msp04cr2SyntheticProofOptions{
		Scenario: scenario, StateRoot: stateRoot, RemoteSKI: peer.remoteSKI,
		EndpointHost: peer.host, EndpointPort: peer.port, SelectedPath: peer.selectedPath,
		FallbackPath: "", TLSConfig: peer.clientTLS.Clone(),
	}
}

func (peer *msp04cr2FakePeer) handle(writer http.ResponseWriter, request *http.Request) {
	peer.mu.Lock()
	peer.requestTotal++
	peer.mu.Unlock()
	select {
	case peer.requests <- request.URL.Path:
	default:
	}
	if request.URL.Path == peer.rejectPath && peer.rejectPath != "" {
		http.Error(writer, "synthetic rejection", http.StatusServiceUnavailable)
		return
	}
	connection, err := upgradeMSP04CR2SHIP(writer, request)
	if err != nil {
		return
	}
	defer connection.Close()
	select {
	case peer.accepted <- request.URL.Path:
	default:
	}
	peer.mu.Lock()
	peer.acceptTotal++
	closeNow := peer.closeAfterAccepts > 0 && peer.acceptTotal <= peer.closeAfterAccepts
	peer.mu.Unlock()
	if closeNow {
		return
	}
	<-peer.release
}

func upgradeMSP04CR2SHIP(writer http.ResponseWriter, request *http.Request) (net.Conn, error) {
	key := request.Header.Get("Sec-WebSocket-Key")
	if !strings.EqualFold(request.Header.Get("Upgrade"), "websocket") ||
		!msp04cr2HeaderHasToken(request.Header.Get("Connection"), "upgrade") ||
		!msp04cr2HeaderHasToken(request.Header.Get("Sec-WebSocket-Protocol"), "ship") || key == "" {
		return nil, os.ErrInvalid
	}
	hijacker, ok := writer.(http.Hijacker)
	if !ok {
		return nil, os.ErrInvalid
	}
	connection, buffered, err := hijacker.Hijack()
	if err != nil {
		return nil, err
	}
	accept := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	response := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Protocol: ship\r\n" +
		"Sec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(accept[:]) + "\r\n\r\n"
	if _, err := buffered.WriteString(response); err != nil {
		_ = connection.Close()
		return nil, err
	}
	if err := buffered.Flush(); err != nil {
		_ = connection.Close()
		return nil, err
	}
	return connection, nil
}

func msp04cr2HeaderHasToken(value, want string) bool {
	for _, token := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(token), want) {
			return true
		}
	}
	return false
}

func (peer *msp04cr2FakePeer) requireAccept(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case <-peer.accepted:
	case <-ctx.Done():
		t.Fatal("fake TLS/SHIP peer did not observe an accepted websocket")
	}
}

func (peer *msp04cr2FakePeer) requestCount() int {
	peer.mu.Lock()
	defer peer.mu.Unlock()
	return peer.requestTotal
}

func (peer *msp04cr2FakePeer) setCloseAfterAccepts(count int) {
	peer.mu.Lock()
	defer peer.mu.Unlock()
	peer.closeAfterAccepts = count
}

func newMSP04CR2Certificate(t *testing.T) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	ski := sha1.Sum(publicKey)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "msp04cr2.invalid"},
		NotBefore: time.Unix(1_700_000_000, 0), NotAfter: time.Unix(2_000_000_000, 0),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:    []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, SubjectKeyId: ski[:],
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, leaf
}
