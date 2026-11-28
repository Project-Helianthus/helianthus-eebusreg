package charger

import (
	"sort"
	"strings"

	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusfacade"
)

const (
	useCaseEVSECommissioning  = "EVSE Commissioning and Configuration"
	useCasePowerMonitoring    = "Monitoring of Power Consumption"
	elementTotalPower         = "total active power"
	elementState              = "operating state"
	lossEV                    = "EV"
	operationAllocatedCurrent = "evse.operation.set_allocated_current"
	featureDiagnosis          = "device diagnosis"
	featureMeasurement        = "measurement"
	featureElectrical         = "electrical connection"
	featureLoad               = "load control"
	kindEVSE                  = "EVSE"
	cemAddress                = "cem"
)

// evUseCases are use cases whose actor is the EV. The EVSE entity never takes
// that actor, so a lens result naming one announces nothing.
var evUseCases = map[string]struct{}{
	"Overload Protection by EV Charging Current Curtailment": {},
	"Optimization of Self-Consumption During EV Charging":    {},
	"EV Charging Electricity Measurement":                    {},
	"Coordinated EV Charging":                                {},
	"EV Commissioning and Configuration":                     {},
	"EV State Of Charge":                                     {},
}

// useCaseFeatures lists, per announced use case, the server features the EVSE
// entity needs. A withdrawn use case leaves none of its own features behind.
var useCaseFeatures = []struct {
	useCase  string
	features []string
}{
	{useCaseEVSECommissioning, []string{featureDiagnosis}},
	{useCasePowerMonitoring, []string{featureMeasurement, featureElectrical}},
}

// Monitor decides which use cases an admitted charger lens announces.
// Limitation of Power Consumption is never announced.
func Monitor(result Result) Outcome {
	if !admitted(result) {
		return Outcome{}
	}
	if evActor(result) {
		return Outcome{Loss: []string{lossEV}}
	}
	var outcome Outcome
	if fillsOperatingState(result) {
		outcome.Announced = append(outcome.Announced, useCaseEVSECommissioning)
	} else {
		outcome.Loss = append(outcome.Loss, elementState)
	}
	// The free-text total name still selects the use case. It does not count as a served total.
	if listed(admittedElements(result), elementTotalPower) || fillsTotal(result) || hasMeasurement(result) {
		outcome.Announced = append(outcome.Announced, useCasePowerMonitoring)
	}
	if !fillsTotal(result) {
		outcome.Loss = append(outcome.Loss, totalLoss(result)...)
	}
	outcome.Loss = append(outcome.Loss, energyLosses(result)...)
	outcome.Loss = append(outcome.Loss, "device classification")
	for _, operation := range result.Operations {
		if recognizedCurrent(operation) {
			outcome.Operations = append(outcome.Operations, operation.Name)
		}
	}
	sort.Strings(outcome.Loss)
	return outcome
}

// Serve attaches the server features of each announced use case, then the
// features of each admitted operation. attach is called once per group. A
// rejected group withdraws only the use case or operation that needs it.
func Serve(result Result, place Placement, attach func([]string) (string, error)) Outcome {
	outcome := Monitor(result)
	operations := operationFeatures(result)
	if !place.Binding || (len(outcome.Announced) == 0 && len(operations) == 0) {
		if err := clearStaleDiagnosis(result, place); err != nil {
			outcome.Loss = nameLoss(outcome.Loss, featureDiagnosis)
			sort.Strings(outcome.Loss)
		}
		if err := clearStaleMeasurements(result, place); err != nil {
			outcome.Loss = nameLoss(outcome.Loss, featureMeasurement)
			sort.Strings(outcome.Loss)
		}
		return outcome
	}
	if !place.Node || !place.Found || place.Kind != kindEVSE || !letterAddress(place.Address) {
		return withdraw(outcome, "existing node")
	}
	var announced []string
	for _, entry := range useCaseFeatures {
		if !listed(outcome.Announced, entry.useCase) {
			continue
		}
		// attach may reorder its argument. The copy keeps this order for the next EVSE.
		group := append([]string(nil), entry.features...)
		if rejected, ok := acceptGroup(attach, group); !ok {
			outcome.Loss = nameLoss(outcome.Loss, rejected)
			continue
		}
		announced = append(announced, entry.useCase)
	}
	for _, features := range operations {
		if rejected, ok := acceptGroup(attach, features); !ok {
			outcome.Loss = nameLoss(outcome.Loss, rejected)
		}
	}
	outcome.Announced = announced
	if node, ok := place.Surface.(*eebusfacade.OutputNode); ok {
		if rejected, err := publishCharger(node, place.Address, result, announced); err != nil {
			outcome.Loss = nameLoss(outcome.Loss, rejected)
		}
	}
	sort.Strings(outcome.Loss)
	return outcome
}

func clearStaleDiagnosis(result Result, place Placement) error {
	if !admitted(result) || !place.Node || !place.Found || place.Kind != kindEVSE || !letterAddress(place.Address) {
		return nil
	}
	node, ok := place.Surface.(*eebusfacade.OutputNode)
	if !ok {
		return nil
	}
	return publishUnknownDiagnosis(node, place.Address, result)
}

func clearStaleMeasurements(result Result, place Placement) error {
	if !admitted(result) || !place.Node || !place.Found || place.Kind != kindEVSE || !letterAddress(place.Address) {
		return nil
	}
	node, ok := place.Surface.(*eebusfacade.OutputNode)
	if !ok {
		return nil
	}
	return retireUnannouncedMeasurements(node, place.Address)
}

func acceptGroup(attach func([]string) (string, error), features []string) (string, bool) {
	rejected, err := attach(features)
	if err == nil {
		return "", true
	}
	if rejected == "" {
		rejected = "existing node"
	}
	return rejected, false
}

func nameLoss(loss []string, name string) []string {
	if listed(loss, name) {
		return loss
	}
	return append(loss, name)
}

func operationFeatures(result Result) [][]string {
	if !admitted(result) {
		return nil
	}
	var features [][]string
	for _, operation := range result.Operations {
		if bound := featuresForOperation(operation); len(bound) > 0 {
			features = append(features, bound)
		}
	}
	return features
}

func recognizedCurrent(operation Operation) bool {
	return operation.OwnCapability && !operation.Converted && operation.Name == operationAllocatedCurrent && operation.Unit == "A"
}

func featuresForOperation(operation Operation) []string {
	if !recognizedCurrent(operation) {
		return nil
	}
	return []string{featureElectrical, featureLoad}
}

func admitted(result Result) bool {
	if !result.LensAdmitted || !result.RuntimeEnabled || result.Basis != "" || result.Overlapping {
		return false
	}
	if result.ResourceID == "" || strings.ContainsAny(result.ResourceID, " \t\r\n") {
		return false
	}
	return result.Family == "charger"
}

func evActor(result Result) bool {
	if result.DeviceActor == lossEV || result.Actor == lossEV {
		return true
	}
	_, ev := evUseCases[result.UseCase]
	return ev
}

func admittedElements(result Result) []string {
	admitted := make([]string, 0, len(result.Elements))
	for _, element := range result.Elements {
		if !listed(result.MissingElements, element) {
			admitted = append(admitted, element)
		}
	}
	return admitted
}

func listed(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func withdraw(outcome Outcome, gap string) Outcome {
	outcome.Announced = nil
	outcome.Loss = append(outcome.Loss, gap)
	return outcome
}

func letterAddress(value string) bool {
	if value == "" || value == cemAddress {
		return false
	}
	for _, r := range value {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}
