package inbound

import (
	"math/big"
	"strings"

	semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"
)

type observationValue struct {
	quantity *semreg.Quantity
	symbol   *semreg.Symbol
	boolean  *bool
	text     *string
}

func (w *world) ingestElectricalEntity(remote Remote, entity Entity, updated map[string]struct{}) error {
	var connected *Reading
	for i := range entity.Readings {
		reading := entity.Readings[i]
		if reading.Field == "connected" {
			copy := reading
			connected = &copy
			continue
		}
		if err := w.applyElectrical(remote, entity, reading, updated); err != nil {
			return err
		}
	}
	if connected == nil {
		return nil
	}
	return w.applyConnected(remote, entity, *connected, updated)
}

func (w *world) applyElectrical(remote Remote, entity Entity, reading Reading, updated map[string]struct{}) error {
	switch reading.Field {
	case "operatingState":
		return w.projectOperating(remote, entity, reading, updated)
	case "chargeState":
		return w.projectCharge(remote, entity, reading, updated)
	case "sleepMode":
		return w.projectSleep(remote, entity, reading, updated)
	}
	if manufacturerField(reading.Field) {
		return w.applyManufacturer(remote, entity, reading, updated)
	}
	row, ok := findElectricalRow(entity.Type, reading.Field)
	if !ok {
		if reading.Field != "" {
			w.note(remote.Device, "field:"+reading.Field)
		}
		return nil
	}
	return w.applyRow(remote, entity, row, reading, updated)
}

func (w *world) readyReading(remote Remote, reading Reading) (uint64, int32, bool, error) {
	generation := reading.Generation
	if generation == 0 {
		generation = remote.Generation
	}
	if generation == 0 {
		w.note(remote.Device, "generation_missing:"+reading.Field)
		return 0, 0, false, nil
	}
	if _, stopped := w.stopped[remote.SKI][generation]; stopped || reading.Text == "" {
		return 0, 0, false, nil
	}
	priority, err := priorityOf(reading.Rank)
	if err != nil {
		return 0, 0, false, err
	}
	return generation, priority, true, nil
}

func (w *world) applyRow(remote Remote, entity Entity, row elecRow, reading Reading, updated map[string]struct{}) error {
	generation, priority, ok, err := w.readyReading(remote, reading)
	if err != nil || !ok {
		return err
	}
	if row.mode == "member" {
		w.storeMember(remote, entity, row, reading)
		return nil
	}
	if !unitAccepted(row, reading.Unit) {
		w.note(remote.Device, "unit:"+reading.Field)
		return nil
	}
	node, phase, ok := w.electricalSubject(remote, entity, row, reading)
	if !ok {
		w.note(remote.Device, "subject:"+reading.Field)
		return nil
	}
	value, ok := w.observationOf(remote, row, reading)
	if !ok {
		return nil
	}
	pathEntity := entityText(entity.Address)
	if row.target == "session" {
		pathEntity = node.slot
	}
	phasePath := ""
	if row.phase {
		phasePath = phase
	}
	w.storeObservation(remote, reading, node, row.fact, pathEntity, phasePath, generation, priority, value, updated)
	if reading.Writable {
		w.addWrite(node.slot, remote, reading, entityText(entity.Address))
	}
	noteSign(w, remote.Device, row.fact)
	return nil
}

func (w *world) electricalSubject(remote Remote, entity Entity, row elecRow, reading Reading) (placedNode, string, bool) {
	self, selfOK := w.liveNode(entitySlot(remote.Device, entityText(entity.Address)))
	switch row.target {
	case "self":
		return self, "", selfOK
	case "phase":
		if !selfOK {
			return placedNode{}, "", false
		}
		node, ok := w.ensurePhase(remote, self, reading.Phase)
		return node, reading.Phase, ok
	case "meter":
		if !selfOK || self.kind != "resource.grid_connection" {
			return placedNode{}, "", false
		}
		node, ok := w.ensureGridMeter(remote, self)
		return node, "", ok
	case "vehicle":
		if selfOK && self.kind == "resource.vehicle" {
			return self, "", true
		}
		node, ok := w.oneKind(remote.Device, "resource.vehicle")
		return node, "", ok
	case "connector":
		node, ok := w.oneKind(remote.Device, "resource.connector")
		return node, "", ok
	case "session":
		node, ok := w.oneKind(remote.Device, "resource.charging_session")
		return node, "", ok
	default:
		return placedNode{}, "", false
	}
}

func (w *world) storeMember(remote Remote, entity Entity, row elecRow, reading Reading) {
	node, ok := w.liveNode(entitySlot(remote.Device, entityText(entity.Address)))
	if !ok {
		w.note(remote.Device, "subject:"+reading.Field)
		return
	}
	w.extra(node.slot, row.member, reading.Text)
}

func unitAccepted(row elecRow, unit string) bool {
	if unit == "" || unit == row.native {
		return true
	}
	return row.mode == "percent" && unit == "pct"
}

func (w *world) observationOf(remote Remote, row elecRow, reading Reading) (observationValue, bool) {
	switch row.mode {
	case "symbol":
		return w.symbolValue(remote, row, reading)
	case "boolean":
		return booleanValue(w, remote, reading)
	case "text":
		value := reading.Text
		return observationValue{text: &value}, true
	case "percent":
		return w.scaledValue(remote, row, reading, 1, 100)
	case "watt_hour":
		return w.scaledValue(remote, row, reading, 3600, 1)
	case "number":
		return w.numberValue(remote, row, reading)
	default:
		w.note(remote.Device, "field:"+reading.Field)
		return observationValue{}, false
	}
}

func (w *world) symbolValue(remote Remote, row elecRow, reading Reading) (observationValue, bool) {
	token, ok := row.symbols[reading.Text]
	if !ok {
		w.note(remote.Device, "projection_loss:"+row.fact+":"+reading.Text)
		return observationValue{}, false
	}
	symbol := semreg.Symbol{Namespace: semreg.DefinitionID(row.fact), Token: token, Known: true}
	return observationValue{symbol: &symbol}, true
}

func booleanValue(w *world, remote Remote, reading Reading) (observationValue, bool) {
	switch reading.Text {
	case "true":
		value := true
		return observationValue{boolean: &value}, true
	case "false":
		value := false
		return observationValue{boolean: &value}, true
	default:
		w.note(remote.Device, "boolean:"+reading.Field)
		return observationValue{}, false
	}
}

func (w *world) numberValue(remote Remote, row elecRow, reading Reading) (observationValue, bool) {
	if w.rejectSigned(remote, row, reading) {
		return observationValue{}, false
	}
	number, err := parseDecimal(reading.Text)
	if err != nil {
		w.note(remote.Device, "decimal:"+reading.Field)
		return observationValue{}, false
	}
	return observationValue{quantity: &semreg.Quantity{Number: number, Unit: semreg.DefinitionID(row.unit)}}, true
}

func (w *world) scaledValue(remote Remote, row elecRow, reading Reading, num, den int64) (observationValue, bool) {
	if w.rejectSigned(remote, row, reading) {
		return observationValue{}, false
	}
	number, err := scaleDecimal(reading.Text, num, den)
	if err != nil {
		w.note(remote.Device, "decimal:"+reading.Field)
		return observationValue{}, false
	}
	if row.ratio && !ratioAdmitted(number) {
		w.note(remote.Device, "ratio_out_of_range:"+reading.Field)
		return observationValue{}, false
	}
	return observationValue{quantity: &semreg.Quantity{Number: number, Unit: semreg.DefinitionID(row.unit)}}, true
}

func (w *world) rejectSigned(remote Remote, row elecRow, reading Reading) bool {
	negative, ok := negativeDecimal(reading.Text)
	if !ok {
		w.note(remote.Device, "decimal:"+reading.Field)
		return true
	}
	if !negative || !row.nonnegative {
		return false
	}
	if strings.HasSuffix(row.fact, ".current") {
		w.note(remote.Device, "signed_current_not_admitted:"+reading.Phase)
		return true
	}
	if row.mode == "watt_hour" {
		w.note(remote.Device, "negative_energy:"+reading.Field)
		return true
	}
	w.note(remote.Device, "negative_quantity:"+reading.Field)
	return true
}

func negativeDecimal(text string) (bool, bool) {
	number, err := parseDecimal(text)
	if err != nil {
		return false, false
	}
	return strings.HasPrefix(number.Coefficient, "-"), true
}

func ratioAdmitted(number semreg.Decimal) bool {
	rat, err := decimalRat(number)
	if err != nil {
		return false
	}
	return rat.Sign() >= 0 && rat.Cmp(big.NewRat(1, 1)) <= 0
}

func noteSign(w *world, device, fact string) {
	switch fact {
	case "pv.vapd.ac_power_total", "pv.vapd.ac_yield_total", "storage.system.ac_power":
		w.note(device, "sign_reference_uninstantiated:"+fact)
	}
}

func (w *world) storeObservation(remote Remote, reading Reading, node placedNode, factID, pathEntity, phase string, generation uint64, priority int32, value observationValue, updated map[string]struct{}) {
	fact := placedFact{
		path: sourcePath(reading.Rank, remote.Device, pathEntity, factID, phase), ski: remote.SKI, device: remote.Device,
		slot: node.slot, kind: node.kind, fact: factID, rank: rankName(reading.Rank), priority: priority, generation: generation,
		carrier: reading.Carrier, native: reading.Text, unit: reading.Unit, availability: semreg.AvailabilityAvailable,
		freshness: semreg.FreshnessFresh, observed: reading.ObservedUnix, revision: 1, policy: "policy.electrical",
		quantity: value.quantity, symbol: value.symbol, boolean: value.boolean, text: value.text,
	}
	if previous, exists := w.facts[fact.path]; exists {
		fact.revision = previous.revision
		if !sameObservation(previous, fact) {
			fact.revision++
		}
	}
	w.facts[fact.path] = fact
	updated[fact.path] = struct{}{}
}

func (w *world) projectOperating(remote Remote, entity Entity, reading Reading, updated map[string]struct{}) error {
	generation, priority, ok, err := w.readyReading(remote, reading)
	if err != nil || !ok {
		return err
	}
	node, ok := w.liveNode(entitySlot(remote.Device, entityText(entity.Address)))
	if !ok || node.kind != "resource.evse" {
		w.note(remote.Device, "subject:"+reading.Field)
		return nil
	}
	path := entityText(entity.Address)
	w.projectSymbol(remote, reading, node, path, "evse.status.fault", faultSymbols, generation, priority, updated)
	w.projectSymbol(remote, reading, node, path, "evse.status.readiness", readinessSymbols, generation, priority, updated)
	return nil
}

func (w *world) projectCharge(remote Remote, _ Entity, reading Reading, updated map[string]struct{}) error {
	generation, priority, ok, err := w.readyReading(remote, reading)
	if err != nil || !ok {
		return err
	}
	node, ok := w.oneKind(remote.Device, "resource.connector")
	if !ok {
		w.note(remote.Device, "subject:"+reading.Field)
		return nil
	}
	w.projectSymbol(remote, reading, node, node.slot, "evse.status.charging", chargeSymbols, generation, priority, updated)
	return nil
}

func (w *world) projectSymbol(remote Remote, reading Reading, node placedNode, pathEntity, factID string, table map[string]string, generation uint64, priority int32, updated map[string]struct{}) {
	token, ok := table[reading.Text]
	if !ok {
		w.note(remote.Device, "projection_loss:"+factID+":"+reading.Text)
		return
	}
	symbol := semreg.Symbol{Namespace: semreg.DefinitionID(factID), Token: token, Known: true}
	w.storeObservation(remote, reading, node, factID, pathEntity, "", generation, priority, observationValue{symbol: &symbol}, updated)
}

func (w *world) projectSleep(remote Remote, entity Entity, reading Reading, updated map[string]struct{}) error {
	generation, priority, ok, err := w.readyReading(remote, reading)
	if err != nil || !ok {
		return err
	}
	node, ok := w.liveNode(entitySlot(remote.Device, entityText(entity.Address)))
	if !ok || node.kind != "resource.vehicle" {
		w.note(remote.Device, "subject:"+reading.Field)
		return nil
	}
	token := ""
	switch reading.Text {
	case "true":
		token = "sleeping"
	case "false":
		token = "awake"
	default:
		w.note(remote.Device, "projection_loss:evse.vehicle.sleep:"+reading.Text)
		return nil
	}
	symbol := semreg.Symbol{Namespace: "evse.vehicle.sleep", Token: token, Known: true}
	w.storeObservation(remote, reading, node, "evse.vehicle.sleep", entityText(entity.Address), "", generation, priority, observationValue{symbol: &symbol}, updated)
	return nil
}

func (w *world) applyConnected(remote Remote, _ Entity, reading Reading, updated map[string]struct{}) error {
	generation, priority, ok, err := w.readyReading(remote, reading)
	if err != nil || !ok {
		return err
	}
	node, ok := w.oneKind(remote.Device, "resource.connector")
	if !ok {
		w.note(remote.Device, "subject:"+reading.Field)
		return nil
	}
	token := ""
	switch reading.Text {
	case "true":
		token = "connected"
	case "false":
		token = "disconnected"
	default:
		w.note(remote.Device, "projection_loss:evse.status.connection:"+reading.Text)
		return nil
	}
	symbol := semreg.Symbol{Namespace: "evse.status.connection", Token: token, Known: true}
	w.storeObservation(remote, reading, node, "evse.status.connection", node.slot, "", generation, priority, observationValue{symbol: &symbol}, updated)
	if reading.Text == "false" {
		idle := semreg.Symbol{Namespace: "evse.status.charging", Token: "idle", Known: true}
		w.storeObservation(remote, reading, node, "evse.status.charging", node.slot, "", generation, priority, observationValue{symbol: &idle}, updated)
	}
	return nil
}

func (w *world) applyManufacturer(remote Remote, entity Entity, reading Reading, updated map[string]struct{}) error {
	generation, priority, ok, err := w.readyReading(remote, reading)
	if err != nil || !ok {
		return err
	}
	factID, listed := manufacturerID(entity.Type, reading.Field)
	if !listed {
		w.note(remote.Device, "use_case_row_unlisted:"+reading.Field)
		if node, live := w.liveNode(entitySlot(remote.Device, entityText(entity.Address))); live {
			w.extra(node.slot, snakeField(reading.Field), reading.Text)
		}
		return nil
	}
	node, ok := w.oneKind(remote.Device, "resource.connector")
	if !ok {
		w.note(remote.Device, "subject:"+reading.Field)
		return nil
	}
	value := reading.Text
	w.storeObservation(remote, reading, node, factID, node.slot, "", generation, priority, observationValue{text: &value}, updated)
	return nil
}

func manufacturerField(field string) bool {
	switch snakeField(field) {
	case "device_name", "device_code", "serial_number", "software_revision", "hardware_revision",
		"vendor_name", "vendor_code", "brand_name", "manufacturer_label", "manufacturer_description":
		return true
	default:
		return false
	}
}

func manufacturerID(entityType, field string) (string, bool) {
	snake := snakeField(field)
	if snake == "manufacturer_description" {
		if entityType == "EV" {
			return "evse.evcc.manufacturer_description", true
		}
		return "", false
	}
	switch snake {
	case "device_name", "device_code", "serial_number", "software_revision", "hardware_revision",
		"vendor_name", "vendor_code", "brand_name", "manufacturer_label":
	default:
		return "", false
	}
	switch entityType {
	case "EVSE":
		return "evse.evsecc." + snake, true
	case "EV":
		return "evse.evcc." + snake, true
	default:
		return "", false
	}
}

func snakeField(field string) string {
	var b strings.Builder
	for i, r := range field {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteByte(byte(r - 'A' + 'a'))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
