package inbound

import (
	"context"
	"strings"
	"testing"

	semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"
	"github.com/Project-Helianthus/helianthus-semreg/semreg/v1/driver"
	"github.com/Project-Helianthus/helianthus-semreg/semreg/v1/operation"
)

func writableRoom(address uint64, feature string, generation uint64) Remote {
	reading := heat("room_temperature_setpoint", "20")
	reading.Writable = true
	reading.Feature = feature
	reading.Generation = generation
	return Remote{
		Device: "device.heat", SKI: "synthetic-ship-ski", Ready: true, Generation: generation,
		Entities: []Entity{{Type: "HVACRoom", Address: []uint64{address}, Readings: []Reading{reading}}},
	}
}

func onlyRoute(t *testing.T, result Result, operationID string) driver.SettingRoute {
	t.Helper()
	var found driver.SettingRoute
	count := 0
	for _, route := range result.Routes {
		if string(route.Operation.ID) != operationID {
			continue
		}
		found = route
		count++
	}
	if count != 1 {
		t.Fatalf("routes for %s = %d", operationID, count)
	}
	return found
}

func routeCount(result Result, operationID string) int {
	count := 0
	for _, route := range result.Routes {
		if string(route.Operation.ID) == operationID {
			count++
		}
	}
	return count
}

func executorFor(t *testing.T, session *host, route driver.SettingRoute) driver.Executor {
	t.Helper()
	var exec driver.Executor
	for i, declared := range session.routes {
		if declared.Owner.PathKey == route.Owner.PathKey && declared.Operation.ID == route.Operation.ID {
			exec = session.executors[i]
		}
	}
	if exec == nil {
		t.Fatalf("missing executor for %s", route.Operation.ID)
	}
	return exec
}

func quantityValue(coefficient string, exponent int32, unit string) semreg.Value {
	return semreg.Value{Kind: semreg.ValueQuantity, Quantity: &semreg.Quantity{
		Number: semreg.Decimal{Coefficient: coefficient, Exponent10: exponent}, Unit: semreg.DefinitionID(unit),
	}}
}

func dispatchFor(route driver.SettingRoute, value semreg.Value) driver.Dispatch {
	return driver.Dispatch{
		Request: driver.OperationRequest{Arguments: []semreg.TypedField{{ID: route.For.Key.FactID, Value: value}}},
		Route:   route.Route,
		Guard: operation.GuardClaims{
			CapabilityInstance: route.Route.CapabilityInstance, ServiceInstance: route.Route.ServiceInstance,
			BindingID: route.Route.BindingID, SourceID: route.Route.SourceID, SourceEpochID: route.Route.SourceEpochID,
			DriverGeneration: route.Route.DriverGeneration,
		},
		AdmittedAt: timePoint(0),
	}
}

func invokeRoute(t *testing.T, session *host, route driver.SettingRoute, value semreg.Value) driver.Completion {
	t.Helper()
	completion, err := executorFor(t, session, route).Execute(context.Background(), dispatchFor(route, value))
	if err != nil {
		t.Fatal(err)
	}
	if err := completion.Validate(); err != nil {
		t.Fatal(err)
	}
	return completion
}

func setpointCoefficient(t *testing.T, batches []semreg.PublicationBatch, subject semreg.ResourceID) string {
	t.Helper()
	if len(batches) == 0 {
		t.Fatal("no publication batch")
	}
	batch := batches[len(batches)-1]
	for _, candidate := range batch.FactUpserts {
		if candidate.Key.FactID != "thermal.room.setpoint_temperature_setpoint" || candidate.Value == nil || candidate.Value.Quantity == nil {
			continue
		}
		for _, dimension := range candidate.Key.Dimensions {
			if dimension.Value.Text != nil && semreg.ResourceID(*dimension.Value.Text) == subject {
				return candidate.Value.Quantity.Number.Coefficient
			}
		}
	}
	t.Fatal("room setpoint is missing from the last batch")
	return ""
}

func callMethod(t *testing.T, session *host, local, arg string) ([]byte, error) {
	t.Helper()
	suffix := "\n" + local
	var fn driver.NativeMethod
	for key, method := range session.methodFns {
		if !strings.HasSuffix(key, suffix) {
			continue
		}
		if fn != nil {
			t.Fatalf("duplicate method %s", local)
		}
		fn = method
	}
	if fn == nil {
		t.Fatalf("missing method %s", local)
	}
	return fn(context.Background(), []byte(arg))
}

func TestDeclaredWritableFieldsGainRoutes(t *testing.T) {
	remote := heatingRemote()
	remote.Readings[0].Writable = true
	remote.Readings[0].Feature = "feature.system.overrun"
	for i := range remote.Entities {
		for j := range remote.Entities[i].Readings {
			reading := &remote.Entities[i].Readings[j]
			switch reading.Field {
			case "temperature_setpoint", "zone_mode", "dhw_temperature_setpoint", "dhw_mode":
				reading.Writable = true
				reading.Feature = "feature." + reading.Field
			}
		}
	}
	result, session := publish(t, heatingScope("instance.heating"), remote)
	if routeCount(result, "thermal.operation.set_temperature") != 3 {
		t.Fatalf("zone setpoint routes = %d", routeCount(result, "thermal.operation.set_temperature"))
	}
	if routeCount(result, "thermal.operation.set_zone_mode") != 3 {
		t.Fatalf("zone mode routes = %d", routeCount(result, "thermal.operation.set_zone_mode"))
	}
	if routeCount(result, "thermal.operation.set_dhw_temperature") != 1 || routeCount(result, "thermal.operation.set_dhw_mode") != 1 {
		t.Fatalf("dhw routes missing: %+v", result.Routes)
	}
	overrun := onlyRoute(t, result, "thermal.operation.set_overrun_one_time_dhw")
	if overrun.Route.DriverGeneration != "4" {
		t.Fatalf("overrun generation = %s", overrun.Route.DriverGeneration)
	}
	for _, route := range result.Routes {
		if string(route.Operation.ID) == "thermal.operation.set_overrun_one_time_dhw" {
			for _, node := range result.Nodes {
				if node.ID == route.For.Subject.ID && node.Key.Kind != "resource.thermal_system" {
					t.Fatalf("overrun subject kind = %s", node.Key.Kind)
				}
			}
		}
	}
	if len(session.routes) != len(result.Routes) || len(session.executors) != len(session.routes) {
		t.Fatalf("registered %d executors for %d routes", len(session.executors), len(result.Routes))
	}
	if session.routes[0].Owner.Producer != session.Root() {
		t.Fatal("bindExecutors left a foreign producer on the registered route")
	}
	if result.Routes[0].Owner.Producer == session.Root() {
		t.Fatal("the returned route producer was replaced by the session root")
	}
}

func TestWritableRouteSendsOnInvoke(t *testing.T) {
	var calls []string
	remote := writableRoom(6, "feature.room.setpoint", 4)
	remote.Command = func(kind, operationID, feature, entity, value string) error {
		calls = append(calls, kind+" "+operationID+" "+feature+" "+entity+" "+value)
		return nil
	}
	publisher, err := New(heatingScope("instance.heating"))
	if err != nil {
		t.Fatal(err)
	}
	session := newHost()
	result, err := publisher.Apply(context.Background(), session, []Remote{remote})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("registration sent %v", calls)
	}
	route := onlyRoute(t, result, "thermal.room.operation_set_temperature")
	if route.Route.DriverGeneration != "4" || !strings.Contains(route.Owner.PathKey, "/entity/6/") {
		t.Fatalf("route = %+v", route)
	}
	before := setpointCoefficient(t, session.batches, route.For.Subject.ID)
	completion := invokeRoute(t, session, route, quantityValue("29415", -2, "unit.kelvin"))
	if completion.Outcome != operation.OutcomeAcknowledgedUnverified || completion.Dispatch == nil || completion.Dispatch.Delivery != operation.DeliverySent {
		t.Fatalf("completion = %+v", completion)
	}
	if len(calls) != 1 || calls[0] != "semreg thermal.room.operation_set_temperature feature.room.setpoint 6 29415" {
		t.Fatalf("command = %v", calls)
	}
	if before != "29315" || setpointCoefficient(t, session.batches, route.For.Subject.ID) != "29415" {
		t.Fatalf("readback before %s after %s", before, setpointCoefficient(t, session.batches, route.For.Subject.ID))
	}
	remote.SKI = "synthetic-ship-other"
	remote.Command = func(kind, operationID, feature, entity, value string) error {
		calls = append(calls, "other "+kind+" "+entity)
		return nil
	}
	result, err = publisher.Apply(context.Background(), session, []Remote{remote})
	if err != nil {
		t.Fatal(err)
	}
	route = onlyRoute(t, result, "thermal.room.operation_set_temperature")
	completion = invokeRoute(t, session, route, quantityValue("29415", -2, "unit.kelvin"))
	if completion.Outcome != operation.OutcomeAcknowledgedUnverified || len(calls) != 2 || calls[1] != "other semreg 6" {
		t.Fatalf("ski callback = %v outcome %s", calls, completion.Outcome)
	}
}

func TestInvokeRefusesABlockedSend(t *testing.T) {
	var calls int
	remote := writableRoom(6, "feature.room.setpoint", 4)
	remote.Command = func(string, string, string, string, string) error {
		calls++
		return nil
	}
	publisher, err := New(heatingScope("instance.heating"))
	if err != nil {
		t.Fatal(err)
	}
	session := newHost()
	result, err := publisher.Apply(context.Background(), session, []Remote{remote})
	if err != nil {
		t.Fatal(err)
	}
	route := onlyRoute(t, result, "thermal.room.operation_set_temperature")
	exec := executorFor(t, session, route)
	mismatch := dispatchFor(route, quantityValue("29415", -2, "unit.kelvin"))
	mismatch.Guard.BindingID = "binding.other"
	blocked, err := exec.Execute(context.Background(), mismatch)
	if err != nil || blocked.Outcome != operation.OutcomeFailedNoContact || calls != 0 {
		t.Fatalf("guard mismatch outcome %s calls %d err %v", blocked.Outcome, calls, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	blocked, err = exec.Execute(ctx, dispatchFor(route, quantityValue("29415", -2, "unit.kelvin")))
	if err != nil || blocked.Outcome != operation.OutcomeFailedNoContact || calls != 0 {
		t.Fatalf("cancelled outcome %s calls %d", blocked.Outcome, calls)
	}
	empty := dispatchFor(route, quantityValue("29415", -2, "unit.kelvin"))
	empty.Request.Arguments = nil
	blocked, err = exec.Execute(context.Background(), empty)
	if err != nil || blocked.Outcome != operation.OutcomeFailedNoContact || calls != 0 {
		t.Fatalf("empty arguments outcome %s calls %d", blocked.Outcome, calls)
	}
	if _, err := publisher.StopGeneration(context.Background(), session, remote.SKI, 4); err != nil {
		t.Fatal(err)
	}
	stopped := onlyRoute(t, result, "thermal.room.operation_set_temperature")
	blocked = invokeRoute(t, session, stopped, quantityValue("29415", -2, "unit.kelvin"))
	if blocked.Outcome != operation.OutcomeFailedNoContact || calls != 0 {
		t.Fatalf("stopped generation outcome %s calls %d", blocked.Outcome, calls)
	}
	if _, err := publisher.Revoke(context.Background(), session, remote.SKI); err != nil {
		t.Fatal(err)
	}
	revoked, err := publisher.Apply(context.Background(), session, []Remote{remote})
	if err != nil {
		t.Fatal(err)
	}
	if routeCount(revoked, "thermal.room.operation_set_temperature") != 0 || calls != 0 {
		t.Fatalf("revoked routes %d calls %d", routeCount(revoked, "thermal.room.operation_set_temperature"), calls)
	}
	silent := writableRoom(6, "feature.room.setpoint", 4)
	silent.SKI = "synthetic-ship-silent"
	silentResult, err := publisher.Apply(context.Background(), session, []Remote{silent})
	if err != nil {
		t.Fatal(err)
	}
	completion := invokeRoute(t, session, onlyRoute(t, silentResult, "thermal.room.operation_set_temperature"), quantityValue("29415", -2, "unit.kelvin"))
	if completion.Outcome != operation.OutcomeFailedNoContact || calls != 0 {
		t.Fatalf("nil command outcome %s calls %d", completion.Outcome, calls)
	}
}

func TestMissingFeatureDoesNotSend(t *testing.T) {
	var calls int
	reading := mode("room_mode", "on")
	reading.Writable = true
	remote := Remote{
		Device: "device.heat", SKI: "synthetic-ship-ski", Ready: true, Generation: 4,
		Command: func(string, string, string, string, string) error {
			calls++
			return nil
		},
		Entities: []Entity{{Type: "HVACRoom", Address: []uint64{6}, Readings: []Reading{reading}}},
	}
	result, session := publish(t, heatingScope("instance.heating"), remote)
	if len(session.routes) != 0 || !gapSet(result)["device.heat:feature_missing:thermal.room.operation_set_zone_mode"] {
		t.Fatalf("routes %d gaps %v", len(session.routes), result.Gaps)
	}
	if _, err := callMethod(t, session, "write.room_mode", "on"); err == nil || calls != 0 {
		t.Fatalf("missing feature sent err %v calls %d", err, calls)
	}
}

func TestNamespaceWriteSendsOnCall(t *testing.T) {
	var calls []string
	record := func(kind, operationID, feature, entity, value string) error {
		calls = append(calls, kind+" "+operationID+" "+feature+" "+entity+" "+value)
		return nil
	}
	remote := Remote{
		Device: "device.heat", SKI: "synthetic-ship-ski", Ready: true, Generation: 4, Command: record,
		Entities: []Entity{{Type: "HVACRoom", Address: []uint64{6}, Readings: []Reading{{
			Field: "label_text", Text: "north", Writable: true, Feature: "feature.label", Rank: "producer", Generation: 4, ObservedUnix: 400,
		}}}},
	}
	result, session := publish(t, heatingScope("instance.heating"), remote)
	if len(result.Routes) != 0 || !gapSet(result)["device.heat:field:label_text"] {
		t.Fatalf("routes %d gaps %v", len(result.Routes), result.Gaps)
	}
	if len(calls) != 0 {
		t.Fatalf("registration sent %v", calls)
	}
	if _, err := callMethod(t, session, "write.label_text", "south"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0] != "namespace write.label_text feature.label 6 south" {
		t.Fatalf("namespace command = %v", calls)
	}
	grid := gridRemote()
	grid.Command = record
	grid.Entities[0].Readings[0].Writable = true
	grid.Entities[0].Readings[0].Feature = "feature.grid.power"
	gridResult, gridSession := publish(t, electricalScope("instance.grid"), grid)
	if len(gridResult.Routes) != 0 {
		t.Fatalf("measurement route %+v", gridResult.Routes)
	}
	if _, err := callMethod(t, gridSession, "write.power", "-2500"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[1] != "namespace write.power feature.grid.power 1 -2500" {
		t.Fatalf("grid command = %v", calls)
	}
}

func TestCommandUseCasesNeedTheirSubject(t *testing.T) {
	var calls int
	remote := chargerRemote(true, "normalOperation", "normalOperation")
	remote.Commands = []string{"overload_protection", "self_consumption", "coordinated_charging"}
	remote.Command = func(kind, operationID, feature, entity, value string) error {
		calls++
		if kind != "semreg" || operationID != "evse.operation.set_allocated_current" || feature != "LoadControl" || entity != "1" || value != "16" {
			t.Fatalf("overload command %s %s %s %s %s", kind, operationID, feature, entity, value)
		}
		return nil
	}
	result, session := publish(t, electricalScope("instance.charger"), remote)
	if calls != 0 {
		t.Fatal("registration sent a command")
	}
	counts := map[string]int{}
	for _, route := range result.Routes {
		counts[string(route.Operation.ID)]++
		if route.Route.DriverGeneration != "4" {
			t.Fatalf("generation %s", route.Route.DriverGeneration)
		}
	}
	if counts["evse.operation.set_allocated_current"] != 1 || counts["evse.operation.set_recommended_current_phase"] != 3 {
		t.Fatalf("counts %+v", counts)
	}
	if counts["energy.operation.modify_series_slot"] != 1 || counts["energy.operation.write_incentive_slot"] != 1 || len(result.Routes) != 6 {
		t.Fatalf("counts %+v routes %d", counts, len(result.Routes))
	}
	for _, fact := range result.Facts {
		switch fact.FactID {
		case "evse.limit.allocated_current", "evse.limit.recommended_current_phase", "energy.series.slot_power_nominal", "energy.incentive.incentive_value":
			t.Fatalf("invented command fact %s", fact.FactID)
		}
	}
	overload := onlyRoute(t, result, "evse.operation.set_allocated_current")
	completion := invokeRoute(t, session, overload, quantityValue("16", 0, "unit.ampere"))
	if completion.Outcome != operation.OutcomeAcknowledgedUnverified || calls != 1 {
		t.Fatalf("overload outcome %s calls %d", completion.Outcome, calls)
	}
	missing := heatingRemote()
	missing.Commands = []string{"overload_protection", "self_consumption", "coordinated_charging", "unlisted"}
	missingResult, _ := publish(t, heatingScope("instance.heating"), missing)
	gaps := gapSet(missingResult)
	for _, token := range []string{"overload_protection", "self_consumption", "coordinated_charging"} {
		if !gaps["device.heat:command_subject_missing:"+token] {
			t.Fatalf("gaps %v", missingResult.Gaps)
		}
	}
	if !gaps["device.heat:command_unknown:unlisted"] {
		t.Fatalf("gaps %v", missingResult.Gaps)
	}
	for _, route := range missingResult.Routes {
		id := string(route.Operation.ID)
		if strings.HasPrefix(id, "evse.") || strings.HasPrefix(id, "energy.") {
			t.Fatalf("missing subject still routed %s", id)
		}
	}
	idle := chargerRemote(true, "normalOperation", "normalOperation")
	idle.Generation = 0
	idle.Commands = []string{"overload_protection"}
	idleResult, _ := publish(t, electricalScope("instance.charger"), idle)
	if routeCount(idleResult, "evse.operation.set_allocated_current") != 0 || gapSet(idleResult)["device.charger:command_subject_missing:overload_protection"] {
		t.Fatalf("generation zero routes %d gaps %v", len(idleResult.Routes), idleResult.Gaps)
	}
}

func TestReplyCarrierLeavesLastNotifiedEmpty(t *testing.T) {
	remote := heatingRemote()
	setCarrier(&remote, "reply")
	result, _ := publish(t, heatingScope("instance.heating"), remote)
	if len(result.LastNotified) != 0 || len(result.Facts) == 0 {
		t.Fatalf("reply notified %d facts %d", len(result.LastNotified), len(result.Facts))
	}
	notified := heatingRemote()
	setCarrier(&notified, "notify")
	again, _ := publish(t, heatingScope("instance.heating"), notified)
	if len(again.LastNotified) == 0 || len(again.LastNotified) != len(again.Facts) {
		t.Fatalf("notify last %d facts %d", len(again.LastNotified), len(again.Facts))
	}
}

func setCarrier(remote *Remote, carrier string) {
	for i := range remote.Readings {
		remote.Readings[i].Carrier = carrier
	}
	for i := range remote.Entities {
		for j := range remote.Entities[i].Readings {
			remote.Entities[i].Readings[j].Carrier = carrier
		}
	}
}

func TestRouteIdentityChangesOneField(t *testing.T) {
	baseScope := heatingScope("instance.heating")
	baseRemote := writableRoom(6, "feature.room.setpoint", 4)
	baseResult, baseSession := publish(t, baseScope, baseRemote)
	base := onlyRoute(t, baseResult, "thermal.room.operation_set_temperature")
	if baseSession.routes[0].Owner.Producer != baseSession.Root() || base.Owner.Producer == baseSession.Root() {
		t.Fatal("producer rewrite did not stay on the registered copy")
	}
	type identityCase struct {
		name, generation, bridge, path, catches string
		scope                                   Scope
		remote                                  Remote
		binding, subject                        bool
	}
	otherScope := baseScope
	otherScope.Installation = "installation.other"
	otherInstance := baseScope
	otherInstance.Instance = "instance.other"
	otherBridge := baseScope
	otherBridge.Bridge = "bridge.other"
	otherDevice := baseRemote
	otherDevice.Device = "device.other"
	otherAddress := writableRoom(9, "feature.room.setpoint", 4)
	otherFeature := writableRoom(6, "feature.room.other", 4)
	otherGeneration := writableRoom(6, "feature.room.setpoint", 5)
	cases := []identityCase{
		{name: "installation", scope: otherScope, remote: writableRoom(6, "feature.room.setpoint", 4), binding: true, subject: true, generation: "4", bridge: "bridge.heating", catches: "scope.nodeID and featureBinding hash installation"},
		{name: "instance", scope: otherInstance, remote: writableRoom(6, "feature.room.setpoint", 4), binding: true, subject: true, generation: "4", bridge: "bridge.heating", catches: "scope.nodeID and featureBinding hash instance"},
		{name: "bridge", scope: otherBridge, remote: writableRoom(6, "feature.room.setpoint", 4), generation: "4", bridge: "bridge.other", catches: "scope.ref copies bridge onto the subject and featureBinding omits it"},
		{name: "device", scope: baseScope, remote: otherDevice, binding: true, subject: true, generation: "4", bridge: "bridge.heating", path: "/device/device.other/", catches: "entity slot and featureBinding include the device"},
		{name: "address", scope: baseScope, remote: otherAddress, binding: true, subject: true, generation: "4", bridge: "bridge.heating", path: "/entity/9/", catches: "entity address is the slot segment hashed by featureBinding and written into sourcePath"},
		{name: "feature", scope: baseScope, remote: otherFeature, binding: true, generation: "4", bridge: "bridge.heating", catches: "featureBinding hashes feature and the subject slot does not"},
		{name: "generation", scope: baseScope, remote: otherGeneration, generation: "5", bridge: "bridge.heating", catches: "settingRoute writes the decimal generation and featureBinding omits it"},
	}
	for _, item := range cases {
		gotResult, _ := publish(t, item.scope, item.remote)
		got := onlyRoute(t, gotResult, "thermal.room.operation_set_temperature")
		if (got.Route.BindingID != base.Route.BindingID) != item.binding {
			t.Fatalf("%s binding changed %v, catches %s", item.name, got.Route.BindingID != base.Route.BindingID, item.catches)
		}
		if (got.For.Subject.ID != base.For.Subject.ID) != item.subject {
			t.Fatalf("%s subject changed %v, catches %s", item.name, got.For.Subject.ID != base.For.Subject.ID, item.catches)
		}
		if string(got.For.Subject.Bridge) != item.bridge || string(got.Route.DriverGeneration) != item.generation {
			t.Fatalf("%s bridge %s generation %s, catches %s", item.name, got.For.Subject.Bridge, got.Route.DriverGeneration, item.catches)
		}
		if item.path == "" && got.Owner.PathKey != base.Owner.PathKey {
			t.Fatalf("%s path changed to %s, catches %s", item.name, got.Owner.PathKey, item.catches)
		}
		if item.path != "" && !strings.Contains(got.Owner.PathKey, item.path) {
			t.Fatalf("%s path %s, catches %s", item.name, got.Owner.PathKey, item.catches)
		}
	}
}

func TestCapabilityConstraintUsesTheAdmittedObservation(t *testing.T) {
	_, session := publish(t, heatingScope("instance.heating"), heatingRemote())
	if len(session.batches) != 1 {
		t.Fatalf("batches %d", len(session.batches))
	}
	setpoints := map[string]int{}
	modes := 0
	for _, capability := range session.batches[0].CapabilityUpserts {
		switch capability.Definition.ID {
		case "thermal.room.capability_set_temperature":
			if len(capability.Constraints) != 1 || capability.Constraints[0].ID != "thermal.room.setpoint_temperature_setpoint" {
				t.Fatalf("setpoint constraint %+v, catches operationConstraints field id", capability.Constraints)
			}
			quantity := capability.Constraints[0].Value.Quantity
			if quantity == nil || quantity.Unit != "unit.kelvin" || quantity.Number.Exponent10 != -2 {
				t.Fatalf("setpoint quantity %+v, catches copyConstraint unit and exponent", quantity)
			}
			setpoints[quantity.Number.Coefficient]++
		case "thermal.room.capability_set_zone_mode":
			modes++
			if len(capability.Constraints) != 1 || capability.Constraints[0].ID != "thermal.room.mode_zone" || capability.Constraints[0].Value.Symbol == nil || !capability.Constraints[0].Value.Symbol.Known || capability.Constraints[0].Value.Symbol.Token != "on" || capability.Constraints[0].Value.Symbol.Namespace != "thermal.room.mode_zone" {
				t.Fatalf("mode constraint %+v, catches admittedSample skipping an unknown symbol", capability.Constraints)
			}
		}
	}
	if setpoints["29315"] != 1 || setpoints["29215"] != 1 || len(setpoints) != 2 || modes != 1 {
		t.Fatalf("setpoints %+v modes %d, catches a constraint copied from the other room or an unknown mode published as known", setpoints, modes)
	}
}
