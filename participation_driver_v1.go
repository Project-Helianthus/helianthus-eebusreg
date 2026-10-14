package eebusruntime

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
)

type ParticipationDriverV1 interface {
	Participation(context.Context) (ParticipationContributionV1, error)
	ApplyParticipation(context.Context, ParticipationApplyRequestV1) (ParticipationApplyResultV1, error)
	OperatorAdmin() AdminV1
}

type ParticipationApplyRequestV1 struct {
	MutationPreconditionV1
	Configuration ParticipationConfigurationV1 `json:"configuration"`
}

var participationIDV1 = regexp.MustCompile(`^[0-9a-f]{32}$`)
var participationInterfaceV1 = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)
var participationVersionV1 = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var participationPathV1 = regexp.MustCompile(`^(/[A-Za-z0-9_-]+)+$`)

func (configuration ParticipationConfigurationV1) Validate() error {
	if configuration.Contract != ParticipationContractV1 || configuration.Revision == 0 {
		return errors.New("participation contract or revision is invalid")
	}
	if !participationIDV1.MatchString(configuration.LocalParticipant) {
		return errors.New("local participant reference is invalid")
	}
	listener := configuration.Listener
	if configuration.Enabled || listener != (ParticipationListenerV1{}) {
		if listener.Interface != strings.TrimSpace(listener.Interface) || !participationInterfaceV1.MatchString(listener.Interface) || runtimeWildcard(listener.Interface) {
			return errors.New("participation listener interface is invalid")
		}
		if err := validateRuntimeListenAddress(listener.Address); err != nil {
			return errors.New("participation listener address is invalid")
		}
	}
	if len(configuration.Bindings) > 256 {
		return errors.New("participation binding limit exceeded")
	}
	seen := make(map[string]bool, len(configuration.Bindings))
	for _, binding := range configuration.Bindings {
		for _, id := range []string{binding.ID, binding.PeerParticipant, binding.LocalCapability, binding.PeerCapability, binding.Selection} {
			if !participationIDV1.MatchString(id) {
				return errors.New("participation binding reference is invalid")
			}
		}
		if seen[binding.ID] || binding.PeerParticipant == configuration.LocalParticipant {
			return errors.New("participation binding identity is invalid")
		}
		seen[binding.ID] = true
		if binding.Direction != ParticipationDirectionV1Send && binding.Direction != ParticipationDirectionV1Receive {
			return errors.New("participation binding direction is invalid")
		}
		if len(binding.Permissions) == 0 || !participationPermissionsValidV1(binding.Permissions) {
			return errors.New("participation binding permissions are invalid")
		}
	}
	return nil
}

func ValidateParticipationChangeV1(previous, next ParticipationConfigurationV1) error {
	if err := previous.Validate(); err != nil {
		return err
	}
	if err := next.Validate(); err != nil {
		return err
	}
	if previous.Revision == ^uint64(0) || next.Revision != previous.Revision+1 {
		return errors.New("participation configuration revision conflict")
	}
	if next.LocalParticipant != previous.LocalParticipant {
		return errors.New("participation local identity replacement requires separate reset")
	}
	old := make(map[string]ParticipationBindingV1, len(previous.Bindings))
	for _, binding := range previous.Bindings {
		old[binding.ID] = binding
	}
	for _, binding := range next.Bindings {
		prior, exists := old[binding.ID]
		if !exists {
			continue
		}
		if prior.PeerParticipant != binding.PeerParticipant || prior.LocalCapability != binding.LocalCapability || prior.PeerCapability != binding.PeerCapability || prior.Selection != binding.Selection || prior.Direction != binding.Direction {
			return errors.New("participation binding replacement requires a new identity")
		}
	}
	return nil
}

func (contribution ParticipationContributionV1) Validate() error {
	configuration := contribution.Configuration
	if err := configuration.Validate(); err != nil {
		return err
	}
	if contribution.ObservedAt.IsZero() || contribution.AppliedRevision > configuration.Revision {
		return errors.New("participation observation revision or timestamp is invalid")
	}
	if !participationStateValidV1(contribution.ListenerState) || contribution.ListenerState == ParticipationStateV1Incompatible {
		return errors.New("participation listener state is invalid")
	}
	if contribution.ListenerState == ParticipationStateV1Active && (!configuration.Enabled || contribution.AppliedRevision != configuration.Revision) {
		return errors.New("participation listener is not applied")
	}
	if len(contribution.Bindings) != len(configuration.Bindings) {
		return errors.New("participation observations must preserve every configured binding")
	}
	configured := make(map[string]ParticipationBindingV1, len(configuration.Bindings))
	for _, binding := range configuration.Bindings {
		configured[binding.ID] = binding
	}
	seen := make(map[string]bool, len(contribution.Bindings))
	for _, observation := range contribution.Bindings {
		binding, exists := configured[observation.ID]
		if !exists || seen[observation.ID] {
			return errors.New("participation observation identity is invalid")
		}
		seen[observation.ID] = true
		if !participationStateValidV1(observation.State) || !participationPermissionsValidV1(observation.Granted) {
			return errors.New("participation observation state or grant is invalid")
		}
		for _, grant := range observation.Granted {
			if !slices.Contains(binding.Permissions, grant) {
				return errors.New("participation grant exceeds configured permission")
			}
		}
		switch observation.Compatibility {
		case ParticipationCompatibilityV1Unknown, ParticipationCompatibilityV1Compatible, ParticipationCompatibilityV1Incompatible:
		default:
			return errors.New("participation compatibility is invalid")
		}
		switch observation.Admission {
		case ParticipationAdmissionV1Unknown, ParticipationAdmissionV1Candidate, ParticipationAdmissionV1Qualified, ParticipationAdmissionV1Promoted, ParticipationAdmissionV1Unsupported:
		default:
			return errors.New("participation admission is invalid")
		}
		switch observation.ControlOutcome {
		case ParticipationControlOutcomeV1NotRequested, ParticipationControlOutcomeV1Rejected, ParticipationControlOutcomeV1Accepted, ParticipationControlOutcomeV1Unknown:
		default:
			return errors.New("participation control outcome is invalid")
		}
		switch observation.Failure {
		case ParticipationFailureV1None, ParticipationFailureV1Authorization, ParticipationFailureV1Negotiation, ParticipationFailureV1Listener, ParticipationFailureV1Persistence, ParticipationFailureV1Recovery:
		default:
			return errors.New("participation failure is invalid")
		}
		failed := observation.Failure != ParticipationFailureV1None
		if failed != (observation.State == ParticipationStateV1Error) {
			return errors.New("participation failure must have an error state")
		}
		if (observation.ControlOutcome == ParticipationControlOutcomeV1Rejected || observation.ControlOutcome == ParticipationControlOutcomeV1Unknown) && !failed {
			return errors.New("participation rejected or uncertain control requires an error state")
		}
		if observation.State == ParticipationStateV1Offline && observation.PeerOnline {
			return errors.New("participation offline state contradicts peer observation")
		}
		if observation.State == ParticipationStateV1Incompatible && observation.Compatibility != ParticipationCompatibilityV1Incompatible {
			return errors.New("participation incompatible state contradicts capability observation")
		}
		nativePresent := observation.Provenance.Native != nil
		if nativePresent {
			if len(observation.Provenance.Native.Objects) > 256 {
				return errors.New("participation native evidence object limit exceeded")
			}
			if err := observation.Provenance.Native.Validate(); err != nil {
				return errors.New("participation native provenance is invalid")
			}
		}
		for _, version := range []*string{observation.Provenance.TransportVersion, observation.Provenance.DataModelVersion} {
			if version != nil && !participationVersionV1.MatchString(*version) {
				return errors.New("participation native version is invalid")
			}
		}
		if len(observation.Losses) > 256 {
			return errors.New("participation loss limit exceeded")
		}
		unknownLoss := false
		for _, loss := range observation.Losses {
			if len(loss.NativePath) > 256 || !participationPathV1.MatchString(loss.NativePath) {
				return errors.New("participation native loss path is invalid")
			}
			switch loss.Code {
			case ParticipationLossCodeV1Omitted, ParticipationLossCodeV1Transformed:
			case ParticipationLossCodeV1Unknown:
				unknownLoss = true
			default:
				return errors.New("participation loss code is invalid")
			}
		}
		if observation.State == ParticipationStateV1Active {
			if len(observation.Granted) == 0 || !configuration.Enabled || contribution.AppliedRevision != configuration.Revision || contribution.ListenerState != ParticipationStateV1Active || !observation.PeerOnline || !observation.Trusted || observation.Compatibility != ParticipationCompatibilityV1Compatible || observation.Admission != ParticipationAdmissionV1Promoted || !observation.Negotiated {
				return errors.New("participation active state lacks established operation")
			}
			if !nativePresent || observation.Provenance.Native.DataHash == "" || observation.Provenance.TransportVersion == nil || observation.Provenance.DataModelVersion == nil || observation.Losses == nil || unknownLoss {
				return errors.New("participation active state lacks native provenance or explicit loss assessment")
			}
		}
	}
	return nil
}

func ValidateParticipationControlV1(contribution ParticipationContributionV1, bindingID string) error {
	if err := contribution.Validate(); err != nil {
		return err
	}
	for _, observation := range contribution.Bindings {
		if observation.ID == bindingID && observation.State == ParticipationStateV1Active && slices.Contains(observation.Granted, ParticipationPermissionV1Control) {
			for _, loss := range observation.Losses {
				if !loss.ControlSafe {
					return errors.New("participation control loss is not qualified")
				}
			}
			return nil
		}
	}
	return errors.New("participation control is unavailable")
}

func participationStateValidV1(state ParticipationStateV1) bool {
	switch state {
	case ParticipationStateV1Configured, ParticipationStateV1Offline, ParticipationStateV1Incompatible, ParticipationStateV1Active, ParticipationStateV1Error:
		return true
	default:
		return false
	}
}

func participationPermissionsValidV1(permissions []ParticipationPermissionV1) bool {
	if len(permissions) > 2 {
		return false
	}
	seen := map[ParticipationPermissionV1]bool{}
	for _, permission := range permissions {
		if (permission != ParticipationPermissionV1Observe && permission != ParticipationPermissionV1Control) || seen[permission] {
			return false
		}
		seen[permission] = true
	}
	return true
}
