//go:build darwin

package eebusfacade

import (
	"crypto/sha256"
	"io"
	"strings"

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

	return [sha256.Size]byte{}, errNativeProtectedBindingUnavailable
}
