package eebusruntime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusfacade"
)

var (
	ErrRuntimeDisabled = errors.New("eebus runtime is disabled")
	ErrRuntimeShutdown = errors.New("eebus runtime is shutdown")
)

var errRuntimeSnapshotUnavailable = errors.New("eebus runtime snapshot is unavailable")

type Runtime interface {
	RawFeatureRuntimeV1
	Start(context.Context) error
	Shutdown() error
	Snapshot() (SnapshotV1, error)
	PairingState() ([]PairingObservationV1, error)
}

type Config struct {
	Enabled             bool
	StateRoot           string
	Interface           string
	ListenAddress       netip.AddrPort
	DiscoveryEnabled    bool
	Remotes             []Remote
	PairingPolicy       PairingPolicy
	MutationLabProfiles []eebusraw.MutationLabProfileV1
}

type PairingPolicy string

const PairingPolicyClosed PairingPolicy = "closed"

type Remote struct {
	SKI string
}

type runtimeBackend interface {
	Run(context.Context, func(SnapshotV1)) error
	Close() error
}

type runtimeTerminalSnapshotProvider interface {
	TerminalSnapshot() (SnapshotV1, bool)
}

type runtimeBackendFactory func(context.Context, Config) (runtimeBackend, error)

type runtimeStartAttempt struct {
	done   chan struct{}
	cancel context.CancelFunc
	err    error
}

type runtimeImplementation struct {
	mu sync.Mutex

	enabled bool

	config  Config
	factory runtimeBackendFactory

	starting *runtimeStartAttempt
	started  bool
	shutdown bool

	backend   runtimeBackend
	cancel    context.CancelFunc
	done      chan struct{}
	workerErr error

	snapshot    SnapshotV1
	hasSnapshot bool

	shutdownDone chan struct{}
	shutdownErr  error

	adminBackend *operatorAdminV1BackendSlot
}

type facadeRuntimeBackend struct {
	backend     eebusfacade.Backend
	mu          sync.Mutex
	closing     bool
	terminal    SnapshotV1
	hasTerminal bool
}

func New(config Config) (Runtime, error) {
	return newRuntime(config, newFacadeRuntimeBackend)
}

func NewOperatorRuntimeV1(config Config) (Runtime, AdminV1, error) {
	runtime, err := newRuntime(config, newFacadeRuntimeBackend)
	if err != nil {
		return nil, nil, newAdminBoundaryUnavailableV1()
	}
	implementation, ok := runtime.(*runtimeImplementation)
	if !ok {
		return nil, nil, newAdminBoundaryUnavailableV1()
	}
	slot := &operatorAdminV1BackendSlot{}
	implementation.adminBackend = slot
	admin := newOperatorAdminV1Reducer(time.Now, rand.Reader, implementation, slot)
	return runtime, admin, nil
}

func newRuntime(config Config, factory runtimeBackendFactory) (Runtime, error) {
	normalized, err := normalizeRuntimeConfig(config)
	if err != nil {
		return nil, err
	}
	if normalized.Enabled && factory == nil {
		return nil, errors.New("runtime backend factory is required")
	}
	return &runtimeImplementation{
		enabled: normalized.Enabled,
		config:  normalized,
		factory: factory,
	}, nil
}

func (runtime *runtimeImplementation) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	runtime.mu.Lock()
	if runtime.shutdown {
		runtime.mu.Unlock()
		return ErrRuntimeShutdown
	}
	if !runtime.enabled {
		runtime.mu.Unlock()
		return nil
	}
	if runtime.workerErr != nil {
		err := runtime.workerErr
		runtime.mu.Unlock()
		return err
	}
	if runtime.started {
		runtime.mu.Unlock()
		return nil
	}
	if attempt := runtime.starting; attempt != nil {
		done := attempt.done
		runtime.mu.Unlock()
		select {
		case <-done:
			return attempt.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		runtime.mu.Unlock()
		return err
	}

	acquireCtx, cancel := context.WithCancel(ctx)
	attempt := &runtimeStartAttempt{
		done:   make(chan struct{}),
		cancel: cancel,
	}
	runtime.starting = attempt
	config := cloneRuntimeConfig(runtime.config)
	factory := runtime.factory
	runtime.mu.Unlock()

	backend, acquireErr := factory(acquireCtx, config)
	contextErr := acquireCtx.Err()
	cancel()
	if acquireErr == nil && contextErr != nil {
		acquireErr = contextErr
	}
	if acquireErr == nil && backend == nil {
		acquireErr = errors.New("runtime backend factory returned nil")
	}

	runtime.mu.Lock()
	if runtime.shutdown {
		if backend != nil {
			runtime.backend = backend
		}
		runtime.finishStartAttemptLocked(attempt, ErrRuntimeShutdown)
		runtime.mu.Unlock()
		return ErrRuntimeShutdown
	}
	if acquireErr != nil {
		if backend == nil {
			runtime.finishStartAttemptLocked(attempt, acquireErr)
			runtime.mu.Unlock()
			return acquireErr
		}
		runtime.mu.Unlock()
		closeErr := backend.Close()
		result := errors.Join(acquireErr, closeErr)
		runtime.mu.Lock()
		if runtime.shutdown {
			result = ErrRuntimeShutdown
		}
		runtime.finishStartAttemptLocked(attempt, result)
		runtime.mu.Unlock()
		return result
	}

	runCtx, runCancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	runtime.backend = backend
	runtime.cancel = runCancel
	runtime.done = done
	if runtime.adminBackend != nil {
		if adminBackend, ok := backend.(operatorAdminV1Backend); ok {
			runtime.adminBackend.attach(adminBackend)
		}
	}
	runtime.started = true
	go runtime.runBackend(runCtx, backend, done)
	runtime.finishStartAttemptLocked(attempt, nil)
	runtime.mu.Unlock()
	return nil
}

func (runtime *runtimeImplementation) Shutdown() error {
	runtime.mu.Lock()
	if runtime.shutdownDone != nil {
		done := runtime.shutdownDone
		runtime.mu.Unlock()
		<-done
		runtime.mu.Lock()
		err := runtime.shutdownErr
		runtime.mu.Unlock()
		return err
	}

	runtime.shutdown = true
	if runtime.adminBackend != nil {
		if adminBackend, ok := runtime.backend.(operatorAdminV1Backend); ok {
			runtime.adminBackend.detach(adminBackend)
		}
	}
	completion := make(chan struct{})
	runtime.shutdownDone = completion
	attempt := runtime.starting
	if attempt != nil {
		attempt.cancel()
	}
	if runtime.cancel != nil {
		runtime.cancel()
	}
	runtime.mu.Unlock()

	if attempt != nil {
		<-attempt.done
	}

	runtime.mu.Lock()
	if runtime.cancel != nil {
		runtime.cancel()
	}
	done := runtime.done
	backend := runtime.backend
	runtime.mu.Unlock()

	var closeErr error
	if backend != nil {
		closeErr = backend.Close()
	}
	if done != nil {
		<-done
	}

	runtime.mu.Lock()
	var terminalErr error
	if provider, ok := backend.(runtimeTerminalSnapshotProvider); ok {
		if terminal, exists := provider.TerminalSnapshot(); exists {
			terminalErr = runtime.acceptTerminalSnapshotLocked(terminal)
		}
	}
	terminalErr = errors.Join(terminalErr, runtime.freezeTerminalSnapshotLocked())
	workerErr := runtime.workerErr
	runtime.mu.Unlock()

	result := errors.Join(workerErr, terminalErr, closeErr)

	runtime.mu.Lock()
	runtime.shutdownErr = result
	close(completion)
	runtime.mu.Unlock()
	return result
}

func (runtime *runtimeImplementation) acceptTerminalSnapshotLocked(source SnapshotV1) error {
	snapshot, err := NewSnapshotV1(source)
	if err != nil {
		return fmt.Errorf("accept terminal runtime snapshot: %w", err)
	}
	runtime.snapshot = snapshot.Clone()
	runtime.hasSnapshot = true
	return nil
}

func (runtime *runtimeImplementation) Snapshot() (SnapshotV1, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if !runtime.enabled {
		return SnapshotV1{}, ErrRuntimeDisabled
	}
	if runtime.workerErr != nil {
		return SnapshotV1{}, runtime.workerErr
	}
	if !runtime.hasSnapshot {
		return SnapshotV1{}, errRuntimeSnapshotUnavailable
	}
	return runtime.snapshot.Clone(), nil
}

func (runtime *runtimeImplementation) PairingState() ([]PairingObservationV1, error) {
	snapshot, err := runtime.Snapshot()
	if err != nil {
		return nil, err
	}
	return snapshot.Pairing, nil
}

func (runtime *runtimeImplementation) operatorAdminV1Lifecycle() (bool, bool, bool) {
	if runtime == nil {
		return false, false, true
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.enabled, runtime.started && runtime.backend != nil && runtime.workerErr == nil, runtime.shutdown
}

func (runtime *runtimeImplementation) finishStartAttemptLocked(attempt *runtimeStartAttempt, err error) {
	attempt.err = err
	runtime.starting = nil
	close(attempt.done)
}

func (runtime *runtimeImplementation) runBackend(ctx context.Context, backend runtimeBackend, done chan struct{}) {
	runErr := backend.Run(ctx, runtime.publishSnapshot)
	runtime.mu.Lock()
	runtime.started = false
	if runtime.adminBackend != nil {
		if adminBackend, ok := backend.(operatorAdminV1Backend); ok {
			runtime.adminBackend.detach(adminBackend)
		}
	}
	switch {
	case runErr == nil:
		if !runtime.shutdown && ctx.Err() == nil && runtime.workerErr == nil {
			runtime.retainWorkerErrorLocked(errors.New("runtime backend Run stopped unexpectedly"))
		}
	case ctx.Err() == nil || !errors.Is(runErr, ctx.Err()):
		runtime.retainWorkerErrorLocked(fmt.Errorf("runtime backend Run: %w", runErr))
	}
	runtime.mu.Unlock()
	close(done)
}

func (runtime *runtimeImplementation) publishSnapshot(source SnapshotV1) {
	snapshot, err := NewSnapshotV1(source)
	if err != nil {
		runtime.mu.Lock()
		if runtime.shutdown {
			runtime.mu.Unlock()
			return
		}
		runtime.retainWorkerErrorLocked(fmt.Errorf("publish runtime snapshot: %w", err))
		cancel := runtime.cancel
		runtime.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.shutdown || runtime.workerErr != nil {
		return
	}
	runtime.snapshot = snapshot.Clone()
	runtime.hasSnapshot = true
}

func (runtime *runtimeImplementation) retainWorkerErrorLocked(err error) {
	if err == nil {
		return
	}
	runtime.workerErr = errors.Join(runtime.workerErr, err)
	runtime.started = false
}

func (runtime *runtimeImplementation) freezeTerminalSnapshotLocked() error {
	if !runtime.hasSnapshot {
		return nil
	}
	draft := runtime.snapshot.Clone()
	draft.Status = RuntimeObservationV1{State: ObservedRuntimeStateV1Shutdown}
	draft.Meta.DataHash = ""
	terminal, err := NewSnapshotV1(draft)
	if err != nil {
		return fmt.Errorf("freeze terminal runtime snapshot: %w", err)
	}
	runtime.snapshot = terminal
	return nil
}

func newFacadeRuntimeBackend(ctx context.Context, config Config) (runtimeBackend, error) {
	remotes := make([]eebusfacade.RuntimeRemote, len(config.Remotes))
	for index, remote := range config.Remotes {
		remotes[index] = eebusfacade.RuntimeRemote{
			SKI:         remote.SKI,
			Allowlisted: true,
		}
	}
	labProfiles := make([]eebusfacade.RuntimeLabProfile, len(config.MutationLabProfiles))
	for index, profile := range config.MutationLabProfiles {
		labProfiles[index] = mutationLabProfileForFacade(profile)
	}
	backend, err := eebusfacade.Acquire(ctx, eebusfacade.RuntimeConfig{
		StateRoot:        config.StateRoot,
		Interface:        config.Interface,
		ListenPort:       int(config.ListenAddress.Port()),
		ListenAddress:    config.ListenAddress,
		DiscoveryEnabled: config.DiscoveryEnabled,
		Remotes:          remotes,
		LabProfiles:      labProfiles,
	})
	if err != nil {
		return nil, err
	}
	return &facadeRuntimeBackend{backend: backend}, nil
}

func (backend *facadeRuntimeBackend) Run(ctx context.Context, publish func(SnapshotV1)) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var parseMu sync.Mutex
	var parseErr error
	runErr := backend.backend.Run(runCtx, func(payload []byte) {
		var snapshot SnapshotV1
		if err := json.Unmarshal(payload, &snapshot); err != nil {
			parseMu.Lock()
			if parseErr == nil {
				parseErr = fmt.Errorf("decode internal runtime snapshot: %w", err)
				cancel()
			}
			parseMu.Unlock()
			return
		}
		backend.mu.Lock()
		if backend.closing {
			backend.terminal = snapshot.Clone()
			backend.hasTerminal = true
		}
		backend.mu.Unlock()
		publish(snapshot)
	})
	parseMu.Lock()
	defer parseMu.Unlock()
	return errors.Join(runErr, parseErr)
}

func (backend *facadeRuntimeBackend) Close() error {
	backend.mu.Lock()
	backend.closing = true
	backend.mu.Unlock()
	return backend.backend.Close()
}

func (backend *facadeRuntimeBackend) TerminalSnapshot() (SnapshotV1, bool) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if !backend.hasTerminal {
		return SnapshotV1{}, false
	}
	return backend.terminal.Clone(), true
}

func (backend *facadeRuntimeBackend) snapshotOperatorAdminV1(ctx context.Context) (operatorAdminV1SnapshotFacts, *AdminErrorV1) {
	internal := backend.operatorAdminV1InternalBackend()
	if internal == nil {
		return operatorAdminV1SnapshotFacts{}, newAdminBoundaryUnavailableV1()
	}
	snapshot, failure := internal.OperatorAdminV1Snapshot(ctx)
	if terminal := operatorAdminV1FacadeFailure(failure); terminal != nil {
		return operatorAdminV1SnapshotFacts{}, terminal
	}
	return operatorAdminV1SnapshotFacts{
		capturedAt: snapshot.CapturedAt, localSKI: snapshot.LocalSKI, localSHIPID: snapshot.LocalSHIPID,
		status: snapshot.Status, window: snapshot.Window,
		windowDeadline: snapshot.WindowDeadline, register: snapshot.Register, listener: snapshot.Listener,
		discovery: snapshot.Discovery, degraded: AdminErrorCodeV1(snapshot.Degraded),
		trusted: operatorAdminV1TrustedFactsFromFacade(snapshot.Trusted), connected: operatorAdminV1ConnectedFactsFromFacade(snapshot.Connected),
		discovered: operatorAdminV1DiscoveredFactsFromFacade(snapshot.Discovered), candidates: operatorAdminV1CandidateFactsFromFacade(snapshot.Candidates),
	}, nil
}

func (backend *facadeRuntimeBackend) openOperatorAdminV1(ctx context.Context, duration time.Duration) (operatorAdminV1Transition, *AdminErrorV1) {
	internal := backend.operatorAdminV1InternalBackend()
	if internal == nil {
		return operatorAdminV1Transition{}, newAdminBoundaryUnavailableV1()
	}
	transition, failure := internal.OperatorAdminV1Open(ctx, duration)
	return operatorAdminV1TransitionFromFacade(transition), operatorAdminV1FacadeFailure(failure)
}

func (backend *facadeRuntimeBackend) closeOperatorAdminV1(ctx context.Context) (operatorAdminV1Transition, *AdminErrorV1) {
	internal := backend.operatorAdminV1InternalBackend()
	if internal == nil {
		return operatorAdminV1Transition{}, newAdminBoundaryUnavailableV1()
	}
	transition, failure := internal.OperatorAdminV1Close(ctx)
	return operatorAdminV1TransitionFromFacade(transition), operatorAdminV1FacadeFailure(failure)
}

func (backend *facadeRuntimeBackend) selectOperatorAdminV1(ctx context.Context, reference, expectedSKI string) (string, operatorAdminV1Transition, *AdminErrorV1) {
	internal := backend.operatorAdminV1InternalBackend()
	if internal == nil {
		return "", operatorAdminV1Transition{}, newAdminBoundaryUnavailableV1()
	}
	selection, transition, failure := internal.OperatorAdminV1Select(ctx, reference, expectedSKI)
	return selection, operatorAdminV1TransitionFromFacade(transition), operatorAdminV1FacadeFailure(failure)
}

func (backend *facadeRuntimeBackend) connectOperatorAdminV1(ctx context.Context, reference string) (operatorAdminV1Transition, *AdminErrorV1) {
	internal := backend.operatorAdminV1InternalBackend()
	if internal == nil {
		return operatorAdminV1Transition{}, newAdminBoundaryUnavailableV1()
	}
	transition, failure := internal.OperatorAdminV1Connect(ctx, reference)
	return operatorAdminV1TransitionFromFacade(transition), operatorAdminV1FacadeFailure(failure)
}

func (backend *facadeRuntimeBackend) confirmOperatorAdminV1(ctx context.Context, reference, expectedSKI string) (operatorAdminV1Transition, *AdminErrorV1) {
	internal := backend.operatorAdminV1InternalBackend()
	if internal == nil {
		return operatorAdminV1Transition{}, newAdminBoundaryUnavailableV1()
	}
	transition, failure := internal.OperatorAdminV1Confirm(ctx, reference, expectedSKI)
	return operatorAdminV1TransitionFromFacade(transition), operatorAdminV1FacadeFailure(failure)
}

func (backend *facadeRuntimeBackend) cancelOperatorAdminV1(ctx context.Context, reference string) (operatorAdminV1Transition, *AdminErrorV1) {
	internal := backend.operatorAdminV1InternalBackend()
	if internal == nil {
		return operatorAdminV1Transition{}, newAdminBoundaryUnavailableV1()
	}
	transition, failure := internal.OperatorAdminV1Cancel(ctx, reference)
	return operatorAdminV1TransitionFromFacade(transition), operatorAdminV1FacadeFailure(failure)
}

func (backend *facadeRuntimeBackend) retryTrustedOperatorAdminV1(ctx context.Context, reference string) (operatorAdminV1Transition, *AdminErrorV1) {
	internal := backend.operatorAdminV1InternalBackend()
	if internal == nil {
		return operatorAdminV1Transition{}, newAdminBoundaryUnavailableV1()
	}
	transition, failure := internal.OperatorAdminV1RetryTrusted(ctx, reference)
	return operatorAdminV1TransitionFromFacade(transition), operatorAdminV1FacadeFailure(failure)
}

func (backend *facadeRuntimeBackend) untrustOperatorAdminV1(ctx context.Context, reference string) (operatorAdminV1Transition, *AdminErrorV1) {
	internal := backend.operatorAdminV1InternalBackend()
	if internal == nil {
		return operatorAdminV1Transition{}, newAdminBoundaryUnavailableV1()
	}
	transition, failure := internal.OperatorAdminV1Untrust(ctx, reference)
	return operatorAdminV1TransitionFromFacade(transition), operatorAdminV1FacadeFailure(failure)
}

func (backend *facadeRuntimeBackend) operatorAdminV1InternalBackend() eebusfacade.OperatorAdminV1Backend {
	if backend == nil || backend.backend == nil {
		return nil
	}
	internal, _ := backend.backend.(eebusfacade.OperatorAdminV1Backend)
	return internal
}

func operatorAdminV1TransitionFromFacade(source eebusfacade.OperatorAdminV1Transition) operatorAdminV1Transition {
	return operatorAdminV1Transition{outcome: AdminOutcomeV1(source.Outcome), changed: source.Changed}
}

func operatorAdminV1FacadeFailure(failure string) *AdminErrorV1 {
	if failure == "" {
		return nil
	}
	return normalizeOperatorAdminV1Error(operatorAdminV1Error(AdminErrorCodeV1(failure)))
}

func operatorAdminV1TrustedFactsFromFacade(source []eebusfacade.OperatorAdminV1Fact) []operatorAdminV1TrustedFact {
	result := make([]operatorAdminV1TrustedFact, len(source))
	for index, fact := range source {
		result[index] = operatorAdminV1TrustedFact{
			reference: fact.Reference, ski: fact.SKI, endpoint: fact.Endpoint, trustState: fact.TrustState,
			connectionState: fact.ConnectionState, shipID: fact.SHIPID, lastSeen: fact.LastSeen,
			name: fact.Name, identifier: fact.Identifier, brand: fact.Brand, typeName: fact.Type, model: fact.Model,
			retryState: fact.RetryState, retryDeadline: fact.RetryDeadline, retryAdmitted: fact.RetryAdmitted,
		}
	}
	return result
}

func operatorAdminV1ConnectedFactsFromFacade(source []eebusfacade.OperatorAdminV1Fact) []operatorAdminV1ConnectedFact {
	result := make([]operatorAdminV1ConnectedFact, len(source))
	for index, fact := range source {
		result[index] = operatorAdminV1ConnectedFact{
			ski: fact.SKI, endpoint: fact.Endpoint, trustState: fact.TrustState,
			connectionState: fact.ConnectionState, shipID: fact.SHIPID, lastSeen: fact.LastSeen,
			name: fact.Name, identifier: fact.Identifier, brand: fact.Brand, typeName: fact.Type, model: fact.Model,
		}
	}
	return result
}

func operatorAdminV1DiscoveredFactsFromFacade(source []eebusfacade.OperatorAdminV1Fact) []operatorAdminV1DiscoveredFact {
	result := make([]operatorAdminV1DiscoveredFact, len(source))
	for index, fact := range source {
		result[index] = operatorAdminV1DiscoveredFact{
			reference: fact.Reference, ski: fact.SKI, endpoint: fact.Endpoint, observationRevision: fact.ObservationRevision,
			lastSeen: fact.LastSeen, name: fact.Name, identifier: fact.Identifier, brand: fact.Brand,
			typeName: fact.Type, model: fact.Model, expiresAt: fact.ExpiresAt,
		}
	}
	return result
}

func operatorAdminV1CandidateFactsFromFacade(source []eebusfacade.OperatorAdminV1Fact) []operatorAdminV1CandidateFact {
	result := make([]operatorAdminV1CandidateFact, len(source))
	for index, fact := range source {
		result[index] = operatorAdminV1CandidateFact{
			reference: fact.Reference, ski: fact.SKI, state: fact.State,
			expiresAt: fact.ExpiresAt, associationComplete: fact.AssociationComplete,
		}
	}
	return result
}

func normalizeRuntimeConfig(config Config) (Config, error) {
	if !config.Enabled {
		if config.StateRoot != "" || config.Interface != "" ||
			config.ListenAddress != (netip.AddrPort{}) || config.DiscoveryEnabled ||
			config.Remotes != nil || config.PairingPolicy != "" ||
			config.MutationLabProfiles != nil {
			return Config{}, errors.New("disabled runtime configuration must be empty")
		}
		return Config{}, nil
	}

	config.StateRoot = filepath.Clean(strings.TrimSpace(config.StateRoot))
	if config.StateRoot == "." || config.StateRoot == "" {
		return Config{}, errors.New("runtime state root is required")
	}
	if !filepath.IsAbs(config.StateRoot) {
		return Config{}, errors.New("runtime state root must be absolute")
	}
	volumeRoot := filepath.VolumeName(config.StateRoot) + string(filepath.Separator)
	if config.StateRoot == volumeRoot {
		return Config{}, errors.New("runtime state root must not be the filesystem root")
	}

	config.Interface = strings.TrimSpace(config.Interface)
	if runtimeWildcard(config.Interface) {
		return Config{}, errors.New("runtime interface must be explicit")
	}
	if err := validateRuntimeListenAddress(config.ListenAddress); err != nil {
		return Config{}, err
	}
	if config.PairingPolicy != PairingPolicyClosed {
		return Config{}, errors.New("runtime pairing policy must be closed")
	}

	if config.Remotes != nil {
		remotes := make([]Remote, len(config.Remotes))
		seen := make(map[string]struct{}, len(config.Remotes))
		for index, source := range config.Remotes {
			remote, err := normalizeRuntimeRemote(source)
			if err != nil {
				return Config{}, fmt.Errorf("runtime remote %d: %w", index, err)
			}
			if _, exists := seen[remote.SKI]; exists {
				return Config{}, fmt.Errorf("runtime remote %d duplicates remote SKI", index)
			}
			seen[remote.SKI] = struct{}{}
			remotes[index] = remote
		}
		config.Remotes = remotes
	}
	if config.MutationLabProfiles != nil {
		if len(config.MutationLabProfiles) > 16 {
			return Config{}, errors.New("runtime mutation lab profile count exceeds the limit")
		}
		profiles := make([]eebusraw.MutationLabProfileV1, len(config.MutationLabProfiles))
		seenIDs := make(map[string]struct{}, len(config.MutationLabProfiles))
		seenProfiles := make(map[eebusraw.HashV1]struct{}, len(config.MutationLabProfiles))
		remotes := make(map[string]struct{}, len(config.Remotes))
		for _, remote := range config.Remotes {
			remotes[remote.SKI] = struct{}{}
		}
		for index, source := range config.MutationLabProfiles {
			profile := source.Clone()
			if terminal := eebusraw.ValidateMutationLabProfileV1(profile); terminal != nil {
				return Config{}, fmt.Errorf("runtime mutation lab profile %d is invalid", index)
			}
			if _, admitted := remotes[profile.Target.RemoteSKI]; !admitted {
				return Config{}, fmt.Errorf(
					"runtime mutation lab profile %d targets an unadmitted remote",
					index,
				)
			}
			if _, duplicate := seenIDs[profile.ProfileID]; duplicate {
				return Config{}, fmt.Errorf(
					"runtime mutation lab profile %d duplicates profile id",
					index,
				)
			}
			commitment, err := eebusraw.CanonicalSHA256V1(profile)
			if err != nil {
				return Config{}, fmt.Errorf("runtime mutation lab profile %d is invalid", index)
			}
			if _, duplicate := seenProfiles[commitment]; duplicate {
				return Config{}, fmt.Errorf(
					"runtime mutation lab profile %d duplicates an exact profile",
					index,
				)
			}
			seenIDs[profile.ProfileID] = struct{}{}
			seenProfiles[commitment] = struct{}{}
			profiles[index] = profile
		}
		config.MutationLabProfiles = profiles
	}
	return config, nil
}

func validateRuntimeListenAddress(endpoint netip.AddrPort) error {
	if !endpoint.IsValid() {
		return errors.New("runtime listen address must be valid")
	}
	if endpoint.Port() == 0 {
		return errors.New("runtime listen address port must be non-zero")
	}
	address := endpoint.Addr()
	if address.IsUnspecified() || address.IsMulticast() {
		return errors.New("runtime listen address must be specified unicast")
	}
	if address.Is4In6() {
		return errors.New("runtime listen address must not be IPv4-mapped IPv6")
	}
	if address.Is4() {
		octets := address.As4()
		if octets[0] == 0 || octets == [4]byte{255, 255, 255, 255} {
			return errors.New("runtime listen address must not be wildcard or broadcast")
		}
	}
	return nil
}

func normalizeRuntimeRemote(remote Remote) (Remote, error) {
	remote.SKI = strings.ToLower(strings.TrimSpace(remote.SKI))
	if len(remote.SKI) != 40 {
		return Remote{}, errors.New("remote SKI must contain 40 hexadecimal characters")
	}
	if _, err := hex.DecodeString(remote.SKI); err != nil {
		return Remote{}, errors.New("remote SKI must contain 40 hexadecimal characters")
	}
	return remote, nil
}

func cloneRuntimeConfig(config Config) Config {
	if config.Remotes != nil {
		config.Remotes = append([]Remote{}, config.Remotes...)
	}
	if config.MutationLabProfiles != nil {
		profiles := make([]eebusraw.MutationLabProfileV1, len(config.MutationLabProfiles))
		for index, profile := range config.MutationLabProfiles {
			profiles[index] = profile.Clone()
		}
		config.MutationLabProfiles = profiles
	}
	return config
}

func mutationLabProfileForFacade(
	profile eebusraw.MutationLabProfileV1,
) eebusfacade.RuntimeLabProfile {
	return eebusfacade.RuntimeLabProfile{
		Contract:               profile.Contract,
		ProfileID:              profile.ProfileID,
		Target:                 profile.Target.Clone(),
		AllowedValueHashes:     append([]eebusraw.HashV1(nil), profile.AllowedValueHashes...),
		RollbackValueHash:      profile.RollbackValueHash,
		MaximumProbeTTLSeconds: profile.MaximumProbeTTLSeconds,
		SafetyPredicates:       append([]string(nil), profile.SafetyPredicates...),
		EvidenceHashes:         append([]eebusraw.HashV1(nil), profile.EvidenceHashes...),
		ExpiresAt:              profile.ExpiresAt,
	}
}

func runtimeWildcard(value string) bool {
	switch strings.TrimSpace(value) {
	case "", "*", "0.0.0.0", "::", "[::]":
		return true
	default:
		return false
	}
}
