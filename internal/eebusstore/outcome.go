package eebusstore

import (
	"fmt"
	"io"
	"strconv"
)

type outcome string

const (
	outcomeOpenedEmpty                     outcome = "opened_empty"
	outcomeOpenedCurrent                   outcome = "opened_current"
	outcomeRecoveryCandidateAvailable      outcome = "recovery_candidate_available"
	outcomeCommitDurable                   outcome = "commit_durable"
	outcomeCommitAppliedMaintenanceFailed  outcome = "commit_applied_maintenance_failed"
	outcomePathRejected                    outcome = "path_rejected"
	outcomeBootstrapDurabilityUnknown      outcome = "bootstrap_durability_unknown"
	outcomeFilesystemCapabilityUnavailable outcome = "filesystem_capability_unavailable"
	outcomePermissionsRejected             outcome = "permissions_rejected"
	outcomeLayoutRejected                  outcome = "layout_rejected"
	outcomeWriterBusy                      outcome = "writer_busy"
	outcomeLockUnavailable                 outcome = "lock_unavailable"
	outcomeManifestAmbiguous               outcome = "manifest_ambiguous"
	outcomeNoValidManifest                 outcome = "no_valid_manifest"
	outcomeUnsupportedLegacyVersion        outcome = "unsupported_legacy_version"
	outcomeUnsupportedFutureVersion        outcome = "unsupported_future_version"
	outcomeMalformedState                  outcome = "malformed_state"
	outcomeNoValidCurrent                  outcome = "no_valid_current"
	outcomeKeyProviderUnavailable          outcome = "key_provider_unavailable"
	outcomeKeyMaterialUnavailable          outcome = "key_material_unavailable"
	outcomeMaintenanceFailed               outcome = "maintenance_failed"
	outcomeCommitNotPublished              outcome = "commit_not_published"
	outcomeCommitDurabilityUnknown         outcome = "commit_durability_unknown"
	outcomeIOFailed                        outcome = "io_failed"
)

type storeError struct {
	result    outcome
	operation string
}

func newStoreError(result outcome, operation string, cause error) *storeError {
	_ = cause
	return &storeError{result: result, operation: operation}
}

func (e *storeError) Error() string {
	if e == nil {
		return "eebusstore_error"
	}
	return string(e.result) + ":" + e.operation
}

func (e *storeError) String() string { return e.Error() }

func (e *storeError) GoString() string { return e.Error() }

func (e *storeError) Format(state fmt.State, verb rune) {
	value := e.Error()
	if verb == 'q' {
		value = strconv.Quote(value)
	}
	_, _ = io.WriteString(state, value)
}

func (e *storeError) MarshalText() ([]byte, error) {
	return []byte(e.Error()), nil
}

func outcomeOf(err error) outcome {
	if typed, ok := err.(*storeError); ok && typed != nil {
		return typed.result
	}
	return outcomeIOFailed
}
