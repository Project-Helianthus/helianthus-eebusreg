package eebusfacade

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
	spine "github.com/Project-Helianthus/helianthus-spine-go/spine"
)

type remoteSubscriptionCall struct {
	source      string
	destination string
	serverType  spinemodel.FeatureTypeType
}

// The raw feature fixtures admit readable Measurement server features, which now sends a
// subscription request through the synthetic round-tripper.
func (sender *issue83RoundTripper) Subscribe(
	*spinemodel.FeatureAddressType,
	*spinemodel.FeatureAddressType,
	spinemodel.FeatureTypeType,
) (*spinemodel.MsgCounterType, error) {
	counter := spinemodel.MsgCounterType(1)
	return &counter, nil
}

func (sender *issue83RoundTripper) Unsubscribe(
	*spinemodel.FeatureAddressType,
	*spinemodel.FeatureAddressType,
) (*spinemodel.MsgCounterType, error) {
	counter := spinemodel.MsgCounterType(1)
	return &counter, nil
}

type remoteSubscriptionSender struct {
	*issue83RoundTripper
	mu           sync.Mutex
	subscribed   []remoteSubscriptionCall
	unsubscribed []remoteSubscriptionCall
	failures     map[spinemodel.FeatureTypeType]int
	sequence     []string
}

func (sender *remoteSubscriptionSender) Subscribe(
	source *spinemodel.FeatureAddressType,
	destination *spinemodel.FeatureAddressType,
	serverType spinemodel.FeatureTypeType,
) (*spinemodel.MsgCounterType, error) {
	sender.mu.Lock()
	defer sender.mu.Unlock()
	if sender.failures[serverType] > 0 {
		sender.failures[serverType]--
		return nil, errRemoteSubscriptionTestFailure
	}
	sender.subscribed = append(sender.subscribed, remoteSubscriptionCall{
		source: source.String(), destination: destination.String(), serverType: serverType,
	})
	sender.sequence = append(sender.sequence, "subscribe "+source.String())
	counter := spinemodel.MsgCounterType(len(sender.subscribed))
	return &counter, nil
}

func (sender *remoteSubscriptionSender) Notify(
	_ *spinemodel.FeatureAddressType,
	_ *spinemodel.FeatureAddressType,
	cmd spinemodel.CmdType,
) (*spinemodel.MsgCounterType, error) {
	sender.mu.Lock()
	defer sender.mu.Unlock()
	if discovery := cmd.NodeManagementDetailedDiscoveryData; discovery != nil {
		for _, feature := range discovery.FeatureInformation {
			if feature.Description != nil && feature.Description.FeatureAddress != nil &&
				feature.Description.Role != nil && *feature.Description.Role == spinemodel.RoleTypeClient {
				sender.sequence = append(sender.sequence, "announce "+feature.Description.FeatureAddress.String())
			}
		}
	}
	counter := spinemodel.MsgCounterType(1000 + len(sender.sequence))
	return &counter, nil
}

func (sender *remoteSubscriptionSender) order() []string {
	sender.mu.Lock()
	defer sender.mu.Unlock()
	return append([]string(nil), sender.sequence...)
}

func (sender *remoteSubscriptionSender) Unsubscribe(
	source *spinemodel.FeatureAddressType,
	destination *spinemodel.FeatureAddressType,
) (*spinemodel.MsgCounterType, error) {
	sender.mu.Lock()
	defer sender.mu.Unlock()
	sender.unsubscribed = append(sender.unsubscribed, remoteSubscriptionCall{
		source: source.String(), destination: destination.String(),
	})
	counter := spinemodel.MsgCounterType(len(sender.unsubscribed))
	return &counter, nil
}

func (sender *remoteSubscriptionSender) calls() ([]remoteSubscriptionCall, []remoteSubscriptionCall) {
	sender.mu.Lock()
	defer sender.mu.Unlock()
	return append([]remoteSubscriptionCall(nil), sender.subscribed...),
		append([]remoteSubscriptionCall(nil), sender.unsubscribed...)
}

type remoteSubscriptionTestError string

func (failure remoteSubscriptionTestError) Error() string { return string(failure) }

const errRemoteSubscriptionTestFailure = remoteSubscriptionTestError("subscription refused")

type remoteSubscriptionFeatureSpec struct {
	address     uint
	featureType spinemodel.FeatureTypeType
	role        spinemodel.RoleType
	function    spinemodel.FunctionType
	readable    bool
}

type remoteSubscriptionFixture struct {
	bridge    *rawFeatureRuntimeBridge
	local     *spine.DeviceLocal
	remote    *issue83MutableRemote
	base      *spine.DeviceRemote
	entity    *spine.EntityRemote
	sender    *remoteSubscriptionSender
	remoteSKI string
	shipID    string
	address   string
	clock     *remoteSubscriptionClock
}

type remoteSubscriptionClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *remoteSubscriptionClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *remoteSubscriptionClock) set(value time.Time) {
	clock.mu.Lock()
	clock.now = value
	clock.mu.Unlock()
}

func remoteSubscriptionMeasurementSpec() []remoteSubscriptionFeatureSpec {
	return []remoteSubscriptionFeatureSpec{{
		address:     11,
		featureType: spinemodel.FeatureTypeTypeMeasurement,
		role:        spinemodel.RoleTypeServer,
		function:    spinemodel.FunctionTypeMeasurementListData,
		readable:    true,
	}}
}

func newRemoteSubscriptionFixture(
	t *testing.T,
	remoteSKI string,
	specs []remoteSubscriptionFeatureSpec,
) *remoteSubscriptionFixture {
	t.Helper()
	local := spine.NewDeviceLocal(
		"brand", "model", "serial", "code", "local-device",
		spinemodel.DeviceTypeTypeEnergyManagementSystem,
		spinemodel.NetworkManagementFeatureSetTypeSmart,
	)
	localEntity := spine.NewEntityLocal(
		local, spinemodel.EntityTypeTypeCEM, []spinemodel.AddressEntityType{1}, time.Second,
	)
	localEntity.GetOrAddFeature(spinemodel.FeatureTypeTypeGeneric, spinemodel.RoleTypeClient)
	local.AddEntity(localEntity)

	sender := &remoteSubscriptionSender{
		issue83RoundTripper: &issue83RoundTripper{},
		failures:            make(map[spinemodel.FeatureTypeType]int),
	}
	address := "remote-device"
	base := spine.NewDeviceRemote(local, remoteSKI, sender)
	deviceAddress := spinemodel.AddressDeviceType(address)
	deviceType := spinemodel.DeviceTypeTypeHeatgenerationSystem
	base.UpdateDevice(&spinemodel.NetworkManagementDeviceDescriptionDataType{
		DeviceAddress: &spinemodel.DeviceAddressType{Device: &deviceAddress},
		DeviceType:    &deviceType,
	})
	entity := spine.NewEntityRemote(
		base, spinemodel.EntityTypeTypeHeatPumpAppliance, []spinemodel.AddressEntityType{1},
	)
	for _, spec := range specs {
		entity.AddFeature(remoteSubscriptionFeature(entity, spec))
	}
	base.AddEntity(entity)
	remote := &issue83MutableRemote{DeviceRemoteInterface: base, sender: sender}
	local.AddRemoteDeviceForSki(remoteSKI, remote)

	issuer, err := newRawReadTokenIssuer(bytes.Repeat([]byte{0x5a}, 32))
	if err != nil {
		t.Fatal(err)
	}
	clock := &remoteSubscriptionClock{now: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)}
	bridge := newRawFeatureRuntimeBridge(local, func() uint64 { return 9 }, clock.Now, issuer)
	return &remoteSubscriptionFixture{
		bridge: bridge, local: local, remote: remote, base: base, entity: entity, sender: sender,
		remoteSKI: remoteSKI, shipID: "synthetic-ship-id", address: address, clock: clock,
	}
}

func remoteSubscriptionFeature(
	entity *spine.EntityRemote,
	spec remoteSubscriptionFeatureSpec,
) *spine.FeatureRemote {
	feature := spine.NewFeatureRemote(spec.address, entity, spec.featureType, spec.role)
	function := spec.function
	operations := &spinemodel.PossibleOperationsType{}
	if spec.readable {
		operations.Read = &spinemodel.PossibleOperationsReadType{}
	} else {
		operations.Write = &spinemodel.PossibleOperationsWriteType{}
	}
	feature.SetOperations([]spinemodel.FunctionPropertyType{{
		Function:           &function,
		PossibleOperations: operations,
	}})
	return feature
}

func (fixture *remoteSubscriptionFixture) admit(t *testing.T, generation uint64) {
	t.Helper()
	if err := fixture.bridge.beginConnectionGeneration(fixture.remoteSKI, generation); err != nil {
		t.Fatal(err)
	}
	if err := fixture.bridge.admitRemote(fixture.remoteSKI, fixture.shipID, generation, fixture.remote); err != nil {
		t.Fatal(err)
	}
}

func (fixture *remoteSubscriptionFixture) feature(address uint) *spine.FeatureRemote {
	featureAddress := spinemodel.AddressFeatureType(address)
	deviceAddress := spinemodel.AddressDeviceType(fixture.address)
	feature := fixture.base.FeatureByAddress(&spinemodel.FeatureAddressType{
		Device: &deviceAddress, Entity: []spinemodel.AddressEntityType{1}, Feature: &featureAddress,
	})
	if feature == nil {
		return nil
	}
	return feature.(*spine.FeatureRemote)
}

func (fixture *remoteSubscriptionFixture) target(
	address uint,
	featureType spinemodel.FeatureTypeType,
	function spinemodel.FunctionType,
) eebusraw.FeatureTargetV1 {
	return eebusraw.FeatureTargetV1{
		RemoteSKI:      fixture.remoteSKI,
		SHIPID:         fixture.shipID,
		DeviceAddress:  fixture.address,
		EntityAddress:  []uint64{1},
		FeatureAddress: uint64(address),
		FeatureType:    string(featureType),
		FeatureRole:    eebusraw.FeatureRoleV1Server,
		Function:       string(function),
		Operation:      eebusraw.OperationV1Read,
	}
}

func remoteSubscriptionMeasurementData(value int64) *spinemodel.MeasurementListDataType {
	measurementID := spinemodel.MeasurementIdType(3)
	number := spinemodel.NumberType(value)
	scale := spinemodel.ScaleType(-1)
	return &spinemodel.MeasurementListDataType{MeasurementData: []spinemodel.MeasurementDataType{{
		MeasurementId: &measurementID,
		Value:         &spinemodel.ScaledNumberType{Number: &number, Scale: &scale},
	}}}
}

func remoteSubscriptionNotify(
	t *testing.T,
	fixture *remoteSubscriptionFixture,
	feature *spine.FeatureRemote,
	function spinemodel.FunctionType,
	data any,
) spineapi.EventPayload {
	t.Helper()
	if _, failure := feature.UpdateData(true, function, data, nil, nil); failure != nil {
		t.Fatalf("UpdateData() = %v", failure)
	}
	classifier := spinemodel.CmdClassifierTypeNotify
	return spineapi.EventPayload{
		Ski:           fixture.remoteSKI,
		EventType:     spineapi.EventTypeDataChange,
		ChangeType:    spineapi.ElementChangeUpdate,
		Feature:       feature,
		Device:        fixture.remote,
		Entity:        feature.Entity(),
		Function:      function,
		CmdClassifier: &classifier,
		Data:          data,
	}
}

func newRemoteSubscriptionHandler(
	t *testing.T,
	fixture *remoteSubscriptionFixture,
	trusted func(string) bool,
) *runtimeServiceHandler {
	t.Helper()
	handler, err := newRuntimeServiceHandler(RuntimeConfig{}, strings.Repeat("e", 40), fixture.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	handler.bindRawFeatureRuntime(fixture.bridge)
	handler.projectionLivenessAllowed = trusted
	handler.activateSPINEEvents(&fakeRuntimeService{started: make(chan struct{}), localDevice: fixture.local})
	t.Cleanup(func() {
		handler.deactivateSPINEEvents()
		handler.waitForSPINEEvents()
	})
	return handler
}

func remoteSubscriptionAuthorization() eebusraw.ReadAuthorizationV1 {
	return eebusraw.ReadAuthorizationV1{
		PrincipalClass: "operator",
		Scope:          eebusraw.AuthScopeV1RawRead,
		Tool:           eebusraw.ToolV1FeaturesDataGet,
		MaskTier:       eebusraw.MaskTierRaw,
	}
}

func TestRemoteSubscriptionTrustedRemoteIsSubscribedOnceAndServesNotifiedData(t *testing.T) {
	fixture := newRemoteSubscriptionFixture(t, strings.Repeat("a", 40), remoteSubscriptionMeasurementSpec())
	handler := newRemoteSubscriptionHandler(t, fixture, func(string) bool { return true })
	backend := &serviceBackend{rawFeatures: fixture.bridge, handler: handler}
	target := fixture.target(11, spinemodel.FeatureTypeTypeMeasurement, spinemodel.FunctionTypeMeasurementListData)

	_, terminal := backend.FeatureNotifiedDataGet(context.Background(), remoteSubscriptionAuthorization(), target)
	if terminal == nil || terminal.Code != eebusraw.ErrorCodeV1NotFound || !terminal.Retriable {
		t.Fatalf("read before admission = %+v, want retriable not_found", terminal)
	}

	fixture.admit(t, 4)
	if err := fixture.bridge.refreshRemote(fixture.remoteSKI, fixture.shipID, 4, fixture.remote); err != nil {
		t.Fatal(err)
	}
	subscribed, unsubscribed := fixture.sender.calls()
	if len(subscribed) != 1 || len(unsubscribed) != 0 {
		t.Fatalf("subscriptions = %+v, unsubscriptions = %+v, want exactly one subscription", subscribed, unsubscribed)
	}
	if subscribed[0].serverType != spinemodel.FeatureTypeTypeMeasurement ||
		subscribed[0].destination != fixture.feature(11).Address().String() {
		t.Fatalf("subscription = %+v", subscribed[0])
	}
	client := fixture.local.EntityForType(spinemodel.EntityTypeTypeCEM).
		FeatureOfTypeAndRole(spinemodel.FeatureTypeTypeMeasurement, spinemodel.RoleTypeClient)
	if client == nil || subscribed[0].source != client.Address().String() {
		t.Fatalf("subscription source = %q, want local Measurement client", subscribed[0].source)
	}

	_, terminal = backend.FeatureNotifiedDataGet(context.Background(), remoteSubscriptionAuthorization(), target)
	if terminal == nil || terminal.Code != eebusraw.ErrorCodeV1NotFound {
		t.Fatalf("read before notification = %+v, want not_found", terminal)
	}

	notifiedAt := time.Date(2030, 1, 2, 3, 5, 0, 0, time.UTC)
	fixture.clock.set(notifiedAt)
	handler.HandleEvent(remoteSubscriptionNotify(
		t, fixture, fixture.feature(11), spinemodel.FunctionTypeMeasurementListData,
		remoteSubscriptionMeasurementData(215),
	))

	observation, terminal := backend.FeatureNotifiedDataGet(context.Background(), remoteSubscriptionAuthorization(), target)
	if terminal != nil {
		t.Fatalf("read after notification error = %+v", terminal)
	}
	if !reflect.DeepEqual(observation.Target, target) ||
		observation.Runtime != (eebusraw.RuntimeBindingV1{RuntimeEpoch: 9, ConnectionGeneration: 4}) ||
		!observation.ReceivedAt.Equal(notifiedAt) || !observation.DataTimestamp.Equal(notifiedAt) ||
		observation.Source != eebusraw.ObservationSourceV1Live ||
		observation.RawResponse.Classifier != "NOTIFY" ||
		observation.RawResponse.Function != string(spinemodel.FunctionTypeMeasurementListData) ||
		observation.ReadToken != (eebusraw.ReadTokenV1{}) {
		t.Fatalf("notified observation = %#v", observation)
	}
	if got := remoteSubscriptionMeasurementNumber(t, observation); got != 215 {
		t.Fatalf("notified value number = %v, want 215", got)
	}
	hash, err := observation.ComputeDataHash()
	if err != nil || hash != observation.DataHash {
		t.Fatalf("data hash = %q, computed %q (%v)", observation.DataHash, hash, err)
	}

	later := notifiedAt.Add(time.Minute)
	fixture.clock.set(later)
	handler.HandleEvent(remoteSubscriptionNotify(
		t, fixture, fixture.feature(11), spinemodel.FunctionTypeMeasurementListData,
		remoteSubscriptionMeasurementData(216),
	))
	observation, terminal = backend.FeatureNotifiedDataGet(context.Background(), remoteSubscriptionAuthorization(), target)
	if terminal != nil || !observation.ReceivedAt.Equal(later) ||
		remoteSubscriptionMeasurementNumber(t, observation) != 216 {
		t.Fatalf("second notification = %+v / %+v", observation.ReceivedAt, terminal)
	}
	if subscribed, _ := fixture.sender.calls(); len(subscribed) != 1 {
		t.Fatalf("notifications changed subscription count to %d", len(subscribed))
	}
}

func remoteSubscriptionMeasurementNumber(t *testing.T, observation eebusraw.ReadObservationV1) float64 {
	t.Helper()
	document, ok := observation.Value.Value().(map[string]any)
	if !ok {
		t.Fatalf("value = %#v", observation.Value.Value())
	}
	list, ok := document["measurementData"].([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("measurementData = %#v", document["measurementData"])
	}
	item, ok := list[0].(map[string]any)
	if !ok {
		t.Fatalf("measurement item = %#v", list[0])
	}
	value, ok := item["value"].(map[string]any)
	if !ok {
		t.Fatalf("measurement value = %#v", item["value"])
	}
	return remoteSubscriptionNumber(t, value["number"])
}

func remoteSubscriptionNumber(t *testing.T, value any) float64 {
	t.Helper()
	number, err := strconv.ParseFloat(fmt.Sprint(value), 64)
	if err != nil {
		t.Fatalf("number = %#v (%T): %v", value, value, err)
	}
	return number
}

func TestRemoteSubscriptionUntrustedRemoteIsNeitherSubscribedNorServed(t *testing.T) {
	fixture := newRemoteSubscriptionFixture(t, strings.Repeat("b", 40), remoteSubscriptionMeasurementSpec())
	var trusted sync.Map
	handler := newRemoteSubscriptionHandler(t, fixture, func(ski string) bool {
		allowed, _ := trusted.Load(ski)
		return allowed == true
	})
	backend := &serviceBackend{rawFeatures: fixture.bridge, handler: handler}
	target := fixture.target(11, spinemodel.FeatureTypeTypeMeasurement, spinemodel.FunctionTypeMeasurementListData)

	if err := fixture.bridge.beginConnectionGeneration(fixture.remoteSKI, 4); err != nil {
		t.Fatal(err)
	}
	handler.mu.Lock()
	graph := handler.newRemoteObservation(fixture.remoteSKI)
	graph.ShipID, graph.SessionIndex, graph.SessionState = fixture.shipID, 4, "connected"
	handler.observations[fixture.remoteSKI] = graph
	handler.mu.Unlock()
	handler.ServiceShipIDUpdate(fixture.remoteSKI, fixture.shipID)
	handler.HandleEvent(remoteSubscriptionNotify(
		t, fixture, fixture.feature(11), spinemodel.FunctionTypeMeasurementListData,
		remoteSubscriptionMeasurementData(215),
	))
	if subscribed, _ := fixture.sender.calls(); len(subscribed) != 0 {
		t.Fatalf("untrusted remote was subscribed: %+v", subscribed)
	}
	if _, terminal := backend.FeatureNotifiedDataGet(
		context.Background(), remoteSubscriptionAuthorization(), target,
	); terminal == nil || terminal.Code != eebusraw.ErrorCodeV1NotFound {
		t.Fatalf("untrusted read = %+v, want not_found", terminal)
	}

	trusted.Store(fixture.remoteSKI, true)
	handler.ServiceShipIDUpdate(fixture.remoteSKI, fixture.shipID)
	if subscribed, _ := fixture.sender.calls(); len(subscribed) != 1 {
		t.Fatalf("trusted remote subscriptions = %+v, want one", subscribed)
	}
	handler.HandleEvent(remoteSubscriptionNotify(
		t, fixture, fixture.feature(11), spinemodel.FunctionTypeMeasurementListData,
		remoteSubscriptionMeasurementData(217),
	))
	if _, terminal := backend.FeatureNotifiedDataGet(
		context.Background(), remoteSubscriptionAuthorization(), target,
	); terminal != nil {
		t.Fatalf("trusted read error = %+v", terminal)
	}

	trusted.Delete(fixture.remoteSKI)
	handler.HandleEvent(remoteSubscriptionNotify(
		t, fixture, fixture.feature(11), spinemodel.FunctionTypeMeasurementListData,
		remoteSubscriptionMeasurementData(218),
	))
	if _, unsubscribed := fixture.sender.calls(); len(unsubscribed) != 1 ||
		unsubscribed[0].destination != fixture.feature(11).Address().String() {
		t.Fatalf("trust withdrawal unsubscriptions = %+v, want the Measurement subscription", unsubscribed)
	}
	if _, terminal := backend.FeatureNotifiedDataGet(
		context.Background(), remoteSubscriptionAuthorization(), target,
	); terminal == nil || terminal.Code != eebusraw.ErrorCodeV1NotFound {
		t.Fatalf("read after trust withdrawal = %+v, want not_found", terminal)
	}
	trusted.Store(fixture.remoteSKI, true)
	if _, terminal := backend.FeatureNotifiedDataGet(
		context.Background(), remoteSubscriptionAuthorization(), target,
	); terminal == nil || terminal.Code != eebusraw.ErrorCodeV1NotFound {
		t.Fatalf("later grant served the pre-withdrawal data: %+v", terminal)
	}
}

func newRemoteSubscriptionTrustSwitch(
	t *testing.T,
	fixture *remoteSubscriptionFixture,
) (*runtimeServiceHandler, *serviceBackend, *sync.Map) {
	t.Helper()
	trusted := &sync.Map{}
	trusted.Store(fixture.remoteSKI, true)
	handler := newRemoteSubscriptionHandler(t, fixture, func(ski string) bool {
		allowed, _ := trusted.Load(ski)
		return allowed == true
	})
	return handler, &serviceBackend{rawFeatures: fixture.bridge, handler: handler}, trusted
}

func TestRemoteSubscriptionTrustWithdrawalSeenByReadDropsSubscriptionAndData(t *testing.T) {
	fixture := newRemoteSubscriptionFixture(t, strings.Repeat("1", 40), remoteSubscriptionMeasurementSpec())
	handler, backend, trusted := newRemoteSubscriptionTrustSwitch(t, fixture)
	target := fixture.target(11, spinemodel.FeatureTypeTypeMeasurement, spinemodel.FunctionTypeMeasurementListData)
	fixture.admit(t, 4)
	handler.HandleEvent(remoteSubscriptionNotify(
		t, fixture, fixture.feature(11), spinemodel.FunctionTypeMeasurementListData,
		remoteSubscriptionMeasurementData(215),
	))
	if _, terminal := backend.FeatureNotifiedDataGet(context.Background(), remoteSubscriptionAuthorization(), target); terminal != nil {
		t.Fatalf("trusted read error = %+v", terminal)
	}

	trusted.Delete(fixture.remoteSKI)
	if _, terminal := backend.FeatureNotifiedDataGet(
		context.Background(), remoteSubscriptionAuthorization(), target,
	); terminal == nil || terminal.Code != eebusraw.ErrorCodeV1NotFound {
		t.Fatalf("read after trust withdrawal = %+v, want not_found", terminal)
	}
	if _, unsubscribed := fixture.sender.calls(); len(unsubscribed) != 1 {
		t.Fatalf("trust withdrawal unsubscriptions = %+v, want one", unsubscribed)
	}
	trusted.Store(fixture.remoteSKI, true)
	if _, terminal := backend.FeatureNotifiedDataGet(
		context.Background(), remoteSubscriptionAuthorization(), target,
	); terminal == nil || terminal.Code != eebusraw.ErrorCodeV1NotFound {
		t.Fatalf("later grant served the pre-withdrawal data: %+v", terminal)
	}

	if err := fixture.bridge.refreshRemote(fixture.remoteSKI, fixture.shipID, 4, fixture.remote); err != nil {
		t.Fatal(err)
	}
	if subscribed, _ := fixture.sender.calls(); len(subscribed) != 2 {
		t.Fatalf("subscriptions after the later grant = %+v, want a new request", subscribed)
	}
	handler.HandleEvent(remoteSubscriptionNotify(
		t, fixture, fixture.feature(11), spinemodel.FunctionTypeMeasurementListData,
		remoteSubscriptionMeasurementData(219),
	))
	observation, terminal := backend.FeatureNotifiedDataGet(context.Background(), remoteSubscriptionAuthorization(), target)
	if terminal != nil || remoteSubscriptionMeasurementNumber(t, observation) != 219 {
		t.Fatalf("read after resubscription = %+v", terminal)
	}
}

func TestRemoteSubscriptionRemoteDisappearingWhileUntrustedClearsState(t *testing.T) {
	fixture := newRemoteSubscriptionFixture(t, strings.Repeat("2", 40), remoteSubscriptionMeasurementSpec())
	handler, backend, trusted := newRemoteSubscriptionTrustSwitch(t, fixture)
	target := fixture.target(11, spinemodel.FeatureTypeTypeMeasurement, spinemodel.FunctionTypeMeasurementListData)
	fixture.admit(t, 4)
	handler.HandleEvent(remoteSubscriptionNotify(
		t, fixture, fixture.feature(11), spinemodel.FunctionTypeMeasurementListData,
		remoteSubscriptionMeasurementData(215),
	))
	if _, terminal := backend.FeatureNotifiedDataGet(context.Background(), remoteSubscriptionAuthorization(), target); terminal != nil {
		t.Fatalf("trusted read error = %+v", terminal)
	}

	trusted.Delete(fixture.remoteSKI)
	handler.HandleEvent(spineapi.EventPayload{
		Ski:        fixture.remoteSKI,
		EventType:  spineapi.EventTypeDeviceChange,
		ChangeType: spineapi.ElementChangeRemove,
		Device:     fixture.remote,
	})
	if _, unsubscribed := fixture.sender.calls(); len(unsubscribed) != 0 {
		t.Fatalf("disappeared remote was sent unsubscriptions: %+v", unsubscribed)
	}
	trusted.Store(fixture.remoteSKI, true)
	if _, terminal := backend.FeatureNotifiedDataGet(
		context.Background(), remoteSubscriptionAuthorization(), target,
	); terminal == nil || terminal.Code != eebusraw.ErrorCodeV1NotFound {
		t.Fatalf("read after the remote disappeared while untrusted = %+v, want not_found", terminal)
	}
	if active := fixture.activeSubscriptions(); active != 0 {
		t.Fatalf("disappeared remote keeps %d subscriptions", active)
	}
}

func (fixture *remoteSubscriptionFixture) activeSubscriptions() int {
	fixture.bridge.mu.Lock()
	defer fixture.bridge.mu.Unlock()
	return len(fixture.bridge.remoteSubscriptions.active)
}

func (fixture *remoteSubscriptionFixture) pendingResults() int {
	fixture.bridge.mu.Lock()
	defer fixture.bridge.mu.Unlock()
	return len(fixture.bridge.remoteSubscriptions.pending)
}

func remoteSubscriptionEventually(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (fixture *remoteSubscriptionFixture) deliverResult(
	t *testing.T,
	counter spinemodel.MsgCounterType,
	errorNumber spinemodel.ErrorNumberType,
) {
	t.Helper()
	failure := fixture.local.NodeManagement().HandleMessage(&spineapi.Message{
		RequestHeader: &spinemodel.HeaderType{MsgCounterReference: &counter},
		CmdClassifier: spinemodel.CmdClassifierTypeResult,
		Cmd:           spinemodel.CmdType{ResultData: &spinemodel.ResultDataType{ErrorNumber: &errorNumber}},
		FeatureRemote: fixture.feature(11),
		EntityRemote:  fixture.entity,
		DeviceRemote:  fixture.remote,
	})
	if failure != nil {
		t.Fatalf("result handling error = %v", failure)
	}
}

func TestRemoteSubscriptionRefusedResultIsRetriedOnNextDiscovery(t *testing.T) {
	fixture := newRemoteSubscriptionFixture(t, strings.Repeat("3", 40), remoteSubscriptionMeasurementSpec())
	fixture.admit(t, 4)
	if subscribed, _ := fixture.sender.calls(); len(subscribed) != 1 {
		t.Fatalf("subscriptions = %+v, want one", subscribed)
	}
	fixture.deliverResult(t, 1, spinemodel.ErrorNumberTypeGeneralError)
	remoteSubscriptionEventually(t, "the refused subscription is released", func() bool {
		return fixture.activeSubscriptions() == 0
	})
	if err := fixture.bridge.refreshRemote(fixture.remoteSKI, fixture.shipID, 4, fixture.remote); err != nil {
		t.Fatal(err)
	}
	if subscribed, _ := fixture.sender.calls(); len(subscribed) != 2 {
		t.Fatalf("subscriptions after refusal = %+v, want a second request", subscribed)
	}

	fixture.deliverResult(t, 2, spinemodel.ErrorNumberTypeNoError)
	remoteSubscriptionEventually(t, "the accepted result is consumed", func() bool {
		return fixture.pendingResults() == 0
	})
	if err := fixture.bridge.refreshRemote(fixture.remoteSKI, fixture.shipID, 4, fixture.remote); err != nil {
		t.Fatal(err)
	}
	if subscribed, _ := fixture.sender.calls(); len(subscribed) != 2 || fixture.activeSubscriptions() != 1 {
		t.Fatalf("accepted subscription was requested again: %+v", subscribed)
	}
}

func TestRemoteSubscriptionRefusalBeforeRegistrationIsRetried(t *testing.T) {
	fixture := newRemoteSubscriptionFixture(t, strings.Repeat("4", 40), remoteSubscriptionMeasurementSpec())
	refused := spinemodel.ErrorNumberTypeGeneralError
	fixture.bridge.recordRemoteSubscriptionResult(spineapi.ResponseMessage{
		MsgCounterReference: 1,
		Data:                &spinemodel.ResultDataType{ErrorNumber: &refused},
		DeviceRemote:        fixture.remote,
	})
	fixture.admit(t, 4)
	if active := fixture.activeSubscriptions(); active != 0 {
		t.Fatalf("subscription refused before registration stays active (%d)", active)
	}
	if err := fixture.bridge.refreshRemote(fixture.remoteSKI, fixture.shipID, 4, fixture.remote); err != nil {
		t.Fatal(err)
	}
	if subscribed, _ := fixture.sender.calls(); len(subscribed) != 2 {
		t.Fatalf("subscriptions = %+v, want a retry", subscribed)
	}
}

func TestRemoteSubscriptionAnnouncesNewClientBeforeRequest(t *testing.T) {
	fixture := newRemoteSubscriptionFixture(t, strings.Repeat("5", 40), remoteSubscriptionMeasurementSpec())
	information := spine.NewEntityRemote(
		fixture.base, spinemodel.EntityTypeTypeDeviceInformation, []spinemodel.AddressEntityType{0},
	)
	management := spine.NewFeatureRemote(0, information, spinemodel.FeatureTypeTypeNodeManagement, spinemodel.RoleTypeSpecial)
	information.AddFeature(management)
	fixture.base.AddEntity(information)
	managementType := spinemodel.FeatureTypeTypeNodeManagement
	if err := fixture.local.SubscriptionManager().AddSubscription(fixture.remote, spinemodel.SubscriptionManagementRequestCallType{
		ClientAddress:     management.Address(),
		ServerAddress:     fixture.local.NodeManagement().Address(),
		ServerFeatureType: &managementType,
	}); err != nil {
		t.Fatal(err)
	}

	fixture.admit(t, 4)
	client := fixture.local.EntityForType(spinemodel.EntityTypeTypeCEM).
		FeatureOfTypeAndRole(spinemodel.FeatureTypeTypeMeasurement, spinemodel.RoleTypeClient)
	if client == nil {
		t.Fatal("no local Measurement client")
	}
	address := client.Address().String()
	order := fixture.sender.order()
	announced, requested := -1, -1
	for index, step := range order {
		switch step {
		case "announce " + address:
			if announced < 0 {
				announced = index
			}
		case "subscribe " + address:
			requested = index
		}
	}
	if announced < 0 || requested < 0 || announced > requested {
		t.Fatalf("sequence = %v, want the Measurement client announced before its subscription request", order)
	}

	if err := fixture.bridge.refreshRemote(fixture.remoteSKI, fixture.shipID, 4, fixture.remote); err != nil {
		t.Fatal(err)
	}
	if again := fixture.sender.order(); len(again) != len(order) {
		t.Fatalf("refresh without a new client sent %v", again[len(order):])
	}
}

func TestRemoteSubscriptionUndecodableNotifyIsReportedAsDecodeError(t *testing.T) {
	fixture := newRemoteSubscriptionFixture(t, strings.Repeat("6", 40), []remoteSubscriptionFeatureSpec{{
		address:     13,
		featureType: spinemodel.FeatureTypeTypeElectricalConnection,
		role:        spinemodel.RoleTypeServer,
		function:    spinemodel.FunctionTypeElectricalConnectionPermittedValueSetListData,
		readable:    true,
	}})
	handler := newRemoteSubscriptionHandler(t, fixture, func(string) bool { return true })
	backend := &serviceBackend{rawFeatures: fixture.bridge, handler: handler}
	fixture.admit(t, 4)
	connection := spinemodel.ElectricalConnectionIdType(1)
	parameter := spinemodel.ElectricalConnectionParameterIdType(2)
	number := spinemodel.NumberType(6)
	handler.HandleEvent(remoteSubscriptionNotify(
		t, fixture, fixture.feature(13), spinemodel.FunctionTypeElectricalConnectionPermittedValueSetListData,
		&spinemodel.ElectricalConnectionPermittedValueSetListDataType{
			ElectricalConnectionPermittedValueSetData: []spinemodel.ElectricalConnectionPermittedValueSetDataType{{
				ElectricalConnectionId: &connection,
				ParameterId:            &parameter,
				PermittedValueSet: []spinemodel.ScaledNumberSetType{{
					Range: []spinemodel.ScaledNumberRangeType{{
						Min: &spinemodel.ScaledNumberType{Number: &number},
						Max: &spinemodel.ScaledNumberType{Number: &number},
					}},
				}},
			}},
		},
	))
	target := fixture.target(
		13, spinemodel.FeatureTypeTypeElectricalConnection,
		spinemodel.FunctionTypeElectricalConnectionPermittedValueSetListData,
	)
	_, terminal := backend.FeatureNotifiedDataGet(context.Background(), remoteSubscriptionAuthorization(), target)
	if terminal == nil || terminal.Code != eebusraw.ErrorCodeV1DecodeError {
		t.Fatalf("read of an undecodable notification = %+v, want decode_error", terminal)
	}
}

func TestRemoteSubscriptionCoversEveryListedReadableServerFeatureType(t *testing.T) {
	listed := []spinemodel.FeatureTypeType{
		spinemodel.FeatureTypeTypeMeasurement,
		spinemodel.FeatureTypeTypeSetpoint,
		spinemodel.FeatureTypeTypeHvac,
		spinemodel.FeatureTypeTypeDeviceClassification,
		spinemodel.FeatureTypeTypeElectricalConnection,
		spinemodel.FeatureTypeTypeDeviceConfiguration,
		spinemodel.FeatureTypeTypeDeviceDiagnosis,
		spinemodel.FeatureTypeTypeLoadControl,
	}
	specs := make([]remoteSubscriptionFeatureSpec, 0, len(listed)+3)
	for index, featureType := range listed {
		specs = append(specs, remoteSubscriptionFeatureSpec{
			address: uint(20 + index), featureType: featureType, role: spinemodel.RoleTypeServer,
			function: spinemodel.FunctionTypeMeasurementListData, readable: true,
		})
	}
	specs = append(specs,
		remoteSubscriptionFeatureSpec{
			address: 40, featureType: spinemodel.FeatureTypeTypeMeasurement, role: spinemodel.RoleTypeClient,
			function: spinemodel.FunctionTypeMeasurementListData, readable: true,
		},
		remoteSubscriptionFeatureSpec{
			address: 41, featureType: spinemodel.FeatureTypeTypeBill, role: spinemodel.RoleTypeServer,
			function: spinemodel.FunctionTypeBillListData, readable: true,
		},
		remoteSubscriptionFeatureSpec{
			address: 42, featureType: spinemodel.FeatureTypeTypeSetpoint, role: spinemodel.RoleTypeServer,
			function: spinemodel.FunctionTypeSetpointListData, readable: false,
		},
	)
	fixture := newRemoteSubscriptionFixture(t, strings.Repeat("c", 40), specs)
	fixture.admit(t, 4)

	subscribed, _ := fixture.sender.calls()
	got := make([]string, 0, len(subscribed))
	for _, call := range subscribed {
		got = append(got, call.destination+"="+string(call.serverType))
	}
	want := make([]string, 0, len(listed))
	for index, featureType := range listed {
		want = append(want, fixture.feature(uint(20+index)).Address().String()+"="+string(featureType))
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("subscriptions = %v, want %v", got, want)
	}
	for index, featureType := range listed {
		client := fixture.local.EntityForType(spinemodel.EntityTypeTypeCEM).
			FeatureOfTypeAndRole(featureType, spinemodel.RoleTypeClient)
		if client == nil || !client.HasSubscriptionToRemote(fixture.feature(uint(20+index)).Address()) {
			t.Fatalf("local %s client holds no subscription", featureType)
		}
	}
}

func TestRemoteSubscriptionDroppedWhenFeatureOrRemoteDisappears(t *testing.T) {
	specs := append(remoteSubscriptionMeasurementSpec(), remoteSubscriptionFeatureSpec{
		address: 12, featureType: spinemodel.FeatureTypeTypeDeviceDiagnosis, role: spinemodel.RoleTypeServer,
		function: spinemodel.FunctionTypeDeviceDiagnosisStateData, readable: true,
	})
	fixture := newRemoteSubscriptionFixture(t, strings.Repeat("d", 40), specs)
	handler := newRemoteSubscriptionHandler(t, fixture, func(string) bool { return true })
	backend := &serviceBackend{rawFeatures: fixture.bridge, handler: handler}
	fixture.admit(t, 4)
	measurement := fixture.target(11, spinemodel.FeatureTypeTypeMeasurement, spinemodel.FunctionTypeMeasurementListData)
	handler.HandleEvent(remoteSubscriptionNotify(
		t, fixture, fixture.feature(11), spinemodel.FunctionTypeMeasurementListData,
		remoteSubscriptionMeasurementData(215),
	))
	if _, terminal := backend.FeatureNotifiedDataGet(context.Background(), remoteSubscriptionAuthorization(), measurement); terminal != nil {
		t.Fatalf("read error = %+v", terminal)
	}

	diagnosis := fixture.feature(12).Address().String()
	fixture.remote.hideFeature(12)
	if err := fixture.bridge.refreshRemote(fixture.remoteSKI, fixture.shipID, 4, fixture.remote); err != nil {
		t.Fatal(err)
	}
	subscribed, unsubscribed := fixture.sender.calls()
	if len(subscribed) != 2 || len(unsubscribed) != 1 || unsubscribed[0].destination != diagnosis {
		t.Fatalf("after feature removal subscriptions = %+v, unsubscriptions = %+v", subscribed, unsubscribed)
	}
	if _, terminal := backend.FeatureNotifiedDataGet(context.Background(), remoteSubscriptionAuthorization(), measurement); terminal != nil {
		t.Fatalf("remaining feature lost its notified data: %+v", terminal)
	}

	fixture.bridge.retireRemote(fixture.remoteSKI, 4)
	if _, terminal := backend.FeatureNotifiedDataGet(context.Background(), remoteSubscriptionAuthorization(), measurement); terminal == nil || terminal.Code != eebusraw.ErrorCodeV1NotFound {
		t.Fatalf("read after the remote disappeared = %+v, want not_found", terminal)
	}
	handler.HandleEvent(remoteSubscriptionNotify(
		t, fixture, fixture.feature(11), spinemodel.FunctionTypeMeasurementListData,
		remoteSubscriptionMeasurementData(216),
	))
	if _, terminal := backend.FeatureNotifiedDataGet(context.Background(), remoteSubscriptionAuthorization(), measurement); terminal == nil {
		t.Fatal("notify after the remote disappeared was stored")
	}

	fixture.admit(t, 5)
	subscribed, _ = fixture.sender.calls()
	if len(subscribed) != 3 {
		t.Fatalf("new connection subscriptions = %+v, want one more", subscribed)
	}
	if _, terminal := backend.FeatureNotifiedDataGet(context.Background(), remoteSubscriptionAuthorization(), measurement); terminal == nil {
		t.Fatal("previous connection data survived into the new connection")
	}
}

func TestRemoteSubscriptionFailedRequestIsRetriedOnNextDiscovery(t *testing.T) {
	fixture := newRemoteSubscriptionFixture(t, strings.Repeat("f", 40), remoteSubscriptionMeasurementSpec())
	fixture.sender.failures[spinemodel.FeatureTypeTypeMeasurement] = 1
	fixture.admit(t, 4)
	if subscribed, _ := fixture.sender.calls(); len(subscribed) != 0 {
		t.Fatalf("refused subscription recorded: %+v", subscribed)
	}
	if err := fixture.bridge.refreshRemote(fixture.remoteSKI, fixture.shipID, 4, fixture.remote); err != nil {
		t.Fatal(err)
	}
	if subscribed, _ := fixture.sender.calls(); len(subscribed) != 1 {
		t.Fatalf("retry subscriptions = %+v, want one", subscribed)
	}
}

func TestRemoteSubscriptionReadKeyVariesEveryTargetField(t *testing.T) {
	fixture := newRemoteSubscriptionFixture(t, strings.Repeat("a", 40), remoteSubscriptionMeasurementSpec())
	handler := newRemoteSubscriptionHandler(t, fixture, func(string) bool { return true })
	backend := &serviceBackend{rawFeatures: fixture.bridge, handler: handler}
	fixture.admit(t, 4)
	handler.HandleEvent(remoteSubscriptionNotify(
		t, fixture, fixture.feature(11), spinemodel.FunctionTypeMeasurementListData,
		remoteSubscriptionMeasurementData(215),
	))
	base := fixture.target(11, spinemodel.FeatureTypeTypeMeasurement, spinemodel.FunctionTypeMeasurementListData)
	if _, terminal := backend.FeatureNotifiedDataGet(context.Background(), remoteSubscriptionAuthorization(), base); terminal != nil {
		t.Fatalf("base read error = %+v", terminal)
	}
	mutations := map[string]func(*eebusraw.FeatureTargetV1){
		"remote_ski":      func(target *eebusraw.FeatureTargetV1) { target.RemoteSKI = strings.Repeat("b", 40) },
		"ship_id":         func(target *eebusraw.FeatureTargetV1) { target.SHIPID = "other-ship-id" },
		"device_address":  func(target *eebusraw.FeatureTargetV1) { target.DeviceAddress = "other-device" },
		"entity_address":  func(target *eebusraw.FeatureTargetV1) { target.EntityAddress = []uint64{2} },
		"feature_address": func(target *eebusraw.FeatureTargetV1) { target.FeatureAddress = 12 },
		"feature_type":    func(target *eebusraw.FeatureTargetV1) { target.FeatureType = "Setpoint" },
		"feature_role":    func(target *eebusraw.FeatureTargetV1) { target.FeatureRole = eebusraw.FeatureRoleV1Client },
		"function":        func(target *eebusraw.FeatureTargetV1) { target.Function = "measurementDescriptionListData" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			target := base.Clone()
			mutate(&target)
			_, terminal := backend.FeatureNotifiedDataGet(context.Background(), remoteSubscriptionAuthorization(), target)
			if terminal == nil || terminal.Code != eebusraw.ErrorCodeV1NotFound {
				t.Fatalf("mutated %s read = %+v, want not_found", name, terminal)
			}
		})
	}
	write := base.Clone()
	write.Operation = eebusraw.OperationV1Write
	if _, terminal := backend.FeatureNotifiedDataGet(context.Background(), remoteSubscriptionAuthorization(), write); terminal == nil ||
		terminal.Code != eebusraw.ErrorCodeV1UnsupportedOperation {
		t.Fatalf("operation write read = %+v, want unsupported_operation", terminal)
	}
	auth := remoteSubscriptionAuthorization()
	auth.Tool = eebusraw.ToolV1FeaturesGet
	if _, terminal := backend.FeatureNotifiedDataGet(context.Background(), auth, base); terminal == nil ||
		terminal.Code != eebusraw.ErrorCodeV1PermissionDenied {
		t.Fatalf("wrong tool read = %+v, want permission_denied", terminal)
	}
}

func TestRemoteSubscriptionFullReadKeepsItsBehaviour(t *testing.T) {
	fixture := newIssue83RawBridgeFixture(t)
	target := issue83TargetFromLocator(fixture.locators[0])
	data, terminal := fixture.bridge.featuresDataGet(
		context.Background(),
		issue83FacadeAuthorization(eebusraw.ToolV1FeaturesDataGet),
		eebusraw.FeatureDataGetRequestV1{Targets: []eebusraw.FeatureTargetV1{target}},
	)
	if terminal != nil || len(data.Results) != 1 ||
		data.Results[0].Source != eebusraw.ObservationSourceV1Live ||
		data.Results[0].RawRequest.Classifier != "READ" || data.Results[0].ReadToken.ReadToken == "" {
		t.Fatalf("full READ = %+v / %+v", data, terminal)
	}
	if fixture.sender.calls.Load() != 1 {
		t.Fatalf("full READ round trips = %d, want 1", fixture.sender.calls.Load())
	}
}
