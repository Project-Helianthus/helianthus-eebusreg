package eebusfacade

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

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
	return nil
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
	state := &bridge.remoteSubscriptions
	state.io.Lock()
	defer state.io.Unlock()
	bridge.mu.Lock()
	instance := state.session
	state.session = nil
	bridge.mu.Unlock()
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
		return err == nil && len(remote) == 20 && !coordinator.trusted(remote)
	}
	bridge.mu.Unlock()
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

func (remoteSubscriptionTransport) PeerHeld(string) ([]session.Pair, bool) {
	return nil, false
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

type remoteNotifiedData struct {
	entry      *remoteSubscriptionEntry
	target     eebusraw.FeatureTargetV1
	runtime    eebusraw.RuntimeBindingV1
	value      eebusraw.TypedValueV1
	failure    *eebusraw.ErrorV1
	notifiedAt time.Time
}

func (backend *serviceBackend) FeatureNotifiedDataGet(
	_ context.Context,
	auth eebusraw.ReadAuthorizationV1,
	target eebusraw.FeatureTargetV1,
) (eebusraw.ReadObservationV1, *eebusraw.ErrorV1) {
	if backend == nil || backend.rawFeatures == nil {
		return eebusraw.ReadObservationV1{}, eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1Disconnected,
			"raw feature runtime is unavailable",
			true,
			eebusraw.SourceLayerV1Runtime,
		)
	}
	trusted := func(string) bool { return true }
	if backend.handler != nil {
		trusted = backend.handler.remoteLivenessAllowed
	}
	return backend.rawFeatures.featureNotifiedDataGet(auth, target, trusted)
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

func (bridge *rawFeatureRuntimeBridge) recordRemoteNotify(
	ski string,
	feature spineapi.FeatureRemoteInterface,
	function spinemodel.FunctionType,
) {
	if isNilRawRuntimeValue(feature) || feature.Address() == nil {
		return
	}
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	lease := bridge.leasesBySKI[ski]
	if lease == nil || lease.retired || bridge.currentRuntimeEpoch() != lease.runtimeEpoch {
		return
	}
	entry := bridge.remoteSubscriptions.active[remoteSubscriptionKey(lease.ski, feature)]
	current := lease.remote.FeatureByAddress(feature.Address())
	if entry == nil || entry.lease != lease || !sameRawRuntimeValue(current, feature) ||
		current.Operations()[function] == nil {
		return
	}
	var failure *eebusraw.ErrorV1
	value, err := remoteNotifiedValue(current, function)
	if err != nil {
		value = eebusraw.TypedValueV1{}
		failure = remoteNotifiedDecodeError()
	}
	inventory, err := bridge.inventoryForFeatureLocked(lease, current)
	if err != nil {
		// The feature cannot be represented in the inventory, so the notification is kept as a
		// failure under the feature's own address rather than read as missing.
		locator, complete := remoteNotifiedLocator(lease, current)
		if !complete {
			return
		}
		inventory = eebusraw.FeaturesGetDataV1{
			Feature: locator,
			Runtime: eebusraw.RuntimeBindingV1{
				RuntimeEpoch:         lease.runtimeEpoch,
				ConnectionGeneration: uint64(lease.generation),
			},
		}
		value = eebusraw.TypedValueV1{}
		failure = eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1DecodeError,
			"notified raw feature could not be represented in the feature inventory",
			false,
			eebusraw.SourceLayerV1Decode,
		)
	}
	locator := inventory.Feature
	target := eebusraw.FeatureTargetV1{
		RemoteSKI:      locator.RemoteSKI,
		SHIPID:         locator.SHIPID,
		DeviceAddress:  locator.DeviceAddress,
		EntityAddress:  locator.EntityAddress,
		FeatureAddress: locator.FeatureAddress,
		FeatureType:    locator.FeatureType,
		FeatureRole:    locator.FeatureRole,
		Function:       string(function),
		Operation:      eebusraw.OperationV1Read,
	}
	bridge.remoteSubscriptions.notified[remoteNotifiedKey(target)] = remoteNotifiedData{
		entry:      entry,
		target:     target,
		runtime:    inventory.Runtime,
		value:      value,
		failure:    failure,
		notifiedAt: bridge.timestamp(),
	}
	bridge.reportRemoteNotifyLocked(lease, entry, function, value)
}

func (bridge *rawFeatureRuntimeBridge) reportRemoteNotifyLocked(
	lease *rawRemoteLease,
	entry *remoteSubscriptionEntry,
	function spinemodel.FunctionType,
	value eebusraw.TypedValueV1,
) {
	instance := bridge.remoteSubscriptions.session
	if instance == nil {
		return
	}
	client, _ := remoteSubscriptionClient(bridge.local, entry.featureType, false)
	if client == nil || client.Address() == nil {
		return
	}
	instance.ReceiveNotify(lease.ski, session.Pair{
		Client:            cloneRawFeatureAddress(*client.Address()),
		Server:            cloneRawFeatureAddress(entry.address),
		ServerFeatureType: entry.featureType,
	}, function, value, bridge.timestamp())
}

// remoteNotifiedLocator identifies a subscribed feature on its lease. A feature whose address is
// incomplete has no locator.
func remoteNotifiedLocator(
	lease *rawRemoteLease,
	feature spineapi.FeatureRemoteInterface,
) (eebusraw.FeatureLocatorV1, bool) {
	address := feature.Address()
	if address == nil || address.Device == nil || address.Feature == nil || len(address.Entity) == 0 {
		return eebusraw.FeatureLocatorV1{}, false
	}
	entity := make([]uint64, len(address.Entity))
	for index, part := range address.Entity {
		entity[index] = uint64(part)
	}
	return eebusraw.FeatureLocatorV1{
		RemoteSKI:      lease.ski,
		SHIPID:         lease.shipID,
		DeviceAddress:  string(*address.Device),
		EntityAddress:  entity,
		FeatureAddress: uint64(*address.Feature),
		FeatureType:    string(feature.Type()),
		FeatureRole:    eebusraw.FeatureRoleV1(feature.Role()),
	}, true
}

// remoteNotifiedDecodeError answers a read whose last notification arrived but could not be
// represented as a typed value, so it is not mistaken for a missing notification.
func remoteNotifiedDecodeError() *eebusraw.ErrorV1 {
	return eebusraw.NewErrorV1(
		eebusraw.ErrorCodeV1DecodeError,
		"notified raw feature data contained no complete typed data",
		false,
		eebusraw.SourceLayerV1Decode,
	)
}

var errRemoteNotifiedEncode = errors.New("notified raw feature data could not be encoded")

// remoteNotifiedValue encodes a stable copy of one function's current data. A failure while
// copying or encoding is returned as an error so the SPINE event handler keeps running.
func remoteNotifiedValue(
	feature spineapi.FeatureRemoteInterface,
	function spinemodel.FunctionType,
) (value eebusraw.TypedValueV1, err error) {
	defer func() {
		if recover() != nil {
			value, err = eebusraw.TypedValueV1{}, errRemoteNotifiedEncode
		}
	}()
	data := remoteNotifiedSnapshot(feature, function)
	if isNilRawRuntimeValue(data) {
		return eebusraw.TypedValueV1{}, errRawTypedEmpty
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return eebusraw.TypedValueV1{}, err
	}
	return eebusraw.DecodeTypedValueV1(encoded)
}

// remoteNotifiedSnapshot returns one function's current data in memory no later notify writes.
// The feature's data copy shares its item list with the stored data, and a partial notify rewrites
// stored items in place. For a list function the items are therefore copied by a merge that is not
// persisted: spine-go builds it into a new list, ordered by item key, while it holds the function's
// lock. Any other function is only ever replaced whole, so its data copy is already stable.
func remoteNotifiedSnapshot(feature spineapi.FeatureRemoteInterface, function spinemodel.FunctionType) any {
	data := feature.DataCopy(function)
	if isNilRawRuntimeValue(data) {
		return nil
	}
	if _, list := data.(spinemodel.Updater); !list {
		return data
	}
	copied := reflect.ValueOf(data)
	if copied.Kind() != reflect.Pointer || copied.Elem().Kind() != reflect.Struct {
		return nil
	}
	merged, failure := feature.UpdateData(false, function, reflect.New(copied.Type().Elem()).Interface(), nil, nil)
	if failure != nil || merged == nil {
		return nil
	}
	items := reflect.ValueOf(merged)
	if items.Kind() != reflect.Slice {
		return nil
	}
	document := copied.Elem()
	for index := 0; index < document.NumField(); index++ {
		if field := document.Field(index); field.Type() == items.Type() && field.CanSet() {
			field.Set(items)
			return data
		}
	}
	return nil
}

func (bridge *rawFeatureRuntimeBridge) featureNotifiedDataGet(
	auth eebusraw.ReadAuthorizationV1,
	target eebusraw.FeatureTargetV1,
	trusted func(string) bool,
) (eebusraw.ReadObservationV1, *eebusraw.ErrorV1) {
	if terminal := eebusraw.ValidateReadAuthorizationV1(auth, eebusraw.ToolV1FeaturesDataGet); terminal != nil {
		return eebusraw.ReadObservationV1{}, terminal
	}
	if terminal := eebusraw.ValidateFeatureDataGetRequestV1(eebusraw.FeatureDataGetRequestV1{
		Targets: []eebusraw.FeatureTargetV1{target},
	}); terminal != nil {
		return eebusraw.ReadObservationV1{}, terminal
	}
	notAvailable := eebusraw.NewErrorV1(
		eebusraw.ErrorCodeV1NotFound,
		"notified raw feature data is not available",
		true,
		eebusraw.SourceLayerV1Runtime,
	)
	if ski := strings.ToLower(strings.TrimSpace(target.RemoteSKI)); trusted == nil || !trusted(ski) {
		bridge.withdrawRemoteSubscriptions(ski)
		return eebusraw.ReadObservationV1{}, notAvailable
	}
	bridge.mu.Lock()
	state := &bridge.remoteSubscriptions
	stored, found := state.notified[remoteNotifiedKey(target)]
	available := found && state.active[stored.entry.key] == stored.entry &&
		!stored.entry.lease.retired &&
		bridge.leasesBySKI[stored.entry.lease.ski] == stored.entry.lease
	var remote spineapi.DeviceRemoteInterface
	if available {
		remote = stored.entry.lease.remote
	}
	stale := available && bridge.currentRuntimeEpoch() != stored.runtime.RuntimeEpoch
	bridge.mu.Unlock()
	if !available {
		return eebusraw.ReadObservationV1{}, notAvailable
	}
	if stale {
		return eebusraw.ReadObservationV1{}, eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1RuntimeEpochMismatch,
			"runtime epoch changed after the raw feature notification",
			true,
			eebusraw.SourceLayerV1Runtime,
		)
	}
	ski := stored.entry.lease.ski
	if !isNilRawRuntimeValue(bridge.local) {
		if present := bridge.local.RemoteDeviceForSki(ski); !sameRawRuntimeValue(present, remote) {
			bridge.forgetRemoteSubscriptions(ski, remote)
			return eebusraw.ReadObservationV1{}, notAvailable
		}
	}
	if stored.failure != nil {
		failure := stored.failure.Clone()
		return eebusraw.ReadObservationV1{}, &failure
	}
	value := stored.value.Clone()
	observation := eebusraw.ReadObservationV1{
		Target:  stored.target.Clone(),
		Runtime: stored.runtime,
		RawResponse: eebusraw.ProtocolMessageV1{
			Classifier: strings.ToUpper(string(spinemodel.CmdClassifierTypeNotify)),
			Function:   stored.target.Function,
			Data:       &value,
		},
		Value:         stored.value.Clone(),
		RequestedAt:   stored.notifiedAt,
		ReceivedAt:    stored.notifiedAt,
		DataTimestamp: stored.notifiedAt,
		Source:        eebusraw.ObservationSourceV1Live,
	}
	hash, err := observation.ComputeDataHash()
	if err != nil {
		return eebusraw.ReadObservationV1{}, rawInternalError()
	}
	observation.DataHash = hash
	return observation.Clone(), nil
}
