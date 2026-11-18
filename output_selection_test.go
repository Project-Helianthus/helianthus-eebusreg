package eebusruntime

import (
	"reflect"
	"testing"
)

const outputSelectionTrustedSKI = "1111111111111111111111111111111111111111"

func outputSelectionInverter(id string) LensResult {
	return LensResult{Family: "inverter", ResourceID: id, LensAdmitted: true}
}

func outputSelectionCharger(id string) LensResult {
	return LensResult{Family: "charger", ResourceID: id, LensAdmitted: true}
}

func outputSelectionString(id, parent string) LensResult {
	return LensResult{Family: "pv string", ResourceID: id, ParentResourceID: parent, ContainsCited: true, LensAdmitted: true}
}

func outputSelectionProduction(id string) LensResult {
	result := outputSelectionInverter(id)
	result.UseCase = "Limitation of Power Production"
	result.Elements = []string{"failsafe production active power limit", "failsafe duration minimum"}
	result.Operations = []LensOperation{{Name: operationProductionLimit, Unit: "W", OwnCapability: true}}
	return result
}

func outputSelectionTrusted() []TrustedPartnerV1 {
	return []TrustedPartnerV1{{SKI: outputSelectionTrustedSKI, TrustState: "trusted"}}
}

func outputSelectionIDs(selection outputSelection) []string {
	var ids []string
	for _, result := range selection.Results {
		ids = append(ids, result.ResourceID)
	}
	return ids
}

func TestOutputSelectionEnabledSelectsInverterAndChargerAndDropsHeating(t *testing.T) {
	results := []LensResult{
		outputSelectionInverter("inverter-1"),
		outputSelectionCharger("charger-1"),
		{Family: "heating", ResourceID: "heating-1", LensAdmitted: true},
		{Family: "battery", ResourceID: "battery-1", LensAdmitted: true},
	}
	selection := selectOutputs(true, results, nil, nil)
	if got, want := outputSelectionIDs(selection), []string{"inverter-1", "charger-1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("selected = %v, want %v", got, want)
	}
	for _, result := range selection.Results {
		if !result.RuntimeEnabled {
			t.Fatalf("selected %s does not carry the enabled runtime", result.ResourceID)
		}
	}
}

func TestOutputSelectionDisabledSelectsNothing(t *testing.T) {
	results := []LensResult{
		outputSelectionProduction("inverter-1"),
		outputSelectionCharger("charger-1"),
		outputSelectionString("string-1", "inverter-1"),
	}
	selection := selectOutputs(false, results, nil, nil)
	if len(selection.Results) != 0 || len(selection.EnergyGuard) != 0 {
		t.Fatalf("disabled selection = %#v, want nothing", selection)
	}
}

func TestOutputSelectionNeedsLensAdmission(t *testing.T) {
	refused := []LensResult{
		{Family: "inverter", ResourceID: "inverter-1"},
		{Family: "charger", ResourceID: "charger-1", LensAdmitted: true, Basis: "estimated"},
		{Family: "charger", ResourceID: "charger 2", LensAdmitted: true},
		{Family: "charger", LensAdmitted: true},
	}
	if selection := selectOutputs(true, refused, nil, nil); len(selection.Results) != 0 {
		t.Fatalf("selected = %v, want none", outputSelectionIDs(selection))
	}
}

func TestOutputSelectionStringFollowsSelectedInverter(t *testing.T) {
	inputDimension := outputSelectionString("string-3", "inverter-1")
	inputDimension.InputDimension = true
	uncited := outputSelectionString("string-4", "inverter-1")
	uncited.ContainsCited = false
	results := []LensResult{
		outputSelectionString("string-1", "inverter-1"),
		outputSelectionString("string-2", "heating-1"),
		inputDimension,
		uncited,
		{Family: "heating", ResourceID: "heating-1", LensAdmitted: true},
		outputSelectionInverter("inverter-1"),
	}
	selection := selectOutputs(true, results, nil, nil)
	if got, want := outputSelectionIDs(selection), []string{"inverter-1", "string-1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("selected = %v, want %v", got, want)
	}
}

func TestOutputSelectionRepeatedOperationIsConflict(t *testing.T) {
	results := []LensResult{outputSelectionProduction("inverter-1"), outputSelectionProduction("inverter-1")}
	selection := selectOutputs(true, results, nil, nil)
	if len(selection.Results) != 2 {
		t.Fatalf("selected = %v, want both writes", outputSelectionIDs(selection))
	}
	for _, result := range selection.Results {
		if loss := announcementLoss(result); !reflect.DeepEqual(loss, []string{"conflict"}) {
			t.Fatalf("loss = %v, want conflict", loss)
		}
	}
	if len(selection.EnergyGuard) != 0 {
		t.Fatalf("energy guard = %v, want none for a conflict", selection.EnergyGuard)
	}
	distinct := []LensResult{outputSelectionProduction("inverter-1"), outputSelectionProduction("inverter-2")}
	for _, result := range selectOutputs(true, distinct, nil, nil).Results {
		if result.Overlapping {
			t.Fatalf("%s overlaps, want distinct resources to stay apart", result.ResourceID)
		}
	}
}

func TestOutputSelectionLocalEnergyGuardOnlyWithoutTrustedRemote(t *testing.T) {
	production := []LensResult{outputSelectionProduction("inverter-1")}
	want := []string{"Limitation of Power Production"}
	if got := selectOutputs(true, production, nil, nil).EnergyGuard; !reflect.DeepEqual(got, want) {
		t.Fatalf("energy guard without remote = %v, want %v", got, want)
	}
	remote := remoteAnnouncement{SKI: outputSelectionTrustedSKI, Actor: "Energy Guard", UseCase: "Limitation of Power Production"}
	if got := selectOutputs(true, production, outputSelectionTrusted(), []remoteAnnouncement{remote}).EnergyGuard; len(got) != 0 {
		t.Fatalf("energy guard with trusted remote = %v, want none", got)
	}
	otherSKI := remote
	otherSKI.SKI = "2222222222222222222222222222222222222222"
	otherActor := remote
	otherActor.Actor = "Controllable System"
	otherUseCase := remote
	otherUseCase.UseCase = "Limitation of Power Consumption"
	for name, announcement := range map[string]remoteAnnouncement{"ski": otherSKI, "actor": otherActor, "use case": otherUseCase} {
		got := selectOutputs(true, production, outputSelectionTrusted(), []remoteAnnouncement{announcement}).EnergyGuard
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("energy guard with remote differing in %s = %v, want %v", name, got, want)
		}
	}
	revoked := outputSelectionTrusted()
	revoked[0].TrustState = "revoked"
	if got := selectOutputs(true, production, revoked, []remoteAnnouncement{remote}).EnergyGuard; !reflect.DeepEqual(got, want) {
		t.Fatalf("energy guard with untrusted remote = %v, want %v", got, want)
	}
}

func TestOutputSelectionEnergyGuardNeedsMandatoryScenarios(t *testing.T) {
	noLimit := outputSelectionProduction("inverter-1")
	noLimit.Operations = nil
	converted := outputSelectionProduction("inverter-2")
	converted.Operations[0].Converted = true
	noFailsafe := outputSelectionProduction("inverter-3")
	noFailsafe.Elements = []string{"failsafe production active power limit"}
	missing := outputSelectionProduction("inverter-4")
	missing.MissingElements = []string{"failsafe duration minimum"}
	charger := outputSelectionCharger("charger-1")
	charger.UseCase = useCaseConsumption
	charger.Operations = []LensOperation{{Name: operationActivePower, Unit: "W", OwnCapability: true}}
	for _, result := range []LensResult{noLimit, converted, noFailsafe, missing, charger} {
		selection := selectOutputs(true, []LensResult{result}, nil, nil)
		if len(selection.Results) != 1 {
			t.Fatalf("%s not selected", result.ResourceID)
		}
		if len(selection.EnergyGuard) != 0 {
			t.Fatalf("%s energy guard = %v, want none", result.ResourceID, selection.EnergyGuard)
		}
	}
}

func TestOutputSelectionLeavesInputUntouched(t *testing.T) {
	results := []LensResult{outputSelectionProduction("inverter-1"), outputSelectionProduction("inverter-1")}
	selectOutputs(true, results, nil, nil)
	for _, result := range results {
		if result.RuntimeEnabled || result.Overlapping {
			t.Fatalf("input changed: %#v", result)
		}
	}
}
