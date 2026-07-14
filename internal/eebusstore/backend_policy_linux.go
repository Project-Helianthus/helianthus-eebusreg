//go:build linux

package eebusstore

import (
	"errors"

	"golang.org/x/sys/unix"
)

const (
	extFamilyMagic = 0xef53
	xfsMagic       = 0x58465342
	btrfsMagic     = 0x9123683e
	overlayfsMagic = 0x794c7630
)

func qualifyFilesystemPolicy(fd int) error {
	var stat unix.Statfs_t
	if err := unix.Fstatfs(fd, &stat); err != nil {
		return err
	}
	switch uint64(stat.Type) {
	case extFamilyMagic, xfsMagic, btrfsMagic, overlayfsMagic:
		return nil
	default:
		return errors.New("filesystem is outside the supported Linux policy")
	}
}
