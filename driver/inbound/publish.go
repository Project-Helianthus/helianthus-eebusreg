package inbound

import (
	"context"
	"errors"

	"github.com/Project-Helianthus/helianthus-semreg/semreg/v1/driver"
)

// Publisher turns paired heating observations into SemReg publications for one instance.
type Publisher struct {
	scope   Scope
	pass    chan struct{} // holds one token: one publication pass runs at a time
	world   *world
	nodes   map[string]driver.NodeRef
	members map[string]struct{}
}

// New returns a publisher for one instance. Installation, instance and bridge are required.
func New(scope Scope) (*Publisher, error) {
	if scope.Installation == "" || scope.Instance == "" || scope.Bridge == "" {
		return nil, errors.New("inbound: installation, instance and bridge are required")
	}
	return &Publisher{scope: scope, pass: make(chan struct{}, 1), world: freshWorld()}, nil
}

// Apply publishes the remotes. A remote that is not ready keeps its last accepted
// values and marks those facts unavailable. Discovery order does not change identities.
func (p *Publisher) Apply(ctx context.Context, session driver.Session, remotes []Remote) (Result, error) {
	p.pass <- struct{}{}
	defer func() { <-p.pass }()
	return p.publish(ctx, session, func(next *world) error { return next.ingest(remotes) })
}

// StopGeneration keeps the sources last updated at that generation, with their
// last value, and marks them stale. A later observation of that generation is ignored.
func (p *Publisher) StopGeneration(ctx context.Context, session driver.Session, ski string, generation uint64) (Result, error) {
	p.pass <- struct{}{}
	defer func() { <-p.pass }()
	return p.publish(ctx, session, func(next *world) error {
		next.stop(ski, generation)
		return nil
	})
}

// Revoke withdraws the sources of one remote. Sources of any other remote stay.
func (p *Publisher) Revoke(ctx context.Context, session driver.Session, ski string) (Result, error) {
	p.pass <- struct{}{}
	defer func() { <-p.pass }()
	return p.publish(ctx, session, func(next *world) error {
		next.revoke(ski)
		return nil
	})
}

func (p *Publisher) publish(ctx context.Context, session driver.Session, mutate func(*world) error) (Result, error) {
	next := p.world.clone()
	if err := mutate(next); err != nil {
		return Result{}, err
	}
	result, err := next.view(p.scope)
	if err != nil {
		return Result{}, err
	}
	if err := stage(ctx, session, p, next, &result); err != nil {
		return Result{}, err
	}
	p.world = next
	return result, nil
}
