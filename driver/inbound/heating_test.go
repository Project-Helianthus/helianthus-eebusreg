package inbound

import (
	"context"
	"math/big"
	"strings"
	"testing"

	semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"
)

func publish(t *testing.T, scope Scope, remotes ...Remote) (Result, *host) {
	t.Helper()
	publisher, err := New(scope)
	if err != nil {
		t.Fatal(err)
	}
	session := newHost()
	result, err := publisher.Apply(context.Background(), session, remotes)
	if err != nil {
		t.Fatal(err)
	}
	return result, session
}

func heatingScope(instance string) Scope {
	return Scope{Installation: "installation.synthetic", Instance: instance, Bridge: "bridge.heating"}
}

func heat(field, text string) Reading {
	return Reading{Field: field, Text: text, Unit: "degC", Rank: "producer", Carrier: "notify", Generation: 4, ObservedUnix: 400}
}

func mode(field, text string) Reading {
	return Reading{Field: field, Text: text, Rank: "producer", Carrier: "notify", Generation: 4, ObservedUnix: 400}
}

func flag(field, text string) Reading {
	return Reading{Field: field, Text: text, Rank: "producer", Generation: 4, ObservedUnix: 400}
}

func zone(address uint64, parent []uint64, name, zoneMode, temperature, setpoint string) Entity {
	return Entity{
		Type: "HeatingZone", Address: []uint64{address}, Parent: parent, Name: name,
		Readings: []Reading{
			heat("air_temperature", temperature), heat("temperature_setpoint", setpoint), mode("zone_mode", zoneMode),
			flag("operation_mode_changeable", "true"), flag("setpoint_changeable", "false"), flag("overrun_changeable", "true"),
		},
	}
}

func room(address uint64, parent []uint64, temperature, setpoint, roomMode, activity string) Entity {
	setpointReading := heat("room_temperature_setpoint", setpoint)
	modeReading := mode("room_mode", roomMode)
	setpointReading.Writable = true
	setpointReading.Feature = "feature.room.setpoint." + entityText([]uint64{address})
	modeReading.Writable = true
	modeReading.Feature = "feature.room.mode." + entityText([]uint64{address})
	readings := []Reading{heat("room_air_temperature", temperature), setpointReading, modeReading}
	if activity != "" {
		readings = append(readings, flag("heating_activity", activity))
	}
	return Entity{Type: "HVACRoom", Address: []uint64{address}, Parent: parent, Readings: readings}
}

func heatingRemote() Remote {
	return Remote{
		Device: "device.heat", SKI: "synthetic-ship-ski", Manufacturer: "synthetic-maker", Serial: "synthetic-serial",
		IdentityToken: "0123456789AB", Brand: "synthetic-brand", Vendor: "synthetic-vendor", Ready: true, Generation: 4,
		Entities: []Entity{
			zone(1, nil, "north", "auto", "20", "21"),
			zone(2, nil, "south", "on", "21", "22"),
			{Type: "HeatingCircuit", Address: []uint64{7}},
			zone(8, []uint64{7}, "east", "eco", "22", "23"),
			room(6, []uint64{7}, "19", "20", "on", "running"),
			room(9, []uint64{8}, "18", "19", "boost", ""),
			{Type: "DHWCircuit", Address: []uint64{3}, Readings: []Reading{heat("dhw_temperature", "48.5"), heat("dhw_temperature_setpoint", "50"), mode("dhw_mode", "on")}},
			{Type: "TemperatureSensor", Address: []uint64{10}, Readings: []Reading{heat("outdoor_air_temperature", "-5")}},
		},
		Readings: []Reading{mode("one_time_dhw", "active")},
	}
}

func findFact(result Result, kind, id, native string) (FactView, bool) {
	for _, fact := range result.Facts {
		if fact.SubjectKind == kind && fact.FactID == id && fact.Native == native {
			return fact, true
		}
	}
	return FactView{}, false
}

func requireFact(t *testing.T, result Result, kind, id, native string) FactView {
	t.Helper()
	fact, ok := findFact(result, kind, id, native)
	if !ok {
		t.Fatalf("missing %s %s native %s", kind, id, native)
	}
	return fact
}

func TestHeatingPublication(t *testing.T) {
	result, session := publish(t, heatingScope("instance.heating"), heatingRemote())
	dhw := requireFact(t, result, "resource.domestic_hot_water", "thermal.measurement.dhw_temperature", "48.5")
	if dhw.Coefficient != "32165" || dhw.Exponent != -2 || dhw.Unit != "unit.kelvin" || dhw.NativeUnit != "degC" || !dhw.Authoritative || dhw.Priority != PriorityProducer {
		t.Fatalf("dhw temperature = %+v", dhw)
	}
	setpoint := requireFact(t, result, "resource.domestic_hot_water", "thermal.setpoint.dhw_temperature_setpoint", "50")
	if setpoint.Coefficient != "32315" || setpoint.Exponent != -2 {
		t.Fatalf("dhw setpoint = %+v", setpoint)
	}
	outdoor := requireFact(t, result, "resource.thermal_system", "thermal.measurement.outdoor_air_temperature", "-5")
	if outdoor.Coefficient != "26815" || outdoor.Exponent != -2 || outdoor.SubjectKind != "resource.thermal_system" {
		t.Fatalf("outdoor = %+v", outdoor)
	}
	for _, item := range []struct{ native, token string }{{"auto", "auto"}, {"on", "on"}, {"eco", "eco"}} {
		fact := requireFact(t, result, "resource.zone", "thermal.mode.zone", item.native)
		if fact.SymbolNamespace != "thermal.mode.zone" || fact.SymbolToken != item.token || !fact.SymbolKnown || fact.SymbolToken == "comfort" {
			t.Fatalf("zone mode %s = %+v", item.native, fact)
		}
	}
	charge := requireFact(t, result, "resource.thermal_system", "thermal.overrun.one_time_dhw", "active")
	if charge.SymbolNamespace != "thermal.overrun.one_time_dhw" || charge.SymbolToken != "active" || !charge.SymbolKnown {
		t.Fatalf("one time charge = %+v", charge)
	}
	roomMode := requireFact(t, result, "resource.room", "thermal.room.mode_zone", "on")
	if roomMode.SymbolToken != "on" || roomMode.SymbolNamespace != "thermal.room.mode_zone" {
		t.Fatalf("room mode = %+v", roomMode)
	}
	unknown := requireFact(t, result, "resource.room", "thermal.room.mode_zone", "boost")
	if unknown.SymbolKnown || unknown.SymbolToken != "boost" || unknown.SymbolNamespace != "native.hvac.mode" {
		t.Fatalf("unknown mode = %+v", unknown)
	}
	if _, ok := findFact(result, "resource.zone", "thermal.room.mode_zone", "on"); ok {
		t.Fatal("room mode was cloned onto a zone")
	}
	if _, ok := findFact(result, "resource.room", "thermal.measurement.air_temperature", "19"); ok {
		t.Fatal("zone temperature was cloned onto a room")
	}
	dhwMode := requireFact(t, result, "resource.domestic_hot_water", "thermal.mode.dhw", "on")
	if dhwMode.SymbolToken != "on" || !dhwMode.SymbolKnown || dhwMode.SymbolNamespace != "thermal.mode.dhw" {
		t.Fatalf("dhw mode = %+v", dhwMode)
	}
	zoneSlots := map[string]bool{}
	for _, native := range []string{"20", "21", "22"} {
		fact := requireFact(t, result, "resource.zone", "thermal.measurement.air_temperature", native)
		if !sameKelvin(fact, native) || fact.SubjectKind != "resource.zone" || fact.NativeUnit != "degC" {
			t.Fatalf("zone temperature %s = %+v", native, fact)
		}
		zoneSlots[fact.SubjectSlot] = true
	}
	if len(zoneSlots) != 3 {
		t.Fatalf("zone temperatures share a subject: %+v", zoneSlots)
	}
	setpointSlots := map[string]bool{}
	for _, native := range []string{"21", "22", "23"} {
		fact := requireFact(t, result, "resource.zone", "thermal.setpoint.temperature_setpoint", native)
		if !sameKelvin(fact, native) || !zoneSlots[fact.SubjectSlot] {
			t.Fatalf("zone setpoint %s = %+v", native, fact)
		}
		setpointSlots[fact.SubjectSlot] = true
	}
	if len(setpointSlots) != 3 {
		t.Fatalf("zone setpoints share a subject: %+v", setpointSlots)
	}
	roomSlots := map[string]bool{}
	for _, native := range []string{"19", "18"} {
		fact := requireFact(t, result, "resource.room", "thermal.room.measurement_air_temperature", native)
		if !sameKelvin(fact, native) || fact.SubjectKind != "resource.room" || zoneSlots[fact.SubjectSlot] {
			t.Fatalf("room temperature %s = %+v", native, fact)
		}
		roomSlots[fact.SubjectSlot] = true
	}
	if len(roomSlots) != 2 {
		t.Fatalf("room temperatures share a subject: %+v", roomSlots)
	}
	for _, native := range []string{"20", "19"} {
		fact := requireFact(t, result, "resource.room", "thermal.room.setpoint_temperature_setpoint", native)
		if !sameKelvin(fact, native) || !roomSlots[fact.SubjectSlot] || zoneSlots[fact.SubjectSlot] {
			t.Fatalf("room setpoint %s = %+v", native, fact)
		}
	}
	assertLinks(t, result)
	assertMembers(t, result)
	assertNoCarrierInPaths(t, result)
	gaps := gapSet(result)
	if !gaps["ship_ski_scheme_unlisted:device.heat"] || !gaps["device.heat:unknown_mode:boost"] {
		t.Fatalf("gaps = %v", result.Gaps)
	}
	if len(session.batches) != 1 || len(session.samples) == 0 || len(session.serials) != 1 || len(session.matches) != 1 {
		t.Fatalf("staged batches %d samples %d serials %d matches %d", len(session.batches), len(session.samples), len(session.serials), len(session.matches))
	}
	if session.serials[0].Manufacturer != "synthetic-maker" || session.serials[0].Serial != "synthetic-serial" || session.matches[0].Scheme != "scheme.ieee.eui48" || session.matches[0].Issuer != "" {
		t.Fatalf("claims serial %+v match %+v", session.serials[0], session.matches[0])
	}
	affine := celsiusAffine()
	if affine.Num != 1 || affine.Den != 1 || affine.ExponentRow != "" || affine.Add.Coefficient != "27315" || affine.Add.Exponent10 != -2 {
		t.Fatalf("celsius row affine = %+v", affine)
	}
	if dhw.Coefficient != "32165" || dhw.Exponent != -2 || !sameKelvin(dhw, "48.5") {
		t.Fatalf("dhw temperature did not apply the row affine: %+v", dhw)
	}
}

func assertLinks(t *testing.T, result Result) {
	t.Helper()
	system := "device/device.heat"
	entity := func(address string) string { return system + "/entity/" + address }
	want := map[string]bool{}
	for _, address := range []string{"1", "2", "8", "7", "3"} {
		want["thermal.relation.system_member "+system+" "+entity(address)] = true
	}
	want["thermal.relation.circuit_room "+entity("7")+" "+entity("6")] = true
	want["thermal.relation.circuit_zone "+entity("7")+" "+entity("8")] = true
	want["thermal.relation.zone_room "+entity("8")+" "+entity("9")] = true
	got := map[string]bool{}
	for _, link := range result.Links {
		got[string(link.Key.Type.ID)+" "+link.FromSlot+" "+link.ToSlot] = true
		if strings.Contains(string(link.Key.Type.ID), "fluid") {
			t.Fatalf("fluid relation was inferred: %+v", link)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("links = %+v", got)
	}
	for link := range want {
		if !got[link] {
			t.Fatalf("missing link %s in %+v", link, got)
		}
	}
}

func assertMembers(t *testing.T, result Result) {
	t.Helper()
	names := map[string]bool{}
	activityNodes := []string{}
	for _, member := range result.Members {
		if member.Member.Key.Namespace != "eebus-inbound" || member.Member.Scope != "instance" || member.Member.Key.Instance != "instance.heating" {
			t.Fatalf("member key = %+v", member.Member)
		}
		names[string(member.Member.Key.Local)+"="+member.Value] = true
		if member.Member.Key.Local == "heating_activity" {
			activityNodes = append(activityNodes, string(member.Member.Key.Node))
		}
		if member.Member.Key.Local == "zone_name" && !strings.Contains(string(member.Member.Key.Node), "entity/") {
			t.Fatalf("zone name is not on its zone node: %+v", member.Member)
		}
	}
	for _, pair := range []string{"zone_name=north", "zone_name=south", "zone_name=east", "gateway_brand=synthetic-brand", "gateway_vendor=synthetic-vendor", "mode_changeable=true", "setpoint_changeable=false", "overrun_changeable=true", "heating_activity=running", "ship_ski=synthetic-ship-ski"} {
		if !names[pair] {
			t.Fatalf("missing member %s in %+v", pair, names)
		}
	}
	if len(activityNodes) != 1 || !strings.Contains(activityNodes[0], "entity/6") {
		t.Fatalf("heating activity nodes = %v", activityNodes)
	}
	for _, fact := range result.Facts {
		if fact.FactID == "equipment.identification.brand_name" || fact.FactID == "equipment.identification.vendor_name" || fact.FactID == "thermal.room.heating_activity" {
			t.Fatalf("namespace value published as fact %s", fact.FactID)
		}
	}
}

func assertNoCarrierInPaths(t *testing.T, result Result) {
	t.Helper()
	for _, fact := range result.Facts {
		if strings.Contains(fact.Path, "reply") || strings.Contains(fact.Path, "notify") {
			t.Fatalf("carrier is part of the path %s", fact.Path)
		}
	}
}

func gapSet(result Result) map[string]bool {
	gaps := map[string]bool{}
	for _, gap := range result.Gaps {
		gaps[gap] = true
	}
	return gaps
}

func sameKelvin(fact FactView, celsius string) bool {
	if fact.Unit != "unit.kelvin" {
		return false
	}
	got, ok := decimalRat(semreg.Decimal{Coefficient: fact.Coefficient, Exponent10: fact.Exponent})
	if ok != nil {
		return false
	}
	want, exact := new(big.Rat).SetString(celsius)
	if !exact {
		return false
	}
	want.Add(want, big.NewRat(27315, 100))
	return got.Cmp(want) == 0
}

func TestNotReadyKeepsLastValue(t *testing.T) {
	publisher, err := New(heatingScope("instance.heating"))
	if err != nil {
		t.Fatal(err)
	}
	session := newHost()
	first, err := publisher.Apply(context.Background(), session, []Remote{heatingRemote()})
	if err != nil {
		t.Fatal(err)
	}
	registeredNodes, registeredMembers := len(session.nodes), len(session.members)
	remote := heatingRemote()
	remote.Ready = false
	remote.Entities[6].Readings[0].Text = "0"
	second, err := publisher.Apply(context.Background(), session, []Remote{remote})
	if err != nil {
		t.Fatal(err)
	}
	before := requireFact(t, first, "resource.domestic_hot_water", "thermal.measurement.dhw_temperature", "48.5")
	after := requireFact(t, second, "resource.domestic_hot_water", "thermal.measurement.dhw_temperature", "48.5")
	if after.Coefficient != before.Coefficient || after.Exponent != before.Exponent || after.Availability != "unavailable" || after.Freshness != before.Freshness {
		t.Fatalf("not ready fact = %+v", after)
	}
	if len(session.nodes) != registeredNodes || len(session.members) != registeredMembers {
		t.Fatalf("second apply registered nodes %d members %d again", len(session.nodes), len(session.members))
	}
	remote.Ready = true
	remote.Entities[6].Readings[0].Text = "49"
	third, err := publisher.Apply(context.Background(), session, []Remote{remote})
	if err != nil {
		t.Fatal(err)
	}
	restored := requireFact(t, third, "resource.domestic_hot_water", "thermal.measurement.dhw_temperature", "49")
	if restored.Availability != "available" || !sameKelvin(restored, "49") {
		t.Fatalf("ready again = %+v", restored)
	}
}

func TestRoomRoutesUseRoomSubject(t *testing.T) {
	result, session := publish(t, heatingScope("instance.heating"), heatingRemote())
	if len(session.routes) == 0 {
		t.Fatal("no room route")
	}
	rooms := map[semreg.ResourceID]string{}
	zones := map[semreg.ResourceID]bool{}
	for _, node := range result.Nodes {
		switch node.Key.Kind {
		case "resource.room":
			rooms[node.ID] = node.Key.Anchor.Slot
		case "resource.zone":
			zones[node.ID] = true
		}
	}
	seen := map[string]map[semreg.ResourceID]bool{}
	for _, route := range session.routes {
		id := string(route.Operation.ID)
		if seen[id] == nil {
			seen[id] = map[semreg.ResourceID]bool{}
		}
		seen[id][route.For.Subject.ID] = true
		if zones[route.For.Subject.ID] || rooms[route.For.Subject.ID] == "" || route.Route.DriverGeneration != "4" {
			t.Fatalf("route %s subject %s slot %s", id, route.For.Subject.ID, rooms[route.For.Subject.ID])
		}
	}
	if len(rooms) != 2 {
		t.Fatalf("rooms = %+v", rooms)
	}
	for _, id := range []string{"thermal.room.operation_set_temperature", "thermal.room.operation_set_zone_mode"} {
		if len(seen[id]) != len(rooms) {
			t.Fatalf("route %s subjects = %+v rooms = %+v", id, seen[id], rooms)
		}
	}
	for _, fact := range result.Facts {
		if fact.SubjectKind == "resource.zone" && (fact.FactID == "thermal.room.operation_set_temperature" || fact.FactID == "thermal.room.setpoint_temperature_setpoint") {
			t.Fatalf("room fact on zone %s", fact.FactID)
		}
	}
}
