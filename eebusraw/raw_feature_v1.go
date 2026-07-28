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
	MaximumReadTargetsV1   = 16
	MaximumReadTimeoutMSV1 = 30000
)

type ToolV1 string

const (
	ToolV1FeaturesGet       ToolV1 = "eebus.v1.features.get"
	ToolV1FeaturesDataGet   ToolV1 = "eebus.v1.features.data.get"
	ToolV1FeaturesDataSet   ToolV1 = "eebus.v1.features.data.set"
	ToolV1MutationsGet      ToolV1 = "eebus.v1.mutations.get"
	ToolV1MutationsRollback ToolV1 = "eebus.v1.mutations.rollback"
)

type AuthScopeV1 string

const (
	AuthScopeV1RawRead  AuthScopeV1 = "eebus.raw.read"
	AuthScopeV1RawWrite AuthScopeV1 = "eebus.raw.write"
)

const MaskTierRaw MaskTier = "raw"

type FeatureRoleV1 string

const (
	FeatureRoleV1Client  FeatureRoleV1 = "client"
	FeatureRoleV1Server  FeatureRoleV1 = "server"
	FeatureRoleV1Special FeatureRoleV1 = "special"
)

type OperationV1 string

const (
	OperationV1Read  OperationV1 = "READ"
	OperationV1Write OperationV1 = "WRITE"
)

type ObservationSourceV1 string

const (
	ObservationSourceV1Live  ObservationSourceV1 = "live"
	ObservationSourceV1Cache ObservationSourceV1 = "cache"
)

type ChangeabilityV1 string

const (
	ChangeabilityV1Unknown ChangeabilityV1 = "unknown"
	ChangeabilityV1False   ChangeabilityV1 = "false"
	ChangeabilityV1True    ChangeabilityV1 = "true"
)

type ConstraintStatusV1 string

const (
	ConstraintStatusV1Unknown ConstraintStatusV1 = "unknown"
	ConstraintStatusV1Known   ConstraintStatusV1 = "known"
)

type ErrorCodeV1 string

const (
	ErrorCodeV1PermissionDenied             ErrorCodeV1 = "permission_denied"
	ErrorCodeV1InvalidArgument              ErrorCodeV1 = "invalid_argument"
	ErrorCodeV1UnsupportedOperation         ErrorCodeV1 = "unsupported_operation"
	ErrorCodeV1PartialOperationForbidden    ErrorCodeV1 = "partial_operation_forbidden"
	ErrorCodeV1ConstraintsUnknown           ErrorCodeV1 = "constraints_unknown"
	ErrorCodeV1ConstraintFailure            ErrorCodeV1 = "constraint_failure"
	ErrorCodeV1StaleReadToken               ErrorCodeV1 = "stale_read_token"
	ErrorCodeV1CASMismatch                  ErrorCodeV1 = "cas_mismatch"
	ErrorCodeV1RuntimeEpochMismatch         ErrorCodeV1 = "runtime_epoch_mismatch"
	ErrorCodeV1ConnectionGenerationMismatch ErrorCodeV1 = "connection_generation_mismatch"
	ErrorCodeV1IdempotencyConflict          ErrorCodeV1 = "idempotency_conflict"
	ErrorCodeV1WriterBusy                   ErrorCodeV1 = "writer_busy"
	ErrorCodeV1Disconnected                 ErrorCodeV1 = "disconnected"
	ErrorCodeV1Timeout                      ErrorCodeV1 = "timeout"
	ErrorCodeV1Cancelled                    ErrorCodeV1 = "cancelled"
	ErrorCodeV1RemoteError                  ErrorCodeV1 = "remote_error"
	ErrorCodeV1DecodeError                  ErrorCodeV1 = "decode_error"
	ErrorCodeV1PartialResult                ErrorCodeV1 = "partial_result"
	ErrorCodeV1OutcomeUnknown               ErrorCodeV1 = "outcome_unknown"
	ErrorCodeV1Conflict                     ErrorCodeV1 = "conflict"
	ErrorCodeV1RollbackFailed               ErrorCodeV1 = "rollback_failed"
	ErrorCodeV1NoEffect                     ErrorCodeV1 = "no_effect"
	ErrorCodeV1NotFound                     ErrorCodeV1 = "not_found"
	ErrorCodeV1SecretDetected               ErrorCodeV1 = "secret_detected"
	ErrorCodeV1Internal                     ErrorCodeV1 = "internal"
)

type SourceLayerV1 string

const (
	SourceLayerV1Authorization  SourceLayerV1 = "eebusreg-runtime"
	SourceLayerV1Validation     SourceLayerV1 = "eebusreg-runtime"
	SourceLayerV1Runtime        SourceLayerV1 = "eebusreg-runtime"
	SourceLayerV1Executor       SourceLayerV1 = "eebus-go-executor"
	SourceLayerV1SpineRoundTrip SourceLayerV1 = "spine-go-round-trip"
	SourceLayerV1Decode         SourceLayerV1 = "eebusreg-runtime"
	SourceLayerV1Remote         SourceLayerV1 = "remote"
)

type ReadAuthorizationV1 struct {
	PrincipalClass string      `json:"principal_class"`
	Scope          AuthScopeV1 `json:"scope"`
	Tool           ToolV1      `json:"tool"`
	MaskTier       MaskTier    `json:"mask_tier"`
}

func (auth ReadAuthorizationV1) String() string {
	return "read_authorization_v1:[redacted]"
}

func (auth ReadAuthorizationV1) GoString() string {
	return auth.String()
}

func (auth ReadAuthorizationV1) Format(state fmt.State, _ rune) {
	formatRawFeatureV1(state, auth.String())
}

type RuntimeBindingV1 struct {
	RuntimeEpoch         uint64 `json:"runtime_epoch"`
	ConnectionGeneration uint64 `json:"connection_generation"`
}

type FeatureLocatorV1 struct {
	RemoteSKI      string        `json:"remote_ski"`
	SHIPID         string        `json:"ship_id"`
	DeviceAddress  string        `json:"device_address"`
	EntityAddress  []uint64      `json:"entity_address"`
	FeatureAddress uint64        `json:"feature_address"`
	FeatureType    string        `json:"feature_type"`
	FeatureRole    FeatureRoleV1 `json:"feature_role"`
}

func (locator FeatureLocatorV1) Clone() FeatureLocatorV1 {
	locator.EntityAddress = append([]uint64(nil), locator.EntityAddress...)
	return locator
}

func (locator FeatureLocatorV1) String() string {
	return "feature_locator_v1:[redacted]"
}

func (locator FeatureLocatorV1) GoString() string {
	return locator.String()
}

func (locator FeatureLocatorV1) Format(state fmt.State, _ rune) {
	formatRawFeatureV1(state, locator.String())
}

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

func (target FeatureTargetV1) Clone() FeatureTargetV1 {
	target.EntityAddress = append([]uint64(nil), target.EntityAddress...)
	return target
}

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

func (target FeatureTargetV1) String() string {
	return "feature_target_v1:[redacted]"
}

func (target FeatureTargetV1) GoString() string {
	return target.String()
}

func (target FeatureTargetV1) Format(state fmt.State, _ rune) {
	formatRawFeatureV1(state, target.String())
}

type FullOperationsV1 struct {
	Read  bool `json:"read"`
	Write bool `json:"write"`
}

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

type FunctionDescriptorV1 struct {
	Function           string           `json:"function"`
	Description        string           `json:"description,omitempty"`
	PossibleOperations FullOperationsV1 `json:"possible_operations"`
	Changeable         ChangeabilityV1  `json:"changeable"`
	Constraints        ConstraintSetV1  `json:"constraints"`
}

func (descriptor FunctionDescriptorV1) Clone() FunctionDescriptorV1 {
	descriptor.Constraints = descriptor.Constraints.Clone()
	return descriptor
}

type FeaturesGetRequestV1 struct {
	Target FeatureLocatorV1 `json:"target"`
}

func (request FeaturesGetRequestV1) Clone() FeaturesGetRequestV1 {
	request.Target = request.Target.Clone()
	return request
}

type FeaturesGetDataV1 struct {
	Feature       FeatureLocatorV1       `json:"feature"`
	Description   string                 `json:"description,omitempty"`
	Functions     []FunctionDescriptorV1 `json:"functions"`
	Runtime       RuntimeBindingV1       `json:"runtime"`
	DataTimestamp time.Time              `json:"data_timestamp"`
	Source        ObservationSourceV1    `json:"source"`
	DataHash      HashV1                 `json:"data_hash"`
}

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

func (data FeaturesGetDataV1) ComputeDataHash() (HashV1, error) {
	commitment := data.Clone()
	commitment.Source = ""
	commitment.DataHash = ""
	return CanonicalSHA256V1(commitment)
}

func (data FeaturesGetDataV1) String() string {
	return "features_get_data_v1:[redacted]"
}

func (data FeaturesGetDataV1) GoString() string {
	return data.String()
}

func (data FeaturesGetDataV1) Format(state fmt.State, _ rune) {
	formatRawFeatureV1(state, data.String())
}

type FeatureDataGetRequestV1 struct {
	Targets   []FeatureTargetV1 `json:"targets"`
	TimeoutMS uint64            `json:"timeout_ms,omitempty"`
}

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

type ProtocolMessageV1 struct {
	Classifier     string                `json:"classifier"`
	CorrelationKey uint64                `json:"correlation_key"`
	Function       string                `json:"function"`
	Data           *TypedValueV1         `json:"data,omitempty"`
	ErrorNumber    *uint64               `json:"error_number,omitempty"`
	Unknown        []OpaqueObservationV1 `json:"unknown,omitempty"`
}

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

func (message ProtocolMessageV1) String() string {
	return "protocol_message_v1:[redacted]"
}

func (message ProtocolMessageV1) GoString() string {
	return message.String()
}

func (message ProtocolMessageV1) Format(state fmt.State, _ rune) {
	formatRawFeatureV1(state, message.String())
}

type OpaqueObservationV1 struct {
	Path   string       `json:"path"`
	Source string       `json:"source"`
	Value  TypedValueV1 `json:"value"`
}

func (observation OpaqueObservationV1) Clone() OpaqueObservationV1 {
	observation.Value = observation.Value.Clone()
	return observation
}

func (observation OpaqueObservationV1) String() string {
	return "opaque_observation_v1:[redacted]"
}

func (observation OpaqueObservationV1) GoString() string {
	return observation.String()
}

func (observation OpaqueObservationV1) Format(state fmt.State, _ rune) {
	formatRawFeatureV1(state, observation.String())
}

type ReadTokenV1 struct {
	ReadToken   string    `json:"read_token"`
	Reusable    bool      `json:"reusable"`
	ExpiresAt   time.Time `json:"expires_at"`
	BindingHash HashV1    `json:"binding_hash"`
}

func (token ReadTokenV1) String() string {
	return "read_token_v1:[redacted]"
}

func (token ReadTokenV1) GoString() string {
	return token.String()
}

func (token ReadTokenV1) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, token.String())
}

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

func (observation ReadObservationV1) ComputeDataHash() (HashV1, error) {
	commitment := observation.Clone()
	commitment.ReadToken = ReadTokenV1{}
	commitment.DataHash = ""
	return CanonicalSHA256V1(commitment)
}

func (observation ReadObservationV1) String() string {
	return "read_observation_v1:[redacted]"
}

func (observation ReadObservationV1) GoString() string {
	return observation.String()
}

func (observation ReadObservationV1) Format(state fmt.State, _ rune) {
	formatRawFeatureV1(state, observation.String())
}

type ErrorV1 struct {
	Code        ErrorCodeV1     `json:"code"`
	Message     string          `json:"message"`
	Retriable   bool            `json:"retriable"`
	SourceLayer SourceLayerV1   `json:"source_layer"`
	Details     *ErrorDetailsV1 `json:"details,omitempty"`
}

func (value ErrorV1) Clone() ErrorV1 {
	if value.Details != nil {
		details := value.Details.Clone()
		value.Details = &details
	}
	return value
}

func (value ErrorV1) Error() string {
	return string(value.Code) + ": " + value.Message
}

type ErrorDetailsV1 struct {
	TargetIndex    *uint64               `json:"target_index,omitempty"`
	Classification string                `json:"classification,omitempty"`
	Unknown        []OpaqueObservationV1 `json:"unknown,omitempty"`
}

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

type ReadFailureV1 struct {
	TargetIndex uint64          `json:"target_index"`
	Target      FeatureTargetV1 `json:"target"`
	Error       ErrorV1         `json:"error"`
}

func (failure ReadFailureV1) Clone() ReadFailureV1 {
	failure.Target = failure.Target.Clone()
	failure.Error = failure.Error.Clone()
	return failure
}

func (failure ReadFailureV1) String() string {
	return "read_failure_v1:[redacted]"
}

func (failure ReadFailureV1) GoString() string {
	return failure.String()
}

func (failure ReadFailureV1) Format(state fmt.State, _ rune) {
	formatRawFeatureV1(state, failure.String())
}

type FeatureDataGetDataV1 struct {
	Results  []ReadObservationV1 `json:"results"`
	Failures []ReadFailureV1     `json:"failures"`
	Complete bool                `json:"complete"`
}

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

func (data FeatureDataGetDataV1) String() string {
	return "feature_data_get_data_v1:[redacted]"
}

func (data FeatureDataGetDataV1) GoString() string {
	return data.String()
}

func (data FeatureDataGetDataV1) Format(state fmt.State, _ rune) {
	formatRawFeatureV1(state, data.String())
}

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

func ValidateFeaturesGetRequestV1(request FeaturesGetRequestV1) *ErrorV1 {
	if terminal := validateCanonicalDocumentV1(request); terminal != nil {
		return terminal
	}
	if err := validateFeatureLocatorV1(request.Target); err != nil {
		return NewErrorV1(ErrorCodeV1InvalidArgument, err.Error(), false, SourceLayerV1Validation)
	}
	return nil
}

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
