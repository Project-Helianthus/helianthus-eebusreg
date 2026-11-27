package inbound

import (
	"context"
	"strings"
	"testing"
)

func rankedReading(rank, text, carrier string, generation uint64, observed int64) Reading {
	return Reading{
		Field: "dhw_temperature", Rank: rank, Text: text, Unit: "degC", Carrier: carrier,
		Generation: generation, ObservedUnix: observed,
	}
}

func rankedRemote(readings ...Reading) Remote {
	return Remote{
		Device: "device.heat", SKI: "synthetic-ship-ski", Manufacturer: "synthetic-maker", Serial: "synthetic-serial",
		Ready: true, Generation: 4,
		Entities: []Entity{{Type: "DHWCircuit", Address: []uint64{3}, Readings: readings}},
	}
}

func otherZone() Remote {
	remote := rankedRemote(rankedReading("producer", "10", "notify", 4, 100))
	remote.Device = "device.other"
	remote.SKI = "synthetic-ship-other"
	remote.Entities[0].Type = "HeatingZone"
	remote.Entities[0].Readings[0].Field = "air_temperature"
	return remote
}

func factByPriority(result Result, priority int32) FactView {
	for _, fact := range result.Facts {
		if fact.Priority == priority && fact.FactID == "thermal.measurement.dhw_temperature" {
			return fact
		}
	}
	return FactView{}
}

func TestRankedSourcesKeepTheStaleProducer(t *testing.T) {
	publisher, err := New(heatingScope("instance.heating"))
	if err != nil {
		t.Fatal(err)
	}
	session := newHost()
	first, err := publisher.Apply(context.Background(), session, []Remote{
		rankedRemote(
			rankedReading("producer", "48.5", "notify", 4, 100),
			rankedReading("secondary", "41", "notify", 5, 500),
			rankedReading("relayed", "40", "notify", 5, 500),
		),
		otherZone(),
	})
	if err != nil {
		t.Fatal(err)
	}
	producer, secondary, relayed := factByPriority(first, PriorityProducer), factByPriority(first, PrioritySecondary), factByPriority(first, PriorityRelayed)
	if !producer.Authoritative || secondary.Authoritative || relayed.Authoritative || producer.Priority != 3 || secondary.Priority != 2 || relayed.Priority != 1 {
		t.Fatalf("producer %+v secondary %+v relayed %+v", producer, secondary, relayed)
	}
	if !sameKelvin(producer, "48.5") || !sameKelvin(secondary, "41") || !sameKelvin(relayed, "40") || producer.Path == relayed.Path {
		t.Fatalf("ranked values producer %+v relayed %+v", producer, relayed)
	}
	stopped, err := publisher.StopGeneration(context.Background(), session, "synthetic-ship-ski", 4)
	if err != nil {
		t.Fatal(err)
	}
	stale := factByPriority(stopped, PriorityProducer)
	if stale.Freshness != "stale" || stale.Availability != "available" || !stale.Authoritative || !sameKelvin(stale, "48.5") || stale.ObservedUnix != 100 {
		t.Fatalf("stale producer = %+v", stale)
	}
	if factByPriority(stopped, PriorityRelayed).Authoritative || factByPriority(stopped, PrioritySecondary).Authoritative {
		t.Fatal("a lower source became authoritative")
	}
	late, err := publisher.Apply(context.Background(), session, []Remote{rankedRemote(
		rankedReading("producer", "1", "reply", 4, 900),
		rankedReading("secondary", "41", "notify", 5, 500),
		rankedReading("relayed", "40", "notify", 5, 500),
	), otherZone()})
	if err != nil {
		t.Fatal(err)
	}
	ignored := factByPriority(late, PriorityProducer)
	if ignored.Carrier != "notify" || ignored.ObservedUnix != 100 || !sameKelvin(ignored, "48.5") || ignored.Freshness != "stale" {
		t.Fatalf("late reply replaced the stopped generation: %+v", ignored)
	}
	updated, err := publisher.Apply(context.Background(), session, []Remote{rankedRemote(
		rankedReading("producer", "49", "reply", 6, 600),
		rankedReading("relayed", "40", "notify", 5, 500),
	), otherZone()})
	if err != nil {
		t.Fatal(err)
	}
	refreshed := factByPriority(updated, PriorityProducer)
	if refreshed.Carrier != "reply" || refreshed.Freshness != "fresh" || refreshed.ObservedUnix != 600 || !refreshed.Authoritative || !sameKelvin(refreshed, "49") || refreshed.Path != producer.Path {
		t.Fatalf("newer reply = %+v path %s", refreshed, producer.Path)
	}
	revoked, err := publisher.Revoke(context.Background(), session, "synthetic-ship-ski")
	if err != nil {
		t.Fatal(err)
	}
	for _, fact := range revoked.Facts {
		if fact.FactID == "thermal.measurement.dhw_temperature" && (!fact.Withdrawn || fact.Authoritative || fact.Availability != "withdrawn" || fact.Coefficient == "") {
			t.Fatalf("revoked source = %+v", fact)
		}
	}
	other := requireFact(t, revoked, "resource.zone", "thermal.measurement.air_temperature", "10")
	if other.Withdrawn || other.Availability != "available" || !other.Authoritative {
		t.Fatalf("other remote = %+v", other)
	}
}

func TestReplyAndNotifyShareOnePath(t *testing.T) {
	publisher, err := New(heatingScope("instance.heating"))
	if err != nil {
		t.Fatal(err)
	}
	session := newHost()
	if _, err := publisher.Apply(context.Background(), session, []Remote{rankedRemote(rankedReading("producer", "48.5", "notify", 4, 100))}); err != nil {
		t.Fatal(err)
	}
	result, err := publisher.Apply(context.Background(), session, []Remote{rankedRemote(rankedReading("producer", "48.5", "reply", 4, 200))})
	if err != nil {
		t.Fatal(err)
	}
	matches := 0
	for _, fact := range result.Facts {
		if fact.FactID != "thermal.measurement.dhw_temperature" {
			continue
		}
		matches++
		if fact.Carrier != "reply" || fact.ObservedUnix != 200 || strings.Contains(fact.Path, "reply") || strings.Contains(fact.Path, "notify") {
			t.Fatalf("shared path = %+v", fact)
		}
	}
	if matches != 1 {
		t.Fatalf("temperature facts = %d", matches)
	}
}

func TestReplyWithoutNotifyPublishes(t *testing.T) {
	result, _ := publish(t, heatingScope("instance.heating"), rankedRemote(rankedReading("producer", "48.5", "reply", 4, 200)))
	fact := requireFact(t, result, "resource.domestic_hot_water", "thermal.measurement.dhw_temperature", "48.5")
	if fact.Carrier != "reply" || !sameKelvin(fact, "48.5") || strings.Contains(fact.Path, "reply") {
		t.Fatalf("reply only = %+v", fact)
	}
}

func TestEmptyRankIsTheProducerPath(t *testing.T) {
	empty := rankedReading("", "48.5", "notify", 4, 100)
	named := rankedReading("producer", "49", "notify", 4, 200)
	result, err := publishRanked(t, rankedRemote(empty, named))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Facts) != 1 || result.Facts[0].Priority != PriorityProducer || !sameKelvin(result.Facts[0], "49") {
		t.Fatalf("empty and producer ranks diverged: %+v", result.Facts)
	}
}

func TestEqualPriorityIsRejected(t *testing.T) {
	publisher, err := New(heatingScope("instance.heating"))
	if err != nil {
		t.Fatal(err)
	}
	session := newHost()
	first := rankedRemote(rankedReading("producer", "48.5", "notify", 4, 100))
	first.Entities = append(first.Entities, Entity{Type: "TemperatureSensor", Address: []uint64{10}, Readings: []Reading{heat("outdoor_air_temperature", "-5")}})
	if _, err := publisher.Apply(context.Background(), session, []Remote{first}); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Entities = append(append([]Entity{}, first.Entities...), Entity{Type: "TemperatureSensor", Address: []uint64{11}, Readings: []Reading{heat("outdoor_air_temperature", "-4")}})
	if _, err := publisher.Apply(context.Background(), session, []Remote{second}); err == nil || !strings.Contains(err.Error(), "same priority") {
		t.Fatalf("equal priority error = %v", err)
	}
	restored, err := publisher.Apply(context.Background(), session, []Remote{first})
	if err != nil {
		t.Fatal(err)
	}
	outdoor := 0
	for _, fact := range restored.Facts {
		if fact.FactID == "thermal.measurement.outdoor_air_temperature" {
			outdoor++
			if fact.Native != "-5" || fact.Availability != "available" {
				t.Fatalf("failed apply changed the world: %+v", fact)
			}
		}
	}
	if outdoor != 1 {
		t.Fatalf("outdoor facts = %d", outdoor)
	}
}

func publishRanked(t *testing.T, remote Remote) (Result, error) {
	t.Helper()
	publisher, err := New(heatingScope("instance.heating"))
	if err != nil {
		return Result{}, err
	}
	return publisher.Apply(context.Background(), newHost(), []Remote{remote})
}
