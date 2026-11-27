package inbound

import (
	"context"
	"testing"

	semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"
)

func TestAggregatePublication(t *testing.T) {
	result, session := publish(t, electricalScope("instance.aggregate"), aggregateRemote("50"))
	assertNoAccuracy(t, result)
	peak := requireFact(t, result, "resource.pv_system", "pv.nameplate.peak_power", "4000")
	requireQuantity(t, peak, "4", 3, "unit.watt")
	power := requireFact(t, result, "resource.pv_system", "pv.vapd.ac_power_total", "-1500")
	requireQuantity(t, power, "-15", 2, "unit.watt")
	yield := requireFact(t, result, "resource.pv_system", "pv.vapd.ac_yield_total", "5")
	requireQuantity(t, yield, "18", 3, "unit.joule")
	denyFact(t, result, "pv.system.active_power", "-1500")
	denyFact(t, result, "pv.energy.generated", "5")
	battery := requireFact(t, result, "resource.storage_system", "storage.system.ac_power", "-800")
	requireQuantity(t, battery, "-8", 2, "unit.watt")
	charged := requireFact(t, result, "resource.storage_system", "storage.system.energy_charged", "3")
	requireQuantity(t, charged, "108", 2, "unit.joule")
	discharged := requireFact(t, result, "resource.storage_system", "storage.system.energy_discharged", "4")
	requireQuantity(t, discharged, "144", 2, "unit.joule")
	stored := requireFact(t, result, "resource.storage_system", "storage.system.state_of_charge", "50")
	requireQuantity(t, stored, "5", -1, "unit.ratio")
	denyFact(t, result, "storage.state.soc", "50")
	if len(nodeSlots(result, "resource.battery_pack")) != 0 || len(nodeSlots(result, "resource.inverter")) != 0 {
		t.Fatal("aggregate landed on a pack or inverter")
	}
	gaps := gapSet(result)
	for _, gap := range []string{
		"device.plant:sign_reference_uninstantiated:pv.vapd.ac_power_total",
		"device.plant:sign_reference_uninstantiated:pv.vapd.ac_yield_total",
		"device.plant:sign_reference_uninstantiated:storage.system.ac_power",
	} {
		if !gaps[gap] {
			t.Fatalf("missing %s in %+v", gap, result.Gaps)
		}
	}
	seenStorage, seenPV := false, false
	for _, sample := range session.samples {
		switch sample.Candidate.Key.FactID {
		case "storage.system.state_of_charge":
			seenStorage = true
			if sample.Candidate.Key.PackVersion != "1.1.0" || sample.Candidate.Key.PackID != "helianthus.pack.storage" {
				t.Fatalf("storage key = %+v", sample.Candidate.Key)
			}
		case "pv.vapd.ac_yield_total":
			seenPV = true
			if sample.Candidate.Key.PackVersion != "1.0.0" || sample.Candidate.Key.PackID != "helianthus.pack.pv" {
				t.Fatalf("pv key = %+v", sample.Candidate.Key)
			}
		}
	}
	if !seenStorage || !seenPV {
		t.Fatal("aggregate samples were not staged")
	}
	other, _ := publish(t, electricalScope("instance.aggregate"), aggregateRemote("80"))
	ratio := requireFact(t, other, "resource.storage_system", "storage.system.state_of_charge", "80")
	requireQuantity(t, ratio, "8", -1, "unit.ratio")
}

func TestAggregateUnavailableKeepsValue(t *testing.T) {
	publisher, err := New(electricalScope("instance.aggregate"))
	if err != nil {
		t.Fatal(err)
	}
	session := newHost()
	if _, err := publisher.Apply(context.Background(), session, []Remote{aggregateRemote("50")}); err != nil {
		t.Fatal(err)
	}
	remote := aggregateRemote("50")
	remote.Ready = false
	result, err := publisher.Apply(context.Background(), session, []Remote{remote})
	if err != nil {
		t.Fatal(err)
	}
	fact := requireFact(t, result, "resource.storage_system", "storage.system.state_of_charge", "50")
	if fact.Coefficient != "5" || fact.Exponent != -1 || fact.Availability != string(semreg.AvailabilityUnavailable) {
		t.Fatalf("unavailable = %+v", fact)
	}
}

func TestAggregateRatioBounds(t *testing.T) {
	result, _ := publish(t, electricalScope("instance.aggregate"), aggregateRemote("150"))
	denyFact(t, result, "storage.system.state_of_charge", "150")
	if !gapSet(result)["device.plant:ratio_out_of_range:stateOfCharge"] {
		t.Fatalf("gaps = %+v", result.Gaps)
	}
}

func aggregateRemote(percent string) Remote {
	return Remote{
		Device: "device.plant", SKI: "synthetic-ship-ski", Manufacturer: "synthetic-maker", Serial: "synthetic-serial",
		Ready: true, Generation: 4,
		Entities: []Entity{
			{Type: "PVSystem", Address: []uint64{1}, Readings: []Reading{
				measure("nominalPeakPower", "4000", "W", ""), measure("power", "-1500", "W", ""), measure("yieldTotal", "5", "Wh", ""),
			}},
			{Type: "BatterySystem", Address: []uint64{2}, Readings: []Reading{
				measure("power", "-800", "W", ""), measure("energyCharged", "3", "Wh", ""), measure("energyDischarged", "4", "Wh", ""),
				measure("stateOfCharge", percent, "%", ""),
			}},
		},
	}
}
