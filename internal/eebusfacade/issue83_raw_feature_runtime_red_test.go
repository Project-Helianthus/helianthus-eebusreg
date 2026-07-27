package eebusfacade

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	executor "github.com/Project-Helianthus/helianthus-eebus-go/features/executor"
	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
	spine "github.com/Project-Helianthus/helianthus-spine-go/spine"
)

func TestIssue83FeaturesGetReturnsExactInventoryThenCache(t *testing.T) {
	fixture := newIssue83RawBridgeFixture(t)
	description := spinemodel.DescriptionType("VR940 measurement server")
	fixture.features[0].SetDescription(&description)

	data, terminal := fixture.bridge.featuresGet(
		context.Background(),
		issue83FacadeAuthorization(eebusraw.ToolV1FeaturesGet),
		eebusraw.FeaturesGetRequestV1{Target: fixture.locators[0]},
	)
	if terminal != nil {
		t.Fatalf("featuresGet() error = %+v", terminal)
	}
	if data.Feature.RemoteSKI != fixture.remoteSKI ||
		data.Feature.SHIPID != fixture.shipID ||
		data.Feature.DeviceAddress != fixture.remoteAddress ||
		!reflect.DeepEqual(data.Feature.EntityAddress, []uint64{1}) ||
		data.Feature.FeatureAddress != 11 ||
		data.Feature.FeatureType != "measurement" ||
		data.Feature.FeatureRole != eebusraw.FeatureRoleV1Server {
		t.Fatalf("inventory feature = %+v", data.Feature)
	}
	if data.Description != "VR940 measurement server" {
		t.Fatalf("inventory description = %q", data.Description)
	}
	if data.Source != eebusraw.ObservationSourceV1Live ||
		data.Runtime != (eebusraw.RuntimeBindingV1{RuntimeEpoch: 9, ConnectionGeneration: 4}) {
		t.Fatalf("inventory source/runtime = %q %+v", data.Source, data.Runtime)
	}
	if len(data.Functions) != 2 ||
		data.Functions[0].Function != "measurementDescriptionListData" ||
		data.Functions[1].Function != "measurementListData" {
		t.Fatalf("inventory functions are not canonical: %+v", data.Functions)
	}
	if data.Functions[0].PossibleOperations.Read != true ||
		data.Functions[0].PossibleOperations.Write != false ||
		data.Functions[1].PossibleOperations.Read != true ||
		data.Functions[1].PossibleOperations.Write != true {
		t.Fatalf("inventory operations = %+v", data.Functions)
	}
	for _, function := range data.Functions {
		if function.Changeable != eebusraw.ChangeabilityV1Unknown ||
			function.Constraints.Status != eebusraw.ConstraintStatusV1Unknown {
			t.Fatalf("inventory invented changeability/constraints: %+v", function)
		}
	}
	computed, err := data.ComputeDataHash()
	if err != nil {
		t.Fatal(err)
	}
	if data.DataHash != computed {
		t.Fatalf("inventory hash = %q, want %q", data.DataHash, computed)
	}
	if fixture.sender.calls.Load() != 0 {
		t.Fatalf("inventory contacted remote %d times", fixture.sender.calls.Load())
	}

	fixture.bridge.retireRemote(fixture.remoteSKI, 4)
	cached, terminal := fixture.bridge.featuresGet(
		context.Background(),
		issue83FacadeAuthorization(eebusraw.ToolV1FeaturesGet),
		eebusraw.FeaturesGetRequestV1{Target: fixture.locators[0]},
	)
	if terminal != nil {
		t.Fatalf("cached featuresGet() error = %+v", terminal)
	}
	if cached.Source != eebusraw.ObservationSourceV1Cache ||
		cached.Runtime != data.Runtime ||
		cached.DataHash != data.DataHash {
		t.Fatalf("cached inventory = %+v, live = %+v", cached, data)
	}
}

func TestIssue83BoundedReadsPreserveOrderAndExplicitPartialResult(t *testing.T) {
	fixture := newIssue83RawBridgeFixture(t)
	fixture.sender.roundTrip = func(
		ctx context.Context,
		request spineapi.CorrelatedRequest,
	) (spineapi.CorrelatedResponse, error) {
		feature := uint64(*request.Destination.Feature)
		fixture.sender.mu.Lock()
		fixture.sender.order = append(fixture.sender.order, feature)
		fixture.sender.mu.Unlock()
		if feature == 12 {
			return spineapi.CorrelatedResponse{}, context.DeadlineExceeded
		}
		return issue83MeasurementReply(request, 41, 11, 215), nil
	}
	targets := []eebusraw.FeatureTargetV1{
		issue83TargetFromLocator(fixture.locators[0]),
		issue83TargetFromLocator(fixture.locators[1]),
	}
	data, terminal := fixture.bridge.featuresDataGet(
		context.Background(),
		issue83FacadeAuthorization(eebusraw.ToolV1FeaturesDataGet),
		eebusraw.FeatureDataGetRequestV1{Targets: targets, TimeoutMS: 1000},
	)
	if terminal == nil || terminal.Code != eebusraw.ErrorCodeV1PartialResult {
		t.Fatalf("featuresDataGet() error = %+v, want partial_result", terminal)
	}
	if data.Complete || len(data.Results) != 1 || len(data.Failures) != 1 {
		t.Fatalf("partial result = %+v", data)
	}
	if data.Results[0].Target.FeatureAddress != 11 ||
		data.Failures[0].TargetIndex != 1 ||
		data.Failures[0].Target.FeatureAddress != 12 ||
		data.Failures[0].Error.Code != eebusraw.ErrorCodeV1Timeout {
		t.Fatalf("ordered partial result = %+v", data)
	}
	fixture.sender.mu.Lock()
	order := append([]uint64(nil), fixture.sender.order...)
	fixture.sender.mu.Unlock()
	if !reflect.DeepEqual(order, []uint64{11, 12}) {
		t.Fatalf("dispatch order = %v, want [11 12]", order)
	}

	observation := data.Results[0]
	if observation.RawRequest.Classifier != "READ" ||
		observation.RawResponse.Classifier != "REPLY" ||
		observation.RawRequest.Function != "measurementListData" ||
		observation.RawResponse.Function != "measurementListData" ||
		observation.RawRequest.CorrelationKey != observation.RawResponse.CorrelationKey ||
		observation.RawRequest.CorrelationKey != 41 {
		t.Fatalf("raw protocol translation = request:%+v response:%+v", observation.RawRequest, observation.RawResponse)
	}
	if observation.Source != eebusraw.ObservationSourceV1Live ||
		observation.Runtime != (eebusraw.RuntimeBindingV1{RuntimeEpoch: 9, ConnectionGeneration: 4}) ||
		!observation.RequestedAt.Before(observation.ReceivedAt) && !observation.RequestedAt.Equal(observation.ReceivedAt) ||
		observation.DataTimestamp != observation.ReceivedAt {
		t.Fatalf("observation binding/timestamps = %+v", observation)
	}
	rawValue := observation.Value.Value().(map[string]any)
	measurements := rawValue["measurementData"].([]any)
	if len(measurements) != 1 ||
		measurements[0].(map[string]any)["measurementId"] != int64(11) {
		t.Fatalf("typed response value = %#v", rawValue)
	}
	computed, err := observation.ComputeDataHash()
	if err != nil {
		t.Fatal(err)
	}
	if observation.DataHash != computed ||
		observation.ReadToken.ReadToken == "" ||
		observation.ReadToken.BindingHash == "" ||
		observation.ReadToken.Reusable ||
		!observation.ReadToken.ExpiresAt.After(observation.ReceivedAt) {
		t.Fatalf("observation commitments/token = %+v", observation)
	}
}

func TestIssue83ReadBoundsUnsupportedAndEmptyDataAreZeroOrExplicit(t *testing.T) {
	fixture := newIssue83RawBridgeFixture(t)
	auth := issue83FacadeAuthorization(eebusraw.ToolV1FeaturesDataGet)

	for _, count := range []int{0, eebusraw.MaximumReadTargetsV1 + 1} {
		targets := make([]eebusraw.FeatureTargetV1, count)
		for index := range targets {
			targets[index] = issue83TargetFromLocator(fixture.locators[0])
		}
		before := fixture.sender.calls.Load()
		_, terminal := fixture.bridge.featuresDataGet(
			context.Background(),
			auth,
			eebusraw.FeatureDataGetRequestV1{Targets: targets, TimeoutMS: 1000},
		)
		if terminal == nil || terminal.Code != eebusraw.ErrorCodeV1InvalidArgument {
			t.Fatalf("target count %d error = %+v", count, terminal)
		}
		if fixture.sender.calls.Load() != before {
			t.Fatalf("target count %d contacted remote", count)
		}
	}

	writeTarget := issue83TargetFromLocator(fixture.locators[0])
	writeTarget.Operation = eebusraw.OperationV1Write
	before := fixture.sender.calls.Load()
	_, terminal := fixture.bridge.featuresDataGet(
		context.Background(),
		auth,
		eebusraw.FeatureDataGetRequestV1{Targets: []eebusraw.FeatureTargetV1{writeTarget}, TimeoutMS: 1000},
	)
	if terminal == nil || terminal.Code != eebusraw.ErrorCodeV1UnsupportedOperation {
		t.Fatalf("WRITE-through-read error = %+v", terminal)
	}
	if fixture.sender.calls.Load() != before {
		t.Fatal("unsupported operation contacted remote")
	}

	fixture.sender.roundTrip = func(
		_ context.Context,
		request spineapi.CorrelatedRequest,
	) (spineapi.CorrelatedResponse, error) {
		return issue83EmptyMeasurementReply(request, 51), nil
	}
	data, terminal := fixture.bridge.featuresDataGet(
		context.Background(),
		auth,
		eebusraw.FeatureDataGetRequestV1{
			Targets:   []eebusraw.FeatureTargetV1{issue83TargetFromLocator(fixture.locators[0])},
			TimeoutMS: 1000,
		},
	)
	if terminal == nil || terminal.Code != eebusraw.ErrorCodeV1DecodeError ||
		data.Complete || len(data.Results) != 0 {
		t.Fatalf("empty reply result=%+v error=%+v", data, terminal)
	}
}

func TestIssue83ExactRuntimeRejectsReplacementAndSenderSubstitutionWithoutSend(t *testing.T) {
	t.Run("same address replacement", func(t *testing.T) {
		fixture := newIssue83RawBridgeFixture(t)
		target := issue83TargetFromLocator(fixture.locators[0])
		binding, request, err := fixture.bridge.exactBindingAndRequest(target)
		if err != nil {
			t.Fatal(err)
		}

		replacementSender := &issue83RoundTripper{}
		replacement := issue83RemoteDevice(
			t,
			fixture.local,
			strings.Repeat("b", 40),
			fixture.remoteAddress,
			replacementSender,
			11,
		)
		if err := fixture.bridge.admitRemote(
			strings.Repeat("b", 40),
			"replacement-ship-id",
			5,
			replacement,
		); err != nil {
			t.Fatal(err)
		}

		_, err = fixture.bridge.RoundTripIfCurrent(context.Background(), binding, request)
		if !errors.Is(err, executor.ErrExactRemoteBindingMismatch) {
			t.Fatalf("RoundTripIfCurrent() error = %v", err)
		}
		var bindingError *executor.ExactRemoteBindingError
		if !errors.As(err, &bindingError) ||
			bindingError.Failure != executor.ExactRemoteBindingIdentityMismatch {
			t.Fatalf("binding error = %#v", bindingError)
		}
		if fixture.sender.calls.Load() != 0 || replacementSender.calls.Load() != 0 {
			t.Fatalf("replacement sent frames: old=%d new=%d", fixture.sender.calls.Load(), replacementSender.calls.Load())
		}
	})

	t.Run("sender capability substitution", func(t *testing.T) {
		fixture := newIssue83RawBridgeFixture(t)
		target := issue83TargetFromLocator(fixture.locators[0])
		binding, request, err := fixture.bridge.exactBindingAndRequest(target)
		if err != nil {
			t.Fatal(err)
		}
		replacementSender := &issue83RoundTripper{}
		fixture.remote.setSender(replacementSender)

		_, err = fixture.bridge.RoundTripIfCurrent(context.Background(), binding, request)
		if !errors.Is(err, executor.ErrExactRemoteBindingMismatch) {
			t.Fatalf("RoundTripIfCurrent() error = %v", err)
		}
		var bindingError *executor.ExactRemoteBindingError
		if !errors.As(err, &bindingError) ||
			bindingError.Failure != executor.ExactRemoteBindingGenerationMismatch {
			t.Fatalf("binding error = %#v", bindingError)
		}
		if fixture.sender.calls.Load() != 0 || replacementSender.calls.Load() != 0 {
			t.Fatalf("substitution sent frames: old=%d new=%d", fixture.sender.calls.Load(), replacementSender.calls.Load())
		}
	})
}

func TestIssue83ReadTokenIsDeterministicAndPurposeBound(t *testing.T) {
	target := issue83TargetFromLocator(issue83Locator(strings.Repeat("a", 40), "ship-a", "remote-device", 11))
	runtime := eebusraw.RuntimeBindingV1{RuntimeEpoch: 9, ConnectionGeneration: 4}
	auth := issue83FacadeAuthorization(eebusraw.ToolV1FeaturesDataGet)
	requestHash := eebusraw.HashV1("sha256:" + strings.Repeat("1", 64))
	beforeHash := eebusraw.HashV1("sha256:" + strings.Repeat("2", 64))
	receivedAt := time.Unix(1000, 0).UTC()
	key := bytes.Repeat([]byte{0x6b}, 32)
	issuer, err := newRawReadTokenIssuer(key)
	if err != nil {
		t.Fatal(err)
	}

	first, err := issuer.issue(auth, target, runtime, requestHash, beforeHash, receivedAt)
	if err != nil {
		t.Fatal(err)
	}
	second, err := issuer.issue(auth, target, runtime, requestHash, beforeHash, receivedAt)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("deterministic token mismatch: first=%+v second=%+v", first, second)
	}

	tests := []struct {
		name    string
		auth    eebusraw.ReadAuthorizationV1
		target  eebusraw.FeatureTargetV1
		runtime eebusraw.RuntimeBindingV1
		request eebusraw.HashV1
		before  eebusraw.HashV1
	}{
		{
			name: "principal",
			auth: func() eebusraw.ReadAuthorizationV1 {
				value := auth
				value.PrincipalClass = "different-owner"
				return value
			}(),
			target: target, runtime: runtime, request: requestHash, before: beforeHash,
		},
		{
			name: "tool",
			auth: func() eebusraw.ReadAuthorizationV1 {
				value := auth
				value.Tool = eebusraw.ToolV1FeaturesGet
				return value
			}(),
			target: target, runtime: runtime, request: requestHash, before: beforeHash,
		},
		{
			name: "tier",
			auth: func() eebusraw.ReadAuthorizationV1 {
				value := auth
				value.MaskTier = eebusraw.MaskTierRedacted
				return value
			}(),
			target: target, runtime: runtime, request: requestHash, before: beforeHash,
		},
		{
			name: "target",
			auth: auth,
			target: func() eebusraw.FeatureTargetV1 {
				value := target
				value.FeatureAddress++
				return value
			}(),
			runtime: runtime, request: requestHash, before: beforeHash,
		},
		{
			name:    "epoch",
			auth:    auth,
			target:  target,
			runtime: eebusraw.RuntimeBindingV1{RuntimeEpoch: 10, ConnectionGeneration: 4},
			request: requestHash,
			before:  beforeHash,
		},
		{
			name:    "generation",
			auth:    auth,
			target:  target,
			runtime: eebusraw.RuntimeBindingV1{RuntimeEpoch: 9, ConnectionGeneration: 5},
			request: requestHash,
			before:  beforeHash,
		},
		{
			name: "request shape", auth: auth, target: target, runtime: runtime,
			request: eebusraw.HashV1("sha256:" + strings.Repeat("3", 64)), before: beforeHash,
		},
		{
			name: "before image", auth: auth, target: target, runtime: runtime,
			request: requestHash, before: eebusraw.HashV1("sha256:" + strings.Repeat("4", 64)),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed, err := issuer.issue(
				test.auth,
				test.target,
				test.runtime,
				test.request,
				test.before,
				receivedAt,
			)
			if err != nil {
				t.Fatal(err)
			}
			if changed == first ||
				changed.ReadToken == first.ReadToken ||
				changed.BindingHash == first.BindingHash {
				t.Fatalf("%s substitution preserved token binding: %+v", test.name, changed)
			}
		})
	}

	otherIssuer, err := newRawReadTokenIssuer(bytes.Repeat([]byte{0x6c}, 32))
	if err != nil {
		t.Fatal(err)
	}
	otherKey, err := otherIssuer.issue(auth, target, runtime, requestHash, beforeHash, receivedAt)
	if err != nil {
		t.Fatal(err)
	}
	if otherKey.ReadToken == first.ReadToken {
		t.Fatal("changing the runtime-owned signing key preserved the token")
	}
	if otherKey.BindingHash != first.BindingHash {
		t.Fatalf("signing key changed the public binding commitment: first=%q other=%q", first.BindingHash, otherKey.BindingHash)
	}
	for _, value := range []string{
		first.ReadToken,
		string(first.BindingHash),
		first.String(),
		first.GoString(),
	} {
		if strings.Contains(value, string(key)) || strings.Contains(value, hex.EncodeToString(key)) {
			t.Fatalf("token DTO disclosed signing key through %q", value)
		}
	}
}

func TestIssue83ServiceBackendComposesRawFeatureRuntime(t *testing.T) {
	fixture := newIssue83RawBridgeFixture(t)
	backend := &serviceBackend{rawFeatures: fixture.bridge}
	var rawBackend RawFeatureBackend = backend

	data, terminal := rawBackend.FeaturesGet(
		context.Background(),
		issue83FacadeAuthorization(eebusraw.ToolV1FeaturesGet),
		eebusraw.FeaturesGetRequestV1{Target: fixture.locators[0]},
	)
	if terminal != nil {
		t.Fatalf("composed FeaturesGet() error = %+v", terminal)
	}
	if !reflect.DeepEqual(data.Feature, fixture.locators[0]) ||
		data.Runtime != (eebusraw.RuntimeBindingV1{RuntimeEpoch: 9, ConnectionGeneration: 4}) {
		t.Fatalf("composed FeaturesGet() = %+v", data)
	}
}

type issue83RawBridgeFixture struct {
	bridge        *rawFeatureRuntimeBridge
	local         spineapi.DeviceLocalInterface
	remote        *issue83MutableRemote
	features      []*spine.FeatureRemote
	sender        *issue83RoundTripper
	remoteSKI     string
	shipID        string
	remoteAddress string
	locators      []eebusraw.FeatureLocatorV1
}

func newIssue83RawBridgeFixture(t *testing.T) issue83RawBridgeFixture {
	t.Helper()
	remoteSKI := strings.Repeat("a", 40)
	shipID := "vr940-ship-id"
	remoteAddress := "remote-device"
	sender := &issue83RoundTripper{}
	local := spine.NewDeviceLocal(
		"brand",
		"model",
		"serial",
		"code",
		"local-device",
		spinemodel.DeviceTypeTypeEnergyManagementSystem,
		spinemodel.NetworkManagementFeatureSetTypeSmart,
	)
	localEntity := spine.NewEntityLocal(
		local,
		spinemodel.EntityTypeTypeDeviceInformation,
		[]spinemodel.AddressEntityType{1},
		time.Second,
	)
	localFeature := spine.NewFeatureLocal(
		1,
		localEntity,
		spinemodel.FeatureTypeTypeMeasurement,
		spinemodel.RoleTypeClient,
	)
	localEntity.AddFeature(localFeature)
	local.AddEntity(localEntity)

	base := issue83RemoteDevice(t, local, remoteSKI, remoteAddress, sender, 11, 12)
	remote := &issue83MutableRemote{DeviceRemoteInterface: base, sender: sender}
	local.AddRemoteDeviceForSki(remoteSKI, remote)
	features := make([]*spine.FeatureRemote, 0, 2)
	for _, entity := range base.Entities() {
		for _, feature := range entity.Features() {
			features = append(features, feature.(*spine.FeatureRemote))
		}
	}
	issuer, err := newRawReadTokenIssuer(bytes.Repeat([]byte{0x6b}, 32))
	if err != nil {
		t.Fatal(err)
	}
	bridge := newRawFeatureRuntimeBridge(local, func() uint64 { return 9 }, time.Now, issuer)
	if err := bridge.admitRemote(remoteSKI, shipID, 4, remote); err != nil {
		t.Fatal(err)
	}
	return issue83RawBridgeFixture{
		bridge: bridge, local: local, remote: remote, features: features, sender: sender,
		remoteSKI: remoteSKI, shipID: shipID, remoteAddress: remoteAddress,
		locators: []eebusraw.FeatureLocatorV1{
			issue83Locator(remoteSKI, shipID, remoteAddress, 11),
			issue83Locator(remoteSKI, shipID, remoteAddress, 12),
		},
	}
}

func issue83RemoteDevice(
	t *testing.T,
	local spineapi.DeviceLocalInterface,
	ski string,
	address string,
	sender spineapi.SenderInterface,
	featureAddresses ...uint,
) *spine.DeviceRemote {
	t.Helper()
	remote := spine.NewDeviceRemote(local, ski, sender)
	remoteAddress := spinemodel.AddressDeviceType(address)
	remoteType := spinemodel.DeviceTypeTypeSubmeter
	remote.UpdateDevice(&spinemodel.NetworkManagementDeviceDescriptionDataType{
		DeviceAddress: &spinemodel.DeviceAddressType{Device: &remoteAddress},
		DeviceType:    &remoteType,
	})
	entity := spine.NewEntityRemote(
		remote,
		spinemodel.EntityTypeTypeGridConnectionPointOfPremises,
		[]spinemodel.AddressEntityType{1},
	)
	for _, address := range featureAddresses {
		feature := spine.NewFeatureRemote(
			address,
			entity,
			spinemodel.FeatureTypeTypeMeasurement,
			spinemodel.RoleTypeServer,
		)
		measurementList := spinemodel.FunctionTypeMeasurementListData
		descriptionList := spinemodel.FunctionTypeMeasurementDescriptionListData
		feature.SetOperations([]spinemodel.FunctionPropertyType{
			{
				Function: &measurementList,
				PossibleOperations: &spinemodel.PossibleOperationsType{
					Read:  &spinemodel.PossibleOperationsReadType{},
					Write: &spinemodel.PossibleOperationsWriteType{},
				},
			},
			{
				Function: &descriptionList,
				PossibleOperations: &spinemodel.PossibleOperationsType{
					Read: &spinemodel.PossibleOperationsReadType{},
				},
			},
		})
		entity.AddFeature(feature)
	}
	remote.AddEntity(entity)
	return remote
}

type issue83MutableRemote struct {
	spineapi.DeviceRemoteInterface
	mu     sync.Mutex
	sender spineapi.SenderInterface
}

func (remote *issue83MutableRemote) Sender() spineapi.SenderInterface {
	remote.mu.Lock()
	defer remote.mu.Unlock()
	return remote.sender
}

func (remote *issue83MutableRemote) setSender(sender spineapi.SenderInterface) {
	remote.mu.Lock()
	remote.sender = sender
	remote.mu.Unlock()
}

type issue83RoundTripper struct {
	spineapi.SenderInterface
	calls     atomic.Int64
	mu        sync.Mutex
	order     []uint64
	roundTrip func(context.Context, spineapi.CorrelatedRequest) (spineapi.CorrelatedResponse, error)
}

func (sender *issue83RoundTripper) RoundTrip(
	ctx context.Context,
	request spineapi.CorrelatedRequest,
) (spineapi.CorrelatedResponse, error) {
	sender.calls.Add(1)
	if sender.roundTrip != nil {
		return sender.roundTrip(ctx, request)
	}
	return issue83MeasurementReply(request, 41, 11, 215), nil
}

func (sender *issue83RoundTripper) Stats() spineapi.CorrelatedRoundTripStats {
	return spineapi.CorrelatedRoundTripStats{}
}

func (sender *issue83RoundTripper) Close() error {
	return nil
}

func issue83MeasurementReply(
	request spineapi.CorrelatedRequest,
	key spinemodel.MsgCounterType,
	measurementID spinemodel.MeasurementIdType,
	numberValue spinemodel.NumberType,
) spineapi.CorrelatedResponse {
	classifier := spinemodel.CmdClassifierTypeReply
	scale := spinemodel.ScaleType(-1)
	return spineapi.CorrelatedResponse{
		CorrelationKey: key,
		Header: spinemodel.HeaderType{
			AddressSource:       &request.Destination,
			AddressDestination:  &request.Source,
			MsgCounterReference: &key,
			CmdClassifier:       &classifier,
		},
		Cmd: spinemodel.CmdType{
			MeasurementListData: &spinemodel.MeasurementListDataType{
				MeasurementData: []spinemodel.MeasurementDataType{{
					MeasurementId: &measurementID,
					Value: &spinemodel.ScaledNumberType{
						Number: &numberValue,
						Scale:  &scale,
					},
				}},
			},
		},
	}
}

func issue83EmptyMeasurementReply(
	request spineapi.CorrelatedRequest,
	key spinemodel.MsgCounterType,
) spineapi.CorrelatedResponse {
	classifier := spinemodel.CmdClassifierTypeReply
	return spineapi.CorrelatedResponse{
		CorrelationKey: key,
		Header: spinemodel.HeaderType{
			AddressSource:       &request.Destination,
			AddressDestination:  &request.Source,
			MsgCounterReference: &key,
			CmdClassifier:       &classifier,
		},
		Cmd: spinemodel.CmdType{
			MeasurementListData: &spinemodel.MeasurementListDataType{},
		},
	}
}

func issue83Locator(ski, shipID, device string, feature uint64) eebusraw.FeatureLocatorV1 {
	return eebusraw.FeatureLocatorV1{
		RemoteSKI:      ski,
		SHIPID:         shipID,
		DeviceAddress:  device,
		EntityAddress:  []uint64{1},
		FeatureAddress: feature,
		FeatureType:    "measurement",
		FeatureRole:    eebusraw.FeatureRoleV1Server,
	}
}

func issue83TargetFromLocator(locator eebusraw.FeatureLocatorV1) eebusraw.FeatureTargetV1 {
	return eebusraw.FeatureTargetV1{
		RemoteSKI:      locator.RemoteSKI,
		SHIPID:         locator.SHIPID,
		DeviceAddress:  locator.DeviceAddress,
		EntityAddress:  append([]uint64(nil), locator.EntityAddress...),
		FeatureAddress: locator.FeatureAddress,
		FeatureType:    locator.FeatureType,
		FeatureRole:    locator.FeatureRole,
		Function:       "measurementListData",
		Operation:      eebusraw.OperationV1Read,
	}
}

func issue83FacadeAuthorization(tool eebusraw.ToolV1) eebusraw.ReadAuthorizationV1 {
	return eebusraw.ReadAuthorizationV1{
		PrincipalClass: "owner-local",
		Scope:          eebusraw.AuthScopeV1RawRead,
		Tool:           tool,
		MaskTier:       eebusraw.MaskTierRaw,
	}
}
