package eebusfacade

import "context"

const (
	// RemoteTransportConnected is the notice kind sent from RemoteSKIConnected.
	RemoteTransportConnected = "connected"
	// RemoteTransportDisconnected is the notice kind sent from RemoteSKIDisconnected.
	RemoteTransportDisconnected = "disconnected"
)

// RemoteTransportNotice is one SHIP connection notice for one remote SKI.
// Kind is RemoteTransportConnected or RemoteTransportDisconnected.
type RemoteTransportNotice struct {
	Kind string
	SKI  string
}

// RemoteTransportFunc receives SHIP connection notices for one runtime instance.
type RemoteTransportFunc func(RemoteTransportNotice)

type remoteTransportContextKey struct{}

// WithRemoteTransport returns a child of ctx that carries notify.
// Acquire copies that function onto the instance handler. A nil context uses
// context.Background. A nil notify leaves the handler without a receiver.
func WithRemoteTransport(ctx context.Context, notify RemoteTransportFunc) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if notify == nil {
		return ctx
	}
	return context.WithValue(ctx, remoteTransportContextKey{}, notify)
}

// RemoteTransportFrom returns the notice function carried by ctx, or nil.
func RemoteTransportFrom(ctx context.Context) RemoteTransportFunc {
	if ctx == nil {
		return nil
	}
	notify, _ := ctx.Value(remoteTransportContextKey{}).(RemoteTransportFunc)
	return notify
}

func bindRemoteTransport(handler *runtimeServiceHandler, ctx context.Context) {
	if handler == nil {
		return
	}
	notify := RemoteTransportFrom(ctx)
	handler.mu.Lock()
	handler.remoteTransport = notify
	handler.mu.Unlock()
}

func (handler *runtimeServiceHandler) notifyTransport(kind, ski string) {
	if handler == nil || ski == "" {
		return
	}
	handler.mu.Lock()
	notify := handler.remoteTransport
	handler.mu.Unlock()
	if notify == nil {
		return
	}
	notify(RemoteTransportNotice{Kind: kind, SKI: ski})
}
