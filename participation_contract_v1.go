package eebusruntime

import (
	"net/netip"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusevidence"
)

type ParticipationContractVersionV1 string

const ParticipationContractV1 ParticipationContractVersionV1 = "helianthus.eebus.participation.v1"

type ParticipationApplyResultV1 struct {
	StateRevision   uint64                 `json:"state_revision"`
	AppliedRevision uint64                 `json:"applied_revision"`
	State           ParticipationStateV1   `json:"state"`
	Failure         ParticipationFailureV1 `json:"failure"`
}

type ParticipationConfigurationV1 struct {
	Contract         ParticipationContractVersionV1 `json:"contract"`
	Revision         uint64                         `json:"revision"`
	LocalParticipant string                         `json:"local_participant"`
	Enabled          bool                           `json:"enabled"`
	Listener         ParticipationListenerV1        `json:"listener"`
	DiscoveryEnabled bool                           `json:"discovery_enabled"`
	Bindings         []ParticipationBindingV1       `json:"bindings"`
}

type ParticipationListenerV1 struct {
	Interface string         `json:"interface"`
	Address   netip.AddrPort `json:"address"`
}

type ParticipationBindingV1 struct {
	ID              string                      `json:"id"`
	PeerParticipant string                      `json:"peer_participant"`
	LocalCapability string                      `json:"local_capability"`
	PeerCapability  string                      `json:"peer_capability"`
	Selection       string                      `json:"selection"`
	Direction       ParticipationDirectionV1    `json:"direction"`
	Permissions     []ParticipationPermissionV1 `json:"permissions"`
}

type ParticipationDirectionV1 string

const (
	ParticipationDirectionV1Send    ParticipationDirectionV1 = "send"
	ParticipationDirectionV1Receive ParticipationDirectionV1 = "receive"
)

type ParticipationPermissionV1 string

const (
	ParticipationPermissionV1Observe ParticipationPermissionV1 = "observe"
	ParticipationPermissionV1Control ParticipationPermissionV1 = "control"
)

type ParticipationStateV1 string

const (
	ParticipationStateV1Configured   ParticipationStateV1 = "configured"
	ParticipationStateV1Offline      ParticipationStateV1 = "offline"
	ParticipationStateV1Incompatible ParticipationStateV1 = "incompatible"
	ParticipationStateV1Active       ParticipationStateV1 = "active"
	ParticipationStateV1Error        ParticipationStateV1 = "error"
)

type ParticipationFailureV1 string

const (
	ParticipationFailureV1None          ParticipationFailureV1 = "none"
	ParticipationFailureV1Authorization ParticipationFailureV1 = "authorization"
	ParticipationFailureV1Negotiation   ParticipationFailureV1 = "negotiation"
	ParticipationFailureV1Listener      ParticipationFailureV1 = "listener"
	ParticipationFailureV1Persistence   ParticipationFailureV1 = "persistence"
	ParticipationFailureV1Recovery      ParticipationFailureV1 = "recovery"
)

type ParticipationControlOutcomeV1 string

const (
	ParticipationControlOutcomeV1NotRequested ParticipationControlOutcomeV1 = "not_requested"
	ParticipationControlOutcomeV1Rejected     ParticipationControlOutcomeV1 = "rejected"
	ParticipationControlOutcomeV1Accepted     ParticipationControlOutcomeV1 = "accepted"
	ParticipationControlOutcomeV1Unknown      ParticipationControlOutcomeV1 = "outcome_unknown"
)

type ParticipationContributionV1 struct {
	Configuration   ParticipationConfigurationV1        `json:"configuration"`
	AppliedRevision uint64                              `json:"applied_revision"`
	ObservedAt      time.Time                           `json:"observed_at"`
	ListenerState   ParticipationStateV1                `json:"listener_state"`
	Bindings        []ParticipationBindingObservationV1 `json:"bindings"`
}

type ParticipationBindingObservationV1 struct {
	ID             string                        `json:"id"`
	State          ParticipationStateV1          `json:"state"`
	PeerOnline     bool                          `json:"peer_online"`
	Trusted        bool                          `json:"trusted"`
	Compatibility  ParticipationCompatibilityV1  `json:"compatibility"`
	Admission      ParticipationAdmissionV1      `json:"admission"`
	Negotiated     bool                          `json:"negotiated"`
	Granted        []ParticipationPermissionV1   `json:"granted"`
	ControlOutcome ParticipationControlOutcomeV1 `json:"control_outcome"`
	Failure        ParticipationFailureV1        `json:"failure"`
	Provenance     ParticipationProvenanceV1     `json:"provenance"`
	Losses         []ParticipationLossV1         `json:"losses"`
}

type ParticipationProvenanceV1 struct {
	Native           *eebusevidence.EnvelopeV1 `json:"native"`
	TransportVersion *string                   `json:"transport_version"`
	DataModelVersion *string                   `json:"data_model_version"`
}

type ParticipationLossV1 struct {
	NativePath  string                  `json:"native_path"`
	Code        ParticipationLossCodeV1 `json:"code"`
	ControlSafe bool                    `json:"control_safe"`
}

type ParticipationLossCodeV1 string

const (
	ParticipationLossCodeV1Omitted     ParticipationLossCodeV1 = "omitted"
	ParticipationLossCodeV1Transformed ParticipationLossCodeV1 = "transformed"
	ParticipationLossCodeV1Unknown     ParticipationLossCodeV1 = "unknown"
)

type ParticipationCompatibilityV1 string

const (
	ParticipationCompatibilityV1Unknown      ParticipationCompatibilityV1 = "unknown"
	ParticipationCompatibilityV1Compatible   ParticipationCompatibilityV1 = "compatible"
	ParticipationCompatibilityV1Incompatible ParticipationCompatibilityV1 = "incompatible"
)

type ParticipationAdmissionV1 string

const (
	ParticipationAdmissionV1Unknown     ParticipationAdmissionV1 = "unknown"
	ParticipationAdmissionV1Candidate   ParticipationAdmissionV1 = "candidate"
	ParticipationAdmissionV1Qualified   ParticipationAdmissionV1 = "qualified"
	ParticipationAdmissionV1Promoted    ParticipationAdmissionV1 = "promoted"
	ParticipationAdmissionV1Unsupported ParticipationAdmissionV1 = "unsupported"
)
