package inbound

import (
	"context"
	"testing"

	semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"
)

func TestChargerPublication(t *testing.T) {
	result, session := publish(t, electricalScope("instance.charger"), chargerRemote(true, "normalOperation", "normalOperation"))
	assertNoAccuracy(t, result)
	requireSymbol(t, result, "resource.connector", "evse.status.connection", "connected")
	charging := requireSymbol(t, result, "resource.connector", "evse.status.charging", "charging")
	if charging.SubjectKind != "resource.connector" {
		t.Fatalf("charging subject = %s", charging.SubjectKind)
	}
	requireSymbol(t, result, "resource.evse", "evse.status.fault", "clear")
	requireSymbol(t, result, "resource.evse", "evse.status.readiness", "ready")
	energy := requireFact(t, result, "resource.charging_session", "evse.energy.session", "5")
	requireQuantity(t, energy, "18", 3, "unit.joule")
	current := requireFact(t, result, "resource.phase", "evse.ac.current", "10")
	requireQuantity(t, current, "1", 1, "unit.ampere")
	active := requireFact(t, result, "resource.phase", "evse.ac.active_power", "1000")
	requireQuantity(t, active, "1", 3, "unit.watt")
	name := requireFact(t, result, "resource.connector", "evse.evsecc.device_name", "synthetic-station")
	if name.Text != "synthetic-station" {
		t.Fatalf("manufacturer text = %+v", name)
	}
	if linkCount(result, "evse.relation.connector_phase") != 3 || linkCount(result, "evse.relation.connector_session") != 1 {
		t.Fatalf("links = %+v", result.Links)
	}
	if linkCount(result, "evse.relation.asset_evse") != 0 || linkCount(result, "evse.relation.evse_connector") != 0 {
		t.Fatalf("configuration relation was inferred: %+v", result.Links)
	}
	gaps := gapSet(result)
	if !gaps["device.charger:configuration_only_relation:evse.relation.asset_evse"] || !gaps["device.charger:configuration_only_relation:evse.relation.evse_connector"] {
		t.Fatalf("gaps = %+v", result.Gaps)
	}
	assertChargerAnchors(t, session.batches[0], semreg.ResourceCurrent)
	if len(result.Serials) != 1 || result.Serials[0].Manufacturer != "synthetic-maker" || result.Serials[0].Serial != "synthetic-serial" {
		t.Fatalf("serials = %+v", result.Serials)
	}
}

func TestChargerChargingSymbols(t *testing.T) {
	idle, _ := publish(t, electricalScope("instance.charger"), chargerRemote(false, "", ""))
	requireSymbol(t, idle, "resource.connector", "evse.status.connection", "disconnected")
	requireSymbol(t, idle, "resource.connector", "evse.status.charging", "idle")
	if len(nodeSlots(idle, "resource.charging_session")) != 0 {
		t.Fatal("disconnected ev published a session")
	}
	finished, _ := publish(t, electricalScope("instance.charger"), chargerRemote(true, "finished", ""))
	requireSymbol(t, finished, "resource.connector", "evse.status.charging", "complete")
}

func TestChargerProjectionLoss(t *testing.T) {
	cases := []struct {
		state string
		fault string
		ready string
	}{
		{state: "failure", fault: "present"},
		{state: "temporarilyNotReady", ready: "not_ready"},
		{state: "standby"},
	}
	for _, item := range cases {
		result, _ := publish(t, electricalScope("instance.charger"), chargerRemote(true, "", item.state))
		if item.fault != "" {
			requireSymbol(t, result, "resource.evse", "evse.status.fault", item.fault)
		} else {
			denySymbol(t, result, "evse.status.fault")
		}
		if item.ready != "" {
			requireSymbol(t, result, "resource.evse", "evse.status.readiness", item.ready)
		} else {
			denySymbol(t, result, "evse.status.readiness")
		}
		gaps := gapSet(result)
		if item.fault == "" && !gaps["device.charger:projection_loss:evse.status.fault:"+item.state] {
			t.Fatalf("fault loss %s gaps = %+v", item.state, result.Gaps)
		}
		if item.ready == "" && !gaps["device.charger:projection_loss:evse.status.readiness:"+item.state] {
			t.Fatalf("readiness loss %s gaps = %+v", item.state, result.Gaps)
		}
	}
	held, _ := publish(t, electricalScope("instance.charger"), chargerRemote(true, "standby", "normalOperation"))
	denySymbol(t, held, "evse.status.charging")
	if !gapSet(held)["device.charger:projection_loss:evse.status.charging:standby"] {
		t.Fatalf("gaps = %+v", held.Gaps)
	}
}

func TestChargerSessionEnds(t *testing.T) {
	publisher, err := New(electricalScope("instance.charger"))
	if err != nil {
		t.Fatal(err)
	}
	session := newHost()
	first, err := publisher.Apply(context.Background(), session, []Remote{chargerRemote(true, "normalOperation", "normalOperation")})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodeSlots(first, "resource.charging_session")) != 1 {
		t.Fatal("connected ev has no session")
	}
	second, err := publisher.Apply(context.Background(), session, []Remote{chargerRemote(false, "", "normalOperation")})
	if err != nil {
		t.Fatal(err)
	}
	requireSymbol(t, second, "resource.connector", "evse.status.connection", "disconnected")
	requireSymbol(t, second, "resource.connector", "evse.status.charging", "idle")
	if slots := nodeSlots(second, "resource.charging_session"); len(slots) != 1 || second.Nodes[indexKind(second, "resource.charging_session")].State != string(semreg.ResourceRetired) {
		t.Fatalf("session nodes = %+v", nodeSlots(second, "resource.charging_session"))
	}
	for _, fact := range second.Facts {
		if fact.FactID == "evse.energy.session" && !fact.Withdrawn {
			t.Fatal("session energy stayed current")
		}
	}
	if linkCount(second, "evse.relation.connector_session") != 0 {
		t.Fatalf("session link stayed: %+v", second.Links)
	}
	batch := session.batches[len(session.batches)-1]
	sessions := resourcesOf(batch, "resource.charging_session")
	if len(sessions) != 1 || sessions[0].State != semreg.ResourceRetired {
		t.Fatalf("session records = %+v", sessions)
	}
	if len(batch.RelationWithdrawals) != 1 || batch.RelationWithdrawals[0].Definition.ID != "evse.relation.connector_session" {
		t.Fatalf("withdrawals = %+v", batch.RelationWithdrawals)
	}
	for _, kind := range []string{"resource.charging_station", "resource.evse", "resource.connector", "resource.phase"} {
		for _, resource := range resourcesOf(batch, kind) {
			if resource.State != semreg.ResourceCurrent {
				t.Fatalf("%s state = %s", kind, resource.State)
			}
		}
	}
}

func TestChargerVehicle(t *testing.T) {
	remote := chargerRemote(true, "", "")
	remote.Entities[1].Readings = append(remote.Entities[1].Readings,
		measure("stateOfCharge", "25", "%", ""), measure("sleepMode", "true", "", ""), measure("communicationStandard", "iec61851", "", ""),
	)
	result, _ := publish(t, electricalScope("instance.charger"), remote)
	charge := requireFact(t, result, "resource.vehicle", "evse.vehicle.state_of_charge", "25")
	requireQuantity(t, charge, "25", -2, "unit.ratio")
	denyFact(t, result, "storage.system.state_of_charge", "25")
	requireSymbol(t, result, "resource.vehicle", "evse.vehicle.sleep", "sleeping")
	requireSymbol(t, result, "resource.charging_session", "evse.session.communication_standard", "iec_61851")
	lost := chargerRemote(true, "", "")
	lost.Entities[1].Readings = append(lost.Entities[1].Readings, measure("communicationStandard", "iso15118-2ed2", "", ""))
	rejected, _ := publish(t, electricalScope("instance.charger"), lost)
	denySymbol(t, rejected, "evse.session.communication_standard")
	if !gapSet(rejected)["device.charger:projection_loss:evse.session.communication_standard:iso15118-2ed2"] {
		t.Fatalf("gaps = %+v", rejected.Gaps)
	}
}

func chargerRemote(connected bool, charge, operating string) Remote {
	evse := []Reading{measure("deviceName", "synthetic-station", "", "")}
	if operating != "" {
		evse = append(evse, measure("operatingState", operating, "", ""))
	}
	evse = append(evse,
		measure("currentPerPhase", "10", "A", "a"), measure("currentPerPhase", "11", "A", "b"), measure("currentPerPhase", "12", "A", "c"),
		measure("powerPerPhase", "1000", "W", "a"), measure("powerPerPhase", "1100", "W", "b"), measure("powerPerPhase", "1200", "W", "c"),
		measure("energyCharged", "5", "Wh", ""),
	)
	text := "false"
	if connected {
		text = "true"
	}
	ev := []Reading{measure("connected", text, "", "")}
	if charge != "" {
		ev = append(ev, measure("chargeState", charge, "", ""))
	}
	return Remote{
		Device: "device.charger", SKI: "synthetic-ship-ski", Manufacturer: "synthetic-maker", Serial: "synthetic-serial",
		Ready: true, Generation: 4,
		Entities: []Entity{
			{Type: "EVSE", Address: []uint64{1}, Readings: evse},
			{Type: "EV", Address: []uint64{2}, Readings: ev},
		},
	}
}

func assertChargerAnchors(t *testing.T, batch semreg.PublicationBatch, sessionState semreg.ResourceState) {
	t.Helper()
	stations := resourcesOf(batch, "resource.charging_station")
	evse := resourcesOf(batch, "resource.evse")
	connectors := resourcesOf(batch, "resource.connector")
	phases := resourcesOf(batch, "resource.phase")
	sessions := resourcesOf(batch, "resource.charging_session")
	vehicles := resourcesOf(batch, "resource.vehicle")
	if len(stations) != 1 || len(evse) != 1 || len(connectors) != 1 || len(phases) != 3 || len(sessions) != 1 || len(vehicles) != 1 {
		t.Fatalf("station %d evse %d connector %d phase %d session %d vehicle %d", len(stations), len(evse), len(connectors), len(phases), len(sessions), len(vehicles))
	}
	assertParent(t, stations[0], semreg.AnchorEquipment, false)
	assertParent(t, evse[0], semreg.AnchorComponent, true)
	assertParent(t, connectors[0], semreg.AnchorComponent, true)
	assertParent(t, vehicles[0], semreg.AnchorEquipment, false)
	for _, phase := range phases {
		assertParent(t, phase, semreg.AnchorComponent, true)
	}
	assertParent(t, sessions[0], semreg.AnchorEpisode, true)
	if sessions[0].State != sessionState {
		t.Fatalf("session state = %s", sessions[0].State)
	}
	for _, relation := range batch.RelationUpserts {
		switch relation.Definition.ID {
		case "evse.relation.connector_phase", "evse.relation.connector_session":
			if relation.BasisClass != semreg.RelationGateRule {
				t.Fatalf("basis %s = %s", relation.Definition.ID, relation.BasisClass)
			}
		}
	}
}

func denySymbol(t *testing.T, result Result, id string) {
	t.Helper()
	for _, fact := range result.Facts {
		if fact.FactID == id && !fact.Withdrawn {
			t.Fatalf("published symbol %s token %s", id, fact.SymbolToken)
		}
	}
}

func indexKind(result Result, kind string) int {
	for i, node := range result.Nodes {
		if string(node.Key.Kind) == kind {
			return i
		}
	}
	return 0
}
