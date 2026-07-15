package eebusadmin

import (
	"bytes"
	"context"
	network "net"
	"os"
	"path/filepath"
	"testing"
)

func TestAdminLifecycleRejectsSymlinkAndExistingWrongType(t *testing.T) {
	t.Run("symlink directory", func(t *testing.T) {
		runtimeDir := msp04bCanonicalRuntimeDir(t)
		target := filepath.Join(filepath.Dir(runtimeDir), "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, runtimeDir); err != nil {
			t.Fatal(err)
		}
		transport, err := startAdminTransport(context.Background(), runtimeDir, os.Geteuid(), nativeAdminPeerUID, msp04bAdminEcho)
		if err == nil || transport != nil {
			t.Fatal("symlink runtime directory was accepted")
		}
	})

	t.Run("existing regular file", func(t *testing.T) {
		runtimeDir := msp04bCanonicalRuntimeDir(t)
		if err := os.Mkdir(runtimeDir, 0o700); err != nil {
			t.Fatal(err)
		}
		socketPath := filepath.Join(runtimeDir, adminSocketName)
		if err := os.WriteFile(socketPath, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		transport, err := startAdminTransport(context.Background(), runtimeDir, os.Geteuid(), nativeAdminPeerUID, msp04bAdminEcho)
		if err == nil || transport != nil {
			t.Fatal("existing non-socket path was accepted")
		}
		info, statErr := os.Lstat(socketPath)
		if statErr != nil || !info.Mode().IsRegular() {
			t.Fatal("rejected existing path was removed or changed")
		}
	})
}

func TestAdminLifecycleDistinguishesActiveAndStaleSockets(t *testing.T) {
	t.Run("active", func(t *testing.T) {
		runtimeDir := msp04bCanonicalRuntimeDir(t)
		if err := os.Mkdir(runtimeDir, 0o700); err != nil {
			t.Fatal(err)
		}
		socketPath := filepath.Join(runtimeDir, adminSocketName)
		listener, err := network.ListenUnix("unix", &network.UnixAddr{Name: socketPath, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		listener.SetUnlinkOnClose(false)
		defer listener.Close()
		if err := os.Chmod(socketPath, 0o600); err != nil {
			t.Fatal(err)
		}
		transport, err := startAdminTransport(context.Background(), runtimeDir, os.Geteuid(), nativeAdminPeerUID, msp04bAdminEcho)
		if err == nil || transport != nil {
			t.Fatal("active listener was replaced")
		}
		if _, err := os.Lstat(socketPath); err != nil {
			t.Fatal("active socket path was removed")
		}
	})

	t.Run("stale", func(t *testing.T) {
		runtimeDir := msp04bCanonicalRuntimeDir(t)
		if err := os.Mkdir(runtimeDir, 0o700); err != nil {
			t.Fatal(err)
		}
		socketPath := filepath.Join(runtimeDir, adminSocketName)
		listener, err := network.ListenUnix("unix", &network.UnixAddr{Name: socketPath, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		listener.SetUnlinkOnClose(false)
		if err := os.Chmod(socketPath, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
		transport, err := startAdminTransport(context.Background(), runtimeDir, os.Geteuid(), nativeAdminPeerUID, msp04bAdminEcho)
		if err != nil {
			t.Fatal(err)
		}
		if err := transport.close(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(socketPath); !os.IsNotExist(err) {
			t.Fatal("owned socket was not removed on shutdown")
		}
	})
}

func TestAdminShutdownLeavesSubstitutedPathUntouched(t *testing.T) {
	runtimeDir := msp04bCanonicalRuntimeDir(t)
	transport, err := startAdminTransport(context.Background(), runtimeDir, os.Geteuid(), nativeAdminPeerUID, msp04bAdminEcho)
	if err != nil {
		t.Fatal(err)
	}
	heldDir := runtimeDir + ".held"
	if err := os.Rename(runtimeDir, heldDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(runtimeDir, adminSocketName)
	if err := os.WriteFile(replacement, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := transport.close(); err == nil {
		t.Fatal("pathname substitution was not reported")
	}
	info, err := os.Lstat(replacement)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatal("substituted path was removed or changed")
	}
}

func TestAdminFramingRejectsUnknownVersion(t *testing.T) {
	frame, err := encodeAdminFrame([]byte("opaque"))
	if err != nil {
		t.Fatal(err)
	}
	frame[0]++
	if _, err := readAdminFrame(bytes.NewReader(frame)); err == nil {
		t.Fatal("unknown frame version was accepted")
	}
}

func msp04bAdminEcho(_ context.Context, payload []byte) []byte {
	return append([]byte(nil), payload...)
}
