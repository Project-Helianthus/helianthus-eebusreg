package eebusfacade

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	executor "github.com/Project-Helianthus/helianthus-eebus-go/features/executor"
	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
)

func TestIssue84ProductionConnectionGenerationAllocatesAfterSameEpochRestart(t *testing.T) {
	stateRoot := issue84PrivateRoot(t)
	store, err := newRawConnectionGenerationStore(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	fixture := newIssue83RawBridgeFixture(t)
	if err := store.advance(9, fixture.remoteSKI, 4); err != nil {
		t.Fatal(err)
	}

	restartedStore, err := newRawConnectionGenerationStore(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	bridge := newRawFeatureRuntimeBridgeWithGenerationStore(
		fixture.local,
		func() uint64 { return 9 },
		time.Now,
		fixture.bridge.tokenIssuer,
		restartedStore,
	)
	handler, service := newIssue84ProductionHandler(t, fixture, bridge)
	defer func() {
		handler.deactivateSPINEEvents()
		handler.waitForSPINEEvents()
	}()

	handler.RemoteSKIConnected(eebusServiceWithFeatureGraph(t, fixture.remoteSKI), fixture.remoteSKI)
	handler.ServiceShipIDUpdate(fixture.remoteSKI, fixture.shipID)

	handler.mu.Lock()
	observation := handler.observations[fixture.remoteSKI]
	handler.mu.Unlock()
	if observation.SessionIndex != 5 {
		t.Fatalf("production restart connection generation = %d, want persisted high-water + 1", observation.SessionIndex)
	}
	bridge.mu.Lock()
	lease := bridge.leasesBySKI[fixture.remoteSKI]
	bridge.mu.Unlock()
	if lease == nil || uint64(lease.generation) != 5 {
		t.Fatalf("production restart admitted lease = %+v, want generation 5", lease)
	}
	if service.localDevice != fixture.local {
		t.Fatal("production generator did not use the active SPINE service")
	}
}

func TestIssue84ProductionAllocationFailureFailsClosedBeforeRawAdmission(t *testing.T) {
	store := rawConnectionGenerationStoreFunc(func(uint64, string, uint64) error {
		return errors.New("injected persistence failure")
	})
	fixture := newIssue83RawBridgeFixture(t)
	bridge := newRawFeatureRuntimeBridgeWithGenerationStore(
		fixture.local,
		func() uint64 { return 9 },
		time.Now,
		fixture.bridge.tokenIssuer,
		nil,
	)
	if err := bridge.admitRemote(fixture.remoteSKI, fixture.shipID, 4, fixture.remote); err != nil {
		t.Fatal(err)
	}
	bridge.mu.Lock()
	prior := bridge.leasesBySKI[fixture.remoteSKI]
	bridge.generationStore = store
	bridge.mu.Unlock()
	handler, _ := newIssue84ProductionHandler(t, fixture, bridge)
	defer func() {
		handler.deactivateSPINEEvents()
		handler.waitForSPINEEvents()
	}()

	handler.RemoteSKIConnected(eebusServiceWithFeatureGraph(t, fixture.remoteSKI), fixture.remoteSKI)

	bridge.mu.Lock()
	pending := bridge.pendingGeneration[fixture.remoteSKI]
	lease := bridge.leasesBySKI[fixture.remoteSKI]
	bridge.mu.Unlock()
	if pending != 0 || lease != nil || prior == nil || !prior.retired {
		t.Fatalf("failed durable allocation left pending=%d lease=%+v prior=%+v", pending, lease, prior)
	}
	if fixture.sender.calls.Load() != 0 {
		t.Fatal("failed persistence left a contact-capable remote lease")
	}
}

func TestIssue84AllocationFailurePreservesPriorSessionProjectionWithoutPublishing(t *testing.T) {
	store := rawConnectionGenerationStoreFunc(func(uint64, string, uint64) error {
		return errors.New("injected persistence failure")
	})
	fixture := newIssue83RawBridgeFixture(t)
	bridge := newRawFeatureRuntimeBridgeWithGenerationStore(
		fixture.local,
		func() uint64 { return 9 },
		time.Now,
		fixture.bridge.tokenIssuer,
		store,
	)
	handler, _ := newIssue84ProductionHandler(t, fixture, bridge)
	defer func() {
		handler.deactivateSPINEEvents()
		handler.waitForSPINEEvents()
	}()

	prior := handler.newRemoteObservation(fixture.remoteSKI)
	prior.SessionID = "session:prior-valid:4"
	prior.SessionState = "disconnected"
	prior.SessionIndex = 4
	prior.ShipID = "prior-valid"
	prior.Visible = true
	prior.ServiceIDs = []string{"service:" + fixture.remoteSKI}
	if err := handler.reducer.Replace(prior); err != nil {
		t.Fatal(err)
	}
	handler.mu.Lock()
	handler.observations[fixture.remoteSKI] = cloneRuntimeGraphObservation(prior)
	handler.runtimeRevision = 7
	handler.publishedRuntimeRevision = 7
	published := 0
	handler.publish = func([]byte) {
		published++
	}
	handler.mu.Unlock()

	handler.RemoteSKIConnected(eebusServiceWithFeatureGraph(t, fixture.remoteSKI), fixture.remoteSKI)

	handler.mu.Lock()
	got := cloneRuntimeGraphObservation(handler.observations[fixture.remoteSKI])
	revision := handler.runtimeRevision
	handler.mu.Unlock()
	if !reflect.DeepEqual(got, prior) {
		t.Fatalf("allocation failure changed prior session projection:\n got: %+v\nwant: %+v", got, prior)
	}
	if revision != 7 {
		t.Fatalf("allocation failure advanced runtime revision to %d, want 7", revision)
	}
	if published != 0 {
		t.Fatalf("allocation failure published %d connected/session projections, want zero", published)
	}
	graph := handler.reducer.Snapshot()
	if len(graph) != 1 || !reflect.DeepEqual(graph[0], prior) {
		t.Fatalf("allocation failure changed reducer projection: %+v", graph)
	}
	select {
	case err := <-handler.errors:
		if err == nil {
			t.Fatal("allocation failure reported a nil error")
		}
	default:
		t.Fatal("allocation failure was not reported")
	}
}

func TestIssue84IndependentGenerationStoresExcludeDuplicateAdvance(t *testing.T) {
	root := issue84PrivateRoot(t)
	first, err := newRawConnectionGenerationStore(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newRawConnectionGenerationStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.advance(9, strings.Repeat("a", 40), 1); err != nil {
		t.Fatal(err)
	}
	if err := second.advance(9, strings.Repeat("b", 40), 1); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for _, store := range []rawConnectionGenerationStore{first, second} {
		workers.Add(1)
		go func(store rawConnectionGenerationStore) {
			defer workers.Done()
			<-start
			results <- store.advance(9, strings.Repeat("c", 40), 1)
		}(store)
	}
	close(start)
	workers.Wait()
	close(results)

	successes := 0
	for result := range results {
		if result == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("independent stores admitted generation 1 %d times, want exactly once", successes)
	}
}

func TestIssue84IndependentProcessesExcludeDuplicateGeneration(t *testing.T) {
	if os.Getenv("HELIANTHUS_ISSUE84_GENERATION_HELPER") == "1" {
		t.Skip("parent-only test")
	}
	root := issue84PrivateRoot(t)
	coordination := t.TempDir()
	release := filepath.Join(coordination, "release")
	first := startIssue84GenerationProcess(
		t,
		root,
		strings.Repeat("a", 40),
		filepath.Join(coordination, "ready-a"),
		release,
		filepath.Join(coordination, "result-a"),
	)
	waitIssue84Path(t, filepath.Join(coordination, "ready-a"))
	second := startIssue84GenerationProcess(
		t,
		root,
		strings.Repeat("b", 40),
		filepath.Join(coordination, "ready-b"),
		release,
		filepath.Join(coordination, "result-b"),
	)
	waitIssue84Path(t, filepath.Join(coordination, "ready-b"))
	if err := os.WriteFile(release, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitIssue84GenerationProcess(t, first)
	waitIssue84GenerationProcess(t, second)

	outcomes := []string{
		readIssue84Path(t, filepath.Join(coordination, "result-a")),
		readIssue84Path(t, filepath.Join(coordination, "result-b")),
	}
	advanced := 0
	for _, outcome := range outcomes {
		if outcome == "advanced" {
			advanced++
		}
	}
	if advanced != 1 {
		t.Fatalf("independent processes produced outcomes %v, want one advanced and one rejected", outcomes)
	}
}

func TestIssue84GenerationStoreProcessHelper(t *testing.T) {
	if os.Getenv("HELIANTHUS_ISSUE84_GENERATION_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	store, err := newRawConnectionGenerationStore(os.Getenv("HELIANTHUS_ISSUE84_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.advance(9, os.Getenv("HELIANTHUS_ISSUE84_PRIME_SKI"), 1); err != nil {
		t.Fatalf("prime independent store: %v", err)
	}
	if err := os.WriteFile(os.Getenv("HELIANTHUS_ISSUE84_READY"), []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitIssue84Path(t, os.Getenv("HELIANTHUS_ISSUE84_RELEASE"))
	outcome := "rejected"
	if err := store.advance(9, strings.Repeat("c", 40), 1); err == nil {
		outcome = "advanced"
	}
	if err := os.WriteFile(os.Getenv("HELIANTHUS_ISSUE84_RESULT"), []byte(outcome), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestIssue84GenerationStoreRejectsUnsafePathsAndObjects(t *testing.T) {
	t.Run("relative root", func(t *testing.T) {
		if _, err := newRawConnectionGenerationStore("relative/state"); err == nil {
			t.Fatal("relative state root was accepted")
		}
	})
	t.Run("symlinked parent", func(t *testing.T) {
		parent := issue84PrivateRoot(t)
		realParent := filepath.Join(parent, "real")
		if err := os.Mkdir(realParent, 0o700); err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(realParent, "state")
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(parent, "linked")
		if err := os.Symlink(realParent, link); err != nil {
			t.Fatal(err)
		}
		store, err := newRawConnectionGenerationStore(filepath.Join(link, "state"))
		if err == nil {
			err = store.advance(9, strings.Repeat("a", 40), 1)
		}
		if err == nil {
			t.Fatal("symlinked state-root component was accepted")
		}
	})
	t.Run("symlinked state", func(t *testing.T) {
		stateRoot := issue84PrivateRoot(t)
		root := rawConnectionGenerationStoreRoot(stateRoot)
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "target.json")
		payload := []byte(`{"runtime_epoch":9,"generations":{}}`)
		if err := os.WriteFile(target, payload, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, rawConnectionGenerationStateFilename)); err != nil {
			t.Fatal(err)
		}
		store, err := newRawConnectionGenerationStore(stateRoot)
		if err == nil {
			err = store.advance(9, strings.Repeat("a", 40), 1)
		}
		if err == nil {
			t.Fatal("symlinked generation state was accepted")
		}
	})
	t.Run("broad root mode", func(t *testing.T) {
		root := issue84PrivateRoot(t)
		if err := os.Chmod(root, 0o755); err != nil {
			t.Fatal(err)
		}
		store, err := newRawConnectionGenerationStore(root)
		if err == nil {
			err = store.advance(9, strings.Repeat("a", 40), 1)
		}
		if err == nil {
			t.Fatal("generation store accepted a non-private root")
		}
	})
}

func TestIssue84GenerationStorePersistsPrivateLockAndState(t *testing.T) {
	root := issue84PrivateRoot(t)
	store, err := newRawConnectionGenerationStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.advance(9, strings.Repeat("a", 40), 1); err != nil {
		t.Fatal(err)
	}
	storeRoot := rawConnectionGenerationStoreRoot(root)
	assertIssue84Mode(t, storeRoot, os.ModeDir|0o700)
	assertIssue84Mode(t, filepath.Join(storeRoot, rawConnectionGenerationStateFilename), 0o600)
	assertIssue84Mode(t, filepath.Join(storeRoot, "raw-connection-generations-v1.lock"), 0o600)
}

func TestIssue84NewGenerationRetiresStaleSHIPIDBeforeUpdateWithoutContact(t *testing.T) {
	fixture := newIssue83RawBridgeFixture(t)
	if err := fixture.bridge.beginConnectionGeneration(fixture.remoteSKI, 5); err != nil {
		t.Fatal(err)
	}
	target := issue83TargetFromLocator(fixture.locators[0])
	_, terminal := fixture.bridge.featuresDataGet(
		context.Background(),
		issue83FacadeAuthorization(eebusraw.ToolV1FeaturesDataGet),
		eebusraw.FeatureDataGetRequestV1{Targets: []eebusraw.FeatureTargetV1{target}, TimeoutMS: 1000},
	)
	if terminal == nil || terminal.Code != eebusraw.ErrorCodeV1Disconnected {
		t.Fatalf("pre-SHIP-ID READ error = %+v, want disconnected", terminal)
	}
	if fixture.sender.calls.Load() != 0 {
		t.Fatalf("pre-SHIP-ID READ contacted sender %d times", fixture.sender.calls.Load())
	}
}

func TestIssue84UnknownFieldsBecomeBoundedOpaqueObservations(t *testing.T) {
	fields := []spineapi.CorrelatedUnknownField{
		{Path: "/datagram/payload/cmd/0/futureZ", Value: spineapi.CorrelatedUnknownValue(`{"z":2}`)},
		{Path: "/datagram/payload/cmd/0/futureA", Value: spineapi.CorrelatedUnknownValue(`{"a":1}`)},
	}
	result := executor.ExactFeatureResult{UnknownFields: fields}
	observations, err := rawExactUnknownObservations(result.UnknownFields)
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != 2 || observations[0].Path != fields[1].Path || observations[1].Path != fields[0].Path {
		t.Fatalf("unknown observations are not canonically ordered: %+v", observations)
	}
	fields[0].Value[0] = 'X'
	if strings.Contains(observations[1].Value.String(), "X") {
		t.Fatal("opaque unknown observation aliases upstream bytes")
	}
	for _, observation := range observations {
		if observation.Source != "eebus-go/executor.ExactFeatureResult.UnknownFields" {
			t.Fatalf("unknown source = %q", observation.Source)
		}
	}
}

func TestIssue84TypedExecutorErrorPreservesBoundedUnknownFields(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code eebusraw.ErrorCodeV1
	}{
		{
			name: "remote error",
			err:  &spineapi.CorrelatedRemoteError{ErrorNumber: 1},
			code: eebusraw.ErrorCodeV1RemoteError,
		},
		{
			name: "protocol error",
			err:  &spineapi.CorrelatedProtocolError{Message: "typed response rejected"},
			code: eebusraw.ErrorCodeV1DecodeError,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newIssue83RawBridgeFixture(t)
			fields := []spineapi.CorrelatedUnknownField{
				{Path: "/datagram/payload/cmd/0/futureZ", Value: spineapi.CorrelatedUnknownValue(`{"z":2}`)},
				{Path: "/datagram/payload/cmd/0/futureA", Value: spineapi.CorrelatedUnknownValue(`{"a":1}`)},
			}
			fixture.sender.roundTrip = func(
				_ context.Context,
				request spineapi.CorrelatedRequest,
			) (spineapi.CorrelatedResponse, error) {
				response := issue83MeasurementReply(request, 41, 11, 215)
				response.UnknownFields = fields
				return response, test.err
			}
			data, terminal := fixture.bridge.featuresDataGet(
				context.Background(),
				issue83FacadeAuthorization(eebusraw.ToolV1FeaturesDataGet),
				eebusraw.FeatureDataGetRequestV1{
					Targets:   []eebusraw.FeatureTargetV1{issue83TargetFromLocator(fixture.locators[0])},
					TimeoutMS: 1000,
				},
			)
			if !reflect.DeepEqual(data, eebusraw.FeatureDataGetDataV1{}) {
				t.Fatalf("typed executor error exposed a successful result: %+v", data)
			}
			if terminal == nil || terminal.Code != test.code {
				t.Fatalf("typed executor terminal = %+v, want %s", terminal, test.code)
			}
			fields[0].Value[0] = 'X'
			encoded, err := json.Marshal(terminal)
			if err != nil {
				t.Fatal(err)
			}
			var typed struct {
				Details *struct {
					Unknown []eebusraw.OpaqueObservationV1 `json:"unknown"`
				} `json:"details"`
			}
			if err := json.Unmarshal(encoded, &typed); err != nil {
				t.Fatal(err)
			}
			if typed.Details == nil || len(typed.Details.Unknown) != 2 {
				t.Fatalf("typed executor error unknown details = %+v", typed.Details)
			}
			if typed.Details.Unknown[0].Path != fields[1].Path ||
				typed.Details.Unknown[1].Path != fields[0].Path {
				t.Fatalf("typed executor error unknowns are not deterministic: %+v", typed.Details.Unknown)
			}
			if strings.Contains(string(encoded), `"Xz"`) {
				t.Fatal("typed executor error unknown details share upstream byte storage")
			}
		})
	}
}

func TestIssue84RawDTOsRetainOnlyFunctionDataUnknownFields(t *testing.T) {
	const (
		functionPath  = "/datagram/payload/cmd/0/measurementListData/futureValue"
		functionValue = "function-data-marker"
	)
	fields := []spineapi.CorrelatedUnknownField{
		{
			Path:  "/rootExtension",
			Value: spineapi.CorrelatedUnknownValue(`"root-transcript-marker"`),
		},
		{
			Path:  "/datagram/frameExtension",
			Value: spineapi.CorrelatedUnknownValue(`"frame-transcript-marker"`),
		},
		{
			Path:  "/datagram/header/future",
			Value: spineapi.CorrelatedUnknownValue(`"header-transcript-marker"`),
		},
		{
			Path:  "/datagram/payload/futureEnvelope",
			Value: spineapi.CorrelatedUnknownValue(`"payload-envelope-marker"`),
		},
		{
			Path:  "/transport/transcript/0",
			Value: spineapi.CorrelatedUnknownValue(`"transport-transcript-marker"`),
		},
		{
			Path:  "/datagram/payload/cmd/1/futureValue",
			Value: spineapi.CorrelatedUnknownValue(`"other-command-marker"`),
		},
		{
			Path:  functionPath,
			Value: spineapi.CorrelatedUnknownValue(`"` + functionValue + `"`),
		},
	}
	for _, test := range []struct {
		name         string
		executorErr  error
		wantTerminal eebusraw.ErrorCodeV1
	}{
		{name: "successful read"},
		{
			name:         "typed remote error",
			executorErr:  &spineapi.CorrelatedRemoteError{ErrorNumber: 1},
			wantTerminal: eebusraw.ErrorCodeV1RemoteError,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newIssue83RawBridgeFixture(t)
			fixture.sender.roundTrip = func(
				_ context.Context,
				request spineapi.CorrelatedRequest,
			) (spineapi.CorrelatedResponse, error) {
				response := issue83MeasurementReply(request, 41, 11, 215)
				response.UnknownFields = fields
				return response, test.executorErr
			}
			target := issue83TargetFromLocator(fixture.locators[0])
			data, terminal := fixture.bridge.featuresDataGet(
				context.Background(),
				issue83FacadeAuthorization(eebusraw.ToolV1FeaturesDataGet),
				eebusraw.FeatureDataGetRequestV1{
					Targets:   []eebusraw.FeatureTargetV1{target},
					TimeoutMS: 1000,
				},
			)

			if test.executorErr == nil {
				if terminal != nil {
					t.Fatal(terminal)
				}
				if len(data.Results) != 1 {
					t.Fatalf("successful raw READ results = %+v, want one", data.Results)
				}
				observation := data.Results[0]
				if !reflect.DeepEqual(observation.Target, target) {
					t.Fatalf("unknown filtering changed exact target:\n got: %+v\nwant: %+v", observation.Target, target)
				}
				if observation.RawRequest.Function != target.Function ||
					observation.RawResponse.Function != target.Function {
					t.Fatalf(
						"unknown filtering changed function metadata: request=%q response=%q target=%q",
						observation.RawRequest.Function,
						observation.RawResponse.Function,
						target.Function,
					)
				}
				assertIssue84FunctionDataUnknowns(t, "RawRequest", observation.RawRequest.Unknown, nil)
				assertIssue84FunctionDataUnknowns(
					t,
					"RawResponse",
					observation.RawResponse.Unknown,
					[]string{functionPath},
				)
				assertIssue84FunctionDataUnknowns(
					t,
					"ReadObservation",
					observation.Unknown,
					[]string{functionPath},
				)
				encoded, err := json.Marshal(observation)
				if err != nil {
					t.Fatal(err)
				}
				assertIssue84NoTranscriptMarkers(t, encoded)
				if !bytes.Contains(encoded, []byte(functionValue)) {
					t.Fatalf("successful raw READ omitted bounded function-data unknown: %s", encoded)
				}
				return
			}

			if !reflect.DeepEqual(data, eebusraw.FeatureDataGetDataV1{}) {
				t.Fatalf("typed error exposed successful raw data: %+v", data)
			}
			if terminal == nil || terminal.Code != test.wantTerminal {
				t.Fatalf("typed error terminal = %+v, want %s", terminal, test.wantTerminal)
			}
			if terminal.Details == nil {
				t.Fatal("typed error omitted bounded function-data unknown details")
			}
			assertIssue84FunctionDataUnknowns(
				t,
				"ErrorDetails",
				terminal.Details.Unknown,
				[]string{functionPath},
			)
			encoded, err := json.Marshal(terminal)
			if err != nil {
				t.Fatal(err)
			}
			assertIssue84NoTranscriptMarkers(t, encoded)
			if !bytes.Contains(encoded, []byte(functionValue)) {
				t.Fatalf("typed error omitted bounded function-data unknown: %s", encoded)
			}
		})
	}
}

func TestIssue84SecretDecodeErrorUsesStableSecretCode(t *testing.T) {
	terminal := translateRawExecutorError(fmt.Errorf("wrapped decode classification: %w", eebusraw.ErrSecretDetected))
	if terminal == nil || terminal.Code != eebusraw.ErrorCodeV1SecretDetected {
		t.Fatalf("secret conversion error = %+v, want secret_detected", terminal)
	}
}

func TestIssue84TypedExecutorErrorUnknownSecretFailsClosed(t *testing.T) {
	fixture := newIssue83RawBridgeFixture(t)
	secret := "-----BEGIN PRIVATE KEY-----\nowner-only-test-secret\n-----END PRIVATE KEY-----"
	fixture.sender.roundTrip = func(
		_ context.Context,
		request spineapi.CorrelatedRequest,
	) (spineapi.CorrelatedResponse, error) {
		response := issue83MeasurementReply(request, 41, 11, 215)
		response.UnknownFields = []spineapi.CorrelatedUnknownField{{
			Path:  "/datagram/payload/cmd/0/future",
			Value: spineapi.CorrelatedUnknownValue(fmt.Sprintf("%q", secret)),
		}}
		return response, &spineapi.CorrelatedRemoteError{ErrorNumber: 1}
	}
	data, terminal := fixture.bridge.featuresDataGet(
		context.Background(),
		issue83FacadeAuthorization(eebusraw.ToolV1FeaturesDataGet),
		eebusraw.FeatureDataGetRequestV1{
			Targets:   []eebusraw.FeatureTargetV1{issue83TargetFromLocator(fixture.locators[0])},
			TimeoutMS: 1000,
		},
	)
	if !reflect.DeepEqual(data, eebusraw.FeatureDataGetDataV1{}) ||
		terminal == nil ||
		terminal.Code != eebusraw.ErrorCodeV1SecretDetected {
		t.Fatalf("secret-classified typed error result = data:%+v terminal:%+v", data, terminal)
	}
	encoded, err := json.Marshal(terminal)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(secret)) {
		t.Fatal("secret-classified typed error result leaked payload bytes")
	}
}

func TestIssue84RemovedFeatureCannotEscapeAsLiveCache(t *testing.T) {
	fixture := newIssue83RawBridgeFixture(t)
	stale := fixture.bridge.inventory[rawFeatureLocatorKey(fixture.locators[1])].Clone()
	stale.Source = eebusraw.ObservationSourceV1Live
	fixture.remote.hideFeature(12)
	if err := fixture.bridge.refreshRemote(fixture.remoteSKI, fixture.shipID, 4, fixture.remote); err != nil {
		t.Fatal(err)
	}
	fixture.bridge.inventory[rawFeatureLocatorKey(fixture.locators[1])] = stale
	data, terminal := fixture.bridge.featuresGet(
		context.Background(),
		issue83FacadeAuthorization(eebusraw.ToolV1FeaturesGet),
		eebusraw.FeaturesGetRequestV1{Target: fixture.locators[1]},
	)
	if terminal != nil {
		t.Fatal(terminal)
	}
	if data.Source == eebusraw.ObservationSourceV1Live {
		t.Fatalf("removed feature escaped stale inventory as source=live: %+v", data)
	}
}

func assertIssue84FunctionDataUnknowns(
	t *testing.T,
	label string,
	observations []eebusraw.OpaqueObservationV1,
	wantPaths []string,
) {
	t.Helper()
	gotPaths := make([]string, len(observations))
	for index, observation := range observations {
		gotPaths[index] = observation.Path
	}
	if len(gotPaths) != len(wantPaths) {
		t.Fatalf("%s unknown paths = %#v, want %#v", label, gotPaths, wantPaths)
	}
	for index := range gotPaths {
		if gotPaths[index] == wantPaths[index] {
			continue
		}
		t.Fatalf("%s unknown paths = %#v, want %#v", label, gotPaths, wantPaths)
	}
}

func assertIssue84NoTranscriptMarkers(t *testing.T, encoded []byte) {
	t.Helper()
	for _, marker := range []string{
		"root-transcript-marker",
		"frame-transcript-marker",
		"header-transcript-marker",
		"payload-envelope-marker",
		"transport-transcript-marker",
		"other-command-marker",
	} {
		if bytes.Contains(encoded, []byte(marker)) {
			t.Fatalf("raw DTO leaked %q: %s", marker, encoded)
		}
	}
}

func newIssue84ProductionHandler(
	t *testing.T,
	fixture issue83RawBridgeFixture,
	bridge *rawFeatureRuntimeBridge,
) (*runtimeServiceHandler, *fakeRuntimeService) {
	t.Helper()
	handler, err := newRuntimeServiceHandler(
		RuntimeConfig{Remotes: []RuntimeRemote{{SKI: fixture.remoteSKI}}},
		strings.Repeat("d", 40),
		time.Now,
	)
	if err != nil {
		t.Fatal(err)
	}
	service := &fakeRuntimeService{started: make(chan struct{}), localDevice: fixture.local}
	handler.bindRawFeatureRuntime(bridge)
	handler.activateSPINEEvents(service)
	return handler, service
}

type issue84GenerationProcess struct {
	command *exec.Cmd
	output  *bytes.Buffer
}

func startIssue84GenerationProcess(
	t *testing.T,
	root string,
	primeSKI string,
	ready string,
	release string,
	result string,
) issue84GenerationProcess {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestIssue84GenerationStoreProcessHelper$")
	command.Env = append(os.Environ(),
		"HELIANTHUS_ISSUE84_GENERATION_HELPER=1",
		"HELIANTHUS_ISSUE84_ROOT="+root,
		"HELIANTHUS_ISSUE84_PRIME_SKI="+primeSKI,
		"HELIANTHUS_ISSUE84_READY="+ready,
		"HELIANTHUS_ISSUE84_RELEASE="+release,
		"HELIANTHUS_ISSUE84_RESULT="+result,
	)
	output := &bytes.Buffer{}
	command.Stdout = output
	command.Stderr = output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	return issue84GenerationProcess{command: command, output: output}
}

func waitIssue84GenerationProcess(t *testing.T, process issue84GenerationProcess) {
	t.Helper()
	if err := process.command.Wait(); err != nil {
		t.Fatalf("generation helper failed: %v\n%s", err, process.output.String())
	}
}

func waitIssue84Path(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Lstat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func readIssue84Path(t *testing.T, path string) string {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(payload))
}

func assertIssue84Mode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode() & (os.ModeType | os.ModePerm); got != want {
		t.Fatalf("%s mode = %v, want %v", path, got, want)
	}
}

func issue84PrivateRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
