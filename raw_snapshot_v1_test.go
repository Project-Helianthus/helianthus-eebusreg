package eebusruntime

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

var (
	_ json.Marshaler = SnapshotV1{}
	_ fmt.Stringer   = SnapshotV1{}
	_ fmt.Formatter  = SnapshotV1{}
)

func TestSnapshotV1ClosedEnums(t *testing.T) {
	for _, check := range []struct{ got, want string }{
		{SnapshotContractV1, "helianthus.eebus.runtime.raw-snapshot.v1"},
		{string(ObservedRuntimeStateV1Unknown), "unknown"},
		{string(ObservedRuntimeStateV1Stopped), "stopped"},
		{string(ObservedRuntimeStateV1Starting), "starting"},
		{string(ObservedRuntimeStateV1Ready), "ready"},
		{string(ObservedRuntimeStateV1Degraded), "degraded"},
		{string(ObservedRuntimeStateV1Shutdown), "shutdown"},
		{string(DegradationReasonV1MissingDiscovery), "missing-discovery"},
		{string(DegradationReasonV1DeniedTrust), "denied-trust"},
		{string(DegradationReasonV1RemoteDisconnect), "remote-disconnect"},
		{string(DegradationReasonV1CertificateUnavailable), "certificate-unavailable"},
		{string(DegradationReasonV1NoVisibleServices), "no-visible-services"},
		{string(DegradationReasonV1NoData), "no-data"},
		{string(ServiceKindV1Local), "local"},
		{string(ServiceKindV1Remote), "remote"},
		{string(ObservedSessionStateV1Unknown), "unknown"},
		{string(ObservedSessionStateV1Connecting), "connecting"},
		{string(ObservedSessionStateV1Connected), "connected"},
		{string(ObservedSessionStateV1Disconnected), "disconnected"},
		{string(ObservedSessionStateV1Degraded), "degraded"},
		{string(FeatureRoleV1Unspecified), ""},
		{string(FeatureRoleV1Client), "client"},
		{string(FeatureRoleV1Server), "server"},
	} {
		if check.got != check.want {
			t.Fatalf("enum value = %q, want %q", check.got, check.want)
		}
	}
}

func TestSnapshotV1FormatRedactsEveryVerb(t *testing.T) {
	snapshot := rawSnapshotV1(t, false)
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X"} {
		if got := fmt.Sprintf(format, snapshot); got != snapshot.String() &&
			got != fmt.Sprintf("%q", snapshot.String()) {
			t.Fatalf("fmt.Sprintf(%q, SnapshotV1) = %q", format, got)
		}
	}
}

func TestSnapshotV1ConstructorDetachesAndCanonicalizes(t *testing.T) {
	source := rawSnapshotDraftV1(t, false)
	first, err := NewSnapshotV1(source)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewSnapshotV1(rawSnapshotDraftV1(t, true))
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstJSON) != string(secondJSON) || first.Meta.DataHash != second.Meta.DataHash {
		t.Fatal("equivalent input ordering changed canonical snapshot output")
	}
	source.Services[0].Name = "mutated"
	source.Opaque[0].Path = "/mutated"
	if first.Services[0].Name == "mutated" || first.Opaque[0].Path == "/mutated" {
		t.Fatal("NewSnapshotV1 retained caller-owned storage")
	}
	clone := first.Clone()
	clone.Services[0].Name = "clone mutation"
	if first.Services[0].Name == "clone mutation" {
		t.Fatal("Clone retained snapshot storage")
	}
}

func rawSnapshotV1(t *testing.T, reverse bool) SnapshotV1 {
	t.Helper()
	snapshot, err := NewSnapshotV1(rawSnapshotDraftV1(t, reverse))
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func rawSnapshotDraftV1(t *testing.T, reverse bool) SnapshotV1 {
	t.Helper()
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	runtimeID, err := eebusraw.RedactID(eebusraw.IDKindPeer, "runtime")
	if err != nil {
		t.Fatal(err)
	}
	localSKI, err := eebusraw.RedactID(eebusraw.IDKindLocalSKI, strings.Repeat("1", 40))
	if err != nil {
		t.Fatal(err)
	}
	shipA, shipB := "ship-a", "ship-b"
	description := "observed"
	scenarios := []string{"2", "1"}
	available := true
	opaque := []OpaqueObservationV1{rawOpaqueObservationV1("/raw/a", "test", "a")}
	snapshot := SnapshotV1{
		Meta: SnapshotMetaV1{
			Contract: SnapshotContractV1, Runtime: runtimeID, LocalSKI: localSKI,
			MaskTier: snapshotMaskTierRawV1, CapturedAt: now.Add(time.Minute), DataTimestamp: now,
		},
		Status: RuntimeObservationV1{State: ObservedRuntimeStateV1Ready},
		Pairing: []PairingObservationV1{{
			RemoteSKI: strings.Repeat("2", 40), State: eebusraw.PairingStatePaired, Since: now,
		}},
		Services: []ServiceV1{
			{SKI: strings.Repeat("3", 40), SHIPID: &shipB, Kind: ServiceKindV1Remote, Visible: true, Paired: false, Name: "B", Identifier: "b", Brand: "brand", Type: "type", Model: "model"},
			{SKI: strings.Repeat("2", 40), SHIPID: &shipA, Kind: ServiceKindV1Remote, Visible: true, Paired: true, Name: "A", Identifier: "a", Brand: "brand", Type: "type", Model: "model"},
		},
		Sessions: []SessionV1{{
			ID: "session-a", RemoteSKI: strings.Repeat("2", 40),
			State: ObservedSessionStateV1Connected, Since: now,
		}},
		Devices: []DeviceV1{
			{SKI: strings.Repeat("3", 40), SHIPID: &shipB, Address: "device-b", Type: "type"},
			{SKI: strings.Repeat("2", 40), SHIPID: &shipA, Address: "device-a", Type: "type", Description: &description},
		},
		Entities: []EntityV1{
			{DeviceAddress: "device-b", EntityAddress: "entity-b", Type: "type"},
			{DeviceAddress: "device-a", EntityAddress: "entity-a", Type: "type", Description: &description},
		},
		Features: []FeatureV1{
			{DeviceAddress: "device-b", EntityAddress: "entity-b", FeatureAddress: "feature-b", Type: "type", Role: "server"},
			{DeviceAddress: "device-a", EntityAddress: "entity-a", FeatureAddress: "feature-a", Type: "type", Role: "client", Description: &description},
		},
		UseCases: []UseCaseV1{{
			ContextAddress: "device-a/entity-a/feature-a", Name: "monitoring", Actor: "client",
			Scenarios: &scenarios, Availability: &available,
		}},
		Opaque: opaque,
	}
	if reverse {
		slices.Reverse(snapshot.Services)
		slices.Reverse(snapshot.Devices)
		slices.Reverse(snapshot.Entities)
		slices.Reverse(snapshot.Features)
		slices.Reverse(snapshot.UseCases)
	}
	return snapshot
}

func rawOpaqueObservationV1(path, source, value string) OpaqueObservationV1 {
	scalar := OpaqueScalarV1{String: &value}
	return OpaqueObservationV1{Path: path, Source: source, Value: OpaqueValueV1{Scalar: &scalar}}
}
