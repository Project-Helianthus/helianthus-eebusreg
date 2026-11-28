package charger

import (
	"errors"
	"sort"
	"testing"

	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
)

func TestChargerOutputEVActor(t *testing.T) {
	// EV fields are device actor, actor and use case.
	// device actor: EV announces nothing. The gate returns the EV loss before any scenario.
	// actor: EV announces nothing. The gate returns the EV loss before any scenario.
	// use case: each use case whose actor is EV announces nothing. The gate returns the EV loss.
	// The baseline with the same elements announces both EVSE use cases, so each field alone flips it.
	baseline := chargerLens("bay")
	outcome := Monitor(baseline)
	if !listed(outcome.Announced, "EVSE Commissioning and Configuration") || !listed(outcome.Announced, "Monitoring of Power Consumption") {
		t.Fatalf("baseline announced %v loss %v", outcome.Announced, outcome.Loss)
	}

	deviceActor := chargerLens("bay")
	deviceActor.DeviceActor = "EV"
	actor := chargerLens("bay")
	actor.Actor = "EV"
	cases := map[string]Result{"device actor": deviceActor, "actor": actor}
	for _, useCase := range []string{
		"Overload Protection by EV Charging Current Curtailment",
		"Optimization of Self-Consumption During EV Charging",
		"EV Charging Electricity Measurement",
		"Coordinated EV Charging",
		"EV Commissioning and Configuration",
		"EV State Of Charge",
	} {
		result := chargerLens("bay")
		result.UseCase = useCase
		cases[useCase] = result
	}
	for name, result := range cases {
		t.Run(name, func(t *testing.T) {
			outcome := Monitor(result)
			if len(outcome.Announced) != 0 || len(outcome.Operations) != 0 {
				t.Fatalf("announced %v operations %v", outcome.Announced, outcome.Operations)
			}
			if len(outcome.Loss) != 1 || outcome.Loss[0] != "EV" {
				t.Fatalf("loss %v", outcome.Loss)
			}
		})
	}
}

func TestChargerOutputConsumptionLimitWrite(t *testing.T) {
	// Operation fields are name, unit, own capability and converted.
	// admitted: the allocated current operation is recognized. The refusal strings are absent.
	// name: recognizedCurrent compares the operation name, so active power is not recognized.
	// unit: recognizedCurrent compares the unit, so watts are not recognized.
	// own capability: recognizedCurrent requires it, so visibility alone is not recognized.
	// converted: recognizedCurrent rejects it, so a converted value is not recognized.
	// none: a charger without operations recognizes nothing.
	// Every case leaves the three refusal strings absent. Limitation of Power Consumption stays unannounced.
	cases := map[string]struct {
		operations []Operation
		recognized bool
	}{
		"admitted":       {[]Operation{{Name: "evse.operation.set_allocated_current", Unit: "A", OwnCapability: true}}, true},
		"name":           {[]Operation{{Name: "active power", Unit: "W", OwnCapability: true}}, false},
		"unit":           {[]Operation{{Name: "evse.operation.set_allocated_current", Unit: "W", OwnCapability: true}}, false},
		"own capability": {[]Operation{{Name: "evse.operation.set_allocated_current", Unit: "A"}}, false},
		"converted":      {[]Operation{{Name: "evse.operation.set_allocated_current", Unit: "A", OwnCapability: true, Converted: true}}, false},
		"none":           {nil, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			result := chargerLens("bay")
			result.UseCase = "Limitation of Power Consumption"
			result.Operations = tc.operations
			outcome := Monitor(result)
			if listed(outcome.Announced, "Limitation of Power Consumption") {
				t.Fatalf("announced %v", outcome.Announced)
			}
			for _, refused := range []string{
				"consumption_limit_not_applied",
				"missing operation: active power consumption limit",
				"not called: evse.operation.set_allocated_current",
			} {
				if listed(outcome.Loss, refused) {
					t.Fatalf("loss still names %s: %v", refused, outcome.Loss)
				}
			}
			if listed(outcome.Operations, "evse.operation.set_allocated_current") != tc.recognized {
				t.Fatalf("recognized=%v operations %v", tc.recognized, outcome.Operations)
			}
		})
	}
}

func TestChargerOutputMandatoryScenarios(t *testing.T) {
	// Scenario fields are the total active power, the operating state and the missing element.
	// total active power: absent leaves Monitoring of Power Consumption unannounced and named.
	// phase active power: alone it does not fill the total. The gate does not sum phases.
	// operating state: absent leaves EVSE Commissioning and Configuration unannounced and named.
	// charger statuses: charging, connection, fault and readiness do not fill the operating state.
	// missing element: an element the lens also names as missing is not admitted.
	// Each case keeps the other use case announced, so the gate withdraws only the unfilled one.
	noTotal := chargerLens("bay")
	noTotal.Elements = dropElement(noTotal.Elements, "total active power")
	outcome := Monitor(noTotal)
	if listed(outcome.Announced, "Monitoring of Power Consumption") || !listed(outcome.Loss, "total active power") {
		t.Fatalf("total announced %v loss %v", outcome.Announced, outcome.Loss)
	}
	if !listed(outcome.Announced, "EVSE Commissioning and Configuration") {
		t.Fatalf("total withdrew commissioning %v", outcome.Announced)
	}
	if !listed(noTotal.Elements, "phase active power") {
		t.Fatal("phase active power is not part of the case")
	}

	noState := chargerLens("bay")
	noState.Elements = dropElement(noState.Elements, "operating state")
	outcome = Monitor(noState)
	if listed(outcome.Announced, "EVSE Commissioning and Configuration") || !listed(outcome.Loss, "operating state") {
		t.Fatalf("state announced %v loss %v", outcome.Announced, outcome.Loss)
	}
	if !listed(outcome.Announced, "Monitoring of Power Consumption") {
		t.Fatalf("state withdrew monitoring %v", outcome.Announced)
	}

	for _, excluded := range []string{"total active power", "operating state"} {
		t.Run(excluded, func(t *testing.T) {
			result := chargerLens("bay")
			result.MissingElements = []string{excluded}
			outcome := Monitor(result)
			if !listed(outcome.Loss, excluded) || len(outcome.Announced) != 1 {
				t.Fatalf("announced %v loss %v", outcome.Announced, outcome.Loss)
			}
		})
	}

	for _, unrelated := range []string{"Limitation of Power Consumption", "Monitoring of Inverter"} {
		outcome = Monitor(chargerLens("bay"))
		if listed(outcome.Announced, unrelated) {
			t.Fatalf("announced %s", unrelated)
		}
	}
	if !listed(outcome.Loss, "device classification") {
		t.Fatalf("manufacturer data gap %v", outcome.Loss)
	}
}

func TestChargerOutputUnboundEntity(t *testing.T) {
	// A result the binding has not added as an EVSE entity gains no feature and announces nothing.
	// resource: an unbound resource is the existing-node loss. Nothing is attached.
	// kind: an inverter entity under the same resource is not the EVSE monitored unit.
	board := &featureBoard{}
	outcome := Serve(chargerLens("bay"), Placement{Binding: true, Node: true}, board.attach)
	if len(outcome.Announced) != 0 || !listed(outcome.Loss, "existing node") {
		t.Fatalf("unbound announced %v loss %v", outcome.Announced, outcome.Loss)
	}
	if board.calls != 0 || len(board.names) != 0 {
		t.Fatalf("unbound attached %v calls %d", board.names, board.calls)
	}

	board = &featureBoard{}
	inverter := Placement{Binding: true, Found: true, Kind: "Inverter", Address: "a", Node: true}
	outcome = Serve(chargerLens("north"), inverter, board.attach)
	if len(outcome.Announced) != 0 || !listed(outcome.Loss, "existing node") {
		t.Fatalf("inverter announced %v loss %v", outcome.Announced, outcome.Loss)
	}
	if listed(board.names, "measurement") || board.calls != 0 {
		t.Fatalf("inverter features %v calls %d", board.names, board.calls)
	}
}

func TestChargerOutputFeatureReject(t *testing.T) {
	// A server feature the node rejects withdraws only the use case that needs it.
	// device diagnosis: commissioning is withdrawn and named. Power monitoring keeps both features.
	// measurement: power monitoring is withdrawn and named. Commissioning keeps device diagnosis.
	// electrical connection: power monitoring is withdrawn and named, and the measurement it
	// would add is not added. Commissioning keeps device diagnosis.
	cases := map[string][]string{
		"device diagnosis":      {"device diagnosis"},
		"measurement":           {"measurement", "electrical connection"},
		"electrical connection": {"measurement", "electrical connection"},
	}
	for feature, withdrawn := range cases {
		t.Run(feature, func(t *testing.T) {
			board := &featureBoard{reject: feature}
			outcome := Serve(chargerLens("bay"), evsePlacement(), board.attach)
			if !listed(outcome.Loss, feature) {
				t.Fatalf("loss %v", outcome.Loss)
			}
			for _, name := range []string{"device diagnosis", "measurement", "electrical connection"} {
				if listed(board.names, name) == listed(withdrawn, name) {
					t.Fatalf("%s withdrawn=%v features %v", name, listed(withdrawn, name), board.names)
				}
			}
		})
	}
}

func TestChargerOutputPowerMonitoringOrder(t *testing.T) {
	// Serve gives each EVSE the power-monitoring group in package order.
	// Sorting that group in place leaves the next binding with measurement
	// followed by electrical connection.
	var power [][]string
	attach := func(features []string) (string, error) {
		if !listed(features, featureMeasurement) || !listed(features, featureElectrical) {
			return "", nil
		}
		power = append(power, append([]string(nil), features...))
		sort.Strings(features)
		return "", nil
	}
	Serve(chargerLens("bay"), evsePlacement(), attach)
	next := evsePlacement()
	next.Address = "b"
	Serve(chargerLens("north"), next, attach)
	if len(power) != 2 {
		t.Fatalf("power monitoring calls %d", len(power))
	}
	for _, group := range power {
		if len(group) != 2 || group[0] != featureMeasurement || group[1] != featureElectrical {
			t.Fatalf("power monitoring order %v", group)
		}
	}
}

func TestChargerOutputUnadmitted(t *testing.T) {
	// Admission fields are lens admitted, runtime enabled, basis, resource, family and overlapping.
	// Each mutation announces nothing and adds no loss, so an unadmitted result is never served.
	mutations := map[string]func(*Result){
		"lens admitted":   func(r *Result) { r.LensAdmitted = false },
		"runtime enabled": func(r *Result) { r.RuntimeEnabled = false },
		"basis":           func(r *Result) { r.Basis = "derived" },
		"empty resource":  func(r *Result) { r.ResourceID = "" },
		"spaced resource": func(r *Result) { r.ResourceID = "bay one" },
		"family":          func(r *Result) { r.Family = "inverter" },
		"overlapping":     func(r *Result) { r.Overlapping = true },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			result := chargerLens("bay")
			mutate(&result)
			outcome := Monitor(result)
			if len(outcome.Announced) != 0 || len(outcome.Loss) != 0 || len(outcome.Operations) != 0 {
				t.Fatalf("outcome %+v", outcome)
			}
		})
	}
}

func TestChargerOutputRejectedConnectionLeavesNoMeasurement(t *testing.T) {
	// The node rejects electrical connection. Power monitoring asks for measurement
	// and electrical connection at once, so it is withdrawn without adding measurement,
	// and the EVSE entity keeps only the commissioning feature.
	board := &featureBoard{reject: "electrical connection"}
	outcome := Serve(chargerLens("bay"), evsePlacement(), board.attach)
	if listed(board.names, "measurement") || listed(board.names, "electrical connection") {
		t.Fatalf("withdrawn power monitoring left %v", board.names)
	}
	if !listed(board.names, "device diagnosis") {
		t.Fatalf("commissioning features %v", board.names)
	}
	if !listed(outcome.Loss, "electrical connection") || listed(outcome.Loss, "measurement") {
		t.Fatalf("loss %v", outcome.Loss)
	}
}

func TestChargerOutputRejectedOperationFeature(t *testing.T) {
	// A charger that carries the admitted allocated current operation. The node rejects one
	// feature that operation binds. The entity stays applied and commissioning stays served.
	// load control: only the operation needs it. Both use cases stay served and the loss names it.
	// electrical connection: power monitoring and the operation need it. Both are withdrawn,
	// and neither measurement nor load control lands on the entity.
	cases := map[string]struct{ on, off []string }{
		"load control":          {[]string{"device diagnosis", "measurement", "electrical connection"}, []string{"load control"}},
		"electrical connection": {[]string{"device diagnosis"}, []string{"measurement", "electrical connection", "load control"}},
	}
	for feature, want := range cases {
		t.Run(feature, func(t *testing.T) {
			board := &featureBoard{reject: feature}
			result := chargerLens("bay")
			result.Operations = []Operation{{Name: "evse.operation.set_allocated_current", Unit: "A", OwnCapability: true}}
			outcome := Serve(result, evsePlacement(), board.attach)
			for _, name := range want.on {
				if !listed(board.names, name) {
					t.Fatalf("%s missing in %v", name, board.names)
				}
			}
			for _, name := range want.off {
				if listed(board.names, name) {
					t.Fatalf("%s left in %v", name, board.names)
				}
			}
			if !listed(outcome.Loss, feature) || !listed(outcome.Loss, "device classification") {
				t.Fatalf("loss %v", outcome.Loss)
			}
		})
	}
}

func TestChargerOutputEnergyScale(t *testing.T) {
	// Coefficient, exponent and produced select the watt-hour scale.
	// coefficient: 3600000 J is 1000 Wh. A doubled coefficient is 2000 Wh.
	// exponent: 36 * 10^5 J is the same 1000 Wh. Ignoring the exponent leaves 36 J.
	// produced: the same 3600000 J is -1000 Wh. A negative export counter is not served.
	consumed, scale, ok := jouleToWattHour("3600000", 0, false)
	if !ok || consumed != 1000 || scale != 0 {
		t.Fatalf("consumed %d scale %d ok %v", consumed, scale, ok)
	}
	doubled, scale, ok := jouleToWattHour("7200000", 0, false)
	if !ok || doubled != 2000 || scale != 0 {
		t.Fatalf("doubled coefficient %d scale %d", doubled, scale)
	}
	shifted, scale, ok := jouleToWattHour("36", 5, false)
	if !ok || shifted != 1000 || scale != 0 {
		t.Fatalf("exponent %d scale %d", shifted, scale)
	}
	produced, scale, ok := jouleToWattHour("3600000", 0, true)
	if !ok || produced != -1000 || scale != 0 {
		t.Fatalf("produced %d scale %d", produced, scale)
	}
	if _, _, ok := jouleToWattHour("-3600000", 0, true); ok {
		t.Fatal("negative export counter was served")
	}
	if _, _, ok := jouleToWattHour("1000", 0, false); ok {
		t.Fatal("inexact joule became a watt hour")
	}
}

func TestChargerOutputGenericOperatingState(t *testing.T) {
	// Table rows are fault, readiness and charging, in that order.
	// fault: present wins over not ready and idle, so the state is failure.
	// readiness: not ready wins over charging, so the state is temporarilyNotReady.
	// charging: charging is normalOperation when fault is clear and readiness is ready.
	// idle: idle is standby when fault is clear and readiness is ready.
	// A clear, ready, complete combination matches none of the four rows.
	cases := []struct {
		name                 string
		fault, ready, charge string
		want                 spinemodel.DeviceDiagnosisOperatingStateType
		known                bool
	}{
		{"fault", symbolPresent, symbolNotReady, symbolIdle, spinemodel.DeviceDiagnosisOperatingStateTypeFailure, true},
		{"readiness", "clear", symbolNotReady, symbolCharging, spinemodel.DeviceDiagnosisOperatingStateTypeTemporarilyNotReady, true},
		{"charging", "clear", "ready", symbolCharging, spinemodel.DeviceDiagnosisOperatingStateTypeNormalOperation, true},
		{"idle", "clear", "ready", symbolIdle, spinemodel.DeviceDiagnosisOperatingStateTypeStandby, true},
		{"unknown", "clear", "ready", "complete", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, known := genericOperatingState(tc.fault, tc.ready, tc.charge)
			if known != tc.known || got != tc.want {
				t.Fatalf("state %s known %v", got, known)
			}
		})
	}
}

func evsePlacement() Placement {
	return Placement{Binding: true, Found: true, Kind: "EVSE", Address: "a", Node: true}
}

type featureBoard struct {
	reject string
	names  []string
	calls  int
}

func (board *featureBoard) attach(features []string) (string, error) {
	board.calls++
	if board.reject != "" && listed(features, board.reject) {
		return board.reject, errors.New("existing node is unavailable")
	}
	for _, feature := range features {
		if !listed(board.names, feature) {
			board.names = append(board.names, feature)
		}
	}
	sort.Strings(board.names)
	return "", nil
}

func chargerLens(resourceID string) Result {
	return Result{
		Family: "charger", ResourceID: resourceID, LensAdmitted: true, RuntimeEnabled: true,
		Elements: []string{
			"total active power", "phase active power", "phase current", "phase voltage", "frequency",
			"session energy", "lifetime energy", "operating state", "charging status", "connection status",
			"fault status", "readiness status",
		},
	}
}

func dropElement(values []string, drop string) []string {
	var kept []string
	for _, value := range values {
		if value != drop {
			kept = append(kept, value)
		}
	}
	return kept
}
