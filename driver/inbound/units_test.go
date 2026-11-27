package inbound

import (
	"context"
	"math/big"
	"strings"
	"testing"

	semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"
)

func TestTemperatureUnits(t *testing.T) {
	celsius := publishOne(t, heatingScope("instance.heating"), sensorRemote("°C", "48.5"))
	mark := requireFact(t, celsius, "resource.thermal_system", "thermal.measurement.outdoor_air_temperature", "48.5")
	if !sameKelvin(mark, "48.5") {
		t.Fatalf("degree sign = %+v", mark)
	}
	kelvin := publishOne(t, heatingScope("instance.heating"), sensorRemote("K", "300"))
	passed := requireFact(t, kelvin, "resource.thermal_system", "thermal.measurement.outdoor_air_temperature", "300")
	if passed.Coefficient == "" || sameKelvin(passed, "300") {
		t.Fatalf("kelvin was shifted: %+v", passed)
	}
	if !sameNumber(passed, "300") {
		t.Fatalf("kelvin passthrough = %+v", passed)
	}
	for _, unit := range []string{"degF", "C"} {
		result := publishOne(t, heatingScope("instance.heating"), sensorRemote(unit, "48.5"))
		if _, ok := findFact(result, "resource.thermal_system", "thermal.measurement.outdoor_air_temperature", "48.5"); ok || !gapSet(result)["device.heat:temperature_unit:outdoor_air_temperature"] {
			t.Fatalf("unit %s published or lost its gap: %+v gaps %v", unit, result.Facts, result.Gaps)
		}
	}
}

func TestOverrunSymbolsAndRoomComfort(t *testing.T) {
	active := publishOne(t, heatingScope("instance.heating"), overrunRemote("true"))
	flag := requireFact(t, active, "resource.thermal_system", "thermal.overrun.one_time_dhw", "true")
	if flag.SymbolToken != "active" || !flag.SymbolKnown {
		t.Fatalf("true overrun = %+v", flag)
	}
	inactive := publishOne(t, heatingScope("instance.heating"), overrunRemote("false"))
	off := requireFact(t, inactive, "resource.thermal_system", "thermal.overrun.one_time_dhw", "false")
	if off.SymbolToken != "inactive" || !off.SymbolKnown {
		t.Fatalf("false overrun = %+v", off)
	}
	zoneComfort := publishOne(t, heatingScope("instance.heating"), oneZoneMode("HeatingZone", "zone_mode", "comfort"))
	known := requireFact(t, zoneComfort, "resource.zone", "thermal.mode.zone", "comfort")
	if !known.SymbolKnown || known.SymbolToken != "comfort" {
		t.Fatalf("zone comfort = %+v", known)
	}
	roomComfort := publishOne(t, heatingScope("instance.heating"), oneZoneMode("HVACRoom", "room_mode", "comfort"))
	unknown := requireFact(t, roomComfort, "resource.room", "thermal.room.mode_zone", "comfort")
	if unknown.SymbolKnown || unknown.SymbolNamespace != "native.hvac.mode" || unknown.SymbolToken != "comfort" {
		t.Fatalf("room comfort = %+v", unknown)
	}
}

func TestOmittedReadingKeepsTheLastValue(t *testing.T) {
	publisher, err := New(heatingScope("instance.heating"))
	if err != nil {
		t.Fatal(err)
	}
	session := newHost()
	remote := rankedRemote(rankedReading("producer", "48.5", "notify", 4, 100), heat("dhw_temperature_setpoint", "50"))
	if _, err := publisher.Apply(context.Background(), session, []Remote{remote}); err != nil {
		t.Fatal(err)
	}
	remote.Entities[0].Readings = []Reading{rankedReading("producer", "48.5", "notify", 4, 100)}
	result, err := publisher.Apply(context.Background(), session, []Remote{remote})
	if err != nil {
		t.Fatal(err)
	}
	setpoint := requireFact(t, result, "resource.domestic_hot_water", "thermal.setpoint.dhw_temperature_setpoint", "50")
	if setpoint.Availability != "unavailable" || !sameKelvin(setpoint, "50") {
		t.Fatalf("omitted setpoint = %+v", setpoint)
	}
	temperature := requireFact(t, result, "resource.domestic_hot_water", "thermal.measurement.dhw_temperature", "48.5")
	if temperature.Availability != "available" {
		t.Fatalf("present temperature = %+v", temperature)
	}
}

func TestIncompleteIdentityAndAbsentValue(t *testing.T) {
	remote := oneZone("device.heat", 1)
	remote.Entities = append(remote.Entities, Entity{Type: "HeatingZone", Name: "unnamed"})
	remote.Entities[0].Readings = []Reading{mode("zone_mode", "auto")}
	result, session := publish(t, heatingScope("instance.heating"), remote)
	if _, ok := findFact(result, "resource.zone", "thermal.measurement.air_temperature", ""); ok {
		t.Fatal("an absent temperature was published")
	}
	for _, fact := range result.Facts {
		if fact.Coefficient == "0" && fact.Native == "" {
			t.Fatalf("absent value became zero: %+v", fact)
		}
	}
	found := false
	for _, member := range result.Members {
		if member.Member.Key.Local == "missing_identity" && member.Value == "entity" {
			found = true
		}
	}
	registered := false
	for _, spec := range session.nodes {
		if strings.Contains(string(spec.Local), "missing/") {
			registered = true
		}
	}
	found = found && registered
	if !found || !gapSet(result)["device.heat:missing_identity:missing/device.heat/HeatingZone"] {
		t.Fatalf("incomplete identity members %+v gaps %v", result.Members, result.Gaps)
	}
	for _, node := range result.Nodes {
		if strings.Contains(node.Key.Anchor.Slot, "missing/") {
			t.Fatalf("incomplete entity became a resource: %+v", node)
		}
	}
	empty := Remote{SKI: "synthetic-ship-ski", Ready: true, Generation: 4}
	emptyResult, _ := publish(t, heatingScope("instance.heating"), empty)
	if len(emptyResult.Nodes) != 0 || !gapSet(emptyResult)["missing_identity:device"] {
		t.Fatalf("empty device = %+v gaps %v", emptyResult.Nodes, emptyResult.Gaps)
	}
}

func TestUnknownRankAndUnresolvedParent(t *testing.T) {
	publisher, err := New(heatingScope("instance.heating"))
	if err != nil {
		t.Fatal(err)
	}
	remote := rankedRemote(rankedReading("producer", "48.5", "notify", 4, 100))
	if _, err := publisher.Apply(context.Background(), newHost(), []Remote{remote}); err != nil {
		t.Fatal(err)
	}
	remote.Entities[0].Readings[0].Rank = "guessed"
	if _, err := publisher.Apply(context.Background(), newHost(), []Remote{remote}); err == nil || !strings.Contains(err.Error(), "rank") {
		t.Fatalf("unknown rank = %v", err)
	}
	hanging := oneZone("device.heat", 1)
	hanging.Entities = []Entity{{Type: "HVACRoom", Address: []uint64{6}, Parent: []uint64{99}, Readings: []Reading{mode("room_mode", "on")}}}
	result, _ := publish(t, heatingScope("instance.heating"), hanging)
	if !gapSet(result)["device.heat:parent_unresolved:6"] {
		t.Fatalf("gaps = %v", result.Gaps)
	}
	for _, link := range result.Links {
		if link.Key.Type.ID != "thermal.relation.system_member" {
			t.Fatalf("unresolved parent became a relation: %+v", link)
		}
	}
	plain := mode("room_mode", "on")
	plain.Writable = true
	result, session := publish(t, heatingScope("instance.heating"), Remote{
		Device: "device.heat", SKI: "synthetic-ship-ski", Manufacturer: "synthetic-maker", Serial: "synthetic-serial",
		Ready: true, Generation: 4,
		Entities: []Entity{{Type: "HVACRoom", Address: []uint64{6}, Readings: []Reading{plain}}},
	})
	if len(session.routes) != 0 || !gapSet(result)["device.heat:feature_missing:thermal.room.operation_set_zone_mode"] {
		t.Fatalf("routes %d gaps %v", len(session.routes), result.Gaps)
	}
}

func TestStoppedGenerationIsNotMarkedAbsent(t *testing.T) {
	publisher, err := New(heatingScope("instance.heating"))
	if err != nil {
		t.Fatal(err)
	}
	session := newHost()
	remote := rankedRemote(rankedReading("producer", "48.5", "notify", 4, 100))
	if _, err := publisher.Apply(context.Background(), session, []Remote{remote}); err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.StopGeneration(context.Background(), session, remote.SKI, 4); err != nil {
		t.Fatal(err)
	}
	remote.Entities[0].Readings = nil
	result, err := publisher.Apply(context.Background(), session, []Remote{remote})
	if err != nil {
		t.Fatal(err)
	}
	fact := requireFact(t, result, "resource.domestic_hot_water", "thermal.measurement.dhw_temperature", "48.5")
	if fact.Freshness != "stale" || fact.Availability != "available" || !fact.Authoritative {
		t.Fatalf("stopped fact was rewritten by omission: %+v", fact)
	}
}

func TestMemberReaderFollowsTheLatestValue(t *testing.T) {
	publisher, err := New(heatingScope("instance.heating"))
	if err != nil {
		t.Fatal(err)
	}
	session := newHost()
	remote := oneZone("device.heat", 1)
	remote.Brand = "synthetic-brand"
	if _, err := publisher.Apply(context.Background(), session, []Remote{remote}); err != nil {
		t.Fatal(err)
	}
	remote.Brand = "synthetic-other"
	if _, err := publisher.Apply(context.Background(), session, []Remote{remote}); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	for key, read := range session.readers {
		if strings.HasSuffix(key, "\ngateway_brand") {
			raw, err = read(context.Background())
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if string(raw) != "synthetic-other" {
		t.Fatalf("reader = %q", raw)
	}
}

func sensorRemote(unit, text string) Remote {
	reading := heat("outdoor_air_temperature", text)
	reading.Unit = unit
	return Remote{
		Device: "device.heat", SKI: "synthetic-ship-ski", Manufacturer: "synthetic-maker", Serial: "synthetic-serial",
		Ready: true, Generation: 4,
		Entities: []Entity{{Type: "TemperatureSensor", Address: []uint64{10}, Readings: []Reading{reading}}},
	}
}

func overrunRemote(text string) Remote {
	return Remote{
		Device: "device.heat", SKI: "synthetic-ship-ski", Manufacturer: "synthetic-maker", Serial: "synthetic-serial",
		Ready: true, Generation: 4, Readings: []Reading{mode("one_time_dhw", text)},
	}
}

func oneZoneMode(entityType, field, text string) Remote {
	reading := mode(field, text)
	return Remote{
		Device: "device.heat", SKI: "synthetic-ship-ski", Manufacturer: "synthetic-maker", Serial: "synthetic-serial",
		Ready: true, Generation: 4,
		Entities: []Entity{{Type: entityType, Address: []uint64{1}, Readings: []Reading{reading}}},
	}
}

func sameNumber(fact FactView, text string) bool {
	got, err := decimalRat(semreg.Decimal{Coefficient: fact.Coefficient, Exponent10: fact.Exponent})
	want, exact := new(big.Rat).SetString(text)
	return err == nil && exact && got.Cmp(want) == 0
}
