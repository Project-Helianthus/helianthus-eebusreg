//go:build darwin

package eebusfacade

import (
	"crypto/sha256"
	"encoding/binary"
	"io"
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func nativeMachineIdentity() ([sha256.Size]byte, error) {
	for _, key := range []string{"kern.hostuuid", "kern.uuid"} {
		value, err := unix.Sysctl(key)
		value = strings.TrimSpace(value)
		if err == nil && len(value) >= 8 {
			hash := sha256.New()
			_, _ = io.WriteString(hash, "helianthus-eebusreg/darwin-machine/v1\x00")
			_, _ = io.WriteString(hash, key)
			_, _ = hash.Write([]byte{0})
			_, _ = io.WriteString(hash, value)
			var identity [sha256.Size]byte
			copy(identity[:], hash.Sum(nil))
			return identity, nil
		}
	}

	var root syscall.Stat_t
	if err := syscall.Stat("/", &root); err != nil {
		return [sha256.Size]byte{}, errNativeProtectedBindingUnavailable
	}
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		return [sha256.Size]byte{}, errNativeProtectedBindingUnavailable
	}
	hash := sha256.New()
	_, _ = io.WriteString(hash, "helianthus-eebusreg/darwin-machine/v1\x00root-node")
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(root.Dev))
	_, _ = hash.Write(encoded[:])
	binary.BigEndian.PutUint64(encoded[:], uint64(root.Ino))
	_, _ = hash.Write(encoded[:])
	_, _ = io.WriteString(hash, hostname)
	var identity [sha256.Size]byte
	copy(identity[:], hash.Sum(nil))
	return identity, nil
}
