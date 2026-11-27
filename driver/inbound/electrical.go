package inbound

import (
	"strconv"
	"strings"

	semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"
)

// maxSlotBytes is the anchor slot limit. A longer slot is an incomplete node.
const maxSlotBytes = 128

func heatingContent(remote Remote) bool {
	if heatingReadings(remote.Readings, "") {
		return true
	}
	for _, entity := range remote.Entities {
		switch entity.Type {
		case "HeatingZone", "DHWCircuit", "HeatingCircuit", "HVACRoom", "TemperatureSensor":
			return true
		}
		if heatingReadings(entity.Readings, entity.Type) {
			return true
		}
	}
	return false
}

func heatingReadings(readings []Reading, entityType string) bool {
	for _, reading := range readings {
		if _, ok := findRow(entityType, reading.Field); ok {
			return true
		}
		if _, ok := namespaceLocal(reading.Field); ok {
			return true
		}
	}
	return false
}

func electricalOnly(remote Remote) bool {
	if heatingContent(remote) {
		return false
	}
	for _, entity := range remote.Entities {
		if entity.Type != "" {
			return true
		}
	}
	return false
}

func electricalKind(entityType string) (string, semreg.AnchorClass, bool) {
	switch entityType {
	case "GridConnectionPointOfPremises":
		return "resource.grid_connection", semreg.AnchorFunction, true
	case "HeatPumpAppliance":
		return "resource.meter", semreg.AnchorEquipment, true
	case "Inverter":
		return "resource.inverter", semreg.AnchorEquipment, true
	case "EVSE":
		return "resource.evse", semreg.AnchorComponent, true
	case "EV":
		return "resource.vehicle", semreg.AnchorEquipment, true
	case "PVSystem":
		return "resource.pv_system", semreg.AnchorFunction, true
	case "BatterySystem":
		return "resource.storage_system", semreg.AnchorFunction, true
	default:
		return "", "", false
	}
}

func (w *world) ingestElectrical(remote Remote) error {
	w.placeElectricalNodes(remote)
	updated := map[string]struct{}{}
	opened := w.openSeenSessions(remote)
	for _, entity := range remote.Entities {
		if err := w.ingestElectricalEntity(remote, entity, updated); err != nil {
			return err
		}
	}
	w.retireSessions(remote, opened)
	w.noteConfigurationGaps(remote)
	w.markElectricalIdentity(remote)
	w.omitAbsent(remote.SKI, updated)
	return nil
}

func (w *world) placeElectricalNodes(remote Remote) {
	var evse []string
	for _, entity := range remote.Entities {
		if slot, ok := w.placeElectricalEntity(remote, entity); ok && entity.Type == "EVSE" {
			evse = append(evse, slot)
		}
	}
	if len(evse) == 0 {
		return
	}
	w.putNode(placedNode{
		kind: "resource.charging_station", slot: deviceSlot(remote.Device), device: remote.Device,
		entityType: "device", ski: remote.SKI, anchor: semreg.AnchorEquipment,
	})
	for _, slot := range evse {
		node := w.nodes[slot]
		w.putNode(placedNode{
			kind: "resource.connector", slot: slot + "/connector", device: remote.Device,
			entity: node.entity, entityType: "connector", ski: remote.SKI,
			anchor: semreg.AnchorComponent, parentSlot: slot,
		})
		w.note(remote.Device, "implied_resource:resource.connector")
		w.note(remote.Device, "configuration_only_relation:evse.relation.asset_evse")
		w.note(remote.Device, "configuration_only_relation:evse.relation.evse_connector")
	}
}

func (w *world) placeElectricalEntity(remote Remote, entity Entity) (string, bool) {
	text := entityText(entity.Address)
	if text == "" {
		slot := "missing/" + remote.Device + "/" + entity.Type
		w.putNode(placedNode{slot: slot, device: remote.Device, entityType: entity.Type, ski: remote.SKI, incomplete: true, missing: "entity"})
		w.extra(slot, "missing_identity", "entity")
		w.note(remote.Device, "missing_identity:"+slot)
		return "", false
	}
	kind, class, known := electricalKind(entity.Type)
	if !known {
		kind, class = "resource.meter", semreg.AnchorEquipment
		w.note(remote.Device, "monitored_unit:"+entity.Type)
	}
	node := placedNode{
		kind: kind, slot: entitySlot(remote.Device, text), device: remote.Device, entity: text,
		entityType: entity.Type, ski: remote.SKI, name: entity.Name, anchor: class,
	}
	if entity.Type == "EVSE" {
		node.parentSlot = deviceSlot(remote.Device)
	}
	w.putNode(node)
	stored, ok := w.liveNode(node.slot)
	if !ok {
		return "", false
	}
	return stored.slot, true
}

func (w *world) putNode(node placedNode) {
	if previous, ok := w.nodes[node.slot]; ok && previous.retired {
		w.note(node.device, "retired_resource:"+node.slot)
		return
	}
	if len(node.slot) > maxSlotBytes {
		node.incomplete = true
		node.missing = "slot"
		w.note(node.device, "missing_identity:"+node.slot)
	}
	if !node.incomplete && needsParent(node.anchor) && node.parentSlot == "" {
		node.incomplete = true
		node.missing = "parent"
		w.note(node.device, "missing_parent:"+node.slot)
	}
	w.nodes[node.slot] = node
}

func needsParent(class semreg.AnchorClass) bool {
	return class == semreg.AnchorComponent || class == semreg.AnchorEpisode
}

func (w *world) liveNode(slot string) (placedNode, bool) {
	node, ok := w.nodes[slot]
	if !ok || node.incomplete || node.retired || node.kind == "" {
		return placedNode{}, false
	}
	return node, true
}

func phaseAllowed(name string) bool {
	switch name {
	case "a", "b", "c", "ab", "bc", "ac", "abc", "neutral", "ground", "none":
		return true
	default:
		return false
	}
}

func (w *world) ensurePhase(remote Remote, parent placedNode, phase string) (placedNode, bool) {
	if !phaseAllowed(phase) {
		w.note(remote.Device, "phase_name:"+phase)
		return placedNode{}, false
	}
	if parent.kind == "resource.evse" || parent.kind == "resource.vehicle" {
		connector, ok := w.oneKind(remote.Device, "resource.connector")
		if !ok {
			w.note(remote.Device, "connector_ambiguous")
			return placedNode{}, false
		}
		parent = connector
	}
	slot := parent.slot + "/phase/" + phase
	w.putNode(placedNode{
		kind: "resource.phase", slot: slot, device: remote.Device, entity: parent.entity,
		entityType: "phase", ski: remote.SKI, name: phase, anchor: semreg.AnchorComponent, parentSlot: parent.slot,
	})
	return w.liveNode(slot)
}

func (w *world) ensureGridMeter(remote Remote, grid placedNode) (placedNode, bool) {
	slot := grid.slot + "/meter"
	w.putNode(placedNode{
		kind: "resource.meter", slot: slot, device: remote.Device, entity: grid.entity,
		entityType: "meter", ski: remote.SKI, anchor: semreg.AnchorEquipment,
	})
	w.note(remote.Device, "implied_resource:resource.meter")
	return w.liveNode(slot)
}

func (w *world) oneKind(device, kind string) (placedNode, bool) {
	var found placedNode
	count := 0
	for _, node := range w.nodes {
		if node.device != device || node.kind != kind || node.incomplete || node.retired {
			continue
		}
		found = node
		count++
	}
	if count != 1 {
		return placedNode{}, false
	}
	return found, true
}

func sessionSlot(connector placedNode, unix int64) string {
	episode := "unspecified"
	if unix != 0 {
		episode = strconv.FormatInt(unix, 10)
	}
	return connector.slot + "/session/" + episode
}

func (w *world) openSession(remote Remote, connector placedNode, unix int64) (placedNode, bool) {
	slot := sessionSlot(connector, unix)
	if previous, ok := w.nodes[slot]; ok && previous.retired {
		w.note(remote.Device, "session_retired:"+episodeOf(slot))
		return placedNode{}, false
	}
	w.putNode(placedNode{
		kind: "resource.charging_session", slot: slot, device: remote.Device, entity: connector.entity,
		entityType: "session", ski: remote.SKI, anchor: semreg.AnchorEpisode, parentSlot: connector.slot,
	})
	return w.liveNode(slot)
}

func episodeOf(slot string) string {
	if i := strings.LastIndex(slot, "/"); i >= 0 {
		return slot[i+1:]
	}
	return slot
}

func (w *world) openSeenSessions(remote Remote) map[string]struct{} {
	opened := map[string]struct{}{}
	connector, ok := w.oneKind(remote.Device, "resource.connector")
	if !ok {
		return opened
	}
	for _, entity := range remote.Entities {
		for _, reading := range entity.Readings {
			if reading.Field != "connected" || reading.Text != "true" {
				continue
			}
			node, placed := w.openSession(remote, connector, reading.ObservedUnix)
			if placed {
				opened[node.slot] = struct{}{}
			}
		}
	}
	return opened
}

func (w *world) retireSessions(remote Remote, opened map[string]struct{}) {
	for slot, node := range w.nodes {
		if node.device != remote.Device || node.kind != "resource.charging_session" || node.retired || node.incomplete {
			continue
		}
		if _, ok := opened[slot]; ok {
			continue
		}
		node.retired = true
		w.nodes[slot] = node
		w.withdrawSlot(remote.SKI, slot)
	}
}

func (w *world) withdrawSlot(ski, slot string) {
	for path, fact := range w.facts {
		if fact.ski != ski || fact.slot != slot || fact.withdrawn {
			continue
		}
		fact.withdrawn = true
		fact.availability = semreg.AvailabilityWithdrawn
		fact.quantity, fact.symbol, fact.boolean, fact.text = nil, nil, nil, nil
		fact.revision++
		w.facts[path] = fact
	}
}

func (w *world) noteConfigurationGaps(remote Remote) {
	grid, derivedMeter, phaseOnGridOrMeter := false, false, false
	for _, node := range w.nodes {
		if node.device != remote.Device || node.incomplete {
			continue
		}
		if node.kind == "resource.grid_connection" {
			grid = true
		}
		if node.kind == "resource.meter" && strings.HasSuffix(node.slot, "/meter") {
			derivedMeter = true
		}
		if node.kind != "resource.phase" {
			continue
		}
		parent := w.nodes[node.parentSlot]
		if parent.kind == "resource.meter" || parent.kind == "resource.grid_connection" {
			phaseOnGridOrMeter = true
		}
	}
	if grid && derivedMeter {
		w.note(remote.Device, "configuration_only_relation:infrastructure.relation.grid_connection_meter")
	}
	if phaseOnGridOrMeter {
		w.note(remote.Device, "configuration_only_relation:infrastructure.relation.circuit_phase")
	}
}

func (w *world) markElectricalIdentity(remote Remote) {
	for slot, node := range w.nodes {
		if node.device == remote.Device && node.identity {
			node.identity = false
			w.nodes[slot] = node
		}
	}
	best, ok := w.identityChoice(remote.Device)
	if !ok {
		return
	}
	best.identity = true
	best.ski = remote.SKI
	best.brand = remote.Brand
	best.vendor = remote.Vendor
	best.manufacturer = trim(remote.Manufacturer)
	best.serial = trim(remote.Serial)
	best.identityToken = trim(remote.IdentityToken)
	w.nodes[best.slot] = best
	w.identityMembers(best)
}

func (w *world) identityChoice(device string) (placedNode, bool) {
	rank := map[string]int{
		"resource.charging_station": 1, "resource.grid_connection": 2, "resource.pv_system": 3,
		"resource.storage_system": 4, "resource.inverter": 5, "resource.meter": 6, "resource.vehicle": 7,
	}
	var best placedNode
	bestRank := 0
	for _, node := range w.nodes {
		order, ok := rank[node.kind]
		if !ok || node.device != device || node.incomplete || node.retired {
			continue
		}
		if bestRank == 0 || order < bestRank || (order == bestRank && node.slot < best.slot) {
			best, bestRank = node, order
		}
	}
	return best, bestRank != 0
}

func (w *world) identitySlot(device string) string {
	for _, node := range w.nodes {
		if node.device == device && node.identity && !node.incomplete && !node.retired {
			return node.slot
		}
	}
	return ""
}

func electricalLinks(nodes map[string]placedNode) []builtLink {
	basis := string(semreg.RelationGateRule)
	var out []builtLink
	for _, node := range nodes {
		if node.incomplete {
			continue
		}
		parent, ok := nodes[node.parentSlot]
		if !ok || parent.incomplete {
			continue
		}
		switch {
		case node.kind == "resource.phase" && parent.kind == "resource.connector":
			out = append(out, builtLink{relation: "evse.relation.connector_phase", from: parent.slot, to: node.slot, basis: basis})
		case node.kind == "resource.phase" && parent.kind == "resource.inverter":
			out = append(out, builtLink{relation: "pv.relation.inverter_phase", from: parent.slot, to: node.slot, basis: basis})
		case node.kind == "resource.charging_session" && !node.retired && parent.kind == "resource.connector":
			out = append(out, builtLink{relation: "evse.relation.connector_session", from: parent.slot, to: node.slot, basis: basis})
		}
	}
	return out
}
