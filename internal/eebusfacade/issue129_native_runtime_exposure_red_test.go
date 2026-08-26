package eebusfacade

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestIssue129RuntimeSnapshotKeepsNativeRuntimeIdentityWithoutMaskTier(t *testing.T) {
	const (
		runtimeID = "native-runtime-id"
		localSKI  = "3333333333333333333333333333333333333333"
	)
	payload, err := marshalNativeRuntimeSnapshotV2WithIdentity(runtimeID, localSKI, nil, time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatal(err)
	}
	meta, ok := document["meta"].(map[string]any)
	if !ok {
		t.Fatalf("meta = %#v, want object", document["meta"])
	}
	if got, ok := meta["runtime"].(string); !ok || got != runtimeID {
		t.Fatalf("runtime = %#v, want native %q", meta["runtime"], runtimeID)
	}
	if got := meta["contract"]; got != "helianthus.eebus.runtime.native-snapshot.v2" {
		t.Fatalf("contract = %#v, want native snapshot v2", got)
	}
	if _, found := meta["mask_tier"]; found {
		t.Fatalf("runtime snapshot retains retired mask_tier: %#v", meta)
	}
}

func TestIssue129RuntimeSnapshotPublishesDetachedFloatNativeValue(t *testing.T) {
	const (
		runtimeID = "native-runtime-id"
		localSKI  = "3333333333333333333333333333333333333333"
		remoteSKI = "4444444444444444444444444444444444444444"
	)
	now := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	payload, err := marshalNativeRuntimeSnapshotV2WithIdentity(runtimeID, localSKI, []runtimeGraphObservation{{
		RemoteSKI: remoteSKI,
		Visible:   true,
		Devices: []runtimeDeviceObservation{{
			SKI: remoteSKI, Address: "1", Type: "device",
			Opaque: []runtimeOpaquePayload{{Path: "/device/1/value", Source: "spine", Value: float64(12.5)}},
		}},
	}}, now)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatal(err)
	}
	devices, ok := document["devices"].([]any)
	if !ok || len(devices) != 1 {
		t.Fatalf("devices = %#v, want one native device", document["devices"])
	}
	device, ok := devices[0].(map[string]any)
	if !ok {
		t.Fatalf("device = %#v, want object", devices[0])
	}
	observations, ok := device["observations"].([]any)
	if !ok || len(observations) != 1 {
		t.Fatalf("observations = %#v, want one native observation", device["observations"])
	}
	observation, ok := observations[0].(map[string]any)
	if !ok {
		t.Fatalf("observation = %#v, want object", observations[0])
	}
	value, ok := observation["value"].(map[string]any)
	if !ok {
		t.Fatalf("value = %#v, want object", observation["value"])
	}
	if got, ok := value["float"].(float64); !ok || got != 12.5 {
		t.Fatalf("native float = %#v, want 12.5", value["float"])
	}
	if _, found := value["integer"]; found {
		t.Fatalf("native float was rewritten as integer: %#v", value)
	}
}

func TestIssue129RuntimeDevicesPreserveDetachedNativeNumberTokensInV2(t *testing.T) {
	devices, err := runtimeDevicesForRemoteDevice(issue77DetailedVR940(t), issue77ReducerRemoteSKI)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 {
		t.Fatalf("devices = %#v, want one runtime device", devices)
	}
	detached, err := detachedRuntimeJSONValue(map[string]any{
		"counter":  int64(9007199254740993),
		"fraction": 12.5,
	})
	if err != nil {
		t.Fatal(err)
	}
	devices[0].Opaque = append(devices[0].Opaque, runtimeOpaquePayload{
		Path: "/devices/d:_n:Vaillant_VR940/native_numbers", Source: "spine.detailed-discovery", Value: detached,
	})
	payload, err := marshalNativeRuntimeSnapshotV2WithIdentity("native-runtime-id", issue77ReducerRemoteSKI, []runtimeGraphObservation{{
		RemoteSKI: issue77ReducerRemoteSKI, Visible: true, Devices: devices,
	}}, time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		t.Fatal(err)
	}
	values := issue129NativeNumberObject(t, document)
	if got := issue129NativeNumber(t, values, "counter", "integer"); got != "9007199254740993" {
		t.Fatalf("counter = %q, want exact native integer", got)
	}
	if got := issue129NativeNumber(t, values, "fraction", "float"); got != "12.5" {
		t.Fatalf("fraction = %q, want native fraction", got)
	}
}

func issue129NativeNumberObject(t *testing.T, document map[string]any) map[string]any {
	t.Helper()
	devices, ok := document["devices"].([]any)
	if !ok || len(devices) != 1 {
		t.Fatalf("devices = %#v, want one native device", document["devices"])
	}
	device, ok := devices[0].(map[string]any)
	if !ok {
		t.Fatalf("device = %#v, want object", devices[0])
	}
	observations, ok := device["observations"].([]any)
	if !ok {
		t.Fatalf("observations = %#v, want objects", device["observations"])
	}
	for _, observation := range observations {
		entry, ok := observation.(map[string]any)
		if !ok || entry["path"] != "/devices/d:_n:Vaillant_VR940/native_numbers" {
			continue
		}
		value, ok := entry["value"].(map[string]any)
		if !ok {
			t.Fatalf("value = %#v, want object", entry["value"])
		}
		object, ok := value["object"].(map[string]any)
		if !ok {
			t.Fatalf("object = %#v, want native object", value["object"])
		}
		return object
	}
	t.Fatalf("native number observation missing: %#v", observations)
	return nil
}

func issue129NativeNumber(t *testing.T, values map[string]any, key, kind string) string {
	t.Helper()
	value, ok := values[key].(map[string]any)
	if !ok {
		t.Fatalf("%s = %#v, want native value", key, values[key])
	}
	number, ok := value[kind].(json.Number)
	if !ok {
		t.Fatalf("%s %s = %#v, want native number", key, kind, value[kind])
	}
	return number.String()
}
