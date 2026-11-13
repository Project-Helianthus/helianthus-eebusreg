package eebusfacade

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

var observationReducerFixtureTime = time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)

func observationReducerLocalSKI() string { return strings.Repeat("1", 40) }

func observationReducerRemote(digit string) string { return strings.Repeat(digit, 40) }

func observationReducerString(value string) *string { return &value }

func observationReducerFixture(remote string) runtimeGraphObservation {
	return runtimeGraphObservation{
		RuntimeID: "runtime:" + observationReducerLocalSKI(), LocalSKI: observationReducerLocalSKI(),
		RemoteSKI: remote, Since: observationReducerFixtureTime,
	}
}

func TestRuntimeObservationReducerNormalizesObservation(t *testing.T) {
	source := runtimeGraphObservation{
		RuntimeID: " runtime:" + observationReducerLocalSKI() + " ", LocalSKI: " " + observationReducerLocalSKI(),
		RemoteSKI: observationReducerRemote("a") + " ", SessionID: " session:a:1 ", SessionState: " connected ",
		PairingState: " paired ", TrustDegradation: " denied-trust ", ShipID: " ship-a ", ServiceName: " Name ",
		ServiceIdentifier: " id ", ServiceBrand: " Brand ", ServiceType: " Type ", ServiceModel: " Model ",
		Since:      observationReducerFixtureTime.In(time.FixedZone("fixture", 7200)),
		ServiceIDs: []string{" service:b ", "service:a", "service:b"},
		Devices: []runtimeDeviceObservation{
			{ID: "device:b", Address: "d2", Type: "Generic"},
			{
				ID: " device:a ", SKI: " ski ", SHIPID: " ship ", Address: " d1 ", Type: " HeatPump ",
				UseCaseIDs: []string{"usecase:b", " usecase:a "},
				UseCases: []runtimeUseCaseObservation{
					{ID: "usecase:b", Name: "B", Scenarios: []string{" 2 ", "1"}},
					{ID: " usecase:a ", ContextAddress: " c ", Name: " A ", Actor: " actor "},
				},
				Entities: []runtimeEntityObservation{
					{ID: "entity:b", Features: []runtimeFeatureObservation{{ID: "feature:b", Role: "server"}, {ID: " feature:a ", Role: " client "}}},
					{ID: " entity:a ", DeviceAddress: " d1 ", EntityAddress: " [1] ", Type: " Appliance "},
				},
			},
			{ID: "device:a", Metadata: map[string]string{"label": "L"}},
		},
	}
	reducer := newRuntimeObservationReducer()
	if err := reducer.Replace(source); err != nil {
		t.Fatalf("replace: %v", err)
	}
	snapshot := reducer.Snapshot()
	if len(snapshot) != 1 {
		t.Fatalf("snapshot size = %d, want 1", len(snapshot))
	}
	got := snapshot[0]
	if got.RuntimeID != "runtime:"+observationReducerLocalSKI() || got.LocalSKI != observationReducerLocalSKI() ||
		got.RemoteSKI != observationReducerRemote("a") || got.SessionID != "session:a:1" || got.SessionState != "connected" ||
		got.PairingState != "paired" || got.TrustDegradation != "denied-trust" || got.ShipID != "ship-a" ||
		got.ServiceName != "Name" || got.ServiceIdentifier != "id" || got.ServiceBrand != "Brand" ||
		got.ServiceType != "Type" || got.ServiceModel != "Model" {
		t.Fatalf("scalar fields were not trimmed: %+v", got)
	}
	if got.Since.Location() != time.UTC || !got.Since.Equal(observationReducerFixtureTime) {
		t.Fatalf("since = %s, want UTC fixture instant", got.Since)
	}
	if !reflect.DeepEqual(got.ServiceIDs, []string{"service:a", "service:b"}) {
		t.Fatalf("service ids = %q", got.ServiceIDs)
	}
	if len(got.Devices) != 2 || got.Devices[0].ID != "device:a" || got.Devices[1].ID != "device:b" {
		t.Fatalf("devices were not merged and sorted: %+v", got.Devices)
	}
	device := got.Devices[0]
	if device.SKI != "ski" || device.SHIPID != "ship" || device.Address != "d1" || device.Type != "HeatPump" ||
		device.Metadata["label"] != "L" {
		t.Fatalf("device fields = %+v", device)
	}
	if !reflect.DeepEqual(device.UseCaseIDs, []string{"usecase:a", "usecase:b"}) ||
		device.UseCases[0].ID != "usecase:a" || device.UseCases[0].ContextAddress != "c" ||
		device.UseCases[0].Name != "A" || device.UseCases[0].Actor != "actor" ||
		!reflect.DeepEqual(device.UseCases[1].Scenarios, []string{"1", "2"}) {
		t.Fatalf("use cases = %+v ids=%q", device.UseCases, device.UseCaseIDs)
	}
	if device.Entities[0].ID != "entity:a" || device.Entities[0].DeviceAddress != "d1" ||
		device.Entities[0].EntityAddress != "[1]" || device.Entities[0].Type != "Appliance" ||
		device.Entities[1].Features[0].ID != "feature:a" || device.Entities[1].Features[0].Role != "client" {
		t.Fatalf("entities = %+v", device.Entities)
	}
}

func TestRuntimeObservationReducerRejectsInvalidObservations(t *testing.T) {
	device := func(mutate func(*runtimeDeviceObservation)) func(*runtimeGraphObservation) {
		return func(observation *runtimeGraphObservation) {
			value := runtimeDeviceObservation{ID: "device:a"}
			mutate(&value)
			observation.Devices = []runtimeDeviceObservation{value}
		}
	}
	tests := map[string]func(*runtimeGraphObservation){
		"runtime id":        func(observation *runtimeGraphObservation) { observation.RuntimeID = " " },
		"local ski":         func(observation *runtimeGraphObservation) { observation.LocalSKI = "" },
		"remote ski":        func(observation *runtimeGraphObservation) { observation.RemoteSKI = "" },
		"local ski format":  func(observation *runtimeGraphObservation) { observation.LocalSKI = strings.Repeat("z", 40) },
		"remote ski format": func(observation *runtimeGraphObservation) { observation.RemoteSKI = strings.Repeat("a", 39) },
		"session state": func(observation *runtimeGraphObservation) {
			observation.SessionID, observation.SessionState = "s", "paused"
		},
		"session without id": func(observation *runtimeGraphObservation) { observation.SessionState = "connected" },
		"id without session": func(observation *runtimeGraphObservation) { observation.SessionID = "s" },
		"pairing state":      func(observation *runtimeGraphObservation) { observation.PairingState = "pending" },
		"trust degradation":  func(observation *runtimeGraphObservation) { observation.TrustDegradation = "expired" },
		"zero since":         func(observation *runtimeGraphObservation) { observation.Since = time.Time{} },
		"blank service id":   func(observation *runtimeGraphObservation) { observation.ServiceIDs = []string{" "} },
		"device id":          device(func(value *runtimeDeviceObservation) { value.ID = " " }),
		"blank use case id":  device(func(value *runtimeDeviceObservation) { value.UseCaseIDs = []string{""} }),
		"use case identity":  device(func(value *runtimeDeviceObservation) { value.UseCases = []runtimeUseCaseObservation{{}} }),
		"entity identity":    device(func(value *runtimeDeviceObservation) { value.Entities = []runtimeEntityObservation{{}} }),
		"opaque value":       device(func(value *runtimeDeviceObservation) { value.Opaque = []runtimeOpaquePayload{{Value: func() {}}} }),
		"feature identity": device(func(value *runtimeDeviceObservation) {
			value.Entities = []runtimeEntityObservation{{ID: "entity:a", Features: []runtimeFeatureObservation{{}}}}
		}),
		"conflicting feature roles": device(func(value *runtimeDeviceObservation) {
			value.Entities = []runtimeEntityObservation{{ID: "entity:a", Features: []runtimeFeatureObservation{
				{ID: "feature:a", Role: "client"}, {ID: "feature:a", Role: "server"},
			}}}
		}),
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			reducer := newRuntimeObservationReducer()
			valid := observationReducerFixture(observationReducerRemote("a"))
			if err := reducer.Replace(valid); err != nil {
				t.Fatalf("seed: %v", err)
			}
			invalid := observationReducerFixture(observationReducerRemote("a"))
			invalid.ServiceIDs = []string{"service:changed"}
			mutate(&invalid)
			if err := reducer.Replace(invalid); err == nil {
				t.Fatal("invalid observation was accepted")
			}
			snapshot := reducer.Snapshot()
			if len(snapshot) != 1 || len(snapshot[0].ServiceIDs) != 0 || len(snapshot[0].Devices) != 0 {
				t.Fatalf("rejected observation replaced last-known-good state: %+v", snapshot)
			}
		})
	}
}

func TestRuntimeObservationReducerPinsRuntimeAndLocalIdentity(t *testing.T) {
	reducer := newRuntimeObservationReducer()
	if err := reducer.Replace(observationReducerFixture(observationReducerRemote("b"))); err != nil {
		t.Fatalf("seed: %v", err)
	}
	changedRuntime := observationReducerFixture(observationReducerRemote("a"))
	changedRuntime.RuntimeID = "runtime:other"
	if err := reducer.Replace(changedRuntime); err == nil || !strings.Contains(err.Error(), "runtime identity") {
		t.Fatalf("changed runtime identity error = %v", err)
	}
	changedLocal := observationReducerFixture(observationReducerRemote("a"))
	changedLocal.LocalSKI = observationReducerRemote("2")
	if err := reducer.Replace(changedLocal); err == nil || !strings.Contains(err.Error(), "local identity") {
		t.Fatalf("changed local identity error = %v", err)
	}
	if err := reducer.Replace(observationReducerFixture(observationReducerRemote("a"))); err != nil {
		t.Fatalf("same identities rejected: %v", err)
	}
	snapshot := reducer.Snapshot()
	if len(snapshot) != 2 || snapshot[0].RemoteSKI != observationReducerRemote("a") || snapshot[1].RemoteSKI != observationReducerRemote("b") {
		t.Fatalf("snapshot is not ordered by remote: %+v", snapshot)
	}
}

func TestRuntimeObservationReducerSnapshotIsDetached(t *testing.T) {
	source := observationReducerFixture(observationReducerRemote("a"))
	source.ServiceIDs = []string{"service:a"}
	source.Devices = []runtimeDeviceObservation{{
		ID: "device:a", Description: observationReducerString("D"), Metadata: map[string]string{"label": "L"},
		Opaque:     []runtimeOpaquePayload{{Path: "/p", Source: "s", Value: map[string]any{"k": "v"}}},
		UseCaseIDs: []string{"usecase:a"},
		UseCases:   []runtimeUseCaseObservation{{ID: "usecase:a", Scenarios: []string{"1"}, Version: observationReducerString("1")}},
		Entities: []runtimeEntityObservation{{ID: "entity:a", Description: observationReducerString("E"),
			Features: []runtimeFeatureObservation{{ID: "feature:a", Description: observationReducerString("F")}}}},
	}}
	reducer := newRuntimeObservationReducer()
	if err := reducer.Replace(source); err != nil {
		t.Fatalf("replace: %v", err)
	}
	*source.Devices[0].Description = "source changed"
	source.Devices[0].Metadata["label"] = "source changed"
	first := reducer.Snapshot()
	device := &first[0].Devices[0]
	first[0].ServiceIDs[0] = "changed"
	*device.Description = "changed"
	device.Metadata["label"] = "changed"
	device.Opaque[0].Value.(map[string]any)["k"] = "changed"
	device.UseCaseIDs[0] = "changed"
	device.UseCases[0].Scenarios[0] = "changed"
	*device.UseCases[0].Version = "changed"
	*device.Entities[0].Description = "changed"
	*device.Entities[0].Features[0].Description = "changed"
	second := reducer.Snapshot()[0]
	again := second.Devices[0]
	if second.ServiceIDs[0] != "service:a" || *again.Description != "D" || again.Metadata["label"] != "L" ||
		again.Opaque[0].Value.(map[string]any)["k"] != "v" || again.UseCaseIDs[0] != "usecase:a" ||
		again.UseCases[0].Scenarios[0] != "1" || *again.UseCases[0].Version != "1" ||
		*again.Entities[0].Description != "E" || *again.Entities[0].Features[0].Description != "F" {
		t.Fatalf("reducer state aliases a source or snapshot: %+v", second)
	}
}

func TestRuntimeObservationReducerMergesDeviceCollections(t *testing.T) {
	left := []runtimeDeviceObservation{{
		ID: "device:a", Type: "Old", Metadata: map[string]string{"left": "1", "shared": "left"},
		Opaque:     []runtimeOpaquePayload{{Path: "/p", Source: "s", Value: "v"}},
		UseCaseIDs: []string{"usecase:a"},
		UseCases:   []runtimeUseCaseObservation{{ID: "usecase:a", Name: "A", Scenarios: []string{"2"}}},
		Entities: []runtimeEntityObservation{{ID: "entity:a", Type: "Old",
			Features: []runtimeFeatureObservation{{ID: "feature:a", Role: "server"}}}},
	}}
	right := []runtimeDeviceObservation{
		{
			ID: "device:a", Type: "New", Metadata: map[string]string{"shared": "right"},
			Opaque: []runtimeOpaquePayload{
				{Path: "/p", Source: "s", Value: "v"},
				{Path: "/q", Source: "s", Value: "v"},
				{Path: "/p", Source: "t", Value: "v"},
				{Path: "/p", Source: "s", Value: "w"},
			},
			UseCaseIDs: []string{"usecase:b"},
			UseCases:   []runtimeUseCaseObservation{{ID: "usecase:a", Scenarios: []string{"1", " "}}},
			Entities: []runtimeEntityObservation{{ID: "entity:a", Type: "New",
				Features: []runtimeFeatureObservation{{ID: "feature:a", Type: "Measurement"}}}},
		},
		{ID: "device:b"},
	}
	merged, err := mergeRuntimeDeviceCollections(left, right)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if len(merged) != 2 || merged[0].ID != "device:a" || merged[1].ID != "device:b" {
		t.Fatalf("merged devices = %+v", merged)
	}
	device := merged[0]
	if device.Type != "New" || !reflect.DeepEqual(device.Metadata, map[string]string{"left": "1", "shared": "right"}) {
		t.Fatalf("device fields = %+v", device)
	}
	var opaque []string
	for _, value := range device.Opaque {
		encoded, _ := json.Marshal(value.Value)
		opaque = append(opaque, value.Path+"|"+value.Source+"|"+string(encoded))
	}
	if want := []string{`/p|s|"v"`, `/p|s|"w"`, `/p|t|"v"`, `/q|s|"v"`}; !reflect.DeepEqual(opaque, want) {
		t.Fatalf("opaque = %q, want %q", opaque, want)
	}
	if !reflect.DeepEqual(device.UseCaseIDs, []string{"usecase:a", "usecase:b"}) ||
		device.UseCases[0].Name != "A" || !reflect.DeepEqual(device.UseCases[0].Scenarios, []string{"1", "2"}) {
		t.Fatalf("use cases = %+v ids=%q", device.UseCases, device.UseCaseIDs)
	}
	feature := device.Entities[0].Features[0]
	if device.Entities[0].Type != "New" || feature.Role != "server" || feature.Type != "Measurement" {
		t.Fatalf("entities = %+v", device.Entities)
	}
	exact, err := exactRuntimeDeviceCollection(right)
	if err != nil || len(exact) != 2 || len(exact[0].Opaque) != 4 {
		t.Fatalf("exact collection = %+v err=%v", exact, err)
	}
}
