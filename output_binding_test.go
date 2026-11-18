package eebusruntime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOutputBinding(t *testing.T) {
	binding := newOutputBinding(t)
	if binding.AdvertisementCount() != 1 || binding.ServiceCount() != 1 {
		t.Fatalf("advertisements=%d services=%d", binding.AdvertisementCount(), binding.ServiceCount())
	}
	if binding.DeviceType() != "EnergyManagementSystem" {
		t.Fatalf("device type %q", binding.DeviceType())
	}
	types := binding.EntityTypes()
	if len(types) != 1 || types[0] != "CEM" {
		t.Fatalf("entities %v", types)
	}
	if outputBindingHasType(types, "Inverter") || outputBindingHasType(types, "EVSE") || outputBindingHasType(types, "PV String") {
		t.Fatalf("entity before a lens result %v", types)
	}
	for _, address := range binding.EntityAddresses() {
		if outputBindingHasDigit(address) {
			t.Fatalf("identifier %q contains a digit", address)
		}
	}

	loss := binding.Apply(outputBindingInverter("north"))
	if len(loss) != 0 {
		t.Fatalf("loss %v", loss)
	}
	types = binding.EntityTypes()
	if !outputBindingHasType(types, "CEM") || !outputBindingHasType(types, "Inverter") {
		t.Fatalf("entities after inverter lens %v", types)
	}
	if outputBindingHasType(types, "EVSE") {
		t.Fatalf("evse appeared before a charger lens %v", types)
	}
	if binding.AdvertisementCount() != 1 || binding.ServiceCount() != 1 || binding.DeviceType() != "EnergyManagementSystem" {
		t.Fatal("existing node changed")
	}
}

func TestOutputBindingResourceFields(t *testing.T) {
	// Resource fields are family, lens admitted, runtime enabled, input dimension,
	// parent and contains cited.
	// family: a heating lens adds no entity. The production gate admits inverter,
	// pv string and charger only.
	// lens admitted: false adds no entity. The production gate returns before allocation.
	// runtime enabled: false adds no entity. The production gate returns before allocation.
	// input dimension: true adds no pv string. The production gate treats that fact as not a string.
	// parent: an empty parent adds no pv string. The production gate requires the inverter resource.
	// contains cited: false adds no pv string. The production gate requires the cited contains instance.
	binding := newOutputBinding(t)
	if len(binding.Apply(outputBindingInverter("north"))) != 0 {
		t.Fatal("inverter lens")
	}
	base := lensResult{
		Family: "pv string", ResourceID: "east", ParentResourceID: "north",
		LensAdmitted: true, RuntimeEnabled: true, ContainsCited: true,
	}
	if loss := binding.Apply(base); len(loss) != 0 || !outputBindingHasType(binding.EntityTypes(), "PV String") {
		t.Fatalf("baseline string loss=%v types=%v", loss, binding.EntityTypes())
	}
	if binding.ParentAddress("east") == "" || binding.ParentAddress("east") != binding.EntityAddress("north") {
		t.Fatal("string parent is not the inverter")
	}

	cases := []struct {
		name   string
		mutate func(*lensResult)
	}{
		{"family", func(result *lensResult) { result.Family = "heating"; result.ResourceID = "heat" }},
		{"lens admitted", func(result *lensResult) { result.LensAdmitted = false; result.ResourceID = "west" }},
		{"runtime enabled", func(result *lensResult) { result.RuntimeEnabled = false; result.ResourceID = "south" }},
		{"input dimension", func(result *lensResult) { result.InputDimension = true; result.ResourceID = "flat" }},
		{"parent", func(result *lensResult) { result.ParentResourceID = ""; result.ResourceID = "orphan" }},
		{"contains cited", func(result *lensResult) { result.ContainsCited = false; result.ResourceID = "loose" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mutated := base
			tc.mutate(&mutated)
			if loss := binding.Apply(mutated); len(loss) != 0 {
				t.Fatalf("loss %v", loss)
			}
			if binding.EntityAddress(mutated.ResourceID) != "" {
				t.Fatalf("field %s still published %q", tc.name, mutated.ResourceID)
			}
		})
	}
	if loss := binding.Apply(lensResult{
		Family: "battery", ResourceID: "pack", LensAdmitted: true, RuntimeEnabled: true,
	}); len(loss) != 0 || binding.EntityAddress("pack") != "" {
		t.Fatal("battery published")
	}
	if loss := binding.Apply(lensResult{
		Family: "charger", ResourceID: "bay", LensAdmitted: true, RuntimeEnabled: true,
	}); len(loss) != 0 || !outputBindingHasType(binding.EntityTypes(), "EVSE") {
		t.Fatal("charger not published")
	}
}

func TestOutputBindingBasisAndMerge(t *testing.T) {
	// Basis is empty, index, name or address.
	// empty basis keeps the entity. A non-empty basis returns before allocation.
	// index, name and address are the same rejection.
	// A second family for one resource identifier keeps the first entity and reports merged resources.
	binding := newOutputBinding(t)
	for _, basis := range []string{"index", "name", "address"} {
		result := outputBindingInverter("north")
		result.Basis = basis
		result.ResourceID = basis
		if len(binding.Apply(result)) != 0 || binding.EntityAddress(basis) != "" {
			t.Fatalf("basis %s published", basis)
		}
	}
	if len(binding.Apply(outputBindingInverter("north"))) != 0 {
		t.Fatal("empty basis")
	}
	charger := lensResult{Family: "charger", ResourceID: "north", LensAdmitted: true, RuntimeEnabled: true}
	loss := binding.Apply(charger)
	if len(loss) != 1 || loss[0] != "merged_resources" {
		t.Fatalf("loss %v", loss)
	}
	if !outputBindingHasType(binding.EntityTypes(), "Inverter") || outputBindingHasType(binding.EntityTypes(), "EVSE") {
		t.Fatalf("merge changed entities %v", binding.EntityTypes())
	}
}

func TestOutputBindingDeviceTypeProhibition(t *testing.T) {
	// Prohibition fields are the use-case text, the actor and the device type named in that text.
	// use-case text: without the prohibition word the entity is added. The production gate
	// requires that word before it refuses.
	// actor: text that names a different actor still adds this entity. The production gate
	// requires the actor under test to be the one named.
	// device type: text that forbids another type still adds the entity. The production gate
	// requires the node device type to be the one named.
	// A matching prohibition adds nothing and the loss names the prohibition.
	binding := newOutputBinding(t)
	refused := outputBindingInverter("north")
	refused.Actor = "Inverter"
	refused.UseCaseText = "The actor Inverter forbids device type EnergyManagementSystem"
	loss := binding.Apply(refused)
	if len(loss) != 2 || loss[0] != "device_type_prohibition" || loss[1] != "EnergyManagementSystem" {
		t.Fatalf("loss %v", loss)
	}
	if binding.EntityAddress("north") != "" || len(binding.EntityTypes()) != 1 {
		t.Fatalf("prohibition added an entity %v", binding.EntityTypes())
	}

	permitted := refused
	permitted.ResourceID = "south"
	permitted.UseCaseText = "The actor Inverter describes device type EnergyManagementSystem"
	if len(binding.Apply(permitted)) != 0 || binding.EntityAddress("south") == "" {
		t.Fatal("text without the prohibition was refused")
	}
	otherActor := refused
	otherActor.ResourceID = "bay"
	otherActor.Family = "charger"
	otherActor.Actor = "EVSE"
	if len(binding.Apply(otherActor)) != 0 || binding.EntityAddress("bay") == "" {
		t.Fatal("text naming another actor was refused")
	}
	otherType := refused
	otherType.ResourceID = "roof"
	otherType.UseCaseText = "The actor Inverter forbids device type GridGuard"
	if len(binding.Apply(otherType)) != 0 || binding.EntityAddress("roof") == "" {
		t.Fatal("text forbidding another type was refused")
	}
}

func TestOutputBindingAddressIdentity(t *testing.T) {
	// Address fields are the resource identifier and the stored address.
	// resource identifier: a different identifier receives a different address.
	// The production map is keyed by that identifier alone.
	// stored address: a later note of another address is rejected and the first address remains.
	// The production note refuses a different stored address.
	// A new binding on the same directory keeps the address. The production load reads the stored token.
	// Every address is letters only.
	directory := t.TempDir()
	first := newOutputBindingAt(t, directory)
	if len(first.Apply(outputBindingInverter("north"))) != 0 {
		t.Fatal("north")
	}
	if len(first.Apply(outputBindingInverter("south"))) != 0 {
		t.Fatal("south")
	}
	north := first.EntityAddress("north")
	south := first.EntityAddress("south")
	if north == "" || south == "" || north == south {
		t.Fatalf("addresses north=%q south=%q", north, south)
	}
	for _, address := range first.EntityAddresses() {
		if outputBindingHasDigit(address) || address == "" {
			t.Fatalf("identifier %q", address)
		}
	}
	if err := first.noteAddress("north", "other"); err == nil {
		t.Fatal("replacement admitted")
	}
	if first.addresses["north"] != north || first.EntityAddress("north") != north {
		t.Fatal("stored address changed")
	}
	second := newOutputBindingAt(t, directory)
	if len(second.Apply(outputBindingInverter("north"))) != 0 {
		t.Fatal("restart apply")
	}
	if second.EntityAddress("north") != north {
		t.Fatalf("address after restart %q", second.EntityAddress("north"))
	}
	conflict := filepath.Join(directory, "entity-addresses")
	if err := os.WriteFile(conflict, []byte("east a\neast b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewOutputBinding(directory); err == nil {
		t.Fatal("conflicting stored address admitted")
	}
}

func TestOutputBindingServerFeatures(t *testing.T) {
	// Feature fields are the cited element, the operation, its unit, own capability and converted.
	// cited element: an admitted inverter element becomes the matching server feature.
	// apparent power, reactive power, line-to-line voltage and string dc add no feature.
	// operation: the production limit maps to load control. The system export operation does not.
	// unit: amperes do not map a watt limit. The production gate requires watts for that limit.
	// own capability: visibility without it adds no load control. The production gate ignores visibility.
	// converted: true adds no load control. The production gate rejects a converted value.
	// allocated current on a charger maps to load control. An active-power limit on a charger does not.
	binding := newOutputBinding(t)
	inverter := outputBindingInverter("north")
	inverter.Elements = []string{"inverter ac measurement", "energy", "temperature", "operating state", "apparent power", "reactive power", "line-to-line voltage", "string dc"}
	inverter.Operations = []lensOperation{{
		Name: "pv.operation.set_active_power_limit", Unit: "W", OwnCapability: true,
	}}
	if len(binding.Apply(inverter)) != 0 {
		t.Fatal("inverter")
	}
	features := binding.ServerFeatures("north")
	for _, want := range []string{"measurement", "device diagnosis", "electrical connection", "load control"} {
		if !outputBindingHasType(features, want) {
			t.Fatalf("missing %s in %v", want, features)
		}
	}
	if outputBindingHasType(features, "apparent power") || outputBindingHasType(features, "string dc") {
		t.Fatalf("rejected element became a feature %v", features)
	}

	cases := []struct {
		name       string
		resourceID string
		operation  lensOperation
		want       bool
	}{
		{"system export", "exportop", lensOperation{Name: "system export", Unit: "W", OwnCapability: true}, false},
		{"amperes", "ampereunit", lensOperation{Name: "pv.operation.set_active_power_limit", Unit: "A", OwnCapability: true}, false},
		{"visibility", "visibleonly", lensOperation{Name: "pv.operation.set_active_power_limit", Unit: "W"}, false},
		{"converted", "convertedop", lensOperation{Name: "pv.operation.set_active_power_limit", Unit: "W", OwnCapability: true, Converted: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			one := newOutputBinding(t)
			result := outputBindingInverter("roof")
			result.ResourceID = tc.resourceID
			result.Operations = []lensOperation{tc.operation}
			if len(one.Apply(result)) != 0 {
				t.Fatal("apply")
			}
			if outputBindingHasType(one.ServerFeatures(result.ResourceID), "load control") != tc.want {
				t.Fatalf("load control=%v features %v", tc.want, one.ServerFeatures(result.ResourceID))
			}
		})
	}
	charger := newOutputBinding(t)
	admitted := lensResult{
		Family: "charger", ResourceID: "bay", LensAdmitted: true, RuntimeEnabled: true,
		Operations: []lensOperation{{Name: "evse.operation.set_allocated_current", Unit: "A", OwnCapability: true}},
	}
	if len(charger.Apply(admitted)) != 0 || !outputBindingHasType(charger.ServerFeatures("bay"), "load control") {
		t.Fatalf("allocated current %v", charger.ServerFeatures("bay"))
	}
	watts := admitted
	watts.ResourceID = "otherbay"
	watts.Operations = []lensOperation{{Name: "active power", Unit: "W", OwnCapability: true}}
	if len(charger.Apply(watts)) != 0 || outputBindingHasType(charger.ServerFeatures("otherbay"), "load control") {
		t.Fatal("charger active power became load control")
	}
}

func TestOutputBindingAnnouncement(t *testing.T) {
	// Announcement fields are overlapping, device actor, missing elements and use case.
	// overlapping: true is the conflict loss and does not map the write.
	// The production gate returns that loss before feature mapping.
	// device actor: EV is not announced. The production gate returns that actor and adds no EV entity.
	// missing elements: the loss names each cited element and does not invent a feature for it.
	// use case: Limitation of Power Consumption stays unannounced when the watt write is not applied.
	// The production gate returns that loss for this use case on a charger.
	binding := newOutputBinding(t)
	overlap := outputBindingInverter("north")
	overlap.Overlapping = true
	overlap.UseCase = "Limitation of Power Production"
	overlap.Operations = []lensOperation{{Name: "pv.operation.set_active_power_limit", Unit: "W", OwnCapability: true}}
	loss := binding.Apply(overlap)
	if len(loss) != 1 || loss[0] != "conflict" {
		t.Fatalf("loss %v", loss)
	}
	if outputBindingHasType(binding.ServerFeatures("north"), "load control") {
		t.Fatal("overlapping write mapped")
	}
	if !outputBindingHasType(binding.EntityTypes(), "Inverter") {
		t.Fatal("conflict removed the entity")
	}

	ev := lensResult{Family: "ev", ResourceID: "car", LensAdmitted: true, RuntimeEnabled: true, DeviceActor: "EV", UseCase: "Overload protection"}
	loss = binding.Apply(ev)
	if len(loss) != 1 || loss[0] != "EV" || binding.EntityAddress("car") != "" {
		t.Fatalf("ev loss %v address %q", loss, binding.EntityAddress("car"))
	}

	partial := outputBindingInverter("south")
	partial.UseCase = "Monitoring of Inverter"
	partial.MissingElements = []string{"energy", "temperature"}
	partial.Elements = []string{"energy", "temperature", "operating state"}
	loss = binding.Apply(partial)
	if len(loss) != 2 || loss[0] != "energy" || loss[1] != "temperature" {
		t.Fatalf("loss %v", loss)
	}
	if outputBindingHasType(binding.ServerFeatures("south"), "measurement") {
		t.Fatal("missing element became a feature")
	}
	if !outputBindingHasType(binding.ServerFeatures("south"), "device diagnosis") {
		t.Fatalf("present element dropped %v", binding.ServerFeatures("south"))
	}

	charger := lensResult{
		Family: "charger", ResourceID: "bay", LensAdmitted: true, RuntimeEnabled: true,
		UseCase:    "Limitation of Power Consumption",
		Operations: []lensOperation{{Name: "evse.operation.set_allocated_current", Unit: "A", OwnCapability: true}},
	}
	loss = binding.Apply(charger)
	if len(loss) != 1 || loss[0] != "consumption_limit_not_applied" {
		t.Fatalf("loss %v", loss)
	}
	if !outputBindingHasType(binding.EntityTypes(), "EVSE") {
		t.Fatal("charger entity missing")
	}
	if !outputBindingHasType(binding.ServerFeatures("bay"), "load control") {
		t.Fatal("allocated current was dropped")
	}
}

func newOutputBinding(t *testing.T) *OutputBinding {
	t.Helper()
	return newOutputBindingAt(t, t.TempDir())
}

func newOutputBindingAt(t *testing.T, directory string) *OutputBinding {
	t.Helper()
	binding, err := NewOutputBinding(directory)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func outputBindingInverter(resourceID string) lensResult {
	return lensResult{Family: "inverter", ResourceID: resourceID, LensAdmitted: true, RuntimeEnabled: true}
}

func outputBindingHasType(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func outputBindingHasDigit(value string) bool {
	for _, r := range value {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}
