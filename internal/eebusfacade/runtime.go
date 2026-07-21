package eebusfacade

import (
	"context"
	"crypto"
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
	firstTrust       *runtimeFirstTrustResources
	listenerTerminal <-chan error
	runClaimed       bool
	serviceStarted   bool
	closed           bool
	closeErr         error
}

type runtimeMaterial struct {
	certificate tls.Certificate
	localSKI    string
	nodeToken   string
	pretrusted  map[string]bool
	firstTrust  *runtimeFirstTrustAuthorization
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

type runtimeDependencies struct {
	loadMaterial          runtimeMaterialLoader
	newService            runtimeServiceFactory
	now                   func() time.Time
	openAssociationBridge runtimeAssociationBridgeFactory
	startFirstTrustAdmin  runtimeFirstTrustAdminFactory
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
	RuntimeID        string
	LocalSKI         string
	RemoteSKI        string
	SessionID        string
	SessionState     string
	PairingState     string
	Visible          bool
	Paired           bool
	Since            time.Time
	ServiceIDs       []string
	Devices          []runtimeDeviceObservation
	ShipID           string
	SessionIndex     uint64
	TrustDegradation string
}

type runtimeObservationReducer struct {
	mu sync.RWMutex

	initialized bool
	runtimeID   string
	localSKI    string
	remotes     map[string]runtimeGraphObservation
}

var _ Backend = (*serviceBackend)(nil)

var defaultRuntimeDependencies = runtimeDependencies{
	loadMaterial:          loadProtectedRuntimeMaterial,
	newService:            newEEBusService,
	now:                   time.Now,
	openAssociationBridge: openRuntimeAssociationBridge,
	startFirstTrustAdmin:  startFirstTrustAdmin,
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
	closeRuntime := func(cause error) error {
		trustErr := closeFirstTrust()
		service.Shutdown()
		return errors.Join(cause, trustErr)
	}
	if err := service.Setup(); err != nil {
		return nil, closeRuntime(fmt.Errorf("setup eebus runtime service: %w", err))
	}
	if firstTrust != nil {
		if err := attachRuntimeFirstTrust(ctx, firstTrust, service, reader, dependencies); err != nil {
			return nil, closeRuntime(err)
		}
	}
	backend := &serviceBackend{service: service, handler: handler, firstTrust: firstTrust}
	if scoped, ok := service.(runtimeScopedService); ok {
		if !backend.runtimeStartAuthorized() {
			return nil, closeRuntime(errRuntimeTrustEffectsDenied)
		}
		if err := scoped.StartWithPolicy(); err != nil {
			return nil, closeRuntime(fmt.Errorf("start scoped eebus runtime service: %w", err))
		}
		terminal := scoped.ListenerTerminal()
		if terminal == nil {
			return nil, closeRuntime(errors.New("scoped eebus runtime service omitted listener terminal signal"))
		}
		backend.listenerTerminal = terminal
		backend.serviceStarted = true
	}
	return backend, nil
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

func (backend *serviceBackend) Close() error {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.closed {
		return backend.closeErr
	}
	backend.closed = true
	if backend.firstTrust != nil {
		backend.closeErr = backend.firstTrust.Close()
	}
	if backend.service != nil {
		backend.service.Shutdown()
	}
	return backend.closeErr
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

	runtimeID                 string
	localSKI                  string
	reducer                   *runtimeObservationReducer
	observations              map[string]runtimeGraphObservation
	projectionCapture         func() trustAdminProjection
	projectionLivenessAllowed func(string) bool
	projectionRemotes         []string
	policyRemotes             []string
	publishedTrustRevision    uint64
	now                       func() time.Time
	publish                   func([]byte)
	errors                    chan error
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
	handler.updateRemote(ski, true, func(observation *runtimeGraphObservation) {
		if len(observation.ServiceIDs) == 0 {
			observation.ServiceIDs = []string{"service:" + ski}
		}
		observation.SessionIndex++
		observation.SessionID = runtimeSessionIdentity(*observation)
		observation.SessionState = "connected"
		observation.Visible = true
		observation.Since = handler.timestamp()
		observation.Devices = devices
	})
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
}

func (handler *runtimeServiceHandler) VisibleRemoteServicesUpdated(_ eebusapi.ServiceInterface, entries []shipapi.RemoteService) {
	visible := make(map[string]bool, len(entries))
	for _, entry := range entries {
		ski := strings.ToLower(strings.TrimSpace(entry.Ski))
		if validRuntimeSKI(ski) {
			visible[ski] = true
		}
	}

	handler.mu.Lock()
	changed := false
	for ski := range visible {
		if !handler.remoteLivenessAllowedLocked(ski) {
			continue
		}
		if _, exists := handler.observations[ski]; exists {
			continue
		}
		observation := handler.newRemoteObservation(ski)
		observation.Visible = true
		observation.ServiceIDs = []string{"service:" + ski}
		if err := handler.reducer.Replace(observation); err != nil {
			handler.mu.Unlock()
			handler.report(err)
			return
		}
		handler.observations[ski] = observation
		changed = true
	}
	for ski, observation := range handler.observations {
		if !handler.remoteLivenessAllowedLocked(ski) {
			continue
		}
		isVisible := visible[ski]
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
		changed = true
	}
	handler.mu.Unlock()
	if changed {
		handler.publishOrReport()
	}
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
		observation.Since = handler.timestamp()
	})
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
	handler.mu.Unlock()
	if capture != nil {
		return handler.publishTrustAdminProjection(capture())
	}
	return handler.publishRuntimeGraph(handler.reducer.Snapshot())
}

func (handler *runtimeServiceHandler) publishTrustAdminProjection(projection trustAdminProjection) error {
	handler.publishMu.Lock()
	defer handler.publishMu.Unlock()
	if projection.revision < handler.publishedTrustRevision {
		return nil
	}
	handler.publishedTrustRevision = projection.revision
	graph, remotes := handler.runtimeGraphAndProjectionRemotes()
	applyTrustAdminProjection(graph, remotes, projection)
	return handler.publishRuntimeGraph(graph)
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

func (handler *runtimeServiceHandler) runtimeGraphAndProjectionRemotes() ([]runtimeGraphObservation, []string) {
	handler.mu.Lock()
	remotes := append([]string(nil), handler.projectionRemotes...)
	handler.mu.Unlock()
	return handler.reducer.Snapshot(), remotes
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

func runtimeDevicesForRemote(service eebusapi.ServiceInterface, ski string) ([]runtimeDeviceObservation, error) {
	if service == nil || service.LocalDevice() == nil {
		return nil, nil
	}
	remote := service.LocalDevice().RemoteDeviceForSki(ski)
	if remote == nil {
		return nil, nil
	}
	deviceID, err := runtimeIdentity("device", ski, remote.Address())
	if err != nil {
		return nil, err
	}
	device := runtimeDeviceObservation{ID: deviceID}
	for index, entity := range remote.Entities() {
		if entity == nil {
			continue
		}
		entityID, err := runtimeIdentity("entity", ski, entity.Address(), index)
		if err != nil {
			return nil, err
		}
		entityObservation := runtimeEntityObservation{ID: entityID}
		for featureIndex, feature := range entity.Features() {
			if feature == nil {
				continue
			}
			featureID, err := runtimeIdentity("feature", ski, feature.Address(), featureIndex)
			if err != nil {
				return nil, err
			}
			role := strings.ToLower(string(feature.Role()))
			if role != "client" && role != "server" {
				role = ""
			}
			entityObservation.Features = append(entityObservation.Features, runtimeFeatureObservation{ID: featureID, Role: role})
		}
		device.Entities = append(device.Entities, entityObservation)
	}
	for index, useCase := range remote.UseCases() {
		useCaseID, err := runtimeIdentity("usecase", ski, useCase, index)
		if err != nil {
			return nil, err
		}
		device.UseCaseIDs = append(device.UseCaseIDs, useCaseID)
	}
	return []runtimeDeviceObservation{device}, nil
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
	Pairing  []runtimePairingPayload    `json:"pairing,omitempty"`
	Services []runtimeServicePayload    `json:"services,omitempty"`
	Sessions []runtimeSessionPayload    `json:"sessions,omitempty"`
	Topology runtimeTopologyPayload     `json:"topology"`
}

type runtimeSnapshotMetaPayload struct {
	Contract      string              `json:"contract"`
	Runtime       eebusraw.RedactedID `json:"runtime"`
	LocalSKI      eebusraw.RedactedID `json:"local_ski"`
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
	Remote eebusraw.RedactedID `json:"remote"`
	State  string              `json:"state"`
	Since  time.Time           `json:"since,omitempty"`
}

type runtimeServicePayload struct {
	ID      eebusraw.RedactedID `json:"id"`
	Kind    string              `json:"kind"`
	Visible bool                `json:"visible"`
	Paired  bool                `json:"paired"`
}

type runtimeSessionPayload struct {
	ID     eebusraw.RedactedID `json:"id"`
	Remote eebusraw.RedactedID `json:"remote"`
	State  string              `json:"state"`
	Since  time.Time           `json:"since,omitempty"`
}

type runtimeTopologyPayload struct {
	Devices []runtimeDevicePayload `json:"devices,omitempty"`
}

type runtimeDevicePayload struct {
	ID            eebusraw.RedactedID     `json:"id"`
	Entities      []runtimeEntityPayload  `json:"entities,omitempty"`
	UseCaseClaims []runtimeUseCasePayload `json:"usecase_claims,omitempty"`
}

type runtimeEntityPayload struct {
	ID       eebusraw.RedactedID     `json:"id"`
	Features []runtimeFeaturePayload `json:"features,omitempty"`
}

type runtimeFeaturePayload struct {
	ID   eebusraw.RedactedID `json:"id"`
	Role string              `json:"role"`
}

type runtimeUseCasePayload struct {
	ID eebusraw.RedactedID `json:"id"`
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
	localSKI, err := eebusraw.RedactID(eebusraw.IDKindLocalSKI, localIdentity)
	if err != nil {
		return nil, err
	}
	now = now.UTC()
	payload := runtimeSnapshotPayload{
		Meta: runtimeSnapshotMetaPayload{
			Contract:      "helianthus.eebus.runtime.raw-snapshot.v1",
			Runtime:       runtimeID,
			LocalSKI:      localSKI,
			MaskTier:      eebusraw.MaskTierRedacted,
			CapturedAt:    now,
			DataTimestamp: now,
		},
		Status: runtimeStatusPayload{State: "starting"},
	}
	visible := false
	connected := false
	disconnected := false
	trustDegradation := ""
	for _, remote := range graph {
		remoteID, err := eebusraw.RedactID(eebusraw.IDKindRemoteSKI, remote.RemoteSKI)
		if err != nil {
			return nil, err
		}
		if remote.PairingState != "" {
			payload.Pairing = append(payload.Pairing, runtimePairingPayload{Remote: remoteID, State: remote.PairingState, Since: remote.Since})
		}
		for _, serviceID := range remote.ServiceIDs {
			id, err := eebusraw.RedactID(eebusraw.IDKindPeer, serviceID)
			if err != nil {
				return nil, err
			}
			payload.Services = append(payload.Services, runtimeServicePayload{ID: id, Kind: "remote", Visible: remote.Visible, Paired: remote.Paired})
		}
		if remote.SessionID != "" && remote.SessionState != "" {
			sessionID, err := eebusraw.RedactID(eebusraw.IDKindSession, remote.SessionID)
			if err != nil {
				return nil, err
			}
			payload.Sessions = append(payload.Sessions, runtimeSessionPayload{ID: sessionID, Remote: remoteID, State: remote.SessionState, Since: remote.Since})
		}
		for _, device := range remote.Devices {
			devicePayload, err := marshalRuntimeDevice(device)
			if err != nil {
				return nil, err
			}
			payload.Topology.Devices = append(payload.Topology.Devices, devicePayload)
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

func marshalRuntimeDevice(source runtimeDeviceObservation) (runtimeDevicePayload, error) {
	id, err := eebusraw.RedactID(eebusraw.IDKindPeer, source.ID)
	if err != nil {
		return runtimeDevicePayload{}, err
	}
	result := runtimeDevicePayload{ID: id}
	for _, entity := range source.Entities {
		entityID, err := eebusraw.RedactID(eebusraw.IDKindPeer, entity.ID)
		if err != nil {
			return runtimeDevicePayload{}, err
		}
		entityPayload := runtimeEntityPayload{ID: entityID}
		for _, feature := range entity.Features {
			featureID, err := eebusraw.RedactID(eebusraw.IDKindPeer, feature.ID)
			if err != nil {
				return runtimeDevicePayload{}, err
			}
			entityPayload.Features = append(entityPayload.Features, runtimeFeaturePayload{ID: featureID, Role: feature.Role})
		}
		result.Entities = append(result.Entities, entityPayload)
	}
	for _, useCase := range source.UseCaseIDs {
		useCaseID, err := eebusraw.RedactID(eebusraw.IDKindPeer, useCase)
		if err != nil {
			return runtimeDevicePayload{}, err
		}
		result.UseCaseClaims = append(result.UseCaseClaims, runtimeUseCasePayload{ID: useCaseID})
	}
	return result, nil
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
