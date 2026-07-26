package eebusruntime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

const issue77FixtureHash = "sha256:a8c602ea1227d207557a97f9958222a416e583061863cfcaa0133a8be4740d47"

func TestIssue77InitialV1PublicValueSurfaceIsExact(t *testing.T) {
	for _, check := range []struct {
		value  any
		fields []string
	}{
		{SnapshotV1{}, []string{"Meta:meta", "Status:status", "Pairing:pairing", "Services:services", "Sessions:sessions", "Devices:devices", "Entities:entities", "Features:features", "UseCases:usecases", "Opaque:opaque"}},
		{RedactedSnapshotV1{}, []string{"Meta:meta", "Status:status", "Pairing:pairing", "Services:services", "Sessions:sessions", "Devices:devices", "Entities:entities", "Features:features", "UseCases:usecases"}},
		{PairingObservationV1{}, []string{"RemoteSKI:remote_ski", "State:state", "Since:since", "Opaque:opaque"}},
		{ServiceV1{}, []string{"SKI:ski", "SHIPID:ship_id", "Name:name", "Identifier:identifier", "Brand:brand", "Type:type", "Model:model", "SecondaryDigest:secondary_digest", "Opaque:opaque"}},
		{SessionV1{}, []string{"ID:id", "RemoteSKI:remote_ski", "State:state", "Since:since", "Opaque:opaque"}},
		{DeviceV1{}, []string{"SKI:ski", "SHIPID:ship_id", "Address:address", "Type:type", "Description:description", "Metadata:metadata", "SecondaryDigest:secondary_digest", "Opaque:opaque"}},
		{EntityV1{}, []string{"DeviceAddress:device_address", "EntityAddress:entity_address", "Type:type", "Description:description", "SecondaryDigest:secondary_digest", "Opaque:opaque"}},
		{FeatureV1{}, []string{"DeviceAddress:device_address", "EntityAddress:entity_address", "FeatureAddress:feature_address", "Type:type", "Role:role", "Description:description", "SecondaryDigest:secondary_digest", "Opaque:opaque"}},
		{UseCaseV1{}, []string{"ContextAddress:context_address", "Name:name", "Actor:actor", "ResolvedRole:resolved_role", "Scenarios:scenarios", "Version:version", "Availability:availability", "DocumentSubrevision:document_subrevision", "SecondaryDigest:secondary_digest", "Opaque:opaque"}},
		{OpaqueObservationV1{}, []string{"Path:path", "Source:source", "Value:value"}},
		{OpaqueValueV1{}, []string{"Scalar:scalar", "Array:array", "Object:object"}},
		{OpaqueScalarV1{}, []string{"Null:null", "Boolean:boolean", "Integer:integer", "String:string"}},
		{MetadataV1{}, []string{"Values:values"}},
		{MetadataValueV1{}, []string{"Null:null", "Boolean:boolean", "Integer:integer", "String:string"}},
		{RedactedServiceV1{}, []string{"ID:id", "Kind:kind", "Visible:visible", "Paired:paired"}},
		{RedactedSessionV1{}, []string{"ID:id", "Remote:remote", "State:state", "Since:since"}},
		{RedactedDeviceV1{}, []string{"ID:id", "Entities:entities", "UseCaseClaims:usecase_claims"}},
		{RedactedEntityV1{}, []string{"ID:id", "Features:features"}},
		{RedactedFeatureV1{}, []string{"ID:id", "Role:role"}},
		{RedactedUseCaseV1{}, []string{"ID:id"}},
	} {
		assertIssue77Fields(t, reflect.TypeOf(check.value), check.fields)
	}

	var (
		service = ServiceV1{}
		device  = DeviceV1{}
		entity  = EntityV1{}
		feature = FeatureV1{}
		usecase = UseCaseV1{}
	)
	assertIssue77Type[*string](t, service.SHIPID)
	assertIssue77Type[*string](t, service.SecondaryDigest)
	assertIssue77Type[*[]OpaqueObservationV1](t, service.Opaque)
	assertIssue77Type[*string](t, device.Description)
	assertIssue77Type[*MetadataV1](t, device.Metadata)
	assertIssue77Type[*string](t, entity.Description)
	assertIssue77Type[*string](t, feature.Description)
	assertIssue77Type[*string](t, usecase.ResolvedRole)
	assertIssue77Type[*[]string](t, usecase.Scenarios)
	assertIssue77Type[*string](t, usecase.Version)
	assertIssue77Type[*bool](t, usecase.Availability)
	assertIssue77Type[*string](t, usecase.DocumentSubrevision)

	var _ func(SnapshotV1) (RedactedSnapshotV1, error) = BuildRedactedSnapshotV1
}

func TestIssue77VR940FixtureRetainsRawOperationalFacts(t *testing.T) {
	snapshot := issue77Snapshot(t)
	if snapshot.Meta.DataHash != issue77FixtureHash {
		t.Fatalf("raw data_hash = %q, want fixture JCS hash %q", snapshot.Meta.DataHash, issue77FixtureHash)
	}
	if len(snapshot.Services) != 1 || len(snapshot.Devices) != 1 ||
		len(snapshot.Entities) != 11 || len(snapshot.Features) != 20 || len(snapshot.UseCases) != 10 {
		t.Fatalf(
			"VR940 fixture counts services/devices/entities/features/usecases = %d/%d/%d/%d/%d, want 1/1/11/20/10",
			len(snapshot.Services), len(snapshot.Devices), len(snapshot.Entities), len(snapshot.Features), len(snapshot.UseCases),
		)
	}

	service := snapshot.Services[0]
	if service.SKI != issue77RemoteSKI || service.SHIPID == nil || *service.SHIPID != "vaillant-vr940f-ship-id" ||
		service.Name != "Vaillant VR940f eeBUS" || service.Identifier != "vr940f-lab-service" ||
		service.Brand != "Vaillant" || service.Type != "eeBUS" || service.Model != "VR940f" {
		t.Fatalf("raw mDNS/SHIP service facts were not retained: %+v", service)
	}
	device := snapshot.Devices[0]
	if device.SKI != issue77RemoteSKI || device.SHIPID == nil || *device.SHIPID != *service.SHIPID ||
		device.Address != "d:_n:Vaillant_VR940" || device.Type != "EnergyManagementSystem" ||
		device.Description == nil || *device.Description != "VR940f gateway" ||
		device.Metadata == nil || len(device.Metadata.Values) != 4 {
		t.Fatalf("raw SPINE device facts were not retained: %+v", device)
	}
	if snapshot.Entities[0].DeviceAddress != device.Address || snapshot.Entities[0].EntityAddress == "" ||
		snapshot.Entities[0].Type == "" || snapshot.Entities[0].Description == nil {
		t.Fatalf("raw entity facts were not retained: %+v", snapshot.Entities[0])
	}
	if snapshot.Features[0].DeviceAddress != device.Address || snapshot.Features[0].EntityAddress == "" ||
		snapshot.Features[0].FeatureAddress == "" || snapshot.Features[0].Type == "" ||
		snapshot.Features[0].Role == "" || snapshot.Features[0].Description == nil {
		t.Fatalf("raw feature facts were not retained: %+v", snapshot.Features[0])
	}
	for index, claim := range snapshot.UseCases {
		if claim.ContextAddress == "" || claim.Name == "" || claim.Actor == "" ||
			claim.ResolvedRole == nil || claim.Scenarios == nil || claim.Version == nil ||
			claim.Availability == nil || claim.DocumentSubrevision == nil {
			t.Fatalf("named use-case claim %d lost raw fields: %+v", index, claim)
		}
	}
}

func TestIssue77OptionalPresenceSurvivesConstructionAndJSON(t *testing.T) {
	document := issue77FixtureDocument(t)
	service := document["services"].([]any)[0].(map[string]any)
	service["ship_id"] = ""
	service["opaque"] = []any{}
	device := document["devices"].([]any)[0].(map[string]any)
	device["description"] = ""
	device["metadata"] = map[string]any{}
	entity := document["entities"].([]any)[0].(map[string]any)
	entity["description"] = ""
	feature := document["features"].([]any)[0].(map[string]any)
	feature["description"] = ""
	usecase := document["usecases"].([]any)[0].(map[string]any)
	usecase["resolved_role"] = ""
	usecase["scenarios"] = []any{}
	usecase["version"] = ""
	usecase["availability"] = false
	usecase["document_subrevision"] = ""
	document["meta"].(map[string]any)["data_hash"] = ""

	snapshot := issue77ConstructDocument(t, document)
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	assertIssue77JSONPresent(t, got["services"].([]any)[0].(map[string]any), "ship_id", "")
	assertIssue77JSONPresent(t, got["services"].([]any)[0].(map[string]any), "opaque", []any{})
	assertIssue77JSONPresent(t, got["devices"].([]any)[0].(map[string]any), "description", "")
	assertIssue77JSONPresent(t, got["devices"].([]any)[0].(map[string]any), "metadata", map[string]any{})
	assertIssue77JSONPresent(t, got["entities"].([]any)[0].(map[string]any), "description", "")
	assertIssue77JSONPresent(t, got["features"].([]any)[0].(map[string]any), "description", "")
	assertIssue77JSONPresent(t, got["usecases"].([]any)[0].(map[string]any), "resolved_role", "")
	assertIssue77JSONPresent(t, got["usecases"].([]any)[0].(map[string]any), "scenarios", []any{})
	assertIssue77JSONPresent(t, got["usecases"].([]any)[0].(map[string]any), "availability", false)
}

func TestIssue77RawJCSHashAndOrderAreDeterministic(t *testing.T) {
	first := issue77Snapshot(t)
	reordered := first.Clone()
	slices.Reverse(reordered.Services)
	slices.Reverse(reordered.Sessions)
	slices.Reverse(reordered.Devices)
	slices.Reverse(reordered.Entities)
	slices.Reverse(reordered.Features)
	slices.Reverse(reordered.UseCases)
	slices.Reverse(reordered.Opaque)
	for index := range reordered.UseCases {
		if reordered.UseCases[index].Scenarios != nil {
			slices.Reverse(*reordered.UseCases[index].Scenarios)
		}
	}
	reordered.Meta.DataHash = ""
	second, err := NewSnapshotV1(reordered)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatalf("equivalent raw orderings are not byte-identical:\n%s\n%s", firstJSON, secondJSON)
	}
	if second.Meta.DataHash != issue77FixtureHash {
		t.Fatalf("reordered JCS hash = %q, want %q", second.Meta.DataHash, issue77FixtureHash)
	}

	captureOnly := first.Clone()
	captureOnly.Meta.CapturedAt = captureOnly.Meta.CapturedAt.Add(24 * time.Hour)
	captureOnly.Meta.DataHash = ""
	hash, err := captureOnly.ComputeDataHash()
	if err != nil {
		t.Fatal(err)
	}
	if hash != issue77FixtureHash {
		t.Fatalf("captured_at changed raw data hash: %q", hash)
	}
}

func TestIssue77RedactedSnapshotIsSeparateIrreversibleAndIndependentlyHashed(t *testing.T) {
	raw := issue77Snapshot(t)
	redacted, err := BuildRedactedSnapshotV1(raw)
	if err != nil {
		t.Fatal(err)
	}
	reordered := raw.Clone()
	slices.Reverse(reordered.Entities)
	slices.Reverse(reordered.Features)
	slices.Reverse(reordered.UseCases)
	reordered.Meta.DataHash = ""
	reordered, err = NewSnapshotV1(reordered)
	if err != nil {
		t.Fatal(err)
	}
	reorderedRedacted, err := BuildRedactedSnapshotV1(reordered)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(redacted)
	if err != nil {
		t.Fatal(err)
	}
	reorderedEncoded, err := json.Marshal(reorderedRedacted)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, reorderedEncoded) || redacted.Meta.DataHash != reorderedRedacted.Meta.DataHash {
		t.Fatal("redacted JSON/hash changed with equivalent raw input ordering")
	}
	if raw.Meta.DataHash == redacted.Meta.DataHash {
		t.Fatalf("raw and redacted snapshots share data_hash %q", raw.Meta.DataHash)
	}
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(redacted.Meta.DataHash) {
		t.Fatalf("redacted data_hash is not canonical: %q", redacted.Meta.DataHash)
	}
	for _, forbidden := range []string{
		issue77RemoteSKI,
		"vaillant-vr940f-ship-id",
		"Vaillant VR940f eeBUS",
		"vr940f-lab-service",
		"Vaillant",
		"VR940f",
		"d:_n:Vaillant_VR940",
		"serial_number",
		"firmware_version",
		"opaque",
		"metadata",
		"secondary_digest",
		"candidate_ref",
		"entity_address",
		"feature_address",
		"context_address",
	} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Errorf("redacted snapshot leaked %q: %s", forbidden, encoded)
		}
	}
	if strings.Contains(fmt.Sprintf("%v %#v %s", redacted, redacted, redacted), issue77RemoteSKI) {
		t.Fatal("redacted formatter leaked raw identity")
	}
}

func TestIssue77SecondaryDigestNeverSubstitutesForRawFields(t *testing.T) {
	tests := []struct {
		collection string
		field      string
	}{
		{"services", "ski"},
		{"services", "name"},
		{"services", "identifier"},
		{"services", "brand"},
		{"services", "type"},
		{"services", "model"},
		{"devices", "ski"},
		{"devices", "address"},
		{"devices", "type"},
		{"entities", "device_address"},
		{"entities", "entity_address"},
		{"features", "feature_address"},
		{"features", "type"},
		{"features", "role"},
		{"usecases", "context_address"},
		{"usecases", "name"},
		{"usecases", "actor"},
	}
	for _, test := range tests {
		t.Run(test.collection+"-"+test.field, func(t *testing.T) {
			document := issue77FixtureDocument(t)
			delete(document[test.collection].([]any)[0].(map[string]any), test.field)
			document["meta"].(map[string]any)["data_hash"] = ""
			if _, err := issue77TryConstructDocument(document); err == nil {
				t.Fatalf("secondary_digest replaced required raw %s.%s", test.collection, test.field)
			}
		})
	}
}

func TestIssue77SecretDenylistFailsClosedWithoutFormatterOrErrorDisclosure(t *testing.T) {
	denylist := []string{
		"private_" + "key",
		"private_" + "pem",
		"trust_" + "store_bytes",
		"credential_" + "token",
		"bearer_" + "token",
		"session_" + "token",
		"authentication_" + "token",
		"cryptographic_" + "secret",
	}
	for _, key := range denylist {
		t.Run("opaque-"+key, func(t *testing.T) {
			marker := "issue77-sensitive-" + strings.ReplaceAll(key, "_", "-")
			document := issue77FixtureDocument(t)
			document["opaque"] = []any{map[string]any{
				"path":   "/diagnostic/nested",
				"source": "spine",
				"value": map[string]any{
					"safe": map[string]any{key: marker},
				},
			}}
			document["meta"].(map[string]any)["data_hash"] = ""
			draft, decodeErr := issue77DecodeDocument(document)
			if decodeErr != nil {
				assertIssue77NoDisclosure(t, decodeErr, marker)
				return
			}
			assertIssue77RejectedWithoutDisclosure(t, "Validate", draft.Validate(), marker)
			_, constructErr := NewSnapshotV1(draft)
			assertIssue77RejectedWithoutDisclosure(t, "NewSnapshotV1", constructErr, marker)
			_, redactErr := BuildRedactedSnapshotV1(draft)
			assertIssue77RejectedWithoutDisclosure(t, "BuildRedactedSnapshotV1", redactErr, marker)
			_, marshalErr := json.Marshal(draft)
			assertIssue77RejectedWithoutDisclosure(t, "MarshalJSON", marshalErr, marker)
			if formatted := fmt.Sprintf("%v %+v %#v %s %q", draft, draft, draft, draft, draft); strings.Contains(formatted, marker) {
				t.Fatalf("SnapshotV1 formatter disclosed rejected secret marker: %q", formatted)
			}
		})
		t.Run("metadata-"+key, func(t *testing.T) {
			marker := "issue77-sensitive-" + strings.ReplaceAll(key, "_", "-")
			document := issue77FixtureDocument(t)
			metadata := document["devices"].([]any)[0].(map[string]any)["metadata"].(map[string]any)
			metadata[key] = marker
			document["meta"].(map[string]any)["data_hash"] = ""
			draft, decodeErr := issue77DecodeDocument(document)
			if decodeErr != nil {
				assertIssue77NoDisclosure(t, decodeErr, marker)
				return
			}
			assertIssue77RejectedWithoutDisclosure(t, "Validate", draft.Validate(), marker)
			_, constructErr := NewSnapshotV1(draft)
			assertIssue77RejectedWithoutDisclosure(t, "NewSnapshotV1", constructErr, marker)
			_, redactErr := BuildRedactedSnapshotV1(draft)
			assertIssue77RejectedWithoutDisclosure(t, "BuildRedactedSnapshotV1", redactErr, marker)
			_, marshalErr := json.Marshal(draft)
			assertIssue77RejectedWithoutDisclosure(t, "MarshalJSON", marshalErr, marker)
			if formatted := fmt.Sprintf("%v %+v %#v %s %q", draft, draft, draft, draft, draft); strings.Contains(formatted, marker) {
				t.Fatalf("SnapshotV1 formatter disclosed rejected metadata marker: %q", formatted)
			}
		})
	}

	t.Run("pem-content", func(t *testing.T) {
		marker := "-----BEGIN " + "PRIVATE KEY-----issue77-sensitive-material"
		document := issue77FixtureDocument(t)
		document["opaque"] = []any{map[string]any{
			"path":   "/diagnostic/text",
			"source": "spine",
			"value":  map[string]any{"note": marker},
		}}
		document["meta"].(map[string]any)["data_hash"] = ""
		draft, decodeErr := issue77DecodeDocument(document)
		if decodeErr != nil {
			assertIssue77NoDisclosure(t, decodeErr, marker)
			return
		}
		assertIssue77RejectedWithoutDisclosure(t, "Validate", draft.Validate(), marker)
		_, constructErr := NewSnapshotV1(draft)
		assertIssue77RejectedWithoutDisclosure(t, "NewSnapshotV1", constructErr, marker)
		_, redactErr := BuildRedactedSnapshotV1(draft)
		assertIssue77RejectedWithoutDisclosure(t, "BuildRedactedSnapshotV1", redactErr, marker)
		_, marshalErr := json.Marshal(draft)
		assertIssue77RejectedWithoutDisclosure(t, "MarshalJSON", marshalErr, marker)
		if formatted := fmt.Sprintf("%v %+v %#v %s %q", draft, draft, draft, draft, draft); strings.Contains(formatted, marker) {
			t.Fatalf("SnapshotV1 formatter disclosed rejected PEM marker: %q", formatted)
		}
	})
}

func TestIssue77OpaqueJCSBoundsRejectEveryOverflow(t *testing.T) {
	tests := []struct {
		name  string
		value func() any
	}{
		{"depth", func() any {
			return map[string]any{"a": map[string]any{"b": map[string]any{"c": map[string]any{"d": true}}}}
		}},
		{"array-members", func() any { return make([]any, 33) }},
		{"object-members", func() any {
			value := make(map[string]any, 33)
			for index := 0; index < 33; index++ {
				value[fmt.Sprintf("k%02d", index)] = index
			}
			return value
		}},
		{"string-bytes", func() any { return strings.Repeat("x", 4097) }},
		{"jcs-bytes", func() any {
			value := make(map[string]any, 32)
			for index := 0; index < 32; index++ {
				value[fmt.Sprintf("k%02d", index)] = strings.Repeat("x", 600)
			}
			return value
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := issue77FixtureDocument(t)
			document["opaque"] = []any{map[string]any{"path": "/bounds", "source": "test", "value": test.value()}}
			document["meta"].(map[string]any)["data_hash"] = ""
			if _, err := issue77TryConstructDocument(document); err == nil {
				t.Fatal("NewSnapshotV1 accepted an out-of-bounds opaque value")
			}
		})
	}

	document := issue77FixtureDocument(t)
	observations := make([]any, 257)
	for index := range observations {
		observations[index] = map[string]any{"path": fmt.Sprintf("/opaque/%03d", index), "source": "test", "value": index}
	}
	document["opaque"] = observations
	document["meta"].(map[string]any)["data_hash"] = ""
	if _, err := issue77TryConstructDocument(document); err == nil {
		t.Fatal("NewSnapshotV1 accepted more than 256 opaque observations")
	}

	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"path-bytes", func(observation map[string]any) { observation["path"] = "/" + strings.Repeat("p", 512) }},
		{"source-bytes", func(observation map[string]any) { observation["source"] = strings.Repeat("s", 129) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			document := issue77FixtureDocument(t)
			observation := map[string]any{"path": "/bounds", "source": "test", "value": true}
			test.mutate(observation)
			document["opaque"] = []any{observation}
			document["meta"].(map[string]any)["data_hash"] = ""
			if _, err := issue77TryConstructDocument(document); err == nil {
				t.Fatal("NewSnapshotV1 accepted an out-of-bounds opaque observation")
			}
		})
	}

	document = issue77FixtureDocument(t)
	observations = make([]any, 17)
	for index := range observations {
		observations[index] = map[string]any{
			"path":   fmt.Sprintf("/aggregate/%02d", index),
			"source": "test",
			"value": []any{
				strings.Repeat("x", 4000),
				strings.Repeat("y", 4000),
				strings.Repeat("z", 4000),
				strings.Repeat("w", 4000),
			},
		}
	}
	document["opaque"] = observations
	document["meta"].(map[string]any)["data_hash"] = ""
	if _, err := issue77TryConstructDocument(document); err == nil {
		t.Fatal("NewSnapshotV1 accepted more than 262144 aggregate canonical opaque bytes")
	}
}

func TestIssue77RestartReloadPreservesRawAndRedactedHashes(t *testing.T) {
	raw := issue77Snapshot(t)
	redacted, err := BuildRedactedSnapshotV1(raw)
	if err != nil {
		t.Fatal(err)
	}
	rawJSON, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	var reloadedDraft SnapshotV1
	decoder := json.NewDecoder(bytes.NewReader(rawJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&reloadedDraft); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewSnapshotV1(reloadedDraft)
	if err != nil {
		t.Fatal(err)
	}
	reloadedRedacted, err := BuildRedactedSnapshotV1(reloaded)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Meta.DataHash != raw.Meta.DataHash || reloadedRedacted.Meta.DataHash != redacted.Meta.DataHash {
		t.Fatalf(
			"restart hashes raw/redacted = %q/%q, want %q/%q",
			reloaded.Meta.DataHash, reloadedRedacted.Meta.DataHash, raw.Meta.DataHash, redacted.Meta.DataHash,
		)
	}
}

func TestIssue77SnapshotCarriesNoCrossProtocolState(t *testing.T) {
	raw, err := json.Marshal(issue77Snapshot(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"candidate_ref",
		"semantic_registry",
		"ebus_registry",
		"canonical_zone",
		"dhw_projection",
		"energy_projection",
	} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Errorf("raw snapshot promoted a forbidden registry/semantic field %q", forbidden)
		}
	}
}

func issue77Snapshot(t *testing.T) SnapshotV1 {
	t.Helper()
	document := issue77FixtureDocument(t)
	snapshot := issue77ConstructDocument(t, document)
	return snapshot
}

func issue77FixtureDocument(t *testing.T) map[string]any {
	t.Helper()
	path := filepath.Join("testdata", "issue77", "vr940-raw-snapshot-v1.json")
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		t.Fatal(err)
	}
	return document
}

func issue77ConstructDocument(t *testing.T, document map[string]any) SnapshotV1 {
	t.Helper()
	snapshot, err := issue77TryConstructDocument(document)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func issue77TryConstructDocument(document map[string]any) (SnapshotV1, error) {
	draft, err := issue77DecodeDocument(document)
	if err != nil {
		return SnapshotV1{}, err
	}
	return NewSnapshotV1(draft)
}

func issue77DecodeDocument(document map[string]any) (SnapshotV1, error) {
	payload, err := json.Marshal(document)
	if err != nil {
		return SnapshotV1{}, err
	}
	var draft SnapshotV1
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&draft); err != nil {
		return SnapshotV1{}, err
	}
	return draft, nil
}

func assertIssue77Fields(t *testing.T, typ reflect.Type, expected []string) {
	t.Helper()
	if typ.NumField() != len(expected) {
		t.Fatalf("%s has %d fields, want %d: %v", typ, typ.NumField(), len(expected), expected)
	}
	for index, want := range expected {
		name, jsonName, _ := strings.Cut(want, ":")
		field := typ.Field(index)
		actualJSON, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if field.Name != name || actualJSON != jsonName {
			t.Fatalf("%s field %d = %s:%s, want %s", typ, index, field.Name, actualJSON, want)
		}
	}
}

func assertIssue77Type[Want any](t *testing.T, got any) {
	t.Helper()
	want := reflect.TypeOf((*Want)(nil)).Elem()
	if reflect.TypeOf(got) != want {
		t.Fatalf("field type = %T, want %s", got, want)
	}
}

func assertIssue77JSONPresent(t *testing.T, object map[string]any, key string, want any) {
	t.Helper()
	got, ok := object[key]
	if !ok {
		t.Fatalf("JSON omitted observed optional property %q", key)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("JSON property %q = %#v, want %#v", key, got, want)
	}
}

func assertIssue77RejectedWithoutDisclosure(t *testing.T, operation string, err error, marker string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s accepted secret-bearing raw data", operation)
	}
	assertIssue77NoDisclosure(t, err, marker)
}

func assertIssue77NoDisclosure(t *testing.T, err error, marker string) {
	t.Helper()
	if strings.Contains(err.Error(), marker) {
		t.Fatalf("error disclosed rejected secret marker %q: %v", marker, err)
	}
}

const issue77RemoteSKI = "2222222222222222222222222222222222222222"
