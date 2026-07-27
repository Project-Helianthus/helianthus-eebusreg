package eebusfacade

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const rawConnectionGenerationStateFilename = "raw-connection-generations-v1.json"

type rawConnectionGenerationStore interface {
	advance(runtimeEpoch uint64, ski string, generation uint64) error
}

type rawConnectionGenerationStoreFunc func(uint64, string, uint64) error

func (fn rawConnectionGenerationStoreFunc) advance(runtimeEpoch uint64, ski string, generation uint64) error {
	if fn == nil {
		return nil
	}
	return fn(runtimeEpoch, ski, generation)
}

type rawConnectionGenerationFileStore struct {
	mu           sync.Mutex
	path         string
	runtimeEpoch uint64
	generations  map[string]uint64
}

type rawConnectionGenerationState struct {
	RuntimeEpoch uint64            `json:"runtime_epoch"`
	Generations  map[string]uint64 `json:"generations"`
}

func newRawConnectionGenerationStore(stateRoot string) (rawConnectionGenerationStore, error) {
	stateRoot = strings.TrimSpace(stateRoot)
	if stateRoot == "" {
		return nil, errors.New("raw connection generation state root is unavailable")
	}
	return &rawConnectionGenerationFileStore{
		path:        filepath.Join(stateRoot, rawConnectionGenerationStateFilename),
		generations: make(map[string]uint64),
	}, nil
}

func (store *rawConnectionGenerationFileStore) advance(runtimeEpoch uint64, ski string, generation uint64) error {
	if store == nil || runtimeEpoch == 0 || generation == 0 || !validRuntimeSKI(ski) {
		return errors.New("raw connection generation persistence input is invalid")
	}
	ski = strings.ToLower(strings.TrimSpace(ski))
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.loadLocked(runtimeEpoch); err != nil {
		return err
	}
	if generation <= store.generations[ski] {
		return errors.New("raw connection generation is not monotonic")
	}
	updated := make(map[string]uint64, len(store.generations)+1)
	for key, value := range store.generations {
		updated[key] = value
	}
	updated[ski] = generation
	if err := store.writeLocked(runtimeEpoch, updated); err != nil {
		return err
	}
	store.runtimeEpoch = runtimeEpoch
	store.generations = updated
	return nil
}

func (store *rawConnectionGenerationFileStore) loadLocked(runtimeEpoch uint64) error {
	if store.runtimeEpoch == runtimeEpoch {
		return nil
	}
	payload, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		store.runtimeEpoch = runtimeEpoch
		store.generations = make(map[string]uint64)
		return nil
	}
	if err != nil {
		return errors.New("raw connection generation state cannot be read")
	}
	var state rawConnectionGenerationState
	if json.Unmarshal(payload, &state) != nil || state.RuntimeEpoch == 0 {
		return errors.New("raw connection generation state is invalid")
	}
	if state.RuntimeEpoch != runtimeEpoch {
		store.runtimeEpoch = runtimeEpoch
		store.generations = make(map[string]uint64)
		return nil
	}
	for ski, generation := range state.Generations {
		if !validRuntimeSKI(ski) || generation == 0 {
			return errors.New("raw connection generation state is invalid")
		}
	}
	store.runtimeEpoch = runtimeEpoch
	store.generations = state.Generations
	return nil
}

func (store *rawConnectionGenerationFileStore) writeLocked(runtimeEpoch uint64, generations map[string]uint64) error {
	keys := make([]string, 0, len(generations))
	for ski := range generations {
		keys = append(keys, ski)
	}
	sort.Strings(keys)
	ordered := make(map[string]uint64, len(keys))
	for _, ski := range keys {
		ordered[ski] = generations[ski]
	}
	payload, err := json.Marshal(rawConnectionGenerationState{RuntimeEpoch: runtimeEpoch, Generations: ordered})
	if err != nil {
		return errors.New("raw connection generation state cannot be encoded")
	}
	temporary, err := os.CreateTemp(filepath.Dir(store.path), ".raw-connection-generations-")
	if err != nil {
		return errors.New("raw connection generation state cannot be created")
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return errors.New("raw connection generation state cannot be protected")
	}
	if _, err := temporary.Write(payload); err != nil || temporary.Sync() != nil || temporary.Close() != nil {
		return errors.New("raw connection generation state cannot be persisted")
	}
	if err := os.Rename(temporaryPath, store.path); err != nil {
		return errors.New("raw connection generation state cannot be published")
	}
	directory, err := os.Open(filepath.Dir(store.path))
	if err != nil {
		return errors.New("raw connection generation state directory is unavailable")
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return errors.New("raw connection generation state durability is unknown")
	}
	return nil
}
