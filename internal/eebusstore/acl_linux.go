//go:build linux

package eebusstore

import (
	"bytes"
	"errors"
	"strings"

	"golang.org/x/sys/unix"
)

func inspectAdditionalAccess(fd int) (bool, error) {
	const (
		maximumXattrListBytes = 64 << 10
		maximumXattrCount     = 256
		maximumXattrNameBytes = 255
	)
	size, err := unix.Flistxattr(fd, nil)
	if err != nil {
		return false, err
	}
	if size == 0 {
		return false, nil
	}
	if size < 0 || size > maximumXattrListBytes {
		return false, errors.New("extended attribute list bound exceeded")
	}
	buffer := make([]byte, size)
	read, err := unix.Flistxattr(fd, buffer)
	if err != nil {
		return false, err
	}
	if read <= 0 || read > len(buffer) || buffer[read-1] != 0 {
		return false, errors.New("ambiguous extended attribute list")
	}
	names := bytes.Split(buffer[:read-1], []byte{0})
	if len(names) > maximumXattrCount {
		return false, errors.New("extended attribute count bound exceeded")
	}
	for _, name := range names {
		if len(name) == 0 || len(name) > maximumXattrNameBytes {
			return false, errors.New("ambiguous extended attribute name")
		}
		lower := strings.ToLower(string(name))
		if strings.Contains(lower, "acl") ||
			strings.Contains(lower, "access_control") ||
			strings.Contains(lower, "security_descriptor") ||
			lower == "security.capability" {
			return true, nil
		}
	}
	return false, nil
}
