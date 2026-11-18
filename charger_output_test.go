package eebusruntime

import "testing"

func TestChargerOutputEVActor(t *testing.T) {
	// EV fields are device actor, actor and use case.
	// device actor: EV announces nothing. The gate returns the EV loss before any scenario.
	// actor: EV announces nothing. The gate returns the EV loss before any scenario.
	// use case: each use case whose actor is EV announces nothing. The gate returns the EV loss.
	// The baseline with the same elements announces both EVSE use cases, so each field alone flips it.
	baseline := chargerOutputLens("bay")
	outcome := monitorChargerLens(baseline)
	if !monitoringListed(outcome.Announced, "EVSE Commissioning and Configuration") || !monitoringListed(outcome.Announced, "Monitoring of Power Consumption") {
		t.Fatalf("baseline announced %v loss %v", outcome.Announced, outcome.Loss)
	}

	deviceActor := chargerOutputLens("bay")
	deviceActor.DeviceActor = "EV"
	actor := chargerOutputLens("bay")
	actor.Actor = "EV"
	cases := map[string]LensResult{"device actor": deviceActor, "actor": actor}
	for _, useCase := range []string{
		"Overload Protection by EV Charging Current Curtailment",
		"Optimization of Self-Consumption During EV Charging",
		"EV Charging Electricity Measurement",
		"Coordinated EV Charging",
		"EV Commissioning and Configuration",
		"EV State Of Charge",
	} {
		result := chargerOutputLens("bay")
		result.UseCase = useCase
		cases[useCase] = result
	}
	for name, result := range cases {
		t.Run(name, func(t *testing.T) {
			outcome := monitorChargerLens(result)
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
	cases := map[string][]LensOperation{
		"admitted":       {{Name: "evse.operation.set_allocated_current", Unit: "A", OwnCapability: true}},
		"name":           {{Name: "active power", Unit: "W", OwnCapability: true}},
		"unit":           {{Name: "evse.operation.set_allocated_current", Unit: "W", OwnCapability: true}},
		"own capability": {{Name: "evse.operation.set_allocated_current", Unit: "A"}},
		"converted":      {{Name: "evse.operation.set_allocated_current", Unit: "A", OwnCapability: true, Converted: true}},
		"none":           nil,
	}
	for name, operations := range cases {
		t.Run(name, func(t *testing.T) {
			result := chargerOutputLens("bay")
			result.UseCase = "Limitation of Power Consumption"
			result.Operations = operations
			outcome := chargerConsumptionLimitWrite(result)
			if len(outcome.Operations) != 0 || monitoringListed(outcome.Operations, "evse.operation.set_allocated_current") {
				t.Fatalf("called %v", outcome.Operations)
			}
			if len(outcome.Announced) != 0 {
				t.Fatalf("announced %v", outcome.Announced)
			}
			if !monitoringListed(outcome.Loss, "missing operation: active power consumption limit") || !monitoringListed(outcome.Loss, "not called: evse.operation.set_allocated_current") {
				t.Fatalf("loss %v", outcome.Loss)
			}
			monitored := monitorChargerLens(result)
			if monitoringListed(monitored.Announced, "Limitation of Power Consumption") || !monitoringListed(monitored.Loss, "consumption_limit_not_applied") {
				t.Fatalf("monitoring announced %v loss %v", monitored.Announced, monitored.Loss)
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
	noTotal := chargerOutputLens("bay")
	noTotal.Elements = monitoringDrop(noTotal.Elements, "total active power")
	outcome := monitorChargerLens(noTotal)
	if monitoringListed(outcome.Announced, "Monitoring of Power Consumption") || !monitoringListed(outcome.Loss, "total active power") {
		t.Fatalf("total announced %v loss %v", outcome.Announced, outcome.Loss)
	}
	if !monitoringListed(outcome.Announced, "EVSE Commissioning and Configuration") {
		t.Fatalf("total withdrew commissioning %v", outcome.Announced)
	}
	if !monitoringListed(noTotal.Elements, "phase active power") {
		t.Fatal("phase active power is not part of the case")
	}

	noState := chargerOutputLens("bay")
	noState.Elements = monitoringDrop(noState.Elements, "operating state")
	outcome = monitorChargerLens(noState)
	if monitoringListed(outcome.Announced, "EVSE Commissioning and Configuration") || !monitoringListed(outcome.Loss, "operating state") {
		t.Fatalf("state announced %v loss %v", outcome.Announced, outcome.Loss)
	}
	if !monitoringListed(outcome.Announced, "Monitoring of Power Consumption") {
		t.Fatalf("state withdrew monitoring %v", outcome.Announced)
	}

	for _, excluded := range []string{"total active power", "operating state"} {
		t.Run(excluded, func(t *testing.T) {
			result := chargerOutputLens("bay")
			result.MissingElements = []string{excluded}
			outcome := monitorChargerLens(result)
			if !monitoringListed(outcome.Loss, excluded) || len(outcome.Announced) != 1 {
				t.Fatalf("announced %v loss %v", outcome.Announced, outcome.Loss)
			}
		})
	}

	for _, unrelated := range []string{"Limitation of Power Consumption", "Monitoring of Inverter"} {
		outcome = monitorChargerLens(chargerOutputLens("bay"))
		if monitoringListed(outcome.Announced, unrelated) {
			t.Fatalf("announced %s", unrelated)
		}
	}
	if !monitoringListed(outcome.Loss, "device classification") {
		t.Fatalf("manufacturer data gap %v", outcome.Loss)
	}
}

func TestChargerOutputServedOnEVSE(t *testing.T) {
	// Apply adds the EVSE entity. Serving attaches measurement and electrical connection for
	// Monitoring of Power Consumption and device diagnosis for EVSE Commissioning and
	// Configuration on that entity. The monitored unit is the EVSE entity, so the CEM entity
	// gains no feature.
	binding := newOutputBinding(t)
	result := chargerOutputLens("bay")
	if loss := binding.Apply(result); len(loss) != 0 {
		t.Fatalf("apply loss %v", loss)
	}
	outcome := chargerOutputServe(binding, result)
	if !monitoringListed(outcome.Announced, "EVSE Commissioning and Configuration") || !monitoringListed(outcome.Announced, "Monitoring of Power Consumption") {
		t.Fatalf("announced %v loss %v", outcome.Announced, outcome.Loss)
	}
	features := binding.ServerFeatures("bay")
	for _, want := range []string{"measurement", "electrical connection", "device diagnosis"} {
		if !outputBindingHasType(features, want) {
			t.Fatalf("missing %s in %v", want, features)
		}
	}
	if outputBindingHasType(features, "load control") {
		t.Fatalf("load control without a current operation %v", features)
	}
	node := outputBindingNode(t, binding)
	if got := node.features["cem"]; len(got) != 0 {
		t.Fatalf("cem features %v", got)
	}
	if !outputBindingHasType(binding.EntityTypes(), "EVSE") {
		t.Fatalf("entities %v", binding.EntityTypes())
	}
}

func TestChargerOutputUnboundEntity(t *testing.T) {
	// A result the binding has not added as an EVSE entity gains no feature and announces nothing.
	// resource: an unbound resource is the existing-node loss. Nothing lands on the CEM entity.
	// kind: an inverter entity under the same resource is not the EVSE monitored unit.
	binding := newOutputBinding(t)
	outcome := chargerOutputServe(binding, chargerOutputLens("bay"))
	if len(outcome.Announced) != 0 || !monitoringListed(outcome.Loss, "existing node") {
		t.Fatalf("unbound announced %v loss %v", outcome.Announced, outcome.Loss)
	}
	if got := outputBindingNode(t, binding).features["cem"]; len(got) != 0 {
		t.Fatalf("cem features %v", got)
	}

	if loss := binding.Apply(outputBindingInverter("north")); len(loss) != 0 {
		t.Fatalf("inverter loss %v", loss)
	}
	outcome = chargerOutputServe(binding, chargerOutputLens("north"))
	if len(outcome.Announced) != 0 || !monitoringListed(outcome.Loss, "existing node") {
		t.Fatalf("inverter announced %v loss %v", outcome.Announced, outcome.Loss)
	}
	if outputBindingHasType(binding.ServerFeatures("north"), "measurement") {
		t.Fatalf("inverter features %v", binding.ServerFeatures("north"))
	}
}

func TestChargerOutputFeatureReject(t *testing.T) {
	// A server feature the node rejects withdraws only the use case that needs it.
	// device diagnosis: commissioning is withdrawn and named. Power monitoring stays announced.
	// measurement: power monitoring is withdrawn and named. Commissioning stays announced.
	// electrical connection: power monitoring is withdrawn and named. Commissioning stays announced.
	cases := map[string]string{
		"device diagnosis":      "EVSE Commissioning and Configuration",
		"measurement":           "Monitoring of Power Consumption",
		"electrical connection": "Monitoring of Power Consumption",
	}
	for feature, withdrawn := range cases {
		t.Run(feature, func(t *testing.T) {
			node := &chargerOutputRejectNode{outputBindingFakeNode: newOutputBindingFakeNode(), reject: feature}
			useOutputBindingNode(t, func() (shipNode, error) { return node, nil })
			binding, err := NewOutputBinding(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			result := chargerOutputLens("bay")
			if loss := binding.Apply(result); len(loss) != 0 {
				t.Fatalf("apply loss %v", loss)
			}
			outcome := chargerOutputServe(binding, result)
			if monitoringListed(outcome.Announced, withdrawn) || !monitoringListed(outcome.Loss, feature) {
				t.Fatalf("announced %v loss %v", outcome.Announced, outcome.Loss)
			}
			if len(outcome.Announced) != 1 {
				t.Fatalf("announced %v", outcome.Announced)
			}
		})
	}
}

func TestChargerOutputUnadmitted(t *testing.T) {
	// Admission fields are lens admitted, runtime enabled, basis, resource, family and overlapping.
	// Each mutation announces nothing and adds no loss, so an unadmitted result is never served.
	mutations := map[string]func(*LensResult){
		"lens admitted":   func(r *LensResult) { r.LensAdmitted = false },
		"runtime enabled": func(r *LensResult) { r.RuntimeEnabled = false },
		"basis":           func(r *LensResult) { r.Basis = "derived" },
		"empty resource":  func(r *LensResult) { r.ResourceID = "" },
		"spaced resource": func(r *LensResult) { r.ResourceID = "bay one" },
		"family":          func(r *LensResult) { r.Family = "inverter" },
		"overlapping":     func(r *LensResult) { r.Overlapping = true },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			result := chargerOutputLens("bay")
			mutate(&result)
			outcome := monitorChargerLens(result)
			if len(outcome.Announced) != 0 || len(outcome.Loss) != 0 || len(outcome.Operations) != 0 {
				t.Fatalf("outcome %+v", outcome)
			}
		})
	}
}

type chargerOutputRejectNode struct {
	*outputBindingFakeNode
	reject string
}

func (n *chargerOutputRejectNode) AddServerFeature(address, name string) error {
	if name == n.reject {
		return errExistingNode
	}
	return n.outputBindingFakeNode.AddServerFeature(address, name)
}

func chargerOutputServe(binding *OutputBinding, result LensResult) monitoringOutcome {
	outputBindingGate.Lock()
	defer outputBindingGate.Unlock()
	binding.liveNodeLocked()
	return binding.serveChargerOutput(result)
}

func chargerOutputLens(resourceID string) LensResult {
	return LensResult{
		Family: "charger", ResourceID: resourceID, LensAdmitted: true, RuntimeEnabled: true,
		Elements: []string{
			"total active power", "phase active power", "phase current", "phase voltage", "frequency",
			"session energy", "lifetime energy", "operating state", "charging status", "connection status",
			"fault status", "interlock status", "readiness status",
		},
	}
}
