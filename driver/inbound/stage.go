package inbound

import (
	"context"
	"errors"
	"sort"

	semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"
	"github.com/Project-Helianthus/helianthus-semreg/semreg/v1/driver"
)

type matchAdmission interface {
	AdmitMatch(driver.MatchClaim) error
}

func stage(ctx context.Context, session driver.Session, publisher *Publisher, current *world, result *Result) error {
	if session == nil || session.Publisher() == nil || session.Native() == nil {
		return errors.New("inbound: session publisher and namespace are required")
	}
	tx, err := session.Publisher().Begin(ctx)
	if err != nil {
		return err
	}
	scope := publisher.scope
	resources, err := current.resources(scope)
	if err != nil {
		return fail(ctx, tx, err)
	}
	relations, err := current.relations(scope)
	if err != nil {
		return fail(ctx, tx, err)
	}
	batch, err := session.Prepare(func(batch *semreg.PublicationBatch) {
		if len(resources) > 0 {
			batch.ResourceUpserts = resources
		}
		if len(relations) > 0 {
			batch.RelationUpserts = relations
		}
	})
	if err != nil {
		return fail(ctx, tx, err)
	}
	if err := tx.Stage(batch); err != nil {
		return fail(ctx, tx, err)
	}
	if err := current.stageFacts(tx, scope); err != nil {
		return fail(ctx, tx, err)
	}
	if err := current.stageLinks(tx, scope); err != nil {
		return fail(ctx, tx, err)
	}
	for _, claim := range result.Serials {
		if err := tx.AdmitManufacturerSerial(claim); err != nil {
			return fail(ctx, tx, err)
		}
	}
	if admission, ok := tx.(matchAdmission); ok {
		for _, claim := range result.Matches {
			if err := admission.AdmitMatch(claim); err != nil {
				return fail(ctx, tx, err)
			}
		}
	}
	for _, route := range result.Routes {
		if err := tx.DeclareSettingRoute(route); err != nil {
			return fail(ctx, tx, err)
		}
	}
	if err := publisher.stageMembers(session.Native(), current, result); err != nil {
		return fail(ctx, tx, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fail(ctx, tx, err)
	}
	return nil
}

func fail(ctx context.Context, tx driver.PublicationTransaction, err error) error {
	if abort := tx.Abort(ctx); abort != nil {
		return errors.Join(err, abort)
	}
	return err
}

func (w *world) resources(scope Scope) ([]semreg.Resource, error) {
	var records []semreg.Resource
	for _, node := range w.nodes {
		if node.incomplete || node.kind == "" {
			continue
		}
		record, err := resourceRecord(scope, node)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ResourceID < records[j].ResourceID })
	return records, nil
}

func (w *world) relations(scope Scope) ([]semreg.RelationInstance, error) {
	ids := slotIDs(scope, w.nodes)
	var records []semreg.RelationInstance
	for _, link := range w.links() {
		if ids[link.from] == "" || ids[link.to] == "" {
			return nil, errors.New("inbound: a relation names a missing resource")
		}
		record, err := relationRecord(scope, link, ids[link.from], ids[link.to])
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Definition.ID != records[j].Definition.ID {
			return records[i].Definition.ID < records[j].Definition.ID
		}
		if records[i].From != records[j].From {
			return records[i].From < records[j].From
		}
		return records[i].To < records[j].To
	})
	return records, nil
}

func slotIDs(scope Scope, nodes map[string]placedNode) map[string]semreg.ResourceID {
	ids := make(map[string]semreg.ResourceID, len(nodes))
	for slot, node := range nodes {
		if node.incomplete || node.kind == "" {
			continue
		}
		ids[slot] = scope.nodeID(node)
	}
	return ids
}

func (w *world) stageFacts(tx driver.PublicationTransaction, scope Scope) error {
	ids := slotIDs(scope, w.nodes)
	paths := make([]string, 0, len(w.facts))
	for path := range w.facts {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		fact := w.facts[path]
		source := sourceOf(scope, fact, ids)
		if err := tx.DeclareSource(source); err != nil {
			return err
		}
		sample, err := sampleOf(scope, fact, source)
		if err != nil {
			return err
		}
		if err := tx.StageSample(sample); err != nil {
			return err
		}
	}
	return nil
}

func (w *world) stageLinks(tx driver.PublicationTransaction, scope Scope) error {
	ids := slotIDs(scope, w.nodes)
	for _, link := range w.links() {
		proof, err := proofFor(link.from+"\n"+link.to, link.relation)
		if err != nil {
			return err
		}
		claim := driver.LinkClaim{
			Key:   driver.LinkKey{Type: thermalRef(link.relation), From: scope.ref(ids[link.from]), To: scope.ref(ids[link.to])},
			Basis: []semreg.EvidenceRef{proof},
		}
		if err := tx.StageLink(claim); err != nil {
			return err
		}
	}
	return nil
}

func (p *Publisher) stageMembers(native driver.NativeRegistry, current *world, result *Result) error {
	ids := slotIDs(p.scope, current.nodes)
	slots := make([]string, 0, len(current.extras))
	for slot := range current.extras {
		slots = append(slots, slot)
	}
	sort.Strings(slots)
	for _, slot := range slots {
		node, ok := current.nodes[slot]
		if !ok {
			continue
		}
		ref, err := p.nodeRef(native, node, ids[slot])
		if err != nil {
			return err
		}
		locals := make([]string, 0, len(current.extras[slot]))
		for local := range current.extras[slot] {
			locals = append(locals, local)
		}
		sort.Strings(locals)
		for _, local := range locals {
			if err := p.readMember(native, ref, slot, local); err != nil {
				return err
			}
		}
	}
	p.stampMembers(result)
	return nil
}

func (p *Publisher) nodeRef(native driver.NativeRegistry, node placedNode, id semreg.ResourceID) (driver.NodeRef, error) {
	if ref, ok := p.nodes[node.slot]; ok {
		return ref, nil
	}
	root := native.Root()
	kind := node.kind
	if kind == "" || node.incomplete {
		kind = "native.incomplete"
	}
	spec := driver.NodeSpec{
		Parent: &root, Kind: semreg.DefinitionID(kind), Local: driver.NodeID(node.slot), Contract: "eebus.inbound/v1",
	}
	if !node.incomplete && id != "" {
		subject := p.scope.ref(id)
		spec.Subject = &subject
	}
	ref, err := native.Node(spec)
	if err != nil {
		return driver.NodeRef{}, err
	}
	if p.nodes == nil {
		p.nodes = map[string]driver.NodeRef{}
	}
	p.nodes[node.slot] = ref
	return ref, nil
}

func (p *Publisher) readMember(native driver.NativeRegistry, ref driver.NodeRef, slot, local string) error {
	key := slot + "\n" + local
	if _, ok := p.members[key]; ok {
		return nil
	}
	member := driver.MemberSpec{Local: driver.MemberID(local), Schema: "native.text", Mode: "read", Scope: "instance"}
	if err := native.ReadMember(ref, member, p.memberReader(slot, local)); err != nil {
		return err
	}
	if p.members == nil {
		p.members = map[string]struct{}{}
	}
	p.members[key] = struct{}{}
	return nil
}

func (p *Publisher) memberReader(slot, local string) driver.NativeReader {
	return func(context.Context) ([]byte, error) {
		p.pass <- struct{}{}
		defer func() { <-p.pass }()
		if p.world.extras[slot] == nil {
			return []byte{}, nil
		}
		return []byte(p.world.extras[slot][local]), nil
	}
}

func (p *Publisher) stampMembers(result *Result) {
	if result == nil {
		return
	}
	for i := range result.Members {
		slot := string(result.Members[i].Member.Key.Node)
		ref, ok := p.nodes[slot]
		if !ok {
			continue
		}
		result.Members[i].Member.Key.Node = driver.NodeID(ref.ID)
	}
}

func sourceOf(scope Scope, fact placedFact, ids map[string]semreg.ResourceID) driver.Source {
	return driver.Source{
		For:      driver.Element{Subject: scope.ref(ids[fact.slot]), Key: factKey(fact.fact)},
		Producer: scope.ref(ids[deviceSlot(fact.device)]),
		PathKey:  fact.path,
		Priority: fact.priority,
	}
}
