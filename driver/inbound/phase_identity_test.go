package inbound

import (
	"strings"
	"testing"
)

// TestPhaseIdentity varies every input of the resource id.
// Dropping installation, instance, driver kind or resource kind from nodeID
// makes the published id equal the id recomputed without that input.
// Dropping device, entity address or phase name from the slot makes two
// fixtures that differ only in that field share one id. Reversing entity
// order catches an arrival counter in the key.
func TestPhaseIdentity(t *testing.T) {
	first := phaseRemote("device.alpha", []Entity{phaseEntity(4, "a", "b"), phaseEntity(5, "a")})
	second := phaseRemote("device.beta", []Entity{phaseEntity(4, "a")})
	result, _ := publish(t, electricalScope("instance.phase"), first, second)
	ids := map[string]string{}
	for _, node := range result.Nodes {
		if string(node.Key.Kind) != "resource.phase" {
			continue
		}
		ids[node.Key.Anchor.Slot] = string(node.ID)
		if node.Key.Driver != driverKind {
			t.Fatalf("driver = %s", node.Key.Driver)
		}
	}
	alphaA := phaseSlot("device.alpha", "4", "a")
	alphaB := phaseSlot("device.alpha", "4", "b")
	alphaOther := phaseSlot("device.alpha", "5", "a")
	betaA := phaseSlot("device.beta", "4", "a")
	if len(ids) != 4 || ids[alphaA] == "" || ids[alphaB] == "" || ids[alphaOther] == "" || ids[betaA] == "" {
		t.Fatalf("phase ids = %+v", ids)
	}
	if ids[alphaA] == ids[alphaB] || ids[alphaA] == ids[alphaOther] || ids[alphaA] == ids[betaA] {
		t.Fatalf("phase identity collided: %+v", ids)
	}
	scope := electricalScope("instance.phase")
	published := ids[alphaA]
	node := placedNode{kind: "resource.phase", slot: alphaA}
	if published == string(scope.nodeID(node)) && published == string(electricalScope("instance.other").nodeID(node)) {
		t.Fatal("instance is not part of the resource id")
	}
	if published == string(scope.nodeID(placedNode{kind: "resource.meter", slot: alphaA})) {
		t.Fatal("resource kind is not part of the resource id")
	}
	otherInstallation := Scope{Installation: "installation.other", Instance: scope.Instance, Bridge: scope.Bridge}
	if published == string(otherInstallation.nodeID(node)) {
		t.Fatal("installation is not part of the resource id")
	}
	if published == opaqueID("resource.", scope.Installation, scope.Instance, node.kind, node.slot) {
		t.Fatal("driver kind is not part of the resource id")
	}
	forward := sortedKeyText(result)
	reversedRemote := phaseRemote("device.alpha", []Entity{phaseEntity(5, "a"), phaseEntity(4, "a", "b")})
	reversed, _ := publish(t, electricalScope("instance.phase"), second, reversedRemote)
	backward := sortedKeyText(reversed)
	if strings.Join(forward, "|") != strings.Join(backward, "|") {
		t.Fatalf("discovery order changed identities\n%v\n%v", forward, backward)
	}
}

func phaseRemote(device string, entities []Entity) Remote {
	return Remote{Device: device, SKI: "ski." + device, Ready: true, Generation: 4, Entities: entities}
}

func phaseEntity(address uint64, phases ...string) Entity {
	var readings []Reading
	for _, phase := range phases {
		readings = append(readings, measure("currentPerPhase", "10", "A", phase))
	}
	return Entity{Type: "HeatPumpAppliance", Address: []uint64{address}, Readings: readings}
}

func phaseSlot(device, entity, phase string) string {
	return "device/" + device + "/entity/" + entity + "/phase/" + phase
}
