package eebusruntime

import (
	"sort"
	"strings"

	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusfacade"
)

func monitorInverterLens(result LensResult) monitoringOutcome {
	if !monitoringAdmitted(result) {
		return monitoringOutcome{}
	}
	if result.Family == "pv string" {
		return monitorStringLens(result)
	}
	return monitorInverterEntity(result)
}

type monitoringOutcome struct {
	Announced  []string
	Loss       []string
	Operations []string
}

// serveInverterMonitoring admits the lens result and attaches the PV-string
// server features when that use case is announced. Apply calls it while holding
// outputBindingGate, on the node that just received the entity.
func (b *OutputBinding) serveInverterMonitoring(result LensResult) monitoringOutcome {
	outcome := monitorInverterLens(result)
	if b == nil || !monitoringHas(outcome.Announced, "Monitoring of PV String") {
		return outcome
	}
	node := monitoringBoundNode(b.node)
	entity, ok := b.byResource[result.ResourceID]
	if node == nil || !ok || entity.kind != "PV String" || entity.address == "" {
		return monitoringWithdraw(outcome, "existing node")
	}
	for _, feature := range []string{featureMeasurement, featureElectrical} {
		if err := node.AddServerFeature(entity.address, feature); err != nil {
			return monitoringWithdraw(outcome, feature)
		}
	}
	return outcome
}

func monitoringFeatureGap(outcome monitoringOutcome) bool {
	return monitoringHas(outcome.Loss, "existing node") ||
		monitoringHas(outcome.Loss, featureMeasurement) ||
		monitoringHas(outcome.Loss, featureElectrical)
}

func productionLimitAdmitted(result LensResult) bool {
	for _, operation := range result.Operations {
		matched := operation.Name == operationProductionLimit &&
			operation.Unit == "W" &&
			operation.OwnCapability &&
			!operation.Converted
		if matched {
			return true
		}
	}
	return false
}

func monitoringProductionWrite(announced bool, result LensResult) []string {
	if announced && productionLimitAdmitted(result) {
		return []string{operationProductionLimit}
	}
	return nil
}

func monitoringAdmitted(result LensResult) bool {
	if !result.LensAdmitted || !result.RuntimeEnabled || result.Basis != "" {
		return false
	}
	if result.ResourceID == "" || strings.ContainsAny(result.ResourceID, " \t\r\n") {
		return false
	}
	return result.Family == "inverter" || result.Family == "pv string"
}

func monitorStringLens(result LensResult) monitoringOutcome {
	if result.InputDimension {
		return monitoringOutcome{Loss: []string{"input dimension"}}
	}
	if !result.ContainsCited || result.ParentResourceID == "" {
		return monitoringOutcome{Loss: []string{"string resource"}}
	}
	var loss []string
	for _, element := range []string{"string dc power", "string dc current", "string dc voltage"} {
		if !monitoringHas(result.Elements, element) {
			loss = append(loss, element)
		}
	}
	if len(loss) > 0 {
		return monitoringOutcome{Loss: loss}
	}
	return monitoringOutcome{Announced: []string{"Monitoring of PV String"}}
}

func monitorInverterEntity(result LensResult) monitoringOutcome {
	required := []string{
		"device name",
		"device code",
		"serial number",
		"software revision",
		"hardware revision",
		"vendor name",
		"brand name",
		"inverter grid code",
		"operating state",
		"energy",
		"power factor",
		"power production nominal max",
	}
	loss := make([]string, 0, len(required)+6)
	for _, element := range required {
		if !monitoringHas(result.Elements, element) {
			loss = append(loss, element)
		}
	}
	if !monitoringHas(result.Elements, "apparent power") && !monitoringHas(result.Elements, "reactive power") {
		loss = append(loss, "apparent power", "reactive power")
	}
	loss = append(loss, "device classification", "state information")
	if !productionLimitAdmitted(result) {
		loss = append(loss, "active power production limit")
	}
	for _, element := range []string{"failsafe production active power limit", "failsafe duration minimum"} {
		if !monitoringHas(result.Elements, element) {
			loss = append(loss, element)
		}
	}
	loss = append(loss, "heartbeat")
	sort.Strings(loss)
	return monitoringOutcome{Loss: loss, Operations: monitoringProductionWrite(false, result)}
}

func monitoringWithdraw(outcome monitoringOutcome, gap string) monitoringOutcome {
	outcome.Announced = nil
	outcome.Loss = append(outcome.Loss, gap)
	return outcome
}

func monitoringBoundNode(node shipNode) shipNode {
	if _, ok := node.(*eebusfacade.OutputNode); ok {
		return node
	}
	return node
}

func monitoringHas(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
