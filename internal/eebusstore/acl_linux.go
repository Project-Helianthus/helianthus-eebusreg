//go:build linux

package eebusstore

import (
	"bytes"

	"golang.org/x/sys/unix"
)

func inspectAdditionalAccess(fd int) (bool, error) {
	size, err := unix.Flistxattr(fd, nil)
	if err != nil {
		return false, err
	}
	if size == 0 {
		return false, nil
	}
	buffer := make([]byte, size)
	read, err := unix.Flistxattr(fd, buffer)
	if err != nil {
		return false, err
	}
	for _, name := range bytes.Split(buffer[:read], []byte{0}) {
		if bytes.Equal(name, []byte("system.posix_acl_access")) || bytes.Equal(name, []byte("system.posix_acl_default")) {
			return true, nil
		}
	}
	return false, nil
}
