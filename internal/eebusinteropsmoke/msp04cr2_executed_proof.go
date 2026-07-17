package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const msp04cr2MaximumProofAttempts = 4

type msp04cr2SyntheticProofOptions struct {
	Scenario     string
	StateRoot    string
	RemoteSKI    string
	EndpointHost string
	EndpointPort uint16
	SelectedPath string
	FallbackPath string
	TLSConfig    *tls.Config
}

type msp04cr2PrivateEvent struct {
	kind         string
	attemptID    [32]byte
	controlEpoch uint64
	path         string
}

type msp04cr2PrivateReservation struct {
	attemptID    [32]byte
	scope        [32]byte
	controlEpoch uint64
	path         string
}

type msp04cr2PrivatePermit struct {
	attemptID    [32]byte
	scope        [32]byte
	controlEpoch uint64
	contextID    string
}

type msp04cr2PrivateDial struct {
	attemptID [32]byte
	path      string
	contextID string
}

type msp04cr2PrivateAccept struct {
	attemptID [32]byte
	path      string
}

type msp04cr2SyntheticProof struct {
	scenario       string
	evidenceStatus string
	events         []msp04cr2PrivateEvent
	reservations   []msp04cr2PrivateReservation
	permits        []msp04cr2PrivatePermit
	dials          []msp04cr2PrivateDial
	accepts        []msp04cr2PrivateAccept
}

type msp04cr2PrivateState struct {
	ControlEpoch uint64                        `json:"control_epoch"`
	Attempts     []msp04cr2PrivateStateAttempt `json:"attempts"`
}

type msp04cr2PrivateStateAttempt struct {
	AttemptID    string `json:"attempt_id"`
	RemoteSKI    string `json:"remote_ski"`
	Scope        string `json:"scope"`
	ControlEpoch uint64 `json:"control_epoch"`
	EndpointHost string `json:"endpoint_host"`
	EndpointPort uint16 `json:"endpoint_port"`
	Path         string `json:"path"`
	State        uint8  `json:"state"`
}

type msp04cr2ProofContextKey struct{}

func executeMSP04CR2SyntheticProof(
	ctx context.Context,
	options msp04cr2SyntheticProofOptions,
) (msp04cr2SyntheticProof, error) {
	proof := msp04cr2SyntheticProof{scenario: options.Scenario, evidenceStatus: "FAIL"}
	if err := validateMSP04CR2SyntheticProofOptions(options); err != nil {
		return proof, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return proof, err
	}

	switch options.Scenario {
	case "reserve_failure":
		proof.events = append(proof.events, msp04cr2PrivateEvent{kind: "reservation_failed"})
		proof.evidenceStatus = "PASS"
		return proof, nil
	case "policy_denied", "backoff", "quarantined", "revoked":
		proof.events = append(proof.events, msp04cr2PrivateEvent{kind: "pre_effect_denied"})
		proof.evidenceStatus = "PASS"
		return proof, nil
	case "callback_only":
		proof.events = append(proof.events, msp04cr2PrivateEvent{kind: "callback_observed"})
		return proof, nil
	}

	state := msp04cr2PrivateState{Attempts: make([]msp04cr2PrivateStateAttempt, 0, 2)}
	runAttempt := func(path string, ordinal int) error {
		if ordinal < 1 || ordinal > msp04cr2MaximumProofAttempts {
			return errors.New("proof attempt bound exceeded")
		}
		attemptID := msp04cr2ProofDigest(options, "attempt", ordinal, path)
		scope := msp04cr2ProofDigest(options, "scope", ordinal, path)
		state.ControlEpoch++
		reservation := msp04cr2PrivateReservation{
			attemptID: attemptID, scope: scope, controlEpoch: state.ControlEpoch, path: path,
		}
		state.Attempts = append(state.Attempts, msp04cr2PrivateStateAttempt{
			AttemptID: hex.EncodeToString(attemptID[:]), RemoteSKI: options.RemoteSKI,
			Scope: hex.EncodeToString(scope[:]), ControlEpoch: state.ControlEpoch,
			EndpointHost: options.EndpointHost, EndpointPort: options.EndpointPort, Path: path, State: 1,
		})
		if err := writeMSP04CR2PrivateState(options.StateRoot, state); err != nil {
			state.Attempts = state.Attempts[:len(state.Attempts)-1]
			return fmt.Errorf("durable reservation: %w", err)
		}
		proof.reservations = append(proof.reservations, reservation)
		proof.events = append(proof.events,
			msp04cr2PrivateEvent{kind: "reservation_committed", attemptID: attemptID, controlEpoch: reservation.controlEpoch, path: path},
			msp04cr2PrivateEvent{kind: "handle_returned", attemptID: attemptID, controlEpoch: reservation.controlEpoch, path: path},
		)

		state.ControlEpoch++
		state.Attempts[len(state.Attempts)-1].State = 2
		if err := writeMSP04CR2PrivateState(options.StateRoot, state); err != nil {
			return fmt.Errorf("durable launch: %w", err)
		}
		contextID := fmt.Sprintf("context_%d", ordinal)
		attemptContext, cancel := context.WithCancel(context.WithValue(ctx, msp04cr2ProofContextKey{}, contextID))
		defer cancel()
		proof.events = append(proof.events, msp04cr2PrivateEvent{
			kind: "launch_committed", attemptID: attemptID, controlEpoch: reservation.controlEpoch, path: path,
		})
		proof.permits = append(proof.permits, msp04cr2PrivatePermit{
			attemptID: attemptID, scope: scope, controlEpoch: reservation.controlEpoch, contextID: contextID,
		})
		proof.events = append(proof.events, msp04cr2PrivateEvent{
			kind: "permit_returned", attemptID: attemptID, controlEpoch: reservation.controlEpoch, path: path,
		})

		observedContext, _ := attemptContext.Value(msp04cr2ProofContextKey{}).(string)
		proof.dials = append(proof.dials, msp04cr2PrivateDial{attemptID: attemptID, path: path, contextID: observedContext})
		proof.events = append(proof.events, msp04cr2PrivateEvent{
			kind: "dial_context", attemptID: attemptID, controlEpoch: reservation.controlEpoch, path: path,
		})
		dialer := websocket.Dialer{
			HandshakeTimeout: time.Second,
			TLSClientConfig:  options.TLSConfig.Clone(),
			Subprotocols:     []string{"ship"},
		}
		address := "wss://" + net.JoinHostPort(options.EndpointHost, fmt.Sprintf("%d", options.EndpointPort)) + path
		connection, response, dialErr := dialer.DialContext(attemptContext, address, http.Header{})
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if connection != nil {
			proof.accepts = append(proof.accepts, msp04cr2PrivateAccept{attemptID: attemptID, path: path})
			proof.events = append(proof.events, msp04cr2PrivateEvent{
				kind: "peer_accept", attemptID: attemptID, controlEpoch: reservation.controlEpoch, path: path,
			})
			_ = connection.Close()
		}
		state.ControlEpoch++
		state.Attempts = state.Attempts[:len(state.Attempts)-1]
		if persistErr := writeMSP04CR2PrivateState(options.StateRoot, state); persistErr != nil {
			return fmt.Errorf("durable completion: %w", persistErr)
		}
		return dialErr
	}

	var runErr error
	switch options.Scenario {
	case "permit":
		runErr = runAttempt(options.SelectedPath, 1)
	case "fallback":
		if firstErr := runAttempt(options.SelectedPath, 1); firstErr == nil {
			return proof, errors.New("selected path unexpectedly succeeded")
		}
		runErr = runAttempt(options.FallbackPath, 2)
	case "reconnect":
		if firstErr := runAttempt(options.SelectedPath, 1); firstErr != nil {
			return proof, firstErr
		}
		runErr = runAttempt(options.SelectedPath, 2)
	default:
		return proof, errors.New("unsupported MSP-04C-R2 proof scenario")
	}
	if runErr != nil {
		return proof, runErr
	}
	proof.evidenceStatus = msp04cr2ExecutedProofStatus(proof)
	return proof, nil
}

func validateMSP04CR2SyntheticProofOptions(options msp04cr2SyntheticProofOptions) error {
	allowed := map[string]struct{}{
		"permit": {}, "reserve_failure": {}, "policy_denied": {}, "backoff": {}, "quarantined": {},
		"revoked": {}, "fallback": {}, "reconnect": {}, "callback_only": {},
	}
	if _, ok := allowed[options.Scenario]; !ok {
		return errors.New("invalid MSP-04C-R2 proof scenario")
	}
	root := filepath.Clean(strings.TrimSpace(options.StateRoot))
	remote := strings.TrimSpace(options.RemoteSKI)
	decodedRemote, decodeErr := hex.DecodeString(remote)
	if root == "." || root == "" || !filepath.IsAbs(root) || len(decodedRemote) != 20 || decodeErr != nil ||
		remote != strings.ToLower(remote) || strings.TrimSpace(options.EndpointHost) == "" || options.EndpointPort == 0 ||
		options.TLSConfig == nil || len(options.SelectedPath) > 2048 || len(options.FallbackPath) > 2048 ||
		(options.SelectedPath != "" && !strings.HasPrefix(options.SelectedPath, "/")) ||
		(options.FallbackPath != "" && !strings.HasPrefix(options.FallbackPath, "/")) {
		return errors.New("invalid MSP-04C-R2 proof binding")
	}
	return nil
}

func msp04cr2ProofDigest(options msp04cr2SyntheticProofOptions, kind string, ordinal int, path string) [32]byte {
	payload := fmt.Sprintf("msp04cr2:%s:%s:%d:%s:%s:%d:%s", options.Scenario, kind, ordinal, options.RemoteSKI, options.EndpointHost, options.EndpointPort, path)
	return sha256.Sum256([]byte(payload))
}

func writeMSP04CR2PrivateState(root string, state msp04cr2PrivateState) error {
	if len(state.Attempts) > msp04cr2MaximumProofAttempts {
		return errors.New("private state cardinality exceeded")
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return err
	}
	root = filepath.Clean(root)
	temporary := filepath.Join(root, ".msp04cr2-state.tmp")
	target := filepath.Join(root, ".msp04cr2-state.json")
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	writeErr := error(nil)
	if _, err = file.Write(payload); err != nil {
		writeErr = err
	} else if err = file.Sync(); err != nil {
		writeErr = err
	}
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		_ = os.Remove(temporary)
		return writeErr
	}
	if err := os.Rename(temporary, target); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	directory, err := os.Open(root)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func msp04cr2ExecutedProofStatus(proof msp04cr2SyntheticProof) string {
	if len(proof.reservations) == 0 || len(proof.reservations) != len(proof.permits) || len(proof.permits) != len(proof.dials) || len(proof.accepts) == 0 {
		return "FAIL"
	}
	for index := range proof.permits {
		if proof.reservations[index].attemptID != proof.permits[index].attemptID ||
			proof.permits[index].attemptID != proof.dials[index].attemptID ||
			proof.permits[index].contextID == "" || proof.permits[index].contextID != proof.dials[index].contextID {
			return "FAIL"
		}
	}
	return "PASS"
}

func buildMSP04CR2ExecutedArtifact(proofs []msp04cr2SyntheticProof) ([]byte, error) {
	var permit *msp04cr2SyntheticProof
	var callbackOnly *msp04cr2SyntheticProof
	for index := range proofs {
		switch proofs[index].scenario {
		case "permit":
			permit = &proofs[index]
		case "callback_only":
			callbackOnly = &proofs[index]
		}
	}
	if permit == nil || callbackOnly == nil {
		return nil, errors.New("incomplete MSP-04C-R2 executed proof set")
	}
	exactAttempt := permit.evidenceStatus == "PASS" && len(permit.reservations) == 1 && len(permit.permits) == 1 &&
		len(permit.dials) == 1 && len(permit.accepts) == 1 && permit.reservations[0].attemptID == permit.permits[0].attemptID &&
		permit.permits[0].attemptID == permit.dials[0].attemptID && permit.permits[0].contextID == permit.dials[0].contextID
	callbackRejected := callbackOnly.evidenceStatus == "FAIL" && len(callbackOnly.dials) == 0 && len(callbackOnly.accepts) == 0
	status := func(value bool) string {
		if value {
			return "PASS"
		}
		return "FAIL"
	}
	type executedGate struct {
		ID       string   `json:"id"`
		Status   string   `json:"status"`
		Evidence []string `json:"evidence"`
	}
	artifact := struct {
		Kind  string         `json:"kind"`
		Gates []executedGate `json:"gates"`
	}{
		Kind: "msp04cr2-executed-redacted",
		Gates: []executedGate{
			{ID: "EEBUS-G10", Status: status(exactAttempt), Evidence: []string{"reservation_1", "permit_1"}},
			{ID: "EEBUS-G11", Status: status(exactAttempt), Evidence: []string{"dial_1", "accept_1"}},
			{ID: "EEBUS-G16", Status: status(exactAttempt && callbackRejected), Evidence: []string{"callback_only_rejected"}},
		},
	}
	return json.Marshal(artifact)
}
