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

// withExtensionElements returns a copy of the result whose elements also list
// each scenario element an extension element of that family fills. An
// extension element the result also cites as missing, by its own name or by
// the scenario element it fills, fills nothing.
func withExtensionElements(result LensResult) LensResult {
	elements := append([]string(nil), result.Elements...)
	for _, extension := range outputExtensions {
		if extensionCarried(result, extension) && !monitoringHas(elements, extension.fills) {
			elements = append(elements, extension.fills)
		}
	}
	result.Elements = elements
	return result
}

// extensionLoss names each scenario element of the entity that the result
// does not fill with its extension element. It does not change which use
// cases are announced: those still need every mandatory element.
func extensionLoss(result LensResult) []string {
	var loss []string
	for _, extension := range outputExtensions {
		if extension.named && extension.family == result.Family && !extensionCarried(result, extension) {
			loss = append(loss, extension.fills)
		}
	}
	return loss
}

func extensionCarried(result LensResult, extension outputExtension) bool {
	return result.Family == extension.family &&
		monitoringHas(result.Elements, extension.element) &&
		!monitoringHas(result.MissingElements, extension.element) &&
		!monitoringHas(result.MissingElements, extension.fills)
}
