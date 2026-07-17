package main

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
)

type msp04cr2SyntheticProofOptions struct {
	Scenario       string
	StateRoot      string
	RemoteSKI      string
	EndpointHost   string
	EndpointPort   uint16
	SelectedPath   string
	FallbackPath   string
	TLSConfig      *tls.Config
	ObserveNetwork func() ([]string, []string)
	ReleaseNetwork func()
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

type msp04cr2ObservedNetwork struct {
	composition      string
	requests         []string
	accepts          []string
	callbackRejected bool
}

type msp04cr2SyntheticProof struct {
	scenario       string
	evidenceStatus string
	events         []msp04cr2PrivateEvent
	reservations   []msp04cr2PrivateReservation
	permits        []msp04cr2PrivatePermit
	dials          []msp04cr2PrivateDial
	accepts        []msp04cr2PrivateAccept
	observed       *msp04cr2ObservedNetwork
}

type msp04cr2ProductionProofRunner func(
	context.Context,
	string,
	string,
	string,
	string,
	uint16,
	string,
	func() ([]string, []string),
	func(),
) ([]byte, error)

var runMSP04CR2ProductionProof msp04cr2ProductionProofRunner = func(
	context.Context,
	string,
	string,
	string,
	string,
	uint16,
	string,
	func() ([]string, []string),
	func(),
) ([]byte, error) {
	return nil, errors.New("MSP-04C-R2 production proof runner is unavailable")
}

type msp04cr2WireBinding struct {
	AttemptID    string `json:"attempt_id"`
	Scope        string `json:"scope"`
	ControlEpoch uint64 `json:"control_epoch"`
	Path         string `json:"path"`
	ContextID    string `json:"context_id"`
}

type msp04cr2WireProof struct {
	Composition      string                `json:"composition"`
	Scenario         string                `json:"scenario"`
	Reservations     []msp04cr2WireBinding `json:"reservations"`
	Permits          []msp04cr2WireBinding `json:"permits"`
	Requests         []string              `json:"requests"`
	Accepts          []string              `json:"accepts"`
	CallbackRejected bool                  `json:"callback_rejected"`
}

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

	payload, err := runMSP04CR2ProductionProof(
		ctx,
		options.Scenario,
		options.StateRoot,
		options.RemoteSKI,
		options.EndpointHost,
		options.EndpointPort,
		options.SelectedPath,
		options.ObserveNetwork,
		options.ReleaseNetwork,
	)
	if err != nil {
		return proof, err
	}
	var wire msp04cr2WireProof
	if err := json.Unmarshal(payload, &wire); err != nil {
		return proof, errors.New("malformed MSP-04C-R2 production proof")
	}
	if wire.Scenario != options.Scenario || wire.Composition != "released-eebus-go+ship-go+canonical-store" {
		return proof, errors.New("unexpected MSP-04C-R2 production composition")
	}
	proof.observed = &msp04cr2ObservedNetwork{
		composition:      wire.Composition,
		requests:         append([]string(nil), wire.Requests...),
		accepts:          append([]string(nil), wire.Accepts...),
		callbackRejected: wire.CallbackRejected,
	}
	if err := proof.consumeBindings(wire); err != nil {
		return proof, err
	}
	proof.evidenceStatus = msp04cr2ExecutedProofStatus(proof)
	return proof, nil
}

func (proof *msp04cr2SyntheticProof) consumeBindings(wire msp04cr2WireProof) error {
	if len(wire.Reservations) != len(wire.Permits) || len(wire.Permits) != len(wire.Requests) {
		if len(wire.Reservations) == 0 && len(wire.Permits) == 0 && len(wire.Requests) == 0 {
			return nil
		}
		return errors.New("production gate and network observation cardinality differ")
	}
	for index := range wire.Reservations {
		reservationID, ok := decodeMSP04CR2Opaque(wire.Reservations[index].AttemptID)
		if !ok {
			return errors.New("invalid production reservation attempt id")
		}
		scope, ok := decodeMSP04CR2Opaque(wire.Reservations[index].Scope)
		if !ok {
			return errors.New("invalid production reservation scope")
		}
		permitID, ok := decodeMSP04CR2Opaque(wire.Permits[index].AttemptID)
		if !ok || permitID != reservationID || wire.Permits[index].Scope != wire.Reservations[index].Scope ||
			wire.Permits[index].ControlEpoch != wire.Reservations[index].ControlEpoch || wire.Permits[index].ContextID == "" ||
			!msp04cr2EquivalentPath(wire.Reservations[index].Path, wire.Requests[index]) {
			return errors.New("production permit or DialContext changed its durable binding")
		}
		reservation := msp04cr2PrivateReservation{
			attemptID: reservationID, scope: scope, controlEpoch: wire.Reservations[index].ControlEpoch,
			path: wire.Reservations[index].Path,
		}
		permit := msp04cr2PrivatePermit{
			attemptID: permitID, scope: scope, controlEpoch: wire.Permits[index].ControlEpoch,
			contextID: wire.Permits[index].ContextID,
		}
		proof.reservations = append(proof.reservations, reservation)
		proof.permits = append(proof.permits, permit)
		proof.dials = append(proof.dials, msp04cr2PrivateDial{
			attemptID: permitID, path: reservation.path, contextID: permit.contextID,
		})
		proof.events = append(proof.events,
			msp04cr2PrivateEvent{kind: "reservation_committed", attemptID: reservationID, controlEpoch: reservation.controlEpoch, path: reservation.path},
			msp04cr2PrivateEvent{kind: "handle_returned", attemptID: reservationID, controlEpoch: reservation.controlEpoch, path: reservation.path},
			msp04cr2PrivateEvent{kind: "launch_committed", attemptID: permitID, controlEpoch: permit.controlEpoch, path: reservation.path},
			msp04cr2PrivateEvent{kind: "permit_returned", attemptID: permitID, controlEpoch: permit.controlEpoch, path: reservation.path},
			msp04cr2PrivateEvent{kind: "dial_context", attemptID: permitID, controlEpoch: permit.controlEpoch, path: reservation.path},
		)
	}

	usedRequests := make([]bool, len(wire.Requests))
	for _, acceptedPath := range wire.Accepts {
		requestIndex := -1
		for index, requestPath := range wire.Requests {
			if !usedRequests[index] && msp04cr2EquivalentPath(requestPath, acceptedPath) {
				requestIndex = index
				break
			}
		}
		if requestIndex < 0 {
			return errors.New("peer accept has no production DialContext observation")
		}
		usedRequests[requestIndex] = true
		accepted := msp04cr2PrivateAccept{
			attemptID: proof.permits[requestIndex].attemptID,
			path:      proof.reservations[requestIndex].path,
		}
		proof.accepts = append(proof.accepts, accepted)
		proof.events = append(proof.events, msp04cr2PrivateEvent{
			kind: "peer_accept", attemptID: accepted.attemptID,
			controlEpoch: proof.permits[requestIndex].controlEpoch, path: accepted.path,
		})
	}
	return nil
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
		options.TLSConfig == nil || options.ObserveNetwork == nil || options.ReleaseNetwork == nil ||
		len(options.SelectedPath) > 2048 || len(options.FallbackPath) > 2048 ||
		(options.SelectedPath != "" && !strings.HasPrefix(options.SelectedPath, "/")) ||
		(options.FallbackPath != "" && !strings.HasPrefix(options.FallbackPath, "/")) {
		return errors.New("invalid MSP-04C-R2 proof binding")
	}
	return nil
}

func decodeMSP04CR2Opaque(value string) ([32]byte, bool) {
	var result [32]byte
	if len(value) != 64 || value != strings.ToLower(value) {
		return result, false
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(result) {
		return result, false
	}
	copy(result[:], decoded)
	return result, result != [32]byte{}
}

func msp04cr2EquivalentPath(left, right string) bool {
	if left == "" {
		left = "/"
	}
	if right == "" {
		right = "/"
	}
	return left == right
}

func msp04cr2ExecutedProofStatus(proof msp04cr2SyntheticProof) string {
	if proof.observed == nil || proof.observed.composition != "released-eebus-go+ship-go+canonical-store" ||
		len(proof.dials) != len(proof.observed.requests) || len(proof.accepts) != len(proof.observed.accepts) {
		return "FAIL"
	}
	switch proof.scenario {
	case "callback_only":
		return "FAIL"
	case "reserve_failure", "policy_denied", "backoff", "quarantined", "revoked":
		if len(proof.reservations) == 0 && len(proof.permits) == 0 && len(proof.dials) == 0 && len(proof.accepts) == 0 {
			return "PASS"
		}
		return "FAIL"
	}
	if len(proof.reservations) == 0 || len(proof.reservations) != len(proof.permits) ||
		len(proof.permits) != len(proof.dials) || len(proof.accepts) == 0 {
		return "FAIL"
	}
	for index := range proof.permits {
		if proof.reservations[index].attemptID != proof.permits[index].attemptID ||
			proof.permits[index].attemptID != proof.dials[index].attemptID ||
			proof.permits[index].contextID == "" || proof.permits[index].contextID != proof.dials[index].contextID ||
			!msp04cr2EquivalentPath(proof.dials[index].path, proof.observed.requests[index]) {
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
	exactAttempt := permit.evidenceStatus == "PASS" && permit.observed != nil && len(permit.observed.requests) == 1 &&
		len(permit.observed.accepts) == 1 && len(permit.reservations) == 1 && len(permit.permits) == 1 &&
		len(permit.dials) == 1 && len(permit.accepts) == 1 && permit.reservations[0].attemptID == permit.permits[0].attemptID &&
		permit.permits[0].attemptID == permit.dials[0].attemptID && permit.permits[0].contextID == permit.dials[0].contextID
	callbackRejected := callbackOnly.evidenceStatus == "FAIL" && callbackOnly.observed != nil &&
		callbackOnly.observed.callbackRejected && len(callbackOnly.observed.requests) == 0 && len(callbackOnly.observed.accepts) == 0 &&
		len(callbackOnly.dials) == 0 && len(callbackOnly.accepts) == 0
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
