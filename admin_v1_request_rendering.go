package eebusruntime

import (
	"fmt"
)

// String returns the fixed redacted rendering, never the MutationPreconditionV1 contents.
func (MutationPreconditionV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (MutationPreconditionV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (MutationPreconditionV1) Format(state fmt.State, verb rune) { operatorAdminV1Format(state, verb) }

// MarshalJSON always fails: a MutationPreconditionV1 is an in-process value with no serialized form.
func (MutationPreconditionV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the AdminSnapshotRequestV1 contents.
func (AdminSnapshotRequestV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (AdminSnapshotRequestV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (AdminSnapshotRequestV1) Format(state fmt.State, verb rune) { operatorAdminV1Format(state, verb) }

// MarshalJSON always fails: an AdminSnapshotRequestV1 is an in-process value with no serialized form.
func (AdminSnapshotRequestV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the OpenPairingWindowRequestV1 contents.
func (OpenPairingWindowRequestV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (OpenPairingWindowRequestV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (OpenPairingWindowRequestV1) Format(state fmt.State, verb rune) {
	operatorAdminV1Format(state, verb)
}

// MarshalJSON always fails: an OpenPairingWindowRequestV1 is an in-process value with no serialized form.
func (OpenPairingWindowRequestV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the ClosePairingWindowRequestV1 contents.
func (ClosePairingWindowRequestV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (ClosePairingWindowRequestV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (ClosePairingWindowRequestV1) Format(state fmt.State, verb rune) {
	operatorAdminV1Format(state, verb)
}

// MarshalJSON always fails: a ClosePairingWindowRequestV1 is an in-process value with no serialized form.
func (ClosePairingWindowRequestV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the SelectRequestV1 contents.
func (SelectRequestV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (SelectRequestV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (SelectRequestV1) Format(state fmt.State, verb rune) { operatorAdminV1Format(state, verb) }

// MarshalJSON always fails: a SelectRequestV1 is an in-process value with no serialized form.
func (SelectRequestV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the ConnectRequestV1 contents.
func (ConnectRequestV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (ConnectRequestV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (ConnectRequestV1) Format(state fmt.State, verb rune) { operatorAdminV1Format(state, verb) }

// MarshalJSON always fails: a ConnectRequestV1 is an in-process value with no serialized form.
func (ConnectRequestV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the ConfirmRequestV1 contents.
func (ConfirmRequestV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (ConfirmRequestV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (ConfirmRequestV1) Format(state fmt.State, verb rune) { operatorAdminV1Format(state, verb) }

// MarshalJSON always fails: a ConfirmRequestV1 is an in-process value with no serialized form.
func (ConfirmRequestV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the CancelRequestV1 contents.
func (CancelRequestV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (CancelRequestV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (CancelRequestV1) Format(state fmt.State, verb rune) { operatorAdminV1Format(state, verb) }

// MarshalJSON always fails: a CancelRequestV1 is an in-process value with no serialized form.
func (CancelRequestV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the RetryTrustedRequestV1 contents.
func (RetryTrustedRequestV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (RetryTrustedRequestV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (RetryTrustedRequestV1) Format(state fmt.State, verb rune) { operatorAdminV1Format(state, verb) }

// MarshalJSON always fails: a RetryTrustedRequestV1 is an in-process value with no serialized form.
func (RetryTrustedRequestV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }

// String returns the fixed redacted rendering, never the UntrustRequestV1 contents.
func (UntrustRequestV1) String() string { return operatorAdminV1String() }

// GoString returns the same redacted rendering as String.
func (UntrustRequestV1) GoString() string { return operatorAdminV1String() }

// Format writes the redacted rendering for every fmt verb and flag.
func (UntrustRequestV1) Format(state fmt.State, verb rune) { operatorAdminV1Format(state, verb) }

// MarshalJSON always fails: an UntrustRequestV1 is an in-process value with no serialized form.
func (UntrustRequestV1) MarshalJSON() ([]byte, error) { return operatorAdminV1MarshalJSON() }
