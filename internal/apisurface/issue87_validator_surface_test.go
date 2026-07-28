package main

import "testing"

func TestIssue87CanonicalValidatorSurfaceIsExact(t *testing.T) {
	doc, err := extract(moduleRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	var raw *surface
	for index := range doc.Packages {
		if doc.Packages[index].Path == modulePath+"/eebusraw" {
			raw = &doc.Packages[index]
			break
		}
	}
	if raw == nil {
		t.Fatalf("public manifest omitted %s/eebusraw", modulePath)
	}
	symbols := issue85SymbolsByName(raw.Symbols)
	want := map[string]string{
		"ValidateFeatureDataSetRequestV1":   "func ValidateFeatureDataSetRequestV1(FeatureDataSetRequestV1) *ErrorV1",
		"ValidateMutationGetRequestV1":      "func ValidateMutationGetRequestV1(MutationGetRequestV1) *ErrorV1",
		"ValidateMutationRollbackRequestV1": "func ValidateMutationRollbackRequestV1(MutationRollbackRequestV1) *ErrorV1",
		"ValidateFeaturesGetDataV1":         "func ValidateFeaturesGetDataV1(FeaturesGetRequestV1, FeaturesGetDataV1) *ErrorV1",
		"ValidateFeatureDataGetDataV1":      "func ValidateFeatureDataGetDataV1(FeatureDataGetRequestV1, FeatureDataGetDataV1, *ErrorV1) *ErrorV1",
		"ValidateMutationV1":                "func ValidateMutationV1(MutationV1) *ErrorV1",
	}
	for name, signature := range want {
		got, exists := symbols[name]
		if !exists {
			t.Errorf("eebusraw missing canonical validator %s", name)
			continue
		}
		if got.Kind != "func" || got.Signature != signature {
			t.Errorf("%s = kind %q signature %q, want func %q", name, got.Kind, got.Signature, signature)
		}
	}
}
