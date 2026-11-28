//go:build eebusreg_second_host

package eebusservicebridge_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	eebusdriver "github.com/Project-Helianthus/helianthus-eebusreg/driver"
	"github.com/Project-Helianthus/helianthus-eebusreg/driver/inbound"
	"github.com/Project-Helianthus/helianthus-semreg/semreg/host"
	"github.com/Project-Helianthus/helianthus-semreg/semreg/runtime/tree"
	semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"
	semdriver "github.com/Project-Helianthus/helianthus-semreg/semreg/v1/driver"
	"github.com/Project-Helianthus/helianthus-semreg/semreg/v1/graph"
	"github.com/Project-Helianthus/helianthus-semreg/semreg/v1/operation"
)

const (
	proofInstallation = "installation.synthetic"
	proofBridge       = "bridge.heating"
	proofInstanceName = "instance.heating"
)

type commandLog struct {
	mu   sync.Mutex
	sent []string
}

func (c *commandLog) record(kind, operationID, feature, entity, value string) error {
	c.mu.Lock()
	c.sent = append(c.sent, kind+" "+operationID+" "+feature+" "+entity+" "+value)
	c.mu.Unlock()
	return nil
}

func (c *commandLog) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.sent...)
}

type proofFactory struct {
	mu        sync.Mutex
	remotes   []inbound.Remote
	publisher *inbound.Publisher
	result    inbound.Result
	session   semdriver.Session
	err       error
	ready     chan struct{}
}

func (f *proofFactory) Descriptor() semdriver.Descriptor {
	return eebusdriver.NewFactory().Descriptor()
}

func (f *proofFactory) Plan(config semdriver.Config) ([]semdriver.InstancePlan, error) {
	key := semdriver.InstanceKey(config.Instance)
	if err := key.Validate(); err != nil {
		return nil, err
	}
	return []semdriver.InstancePlan{{Driver: eebusdriver.Kind, Instance: key}}, nil
}

func (f *proofFactory) Construct(_ context.Context, plan semdriver.InstancePlan) (semdriver.Instance, error) {
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	if plan.Driver != eebusdriver.Kind || string(plan.Instance) != proofInstanceName {
		return nil, errWrongInstance
	}
	return &proofInstance{factory: f}, nil
}

type proofInstance struct{ factory *proofFactory }

func (i *proofInstance) Start(ctx context.Context, session semdriver.Session) error {
	publisher, err := inbound.New(inbound.Scope{Installation: proofInstallation, Instance: proofInstanceName, Bridge: proofBridge})
	if err != nil {
		return err
	}
	result, err := publisher.Apply(ctx, session, i.factory.remotes)
	i.factory.mu.Lock()
	i.factory.publisher = publisher
	i.factory.result = result
	i.factory.session = session
	i.factory.err = err
	i.factory.mu.Unlock()
	close(i.factory.ready)
	return err
}

func (i *proofInstance) FenceWithdrawal() {}

func (i *proofInstance) Stop(context.Context) semdriver.CloseReport {
	return semdriver.CloseReport{Confirmed: true}
}

type wrongInstanceError struct{}

func (wrongInstanceError) Error() string { return "plan is for another instance" }

var errWrongInstance = wrongInstanceError{}

func replyReading(field, text, unit, phase string) inbound.Reading {
	return inbound.Reading{
		Field: field, Phase: phase, Text: text, Unit: unit, Rank: "producer", Carrier: "reply", Generation: 4, ObservedUnix: 400,
	}
}

func heatingPeer() inbound.Remote {
	setpoint := replyReading("room_temperature_setpoint", "20", "degC", "")
	setpoint.Writable = true
	setpoint.Feature = "feature.room.setpoint.circuit"
	mode := replyReading("room_mode", "on", "", "")
	mode.Writable = true
	mode.Feature = "feature.room.mode.circuit"
	otherSetpoint := replyReading("room_temperature_setpoint", "19", "degC", "")
	otherSetpoint.Writable = true
	otherSetpoint.Feature = "feature.room.setpoint.zoned"
	otherMode := replyReading("room_mode", "boost", "", "")
	otherMode.Writable = true
	otherMode.Feature = "feature.room.mode.zoned"
	return inbound.Remote{
		Device: "device.heat", SKI: "synthetic-ship-heat", Ready: true, Generation: 4,
		Entities: []inbound.Entity{
			{Type: "HeatingCircuit", Address: []uint64{7}},
			{Type: "HeatingZone", Address: []uint64{8}, Parent: []uint64{7}, Name: "east", Readings: []inbound.Reading{
				replyReading("air_temperature", "22", "degC", ""), replyReading("temperature_setpoint", "23", "degC", ""), replyReading("zone_mode", "eco", "", ""),
			}},
			{Type: "HVACRoom", Address: []uint64{6}, Parent: []uint64{7}, Readings: []inbound.Reading{
				replyReading("room_air_temperature", "19", "degC", ""), setpoint, mode,
			}},
			{Type: "HVACRoom", Address: []uint64{9}, Parent: []uint64{8}, Readings: []inbound.Reading{
				replyReading("room_air_temperature", "18", "degC", ""), otherSetpoint, otherMode,
			}},
			{Type: "DHWCircuit", Address: []uint64{3}, Readings: []inbound.Reading{
				replyReading("dhw_temperature", "48.5", "degC", ""), replyReading("dhw_temperature_setpoint", "50", "degC", ""),
			}},
			{Type: "TemperatureSensor", Address: []uint64{10}, Readings: []inbound.Reading{replyReading("outdoor_air_temperature", "-5", "degC", "")}},
		},
		Readings: []inbound.Reading{replyReading("one_time_dhw", "active", "", "")},
	}
}

func gridPeer() inbound.Remote {
	return inbound.Remote{
		Device: "device.grid", SKI: "synthetic-ship-grid", Ready: true, Generation: 4,
		Entities: []inbound.Entity{
			{Type: "GridConnectionPointOfPremises", Address: []uint64{1}, Readings: []inbound.Reading{
				replyReading("power", "-2500", "W", ""), replyReading("frequency", "50", "Hz", ""), replyReading("powerLimitationFactor", "50", "%", ""),
				replyReading("energyFeedIn", "3", "Wh", ""), replyReading("energyConsumed", "4", "Wh", ""),
				replyReading("currentPerPhase", "10", "A", "a"), replyReading("currentPerPhase", "11", "A", "b"), replyReading("currentPerPhase", "12", "A", "c"),
				replyReading("voltagePerPhase", "230", "V", "a"), replyReading("voltagePerPhase", "231", "V", "b"), replyReading("voltagePerPhase", "232", "V", "c"),
			}},
			{Type: "HeatPumpAppliance", Address: []uint64{2}, Readings: []inbound.Reading{
				replyReading("power", "3500", "W", ""), replyReading("frequency", "50", "Hz", ""), replyReading("energyConsumed", "2", "Wh", ""),
				replyReading("powerPerPhase", "1000", "W", "a"), replyReading("powerPerPhase", "1100", "W", "b"), replyReading("powerPerPhase", "1200", "W", "c"),
			}},
		},
	}
}

func chargerPeer() inbound.Remote {
	return inbound.Remote{
		Device: "device.charger", SKI: "synthetic-ship-charge", Ready: true, Generation: 4,
		Entities: []inbound.Entity{
			{Type: "EVSE", Address: []uint64{1}, Readings: []inbound.Reading{
				replyReading("deviceName", "synthetic-station", "", ""), replyReading("operatingState", "normalOperation", "", ""),
				replyReading("currentPerPhase", "10", "A", "a"), replyReading("currentPerPhase", "11", "A", "b"), replyReading("currentPerPhase", "12", "A", "c"),
				replyReading("powerPerPhase", "1000", "W", "a"), replyReading("powerPerPhase", "1100", "W", "b"), replyReading("powerPerPhase", "1200", "W", "c"),
				replyReading("energyCharged", "5", "Wh", ""),
			}},
			{Type: "EV", Address: []uint64{2}, Readings: []inbound.Reading{
				replyReading("connected", "true", "", ""), replyReading("chargeState", "normalOperation", "", ""),
			}},
		},
	}
}

func assertResolvedCatalog(t *testing.T) {
	t.Helper()
	catalog, err := graph.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	curtailment, ok := catalog.FactSubject(semreg.PackRef{ID: "helianthus.pack.infrastructure", Version: "1.0.0"}, "infrastructure.limit.pv_curtailment_factor")
	if !ok || !curtailment.Resolved || curtailment.Kind != "resource.grid_connection" {
		t.Fatalf("curtailment subject resolved=%v kind=%s", ok, curtailment.Kind)
	}
	room, ok := catalog.FactSubject(semreg.PackRef{ID: "helianthus.pack.thermal", Version: "1.0.0"}, "thermal.room.mode_zone")
	if !ok || !room.Resolved || room.Kind != "resource.room" {
		t.Fatalf("room mode subject resolved=%v kind=%s", ok, room.Kind)
	}
}

func captureCut(t *testing.T, ctx context.Context, site *host.Site) semdriver.SemanticCut {
	t.Helper()
	cut, err := site.Tree().SemanticInput(semdriver.Filter{}).Capture(ctx, semdriver.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	return cut
}

func resourcesOfKind(cut semdriver.SemanticCut, kind string) []semreg.Resource {
	var out []semreg.Resource
	for _, snap := range cut.Snapshots {
		for _, resource := range snap.Resources {
			if string(resource.Kind) == kind {
				out = append(out, resource)
			}
		}
	}
	return out
}

func roomByFragment(t *testing.T, cut semdriver.SemanticCut, fragment string) semreg.Resource {
	t.Helper()
	for _, resource := range resourcesOfKind(cut, "resource.room") {
		if resource.Anchor.Slot != nil && strings.Contains(*resource.Anchor.Slot, fragment) {
			return resource
		}
	}
	t.Fatalf("no room slot %s", fragment)
	return semreg.Resource{}
}

func findCandidate(cut semdriver.SemanticCut, factID string, match func(semreg.FactCandidate) bool) (semreg.FactCandidate, bool) {
	for _, snap := range cut.Snapshots {
		for _, envelope := range snap.Facts {
			if string(envelope.Key.FactID) != factID {
				continue
			}
			for _, candidate := range envelope.Candidates {
				if match(candidate) {
					return candidate, true
				}
			}
		}
	}
	return semreg.FactCandidate{}, false
}

func requireQuantity(t *testing.T, cut semdriver.SemanticCut, factID, coefficient string, exponent int32, unit string) {
	t.Helper()
	_, ok := findCandidate(cut, factID, func(candidate semreg.FactCandidate) bool {
		return candidate.Value != nil && candidate.Value.Quantity != nil &&
			candidate.Value.Quantity.Number.Coefficient == coefficient &&
			candidate.Value.Quantity.Number.Exponent10 == exponent &&
			string(candidate.Value.Quantity.Unit) == unit
	})
	if !ok {
		t.Fatalf("missing %s coefficient %s exponent %d unit %s", factID, coefficient, exponent, unit)
	}
}

func requireQuantityOn(t *testing.T, cut semdriver.SemanticCut, factID, coefficient string, exponent int32, unit string, subject semreg.ResourceID) {
	t.Helper()
	_, ok := findCandidate(cut, factID, func(candidate semreg.FactCandidate) bool {
		if candidate.Value == nil || candidate.Value.Quantity == nil ||
			candidate.Value.Quantity.Number.Coefficient != coefficient ||
			candidate.Value.Quantity.Number.Exponent10 != exponent ||
			string(candidate.Value.Quantity.Unit) != unit {
			return false
		}
		return dimensionNames(candidate, subject)
	})
	if !ok {
		t.Fatalf("missing %s coefficient %s on the named subject", factID, coefficient)
	}
}

func requireSymbolOn(t *testing.T, cut semdriver.SemanticCut, factID, token string, subject semreg.ResourceID) {
	t.Helper()
	_, ok := findCandidate(cut, factID, func(candidate semreg.FactCandidate) bool {
		if candidate.Value == nil || candidate.Value.Symbol == nil || candidate.Value.Symbol.Token != token {
			return false
		}
		for _, dimension := range candidate.Key.Dimensions {
			if dimension.Value.Text != nil && *dimension.Value.Text == string(subject) {
				return true
			}
		}
		return false
	})
	if !ok {
		t.Fatalf("missing %s token %s on the named subject", factID, token)
	}
}

func relationCount(cut semdriver.SemanticCut, id string) int {
	count := 0
	for _, snap := range cut.Snapshots {
		for _, relation := range snap.Relations {
			if string(relation.Definition.ID) == id {
				count++
			}
		}
	}
	return count
}

func roomSetpointRequest(t *testing.T, cut semdriver.SemanticCut, room semreg.Resource) *semdriver.OperationRequest {
	t.Helper()
	var binding semreg.NativeBinding
	var capability semreg.CapabilityInstance
	var asset semreg.AssetID
	var revisions semreg.RevisionVector
	found := false
	for _, snap := range cut.Snapshots {
		for _, envelope := range snap.Facts {
			if string(envelope.Key.FactID) != "thermal.room.setpoint_temperature_setpoint" {
				continue
			}
			for _, candidate := range envelope.Candidates {
				if candidate.BindingID == nil || !dimensionNames(candidate, room.ResourceID) {
					continue
				}
				for _, item := range snap.Bindings {
					if item.BindingID == *candidate.BindingID && item.State == semreg.BindingCurrent {
						binding = item
						found = true
					}
				}
				for _, item := range snap.Capabilities {
					if item.BindingID == binding.BindingID && item.Definition.ID == "thermal.room.capability_set_temperature" {
						capability = item
					}
				}
				asset = snap.AssetID
				revisions = snap.Revisions
			}
		}
	}
	if !found || capability.InstanceID == "" || capability.Definition.ID != "thermal.room.capability_set_temperature" {
		t.Fatal("room setpoint binding is not current")
	}
	if len(capability.Constraints) != 1 || capability.Constraints[0].ID != "thermal.room.setpoint_temperature_setpoint" || capability.Constraints[0].Value.Quantity == nil {
		t.Fatalf("room setpoint constraint %+v", capability.Constraints)
	}
	quantity := *capability.Constraints[0].Value.Quantity
	if quantity.Number.Coefficient != "29315" || quantity.Number.Exponent10 != -2 || quantity.Unit != "unit.kelvin" {
		t.Fatalf("circuit room constraint %+v", quantity)
	}
	operationRef := semreg.DefinitionRef{
		Pack:    semreg.PackRef{ID: "helianthus.pack.thermal", Version: "1.0.0"},
		ID:      "thermal.room.operation_set_temperature",
		Version: "1.0.0",
	}
	value := semreg.Value{Kind: semreg.ValueQuantity, Quantity: &quantity}
	return &semdriver.OperationRequest{
		AssetID: asset, BindingID: binding.BindingID, CapabilityInstanceID: capability.InstanceID, Operation: operationRef,
		Arguments:                          []semreg.TypedField{{ID: "thermal.room.setpoint_temperature_setpoint", Value: value}},
		ExpectedSemanticRevision:           revisions.Semantic,
		ExpectedCapabilityRevision:         revisions.Capabilities,
		ExpectedCapabilityInstanceRevision: capability.Revision,
		ExpectedDriverGeneration:           binding.DriverGeneration, ExpectedSourceEpochID: binding.SourceEpochID,
		Preconditions: []operation.Precondition{}, Timeout: 2 * time.Minute,
	}
}

func dimensionNames(candidate semreg.FactCandidate, subject semreg.ResourceID) bool {
	for _, dimension := range candidate.Key.Dimensions {
		if dimension.Value.Text != nil && *dimension.Value.Text == string(subject) {
			return true
		}
	}
	return false
}

func invokeRequest(t *testing.T, ctx context.Context, site *host.Site, request *semdriver.OperationRequest) tree.Outcome {
	t.Helper()
	segment, err := tree.DefinitionSegment(request.Operation)
	if err != nil {
		t.Fatal(err)
	}
	method := tree.MethodRef(tree.Key{
		Node: tree.NodePath{Installation: proofInstallation}, Facet: tree.KeywordMethods, Domain: tree.KeywordSem, Segment: segment,
	}.String())
	cut, err := site.Cut(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := cut.Close(); closeErr != nil {
			t.Errorf("cut close: %v", closeErr)
		}
	}()
	prepared, err := cut.Prepare(ctx, host.MethodSelection{Method: method, Input: tree.Input{Request: request}})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := site.Invoke(ctx, prepared)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := site.WaitOutcome(ctx, receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	return outcome
}

func findResultFact(result inbound.Result, id, native string) (inbound.FactView, bool) {
	for _, fact := range result.Facts {
		if fact.FactID == id && fact.Native == native && !fact.Withdrawn {
			return fact, true
		}
	}
	return inbound.FactView{}, false
}

func factView(t *testing.T, result inbound.Result, id, native string) inbound.FactView {
	t.Helper()
	fact, ok := findResultFact(result, id, native)
	if !ok {
		t.Fatalf("missing result fact %s native %s", id, native)
	}
	return fact
}

func TestSecondHostReadsAndWrites(t *testing.T) {
	assertResolvedCatalog(t)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	log := &commandLog{}
	heating := heatingPeer()
	heating.Command = log.record
	factory := &proofFactory{remotes: []inbound.Remote{heating, gridPeer(), chargerPeer()}, ready: make(chan struct{})}
	calendar := "UTC"
	site, err := host.Open(ctx, host.Config{
		InstallationKey: proofInstallation, BridgeID: proofBridge, StateDir: t.TempDir(), Calendar: &calendar,
		Instances: []host.InstanceConfig{{Kind: eebusdriver.Kind, Instance: proofInstanceName, Enabled: true}},
	}, factory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		closeCtx, cancelClose := context.WithTimeout(context.Background(), time.Minute)
		defer cancelClose()
		if closeErr := site.Close(closeCtx); closeErr != nil {
			t.Errorf("close: %v", closeErr)
		}
	}()
	if err := site.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := site.WaitInitial(ctx); err != nil {
		factory.mu.Lock()
		startErr := factory.err
		factory.mu.Unlock()
		t.Fatalf("wait initial: %v; start: %v", err, startErr)
	}
	select {
	case <-factory.ready:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	factory.mu.Lock()
	result, publisher, session, startErr := factory.result, factory.publisher, factory.session, factory.err
	factory.mu.Unlock()
	if startErr != nil {
		t.Fatal(startErr)
	}
	if len(result.LastNotified) != 0 {
		t.Fatalf("reply publication notified %d facts", len(result.LastNotified))
	}
	if _, ok := findResultFact(result, "thermal.measurement.dhw_temperature", "48.5"); !ok {
		sample := make([]string, 0, len(result.Facts))
		for _, fact := range result.Facts {
			sample = append(sample, fact.FactID+" "+fact.Native+" "+fact.SubjectKind)
		}
		t.Fatalf("facts %d nodes %d gaps %v sample %v", len(result.Facts), len(result.Nodes), result.Gaps, sample)
	}
	dhw := factView(t, result, "thermal.measurement.dhw_temperature", "48.5")
	if dhw.Carrier != "reply" || dhw.Coefficient != "32165" || dhw.Exponent != -2 || dhw.Generation != 4 {
		t.Fatalf("reply observation = %+v", dhw)
	}
	cut := captureCut(t, ctx, site)
	requireQuantity(t, cut, "thermal.measurement.dhw_temperature", "32165", -2, "unit.kelvin")
	requireQuantity(t, cut, "thermal.measurement.outdoor_air_temperature", "26815", -2, "unit.kelvin")
	requireQuantity(t, cut, "infrastructure.limit.pv_curtailment_factor", "5", -1, "unit.ratio")
	requireQuantity(t, cut, "infrastructure.power.pcc", "-25", 2, "unit.watt")
	circuitRoom := roomByFragment(t, cut, "/entity/6")
	zoneRoom := roomByFragment(t, cut, "/entity/9")
	requireSymbolOn(t, cut, "thermal.room.mode_zone", "on", circuitRoom.ResourceID)
	if _, ok := findCandidate(cut, "thermal.room.mode_zone", func(candidate semreg.FactCandidate) bool {
		return candidate.Value != nil && candidate.Value.Symbol != nil && candidate.Value.Symbol.Token == "on" && dimensionNames(candidate, zoneRoom.ResourceID)
	}); ok {
		t.Fatal("room mode on was cloned onto the zone room")
	}
	if relationCount(cut, "thermal.relation.system_member") == 0 {
		t.Fatal("system membership was not published")
	}
	if relationCount(cut, "thermal.relation.circuit_room") != 0 || relationCount(cut, "thermal.relation.circuit_zone") != 0 || relationCount(cut, "thermal.relation.zone_room") != 0 {
		t.Fatalf("configuration-only figure relations were instantiated circuit_room %d circuit_zone %d zone_room %d", relationCount(cut, "thermal.relation.circuit_room"), relationCount(cut, "thermal.relation.circuit_zone"), relationCount(cut, "thermal.relation.zone_room"))
	}
	gaps := map[string]bool{}
	for _, gap := range result.Gaps {
		gaps[gap] = true
	}
	for _, relation := range []string{"thermal.relation.circuit_room", "thermal.relation.circuit_zone", "thermal.relation.zone_room"} {
		if !gaps["device.heat:configuration_only_relation:"+relation] {
			t.Fatalf("missing configuration-only gap for %s in %v", relation, result.Gaps)
		}
	}
	if len(resourcesOfKind(cut, "resource.grid_connection")) != 1 || len(resourcesOfKind(cut, "resource.evse")) != 1 || len(resourcesOfKind(cut, "resource.connector")) != 1 {
		t.Fatal("grid or charger resources were not published")
	}
	_, connected := findCandidate(cut, "evse.status.connection", func(candidate semreg.FactCandidate) bool {
		return candidate.Value != nil && candidate.Value.Symbol != nil && candidate.Value.Symbol.Token == "connected"
	})
	_, charging := findCandidate(cut, "evse.status.charging", func(candidate semreg.FactCandidate) bool {
		return candidate.Value != nil && candidate.Value.Symbol != nil && candidate.Value.Symbol.Token == "charging"
	})
	if !connected || !charging {
		t.Fatal("charger status was not published")
	}
	request := roomSetpointRequest(t, cut, circuitRoom)
	if request.ExpectedDriverGeneration == "" {
		t.Fatal("stamped generation is empty")
	}
	outcome := invokeRequest(t, ctx, site, request)
	if outcome.Operation == nil || outcome.Operation.KernelOutcome != operation.OutcomeAcknowledgedUnverified {
		t.Fatalf("outcome %+v", outcome.Operation)
	}
	sent := log.snapshot()
	if len(sent) != 1 || sent[0] != "semreg thermal.room.operation_set_temperature feature.room.setpoint.circuit 6 29315" {
		t.Fatalf("native command %v", sent)
	}
	readback := captureCut(t, ctx, site)
	requireQuantityOn(t, readback, "thermal.room.setpoint_temperature_setpoint", "29315", -2, "unit.kelvin", circuitRoom.ResourceID)
	requireQuantityOn(t, readback, "thermal.room.setpoint_temperature_setpoint", "29215", -2, "unit.kelvin", zoneRoom.ResourceID)
	if _, err := publisher.StopGeneration(ctx, session, heating.SKI, 4); err != nil {
		t.Fatal(err)
	}
	late := heatingPeer()
	for i := range late.Entities {
		if late.Entities[i].Type != "DHWCircuit" {
			continue
		}
		for j := range late.Entities[i].Readings {
			if late.Entities[i].Readings[j].Field == "dhw_temperature" {
				late.Entities[i].Readings[j].Text = "49"
			}
		}
	}
	lateResult, err := publisher.Apply(ctx, session, []inbound.Remote{late})
	if err != nil {
		t.Fatal(err)
	}
	kept := factView(t, lateResult, "thermal.measurement.dhw_temperature", "48.5")
	if kept.Coefficient != "32165" || kept.Freshness != "stale" {
		t.Fatalf("retired reply replaced the observation: %+v", kept)
	}
	final := captureCut(t, ctx, site)
	requireQuantity(t, final, "thermal.measurement.dhw_temperature", "32165", -2, "unit.kelvin")
	requireQuantityOn(t, final, "thermal.room.setpoint_temperature_setpoint", "29315", -2, "unit.kelvin", circuitRoom.ResourceID)
}
