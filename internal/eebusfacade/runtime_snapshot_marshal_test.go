package eebusfacade

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var snapshotMarshalFixtureTime = time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)

func snapshotMarshalLocalSKI() string  { return strings.Repeat("1", 40) }
func snapshotMarshalRemoteA() string   { return strings.Repeat("a", 40) }
func snapshotMarshalRemoteB() string   { return strings.Repeat("b", 40) }
func snapshotMarshalRuntimeID() string { return "runtime:" + snapshotMarshalLocalSKI() }

func snapshotMarshalString(value string) *string { return &value }

func snapshotMarshalBool(value bool) *bool { return &value }

func snapshotMarshalRuntimeJSON(runtimeID string) string {
	sum := sha256.Sum256([]byte(runtimeID))
	return `{"kind":"peer","masked":"[redacted]","digest":"sha256:` + hex.EncodeToString(sum[:]) + `"}`
}

func snapshotMarshalFixtureGraph() []runtimeGraphObservation {
	since := snapshotMarshalFixtureTime.Add(-time.Minute)
	device := runtimeDeviceObservation{
		ID: "device:a", Address: "d1", Type: "HeatPump", Description: snapshotMarshalString("Device A"),
		Metadata: map[string]string{"label": "L", "network_feature_set": "simple"},
		Opaque: []runtimeOpaquePayload{{
			Path: "/devices/d1/destination_data", Source: "spine.detailed-discovery",
			Value: map[string]any{"n": json.Number("1.50"), "s": "x"},
		}},
		Entities: []runtimeEntityObservation{
			{
				ID: "entity:a", DeviceAddress: "d1", EntityAddress: "[1]", Type: "HeatPumpAppliance",
				Description: snapshotMarshalString("Entity A"),
				Features: []runtimeFeatureObservation{
					{ID: "feature:a", DeviceAddress: "d1", EntityAddress: "[1]", FeatureAddress: "[1]:2", Type: "Measurement", Role: "server"},
					{ID: "feature:no-role", DeviceAddress: "d1", EntityAddress: "[1]", FeatureAddress: "[1]:3", Type: "Measurement"},
				},
			},
			{
				ID: "entity:no-type", DeviceAddress: "d1", EntityAddress: "[2]",
				Features: []runtimeFeatureObservation{
					{ID: "feature:hidden", DeviceAddress: "d1", EntityAddress: "[2]", FeatureAddress: "[2]:1", Type: "Generic", Role: "client"},
				},
			},
		},
		UseCaseIDs: []string{"usecase:a"},
		UseCases: []runtimeUseCaseObservation{
			{
				ID: "usecase:a", ContextAddress: "d1:[1]", Name: "useCaseA", Actor: "HeatPump",
				ResolvedRole: snapshotMarshalString("server"), Scenarios: []string{"1", "2"},
				Version: snapshotMarshalString("1.0.0"), Availability: snapshotMarshalBool(true),
				DocumentSubrevision: snapshotMarshalString("release"),
			},
			{ID: "usecase:no-name", ContextAddress: "d1:[1]", Actor: "HeatPump"},
		},
	}
	return []runtimeGraphObservation{
		{
			RuntimeID: snapshotMarshalRuntimeID(), LocalSKI: snapshotMarshalLocalSKI(), RemoteSKI: snapshotMarshalRemoteA(),
			SessionID: "session:ship-a:1", SessionState: "connected", PairingState: "paired",
			Visible: true, Paired: true, Since: since, ShipID: "ship-a",
			ServiceName: "Name A", ServiceBrand: "Brand A", ServiceModel: "Model A",
			Devices: []runtimeDeviceObservation{device, {ID: "device:no-address", Type: "Generic"}},
		},
		{
			RuntimeID: snapshotMarshalRuntimeID(), LocalSKI: snapshotMarshalLocalSKI(), RemoteSKI: snapshotMarshalRemoteB(),
			PairingState: "unpaired", Since: since, ServiceIdentifier: "id-b", ServiceType: "Type B",
		},
	}
}

func TestRuntimeSnapshotMarshalProducesPinnedBytesForSyntheticGraph(t *testing.T) {
	since := `"2001-02-03T04:04:06Z"`
	now := `"2001-02-03T04:05:06Z"`
	a, b := snapshotMarshalRemoteA(), snapshotMarshalRemoteB()
	want := `{"meta":{"contract":"helianthus.eebus.runtime.raw-snapshot.v1","runtime":` +
		snapshotMarshalRuntimeJSON(snapshotMarshalRuntimeID()) +
		`,"local_ski":"` + snapshotMarshalLocalSKI() + `","mask_tier":"raw","captured_at":` + now + `,"data_timestamp":` + now + `},` +
		`"status":{"state":"ready"},` +
		`"pairing":[{"remote_ski":"` + a + `","state":"paired","since":` + since + `},` +
		`{"remote_ski":"` + b + `","state":"unpaired","since":` + since + `}],` +
		`"services":[{"ski":"` + a + `","ship_id":"ship-a","kind":"remote","visible":true,"paired":true,"name":"Name A","brand":"Brand A","model":"Model A"},` +
		`{"ski":"` + b + `","kind":"remote","visible":false,"paired":false,"identifier":"id-b","type":"Type B"}],` +
		`"sessions":[{"id":"session:ship-a:1","remote_ski":"` + a + `","state":"connected","since":` + since + `}],` +
		`"devices":[{"ski":"` + a + `","ship_id":"ship-a","address":"d1","type":"HeatPump","description":"Device A",` +
		`"metadata":{"label":"L","network_feature_set":"simple"},` +
		`"opaque":[{"path":"/devices/d1/destination_data","source":"spine.detailed-discovery","value":{"n":1.5,"s":"x"}}]}],` +
		`"entities":[{"device_address":"d1","entity_address":"[1]","type":"HeatPumpAppliance","description":"Entity A"}],` +
		`"features":[{"device_address":"d1","entity_address":"[1]","feature_address":"[1]:2","type":"Measurement","role":"server"}],` +
		`"use_cases":[{"context_address":"d1:[1]","name":"useCaseA","actor":"HeatPump","resolved_role":"server","scenarios":["1","2"],` +
		`"version":"1.0.0","availability":true,"document_subrevision":"release"}],` +
		`"opaque":[]}`
	got, err := marshalRuntimeSnapshotWithIdentity(
		snapshotMarshalRuntimeID(), snapshotMarshalLocalSKI(), snapshotMarshalFixtureGraph(),
		snapshotMarshalFixtureTime.In(time.FixedZone("fixture", 3600)),
	)
	if err != nil {
		t.Fatalf("marshal synthetic graph: %v", err)
	}
	if string(got) != want {
		t.Fatalf("snapshot bytes changed:\n got %s\nwant %s", got, want)
	}
	again, err := marshalRuntimeSnapshot(snapshotMarshalFixtureGraph(), snapshotMarshalFixtureTime)
	if err != nil {
		t.Fatalf("marshal synthetic graph from first remote identity: %v", err)
	}
	if string(again) != want {
		t.Fatalf("first-remote identity bytes changed:\n got %s\nwant %s", again, want)
	}
}

func TestRuntimeSnapshotMarshalDerivesStatusFromGraph(t *testing.T) {
	base := func(remote string, mutate func(*runtimeGraphObservation)) runtimeGraphObservation {
		observation := runtimeGraphObservation{
			RuntimeID: snapshotMarshalRuntimeID(), LocalSKI: snapshotMarshalLocalSKI(), RemoteSKI: remote,
			Since: snapshotMarshalFixtureTime,
		}
		mutate(&observation)
		return observation
	}
	visible := func(observation *runtimeGraphObservation) { observation.Visible = true }
	connected := func(observation *runtimeGraphObservation) {
		observation.SessionID, observation.SessionState = "session:a:1", "connected"
	}
	session := func(state string) func(*runtimeGraphObservation) {
		return func(observation *runtimeGraphObservation) {
			observation.SessionID, observation.SessionState, observation.Visible = "session:x:1", state, true
		}
	}
	trust := func(reason string) func(*runtimeGraphObservation) {
		return func(observation *runtimeGraphObservation) {
			connected(observation)
			observation.TrustDegradation = reason
		}
	}
	a, b := snapshotMarshalRemoteA(), snapshotMarshalRemoteB()
	tests := []struct {
		name   string
		graph  []runtimeGraphObservation
		state  string
		reason string
	}{
		{name: "nothing visible", graph: []runtimeGraphObservation{base(a, func(*runtimeGraphObservation) {})}, state: "degraded", reason: "no-visible-services"},
		{name: "visible only", graph: []runtimeGraphObservation{base(a, visible)}, state: "starting"},
		{name: "connected", graph: []runtimeGraphObservation{base(a, connected)}, state: "ready"},
		{name: "disconnected", graph: []runtimeGraphObservation{base(a, session("disconnected"))}, state: "degraded", reason: "remote-disconnect"},
		{name: "session degraded", graph: []runtimeGraphObservation{base(a, session("degraded"))}, state: "degraded", reason: "remote-disconnect"},
		{name: "connected wins over disconnected", graph: []runtimeGraphObservation{base(a, session("disconnected")), base(b, connected)}, state: "ready"},
		{name: "certificate unavailable", graph: []runtimeGraphObservation{base(a, trust("certificate-unavailable"))}, state: "degraded", reason: "certificate-unavailable"},
		{name: "denied trust after certificate", graph: []runtimeGraphObservation{base(a, trust("certificate-unavailable")), base(b, trust("denied-trust"))}, state: "degraded", reason: "denied-trust"},
		{name: "certificate after denied trust", graph: []runtimeGraphObservation{base(a, trust("denied-trust")), base(b, trust("certificate-unavailable"))}, state: "degraded", reason: "denied-trust"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload, err := marshalRuntimeSnapshotWithIdentity(snapshotMarshalRuntimeID(), snapshotMarshalLocalSKI(), test.graph, snapshotMarshalFixtureTime)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var decoded runtimeSnapshotPayload
			if err := json.Unmarshal(payload, &decoded); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if decoded.Status.State != test.state {
				t.Fatalf("status = %q, want %q", decoded.Status.State, test.state)
			}
			reason := ""
			if decoded.Status.Degradation != nil {
				reason = decoded.Status.Degradation.Reason
				if !decoded.Status.Degradation.Since.Equal(snapshotMarshalFixtureTime) {
					t.Fatalf("degradation since = %s, want capture time", decoded.Status.Degradation.Since)
				}
			}
			if reason != test.reason {
				t.Fatalf("degradation reason = %q, want %q", reason, test.reason)
			}
		})
	}
}

func TestRuntimeSnapshotMarshalRejectsMissingIdentity(t *testing.T) {
	if _, err := marshalRuntimeSnapshot(nil, snapshotMarshalFixtureTime); err == nil {
		t.Fatal("empty graph marshalled without a runtime identity")
	}
	if _, err := marshalRuntimeSnapshotWithIdentity(" ", snapshotMarshalLocalSKI(), nil, snapshotMarshalFixtureTime); err == nil {
		t.Fatal("blank runtime identity was accepted")
	}
	payload, err := marshalRuntimeSnapshotWithIdentity(snapshotMarshalRuntimeID(), snapshotMarshalLocalSKI(), nil, snapshotMarshalFixtureTime)
	if err != nil {
		t.Fatalf("empty graph with explicit identity: %v", err)
	}
	for _, field := range []string{`"pairing":[]`, `"services":[]`, `"sessions":[]`, `"devices":[]`, `"entities":[]`, `"features":[]`, `"use_cases":[]`, `"opaque":[]`} {
		if !strings.Contains(string(payload), field) {
			t.Fatalf("empty graph payload lacks %s: %s", field, payload)
		}
	}
}

func TestRuntimeSnapshotMarshalDeviceSkipsIncompleteRecordsAndDetachesValues(t *testing.T) {
	for name, device := range map[string]runtimeDeviceObservation{
		"missing ski":     {Address: "d1", Type: "HeatPump"},
		"missing address": {SKI: snapshotMarshalRemoteA(), Type: "HeatPump"},
		"missing type":    {SKI: snapshotMarshalRemoteA(), Address: "d1"},
	} {
		payload, entities, features, useCases := marshalRuntimeDevice(device)
		if payload.Address != "" || entities != nil || features != nil || useCases != nil {
			t.Fatalf("%s: incomplete device produced output", name)
		}
	}
	source := snapshotMarshalFixtureGraph()[0].Devices[0]
	source.SKI = snapshotMarshalRemoteA()
	payload, entities, features, useCases := marshalRuntimeDevice(source)
	if payload.SHIPID != nil || len(entities) != 1 || len(features) != 1 || len(useCases) != 1 {
		t.Fatalf("device projection = %+v entities=%d features=%d use cases=%d", payload, len(entities), len(features), len(useCases))
	}
	source.Metadata["label"] = "changed"
	*source.Description = "changed"
	source.UseCases[0].Scenarios[0] = "changed"
	if (*payload.Metadata)["label"] != "L" || *payload.Description != "Device A" || useCases[0].Scenarios[0] != "1" {
		t.Fatal("device projection aliases its source observation")
	}
	opaque := (*payload.Opaque)[0].Value.(map[string]any)
	if value, ok := opaque["n"].(float64); !ok || value != 1.5 {
		t.Fatalf("opaque number = %#v, want float64 1.5", opaque["n"])
	}
	if runtimeOptionalString("") != nil || *runtimeOptionalString("x") != "x" {
		t.Fatal("optional string projection changed")
	}
}
