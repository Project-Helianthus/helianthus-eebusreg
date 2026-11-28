package inbound

import (
	"context"
	"errors"

	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusservicebridge"
	semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"
	"github.com/Project-Helianthus/helianthus-semreg/semreg/v1/driver"
)

type host struct {
	eebusservicebridge.PairingSessionSurface
	root       driver.NodeRef
	nodes      []driver.NodeSpec
	members    []driver.MemberSpec
	readers    map[string]driver.NativeReader
	nodeLocals map[string]struct{}
	memberKeys map[string]struct{}
	batches    []semreg.PublicationBatch
	sources    []driver.Source
	samples    []driver.Sample
	links      []driver.LinkClaim
	serials    []driver.ManufacturerSerialClaim
	matches    []driver.MatchClaim
	routes     []driver.SettingRoute
	executors  []driver.Executor
	methodKeys map[string]struct{}
	methods    []driver.MethodSpec
	methodFns  map[string]driver.NativeMethod
}

func newHost() *host {
	session := &host{
		root: driver.NodeRef{Bridge: "bridge.heating", ID: "resource.host"}, readers: map[string]driver.NativeReader{},
		nodeLocals: map[string]struct{}{}, memberKeys: map[string]struct{}{},
	}
	session.PairingSessionSurface = eebusservicebridge.NewPairingSessionSurface(session)
	return session
}

var (
	_ driver.Session                = (*host)(nil)
	_ driver.NativeRegistry         = (*host)(nil)
	_ driver.Publisher              = (*host)(nil)
	_ driver.PublicationTransaction = (*host)(nil)
	_ driver.ScopedOperations       = (*host)(nil)
	_ driver.Calendar               = (*host)(nil)
)

func (h *host) ScopedOperations() driver.ScopedOperations { return h }
func (h *host) Calendar() driver.Calendar                 { return h }
func (h *host) Publisher() driver.Publisher               { return h }
func (h *host) RegisterExecutor(_ driver.SettingRoute, exec driver.Executor) (func(), error) {
	h.executors = append(h.executors, exec)
	return func() {}, nil
}
func (h *host) Invoke(context.Context, driver.OperationRequest) (driver.OperationResult, error) {
	return driver.OperationResult{}, nil
}
func (h *host) InvokeCallBound(context.Context, driver.OperationRequest) (driver.OperationResult, error) {
	return driver.OperationResult{}, nil
}
func (h *host) BeginAcquisition() driver.CalendarAcquisition { return nil }
func (h *host) Root() driver.NodeRef                         { return h.root }
func (h *host) Node(spec driver.NodeSpec) (driver.NodeRef, error) {
	if err := spec.Validate(); err != nil {
		return driver.NodeRef{}, err
	}
	local := string(spec.Local)
	if _, ok := h.nodeLocals[local]; ok {
		return driver.NodeRef{}, errors.New("key is already registered")
	}
	h.nodeLocals[local] = struct{}{}
	h.nodes = append(h.nodes, spec)
	return driver.NodeRef{Bridge: h.root.Bridge, ID: semreg.ResourceID("node." + local)}, nil
}
func (h *host) ReadMember(ref driver.NodeRef, spec driver.MemberSpec, read driver.NativeReader) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	key := string(ref.ID) + "\n" + string(spec.Local)
	if _, ok := h.memberKeys[key]; ok {
		return errors.New("key is already registered")
	}
	h.memberKeys[key] = struct{}{}
	h.members = append(h.members, spec)
	h.readers[key] = read
	return nil
}
func (h *host) Method(ref driver.NodeRef, spec driver.MethodSpec, method driver.NativeMethod) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	if h.methodKeys == nil {
		h.methodKeys = map[string]struct{}{}
		h.methodFns = map[string]driver.NativeMethod{}
	}
	key := string(ref.ID) + "\n" + string(spec.Local)
	if _, ok := h.methodKeys[key]; ok {
		return errors.New("key is already registered")
	}
	h.methodKeys[key] = struct{}{}
	h.methods = append(h.methods, spec)
	h.methodFns[key] = method
	return nil
}
func (h *host) View(driver.ViewSpec) error                                   { return nil }
func (h *host) Begin(context.Context) (driver.PublicationTransaction, error) { return h, nil }
func (h *host) OperatorLinks([]semreg.NativeBinding) []semreg.IdentityLink   { return nil }
func (h *host) AdmitManufacturerSerial(claim driver.ManufacturerSerialClaim) error {
	if err := claim.Validate(); err != nil {
		return err
	}
	h.serials = append(h.serials, claim)
	return nil
}
func (h *host) AdmitMatch(claim driver.MatchClaim) error {
	if err := claim.Validate(); err != nil {
		return err
	}
	h.matches = append(h.matches, claim)
	return nil
}
func (h *host) Stage(batch semreg.PublicationBatch) error {
	if err := batch.Validate(); err != nil {
		return err
	}
	h.batches = append(h.batches, batch)
	return nil
}
func (h *host) DeclareSource(source driver.Source) error {
	if err := source.Validate(); err != nil {
		return err
	}
	h.sources = append(h.sources, source)
	return nil
}
func (h *host) DeclareSettingRoute(route driver.SettingRoute) error {
	if err := route.Validate(); err != nil {
		return err
	}
	h.routes = append(h.routes, route)
	return nil
}
func (h *host) StageSample(sample driver.Sample) error {
	if err := sample.Validate(); err != nil {
		return err
	}
	h.samples = append(h.samples, sample)
	return nil
}
func (h *host) StageLink(claim driver.LinkClaim) error {
	if err := claim.Validate(); err != nil {
		return err
	}
	h.links = append(h.links, claim)
	return nil
}
func (h *host) Commit(context.Context) error { return nil }
func (h *host) Abort(context.Context) error  { return nil }

func (h *host) Prepare(mutate func(*semreg.PublicationBatch)) (semreg.PublicationBatch, error) {
	batch := semreg.PublicationBatch{
		Contract: semreg.ContractKernelV1, BatchID: "batch.heating", AssetID: "asset.heating",
		SourceID: "source.heating", SourceEpochID: "epoch.heating", DriverGeneration: "1", Sequence: "1",
		ExpectedSemanticRevision: "0",
		ObservedAt:               semreg.TimePoint{UnixNanoseconds: "0", ClockID: "clock.utc", UncertaintyNS: "0"},
		SourceUpserts:            []semreg.SourceDescriptor{}, SourceRetirements: []semreg.SourceEpochID{},
		BindingUpserts: []semreg.NativeBinding{}, IdentityLinkUpserts: []semreg.IdentityLink{},
		FactUpserts: []semreg.FactCandidate{}, FactWithdrawals: []semreg.CandidateID{},
		ServiceUpserts: []semreg.ServiceInstance{}, ServiceWithdrawals: []semreg.ServiceInstanceID{},
		CapabilityUpserts: []semreg.CapabilityInstance{}, CapabilityWithdrawals: []semreg.CapabilityInstanceID{},
		GenerationFences: []semreg.GenerationFence{},
	}
	if mutate != nil {
		mutate(&batch)
	}
	sum, err := batch.ComputedDigest()
	if err != nil {
		return semreg.PublicationBatch{}, err
	}
	batch.BatchDigest = sum
	return batch, nil
}
