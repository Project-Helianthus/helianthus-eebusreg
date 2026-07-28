package eebusmutation

import "github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"

func cloneMutation(mutation eebusraw.MutationV1) eebusraw.MutationV1 {
	mutation.Target = mutation.Target.Clone()
	mutation.Before = mutation.Before.Clone()
	mutation.Requested = mutation.Requested.Clone()
	mutation.ProtocolAccepted = cloneBool(mutation.ProtocolAccepted)
	mutation.ObservedAfter = cloneValue(mutation.ObservedAfter)
	if mutation.Rollback != nil {
		rollback := *mutation.Rollback
		rollback.Before = rollback.Before.Clone()
		rollback.ProtocolAccepted = cloneBool(rollback.ProtocolAccepted)
		rollback.ObservedAfter = cloneValue(rollback.ObservedAfter)
		if rollback.Error != nil {
			value := rollback.Error.Clone()
			rollback.Error = &value
		}
		if rollback.Verification != nil {
			value := *rollback.Verification
			rollback.Verification = &value
		}
		mutation.Rollback = &rollback
	}
	if mutation.ProbeDeadline != nil {
		value := *mutation.ProbeDeadline
		mutation.ProbeDeadline = &value
	}
	if mutation.Error != nil {
		value := mutation.Error.Clone()
		mutation.Error = &value
	}
	if mutation.ApplyVerification != nil {
		value := *mutation.ApplyVerification
		mutation.ApplyVerification = &value
	}
	if mutation.ConflictEvidence != nil {
		value := *mutation.ConflictEvidence
		mutation.ConflictEvidence = &value
	}
	if mutation.NoContactEvidence != nil {
		value := *mutation.NoContactEvidence
		mutation.NoContactEvidence = &value
	}
	if mutation.RejectionVerification != nil {
		value := *mutation.RejectionVerification
		mutation.RejectionVerification = &value
	}
	if mutation.NoEffectVerification != nil {
		value := *mutation.NoEffectVerification
		mutation.NoEffectVerification = &value
	}
	if mutation.OutcomeEvidence != nil {
		value := *mutation.OutcomeEvidence
		mutation.OutcomeEvidence = &value
	}
	if mutation.Audit != nil {
		mutation.Audit = append([]eebusraw.AuditTransitionV1(nil), mutation.Audit...)
		for index := range mutation.Audit {
			if mutation.Audit[index].PreviousHash != nil {
				value := *mutation.Audit[index].PreviousHash
				mutation.Audit[index].PreviousHash = &value
			}
		}
	}
	return mutation
}

func cloneValue(value *eebusraw.TypedValueV1) *eebusraw.TypedValueV1 {
	if value == nil {
		return nil
	}
	cloned := value.Clone()
	return &cloned
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneTerminal(terminal *eebusraw.ErrorV1) *eebusraw.ErrorV1 {
	if terminal == nil {
		return nil
	}
	cloned := terminal.Clone()
	return &cloned
}
