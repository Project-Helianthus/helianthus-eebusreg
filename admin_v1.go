package eebusruntime

import (
	"context"
	"errors"
	"sync"
	"time"
)

var errAdminV1Unavailable = errors.New("eebus AdminV1 is unavailable")

// AdminV1 is the in-process, owner-only runtime administration facade. It
// exposes neither transport coordinates nor durable-control bindings.
type AdminV1 interface {
	Snapshot(context.Context) (AdminSnapshotV1, error)
	OpenPairingWindow(context.Context, time.Duration, uint64) (ActionHandleV1, error)
	ClosePairingWindow(context.Context, uint64) (ActionHandleV1, error)
	Select(context.Context, ObservationHandleV1, string, uint64) (SelectionHandleV1, error)
	Connect(context.Context, SelectionHandleV1, uint64) (ActionHandleV1, error)
	Confirm(context.Context, CandidateHandleV1, string, uint64) (ActionHandleV1, error)
	Cancel(context.Context, CandidateHandleV1, uint64) (ActionHandleV1, error)
	Retry(context.Context, PartnerHandleV1, uint64) (ActionHandleV1, error)
	Untrust(context.Context, PartnerHandleV1, uint64) (ActionHandleV1, error)
}

// The handles are intentionally sealed. Only an AdminV1 implementation may
// create a usable value; callers may only retain one returned by this runtime.
type PartnerHandleV1 struct{ token [16]byte }
type ObservationHandleV1 struct{ token [16]byte }
type SelectionHandleV1 struct{ token [16]byte }
type CandidateHandleV1 struct{ token [16]byte }
type ActionHandleV1 struct{ token [16]byte }

type AdminSnapshotV1 struct {
	Trusted    []TrustedPartnerV1
	Connected  []ConnectedPartnerV1
	Discovered []DiscoveredPartnerV1
	Candidates []PairingCandidateV1
}

type TrustedPartnerV1 struct{ SKI string }
type ConnectedPartnerV1 struct{ SKI string }
type DiscoveredPartnerV1 struct{ SKI string }
type PairingCandidateV1 struct{}

type unavailableAdminV1 struct {
	runtime *runtimeImplementation
	mu      sync.Mutex
}

func (runtime *runtimeImplementation) AdminV1() AdminV1 {
	return &unavailableAdminV1{runtime: runtime}
}

func (admin *unavailableAdminV1) available() error {
	if admin == nil || admin.runtime == nil {
		return errAdminV1Unavailable
	}
	admin.runtime.mu.Lock()
	defer admin.runtime.mu.Unlock()
	if admin.runtime.shutdown {
		return ErrRuntimeShutdown
	}
	if !admin.runtime.enabled {
		return ErrRuntimeDisabled
	}
	return errAdminV1Unavailable
}

func (admin *unavailableAdminV1) Snapshot(context.Context) (AdminSnapshotV1, error) {
	return AdminSnapshotV1{}, admin.available()
}

func (admin *unavailableAdminV1) OpenPairingWindow(context.Context, time.Duration, uint64) (ActionHandleV1, error) {
	return ActionHandleV1{}, admin.available()
}

func (admin *unavailableAdminV1) ClosePairingWindow(context.Context, uint64) (ActionHandleV1, error) {
	return ActionHandleV1{}, admin.available()
}

func (admin *unavailableAdminV1) Select(context.Context, ObservationHandleV1, string, uint64) (SelectionHandleV1, error) {
	return SelectionHandleV1{}, admin.available()
}

func (admin *unavailableAdminV1) Connect(context.Context, SelectionHandleV1, uint64) (ActionHandleV1, error) {
	return ActionHandleV1{}, admin.available()
}

func (admin *unavailableAdminV1) Confirm(context.Context, CandidateHandleV1, string, uint64) (ActionHandleV1, error) {
	return ActionHandleV1{}, admin.available()
}

func (admin *unavailableAdminV1) Cancel(context.Context, CandidateHandleV1, uint64) (ActionHandleV1, error) {
	return ActionHandleV1{}, admin.available()
}

func (admin *unavailableAdminV1) Retry(context.Context, PartnerHandleV1, uint64) (ActionHandleV1, error) {
	return ActionHandleV1{}, admin.available()
}

func (admin *unavailableAdminV1) Untrust(context.Context, PartnerHandleV1, uint64) (ActionHandleV1, error) {
	return ActionHandleV1{}, admin.available()
}
