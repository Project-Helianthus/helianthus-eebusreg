package main

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

var issue85LegacyToolNamespace = regexp.MustCompile(`(^|[^a-z0-9])ebus\.v1(?:\.|$)`)

func TestIssue85PublicMutationSurfaceIsExactAndDependencyFree(t *testing.T) {
	doc, err := extract(moduleRoot(t))
	if err != nil {
		t.Fatal(err)
	}

	packages := make(map[string]surface, len(doc.Packages))
	for _, pkg := range doc.Packages {
		packages[pkg.Path] = pkg
	}
	root, ok := packages[modulePath]
	if !ok {
		t.Fatalf("public manifest omitted %s", modulePath)
	}
	raw, ok := packages[modulePath+"/eebusraw"]
	if !ok {
		t.Fatalf("public manifest omitted %s/eebusraw", modulePath)
	}

	for _, pkg := range []surface{root, raw} {
		for _, imported := range pkg.Imports {
			for _, forbidden := range []string{
				"github.com/enbility/",
				"github.com/Project-Helianthus/helianthus-eebus-go",
				"github.com/Project-Helianthus/helianthus-ship-go",
				"github.com/Project-Helianthus/helianthus-spine-go",
			} {
				if imported.Path == strings.TrimSuffix(forbidden, "/") ||
					strings.HasPrefix(imported.Path, forbidden) {
					t.Errorf("%s imports mutation implementation dependency %q", pkg.Path, imported.Path)
				}
			}
		}
	}

	rootSymbols := issue85SymbolsByName(root.Symbols)
	const readRuntimeSignature = "type RawFeatureRuntimeV1 interface{ FeaturesDataGet(context.Context, eebusraw.ReadAuthorizationV1, eebusraw.FeatureDataGetRequestV1) (eebusraw.FeatureDataGetDataV1, *eebusraw.ErrorV1); FeaturesGet(context.Context, eebusraw.ReadAuthorizationV1, eebusraw.FeaturesGetRequestV1) (eebusraw.FeaturesGetDataV1, *eebusraw.ErrorV1) }"
	if got := rootSymbols["RawFeatureRuntimeV1"].Signature; got != readRuntimeSignature {
		t.Errorf("RawFeatureRuntimeV1 signature = %q, want unchanged read-only surface %q", got, readRuntimeSignature)
	}
	const mutationRuntimeSignature = "type RawMutationRuntimeV1 interface{ FeaturesDataSet(context.Context, eebusraw.WriteAuthorizationV1, eebusraw.FeatureDataSetRequestV1) (eebusraw.MutationV1, *eebusraw.ErrorV1); MutationsGet(context.Context, eebusraw.ReadAuthorizationV1, eebusraw.MutationGetRequestV1) (eebusraw.MutationV1, *eebusraw.ErrorV1); MutationsRollback(context.Context, eebusraw.WriteAuthorizationV1, eebusraw.MutationRollbackRequestV1) (eebusraw.MutationV1, *eebusraw.ErrorV1) }"
	if got := rootSymbols["RawMutationRuntimeV1"].Signature; got != mutationRuntimeSignature {
		t.Errorf("RawMutationRuntimeV1 signature = %q, want exact separate mutation surface %q", got, mutationRuntimeSignature)
	}
	const runtimeSignature = "type Runtime interface{ PairingState() ([]PairingObservationV1, error); RawFeatureRuntimeV1; Shutdown() error; Snapshot() (SnapshotV1, error); Start(context.Context) error }"
	if got := rootSymbols["Runtime"].Signature; got != runtimeSignature {
		t.Errorf("Runtime signature = %q, want unchanged lifecycle surface %q", got, runtimeSignature)
	}
	if _, exists := rootSymbols["RawMutationCoordinatorV1"]; exists {
		t.Error("public API exposes an undocumented second mutation coordinator surface")
	}

	rawSymbols := issue85SymbolsByName(raw.Symbols)
	for name, signature := range issue85RequiredMutationTypes() {
		got, exists := rawSymbols[name]
		if !exists {
			t.Errorf("eebusraw missing exact mutation type %s", name)
			continue
		}
		if got.Signature != signature || got.TypeForm != "defined" {
			t.Errorf("%s = signature %q form %q, want defined %q", name, got.Signature, got.TypeForm, signature)
		}
	}
	const validateWriteSignature = "func ValidateWriteAuthorizationV1(WriteAuthorizationV1, ToolV1) *ErrorV1"
	if got := rawSymbols["ValidateWriteAuthorizationV1"].Signature; got != validateWriteSignature {
		t.Errorf("ValidateWriteAuthorizationV1 signature = %q, want %q", got, validateWriteSignature)
	}
	for name, value := range issue85RequiredMutationConstants() {
		got, exists := rawSymbols[name]
		if !exists {
			t.Errorf("eebusraw missing exact mutation constant %s=%q", name, value)
			continue
		}
		if got.Kind != "const" || got.Value != `"`+value+`"` {
			t.Errorf("%s = kind %q value %q, want const %q", name, got.Kind, got.Value, value)
		}
	}

	var tools []string
	for _, item := range raw.Symbols {
		if item.Kind == "const" && item.Type == "ToolV1" {
			tools = append(tools, strings.Trim(item.Value, `"`))
		}
	}
	sort.Strings(tools)
	wantTools := []string{
		"eebus.v1.features.data.get",
		"eebus.v1.features.data.set",
		"eebus.v1.features.get",
		"eebus.v1.mutations.get",
		"eebus.v1.mutations.rollback",
	}
	if strings.Join(tools, "\n") != strings.Join(wantTools, "\n") {
		t.Errorf("unreleased raw tool inventory = %v, want exact five %v", tools, wantTools)
	}
}

func TestIssue85PublicSurfaceAddsNoConsumerAliasOrSecretCarrier(t *testing.T) {
	doc, err := extract(moduleRoot(t))
	if err != nil {
		t.Fatal(err)
	}

	var violations []string
	for _, pkg := range doc.Packages {
		if pkg.Path != modulePath && pkg.Path != modulePath+"/eebusraw" &&
			pkg.Path != modulePath+"/eebusevidence" {
			continue
		}
		for _, symbol := range pkg.Symbols {
			lower := strings.ToLower(symbol.Name + " " + symbol.Signature)
			for _, fragment := range []string{
				"candidate_ref",
				"candidateref",
				"graphql",
				"homeassistant",
				"mcp",
				"portal",
				"semantic",
				"rawfeatureruntimev2",
				"mutationv2",
				"compat",
				"legacy",
				"alias",
				"selector",
				"filterdelete",
				"invoke",
				"privatekey",
				"signingkey",
				"truststore",
			} {
				if strings.Contains(lower, fragment) {
					violations = append(violations, pkg.Path+"."+symbol.Name+":"+fragment)
				}
			}
			if issue85LegacyToolNamespace.MatchString(lower) {
				violations = append(violations, pkg.Path+"."+symbol.Name+":ebus.v1")
			}
		}
	}
	sort.Strings(violations)
	if len(violations) != 0 {
		t.Fatalf("public mutation surface leaked excluded concepts: %v", violations)
	}
}

func TestIssue85LegacyNamespaceDetectorDoesNotMisclassifyEEBus(t *testing.T) {
	for _, allowed := range []string{
		"eebus.v1.features.get",
		`const ToolV1FeaturesDataSet ToolV1 = "eebus.v1.features.data.set"`,
	} {
		if issue85LegacyToolNamespace.MatchString(strings.ToLower(allowed)) {
			t.Fatalf("legitimate eeBUS namespace was classified as legacy: %q", allowed)
		}
	}
	for _, forbidden := range []string{
		"ebus.v1.features.get",
		`tool="ebus.v1.mutations.rollback"`,
	} {
		if !issue85LegacyToolNamespace.MatchString(strings.ToLower(forbidden)) {
			t.Fatalf("legacy eBUS namespace escaped detection: %q", forbidden)
		}
	}
}

func issue85SymbolsByName(symbols []symbol) map[string]symbol {
	result := make(map[string]symbol, len(symbols))
	for _, item := range symbols {
		result[item.Name] = item
	}
	return result
}

func issue85RequiredMutationTypes() map[string]string {
	return map[string]string{
		"WriteAuthorizationV1":      `type WriteAuthorizationV1 struct{ PrincipalClass string "json:\"principal_class\""; Scope AuthScopeV1 "json:\"scope\""; Tool ToolV1 "json:\"tool\""; MaskTier MaskTier "json:\"mask_tier\"" }`,
		"ModeV1":                    "type ModeV1 string",
		"MutationStateV1":           "type MutationStateV1 string",
		"ConstraintOverrideV1":      `type ConstraintOverrideV1 struct{ ProfileID string "json:\"profile_id\""; Justification string "json:\"justification\""; ExpiresAt time.Time "json:\"expires_at\"" }`,
		"FeatureDataSetRequestV1":   `type FeatureDataSetRequestV1 struct{ Target FeatureTargetV1 "json:\"target\""; Value TypedValueV1 "json:\"value\""; ReadToken string "json:\"read_token\""; ExpectedCurrent *TypedValueV1 "json:\"expected_current,omitempty\""; IdempotencyKey string "json:\"idempotency_key\""; Mode ModeV1 "json:\"mode\""; ProbeTTLSeconds uint64 "json:\"probe_ttl_seconds,omitempty\""; ConstraintsOverride *ConstraintOverrideV1 "json:\"constraints_override,omitempty\"" }`,
		"MutationGetRequestV1":      `type MutationGetRequestV1 struct{ MutationRef string "json:\"mutation_ref\"" }`,
		"MutationRollbackRequestV1": `type MutationRollbackRequestV1 struct{ MutationRef string "json:\"mutation_ref\""; IdempotencyKey string "json:\"idempotency_key\"" }`,
		"AuditTransitionV1":         `type AuditTransitionV1 struct{ Sequence uint64 "json:\"sequence\""; State MutationStateV1 "json:\"state\""; TransitionedAt time.Time "json:\"transitioned_at\""; Classification string "json:\"classification,omitempty\""; PreviousHash *HashV1 "json:\"previous_hash\""; TransitionHash HashV1 "json:\"transition_hash\"" }`,
		"ApplyVerificationV1":       `type ApplyVerificationV1 struct{ Relation string "json:\"relation\""; Verified bool "json:\"verified\""; EqualValueHash HashV1 "json:\"equal_value_hash\""; VerifiedAt time.Time "json:\"verified_at\"" }`,
		"RollbackVerificationV1":    `type RollbackVerificationV1 struct{ Relation string "json:\"relation\""; Verified bool "json:\"verified\""; EqualValueHash HashV1 "json:\"equal_value_hash\""; VerifiedAt time.Time "json:\"verified_at\"" }`,
		"ConflictEvidenceV1":        `type ConflictEvidenceV1 struct{ Relation string "json:\"relation\""; Verified bool "json:\"verified\""; BeforeHash HashV1 "json:\"before_hash\""; RequestedHash HashV1 "json:\"requested_hash\""; ObservedAfterHash HashV1 "json:\"observed_after_hash\""; VerifiedAt time.Time "json:\"verified_at\"" }`,
		"NoContactEvidenceV1":       `type NoContactEvidenceV1 struct{ RemoteFramesSent uint64 "json:\"remote_frames_sent\""; LastCompletedPhase string "json:\"last_completed_phase\""; VerifiedAt time.Time "json:\"verified_at\"" }`,
		"RejectionVerificationV1":   `type RejectionVerificationV1 struct{ Relation string "json:\"relation\""; Verified bool "json:\"verified\""; CorrelatedRejection bool "json:\"correlated_rejection\""; EqualValueHash HashV1 "json:\"equal_value_hash\""; VerifiedAt time.Time "json:\"verified_at\"" }`,
		"NoEffectVerificationV1":    `type NoEffectVerificationV1 struct{ Relation string "json:\"relation\""; Verified bool "json:\"verified\""; EqualValueHash HashV1 "json:\"equal_value_hash\""; VerifiedAt time.Time "json:\"verified_at\"" }`,
		"OutcomeEvidenceV1":         `type OutcomeEvidenceV1 struct{ PossibleSideEffect bool "json:\"possible_side_effect\""; BlindRetryForbidden bool "json:\"blind_retry_forbidden\""; LastDurableState MutationStateV1 "json:\"last_durable_state\""; RecordedAt time.Time "json:\"recorded_at\"" }`,
		"RollbackV1":                `type RollbackV1 struct{ State MutationStateV1 "json:\"state\""; Before TypedValueV1 "json:\"before\""; ProtocolAccepted *bool "json:\"protocol_accepted\""; ObservedAfter *TypedValueV1 "json:\"observed_after\""; Error *ErrorV1 "json:\"error,omitempty\""; Verification *RollbackVerificationV1 "json:\"verification,omitempty\"" }`,
		"MutationV1":                `type MutationV1 struct{ MutationRef string "json:\"mutation_ref\""; State MutationStateV1 "json:\"state\""; Mode ModeV1 "json:\"mode\""; Target FeatureTargetV1 "json:\"target\""; Runtime RuntimeBindingV1 "json:\"runtime\""; Before TypedValueV1 "json:\"before\""; Requested TypedValueV1 "json:\"requested\""; ProtocolAccepted *bool "json:\"protocol_accepted\""; ObservedAfter *TypedValueV1 "json:\"observed_after\""; Rollback *RollbackV1 "json:\"rollback,omitempty\""; ProbeDeadline *time.Time "json:\"probe_deadline,omitempty\""; CreatedAt time.Time "json:\"created_at\""; UpdatedAt time.Time "json:\"updated_at\""; Error *ErrorV1 "json:\"error,omitempty\""; ApplyVerification *ApplyVerificationV1 "json:\"apply_verification,omitempty\""; ConflictEvidence *ConflictEvidenceV1 "json:\"conflict_evidence,omitempty\""; NoContactEvidence *NoContactEvidenceV1 "json:\"no_contact_evidence,omitempty\""; RejectionVerification *RejectionVerificationV1 "json:\"rejection_verification,omitempty\""; NoEffectVerification *NoEffectVerificationV1 "json:\"no_effect_verification,omitempty\""; OutcomeEvidence *OutcomeEvidenceV1 "json:\"outcome_evidence,omitempty\""; Audit []AuditTransitionV1 "json:\"audit\"" }`,
	}
}

func issue85RequiredMutationConstants() map[string]string {
	return map[string]string{
		"ToolV1FeaturesDataSet":                 "eebus.v1.features.data.set",
		"ToolV1MutationsGet":                    "eebus.v1.mutations.get",
		"ToolV1MutationsRollback":               "eebus.v1.mutations.rollback",
		"AuthScopeV1RawWrite":                   "eebus.raw.write",
		"ModeV1Apply":                           "apply",
		"ModeV1Probe":                           "probe",
		"MutationStateV1Prepared":               "prepared",
		"MutationStateV1DispatchIntent":         "dispatch_intent",
		"MutationStateV1ReplyObserved":          "reply_observed",
		"MutationStateV1VerifyPending":          "verify_pending",
		"MutationStateV1Applied":                "applied",
		"MutationStateV1ProbeActive":            "probe_active",
		"MutationStateV1RollbackIntent":         "rollback_intent",
		"MutationStateV1RollbackDispatchIntent": "rollback_dispatch_intent",
		"MutationStateV1RollbackReplyObserved":  "rollback_reply_observed",
		"MutationStateV1RollbackVerifyPending":  "rollback_verify_pending",
		"MutationStateV1RolledBack":             "rolled_back",
		"MutationStateV1OutcomeUnknown":         "outcome_unknown",
		"MutationStateV1Conflict":               "conflict",
		"MutationStateV1FailedNoContact":        "failed_no_contact",
		"MutationStateV1Rejected":               "rejected",
		"MutationStateV1NoEffect":               "no_effect",
		"ErrorCodeV1ConstraintsUnknown":         "constraints_unknown",
		"ErrorCodeV1ConstraintFailure":          "constraint_failure",
		"ErrorCodeV1StaleReadToken":             "stale_read_token",
		"ErrorCodeV1CASMismatch":                "cas_mismatch",
		"ErrorCodeV1IdempotencyConflict":        "idempotency_conflict",
		"ErrorCodeV1WriterBusy":                 "writer_busy",
		"ErrorCodeV1OutcomeUnknown":             "outcome_unknown",
		"ErrorCodeV1Conflict":                   "conflict",
		"ErrorCodeV1RollbackFailed":             "rollback_failed",
		"ErrorCodeV1NoEffect":                   "no_effect",
	}
}
