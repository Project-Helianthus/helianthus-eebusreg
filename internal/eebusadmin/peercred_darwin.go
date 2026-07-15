//go:build darwin

package eebusadmin

import (
	"errors"
	network "net"

	"golang.org/x/sys/unix"
)

func nativeAdminPeerUID(connection *network.UnixConn) (int, error) {
	if connection == nil {
		return 0, errors.New("peer_credentials_unavailable")
	}
	raw, err := connection.SyscallConn()
	if err != nil {
		return 0, errors.New("peer_credentials_unavailable")
	}
	uid := -1
	var credentialErr error
	if err := raw.Control(func(descriptor uintptr) {
		credentials, err := unix.GetsockoptXucred(int(descriptor), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if err != nil || credentials == nil || credentials.Version != 0 || credentials.Ngroups < 0 || int(credentials.Ngroups) > len(credentials.Groups) {
			credentialErr = errors.New("peer_credentials_unavailable")
			return
		}
		uid = int(credentials.Uid)
	}); err != nil {
		return 0, errors.New("peer_credentials_unavailable")
	}
	if credentialErr != nil || uid < 0 {
		return 0, errors.New("peer_credentials_unavailable")
	}
	return uid, nil
}
