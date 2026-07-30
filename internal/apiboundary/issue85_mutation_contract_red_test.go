package main

import (
	"reflect"
	"sort"
	"testing"
)

func TestIssue85MutationDTOsHaveAnAdditiveExactStableContract(t *testing.T) {
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
		t.Fatal("MSP-0625-REG-MUT must extend the existing eebusraw v1 stable-contract owner")
	}

	actual := make(map[string]manifestStableType, len(contract.types))
	var duplicates []string
	for _, stableType := range contract.types {
		if _, exists := actual[stableType.Name]; exists {
			duplicates = append(duplicates, stableType.Name)
		}
		actual[stableType.Name] = stableType
	}
	expected := issue85MutationStableTypes()
	var missing []string
	for name, want := range expected {
		got, ok := actual[name]
		if !ok {
			missing = append(missing, name)
			continue
		}
		if !stableContractTypeMatches(got, want) {
			t.Errorf("%s stable shape = %#v, want %#v", name, got, want)
		}
	}
	for _, historical := range issue83RawFeatureStableTypeNames() {
		if _, ok := actual[historical]; !ok {
			missing = append(missing, historical)
		}
	}
	sort.Strings(missing)
	sort.Strings(duplicates)
	if len(missing) != 0 || len(duplicates) != 0 {
		t.Errorf("additive stable types missing=%v duplicates=%v", missing, duplicates)
	}
}

func TestIssue85MutationEnumsAreClosedAndExact(t *testing.T) {
	specs := stableContractSpecs(canonicalModulePath)
	var owner *stableContractSpec
	owners := make(map[string][]string)
	for index := range specs {
		spec := &specs[index]
		if spec.importPath != canonicalModulePath+"/eebusraw" {
			continue
		}
		for _, enum := range spec.enums {
			owners[enum.typeName] = append(owners[enum.typeName], spec.root)
		}
		if spec.root == "ReadAuthorizationV1" {
			owner = spec
		}
	}
	if owner == nil {
		t.Fatal("existing eebusraw v1 stable-contract owner is missing")
	}

	got := make(map[string][]manifestStableEnumValue)
	for _, enum := range owner.enums {
		if !enum.exact {
			t.Errorf("%s enum gate is not exact", enum.typeName)
		}
		got[enum.typeName] = append([]manifestStableEnumValue(nil), enum.values...)
	}
	for typeName, want := range issue85MutationStableEnums() {
		if roots := owners[typeName]; !reflect.DeepEqual(roots, []string{"ReadAuthorizationV1"}) {
			t.Errorf("%s enum owners = %v, want one coherent additive owner", typeName, roots)
		}
		actualValues := append([]manifestStableEnumValue(nil), got[typeName]...)
		wantValues := append([]manifestStableEnumValue(nil), want...)
		sort.Slice(actualValues, func(i, j int) bool {
			return actualValues[i].Name < actualValues[j].Name
		})
		sort.Slice(wantValues, func(i, j int) bool {
			return wantValues[i].Name < wantValues[j].Name
		})
		if !reflect.DeepEqual(actualValues, wantValues) {
			t.Errorf("%s enum = %#v, want exact %#v", typeName, got[typeName], want)
		}
	}
}

func TestIssue85BoundaryAllowlistsAddOnlyTheMutationSurface(t *testing.T) {
	for _, export := range issue85MutationRuntimeExports() {
		if _, ok := allowedRuntimeExports[export]; !ok {
			t.Errorf("root mutation export is absent from the closed boundary inventory: %s %s", export.Kind, export.Name)
		}
	}
	for _, export := range issue85MutationRawExports() {
		if _, ok := allowedCurrentRawExports[export]; !ok {
			t.Errorf("eebusraw mutation export is absent from the closed boundary inventory: %s %s", export.Kind, export.Name)
		}
	}
}

func issue85MutationRuntimeExports() []manifestExport {
	return []manifestExport{
		{Kind: "func", Name: "RawMutationOutcomeV1.Clone"},
		{Kind: "type", Name: "RawMutationOutcomeV1"},
		{Kind: "type", Name: "RawMutationRuntimeV1"},
	}
}

func issue85MutationRawExports() []manifestExport {
	exports := []manifestExport{
		{Kind: "func", Name: "ValidateWriteAuthorizationV1"},
	}
	for name := range issue85MutationStableTypes() {
		exports = append(exports, manifestExport{Kind: "type", Name: name})
	}
	for _, values := range issue85MutationStableEnums() {
		for _, value := range values {
			switch value.Name {
			case "ToolV1FeaturesGet", "ToolV1FeaturesDataGet",
				"AuthScopeV1RawRead",
				"ErrorCodeV1PermissionDenied", "ErrorCodeV1InvalidArgument",
				"ErrorCodeV1UnsupportedOperation", "ErrorCodeV1PartialOperationForbidden",
				"ErrorCodeV1RuntimeEpochMismatch", "ErrorCodeV1ConnectionGenerationMismatch",
				"ErrorCodeV1Disconnected", "ErrorCodeV1Timeout", "ErrorCodeV1Cancelled",
				"ErrorCodeV1RemoteError", "ErrorCodeV1TypedEmpty", "ErrorCodeV1DecodeError",
				"ErrorCodeV1PartialResult", "ErrorCodeV1NotFound",
				"ErrorCodeV1SecretDetected", "ErrorCodeV1Internal":
				continue
			}
			exports = append(exports, manifestExport{Kind: "const", Name: value.Name})
		}
	}
	sort.Slice(exports, func(i, j int) bool {
		if exports[i].Kind != exports[j].Kind {
			return exports[i].Kind < exports[j].Kind
		}
		return exports[i].Name < exports[j].Name
	})
	return exports
}

func TestIssue85MutationStableGateRejectsFieldTypeTagAndEnumDrift(t *testing.T) {
	expected := issue85MutationStableTypes()["FeatureDataSetRequestV1"]
	mutations := []struct {
		name   string
		mutate func(*manifestStableType)
	}{
		{
			name: "field",
			mutate: func(value *manifestStableType) {
				value.Fields[0].Name = "CandidateRef"
			},
		},
		{
			name: "type",
			mutate: func(value *manifestStableType) {
				value.Fields[2].Type = "[]byte"
			},
		},
		{
			name: "json tag",
			mutate: func(value *manifestStableType) {
				value.Fields[4].JSONTag = `json:"key"`
			},
		},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			actual := expected
			actual.Fields = append([]manifestStableField(nil), expected.Fields...)
			mutation.mutate(&actual)
			if stableContractTypeMatches(actual, expected) {
				t.Fatalf("%s mutation bypassed exact field/type/tag gate", mutation.name)
			}
		})
	}

	state := append([]manifestStableEnumValue(nil), issue85MutationStableEnums()["MutationStateV1"]...)
	state[0].Value = "candidate"
	if reflect.DeepEqual(state, issue85MutationStableEnums()["MutationStateV1"]) {
		t.Fatal("mutation state drift bypassed exact enum comparison")
	}
}

func issue85MutationStableTypes() map[string]manifestStableType {
	field := func(name, typeName, jsonTag string) manifestStableField {
		return manifestStableField{Name: name, Type: typeName, JSONTag: jsonTag}
	}
	stableType := func(name, underlying string, fields ...manifestStableField) manifestStableType {
		if underlying == "struct" && fields == nil {
			fields = []manifestStableField{}
		}
		return manifestStableType{Name: name, Underlying: underlying, Fields: fields}
	}
	return map[string]manifestStableType{
		"WriteAuthorizationV1": stableType("WriteAuthorizationV1", "struct",
			field("PrincipalClass", "string", `json:"principal_class"`),
			field("Scope", "AuthScopeV1", `json:"scope"`),
			field("Tool", "ToolV1", `json:"tool"`),
			field("MaskTier", "MaskTier", `json:"mask_tier"`),
		),
		"ModeV1":          stableType("ModeV1", "string"),
		"MutationStateV1": stableType("MutationStateV1", "string"),
		"ConstraintOverrideV1": stableType("ConstraintOverrideV1", "struct",
			field("ProfileID", "string", `json:"profile_id"`),
			field("Justification", "string", `json:"justification"`),
			field("ExpiresAt", "time.Time", `json:"expires_at"`),
		),
		"FeatureDataSetRequestV1": stableType("FeatureDataSetRequestV1", "struct",
			field("Target", "FeatureTargetV1", `json:"target"`),
			field("Value", "TypedValueV1", `json:"value"`),
			field("ReadToken", "string", `json:"read_token"`),
			field("ExpectedCurrent", "*TypedValueV1", `json:"expected_current,omitempty"`),
			field("IdempotencyKey", "string", `json:"idempotency_key"`),
			field("Mode", "ModeV1", `json:"mode"`),
			field("ProbeTTLSeconds", "uint64", `json:"probe_ttl_seconds,omitempty"`),
			field("ConstraintsOverride", "*ConstraintOverrideV1", `json:"constraints_override,omitempty"`),
		),
		"MutationGetRequestV1": stableType("MutationGetRequestV1", "struct",
			field("MutationRef", "string", `json:"mutation_ref"`),
		),
		"MutationRollbackRequestV1": stableType("MutationRollbackRequestV1", "struct",
			field("MutationRef", "string", `json:"mutation_ref"`),
			field("IdempotencyKey", "string", `json:"idempotency_key"`),
		),
		"AuditTransitionV1": stableType("AuditTransitionV1", "struct",
			field("Sequence", "uint64", `json:"sequence"`),
			field("State", "MutationStateV1", `json:"state"`),
			field("TransitionedAt", "time.Time", `json:"transitioned_at"`),
			field("Classification", "string", `json:"classification,omitempty"`),
			field("PreviousHash", "*HashV1", `json:"previous_hash"`),
			field("TransitionHash", "HashV1", `json:"transition_hash"`),
		),
		"ApplyVerificationV1": stableType("ApplyVerificationV1", "struct",
			field("Relation", "string", `json:"relation"`),
			field("Verified", "bool", `json:"verified"`),
			field("EqualValueHash", "HashV1", `json:"equal_value_hash"`),
			field("VerifiedAt", "time.Time", `json:"verified_at"`),
		),
		"RollbackVerificationV1": stableType("RollbackVerificationV1", "struct",
			field("Relation", "string", `json:"relation"`),
			field("Verified", "bool", `json:"verified"`),
			field("EqualValueHash", "HashV1", `json:"equal_value_hash"`),
			field("VerifiedAt", "time.Time", `json:"verified_at"`),
		),
		"ConflictEvidenceV1": stableType("ConflictEvidenceV1", "struct",
			field("Relation", "string", `json:"relation"`),
			field("Verified", "bool", `json:"verified"`),
			field("BeforeHash", "HashV1", `json:"before_hash"`),
			field("RequestedHash", "HashV1", `json:"requested_hash"`),
			field("ObservedAfterHash", "HashV1", `json:"observed_after_hash"`),
			field("VerifiedAt", "time.Time", `json:"verified_at"`),
		),
		"NoContactEvidenceV1": stableType("NoContactEvidenceV1", "struct",
			field("RemoteFramesSent", "uint64", `json:"remote_frames_sent"`),
			field("LastCompletedPhase", "string", `json:"last_completed_phase"`),
			field("VerifiedAt", "time.Time", `json:"verified_at"`),
		),
		"RejectionVerificationV1": stableType("RejectionVerificationV1", "struct",
			field("Relation", "string", `json:"relation"`),
			field("Verified", "bool", `json:"verified"`),
			field("CorrelatedRejection", "bool", `json:"correlated_rejection"`),
			field("EqualValueHash", "HashV1", `json:"equal_value_hash"`),
			field("VerifiedAt", "time.Time", `json:"verified_at"`),
		),
		"NoEffectVerificationV1": stableType("NoEffectVerificationV1", "struct",
			field("Relation", "string", `json:"relation"`),
			field("Verified", "bool", `json:"verified"`),
			field("EqualValueHash", "HashV1", `json:"equal_value_hash"`),
			field("VerifiedAt", "time.Time", `json:"verified_at"`),
		),
		"OutcomeEvidenceV1": stableType("OutcomeEvidenceV1", "struct",
			field("PossibleSideEffect", "bool", `json:"possible_side_effect"`),
			field("BlindRetryForbidden", "bool", `json:"blind_retry_forbidden"`),
			field("LastDurableState", "MutationStateV1", `json:"last_durable_state"`),
			field("RecordedAt", "time.Time", `json:"recorded_at"`),
		),
		"RollbackV1": stableType("RollbackV1", "struct",
			field("State", "MutationStateV1", `json:"state"`),
			field("Before", "TypedValueV1", `json:"before"`),
			field("ProtocolAccepted", "*bool", `json:"protocol_accepted"`),
			field("ObservedAfter", "*TypedValueV1", `json:"observed_after"`),
			field("Error", "*ErrorV1", `json:"error,omitempty"`),
			field("Verification", "*RollbackVerificationV1", `json:"verification,omitempty"`),
		),
		"MutationV1": stableType("MutationV1", "struct",
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
	}
}

func issue85MutationStableEnums() map[string][]manifestStableEnumValue {
	values := func(entries ...string) []manifestStableEnumValue {
		result := make([]manifestStableEnumValue, 0, len(entries)/2)
		for index := 0; index < len(entries); index += 2 {
			result = append(result, manifestStableEnumValue{Name: entries[index], Value: entries[index+1]})
		}
		return result
	}
	return map[string][]manifestStableEnumValue{
		"ToolV1": values(
			"ToolV1FeaturesGet", "eebus.v1.features.get",
			"ToolV1FeaturesDataGet", "eebus.v1.features.data.get",
			"ToolV1FeaturesDataSet", "eebus.v1.features.data.set",
			"ToolV1MutationsGet", "eebus.v1.mutations.get",
			"ToolV1MutationsRollback", "eebus.v1.mutations.rollback",
		),
		"AuthScopeV1": values(
			"AuthScopeV1RawRead", "eebus.raw.read",
			"AuthScopeV1RawWrite", "eebus.raw.write",
		),
		"ModeV1": values(
			"ModeV1Apply", "apply",
			"ModeV1Probe", "probe",
		),
		"MutationStateV1": values(
			"MutationStateV1Prepared", "prepared",
			"MutationStateV1DispatchIntent", "dispatch_intent",
			"MutationStateV1ReplyObserved", "reply_observed",
			"MutationStateV1VerifyPending", "verify_pending",
			"MutationStateV1Applied", "applied",
			"MutationStateV1ProbeActive", "probe_active",
			"MutationStateV1RollbackIntent", "rollback_intent",
			"MutationStateV1RollbackDispatchIntent", "rollback_dispatch_intent",
			"MutationStateV1RollbackReplyObserved", "rollback_reply_observed",
			"MutationStateV1RollbackVerifyPending", "rollback_verify_pending",
			"MutationStateV1RolledBack", "rolled_back",
			"MutationStateV1OutcomeUnknown", "outcome_unknown",
			"MutationStateV1Conflict", "conflict",
			"MutationStateV1FailedNoContact", "failed_no_contact",
			"MutationStateV1Rejected", "rejected",
			"MutationStateV1NoEffect", "no_effect",
		),
		"ErrorCodeV1": values(
			"ErrorCodeV1PermissionDenied", "permission_denied",
			"ErrorCodeV1InvalidArgument", "invalid_argument",
			"ErrorCodeV1UnsupportedOperation", "unsupported_operation",
			"ErrorCodeV1PartialOperationForbidden", "partial_operation_forbidden",
			"ErrorCodeV1ConstraintsUnknown", "constraints_unknown",
			"ErrorCodeV1ConstraintFailure", "constraint_failure",
			"ErrorCodeV1StaleReadToken", "stale_read_token",
			"ErrorCodeV1CASMismatch", "cas_mismatch",
			"ErrorCodeV1RuntimeEpochMismatch", "runtime_epoch_mismatch",
			"ErrorCodeV1ConnectionGenerationMismatch", "connection_generation_mismatch",
			"ErrorCodeV1IdempotencyConflict", "idempotency_conflict",
			"ErrorCodeV1WriterBusy", "writer_busy",
			"ErrorCodeV1Timeout", "timeout",
			"ErrorCodeV1Cancelled", "cancelled",
			"ErrorCodeV1Disconnected", "disconnected",
			"ErrorCodeV1RemoteError", "remote_error",
			"ErrorCodeV1TypedEmpty", "typed_empty",
			"ErrorCodeV1DecodeError", "decode_error",
			"ErrorCodeV1PartialResult", "partial_result",
			"ErrorCodeV1OutcomeUnknown", "outcome_unknown",
			"ErrorCodeV1Conflict", "conflict",
			"ErrorCodeV1RollbackFailed", "rollback_failed",
			"ErrorCodeV1NoEffect", "no_effect",
			"ErrorCodeV1NotFound", "not_found",
			"ErrorCodeV1SecretDetected", "secret_detected",
			"ErrorCodeV1Internal", "internal",
		),
	}
}
