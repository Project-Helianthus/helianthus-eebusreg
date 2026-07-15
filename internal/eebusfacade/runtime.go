package eebusfacade

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	eebusservice "github.com/enbility/eebus-go/service"
)

var errProtectedRuntimeCredentials = errors.New("eebus runtime protected credentials are unavailable")

type Backend interface {
	Run(context.Context, func([]byte)) error
	Close() error
}

type RuntimeConfig struct {
	StateRoot string
	Interface string
	Remotes   []RuntimeRemote
}

type RuntimeRemote struct {
	SKI         string
	Endpoint    RuntimeEndpoint
	Pretrusted  bool
	Allowlisted bool
}

type RuntimeEndpoint struct {
	Host string
	Port int
	Path string
}

type serviceBackend struct {
	service   *eebusservice.Service
	reducer   *runtimeObservationReducer
	closeOnce sync.Once
}

type runtimeFeatureObservation struct {
	ID   string
	Role string
}

type runtimeEntityObservation struct {
	ID       string
	Features []runtimeFeatureObservation
}

type runtimeDeviceObservation struct {
	ID         string
	Entities   []runtimeEntityObservation
	UseCaseIDs []string
}

type runtimeGraphObservation struct {
	RuntimeID  string
	LocalSKI   string
	RemoteSKI  string
	SessionID  string
	ServiceIDs []string
	Devices    []runtimeDeviceObservation
}

type runtimeObservationReducer struct {
	mu sync.RWMutex

	initialized bool
	runtimeID   string
	localSKI    string
	remotes     map[string]runtimeGraphObservation
}

var _ Backend = (*serviceBackend)(nil)

func Acquire(ctx context.Context, config RuntimeConfig) (Backend, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stateRoot := filepath.Clean(strings.TrimSpace(config.StateRoot))
	if stateRoot == "." || stateRoot == "" || !filepath.IsAbs(stateRoot) {
		return nil, errors.New("runtime state root must be an absolute non-empty path")
	}
	volumeRoot := filepath.VolumeName(stateRoot) + string(filepath.Separator)
	if stateRoot == volumeRoot {
		return nil, errors.New("runtime state root must not be the filesystem root")
	}
	if len(config.Remotes) == 0 {
		return nil, errors.New("at least one runtime remote is required")
	}

	reducer := newRuntimeObservationReducer()
	_ = reducer
	seen := make(map[string]struct{}, len(config.Remotes))
	for index, remote := range config.Remotes {
		ski := strings.ToLower(strings.TrimSpace(remote.SKI))
		if ski == "" {
			return nil, fmt.Errorf("runtime remote %d SKI is required", index)
		}
		if _, exists := seen[ski]; exists {
			return nil, fmt.Errorf("runtime remote %d duplicates remote SKI", index)
		}
		seen[ski] = struct{}{}
		if err := validateRuntimeScope(config.Interface, remote.Endpoint.Host, remote.Endpoint.Port); err != nil {
			return nil, fmt.Errorf("runtime remote %d scope: %w", index, err)
		}
		path := strings.TrimSpace(remote.Endpoint.Path)
		if path == "" || !strings.HasPrefix(path, "/") {
			return nil, fmt.Errorf("runtime remote %d endpoint path must be an absolute URL path", index)
		}
		if !runtimeRemoteAdmitted(remote.Pretrusted, remote.Allowlisted) {
			return nil, fmt.Errorf("%w: runtime remote %d is not admitted", errProtectedRuntimeCredentials, index)
		}
	}
	return nil, errProtectedRuntimeCredentials
}

func (backend *serviceBackend) Run(ctx context.Context, publish func([]byte)) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if backend.service == nil || backend.reducer == nil || publish == nil {
		return errors.New("eebus runtime service backend is incomplete")
	}
	backend.service.Start()
	<-ctx.Done()
	return ctx.Err()
}

func (backend *serviceBackend) Close() error {
	backend.closeOnce.Do(func() {
		if backend.service != nil {
			backend.service.Shutdown()
		}
	})
	return nil
}

func validateRuntimeScope(interfaceName, host string, port int) error {
	if runtimeScopeWildcard(interfaceName) {
		return errors.New("runtime interface must be explicit")
	}
	if runtimeScopeWildcard(host) {
		return errors.New("runtime endpoint host must be explicit")
	}
	if strings.Contains(strings.TrimSpace(host), "://") {
		return errors.New("runtime endpoint host must not include a URL scheme")
	}
	if port < 1 || port > 65535 {
		return errors.New("runtime endpoint port must be between 1 and 65535")
	}
	return nil
}

func runtimeScopeWildcard(value string) bool {
	switch strings.TrimSpace(value) {
	case "", "*", "0.0.0.0", "::", "[::]":
		return true
	default:
		return false
	}
}

func runtimeRemoteAdmitted(pretrusted, allowlisted bool) bool {
	return pretrusted || allowlisted
}

func newRuntimeObservationReducer() *runtimeObservationReducer {
	return &runtimeObservationReducer{remotes: make(map[string]runtimeGraphObservation)}
}

func (reducer *runtimeObservationReducer) Replace(source runtimeGraphObservation) error {
	observation, err := normalizeRuntimeGraphObservation(source)
	if err != nil {
		return err
	}

	reducer.mu.Lock()
	defer reducer.mu.Unlock()
	if reducer.initialized {
		if observation.RuntimeID != reducer.runtimeID {
			return errors.New("runtime observation changed the stable runtime identity")
		}
		if observation.LocalSKI != reducer.localSKI {
			return errors.New("runtime observation changed the persisted local identity")
		}
	} else {
		reducer.initialized = true
		reducer.runtimeID = observation.RuntimeID
		reducer.localSKI = observation.LocalSKI
	}
	reducer.remotes[observation.RemoteSKI] = cloneRuntimeGraphObservation(observation)
	return nil
}

func (reducer *runtimeObservationReducer) Snapshot() []runtimeGraphObservation {
	reducer.mu.RLock()
	result := make([]runtimeGraphObservation, 0, len(reducer.remotes))
	for _, observation := range reducer.remotes {
		result = append(result, cloneRuntimeGraphObservation(observation))
	}
	reducer.mu.RUnlock()
	sort.Slice(result, func(left, right int) bool {
		return result[left].RemoteSKI < result[right].RemoteSKI
	})
	return result
}

func normalizeRuntimeGraphObservation(source runtimeGraphObservation) (runtimeGraphObservation, error) {
	result := source
	result.RuntimeID = strings.TrimSpace(result.RuntimeID)
	result.LocalSKI = strings.TrimSpace(result.LocalSKI)
	result.RemoteSKI = strings.TrimSpace(result.RemoteSKI)
	result.SessionID = strings.TrimSpace(result.SessionID)
	if result.RuntimeID == "" || result.LocalSKI == "" || result.RemoteSKI == "" || result.SessionID == "" {
		return runtimeGraphObservation{}, errors.New("runtime graph identities are required")
	}

	serviceIDs, err := uniqueRuntimeStrings(result.ServiceIDs, "service")
	if err != nil {
		return runtimeGraphObservation{}, err
	}
	result.ServiceIDs = serviceIDs

	devices := make(map[string]runtimeDeviceObservation, len(result.Devices))
	for _, sourceDevice := range result.Devices {
		device, err := normalizeRuntimeDeviceObservation(sourceDevice)
		if err != nil {
			return runtimeGraphObservation{}, err
		}
		if existing, ok := devices[device.ID]; ok {
			device, err = mergeRuntimeDeviceObservations(existing, device)
			if err != nil {
				return runtimeGraphObservation{}, err
			}
		}
		devices[device.ID] = device
	}
	result.Devices = make([]runtimeDeviceObservation, 0, len(devices))
	for _, device := range devices {
		result.Devices = append(result.Devices, device)
	}
	sort.Slice(result.Devices, func(left, right int) bool {
		return result.Devices[left].ID < result.Devices[right].ID
	})
	return result, nil
}

func normalizeRuntimeDeviceObservation(source runtimeDeviceObservation) (runtimeDeviceObservation, error) {
	result := source
	result.ID = strings.TrimSpace(result.ID)
	if result.ID == "" {
		return runtimeDeviceObservation{}, errors.New("runtime device identity is required")
	}
	useCaseIDs, err := uniqueRuntimeStrings(result.UseCaseIDs, "use case")
	if err != nil {
		return runtimeDeviceObservation{}, err
	}
	result.UseCaseIDs = useCaseIDs

	entities := make(map[string]runtimeEntityObservation, len(result.Entities))
	for _, sourceEntity := range result.Entities {
		entity, err := normalizeRuntimeEntityObservation(sourceEntity)
		if err != nil {
			return runtimeDeviceObservation{}, err
		}
		if existing, ok := entities[entity.ID]; ok {
			entity, err = mergeRuntimeEntityObservations(existing, entity)
			if err != nil {
				return runtimeDeviceObservation{}, err
			}
		}
		entities[entity.ID] = entity
	}
	result.Entities = make([]runtimeEntityObservation, 0, len(entities))
	for _, entity := range entities {
		result.Entities = append(result.Entities, entity)
	}
	sort.Slice(result.Entities, func(left, right int) bool {
		return result.Entities[left].ID < result.Entities[right].ID
	})
	return result, nil
}

func normalizeRuntimeEntityObservation(source runtimeEntityObservation) (runtimeEntityObservation, error) {
	result := source
	result.ID = strings.TrimSpace(result.ID)
	if result.ID == "" {
		return runtimeEntityObservation{}, errors.New("runtime entity identity is required")
	}
	features := make(map[string]runtimeFeatureObservation, len(result.Features))
	for _, sourceFeature := range result.Features {
		feature := sourceFeature
		feature.ID = strings.TrimSpace(feature.ID)
		feature.Role = strings.TrimSpace(feature.Role)
		if feature.ID == "" {
			return runtimeEntityObservation{}, errors.New("runtime feature identity is required")
		}
		switch feature.Role {
		case "", "client", "server":
		default:
			return runtimeEntityObservation{}, errors.New("runtime feature role is unsupported")
		}
		if existing, ok := features[feature.ID]; ok && existing.Role != feature.Role {
			return runtimeEntityObservation{}, errors.New("runtime feature identity has conflicting roles")
		}
		features[feature.ID] = feature
	}
	result.Features = make([]runtimeFeatureObservation, 0, len(features))
	for _, feature := range features {
		result.Features = append(result.Features, feature)
	}
	sort.Slice(result.Features, func(left, right int) bool {
		if result.Features[left].ID == result.Features[right].ID {
			return result.Features[left].Role < result.Features[right].Role
		}
		return result.Features[left].ID < result.Features[right].ID
	})
	return result, nil
}

func mergeRuntimeDeviceObservations(left, right runtimeDeviceObservation) (runtimeDeviceObservation, error) {
	result := left
	useCaseIDs, err := uniqueRuntimeStrings(append(append([]string(nil), left.UseCaseIDs...), right.UseCaseIDs...), "use case")
	if err != nil {
		return runtimeDeviceObservation{}, err
	}
	result.UseCaseIDs = useCaseIDs
	entities := make(map[string]runtimeEntityObservation, len(left.Entities)+len(right.Entities))
	for _, entity := range left.Entities {
		entities[entity.ID] = entity
	}
	for _, entity := range right.Entities {
		if existing, ok := entities[entity.ID]; ok {
			entity, err = mergeRuntimeEntityObservations(existing, entity)
			if err != nil {
				return runtimeDeviceObservation{}, err
			}
		}
		entities[entity.ID] = entity
	}
	result.Entities = make([]runtimeEntityObservation, 0, len(entities))
	for _, entity := range entities {
		result.Entities = append(result.Entities, entity)
	}
	sort.Slice(result.Entities, func(left, right int) bool {
		return result.Entities[left].ID < result.Entities[right].ID
	})
	return result, nil
}

func mergeRuntimeEntityObservations(left, right runtimeEntityObservation) (runtimeEntityObservation, error) {
	result := left
	features := make(map[string]runtimeFeatureObservation, len(left.Features)+len(right.Features))
	for _, feature := range left.Features {
		features[feature.ID] = feature
	}
	for _, feature := range right.Features {
		if existing, ok := features[feature.ID]; ok && existing.Role != feature.Role {
			return runtimeEntityObservation{}, errors.New("runtime feature identity has conflicting roles")
		}
		features[feature.ID] = feature
	}
	result.Features = make([]runtimeFeatureObservation, 0, len(features))
	for _, feature := range features {
		result.Features = append(result.Features, feature)
	}
	sort.Slice(result.Features, func(left, right int) bool {
		if result.Features[left].ID == result.Features[right].ID {
			return result.Features[left].Role < result.Features[right].Role
		}
		return result.Features[left].ID < result.Features[right].ID
	})
	return result, nil
}

func uniqueRuntimeStrings(values []string, label string) ([]string, error) {
	set := make(map[string]struct{}, len(values))
	for _, source := range values {
		value := strings.TrimSpace(source)
		if value == "" {
			return nil, fmt.Errorf("runtime %s identity is required", label)
		}
		set[value] = struct{}{}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func cloneRuntimeGraphObservation(source runtimeGraphObservation) runtimeGraphObservation {
	result := source
	result.ServiceIDs = append([]string(nil), source.ServiceIDs...)
	result.Devices = make([]runtimeDeviceObservation, len(source.Devices))
	for deviceIndex, device := range source.Devices {
		result.Devices[deviceIndex] = device
		result.Devices[deviceIndex].UseCaseIDs = append([]string(nil), device.UseCaseIDs...)
		result.Devices[deviceIndex].Entities = make([]runtimeEntityObservation, len(device.Entities))
		for entityIndex, entity := range device.Entities {
			result.Devices[deviceIndex].Entities[entityIndex] = entity
			result.Devices[deviceIndex].Entities[entityIndex].Features = append([]runtimeFeatureObservation(nil), entity.Features...)
		}
	}
	return result
}
