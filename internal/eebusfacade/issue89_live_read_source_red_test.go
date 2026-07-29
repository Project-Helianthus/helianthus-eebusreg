package eebusfacade

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	eebusapi "github.com/Project-Helianthus/helianthus-eebus-go/api"
	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
	shipcert "github.com/Project-Helianthus/helianthus-ship-go/cert"
	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
	spine "github.com/Project-Helianthus/helianthus-spine-go/spine"
)

func TestIssue89NativeMeasurementInventoryTargetRoundTripPreservesCasing(t *testing.T) {
	fixture := newIssue83RawBridgeFixture(t)
	native := fixture.locators[0]
	if native.FeatureType != string(spinemodel.FeatureTypeTypeMeasurement) {
		t.Fatalf("fixture feature type = %q, want native Measurement", native.FeatureType)
	}

	inventory, terminal := fixture.bridge.featuresGet(
		context.Background(),
		issue83FacadeAuthorization(eebusraw.ToolV1FeaturesGet),
		eebusraw.FeaturesGetRequestV1{Target: native},
	)
	if terminal != nil {
		t.Fatalf("native features.get error = %+v", terminal)
	}
	target := issue83TargetFromLocator(inventory.Feature)
	data, terminal := fixture.bridge.featuresDataGet(
		context.Background(),
		issue83FacadeAuthorization(eebusraw.ToolV1FeaturesDataGet),
		eebusraw.FeatureDataGetRequestV1{
			Targets:   []eebusraw.FeatureTargetV1{target},
			TimeoutMS: 1000,
		},
	)
	if terminal != nil {
		t.Fatalf("native inventory target round-trip error = %+v", terminal)
	}
	if len(data.Results) != 1 {
		t.Fatalf("native inventory target results = %+v, want one", data.Results)
	}
	if inventory.Feature.FeatureType != native.FeatureType {
		t.Errorf("inventory feature type = %q, want exact native %q", inventory.Feature.FeatureType, native.FeatureType)
	}
	if data.Results[0].Target.FeatureType != native.FeatureType {
		t.Errorf("round-trip target feature type = %q, want exact native %q", data.Results[0].Target.FeatureType, native.FeatureType)
	}
	if fixture.sender.calls.Load() != 1 {
		t.Errorf("native inventory target contacted correlated peer %d times, want 1", fixture.sender.calls.Load())
	}
}

func TestIssue89LowercaseMeasurementIsNotAnAliasAndNeverContactsPeer(t *testing.T) {
	fixture := newIssue83RawBridgeFixture(t)
	lowercase := fixture.locators[0]
	lowercase.FeatureType = strings.ToLower(lowercase.FeatureType)
	if lowercase.FeatureType == fixture.locators[0].FeatureType {
		t.Fatal("lowercase alias fixture did not change native casing")
	}

	_, inventoryTerminal := fixture.bridge.featuresGet(
		context.Background(),
		issue83FacadeAuthorization(eebusraw.ToolV1FeaturesGet),
		eebusraw.FeaturesGetRequestV1{Target: lowercase},
	)
	if inventoryTerminal == nil || inventoryTerminal.Code != eebusraw.ErrorCodeV1NotFound {
		t.Errorf("lowercase features.get error = %+v, want controlled not_found", inventoryTerminal)
	}

	_, readTerminal := fixture.bridge.featuresDataGet(
		context.Background(),
		issue83FacadeAuthorization(eebusraw.ToolV1FeaturesDataGet),
		eebusraw.FeatureDataGetRequestV1{
			Targets:   []eebusraw.FeatureTargetV1{issue83TargetFromLocator(lowercase)},
			TimeoutMS: 1000,
		},
	)
	if readTerminal == nil || readTerminal.Code != eebusraw.ErrorCodeV1NotFound {
		t.Errorf("lowercase features.data.get error = %+v, want controlled not_found", readTerminal)
	}
	if fixture.sender.calls.Load() != 0 {
		t.Errorf("lowercase alias contacted correlated peer %d times, want 0", fixture.sender.calls.Load())
	}
}

func TestIssue89AcquireProvisionsOneGenericClientBetweenSetupAndStart(t *testing.T) {
	for _, test := range []struct {
		name      string
		preseeded bool
	}{
		{name: "empty CEM"},
		{name: "preseeded CEM", preseeded: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			local := issue89LocalDevice(true)
			var preseededAddress string
			if test.preseeded {
				cem := local.EntityForType(spinemodel.EntityTypeTypeCEM)
				source := cem.GetOrAddFeature(
					spinemodel.FeatureTypeTypeGeneric,
					spinemodel.RoleTypeClient,
				)
				preseededAddress = source.Address().String()
			}
			service := newIssue89RuntimeService(local)
			backend, err := issue89AcquireRuntime(t, service)
			if err != nil {
				t.Fatalf("acquireRuntime() error = %v", err)
			}

			sources := issue89GenericClientSources(local)
			if service.setupSourceCount != issue89BoolCount(test.preseeded) {
				t.Errorf(
					"Setup observed %d Generic/client features, want %d",
					service.setupSourceCount,
					issue89BoolCount(test.preseeded),
				)
			}
			if len(sources) != 1 {
				t.Errorf("post-Setup acquisition provisioned %d Generic/client features, want exactly 1", len(sources))
			}
			if test.preseeded && len(sources) == 1 && sources[0].Address().String() != preseededAddress {
				t.Errorf(
					"preseeded Generic/client address changed from %q to %q",
					preseededAddress,
					sources[0].Address().String(),
				)
			}
			select {
			case <-service.started:
				t.Fatal("service started during acquisition")
			default:
			}

			ctx, cancel := context.WithCancel(context.Background())
			runDone := make(chan error, 1)
			go func() {
				runDone <- backend.Run(ctx, func([]byte) {})
			}()
			select {
			case <-service.started:
			case <-time.After(time.Second):
				t.Fatal("runtime service did not start")
			}
			cancel()
			if err := <-runDone; err != nil {
				t.Fatalf("Run() cancellation error = %v", err)
			}
			if service.startSourceCount != 1 {
				t.Errorf("Start observed %d Generic/client features, want exactly 1", service.startSourceCount)
			}
			if err := backend.Close(); err != nil {
				t.Fatal(err)
			}
			if got := service.lifecycle(); !reflect.DeepEqual(got, []string{"setup", "start", "shutdown"}) {
				t.Errorf("service lifecycle = %v, want [setup start shutdown]", got)
			}
			if service.shutdownCount() != 1 {
				t.Errorf("service shutdown count = %d, want 1", service.shutdownCount())
			}
		})
	}
}

func TestIssue89AcquireFailsClosedWithoutUsableLocalReadSource(t *testing.T) {
	var typedNil *spine.DeviceLocal
	realWithCEM := issue89LocalDevice(true)
	cem := realWithCEM.EntityForType(spinemodel.EntityTypeTypeCEM)
	nilSourceEntity := &issue89NilSourceEntity{EntityLocalInterface: cem}
	nilSourceDevice := &issue89NilSourceDevice{
		DeviceLocalInterface: realWithCEM,
		cem:                  nilSourceEntity,
	}

	for _, test := range []struct {
		name  string
		local spineapi.DeviceLocalInterface
	}{
		{name: "nil local device", local: nil},
		{name: "typed-nil local device", local: typedNil},
		{name: "missing CEM", local: issue89LocalDevice(false)},
		{name: "nil source feature", local: nilSourceDevice},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := newIssue89RuntimeService(test.local)
			backend, err := issue89AcquireRuntime(t, service)
			shutdownsBeforeCleanup := service.shutdownCount()

			if err == nil {
				t.Errorf("acquireRuntime() error = nil, want fail-closed source error")
			} else {
				var sourceError *runtimeLocalReadSourceError
				if !errors.As(err, &sourceError) {
					t.Errorf("acquireRuntime() error = %T %v, want typed local source error", err, err)
				}
			}
			if backend != nil {
				t.Errorf("acquireRuntime() backend = %T, want nil", backend)
			}
			if service.setupCount() != 1 {
				t.Errorf("service Setup count = %d, want 1", service.setupCount())
			}
			if service.startCount() != 0 {
				t.Errorf("service Start count = %d, want 0", service.startCount())
			}
			if shutdownsBeforeCleanup != 1 {
				t.Errorf("service shutdown count at failed acquisition = %d, want 1", shutdownsBeforeCleanup)
			}

			if backend != nil {
				if closeErr := backend.Close(); closeErr != nil {
					t.Fatalf("cleanup Close() error = %v", closeErr)
				}
			}
		})
	}
}

func TestIssue89RuntimeReadUsesGenericClientWithoutProjectingLocalSource(t *testing.T) {
	local := issue89LocalDevice(true)
	service := newIssue89RuntimeService(local)
	backend, err := issue89AcquireRuntime(t, service)
	if err != nil {
		t.Fatalf("acquireRuntime() error = %v", err)
	}
	implementation, ok := backend.(*serviceBackend)
	if !ok {
		t.Fatalf("runtime backend = %T, want service backend", backend)
	}

	sources := issue89GenericClientSources(local)
	if len(sources) != 1 {
		t.Errorf("runtime acquisition provisioned %d Generic/client features, want 1", len(sources))
	}

	remoteSKI := issue89RemoteSKI
	shipID := "issue89-vr940"
	remoteAddress := "issue89-remote-device"
	sender := &issue83RoundTripper{}
	base := issue83RemoteDevice(t, local, remoteSKI, remoteAddress, sender, 11)
	remote := &issue83MutableRemote{DeviceRemoteInterface: base, sender: sender}
	local.AddRemoteDeviceForSki(remoteSKI, remote)

	var requestMu sync.Mutex
	var received spineapi.CorrelatedRequest
	sender.roundTrip = func(
		_ context.Context,
		request spineapi.CorrelatedRequest,
	) (spineapi.CorrelatedResponse, error) {
		requestMu.Lock()
		received = request
		requestMu.Unlock()
		return issue83MeasurementReply(request, 89, 11, 215), nil
	}

	implementation.handler.RemoteSKIConnected(eebusServiceWithFeatureGraph(t, remoteSKI), remoteSKI)
	implementation.handler.ServiceShipIDUpdate(remoteSKI, shipID)

	ctx, cancel := context.WithCancel(context.Background())
	updates := make(chan []byte, 1)
	runDone := make(chan error, 1)
	go func() {
		runDone <- backend.Run(ctx, func(payload []byte) {
			select {
			case updates <- append([]byte(nil), payload...):
			default:
			}
		})
	}()
	published := waitRuntimePayload(t, updates)
	select {
	case <-service.started:
	case <-time.After(time.Second):
		t.Fatal("runtime service did not start")
	}

	target := issue83TargetFromLocator(issue83Locator(remoteSKI, shipID, remoteAddress, 11))
	data, terminal := implementation.FeaturesDataGet(
		context.Background(),
		issue83FacadeAuthorization(eebusraw.ToolV1FeaturesDataGet),
		eebusraw.FeatureDataGetRequestV1{
			Targets:   []eebusraw.FeatureTargetV1{target},
			TimeoutMS: 1000,
		},
	)
	if terminal != nil {
		t.Errorf("runtime native READ error = %+v", terminal)
	}
	if len(data.Results) != 1 {
		t.Errorf("runtime native READ results = %+v, want one", data.Results)
	}
	if sender.calls.Load() != 1 {
		t.Errorf("runtime native READ contacted correlated peer %d times, want 1", sender.calls.Load())
	}

	requestMu.Lock()
	captured := received
	requestMu.Unlock()
	if captured.Classifier != spinemodel.CmdClassifierTypeRead {
		t.Errorf("correlated peer classifier = %q, want READ", captured.Classifier)
	}
	if len(sources) == 1 && !reflect.DeepEqual(captured.Source, *sources[0].Address()) {
		t.Errorf(
			"correlated READ source = %s, want provisioned Generic/client %s",
			captured.Source.String(),
			sources[0].Address().String(),
		)
	}

	localAddress := string(*local.Address())
	graph := implementation.handler.reducer.Snapshot()
	if issue89GraphContainsDeviceAddress(graph, localAddress) {
		t.Errorf("remote topology/projection contains local READ source device %q", localAddress)
	}
	if bytes.Contains(published, []byte(localAddress)) {
		t.Errorf("public runtime snapshot contains local READ source device %q: %s", localAddress, published)
	}

	cancel()
	if err := <-runDone; err != nil {
		t.Fatalf("Run() cancellation error = %v", err)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
	if service.shutdownCount() != 1 {
		t.Errorf("service shutdown count = %d, want 1", service.shutdownCount())
	}
}

const issue89RemoteSKI = "0000000000000000000000000000000000000089"

type issue89RuntimeService struct {
	mu sync.Mutex

	localDevice      spineapi.DeviceLocalInterface
	started          chan struct{}
	setupCalls       int
	startCalls       int
	shutdowns        int
	setupSourceCount int
	startSourceCount int
	events           []string
}

func newIssue89RuntimeService(local spineapi.DeviceLocalInterface) *issue89RuntimeService {
	return &issue89RuntimeService{
		localDevice: local,
		started:     make(chan struct{}),
	}
}

func (service *issue89RuntimeService) Setup() error {
	service.mu.Lock()
	service.setupCalls++
	service.setupSourceCount = len(issue89GenericClientSources(service.localDevice))
	service.events = append(service.events, "setup")
	service.mu.Unlock()
	return nil
}

func (service *issue89RuntimeService) Start() {
	service.mu.Lock()
	service.startCalls++
	service.startSourceCount = len(issue89GenericClientSources(service.localDevice))
	service.events = append(service.events, "start")
	if service.startCalls == 1 {
		close(service.started)
	}
	service.mu.Unlock()
}

func (service *issue89RuntimeService) Shutdown() {
	service.mu.Lock()
	service.shutdowns++
	service.events = append(service.events, "shutdown")
	service.mu.Unlock()
}

func (*issue89RuntimeService) RegisterRemoteSKI(string) {}

func (*issue89RuntimeService) LocalService() *shipapi.ServiceDetails { return nil }

func (service *issue89RuntimeService) LocalDevice() spineapi.DeviceLocalInterface {
	return service.localDevice
}

func (service *issue89RuntimeService) setupCount() int {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.setupCalls
}

func (service *issue89RuntimeService) startCount() int {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.startCalls
}

func (service *issue89RuntimeService) shutdownCount() int {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.shutdowns
}

func (service *issue89RuntimeService) lifecycle() []string {
	service.mu.Lock()
	defer service.mu.Unlock()
	return append([]string(nil), service.events...)
}

func issue89AcquireRuntime(t *testing.T, service runtimeService) (Backend, error) {
	t.Helper()
	certificate, err := shipcert.CreateCertificate("", "Helianthus", "RO", "issue89")
	if err != nil {
		t.Fatal(err)
	}
	localSKI := certificateSKI(t, certificate)
	return acquireRuntime(context.Background(), RuntimeConfig{
		StateRoot:  runtimeTestStateRoot(t),
		Interface:  "fixture-interface",
		ListenPort: 4711,
		Remotes:    []RuntimeRemote{{SKI: issue89RemoteSKI}},
	}, runtimeDependencies{
		loadMaterial: func(context.Context, string) (runtimeMaterial, error) {
			return runtimeMaterial{
				certificate: certificate,
				localSKI:    localSKI,
				nodeToken:   runtimeTestNodeToken,
				pretrusted:  map[string]bool{issue89RemoteSKI: true},
			}, nil
		},
		newService: func(
			RuntimeConfig,
			runtimeMaterial,
			eebusapi.ServiceReaderInterface,
		) (runtimeService, error) {
			return service, nil
		},
		subscribeSPINEEvents: runtimeTestSPINEEventSubscriber,
		now:                  time.Now,
	})
}

func issue89LocalDevice(withCEM bool) *spine.DeviceLocal {
	local := spine.NewDeviceLocal(
		"Helianthus",
		"eebusreg",
		"issue89",
		"issue89",
		"issue89-local-device",
		spinemodel.DeviceTypeTypeEnergyManagementSystem,
		spinemodel.NetworkManagementFeatureSetTypeSmart,
	)
	if !withCEM {
		return local
	}
	cem := spine.NewEntityLocal(
		local,
		spinemodel.EntityTypeTypeCEM,
		[]spinemodel.AddressEntityType{1},
		time.Second,
	)
	local.AddEntity(cem)
	return local
}

func issue89GenericClientSources(
	local spineapi.DeviceLocalInterface,
) []spineapi.FeatureLocalInterface {
	if issue89NilInterface(local) {
		return nil
	}
	cem := local.EntityForType(spinemodel.EntityTypeTypeCEM)
	if issue89NilInterface(cem) {
		return nil
	}
	result := make([]spineapi.FeatureLocalInterface, 0, 1)
	for _, feature := range cem.Features() {
		if issue89NilInterface(feature) {
			continue
		}
		if feature.Type() == spinemodel.FeatureTypeTypeGeneric &&
			feature.Role() == spinemodel.RoleTypeClient {
			result = append(result, feature)
		}
	}
	return result
}

func issue89NilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func issue89BoolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}

func issue89GraphContainsDeviceAddress(graph []runtimeGraphObservation, address string) bool {
	for _, remote := range graph {
		for _, device := range remote.Devices {
			if device.Address == address {
				return true
			}
			for _, entity := range device.Entities {
				if entity.DeviceAddress == address {
					return true
				}
				for _, feature := range entity.Features {
					if feature.DeviceAddress == address {
						return true
					}
				}
			}
		}
	}
	return false
}

type issue89NilSourceEntity struct {
	spineapi.EntityLocalInterface
}

func (*issue89NilSourceEntity) GetOrAddFeature(
	spinemodel.FeatureTypeType,
	spinemodel.RoleType,
) spineapi.FeatureLocalInterface {
	return nil
}

func (*issue89NilSourceEntity) FeatureOfTypeAndRole(
	spinemodel.FeatureTypeType,
	spinemodel.RoleType,
) spineapi.FeatureLocalInterface {
	return nil
}

type issue89NilSourceDevice struct {
	spineapi.DeviceLocalInterface
	cem spineapi.EntityLocalInterface
}

func (device *issue89NilSourceDevice) EntityForType(
	entityType spinemodel.EntityTypeType,
) spineapi.EntityLocalInterface {
	if entityType == spinemodel.EntityTypeTypeCEM {
		return device.cem
	}
	return device.DeviceLocalInterface.EntityForType(entityType)
}

func (device *issue89NilSourceDevice) Entity(
	address []spinemodel.AddressEntityType,
) spineapi.EntityLocalInterface {
	if device.cem != nil && reflect.DeepEqual(device.cem.Address().Entity, address) {
		return device.cem
	}
	return device.DeviceLocalInterface.Entity(address)
}

func (device *issue89NilSourceDevice) Entities() []spineapi.EntityLocalInterface {
	entities := append([]spineapi.EntityLocalInterface(nil), device.DeviceLocalInterface.Entities()...)
	for index, entity := range entities {
		if entity.EntityType() == spinemodel.EntityTypeTypeCEM {
			entities[index] = device.cem
		}
	}
	return entities
}
