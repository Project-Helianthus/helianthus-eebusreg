package eebusruntime

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

const (
	rawContainmentController = "d:_n:synthetic_controller"
	rawContainmentPeer       = "d:_n:synthetic_peer"
)

func rawContainmentAddress(device, suffix string) string {
	return device + ":" + suffix
}

func rawContainmentVector(t *testing.T) map[string]any {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("testdata", "raw_containment", "nested_entity.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func rawContainmentCopy(t *testing.T, document map[string]any) map[string]any {
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

func rawContainmentDecode(t *testing.T, document map[string]any) SnapshotV1 {
	t.Helper()
	payload, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	var draft SnapshotV1
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&draft); err != nil {
		t.Fatalf("vector does not decode into SnapshotV1: %v", err)
	}
	return draft
}

func rawContainmentBuild(t *testing.T, document map[string]any) (SnapshotV1, error) {
	t.Helper()
	return NewSnapshotV1(rawContainmentDecode(t, document))
}

func rawContainmentItem(document map[string]any, key string, index int) map[string]any {
	return document[key].([]any)[index].(map[string]any)
}

func rawContainmentAppendCopy(t *testing.T, document map[string]any, key string, index int) map[string]any {
	t.Helper()
	copied := rawContainmentCopy(t, rawContainmentItem(document, key, index))
	document[key] = append(document[key].([]any), copied)
	return copied
}

func rawContainmentID(t *testing.T, kind eebusraw.IDKind, basis spineIdentityBasis) eebusraw.RedactedID {
	t.Helper()
	id, err := eebusraw.RedactID(kind, basis.preimage())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

type rawContainmentIDs struct {
	controller, peer                             eebusraw.RedactedID
	circuit, zone, peerEntity                    eebusraw.RedactedID
	circuitFeature, zoneFeature, peerFeature     eebusraw.RedactedID
	zoneClaim, featureClaim, peerClaim, unscoped eebusraw.RedactedID
}

func rawContainmentExpectedIDs(t *testing.T, raw SnapshotV1) rawContainmentIDs {
	t.Helper()
	controllerShip, peerShip := "synthetic-ship-0001", "synthetic-ship-0002"
	controller := spineDeviceBasis(&controllerShip, rawContainmentController)
	peer := spineDeviceBasis(&peerShip, rawContainmentPeer)
	circuit := controller.entity(rawContainmentAddress(rawContainmentController, "[1]:"))
	zone := controller.entity(rawContainmentAddress(rawContainmentController, "[1,1]:"))
	peerEntity := peer.entity(rawContainmentAddress(rawContainmentPeer, "[1]:"))
	claims := make(map[string]eebusraw.RedactedID, len(raw.UseCases))
	for _, useCase := range raw.UseCases {
		id, err := eebusraw.RedactID(eebusraw.IDKindPeer, useCaseIdentityV1(useCase))
		if err != nil {
			t.Fatal(err)
		}
		claims[useCase.Name] = id
	}
	return rawContainmentIDs{
		controller:     rawContainmentID(t, eebusraw.IDKindDevice, controller),
		peer:           rawContainmentID(t, eebusraw.IDKindDevice, peer),
		circuit:        rawContainmentID(t, eebusraw.IDKindEntity, circuit),
		zone:           rawContainmentID(t, eebusraw.IDKindEntity, zone),
		peerEntity:     rawContainmentID(t, eebusraw.IDKindEntity, peerEntity),
		circuitFeature: rawContainmentID(t, eebusraw.IDKindFeature, circuit.feature(rawContainmentAddress(rawContainmentController, "[1]:0"))),
		zoneFeature:    rawContainmentID(t, eebusraw.IDKindFeature, zone.feature(rawContainmentAddress(rawContainmentController, "[1,1]:0"))),
		peerFeature:    rawContainmentID(t, eebusraw.IDKindFeature, peerEntity.feature(rawContainmentAddress(rawContainmentPeer, "[1]:0"))),
		zoneClaim:      claims["configurationOfRoomHeatingTemperature"],
		featureClaim:   claims["monitoringOfRoomTemperature"],
		peerClaim:      claims["monitoringOfPowerConsumption"],
		unscoped:       claims["monitoringOfGridConnectionPoint"],
	}
}

func rawContainmentDevice(t *testing.T, snapshot RedactedSnapshotV1, id eebusraw.RedactedID) RedactedDeviceV1 {
	t.Helper()
	for _, device := range snapshot.Devices {
		if device.ID == id {
			return device
		}
	}
	t.Fatalf("redacted device %s is missing", id.Digest)
	return RedactedDeviceV1{}
}

func rawContainmentEntity(t *testing.T, entities []RedactedEntityV1, id eebusraw.RedactedID) RedactedEntityV1 {
	t.Helper()
	for _, entity := range entities {
		if entity.ID == id {
			return entity
		}
	}
	t.Fatalf("redacted entity %s is missing", id.Digest)
	return RedactedEntityV1{}
}

func rawContainmentClaim(t *testing.T, claims []RedactedUseCaseV1, id eebusraw.RedactedID) (RedactedUseCaseV1, bool) {
	t.Helper()
	for _, claim := range claims {
		if claim.ID == id {
			return claim, true
		}
	}
	return RedactedUseCaseV1{}, false
}

func rawContainmentScope(claim RedactedUseCaseV1) eebusraw.RedactedID {
	if claim.Scope == nil {
		return eebusraw.RedactedID{}
	}
	return *claim.Scope
}

func TestRawSnapshotContainmentVector(t *testing.T) {
	vector := rawContainmentVector(t)
	raw, err := rawContainmentBuild(t, vector)
	if err != nil {
		t.Fatalf("vector rejected: %v", err)
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	delete(document["meta"].(map[string]any), "data_hash")
	if !reflect.DeepEqual(document, vector) {
		t.Errorf("vector did not survive the raw snapshot:\n got %s\nwant %#v", encoded, vector)
	}

	zone := raw.Entities[0]
	if zone.EntityAddress != rawContainmentAddress(rawContainmentController, "[1,1]:") ||
		optionalStringV1(zone.ParentEntityAddress) != rawContainmentAddress(rawContainmentController, "[1]:") {
		t.Fatalf("nested entity lost its parent: %#v", zone)
	}
	if raw.Entities[1].ParentEntityAddress != nil || raw.Entities[2].ParentEntityAddress != nil {
		t.Fatal("top-level entity carries a parent")
	}
	entityClaim := raw.UseCases[0]
	if optionalStringV1(entityClaim.DeviceAddress) != rawContainmentController ||
		optionalStringV1(entityClaim.EntityAddress) != zone.EntityAddress || entityClaim.FeatureAddress != nil {
		t.Fatalf("entity-scoped claim does not resolve to its entity: %#v", entityClaim)
	}
	unscoped := raw.UseCases[2]
	if unscoped.DeviceAddress != nil || unscoped.EntityAddress != nil || unscoped.FeatureAddress != nil {
		t.Fatalf("claim without a reported scope gained one: %#v", unscoped)
	}

	redacted, err := BuildRedactedSnapshotV1(raw)
	if err != nil {
		t.Fatalf("redacted build rejected the vector: %v", err)
	}
	ids := rawContainmentExpectedIDs(t, raw)
	controller := rawContainmentDevice(t, redacted, ids.controller)
	peer := rawContainmentDevice(t, redacted, ids.peer)
	if len(controller.Entities) != 2 || len(peer.Entities) != 1 {
		t.Fatalf("device-to-entity relation: controller %d, peer %d", len(controller.Entities), len(peer.Entities))
	}
	nested := rawContainmentEntity(t, controller.Entities, ids.zone)
	if nested.ParentEntity == nil || *nested.ParentEntity != ids.circuit {
		t.Fatalf("redacted nested entity parent = %#v, want the circuit entity", nested.ParentEntity)
	}
	if top := rawContainmentEntity(t, controller.Entities, ids.circuit); top.ParentEntity != nil {
		t.Fatal("redacted top-level entity carries a parent")
	}
	if !reflect.DeepEqual(rawContainmentEntity(t, redacted.Entities, ids.zone), nested) {
		t.Fatal("top-level and nested entity differ")
	}
	if features := nested.Features; len(features) != 1 || features[0].ID != ids.zoneFeature {
		t.Fatalf("entity-to-feature relation of the nested entity = %#v", features)
	}
	if features := rawContainmentEntity(t, peer.Entities, ids.peerEntity).Features; len(features) != 1 || features[0].ID != ids.peerFeature {
		t.Fatalf("entity-to-feature relation of the peer entity = %#v", features)
	}

	for _, check := range []struct {
		name   string
		device RedactedDeviceV1
		claim  eebusraw.RedactedID
		scope  eebusraw.RedactedID
	}{
		{"entity-scoped", controller, ids.zoneClaim, ids.zone},
		{"feature-scoped", controller, ids.featureClaim, ids.circuitFeature},
		{"device-scoped", peer, ids.peerClaim, ids.peer},
	} {
		claim, found := rawContainmentClaim(t, check.device.UseCaseClaims, check.claim)
		if !found || rawContainmentScope(claim) != check.scope {
			t.Fatalf("%s claim under its device = %#v (found %t)", check.name, claim, found)
		}
		top, found := rawContainmentClaim(t, redacted.UseCases, check.claim)
		if !found || !reflect.DeepEqual(top, claim) {
			t.Fatalf("%s claim differs at top level: %#v", check.name, top)
		}
	}
	if len(controller.UseCaseClaims) != 2 || len(peer.UseCaseClaims) != 1 {
		t.Fatalf("claims per device: controller %d, peer %d", len(controller.UseCaseClaims), len(peer.UseCaseClaims))
	}
	top, found := rawContainmentClaim(t, redacted.UseCases, ids.unscoped)
	if !found || top.Scope != nil {
		t.Fatalf("claim without a reported scope at top level = %#v (found %t)", top, found)
	}
	if _, nestedClaim := rawContainmentClaim(t, controller.UseCaseClaims, ids.unscoped); nestedClaim {
		t.Fatal("claim without a reported scope was placed under a device by its context address")
	}

	payload, err := json.Marshal(redacted)
	if err != nil {
		t.Fatal(err)
	}
	var decoded RedactedSnapshotV1
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("redacted round trip rejected: %v", err)
	}
	if !reflect.DeepEqual(decoded, redacted) {
		t.Fatal("redacted round trip changed the snapshot")
	}
}

type rawContainmentMutation struct {
	name   string
	mutate func(*testing.T, map[string]any)
	want   string
}

func rawContainmentSet(key string, index int, field string, value any) func(*testing.T, map[string]any) {
	return func(_ *testing.T, document map[string]any) {
		item := rawContainmentItem(document, key, index)
		if value == nil {
			delete(item, field)
			return
		}
		item[field] = value
	}
}

func rawContainmentMutations() []rawContainmentMutation {
	controller := func(suffix string) string { return rawContainmentAddress(rawContainmentController, suffix) }
	peer := func(suffix string) string { return rawContainmentAddress(rawContainmentPeer, suffix) }
	const (
		missingDevice = "entity references a missing device"
		missingParent = "entity references a missing parent entity"
		missingEntity = "feature references a missing entity"
		missingScope  = "use-case claim references a missing scope"
	)
	return []rawContainmentMutation{
		{"ambiguous device address", func(t *testing.T, document map[string]any) {
			rawContainmentAppendCopy(t, document, "devices", 1)["ski"] = strings.Repeat("3", 40)
		}, "device address is ambiguous"},
		{"entity device dangling", rawContainmentSet("entities", 2, "device_address", "d:_n:synthetic_absent"), missingDevice},
		{"entity duplicate", func(t *testing.T, document map[string]any) {
			rawContainmentAppendCopy(t, document, "entities", 1)
		}, "entities contain a duplicate identity"},
		{"entity parent dangling", rawContainmentSet("entities", 0, "parent_entity_address", controller("[9]:")), missingParent},
		{"entity parent is itself", rawContainmentSet("entities", 1, "parent_entity_address", controller("[1]:")), missingParent},
		{"entity parent on another device", rawContainmentSet("entities", 2, "parent_entity_address", controller("[1]:")), missingParent},
		{"entity parent cycle", rawContainmentSet("entities", 1, "parent_entity_address", controller("[1,1]:")), "entity containment has a cycle"},
		{"entity parent blank", rawContainmentSet("entities", 0, "parent_entity_address", ""), "entity parent address is invalid"},
		{"feature device dangling", rawContainmentSet("features", 2, "device_address", rawContainmentController), missingEntity},
		{"feature entity dangling", rawContainmentSet("features", 0, "entity_address", controller("[9]:")), missingEntity},
		{"feature duplicate", func(t *testing.T, document map[string]any) {
			rawContainmentAppendCopy(t, document, "features", 1)
		}, "features contain a duplicate identity"},
		{"claim device dangling", rawContainmentSet("use_cases", 3, "device_address", "d:_n:synthetic_absent"), missingScope},
		{"claim device of another entity", rawContainmentSet("use_cases", 0, "device_address", rawContainmentPeer), missingScope},
		{"claim entity dangling", rawContainmentSet("use_cases", 0, "entity_address", controller("[9]:")), missingScope},
		{"claim entity of another device", rawContainmentSet("use_cases", 0, "entity_address", peer("[1]:")), missingScope},
		{"claim feature dangling", rawContainmentSet("use_cases", 1, "feature_address", controller("[1]:9")), missingScope},
		{"claim feature of another entity", rawContainmentSet("use_cases", 1, "entity_address", controller("[1,1]:")), missingScope},
		{"claim feature without entity", rawContainmentSet("use_cases", 1, "entity_address", nil), missingScope},
		{"claim entity without device", rawContainmentSet("use_cases", 0, "device_address", nil), missingScope},
		{"claim feature without device", func(_ *testing.T, document map[string]any) {
			claim := rawContainmentItem(document, "use_cases", 2)
			claim["entity_address"], claim["feature_address"] = controller("[1]:"), controller("[1]:0")
		}, missingScope},
		{"claim scope blank", rawContainmentSet("use_cases", 0, "entity_address", ""), "use-case scope address is invalid"},
		{"claim duplicate scope", func(t *testing.T, document map[string]any) {
			rawContainmentAppendCopy(t, document, "use_cases", 0)["context_address"] = controller("[1,1]:duplicate")
		}, "duplicate use-case claim"},
	}
}

func TestRawSnapshotContainmentRejectsInvalidEndpoints(t *testing.T) {
	vector := rawContainmentVector(t)
	for _, tc := range rawContainmentMutations() {
		t.Run(tc.name, func(t *testing.T) {
			document := rawContainmentCopy(t, vector)
			tc.mutate(t, document)
			draft := rawContainmentDecode(t, document)
			if _, err := NewSnapshotV1(draft); err == nil || err.Error() != tc.want {
				t.Fatalf("NewSnapshotV1 error = %v, want %q", err, tc.want)
			}
			if _, err := BuildRedactedSnapshotV1(draft); err == nil || err.Error() != tc.want {
				t.Fatalf("BuildRedactedSnapshotV1 error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestRawSnapshotContainmentClaimKeyVariesEveryField(t *testing.T) {
	controller := func(suffix string) string { return rawContainmentAddress(rawContainmentController, suffix) }
	vector := rawContainmentVector(t)
	for _, tc := range []struct {
		name   string
		source int
		vary   func(map[string]any)
	}{
		{"device_address", 3, func(claim map[string]any) { claim["device_address"] = rawContainmentController }},
		{"entity_address", 0, func(claim map[string]any) { claim["entity_address"] = controller("[1]:") }},
		{"feature_address", 1, func(claim map[string]any) { delete(claim, "feature_address") }},
		{"name", 0, func(claim map[string]any) { claim["name"] = "monitoringOfRoomHeatingSystemFunction" }},
		{"actor", 0, func(claim map[string]any) { claim["actor"] = "HeatingCircuit" }},
		{"version", 0, func(claim map[string]any) { claim["version"] = "1.0.1" }},
		{"version unset", 0, func(claim map[string]any) { delete(claim, "version") }},
		{"document_subrevision", 0, func(claim map[string]any) { claim["document_subrevision"] = "draft" }},
		{"document_subrevision unset", 0, func(claim map[string]any) { delete(claim, "document_subrevision") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			document := rawContainmentCopy(t, vector)
			duplicate := rawContainmentAppendCopy(t, document, "use_cases", tc.source)
			duplicate["context_address"] = controller("[]:variant")
			if _, err := rawContainmentBuild(t, document); err == nil || err.Error() != "duplicate use-case claim" {
				t.Fatalf("unvaried copy error = %v, want the duplicate claim rejection", err)
			}
			tc.vary(duplicate)
			raw, err := rawContainmentBuild(t, document)
			if err != nil {
				t.Fatalf("claim differing in %s rejected: %v", tc.name, err)
			}
			if _, err := BuildRedactedSnapshotV1(raw); err != nil {
				t.Fatalf("redacted build rejected a claim differing in %s: %v", tc.name, err)
			}
		})
	}
}

func rawContainmentRedacted(t *testing.T) (RedactedSnapshotV1, rawContainmentIDs) {
	t.Helper()
	raw, err := rawContainmentBuild(t, rawContainmentVector(t))
	if err != nil {
		t.Fatal(err)
	}
	redacted, err := BuildRedactedSnapshotV1(raw)
	if err != nil {
		t.Fatal(err)
	}
	return redacted, rawContainmentExpectedIDs(t, raw)
}

func rawContainmentEditEntity(snapshot *RedactedSnapshotV1, id eebusraw.RedactedID, nestedOnly bool, edit func(*RedactedEntityV1)) {
	for deviceIndex := range snapshot.Devices {
		for entityIndex := range snapshot.Devices[deviceIndex].Entities {
			if snapshot.Devices[deviceIndex].Entities[entityIndex].ID == id {
				edit(&snapshot.Devices[deviceIndex].Entities[entityIndex])
			}
		}
	}
	if nestedOnly {
		return
	}
	for index := range snapshot.Entities {
		if snapshot.Entities[index].ID == id {
			edit(&snapshot.Entities[index])
		}
	}
}

func rawContainmentEditClaim(snapshot *RedactedSnapshotV1, id eebusraw.RedactedID, nestedOnly bool, edit func(*RedactedUseCaseV1)) {
	for deviceIndex := range snapshot.Devices {
		for claimIndex := range snapshot.Devices[deviceIndex].UseCaseClaims {
			if snapshot.Devices[deviceIndex].UseCaseClaims[claimIndex].ID == id {
				edit(&snapshot.Devices[deviceIndex].UseCaseClaims[claimIndex])
			}
		}
	}
	if nestedOnly {
		return
	}
	for index := range snapshot.UseCases {
		if snapshot.UseCases[index].ID == id {
			edit(&snapshot.UseCases[index])
		}
	}
}

func rawContainmentPointer(id eebusraw.RedactedID) *eebusraw.RedactedID {
	return &id
}

func TestRawSnapshotContainmentRedactedValidatorRejectsInvalidEndpoints(t *testing.T) {
	valid, ids := rawContainmentRedacted(t)
	absentEntity := eebusraw.RedactedID{Kind: eebusraw.IDKindEntity, Masked: ids.zone.Masked, Digest: "sha256:" + strings.Repeat("a", 64)}
	setParent := func(entity eebusraw.RedactedID, parent *eebusraw.RedactedID, nestedOnly bool) func(*RedactedSnapshotV1) {
		return func(snapshot *RedactedSnapshotV1) {
			rawContainmentEditEntity(snapshot, entity, nestedOnly, func(value *RedactedEntityV1) { value.ParentEntity = parent })
		}
	}
	setScope := func(claim eebusraw.RedactedID, scope *eebusraw.RedactedID, nestedOnly bool) func(*RedactedSnapshotV1) {
		return func(snapshot *RedactedSnapshotV1) {
			rawContainmentEditClaim(snapshot, claim, nestedOnly, func(value *RedactedUseCaseV1) { value.Scope = scope })
		}
	}
	deviceIndex := func(snapshot *RedactedSnapshotV1, id eebusraw.RedactedID) int {
		for index := range snapshot.Devices {
			if snapshot.Devices[index].ID == id {
				return index
			}
		}
		return -1
	}
	for _, tc := range []struct {
		name   string
		mutate func(*RedactedSnapshotV1)
		want   string
	}{
		{"parent dangling", setParent(ids.zone, &absentEntity, false), "entity references a missing parent entity"},
		{"parent on another device", setParent(ids.peerEntity, rawContainmentPointer(ids.circuit), false), "entity references a missing parent entity"},
		{"parent is itself", setParent(ids.circuit, rawContainmentPointer(ids.circuit), false), "entity references a missing parent entity"},
		{"parent cycle", setParent(ids.circuit, rawContainmentPointer(ids.zone), false), "entity containment has a cycle"},
		{"parent kind", setParent(ids.zone, rawContainmentPointer(ids.controller), false), "redacted entity parent identity is invalid"},
		{"parent differs from top level", setParent(ids.zone, nil, true), "redacted device contains an invalid entity relationship"},
		{"scope dangling", setScope(ids.zoneClaim, &absentEntity, false), "use-case claim references a missing scope"},
		{"scope entity of another device", setScope(ids.zoneClaim, rawContainmentPointer(ids.peerEntity), false), "use-case claim references a missing scope"},
		{"scope feature of another device", setScope(ids.featureClaim, rawContainmentPointer(ids.peerFeature), false), "use-case claim references a missing scope"},
		{"scope another device", setScope(ids.peerClaim, rawContainmentPointer(ids.controller), false), "use-case claim references a missing scope"},
		{"scope kind", setScope(ids.zoneClaim, rawContainmentPointer(ids.unscoped), false), "redacted use-case scope identity is invalid"},
		{"scope differs from top level", setScope(ids.zoneClaim, rawContainmentPointer(ids.circuit), true), "redacted device contains an invalid use-case relationship"},
		{"scoped claim not under a device", func(snapshot *RedactedSnapshotV1) {
			index := deviceIndex(snapshot, ids.peer)
			snapshot.Devices[index].UseCaseClaims = nil
		}, "redacted graph contains an unreferenced relationship"},
		{"unscoped claim under a device", func(snapshot *RedactedSnapshotV1) {
			index := deviceIndex(snapshot, ids.controller)
			snapshot.Devices[index].UseCaseClaims = append(snapshot.Devices[index].UseCaseClaims, RedactedUseCaseV1{ID: ids.unscoped})
		}, "redacted use-case claim without a scope is placed under a device"},
		{"scope removed", setScope(ids.zoneClaim, nil, false), "redacted use-case claim without a scope is placed under a device"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := canonicalRedactedSnapshotV1(valid)
			candidate.Devices = cloneRedactedDevicesV1(candidate.Devices)
			candidate.Entities = cloneRedactedEntitiesV1(candidate.Entities)
			tc.mutate(&candidate)
			candidate.Meta.DataHash = ""
			hash, err := candidate.computeDataHash()
			if err != nil {
				t.Fatal(err)
			}
			candidate.Meta.DataHash = hash
			if err := candidate.Validate(); err == nil || err.Error() != tc.want {
				t.Fatalf("Validate error = %v, want %q", err, tc.want)
			}
			type wire RedactedSnapshotV1
			payload, err := json.Marshal(wire(candidate))
			if err != nil {
				t.Fatal(err)
			}
			var decoded RedactedSnapshotV1
			if err := json.Unmarshal(payload, &decoded); err == nil {
				t.Fatal("UnmarshalJSON accepted the malformed redacted snapshot")
			}
		})
	}
}

func rawContainmentNativeGraph(snapshot NativeSnapshotV2) spineContainment {
	var graph spineContainment
	for _, device := range snapshot.Devices {
		graph.devices = append(graph.devices, device.Address)
	}
	for _, entity := range snapshot.Entities {
		graph.entities = append(graph.entities, spineContainedEntity{entity.DeviceAddress, entity.EntityAddress, entity.ParentEntityAddress})
	}
	for _, feature := range snapshot.Features {
		graph.features = append(graph.features, spineContainedFeature{feature.DeviceAddress, feature.EntityAddress, feature.FeatureAddress})
	}
	for _, claim := range snapshot.UseCases {
		graph.claims = append(graph.claims, spineContainedClaim{
			device: claim.DeviceAddress, entity: claim.EntityAddress, feature: claim.FeatureAddress,
			identity: spineClaimIdentity(claim.Name, claim.Actor, claim.Version, claim.DocumentSubrevision),
		})
	}
	return graph
}

func TestRawSnapshotContainmentRuleMatchesNativeSnapshot(t *testing.T) {
	vector := nativeContainmentVector(t)
	cases := append([]nativeContainmentMutation{{name: "vector", mutate: func(map[string]any) {}}}, nativeContainmentMutations()...)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			document := nativeContainmentCopy(t, vector)
			tc.mutate(document)
			payload, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			var draft NativeSnapshotV2
			if err := json.Unmarshal(payload, &draft); err != nil {
				t.Fatal(err)
			}
			native, shared := validateNativeContainment(draft), validateSpineContainment(rawContainmentNativeGraph(draft))
			if (native == nil) != (shared == nil) || native != nil && native.Error() != shared.Error() {
				t.Fatalf("shared rule = %v, native rule = %v", shared, native)
			}
			if native != nil && native.Error() != tc.want {
				t.Fatalf("native rule = %v, want %q", native, tc.want)
			}
		})
	}
}
