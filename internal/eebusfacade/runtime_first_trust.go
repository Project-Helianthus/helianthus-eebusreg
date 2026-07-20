package eebusfacade

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	eebusapi "github.com/Project-Helianthus/helianthus-eebus-go/api"
	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusstore"
	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
)

type runtimeFirstTrustAuthorization struct {
	adminRuntimeDir  string
	keyProviders     []eebusstore.KeyProviderBinding
	hostAnchor       firstTrustAnchorProvider
	identityProvider firstTrustIdentityProvider
}

type runtimeAssociationBridge interface {
	firstTrustPersistence
	firstTrustControlPersistence
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
	adminDir    string
	config      RuntimeConfig
	outbound    *runtimeOutboundController
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
	if facade := reader.trustFacade(); facade != nil {
		facade.RemoteSKIConnected(service, ski)
	}
	if reader.observation != nil {
		reader.observation.RemoteSKIConnected(service, ski)
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
	if facade := reader.trustFacade(); facade != nil {
		facade.ServicePairingDetailUpdate(ski, detail)
	}
	if reader.observation != nil {
		reader.observation.ServicePairingDetailUpdate(ski, detail)
	}
}

func openRuntimeAssociationBridge(root string, bindings []eebusstore.KeyProviderBinding) (runtimeAssociationBridge, string) {
	bridge, outcome := eebusstore.OpenAssociationBridge(root, bindings)
	if bridge == nil {
		return nil, outcome
	}
	return &runtimeControlBridge{bridge: bridge}, outcome
}

func acquireRuntimeFirstTrust(
	ctx context.Context,
	config RuntimeConfig,
	material runtimeMaterial,
	service runtimeService,
	reader *runtimeServiceReader,
	dependencies runtimeDependencies,
) (*runtimeFirstTrustResources, error) {
	resources, err := classifyRuntimeFirstTrust(ctx, config, material, dependencies)
	if err != nil || resources == nil {
		return resources, err
	}
	if err := attachRuntimeFirstTrust(ctx, resources, service, reader, dependencies); err != nil {
		return nil, errors.Join(err, resources.Close())
	}
	return resources, nil
}

func classifyRuntimeFirstTrust(
	ctx context.Context,
	config RuntimeConfig,
	material runtimeMaterial,
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
	if dependencies.openAssociationBridge == nil || dependencies.startFirstTrustAdmin == nil || authorization.hostAnchor == nil || authorization.identityProvider == nil {
		return nil, errors.New("first trust runtime dependencies are incomplete")
	}

	bindings := append([]eebusstore.KeyProviderBinding(nil), authorization.keyProviders...)
	store, outcome := dependencies.openAssociationBridge(config.StateRoot, bindings)
	if store == nil {
		return nil, fmt.Errorf("first trust store unavailable: %s", outcome)
	}
	config.Remotes = append([]RuntimeRemote(nil), config.Remotes...)
	resources := &runtimeFirstTrustResources{store: store, adminDir: adminRuntimeDir, config: config}
	monotonicOrigin := time.Now()
	coordinator := newFirstTrustCoordinatorWithRecovery(
		dependencies.now,
		func() time.Duration { return time.Since(monotonicOrigin) },
		rand.Reader,
		store,
		authorization.hostAnchor,
		nil,
		firstTrustBackoffPolicy{
			base: firstTrustBackoffBase, exponentCap: firstTrustBackoffExponentCap,
			maximum: firstTrustBackoffMaximum, attemptMaximum: firstTrustAttemptMaximum,
		},
	)
	coordinator.identityProvider = authorization.identityProvider
	resources.coordinator = coordinator
	if outcome := coordinator.reopenWithRecovery(ctx); outcome == "reopen_cancelled" || outcome == "reopen_in_progress" || outcome == "store_unavailable" {
		startupErr := fmt.Errorf("first trust store reopen failed: %s", outcome)
		return nil, errors.Join(startupErr, resources.Close())
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, resources.Close())
	}

	return resources, nil
}

func attachRuntimeFirstTrust(
	ctx context.Context,
	resources *runtimeFirstTrustResources,
	service runtimeService,
	reader *runtimeServiceReader,
	dependencies runtimeDependencies,
) error {
	if resources == nil || resources.coordinator == nil || reader == nil {
		return errors.New("first trust runtime composition is incomplete")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	trustService, ok := service.(firstTrustService)
	if !ok {
		return errors.New("runtime service does not support first trust controls")
	}
	facade, err := newFirstTrustFacade(trustService, resources.coordinator)
	if err != nil {
		return fmt.Errorf("initialize first trust pairing registration: %w", err)
	}
	resources.reader = reader
	resources.facade = facade
	resources.outbound = newRuntimeOutboundController(resources.coordinator, trustService, service, resources.config)
	resources.coordinator.mu.Lock()
	resources.coordinator.effects = facade
	resources.coordinator.outbound = resources.outbound
	resources.coordinator.mu.Unlock()

	lifetime, cancel := context.WithCancel(context.Background())
	resources.cancel = cancel
	admin, err := dependencies.startFirstTrustAdmin(lifetime, resources.adminDir, resources.coordinator)
	resources.admin = admin
	if err == nil && admin == nil {
		err = errors.New("first trust admin factory returned nil")
	}
	if err != nil {
		return fmt.Errorf("first trust admin startup failed: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := reader.attachFirstTrust(facade); err != nil {
		return err
	}
	if reader.observation != nil {
		if err := reader.observation.bindTrustAdminProjection(resources.coordinator); err != nil {
			reader.detachFirstTrust(facade)
			return err
		}
	}
	return nil
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
		var checkpointErr error
		if resources.coordinator != nil {
			checkpointErr = resources.coordinator.checkpointActiveRetries(context.Background())
		}
		var registrationErr error
		if resources.coordinator != nil {
			registrationErr = resources.coordinator.shutdown()
		}
		if resources.outbound != nil {
			resources.outbound.Close()
		}
		var storeErr error
		if resources.store != nil {
			storeErr = resources.store.Close()
		}
		resources.closeErr = errors.Join(adminErr, checkpointErr, registrationErr, storeErr)
	})
	return resources.closeErr
}

func (coordinator *firstTrustCoordinator) checkpointActiveRetries(ctx context.Context) error {
	coordinator.mu.Lock()
	scopes := make([][32]byte, 0, len(coordinator.controlView.control.quarantines))
	for _, record := range coordinator.controlView.control.quarantines {
		if record.state == "BACKOFF_ACTIVE" {
			scopes = append(scopes, record.scope)
		}
	}
	coordinator.mu.Unlock()
	for _, scope := range scopes {
		if outcome := coordinator.checkpointRetry(ctx, scope); outcome != "checkpoint_durable" && outcome != "checkpoint_not_applicable" {
			return fmt.Errorf("first trust retry checkpoint failed: %s", outcome)
		}
	}
	return nil
}
