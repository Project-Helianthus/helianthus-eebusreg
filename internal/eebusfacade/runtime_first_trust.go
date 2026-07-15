package eebusfacade

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusstore"
	eebusapi "github.com/enbility/eebus-go/api"
	shipapi "github.com/enbility/ship-go/api"
)

type runtimeFirstTrustAuthorization struct {
	adminRuntimeDir string
	keyProviders    []eebusstore.KeyProviderBinding
}

type runtimeAssociationBridge interface {
	firstTrustPersistence
	Close() error
}

type runtimeAssociationBridgeFactory func(string, []eebusstore.KeyProviderBinding) (runtimeAssociationBridge, string)
type runtimeFirstTrustAdminFactory func(context.Context, string, *firstTrustCoordinator) (firstTrustAdminEndpoint, error)

type runtimeFirstTrustResources struct {
	closeOnce sync.Once
	closeErr  error

	cancel      context.CancelFunc
	reader      *runtimeServiceReader
	coordinator *firstTrustCoordinator
	facade      *firstTrustFacade
	store       runtimeAssociationBridge
	admin       firstTrustAdminEndpoint
}

type runtimeServiceReader struct {
	mu sync.RWMutex

	observation *runtimeServiceHandler
	firstTrust  *firstTrustFacade
}

var _ eebusapi.ServiceReaderInterface = (*runtimeServiceReader)(nil)

func newRuntimeServiceReader(observation *runtimeServiceHandler) *runtimeServiceReader {
	return &runtimeServiceReader{observation: observation}
}

func (reader *runtimeServiceReader) attachFirstTrust(facade *firstTrustFacade) error {
	if reader == nil || facade == nil {
		return errors.New("first trust runtime composition is incomplete")
	}
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if reader.firstTrust != nil {
		return errors.New("first trust runtime composition already exists")
	}
	reader.firstTrust = facade
	return nil
}

func (reader *runtimeServiceReader) detachFirstTrust(facade *firstTrustFacade) {
	if reader == nil {
		return
	}
	reader.mu.Lock()
	if reader.firstTrust == facade {
		reader.firstTrust = nil
	}
	reader.mu.Unlock()
}

func (reader *runtimeServiceReader) trustFacade() *firstTrustFacade {
	if reader == nil {
		return nil
	}
	reader.mu.RLock()
	defer reader.mu.RUnlock()
	return reader.firstTrust
}

func (reader *runtimeServiceReader) RemoteSKIConnected(service eebusapi.ServiceInterface, ski string) {
	if reader.observation != nil {
		reader.observation.RemoteSKIConnected(service, ski)
	}
	if facade := reader.trustFacade(); facade != nil {
		facade.RemoteSKIConnected(service, ski)
	}
}

func (reader *runtimeServiceReader) RemoteSKIDisconnected(service eebusapi.ServiceInterface, ski string) {
	if reader.observation != nil {
		reader.observation.RemoteSKIDisconnected(service, ski)
	}
	if facade := reader.trustFacade(); facade != nil {
		facade.RemoteSKIDisconnected(service, ski)
	}
}

func (reader *runtimeServiceReader) VisibleRemoteServicesUpdated(service eebusapi.ServiceInterface, remotes []shipapi.RemoteService) {
	if reader.observation != nil {
		reader.observation.VisibleRemoteServicesUpdated(service, remotes)
	}
	if facade := reader.trustFacade(); facade != nil {
		facade.VisibleRemoteServicesUpdated(service, remotes)
	}
}

func (reader *runtimeServiceReader) ServiceShipIDUpdate(ski string, shipID string) {
	if reader.observation != nil {
		reader.observation.ServiceShipIDUpdate(ski, shipID)
	}
	if facade := reader.trustFacade(); facade != nil {
		facade.ServiceShipIDUpdate(ski, shipID)
	}
}

func (reader *runtimeServiceReader) ServicePairingDetailUpdate(ski string, detail *shipapi.ConnectionStateDetail) {
	if reader.observation != nil {
		reader.observation.ServicePairingDetailUpdate(ski, detail)
	}
	if facade := reader.trustFacade(); facade != nil {
		facade.ServicePairingDetailUpdate(ski, detail)
	}
}

func openRuntimeAssociationBridge(root string, bindings []eebusstore.KeyProviderBinding) (runtimeAssociationBridge, string) {
	bridge, outcome := eebusstore.OpenAssociationBridge(root, bindings)
	if bridge == nil {
		return nil, outcome
	}
	return bridge, outcome
}

func acquireRuntimeFirstTrust(
	ctx context.Context,
	config RuntimeConfig,
	material runtimeMaterial,
	service runtimeService,
	reader *runtimeServiceReader,
	dependencies runtimeDependencies,
) (*runtimeFirstTrustResources, error) {
	authorization := material.firstTrust
	if authorization == nil {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	adminRuntimeDir, err := validateRuntimeFirstTrustAuthorization(config.StateRoot, authorization)
	if err != nil {
		return nil, err
	}
	if dependencies.openAssociationBridge == nil || dependencies.startFirstTrustAdmin == nil {
		return nil, errors.New("first trust runtime dependencies are incomplete")
	}
	trustService, ok := service.(firstTrustService)
	if !ok {
		return nil, errors.New("runtime service does not support first trust controls")
	}

	bindings := append([]eebusstore.KeyProviderBinding(nil), authorization.keyProviders...)
	store, outcome := dependencies.openAssociationBridge(config.StateRoot, bindings)
	if store == nil {
		return nil, fmt.Errorf("first trust store unavailable: %s", outcome)
	}
	resources := &runtimeFirstTrustResources{reader: reader, store: store}
	coordinator := newFirstTrustCoordinator(dependencies.now, nil, store, nil)
	facade := newFirstTrustFacade(trustService, coordinator)
	coordinator.effects = facade
	resources.coordinator = coordinator
	resources.facade = facade
	if outcome := coordinator.reopen(ctx); outcome != "pairing_closed" {
		startupErr := fmt.Errorf("first trust store reopen failed: %s", outcome)
		return nil, errors.Join(startupErr, resources.Close())
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, resources.Close())
	}

	lifetime, cancel := context.WithCancel(context.Background())
	resources.cancel = cancel
	admin, err := dependencies.startFirstTrustAdmin(lifetime, adminRuntimeDir, coordinator)
	resources.admin = admin
	if err == nil && admin == nil {
		err = errors.New("first trust admin factory returned nil")
	}
	if err != nil {
		startupErr := fmt.Errorf("first trust admin startup failed: %w", err)
		return nil, errors.Join(startupErr, resources.Close())
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, resources.Close())
	}
	if err := reader.attachFirstTrust(facade); err != nil {
		return nil, errors.Join(err, resources.Close())
	}
	return resources, nil
}

func validateRuntimeFirstTrustAuthorization(stateRoot string, authorization *runtimeFirstTrustAuthorization) (string, error) {
	if authorization == nil {
		return "", errors.New("first trust runtime authorization is missing")
	}
	adminRuntimeDir := filepath.Clean(strings.TrimSpace(authorization.adminRuntimeDir))
	if adminRuntimeDir == "." || adminRuntimeDir == "" || !filepath.IsAbs(adminRuntimeDir) {
		return "", errors.New("first trust admin runtime directory is invalid")
	}
	volumeRoot := filepath.VolumeName(adminRuntimeDir) + string(filepath.Separator)
	if adminRuntimeDir == volumeRoot {
		return "", errors.New("first trust admin runtime directory is invalid")
	}
	relative, err := filepath.Rel(stateRoot, adminRuntimeDir)
	if err != nil || relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("first trust admin runtime directory must be outside StateRoot")
	}
	return adminRuntimeDir, nil
}

func (resources *runtimeFirstTrustResources) Close() error {
	if resources == nil {
		return nil
	}
	resources.closeOnce.Do(func() {
		if resources.cancel != nil {
			resources.cancel()
		}
		var adminErr error
		if resources.admin != nil {
			adminErr = resources.admin.Close()
		}
		if resources.reader != nil {
			resources.reader.detachFirstTrust(resources.facade)
		}
		if resources.coordinator != nil {
			resources.coordinator.shutdown()
		}
		var storeErr error
		if resources.store != nil {
			storeErr = resources.store.Close()
		}
		resources.closeErr = errors.Join(adminErr, storeErr)
	})
	return resources.closeErr
}
