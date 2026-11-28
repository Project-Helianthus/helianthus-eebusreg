package inbound

import (
	"sort"
	"strconv"
	"strings"

	semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"
	"github.com/Project-Helianthus/helianthus-semreg/semreg/v1/driver"
	"github.com/Project-Helianthus/helianthus-semreg/semreg/v1/operation"
)

type builtLink struct {
	relation string
	from     string
	to       string
	basis    string
}

func packOf(id string) (string, string) {
	switch {
	case strings.HasPrefix(id, "infrastructure."):
		return "helianthus.pack.infrastructure", "1.0.0"
	case strings.HasPrefix(id, "evse."):
		return "helianthus.pack.evse", "1.0.0"
	case strings.HasPrefix(id, "pv."):
		return "helianthus.pack.pv", "1.0.0"
	case strings.HasPrefix(id, "storage."):
		return "helianthus.pack.storage", "1.1.0"
	default:
		return "helianthus.pack.thermal", "1.0.0"
	}
}

func thermalRef(id string) semreg.DefinitionRef {
	pack, version := packOf(id)
	return semreg.DefinitionRef{
		Pack:    semreg.PackRef{ID: semreg.DefinitionID(pack), Version: semreg.SemanticVersion(version)},
		ID:      semreg.DefinitionID(id),
		Version: semreg.SemanticVersion(version),
	}
}

func (scope Scope) ref(id semreg.ResourceID) driver.NodeRef {
	return driver.NodeRef{Bridge: driver.BridgeID(scope.Bridge), ID: id}
}

func (scope Scope) nodeID(node placedNode) semreg.ResourceID {
	return semreg.ResourceID(opaqueID("resource.", scope.Installation, scope.Instance, driverKind, node.kind, node.slot))
}

func (scope Scope) nodeKey(node placedNode) driver.NodeKey {
	return driver.NodeKey{
		Installation: scope.Installation,
		Driver:       driverKind,
		Instance:     driver.InstanceKey(scope.Instance),
		Kind:         semreg.DefinitionID(node.kind),
		Anchor:       driver.AnchorKey{Slot: node.slot},
	}
}

func deviceBinding(scope Scope, device string) (semreg.SourceID, semreg.SourceEpochID, semreg.NativeBindingID) {
	return semreg.SourceID(opaqueID("source.", scope.Installation, scope.Instance, device)),
		semreg.SourceEpochID(opaqueID("epoch.", scope.Installation, scope.Instance, device)),
		semreg.NativeBindingID(opaqueID("binding.", scope.Installation, scope.Instance, device))
}

func (w *world) links() []builtLink {
	systems := map[string]placedNode{}
	entities := map[string]placedNode{}
	for _, node := range w.nodes {
		if node.incomplete {
			continue
		}
		if node.kind == "resource.thermal_system" {
			systems[node.device] = node
		}
		if node.entity != "" {
			entities[node.device+"\n"+node.entity] = node
		}
	}
	var out []builtLink
	for _, node := range w.nodes {
		out = append(out, membership(node, systems, entities)...)
	}
	out = append(out, electricalLinks(w.nodes)...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].relation != out[j].relation {
			return out[i].relation < out[j].relation
		}
		if out[i].from != out[j].from {
			return out[i].from < out[j].from
		}
		return out[i].to < out[j].to
	})
	return dedupeLinks(out)
}

func membership(node placedNode, systems, entities map[string]placedNode) []builtLink {
	if node.incomplete {
		return nil
	}
	var out []builtLink
	if system, ok := systems[node.device]; ok && isSystemMember(node.kind) {
		out = append(out, builtLink{relation: "thermal.relation.system_member", from: system.slot, to: node.slot})
	}
	if node.parent == "" {
		return out
	}
	parent, ok := entities[node.device+"\n"+node.parent]
	if !ok {
		return out
	}
	switch {
	case node.kind == "resource.zone" && parent.kind == "resource.thermal_circuit":
		out = append(out, builtLink{relation: "thermal.relation.circuit_zone", from: parent.slot, to: node.slot})
	case node.kind == "resource.room" && parent.kind == "resource.thermal_circuit":
		out = append(out, builtLink{relation: "thermal.relation.circuit_room", from: parent.slot, to: node.slot})
	case node.kind == "resource.room" && parent.kind == "resource.zone":
		out = append(out, builtLink{relation: "thermal.relation.zone_room", from: parent.slot, to: node.slot})
	}
	return out
}

func isSystemMember(kind string) bool {
	return kind == "resource.zone" || kind == "resource.domestic_hot_water" || kind == "resource.thermal_circuit"
}

func dedupeLinks(links []builtLink) []builtLink {
	if len(links) == 0 {
		return nil
	}
	out := []builtLink{links[0]}
	for _, link := range links[1:] {
		last := out[len(out)-1]
		if last.relation == link.relation && last.from == link.from && last.to == link.to {
			continue
		}
		out = append(out, link)
	}
	return out
}

func (w *world) view(scope Scope) (Result, error) {
	result := Result{}
	ids := map[string]semreg.ResourceID{}
	for _, node := range w.nodes {
		if node.incomplete || node.kind == "" {
			continue
		}
		id := scope.nodeID(node)
		ids[node.slot] = id
		state := string(semreg.ResourceCurrent)
		if node.retired {
			state = string(semreg.ResourceRetired)
		}
		result.Nodes = append(result.Nodes, NodeView{Key: scope.nodeKey(node), ID: id, State: state})
	}
	sort.Slice(result.Nodes, func(i, j int) bool {
		if result.Nodes[i].Key.Kind != result.Nodes[j].Key.Kind {
			return result.Nodes[i].Key.Kind < result.Nodes[j].Key.Kind
		}
		return result.Nodes[i].Key.Anchor.Slot < result.Nodes[j].Key.Anchor.Slot
	})
	for _, link := range w.links() {
		view := LinkView{
			Key:      driver.LinkKey{Type: thermalRef(link.relation), From: scope.ref(ids[link.from]), To: scope.ref(ids[link.to])},
			FromSlot: link.from, ToSlot: link.to,
		}
		result.Links = append(result.Links, view)
	}
	result.Facts = w.factViews()
	result.Members = w.memberViews(scope)
	serials, matches, gaps, err := w.claims(scope, ids)
	if err != nil {
		return Result{}, err
	}
	result.Serials, result.Matches = serials, matches
	routes, err := w.routes(scope, ids)
	if err != nil {
		return Result{}, err
	}
	result.Routes = routes
	result.Gaps = uniqueSorted(append(gaps, w.noteGaps()...))
	return result, nil
}

func (w *world) factViews() []FactView {
	best := map[elementKey]int32{}
	for _, fact := range w.facts {
		if fact.withdrawn {
			continue
		}
		key := elementKey{fact.slot, fact.fact}
		if priority, ok := best[key]; !ok || fact.priority > priority {
			best[key] = fact.priority
		}
	}
	views := make([]FactView, 0, len(w.facts))
	for _, fact := range w.facts {
		views = append(views, factView(fact, best))
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Path < views[j].Path })
	return views
}

func factView(fact placedFact, best map[elementKey]int32) FactView {
	view := FactView{
		SubjectSlot: fact.slot, SubjectKind: fact.kind, FactID: fact.fact, Native: fact.native, NativeUnit: fact.unit,
		Availability: string(fact.availability), Freshness: string(fact.freshness), Priority: fact.priority,
		Path: fact.path, Carrier: fact.carrier, Withdrawn: fact.withdrawn, Generation: fact.generation, ObservedUnix: fact.observed,
		Authoritative: !fact.withdrawn && fact.priority == best[elementKey{fact.slot, fact.fact}],
	}
	if fact.text != nil {
		view.Text = *fact.text
	}
	if fact.boolean != nil {
		view.Flagged = true
		view.Flag = *fact.boolean
	}
	if fact.quantity != nil {
		view.Coefficient = fact.quantity.Number.Coefficient
		view.Exponent = fact.quantity.Number.Exponent10
		view.Unit = string(fact.quantity.Unit)
	}
	if fact.symbol != nil {
		view.SymbolNamespace = string(fact.symbol.Namespace)
		view.SymbolToken = fact.symbol.Token
		view.SymbolKnown = fact.symbol.Known
	}
	return view
}

func (w *world) memberViews(scope Scope) []MemberView {
	var views []MemberView
	for slot, locals := range w.extras {
		for local, value := range locals {
			views = append(views, MemberView{Member: driver.Member{
				Key: driver.MemberKey{
					Namespace: driver.NamespaceID(driverKind), Instance: driver.InstanceKey(scope.Instance),
					Node: driver.NodeID(slot), Local: driver.MemberID(local),
				},
				Schema: "native.text", Mode: "read", Scope: "instance",
			}, Value: value})
		}
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].Member.Key.Node != views[j].Member.Key.Node {
			return views[i].Member.Key.Node < views[j].Member.Key.Node
		}
		return views[i].Member.Key.Local < views[j].Member.Key.Local
	})
	return views
}

func (w *world) claims(scope Scope, ids map[string]semreg.ResourceID) ([]driver.ManufacturerSerialClaim, []driver.MatchClaim, []string, error) {
	var serials []driver.ManufacturerSerialClaim
	var matches []driver.MatchClaim
	var gaps []string
	slots := make([]string, 0, len(w.nodes))
	for slot := range w.nodes {
		slots = append(slots, slot)
	}
	sort.Strings(slots)
	for _, slot := range slots {
		node := w.nodes[slot]
		if !node.identity || node.incomplete {
			continue
		}
		gaps = append(gaps, identityGaps(node)...)
		if node.manufacturer != "" && node.serial != "" {
			proof, err := proofFor(node.manufacturer+"\n"+node.serial, "device.classification")
			if err != nil {
				return nil, nil, nil, err
			}
			serials = append(serials, driver.ManufacturerSerialClaim{Member: scope.ref(ids[slot]), Manufacturer: node.manufacturer, Serial: node.serial, Proof: []semreg.EvidenceRef{proof}})
		}
		scheme, ok := schemeForToken(node.identityToken)
		if !ok {
			continue
		}
		key, err := identityDigest(scheme, node.identityToken)
		if err != nil {
			return nil, nil, nil, err
		}
		proof, err := proofFor(scheme+"\n"+node.identityToken, "identity.token")
		if err != nil {
			return nil, nil, nil, err
		}
		matches = append(matches, driver.MatchClaim{Member: scope.ref(ids[slot]), Scheme: scheme, Key: key, Proof: []semreg.EvidenceRef{proof}})
	}
	return serials, matches, gaps, nil
}

func identityGaps(node placedNode) []string {
	var gaps []string
	if node.ski != "" {
		if _, ok := schemeForToken(node.ski); !ok {
			gaps = append(gaps, "ship_ski_scheme_unlisted:"+node.device)
		}
	}
	switch {
	case node.manufacturer == "" && node.serial == "":
		gaps = append(gaps, "identification_missing:"+node.device)
	case node.manufacturer == "":
		gaps = append(gaps, "manufacturer_missing:"+node.device)
	case node.serial == "":
		gaps = append(gaps, "serial_missing:"+node.device)
	}
	if node.identityToken != "" {
		if _, ok := schemeForToken(node.identityToken); !ok {
			gaps = append(gaps, "identity_token_unlisted:"+node.device)
		}
	}
	return gaps
}

func (w *world) routes(scope Scope, ids map[string]semreg.ResourceID) ([]driver.SettingRoute, error) {
	var routes []driver.SettingRoute
	paths := make([]string, 0, len(w.facts))
	for path := range w.facts {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		fact := w.facts[path]
		if fact.withdrawn || fact.operation == "" || fact.feature == "" || fact.generation == 0 {
			continue
		}
		sourceID, epoch, _ := deviceBinding(scope, fact.device)
		element := driver.Element{Subject: scope.ref(ids[fact.slot]), Key: factKey(fact.fact)}
		producer := scope.ref(ids[deviceSlot(fact.device)])
		source := driver.SourceRef{For: element, Producer: producer, PathKey: fact.path}
		routes = append(routes, driver.SettingRoute{
			For: element, Operation: thermalRef(fact.operation), Owner: source,
			Route: operation.Route{
				CapabilityInstance: semreg.CapabilityInstanceID(opaqueID("capability.", fact.operation, fact.slot)),
				ServiceInstance:    semreg.ServiceInstanceID(opaqueID("service.", fact.slot)),
				BindingID:          semreg.NativeBindingID(fact.feature),
				SourceID:           sourceID,
				SourceEpochID:      epoch,
				DriverGeneration:   semreg.Uint64(strconv.FormatUint(fact.generation, 10)),
			},
		})
	}
	return routes, nil
}

func factKey(id string) semreg.FactKey {
	pack, version := packOf(id)
	return semreg.FactKey{PackID: semreg.DefinitionID(pack), PackVersion: semreg.SemanticVersion(version), FactID: semreg.DefinitionID(id), Dimensions: []semreg.Dimension{}}
}

func (w *world) keepWithdrawals(previous []builtLink) {
	w.withdrawn = disappeared(previous, w.links())
}

func disappeared(previous, current []builtLink) []builtLink {
	seen := map[string]struct{}{}
	for _, link := range current {
		seen[linkID(link)] = struct{}{}
	}
	var gone []builtLink
	for _, link := range previous {
		if _, ok := seen[linkID(link)]; !ok {
			gone = append(gone, link)
		}
	}
	sort.Slice(gone, func(i, j int) bool {
		if gone[i].relation != gone[j].relation {
			return gone[i].relation < gone[j].relation
		}
		if gone[i].from != gone[j].from {
			return gone[i].from < gone[j].from
		}
		return gone[i].to < gone[j].to
	})
	return gone
}

func linkID(link builtLink) string {
	return link.relation + "\n" + link.from + "\n" + link.to
}

func (w *world) noteGaps() []string {
	gaps := make([]string, 0, len(w.notes))
	for _, item := range w.notes {
		if item.device == "" {
			gaps = append(gaps, item.text)
			continue
		}
		gaps = append(gaps, item.device+":"+item.text)
	}
	return gaps
}

func uniqueSorted(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	sort.Strings(values)
	out := []string{values[0]}
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}
