package eebusadmin

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMSP04BAdminUsesOwnerOnlyAFUNIXAndNativeSameUID(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("native peer-credential proof is defined for Linux and Darwin")
	}
	runtimeDir := msp04bCanonicalRuntimeDir(t)
	handled := make(chan struct{}, 1)
	transport, err := startAdminTransport(
		context.Background(),
		runtimeDir,
		os.Geteuid(),
		nativeAdminPeerUID,
		func(context.Context, []byte) []byte {
			handled <- struct{}{}
			return []byte("ok")
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := transport.close(); err != nil {
			t.Errorf("close admin transport: %v", err)
		}
	})

	assertMSP04BMode(t, runtimeDir, 0o700)
	socketPath := transport.address()
	if !filepath.IsAbs(socketPath) || filepath.Dir(socketPath) != runtimeDir {
		t.Fatal("admin endpoint was not confined to its configured runtime directory")
	}
	assertMSP04BMode(t, socketPath, 0o600)

	connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	frame, err := encodeAdminFrame([]byte("request"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Write(frame); err != nil {
		t.Fatal(err)
	}
	response, err := readAdminFrame(connection)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(response, []byte("ok")) {
		t.Fatal("unexpected same-UID response")
	}
	select {
	case <-handled:
	case <-time.After(2 * time.Second):
		t.Fatal("same-UID request was not delivered")
	}
}

func TestMSP04BAdminRejectsWrongOrMissingCredentialsBeforeBodyRead(t *testing.T) {
	tests := []struct {
		name string
		peer func(*net.UnixConn) (int, error)
	}{
		{name: "wrong uid", peer: func(*net.UnixConn) (int, error) { return os.Geteuid() + 1, nil }},
		{name: "missing credentials", peer: func(*net.UnixConn) (int, error) { return 0, errors.New("credentials unavailable") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var handled atomic.Int32
			transport, err := startAdminTransport(
				context.Background(),
				msp04bCanonicalRuntimeDir(t),
				os.Geteuid(),
				test.peer,
				func(context.Context, []byte) []byte {
					handled.Add(1)
					return nil
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			defer transport.close()
			connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: transport.address(), Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			if err := connection.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatal(err)
			}
			buffer := make([]byte, 1)
			if _, err := connection.Read(buffer); err == nil {
				t.Fatal("unauthenticated connection remained open awaiting a body")
			}
			if handled.Load() != 0 {
				t.Fatal("unauthenticated connection reached the command handler")
			}
		})
	}
}

func TestMSP04BAdminFramingIsBoundedWithoutFreezingPrivateBytes(t *testing.T) {
	if maxAdminFrameBytes <= 0 {
		t.Fatal("admin frame bound must be nonzero")
	}
	payload := []byte("opaque-request")
	frame, err := encodeAdminFrame(payload)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := readAdminFrame(bytes.NewReader(frame))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, payload) {
		t.Fatal("framing round trip changed the opaque payload")
	}
	if _, err := encodeAdminFrame(make([]byte, maxAdminFrameBytes+1)); err == nil {
		t.Fatal("encoder accepted an oversized frame")
	}

	body := &msp04bCountingReader{reader: bytes.NewReader([]byte("unread"))}
	declaredOversize := io.MultiReader(bytes.NewReader(adminFrameHeader(uint64(maxAdminFrameBytes+1))), body)
	if _, err := readAdminFrame(declaredOversize); err == nil {
		t.Fatal("decoder accepted an oversized declared length")
	}
	if body.reads.Load() != 0 {
		t.Fatal("decoder read a body after an oversized declared length")
	}

	header := adminFrameHeader(2)
	if len(header) < 2 {
		t.Fatal("length-delimited frame header is unexpectedly empty")
	}
	if _, err := readAdminFrame(bytes.NewReader(header[:len(header)-1])); err == nil {
		t.Fatal("decoder accepted a partial frame header")
	}
	partialBody := io.MultiReader(bytes.NewReader(header), bytes.NewReader([]byte{1}))
	if _, err := readAdminFrame(partialBody); err == nil {
		t.Fatal("decoder accepted a partial frame body")
	}
}

func TestMSP04BPeerCredentialContractsAreStaticForTheOtherOS(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var linuxContract, darwinContract bool
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		payload, err := os.ReadFile(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		source := string(payload)
		if strings.Contains(source, "//go:build linux") && strings.Contains(source, "SO_PEERCRED") {
			linuxContract = true
		}
		if strings.Contains(source, "//go:build darwin") && strings.Contains(source, "LOCAL_PEERCRED") && strings.Contains(source, "GetsockoptXucred") {
			darwinContract = true
		}
	}
	if !linuxContract || !darwinContract {
		t.Fatalf("peer credential source contracts linux=%t darwin=%t; only runtime.GOOS=%s is native proof in this run", linuxContract, darwinContract, runtime.GOOS)
	}
}

type msp04bCountingReader struct {
	reader io.Reader
	reads  atomic.Int32
}

func (reader *msp04bCountingReader) Read(payload []byte) (int, error) {
	reader.reads.Add(1)
	return reader.reader.Read(payload)
}

func assertMSP04BMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode = %04o, want %04o", got, want)
	}
}

func msp04bCanonicalRuntimeDir(t *testing.T) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(base, "eebusadmin-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove admin test directory: %v", err)
		}
	})
	return filepath.Join(root, "admin")
}
