package inbound

import semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"

func (w *world) ingestReading(remote Remote, entityType, entity string, reading Reading, updated map[string]struct{}) error {
	if local, ok := namespaceLocal(reading.Field); ok {
		if slot := w.memberSlot(remote, entity); slot != "" && reading.Text != "" {
			w.extra(slot, local, reading.Text)
		}
		if reading.Writable {
			w.addWrite(w.memberSlot(remote, entity), remote, reading, entity)
		}
		return nil
	}
	row, ok := findRow(entityType, reading.Field)
	if !ok {
		if reading.Field != "" {
			w.note(remote.Device, "field:"+reading.Field)
			if reading.Writable {
				w.addWrite(w.memberSlot(remote, entity), remote, reading, entity)
			}
		}
		return nil
	}
	generation := reading.Generation
	if generation == 0 {
		generation = remote.Generation
	}
	if generation == 0 {
		w.note(remote.Device, "generation_missing:"+reading.Field)
		return nil
	}
	if _, stopped := w.stopped[remote.SKI][generation]; stopped || reading.Text == "" {
		return nil
	}
	slot, kind, ok := w.subject(remote, row, entity)
	if !ok {
		w.note(remote.Device, "subject:"+reading.Field)
		return nil
	}
	priority, err := priorityOf(reading.Rank)
	if err != nil {
		return err
	}
	fact, err := w.observation(remote, row, reading, slot, kind, entity, generation, priority)
	if err != nil {
		w.note(remote.Device, "temperature_unit:"+reading.Field)
		return nil
	}
	if fact.fact == "" {
		return nil
	}
	if previous, exists := w.facts[fact.path]; exists {
		fact.revision = previous.revision
		if !sameObservation(previous, fact) {
			fact.revision++
		}
	}
	w.facts[fact.path] = fact
	updated[fact.path] = struct{}{}
	return nil
}

func (w *world) observation(remote Remote, row heatingRow, reading Reading, slot, kind, entity string, generation uint64, priority int32) (placedFact, error) {
	fact := placedFact{
		path: sourcePath(reading.Rank, remote.Device, entity, row.Fact, ""), ski: remote.SKI, device: remote.Device, slot: slot, kind: kind,
		fact: row.Fact, rank: rankName(reading.Rank), priority: priority, generation: generation, carrier: reading.Carrier,
		native: reading.Text, unit: reading.Unit, availability: semreg.AvailabilityAvailable, freshness: semreg.FreshnessFresh,
		observed: reading.ObservedUnix, revision: 1,
	}
	if row.Kelvin {
		number, err := convertTemperature(row, reading.Text, reading.Unit)
		if err != nil {
			return placedFact{}, err
		}
		fact.quantity = &semreg.Quantity{Number: number, Unit: "unit.kelvin"}
	} else {
		symbol, known := convertSymbol(row, reading.Text)
		fact.symbol = &symbol
		if !known {
			w.note(remote.Device, "unknown_mode:"+reading.Text)
		}
	}
	if reading.Writable && row.Operation != "" {
		fact.operation = row.Operation
		fact.feature = reading.Feature
		if reading.Feature == "" {
			w.note(remote.Device, "feature_missing:"+row.Operation)
			w.addWrite(slot, remote, reading, entity)
		}
	} else if reading.Writable {
		w.addWrite(slot, remote, reading, entity)
	}
	return fact, nil
}

func (w *world) memberSlot(remote Remote, entity string) string {
	if entity == "" {
		slot := deviceSlot(remote.Device)
		if node, ok := w.nodes[slot]; ok && !node.incomplete {
			return slot
		}
		return ""
	}
	slot := entitySlot(remote.Device, entity)
	if node, ok := w.nodes[slot]; ok && !node.incomplete {
		return slot
	}
	if node, ok := w.nodes[deviceSlot(remote.Device)]; ok && !node.incomplete {
		return node.slot
	}
	return ""
}

func (w *world) subject(remote Remote, row heatingRow, entity string) (string, string, bool) {
	if row.Subject == "system" || entity == "" {
		node, ok := w.nodes[deviceSlot(remote.Device)]
		if !ok || node.incomplete {
			return "", "", false
		}
		return node.slot, node.kind, true
	}
	node, ok := w.nodes[entitySlot(remote.Device, entity)]
	if !ok || node.incomplete {
		return "", "", false
	}
	return node.slot, node.kind, true
}

func sameObservation(previous, next placedFact) bool {
	return previous.native == next.native && previous.unit == next.unit && previous.carrier == next.carrier &&
		previous.generation == next.generation && previous.observed == next.observed &&
		previous.availability == next.availability && previous.freshness == next.freshness &&
		previous.withdrawn == next.withdrawn && previous.operation == next.operation && previous.feature == next.feature &&
		sameQuantity(previous.quantity, next.quantity) && sameSymbol(previous.symbol, next.symbol) &&
		sameBool(previous.boolean, next.boolean) && sameText(previous.text, next.text)
}

func sameBool(left, right *bool) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameText(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameQuantity(left, right *semreg.Quantity) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Unit == right.Unit && left.Number == right.Number
}

func sameSymbol(left, right *semreg.Symbol) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
