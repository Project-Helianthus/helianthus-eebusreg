package eebusruntime

import (
	"net/netip"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusevidence"
)

// ParticipationContractVersionV1 names a participation contract version; the validators accept
// only ParticipationContractV1. The contract is documented in
// https://github.com/Project-Helianthus/helianthus-docs-eebus/tree/main/api.
type ParticipationContractVersionV1 string

// ParticipationContractV1 is the Contract value every ParticipationConfigurationV1 must carry.
const ParticipationContractV1 ParticipationContractVersionV1 = "helianthus.eebus.participation.v1"

// ParticipationApplyResultV1 is a driver's report of one ApplyParticipation call. StateRevision
// is the non-zero native state revision after the call. AppliedRevision is the configuration
// revision now operating; after a failed apply it may still be the previous one. Failure is
// ParticipationFailureV1None exactly when State is not error, and an active result needs a
// non-zero AppliedRevision. A returned result is not evidence that a device operated.
type ParticipationApplyResultV1 struct {
	StateRevision   uint64                 `json:"state_revision"`
	AppliedRevision uint64                 `json:"applied_revision"`
	State           ParticipationStateV1   `json:"state"`
	Failure         ParticipationFailureV1 `json:"failure"`
}

// ParticipationConfigurationV1 is the complete participation intent at one non-zero Revision;
// each change raises it by exactly one (see ValidateParticipationChangeV1). LocalParticipant and
// every binding reference are owner-allocated opaque tokens of 32 lowercase hexadecimal
// characters, never derived from a fingerprint, SKI, serial, address or display name. Listener
// must be valid when Enabled is true or any listener field is set. DiscoveryEnabled grants
// neither trust nor control and creates no binding. Bindings holds at most 256 entries with
// unique IDs.
type ParticipationConfigurationV1 struct {
	Contract         ParticipationContractVersionV1 `json:"contract"`
	Revision         uint64                         `json:"revision"`
	LocalParticipant string                         `json:"local_participant"`
	Enabled          bool                           `json:"enabled"`
	Listener         ParticipationListenerV1        `json:"listener"`
	DiscoveryEnabled bool                           `json:"discovery_enabled"`
	Bindings         []ParticipationBindingV1       `json:"bindings"`
}

// ParticipationListenerV1 is the single listener of a participation configuration: an explicit
// interface name of 1 to 64 letters, digits or the characters _.:- that is not a wildcard, and an
// address under the rules of Config.ListenAddress. Validation checks the shape only; whether the
// interface exists is the driver's check.
type ParticipationListenerV1 struct {
	Interface string         `json:"interface"`
	Address   netip.AddrPort `json:"address"`
}

// ParticipationBindingV1 binds LocalCapability to PeerCapability of PeerParticipant, which must
// differ from the local participant, for the semantic result named by Selection. ID, the peer,
// both capabilities, Selection and Direction form the binding's immutable target: changing any
// of them requires a new ID. Permissions holds one or both permissions without duplicates and may
// change under the same ID.
type ParticipationBindingV1 struct {
	ID              string                      `json:"id"`
	PeerParticipant string                      `json:"peer_participant"`
	LocalCapability string                      `json:"local_capability"`
	PeerCapability  string                      `json:"peer_capability"`
	Selection       string                      `json:"selection"`
	Direction       ParticipationDirectionV1    `json:"direction"`
	Permissions     []ParticipationPermissionV1 `json:"permissions"`
}

// ParticipationDirectionV1 orients a binding relative to the local participant.
type ParticipationDirectionV1 string

const (
	// ParticipationDirectionV1Send runs from the local capability to the peer capability; with
	// control, the local participant may control the peer capability.
	ParticipationDirectionV1Send ParticipationDirectionV1 = "send"
	// ParticipationDirectionV1Receive runs from the peer capability to the local one; with
	// control, the peer may control the bound local capability, which grants no local control
	// of the peer.
	ParticipationDirectionV1Receive ParticipationDirectionV1 = "receive"
)

// ParticipationPermissionV1 is a functional permission selected per binding. Permissions are
// independent: control is never inferred from observe, from a mapping or from peer trust.
type ParticipationPermissionV1 string

const (
	// ParticipationPermissionV1Observe permits observing data across the binding.
	ParticipationPermissionV1Observe ParticipationPermissionV1 = "observe"
	// ParticipationPermissionV1Control permits control in the binding's direction. Each use also
	// needs ValidateParticipationControlV1 and the native owner's own authorization.
	ParticipationPermissionV1Control ParticipationPermissionV1 = "control"
)

// ParticipationStateV1 is the observed state of the listener, of one binding or of an apply
// result.
type ParticipationStateV1 string

const (
	// ParticipationStateV1Configured means saved but not operating, including a disabled or
	// restart-pending configuration.
	ParticipationStateV1Configured ParticipationStateV1 = "configured"
	// ParticipationStateV1Offline means the bound peer is unavailable; an offline observation
	// cannot report the peer online.
	ParticipationStateV1Offline ParticipationStateV1 = "offline"
	// ParticipationStateV1Incompatible means compatibility is negatively established; it requires
	// ParticipationCompatibilityV1Incompatible and is invalid as a listener state.
	ParticipationStateV1Incompatible ParticipationStateV1 = "incompatible"
	// ParticipationStateV1Active means operation is established: an enabled applied revision, an
	// active listener, an online trusted peer, a compatible and promoted selection, native
	// negotiation, at least one grant, a hashed native envelope with both versions, and an
	// explicit loss assessment without unknown losses.
	ParticipationStateV1Active ParticipationStateV1 = "active"
	// ParticipationStateV1Error means a failure occurred; it is the only state whose Failure is
	// not ParticipationFailureV1None.
	ParticipationStateV1Error ParticipationStateV1 = "error"
)

// ParticipationFailureV1 is a fixed failure category; failures never carry native payload text.
type ParticipationFailureV1 string

const (
	// ParticipationFailureV1None reports no failure; it is required unless the state is error.
	ParticipationFailureV1None ParticipationFailureV1 = "none"
	// ParticipationFailureV1Authorization reports a refused authorization.
	ParticipationFailureV1Authorization ParticipationFailureV1 = "authorization"
	// ParticipationFailureV1Negotiation reports a failed native negotiation with the peer.
	ParticipationFailureV1Negotiation ParticipationFailureV1 = "negotiation"
	// ParticipationFailureV1Listener reports a listener that could not be established or kept.
	ParticipationFailureV1Listener ParticipationFailureV1 = "listener"
	// ParticipationFailureV1Persistence reports that native participation state could not be
	// saved or loaded.
	ParticipationFailureV1Persistence ParticipationFailureV1 = "persistence"
	// ParticipationFailureV1Recovery reports an interrupted or timed-out apply whose outcome
	// needs fresh inspection before a retry.
	ParticipationFailureV1Recovery ParticipationFailureV1 = "recovery"
)

// ParticipationControlOutcomeV1 is the retained result of the latest control request over a
// binding. It is historical and never a current grant.
type ParticipationControlOutcomeV1 string

const (
	// ParticipationControlOutcomeV1NotRequested means no control was requested.
	ParticipationControlOutcomeV1NotRequested ParticipationControlOutcomeV1 = "not_requested"
	// ParticipationControlOutcomeV1Rejected means the peer rejected control; it requires an error
	// state.
	ParticipationControlOutcomeV1Rejected ParticipationControlOutcomeV1 = "rejected"
	// ParticipationControlOutcomeV1Accepted means the peer accepted control, which is not a
	// verified effect.
	ParticipationControlOutcomeV1Accepted ParticipationControlOutcomeV1 = "accepted"
	// ParticipationControlOutcomeV1Unknown means the outcome could not be established; it
	// requires an error state and never counts as success.
	ParticipationControlOutcomeV1Unknown ParticipationControlOutcomeV1 = "outcome_unknown"
)

// ParticipationContributionV1 is a driver's observation of participation at the required time
// ObservedAt. Configuration is the saved intent and AppliedConfiguration the separately applied
// one: nil with AppliedRevision zero means nothing was applied; otherwise it has revision
// AppliedRevision, not newer than Configuration, the same local participant, and equals
// Configuration when the revisions match. Bindings holds exactly one observation per binding ID
// in the union of saved and applied bindings, so a binding removed from saved intent but still
// operating stays visible. ListenerState is never incompatible and is active only with an
// enabled applied configuration.
type ParticipationContributionV1 struct {
	Configuration        ParticipationConfigurationV1        `json:"configuration"`
	AppliedConfiguration *ParticipationConfigurationV1       `json:"applied_configuration"`
	AppliedRevision      uint64                              `json:"applied_revision"`
	ObservedAt           time.Time                           `json:"observed_at"`
	ListenerState        ParticipationStateV1                `json:"listener_state"`
	Bindings             []ParticipationBindingObservationV1 `json:"bindings"`
}

// ParticipationBindingObservationV1 is the observed state of one binding. ConfigurationRevision
// is the applied revision while the ID remains applied and the saved revision otherwise; a
// saved-only binding cannot be negotiated, granted or active. Granted is the natively negotiated
// subset of the binding's permissions, and Admission is the producer's current admission for the
// selection, not the operator's choice. A non-nil Losses, at most 256 entries, is the producer's
// explicit assessment and empty means no loss; nil means unassessed, which blocks the active
// state. ParticipationStateV1 lists what each State requires.
type ParticipationBindingObservationV1 struct {
	ID                    string                        `json:"id"`
	ConfigurationRevision uint64                        `json:"configuration_revision"`
	State                 ParticipationStateV1          `json:"state"`
	PeerOnline            bool                          `json:"peer_online"`
	Trusted               bool                          `json:"trusted"`
	Compatibility         ParticipationCompatibilityV1  `json:"compatibility"`
	Admission             ParticipationAdmissionV1      `json:"admission"`
	Negotiated            bool                          `json:"negotiated"`
	Granted               []ParticipationPermissionV1   `json:"granted"`
	ControlOutcome        ParticipationControlOutcomeV1 `json:"control_outcome"`
	Failure               ParticipationFailureV1        `json:"failure"`
	Provenance            ParticipationProvenanceV1     `json:"provenance"`
	Losses                []ParticipationLossV1         `json:"losses"`
}

// ParticipationProvenanceV1 is the native evidence behind a binding observation. Native is the
// redacted evidence envelope, validated and limited to 256 objects; nil means the evidence is
// unavailable. TransportVersion and DataModelVersion are observed version labels of 1 to 64
// letters, digits or ._- starting with a letter or digit; nil means unknown. Version labels are
// not compatibility evidence.
type ParticipationProvenanceV1 struct {
	Native           *eebusevidence.EnvelopeV1 `json:"native"`
	TransportVersion *string                   `json:"transport_version"`
	DataModelVersion *string                   `json:"data_model_version"`
}

// ParticipationLossV1 is one projection loss the semantic producer reports for the native
// observation at NativePath, a path of slash-led segments of letters, digits, _ or - of at most
// 256 bytes. ControlSafe comes from the owning qualification; any loss with ControlSafe false
// blocks control eligibility.
type ParticipationLossV1 struct {
	NativePath  string                  `json:"native_path"`
	Code        ParticipationLossCodeV1 `json:"code"`
	ControlSafe bool                    `json:"control_safe"`
}

// ParticipationLossCodeV1 classifies a projection loss.
type ParticipationLossCodeV1 string

const (
	// ParticipationLossCodeV1Omitted means the native data is absent from the projection.
	ParticipationLossCodeV1Omitted ParticipationLossCodeV1 = "omitted"
	// ParticipationLossCodeV1Transformed means the projection represents the native data in a
	// changed form.
	ParticipationLossCodeV1Transformed ParticipationLossCodeV1 = "transformed"
	// ParticipationLossCodeV1Unknown means the loss is not classified; it blocks the active state.
	ParticipationLossCodeV1Unknown ParticipationLossCodeV1 = "unknown"
)

// ParticipationCompatibilityV1 is the native compatibility of a binding's capabilities, versions
// and direction. Unknown never counts as compatible.
type ParticipationCompatibilityV1 string

const (
	// ParticipationCompatibilityV1Unknown means compatibility is not established.
	ParticipationCompatibilityV1Unknown ParticipationCompatibilityV1 = "unknown"
	// ParticipationCompatibilityV1Compatible means compatibility is positively established; the
	// active state requires it.
	ParticipationCompatibilityV1Compatible ParticipationCompatibilityV1 = "compatible"
	// ParticipationCompatibilityV1Incompatible means incompatibility is established.
	ParticipationCompatibilityV1Incompatible ParticipationCompatibilityV1 = "incompatible"
)

// ParticipationAdmissionV1 is the semantic producer's admission of a binding's selection. Only
// ParticipationAdmissionV1Promoted permits the active state; selecting a candidate does not
// elevate it.
type ParticipationAdmissionV1 string

const (
	// ParticipationAdmissionV1Unknown means the producer's admission is not established.
	ParticipationAdmissionV1Unknown ParticipationAdmissionV1 = "unknown"
	// ParticipationAdmissionV1Candidate means the selection is a candidate, not yet qualified.
	ParticipationAdmissionV1Candidate ParticipationAdmissionV1 = "candidate"
	// ParticipationAdmissionV1Qualified means the selection is qualified but not promoted.
	ParticipationAdmissionV1Qualified ParticipationAdmissionV1 = "qualified"
	// ParticipationAdmissionV1Promoted means the producer promoted the selection.
	ParticipationAdmissionV1Promoted ParticipationAdmissionV1 = "promoted"
	// ParticipationAdmissionV1Unsupported means the producer does not support the selection.
	ParticipationAdmissionV1Unsupported ParticipationAdmissionV1 = "unsupported"
)
