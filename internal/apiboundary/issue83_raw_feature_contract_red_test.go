package main

import (
	"sort"
	"testing"
)

func TestIssue83StableRawFeatureDTOsRemainInTheAdditiveContract(t *testing.T) {
	var contract *stableContractSpec
	specs := stableContractSpecs(canonicalModulePath)
	for index := range specs {
		spec := &specs[index]
		if spec.importPath == canonicalModulePath+"/eebusraw" &&
			spec.root == "ReadAuthorizationV1" {
			contract = spec
			break
		}
	}
	if contract == nil {
		t.Fatal("MSP-0625 stable DTO contract is not enforced by the API boundary gate")
	}

	got := make(map[string]struct{}, len(contract.types))
	for _, stableType := range contract.types {
		got[stableType.Name] = struct{}{}
	}
	var missing []string
	for _, name := range issue83RawFeatureStableTypeNames() {
		if _, ok := got[name]; !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) != 0 {
		t.Fatalf("MSP-0625 historical stable types missing from additive owner: %v", missing)
	}
}

func issue83RawFeatureStableTypeNames() []string {
	return []string{
		"AuthScopeV1",
		"ChangeabilityV1",
		"ConstraintSetV1",
		"ConstraintStatusV1",
		"ErrorCodeV1",
		"ErrorDetailsV1",
		"ErrorV1",
		"FeatureDataGetDataV1",
		"FeatureDataGetRequestV1",
		"FeatureLocatorV1",
		"FeatureRoleV1",
		"FeatureTargetV1",
		"FeaturesGetDataV1",
		"FeaturesGetRequestV1",
		"FullOperationsV1",
		"FunctionDescriptorV1",
		"HashV1",
		"ObservationSourceV1",
		"OpaqueObservationV1",
		"OperationV1",
		"ProtocolMessageV1",
		"ReadAuthorizationV1",
		"ReadFailureV1",
		"ReadObservationV1",
		"ReadTokenV1",
		"RuntimeBindingV1",
		"SourceLayerV1",
		"ToolV1",
		"TypedValueV1",
	}
}

func TestIssue83StableRawFeatureGateRejectsFieldTypeAndTagMutations(t *testing.T) {
	var target manifestStableType
	for _, spec := range stableContractSpecs(canonicalModulePath) {
		if spec.importPath != canonicalModulePath+"/eebusraw" ||
			spec.root != "ReadAuthorizationV1" {
			continue
		}
		for _, stableType := range spec.types {
			if stableType.Name == "FeatureTargetV1" {
				target = stableType
				break
			}
		}
	}
	if len(target.Fields) == 0 {
		t.Fatal("FeatureTargetV1 exact field contract is missing")
	}
	mutations := []struct {
		name   string
		mutate func(*manifestStableType)
	}{
		{
			name: "field",
			mutate: func(value *manifestStableType) {
				value.Fields[0].Name = "RemoteAlias"
			},
		},
		{
			name: "type",
			mutate: func(value *manifestStableType) {
				value.Fields[0].Type = "[]byte"
			},
		},
		{
			name: "json tag",
			mutate: func(value *manifestStableType) {
				value.Fields[0].JSONTag = `json:"remote_alias"`
			},
		},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			actual := target
			actual.Fields = append([]manifestStableField(nil), target.Fields...)
			mutation.mutate(&actual)
			if stableContractTypeMatches(actual, target) {
				t.Fatalf("%s mutation bypassed exact field/type/tag gate", mutation.name)
			}
		})
	}
}
