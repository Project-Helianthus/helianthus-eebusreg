package eebusruntime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

func identityRotationVector(t *testing.T) (SnapshotV1, SnapshotV1) {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("testdata", "native_identity", "rotation.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		Before SnapshotV1 `json:"before"`
		After  SnapshotV1 `json:"after"`
	}
	if err := json.Unmarshal(payload, &vector); err != nil {
		t.Fatal(err)
	}
	runtimeID, err := eebusraw.RedactID(eebusraw.IDKindPeer, "synthetic-runtime")
	if err != nil {
		t.Fatal(err)
	}
	vector.Before.Meta.Runtime = runtimeID
	vector.After.Meta.Runtime = runtimeID
	return vector.Before, vector.After
}

func identityRotationRedacted(t *testing.T, source SnapshotV1) RedactedSnapshotV1 {
	t.Helper()
	redacted, err := BuildRedactedSnapshotV1(source)
	if err != nil {
		t.Fatal(err)
	}
	return redacted
}

func redactedIdentityKeys(ids ...eebusraw.RedactedID) []string {
	keys := make([]string, 0, len(ids))
	for _, id := range ids {
		keys = append(keys, redactedIdentityKeyV1(id))
	}
	sort.Strings(keys)
	return keys
}

type redactedNodeIdentities struct {
	devices, entities, nested, features []string
}

func redactedNodeIdentitiesOf(snapshot RedactedSnapshotV1) redactedNodeIdentities {
	var devices, entities, nested, features []eebusraw.RedactedID
	for _, device := range snapshot.Devices {
		devices = append(devices, device.ID)
		for _, entity := range device.Entities {
			nested = append(nested, entity.ID)
			for _, feature := range entity.Features {
				nested = append(nested, feature.ID)
			}
		}
	}
	for _, entity := range snapshot.Entities {
		entities = append(entities, entity.ID)
	}
	for _, feature := range snapshot.Features {
		features = append(features, feature.ID)
	}
	return redactedNodeIdentities{
		devices: redactedIdentityKeys(devices...), entities: redactedIdentityKeys(entities...),
		nested: redactedIdentityKeys(nested...), features: redactedIdentityKeys(features...),
	}
}

func TestSnapshotIdentityRotation(t *testing.T) {
	before, after := identityRotationVector(t)
	if len(before.Devices) != 2 || len(after.Devices) != 2 {
		t.Fatal("rotation vector must hold one anchored and one unresolved device")
	}
	for index := range before.Devices {
		if before.Devices[index].SKI == after.Devices[index].SKI ||
			before.Pairing[index].RemoteSKI == after.Pairing[index].RemoteSKI ||
			before.Services[index].SKI == after.Services[index].SKI ||
			before.Sessions[index].RemoteSKI == after.Sessions[index].RemoteSKI {
			t.Fatalf("rotation vector does not replace certificate %d everywhere", index)
		}
		if optionalStringV1(before.Devices[index].SHIPID) != optionalStringV1(after.Devices[index].SHIPID) ||
			before.Devices[index].Address != after.Devices[index].Address {
			t.Fatalf("rotation vector must keep the SHIP identifier and address of device %d", index)
		}
	}
	left := identityRotationRedacted(t, before)
	right := identityRotationRedacted(t, after)

	var leftRemotes, rightRemotes, leftServices, rightServices []eebusraw.RedactedID
	for index := range left.Sessions {
		leftRemotes = append(leftRemotes, left.Sessions[index].Remote)
		rightRemotes = append(rightRemotes, right.Sessions[index].Remote)
	}
	for index := range left.Services {
		leftServices = append(leftServices, left.Services[index].ID)
		rightServices = append(rightServices, right.Services[index].ID)
	}
	for index, key := range redactedIdentityKeys(leftRemotes...) {
		if key == redactedIdentityKeys(rightRemotes...)[index] {
			t.Error("session remote is a trust record and must follow the certificate")
		}
	}
	for index, key := range redactedIdentityKeys(leftServices...) {
		if key == redactedIdentityKeys(rightServices...)[index] {
			t.Error("service identity is a trust record and must follow the certificate")
		}
	}

	leftNodes, rightNodes := redactedNodeIdentitiesOf(left), redactedNodeIdentitiesOf(right)
	for _, check := range []struct {
		name        string
		left, right []string
		count       int
	}{
		{"device", leftNodes.devices, rightNodes.devices, 2},
		{"entity", leftNodes.entities, rightNodes.entities, 3},
		{"nested entity and feature", leftNodes.nested, rightNodes.nested, 6},
		{"feature", leftNodes.features, rightNodes.features, 3},
	} {
		if len(check.left) != check.count || len(check.right) != check.count {
			t.Errorf("%s count = %d/%d, want %d", check.name, len(check.left), len(check.right), check.count)
			continue
		}
		if strings.Join(check.left, "|") != strings.Join(check.right, "|") {
			t.Errorf("%s identifiers changed with the certificate", check.name)
		}
	}
}

func TestRedactedIdentifierKind(t *testing.T) {
	before, _ := identityRotationVector(t)
	redacted := identityRotationRedacted(t, before)
	for _, device := range redacted.Devices {
		if string(device.ID.Kind) != "device" {
			t.Errorf("device identifier kind = %q, want device", string(device.ID.Kind))
		}
		for _, entity := range device.Entities {
			if string(entity.ID.Kind) != "entity" {
				t.Errorf("nested entity identifier kind = %q, want entity", string(entity.ID.Kind))
			}
			for _, feature := range entity.Features {
				if string(feature.ID.Kind) != "feature" {
					t.Errorf("nested feature identifier kind = %q, want feature", string(feature.ID.Kind))
				}
			}
		}
	}
	for _, entity := range redacted.Entities {
		if string(entity.ID.Kind) != "entity" {
			t.Errorf("entity identifier kind = %q, want entity", string(entity.ID.Kind))
		}
	}
	for _, feature := range redacted.Features {
		if string(feature.ID.Kind) != "feature" {
			t.Errorf("feature identifier kind = %q, want feature", string(feature.ID.Kind))
		}
	}
	for _, service := range redacted.Services {
		if service.ID.Kind != eebusraw.IDKindPeer {
			t.Errorf("service identifier kind = %q, want peer", string(service.ID.Kind))
		}
	}
	for _, session := range redacted.Sessions {
		if session.ID.Kind != eebusraw.IDKindSession || session.Remote.Kind != eebusraw.IDKindRemoteSKI {
			t.Errorf("session identifier kinds = %q/%q, want session/remote-ski", string(session.ID.Kind), string(session.Remote.Kind))
		}
	}
}

func identityBasisDraft(t *testing.T) SnapshotV1 {
	t.Helper()
	before, _ := identityRotationVector(t)
	before.Meta.DataHash = ""
	return before
}

func identityBasisIDs(t *testing.T, source SnapshotV1) (device, entities, features string) {
	t.Helper()
	for _, value := range identityRotationRedacted(t, source).Devices {
		if len(value.Entities) != 2 {
			continue
		}
		var entityIDs, featureIDs []eebusraw.RedactedID
		for _, nested := range value.Entities {
			entityIDs = append(entityIDs, nested.ID)
			for _, leaf := range nested.Features {
				featureIDs = append(featureIDs, leaf.ID)
			}
		}
		return redactedIdentityKeyV1(value.ID), strings.Join(redactedIdentityKeys(entityIDs...), "|"),
			strings.Join(redactedIdentityKeys(featureIDs...), "|")
	}
	t.Fatal("anchored device with its entities is missing")
	return "", "", ""
}

func TestRedactedIdentifierForm(t *testing.T) {
	redacted := identityRotationRedacted(t, identityBasisDraft(t))
	want := map[string]struct{}{}
	for _, form := range []struct {
		kind     eebusraw.IDKind
		preimage string
	}{
		{eebusraw.IDKindDevice, `["device","ship_id","synthetic-ship-0001"]`},
		{eebusraw.IDKindDevice, `["device","unresolved","d:_n:synthetic_energy_manager"]`},
		{eebusraw.IDKindEntity, `["entity","ship_id","synthetic-ship-0001","[1]"]`},
		{eebusraw.IDKindEntity, `["entity","ship_id","synthetic-ship-0001","[1,1]"]`},
		{eebusraw.IDKindEntity, `["entity","unresolved","d:_n:synthetic_energy_manager","[1]"]`},
		{eebusraw.IDKindFeature, `["feature","ship_id","synthetic-ship-0001","[1]","1"]`},
		{eebusraw.IDKindFeature, `["feature","ship_id","synthetic-ship-0001","[1,1]","2"]`},
		{eebusraw.IDKindFeature, `["feature","unresolved","d:_n:synthetic_energy_manager","[1]","1"]`},
	} {
		id, err := eebusraw.RedactID(form.kind, form.preimage)
		if err != nil {
			t.Fatal(err)
		}
		want[redactedIdentityKeyV1(id)] = struct{}{}
	}
	nodes := redactedNodeIdentitiesOf(redacted)
	got := append(append(append([]string(nil), nodes.devices...), nodes.entities...), nodes.features...)
	if len(got) != len(want) {
		t.Fatalf("node identifier count = %d, want %d", len(got), len(want))
	}
	for _, key := range got {
		if _, found := want[key]; !found {
			t.Errorf("node identifier %q is not derived from its documented basis", key)
		}
	}
}

func TestRedactedIdentityBasisFields(t *testing.T) {
	baseDevice, baseEntity, baseFeature := identityBasisIDs(t, identityBasisDraft(t))
	for _, test := range []struct {
		name                    string
		mutate                  func(*SnapshotV1)
		device, entity, feature bool
	}{
		{"remote SKI", func(s *SnapshotV1) {
			s.Devices[0].SKI = strings.Repeat("6", 40)
		}, false, false, false},
		{"SHIP identifier", func(s *SnapshotV1) {
			ship := "synthetic-ship-0002"
			s.Devices[0].SHIPID = &ship
		}, true, true, true},
		{"device address under a SHIP identifier", func(s *SnapshotV1) {
			s.Devices[0].Address = "d:_n:synthetic_heat_pump_moved"
			s.Entities[0].DeviceAddress, s.Entities[1].DeviceAddress = s.Devices[0].Address, s.Devices[0].Address
			s.Features[0].DeviceAddress, s.Features[1].DeviceAddress = s.Devices[0].Address, s.Devices[0].Address
		}, false, false, false},
		{"source", func(s *SnapshotV1) {
			s.Devices[0].Address = "synthetic-ship-0001"
			s.Devices[0].SHIPID = nil
			s.Entities[0].DeviceAddress, s.Entities[1].DeviceAddress = s.Devices[0].Address, s.Devices[0].Address
			s.Features[0].DeviceAddress, s.Features[1].DeviceAddress = s.Devices[0].Address, s.Devices[0].Address
		}, true, true, true},
		{"entity address", func(s *SnapshotV1) {
			s.Entities[0].EntityAddress = "[2]"
			s.Features[0].EntityAddress = "[2]"
		}, false, true, true},
		{"feature address", func(s *SnapshotV1) {
			s.Features[0].FeatureAddress = "3"
		}, false, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			draft := identityBasisDraft(t)
			test.mutate(&draft)
			device, entity, feature := identityBasisIDs(t, draft)
			for _, check := range []struct {
				name        string
				base, value string
				changed     bool
			}{
				{"device", baseDevice, device, test.device},
				{"entity", baseEntity, entity, test.entity},
				{"feature", baseFeature, feature, test.feature},
			} {
				if (check.base != check.value) != check.changed {
					t.Errorf("%s identifier changed = %t, want %t", check.name, check.base != check.value, check.changed)
				}
			}
		})
	}

	unresolved := func(address string) string {
		draft := identityBasisDraft(t)
		draft.Devices[1].Address = address
		draft.Entities[2].DeviceAddress, draft.Features[2].DeviceAddress = address, address
		for _, device := range identityRotationRedacted(t, draft).Devices {
			if len(device.Entities) == 1 {
				return redactedIdentityKeyV1(device.ID)
			}
		}
		t.Fatal("unresolved device is missing")
		return ""
	}
	if unresolved("d:_n:synthetic_energy_manager") == unresolved("d:_n:synthetic_energy_manager_2") {
		t.Error("unresolved device identifier ignores the device address")
	}
}

func TestRedactedIdentityBasisFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*SnapshotV1)
	}{
		{"shared SHIP identifier", func(s *SnapshotV1) {
			ship := "synthetic-ship-0001"
			s.Devices[1].SHIPID = &ship
		}},
		{"entity under two devices", func(s *SnapshotV1) {
			s.Devices = append(s.Devices, DeviceV1{SKI: strings.Repeat("7", 40), Address: s.Devices[0].Address, Type: "HeatPumpAppliance"})
		}},
		{"feature without a device", func(s *SnapshotV1) {
			s.Features[2].DeviceAddress = "d:_n:synthetic_missing"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			draft := identityBasisDraft(t)
			test.mutate(&draft)
			if _, err := BuildRedactedSnapshotV1(draft); err == nil {
				t.Fatal("ambiguous identity basis was redacted")
			}
		})
	}

	redacted := identityRotationRedacted(t, identityBasisDraft(t))
	for _, mutate := range []func(*RedactedSnapshotV1){
		func(s *RedactedSnapshotV1) { s.Devices[0].ID.Kind = eebusraw.IDKindPeer },
		func(s *RedactedSnapshotV1) { s.Entities[0].ID.Kind = eebusraw.IDKindPeer },
		func(s *RedactedSnapshotV1) { s.Features[0].ID.Kind = eebusraw.IDKindPeer },
	} {
		changed := redacted
		changed.Devices = cloneRedactedDevicesV1(redacted.Devices)
		changed.Entities = cloneRedactedEntitiesV1(redacted.Entities)
		changed.Features = append([]RedactedFeatureV1(nil), redacted.Features...)
		mutate(&changed)
		if err := changed.Validate(); err == nil {
			t.Fatal("redacted node identifier with the peer kind was accepted")
		}
	}
}

func TestNativeIdentityBasis(t *testing.T) {
	ship := "synthetic-ship-0001"
	anchored := NativeDeviceV2{SKI: strings.Repeat("2", 40), SHIPID: &ship, Address: "d:_n:synthetic_heat_pump", Type: "HeatPumpAppliance"}
	rotated := anchored
	rotated.SKI = strings.Repeat("3", 40)
	blank := " "
	unresolved := NativeDeviceV2{SKI: strings.Repeat("4", 40), SHIPID: &blank, Address: "d:_n:synthetic_energy_manager", Type: "CEM"}

	text := func(value *string) string {
		if value == nil {
			return "<nil>"
		}
		return *value
	}
	describe := func(basis NativeIdentityBasis) string {
		return strings.Join([]string{basis.Kind, basis.Source, text(basis.SHIPID), text(basis.DeviceAddress), text(basis.EntityAddress), text(basis.FeatureAddress)}, "|")
	}
	for _, test := range []struct {
		name  string
		basis NativeIdentityBasis
		want  string
	}{
		{"anchored device", anchored.IdentityBasis(), "device|ship_id|synthetic-ship-0001|<nil>|<nil>|<nil>"},
		{"anchored entity", anchored.EntityIdentityBasis("[1,1]"), "entity|ship_id|synthetic-ship-0001|<nil>|[1,1]|<nil>"},
		{"anchored feature", anchored.FeatureIdentityBasis("[1,1]", "2"), "feature|ship_id|synthetic-ship-0001|<nil>|[1,1]|2"},
		{"rotated device", rotated.IdentityBasis(), "device|ship_id|synthetic-ship-0001|<nil>|<nil>|<nil>"},
		{"unresolved device", unresolved.IdentityBasis(), "device|unresolved|<nil>|d:_n:synthetic_energy_manager|<nil>|<nil>"},
		{"unresolved feature", unresolved.FeatureIdentityBasis("[1]", "1"), "feature|unresolved|<nil>|d:_n:synthetic_energy_manager|[1]|1"},
	} {
		if got := describe(test.basis); got != test.want {
			t.Errorf("%s basis = %q, want %q", test.name, got, test.want)
		}
	}

	basis := anchored.FeatureIdentityBasis("[1]", "1")
	*basis.SHIPID, *basis.EntityAddress, *basis.FeatureAddress = "changed", "changed", "changed"
	if *anchored.SHIPID != "synthetic-ship-0001" || describe(anchored.FeatureIdentityBasis("[1]", "1")) != "feature|ship_id|synthetic-ship-0001|<nil>|[1]|1" {
		t.Fatal("identity basis shares memory with its device")
	}
}
