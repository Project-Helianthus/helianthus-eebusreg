package driver

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	eebusruntime "github.com/Project-Helianthus/helianthus-eebusreg"
	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
	semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"
	semdriver "github.com/Project-Helianthus/helianthus-semreg/semreg/v1/driver"
	"github.com/Project-Helianthus/helianthus-semreg/semreg/v1/graph"
)

type pairingWorld struct {
	instance *peerInstance
	runtime  *pairingRuntime
	admin    *pairingAdmin
	session  *pairingSession
}

type pairingRuntime struct {
	snapshot eebusruntime.SnapshotV1
	reads    int
}

type pairingAdmin struct {
	revision   uint64
	trusted    []eebusruntime.TrustedPartnerV1
	discovered []eebusruntime.DiscoveredPartnerV1
	candidates []eebusruntime.CandidateV1
	calls      []string
	duration   time.Duration
	pin        []byte
	expected   uint64
}

type pairingSession struct {
	root    semdriver.NodeRef
	methods map[string]semdriver.NativeMethod
	members []semdriver.MemberSpec
	nodes   []semdriver.NodeSpec
	view    semdriver.ViewSpec
	sources []semdriver.Source
	readers map[string]semdriver.NativeReader
}

func newPairingWorld(t *testing.T, key string) *pairingWorld {
	t.Helper()
	created, err := New(semdriver.InstanceKey(key), eebusruntime.Config{
		Enabled: true, StateRoot: filepath.Join(t.TempDir(), "state"), Interface: "test-interface",
		ListenAddress: netip.MustParseAddrPort("192.0.2.10:4711"), PairingPolicy: eebusruntime.PairingPolicyClosed,
	})
	if err != nil {
		t.Fatal(err)
	}
	world := &pairingWorld{
		instance: created.(*peerInstance),
		runtime:  &pairingRuntime{snapshot: eebusruntime.SnapshotV1{}},
		admin:    &pairingAdmin{revision: 7},
		session: &pairingSession{
			root:    semdriver.NodeRef{Bridge: "bridge.eebus", ID: semreg.ResourceID("installations/lab/instances/eebus-inbound/" + key)},
			methods: map[string]semdriver.NativeMethod{},
		},
	}
	world.instance.runtime = world.runtime
	world.instance.admin = world.admin
	if err := world.instance.Start(context.Background(), world.session); err != nil {
		t.Fatal(err)
	}
	return world
}

func (world *pairingWorld) call(t *testing.T, name string, body any) []byte {
	t.Helper()
	method := world.session.methods[name]
	if method == nil {
		t.Fatalf("method %s is not registered", name)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	result, err := method(context.Background(), raw)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return result
}

func (world *pairingWorld) read(t *testing.T, local string) []byte {
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

func (runtime *pairingRuntime) Start(context.Context) error { return nil }
func (runtime *pairingRuntime) Shutdown() error             { return nil }
func (runtime *pairingRuntime) Snapshot() (eebusruntime.SnapshotV1, error) {
	return runtime.snapshot, nil
}
func (runtime *pairingRuntime) PairingState() ([]eebusruntime.PairingObservationV1, error) {
	return runtime.snapshot.Pairing, nil
}
func (runtime *pairingRuntime) FeaturesGet(context.Context, eebusraw.ReadAuthorizationV1, eebusraw.FeaturesGetRequestV1) (eebusraw.FeaturesGetDataV1, *eebusraw.ErrorV1) {
	runtime.reads++
	return eebusraw.FeaturesGetDataV1{}, nil
}
func (runtime *pairingRuntime) FeaturesDataGet(context.Context, eebusraw.ReadAuthorizationV1, eebusraw.FeatureDataGetRequestV1) (eebusraw.FeatureDataGetDataV1, *eebusraw.ErrorV1) {
	runtime.reads++
	return eebusraw.FeatureDataGetDataV1{}, nil
}

func (admin *pairingAdmin) Snapshot(_ context.Context, request eebusruntime.AdminSnapshotRequestV1) (eebusruntime.AdminSnapshotV1, *eebusruntime.AdminErrorV1) {
	snapshot := eebusruntime.AdminSnapshotV1{StateRevision: admin.revision}
	switch request.View {
	case eebusruntime.AdminViewV1Trusted:
		snapshot.Trusted = append([]eebusruntime.TrustedPartnerV1(nil), admin.trusted...)
	case eebusruntime.AdminViewV1Discovered:
		snapshot.Discovered = append([]eebusruntime.DiscoveredPartnerV1(nil), admin.discovered...)
	case eebusruntime.AdminViewV1Candidate:
		snapshot.Candidates = append([]eebusruntime.CandidateV1(nil), admin.candidates...)
	}
	return snapshot, nil
}
func (admin *pairingAdmin) OpenPairingWindow(_ context.Context, request eebusruntime.OpenPairingWindowRequestV1) (eebusruntime.AdminMutationResultV1, *eebusruntime.AdminErrorV1) {
	admin.calls = append(admin.calls, "open_pairing_window")
	admin.duration = request.Duration
	admin.expected = request.ExpectedStateRevision
	return eebusruntime.AdminMutationResultV1{StateRevision: admin.revision, Outcome: "opened"}, nil
}
func (admin *pairingAdmin) ClosePairingWindow(context.Context, eebusruntime.ClosePairingWindowRequestV1) (eebusruntime.AdminMutationResultV1, *eebusruntime.AdminErrorV1) {
	admin.calls = append(admin.calls, "close_pairing_window")
	return eebusruntime.AdminMutationResultV1{StateRevision: admin.revision, Outcome: "closed"}, nil
}
func (admin *pairingAdmin) Select(context.Context, eebusruntime.SelectRequestV1) (eebusruntime.AdminSelectionResultV1, *eebusruntime.AdminErrorV1) {
	admin.calls = append(admin.calls, "select")
	return eebusruntime.AdminSelectionResultV1{AdminMutationResultV1: eebusruntime.AdminMutationResultV1{StateRevision: admin.revision, Outcome: "selected"}}, nil
}
func (admin *pairingAdmin) Connect(_ context.Context, request eebusruntime.ConnectRequestV1) (eebusruntime.ConnectResultV1, *eebusruntime.AdminErrorV1) {
	name := "connect"
	if request.PIN != nil {
		name = "connect_with_pin"
		admin.pin = append([]byte(nil), request.PIN...)
	}
	admin.calls = append(admin.calls, name)
	return eebusruntime.ConnectResultV1{AdminMutationResultV1: eebusruntime.AdminMutationResultV1{StateRevision: admin.revision, Outcome: "connecting"}, ActionID: "action-1"}, nil
}
func (admin *pairingAdmin) Confirm(context.Context, eebusruntime.ConfirmRequestV1) (eebusruntime.AdminMutationResultV1, *eebusruntime.AdminErrorV1) {
	admin.calls = append(admin.calls, "confirm")
	return eebusruntime.AdminMutationResultV1{StateRevision: admin.revision, Outcome: "confirmed"}, nil
}
func (admin *pairingAdmin) Cancel(context.Context, eebusruntime.CancelRequestV1) (eebusruntime.AdminMutationResultV1, *eebusruntime.AdminErrorV1) {
	admin.calls = append(admin.calls, "cancel")
	return eebusruntime.AdminMutationResultV1{StateRevision: admin.revision, Outcome: "cancelled"}, nil
}
func (admin *pairingAdmin) RetryTrusted(context.Context, eebusruntime.RetryTrustedRequestV1) (eebusruntime.AdminMutationResultV1, *eebusruntime.AdminErrorV1) {
	admin.calls = append(admin.calls, "retry_trusted")
	return eebusruntime.AdminMutationResultV1{StateRevision: admin.revision, Outcome: "retrying"}, nil
}
func (admin *pairingAdmin) Untrust(context.Context, eebusruntime.UntrustRequestV1) (eebusruntime.AdminMutationResultV1, *eebusruntime.AdminErrorV1) {
	admin.calls = append(admin.calls, "untrust")
	return eebusruntime.AdminMutationResultV1{StateRevision: admin.revision, Outcome: "untrusted"}, nil
}

func (session *pairingSession) Native() semdriver.NativeRegistry             { return session }
func (session *pairingSession) SemanticInput() semdriver.SemanticInput       { return session }
func (session *pairingSession) ScopedOperations() semdriver.ScopedOperations { return session }
func (session *pairingSession) Calendar() semdriver.Calendar                 { return session }
func (session *pairingSession) Publisher() semdriver.Publisher               { return session }
func (session *pairingSession) Prepare(func(*semreg.PublicationBatch)) (semreg.PublicationBatch, error) {
	return semreg.PublicationBatch{}, nil
}
func (session *pairingSession) RegisterExecutor(semdriver.SettingRoute, semdriver.Executor) (func(), error) {
	return nil, nil
}
func (session *pairingSession) Capture(context.Context, semdriver.Filter) (semdriver.SemanticCut, error) {
	return semdriver.SemanticCut{}, errors.New("not used")
}
func (session *pairingSession) Resume(context.Context, semdriver.Cursor, semdriver.Filter) (semdriver.Changes, error) {
	return semdriver.Changes{}, errors.New("not used")
}
func (session *pairingSession) RetrieveLens(context.Context, graph.CutRef, graph.LensRequest) (graph.LensResult, error) {
	return graph.LensResult{}, errors.New("not used")
}
func (session *pairingSession) SumMembers(context.Context, graph.CutRef, graph.LensRequest, semreg.DefinitionRef, semreg.ResourceID) (graph.MemberSum, error) {
	return graph.MemberSum{}, errors.New("not used")
}
func (session *pairingSession) Invoke(context.Context, semdriver.OperationRequest) (semdriver.OperationResult, error) {
	return semdriver.OperationResult{}, nil
}
func (session *pairingSession) InvokeCallBound(context.Context, semdriver.OperationRequest) (semdriver.OperationResult, error) {
	return semdriver.OperationResult{}, nil
}
func (session *pairingSession) BeginAcquisition() semdriver.CalendarAcquisition { return nil }
func (session *pairingSession) Root() semdriver.NodeRef                         { return session.root }
func (session *pairingSession) Node(spec semdriver.NodeSpec) (semdriver.NodeRef, error) {
	session.nodes = append(session.nodes, spec)
	return semdriver.NodeRef{Bridge: session.root.Bridge, ID: semreg.ResourceID(string(session.root.ID) + "/" + string(spec.Local))}, nil
}
func (session *pairingSession) ReadMember(_ semdriver.NodeRef, spec semdriver.MemberSpec, read semdriver.NativeReader) error {
	session.members = append(session.members, spec)
	session.keepReader(string(spec.Local), read)
	return nil
}
func (session *pairingSession) Method(_ semdriver.NodeRef, spec semdriver.MethodSpec, call semdriver.NativeMethod) error {
	session.methods[string(spec.Local)] = call
	return nil
}
func (session *pairingSession) View(spec semdriver.ViewSpec) error {
	session.view = spec
	return nil
}
func (session *pairingSession) Begin(context.Context) (semdriver.PublicationTransaction, error) {
	return session, nil
}
func (session *pairingSession) OperatorLinks([]semreg.NativeBinding) []semreg.IdentityLink {
	return nil
}
func (session *pairingSession) AdmitManufacturerSerial(semdriver.ManufacturerSerialClaim) error {
	return nil
}
func (session *pairingSession) Stage(semreg.PublicationBatch) error { return nil }
func (session *pairingSession) DeclareSource(source semdriver.Source) error {
	if err := source.Validate(); err != nil {
		return err
	}
	session.sources = append(session.sources, source)
	return nil
}
func (session *pairingSession) DeclareSettingRoute(semdriver.SettingRoute) error { return nil }
func (session *pairingSession) StageSample(semdriver.Sample) error               { return nil }
func (session *pairingSession) StageLink(semdriver.LinkClaim) error              { return nil }
func (session *pairingSession) Commit(context.Context) error                     { return nil }
func (session *pairingSession) Abort(context.Context) error                      { return nil }

func (session *pairingSession) keepReader(local string, read semdriver.NativeReader) {
	if session.readers == nil {
		session.readers = map[string]semdriver.NativeReader{}
	}
	session.readers[local] = read
}

func (session *pairingSession) reader(local string) semdriver.NativeReader {
	return session.readers[local]
}

func TestPairingUnpairedRemoteIsListedAndPublishesAfterPairing(t *testing.T) {
	ski := strings.Repeat("a", 40)
	world := newPairingWorld(t, "heating.east")
	world.runtime.snapshot = remoteSnapshot(ski, eebusruntime.ObservedSessionStateV1Connecting, eebusraw.PairingStateUnpaired, false)
	listed := []pairingRemote(nil)
	if err := json.Unmarshal(world.read(t, "remotes"), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].SKI != ski || listed[0].ShipState != "connecting" || listed[0].PairingState != "unpaired" {
		t.Fatalf("listed = %+v", listed)
	}
	at := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := world.instance.recordPairedValue(ski, semdriver.Element{}, 3, 4, "48.5", at, ""); err != nil {
		t.Fatal(err)
	}
	if len(world.session.sources) != 0 {
		t.Fatalf("unpaired remote declared %#v", world.session.sources)
	}
	world.runtime.snapshot = remoteSnapshot(ski, eebusruntime.ObservedSessionStateV1Connected, eebusraw.PairingStatePaired, true)
	if listedAgain := world.read(t, "remotes"); len(listedAgain) == 0 {
		t.Fatal("paired remote was not listed")
	}
	if len(world.session.sources) != 1 || world.session.sources[0].Priority != 3 || world.session.sources[0].PathKey != "heating.east/"+ski+"/4" {
		t.Fatalf("paired declaration = %#v", world.session.sources)
	}
	if err := world.session.sources[0].Validate(); err != nil {
		t.Fatal(err)
	}
	world.admin.discovered = []eebusruntime.DiscoveredPartnerV1{{SKI: ski}}
	world.admin.trusted = []eebusruntime.TrustedPartnerV1{{SKI: ski}}
	world.admin.candidates = []eebusruntime.CandidateV1{{SKI: ski}}
	world.call(t, "open_pairing_window", map[string]any{"duration_ns": int64(time.Second)})
	world.call(t, "close_pairing_window", map[string]any{})
	world.call(t, "select", map[string]any{"ski": ski})
	world.call(t, "connect", map[string]any{"ski": ski})
	world.call(t, "connect_with_pin", map[string]any{"ski": ski, "pin": "abcdef12"})
	world.call(t, "confirm", map[string]any{"ski": ski})
	world.call(t, "cancel", map[string]any{"ski": ski})
	world.call(t, "retry_trusted", map[string]any{"ski": ski})
	if world.admin.duration != time.Second || world.admin.expected != 7 || string(world.admin.pin) != "abcdef12" {
		t.Fatalf("admin recorded duration %s revision %d pin %q", world.admin.duration, world.admin.expected, world.admin.pin)
	}
	want := []string{"open_pairing_window", "close_pairing_window", "select", "connect", "connect_with_pin", "confirm", "cancel", "retry_trusted"}
	if strings.Join(world.admin.calls, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %v", world.admin.calls)
	}
	if strings.Contains(string(world.call(t, "features_data_get", map[string]any{"targets": []any{}})), "pin") {
		t.Fatal("feature read echoed a pin")
	}
	body, err := world.session.methods["features_data_set"](context.Background(), []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "raw mutation runtime is unavailable") || len(body) != 0 && strings.Contains(string(body), "abcdef12") {
		t.Fatalf("features_data_set = %s, %v", body, err)
	}
	if world.runtime.reads != 1 {
		t.Fatalf("feature reads = %d", world.runtime.reads)
	}
}

func TestPairingDisconnectKeepsStaleSourceAndUntrustWithdrawsOnlyThatRemote(t *testing.T) {
	first := strings.Repeat("a", 40)
	second := strings.Repeat("b", 40)
	world := newPairingWorld(t, "heating.east")
	world.runtime.snapshot = eebusruntime.SnapshotV1{
		Services: []eebusruntime.ServiceV1{{SKI: first, Paired: true}, {SKI: second, Paired: true}},
		Sessions: []eebusruntime.SessionV1{
			{RemoteSKI: first, State: eebusruntime.ObservedSessionStateV1Connected},
			{RemoteSKI: second, State: eebusruntime.ObservedSessionStateV1Connected},
		},
		Pairing: []eebusruntime.PairingObservationV1{
			{RemoteSKI: first, State: eebusraw.PairingStatePaired},
			{RemoteSKI: second, State: eebusraw.PairingStatePaired},
		},
	}
	at := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := world.instance.recordPairedValue(first, semdriver.Element{}, 3, 4, "48.5", at, ""); err != nil {
		t.Fatal(err)
	}
	later := at.Add(time.Minute)
	if err := world.instance.recordPairedValue(second, semdriver.Element{}, 3, 8, "12.0", later, ""); err != nil {
		t.Fatal(err)
	}
	if len(world.session.sources) != 2 {
		t.Fatalf("declarations = %d", len(world.session.sources))
	}
	world.instance.onTransport("disconnected", first)
	stale, ok := world.instance.sourceForSKI(first)
	if !ok || stale.value != "48.5" || !stale.at.Equal(at) || stale.availability != "stale" || stale.priority != 3 || stale.generation != 4 || stale.path != "heating.east/"+first+"/4" {
		t.Fatalf("after disconnect = %+v ok=%v", stale, ok)
	}
	if len(world.session.sources) != 2 {
		t.Fatal("disconnect declared another source")
	}
	world.instance.onTransport("connected", first)
	restored, _ := world.instance.sourceForSKI(first)
	if restored.availability != "available" || restored.value != "48.5" || !restored.at.Equal(at) || restored.priority != 3 || restored.generation != 4 || restored.path != "heating.east/"+first+"/4" || len(world.session.sources) != 2 {
		t.Fatalf("after reconnect = %+v declarations %d", restored, len(world.session.sources))
	}
	world.admin.trusted = []eebusruntime.TrustedPartnerV1{{SKI: first}, {SKI: second}}
	world.call(t, "untrust", map[string]any{"ski": first})
	if strings.Join(world.admin.calls, ",") != "untrust" {
		t.Fatalf("calls = %v", world.admin.calls)
	}
	withdrawn, _ := world.instance.authoritative(world.instance.defaultElement(first))
	if withdrawn.ski != "" {
		t.Fatalf("withdrawn remote is still authoritative: %+v", withdrawn)
	}
	kept, ok := world.instance.sourceForSKI(second)
	if !ok || kept.value != "12.0" || kept.availability != "available" || kept.ski != second {
		t.Fatalf("other remote = %+v ok=%v", kept, ok)
	}
}

func TestPairingGenerationStopKeepsTheHigherSourceUntilRevocation(t *testing.T) {
	highSKI := strings.Repeat("c", 40)
	lowSKI := strings.Repeat("d", 40)
	world := newPairingWorld(t, "heating.east")
	world.runtime.snapshot = eebusruntime.SnapshotV1{
		Services: []eebusruntime.ServiceV1{{SKI: highSKI, Paired: true}, {SKI: lowSKI, Paired: true}},
		Pairing: []eebusruntime.PairingObservationV1{
			{RemoteSKI: highSKI, State: eebusraw.PairingStatePaired},
			{RemoteSKI: lowSKI, State: eebusraw.PairingStatePaired},
		},
	}
	element := sharedElement()
	highAt := time.Date(2030, 2, 3, 4, 5, 6, 0, time.UTC)
	lowAt := highAt.Add(time.Second)
	if err := world.instance.recordPairedValue(highSKI, element, 3, 9, "producer", highAt, "heating.east/producer/9"); err != nil {
		t.Fatal(err)
	}
	if err := world.instance.recordPairedValue(lowSKI, element, 1, 9, "tertiary", lowAt, "heating.east/tertiary/9"); err != nil {
		t.Fatal(err)
	}
	world.instance.stopGeneration(highSKI, 9)
	winner, ok := world.instance.authoritative(element)
	if !ok || winner.priority != 3 || winner.value != "producer" || !winner.at.Equal(highAt) || winner.availability != "stale" || winner.path != "heating.east/producer/9" {
		t.Fatalf("stale winner = %+v ok=%v", winner, ok)
	}
	if err := world.instance.recordPairedValue(highSKI, element, 3, 9, "replaced", highAt.Add(time.Hour), "heating.east/producer/9"); err != nil {
		t.Fatal(err)
	}
	winner, _ = world.instance.authoritative(element)
	if winner.value != "producer" || winner.priority != 3 || winner.path == "heating.east/tertiary/9" {
		t.Fatalf("stopped generation changed winner to %+v", winner)
	}
	low, _ := world.instance.sourceForSKI(lowSKI)
	if low.value != "tertiary" || low.priority != 1 || low.availability != "available" {
		t.Fatalf("lower source = %+v", low)
	}
	world.admin.trusted = []eebusruntime.TrustedPartnerV1{{SKI: highSKI}}
	world.call(t, "untrust", map[string]any{"ski": highSKI})
	winner, ok = world.instance.authoritative(element)
	if !ok || winner.ski != lowSKI || winner.priority != 1 || winner.value != "tertiary" {
		t.Fatalf("after revocation winner = %+v ok=%v", winner, ok)
	}
	if views := world.instance.sourceViews(); len(views) != 2 || views[0].Availability != "withdrawn" && views[1].Availability != "withdrawn" {
		t.Fatalf("views = %+v", views)
	}
}

func TestPairingTwoInstancesPublishDistinctNamespaceMembers(t *testing.T) {
	ski := strings.Repeat("e", 40)
	east := newPairingWorld(t, "heating.east")
	west := newPairingWorld(t, "heating.west")
	if east.instance.runtime == west.instance.runtime || east.instance.admin == west.instance.admin {
		t.Fatal("instances share a runtime or admin")
	}
	east.runtime.snapshot = remoteSnapshot(ski, eebusruntime.ObservedSessionStateV1Connected, eebusraw.PairingStatePaired, true)
	west.runtime.snapshot = remoteSnapshot(ski, eebusruntime.ObservedSessionStateV1Degraded, eebusraw.PairingStatePaired, true)
	at := time.Date(2030, 3, 4, 5, 6, 7, 0, time.UTC)
	if err := east.instance.recordPairedValue(ski, semdriver.Element{}, 3, 1, "east-value", at, ""); err != nil {
		t.Fatal(err)
	}
	if err := west.instance.recordPairedValue(ski, semdriver.Element{}, 1, 2, "west-value", at.Add(time.Second), ""); err != nil {
		t.Fatal(err)
	}
	if len(east.session.sources) != 1 || len(west.session.sources) != 1 {
		t.Fatalf("declarations east %d west %d", len(east.session.sources), len(west.session.sources))
	}
	if east.session.sources[0].PathKey == west.session.sources[0].PathKey || east.session.sources[0].Priority == west.session.sources[0].Priority {
		t.Fatalf("sources were not distinct: %#v %#v", east.session.sources[0], west.session.sources[0])
	}
	if len(east.session.view.Records) != 1 || len(west.session.view.Records) != 1 {
		t.Fatalf("views east %+v west %+v", east.session.view, west.session.view)
	}
	eastFields := east.session.view.Records[0].Fields
	westFields := west.session.view.Records[0].Fields
	if len(eastFields) != 2 || len(westFields) != 2 {
		t.Fatalf("fields east %+v west %+v", eastFields, westFields)
	}
	if eastFields[0].Member.Instance == westFields[0].Member.Instance {
		t.Fatal("namespace member instance key was shared")
	}
	if eastFields[0].Member.Namespace != semdriver.NamespaceID(Kind) || eastFields[0].Member.Local != "remotes" {
		t.Fatalf("east member = %+v", eastFields[0].Member)
	}
	if eastFields[1].Member.Local != "sources" || eastFields[0].Member.Node == westFields[0].Member.Node {
		t.Fatalf("view fields east %+v west %+v", eastFields, westFields)
	}
	eastRows := []pairingRemote(nil)
	westRows := []pairingRemote(nil)
	if err := json.Unmarshal(east.read(t, "remotes"), &eastRows); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(west.read(t, "remotes"), &westRows); err != nil {
		t.Fatal(err)
	}
	if eastRows[0].ShipState != "connected" || westRows[0].ShipState != "degraded" || eastRows[0].SKI != ski || westRows[0].SKI != ski {
		t.Fatalf("rows east %+v west %+v", eastRows, westRows)
	}
	east.runtime.snapshot.Sessions[0].State = eebusruntime.ObservedSessionStateV1Disconnected
	if err := json.Unmarshal(east.read(t, "remotes"), &eastRows); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(west.read(t, "remotes"), &westRows); err != nil {
		t.Fatal(err)
	}
	if eastRows[0].ShipState != "disconnected" || westRows[0].ShipState != "degraded" {
		t.Fatalf("ship state leaked: east %+v west %+v", eastRows, westRows)
	}
}

func TestPairingIdentityFieldsChangeIndependently(t *testing.T) {
	base := pairingMemberKey("eebus-inbound", "heating.east", "node.east", "remotes")
	namespace := pairingMemberKey("eebus-other", "heating.east", "node.east", "remotes")
	instance := pairingMemberKey("eebus-inbound", "heating.west", "node.east", "remotes")
	node := pairingMemberKey("eebus-inbound", "heating.east", "node.west", "remotes")
	local := pairingMemberKey("eebus-inbound", "heating.east", "node.east", "sources")
	if namespace.Namespace == base.Namespace || namespace.Instance != base.Instance || namespace.Node != base.Node || namespace.Local != base.Local {
		t.Fatalf("namespace mutation = %+v", namespace)
	}
	if instance.Instance == base.Instance || instance.Namespace != base.Namespace || instance.Node != base.Node || instance.Local != base.Local {
		t.Fatalf("instance mutation = %+v", instance)
	}
	if node.Node == base.Node || node.Namespace != base.Namespace || node.Instance != base.Instance || node.Local != base.Local {
		t.Fatalf("node mutation = %+v", node)
	}
	if local.Local == base.Local || local.Namespace != base.Namespace || local.Instance != base.Instance || local.Node != base.Node {
		t.Fatalf("local mutation = %+v", local)
	}
	ski := strings.Repeat("f", 40)
	other := strings.Repeat("1", 40)
	baseRow := rowsFromSnapshot(remoteSnapshot(ski, eebusruntime.ObservedSessionStateV1Connecting, eebusraw.PairingStateUnpaired, false))
	otherSKI := rowsFromSnapshot(remoteSnapshot(other, eebusruntime.ObservedSessionStateV1Connecting, eebusraw.PairingStateUnpaired, false))
	otherShip := rowsFromSnapshot(remoteSnapshot(ski, eebusruntime.ObservedSessionStateV1Degraded, eebusraw.PairingStateUnpaired, false))
	otherPairing := rowsFromSnapshot(remoteSnapshot(ski, eebusruntime.ObservedSessionStateV1Connecting, eebusraw.PairingStatePaired, true))
	if otherSKI[0].SKI == baseRow[0].SKI || otherSKI[0].ShipState != baseRow[0].ShipState || otherSKI[0].PairingState != baseRow[0].PairingState {
		t.Fatalf("ski mutation = %+v", otherSKI)
	}
	if otherShip[0].ShipState == baseRow[0].ShipState || otherShip[0].SKI != baseRow[0].SKI || otherShip[0].PairingState != baseRow[0].PairingState {
		t.Fatalf("ship mutation = %+v", otherShip)
	}
	if otherPairing[0].PairingState == baseRow[0].PairingState || otherPairing[0].SKI != baseRow[0].SKI || otherPairing[0].ShipState != baseRow[0].ShipState {
		t.Fatalf("pairing mutation = %+v", otherPairing)
	}
	world := newPairingWorld(t, "heating.east")
	world.runtime.snapshot = remoteSnapshot(ski, eebusruntime.ObservedSessionStateV1Connected, eebusraw.PairingStatePaired, true)
	element := sharedElement()
	at := time.Date(2030, 4, 5, 6, 7, 8, 0, time.UTC)
	if err := world.instance.recordPairedValue(ski, element, 3, 4, "first", at, "heating.east/path/4"); err != nil {
		t.Fatal(err)
	}
	if err := world.instance.recordPairedValue(ski, element, 2, 4, "first", at, "heating.east/path/4"); err != nil {
		t.Fatal(err)
	}
	priority, _ := world.instance.sourceForSKI(ski)
	if priority.priority != 2 || priority.path != "heating.east/path/4" || priority.generation != 4 || priority.value != "first" || !priority.at.Equal(at) {
		t.Fatalf("priority mutation = %+v", priority)
	}
	if err := world.instance.recordPairedValue(ski, element, 2, 4, "first", at, "heating.east/other/4"); err != nil {
		t.Fatal(err)
	}
	if len(world.instance.pairing.records) != 2 {
		t.Fatalf("path mutation did not add a source: %+v", world.instance.pairing.records)
	}
	if err := world.instance.recordPairedValue(ski, element, 2, 5, "second", at.Add(time.Second), "heating.east/path/4"); err != nil {
		t.Fatal(err)
	}
	changed, _ := world.instance.sourceForSKI(ski)
	if changed.generation != 5 || changed.value != "second" || !changed.at.Equal(at.Add(time.Second)) || changed.priority != 2 {
		t.Fatalf("generation value time mutation = %+v", changed)
	}
	mutations := []semdriver.Element{element, element, element, element, element, element}
	mutations[0].Subject.Bridge = "bridge.other"
	mutations[1].Subject.ID = "resource.other.element"
	mutations[2].Key.PackID = "pack.eebus-other"
	mutations[3].Key.PackVersion = "1.0.1"
	mutations[4].Key.FactID = "fact.remote-other"
	flag := true
	mutations[5].Key.Dimensions = []semreg.Dimension{{ID: "dimension.phase", Value: semreg.Value{Kind: semreg.ValueBoolean, Boolean: &flag}}}
	paths := []string{
		"heating.east/bridge/4", "heating.east/subject/4", "heating.east/pack/4",
		"heating.east/version/4", "heating.east/fact/4", "heating.east/dimension/4",
	}
	values := []string{"bridge", "subject", "pack", "version", "fact", "dimension"}
	for index := range mutations {
		if err := world.instance.recordPairedValue(ski, mutations[index], 9, 4, values[index], at, paths[index]); err != nil {
			t.Fatal(err)
		}
		baseWinner, _ := world.instance.authoritative(element)
		mutatedWinner, ok := world.instance.authoritative(mutations[index])
		if baseWinner.path != "heating.east/path/4" || baseWinner.priority != 2 || baseWinner.value != "second" || !ok || mutatedWinner.path != paths[index] || mutatedWinner.priority != 9 || mutatedWinner.value != values[index] {
			t.Fatalf("element field %d base %+v mutated %+v", index, baseWinner, mutatedWinner)
		}
	}
	if err := world.instance.recordPairedValue(strings.ToUpper(ski), element, 2, 6, "case", at, "heating.east/case/6"); err != nil {
		t.Fatal(err)
	}
	stored := pairingSource{}
	for _, record := range world.instance.pairing.records {
		if record.path == "heating.east/case/6" {
			stored = record
		}
	}
	if stored.ski != ski {
		t.Fatalf("ski case stored %s", stored.ski)
	}
	if _, ok := world.instance.sourceForSKI(strings.ToUpper(ski)); !ok {
		t.Fatal("uppercase lookup missed the normalized ski")
	}
}

func remoteSnapshot(ski string, ship eebusruntime.ObservedSessionStateV1, pairing eebusraw.PairingState, paired bool) eebusruntime.SnapshotV1 {
	return eebusruntime.SnapshotV1{
		Services: []eebusruntime.ServiceV1{{SKI: ski, Visible: true, Paired: paired}},
		Sessions: []eebusruntime.SessionV1{{RemoteSKI: ski, State: ship}},
		Pairing:  []eebusruntime.PairingObservationV1{{RemoteSKI: ski, State: pairing}},
	}
}

func sharedElement() semdriver.Element {
	return semdriver.Element{
		Subject: semdriver.NodeRef{Bridge: "bridge.eebus", ID: "resource.shared.element"},
		Key: semreg.FactKey{
			PackID: "pack.eebus-inbound", PackVersion: "1.0.0", FactID: "fact.remote-observation", Dimensions: []semreg.Dimension{},
		},
	}
}
