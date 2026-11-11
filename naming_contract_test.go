package eebusruntime

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

func TestNamingContractSnapshotKeys(t *testing.T) {
	raw := issue77Snapshot(t)
	redacted, err := BuildRedactedSnapshotV1(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		value any
		empty func() any
	}{
		{"raw", raw, func() any { return new(SnapshotV1) }},
		{"redacted", redacted, func() any { return new(RedactedSnapshotV1) }},
		{"native", NativeSnapshotV2{UseCases: []NativeUseCaseV2{{}}}, func() any { return new(NativeSnapshotV2) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			var object map[string]json.RawMessage
			if err := json.Unmarshal(b, &object); err != nil {
				t.Fatal(err)
			}
			if _, ok := object["use_cases"]; !ok {
				t.Fatal("missing use_cases")
			}
			if _, ok := object["usecases"]; ok {
				t.Fatal("old key emitted")
			}
			object["usecases"] = object["use_cases"]
			delete(object, "use_cases")
			b, err = json.Marshal(object)
			if err != nil {
				t.Fatal(err)
			}
			decoder := json.NewDecoder(bytes.NewReader(b))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(tc.empty()); err == nil {
				t.Fatal("old alias admitted")
			}
		})
	}
}

func TestNamingContractOpaqueNativeKeys(t *testing.T) {
	document := issue77FixtureDocument(t)
	value := map[string]any{"usecases": "nativeValue", "makerFeature": "green iQ", "scale": json.Number("-2"), "unit": "W"}
	document["opaque"] = []any{map[string]any{"path": "/native/object", "source": "spine", "value": value}}
	document["meta"].(map[string]any)["data_hash"] = ""
	raw := issue77ConstructDocument(t, document)
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	if err := decoder.Decode(&got); err != nil {
		t.Fatal(err)
	}
	observation := got["opaque"].([]any)[0].(map[string]any)
	if observation["path"] != "/native/object" || observation["source"] != "spine" || !reflect.DeepEqual(observation["value"], value) {
		t.Fatal("opaque native evidence changed")
	}
}

func TestNamingContractUseCaseIdentityAxes(t *testing.T) {
	version, revision := "v1", "r1"
	base := UseCaseV1{ContextAddress: "context", Name: "nativeName", Actor: "actor", Version: &version, DocumentSubrevision: &revision}
	if err := validateUseCasesV1([]UseCaseV1{base, base}); err == nil {
		t.Fatal("duplicate identity admitted")
	}
	for _, tc := range []struct {
		name   string
		change func(*UseCaseV1)
	}{
		{"context_address", func(v *UseCaseV1) { v.ContextAddress = "other-context" }},
		{"name", func(v *UseCaseV1) { v.Name = "otherName" }},
		{"actor", func(v *UseCaseV1) { v.Actor = "other-actor" }},
		{"version", func(v *UseCaseV1) { s := "v2"; v.Version = &s }},
		{"document_subrevision", func(v *UseCaseV1) { s := "r2"; v.DocumentSubrevision = &s }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := base
			tc.change(&changed)
			if useCaseIdentityV1(base) == useCaseIdentityV1(changed) {
				t.Fatal("identity axis omitted")
			}
			if err := validateUseCasesV1([]UseCaseV1{base, changed}); err != nil {
				t.Fatal("distinct native identities collided")
			}
		})
	}
}
