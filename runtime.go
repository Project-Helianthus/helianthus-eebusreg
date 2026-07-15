package eebusruntime

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusfacade"
)

var (
	ErrRuntimeDisabled = errors.New("eebus runtime is disabled")
	ErrRuntimeShutdown = errors.New("eebus runtime is shutdown")
)

var errRuntimeSnapshotUnavailable = errors.New("eebus runtime snapshot is unavailable")

type Runtime interface {
	Start(context.Context) error
	Shutdown() error
	Snapshot() (SnapshotV1, error)
	PairingState() ([]PairingObservationV1, error)
}

type Config struct {
	Enabled   bool
	StateRoot string
	Interface string
	Remotes   []Remote
}

type Remote struct {
	SKI      string
	Endpoint Endpoint
}

type Endpoint struct {
	Host string
	Port int
	Path string
}

type runtimeBackend interface {
	Run(context.Context, func(SnapshotV1)) error
	Close() error
}

type runtimeBackendFactory func(context.Context, Config) (runtimeBackend, error)

type runtimeStartAttempt struct {
	done   chan struct{}
	cancel context.CancelFunc
	err    error
}

type runtimeImplementation struct {
	mu sync.Mutex

	config  Config
	factory runtimeBackendFactory

	starting *runtimeStartAttempt
	started  bool
	shutdown bool

	backend runtimeBackend
	cancel  context.CancelFunc
	done    chan struct{}

	snapshot    SnapshotV1
	hasSnapshot bool

	shutdownDone chan struct{}
	shutdownErr  error
}

type facadeRuntimeBackend struct {
	backend eebusfacade.Backend
}

func New(config Config) (Runtime, error) {
	return newRuntime(config, newFacadeRuntimeBackend)
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
	if !runtime.config.Enabled || runtime.started {
		runtime.mu.Unlock()
		return nil
	}
	if attempt := runtime.starting; attempt != nil {
		done := attempt.done
		runtime.mu.Unlock()
		<-done
		return attempt.err
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

	if done != nil {
		<-done
	}

	runtime.mu.Lock()
	terminalErr := runtime.freezeTerminalSnapshotLocked()
	runtime.mu.Unlock()

	var closeErr error
	if backend != nil {
		closeErr = backend.Close()
	}
	result := errors.Join(terminalErr, closeErr)

	runtime.mu.Lock()
	runtime.shutdownErr = result
	close(completion)
	runtime.mu.Unlock()
	return result
}

func (runtime *runtimeImplementation) Snapshot() (SnapshotV1, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if !runtime.config.Enabled {
		return SnapshotV1{}, ErrRuntimeDisabled
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

func (runtime *runtimeImplementation) finishStartAttemptLocked(attempt *runtimeStartAttempt, err error) {
	attempt.err = err
	runtime.starting = nil
	close(attempt.done)
}

func (runtime *runtimeImplementation) runBackend(ctx context.Context, backend runtimeBackend, done chan struct{}) {
	defer close(done)
	_ = backend.Run(ctx, runtime.publishSnapshot)
}

func (runtime *runtimeImplementation) publishSnapshot(source SnapshotV1) {
	snapshot, err := NewSnapshotV1(source)
	if err != nil {
		return
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.shutdown {
		return
	}
	runtime.snapshot = snapshot.Clone()
	runtime.hasSnapshot = true
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
			SKI: remote.SKI,
			Endpoint: eebusfacade.RuntimeEndpoint{
				Host: remote.Endpoint.Host,
				Port: remote.Endpoint.Port,
				Path: remote.Endpoint.Path,
			},
		}
	}
	backend, err := eebusfacade.Acquire(ctx, eebusfacade.RuntimeConfig{
		StateRoot: config.StateRoot,
		Interface: config.Interface,
		Remotes:   remotes,
	})
	if err != nil {
		return nil, err
	}
	return &facadeRuntimeBackend{backend: backend}, nil
}

func (backend *facadeRuntimeBackend) Run(ctx context.Context, publish func(SnapshotV1)) error {
	return backend.backend.Run(ctx, func(payload []byte) {
		var snapshot SnapshotV1
		if err := json.Unmarshal(payload, &snapshot); err == nil {
			publish(snapshot)
		}
	})
}

func (backend *facadeRuntimeBackend) Close() error {
	return backend.backend.Close()
}

func normalizeRuntimeConfig(config Config) (Config, error) {
	if !config.Enabled {
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
	if len(config.Remotes) == 0 {
		return Config{}, errors.New("at least one runtime remote is required")
	}

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
	return config, nil
}

func normalizeRuntimeRemote(remote Remote) (Remote, error) {
	remote.SKI = strings.ToLower(strings.TrimSpace(remote.SKI))
	if len(remote.SKI) != 40 {
		return Remote{}, errors.New("remote SKI must contain 40 hexadecimal characters")
	}
	if _, err := hex.DecodeString(remote.SKI); err != nil {
		return Remote{}, errors.New("remote SKI must contain 40 hexadecimal characters")
	}

	remote.Endpoint.Host = strings.TrimSpace(remote.Endpoint.Host)
	remote.Endpoint.Path = strings.TrimSpace(remote.Endpoint.Path)
	if runtimeWildcard(remote.Endpoint.Host) {
		return Remote{}, errors.New("remote endpoint host must be explicit")
	}
	if strings.Contains(remote.Endpoint.Host, "://") {
		return Remote{}, errors.New("remote endpoint host must not include a URL scheme")
	}
	if remote.Endpoint.Port < 1 || remote.Endpoint.Port > 65535 {
		return Remote{}, errors.New("remote endpoint port must be between 1 and 65535")
	}
	if remote.Endpoint.Path == "" || !strings.HasPrefix(remote.Endpoint.Path, "/") {
		return Remote{}, errors.New("remote endpoint path must be an absolute URL path")
	}
	return remote, nil
}

func cloneRuntimeConfig(config Config) Config {
	config.Remotes = append([]Remote(nil), config.Remotes...)
	return config
}

func runtimeWildcard(value string) bool {
	switch strings.TrimSpace(value) {
	case "", "*", "0.0.0.0", "::", "[::]":
		return true
	default:
		return false
	}
}
