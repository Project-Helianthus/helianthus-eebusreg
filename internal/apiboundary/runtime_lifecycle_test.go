package main

import (
	"maps"
	"sort"
	"strings"
	"testing"
)

func TestMSP055LifecycleExportsAreAllowlisted(t *testing.T) {
	for _, exported := range msp055LifecycleExports() {
		if _, ok := allowedRuntimeExports[exported]; !ok {
			t.Errorf("MSP-055 runtime export is not allowlisted: %s %s", exported.Kind, exported.Name)
		}
	}
}

func TestMSP05PHistoricalRuntimeExportProjectionIsExact(t *testing.T) {
	want := msp05pRuntimeExportInventory()
	historical := msp05pHistoricalRuntimeExportProjection(allowedRuntimeExports)
	if maps.Equal(historical, want) {
		return
	}

	var missing []string
	for exported := range want {
		if _, ok := historical[exported]; !ok {
			missing = append(missing, exported.Kind+" "+exported.Name)
		}
	}
	var unexpected []string
	for exported := range historical {
		if _, ok := want[exported]; !ok {
			unexpected = append(unexpected, exported.Kind+" "+exported.Name)
		}
	}
	sort.Strings(missing)
	sort.Strings(unexpected)
	t.Fatalf(
		"historical MSP-05P runtime export projection mismatch: got=%d want=%d missing=[%s] unexpected=[%s]",
		len(historical),
		len(want),
		strings.Join(missing, ", "),
		strings.Join(unexpected, ", "),
	)
}

func msp05pHistoricalRuntimeExportProjection(
	current map[manifestExport]struct{},
) map[manifestExport]struct{} {
	baseline := msp05pRuntimeExportInventory()
	historical := make(map[manifestExport]struct{}, len(baseline))
	for exported := range baseline {
		if _, remainsPublished := current[exported]; remainsPublished {
			historical[exported] = struct{}{}
		}
	}
	return historical
}

func TestMSP05PInitialV1ContractIsExactlyAllowlisted(t *testing.T) {
	want := []manifestExport{
		{Kind: "const", Name: "PairingPolicyClosed"},
		{Kind: "func", Name: "New"},
		{Kind: "type", Name: "Config"},
		{Kind: "type", Name: "PairingPolicy"},
	}
	for _, exported := range want {
		if _, ok := allowedRuntimeExports[exported]; !ok {
			t.Errorf("MSP-05P runtime export is not allowlisted: %s %s", exported.Kind, exported.Name)
		}
	}
}

func TestIssue129NativeRuntimeV2ContractIsExactlyAllowlisted(t *testing.T) {
	want := loadExportInventory("lifecycle_native_runtime_v2.txt")
	if !maps.Equal(issue129NativeRuntimeV2Exports, want) {
		t.Fatal("issue #129 native V2 closed inventory drifted")
	}
	for exported := range want {
		if _, ok := allowedRuntimeExports[exported]; !ok {
			t.Errorf("issue #129 native V2 export is not allowlisted: %s %s", exported.Kind, exported.Name)
		}
	}
}

func TestPostM9OperatorAdminV1ContractIsExactlyAllowlisted(t *testing.T) {
	want := postM9OperatorAdminV1ExportInventory()
	if !maps.Equal(postM9OperatorAdminRuntimeExports, want) {
		t.Fatal("post-M9 AdminV1 snapshot-only exemption does not match the exact public inventory")
	}
	for exported := range want {
		if _, ok := allowedRuntimeExports[exported]; !ok {
			t.Errorf("post-M9 AdminV1 export is not allowlisted: %s %s", exported.Kind, exported.Name)
		}
	}
	if _, legacy := allowedRuntimeExports[manifestExport{Kind: "type", Name: "ActionHandleV1"}]; legacy {
		t.Error("post-M9 AdminV1 allowlist still exposes generic ActionHandleV1")
	}
}

func postM9OperatorAdminV1ExportInventory() map[manifestExport]struct{} {
	return loadExportInventory("lifecycle_operator_admin_v1.txt")
}

func msp055LifecycleExports() []manifestExport {
	return []manifestExport{
		{Kind: "func", Name: "New"},
		{Kind: "type", Name: "Config"},
		{Kind: "type", Name: "Remote"},
		{Kind: "type", Name: "Runtime"},
		{Kind: "var", Name: "ErrRuntimeDisabled"},
		{Kind: "var", Name: "ErrRuntimeShutdown"},
	}
}

func msp05pRuntimeExportInventory() map[manifestExport]struct{} {
	return loadExportInventory("lifecycle_runtime.txt")
}
