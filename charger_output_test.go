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
			if !monitoringListed(monitored.Loss, "missing operation: active power consumption limit") || !monitoringListed(monitored.Loss, "not called: evse.operation.set_allocated_current") {
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
	// Apply adds the EVSE entity and serves it. It attaches measurement and electrical
	// connection for Monitoring of Power Consumption and device diagnosis for EVSE
	// Commissioning and Configuration on that entity. The monitored unit is the EVSE
	// entity, so the CEM entity gains no feature. The caller of Apply reads the device
	// classification gap from MonitoringLoss.
	binding := newOutputBinding(t)
	result := chargerOutputLens("bay")
	if loss := binding.Apply(result); len(loss) != 0 {
		t.Fatalf("apply loss %v", loss)
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
	gaps := binding.MonitoringLoss("bay")
	if len(gaps) != 1 || gaps[0] != "device classification" {
		t.Fatalf("monitoring loss %v", gaps)
	}
}

func TestChargerOutputApplyMandatoryElement(t *testing.T) {
	// Apply serves each use case only when its mandatory element is admitted.
	// operating state: absent leaves device diagnosis off the EVSE entity and names the element.
	// total active power: absent leaves measurement and electrical connection off and names the element.
	// missing element: an element the lens also names as missing is not admitted either.
	cases := []struct {
		name    string
		mutate  func(*LensResult)
		element string
		off     []string
		on      []string
	}{
		{"operating state", func(r *LensResult) { r.Elements = monitoringDrop(r.Elements, "operating state") },
			"operating state", []string{"device diagnosis"}, []string{"measurement", "electrical connection"}},
		{"total active power", func(r *LensResult) { r.Elements = monitoringDrop(r.Elements, "total active power") },
			"total active power", []string{"measurement", "electrical connection"}, []string{"device diagnosis"}},
		{"missing total", func(r *LensResult) { r.MissingElements = []string{"total active power"} },
			"total active power", []string{"measurement", "electrical connection"}, []string{"device diagnosis"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			binding := newOutputBinding(t)
			result := chargerOutputLens("bay")
			tc.mutate(&result)
			binding.Apply(result)
			features := binding.ServerFeatures("bay")
			for _, off := range tc.off {
				if outputBindingHasType(features, off) {
					t.Fatalf("%s served in %v", off, features)
				}
			}
			for _, on := range tc.on {
				if !outputBindingHasType(features, on) {
					t.Fatalf("%s missing in %v", on, features)
				}
			}
			gaps := binding.MonitoringLoss("bay")
			if !monitoringListed(gaps, tc.element) || !monitoringListed(gaps, "device classification") {
				t.Fatalf("monitoring loss %v", gaps)
			}
		})
	}
}

func TestChargerOutputApplyConsumptionLimit(t *testing.T) {
	// A Limitation of Power Consumption result applied on a charger keeps its returned
	// loss, and MonitoringLoss gives the caller both write entries: the missing active-power
	// operation and the allocated current operation that was not called.
	binding := newOutputBinding(t)
	result := chargerOutputLens("bay")
	result.UseCase = "Limitation of Power Consumption"
	result.Operations = []LensOperation{{Name: "evse.operation.set_allocated_current", Unit: "A", OwnCapability: true}}
	loss := binding.Apply(result)
	if len(loss) != 1 || loss[0] != "consumption_limit_not_applied" {
		t.Fatalf("apply loss %v", loss)
	}
	gaps := binding.MonitoringLoss("bay")
	for _, want := range []string{
		"missing operation: active power consumption limit",
		"not called: evse.operation.set_allocated_current",
		"device classification",
	} {
		if !monitoringListed(gaps, want) {
			t.Fatalf("monitoring loss omits %s: %v", want, gaps)
		}
	}
	if monitoringListed(gaps, "Limitation of Power Consumption") {
		t.Fatalf("monitoring loss %v", gaps)
	}

	plain := chargerOutputLens("other")
	binding.Apply(plain)
	if gaps := binding.MonitoringLoss("other"); monitoringListed(gaps, "not called: evse.operation.set_allocated_current") {
		t.Fatalf("write loss without a write %v", gaps)
	}
}

func TestChargerOutputApplyEVActor(t *testing.T) {
	// A charger result whose actor is the EV adds the EVSE entity but serves no use case on it.
	// MonitoringLoss names the EV.
	binding := newOutputBinding(t)
	result := chargerOutputLens("bay")
	result.Actor = "EV"
	binding.Apply(result)
	if got := binding.ServerFeatures("bay"); len(got) != 0 {
		t.Fatalf("features %v", got)
	}
	if gaps := binding.MonitoringLoss("bay"); len(gaps) != 1 || gaps[0] != "EV" {
		t.Fatalf("monitoring loss %v", gaps)
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
	// A server feature the node rejects during Apply withdraws only the use case that needs it.
	// device diagnosis: commissioning is withdrawn and named. Power monitoring keeps both features.
	// measurement: power monitoring is withdrawn and named. Commissioning keeps device diagnosis.
	// electrical connection: power monitoring is withdrawn and named, and the measurement it
	// already added is taken back. Commissioning keeps device diagnosis.
	cases := map[string][]string{
		"device diagnosis":      {"device diagnosis"},
		"measurement":           {"measurement", "electrical connection"},
		"electrical connection": {"measurement", "electrical connection"},
	}
	for feature, withdrawn := range cases {
		t.Run(feature, func(t *testing.T) {
			node := &chargerOutputRejectNode{outputBindingFakeNode: newOutputBindingFakeNode(), reject: feature}
			useOutputBindingNode(t, func() (shipNode, error) { return node, nil })
			binding, err := NewOutputBinding(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if loss := binding.Apply(chargerOutputLens("bay")); len(loss) != 0 {
				t.Fatalf("apply loss %v", loss)
			}
			gaps := binding.MonitoringLoss("bay")
			if !monitoringListed(gaps, feature) {
				t.Fatalf("monitoring loss %v", gaps)
			}
			features := binding.ServerFeatures("bay")
			for _, name := range []string{"device diagnosis", "measurement", "electrical connection"} {
				if outputBindingHasType(features, name) == monitoringListed(withdrawn, name) {
					t.Fatalf("%s withdrawn=%v features %v", name, monitoringListed(withdrawn, name), features)
				}
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

func (n *chargerOutputRejectNode) AddServerFeatures(address string, names []string) (string, error) {
	if monitoringListed(names, n.reject) {
		return n.reject, errExistingNode
	}
	return n.outputBindingFakeNode.AddServerFeatures(address, names)
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

func TestChargerOutputRejectedConnectionLeavesNoMeasurement(t *testing.T) {
	// The node rejects electrical connection. Power monitoring asks for measurement
	// and electrical connection at once, so it is withdrawn without adding measurement,
	// and the EVSE entity keeps only the commissioning feature.
	node := &chargerOutputRejectNode{outputBindingFakeNode: newOutputBindingFakeNode(), reject: "electrical connection"}
	useOutputBindingNode(t, func() (shipNode, error) { return node, nil })
	binding, err := NewOutputBinding(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if loss := binding.Apply(chargerOutputLens("bay")); len(loss) != 0 {
		t.Fatalf("apply loss %v", loss)
	}
	features := binding.ServerFeatures("bay")
	if outputBindingHasType(features, "measurement") || outputBindingHasType(features, "electrical connection") {
		t.Fatalf("withdrawn power monitoring left %v", features)
	}
	if !outputBindingHasType(features, "device diagnosis") {
		t.Fatalf("commissioning features %v", features)
	}
	gaps := binding.MonitoringLoss("bay")
	if !monitoringListed(gaps, "electrical connection") || monitoringListed(gaps, "measurement") {
		t.Fatalf("monitoring loss %v", gaps)
	}
}

func TestChargerOutputRejectedOperationFeature(t *testing.T) {
	// A charger that carries the admitted allocated current operation. The node rejects one
	// feature that operation binds. Apply keeps the entity applied and commissioning served.
	// load control: only the operation needs it. Both use cases stay served and the loss names it.
	// electrical connection: power monitoring and the operation need it. Both are withdrawn,
	// and neither measurement nor load control lands on the entity.
	cases := map[string]struct{ on, off []string }{
		"load control":          {[]string{"device diagnosis", "measurement", "electrical connection"}, []string{"load control"}},
		"electrical connection": {[]string{"device diagnosis"}, []string{"measurement", "electrical connection", "load control"}},
	}
	for feature, want := range cases {
		t.Run(feature, func(t *testing.T) {
			node := &chargerOutputRejectNode{outputBindingFakeNode: newOutputBindingFakeNode(), reject: feature}
			useOutputBindingNode(t, func() (shipNode, error) { return node, nil })
			binding, err := NewOutputBinding(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			result := chargerOutputLens("bay")
			result.Operations = []LensOperation{{Name: "evse.operation.set_allocated_current", Unit: "A", OwnCapability: true}}
			if loss := binding.Apply(result); len(loss) != 0 {
				t.Fatalf("apply loss %v", loss)
			}
			features := binding.ServerFeatures("bay")
			for _, name := range want.on {
				if !outputBindingHasType(features, name) {
					t.Fatalf("%s missing in %v", name, features)
				}
			}
			for _, name := range want.off {
				if outputBindingHasType(features, name) {
					t.Fatalf("%s left in %v", name, features)
				}
			}
			gaps := binding.MonitoringLoss("bay")
			if !monitoringListed(gaps, feature) || !monitoringListed(gaps, "device classification") {
				t.Fatalf("monitoring loss %v", gaps)
			}
		})
	}
}
