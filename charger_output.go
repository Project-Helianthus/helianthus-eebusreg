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
		if node, ok := b.node.(*eebusfacade.OutputNode); ok {
			place.Surface = node
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
		Facts:           chargerFacts(result.Facts),
		MemberSum:       chargerMemberSum(result.MemberSum),
	}
}

func chargerFacts(facts []LensFact) []struct {
	Element     string
	Phase       string
	Connector   string
	Kind        string
	Coefficient string
	Exponent    int32
	Boolean     bool
	Text        string
	Symbol      string
	Unit        string
} {
	if facts == nil {
		return nil
	}
	copied := make([]struct {
		Element     string
		Phase       string
		Connector   string
		Kind        string
		Coefficient string
		Exponent    int32
		Boolean     bool
		Text        string
		Symbol      string
		Unit        string
	}, len(facts))
	for index, fact := range facts {
		copied[index] = struct {
			Element     string
			Phase       string
			Connector   string
			Kind        string
			Coefficient string
			Exponent    int32
			Boolean     bool
			Text        string
			Symbol      string
			Unit        string
		}{
			Element: fact.Element, Phase: fact.Phase, Connector: fact.Connector,
			Kind: fact.Value.Kind, Coefficient: fact.Value.Coefficient, Exponent: fact.Value.Exponent,
			Boolean: fact.Value.Boolean, Text: fact.Value.Text, Symbol: fact.Value.Symbol, Unit: fact.Unit,
		}
	}
	return copied
}

func chargerMemberSum(sum *LensMemberSum) *struct {
	Withheld         bool
	Coefficient      string
	Exponent         int32
	Unit             string
	Reason           string
	NotIncludedCount int
	Gaps             []struct {
		Asset  string
		Reason string
	}
} {
	if sum == nil {
		return nil
	}
	gaps := make([]struct {
		Asset  string
		Reason string
	}, len(sum.NotIncluded))
	for index, gap := range sum.NotIncluded {
		gaps[index] = struct {
			Asset  string
			Reason string
		}{Asset: gap.Asset, Reason: gap.Reason}
	}
	return &struct {
		Withheld         bool
		Coefficient      string
		Exponent         int32
		Unit             string
		Reason           string
		NotIncludedCount int
		Gaps             []struct {
			Asset  string
			Reason string
		}
	}{
		Withheld: sum.Withheld, Coefficient: sum.Coefficient, Exponent: sum.Exponent,
		Unit: sum.Unit, Reason: sum.Reason, NotIncludedCount: sum.NotIncludedCount, Gaps: gaps,
	}
}

func chargerOutcome(outcome charger.Outcome) monitoringOutcome {
	return monitoringOutcome{
		Announced:  append([]string(nil), outcome.Announced...),
		Loss:       append([]string(nil), outcome.Loss...),
		Operations: append([]string(nil), outcome.Operations...),
	}
}
