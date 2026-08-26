package eebusruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusfacade"
)

var errNativeRuntimeV2SnapshotUnavailable = errors.New("native eebus runtime snapshot is unavailable")

type NativeRuntimeV2 interface {
	Start(context.Context) error
	Shutdown() error
	NativeSnapshot() (NativeSnapshotV2, error)
	NativePairingState() ([]NativePairingObservationV2, error)
}

type nativeRuntimeV2Backend interface {
	Run(context.Context, func(NativeSnapshotV2)) error
	Close() error
}

type nativeRuntimeV2TerminalSnapshotProvider interface {
	TerminalNativeSnapshot() (NativeSnapshotV2, bool)
}

type nativeRuntimeV2BackendFactory func(context.Context, Config) (nativeRuntimeV2Backend, error)

type nativeRuntimeV2StartAttempt struct {
	done   chan struct{}
	cancel context.CancelFunc
	err    error
}

type nativeRuntimeV2Implementation struct {
	mu sync.Mutex

	enabled bool
	config  Config
	factory nativeRuntimeV2BackendFactory

	starting *nativeRuntimeV2StartAttempt
	started  bool
	shutdown bool

	backend   nativeRuntimeV2Backend
	cancel    context.CancelFunc
	done      chan struct{}
	workerErr error

	snapshot    NativeSnapshotV2
	hasSnapshot bool

	shutdownDone chan struct{}
	shutdownErr  error
}

type facadeNativeRuntimeV2Backend struct {
	backend     eebusfacade.Backend
	mu          sync.Mutex
	closing     bool
	terminal    NativeSnapshotV2
	hasTerminal bool
}

func NewNativeRuntimeV2(config Config) (NativeRuntimeV2, error) {
	return newNativeRuntimeV2(config, newFacadeNativeRuntimeV2Backend)
}

func newNativeRuntimeV2(config Config, factory nativeRuntimeV2BackendFactory) (NativeRuntimeV2, error) {
	normalized, err := normalizeRuntimeConfig(config)
	if err != nil {
		return nil, err
	}
	if normalized.Enabled && factory == nil {
		return nil, errors.New("native runtime V2 backend factory is required")
	}
	return &nativeRuntimeV2Implementation{enabled: normalized.Enabled, config: normalized, factory: factory}, nil
}

func (runtime *nativeRuntimeV2Implementation) Start(ctx context.Context) error {
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
	attempt := &nativeRuntimeV2StartAttempt{done: make(chan struct{}), cancel: cancel}
	runtime.starting = attempt
	config, factory := cloneRuntimeConfig(runtime.config), runtime.factory
	runtime.mu.Unlock()

	backend, acquireErr := factory(acquireCtx, config)
	contextErr := acquireCtx.Err()
	cancel()
	if acquireErr == nil && contextErr != nil {
		acquireErr = contextErr
	}
	if acquireErr == nil && backend == nil {
		acquireErr = errors.New("native runtime V2 backend factory returned nil")
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
	runtime.backend, runtime.cancel, runtime.done, runtime.started = backend, runCancel, done, true
	go runtime.runBackend(runCtx, backend, done)
	runtime.finishStartAttemptLocked(attempt, nil)
	runtime.mu.Unlock()
	return nil
}

func (runtime *nativeRuntimeV2Implementation) Shutdown() error {
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
	done, backend := runtime.done, runtime.backend
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
	if provider, ok := backend.(nativeRuntimeV2TerminalSnapshotProvider); ok {
		if terminal, exists := provider.TerminalNativeSnapshot(); exists {
			terminalErr = runtime.acceptTerminalSnapshotLocked(terminal)
		}
	}
	terminalErr = errors.Join(terminalErr, runtime.freezeTerminalSnapshotLocked())
	result := errors.Join(runtime.workerErr, terminalErr, closeErr)
	runtime.shutdownErr = result
	close(completion)
	runtime.mu.Unlock()
	return result
}

func (runtime *nativeRuntimeV2Implementation) NativeSnapshot() (NativeSnapshotV2, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if !runtime.enabled {
		return NativeSnapshotV2{}, ErrRuntimeDisabled
	}
	if runtime.workerErr != nil {
		return NativeSnapshotV2{}, runtime.workerErr
	}
	if !runtime.hasSnapshot {
		return NativeSnapshotV2{}, errNativeRuntimeV2SnapshotUnavailable
	}
	return runtime.snapshot.Clone(), nil
}

func (runtime *nativeRuntimeV2Implementation) NativePairingState() ([]NativePairingObservationV2, error) {
	snapshot, err := runtime.NativeSnapshot()
	if err != nil {
		return nil, err
	}
	return append([]NativePairingObservationV2(nil), snapshot.Pairing...), nil
}

func (runtime *nativeRuntimeV2Implementation) finishStartAttemptLocked(attempt *nativeRuntimeV2StartAttempt, err error) {
	attempt.err = err
	runtime.starting = nil
	close(attempt.done)
}

func (runtime *nativeRuntimeV2Implementation) runBackend(ctx context.Context, backend nativeRuntimeV2Backend, done chan struct{}) {
	runErr := backend.Run(ctx, runtime.publishSnapshot)
	runtime.mu.Lock()
	runtime.started = false
	switch {
	case runErr == nil:
		if !runtime.shutdown && ctx.Err() == nil && runtime.workerErr == nil {
			runtime.retainWorkerErrorLocked(errors.New("native runtime V2 backend Run stopped unexpectedly"))
		}
	case ctx.Err() == nil || !errors.Is(runErr, ctx.Err()):
		runtime.retainWorkerErrorLocked(fmt.Errorf("native runtime V2 backend Run: %w", runErr))
	}
	runtime.mu.Unlock()
	close(done)
}

func (runtime *nativeRuntimeV2Implementation) publishSnapshot(source NativeSnapshotV2) {
	snapshot, err := NewNativeSnapshotV2(source)
	if err != nil {
		runtime.mu.Lock()
		if runtime.shutdown {
			runtime.mu.Unlock()
			return
		}
		runtime.retainWorkerErrorLocked(fmt.Errorf("publish native runtime V2 snapshot: %w", err))
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
	runtime.snapshot, runtime.hasSnapshot = snapshot.Clone(), true
}

func (runtime *nativeRuntimeV2Implementation) acceptTerminalSnapshotLocked(source NativeSnapshotV2) error {
	snapshot, err := NewNativeSnapshotV2(source)
	if err != nil {
		return fmt.Errorf("accept terminal native runtime V2 snapshot: %w", err)
	}
	runtime.snapshot, runtime.hasSnapshot = snapshot.Clone(), true
	return nil
}

func (runtime *nativeRuntimeV2Implementation) retainWorkerErrorLocked(err error) {
	if err == nil {
		return
	}
	runtime.workerErr = errors.Join(runtime.workerErr, err)
	runtime.started = false
}

func (runtime *nativeRuntimeV2Implementation) freezeTerminalSnapshotLocked() error {
	if !runtime.hasSnapshot {
		return nil
	}
	draft := runtime.snapshot.Clone()
	draft.Status = NativeRuntimeObservationV2{State: string(ObservedRuntimeStateV1Shutdown)}
	terminal, err := NewNativeSnapshotV2(draft)
	if err != nil {
		return fmt.Errorf("freeze terminal native runtime V2 snapshot: %w", err)
	}
	runtime.snapshot = terminal
	return nil
}

func newFacadeNativeRuntimeV2Backend(ctx context.Context, config Config) (nativeRuntimeV2Backend, error) {
	remotes := make([]eebusfacade.RuntimeRemote, len(config.Remotes))
	for index, remote := range config.Remotes {
		remotes[index] = eebusfacade.RuntimeRemote{SKI: remote.SKI, Allowlisted: true}
	}
	labProfiles := make([]eebusfacade.RuntimeLabProfile, len(config.MutationLabProfiles))
	for index, profile := range config.MutationLabProfiles {
		labProfiles[index] = mutationLabProfileForFacade(profile)
	}
	backend, err := eebusfacade.AcquireNativeRuntimeV2(ctx, eebusfacade.RuntimeConfig{StateRoot: config.StateRoot, Interface: config.Interface, ListenPort: int(config.ListenAddress.Port()), ListenAddress: config.ListenAddress, DiscoveryEnabled: config.DiscoveryEnabled, Remotes: remotes, LabProfiles: labProfiles})
	if err != nil {
		return nil, err
	}
	return &facadeNativeRuntimeV2Backend{backend: backend}, nil
}

func (backend *facadeNativeRuntimeV2Backend) Run(ctx context.Context, publish func(NativeSnapshotV2)) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var parseMu sync.Mutex
	var parseErr error
	runErr := backend.backend.Run(runCtx, func(payload []byte) {
		var snapshot NativeSnapshotV2
		if err := json.Unmarshal(payload, &snapshot); err != nil {
			parseMu.Lock()
			if parseErr == nil {
				parseErr = fmt.Errorf("decode native runtime V2 snapshot: %w", err)
				cancel()
			}
			parseMu.Unlock()
			return
		}
		normalized, err := NewNativeSnapshotV2(snapshot)
		if err != nil {
			parseMu.Lock()
			if parseErr == nil {
				parseErr = fmt.Errorf("validate native runtime V2 snapshot: %w", err)
				cancel()
			}
			parseMu.Unlock()
			return
		}
		backend.mu.Lock()
		if backend.closing {
			backend.terminal, backend.hasTerminal = normalized.Clone(), true
		}
		backend.mu.Unlock()
		publish(normalized)
	})
	parseMu.Lock()
	defer parseMu.Unlock()
	return errors.Join(runErr, parseErr)
}

func (backend *facadeNativeRuntimeV2Backend) Close() error {
	backend.mu.Lock()
	backend.closing = true
	backend.mu.Unlock()
	return backend.backend.Close()
}
func (backend *facadeNativeRuntimeV2Backend) TerminalNativeSnapshot() (NativeSnapshotV2, bool) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if !backend.hasTerminal {
		return NativeSnapshotV2{}, false
	}
	return backend.terminal.Clone(), true
}
