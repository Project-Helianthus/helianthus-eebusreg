package main

import "testing"

// outputBindingSymbol keeps the historical public projection byte-identical.
// TestOutputBindingSurfaceIsClosed attests the binding on its own.
func outputBindingSymbol(symbol symbol) bool {
	if symbol.Name == "NewOutputBinding" || symbol.Name == "OutputBinding" {
		return true
	}
	return symbol.Receiver != nil && symbol.Receiver.Base == "OutputBinding"
}

func TestOutputBindingSurfaceIsClosed(t *testing.T) {
	doc, err := extract(moduleRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	root := msp05pRootSurface(t, doc)
	want := map[string]string{
		"func NewOutputBinding":                 "func NewOutputBinding(string) (*OutputBinding, error)",
		"func OutputBinding.AdvertisementCount": "func (*OutputBinding) AdvertisementCount() int",
		"func OutputBinding.Apply":              "func (*OutputBinding) Apply(lensResult) []string",
		"func OutputBinding.DeviceType":         "func (*OutputBinding) DeviceType() string",
		"func OutputBinding.EntityAddress":      "func (*OutputBinding) EntityAddress(string) string",
		"func OutputBinding.EntityAddresses":    "func (*OutputBinding) EntityAddresses() []string",
		"func OutputBinding.EntityTypes":        "func (*OutputBinding) EntityTypes() []string",
		"func OutputBinding.ParentAddress":      "func (*OutputBinding) ParentAddress(string) string",
		"func OutputBinding.ServerFeatures":     "func (*OutputBinding) ServerFeatures(string) []string",
		"func OutputBinding.ServiceCount":       "func (*OutputBinding) ServiceCount() int",
		"type OutputBinding":                    "type OutputBinding struct{ directory string; addresses map[string]string; byResource map[string]boundEntity; order []string; features map[string][]string }",
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
