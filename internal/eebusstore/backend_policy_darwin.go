//go:build darwin

package eebusstore

import (
	"errors"

	"golang.org/x/sys/unix"
)

func qualifyFilesystemPolicy(fd int) error {
	var stat unix.Statfs_t
	if err := unix.Fstatfs(fd, &stat); err != nil {
		return err
	}
	name := make([]byte, 0, len(stat.Fstypename))
	for _, character := range stat.Fstypename {
		if character == 0 {
			break
		}
		name = append(name, byte(character))
	}
	if string(name) != "apfs" {
		return errors.New("filesystem is outside the supported Darwin policy")
	}
	return nil
}
