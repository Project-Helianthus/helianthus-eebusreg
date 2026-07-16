//go:build linux || darwin

package eebusstore

import (
	"bytes"
	"context"
	"crypto"
	"sort"
	"sync"
)

type KeyProvider interface {
	Probe(string, uint64) error
	Validate([]byte, []byte) error
	Unseal([]byte) (crypto.Signer, error)
}

type KeyProviderBinding struct {
	ID       string
	Version  uint64
	Provider KeyProvider
}

type AssociationBridge struct {
	mu sync.Mutex

	config storeConfig
	opened *store
}

func OpenAssociationBridge(root string, bindings []KeyProviderBinding) (*AssociationBridge, string) {
	backend, err := newNativeSyscallBackend(nil)
	if err != nil {
		return nil, string(outcomeFilesystemCapabilityUnavailable)
	}
	providers := make(map[providerKey]protectedKeyProvider, len(bindings))
	for _, binding := range bindings {
		if binding.Provider == nil || binding.ID == "" || binding.Version == 0 {
			return nil, string(outcomeKeyProviderUnavailable)
		}
		key := providerKey{id: binding.ID, version: binding.Version}
		if _, exists := providers[key]; exists {
			return nil, string(outcomeKeyProviderUnavailable)
		}
		providers[key] = keyProviderAdapter{provider: binding.Provider}
	}
	graph, err := currentMigrationGraph()
	if err != nil {
		return nil, string(outcomeMigrationFailed)
	}
	bridge := &AssociationBridge{config: storeConfig{
		root: root, backend: backend, providers: providers, migrations: graph,
		retainUnavailableProtectedState: true,
	}}
	result := openStore(bridge.config)
	if result.store == nil {
		return nil, string(result.outcome)
	}
	bridge.opened = result.store
	return bridge, string(result.outcome)
}

func (bridge *AssociationBridge) Reload(ctx context.Context) (uint64, map[string]string, string) {
	if ctx == nil {
		ctx = context.Background()
	}
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	if ctx.Err() != nil {
		return 0, nil, string(outcomeIOFailed)
	}
	if bridge.opened != nil {
		_ = bridge.opened.close()
		bridge.opened = nil
	}
	result := openStore(bridge.config)
	if result.store == nil || result.state == nil {
		return 0, nil, string(result.outcome)
	}
	bridge.opened = result.store
	return selectedStoreGeneration(bridge.opened), associationMap(*result.state), string(result.outcome)
}

func (bridge *AssociationBridge) SelectedGeneration() uint64 {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	return selectedStoreGeneration(bridge.opened)
}

func (bridge *AssociationBridge) Commit(ctx context.Context, expected uint64, remote []byte, shipID string) string {
	if ctx == nil {
		ctx = context.Background()
	}
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	if ctx.Err() != nil || bridge.opened == nil || selectedStoreGeneration(bridge.opened) != expected {
		return string(outcomeCommitNotPublished)
	}
	state := cloneStateV1(bridge.opened.state)
	state.remoteIdentities = append(state.remoteIdentities, remoteIdentityV1{
		recordID:     bytes.Clone(remote),
		remoteSKI:    bytes.Clone(remote),
		remoteSHIPID: shipID,
	})
	sort.Slice(state.remoteIdentities, func(left, right int) bool {
		return bytes.Compare(state.remoteIdentities[left].recordID, state.remoteIdentities[right].recordID) < 0
	})
	return string(bridge.opened.commit(state).outcome)
}

func (bridge *AssociationBridge) Close() error {
	if bridge == nil {
		return nil
	}
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	if bridge.opened == nil {
		return nil
	}
	err := bridge.opened.close()
	bridge.opened = nil
	return err
}

type keyProviderAdapter struct {
	provider KeyProvider
}

func (adapter keyProviderAdapter) probe(id string, version uint64) error {
	return adapter.provider.Probe(id, version)
}

func (adapter keyProviderAdapter) validate(blob, spki []byte) error {
	return adapter.provider.Validate(bytes.Clone(blob), bytes.Clone(spki))
}

func (adapter keyProviderAdapter) unseal(blob []byte) (crypto.Signer, error) {
	return adapter.provider.Unseal(bytes.Clone(blob))
}

func selectedStoreGeneration(opened *store) uint64 {
	if opened == nil || opened.manifest == nil {
		return 0
	}
	return opened.manifest.current.generation
}

func associationMap(state stateV1) map[string]string {
	associations := make(map[string]string, len(state.remoteIdentities))
	for _, identity := range state.remoteIdentities {
		associations[string(identity.remoteSKI)] = identity.remoteSHIPID
	}
	return associations
}
