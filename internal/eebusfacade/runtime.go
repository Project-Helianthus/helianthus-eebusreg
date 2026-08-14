package eebusfacade

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"sort"
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

type runtimeDeviceProvider interface {
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

type runtimeFeatureObservation struct {
	ID             string
	DeviceAddress  string
	EntityAddress  string
	FeatureAddress string
	Type           string
	Role           string
	Description    *string
}

type runtimeEntityObservation struct {
	ID            string
	DeviceAddress string
	EntityAddress string
	Type          string
	Description   *string
	Features      []runtimeFeatureObservation
}

type runtimeUseCaseObservation struct {
	ID                  string
	ContextAddress      string
	Name                string
	Actor               string
	ResolvedRole        *string
	Scenarios           []string
	Version             *string
	Availability        *bool
	DocumentSubrevision *string
}

type runtimeDeviceObservation struct {
	ID          string
	SKI         string
	SHIPID      string
	Address     string
	Type        string
	Description *string
	Metadata    map[string]string
	Opaque      []runtimeOpaquePayload
	Entities    []runtimeEntityObservation
	UseCaseIDs  []string
	UseCases    []runtimeUseCaseObservation
}

type runtimeGraphObservation struct {
	RuntimeID         string
	LocalSKI          string
	RemoteSKI         string
	SessionID         string
	SessionState      string
	PairingState      string
	Visible           bool
	Paired            bool
	Since             time.Time
	ServiceIDs        []string
	Devices           []runtimeDeviceObservation
	ShipID            string
	ServiceName       string
	ServiceIdentifier string
	ServiceBrand      string
	ServiceType       string
	ServiceModel      string
	SessionIndex      uint64
	TrustDegradation  string
}

type runtimeObservationReducer struct {
	mu sync.RWMutex

	initialized bool
	runtimeID   string
	localSKI    string
	remotes     map[string]runtimeGraphObservation
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

func acquireRuntime(ctx context.Context, config RuntimeConfig, dependencies runtimeDependencies) (Backend, error) {
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
	if len(config.Remotes) == 0 && !config.ListenAddress.IsValid() {
		return nil, errors.New("at least one runtime remote is required")
	}
	if config.ListenPort < 1 || config.ListenPort > 65535 {
		return nil, errors.New("runtime listen port must be between 1 and 65535")
	}
	if dependencies.loadMaterial == nil || dependencies.now == nil {
		return nil, errors.New("runtime dependencies are incomplete")
	}
	labProfiles := mutationLabProfilesForRuntime(config.LabProfiles)

	seen := make(map[string]struct{}, len(config.Remotes))
	for index, remote := range config.Remotes {
		ski := strings.ToLower(strings.TrimSpace(remote.SKI))
		if !validRuntimeSKI(ski) {
			return nil, fmt.Errorf("runtime remote %d SKI must contain 40 hexadecimal characters", index)
		}
		if _, exists := seen[ski]; exists {
			return nil, fmt.Errorf("runtime remote %d duplicates remote SKI", index)
		}
		seen[ski] = struct{}{}
		if err := validateRuntimeScope(config.Interface, config.ListenPort); err != nil {
			return nil, fmt.Errorf("runtime remote %d scope: %w", index, err)
		}
	}

	material, err := dependencies.loadMaterial(ctx, stateRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errProtectedRuntimeCredentials, err)
	}
	if err := validateRuntimeMaterial(material); err != nil {
		return nil, fmt.Errorf("%w: %v", errProtectedRuntimeCredentials, err)
	}
	for index := range config.Remotes {
		ski := strings.ToLower(strings.TrimSpace(config.Remotes[index].SKI))
		config.Remotes[index].SKI = ski
		config.Remotes[index].Pretrusted = config.Remotes[index].Pretrusted || material.pretrusted[ski]
		if !runtimeRemoteAdmitted(config.Remotes[index].Pretrusted, config.Remotes[index].Allowlisted) {
			return nil, fmt.Errorf("%w: runtime remote %d is not admitted", errProtectedRuntimeCredentials, index)
		}
	}
	firstTrust, err := classifyRuntimeFirstTrust(ctx, config, material, dependencies)
	if err != nil {
		return nil, err
	}
	closeFirstTrust := func() error {
		if firstTrust == nil {
			return nil
		}
		return firstTrust.Close()
	}
	rawTokenIssuer, err := newRuntimeRawReadTokenIssuer(material.nodeToken)
	if err != nil {
		return nil, errors.Join(err, closeFirstTrust())
	}
	rawTokenIssuer.now = dependencies.now
	rawRuntimeEpoch, err := rawRuntimeEpochForIdentity(material.localSKI)
	if err != nil {
		return nil, errors.Join(err, closeFirstTrust())
	}
	rawConnectionGenerations, err := newRawConnectionGenerationStore(stateRoot)
	if err != nil {
		return nil, errors.Join(err, closeFirstTrust())
	}
	outgoingAttemptBridge := newFirstTrustOutgoingAttemptBridge(firstTrust)
	if outgoingAttemptBridge == nil {
		return nil, errors.Join(errors.New("runtime outgoing-attempt gate is unavailable"), closeFirstTrust())
	}
	material.outgoingAttemptBridge = outgoingAttemptBridge
	if dependencies.newService == nil {
		return nil, errors.Join(errors.New("runtime service dependency is incomplete"), closeFirstTrust())
	}

	handler, err := newRuntimeServiceHandler(config, material.localSKI, dependencies.now)
	if err != nil {
		return nil, errors.Join(err, closeFirstTrust())
	}
	reader := newRuntimeServiceReader(handler)
	service, err := dependencies.newService(config, material, reader)
	if err != nil {
		return nil, errors.Join(err, closeFirstTrust())
	}
	if service == nil {
		return nil, errors.Join(errors.New("runtime service factory returned nil"), closeFirstTrust())
	}
	outgoingAttemptBridge.bindLifecycle(service)
	closeRuntime := func(cause error) error {
		service.Shutdown()
		attemptErr := outgoingAttemptBridge.shutdown()
		trustErr := closeFirstTrust()
		return errors.Join(cause, attemptErr, trustErr)
	}
	if err := service.Setup(); err != nil {
		return nil, closeRuntime(fmt.Errorf("setup eebus runtime service: %w", err))
	}
	localDevice, err := provisionRuntimeLocalReadSource(service)
	if err != nil {
		return nil, closeRuntime(err)
	}
	runtimeEpoch := rawRuntimeEpochProvider(firstTrust, rawRuntimeEpoch)
	rawFeatures := newRawFeatureRuntimeBridgeWithMutationProfiles(
		localDevice,
		runtimeEpoch,
		dependencies.now,
		rawTokenIssuer,
		rawConnectionGenerations,
		labProfiles,
	)
	handler.bindRawFeatureRuntime(rawFeatures)
	if dependencies.subscribeSPINEEvents == nil {
		return nil, closeRuntime(errors.New("runtime SPINE event dependency is incomplete"))
	}
	handler.activateSPINEEvents(service)
	unsubscribeSPINE, err := dependencies.subscribeSPINEEvents(handler)
	if err != nil {
		handler.deactivateSPINEEvents()
		return nil, closeRuntime(fmt.Errorf("subscribe to SPINE runtime events: %w", err))
	}
	if unsubscribeSPINE == nil {
		handler.deactivateSPINEEvents()
		return nil, closeRuntime(errors.New("SPINE runtime event subscription omitted unsubscribe"))
	}
	closeRuntimeWithSPINE := func(cause error) error {
		handler.deactivateSPINEEvents()
		eventErr := unsubscribeSPINE()
		handler.waitForSPINEEvents()
		return errors.Join(closeRuntime(cause), eventErr)
	}
	if firstTrust != nil {
		if err := attachRuntimeFirstTrust(ctx, firstTrust, config, service, reader, dependencies); err != nil {
			return nil, closeRuntimeWithSPINE(err)
		}
		outgoingAttemptBridge.bindTLSLifecycle(firstTrust.facade)
	}
	var operatorAdmin *operatorAdminV1Bridge
	if firstTrust != nil {
		if adminService, ok := service.(operatorAdminV1Service); ok {
			operatorAdmin = newOperatorAdminV1Bridge(firstTrust.coordinator, adminService, rand.Reader)
		}
	}
	var rawMutations *eebusmutation.Coordinator
	if dependencies.newMutationCoordinator != nil {
		mutationEpoch := runtimeEpoch()
		if mutationEpoch == 0 {
			return nil, closeRuntimeWithSPINE(errors.New("raw mutation runtime epoch is unavailable"))
		}
		mutationRuntimeEpoch := func() uint64 {
			if runtimeEpoch() != mutationEpoch {
				return 0
			}
			return mutationEpoch
		}
		mutationReferenceKey, err := loadRawMutationReferenceKey(stateRoot, mutationEpoch)
		if err != nil {
			return nil, closeRuntimeWithSPINE(fmt.Errorf(
				"load raw mutation reference key: %w",
				err,
			))
		}
		var terminal *eebusraw.ErrorV1
		rawMutations, terminal = dependencies.newMutationCoordinator(
			eebusmutation.CoordinatorConfig{
				StateRoot:        stateRoot,
				RuntimeEpoch:     mutationRuntimeEpoch,
				Now:              dependencies.now,
				WriterWait:       50 * time.Millisecond,
				RecoveryDeadline: 5 * time.Minute,
				ReferenceKey:     mutationReferenceKey,
				LabProfiles:      cloneRuntimeMutationLabProfiles(labProfiles),
			},
			eebusmutation.CoordinatorDependencies{
				Executor:         rawFeatures,
				BindingAuthority: rawFeatures,
				TokenVerifier:    rawTokenIssuer,
				Policy:           rawFeatures,
				CancelInFlight:   rawFeatures.cancelInFlight,
			},
		)
		clear(mutationReferenceKey)
		if terminal != nil {
			return nil, closeRuntimeWithSPINE(errors.New("initialize raw mutation coordinator"))
		}
	}
	backend := &serviceBackend{
		service: service, handler: handler, firstTrust: firstTrust, outgoingAttempts: outgoingAttemptBridge,
		operatorAdmin: operatorAdmin, rawFeatures: rawFeatures, rawMutations: rawMutations, unsubscribeSPINE: unsubscribeSPINE,
		closeDone: make(chan struct{}),
	}
	if scoped, ok := service.(runtimeScopedService); ok {
		if !backend.runtimeStartAuthorized() {
			if rawMutations != nil {
				_ = rawMutations.Close()
			}
			return nil, closeRuntimeWithSPINE(errRuntimeTrustEffectsDenied)
		}
		if err := scoped.StartWithPolicy(); err != nil {
			if rawMutations != nil {
				_ = rawMutations.Close()
			}
			return nil, closeRuntimeWithSPINE(fmt.Errorf("start scoped eebus runtime service: %w", err))
		}
		terminal := scoped.ListenerTerminal()
		if terminal == nil {
			if rawMutations != nil {
				_ = rawMutations.Close()
			}
			return nil, closeRuntimeWithSPINE(errors.New("scoped eebus runtime service omitted listener terminal signal"))
		}
		backend.listenerTerminal = terminal
		backend.serviceStarted = true
	}
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
	configuration.SetAlternateIdentifier("HLS-" + material.nodeToken)
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

type runtimeServiceHandler struct {
	mu        sync.Mutex
	publishMu sync.Mutex
	spineWG   sync.WaitGroup
	spineWork sync.WaitGroup

	runtimeID                 string
	localSKI                  string
	reducer                   *runtimeObservationReducer
	observations              map[string]runtimeGraphObservation
	projectionCapture         func() trustAdminProjection
	projectionLivenessAllowed func(string) bool
	projectionRemotes         []string
	policyRemotes             []string
	runtimeRevision           uint64
	publishedRuntimeRevision  uint64
	publishedTrustRevision    uint64
	now                       func() time.Time
	publish                   func([]byte)
	errors                    chan error
	spineService              runtimeService
	spineEventsActive         bool
	spineGeneration           uint64
	spineCancel               context.CancelFunc
	spineWake                 chan struct{}
	spinePending              map[string]runtimeSPINERefresh
	rawFeatures               *rawFeatureRuntimeBridge
}

func newRuntimeServiceHandler(config RuntimeConfig, localSKI string, now func() time.Time) (*runtimeServiceHandler, error) {
	localSKI = strings.ToLower(strings.TrimSpace(localSKI))
	if !validRuntimeSKI(localSKI) {
		return nil, errors.New("runtime service local SKI is invalid")
	}
	if now == nil {
		return nil, errors.New("runtime service clock is required")
	}
	handler := &runtimeServiceHandler{
		runtimeID:    "runtime:" + localSKI,
		localSKI:     localSKI,
		reducer:      newRuntimeObservationReducer(),
		observations: make(map[string]runtimeGraphObservation, len(config.Remotes)),
		now:          now,
		errors:       make(chan error, 1),
	}
	for _, remote := range config.Remotes {
		handler.policyRemotes = append(handler.policyRemotes, strings.ToLower(strings.TrimSpace(remote.SKI)))
	}
	sort.Strings(handler.policyRemotes)
	return handler, nil
}

func (handler *runtimeServiceHandler) setPublisher(publish func([]byte)) {
	handler.mu.Lock()
	handler.publish = publish
	handler.mu.Unlock()
}

func (handler *runtimeServiceHandler) bindRawFeatureRuntime(bridge *rawFeatureRuntimeBridge) {
	handler.mu.Lock()
	handler.rawFeatures = bridge
	handler.mu.Unlock()
}

func (handler *runtimeServiceHandler) RemoteSKIConnected(service eebusapi.ServiceInterface, ski string) {
	ski = strings.ToLower(strings.TrimSpace(ski))
	if !handler.remoteLivenessAllowed(ski) {
		return
	}
	devices, err := runtimeDevicesForRemote(service, ski)
	if err != nil {
		handler.report(err)
		return
	}
	generation, allocationErr := handler.allocateRawFeatureConnectionGeneration(ski)
	if allocationErr != nil {
		handler.report(allocationErr)
		return
	}
	if generation == 0 {
		handler.report(errors.New("runtime connection generation is exhausted"))
		return
	}
	handler.updateRemote(ski, true, func(observation *runtimeGraphObservation) {
		if len(observation.ServiceIDs) == 0 {
			observation.ServiceIDs = []string{"service:" + ski}
		}
		observation.SessionIndex = generation
		observation.ShipID = ""
		observation.SessionID = runtimeSessionIdentity(*observation)
		observation.SessionState = "connected"
		observation.Visible = true
		observation.Since = handler.timestamp()
		merged, mergeErr := mergeRuntimeDeviceCollections(observation.Devices, devices)
		if mergeErr != nil {
			handler.report(mergeErr)
			return
		}
		observation.Devices = merged
	})
	handler.refreshRawFeatureRemote(ski)
}

func (handler *runtimeServiceHandler) allocateRawFeatureConnectionGeneration(ski string) (uint64, error) {
	handler.mu.Lock()
	bridge := handler.rawFeatures
	observation := handler.observations[ski]
	handler.mu.Unlock()
	if observation.SessionIndex == ^uint64(0) {
		return 0, errors.New("runtime connection generation is exhausted")
	}
	candidate := observation.SessionIndex + 1
	if bridge == nil {
		return candidate, nil
	}
	generation, err := bridge.allocateConnectionGeneration(ski, candidate)
	if err != nil {
		return 0, err
	}
	return generation, nil
}

func (handler *runtimeServiceHandler) RemoteSKIDisconnected(_ eebusapi.ServiceInterface, ski string) {
	ski = strings.ToLower(strings.TrimSpace(ski))
	if !handler.remoteLivenessAllowed(ski) {
		return
	}
	handler.updateRemote(ski, false, func(observation *runtimeGraphObservation) {
		if observation.SessionID == "" {
			return
		}
		observation.SessionState = "disconnected"
		observation.Since = handler.timestamp()
	})
	handler.retireRawFeatureRemote(ski)
}

func (handler *runtimeServiceHandler) VisibleRemoteServicesUpdated(_ eebusapi.ServiceInterface, entries []shipapi.RemoteService) {
	visible := make(map[string]shipapi.RemoteService, len(entries))
	for _, entry := range entries {
		ski := strings.ToLower(strings.TrimSpace(entry.Ski))
		if validRuntimeSKI(ski) {
			entry.Ski = ski
			visible[ski] = entry
		}
	}

	handler.mu.Lock()
	changed := false
	for ski, entry := range visible {
		if !handler.remoteLivenessAllowedLocked(ski) {
			continue
		}
		observation, exists := handler.observations[ski]
		if !exists {
			observation = handler.newRemoteObservation(ski)
			observation.Visible = true
			observation.ServiceIDs = []string{"service:" + ski}
		}
		before := observation
		mergeRuntimeRemoteService(&observation, entry)
		observation.Visible = true
		if exists && runtimeServiceObservationEqual(before, observation) {
			continue
		}
		observation.Since = handler.timestamp()
		if err := handler.reducer.Replace(observation); err != nil {
			handler.mu.Unlock()
			handler.report(err)
			return
		}
		handler.observations[ski] = observation
		handler.runtimeRevision++
		changed = true
	}
	for ski, observation := range handler.observations {
		if !handler.remoteLivenessAllowedLocked(ski) {
			continue
		}
		_, isVisible := visible[ski]
		if observation.Visible == isVisible {
			continue
		}
		observation.Visible = isVisible
		observation.Since = handler.timestamp()
		if err := handler.reducer.Replace(observation); err != nil {
			handler.mu.Unlock()
			handler.report(err)
			return
		}
		handler.observations[ski] = observation
		handler.runtimeRevision++
		changed = true
	}
	handler.mu.Unlock()
	if changed {
		handler.publishOrReport()
	}
}

func mergeRuntimeRemoteService(observation *runtimeGraphObservation, service shipapi.RemoteService) {
	if value := strings.TrimSpace(service.Name); value != "" {
		observation.ServiceName = value
	}
	if value := strings.TrimSpace(service.Identifier); value != "" {
		observation.ServiceIdentifier = value
	}
	if value := strings.TrimSpace(service.Brand); value != "" {
		observation.ServiceBrand = value
	}
	if value := strings.TrimSpace(service.Type); value != "" {
		observation.ServiceType = value
	}
	if value := strings.TrimSpace(service.Model); value != "" {
		observation.ServiceModel = value
	}
}

func runtimeServiceObservationEqual(left, right runtimeGraphObservation) bool {
	return left.Visible == right.Visible &&
		left.ServiceName == right.ServiceName &&
		left.ServiceIdentifier == right.ServiceIdentifier &&
		left.ServiceBrand == right.ServiceBrand &&
		left.ServiceType == right.ServiceType &&
		left.ServiceModel == right.ServiceModel
}

func (handler *runtimeServiceHandler) ServiceShipIDUpdate(ski string, shipID string) {
	ski = strings.ToLower(strings.TrimSpace(ski))
	if !handler.remoteLivenessAllowed(ski) {
		return
	}
	shipID = strings.TrimSpace(shipID)
	if shipID == "" {
		return
	}
	handler.updateRemote(ski, false, func(observation *runtimeGraphObservation) {
		observation.ShipID = shipID
		if observation.SessionIndex != 0 {
			observation.SessionID = runtimeSessionIdentity(*observation)
		}
		observation.Since = handler.timestamp()
	})
	handler.refreshRawFeatureRemote(ski)
}

func (handler *runtimeServiceHandler) refreshRawFeatureRemote(ski string) {
	handler.mu.Lock()
	bridge := handler.rawFeatures
	service := handler.spineService
	observation, ok := handler.observations[ski]
	handler.mu.Unlock()
	if bridge == nil || service == nil || !ok ||
		observation.SessionState != "connected" ||
		observation.ShipID == "" ||
		observation.SessionIndex == 0 ||
		service.LocalDevice() == nil {
		return
	}
	remote := service.LocalDevice().RemoteDeviceForSki(ski)
	if isNilRawRuntimeValue(remote) || remote.Address() == nil {
		return
	}
	err := bridge.refreshRemote(ski, observation.ShipID, observation.SessionIndex, remote)
	if errors.Is(err, errRawRemoteNotAdmitted) {
		err = bridge.admitRemote(ski, observation.ShipID, observation.SessionIndex, remote)
	}
	if err != nil {
		handler.report(err)
	}
}

func (handler *runtimeServiceHandler) retireRawFeatureRemote(ski string) {
	handler.mu.Lock()
	bridge := handler.rawFeatures
	observation, ok := handler.observations[ski]
	handler.mu.Unlock()
	if bridge != nil && ok && observation.SessionIndex > 0 {
		bridge.retireRemote(ski, observation.SessionIndex)
	}
}

func runtimeSessionIdentity(observation runtimeGraphObservation) string {
	seed := observation.RemoteSKI
	if observation.ShipID != "" {
		seed = observation.ShipID
	}
	return fmt.Sprintf("session:%s:%d", seed, observation.SessionIndex)
}

func (handler *runtimeServiceHandler) ServicePairingDetailUpdate(ski string, detail *shipapi.ConnectionStateDetail) {
	ski = strings.ToLower(strings.TrimSpace(ski))
	if !handler.remoteLivenessAllowed(ski) {
		return
	}
	sessionState := ""
	if detail != nil {
		switch detail.State() {
		case shipapi.ConnectionStateRemoteDeniedTrust:
			sessionState = "degraded"
		case shipapi.ConnectionStateError:
			sessionState = "degraded"
		}
	}
	handler.updateRemote(ski, false, func(observation *runtimeGraphObservation) {
		if sessionState == "degraded" && observation.SessionID != "" {
			observation.SessionState = sessionState
		}
		observation.Since = handler.timestamp()
	})
}

func (handler *runtimeServiceHandler) updateRemote(ski string, create bool, update func(*runtimeGraphObservation)) {
	handler.mu.Lock()
	observation, ok := handler.observations[ski]
	if !ok {
		if !create {
			handler.mu.Unlock()
			return
		}
		observation = handler.newRemoteObservation(ski)
	}
	update(&observation)
	if err := handler.reducer.Replace(observation); err != nil {
		handler.mu.Unlock()
		handler.report(err)
		return
	}
	handler.observations[ski] = observation
	handler.runtimeRevision++
	handler.mu.Unlock()
	handler.publishOrReport()
}

func (handler *runtimeServiceHandler) newRemoteObservation(ski string) runtimeGraphObservation {
	return runtimeGraphObservation{
		RuntimeID: handler.runtimeID,
		LocalSKI:  handler.localSKI,
		RemoteSKI: ski,
		Since:     handler.timestamp(),
	}
}

func (handler *runtimeServiceHandler) publishOrReport() {
	if err := handler.publishCurrent(); err != nil {
		handler.report(err)
	}
}

func (handler *runtimeServiceHandler) publishCurrent() error {
	handler.mu.Lock()
	capture := handler.projectionCapture
	revision := handler.runtimeRevision
	graph := handler.reducer.Snapshot()
	handler.mu.Unlock()
	if capture != nil {
		return handler.publishTrustAdminProjection(capture())
	}
	return handler.publishRuntimeGraphAtRevision(graph, revision)
}

func (handler *runtimeServiceHandler) publishTrustAdminProjection(projection trustAdminProjection) error {
	graph, remotes, runtimeRevision := handler.runtimeGraphAndProjectionRemotes()
	handler.publishMu.Lock()
	defer handler.publishMu.Unlock()
	if projection.revision < handler.publishedTrustRevision ||
		runtimeRevision < handler.publishedRuntimeRevision {
		return nil
	}
	applyTrustAdminProjection(graph, remotes, projection)
	if err := handler.publishRuntimeGraph(graph); err != nil {
		return err
	}
	handler.publishedRuntimeRevision = runtimeRevision
	handler.publishedTrustRevision = projection.revision
	return nil
}

func (handler *runtimeServiceHandler) remoteLivenessAllowed(ski string) bool {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	return handler.remoteLivenessAllowedLocked(ski)
}

func (handler *runtimeServiceHandler) remoteLivenessAllowedLocked(ski string) bool {
	return handler.projectionLivenessAllowed == nil || handler.projectionLivenessAllowed(ski)
}

func (handler *runtimeServiceHandler) publishRuntimeGraph(graph []runtimeGraphObservation) error {
	handler.mu.Lock()
	publish := handler.publish
	handler.mu.Unlock()
	if publish == nil {
		return nil
	}
	payload, err := marshalRuntimeSnapshotWithIdentity(handler.runtimeID, handler.localSKI, graph, handler.timestamp())
	if err != nil {
		return err
	}
	publish(payload)
	return nil
}

func (handler *runtimeServiceHandler) publishRuntimeGraphAtRevision(graph []runtimeGraphObservation, revision uint64) error {
	handler.publishMu.Lock()
	defer handler.publishMu.Unlock()
	if revision < handler.publishedRuntimeRevision {
		return nil
	}
	if err := handler.publishRuntimeGraph(graph); err != nil {
		return err
	}
	handler.publishedRuntimeRevision = revision
	return nil
}

func (handler *runtimeServiceHandler) runtimeGraphAndProjectionRemotes() ([]runtimeGraphObservation, []string, uint64) {
	handler.mu.Lock()
	remotes := append([]string(nil), handler.projectionRemotes...)
	revision := handler.runtimeRevision
	graph := handler.reducer.Snapshot()
	handler.mu.Unlock()
	return graph, remotes, revision
}

func (handler *runtimeServiceHandler) report(err error) {
	if err == nil {
		return
	}
	select {
	case handler.errors <- err:
	default:
	}
}

func (handler *runtimeServiceHandler) timestamp() time.Time {
	value := handler.now().UTC()
	if value.IsZero() {
		return time.Unix(0, 0).UTC()
	}
	return value
}

func runtimeDevicesForRemote(service runtimeDeviceProvider, ski string) ([]runtimeDeviceObservation, error) {
	if service == nil || service.LocalDevice() == nil {
		return nil, nil
	}
	remote := service.LocalDevice().RemoteDeviceForSki(ski)
	return runtimeDevicesForRemoteDevice(remote, ski)
}

func runtimeDevicesForRemoteDevice(remote spineapi.DeviceRemoteInterface, ski string) ([]runtimeDeviceObservation, error) {
	if remote == nil {
		return nil, nil
	}
	address := remote.Address()
	deviceType := remote.DeviceType()
	if address == nil || deviceType == nil {
		return nil, nil
	}
	deviceID, err := runtimeIdentity("device", ski, remote.Address())
	if err != nil {
		return nil, err
	}
	device := runtimeDeviceObservation{
		ID: deviceID, SKI: ski, Address: string(*address), Type: string(*deviceType),
	}
	destination := remote.DestinationData()
	if description := destination.DeviceDescription; description != nil {
		device.Description = cloneRuntimeDescription(description.Description)
		device.Metadata = runtimeDeviceMetadata(remote.FeatureSet(), description)
		value, err := detachedRuntimeJSONValue(destination)
		if err != nil {
			return nil, err
		}
		device.Opaque = []runtimeOpaquePayload{{
			Path: "/devices/" + device.Address + "/destination_data", Source: "spine.detailed-discovery", Value: value,
		}}
	} else if featureSet := remote.FeatureSet(); featureSet != nil {
		device.Metadata = map[string]string{"network_feature_set": string(*featureSet)}
	}
	rolesByAddress := make(map[string]string)
	for _, entity := range remote.Entities() {
		if entity == nil {
			continue
		}
		entityAddress := entity.Address()
		entityType := entity.EntityType()
		if entityAddress == nil || entityType == "" {
			continue
		}
		entityID, err := runtimeIdentity("entity", ski, entityAddress.String())
		if err != nil {
			return nil, err
		}
		entityObservation := runtimeEntityObservation{
			ID: entityID, DeviceAddress: device.Address, EntityAddress: entityAddress.String(),
			Type: string(entityType), Description: cloneRuntimeDescription(entity.Description()),
		}
		for _, feature := range entity.Features() {
			if feature == nil {
				continue
			}
			featureAddress := feature.Address()
			featureType := feature.Type()
			if featureAddress == nil || featureType == "" {
				continue
			}
			featureID, err := runtimeIdentity("feature", ski, featureAddress.String())
			if err != nil {
				return nil, err
			}
			role := strings.ToLower(string(feature.Role()))
			if role == "" {
				role = ""
			}
			rolesByAddress[featureAddress.String()] = role
			entityObservation.Features = append(entityObservation.Features, runtimeFeatureObservation{
				ID: featureID, DeviceAddress: device.Address, EntityAddress: entityObservation.EntityAddress,
				FeatureAddress: featureAddress.String(), Type: string(featureType), Role: role,
				Description: cloneRuntimeDescription(feature.Description()),
			})
		}
		device.Entities = append(device.Entities, entityObservation)
	}
	for _, information := range remote.UseCases() {
		if information.Address == nil || information.Actor == nil {
			continue
		}
		contextAddress := information.Address.String()
		actor := string(*information.Actor)
		for _, support := range information.UseCaseSupport {
			if support.UseCaseName == nil {
				continue
			}
			useCaseID, err := runtimeIdentity(
				"usecase", ski, contextAddress, actor, string(*support.UseCaseName),
				cloneRuntimeSpecificationVersion(support.UseCaseVersion),
				cloneRuntimeString(support.UseCaseDocumentSubRevision),
			)
			if err != nil {
				return nil, err
			}
			scenarios := make([]string, len(support.ScenarioSupport))
			for scenarioIndex, scenario := range support.ScenarioSupport {
				scenarios[scenarioIndex] = fmt.Sprint(scenario)
			}
			var resolvedRole *string
			if role, ok := rolesByAddress[contextAddress]; ok {
				resolvedRole = runtimeStringPointer(role)
			}
			observation := runtimeUseCaseObservation{
				ID: useCaseID, ContextAddress: contextAddress, Name: string(*support.UseCaseName),
				Actor: actor, ResolvedRole: resolvedRole, Scenarios: scenarios,
				Version:             cloneRuntimeSpecificationVersion(support.UseCaseVersion),
				Availability:        cloneRuntimeBool(support.UseCaseAvailable),
				DocumentSubrevision: cloneRuntimeString(support.UseCaseDocumentSubRevision),
			}
			device.UseCaseIDs = append(device.UseCaseIDs, useCaseID)
			device.UseCases = append(device.UseCases, observation)
		}
	}
	return []runtimeDeviceObservation{device}, nil
}

func runtimeDeviceMetadata(
	featureSet *spinemodel.NetworkManagementFeatureSetType,
	description *spinemodel.NetworkManagementDeviceDescriptionDataType,
) map[string]string {
	result := make(map[string]string)
	if featureSet != nil {
		result["network_feature_set"] = string(*featureSet)
	}
	if description.NetworkFeatureSet != nil {
		result["network_feature_set"] = string(*description.NetworkFeatureSet)
	}
	if description.NativeSetup != nil {
		result["native_setup"] = string(*description.NativeSetup)
	}
	if description.TechnologyAddress != nil {
		result["technology_address"] = string(*description.TechnologyAddress)
	}
	if description.CommunicationsTechnologyInformation != nil {
		result["communications_technology_information"] = string(*description.CommunicationsTechnologyInformation)
	}
	if description.LastStateChange != nil {
		result["last_state_change"] = string(*description.LastStateChange)
	}
	if description.MinimumTrustLevel != nil {
		result["minimum_trust_level"] = string(*description.MinimumTrustLevel)
	}
	if description.Label != nil {
		result["label"] = string(*description.Label)
	}
	if description.NetworkManagementResponsibleAddress != nil {
		result["network_management_responsible_address"] = description.NetworkManagementResponsibleAddress.String()
	}
	return result
}

func detachedRuntimeJSONValue(value any) (any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, errors.New("encode detailed discovery value")
	}
	var detached any
	if err := json.Unmarshal(encoded, &detached); err != nil {
		return nil, errors.New("decode detailed discovery value")
	}
	return detached, nil
}

func cloneRuntimeDescription(value *spinemodel.DescriptionType) *string {
	if value == nil {
		return nil
	}
	result := string(*value)
	return &result
}

func cloneRuntimeSpecificationVersion(value *spinemodel.SpecificationVersionType) *string {
	if value == nil {
		return nil
	}
	result := string(*value)
	return &result
}

func cloneRuntimeString(value *string) *string {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func runtimeStringPointer(value string) *string {
	return &value
}

func cloneRuntimeBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func runtimeIdentity(kind string, values ...any) (string, error) {
	payload, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("encode runtime %s identity: %w", kind, err)
	}
	return kind + ":" + string(payload), nil
}

type runtimeSnapshotPayload struct {
	Meta     runtimeSnapshotMetaPayload `json:"meta"`
	Status   runtimeStatusPayload       `json:"status"`
	Pairing  []runtimePairingPayload    `json:"pairing"`
	Services []runtimeServicePayload    `json:"services"`
	Sessions []runtimeSessionPayload    `json:"sessions"`
	Devices  []runtimeDevicePayload     `json:"devices"`
	Entities []runtimeEntityPayload     `json:"entities"`
	Features []runtimeFeaturePayload    `json:"features"`
	UseCases []runtimeUseCasePayload    `json:"usecases"`
	Opaque   []runtimeOpaquePayload     `json:"opaque"`
}

type runtimeSnapshotMetaPayload struct {
	Contract      string              `json:"contract"`
	Runtime       eebusraw.RedactedID `json:"runtime"`
	LocalSKI      string              `json:"local_ski"`
	MaskTier      eebusraw.MaskTier   `json:"mask_tier"`
	CapturedAt    time.Time           `json:"captured_at"`
	DataTimestamp time.Time           `json:"data_timestamp"`
}

type runtimeStatusPayload struct {
	State       string                     `json:"state"`
	Degradation *runtimeDegradationPayload `json:"degradation,omitempty"`
}

type runtimeDegradationPayload struct {
	Reason string    `json:"reason"`
	Since  time.Time `json:"since"`
}

type runtimePairingPayload struct {
	RemoteSKI string    `json:"remote_ski"`
	State     string    `json:"state"`
	Since     time.Time `json:"since"`
}

type runtimeServicePayload struct {
	SKI        string  `json:"ski"`
	SHIPID     *string `json:"ship_id,omitempty"`
	Kind       string  `json:"kind"`
	Visible    bool    `json:"visible"`
	Paired     bool    `json:"paired"`
	Name       *string `json:"name,omitempty"`
	Identifier *string `json:"identifier,omitempty"`
	Brand      *string `json:"brand,omitempty"`
	Type       *string `json:"type,omitempty"`
	Model      *string `json:"model,omitempty"`
}

type runtimeSessionPayload struct {
	ID        string    `json:"id"`
	RemoteSKI string    `json:"remote_ski"`
	State     string    `json:"state"`
	Since     time.Time `json:"since"`
}

type runtimeDevicePayload struct {
	SKI         string                  `json:"ski"`
	SHIPID      *string                 `json:"ship_id,omitempty"`
	Address     string                  `json:"address"`
	Type        string                  `json:"type"`
	Description *string                 `json:"description,omitempty"`
	Metadata    *map[string]string      `json:"metadata,omitempty"`
	Opaque      *[]runtimeOpaquePayload `json:"opaque,omitempty"`
}

type runtimeEntityPayload struct {
	DeviceAddress string  `json:"device_address"`
	EntityAddress string  `json:"entity_address"`
	Type          string  `json:"type"`
	Description   *string `json:"description,omitempty"`
}

type runtimeFeaturePayload struct {
	DeviceAddress  string  `json:"device_address"`
	EntityAddress  string  `json:"entity_address"`
	FeatureAddress string  `json:"feature_address"`
	Type           string  `json:"type"`
	Role           string  `json:"role"`
	Description    *string `json:"description,omitempty"`
}

type runtimeUseCasePayload struct {
	ContextAddress      string   `json:"context_address"`
	Name                string   `json:"name"`
	Actor               string   `json:"actor"`
	ResolvedRole        *string  `json:"resolved_role,omitempty"`
	Scenarios           []string `json:"scenarios"`
	Version             *string  `json:"version,omitempty"`
	Availability        *bool    `json:"availability,omitempty"`
	DocumentSubrevision *string  `json:"document_subrevision,omitempty"`
}

type runtimeOpaquePayload struct {
	Path   string `json:"path"`
	Source string `json:"source"`
	Value  any    `json:"value"`
}

func marshalRuntimeSnapshot(graph []runtimeGraphObservation, now time.Time) ([]byte, error) {
	if len(graph) == 0 {
		return nil, errors.New("runtime graph is empty")
	}
	first := graph[0]
	return marshalRuntimeSnapshotWithIdentity(first.RuntimeID, first.LocalSKI, graph, now)
}

func marshalRuntimeSnapshotWithIdentity(runtimeIdentity, localIdentity string, graph []runtimeGraphObservation, now time.Time) ([]byte, error) {
	runtimeID, err := eebusraw.RedactID(eebusraw.IDKindPeer, runtimeIdentity)
	if err != nil {
		return nil, err
	}
	now = now.UTC()
	payload := runtimeSnapshotPayload{
		Meta: runtimeSnapshotMetaPayload{
			Contract:      "helianthus.eebus.runtime.raw-snapshot.v1",
			Runtime:       runtimeID,
			LocalSKI:      localIdentity,
			MaskTier:      eebusraw.MaskTier("raw"),
			CapturedAt:    now,
			DataTimestamp: now,
		},
		Status:   runtimeStatusPayload{State: "starting"},
		Pairing:  []runtimePairingPayload{},
		Services: []runtimeServicePayload{},
		Sessions: []runtimeSessionPayload{},
		Devices:  []runtimeDevicePayload{},
		Entities: []runtimeEntityPayload{},
		Features: []runtimeFeaturePayload{},
		UseCases: []runtimeUseCasePayload{},
		Opaque:   []runtimeOpaquePayload{},
	}
	visible := false
	connected := false
	disconnected := false
	trustDegradation := ""
	for _, remote := range graph {
		if remote.PairingState != "" {
			payload.Pairing = append(payload.Pairing, runtimePairingPayload{
				RemoteSKI: remote.RemoteSKI, State: remote.PairingState, Since: remote.Since,
			})
		}
		if remote.RemoteSKI != "" {
			payload.Services = append(payload.Services, runtimeServicePayload{
				SKI: remote.RemoteSKI, SHIPID: runtimeOptionalString(remote.ShipID), Kind: "remote",
				Visible: remote.Visible, Paired: remote.Paired, Name: runtimeOptionalString(remote.ServiceName),
				Identifier: runtimeOptionalString(remote.ServiceIdentifier), Brand: runtimeOptionalString(remote.ServiceBrand),
				Type: runtimeOptionalString(remote.ServiceType), Model: runtimeOptionalString(remote.ServiceModel),
			})
		}
		if remote.SessionID != "" && remote.SessionState != "" {
			payload.Sessions = append(payload.Sessions, runtimeSessionPayload{
				ID: remote.SessionID, RemoteSKI: remote.RemoteSKI,
				State: remote.SessionState, Since: remote.Since,
			})
		}
		for _, device := range remote.Devices {
			if device.SHIPID == "" {
				device.SHIPID = remote.ShipID
			}
			if device.SKI == "" {
				device.SKI = remote.RemoteSKI
			}
			devicePayload, entities, features, useCases := marshalRuntimeDevice(device)
			if devicePayload.Address == "" {
				continue
			}
			payload.Devices = append(payload.Devices, devicePayload)
			payload.Entities = append(payload.Entities, entities...)
			payload.Features = append(payload.Features, features...)
			payload.UseCases = append(payload.UseCases, useCases...)
		}
		visible = visible || remote.Visible
		connected = connected || remote.SessionState == "connected"
		disconnected = disconnected || remote.SessionState == "disconnected" || remote.SessionState == "degraded"
		if remote.TrustDegradation == "denied-trust" || trustDegradation == "" && remote.TrustDegradation == "certificate-unavailable" {
			trustDegradation = remote.TrustDegradation
		}
	}
	if trustDegradation != "" {
		payload.Status.State = "degraded"
		payload.Status.Degradation = &runtimeDegradationPayload{Reason: trustDegradation, Since: now}
	} else if connected {
		payload.Status.State = "ready"
	} else if disconnected {
		payload.Status.State = "degraded"
		payload.Status.Degradation = &runtimeDegradationPayload{Reason: "remote-disconnect", Since: now}
	} else if !visible {
		payload.Status.State = "degraded"
		payload.Status.Degradation = &runtimeDegradationPayload{Reason: "no-visible-services", Since: now}
	}
	return json.Marshal(payload)
}

func marshalRuntimeDevice(source runtimeDeviceObservation) (
	runtimeDevicePayload,
	[]runtimeEntityPayload,
	[]runtimeFeaturePayload,
	[]runtimeUseCasePayload,
) {
	if source.SKI == "" || source.Address == "" || source.Type == "" {
		return runtimeDevicePayload{}, nil, nil, nil
	}
	result := runtimeDevicePayload{
		SKI: source.SKI, SHIPID: runtimeOptionalString(source.SHIPID),
		Address: source.Address, Type: source.Type, Description: cloneRuntimeString(source.Description),
	}
	if source.Metadata != nil {
		metadata := cloneRuntimeMetadata(source.Metadata)
		result.Metadata = &metadata
	}
	if source.Opaque != nil {
		opaque := cloneRuntimeOpaque(source.Opaque)
		result.Opaque = &opaque
	}
	var entities []runtimeEntityPayload
	var features []runtimeFeaturePayload
	for _, entity := range source.Entities {
		if entity.DeviceAddress == "" || entity.EntityAddress == "" || entity.Type == "" {
			continue
		}
		entities = append(entities, runtimeEntityPayload{
			DeviceAddress: entity.DeviceAddress, EntityAddress: entity.EntityAddress,
			Type: entity.Type, Description: cloneRuntimeString(entity.Description),
		})
		for _, feature := range entity.Features {
			if feature.DeviceAddress == "" || feature.EntityAddress == "" ||
				feature.FeatureAddress == "" || feature.Type == "" || feature.Role == "" {
				continue
			}
			features = append(features, runtimeFeaturePayload{
				DeviceAddress: feature.DeviceAddress, EntityAddress: feature.EntityAddress,
				FeatureAddress: feature.FeatureAddress, Type: feature.Type, Role: feature.Role,
				Description: cloneRuntimeString(feature.Description),
			})
		}
	}
	useCases := make([]runtimeUseCasePayload, 0, len(source.UseCases))
	for _, useCase := range source.UseCases {
		if useCase.ContextAddress == "" || useCase.Name == "" || useCase.Actor == "" {
			continue
		}
		useCases = append(useCases, runtimeUseCasePayload{
			ContextAddress: useCase.ContextAddress, Name: useCase.Name, Actor: useCase.Actor,
			ResolvedRole:        cloneRuntimeString(useCase.ResolvedRole),
			Scenarios:           append([]string(nil), useCase.Scenarios...),
			Version:             cloneRuntimeString(useCase.Version),
			Availability:        cloneRuntimeBool(useCase.Availability),
			DocumentSubrevision: cloneRuntimeString(useCase.DocumentSubrevision),
		})
	}
	return result, entities, features, useCases
}

func runtimeOptionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
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
	result.SessionState = strings.TrimSpace(result.SessionState)
	result.PairingState = strings.TrimSpace(result.PairingState)
	result.TrustDegradation = strings.TrimSpace(result.TrustDegradation)
	result.ShipID = strings.TrimSpace(result.ShipID)
	result.ServiceName = strings.TrimSpace(result.ServiceName)
	result.ServiceIdentifier = strings.TrimSpace(result.ServiceIdentifier)
	result.ServiceBrand = strings.TrimSpace(result.ServiceBrand)
	result.ServiceType = strings.TrimSpace(result.ServiceType)
	result.ServiceModel = strings.TrimSpace(result.ServiceModel)
	if result.RuntimeID == "" || result.LocalSKI == "" || result.RemoteSKI == "" {
		return runtimeGraphObservation{}, errors.New("runtime graph identities are required")
	}
	if !validRuntimeSKI(result.LocalSKI) || !validRuntimeSKI(result.RemoteSKI) {
		return runtimeGraphObservation{}, errors.New("runtime graph SKIs must contain 40 hexadecimal characters")
	}
	switch result.SessionState {
	case "", "unknown", "connecting", "connected", "disconnected", "degraded":
	default:
		return runtimeGraphObservation{}, errors.New("runtime session state is unsupported")
	}
	if (result.SessionID == "") != (result.SessionState == "") {
		return runtimeGraphObservation{}, errors.New("runtime session identity and state must be observed together")
	}
	switch result.PairingState {
	case "", string(eebusraw.PairingStateUnknown), string(eebusraw.PairingStateUnpaired), string(eebusraw.PairingStatePaired), string(eebusraw.PairingStateDenied):
	default:
		return runtimeGraphObservation{}, errors.New("runtime pairing state is unsupported")
	}
	switch result.TrustDegradation {
	case "", "denied-trust", "certificate-unavailable":
	default:
		return runtimeGraphObservation{}, errors.New("runtime trust degradation is unsupported")
	}
	if result.Since.IsZero() {
		return runtimeGraphObservation{}, errors.New("runtime observation timestamp is required")
	}
	result.Since = result.Since.UTC()

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
	result.SKI = strings.TrimSpace(result.SKI)
	result.SHIPID = strings.TrimSpace(result.SHIPID)
	result.Address = strings.TrimSpace(result.Address)
	result.Type = strings.TrimSpace(result.Type)
	result.Description = cloneRuntimeString(result.Description)
	result.Metadata = cloneRuntimeMetadata(result.Metadata)
	opaque, err := normalizeRuntimeOpaque(result.Opaque)
	if err != nil {
		return runtimeDeviceObservation{}, err
	}
	result.Opaque = opaque
	if result.ID == "" {
		return runtimeDeviceObservation{}, errors.New("runtime device identity is required")
	}
	useCaseIDs, err := uniqueRuntimeStrings(result.UseCaseIDs, "use case")
	if err != nil {
		return runtimeDeviceObservation{}, err
	}
	result.UseCaseIDs = useCaseIDs
	useCases := make(map[string]runtimeUseCaseObservation, len(result.UseCases))
	for _, value := range result.UseCases {
		value.ID = strings.TrimSpace(value.ID)
		value.ContextAddress = strings.TrimSpace(value.ContextAddress)
		value.Name = strings.TrimSpace(value.Name)
		value.Actor = strings.TrimSpace(value.Actor)
		value.ResolvedRole = cloneRuntimeString(value.ResolvedRole)
		value.Version = cloneRuntimeString(value.Version)
		value.Availability = cloneRuntimeBool(value.Availability)
		value.DocumentSubrevision = cloneRuntimeString(value.DocumentSubrevision)
		value.Scenarios = append([]string(nil), value.Scenarios...)
		for index := range value.Scenarios {
			value.Scenarios[index] = strings.TrimSpace(value.Scenarios[index])
		}
		sort.Strings(value.Scenarios)
		if value.ID == "" {
			return runtimeDeviceObservation{}, errors.New("runtime use-case identity is required")
		}
		if existing, ok := useCases[value.ID]; ok {
			value = mergeRuntimeUseCaseObservations(existing, value)
		}
		useCases[value.ID] = value
	}
	result.UseCases = make([]runtimeUseCaseObservation, 0, len(useCases))
	for _, value := range useCases {
		result.UseCases = append(result.UseCases, value)
	}
	sort.Slice(result.UseCases, func(left, right int) bool {
		return result.UseCases[left].ID < result.UseCases[right].ID
	})

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
	result.DeviceAddress = strings.TrimSpace(result.DeviceAddress)
	result.EntityAddress = strings.TrimSpace(result.EntityAddress)
	result.Type = strings.TrimSpace(result.Type)
	result.Description = cloneRuntimeString(result.Description)
	if result.ID == "" {
		return runtimeEntityObservation{}, errors.New("runtime entity identity is required")
	}
	features := make(map[string]runtimeFeatureObservation, len(result.Features))
	for _, sourceFeature := range result.Features {
		feature := sourceFeature
		feature.ID = strings.TrimSpace(feature.ID)
		feature.DeviceAddress = strings.TrimSpace(feature.DeviceAddress)
		feature.EntityAddress = strings.TrimSpace(feature.EntityAddress)
		feature.FeatureAddress = strings.TrimSpace(feature.FeatureAddress)
		feature.Type = strings.TrimSpace(feature.Type)
		feature.Role = strings.TrimSpace(feature.Role)
		feature.Description = cloneRuntimeString(feature.Description)
		if feature.ID == "" {
			return runtimeEntityObservation{}, errors.New("runtime feature identity is required")
		}
		if existing, ok := features[feature.ID]; ok {
			var err error
			feature, err = mergeRuntimeFeatureObservations(existing, feature)
			if err != nil {
				return runtimeEntityObservation{}, err
			}
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
	result := mergeRuntimeDeviceFields(left, right)
	useCaseIDs, err := uniqueRuntimeStrings(append(append([]string(nil), left.UseCaseIDs...), right.UseCaseIDs...), "use case")
	if err != nil {
		return runtimeDeviceObservation{}, err
	}
	result.UseCaseIDs = useCaseIDs
	useCases := make(map[string]runtimeUseCaseObservation, len(left.UseCases)+len(right.UseCases))
	for _, value := range left.UseCases {
		useCases[value.ID] = value
	}
	for _, value := range right.UseCases {
		if existing, ok := useCases[value.ID]; ok {
			value = mergeRuntimeUseCaseObservations(existing, value)
		}
		useCases[value.ID] = value
	}
	result.UseCases = make([]runtimeUseCaseObservation, 0, len(useCases))
	for _, value := range useCases {
		result.UseCases = append(result.UseCases, value)
	}
	sort.Slice(result.UseCases, func(left, right int) bool {
		return result.UseCases[left].ID < result.UseCases[right].ID
	})
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

func mergeRuntimeDeviceCollections(
	left, right []runtimeDeviceObservation,
) ([]runtimeDeviceObservation, error) {
	devices := make(map[string]runtimeDeviceObservation, len(left)+len(right))
	for _, source := range left {
		device, err := normalizeRuntimeDeviceObservation(source)
		if err != nil {
			return nil, err
		}
		devices[device.ID] = device
	}
	for _, source := range right {
		device, err := normalizeRuntimeDeviceObservation(source)
		if err != nil {
			return nil, err
		}
		if existing, ok := devices[device.ID]; ok {
			device, err = mergeRuntimeDeviceObservations(existing, device)
			if err != nil {
				return nil, err
			}
		}
		devices[device.ID] = device
	}
	result := make([]runtimeDeviceObservation, 0, len(devices))
	for _, device := range devices {
		result = append(result, device)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].ID < result[right].ID
	})
	return result, nil
}

func mergeRuntimeEntityObservations(left, right runtimeEntityObservation) (runtimeEntityObservation, error) {
	result := left
	if right.DeviceAddress != "" {
		result.DeviceAddress = right.DeviceAddress
	}
	if right.EntityAddress != "" {
		result.EntityAddress = right.EntityAddress
	}
	if right.Type != "" {
		result.Type = right.Type
	}
	if right.Description != nil {
		result.Description = cloneRuntimeString(right.Description)
	}
	features := make(map[string]runtimeFeatureObservation, len(left.Features)+len(right.Features))
	for _, feature := range left.Features {
		features[feature.ID] = feature
	}
	for _, feature := range right.Features {
		if existing, ok := features[feature.ID]; ok {
			var err error
			feature, err = mergeRuntimeFeatureObservations(existing, feature)
			if err != nil {
				return runtimeEntityObservation{}, err
			}
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

func mergeRuntimeFeatureObservations(
	left, right runtimeFeatureObservation,
) (runtimeFeatureObservation, error) {
	result := left
	if right.DeviceAddress != "" {
		result.DeviceAddress = right.DeviceAddress
	}
	if right.EntityAddress != "" {
		result.EntityAddress = right.EntityAddress
	}
	if right.FeatureAddress != "" {
		result.FeatureAddress = right.FeatureAddress
	}
	if right.Type != "" {
		result.Type = right.Type
	}
	if right.Role != "" {
		if result.Role != "" && result.Role != right.Role {
			return runtimeFeatureObservation{}, errors.New("runtime feature identity has conflicting roles")
		}
		result.Role = right.Role
	}
	if right.Description != nil {
		result.Description = cloneRuntimeString(right.Description)
	}
	return result, nil
}

func mergeRuntimeUseCaseObservations(
	left, right runtimeUseCaseObservation,
) runtimeUseCaseObservation {
	result := left
	if right.ContextAddress != "" {
		result.ContextAddress = right.ContextAddress
	}
	if right.Name != "" {
		result.Name = right.Name
	}
	if right.Actor != "" {
		result.Actor = right.Actor
	}
	if right.ResolvedRole != nil {
		result.ResolvedRole = cloneRuntimeString(right.ResolvedRole)
	}
	scenarios := make(map[string]struct{}, len(left.Scenarios)+len(right.Scenarios))
	for _, value := range append(append([]string(nil), left.Scenarios...), right.Scenarios...) {
		value = strings.TrimSpace(value)
		if value != "" {
			scenarios[value] = struct{}{}
		}
	}
	result.Scenarios = make([]string, 0, len(scenarios))
	for value := range scenarios {
		result.Scenarios = append(result.Scenarios, value)
	}
	sort.Strings(result.Scenarios)
	if right.Version != nil {
		result.Version = cloneRuntimeString(right.Version)
	}
	if right.Availability != nil {
		result.Availability = cloneRuntimeBool(right.Availability)
	}
	if right.DocumentSubrevision != nil {
		result.DocumentSubrevision = cloneRuntimeString(right.DocumentSubrevision)
	}
	return result
}

func mergeRuntimeDeviceFields(left, right runtimeDeviceObservation) runtimeDeviceObservation {
	result := left
	if right.SKI != "" {
		result.SKI = right.SKI
	}
	if right.SHIPID != "" {
		result.SHIPID = right.SHIPID
	}
	if right.Address != "" {
		result.Address = right.Address
	}
	if right.Type != "" {
		result.Type = right.Type
	}
	if right.Description != nil {
		result.Description = cloneRuntimeString(right.Description)
	}
	if right.Metadata != nil {
		if result.Metadata == nil {
			result.Metadata = make(map[string]string, len(right.Metadata))
		}
		for key, value := range right.Metadata {
			result.Metadata[key] = value
		}
	}
	result.Opaque = mergeRuntimeOpaque(result.Opaque, right.Opaque)
	return result
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
		result.Devices[deviceIndex].Description = cloneRuntimeString(device.Description)
		result.Devices[deviceIndex].Metadata = cloneRuntimeMetadata(device.Metadata)
		result.Devices[deviceIndex].Opaque = cloneRuntimeOpaque(device.Opaque)
		result.Devices[deviceIndex].UseCaseIDs = append([]string(nil), device.UseCaseIDs...)
		result.Devices[deviceIndex].UseCases = make([]runtimeUseCaseObservation, len(device.UseCases))
		for useCaseIndex, useCase := range device.UseCases {
			result.Devices[deviceIndex].UseCases[useCaseIndex] = useCase
			result.Devices[deviceIndex].UseCases[useCaseIndex].ResolvedRole = cloneRuntimeString(useCase.ResolvedRole)
			result.Devices[deviceIndex].UseCases[useCaseIndex].Scenarios = append([]string(nil), useCase.Scenarios...)
			result.Devices[deviceIndex].UseCases[useCaseIndex].Version = cloneRuntimeString(useCase.Version)
			result.Devices[deviceIndex].UseCases[useCaseIndex].Availability = cloneRuntimeBool(useCase.Availability)
			result.Devices[deviceIndex].UseCases[useCaseIndex].DocumentSubrevision = cloneRuntimeString(useCase.DocumentSubrevision)
		}
		result.Devices[deviceIndex].Entities = make([]runtimeEntityObservation, len(device.Entities))
		for entityIndex, entity := range device.Entities {
			result.Devices[deviceIndex].Entities[entityIndex] = entity
			result.Devices[deviceIndex].Entities[entityIndex].Description = cloneRuntimeString(entity.Description)
			result.Devices[deviceIndex].Entities[entityIndex].Features = make([]runtimeFeatureObservation, len(entity.Features))
			for featureIndex, feature := range entity.Features {
				result.Devices[deviceIndex].Entities[entityIndex].Features[featureIndex] = feature
				result.Devices[deviceIndex].Entities[entityIndex].Features[featureIndex].Description =
					cloneRuntimeString(feature.Description)
			}
		}
	}
	return result
}

func cloneRuntimeMetadata(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func cloneRuntimeOpaque(source []runtimeOpaquePayload) []runtimeOpaquePayload {
	if source == nil {
		return nil
	}
	result := make([]runtimeOpaquePayload, len(source))
	for index, observation := range source {
		result[index] = observation
		value, err := detachedRuntimeJSONValue(observation.Value)
		if err == nil {
			result[index].Value = value
		} else {
			result[index].Value = nil
		}
	}
	return result
}

func normalizeRuntimeOpaque(source []runtimeOpaquePayload) ([]runtimeOpaquePayload, error) {
	if source == nil {
		return nil, nil
	}
	result := make([]runtimeOpaquePayload, len(source))
	for index, observation := range source {
		value, err := detachedRuntimeJSONValue(observation.Value)
		if err != nil {
			return nil, errors.New("runtime opaque observation is not JSON")
		}
		result[index] = runtimeOpaquePayload{
			Path: strings.TrimSpace(observation.Path), Source: strings.TrimSpace(observation.Source), Value: value,
		}
	}
	return result, nil
}

func mergeRuntimeOpaque(left, right []runtimeOpaquePayload) []runtimeOpaquePayload {
	if right == nil {
		return cloneRuntimeOpaque(left)
	}
	values := make(map[string]runtimeOpaquePayload, len(left)+len(right))
	for _, observation := range append(cloneRuntimeOpaque(left), cloneRuntimeOpaque(right)...) {
		encoded, _ := json.Marshal(observation.Value)
		key := observation.Path + "\x00" + observation.Source + "\x00" + string(encoded)
		values[key] = observation
	}
	result := make([]runtimeOpaquePayload, 0, len(values))
	for _, observation := range values {
		result = append(result, observation)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Path != result[right].Path {
			return result[left].Path < result[right].Path
		}
		if result[left].Source != result[right].Source {
			return result[left].Source < result[right].Source
		}
		leftValue, _ := json.Marshal(result[left].Value)
		rightValue, _ := json.Marshal(result[right].Value)
		return string(leftValue) < string(rightValue)
	})
	return result
}
