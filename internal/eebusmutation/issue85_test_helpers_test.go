package eebusmutation

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

var errIssue85InjectedCrash = errors.New("issue85 injected crash")
var errIssue85InjectedPersistence = errors.New("issue85 injected persistence failure")

type issue85MutationCoordinator interface {
	FeaturesDataSet(
		context.Context,
		eebusraw.WriteAuthorizationV1,
		eebusraw.FeatureDataSetRequestV1,
	) (eebusraw.MutationV1, *eebusraw.ErrorV1)
	MutationsGet(
		context.Context,
		eebusraw.ReadAuthorizationV1,
		eebusraw.MutationGetRequestV1,
	) (eebusraw.MutationV1, *eebusraw.ErrorV1)
	MutationsRollback(
		context.Context,
		eebusraw.WriteAuthorizationV1,
		eebusraw.MutationRollbackRequestV1,
	) (eebusraw.MutationV1, *eebusraw.ErrorV1)
	Close() *eebusraw.ErrorV1
}

func issue85OpenCoordinator(
	config rawMutationCoordinatorConfig,
	dependencies rawMutationCoordinatorDependencies,
) (issue85MutationCoordinator, *eebusraw.ErrorV1) {
	coordinator, terminal := newRawMutationCoordinator(config, dependencies)
	if coordinator == nil {
		return nil, terminal
	}
	return coordinator, terminal
}

type issue85Harness struct {
	t           *testing.T
	root        string
	coordinator issue85MutationCoordinator
	executor    *issue85Executor
	tokens      *issue85TokenVerifier
	policy      *issue85PolicyProvider
	persistence *issue85PersistenceProbe
	clock       *issue85Clock
	scheduler   *issue85Scheduler
	events      *issue85EventLog
	epoch       uint64
	generation  uint64
	target      eebusraw.FeatureTargetV1
	before      eebusraw.TypedValueV1
	requested   eebusraw.TypedValueV1
	third       eebusraw.TypedValueV1
	auth        eebusraw.WriteAuthorizationV1
	readAuth    eebusraw.ReadAuthorizationV1
	request     eebusraw.FeatureDataSetRequestV1
	config      rawMutationCoordinatorConfig
	deps        rawMutationCoordinatorDependencies
	cleanable   bool
}

type issue85HarnessOption func(*issue85Harness)

func (harness *issue85Harness) CurrentRuntimeBinding(
	_ eebusraw.FeatureTargetV1,
) (eebusraw.RuntimeBindingV1, *eebusraw.ErrorV1) {
	return eebusraw.RuntimeBindingV1{
		RuntimeEpoch:         harness.epoch,
		ConnectionGeneration: harness.generation,
	}, nil
}

func newIssue85Harness(t *testing.T, options ...issue85HarnessOption) *issue85Harness {
	t.Helper()
	harness := issue85HarnessDraft(t)
	for _, option := range options {
		option(harness)
	}
	harness.open()
	return harness
}

func issue85HarnessDraft(t *testing.T) *issue85Harness {
	t.Helper()
	before := issue85Value(t, map[string]any{"limit": int64(18), "unit": "degC"})
	requested := issue85Value(t, map[string]any{"limit": int64(20), "unit": "degC"})
	third := issue85Value(t, map[string]any{"limit": int64(19), "unit": "degC"})
	target := eebusraw.FeatureTargetV1{
		RemoteSKI:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SHIPID:         "vr940-runtime",
		DeviceAddress:  "remote-device",
		EntityAddress:  []uint64{1},
		FeatureAddress: 11,
		FeatureType:    "measurement",
		FeatureRole:    eebusraw.FeatureRoleV1Server,
		Function:       "measurementListData",
		Operation:      eebusraw.OperationV1Write,
	}
	epoch := uint64(7)
	generation := uint64(11)
	clock := &issue85Clock{current: time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)}
	events := &issue85EventLog{}
	scheduler := newIssue85Scheduler(clock, events)
	executor := &issue85Executor{t: t, events: events}
	persistence := &issue85PersistenceProbe{events: events}

	beforeHash, err := before.ComputeHash()
	if err != nil {
		t.Fatal(err)
	}
	readTarget := target.Clone()
	readTarget.Operation = eebusraw.OperationV1Read
	readRequestHash, err := eebusraw.CanonicalSHA256V1(eebusraw.FeatureDataGetRequestV1{
		Targets: []eebusraw.FeatureTargetV1{readTarget},
	})
	if err != nil {
		t.Fatal(err)
	}
	tokens := &issue85TokenVerifier{
		bindings: map[string]rawMutationReadTokenBinding{
			issue85OpaqueReference("read-token-issue85"): {
				Runtime:         eebusraw.RuntimeBindingV1{RuntimeEpoch: epoch, ConnectionGeneration: generation},
				Target:          readTarget,
				RequestHash:     readRequestHash,
				BeforeImageHash: beforeHash,
				PrincipalClass:  "owner",
				Scope:           eebusraw.AuthScopeV1RawRead,
				Tool:            eebusraw.ToolV1FeaturesDataGet,
				MaskTier:        eebusraw.MaskTierRaw,
				ExpiresAt:       clock.Now().Add(5 * time.Minute),
				Reusable:        false,
			},
		},
		consumed: make(map[string]int),
	}
	policy := &issue85PolicyProvider{
		t:              t,
		expectedTarget: target.Clone(),
		expectedBefore: before.Clone(),
		expectedValue:  requested.Clone(),
		maxCalls:       -1,
		decision: rawMutationPolicyDecision{
			FullWrite:             true,
			Changeability:         eebusraw.ChangeabilityV1True,
			ConstraintsKnown:      true,
			LabAllowlisted:        true,
			RollbackRepresentable: true,
		},
	}
	harness := &issue85Harness{
		t:           t,
		root:        t.TempDir(),
		executor:    executor,
		tokens:      tokens,
		policy:      policy,
		persistence: persistence,
		clock:       clock,
		scheduler:   scheduler,
		events:      events,
		epoch:       epoch,
		generation:  generation,
		target:      target,
		before:      before,
		requested:   requested,
		third:       third,
		auth: eebusraw.WriteAuthorizationV1{
			PrincipalClass: "owner",
			Scope:          eebusraw.AuthScopeV1RawWrite,
			Tool:           eebusraw.ToolV1FeaturesDataSet,
			MaskTier:       eebusraw.MaskTierRaw,
		},
		readAuth: eebusraw.ReadAuthorizationV1{
			PrincipalClass: "owner",
			Scope:          eebusraw.AuthScopeV1RawRead,
			Tool:           eebusraw.ToolV1MutationsGet,
			MaskTier:       eebusraw.MaskTierRaw,
		},
		request: eebusraw.FeatureDataSetRequestV1{
			Target:         target.Clone(),
			Value:          requested.Clone(),
			ReadToken:      issue85OpaqueReference("read-token-issue85"),
			IdempotencyKey: "issue85-idempotency-key",
			Mode:           eebusraw.ModeV1Apply,
		},
		cleanable: true,
	}
	harness.config = rawMutationCoordinatorConfig{
		StateRoot:        harness.root,
		RuntimeEpoch:     func() uint64 { return harness.epoch },
		Now:              harness.clock.Now,
		WriterWait:       40 * time.Millisecond,
		RecoveryDeadline: 5 * time.Minute,
		ReferenceKey:     []byte("0123456789abcdef0123456789abcdef"),
	}
	harness.deps = rawMutationCoordinatorDependencies{
		Executor:         harness.executor,
		BindingAuthority: harness,
		TokenVerifier:    harness.tokens,
		Policy:           harness.policy,
		Scheduler:        harness.scheduler,
		Persistence:      harness.persistence,
		CrashAfterDurable: func(state eebusraw.MutationStateV1) error {
			harness.events.add("durable-state:" + string(state))
			return nil
		},
	}
	harness.executor.currentBinding = func() eebusraw.RuntimeBindingV1 {
		return eebusraw.RuntimeBindingV1{
			RuntimeEpoch:         harness.epoch,
			ConnectionGeneration: harness.generation,
		}
	}
	harness.executor.setSteps(
		[]issue85ReadStep{
			harness.readStep(before),
			harness.readStep(requested),
		},
		[]issue85WriteStep{harness.writeStep(requested, rawMutationWriteResult{
			FrameSent:  true,
			Correlated: true,
			Accepted:   true,
		})},
	)
	return harness
}

func (harness *issue85Harness) open() {
	harness.t.Helper()
	coordinator, terminal := harness.tryOpen()
	if terminal != nil {
		harness.t.Fatalf("mutation coordinator factory error = %+v", terminal)
	}
	if coordinator == nil {
		harness.t.Fatal("mutation coordinator factory returned nil")
	}
	harness.coordinator = coordinator
	harness.t.Cleanup(func() {
		if harness.coordinator == nil || !harness.cleanable {
			return
		}
		if terminal := harness.coordinator.Close(); terminal != nil {
			harness.t.Errorf("Close() error = %+v", terminal)
		}
		harness.coordinator = nil
	})
}

func (harness *issue85Harness) tryOpen() (issue85MutationCoordinator, *eebusraw.ErrorV1) {
	harness.config.StateRoot = harness.root
	harness.config.RuntimeEpoch = func() uint64 { return harness.epoch }
	harness.config.Now = harness.clock.Now
	harness.deps.Executor = harness.executor
	harness.deps.TokenVerifier = harness.tokens
	harness.deps.Policy = harness.policy
	harness.deps.Scheduler = harness.scheduler
	harness.deps.Persistence = harness.persistence
	return issue85OpenCoordinator(harness.config, harness.deps)
}

func (harness *issue85Harness) closeClean() {
	harness.t.Helper()
	if harness.coordinator == nil {
		return
	}
	if terminal := harness.coordinator.Close(); terminal != nil {
		harness.t.Fatalf("Close() error = %+v", terminal)
	}
	harness.coordinator = nil
}

func issue85WithRoot(root string) issue85HarnessOption {
	return func(harness *issue85Harness) {
		harness.root = root
		harness.config.StateRoot = root
	}
}

func issue85WithCrash(state eebusraw.MutationStateV1) issue85HarnessOption {
	return func(harness *issue85Harness) {
		harness.deps.CrashAfterDurable = func(got eebusraw.MutationStateV1) error {
			harness.events.add("durable-state:" + string(got))
			if got == state {
				return errIssue85InjectedCrash
			}
			return nil
		}
	}
}

func issue85WithAbruptCrash(state eebusraw.MutationStateV1, exitCode int) issue85HarnessOption {
	return func(harness *issue85Harness) {
		harness.cleanable = false
		harness.deps.CrashAfterDurable = func(got eebusraw.MutationStateV1) error {
			harness.events.add("durable-state:" + string(got))
			if got == state {
				os.Exit(exitCode)
			}
			return nil
		}
	}
}

func issue85WithExecutor(executor *issue85Executor) issue85HarnessOption {
	return func(harness *issue85Harness) {
		harness.executor = executor
		harness.deps.Executor = executor
	}
}

func issue85WithTokenVerifier(verifier *issue85TokenVerifier) issue85HarnessOption {
	return func(harness *issue85Harness) {
		harness.tokens = verifier
		harness.deps.TokenVerifier = verifier
	}
}

func issue85WithPolicy(provider *issue85PolicyProvider) issue85HarnessOption {
	return func(harness *issue85Harness) {
		harness.policy = provider
		harness.deps.Policy = provider
	}
}

func issue85WithClock(clock *issue85Clock) issue85HarnessOption {
	return func(harness *issue85Harness) {
		harness.clock = clock
		harness.config.Now = clock.Now
		if harness.scheduler != nil {
			harness.scheduler.clock = clock
		}
	}
}

func issue85WithScheduler(scheduler *issue85Scheduler) issue85HarnessOption {
	return func(harness *issue85Harness) {
		harness.scheduler = scheduler
		harness.deps.Scheduler = scheduler
	}
}

func issue85WithEvents(events *issue85EventLog) issue85HarnessOption {
	return func(harness *issue85Harness) {
		harness.events = events
		if harness.executor != nil {
			harness.executor.events = events
		}
		if harness.scheduler != nil {
			harness.scheduler.events = events
		}
		if harness.persistence != nil {
			harness.persistence.events = events
		}
	}
}

func issue85WithMarkerPath(path string) issue85HarnessOption {
	return func(harness *issue85Harness) {
		harness.events.markerPath = path
	}
}

func issue85WithReferenceKey(key []byte) issue85HarnessOption {
	return func(harness *issue85Harness) {
		harness.config.ReferenceKey = append([]byte(nil), key...)
	}
}

func issue85WithRuntimeBinding(epoch, generation uint64) issue85HarnessOption {
	return func(harness *issue85Harness) {
		harness.epoch = epoch
		harness.generation = generation
	}
}

func issue85WithProfile(profile rawMutationLabProfile) issue85HarnessOption {
	return func(harness *issue85Harness) {
		harness.config.LabProfiles = []rawMutationLabProfile{profile}
	}
}

func (harness *issue85Harness) set() (eebusraw.MutationV1, *eebusraw.ErrorV1) {
	return harness.coordinator.FeaturesDataSet(context.Background(), harness.auth, harness.request)
}

func (harness *issue85Harness) status(ref string) (eebusraw.MutationV1, *eebusraw.ErrorV1) {
	return harness.coordinator.MutationsGet(
		context.Background(),
		harness.readAuth,
		eebusraw.MutationGetRequestV1{MutationRef: ref},
	)
}

func (harness *issue85Harness) rollback(ref, key string) (eebusraw.MutationV1, *eebusraw.ErrorV1) {
	auth := harness.auth
	auth.Tool = eebusraw.ToolV1MutationsRollback
	return harness.coordinator.MutationsRollback(
		context.Background(),
		auth,
		eebusraw.MutationRollbackRequestV1{MutationRef: ref, IdempotencyKey: key},
	)
}

func (harness *issue85Harness) readTarget() eebusraw.FeatureTargetV1 {
	target := harness.target.Clone()
	target.Operation = eebusraw.OperationV1Read
	return target
}

func (harness *issue85Harness) distinctTarget() eebusraw.FeatureTargetV1 {
	return harness.targetVariant(2, 12)
}

func (harness *issue85Harness) targetVariant(
	entityAddress uint64,
	featureAddress uint64,
) eebusraw.FeatureTargetV1 {
	target := harness.request.Target.Clone()
	target.EntityAddress = []uint64{entityAddress}
	target.FeatureAddress = featureAddress
	return target
}

func (harness *issue85Harness) requestForTarget(
	target eebusraw.FeatureTargetV1,
	readToken string,
	idempotencyKey string,
) eebusraw.FeatureDataSetRequestV1 {
	return harness.requestForTargetAndPrincipal(
		target,
		readToken,
		idempotencyKey,
		harness.auth.PrincipalClass,
	)
}

func (harness *issue85Harness) requestForTargetAndPrincipal(
	target eebusraw.FeatureTargetV1,
	readToken string,
	idempotencyKey string,
	principal string,
) eebusraw.FeatureDataSetRequestV1 {
	harness.t.Helper()
	readToken = issue85OpaqueReference(readToken)
	harness.bindReadTokenForPrincipal(readToken, target, harness.before, principal)
	harness.policy.allow(target, harness.before, harness.requested)
	request := harness.request
	request.Target = target.Clone()
	request.Value = harness.requested.Clone()
	request.ReadToken = readToken
	request.IdempotencyKey = idempotencyKey
	return request
}

func issue85OpaqueReference(label string) string {
	digest := sha256.Sum256([]byte(label))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func (harness *issue85Harness) bindReadToken(
	token string,
	target eebusraw.FeatureTargetV1,
	before eebusraw.TypedValueV1,
) {
	harness.bindReadTokenForPrincipal(
		token,
		target,
		before,
		harness.auth.PrincipalClass,
	)
}

func (harness *issue85Harness) bindReadTokenForPrincipal(
	token string,
	target eebusraw.FeatureTargetV1,
	before eebusraw.TypedValueV1,
	principal string,
) {
	harness.t.Helper()
	beforeHash, err := before.ComputeHash()
	if err != nil {
		harness.t.Fatal(err)
	}
	readTarget := target.Clone()
	readTarget.Operation = eebusraw.OperationV1Read
	requestHash, err := eebusraw.CanonicalSHA256V1(eebusraw.FeatureDataGetRequestV1{
		Targets: []eebusraw.FeatureTargetV1{readTarget},
	})
	if err != nil {
		harness.t.Fatal(err)
	}
	harness.tokens.bind(token, rawMutationReadTokenBinding{
		Runtime:         eebusraw.RuntimeBindingV1{RuntimeEpoch: harness.epoch, ConnectionGeneration: harness.generation},
		Target:          readTarget,
		RequestHash:     requestHash,
		BeforeImageHash: beforeHash,
		PrincipalClass:  principal,
		Scope:           eebusraw.AuthScopeV1RawRead,
		Tool:            eebusraw.ToolV1FeaturesDataGet,
		MaskTier:        eebusraw.MaskTierRaw,
		ExpiresAt:       harness.clock.Now().Add(5 * time.Minute),
		Reusable:        false,
	})
}

func (harness *issue85Harness) readStep(value eebusraw.TypedValueV1) issue85ReadStep {
	return harness.readStepForTarget(harness.target, value)
}

func (harness *issue85Harness) readStepForTarget(
	target eebusraw.FeatureTargetV1,
	value eebusraw.TypedValueV1,
) issue85ReadStep {
	readTarget := target.Clone()
	readTarget.Operation = eebusraw.OperationV1Read
	return issue85ReadStep{
		wantTarget: readTarget,
		result: rawMutationReadResult{
			Value:       value.Clone(),
			Runtime:     eebusraw.RuntimeBindingV1{RuntimeEpoch: harness.epoch, ConnectionGeneration: harness.generation},
			Full:        true,
			Trustworthy: true,
		},
	}
}

func (harness *issue85Harness) untrustworthyReadStep(value eebusraw.TypedValueV1) issue85ReadStep {
	step := harness.readStep(value)
	step.result.Trustworthy = false
	return step
}

func (harness *issue85Harness) writeStep(
	value eebusraw.TypedValueV1,
	result rawMutationWriteResult,
) issue85WriteStep {
	return harness.writeStepForTarget(harness.target, value, result)
}

func (harness *issue85Harness) writeStepForTarget(
	target eebusraw.FeatureTargetV1,
	value eebusraw.TypedValueV1,
	result rawMutationWriteResult,
) issue85WriteStep {
	return issue85WriteStep{
		wantTarget: target.Clone(),
		wantValue:  value.Clone(),
		result:     result,
	}
}

func (harness *issue85Harness) exactLabProfile() rawMutationLabProfile {
	requestedHash, err := harness.requested.ComputeHash()
	if err != nil {
		harness.t.Fatal(err)
	}
	beforeHash, err := harness.before.ComputeHash()
	if err != nil {
		harness.t.Fatal(err)
	}
	return rawMutationLabProfile{
		Contract:               "helianthus.eebus.raw-mutation-lab-profile.v1",
		ProfileID:              "issue85-exact-profile",
		Target:                 harness.target.Clone(),
		AllowedValueHashes:     []eebusraw.HashV1{requestedHash},
		RollbackValueHash:      beforeHash,
		MaximumProbeTTLSeconds: 60,
		SafetyPredicates:       []string{"rollback_representable"},
		ExpiresAt:              harness.clock.Now().Add(10 * time.Minute),
	}
}

type issue85Clock struct {
	mu      sync.Mutex
	current time.Time
}

func (clock *issue85Clock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.current
}

func (clock *issue85Clock) Advance(delta time.Duration) {
	clock.mu.Lock()
	clock.current = clock.current.Add(delta)
	clock.mu.Unlock()
}

type issue85ScheduledCall struct {
	id       uint64
	deadline time.Time
	callback func()
	stopped  bool
	fired    bool
}

type issue85Scheduler struct {
	mu      sync.Mutex
	clock   *issue85Clock
	events  *issue85EventLog
	nextID  uint64
	pending []*issue85ScheduledCall
}

type issue85Timer struct {
	scheduler *issue85Scheduler
	call      *issue85ScheduledCall
}

func newIssue85Scheduler(clock *issue85Clock, events *issue85EventLog) *issue85Scheduler {
	return &issue85Scheduler{clock: clock, events: events}
}

func (scheduler *issue85Scheduler) Schedule(deadline time.Time, callback func()) rawMutationTimer {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	scheduler.nextID++
	call := &issue85ScheduledCall{
		id:       scheduler.nextID,
		deadline: deadline,
		callback: callback,
	}
	scheduler.pending = append(scheduler.pending, call)
	scheduler.events.add("timer:scheduled:" + deadline.UTC().Format(time.RFC3339Nano))
	return &issue85Timer{scheduler: scheduler, call: call}
}

func (timer *issue85Timer) Stop() bool {
	timer.scheduler.mu.Lock()
	defer timer.scheduler.mu.Unlock()
	if timer.call.stopped || timer.call.fired {
		return false
	}
	timer.call.stopped = true
	return true
}

func (scheduler *issue85Scheduler) FireDue() int {
	now := scheduler.clock.Now()
	scheduler.mu.Lock()
	var due []*issue85ScheduledCall
	for _, call := range scheduler.pending {
		if !call.stopped && !call.fired && !call.deadline.After(now) {
			call.fired = true
			due = append(due, call)
		}
	}
	sort.Slice(due, func(i, j int) bool {
		if due[i].deadline.Equal(due[j].deadline) {
			return due[i].id < due[j].id
		}
		return due[i].deadline.Before(due[j].deadline)
	})
	scheduler.mu.Unlock()
	for _, call := range due {
		scheduler.events.add("timer:fired:" + call.deadline.UTC().Format(time.RFC3339Nano))
		call.callback()
	}
	return len(due)
}

func (scheduler *issue85Scheduler) pendingCount() int {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	count := 0
	for _, call := range scheduler.pending {
		if !call.stopped && !call.fired {
			count++
		}
	}
	return count
}

type issue85EventLog struct {
	mu         sync.Mutex
	events     []string
	markerPath string
}

func (log *issue85EventLog) add(event string) {
	log.mu.Lock()
	log.events = append(log.events, event)
	markerPath := log.markerPath
	log.mu.Unlock()
	if markerPath == "" {
		return
	}
	file, err := os.OpenFile(markerPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = file.WriteString(event + "\n")
	_ = file.Sync()
	_ = file.Close()
}

func (log *issue85EventLog) snapshot() []string {
	log.mu.Lock()
	defer log.mu.Unlock()
	return append([]string(nil), log.events...)
}

func (log *issue85EventLog) reset() {
	log.mu.Lock()
	log.events = nil
	log.mu.Unlock()
}

type issue85PersistenceSeam interface {
	SyncFile(*os.File) error
	SyncDirectory(*os.File) error
}

const (
	issue85PersistenceFileSync      = "file_sync"
	issue85PersistenceDirectorySync = "directory_sync"
)

type issue85PersistenceProbe struct {
	mu       sync.Mutex
	events   *issue85EventLog
	failNext string
}

var _ issue85PersistenceSeam = (*issue85PersistenceProbe)(nil)

func (probe *issue85PersistenceProbe) SyncFile(file *os.File) error {
	if err := probe.beforeSync(issue85PersistenceFileSync); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	probe.afterSync(issue85PersistenceFileSync)
	return nil
}

func (probe *issue85PersistenceProbe) SyncDirectory(directory *os.File) error {
	if err := probe.beforeSync(issue85PersistenceDirectorySync); err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		return err
	}
	probe.afterSync(issue85PersistenceDirectorySync)
	return nil
}

func (probe *issue85PersistenceProbe) beforeSync(operation string) error {
	probe.mu.Lock()
	events := probe.events
	fail := probe.failNext == operation
	if fail {
		probe.failNext = ""
	}
	probe.mu.Unlock()
	if events != nil {
		events.add("persistence:attempt:" + operation)
	}
	if fail {
		return errIssue85InjectedPersistence
	}
	return nil
}

func (probe *issue85PersistenceProbe) afterSync(operation string) {
	probe.mu.Lock()
	events := probe.events
	probe.mu.Unlock()
	if events != nil {
		events.add("persistence:synced:" + operation)
	}
}

func (probe *issue85PersistenceProbe) failNextSync(operation string) {
	probe.mu.Lock()
	probe.failNext = operation
	probe.mu.Unlock()
}

type issue85ReadStep struct {
	wantTarget eebusraw.FeatureTargetV1
	result     rawMutationReadResult
	terminal   *eebusraw.ErrorV1
	block      <-chan struct{}
}

type issue85WriteStep struct {
	wantTarget eebusraw.FeatureTargetV1
	wantValue  eebusraw.TypedValueV1
	result     rawMutationWriteResult
	terminal   *eebusraw.ErrorV1
	block      <-chan struct{}
}

type issue85Executor struct {
	mu             sync.Mutex
	t              testing.TB
	events         *issue85EventLog
	reads          []issue85ReadStep
	writes         []issue85WriteStep
	readCalls      int
	writeCalls     int
	active         int
	maxActive      int
	exhausted      int
	hardFailure    func()
	currentBinding func() eebusraw.RuntimeBindingV1
}

func (executor *issue85Executor) FullReadIfCurrent(
	ctx context.Context,
	target eebusraw.FeatureTargetV1,
	expected eebusraw.RuntimeBindingV1,
) (rawMutationReadResult, *eebusraw.ErrorV1) {
	executor.mu.Lock()
	if executor.currentBinding != nil && executor.currentBinding() != expected {
		executor.mu.Unlock()
		return rawMutationReadResult{}, issue85Error(eebusraw.ErrorCodeV1ConnectionGenerationMismatch)
	}
	index := executor.readCalls
	executor.readCalls++
	executor.active++
	if executor.active > executor.maxActive {
		executor.maxActive = executor.active
	}
	if index >= len(executor.reads) {
		executor.exhausted++
		executor.mu.Unlock()
		executor.finishCall()
		executor.failf("unexpected READ #%d exhausted script: target=%+v", index+1, target)
		return rawMutationReadResult{}, issue85Error(eebusraw.ErrorCodeV1Internal)
	}
	step := executor.reads[index]
	executor.mu.Unlock()

	executor.events.add("remote:READ:" + target.Function)
	if !reflect.DeepEqual(target, step.wantTarget) {
		executor.failf("READ #%d target = %+v, want exact %+v", index+1, target, step.wantTarget)
	}
	if step.block != nil {
		select {
		case <-step.block:
		case <-ctx.Done():
			executor.finishCall()
			return rawMutationReadResult{}, issue85Error(eebusraw.ErrorCodeV1Cancelled)
		}
	}
	executor.finishCall()
	if step.terminal != nil {
		cloned := step.terminal.Clone()
		return cloneIssue85ReadResult(step.result), &cloned
	}
	return cloneIssue85ReadResult(step.result), nil
}

func (executor *issue85Executor) FullWriteIfCurrent(
	ctx context.Context,
	target eebusraw.FeatureTargetV1,
	value eebusraw.TypedValueV1,
	expected eebusraw.RuntimeBindingV1,
) (rawMutationWriteResult, *eebusraw.ErrorV1) {
	executor.mu.Lock()
	if executor.currentBinding != nil && executor.currentBinding() != expected {
		executor.mu.Unlock()
		return rawMutationWriteResult{}, issue85Error(eebusraw.ErrorCodeV1ConnectionGenerationMismatch)
	}
	index := executor.writeCalls
	executor.writeCalls++
	executor.active++
	if executor.active > executor.maxActive {
		executor.maxActive = executor.active
	}
	if index >= len(executor.writes) {
		executor.exhausted++
		executor.mu.Unlock()
		executor.finishCall()
		executor.failf("unexpected WRITE #%d exhausted script: target=%+v value=%s", index+1, target, issue85ValueJSON(value))
		return rawMutationWriteResult{}, issue85Error(eebusraw.ErrorCodeV1Internal)
	}
	step := executor.writes[index]
	executor.mu.Unlock()

	hash, _ := value.ComputeHash()
	executor.events.add("remote:WRITE:" + string(hash))
	if !reflect.DeepEqual(target, step.wantTarget) {
		executor.failf("WRITE #%d target = %+v, want exact %+v", index+1, target, step.wantTarget)
	}
	if !issue85ValuesEqual(value, step.wantValue) {
		executor.failf("WRITE #%d value = %s, want exact %s", index+1, issue85ValueJSON(value), issue85ValueJSON(step.wantValue))
	}
	if step.block != nil {
		select {
		case <-step.block:
		case <-ctx.Done():
			executor.finishCall()
			return rawMutationWriteResult{FrameSent: true}, issue85Error(eebusraw.ErrorCodeV1Cancelled)
		}
	}
	executor.finishCall()
	if step.terminal != nil {
		cloned := step.terminal.Clone()
		return step.result, &cloned
	}
	return step.result, nil
}

func cloneIssue85ReadResult(result rawMutationReadResult) rawMutationReadResult {
	result.Value = result.Value.Clone()
	return result
}

func (executor *issue85Executor) finishCall() {
	executor.mu.Lock()
	executor.active--
	executor.mu.Unlock()
}

func (executor *issue85Executor) failf(format string, arguments ...any) {
	executor.mu.Lock()
	hardFailure := executor.hardFailure
	executor.mu.Unlock()
	if hardFailure != nil {
		hardFailure()
		return
	}
	executor.t.Errorf(format, arguments...)
}

func (executor *issue85Executor) setHardFailure(handler func()) {
	executor.mu.Lock()
	executor.hardFailure = handler
	executor.mu.Unlock()
}

func (executor *issue85Executor) counts() (reads, writes, maxActive, exhausted int) {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return executor.readCalls, executor.writeCalls, executor.maxActive, executor.exhausted
}

func (executor *issue85Executor) setSteps(reads []issue85ReadStep, writes []issue85WriteStep) {
	executor.mu.Lock()
	executor.reads = append([]issue85ReadStep(nil), reads...)
	executor.writes = append([]issue85WriteStep(nil), writes...)
	executor.readCalls = 0
	executor.writeCalls = 0
	executor.maxActive = 0
	executor.active = 0
	executor.exhausted = 0
	executor.mu.Unlock()
}

type issue85TokenVerifier struct {
	mu           sync.Mutex
	bindings     map[string]rawMutationReadTokenBinding
	consumed     map[string]int
	terminal     *eebusraw.ErrorV1
	verifyCalls  int
	consumeCalls int
}

func (verifier *issue85TokenVerifier) VerifyReadToken(
	_ context.Context,
	token string,
) (rawMutationReadTokenBinding, *eebusraw.ErrorV1) {
	verifier.mu.Lock()
	defer verifier.mu.Unlock()
	verifier.verifyCalls++
	if verifier.terminal != nil {
		cloned := verifier.terminal.Clone()
		return rawMutationReadTokenBinding{}, &cloned
	}
	binding, ok := verifier.bindings[token]
	if !ok {
		return rawMutationReadTokenBinding{}, issue85Error(eebusraw.ErrorCodeV1StaleReadToken)
	}
	binding.Target = binding.Target.Clone()
	return binding, nil
}

func (verifier *issue85TokenVerifier) ConsumeReadToken(
	_ context.Context,
	token string,
) *eebusraw.ErrorV1 {
	verifier.mu.Lock()
	defer verifier.mu.Unlock()
	verifier.consumeCalls++
	binding, ok := verifier.bindings[token]
	if !ok || (!binding.Reusable && verifier.consumed[token] != 0) {
		return issue85Error(eebusraw.ErrorCodeV1StaleReadToken)
	}
	verifier.consumed[token]++
	return nil
}

func (verifier *issue85TokenVerifier) counts() (verify, consume int) {
	verifier.mu.Lock()
	defer verifier.mu.Unlock()
	return verifier.verifyCalls, verifier.consumeCalls
}

func (verifier *issue85TokenVerifier) bind(token string, binding rawMutationReadTokenBinding) {
	verifier.mu.Lock()
	binding.Target = binding.Target.Clone()
	verifier.bindings[token] = binding
	verifier.mu.Unlock()
}

type issue85PolicyExpectation struct {
	target eebusraw.FeatureTargetV1
	before eebusraw.TypedValueV1
	value  eebusraw.TypedValueV1
}

type issue85PolicyProvider struct {
	mu             sync.Mutex
	t              testing.TB
	expectedTarget eebusraw.FeatureTargetV1
	expectedBefore eebusraw.TypedValueV1
	expectedValue  eebusraw.TypedValueV1
	decision       rawMutationPolicyDecision
	terminal       *eebusraw.ErrorV1
	calls          int
	maxCalls       int
	additional     []issue85PolicyExpectation
	hardFailure    func()
}

func (provider *issue85PolicyProvider) MutationPolicy(
	_ context.Context,
	target eebusraw.FeatureTargetV1,
	before eebusraw.TypedValueV1,
	value eebusraw.TypedValueV1,
) (rawMutationPolicyDecision, *eebusraw.ErrorV1) {
	provider.mu.Lock()
	provider.calls++
	call := provider.calls
	maxCalls := provider.maxCalls
	matched := reflect.DeepEqual(target, provider.expectedTarget) &&
		issue85ValuesEqual(before, provider.expectedBefore) &&
		issue85ValuesEqual(value, provider.expectedValue)
	for _, expectation := range provider.additional {
		if reflect.DeepEqual(target, expectation.target) &&
			issue85ValuesEqual(before, expectation.before) &&
			issue85ValuesEqual(value, expectation.value) {
			matched = true
			break
		}
	}
	var terminal *eebusraw.ErrorV1
	if provider.terminal != nil {
		cloned := provider.terminal.Clone()
		terminal = &cloned
	}
	result := provider.decision
	result.ConstraintFailures = append([]string(nil), result.ConstraintFailures...)
	result.SafetyFailures = append([]string(nil), result.SafetyFailures...)
	provider.mu.Unlock()

	if maxCalls >= 0 && call > maxCalls {
		provider.failf(
			"unexpected policy call #%d exhausted script: target=%+v before=%s requested=%s",
			call,
			target,
			issue85ValueJSON(before),
			issue85ValueJSON(value),
		)
	}
	if !matched {
		provider.failf(
			"policy inputs target=%+v before=%s requested=%s did not match an allowed fixture",
			target,
			issue85ValueJSON(before),
			issue85ValueJSON(value),
		)
	}
	if terminal != nil {
		return rawMutationPolicyDecision{}, terminal
	}
	return result, nil
}

func (provider *issue85PolicyProvider) failf(format string, arguments ...any) {
	provider.mu.Lock()
	hardFailure := provider.hardFailure
	provider.mu.Unlock()
	if hardFailure != nil {
		hardFailure()
		return
	}
	provider.t.Errorf(format, arguments...)
}

func (provider *issue85PolicyProvider) setHardFailure(handler func()) {
	provider.mu.Lock()
	provider.hardFailure = handler
	provider.mu.Unlock()
}

func (provider *issue85PolicyProvider) setMaxCalls(maxCalls int) {
	provider.mu.Lock()
	provider.maxCalls = maxCalls
	provider.mu.Unlock()
}

func (provider *issue85PolicyProvider) setExpectedTarget(target eebusraw.FeatureTargetV1) {
	provider.mu.Lock()
	provider.expectedTarget = target.Clone()
	provider.mu.Unlock()
}

func (provider *issue85PolicyProvider) count() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.calls
}

func (provider *issue85PolicyProvider) allow(
	target eebusraw.FeatureTargetV1,
	before eebusraw.TypedValueV1,
	value eebusraw.TypedValueV1,
) {
	provider.mu.Lock()
	provider.additional = append(provider.additional, issue85PolicyExpectation{
		target: target.Clone(),
		before: before.Clone(),
		value:  value.Clone(),
	})
	provider.mu.Unlock()
}

func issue85Value(t testing.TB, value any) eebusraw.TypedValueV1 {
	t.Helper()
	result, err := eebusraw.NewTypedValueV1(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func issue85Error(code eebusraw.ErrorCodeV1) *eebusraw.ErrorV1 {
	return eebusraw.NewErrorV1(code, "issue85 classified failure", false, eebusraw.SourceLayerV1Runtime)
}

func issue85ErrorWith(code eebusraw.ErrorCodeV1, message string, retriable bool) *eebusraw.ErrorV1 {
	return eebusraw.NewErrorV1(code, message, retriable, eebusraw.SourceLayerV1Runtime)
}

func issue85Bool(value bool) *bool {
	return &value
}

func issue85ValuesEqual(left, right eebusraw.TypedValueV1) bool {
	leftHash, leftErr := left.ComputeHash()
	rightHash, rightErr := right.ComputeHash()
	return leftErr == nil && rightErr == nil && leftHash == rightHash
}

func issue85ValueJSON(value eebusraw.TypedValueV1) string {
	data, err := json.Marshal(value)
	if err != nil {
		return "<invalid>"
	}
	return string(data)
}

func issue85AssertState(t testing.TB, mutation eebusraw.MutationV1, state eebusraw.MutationStateV1) {
	t.Helper()
	if mutation.State != state {
		t.Fatalf("mutation state = %q, want %q: %+v", mutation.State, state, mutation)
	}
	if terminal := eebusraw.ValidateMutationV1(mutation); terminal != nil {
		payload, _ := json.Marshal(mutation)
		t.Fatalf(
			"mutation state %q failed canonical validation: %+v\nmutation: %s",
			state,
			terminal,
			payload,
		)
	}
}

func issue85AssertError(t testing.TB, terminal *eebusraw.ErrorV1, code eebusraw.ErrorCodeV1) {
	t.Helper()
	if terminal == nil || terminal.Code != code {
		t.Fatalf("error = %+v, want code %q", terminal, code)
	}
}

func issue85AssertNoError(t testing.TB, terminal *eebusraw.ErrorV1) {
	t.Helper()
	if terminal != nil {
		t.Fatalf("unexpected error = %+v", terminal)
	}
}

func issue85Index(events []string, want string) int {
	for index, event := range events {
		if event == want {
			return index
		}
	}
	return -1
}

func issue85RequireOrder(t testing.TB, events []string, ordered ...string) {
	t.Helper()
	position := -1
	for _, want := range ordered {
		found := -1
		for index := position + 1; index < len(events); index++ {
			if events[index] == want {
				found = index
				break
			}
		}
		if found < 0 {
			t.Fatalf("events omit %q after %d: %v", want, position, events)
		}
		position = found
	}
}

func issue85StateNames(audit []eebusraw.AuditTransitionV1) []string {
	result := make([]string, len(audit))
	for index := range audit {
		result[index] = string(audit[index].State)
	}
	return result
}

func issue85Eventually(t testing.TB, condition func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func issue85ReadMarker(t testing.TB, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		t.Fatal(err)
	}
	var result []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}

func (harness *issue85Harness) String() string {
	return fmt.Sprintf("issue85Harness{root:%q}", harness.root)
}
