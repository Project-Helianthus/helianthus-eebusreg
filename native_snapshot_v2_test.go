package eebusruntime

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNativeSnapshotV2PreservesNativeRuntimeAndRawPayload(t *testing.T) {
	observedAt := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	identifier := "vr940f-lab-service"
	snapshot, err := NewNativeSnapshotV2(NativeSnapshotV2{
		Meta: NativeSnapshotMetaV2{
			Contract: NativeSnapshotContractV2, Runtime: "synthetic-runtime-id",
			LocalSKI: "3333333333333333333333333333333333333333", Source: "synthetic",
			ObservedAt: observedAt, CapturedAt: observedAt, DataTimestamp: observedAt,
		},
		Status: NativeRuntimeObservationV2{State: "ready"},
		Services: []NativeServiceV2{{
			SKI: "4444444444444444444444444444444444444444", Kind: "remote", Identifier: &identifier,
		}},
		Observations: []NativeObservationV2{{
			Path: "device.native", Source: "synthetic", ObservedAt: observedAt,
			Value: NativeValueV2{Object: nativeSnapshotV2ObjectPointer(map[string]NativeValueV2{
				"register": {Integer: nativeSnapshotV2IntegerPointer(17)},
				"scaled":   {Float: nativeSnapshotV2FloatPointer(12.5)},
			})},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatal(err)
	}
	meta := document["meta"].(map[string]any)
	if got := meta["runtime"]; got != "synthetic-runtime-id" {
		t.Fatalf("runtime = %#v, want native runtime identity", got)
	}
	if _, found := meta["mask_tier"]; found {
		t.Fatalf("native snapshot retained retired mask_tier: %#v", meta)
	}
	if got := document["services"].([]any)[0].(map[string]any)["identifier"]; got != "vr940f-lab-service" {
		t.Fatalf("native service identifier = %#v, want raw synthetic identifier", got)
	}
	observation := snapshot.Observations[0]
	if got := *(*observation.Value.Object)["register"].Integer; got != 17 {
		t.Fatalf("native observation = %#v, want direct bounded value", got)
	}
	if got := *(*observation.Value.Object)["scaled"].Float; got != 12.5 {
		t.Fatalf("native float observation = %#v, want direct native value", got)
	}
}

func nativeSnapshotV2IntegerPointer(value int64) *int64   { return &value }
func nativeSnapshotV2FloatPointer(value float64) *float64 { return &value }
func nativeSnapshotV2ObjectPointer(value map[string]NativeValueV2) *map[string]NativeValueV2 {
	return &value
}

func TestNativeSnapshotV2RoundTripPreservesEmptyNativeContainers(t *testing.T) {
	observedAt := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	array := []NativeValueV2{}
	object := map[string]NativeValueV2{}
	draft := NativeSnapshotV2{
		Meta:   NativeSnapshotMetaV2{Contract: NativeSnapshotContractV2, Runtime: "synthetic-runtime-id", LocalSKI: "3333333333333333333333333333333333333333", Source: "synthetic", ObservedAt: observedAt, CapturedAt: observedAt, DataTimestamp: observedAt},
		Status: NativeRuntimeObservationV2{State: "ready"},
		Observations: []NativeObservationV2{
			{Path: "empty.array", Source: "synthetic", ObservedAt: observedAt, Value: NativeValueV2{Array: &array}},
			{Path: "empty.object", Source: "synthetic", ObservedAt: observedAt, Value: NativeValueV2{Object: &object}},
		},
	}
	payload, err := json.Marshal(draft)
	if err != nil {
		t.Fatal(err)
	}
	var decoded NativeSnapshotV2
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	snapshot, err := NewNativeSnapshotV2(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Observations[0].Value.Array == nil || len(*snapshot.Observations[0].Value.Array) != 0 {
		t.Fatalf("empty array did not survive round trip: %#v", snapshot.Observations[0].Value)
	}
	if snapshot.Observations[1].Value.Object == nil || len(*snapshot.Observations[1].Value.Object) != 0 {
		t.Fatalf("empty object did not survive round trip: %#v", snapshot.Observations[1].Value)
	}
}
