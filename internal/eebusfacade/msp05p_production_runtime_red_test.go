//go:build linux || darwin

package eebusfacade

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	eebusapi "github.com/Project-Helianthus/helianthus-eebus-go/api"
	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
	shipcert "github.com/Project-Helianthus/helianthus-ship-go/cert"
	"github.com/gorilla/websocket"
)

func TestMSP05PScopedStartupRollbackClosesTrustAndPreservesPrimaryError(t *testing.T) {
	root := canonicalRuntimeTempDir(t)
	stateRoot := filepath.Join(root, "state")
	remoteSKI := "1111111111111111111111111111111111111111"

	initial, err := loadProtectedRuntimeMaterial(context.Background(), stateRoot)
	if err != nil {
		t.Fatalf("load initial protected material: %v", err)
	}
	trust := acquireMSP05PTrustResources(t, stateRoot, filepath.Join(root, "admin-bootstrap"), initial)
	pairRuntimeRemote(t, trust, remoteSKI, 73)
	if err := trust.Close(); err != nil {
		t.Fatalf("close bootstrap trust resources: %v", err)
	}

	material, err := loadProtectedRuntimeMaterial(context.Background(), stateRoot)
	if err != nil {
		t.Fatalf("reload paired protected material: %v", err)
	}
	authorization := *material.firstTrust
	authorization.adminRuntimeDir = filepath.Join(root, "admin-failing-start")
	material.firstTrust = &authorization
	primary := errors.New("synthetic scoped listener bind failure")
	service := newMSP05PScopedService(primary)
	dependencies := defaultRuntimeDependencies
	dependencies.loadMaterial = func(context.Context, string) (runtimeMaterial, error) { return material, nil }
	dependencies.newService = func(RuntimeConfig, runtimeMaterial, eebusapi.ServiceReaderInterface) (runtimeService, error) {
		return service, nil
	}

	failed, err := acquireRuntime(context.Background(), RuntimeConfig{
		StateRoot: stateRoot, Interface: "fixture-interface", ListenPort: 4711,
		ListenAddress: netip.MustParseAddrPort("127.0.0.1:4711"),
		Remotes:       []RuntimeRemote{{SKI: remoteSKI}},
	}, dependencies)
	if failed != nil || !errors.Is(err, primary) {
		t.Fatalf("scoped startup result backend=%v error=%v, want primary bind error", failed, err)
	}
	if got, want := service.eventsSnapshot(), []string{"setup", "register:" + remoteSKI, "start-policy", "shutdown"}; !slices.Equal(got, want) {
		t.Fatalf("scoped startup events = %v, want %v", got, want)
	}
	if service.shutdownCount() != 1 {
		t.Fatalf("scoped startup shutdown count = %d, want 1", service.shutdownCount())
	}
	entries, readErr := os.ReadDir(authorization.adminRuntimeDir)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		t.Fatalf("inspect first-trust admin directory after rollback: %v", readErr)
	}
	for _, entry := range entries {
		info, statErr := entry.Info()
		if statErr != nil {
			t.Fatal(statErr)
		}
		if info.Mode()&os.ModeSocket != 0 {
			t.Fatalf("first-trust admin socket %q survived scoped startup rollback", entry.Name())
		}
	}
	reloaded, err := loadProtectedRuntimeMaterial(context.Background(), stateRoot)
	if err != nil {
		t.Fatalf("protected store remained owned after scoped startup rollback: %v", err)
	}
	if !reloaded.pretrusted[remoteSKI] {
		t.Fatal("scoped startup rollback discarded durable trust")
	}
}

func TestMSP05PServiceBackendReportsListenerTerminalAndClaimsPublisherOnce(t *testing.T) {
	localSKI := "0000000000000000000000000000000000000001"
	remoteSKI := "0000000000000000000000000000000000000002"
	handler, err := newRuntimeServiceHandler(RuntimeConfig{
		Remotes: []RuntimeRemote{{SKI: remoteSKI, Allowlisted: true}},
	}, localSKI, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	service := newMSP05PScopedService(nil)
	dependencies := runtimeDependencies{
		loadMaterial: func(context.Context, string) (runtimeMaterial, error) {
			certificate, certificateErr := shipcert.CreateCertificate("", "Helianthus", "RO", "scoped-backend-test")
			if certificateErr != nil {
				return runtimeMaterial{}, certificateErr
			}
			return runtimeMaterial{certificate: certificate, localSKI: certificateSKI(t, certificate), pretrusted: map[string]bool{remoteSKI: true}}, nil
		},
		newService: func(RuntimeConfig, runtimeMaterial, eebusapi.ServiceReaderInterface) (runtimeService, error) {
			return service, nil
		},
		now: time.Now,
	}
	backend, err := acquireRuntime(context.Background(), RuntimeConfig{
		StateRoot: filepath.Join(canonicalRuntimeTempDir(t), "state"), Interface: "fixture-interface", ListenPort: 4711,
		ListenAddress: netip.MustParseAddrPort("127.0.0.1:4711"), Remotes: []RuntimeRemote{{SKI: remoteSKI}},
	}, dependencies)
	if err != nil {
		t.Fatalf("acquire scoped backend: %v", err)
	}
	implementation := backend.(*serviceBackend)
	implementation.handler = handler

	runContext, cancelRuns := context.WithCancel(context.Background())
	defer cancelRuns()
	firstPublished := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- backend.Run(runContext, func([]byte) {
			close(firstPublished)
			<-releaseFirst
		})
	}()
	select {
	case <-firstPublished:
	case <-time.After(time.Second):
		t.Fatal("first Run did not publish")
	}
	secondPublished := make(chan struct{}, 1)
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- backend.Run(runContext, func([]byte) { secondPublished <- struct{}{} })
	}()
	select {
	case <-secondPublished:
		close(releaseFirst)
		cancelRuns()
		_ = backend.Close()
		<-firstDone
		<-secondDone
		t.Fatal("rejected Run replaced or invoked the active publisher")
	case secondErr := <-secondDone:
		if secondErr == nil || !strings.Contains(secondErr.Error(), "already running") {
			t.Fatalf("second Run error = %v, want ownership rejection", secondErr)
		}
	case <-time.After(time.Second):
		close(releaseFirst)
		cancelRuns()
		_ = backend.Close()
		<-firstDone
		t.Fatal("second Run did not reject concurrent ownership")
	}
	select {
	case <-secondPublished:
		t.Fatal("rejected Run invoked the active publisher")
	default:
	}
	close(releaseFirst)
	terminal := errors.New("synthetic listener terminal")
	service.terminal <- terminal
	select {
	case err := <-firstDone:
		if !errors.Is(err, terminal) {
			t.Fatalf("Run terminal error = %v, want %v", err, terminal)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not observe listener terminalization")
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
}

type msp05pScopedService struct {
	*fakeRuntimeService
	mu             sync.Mutex
	events         []string
	startPolicyErr error
	terminal       chan error
}

func newMSP05PScopedService(startErr error) *msp05pScopedService {
	return &msp05pScopedService{
		fakeRuntimeService: &fakeRuntimeService{started: make(chan struct{})},
		startPolicyErr:     startErr,
		terminal:           make(chan error, 1),
	}
}

func (service *msp05pScopedService) Setup() error {
	service.record("setup")
	return service.fakeRuntimeService.Setup()
}

func (service *msp05pScopedService) RegisterRemoteSKI(ski string) {
	service.record("register:" + ski)
	service.fakeRuntimeService.RegisterRemoteSKI(ski)
}

func (service *msp05pScopedService) StartWithPolicy() error {
	service.record("start-policy")
	return service.startPolicyErr
}

func (service *msp05pScopedService) ListenerTerminal() <-chan error { return service.terminal }

func (service *msp05pScopedService) Shutdown() {
	service.record("shutdown")
	service.fakeRuntimeService.Shutdown()
}

func (service *msp05pScopedService) record(event string) {
	service.mu.Lock()
	service.events = append(service.events, event)
	service.mu.Unlock()
}

func (service *msp05pScopedService) eventsSnapshot() []string {
	service.mu.Lock()
	defer service.mu.Unlock()
	return append([]string(nil), service.events...)
}

func (service *msp05pScopedService) shutdownCount() int {
	service.fakeRuntimeService.mu.Lock()
	defer service.fakeRuntimeService.mu.Unlock()
	return service.fakeRuntimeService.shutdowns
}

func TestMSP05PProductionRuntimeScopesListenerDisablesDiscoveryAndDeniesUnknownTrust(t *testing.T) {
	root := canonicalRuntimeTempDir(t)
	stateRoot := filepath.Join(root, "state")
	alternate, endpoint := msp05pProductionScopedEndpoint(t)
	if alternate != nil {
		defer alternate.Close()
	}

	instance, err := Acquire(context.Background(), msp05pProductionConfig(stateRoot, endpoint))
	if err != nil {
		t.Fatalf("acquire production runtime: %v", err)
	}
	runCancel, runDone := msp05pProductionRun(t, instance)
	defer runCancel()

	before := msp05pProductionStateDigest(t, stateRoot)
	peerCertificate, err := shipcert.CreateCertificate("", "Helianthus", "RO", "unknown-peer")
	if err != nil {
		t.Fatalf("create unknown peer certificate: %v", err)
	}
	dialer := websocket.Dialer{
		TLSClientConfig: &tls.Config{
			Certificates:       []tls.Certificate{peerCertificate},
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: true, //nolint:gosec -- isolated disposable loopback proof
		},
		Subprotocols: []string{shipapi.ShipWebsocketSubProtocol},
	}
	connection, response, err := dialer.Dial("wss://"+endpoint.String()+"/ship/", nil)
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if err != nil {
		t.Fatalf("connect unknown SHIP peer: %v", err)
	}
	_ = connection.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	_, _, _ = connection.ReadMessage()
	_ = connection.Close()
	time.Sleep(100 * time.Millisecond)

	after := msp05pProductionStateDigest(t, stateRoot)
	if before != after {
		t.Fatal("closed pairing persisted trust for an unknown peer")
	}
	if err := instance.Close(); err != nil {
		t.Fatalf("close production runtime: %v", err)
	}
	if err := instance.Close(); err != nil {
		t.Fatalf("repeat production runtime close: %v", err)
	}
	runCancel()
	msp05pProductionWaitRun(t, runDone)

	listener, err := net.ListenTCP("tcp4", net.TCPAddrFromAddrPort(endpoint))
	if err != nil {
		t.Fatalf("exact listener address was not released: %v", err)
	}
	_ = listener.Close()
}

func TestMSP05PProductionRuntimeBindFailureRollsBackAndRestartSucceeds(t *testing.T) {
	root := canonicalRuntimeTempDir(t)
	stateRoot := filepath.Join(root, "state")
	held, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("hold production endpoint: %v", err)
	}
	endpoint := held.Addr().(*net.TCPAddr).AddrPort()

	failed, err := Acquire(context.Background(), msp05pProductionConfig(stateRoot, endpoint))
	if failed != nil {
		_ = failed.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "bind SHIP listener") {
		t.Fatalf("occupied endpoint acquire error = %v, want scoped bind failure", err)
	}
	if err := held.Close(); err != nil {
		t.Fatalf("release occupied endpoint: %v", err)
	}

	restarted, err := Acquire(context.Background(), msp05pProductionConfig(stateRoot, endpoint))
	if err != nil {
		t.Fatalf("restart after scoped bind rollback: %v", err)
	}
	runCancel, runDone := msp05pProductionRun(t, restarted)
	if err := restarted.Close(); err != nil {
		t.Fatalf("close restarted runtime: %v", err)
	}
	if err := restarted.Close(); err != nil {
		t.Fatalf("repeat close restarted runtime: %v", err)
	}
	runCancel()
	msp05pProductionWaitRun(t, runDone)

	listener, err := net.ListenTCP("tcp4", net.TCPAddrFromAddrPort(endpoint))
	if err != nil {
		t.Fatalf("restart listener leaked after shutdown: %v", err)
	}
	_ = listener.Close()
}

func msp05pProductionConfig(stateRoot string, endpoint netip.AddrPort) RuntimeConfig {
	return RuntimeConfig{
		StateRoot:        stateRoot,
		Interface:        "helianthus-msp05p-missing-interface",
		ListenPort:       int(endpoint.Port()),
		ListenAddress:    endpoint,
		DiscoveryEnabled: false,
		Remotes:          []RuntimeRemote{},
	}
}

func msp05pProductionScopedEndpoint(t *testing.T) (*net.TCPListener, netip.AddrPort) {
	t.Helper()
	for attempt := 0; attempt < 32; attempt++ {
		alternate, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.ParseIP("127.0.0.2"), Port: 0})
		if err != nil {
			listener, listenErr := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
			if listenErr != nil {
				t.Fatalf("allocate loopback endpoint after alternate-address rejection: %v", listenErr)
			}
			endpoint := listener.Addr().(*net.TCPAddr).AddrPort()
			_ = listener.Close()
			return nil, endpoint
		}
		port := alternate.Addr().(*net.TCPAddr).Port
		endpoint := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(port))
		probe, probeErr := net.ListenTCP("tcp4", net.TCPAddrFromAddrPort(endpoint))
		if probeErr == nil {
			_ = probe.Close()
			return alternate, endpoint
		}
		_ = alternate.Close()
	}
	t.Fatal("could not allocate exact and alternate loopback addresses")
	return nil, netip.AddrPort{}
}

func msp05pProductionRun(t *testing.T, backend Backend) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	updates := make(chan []byte, 1)
	done := make(chan error, 1)
	go func() {
		done <- backend.Run(ctx, func(payload []byte) {
			select {
			case updates <- append([]byte(nil), payload...):
			default:
			}
		})
	}()
	select {
	case <-updates:
	case err := <-done:
		cancel()
		t.Fatalf("production runtime stopped before initial snapshot: %v", err)
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("production runtime did not publish its initial snapshot")
	}
	return cancel, done
}

func msp05pProductionWaitRun(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("production runtime Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("production runtime Run did not stop")
	}
}

func msp05pProductionStateDigest(t *testing.T, root string) [sha256.Size]byte {
	t.Helper()
	hash := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprint(hash, relative, "\x00", info.Mode().Type(), "\x00", info.Mode().Perm(), "\x00")
		if entry.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unexpected protected state entry %s", relative)
		}
		payload, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(payload)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write(payload)
		return nil
	})
	if err != nil {
		t.Fatalf("digest protected state: %v", err)
	}
	var result [sha256.Size]byte
	copy(result[:], hash.Sum(nil))
	if result == [sha256.Size]byte{} || bytes.Equal(result[:], make([]byte, sha256.Size)) {
		t.Fatal("protected state digest is empty")
	}
	return result
}
