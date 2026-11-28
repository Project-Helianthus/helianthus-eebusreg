package base

import (
	"os"
	"path/filepath"
	"testing"

	eebusruntime "github.com/Project-Helianthus/helianthus-eebusreg"
	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusfacade"
	semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"
	semdriver "github.com/Project-Helianthus/helianthus-semreg/semreg/v1/driver"
	"github.com/Project-Helianthus/helianthus-semreg/semreg/v1/graph"
)

// TestOutputEntityAddressTwoLifecycles keeps one address per resource id across
// two lifecycles. Resource id, kind and parent each have a mutation below.
func TestOutputEntityAddressTwoLifecycles(t *testing.T) {
	assertDisabledFeedWritesNoAddress(t)

	directory := t.TempDir()
	first := runIdentity(t, directory, 4761, feedTokenA, false)
	east := waitAddress(t, first.binding, "inv-east")
	west := waitAddress(t, first.binding, "inv-west")
	stringEast := waitAddress(t, first.binding, "string-east")
	stringWest := waitAddress(t, first.binding, "string-west")
	// Two inverters of one kind must not share an address: a store keyed only by kind collides.
	if east == west || east == stringEast || west == stringWest || stringEast == stringWest || east == "" {
		t.Fatalf("addresses east=%s west=%s string-east=%s string-west=%s", east, west, stringEast, stringWest)
	}
	// Parent is the cited inverter, not an empty parent and not the other inverter.
	if first.binding.ParentAddress("string-east") != east || first.binding.ParentAddress("string-west") != west {
		t.Fatalf("parents east=%s west=%s", first.binding.ParentAddress("string-east"), first.binding.ParentAddress("string-west"))
	}
	if !first.node.NestedUnder(stringEast, east) || !first.node.NestedUnder(stringWest, west) {
		t.Fatal("string entity is not nested under its inverter")
	}
	first.run.stop()

	second := runIdentity(t, directory, 4762, feedTokenB, true)
	waitUntil(t, func() bool {
		return second.binding.EntityAddress("inv-east") == east &&
			second.binding.EntityAddress("inv-west") == west &&
			second.binding.EntityAddress("string-east") == stringEast &&
			second.binding.EntityAddress("string-west") == stringWest
	})
	// Reversing apply order must not reallocate. A store that ignores the resource id would.
	if second.binding.ParentAddress("string-east") != east || second.binding.ParentAddress("string-west") != west {
		t.Fatal("second lifecycle lost the parent address")
	}

	// Same resource id, different kind, same binding: merged_resources and the stored address stays.
	// A binding that ignores kind would add Battery or move the address.
	changed := identityCut("cut-kind", true)
	changed.page = identityPage(changed.ref, true, "resource.battery")
	second.script.setLens(changed.ref, lensHierarchy, changed.page)
	second.script.setLens(changed.ref, lensComposition, changed.page)
	second.script.push(semdriver.SemanticCut{Ref: changed.ref})
	waitUntil(t, func() bool {
		line, ok := lineByID(second.run.output.Reason(), "inv-east")
		return ok && hasText(line.Losses, "merged_resources")
	})
	if second.binding.EntityAddress("inv-east") != east {
		t.Fatalf("kind change moved the address to %s", second.binding.EntityAddress("inv-east"))
	}
	if !hasText(second.node.EntityTypes(), "Inverter") || hasText(second.node.EntityTypes(), "Battery") {
		t.Fatalf("node kinds %v", second.node.EntityTypes())
	}
}

func assertDisabledFeedWritesNoAddress(t *testing.T) {
	t.Helper()
	directory := t.TempDir()
	node := openFeedNode(t, 4760, feedTokenA)
	binding := newFeedBinding(t, directory, node)
	ref := cutRef("cut-disabled", graph.CutComplete)
	page := graph.LensResult{Cut: ref, Resources: []graph.LensResource{lensResource("inv-east", "resource.inverter", "asset-east")}}
	script := newFeedScript(ref)
	script.setLens(ref, lensHierarchy, page)
	script.setLens(ref, lensComposition, page)
	run := startFeed(t, binding, false, script)
	waitUntil(t, func() bool { return script.readsCount() >= 2 })
	// RuntimeEnabled false admits nothing. A feed that ignores enablement would store an address.
	if binding.EntityAddress("inv-east") != "" || run.output.Reason().Published != 0 {
		t.Fatalf("disabled feed published %q published=%d", binding.EntityAddress("inv-east"), run.output.Reason().Published)
	}
	if _, err := os.Lstat(filepath.Join(directory, "entity-addresses")); !os.IsNotExist(err) {
		t.Fatalf("disabled feed wrote the address file: %v", err)
	}
	run.stop()
}

type identityRun struct {
	run     *runningFeed
	script  *feedScript
	binding *eebusruntime.OutputBinding
	node    *eebusfacade.OutputNode
}

type identityFixture struct {
	ref  graph.CutRef
	page graph.LensResult
}

func runIdentity(t *testing.T, directory string, port int, token string, reversed bool) identityRun {
	t.Helper()
	node := openFeedNode(t, port, token)
	binding := newFeedBinding(t, directory, node)
	fixture := identityCut("cut-identity", reversed)
	script := newFeedScript(fixture.ref)
	script.setLens(fixture.ref, lensHierarchy, fixture.page)
	script.setLens(fixture.ref, lensComposition, fixture.page)
	return identityRun{run: startFeed(t, binding, true, script), script: script, binding: binding, node: node}
}

func identityCut(id string, reversed bool) identityFixture {
	ref := cutRef(id, graph.CutComplete)
	return identityFixture{ref: ref, page: identityPage(ref, reversed, "resource.inverter")}
}

func identityPage(ref graph.CutRef, reversed bool, eastKind string) graph.LensResult {
	resources := []graph.LensResource{
		lensResource("inv-east", eastKind, "asset-east"),
		lensResource("inv-west", "resource.inverter", "asset-west"),
		lensResource("string-east", "resource.pv_string", "asset-string-east"),
		lensResource("string-west", "resource.pv_string", "asset-string-west"),
	}
	if reversed {
		for left, right := 0, len(resources)-1; left < right; left, right = left+1, right-1 {
			resources[left], resources[right] = resources[right], resources[left]
		}
	}
	return graph.LensResult{
		Cut:       ref,
		Resources: resources,
		Relations: []graph.LensRelation{
			containsRelation("inv-east", "string-east", "asset-rel-east"),
			containsRelation("inv-west", "string-west", "asset-rel-west"),
		},
	}
}

func waitAddress(t *testing.T, binding *eebusruntime.OutputBinding, id string) string {
	t.Helper()
	var address string
	waitUntil(t, func() bool {
		address = binding.EntityAddress(id)
		return address != ""
	})
	return address
}

func TestOutputFeedMixedCut(t *testing.T) {
	script, ref := mixedScript()
	prepared := mustPrepare(t, script, ref, true)
	assertMixedPrepared(t, prepared)
	disabled := mustPrepare(t, script, ref, false)
	for _, result := range disabled.results {
		// enabled is an argument, not a constant. Hardcoding true fails this cut.
		if result.RuntimeEnabled {
			t.Fatalf("%s stayed enabled", result.ResourceID)
		}
	}
	assertMixedCalls(t, script)

	node := openFeedNode(t, 4771, feedTokenA)
	binding := newFeedBinding(t, t.TempDir(), node)
	run := startFeed(t, binding, true, script)
	for _, id := range mixedPublished {
		waitAddress(t, binding, id)
	}
	if binding.EntityAddress("connector-1") != "" || binding.EntityAddress("phase-a") != "" || binding.EntityAddress("heating-unknown") != "" {
		t.Fatal("connector, phase or unknown heating was published")
	}
	assertMixedParents(t, binding, node)
	line, ok := lineByID(run.output.Reason(), "evse-1")
	if !ok || line.SumWithheld || line.SumCoefficient != "7" || line.SumExponent != 1 || line.SumUnit != "unit.watt" || line.SumReason != "" {
		t.Fatalf("member sum line %+v", line)
	}
	alone, ok := lineByID(run.output.Reason(), "room-alone")
	ambiguous, ambiguousOK := lineByID(run.output.Reason(), "room-ambiguous")
	if !ok || alone.Grouping != "missing" || !ambiguousOK || ambiguous.Grouping != "ambiguous" {
		t.Fatalf("room grouping alone=%+v ambiguous=%+v", alone, ambiguous)
	}
	types := node.EntityTypes()
	for _, kind := range []string{"Inverter", "PV String", "EVSE", "EV", "SubMeterElectricity", "Battery", "HeatingCircuit", "HeatingZone", "HVACRoom", "GridConnectionPointOfPremises"} {
		if !hasText(types, kind) {
			t.Fatalf("missing %s in %v", kind, types)
		}
	}
	if countText(types, "PV String") < 2 || countText(types, "EVSE") < 3 || countText(types, "HVACRoom") < 4 {
		t.Fatalf("entity counts %v", types)
	}
}

var mixedPublished = []string{
	"inv-1", "string-1", "dc-1", "evse-1", "evse-2", "station-1", "vehicle-1", "meter-1",
	"battery-1", "circuit-1", "circuit-2", "zone-1", "room-zone", "room-circuit", "room-alone", "room-ambiguous", "grid-1",
}

func mixedScript() (*feedScript, graph.CutRef) {
	ref := cutRef("cut-mixed", graph.CutComplete)
	script := newFeedScript(ref)
	script.setLens(ref, lensHierarchy, mixedTopology(ref))
	script.setLens(ref, lensComposition, mixedTopology(ref))
	script.setLens(ref, lensEnergy, mixedEnergy(ref))
	script.setSum(ref, "evse-1", wattSum("7", 1, "unit.watt"), nil)
	script.setSum(ref, "evse-2", graph.MemberSum{Withheld: true, Reason: graph.SumNoCoverage}, nil)
	script.setSum(ref, "station-1", graph.MemberSum{Withheld: true, Reason: graph.SumNoCoverage}, nil)
	return script, ref
}

func mixedTopology(ref graph.CutRef) graph.LensResult {
	return graph.LensResult{
		Cut: ref,
		Resources: []graph.LensResource{
			lensResource("room-zone", "resource.room", "asset-room-zone"),
			lensResource("zone-1", "resource.zone", "asset-zone"),
			lensResource("string-1", "resource.pv_string", "asset-string"),
			lensResource("dc-1", "resource.dc_input", "asset-dc"),
			lensResource("vehicle-1", "resource.vehicle", "asset-vehicle"),
			lensResource("battery-1", "resource.battery", "asset-battery"),
			lensResource("room-circuit", "resource.room", "asset-room-circuit"),
			lensResource("room-alone", "resource.room", "asset-room-alone"),
			lensResource("room-ambiguous", "resource.room", "asset-room-ambiguous"),
			lensResource("inv-1", "resource.inverter", "asset-inv"),
			lensResource("evse-1", "resource.evse", "asset-evse"),
			lensResource("evse-2", "resource.evse", "asset-evse-2"),
			lensResource("station-1", "resource.charging_station", "asset-station"),
			lensResource("connector-1", "resource.connector", "asset-connector"),
			lensResource("meter-1", "resource.meter", "asset-meter"),
			lensResource("circuit-1", "resource.thermal_circuit", "asset-circuit"),
			lensResource("circuit-2", "resource.thermal_circuit", "asset-circuit-2"),
			lensResource("grid-1", "resource.grid_connection", "asset-grid"),
			lensResource("heating-unknown", "resource.heating", "asset-heating"),
		},
		Relations: []graph.LensRelation{
			containsRelation("inv-1", "string-1", "asset-rel-string"),
			containsRelation("inv-1", "dc-1", "asset-rel-dc"),
			containsRelation("inv-1", "battery-1", "asset-rel-battery"),
			containsRelation("evse-2", "vehicle-1", "asset-rel-decoy"),
			containsRelation("evse-1", "meter-1", "asset-rel-meter"),
			namedRelation(relationConnector, "evse-1", "connector-1", "asset-rel-connector"),
			namedRelation(relationVehicle, "connector-1", "vehicle-1", "asset-rel-vehicle"),
			namedRelation(relationCircuitZone, "circuit-1", "zone-1", "asset-rel-zone"),
			namedRelation(relationZoneRoom, "zone-1", "room-zone", "asset-rel-room-zone"),
			namedRelation(relationCircuitRoom, "circuit-1", "room-circuit", "asset-rel-room-circuit"),
			namedRelation(relationCircuitRoom, "circuit-1", "room-ambiguous", "asset-rel-ambiguous-1"),
			namedRelation(relationCircuitRoom, "circuit-2", "room-ambiguous", "asset-rel-ambiguous-2"),
		},
		Services: []semreg.ServiceInstance{
			serviceOn("service-evse", "evse-1"),
			serviceOn("service-inv", "inv-1"),
		},
		Capabilities: []semreg.CapabilityInstance{
			capabilityOn("cap-current", "service-evse", capabilityAllocatedCurrent, true),
			capabilityOn("cap-skipped", "service-evse", "evse.capability.unqualified", false),
			capabilityOn("cap-limit", "service-inv", capabilityPowerLimit, true),
		},
	}
}

func mixedEnergy(ref graph.CutRef) graph.LensResult {
	fact := powerFact("phase-a", "a", "10", "9.9.9")
	return graph.LensResult{
		Cut: ref,
		Resources: []graph.LensResource{
			lensResource("phase-a", "resource.phase", "asset-phase-a"),
			lensResource("phase-b", "resource.phase", "asset-phase-b"),
			lensResource("connector-1", "resource.connector", "asset-connector"),
		},
		Relations: []graph.LensRelation{
			namedRelation(relationConnector, "evse-1", "connector-1", "asset-energy-connector"),
			namedRelation("evse.relation.connector_phase", "connector-1", "phase-a", "asset-energy-phase-a"),
			namedRelation("evse.relation.connector_phase", "connector-1", "phase-b", "asset-energy-phase-b"),
		},
		Facts: []graph.LensFact{fact, fact, powerFact("phase-b", "b", "20", "9.9.9")},
	}
}

func powerFact(subject, phase, coefficient, version string) graph.LensFact {
	return graph.LensFact{
		AssetID: semreg.AssetID("asset-" + subject),
		Definition: semreg.DefinitionRef{
			Pack:    semreg.PackRef{ID: "helianthus.pack.evse", Version: semreg.SemanticVersion(version)},
			ID:      elementActivePower,
			Version: semreg.SemanticVersion(version),
		},
		Key: semreg.FactKey{
			FactID:     elementActivePower,
			Dimensions: []semreg.Dimension{{ID: dimensionPhase, Value: textValue(phase)}},
		},
		Subject:    semreg.ResourceID(subject),
		Candidates: []semreg.FactCandidate{{CandidateID: semreg.CandidateID("cand-" + subject + "-" + phase), Value: quantityPointer(coefficient, 0, "unit.watt"), Quality: promotedQuality()}},
	}
}

func quantityPointer(coefficient string, exponent int32, unit string) *semreg.Value {
	value := quantityValue(coefficient, exponent, unit)
	return &value
}

func assertMixedPrepared(t *testing.T, prepared preparedCut) {
	t.Helper()
	if len(prepared.results) != len(mixedPublished) {
		t.Fatalf("results %d, want %d", len(prepared.results), len(mixedPublished))
	}
	for _, id := range []string{"connector-1", "phase-a", "phase-b", "heating-unknown"} {
		if _, ok := resultByID(prepared.results, id); ok {
			t.Fatalf("%s was published", id)
		}
	}
	parents := map[string]string{
		"string-1": "inv-1", "dc-1": "inv-1", "battery-1": "inv-1", "vehicle-1": "evse-1",
		"zone-1": "circuit-1", "room-zone": "zone-1", "room-circuit": "circuit-1",
	}
	for child, parent := range parents {
		result, ok := resultByID(prepared.results, child)
		if !ok || result.ParentResourceID != parent || !result.ContainsCited || !result.RuntimeEnabled || !result.LensAdmitted {
			t.Fatalf("%s parent %+v", child, result)
		}
		if indexByID(prepared.results, parent) < 0 || indexByID(prepared.results, parent) > indexByID(prepared.results, child) {
			t.Fatalf("%s was applied before %s", child, parent)
		}
	}
	for _, id := range []string{"meter-1", "room-alone", "room-ambiguous", "grid-1", "evse-1"} {
		result, ok := resultByID(prepared.results, id)
		if !ok || result.ParentResourceID != "" {
			t.Fatalf("%s parent %q", id, result.ParentResourceID)
		}
	}
	vehicle, _ := resultByID(prepared.results, "vehicle-1")
	if vehicle.ParentResourceID == "evse-2" {
		t.Fatal("decoy contains selected the vehicle parent")
	}
	evse, _ := resultByID(prepared.results, "evse-1")
	if evse.MemberSum == nil || evse.MemberSum.Withheld || evse.MemberSum.Coefficient != "7" || evse.MemberSum.Exponent != 1 || evse.MemberSum.Unit != "unit.watt" {
		t.Fatalf("evse sum %+v", evse.MemberSum)
	}
	phases := 0
	for _, fact := range evse.Facts {
		if fact.Element == elementActivePower {
			phases++
		}
	}
	// The same energy fact is on the page twice. A missing dedupe publishes two.
	if phases != 2 {
		t.Fatalf("phase power facts %d, want one per phase", phases)
	}
	if len(evse.Operations) != 1 || evse.Operations[0].Name != operationAllocatedCurrent || evse.Operations[0].Unit != "A" || !evse.Operations[0].OwnCapability || evse.Operations[0].Converted {
		t.Fatalf("evse operations %+v", evse.Operations)
	}
	inverter, _ := resultByID(prepared.results, "inv-1")
	if len(inverter.Operations) != 1 || inverter.Operations[0].Name != operationPowerLimit || inverter.Operations[0].Unit != "W" {
		t.Fatalf("inverter operations %+v", inverter.Operations)
	}
	if prepared.grouping["room-alone"] != "missing" || prepared.grouping["room-ambiguous"] != "ambiguous" {
		t.Fatalf("grouping %+v", prepared.grouping)
	}
	station, _ := resultByID(prepared.results, "station-1")
	dc, _ := resultByID(prepared.results, "dc-1")
	if station.Family != "charger" || dc.Family != "pv string" {
		t.Fatalf("station %s dc %s", station.Family, dc.Family)
	}
}

func assertMixedParents(t *testing.T, binding *eebusruntime.OutputBinding, node *eebusfacade.OutputNode) {
	t.Helper()
	pairs := map[string]string{
		"string-1": "inv-1", "dc-1": "inv-1", "battery-1": "inv-1", "vehicle-1": "evse-1",
		"zone-1": "circuit-1", "room-zone": "zone-1", "room-circuit": "circuit-1",
	}
	for child, parent := range pairs {
		if binding.ParentAddress(child) != binding.EntityAddress(parent) || !node.NestedUnder(binding.EntityAddress(child), binding.EntityAddress(parent)) {
			t.Fatalf("%s parent address %s, entity %s under %s", child, binding.ParentAddress(child), binding.EntityAddress(child), binding.EntityAddress(parent))
		}
	}
	if binding.ParentAddress("meter-1") != "" || node.NestedUnder(binding.EntityAddress("meter-1"), binding.EntityAddress("evse-1")) {
		t.Fatal("meter was nested under the charging station")
	}
	if binding.ParentAddress("vehicle-1") == binding.EntityAddress("evse-2") {
		t.Fatal("vehicle nested under the decoy charging station")
	}
}

func assertMixedCalls(t *testing.T, script *feedScript) {
	t.Helper()
	for _, request := range script.Requests() {
		if request.Direction != graph.DirectionOutgoing || request.PageSize != graph.MaxPageSize {
			t.Fatalf("request %+v", request.Entry)
		}
		switch string(request.Lens) {
		case lensHierarchy:
			if request.Depth != depthHierarchy || request.Entry.Kind != graph.EntryLensRoots || string(request.Entry.Lens) != lensHierarchy {
				t.Fatalf("hierarchy %+v", request)
			}
		case lensComposition:
			if request.Depth != depthComposition || request.Entry.Kind != graph.EntryLensRoots || string(request.Entry.Lens) != lensComposition {
				t.Fatalf("composition %+v", request)
			}
		case lensEnergy:
			if request.Depth != depthEnergy || request.Entry.Kind != graph.EntryResource {
				t.Fatalf("energy %+v", request)
			}
		default:
			t.Fatalf("lens %s", request.Lens)
		}
	}
	sawVersion := false
	sawFallback := false
	for _, call := range script.Sums() {
		if string(call.definition.ID) != elementActivePower || call.request.Entry.Kind != graph.EntryResource || string(call.request.Entry.Resource) != string(call.scope) {
			t.Fatalf("sum %+v", call)
		}
		if string(call.scope) == "evse-1" {
			sawVersion = call.definition.Version == "9.9.9" && call.definition.Pack.ID == "helianthus.pack.evse" && call.definition.Pack.Version == "9.9.9"
		}
		if string(call.scope) == "evse-2" && call.definition.Version == "1.0.0" && call.definition.Pack.ID == "helianthus.pack.evse" && call.definition.Pack.Version == "1.0.0" {
			sawFallback = true
		}
	}
	// A fact's pack version is the definition. Hardcoding 1.0.0 fails evse-1.
	// An EVSE with no power fact uses the fallback pack. Skipping that fallback fails evse-2.
	if !sawVersion || !sawFallback {
		t.Fatalf("definition versions version=%v fallback=%v", sawVersion, sawFallback)
	}
}

func TestOutputFeedFactFields(t *testing.T) {
	assertOneFact(t, factBaseline(), factWant("evse.ac.voltage", "a", "9", "quantity", "4", 3, false, "", "", "unit.watt"))

	// Each row changes one field of the fact identity. The production copy of that field is what fails when dropped.
	element := factBaseline()
	element.Definition.ID = "evse.ac.current"
	assertOneFact(t, element, factWant("evse.ac.current", "a", "9", "quantity", "4", 3, false, "", "", "unit.watt"))
	phase := factBaseline()
	phase.Key.Dimensions[0].Value = textValue("c")
	assertOneFact(t, phase, factWant("evse.ac.voltage", "c", "9", "quantity", "4", 3, false, "", "", "unit.watt"))
	connector := factBaseline()
	connector.Key.Dimensions[1].Value = textValue("2")
	assertOneFact(t, connector, factWant("evse.ac.voltage", "a", "2", "quantity", "4", 3, false, "", "", "unit.watt"))
	// Dimension identity is the dimension id, not its position.
	reordered := factBaseline()
	reordered.Key.Dimensions = []semreg.Dimension{
		{ID: dimensionConnector, Value: textValue("9")},
		{ID: dimensionPhase, Value: textValue("a")},
	}
	assertOneFact(t, reordered, factWant("evse.ac.voltage", "a", "9", "quantity", "4", 3, false, "", "", "unit.watt"))
	coefficient := factBaseline()
	coefficient.Candidates[0].Value = quantityPointer("15", 3, "unit.watt")
	assertOneFact(t, coefficient, factWant("evse.ac.voltage", "a", "9", "quantity", "15", 3, false, "", "", "unit.watt"))
	exponent := factBaseline()
	exponent.Candidates[0].Value = quantityPointer("4", -2, "unit.watt")
	assertOneFact(t, exponent, factWant("evse.ac.voltage", "a", "9", "quantity", "4", -2, false, "", "", "unit.watt"))
	unit := factBaseline()
	unit.Candidates[0].Value = quantityPointer("4", 3, "unit.volt")
	assertOneFact(t, unit, factWant("evse.ac.voltage", "a", "9", "quantity", "4", 3, false, "", "", "unit.volt"))
	boolean := factBaseline()
	boolean.Candidates[0].Value = boolPointer(true)
	assertOneFact(t, boolean, factWant("evse.ac.voltage", "a", "9", "boolean", "", 0, true, "", "", ""))
	text := factBaseline()
	text.Candidates[0].Value = textPointer("ready")
	assertOneFact(t, text, factWant("evse.ac.voltage", "a", "9", "text", "", 0, false, "ready", "", ""))
	symbol := factBaseline()
	symbol.Candidates[0].Value = symbolPointer("idle")
	assertOneFact(t, symbol, factWant("evse.ac.voltage", "a", "9", "symbol", "", 0, false, "", "idle", ""))

	distinct := factBaseline()
	distinct.Candidates[0].Quality = distinctQuality("assert-1", "valid-4", "avail-5", "fresh-6")
	got := oneFact(t, distinct)
	// Qualification and promotion stay the admitted literals. Copying assertion into either axis fails.
	if got.Quality.Assertion != "assert-1" || got.Quality.Qualification != string(semreg.QualificationQualified) ||
		got.Quality.Promotion != string(semreg.PromotionPromoted) || got.Quality.Validity != "valid-4" ||
		got.Quality.Availability != "avail-5" || got.Quality.Freshness != "fresh-6" {
		t.Fatalf("quality %+v", got.Quality)
	}
	axes := []struct {
		name   string
		assign func(*semreg.Quality)
		read   func(eebusruntime.LensQuality) string
		want   string
	}{
		{"assertion", func(q *semreg.Quality) { q.Assertion = "assert-9" }, func(q eebusruntime.LensQuality) string { return q.Assertion }, "assert-9"},
		{"validity", func(q *semreg.Quality) { q.Validity = "valid-9" }, func(q eebusruntime.LensQuality) string { return q.Validity }, "valid-9"},
		{"availability", func(q *semreg.Quality) { q.Availability = "avail-9" }, func(q eebusruntime.LensQuality) string { return q.Availability }, "avail-9"},
		{"freshness", func(q *semreg.Quality) { q.Freshness = "fresh-9" }, func(q eebusruntime.LensQuality) string { return q.Freshness }, "fresh-9"},
	}
	for _, axis := range axes {
		mutated := factBaseline()
		quality := distinctQuality("assert-1", "valid-4", "avail-5", "fresh-6")
		axis.assign(&quality)
		mutated.Candidates[0].Quality = quality
		got := oneFact(t, mutated)
		if axis.read(got.Quality) != axis.want || got.Quality.Qualification != string(semreg.QualificationQualified) || got.Quality.Promotion != string(semreg.PromotionPromoted) {
			t.Fatalf("%s copy %+v", axis.name, got.Quality)
		}
	}

	rejected := factBaseline()
	rejected.Candidates[0].Quality.Promotion = semreg.PromotionUnpromoted
	if facts, missing := oneFactOutcome(t, rejected); len(facts) != 0 || !hasText(missing, "evse.ac.voltage") {
		t.Fatalf("unpromoted facts %d missing %v", len(facts), missing)
	}
	conflicted := factBaseline()
	conflicted.Conflicts = []semreg.Conflict{{}}
	if facts, missing := oneFactOutcome(t, conflicted); len(facts) != 0 || !hasText(missing, "evse.ac.voltage") {
		t.Fatalf("conflicted facts %d missing %v", len(facts), missing)
	}
}

type factExpectation struct {
	element, phase, connector, kind, coefficient, text, symbol, unit string
	exponent                                                         int32
	boolean                                                          bool
}

func factWant(element, phase, connector, kind, coefficient string, exponent int32, boolean bool, text, symbol, unit string) factExpectation {
	return factExpectation{element: element, phase: phase, connector: connector, kind: kind, coefficient: coefficient, exponent: exponent, boolean: boolean, text: text, symbol: symbol, unit: unit}
}

func factBaseline() graph.LensFact {
	return graph.LensFact{
		Definition: semreg.DefinitionRef{ID: "evse.ac.voltage", Version: "1.0.0"},
		Key: semreg.FactKey{
			FactID: "evse.ac.current",
			Dimensions: []semreg.Dimension{
				{ID: dimensionPhase, Value: textValue("a")},
				{ID: dimensionConnector, Value: textValue("9")},
			},
		},
		Subject: "evse-1",
		Candidates: []semreg.FactCandidate{{
			CandidateID: "cand-1",
			Value:       quantityPointer("4", 3, "unit.watt"),
			Quality:     promotedQuality(),
		}},
	}
}

func assertOneFact(t *testing.T, fact graph.LensFact, want factExpectation) {
	t.Helper()
	got := oneFact(t, fact)
	if got.Element != want.element || got.Phase != want.phase || got.Connector != want.connector || got.Value.Kind != want.kind ||
		got.Value.Coefficient != want.coefficient || got.Value.Exponent != want.exponent || got.Value.Boolean != want.boolean ||
		got.Value.Text != want.text || got.Value.Symbol != want.symbol || got.Unit != want.unit {
		t.Fatalf("fact %+v value %+v unit %s, want %+v", got, got.Value, got.Unit, want)
	}
}

func oneFact(t *testing.T, fact graph.LensFact) eebusruntime.LensFact {
	t.Helper()
	facts, missing := oneFactOutcome(t, fact)
	if len(facts) != 1 {
		t.Fatalf("facts %d missing %v", len(facts), missing)
	}
	return facts[0]
}

func oneFactOutcome(t *testing.T, fact graph.LensFact) ([]eebusruntime.LensFact, []string) {
	t.Helper()
	ref := cutRef("cut-fact", graph.CutComplete)
	page := graph.LensResult{
		Cut:       ref,
		Resources: []graph.LensResource{lensResource("evse-1", "resource.evse", "asset-evse")},
		Facts:     []graph.LensFact{fact},
	}
	script := newFeedScript(ref)
	script.setLens(ref, lensHierarchy, page)
	script.setLens(ref, lensComposition, page)
	script.setSum(ref, "evse-1", wattSum("1", 0, "unit.watt"), nil)
	prepared := mustPrepare(t, script, ref, true)
	result, ok := resultByID(prepared.results, "evse-1")
	if !ok {
		t.Fatal("evse result missing")
	}
	return result.Facts, result.MissingElements
}

func distinctQuality(assertion, validity, availability, freshness string) semreg.Quality {
	quality := promotedQuality()
	quality.Assertion = semreg.AssertionKind(assertion)
	quality.Validity = semreg.Validity(validity)
	quality.Availability = semreg.Availability(availability)
	quality.Freshness = semreg.Freshness(freshness)
	return quality
}

func boolPointer(value bool) *semreg.Value {
	got := boolValue(value)
	return &got
}

func textPointer(text string) *semreg.Value {
	got := textValue(text)
	return &got
}

func symbolPointer(token string) *semreg.Value {
	got := symbolValue(token)
	return &got
}
