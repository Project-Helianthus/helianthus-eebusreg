package eebusmutation

import (
	"context"
	"os"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

const rawMutationLabProfileContract = "helianthus.eebus.raw-mutation-lab-profile.v1"

type rawMutationCoordinatorConfig struct {
	StateRoot        string
	Context          context.Context
	RuntimeEpoch     func() uint64
	Now              func() time.Time
	WriterWait       time.Duration
	RecoveryDeadline time.Duration
	ReferenceKey     []byte
	LabProfiles      []rawMutationLabProfile
}

type rawMutationCoordinatorDependencies struct {
	Executor          rawMutationExecutor
	BindingAuthority  rawMutationRuntimeBindingAuthority
	TokenVerifier     rawMutationReadTokenVerifier
	Policy            rawMutationPolicyProvider
	Scheduler         rawMutationScheduler
	Persistence       rawMutationPersistence
	CancelInFlight    func()
	CrashAfterDurable func(eebusraw.MutationStateV1) error
}

type rawMutationExecutor interface {
	FullReadIfCurrent(
		context.Context,
		eebusraw.FeatureTargetV1,
		eebusraw.RuntimeBindingV1,
	) (rawMutationReadResult, *eebusraw.ErrorV1)
	FullWriteIfCurrent(
		context.Context,
		eebusraw.FeatureTargetV1,
		eebusraw.TypedValueV1,
		eebusraw.RuntimeBindingV1,
	) (rawMutationWriteResult, *eebusraw.ErrorV1)
}

type rawMutationRuntimeBindingAuthority interface {
	CurrentRuntimeBinding(
		eebusraw.FeatureTargetV1,
	) (eebusraw.RuntimeBindingV1, *eebusraw.ErrorV1)
}

type rawMutationReadTokenVerifier interface {
	VerifyReadToken(
		context.Context,
		string,
	) (rawMutationReadTokenBinding, *eebusraw.ErrorV1)
	ConsumeReadToken(context.Context, string) *eebusraw.ErrorV1
}

type rawMutationPolicyProvider interface {
	MutationPolicy(
		context.Context,
		eebusraw.FeatureTargetV1,
		eebusraw.TypedValueV1,
		eebusraw.TypedValueV1,
	) (rawMutationPolicyDecision, *eebusraw.ErrorV1)
}

type rawMutationScheduler interface {
	Schedule(time.Time, func()) rawMutationTimer
}

type rawMutationTimer interface {
	Stop() bool
}

type rawMutationNativeScheduler struct{}

func (rawMutationNativeScheduler) Schedule(
	deadline time.Time,
	callback func(),
) rawMutationTimer {
	delay := time.Until(deadline)
	if delay < 0 {
		delay = 0
	}
	return time.AfterFunc(delay, callback)
}

type rawMutationPersistence interface {
	SyncFile(*os.File) error
	SyncDirectory(*os.File) error
}

type rawMutationReadResult struct {
	Value       eebusraw.TypedValueV1
	Runtime     eebusraw.RuntimeBindingV1
	Full        bool
	Trustworthy bool
}

type rawMutationWriteResult struct {
	FrameSent  bool
	Correlated bool
	Accepted   bool
}

type rawMutationReadTokenBinding struct {
	Runtime         eebusraw.RuntimeBindingV1
	Target          eebusraw.FeatureTargetV1
	RequestHash     eebusraw.HashV1
	BeforeImageHash eebusraw.HashV1
	PrincipalClass  string
	Scope           eebusraw.AuthScopeV1
	Tool            eebusraw.ToolV1
	MaskTier        eebusraw.MaskTier
	ExpiresAt       time.Time
	Reusable        bool
}

type rawMutationPolicyDecision struct {
	FullWrite             bool
	Changeability         eebusraw.ChangeabilityV1
	ConstraintsKnown      bool
	LabAllowlisted        bool
	RollbackRepresentable bool
	ConstraintFailures    []string
	SafetyFailures        []string
}

type rawMutationLabProfile struct {
	Contract               string
	ProfileID              string
	Target                 eebusraw.FeatureTargetV1
	AllowedValueHashes     []eebusraw.HashV1
	RollbackValueHash      eebusraw.HashV1
	MaximumProbeTTLSeconds uint64
	SafetyPredicates       []string
	ExpiresAt              time.Time
}

type CoordinatorConfig = rawMutationCoordinatorConfig
type CoordinatorDependencies = rawMutationCoordinatorDependencies
type Coordinator = rawMutationCoordinator
type Executor = rawMutationExecutor
type RuntimeBindingAuthority = rawMutationRuntimeBindingAuthority
type ReadTokenVerifier = rawMutationReadTokenVerifier
type PolicyProvider = rawMutationPolicyProvider
type Scheduler = rawMutationScheduler
type Timer = rawMutationTimer
type Persistence = rawMutationPersistence
type ReadResult = rawMutationReadResult
type WriteResult = rawMutationWriteResult
type ReadTokenBinding = rawMutationReadTokenBinding
type PolicyDecision = rawMutationPolicyDecision
type LabProfile = rawMutationLabProfile

const LabProfileContract = rawMutationLabProfileContract
