package inbound

import (
	"errors"
	"fmt"

	semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"
)

func resourceKind(entityType string) (string, bool) {
	switch entityType {
	case "HeatingZone":
		return "resource.zone", true
	case "DHWCircuit":
		return "resource.domestic_hot_water", true
	case "HeatingCircuit":
		return "resource.thermal_circuit", true
	case "HVACRoom":
		return "resource.room", true
	default:
		return "", false
	}
}

func priorityOf(rank string) (int32, error) {
	switch rank {
	case "", "producer":
		return PriorityProducer, nil
	case "secondary":
		return PrioritySecondary, nil
	case "relayed":
		return PriorityRelayed, nil
	default:
		return 0, fmt.Errorf("inbound: rank %q is not producer, secondary or relayed", rank)
	}
}

func rankName(rank string) string {
	if rank == "" {
		return "producer"
	}
	return rank
}

func sourcePath(rank, device, entity, fact, phase string) string {
	path := "rank/" + rankName(rank) + "/device/" + device + "/entity/" + entity + "/fact/" + fact
	if phase != "" {
		path += "/phase/" + phase
	}
	return path
}

func (w *world) ingest(remotes []Remote) error {
	for _, remote := range remotes {
		if err := w.ingestRemote(remote); err != nil {
			return err
		}
	}
	return w.equalPriorities()
}

func (w *world) ingestRemote(remote Remote) error {
	if _, revoked := w.revoked[remote.SKI]; revoked {
		return nil
	}
	if !remote.Ready {
		w.markUnavailable(remote.SKI)
		return nil
	}
	if remote.Device == "" {
		w.note(remote.Device, "missing_identity:device")
		return nil
	}
	w.clearDevice(remote.Device)
	if electricalOnly(remote) {
		return w.ingestElectrical(remote)
	}
	system := w.placeSystem(remote)
	if system.incomplete {
		return nil
	}
	w.identityMembers(system)
	updated := map[string]struct{}{}
	for _, entity := range remote.Entities {
		placed, ok := w.placeEntity(remote, entity)
		if !ok {
			continue
		}
		if placed.name != "" && placed.kind == "resource.zone" {
			w.extra(placed.slot, "zone_name", placed.name)
		}
		for _, reading := range entity.Readings {
			if err := w.ingestReading(remote, entity.Type, entityText(entity.Address), reading, updated); err != nil {
				return err
			}
		}
	}
	for _, reading := range remote.Readings {
		if err := w.ingestReading(remote, "", "", reading, updated); err != nil {
			return err
		}
	}
	w.noteUnresolved(remote.Device)
	w.noteUnpublishableRelations(remote.Device)
	w.omitAbsent(remote.SKI, updated)
	w.admitCommands(remote)
	return nil
}

func (w *world) placeSystem(remote Remote) placedNode {
	node := placedNode{
		kind: "resource.thermal_system", slot: deviceSlot(remote.Device), device: remote.Device,
		entityType: "device", ski: remote.SKI, brand: remote.Brand, vendor: remote.Vendor,
		manufacturer: trim(remote.Manufacturer), serial: trim(remote.Serial), identityToken: trim(remote.IdentityToken),
		identity: true,
	}
	if len(node.slot) > 128 {
		node.incomplete = true
		node.missing = "slot"
		w.note(remote.Device, "missing_identity:slot")
	}
	w.nodes[node.slot] = node
	return node
}

func (w *world) identityMembers(system placedNode) {
	if system.ski != "" {
		w.extra(system.slot, "ship_ski", system.ski)
	}
	if system.brand != "" {
		w.extra(system.slot, "gateway_brand", system.brand)
	}
	if system.vendor != "" {
		w.extra(system.slot, "gateway_vendor", system.vendor)
	}
}

func (w *world) placeEntity(remote Remote, entity Entity) (placedNode, bool) {
	text := entityText(entity.Address)
	if text == "" {
		slot := "missing/" + remote.Device + "/" + entity.Type
		w.nodes[slot] = placedNode{slot: slot, device: remote.Device, entityType: entity.Type, ski: remote.SKI, incomplete: true, missing: "entity"}
		w.extra(slot, "missing_identity", "entity")
		w.note(remote.Device, "missing_identity:"+slot)
		return placedNode{}, false
	}
	kind, ok := resourceKind(entity.Type)
	if !ok {
		if entity.Type != "TemperatureSensor" {
			w.note(remote.Device, "entity_type:"+entity.Type)
			return placedNode{}, false
		}
		return placedNode{device: remote.Device, entity: text, entityType: entity.Type, ski: remote.SKI}, true
	}
	node := placedNode{
		kind: kind, slot: entitySlot(remote.Device, text), device: remote.Device, entity: text,
		entityType: entity.Type, parent: entityText(entity.Parent), ski: remote.SKI, name: entity.Name,
	}
	if len(node.slot) > 128 {
		node.incomplete = true
		node.missing = "slot"
		w.nodes[node.slot] = node
		w.note(remote.Device, "missing_identity:"+node.slot)
		return placedNode{}, false
	}
	w.nodes[node.slot] = node
	return node, true
}

func (w *world) noteUnresolved(device string) {
	for _, node := range w.nodes {
		if node.device != device || node.incomplete || node.parent == "" {
			continue
		}
		if _, ok := w.nodes[entitySlot(device, node.parent)]; !ok {
			w.note(device, "parent_unresolved:"+node.entity)
		}
	}
}

func (w *world) markUnavailable(ski string) {
	for path, fact := range w.facts {
		if fact.ski != ski || fact.withdrawn || fact.availability == semreg.AvailabilityUnavailable {
			continue
		}
		fact.availability = semreg.AvailabilityUnavailable
		fact.revision++
		w.facts[path] = fact
	}
}

func (w *world) omitAbsent(ski string, updated map[string]struct{}) {
	for path, fact := range w.facts {
		if fact.ski != ski || fact.withdrawn {
			continue
		}
		if _, seen := updated[path]; seen {
			continue
		}
		if _, stopped := w.stopped[ski][fact.generation]; stopped {
			continue
		}
		if fact.availability == semreg.AvailabilityUnavailable {
			continue
		}
		fact.availability = semreg.AvailabilityUnavailable
		fact.revision++
		w.facts[path] = fact
	}
}

func (w *world) stop(ski string, generation uint64) {
	if w.stopped[ski] == nil {
		w.stopped[ski] = map[uint64]struct{}{}
	}
	w.stopped[ski][generation] = struct{}{}
	for path, fact := range w.facts {
		if fact.ski != ski || fact.generation != generation || fact.withdrawn || fact.freshness == semreg.FreshnessStale {
			continue
		}
		fact.freshness = semreg.FreshnessStale
		fact.revision++
		w.facts[path] = fact
	}
}

func (w *world) revoke(ski string) {
	w.revoked[ski] = struct{}{}
	for path, fact := range w.facts {
		if fact.ski != ski || fact.withdrawn {
			continue
		}
		fact.withdrawn = true
		fact.availability = semreg.AvailabilityWithdrawn
		fact.revision++
		w.facts[path] = fact
	}
	w.dropRemote(ski)
}

type elementKey struct {
	slot string
	fact string
}

func (w *world) equalPriorities() error {
	seen := map[elementKey]placedFact{}
	for _, fact := range w.facts {
		if fact.withdrawn {
			continue
		}
		key := elementKey{fact.slot, fact.fact}
		previous, ok := seen[key]
		if !ok {
			seen[key] = fact
			continue
		}
		if previous.path != fact.path && previous.priority == fact.priority {
			return errors.New("inbound: two sources of one element declare the same priority")
		}
	}
	return nil
}

func trim(value string) string {
	start, end := 0, len(value)
	for start < end && (value[start] == ' ' || value[start] == '\t') {
		start++
	}
	for end > start && (value[end-1] == ' ' || value[end-1] == '\t') {
		end--
	}
	return value[start:end]
}
