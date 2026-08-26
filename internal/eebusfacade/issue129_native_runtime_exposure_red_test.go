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
	if got := issue129NativeNumber(t, values, "counter", "number"); got != "9007199254740993" {
		t.Fatalf("counter = %q, want exact native integer", got)
	}
	if got := issue129NativeNumber(t, values, "fraction", "number"); got != "12.5" {
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
	if kind == "" {
		switch number := values[key].(type) {
		case json.Number:
			return number.String()
		case string:
			return number
		default:
			t.Fatalf("%s = %#v, want native number", key, values[key])
		}
	}
	value, ok := values[key].(map[string]any)
	if !ok {
		t.Fatalf("%s = %#v, want native value", key, values[key])
	}
	switch number := value[kind].(type) {
	case json.Number:
		return number.String()
	case string:
		return number
	default:
		t.Fatalf("%s %s = %#v, want native number", key, kind, value[kind])
	}
	return ""
}

func TestIssue129RuntimeV2PreservesWideJSONNumbersAndEmptyContainers(t *testing.T) {
	value, err := detachedRuntimeJSONValue(map[string]any{
		"wide":   json.Number("18446744073709551615"),
		"minus":  json.Number("-0"),
		"array":  []any{},
		"object": map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := marshalNativeRuntimeSnapshotV2WithIdentity("native-runtime-id", issue77ReducerRemoteSKI, []runtimeGraphObservation{{
		RemoteSKI: issue77ReducerRemoteSKI,
		Visible:   true,
		Devices: []runtimeDeviceObservation{{
			SKI: issue77ReducerRemoteSKI, Address: "1", Type: "device",
			Opaque: []runtimeOpaquePayload{{Path: "/devices/1/native", Source: "spine", Value: value}},
		}},
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
	values := issue129NativeObject(t, document, "/devices/1/native")
	if got := issue129NativeNumber(t, values, "wide", "number"); got != "18446744073709551615" {
		t.Fatalf("wide number = %q, want exact native token", got)
	}
	if got := issue129NativeNumber(t, values, "minus", "number"); got != "-0" {
		t.Fatalf("negative zero = %q, want exact native token", got)
	}
	if array, ok := values["array"].(map[string]any)["array"].([]any); !ok || len(array) != 0 {
		t.Fatalf("empty native array = %#v, want []", values["array"])
	}
	if object, ok := values["object"].(map[string]any)["object"].(map[string]any); !ok || len(object) != 0 {
		t.Fatalf("empty native object = %#v, want {}", values["object"])
	}
}

func TestIssue129V1NormalizesAndV2PreservesDetachedNativeNumberTokens(t *testing.T) {
	value, err := detachedRuntimeJSONValue(map[string]any{
		"large":    json.Number("9007199254740993"),
		"minus":    json.Number("-0"),
		"fraction": json.Number("12.5"),
		"exponent": json.Number("1e+3"),
	})
	if err != nil {
		t.Fatal(err)
	}
	graph := []runtimeGraphObservation{{
		RemoteSKI: issue77ReducerRemoteSKI,
		Visible:   true,
		Devices: []runtimeDeviceObservation{{
			SKI: issue77ReducerRemoteSKI, Address: "1", Type: "device",
			Opaque: []runtimeOpaquePayload{{Path: "/devices/1/native", Source: "spine", Value: value}},
		}},
	}}
	now := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	v1, err := marshalRuntimeSnapshotWithIdentity("runtime", issue77ReducerRemoteSKI, graph, now)
	if err != nil {
		t.Fatal(err)
	}
	v2, err := marshalNativeRuntimeSnapshotV2WithIdentity("runtime", issue77ReducerRemoteSKI, graph, now)
	if err != nil {
		t.Fatal(err)
	}
	v1Values := issue129V1NativeObject(t, v1)
	if got := issue129NativeNumber(t, v1Values, "large", ""); got != "9007199254740992" {
		t.Fatalf("V1 large number = %q, want frozen float64 normalization", got)
	}
	if got := issue129NativeNumber(t, v1Values, "exponent", ""); got != "1000" {
		t.Fatalf("V1 exponent = %q, want frozen float64 normalization", got)
	}
	decoder := json.NewDecoder(bytes.NewReader(v2))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		t.Fatal(err)
	}
	v2Values := issue129NativeObject(t, document, "/devices/1/native")
	for key, want := range map[string]string{"large": "9007199254740993", "minus": "-0", "fraction": "12.5", "exponent": "1e+3"} {
		if got := issue129NativeNumber(t, v2Values, key, "number"); got != want {
			t.Fatalf("V2 %s = %q, want exact native token %q", key, got, want)
		}
	}
}

func issue129V1NativeObject(t *testing.T, payload []byte) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		t.Fatal(err)
	}
	devices, ok := document["devices"].([]any)
	if !ok || len(devices) != 1 {
		t.Fatalf("V1 devices = %#v, want one", document["devices"])
	}
	opaque, ok := devices[0].(map[string]any)["opaque"].([]any)
	if !ok || len(opaque) != 1 {
		t.Fatalf("V1 opaque = %#v, want one", devices[0])
	}
	value, ok := opaque[0].(map[string]any)["value"].(map[string]any)
	if !ok {
		t.Fatalf("V1 opaque value = %#v, want object", opaque[0])
	}
	return value
}

func issue129NativeObject(t *testing.T, document map[string]any, path string) map[string]any {
	t.Helper()
	devices, ok := document["devices"].([]any)
	if !ok || len(devices) != 1 {
		t.Fatalf("devices = %#v, want one native device", document["devices"])
	}
	observations, ok := devices[0].(map[string]any)["observations"].([]any)
	if !ok {
		t.Fatalf("observations = %#v, want array", devices[0])
	}
	for _, observation := range observations {
		entry, ok := observation.(map[string]any)
		if !ok || entry["path"] != path {
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
	t.Fatalf("native observation %q missing: %#v", path, observations)
	return nil
}
