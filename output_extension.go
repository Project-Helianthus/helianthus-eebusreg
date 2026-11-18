package eebusruntime

// outputExtension maps one element the semantic packs export onto the
// family whose entity serves it and the scenario element it fills there.
// A named element is one the entity lists as a loss when the lens result does
// not carry it.
type outputExtension struct {
	element string
	family  string
	fills   string
	named   bool
}

// outputExtensions are the extension elements an output entity consumes.
// String DC energy fills the energy of Monitoring of PV String. The inverter
// totals of apparent and reactive power fill the power details of Monitoring
// of Inverter; the apparent and reactive power of a meter or grid connection
// never do. Line-to-line voltage fills the AC voltage of Monitoring of
// Inverter. Phase allocated current fills no scenario of a use case this node
// announces, and it is never the argument of the allocated current operation,
// which stays the connector limit.
var outputExtensions = []outputExtension{
	{element: "pv.dc.energy", family: "pv string", fills: "string dc energy", named: true},
	{element: "pv.ac.aggregate_apparent_power", family: "inverter", fills: "apparent power"},
	{element: "pv.ac.aggregate_reactive_power", family: "inverter", fills: "reactive power"},
	{element: "infrastructure.ac.voltage_line_line", family: "inverter", fills: "line-to-line voltage", named: true},
}

// This file depends on no other declaration of the package, so it stays
// inside the set of files the stable contract type-checks on their own.

// extensionElements returns a copy of the elements of a result of the family
// that also lists each scenario element an extension element of that family
// fills. An extension element the result also cites as missing, by its own
// name or by the scenario element it fills, fills nothing.
func extensionElements(family string, elements, missing []string) []string {
	filled := append([]string(nil), elements...)
	for _, extension := range outputExtensions {
		if extensionCarried(family, elements, missing, extension) && !extensionListed(filled, extension.fills) {
			filled = append(filled, extension.fills)
		}
	}
	return filled
}

// extensionLosses names each scenario element of the entity that the result
// does not fill with its extension element. It does not change which use
// cases are announced: those still need every mandatory element.
func extensionLosses(family string, elements, missing []string) []string {
	var loss []string
	for _, extension := range outputExtensions {
		if extension.named && extension.family == family && !extensionCarried(family, elements, missing, extension) {
			loss = append(loss, extension.fills)
		}
	}
	return loss
}

func extensionCarried(family string, elements, missing []string, extension outputExtension) bool {
	return family == extension.family &&
		extensionListed(elements, extension.element) &&
		!extensionListed(missing, extension.element) &&
		!extensionListed(missing, extension.fills)
}

func extensionListed(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
