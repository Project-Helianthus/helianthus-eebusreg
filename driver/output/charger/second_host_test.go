package charger_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
	"unsafe"

	eebusruntime "github.com/Project-Helianthus/helianthus-eebusreg"
	"github.com/Project-Helianthus/helianthus-eebusreg/driver"
	"github.com/Project-Helianthus/helianthus-eebusreg/driver/output/base"
	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusfacade"
	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusservicebridge"
	semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"
	semdriver "github.com/Project-Helianthus/helianthus-semreg/semreg/v1/driver"
	"github.com/Project-Helianthus/helianthus-semreg/semreg/v1/graph"
	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
)

const (
	hostTokenA = "00112233445566778899aabbccddee11"
	hostTokenB = "00112233445566778899aabbccddee22"
)

func TestSecondHostChargerMeasurement(t *testing.T) {
	// The peer reads the reported total, not the member sum, and every measurement row.
	ref := hostRef("cut-measure", graph.CutComplete)
	script := newHostScript(ref)
	script.install(ref, []graph.LensResource{hostResource("evse-1", "resource.evse", "asset-evse")}, nil, measurementEnergy(), measurementFacts("evse-1"), map[string]graph.MemberSum{
		"evse-1": hostWatt("4", 3),
	})
	host := startChargerHost(t, "charger.measure", "192.0.2.40:4951", hostTokenA, script)
	otherRef := hostRef("cut-other", graph.CutComplete)
	otherScript := newHostScript(otherRef)
	otherScript.install(otherRef, []graph.LensResource{hostResource("evse-other", "resource.evse", "asset-other")}, nil, nil, nil, map[string]graph.MemberSum{
		"evse-other": hostWatt("1", 0),
	})
	other := startChargerHost(t, "charger.other", "192.0.2.41:4952", hostTokenB, otherScript)
	waitHost(t, host, "evse-1")
	waitHost(t, other, "evse-other")
	entity := hostEntity(t, host, "evse-1")
	assertConnection(t, entity)
	assertRows(t, entity, fullRows())
	state := diagnosisState(t, entity)
	if state.OperatingState == nil || string(*state.OperatingState) != string(spinemodel.DeviceDiagnosisOperatingStateTypeNormalOperation) || state.LastErrorCode != nil {
		t.Fatalf("diagnosis %+v", state)
	}
	stopLeavesTheOther(t, host, other)
}

func TestSecondHostChargerPhaseOnlyTotal(t *testing.T) {
	// Phase power is 15*10^2 and 25*10^2. The member sum is coefficient 4 exponent 3.
	// A naive coefficient add is 40. A renormalized watt sum is 4000 at scale 0.
	ref := hostRef("cut-phase", graph.CutComplete)
	script := newHostScript(ref)
	script.install(ref, []graph.LensResource{hostResource("evse-1", "resource.evse", "asset-evse")}, nil, phaseRelations("evse-1"), []graph.LensFact{
		hostQuantity("phase-a", "evse.ac.active_power", "a", "15", 2, "unit.watt", "asset-phase-a"),
		hostQuantity("phase-b", "evse.ac.active_power", "b", "25", 2, "unit.watt", "asset-phase-b"),
	}, map[string]graph.MemberSum{"evse-1": hostWatt("4", 3)})
	host := startChargerHost(t, "charger.phase", "192.0.2.42:4953", hostTokenA, script)
	waitHost(t, host, "evse-1")
	rows := readRows(t, hostEntity(t, host, "evse-1"))
	if !hasRow(rows, wantRow{scope: "acPowerTotal", measure: "power", unit: "W", number: 4, scale: 3, calculated: true, acType: "real"}) {
		t.Fatalf("total %+v", rows)
	}
	if !hasRow(rows, wantRow{scope: "acPower", measure: "power", unit: "W", number: 15, scale: 2, acType: "real", variant: "rms", phase: "a", reference: "neutral"}) || !hasRow(rows, wantRow{scope: "acPower", measure: "power", unit: "W", number: 25, scale: 2, acType: "real", variant: "rms", phase: "b", reference: "neutral"}) {
		t.Fatalf("phases %+v", rows)
	}
}

func TestSecondHostChargerEVAndMeter(t *testing.T) {
	// The vehicle parent is the connector chain. A contains edge from another EVSE is not the parent.
	// The meter stays a root entity when a contains edge names the EVSE.
	ref := hostRef("cut-ev", graph.CutComplete)
	script := newHostScript(ref)
	script.install(ref, []graph.LensResource{
		hostResource("evse-1", "resource.evse", "asset-evse"),
		hostResource("evse-decoy", "resource.evse", "asset-decoy"),
		hostResource("connector-1", "resource.connector", "asset-connector"),
		hostResource("vehicle-1", "resource.vehicle", "asset-vehicle"),
		hostResource("meter-1", "resource.meter", "asset-meter"),
	}, []graph.LensRelation{
		hostRelation("evse.relation.evse_connector", "evse-1", "connector-1", "asset-rel-connector"),
		hostRelation("evse.relation.connector_vehicle", "connector-1", "vehicle-1", "asset-rel-vehicle"),
		hostContains("evse-decoy", "vehicle-1", "asset-rel-decoy"),
		hostContains("evse-1", "meter-1", "asset-rel-meter"),
	}, nil, nil, map[string]graph.MemberSum{
		"evse-1": hostWatt("1", 0), "evse-decoy": hostWatt("1", 0),
	})
	host := startChargerHost(t, "charger.ev", "192.0.2.43:4954", hostTokenA, script)
	waitHost(t, host, "evse-1")
	waitHost(t, host, "vehicle-1")
	waitHost(t, host, "meter-1")
	evse := host.binding.EntityAddress("evse-1")
	vehicle := host.binding.EntityAddress("vehicle-1")
	meter := host.binding.EntityAddress("meter-1")
	decoy := host.binding.EntityAddress("evse-decoy")
	if evse == "" || vehicle == "" || meter == "" || decoy == "" || decoy == evse {
		t.Fatalf("addresses evse %s vehicle %s meter %s decoy %s", evse, vehicle, meter, decoy)
	}
	if !host.node.NestedUnder(vehicle, evse) || host.node.NestedUnder(vehicle, decoy) || host.binding.ParentAddress("vehicle-1") != evse {
		t.Fatal("vehicle was not nested under the connector's EVSE")
	}
	if host.node.NestedUnder(meter, evse) || host.binding.ParentAddress("meter-1") != "" {
		t.Fatal("meter was nested under the EVSE")
	}
	if !chargerHas(host.node.EntityTypes(), "EV") || !chargerHas(host.node.EntityTypes(), "SubMeterElectricity") || !chargerHas(host.node.EntityTypes(), "EVSE") {
		t.Fatalf("types %v", host.node.EntityTypes())
	}
	if hostEntity(t, host, "meter-1").EntityType() != spinemodel.EntityTypeTypeSubMeterElectricity {
		t.Fatal("meter entity type")
	}
	if hostEntity(t, host, "vehicle-1").EntityType() != spinemodel.EntityTypeTypeEV {
		t.Fatal("vehicle entity type")
	}
}

func TestSecondHostChargerPartialCut(t *testing.T) {
	// One phase asset is omitted. The peer gets that phase's sibling and no total.
	// The manager reason names partial_cut and the omitted asset.
	omitted := graph.NotIncludedAsset{AssetID: "asset-phase-c", Reason: graph.NotIncludedSourceUnavailable}
	ref := hostRef("cut-partial", graph.CutPartial, omitted)
	script := newHostScript(ref)
	script.install(ref, []graph.LensResource{hostResource("evse-1", "resource.evse", "asset-evse")}, nil, measurementEnergy(), []graph.LensFact{
		hostQuantity("phase-a", "evse.ac.active_power", "a", "15", 0, "unit.watt", "asset-phase-a"),
		hostQuantity("phase-c", "evse.ac.active_power", "c", "99", 0, "unit.watt", "asset-phase-c"),
	}, map[string]graph.MemberSum{"evse-1": {Withheld: true, Reason: graph.SumPartialCut}})
	host := startChargerHost(t, "charger.partial", "192.0.2.44:4955", hostTokenA, script)
	waitHost(t, host, "evse-1")
	rows := readRows(t, hostEntity(t, host, "evse-1"))
	if rowScope(rows, "acPowerTotal") {
		t.Fatalf("partial cut served a total %+v", rows)
	}
	if !hasRow(rows, wantRow{scope: "acPower", measure: "power", unit: "W", number: 15, acType: "real", variant: "rms", phase: "a", reference: "neutral"}) {
		t.Fatalf("known phase %+v", rows)
	}
	if hasRow(rows, wantRow{scope: "acPower", measure: "power", unit: "W", number: 99, acType: "real", variant: "rms", phase: "c", reference: "neutral"}) {
		t.Fatal("omitted phase was served")
	}
	line, ok := reasonLine(host.output.Reason(), "evse-1")
	if !ok || !line.SumWithheld || line.SumReason != string(graph.SumPartialCut) || !gapNamed(line, "asset-phase-c", string(graph.NotIncludedSourceUnavailable)) {
		t.Fatalf("reason %+v", line)
	}
}

func stopLeavesTheOther(t *testing.T, stopped, live *chargerHost) {
	t.Helper()
	reads := stopped.script.readsCount()
	kept := stopped.binding.EntityAddress("evse-1")
	stopped.instance.Stop(context.Background())
	later := hostRef("cut-stopped-later", graph.CutComplete)
	stopped.script.install(later, []graph.LensResource{hostResource("evse-later", "resource.evse", "asset-later")}, nil, nil, nil, nil)
	stopped.script.push(later)
	if stopped.script.readsCount() != reads || stopped.binding.EntityAddress("evse-later") != "" || stopped.binding.EntityAddress("evse-1") != kept {
		t.Fatalf("stopped host changed, reads %d address %q", stopped.script.readsCount(), stopped.binding.EntityAddress("evse-later"))
	}
	added := hostRef("cut-live-later", graph.CutComplete)
	live.script.install(added, []graph.LensResource{
		hostResource("evse-other", "resource.evse", "asset-other"),
		hostResource("evse-added", "resource.evse", "asset-added"),
	}, nil, nil, nil, map[string]graph.MemberSum{
		"evse-other": hostWatt("1", 0), "evse-added": hostWatt("1", 0),
	})
	live.script.push(added)
	waitHost(t, live, "evse-added")
	if live.binding.EntityAddress("evse-other") == "" {
		t.Fatal("live host dropped its entity")
	}
}

type chargerHost struct {
	instance semdriver.Instance
	script   *hostScript
	binding  *eebusruntime.OutputBinding
	output   *base.Output
	service  *eebusservicebridge.Service
	node     *openedNode
}

var hostListenPort = 4951

func startChargerHost(t *testing.T, key, address, token string, script *hostScript) *chargerHost {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	config := eebusruntime.Config{
		Enabled: true, StateRoot: root, Interface: "test-interface-charger",
		ListenAddress: netip.MustParseAddrPort(address), PairingPolicy: eebusruntime.PairingPolicyClosed,
	}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	factory := driver.NewFactory()
	plans, err := factory.Plan(semdriver.Config{Instance: key, Enabled: true, Options: raw})
	if err != nil || len(plans) != 1 {
		t.Fatalf("plan %v %v", plans, err)
	}
	instance, err := factory.Construct(context.Background(), plans[0])
	if err != nil {
		t.Fatal(err)
	}
	port := hostListenPort
	hostListenPort++
	service, err := eebusfacade.NewPreparedOutputService(eebusfacade.RuntimeConfig{
		Interface: "fixture-interface", ListenPort: port,
		ListenAddress: netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(port)),
	}, token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { service.Shutdown() })
	backend := eebusfacade.NewOutputServiceBackend(service)
	t.Cleanup(func() { _ = backend.Close() })
	node, err := eebusfacade.OpenOutputNode(backend)
	if err != nil {
		t.Fatal(err)
	}
	setPeerField(instance, "runtime", &hostRuntime{node: node})
	session := &hostSession{root: semdriver.NodeRef{Bridge: "bridge.eebus", ID: semreg.ResourceID("installations/lab/instances/eebus-inbound/" + key)}}
	session.PairingSessionSurface = eebusservicebridge.NewPairingSessionSurface(session).WithInput(script)
	if err := instance.Start(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { instance.Stop(context.Background()) })
	binding, _ := peerField(instance, "binding").Interface().(*eebusruntime.OutputBinding)
	output, _ := peerField(instance, "output").Interface().(*base.Output)
	if binding == nil || output == nil {
		t.Fatal("start did not attach output")
	}
	return &chargerHost{instance: instance, script: script, binding: binding, output: output, service: service, node: node}
}

func waitHost(t *testing.T, host *chargerHost, id string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if address := host.binding.EntityAddress(id); address != "" {
			return address
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("address for %s missing, reason %+v", id, host.output.Reason())
	return ""
}

func hostEntity(t *testing.T, host *chargerHost, id string) spineapi.EntityLocalInterface {
	t.Helper()
	letter := host.binding.EntityAddress(id)
	number := letterNumber(letter)
	parent := host.binding.ParentAddress(id)
	numbers := []spinemodel.AddressEntityType{number}
	if parent != "" {
		numbers = []spinemodel.AddressEntityType{letterNumber(parent), number}
	}
	return entityAt(t, host.service, numbers)
}

func letterNumber(address string) spinemodel.AddressEntityType {
	value := 0
	for index := 0; index < len(address); index++ {
		value = value*26 + int(address[index]-'a') + 1
	}
	return spinemodel.AddressEntityType(2 + value - 1)
}

func reasonLine(reason base.Reason, id string) (base.ResourceLine, bool) {
	for _, line := range reason.Resources {
		if line.ResourceID == id {
			return line, true
		}
	}
	return base.ResourceLine{}, false
}

func gapNamed(line base.ResourceLine, asset, reason string) bool {
	for _, gap := range line.NotIncluded {
		if gap.Asset == asset && gap.Reason == reason {
			return true
		}
	}
	return false
}

func peerField(instance semdriver.Instance, name string) reflect.Value {
	field := reflect.ValueOf(instance).Elem().FieldByName(name)
	return reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem()
}

func setPeerField(instance semdriver.Instance, name string, value any) {
	peerField(instance, name).Set(reflect.ValueOf(value))
}

type hostRuntime struct{ node *openedNode }

var _ eebusruntime.Runtime = (*hostRuntime)(nil)

func (runtime *hostRuntime) OutputNode() (*openedNode, error) {
	if runtime == nil || runtime.node == nil {
		return nil, errors.New("existing node is unavailable")
	}
	return runtime.node, nil
}
func (runtime *hostRuntime) Start(context.Context) error { return nil }
func (runtime *hostRuntime) Shutdown() error             { return nil }
func (runtime *hostRuntime) Snapshot() (eebusruntime.SnapshotV1, error) {
	return eebusruntime.SnapshotV1{}, nil
}
func (runtime *hostRuntime) PairingState() ([]eebusruntime.PairingObservationV1, error) {
	return nil, nil
}
func (runtime *hostRuntime) FeaturesGet(context.Context, eebusraw.ReadAuthorizationV1, eebusraw.FeaturesGetRequestV1) (eebusraw.FeaturesGetDataV1, *eebusraw.ErrorV1) {
	return eebusraw.FeaturesGetDataV1{}, nil
}
func (runtime *hostRuntime) FeaturesDataGet(context.Context, eebusraw.ReadAuthorizationV1, eebusraw.FeatureDataGetRequestV1) (eebusraw.FeatureDataGetDataV1, *eebusraw.ErrorV1) {
	return eebusraw.FeatureDataGetDataV1{}, nil
}

type hostCut = semdriver.SemanticCut

type hostScript struct {
	mu      sync.Mutex
	notify  chan struct{}
	current semdriver.SemanticCut
	pending []semdriver.SemanticCut
	pages   map[string]map[string]graph.LensResult
	sums    map[string]map[string]graph.MemberSum
	reads   int
}

func newHostScript(ref graph.CutRef) *hostScript {
	return &hostScript{
		notify:  make(chan struct{}, 1),
		current: semdriver.SemanticCut{Ref: ref},
		pages:   map[string]map[string]graph.LensResult{},
		sums:    map[string]map[string]graph.MemberSum{},
	}
}

func (script *hostScript) install(ref graph.CutRef, resources []graph.LensResource, topology, energy []graph.LensRelation, facts []graph.LensFact, sums map[string]graph.MemberSum) {
	script.set(ref, "lens.installation_hierarchy", graph.LensResult{Resources: resources, Relations: topology})
	script.set(ref, "lens.equipment_composition", graph.LensResult{Resources: resources})
	script.set(ref, "lens.energy_flow", graph.LensResult{Relations: energy, Facts: facts})
	for id, sum := range sums {
		script.setSum(ref, id, sum)
	}
}

func (script *hostScript) set(ref graph.CutRef, lens string, page graph.LensResult) {
	script.mu.Lock()
	defer script.mu.Unlock()
	page.Cut = ref
	id := string(ref.CutID)
	if script.pages[id] == nil {
		script.pages[id] = map[string]graph.LensResult{}
	}
	script.pages[id][lens] = page
}

func (script *hostScript) setSum(ref graph.CutRef, resource string, sum graph.MemberSum) {
	script.mu.Lock()
	defer script.mu.Unlock()
	id := string(ref.CutID)
	if script.sums[id] == nil {
		script.sums[id] = map[string]graph.MemberSum{}
	}
	script.sums[id][resource] = sum
}

func (script *hostScript) push(ref graph.CutRef) {
	script.mu.Lock()
	script.pending = append(script.pending, semdriver.SemanticCut{Ref: ref})
	script.mu.Unlock()
	select {
	case script.notify <- struct{}{}:
	default:
	}
}

func (script *hostScript) readsCount() int {
	script.mu.Lock()
	defer script.mu.Unlock()
	return script.reads
}

func (script *hostScript) Capture(ctx context.Context, _ semdriver.Filter) (hostCut, error) {
	if err := ctx.Err(); err != nil {
		return hostCut{}, err
	}
	script.mu.Lock()
	defer script.mu.Unlock()
	return script.current, nil
}

func (script *hostScript) Resume(ctx context.Context, _ semdriver.Cursor, _ semdriver.Filter) (semdriver.Changes, error) {
	for {
		script.mu.Lock()
		if len(script.pending) > 0 {
			cut := script.pending[0]
			script.pending = script.pending[1:]
			script.current = cut
			script.mu.Unlock()
			return semdriver.Changes{Commits: []semdriver.SemanticCut{cut}}, nil
		}
		script.mu.Unlock()
		select {
		case <-ctx.Done():
			return semdriver.Changes{}, ctx.Err()
		case <-script.notify:
		}
	}
}

func (script *hostScript) RetrieveLens(_ context.Context, ref graph.CutRef, request graph.LensRequest) (graph.LensResult, error) {
	script.mu.Lock()
	defer script.mu.Unlock()
	script.reads++
	page, ok := script.pages[string(ref.CutID)][string(request.Lens)]
	if !ok {
		return graph.LensResult{Cut: ref}, nil
	}
	return page, nil
}

func (script *hostScript) SumMembers(_ context.Context, ref graph.CutRef, _ graph.LensRequest, _ semreg.DefinitionRef, scope semreg.ResourceID) (graph.MemberSum, error) {
	script.mu.Lock()
	defer script.mu.Unlock()
	sum, ok := script.sums[string(ref.CutID)][string(scope)]
	if !ok {
		return graph.MemberSum{}, nil
	}
	return sum, nil
}

func hostRef(id string, completeness graph.Completeness, omitted ...graph.NotIncludedAsset) graph.CutRef {
	return graph.CutRef{CutID: semreg.Digest(id), Completeness: completeness, NotIncluded: omitted}
}

func hostResource(id, kind, asset string) graph.LensResource {
	return graph.LensResource{Resource: semreg.Resource{
		ResourceID: semreg.ResourceID(id), Kind: semreg.DefinitionID(kind), State: semreg.ResourceCurrent,
	}, AssetID: semreg.AssetID(asset)}
}

func hostRelation(definition, from, to, asset string) graph.LensRelation {
	return graph.LensRelation{
		AssetID: semreg.AssetID(asset), Definition: semreg.DefinitionRef{ID: semreg.DefinitionID(definition)},
		From: semreg.ResourceID(from), To: semreg.ResourceID(to),
		State: semreg.LinkQualified, Coherence: graph.CoherenceCoherent, InPlane: true,
	}
}

func hostContains(from, to, asset string) graph.LensRelation {
	relation := hostRelation("contains", from, to, asset)
	relation.CoreType = graph.CoreContains
	return relation
}

func hostWatt(coefficient string, exponent int32) graph.MemberSum {
	return graph.MemberSum{Value: &semreg.Quantity{
		Number: semreg.Decimal{Coefficient: coefficient, Exponent10: exponent}, Unit: "unit.watt",
	}}
}

func measurementEnergy() []graph.LensRelation {
	return append(phaseRelations("evse-1"), hostRelation("evse.relation.connector_phase", "connector-1", "phase-c", "asset-energy-phase-c"))
}

func phaseRelations(evse string) []graph.LensRelation {
	return []graph.LensRelation{
		hostRelation("evse.relation.evse_connector", evse, "connector-1", "asset-energy-connector"),
		hostRelation("evse.relation.connector_phase", "connector-1", "phase-a", "asset-energy-phase-a"),
		hostRelation("evse.relation.connector_phase", "connector-1", "phase-b", "asset-energy-phase-b"),
	}
}

func measurementFacts(evse string) []graph.LensFact {
	return []graph.LensFact{
		hostQuantity(evse, "evse.ac.total_active_power", "", "9000", 0, "unit.watt", "asset-total"),
		hostQuantity("phase-a", "evse.ac.active_power", "a", "15", 2, "unit.watt", "asset-phase-a"),
		hostQuantity("phase-b", "evse.ac.active_power", "b", "25", 2, "unit.watt", "asset-phase-b"),
		hostQuantity("phase-c", "evse.ac.active_power", "c", "5", 0, "unit.watt", "asset-phase-c"),
		hostQuantity(evse, "evse.ac.current", "a", "11", 0, "unit.ampere", "asset-current-a"),
		hostQuantity(evse, "evse.ac.current", "b", "12", 0, "unit.ampere", "asset-current-b"),
		hostQuantity(evse, "evse.ac.current", "c", "13", 0, "unit.ampere", "asset-current-c"),
		hostQuantity(evse, "evse.ac.voltage", "a", "230", 0, "unit.volt", "asset-voltage-a"),
		hostQuantity(evse, "evse.ac.voltage", "b", "231", 0, "unit.volt", "asset-voltage-b"),
		hostQuantity(evse, "evse.ac.voltage", "c", "232", 0, "unit.volt", "asset-voltage-c"),
		hostQuantity(evse, "evse.ac.frequency", "", "50", 0, "unit.hertz", "asset-frequency"),
		hostQuantity(evse, "evse.energy.lifetime", "", "3600000", 0, "unit.joule", "asset-lifetime"),
		hostQuantity(evse, "evse.energy.lifetime_exported", "", "3600000", 0, "unit.joule", "asset-exported"),
		hostSymbol(evse, "evse.status.fault", "clear", "asset-fault"),
	}
}

func hostQuantity(subject, element, phase, coefficient string, exponent int32, unit, asset string) graph.LensFact {
	quantity := semreg.Quantity{Number: semreg.Decimal{Coefficient: coefficient, Exponent10: exponent}, Unit: semreg.DefinitionID(unit)}
	value := semreg.Value{Kind: semreg.ValueQuantity, Quantity: &quantity}
	return hostFact(subject, element, phase, asset, &value)
}

func hostSymbol(subject, element, token, asset string) graph.LensFact {
	value := semreg.Value{Kind: semreg.ValueSymbol, Symbol: &semreg.Symbol{Token: token}}
	return hostFact(subject, element, "", asset, &value)
}

func hostFact(subject, element, phase, asset string, value *semreg.Value) graph.LensFact {
	return graph.LensFact{
		AssetID: semreg.AssetID(asset),
		Definition: semreg.DefinitionRef{
			Pack: semreg.PackRef{ID: "helianthus.pack.evse", Version: "1.0.0"}, ID: semreg.DefinitionID(element), Version: "1.0.0",
		},
		Key:        semreg.FactKey{FactID: semreg.DefinitionID(element), Dimensions: phaseDimension(phase)},
		Subject:    semreg.ResourceID(subject),
		Candidates: []semreg.FactCandidate{{CandidateID: semreg.CandidateID("cand-" + asset), Value: value, Quality: hostQuality()}},
	}
}

func phaseDimension(phase string) []semreg.Dimension {
	if phase == "" {
		return []semreg.Dimension{}
	}
	text := phase
	return []semreg.Dimension{{ID: "evse.dimension.phase", Value: semreg.Value{Kind: semreg.ValueText, Text: &text}}}
}

func hostQuality() semreg.Quality {
	return semreg.Quality{
		Assertion: semreg.AssertionObserved, Qualification: semreg.QualificationQualified, Promotion: semreg.PromotionPromoted,
		Validity: semreg.ValidityGood, Availability: semreg.AvailabilityAvailable, Freshness: semreg.FreshnessFresh,
	}
}
