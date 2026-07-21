package eebusfacade

import (
	"bytes"
	"encoding/hex"
	"errors"
	"sort"
)

const trustAdminProjectionContract = "helianthus.eebus.trust-admin-projection.v1"

type trustAdminProjection struct {
	contract          string
	revision          uint64
	phase             string
	recovery          string
	mutationAvailable bool
	degradation       string
	remotes           []trustAdminRemoteProjection
}

type trustAdminRemoteProjection struct {
	configuredIndex int
	state           string
	paired          bool
}

type trustAdminProjectionBinding struct {
	remotes  [][]byte
	observer func(trustAdminProjection)
	last     trustAdminProjection
}

func (handler *runtimeServiceHandler) bindTrustAdminProjection(coordinator *firstTrustCoordinator) error {
	if handler == nil || coordinator == nil {
		return errors.New("trust admin projection runtime binding is incomplete")
	}
	handler.mu.Lock()
	remotes := append([]string(nil), handler.policyRemotes...)
	handler.mu.Unlock()
	decoded := make([][]byte, len(remotes))
	for index, ski := range remotes {
		remote, err := hex.DecodeString(ski)
		if err != nil || len(remote) != 20 {
			return errors.New("trust admin projection runtime remote is invalid")
		}
		decoded[index] = remote
	}
	sort.SliceStable(decoded, func(left, right int) bool {
		return bytes.Compare(decoded[left], decoded[right]) < 0
	})
	sort.Strings(remotes)

	handler.mu.Lock()
	if handler.projectionCapture != nil {
		handler.mu.Unlock()
		return errors.New("trust admin projection runtime is already bound")
	}
	handler.projectionCapture = coordinator.captureTrustAdminProjection
	handler.projectionLivenessAllowed = coordinator.trustAdminLivenessAllowed
	handler.projectionRemotes = append([]string(nil), remotes...)
	handler.mu.Unlock()

	err := coordinator.bindTrustAdminProjection(decoded, func(projection trustAdminProjection) {
		if publishErr := handler.publishTrustAdminProjection(projection); publishErr != nil {
			handler.report(publishErr)
		}
	})
	if err != nil {
		handler.mu.Lock()
		handler.projectionCapture = nil
		handler.projectionLivenessAllowed = nil
		handler.projectionRemotes = nil
		handler.mu.Unlock()
	}
	return err
}

func applyTrustAdminProjection(graph []runtimeGraphObservation, remotes []string, projection trustAdminProjection) {
	valid := projection.contract == trustAdminProjectionContract && projection.revision != 0 && len(projection.remotes) == len(remotes)
	if projection.degradation != "" && projection.degradation != "denied-trust" && projection.degradation != "certificate-unavailable" {
		valid = false
	}
	byRemote := make(map[string]trustAdminRemoteProjection, len(remotes))
	if valid {
		for index, result := range projection.remotes {
			if result.configuredIndex != index || result.state != "unknown" && result.state != "denied" && result.state != "paired" && result.state != "unpaired" ||
				result.paired != (result.state == "paired") {
				valid = false
				break
			}
			byRemote[remotes[index]] = result
		}
	}
	if !valid {
		for index := range graph {
			graph[index].PairingState = "unknown"
			graph[index].Paired = false
			graph[index].TrustDegradation = "denied-trust"
		}
		return
	}
	for index := range graph {
		result, configured := byRemote[graph[index].RemoteSKI]
		if !configured {
			continue
		}
		graph[index].PairingState = result.state
		graph[index].Paired = result.paired
		graph[index].TrustDegradation = projection.degradation
	}
}

func (coordinator *firstTrustCoordinator) bindTrustAdminProjection(remotes [][]byte, observer func(trustAdminProjection)) error {
	if coordinator == nil || observer == nil {
		return errors.New("trust admin projection binding is incomplete")
	}
	cloned := make([][]byte, len(remotes))
	for index, remote := range remotes {
		if len(remote) != 20 {
			return errors.New("trust admin projection remote is invalid")
		}
		cloned[index] = bytes.Clone(remote)
		if index > 0 && bytes.Compare(cloned[index-1], cloned[index]) >= 0 {
			return errors.New("trust admin projection remotes are not strictly ordered")
		}
	}

	coordinator.mu.Lock()
	if coordinator.trustAdminProjection != nil {
		coordinator.mu.Unlock()
		return errors.New("trust admin projection is already bound")
	}
	coordinator.trustAdminProjection = &trustAdminProjectionBinding{remotes: cloned, observer: observer}
	coordinator.trustAdminRevision++
	projection := coordinator.captureTrustAdminProjectionLocked()
	coordinator.trustAdminProjection.last = cloneTrustAdminProjection(projection)
	coordinator.mu.Unlock()

	observer(projection)
	return nil
}

func (coordinator *firstTrustCoordinator) captureTrustAdminProjection() trustAdminProjection {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return coordinator.captureTrustAdminProjectionLocked()
}

func (coordinator *firstTrustCoordinator) captureTrustAdminProjectionLocked() trustAdminProjection {
	projection := trustAdminProjection{contract: trustAdminProjectionContract, revision: coordinator.trustAdminRevision}
	if coordinator.trustAdminProjection == nil {
		projection.degradation = "denied-trust"
		return projection
	}
	phase, phaseKnown := firstTrustProjectionPhase(coordinator.phase)
	projection.phase = phase
	projection.recovery = coordinator.recovery
	projection.mutationAvailable = phaseKnown && coordinator.phase != firstTrustDisabled && !coordinator.reopening &&
		coordinator.recoveryOperation == nil && coordinator.anchorRecord.pending == nil
	projection.remotes = make([]trustAdminRemoteProjection, len(coordinator.trustAdminProjection.remotes))
	for index := range projection.remotes {
		projection.remotes[index] = trustAdminRemoteProjection{
			configuredIndex: index,
			state:           "unpaired",
		}
	}

	if coordinator.trustAdminStructuralIndeterminateLocked(phase, phaseKnown) {
		projection.degradation = "denied-trust"
		for index := range projection.remotes {
			projection.remotes[index].state = "unknown"
		}
		return projection
	}
	if coordinator.trustAdminTerminalDenialLocked() {
		projection.degradation = "denied-trust"
		for index := range projection.remotes {
			projection.remotes[index].state = "denied"
		}
		return projection
	}
	if coordinator.recovery == "NO_LOCAL_IDENTITY" {
		projection.degradation = "certificate-unavailable"
		for index := range projection.remotes {
			projection.remotes[index].state = "unknown"
		}
		return projection
	}
	if coordinator.recovery != "PAIRED_TRUSTED" {
		return projection
	}

	for index, remote := range coordinator.trustAdminProjection.remotes {
		matches := 0
		for _, association := range coordinator.controlView.associations {
			if !bytes.Equal(association.subject, remote) ||
				!firstTrustAssociationUsable(association, coordinator.controlView.control.associationLineage) ||
				coordinator.firstTrustTombstonedLocked(association) {
				continue
			}
			matches++
		}
		if matches == 1 {
			projection.remotes[index].state = "paired"
			projection.remotes[index].paired = true
		}
	}
	return projection
}

func (coordinator *firstTrustCoordinator) notifyTrustAdminProjection() {
	if coordinator == nil {
		return
	}
	coordinator.mu.Lock()
	if coordinator.trustAdminProjection == nil || coordinator.trustAdminProjection.observer == nil {
		coordinator.mu.Unlock()
		return
	}
	projection := coordinator.captureTrustAdminProjectionLocked()
	if trustAdminProjectionEqual(coordinator.trustAdminProjection.last, projection) {
		coordinator.mu.Unlock()
		return
	}
	coordinator.trustAdminRevision++
	projection.revision = coordinator.trustAdminRevision
	coordinator.trustAdminProjection.last = cloneTrustAdminProjection(projection)
	observer := coordinator.trustAdminProjection.observer
	coordinator.mu.Unlock()
	observer(projection)
}

func (coordinator *firstTrustCoordinator) unlockAndNotifyTrustAdminProjectionChange(before trustAdminProjection) {
	_ = before
	after := coordinator.captureTrustAdminProjectionLocked()
	var observer func(trustAdminProjection)
	if coordinator.trustAdminProjection != nil && !trustAdminProjectionEqual(coordinator.trustAdminProjection.last, after) {
		coordinator.trustAdminRevision++
		after.revision = coordinator.trustAdminRevision
		coordinator.trustAdminProjection.last = cloneTrustAdminProjection(after)
		observer = coordinator.trustAdminProjection.observer
	}
	coordinator.mu.Unlock()
	if observer != nil {
		observer(after)
	}
}

func (coordinator *firstTrustCoordinator) trustAdminLivenessAllowed(ski string) bool {
	remote, err := hex.DecodeString(ski)
	if err != nil || len(remote) != 20 {
		return false
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return coordinator.currentCandidate == nil || !bytes.Equal(coordinator.currentCandidate.remote, remote) ||
		coordinator.phase != firstTrustCandidatePending && coordinator.phase != firstTrustCommitting
}

func (coordinator *firstTrustCoordinator) trustAdminStructuralIndeterminateLocked(phase string, phaseKnown bool) bool {
	if !phaseKnown || !firstTrustProductAllowed(phase, coordinator.recovery) || coordinator.reopening ||
		coordinator.anchorRecord.pending != nil {
		return true
	}
	if operation := coordinator.recoveryOperation; operation != nil && operation.operationClass != "first_trust" && operation.operationClass != "revocation" {
		return true
	}
	for _, record := range coordinator.controlView.control.quarantines {
		switch record.state {
		case "RETRY_READY":
			if !firstTrustQuarantineRecordValid(record, coordinator.backoffPolicy) {
				return true
			}
		case "BACKOFF_ACTIVE", "ADMIN_HOLD":
		default:
			return true
		}
	}
	for _, attempt := range coordinator.controlView.control.attempts {
		if attempt.state != firstTrustAttemptReserved && attempt.state != firstTrustAttemptLaunchAuthorized {
			return true
		}
	}
	if coordinator.recovery != "NO_LOCAL_IDENTITY" && coordinator.firstTrustAnchorProductReasonLocked() != "" {
		return true
	}

	switch coordinator.recovery {
	case "CORRUPT_STORE":
		return true
	case "QUARANTINED":
		switch coordinator.recoveryReasonCode {
		case "DURABILITY_UNKNOWN", "HOST_BINDING_MISMATCH", "CLONE_DETECTED", "MANIFEST_GENERATION_ROLLBACK", "CONTROL_EPOCH_ROLLBACK":
			return true
		case "ADMIN_HOLD", "HANDSHAKE_ATTEMPT_LIMIT":
			return !coordinator.trustAdminHasQuarantineStateLocked("ADMIN_HOLD")
		case "RETRYABLE_FAILURE":
			return !coordinator.trustAdminHasQuarantineStateLocked("BACKOFF_ACTIVE")
		default:
			return true
		}
	case "NO_LOCAL_IDENTITY":
		return coordinator.recoveryReasonCode != "" && coordinator.recoveryReasonCode != "HOST_KEY_UNAVAILABLE"
	case "UNPAIRED_LOCKED", "PAIRED_TRUSTED":
		return coordinator.recoveryReasonCode != ""
	case "REVOKED":
		return coordinator.recoveryReasonCode != "REVOKED_ASSOCIATION"
	default:
		return true
	}
}

func (coordinator *firstTrustCoordinator) trustAdminHasQuarantineStateLocked(state string) bool {
	for _, record := range coordinator.controlView.control.quarantines {
		if record.state == state && firstTrustQuarantineRecordValid(record, coordinator.backoffPolicy) {
			return true
		}
	}
	return false
}

func (coordinator *firstTrustCoordinator) trustAdminTerminalDenialLocked() bool {
	if coordinator.recovery == "REVOKED" || coordinator.trustAdminHasTerminalQuarantineLocked() {
		return true
	}
	for _, association := range coordinator.controlView.associations {
		if association.lineage == coordinator.controlView.control.associationLineage && coordinator.firstTrustTombstonedLocked(association) {
			return true
		}
	}
	return false
}

func (coordinator *firstTrustCoordinator) trustAdminHasTerminalQuarantineLocked() bool {
	for _, record := range coordinator.controlView.control.quarantines {
		if record.state == "ADMIN_HOLD" || record.state == "BACKOFF_ACTIVE" {
			return true
		}
	}
	return false
}

func firstTrustProjectionPhase(phase firstTrustPhase) (string, bool) {
	switch phase {
	case firstTrustDisabled:
		return "DISABLED", true
	case firstTrustPairingClosed:
		return "PAIRING_CLOSED", true
	case firstTrustOpenEmpty:
		return "OPEN_EMPTY", true
	case firstTrustCandidatePending:
		return "CANDIDATE_PENDING", true
	case firstTrustCommitting:
		return "COMMITTING", true
	default:
		return "UNKNOWN", false
	}
}

func cloneTrustAdminProjection(source trustAdminProjection) trustAdminProjection {
	result := source
	result.remotes = append([]trustAdminRemoteProjection(nil), source.remotes...)
	return result
}

func trustAdminProjectionEqual(left, right trustAdminProjection) bool {
	if left.contract != right.contract || left.phase != right.phase || left.recovery != right.recovery ||
		left.mutationAvailable != right.mutationAvailable || left.degradation != right.degradation || len(left.remotes) != len(right.remotes) {
		return false
	}
	for index := range left.remotes {
		if left.remotes[index] != right.remotes[index] {
			return false
		}
	}
	return true
}
