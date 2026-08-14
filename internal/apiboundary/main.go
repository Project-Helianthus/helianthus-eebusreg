package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/mod/modfile"
)

const canonicalModulePath = "github.com/Project-Helianthus/helianthus-eebusreg"

const minimalREADME = "# eeBUS Registry\n\nCanonical docs: Project-Helianthus/helianthus-docs-eebus.\n\nBuild: `./scripts/ci_local.sh`.\n"

var forbiddenExportFragments = []string{
	"Registry",
	"Projection",
	"Semantic",
	"Enbility",
	"Ship",
	"SHIP",
	"Spine",
	"SPINE",
	"EvidenceRef",
	"Dereference",
	"GraphQL",
	"Portal",
	"HomeAssistant",
	"CommandRoute",
	"CommandRouting",
	"TrustStore",
	"TrustMutation",
	"PairingWindow",
}

var mutationVerbs = []string{
	"Register",
	"Unregister",
	"Set",
	"Accept",
	"Pair",
	"Authorize",
	"Trust",
	"Add",
	"Remove",
	"Delete",
	"Update",
	"Enable",
	"Disable",
	"Open",
	"Close",
	"Write",
	"Mutate",
}

var mutationNouns = []string{
	"RemoteSKI",
	"SKI",
	"Peer",
	"Remote",
	"Pairing",
	"Trust",
	"Trusted",
	"Certificate",
	"Fingerprint",
}

var forbiddenImports = []string{
	"github.com/Project-Helianthus/helianthus-ebusgateway",
	"github.com/Project-Helianthus/helianthus-ha-integration",
}

var allowedPublicPackages = map[string]string{
	".":             "eebusruntime",
	"eebusevidence": "eebusevidence",
	"eebusraw":      "eebusraw",
}

var allowedRuntimeExports = map[manifestExport]struct{}{
	{Kind: "const", Name: "SnapshotContractV1"}:                        {},
	{Kind: "const", Name: "ObservedRuntimeStateV1Unknown"}:             {},
	{Kind: "const", Name: "ObservedRuntimeStateV1Stopped"}:             {},
	{Kind: "const", Name: "ObservedRuntimeStateV1Starting"}:            {},
	{Kind: "const", Name: "ObservedRuntimeStateV1Ready"}:               {},
	{Kind: "const", Name: "ObservedRuntimeStateV1Degraded"}:            {},
	{Kind: "const", Name: "ObservedRuntimeStateV1Shutdown"}:            {},
	{Kind: "const", Name: "DegradationReasonV1MissingDiscovery"}:       {},
	{Kind: "const", Name: "DegradationReasonV1DeniedTrust"}:            {},
	{Kind: "const", Name: "DegradationReasonV1RemoteDisconnect"}:       {},
	{Kind: "const", Name: "DegradationReasonV1CertificateUnavailable"}: {},
	{Kind: "const", Name: "DegradationReasonV1NoVisibleServices"}:      {},
	{Kind: "const", Name: "DegradationReasonV1NoData"}:                 {},
	{Kind: "const", Name: "ServiceKindV1Local"}:                        {},
	{Kind: "const", Name: "ServiceKindV1Remote"}:                       {},
	{Kind: "const", Name: "ObservedSessionStateV1Unknown"}:             {},
	{Kind: "const", Name: "ObservedSessionStateV1Connecting"}:          {},
	{Kind: "const", Name: "ObservedSessionStateV1Connected"}:           {},
	{Kind: "const", Name: "ObservedSessionStateV1Disconnected"}:        {},
	{Kind: "const", Name: "ObservedSessionStateV1Degraded"}:            {},
	{Kind: "const", Name: "FeatureRoleV1Unspecified"}:                  {},
	{Kind: "const", Name: "FeatureRoleV1Client"}:                       {},
	{Kind: "const", Name: "FeatureRoleV1Server"}:                       {},
	{Kind: "const", Name: "PairingPolicyClosed"}:                       {},
	{Kind: "func", Name: "BuildRedactedSnapshotV1"}:                    {},
	{Kind: "func", Name: "MetadataV1.MarshalJSON"}:                     {},
	{Kind: "func", Name: "MetadataV1.UnmarshalJSON"}:                   {},
	{Kind: "func", Name: "MetadataValueV1.MarshalJSON"}:                {},
	{Kind: "func", Name: "MetadataValueV1.UnmarshalJSON"}:              {},
	{Kind: "func", Name: "New"}:                                        {},
	{Kind: "func", Name: "NewSnapshotV1"}:                              {},
	{Kind: "func", Name: "OpaqueScalarV1.MarshalJSON"}:                 {},
	{Kind: "func", Name: "OpaqueScalarV1.UnmarshalJSON"}:               {},
	{Kind: "func", Name: "OpaqueValueV1.MarshalJSON"}:                  {},
	{Kind: "func", Name: "OpaqueValueV1.UnmarshalJSON"}:                {},
	{Kind: "func", Name: "RedactedSnapshotV1.Format"}:                  {},
	{Kind: "func", Name: "RedactedSnapshotV1.GoString"}:                {},
	{Kind: "func", Name: "RedactedSnapshotV1.MarshalJSON"}:             {},
	{Kind: "func", Name: "RedactedSnapshotV1.String"}:                  {},
	{Kind: "func", Name: "RedactedSnapshotV1.UnmarshalJSON"}:           {},
	{Kind: "func", Name: "RedactedSnapshotV1.Validate"}:                {},
	{Kind: "func", Name: "RawMutationOutcomeV1.Clone"}:                 {},
	{Kind: "func", Name: "SnapshotV1.Clone"}:                           {},
	{Kind: "func", Name: "SnapshotV1.ComputeDataHash"}:                 {},
	{Kind: "func", Name: "SnapshotV1.Format"}:                          {},
	{Kind: "func", Name: "SnapshotV1.GoString"}:                        {},
	{Kind: "func", Name: "SnapshotV1.MarshalJSON"}:                     {},
	{Kind: "func", Name: "SnapshotV1.String"}:                          {},
	{Kind: "func", Name: "SnapshotV1.Validate"}:                        {},
	{Kind: "type", Name: "DegradationReasonV1"}:                        {},
	{Kind: "type", Name: "DegradationV1"}:                              {},
	{Kind: "type", Name: "DeviceV1"}:                                   {},
	{Kind: "type", Name: "EntityV1"}:                                   {},
	{Kind: "type", Name: "FeatureRoleV1"}:                              {},
	{Kind: "type", Name: "FeatureV1"}:                                  {},
	{Kind: "type", Name: "MetadataV1"}:                                 {},
	{Kind: "type", Name: "MetadataValueV1"}:                            {},
	{Kind: "type", Name: "ObservedRuntimeStateV1"}:                     {},
	{Kind: "type", Name: "ObservedSessionStateV1"}:                     {},
	{Kind: "type", Name: "OpaqueObservationV1"}:                        {},
	{Kind: "type", Name: "OpaqueScalarV1"}:                             {},
	{Kind: "type", Name: "OpaqueValueV1"}:                              {},
	{Kind: "type", Name: "PairingObservationV1"}:                       {},
	{Kind: "type", Name: "PairingPolicy"}:                              {},
	{Kind: "type", Name: "RedactedDeviceV1"}:                           {},
	{Kind: "type", Name: "RedactedEntityV1"}:                           {},
	{Kind: "type", Name: "RedactedFeatureV1"}:                          {},
	{Kind: "type", Name: "RedactedServiceV1"}:                          {},
	{Kind: "type", Name: "RedactedSessionV1"}:                          {},
	{Kind: "type", Name: "RedactedSnapshotMetaV1"}:                     {},
	{Kind: "type", Name: "RedactedSnapshotV1"}:                         {},
	{Kind: "type", Name: "RedactedUseCaseV1"}:                          {},
	{Kind: "type", Name: "Remote"}:                                     {},
	{Kind: "type", Name: "RawFeatureRuntimeV1"}:                        {},
	{Kind: "type", Name: "RawMutationOutcomeV1"}:                       {},
	{Kind: "type", Name: "RawMutationRuntimeV1"}:                       {},
	{Kind: "type", Name: "Runtime"}:                                    {},
	{Kind: "type", Name: "RuntimeObservationV1"}:                       {},
	{Kind: "type", Name: "ServiceKindV1"}:                              {},
	{Kind: "type", Name: "ServiceV1"}:                                  {},
	{Kind: "type", Name: "SessionV1"}:                                  {},
	{Kind: "type", Name: "SnapshotMetaV1"}:                             {},
	{Kind: "type", Name: "SnapshotV1"}:                                 {},
	{Kind: "type", Name: "UseCaseV1"}:                                  {},
	{Kind: "type", Name: "Config"}:                                     {},
	{Kind: "type", Name: "AdminV1"}:                                    {},
	{Kind: "type", Name: "AdminSnapshotV1"}:                            {},
	{Kind: "type", Name: "ActionHandleV1"}:                             {},
	{Kind: "type", Name: "CandidateHandleV1"}:                          {},
	{Kind: "type", Name: "ConnectedPartnerV1"}:                         {},
	{Kind: "type", Name: "DiscoveredPartnerV1"}:                        {},
	{Kind: "type", Name: "ObservationHandleV1"}:                        {},
	{Kind: "type", Name: "PairingCandidateV1"}:                         {},
	{Kind: "type", Name: "PartnerHandleV1"}:                            {},
	{Kind: "type", Name: "SelectionHandleV1"}:                          {},
	{Kind: "type", Name: "TrustedPartnerV1"}:                           {},
	{Kind: "var", Name: "ErrRuntimeDisabled"}:                          {},
	{Kind: "var", Name: "ErrRuntimeShutdown"}:                          {},
}

var msp055RuntimeExports = map[manifestExport]struct{}{
	{Kind: "const", Name: "PairingPolicyClosed"}:       {},
	{Kind: "func", Name: "New"}:                        {},
	{Kind: "func", Name: "RawMutationOutcomeV1.Clone"}: {},
	{Kind: "type", Name: "Config"}:                     {},
	{Kind: "type", Name: "PairingPolicy"}:              {},
	{Kind: "type", Name: "RawFeatureRuntimeV1"}:        {},
	{Kind: "type", Name: "RawMutationOutcomeV1"}:       {},
	{Kind: "type", Name: "RawMutationRuntimeV1"}:       {},
	{Kind: "type", Name: "Remote"}:                     {},
	{Kind: "type", Name: "Runtime"}:                    {},
	{Kind: "var", Name: "ErrRuntimeDisabled"}:          {},
	{Kind: "var", Name: "ErrRuntimeShutdown"}:          {},
}

var allowedMSP035RawExports = frozenExportInventory(`
const EndpointRoleLocal
const EndpointRoleRemote
const EndpointRoleV1Local
const EndpointRoleV1Remote
const IDKindCertificateFingerprint
const IDKindLocalSKI
const IDKindPeer
const IDKindRemoteSKI
const IDKindSession
const IdentityContractV1
const IdentityContractV1Alpha1
const MaskTierRedacted
const PairingStateDenied
const PairingStatePaired
const PairingStateUnknown
const PairingStateUnpaired
const SessionStateDegraded
const SessionStateDisconnected
const SessionStateObserved
const SessionStateUnknown
const UnknownPathDevice
const UnknownPathDocument
const UnknownPathLocal
const UnknownPathRemote
const UnknownPathSession
func EndpointIdentity.Validate
func EndpointIdentityV1.Format
func EndpointIdentityV1.GoString
func EndpointIdentityV1.MarshalJSON
func EndpointIdentityV1.String
func EndpointIdentityV1.Validate
func EndpointRoleV1.Format
func EndpointRoleV1.GoString
func EndpointRoleV1.MarshalJSON
func EndpointRoleV1.String
func EndpointRoleV1.Validate
func IDKind.Format
func IDKind.GoString
func IDKind.MarshalJSON
func IDKind.String
func IDKind.Validate
func IdentityDocument.Format
func IdentityDocument.GoString
func IdentityDocument.MarshalJSON
func IdentityDocument.String
func IdentityDocument.Validate
func IdentityDocumentV1.Format
func IdentityDocumentV1.GoString
func IdentityDocumentV1.MarshalJSON
func IdentityDocumentV1.String
func IdentityDocumentV1.Validate
func NewIdentityDocument
func NewIdentityDocumentV1
func OpaqueBytes
func OpaqueValue.Format
func OpaqueValue.GoString
func OpaqueValue.MarshalJSON
func OpaqueValue.String
func OpaqueValue.Validate
func PairingObservation.Validate
func RedactID
func RedactedID.Format
func RedactedID.GoString
func RedactedID.MarshalJSON
func RedactedID.String
func RedactedID.Validate
func SessionIdentity.Validate
func SessionIdentityV1.Format
func SessionIdentityV1.GoString
func SessionIdentityV1.MarshalJSON
func SessionIdentityV1.String
func SessionIdentityV1.Validate
func UnknownField.Format
func UnknownField.GoString
func UnknownField.MarshalJSON
func UnknownField.String
func UnknownField.Validate
func UnknownPath.Format
func UnknownPath.GoString
func UnknownPath.MarshalJSON
func UnknownPath.String
func UnknownPath.Validate
type ContractVersion
type EndpointIdentity
type EndpointIdentityV1
type EndpointRole
type EndpointRoleV1
type IDKind
type IdentityDocument
type IdentityDocumentV1
type MaskTier
type OpaqueValue
type PairingObservation
type PairingState
type RedactedID
type SessionIdentity
type SessionIdentityV1
type SessionState
type UnknownField
type UnknownPath
`)

var allowedMSP0625RawExports = frozenExportInventory(`
const AuthScopeV1RawRead
const ChangeabilityV1False
const ChangeabilityV1True
const ChangeabilityV1Unknown
const ConstraintStatusV1Known
const ConstraintStatusV1Unknown
const ErrorCodeV1Cancelled
const ErrorCodeV1ConnectionGenerationMismatch
const ErrorCodeV1DecodeError
const ErrorCodeV1Disconnected
const ErrorCodeV1Internal
const ErrorCodeV1InvalidArgument
const ErrorCodeV1NotFound
const ErrorCodeV1PartialOperationForbidden
const ErrorCodeV1PartialResult
const ErrorCodeV1PermissionDenied
const ErrorCodeV1RemoteError
const ErrorCodeV1RuntimeEpochMismatch
const ErrorCodeV1SecretDetected
const ErrorCodeV1Timeout
const ErrorCodeV1TypedEmpty
const ErrorCodeV1UnsupportedOperation
const FeatureRoleV1Client
const FeatureRoleV1Server
const FeatureRoleV1Special
const MaskTierRaw
const MaximumReadTargetsV1
const MaximumReadTimeoutMSV1
const ObservationSourceV1Cache
const ObservationSourceV1Live
const OperationV1Read
const OperationV1Write
const SourceLayerV1Authorization
const SourceLayerV1Decode
const SourceLayerV1Executor
const SourceLayerV1Remote
const SourceLayerV1Runtime
const SourceLayerV1SpineRoundTrip
const SourceLayerV1Validation
const ToolV1FeaturesDataGet
const ToolV1FeaturesGet
func CanonicalSHA256V1
func ConstraintSetV1.Clone
func DecodeTypedValueV1
func ErrorV1.Clone
func ErrorV1.Error
func ErrorDetailsV1.Clone
func FeatureDataGetDataV1.Format
func FeatureDataGetDataV1.GoString
func FeatureDataGetDataV1.String
func FeatureDataGetDataV1.Clone
func FeatureDataGetRequestV1.Clone
func FeatureLocatorV1.Clone
func FeatureLocatorV1.Format
func FeatureLocatorV1.GoString
func FeatureLocatorV1.String
func FeatureTargetV1.Clone
func FeatureTargetV1.Format
func FeatureTargetV1.GoString
func FeatureTargetV1.Locator
func FeatureTargetV1.String
func FeaturesGetDataV1.Clone
func FeaturesGetDataV1.ComputeDataHash
func FeaturesGetDataV1.Format
func FeaturesGetDataV1.GoString
func FeaturesGetDataV1.String
func FeaturesGetRequestV1.Clone
func FunctionDescriptorV1.Clone
func NewErrorV1
func NewTypedValueV1
func OpaqueObservationV1.Clone
func OpaqueObservationV1.Format
func OpaqueObservationV1.GoString
func OpaqueObservationV1.String
func ProtocolMessageV1.Clone
func ProtocolMessageV1.Format
func ProtocolMessageV1.GoString
func ProtocolMessageV1.String
func ReadAuthorizationV1.Format
func ReadAuthorizationV1.GoString
func ReadAuthorizationV1.String
func ReadFailureV1.Clone
func ReadFailureV1.Format
func ReadFailureV1.GoString
func ReadFailureV1.String
func ReadObservationV1.Clone
func ReadObservationV1.ComputeDataHash
func ReadObservationV1.Format
func ReadObservationV1.GoString
func ReadObservationV1.String
func ReadTokenV1.Format
func ReadTokenV1.GoString
func ReadTokenV1.String
func TypedValueV1.Clone
func TypedValueV1.ComputeHash
func TypedValueV1.Format
func TypedValueV1.GoString
func TypedValueV1.MarshalJSON
func TypedValueV1.String
func TypedValueV1.UnmarshalJSON
func TypedValueV1.Validate
func TypedValueV1.Value
func ValidateFeatureDataGetRequestV1
func ValidateFeatureDataGetDataV1
func ValidateFeaturesGetRequestV1
func ValidateFeaturesGetDataV1
func ValidateReadAuthorizationV1
type AuthScopeV1
type ChangeabilityV1
type ConstraintSetV1
type ConstraintStatusV1
type ErrorCodeV1
type ErrorDetailsV1
type ErrorV1
type FeatureDataGetDataV1
type FeatureDataGetRequestV1
type FeatureLocatorV1
type FeatureRoleV1
type FeatureTargetV1
type FeaturesGetDataV1
type FeaturesGetRequestV1
type FullOperationsV1
type FunctionDescriptorV1
type HashV1
type ObservationSourceV1
type OpaqueObservationV1
type OperationV1
type ProtocolMessageV1
type ReadAuthorizationV1
type ReadFailureV1
type ReadObservationV1
type ReadTokenV1
type RuntimeBindingV1
type SourceLayerV1
type ToolV1
type TypedValueV1
var ErrSecretDetected
`)

var allowedMSP0625MutationRawExports = frozenExportInventory(`
const AuthScopeV1RawWrite
const MutationLabProfileContractV1
const ErrorCodeV1CASMismatch
const ErrorCodeV1Conflict
const ErrorCodeV1ConstraintFailure
const ErrorCodeV1ConstraintsUnknown
const ErrorCodeV1IdempotencyConflict
const ErrorCodeV1NoEffect
const ErrorCodeV1OutcomeUnknown
const ErrorCodeV1RollbackFailed
const ErrorCodeV1StaleReadToken
const ErrorCodeV1WriterBusy
const ModeV1Apply
const ModeV1Probe
const MutationStateV1Applied
const MutationStateV1Conflict
const MutationStateV1DispatchIntent
const MutationStateV1FailedNoContact
const MutationStateV1NoEffect
const MutationStateV1OutcomeUnknown
const MutationStateV1Prepared
const MutationStateV1ProbeActive
const MutationStateV1Rejected
const MutationStateV1ReplyObserved
const MutationStateV1RollbackDispatchIntent
const MutationStateV1RollbackIntent
const MutationStateV1RollbackReplyObserved
const MutationStateV1RollbackVerifyPending
const MutationStateV1RolledBack
const MutationStateV1VerifyPending
const ToolV1FeaturesDataSet
const ToolV1MutationsGet
const ToolV1MutationsRollback
func ValidateWriteAuthorizationV1
func ValidateFeatureDataSetRequestV1
func ValidateMutationGetRequestV1
func ValidateMutationRollbackRequestV1
func ValidateMutationLabProfileV1
func ValidateMutationV1
type ApplyVerificationV1
type AuditTransitionV1
type ConflictEvidenceV1
type ConstraintOverrideV1
type FeatureDataSetRequestV1
func FeatureDataSetRequestV1.Format
func FeatureDataSetRequestV1.GoString
func FeatureDataSetRequestV1.String
type ModeV1
type MutationGetRequestV1
type MutationLabProfileV1
func MutationLabProfileV1.Clone
func MutationLabProfileV1.Format
func MutationLabProfileV1.GoString
func MutationLabProfileV1.String
type MutationRollbackRequestV1
func MutationRollbackRequestV1.Format
func MutationRollbackRequestV1.GoString
func MutationRollbackRequestV1.String
type MutationStateV1
type MutationV1
type NoContactEvidenceV1
type NoEffectVerificationV1
type OutcomeEvidenceV1
type RejectionVerificationV1
type RollbackV1
type RollbackVerificationV1
type WriteAuthorizationV1
`)

var allowedCurrentRawExports = mergeExportInventories(
	allowedMSP035RawExports,
	allowedMSP0625RawExports,
	allowedMSP0625MutationRawExports,
)

var allowedMSP035EvidenceExports = frozenExportInventory(`
const AuthScopeReadRaw
const CaptureProvenanceRuntimeObservation
const EnvelopeContractV1
const EnvelopeContractV1Alpha1
const ObjectKindIdentity
const ObjectKindService
const ObjectKindSession
const ObjectKindTopology
const ObjectKindUnknown
const RawSnapshotScopeIdentity
const RawSnapshotScopeRoot
const RawSnapshotScopeServices
const RawSnapshotScopeSessions
const RawSnapshotScopeTopology
const RawSnapshotScopeUnknown
const ScopePairingStatus
const ScopeRuntimeStatus
const ScopeService
const ScopeServices
const ScopeSession
const ScopeSessions
const ScopeTopology
const ScopeWholeRoot
const ToolCapture
const ToolPairingStatus
const ToolRuntimeStatus
const ToolServicesGet
const ToolServicesList
const ToolSessionsGet
const ToolSessionsList
const ToolTopologyGet
func AuthScope.Format
func AuthScope.GoString
func AuthScope.MarshalJSON
func AuthScope.String
func AuthScope.Validate
func CaptureProvenanceV1.Format
func CaptureProvenanceV1.GoString
func CaptureProvenanceV1.MarshalJSON
func CaptureProvenanceV1.String
func CaptureProvenanceV1.Validate
func ContractVersion.Format
func ContractVersion.GoString
func ContractVersion.MarshalJSON
func ContractVersion.String
func ContractVersion.Validate
func DigestBytes
func Envelope.ComputeDataHash
func Envelope.Format
func Envelope.GoString
func Envelope.MarshalJSON
func Envelope.String
func Envelope.Validate
func Envelope.WithDataHash
func EnvelopeV1.ComputeDataHash
func EnvelopeV1.Format
func EnvelopeV1.GoString
func EnvelopeV1.MarshalJSON
func EnvelopeV1.String
func EnvelopeV1.Validate
func EnvelopeV1.WithDataHash
func NewEnvelope
func NewEnvelopeV1
func NewObject
func NewObjectV1
func NewReference
func NewReferenceV1
func Object.Format
func Object.GoString
func Object.MarshalJSON
func Object.String
func Object.Validate
func ObjectKind.Format
func ObjectKind.GoString
func ObjectKind.MarshalJSON
func ObjectKind.String
func ObjectKind.Validate
func ObjectV1.Format
func ObjectV1.GoString
func ObjectV1.MarshalJSON
func ObjectV1.String
func ObjectV1.Validate
func RawSnapshotScopeV1.Format
func RawSnapshotScopeV1.GoString
func RawSnapshotScopeV1.MarshalJSON
func RawSnapshotScopeV1.String
func RawSnapshotScopeV1.Validate
func Reference.Format
func Reference.GoString
func Reference.MarshalJSON
func Reference.Matches
func Reference.String
func Reference.Validate
func ReferenceV1.Format
func ReferenceV1.GoString
func ReferenceV1.MarshalJSON
func ReferenceV1.Matches
func ReferenceV1.String
func ReferenceV1.Validate
func Scope.Format
func Scope.GoString
func Scope.MarshalJSON
func Scope.String
func Scope.Validate
func ToolID.Format
func ToolID.GoString
func ToolID.MarshalJSON
func ToolID.String
func ToolID.Validate
type AuthScope
type CaptureProvenanceV1
type ContractVersion
type Envelope
type EnvelopeV1
type Object
type ObjectKind
type ObjectV1
type RawSnapshotScopeV1
type Reference
type ReferenceV1
type Scope
type ToolID
`)

var documentationExtensions = map[string]struct{}{
	".adoc":     {},
	".asciidoc": {},
	".markdown": {},
	".md":       {},
	".mdown":    {},
	".mkd":      {},
	".rst":      {},
	".txt":      {},
}

var documentationNames = map[string]struct{}{
	"api":          {},
	"architecture": {},
	"changelog":    {},
	"contributing": {},
	"design":       {},
	"governance":   {},
	"protocol":     {},
	"readme":       {},
	"roadmap":      {},
	"security":     {},
	"support":      {},
}

var (
	cgoFunctionOpening = regexp.MustCompile(`^static[ \t]+int[ \t]+[A-Za-z_][A-Za-z0-9_]*\([ \t]*int[ \t]+[A-Za-z_][A-Za-z0-9_]*[ \t]*\)[ \t]*\{$`)
	cgoIdentifier      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	cgoInclude         = regexp.MustCompile(`^#include[ \t]+<[A-Za-z0-9_]+(?:/[A-Za-z0-9_]+)*\.h>$`)
	cgoInteger         = regexp.MustCompile(`^[0-9]+$`)
)

type apiManifest struct {
	Module          string                   `json:"module"`
	Packages        []manifestPackage        `json:"packages"`
	Schema          string                   `json:"schema"`
	StableContracts []manifestStableContract `json:"stable_contracts"`
	Version         int                      `json:"version"`
}

type manifestPackage struct {
	Exports    []manifestExport `json:"exports"`
	ImportPath string           `json:"import_path"`
	Name       string           `json:"name"`
}

type manifestExport struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

func frozenExportInventory(entries string) map[manifestExport]struct{} {
	inventory := make(map[manifestExport]struct{})
	for _, line := range strings.Split(strings.TrimSpace(entries), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			panic("invalid frozen public export inventory entry")
		}
		inventory[manifestExport{Kind: fields[0], Name: fields[1]}] = struct{}{}
	}
	return inventory
}

func mergeExportInventories(inventories ...map[manifestExport]struct{}) map[manifestExport]struct{} {
	result := make(map[manifestExport]struct{})
	for _, inventory := range inventories {
		for exported := range inventory {
			result[exported] = struct{}{}
		}
	}
	return result
}

type manifestStableContract struct {
	Enums      []manifestStableEnum `json:"enums"`
	ImportPath string               `json:"import_path"`
	Types      []manifestStableType `json:"types"`
}

type manifestStableEnum struct {
	Type   string                    `json:"type"`
	Values []manifestStableEnumValue `json:"values"`
}

type manifestStableEnumValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type manifestStableType struct {
	Fields     []manifestStableField `json:"fields,omitempty"`
	Name       string                `json:"name"`
	Underlying string                `json:"underlying"`
}

type manifestStableField struct {
	JSONTag string `json:"json_tag"`
	Name    string `json:"name"`
	Type    string `json:"type"`
}

type packageInventory struct {
	exports map[manifestExport]struct{}
	files   []*ast.File
	name    string
	types   map[string]typeDeclaration
}

type typeDeclaration struct {
	imports map[string]string
	rel     string
	spec    *ast.TypeSpec
}

type constantDeclaration struct {
	name     string
	rel      string
	typeName string
	value    string
}

type sourceImporter struct {
	checked     map[string]*types.Package
	checking    map[string]bool
	fallback    types.Importer
	fset        *token.FileSet
	inventories map[string]*packageInventory
}

type chainedImporter struct {
	primary   types.Importer
	secondary types.Importer
}

type stableContractSpec struct {
	enums      []stableEnumSpec
	importPath string
	types      []manifestStableType
	root       string
}

type stableEnumSpec struct {
	exact    bool
	typeName string
	values   []manifestStableEnumValue
}

type pathOrigin string

const (
	trackedPath    pathOrigin = "tracked"
	untrackedPath  pathOrigin = "untracked"
	ignoredPath    pathOrigin = "ignored"
	filesystemPath pathOrigin = "filesystem"
)

func main() {
	flags := flag.NewFlagSet("apiboundary", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	manifestPath := flags.String("manifest", "", "write the API boundary manifest outside the repository")
	if err := flags.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	if flags.NArg() != 0 {
		fatal(fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " ")))
	}

	root, err := os.Getwd()
	if err != nil {
		fatal(err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		fatal(err)
	}

	violations, err := validateRepositoryPaths(root)
	if err != nil {
		fatal(err)
	}
	manifest := apiManifest{}
	if len(violations) == 0 {
		var goViolations []string
		manifest, goViolations, err = inspectGoBoundary(root)
		if err != nil {
			fatal(err)
		}
		violations = append(violations, goViolations...)
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		for _, violation := range violations {
			fmt.Fprintln(os.Stderr, violation)
		}
		os.Exit(1)
	}

	if *manifestPath == "" {
		return
	}
	destination, err := safeManifestDestination(root, *manifestPath)
	if err != nil {
		fatal(err)
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		fatal(fmt.Errorf("marshal API boundary artifact: %w", err))
	}
	data = append(data, '\n')
	if err := writeAtomic(destination, data); err != nil {
		fatal(fmt.Errorf("write API boundary artifact: %w", err))
	}
}

func validateRepositoryPaths(root string) ([]string, error) {
	paths := make(map[string]map[pathOrigin]struct{})
	lists := []struct {
		origin pathOrigin
		args   []string
	}{
		{origin: trackedPath, args: []string{"ls-files", "-z"}},
		{origin: untrackedPath, args: []string{"ls-files", "--others", "--exclude-standard", "-z"}},
		{origin: ignoredPath, args: []string{"ls-files", "--others", "--ignored", "--exclude-standard", "-z"}},
	}
	for _, list := range lists {
		entries, err := gitNullList(root, list.args...)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			addPathOrigin(paths, filepath.ToSlash(entry), list.origin)
		}
	}

	violations := make(map[string]struct{})
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == ".git" && entry.IsDir() {
			return filepath.SkipDir
		}
		if rel == "." {
			return nil
		}
		if _, known := paths[rel]; !known {
			addPathOrigin(paths, rel, filesystemPath)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return fmt.Errorf("inspect symlink %s: %w", rel, err)
			}
			violations[symlinkViolation(rel, target)] = struct{}{}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	stagedSymlinks, err := gitStagedSymlinks(root)
	if err != nil {
		return nil, err
	}
	for path, target := range stagedSymlinks {
		violations[symlinkViolation(path, target)] = struct{}{}
	}

	casefold := make(map[string]string)
	pathNames := make([]string, 0, len(paths))
	for path := range paths {
		pathNames = append(pathNames, path)
	}
	sort.Strings(pathNames)
	for _, path := range pathNames {
		if err := validateRelativePath(path); err != nil {
			violations[fmt.Sprintf("unsafe repository path %q: %v", path, err)] = struct{}{}
		}
		folded := strings.ToLower(path)
		if prior, ok := casefold[folded]; ok && prior != path {
			violations[fmt.Sprintf("casefold collision: %s conflicts with %s", path, prior)] = struct{}{}
		} else {
			casefold[folded] = path
		}
		for origin := range paths[path] {
			if hasFoldedPathSegment(path, "docs") {
				violations[fmt.Sprintf("%s docs path is forbidden: %s", origin, path)] = struct{}{}
			}
			if isDocumentationPath(path) && path != "README.md" && path != "AGENTS.md" {
				violations[fmt.Sprintf("%s markdown/documentation path is outside the allowlist: %s", origin, path)] = struct{}{}
			}
		}
	}

	readmePath := filepath.Join(root, "README.md")
	info, err := os.Lstat(readmePath)
	if err != nil {
		if os.IsNotExist(err) {
			violations["README.md must exist and match the exact minimal README"] = struct{}{}
		} else {
			return nil, err
		}
	} else if info.Mode().IsRegular() {
		data, err := os.ReadFile(readmePath)
		if err != nil {
			return nil, err
		}
		if string(data) != minimalREADME {
			violations["README.md must match the exact minimal README"] = struct{}{}
		}
	} else {
		violations["README.md must be a regular file with the exact minimal content"] = struct{}{}
	}

	result := make([]string, 0, len(violations))
	for violation := range violations {
		result = append(result, violation)
	}
	sort.Strings(result)
	return result, nil
}

func inspectGoBoundary(root string) (apiManifest, []string, error) {
	moduleData, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return apiManifest{}, nil, fmt.Errorf("read module identity: %w", err)
	}
	moduleFile, err := modfile.Parse("go.mod", moduleData, nil)
	if err != nil {
		return apiManifest{}, nil, fmt.Errorf("read module identity: %w", err)
	}
	if moduleFile.Module == nil || moduleFile.Module.Mod.Path == "" {
		return apiManifest{}, nil, fmt.Errorf("read module identity: go.mod has no module directive")
	}
	modulePath := moduleFile.Module.Mod.Path
	runtimeLifecycleActive := false
	for _, required := range moduleFile.Require {
		if required.Mod.Path == "github.com/Project-Helianthus/helianthus-eebus-go" {
			runtimeLifecycleActive = true
			break
		}
	}

	fset := token.NewFileSet()
	packages := make(map[string]*packageInventory)
	var violations []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch {
			case entry.Name() == ".git", entry.Name() == "vendor", strings.EqualFold(entry.Name(), "docs"):
				return filepath.SkipDir
			default:
				return nil
			}
		}
		if entry.Type()&os.ModeSymlink != 0 || filepath.Ext(path) != ".go" {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			violations = append(violations, fmt.Sprintf("%s: parse error: %v", rel, err))
			return nil
		}

		internal := hasPathSegment(rel, "internal")
		testFile := strings.HasSuffix(rel, "_test.go")
		if !testFile {
			checkProductionComments(fset, rel, file, &violations)
		}
		if !internal && !testFile {
			directory := filepath.ToSlash(filepath.Dir(rel))
			allowedName, allowed := allowedPublicPackages[directory]
			if !allowed || file.Name.Name != allowedName {
				violations = append(violations, fmt.Sprintf("%s: unexpected public package %s", rel, file.Name.Name))
			} else {
				importPath := modulePath
				if directory != "." {
					importPath += "/" + directory
				}
				inventory := packages[importPath]
				if inventory == nil {
					inventory = &packageInventory{
						exports: make(map[manifestExport]struct{}),
						name:    file.Name.Name,
						types:   make(map[string]typeDeclaration),
					}
					packages[importPath] = inventory
				}
				inventory.files = append(inventory.files, file)
				collectExports(file, inventory.exports)
				collectDeclarations(rel, file, inventory)
			}
		}

		internalAliases := internalImportAliases(file, modulePath)
		for _, imp := range file.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			if !internal && isProtocolImplementationImport(importPath) {
				violations = append(violations, at(fset, imp.Pos(), rel, "direct protocol implementation imports are allowed only under internal/"))
			}
			rootRuntimeImplementation := !internal && !testFile && filepath.ToSlash(filepath.Dir(rel)) == "." && file.Name.Name == "eebusruntime"
			facadeImplementationImport := rootRuntimeImplementation && importPath == modulePath+"/internal/eebusfacade" &&
				(imp.Name == nil || imp.Name.Name != ".")
			if !internal && !testFile && strings.HasPrefix(importPath, modulePath+"/internal/") && !facadeImplementationImport {
				violations = append(violations, at(fset, imp.Pos(), rel, "public API packages must not import internal implementation types"))
			}
			for _, forbidden := range forbiddenImports {
				if strings.Contains(importPath, forbidden) {
					violations = append(violations, at(fset, imp.Pos(), rel, "gateway or consumer imports are not allowed in this repo"))
				}
			}
		}
		if internal {
			return nil
		}
		if !testFile {
			checkCrossRuntimeStrings(fset, rel, file, &violations)
		}
		for _, decl := range file.Decls {
			switch declaration := decl.(type) {
			case *ast.FuncDecl:
				if declaration.Name != nil {
					checkExportedName(fset, rel, declaration.Name, &violations)
					if declaration.Name.IsExported() {
						checkExportedTypeSurface(fset, rel, declaration.Type, &violations)
						checkInternalTypeEscape(fset, rel, declaration.Type, internalAliases, &violations)
					}
				}
			case *ast.GenDecl:
				for _, spec := range declaration.Specs {
					switch typed := spec.(type) {
					case *ast.TypeSpec:
						checkExportedName(fset, rel, typed.Name, &violations)
						if typed.Name.IsExported() {
							checkExportedTypeSurface(fset, rel, typed.Type, &violations)
							checkInternalTypeEscape(fset, rel, typed.Type, internalAliases, &violations)
						}
					case *ast.ValueSpec:
						exported := false
						for _, name := range typed.Names {
							checkExportedName(fset, rel, name, &violations)
							exported = exported || name.IsExported()
						}
						if exported {
							checkInternalTypeEscape(fset, rel, typed, internalAliases, &violations)
						}
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return apiManifest{}, nil, err
	}

	stableContracts, stableViolations := inspectStableContracts(root, modulePath, fset, packages)
	violations = append(violations, stableViolations...)
	violations = append(violations, inspectRuntimeExports(modulePath, packages, runtimeLifecycleActive)...)
	violations = append(violations, inspectMSP035DependencyExports(modulePath, packages)...)
	manifest := apiManifest{
		Module:          modulePath,
		Packages:        make([]manifestPackage, 0, len(packages)),
		Schema:          "helianthus.api-boundary-manifest",
		StableContracts: stableContracts,
		Version:         1,
	}
	for importPath, inventory := range packages {
		pkg := manifestPackage{
			Exports:    make([]manifestExport, 0, len(inventory.exports)),
			ImportPath: importPath,
			Name:       inventory.name,
		}
		for exported := range inventory.exports {
			pkg.Exports = append(pkg.Exports, exported)
		}
		sort.Slice(pkg.Exports, func(i, j int) bool {
			if pkg.Exports[i].Kind != pkg.Exports[j].Kind {
				return pkg.Exports[i].Kind < pkg.Exports[j].Kind
			}
			return pkg.Exports[i].Name < pkg.Exports[j].Name
		})
		manifest.Packages = append(manifest.Packages, pkg)
	}
	sort.Slice(manifest.Packages, func(i, j int) bool {
		return manifest.Packages[i].ImportPath < manifest.Packages[j].ImportPath
	})
	return manifest, violations, nil
}

func isProtocolImplementationImport(path string) bool {
	for _, prefix := range []string{
		"github.com/Project-Helianthus/helianthus-eebus-go",
		"github.com/Project-Helianthus/helianthus-ship-go",
		"github.com/Project-Helianthus/helianthus-spine-go",
		"github.com/enbility/eebus-go",
		"github.com/enbility/ship-go",
		"github.com/enbility/spine-go",
	} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func inspectRuntimeExports(modulePath string, packages map[string]*packageInventory, lifecycleActive bool) []string {
	inventory := packages[modulePath]
	if inventory == nil {
		if modulePath == canonicalModulePath {
			return []string{"root eebusruntime package is missing"}
		}
		return nil
	}
	if modulePath != canonicalModulePath {
		if _, active := inventory.types["SnapshotV1"]; !active {
			return nil
		}
	}
	snapshotOnlyContract := modulePath == canonicalModulePath && !lifecycleActive
	var violations []string
	for actual := range inventory.exports {
		if _, allowed := allowedRuntimeExports[actual]; !allowed {
			violations = append(violations, fmt.Sprintf("root eebusruntime export is outside the MSP-036 closed inventory: %s %s", actual.Kind, actual.Name))
		}
	}
	for expected := range allowedRuntimeExports {
		if snapshotOnlyContract {
			if _, lifecycle := msp055RuntimeExports[expected]; lifecycle {
				continue
			}
		}
		if _, present := inventory.exports[expected]; !present {
			violations = append(violations, fmt.Sprintf("root eebusruntime export required by MSP-036 is missing: %s %s", expected.Kind, expected.Name))
		}
	}
	return violations
}

func inspectMSP035DependencyExports(modulePath string, packages map[string]*packageInventory) []string {
	if modulePath != canonicalModulePath {
		return nil
	}
	dependencies := []struct {
		name    string
		path    string
		exports map[manifestExport]struct{}
	}{
		{name: "eebusraw", path: modulePath + "/eebusraw", exports: allowedCurrentRawExports},
		{name: "eebusevidence", path: modulePath + "/eebusevidence", exports: allowedMSP035EvidenceExports},
	}
	var violations []string
	for _, dependency := range dependencies {
		inventory := packages[dependency.path]
		if inventory == nil {
			violations = append(violations, fmt.Sprintf("MSP-035 dependency %s public package is missing", dependency.name))
			continue
		}
		for actual := range inventory.exports {
			if _, allowed := dependency.exports[actual]; !allowed {
				violations = append(violations, fmt.Sprintf("MSP-035 dependency %s public export is outside the frozen inventory: %s %s", dependency.name, actual.Kind, actual.Name))
			}
		}
		for expected := range dependency.exports {
			if _, present := inventory.exports[expected]; !present {
				violations = append(violations, fmt.Sprintf("MSP-035 dependency %s frozen public export is missing: %s %s", dependency.name, expected.Kind, expected.Name))
			}
		}
	}
	sort.Strings(violations)
	return violations
}

func checkProductionComments(fset *token.FileSet, rel string, file *ast.File, violations *[]string) {
	compilerComments := allowedCompilerCommentGroups(fset, file)
	for _, group := range file.Comments {
		if _, allowed := compilerComments[group]; allowed {
			continue
		}
		if group != file.Doc {
			*violations = append(*violations, at(fset, group.Pos(), rel, "production Go comment is not allowed"))
			continue
		}
		prefix := "// Package " + file.Name.Name + " "
		valid := len(group.List) == 1 && strings.HasPrefix(group.List[0].Text, prefix) && len(group.List[0].Text) <= 120
		if valid {
			valid = strings.TrimSpace(strings.TrimPrefix(group.List[0].Text, prefix)) != ""
		}
		if !valid {
			*violations = append(*violations, at(fset, group.Pos(), rel, "package comment must be one concise line starting with Package "+file.Name.Name))
		}
	}
}

func allowedCompilerCommentGroups(fset *token.FileSet, file *ast.File) map[*ast.CommentGroup]struct{} {
	allowed := make(map[*ast.CommentGroup]struct{})
	var buildConstraints []*ast.CommentGroup
	for _, group := range file.Comments {
		if isCanonicalBuildConstraintGroup(fset, file, group) {
			buildConstraints = append(buildConstraints, group)
		}
	}
	if len(buildConstraints) == 1 {
		allowed[buildConstraints[0]] = struct{}{}
	}
	for _, declaration := range file.Decls {
		imports, ok := declaration.(*ast.GenDecl)
		if !ok || imports.Tok != token.IMPORT {
			continue
		}
		for _, rawSpec := range imports.Specs {
			spec, ok := rawSpec.(*ast.ImportSpec)
			if !ok || spec.Name != nil || spec.Path.Value != `"C"` {
				continue
			}
			preamble := spec.Doc
			if preamble == nil && len(imports.Specs) == 1 {
				preamble = imports.Doc
			}
			if isMachineOnlyCGOPreamble(preamble) {
				allowed[preamble] = struct{}{}
			}
		}
	}
	return allowed
}

func isMachineOnlyCGOPreamble(group *ast.CommentGroup) bool {
	if group == nil {
		return false
	}
	preamble := strings.TrimSpace(group.Text())
	if preamble == "" || strings.ContainsAny(preamble, "\"'`") {
		return false
	}
	for _, marker := range []string{"//", "/*", "*/"} {
		if strings.Contains(preamble, marker) {
			return false
		}
	}
	foundMachineLine := false
	for _, rawLine := range strings.Split(preamble, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		foundMachineLine = true
		if strings.HasPrefix(line, "#") {
			if !cgoInclude.MatchString(line) {
				return false
			}
			continue
		}
		if !isRestrictedCGOLine(line) {
			return false
		}
	}
	return foundMachineLine
}

func isRestrictedCGOLine(line string) bool {
	if !hasRestrictedCAlphabet(line) {
		return false
	}
	if line == "}" || cgoFunctionOpening.MatchString(line) {
		return true
	}
	if strings.HasPrefix(line, "if (") && strings.HasSuffix(line, ") {") {
		condition := strings.TrimSuffix(strings.TrimPrefix(line, "if ("), ") {")
		return isCCondition(condition)
	}
	if !strings.HasSuffix(line, ";") {
		return false
	}
	return isCStatement(strings.TrimSpace(strings.TrimSuffix(line, ";")))
}

func hasRestrictedCAlphabet(line string) bool {
	for _, character := range line {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '_' || character == ' ' || character == '\t' {
			continue
		}
		switch character {
		case '(', ')', '{', '}', '[', ']', ',', ';', '=', '<', '>', '!', '+', '-', '*', '/', '%', '&', '|', '^', '~':
			continue
		default:
			return false
		}
	}
	return true
}

func isCStatement(statement string) bool {
	if strings.HasPrefix(statement, "if (") {
		parts := strings.SplitN(strings.TrimPrefix(statement, "if ("), ") return ", 2)
		return len(parts) == 2 && isCCondition(parts[0]) && isCValue(parts[1])
	}
	if strings.HasPrefix(statement, "return ") {
		return isCValue(strings.TrimPrefix(statement, "return "))
	}
	if strings.Count(statement, "=") == 1 {
		parts := strings.SplitN(statement, "=", 2)
		left := strings.Fields(parts[0])
		validLeft := len(left) == 1 && cgoIdentifier.MatchString(left[0]) ||
			len(left) == 2 && cgoIdentifier.MatchString(left[0]) && cgoIdentifier.MatchString(left[1])
		return validLeft && isCValue(parts[1])
	}
	fields := strings.Fields(statement)
	if len(fields) == 2 && cgoIdentifier.MatchString(fields[0]) && cgoIdentifier.MatchString(fields[1]) {
		return true
	}
	return isCCall(statement)
}

func isCCondition(condition string) bool {
	for _, operator := range []string{"==", "!=", "<=", ">=", "<", ">"} {
		parts := strings.Split(condition, operator)
		if len(parts) == 2 {
			return isCOperand(parts[0]) && isCOperand(parts[1])
		}
	}
	return false
}

func isCValue(value string) bool {
	return isCOperand(value) || isCCall(value)
}

func isCCall(expression string) bool {
	expression = strings.TrimSpace(expression)
	opening := strings.IndexByte(expression, '(')
	if opening <= 0 || !strings.HasSuffix(expression, ")") || !cgoIdentifier.MatchString(expression[:opening]) {
		return false
	}
	arguments := strings.TrimSpace(expression[opening+1 : len(expression)-1])
	if arguments == "" {
		return true
	}
	for _, argument := range strings.Split(arguments, ",") {
		if !isCOperand(argument) {
			return false
		}
	}
	return true
}

func isCOperand(operand string) bool {
	operand = strings.TrimSpace(operand)
	if operand == "" {
		return false
	}
	if strings.ContainsAny(operand[:1], "+-*&") {
		operand = operand[1:]
	}
	return cgoIdentifier.MatchString(operand) || cgoInteger.MatchString(operand)
}

func isCanonicalBuildConstraintGroup(fset *token.FileSet, file *ast.File, group *ast.CommentGroup) bool {
	if len(group.List) != 1 || group.End() >= file.Package {
		return false
	}
	comment := group.List[0].Text
	if !strings.HasPrefix(comment, "//go:build ") {
		return false
	}
	expression, err := constraint.Parse(comment)
	if err != nil || comment != "//go:build "+expression.String() {
		return false
	}
	commentLine := fset.PositionFor(group.End(), false).Line
	packageLine := fset.PositionFor(file.Package, false).Line
	return commentLine > 0 && packageLine >= commentLine+2
}

func collectExports(file *ast.File, exports map[manifestExport]struct{}) {
	for _, decl := range file.Decls {
		switch declaration := decl.(type) {
		case *ast.FuncDecl:
			if declaration.Name == nil || !declaration.Name.IsExported() {
				continue
			}
			name := declaration.Name.Name
			if declaration.Recv != nil && len(declaration.Recv.List) > 0 {
				receiver := receiverName(declaration.Recv.List[0].Type)
				if receiver == "" || !ast.IsExported(receiver) {
					continue
				}
				name = receiver + "." + name
			}
			exports[manifestExport{Kind: "func", Name: name}] = struct{}{}
		case *ast.GenDecl:
			kind := ""
			switch declaration.Tok {
			case token.CONST:
				kind = "const"
			case token.TYPE:
				kind = "type"
			case token.VAR:
				kind = "var"
			}
			if kind == "" {
				continue
			}
			for _, spec := range declaration.Specs {
				switch typed := spec.(type) {
				case *ast.TypeSpec:
					if typed.Name.IsExported() {
						exports[manifestExport{Kind: kind, Name: typed.Name.Name}] = struct{}{}
					}
				case *ast.ValueSpec:
					for _, name := range typed.Names {
						if name.IsExported() {
							exports[manifestExport{Kind: kind, Name: name.Name}] = struct{}{}
						}
					}
				}
			}
		}
	}
}

func collectDeclarations(rel string, file *ast.File, inventory *packageInventory) {
	imports := make(map[string]string)
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		name := filepath.Base(importPath)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		imports[name] = importPath
	}
	for _, declaration := range file.Decls {
		gen, ok := declaration.(*ast.GenDecl)
		if !ok {
			continue
		}
		switch gen.Tok {
		case token.TYPE:
			for _, rawSpec := range gen.Specs {
				spec, ok := rawSpec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				inventory.types[spec.Name.Name] = typeDeclaration{imports: imports, rel: rel, spec: spec}
			}
		}
	}
}

func inspectStableContracts(root, modulePath string, fset *token.FileSet, packages map[string]*packageInventory) ([]manifestStableContract, []string) {
	specs := stableContractSpecs(modulePath)
	active := modulePath == canonicalModulePath
	if !active {
		for _, spec := range specs {
			if inventory := packages[spec.importPath]; inventory != nil {
				if _, ok := inventory.types[spec.root]; ok {
					active = true
					break
				}
			}
		}
	}
	if !active {
		return []manifestStableContract{}, nil
	}

	checkedPackages, typeViolations := typeCheckStablePackages(root, fset, packages, specs)
	contracts := make([]manifestStableContract, 0, len(specs))
	violations := append([]string(nil), typeViolations...)
	allExpectedTypes := make(map[string]map[string]struct{})
	for _, spec := range specs {
		expected := allExpectedTypes[spec.importPath]
		if expected == nil {
			expected = make(map[string]struct{})
			allExpectedTypes[spec.importPath] = expected
		}
		for _, stableType := range spec.types {
			expected[stableType.Name] = struct{}{}
		}
	}
	allExpectedTypes[modulePath]["RawFeatureRuntimeV1"] = struct{}{}
	allExpectedTypes[modulePath]["RawMutationOutcomeV1"] = struct{}{}
	allExpectedTypes[modulePath]["RawMutationRuntimeV1"] = struct{}{}
	for _, spec := range specs {
		if modulePath != canonicalModulePath {
			inventory := packages[spec.importPath]
			if inventory == nil {
				continue
			}
			if _, ok := inventory.types[spec.root]; !ok {
				continue
			}
		}
		contract := manifestStableContract{
			Enums:      make([]manifestStableEnum, 0, len(spec.enums)),
			ImportPath: spec.importPath,
			Types:      append([]manifestStableType(nil), spec.types...),
		}
		inventory := packages[spec.importPath]
		if inventory == nil {
			violations = append(violations, "stable contract package is missing: "+spec.importPath)
			contracts = append(contracts, contract)
			continue
		}
		constants := typedConstants(root, fset, checkedPackages[spec.importPath])

		expectedTypes := make(map[string]manifestStableType, len(spec.types))
		for _, expected := range spec.types {
			expectedTypes[expected.Name] = expected
			declaration, ok := inventory.types[expected.Name]
			if !ok {
				violations = append(violations, fmt.Sprintf("%s: stable contract type %s is missing", spec.importPath, expected.Name))
				continue
			}
			actual, err := inspectStableType(declaration)
			if err != nil {
				violations = append(violations, fmt.Sprintf("%s: stable contract type %s: %v", declaration.rel, expected.Name, err))
				continue
			}
			if !stableContractTypeMatches(actual, expected) {
				violations = append(violations, fmt.Sprintf("%s: stable contract type %s does not match exact field/type/tag manifest: got %s want %s", declaration.rel, expected.Name, stableTypeText(actual), stableTypeText(expected)))
			}
		}
		for name := range inventory.types {
			if ast.IsExported(name) && strings.HasSuffix(name, "V1") {
				if _, ok := allExpectedTypes[spec.importPath][name]; !ok {
					violations = append(violations, fmt.Sprintf("%s: unexpected stable contract type %s", spec.importPath, name))
				}
			}
		}

		for _, enumSpec := range spec.enums {
			enum := manifestStableEnum{Type: enumSpec.typeName, Values: append([]manifestStableEnumValue(nil), enumSpec.values...)}
			contract.Enums = append(contract.Enums, enum)
			expectedNames := make(map[string]manifestStableEnumValue, len(enumSpec.values))
			for _, expected := range enumSpec.values {
				expectedNames[expected.Name] = expected
				actual, ok := constants[expected.Name]
				if !ok {
					violations = append(violations, fmt.Sprintf("%s: stable enum value %s.%s is missing", spec.importPath, enumSpec.typeName, expected.Name))
					continue
				}
				if actual.typeName != enumSpec.typeName || actual.value != expected.Value {
					violations = append(violations, fmt.Sprintf("%s: stable enum value %s.%s does not match exact manifest", actual.rel, enumSpec.typeName, expected.Name))
				}
			}
			if enumSpec.exact {
				for name, actual := range constants {
					if !ast.IsExported(name) || actual.typeName != enumSpec.typeName {
						continue
					}
					if _, ok := expectedNames[name]; !ok {
						violations = append(violations, fmt.Sprintf("%s: stable enum %s has unexpected value %s", actual.rel, enumSpec.typeName, name))
					}
				}
			}
		}
		sort.Slice(contract.Types, func(i, j int) bool { return contract.Types[i].Name < contract.Types[j].Name })
		sort.Slice(contract.Enums, func(i, j int) bool { return contract.Enums[i].Type < contract.Enums[j].Type })
		for i := range contract.Enums {
			sort.Slice(contract.Enums[i].Values, func(left, right int) bool {
				return contract.Enums[i].Values[left].Name < contract.Enums[i].Values[right].Name
			})
		}
		contracts = append(contracts, contract)
	}
	sort.Slice(contracts, func(i, j int) bool { return contracts[i].ImportPath < contracts[j].ImportPath })
	return contracts, violations
}

func typeCheckStablePackages(root string, fset *token.FileSet, inventories map[string]*packageInventory, specs []stableContractSpec) (map[string]*types.Package, []string) {
	fallback := types.Importer(importer.Default())
	if len(specs) != 0 {
		fallback = &chainedImporter{
			primary:   fallback,
			secondary: moduleExportImporter(fset, root),
		}
	}
	loader := &sourceImporter{
		checked:     make(map[string]*types.Package),
		checking:    make(map[string]bool),
		fallback:    fallback,
		fset:        fset,
		inventories: inventories,
	}
	var violations []string
	for _, spec := range specs {
		if inventories[spec.importPath] == nil {
			continue
		}
		if _, err := loader.Import(spec.importPath); err != nil {
			violations = append(violations, fmt.Sprintf("%s: stable contract package does not type-check: %v", spec.importPath, err))
		}
	}
	return loader.checked, violations
}

func (i *chainedImporter) Import(path string) (*types.Package, error) {
	checked, primaryErr := i.primary.Import(path)
	if primaryErr == nil {
		return checked, nil
	}
	checked, secondaryErr := i.secondary.Import(path)
	if secondaryErr == nil {
		return checked, nil
	}
	return nil, errors.Join(primaryErr, secondaryErr)
}

func moduleExportImporter(fset *token.FileSet, root string) types.Importer {
	exportFiles := make(map[string]string)
	lookup := func(path string) (io.ReadCloser, error) {
		exportFile := exportFiles[path]
		if exportFile == "" {
			command := exec.Command("go", "list", "-export", "-f={{.Export}}", path)
			command.Dir = root
			command.Env = append(os.Environ(), "GOWORK=off")
			output, err := command.Output()
			if err != nil {
				return nil, fmt.Errorf("resolve module export for %s: %w", path, err)
			}
			exportFile = strings.TrimSpace(string(output))
			if exportFile == "" {
				return nil, fmt.Errorf("resolve module export for %s: empty export path", path)
			}
			exportFiles[path] = exportFile
		}
		return os.Open(exportFile)
	}
	return importer.ForCompiler(fset, "gc", lookup)
}

func (i *sourceImporter) Import(path string) (*types.Package, error) {
	if checked := i.checked[path]; checked != nil {
		return checked, nil
	}
	inventory := i.inventories[path]
	if inventory == nil {
		return i.fallback.Import(path)
	}
	if i.checking[path] {
		return nil, fmt.Errorf("import cycle involving %s", path)
	}
	i.checking[path] = true
	defer delete(i.checking, path)
	config := types.Config{Importer: i}
	checked, err := config.Check(path, i.fset, stableTypeCheckFiles(path, inventory.files), nil)
	if err != nil {
		return nil, err
	}
	i.checked[path] = checked
	return checked, nil
}

func stableTypeCheckFiles(packagePath string, files []*ast.File) []*ast.File {
	result := make([]*ast.File, 0, len(files))
	for _, file := range files {
		implementation := false
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err == nil && strings.HasPrefix(importPath, packagePath+"/internal/") {
				implementation = true
				break
			}
		}
		if !implementation {
			result = append(result, file)
		}
	}
	return result
}

func typedConstants(root string, fset *token.FileSet, pkg *types.Package) map[string]constantDeclaration {
	constants := make(map[string]constantDeclaration)
	if pkg == nil {
		return constants
	}
	for _, name := range pkg.Scope().Names() {
		object, ok := pkg.Scope().Lookup(name).(*types.Const)
		if !ok {
			continue
		}
		typeName := ""
		if named, ok := object.Type().(*types.Named); ok && named.Obj().Pkg() == pkg {
			typeName = named.Obj().Name()
		}
		value := object.Val().ExactString()
		if object.Val().Kind() == constant.String {
			value = constant.StringVal(object.Val())
		}
		constants[name] = constantDeclaration{
			name:     name,
			rel:      relativePosition(root, fset, object.Pos()),
			typeName: typeName,
			value:    value,
		}
	}
	return constants
}

func relativePosition(root string, fset *token.FileSet, pos token.Pos) string {
	filename := fset.Position(pos).Filename
	rel, err := filepath.Rel(root, filename)
	if err != nil {
		return filepath.ToSlash(filename)
	}
	return filepath.ToSlash(rel)
}

func inspectStableType(declaration typeDeclaration) (manifestStableType, error) {
	if declaration.spec.Assign.IsValid() {
		return manifestStableType{}, errors.New("type aliases are not allowed in the stable contract")
	}
	result := manifestStableType{Name: declaration.spec.Name.Name}
	if structure, ok := declaration.spec.Type.(*ast.StructType); ok {
		result.Underlying = "struct"
		result.Fields = make([]manifestStableField, 0, len(structure.Fields.List))
		for _, field := range structure.Fields.List {
			if len(field.Names) != 1 {
				return manifestStableType{}, errors.New("stable struct fields must have exactly one explicit name")
			}
			if !ast.IsExported(field.Names[0].Name) {
				continue
			}
			typeName, err := canonicalType(field.Type, declaration.imports)
			if err != nil {
				return manifestStableType{}, err
			}
			jsonTag := ""
			if field.Tag != nil {
				jsonTag, err = strconv.Unquote(field.Tag.Value)
				if err != nil {
					return manifestStableType{}, fmt.Errorf("invalid field tag on %s: %w", field.Names[0].Name, err)
				}
			}
			result.Fields = append(result.Fields, manifestStableField{JSONTag: jsonTag, Name: field.Names[0].Name, Type: typeName})
		}
		return result, nil
	}
	underlying, err := canonicalType(declaration.spec.Type, declaration.imports)
	if err != nil {
		return manifestStableType{}, err
	}
	result.Underlying = underlying
	return result, nil
}

func canonicalType(expr ast.Expr, imports map[string]string) (string, error) {
	switch typed := expr.(type) {
	case *ast.Ident:
		return typed.Name, nil
	case *ast.SelectorExpr:
		qualifier, ok := typed.X.(*ast.Ident)
		if !ok {
			return "", errors.New("unsupported qualified stable field type")
		}
		importPath, ok := imports[qualifier.Name]
		if !ok {
			return "", fmt.Errorf("unresolved stable field type qualifier %s", qualifier.Name)
		}
		return importPath + "." + typed.Sel.Name, nil
	case *ast.ArrayType:
		if typed.Len != nil {
			return "", errors.New("fixed arrays are not allowed in the stable contract")
		}
		element, err := canonicalType(typed.Elt, imports)
		if err != nil {
			return "", err
		}
		return "[]" + element, nil
	case *ast.StarExpr:
		element, err := canonicalType(typed.X, imports)
		if err != nil {
			return "", err
		}
		return "*" + element, nil
	case *ast.MapType:
		key, err := canonicalType(typed.Key, imports)
		if err != nil {
			return "", err
		}
		element, err := canonicalType(typed.Value, imports)
		if err != nil {
			return "", err
		}
		return "map[" + key + "]" + element, nil
	default:
		return "", fmt.Errorf("unsupported stable field type %T", expr)
	}
}

func stableTypeText(value manifestStableType) string {
	data, err := json.Marshal(value)
	if err != nil {
		return value.Name
	}
	return string(data)
}

func stableContractTypeMatches(actual, expected manifestStableType) bool {
	return reflect.DeepEqual(actual, expected)
}

func stableContractSpecs(modulePath string) []stableContractSpec {
	rawPath := modulePath + "/eebusraw"
	evidencePath := modulePath + "/eebusevidence"
	field := func(name, typeName, jsonTag string) manifestStableField {
		return manifestStableField{Name: name, Type: typeName, JSONTag: jsonTag}
	}
	stableType := func(name, underlying string, fields ...manifestStableField) manifestStableType {
		if underlying == "struct" && fields == nil {
			fields = []manifestStableField{}
		}
		return manifestStableType{Name: name, Underlying: underlying, Fields: fields}
	}
	enumValue := func(name, value string) manifestStableEnumValue {
		return manifestStableEnumValue{Name: name, Value: value}
	}
	return []stableContractSpec{
		{
			importPath: modulePath,
			root:       "SnapshotV1",
			types: []manifestStableType{
				stableType("ObservedRuntimeStateV1", "string"),
				stableType("DegradationReasonV1", "string"),
				stableType("ServiceKindV1", "string"),
				stableType("ObservedSessionStateV1", "string"),
				stableType("FeatureRoleV1", "string"),
				stableType("SnapshotV1", "struct",
					field("Meta", "SnapshotMetaV1", `json:"meta"`),
					field("Status", "RuntimeObservationV1", `json:"status"`),
					field("Pairing", "[]PairingObservationV1", `json:"pairing"`),
					field("Services", "[]ServiceV1", `json:"services"`),
					field("Sessions", "[]SessionV1", `json:"sessions"`),
					field("Devices", "[]DeviceV1", `json:"devices"`),
					field("Entities", "[]EntityV1", `json:"entities"`),
					field("Features", "[]FeatureV1", `json:"features"`),
					field("UseCases", "[]UseCaseV1", `json:"usecases"`),
					field("Opaque", "[]OpaqueObservationV1", `json:"opaque"`),
				),
				stableType("SnapshotMetaV1", "struct",
					field("Contract", "string", `json:"contract"`),
					field("Runtime", rawPath+".RedactedID", `json:"runtime"`),
					field("LocalSKI", "string", `json:"local_ski"`),
					field("MaskTier", rawPath+".MaskTier", `json:"mask_tier"`),
					field("CapturedAt", "time.Time", `json:"captured_at"`),
					field("DataTimestamp", "time.Time", `json:"data_timestamp"`),
					field("DataHash", "string", `json:"data_hash,omitempty"`),
				),
				stableType("RuntimeObservationV1", "struct",
					field("State", "ObservedRuntimeStateV1", `json:"state"`),
					field("Degradation", "*DegradationV1", `json:"degradation,omitempty"`),
				),
				stableType("DegradationV1", "struct",
					field("Reason", "DegradationReasonV1", `json:"reason"`),
					field("Since", "time.Time", `json:"since"`),
				),
				stableType("PairingObservationV1", "struct",
					field("RemoteSKI", "string", `json:"remote_ski"`),
					field("State", rawPath+".PairingState", `json:"state"`),
					field("Since", "time.Time", `json:"since"`),
					field("Opaque", "*[]OpaqueObservationV1", `json:"opaque,omitempty"`),
				),
				stableType("ServiceV1", "struct",
					field("SKI", "string", `json:"ski"`),
					field("SHIPID", "*string", `json:"ship_id,omitempty"`),
					field("Kind", "ServiceKindV1", `json:"kind"`),
					field("Visible", "bool", `json:"visible"`),
					field("Paired", "bool", `json:"paired"`),
					field("Name", "*string", `json:"name,omitempty"`),
					field("Identifier", "*string", `json:"identifier,omitempty"`),
					field("Brand", "*string", `json:"brand,omitempty"`),
					field("Type", "*string", `json:"type,omitempty"`),
					field("Model", "*string", `json:"model,omitempty"`),
					field("SecondaryDigest", "*string", `json:"secondary_digest,omitempty"`),
					field("Opaque", "*[]OpaqueObservationV1", `json:"opaque,omitempty"`),
				),
				stableType("SessionV1", "struct",
					field("ID", "string", `json:"id"`),
					field("RemoteSKI", "string", `json:"remote_ski"`),
					field("State", "ObservedSessionStateV1", `json:"state"`),
					field("Since", "time.Time", `json:"since"`),
					field("Opaque", "*[]OpaqueObservationV1", `json:"opaque,omitempty"`),
				),
				stableType("DeviceV1", "struct",
					field("SKI", "string", `json:"ski"`),
					field("SHIPID", "*string", `json:"ship_id,omitempty"`),
					field("Address", "string", `json:"address"`),
					field("Type", "string", `json:"type"`),
					field("Description", "*string", `json:"description,omitempty"`),
					field("Metadata", "*MetadataV1", `json:"metadata,omitempty"`),
					field("SecondaryDigest", "*string", `json:"secondary_digest,omitempty"`),
					field("Opaque", "*[]OpaqueObservationV1", `json:"opaque,omitempty"`),
				),
				stableType("EntityV1", "struct",
					field("DeviceAddress", "string", `json:"device_address"`),
					field("EntityAddress", "string", `json:"entity_address"`),
					field("Type", "string", `json:"type"`),
					field("Description", "*string", `json:"description,omitempty"`),
					field("SecondaryDigest", "*string", `json:"secondary_digest,omitempty"`),
					field("Opaque", "*[]OpaqueObservationV1", `json:"opaque,omitempty"`),
				),
				stableType("FeatureV1", "struct",
					field("DeviceAddress", "string", `json:"device_address"`),
					field("EntityAddress", "string", `json:"entity_address"`),
					field("FeatureAddress", "string", `json:"feature_address"`),
					field("Type", "string", `json:"type"`),
					field("Role", "string", `json:"role"`),
					field("Description", "*string", `json:"description,omitempty"`),
					field("SecondaryDigest", "*string", `json:"secondary_digest,omitempty"`),
					field("Opaque", "*[]OpaqueObservationV1", `json:"opaque,omitempty"`),
				),
				stableType("UseCaseV1", "struct",
					field("ContextAddress", "string", `json:"context_address"`),
					field("Name", "string", `json:"name"`),
					field("Actor", "string", `json:"actor"`),
					field("ResolvedRole", "*string", `json:"resolved_role,omitempty"`),
					field("Scenarios", "*[]string", `json:"scenarios,omitempty"`),
					field("Version", "*string", `json:"version,omitempty"`),
					field("Availability", "*bool", `json:"availability,omitempty"`),
					field("DocumentSubrevision", "*string", `json:"document_subrevision,omitempty"`),
					field("SecondaryDigest", "*string", `json:"secondary_digest,omitempty"`),
					field("Opaque", "*[]OpaqueObservationV1", `json:"opaque,omitempty"`),
				),
				stableType("OpaqueObservationV1", "struct",
					field("Path", "string", `json:"path"`),
					field("Source", "string", `json:"source"`),
					field("Value", "OpaqueValueV1", `json:"value"`),
				),
				stableType("OpaqueValueV1", "struct",
					field("Scalar", "*OpaqueScalarV1", `json:"scalar,omitempty"`),
					field("Array", "*[]OpaqueValueV1", `json:"array,omitempty"`),
					field("Object", "*map[string]OpaqueValueV1", `json:"object,omitempty"`),
				),
				stableType("OpaqueScalarV1", "struct",
					field("Null", "*bool", `json:"null,omitempty"`),
					field("Boolean", "*bool", `json:"boolean,omitempty"`),
					field("Integer", "*int64", `json:"integer,omitempty"`),
					field("String", "*string", `json:"string,omitempty"`),
				),
				stableType("MetadataV1", "struct",
					field("Values", "map[string]MetadataValueV1", `json:"values"`),
				),
				stableType("MetadataValueV1", "struct",
					field("Null", "*bool", `json:"null,omitempty"`),
					field("Boolean", "*bool", `json:"boolean,omitempty"`),
					field("Integer", "*int64", `json:"integer,omitempty"`),
					field("String", "*string", `json:"string,omitempty"`),
				),
				stableType("RedactedSnapshotV1", "struct",
					field("Meta", "RedactedSnapshotMetaV1", `json:"meta"`),
					field("Status", "RuntimeObservationV1", `json:"status"`),
					field("Pairing", "[]"+rawPath+".PairingState", `json:"pairing"`),
					field("Services", "[]RedactedServiceV1", `json:"services"`),
					field("Sessions", "[]RedactedSessionV1", `json:"sessions"`),
					field("Devices", "[]RedactedDeviceV1", `json:"devices"`),
					field("Entities", "[]RedactedEntityV1", `json:"entities"`),
					field("Features", "[]RedactedFeatureV1", `json:"features"`),
					field("UseCases", "[]RedactedUseCaseV1", `json:"usecases"`),
				),
				stableType("RedactedSnapshotMetaV1", "struct",
					field("Contract", "string", `json:"contract"`),
					field("Runtime", rawPath+".RedactedID", `json:"runtime"`),
					field("LocalSKI", rawPath+".RedactedID", `json:"local_ski"`),
					field("MaskTier", rawPath+".MaskTier", `json:"mask_tier"`),
					field("CapturedAt", "time.Time", `json:"captured_at"`),
					field("DataTimestamp", "time.Time", `json:"data_timestamp"`),
					field("DataHash", "string", `json:"data_hash,omitempty"`),
				),
				stableType("RedactedServiceV1", "struct",
					field("ID", rawPath+".RedactedID", `json:"id"`),
					field("Kind", "ServiceKindV1", `json:"kind"`),
					field("Visible", "bool", `json:"visible"`),
					field("Paired", "bool", `json:"paired"`),
				),
				stableType("RedactedSessionV1", "struct",
					field("ID", rawPath+".RedactedID", `json:"id"`),
					field("Remote", rawPath+".RedactedID", `json:"remote"`),
					field("State", "ObservedSessionStateV1", `json:"state"`),
					field("Since", "time.Time", `json:"since"`),
				),
				stableType("RedactedDeviceV1", "struct",
					field("ID", rawPath+".RedactedID", `json:"id"`),
					field("Entities", "[]RedactedEntityV1", `json:"entities"`),
					field("UseCaseClaims", "[]RedactedUseCaseV1", `json:"usecase_claims"`),
				),
				stableType("RedactedEntityV1", "struct",
					field("ID", rawPath+".RedactedID", `json:"id"`),
					field("Features", "[]RedactedFeatureV1", `json:"features"`),
				),
				stableType("RedactedFeatureV1", "struct",
					field("ID", rawPath+".RedactedID", `json:"id"`),
					field("Role", "FeatureRoleV1", `json:"role"`),
				),
				stableType("RedactedUseCaseV1", "struct",
					field("ID", rawPath+".RedactedID", `json:"id"`),
				),
			},
			enums: []stableEnumSpec{
				{exact: true, values: []manifestStableEnumValue{enumValue("SnapshotContractV1", "helianthus.eebus.runtime.raw-snapshot.v1")}},
				{exact: true, typeName: "ObservedRuntimeStateV1", values: []manifestStableEnumValue{
					enumValue("ObservedRuntimeStateV1Unknown", "unknown"),
					enumValue("ObservedRuntimeStateV1Stopped", "stopped"),
					enumValue("ObservedRuntimeStateV1Starting", "starting"),
					enumValue("ObservedRuntimeStateV1Ready", "ready"),
					enumValue("ObservedRuntimeStateV1Degraded", "degraded"),
					enumValue("ObservedRuntimeStateV1Shutdown", "shutdown"),
				}},
				{exact: true, typeName: "DegradationReasonV1", values: []manifestStableEnumValue{
					enumValue("DegradationReasonV1MissingDiscovery", "missing-discovery"),
					enumValue("DegradationReasonV1DeniedTrust", "denied-trust"),
					enumValue("DegradationReasonV1RemoteDisconnect", "remote-disconnect"),
					enumValue("DegradationReasonV1CertificateUnavailable", "certificate-unavailable"),
					enumValue("DegradationReasonV1NoVisibleServices", "no-visible-services"),
					enumValue("DegradationReasonV1NoData", "no-data"),
				}},
				{exact: true, typeName: "ServiceKindV1", values: []manifestStableEnumValue{
					enumValue("ServiceKindV1Local", "local"),
					enumValue("ServiceKindV1Remote", "remote"),
				}},
				{exact: true, typeName: "ObservedSessionStateV1", values: []manifestStableEnumValue{
					enumValue("ObservedSessionStateV1Unknown", "unknown"),
					enumValue("ObservedSessionStateV1Connecting", "connecting"),
					enumValue("ObservedSessionStateV1Connected", "connected"),
					enumValue("ObservedSessionStateV1Disconnected", "disconnected"),
					enumValue("ObservedSessionStateV1Degraded", "degraded"),
				}},
				{exact: true, typeName: "FeatureRoleV1", values: []manifestStableEnumValue{
					enumValue("FeatureRoleV1Unspecified", ""),
					enumValue("FeatureRoleV1Client", "client"),
					enumValue("FeatureRoleV1Server", "server"),
				}},
			},
		},
		{
			importPath: rawPath,
			root:       "IdentityDocumentV1",
			types: []manifestStableType{
				stableType("ContractVersion", "string"),
				stableType("MaskTier", "string"),
				stableType("EndpointRoleV1", "string"),
				stableType("IDKind", "string"),
				stableType("RedactedID", "struct",
					field("Kind", "IDKind", `json:"kind"`),
					field("Masked", "string", `json:"masked"`),
					field("Digest", "string", `json:"digest,omitempty"`),
				),
				stableType("UnknownPath", "string"),
				stableType("UnknownField", "struct",
					field("Path", "UnknownPath", `json:"path"`),
					field("Value", "OpaqueValue", `json:"value"`),
				),
				stableType("OpaqueValue", "struct",
					field("Masked", "string", `json:"masked"`),
					field("Digest", "string", `json:"digest,omitempty"`),
					field("Size", "int", `json:"size,omitempty"`),
				),
				stableType("EndpointIdentityV1", "struct",
					field("Role", "EndpointRoleV1", `json:"role"`),
					field("ID", "RedactedID", `json:"id"`),
					field("Unknown", "[]UnknownField", `json:"unknown,omitempty"`),
				),
				stableType("SessionIdentityV1", "struct",
					field("ID", "RedactedID", `json:"id"`),
					field("RemoteID", "RedactedID", `json:"remote_id"`),
					field("Unknown", "[]UnknownField", `json:"unknown,omitempty"`),
				),
				stableType("IdentityDocumentV1", "struct",
					field("Contract", "ContractVersion", `json:"contract"`),
					field("MaskTier", "MaskTier", `json:"mask_tier"`),
					field("CapturedAt", "time.Time", `json:"captured_at"`),
					field("Local", "EndpointIdentityV1", `json:"local"`),
					field("Remotes", "[]EndpointIdentityV1", `json:"remotes,omitempty"`),
					field("Sessions", "[]SessionIdentityV1", `json:"sessions,omitempty"`),
					field("Unknown", "[]UnknownField", `json:"unknown,omitempty"`),
				),
			},
			enums: []stableEnumSpec{
				{typeName: "ContractVersion", values: []manifestStableEnumValue{enumValue("IdentityContractV1", "helianthus.eebus.raw.identity.v1")}},
				{typeName: "MaskTier", values: []manifestStableEnumValue{enumValue("MaskTierRedacted", "redacted")}},
				{exact: true, typeName: "EndpointRoleV1", values: []manifestStableEnumValue{
					enumValue("EndpointRoleV1Local", "local"),
					enumValue("EndpointRoleV1Remote", "remote"),
				}},
				{exact: true, typeName: "IDKind", values: []manifestStableEnumValue{
					enumValue("IDKindLocalSKI", "local-ski"),
					enumValue("IDKindRemoteSKI", "remote-ski"),
					enumValue("IDKindCertificateFingerprint", "certificate-fingerprint"),
					enumValue("IDKindPeer", "peer"),
					enumValue("IDKindSession", "session"),
				}},
				{exact: true, typeName: "UnknownPath", values: []manifestStableEnumValue{
					enumValue("UnknownPathDocument", "/document/unknown"),
					enumValue("UnknownPathDevice", "/device/unknown"),
					enumValue("UnknownPathLocal", "/local/unknown"),
					enumValue("UnknownPathRemote", "/remote/unknown"),
					enumValue("UnknownPathSession", "/session/unknown"),
				}},
			},
		},
		{
			importPath: rawPath,
			root:       "ReadAuthorizationV1",
			types: []manifestStableType{
				stableType("ToolV1", "string"),
				stableType("AuthScopeV1", "string"),
				stableType("FeatureRoleV1", "string"),
				stableType("OperationV1", "string"),
				stableType("ObservationSourceV1", "string"),
				stableType("ChangeabilityV1", "string"),
				stableType("ConstraintStatusV1", "string"),
				stableType("ErrorCodeV1", "string"),
				stableType("SourceLayerV1", "string"),
				stableType("HashV1", "string"),
				stableType("TypedValueV1", "struct"),
				stableType("ReadAuthorizationV1", "struct",
					field("PrincipalClass", "string", `json:"principal_class"`),
					field("Scope", "AuthScopeV1", `json:"scope"`),
					field("Tool", "ToolV1", `json:"tool"`),
					field("MaskTier", "MaskTier", `json:"mask_tier"`),
				),
				stableType("WriteAuthorizationV1", "struct",
					field("PrincipalClass", "string", `json:"principal_class"`),
					field("Scope", "AuthScopeV1", `json:"scope"`),
					field("Tool", "ToolV1", `json:"tool"`),
					field("MaskTier", "MaskTier", `json:"mask_tier"`),
				),
				stableType("ModeV1", "string"),
				stableType("MutationStateV1", "string"),
				stableType("ConstraintOverrideV1", "struct",
					field("ProfileID", "string", `json:"profile_id"`),
					field("Justification", "string", `json:"justification"`),
					field("ExpiresAt", "time.Time", `json:"expires_at"`),
				),
				stableType("MutationLabProfileV1", "struct",
					field("Contract", "string", `json:"contract"`),
					field("ProfileID", "string", `json:"profile_id"`),
					field("Target", "FeatureTargetV1", `json:"target"`),
					field("AllowedValueHashes", "[]HashV1", `json:"allowed_value_hashes"`),
					field("RollbackValueHash", "HashV1", `json:"rollback_value_hash"`),
					field("MaximumProbeTTLSeconds", "uint64", `json:"maximum_probe_ttl_seconds"`),
					field("SafetyPredicates", "[]string", `json:"safety_predicates"`),
					field("EvidenceHashes", "[]HashV1", `json:"evidence_hashes"`),
					field("ExpiresAt", "time.Time", `json:"expires_at"`),
				),
				stableType("FeatureDataSetRequestV1", "struct",
					field("Target", "FeatureTargetV1", `json:"target"`),
					field("Value", "TypedValueV1", `json:"value"`),
					field("ReadToken", "string", `json:"read_token"`),
					field("ExpectedCurrent", "*TypedValueV1", `json:"expected_current,omitempty"`),
					field("IdempotencyKey", "string", `json:"idempotency_key"`),
					field("Mode", "ModeV1", `json:"mode"`),
					field("ProbeTTLSeconds", "uint64", `json:"probe_ttl_seconds,omitempty"`),
					field("ConstraintsOverride", "*ConstraintOverrideV1", `json:"constraints_override,omitempty"`),
				),
				stableType("MutationGetRequestV1", "struct",
					field("MutationRef", "string", `json:"mutation_ref"`),
				),
				stableType("MutationRollbackRequestV1", "struct",
					field("MutationRef", "string", `json:"mutation_ref"`),
					field("IdempotencyKey", "string", `json:"idempotency_key"`),
				),
				stableType("AuditTransitionV1", "struct",
					field("Sequence", "uint64", `json:"sequence"`),
					field("State", "MutationStateV1", `json:"state"`),
					field("TransitionedAt", "time.Time", `json:"transitioned_at"`),
					field("Classification", "string", `json:"classification,omitempty"`),
					field("PreviousHash", "*HashV1", `json:"previous_hash"`),
					field("TransitionHash", "HashV1", `json:"transition_hash"`),
				),
				stableType("ApplyVerificationV1", "struct",
					field("Relation", "string", `json:"relation"`),
					field("Verified", "bool", `json:"verified"`),
					field("EqualValueHash", "HashV1", `json:"equal_value_hash"`),
					field("VerifiedAt", "time.Time", `json:"verified_at"`),
				),
				stableType("RollbackVerificationV1", "struct",
					field("Relation", "string", `json:"relation"`),
					field("Verified", "bool", `json:"verified"`),
					field("EqualValueHash", "HashV1", `json:"equal_value_hash"`),
					field("VerifiedAt", "time.Time", `json:"verified_at"`),
				),
				stableType("ConflictEvidenceV1", "struct",
					field("Relation", "string", `json:"relation"`),
					field("Verified", "bool", `json:"verified"`),
					field("BeforeHash", "HashV1", `json:"before_hash"`),
					field("RequestedHash", "HashV1", `json:"requested_hash"`),
					field("ObservedAfterHash", "HashV1", `json:"observed_after_hash"`),
					field("VerifiedAt", "time.Time", `json:"verified_at"`),
				),
				stableType("NoContactEvidenceV1", "struct",
					field("RemoteFramesSent", "uint64", `json:"remote_frames_sent"`),
					field("LastCompletedPhase", "string", `json:"last_completed_phase"`),
					field("VerifiedAt", "time.Time", `json:"verified_at"`),
				),
				stableType("RejectionVerificationV1", "struct",
					field("Relation", "string", `json:"relation"`),
					field("Verified", "bool", `json:"verified"`),
					field("CorrelatedRejection", "bool", `json:"correlated_rejection"`),
					field("EqualValueHash", "HashV1", `json:"equal_value_hash"`),
					field("VerifiedAt", "time.Time", `json:"verified_at"`),
				),
				stableType("NoEffectVerificationV1", "struct",
					field("Relation", "string", `json:"relation"`),
					field("Verified", "bool", `json:"verified"`),
					field("EqualValueHash", "HashV1", `json:"equal_value_hash"`),
					field("VerifiedAt", "time.Time", `json:"verified_at"`),
				),
				stableType("OutcomeEvidenceV1", "struct",
					field("PossibleSideEffect", "bool", `json:"possible_side_effect"`),
					field("BlindRetryForbidden", "bool", `json:"blind_retry_forbidden"`),
					field("LastDurableState", "MutationStateV1", `json:"last_durable_state"`),
					field("RecordedAt", "time.Time", `json:"recorded_at"`),
				),
				stableType("RollbackV1", "struct",
					field("State", "MutationStateV1", `json:"state"`),
					field("Before", "TypedValueV1", `json:"before"`),
					field("ProtocolAccepted", "*bool", `json:"protocol_accepted"`),
					field("ObservedAfter", "*TypedValueV1", `json:"observed_after"`),
					field("Error", "*ErrorV1", `json:"error,omitempty"`),
					field("Verification", "*RollbackVerificationV1", `json:"verification,omitempty"`),
				),
				stableType("MutationV1", "struct",
					field("MutationRef", "string", `json:"mutation_ref"`),
					field("State", "MutationStateV1", `json:"state"`),
					field("Mode", "ModeV1", `json:"mode"`),
					field("Target", "FeatureTargetV1", `json:"target"`),
					field("Runtime", "RuntimeBindingV1", `json:"runtime"`),
					field("Before", "TypedValueV1", `json:"before"`),
					field("Requested", "TypedValueV1", `json:"requested"`),
					field("ProtocolAccepted", "*bool", `json:"protocol_accepted"`),
					field("ObservedAfter", "*TypedValueV1", `json:"observed_after"`),
					field("Rollback", "*RollbackV1", `json:"rollback,omitempty"`),
					field("ProbeDeadline", "*time.Time", `json:"probe_deadline,omitempty"`),
					field("CreatedAt", "time.Time", `json:"created_at"`),
					field("UpdatedAt", "time.Time", `json:"updated_at"`),
					field("Error", "*ErrorV1", `json:"error,omitempty"`),
					field("ApplyVerification", "*ApplyVerificationV1", `json:"apply_verification,omitempty"`),
					field("ConflictEvidence", "*ConflictEvidenceV1", `json:"conflict_evidence,omitempty"`),
					field("NoContactEvidence", "*NoContactEvidenceV1", `json:"no_contact_evidence,omitempty"`),
					field("RejectionVerification", "*RejectionVerificationV1", `json:"rejection_verification,omitempty"`),
					field("NoEffectVerification", "*NoEffectVerificationV1", `json:"no_effect_verification,omitempty"`),
					field("OutcomeEvidence", "*OutcomeEvidenceV1", `json:"outcome_evidence,omitempty"`),
					field("Audit", "[]AuditTransitionV1", `json:"audit"`),
				),
				stableType("RuntimeBindingV1", "struct",
					field("RuntimeEpoch", "uint64", `json:"runtime_epoch"`),
					field("ConnectionGeneration", "uint64", `json:"connection_generation"`),
				),
				stableType("FeatureLocatorV1", "struct",
					field("RemoteSKI", "string", `json:"remote_ski"`),
					field("SHIPID", "string", `json:"ship_id"`),
					field("DeviceAddress", "string", `json:"device_address"`),
					field("EntityAddress", "[]uint64", `json:"entity_address"`),
					field("FeatureAddress", "uint64", `json:"feature_address"`),
					field("FeatureType", "string", `json:"feature_type"`),
					field("FeatureRole", "FeatureRoleV1", `json:"feature_role"`),
				),
				stableType("FeatureTargetV1", "struct",
					field("RemoteSKI", "string", `json:"remote_ski"`),
					field("SHIPID", "string", `json:"ship_id"`),
					field("DeviceAddress", "string", `json:"device_address"`),
					field("EntityAddress", "[]uint64", `json:"entity_address"`),
					field("FeatureAddress", "uint64", `json:"feature_address"`),
					field("FeatureType", "string", `json:"feature_type"`),
					field("FeatureRole", "FeatureRoleV1", `json:"feature_role"`),
					field("Function", "string", `json:"function"`),
					field("Operation", "OperationV1", `json:"operation"`),
				),
				stableType("FullOperationsV1", "struct",
					field("Read", "bool", `json:"read"`),
					field("Write", "bool", `json:"write"`),
				),
				stableType("ConstraintSetV1", "struct",
					field("Status", "ConstraintStatusV1", `json:"status"`),
					field("EnumValues", "[]TypedValueV1", `json:"enum_values,omitempty"`),
					field("Minimum", "*TypedValueV1", `json:"minimum,omitempty"`),
					field("Maximum", "*TypedValueV1", `json:"maximum,omitempty"`),
					field("Step", "*TypedValueV1", `json:"step,omitempty"`),
					field("Unit", "string", `json:"unit,omitempty"`),
					field("MinCardinality", "*uint64", `json:"min_cardinality,omitempty"`),
					field("MaxCardinality", "*uint64", `json:"max_cardinality,omitempty"`),
					field("CrossFieldRules", "[]string", `json:"cross_field_rules,omitempty"`),
				),
				stableType("FunctionDescriptorV1", "struct",
					field("Function", "string", `json:"function"`),
					field("Description", "string", `json:"description,omitempty"`),
					field("PossibleOperations", "FullOperationsV1", `json:"possible_operations"`),
					field("Changeable", "ChangeabilityV1", `json:"changeable"`),
					field("Constraints", "ConstraintSetV1", `json:"constraints"`),
				),
				stableType("FeaturesGetRequestV1", "struct",
					field("Target", "FeatureLocatorV1", `json:"target"`),
				),
				stableType("FeaturesGetDataV1", "struct",
					field("Feature", "FeatureLocatorV1", `json:"feature"`),
					field("Description", "string", `json:"description,omitempty"`),
					field("Functions", "[]FunctionDescriptorV1", `json:"functions"`),
					field("Runtime", "RuntimeBindingV1", `json:"runtime"`),
					field("DataTimestamp", "time.Time", `json:"data_timestamp"`),
					field("Source", "ObservationSourceV1", `json:"source"`),
					field("DataHash", "HashV1", `json:"data_hash"`),
				),
				stableType("FeatureDataGetRequestV1", "struct",
					field("Targets", "[]FeatureTargetV1", `json:"targets"`),
					field("TimeoutMS", "uint64", `json:"timeout_ms,omitempty"`),
				),
				stableType("ProtocolMessageV1", "struct",
					field("Classifier", "string", `json:"classifier"`),
					field("CorrelationKey", "uint64", `json:"correlation_key"`),
					field("Function", "string", `json:"function"`),
					field("Data", "*TypedValueV1", `json:"data,omitempty"`),
					field("ErrorNumber", "*uint64", `json:"error_number,omitempty"`),
					field("Unknown", "[]OpaqueObservationV1", `json:"unknown,omitempty"`),
				),
				stableType("OpaqueObservationV1", "struct",
					field("Path", "string", `json:"path"`),
					field("Source", "string", `json:"source"`),
					field("Value", "TypedValueV1", `json:"value"`),
				),
				stableType("ReadTokenV1", "struct",
					field("ReadToken", "string", `json:"read_token"`),
					field("Reusable", "bool", `json:"reusable"`),
					field("ExpiresAt", "time.Time", `json:"expires_at"`),
					field("BindingHash", "HashV1", `json:"binding_hash"`),
				),
				stableType("ReadObservationV1", "struct",
					field("Target", "FeatureTargetV1", `json:"target"`),
					field("Runtime", "RuntimeBindingV1", `json:"runtime"`),
					field("RawRequest", "ProtocolMessageV1", `json:"raw_request"`),
					field("RawResponse", "ProtocolMessageV1", `json:"raw_response"`),
					field("Value", "TypedValueV1", `json:"value"`),
					field("Unknown", "[]OpaqueObservationV1", `json:"unknown,omitempty"`),
					field("RequestedAt", "time.Time", `json:"requested_at"`),
					field("ReceivedAt", "time.Time", `json:"received_at"`),
					field("DataTimestamp", "time.Time", `json:"data_timestamp"`),
					field("Source", "ObservationSourceV1", `json:"source"`),
					field("ReadToken", "ReadTokenV1", `json:"read_token"`),
					field("DataHash", "HashV1", `json:"data_hash"`),
				),
				stableType("ErrorV1", "struct",
					field("Code", "ErrorCodeV1", `json:"code"`),
					field("Message", "string", `json:"message"`),
					field("Retriable", "bool", `json:"retriable"`),
					field("SourceLayer", "SourceLayerV1", `json:"source_layer"`),
					field("Details", "*ErrorDetailsV1", `json:"details,omitempty"`),
				),
				stableType("ErrorDetailsV1", "struct",
					field("TargetIndex", "*uint64", `json:"target_index,omitempty"`),
					field("Classification", "string", `json:"classification,omitempty"`),
					field("Unknown", "[]OpaqueObservationV1", `json:"unknown,omitempty"`),
				),
				stableType("ReadFailureV1", "struct",
					field("TargetIndex", "uint64", `json:"target_index"`),
					field("Target", "FeatureTargetV1", `json:"target"`),
					field("Error", "ErrorV1", `json:"error"`),
				),
				stableType("FeatureDataGetDataV1", "struct",
					field("Results", "[]ReadObservationV1", `json:"results"`),
					field("Failures", "[]ReadFailureV1", `json:"failures"`),
					field("Complete", "bool", `json:"complete"`),
				),
			},
			enums: []stableEnumSpec{
				{exact: true, typeName: "ToolV1", values: []manifestStableEnumValue{
					enumValue("ToolV1FeaturesGet", "eebus.v1.features.get"),
					enumValue("ToolV1FeaturesDataGet", "eebus.v1.features.data.get"),
					enumValue("ToolV1FeaturesDataSet", "eebus.v1.features.data.set"),
					enumValue("ToolV1MutationsGet", "eebus.v1.mutations.get"),
					enumValue("ToolV1MutationsRollback", "eebus.v1.mutations.rollback"),
				}},
				{exact: true, typeName: "AuthScopeV1", values: []manifestStableEnumValue{
					enumValue("AuthScopeV1RawRead", "eebus.raw.read"),
					enumValue("AuthScopeV1RawWrite", "eebus.raw.write"),
				}},
				{exact: true, typeName: "ModeV1", values: []manifestStableEnumValue{
					enumValue("ModeV1Apply", "apply"),
					enumValue("ModeV1Probe", "probe"),
				}},
				{exact: true, typeName: "MutationStateV1", values: []manifestStableEnumValue{
					enumValue("MutationStateV1Prepared", "prepared"),
					enumValue("MutationStateV1DispatchIntent", "dispatch_intent"),
					enumValue("MutationStateV1ReplyObserved", "reply_observed"),
					enumValue("MutationStateV1VerifyPending", "verify_pending"),
					enumValue("MutationStateV1Applied", "applied"),
					enumValue("MutationStateV1ProbeActive", "probe_active"),
					enumValue("MutationStateV1RollbackIntent", "rollback_intent"),
					enumValue("MutationStateV1RollbackDispatchIntent", "rollback_dispatch_intent"),
					enumValue("MutationStateV1RollbackReplyObserved", "rollback_reply_observed"),
					enumValue("MutationStateV1RollbackVerifyPending", "rollback_verify_pending"),
					enumValue("MutationStateV1RolledBack", "rolled_back"),
					enumValue("MutationStateV1OutcomeUnknown", "outcome_unknown"),
					enumValue("MutationStateV1Conflict", "conflict"),
					enumValue("MutationStateV1FailedNoContact", "failed_no_contact"),
					enumValue("MutationStateV1Rejected", "rejected"),
					enumValue("MutationStateV1NoEffect", "no_effect"),
				}},
				{exact: true, typeName: "FeatureRoleV1", values: []manifestStableEnumValue{
					enumValue("FeatureRoleV1Client", "client"),
					enumValue("FeatureRoleV1Server", "server"),
					enumValue("FeatureRoleV1Special", "special"),
				}},
				{exact: true, typeName: "OperationV1", values: []manifestStableEnumValue{
					enumValue("OperationV1Read", "READ"),
					enumValue("OperationV1Write", "WRITE"),
				}},
				{exact: true, typeName: "ObservationSourceV1", values: []manifestStableEnumValue{
					enumValue("ObservationSourceV1Live", "live"),
					enumValue("ObservationSourceV1Cache", "cache"),
				}},
				{exact: true, typeName: "ChangeabilityV1", values: []manifestStableEnumValue{
					enumValue("ChangeabilityV1Unknown", "unknown"),
					enumValue("ChangeabilityV1False", "false"),
					enumValue("ChangeabilityV1True", "true"),
				}},
				{exact: true, typeName: "ConstraintStatusV1", values: []manifestStableEnumValue{
					enumValue("ConstraintStatusV1Unknown", "unknown"),
					enumValue("ConstraintStatusV1Known", "known"),
				}},
				{exact: true, typeName: "ErrorCodeV1", values: []manifestStableEnumValue{
					enumValue("ErrorCodeV1PermissionDenied", "permission_denied"),
					enumValue("ErrorCodeV1InvalidArgument", "invalid_argument"),
					enumValue("ErrorCodeV1UnsupportedOperation", "unsupported_operation"),
					enumValue("ErrorCodeV1PartialOperationForbidden", "partial_operation_forbidden"),
					enumValue("ErrorCodeV1ConstraintsUnknown", "constraints_unknown"),
					enumValue("ErrorCodeV1ConstraintFailure", "constraint_failure"),
					enumValue("ErrorCodeV1StaleReadToken", "stale_read_token"),
					enumValue("ErrorCodeV1CASMismatch", "cas_mismatch"),
					enumValue("ErrorCodeV1RuntimeEpochMismatch", "runtime_epoch_mismatch"),
					enumValue("ErrorCodeV1ConnectionGenerationMismatch", "connection_generation_mismatch"),
					enumValue("ErrorCodeV1IdempotencyConflict", "idempotency_conflict"),
					enumValue("ErrorCodeV1WriterBusy", "writer_busy"),
					enumValue("ErrorCodeV1Disconnected", "disconnected"),
					enumValue("ErrorCodeV1Timeout", "timeout"),
					enumValue("ErrorCodeV1Cancelled", "cancelled"),
					enumValue("ErrorCodeV1RemoteError", "remote_error"),
					enumValue("ErrorCodeV1TypedEmpty", "typed_empty"),
					enumValue("ErrorCodeV1DecodeError", "decode_error"),
					enumValue("ErrorCodeV1PartialResult", "partial_result"),
					enumValue("ErrorCodeV1OutcomeUnknown", "outcome_unknown"),
					enumValue("ErrorCodeV1Conflict", "conflict"),
					enumValue("ErrorCodeV1RollbackFailed", "rollback_failed"),
					enumValue("ErrorCodeV1NoEffect", "no_effect"),
					enumValue("ErrorCodeV1NotFound", "not_found"),
					enumValue("ErrorCodeV1SecretDetected", "secret_detected"),
					enumValue("ErrorCodeV1Internal", "internal"),
				}},
				{exact: true, typeName: "SourceLayerV1", values: []manifestStableEnumValue{
					enumValue("SourceLayerV1Authorization", "eebusreg-runtime"),
					enumValue("SourceLayerV1Validation", "eebusreg-runtime"),
					enumValue("SourceLayerV1Runtime", "eebusreg-runtime"),
					enumValue("SourceLayerV1Executor", "eebus-go-executor"),
					enumValue("SourceLayerV1SpineRoundTrip", "spine-go-round-trip"),
					enumValue("SourceLayerV1Decode", "eebusreg-runtime"),
					enumValue("SourceLayerV1Remote", "remote"),
				}},
			},
		},
		{
			importPath: evidencePath,
			root:       "EnvelopeV1",
			types: []manifestStableType{
				stableType("ContractVersion", "string"),
				stableType("CaptureProvenanceV1", "string"),
				stableType("RawSnapshotScopeV1", "string"),
				stableType("AuthScope", "string"),
				stableType("ObjectKind", "string"),
				stableType("ReferenceV1", "struct",
					field("Runtime", rawPath+".RedactedID", `json:"runtime"`),
					field("Contract", "ContractVersion", `json:"contract"`),
					field("CaptureProvenance", "CaptureProvenanceV1", `json:"capture_provenance"`),
					field("Scope", "RawSnapshotScopeV1", `json:"scope"`),
					field("MaskTier", rawPath+".MaskTier", `json:"mask_tier"`),
					field("AuthScope", "AuthScope", `json:"auth_scope"`),
				),
				stableType("ObjectV1", "struct",
					field("Kind", "ObjectKind", `json:"kind"`),
					field("Digest", "string", `json:"digest"`),
					field("Size", "int", `json:"size"`),
					field("DataTimestamp", "time.Time", `json:"data_timestamp"`),
					field("Unknown", "[]"+rawPath+".UnknownField", `json:"unknown,omitempty"`),
				),
				stableType("EnvelopeV1", "struct",
					field("Reference", "ReferenceV1", `json:"ref"`),
					field("CapturedAt", "time.Time", `json:"captured_at"`),
					field("DataTimestamp", "time.Time", `json:"data_timestamp"`),
					field("Objects", "[]ObjectV1", `json:"objects,omitempty"`),
					field("DataHash", "string", `json:"data_hash,omitempty"`),
				),
			},
			enums: []stableEnumSpec{
				{typeName: "ContractVersion", values: []manifestStableEnumValue{enumValue("EnvelopeContractV1", "helianthus.eebus.raw.evidence-envelope.v1")}},
				{exact: true, typeName: "CaptureProvenanceV1", values: []manifestStableEnumValue{
					enumValue("CaptureProvenanceRuntimeObservation", "runtime-observation"),
				}},
				{exact: true, typeName: "RawSnapshotScopeV1", values: []manifestStableEnumValue{
					enumValue("RawSnapshotScopeRoot", "raw-root"),
					enumValue("RawSnapshotScopeIdentity", "raw-identity"),
					enumValue("RawSnapshotScopeTopology", "raw-topology"),
					enumValue("RawSnapshotScopeServices", "raw-services"),
					enumValue("RawSnapshotScopeSessions", "raw-sessions"),
					enumValue("RawSnapshotScopeUnknown", "raw-unknown"),
				}},
				{exact: true, typeName: "AuthScope", values: []manifestStableEnumValue{enumValue("AuthScopeReadRaw", "eebus.raw.read")}},
				{exact: true, typeName: "ObjectKind", values: []manifestStableEnumValue{
					enumValue("ObjectKindIdentity", "identity"),
					enumValue("ObjectKindTopology", "topology"),
					enumValue("ObjectKindService", "service"),
					enumValue("ObjectKindSession", "session"),
					enumValue("ObjectKindUnknown", "unknown"),
				}},
			},
		},
	}
}

func receiverName(expr ast.Expr) string {
	switch typed := expr.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.ParenExpr:
		return receiverName(typed.X)
	case *ast.StarExpr:
		return receiverName(typed.X)
	case *ast.IndexExpr:
		return receiverName(typed.X)
	case *ast.IndexListExpr:
		return receiverName(typed.X)
	default:
		return ""
	}
}

func safeManifestDestination(root, requested string) (string, error) {
	if !filepath.IsAbs(requested) {
		return "", fmt.Errorf("API boundary artifact destination must be an absolute path outside the repository")
	}
	requested = filepath.Clean(requested)
	if info, err := os.Lstat(requested); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("API boundary artifact destination must be outside the repository and must not be a symlink")
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("API boundary artifact destination must be a regular path outside the repository")
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect API boundary artifact destination: %w", err)
	}

	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve repository root for API boundary artifact: %w", err)
	}
	parent := filepath.Dir(requested)
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", fmt.Errorf("API boundary artifact parent must exist outside the repository: %w", err)
	}
	info, err := os.Stat(resolvedParent)
	if err != nil {
		return "", fmt.Errorf("inspect API boundary artifact parent: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("API boundary artifact parent must be a directory outside the repository")
	}
	resolvedDestination := filepath.Join(resolvedParent, filepath.Base(requested))
	inside, err := pathWithin(resolvedRoot, resolvedDestination)
	if err != nil {
		return "", err
	}
	if inside {
		return "", fmt.Errorf("API boundary artifact destination must be outside the repository")
	}
	return resolvedDestination, nil
}

func writeAtomic(destination string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(destination), ".api-boundary-*.tmp")
	if err != nil {
		return err
	}
	temporary := file.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(temporary)
		}
	}()
	if err := file.Chmod(0o644); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if info, err := os.Lstat(destination); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to replace symlink destination")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(temporary, destination); err != nil {
		return err
	}
	keep = true
	return nil
}

func gitNullList(root string, args ...string) ([]string, error) {
	command := exec.Command("git", args...)
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return splitNull(output), nil
}

func gitStagedSymlinks(root string) (map[string]string, error) {
	records, err := gitNullList(root, "ls-files", "--stage", "-z")
	if err != nil {
		return nil, err
	}
	result := make(map[string]string)
	for _, record := range records {
		tab := strings.IndexByte(record, '\t')
		if tab < 0 {
			return nil, fmt.Errorf("parse git staged path record %q", record)
		}
		metadata := strings.Fields(record[:tab])
		if len(metadata) != 3 || metadata[0] != "120000" {
			continue
		}
		command := exec.Command("git", "cat-file", "blob", metadata[1])
		command.Dir = root
		target, err := command.Output()
		if err != nil {
			return nil, fmt.Errorf("inspect tracked symlink %s: %w", record[tab+1:], err)
		}
		result[filepath.ToSlash(record[tab+1:])] = string(target)
	}
	return result, nil
}

func splitNull(data []byte) []string {
	parts := strings.Split(string(data), "\x00")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

func addPathOrigin(paths map[string]map[pathOrigin]struct{}, path string, origin pathOrigin) {
	if paths[path] == nil {
		paths[path] = make(map[pathOrigin]struct{})
	}
	paths[path][origin] = struct{}{}
}

func validateRelativePath(path string) error {
	if filepath.IsAbs(filepath.FromSlash(path)) {
		return fmt.Errorf("absolute paths are forbidden")
	}
	for _, part := range strings.Split(path, "/") {
		if part == ".." {
			return fmt.Errorf("path traversal is forbidden")
		}
	}
	return nil
}

func symlinkViolation(path, target string) string {
	targetKind := "relative"
	if filepath.IsAbs(target) {
		targetKind = "absolute"
	} else {
		for _, part := range strings.Split(filepath.ToSlash(target), "/") {
			if part == ".." {
				targetKind = "traversal"
				break
			}
		}
	}
	return fmt.Sprintf("symlink is forbidden: %s has %s target %q", path, targetKind, target)
}

func hasFoldedPathSegment(path, segment string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if strings.EqualFold(part, segment) {
			return true
		}
	}
	return false
}

func isDocumentationPath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	extension := strings.ToLower(filepath.Ext(base))
	if _, ok := documentationExtensions[extension]; ok {
		return true
	}
	if extension != "" {
		return false
	}
	name := base
	_, ok := documentationNames[name]
	return ok
}

func pathWithin(root, candidate string) (bool, error) {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false, fmt.Errorf("compare API boundary artifact destination with repository: %w", err)
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))), nil
}

func checkExportedName(fset *token.FileSet, rel string, ident *ast.Ident, violations *[]string) {
	if ident == nil || !ident.IsExported() {
		return
	}
	name := ident.Name
	for _, fragment := range forbiddenExportFragments {
		if name == "SHIPID" && fragment == "SHIP" {
			continue
		}
		if name == "SourceLayerV1SpineRoundTrip" && fragment == "Spine" {
			continue
		}
		if strings.Contains(name, fragment) {
			*violations = append(*violations, at(fset, ident.Pos(), rel, "public API exposes forbidden boundary term "+fragment))
			return
		}
	}
	if looksLikeMutationSurface(name) {
		*violations = append(*violations, at(fset, ident.Pos(), rel, "public API exposes premature trust or pairing mutation surface"))
	}
}

func checkExportedTypeSurface(fset *token.FileSet, rel string, node ast.Node, violations *[]string) {
	ast.Inspect(node, func(current ast.Node) bool {
		ident, ok := current.(*ast.Ident)
		if ok {
			checkExportedName(fset, rel, ident, violations)
		}
		return true
	})
}

func internalImportAliases(file *ast.File, modulePath string) map[string]string {
	aliases := make(map[string]string)
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil || !strings.HasPrefix(importPath, modulePath+"/internal/") {
			continue
		}
		name := filepath.Base(importPath)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		if name != "" && name != "_" && name != "." {
			aliases[name] = importPath
		}
	}
	return aliases
}

func checkInternalTypeEscape(fset *token.FileSet, rel string, node ast.Node, aliases map[string]string, violations *[]string) {
	found := false
	ast.Inspect(node, func(current ast.Node) bool {
		if found {
			return false
		}
		selector, ok := current.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		identifier, ok := selector.X.(*ast.Ident)
		if !ok {
			return true
		}
		if _, internal := aliases[identifier.Name]; internal {
			*violations = append(*violations, at(fset, selector.Pos(), rel, "public API packages must not import internal implementation types"))
			found = true
			return false
		}
		return true
	})
}

func checkCrossRuntimeStrings(fset *token.FileSet, rel string, file *ast.File, violations *[]string) {
	ast.Inspect(file, func(node ast.Node) bool {
		literal, ok := node.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(literal.Value)
		if err == nil && strings.HasPrefix(strings.TrimSpace(value), "ebus.v1.") {
			*violations = append(*violations, at(fset, literal.Pos(), rel, "public API exposes forbidden eBUS runtime identifier"))
		}
		return true
	})
}

func looksLikeMutationSurface(name string) bool {
	for _, verb := range mutationVerbs {
		if !startsWithWord(name, verb) {
			continue
		}
		for _, noun := range mutationNouns {
			if strings.Contains(name, noun) {
				return true
			}
		}
	}
	return false
}

func startsWithWord(name string, word string) bool {
	if !strings.HasPrefix(name, word) {
		return false
	}
	if len(name) == len(word) {
		return true
	}
	next := []rune(strings.TrimPrefix(name, word))[0]
	return unicode.IsUpper(next) || unicode.IsDigit(next)
}

func hasPathSegment(path string, segment string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == segment {
			return true
		}
	}
	return false
}

func at(fset *token.FileSet, pos token.Pos, rel string, message string) string {
	position := fset.Position(pos)
	if position.IsValid() {
		return fmt.Sprintf("%s:%d: %s", rel, position.Line, message)
	}
	return rel + ": " + message
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
