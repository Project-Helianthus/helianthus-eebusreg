package base

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"

	eebusruntime "github.com/Project-Helianthus/helianthus-eebusreg"
	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusfacade"
	semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"
	semdriver "github.com/Project-Helianthus/helianthus-semreg/semreg/v1/driver"
	"github.com/Project-Helianthus/helianthus-semreg/semreg/v1/graph"
)

const (
	feedTokenA = "0123456789abcdef0123456789abcdef"
	feedTokenB = "fedcba9876543210fedcba9876543210"
)

// feedRuntime is the runtime double the feed's binding reads. It does not listen.
type feedRuntime struct {
	node *eebusfacade.OutputNode
}

var _ eebusruntime.Runtime = (*feedRuntime)(nil)

// openedNode is the facade node type. The exported method signature names this alias.
type openedNode = eebusfacade.OutputNode

// capturedCut is one committed cut. The exported method signature names this alias.
type capturedCut = semdriver.SemanticCut

func (runtime *feedRuntime) OutputNode() (*openedNode, error) {
	if runtime == nil || runtime.node == nil {
		return nil, errors.New("existing node is unavailable")
	}
	return runtime.node, nil
}

func (runtime *feedRuntime) Start(context.Context) error { return nil }
func (runtime *feedRuntime) Shutdown() error             { return nil }
func (runtime *feedRuntime) Snapshot() (eebusruntime.SnapshotV1, error) {
	return eebusruntime.SnapshotV1{}, nil
}
func (runtime *feedRuntime) PairingState() ([]eebusruntime.PairingObservationV1, error) {
	return nil, nil
}
func (runtime *feedRuntime) FeaturesGet(context.Context, eebusraw.ReadAuthorizationV1, eebusraw.FeaturesGetRequestV1) (eebusraw.FeaturesGetDataV1, *eebusraw.ErrorV1) {
	return eebusraw.FeaturesGetDataV1{}, nil
}
func (runtime *feedRuntime) FeaturesDataGet(context.Context, eebusraw.ReadAuthorizationV1, eebusraw.FeatureDataGetRequestV1) (eebusraw.FeatureDataGetDataV1, *eebusraw.ErrorV1) {
	return eebusraw.FeatureDataGetDataV1{}, nil
}

func openFeedNode(t *testing.T, port int, token string) *eebusfacade.OutputNode {
	t.Helper()
	service, err := eebusfacade.NewPreparedOutputService(eebusfacade.RuntimeConfig{
		Interface:     "fixture-interface",
		ListenPort:    port,
		ListenAddress: netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(port)),
	}, token)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { service.Shutdown() })
	backend := eebusfacade.NewOutputServiceBackend(service)
	t.Cleanup(func() { _ = backend.Close() })
	node, err := eebusfacade.OpenOutputNode(backend)
	if err != nil {
		t.Fatal(err)
	}
	return node
}

func newFeedBinding(t *testing.T, directory string, node *eebusfacade.OutputNode) *eebusruntime.OutputBinding {
	t.Helper()
	binding, err := eebusruntime.NewOutputBinding(&feedRuntime{node: node}, directory)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

type sumAnswer struct {
	sum graph.MemberSum
	err error
}

// feedScript is one captured SemReg session. Resume blocks until push or cancel.
type feedScript struct {
	mu       sync.Mutex
	notify   chan struct{}
	current  semdriver.SemanticCut
	pending  []semdriver.SemanticCut
	pages    map[string]map[string][]graph.LensResult
	sums     map[string]map[string]sumAnswer
	reads    int
	requests []graph.LensRequest
	sumCalls []recordedSum
}

type recordedSum struct {
	request    graph.LensRequest
	definition semreg.DefinitionRef
	scope      semreg.ResourceID
}

func newFeedScript(ref graph.CutRef) *feedScript {
	return &feedScript{
		notify:  make(chan struct{}, 1),
		current: semdriver.SemanticCut{Ref: ref},
		pages:   map[string]map[string][]graph.LensResult{},
		sums:    map[string]map[string]sumAnswer{},
	}
}

func (script *feedScript) setLens(ref graph.CutRef, lens string, pages ...graph.LensResult) {
	script.mu.Lock()
	defer script.mu.Unlock()
	id := string(ref.CutID)
	if script.pages[id] == nil {
		script.pages[id] = map[string][]graph.LensResult{}
	}
	script.pages[id][lens] = append([]graph.LensResult(nil), pages...)
}

func (script *feedScript) setSum(ref graph.CutRef, resource string, sum graph.MemberSum, err error) {
	script.mu.Lock()
	defer script.mu.Unlock()
	id := string(ref.CutID)
	if script.sums[id] == nil {
		script.sums[id] = map[string]sumAnswer{}
	}
	script.sums[id][resource] = sumAnswer{sum: sum, err: err}
}

func (script *feedScript) push(cut semdriver.SemanticCut) {
	script.mu.Lock()
	script.pending = append(script.pending, cut)
	script.mu.Unlock()
	select {
	case script.notify <- struct{}{}:
	default:
	}
}

func (script *feedScript) readsCount() int {
	script.mu.Lock()
	defer script.mu.Unlock()
	return script.reads
}

func (script *feedScript) Requests() []graph.LensRequest {
	script.mu.Lock()
	defer script.mu.Unlock()
	return append([]graph.LensRequest(nil), script.requests...)
}

func (script *feedScript) Sums() []recordedSum {
	script.mu.Lock()
	defer script.mu.Unlock()
	return append([]recordedSum(nil), script.sumCalls...)
}

func (script *feedScript) Capture(ctx context.Context, _ semdriver.Filter) (capturedCut, error) {
	if err := ctx.Err(); err != nil {
		return semdriver.SemanticCut{}, err
	}
	script.mu.Lock()
	defer script.mu.Unlock()
	if script.current.Ref.CutID == "" && len(script.pending) > 0 {
		script.current = script.pending[0]
		script.pending = script.pending[1:]
	}
	if script.current.Ref.CutID == "" {
		return semdriver.SemanticCut{}, errors.New("no cut")
	}
	return script.current, nil
}

func (script *feedScript) Resume(ctx context.Context, _ semdriver.Cursor, _ semdriver.Filter) (semdriver.Changes, error) {
	for {
		script.mu.Lock()
		if len(script.pending) > 0 {
			cut := script.pending[0]
			script.pending = script.pending[1:]
			script.current = cut
			script.mu.Unlock()
			return semdriver.Changes{Commits: []semdriver.SemanticCut{cut}}, nil
		}
		script.mu.Unlock()
		select {
		case <-ctx.Done():
			return semdriver.Changes{}, ctx.Err()
		case <-script.notify:
		}
	}
}

func (script *feedScript) RetrieveLens(_ context.Context, ref graph.CutRef, request graph.LensRequest) (graph.LensResult, error) {
	script.mu.Lock()
	defer script.mu.Unlock()
	script.reads++
	script.requests = append(script.requests, request)
	pages := script.pages[string(ref.CutID)][string(request.Lens)]
	if len(pages) == 0 {
		return graph.LensResult{Cut: ref}, nil
	}
	index := 0
	if request.PageToken != "" {
		index = 1
		if index >= len(pages) {
			index = 0
		}
	}
	return pages[index], nil
}

func (script *feedScript) SumMembers(_ context.Context, ref graph.CutRef, request graph.LensRequest, definition semreg.DefinitionRef, scope semreg.ResourceID) (graph.MemberSum, error) {
	script.mu.Lock()
	defer script.mu.Unlock()
	script.sumCalls = append(script.sumCalls, recordedSum{request: request, definition: definition, scope: scope})
	answer, ok := script.sums[string(ref.CutID)][string(scope)]
	if !ok {
		return graph.MemberSum{}, nil
	}
	return answer.sum, answer.err
}

type runningFeed struct {
	binding *eebusruntime.OutputBinding
	output  *Output
	script  *feedScript
	cancel  context.CancelFunc
	done    chan struct{}
}

func startFeed(t *testing.T, binding *eebusruntime.OutputBinding, enabled bool, script *feedScript) *runningFeed {
	t.Helper()
	output := New(binding, enabled, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	run := &runningFeed{binding: binding, output: output, script: script, cancel: cancel, done: done}
	go func() {
		defer close(done)
		_ = output.StartOutput(ctx, script, nil)
	}()
	t.Cleanup(run.stop)
	return run
}

func (run *runningFeed) stop() {
	if run == nil || run.cancel == nil {
		return
	}
	run.cancel()
	<-run.done
}

func waitUntil(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition was not reached")
}

func cutRef(id string, completeness graph.Completeness) graph.CutRef {
	return graph.CutRef{CutID: semreg.Digest(id), Completeness: completeness}
}

func lensResource(id, kind, asset string) graph.LensResource {
	return graph.LensResource{
		Resource: semreg.Resource{
			ResourceID: semreg.ResourceID(id),
			Kind:       semreg.DefinitionID(kind),
			State:      semreg.ResourceCurrent,
		},
		AssetID: semreg.AssetID(asset),
	}
}

func qualifiedRelation(definition, from, to, asset string, core graph.CoreType) graph.LensRelation {
	return graph.LensRelation{
		AssetID:    semreg.AssetID(asset),
		Definition: semreg.DefinitionRef{ID: semreg.DefinitionID(definition)},
		From:       semreg.ResourceID(from),
		To:         semreg.ResourceID(to),
		State:      semreg.LinkQualified,
		Coherence:  graph.CoherenceCoherent,
		InPlane:    true,
		CoreType:   core,
	}
}

func containsRelation(from, to, asset string) graph.LensRelation {
	return qualifiedRelation("contains", from, to, asset, graph.CoreContains)
}

func namedRelation(definition, from, to, asset string) graph.LensRelation {
	return qualifiedRelation(definition, from, to, asset, "")
}

func textValue(text string) semreg.Value {
	return semreg.Value{Kind: semreg.ValueText, Text: &text}
}

func quantityValue(coefficient string, exponent int32, unit string) semreg.Value {
	quantity := semreg.Quantity{
		Number: semreg.Decimal{Coefficient: coefficient, Exponent10: exponent},
		Unit:   semreg.DefinitionID(unit),
	}
	return semreg.Value{Kind: semreg.ValueQuantity, Quantity: &quantity}
}

func boolValue(value bool) semreg.Value {
	return semreg.Value{Kind: semreg.ValueBoolean, Boolean: &value}
}

func symbolValue(token string) semreg.Value {
	return semreg.Value{Kind: semreg.ValueSymbol, Symbol: &semreg.Symbol{Token: token}}
}

func promotedQuality() semreg.Quality {
	return semreg.Quality{
		Assertion:     semreg.AssertionObserved,
		Qualification: semreg.QualificationQualified,
		Promotion:     semreg.PromotionPromoted,
		Validity:      semreg.ValidityGood,
		Availability:  semreg.AvailabilityAvailable,
		Freshness:     semreg.FreshnessFresh,
	}
}

func serviceOn(instance, resource string) semreg.ServiceInstance {
	id := semreg.ResourceID(resource)
	return semreg.ServiceInstance{InstanceID: semreg.ServiceInstanceID(instance), Resource: &id}
}

func capabilityOn(instance, service, definition string, qualified bool) semreg.CapabilityInstance {
	qualification := semreg.QualificationCandidate
	if qualified {
		qualification = semreg.QualificationQualified
	}
	return semreg.CapabilityInstance{
		InstanceID:      semreg.CapabilityInstanceID(instance),
		ServiceInstance: semreg.ServiceInstanceID(service),
		Definition:      semreg.DefinitionRef{ID: semreg.DefinitionID(definition)},
		Qualification:   qualification,
		Availability:    semreg.AvailabilityAvailable,
	}
}

func resultByID(results []eebusruntime.LensResult, id string) (eebusruntime.LensResult, bool) {
	for _, result := range results {
		if result.ResourceID == id {
			return result, true
		}
	}
	return eebusruntime.LensResult{}, false
}

func lineByID(reason Reason, id string) (ResourceLine, bool) {
	for _, line := range reason.Resources {
		if line.ResourceID == id {
			return line, true
		}
	}
	return ResourceLine{}, false
}

func indexByID(results []eebusruntime.LensResult, id string) int {
	for index, result := range results {
		if result.ResourceID == id {
			return index
		}
	}
	return -1
}

func hasText(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func countText(values []string, want string) int {
	count := 0
	for _, value := range values {
		if value == want {
			count++
		}
	}
	return count
}

func mustPrepare(t *testing.T, script *feedScript, ref graph.CutRef, enabled bool) preparedCut {
	t.Helper()
	prepared, err := prepareCut(context.Background(), script, ref, enabled)
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func wattSum(coefficient string, exponent int32, unit string) graph.MemberSum {
	return graph.MemberSum{Value: &semreg.Quantity{
		Number: semreg.Decimal{Coefficient: coefficient, Exponent10: exponent},
		Unit:   semreg.DefinitionID(unit),
	}}
}
