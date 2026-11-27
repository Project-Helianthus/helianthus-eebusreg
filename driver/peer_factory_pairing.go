package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	eebusruntime "github.com/Project-Helianthus/helianthus-eebusreg"
	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusfacade"
	semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"
	semdriver "github.com/Project-Helianthus/helianthus-semreg/semreg/v1/driver"
)

type pairingState struct {
	mu         sync.Mutex
	records    []pairingSource
	selections map[string]eebusruntime.SelectionHandleV1
	nodes      map[string]semdriver.NodeRef
	stopped    map[string]struct{}
	seq        uint64
}

const producerPriority int32 = 3

type pairingSource struct {
	ski          string
	element      semdriver.Element
	path         string
	priority     int32
	generation   uint64
	value        string
	at           time.Time
	availability string
	stopped      bool
	declared     bool
}

type pairingRemote struct {
	SKI          string `json:"ski"`
	ShipState    string `json:"ship_state"`
	PairingState string `json:"pairing_state"`
}

type pairingSourceView struct {
	SKI           string `json:"ski"`
	Path          string `json:"path"`
	Priority      int32  `json:"priority"`
	Generation    uint64 `json:"generation"`
	Value         string `json:"value"`
	ObservedAt    string `json:"observed_at,omitempty"`
	Availability  string `json:"availability"`
	Stopped       bool   `json:"stopped"`
	Declared      bool   `json:"declared"`
	Authoritative bool   `json:"authoritative"`
}

type pairingArgs struct {
	SKI                   string `json:"ski"`
	DurationNS            int64  `json:"duration_ns"`
	PIN                   string `json:"pin"`
	IdempotencyKey        string `json:"idempotency_key"`
	ExpectedStateRevision uint64 `json:"expected_state_revision"`
}

type pairingCallResult struct {
	StateRevision uint64 `json:"state_revision,omitempty"`
	Outcome       string `json:"outcome,omitempty"`
	Replayed      bool   `json:"replayed,omitempty"`
	ActionID      string `json:"action_id,omitempty"`
	Code          string `json:"code,omitempty"`
}

func (instance *peerInstance) transportContext(ctx context.Context) context.Context {
	ctx = eebusfacade.WithRemoteTransport(ctx, func(notice eebusfacade.RemoteTransportNotice) {
		instance.onTransport(notice.Kind, notice.SKI)
	})
	return eebusfacade.WithRemoteSource(ctx, func(notice eebusfacade.RemoteSourceNotice) {
		instance.onSource(notice)
	})
}

func (instance *peerInstance) onSource(notice eebusfacade.RemoteSourceNotice) {
	switch notice.Kind {
	case eebusfacade.RemoteSourceValue:
		if notice.Generation == 0 || notice.Value == "" || notice.Value == "null" {
			return
		}
		priority := notice.Priority
		if priority == 0 {
			priority = producerPriority
		}
		_ = instance.recordPairedValue(notice.SKI, semdriver.Element{}, priority, notice.Generation, notice.Value, notice.ObservedAt, notice.Path)
	case eebusfacade.RemoteSourceGenerationStopped:
		instance.stopGeneration(notice.SKI, notice.Generation)
	}
}

func (instance *peerInstance) onTransport(kind, ski string) {
	ski = normalizeSKI(ski)
	if ski == "" {
		return
	}
	instance.pairing.mu.Lock()
	defer instance.pairing.mu.Unlock()
	for index := range instance.pairing.records {
		record := &instance.pairing.records[index]
		if record.ski != ski || record.availability == "withdrawn" {
			continue
		}
		switch kind {
		case eebusfacade.RemoteTransportDisconnected:
			record.availability = "stale"
		case eebusfacade.RemoteTransportConnected:
			if !record.stopped {
				record.availability = "available"
			}
		}
	}
}

func (instance *peerInstance) registerPairingNamespace(ctx context.Context, session semdriver.Session) error {
	native := session.Native()
	if native == nil {
		return errors.New("native registry is unavailable")
	}
	root := native.Root()
	if err := instance.registerPairingMethods(native, root); err != nil {
		return err
	}
	if err := instance.registerPairingMembers(native, root); err != nil {
		return err
	}
	if err := instance.registerPairingView(native, root); err != nil {
		return err
	}
	return instance.publishReady(ctx)
}

func (instance *peerInstance) registerPairingMethods(native semdriver.NativeRegistry, root semdriver.NodeRef) error {
	methods := []struct {
		local string
		call  semdriver.NativeMethod
	}{
		{"open_pairing_window", instance.openPairingWindow},
		{"close_pairing_window", instance.closePairingWindow},
		{"select", instance.selectRemote},
		{"connect", instance.connectRemote},
		{"connect_with_pin", instance.connectRemoteWithPIN},
		{"confirm", instance.confirmRemote},
		{"cancel", instance.cancelRemote},
		{"retry_trusted", instance.retryTrusted},
		{"untrust", instance.untrustRemote},
		{"features_data_get", instance.featuresDataGet},
		{"features_data_set", instance.featuresDataSet},
	}
	for _, method := range methods {
		if err := native.Method(root, pairingMethodSpec(method.local), method.call); err != nil {
			return err
		}
	}
	return nil
}

func (instance *peerInstance) registerPairingMembers(native semdriver.NativeRegistry, root semdriver.NodeRef) error {
	readers := []struct {
		local string
		read  semdriver.NativeReader
	}{
		{"remotes", instance.readRemotes},
		{"sources", instance.readSources},
	}
	for _, reader := range readers {
		if err := native.ReadMember(root, pairingMemberSpec(reader.local), reader.read); err != nil {
			return err
		}
	}
	return nil
}

func (instance *peerInstance) registerPairingView(native semdriver.NativeRegistry, root semdriver.NodeRef) error {
	return native.View(semdriver.ViewSpec{
		Root: "remotes",
		Records: []semdriver.ViewRecord{{
			Name: "remotes",
			Fields: []semdriver.ViewField{
				{Name: "remotes", Type: semdriver.ViewType{Name: "string"}, Member: pairingMemberKey(semdriver.NamespaceID(Kind), instance.key, semdriver.NodeID(root.ID), "remotes")},
				{Name: "sources", Type: semdriver.ViewType{Name: "string"}, Member: pairingMemberKey(semdriver.NamespaceID(Kind), instance.key, semdriver.NodeID(root.ID), "sources")},
			},
		}},
	})
}

func (instance *peerInstance) openPairingWindow(ctx context.Context, raw []byte) ([]byte, error) {
	args, err := decodePairingArgs(raw)
	if err != nil {
		return nil, err
	}
	snapshot, err := instance.adminSnapshot(ctx, eebusruntime.AdminViewV1Trusted)
	if err != nil {
		return nil, err
	}
	result, failure := instance.admin.OpenPairingWindow(ctx, eebusruntime.OpenPairingWindowRequestV1{
		MutationPreconditionV1: instance.precondition(args, snapshot.StateRevision),
		Duration:               time.Duration(args.DurationNS),
	})
	return encodeAdminResult(result.StateRevision, string(result.Outcome), result.Replayed, "", failure)
}

func (instance *peerInstance) closePairingWindow(ctx context.Context, raw []byte) ([]byte, error) {
	args, err := decodePairingArgs(raw)
	if err != nil {
		return nil, err
	}
	snapshot, err := instance.adminSnapshot(ctx, eebusruntime.AdminViewV1Trusted)
	if err != nil {
		return nil, err
	}
	result, failure := instance.admin.ClosePairingWindow(ctx, eebusruntime.ClosePairingWindowRequestV1{
		MutationPreconditionV1: instance.precondition(args, snapshot.StateRevision),
	})
	return encodeAdminResult(result.StateRevision, string(result.Outcome), result.Replayed, "", failure)
}

func (instance *peerInstance) selectRemote(ctx context.Context, raw []byte) ([]byte, error) {
	args, err := decodePairingArgs(raw)
	if err != nil {
		return nil, err
	}
	snapshot, err := instance.adminSnapshot(ctx, eebusruntime.AdminViewV1Discovered)
	if err != nil {
		return nil, err
	}
	observation, ok := discoveredObservation(snapshot, args.SKI)
	if !ok {
		return nil, errors.New("discovered partner is absent")
	}
	result, failure := instance.admin.Select(ctx, eebusruntime.SelectRequestV1{
		MutationPreconditionV1: instance.precondition(args, snapshot.StateRevision),
		Observation:            observation,
		ExpectedSKI:            normalizeSKI(args.SKI),
	})
	if failure == nil {
		instance.rememberSelection(args.SKI, result.Selection)
	}
	return encodeAdminResult(result.StateRevision, string(result.Outcome), result.Replayed, "", failure)
}

func (instance *peerInstance) connectRemote(ctx context.Context, raw []byte) ([]byte, error) {
	return instance.connect(ctx, raw, false)
}

func (instance *peerInstance) connectRemoteWithPIN(ctx context.Context, raw []byte) ([]byte, error) {
	return instance.connect(ctx, raw, true)
}

func (instance *peerInstance) connect(ctx context.Context, raw []byte, withPIN bool) ([]byte, error) {
	args, err := decodePairingArgs(raw)
	if err != nil {
		return nil, err
	}
	selection, ok := instance.selectionFor(args.SKI)
	if !ok {
		return nil, errors.New("selection is absent")
	}
	snapshot, err := instance.adminSnapshot(ctx, eebusruntime.AdminViewV1Trusted)
	if err != nil {
		return nil, err
	}
	request := eebusruntime.ConnectRequestV1{
		MutationPreconditionV1: instance.precondition(args, snapshot.StateRevision),
		Selection:              selection,
	}
	if withPIN {
		if args.PIN == "" {
			return nil, errors.New("pin is required")
		}
		request.PIN = []byte(args.PIN)
	}
	result, failure := instance.admin.Connect(ctx, request)
	for index := range request.PIN {
		request.PIN[index] = 0
	}
	return encodeAdminResult(result.StateRevision, string(result.Outcome), result.Replayed, result.ActionID, failure)
}

func (instance *peerInstance) confirmRemote(ctx context.Context, raw []byte) ([]byte, error) {
	return instance.answerCandidate(ctx, raw, true)
}

func (instance *peerInstance) cancelRemote(ctx context.Context, raw []byte) ([]byte, error) {
	return instance.answerCandidate(ctx, raw, false)
}

func (instance *peerInstance) answerCandidate(ctx context.Context, raw []byte, confirm bool) ([]byte, error) {
	args, err := decodePairingArgs(raw)
	if err != nil {
		return nil, err
	}
	snapshot, err := instance.adminSnapshot(ctx, eebusruntime.AdminViewV1Candidate)
	if err != nil {
		return nil, err
	}
	candidate, ok := candidateHandle(snapshot, args.SKI)
	if !ok {
		return nil, errors.New("candidate is absent")
	}
	precondition := instance.precondition(args, snapshot.StateRevision)
	if confirm {
		result, failure := instance.admin.Confirm(ctx, eebusruntime.ConfirmRequestV1{
			MutationPreconditionV1: precondition,
			Candidate:              candidate,
			ExpectedSKI:            normalizeSKI(args.SKI),
		})
		return encodeAdminResult(result.StateRevision, string(result.Outcome), result.Replayed, "", failure)
	}
	result, failure := instance.admin.Cancel(ctx, eebusruntime.CancelRequestV1{
		MutationPreconditionV1: precondition,
		Candidate:              candidate,
	})
	return encodeAdminResult(result.StateRevision, string(result.Outcome), result.Replayed, "", failure)
}

func (instance *peerInstance) retryTrusted(ctx context.Context, raw []byte) ([]byte, error) {
	args, partner, snapshot, err := instance.trustedPartner(ctx, raw)
	if err != nil {
		return nil, err
	}
	result, failure := instance.admin.RetryTrusted(ctx, eebusruntime.RetryTrustedRequestV1{
		MutationPreconditionV1: instance.precondition(args, snapshot.StateRevision),
		Partner:                partner,
	})
	return encodeAdminResult(result.StateRevision, string(result.Outcome), result.Replayed, "", failure)
}

func (instance *peerInstance) untrustRemote(ctx context.Context, raw []byte) ([]byte, error) {
	args, partner, snapshot, err := instance.trustedPartner(ctx, raw)
	if err != nil {
		return nil, err
	}
	result, failure := instance.admin.Untrust(ctx, eebusruntime.UntrustRequestV1{
		MutationPreconditionV1: instance.precondition(args, snapshot.StateRevision),
		Partner:                partner,
	})
	if failure == nil {
		instance.withdrawRemote(args.SKI)
	}
	return encodeAdminResult(result.StateRevision, string(result.Outcome), result.Replayed, "", failure)
}

func (instance *peerInstance) trustedPartner(ctx context.Context, raw []byte) (pairingArgs, eebusruntime.PartnerHandleV1, eebusruntime.AdminSnapshotV1, error) {
	args, err := decodePairingArgs(raw)
	if err != nil {
		return pairingArgs{}, eebusruntime.PartnerHandleV1{}, eebusruntime.AdminSnapshotV1{}, err
	}
	snapshot, err := instance.adminSnapshot(ctx, eebusruntime.AdminViewV1Trusted)
	if err != nil {
		return pairingArgs{}, eebusruntime.PartnerHandleV1{}, eebusruntime.AdminSnapshotV1{}, err
	}
	partner, ok := trustedHandle(snapshot, args.SKI)
	if !ok {
		return pairingArgs{}, eebusruntime.PartnerHandleV1{}, eebusruntime.AdminSnapshotV1{}, errors.New("trusted partner is absent")
	}
	return args, partner, snapshot, nil
}

func (instance *peerInstance) featuresDataGet(ctx context.Context, raw []byte) ([]byte, error) {
	request := eebusraw.FeatureDataGetRequestV1{}
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, errors.New("feature request is invalid")
	}
	data, terminal := instance.runtime.FeaturesDataGet(ctx, eebusraw.ReadAuthorizationV1{
		PrincipalClass: "operator", Scope: eebusraw.AuthScopeV1RawRead, Tool: eebusraw.ToolV1FeaturesDataGet, MaskTier: eebusraw.MaskTierRaw,
	}, request)
	if terminal != nil {
		return encodeCode(string(terminal.Code))
	}
	instance.recordRead(data)
	body, err := json.Marshal(data)
	return body, err
}

func (instance *peerInstance) recordRead(data eebusraw.FeatureDataGetDataV1) {
	for _, result := range data.Results {
		if result.Runtime.ConnectionGeneration == 0 || strings.TrimSpace(result.Target.RemoteSKI) == "" {
			continue
		}
		encoded, err := json.Marshal(result.Value)
		if err != nil || len(encoded) == 0 || string(encoded) == "null" {
			continue
		}
		at := result.DataTimestamp
		if at.IsZero() {
			at = result.ReceivedAt
		}
		_ = instance.recordPairedValue(result.Target.RemoteSKI, semdriver.Element{}, producerPriority, result.Runtime.ConnectionGeneration, string(encoded), at, "")
	}
}

func (instance *peerInstance) featuresDataSet(ctx context.Context, raw []byte) ([]byte, error) {
	request := eebusraw.FeatureDataSetRequestV1{}
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, errors.New("feature request is invalid")
	}
	mutation, ok := instance.runtime.(eebusruntime.RawMutationRuntimeV1)
	if !ok || mutation == nil {
		return nil, errors.New("raw mutation runtime is unavailable")
	}
	outcome, terminal := mutation.FeaturesDataSet(ctx, eebusraw.WriteAuthorizationV1{
		PrincipalClass: "operator", Scope: eebusraw.AuthScopeV1RawWrite, Tool: eebusraw.ToolV1FeaturesDataSet, MaskTier: eebusraw.MaskTierRaw,
	}, request)
	if terminal != nil {
		return encodeCode(string(terminal.Code))
	}
	body, err := json.Marshal(outcome)
	return body, err
}

func (instance *peerInstance) readRemotes(ctx context.Context) ([]byte, error) {
	if err := instance.publishReady(ctx); err != nil {
		return nil, err
	}
	rows, err := instance.remoteRows()
	if err != nil {
		return nil, err
	}
	if rows == nil {
		rows = []pairingRemote{}
	}
	return json.Marshal(rows)
}

func (instance *peerInstance) readSources(context.Context) ([]byte, error) {
	views := instance.sourceViews()
	if views == nil {
		views = []pairingSourceView{}
	}
	return json.Marshal(views)
}

func (instance *peerInstance) readRemoteField(ski, local string) ([]byte, error) {
	ski = normalizeSKI(ski)
	rows, err := instance.remoteRows()
	if err != nil {
		return nil, err
	}
	row := pairingRemote{}
	for _, candidate := range rows {
		if candidate.SKI == ski {
			row = candidate
			break
		}
	}
	record, _ := instance.sourceForSKI(ski)
	switch local {
	case "ski":
		return json.Marshal(ski)
	case "ship_state":
		return json.Marshal(row.ShipState)
	case "pairing_state":
		return json.Marshal(row.PairingState)
	case "last_value":
		return json.Marshal(record.value)
	case "observed_at":
		return json.Marshal(observedAt(record.at))
	default:
		return nil, errors.New("remote member is unknown")
	}
}

func (instance *peerInstance) remoteReader(ski, local string) semdriver.NativeReader {
	return func(context.Context) ([]byte, error) {
		return instance.readRemoteField(ski, local)
	}
}

func (instance *peerInstance) ensureRemoteNode(ski string) error {
	ski = normalizeSKI(ski)
	if ski == "" {
		return nil
	}
	instance.pairing.mu.Lock()
	if instance.pairing.nodes != nil {
		if _, ok := instance.pairing.nodes[ski]; ok {
			instance.pairing.mu.Unlock()
			return nil
		}
	}
	instance.pairing.mu.Unlock()
	native := instance.currentNative()
	if native == nil {
		return errors.New("native registry is unavailable")
	}
	root := native.Root()
	ref, err := native.Node(semdriver.NodeSpec{
		Parent:   &root,
		Kind:     "resource.remote",
		Local:    semdriver.NodeID(ski),
		Contract: "eebus.remote",
	})
	if err != nil {
		return err
	}
	for _, local := range []string{"ski", "ship_state", "pairing_state", "last_value", "observed_at"} {
		if err := native.ReadMember(ref, pairingMemberSpec(local), instance.remoteReader(ski, local)); err != nil {
			return err
		}
	}
	instance.pairing.mu.Lock()
	if instance.pairing.nodes == nil {
		instance.pairing.nodes = map[string]semdriver.NodeRef{}
	}
	instance.pairing.nodes[ski] = ref
	instance.pairing.mu.Unlock()
	return nil
}

func (instance *peerInstance) recordPairedValue(ski string, element semdriver.Element, priority int32, generation uint64, value string, at time.Time, path string) error {
	ski = normalizeSKI(ski)
	if ski == "" {
		return errors.New("remote ski is empty")
	}
	if element.Subject.Bridge == "" && element.Subject.ID == "" && element.Key.FactID == "" {
		element = instance.defaultElement(ski)
	}
	if path == "" {
		path = instance.defaultPath(ski, generation)
	}
	instance.pairing.mu.Lock()
	if _, stopped := instance.pairing.stopped[generationKey(ski, generation)]; stopped {
		instance.pairing.mu.Unlock()
		return nil
	}
	if current := instance.pairing.byPath(path); current != nil {
		if current.availability == "withdrawn" {
			instance.pairing.mu.Unlock()
			return errors.New("source is withdrawn")
		}
		if current.stopped && current.generation == generation {
			instance.pairing.mu.Unlock()
			return nil
		}
		current.ski = ski
		current.element = element
		current.priority = priority
		current.generation = generation
		current.value = value
		current.at = at
		if current.availability == "" {
			current.availability = "available"
		}
		instance.pairing.mu.Unlock()
		return instance.publishReady(context.Background())
	}
	instance.pairing.records = append(instance.pairing.records, pairingSource{
		ski: ski, element: element, path: path, priority: priority, generation: generation,
		value: value, at: at, availability: "available",
	})
	instance.pairing.mu.Unlock()
	return instance.publishReady(context.Background())
}

func (instance *peerInstance) stopGeneration(ski string, generation uint64) {
	ski = normalizeSKI(ski)
	instance.pairing.mu.Lock()
	defer instance.pairing.mu.Unlock()
	if instance.pairing.stopped == nil {
		instance.pairing.stopped = map[string]struct{}{}
	}
	instance.pairing.stopped[generationKey(ski, generation)] = struct{}{}
	for index := range instance.pairing.records {
		record := &instance.pairing.records[index]
		if record.ski == ski && record.generation == generation && record.availability != "withdrawn" {
			record.stopped = true
			record.availability = "stale"
		}
	}
}

func (instance *peerInstance) withdrawRemote(ski string) {
	ski = normalizeSKI(ski)
	instance.pairing.mu.Lock()
	defer instance.pairing.mu.Unlock()
	for index := range instance.pairing.records {
		record := &instance.pairing.records[index]
		if record.ski == ski {
			record.availability = "withdrawn"
		}
	}
}

func (instance *peerInstance) authoritative(element semdriver.Element) (pairingSource, bool) {
	instance.pairing.mu.Lock()
	defer instance.pairing.mu.Unlock()
	return instance.pairing.authoritative(element)
}

func (instance *peerInstance) sourceForSKI(ski string) (pairingSource, bool) {
	ski = normalizeSKI(ski)
	instance.pairing.mu.Lock()
	defer instance.pairing.mu.Unlock()
	found := pairingSource{}
	ok := false
	for _, record := range instance.pairing.records {
		if record.ski != ski || record.availability == "withdrawn" {
			continue
		}
		if !ok || record.priority > found.priority {
			found = record
			ok = true
		}
	}
	return found, ok
}

func (instance *peerInstance) sourceViews() []pairingSourceView {
	instance.pairing.mu.Lock()
	defer instance.pairing.mu.Unlock()
	views := make([]pairingSourceView, 0, len(instance.pairing.records))
	for _, record := range instance.pairing.records {
		winner, won := instance.pairing.authoritative(record.element)
		views = append(views, pairingSourceView{
			SKI: record.ski, Path: record.path, Priority: record.priority, Generation: record.generation,
			Value: record.value, ObservedAt: observedAt(record.at), Availability: record.availability,
			Stopped: record.stopped, Declared: record.declared, Authoritative: won && winner.path == record.path,
		})
	}
	return views
}

func (state *pairingState) authoritative(element semdriver.Element) (pairingSource, bool) {
	found := pairingSource{}
	ok := false
	for _, record := range state.records {
		if record.availability == "withdrawn" || !pairingElementEqual(record.element, element) {
			continue
		}
		if !ok || record.priority > found.priority {
			found = record
			ok = true
		}
	}
	return found, ok
}

func (state *pairingState) byPath(path string) *pairingSource {
	for index := range state.records {
		if state.records[index].path == path {
			return &state.records[index]
		}
	}
	return nil
}

func (instance *peerInstance) publishReady(ctx context.Context) error {
	rows, err := instance.remoteRows()
	if err != nil {
		return err
	}
	paired := map[string]bool{}
	for _, row := range rows {
		paired[row.SKI] = row.PairingState == string(eebusraw.PairingStatePaired)
		if err := instance.ensureRemoteNode(row.SKI); err != nil {
			return err
		}
	}
	instance.pairing.mu.Lock()
	pending := []pairingSource(nil)
	for index := range instance.pairing.records {
		record := &instance.pairing.records[index]
		if record.declared || record.availability == "withdrawn" || record.value == "" || !paired[record.ski] {
			continue
		}
		record.declared = true
		pending = append(pending, *record)
	}
	instance.pairing.mu.Unlock()
	for _, record := range pending {
		if err := instance.declareSource(ctx, record); err != nil {
			instance.pairing.mu.Lock()
			if current := instance.pairing.byPath(record.path); current != nil {
				current.declared = false
			}
			instance.pairing.mu.Unlock()
			return err
		}
	}
	return nil
}

func (instance *peerInstance) declareSource(ctx context.Context, record pairingSource) error {
	instance.mu.Lock()
	session := instance.session
	instance.mu.Unlock()
	if session == nil || session.Publisher() == nil {
		return errors.New("publisher is unavailable")
	}
	transaction, err := session.Publisher().Begin(ctx)
	if err != nil {
		return err
	}
	source := semdriver.Source{
		For:      record.element,
		Producer: instance.producer(record.ski),
		PathKey:  record.path,
		Priority: record.priority,
	}
	if err := transaction.DeclareSource(source); err != nil {
		_ = transaction.Abort(ctx)
		return err
	}
	return transaction.Commit(ctx)
}

func (instance *peerInstance) remoteRows() ([]pairingRemote, error) {
	instance.mu.Lock()
	runtime := instance.runtime
	instance.mu.Unlock()
	if runtime == nil {
		return nil, errors.New("runtime is unavailable")
	}
	snapshot, err := runtime.Snapshot()
	if err != nil {
		return nil, err
	}
	return rowsFromSnapshot(snapshot), nil
}

func rowsFromSnapshot(snapshot eebusruntime.SnapshotV1) []pairingRemote {
	index := map[string]int{}
	rows := []pairingRemote(nil)
	touch := func(ski string) (int, bool) {
		ski = normalizeSKI(ski)
		if ski == "" {
			return 0, false
		}
		if at, ok := index[ski]; ok {
			return at, true
		}
		rows = append(rows, pairingRemote{SKI: ski, ShipState: "unknown", PairingState: string(eebusraw.PairingStateUnknown)})
		index[ski] = len(rows) - 1
		return len(rows) - 1, true
	}
	for _, service := range snapshot.Services {
		at, ok := touch(service.SKI)
		if !ok {
			continue
		}
		if service.Paired {
			rows[at].PairingState = string(eebusraw.PairingStatePaired)
		} else if rows[at].PairingState == string(eebusraw.PairingStateUnknown) {
			rows[at].PairingState = string(eebusraw.PairingStateUnpaired)
		}
	}
	for _, session := range snapshot.Sessions {
		at, ok := touch(session.RemoteSKI)
		if !ok || session.State == "" {
			continue
		}
		rows[at].ShipState = string(session.State)
	}
	for _, pairing := range snapshot.Pairing {
		at, ok := touch(pairing.RemoteSKI)
		if !ok || pairing.State == "" {
			continue
		}
		rows[at].PairingState = string(pairing.State)
	}
	return rows
}

func (instance *peerInstance) currentNative() semdriver.NativeRegistry {
	instance.mu.Lock()
	session := instance.session
	instance.mu.Unlock()
	if session == nil {
		return nil
	}
	return session.Native()
}

func (instance *peerInstance) adminSnapshot(ctx context.Context, view eebusruntime.AdminViewV1) (eebusruntime.AdminSnapshotV1, error) {
	instance.mu.Lock()
	admin := instance.admin
	instance.mu.Unlock()
	if admin == nil {
		return eebusruntime.AdminSnapshotV1{}, errors.New("admin is unavailable")
	}
	snapshot, failure := admin.Snapshot(ctx, eebusruntime.AdminSnapshotRequestV1{View: view})
	if failure != nil {
		return eebusruntime.AdminSnapshotV1{}, failure
	}
	return snapshot, nil
}

func (instance *peerInstance) precondition(args pairingArgs, revision uint64) eebusruntime.MutationPreconditionV1 {
	key := args.IdempotencyKey
	if key == "" {
		instance.pairing.mu.Lock()
		instance.pairing.seq++
		seq := instance.pairing.seq
		instance.pairing.mu.Unlock()
		key = "pairing-" + strconv.FormatUint(seq, 10)
	}
	expected := args.ExpectedStateRevision
	if expected == 0 {
		expected = revision
	}
	return eebusruntime.MutationPreconditionV1{IdempotencyKey: key, ExpectedStateRevision: expected}
}

func (instance *peerInstance) rememberSelection(ski string, selection eebusruntime.SelectionHandleV1) {
	ski = normalizeSKI(ski)
	instance.pairing.mu.Lock()
	defer instance.pairing.mu.Unlock()
	if instance.pairing.selections == nil {
		instance.pairing.selections = map[string]eebusruntime.SelectionHandleV1{}
	}
	instance.pairing.selections[ski] = selection
}

func (instance *peerInstance) selectionFor(ski string) (eebusruntime.SelectionHandleV1, bool) {
	ski = normalizeSKI(ski)
	instance.pairing.mu.Lock()
	defer instance.pairing.mu.Unlock()
	selection, ok := instance.pairing.selections[ski]
	return selection, ok
}

func (instance *peerInstance) defaultElement(ski string) semdriver.Element {
	return semdriver.Element{
		Subject: semdriver.NodeRef{Bridge: "bridge.eebus", ID: semreg.ResourceID("resource." + string(instance.key) + "." + ski)},
		Key: semreg.FactKey{
			PackID: "pack.eebus-inbound", PackVersion: "1.0.0", FactID: "fact.remote-observation",
			Dimensions: []semreg.Dimension{},
		},
	}
}

func (instance *peerInstance) producer(ski string) semdriver.NodeRef {
	return semdriver.NodeRef{Bridge: "bridge.eebus", ID: semreg.ResourceID("resource." + string(instance.key) + ".producer." + ski)}
}

func (instance *peerInstance) defaultPath(ski string, generation uint64) string {
	return string(instance.key) + "/" + ski + "/" + strconv.FormatUint(generation, 10)
}

func decodePairingArgs(raw []byte) (pairingArgs, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return pairingArgs{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	args := pairingArgs{}
	if err := decoder.Decode(&args); err != nil {
		return pairingArgs{}, errors.New("pairing request is invalid")
	}
	return args, nil
}

func encodeAdminResult(revision uint64, outcome string, replayed bool, actionID string, failure *eebusruntime.AdminErrorV1) ([]byte, error) {
	if failure != nil {
		return encodeCode(string(failure.Code))
	}
	body, err := json.Marshal(pairingCallResult{StateRevision: revision, Outcome: outcome, Replayed: replayed, ActionID: actionID})
	return body, err
}

func encodeCode(code string) ([]byte, error) {
	if code == "" {
		code = string(eebusruntime.AdminErrorCodeV1UnknownState)
	}
	body, err := json.Marshal(pairingCallResult{Code: code})
	if err != nil {
		return nil, err
	}
	return body, errors.New(code)
}

func trustedHandle(snapshot eebusruntime.AdminSnapshotV1, ski string) (eebusruntime.PartnerHandleV1, bool) {
	ski = normalizeSKI(ski)
	for _, partner := range snapshot.Trusted {
		if normalizeSKI(partner.SKI) == ski {
			return partner.Partner, true
		}
	}
	return eebusruntime.PartnerHandleV1{}, false
}

func discoveredObservation(snapshot eebusruntime.AdminSnapshotV1, ski string) (eebusruntime.ObservationHandleV1, bool) {
	ski = normalizeSKI(ski)
	for _, partner := range snapshot.Discovered {
		if normalizeSKI(partner.SKI) == ski {
			return partner.Observation, true
		}
	}
	return eebusruntime.ObservationHandleV1{}, false
}

func candidateHandle(snapshot eebusruntime.AdminSnapshotV1, ski string) (eebusruntime.CandidateHandleV1, bool) {
	ski = normalizeSKI(ski)
	for _, candidate := range snapshot.Candidates {
		if normalizeSKI(candidate.SKI) == ski {
			return candidate.Candidate, true
		}
	}
	return eebusruntime.CandidateHandleV1{}, false
}

func generationKey(ski string, generation uint64) string {
	return ski + "/" + strconv.FormatUint(generation, 10)
}

func pairingElementEqual(left, right semdriver.Element) bool {
	if left.Subject != right.Subject || left.Key.PackID != right.Key.PackID || left.Key.PackVersion != right.Key.PackVersion || left.Key.FactID != right.Key.FactID || len(left.Key.Dimensions) != len(right.Key.Dimensions) {
		return false
	}
	return reflect.DeepEqual(left.Key.Dimensions, right.Key.Dimensions)
}

func observedAt(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.UTC().Format(time.RFC3339Nano)
}
