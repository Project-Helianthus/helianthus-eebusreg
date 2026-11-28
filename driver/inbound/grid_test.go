package inbound

import (
	"strings"
	"testing"

	semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"
)

func TestGridPublication(t *testing.T) {
	result, session := publish(t, electricalScope("instance.grid"), gridRemote())
	assertNoAccuracy(t, result)
	pcc := requireFact(t, result, "resource.grid_connection", "infrastructure.power.pcc", "-2500")
	requireQuantity(t, pcc, "-25", 2, "unit.watt")
	if pcc.NativeUnit != "W" {
		t.Fatalf("pcc native unit = %s", pcc.NativeUnit)
	}
	frequency := requireFact(t, result, "resource.grid_connection", "infrastructure.ac.frequency", "50")
	requireQuantity(t, frequency, "5", 1, "unit.hertz")
	limit := requireFact(t, result, "resource.grid_connection", "infrastructure.limit.pv_curtailment_factor", "50")
	requireQuantity(t, limit, "5", -1, "unit.ratio")
	exported := requireFact(t, result, "resource.meter", "infrastructure.energy.export", "3")
	requireQuantity(t, exported, "108", 2, "unit.joule")
	imported := requireFact(t, result, "resource.meter", "infrastructure.energy.import", "4")
	requireQuantity(t, imported, "144", 2, "unit.joule")
	if !strings.HasSuffix(exported.SubjectSlot, "/meter") || !strings.HasSuffix(imported.SubjectSlot, "/meter") {
		t.Fatalf("grid energy is not on the derived meter")
	}
	consumed := requireFact(t, result, "resource.meter", "infrastructure.energy.import", "2")
	requireQuantity(t, consumed, "72", 2, "unit.joule")
	if strings.Contains(consumed.SubjectSlot, "/phase/") || strings.HasSuffix(consumed.SubjectSlot, "/meter") {
		t.Fatalf("heat pump energy slot = %s", consumed.SubjectSlot)
	}
	assertPhaseQuantity(t, result, "1", "a", "infrastructure.ac.current", "10", "1", 1, "unit.ampere")
	assertPhaseQuantity(t, result, "1", "b", "infrastructure.ac.current", "11", "11", 0, "unit.ampere")
	assertPhaseQuantity(t, result, "1", "c", "infrastructure.ac.current", "12", "12", 0, "unit.ampere")
	assertPhaseQuantity(t, result, "1", "a", "infrastructure.ac.voltage", "230", "23", 1, "unit.volt")
	assertPhaseQuantity(t, result, "2", "a", "infrastructure.ac.active_power", "1000", "1", 3, "unit.watt")
	assertPhaseQuantity(t, result, "2", "b", "infrastructure.ac.current", "21", "21", 0, "unit.ampere")
	assertPhaseQuantity(t, result, "2", "c", "infrastructure.ac.voltage", "242", "242", 0, "unit.volt")
	if power, ok := memberValue(result, "power_total"); !ok || power != "3500" {
		t.Fatalf("power total member = %q %v", power, ok)
	}
	if frequencyMember, ok := memberValue(result, "frequency"); !ok || frequencyMember != "50" {
		t.Fatalf("frequency member = %q %v", frequencyMember, ok)
	}
	for _, fact := range result.Facts {
		if fact.SubjectKind == "resource.meter" && fact.FactID == "infrastructure.ac.frequency" {
			t.Fatalf("heat pump frequency became a fact")
		}
		if fact.SubjectKind == "resource.thermal_system" {
			t.Fatalf("electrical remote published a thermal fact")
		}
	}
	if len(nodeSlots(result, "resource.thermal_system")) != 0 {
		t.Fatal("electrical remote has a thermal system")
	}
	gaps := gapSet(result)
	if !gaps["device.grid:configuration_only_relation:infrastructure.relation.grid_connection_meter"] || !gaps["device.grid:configuration_only_relation:infrastructure.relation.circuit_phase"] {
		t.Fatalf("gaps = %+v", result.Gaps)
	}
	if linkCount(result, "infrastructure.relation.grid_connection_meter") != 0 || linkCount(result, "infrastructure.relation.circuit_phase") != 0 {
		t.Fatalf("configuration relation was inferred: %+v", result.Links)
	}
	assertGridAnchors(t, session.batches[0])
}

func assertPhaseQuantity(t *testing.T, result Result, entity, phase, id, native, coefficient string, exponent int32, unit string) {
	t.Helper()
	fact := requireFact(t, result, "resource.phase", id, native)
	requireQuantity(t, fact, coefficient, exponent, unit)
	if !strings.Contains(fact.SubjectSlot, "/entity/"+entity+"/phase/"+phase) && !strings.Contains(fact.SubjectSlot, "/entity/"+entity+"/connector/phase/"+phase) {
		t.Fatalf("slot %s does not name entity %s phase %s", fact.SubjectSlot, entity, phase)
	}
}

func assertGridAnchors(t *testing.T, batch semreg.PublicationBatch) {
	t.Helper()
	grids := resourcesOf(batch, "resource.grid_connection")
	meters := resourcesOf(batch, "resource.meter")
	phases := resourcesOf(batch, "resource.phase")
	if len(grids) != 1 || len(meters) != 2 || len(phases) != 6 {
		t.Fatalf("resources grid %d meter %d phase %d", len(grids), len(meters), len(phases))
	}
	assertParent(t, grids[0], semreg.AnchorFunction, false)
	for _, meter := range meters {
		assertParent(t, meter, semreg.AnchorEquipment, false)
	}
	for _, phase := range phases {
		assertParent(t, phase, semreg.AnchorComponent, true)
	}
}

func gridRemote() Remote {
	return Remote{
		Device: "device.grid", SKI: "synthetic-ship-ski", Manufacturer: "synthetic-maker", Serial: "synthetic-serial",
		Ready: true, Generation: 4,
		Entities: []Entity{
			{Type: "GridConnectionPointOfPremises", Address: []uint64{1}, Readings: []Reading{
				measure("power", "-2500", "W", ""), measure("frequency", "50", "Hz", ""), measure("powerLimitationFactor", "50", "%", ""),
				measure("energyFeedIn", "3", "Wh", ""), measure("energyConsumed", "4", "Wh", ""),
				measure("currentPerPhase", "10", "A", "a"), measure("currentPerPhase", "11", "A", "b"), measure("currentPerPhase", "12", "A", "c"),
				measure("voltagePerPhase", "230", "V", "a"), measure("voltagePerPhase", "231", "V", "b"), measure("voltagePerPhase", "232", "V", "c"),
			}},
			{Type: "HeatPumpAppliance", Address: []uint64{2}, Readings: []Reading{
				measure("power", "3500", "W", ""), measure("frequency", "50", "Hz", ""), measure("energyConsumed", "2", "Wh", ""),
				measure("powerPerPhase", "1000", "W", "a"), measure("powerPerPhase", "1100", "W", "b"), measure("powerPerPhase", "1200", "W", "c"),
				measure("currentPerPhase", "20", "A", "a"), measure("currentPerPhase", "21", "A", "b"), measure("currentPerPhase", "22", "A", "c"),
				measure("voltagePerPhase", "240", "V", "a"), measure("voltagePerPhase", "241", "V", "b"), measure("voltagePerPhase", "242", "V", "c"),
			}},
		},
	}
}

func TestGridRejectsSignedQuantities(t *testing.T) {
	remote := Remote{
		Device: "device.grid", SKI: "synthetic-ship-ski", Ready: true, Generation: 4,
		Entities: []Entity{{Type: "GridConnectionPointOfPremises", Address: []uint64{1}, Readings: []Reading{
			measure("energyFeedIn", "-1", "Wh", ""), measure("currentPerPhase", "-5", "A", "b"),
		}}},
	}
	result, _ := publish(t, electricalScope("instance.grid"), remote)
	denyFact(t, result, "infrastructure.energy.export", "-1")
	denyFact(t, result, "infrastructure.ac.current", "-5")
	gaps := gapSet(result)
	if !gaps["device.grid:negative_energy:energyFeedIn"] || !gaps["device.grid:signed_current_not_admitted:b"] {
		t.Fatalf("gaps = %+v", result.Gaps)
	}
}

func TestInverterPhases(t *testing.T) {
	remote := Remote{
		Device: "device.inverter", SKI: "synthetic-ship-ski", Ready: true, Generation: 4,
		Entities: []Entity{{Type: "Inverter", Address: []uint64{3}, Readings: []Reading{
			measure("power", "900", "W", ""), measure("energyProduced", "6", "Wh", ""), measure("frequency", "50", "Hz", ""),
			measure("powerPerPhase", "1000", "W", "a"), measure("currentPerPhase", "10", "A", "a"), measure("voltagePerPhase", "230", "V", "a"),
		}}},
	}
	result, session := publish(t, electricalScope("instance.inverter"), remote)
	assertNoAccuracy(t, result)
	power := requireFact(t, result, "resource.phase", "pv.ac.active_power", "1000")
	requireQuantity(t, power, "1", 3, "unit.watt")
	current := requireFact(t, result, "resource.phase", "pv.ac.current", "10")
	requireQuantity(t, current, "1", 1, "unit.ampere")
	frequency := requireFact(t, result, "resource.inverter", "pv.ac.frequency", "50")
	requireQuantity(t, frequency, "5", 1, "unit.hertz")
	if produced, ok := memberValue(result, "energy_produced"); !ok || produced != "6" {
		t.Fatalf("energy produced member = %q %v", produced, ok)
	}
	denyFact(t, result, "pv.energy.generated", "6")
	denyFact(t, result, "pv.vapd.ac_power_total", "900")
	denyFact(t, result, "infrastructure.ac.active_power", "1000")
	if linkCount(result, "pv.relation.inverter_phase") != 1 {
		t.Fatalf("inverter phase links = %+v", result.Links)
	}
	for _, relation := range session.batches[0].RelationUpserts {
		if relation.Definition.ID == "pv.relation.inverter_phase" && relation.BasisClass != semreg.RelationGateRule {
			t.Fatalf("basis = %s", relation.BasisClass)
		}
	}
}
