package eebusfacade

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusmutation"
)

type issue95FacadeRuntime struct {
	mu           sync.Mutex
	binding      eebusraw.RuntimeBindingV1
	current      eebusraw.TypedValueV1
	bindingCalls int
	readCalls    int
	writeCalls   int
	policyCalls  int
	blockRead    <-chan struct{}
	readStarted  chan struct{}
}

func (runtime *issue95FacadeRuntime) CurrentRuntimeBinding(
	eebusraw.FeatureTargetV1,
) (eebusraw.RuntimeBindingV1, *eebusraw.ErrorV1) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.bindingCalls++
	return runtime.binding, nil
}

func (runtime *issue95FacadeRuntime) FullReadIfCurrent(
	_ context.Context,
	_ eebusraw.FeatureTargetV1,
	expected eebusraw.RuntimeBindingV1,
) (eebusmutation.ReadResult, *eebusraw.ErrorV1) {
	runtime.mu.Lock()
	runtime.readCalls++
	if expected != runtime.binding {
		runtime.mu.Unlock()
		return eebusmutation.ReadResult{}, rawMutationFacadeError(
			eebusraw.ErrorCodeV1ConnectionGenerationMismatch,
			false,
		)
	}
	blockRead := runtime.blockRead
	readStarted := runtime.readStarted
	runtime.blockRead = nil
	runtime.readStarted = nil
	current := runtime.current.Clone()
	runtime.mu.Unlock()
	if readStarted != nil {
		close(readStarted)
	}
	if blockRead != nil {
		<-blockRead
	}
	return eebusmutation.ReadResult{
		Value:       current,
		Runtime:     runtime.binding,
		Full:        true,
		Trustworthy: true,
	}, nil
}

func (runtime *issue95FacadeRuntime) FullWriteIfCurrent(
	_ context.Context,
	_ eebusraw.FeatureTargetV1,
	value eebusraw.TypedValueV1,
	expected eebusraw.RuntimeBindingV1,
) (eebusmutation.WriteResult, *eebusraw.ErrorV1) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.writeCalls++
	if expected != runtime.binding {
		return eebusmutation.WriteResult{}, rawMutationFacadeError(
			eebusraw.ErrorCodeV1ConnectionGenerationMismatch,
			false,
		)
	}
	runtime.current = value.Clone()
	return eebusmutation.WriteResult{
		FrameSent:  true,
		Correlated: true,
		Accepted:   true,
	}, nil
}

func (runtime *issue95FacadeRuntime) MutationPolicy(
	context.Context,
	eebusraw.FeatureDataSetRequestV1,
	eebusraw.TypedValueV1,
) (eebusmutation.PolicyDecision, *eebusraw.ErrorV1) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.policyCalls++
	return eebusmutation.PolicyDecision{
		FullWrite:             true,
		Changeability:         eebusraw.ChangeabilityV1True,
		ConstraintsKnown:      true,
		LabAllowlisted:        true,
		RollbackRepresentable: true,
	}, nil
}

func (runtime *issue95FacadeRuntime) callCounts() (int, int, int, int) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.bindingCalls, runtime.readCalls, runtime.writeCalls, runtime.policyCalls
}

func (runtime *issue95FacadeRuntime) blockNextRead() (<-chan struct{}, chan<- struct{}) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	started := make(chan struct{})
	release := make(chan struct{})
	runtime.readStarted = started
	runtime.blockRead = release
	return started, release
}

type issue95FacadeFixture struct {
	backend   *serviceBackend
	issuer    *rawReadTokenIssuer
	runtime   *issue95FacadeRuntime
	now       *time.Time
	root      string
	binding   eebusraw.RuntimeBindingV1
	target    eebusraw.FeatureTargetV1
	before    eebusraw.TypedValueV1
	requested eebusraw.TypedValueV1
	readAuth  eebusraw.ReadAuthorizationV1
	writeAuth eebusraw.WriteAuthorizationV1
	request   eebusraw.FeatureDataSetRequestV1
	token     string
}

func newIssue95FacadeFixture(t *testing.T) *issue95FacadeFixture {
	t.Helper()
	current := time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC)
	before := issue95FacadeValue(t, int64(23))
	requested := issue95FacadeValue(t, int64(22))
	binding := eebusraw.RuntimeBindingV1{
		RuntimeEpoch:         2,
		ConnectionGeneration: 10,
	}
	target := eebusraw.FeatureTargetV1{
		RemoteSKI:      "b1b7197b064084e4cfef2365105d8d36ff185e5b",
		SHIPID:         "vr940",
		DeviceAddress:  "4",
		EntityAddress:  []uint64{4, 1, 1},
		FeatureAddress: 18,
		FeatureType:    "setpoint",
		FeatureRole:    eebusraw.FeatureRoleV1Server,
		Function:       "setpointListData",
		Operation:      eebusraw.OperationV1Write,
	}
	readTarget := target.Clone()
	readTarget.Operation = eebusraw.OperationV1Read
	requestHash, err := eebusraw.CanonicalSHA256V1(eebusraw.FeatureDataGetRequestV1{
		Targets: []eebusraw.FeatureTargetV1{readTarget},
	})
	if err != nil {
		t.Fatal(err)
	}
	beforeHash, err := before.ComputeHash()
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := newRawReadTokenIssuer(bytes.Repeat([]byte{0x95}, 32))
	if err != nil {
		t.Fatal(err)
	}
	issuer.now = func() time.Time { return current }
	readAuth := eebusraw.ReadAuthorizationV1{
		PrincipalClass: "owner",
		Scope:          eebusraw.AuthScopeV1RawRead,
		Tool:           eebusraw.ToolV1FeaturesDataGet,
		MaskTier:       eebusraw.MaskTierRaw,
	}
	token, err := issuer.issue(
		readAuth,
		readTarget,
		binding,
		requestHash,
		beforeHash,
		current,
	)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &issue95FacadeRuntime{
		binding: binding,
		current: before,
	}
	root := t.TempDir()
	coordinator, terminal := eebusmutation.NewCoordinator(
		eebusmutation.CoordinatorConfig{
			StateRoot: root,
			RuntimeEpoch: func() uint64 {
				return binding.RuntimeEpoch
			},
			Now:              func() time.Time { return current },
			WriterWait:       20 * time.Millisecond,
			RecoveryDeadline: time.Second,
			ReferenceKey:     bytes.Repeat([]byte{0x59}, 32),
		},
		eebusmutation.CoordinatorDependencies{
			Executor:         runtime,
			BindingAuthority: runtime,
			TokenVerifier:    issuer,
			Policy:           runtime,
		},
	)
	if terminal != nil {
		t.Fatalf("NewCoordinator() error = %+v", terminal)
	}
	t.Cleanup(func() {
		if terminal := coordinator.Close(); terminal != nil {
			t.Errorf("Close() error = %+v", terminal)
		}
	})
	writeAuth := eebusraw.WriteAuthorizationV1{
		PrincipalClass: "owner",
		Scope:          eebusraw.AuthScopeV1RawWrite,
		Tool:           eebusraw.ToolV1FeaturesDataSet,
		MaskTier:       eebusraw.MaskTierRaw,
	}
	request := eebusraw.FeatureDataSetRequestV1{
		Target:         target,
		Value:          requested,
		ReadToken:      token.ReadToken,
		IdempotencyKey: "issue95-stage-bound-set",
		Mode:           eebusraw.ModeV1Apply,
	}
	return &issue95FacadeFixture{
		backend: &serviceBackend{
			rawFeatures:  &rawFeatureRuntimeBridge{tokenIssuer: issuer},
			rawMutations: coordinator,
		},
		issuer: issuer, runtime: runtime, now: &current, root: root,
		binding: binding, target: target, before: before, requested: requested,
		readAuth: readAuth, writeAuth: writeAuth, request: request,
		token: token.ReadToken,
	}
}

func issue95FacadeValue(t *testing.T, value any) eebusraw.TypedValueV1 {
	t.Helper()
	typed, err := eebusraw.NewTypedValueV1(value)
	if err != nil {
		t.Fatal(err)
	}
	return typed
}

func (fixture *issue95FacadeFixture) issueReadToken(
	t *testing.T,
	receivedAt time.Time,
	before eebusraw.TypedValueV1,
) string {
	t.Helper()
	readTarget := fixture.target.Clone()
	readTarget.Operation = eebusraw.OperationV1Read
	requestHash, err := eebusraw.CanonicalSHA256V1(eebusraw.FeatureDataGetRequestV1{
		Targets: []eebusraw.FeatureTargetV1{readTarget},
	})
	if err != nil {
		t.Fatal(err)
	}
	beforeHash, err := before.ComputeHash()
	if err != nil {
		t.Fatal(err)
	}
	token, err := fixture.issuer.issue(
		fixture.readAuth,
		readTarget,
		fixture.binding,
		requestHash,
		beforeHash,
		receivedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	return token.ReadToken
}

func issue95AssertOutcomeNonLeak(
	t *testing.T,
	outcome RawMutationOutcomeV1,
	terminal *eebusraw.ErrorV1,
	secrets ...string,
) {
	t.Helper()
	encoded, err := json.Marshal(struct {
		Outcome RawMutationOutcomeV1
		Error   *eebusraw.ErrorV1
	}{Outcome: outcome, Error: terminal})
	if err != nil {
		t.Fatal(err)
	}
	diagnostic := fmt.Sprintf("%s\n%+v\n%#v", encoded, outcome, terminal)
	for _, forbidden := range append(secrets,
		`"candidate_ref"`,
		`"read_token"`,
		`"idempotency_key"`,
		`"private_key"`,
		`"private_pem"`,
		`"credential_token"`,
		"PRIVATE KEY",
		"BEGIN PRIVATE",
	) {
		if forbidden != "" && strings.Contains(diagnostic, forbidden) {
			t.Fatalf("terminal outcome leaked forbidden value %q", forbidden)
		}
	}
}

func TestIssue95AuthenticatedExpiredTokenCarriesRuntimeWithoutContactOrWAL(t *testing.T) {
	fixture := newIssue95FacadeFixture(t)
	*fixture.now = fixture.now.Add(2 * time.Minute)

	outcome, terminal := fixture.backend.FeaturesDataSet(
		context.Background(),
		fixture.writeAuth,
		fixture.request,
	)
	if terminal == nil ||
		terminal.Code != eebusraw.ErrorCodeV1StaleReadToken ||
		terminal.Message != "raw mutation read token is stale" ||
		terminal.Retriable ||
		terminal.SourceLayer != eebusraw.SourceLayerV1Runtime {
		t.Fatalf("expired token error = %+v", terminal)
	}
	if !reflect.DeepEqual(outcome.Mutation, eebusraw.MutationV1{}) ||
		outcome.Runtime == nil ||
		*outcome.Runtime != fixture.binding {
		t.Fatalf("expired token outcome = %+v, want zero mutation and runtime %+v", outcome, fixture.binding)
	}
	if bindingCalls, reads, writes, policies := fixture.runtime.callCounts(); bindingCalls != 0 ||
		reads != 0 || writes != 0 || policies != 0 {
		t.Fatalf(
			"expired token reached runtime: binding=%d reads=%d writes=%d policies=%d",
			bindingCalls,
			reads,
			writes,
			policies,
		)
	}
	wal, err := os.ReadFile(filepath.Join(fixture.root, "eebusmutation", "mutation-v1.wal"))
	if err != nil {
		t.Fatal(err)
	}
	if len(wal) != 0 {
		t.Fatalf("expired token wrote %d WAL bytes", len(wal))
	}
	issue95AssertOutcomeNonLeak(
		t,
		outcome,
		terminal,
		fixture.token,
		fixture.request.IdempotencyKey,
	)
}

func TestIssue95ExpiredTokenPrecedesContendedWriterLease(t *testing.T) {
	fixture := newIssue95FacadeFixture(t)
	started, release := fixture.runtime.blockNextRead()
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	type setResult struct {
		outcome  RawMutationOutcomeV1
		terminal *eebusraw.ErrorV1
	}
	firstResult := make(chan setResult, 1)
	go func() {
		outcome, terminal := fixture.backend.FeaturesDataSet(
			context.Background(),
			fixture.writeAuth,
			fixture.request,
		)
		firstResult <- setResult{outcome: outcome, terminal: terminal}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first SET did not acquire the writer lease")
	}

	issuedAt := *fixture.now
	expiredRequest := fixture.request
	expiredRequest.ReadToken = fixture.issueReadToken(
		t,
		issuedAt.Add(10*time.Second),
		fixture.before,
	)
	expiredRequest.IdempotencyKey = "issue95-expired-contender"
	*fixture.now = issuedAt.Add(2 * time.Minute)
	beforeBinding, beforeReads, beforeWrites, beforePolicies :=
		fixture.runtime.callCounts()
	outcome, terminal := fixture.backend.FeaturesDataSet(
		context.Background(),
		fixture.writeAuth,
		expiredRequest,
	)
	if terminal == nil ||
		terminal.Code != eebusraw.ErrorCodeV1StaleReadToken ||
		outcome.Runtime == nil ||
		*outcome.Runtime != fixture.binding ||
		!reflect.DeepEqual(outcome.Mutation, eebusraw.MutationV1{}) {
		t.Fatalf("contended expired outcome = %+v, terminal = %+v", outcome, terminal)
	}
	afterBinding, afterReads, afterWrites, afterPolicies :=
		fixture.runtime.callCounts()
	if afterBinding != beforeBinding ||
		afterReads != beforeReads ||
		afterWrites != beforeWrites ||
		afterPolicies != beforePolicies {
		t.Fatalf(
			"expired contender reached runtime: before=%d/%d/%d/%d after=%d/%d/%d/%d",
			beforeBinding,
			beforeReads,
			beforeWrites,
			beforePolicies,
			afterBinding,
			afterReads,
			afterWrites,
			afterPolicies,
		)
	}
	close(release)
	released = true
	first := <-firstResult
	if first.terminal != nil ||
		first.outcome.Mutation.State != eebusraw.MutationStateV1Applied {
		t.Fatalf("writer holder outcome = %+v, terminal = %+v", first.outcome, first.terminal)
	}
}

func TestIssue95UntrustedTokensReturnSameCanonicalErrorWithoutRuntime(t *testing.T) {
	for _, test := range []struct {
		name  string
		token string
	}{
		{name: "malformed", token: "malformed"},
		{name: "unknown", token: strings.Repeat("A", 43)},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newIssue95FacadeFixture(t)
			request := fixture.request
			request.ReadToken = test.token
			request.IdempotencyKey += "-" + test.name

			outcome, terminal := fixture.backend.FeaturesDataSet(
				context.Background(),
				fixture.writeAuth,
				request,
			)
			if terminal == nil ||
				terminal.Code != eebusraw.ErrorCodeV1StaleReadToken ||
				terminal.Message != "raw mutation read token is stale" ||
				terminal.Retriable ||
				terminal.SourceLayer != eebusraw.SourceLayerV1Runtime {
				t.Fatalf("%s token error = %+v", test.name, terminal)
			}
			if !reflect.DeepEqual(outcome, RawMutationOutcomeV1{}) {
				t.Fatalf("%s token outcome = %+v, want unbound zero outcome", test.name, outcome)
			}
			if bindingCalls, reads, writes, policies := fixture.runtime.callCounts(); bindingCalls != 0 ||
				reads != 0 || writes != 0 || policies != 0 {
				t.Fatalf(
					"%s token reached runtime: binding=%d reads=%d writes=%d policies=%d",
					test.name,
					bindingCalls,
					reads,
					writes,
					policies,
				)
			}
			wal, err := os.ReadFile(filepath.Join(fixture.root, "eebusmutation", "mutation-v1.wal"))
			if err != nil {
				t.Fatal(err)
			}
			if len(wal) != 0 {
				t.Fatalf("%s token wrote %d WAL bytes", test.name, len(wal))
			}
		})
	}
}

func TestIssue95ResolvedMutationOperationsCarryStoredRuntime(t *testing.T) {
	fixture := newIssue95FacadeFixture(t)
	applied, terminal := fixture.backend.FeaturesDataSet(
		context.Background(),
		fixture.writeAuth,
		fixture.request,
	)
	if terminal != nil ||
		applied.Mutation.State != eebusraw.MutationStateV1Applied ||
		applied.Runtime == nil ||
		*applied.Runtime != fixture.binding {
		t.Fatalf("applied outcome = %+v, terminal = %+v", applied, terminal)
	}
	issue95AssertOutcomeNonLeak(
		t,
		applied,
		terminal,
		fixture.token,
		fixture.request.IdempotencyKey,
	)
	getAuth := fixture.readAuth
	getAuth.Tool = eebusraw.ToolV1MutationsGet
	got, terminal := fixture.backend.MutationsGet(
		context.Background(),
		getAuth,
		eebusraw.MutationGetRequestV1{MutationRef: applied.Mutation.MutationRef},
	)
	if terminal != nil ||
		got.Mutation.MutationRef != applied.Mutation.MutationRef ||
		got.Runtime == nil ||
		*got.Runtime != fixture.binding {
		t.Fatalf("resolved get outcome = %+v, terminal = %+v", got, terminal)
	}
	missingGet, terminal := fixture.backend.MutationsGet(
		context.Background(),
		getAuth,
		eebusraw.MutationGetRequestV1{MutationRef: strings.Repeat("B", 42) + "A"},
	)
	if terminal == nil ||
		terminal.Code != eebusraw.ErrorCodeV1NotFound ||
		!reflect.DeepEqual(missingGet, RawMutationOutcomeV1{}) {
		t.Fatalf("missing get outcome = %+v, terminal = %+v", missingGet, terminal)
	}
	deniedAuth := getAuth
	deniedAuth.PrincipalClass = "other-owner"
	deniedGet, terminal := fixture.backend.MutationsGet(
		context.Background(),
		deniedAuth,
		eebusraw.MutationGetRequestV1{MutationRef: applied.Mutation.MutationRef},
	)
	if terminal == nil ||
		terminal.Code != eebusraw.ErrorCodeV1PermissionDenied ||
		!reflect.DeepEqual(deniedGet, RawMutationOutcomeV1{}) {
		t.Fatalf("denied get outcome = %+v, terminal = %+v", deniedGet, terminal)
	}
	rollbackAuth := fixture.writeAuth
	rollbackAuth.Tool = eebusraw.ToolV1MutationsRollback
	rolledBack, terminal := fixture.backend.MutationsRollback(
		context.Background(),
		rollbackAuth,
		eebusraw.MutationRollbackRequestV1{
			MutationRef:    applied.Mutation.MutationRef,
			IdempotencyKey: "issue95-stage-bound-rollback",
		},
	)
	if terminal != nil ||
		rolledBack.Mutation.State != eebusraw.MutationStateV1RolledBack ||
		rolledBack.Runtime == nil ||
		*rolledBack.Runtime != fixture.binding {
		t.Fatalf("resolved rollback outcome = %+v, terminal = %+v", rolledBack, terminal)
	}
	missingRollback, terminal := fixture.backend.MutationsRollback(
		context.Background(),
		rollbackAuth,
		eebusraw.MutationRollbackRequestV1{
			MutationRef:    strings.Repeat("C", 42) + "A",
			IdempotencyKey: "issue95-stage-bound-missing-rollback",
		},
	)
	if terminal == nil ||
		terminal.Code != eebusraw.ErrorCodeV1NotFound ||
		!reflect.DeepEqual(missingRollback, RawMutationOutcomeV1{}) {
		t.Fatalf("missing rollback outcome = %+v, terminal = %+v", missingRollback, terminal)
	}
}

func TestIssue95RollbackWriterBusyCarriesResolvedStoredRuntime(t *testing.T) {
	fixture := newIssue95FacadeFixture(t)
	applied, terminal := fixture.backend.FeaturesDataSet(
		context.Background(),
		fixture.writeAuth,
		fixture.request,
	)
	if terminal != nil || applied.Mutation.State != eebusraw.MutationStateV1Applied {
		t.Fatalf("initial SET outcome = %+v, terminal = %+v", applied, terminal)
	}

	holderRequest := fixture.request
	holderRequest.Value = fixture.before
	holderRequest.ReadToken = fixture.issueReadToken(
		t,
		fixture.now.Add(10*time.Second),
		fixture.requested,
	)
	holderRequest.IdempotencyKey = "issue95-rollback-writer-holder"
	started, release := fixture.runtime.blockNextRead()
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	type setResult struct {
		outcome  RawMutationOutcomeV1
		terminal *eebusraw.ErrorV1
	}
	holderResult := make(chan setResult, 1)
	go func() {
		outcome, terminal := fixture.backend.FeaturesDataSet(
			context.Background(),
			fixture.writeAuth,
			holderRequest,
		)
		holderResult <- setResult{outcome: outcome, terminal: terminal}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("second SET did not acquire the writer lease")
	}

	rollbackAuth := fixture.writeAuth
	rollbackAuth.Tool = eebusraw.ToolV1MutationsRollback
	beforeBinding, beforeReads, beforeWrites, beforePolicies :=
		fixture.runtime.callCounts()
	outcome, terminal := fixture.backend.MutationsRollback(
		context.Background(),
		rollbackAuth,
		eebusraw.MutationRollbackRequestV1{
			MutationRef:    applied.Mutation.MutationRef,
			IdempotencyKey: "issue95-contended-rollback",
		},
	)
	if terminal == nil ||
		terminal.Code != eebusraw.ErrorCodeV1WriterBusy ||
		!reflect.DeepEqual(outcome.Mutation, eebusraw.MutationV1{}) ||
		outcome.Runtime == nil ||
		*outcome.Runtime != fixture.binding {
		t.Fatalf("contended rollback outcome = %+v, terminal = %+v", outcome, terminal)
	}
	afterBinding, afterReads, afterWrites, afterPolicies :=
		fixture.runtime.callCounts()
	if afterBinding != beforeBinding ||
		afterReads != beforeReads ||
		afterWrites != beforeWrites ||
		afterPolicies != beforePolicies {
		t.Fatalf(
			"rollback binding used runtime lookup: before=%d/%d/%d/%d after=%d/%d/%d/%d",
			beforeBinding,
			beforeReads,
			beforeWrites,
			beforePolicies,
			afterBinding,
			afterReads,
			afterWrites,
			afterPolicies,
		)
	}
	close(release)
	released = true
	holder := <-holderResult
	if holder.terminal != nil ||
		holder.outcome.Mutation.State != eebusraw.MutationStateV1Applied {
		t.Fatalf("writer holder outcome = %+v, terminal = %+v", holder.outcome, holder.terminal)
	}
}

func TestIssue95DisconnectedOutcomeIsBoundOnlyAfterTokenAdmission(t *testing.T) {
	fixture := newIssue95FacadeFixture(t)
	fixture.backend.rawMutations = nil
	bound, terminal := fixture.backend.FeaturesDataSet(
		context.Background(),
		fixture.writeAuth,
		fixture.request,
	)
	if terminal == nil ||
		terminal.Code != eebusraw.ErrorCodeV1Disconnected ||
		bound.Runtime == nil ||
		*bound.Runtime != fixture.binding ||
		!reflect.DeepEqual(bound.Mutation, eebusraw.MutationV1{}) {
		t.Fatalf("bound disconnected outcome = %+v, terminal = %+v", bound, terminal)
	}
	getAuth := fixture.readAuth
	getAuth.Tool = eebusraw.ToolV1MutationsGet
	unbound, terminal := fixture.backend.MutationsGet(
		context.Background(),
		getAuth,
		eebusraw.MutationGetRequestV1{MutationRef: strings.Repeat("D", 42) + "A"},
	)
	if terminal == nil ||
		terminal.Code != eebusraw.ErrorCodeV1Disconnected ||
		!reflect.DeepEqual(unbound, RawMutationOutcomeV1{}) {
		t.Fatalf("unbound disconnected outcome = %+v, terminal = %+v", unbound, terminal)
	}
	issue95AssertOutcomeNonLeak(t, bound, terminal, fixture.token)
}
