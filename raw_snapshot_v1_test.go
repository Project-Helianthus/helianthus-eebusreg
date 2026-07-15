package eebusruntime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusevidence"
	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

var (
	_ json.Marshaler = SnapshotV1{}
	_ fmt.Stringer   = SnapshotV1{}
	_ fmt.Formatter  = SnapshotV1{}
)

func TestRawSnapshotV1PublicContract(t *testing.T) {
	for _, check := range []struct {
		value  any
		fields []string
	}{
		{SnapshotV1{}, []string{"Meta:meta", "Status:status", "Pairing:pairing", "Services:services", "Sessions:sessions", "Topology:topology", "Raw:raw"}},
		{SnapshotMetaV1{}, []string{"Contract:contract", "Runtime:runtime", "LocalSKI:local_ski", "MaskTier:mask_tier", "CapturedAt:captured_at", "DataTimestamp:data_timestamp", "DataHash:data_hash"}},
		{RuntimeObservationV1{}, []string{"State:state", "Degradation:degradation"}},
		{DegradationV1{}, []string{"Reason:reason", "Since:since"}},
		{PairingObservationV1{}, []string{"Remote:remote", "State:state", "Since:since", "Raw:raw", "Unknown:unknown"}},
		{ServiceV1{}, []string{"ID:id", "Kind:kind", "Visible:visible", "Paired:paired", "Raw:raw", "Unknown:unknown"}},
		{SessionV1{}, []string{"ID:id", "Remote:remote", "State:state", "Since:since", "Raw:raw", "Unknown:unknown"}},
		{TopologyV1{}, []string{"Devices:devices"}},
		{DeviceV1{}, []string{"ID:id", "Entities:entities", "UseCaseClaims:usecase_claims", "Raw:raw", "Unknown:unknown"}},
		{EntityV1{}, []string{"ID:id", "Features:features", "Raw:raw", "Unknown:unknown"}},
		{FeatureV1{}, []string{"ID:id", "Role:role", "Raw:raw", "Unknown:unknown"}},
		{UseCaseClaimV1{}, []string{"ID:id", "Raw:raw", "Unknown:unknown"}},
	} {
		assertSnapshotV1Fields(t, reflect.TypeOf(check.value), check.fields)
	}

	for _, check := range []struct{ got, want string }{
		{string(SnapshotContractV1), "helianthus.eebus.runtime.raw-snapshot.v1"},
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

func TestRawSnapshotV1IdentityKinds(t *testing.T) {
	valid := []struct {
		name   string
		mutate func(*SnapshotV1)
	}{
		{"runtime peer", func(snapshot *SnapshotV1) {
			snapshot.Meta.Runtime = rawSnapshotID(t, eebusraw.IDKindPeer, "runtime-peer")
		}},
		{"runtime local ski", func(snapshot *SnapshotV1) {
			snapshot.Meta.Runtime = rawSnapshotID(t, eebusraw.IDKindLocalSKI, "runtime-local-ski")
		}},
		{"pairing remote ski", func(snapshot *SnapshotV1) {
			snapshot.Pairing[0].Remote = rawSnapshotID(t, eebusraw.IDKindRemoteSKI, "pairing-remote-ski")
		}},
		{"session remote ski", func(snapshot *SnapshotV1) {
			snapshot.Sessions[0].Remote = rawSnapshotID(t, eebusraw.IDKindRemoteSKI, "session-remote-ski")
		}},
		{"generic peer ids", func(snapshot *SnapshotV1) {
			snapshot.Services[0].ID = rawSnapshotID(t, eebusraw.IDKindPeer, "service-peer")
			snapshot.Topology.Devices[1].ID = rawSnapshotID(t, eebusraw.IDKindPeer, "device-peer")
			snapshot.Topology.Devices[1].Entities[0].ID = rawSnapshotID(t, eebusraw.IDKindPeer, "entity-peer")
			snapshot.Topology.Devices[1].Entities[0].Features[0].ID = rawSnapshotID(t, eebusraw.IDKindPeer, "feature-peer")
			snapshot.Topology.Devices[1].UseCaseClaims[0].ID = rawSnapshotID(t, eebusraw.IDKindPeer, "usecase-peer")
		}},
	}
	for _, test := range valid {
		t.Run(test.name, func(t *testing.T) {
			snapshot := rawSnapshotV1(t, false)
			test.mutate(&snapshot)
			if _, err := NewSnapshotV1(snapshot); err != nil {
				t.Fatalf("NewSnapshotV1() error = %v", err)
			}
		})
	}

	invalid := []struct {
		name   string
		mutate func(*SnapshotV1)
	}{
		{"runtime remote ski", func(snapshot *SnapshotV1) {
			snapshot.Meta.Runtime = rawSnapshotID(t, eebusraw.IDKindRemoteSKI, "runtime-remote-ski")
		}},
		{"local ski peer", func(snapshot *SnapshotV1) {
			snapshot.Meta.LocalSKI = rawSnapshotID(t, eebusraw.IDKindPeer, "local-peer")
		}},
		{"session id peer", func(snapshot *SnapshotV1) {
			snapshot.Sessions[0].ID = rawSnapshotID(t, eebusraw.IDKindPeer, "session-peer")
		}},
		{"pairing local ski", func(snapshot *SnapshotV1) {
			snapshot.Pairing[0].Remote = rawSnapshotID(t, eebusraw.IDKindLocalSKI, "pairing-local-ski")
		}},
		{"session remote local ski", func(snapshot *SnapshotV1) {
			snapshot.Sessions[0].Remote = rawSnapshotID(t, eebusraw.IDKindLocalSKI, "session-local-ski")
		}},
		{"service remote ski", func(snapshot *SnapshotV1) {
			snapshot.Services[0].ID = rawSnapshotID(t, eebusraw.IDKindRemoteSKI, "service-remote-ski")
		}},
		{"device remote ski", func(snapshot *SnapshotV1) {
			snapshot.Topology.Devices[1].ID = rawSnapshotID(t, eebusraw.IDKindRemoteSKI, "device-remote-ski")
		}},
		{"entity remote ski", func(snapshot *SnapshotV1) {
			snapshot.Topology.Devices[1].Entities[0].ID = rawSnapshotID(t, eebusraw.IDKindRemoteSKI, "entity-remote-ski")
		}},
		{"feature remote ski", func(snapshot *SnapshotV1) {
			snapshot.Topology.Devices[1].Entities[0].Features[0].ID = rawSnapshotID(t, eebusraw.IDKindRemoteSKI, "feature-remote-ski")
		}},
		{"usecase remote ski", func(snapshot *SnapshotV1) {
			snapshot.Topology.Devices[1].UseCaseClaims[0].ID = rawSnapshotID(t, eebusraw.IDKindRemoteSKI, "usecase-remote-ski")
		}},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			snapshot := rawSnapshotV1(t, false)
			test.mutate(&snapshot)
			if _, err := NewSnapshotV1(snapshot); err == nil {
				t.Fatal("NewSnapshotV1() accepted an invalid identity kind")
			}
		})
	}
}

func TestRawSnapshotV1FeatureRoleUnspecified(t *testing.T) {
	snapshot := rawSnapshotV1(t, false)
	snapshot.Topology.Devices[1].Entities[0].Features[0].Role = FeatureRoleV1Unspecified
	if _, err := NewSnapshotV1(snapshot); err != nil {
		t.Fatalf("NewSnapshotV1() error = %v", err)
	}
}

func TestRawSnapshotV1PairingStateIsClosed(t *testing.T) {
	for _, state := range []eebusraw.PairingState{
		eebusraw.PairingStateUnknown,
		eebusraw.PairingStateUnpaired,
		eebusraw.PairingStatePaired,
		eebusraw.PairingStateDenied,
	} {
		snapshot := rawSnapshotV1(t, false)
		snapshot.Pairing[0].State = state
		if _, err := NewSnapshotV1(snapshot); err != nil {
			t.Fatalf("NewSnapshotV1(%q) error = %v", state, err)
		}
	}
	for _, state := range []eebusraw.PairingState{"", "future"} {
		snapshot := rawSnapshotV1(t, false)
		snapshot.Pairing[0].State = state
		if _, err := NewSnapshotV1(snapshot); err == nil {
			t.Fatalf("NewSnapshotV1(%q) accepted unsupported pairing state", state)
		}
	}
}

func TestRawSnapshotV1RejectsExactDuplicateEvidence(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	object := eebusevidence.NewObjectV1(eebusevidence.ObjectKindIdentity, rawSnapshotDigest("c"), 3, now)
	duplicateRaw := []struct {
		name   string
		mutate func(*SnapshotV1)
	}{
		{"root", func(snapshot *SnapshotV1) { snapshot.Raw = []eebusevidence.ObjectV1{object, object} }},
		{"pairing", func(snapshot *SnapshotV1) { snapshot.Pairing[0].Raw = []eebusevidence.ObjectV1{object, object} }},
		{"service", func(snapshot *SnapshotV1) { snapshot.Services[0].Raw = []eebusevidence.ObjectV1{object, object} }},
		{"session", func(snapshot *SnapshotV1) { snapshot.Sessions[0].Raw = []eebusevidence.ObjectV1{object, object} }},
		{"device", func(snapshot *SnapshotV1) {
			snapshot.Topology.Devices[1].Raw = []eebusevidence.ObjectV1{object, object}
		}},
		{"entity", func(snapshot *SnapshotV1) {
			snapshot.Topology.Devices[1].Entities[0].Raw = []eebusevidence.ObjectV1{object, object}
		}},
		{"feature", func(snapshot *SnapshotV1) {
			snapshot.Topology.Devices[1].Entities[0].Features[0].Raw = []eebusevidence.ObjectV1{object, object}
		}},
		{"usecase", func(snapshot *SnapshotV1) {
			snapshot.Topology.Devices[1].UseCaseClaims[0].Raw = []eebusevidence.ObjectV1{object, object}
		}},
	}
	for _, test := range duplicateRaw {
		t.Run(test.name, func(t *testing.T) {
			snapshot := rawSnapshotV1(t, false)
			test.mutate(&snapshot)
			if _, err := NewSnapshotV1(snapshot); err == nil || !strings.Contains(err.Error(), "duplicates raw evidence object") {
				t.Fatalf("NewSnapshotV1() error = %v, want duplicate raw evidence rejection", err)
			}
		})
	}

	field := rawSnapshotUnknown("duplicate")
	duplicateUnknown := []struct {
		name   string
		mutate func(*SnapshotV1)
	}{
		{"raw object", func(snapshot *SnapshotV1) {
			snapshot.Raw = []eebusevidence.ObjectV1{{Kind: eebusevidence.ObjectKindIdentity, Digest: rawSnapshotDigest("d"), Size: 4, DataTimestamp: now, Unknown: []eebusraw.UnknownField{field, field}}}
		}},
		{"pairing", func(snapshot *SnapshotV1) { snapshot.Pairing[0].Unknown = []eebusraw.UnknownField{field, field} }},
		{"service", func(snapshot *SnapshotV1) { snapshot.Services[0].Unknown = []eebusraw.UnknownField{field, field} }},
		{"session", func(snapshot *SnapshotV1) { snapshot.Sessions[0].Unknown = []eebusraw.UnknownField{field, field} }},
		{"device", func(snapshot *SnapshotV1) {
			snapshot.Topology.Devices[1].Unknown = []eebusraw.UnknownField{field, field}
		}},
		{"entity", func(snapshot *SnapshotV1) {
			snapshot.Topology.Devices[1].Entities[0].Unknown = []eebusraw.UnknownField{field, field}
		}},
		{"feature", func(snapshot *SnapshotV1) {
			snapshot.Topology.Devices[1].Entities[0].Features[0].Unknown = []eebusraw.UnknownField{field, field}
		}},
		{"usecase", func(snapshot *SnapshotV1) {
			snapshot.Topology.Devices[1].UseCaseClaims[0].Unknown = []eebusraw.UnknownField{field, field}
		}},
	}
	for _, test := range duplicateUnknown {
		t.Run(test.name, func(t *testing.T) {
			snapshot := rawSnapshotV1(t, false)
			test.mutate(&snapshot)
			if _, err := NewSnapshotV1(snapshot); err == nil || !strings.Contains(err.Error(), "duplicates unknown field") {
				t.Fatalf("NewSnapshotV1() error = %v, want duplicate unknown rejection", err)
			}
		})
	}
}

func TestRawSnapshotV1RawEvidenceOrderingMatchesV1(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	sizeTen := eebusevidence.NewObjectV1(eebusevidence.ObjectKindIdentity, rawSnapshotDigest("e"), 10, now)
	sizeTwo := eebusevidence.NewObjectV1(eebusevidence.ObjectKindIdentity, rawSnapshotDigest("e"), 2, now)
	snapshot := rawSnapshotV1(t, false)
	snapshot.Raw = []eebusevidence.ObjectV1{sizeTen, sizeTwo}
	result, err := NewSnapshotV1(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if result.Raw[0].Size != 2 || result.Raw[1].Size != 10 {
		t.Fatalf("raw evidence order sizes = %d, %d; want 2, 10", result.Raw[0].Size, result.Raw[1].Size)
	}
}

func TestRawSnapshotV1FormatRedactsEveryVerb(t *testing.T) {
	snapshot, err := NewSnapshotV1(rawSnapshotV1(t, false))
	if err != nil {
		t.Fatal(err)
	}
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

func TestRawSnapshotV1CanonicalHashAndDetachment(t *testing.T) {
	source := rawSnapshotV1(t, false)
	first, err := NewSnapshotV1(source)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewSnapshotV1(rawSnapshotV1(t, true))
	if err != nil {
		t.Fatal(err)
	}

	firstHash, err := first.ComputeDataHash()
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := second.ComputeDataHash()
	if err != nil {
		t.Fatal(err)
	}
	if firstHash != secondHash {
		t.Fatalf("data_hash changed with equivalent ordering: %s != %s", firstHash, secondHash)
	}
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatalf("canonical JSON changed with equivalent ordering:\n%s\n%s", firstJSON, secondJSON)
	}

	for _, mutation := range []struct {
		name   string
		mutate func(*SnapshotV1)
	}{
		{"runtime", func(snapshot *SnapshotV1) {
			snapshot.Meta.Runtime = rawSnapshotID(t, eebusraw.IDKindPeer, "other-runtime")
		}},
		{"local_ski", func(snapshot *SnapshotV1) {
			snapshot.Meta.LocalSKI = rawSnapshotID(t, eebusraw.IDKindLocalSKI, "other-local-ski")
		}},
		{"data_timestamp", func(snapshot *SnapshotV1) {
			snapshot.Meta.DataTimestamp = snapshot.Meta.DataTimestamp.Add(time.Second)
		}},
	} {
		t.Run("hash-binds-"+mutation.name, func(t *testing.T) {
			bound := first.Clone()
			mutation.mutate(&bound)
			bound.Meta.DataHash = ""
			boundHash, err := bound.ComputeDataHash()
			if err != nil {
				t.Fatal(err)
			}
			if firstHash == boundHash {
				t.Fatalf("data_hash did not bind %s", mutation.name)
			}
		})
	}

	captureChanged := first.Clone()
	captureChanged.Meta.CapturedAt = captureChanged.Meta.CapturedAt.Add(time.Hour)
	captureChanged.Meta.DataHash = ""
	captureHash, err := captureChanged.ComputeDataHash()
	if err != nil {
		t.Fatal(err)
	}
	if firstHash != captureHash {
		t.Fatal("data_hash included captured_at")
	}

	forged := first.Clone()
	forged.Meta.DataHash = "sha256:" + strings.Repeat("0", 64)
	if err := forged.Validate(); err == nil {
		t.Fatal("Validate() accepted a forged data_hash")
	}

	source.Services[0].Unknown[0].Value = eebusraw.OpaqueBytes([]byte("mutated-source"))
	source.Raw[0].Unknown[0].Value = eebusraw.OpaqueBytes([]byte("mutated-source-raw"))
	afterSourceMutation, err := first.ComputeDataHash()
	if err != nil {
		t.Fatal(err)
	}
	if afterSourceMutation != firstHash {
		t.Fatal("NewSnapshotV1 retained caller-owned nested storage")
	}

	clone := first.Clone()
	clone.Services[0].Unknown[0].Value = eebusraw.OpaqueBytes([]byte("mutated-clone"))
	clone.Raw[0].Unknown[0].Value = eebusraw.OpaqueBytes([]byte("mutated-clone-raw"))
	afterCloneMutation, err := first.ComputeDataHash()
	if err != nil {
		t.Fatal(err)
	}
	if afterCloneMutation != firstHash {
		t.Fatal("Clone retained caller-owned nested storage")
	}
}

func rawSnapshotV1(t *testing.T, reverse bool) SnapshotV1 {
	t.Helper()
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	serviceA := ServiceV1{
		ID:      rawSnapshotID(t, eebusraw.IDKindPeer, "service-a"),
		Kind:    ServiceKindV1Local,
		Visible: true,
		Paired:  true,
		Unknown: []eebusraw.UnknownField{rawSnapshotUnknown("service-a")},
	}
	serviceB := ServiceV1{
		ID:      rawSnapshotID(t, eebusraw.IDKindPeer, "service-b"),
		Kind:    ServiceKindV1Remote,
		Visible: true,
		Paired:  true,
		Unknown: []eebusraw.UnknownField{rawSnapshotUnknown("service-b")},
	}
	sessionA := SessionV1{
		ID:     rawSnapshotID(t, eebusraw.IDKindSession, "session-a"),
		Remote: rawSnapshotID(t, eebusraw.IDKindPeer, "remote-a"),
		State:  ObservedSessionStateV1Connected,
	}
	sessionB := SessionV1{
		ID:     rawSnapshotID(t, eebusraw.IDKindSession, "session-b"),
		Remote: rawSnapshotID(t, eebusraw.IDKindPeer, "remote-b"),
		State:  ObservedSessionStateV1Disconnected,
	}
	deviceA := DeviceV1{
		ID: rawSnapshotID(t, eebusraw.IDKindPeer, "device-a"),
		Entities: []EntityV1{{
			ID: rawSnapshotID(t, eebusraw.IDKindPeer, "entity-a"),
			Features: []FeatureV1{
				{ID: rawSnapshotID(t, eebusraw.IDKindPeer, "feature-a"), Role: FeatureRoleV1Client},
				{ID: rawSnapshotID(t, eebusraw.IDKindPeer, "feature-b"), Role: FeatureRoleV1Server},
			},
		}},
		UseCaseClaims: []UseCaseClaimV1{
			{ID: rawSnapshotID(t, eebusraw.IDKindPeer, "usecase-a")},
			{ID: rawSnapshotID(t, eebusraw.IDKindPeer, "usecase-b")},
		},
	}
	deviceB := DeviceV1{ID: rawSnapshotID(t, eebusraw.IDKindPeer, "device-b")}
	rawA := eebusevidence.NewObjectV1(eebusevidence.ObjectKindIdentity, rawSnapshotDigest("a"), 1, now)
	rawA.Unknown = []eebusraw.UnknownField{rawSnapshotUnknown("raw-a")}
	rawB := eebusevidence.NewObjectV1(eebusevidence.ObjectKindUnknown, rawSnapshotDigest("b"), 2, now)
	rawB.Unknown = []eebusraw.UnknownField{rawSnapshotUnknown("raw-b")}

	snapshot := SnapshotV1{
		Meta: SnapshotMetaV1{
			Contract:      SnapshotContractV1,
			Runtime:       rawSnapshotID(t, eebusraw.IDKindPeer, "runtime"),
			LocalSKI:      rawSnapshotID(t, eebusraw.IDKindLocalSKI, "local-ski"),
			MaskTier:      eebusraw.MaskTierRedacted,
			CapturedAt:    now.Add(time.Minute),
			DataTimestamp: now,
		},
		Status: RuntimeObservationV1{State: ObservedRuntimeStateV1Ready},
		Pairing: []PairingObservationV1{
			{Remote: rawSnapshotID(t, eebusraw.IDKindPeer, "remote-b"), State: eebusraw.PairingStateUnknown},
			{Remote: rawSnapshotID(t, eebusraw.IDKindPeer, "remote-a"), State: eebusraw.PairingStateUnknown},
		},
		Services: []ServiceV1{serviceB, serviceA},
		Sessions: []SessionV1{sessionB, sessionA},
		Topology: TopologyV1{Devices: []DeviceV1{deviceB, deviceA}},
		Raw:      []eebusevidence.ObjectV1{rawB, rawA},
	}
	if reverse {
		snapshot.Pairing[0], snapshot.Pairing[1] = snapshot.Pairing[1], snapshot.Pairing[0]
		snapshot.Services[0], snapshot.Services[1] = snapshot.Services[1], snapshot.Services[0]
		snapshot.Sessions[0], snapshot.Sessions[1] = snapshot.Sessions[1], snapshot.Sessions[0]
		snapshot.Topology.Devices[0], snapshot.Topology.Devices[1] = snapshot.Topology.Devices[1], snapshot.Topology.Devices[0]
		snapshot.Topology.Devices[0].Entities[0].Features[0], snapshot.Topology.Devices[0].Entities[0].Features[1] = snapshot.Topology.Devices[0].Entities[0].Features[1], snapshot.Topology.Devices[0].Entities[0].Features[0]
		snapshot.Topology.Devices[0].UseCaseClaims[0], snapshot.Topology.Devices[0].UseCaseClaims[1] = snapshot.Topology.Devices[0].UseCaseClaims[1], snapshot.Topology.Devices[0].UseCaseClaims[0]
		snapshot.Raw[0], snapshot.Raw[1] = snapshot.Raw[1], snapshot.Raw[0]
	}
	return snapshot
}

func rawSnapshotID(t *testing.T, kind eebusraw.IDKind, raw string) eebusraw.RedactedID {
	t.Helper()
	id, err := eebusraw.RedactID(kind, raw)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func rawSnapshotUnknown(raw string) eebusraw.UnknownField {
	return eebusraw.UnknownField{
		Path:  eebusraw.UnknownPathDevice,
		Value: eebusraw.OpaqueBytes([]byte(raw)),
	}
}

func rawSnapshotDigest(fill string) string {
	return "sha256:" + strings.Repeat(fill, 64)
}

func assertSnapshotV1Fields(t *testing.T, typ reflect.Type, want []string) {
	t.Helper()
	if typ.NumField() != len(want) {
		t.Fatalf("%s field count = %d, want %d", typ, typ.NumField(), len(want))
	}
	for index, expected := range want {
		name, jsonName, _ := strings.Cut(expected, ":")
		field := typ.Field(index)
		if field.Name != name {
			t.Fatalf("%s field %d = %s, want %s", typ, index, field.Name, name)
		}
		actualJSON, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if actualJSON != jsonName {
			t.Fatalf("%s.%s JSON name = %q, want %q", typ, name, actualJSON, jsonName)
		}
	}
}
