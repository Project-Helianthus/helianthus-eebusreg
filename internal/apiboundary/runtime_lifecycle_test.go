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

func TestMSP05PRuntimeExportInventoryIsExact(t *testing.T) {
	want := msp05pRuntimeExportInventory()
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
const PairingPolicyClosed
func BuildRedactedSnapshotV1
func MetadataV1.MarshalJSON
func MetadataV1.UnmarshalJSON
func MetadataValueV1.MarshalJSON
func MetadataValueV1.UnmarshalJSON
func New
func NewSnapshotV1
func OpaqueScalarV1.MarshalJSON
func OpaqueScalarV1.UnmarshalJSON
func OpaqueValueV1.MarshalJSON
func OpaqueValueV1.UnmarshalJSON
func RedactedSnapshotV1.Format
func RedactedSnapshotV1.GoString
func RedactedSnapshotV1.MarshalJSON
func RedactedSnapshotV1.String
func RedactedSnapshotV1.UnmarshalJSON
func RedactedSnapshotV1.Validate
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
type MetadataV1
type MetadataValueV1
type ObservedRuntimeStateV1
type ObservedSessionStateV1
type OpaqueObservationV1
type OpaqueScalarV1
type OpaqueValueV1
type PairingObservationV1
type PairingPolicy
type RedactedDeviceV1
type RedactedEntityV1
type RedactedFeatureV1
type RedactedServiceV1
type RedactedSessionV1
type RedactedSnapshotMetaV1
type RedactedSnapshotV1
type RedactedUseCaseV1
type Remote
type Runtime
type RuntimeObservationV1
type ServiceKindV1
type ServiceV1
type SessionV1
type SnapshotMetaV1
type SnapshotV1
type UseCaseV1
var ErrRuntimeDisabled
var ErrRuntimeShutdown
`)
}
