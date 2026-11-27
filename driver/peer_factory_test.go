package driver

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	eebusruntime "github.com/Project-Helianthus/helianthus-eebusreg"
	semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"
	"github.com/Project-Helianthus/helianthus-semreg/semreg/v1/catalog"
	semdriver "github.com/Project-Helianthus/helianthus-semreg/semreg/v1/driver"
)

func TestPeerFactoryKeepsTwoInstancesAndTwoResourcesDistinct(t *testing.T) {
	root := t.TempDir()
	eastRoot := filepath.Join(root, "east")
	westRoot := filepath.Join(root, "west")
	eastPower := watt("15", 2)
	westPower := watt("5", 0)
	east := []eebusruntime.OutputPowerLimitFallback{
		{Resource: "resource.east.heater", Direction: eebusruntime.PowerLimitDirectionConsumption, Power: eastPower},
		{Resource: "resource.east.meter", Direction: eebusruntime.PowerLimitDirectionProduction, Power: watt("15", -1)},
	}
	west := []eebusruntime.OutputPowerLimitFallback{
		{Resource: "resource.west.heater", Direction: eebusruntime.PowerLimitDirectionConsumption, Power: westPower},
	}
	eastConfig := runtimeConfig(eastRoot, "test-interface-east", "192.0.2.10:4711", east)
	westConfig := runtimeConfig(westRoot, "test-interface-west", "192.0.2.11:4712", west)

	eastInstance, err := New("heating.east", eastConfig)
	if err != nil {
		t.Fatal(err)
	}
	westInstance, err := New("heating.west", westConfig)
	if err != nil {
		t.Fatal(err)
	}
	eastConfig.OutputPowerLimitFallbacks[0].Resource = "resource.replaced"
	eastConfig.OutputPowerLimitFallbacks[0].Power.Number.Coefficient = "9"
	eastConfig.OutputPowerLimitFallbacks[0].Power.Number.Exponent10 = 0

	gotEast := eastInstance.(*peerInstance)
	gotWest := westInstance.(*peerInstance)
	if gotEast.runtime == gotWest.runtime {
		t.Fatal("two instances share one runtime")
	}
	if gotEast.key == gotWest.key || gotEast.config.StateRoot == gotWest.config.StateRoot {
		t.Fatal("instance key or state root was shared")
	}
	if gotEast.config.Interface == gotWest.config.Interface || gotEast.config.ListenAddress == gotWest.config.ListenAddress {
		t.Fatal("interface or listen address was shared")
	}
	if len(gotEast.config.OutputPowerLimitFallbacks) != 2 || len(gotWest.config.OutputPowerLimitFallbacks) != 1 {
		t.Fatalf("fallback counts = %d and %d", len(gotEast.config.OutputPowerLimitFallbacks), len(gotWest.config.OutputPowerLimitFallbacks))
	}
	if gotEast.config.OutputPowerLimitFallbacks[0].Resource != "resource.east.heater" ||
		gotEast.config.OutputPowerLimitFallbacks[0].Power.Number.Coefficient != "15" ||
		gotEast.config.OutputPowerLimitFallbacks[0].Power.Number.Exponent10 != 2 {
		t.Fatalf("east fallback changed after the caller mutated its slice: %+v", gotEast.config.OutputPowerLimitFallbacks[0])
	}
	if gotEast.config.OutputPowerLimitFallbacks[1].Power.Number.Exponent10 != -1 {
		t.Fatalf("exponent = %d, want -1", gotEast.config.OutputPowerLimitFallbacks[1].Power.Number.Exponent10)
	}
	if fallbackFor(gotWest.config.OutputPowerLimitFallbacks, "resource.east.heater", eebusruntime.PowerLimitDirectionConsumption) != nil {
		t.Fatal("west inherited the east resource")
	}
	gotEast.config.OutputPowerLimitFallbacks[0].Power.Number.Coefficient = "4"
	if gotWest.config.OutputPowerLimitFallbacks[0].Power.Number.Coefficient != "5" {
		t.Fatal("instances share one fallback backing array")
	}
	if _, err := os.Lstat(eastRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("constructor touched the state root: %v", err)
	}
}

func TestPeerFactoryDeclaredZeroIsNotAbsence(t *testing.T) {
	root := t.TempDir()
	zero := eebusruntime.OutputPowerLimitFallback{
		Resource:  "resource.east.heater",
		Direction: eebusruntime.PowerLimitDirectionConsumption,
		Power:     watt("0", 0),
	}
	present, err := New("heating.east", runtimeConfig(filepath.Join(root, "east"), "test-interface-east", "192.0.2.10:4711", []eebusruntime.OutputPowerLimitFallback{zero}))
	if err != nil {
		t.Fatal(err)
	}
	absent, err := New("heating.west", runtimeConfig(filepath.Join(root, "west"), "test-interface-west", "192.0.2.11:4712", nil))
	if err != nil {
		t.Fatal(err)
	}
	gotPresent := present.(*peerInstance).config.OutputPowerLimitFallbacks
	gotAbsent := absent.(*peerInstance).config.OutputPowerLimitFallbacks
	if len(gotPresent) != 1 || gotPresent[0].Power.Number.Coefficient != "0" || gotPresent[0].Power.Number.Exponent10 != 0 || gotPresent[0].Power.Unit != "unit.watt" {
		t.Fatalf("declared zero = %+v", gotPresent)
	}
	if gotAbsent != nil {
		t.Fatalf("absent fallbacks = %+v, want nil", gotAbsent)
	}
}

func TestPeerFactoryRejectsDuplicateAndInvalidFallbackFields(t *testing.T) {
	base := eebusruntime.OutputPowerLimitFallback{
		Resource:  "resource.east.heater",
		Direction: eebusruntime.PowerLimitDirectionConsumption,
		Power:     watt("15", 2),
	}
	otherResource := base
	otherResource.Resource = "resource.west.heater"
	otherDirection := base
	otherDirection.Direction = eebusruntime.PowerLimitDirectionProduction
	valid, err := New("heating.east", runtimeConfig(filepath.Join(t.TempDir(), "state"), "test-interface", "192.0.2.10:4711", []eebusruntime.OutputPowerLimitFallback{base, otherResource, otherDirection}))
	if err != nil {
		t.Fatal(err)
	}
	if len(valid.(*peerInstance).config.OutputPowerLimitFallbacks) != 3 {
		t.Fatal("same resource with two directions, or two resources with one direction, was rejected")
	}

	duplicate := base
	cases := []struct {
		name   string
		entry  eebusruntime.OutputPowerLimitFallback
		second *eebusruntime.OutputPowerLimitFallback
		want   string
		absent string
	}{
		{name: "empty resource", entry: mutateFallback(base, func(entry *eebusruntime.OutputPowerLimitFallback) { entry.Resource = "" }), want: "resource is empty"},
		{name: "invalid resource", entry: mutateFallback(base, func(entry *eebusruntime.OutputPowerLimitFallback) { entry.Resource = " resource.east.heater" }), want: "resource is invalid", absent: "resource.east.heater"},
		{name: "empty direction", entry: mutateFallback(base, func(entry *eebusruntime.OutputPowerLimitFallback) { entry.Direction = "" }), want: "direction is empty"},
		{name: "invalid direction", entry: mutateFallback(base, func(entry *eebusruntime.OutputPowerLimitFallback) { entry.Direction = "export" }), want: "direction is invalid", absent: "export"},
		{name: "empty unit", entry: mutateFallback(base, func(entry *eebusruntime.OutputPowerLimitFallback) { entry.Power.Unit = "" }), want: "unit is empty"},
		{name: "invalid unit", entry: mutateFallback(base, func(entry *eebusruntime.OutputPowerLimitFallback) { entry.Power.Unit = "unit.volt" }), want: "unit is invalid", absent: "unit.volt"},
		{name: "negative power", entry: mutateFallback(base, func(entry *eebusruntime.OutputPowerLimitFallback) { entry.Power.Number.Coefficient = "-15" }), want: "power is negative", absent: "-15"},
		{name: "invalid power", entry: mutateFallback(base, func(entry *eebusruntime.OutputPowerLimitFallback) { entry.Power.Number.Coefficient = "1500" }), want: "power is invalid", absent: "1500"},
		{name: "duplicate resource and direction", entry: base, second: &duplicate, want: "fallback 1 duplicates resource and direction", absent: "resource.east.heater"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			entries := []eebusruntime.OutputPowerLimitFallback{test.entry}
			if test.second != nil {
				entries = []eebusruntime.OutputPowerLimitFallback{base, *test.second}
			}
			_, err := New("heating.east", runtimeConfig(filepath.Join(t.TempDir(), "state"), "test-interface", "192.0.2.10:4711", entries))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			if test.absent != "" && strings.Contains(err.Error(), test.absent) {
				t.Fatalf("error contains configured value %q: %v", test.absent, err)
			}
		})
	}
}

func TestPeerFactoryPlansRecognizedConfigurationOnly(t *testing.T) {
	root := t.TempDir()
	factory := NewFactory()
	other := NewFactory()
	east := runtimeConfig(filepath.Join(root, "east"), "test-interface-east", "192.0.2.10:4711", []eebusruntime.OutputPowerLimitFallback{{
		Resource: "resource.east.heater", Direction: eebusruntime.PowerLimitDirectionConsumption, Power: watt("0", 0),
	}})
	west := runtimeConfig(filepath.Join(root, "west"), "test-interface-west", "192.0.2.11:4712", nil)
	eastPlan, err := factory.Plan(driverConfig("heating.east", east))
	if err != nil || len(eastPlan) != 1 || eastPlan[0].Driver != Kind || eastPlan[0].Instance != "heating.east" || len(eastPlan[0].Transports) != 0 {
		t.Fatalf("east plan = %+v, error %v", eastPlan, err)
	}
	westPlan, err := factory.Plan(driverConfig("heating.west", west))
	if err != nil || len(westPlan) != 1 || westPlan[0].Instance != "heating.west" {
		t.Fatalf("west plan = %+v, error %v", westPlan, err)
	}
	foreign, err := factory.Plan(semdriver.Config{Instance: "other.kind", Enabled: true, Options: []byte(`{"profile":"foreign"}`)})
	if err != nil || foreign != nil {
		t.Fatalf("foreign plan = %+v, error %v", foreign, err)
	}
	enabledOnly, err := factory.Plan(semdriver.Config{Instance: "enabled.only", Enabled: true, Options: []byte(`{"Enabled":true}`)})
	if err != nil || enabledOnly != nil {
		t.Fatalf("enabled-only plan = %+v, error %v", enabledOnly, err)
	}
	if _, err := factory.Plan(semdriver.Config{Instance: "heating.broken", Enabled: true, Options: []byte(`{"StateRoot":"/tmp/state","Enabled":false}`)}); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched enabled flag error = %v", err)
	}
	badUnit, _ := json.Marshal(east)
	badUnit = bytesReplace(badUnit, []byte("unit.watt"), []byte("unit.volt"))
	if _, err := factory.Plan(semdriver.Config{Instance: "heating.bad", Enabled: true, Options: badUnit}); err == nil || !strings.Contains(err.Error(), "unit is invalid") || strings.Contains(err.Error(), "unit.volt") {
		t.Fatalf("invalid unit plan error = %v", err)
	}

	eastInstance, err := factory.Construct(context.Background(), eastPlan[0])
	if err != nil {
		t.Fatal(err)
	}
	westInstance, err := factory.Construct(context.Background(), westPlan[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Construct(context.Background(), eastPlan[0]); err == nil {
		t.Fatal("another factory constructed an instance it did not plan")
	}
	if _, err := factory.Construct(context.Background(), semdriver.InstancePlan{Driver: Kind, Instance: "heating.absent"}); err == nil {
		t.Fatal("construct without a plan succeeded")
	}
	gotEast := eastInstance.(*peerInstance).config.OutputPowerLimitFallbacks
	gotWest := westInstance.(*peerInstance).config.OutputPowerLimitFallbacks
	if len(gotEast) != 1 || gotEast[0].Power.Number.Coefficient != "0" || gotWest != nil {
		t.Fatalf("planned fallbacks = %+v and %+v", gotEast, gotWest)
	}
	if eastInstance.(*peerInstance).runtime == westInstance.(*peerInstance).runtime {
		t.Fatal("planned instances share one runtime")
	}
}

func TestPeerFactoryDescriptorDeclaresQualifiedInboundFamily(t *testing.T) {
	descriptor := NewFactory().Descriptor()
	if err := descriptor.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(descriptor.Kinds) != 1 || descriptor.Kinds[0] != Kind {
		t.Fatalf("kinds = %+v", descriptor.Kinds)
	}
	if len(descriptor.Families) != 1 {
		t.Fatalf("families = %d", len(descriptor.Families))
	}
	family := descriptor.Families[0]
	if family.FamilyID != "eebus-inbound" || family.State != semreg.QualificationQualified || family.Reason != catalog.ReasonDeclaredBySpecification {
		t.Fatalf("family qualification = %+v", family)
	}
	if family.Verification != catalog.VerificationNotTestedOnHardware || family.Composition != catalog.CompositionNotComposed || family.CompositionVerified || family.Module != "" || family.Version != "" {
		t.Fatalf("family evidence = %+v", family)
	}
	if family.SemanticPack != "" || family.FurtherSemanticPacks != nil || len(family.Evidence) != 1 || family.Evidence[0].Kind != catalog.EvidenceKindSpecification {
		t.Fatalf("family packs or evidence = %+v", family)
	}
}

func TestPeerFactoryDisabledInstanceStartsWithoutOpening(t *testing.T) {
	instance, err := New("heating.idle", eebusruntime.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.Start(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := instance.Start(context.Background(), nil); err == nil {
		t.Fatal("second start succeeded")
	}
	instance.FenceWithdrawal()
	withdrawn, err := New("heating.withdrawn", eebusruntime.Config{})
	if err != nil {
		t.Fatal(err)
	}
	withdrawn.FenceWithdrawal()
	if err := withdrawn.Start(context.Background(), nil); err == nil {
		t.Fatal("start after withdrawal succeeded")
	}
	report := instance.Stop(context.Background())
	if !report.Confirmed || report.Err != nil {
		t.Fatalf("stop = %+v", report)
	}
}

func runtimeConfig(stateRoot, iface, address string, fallbacks []eebusruntime.OutputPowerLimitFallback) eebusruntime.Config {
	return eebusruntime.Config{
		Enabled:                   true,
		StateRoot:                 stateRoot,
		Interface:                 iface,
		ListenAddress:             netip.MustParseAddrPort(address),
		PairingPolicy:             eebusruntime.PairingPolicyClosed,
		OutputPowerLimitFallbacks: fallbacks,
	}
}

func driverConfig(instance string, config eebusruntime.Config) semdriver.Config {
	raw, err := json.Marshal(config)
	if err != nil {
		panic(err)
	}
	return semdriver.Config{Instance: instance, Enabled: config.Enabled, Options: raw}
}

func watt(coefficient string, exponent int32) semreg.Quantity {
	return semreg.Quantity{Number: semreg.Decimal{Coefficient: coefficient, Exponent10: exponent}, Unit: "unit.watt"}
}

func mutateFallback(entry eebusruntime.OutputPowerLimitFallback, mutate func(*eebusruntime.OutputPowerLimitFallback)) eebusruntime.OutputPowerLimitFallback {
	mutate(&entry)
	return entry
}

func fallbackFor(entries []eebusruntime.OutputPowerLimitFallback, resource semreg.ResourceID, direction eebusruntime.PowerLimitDirection) *eebusruntime.OutputPowerLimitFallback {
	for index := range entries {
		if entries[index].Resource == resource && entries[index].Direction == direction {
			return &entries[index]
		}
	}
	return nil
}

func bytesReplace(source, old, replacement []byte) []byte {
	return []byte(strings.ReplaceAll(string(source), string(old), string(replacement)))
}
