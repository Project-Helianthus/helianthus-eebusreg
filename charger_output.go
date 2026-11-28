package eebusruntime

import (
	"github.com/Project-Helianthus/helianthus-eebusreg/driver/output/charger"
	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusfacade"
)

// The EVSE features land on the facade node of the running runtime service.
var _ shipNode = (*eebusfacade.OutputNode)(nil)

func monitorChargerLens(result LensResult) monitoringOutcome {
	return chargerOutcome(charger.Monitor(chargerResult(result)))
}

// serveChargerOutput attaches the server features of each announced use case
// on the EVSE entity the binding added. The caller holds outputBindingGate.
func (b *OutputBinding) serveChargerOutput(result LensResult) monitoringOutcome {
	var place charger.Placement
	if b != nil {
		entity, found := b.byResource[result.ResourceID]
		place = charger.Placement{
			Binding: true,
			Found:   found,
			Kind:    entity.kind,
			Address: entity.address,
			Node:    b.node != nil,
		}
	}
	return chargerOutcome(charger.Serve(chargerResult(result), place, func(features []string) (string, error) {
		return b.node.AddServerFeatures(place.Address, features)
	}))
}

func chargerResult(result LensResult) charger.Result {
	var operations []charger.Operation
	if result.Operations != nil {
		operations = make([]charger.Operation, len(result.Operations))
		for index, operation := range result.Operations {
			operations[index] = charger.Operation{
				Name:          operation.Name,
				Unit:          operation.Unit,
				OwnCapability: operation.OwnCapability,
				Converted:     operation.Converted,
			}
		}
	}
	return charger.Result{
		Family:          result.Family,
		ResourceID:      result.ResourceID,
		LensAdmitted:    result.LensAdmitted,
		RuntimeEnabled:  result.RuntimeEnabled,
		Basis:           result.Basis,
		Elements:        append([]string(nil), result.Elements...),
		Operations:      operations,
		UseCase:         result.UseCase,
		Actor:           result.Actor,
		DeviceActor:     result.DeviceActor,
		MissingElements: append([]string(nil), result.MissingElements...),
		Overlapping:     result.Overlapping,
	}
}

func chargerOutcome(outcome charger.Outcome) monitoringOutcome {
	return monitoringOutcome{
		Announced:  append([]string(nil), outcome.Announced...),
		Loss:       append([]string(nil), outcome.Loss...),
		Operations: append([]string(nil), outcome.Operations...),
	}
}
