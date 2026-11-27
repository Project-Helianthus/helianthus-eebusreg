package inbound

import semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"

type world struct {
	nodes   map[string]placedNode
	facts   map[string]placedFact
	extras  map[string]map[string]string
	notes   []note
	stopped map[string]map[uint64]struct{}
	revoked map[string]struct{}
}

type note struct {
	device string
	text   string
}

type placedNode struct {
	kind          string
	slot          string
	device        string
	entity        string
	entityType    string
	parent        string
	ski           string
	name          string
	brand         string
	vendor        string
	manufacturer  string
	serial        string
	identityToken string
	incomplete    bool
	missing       string
}

type placedFact struct {
	path         string
	ski          string
	device       string
	slot         string
	kind         string
	fact         string
	rank         string
	priority     int32
	generation   uint64
	carrier      string
	native       string
	unit         string
	quantity     *semreg.Quantity
	symbol       *semreg.Symbol
	availability semreg.Availability
	freshness    semreg.Freshness
	observed     int64
	withdrawn    bool
	operation    string
	feature      string
	revision     uint64
}

func freshWorld() *world {
	return &world{
		nodes:   map[string]placedNode{},
		facts:   map[string]placedFact{},
		extras:  map[string]map[string]string{},
		stopped: map[string]map[uint64]struct{}{},
		revoked: map[string]struct{}{},
	}
}

func (w *world) clone() *world {
	next := freshWorld()
	for slot, node := range w.nodes {
		next.nodes[slot] = node
	}
	for path, fact := range w.facts {
		next.facts[path] = copyFact(fact)
	}
	for slot, locals := range w.extras {
		next.extras[slot] = map[string]string{}
		for local, value := range locals {
			next.extras[slot][local] = value
		}
	}
	next.notes = append([]note(nil), w.notes...)
	for ski, generations := range w.stopped {
		next.stopped[ski] = map[uint64]struct{}{}
		for generation := range generations {
			next.stopped[ski][generation] = struct{}{}
		}
	}
	for ski := range w.revoked {
		next.revoked[ski] = struct{}{}
	}
	return next
}

func copyFact(fact placedFact) placedFact {
	if fact.quantity != nil {
		quantity := *fact.quantity
		fact.quantity = &quantity
	}
	if fact.symbol != nil {
		symbol := *fact.symbol
		fact.symbol = &symbol
	}
	return fact
}

func (w *world) extra(slot, local, value string) {
	if w.extras[slot] == nil {
		w.extras[slot] = map[string]string{}
	}
	w.extras[slot][local] = value
}

func (w *world) note(device, text string) {
	w.notes = append(w.notes, note{device: device, text: text})
}

func (w *world) clearDevice(device string) {
	for slot, node := range w.nodes {
		if node.device != device {
			continue
		}
		delete(w.extras, slot)
		if node.incomplete {
			delete(w.nodes, slot)
		}
	}
	kept := w.notes[:0]
	for _, item := range w.notes {
		if item.device != device {
			kept = append(kept, item)
		}
	}
	w.notes = append([]note(nil), kept...)
}
