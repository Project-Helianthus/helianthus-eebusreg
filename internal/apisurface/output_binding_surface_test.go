package main

import "testing"

// outputBindingSymbol keeps the historical public projection byte-identical.
// TestOutputBindingSurfaceIsClosed attests the binding on its own.
func outputBindingSymbol(symbol symbol) bool {
	switch symbol.Name {
	case "NewOutputBinding", "OutputBinding", "LensResult", "LensOperation",
		"LensFact", "LensValue", "LensQuality", "LensMemberSum", "LensNotIncluded":
		return true
	}
	if symbol.Receiver == nil {
		return false
	}
	switch symbol.Receiver.Base {
	case "OutputBinding", "LensResult", "LensOperation",
		"LensFact", "LensValue", "LensQuality", "LensMemberSum", "LensNotIncluded":
		return true
	default:
		return false
	}
}

func TestOutputBindingSurfaceIsClosed(t *testing.T) {
	doc, err := extract(moduleRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	root := msp05pRootSurface(t, doc)
	want := map[string]string{
		"func NewOutputBinding":                  "func NewOutputBinding(Runtime, string) (*OutputBinding, error)",
		"func OutputBinding.AdvertisementCount":  "func (*OutputBinding) AdvertisementCount() int",
		"func OutputBinding.Apply":               "func (*OutputBinding) Apply(LensResult) []string",
		"func OutputBinding.ApplyCut":            "func (*OutputBinding) ApplyCut([]LensResult) [][]string",
		"func OutputBinding.DeviceType":          "func (*OutputBinding) DeviceType() string",
		"func OutputBinding.EntityAddress":       "func (*OutputBinding) EntityAddress(string) string",
		"func OutputBinding.EntityAddresses":     "func (*OutputBinding) EntityAddresses() []string",
		"func OutputBinding.EntityPresent":       "func (*OutputBinding) EntityPresent(string) bool",
		"func OutputBinding.EntityTypes":         "func (*OutputBinding) EntityTypes() []string",
		"func OutputBinding.MonitoringLoss":      "func (*OutputBinding) MonitoringLoss(string) []string",
		"func OutputBinding.ParentAddress":       "func (*OutputBinding) ParentAddress(string) string",
		"func OutputBinding.PowerLimitFallbacks": "func (*OutputBinding) PowerLimitFallbacks() []OutputPowerLimitFallback",
		"func OutputBinding.RetainSession":       "func (*OutputBinding) RetainSession(interface{}, []OutputPowerLimitFallback)",
		"func OutputBinding.RetainedOperations":  "func (*OutputBinding) RetainedOperations() interface{}",
		"func OutputBinding.ServerFeatures":      "func (*OutputBinding) ServerFeatures(string) []string",
		"func OutputBinding.ServiceCount":        "func (*OutputBinding) ServiceCount() int",
		"func OutputBinding.Withdraw":            "func (*OutputBinding) Withdraw(string) error",
		"type LensFact":                          "type LensFact struct{ Element string; Phase string; Connector string; Value LensValue; Unit string; Quality LensQuality }",
		"type LensMemberSum":                     "type LensMemberSum struct{ Withheld bool; Coefficient string; Exponent int32; Unit string; Reason string; NotIncludedCount int; NotIncluded []LensNotIncluded }",
		"type LensNotIncluded":                   "type LensNotIncluded struct{ Asset string; Reason string }",
		"type LensOperation":                     "type LensOperation struct{ Name string; Unit string; OwnCapability bool; Converted bool }",
		"type LensQuality":                       "type LensQuality struct{ Assertion string; Qualification string; Promotion string; Validity string; Availability string; Freshness string }",
		"type LensResult":                        "type LensResult struct{ Family string; ResourceID string; ParentResourceID string; LensAdmitted bool; RuntimeEnabled bool; InputDimension bool; ContainsCited bool; Basis string; Elements []string; Operations []LensOperation; UseCase string; Actor string; UseCaseText string; DeviceActor string; MissingElements []string; Overlapping bool; Facts []LensFact; MemberSum *LensMemberSum }",
		"type LensValue":                         "type LensValue struct{ Kind string; Coefficient string; Exponent int32; Boolean bool; Text string; Symbol string }",
		"type OutputBinding":                     "type OutputBinding struct{ directory string; addresses map[string]string; byResource map[string]boundEntity; order []string; node shipNode; gaps map[string][]string; runtime Runtime; operations interface{}; fallbacks []OutputPowerLimitFallback }",
	}
	got := map[string]string{}
	for _, symbol := range root.Symbols {
		if !outputBindingSymbol(symbol) {
			continue
		}
		kind, name := symbol.Kind, symbol.Name
		if symbol.Receiver != nil {
			kind = "func"
			name = symbol.Receiver.Base + "." + name
		}
		key := kind + " " + name
		if _, ok := want[key]; !ok {
			t.Fatalf("unreviewed output binding export %s", symbol.Signature)
		}
		got[key] = symbol.Signature
	}
	if len(got) != len(want) {
		t.Fatalf("output binding exports = %d, want %d", len(got), len(want))
	}
	for key, signature := range want {
		if got[key] != signature {
			t.Errorf("%s signature = %q, want %q", key, got[key], signature)
		}
	}
	projected := msp05pProjectFrozenV1(t, doc)
	for _, symbol := range msp05pRootSurface(t, projected).Symbols {
		if outputBindingSymbol(symbol) {
			t.Fatalf("historical projection retained %s", symbol.Signature)
		}
	}
}
