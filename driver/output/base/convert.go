package base

import (
	"context"
	"errors"

	eebusruntime "github.com/Project-Helianthus/helianthus-eebusreg"
	semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"
	semdriver "github.com/Project-Helianthus/helianthus-semreg/semreg/v1/driver"
	"github.com/Project-Helianthus/helianthus-semreg/semreg/v1/graph"
)

const (
	lensHierarchy   = "lens.installation_hierarchy"
	lensComposition = "lens.equipment_composition"
	lensEnergy      = "lens.energy_flow"
	// Depths are the catalog max_depth of each lens. A deeper request is rejected.
	depthHierarchy   = 16
	depthComposition = 8
	depthEnergy      = 16

	elementActivePower  = "evse.ac.active_power"
	relationConnector   = "evse.relation.evse_connector"
	relationVehicle     = "evse.relation.connector_vehicle"
	relationCircuitRoom = "thermal.relation.circuit_room"
	relationZoneRoom    = "thermal.relation.zone_room"
	relationCircuitZone = "thermal.relation.circuit_zone"
	dimensionPhase      = "evse.dimension.phase"
	dimensionConnector  = "evse.dimension.connector"

	capabilityAllocatedCurrent = "evse.capability.set_allocated_current"
	capabilityPowerLimit       = "pv.capability.set_active_power_limit"
	operationAllocatedCurrent  = "evse.operation.set_allocated_current"
	operationPowerLimit        = "pv.operation.set_active_power_limit"
)

func prepareCut(ctx context.Context, input semdriver.SemanticInput, ref graph.CutRef, enabled bool) (preparedCut, error) {
	hierarchy, err := readLens(ctx, input, ref, lensRequest(lensHierarchy, depthHierarchy, graph.EntryPoint{
		Kind: graph.EntryLensRoots,
		Lens: semreg.DefinitionID(lensHierarchy),
	}))
	if err != nil {
		return preparedCut{}, err
	}
	composition, err := readLens(ctx, input, ref, lensRequest(lensComposition, depthComposition, graph.EntryPoint{
		Kind: graph.EntryLensRoots,
		Lens: semreg.DefinitionID(lensComposition),
	}))
	if err != nil {
		return preparedCut{}, err
	}
	if !sameCompleteness(ref, hierarchy.Cut, composition.Cut) {
		return preparedCut{omissions: omissionsOf(ref, hierarchy.Cut, composition.Cut)}, errCutMismatch
	}
	body := unionBodies(hierarchy, composition)
	omissions := omissionsOf(ref, hierarchy.Cut, composition.Cut)
	omitted := omissionSet(omissions)
	kinds := resourceKinds(body.resources)
	partial := ref.Completeness == graph.CutPartial
	prepared := preparedCut{
		grouping:  map[string]string{},
		assets:    map[string]string{},
		seen:      map[string]struct{}{},
		omissions: omissions,
		partial:   partial,
	}
	var drafts []draft
	for _, resource := range body.resources {
		kind := string(resource.Resource.Kind)
		family, output := outputFamilies[kind]
		if !output {
			continue
		}
		id := string(resource.Resource.ResourceID)
		asset := string(resource.AssetID)
		if resource.Resource.State == semreg.ResourceWithdrawn {
			continue
		}
		if resource.Resource.State != semreg.ResourceCurrent {
			continue
		}
		if _, skip := omitted[asset]; skip || id == "" {
			continue
		}
		drafts = append(drafts, draft{
			id:       id,
			kind:     kind,
			family:   family,
			asset:    asset,
			facts:    factsFor(body.facts, id, omitted),
			services: body.services,
			caps:     body.capabilities,
		})
	}
	relations := body.relations
	for index := range drafts {
		if drafts[index].kind != "resource.evse" {
			continue
		}
		request := lensRequest(lensEnergy, depthEnergy, graph.EntryPoint{
			Kind:     graph.EntryResource,
			Resource: semreg.ResourceID(drafts[index].id),
		})
		energy, err := readLens(ctx, input, ref, request)
		if err != nil {
			return preparedCut{}, err
		}
		if !sameCompleteness(ref, energy.Cut) {
			return preparedCut{omissions: omissionsOf(ref, hierarchy.Cut, composition.Cut, energy.Cut)}, errCutMismatch
		}
		drafts[index].facts = appendFacts(drafts[index].facts, energyFacts(energy, drafts[index].id, kinds, omitted))
		drafts[index].sumOmissions = omissionsOf(ref, energy.Cut)
		sum, err := input.SumMembers(ctx, ref, request, activePowerDefinition(drafts[index].facts), semreg.ResourceID(drafts[index].id))
		if err != nil {
			if ctx.Err() != nil {
				return preparedCut{}, err
			}
			drafts[index].sumErr = true
			continue
		}
		drafts[index].sum = &sum
	}
	results := make([]eebusruntime.LensResult, 0, len(drafts))
	for _, item := range drafts {
		result, grouping := convertDraft(item, relations, kinds, enabled)
		prepared.grouping[result.ResourceID] = grouping
		prepared.assets[result.ResourceID] = item.asset
		prepared.seen[result.ResourceID] = struct{}{}
		results = append(results, result)
	}
	prepared.results = orderResults(results)
	return prepared, nil
}

type draft struct {
	id           string
	kind         string
	family       string
	asset        string
	facts        []graph.LensFact
	services     []semreg.ServiceInstance
	caps         []semreg.CapabilityInstance
	sum          *graph.MemberSum
	sumOmissions []AssetGap
	sumErr       bool
}

type lensBody struct {
	resources    []graph.LensResource
	relations    []graph.LensRelation
	facts        []graph.LensFact
	services     []semreg.ServiceInstance
	capabilities []semreg.CapabilityInstance
}

func readLens(ctx context.Context, input semdriver.SemanticInput, ref graph.CutRef, request graph.LensRequest) (graph.LensResult, error) {
	seen := map[string]struct{}{}
	var combined graph.LensResult
	first := true
	for {
		page, err := input.RetrieveLens(ctx, ref, request)
		if err != nil {
			return graph.LensResult{}, err
		}
		if first {
			combined = page
			first = false
		} else {
			combined.Resources = append(combined.Resources, page.Resources...)
			combined.Relations = append(combined.Relations, page.Relations...)
			combined.Facts = append(combined.Facts, page.Facts...)
			combined.Services = append(combined.Services, page.Services...)
			combined.Capabilities = append(combined.Capabilities, page.Capabilities...)
			if len(page.Cut.NotIncluded) > 0 {
				combined.Cut.NotIncluded = page.Cut.NotIncluded
			}
		}
		if page.NextPage == nil || *page.NextPage == "" {
			return combined, nil
		}
		token := *page.NextPage
		if _, repeated := seen[token]; repeated {
			return graph.LensResult{}, errRepeatedPage
		}
		seen[token] = struct{}{}
		request.PageToken = token
	}
}

func unionBodies(pages ...graph.LensResult) lensBody {
	var body lensBody
	seenResource := map[string]int{}
	seenFact := map[string]struct{}{}
	seenService := map[string]struct{}{}
	seenCapability := map[string]struct{}{}
	for _, page := range pages {
		for _, resource := range page.Resources {
			id := string(resource.Resource.ResourceID)
			if at, ok := seenResource[id]; ok {
				body.resources[at].Planes = append(body.resources[at].Planes, resource.Planes...)
				continue
			}
			seenResource[id] = len(body.resources)
			body.resources = append(body.resources, resource)
		}
		body.relations = append(body.relations, page.Relations...)
		for _, fact := range page.Facts {
			key := factIdentity(fact)
			if _, ok := seenFact[key]; ok {
				continue
			}
			seenFact[key] = struct{}{}
			body.facts = append(body.facts, fact)
		}
		for _, service := range page.Services {
			id := string(service.InstanceID)
			if _, ok := seenService[id]; ok {
				continue
			}
			seenService[id] = struct{}{}
			body.services = append(body.services, service)
		}
		for _, capability := range page.Capabilities {
			id := string(capability.InstanceID)
			if _, ok := seenCapability[id]; ok {
				continue
			}
			seenCapability[id] = struct{}{}
			body.capabilities = append(body.capabilities, capability)
		}
	}
	return body
}

func convertDraft(item draft, relations []graph.LensRelation, kinds map[string]string, enabled bool) (eebusruntime.LensResult, string) {
	parent, cited, grouping := parentOf(item.id, item.kind, relations, kinds)
	result := eebusruntime.LensResult{
		Family:           item.family,
		ResourceID:       item.id,
		ParentResourceID: parent,
		LensAdmitted:     true,
		RuntimeEnabled:   enabled,
		ContainsCited:    cited,
	}
	promoted := map[string]struct{}{}
	var missing []string
	for _, fact := range item.facts {
		element := elementID(fact)
		value, quality, ok := promotedValue(fact)
		if !ok {
			if element != "" {
				missing = append(missing, element)
			}
			continue
		}
		if element != "" {
			promoted[element] = struct{}{}
		}
		result.Facts = append(result.Facts, eebusruntime.LensFact{
			Element:   element,
			Phase:     dimensionText(fact.Key, dimensionPhase),
			Connector: dimensionText(fact.Key, dimensionConnector),
			Value:     lensValue(value),
			Unit:      quantityUnit(value),
			Quality:   lensQuality(quality),
		})
	}
	for _, element := range missing {
		if _, have := promoted[element]; have {
			continue
		}
		if !containsString(result.MissingElements, element) {
			result.MissingElements = append(result.MissingElements, element)
		}
	}
	for _, fact := range result.Facts {
		if fact.Element != "" && !containsString(result.Elements, fact.Element) {
			result.Elements = append(result.Elements, fact.Element)
		}
	}
	result.Operations = operationsFor(item.id, item.services, item.caps)
	if item.kind == "resource.evse" {
		result.MemberSum = lensSum(item)
	}
	return result, grouping
}

func appendFacts(base, extra []graph.LensFact) []graph.LensFact {
	seen := map[string]struct{}{}
	for _, fact := range base {
		seen[factIdentity(fact)] = struct{}{}
	}
	for _, fact := range extra {
		key := factIdentity(fact)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		base = append(base, fact)
	}
	return base
}

func parentOf(id, kind string, relations []graph.LensRelation, kinds map[string]string) (string, bool, string) {
	switch kind {
	case "resource.meter":
		return "", false, ""
	case "resource.vehicle":
		return oneParent(vehicleParents(id, relations))
	case "resource.room":
		return oneParent(namedParents(id, relations, relationCircuitRoom, relationZoneRoom))
	case "resource.zone":
		return oneParent(namedParents(id, relations, relationCircuitZone))
	default:
		parents := containsParents(id, relations, kinds)
		if len(parents) > 1 {
			return "", false, "ambiguous"
		}
		if len(parents) == 1 {
			return parents[0], true, ""
		}
		return "", false, ""
	}
}

func oneParent(parents []string) (string, bool, string) {
	switch len(parents) {
	case 0:
		return "", false, "missing"
	case 1:
		return parents[0], true, ""
	default:
		return "", false, "ambiguous"
	}
}

func vehicleParents(vehicle string, relations []graph.LensRelation) []string {
	connectors := map[string]struct{}{}
	for _, relation := range relations {
		if definitionID(relation.Definition) != relationVehicle {
			continue
		}
		host, hosted, ok := relationEnds(relation)
		if !ok || hosted != vehicle {
			continue
		}
		connectors[host] = struct{}{}
	}
	var parents []string
	seen := map[string]struct{}{}
	for _, relation := range relations {
		if definitionID(relation.Definition) != relationConnector {
			continue
		}
		evse, connector, ok := relationEnds(relation)
		if !ok {
			continue
		}
		if _, hosted := connectors[connector]; !hosted {
			continue
		}
		if _, dup := seen[evse]; dup {
			continue
		}
		seen[evse] = struct{}{}
		parents = append(parents, evse)
	}
	return parents
}

func namedParents(child string, relations []graph.LensRelation, ids ...string) []string {
	wanted := map[string]struct{}{}
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	var parents []string
	seen := map[string]struct{}{}
	for _, relation := range relations {
		if _, ok := wanted[definitionID(relation.Definition)]; !ok {
			continue
		}
		parent, hosted, ok := relationEnds(relation)
		if !ok || hosted != child || parent == "" {
			continue
		}
		if _, dup := seen[parent]; dup {
			continue
		}
		seen[parent] = struct{}{}
		parents = append(parents, parent)
	}
	return parents
}

func containsParents(child string, relations []graph.LensRelation, kinds map[string]string) []string {
	var parents []string
	seen := map[string]struct{}{}
	for _, relation := range relations {
		if relation.CoreType != graph.CoreContains {
			continue
		}
		parent, hosted, ok := relationEnds(relation)
		if !ok || hosted != child || parent == "" || parent == child {
			continue
		}
		if _, output := outputFamilies[kinds[parent]]; !output {
			continue
		}
		if _, dup := seen[parent]; dup {
			continue
		}
		seen[parent] = struct{}{}
		parents = append(parents, parent)
	}
	return parents
}

func relationEnds(relation graph.LensRelation) (string, string, bool) {
	if !relation.InPlane || relation.State != semreg.LinkQualified || relation.Coherence != graph.CoherenceCoherent {
		return "", "", false
	}
	if relation.Orientation == graph.OrientationInverse {
		return string(relation.To), string(relation.From), true
	}
	return string(relation.From), string(relation.To), true
}

func energyFacts(page graph.LensResult, evse string, kinds map[string]string, omitted map[string]struct{}) []graph.LensFact {
	reached := reachable(evse, page.Relations, kinds)
	return factsForSubjects(page.Facts, reached, omitted)
}

func reachable(from string, relations []graph.LensRelation, kinds map[string]string) map[string]struct{} {
	seen := map[string]struct{}{from: {}}
	queue := []string{from}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, relation := range relations {
			parent, child, ok := relationEnds(relation)
			if !ok {
				continue
			}
			next := ""
			if parent == current {
				next = child
			} else if child == current {
				next = parent
			}
			if next == "" {
				continue
			}
			if _, output := outputFamilies[kinds[next]]; output && next != from {
				continue
			}
			if _, already := seen[next]; already {
				continue
			}
			seen[next] = struct{}{}
			queue = append(queue, next)
		}
	}
	return seen
}

func factsFor(facts []graph.LensFact, subject string, omitted map[string]struct{}) []graph.LensFact {
	return factsForSubjects(facts, map[string]struct{}{subject: {}}, omitted)
}

func factsForSubjects(facts []graph.LensFact, subjects map[string]struct{}, omitted map[string]struct{}) []graph.LensFact {
	var selected []graph.LensFact
	seen := map[string]struct{}{}
	for _, fact := range facts {
		if _, ok := subjects[string(fact.Subject)]; !ok {
			continue
		}
		if _, skip := omitted[string(fact.AssetID)]; skip {
			continue
		}
		key := factIdentity(fact)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		selected = append(selected, fact)
	}
	return selected
}

func operationsFor(resource string, services []semreg.ServiceInstance, capabilities []semreg.CapabilityInstance) []eebusruntime.LensOperation {
	byService := map[string]semreg.ServiceInstance{}
	for _, service := range services {
		byService[string(service.InstanceID)] = service
	}
	var operations []eebusruntime.LensOperation
	seen := map[string]struct{}{}
	for _, capability := range capabilities {
		service, ok := byService[string(capability.ServiceInstance)]
		if !ok || service.Resource == nil || string(*service.Resource) != resource {
			continue
		}
		if capability.Qualification != semreg.QualificationQualified {
			continue
		}
		if capability.Availability == semreg.AvailabilityWithdrawn || capability.Availability == semreg.AvailabilityUnavailable {
			continue
		}
		name := string(capability.Definition.ID)
		unit := ""
		switch name {
		case capabilityAllocatedCurrent:
			name = operationAllocatedCurrent
			unit = "A"
		case capabilityPowerLimit:
			name = operationPowerLimit
			unit = "W"
		}
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		operations = append(operations, eebusruntime.LensOperation{
			Name:          name,
			Unit:          unit,
			OwnCapability: true,
		})
	}
	return operations
}

func lensSum(item draft) *eebusruntime.LensMemberSum {
	sum := &eebusruntime.LensMemberSum{
		NotIncluded:      gapsToLens(item.sumOmissions),
		NotIncludedCount: len(item.sumOmissions),
	}
	if item.sumErr || item.sum == nil {
		sum.Withheld = true
		return sum
	}
	if item.sum.Withheld || item.sum.Value == nil {
		sum.Withheld = true
		sum.Reason = string(item.sum.Reason)
		return sum
	}
	sum.Coefficient = item.sum.Value.Number.Coefficient
	sum.Exponent = item.sum.Value.Number.Exponent10
	sum.Unit = string(item.sum.Value.Unit)
	return sum
}

func activePowerDefinition(facts []graph.LensFact) semreg.DefinitionRef {
	for _, fact := range facts {
		if fact.Definition.ID == elementActivePower {
			return fact.Definition
		}
	}
	return semreg.DefinitionRef{
		Pack:    semreg.PackRef{ID: "helianthus.pack.evse", Version: "1.0.0"},
		ID:      elementActivePower,
		Version: "1.0.0",
	}
}

func promotedValue(fact graph.LensFact) (semreg.Value, semreg.Quality, bool) {
	if len(fact.Conflicts) > 0 {
		return semreg.Value{}, semreg.Quality{}, false
	}
	var chosen *semreg.FactCandidate
	for index := range fact.Candidates {
		candidate := &fact.Candidates[index]
		if candidate.Quality.Promotion != semreg.PromotionPromoted || candidate.Quality.Qualification != semreg.QualificationQualified || candidate.Value == nil {
			continue
		}
		if len(fact.Evaluation) > 0 {
			evaluation, ok := evaluationFor(fact.Evaluation, candidate.CandidateID)
			if !ok || evaluation.Freshness == semreg.FreshnessExpired {
				continue
			}
			if evaluation.EffectiveAvailability != semreg.AvailabilityAvailable && evaluation.EffectiveAvailability != semreg.AvailabilityDegraded {
				continue
			}
		}
		if chosen != nil {
			return semreg.Value{}, semreg.Quality{}, false
		}
		chosen = candidate
	}
	if chosen == nil {
		return semreg.Value{}, semreg.Quality{}, false
	}
	return *chosen.Value, chosen.Quality, true
}

func evaluationFor(rows []semreg.EvaluatedFact, id semreg.CandidateID) (semreg.EvaluatedFact, bool) {
	for _, row := range rows {
		if row.CandidateID == id {
			return row, true
		}
	}
	return semreg.EvaluatedFact{}, false
}

func lensValue(value semreg.Value) eebusruntime.LensValue {
	out := eebusruntime.LensValue{Kind: string(value.Kind)}
	if value.Quantity != nil {
		out.Coefficient = value.Quantity.Number.Coefficient
		out.Exponent = value.Quantity.Number.Exponent10
	}
	if value.Boolean != nil {
		out.Boolean = *value.Boolean
	}
	if value.Text != nil {
		out.Text = *value.Text
	}
	if value.Symbol != nil {
		out.Symbol = value.Symbol.Token
	}
	return out
}

func lensQuality(quality semreg.Quality) eebusruntime.LensQuality {
	return eebusruntime.LensQuality{
		Assertion:     string(quality.Assertion),
		Qualification: string(quality.Qualification),
		Promotion:     string(quality.Promotion),
		Validity:      string(quality.Validity),
		Availability:  string(quality.Availability),
		Freshness:     string(quality.Freshness),
	}
}

func quantityUnit(value semreg.Value) string {
	if value.Quantity == nil {
		return ""
	}
	return string(value.Quantity.Unit)
}

func dimensionText(key semreg.FactKey, id string) string {
	for _, dimension := range key.Dimensions {
		if string(dimension.ID) != id {
			continue
		}
		switch dimension.Value.Kind {
		case semreg.ValueText:
			if dimension.Value.Text != nil {
				return *dimension.Value.Text
			}
		case semreg.ValueSymbol:
			if dimension.Value.Symbol != nil {
				return dimension.Value.Symbol.Token
			}
		case semreg.ValueQuantity:
			if dimension.Value.Quantity != nil {
				return dimension.Value.Quantity.Number.Coefficient
			}
		}
	}
	return ""
}

func elementID(fact graph.LensFact) string {
	if fact.Definition.ID != "" {
		return string(fact.Definition.ID)
	}
	return string(fact.Key.FactID)
}

func definitionID(ref semreg.DefinitionRef) string {
	return string(ref.ID)
}

func factIdentity(fact graph.LensFact) string {
	return elementID(fact) + "\x00" + string(fact.Subject) + "\x00" + string(fact.Key.FactID) + "\x00" + dimensionsText(fact.Key)
}

func dimensionsText(key semreg.FactKey) string {
	text := ""
	for _, dimension := range key.Dimensions {
		text += string(dimension.ID) + "=" + dimensionText(key, string(dimension.ID)) + ";"
	}
	return text
}

func orderResults(results []eebusruntime.LensResult) []eebusruntime.LensResult {
	index := map[string]int{}
	for at, result := range results {
		index[result.ResourceID] = at
	}
	depth := map[string]int{}
	var walk func(id string, seen map[string]struct{}) int
	walk = func(id string, seen map[string]struct{}) int {
		if value, ok := depth[id]; ok {
			return value
		}
		if _, loop := seen[id]; loop {
			return 0
		}
		seen[id] = struct{}{}
		at, ok := index[id]
		if !ok {
			return 0
		}
		parent := results[at].ParentResourceID
		value := 0
		if _, inSet := index[parent]; parent != "" && inSet {
			value = walk(parent, seen) + 1
		}
		depth[id] = value
		return value
	}
	for _, result := range results {
		walk(result.ResourceID, map[string]struct{}{})
	}
	ordered := append([]eebusruntime.LensResult(nil), results...)
	for left := 1; left < len(ordered); left++ {
		item := ordered[left]
		right := left
		for right > 0 && depth[ordered[right-1].ResourceID] > depth[item.ResourceID] {
			ordered[right] = ordered[right-1]
			right--
		}
		ordered[right] = item
	}
	return ordered
}

func lensRequest(lens string, depth int, entry graph.EntryPoint) graph.LensRequest {
	return graph.LensRequest{
		Lens:      semreg.DefinitionID(lens),
		Entry:     entry,
		Direction: graph.DirectionOutgoing,
		Depth:     depth,
		PageSize:  graph.MaxPageSize,
	}
}

func sameCompleteness(ref graph.CutRef, cuts ...graph.CutRef) bool {
	if ref.Completeness != graph.CutComplete && ref.Completeness != graph.CutPartial {
		return false
	}
	for _, cut := range cuts {
		if cut.Completeness != "" && cut.Completeness != ref.Completeness {
			return false
		}
	}
	return true
}

func omissionsOf(cuts ...graph.CutRef) []AssetGap {
	var gaps []AssetGap
	seen := map[string]struct{}{}
	for _, cut := range cuts {
		for _, item := range cut.NotIncluded {
			asset := string(item.AssetID)
			if asset == "" {
				continue
			}
			if _, dup := seen[asset]; dup {
				continue
			}
			seen[asset] = struct{}{}
			gaps = append(gaps, AssetGap{Asset: asset, Reason: string(item.Reason)})
		}
	}
	return gaps
}

func omissionSet(gaps []AssetGap) map[string]struct{} {
	set := map[string]struct{}{}
	for _, gap := range gaps {
		set[gap.Asset] = struct{}{}
	}
	return set
}

func resourceKinds(resources []graph.LensResource) map[string]string {
	kinds := map[string]string{}
	for _, resource := range resources {
		kinds[string(resource.Resource.ResourceID)] = string(resource.Resource.Kind)
	}
	return kinds
}

func gapsToLens(gaps []AssetGap) []eebusruntime.LensNotIncluded {
	if len(gaps) == 0 {
		return nil
	}
	items := make([]eebusruntime.LensNotIncluded, len(gaps))
	for index, gap := range gaps {
		items[index] = eebusruntime.LensNotIncluded{Asset: gap.Asset, Reason: gap.Reason}
	}
	return items
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

var outputFamilies = map[string]string{
	"resource.inverter":         "inverter",
	"resource.pv_string":        "pv string",
	"resource.dc_input":         "pv string",
	"resource.evse":             "charger",
	"resource.charging_station": "charger",
	"resource.vehicle":          "ev",
	"resource.meter":            "meter",
	"resource.battery":          "battery",
	"resource.thermal_circuit":  "heating circuit",
	"resource.zone":             "heating zone",
	"resource.room":             "room",
	"resource.grid_connection":  "grid",
}

// errRepeatedPage is a lens page that did not advance.
var errRepeatedPage = repeatedPageError{}

// errCutMismatch is a lens page whose completeness is not the captured cut.
var errCutMismatch = errors.New("lens cut does not match the captured cut")

type repeatedPageError struct{}

func (repeatedPageError) Error() string { return "lens page repeated" }
