package eebusruntime

import (
	"context"
	"testing"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

type fakeRuntime struct {
	pairing eebusraw.PairingState
}

var _ Runtime = (*fakeRuntime)(nil)

func (r *fakeRuntime) Start(context.Context) error { return nil }

func (r *fakeRuntime) Shutdown() error { return nil }

func (r *fakeRuntime) Snapshot() (eebusraw.Snapshot, error) {
	return eebusraw.Snapshot{}, nil
}

func (r *fakeRuntime) PairingState() eebusraw.PairingState { return r.pairing }

func (r *fakeRuntime) RegisterRemoteSKI(eebusraw.SKI) error { return nil }

func (r *fakeRuntime) UnregisterRemoteSKI(eebusraw.SKI) error { return nil }

func (r *fakeRuntime) SetPairingWindow(bool) error { return nil }

func TestRuntimeInterfaceShape(t *testing.T) {
	r := &fakeRuntime{pairing: eebusraw.PairingState{
		State: eebusraw.PairingWindowOpen,
	}}

	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if r.PairingState().State != eebusraw.PairingWindowOpen {
		t.Fatalf("pairing state = %q", r.PairingState().State)
	}
	if err := r.Shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}
