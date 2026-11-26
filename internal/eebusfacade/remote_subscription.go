package eebusfacade

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/Project-Helianthus/helianthus-eebus-go/session"
	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
	spine "github.com/Project-Helianthus/helianthus-spine-go/spine"
)

var remoteSubscriptionFeatureTypes = map[spinemodel.FeatureTypeType]struct{}{
	spinemodel.FeatureTypeTypeMeasurement:          {},
	spinemodel.FeatureTypeTypeSetpoint:             {},
	spinemodel.FeatureTypeTypeHvac:                 {},
	spinemodel.FeatureTypeTypeDeviceClassification: {},
	spinemodel.FeatureTypeTypeElectricalConnection: {},
	spinemodel.FeatureTypeTypeDeviceConfiguration:  {},
	spinemodel.FeatureTypeTypeDeviceDiagnosis:      {},
	spinemodel.FeatureTypeTypeLoadControl:          {},
}

var errRemoteSubscriptionUnavailable = errors.New("remote feature subscription is unavailable")

// RemoteFeatureDataBackend serves the data a trusted remote last notified for one subscribed
// server-feature function, as a READ observation stamped with the notification time.
type RemoteFeatureDataBackend interface {
	FeatureNotifiedDataGet(
		context.Context,
		eebusraw.ReadAuthorizationV1,
		eebusraw.FeatureTargetV1,
	) (eebusraw.ReadObservationV1, *eebusraw.ErrorV1)
}

var _ RemoteFeatureDataBackend = (*serviceBackend)(nil)

type remoteSubscriptionState struct {
	io       sync.Mutex
	active   map[string]*remoteSubscriptionEntry
	notified map[string]remoteNotifiedData
	results  spineapi.NodeManagementInterface
	trusted  func(string) bool
	revoked  func(string) bool
	session  *session.Instance
	told     map[string]bool
	up       map[string]*rawRemoteLease
	down     map[string]struct{}
	revoke   map[string]struct{}
	dirty    map[string]struct{}
	sentMu   sync.Mutex
	sent     map[string]uint64
	// heldMu guards the peer-held read state. The session calls PeerHeld while it holds its own lock,
	// so this lock is never held across a call into the session.
	heldMu      sync.Mutex
	held        map[string][]session.Pair
	heldReady   map[string]bool
	heldAsked   map[string]bool
	heldCounter map[string]uint64
	heldTap     *remoteSubscriptionHeldTap
}

func (state *remoteSubscriptionState) initLocked() {
	if state.active == nil {
		state.active = make(map[string]*remoteSubscriptionEntry)
		state.notified = make(map[string]remoteNotifiedData)
		state.told = make(map[string]bool)
		state.up = make(map[string]*rawRemoteLease)
		state.down = make(map[string]struct{})
		state.revoke = make(map[string]struct{})
		state.dirty = make(map[string]struct{})
		state.sent = make(map[string]uint64)
		state.held = make(map[string][]session.Pair)
		state.heldReady = make(map[string]bool)
		state.heldAsked = make(map[string]bool)
		state.heldCounter = make(map[string]uint64)
	}
}

type remoteSubscriptionEntry struct {
	key         string
	lease       *rawRemoteLease
	address     spinemodel.FeatureAddressType
	featureType spinemodel.FeatureTypeType
}

type remoteSubscriptionWork struct {
	ski     string
	revoke  bool
	down    bool
	trusted bool
	lease   *rawRemoteLease
	entries []*remoteSubscriptionEntry
}

type remoteSubscriptionTransport struct {
	bridge *rawFeatureRuntimeBridge
}

var _ session.Transport = remoteSubscriptionTransport{}

func (bridge *rawFeatureRuntimeBridge) attachRemoteSubscriptions(stateRoot string) error {
	root := remoteSubscriptionStoreRoot(stateRoot)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	instance, err := session.Attach(session.AttachConfig{
		Device:    bridge.local,
		StateRoot: root,
		Transport: remoteSubscriptionTransport{bridge: bridge},
	})
	if err != nil {
		return err
	}
	bridge.mu.Lock()
	bridge.remoteSubscriptions.initLocked()
	bridge.remoteSubscriptions.session = instance
	bridge.mu.Unlock()
	bridge.remoteSubscriptions.installHeldTap(bridge)
	return nil
}

// remoteSubscriptionHeldTap wraps the local node-management feature the session installed. The session
// records the peer's nodeManagementSubscriptionData reply only for a read it sent itself, which it does
// not do when the driver supplies the transport, so the tap hands that reply to the driver's PeerHeld.
type remoteSubscriptionHeldTap struct {
	spineapi.FeatureLocalInterface
	bridge *rawFeatureRuntimeBridge
}

func (tap *remoteSubscriptionHeldTap) HandleMessage(message *spineapi.Message) *spinemodel.ErrorType {
	result := tap.FeatureLocalInterface.HandleMessage(message)
	if message != nil && message.Cmd.NodeManagementSubscriptionData != nil &&
		message.CmdClassifier == spinemodel.CmdClassifierTypeReply {
		tap.bridge.noteRemoteHeld(message)
	}
	return result
}

func (state *remoteSubscriptionState) installHeldTap(bridge *rawFeatureRuntimeBridge) {
	if isNilRawRuntimeValue(bridge.local) {
		return
	}
	for _, entity := range bridge.local.Entities() {
		features := entity.Features()
		for i, feature := range features {
			if isNilRawRuntimeValue(feature) || feature.Type() != spinemodel.FeatureTypeTypeNodeManagement {
				continue
			}
			tap := &remoteSubscriptionHeldTap{FeatureLocalInterface: feature, bridge: bridge}
			features[i] = tap
			state.heldMu.Lock()
			state.heldTap = tap
			state.heldMu.Unlock()
			return
		}
	}
}

func (state *remoteSubscriptionState) removeHeldTap(local spineapi.DeviceLocalInterface) {
	state.heldMu.Lock()
	tap := state.heldTap
	state.heldTap = nil
	state.heldMu.Unlock()
	if tap == nil || isNilRawRuntimeValue(local) {
		return
	}
	for _, entity := range local.Entities() {
		features := entity.Features()
		for i, feature := range features {
			if wrapped, ok := feature.(*remoteSubscriptionHeldTap); ok && wrapped == tap {
				features[i] = tap.FeatureLocalInterface
				return
			}
		}
	}
}

func (bridge *rawFeatureRuntimeBridge) noteRemoteHeld(message *spineapi.Message) {
	if isNilRawRuntimeValue(message.DeviceRemote) || message.RequestHeader == nil ||
		message.RequestHeader.MsgCounterReference == nil {
		return
	}
	ski := strings.ToLower(message.DeviceRemote.Ski())
	state := &bridge.remoteSubscriptions
	state.heldMu.Lock()
	expected, asking := state.heldCounter[ski]
	if !asking || expected != uint64(*message.RequestHeader.MsgCounterReference) {
		state.heldMu.Unlock()
		return
	}
	delete(state.heldCounter, ski)
	var pairs []session.Pair
	for _, entry := range message.Cmd.NodeManagementSubscriptionData.SubscriptionEntry {
		if entry.ClientAddress == nil || entry.ServerAddress == nil {
			continue
		}
		pairs = append(pairs, session.Pair{
			Client: cloneRawFeatureAddress(*entry.ClientAddress),
			Server: cloneRawFeatureAddress(*entry.ServerAddress),
		})
	}
	state.held[ski] = pairs
	state.heldReady[ski] = true
	state.heldMu.Unlock()
	bridge.mu.Lock()
	instance := state.session
	bridge.mu.Unlock()
	if instance != nil {
		_ = instance.Reconcile(ski)
	}
}

// sendRemoteSubscriptionRead sends the read; a sender that cannot send counts as not answered.
func sendRemoteSubscriptionRead(
	local spineapi.DeviceLocalInterface,
	remote spineapi.DeviceRemoteInterface,
) (counter *spinemodel.MsgCounterType, ok bool) {
	defer func() {
		if recover() != nil {
			counter, ok = nil, false
		}
	}()
	counter, err := remote.Sender().Request(
		spinemodel.CmdClassifierTypeCall,
		local.NodeManagement().Address(),
		spine.NodeManagementAddress(remote.Address()),
		true,
		[]spinemodel.CmdType{{NodeManagementSubscriptionData: &spinemodel.NodeManagementSubscriptionDataType{}}},
	)
	return counter, err == nil && counter != nil
}

func (state *remoteSubscriptionState) forgetHeld(ski string) {
	ski = strings.ToLower(ski)
	state.heldMu.Lock()
	defer state.heldMu.Unlock()
	delete(state.held, ski)
	delete(state.heldReady, ski)
	delete(state.heldAsked, ski)
	delete(state.heldCounter, ski)
}

// peerHeld asks the remote for its nodeManagementSubscriptionData once per connection (SPINE 1.3.0
// §7.4.3) and answers the session with the pairs the remote still holds once the reply arrived. The
// answer is given a single time, so a pair established after the reply is not mistaken for a lost one.
func (bridge *rawFeatureRuntimeBridge) peerHeld(peer string) ([]session.Pair, bool) {
	ski := strings.ToLower(peer)
	state := &bridge.remoteSubscriptions
	state.heldMu.Lock()
	if state.heldReady[ski] {
		pairs := state.held[ski]
		delete(state.heldReady, ski)
		delete(state.held, ski)
		state.heldMu.Unlock()
		return pairs, true
	}
	asked := state.heldAsked[ski]
	state.heldMu.Unlock()
	if asked || isNilRawRuntimeValue(bridge.local) || isNilRawRuntimeValue(bridge.local.NodeManagement()) {
		return nil, false
	}
	remote := bridge.local.RemoteDeviceForSki(ski)
	if isNilRawRuntimeValue(remote) || isNilRawRuntimeValue(remote.Sender()) || remote.Address() == nil {
		return nil, false
	}
	counter, ok := sendRemoteSubscriptionRead(bridge.local, remote)
	if !ok {
		return nil, false
	}
	state.heldMu.Lock()
	state.heldAsked[ski] = true
	state.heldCounter[ski] = uint64(*counter)
	state.heldMu.Unlock()
	return nil, false
}

// attachRemoteSubscriptionsReporting attaches the store and makes a failure visible: without the store the
// driver holds no remote subscriptions, so the error is logged instead of being dropped.
func (bridge *rawFeatureRuntimeBridge) attachRemoteSubscriptionsReporting(stateRoot string) {
	if err := bridge.attachRemoteSubscriptions(stateRoot); err != nil {
		slog.Error("remote subscriptions are unavailable: the subscription store did not attach", "error", err)
	}
}

func remoteSubscriptionStoreRoot(stateRoot string) string {
	return filepath.Join(filepath.Dir(stateRoot), "."+filepath.Base(stateRoot)+"-remote-subscriptions")
}

func (backend *serviceBackend) closeRemoteSubscriptions() error {
	if backend.rawFeatures == nil {
		return nil
	}
	return backend.rawFeatures.closeRemoteSubscriptions()
}

func (bridge *rawFeatureRuntimeBridge) closeRemoteSubscriptions() error {
	if bridge == nil {
		return nil
	}
	state := &bridge.remoteSubscriptions
	state.io.Lock()
	defer state.io.Unlock()
	bridge.mu.Lock()
	instance := state.session
	state.session = nil
	bridge.mu.Unlock()
	state.removeHeldTap(bridge.local)
	if instance == nil {
		return nil
	}
	return instance.Close()
}

func (bridge *rawFeatureRuntimeBridge) bindRemoteSubscriptionPairing(firstTrust *runtimeFirstTrustResources) {
	if firstTrust == nil || firstTrust.coordinator == nil {
		return
	}
	coordinator := firstTrust.coordinator
	bridge.mu.Lock()
	bridge.remoteSubscriptions.revoked = func(ski string) bool {
		remote, err := hex.DecodeString(ski)
		return err == nil && len(remote) == 20 && coordinator.revokedAssociation(remote)
	}
	instance := bridge.remoteSubscriptions.session
	revoked := bridge.remoteSubscriptions.revoked
	bridge.mu.Unlock()
	sweepRevokedRemoteSubscriptions(instance, revoked)
}

// revokedAssociation reports an explicit revocation only: the durable control view lists the remote's
// association as no longer trusted. A remote that is merely absent from the in-memory trusted set
// (quarantine, reload, unknown outcome) is not revoked and keeps its feature pairs. Only the current
// lineage decides, so a remote paired again after a revocation is not hidden by its older revoked record.
func (coordinator *firstTrustCoordinator) revokedAssociation(remote []byte) bool {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	for _, association := range coordinator.controlView.associations {
		if association.lineage == coordinator.controlView.control.associationLineage && bytes.Equal(association.subject, remote) {
			return !association.trusted
		}
	}
	return false
}

func (transport remoteSubscriptionTransport) SendSubscribe(peer string, pair session.Pair) session.SendResult {
	client := transport.bridge.local.FeatureByAddress(&pair.Client)
	if isNilRawRuntimeValue(client) {
		return session.SendResult{}
	}
	server := cloneRawFeatureAddress(pair.Server)
	counter, failure := client.SubscribeToRemote(&server)
	return transport.bridge.remoteSubscriptions.sendResult(
		peer+"\x00subscribe\x00"+pair.Client.String()+"\x00"+pair.Server.String(), counter, failure,
	)
}

func (transport remoteSubscriptionTransport) SendDelete(peer string, del session.Delete) session.SendResult {
	client := transport.bridge.local.FeatureByAddress(&del.Client)
	if isNilRawRuntimeValue(client) {
		return session.SendResult{}
	}
	server := cloneRawFeatureAddress(del.Server)
	counter, failure := client.RemoveRemoteSubscription(&server)
	return transport.bridge.remoteSubscriptions.sendResult(
		peer+"\x00delete\x00"+del.Client.String()+"\x00"+del.Server.String(), counter, failure,
	)
}

func (remoteSubscriptionTransport) SendNotify(
	string,
	spinemodel.FeatureAddressType,
	spinemodel.FunctionType,
	any,
) error {
	return errRemoteSubscriptionUnavailable
}

func (transport remoteSubscriptionTransport) PeerHeld(peer string) ([]session.Pair, bool) {
	return transport.bridge.peerHeld(peer)
}

func (state *remoteSubscriptionState) sendResult(
	key string,
	counter *spinemodel.MsgCounterType,
	failure *spinemodel.ErrorType,
) session.SendResult {
	if failure != nil || counter == nil {
		return session.SendResult{}
	}
	state.sentMu.Lock()
	defer state.sentMu.Unlock()
	last, repeated := state.sent[key]
	state.sent[key] = uint64(*counter)
	return session.SendResult{Counter: uint64(*counter), Sent: true, Replay: repeated && last == uint64(*counter)}
}

func (state *remoteSubscriptionState) forgetSentCounters(ski string) {
	state.sentMu.Lock()
	defer state.sentMu.Unlock()
	for key := range state.sent {
		if strings.HasPrefix(key, ski+"\x00") {
			delete(state.sent, key)
		}
	}
}

// bindRemoteSubscriptionTrust hands the subscription store the trust gate the SPINE event path
// uses, so a discovery refresh never subscribes a remote that is not trusted and drops what such a
// remote still holds.
func (handler *runtimeServiceHandler) bindRemoteSubscriptionTrust() {
	handler.mu.Lock()
	bridge := handler.rawFeatures
	handler.mu.Unlock()
	if bridge == nil {
		return
	}
	bridge.mu.Lock()
	bridge.remoteSubscriptions.trusted = handler.remoteLivenessAllowed
	bridge.mu.Unlock()
}

func (bridge *rawFeatureRuntimeBridge) remoteSubscriptionTrustedLocked(ski string) bool {
	trusted := bridge.remoteSubscriptions.trusted
	return trusted == nil || trusted(ski)
}

func (handler *runtimeServiceHandler) routeRemoteNotify(payload spineapi.EventPayload) {
	ski := strings.ToLower(strings.TrimSpace(payload.Ski))
	if ski == "" {
		return
	}
	handler.mu.Lock()
	bridge := handler.rawFeatures
	active := handler.spineEventsActive
	allowed := active && handler.remoteLivenessAllowedLocked(ski)
	handler.mu.Unlock()
	if bridge == nil || !active {
		return
	}
	switch {
	case payload.EventType == spineapi.EventTypeDeviceChange && payload.ChangeType == spineapi.ElementChangeRemove:
		bridge.forgetRemoteSubscriptions(ski, payload.Device)
		bridge.revokeRemoteSubscriptions(ski)
	case bridge.revokeRemoteSubscriptions(ski):
	case !allowed:
		bridge.suspendRemoteSubscriptions(ski)
	case payload.EventType == spineapi.EventTypeDataChange && payload.CmdClassifier != nil &&
		*payload.CmdClassifier == spinemodel.CmdClassifierTypeNotify &&
		!isNilRawRuntimeValue(payload.Feature) && payload.Function != "":
		bridge.recordRemoteNotify(ski, payload.Feature, payload.Function)
	}
}

func (bridge *rawFeatureRuntimeBridge) withdrawRemoteSubscriptions(ski string) {
	if !bridge.revokeRemoteSubscriptions(ski) {
		bridge.suspendRemoteSubscriptions(ski)
	}
}

func (bridge *rawFeatureRuntimeBridge) suspendRemoteSubscriptions(ski string) {
	bridge.mu.Lock()
	state := &bridge.remoteSubscriptions
	state.initLocked()
	for _, entry := range state.active {
		if entry.lease.ski == ski {
			bridge.dropRemoteSubscriptionLocked(entry)
		}
	}
	state.down[ski] = struct{}{}
	state.dirty[ski] = struct{}{}
	bridge.mu.Unlock()
	bridge.runRemoteSubscriptionActions()
}

func (bridge *rawFeatureRuntimeBridge) revokeRemoteSubscriptions(ski string) bool {
	bridge.mu.Lock()
	revoked := bridge.remoteSubscriptions.revoked
	bridge.mu.Unlock()
	if revoked == nil || !revoked(ski) {
		return false
	}
	bridge.mu.Lock()
	state := &bridge.remoteSubscriptions
	state.initLocked()
	for _, entry := range state.active {
		if entry.lease.ski == ski {
			bridge.dropRemoteSubscriptionLocked(entry)
		}
	}
	state.revoke[ski] = struct{}{}
	state.dirty[ski] = struct{}{}
	bridge.mu.Unlock()
	bridge.runRemoteSubscriptionActions()
	return true
}

func (bridge *rawFeatureRuntimeBridge) forgetRemoteSubscriptions(ski string, device spineapi.DeviceRemoteInterface) {
	bridge.mu.Lock()
	state := &bridge.remoteSubscriptions
	state.initLocked()
	for _, entry := range state.active {
		if entry.lease.ski == ski &&
			(isNilRawRuntimeValue(device) || sameRawRuntimeValue(entry.lease.remote, device)) {
			bridge.dropRemoteSubscriptionLocked(entry)
		}
	}
	if lease := state.up[ski]; lease != nil &&
		(isNilRawRuntimeValue(device) || sameRawRuntimeValue(lease.remote, device)) {
		state.down[ski] = struct{}{}
		state.dirty[ski] = struct{}{}
	}
	bridge.mu.Unlock()
	bridge.runRemoteSubscriptionActions()
}

func rawLeaseFeatures(lease *rawRemoteLease) []spineapi.FeatureRemoteInterface {
	var features []spineapi.FeatureRemoteInterface
	for _, entity := range lease.remote.Entities() {
		if isNilRawRuntimeValue(entity) {
			continue
		}
		for _, feature := range entity.Features() {
			if isNilRawRuntimeValue(feature) || feature.Address() == nil {
				continue
			}
			if current := lease.remote.FeatureByAddress(feature.Address()); !isNilRawRuntimeValue(current) {
				features = append(features, current)
			}
		}
	}
	return features
}

func remoteSubscriptionReadable(feature spineapi.FeatureRemoteInterface) bool {
	if _, listed := remoteSubscriptionFeatureTypes[feature.Type()]; !listed ||
		feature.Role() != spinemodel.RoleTypeServer {
		return false
	}
	address := feature.Address()
	if address == nil || address.Device == nil || address.Feature == nil || len(address.Entity) == 0 {
		return false
	}
	for _, operations := range feature.Operations() {
		if operations != nil && operations.Read() {
			return true
		}
	}
	return false
}

func remoteSubscriptionKey(ski string, feature spineapi.FeatureRemoteInterface) string {
	return ski + "\x00" + feature.Address().String() + "\x00" + string(feature.Type())
}

func remoteNotifiedKey(target eebusraw.FeatureTargetV1) string {
	return rawFeatureLocatorKey(target.Locator()) + "\x00" + target.Function
}

func (bridge *rawFeatureRuntimeBridge) planRemoteSubscriptionsLocked(
	lease *rawRemoteLease,
	features []spineapi.FeatureRemoteInterface,
) {
	state := &bridge.remoteSubscriptions
	state.initLocked()
	if state.session == nil {
		return
	}
	trusted := bridge.remoteSubscriptionTrustedLocked(lease.ski)
	wanted := make(map[string]spineapi.FeatureRemoteInterface)
	for _, feature := range features {
		if trusted && remoteSubscriptionReadable(feature) {
			wanted[remoteSubscriptionKey(lease.ski, feature)] = feature
		}
	}
	for key, entry := range state.active {
		if entry.lease.ski != lease.ski {
			continue
		}
		if _, keep := wanted[key]; !keep || entry.lease != lease {
			bridge.dropRemoteSubscriptionLocked(entry)
		}
	}
	keys := make([]string, 0, len(wanted))
	for key := range wanted {
		if _, exists := state.active[key]; !exists {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		feature := wanted[key]
		state.active[key] = &remoteSubscriptionEntry{
			key:         key,
			lease:       lease,
			address:     cloneRawFeatureAddress(*feature.Address()),
			featureType: feature.Type(),
		}
	}
	state.dirty[lease.ski] = struct{}{}
}

func (bridge *rawFeatureRuntimeBridge) dropRemoteSubscriptionsLocked(lease *rawRemoteLease) {
	state := &bridge.remoteSubscriptions
	state.initLocked()
	for _, entry := range state.active {
		if entry.lease == lease {
			bridge.dropRemoteSubscriptionLocked(entry)
		}
	}
	if state.up[lease.ski] == lease {
		state.down[lease.ski] = struct{}{}
		state.dirty[lease.ski] = struct{}{}
	}
}

func (bridge *rawFeatureRuntimeBridge) dropRemoteSubscriptionLocked(entry *remoteSubscriptionEntry) {
	state := &bridge.remoteSubscriptions
	delete(state.active, entry.key)
	for key, data := range state.notified {
		if data.entry == entry {
			delete(state.notified, key)
		}
	}
}

func (bridge *rawFeatureRuntimeBridge) runRemoteSubscriptionActions() {
	state := &bridge.remoteSubscriptions
	state.io.Lock()
	defer state.io.Unlock()
	bridge.mu.Lock()
	state.initLocked()
	instance := state.session
	revoked := state.revoked
	skis := make([]string, 0, len(state.dirty))
	for ski := range state.dirty {
		skis = append(skis, ski)
	}
	sort.Strings(skis)
	work := make([]remoteSubscriptionWork, 0, len(skis))
	for _, ski := range skis {
		item := remoteSubscriptionWork{ski: ski, trusted: bridge.remoteSubscriptionTrustedLocked(ski)}
		_, item.revoke = state.revoke[ski]
		_, item.down = state.down[ski]
		if lease := bridge.leasesBySKI[ski]; lease != nil && !lease.retired {
			item.lease = lease
		}
		keys := make([]string, 0)
		for key, entry := range state.active {
			if entry.lease.ski == ski {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			item.entries = append(item.entries, state.active[key])
		}
		delete(state.revoke, ski)
		delete(state.down, ski)
		delete(state.dirty, ski)
		work = append(work, item)
	}
	bridge.mu.Unlock()
	if instance == nil {
		return
	}
	for _, item := range work {
		if revoked != nil && revoked(item.ski) {
			item.revoke = true
		}
		bridge.applyRemoteSubscriptionWork(instance, item)
	}
	sweepRevokedRemoteSubscriptions(instance, revoked)
}

// sweepRevokedRemoteSubscriptions removes the retained feature pairs of every remote whose trust was
// explicitly revoked. A remote revoked while it was disconnected sends no event, so the pairs restored
// from the stored state are checked against the revocation record here instead of waiting for one.
func sweepRevokedRemoteSubscriptions(instance *session.Instance, revoked func(string) bool) {
	if instance == nil || revoked == nil {
		return
	}
	last := ""
	for _, record := range instance.Established() {
		if record.Peer == last {
			continue
		}
		last = record.Peer
		if revoked(record.Peer) {
			_ = instance.Trust(record.Peer, false)
		}
	}
}

func (bridge *rawFeatureRuntimeBridge) applyRemoteSubscriptionWork(
	instance *session.Instance,
	item remoteSubscriptionWork,
) {
	state := &bridge.remoteSubscriptions
	disconnect := func() {
		bridge.mu.Lock()
		connected := state.up[item.ski] != nil
		delete(state.up, item.ski)
		bridge.mu.Unlock()
		if connected {
			_ = instance.Connect(item.ski, false)
			state.forgetSentCounters(item.ski)
			state.forgetHeld(item.ski)
		}
	}
	if item.down || item.lease == nil {
		disconnect()
	}
	if item.revoke {
		_ = instance.Trust(item.ski, false)
		bridge.mu.Lock()
		delete(state.told, item.ski)
		bridge.mu.Unlock()
	}
	if item.revoke || item.lease == nil || !item.trusted {
		disconnect()
		return
	}
	bridge.mu.Lock()
	told := state.told[item.ski]
	state.told[item.ski] = true
	bridge.mu.Unlock()
	if !told {
		_ = instance.Trust(item.ski, true)
	}
	bridge.mu.Lock()
	linked := state.up[item.ski] == item.lease
	state.up[item.ski] = item.lease
	bridge.mu.Unlock()
	if !linked {
		state.forgetHeld(item.ski)
		_ = instance.Connect(item.ski, true)
	}
	bridge.prepareRemoteSubscriptionClients(item.entries)
	pairs := make([]session.Pair, 0, len(item.entries))
	for _, entry := range item.entries {
		client, _ := remoteSubscriptionClient(bridge.local, entry.featureType, false)
		if client == nil || client.Address() == nil {
			continue
		}
		pairs = append(pairs, session.Pair{
			Client:            cloneRawFeatureAddress(*client.Address()),
			Server:            cloneRawFeatureAddress(entry.address),
			ServerFeatureType: entry.featureType,
		})
	}
	_ = instance.Select(item.ski, pairs)
}

func (bridge *rawFeatureRuntimeBridge) prepareRemoteSubscriptionClients(entries []*remoteSubscriptionEntry) {
	created := false
	for _, entry := range entries {
		if _, added := remoteSubscriptionClient(bridge.local, entry.featureType, true); added {
			created = true
		}
	}
	if created {
		announceRemoteSubscriptionClients(bridge.local)
	}
	if isNilRawRuntimeValue(bridge.local) {
		return
	}
	management := bridge.local.NodeManagement()
	if isNilRawRuntimeValue(management) {
		return
	}
	bridge.mu.Lock()
	watched := sameRawRuntimeValue(bridge.remoteSubscriptions.results, management)
	bridge.remoteSubscriptions.results = management
	bridge.mu.Unlock()
	if !watched {
		management.AddResultCallback(bridge.recordRemoteSubscriptionResult)
	}
}

func (bridge *rawFeatureRuntimeBridge) recordRemoteSubscriptionResult(message spineapi.ResponseMessage) {
	if isNilRawRuntimeValue(message.DeviceRemote) || message.DeviceRemote.Address() == nil {
		return
	}
	result, _ := message.Data.(*spinemodel.ResultDataType)
	refused := result != nil && result.ErrorNumber != nil &&
		*result.ErrorNumber != spinemodel.ErrorNumberTypeNoError
	bridge.mu.Lock()
	lease := bridge.leasesByAddr[*message.DeviceRemote.Address()]
	instance := bridge.remoteSubscriptions.session
	bridge.mu.Unlock()
	if lease == nil || instance == nil {
		return
	}
	_ = instance.NoteResult(lease.ski, uint64(message.MsgCounterReference), !refused)
}

func remoteSubscriptionClient(
	local spineapi.DeviceLocalInterface,
	featureType spinemodel.FeatureTypeType,
	create bool,
) (spineapi.FeatureLocalInterface, bool) {
	if isNilRawRuntimeValue(local) {
		return nil, false
	}
	cem := local.EntityForType(spinemodel.EntityTypeTypeCEM)
	if isNilRawRuntimeValue(cem) || cem.EntityType() != spinemodel.EntityTypeTypeCEM {
		return nil, false
	}
	client := cem.FeatureOfTypeAndRole(featureType, spinemodel.RoleTypeClient)
	if !isNilRawRuntimeValue(client) {
		if client.Type() != featureType || client.Role() != spinemodel.RoleTypeClient {
			return nil, false
		}
		return client, false
	}
	if !create {
		return nil, false
	}
	id := cem.NextFeatureId()
	for _, existing := range cem.Features() {
		if isNilRawRuntimeValue(existing) || existing.Address() == nil ||
			existing.Address().Feature == nil || uint(*existing.Address().Feature) == id {
			return nil, false
		}
	}
	created := spine.NewFeatureLocal(id, cem, featureType, spinemodel.RoleTypeClient)
	created.SetDescriptionString(string(featureType) + " Client")
	cem.AddFeature(created)
	return created, true
}

// announceRemoteSubscriptionClients notifies the subscribers of the host's node management with the
// CEM entity and its current feature list, in the partial detailed-discovery form the host also uses
// when it adds an entity.
func announceRemoteSubscriptionClients(local spineapi.DeviceLocalInterface) {
	cem := local.EntityForType(spinemodel.EntityTypeTypeCEM)
	management := local.NodeManagement()
	if isNilRawRuntimeValue(cem) || isNilRawRuntimeValue(management) || management.Address() == nil {
		return
	}
	entity := cem.Information()
	if entity == nil || entity.Description == nil {
		return
	}
	description := *entity.Description
	added := spinemodel.NetworkManagementStateChangeTypeAdded
	description.LastStateChange = &added
	entity.Description = &description
	var features []spinemodel.NodeManagementDetailedDiscoveryFeatureInformationType
	for _, feature := range cem.Features() {
		if isNilRawRuntimeValue(feature) {
			continue
		}
		if information := feature.Information(); information != nil {
			features = append(features, *information)
		}
	}
	function := spinemodel.FunctionTypeNodeManagementDetailedDiscoveryData
	local.NotifySubscribers(management.Address(), spinemodel.CmdType{
		Function: &function,
		Filter:   []spinemodel.FilterType{*spinemodel.NewFilterTypePartial()},
		NodeManagementDetailedDiscoveryData: &spinemodel.NodeManagementDetailedDiscoveryDataType{
			SpecificationVersionList: &spinemodel.NodeManagementSpecificationVersionListType{
				SpecificationVersion: []spinemodel.SpecificationVersionDataType{
					spinemodel.SpecificationVersionDataType(spine.SpecificationVersion),
				},
			},
			DeviceInformation:  local.Information(),
			EntityInformation:  []spinemodel.NodeManagementDetailedDiscoveryEntityInformationType{*entity},
			FeatureInformation: features,
		},
	})
}
