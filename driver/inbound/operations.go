package inbound

import (
	"context"
	"errors"
	"sort"
	"strings"

	semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"
	"github.com/Project-Helianthus/helianthus-semreg/semreg/v1/driver"
	"github.com/Project-Helianthus/helianthus-semreg/semreg/v1/graph"
	"github.com/Project-Helianthus/helianthus-semreg/semreg/v1/operation"
)

type writeCall struct {
	slot, device, ski, field, feature, entity string
	generation                                uint64
}

type commandRoute struct {
	token, operation, feature, slot, device, ski, entity, path string
	generation                                                 uint64
}

type callTarget struct {
	ski, operation, feature, entity, path, kind string
	generation                                  uint64
}

type opSpec struct {
	argument, capability, service string
}

var operationSpecs = map[string]opSpec{
	"thermal.room.operation_set_temperature":       {"thermal.room.setpoint_temperature_setpoint", "thermal.room.capability_set_temperature", "thermal.service.room"},
	"thermal.room.operation_set_zone_mode":         {"thermal.room.mode_zone", "thermal.room.capability_set_zone_mode", "thermal.service.room"},
	"thermal.operation.set_dhw_temperature":        {"thermal.setpoint.dhw_temperature_setpoint", "thermal.capability.set_dhw_temperature", "thermal.service.dhw"},
	"thermal.operation.set_dhw_mode":               {"thermal.mode.dhw", "thermal.capability.set_dhw_mode", "thermal.service.dhw"},
	"thermal.operation.set_temperature":            {"thermal.setpoint.temperature_setpoint", "thermal.capability.set_temperature", "thermal.service.zone"},
	"thermal.operation.set_zone_mode":              {"thermal.mode.zone", "thermal.capability.set_zone_mode", "thermal.service.zone"},
	"thermal.operation.set_overrun_one_time_dhw":   {"thermal.overrun.one_time_dhw", "thermal.capability.set_overrun_one_time_dhw", "thermal.service.system"},
	"evse.operation.set_allocated_current":         {"evse.limit.allocated_current", "evse.capability.set_allocated_current", "evse.service.connector"},
	"evse.operation.set_recommended_current_phase": {"evse.limit.recommended_current_phase", "evse.capability.set_recommended_current_phase", "evse.service.phase"},
	"energy.operation.modify_series_slot":          {"energy.series.slot_power_nominal", "energy.capability.modify_series_slot", "energy.service.subject"},
	"energy.operation.write_incentive_slot":        {"energy.incentive.incentive_value", "energy.capability.write_incentive_slot", "energy.service.tariff"},
}

func operationSpec(id string) (opSpec, bool) {
	spec, ok := operationSpecs[id]
	return spec, ok
}

func operationArgument(id string) string {
	spec, ok := operationSpec(id)
	if !ok {
		return ""
	}
	return spec.argument
}

func featureBinding(scope Scope, device, operationID, feature, slot string) semreg.NativeBindingID {
	return semreg.NativeBindingID(opaqueID("feature.", scope.Installation, scope.Instance, device, operationID, feature, slot))
}

func subjectBinding(scope Scope, device, slot string) semreg.NativeBindingID {
	return semreg.NativeBindingID(opaqueID("subject.", scope.Installation, scope.Instance, device, slot))
}

func factSubject(id string) (semreg.GraphFactSubject, bool) {
	catalog, err := graph.Embedded()
	if err != nil {
		return semreg.GraphFactSubject{}, false
	}
	pack, version := packOf(id)
	return catalog.FactSubject(semreg.PackRef{ID: semreg.DefinitionID(pack), Version: semreg.SemanticVersion(version)}, semreg.DefinitionID(id))
}

func factDimension(id string) string {
	subject, ok := factSubject(id)
	if !ok || !subject.Resolved || subject.Dimension == "" || subject.Disposition != semreg.GraphMember {
		return ""
	}
	return string(subject.Dimension)
}

func catalogFact(id string) bool {
	subject, ok := factSubject(id)
	return ok && subject.Disposition == semreg.GraphMember
}

func serviceAdmitted(serviceID string, node placedNode) bool {
	catalog, err := graph.Embedded()
	if err != nil {
		return false
	}
	kind, classes, ok := catalog.ServiceSubject(thermalRef(serviceID))
	if !ok {
		return false
	}
	if kind != "" {
		return string(kind) == node.kind
	}
	for _, class := range classes {
		if class == node.anchor {
			return true
		}
	}
	return false
}

func (w *world) addWrite(slot string, remote Remote, reading Reading, entity string) {
	if slot == "" || reading.Field == "" {
		return
	}
	if w.writes == nil {
		w.writes = map[string]writeCall{}
	}
	generation := reading.Generation
	if generation == 0 {
		generation = remote.Generation
	}
	local := "write." + reading.Field
	w.writes[slot+"\n"+local] = writeCall{
		slot: slot, device: remote.Device, ski: remote.SKI, field: reading.Field,
		feature: reading.Feature, entity: entity, generation: generation,
	}
}

func (w *world) clearOperations(device string) {
	for key, call := range w.writes {
		if call.device == device {
			delete(w.writes, key)
		}
	}
	kept := w.commands[:0]
	for _, command := range w.commands {
		if command.device != device {
			kept = append(kept, command)
		}
	}
	w.commands = append([]commandRoute(nil), kept...)
}

func (w *world) dropRemote(ski string) {
	for key, call := range w.writes {
		if call.ski == ski {
			delete(w.writes, key)
		}
	}
	kept := w.commands[:0]
	for _, command := range w.commands {
		if command.ski != ski {
			kept = append(kept, command)
		}
	}
	w.commands = append([]commandRoute(nil), kept...)
}

func (w *world) admitCommands(remote Remote) {
	if !remote.Ready || remote.Generation == 0 {
		return
	}
	for _, token := range remote.Commands {
		switch token {
		case "overload_protection":
			node, ok := w.oneKind(remote.Device, "resource.connector")
			if !ok {
				w.note(remote.Device, "command_subject_missing:"+token)
				continue
			}
			w.addCommand(remote, token, "evse.operation.set_allocated_current", "LoadControl", node)
		case "self_consumption":
			nodes := w.kindNodes(remote.Device, "resource.phase")
			if len(nodes) == 0 {
				w.note(remote.Device, "command_subject_missing:"+token)
				continue
			}
			for _, node := range nodes {
				w.addCommand(remote, token, "evse.operation.set_recommended_current_phase", "LoadControl", node)
			}
		case "coordinated_charging":
			nodes := w.kindNodes(remote.Device, "resource.charging_session")
			if len(nodes) == 0 {
				node, ok := w.oneKind(remote.Device, "resource.connector")
				if !ok {
					w.note(remote.Device, "command_subject_missing:"+token)
					continue
				}
				nodes = []placedNode{node}
			}
			for _, node := range nodes {
				w.addCommand(remote, token, "energy.operation.modify_series_slot", "PowerLimits", node)
				w.addCommand(remote, token, "energy.operation.write_incentive_slot", "IncentiveTable", node)
			}
		default:
			w.note(remote.Device, "command_unknown:"+token)
		}
	}
}

func (w *world) addCommand(remote Remote, token, operationID, feature string, node placedNode) {
	if _, ok := operationSpec(operationID); !ok {
		return
	}
	path := "command/" + token + "/" + operationID + "/" + node.slot
	for _, existing := range w.commands {
		if existing.path == path {
			return
		}
	}
	w.commands = append(w.commands, commandRoute{
		token: token, operation: operationID, feature: feature, slot: node.slot,
		device: remote.Device, ski: remote.SKI, entity: node.entity, path: path, generation: remote.Generation,
	})
}

func (w *world) kindNodes(device, kind string) []placedNode {
	var nodes []placedNode
	for _, node := range w.nodes {
		if node.device != device || node.kind != kind || node.incomplete || node.retired {
			continue
		}
		nodes = append(nodes, node)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].slot < nodes[j].slot })
	return nodes
}

func (w *world) indexTargets() {
	w.targets = map[string]callTarget{}
	for _, fact := range w.facts {
		if fact.withdrawn || fact.operation == "" || fact.feature == "" || fact.generation == 0 {
			continue
		}
		w.targets[fact.path] = callTarget{
			ski: fact.ski, operation: fact.operation, feature: fact.feature, entity: entityOfPath(fact.path),
			path: fact.path, kind: "semreg", generation: fact.generation,
		}
	}
	for _, command := range w.commands {
		w.targets[command.path] = callTarget{
			ski: command.ski, operation: command.operation, feature: command.feature, entity: command.entity,
			path: command.path, kind: "semreg", generation: command.generation,
		}
	}
}

func entityOfPath(path string) string {
	const mark = "/entity/"
	at := strings.Index(path, mark)
	if at < 0 {
		return ""
	}
	rest := path[at+len(mark):]
	if end := strings.Index(rest, "/fact/"); end >= 0 {
		return rest[:end]
	}
	return rest
}

type attachedBatch struct {
	facts        []semreg.FactCandidate
	bindings     []semreg.NativeBinding
	services     []semreg.ServiceInstance
	capabilities []semreg.CapabilityInstance
	links        []semreg.IdentityLink
}

func (a attachedBatch) apply(batch *semreg.PublicationBatch) {
	asset, source, epoch, generation := batch.AssetID, batch.SourceID, batch.SourceEpochID, batch.DriverGeneration
	for i := range a.bindings {
		a.bindings[i].AssetID = asset
		a.bindings[i].SourceID = source
		a.bindings[i].SourceEpochID = epoch
		a.bindings[i].DriverGeneration = generation
	}
	for i := range a.services {
		a.services[i].AssetID = asset
		a.services[i].SourceEpochID = epoch
		a.services[i].DriverGeneration = generation
	}
	for i := range a.capabilities {
		a.capabilities[i].AssetID = asset
		a.capabilities[i].SourceEpochID = epoch
		a.capabilities[i].DriverGeneration = generation
	}
	if len(a.facts) > 0 {
		batch.FactUpserts = a.facts
	}
	if len(a.bindings) > 0 {
		batch.BindingUpserts = a.bindings
	}
	if len(a.services) > 0 {
		batch.ServiceUpserts = a.services
	}
	if len(a.capabilities) > 0 {
		batch.CapabilityUpserts = a.capabilities
	}
	if len(a.links) > 0 {
		batch.IdentityLinkUpserts = a.links
	}
}

func publicationRecords(scope Scope, current *world) (attachedBatch, error) {
	ids := slotIDs(scope, current.nodes)
	facts, bindings, links, err := factRecords(scope, current, ids)
	if err != nil {
		return attachedBatch{}, err
	}
	services, capabilities, extra, err := operationRecords(scope, current, ids)
	if err != nil {
		return attachedBatch{}, err
	}
	for id, binding := range extra {
		if _, ok := bindings[id]; !ok {
			bindings[id] = binding
		}
	}
	return attachedBatch{
		facts: facts, bindings: sortedBindings(bindings), services: services, capabilities: capabilities, links: sortedLinks(links),
	}, nil
}

func factRecords(scope Scope, current *world, ids map[string]semreg.ResourceID) ([]semreg.FactCandidate, map[semreg.NativeBindingID]semreg.NativeBinding, map[semreg.NativeBindingID]semreg.IdentityLink, error) {
	bindings := map[semreg.NativeBindingID]semreg.NativeBinding{}
	links := map[semreg.NativeBindingID]semreg.IdentityLink{}
	if err := putDeviceBindings(scope, current, bindings); err != nil {
		return nil, nil, nil, err
	}
	paths := make([]string, 0, len(current.facts))
	for path := range current.facts {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var facts []semreg.FactCandidate
	for _, path := range paths {
		fact := current.facts[path]
		if !admittedSample(fact) {
			continue
		}
		sample, err := sampleOf(scope, fact, current.sourceOf(scope, fact, ids), ids[fact.slot])
		if err != nil {
			return nil, nil, nil, err
		}
		facts = append(facts, sample.Candidate)
		if sample.Candidate.BindingID != nil {
			if err := putBinding(bindings, *sample.Candidate.BindingID, fact.device); err != nil {
				return nil, nil, nil, err
			}
			if err := putLink(links, *sample.Candidate.BindingID, ids[fact.slot], fact.device); err != nil {
				return nil, nil, nil, err
			}
		}
	}
	sort.Slice(facts, func(i, j int) bool { return facts[i].CandidateID < facts[j].CandidateID })
	return facts, bindings, links, nil
}

func operationConstraints(operationID, argument string, value *semreg.Value) ([]semreg.TypedField, bool) {
	if strings.HasPrefix(operationID, "energy.") {
		return []semreg.TypedField{}, true
	}
	if value == nil {
		return nil, false
	}
	return []semreg.TypedField{{ID: semreg.DefinitionID(argument), Value: copyConstraint(*value)}}, true
}

func copyConstraint(value semreg.Value) semreg.Value {
	if value.Quantity != nil {
		quantity := *value.Quantity
		value.Quantity = &quantity
	}
	if value.Symbol != nil {
		symbol := *value.Symbol
		value.Symbol = &symbol
	}
	if value.Boolean != nil {
		flag := *value.Boolean
		value.Boolean = &flag
	}
	if value.Text != nil {
		text := *value.Text
		value.Text = &text
	}
	return value
}

func operationRecords(scope Scope, current *world, ids map[string]semreg.ResourceID) ([]semreg.ServiceInstance, []semreg.CapabilityInstance, map[semreg.NativeBindingID]semreg.NativeBinding, error) {
	services := map[semreg.ServiceInstanceID]semreg.ServiceInstance{}
	capabilities := map[semreg.CapabilityInstanceID]semreg.CapabilityInstance{}
	bindings := map[semreg.NativeBindingID]semreg.NativeBinding{}
	add := func(device, slot, operationID, feature string, generation uint64, value *semreg.Value) error {
		spec, ok := operationSpec(operationID)
		node, placed := current.nodes[slot]
		if !ok || !placed || ids[slot] == "" || generation == 0 || feature == "" || !serviceAdmitted(spec.service, node) {
			return nil
		}
		constraints, ok := operationConstraints(operationID, spec.argument, value)
		if !ok {
			return nil
		}
		binding := featureBinding(scope, device, operationID, feature, slot)
		if err := putBinding(bindings, binding, device); err != nil {
			return err
		}
		proof, err := proofFor(operationID, feature)
		if err != nil {
			return err
		}
		serviceID := semreg.ServiceInstanceID(opaqueID("service.", operationID, slot))
		capabilityID := semreg.CapabilityInstanceID(opaqueID("capability.", operationID, slot))
		resource := ids[slot]
		if _, ok := services[serviceID]; !ok {
			services[serviceID] = semreg.ServiceInstance{
				InstanceID: serviceID, AssetID: "asset.pending", Definition: thermalRef(spec.service),
				BindingID: binding, SourceEpochID: "epoch.pending", DriverGeneration: "1",
				Qualification: semreg.QualificationQualified, Availability: semreg.AvailabilityAvailable,
				Revision: "1", Resource: &resource,
			}
		}
		if _, exists := capabilities[capabilityID]; !exists {
			capabilities[capabilityID] = semreg.CapabilityInstance{
				InstanceID: capabilityID, AssetID: "asset.pending", ServiceInstance: serviceID,
				Definition: thermalRef(spec.capability), BindingID: binding, SourceEpochID: "epoch.pending",
				DriverGeneration: "1", Qualification: semreg.QualificationQualified,
				Availability: semreg.AvailabilityAvailable, Constraints: constraints,
				ActivationEvidence: []semreg.EvidenceRef{proof}, Revision: "1",
			}
		}
		return nil
	}
	paths := make([]string, 0, len(current.facts))
	for path := range current.facts {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		fact := current.facts[path]
		if fact.withdrawn || fact.operation == "" || fact.feature == "" || fact.generation == 0 || !admittedSample(fact) {
			continue
		}
		if err := add(fact.device, fact.slot, fact.operation, fact.feature, fact.generation, factValue(fact)); err != nil {
			return nil, nil, nil, err
		}
	}
	for _, command := range current.commands {
		if err := add(command.device, command.slot, command.operation, command.feature, command.generation, nil); err != nil {
			return nil, nil, nil, err
		}
	}
	return sortedServices(services), sortedCapabilities(capabilities), bindings, nil
}

func putDeviceBindings(scope Scope, current *world, bindings map[semreg.NativeBindingID]semreg.NativeBinding) error {
	for _, node := range current.nodes {
		if node.device == "" || node.incomplete {
			continue
		}
		_, _, id := deviceBinding(scope, node.device)
		if err := putBinding(bindings, id, node.device); err != nil {
			return err
		}
	}
	return nil
}

func putLink(links map[semreg.NativeBindingID]semreg.IdentityLink, id semreg.NativeBindingID, resource semreg.ResourceID, device string) error {
	if _, ok := links[id]; ok || resource == "" {
		return nil
	}
	proof, err := proofFor(device, string(resource))
	if err != nil {
		return err
	}
	subject := resource
	links[id] = semreg.IdentityLink{
		AssetID: "asset.pending", BindingID: id, State: semreg.LinkQualified,
		Basis: []semreg.EvidenceRef{proof}, Revision: "1", Resource: &subject,
	}
	return nil
}

func sortedLinks(in map[semreg.NativeBindingID]semreg.IdentityLink) []semreg.IdentityLink {
	if len(in) == 0 {
		return nil
	}
	out := make([]semreg.IdentityLink, 0, len(in))
	for _, link := range in {
		out = append(out, link)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BindingID < out[j].BindingID })
	return out
}

func sourceDescriptor() (semreg.SourceDescriptor, error) {
	proof, err := proofFor("eebus-inbound", "protocol.eebus")
	if err != nil {
		return semreg.SourceDescriptor{}, err
	}
	return semreg.SourceDescriptor{
		ProtocolID: "protocol.eebus", ProfileID: "profile.eebus-inbound", ProfileVersion: "1",
		RegistryEvidence: proof, StartedAt: timePoint(0), State: semreg.SourceCurrent, Revision: "1",
	}, nil
}

func putBinding(bindings map[semreg.NativeBindingID]semreg.NativeBinding, id semreg.NativeBindingID, device string) error {
	if _, ok := bindings[id]; ok {
		return nil
	}
	proof, err := proofFor(device, string(id))
	if err != nil {
		return err
	}
	bindings[id] = semreg.NativeBinding{
		BindingID: id, AssetID: "asset.pending", SourceID: "source.pending", SourceEpochID: "epoch.pending",
		DriverGeneration: "1", NativeResource: proof, State: semreg.BindingCurrent, Revision: "1",
	}
	return nil
}

func sortedBindings(in map[semreg.NativeBindingID]semreg.NativeBinding) []semreg.NativeBinding {
	if len(in) == 0 {
		return nil
	}
	out := make([]semreg.NativeBinding, 0, len(in))
	for _, binding := range in {
		out = append(out, binding)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BindingID < out[j].BindingID })
	return out
}

func sortedServices(in map[semreg.ServiceInstanceID]semreg.ServiceInstance) []semreg.ServiceInstance {
	if len(in) == 0 {
		return nil
	}
	out := make([]semreg.ServiceInstance, 0, len(in))
	for _, service := range in {
		out = append(out, service)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].InstanceID < out[j].InstanceID })
	return out
}

func sortedCapabilities(in map[semreg.CapabilityInstanceID]semreg.CapabilityInstance) []semreg.CapabilityInstance {
	if len(in) == 0 {
		return nil
	}
	out := make([]semreg.CapabilityInstance, 0, len(in))
	for _, capability := range in {
		out = append(out, capability)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].InstanceID < out[j].InstanceID })
	return out
}

func (p *Publisher) bindExecutors(session driver.Session, tx driver.PublicationTransaction, routes []driver.SettingRoute) error {
	for _, cancel := range p.cancels {
		if cancel != nil {
			cancel()
		}
	}
	p.cancels = nil
	root := session.Native().Root()
	for _, route := range routes {
		owned := route
		owned.Owner.Producer = root
		if err := tx.DeclareSettingRoute(owned); err != nil {
			return err
		}
		cancel, err := session.RegisterExecutor(owned, nativeExecutor{publisher: p, session: session, path: route.Owner.PathKey})
		if err != nil {
			return err
		}
		p.cancels = append(p.cancels, cancel)
	}
	return nil
}

func (p *Publisher) remember(remotes []Remote) {
	if p.commands == nil {
		p.commands = map[string]func(kind, operation, feature, entity, value string) error{}
	}
	for _, remote := range remotes {
		if remote.SKI == "" {
			continue
		}
		if _, revoked := p.world.revoked[remote.SKI]; revoked || remote.Command == nil {
			delete(p.commands, remote.SKI)
			continue
		}
		p.commands[remote.SKI] = remote.Command
	}
}

func (p *Publisher) stageWrites(native driver.NativeRegistry, current *world) error {
	if len(current.writes) == 0 {
		return nil
	}
	keys := make([]string, 0, len(current.writes))
	for key := range current.writes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	ids := slotIDs(p.scope, current.nodes)
	for _, key := range keys {
		call := current.writes[key]
		node, ok := current.nodes[call.slot]
		if !ok {
			continue
		}
		ref, err := p.nodeRef(native, node, ids[call.slot])
		if err != nil {
			return err
		}
		local := "write." + call.field
		mark := call.slot + "\n" + local
		if _, seen := p.methods[mark]; seen {
			continue
		}
		spec := driver.MethodSpec{Local: driver.MemberID(local), Schema: "native.text", Scope: "instance"}
		if err := native.Method(ref, spec, p.writeMethod(call.slot, local)); err != nil {
			return err
		}
		if p.methods == nil {
			p.methods = map[string]struct{}{}
		}
		p.methods[mark] = struct{}{}
	}
	return nil
}

func (p *Publisher) writeMethod(slot, local string) driver.NativeMethod {
	return func(ctx context.Context, arg []byte) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p.pass <- struct{}{}
		call, ok := p.world.writes[slot+"\n"+local]
		command := p.commands[call.ski]
		blocked := !ok || call.feature == "" || command == nil
		if ok {
			if _, stopped := p.world.stopped[call.ski][call.generation]; stopped {
				blocked = true
			}
			if _, revoked := p.world.revoked[call.ski]; revoked {
				blocked = true
			}
		}
		<-p.pass
		if blocked {
			return nil, errors.New("inbound: write was not sent")
		}
		if err := command("namespace", local, call.feature, call.entity, string(arg)); err != nil {
			return nil, errors.New("inbound: write was not sent")
		}
		return arg, nil
	}
}

type nativeExecutor struct {
	publisher *Publisher
	session   driver.Session
	path      string
}

func (e nativeExecutor) Execute(ctx context.Context, dispatch driver.Dispatch) (driver.Completion, error) {
	if ctx.Err() != nil || !guardMatches(dispatch) || len(dispatch.Request.Arguments) == 0 {
		return notSent(dispatch.AdmittedAt)
	}
	e.publisher.pass <- struct{}{}
	target, ok := e.publisher.world.targets[e.path]
	command := e.publisher.commands[target.ski]
	blocked := !ok || command == nil
	if ok {
		if _, stopped := e.publisher.world.stopped[target.ski][target.generation]; stopped {
			blocked = true
		}
		if _, revoked := e.publisher.world.revoked[target.ski]; revoked {
			blocked = true
		}
	}
	<-e.publisher.pass
	if blocked {
		return notSent(dispatch.AdmittedAt)
	}
	value := commandedText(dispatch.Request.Arguments[0].Value)
	if err := command("semreg", target.operation, target.feature, target.entity, value); err != nil {
		return notSent(dispatch.AdmittedAt)
	}
	_ = e.publisher.applyCommanded(ctx, e.session, e.path, dispatch.Request.Arguments[0].Value)
	return sent(dispatch.AdmittedAt, target.operation, value)
}

func (p *Publisher) applyCommanded(ctx context.Context, session driver.Session, path string, value semreg.Value) error {
	p.pass <- struct{}{}
	defer func() { <-p.pass }()
	_, err := p.publish(ctx, session, func(next *world) error {
		fact, ok := next.facts[path]
		if !ok || fact.withdrawn {
			return nil
		}
		applyValue(&fact, value)
		fact.revision++
		next.facts[path] = fact
		return nil
	})
	return err
}

func applyValue(fact *placedFact, value semreg.Value) {
	fact.quantity, fact.symbol, fact.boolean, fact.text = nil, nil, nil, nil
	switch value.Kind {
	case semreg.ValueQuantity:
		if value.Quantity == nil {
			return
		}
		quantity := *value.Quantity
		fact.quantity = &quantity
		fact.native = quantity.Number.Coefficient
		fact.unit = string(quantity.Unit)
	case semreg.ValueSymbol:
		if value.Symbol == nil {
			return
		}
		symbol := *value.Symbol
		fact.symbol = &symbol
		fact.native = symbol.Token
	case semreg.ValueBoolean:
		if value.Boolean == nil {
			return
		}
		flag := *value.Boolean
		fact.boolean = &flag
		fact.native = "false"
		if flag {
			fact.native = "true"
		}
	case semreg.ValueText:
		if value.Text == nil {
			return
		}
		text := *value.Text
		fact.text = &text
		fact.native = text
	}
}

func commandedText(value semreg.Value) string {
	switch value.Kind {
	case semreg.ValueQuantity:
		if value.Quantity != nil {
			return value.Quantity.Number.Coefficient
		}
	case semreg.ValueSymbol:
		if value.Symbol != nil {
			return value.Symbol.Token
		}
	case semreg.ValueBoolean:
		if value.Boolean != nil && *value.Boolean {
			return "true"
		}
		if value.Boolean != nil {
			return "false"
		}
	case semreg.ValueText:
		if value.Text != nil {
			return *value.Text
		}
	}
	return ""
}

func guardMatches(dispatch driver.Dispatch) bool {
	guard, route := dispatch.Guard, dispatch.Route
	return guard.BindingID == route.BindingID && guard.SourceID == route.SourceID && guard.SourceEpochID == route.SourceEpochID &&
		guard.DriverGeneration == route.DriverGeneration && guard.CapabilityInstance == route.CapabilityInstance &&
		guard.ServiceInstance == route.ServiceInstance
}

func notSent(at semreg.TimePoint) (driver.Completion, error) {
	return completion(false, at, "not_sent", "inbound")
}

func sent(at semreg.TimePoint, operationID, value string) (driver.Completion, error) {
	return completion(true, at, operationID, value)
}

func completion(delivered bool, at semreg.TimePoint, text, unit string) (driver.Completion, error) {
	proof, err := proofFor(text, unit)
	if err != nil {
		return driver.Completion{}, err
	}
	if at.Validate() != nil {
		at = timePoint(0)
	}
	evidence := []semreg.EvidenceRef{proof}
	started := semreg.EvaluationContext{EvaluatedAt: at, EvaluateMonotonic: monotonic(0)}
	out := driver.Completion{
		Outcome: operation.OutcomeFailedNoContact,
		Dispatch: &operation.DispatchEvidence{
			AttemptID: "attempt.inbound", Started: started, Delivery: operation.DeliveryNotSent, Evidence: evidence,
		},
		OutcomeEvidence: evidence,
	}
	if delivered {
		out.Outcome = operation.OutcomeAcknowledgedUnverified
		out.Dispatch.Delivery = operation.DeliverySent
		out.Dispatch.PossibleSideEffect = true
		out.Acknowledgement = &operation.Acknowledgement{State: operation.AckAccepted, At: at, Evidence: evidence}
	}
	return out, nil
}
