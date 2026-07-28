package eebusraw

import (
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

type WriteAuthorizationV1 struct {
	PrincipalClass string      `json:"principal_class"`
	Scope          AuthScopeV1 `json:"scope"`
	Tool           ToolV1      `json:"tool"`
	MaskTier       MaskTier    `json:"mask_tier"`
}

type ModeV1 string

const (
	ModeV1Apply ModeV1 = "apply"
	ModeV1Probe ModeV1 = "probe"
)

type MutationStateV1 string

const (
	MutationStateV1Prepared               MutationStateV1 = "prepared"
	MutationStateV1DispatchIntent         MutationStateV1 = "dispatch_intent"
	MutationStateV1ReplyObserved          MutationStateV1 = "reply_observed"
	MutationStateV1VerifyPending          MutationStateV1 = "verify_pending"
	MutationStateV1Applied                MutationStateV1 = "applied"
	MutationStateV1ProbeActive            MutationStateV1 = "probe_active"
	MutationStateV1RollbackIntent         MutationStateV1 = "rollback_intent"
	MutationStateV1RollbackDispatchIntent MutationStateV1 = "rollback_dispatch_intent"
	MutationStateV1RollbackReplyObserved  MutationStateV1 = "rollback_reply_observed"
	MutationStateV1RollbackVerifyPending  MutationStateV1 = "rollback_verify_pending"
	MutationStateV1RolledBack             MutationStateV1 = "rolled_back"
	MutationStateV1OutcomeUnknown         MutationStateV1 = "outcome_unknown"
	MutationStateV1Conflict               MutationStateV1 = "conflict"
	MutationStateV1FailedNoContact        MutationStateV1 = "failed_no_contact"
	MutationStateV1Rejected               MutationStateV1 = "rejected"
	MutationStateV1NoEffect               MutationStateV1 = "no_effect"
)

type ConstraintOverrideV1 struct {
	ProfileID     string    `json:"profile_id"`
	Justification string    `json:"justification"`
	ExpiresAt     time.Time `json:"expires_at"`
}

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

func (FeatureDataSetRequestV1) String() string {
	return "feature_data_set_request_v1:[redacted]"
}

func (request FeatureDataSetRequestV1) GoString() string {
	return request.String()
}

func (request FeatureDataSetRequestV1) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, request.String())
}

type MutationGetRequestV1 struct {
	MutationRef string `json:"mutation_ref"`
}

type MutationRollbackRequestV1 struct {
	MutationRef    string `json:"mutation_ref"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (MutationRollbackRequestV1) String() string {
	return "mutation_rollback_request_v1:[redacted]"
}

func (request MutationRollbackRequestV1) GoString() string {
	return request.String()
}

func (request MutationRollbackRequestV1) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, request.String())
}

type AuditTransitionV1 struct {
	Sequence       uint64          `json:"sequence"`
	State          MutationStateV1 `json:"state"`
	TransitionedAt time.Time       `json:"transitioned_at"`
	Classification string          `json:"classification,omitempty"`
	PreviousHash   *HashV1         `json:"previous_hash"`
	TransitionHash HashV1          `json:"transition_hash"`
}

type ApplyVerificationV1 struct {
	Relation       string    `json:"relation"`
	Verified       bool      `json:"verified"`
	EqualValueHash HashV1    `json:"equal_value_hash"`
	VerifiedAt     time.Time `json:"verified_at"`
}

type RollbackVerificationV1 struct {
	Relation       string    `json:"relation"`
	Verified       bool      `json:"verified"`
	EqualValueHash HashV1    `json:"equal_value_hash"`
	VerifiedAt     time.Time `json:"verified_at"`
}

type ConflictEvidenceV1 struct {
	Relation          string    `json:"relation"`
	Verified          bool      `json:"verified"`
	BeforeHash        HashV1    `json:"before_hash"`
	RequestedHash     HashV1    `json:"requested_hash"`
	ObservedAfterHash HashV1    `json:"observed_after_hash"`
	VerifiedAt        time.Time `json:"verified_at"`
}

type NoContactEvidenceV1 struct {
	RemoteFramesSent   uint64    `json:"remote_frames_sent"`
	LastCompletedPhase string    `json:"last_completed_phase"`
	VerifiedAt         time.Time `json:"verified_at"`
}

type RejectionVerificationV1 struct {
	Relation            string    `json:"relation"`
	Verified            bool      `json:"verified"`
	CorrelatedRejection bool      `json:"correlated_rejection"`
	EqualValueHash      HashV1    `json:"equal_value_hash"`
	VerifiedAt          time.Time `json:"verified_at"`
}

type NoEffectVerificationV1 struct {
	Relation       string    `json:"relation"`
	Verified       bool      `json:"verified"`
	EqualValueHash HashV1    `json:"equal_value_hash"`
	VerifiedAt     time.Time `json:"verified_at"`
}

type OutcomeEvidenceV1 struct {
	PossibleSideEffect  bool            `json:"possible_side_effect"`
	BlindRetryForbidden bool            `json:"blind_retry_forbidden"`
	LastDurableState    MutationStateV1 `json:"last_durable_state"`
	RecordedAt          time.Time       `json:"recorded_at"`
}

type RollbackV1 struct {
	State            MutationStateV1         `json:"state"`
	Before           TypedValueV1            `json:"before"`
	ProtocolAccepted *bool                   `json:"protocol_accepted"`
	ObservedAfter    *TypedValueV1           `json:"observed_after"`
	Error            *ErrorV1                `json:"error,omitempty"`
	Verification     *RollbackVerificationV1 `json:"verification,omitempty"`
}

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
