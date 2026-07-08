package eebusruntime

import (
	"context"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

// Runtime is the public eeBUS raw runtime boundary consumed by later gateway
// sidecar work. Implementations are introduced behind this interface in later
// milestones.
type Runtime interface {
	Start(context.Context) error
	Shutdown() error
	Snapshot() (eebusraw.Snapshot, error)
	PairingState() eebusraw.PairingState
	RegisterRemoteSKI(eebusraw.SKI) error
	UnregisterRemoteSKI(eebusraw.SKI) error
	SetPairingWindow(enabled bool) error
}

// Identity identifies a Helianthus eeBUS runtime instance without exposing
// third-party runtime types.
type Identity struct {
	RuntimeID string
	LocalSKI  eebusraw.SKI
	Contract  string
}
