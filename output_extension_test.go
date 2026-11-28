package eebusruntime

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"testing"
)

func TestOutputExtensionStringEnergy(t *testing.T) {
	// A cited string with power, current and voltage is served. String DC energy fills
	// the energy of Monitoring of PV String, so MonitoringLoss names it only when the
	// result does not carry it. Its absence never withholds the use case.
	// carried: pv.dc.energy on the string fills the energy and leaves the loss.
	// absent: the loss names string dc energy and the use case keeps its features.
	// cited missing: the same element cited as missing fills nothing.
	// fill name: the scenario element name in place of the element fills nothing.
	// family: pv.dc.energy on the parent inverter does not fill the string.
	cases := []struct {
		name     string
		elements []string
		missing  []string
		onParent bool
		named    bool
	}{
		{"carried", []string{"pv.dc.energy"}, nil, false, false},
		{"absent", nil, nil, false, true},
		{"cited missing", []string{"pv.dc.energy"}, []string{"pv.dc.energy"}, false, true},
		{"fill name", []string{"string dc energy"}, nil, false, true},
		{"family", nil, nil, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			binding := newOutputBinding(t)
			parent := outputBindingInverter("north")
			if tc.onParent {
				parent.Elements = append(parent.Elements, "pv.dc.energy")
			}
			if loss := binding.Apply(parent); len(loss) != 0 {
				t.Fatalf("inverter loss %v", loss)
			}
			result := monitoringString("east")
			result.Elements = append([]string{"string dc power", "string dc current", "string dc voltage"}, tc.elements...)
			result.MissingElements = tc.missing
			binding.Apply(result)
			if got := monitoringListed(binding.MonitoringLoss("east"), "string dc energy"); got != tc.named {
				t.Fatalf("string dc energy named=%v, want %v: %v", got, tc.named, binding.MonitoringLoss("east"))
			}
			if !tc.named && len(binding.MonitoringLoss("east")) != 0 {
				t.Fatalf("served string loss %v", binding.MonitoringLoss("east"))
			}
			features := binding.ServerFeatures("east")
			if len(tc.missing) == 0 && (!outputBindingHasType(features, "measurement") || !outputBindingHasType(features, "electrical connection")) {
				t.Fatalf("string use case withdrawn %v", features)
			}
		})
	}
}

func TestOutputExtensionStringEnergyKeepsMandatory(t *testing.T) {
	// String DC energy does not stand in for a mandatory element. A string that carries
	// pv.dc.energy and lacks one of power, current or voltage stays unannounced and names it.
	for _, dropped := range []string{"string dc power", "string dc current", "string dc voltage"} {
		t.Run(dropped, func(t *testing.T) {
			result := monitoringString("east")
			result.Elements = monitoringDrop([]string{"string dc power", "string dc current", "string dc voltage", "pv.dc.energy"}, dropped)
			outcome := monitorInverterLens(withExtensionElements(result))
			if monitoringListed(outcome.Announced, "Monitoring of PV String") || !monitoringListed(outcome.Loss, dropped) {
				t.Fatalf("announced %v loss %v", outcome.Announced, outcome.Loss)
			}
		})
	}
}

func TestOutputExtensionLineToLineVoltage(t *testing.T) {
	// Line-to-line voltage fills the AC voltage of Monitoring of Inverter.
	// absent: an inverter without it still records the loss.
	// carried: infrastructure.ac.voltage_line_line on the inverter leaves the loss.
	// cited missing: the same element cited as missing fills nothing.
	// fill name: the scenario element name in place of the element fills nothing.
	// family: on a charger it names nothing, and the charger loss is unchanged.
	cases := []struct {
		name     string
		elements []string
		missing  []string
		named    bool
	}{
		{"absent", nil, nil, true},
		{"carried", []string{"infrastructure.ac.voltage_line_line"}, nil, false},
		{"cited missing", []string{"infrastructure.ac.voltage_line_line"}, []string{"infrastructure.ac.voltage_line_line"}, true},
		{"fill name", []string{"line-to-line voltage"}, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			binding := newOutputBinding(t)
			result := outputBindingInverter("north")
			result.Elements = tc.elements
			result.MissingElements = tc.missing
			binding.Apply(result)
			if got := monitoringListed(binding.MonitoringLoss("north"), "line-to-line voltage"); got != tc.named {
				t.Fatalf("line-to-line voltage named=%v, want %v: %v", got, tc.named, binding.MonitoringLoss("north"))
			}
		})
	}
	binding := newOutputBinding(t)
	charger := chargerOutputLens("bay")
	charger.Elements = append(charger.Elements, "infrastructure.ac.voltage_line_line")
	binding.Apply(charger)
	if gaps := binding.MonitoringLoss("bay"); len(gaps) != 1 || gaps[0] != "device classification" {
		t.Fatalf("charger loss %v", gaps)
	}
	plain := newOutputBinding(t)
	plain.Apply(chargerOutputLens("bay"))
	if gaps := plain.MonitoringLoss("bay"); len(gaps) != 1 || gaps[0] != "device classification" {
		t.Fatalf("charger loss without the element %v", gaps)
	}
}

func TestOutputExtensionInverterPowerDetails(t *testing.T) {
	// The power details of Monitoring of Inverter are filled by the inverter totals.
	// apparent total and reactive total: each alone fills them.
	// element: per-phase PV values and meter or grid-connection apparent or reactive
	// power do not fill them on an inverter.
	// family: an inverter total on a string or a charger fills nothing there.
	// cited missing: the total cited as missing fills nothing.
	fills := []string{"pv.ac.aggregate_apparent_power", "pv.ac.aggregate_reactive_power"}
	refused := []string{
		"pv.ac.apparent_power", "pv.ac.reactive_power",
		"infrastructure.ac.apparent_power", "infrastructure.ac.reactive_power",
	}
	for _, element := range append(append([]string(nil), fills...), refused...) {
		t.Run(element, func(t *testing.T) {
			result := outputBindingInverter("north")
			result.Elements = []string{element}
			outcome := monitorInverterLens(withExtensionElements(result))
			lost := monitoringListed(outcome.Loss, "apparent power") && monitoringListed(outcome.Loss, "reactive power")
			if lost == monitoringListed(fills, element) {
				t.Fatalf("loss %v", outcome.Loss)
			}
			result.MissingElements = []string{element}
			outcome = monitorInverterLens(withExtensionElements(result))
			if !monitoringListed(outcome.Loss, "apparent power") || !monitoringListed(outcome.Loss, "reactive power") {
				t.Fatalf("cited missing loss %v", outcome.Loss)
			}
		})
	}
	for _, family := range []LensResult{monitoringString("east"), chargerOutputLens("bay")} {
		family.Elements = append([]string(nil), fills...)
		got := withExtensionElements(family).Elements
		if monitoringListed(got, "apparent power") || monitoringListed(got, "reactive power") {
			t.Fatalf("%s filled %v", family.Family, got)
		}
	}
}

func TestOutputExtensionPhaseAllocatedCurrent(t *testing.T) {
	// Phase allocated current is not the connector limit, the argument of
	// evse.operation.set_allocated_current.
	// element: carried as an element it binds no load control and calls nothing.
	// operation name: named as an operation it binds no load control.
	// consumption limit: the allocated current operation is recognized and the refusal is absent.
	binding := newOutputBinding(t)
	element := chargerOutputLens("bay")
	element.Elements = append(element.Elements, "evse.limit.allocated_current_phase")
	if loss := binding.Apply(element); len(loss) != 0 {
		t.Fatalf("element loss %v", loss)
	}
	if outputBindingHasType(binding.ServerFeatures("bay"), "load control") {
		t.Fatalf("element bound %v", binding.ServerFeatures("bay"))
	}
	if outcome := monitorChargerLens(withExtensionElements(element)); len(outcome.Operations) != 0 {
		t.Fatalf("element called %v", outcome.Operations)
	}

	operation := chargerOutputLens("other")
	operation.Operations = []LensOperation{{Name: "evse.limit.allocated_current_phase", Unit: "A", OwnCapability: true}}
	binding.Apply(operation)
	if outputBindingHasType(binding.ServerFeatures("other"), "load control") {
		t.Fatalf("operation bound %v", binding.ServerFeatures("other"))
	}

	limit := chargerOutputLens("third")
	limit.UseCase = "Limitation of Power Consumption"
	limit.Elements = append(limit.Elements, "evse.limit.allocated_current_phase")
	limit.Operations = []LensOperation{{Name: "evse.operation.set_allocated_current", Unit: "A", OwnCapability: true}}
	if loss := binding.Apply(limit); len(loss) != 0 {
		t.Fatalf("limit loss %v", loss)
	}
	gaps := binding.MonitoringLoss("third")
	for _, want := range []string{"consumption_limit_not_applied", "missing operation: active power consumption limit", "not called: evse.operation.set_allocated_current"} {
		if monitoringListed(gaps, want) {
			t.Fatalf("monitoring loss still names %s: %v", want, gaps)
		}
	}
}

func TestOutputExtensionAnnouncement(t *testing.T) {
	// A use case withheld before the extension is announced only when every mandatory
	// scenario is filled. An inverter carrying every extension element and every
	// identification element is still withheld: device classification, state information
	// and heartbeat have no element. Its loss no longer names what the extension filled.
	result := outputBindingInverter("north")
	result.Elements = []string{
		"device name", "device code", "serial number", "software revision", "hardware revision",
		"vendor name", "brand name", "inverter grid code", "operating state", "energy",
		"power factor", "power production nominal max", "failsafe production active power limit",
		"failsafe duration minimum", "pv.ac.aggregate_apparent_power", "pv.ac.aggregate_reactive_power",
		"infrastructure.ac.voltage_line_line",
	}
	binding := newOutputBinding(t)
	if loss := binding.Apply(result); len(loss) != 0 {
		t.Fatalf("apply loss %v", loss)
	}
	gaps := binding.MonitoringLoss("north")
	for _, want := range []string{"device classification", "state information", "heartbeat"} {
		if !monitoringListed(gaps, want) {
			t.Fatalf("monitoring loss omits %s: %v", want, gaps)
		}
	}
	for _, filled := range []string{"apparent power", "reactive power", "line-to-line voltage"} {
		if monitoringListed(gaps, filled) {
			t.Fatalf("filled %s still named %v", filled, gaps)
		}
	}
	if outcome := monitorInverterLens(withExtensionElements(result)); len(outcome.Announced) != 0 {
		t.Fatalf("announced %v", outcome.Announced)
	}
}

func TestOutputExtensionKeepsResult(t *testing.T) {
	// Filling scenario elements works on a copy. The caller's elements are unchanged.
	result := outputBindingInverter("north")
	result.Elements = make([]string, 1, 4)
	result.Elements[0] = "pv.ac.aggregate_apparent_power"
	filled := withExtensionElements(result)
	if len(result.Elements) != 1 || result.Elements[:2][1] != "" {
		t.Fatalf("caller elements changed %v", result.Elements[:2])
	}
	if !monitoringListed(filled.Elements, "apparent power") {
		t.Fatalf("filled %v", filled.Elements)
	}
}

func TestOutputExtensionTypeChecksAlone(t *testing.T) {
	// The file imports no internal package, so the API boundary gate type-checks it
	// with the stable contract files only. It must not depend on a declaration of a
	// file that imports an internal package, such as LensResult.
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "output_extension.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Imports) != 0 {
		t.Fatalf("output_extension.go imports %d packages", len(file.Imports))
	}
	if _, err := new(types.Config).Check("eebusruntime", fset, []*ast.File{file}, nil); err != nil {
		t.Fatalf("output_extension.go does not type-check on its own: %v", err)
	}
}
