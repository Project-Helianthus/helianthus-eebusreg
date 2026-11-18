package eebusruntime

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
)

func TestOutputBinding(t *testing.T) {
	binding := newOutputBinding(t)
	node := outputBindingNode(t, binding)
	if binding.AdvertisementCount() != node.AdvertisementCount() || binding.ServiceCount() != node.ServiceCount() {
		t.Fatalf("advertisements=%d services=%d", binding.AdvertisementCount(), binding.ServiceCount())
	}
	if binding.AdvertisementCount() != 1 || binding.ServiceCount() != 1 {
		t.Fatalf("node advertisements=%d services=%d", node.AdvertisementCount(), node.ServiceCount())
	}
	if binding.DeviceType() != node.DeviceType() || binding.DeviceType() != "EnergyManagementSystem" {
		t.Fatalf("device type %q", binding.DeviceType())
	}
	if len(node.added) != 0 {
		t.Fatalf("node entity before a lens result %v", node.added)
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
	if len(node.added) != 1 || node.added[0].kind != "Inverter" || node.added[0].address != binding.EntityAddress("north") {
		t.Fatalf("node entities after inverter lens %v", node.added)
	}
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
	base := LensResult{
		Family: "pv string", ResourceID: "east", ParentResourceID: "north",
		LensAdmitted: true, RuntimeEnabled: true, ContainsCited: true,
	}
	if loss := binding.Apply(base); len(loss) != 0 || !outputBindingHasType(binding.EntityTypes(), "PV String") {
		t.Fatalf("baseline string loss=%v types=%v", loss, binding.EntityTypes())
	}
	if binding.ParentAddress("east") == "" || binding.ParentAddress("east") != binding.EntityAddress("north") {
		t.Fatal("string parent is not the inverter")
	}
	node := outputBindingNode(t, binding)
	if node.parentOf(binding.EntityAddress("east")) != binding.EntityAddress("north") {
		t.Fatalf("string is not nested under the inverter on the node %v", node.added)
	}

	cases := []struct {
		name   string
		mutate func(*LensResult)
	}{
		{"family", func(result *LensResult) { result.Family = "heating"; result.ResourceID = "heat" }},
		{"lens admitted", func(result *LensResult) { result.LensAdmitted = false; result.ResourceID = "west" }},
		{"runtime enabled", func(result *LensResult) { result.RuntimeEnabled = false; result.ResourceID = "south" }},
		{"input dimension", func(result *LensResult) { result.InputDimension = true; result.ResourceID = "flat" }},
		{"parent", func(result *LensResult) { result.ParentResourceID = ""; result.ResourceID = "orphan" }},
		{"contains cited", func(result *LensResult) { result.ContainsCited = false; result.ResourceID = "loose" }},
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
	if loss := binding.Apply(LensResult{
		Family: "battery", ResourceID: "pack", LensAdmitted: true, RuntimeEnabled: true,
	}); len(loss) != 0 || binding.EntityAddress("pack") != "" {
		t.Fatal("battery published")
	}
	if loss := binding.Apply(LensResult{
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
	charger := LensResult{Family: "charger", ResourceID: "north", LensAdmitted: true, RuntimeEnabled: true}
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
	// exception: text that excepts this device type still adds the entity. The production gate
	// refuses only the type named as forbidden.
	// admission: a result the publication contract does not admit does not report the prohibition.
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

	exempt := outputBindingInverter("exempt")
	exempt.Actor = "Inverter"
	exempt.UseCaseText = "The actor Inverter forbids every device type except EnergyManagementSystem"
	if len(binding.Apply(exempt)) != 0 || binding.EntityAddress("exempt") == "" {
		t.Fatal("exception was refused")
	}
	kept := outputBindingInverter("kept")
	kept.Actor = "Inverter"
	kept.UseCaseText = "The actor Inverter forbids device type EnergyManagementSystem except GridGuard"
	loss = binding.Apply(kept)
	if len(loss) != 2 || loss[0] != "device_type_prohibition" || loss[1] != "EnergyManagementSystem" {
		t.Fatalf("loss %v", loss)
	}
	if binding.EntityAddress("kept") != "" {
		t.Fatal("prohibition with an unrelated exception added an entity")
	}
	silent := outputBindingInverter("silent")
	silent.LensAdmitted = false
	silent.Actor = "Inverter"
	silent.UseCaseText = "The actor Inverter forbids device type EnergyManagementSystem"
	loss = binding.Apply(silent)
	if len(loss) != 0 || binding.EntityAddress("silent") != "" {
		t.Fatalf("non-admitted prohibition loss %v", loss)
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
	if _, err := openOutputBindingOnFakeNode(t, directory, newOutputBindingFakeNode()); err == nil {
		t.Fatal("conflicting stored address admitted")
	}
}

func TestOutputBindingAddressSync(t *testing.T) {
	// The stored address is synced before its name is published.
	// A failed sync removes the temporary file and does not rename it.
	directory := t.TempDir()
	binding := newOutputBindingAt(t, directory)
	original := syncBindingFile
	syncBindingFile = func(file *os.File) error {
		if err := file.Sync(); err != nil {
			return err
		}
		return errors.New("sync failed")
	}
	t.Cleanup(func() { syncBindingFile = original })
	if err := binding.noteAddress("north", "aa"); err == nil {
		t.Fatal("unsynced address was published")
	}
	if _, err := os.Stat(filepath.Join(directory, addressFileName)); !os.IsNotExist(err) {
		t.Fatal("address file renamed before sync")
	}
	if _, err := os.Stat(filepath.Join(directory, addressFileName+".new")); !os.IsNotExist(err) {
		t.Fatal("temporary address file left behind")
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
	inverter.Operations = []LensOperation{{
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
	added := outputBindingNode(t, binding).features[binding.EntityAddress("north")]
	for _, want := range []string{"measurement", "device diagnosis", "electrical connection", "load control"} {
		if !outputBindingHasType(added, want) {
			t.Fatalf("node missing %s in %v", want, added)
		}
	}

	cases := []struct {
		name       string
		resourceID string
		operation  LensOperation
		want       bool
	}{
		{"system export", "exportop", LensOperation{Name: "system export", Unit: "W", OwnCapability: true}, false},
		{"amperes", "ampereunit", LensOperation{Name: "pv.operation.set_active_power_limit", Unit: "A", OwnCapability: true}, false},
		{"visibility", "visibleonly", LensOperation{Name: "pv.operation.set_active_power_limit", Unit: "W"}, false},
		{"converted", "convertedop", LensOperation{Name: "pv.operation.set_active_power_limit", Unit: "W", OwnCapability: true, Converted: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			one := newOutputBinding(t)
			result := outputBindingInverter("roof")
			result.ResourceID = tc.resourceID
			result.Operations = []LensOperation{tc.operation}
			if len(one.Apply(result)) != 0 {
				t.Fatal("apply")
			}
			if outputBindingHasType(one.ServerFeatures(result.ResourceID), "load control") != tc.want {
				t.Fatalf("load control=%v features %v", tc.want, one.ServerFeatures(result.ResourceID))
			}
		})
	}
	charger := newOutputBinding(t)
	admitted := LensResult{
		Family: "charger", ResourceID: "bay", LensAdmitted: true, RuntimeEnabled: true,
		Operations: []LensOperation{{Name: "evse.operation.set_allocated_current", Unit: "A", OwnCapability: true}},
	}
	if len(charger.Apply(admitted)) != 0 || !outputBindingHasType(charger.ServerFeatures("bay"), "load control") {
		t.Fatalf("allocated current %v", charger.ServerFeatures("bay"))
	}
	watts := admitted
	watts.ResourceID = "otherbay"
	watts.Operations = []LensOperation{{Name: "active power", Unit: "W", OwnCapability: true}}
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
	overlap.Operations = []LensOperation{{Name: "pv.operation.set_active_power_limit", Unit: "W", OwnCapability: true}}
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

	ev := LensResult{Family: "ev", ResourceID: "car", LensAdmitted: true, RuntimeEnabled: true, DeviceActor: "EV", UseCase: "Overload protection"}
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

	charger := LensResult{
		Family: "charger", ResourceID: "bay", LensAdmitted: true, RuntimeEnabled: true,
		UseCase:    "Limitation of Power Consumption",
		Operations: []LensOperation{{Name: "evse.operation.set_allocated_current", Unit: "A", OwnCapability: true}},
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
	binding, err := openOutputBindingOnFakeNode(t, directory, newOutputBindingFakeNode())
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

// openOutputBindingOnFakeNode builds a binding on an injected node. The root
// package cannot prepare the runtime service; the facade package tests the
// binding on the real node. The binding resolves the node on every call, so the
// injected node stays the running node until the test ends.
func openOutputBindingOnFakeNode(t *testing.T, directory string, node *outputBindingFakeNode) (*OutputBinding, error) {
	t.Helper()
	useOutputBindingNode(t, func() (shipNode, error) { return node, nil })
	return NewOutputBinding(directory)
}

// useOutputBindingNode makes open the running node until the test ends.
func useOutputBindingNode(t *testing.T, open func() (shipNode, error)) {
	t.Helper()
	original := openShipNode
	openShipNode = open
	t.Cleanup(func() { openShipNode = original })
}

func outputBindingNode(t *testing.T, binding *OutputBinding) *outputBindingFakeNode {
	t.Helper()
	node, ok := binding.node.(*outputBindingFakeNode)
	if !ok || node == nil {
		t.Fatal("binding is not attached to the node")
	}
	return node
}

func outputBindingInverter(resourceID string) LensResult {
	return LensResult{Family: "inverter", ResourceID: resourceID, LensAdmitted: true, RuntimeEnabled: true}
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

func TestOutputBindingObservesNodeCounts(t *testing.T) {
	// The counts and the device type come from the node, not from the binding.
	node := newOutputBindingFakeNode()
	node.advertisements = 4
	node.services = 2
	node.deviceType = "ProbeNode"
	binding, err := openOutputBindingOnFakeNode(t, t.TempDir(), node)
	if err != nil {
		t.Fatal(err)
	}
	if binding.AdvertisementCount() != 4 || binding.ServiceCount() != 2 || binding.DeviceType() != "ProbeNode" {
		t.Fatalf("ads=%d services=%d type=%q", binding.AdvertisementCount(), binding.ServiceCount(), binding.DeviceType())
	}
	inverter := outputBindingInverter("north")
	inverter.Elements = []string{"temperature"}
	if len(binding.Apply(inverter)) != 0 {
		t.Fatal("apply")
	}
	if len(node.added) != 1 || node.added[0].kind != "Inverter" {
		t.Fatalf("node additions %v", node.added)
	}
	if !outputBindingHasType(binding.EntityTypes(), "Inverter") {
		t.Fatalf("types %v", binding.EntityTypes())
	}
	if !outputBindingHasType(node.features[binding.EntityAddress("north")], "measurement") {
		t.Fatalf("features were not added on the node %v", node.features)
	}
}

func TestOutputBindingRequiresExistingNode(t *testing.T) {
	// Without a running runtime service the binding is refused and nothing is composed.
	if _, err := NewOutputBinding(t.TempDir()); !errors.Is(err, errExistingNode) {
		t.Fatalf("binding without a runtime node err=%v", err)
	}
	useOutputBindingNode(t, func() (shipNode, error) { return nil, errors.New("absent") })
	if _, err := NewOutputBinding(t.TempDir()); !errors.Is(err, errExistingNode) {
		t.Fatalf("binding on an absent node err=%v", err)
	}
}

func TestOutputBindingFollowsReplacedNode(t *testing.T) {
	// A runtime restart replaces the node. A binding kept across it adds nothing
	// while no node runs, and then adds the entity on the new node at its stored address.
	first := newOutputBindingFakeNode()
	var current shipNode = first
	useOutputBindingNode(t, func() (shipNode, error) {
		if current == nil {
			return nil, errors.New("released")
		}
		return current, nil
	})
	binding, err := NewOutputBinding(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if loss := binding.Apply(outputBindingInverter("north")); len(loss) != 0 {
		t.Fatalf("loss %v", loss)
	}
	address := binding.EntityAddress("north")
	if address == "" || len(first.added) != 1 {
		t.Fatalf("first node additions %v", first.added)
	}
	current = nil
	if loss := binding.Apply(outputBindingInverter("north")); len(loss) != 1 || loss[0] != "existing_node" {
		t.Fatalf("loss without a node %v", loss)
	}
	if binding.AdvertisementCount() != 0 || binding.EntityAddress("north") != "" {
		t.Fatal("binding reports the released node")
	}
	second := newOutputBindingFakeNode()
	current = second
	if types := binding.EntityTypes(); len(types) != 1 || types[0] != "CEM" {
		t.Fatalf("new node types before a lens result %v", types)
	}
	if loss := binding.Apply(outputBindingInverter("north")); len(loss) != 0 {
		t.Fatalf("loss on the new node %v", loss)
	}
	if len(second.added) != 1 || second.added[0].address != address || binding.EntityAddress("north") != address {
		t.Fatalf("new node additions %v, want address %q", second.added, address)
	}
	if len(first.added) != 1 {
		t.Fatalf("released node gained entities %v", first.added)
	}
}

func TestOutputBindingNilReceiver(t *testing.T) {
	var binding *OutputBinding
	if loss := binding.Apply(outputBindingInverter("north")); len(loss) != 1 || loss[0] != "existing_node" {
		t.Fatalf("loss %v", loss)
	}
	if binding.EntityAddress("north") != "" || binding.ParentAddress("north") != "" {
		t.Fatal("nil binding reported an address")
	}
	if addresses := binding.EntityAddresses(); len(addresses) != 1 || addresses[0] != cemAddress {
		t.Fatalf("addresses %v", addresses)
	}
}

func TestOutputBindingConcurrentReaders(t *testing.T) {
	binding := newOutputBinding(t)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = binding.EntityTypes()
			_ = binding.EntityAddress("na")
			_ = binding.ServerFeatures("na")
			_ = binding.AdvertisementCount()
			_ = binding.ServiceCount()
			_ = binding.DeviceType()
			_ = binding.EntityAddresses()
			_ = binding.ParentAddress("na")
			_ = binding.MonitoringLoss("na")
		}
	}()
	for ordinal := 0; ordinal < 8; ordinal++ {
		result := outputBindingInverter("n" + letterToken(ordinal))
		if loss := binding.Apply(result); len(loss) != 0 {
			close(stop)
			wg.Wait()
			t.Fatalf("loss %v", loss)
		}
	}
	close(stop)
	wg.Wait()
}

type outputBindingFakeEntity struct {
	kind    string
	address string
	parent  string
}

// outputBindingFakeNode records what the binding adds on the node. It is not
// synchronised, so the race detector reports any call outside the binding gate.
type outputBindingFakeNode struct {
	advertisements int
	services       int
	deviceType     string
	added          []outputBindingFakeEntity
	features       map[string][]string
}

func newOutputBindingFakeNode() *outputBindingFakeNode {
	return &outputBindingFakeNode{
		advertisements: 1,
		services:       1,
		deviceType:     "EnergyManagementSystem",
		features:       map[string][]string{},
	}
}

func (n *outputBindingFakeNode) AdvertisementCount() int { return n.advertisements }

func (n *outputBindingFakeNode) ServiceCount() int { return n.services }

func (n *outputBindingFakeNode) DeviceType() string { return n.deviceType }

func (n *outputBindingFakeNode) EntityTypes() []string {
	types := []string{"CEM"}
	for _, entity := range n.added {
		types = append(types, entity.kind)
	}
	return types
}

func (n *outputBindingFakeNode) AddEntity(kind, address, parent string) error {
	for _, entity := range n.added {
		if entity.address == address {
			return errors.New("duplicate address")
		}
	}
	if parent != "" && n.find(parent) == nil {
		return errors.New("missing parent")
	}
	n.added = append(n.added, outputBindingFakeEntity{kind: kind, address: address, parent: parent})
	return nil
}

func (n *outputBindingFakeNode) ServerFeatures(address string) []string {
	return append([]string(nil), n.features[address]...)
}

func (n *outputBindingFakeNode) AddServerFeature(address, name string) error {
	if n.find(address) == nil {
		return errors.New("missing entity")
	}
	if !outputBindingHasType(n.features[address], name) {
		n.features[address] = append(n.features[address], name)
		sort.Strings(n.features[address])
	}
	return nil
}

// AddServerFeatures adds every named feature or, when the entity is missing, none.
func (n *outputBindingFakeNode) AddServerFeatures(address string, names []string) (string, error) {
	if n.find(address) == nil {
		return "", errors.New("missing entity")
	}
	for _, name := range names {
		if err := n.AddServerFeature(address, name); err != nil {
			return name, err
		}
	}
	return "", nil
}

func (n *outputBindingFakeNode) find(address string) *outputBindingFakeEntity {
	for index := range n.added {
		if n.added[index].address == address {
			return &n.added[index]
		}
	}
	return nil
}

func (n *outputBindingFakeNode) parentOf(address string) string {
	if entity := n.find(address); entity != nil {
		return entity.parent
	}
	return ""
}
