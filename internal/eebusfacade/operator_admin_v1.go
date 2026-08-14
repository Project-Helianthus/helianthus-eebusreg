package eebusfacade

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"sort"
	"strconv"
	"sync"
	"time"

	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
)

const (
	operatorAdminV1BridgeMaximumRows       = 128
	operatorAdminV1BridgeMaximumReferences = 128
	operatorAdminV1BridgeReferenceAttempts = 32
)

type operatorAdminV1Service interface {
	SelectPairingCandidate(string, string) (shipapi.PairingCandidateReservation, error)
	ConnectPairingCandidate(shipapi.PairingCandidateReservation) error
	RetryTrustedRemote(string) error
}

type OperatorAdminV1Backend interface {
	OperatorAdminV1Snapshot(context.Context) (OperatorAdminV1Snapshot, string)
	OperatorAdminV1Open(context.Context, time.Duration) (OperatorAdminV1Transition, string)
	OperatorAdminV1Close(context.Context) (OperatorAdminV1Transition, string)
	OperatorAdminV1Select(context.Context, string, string) (string, OperatorAdminV1Transition, string)
	OperatorAdminV1Connect(context.Context, string) (OperatorAdminV1Transition, string)
	OperatorAdminV1Confirm(context.Context, string, string) (OperatorAdminV1Transition, string)
	OperatorAdminV1Cancel(context.Context, string) (OperatorAdminV1Transition, string)
	OperatorAdminV1RetryTrusted(context.Context, string) (OperatorAdminV1Transition, string)
	OperatorAdminV1Untrust(context.Context, string) (OperatorAdminV1Transition, string)
}

type OperatorAdminV1Transition struct {
	Outcome string
	Changed bool
}

type OperatorAdminV1Snapshot struct {
	CapturedAt     time.Time
	Status         string
	Window         string
	WindowDeadline time.Time
	Register       string
	Listener       string
	Discovery      string
	Degraded       string
	Trusted        []OperatorAdminV1Fact
	Connected      []OperatorAdminV1Fact
	Discovered     []OperatorAdminV1Fact
	Candidates     []OperatorAdminV1Fact
}

type OperatorAdminV1Fact struct {
	Reference           string
	SKI                 string
	Endpoint            string
	TrustState          string
	ConnectionState     string
	SHIPID              string
	LastSeen            time.Time
	ObservationRevision uint64
	Name                string
	Identifier          string
	Brand               string
	Type                string
	Model               string
	State               string
	ExpiresAt           time.Time
	AssociationComplete bool
}

type operatorAdminV1BridgeTransition struct {
	outcome string
	changed bool
}

type operatorAdminV1BridgeSnapshot struct {
	capturedAt     time.Time
	status         string
	window         string
	windowDeadline time.Time
	register       string
	listener       string
	discovery      string
	degraded       string
	trusted        []operatorAdminV1BridgeFact
	connected      []operatorAdminV1BridgeFact
	discovered     []operatorAdminV1BridgeFact
	candidates     []operatorAdminV1BridgeFact
}

type operatorAdminV1BridgeFact struct {
	reference           string
	ski                 string
	endpoint            string
	trustState          string
	connectionState     string
	shipID              string
	lastSeen            time.Time
	observationRevision uint64
	name                string
	identifier          string
	brand               string
	typeName            string
	model               string
	state               string
	expiresAt           time.Time
	associationComplete bool
}

type operatorAdminV1BridgePartnerBinding struct {
	association [32]byte
	ski         string
	durable     bool
}

type operatorAdminV1BridgeObservationBinding struct {
	candidateRef string
	ski          string
	revision     uint64
}

type operatorAdminV1BridgeSelectionBinding struct {
	candidateRef string
	ski          string
	reservation  shipapi.PairingCandidateReservation
	consumed     bool
}

type operatorAdminV1BridgeCandidateBinding struct {
	fingerprint         string
	nonce               string
	expiresAt           time.Time
	connection          uint64
	storeGeneration     uint64
	state               string
	associationComplete bool
}

type operatorAdminV1Bridge struct {
	serial sync.Mutex

	coordinator *firstTrustCoordinator
	service     operatorAdminV1Service
	random      io.Reader
	closed      bool

	partners     map[string]operatorAdminV1BridgePartnerBinding
	observations map[string]operatorAdminV1BridgeObservationBinding
	selections   map[string]operatorAdminV1BridgeSelectionBinding
	candidates   map[string]operatorAdminV1BridgeCandidateBinding
	operationIDs map[[32]byte]struct{}
	operations   map[string]struct{}
}

type operatorAdminV1BridgeRawSnapshot struct {
	capturedAt     time.Time
	status         string
	window         string
	windowDeadline time.Time
	register       string
	listener       string
	discovery      string
	degraded       string
	trusted        []operatorAdminV1BridgeRawPartner
	connected      []operatorAdminV1BridgeRawConnected
	discovered     []operatorAdminV1BridgeRawObservation
	candidate      *operatorAdminV1BridgeCandidateBinding
}

type operatorAdminV1BridgeRawPartner struct {
	target      string
	ski         string
	association [32]byte
	durable     bool
	trustState  string
	shipID      string
	lastSeen    time.Time
}

type operatorAdminV1BridgeRawConnected struct {
	ski             string
	endpoint        string
	trustState      string
	connectionState string
	shipID          string
	lastSeen        time.Time
}

type operatorAdminV1BridgeRawObservation struct {
	target       string
	candidateRef string
	ski          string
	revision     uint64
	lastSeen     time.Time
	name         string
	identifier   string
	brand        string
	typeName     string
	model        string
	expiresAt    time.Time
}

func newOperatorAdminV1Bridge(
	coordinator *firstTrustCoordinator,
	service operatorAdminV1Service,
	random io.Reader,
) *operatorAdminV1Bridge {
	return &operatorAdminV1Bridge{
		coordinator:  coordinator,
		service:      service,
		random:       random,
		partners:     make(map[string]operatorAdminV1BridgePartnerBinding),
		observations: make(map[string]operatorAdminV1BridgeObservationBinding),
		selections:   make(map[string]operatorAdminV1BridgeSelectionBinding),
		candidates:   make(map[string]operatorAdminV1BridgeCandidateBinding),
		operationIDs: make(map[[32]byte]struct{}),
		operations:   make(map[string]struct{}),
	}
}

func (backend *serviceBackend) operatorAdminV1Bridge() *operatorAdminV1Bridge {
	if backend == nil {
		return nil
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.closed || !backend.runClaimed || !backend.serviceStarted {
		return nil
	}
	return backend.operatorAdmin
}

func (backend *serviceBackend) OperatorAdminV1Snapshot(ctx context.Context) (OperatorAdminV1Snapshot, string) {
	bridge := backend.operatorAdminV1Bridge()
	if bridge == nil {
		return OperatorAdminV1Snapshot{}, "admin_boundary_unavailable"
	}
	snapshot, failure := bridge.snapshotOperatorAdminV1(ctx)
	if failure != "" {
		return OperatorAdminV1Snapshot{}, failure
	}
	return exportOperatorAdminV1Snapshot(snapshot), ""
}

func (backend *serviceBackend) OperatorAdminV1Open(ctx context.Context, duration time.Duration) (OperatorAdminV1Transition, string) {
	bridge := backend.operatorAdminV1Bridge()
	if bridge == nil {
		return OperatorAdminV1Transition{}, "admin_boundary_unavailable"
	}
	transition, failure := bridge.openOperatorAdminV1(ctx, duration)
	return exportOperatorAdminV1Transition(transition), failure
}

func (backend *serviceBackend) OperatorAdminV1Close(ctx context.Context) (OperatorAdminV1Transition, string) {
	bridge := backend.operatorAdminV1Bridge()
	if bridge == nil {
		return OperatorAdminV1Transition{}, "admin_boundary_unavailable"
	}
	transition, failure := bridge.closeOperatorAdminV1(ctx)
	return exportOperatorAdminV1Transition(transition), failure
}

func (backend *serviceBackend) OperatorAdminV1Select(ctx context.Context, reference, expectedSKI string) (string, OperatorAdminV1Transition, string) {
	bridge := backend.operatorAdminV1Bridge()
	if bridge == nil {
		return "", OperatorAdminV1Transition{}, "admin_boundary_unavailable"
	}
	selection, transition, failure := bridge.selectOperatorAdminV1(ctx, reference, expectedSKI)
	return selection, exportOperatorAdminV1Transition(transition), failure
}

func (backend *serviceBackend) OperatorAdminV1Connect(ctx context.Context, reference string) (OperatorAdminV1Transition, string) {
	bridge := backend.operatorAdminV1Bridge()
	if bridge == nil {
		return OperatorAdminV1Transition{}, "admin_boundary_unavailable"
	}
	transition, failure := bridge.connectOperatorAdminV1(ctx, reference)
	return exportOperatorAdminV1Transition(transition), failure
}

func (backend *serviceBackend) OperatorAdminV1Confirm(ctx context.Context, reference, expectedSKI string) (OperatorAdminV1Transition, string) {
	bridge := backend.operatorAdminV1Bridge()
	if bridge == nil {
		return OperatorAdminV1Transition{}, "admin_boundary_unavailable"
	}
	transition, failure := bridge.confirmOperatorAdminV1(ctx, reference, expectedSKI)
	return exportOperatorAdminV1Transition(transition), failure
}

func (backend *serviceBackend) OperatorAdminV1Cancel(ctx context.Context, reference string) (OperatorAdminV1Transition, string) {
	bridge := backend.operatorAdminV1Bridge()
	if bridge == nil {
		return OperatorAdminV1Transition{}, "admin_boundary_unavailable"
	}
	transition, failure := bridge.cancelOperatorAdminV1(ctx, reference)
	return exportOperatorAdminV1Transition(transition), failure
}

func (backend *serviceBackend) OperatorAdminV1RetryTrusted(ctx context.Context, reference string) (OperatorAdminV1Transition, string) {
	bridge := backend.operatorAdminV1Bridge()
	if bridge == nil {
		return OperatorAdminV1Transition{}, "admin_boundary_unavailable"
	}
	transition, failure := bridge.retryTrustedOperatorAdminV1(ctx, reference)
	return exportOperatorAdminV1Transition(transition), failure
}

func (backend *serviceBackend) OperatorAdminV1Untrust(ctx context.Context, reference string) (OperatorAdminV1Transition, string) {
	bridge := backend.operatorAdminV1Bridge()
	if bridge == nil {
		return OperatorAdminV1Transition{}, "admin_boundary_unavailable"
	}
	transition, failure := bridge.untrustOperatorAdminV1(ctx, reference)
	return exportOperatorAdminV1Transition(transition), failure
}

func exportOperatorAdminV1Transition(source operatorAdminV1BridgeTransition) OperatorAdminV1Transition {
	return OperatorAdminV1Transition{Outcome: source.outcome, Changed: source.changed}
}

func exportOperatorAdminV1Snapshot(source operatorAdminV1BridgeSnapshot) OperatorAdminV1Snapshot {
	return OperatorAdminV1Snapshot{
		CapturedAt: source.capturedAt, Status: source.status, Window: source.window,
		WindowDeadline: source.windowDeadline, Register: source.register, Listener: source.listener,
		Discovery: source.discovery, Degraded: source.degraded,
		Trusted: exportOperatorAdminV1Facts(source.trusted), Connected: exportOperatorAdminV1Facts(source.connected),
		Discovered: exportOperatorAdminV1Facts(source.discovered), Candidates: exportOperatorAdminV1Facts(source.candidates),
	}
}

func exportOperatorAdminV1Facts(source []operatorAdminV1BridgeFact) []OperatorAdminV1Fact {
	if len(source) == 0 {
		return nil
	}
	result := make([]OperatorAdminV1Fact, len(source))
	for index, fact := range source {
		result[index] = OperatorAdminV1Fact{
			Reference: fact.reference, SKI: fact.ski, Endpoint: fact.endpoint, TrustState: fact.trustState,
			ConnectionState: fact.connectionState, SHIPID: fact.shipID, LastSeen: fact.lastSeen,
			ObservationRevision: fact.observationRevision, Name: fact.name, Identifier: fact.identifier,
			Brand: fact.brand, Type: fact.typeName, Model: fact.model, State: fact.state,
			ExpiresAt: fact.expiresAt, AssociationComplete: fact.associationComplete,
		}
	}
	return result
}

func (bridge *operatorAdminV1Bridge) snapshotOperatorAdminV1(ctx context.Context) (operatorAdminV1BridgeSnapshot, string) {
	bridge.serial.Lock()
	defer bridge.serial.Unlock()
	if failure := bridge.available(ctx); failure != "" {
		return operatorAdminV1BridgeSnapshot{}, failure
	}
	raw, failure := bridge.captureRawSnapshot()
	if failure != "" {
		return operatorAdminV1BridgeSnapshot{}, failure
	}
	bridge.pruneSelectionsLocked()
	return bridge.sanitizeSnapshot(raw)
}

func (bridge *operatorAdminV1Bridge) openOperatorAdminV1(ctx context.Context, duration time.Duration) (operatorAdminV1BridgeTransition, string) {
	bridge.serial.Lock()
	defer bridge.serial.Unlock()
	if failure := bridge.available(ctx); failure != "" {
		return operatorAdminV1BridgeTransition{}, failure
	}
	key, ok := bridge.newOperationReference()
	if !ok {
		return operatorAdminV1BridgeTransition{}, "admin_boundary_unavailable"
	}
	defer delete(bridge.operations, key)
	before := bridge.coordinator.state()
	outcome := bridge.coordinator.openPairingWindow(operatorAdminV1BridgeContext(ctx), key, duration)
	if outcome == "open_empty" {
		return operatorAdminV1BridgeTransition{outcome: outcome, changed: before != bridge.coordinator.state()}, ""
	}
	if failure := mapOperatorAdminV1CoordinatorOutcome(outcome); failure != "" {
		return operatorAdminV1BridgeTransition{}, failure
	}
	return operatorAdminV1BridgeTransition{}, "unknown_state"
}

func (bridge *operatorAdminV1Bridge) closeOperatorAdminV1(ctx context.Context) (operatorAdminV1BridgeTransition, string) {
	bridge.serial.Lock()
	defer bridge.serial.Unlock()
	if failure := bridge.available(ctx); failure != "" {
		return operatorAdminV1BridgeTransition{}, failure
	}
	key, ok := bridge.newOperationReference()
	if !ok {
		return operatorAdminV1BridgeTransition{}, "admin_boundary_unavailable"
	}
	defer delete(bridge.operations, key)
	before := bridge.coordinator.state()
	outcome := bridge.coordinator.closePairingWindow(operatorAdminV1BridgeContext(ctx), key)
	if outcome != "pairing_closed" {
		if failure := mapOperatorAdminV1CoordinatorOutcome(outcome); failure != "" {
			return operatorAdminV1BridgeTransition{}, failure
		}
		return operatorAdminV1BridgeTransition{}, "unknown_state"
	}
	changed := before != bridge.coordinator.state()
	if changed {
		bridge.selections = make(map[string]operatorAdminV1BridgeSelectionBinding)
		bridge.candidates = make(map[string]operatorAdminV1BridgeCandidateBinding)
	}
	return operatorAdminV1BridgeTransition{outcome: outcome, changed: changed}, ""
}

func (bridge *operatorAdminV1Bridge) selectOperatorAdminV1(
	ctx context.Context,
	reference,
	expectedSKI string,
) (string, operatorAdminV1BridgeTransition, string) {
	bridge.serial.Lock()
	defer bridge.serial.Unlock()
	if failure := bridge.available(ctx); failure != "" {
		return "", operatorAdminV1BridgeTransition{}, failure
	}
	binding, exists := bridge.observations[reference]
	if !exists || binding.ski != expectedSKI || !validOperatorAdminV1BridgeSKI(expectedSKI) {
		return "", operatorAdminV1BridgeTransition{}, "observation_stale"
	}
	if !bridge.coordinator.operatorAdminV1ObservationCurrent(binding.candidateRef, binding.ski, binding.revision) {
		return "", operatorAdminV1BridgeTransition{}, "observation_stale"
	}
	bridge.pruneSelectionsLocked()
	if len(bridge.selections) >= operatorAdminV1BridgeMaximumReferences {
		return "", operatorAdminV1BridgeTransition{}, "admin_boundary_unavailable"
	}
	selectionReference, ok := bridge.newOpaqueReference(bridge.currentReferences())
	if !ok {
		return "", operatorAdminV1BridgeTransition{}, "admin_boundary_unavailable"
	}
	selectionState, outcome := bridge.coordinator.reserveOperatorAdminV1Selection(binding.candidateRef, expectedSKI)
	if outcome != "" {
		return "", operatorAdminV1BridgeTransition{}, mapOperatorAdminV1CoordinatorOutcome(outcome)
	}
	reservation, err := callOperatorAdminV1Select(bridge.service, binding.candidateRef, expectedSKI)
	if err != nil {
		bridge.coordinator.finishOperatorAdminV1Selection(selectionState, false)
		return "", operatorAdminV1BridgeTransition{changed: true}, mapOperatorAdminV1SelectError(err)
	}
	if !reservation.Valid() {
		bridge.coordinator.finishOperatorAdminV1Selection(selectionState, false)
		return "", operatorAdminV1BridgeTransition{changed: true}, "unknown_state"
	}
	if outcome := bridge.coordinator.finishOperatorAdminV1Selection(selectionState, true); outcome != "candidate_selected" {
		return "", operatorAdminV1BridgeTransition{changed: true}, mapOperatorAdminV1CoordinatorOutcome(outcome)
	}
	bridge.selections[selectionReference] = operatorAdminV1BridgeSelectionBinding{
		candidateRef: binding.candidateRef,
		ski:          expectedSKI,
		reservation:  reservation,
	}
	return selectionReference, operatorAdminV1BridgeTransition{outcome: "candidate_selected", changed: true}, ""
}

func (bridge *operatorAdminV1Bridge) connectOperatorAdminV1(ctx context.Context, reference string) (operatorAdminV1BridgeTransition, string) {
	bridge.serial.Lock()
	defer bridge.serial.Unlock()
	if failure := bridge.available(ctx); failure != "" {
		return operatorAdminV1BridgeTransition{}, failure
	}
	binding, exists := bridge.selections[reference]
	if !exists || binding.consumed || !binding.reservation.Valid() ||
		!bridge.coordinator.operatorAdminV1SelectionCurrent(binding.candidateRef, binding.ski) {
		return operatorAdminV1BridgeTransition{}, "observation_stale"
	}
	// Retire the process-local connect authority before invoking the effect.
	// This keeps a panic or returned error from reopening the same outbound dial.
	delete(bridge.selections, reference)
	if err := callOperatorAdminV1Connect(bridge.service, binding.reservation); err != nil {
		return operatorAdminV1BridgeTransition{changed: true}, mapOperatorAdminV1ConnectError(err)
	}
	return operatorAdminV1BridgeTransition{outcome: "connection_started", changed: true}, ""
}

func (bridge *operatorAdminV1Bridge) pruneSelectionsLocked() {
	for reference, binding := range bridge.selections {
		if binding.consumed || !bridge.coordinator.operatorAdminV1SelectionCurrent(binding.candidateRef, binding.ski) {
			delete(bridge.selections, reference)
		}
	}
}

func (bridge *operatorAdminV1Bridge) confirmOperatorAdminV1(
	ctx context.Context,
	reference,
	expectedSKI string,
) (operatorAdminV1BridgeTransition, string) {
	bridge.serial.Lock()
	defer bridge.serial.Unlock()
	if failure := bridge.available(ctx); failure != "" {
		return operatorAdminV1BridgeTransition{}, failure
	}
	binding, exists := bridge.candidates[reference]
	if !exists {
		return operatorAdminV1BridgeTransition{}, "candidate_expired"
	}
	if binding.fingerprint != expectedSKI || !validOperatorAdminV1BridgeSKI(expectedSKI) {
		return operatorAdminV1BridgeTransition{}, "identity_mismatch"
	}
	if !bridge.coordinator.operatorAdminV1CandidateCurrent(binding, true) {
		return operatorAdminV1BridgeTransition{}, "candidate_expired"
	}
	key, ok := bridge.newOperationReference()
	if !ok {
		return operatorAdminV1BridgeTransition{}, "admin_boundary_unavailable"
	}
	defer delete(bridge.operations, key)
	outcome := bridge.coordinator.confirm(
		operatorAdminV1BridgeContext(ctx), key, binding.fingerprint, binding.nonce,
		binding.expiresAt, binding.connection, binding.storeGeneration,
	)
	if failure := mapOperatorAdminV1CoordinatorOutcome(outcome); failure != "" {
		return operatorAdminV1BridgeTransition{}, failure
	}
	if outcome != "transient_trusted" {
		delete(bridge.candidates, reference)
	}
	return operatorAdminV1BridgeTransition{outcome: outcome, changed: true}, ""
}

func (bridge *operatorAdminV1Bridge) cancelOperatorAdminV1(ctx context.Context, reference string) (operatorAdminV1BridgeTransition, string) {
	bridge.serial.Lock()
	defer bridge.serial.Unlock()
	if failure := bridge.available(ctx); failure != "" {
		return operatorAdminV1BridgeTransition{}, failure
	}
	binding, exists := bridge.candidates[reference]
	if !exists || !bridge.coordinator.operatorAdminV1CandidateCurrent(binding, false) {
		return operatorAdminV1BridgeTransition{}, "candidate_expired"
	}
	key, ok := bridge.newOperationReference()
	if !ok {
		return operatorAdminV1BridgeTransition{}, "admin_boundary_unavailable"
	}
	defer delete(bridge.operations, key)
	outcome := bridge.coordinator.cancel(
		operatorAdminV1BridgeContext(ctx), key, binding.nonce, binding.connection, binding.storeGeneration,
	)
	if failure := mapOperatorAdminV1CoordinatorOutcome(outcome); failure != "" {
		return operatorAdminV1BridgeTransition{}, failure
	}
	delete(bridge.candidates, reference)
	bridge.selections = make(map[string]operatorAdminV1BridgeSelectionBinding)
	return operatorAdminV1BridgeTransition{outcome: outcome, changed: true}, ""
}

func (bridge *operatorAdminV1Bridge) retryTrustedOperatorAdminV1(ctx context.Context, reference string) (operatorAdminV1BridgeTransition, string) {
	bridge.serial.Lock()
	defer bridge.serial.Unlock()
	if failure := bridge.available(ctx); failure != "" {
		return operatorAdminV1BridgeTransition{}, failure
	}
	binding, exists := bridge.partners[reference]
	if !exists || !binding.durable {
		return operatorAdminV1BridgeTransition{}, "trust_denied"
	}
	if failure := bridge.coordinator.operatorAdminV1RetryFailure(binding.association, binding.ski); failure != "" {
		return operatorAdminV1BridgeTransition{}, failure
	}
	if err := callOperatorAdminV1Retry(bridge.service, binding.ski); err != nil {
		return operatorAdminV1BridgeTransition{}, mapOperatorAdminV1RetryError(err)
	}
	return operatorAdminV1BridgeTransition{outcome: "retry_requested", changed: true}, ""
}

func (bridge *operatorAdminV1Bridge) untrustOperatorAdminV1(ctx context.Context, reference string) (operatorAdminV1BridgeTransition, string) {
	bridge.serial.Lock()
	defer bridge.serial.Unlock()
	if failure := bridge.available(ctx); failure != "" {
		return operatorAdminV1BridgeTransition{}, failure
	}
	binding, exists := bridge.partners[reference]
	if !exists || !binding.durable {
		return operatorAdminV1BridgeTransition{}, "trust_denied"
	}
	request, failure := bridge.currentRevocationRequest(binding)
	if failure != "" {
		return operatorAdminV1BridgeTransition{}, failure
	}
	defer delete(bridge.operationIDs, request.operationID)
	outcome := bridge.coordinator.revoke(operatorAdminV1BridgeContext(ctx), request)
	if mapped := mapOperatorAdminV1CoordinatorOutcome(outcome); mapped != "" {
		return operatorAdminV1BridgeTransition{}, mapped
	}
	delete(bridge.partners, reference)
	return operatorAdminV1BridgeTransition{outcome: outcome, changed: true}, ""
}

func (bridge *operatorAdminV1Bridge) closeOperatorAdminV1Bridge() {
	if bridge == nil {
		return
	}
	bridge.serial.Lock()
	bridge.closed = true
	bridge.partners = nil
	bridge.observations = nil
	bridge.selections = nil
	bridge.candidates = nil
	bridge.operationIDs = nil
	bridge.operations = nil
	bridge.service = nil
	bridge.coordinator = nil
	bridge.serial.Unlock()
}

func (bridge *operatorAdminV1Bridge) available(ctx context.Context) string {
	if bridge == nil || bridge.closed || bridge.coordinator == nil || bridge.service == nil || bridge.random == nil {
		return "admin_boundary_unavailable"
	}
	if err := operatorAdminV1BridgeContext(ctx).Err(); err != nil {
		return "unknown_state"
	}
	return ""
}

func (bridge *operatorAdminV1Bridge) captureRawSnapshot() (operatorAdminV1BridgeRawSnapshot, string) {
	coordinator := bridge.coordinator
	coordinator.mu.Lock()
	now := coordinator.now()
	coordinator.expireLocked(now)
	if coordinator.phase == firstTrustDisabled || coordinator.reopening || coordinator.recoveryOperation != nil {
		coordinator.mu.Unlock()
		return operatorAdminV1BridgeRawSnapshot{}, "admin_boundary_unavailable"
	}
	register := "unknown"
	listener := "unknown"
	degraded := ""
	switch {
	case coordinator.pairingRegistrationFault:
		register, listener, degraded = "fault", "unavailable", "listener_unavailable"
	case coordinator.pairingRegistrationKnown && coordinator.pairingRegistrationEnabled:
		register, listener = "enabled", "ready"
	case coordinator.pairingRegistrationKnown:
		register, listener = "disabled", "ready"
	}
	discovery := coordinator.candidateSnapshotState
	if discovery == "" {
		discovery = "empty"
	}
	if discovery == "invalid" && degraded == "" {
		degraded = "discovery_unavailable"
	}
	window := "closed"
	windowDeadline := time.Time{}
	if coordinator.window != nil && now.Before(coordinator.window.deadline) {
		window = "open"
		windowDeadline = coordinator.window.deadline
	}
	raw := operatorAdminV1BridgeRawSnapshot{
		capturedAt: now, status: operatorAdminV1CoordinatorStatusLocked(coordinator), window: window,
		windowDeadline: windowDeadline, register: register, listener: listener, discovery: discovery, degraded: degraded,
	}
	seenTrusted := make(map[string]struct{})
	for _, association := range coordinator.controlView.associations {
		if !firstTrustAssociationUsable(association, coordinator.controlView.control.associationLineage) ||
			coordinator.firstTrustTombstonedLocked(association) || len(association.subject) != 20 {
			continue
		}
		ski := hex.EncodeToString(association.subject)
		if _, duplicate := seenTrusted[ski]; duplicate {
			coordinator.mu.Unlock()
			return operatorAdminV1BridgeRawSnapshot{}, "unknown_state"
		}
		seenTrusted[ski] = struct{}{}
		raw.trusted = append(raw.trusted, operatorAdminV1BridgeRawPartner{
			target: hex.EncodeToString(association.reference[:]), ski: ski, association: association.reference,
			durable: true, trustState: "trusted", shipID: association.service,
		})
	}
	for _, candidate := range coordinator.discoveredCandidates {
		if candidate.lifecycle != "visible" {
			continue
		}
		raw.discovered = append(raw.discovered, operatorAdminV1BridgeRawObservation{
			target:       candidate.ref + "\x00" + candidate.claimedSKI + "\x00" + strconv.FormatUint(candidate.revision, 10),
			candidateRef: candidate.ref, ski: candidate.claimedSKI, revision: candidate.revision,
			lastSeen: candidate.lastReceived, name: candidate.name, identifier: candidate.identifier,
			brand: candidate.brand, typeName: candidate.typeName, model: candidate.model, expiresAt: windowDeadline,
		})
	}
	if current := coordinator.currentCandidate; current != nil &&
		(coordinator.phase == firstTrustCandidatePending || coordinator.phase == firstTrustTransientTrusted) {
		identityBound := !current.tlsRequired || current.tlsBound
		associationComplete := current.shipID != "" && identityBound
		visible := identityBound
		if visible && len(current.remote) == 20 {
			state := "association_incomplete"
			switch {
			case current.transientAuthorized:
				state = "transient_trusted"
			case associationComplete:
				state = "association_complete"
			}
			binding := operatorAdminV1BridgeCandidateBinding{
				fingerprint: hex.EncodeToString(current.remote), nonce: current.nonce, expiresAt: current.expiresAt,
				connection: current.connection, storeGeneration: current.storeGeneration,
				state: state, associationComplete: associationComplete,
			}
			raw.candidate = &binding
		}
	}
	effects := coordinator.effects
	coordinator.mu.Unlock()

	if facade, ok := effects.(*firstTrustFacade); ok {
		raw.connected = facade.operatorAdminV1ConnectedSnapshot()
	}
	if len(raw.trusted) > operatorAdminV1BridgeMaximumRows || len(raw.connected) > operatorAdminV1BridgeMaximumRows ||
		len(raw.discovered) > operatorAdminV1BridgeMaximumRows {
		return operatorAdminV1BridgeRawSnapshot{}, "admin_boundary_unavailable"
	}
	sort.Slice(raw.trusted, func(left, right int) bool {
		if raw.trusted[left].ski == raw.trusted[right].ski {
			return raw.trusted[left].target < raw.trusted[right].target
		}
		return raw.trusted[left].ski < raw.trusted[right].ski
	})
	sort.Slice(raw.connected, func(left, right int) bool { return raw.connected[left].ski < raw.connected[right].ski })
	sort.Slice(raw.discovered, func(left, right int) bool {
		return raw.discovered[left].candidateRef < raw.discovered[right].candidateRef
	})
	return raw, ""
}

func operatorAdminV1CoordinatorStatusLocked(coordinator *firstTrustCoordinator) string {
	if coordinator.pairingRegistrationFault {
		return "DISABLED"
	}
	switch coordinator.phase {
	case firstTrustPairingClosed:
		return "PAIRING_CLOSED"
	case firstTrustOpenEmpty:
		return "OPEN_EMPTY"
	case firstTrustCandidatePending:
		return "CANDIDATE_PENDING"
	case firstTrustTransientTrusted:
		return "TRANSIENT_TRUSTED"
	case firstTrustCommitting:
		return "COMMITTING"
	default:
		return "DISABLED"
	}
}

func (bridge *operatorAdminV1Bridge) sanitizeSnapshot(raw operatorAdminV1BridgeRawSnapshot) (operatorAdminV1BridgeSnapshot, string) {
	partnerRefs := make(map[string]string, len(raw.trusted))
	partnerBindings := make(map[string]operatorAdminV1BridgePartnerBinding, len(raw.trusted))
	observationRefs := make(map[string]string, len(raw.discovered))
	observationBindings := make(map[string]operatorAdminV1BridgeObservationBinding, len(raw.discovered))
	candidateRefs := make(map[string]string, 1)
	candidateBindings := make(map[string]operatorAdminV1BridgeCandidateBinding, 1)
	used := bridge.currentReferences()

	for _, partner := range raw.trusted {
		reference := bridge.referenceForTarget(partner.target, bridge.partners, used)
		if reference == "" {
			return operatorAdminV1BridgeSnapshot{}, "admin_boundary_unavailable"
		}
		used[reference] = struct{}{}
		partnerRefs[partner.target] = reference
		partnerBindings[reference] = operatorAdminV1BridgePartnerBinding{
			association: partner.association, ski: partner.ski, durable: partner.durable,
		}
	}
	for _, observation := range raw.discovered {
		reference := bridge.referenceForObservation(observation, used)
		if reference == "" {
			return operatorAdminV1BridgeSnapshot{}, "admin_boundary_unavailable"
		}
		used[reference] = struct{}{}
		observationRefs[observation.target] = reference
		observationBindings[reference] = operatorAdminV1BridgeObservationBinding{
			candidateRef: observation.candidateRef, ski: observation.ski, revision: observation.revision,
		}
	}
	if raw.candidate != nil {
		target := bridge.candidateTarget(*raw.candidate)
		reference := bridge.referenceForCandidate(target, *raw.candidate, used)
		if reference == "" {
			return operatorAdminV1BridgeSnapshot{}, "admin_boundary_unavailable"
		}
		candidateRefs[target] = reference
		candidateBindings[reference] = *raw.candidate
	}

	snapshot := operatorAdminV1BridgeSnapshot{
		capturedAt: raw.capturedAt, status: raw.status, window: raw.window, windowDeadline: raw.windowDeadline,
		register: raw.register, listener: raw.listener, discovery: raw.discovery, degraded: raw.degraded,
		trusted: make([]operatorAdminV1BridgeFact, len(raw.trusted)), connected: make([]operatorAdminV1BridgeFact, len(raw.connected)),
		discovered: make([]operatorAdminV1BridgeFact, len(raw.discovered)),
	}
	for index, partner := range raw.trusted {
		snapshot.trusted[index] = operatorAdminV1BridgeFact{
			reference: partnerRefs[partner.target], ski: partner.ski, trustState: partner.trustState,
			shipID: partner.shipID, lastSeen: partner.lastSeen,
		}
	}
	for index, connected := range raw.connected {
		snapshot.connected[index] = operatorAdminV1BridgeFact{
			ski: connected.ski, endpoint: connected.endpoint, trustState: connected.trustState,
			connectionState: connected.connectionState, shipID: connected.shipID, lastSeen: connected.lastSeen,
		}
	}
	for index, observation := range raw.discovered {
		snapshot.discovered[index] = operatorAdminV1BridgeFact{
			reference: observationRefs[observation.target], ski: observation.ski,
			observationRevision: observation.revision, lastSeen: observation.lastSeen, name: observation.name,
			identifier: observation.identifier, brand: observation.brand, typeName: observation.typeName,
			model: observation.model, expiresAt: observation.expiresAt,
		}
	}
	if raw.candidate != nil {
		target := bridge.candidateTarget(*raw.candidate)
		snapshot.candidates = []operatorAdminV1BridgeFact{{
			reference: candidateRefs[target], ski: raw.candidate.fingerprint, state: raw.candidate.state,
			expiresAt: raw.candidate.expiresAt, associationComplete: raw.candidate.associationComplete,
		}}
	}
	bridge.partners = partnerBindings
	bridge.observations = observationBindings
	bridge.candidates = candidateBindings
	return snapshot, ""
}

func (bridge *operatorAdminV1Bridge) referenceForTarget(
	target string,
	existing map[string]operatorAdminV1BridgePartnerBinding,
	used map[string]struct{},
) string {
	for reference, binding := range existing {
		bindingTarget := "legacy:" + binding.ski
		if binding.durable {
			bindingTarget = hex.EncodeToString(binding.association[:])
		}
		if bindingTarget == target {
			return reference
		}
	}
	reference, ok := bridge.newOpaqueReference(used)
	if !ok {
		return ""
	}
	return reference
}

func (bridge *operatorAdminV1Bridge) referenceForObservation(
	observation operatorAdminV1BridgeRawObservation,
	used map[string]struct{},
) string {
	for reference, binding := range bridge.observations {
		if binding.candidateRef == observation.candidateRef && binding.ski == observation.ski && binding.revision == observation.revision {
			return reference
		}
	}
	reference, ok := bridge.newOpaqueReference(used)
	if !ok {
		return ""
	}
	return reference
}

func (bridge *operatorAdminV1Bridge) referenceForCandidate(
	target string,
	binding operatorAdminV1BridgeCandidateBinding,
	used map[string]struct{},
) string {
	for reference, current := range bridge.candidates {
		if bridge.candidateTarget(current) == target {
			return reference
		}
	}
	reference, ok := bridge.newOpaqueReference(used)
	if !ok {
		return ""
	}
	return reference
}

func (bridge *operatorAdminV1Bridge) candidateTarget(binding operatorAdminV1BridgeCandidateBinding) string {
	return binding.fingerprint + "\x00" + binding.nonce + "\x00" + binding.expiresAt.UTC().Format(time.RFC3339Nano) +
		"\x00" + strconv.FormatUint(binding.connection, 10) + "\x00" + strconv.FormatUint(binding.storeGeneration, 10)
}

func (bridge *operatorAdminV1Bridge) currentReferences() map[string]struct{} {
	result := make(map[string]struct{}, len(bridge.partners)+len(bridge.observations)+len(bridge.selections)+len(bridge.candidates)+len(bridge.operations))
	for reference := range bridge.partners {
		result[reference] = struct{}{}
	}
	for reference := range bridge.observations {
		result[reference] = struct{}{}
	}
	for reference := range bridge.selections {
		result[reference] = struct{}{}
	}
	for reference := range bridge.candidates {
		result[reference] = struct{}{}
	}
	for reference := range bridge.operations {
		result[reference] = struct{}{}
	}
	return result
}

func (bridge *operatorAdminV1Bridge) newOperationReference() (string, bool) {
	if len(bridge.operations) >= operatorAdminV1BridgeMaximumReferences {
		return "", false
	}
	reference, ok := bridge.newOpaqueReference(bridge.currentReferences())
	if !ok {
		return "", false
	}
	bridge.operations[reference] = struct{}{}
	return reference, true
}

func (bridge *operatorAdminV1Bridge) newOpaqueReference(used map[string]struct{}) (string, bool) {
	for attempt := 0; attempt < operatorAdminV1BridgeReferenceAttempts; attempt++ {
		var token [32]byte
		if bridge.random == nil {
			return "", false
		}
		if _, err := io.ReadFull(bridge.random, token[:]); err != nil || token == [32]byte{} {
			continue
		}
		reference := hex.EncodeToString(token[:])
		if _, collision := used[reference]; collision {
			continue
		}
		return reference, true
	}
	return "", false
}

func (bridge *operatorAdminV1Bridge) currentRevocationRequest(
	binding operatorAdminV1BridgePartnerBinding,
) (firstTrustRevocationRequest, string) {
	coordinator := bridge.coordinator
	coordinator.mu.Lock()
	var association *firstTrustAssociationRecord
	for index := range coordinator.controlView.associations {
		candidate := &coordinator.controlView.associations[index]
		if candidate.reference == binding.association &&
			firstTrustAssociationUsable(*candidate, coordinator.controlView.control.associationLineage) &&
			!coordinator.firstTrustTombstonedLocked(*candidate) && hex.EncodeToString(candidate.subject) == binding.ski {
			copy := *candidate
			association = &copy
			break
		}
	}
	if association == nil {
		coordinator.mu.Unlock()
		return firstTrustRevocationRequest{}, "trust_denied"
	}
	highWater := coordinator.controlView.control.operationHighWater
	request := firstTrustRevocationRequest{
		associationRef:         association.reference,
		associationLineage:     coordinator.controlView.control.associationLineage,
		expectedGeneration:     coordinator.controlView.manifest.current,
		expectedManifestEpoch:  coordinator.controlView.manifest.epoch,
		expectedManifestSHA256: coordinator.controlView.manifest.sha256,
		expectedControlEpoch:   coordinator.controlView.control.controlEpoch,
	}
	coordinator.mu.Unlock()
	operationID, ok := bridge.newOperationID(highWater)
	if !ok {
		return firstTrustRevocationRequest{}, "admin_boundary_unavailable"
	}
	request.operationID = operationID
	return request, ""
}

func (coordinator *firstTrustCoordinator) operatorAdminV1CandidateCurrent(binding operatorAdminV1BridgeCandidateBinding, requireComplete bool) bool {
	if coordinator == nil || !validOperatorAdminV1BridgeSKI(binding.fingerprint) {
		return false
	}
	fingerprint, nonce, expiresAt, connection, storeGeneration, complete, exists := coordinator.candidate()
	return exists && (!requireComplete || complete) && equalOperatorAdminV1CandidateBinding(binding, operatorAdminV1BridgeCandidateBinding{
		fingerprint:     fingerprint,
		nonce:           nonce,
		expiresAt:       expiresAt,
		connection:      connection,
		storeGeneration: storeGeneration,
	})
}

func (coordinator *firstTrustCoordinator) operatorAdminV1RetryFailure(associationRef [32]byte, expectedSKI string) string {
	if coordinator == nil || associationRef == [32]byte{} || !validOperatorAdminV1BridgeSKI(expectedSKI) {
		return "trust_denied"
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.recoveryStore == nil || coordinator.anchor == nil || coordinator.reconciliationRequiredLocked() ||
		coordinator.recoveryOperation != nil {
		return "admin_boundary_unavailable"
	}
	associationCurrent := false
	for _, association := range coordinator.controlView.associations {
		if association.reference == associationRef && firstTrustAssociationUsable(association, coordinator.controlView.control.associationLineage) &&
			!coordinator.firstTrustTombstonedLocked(association) && len(association.subject) == 20 &&
			hex.EncodeToString(association.subject) == expectedSKI {
			associationCurrent = true
			break
		}
	}
	if !associationCurrent {
		return "trust_denied"
	}
	scope := firstTrustRuntimeRetryScope(expectedSKI)
	_, record, exists := coordinator.firstTrustQuarantineLocked(scope)
	if !exists || !firstTrustQuarantineRecordValid(record, coordinator.backoffPolicy) {
		return "trust_denied"
	}
	switch record.state {
	case "RETRY_READY":
		return ""
	case "BACKOFF_ACTIVE":
		return "backoff_active"
	case "ADMIN_HOLD":
		return "terminal_quarantine"
	default:
		return "unknown_state"
	}
}

func (bridge *operatorAdminV1Bridge) newOperationID(highWater uint64) ([32]byte, bool) {
	if len(bridge.operationIDs) >= operatorAdminV1BridgeMaximumReferences {
		return [32]byte{}, false
	}
	for attempt := 0; attempt < operatorAdminV1BridgeReferenceAttempts; attempt++ {
		operationID, ok := firstTrustReadOrdinal(bridge.random)
		if _, collision := bridge.operationIDs[operationID]; ok && !collision && firstTrustOperationOrdinal(operationID) > highWater {
			bridge.operationIDs[operationID] = struct{}{}
			return operationID, true
		}
	}
	return [32]byte{}, false
}

func mapOperatorAdminV1CoordinatorOutcome(outcome string) string {
	switch outcome {
	case "open_empty", "candidate_selected", "connection_started", "trusted", "transient_trusted", "cancelled", "retry_requested", "revoked":
		return ""
	case "duration_out_of_range", "invalid_idempotency_key", "invalid_candidate_ref", "invalid_expected_ski":
		return "invalid_request"
	case "mutation_disabled", "idempotency_capacity", "candidate_queue_unavailable":
		return "admin_boundary_unavailable"
	case "pairing_closed", "stale_request":
		return "pairing_closed"
	case "candidate_unavailable", "candidate_consumed", "candidate_snapshot_invalid":
		return "observation_stale"
	case "candidate_ski_mismatch", "confirmation_mismatch":
		return "identity_mismatch"
	case "association_incomplete":
		return "association_incomplete"
	case "candidate_busy", "candidate_active", "window_conflict", "commit_in_progress", "operation_in_progress":
		return "candidate_busy"
	case "pairing_registration_failed", "transport_gate_unavailable":
		return "listener_unavailable"
	case "reconciliation_required", "admin_hold", "quarantine_capacity", "tombstone_capacity":
		return "terminal_quarantine"
	case "backoff_active":
		return "backoff_active"
	case "commit_not_published", "commit_durability_unknown", "retry_state_failed_closed", "ready_transition_failed_closed", "revocation_withdrawal_incomplete":
		return "persistence_failure"
	case "already_trusted", "association_revoked", "revocation_conflict", "idempotency_expired":
		return "trust_denied"
	case "request_cancelled", "idempotency_conflict":
		return "unknown_state"
	default:
		return "unknown_state"
	}
}

func mapOperatorAdminV1SelectError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, shipapi.ErrPairingCandidateUnavailable), errors.Is(err, shipapi.ErrPairingCandidateConsumed):
		return "observation_stale"
	case errors.Is(err, shipapi.ErrPairingCandidateSKIMismatch):
		return "identity_mismatch"
	case errors.Is(err, shipapi.ErrPairingCandidateActive):
		return "candidate_busy"
	case errors.Is(err, shipapi.ErrOutgoingAttemptGateRequired):
		return "listener_unavailable"
	case errors.Is(err, shipapi.ErrRemoteAlreadyTrusted):
		return "trust_denied"
	default:
		return "unknown_state"
	}
}

func mapOperatorAdminV1ConnectError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, shipapi.ErrPairingCandidateReservationStale), errors.Is(err, shipapi.ErrPairingCandidateReservationUnavailable):
		return "observation_stale"
	case errors.Is(err, shipapi.ErrPairingCandidateAlreadyConnecting):
		return "candidate_busy"
	case errors.Is(err, shipapi.ErrOutgoingAttemptGateRequired):
		return "listener_unavailable"
	case errors.Is(err, shipapi.ErrRemoteAlreadyTrusted):
		return "trust_denied"
	default:
		return "unknown_state"
	}
}

func mapOperatorAdminV1RetryError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, shipapi.ErrInvalidRemoteSKI):
		return "invalid_request"
	case errors.Is(err, shipapi.ErrTrustedRemoteRetryUnavailable):
		return "discovery_unavailable"
	case errors.Is(err, shipapi.ErrTrustedRemoteRetryNotTrusted):
		return "trust_denied"
	case errors.Is(err, shipapi.ErrTrustedRemoteRetryConnected), errors.Is(err, shipapi.ErrTrustedRemoteRetryBusy):
		return "candidate_busy"
	case errors.Is(err, shipapi.ErrTrustedRemoteObservationStale):
		return "observation_stale"
	case errors.Is(err, shipapi.ErrOutgoingAttemptGateRequired):
		return "listener_unavailable"
	case errors.Is(err, shipapi.ErrRemoteAlreadyTrusted):
		return "trust_denied"
	default:
		return "unknown_state"
	}
}

func callOperatorAdminV1Select(
	service operatorAdminV1Service,
	candidateRef,
	expectedSKI string,
) (reservation shipapi.PairingCandidateReservation, err error) {
	defer func() {
		if recover() != nil {
			reservation = shipapi.PairingCandidateReservation{}
			err = errors.New("operator AdminV1 select panicked")
		}
	}()
	return service.SelectPairingCandidate(candidateRef, expectedSKI)
}

func callOperatorAdminV1Connect(service operatorAdminV1Service, reservation shipapi.PairingCandidateReservation) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("operator AdminV1 connect panicked")
		}
	}()
	return service.ConnectPairingCandidate(reservation)
}

func callOperatorAdminV1Retry(service operatorAdminV1Service, expectedSKI string) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("operator AdminV1 retry panicked")
		}
	}()
	return service.RetryTrustedRemote(expectedSKI)
}

func operatorAdminV1BridgeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func validOperatorAdminV1BridgeSKI(value string) bool {
	decoded, normalized, ok := decodeFirstTrustSKI(value)
	return ok && len(decoded) == 20 && normalized == value
}

func equalOperatorAdminV1CandidateBinding(left, right operatorAdminV1BridgeCandidateBinding) bool {
	return left.fingerprint == right.fingerprint && left.nonce == right.nonce && left.expiresAt.Equal(right.expiresAt) &&
		left.connection == right.connection && left.storeGeneration == right.storeGeneration
}
