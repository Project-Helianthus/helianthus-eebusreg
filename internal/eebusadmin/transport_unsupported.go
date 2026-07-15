//go:build !linux && !darwin

package eebusadmin

import (
	"context"
	"errors"
	network "net"
)

type adminTransport struct{}

func Start(context.Context, string, func(context.Context, []byte) []byte) (*adminTransport, error) {
	return nil, errors.New("admin_transport_unsupported")
}

func startAdminTransport(context.Context, string, int, func(*network.UnixConn) (int, error), func(context.Context, []byte) []byte) (*adminTransport, error) {
	return nil, errors.New("admin_transport_unsupported")
}

func (*adminTransport) address() string { return "" }
func (*adminTransport) close() error    { return nil }
func (*adminTransport) Address() string { return "" }
func (*adminTransport) Close() error    { return nil }

func nativeAdminPeerUID(*network.UnixConn) (int, error) {
	return 0, errors.New("peer_credentials_unsupported")
}
