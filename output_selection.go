package eebusruntime

import (
	"strings"

	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusfacade"
)

// The selected results are applied on the facade node of the running runtime
// service.
var _ shipNode = (*eebusfacade.OutputNode)(nil)

const (
	actorEnergyGuard        = "Energy Guard"
	useCaseProduction       = "Limitation of Power Production"
	trustStateTrusted       = "trusted"
	elementFailsafeLimit    = "failsafe production active power limit"
	elementFailsafeDuration = "failsafe duration minimum"
	selectionFamilyInverter = "inverter"
	selectionFamilyPVString = "pv string"
	selectionFamilyCharger  = "charger"
)

// outputSelection is what the node publishes for one set of lens results:
// the results handed to Apply and the limitation use cases for which the
// local CEM originates Energy Guard.
type outputSelection struct {
	Results     []LensResult
	EnergyGuard []string
}

// remoteAnnouncement is one use case a remote node announces, keyed by the
// SKI of that node. It counts only when the admin snapshot lists that SKI as a
// trusted partner; the selection never creates trust.
type remoteAnnouncement struct {
	SKI     string
	Actor   string
	UseCase string
}

// selectOutputs applies the single publication rule. With the runtime
// disabled by its existing configuration nothing is selected. Otherwise every
// lens-admitted inverter, PV string and charger is selected, and every other
// family, heating and battery included, is dropped. A PV string follows only
// an inverter selected in the same set and comes after every other selected
// result, so Apply binds its parent first. Two results that write the same
// admitted operation of one resource both carry the existing conflict loss.
// The local CEM originates Energy Guard for a limitation use case only when no
// trusted remote announces Energy Guard for it and a selected result fills
// every mandatory scenario of that use case. The input is never modified.
func selectOutputs(runtimeEnabled bool, results []LensResult, trusted []TrustedPartnerV1, remotes []remoteAnnouncement) outputSelection {
	if !runtimeEnabled {
		return outputSelection{}
	}
	var selection outputSelection
	inverters := map[string]struct{}{}
	for _, result := range results {
		if result.Family == selectionFamilyInverter && selectionAdmitted(result) {
			inverters[result.ResourceID] = struct{}{}
		}
	}
	for _, stringPass := range []bool{false, true} {
		for _, result := range results {
			if !selectionAdmitted(result) || (result.Family == selectionFamilyPVString) != stringPass {
				continue
			}
			if stringPass {
				if _, ok := inverters[result.ParentResourceID]; !ok || !result.ContainsCited || result.InputDimension {
					continue
				}
			}
			result.RuntimeEnabled = true
			result.Elements = append([]string(nil), result.Elements...)
			result.MissingElements = append([]string(nil), result.MissingElements...)
			result.Operations = append([]LensOperation(nil), result.Operations...)
			selection.Results = append(selection.Results, result)
		}
	}
	selectionMarkConflicts(selection.Results)
	for _, useCase := range []string{useCaseProduction, useCaseConsumption} {
		if selectionRemoteGuard(useCase, trusted, remotes) {
			continue
		}
		for _, result := range selection.Results {
			if selectionFillsGuard(useCase, result) {
				selection.EnergyGuard = append(selection.EnergyGuard, useCase)
				break
			}
		}
	}
	return selection
}

func selectionAdmitted(result LensResult) bool {
	if !result.LensAdmitted || result.Basis != "" || result.ResourceID == "" {
		return false
	}
	if strings.ContainsAny(result.ResourceID, " \t\r\n") {
		return false
	}
	switch result.Family {
	case selectionFamilyInverter, selectionFamilyPVString, selectionFamilyCharger:
		return true
	default:
		return false
	}
}

// selectionMarkConflicts marks every result whose resource and admitted
// operation another result of the set also writes.
func selectionMarkConflicts(results []LensResult) {
	writers := map[[2]string]int{}
	for _, result := range results {
		for _, name := range selectionOperations(result) {
			writers[[2]string{result.ResourceID, name}]++
		}
	}
	for index := range results {
		for _, name := range selectionOperations(results[index]) {
			if writers[[2]string{results[index].ResourceID, name}] > 1 {
				results[index].Overlapping = true
			}
		}
	}
}

// selectionOperations lists the operations of the result that pass the
// existing write admission for its family, so a rejected name or unit never
// counts as a write.
func selectionOperations(result LensResult) []string {
	var names []string
	for _, operation := range result.Operations {
		if featuresForOperation(result.Family, operation) != nil && !monitoringHas(names, operation.Name) {
			names = append(names, operation.Name)
		}
	}
	return names
}

// selectionRemoteGuard reports whether a partner the trust store lists as
// trusted announces Energy Guard for the use case.
func selectionRemoteGuard(useCase string, trusted []TrustedPartnerV1, remotes []remoteAnnouncement) bool {
	for _, remote := range remotes {
		if remote.Actor != actorEnergyGuard || remote.UseCase != useCase || remote.SKI == "" {
			continue
		}
		for _, partner := range trusted {
			if partner.SKI == remote.SKI && partner.TrustState == trustStateTrusted {
				return true
			}
		}
	}
	return false
}

// selectionFillsGuard applies the announce rule: the use case is announced
// only when the result fills its mandatory scenarios. Production needs the
// admitted active-power limit and both failsafe values; consumption needs an
// applied active-power limit, which a charger never has.
func selectionFillsGuard(useCase string, result LensResult) bool {
	if result.UseCase != useCase || announcementLoss(result) != nil {
		return false
	}
	switch useCase {
	case useCaseProduction:
		admitted := monitoringAdmittedElements(result)
		return result.Family == selectionFamilyInverter && productionLimitAdmitted(result) &&
			monitoringHas(admitted, elementFailsafeLimit) && monitoringHas(admitted, elementFailsafeDuration)
	case useCaseConsumption:
		return consumptionApplied(result)
	default:
		return false
	}
}
