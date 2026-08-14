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
	return frozenExportInventory(`
const AdminViewV1Trusted
const AdminViewV1Connected
const AdminViewV1Discovered
const AdminViewV1Candidate
const AdminErrorCodeV1AdminBoundaryUnavailable
const AdminErrorCodeV1Unauthenticated
const AdminErrorCodeV1Forbidden
const AdminErrorCodeV1CSRFRejected
const AdminErrorCodeV1InvalidRequest
const AdminErrorCodeV1StateConflict
const AdminErrorCodeV1SnapshotExpired
const AdminErrorCodeV1IdempotencyConflict
const AdminErrorCodeV1PairingClosed
const AdminErrorCodeV1ObservationStale
const AdminErrorCodeV1IdentityMismatch
const AdminErrorCodeV1AssociationIncomplete
const AdminErrorCodeV1CandidateExpired
const AdminErrorCodeV1CandidateBusy
const AdminErrorCodeV1TrustDenied
const AdminErrorCodeV1ListenerUnavailable
const AdminErrorCodeV1DiscoveryUnavailable
const AdminErrorCodeV1AttemptTimeout
const AdminErrorCodeV1Disconnected
const AdminErrorCodeV1BackoffActive
const AdminErrorCodeV1TerminalQuarantine
const AdminErrorCodeV1PersistenceFailure
const AdminErrorCodeV1UnknownState
func AdminErrorV1.Error
func NewOperatorRuntimeV1
type AdminV1
type AdminErrorCodeV1
type AdminErrorV1
type AdminOutcomeV1
type AdminViewV1
type AdminSnapshotV1
type AdminSnapshotRequestV1
type AdminMutationResultV1
type AdminSelectionResultV1
type MutationPreconditionV1
type OpenPairingWindowRequestV1
type ClosePairingWindowRequestV1
type SelectRequestV1
type ConnectRequestV1
type ConfirmRequestV1
type CancelRequestV1
type RetryTrustedRequestV1
type UntrustRequestV1
type CandidateHandleV1
type ConnectedPartnerV1
type DiscoveredPartnerV1
type ObservationHandleV1
type CandidateV1
type PartnerHandleV1
type SelectionHandleV1
type TrustedPartnerV1
`)
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
type RawFeatureRuntimeV1
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
