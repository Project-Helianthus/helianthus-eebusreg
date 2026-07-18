//go:build linux

package eebusfacade

import (
	"crypto/sha256"
	"encoding/binary"
	"io"
	"os"
	"strings"
	"syscall"
)

func nativeMachineIdentity() ([sha256.Size]byte, error) {
	for _, candidate := range []struct {
		label string
		path  string
	}{
		{label: "machine-id", path: "/etc/machine-id"},
		{label: "dbus-machine-id", path: "/var/lib/dbus/machine-id"},
		{label: "dmi-product-uuid", path: "/sys/class/dmi/id/product_uuid"},
		{label: "dmi-board-serial", path: "/sys/class/dmi/id/board_serial"},
		{label: "dmi-product-serial", path: "/sys/class/dmi/id/product_serial"},
	} {
		if identity, ok := nativeMachineIdentityFile(candidate.label, candidate.path); ok {
			return identity, nil
		}
	}

	bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil || len(strings.TrimSpace(string(bootID))) < 8 {
		return [sha256.Size]byte{}, errNativeProtectedBindingUnavailable
	}
	var root syscall.Stat_t
	if err := syscall.Stat("/", &root); err != nil {
		return [sha256.Size]byte{}, errNativeProtectedBindingUnavailable
	}
	hostname, _ := os.Hostname()
	hash := sha256.New()
	_, _ = io.WriteString(hash, "helianthus-eebusreg/linux-machine/v1\x00boot-root")
	_, _ = hash.Write([]byte(strings.TrimSpace(string(bootID))))
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

func nativeMachineIdentityFile(label, path string) ([sha256.Size]byte, bool) {
	payload, err := os.ReadFile(path)
	value := strings.TrimSpace(string(payload))
	if err != nil || len(value) < 8 || len(value) > 4096 {
		return [sha256.Size]byte{}, false
	}
	hash := sha256.New()
	_, _ = io.WriteString(hash, "helianthus-eebusreg/linux-machine/v1\x00")
	_, _ = io.WriteString(hash, label)
	_, _ = hash.Write([]byte{0})
	_, _ = io.WriteString(hash, value)
	var identity [sha256.Size]byte
	copy(identity[:], hash.Sum(nil))
	return identity, true
}
