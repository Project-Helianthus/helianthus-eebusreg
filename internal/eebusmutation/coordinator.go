package eebusmutation

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

type rawMutationCoordinator struct {
	config rawMutationCoordinatorConfig
	deps   rawMutationCoordinatorDependencies

	journal *rawMutationJournal
	ctx     context.Context
	cancel  context.CancelFunc

	mu          sync.Mutex
	entries     map[string]*rawMutationEntry
	idempotency map[eebusraw.HashV1]rawMutationIdempotencyEntry
	timers      map[string]rawMutationTimer
	writer      bool
	writerDone  chan struct{}
	quarantined bool
	closed      bool
	closeDone   bool
	closeError  *eebusraw.ErrorV1
}

type rawMutationEntry struct {
	mutation            eebusraw.MutationV1
	published           atomic.Pointer[eebusraw.MutationV1]
	principalHash       eebusraw.HashV1
	durableIdentityHash eebusraw.HashV1
	durableRequestHash  eebusraw.HashV1
	durableTool         eebusraw.ToolV1
	durable             bool
}

func (entry *rawMutationEntry) publish(mutation eebusraw.MutationV1) {
	snapshot := cloneMutation(mutation)
	entry.published.Store(&snapshot)
}

func (entry *rawMutationEntry) snapshot() eebusraw.MutationV1 {
	if entry == nil {
		return eebusraw.MutationV1{}
	}
	if published := entry.published.Load(); published != nil {
		return cloneMutation(*published)
	}
	return cloneMutation(entry.mutation)
}

type rawMutationIdempotencyEntry struct {
	requestHash eebusraw.HashV1
	mutationRef string
}

func newRawMutationCoordinator(
	config rawMutationCoordinatorConfig,
	dependencies rawMutationCoordinatorDependencies,
) (*rawMutationCoordinator, *eebusraw.ErrorV1) {
	if dependencies.Scheduler == nil {
		dependencies.Scheduler = rawMutationNativeScheduler{}
	}
	if terminal := validateRawMutationConfiguration(config, dependencies); terminal != nil {
		return nil, terminal
	}
	config.ReferenceKey = append([]byte(nil), config.ReferenceKey...)
	config.LabProfiles = append([]rawMutationLabProfile(nil), config.LabProfiles...)
	journal, records, err := openRawMutationJournal(config.StateRoot, dependencies.Persistence)
	if err != nil {
		return nil, internalMutationError()
	}
	parent := config.Context
	if parent == nil {
		parent = context.Background()
	}
	ownerContext, cancel := context.WithCancel(parent)
	coordinator := &rawMutationCoordinator{
		config:      config,
		deps:        dependencies,
		journal:     journal,
		ctx:         ownerContext,
		cancel:      cancel,
		entries:     make(map[string]*rawMutationEntry),
		idempotency: make(map[eebusraw.HashV1]rawMutationIdempotencyEntry),
		timers:      make(map[string]rawMutationTimer),
	}
	if err := coordinator.restore(records); err != nil {
		cancel()
		_ = journal.close()
		return nil, internalMutationError()
	}
	coordinator.rearmProbeTimers()
	coordinator.rearmRecovery()
	return coordinator, nil
}

func NewCoordinator(
	config CoordinatorConfig,
	dependencies CoordinatorDependencies,
) (*Coordinator, *eebusraw.ErrorV1) {
	return newRawMutationCoordinator(config, dependencies)
}

func (coordinator *rawMutationCoordinator) restore(records []rawMutationJournalRecord) error {
	currentEpoch := coordinator.config.RuntimeEpoch()
	for _, record := range records {
		if record.Mutation.Runtime.RuntimeEpoch != record.RuntimeEpoch {
			return errors.New("mutation runtime epoch is not journal-bound")
		}
		if record.RuntimeEpoch != currentEpoch {
			continue
		}
		idempotency := rawMutationIdempotencyEntry{
			requestHash: record.RequestHash,
			mutationRef: record.Mutation.MutationRef,
		}
		if previous, exists := coordinator.idempotency[record.IdentityHash]; exists &&
			(previous.requestHash != idempotency.requestHash ||
				previous.mutationRef != idempotency.mutationRef) {
			return errors.New("mutation idempotency identity is inconsistent")
		}
		coordinator.idempotency[record.IdentityHash] = idempotency

		entry := coordinator.entries[record.Mutation.MutationRef]
		if entry == nil {
			entry = &rawMutationEntry{}
			coordinator.entries[record.Mutation.MutationRef] = entry
		} else if entry.principalHash != "" &&
			entry.principalHash != record.PrincipalHash {
			return errors.New("mutation principal binding changed")
		}
		entry.mutation = cloneMutation(record.Mutation)
		entry.publish(entry.mutation)
		entry.principalHash = record.PrincipalHash
		entry.durableIdentityHash = record.IdentityHash
		entry.durableRequestHash = record.RequestHash
		entry.durableTool = record.Tool
		entry.durable = true
	}
	for _, entry := range coordinator.entries {
		if rawMutationStateQuarantinesWrites(entry.mutation) {
			coordinator.quarantined = true
		}
	}
	return nil
}

func (coordinator *rawMutationCoordinator) FeaturesDataSet(
	ctx context.Context,
	auth eebusraw.WriteAuthorizationV1,
	request eebusraw.FeatureDataSetRequestV1,
) (eebusraw.MutationV1, *eebusraw.ErrorV1) {
	if ctx == nil {
		ctx = coordinator.ctx
	}
	ctx, cancel := coordinator.operationContext(ctx)
	defer cancel()
	if terminal := eebusraw.ValidateWriteAuthorizationV1(auth, eebusraw.ToolV1FeaturesDataSet); terminal != nil {
		return eebusraw.MutationV1{}, terminal
	}
	request = cloneRawMutationSetRequest(request)
	if terminal := validateRawMutationSetRequest(request); terminal != nil {
		return eebusraw.MutationV1{}, terminal
	}
	epoch := coordinator.config.RuntimeEpoch()
	identityHash, err := rawMutationIdentityHash(
		coordinator.config.ReferenceKey,
		epoch,
		auth.PrincipalClass,
		eebusraw.ToolV1FeaturesDataSet,
		request.IdempotencyKey,
	)
	if err != nil {
		return eebusraw.MutationV1{}, internalMutationError()
	}
	requestHash, err := eebusraw.CanonicalSHA256V1(request)
	if err != nil {
		return eebusraw.MutationV1{}, internalMutationError()
	}
	principalHash, err := rawMutationPrincipalHash(auth.PrincipalClass)
	if err != nil {
		return eebusraw.MutationV1{}, internalMutationError()
	}

	entry, action, terminal := coordinator.reserveSet(
		principalHash,
		identityHash,
		requestHash,
		request,
	)
	if terminal != nil {
		return eebusraw.MutationV1{}, terminal
	}
	switch action {
	case rawMutationSetReplay:
		mutation := entry.snapshot()
		return mutation, terminalForMutation(mutation)
	case rawMutationSetInFlight:
		return entry.snapshot(), nil
	case rawMutationSetRecover:
		defer coordinator.releaseWriter()
		return coordinator.recoverEntry(ctx, entry)
	case rawMutationSetNew:
		defer coordinator.releaseWriter()
		defer coordinator.releaseUndurableReservation(entry)
	default:
		return eebusraw.MutationV1{}, internalMutationError()
	}

	binding, terminal := verifyRawMutationReadToken(
		ctx,
		coordinator.config,
		coordinator.deps.TokenVerifier,
		auth,
		request,
	)
	if terminal != nil {
		return eebusraw.MutationV1{}, terminal
	}
	if terminal := validateCurrentRawMutationBinding(
		coordinator.deps.BindingAuthority,
		request.Target,
		binding.Runtime,
	); terminal != nil {
		return eebusraw.MutationV1{}, terminal
	}
	if terminal := coordinator.deps.TokenVerifier.ConsumeReadToken(
		ctx,
		request.ReadToken,
	); terminal != nil {
		return eebusraw.MutationV1{}, sanitizeMutationError(terminal)
	}
	readTarget := request.Target.Clone()
	readTarget.Operation = eebusraw.OperationV1Read
	beforeRead, readTerminal := coordinator.deps.Executor.FullReadIfCurrent(
		ctx,
		readTarget,
		binding.Runtime,
	)
	if terminal := validateGuardRead(beforeRead, readTerminal, binding, request); terminal != nil {
		if readTerminal == nil {
			return eebusraw.MutationV1{}, terminal
		}
		failed := coordinator.prepareEntry(
			entry,
			principalHash,
			identityHash,
			requestHash,
			eebusraw.ToolV1FeaturesDataSet,
			request,
			binding.Runtime,
			eebusraw.TypedValueV1{},
		)
		failed.mutation.Error = sanitizeMutationError(terminal)
		failed.mutation.NoContactEvidence = &eebusraw.NoContactEvidenceV1{
			RemoteFramesSent:   0,
			LastCompletedPhase: "read_token_verified",
			VerifiedAt:         normalizedNow(coordinator.config.Now),
		}
		if crash := coordinator.transition(failed, eebusraw.MutationStateV1FailedNoContact); crash != nil {
			return cloneMutation(failed.mutation), crash
		}
		return cloneMutation(failed.mutation), cloneTerminal(failed.mutation.Error)
	}

	decision, policyTerminal := coordinator.deps.Policy.MutationPolicy(
		ctx,
		request.Target.Clone(),
		beforeRead.Value.Clone(),
		request.Value.Clone(),
	)
	if policyTerminal != nil {
		return eebusraw.MutationV1{}, sanitizeMutationError(policyTerminal)
	}
	if terminal := validateRawMutationPolicy(coordinator.config, decision, request, beforeRead.Value); terminal != nil {
		return eebusraw.MutationV1{}, terminal
	}

	entry = coordinator.prepareEntry(
		entry,
		principalHash,
		identityHash,
		requestHash,
		eebusraw.ToolV1FeaturesDataSet,
		request,
		beforeRead.Runtime,
		beforeRead.Value,
	)
	if terminal := coordinator.transition(entry, eebusraw.MutationStateV1Prepared); terminal != nil {
		return cloneMutation(entry.mutation), terminal
	}
	if terminal := coordinator.transition(entry, eebusraw.MutationStateV1DispatchIntent); terminal != nil {
		return cloneMutation(entry.mutation), terminal
	}

	writeResult, writeTerminal := coordinator.deps.Executor.FullWriteIfCurrent(
		ctx,
		request.Target.Clone(),
		request.Value.Clone(),
		beforeRead.Runtime,
	)
	return coordinator.completeOriginalWrite(ctx, entry, writeResult, writeTerminal)
}

func (coordinator *rawMutationCoordinator) MutationsGet(
	_ context.Context,
	auth eebusraw.ReadAuthorizationV1,
	request eebusraw.MutationGetRequestV1,
) (eebusraw.MutationV1, *eebusraw.ErrorV1) {
	if terminal := eebusraw.ValidateReadAuthorizationV1(auth, eebusraw.ToolV1MutationsGet); terminal != nil {
		return eebusraw.MutationV1{}, terminal
	}
	if !boundedPurposeValue(request.MutationRef) {
		return eebusraw.MutationV1{}, mutationError(eebusraw.ErrorCodeV1InvalidArgument, false)
	}
	principalHash, err := rawMutationPrincipalHash(auth.PrincipalClass)
	if err != nil {
		return eebusraw.MutationV1{}, internalMutationError()
	}
	coordinator.mu.Lock()
	if coordinator.closed {
		coordinator.mu.Unlock()
		return eebusraw.MutationV1{}, mutationError(eebusraw.ErrorCodeV1Disconnected, true)
	}
	entry := coordinator.entries[request.MutationRef]
	if entry == nil {
		coordinator.mu.Unlock()
		return eebusraw.MutationV1{}, mutationError(eebusraw.ErrorCodeV1NotFound, false)
	}
	entryPrincipal := entry.principalHash
	coordinator.mu.Unlock()
	mutation := entry.snapshot()
	if entryPrincipal != principalHash ||
		mutation.Runtime.RuntimeEpoch != coordinator.config.RuntimeEpoch() {
		return eebusraw.MutationV1{}, mutationError(eebusraw.ErrorCodeV1PermissionDenied, false)
	}
	return mutation, nil
}

func (coordinator *rawMutationCoordinator) Close() *eebusraw.ErrorV1 {
	coordinator.mu.Lock()
	if coordinator.closeDone {
		terminal := cloneTerminal(coordinator.closeError)
		coordinator.mu.Unlock()
		return terminal
	}
	coordinator.closed = true
	coordinator.cancel()
	timers := make([]rawMutationTimer, 0, len(coordinator.timers))
	for _, timer := range coordinator.timers {
		timers = append(timers, timer)
	}
	coordinator.timers = make(map[string]rawMutationTimer)
	writerDone := coordinator.writerDone
	coordinator.mu.Unlock()
	for _, timer := range timers {
		timer.Stop()
	}
	if coordinator.deps.CancelInFlight != nil {
		coordinator.deps.CancelInFlight()
	}
	if writerDone != nil {
		wait := coordinator.config.RecoveryDeadline
		if wait <= 0 {
			wait = coordinator.config.WriterWait
		}
		if wait <= 0 {
			wait = 5 * time.Minute
		}
		timer := time.NewTimer(wait)
		select {
		case <-writerDone:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			terminal := internalMutationError()
			coordinator.mu.Lock()
			coordinator.closeDone = true
			coordinator.closeError = terminal
			coordinator.mu.Unlock()
			return cloneTerminal(terminal)
		}
	}
	var terminal *eebusraw.ErrorV1
	if err := coordinator.journal.close(); err != nil {
		terminal = internalMutationError()
	}
	coordinator.mu.Lock()
	coordinator.closeDone = true
	coordinator.closeError = terminal
	coordinator.mu.Unlock()
	return cloneTerminal(terminal)
}

func (coordinator *rawMutationCoordinator) operationContext(
	parent context.Context,
) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = coordinator.ctx
	}
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(coordinator.ctx, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

type rawMutationSetAction uint8

const (
	rawMutationSetInvalid rawMutationSetAction = iota
	rawMutationSetNew
	rawMutationSetReplay
	rawMutationSetInFlight
	rawMutationSetRecover
)

func (coordinator *rawMutationCoordinator) reserveSet(
	principalHash eebusraw.HashV1,
	identityHash eebusraw.HashV1,
	requestHash eebusraw.HashV1,
	request eebusraw.FeatureDataSetRequestV1,
) (*rawMutationEntry, rawMutationSetAction, *eebusraw.ErrorV1) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.closed {
		return nil, rawMutationSetInvalid, mutationError(eebusraw.ErrorCodeV1Disconnected, true)
	}
	if coordinator.quarantined {
		return nil, rawMutationSetInvalid, mutationError(eebusraw.ErrorCodeV1Conflict, false)
	}
	if known, exists := coordinator.idempotency[identityHash]; exists {
		if known.requestHash != requestHash {
			return nil, rawMutationSetInvalid, mutationError(eebusraw.ErrorCodeV1IdempotencyConflict, false)
		}
		entry := coordinator.entries[known.mutationRef]
		if entry == nil {
			return nil, rawMutationSetInvalid, internalMutationError()
		}
		if coordinator.writer {
			return entry, rawMutationSetInFlight, nil
		}
		if rawMutationStateNeedsRecovery(entry.snapshot().State) {
			coordinator.writer = true
			coordinator.writerDone = make(chan struct{})
			return entry, rawMutationSetRecover, nil
		}
		return entry, rawMutationSetReplay, nil
	}
	if coordinator.writer {
		return nil, rawMutationSetInvalid, mutationError(eebusraw.ErrorCodeV1WriterBusy, true)
	}
	coordinator.writer = true
	coordinator.writerDone = make(chan struct{})
	entry := coordinator.newEntry(
		principalHash,
		identityHash,
		requestHash,
		eebusraw.ToolV1FeaturesDataSet,
		request,
		eebusraw.RuntimeBindingV1{RuntimeEpoch: coordinator.config.RuntimeEpoch()},
		eebusraw.TypedValueV1{},
	)
	entry.publish(entry.mutation)
	coordinator.entries[entry.mutation.MutationRef] = entry
	coordinator.idempotency[identityHash] = rawMutationIdempotencyEntry{
		requestHash: requestHash,
		mutationRef: entry.mutation.MutationRef,
	}
	return entry, rawMutationSetNew, nil
}

func (coordinator *rawMutationCoordinator) releaseUndurableReservation(entry *rawMutationEntry) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if entry == nil || entry.durable {
		return
	}
	delete(coordinator.entries, entry.mutation.MutationRef)
	if known, exists := coordinator.idempotency[entry.durableIdentityHash]; exists &&
		known.mutationRef == entry.mutation.MutationRef {
		delete(coordinator.idempotency, entry.durableIdentityHash)
	}
}

func (coordinator *rawMutationCoordinator) acquireInternalWriter() bool {
	wait := coordinator.config.RecoveryDeadline
	if wait <= 0 {
		wait = coordinator.config.WriterWait
	}
	if wait <= 0 {
		wait = 5 * time.Minute
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		coordinator.mu.Lock()
		if coordinator.closed || coordinator.quarantined {
			coordinator.mu.Unlock()
			return false
		}
		if !coordinator.writer {
			coordinator.writer = true
			coordinator.writerDone = make(chan struct{})
			coordinator.mu.Unlock()
			return true
		}
		done := coordinator.writerDone
		coordinator.mu.Unlock()
		select {
		case <-done:
		case <-deadline.C:
			return false
		}
	}
}

func (coordinator *rawMutationCoordinator) releaseWriter() {
	coordinator.mu.Lock()
	if coordinator.writer {
		coordinator.writer = false
		close(coordinator.writerDone)
		coordinator.writerDone = nil
	}
	coordinator.mu.Unlock()
}

func (coordinator *rawMutationCoordinator) newEntry(
	principalHash eebusraw.HashV1,
	identityHash eebusraw.HashV1,
	requestHash eebusraw.HashV1,
	tool eebusraw.ToolV1,
	request eebusraw.FeatureDataSetRequestV1,
	runtime eebusraw.RuntimeBindingV1,
	before eebusraw.TypedValueV1,
) *rawMutationEntry {
	now := normalizedNow(coordinator.config.Now)
	mutation := eebusraw.MutationV1{
		MutationRef: rawMutationReference(coordinator.config.ReferenceKey, identityHash),
		Mode:        request.Mode,
		Target:      request.Target.Clone(),
		Runtime:     runtime,
		Before:      before.Clone(),
		Requested:   request.Value.Clone(),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if request.Mode == eebusraw.ModeV1Probe {
		deadline := now.Add(time.Duration(request.ProbeTTLSeconds) * time.Second)
		mutation.ProbeDeadline = &deadline
	}
	return &rawMutationEntry{
		mutation:            mutation,
		principalHash:       principalHash,
		durableIdentityHash: identityHash,
		durableRequestHash:  requestHash,
		durableTool:         tool,
	}
}

func (coordinator *rawMutationCoordinator) prepareEntry(
	entry *rawMutationEntry,
	principalHash eebusraw.HashV1,
	identityHash eebusraw.HashV1,
	requestHash eebusraw.HashV1,
	tool eebusraw.ToolV1,
	request eebusraw.FeatureDataSetRequestV1,
	runtime eebusraw.RuntimeBindingV1,
	before eebusraw.TypedValueV1,
) *rawMutationEntry {
	prepared := coordinator.newEntry(
		principalHash,
		identityHash,
		requestHash,
		tool,
		request,
		runtime,
		before,
	)
	if entry == nil {
		return prepared
	}
	prepared.mutation.MutationRef = entry.mutation.MutationRef
	entry.mutation = prepared.mutation
	entry.principalHash = prepared.principalHash
	entry.durableIdentityHash = prepared.durableIdentityHash
	entry.durableRequestHash = prepared.durableRequestHash
	entry.durableTool = prepared.durableTool
	entry.publish(entry.mutation)
	return entry
}

func (coordinator *rawMutationCoordinator) transition(
	entry *rawMutationEntry,
	state eebusraw.MutationStateV1,
) *eebusraw.ErrorV1 {
	mutation := cloneMutation(entry.mutation)
	now := normalizedNow(coordinator.config.Now)
	mutation.State = state
	mutation.UpdatedAt = now
	sequence := uint64(len(mutation.Audit) + 1)
	var previous *eebusraw.HashV1
	if len(mutation.Audit) != 0 {
		value := mutation.Audit[len(mutation.Audit)-1].TransitionHash
		previous = &value
	}
	hash, err := rawMutationAuditHash(sequence, state, now, "", previous)
	if err != nil {
		return internalMutationError()
	}
	mutation.Audit = append(mutation.Audit, eebusraw.AuditTransitionV1{
		Sequence:       sequence,
		State:          state,
		TransitionedAt: now,
		PreviousHash:   cloneHash(previous),
		TransitionHash: hash,
	})
	record := rawMutationJournalRecord{
		RuntimeEpoch:  mutation.Runtime.RuntimeEpoch,
		PrincipalHash: entry.principalHash,
		Tool:          entry.durableTool,
		IdentityHash:  entry.durableIdentityHash,
		RequestHash:   entry.durableRequestHash,
		Mutation:      cloneMutation(mutation),
	}
	if err := coordinator.journal.append(record); err != nil {
		return internalMutationError()
	}
	coordinator.mu.Lock()
	entry.durable = true
	entry.mutation = cloneMutation(mutation)
	entry.publish(mutation)
	coordinator.entries[mutation.MutationRef] = entry
	coordinator.idempotency[entry.durableIdentityHash] = rawMutationIdempotencyEntry{
		requestHash: entry.durableRequestHash,
		mutationRef: mutation.MutationRef,
	}
	if rawMutationStateQuarantinesWrites(mutation) {
		coordinator.quarantined = true
	}
	coordinator.mu.Unlock()
	if coordinator.deps.CrashAfterDurable != nil {
		if err := coordinator.deps.CrashAfterDurable(state); err != nil {
			return internalMutationError()
		}
	}
	return nil
}

func rawMutationStateQuarantinesWrites(mutation eebusraw.MutationV1) bool {
	if mutation.State == eebusraw.MutationStateV1Conflict {
		return true
	}
	return mutation.State == eebusraw.MutationStateV1OutcomeUnknown &&
		mutation.Rollback != nil
}

func (coordinator *rawMutationCoordinator) rearmRecovery() {
	coordinator.mu.Lock()
	entries := make([]*rawMutationEntry, 0, len(coordinator.entries))
	if !coordinator.quarantined {
		for _, entry := range coordinator.entries {
			mutation := entry.snapshot()
			if mutation.Runtime.RuntimeEpoch == coordinator.config.RuntimeEpoch() &&
				rawMutationStateNeedsRecovery(mutation.State) {
				entries = append(entries, entry)
			}
		}
	}
	coordinator.mu.Unlock()
	for _, entry := range entries {
		coordinator.scheduleRecoveryAt(entry, normalizedNow(coordinator.config.Now))
	}
}

func (coordinator *rawMutationCoordinator) scheduleRecoveryAt(
	entry *rawMutationEntry,
	deadline time.Time,
) {
	ref := entry.snapshot().MutationRef
	key := "recovery:" + ref
	timer := coordinator.deps.Scheduler.Schedule(deadline, func() {
		if !coordinator.acquireInternalWriter() {
			coordinator.mu.Lock()
			current := coordinator.entries[ref]
			closed := coordinator.closed
			quarantined := coordinator.quarantined
			eligible := current != nil &&
				rawMutationStateNeedsRecovery(current.snapshot().State)
			coordinator.mu.Unlock()
			if !closed && !quarantined && eligible {
				coordinator.scheduleRecoveryAt(
					current,
					normalizedNow(coordinator.config.Now).Add(coordinator.retryDelay()),
				)
			}
			return
		}
		defer coordinator.releaseWriter()
		coordinator.mu.Lock()
		current := coordinator.entries[ref]
		delete(coordinator.timers, key)
		eligible := current != nil &&
			rawMutationStateNeedsRecovery(current.snapshot().State)
		coordinator.mu.Unlock()
		if eligible {
			_, _ = coordinator.recoverEntry(coordinator.ctx, current)
		}
	})
	coordinator.mu.Lock()
	if previous := coordinator.timers[key]; previous != nil {
		previous.Stop()
	}
	if !coordinator.closed && !coordinator.quarantined {
		coordinator.timers[key] = timer
	} else {
		timer.Stop()
	}
	coordinator.mu.Unlock()
}

func (coordinator *rawMutationCoordinator) retryDelay() time.Duration {
	delay := coordinator.config.WriterWait
	if delay <= 0 {
		delay = 10 * time.Millisecond
	}
	if delay > time.Second {
		delay = time.Second
	}
	return delay
}

func rawMutationStateNeedsRecovery(state eebusraw.MutationStateV1) bool {
	switch state {
	case eebusraw.MutationStateV1DispatchIntent,
		eebusraw.MutationStateV1ReplyObserved,
		eebusraw.MutationStateV1VerifyPending,
		eebusraw.MutationStateV1OutcomeUnknown,
		eebusraw.MutationStateV1RollbackIntent,
		eebusraw.MutationStateV1RollbackDispatchIntent,
		eebusraw.MutationStateV1RollbackReplyObserved,
		eebusraw.MutationStateV1RollbackVerifyPending:
		return true
	default:
		return false
	}
}

func terminalForMutation(mutation eebusraw.MutationV1) *eebusraw.ErrorV1 {
	switch mutation.State {
	case eebusraw.MutationStateV1Applied,
		eebusraw.MutationStateV1ProbeActive,
		eebusraw.MutationStateV1RolledBack,
		eebusraw.MutationStateV1Prepared,
		eebusraw.MutationStateV1DispatchIntent,
		eebusraw.MutationStateV1ReplyObserved,
		eebusraw.MutationStateV1VerifyPending,
		eebusraw.MutationStateV1RollbackIntent,
		eebusraw.MutationStateV1RollbackDispatchIntent,
		eebusraw.MutationStateV1RollbackReplyObserved,
		eebusraw.MutationStateV1RollbackVerifyPending:
		return nil
	default:
		return cloneTerminal(mutation.Error)
	}
}

func cloneRawMutationSetRequest(request eebusraw.FeatureDataSetRequestV1) eebusraw.FeatureDataSetRequestV1 {
	request.Target = request.Target.Clone()
	request.Value = request.Value.Clone()
	if request.ExpectedCurrent != nil {
		value := request.ExpectedCurrent.Clone()
		request.ExpectedCurrent = &value
	}
	if request.ConstraintsOverride != nil {
		override := *request.ConstraintsOverride
		request.ConstraintsOverride = &override
	}
	return request
}
