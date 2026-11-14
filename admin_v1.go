package eebusruntime

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"sync"
	"time"
)

// AdminV1 is the operator surface for local pairing and trust administration; it is
// obtained only from NewOperatorRuntimeV1. Calls are serialized: one runs at a time, and a
// call whose context ends while it waits fails with AdminErrorCodeV1UnknownState. Every
// call fails with AdminErrorCodeV1AdminBoundaryUnavailable unless the runtime is enabled,
// started and not shutting down. Each call re-reads backend facts and rejects the whole
// fact set, without partial use, if any row is malformed. The pairing and trust model is
// specified in helianthus-docs-eebus.
type AdminV1 interface {
	// Snapshot advances the state revision when facts changed and issues handles only for
	// the requested view; handles from earlier revisions stop resolving.
	Snapshot(context.Context, AdminSnapshotRequestV1) (AdminSnapshotV1, *AdminErrorV1)
	// OpenPairingWindow admits new partners for the requested duration.
	OpenPairingWindow(context.Context, OpenPairingWindowRequestV1) (AdminMutationResultV1, *AdminErrorV1)
	// ClosePairingWindow stops admitting new partners before the window deadline.
	ClosePairingWindow(context.Context, ClosePairingWindowRequestV1) (AdminMutationResultV1, *AdminErrorV1)
	// Select resolves a discovered observation and returns the selection handle that
	// Connect consumes.
	Select(context.Context, SelectRequestV1) (AdminSelectionResultV1, *AdminErrorV1)
	// Connect starts an outbound connection to a selection; follow it through ActionID
	// and AdminSnapshotV1.ActiveAction.
	Connect(context.Context, ConnectRequestV1) (ConnectResultV1, *AdminErrorV1)
	// Confirm accepts an inbound candidate whose identity must equal ExpectedSKI.
	Confirm(context.Context, ConfirmRequestV1) (AdminMutationResultV1, *AdminErrorV1)
	// Cancel rejects an inbound candidate.
	Cancel(context.Context, CancelRequestV1) (AdminMutationResultV1, *AdminErrorV1)
	// RetryTrusted asks the backend to retry the connection to a trusted partner.
	RetryTrusted(context.Context, RetryTrustedRequestV1) (AdminMutationResultV1, *AdminErrorV1)
	// Untrust withdraws trust from a partner.
	Untrust(context.Context, UntrustRequestV1) (AdminMutationResultV1, *AdminErrorV1)
}

func newAdminBoundaryUnavailableV1() *AdminErrorV1 {
	return &AdminErrorV1{Code: AdminErrorCodeV1AdminBoundaryUnavailable}
}

func newOperatorAdminV1Reducer(
	now func() time.Time,
	random io.Reader,
	lifecycle operatorAdminV1Lifecycle,
	backend operatorAdminV1Backend,
) *operatorAdminV1Reducer {
	if now == nil {
		now = time.Now
	}
	serial := make(chan struct{}, 1)
	serial <- struct{}{}
	var bindingKey [32]byte
	bindingKeyReady := false
	if random != nil {
		_, err := io.ReadFull(random, bindingKey[:])
		bindingKeyReady = err == nil && bindingKey != [32]byte{}
	}
	return &operatorAdminV1Reducer{
		serial:          serial,
		now:             now,
		random:          random,
		bindingKey:      bindingKey,
		bindingKeyReady: bindingKeyReady,
		lifecycle:       lifecycle,
		backend:         backend,
		handles:         make(map[[32]byte]operatorAdminV1HandleRecord),
		byTarget:        make(map[operatorAdminV1HandleKind]map[string][32]byte),
		replays:         make(map[string]operatorAdminV1Replay),
	}
}

// Snapshot implements AdminV1. When the result holds a terminal active action, the action is
// acknowledged to the backend after the reducer lock is released.
func (admin *operatorAdminV1Reducer) Snapshot(ctx context.Context, request AdminSnapshotRequestV1) (AdminSnapshotV1, *AdminErrorV1) {
	ctx = operatorAdminV1Context(ctx)
	if failure := admin.acquire(ctx); failure != nil {
		return AdminSnapshotV1{}, failure
	}
	defer admin.release()
	if failure := admin.available(); failure != nil {
		return AdminSnapshotV1{}, failure
	}
	if !validOperatorAdminV1View(request.View) {
		return AdminSnapshotV1{}, operatorAdminV1Error(AdminErrorCodeV1InvalidRequest)
	}
	facts, failure := admin.backend.snapshotOperatorAdminV1(ctx)
	if failure != nil {
		return AdminSnapshotV1{}, normalizeOperatorAdminV1Error(failure)
	}
	if failure := validateOperatorAdminV1Facts(facts); failure != nil {
		return AdminSnapshotV1{}, failure
	}
	if failure := admin.available(); failure != nil {
		return AdminSnapshotV1{}, failure
	}

	admin.mu.Lock()
	if failure := admin.acceptFactsLocked(facts); failure != nil {
		admin.mu.Unlock()
		return AdminSnapshotV1{}, failure
	}
	admin.pruneHandlesLocked(admin.now())
	snapshot, snapshotFailure := admin.snapshotLocked(request.View)
	admin.mu.Unlock()
	if snapshotFailure == nil && snapshot.ActiveAction != nil && snapshot.ActiveAction.State == "terminal" {
		admin.backend.observeOperatorAdminV1ActiveAction(ctx, snapshot.ActiveAction.ActionID)
	}
	return snapshot, snapshotFailure
}

// OpenPairingWindow implements AdminV1; the requested duration is part of the idempotency
// binding, so a replay with another duration conflicts.
func (admin *operatorAdminV1Reducer) OpenPairingWindow(ctx context.Context, request OpenPairingWindowRequestV1) (AdminMutationResultV1, *AdminErrorV1) {
	var requestFailure *AdminErrorV1
	if request.Duration <= 0 || request.Duration > operatorAdminV1MaximumWindow {
		requestFailure = operatorAdminV1Error(AdminErrorCodeV1InvalidRequest)
	}
	binding := operatorAdminV1Binding("open", operatorAdminV1Uint64(uint64(request.Duration)))
	result, _, _, failure := admin.execute(
		ctx, request.MutationPreconditionV1, binding, requestFailure, nil,
		func(ctx context.Context, _ string) (string, operatorAdminV1Transition, *AdminErrorV1) {
			transition, failure := admin.backend.openOperatorAdminV1(ctx, request.Duration)
			return "", transition, failure
		},
		false,
	)
	return result, failure
}

// ClosePairingWindow implements AdminV1; its idempotency binding holds no target.
func (admin *operatorAdminV1Reducer) ClosePairingWindow(ctx context.Context, request ClosePairingWindowRequestV1) (AdminMutationResultV1, *AdminErrorV1) {
	binding := operatorAdminV1Binding("close")
	result, _, _, failure := admin.execute(
		ctx, request.MutationPreconditionV1, binding, nil, nil,
		func(ctx context.Context, _ string) (string, operatorAdminV1Transition, *AdminErrorV1) {
			transition, failure := admin.backend.closeOperatorAdminV1(ctx)
			return "", transition, failure
		},
		false,
	)
	return result, failure
}

// Select implements AdminV1. The selection handle is issued only after the effect succeeds and
// only if handle capacity was available before it ran.
func (admin *operatorAdminV1Reducer) Select(ctx context.Context, request SelectRequestV1) (AdminSelectionResultV1, *AdminErrorV1) {
	var requestFailure *AdminErrorV1
	if !validOperatorAdminV1SKI(request.ExpectedSKI) {
		requestFailure = operatorAdminV1Error(AdminErrorCodeV1InvalidRequest)
	}
	binding := operatorAdminV1Binding("select", request.Observation.token[:], []byte(request.ExpectedSKI))
	result, selection, _, failure := admin.execute(
		ctx, request.MutationPreconditionV1, binding, requestFailure,
		func(now time.Time) (string, *AdminErrorV1) {
			return admin.resolveHandleLocked(operatorAdminV1ObservationHandle, request.Observation.token, now)
		},
		func(ctx context.Context, target string) (string, operatorAdminV1Transition, *AdminErrorV1) {
			return admin.backend.selectOperatorAdminV1(ctx, target, request.ExpectedSKI)
		},
		true,
	)
	if failure != nil {
		return AdminSelectionResultV1{}, failure
	}
	return AdminSelectionResultV1{AdminMutationResultV1: result, Selection: selection}, nil
}

// Connect implements AdminV1. The PIN copy is handed to the backend only on success and is
// zeroed on every other path; an invalid action ID from the backend fails closed.
func (admin *operatorAdminV1Reducer) Connect(ctx context.Context, request ConnectRequestV1) (ConnectResultV1, *AdminErrorV1) {
	var pin *operatorAdminV1TransientPIN
	var requestFailure *AdminErrorV1
	handedOff := false
	if request.PIN != nil {
		owned, valid := newOperatorAdminV1TransientPIN(request.PIN)
		if !valid {
			requestFailure = operatorAdminV1Error(AdminErrorCodeV1InvalidRequest)
		} else {
			pin = &owned
			defer func() {
				if !handedOff {
					pin.clear()
				}
			}()
		}
	}
	binding, bindingFailure := admin.connectBinding(request.Selection.token, request.PIN)
	if requestFailure == nil {
		requestFailure = bindingFailure
	}
	result, _, actionID, failure := admin.execute(
		ctx, request.MutationPreconditionV1, binding, requestFailure,
		func(now time.Time) (string, *AdminErrorV1) {
			return admin.resolveHandleLocked(operatorAdminV1SelectionHandle, request.Selection.token, now)
		},
		func(ctx context.Context, target string) (string, operatorAdminV1Transition, *AdminErrorV1) {
			if pin != nil {
				transition, failure := admin.backend.connectOperatorAdminV1WithPIN(ctx, target, pin)
				if failure == nil {
					handedOff = true
				}
				return "", transition, failure
			}
			transition, failure := admin.backend.connectOperatorAdminV1(ctx, target)
			return "", transition, failure
		},
		false,
	)
	if failure != nil {
		return ConnectResultV1{}, failure
	}
	if !validOperatorAdminV1ActionID(actionID) {
		return ConnectResultV1{}, newAdminBoundaryUnavailableV1()
	}
	return ConnectResultV1{AdminMutationResultV1: result, ActionID: actionID}, nil
}

// Confirm implements AdminV1; the expected SKI is part of the idempotency binding.
func (admin *operatorAdminV1Reducer) Confirm(ctx context.Context, request ConfirmRequestV1) (AdminMutationResultV1, *AdminErrorV1) {
	var requestFailure *AdminErrorV1
	if !validOperatorAdminV1SKI(request.ExpectedSKI) {
		requestFailure = operatorAdminV1Error(AdminErrorCodeV1InvalidRequest)
	}
	binding := operatorAdminV1Binding("confirm", request.Candidate.token[:], []byte(request.ExpectedSKI))
	result, _, _, failure := admin.execute(
		ctx, request.MutationPreconditionV1, binding, requestFailure,
		func(now time.Time) (string, *AdminErrorV1) {
			return admin.resolveHandleLocked(operatorAdminV1CandidateHandle, request.Candidate.token, now)
		},
		func(ctx context.Context, target string) (string, operatorAdminV1Transition, *AdminErrorV1) {
			transition, failure := admin.backend.confirmOperatorAdminV1(ctx, target, request.ExpectedSKI)
			return "", transition, failure
		},
		false,
	)
	return result, failure
}

// Cancel implements AdminV1; the candidate handle is the whole idempotency binding.
func (admin *operatorAdminV1Reducer) Cancel(ctx context.Context, request CancelRequestV1) (AdminMutationResultV1, *AdminErrorV1) {
	binding := operatorAdminV1Binding("cancel", request.Candidate.token[:])
	result, _, _, failure := admin.execute(
		ctx, request.MutationPreconditionV1, binding, nil,
		func(now time.Time) (string, *AdminErrorV1) {
			return admin.resolveHandleLocked(operatorAdminV1CandidateHandle, request.Candidate.token, now)
		},
		func(ctx context.Context, target string) (string, operatorAdminV1Transition, *AdminErrorV1) {
			transition, failure := admin.backend.cancelOperatorAdminV1(ctx, target)
			return "", transition, failure
		},
		false,
	)
	return result, failure
}

// RetryTrusted implements AdminV1; the partner handle is the whole idempotency binding.
func (admin *operatorAdminV1Reducer) RetryTrusted(ctx context.Context, request RetryTrustedRequestV1) (AdminMutationResultV1, *AdminErrorV1) {
	binding := operatorAdminV1Binding("retry_trusted", request.Partner.token[:])
	result, _, _, failure := admin.execute(
		ctx, request.MutationPreconditionV1, binding, nil,
		func(now time.Time) (string, *AdminErrorV1) {
			return admin.resolveHandleLocked(operatorAdminV1PartnerHandle, request.Partner.token, now)
		},
		func(ctx context.Context, target string) (string, operatorAdminV1Transition, *AdminErrorV1) {
			transition, failure := admin.backend.retryTrustedOperatorAdminV1(ctx, target)
			return "", transition, failure
		},
		false,
	)
	return result, failure
}

// Untrust implements AdminV1; the partner handle is the whole idempotency binding.
func (admin *operatorAdminV1Reducer) Untrust(ctx context.Context, request UntrustRequestV1) (AdminMutationResultV1, *AdminErrorV1) {
	binding := operatorAdminV1Binding("untrust", request.Partner.token[:])
	result, _, _, failure := admin.execute(
		ctx, request.MutationPreconditionV1, binding, nil,
		func(now time.Time) (string, *AdminErrorV1) {
			return admin.resolveHandleLocked(operatorAdminV1PartnerHandle, request.Partner.token, now)
		},
		func(ctx context.Context, target string) (string, operatorAdminV1Transition, *AdminErrorV1) {
			transition, failure := admin.backend.untrustOperatorAdminV1(ctx, target)
			return "", transition, failure
		},
		false,
	)
	return result, failure
}

type operatorAdminV1Resolver func(time.Time) (string, *AdminErrorV1)
type operatorAdminV1Effect func(context.Context, string) (string, operatorAdminV1Transition, *AdminErrorV1)

func (admin *operatorAdminV1Reducer) execute(
	ctx context.Context,
	precondition MutationPreconditionV1,
	binding [32]byte,
	requestFailure *AdminErrorV1,
	resolve operatorAdminV1Resolver,
	effect operatorAdminV1Effect,
	selectionResult bool,
) (AdminMutationResultV1, SelectionHandleV1, string, *AdminErrorV1) {
	ctx = operatorAdminV1Context(ctx)
	if failure := admin.acquire(ctx); failure != nil {
		return AdminMutationResultV1{}, SelectionHandleV1{}, "", failure
	}
	defer admin.release()
	if failure := admin.available(); failure != nil {
		return AdminMutationResultV1{}, SelectionHandleV1{}, "", failure
	}
	if requestFailure != nil {
		return AdminMutationResultV1{}, SelectionHandleV1{}, "", requestFailure
	}
	if failure := validateOperatorAdminV1Precondition(precondition); failure != nil {
		return AdminMutationResultV1{}, SelectionHandleV1{}, "", failure
	}
	binding = operatorAdminV1Binding(
		"mutation",
		binding[:],
		operatorAdminV1Uint64(precondition.ExpectedStateRevision),
	)

	admin.mu.Lock()
	if replay, exists := admin.replays[precondition.IdempotencyKey]; exists {
		admin.mu.Unlock()
		if replay.binding != binding {
			return AdminMutationResultV1{}, SelectionHandleV1{}, "", operatorAdminV1Error(AdminErrorCodeV1IdempotencyConflict)
		}
		if replay.failure != "" {
			return AdminMutationResultV1{}, SelectionHandleV1{}, "", operatorAdminV1Error(replay.failure)
		}
		result := replay.result
		result.Replayed = true
		return result, replay.selection, replay.actionID, nil
	}
	admin.mu.Unlock()

	facts, failure := admin.backend.snapshotOperatorAdminV1(ctx)
	if failure != nil {
		return AdminMutationResultV1{}, SelectionHandleV1{}, "", normalizeOperatorAdminV1Error(failure)
	}
	if failure := validateOperatorAdminV1Facts(facts); failure != nil {
		return AdminMutationResultV1{}, SelectionHandleV1{}, "", failure
	}
	if failure := admin.available(); failure != nil {
		return AdminMutationResultV1{}, SelectionHandleV1{}, "", failure
	}

	now := admin.now()
	admin.mu.Lock()
	if failure := admin.acceptFactsLocked(facts); failure != nil {
		admin.mu.Unlock()
		return AdminMutationResultV1{}, SelectionHandleV1{}, "", failure
	}
	if precondition.ExpectedStateRevision != admin.revision {
		admin.mu.Unlock()
		return AdminMutationResultV1{}, SelectionHandleV1{}, "", operatorAdminV1Error(AdminErrorCodeV1StateConflict)
	}
	admin.pruneHandlesLocked(now)
	target := ""
	if resolve != nil {
		var resolveFailure *AdminErrorV1
		target, resolveFailure = resolve(now)
		if resolveFailure != nil {
			admin.mu.Unlock()
			return AdminMutationResultV1{}, SelectionHandleV1{}, "", resolveFailure
		}
	}
	if selectionResult && !admin.canIssueHandleLocked(operatorAdminV1SelectionHandle, 1) {
		admin.mu.Unlock()
		return AdminMutationResultV1{}, SelectionHandleV1{}, "", newAdminBoundaryUnavailableV1()
	}
	if !admin.reserveReplaySlotLocked() {
		admin.mu.Unlock()
		return AdminMutationResultV1{}, SelectionHandleV1{}, "", newAdminBoundaryUnavailableV1()
	}
	admin.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return AdminMutationResultV1{}, SelectionHandleV1{}, "", operatorAdminV1Error(AdminErrorCodeV1UnknownState)
	}
	if failure := admin.available(); failure != nil {
		return AdminMutationResultV1{}, SelectionHandleV1{}, "", failure
	}

	selectionReference, transition, effectFailure := effect(ctx, target)
	effectFailure = normalizeOperatorAdminV1Error(effectFailure)
	postFacts, postFailure := admin.backend.snapshotOperatorAdminV1(ctx)
	postFactsValid := postFailure == nil && validateOperatorAdminV1Facts(postFacts) == nil

	admin.mu.Lock()
	changedFacts := postFactsValid && (!admin.initialized || !equalOperatorAdminV1Facts(admin.facts, postFacts))
	changed := transition.changed || changedFacts
	if changed {
		if failure := admin.advanceRevisionLocked(); failure != nil {
			effectFailure = failure
		}
	}
	if postFactsValid {
		admin.facts = cloneOperatorAdminV1Facts(postFacts)
		admin.initialized = true
	}
	if effectFailure == nil && transition.outcome == "" {
		effectFailure = operatorAdminV1Error(AdminErrorCodeV1UnknownState)
	}
	result := AdminMutationResultV1{}
	selection := SelectionHandleV1{}
	if effectFailure == nil {
		result = AdminMutationResultV1{StateRevision: admin.revision, Outcome: transition.outcome}
		if selectionResult {
			if selectionReference == "" {
				effectFailure = operatorAdminV1Error(AdminErrorCodeV1UnknownState)
			} else {
				tokens, handleFailure := admin.issueHandlesLocked(operatorAdminV1SelectionHandle, []string{selectionReference}, admin.now())
				if handleFailure != nil {
					effectFailure = handleFailure
				} else {
					selection = SelectionHandleV1{token: tokens[0]}
				}
			}
		}
	}
	admin.replaySequence++
	if admin.replaySequence == 0 {
		admin.replaySequence++
	}
	replay := operatorAdminV1Replay{binding: binding, result: result, selection: selection, actionID: transition.actionID, sequence: admin.replaySequence}
	if effectFailure != nil {
		replay.failure = effectFailure.Code
	}
	admin.replays[precondition.IdempotencyKey] = replay
	admin.mu.Unlock()

	if effectFailure != nil {
		return AdminMutationResultV1{}, SelectionHandleV1{}, "", effectFailure
	}
	return result, selection, transition.actionID, nil
}

func (admin *operatorAdminV1Reducer) snapshotLocked(view AdminViewV1) (AdminSnapshotV1, *AdminErrorV1) {
	snapshot := AdminSnapshotV1{
		StateRevision:   admin.revision,
		CapturedAt:      admin.facts.capturedAt,
		LocalSKI:        admin.facts.localSKI,
		LocalSHIPID:     admin.facts.localSHIPID,
		Status:          admin.facts.status,
		Window:          admin.facts.window,
		WindowDeadline:  admin.facts.windowDeadline,
		Register:        admin.facts.register,
		Listener:        admin.facts.listener,
		Discovery:       admin.facts.discovery,
		DegradedCode:    admin.facts.degraded,
		TrustedCount:    uint16(len(admin.facts.trusted)),
		ConnectedCount:  uint16(len(admin.facts.connected)),
		DiscoveredCount: uint16(len(admin.facts.discovered)),
		CandidateCount:  uint16(len(admin.facts.candidates)),
	}
	if action := admin.facts.activeAction; action != nil {
		projected := ActiveActionV1{
			ActionID: action.actionID, Kind: action.kind, State: action.state,
			Retryable: action.retryable, Expiry: action.expiresAt,
		}
		if action.outcome != "" {
			outcome := action.outcome
			projected.Outcome = &outcome
		}
		snapshot.ActiveAction = &projected
	}
	switch view {
	case AdminViewV1Trusted:
		targets := make([]string, len(admin.facts.trusted))
		for index, fact := range admin.facts.trusted {
			targets[index] = fact.reference
		}
		tokens, failure := admin.issueHandlesLocked(operatorAdminV1PartnerHandle, targets, admin.now())
		if failure != nil {
			return AdminSnapshotV1{}, failure
		}
		snapshot.Trusted = make([]TrustedPartnerV1, len(admin.facts.trusted))
		for index, fact := range admin.facts.trusted {
			snapshot.Trusted[index] = TrustedPartnerV1{
				Partner: PartnerHandleV1{token: tokens[index]}, SKI: fact.ski, Endpoint: fact.endpoint,
				TrustState: fact.trustState, ConnectionState: fact.connectionState,
				SHIPID: fact.shipID, LastSeen: fact.lastSeen, Name: fact.name, Identifier: fact.identifier,
				Brand: fact.brand, Type: fact.typeName, Model: fact.model,
				RetryState: fact.retryState, RetryDeadline: fact.retryDeadline, RetryAdmitted: fact.retryAdmitted,
			}
		}
	case AdminViewV1Connected:
		snapshot.Connected = make([]ConnectedPartnerV1, len(admin.facts.connected))
		for index, fact := range admin.facts.connected {
			snapshot.Connected[index] = ConnectedPartnerV1{
				SKI: fact.ski, Endpoint: fact.endpoint, TrustState: fact.trustState,
				ConnectionState: fact.connectionState, SHIPID: fact.shipID, LastSeen: fact.lastSeen,
				Name: fact.name, Identifier: fact.identifier, Brand: fact.brand, Type: fact.typeName, Model: fact.model,
			}
		}
	case AdminViewV1Discovered:
		targets := make([]string, len(admin.facts.discovered))
		for index, fact := range admin.facts.discovered {
			targets[index] = fact.reference
		}
		tokens, failure := admin.issueHandlesLocked(operatorAdminV1ObservationHandle, targets, admin.now())
		if failure != nil {
			return AdminSnapshotV1{}, failure
		}
		snapshot.Discovered = make([]DiscoveredPartnerV1, len(admin.facts.discovered))
		for index, fact := range admin.facts.discovered {
			snapshot.Discovered[index] = DiscoveredPartnerV1{
				Observation: ObservationHandleV1{token: tokens[index]}, SKI: fact.ski, Endpoint: fact.endpoint,
				ObservationRevision: fact.observationRevision, LastSeen: fact.lastSeen, Name: fact.name,
				Identifier: fact.identifier, Brand: fact.brand, Type: fact.typeName, Model: fact.model,
			}
		}
	case AdminViewV1Candidate:
		targets := make([]string, len(admin.facts.candidates))
		for index, fact := range admin.facts.candidates {
			targets[index] = fact.reference
		}
		tokens, failure := admin.issueHandlesLocked(operatorAdminV1CandidateHandle, targets, admin.now())
		if failure != nil {
			return AdminSnapshotV1{}, failure
		}
		snapshot.Candidates = make([]CandidateV1, len(admin.facts.candidates))
		for index, fact := range admin.facts.candidates {
			snapshot.Candidates[index] = CandidateV1{
				Candidate: CandidateHandleV1{token: tokens[index]}, SKI: fact.ski, State: fact.state,
				ExpiresAt: fact.expiresAt, AssociationComplete: fact.associationComplete,
			}
		}
	default:
		return AdminSnapshotV1{}, operatorAdminV1Error(AdminErrorCodeV1InvalidRequest)
	}
	return snapshot, nil
}

func (admin *operatorAdminV1Reducer) acceptFactsLocked(facts operatorAdminV1SnapshotFacts) *AdminErrorV1 {
	if !admin.initialized {
		admin.facts = cloneOperatorAdminV1Facts(facts)
		admin.initialized = true
		admin.revision = 1
		admin.invalidateHandlesLocked()
		return nil
	}
	if equalOperatorAdminV1Facts(admin.facts, facts) {
		admin.facts = cloneOperatorAdminV1Facts(facts)
		return nil
	}
	if failure := admin.advanceRevisionLocked(); failure != nil {
		return failure
	}
	admin.facts = cloneOperatorAdminV1Facts(facts)
	return nil
}

func (admin *operatorAdminV1Reducer) reserveReplaySlotLocked() bool {
	if len(admin.replays) < operatorAdminV1MaximumReplayEntries {
		return true
	}
	oldestKey := ""
	oldestSequence := ^uint64(0)
	for key, replay := range admin.replays {
		if replay.failure != "" || replay.result.StateRevision == 0 || replay.result.StateRevision >= admin.revision ||
			replay.sequence >= oldestSequence {
			continue
		}
		oldestKey = key
		oldestSequence = replay.sequence
	}
	if oldestKey == "" {
		return false
	}
	delete(admin.replays, oldestKey)
	return true
}

func (admin *operatorAdminV1Reducer) advanceRevisionLocked() *AdminErrorV1 {
	if admin.revision == ^uint64(0) {
		return newAdminBoundaryUnavailableV1()
	}
	admin.revision++
	if admin.revision == 0 {
		return newAdminBoundaryUnavailableV1()
	}
	admin.invalidateHandlesLocked()
	return nil
}

func (admin *operatorAdminV1Reducer) invalidateHandlesLocked() {
	admin.handles = make(map[[32]byte]operatorAdminV1HandleRecord)
	admin.byTarget = make(map[operatorAdminV1HandleKind]map[string][32]byte)
}

func (admin *operatorAdminV1Reducer) issueHandlesLocked(kind operatorAdminV1HandleKind, targets []string, now time.Time) ([][32]byte, *AdminErrorV1) {
	admin.pruneHandlesLocked(now)
	result := make([][32]byte, len(targets))
	targetMap := admin.byTarget[kind]
	newTargets := make([]int, 0, len(targets))
	for index, target := range targets {
		if token, exists := targetMap[target]; exists {
			if record, valid := admin.handles[token]; valid && record.kind == kind && record.revision == admin.revision && now.Before(record.expiresAt) {
				result[index] = token
				continue
			}
		}
		newTargets = append(newTargets, index)
	}
	if !admin.canIssueHandleLocked(kind, len(newTargets)) {
		return nil, newAdminBoundaryUnavailableV1()
	}
	generated := make(map[[32]byte]struct{}, len(newTargets))
	for _, index := range newTargets {
		token, ok := admin.generateTokenLocked(generated)
		if !ok {
			return nil, newAdminBoundaryUnavailableV1()
		}
		generated[token] = struct{}{}
		result[index] = token
	}
	if targetMap == nil {
		targetMap = make(map[string][32]byte)
		admin.byTarget[kind] = targetMap
	}
	for _, index := range newTargets {
		token := result[index]
		target := targets[index]
		expiresAt := admin.handleExpiryLocked(kind, target, now)
		if !now.Before(expiresAt) {
			return nil, newAdminBoundaryUnavailableV1()
		}
		admin.handles[token] = operatorAdminV1HandleRecord{
			kind: kind, target: target, revision: admin.revision, expiresAt: expiresAt,
		}
		targetMap[target] = token
	}
	return result, nil
}

func (admin *operatorAdminV1Reducer) handleExpiryLocked(kind operatorAdminV1HandleKind, target string, now time.Time) time.Time {
	expiresAt := now.Add(operatorAdminV1MaximumHandleTTL)
	if kind != operatorAdminV1PartnerHandle && admin.facts.window == "open" &&
		!admin.facts.windowDeadline.IsZero() && admin.facts.windowDeadline.Before(expiresAt) {
		expiresAt = admin.facts.windowDeadline
	}
	switch kind {
	case operatorAdminV1ObservationHandle:
		for _, fact := range admin.facts.discovered {
			if fact.reference == target && !fact.expiresAt.IsZero() && fact.expiresAt.Before(expiresAt) {
				expiresAt = fact.expiresAt
				break
			}
		}
	case operatorAdminV1CandidateHandle:
		for _, fact := range admin.facts.candidates {
			if fact.reference == target && fact.expiresAt.Before(expiresAt) {
				expiresAt = fact.expiresAt
				break
			}
		}
	}
	return expiresAt
}

func (admin *operatorAdminV1Reducer) generateTokenLocked(pending map[[32]byte]struct{}) ([32]byte, bool) {
	for attempt := 0; attempt < operatorAdminV1TokenAttempts; attempt++ {
		var token [32]byte
		if admin.random == nil {
			return [32]byte{}, false
		}
		if _, err := io.ReadFull(admin.random, token[:]); err != nil || token == [32]byte{} {
			continue
		}
		if _, collision := admin.handles[token]; collision {
			continue
		}
		if _, collision := pending[token]; collision {
			continue
		}
		return token, true
	}
	return [32]byte{}, false
}

func (admin *operatorAdminV1Reducer) canIssueHandleLocked(kind operatorAdminV1HandleKind, additional int) bool {
	if additional < 0 || len(admin.handles)+additional > operatorAdminV1MaximumHandlesTotal {
		return false
	}
	count := 0
	for _, record := range admin.handles {
		if record.kind == kind {
			count++
		}
	}
	return count+additional <= operatorAdminV1MaximumHandlesPerKind
}

func (admin *operatorAdminV1Reducer) pruneHandlesLocked(now time.Time) {
	for token, record := range admin.handles {
		if record.revision == admin.revision && now.Before(record.expiresAt) {
			continue
		}
		delete(admin.handles, token)
		if targets := admin.byTarget[record.kind]; targets != nil && targets[record.target] == token {
			delete(targets, record.target)
		}
	}
}

func (admin *operatorAdminV1Reducer) resolveHandleLocked(kind operatorAdminV1HandleKind, token [32]byte, now time.Time) (string, *AdminErrorV1) {
	if token == [32]byte{} {
		return "", operatorAdminV1HandleError(kind)
	}
	record, exists := admin.handles[token]
	if !exists || record.kind != kind || record.revision != admin.revision || !now.Before(record.expiresAt) {
		return "", operatorAdminV1HandleError(kind)
	}
	return record.target, nil
}

func operatorAdminV1HandleError(kind operatorAdminV1HandleKind) *AdminErrorV1 {
	switch kind {
	case operatorAdminV1ObservationHandle, operatorAdminV1SelectionHandle:
		return operatorAdminV1Error(AdminErrorCodeV1ObservationStale)
	case operatorAdminV1CandidateHandle:
		return operatorAdminV1Error(AdminErrorCodeV1CandidateExpired)
	case operatorAdminV1PartnerHandle:
		return operatorAdminV1Error(AdminErrorCodeV1SnapshotExpired)
	default:
		return operatorAdminV1Error(AdminErrorCodeV1UnknownState)
	}
}

func (admin *operatorAdminV1Reducer) acquire(ctx context.Context) *AdminErrorV1 {
	if admin == nil || admin.serial == nil {
		return newAdminBoundaryUnavailableV1()
	}
	select {
	case <-admin.serial:
		return nil
	case <-ctx.Done():
		return operatorAdminV1Error(AdminErrorCodeV1UnknownState)
	}
}

func (admin *operatorAdminV1Reducer) release() {
	admin.serial <- struct{}{}
}

func (admin *operatorAdminV1Reducer) available() *AdminErrorV1 {
	if admin == nil || admin.lifecycle == nil || admin.backend == nil {
		return newAdminBoundaryUnavailableV1()
	}
	enabled, started, shutdown := admin.lifecycle.operatorAdminV1Lifecycle()
	if !enabled || !started || shutdown {
		return newAdminBoundaryUnavailableV1()
	}
	return nil
}

func operatorAdminV1Binding(operation string, parts ...[]byte) [32]byte {
	hash := sha256.New()
	operatorAdminV1WriteBindingPart(hash, []byte(operation))
	for _, part := range parts {
		operatorAdminV1WriteBindingPart(hash, part)
	}
	var result [32]byte
	copy(result[:], hash.Sum(nil))
	return result
}

func (admin *operatorAdminV1Reducer) connectBinding(selection [32]byte, pin []byte) ([32]byte, *AdminErrorV1) {
	if admin == nil || !admin.bindingKeyReady {
		return [32]byte{}, newAdminBoundaryUnavailableV1()
	}
	mac := hmac.New(sha256.New, admin.bindingKey[:])
	operatorAdminV1WriteBindingPart(mac, []byte("connect"))
	operatorAdminV1WriteBindingPart(mac, selection[:])
	presence := []byte{0}
	if pin != nil {
		presence[0] = 1
	}
	operatorAdminV1WriteBindingPart(mac, presence)
	operatorAdminV1WriteBindingPart(mac, pin)
	var result [32]byte
	digest := mac.Sum(nil)
	copy(result[:], digest)
	for index := range digest {
		digest[index] = 0
	}
	return result, nil
}

func operatorAdminV1WriteBindingPart(writer io.Writer, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = writer.Write(length[:])
	_, _ = writer.Write(value)
}

func operatorAdminV1Uint64(value uint64) []byte {
	var result [8]byte
	binary.BigEndian.PutUint64(result[:], value)
	return result[:]
}

func operatorAdminV1Context(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func operatorAdminV1Error(code AdminErrorCodeV1) *AdminErrorV1 {
	return &AdminErrorV1{Code: code}
}

func normalizeOperatorAdminV1Error(failure *AdminErrorV1) *AdminErrorV1 {
	if failure == nil {
		return nil
	}
	switch failure.Code {
	case AdminErrorCodeV1AdminBoundaryUnavailable,
		AdminErrorCodeV1Unauthenticated,
		AdminErrorCodeV1Forbidden,
		AdminErrorCodeV1CSRFRejected,
		AdminErrorCodeV1InvalidRequest,
		AdminErrorCodeV1StateConflict,
		AdminErrorCodeV1SnapshotExpired,
		AdminErrorCodeV1IdempotencyConflict,
		AdminErrorCodeV1PairingClosed,
		AdminErrorCodeV1ObservationStale,
		AdminErrorCodeV1IdentityMismatch,
		AdminErrorCodeV1AssociationIncomplete,
		AdminErrorCodeV1CandidateExpired,
		AdminErrorCodeV1CandidateBusy,
		AdminErrorCodeV1TrustDenied,
		AdminErrorCodeV1ListenerUnavailable,
		AdminErrorCodeV1DiscoveryUnavailable,
		AdminErrorCodeV1AttemptTimeout,
		AdminErrorCodeV1Disconnected,
		AdminErrorCodeV1BackoffActive,
		AdminErrorCodeV1TerminalQuarantine,
		AdminErrorCodeV1PersistenceFailure,
		AdminErrorCodeV1PINRequired,
		AdminErrorCodeV1PINOptional,
		AdminErrorCodeV1PINBusy,
		AdminErrorCodeV1PINUnavailable,
		AdminErrorCodeV1PINRejected,
		AdminErrorCodeV1PINProtocolError,
		AdminErrorCodeV1UnknownState:
		return operatorAdminV1Error(failure.Code)
	default:
		return operatorAdminV1Error(AdminErrorCodeV1UnknownState)
	}
}

type operatorAdminV1Backend interface {
	snapshotOperatorAdminV1(context.Context) (operatorAdminV1SnapshotFacts, *AdminErrorV1)
	observeOperatorAdminV1ActiveAction(context.Context, string)
	openOperatorAdminV1(context.Context, time.Duration) (operatorAdminV1Transition, *AdminErrorV1)
	closeOperatorAdminV1(context.Context) (operatorAdminV1Transition, *AdminErrorV1)
	selectOperatorAdminV1(context.Context, string, string) (string, operatorAdminV1Transition, *AdminErrorV1)
	connectOperatorAdminV1(context.Context, string) (operatorAdminV1Transition, *AdminErrorV1)
	connectOperatorAdminV1WithPIN(context.Context, string, operatorAdminV1PINProvider) (operatorAdminV1Transition, *AdminErrorV1)
	confirmOperatorAdminV1(context.Context, string, string) (operatorAdminV1Transition, *AdminErrorV1)
	cancelOperatorAdminV1(context.Context, string) (operatorAdminV1Transition, *AdminErrorV1)
	retryTrustedOperatorAdminV1(context.Context, string) (operatorAdminV1Transition, *AdminErrorV1)
	untrustOperatorAdminV1(context.Context, string) (operatorAdminV1Transition, *AdminErrorV1)
}

type operatorAdminV1BackendSlot struct {
	mu      sync.RWMutex
	backend operatorAdminV1Backend
}

func (slot *operatorAdminV1BackendSlot) attach(backend operatorAdminV1Backend) {
	slot.mu.Lock()
	slot.backend = backend
	slot.mu.Unlock()
}

func (slot *operatorAdminV1BackendSlot) detach(backend operatorAdminV1Backend) {
	slot.mu.Lock()
	if slot.backend == backend {
		slot.backend = nil
	}
	slot.mu.Unlock()
}

func (slot *operatorAdminV1BackendSlot) current() operatorAdminV1Backend {
	if slot == nil {
		return nil
	}
	slot.mu.RLock()
	defer slot.mu.RUnlock()
	return slot.backend
}

func (slot *operatorAdminV1BackendSlot) snapshotOperatorAdminV1(ctx context.Context) (operatorAdminV1SnapshotFacts, *AdminErrorV1) {
	if backend := slot.current(); backend != nil {
		return backend.snapshotOperatorAdminV1(ctx)
	}
	return operatorAdminV1SnapshotFacts{}, newAdminBoundaryUnavailableV1()
}

func (slot *operatorAdminV1BackendSlot) observeOperatorAdminV1ActiveAction(ctx context.Context, actionID string) {
	if backend := slot.current(); backend != nil {
		backend.observeOperatorAdminV1ActiveAction(ctx, actionID)
	}
}

func (slot *operatorAdminV1BackendSlot) openOperatorAdminV1(ctx context.Context, duration time.Duration) (operatorAdminV1Transition, *AdminErrorV1) {
	if backend := slot.current(); backend != nil {
		return backend.openOperatorAdminV1(ctx, duration)
	}
	return operatorAdminV1Transition{}, newAdminBoundaryUnavailableV1()
}

func (slot *operatorAdminV1BackendSlot) closeOperatorAdminV1(ctx context.Context) (operatorAdminV1Transition, *AdminErrorV1) {
	if backend := slot.current(); backend != nil {
		return backend.closeOperatorAdminV1(ctx)
	}
	return operatorAdminV1Transition{}, newAdminBoundaryUnavailableV1()
}

func (slot *operatorAdminV1BackendSlot) selectOperatorAdminV1(ctx context.Context, reference, ski string) (string, operatorAdminV1Transition, *AdminErrorV1) {
	if backend := slot.current(); backend != nil {
		return backend.selectOperatorAdminV1(ctx, reference, ski)
	}
	return "", operatorAdminV1Transition{}, newAdminBoundaryUnavailableV1()
}

func (slot *operatorAdminV1BackendSlot) connectOperatorAdminV1(ctx context.Context, reference string) (operatorAdminV1Transition, *AdminErrorV1) {
	if backend := slot.current(); backend != nil {
		return backend.connectOperatorAdminV1(ctx, reference)
	}
	return operatorAdminV1Transition{}, newAdminBoundaryUnavailableV1()
}

func (slot *operatorAdminV1BackendSlot) connectOperatorAdminV1WithPIN(ctx context.Context, reference string, provider operatorAdminV1PINProvider) (operatorAdminV1Transition, *AdminErrorV1) {
	if backend := slot.current(); backend != nil && provider != nil {
		return backend.connectOperatorAdminV1WithPIN(ctx, reference, provider)
	}
	return operatorAdminV1Transition{}, newAdminBoundaryUnavailableV1()
}

func (slot *operatorAdminV1BackendSlot) confirmOperatorAdminV1(ctx context.Context, reference, ski string) (operatorAdminV1Transition, *AdminErrorV1) {
	if backend := slot.current(); backend != nil {
		return backend.confirmOperatorAdminV1(ctx, reference, ski)
	}
	return operatorAdminV1Transition{}, newAdminBoundaryUnavailableV1()
}

func (slot *operatorAdminV1BackendSlot) cancelOperatorAdminV1(ctx context.Context, reference string) (operatorAdminV1Transition, *AdminErrorV1) {
	if backend := slot.current(); backend != nil {
		return backend.cancelOperatorAdminV1(ctx, reference)
	}
	return operatorAdminV1Transition{}, newAdminBoundaryUnavailableV1()
}

func (slot *operatorAdminV1BackendSlot) retryTrustedOperatorAdminV1(ctx context.Context, reference string) (operatorAdminV1Transition, *AdminErrorV1) {
	if backend := slot.current(); backend != nil {
		return backend.retryTrustedOperatorAdminV1(ctx, reference)
	}
	return operatorAdminV1Transition{}, newAdminBoundaryUnavailableV1()
}

func (slot *operatorAdminV1BackendSlot) untrustOperatorAdminV1(ctx context.Context, reference string) (operatorAdminV1Transition, *AdminErrorV1) {
	if backend := slot.current(); backend != nil {
		return backend.untrustOperatorAdminV1(ctx, reference)
	}
	return operatorAdminV1Transition{}, newAdminBoundaryUnavailableV1()
}
