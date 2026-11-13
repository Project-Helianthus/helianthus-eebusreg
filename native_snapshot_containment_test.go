package eebusruntime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

const (
	nativeContainmentDevice = "d:_n:synthetic_controller"
	nativeContainmentPeer   = "d:_n:synthetic_peer"
)

func nativeContainmentVector(t *testing.T) map[string]any {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("testdata", "native_containment", "snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func nativeContainmentCopy(t *testing.T, document map[string]any) map[string]any {
	t.Helper()
	payload, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func nativeContainmentBuild(t *testing.T, document map[string]any) (NativeSnapshotV2, error) {
	t.Helper()
	payload, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	var draft NativeSnapshotV2
	if err := json.Unmarshal(payload, &draft); err != nil {
		t.Fatal(err)
	}
	return NewNativeSnapshotV2(draft)
}

func nativeContainmentDocument(t *testing.T, snapshot NativeSnapshotV2) map[string]any {
	t.Helper()
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func nativeContainmentItems(document map[string]any, key string) []any {
	items, _ := document[key].([]any)
	return items
}

func nativeContainmentItem(document map[string]any, key string, index int) map[string]any {
	return nativeContainmentItems(document, key)[index].(map[string]any)
}

func nativeContainmentAppend(document map[string]any, key string, value any) {
	document[key] = append(nativeContainmentItems(document, key), value)
}

func nativeContainmentClone(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, child := range typed {
			result[key] = nativeContainmentClone(child)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, child := range typed {
			result[index] = nativeContainmentClone(child)
		}
		return result
	default:
		return value
	}
}

func nativeContainmentAppendCopy(document map[string]any, key string, index int) map[string]any {
	copied := nativeContainmentClone(nativeContainmentItem(document, key, index)).(map[string]any)
	nativeContainmentAppend(document, key, copied)
	return copied
}

func nativeContainmentAddress(entity string) string {
	return nativeContainmentDevice + ":" + entity
}

func TestNativeSnapshotContainmentVectors(t *testing.T) {
	vector := nativeContainmentVector(t)
	snapshot, err := nativeContainmentBuild(t, vector)
	if err != nil {
		t.Fatalf("vector rejected: %v", err)
	}
	if got := nativeContainmentDocument(t, snapshot); !reflect.DeepEqual(got, vector) {
		t.Errorf("vector did not survive the native snapshot:\n got %#v\nwant %#v", got, vector)
	}
	nested := nativeContainmentItem(vector, "entities", 0)
	if nested["parent_entity_address"] != nativeContainmentAddress("[1]:") {
		t.Fatalf("nested entity parent = %#v", nested["parent_entity_address"])
	}
	if _, found := nativeContainmentItem(vector, "entities", 1)["parent_entity_address"]; found {
		t.Fatal("top-level entity carries a parent")
	}
	entityClaim := nativeContainmentItem(vector, "use_cases", 0)
	featureClaim := nativeContainmentItem(vector, "use_cases", 1)
	deviceClaim := nativeContainmentItem(vector, "use_cases", 2)
	if entityClaim["entity_address"] != nativeContainmentAddress("[1]:") || entityClaim["feature_address"] != nil {
		t.Fatalf("entity-scoped claim = %#v", entityClaim)
	}
	if featureClaim["feature_address"] != nativeContainmentAddress("[1]:0") || featureClaim["entity_address"] != nativeContainmentAddress("[1]:") {
		t.Fatalf("feature-scoped claim = %#v", featureClaim)
	}
	if deviceClaim["device_address"] != nativeContainmentDevice || deviceClaim["entity_address"] != nil || deviceClaim["feature_address"] != nil {
		t.Fatalf("device-scoped claim = %#v", deviceClaim)
	}
	if absences := nativeContainmentItems(vector, "absences"); len(absences) != 7 || vector["absences_omitted"] != float64(0) {
		t.Fatalf("absences = %#v omitted %#v", absences, vector["absences_omitted"])
	}
	for _, tc := range nativeContainmentMutations() {
		t.Run(tc.name, func(t *testing.T) {
			document := nativeContainmentCopy(t, vector)
			tc.mutate(document)
			_, err := nativeContainmentBuild(t, document)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

type nativeContainmentMutation struct {
	name   string
	want   string
	mutate func(map[string]any)
}

func nativeContainmentMutations() []nativeContainmentMutation {
	return []nativeContainmentMutation{
		{"missing device", "entity references a missing device", func(d map[string]any) {
			nativeContainmentItem(d, "entities", 0)["device_address"] = nativeContainmentPeer
		}},
		{"duplicate device address", "device address is ambiguous", func(d map[string]any) {
			nativeContainmentAppendCopy(d, "devices", 0)["ski"] = "7777777777777777777777777777777777777777"
		}},
		{"duplicate entity", "duplicate entity", func(d map[string]any) {
			nativeContainmentAppendCopy(d, "entities", 1)
		}},
		{"missing parent", "entity references a missing parent entity", func(d map[string]any) {
			nativeContainmentItem(d, "entities", 0)["parent_entity_address"] = nativeContainmentAddress("[9]:")
		}},
		{"self parent", "entity references a missing parent entity", func(d map[string]any) {
			nativeContainmentItem(d, "entities", 1)["parent_entity_address"] = nativeContainmentAddress("[1]:")
		}},
		{"cycle through two entities", "entity containment has a cycle", func(d map[string]any) {
			nativeContainmentItem(d, "entities", 1)["parent_entity_address"] = nativeContainmentAddress("[1,1]:")
		}},
		{"missing entity of a feature", "feature references a missing entity", func(d map[string]any) {
			nativeContainmentItem(d, "features", 0)["entity_address"] = nativeContainmentAddress("[9]:")
		}},
		{"duplicate feature", "duplicate feature", func(d map[string]any) {
			nativeContainmentAppendCopy(d, "features", 1)
		}},
		{"claim of a missing device", "use-case claim references a missing scope", func(d map[string]any) {
			nativeContainmentItem(d, "use_cases", 2)["device_address"] = nativeContainmentPeer
		}},
		{"claim of a missing entity", "use-case claim references a missing scope", func(d map[string]any) {
			nativeContainmentItem(d, "use_cases", 0)["entity_address"] = nativeContainmentAddress("[9]:")
		}},
		{"claim of a missing feature", "use-case claim references a missing scope", func(d map[string]any) {
			nativeContainmentItem(d, "use_cases", 1)["feature_address"] = nativeContainmentAddress("[1]:9")
		}},
		{"feature scope without entity scope", "use-case claim references a missing scope", func(d map[string]any) {
			delete(nativeContainmentItem(d, "use_cases", 1), "entity_address")
		}},
		{"duplicate claim", "duplicate use-case claim", func(d map[string]any) {
			nativeContainmentAppendCopy(d, "use_cases", 2)
		}},
	}
}

// TestNativeSnapshotContainmentIdentityAxes varies one key field at a time. Each
// distinct pair must be accepted, which catches a uniqueness key that omits the
// field: entity (device_address, entity_address), feature (device_address,
// entity_address, feature_address) and use-case claim (device_address,
// entity_address, feature_address, name, actor, version, document_subrevision).
func TestNativeSnapshotContainmentIdentityAxes(t *testing.T) {
	vector := nativeContainmentVector(t)
	peerDevice := func(d map[string]any) {
		nativeContainmentAppendCopy(d, "devices", 0)["address"] = nativeContainmentPeer
		nativeContainmentAppendCopy(d, "entities", 1)["device_address"] = nativeContainmentPeer
	}
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"entity device_address", peerDevice},
		{"entity entity_address", func(d map[string]any) {
			nativeContainmentAppendCopy(d, "entities", 1)["entity_address"] = nativeContainmentAddress("[3]:")
		}},
		{"feature device_address", func(d map[string]any) {
			peerDevice(d)
			nativeContainmentAppendCopy(d, "features", 1)["device_address"] = nativeContainmentPeer
		}},
		{"feature entity_address", func(d map[string]any) {
			nativeContainmentAppendCopy(d, "features", 0)["entity_address"] = nativeContainmentAddress("[1]:")
		}},
		{"feature feature_address", func(d map[string]any) {
			nativeContainmentAppendCopy(d, "features", 1)["feature_address"] = nativeContainmentAddress("[1]:5")
		}},
		{"claim device_address", func(d map[string]any) {
			peerDevice(d)
			nativeContainmentAppendCopy(d, "use_cases", 2)["device_address"] = nativeContainmentPeer
		}},
		{"claim entity_address", func(d map[string]any) {
			nativeContainmentAppendCopy(d, "use_cases", 0)["entity_address"] = nativeContainmentAddress("[1,1]:")
		}},
		{"claim feature_address", func(d map[string]any) {
			nativeContainmentAppendCopy(d, "use_cases", 0)["feature_address"] = nativeContainmentAddress("[1]:0")
		}},
		{"claim name", func(d map[string]any) {
			nativeContainmentAppendCopy(d, "use_cases", 2)["name"] = "monitoringOfOutdoorTemperature"
		}},
		{"claim actor", func(d map[string]any) {
			nativeContainmentAppendCopy(d, "use_cases", 2)["actor"] = "HeatingZone"
		}},
		{"claim version", func(d map[string]any) {
			nativeContainmentAppendCopy(d, "use_cases", 2)["version"] = "1.0.1"
		}},
		{"claim absent version", func(d map[string]any) {
			delete(nativeContainmentAppendCopy(d, "use_cases", 2), "version")
		}},
		{"claim document_subrevision", func(d map[string]any) {
			nativeContainmentAppendCopy(d, "use_cases", 2)["document_subrevision"] = "draft"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			document := nativeContainmentCopy(t, vector)
			tc.mutate(document)
			if _, err := nativeContainmentBuild(t, document); err != nil {
				t.Fatalf("distinct identities collided: %v", err)
			}
		})
	}
}

func nativeContainmentAbsences(count int) []any {
	result := make([]any, count)
	for index := range result {
		result[index] = map[string]any{
			"kind": "feature", "reason": "missing_type", "device_address": nativeContainmentDevice,
			"entity_address": nativeContainmentAddress("[1]:"), "feature_address": nativeContainmentAddress(fmt.Sprintf("[1]:%d", index+10)),
		}
	}
	return result
}

func TestNativeSnapshotAbsenceBounds(t *testing.T) {
	vector := nativeContainmentVector(t)
	for _, tc := range []struct {
		name   string
		want   string
		mutate func(map[string]any)
	}{
		{"unknown kind", "absence record is invalid", func(d map[string]any) {
			nativeContainmentItem(d, "absences", 0)["kind"] = "node"
		}},
		{"unknown reason", "absence record is invalid", func(d map[string]any) {
			nativeContainmentItem(d, "absences", 0)["reason"] = "missing_everything"
		}},
		{"blank device address", "absence record is invalid", func(d map[string]any) {
			nativeContainmentItem(d, "absences", 0)["device_address"] = " "
		}},
		{"blank entity address", "absence record is invalid", func(d map[string]any) {
			nativeContainmentItem(d, "absences", 0)["entity_address"] = ""
		}},
		{"blank feature address", "absence record is invalid", func(d map[string]any) {
			nativeContainmentItem(d, "absences", 2)["feature_address"] = "\t"
		}},
		{"more than 1024 records", "absence records exceed the limit", func(d map[string]any) {
			d["absences"] = nativeContainmentAbsences(1025)
		}},
		{"omitted count below the limit", "absence count is inconsistent", func(d map[string]any) {
			d["absences_omitted"] = 1
		}},
		{"omitted count at 1023 records", "absence count is inconsistent", func(d map[string]any) {
			d["absences"] = nativeContainmentAbsences(1023)
			d["absences_omitted"] = 1
		}},
		{"accepted at the limit", "", func(d map[string]any) {
			d["absences"] = nativeContainmentAbsences(1024)
		}},
		{"accepted with an omitted count at the limit", "", func(d map[string]any) {
			d["absences"] = nativeContainmentAbsences(1024)
			d["absences_omitted"] = 5
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			document := nativeContainmentCopy(t, vector)
			tc.mutate(document)
			_, err := nativeContainmentBuild(t, document)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("valid absence records rejected: %v", err)
				}
				return
			}
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestNativeSnapshotAbsenceDefaultsToEmptyList(t *testing.T) {
	document := nativeContainmentVector(t)
	delete(document, "absences")
	delete(document, "absences_omitted")
	snapshot, err := nativeContainmentBuild(t, document)
	if err != nil {
		t.Fatal(err)
	}
	encoded := nativeContainmentDocument(t, snapshot)
	if absences, ok := encoded["absences"].([]any); !ok || len(absences) != 0 {
		t.Fatalf("absences = %#v, want []", encoded["absences"])
	}
	if encoded["absences_omitted"] != float64(0) {
		t.Fatalf("absences_omitted = %#v, want 0", encoded["absences_omitted"])
	}
}

func TestNativeSnapshotContainmentRoundTripAndClone(t *testing.T) {
	snapshot, err := nativeContainmentBuild(t, nativeContainmentVector(t))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var decoded NativeSnapshotV2
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	again, err := NewNativeSnapshotV2(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, snapshot) {
		t.Fatalf("round trip changed the snapshot:\n got %#v\nwant %#v", again, snapshot)
	}
	clone := snapshot.Clone()
	if !reflect.DeepEqual(clone, snapshot) {
		t.Fatal("clone differs from its source")
	}
	if shared := nativeContainmentShared(reflect.ValueOf(snapshot), reflect.ValueOf(clone), "snapshot"); shared != "" {
		t.Fatalf("clone shares %s with its source", shared)
	}
}

// nativeContainmentShared walks two equal values in parallel and names the first
// pointer, slice or map that both reference.
func nativeContainmentShared(left, right reflect.Value, path string) string {
	if left.Type() == reflect.TypeOf(time.Time{}) {
		return ""
	}
	switch left.Kind() {
	case reflect.Pointer:
		if left.IsNil() || right.IsNil() {
			return ""
		}
		if left.Pointer() == right.Pointer() {
			return path
		}
		return nativeContainmentShared(left.Elem(), right.Elem(), path)
	case reflect.Slice:
		if left.Len() > 0 && right.Len() > 0 && left.Pointer() == right.Pointer() {
			return path
		}
		for index := 0; index < left.Len() && index < right.Len(); index++ {
			if shared := nativeContainmentShared(left.Index(index), right.Index(index), fmt.Sprintf("%s[%d]", path, index)); shared != "" {
				return shared
			}
		}
	case reflect.Map:
		if !left.IsNil() && !right.IsNil() && left.Pointer() == right.Pointer() {
			return path
		}
		for _, key := range left.MapKeys() {
			if other := right.MapIndex(key); other.IsValid() {
				if shared := nativeContainmentShared(left.MapIndex(key), other, fmt.Sprintf("%s[%v]", path, key)); shared != "" {
					return shared
				}
			}
		}
	case reflect.Struct:
		for index := 0; index < left.NumField(); index++ {
			if shared := nativeContainmentShared(left.Field(index), right.Field(index), path+"."+left.Type().Field(index).Name); shared != "" {
				return shared
			}
		}
	}
	return ""
}
