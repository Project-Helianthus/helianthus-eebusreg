//go:build darwin && cgo

package eebusstore

/*
#include <errno.h>
#include <sys/acl.h>

static int helianthus_acl_entry_count(int fd) {
	errno = 0;
	acl_t acl = acl_get_fd_np(fd, ACL_TYPE_EXTENDED);
	if (acl == NULL) {
		if (errno == ENOENT) return 0;
		return -errno;
	}
	acl_entry_t entry;
	errno = 0;
	int result = acl_get_entry(acl, ACL_FIRST_ENTRY, &entry);
	acl_free(acl);
	if (result == 0) return 1;
	if (errno == 0) return 0;
	if (errno == ENOENT) return 0;
	return -errno;
}
*/
import "C"

import (
	"errors"
	"syscall"
)

func inspectAdditionalAccess(fd int) (bool, error) {
	result := int(C.helianthus_acl_entry_count(C.int(fd)))
	if result < 0 {
		return false, syscall.Errno(-result)
	}
	if result > 1 {
		return false, errors.New("ambiguous ACL result")
	}
	return result == 1, nil
}
