package eebusruntime

import (
	"bytes"
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
	for _, format := range []string{
		"%v", "%+v", "%#v", "%b", "%c", "%d", "%o", "%O", "%q", "%x", "%X", "%U",
		"%e", "%E", "%f", "%F", "%g", "%G", "%s",
	} {
		t.Run(format, func(t *testing.T) {
			if got := fmt.Sprintf(format, snapshot); got != snapshot.String() {
				t.Fatalf("fmt.Sprintf(%q, SnapshotV1) = %q, want %q", format, got, snapshot.String())
			}
		})
	}
	for _, verb := range []rune{'v', 'b', 'c', 'd', 'o', 'O', 'q', 'x', 'X', 'U', 'e', 'E', 'f', 'F', 'g', 'G', 's', 'p'} {
		state := &snapshotFormatStateV1{}
		snapshot.Format(state, verb)
		if got := state.String(); got != snapshot.String() {
			t.Fatalf("SnapshotV1.Format(%q) = %q, want %q", verb, got, snapshot.String())
		}
	}
	if got := fmt.Sprintf("%p", &snapshot); strings.Contains(got, "{") {
		t.Fatalf("fmt.Sprintf(%%p, *SnapshotV1) dumped the snapshot: %q", got)
	}
	if got := fmt.Sprintf("%T", snapshot); got != "eebusruntime.SnapshotV1" {
		t.Fatalf("fmt.Sprintf(%%T, SnapshotV1) = %q", got)
	}
}

type snapshotFormatStateV1 struct {
	bytes.Buffer
}

func (snapshotFormatStateV1) Flag(int) bool {
	return false
}

func (snapshotFormatStateV1) Precision() (int, bool) {
	return 0, false
}

func (snapshotFormatStateV1) Width() (int, bool) {
	return 0, false
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
	source.Services[0].Name = stringPointerV1ForTest("mutated")
	source.Opaque[0].Path = "/mutated"
	if optionalStringV1(first.Services[0].Name) == "mutated" || first.Opaque[0].Path == "/mutated" {
		t.Fatal("NewSnapshotV1 retained caller-owned storage")
	}
	clone := first.Clone()
	clone.Services[0].Name = stringPointerV1ForTest("clone mutation")
	if optionalStringV1(first.Services[0].Name) == "clone mutation" {
		t.Fatal("Clone retained snapshot storage")
	}
}

func TestSnapshotV1ConstructorAndCloneDeepDetachNestedStorage(t *testing.T) {
	source := rawSnapshotDraftV1(t, false)
	metadataText := "metadata-original"
	source.Devices[1].Metadata = &MetadataV1{Values: map[string]MetadataValueV1{
		"label": {String: &metadataText},
	}}
	opaqueText := "opaque-original"
	opaqueLeaf := OpaqueValueV1{Scalar: &OpaqueScalarV1{String: &opaqueText}}
	opaqueArray := []OpaqueValueV1{opaqueLeaf}
	opaqueObject := map[string]OpaqueValueV1{
		"nested": {Array: &opaqueArray},
	}
	source.Opaque = []OpaqueObservationV1{{
		Path: "/recursive", Source: "test", Value: OpaqueValueV1{Object: &opaqueObject},
	}}
	first, err := NewSnapshotV1(source)
	if err != nil {
		t.Fatal(err)
	}

	metadataMutation := "metadata-source-mutation"
	source.Devices[1].Metadata.Values["label"] = MetadataValueV1{String: &metadataMutation}
	(*source.UseCases[0].Scenarios)[0] = "source-scenario-mutation"
	sourceOpaque := (*(*source.Opaque[0].Value.Object)["nested"].Array)[0]
	*sourceOpaque.Scalar.String = "opaque-source-mutation"
	if got := *first.Devices[0].Metadata.Values["label"].String; got != metadataText {
		t.Fatalf("constructor retained metadata map storage: %q", got)
	}
	if got := (*first.UseCases[0].Scenarios)[0]; got == "source-scenario-mutation" {
		t.Fatal("constructor retained scenarios slice storage")
	}
	firstNested := (*(*first.Opaque[0].Value.Object)["nested"].Array)[0]
	if got := *firstNested.Scalar.String; got != "opaque-original" {
		t.Fatalf("constructor retained recursive opaque storage: %q", got)
	}

	clone := first.Clone()
	clone.Opaque[0].Path = "/clone-path-mutation"
	cloneMetadata := "metadata-clone-mutation"
	clone.Devices[0].Metadata.Values["label"] = MetadataValueV1{String: &cloneMetadata}
	(*clone.UseCases[0].Scenarios)[0] = "clone-scenario-mutation"
	cloneNested := (*(*clone.Opaque[0].Value.Object)["nested"].Array)[0]
	*cloneNested.Scalar.String = "opaque-clone-mutation"
	if got := *first.Devices[0].Metadata.Values["label"].String; got != metadataText {
		t.Fatalf("Clone retained metadata map storage: %q", got)
	}
	if first.Opaque[0].Path == "/clone-path-mutation" {
		t.Fatal("Clone retained opaque observation slice storage")
	}
	if got := (*first.UseCases[0].Scenarios)[0]; got == "clone-scenario-mutation" {
		t.Fatal("Clone retained scenarios slice storage")
	}
	firstNested = (*(*first.Opaque[0].Value.Object)["nested"].Array)[0]
	if got := *firstNested.Scalar.String; got != "opaque-original" {
		t.Fatalf("Clone retained recursive opaque storage: %q", got)
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
			{SKI: strings.Repeat("3", 40), SHIPID: &shipB, Kind: ServiceKindV1Remote, Visible: true, Paired: false, Name: stringPointerV1ForTest("B"), Identifier: stringPointerV1ForTest("b"), Brand: stringPointerV1ForTest("brand"), Type: stringPointerV1ForTest("type"), Model: stringPointerV1ForTest("model")},
			{SKI: strings.Repeat("2", 40), SHIPID: &shipA, Kind: ServiceKindV1Remote, Visible: true, Paired: true, Name: stringPointerV1ForTest("A"), Identifier: stringPointerV1ForTest("a"), Brand: stringPointerV1ForTest("brand"), Type: stringPointerV1ForTest("type"), Model: stringPointerV1ForTest("model")},
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

func stringPointerV1ForTest(value string) *string {
	return &value
}
