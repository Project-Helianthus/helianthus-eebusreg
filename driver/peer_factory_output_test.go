package driver

import (
	"context"
	"encoding/json"
	"net/netip"
	"path/filepath"
	"sync"
	"testing"
	"time"

	eebusruntime "github.com/Project-Helianthus/helianthus-eebusreg"
	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusfacade"
	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusservicebridge"
	semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"
	semdriver "github.com/Project-Helianthus/helianthus-semreg/semreg/v1/driver"
	"github.com/Project-Helianthus/helianthus-semreg/semreg/v1/graph"
)

const (
	outputNodeTokenA = "0123456789abcdef0123456789abcdef"
	outputNodeTokenB = "fedcba9876543210fedcba9876543210"
)

// TestPeerFactoryStartsOutput starts output only through Instance.Start.
// Stopping one instance leaves the other, and its inbound read, running.
func TestPeerFactoryStartsOutput(t *testing.T) {
	eastFallbacks := []eebusruntime.OutputPowerLimitFallback{
		{Resource: "resource.east.heater", Direction: eebusruntime.PowerLimitDirectionConsumption, Power: watt("15", 2)},
		{Resource: "resource.east.meter", Direction: eebusruntime.PowerLimitDirectionProduction, Power: watt("5", -1)},
	}
	westFallbacks := []eebusruntime.OutputPowerLimitFallback{
		{Resource: "resource.west.heater", Direction: eebusruntime.PowerLimitDirectionConsumption, Power: watt("5", 0)},
	}
	eastScript := newOutputScript("cut-east", "inv-east")
	westScript := newOutputScript("cut-west", "inv-west")
	east := startFactoryOutput(t, "output.east", "test-interface-east", "192.0.2.10:4711", 4811, outputNodeTokenA, eastFallbacks, eastScript)
	west := startFactoryOutput(t, "output.west", "test-interface-west", "192.0.2.11:4712", 4812, outputNodeTokenB, westFallbacks, westScript)
	if east.instance == west.instance || east.instance.binding == west.instance.binding || east.instance.config.StateRoot == west.instance.config.StateRoot {
		t.Fatal("two instances share a runtime, binding or state root")
	}

	eastAddress := waitFactoryAddress(t, east.instance.binding, "inv-east")
	westAddress := waitFactoryAddress(t, west.instance.binding, "inv-west")
	if eastAddress == "" || westAddress == "" {
		t.Fatal("ordinary startup did not publish")
	}
	assertFallbackCopy(t, east.instance)
	if west.instance.binding.PowerLimitFallbacks()[0].Resource != "resource.west.heater" {
		t.Fatal("west took the east fallback")
	}
	if east.instance.binding.RetainedOperations() != east.session {
		t.Fatal("binding did not keep this session's operation port")
	}
	if err := json.Unmarshal(east.read(t, "remotes"), &[]map[string]any{}); err != nil {
		t.Fatal(err)
	}

	eastReads := eastScript.readsCount()
	east.instance.Stop(context.Background())
	eastScript.push(outputCut("cut-east-later", "inv-east-later"))
	if eastScript.readsCount() != eastReads || east.binding.EntityAddress("inv-east-later") != "" || east.binding.EntityAddress("inv-east") != eastAddress {
		t.Fatalf("stopped binding accepted a later cut, reads %d address %q", eastScript.readsCount(), east.binding.EntityAddress("inv-east-later"))
	}

	westScript.push(outputCut("cut-west-later", "inv-west", "inv-later"))
	later := waitFactoryAddress(t, west.instance.binding, "inv-later")
	if later == westAddress || west.instance.binding.EntityAddress("inv-west") != westAddress {
		t.Fatal("second instance lost its first resource or reused its address")
	}
	if err := json.Unmarshal(west.read(t, "remotes"), &[]map[string]any{}); err != nil {
		t.Fatal(err)
	}

	restartScript := newOutputScript("cut-east-restart", "inv-east")
	restart := east.restart(t, restartScript)
	if restart.binding.RetainedOperations() != restart.session || restart.binding.RetainedOperations() == east.session {
		t.Fatal("restarted binding kept the old operation port")
	}
	if waitFactoryAddress(t, restart.binding, "inv-east") != eastAddress {
		t.Fatal("restarted instance moved the stored address")
	}
	oldReads := eastScript.readsCount()
	eastScript.push(outputCut("cut-east-stale", "inv-stale"))
	if eastScript.readsCount() != oldReads || restart.binding.EntityAddress("inv-stale") != "" {
		t.Fatal("old session still reached the restarted binding")
	}
	if west.instance.binding.EntityAddress("inv-later") != later {
		t.Fatal("restart disturbed the other instance")
	}
}

type factoryOutput struct {
	instance *peerInstance
	session  *pairingSession
	binding  *eebusruntime.OutputBinding
}

func startFactoryOutput(t *testing.T, key, iface, address string, port int, token string, fallbacks []eebusruntime.OutputPowerLimitFallback, script *outputScript) factoryOutput {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	created, err := New(semdriver.InstanceKey(key), runtimeConfig(root, iface, address, fallbacks))
	if err != nil {
		t.Fatal(err)
	}
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
	session := &pairingSession{
		root:    semdriver.NodeRef{Bridge: "bridge.eebus", ID: semreg.ResourceID("installations/lab/instances/eebus-inbound/" + key)},
		methods: map[string]semdriver.NativeMethod{},
	}
	session.PairingSessionSurface = eebusservicebridge.NewPairingSessionSurface(session).WithInput(script)
	instance := created.(*peerInstance)
	instance.runtime = &pairingRuntime{node: node}
	instance.admin = &pairingAdmin{}
	if err := instance.Start(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { instance.Stop(context.Background()) })
	if instance.binding == nil || instance.output == nil {
		t.Fatal("start did not attach output")
	}
	return factoryOutput{instance: instance, session: session, binding: instance.binding}
}

func (world factoryOutput) restart(t *testing.T, script *outputScript) factoryOutput {
	t.Helper()
	session := &pairingSession{
		root:    world.session.root,
		methods: map[string]semdriver.NativeMethod{},
	}
	session.PairingSessionSurface = eebusservicebridge.NewPairingSessionSurface(session).WithInput(script)
	if err := world.instance.Start(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if world.instance.binding == nil {
		t.Fatal("restart did not attach output")
	}
	return factoryOutput{instance: world.instance, session: session, binding: world.instance.binding}
}

func (world factoryOutput) read(t *testing.T, local string) []byte {
	t.Helper()
	reader := world.session.reader(local)
	if reader == nil {
		t.Fatalf("member %s is not registered", local)
	}
	body, err := reader(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func assertFallbackCopy(t *testing.T, instance *peerInstance) {
	t.Helper()
	stored := instance.binding.PowerLimitFallbacks()
	if len(stored) != 2 || stored[0].Resource != "resource.east.heater" || stored[0].Direction != eebusruntime.PowerLimitDirectionConsumption ||
		stored[0].Power.Number.Coefficient != "15" || stored[0].Power.Number.Exponent10 != 2 || stored[0].Power.Unit != "unit.watt" ||
		stored[1].Resource != "resource.east.meter" || stored[1].Direction != eebusruntime.PowerLimitDirectionProduction ||
		stored[1].Power.Number.Coefficient != "5" || stored[1].Power.Number.Exponent10 != -1 || stored[1].Power.Unit != "unit.watt" {
		t.Fatalf("fallbacks %+v", stored)
	}
	// Resource, direction, coefficient, exponent and unit are copied. Mutating the caller changes none of them.
	instance.config.OutputPowerLimitFallbacks[0].Resource = "resource.replaced"
	instance.config.OutputPowerLimitFallbacks[0].Direction = eebusruntime.PowerLimitDirectionProduction
	instance.config.OutputPowerLimitFallbacks[0].Power.Number.Coefficient = "9"
	instance.config.OutputPowerLimitFallbacks[0].Power.Number.Exponent10 = 0
	instance.config.OutputPowerLimitFallbacks[0].Power.Unit = "unit.volt"
	stored[0].Resource = "resource.returned"
	stored[0].Direction = eebusruntime.PowerLimitDirectionProduction
	stored[0].Power.Number.Coefficient = "3"
	stored[0].Power.Number.Exponent10 = 4
	stored[0].Power.Unit = "unit.volt"
	again := instance.binding.PowerLimitFallbacks()
	if again[0].Resource != "resource.east.heater" || again[0].Direction != eebusruntime.PowerLimitDirectionConsumption ||
		again[0].Power.Number.Coefficient != "15" || again[0].Power.Number.Exponent10 != 2 || again[0].Power.Unit != "unit.watt" {
		t.Fatalf("stored fallback changed %+v", again[0])
	}
}

func waitFactoryAddress(t *testing.T, binding *eebusruntime.OutputBinding, id string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if address := binding.EntityAddress(id); address != "" {
			return address
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("address for %s was not stored", id)
	return ""
}

type outputScript struct {
	mu      sync.Mutex
	notify  chan struct{}
	current semdriver.SemanticCut
	pending []semdriver.SemanticCut
	pages   map[string]graph.LensResult
	reads   int
}

func newOutputScript(id, resource string) *outputScript {
	cut, page := outputCut(id, resource)
	return &outputScript{
		notify:  make(chan struct{}, 1),
		current: cut,
		pages:   map[string]graph.LensResult{string(cut.Ref.CutID): page},
	}
}

// scriptedCut is one committed cut. The exported method signature names this alias.
type scriptedCut = semdriver.SemanticCut

func outputCut(id string, resources ...string) (scriptedCut, graph.LensResult) {
	ref := graph.CutRef{CutID: semreg.Digest(id), Completeness: graph.CutComplete}
	page := graph.LensResult{Cut: ref}
	for _, resource := range resources {
		page.Resources = append(page.Resources, graph.LensResource{
			Resource: semreg.Resource{ResourceID: semreg.ResourceID(resource), Kind: "resource.inverter", State: semreg.ResourceCurrent},
			AssetID:  semreg.AssetID("asset-" + resource),
		})
	}
	return semdriver.SemanticCut{Ref: ref}, page
}

func (script *outputScript) push(cut semdriver.SemanticCut, page graph.LensResult) {
	script.mu.Lock()
	script.pages[string(cut.Ref.CutID)] = page
	script.pending = append(script.pending, cut)
	script.mu.Unlock()
	select {
	case script.notify <- struct{}{}:
	default:
	}
}

func (script *outputScript) readsCount() int {
	script.mu.Lock()
	defer script.mu.Unlock()
	return script.reads
}

func (script *outputScript) Capture(ctx context.Context, _ semdriver.Filter) (scriptedCut, error) {
	if err := ctx.Err(); err != nil {
		return semdriver.SemanticCut{}, err
	}
	script.mu.Lock()
	defer script.mu.Unlock()
	return script.current, nil
}

func (script *outputScript) Resume(ctx context.Context, _ semdriver.Cursor, _ semdriver.Filter) (semdriver.Changes, error) {
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

func (script *outputScript) RetrieveLens(_ context.Context, ref graph.CutRef, _ graph.LensRequest) (graph.LensResult, error) {
	script.mu.Lock()
	defer script.mu.Unlock()
	script.reads++
	page, ok := script.pages[string(ref.CutID)]
	if !ok {
		return graph.LensResult{Cut: ref}, nil
	}
	return page, nil
}

func (script *outputScript) SumMembers(context.Context, graph.CutRef, graph.LensRequest, semreg.DefinitionRef, semreg.ResourceID) (graph.MemberSum, error) {
	return graph.MemberSum{}, nil
}
