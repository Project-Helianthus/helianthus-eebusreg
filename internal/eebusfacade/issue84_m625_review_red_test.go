package eebusfacade

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	executor "github.com/Project-Helianthus/helianthus-eebus-go/features/executor"
	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
)

func TestIssue84ConnectionGenerationPersistsAcrossSameEpochRestart(t *testing.T) {
	store, err := newRawConnectionGenerationStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first := newIssue84Bridge(t, store)
	fixture := newIssue83RawBridgeFixture(t)
	if err := first.admitRemote(fixture.remoteSKI, fixture.shipID, 4, fixture.remote); err != nil {
		t.Fatal(err)
	}

	restarted := newIssue84Bridge(t, store)
	if err := restarted.admitRemote(fixture.remoteSKI, fixture.shipID, 4, fixture.remote); err == nil {
		t.Fatal("same-runtime-epoch restart reused connection generation")
	}
	if err := restarted.admitRemote(fixture.remoteSKI, fixture.shipID, 5, fixture.remote); err != nil {
		t.Fatalf("higher generation after restart was rejected: %v", err)
	}
}

func TestIssue84ConnectionGenerationPersistenceFailureFailsClosed(t *testing.T) {
	store := rawConnectionGenerationStoreFunc(func(uint64, string, uint64) error {
		return errors.New("injected persistence failure")
	})
	bridge := newIssue84Bridge(t, store)
	fixture := newIssue83RawBridgeFixture(t)
	if err := bridge.admitRemote(fixture.remoteSKI, fixture.shipID, 5, fixture.remote); err == nil {
		t.Fatal("admission proceeded after generation persistence failed")
	}
	if bridge.leasesBySKI[fixture.remoteSKI] != nil || fixture.sender.calls.Load() != 0 {
		t.Fatal("failed persistence left a contact-capable remote lease")
	}
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

func TestIssue84SecretDecodeErrorUsesStableSecretCode(t *testing.T) {
	terminal := translateRawExecutorError(eebusraw.ErrSecretDetected)
	if terminal == nil || terminal.Code != eebusraw.ErrorCodeV1SecretDetected {
		t.Fatalf("secret conversion error = %+v, want secret_detected", terminal)
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

func newIssue84Bridge(t *testing.T, store rawConnectionGenerationStore) *rawFeatureRuntimeBridge {
	t.Helper()
	fixture := newIssue83RawBridgeFixture(t)
	return newRawFeatureRuntimeBridgeWithGenerationStore(
		fixture.local,
		func() uint64 { return 9 },
		func() time.Time { return time.Unix(1000, 0).UTC() },
		fixture.bridge.tokenIssuer,
		store,
	)
}
