package eebusruntime

import (
	"errors"
	"fmt"
	"io"
)

const operatorAdminV1RedactedRendering = "admin_v1{redacted}"

var errOperatorAdminV1Serialization = errors.New("AdminV1 owner value cannot be serialized")

func operatorAdminV1Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, operatorAdminV1RedactedRendering)
}
func operatorAdminV1String() string               { return operatorAdminV1RedactedRendering }
func operatorAdminV1MarshalJSON() ([]byte, error) { return nil, errOperatorAdminV1Serialization }

// String returns the redacted rendering, never reducer state.
func (*operatorAdminV1Reducer) String() string { return operatorAdminV1String() }

// GoString returns the redacted rendering, never reducer state.
func (*operatorAdminV1Reducer) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (*operatorAdminV1Reducer) Format(state fmt.State, verb rune) {
	operatorAdminV1Format(state, verb)
}

// MarshalJSON always fails; the reducer has no serialized form.
func (*operatorAdminV1Reducer) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the PartnerHandleV1 contents.
func (PartnerHandleV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (PartnerHandleV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (PartnerHandleV1) Format(state fmt.State, verb rune) { operatorAdminV1Format(state, verb) }

// MarshalJSON always fails: a PartnerHandleV1 is an in-process value with no serialized form.
func (PartnerHandleV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the ObservationHandleV1 contents.
func (ObservationHandleV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (ObservationHandleV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (ObservationHandleV1) Format(state fmt.State, verb rune) {
	operatorAdminV1Format(state, verb)
}

// MarshalJSON always fails: an ObservationHandleV1 is an in-process value with no serialized form.
func (ObservationHandleV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the SelectionHandleV1 contents.
func (SelectionHandleV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (SelectionHandleV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (SelectionHandleV1) Format(state fmt.State, verb rune) {
	operatorAdminV1Format(state, verb)
}

// MarshalJSON always fails: a SelectionHandleV1 is an in-process value with no serialized form.
func (SelectionHandleV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the CandidateHandleV1 contents.
func (CandidateHandleV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (CandidateHandleV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (CandidateHandleV1) Format(state fmt.State, verb rune) {
	operatorAdminV1Format(state, verb)
}

// MarshalJSON always fails: a CandidateHandleV1 is an in-process value with no serialized form.
func (CandidateHandleV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the AdminMutationResultV1 contents.
func (AdminMutationResultV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (AdminMutationResultV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (AdminMutationResultV1) Format(state fmt.State, verb rune) { operatorAdminV1Format(state, verb) }

// MarshalJSON always fails: an AdminMutationResultV1 is an in-process value with no serialized form.
func (AdminMutationResultV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the ConnectResultV1 contents.
func (ConnectResultV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (ConnectResultV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (ConnectResultV1) Format(state fmt.State, verb rune) { operatorAdminV1Format(state, verb) }

// MarshalJSON always fails: a ConnectResultV1 is an in-process value with no serialized form.
func (ConnectResultV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the AdminSelectionResultV1 contents.
func (AdminSelectionResultV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (AdminSelectionResultV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (AdminSelectionResultV1) Format(state fmt.State, verb rune) {
	operatorAdminV1Format(state, verb)
}

// MarshalJSON always fails: an AdminSelectionResultV1 is an in-process value with no serialized form.
func (AdminSelectionResultV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the AdminSnapshotV1 contents.
func (AdminSnapshotV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (AdminSnapshotV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (AdminSnapshotV1) Format(state fmt.State, verb rune) { operatorAdminV1Format(state, verb) }

// MarshalJSON always fails: an AdminSnapshotV1 is an in-process value with no serialized form.
func (AdminSnapshotV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the ActiveActionV1 contents.
func (ActiveActionV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (ActiveActionV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (ActiveActionV1) Format(state fmt.State, verb rune) { operatorAdminV1Format(state, verb) }

// MarshalJSON always fails: an ActiveActionV1 is an in-process value with no serialized form.
func (ActiveActionV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the TrustedPartnerV1 contents.
func (TrustedPartnerV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (TrustedPartnerV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (TrustedPartnerV1) Format(state fmt.State, verb rune) { operatorAdminV1Format(state, verb) }

// MarshalJSON always fails: a TrustedPartnerV1 is an in-process value with no serialized form.
func (TrustedPartnerV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the ConnectedPartnerV1 contents.
func (ConnectedPartnerV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (ConnectedPartnerV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (ConnectedPartnerV1) Format(state fmt.State, verb rune) {
	operatorAdminV1Format(state, verb)
}

// MarshalJSON always fails: a ConnectedPartnerV1 is an in-process value with no serialized form.
func (ConnectedPartnerV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the DiscoveredPartnerV1 contents.
func (DiscoveredPartnerV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (DiscoveredPartnerV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (DiscoveredPartnerV1) Format(state fmt.State, verb rune) {
	operatorAdminV1Format(state, verb)
}

// MarshalJSON always fails: a DiscoveredPartnerV1 is an in-process value with no serialized form.
func (DiscoveredPartnerV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the CandidateV1 contents.
func (CandidateV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (CandidateV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (CandidateV1) Format(state fmt.State, verb rune) { operatorAdminV1Format(state, verb) }

// MarshalJSON always fails: a CandidateV1 is an in-process value with no serialized form.
func (CandidateV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }
