package eebusfacade

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
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
		data.Feature.FeatureType != string(spinemodel.FeatureTypeTypeMeasurement) ||
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
		binding, request, err := issue83ExactBindingAndRequest(fixture.bridge, target)
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
		binding, request, err := issue83ExactBindingAndRequest(fixture.bridge, target)
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

	t.Run("remote address substitution", func(t *testing.T) {
		fixture := newIssue83RawBridgeFixture(t)
		target := issue83TargetFromLocator(fixture.locators[0])
		binding, request, err := issue83ExactBindingAndRequest(fixture.bridge, target)
		if err != nil {
			t.Fatal(err)
		}
		fixture.remote.setAddress("substituted-device")

		_, err = fixture.bridge.RoundTripIfCurrent(context.Background(), binding, request)
		var bindingError *executor.ExactRemoteBindingError
		if !errors.As(err, &bindingError) ||
			bindingError.Failure != executor.ExactRemoteBindingAddressMismatch {
			t.Fatalf("binding error = %#v", bindingError)
		}
		if fixture.sender.calls.Load() != 0 {
			t.Fatalf("address substitution sent %d frames", fixture.sender.calls.Load())
		}
	})

	t.Run("remote SKI substitution", func(t *testing.T) {
		fixture := newIssue83RawBridgeFixture(t)
		target := issue83TargetFromLocator(fixture.locators[0])
		binding, request, err := issue83ExactBindingAndRequest(fixture.bridge, target)
		if err != nil {
			t.Fatal(err)
		}
		fixture.remote.setSKI(strings.Repeat("b", 40))

		_, err = fixture.bridge.RoundTripIfCurrent(context.Background(), binding, request)
		var bindingError *executor.ExactRemoteBindingError
		if !errors.As(err, &bindingError) ||
			bindingError.Failure != executor.ExactRemoteBindingIdentityMismatch {
			t.Fatalf("binding error = %#v", bindingError)
		}
		if fixture.sender.calls.Load() != 0 {
			t.Fatalf("SKI substitution sent %d frames", fixture.sender.calls.Load())
		}
	})

	t.Run("stale connection generation", func(t *testing.T) {
		fixture := newIssue83RawBridgeFixture(t)
		target := issue83TargetFromLocator(fixture.locators[0])
		binding, request, err := issue83ExactBindingAndRequest(fixture.bridge, target)
		if err != nil {
			t.Fatal(err)
		}
		if err := fixture.bridge.admitRemote(
			fixture.remoteSKI,
			fixture.shipID,
			5,
			fixture.remote,
		); err != nil {
			t.Fatal(err)
		}

		_, err = fixture.bridge.RoundTripIfCurrent(context.Background(), binding, request)
		var bindingError *executor.ExactRemoteBindingError
		if !errors.As(err, &bindingError) ||
			bindingError.Failure != executor.ExactRemoteBindingGenerationMismatch {
			t.Fatalf("binding error = %#v", bindingError)
		}
		if fixture.sender.calls.Load() != 0 {
			t.Fatalf("stale generation sent %d frames", fixture.sender.calls.Load())
		}
	})

	t.Run("stale runtime epoch", func(t *testing.T) {
		fixture := newIssue83RawBridgeFixture(t)
		var epoch atomic.Uint64
		epoch.Store(9)
		fixture.bridge.runtimeEpoch = epoch.Load
		target := issue83TargetFromLocator(fixture.locators[0])
		binding, request, err := issue83ExactBindingAndRequest(fixture.bridge, target)
		if err != nil {
			t.Fatal(err)
		}
		epoch.Store(10)

		_, err = fixture.bridge.RoundTripIfCurrent(context.Background(), binding, request)
		if !errors.Is(err, errRawRuntimeEpochMismatch) ||
			!errors.Is(err, executor.ErrExactRemoteBindingMismatch) {
			t.Fatalf("RoundTripIfCurrent() error = %v", err)
		}
		if fixture.sender.calls.Load() != 0 {
			t.Fatalf("stale epoch sent %d frames", fixture.sender.calls.Load())
		}
	})
}

func TestIssue83RuntimeEpochUsesDurableIdentityEpoch(t *testing.T) {
	coordinator := &firstTrustCoordinator{
		controlView: firstTrustControlView{
			manifest: firstTrustManifestBinding{epoch: 17},
			control: firstTrustControlRecord{
				controlEpoch:   41,
				repairSequence: 6,
			},
		},
	}
	provider := rawRuntimeEpochProvider(
		&runtimeFirstTrustResources{coordinator: coordinator},
		99,
	)
	if got := provider(); got != 7 {
		t.Fatalf("runtime epoch = %d, want durable repair epoch 7", got)
	}
	coordinator.mu.Lock()
	coordinator.controlView.control.controlEpoch++
	coordinator.controlView.manifest.epoch++
	coordinator.mu.Unlock()
	if got := provider(); got != 7 {
		t.Fatalf("outbound-attempt control churn changed runtime epoch to %d", got)
	}
	coordinator.mu.Lock()
	coordinator.controlView.control.repairSequence = 7
	coordinator.mu.Unlock()
	if got := provider(); got != 8 {
		t.Fatalf("durable identity replacement did not advance runtime epoch: %d", got)
	}

	restarted := &firstTrustCoordinator{
		controlView: firstTrustControlView{
			manifest: firstTrustManifestBinding{epoch: 18},
			control: firstTrustControlRecord{
				controlEpoch:   1,
				repairSequence: 7,
			},
		},
	}
	restartedProvider := rawRuntimeEpochProvider(
		&runtimeFirstTrustResources{coordinator: restarted},
		101,
	)
	if got := restartedProvider(); got != 8 {
		t.Fatalf("restart did not recover durable runtime epoch: %d", got)
	}
	fallback, err := rawRuntimeEpochForIdentity(strings.Repeat("c", 40))
	if err != nil {
		t.Fatal(err)
	}
	restartedFallback, err := rawRuntimeEpochForIdentity(strings.Repeat("c", 40))
	if err != nil {
		t.Fatal(err)
	}
	if fallback == 0 || fallback != restartedFallback {
		t.Fatalf("identity fallback is not restart-stable: %d, %d", fallback, restartedFallback)
	}
}

func TestIssue83AdmissionsRejectRepeatedRegressingAndStaleRefresh(t *testing.T) {
	fixture := newIssue83RawBridgeFixture(t)
	if err := fixture.bridge.admitRemote(
		fixture.remoteSKI,
		fixture.shipID,
		4,
		fixture.remote,
	); err == nil {
		t.Fatal("repeated connection generation was admitted")
	}
	if err := fixture.bridge.admitRemote(
		fixture.remoteSKI,
		fixture.shipID,
		3,
		fixture.remote,
	); err == nil {
		t.Fatal("regressing connection generation was admitted")
	}
	if err := fixture.bridge.refreshRemote(
		fixture.remoteSKI,
		fixture.shipID,
		3,
		fixture.remote,
	); err == nil {
		t.Fatal("stale live topology refresh was admitted")
	}

	binding, request, err := issue83ExactBindingAndRequest(
		fixture.bridge,
		issue83TargetFromLocator(fixture.locators[0]),
	)
	if err != nil {
		t.Fatal(err)
	}
	binding.ConnectionGeneration = 3
	_, err = fixture.bridge.RoundTripIfCurrent(context.Background(), binding, request)
	if !errors.Is(err, executor.ErrExactRemoteBindingMismatch) {
		t.Fatalf("stale generation dispatch error = %v", err)
	}
	if fixture.sender.calls.Load() != 0 {
		t.Fatalf("stale admission path sent %d frames", fixture.sender.calls.Load())
	}

	fixture.bridge.retireRemote(fixture.remoteSKI, 4)
	if err := fixture.bridge.admitRemote(
		fixture.remoteSKI,
		fixture.shipID,
		4,
		fixture.remote,
	); err == nil {
		t.Fatal("retired connection generation was admitted again")
	}
}

func TestIssue83FinalDispatchRevalidatesFunctionTopologyWithoutSend(t *testing.T) {
	fixture := newIssue83RawBridgeFixture(t)
	binding, request, err := issue83ExactBindingAndRequest(
		fixture.bridge,
		issue83TargetFromLocator(fixture.locators[0]),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Cmd = issue83MeasurementReadCommand()
	descriptionList := spinemodel.FunctionTypeMeasurementDescriptionListData
	fixture.features[0].SetOperations([]spinemodel.FunctionPropertyType{{
		Function: &descriptionList,
		PossibleOperations: &spinemodel.PossibleOperationsType{
			Read: &spinemodel.PossibleOperationsReadType{},
		},
	}})

	_, err = fixture.bridge.RoundTripIfCurrent(context.Background(), binding, request)
	if !errors.Is(err, executor.ErrExactTargetMismatch) {
		t.Fatalf("topology drift error = %v, want exact target mismatch", err)
	}
	if fixture.sender.calls.Load() != 0 {
		t.Fatalf("topology drift sent %d frames", fixture.sender.calls.Load())
	}
}

func TestIssue83LiveInventoryRefreshRemovesDisappearedFeatures(t *testing.T) {
	fixture := newIssue83RawBridgeFixture(t)
	fixture.remote.hideFeature(12)
	if err := fixture.bridge.refreshRemote(
		fixture.remoteSKI,
		fixture.shipID,
		4,
		fixture.remote,
	); err != nil {
		t.Fatal(err)
	}
	_, terminal := fixture.bridge.featuresGet(
		context.Background(),
		issue83FacadeAuthorization(eebusraw.ToolV1FeaturesGet),
		eebusraw.FeaturesGetRequestV1{Target: fixture.locators[1]},
	)
	if terminal == nil || terminal.Code != eebusraw.ErrorCodeV1NotFound {
		t.Fatalf("disappeared feature lookup error = %+v", terminal)
	}
}

func TestIssue83CloseCancelsInFlightWithoutHoldingRuntimeLock(t *testing.T) {
	fixture := newIssue83RawBridgeFixture(t)
	backend := &serviceBackend{
		handler:     &runtimeServiceHandler{rawFeatures: fixture.bridge},
		rawFeatures: fixture.bridge,
	}
	binding, request, err := issue83ExactBindingAndRequest(
		fixture.bridge,
		issue83TargetFromLocator(fixture.locators[0]),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Cmd = issue83MeasurementReadCommand()
	started := make(chan struct{})
	fixture.sender.roundTrip = func(
		ctx context.Context,
		_ spineapi.CorrelatedRequest,
	) (spineapi.CorrelatedResponse, error) {
		close(started)
		<-ctx.Done()
		return spineapi.CorrelatedResponse{}, ctx.Err()
	}
	done := make(chan error, 1)
	go func() {
		_, roundTripErr := fixture.bridge.RoundTripIfCurrent(context.Background(), binding, request)
		done <- roundTripErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("round trip did not start")
	}
	begin := time.Now()
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case roundTripErr := <-done:
		if !errors.Is(roundTripErr, executor.ErrExactRemoteBindingMismatch) {
			t.Fatalf("retired round trip error = %v", roundTripErr)
		}
	case <-time.After(time.Second):
		t.Fatal("retirement did not promptly cancel the in-flight round trip")
	}
	if elapsed := time.Since(begin); elapsed > 500*time.Millisecond {
		t.Fatalf("Close cancellation took %v", elapsed)
	}
}

func TestIssue83ConcurrentNewGenerationRetiresActiveLease(t *testing.T) {
	fixture := newIssue83RawBridgeFixture(t)
	oldBinding, oldRequest, err := issue83ExactBindingAndRequest(
		fixture.bridge,
		issue83TargetFromLocator(fixture.locators[0]),
	)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	fixture.sender.roundTrip = func(
		ctx context.Context,
		_ spineapi.CorrelatedRequest,
	) (spineapi.CorrelatedResponse, error) {
		close(started)
		<-ctx.Done()
		return spineapi.CorrelatedResponse{}, ctx.Err()
	}
	oldDone := make(chan error, 1)
	go func() {
		_, roundTripErr := fixture.bridge.RoundTripIfCurrent(
			context.Background(),
			oldBinding,
			oldRequest,
		)
		oldDone <- roundTripErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("old generation did not begin dispatch")
	}
	if err := fixture.bridge.admitRemote(
		fixture.remoteSKI,
		fixture.shipID,
		5,
		fixture.remote,
	); err != nil {
		t.Fatal(err)
	}
	select {
	case roundTripErr := <-oldDone:
		if !errors.Is(roundTripErr, executor.ErrExactRemoteBindingMismatch) {
			t.Fatalf("retired generation error = %v", roundTripErr)
		}
	case <-time.After(time.Second):
		t.Fatal("new admission did not retire the active lease")
	}
	before := fixture.sender.calls.Load()
	_, err = fixture.bridge.RoundTripIfCurrent(
		context.Background(),
		oldBinding,
		oldRequest,
	)
	if !errors.Is(err, executor.ErrExactRemoteBindingMismatch) {
		t.Fatalf("retired binding error = %v", err)
	}
	if fixture.sender.calls.Load() != before {
		t.Fatal("retired binding contacted the sender after concurrent replacement")
	}
}

func TestIssue83RoundTripAllowsReentrantRetirementCallback(t *testing.T) {
	fixture := newIssue83RawBridgeFixture(t)
	binding, request, err := issue83ExactBindingAndRequest(
		fixture.bridge,
		issue83TargetFromLocator(fixture.locators[0]),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Cmd = issue83MeasurementReadCommand()
	fixture.sender.roundTrip = func(
		_ context.Context,
		request spineapi.CorrelatedRequest,
	) (spineapi.CorrelatedResponse, error) {
		fixture.bridge.retireRemote(fixture.remoteSKI, 4)
		return issue83MeasurementReply(request, 41, 11, 215), nil
	}
	done := make(chan error, 1)
	go func() {
		_, roundTripErr := fixture.bridge.RoundTripIfCurrent(context.Background(), binding, request)
		done <- roundTripErr
	}()
	select {
	case roundTripErr := <-done:
		if !errors.Is(roundTripErr, executor.ErrExactRemoteBindingMismatch) {
			t.Fatalf("reentrant retirement error = %v", roundTripErr)
		}
	case <-time.After(time.Second):
		t.Fatal("reentrant retirement deadlocked")
	}
}

func TestIssue83AllFailedReadsReturnOrdinaryTerminalErrorWithoutPartialData(t *testing.T) {
	fixture := newIssue83RawBridgeFixture(t)
	fixture.sender.roundTrip = func(
		_ context.Context,
		request spineapi.CorrelatedRequest,
	) (spineapi.CorrelatedResponse, error) {
		if *request.Destination.Feature == 11 {
			return spineapi.CorrelatedResponse{}, context.DeadlineExceeded
		}
		return spineapi.CorrelatedResponse{}, &spineapi.CorrelatedProtocolError{}
	}
	data, terminal := fixture.bridge.featuresDataGet(
		context.Background(),
		issue83FacadeAuthorization(eebusraw.ToolV1FeaturesDataGet),
		eebusraw.FeatureDataGetRequestV1{
			Targets: []eebusraw.FeatureTargetV1{
				issue83TargetFromLocator(fixture.locators[0]),
				issue83TargetFromLocator(fixture.locators[1]),
			},
			TimeoutMS: 1000,
		},
	)
	if terminal == nil || terminal.Code != eebusraw.ErrorCodeV1Timeout {
		t.Fatalf("all-fail terminal = %+v, want first ordered timeout", terminal)
	}
	if !reflect.DeepEqual(data, eebusraw.FeatureDataGetDataV1{}) {
		t.Fatalf("all-fail exposed partial data: %+v", data)
	}
}

func TestIssue83TypedFunctionDataExcludesProtocolHeadersButKeepsTargetAddress(t *testing.T) {
	fixture := newIssue83RawBridgeFixture(t)
	target := issue83TargetFromLocator(fixture.locators[0])
	unknownPath := "/datagram/payload/cmd/0/future"
	fixture.sender.roundTrip = func(
		_ context.Context,
		request spineapi.CorrelatedRequest,
	) (spineapi.CorrelatedResponse, error) {
		response := issue83MeasurementReply(request, 41, 11, 215)
		ack := true
		originator := cloneRawFeatureAddress(request.Destination)
		response.Header.AckRequest = &ack
		response.Header.AddressOriginator = &originator
		response.UnknownFields = []spineapi.CorrelatedUnknownField{{
			Path:  unknownPath,
			Value: spineapi.CorrelatedUnknownValue(`{"future":true}`),
		}}
		return response, nil
	}
	data, terminal := fixture.bridge.featuresDataGet(
		context.Background(),
		issue83FacadeAuthorization(eebusraw.ToolV1FeaturesDataGet),
		eebusraw.FeatureDataGetRequestV1{
			Targets:   []eebusraw.FeatureTargetV1{target},
			TimeoutMS: 1000,
		},
	)
	if terminal != nil {
		t.Fatalf("featuresDataGet() error = %+v", terminal)
	}
	if len(data.Results) != 1 {
		t.Fatalf("featuresDataGet() results = %+v", data.Results)
	}
	observation := data.Results[0]
	if !reflect.DeepEqual(observation.Target, target) ||
		observation.Target.DeviceAddress != fixture.remoteAddress ||
		!reflect.DeepEqual(observation.Target.EntityAddress, []uint64{1}) ||
		observation.Target.FeatureAddress != fixture.locators[0].FeatureAddress ||
		observation.Target.Function != string(spinemodel.FunctionTypeMeasurementListData) ||
		observation.RawRequest.Function != observation.Target.Function ||
		observation.RawResponse.Function != observation.Target.Function {
		t.Fatalf("typed target/function address was not retained: %+v", observation)
	}
	groups := [][]eebusraw.OpaqueObservationV1{
		observation.RawRequest.Unknown,
		observation.RawResponse.Unknown,
		observation.Unknown,
	}
	for _, group := range groups {
		for _, opaque := range group {
			if strings.HasPrefix(opaque.Path, "/request/") ||
				strings.HasPrefix(opaque.Path, "/header/") {
				t.Fatalf("protocol header/transcript escaped as opaque observation: %+v", opaque)
			}
		}
	}
	if len(observation.RawRequest.Unknown) != 0 ||
		len(observation.RawResponse.Unknown) != 1 ||
		observation.RawResponse.Unknown[0].Path != unknownPath ||
		len(observation.Unknown) != 1 ||
		observation.Unknown[0].Path != unknownPath {
		t.Fatalf("typed function-data unknowns = request:%+v response:%+v observation:%+v",
			observation.RawRequest.Unknown, observation.RawResponse.Unknown, observation.Unknown)
	}
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
	decodedToken, err := base64.RawURLEncoding.DecodeString(first.ReadToken)
	if err != nil || len(decodedToken) != 32 || len(first.ReadToken) != 43 {
		t.Fatalf("opaque token is not a 256-bit base64url reference: %q, %v", first.ReadToken, err)
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
		fmt.Sprintf("%v %#v %x", issuer, issuer, issuer),
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
			if feature.Address() != nil && feature.Address().Feature != nil &&
				(*feature.Address().Feature == 11 || *feature.Address().Feature == 12) {
				features = append(features, feature.(*spine.FeatureRemote))
			}
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
	mu              sync.Mutex
	sender          spineapi.SenderInterface
	addressOverride *spinemodel.AddressDeviceType
	skiOverride     *string
	hiddenFeatures  map[spinemodel.AddressFeatureType]bool
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

func (remote *issue83MutableRemote) Address() *spinemodel.AddressDeviceType {
	remote.mu.Lock()
	defer remote.mu.Unlock()
	if remote.addressOverride == nil {
		return remote.DeviceRemoteInterface.Address()
	}
	value := *remote.addressOverride
	return &value
}

func (remote *issue83MutableRemote) setAddress(address string) {
	remote.mu.Lock()
	value := spinemodel.AddressDeviceType(address)
	remote.addressOverride = &value
	remote.mu.Unlock()
}

func (remote *issue83MutableRemote) Ski() string {
	remote.mu.Lock()
	defer remote.mu.Unlock()
	if remote.skiOverride == nil {
		return remote.DeviceRemoteInterface.Ski()
	}
	return *remote.skiOverride
}

func (remote *issue83MutableRemote) setSKI(ski string) {
	remote.mu.Lock()
	remote.skiOverride = &ski
	remote.mu.Unlock()
}

func (remote *issue83MutableRemote) FeatureByAddress(
	address *spinemodel.FeatureAddressType,
) spineapi.FeatureRemoteInterface {
	remote.mu.Lock()
	defer remote.mu.Unlock()
	if address != nil && address.Feature != nil && remote.hiddenFeatures[*address.Feature] {
		return nil
	}
	return remote.DeviceRemoteInterface.FeatureByAddress(address)
}

func (remote *issue83MutableRemote) hideFeature(feature spinemodel.AddressFeatureType) {
	remote.mu.Lock()
	if remote.hiddenFeatures == nil {
		remote.hiddenFeatures = make(map[spinemodel.AddressFeatureType]bool)
	}
	remote.hiddenFeatures[feature] = true
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

func issue83MeasurementReadCommand() spinemodel.CmdType {
	return spinemodel.CmdType{
		MeasurementListData: &spinemodel.MeasurementListDataType{},
	}
}

func issue83ExactBindingAndRequest(
	bridge *rawFeatureRuntimeBridge,
	target eebusraw.FeatureTargetV1,
) (executor.ExactRemoteBinding, spineapi.CorrelatedRequest, error) {
	request, _, terminal := bridge.exactReadRequest(target)
	if terminal != nil {
		return executor.ExactRemoteBinding{}, spineapi.CorrelatedRequest{}, terminal
	}
	return executor.ExactRemoteBinding{
			DeviceAddress:        *request.Target.Address.Device,
			RemoteIdentity:       request.Target.RemoteIdentity,
			ConnectionGeneration: request.Target.ConnectionGeneration,
		}, spineapi.CorrelatedRequest{
			Classifier:  spinemodel.CmdClassifierTypeRead,
			Source:      cloneRawFeatureAddress(request.Source),
			Destination: cloneRawFeatureAddress(request.Target.Address),
			Cmd:         issue83MeasurementReadCommand(),
		}, nil
}

func containsIssue83String(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func issue83Locator(ski, shipID, device string, feature uint64) eebusraw.FeatureLocatorV1 {
	return eebusraw.FeatureLocatorV1{
		RemoteSKI:      ski,
		SHIPID:         shipID,
		DeviceAddress:  device,
		EntityAddress:  []uint64{1},
		FeatureAddress: feature,
		FeatureType:    string(spinemodel.FeatureTypeTypeMeasurement),
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
