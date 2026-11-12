package eebusraw

import (
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

// WriteAuthorizationV1 is the authorization snapshot the transport boundary selects for one raw
// WRITE-class call. Callers build it from the authenticated boundary, never from request data;
// ValidateWriteAuthorizationV1 decides whether it covers a tool.
type WriteAuthorizationV1 struct {
	PrincipalClass string      `json:"principal_class"`
	Scope          AuthScopeV1 `json:"scope"`
	Tool           ToolV1      `json:"tool"`
	MaskTier       MaskTier    `json:"mask_tier"`
}

// ModeV1 selects whether an accepted write stays in place or is rolled back automatically.
type ModeV1 string

const (
	// ModeV1Apply keeps a verified value until an explicit mutations.rollback; ProbeTTLSeconds
	// must be zero.
	ModeV1Apply ModeV1 = "apply"
	// ModeV1Probe needs ProbeTTLSeconds of 1..900; the runtime restores the before-image when
	// the resulting probe deadline passes.
	ModeV1Probe ModeV1 = "probe"
)

// MutationStateV1 is the durable state of one mutation. The coordinator persists each state
// before acting on it, so a restart resumes from the last recorded state with a fresh full READ
// instead of resending a WRITE. The transition graph is in
// https://github.com/Project-Helianthus/helianthus-docs-eebus/tree/main/api.
type MutationStateV1 string

const (
	// MutationStateV1Prepared records the checked request and before-image; nothing was sent.
	MutationStateV1Prepared MutationStateV1 = "prepared"
	// MutationStateV1DispatchIntent is persisted before the WRITE may leave; recovery from it
	// cannot assume the WRITE was not sent.
	MutationStateV1DispatchIntent MutationStateV1 = "dispatch_intent"
	// MutationStateV1ReplyObserved holds the correlated acceptance, true or false, before any
	// readback.
	MutationStateV1ReplyObserved MutationStateV1 = "reply_observed"
	// MutationStateV1VerifyPending waits for the full readback that classifies the reply.
	MutationStateV1VerifyPending MutationStateV1 = "verify_pending"
	// MutationStateV1Applied means a full readback equal to the requested value was verified.
	MutationStateV1Applied MutationStateV1 = "applied"
	// MutationStateV1ProbeActive is an applied probe whose deadline has not yet passed.
	MutationStateV1ProbeActive MutationStateV1 = "probe_active"
	// MutationStateV1RollbackIntent records the decision to restore the before-image before the
	// rollback READ.
	MutationStateV1RollbackIntent MutationStateV1 = "rollback_intent"
	// MutationStateV1RollbackDispatchIntent is persisted before the rollback WRITE may leave.
	MutationStateV1RollbackDispatchIntent MutationStateV1 = "rollback_dispatch_intent"
	// MutationStateV1RollbackReplyObserved holds the correlated rollback acceptance in RollbackV1.
	MutationStateV1RollbackReplyObserved MutationStateV1 = "rollback_reply_observed"
	// MutationStateV1RollbackVerifyPending waits for the readback after the rollback WRITE.
	MutationStateV1RollbackVerifyPending MutationStateV1 = "rollback_verify_pending"
	// MutationStateV1RolledBack is terminal: a readback equal to the before-image was verified.
	MutationStateV1RolledBack MutationStateV1 = "rolled_back"
	// MutationStateV1OutcomeUnknown means a side effect may have occurred without a trustworthy
	// readback; blind retry is forbidden and only readback-driven recovery may leave it.
	MutationStateV1OutcomeUnknown MutationStateV1 = "outcome_unknown"
	// MutationStateV1Conflict is terminal: the readback matched neither the before-image nor the
	// requested value, and further writes are quarantined.
	MutationStateV1Conflict MutationStateV1 = "conflict"
	// MutationStateV1FailedNoContact is terminal and proves that no remote frame was sent.
	MutationStateV1FailedNoContact MutationStateV1 = "failed_no_contact"
	// MutationStateV1Rejected is terminal: a correlated rejection plus a readback equal to the
	// before-image.
	MutationStateV1Rejected MutationStateV1 = "rejected"
	// MutationStateV1NoEffect is terminal after possible-send recovery read back the before-image;
	// it proves no lasting change, not that the WRITE never executed.
	MutationStateV1NoEffect MutationStateV1 = "no_effect"
)

// ConstraintOverrideV1 asks the coordinator to use the already-loaded MutationLabProfileV1 named
// by ProfileID; it cannot create or widen a profile. Justification is 1..1024 characters, and the
// forward-write deadline is the earlier of ExpiresAt and the profile expiry.
type ConstraintOverrideV1 struct {
	ProfileID     string    `json:"profile_id"`
	Justification string    `json:"justification"`
	ExpiresAt     time.Time `json:"expires_at"`
}

// FeatureDataSetRequestV1 is the closed eebus.v1.features.data.set request. Value is the complete
// function value; ReadToken comes from a prior READ of the same target and ExpectedCurrent, when
// set, joins it as a compare-and-swap guard. A repeated IdempotencyKey with the same canonical
// request returns the original mutation without a second frame; with a different request it
// fails with idempotency_conflict. ValidateFeatureDataSetRequestV1 checks the shape.
type FeatureDataSetRequestV1 struct {
	Target              FeatureTargetV1       `json:"target"`
	Value               TypedValueV1          `json:"value"`
	ReadToken           string                `json:"read_token"`
	ExpectedCurrent     *TypedValueV1         `json:"expected_current,omitempty"`
	IdempotencyKey      string                `json:"idempotency_key"`
	Mode                ModeV1                `json:"mode"`
	ProbeTTLSeconds     uint64                `json:"probe_ttl_seconds,omitempty"`
	ConstraintsOverride *ConstraintOverrideV1 `json:"constraints_override,omitempty"`
}

// String returns a fixed redacted marker so the requested value and token are never logged.
func (FeatureDataSetRequestV1) String() string {
	return "feature_data_set_request_v1:[redacted]"
}

// GoString returns the same redacted marker as String.
func (request FeatureDataSetRequestV1) GoString() string {
	return request.String()
}

// Format writes the redacted marker for every verb and flag.
func (request FeatureDataSetRequestV1) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, request.String())
}

// MutationGetRequestV1 reads the status of one mutation. MutationRef is the opaque reference
// returned in MutationV1 and is bound to the principal, tier and runtime epoch that created it;
// a status read never sends a remote frame.
type MutationGetRequestV1 struct {
	MutationRef string `json:"mutation_ref"`
}

// MutationRollbackRequestV1 asks for an explicit restore of a mutation's before-image under the
// same global writer lease as probe expiry. IdempotencyKey follows the data.set rules.
type MutationRollbackRequestV1 struct {
	MutationRef    string `json:"mutation_ref"`
	IdempotencyKey string `json:"idempotency_key"`
}

// String returns a fixed redacted marker so the reference and key are never logged.
func (MutationRollbackRequestV1) String() string {
	return "mutation_rollback_request_v1:[redacted]"
}

// GoString returns the same redacted marker as String.
func (request MutationRollbackRequestV1) GoString() string {
	return request.String()
}

// Format writes the redacted marker for every verb and flag.
func (request MutationRollbackRequestV1) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, request.String())
}

// AuditTransitionV1 is one link of the hash-chained mutation audit. Sequence starts at 1,
// PreviousHash is nil only on the first link, and TransitionHash commits the other fields with
// CanonicalSHA256V1; ValidateMutationV1 recomputes the whole chain of at most 256 links.
type AuditTransitionV1 struct {
	Sequence       uint64          `json:"sequence"`
	State          MutationStateV1 `json:"state"`
	TransitionedAt time.Time       `json:"transitioned_at"`
	Classification string          `json:"classification,omitempty"`
	PreviousHash   *HashV1         `json:"previous_hash"`
	TransitionHash HashV1          `json:"transition_hash"`
}

// ApplyVerificationV1 proves that the readback equals the requested value. Relation is fixed to
// "observed_after_equals_requested" and validators recompute EqualValueHash for both values.
type ApplyVerificationV1 struct {
	Relation       string    `json:"relation"`
	Verified       bool      `json:"verified"`
	EqualValueHash HashV1    `json:"equal_value_hash"`
	VerifiedAt     time.Time `json:"verified_at"`
}

// RollbackVerificationV1 proves that the rollback readback equals the before-image. Relation is
// fixed to "rollback_observed_after_equals_before" and validators recompute EqualValueHash.
type RollbackVerificationV1 struct {
	Relation       string    `json:"relation"`
	Verified       bool      `json:"verified"`
	EqualValueHash HashV1    `json:"equal_value_hash"`
	VerifiedAt     time.Time `json:"verified_at"`
}

// ConflictEvidenceV1 commits three distinct hashes showing that the readback is a third value.
// Relation is fixed to "observed_after_differs_from_before_and_requested".
type ConflictEvidenceV1 struct {
	Relation          string    `json:"relation"`
	Verified          bool      `json:"verified"`
	BeforeHash        HashV1    `json:"before_hash"`
	RequestedHash     HashV1    `json:"requested_hash"`
	ObservedAfterHash HashV1    `json:"observed_after_hash"`
	VerifiedAt        time.Time `json:"verified_at"`
}

// NoContactEvidenceV1 proves that a failed write sent nothing: RemoteFramesSent is always zero
// and LastCompletedPhase names the last local admission phase from the contract's closed list.
type NoContactEvidenceV1 struct {
	RemoteFramesSent   uint64    `json:"remote_frames_sent"`
	LastCompletedPhase string    `json:"last_completed_phase"`
	VerifiedAt         time.Time `json:"verified_at"`
}

// RejectionVerificationV1 pairs a correlated rejection with a readback equal to the before-image.
// Relation is fixed to "observed_after_equals_before".
type RejectionVerificationV1 struct {
	Relation            string    `json:"relation"`
	Verified            bool      `json:"verified"`
	CorrelatedRejection bool      `json:"correlated_rejection"`
	EqualValueHash      HashV1    `json:"equal_value_hash"`
	VerifiedAt          time.Time `json:"verified_at"`
}

// NoEffectVerificationV1 records that possible-send recovery read back the before-image at
// VerifiedAt. Relation is fixed to "observed_after_equals_before".
type NoEffectVerificationV1 struct {
	Relation       string    `json:"relation"`
	Verified       bool      `json:"verified"`
	EqualValueHash HashV1    `json:"equal_value_hash"`
	VerifiedAt     time.Time `json:"verified_at"`
}

// OutcomeEvidenceV1 is retained whenever a WRITE or rollback WRITE may have left without a
// trustworthy result. PossibleSideEffect and BlindRetryForbidden are always true, and
// LastDurableState names the last state persisted before the uncertainty arose.
type OutcomeEvidenceV1 struct {
	PossibleSideEffect  bool            `json:"possible_side_effect"`
	BlindRetryForbidden bool            `json:"blind_retry_forbidden"`
	LastDurableState    MutationStateV1 `json:"last_durable_state"`
	RecordedAt          time.Time       `json:"recorded_at"`
}

// RollbackV1 is the nested record of a rollback. Before must commit to the same hash as the
// enclosing MutationV1.Before, and ProtocolAccepted stays nil until a correlated rollback reply.
type RollbackV1 struct {
	State            MutationStateV1         `json:"state"`
	Before           TypedValueV1            `json:"before"`
	ProtocolAccepted *bool                   `json:"protocol_accepted"`
	ObservedAfter    *TypedValueV1           `json:"observed_after"`
	Error            *ErrorV1                `json:"error,omitempty"`
	Verification     *RollbackVerificationV1 `json:"verification,omitempty"`
}

// MutationV1 is the durable mutation record returned by every mutation tool. ProtocolAccepted
// is nil until a correlated reply, and after possible-send recovery. Exactly the evidence records
// required by State are set, all timestamps are UTC, and the last audit link carries State;
// ValidateMutationV1 rejects any other combination. See
// https://github.com/Project-Helianthus/helianthus-docs-eebus/tree/main/api.
type MutationV1 struct {
	MutationRef           string                   `json:"mutation_ref"`
	State                 MutationStateV1          `json:"state"`
	Mode                  ModeV1                   `json:"mode"`
	Target                FeatureTargetV1          `json:"target"`
	Runtime               RuntimeBindingV1         `json:"runtime"`
	Before                TypedValueV1             `json:"before"`
	Requested             TypedValueV1             `json:"requested"`
	ProtocolAccepted      *bool                    `json:"protocol_accepted"`
	ObservedAfter         *TypedValueV1            `json:"observed_after"`
	Rollback              *RollbackV1              `json:"rollback,omitempty"`
	ProbeDeadline         *time.Time               `json:"probe_deadline,omitempty"`
	CreatedAt             time.Time                `json:"created_at"`
	UpdatedAt             time.Time                `json:"updated_at"`
	Error                 *ErrorV1                 `json:"error,omitempty"`
	ApplyVerification     *ApplyVerificationV1     `json:"apply_verification,omitempty"`
	ConflictEvidence      *ConflictEvidenceV1      `json:"conflict_evidence,omitempty"`
	NoContactEvidence     *NoContactEvidenceV1     `json:"no_contact_evidence,omitempty"`
	RejectionVerification *RejectionVerificationV1 `json:"rejection_verification,omitempty"`
	NoEffectVerification  *NoEffectVerificationV1  `json:"no_effect_verification,omitempty"`
	OutcomeEvidence       *OutcomeEvidenceV1       `json:"outcome_evidence,omitempty"`
	Audit                 []AuditTransitionV1      `json:"audit"`
}

// ValidateWriteAuthorizationV1 accepts only the raw write scope and raw mask tier for
// eebus.v1.features.data.set or eebus.v1.mutations.rollback, with auth.Tool equal to tool and a
// non-blank principal class of at most 128 characters. Any mismatch returns a non-retriable
// permission_denied error; the check is pure and touches no runtime state.
func ValidateWriteAuthorizationV1(auth WriteAuthorizationV1, tool ToolV1) *ErrorV1 {
	if strings.TrimSpace(auth.PrincipalClass) == "" ||
		utf8.RuneCountInString(auth.PrincipalClass) > 128 ||
		auth.Scope != AuthScopeV1RawWrite ||
		auth.Tool != tool ||
		auth.MaskTier != MaskTierRaw ||
		(tool != ToolV1FeaturesDataSet && tool != ToolV1MutationsRollback) {
		return NewErrorV1(
			ErrorCodeV1PermissionDenied,
			"raw WRITE authorization does not match the required purpose",
			false,
			SourceLayerV1Authorization,
		)
	}
	return nil
}
