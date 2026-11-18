package eebusruntime

import "testing"

func TestInverterMonitoring(t *testing.T) {
	// An inverter lens with AC power and no string does not announce Monitoring of PV String.
	inverter := outputBindingInverter("north")
	inverter.Elements = []string{"inverter ac measurement"}
	outcome := monitorInverterLens(inverter)
	if monitoringListed(outcome.Announced, "Monitoring of PV String") {
		t.Fatalf("announced %v", outcome.Announced)
	}

	// A missing mandatory Monitoring of Inverter scenario does not announce that use case.
	if monitoringListed(outcome.Announced, "Monitoring of Inverter") {
		t.Fatalf("announced %v", outcome.Announced)
	}
	if !monitoringListed(outcome.Loss, "energy") {
		t.Fatalf("loss %v", outcome.Loss)
	}
}

func TestInverterMonitoringStringResource(t *testing.T) {
	// String fields are family, input dimension, contains, parent and the three mandatory elements.
	// family: string measurements on an inverter are not a string. The gate does not announce the string use case.
	// input dimension: a pv string fact on the input dimension is not a string. The gate returns that loss.
	// contains: an uncited string is not a string resource. The gate returns that loss.
	// parent: an empty parent is not a string resource. The gate returns that loss.
	// power, current, voltage: each missing element is named and the use case stays unannounced.
	inverter := outputBindingInverter("north")
	inverter.Elements = []string{"string dc power", "string dc current", "string dc voltage", "inverter ac measurement"}
	outcome := monitorInverterLens(inverter)
	if monitoringListed(outcome.Announced, "Monitoring of PV String") {
		t.Fatalf("inverter announced %v", outcome.Announced)
	}

	input := monitoringString("flat")
	input.InputDimension = true
	input.Elements = []string{"string dc power", "string dc current", "string dc voltage"}
	outcome = monitorInverterLens(input)
	if monitoringListed(outcome.Announced, "Monitoring of PV String") || !monitoringListed(outcome.Loss, "input dimension") {
		t.Fatalf("input announced %v loss %v", outcome.Announced, outcome.Loss)
	}

	uncited := monitoringString("loose")
	uncited.ContainsCited = false
	uncited.Elements = []string{"string dc power", "string dc current", "string dc voltage"}
	outcome = monitorInverterLens(uncited)
	if monitoringListed(outcome.Announced, "Monitoring of PV String") || !monitoringListed(outcome.Loss, "string resource") {
		t.Fatalf("uncited announced %v loss %v", outcome.Announced, outcome.Loss)
	}

	orphan := monitoringString("orphan")
	orphan.ParentResourceID = ""
	orphan.Elements = []string{"string dc power", "string dc current", "string dc voltage"}
	outcome = monitorInverterLens(orphan)
	if monitoringListed(outcome.Announced, "Monitoring of PV String") || !monitoringListed(outcome.Loss, "string resource") {
		t.Fatalf("orphan announced %v loss %v", outcome.Announced, outcome.Loss)
	}

	for _, missing := range []string{"string dc power", "string dc current", "string dc voltage"} {
		t.Run(missing, func(t *testing.T) {
			result := monitoringString("east")
			result.Elements = []string{"string dc power", "string dc current", "string dc voltage"}
			result.Elements = monitoringDrop(result.Elements, missing)
			outcome := monitorInverterLens(result)
			if monitoringListed(outcome.Announced, "Monitoring of PV String") || !monitoringListed(outcome.Loss, missing) {
				t.Fatalf("announced %v loss %v", outcome.Announced, outcome.Loss)
			}
		})
	}
}

func TestInverterMonitoringStringServed(t *testing.T) {
	// A cited string with power, current and voltage announces the use case and asks for the
	// measurement and electrical connection server features. String dc energy is not required.
	binding := newOutputBinding(t)
	if len(binding.Apply(outputBindingInverter("north"))) != 0 {
		t.Fatal("inverter")
	}
	result := monitoringString("east")
	result.Elements = []string{"string dc power", "string dc current", "string dc voltage"}
	if len(binding.Apply(result)) != 0 {
		t.Fatal("string")
	}
	outcome := binding.serveInverterMonitoring(result)
	if len(outcome.Announced) != 1 || outcome.Announced[0] != "Monitoring of PV String" {
		t.Fatalf("announced %v", outcome.Announced)
	}
	if monitoringListed(outcome.Loss, "string dc energy") || monitoringListed(outcome.Loss, "string dc power") {
		t.Fatalf("loss %v", outcome.Loss)
	}
	features := binding.ServerFeatures("east")
	if !outputBindingHasType(features, "measurement") || !outputBindingHasType(features, "electrical connection") {
		t.Fatalf("features %v", features)
	}
	if outputBindingHasType(features, "load control") {
		t.Fatalf("load control %v", features)
	}
}

func TestInverterMonitoringInverterGaps(t *testing.T) {
	// Every admitted identification and measurement element still leaves the use case unannounced.
	// Device classification and state information have no server helper, so the loss names them.
	result := outputBindingInverter("north")
	result.Elements = []string{
		"device name", "device code", "serial number", "software revision", "hardware revision",
		"vendor name", "brand name", "inverter grid code", "operating state", "energy",
		"power factor", "power production nominal max", "apparent power", "inverter ac measurement",
	}
	outcome := monitorInverterLens(result)
	if monitoringListed(outcome.Announced, "Monitoring of Inverter") {
		t.Fatalf("announced %v", outcome.Announced)
	}
	if !monitoringListed(outcome.Loss, "device classification") || !monitoringListed(outcome.Loss, "state information") {
		t.Fatalf("loss %v", outcome.Loss)
	}
}

func TestInverterMonitoringPowerDetails(t *testing.T) {
	// Scenario power details are filled by apparent power or reactive power.
	// neither: both names are the loss. The gate requires one of them.
	// apparent power: reactive power is not required. The gate accepts the one admitted element.
	// reactive power: apparent power is not required. The gate accepts the one admitted element.
	neither := outputBindingInverter("north")
	outcome := monitorInverterLens(neither)
	if !monitoringListed(outcome.Loss, "apparent power") || !monitoringListed(outcome.Loss, "reactive power") {
		t.Fatalf("loss %v", outcome.Loss)
	}
	apparent := neither
	apparent.Elements = []string{"apparent power"}
	outcome = monitorInverterLens(apparent)
	if monitoringListed(outcome.Loss, "apparent power") || monitoringListed(outcome.Loss, "reactive power") {
		t.Fatalf("apparent loss %v", outcome.Loss)
	}
	reactive := neither
	reactive.Elements = []string{"reactive power"}
	outcome = monitorInverterLens(reactive)
	if monitoringListed(outcome.Loss, "apparent power") || monitoringListed(outcome.Loss, "reactive power") {
		t.Fatalf("reactive loss %v", outcome.Loss)
	}
}

func TestInverterMonitoringProductionLimit(t *testing.T) {
	// Production-limit fields are name, unit, own capability and converted.
	// name: the system export operation is not the production limit. The gate does not select it.
	// unit: amperes are not the watt limit. The gate rejects that unit.
	// own capability: visibility without it is not admission. The gate rejects it.
	// converted: a converted value is not the admitted watt limit. The gate rejects it.
	// The write is selected only when the use case is announced and the operation is admitted.
	admitted := LensOperation{Name: "pv.operation.set_active_power_limit", Unit: "W", OwnCapability: true}
	cases := []struct {
		name      string
		operation LensOperation
		want      bool
	}{
		{"admitted", admitted, true},
		{"name", LensOperation{Name: "pv.operation.set_export_limit", Unit: "W", OwnCapability: true}, false},
		{"unit", LensOperation{Name: "pv.operation.set_active_power_limit", Unit: "A", OwnCapability: true}, false},
		{"own capability", LensOperation{Name: "pv.operation.set_active_power_limit", Unit: "W"}, false},
		{"converted", LensOperation{Name: "pv.operation.set_active_power_limit", Unit: "W", OwnCapability: true, Converted: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := outputBindingInverter("north")
			result.Operations = []LensOperation{tc.operation}
			if productionLimitAdmitted(result) != tc.want {
				t.Fatalf("admitted %v", productionLimitAdmitted(result))
			}
			selected := monitoringProductionWrite(true, result)
			if tc.want {
				if len(selected) != 1 || selected[0] != "pv.operation.set_active_power_limit" {
					t.Fatalf("selected %v", selected)
				}
			} else if len(selected) != 0 {
				t.Fatalf("selected %v", selected)
			}
			if monitoringListed(selected, "pv.operation.set_export_limit") {
				t.Fatalf("export selected %v", selected)
			}
			if len(monitoringProductionWrite(false, result)) != 0 {
				t.Fatal("unannounced write")
			}
		})
	}

	both := outputBindingInverter("north")
	both.Operations = []LensOperation{
		{Name: "pv.operation.set_export_limit", Unit: "W", OwnCapability: true},
		admitted,
	}
	selected := monitoringProductionWrite(true, both)
	if len(selected) != 1 || selected[0] != "pv.operation.set_active_power_limit" {
		t.Fatalf("selected %v", selected)
	}

	served := outputBindingInverter("north")
	served.Operations = []LensOperation{admitted}
	outcome := monitorInverterLens(served)
	if monitoringListed(outcome.Announced, "Limitation of Power Production") || len(outcome.Operations) != 0 {
		t.Fatalf("announced %v operations %v", outcome.Announced, outcome.Operations)
	}
	if !monitoringListed(outcome.Loss, "failsafe production active power limit") || !monitoringListed(outcome.Loss, "failsafe duration minimum") || !monitoringListed(outcome.Loss, "heartbeat") {
		t.Fatalf("loss %v", outcome.Loss)
	}

	named := served
	named.Elements = []string{"failsafe production active power limit", "failsafe duration minimum", "heartbeat"}
	outcome = monitorInverterLens(named)
	if monitoringListed(outcome.Announced, "Limitation of Power Production") || len(outcome.Operations) != 0 {
		t.Fatalf("named announced %v operations %v", outcome.Announced, outcome.Operations)
	}
	if monitoringListed(outcome.Loss, "failsafe production active power limit") || monitoringListed(outcome.Loss, "active power production limit") {
		t.Fatalf("named loss %v", outcome.Loss)
	}
	if !monitoringListed(outcome.Loss, "heartbeat") {
		t.Fatalf("heartbeat loss %v", outcome.Loss)
	}
}

func TestInverterMonitoringUnadmitted(t *testing.T) {
	result := outputBindingInverter("north")
	result.LensAdmitted = false
	result.Elements = []string{"string dc power", "string dc current", "string dc voltage"}
	outcome := monitorInverterLens(result)
	if len(outcome.Announced) != 0 || len(outcome.Loss) != 0 || len(outcome.Operations) != 0 {
		t.Fatalf("outcome %+v", outcome)
	}
	charger := LensResult{Family: "charger", ResourceID: "bay", LensAdmitted: true, RuntimeEnabled: true}
	outcome = monitorInverterLens(charger)
	if len(outcome.Announced) != 0 || len(outcome.Loss) != 0 {
		t.Fatalf("charger %+v", outcome)
	}
}

func monitoringString(resourceID string) LensResult {
	return LensResult{
		Family: "pv string", ResourceID: resourceID, ParentResourceID: "north",
		LensAdmitted: true, RuntimeEnabled: true, ContainsCited: true,
	}
}

func monitoringListed(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func monitoringDrop(values []string, drop string) []string {
	var kept []string
	for _, value := range values {
		if value != drop {
			kept = append(kept, value)
		}
	}
	return kept
}
