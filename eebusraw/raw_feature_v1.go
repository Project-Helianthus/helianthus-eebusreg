package eebusraw

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// MaximumReadTargetsV1 is the largest number of targets one features.data.get request may name.
	MaximumReadTargetsV1 = 16
	// MaximumReadTimeoutMSV1 caps TimeoutMS in milliseconds; the runtime also uses it when
	// TimeoutMS
	// is zero.
	MaximumReadTimeoutMSV1 = 30000
)

// ToolV1 is an MCP method string of the raw eeBUS surface. An authorization snapshot names exactly
// one tool and the validators reject it when it is presented for another.
type ToolV1 string

const (
	// ToolV1FeaturesGet reads one feature inventory; it needs the raw read scope.
	ToolV1FeaturesGet ToolV1 = "eebus.v1.features.get"
	// ToolV1FeaturesDataGet performs full READs of 1..16 targets and issues read tokens.
	ToolV1FeaturesDataGet ToolV1 = "eebus.v1.features.data.get"
	// ToolV1FeaturesDataSet performs a governed full WRITE; it needs the raw write scope.
	ToolV1FeaturesDataSet ToolV1 = "eebus.v1.features.data.set"
	// ToolV1MutationsGet returns a stored MutationV1 without sending a frame.
	ToolV1MutationsGet ToolV1 = "eebus.v1.mutations.get"
	// ToolV1MutationsRollback restores a mutation before-image; it needs the raw write scope.
	ToolV1MutationsRollback ToolV1 = "eebus.v1.mutations.rollback"
)

// AuthScopeV1 is the authorization scope a transport boundary grants to a raw call.
type AuthScopeV1 string

const (
	// AuthScopeV1RawRead admits the three non-mutating tools only.
	AuthScopeV1RawRead AuthScopeV1 = "eebus.raw.read"
	// AuthScopeV1RawWrite admits data.set and rollback; it does not imply AuthScopeV1RawRead.
	AuthScopeV1RawWrite AuthScopeV1 = "eebus.raw.write"
)

// MaskTierRaw lets owner-authorized responses carry real identities, addresses and typed values;
// secret-classified content stays rejected even at this tier.
const MaskTierRaw MaskTier = "raw"

// FeatureRoleV1 is the native role of a remote feature, copied without substitution; a target
// naming another role than the topology reports resolves to not_found. See
// https://github.com/Project-Helianthus/helianthus-docs-eebus/tree/main/protocols.
type FeatureRoleV1 string

const (
	// FeatureRoleV1Client is the client role as the remote topology reports it.
	FeatureRoleV1Client FeatureRoleV1 = "client"
	// FeatureRoleV1Server is the server role as the remote topology reports it.
	FeatureRoleV1Server FeatureRoleV1 = "server"
	// FeatureRoleV1Special is the special role as the remote topology reports it.
	FeatureRoleV1Special FeatureRoleV1 = "special"
)

// OperationV1 is the full operation a target requests; partial and selector forms do not exist.
type OperationV1 string

const (
	// OperationV1Read requests the complete current function value.
	OperationV1Read OperationV1 = "READ"
	// OperationV1Write replaces the complete function value.
	OperationV1Write OperationV1 = "WRITE"
)

// ObservationSourceV1 says whether data came from the current connection or from retention.
type ObservationSourceV1 string

const (
	// ObservationSourceV1Live was observed on the current connection; every READ observation is
	// live.
	ObservationSourceV1Live ObservationSourceV1 = "live"
	// ObservationSourceV1Cache is feature inventory retained from an earlier live observation.
	ObservationSourceV1Cache ObservationSourceV1 = "cache"
)

// ChangeabilityV1 is a tri-state rather than a bool so that an undeclared value stays visible;
// a write requires true and unknown is never treated as true.
type ChangeabilityV1 string

const (
	// ChangeabilityV1Unknown means changeability was not established.
	ChangeabilityV1Unknown ChangeabilityV1 = "unknown"
	// ChangeabilityV1False means the function is declared not changeable.
	ChangeabilityV1False ChangeabilityV1 = "false"
	// ChangeabilityV1True means the function is declared changeable.
	ChangeabilityV1True ChangeabilityV1 = "true"
)

// ConstraintStatusV1 says whether a ConstraintSetV1 carries native constraints at all.
type ConstraintStatusV1 string

const (
	// ConstraintStatusV1Unknown requires every other constraint field to be empty.
	ConstraintStatusV1Unknown ConstraintStatusV1 = "unknown"
	// ConstraintStatusV1Known means the remaining fields are the complete native constraints.
	ConstraintStatusV1Known ConstraintStatusV1 = "known"
)

// ErrorCodeV1 is the closed raw error vocabulary; validators reject any other value. Meanings
// are specified in https://github.com/Project-Helianthus/helianthus-docs-eebus/tree/main/api.
type ErrorCodeV1 string

const (
	// ErrorCodeV1PermissionDenied reports a scope, tier, tool, principal or reference mismatch.
	ErrorCodeV1PermissionDenied ErrorCodeV1 = "permission_denied"
	// ErrorCodeV1InvalidArgument reports that the closed request shape or exact target is invalid.
	ErrorCodeV1InvalidArgument ErrorCodeV1 = "invalid_argument"
	// ErrorCodeV1UnsupportedOperation reports a full operation not declared or out of scope.
	ErrorCodeV1UnsupportedOperation ErrorCodeV1 = "unsupported_operation"
	// ErrorCodeV1PartialOperationForbidden reports that a selector, filter or partial operation was
	// requested.
	ErrorCodeV1PartialOperationForbidden ErrorCodeV1 = "partial_operation_forbidden"
	// ErrorCodeV1ConstraintsUnknown reports incomplete constraints and no matching lab profile.
	ErrorCodeV1ConstraintsUnknown ErrorCodeV1 = "constraints_unknown"
	// ErrorCodeV1ConstraintFailure reports that a known constraint, changeability, allowlist or
	// safety check failed.
	ErrorCodeV1ConstraintFailure ErrorCodeV1 = "constraint_failure"
	// ErrorCodeV1StaleReadToken reports an expired, consumed or differently bound read token.
	ErrorCodeV1StaleReadToken ErrorCodeV1 = "stale_read_token"
	// ErrorCodeV1CASMismatch reports a changed before-image; no WRITE frame was sent.
	ErrorCodeV1CASMismatch ErrorCodeV1 = "cas_mismatch"
	// ErrorCodeV1RuntimeEpochMismatch reports that the durable runtime identity changed.
	ErrorCodeV1RuntimeEpochMismatch ErrorCodeV1 = "runtime_epoch_mismatch"
	// ErrorCodeV1ConnectionGenerationMismatch reports that the admitted connection changed.
	ErrorCodeV1ConnectionGenerationMismatch ErrorCodeV1 = "connection_generation_mismatch"
	// ErrorCodeV1IdempotencyConflict reports an idempotency key reused for a different request.
	ErrorCodeV1IdempotencyConflict ErrorCodeV1 = "idempotency_conflict"
	// ErrorCodeV1WriterBusy reports that another writer holds the single global writer lease.
	ErrorCodeV1WriterBusy ErrorCodeV1 = "writer_busy"
	// ErrorCodeV1Disconnected reports that the connection ended before a trusted correlated result.
	ErrorCodeV1Disconnected ErrorCodeV1 = "disconnected"
	// ErrorCodeV1Timeout reports that the deadline passed before a trusted correlated result.
	ErrorCodeV1Timeout ErrorCodeV1 = "timeout"
	// ErrorCodeV1Cancelled reports that the caller cancelled before a trusted correlated result.
	ErrorCodeV1Cancelled ErrorCodeV1 = "cancelled"
	// ErrorCodeV1RemoteError reports that the remote answered with a correlated rejection or error.
	ErrorCodeV1RemoteError ErrorCodeV1 = "remote_error"
	// ErrorCodeV1TypedEmpty reports a known reply shape without typed data; never retriable.
	ErrorCodeV1TypedEmpty ErrorCodeV1 = "typed_empty"
	// ErrorCodeV1DecodeError reports that a reply or result is malformed or fails its contract.
	ErrorCodeV1DecodeError ErrorCodeV1 = "decode_error"
	// ErrorCodeV1PartialResult reports failed READ targets alongside retained completed results.
	ErrorCodeV1PartialResult ErrorCodeV1 = "partial_result"
	// ErrorCodeV1OutcomeUnknown reports a possible side effect; blind retry is forbidden.
	ErrorCodeV1OutcomeUnknown ErrorCodeV1 = "outcome_unknown"
	// ErrorCodeV1Conflict reports that the readback is a third value and writes are quarantined.
	ErrorCodeV1Conflict ErrorCodeV1 = "conflict"
	// ErrorCodeV1RollbackFailed reports that restoring the before-image could not be verified.
	ErrorCodeV1RollbackFailed ErrorCodeV1 = "rollback_failed"
	// ErrorCodeV1NoEffect reports that recovery verified the before-image after a possible send.
	ErrorCodeV1NoEffect ErrorCodeV1 = "no_effect"
	// ErrorCodeV1NotFound reports that the exact target, inventory or mutation reference has no
	// result.
	ErrorCodeV1NotFound ErrorCodeV1 = "not_found"
	// ErrorCodeV1SecretDetected reports that secret-classified content was rejected before output
	// or hashing.
	ErrorCodeV1SecretDetected ErrorCodeV1 = "secret_detected"
	// ErrorCodeV1Internal is a fixed public-safe internal failure; no backend text is exposed.
	ErrorCodeV1Internal ErrorCodeV1 = "internal"
)

// SourceLayerV1 names the layer that produced an ErrorV1. Authorization, Validation, Runtime and
// Decode share one wire value, so a consumer cannot tell them apart; a layer past the runtime
// proves that dispatch was reached.
type SourceLayerV1 string

const (
	// SourceLayerV1Authorization marks a denial before the runtime is contacted.
	SourceLayerV1Authorization SourceLayerV1 = "eebusreg-runtime"
	// SourceLayerV1Validation marks a contract validation failure.
	SourceLayerV1Validation SourceLayerV1 = "eebusreg-runtime"
	// SourceLayerV1Runtime marks an admission or state failure inside this runtime.
	SourceLayerV1Runtime SourceLayerV1 = "eebusreg-runtime"
	// SourceLayerV1Executor marks a failure of the protocol stack executing the operation.
	SourceLayerV1Executor SourceLayerV1 = "eebus-go-executor"
	// SourceLayerV1SpineRoundTrip marks a failure of the correlated request and reply.
	SourceLayerV1SpineRoundTrip SourceLayerV1 = "spine-go-round-trip"
	// SourceLayerV1Decode marks a reply that could not be decoded into typed data.
	SourceLayerV1Decode SourceLayerV1 = "eebusreg-runtime"
	// SourceLayerV1Remote marks an error reported by the remote device.
	SourceLayerV1Remote SourceLayerV1 = "remote"
)

// ReadAuthorizationV1 is the authorization snapshot the transport boundary selects for one raw
// READ-class call; it is never built from request data. ValidateReadAuthorizationV1 checks it.
type ReadAuthorizationV1 struct {
	PrincipalClass string      `json:"principal_class"`
	Scope          AuthScopeV1 `json:"scope"`
	Tool           ToolV1      `json:"tool"`
	MaskTier       MaskTier    `json:"mask_tier"`
}

// String returns a fixed redacted marker so the principal class and scope never reach logs.
func (auth ReadAuthorizationV1) String() string {
	return "read_authorization_v1:[redacted]"
}

// GoString returns the same redacted marker as String.
func (auth ReadAuthorizationV1) GoString() string {
	return auth.String()
}

// Format writes the redacted marker for every verb and flag.
func (auth ReadAuthorizationV1) Format(state fmt.State, _ rune) {
	formatRawFeatureV1(state, auth.String())
}

// RuntimeBindingV1 pins data to one runtime epoch and connection generation; both are positive
// and supplied by the runtime, never by a caller. A changed value invalidates tokens and writes.
type RuntimeBindingV1 struct {
	RuntimeEpoch         uint64 `json:"runtime_epoch"`
	ConnectionGeneration uint64 `json:"connection_generation"`
}

// FeatureLocatorV1 addresses one remote feature exactly. RemoteSKI is 40 lowercase hexadecimal
// digits, EntityAddress has 1..16 parts, numbers stay within 2^53-1, and FeatureType keeps
// its native casing; see
// https://github.com/Project-Helianthus/helianthus-docs-eebus/tree/main/protocols.
type FeatureLocatorV1 struct {
	RemoteSKI      string        `json:"remote_ski"`
	SHIPID         string        `json:"ship_id"`
	DeviceAddress  string        `json:"device_address"`
	EntityAddress  []uint64      `json:"entity_address"`
	FeatureAddress uint64        `json:"feature_address"`
	FeatureType    string        `json:"feature_type"`
	FeatureRole    FeatureRoleV1 `json:"feature_role"`
}

// Clone returns a copy whose entity address shares no memory with locator.
func (locator FeatureLocatorV1) Clone() FeatureLocatorV1 {
	locator.EntityAddress = append([]uint64(nil), locator.EntityAddress...)
	return locator
}

// String returns a fixed redacted marker so remote identity and addresses never reach logs.
func (locator FeatureLocatorV1) String() string {
	return "feature_locator_v1:[redacted]"
}

// GoString returns the same redacted marker as String.
func (locator FeatureLocatorV1) GoString() string {
	return locator.String()
}

// Format writes the redacted marker for every verb and flag.
func (locator FeatureLocatorV1) Format(state fmt.State, _ rune) {
	formatRawFeatureV1(state, locator.String())
}

// FeatureTargetV1 is a FeatureLocatorV1 plus the exact function name (1..256 characters) and
// the full operation. Results and failures echo it unchanged.
type FeatureTargetV1 struct {
	RemoteSKI      string        `json:"remote_ski"`
	SHIPID         string        `json:"ship_id"`
	DeviceAddress  string        `json:"device_address"`
	EntityAddress  []uint64      `json:"entity_address"`
	FeatureAddress uint64        `json:"feature_address"`
	FeatureType    string        `json:"feature_type"`
	FeatureRole    FeatureRoleV1 `json:"feature_role"`
	Function       string        `json:"function"`
	Operation      OperationV1   `json:"operation"`
}

// Clone returns a copy whose entity address shares no memory with target.
func (target FeatureTargetV1) Clone() FeatureTargetV1 {
	target.EntityAddress = append([]uint64(nil), target.EntityAddress...)
	return target
}

// Locator drops Function and Operation and returns an independent copy of the address.
func (target FeatureTargetV1) Locator() FeatureLocatorV1 {
	return FeatureLocatorV1{
		RemoteSKI:      target.RemoteSKI,
		SHIPID:         target.SHIPID,
		DeviceAddress:  target.DeviceAddress,
		EntityAddress:  append([]uint64(nil), target.EntityAddress...),
		FeatureAddress: target.FeatureAddress,
		FeatureType:    target.FeatureType,
		FeatureRole:    target.FeatureRole,
	}
}

// String returns a fixed redacted marker so remote identity and addresses never reach logs.
func (target FeatureTargetV1) String() string {
	return "feature_target_v1:[redacted]"
}

// GoString returns the same redacted marker as String.
func (target FeatureTargetV1) GoString() string {
	return target.String()
}

// Format writes the redacted marker for every verb and flag.
func (target FeatureTargetV1) Format(state fmt.State, _ rune) {
	formatRawFeatureV1(state, target.String())
}

// FullOperationsV1 reports which full operations the remote declares possible for a function;
// a false field makes the matching operation unsupported_operation.
type FullOperationsV1 struct {
	Read  bool `json:"read"`
	Write bool `json:"write"`
}

// ConstraintSetV1 carries native function constraints. Bounds and enum values are non-null
// scalars, Unit is the native unit text, cardinalities are counts, and CrossFieldRules holds at
// most 64 opaque rule strings that this package does not evaluate.
type ConstraintSetV1 struct {
	Status          ConstraintStatusV1 `json:"status"`
	EnumValues      []TypedValueV1     `json:"enum_values,omitempty"`
	Minimum         *TypedValueV1      `json:"minimum,omitempty"`
	Maximum         *TypedValueV1      `json:"maximum,omitempty"`
	Step            *TypedValueV1      `json:"step,omitempty"`
	Unit            string             `json:"unit,omitempty"`
	MinCardinality  *uint64            `json:"min_cardinality,omitempty"`
	MaxCardinality  *uint64            `json:"max_cardinality,omitempty"`
	CrossFieldRules []string           `json:"cross_field_rules,omitempty"`
}

// Clone returns a deep copy, including enum values, bounds and rules.
func (constraints ConstraintSetV1) Clone() ConstraintSetV1 {
	if constraints.EnumValues != nil {
		result := make([]TypedValueV1, len(constraints.EnumValues))
		for index := range constraints.EnumValues {
			result[index] = constraints.EnumValues[index].Clone()
		}
		constraints.EnumValues = result
	}
	constraints.Minimum = cloneTypedValuePointerV1(constraints.Minimum)
	constraints.Maximum = cloneTypedValuePointerV1(constraints.Maximum)
	constraints.Step = cloneTypedValuePointerV1(constraints.Step)
	constraints.MinCardinality = cloneUint64PointerV1(constraints.MinCardinality)
	constraints.MaxCardinality = cloneUint64PointerV1(constraints.MaxCardinality)
	constraints.CrossFieldRules = append([]string(nil), constraints.CrossFieldRules...)
	return constraints
}

// FunctionDescriptorV1 describes one function of a feature inventory; Description is free text
// of up to 4096 characters and Function is unique within the inventory.
type FunctionDescriptorV1 struct {
	Function           string           `json:"function"`
	Description        string           `json:"description,omitempty"`
	PossibleOperations FullOperationsV1 `json:"possible_operations"`
	Changeable         ChangeabilityV1  `json:"changeable"`
	Constraints        ConstraintSetV1  `json:"constraints"`
}

// Clone returns a deep copy of descriptor and its constraints.
func (descriptor FunctionDescriptorV1) Clone() FunctionDescriptorV1 {
	descriptor.Constraints = descriptor.Constraints.Clone()
	return descriptor
}

// FeaturesGetRequestV1 is the closed eebus.v1.features.get request for one exact locator.
type FeaturesGetRequestV1 struct {
	Target FeatureLocatorV1 `json:"target"`
}

// Clone returns a deep copy of request.
func (request FeaturesGetRequestV1) Clone() FeaturesGetRequestV1 {
	request.Target = request.Target.Clone()
	return request
}

// FeaturesGetDataV1 is one feature inventory with at most 512 functions. DataHash commits every
// field except Source and DataHash, so a cached copy keeps the hash of its live origin.
type FeaturesGetDataV1 struct {
	Feature       FeatureLocatorV1       `json:"feature"`
	Description   string                 `json:"description,omitempty"`
	Functions     []FunctionDescriptorV1 `json:"functions"`
	Runtime       RuntimeBindingV1       `json:"runtime"`
	DataTimestamp time.Time              `json:"data_timestamp"`
	Source        ObservationSourceV1    `json:"source"`
	DataHash      HashV1                 `json:"data_hash"`
}

// Clone returns a deep copy of the inventory.
func (data FeaturesGetDataV1) Clone() FeaturesGetDataV1 {
	data.Feature = data.Feature.Clone()
	if data.Functions != nil {
		functions := make([]FunctionDescriptorV1, len(data.Functions))
		for index := range data.Functions {
			functions[index] = data.Functions[index].Clone()
		}
		data.Functions = functions
	}
	return data
}

// ComputeDataHash recomputes the DataHash commitment; it ignores Source and DataHash.
func (data FeaturesGetDataV1) ComputeDataHash() (HashV1, error) {
	commitment := data.Clone()
	commitment.Source = ""
	commitment.DataHash = ""
	return CanonicalSHA256V1(commitment)
}

// String returns a fixed redacted marker so remote identity and functions never reach logs.
func (data FeaturesGetDataV1) String() string {
	return "features_get_data_v1:[redacted]"
}

// GoString returns the same redacted marker as String.
func (data FeaturesGetDataV1) GoString() string {
	return data.String()
}

// Format writes the redacted marker for every verb and flag.
func (data FeaturesGetDataV1) Format(state fmt.State, _ rune) {
	formatRawFeatureV1(state, data.String())
}

// FeatureDataGetRequestV1 is the closed eebus.v1.features.data.get request: 1..16 READ targets
// and an optional timeout in milliseconds of at most MaximumReadTimeoutMSV1.
type FeatureDataGetRequestV1 struct {
	Targets   []FeatureTargetV1 `json:"targets"`
	TimeoutMS uint64            `json:"timeout_ms,omitempty"`
}

// Clone returns a deep copy of request and its targets.
func (request FeatureDataGetRequestV1) Clone() FeatureDataGetRequestV1 {
	if request.Targets != nil {
		targets := make([]FeatureTargetV1, len(request.Targets))
		for index := range request.Targets {
			targets[index] = request.Targets[index].Clone()
		}
		request.Targets = targets
	}
	return request
}

// ProtocolMessageV1 is raw evidence of one native message. Classifier is READ, WRITE, REPLY or
// RESULT, CorrelationKey is positive, and Data and ErrorNumber never appear together. Unknown
// keeps up to 256 fields the decoder did not understand; see
// https://github.com/Project-Helianthus/helianthus-docs-eebus/tree/main/protocols.
type ProtocolMessageV1 struct {
	Classifier     string                `json:"classifier"`
	CorrelationKey uint64                `json:"correlation_key"`
	Function       string                `json:"function"`
	Data           *TypedValueV1         `json:"data,omitempty"`
	ErrorNumber    *uint64               `json:"error_number,omitempty"`
	Unknown        []OpaqueObservationV1 `json:"unknown,omitempty"`
}

// Clone returns a deep copy, including data and unknown fields.
func (message ProtocolMessageV1) Clone() ProtocolMessageV1 {
	message.Data = cloneTypedValuePointerV1(message.Data)
	message.ErrorNumber = cloneUint64PointerV1(message.ErrorNumber)
	if message.Unknown != nil {
		unknown := make([]OpaqueObservationV1, len(message.Unknown))
		for index := range message.Unknown {
			unknown[index] = message.Unknown[index].Clone()
		}
		message.Unknown = unknown
	}
	return message
}

// String returns a fixed redacted marker so raw payloads never reach logs.
func (message ProtocolMessageV1) String() string {
	return "protocol_message_v1:[redacted]"
}

// GoString returns the same redacted marker as String.
func (message ProtocolMessageV1) GoString() string {
	return message.String()
}

// Format writes the redacted marker for every verb and flag.
func (message ProtocolMessageV1) Format(state fmt.State, _ rune) {
	formatRawFeatureV1(state, message.String())
}

// OpaqueObservationV1 preserves one undecoded field: Path (up to 1024 characters) locates it,
// Source names its origin, and Value keeps it as a typed value instead of dropping it.
type OpaqueObservationV1 struct {
	Path   string       `json:"path"`
	Source string       `json:"source"`
	Value  TypedValueV1 `json:"value"`
}

// Clone returns a deep copy of observation.
func (observation OpaqueObservationV1) Clone() OpaqueObservationV1 {
	observation.Value = observation.Value.Clone()
	return observation
}

// String returns a fixed redacted marker so unknown raw values never reach logs.
func (observation OpaqueObservationV1) String() string {
	return "opaque_observation_v1:[redacted]"
}

// GoString returns the same redacted marker as String.
func (observation OpaqueObservationV1) GoString() string {
	return observation.String()
}

// Format writes the redacted marker for every verb and flag.
func (observation OpaqueObservationV1) Format(state fmt.State, _ rune) {
	formatRawFeatureV1(state, observation.String())
}

// ReadTokenV1 is a purpose-bound proof of one READ, not a credential. BindingHash commits the
// principal, scope, tier, tool, target, request, runtime binding and before-image the runtime
// keeps for it; the token is rejected after ExpiresAt, after one use unless Reusable, or when
// presented in any other binding.
type ReadTokenV1 struct {
	ReadToken   string    `json:"read_token"`
	Reusable    bool      `json:"reusable"`
	ExpiresAt   time.Time `json:"expires_at"`
	BindingHash HashV1    `json:"binding_hash"`
}

// String returns a fixed redacted marker so token values never reach logs.
func (token ReadTokenV1) String() string {
	return "read_token_v1:[redacted]"
}

// GoString returns the same redacted marker as String.
func (token ReadTokenV1) GoString() string {
	return token.String()
}

// Format writes the redacted marker for every verb and flag.
func (token ReadTokenV1) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, token.String())
}

// ReadObservationV1 is one successful live READ. Raw request and response share the correlation
// key and target function, Value equals the response data, timestamps are UTC and ordered
// requested <= received <= data, and DataHash commits everything except ReadToken and itself.
type ReadObservationV1 struct {
	Target        FeatureTargetV1       `json:"target"`
	Runtime       RuntimeBindingV1      `json:"runtime"`
	RawRequest    ProtocolMessageV1     `json:"raw_request"`
	RawResponse   ProtocolMessageV1     `json:"raw_response"`
	Value         TypedValueV1          `json:"value"`
	Unknown       []OpaqueObservationV1 `json:"unknown,omitempty"`
	RequestedAt   time.Time             `json:"requested_at"`
	ReceivedAt    time.Time             `json:"received_at"`
	DataTimestamp time.Time             `json:"data_timestamp"`
	Source        ObservationSourceV1   `json:"source"`
	ReadToken     ReadTokenV1           `json:"read_token"`
	DataHash      HashV1                `json:"data_hash"`
}

// Clone returns a deep copy, including raw messages and unknown fields.
func (observation ReadObservationV1) Clone() ReadObservationV1 {
	observation.Target = observation.Target.Clone()
	observation.RawRequest = observation.RawRequest.Clone()
	observation.RawResponse = observation.RawResponse.Clone()
	observation.Value = observation.Value.Clone()
	if observation.Unknown != nil {
		unknown := make([]OpaqueObservationV1, len(observation.Unknown))
		for index := range observation.Unknown {
			unknown[index] = observation.Unknown[index].Clone()
		}
		observation.Unknown = unknown
	}
	return observation
}

// ComputeDataHash recomputes the DataHash commitment without ReadToken and DataHash.
func (observation ReadObservationV1) ComputeDataHash() (HashV1, error) {
	commitment := observation.Clone()
	commitment.ReadToken = ReadTokenV1{}
	commitment.DataHash = ""
	return CanonicalSHA256V1(commitment)
}

// String returns a fixed redacted marker so typed values and identities never reach logs.
func (observation ReadObservationV1) String() string {
	return "read_observation_v1:[redacted]"
}

// GoString returns the same redacted marker as String.
func (observation ReadObservationV1) GoString() string {
	return observation.String()
}

// Format writes the redacted marker for every verb and flag.
func (observation ReadObservationV1) Format(state fmt.State, _ rune) {
	formatRawFeatureV1(state, observation.String())
}

// ErrorV1 is the structured raw failure. Message is public-safe text of up to 512 characters
// that never carries backend text or payloads. Retriable says whether the unchanged request may
// be repeated; typed_empty, no_effect and contract validation failures are never retriable.
type ErrorV1 struct {
	Code        ErrorCodeV1     `json:"code"`
	Message     string          `json:"message"`
	Retriable   bool            `json:"retriable"`
	SourceLayer SourceLayerV1   `json:"source_layer"`
	Details     *ErrorDetailsV1 `json:"details,omitempty"`
}

// Clone returns a deep copy, including details.
func (value ErrorV1) Clone() ErrorV1 {
	if value.Details != nil {
		details := value.Details.Clone()
		value.Details = &details
	}
	return value
}

// Error returns "code: message" without details, so it is safe to log.
func (value ErrorV1) Error() string {
	return string(value.Code) + ": " + value.Message
}

// ErrorDetailsV1 adds optional context: TargetIndex (0..15) names the failing READ target, and
// Unknown keeps up to 256 undecoded fields as evidence.
type ErrorDetailsV1 struct {
	TargetIndex    *uint64               `json:"target_index,omitempty"`
	Classification string                `json:"classification,omitempty"`
	Unknown        []OpaqueObservationV1 `json:"unknown,omitempty"`
}

// Clone returns a deep copy of details.
func (details ErrorDetailsV1) Clone() ErrorDetailsV1 {
	details.TargetIndex = cloneUint64PointerV1(details.TargetIndex)
	if details.Unknown != nil {
		unknown := make([]OpaqueObservationV1, len(details.Unknown))
		for index := range details.Unknown {
			unknown[index] = details.Unknown[index].Clone()
		}
		details.Unknown = unknown
	}
	return details
}

// ReadFailureV1 is the failure of one target in a bounded READ; TargetIndex is its position in
// the request and its error is never partial_result.
type ReadFailureV1 struct {
	TargetIndex uint64          `json:"target_index"`
	Target      FeatureTargetV1 `json:"target"`
	Error       ErrorV1         `json:"error"`
}

// Clone returns a deep copy of failure.
func (failure ReadFailureV1) Clone() ReadFailureV1 {
	failure.Target = failure.Target.Clone()
	failure.Error = failure.Error.Clone()
	return failure
}

// String returns a fixed redacted marker so target identities never reach logs.
func (failure ReadFailureV1) String() string {
	return "read_failure_v1:[redacted]"
}

// GoString returns the same redacted marker as String.
func (failure ReadFailureV1) GoString() string {
	return failure.String()
}

// Format writes the redacted marker for every verb and flag.
func (failure ReadFailureV1) Format(state fmt.State, _ rune) {
	formatRawFeatureV1(state, failure.String())
}

// FeatureDataGetDataV1 holds results in request order and per-target failures; together they
// cover every target once. Complete is false exactly when a partial_result error accompanies it.
type FeatureDataGetDataV1 struct {
	Results  []ReadObservationV1 `json:"results"`
	Failures []ReadFailureV1     `json:"failures"`
	Complete bool                `json:"complete"`
}

// Clone returns a deep copy of every result and failure.
func (data FeatureDataGetDataV1) Clone() FeatureDataGetDataV1 {
	if data.Results != nil {
		results := make([]ReadObservationV1, len(data.Results))
		for index := range data.Results {
			results[index] = data.Results[index].Clone()
		}
		data.Results = results
	}
	if data.Failures != nil {
		failures := make([]ReadFailureV1, len(data.Failures))
		for index := range data.Failures {
			failures[index] = data.Failures[index].Clone()
		}
		data.Failures = failures
	}
	return data
}

// String returns a fixed redacted marker so typed values and identities never reach logs.
func (data FeatureDataGetDataV1) String() string {
	return "feature_data_get_data_v1:[redacted]"
}

// GoString returns the same redacted marker as String.
func (data FeatureDataGetDataV1) GoString() string {
	return data.String()
}

// Format writes the redacted marker for every verb and flag.
func (data FeatureDataGetDataV1) Format(state fmt.State, _ rune) {
	formatRawFeatureV1(state, data.String())
}

// ValidateReadAuthorizationV1 accepts only the raw read scope and raw mask tier for one of the
// three non-mutating tools, with auth.Tool equal to tool and a non-blank principal class of at
// most 128 characters; any mismatch is a non-retriable permission_denied error.
func ValidateReadAuthorizationV1(auth ReadAuthorizationV1, tool ToolV1) *ErrorV1 {
	if strings.TrimSpace(auth.PrincipalClass) == "" ||
		utf8.RuneCountInString(auth.PrincipalClass) > 128 ||
		auth.Scope != AuthScopeV1RawRead ||
		auth.Tool != tool ||
		auth.MaskTier != MaskTierRaw ||
		(tool != ToolV1FeaturesGet &&
			tool != ToolV1FeaturesDataGet &&
			tool != ToolV1MutationsGet) {
		return NewErrorV1(
			ErrorCodeV1PermissionDenied,
			"raw READ authorization does not match the required purpose",
			false,
			SourceLayerV1Authorization,
		)
	}
	return nil
}

// ValidateFeaturesGetRequestV1 rejects secret-classified content and incomplete or out-of-range
// locators; it is a pure check that touches no runtime state.
func ValidateFeaturesGetRequestV1(request FeaturesGetRequestV1) *ErrorV1 {
	if terminal := validateCanonicalDocumentV1(request); terminal != nil {
		return terminal
	}
	if err := validateFeatureLocatorV1(request.Target); err != nil {
		return NewErrorV1(ErrorCodeV1InvalidArgument, err.Error(), false, SourceLayerV1Validation)
	}
	return nil
}

// ValidateFeatureDataGetRequestV1 checks the target count, timeout and every target, reporting
// the first failing target index; a non-READ target is unsupported_operation.
func ValidateFeatureDataGetRequestV1(request FeatureDataGetRequestV1) *ErrorV1 {
	if terminal := validateCanonicalDocumentV1(request); terminal != nil {
		return terminal
	}
	if len(request.Targets) < 1 || len(request.Targets) > MaximumReadTargetsV1 {
		return NewErrorV1(
			ErrorCodeV1InvalidArgument,
			"raw READ requires between 1 and 16 targets",
			false,
			SourceLayerV1Validation,
		)
	}
	if request.TimeoutMS > MaximumReadTimeoutMSV1 {
		return NewErrorV1(
			ErrorCodeV1InvalidArgument,
			"raw READ timeout must not exceed 30000 milliseconds",
			false,
			SourceLayerV1Validation,
		)
	}
	for index, target := range request.Targets {
		if target.Operation != OperationV1Read {
			return NewErrorV1(
				ErrorCodeV1UnsupportedOperation,
				fmt.Sprintf("raw target %d does not request READ", index),
				false,
				SourceLayerV1Validation,
			)
		}
		if err := validateFeatureTargetV1(target); err != nil {
			return NewErrorV1(
				ErrorCodeV1InvalidArgument,
				fmt.Sprintf("raw target %d is invalid", index),
				false,
				SourceLayerV1Validation,
			)
		}
	}
	return nil
}

// NewErrorV1 builds an ErrorV1 without details. It does not validate code or layer and does not
// sanitize message, so callers pass only fixed public-safe text.
func NewErrorV1(code ErrorCodeV1, message string, retriable bool, layer SourceLayerV1) *ErrorV1 {
	return &ErrorV1{Code: code, Message: message, Retriable: retriable, SourceLayer: layer}
}

func validateFeatureTargetV1(target FeatureTargetV1) error {
	if err := validateFeatureLocatorV1(target.Locator()); err != nil {
		return err
	}
	if strings.TrimSpace(target.Function) == "" ||
		utf8.RuneCountInString(target.Function) > 256 {
		return errors.New("feature function is required")
	}
	if target.Operation != OperationV1Read && target.Operation != OperationV1Write {
		return errors.New("feature operation is unsupported")
	}
	return nil
}

func validateFeatureLocatorV1(locator FeatureLocatorV1) error {
	if !rawFeatureSKIV1(locator.RemoteSKI) ||
		!rawFeatureBoundedStringV1(locator.SHIPID, 256) ||
		!rawFeatureBoundedStringV1(locator.DeviceAddress, 64) ||
		len(locator.EntityAddress) == 0 || len(locator.EntityAddress) > 16 ||
		locator.FeatureAddress > uint64(typedValueMaximumSafeInteger) ||
		!rawFeatureBoundedStringV1(locator.FeatureType, 128) {
		return errors.New("complete exact feature address and remote identity are required")
	}
	for _, part := range locator.EntityAddress {
		if part > uint64(typedValueMaximumSafeInteger) {
			return errors.New("feature entity address exceeds the safe integer range")
		}
	}
	switch locator.FeatureRole {
	case FeatureRoleV1Client, FeatureRoleV1Server, FeatureRoleV1Special:
	default:
		return errors.New("feature role is unsupported")
	}
	return nil
}

func rawFeatureSKIV1(value string) bool {
	if len(value) != 40 || value != strings.ToLower(value) {
		return false
	}
	for _, current := range value {
		if current < '0' || current > '9' && current < 'a' || current > 'f' {
			return false
		}
	}
	return true
}

func rawFeatureBoundedStringV1(value string, maximum int) bool {
	value = strings.TrimSpace(value)
	return value != "" && utf8.ValidString(value) &&
		utf8.RuneCountInString(value) <= maximum
}

func cloneTypedValuePointerV1(value *TypedValueV1) *TypedValueV1 {
	if value == nil {
		return nil
	}
	cloned := value.Clone()
	return &cloned
}

func cloneUint64PointerV1(value *uint64) *uint64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func formatRawFeatureV1(state fmt.State, value string) {
	_, _ = io.WriteString(state, value)
}
