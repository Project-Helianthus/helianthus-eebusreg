package charger

import (
	"errors"
	"sort"
	"testing"
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
	// admitted: the allocated current operation is admitted and still not called for a watt limit.
	// name: an active-power operation on a charger is not called. No watt operation is admitted.
	// unit: a watt unit on the current operation is not called. Watts are not converted to amperes.
	// own capability: visibility without it is not called.
	// converted: a converted value is not called.
	// none: a charger without operations still names both gaps.
	cases := map[string][]Operation{
		"admitted":       {{Name: "evse.operation.set_allocated_current", Unit: "A", OwnCapability: true}},
		"name":           {{Name: "active power", Unit: "W", OwnCapability: true}},
		"unit":           {{Name: "evse.operation.set_allocated_current", Unit: "W", OwnCapability: true}},
		"own capability": {{Name: "evse.operation.set_allocated_current", Unit: "A"}},
		"converted":      {{Name: "evse.operation.set_allocated_current", Unit: "A", OwnCapability: true, Converted: true}},
		"none":           nil,
	}
	for name, operations := range cases {
		t.Run(name, func(t *testing.T) {
			result := chargerLens("bay")
			result.UseCase = "Limitation of Power Consumption"
			result.Operations = operations
			outcome := consumptionLimitWrite(result)
			if len(outcome.Operations) != 0 || listed(outcome.Operations, "evse.operation.set_allocated_current") {
				t.Fatalf("called %v", outcome.Operations)
			}
			if len(outcome.Announced) != 0 {
				t.Fatalf("announced %v", outcome.Announced)
			}
			if !listed(outcome.Loss, "missing operation: active power consumption limit") || !listed(outcome.Loss, "not called: evse.operation.set_allocated_current") {
				t.Fatalf("loss %v", outcome.Loss)
			}
			monitored := Monitor(result)
			if listed(monitored.Announced, "Limitation of Power Consumption") || !listed(monitored.Loss, "consumption_limit_not_applied") {
				t.Fatalf("monitoring announced %v loss %v", monitored.Announced, monitored.Loss)
			}
			if !listed(monitored.Loss, "missing operation: active power consumption limit") || !listed(monitored.Loss, "not called: evse.operation.set_allocated_current") {
				t.Fatalf("monitoring loss omits the write gaps %v", monitored.Loss)
			}
			if len(monitored.Operations) != 0 {
				t.Fatalf("monitoring operations %v", monitored.Operations)
			}
		})
	}
}

func TestChargerOutputMandatoryScenarios(t *testing.T) {
	// Scenario fields are the total active power, the operating state and the missing element.
	// total active power: absent leaves Monitoring of Power Consumption unannounced and named.
	// phase active power: alone it does not fill the total. The gate does not sum phases.
	// operating state: absent leaves EVSE Commissioning and Configuration unannounced and named.
	// charger statuses: charging, connection, fault, interlock and readiness do not fill the operating state.
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
			"fault status", "interlock status", "readiness status",
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
