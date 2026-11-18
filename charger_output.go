package eebusruntime

import (
	"sort"
	"strings"

	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusfacade"
)

// The EVSE features land on the facade node of the running runtime service.
var _ shipNode = (*eebusfacade.OutputNode)(nil)

const (
	useCaseEVSECommissioning = "EVSE Commissioning and Configuration"
	useCasePowerMonitoring   = "Monitoring of Power Consumption"
	chargerElementTotalPower = "total active power"
	chargerElementState      = "operating state"
	chargerLossEV            = "EV"
	chargerLossPowerLimit    = "missing operation: active power consumption limit"
	chargerLossCurrentCall   = "not called: " + operationAllocatedCurrent
)

// chargerEVUseCases are use cases whose actor is the EV. The EVSE entity of
// this node never takes that actor, so a lens result naming one announces nothing.
var chargerEVUseCases = map[string]struct{}{
	"Overload Protection by EV Charging Current Curtailment": {},
	"Optimization of Self-Consumption During EV Charging":    {},
	"EV Charging Electricity Measurement":                    {},
	"Coordinated EV Charging":                                {},
	"EV Commissioning and Configuration":                     {},
	"EV State Of Charge":                                     {},
}

// chargerUseCaseFeatures lists, per announced use case, the server features
// the EVSE entity needs to fill its mandatory scenario. The node adds all of
// them or none, so a withdrawn use case leaves nothing of its own on the entity.
var chargerUseCaseFeatures = []struct {
	useCase  string
	features []string
}{
	{useCaseEVSECommissioning, []string{featureDiagnosis}},
	{useCasePowerMonitoring, []string{featureMeasurement, featureElectrical}},
}

// monitorChargerLens decides which use cases the EVSE entity announces for an
// admitted charger lens result. Monitoring of Power Consumption needs the total
// active power, and phase values are never summed into it. EVSE Commissioning
// and Configuration needs the operating state, and charger statuses never stand
// in for it. Limitation of Power Consumption is never announced, and a result
// naming it carries the same loss as a peer write of an active-power limit.
func monitorChargerLens(result LensResult) monitoringOutcome {
	if !chargerAdmitted(result) {
		return monitoringOutcome{}
	}
	if chargerEVActor(result) {
		return monitoringOutcome{Loss: []string{chargerLossEV}}
	}
	admitted := monitoringAdmittedElements(result)
	var outcome monitoringOutcome
	if monitoringHas(admitted, chargerElementState) {
		outcome.Announced = append(outcome.Announced, useCaseEVSECommissioning)
	} else {
		outcome.Loss = append(outcome.Loss, chargerElementState)
	}
	if monitoringHas(admitted, chargerElementTotalPower) {
		outcome.Announced = append(outcome.Announced, useCasePowerMonitoring)
	} else {
		outcome.Loss = append(outcome.Loss, chargerElementTotalPower)
	}
	outcome.Loss = append(outcome.Loss, "device classification")
	if result.UseCase == useCaseConsumption {
		outcome.Loss = append(outcome.Loss, "consumption_limit_not_applied")
		outcome.Loss = append(outcome.Loss, chargerConsumptionLimitWrite(result).Loss...)
	}
	sort.Strings(outcome.Loss)
	return outcome
}

// chargerConsumptionLimitWrite answers a peer write of an active-power
// consumption limit on the EVSE entity. The charger exposes no active-power
// operation, and a watt limit is never converted to allocated current, so the
// write calls nothing and its loss names both facts.
func chargerConsumptionLimitWrite(LensResult) monitoringOutcome {
	return monitoringOutcome{Loss: []string{chargerLossPowerLimit, chargerLossCurrentCall}}
}

// serveChargerOutput attaches the server features of each announced use case
// on the EVSE entity the binding added for the lens result, then the features
// of each admitted charger operation. The monitored unit is that entity and
// never the CEM entity. A feature the node rejects withdraws only the use case
// or the operation that needs it, and MonitoringLoss names that feature. The
// caller holds outputBindingGate.
func (b *OutputBinding) serveChargerOutput(result LensResult) monitoringOutcome {
	outcome := monitorChargerLens(result)
	operations := chargerOperationFeatures(result)
	if b == nil || (len(outcome.Announced) == 0 && len(operations) == 0) {
		return outcome
	}
	entity, ok := b.byResource[result.ResourceID]
	if b.node == nil || !ok || entity.kind != "EVSE" || !letterAddress(entity.address) {
		return monitoringWithdraw(outcome, "existing node")
	}
	var announced []string
	for _, entry := range chargerUseCaseFeatures {
		if !monitoringHas(outcome.Announced, entry.useCase) {
			continue
		}
		if rejected, ok := b.attachAll(entity.address, entry.features); !ok {
			outcome.Loss = chargerNameLoss(outcome.Loss, rejected)
			continue
		}
		announced = append(announced, entry.useCase)
	}
	for _, features := range operations {
		if rejected, ok := b.attachAll(entity.address, features); !ok {
			outcome.Loss = chargerNameLoss(outcome.Loss, rejected)
		}
	}
	outcome.Announced = announced
	sort.Strings(outcome.Loss)
	return outcome
}

// attachAll asks the node for every feature at once, so a rejected feature
// leaves none of them on the entity. It returns the rejected feature, or the
// existing-node loss when the node names none.
func (b *OutputBinding) attachAll(address string, features []string) (string, bool) {
	rejected, err := b.node.AddServerFeatures(address, features)
	if err == nil {
		return "", true
	}
	if rejected == "" {
		rejected = "existing node"
	}
	return rejected, false
}

func chargerNameLoss(loss []string, name string) []string {
	if monitoringHas(loss, name) {
		return loss
	}
	return append(loss, name)
}

// chargerOperationFeatures lists, per admitted charger operation, the server
// features that operation binds on the EVSE entity.
func chargerOperationFeatures(result LensResult) [][]string {
	if !chargerAdmitted(result) {
		return nil
	}
	var features [][]string
	for _, operation := range result.Operations {
		if bound := featuresForOperation(result.Family, operation); len(bound) > 0 {
			features = append(features, bound)
		}
	}
	return features
}

func chargerAdmitted(result LensResult) bool {
	if !result.LensAdmitted || !result.RuntimeEnabled || result.Basis != "" || result.Overlapping {
		return false
	}
	if result.ResourceID == "" || strings.ContainsAny(result.ResourceID, " \t\r\n") {
		return false
	}
	return result.Family == "charger"
}

func chargerEVActor(result LensResult) bool {
	if result.DeviceActor == chargerLossEV || result.Actor == chargerLossEV {
		return true
	}
	_, ev := chargerEVUseCases[result.UseCase]
	return ev
}
