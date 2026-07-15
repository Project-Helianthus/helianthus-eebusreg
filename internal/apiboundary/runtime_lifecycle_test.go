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

func TestMSP055RuntimeExportInventoryIsExact(t *testing.T) {
	want := msp055RuntimeExportInventory()
	if maps.Equal(allowedRuntimeExports, want) {
		return
	}

	var missing []string
	for exported := range want {
		if _, ok := allowedRuntimeExports[exported]; !ok {
			missing = append(missing, exported.Kind+" "+exported.Name)
		}
	}
	var unexpected []string
	for exported := range allowedRuntimeExports {
		if _, ok := want[exported]; !ok {
			unexpected = append(unexpected, exported.Kind+" "+exported.Name)
		}
	}
	sort.Strings(missing)
	sort.Strings(unexpected)
	t.Fatalf(
		"runtime export inventory mismatch: got=%d want=%d missing=[%s] unexpected=[%s]",
		len(allowedRuntimeExports),
		len(want),
		strings.Join(missing, ", "),
		strings.Join(unexpected, ", "),
	)
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

func msp055RuntimeExportInventory() map[manifestExport]struct{} {
	return frozenExportInventory(`
const SnapshotContractV1
const ObservedRuntimeStateV1Unknown
const ObservedRuntimeStateV1Stopped
const ObservedRuntimeStateV1Starting
const ObservedRuntimeStateV1Ready
const ObservedRuntimeStateV1Degraded
const ObservedRuntimeStateV1Shutdown
const DegradationReasonV1MissingDiscovery
const DegradationReasonV1DeniedTrust
const DegradationReasonV1RemoteDisconnect
const DegradationReasonV1CertificateUnavailable
const DegradationReasonV1NoVisibleServices
const DegradationReasonV1NoData
const ServiceKindV1Local
const ServiceKindV1Remote
const ObservedSessionStateV1Unknown
const ObservedSessionStateV1Connecting
const ObservedSessionStateV1Connected
const ObservedSessionStateV1Disconnected
const ObservedSessionStateV1Degraded
const FeatureRoleV1Unspecified
const FeatureRoleV1Client
const FeatureRoleV1Server
func New
func NewSnapshotV1
func SnapshotV1.Clone
func SnapshotV1.ComputeDataHash
func SnapshotV1.Format
func SnapshotV1.GoString
func SnapshotV1.MarshalJSON
func SnapshotV1.String
func SnapshotV1.Validate
type Config
type DegradationReasonV1
type DegradationV1
type DeviceV1
type EntityV1
type FeatureRoleV1
type FeatureV1
type ObservedRuntimeStateV1
type ObservedSessionStateV1
type PairingObservationV1
type Remote
type Runtime
type RuntimeObservationV1
type ServiceKindV1
type ServiceV1
type SessionV1
type SnapshotMetaV1
type SnapshotV1
type TopologyV1
type UseCaseClaimV1
var ErrRuntimeDisabled
var ErrRuntimeShutdown
`)
}
