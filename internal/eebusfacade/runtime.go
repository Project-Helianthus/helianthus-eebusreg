package eebusfacade

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"time"

	eebusapi "github.com/Project-Helianthus/helianthus-eebus-go/api"
	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusmutation"
	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusservicebridge"
	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
	shipcert "github.com/Project-Helianthus/helianthus-ship-go/cert"
	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
)

var (
	errProtectedRuntimeCredentials = errors.New("eebus runtime protected credentials are unavailable")
	errRuntimeTrustEffectsDenied   = errors.New("eebus runtime trust classification denies transport effects")
)

type runtimeLocalReadSourceError struct {
	reason string
}

func (failure *runtimeLocalReadSourceError) Error() string {
	if failure == nil || failure.reason == "" {
		return "eebus runtime local READ source is unavailable"
	}
	return "eebus runtime local READ source is unavailable: " + failure.reason
}

type Backend interface {
	Run(context.Context, func([]byte)) error
	Close() error
}

type RuntimeConfig struct {
	StateRoot        string
	Interface        string
	ListenPort       int
	ListenAddress    netip.AddrPort
	DiscoveryEnabled bool
	Remotes          []RuntimeRemote
	LabProfiles      []RuntimeLabProfile
}

type RuntimeLabProfile struct {
	Contract               string
	ProfileID              string
	Target                 eebusraw.FeatureTargetV1
	AllowedValueHashes     []eebusraw.HashV1
	RollbackValueHash      eebusraw.HashV1
	MaximumProbeTTLSeconds uint64
	SafetyPredicates       []string
	EvidenceHashes         []eebusraw.HashV1
	ExpiresAt              time.Time
}

type RuntimeRemote struct {
	SKI         string
	Pretrusted  bool
	Allowlisted bool
}

type serviceBackend struct {
	mu               sync.Mutex
	service          runtimeService
	handler          *runtimeServiceHandler
	rawFeatures      *rawFeatureRuntimeBridge
	rawMutations     *eebusmutation.Coordinator
	firstTrust       *runtimeFirstTrustResources
	operatorAdmin    *operatorAdminV1Bridge
	outgoingAttempts *firstTrustOutgoingAttemptBridge
	unsubscribeSPINE func() error
	listenerTerminal <-chan error
	runClaimed       bool
	serviceStarted   bool
	closed           bool
	closeErr         error
	closeDone        chan struct{}
}

type runtimeMaterial struct {
	certificate           tls.Certificate
	localSKI              string
	nodeToken             string
	pretrusted            map[string]bool
	firstTrust            *runtimeFirstTrustAuthorization
	outgoingAttemptBridge *firstTrustOutgoingAttemptBridge
}

const redactedRuntimeMaterial = "eebusfacade.runtime_material{redacted}"

func (runtimeMaterial) String() string {
	return redactedRuntimeMaterial
}

func (runtimeMaterial) Format(state fmt.State, verb rune) {
	if verb == 'q' {
		_, _ = fmt.Fprintf(state, "%q", redactedRuntimeMaterial)
		return
	}
	_, _ = fmt.Fprint(state, redactedRuntimeMaterial)
}

type runtimeMaterialLoader func(context.Context, string) (runtimeMaterial, error)

type runtimeService interface {
	Setup() error
	Start()
	Shutdown()
	RegisterRemoteSKI(string)
	LocalService() *shipapi.ServiceDetails
	LocalDevice() spineapi.DeviceLocalInterface
}

type runtimeScopedService interface {
	StartWithPolicy() error
	ListenerTerminal() <-chan error
}

type runtimeServiceFactory func(RuntimeConfig, runtimeMaterial, eebusapi.ServiceReaderInterface) (runtimeService, error)

type runtimeSPINEEventSubscriber func(spineapi.EventHandlerInterface) (func() error, error)

type runtimeMutationCoordinatorFactory func(
	eebusmutation.CoordinatorConfig,
	eebusmutation.CoordinatorDependencies,
) (*eebusmutation.Coordinator, *eebusraw.ErrorV1)

type runtimeDependencies struct {
	loadMaterial           runtimeMaterialLoader
	newService             runtimeServiceFactory
	subscribeSPINEEvents   runtimeSPINEEventSubscriber
	now                    func() time.Time
	openAssociationBridge  runtimeAssociationBridgeFactory
	startFirstTrustAdmin   runtimeFirstTrustAdminFactory
	newMutationCoordinator runtimeMutationCoordinatorFactory
}

var _ Backend = (*serviceBackend)(nil)
var _ RawFeatureBackend = (*serviceBackend)(nil)
var _ RawMutationBackend = (*serviceBackend)(nil)
var _ OperatorAdminV1Backend = (*serviceBackend)(nil)

var defaultRuntimeDependencies = runtimeDependencies{
	loadMaterial:           loadProtectedRuntimeMaterial,
	newService:             newEEBusService,
	subscribeSPINEEvents:   subscribeRuntimeSPINEEvents,
	now:                    time.Now,
	openAssociationBridge:  openRuntimeAssociationBridge,
	startFirstTrustAdmin:   startFirstTrustAdmin,
	newMutationCoordinator: eebusmutation.NewCoordinator,
}

func Acquire(ctx context.Context, config RuntimeConfig) (Backend, error) {
	return acquireRuntime(ctx, config, defaultRuntimeDependencies)
}

func AcquireNativeRuntimeV2(ctx context.Context, config RuntimeConfig) (Backend, error) {
	return acquireRuntimeWithSnapshotSerializer(
		ctx, config, defaultRuntimeDependencies, marshalNativeRuntimeSnapshotV2WithIdentity,
	)
}

func acquireRuntime(ctx context.Context, config RuntimeConfig, dependencies runtimeDependencies) (Backend, error) {
	return acquireRuntimeWithSnapshotSerializer(ctx, config, dependencies, marshalRuntimeSnapshotWithIdentity)
}

type runtimeAcquisition struct {
	config                   RuntimeConfig
	dependencies             runtimeDependencies
	stateRoot                string
	labProfiles              []eebusmutation.LabProfile
	material                 runtimeMaterial
	firstTrust               *runtimeFirstTrustResources
	rawTokenIssuer           *rawReadTokenIssuer
	rawRuntimeEpoch          uint64
	rawConnectionGenerations rawConnectionGenerationStore
	outgoingAttemptBridge    *firstTrustOutgoingAttemptBridge
	handler                  *runtimeServiceHandler
	reader                   *runtimeServiceReader
	service                  runtimeService
	runtimeEpoch             func() uint64
	rawFeatures              *rawFeatureRuntimeBridge
	unsubscribeSPINE         func() error
	operatorAdmin            *operatorAdminV1Bridge
	rawMutations             *eebusmutation.Coordinator
}

// acquireRuntimeWithSnapshotSerializer runs the startup steps in order. A failing
// step rolls back what earlier steps opened, latest first: raw mutations, SPINE
// events, the service, the outgoing-attempt gate, then first trust.
// serviceBackend.Close closes the operator admin bridge, then follows that order.
func acquireRuntimeWithSnapshotSerializer(
	ctx context.Context,
	config RuntimeConfig,
	dependencies runtimeDependencies,
	serializer runtimeSnapshotSerializer,
) (Backend, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	stateRoot, labProfiles, err := validateRuntimeAcquisition(ctx, config, dependencies)
	if err != nil {
		return nil, err
	}
	material, err := loadAdmittedRuntimeMaterial(ctx, config, stateRoot, dependencies)
	if err != nil {
		return nil, err
	}
	firstTrust, err := classifyRuntimeFirstTrust(ctx, config, material, dependencies)
	if err != nil {
		return nil, err
	}
	acquisition := &runtimeAcquisition{
		config: config, dependencies: dependencies, stateRoot: stateRoot,
		labProfiles: labProfiles, material: material, firstTrust: firstTrust,
	}
	if err := acquisition.prepareRawRuntime(); err != nil {
		return nil, err
	}
	if err := acquisition.openService(serializer); err != nil {
		return nil, err
	}
	if err := acquisition.bindRawFeatures(); err != nil {
		return nil, err
	}
	if err := acquisition.subscribeSPINE(); err != nil {
		return nil, err
	}
	if err := acquisition.attachFirstTrust(ctx); err != nil {
		return nil, err
	}
	if err := acquisition.openRawMutations(); err != nil {
		return nil, err
	}
	return acquisition.startBackend()
}

func validateRuntimeAcquisition(
	ctx context.Context,
	config RuntimeConfig,
	dependencies runtimeDependencies,
) (string, []eebusmutation.LabProfile, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	stateRoot := filepath.Clean(strings.TrimSpace(config.StateRoot))
	if stateRoot == "." || stateRoot == "" || !filepath.IsAbs(stateRoot) {
		return "", nil, errors.New("runtime state root must be an absolute non-empty path")
	}
	volumeRoot := filepath.VolumeName(stateRoot) + string(filepath.Separator)
	if stateRoot == volumeRoot {
		return "", nil, errors.New("runtime state root must not be the filesystem root")
	}
	if len(config.Remotes) == 0 && !config.ListenAddress.IsValid() {
		return "", nil, errors.New("at least one runtime remote is required")
	}
	if config.ListenPort < 1 || config.ListenPort > 65535 {
		return "", nil, errors.New("runtime listen port must be between 1 and 65535")
	}
	if dependencies.loadMaterial == nil || dependencies.now == nil {
		return "", nil, errors.New("runtime dependencies are incomplete")
	}
	labProfiles := mutationLabProfilesForRuntime(config.LabProfiles)

	seen := make(map[string]struct{}, len(config.Remotes))
	for index, remote := range config.Remotes {
		ski := strings.ToLower(strings.TrimSpace(remote.SKI))
		if !validRuntimeSKI(ski) {
			return "", nil, fmt.Errorf("runtime remote %d SKI must contain 40 hexadecimal characters", index)
		}
		if _, exists := seen[ski]; exists {
			return "", nil, fmt.Errorf("runtime remote %d duplicates remote SKI", index)
		}
		seen[ski] = struct{}{}
		if err := validateRuntimeScope(config.Interface, config.ListenPort); err != nil {
			return "", nil, fmt.Errorf("runtime remote %d scope: %w", index, err)
		}
	}
	return stateRoot, labProfiles, nil
}

func loadAdmittedRuntimeMaterial(
	ctx context.Context,
	config RuntimeConfig,
	stateRoot string,
	dependencies runtimeDependencies,
) (runtimeMaterial, error) {
	material, err := dependencies.loadMaterial(ctx, stateRoot)
	if err != nil {
		return runtimeMaterial{}, fmt.Errorf("%w: %v", errProtectedRuntimeCredentials, err)
	}
	if err := validateRuntimeMaterial(material); err != nil {
		return runtimeMaterial{}, fmt.Errorf("%w: %v", errProtectedRuntimeCredentials, err)
	}
	for index := range config.Remotes {
		ski := strings.ToLower(strings.TrimSpace(config.Remotes[index].SKI))
		config.Remotes[index].SKI = ski
		config.Remotes[index].Pretrusted = config.Remotes[index].Pretrusted || material.pretrusted[ski]
		if !runtimeRemoteAdmitted(config.Remotes[index].Pretrusted, config.Remotes[index].Allowlisted) {
			return runtimeMaterial{}, fmt.Errorf("%w: runtime remote %d is not admitted", errProtectedRuntimeCredentials, index)
		}
	}
	return material, nil
}

func (acquisition *runtimeAcquisition) closeFirstTrust() error {
	if acquisition.firstTrust == nil {
		return nil
	}
	return acquisition.firstTrust.Close()
}

func (acquisition *runtimeAcquisition) prepareRawRuntime() error {
	rawTokenIssuer, err := newRuntimeRawReadTokenIssuer(acquisition.material.nodeToken)
	if err != nil {
		return errors.Join(err, acquisition.closeFirstTrust())
	}
	rawTokenIssuer.now = acquisition.dependencies.now
	acquisition.rawTokenIssuer = rawTokenIssuer
	rawRuntimeEpoch, err := rawRuntimeEpochForIdentity(acquisition.material.localSKI)
	if err != nil {
		return errors.Join(err, acquisition.closeFirstTrust())
	}
	acquisition.rawRuntimeEpoch = rawRuntimeEpoch
	rawConnectionGenerations, err := newRawConnectionGenerationStore(acquisition.stateRoot)
	if err != nil {
		return errors.Join(err, acquisition.closeFirstTrust())
	}
	acquisition.rawConnectionGenerations = rawConnectionGenerations
	outgoingAttemptBridge := newFirstTrustOutgoingAttemptBridge(acquisition.firstTrust)
	if outgoingAttemptBridge == nil {
		return errors.Join(errors.New("runtime outgoing-attempt gate is unavailable"), acquisition.closeFirstTrust())
	}
	acquisition.material.outgoingAttemptBridge = outgoingAttemptBridge
	acquisition.outgoingAttemptBridge = outgoingAttemptBridge
	if acquisition.dependencies.newService == nil {
		return errors.Join(errors.New("runtime service dependency is incomplete"), acquisition.closeFirstTrust())
	}
	return nil
}

func (acquisition *runtimeAcquisition) openService(serializer runtimeSnapshotSerializer) error {
	handler, err := newRuntimeServiceHandler(acquisition.config, acquisition.material.localSKI, acquisition.dependencies.now)
	if err != nil {
		return errors.Join(err, acquisition.closeFirstTrust())
	}
	handler.setSnapshotSerializer(serializer)
	reader := newRuntimeServiceReader(handler)
	service, err := acquisition.dependencies.newService(acquisition.config, acquisition.material, reader)
	if err != nil {
		return errors.Join(err, acquisition.closeFirstTrust())
	}
	if service == nil {
		return errors.Join(errors.New("runtime service factory returned nil"), acquisition.closeFirstTrust())
	}
	acquisition.handler = handler
	acquisition.reader = reader
	acquisition.service = service
	acquisition.outgoingAttemptBridge.bindLifecycle(service)
	return nil
}

func (acquisition *runtimeAcquisition) closeRuntime(cause error) error {
	acquisition.service.Shutdown()
	attemptErr := acquisition.outgoingAttemptBridge.shutdown()
	trustErr := acquisition.closeFirstTrust()
	return errors.Join(cause, attemptErr, trustErr)
}

func (acquisition *runtimeAcquisition) bindRawFeatures() error {
	if err := acquisition.service.Setup(); err != nil {
		return acquisition.closeRuntime(fmt.Errorf("setup eebus runtime service: %w", err))
	}
	localDevice, err := provisionRuntimeLocalReadSource(acquisition.service)
	if err != nil {
		return acquisition.closeRuntime(err)
	}
	acquisition.runtimeEpoch = rawRuntimeEpochProvider(acquisition.firstTrust, acquisition.rawRuntimeEpoch)
	acquisition.rawFeatures = newRawFeatureRuntimeBridgeWithMutationProfiles(
		localDevice,
		acquisition.runtimeEpoch,
		acquisition.dependencies.now,
		acquisition.rawTokenIssuer,
		acquisition.rawConnectionGenerations,
		acquisition.labProfiles,
	)
	acquisition.handler.bindRawFeatureRuntime(acquisition.rawFeatures)
	return nil
}

func (acquisition *runtimeAcquisition) subscribeSPINE() error {
	if acquisition.dependencies.subscribeSPINEEvents == nil {
		return acquisition.closeRuntime(errors.New("runtime SPINE event dependency is incomplete"))
	}
	acquisition.handler.activateSPINEEvents(acquisition.service)
	unsubscribeSPINE, err := acquisition.dependencies.subscribeSPINEEvents(acquisition.handler)
	if err != nil {
		acquisition.handler.deactivateSPINEEvents()
		return acquisition.closeRuntime(fmt.Errorf("subscribe to SPINE runtime events: %w", err))
	}
	if unsubscribeSPINE == nil {
		acquisition.handler.deactivateSPINEEvents()
		return acquisition.closeRuntime(errors.New("SPINE runtime event subscription omitted unsubscribe"))
	}
	acquisition.unsubscribeSPINE = unsubscribeSPINE
	return nil
}

func (acquisition *runtimeAcquisition) closeRuntimeWithSPINE(cause error) error {
	acquisition.handler.deactivateSPINEEvents()
	eventErr := acquisition.unsubscribeSPINE()
	acquisition.handler.waitForSPINEEvents()
	return errors.Join(acquisition.closeRuntime(cause), eventErr)
}

func (acquisition *runtimeAcquisition) attachFirstTrust(ctx context.Context) error {
	firstTrust, material := acquisition.firstTrust, acquisition.material
	if firstTrust == nil {
		return nil
	}
	if err := attachRuntimeFirstTrust(ctx, firstTrust, acquisition.config, acquisition.service, acquisition.reader, acquisition.dependencies); err != nil {
		return acquisition.closeRuntimeWithSPINE(err)
	}
	acquisition.outgoingAttemptBridge.bindTLSLifecycle(firstTrust.facade)
	if adminService, ok := acquisition.service.(operatorAdminV1Service); ok {
		acquisition.operatorAdmin = newOperatorAdminV1Bridge(
			firstTrust.coordinator,
			adminService,
			rand.Reader,
			operatorAdminV1BridgeLocalIdentity{ski: material.localSKI, shipID: canonicalRuntimeSHIPID(material.nodeToken)},
		)
	}
	return nil
}

func (acquisition *runtimeAcquisition) openRawMutations() error {
	if acquisition.dependencies.newMutationCoordinator == nil {
		return nil
	}
	runtimeEpoch := acquisition.runtimeEpoch
	mutationEpoch := runtimeEpoch()
	if mutationEpoch == 0 {
		return acquisition.closeRuntimeWithSPINE(errors.New("raw mutation runtime epoch is unavailable"))
	}
	mutationRuntimeEpoch := func() uint64 {
		if runtimeEpoch() != mutationEpoch {
			return 0
		}
		return mutationEpoch
	}
	mutationReferenceKey, err := loadRawMutationReferenceKey(acquisition.stateRoot, mutationEpoch)
	if err != nil {
		return acquisition.closeRuntimeWithSPINE(fmt.Errorf("load raw mutation reference key: %w", err))
	}
	rawFeatures := acquisition.rawFeatures
	rawMutations, terminal := acquisition.dependencies.newMutationCoordinator(
		eebusmutation.CoordinatorConfig{
			StateRoot:        acquisition.stateRoot,
			RuntimeEpoch:     mutationRuntimeEpoch,
			Now:              acquisition.dependencies.now,
			WriterWait:       50 * time.Millisecond,
			RecoveryDeadline: 5 * time.Minute,
			ReferenceKey:     mutationReferenceKey,
			LabProfiles:      cloneRuntimeMutationLabProfiles(acquisition.labProfiles),
		},
		eebusmutation.CoordinatorDependencies{
			Executor:         rawFeatures,
			BindingAuthority: rawFeatures,
			TokenVerifier:    acquisition.rawTokenIssuer,
			Policy:           rawFeatures,
			CancelInFlight:   rawFeatures.cancelInFlight,
		},
	)
	clear(mutationReferenceKey)
	if terminal != nil {
		return acquisition.closeRuntimeWithSPINE(errors.New("initialize raw mutation coordinator"))
	}
	acquisition.rawMutations = rawMutations
	return nil
}

func (acquisition *runtimeAcquisition) closeRawMutations() {
	if acquisition.rawMutations != nil {
		_ = acquisition.rawMutations.Close()
	}
}

func (acquisition *runtimeAcquisition) startBackend() (Backend, error) {
	backend := &serviceBackend{
		service: acquisition.service, handler: acquisition.handler, firstTrust: acquisition.firstTrust,
		outgoingAttempts: acquisition.outgoingAttemptBridge, operatorAdmin: acquisition.operatorAdmin,
		rawFeatures: acquisition.rawFeatures, rawMutations: acquisition.rawMutations,
		unsubscribeSPINE: acquisition.unsubscribeSPINE, closeDone: make(chan struct{}),
	}
	scoped, ok := acquisition.service.(runtimeScopedService)
	if !ok {
		return backend, nil
	}
	if !backend.runtimeStartAuthorized() {
		acquisition.closeRawMutations()
		return nil, acquisition.closeRuntimeWithSPINE(errRuntimeTrustEffectsDenied)
	}
	if err := scoped.StartWithPolicy(); err != nil {
		acquisition.closeRawMutations()
		return nil, acquisition.closeRuntimeWithSPINE(fmt.Errorf("start scoped eebus runtime service: %w", err))
	}
	terminal := scoped.ListenerTerminal()
	if terminal == nil {
		acquisition.closeRawMutations()
		return nil, acquisition.closeRuntimeWithSPINE(errors.New("scoped eebus runtime service omitted listener terminal signal"))
	}
	backend.listenerTerminal = terminal
	backend.serviceStarted = true
	return backend, nil
}

func provisionRuntimeLocalReadSource(
	service runtimeService,
) (spineapi.DeviceLocalInterface, error) {
	if service == nil {
		return nil, &runtimeLocalReadSourceError{reason: "service is nil"}
	}
	local := service.LocalDevice()
	if isNilRawRuntimeValue(local) {
		return nil, &runtimeLocalReadSourceError{reason: "local device is nil or malformed"}
	}
	localAddress := local.Address()
	if localAddress == nil || *localAddress == "" {
		return nil, &runtimeLocalReadSourceError{reason: "local device is nil or malformed"}
	}
	cem := local.EntityForType(spinemodel.EntityTypeTypeCEM)
	if isNilRawRuntimeValue(cem) || cem.EntityType() != spinemodel.EntityTypeTypeCEM {
		return nil, &runtimeLocalReadSourceError{reason: "local CEM entity is missing or malformed"}
	}
	cemAddress := cem.Address()
	if cemAddress == nil || cemAddress.Device == nil ||
		*cemAddress.Device != *localAddress || len(cemAddress.Entity) == 0 {
		return nil, &runtimeLocalReadSourceError{reason: "local CEM entity is missing or malformed"}
	}
	source := cem.GetOrAddFeature(
		spinemodel.FeatureTypeTypeGeneric,
		spinemodel.RoleTypeClient,
	)
	if isNilRawRuntimeValue(source) || source.Type() != spinemodel.FeatureTypeTypeGeneric ||
		source.Role() != spinemodel.RoleTypeClient {
		return nil, &runtimeLocalReadSourceError{reason: "local Generic/client feature is nil or malformed"}
	}
	sourceAddress := source.Address()
	if sourceAddress == nil || sourceAddress.Device == nil || sourceAddress.Feature == nil ||
		*sourceAddress.Device != *localAddress ||
		!runtimeEntityAddressEqual(sourceAddress.Entity, cemAddress.Entity) {
		return nil, &runtimeLocalReadSourceError{reason: "local Generic/client feature is nil or malformed"}
	}
	matches := 0
	returnedSourcePresent := false
	for _, feature := range cem.Features() {
		if isNilRawRuntimeValue(feature) ||
			feature.Type() != spinemodel.FeatureTypeTypeGeneric ||
			feature.Role() != spinemodel.RoleTypeClient {
			continue
		}
		matches++
		if feature.Address() != nil &&
			equalRawFeatureAddress(*feature.Address(), *sourceAddress) {
			returnedSourcePresent = true
		}
	}
	if matches != 1 || !returnedSourcePresent {
		return nil, &runtimeLocalReadSourceError{
			reason: "local CEM must contain exactly one Generic/client feature",
		}
	}
	return local, nil
}

func runtimeEntityAddressEqual(
	left []spinemodel.AddressEntityType,
	right []spinemodel.AddressEntityType,
) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (backend *serviceBackend) Run(ctx context.Context, publish func([]byte)) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if backend.service == nil || backend.handler == nil || publish == nil {
		return errors.New("eebus runtime service backend is incomplete")
	}
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return nil
	}
	if backend.runClaimed {
		backend.mu.Unlock()
		return errors.New("eebus runtime service is already running")
	}
	backend.runClaimed = true
	serviceStarted := backend.serviceStarted
	listenerTerminal := backend.listenerTerminal
	backend.mu.Unlock()
	backend.handler.setPublisher(publish)
	if err := backend.handler.publishCurrent(); err != nil {
		return err
	}
	if !serviceStarted {
		backend.mu.Lock()
		if backend.closed {
			backend.mu.Unlock()
			return nil
		}
		if !backend.runtimeStartAuthorized() {
			backend.mu.Unlock()
			if ctx.Err() != nil {
				return nil
			}
			return errRuntimeTrustEffectsDenied
		}
		backend.serviceStarted = true
		backend.service.Start()
		backend.mu.Unlock()
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-backend.handler.errors:
		return err
	case err, ok := <-listenerTerminal:
		if !ok || err == nil {
			return errors.New("scoped eebus runtime listener terminated")
		}
		return err
	}
}

func (backend *serviceBackend) runtimeStartAuthorized() bool {
	return backend.firstTrust == nil || backend.firstTrust.coordinator != nil && backend.firstTrust.coordinator.runtimeStartAuthorized()
}

func (backend *serviceBackend) FeaturesGet(
	ctx context.Context,
	auth eebusraw.ReadAuthorizationV1,
	request eebusraw.FeaturesGetRequestV1,
) (eebusraw.FeaturesGetDataV1, *eebusraw.ErrorV1) {
	if backend == nil || backend.rawFeatures == nil {
		return eebusraw.FeaturesGetDataV1{}, eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1Disconnected,
			"raw feature runtime is unavailable",
			true,
			eebusraw.SourceLayerV1Runtime,
		)
	}
	return backend.rawFeatures.featuresGet(ctx, auth, request)
}

func (backend *serviceBackend) FeaturesDataGet(
	ctx context.Context,
	auth eebusraw.ReadAuthorizationV1,
	request eebusraw.FeatureDataGetRequestV1,
) (eebusraw.FeatureDataGetDataV1, *eebusraw.ErrorV1) {
	if backend == nil || backend.rawFeatures == nil {
		return eebusraw.FeatureDataGetDataV1{}, eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1Disconnected,
			"raw feature runtime is unavailable",
			true,
			eebusraw.SourceLayerV1Runtime,
		)
	}
	return backend.rawFeatures.featuresDataGet(ctx, auth, request)
}

func (backend *serviceBackend) FeaturesDataSet(
	ctx context.Context,
	auth eebusraw.WriteAuthorizationV1,
	request eebusraw.FeatureDataSetRequestV1,
) (RawMutationOutcomeV1, *eebusraw.ErrorV1) {
	if terminal := eebusraw.ValidateWriteAuthorizationV1(
		auth,
		eebusraw.ToolV1FeaturesDataSet,
	); terminal != nil {
		return RawMutationOutcomeV1{}, terminal
	}
	if terminal := validateRawMutationRequestWithoutToken(request); terminal != nil {
		return RawMutationOutcomeV1{}, terminal
	}
	runtime, tokenTerminal := backend.rawMutationTokenRuntime(ctx, request.ReadToken)
	if tokenTerminal != nil {
		if tokenTerminal.Code == eebusraw.ErrorCodeV1StaleReadToken {
			return RawMutationOutcomeV1{}, eebusraw.NewErrorV1(
				eebusraw.ErrorCodeV1StaleReadToken,
				"raw mutation read token is stale",
				false,
				eebusraw.SourceLayerV1Runtime,
			)
		}
		cloned := tokenTerminal.Clone()
		return RawMutationOutcomeV1{}, &cloned
	}
	coordinator, terminal := backend.mutationCoordinator()
	if terminal != nil {
		return RawMutationOutcomeV1{Runtime: runtime}, terminal
	}
	mutation, terminal := coordinator.FeaturesDataSet(ctx, auth, request)
	return rawMutationOutcome(mutation, runtime), terminal
}

func (backend *serviceBackend) rawMutationTokenRuntime(
	ctx context.Context,
	token string,
) (*eebusraw.RuntimeBindingV1, *eebusraw.ErrorV1) {
	if backend == nil || backend.rawFeatures == nil ||
		backend.rawFeatures.tokenIssuer == nil {
		return nil, nil
	}
	binding, terminal := backend.rawFeatures.tokenIssuer.VerifyReadToken(ctx, token)
	if terminal != nil ||
		binding.Runtime.RuntimeEpoch == 0 ||
		binding.Runtime.ConnectionGeneration == 0 {
		return nil, terminal
	}
	runtime := binding.Runtime
	return &runtime, nil
}

func validateRawMutationRequestWithoutToken(
	request eebusraw.FeatureDataSetRequestV1,
) *eebusraw.ErrorV1 {
	request.ReadToken = strings.Repeat("A", 43)
	return eebusraw.ValidateFeatureDataSetRequestV1(request)
}

func rawMutationOutcome(
	mutation eebusraw.MutationV1,
	runtime *eebusraw.RuntimeBindingV1,
) RawMutationOutcomeV1 {
	if mutation.Runtime.RuntimeEpoch != 0 &&
		mutation.Runtime.ConnectionGeneration != 0 {
		bound := mutation.Runtime
		runtime = &bound
	}
	return RawMutationOutcomeV1{
		Mutation: mutation,
		Runtime:  runtime,
	}
}

func (backend *serviceBackend) mutationCoordinator() (
	*eebusmutation.Coordinator,
	*eebusraw.ErrorV1,
) {
	if backend == nil {
		return nil, rawMutationFacadeError(
			eebusraw.ErrorCodeV1Disconnected,
			true,
		)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.closed || backend.rawMutations == nil {
		return nil, rawMutationFacadeError(eebusraw.ErrorCodeV1Disconnected, true)
	}
	return backend.rawMutations, nil
}

func (backend *serviceBackend) MutationsGet(
	ctx context.Context,
	auth eebusraw.ReadAuthorizationV1,
	request eebusraw.MutationGetRequestV1,
) (RawMutationOutcomeV1, *eebusraw.ErrorV1) {
	coordinator, terminal := backend.mutationCoordinator()
	if terminal != nil {
		return RawMutationOutcomeV1{}, terminal
	}
	mutation, runtime, terminal := coordinator.MutationsGetOutcome(ctx, auth, request)
	return rawMutationOutcome(mutation, runtime), terminal
}

func (backend *serviceBackend) MutationsRollback(
	ctx context.Context,
	auth eebusraw.WriteAuthorizationV1,
	request eebusraw.MutationRollbackRequestV1,
) (RawMutationOutcomeV1, *eebusraw.ErrorV1) {
	coordinator, terminal := backend.mutationCoordinator()
	if terminal != nil {
		return RawMutationOutcomeV1{}, terminal
	}
	mutation, runtime, terminal := coordinator.MutationsRollbackOutcome(ctx, auth, request)
	return rawMutationOutcome(mutation, runtime), terminal
}

func (backend *serviceBackend) Close() error {
	backend.mu.Lock()
	if backend.closed {
		closeDone := backend.closeDone
		backend.mu.Unlock()
		if closeDone != nil {
			<-closeDone
		}
		backend.mu.Lock()
		closeErr := backend.closeErr
		backend.mu.Unlock()
		return closeErr
	}
	backend.closed = true
	coordinator := backend.rawMutations
	operatorAdmin := backend.operatorAdmin
	backend.operatorAdmin = nil
	backend.mu.Unlock()
	if operatorAdmin != nil {
		operatorAdmin.closeOperatorAdminV1Bridge()
	}
	var mutationErr error
	if coordinator != nil {
		if terminal := coordinator.Close(); terminal != nil {
			mutationErr = errors.New("close raw mutation coordinator")
		}
	}
	if backend.handler != nil {
		backend.handler.deactivateSPINEEvents()
	}
	var eventErr error
	if backend.unsubscribeSPINE != nil {
		eventErr = backend.unsubscribeSPINE()
	}
	if backend.handler != nil {
		backend.handler.waitForSPINEEvents()
	}
	if backend.service != nil {
		backend.service.Shutdown()
	}
	var attemptErr error
	if backend.outgoingAttempts != nil {
		attemptErr = backend.outgoingAttempts.shutdown()
	}
	var trustErr error
	if backend.firstTrust != nil {
		trustErr = backend.firstTrust.Close()
	}
	backend.mu.Lock()
	backend.closeErr = errors.Join(mutationErr, eventErr, attemptErr, trustErr)
	closeErr := backend.closeErr
	if backend.closeDone != nil {
		close(backend.closeDone)
	}
	backend.mu.Unlock()
	return closeErr
}

func loadProtectedRuntimeMaterial(ctx context.Context, stateRoot string) (runtimeMaterial, error) {
	return loadNativeProtectedRuntimeMaterial(ctx, stateRoot)
}

func newProtectedTLSCertificate(certificateChain [][]byte, signer crypto.Signer) tls.Certificate {
	return tls.Certificate{
		Certificate:                  certificateChain,
		PrivateKey:                   signer,
		SupportedSignatureAlgorithms: []tls.SignatureScheme{tls.ECDSAWithP256AndSHA256},
	}
}

func newEEBusService(config RuntimeConfig, material runtimeMaterial, reader eebusapi.ServiceReaderInterface) (runtimeService, error) {
	if material.outgoingAttemptBridge == nil {
		material.outgoingAttemptBridge = newFirstTrustOutgoingAttemptBridge(nil)
	}
	configuration, err := eebusapi.NewConfiguration(
		"Project-Helianthus", "Helianthus", "eebusreg", material.nodeToken,
		spinemodel.DeviceTypeTypeEnergyManagementSystem,
		[]spinemodel.EntityTypeType{spinemodel.EntityTypeTypeCEM},
		config.ListenPort, material.certificate, 4*time.Second,
	)
	if err != nil {
		return nil, err
	}
	configuration.SetAlternateIdentifier(canonicalRuntimeSHIPID(material.nodeToken))
	configuration.SetAlternateMdnsServiceName("Helianthus EnergyManagementSystem eebusreg")
	configuration.SetInterfaces([]string{config.Interface})
	options := eebusservicebridge.ServiceOptions{
		ListenerPolicy: &eebusservicebridge.ListenerPolicy{
			ListenAddress:    config.ListenAddress,
			DiscoveryEnabled: config.DiscoveryEnabled,
		},
		OutgoingAttemptBridge: &eebusservicebridge.OutgoingAttemptBridgeConfiguration{
			Gate: material.outgoingAttemptBridge,
			Sink: material.outgoingAttemptBridge,
		},
	}
	candidate := eebusservicebridge.NewServiceWithOptions(configuration, reader, options)
	if candidate == nil {
		return nil, errors.New("released scoped service construction failed")
	}
	return candidate, nil
}

func validateRuntimeMaterial(material runtimeMaterial) error {
	material.localSKI = strings.ToLower(strings.TrimSpace(material.localSKI))
	if !validRuntimeSKI(material.localSKI) {
		return errors.New("protected local SKI must contain 40 hexadecimal characters")
	}
	if len(material.certificate.Certificate) == 0 || material.certificate.PrivateKey == nil {
		return errors.New("protected certificate and signer are required")
	}
	certificate, err := x509.ParseCertificate(material.certificate.Certificate[0])
	if err != nil {
		return errors.New("protected certificate is invalid")
	}
	certificateSKI, err := shipcert.SkiFromCertificate(certificate)
	if err != nil || certificateSKI != material.localSKI {
		return errors.New("protected local SKI does not match the certificate")
	}
	if !validRuntimeNodeToken(material.nodeToken) {
		return errors.New("protected node token must contain 32 lowercase hexadecimal characters")
	}
	return nil
}

func canonicalRuntimeNodeToken(storeInstance [sha256.Size]byte) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte("helianthus-eebus-node-v1\x00"))
	_, _ = digest.Write(storeInstance[:])
	return hex.EncodeToString(digest.Sum(nil)[:16])
}

func canonicalRuntimeSHIPID(nodeToken string) string {
	return "HLS-" + nodeToken
}

func validRuntimeNodeToken(value string) bool {
	if len(value) != 32 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validRuntimeSKI(value string) bool {
	if len(value) != 40 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validateRuntimeScope(interfaceName string, port int) error {
	if runtimeScopeWildcard(interfaceName) {
		return errors.New("runtime interface must be explicit")
	}
	if port < 1 || port > 65535 {
		return errors.New("runtime listen port must be between 1 and 65535")
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
