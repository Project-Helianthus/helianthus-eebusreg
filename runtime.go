package eebusruntime

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
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
	Enabled    bool
	StateRoot  string
	Interface  string
	ListenPort int
	Remotes    []Remote
}

type PairingPolicyV2 string

const PairingPolicyV2Closed PairingPolicyV2 = "closed"

type ConfigV2 struct {
	Enabled          bool
	StateRoot        string
	Interface        string
	ListenAddress    netip.AddrPort
	DiscoveryEnabled bool
	Remotes          []Remote
	PairingPolicy    PairingPolicyV2
}

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

type runtimeBackendFactoryV2 func(context.Context, ConfigV2) (runtimeBackend, error)

type runtimeStartAttempt struct {
	done   chan struct{}
	cancel context.CancelFunc
	err    error
}

type runtimeImplementation struct {
	mu sync.Mutex

	enabled bool

	config    Config
	factory   runtimeBackendFactory
	configV2  ConfigV2
	factoryV2 runtimeBackendFactoryV2

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

func NewV2(config ConfigV2) (Runtime, error) {
	return newRuntimeV2(config, newFacadeRuntimeBackendV2)
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

func newRuntimeV2(config ConfigV2, factory runtimeBackendFactoryV2) (Runtime, error) {
	normalized, err := normalizeRuntimeConfigV2(config)
	if err != nil {
		return nil, err
	}
	if normalized.Enabled && factory == nil {
		return nil, errors.New("runtime backend factory is required")
	}
	return &runtimeImplementation{
		enabled:   normalized.Enabled,
		configV2:  normalized,
		factoryV2: factory,
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
	configV2 := cloneRuntimeConfigV2(runtime.configV2)
	factoryV2 := runtime.factoryV2
	runtime.mu.Unlock()

	var backend runtimeBackend
	var acquireErr error
	if factoryV2 != nil {
		backend, acquireErr = factoryV2(acquireCtx, configV2)
	} else {
		backend, acquireErr = factory(acquireCtx, config)
	}
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

func (runtime *runtimeImplementation) finishStartAttemptLocked(attempt *runtimeStartAttempt, err error) {
	attempt.err = err
	runtime.starting = nil
	close(attempt.done)
}

func (runtime *runtimeImplementation) runBackend(ctx context.Context, backend runtimeBackend, done chan struct{}) {
	runErr := backend.Run(ctx, runtime.publishSnapshot)
	runtime.mu.Lock()
	runtime.started = false
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
	backend, err := eebusfacade.Acquire(ctx, eebusfacade.RuntimeConfig{
		StateRoot:  config.StateRoot,
		Interface:  config.Interface,
		ListenPort: config.ListenPort,
		Remotes:    remotes,
	})
	if err != nil {
		return nil, err
	}
	return &facadeRuntimeBackend{backend: backend}, nil
}

func newFacadeRuntimeBackendV2(ctx context.Context, config ConfigV2) (runtimeBackend, error) {
	remotes := make([]eebusfacade.RuntimeRemote, len(config.Remotes))
	for index, remote := range config.Remotes {
		remotes[index] = eebusfacade.RuntimeRemote{
			SKI:         remote.SKI,
			Allowlisted: true,
		}
	}
	backend, err := eebusfacade.Acquire(ctx, eebusfacade.RuntimeConfig{
		StateRoot:        config.StateRoot,
		Interface:        config.Interface,
		ListenPort:       int(config.ListenAddress.Port()),
		ListenAddress:    config.ListenAddress,
		DiscoveryEnabled: config.DiscoveryEnabled,
		Remotes:          remotes,
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
	if config.ListenPort < 1 || config.ListenPort > 65535 {
		return Config{}, errors.New("runtime listen port must be between 1 and 65535")
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

func normalizeRuntimeConfigV2(config ConfigV2) (ConfigV2, error) {
	if !config.Enabled {
		if config.StateRoot != "" || config.Interface != "" ||
			config.ListenAddress != (netip.AddrPort{}) || config.DiscoveryEnabled ||
			config.Remotes != nil || config.PairingPolicy != "" {
			return ConfigV2{}, errors.New("disabled runtime configuration must be empty")
		}
		return ConfigV2{}, nil
	}

	config.StateRoot = filepath.Clean(strings.TrimSpace(config.StateRoot))
	if config.StateRoot == "." || config.StateRoot == "" {
		return ConfigV2{}, errors.New("runtime state root is required")
	}
	if !filepath.IsAbs(config.StateRoot) {
		return ConfigV2{}, errors.New("runtime state root must be absolute")
	}
	volumeRoot := filepath.VolumeName(config.StateRoot) + string(filepath.Separator)
	if config.StateRoot == volumeRoot {
		return ConfigV2{}, errors.New("runtime state root must not be the filesystem root")
	}

	config.Interface = strings.TrimSpace(config.Interface)
	if runtimeWildcard(config.Interface) {
		return ConfigV2{}, errors.New("runtime interface must be explicit")
	}
	if err := validateRuntimeListenAddress(config.ListenAddress); err != nil {
		return ConfigV2{}, err
	}
	if config.PairingPolicy != PairingPolicyV2Closed {
		return ConfigV2{}, errors.New("runtime pairing policy must be closed")
	}

	if config.Remotes != nil {
		remotes := make([]Remote, len(config.Remotes))
		seen := make(map[string]struct{}, len(config.Remotes))
		for index, source := range config.Remotes {
			remote, err := normalizeRuntimeRemote(source)
			if err != nil {
				return ConfigV2{}, fmt.Errorf("runtime remote %d: %w", index, err)
			}
			if _, exists := seen[remote.SKI]; exists {
				return ConfigV2{}, fmt.Errorf("runtime remote %d duplicates remote SKI", index)
			}
			seen[remote.SKI] = struct{}{}
			remotes[index] = remote
		}
		config.Remotes = remotes
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
	config.Remotes = append([]Remote(nil), config.Remotes...)
	return config
}

func cloneRuntimeConfigV2(config ConfigV2) ConfigV2 {
	if config.Remotes != nil {
		config.Remotes = append([]Remote{}, config.Remotes...)
	}
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
