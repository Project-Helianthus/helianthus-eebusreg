//go:build linux || darwin

package eebusfacade

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
	shipcert "github.com/Project-Helianthus/helianthus-ship-go/cert"
	"github.com/gorilla/websocket"
)

func TestMSP05PProductionRuntimeScopesListenerDisablesDiscoveryAndDeniesUnknownTrust(t *testing.T) {
	root := msp05pProductionTempRoot(t)
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
	root := msp05pProductionTempRoot(t)
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

func msp05pProductionTempRoot(t *testing.T) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatalf("resolve temporary directory: %v", err)
	}
	root, err := os.MkdirTemp(base, "eebusreg-msp05p-runtime-")
	if err != nil {
		t.Fatalf("create production runtime root: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove production runtime root: %v", err)
		}
	})
	return root
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
