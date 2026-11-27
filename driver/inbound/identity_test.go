package inbound

import (
	"sort"
	"strings"
	"testing"

	semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"
)

func TestReversedDiscoveryOrderKeepsIdentities(t *testing.T) {
	forward := []Remote{heatingRemote(), renamedRemote("device.other", "synthetic-ship-other")}
	backward := []Remote{reverseRemote(forward[1]), reverseRemote(forward[0])}
	left, _ := publish(t, heatingScope("instance.heating"), forward...)
	right, _ := publish(t, heatingScope("instance.heating"), backward...)
	if strings.Join(nodeIdentities(left), "\n") != strings.Join(nodeIdentities(right), "\n") {
		t.Fatalf("node identities differ\n%s\n%s", strings.Join(nodeIdentities(left), "\n"), strings.Join(nodeIdentities(right), "\n"))
	}
	if strings.Join(linkIdentities(left), "\n") != strings.Join(linkIdentities(right), "\n") {
		t.Fatalf("link keys differ\n%s\n%s", strings.Join(linkIdentities(left), "\n"), strings.Join(linkIdentities(right), "\n"))
	}
}

func TestTwoInstancesPublishDistinctSourcesAndMembers(t *testing.T) {
	east, eastHost := publish(t, heatingScope("instance.east"), heatingRemote())
	west, westHost := publish(t, heatingScope("instance.west"), heatingRemote())
	if len(east.Nodes) == 0 || nodeIdentities(east)[0] == nodeIdentities(west)[0] {
		t.Fatal("instances published the same node identity")
	}
	for _, member := range east.Members {
		if member.Member.Key.Instance == "instance.west" || member.Member.Key.Namespace != "eebus-inbound" {
			t.Fatalf("east member = %+v", member.Member.Key)
		}
	}
	eastSources, westSources := sourceIDs(eastHost), sourceIDs(westHost)
	for id := range eastSources {
		if westSources[id] {
			t.Fatalf("source %s is shared by both instances", id)
		}
	}
}

func TestNodeIdentityFields(t *testing.T) {
	base := publishOne(t, heatingScope("instance.heating"), oneZone("device.heat", 1))
	system := requireNode(t, base, "resource.thermal_system", "device/device.heat")
	zone := requireNode(t, base, "resource.zone", "device/device.heat/entity/1")
	if system.Key.Driver != "eebus-inbound" || zone.Key.Driver != "eebus-inbound" {
		t.Fatal("driver kind is not eebus-inbound")
	}
	if system.Key.Anchor.Slot == "" || system.Key.Anchor.Parent != "" || system.Key.Anchor.Scheme != "" || system.Key.Anchor.Issuer != "" || system.Key.Anchor.Key != "" {
		t.Fatalf("anchor = %+v", system.Key.Anchor)
	}
	t.Run("installation", func(t *testing.T) {
		other := publishOne(t, Scope{Installation: "installation.other", Instance: "instance.heating", Bridge: "bridge.heating"}, oneZone("device.heat", 1))
		moved := requireNode(t, other, "resource.thermal_system", "device/device.heat")
		if moved.Key.Installation == system.Key.Installation || moved.ID == system.ID {
			t.Fatal("installation is not part of the node identity")
		}
	})
	t.Run("instance", func(t *testing.T) {
		other := publishOne(t, heatingScope("instance.other"), oneZone("device.heat", 1))
		moved := requireNode(t, other, "resource.thermal_system", "device/device.heat")
		if moved.Key.Instance == system.Key.Instance || moved.ID == system.ID {
			t.Fatal("instance is not part of the node identity")
		}
	})
	t.Run("kind", func(t *testing.T) {
		if zone.Key.Kind == system.Key.Kind || zone.ID == system.ID {
			t.Fatal("kind is not part of the node identity")
		}
	})
	t.Run("device anchor", func(t *testing.T) {
		other := publishOne(t, heatingScope("instance.heating"), oneZone("device.other", 1))
		moved := requireNode(t, other, "resource.thermal_system", "device/device.other")
		if moved.Key.Anchor.Slot == system.Key.Anchor.Slot || moved.ID == system.ID {
			t.Fatal("device anchor is not part of the node identity")
		}
	})
	t.Run("entity anchor", func(t *testing.T) {
		other := publishOne(t, heatingScope("instance.heating"), oneZone("device.heat", 4))
		moved := requireNode(t, other, "resource.zone", "device/device.heat/entity/4")
		if moved.Key.Anchor.Slot == zone.Key.Anchor.Slot || moved.ID == zone.ID {
			t.Fatal("entity anchor is not part of the node identity")
		}
	})
	t.Run("ship ski", func(t *testing.T) {
		remote := oneZone("device.heat", 1)
		remote.SKI = "synthetic-ship-other"
		other := publishOne(t, heatingScope("instance.heating"), remote)
		moved := requireNode(t, other, "resource.zone", zone.Key.Anchor.Slot)
		if moved.Key != zone.Key || moved.ID != zone.ID {
			t.Fatal("ship ski changed the node identity")
		}
	})
	t.Run("bridge", func(t *testing.T) {
		other := publishOne(t, Scope{Installation: "installation.synthetic", Instance: "instance.heating", Bridge: "bridge.other"}, oneZone("device.heat", 1))
		moved := requireNode(t, other, "resource.zone", zone.Key.Anchor.Slot)
		if moved.Key != zone.Key || moved.ID != zone.ID {
			t.Fatal("bridge changed the node identity")
		}
	})
}

func TestLinkIdentityFields(t *testing.T) {
	base := publishOne(t, heatingScope("instance.heating"), circuitRoom("device.heat", 7, 6))
	link := requireLink(t, base, "thermal.relation.circuit_room")
	if link.Key.Type.Pack.ID != "helianthus.pack.thermal" || link.Key.Type.Pack.Version != "1.0.0" || link.Key.Type.Version != "1.0.0" {
		t.Fatalf("relation type = %+v", link.Key.Type)
	}
	if link.Key.From.Bridge != "bridge.heating" || link.Key.To.Bridge != "bridge.heating" {
		t.Fatalf("endpoints = %+v %+v", link.Key.From, link.Key.To)
	}
	t.Run("relation type", func(t *testing.T) {
		other := publishOne(t, heatingScope("instance.heating"), zoneRoom("device.heat", 8, 9))
		moved := requireLink(t, other, "thermal.relation.zone_room")
		if moved.Key.Type.ID == link.Key.Type.ID || moved.Key.Type.Pack != link.Key.Type.Pack || moved.Key.Type.Version != link.Key.Type.Version {
			t.Fatal("containment does not change the relation type")
		}
	})
	t.Run("from", func(t *testing.T) {
		other := publishOne(t, heatingScope("instance.heating"), circuitRoom("device.heat", 4, 6))
		moved := requireLink(t, other, "thermal.relation.circuit_room")
		if moved.Key.From.ID == link.Key.From.ID || moved.Key.To.ID != link.Key.To.ID || moved.Key.From.Bridge != link.Key.From.Bridge || moved.Key.To.Bridge != link.Key.To.Bridge {
			t.Fatalf("from did not follow the parent: base %+v moved %+v", link.Key, moved.Key)
		}
	})
	t.Run("to", func(t *testing.T) {
		other := publishOne(t, heatingScope("instance.heating"), circuitRoom("device.heat", 7, 5))
		moved := requireLink(t, other, "thermal.relation.circuit_room")
		if moved.Key.To.ID == link.Key.To.ID || moved.Key.From.ID != link.Key.From.ID || moved.Key.From.Bridge != link.Key.From.Bridge || moved.Key.To.Bridge != link.Key.To.Bridge {
			t.Fatalf("to did not follow the child: base %+v moved %+v", link.Key, moved.Key)
		}
	})
	t.Run("bridge", func(t *testing.T) {
		other := publishOne(t, Scope{Installation: "installation.synthetic", Instance: "instance.heating", Bridge: "bridge.other"}, circuitRoom("device.heat", 7, 6))
		moved := requireLink(t, other, "thermal.relation.circuit_room")
		if moved.Key.From.Bridge == link.Key.From.Bridge || moved.Key.To.Bridge != "bridge.other" || moved.Key.From.Bridge != moved.Key.To.Bridge {
			t.Fatalf("bridge = %+v %+v", moved.Key.From, moved.Key.To)
		}
		if moved.Key.Type != link.Key.Type || moved.Key.From.ID != link.Key.From.ID || moved.Key.To.ID != link.Key.To.ID {
			t.Fatal("bridge changed the relation or the resource ids")
		}
	})
}

func TestClaimIdentityFields(t *testing.T) {
	baseHost := hostOf(t, heatingScope("instance.heating"), oneZone("device.heat", 1))
	serial := baseHost.serials[0]
	match := baseHost.matches[0]
	if serial.Manufacturer != "synthetic-maker" || serial.Serial != "synthetic-serial" || match.Scheme != "scheme.ieee.eui48" || match.Issuer != "" {
		t.Fatalf("serial %+v match %+v", serial, match)
	}
	if serial.Proof[0].Digest != proofDigest(serial.Manufacturer+"\n"+serial.Serial, "device.classification") || match.Key != identityRecordDigest(match.Scheme, "0123456789AB") {
		t.Fatal("claim proof does not cover the reported fields")
	}
	for _, proof := range []semreg.EvidenceRef{serial.Proof[0], match.Proof[0]} {
		if proof.Owner != "eebus.inbound" || proof.Kind != "native.observation" || proof.Contract != "eebus.inbound/v1" || proof.Access != semreg.EvidenceAccessPublic || proof.Redaction != semreg.RedactionNone || proof.Digest == "" {
			t.Fatalf("proof = %+v", proof)
		}
	}
	if match.Proof[0].Digest != proofDigest(match.Scheme+"\n"+"0123456789AB", "identity.token") {
		t.Fatal("match proof does not cover the scheme and token")
	}
	t.Run("serial member", func(t *testing.T) {
		other := hostOf(t, heatingScope("instance.heating"), oneZone("device.other", 1)).serials[0]
		if other.Member.ID == serial.Member.ID {
			t.Fatal("manufacturer serial member ignores the device")
		}
	})
	t.Run("manufacturer", func(t *testing.T) {
		remote := oneZone("device.heat", 1)
		remote.Manufacturer = "synthetic-other-maker"
		other := hostOf(t, heatingScope("instance.heating"), remote).serials[0]
		if other.Manufacturer == serial.Manufacturer || other.Proof[0].Digest == serial.Proof[0].Digest || other.Proof[0].Digest != proofDigest(other.Manufacturer+"\n"+other.Serial, "device.classification") {
			t.Fatal("manufacturer is not part of the serial claim")
		}
	})
	t.Run("serial", func(t *testing.T) {
		remote := oneZone("device.heat", 1)
		remote.Serial = "synthetic-other-serial"
		other := hostOf(t, heatingScope("instance.heating"), remote).serials[0]
		if other.Serial == serial.Serial || other.Proof[0].Digest == serial.Proof[0].Digest || other.Proof[0].Digest != proofDigest(other.Manufacturer+"\n"+other.Serial, "device.classification") {
			t.Fatal("serial is not part of the serial claim")
		}
	})
	t.Run("match member", func(t *testing.T) {
		other := hostOf(t, heatingScope("instance.heating"), oneZone("device.other", 1)).matches[0]
		if other.Member.ID == match.Member.ID {
			t.Fatal("match member ignores the device")
		}
	})
	t.Run("match scheme", func(t *testing.T) {
		remote := oneZone("device.heat", 1)
		remote.IdentityToken = "0123456789ABCDEF"
		other := hostOf(t, heatingScope("instance.heating"), remote).matches[0]
		if other.Scheme != "scheme.ieee.eui64" || other.Scheme == match.Scheme || other.Key == match.Key || other.Proof[0].Digest == match.Proof[0].Digest || other.Proof[0].Digest != proofDigest(other.Scheme+"\n"+remote.IdentityToken, "identity.token") {
			t.Fatalf("scheme = %+v", other)
		}
	})
	t.Run("match key", func(t *testing.T) {
		remote := oneZone("device.heat", 1)
		remote.IdentityToken = "0123456789AC"
		other := hostOf(t, heatingScope("instance.heating"), remote).matches[0]
		if other.Scheme != match.Scheme || other.Key == match.Key || other.Key != identityRecordDigest(other.Scheme, remote.IdentityToken) || other.Issuer != "" || other.Proof[0].Digest == match.Proof[0].Digest || other.Proof[0].Digest != proofDigest(other.Scheme+"\n"+remote.IdentityToken, "identity.token") {
			t.Fatalf("key = %+v", other)
		}
	})
	t.Run("bridge", func(t *testing.T) {
		scope := Scope{Installation: "installation.synthetic", Instance: "instance.heating", Bridge: "bridge.other"}
		other := hostOf(t, scope, oneZone("device.heat", 1))
		if other.serials[0].Member.Bridge == serial.Member.Bridge || other.serials[0].Member.ID != serial.Member.ID || other.serials[0].Member.Bridge != "bridge.other" {
			t.Fatalf("serial bridge = %+v", other.serials[0].Member)
		}
		if other.matches[0].Member.Bridge == match.Member.Bridge || other.matches[0].Member.ID != match.Member.ID || other.matches[0].Member.Bridge != "bridge.other" {
			t.Fatalf("match bridge = %+v", other.matches[0].Member)
		}
	})
	t.Run("missing identification", func(t *testing.T) {
		remote := oneZone("device.heat", 1)
		remote.Manufacturer, remote.Serial = "", "   "
		result, session := publish(t, heatingScope("instance.heating"), remote)
		if len(session.serials) != 0 || !gapSet(result)["identification_missing:device.heat"] {
			t.Fatalf("missing identification serials %d gaps %v", len(session.serials), result.Gaps)
		}
	})
	t.Run("manufacturer missing", func(t *testing.T) {
		remote := oneZone("device.heat", 1)
		remote.Manufacturer = ""
		result, session := publish(t, heatingScope("instance.heating"), remote)
		if len(session.serials) != 0 || !gapSet(result)["manufacturer_missing:device.heat"] {
			t.Fatalf("gaps %v", result.Gaps)
		}
	})
	t.Run("unlisted ship ski", func(t *testing.T) {
		result, session := publish(t, heatingScope("instance.heating"), oneZone("device.heat", 1))
		if len(session.matches) != 1 || session.matches[0].Scheme == "" || !gapSet(result)["ship_ski_scheme_unlisted:device.heat"] {
			t.Fatalf("matches %+v gaps %v", session.matches, result.Gaps)
		}
	})
	t.Run("unlisted token", func(t *testing.T) {
		remote := oneZone("device.heat", 1)
		remote.IdentityToken = "0123456789ab"
		result, session := publish(t, heatingScope("instance.heating"), remote)
		if len(session.matches) != 0 || !gapSet(result)["identity_token_unlisted:device.heat"] {
			t.Fatalf("matches %+v gaps %v", session.matches, result.Gaps)
		}
	})
}

func renamedRemote(device, ski string) Remote {
	remote := heatingRemote()
	remote.Device = device
	remote.SKI = ski
	return remote
}

func reverseRemote(remote Remote) Remote {
	remote.Entities = append([]Entity(nil), remote.Entities...)
	remote.Readings = append([]Reading(nil), remote.Readings...)
	reverseSlice(remote.Entities)
	reverseSlice(remote.Readings)
	for i := range remote.Entities {
		remote.Entities[i].Address = append([]uint64(nil), remote.Entities[i].Address...)
		remote.Entities[i].Parent = append([]uint64(nil), remote.Entities[i].Parent...)
		remote.Entities[i].Readings = append([]Reading(nil), remote.Entities[i].Readings...)
		reverseSlice(remote.Entities[i].Readings)
	}
	return remote
}

func reverseSlice[T any](values []T) {
	for i, j := 0, len(values)-1; i < j; i, j = i+1, j-1 {
		values[i], values[j] = values[j], values[i]
	}
}

func oneZone(device string, address uint64) Remote {
	return Remote{
		Device: device, SKI: "synthetic-ship-ski", Manufacturer: "synthetic-maker", Serial: "synthetic-serial",
		IdentityToken: "0123456789AB", Ready: true, Generation: 4,
		Entities: []Entity{zone(address, nil, "north", "auto", "20", "21")},
	}
}

func circuitRoom(device string, circuit, roomAddress uint64) Remote {
	remote := oneZone(device, circuit)
	remote.Entities = []Entity{{Type: "HeatingCircuit", Address: []uint64{circuit}}, room(roomAddress, []uint64{circuit}, "19", "20", "on", "")}
	return remote
}

func zoneRoom(device string, zoneAddress, roomAddress uint64) Remote {
	remote := oneZone(device, zoneAddress)
	remote.Entities = []Entity{zone(zoneAddress, nil, "north", "auto", "20", "21"), room(roomAddress, []uint64{zoneAddress}, "19", "20", "on", "")}
	return remote
}

func publishOne(t *testing.T, scope Scope, remote Remote) Result {
	t.Helper()
	result, _ := publish(t, scope, remote)
	return result
}

func hostOf(t *testing.T, scope Scope, remote Remote) *host {
	t.Helper()
	_, session := publish(t, scope, remote)
	if len(session.serials) != 1 || len(session.matches) != 1 {
		t.Fatalf("serials %d matches %d", len(session.serials), len(session.matches))
	}
	return session
}

func requireNode(t *testing.T, result Result, kind, slot string) NodeView {
	t.Helper()
	for _, node := range result.Nodes {
		if string(node.Key.Kind) == kind && node.Key.Anchor.Slot == slot {
			return node
		}
	}
	t.Fatalf("missing node %s %s", kind, slot)
	return NodeView{}
}

func requireLink(t *testing.T, result Result, id string) LinkView {
	t.Helper()
	for _, link := range result.Links {
		if string(link.Key.Type.ID) == id {
			return link
		}
	}
	t.Fatalf("missing link %s", id)
	return LinkView{}
}

func nodeIdentities(result Result) []string {
	out := make([]string, 0, len(result.Nodes))
	for _, node := range result.Nodes {
		out = append(out, node.Key.Installation+"|"+string(node.Key.Driver)+"|"+string(node.Key.Instance)+"|"+string(node.Key.Kind)+"|"+node.Key.Anchor.Slot+"|"+string(node.ID))
	}
	sort.Strings(out)
	return out
}

func linkIdentities(result Result) []string {
	out := make([]string, 0, len(result.Links))
	for _, link := range result.Links {
		out = append(out, string(link.Key.Type.ID)+"|"+string(link.Key.From.ID)+"|"+string(link.Key.To.ID))
	}
	sort.Strings(out)
	return out
}

func sourceIDs(session *host) map[string]bool {
	out := map[string]bool{}
	for _, sample := range session.samples {
		if sample.Candidate.Origin.SourceID != nil {
			out[string(*sample.Candidate.Origin.SourceID)] = true
		}
	}
	return out
}

type recordedIdentity struct {
	Scheme string `json:"scheme"`
	Issuer string `json:"issuer"`
	Token  string `json:"token"`
}

func (recordedIdentity) Validate() error { return nil }

type recordedProof struct {
	Text string `json:"text"`
	Unit string `json:"unit"`
}

func (recordedProof) Validate() error { return nil }

func identityRecordDigest(scheme, token string) semreg.Digest {
	sum, err := semreg.DigestRecord(recordedIdentity{Scheme: scheme, Issuer: "", Token: token})
	if err != nil {
		panic(err)
	}
	return sum
}

func proofDigest(text, unit string) semreg.Digest {
	sum, err := semreg.DigestRecord(recordedProof{Text: text, Unit: unit})
	if err != nil {
		panic(err)
	}
	return sum
}
