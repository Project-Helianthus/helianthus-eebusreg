package eebusfacade

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

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

// remoteSubscriptionRefusedLimit bounds the refusals kept for requests whose message counter is not
// registered yet; a result can arrive before the request that caused it is registered.
const remoteSubscriptionRefusedLimit = 64

type remoteSubscriptionState struct {
	io       sync.Mutex
	active   map[string]*remoteSubscriptionEntry
	notified map[string]remoteNotifiedData
	queue    []remoteSubscriptionAction
	pending  map[remoteSubscriptionRequest]*remoteSubscriptionEntry
	// unsubscribing holds the unsubscribe requests whose result has not arrived; a refusal sends
	// the unsubscribe again at the next run.
	unsubscribing map[remoteSubscriptionRequest]*remoteSubscriptionEntry
	refused       map[remoteSubscriptionRequest]struct{}
	sent          map[string]*remoteSubscriptionSent
	results       spineapi.NodeManagementInterface
	trusted       func(string) bool
}

// remoteSubscriptionSent tracks, per subscribed feature and connection, the message counters of the
// last subscribe and unsubscribe requests and whether the last request that actually left the host
// was a subscribe. The SPINE sender answers a request that repeats an unanswered one with the earlier
// counter and sends nothing, so a returned counter equal to the last one marks such a replay.
type remoteSubscriptionSent struct {
	lease       *rawRemoteLease
	subscribe   *spinemodel.MsgCounterType
	unsubscribe *spinemodel.MsgCounterType
	subscribed  bool
}

// remoteSubscriptionRequest identifies one subscription request by the responder device and the
// message counter the result refers to.
type remoteSubscriptionRequest struct {
	device  spinemodel.AddressDeviceType
	counter spinemodel.MsgCounterType
}

func (state *remoteSubscriptionState) initLocked() {
	if state.active == nil {
		state.active = make(map[string]*remoteSubscriptionEntry)
		state.notified = make(map[string]remoteNotifiedData)
		state.pending = make(map[remoteSubscriptionRequest]*remoteSubscriptionEntry)
		state.unsubscribing = make(map[remoteSubscriptionRequest]*remoteSubscriptionEntry)
		state.refused = make(map[remoteSubscriptionRequest]struct{})
		state.sent = make(map[string]*remoteSubscriptionSent)
	}
}

// sentLocked returns the request record of an entry's feature on the entry's connection, starting a
// new one when the connection changed.
func (state *remoteSubscriptionState) sentLocked(entry *remoteSubscriptionEntry) *remoteSubscriptionSent {
	state.initLocked()
	sent := state.sent[entry.key]
	if sent == nil || sent.lease != entry.lease {
		sent = &remoteSubscriptionSent{lease: entry.lease}
		state.sent[entry.key] = sent
	}
	return sent
}

func sameRemoteSubscriptionCounter(last, counter *spinemodel.MsgCounterType) bool {
	return last != nil && counter != nil && *last == *counter
}

type remoteSubscriptionEntry struct {
	key         string
	lease       *rawRemoteLease
	address     spinemodel.FeatureAddressType
	featureType spinemodel.FeatureTypeType
}

type remoteSubscriptionAction struct {
	entry     *remoteSubscriptionEntry
	subscribe bool
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

// routeRemoteNotify keeps the subscription store in step with SPINE events: a remote that disappears
// loses its subscriptions and notified data, any event for a remote whose trust is withdrawn removes
// its subscriptions and notified data, and a NOTIFY from a trusted remote updates the stored data.
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
	case !allowed:
		bridge.withdrawRemoteSubscriptions(ski)
	case payload.EventType == spineapi.EventTypeDataChange && payload.CmdClassifier != nil &&
		*payload.CmdClassifier == spinemodel.CmdClassifierTypeNotify &&
		!isNilRawRuntimeValue(payload.Feature) && payload.Function != "":
		bridge.recordRemoteNotify(ski, payload.Feature, payload.Function)
	}
}

// withdrawRemoteSubscriptions removes the subscriptions and notified data of a remote that is no
// longer trusted and asks the responder to delete each subscription.
func (bridge *rawFeatureRuntimeBridge) withdrawRemoteSubscriptions(ski string) {
	bridge.mu.Lock()
	queued := false
	for _, entry := range bridge.remoteSubscriptions.active {
		if entry.lease.ski == ski {
			bridge.dropRemoteSubscriptionLocked(entry, true)
			queued = true
		}
	}
	bridge.mu.Unlock()
	if queued {
		bridge.runRemoteSubscriptionActions()
	}
}

// forgetRemoteSubscriptions removes the subscriptions and notified data of a remote device that
// disappeared. No request is sent because the responder is gone.
func (bridge *rawFeatureRuntimeBridge) forgetRemoteSubscriptions(ski string, device spineapi.DeviceRemoteInterface) {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	for _, entry := range bridge.remoteSubscriptions.active {
		if entry.lease.ski == ski &&
			(isNilRawRuntimeValue(device) || sameRawRuntimeValue(entry.lease.remote, device)) {
			bridge.dropRemoteSubscriptionLocked(entry, false)
		}
	}
	for key, sent := range bridge.remoteSubscriptions.sent {
		if sent.lease.ski == ski &&
			(isNilRawRuntimeValue(device) || sameRawRuntimeValue(sent.lease.remote, device)) {
			delete(bridge.remoteSubscriptions.sent, key)
		}
	}
	for request, entry := range bridge.remoteSubscriptions.unsubscribing {
		if entry.lease.ski == ski &&
			(isNilRawRuntimeValue(device) || sameRawRuntimeValue(entry.lease.remote, device)) {
			delete(bridge.remoteSubscriptions.unsubscribing, request)
		}
	}
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
			bridge.dropRemoteSubscriptionLocked(entry, entry.lease == lease)
		}
	}
	for key, sent := range state.sent {
		if sent.lease.ski == lease.ski && sent.lease != lease {
			delete(state.sent, key)
		}
	}
	for request, entry := range state.unsubscribing {
		if entry.lease.ski == lease.ski && entry.lease != lease {
			delete(state.unsubscribing, request)
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
		entry := &remoteSubscriptionEntry{
			key:         key,
			lease:       lease,
			address:     cloneRawFeatureAddress(*feature.Address()),
			featureType: feature.Type(),
		}
		state.active[key] = entry
		state.queue = append(state.queue, remoteSubscriptionAction{entry: entry, subscribe: true})
	}
}

func (bridge *rawFeatureRuntimeBridge) dropRemoteSubscriptionsLocked(lease *rawRemoteLease) {
	for _, entry := range bridge.remoteSubscriptions.active {
		if entry.lease == lease {
			bridge.dropRemoteSubscriptionLocked(entry, false)
		}
	}
	for key, sent := range bridge.remoteSubscriptions.sent {
		if sent.lease == lease {
			delete(bridge.remoteSubscriptions.sent, key)
		}
	}
	for request, entry := range bridge.remoteSubscriptions.unsubscribing {
		if entry.lease == lease {
			delete(bridge.remoteSubscriptions.unsubscribing, request)
		}
	}
}

func (bridge *rawFeatureRuntimeBridge) dropRemoteSubscriptionLocked(
	entry *remoteSubscriptionEntry,
	unsubscribe bool,
) {
	state := &bridge.remoteSubscriptions
	delete(state.active, entry.key)
	for key, data := range state.notified {
		if data.entry == entry {
			delete(state.notified, key)
		}
	}
	for request, pending := range state.pending {
		if pending == entry {
			delete(state.pending, request)
		}
	}
	if unsubscribe {
		state.queue = append(state.queue, remoteSubscriptionAction{entry: entry})
	}
}

func (bridge *rawFeatureRuntimeBridge) runRemoteSubscriptionActions() {
	state := &bridge.remoteSubscriptions
	state.io.Lock()
	defer state.io.Unlock()
	bridge.mu.Lock()
	actions := state.queue
	state.queue = nil
	bridge.mu.Unlock()
	if len(actions) == 0 {
		return
	}
	bridge.prepareRemoteSubscriptionClients(actions)
	var retry []remoteSubscriptionAction
	for _, action := range actions {
		if !action.subscribe {
			bridge.mu.Lock()
			due := bridge.remoteUnsubscribeDueLocked(action.entry)
			bridge.mu.Unlock()
			if !due {
				continue
			}
			counter, done := unsubscribeRemoteFeature(bridge.local, action.entry)
			if !done {
				retry = append(retry, action)
				continue
			}
			bridge.mu.Lock()
			bridge.noteRemoteUnsubscribeLocked(action.entry, counter)
			bridge.mu.Unlock()
			continue
		}
		bridge.mu.Lock()
		current := state.active[action.entry.key] == action.entry && !action.entry.lease.retired
		if current && !bridge.remoteSubscriptionTrustedLocked(action.entry.lease.ski) {
			bridge.dropRemoteSubscriptionLocked(action.entry, false)
			current = false
		}
		bridge.mu.Unlock()
		if !current {
			continue
		}
		counter, err := subscribeRemoteFeature(bridge.local, action.entry)
		bridge.mu.Lock()
		bridge.registerRemoteSubscriptionLocked(action.entry, counter, err)
		bridge.mu.Unlock()
	}
	bridge.mu.Lock()
	clear(state.refused)
	// An unsubscribe that could not be sent runs again, ahead of anything queued meanwhile, at the
	// next run.
	state.queue = append(retry, state.queue...)
	bridge.mu.Unlock()
}

// remoteUnsubscribeDueLocked reports whether an unsubscribe still has to reach the responder: the
// connection that carried the subscription is live and the feature has not been subscribed again.
func (bridge *rawFeatureRuntimeBridge) remoteUnsubscribeDueLocked(entry *remoteSubscriptionEntry) bool {
	return !entry.lease.retired && bridge.remoteSubscriptions.active[entry.key] == nil
}

// prepareRemoteSubscriptionClients creates the local client features the queued subscriptions need,
// announces new ones before any request is sent so a responder knows the client address when it
// handles the request, and watches the subscription results.
func (bridge *rawFeatureRuntimeBridge) prepareRemoteSubscriptionClients(actions []remoteSubscriptionAction) {
	created := false
	for _, action := range actions {
		if !action.subscribe {
			continue
		}
		if _, added := remoteSubscriptionClient(bridge.local, action.entry.featureType, true); added {
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

// noteRemoteUnsubscribeLocked records an unsubscribe request and keeps it until its result arrives.
// One that left the host cancels any earlier subscribe request still unanswered; a replay of an
// unanswered unsubscribe sent nothing. A refusal that already arrived sends it again at the next run.
func (bridge *rawFeatureRuntimeBridge) noteRemoteUnsubscribeLocked(
	entry *remoteSubscriptionEntry,
	counter *spinemodel.MsgCounterType,
) {
	if counter == nil || entry.lease.retired {
		return
	}
	state := &bridge.remoteSubscriptions
	sent := state.sentLocked(entry)
	if !sameRemoteSubscriptionCounter(sent.unsubscribe, counter) {
		value := *counter
		sent.unsubscribe = &value
		sent.subscribed = false
	}
	if entry.address.Device == nil {
		return
	}
	request := remoteSubscriptionRequest{device: *entry.address.Device, counter: *counter}
	if _, refused := state.refused[request]; refused {
		delete(state.refused, request)
		state.queue = append(state.queue, remoteSubscriptionAction{entry: entry})
		return
	}
	state.unsubscribing[request] = entry
}

// noteRemoteSubscribeLocked records a subscribe request that returned a message counter, unless it
// only replayed the last one.
func (state *remoteSubscriptionState) noteRemoteSubscribeLocked(
	entry *remoteSubscriptionEntry,
	counter *spinemodel.MsgCounterType,
) {
	sent := state.sentLocked(entry)
	if !sameRemoteSubscriptionCounter(sent.subscribe, counter) {
		value := *counter
		sent.subscribe = &value
		sent.subscribed = true
	}
}

// registerRemoteSubscriptionLocked keeps a sent request until its result arrives. A request that
// could not be sent, whose refusal already arrived, or that only replayed an unanswered request an
// unsubscribe has since cancelled, leaves the feature eligible for the next discovery refresh. A
// request whose feature stopped being active while it was sent is still recorded for its connection,
// so the unsubscribe that follows cancels it and a later replay of it is not taken as live.
func (bridge *rawFeatureRuntimeBridge) registerRemoteSubscriptionLocked(
	entry *remoteSubscriptionEntry,
	counter *spinemodel.MsgCounterType,
	err error,
) {
	state := &bridge.remoteSubscriptions
	if state.active[entry.key] != entry {
		if err == nil && counter != nil && !entry.lease.retired &&
			bridge.leasesBySKI[entry.lease.ski] == entry.lease {
			state.noteRemoteSubscribeLocked(entry, counter)
		}
		return
	}
	if err != nil {
		bridge.dropRemoteSubscriptionLocked(entry, false)
		return
	}
	if counter == nil || entry.address.Device == nil {
		return
	}
	if sent := state.sentLocked(entry); sameRemoteSubscriptionCounter(sent.subscribe, counter) && !sent.subscribed {
		bridge.dropRemoteSubscriptionLocked(entry, false)
		return
	}
	state.noteRemoteSubscribeLocked(entry, counter)
	request := remoteSubscriptionRequest{device: *entry.address.Device, counter: *counter}
	if _, refused := state.refused[request]; refused {
		delete(state.refused, request)
		bridge.dropRemoteSubscriptionLocked(entry, false)
		return
	}
	state.pending[request] = entry
}

// recordRemoteSubscriptionResult receives the responder's result for a subscription request. A
// refused subscribe removes the subscription so the next discovery refresh requests it again; a
// refused unsubscribe is sent again at the next run until the responder accepts it or the
// connection is gone.
func (bridge *rawFeatureRuntimeBridge) recordRemoteSubscriptionResult(message spineapi.ResponseMessage) {
	if isNilRawRuntimeValue(message.DeviceRemote) || message.DeviceRemote.Address() == nil {
		return
	}
	result, _ := message.Data.(*spinemodel.ResultDataType)
	refused := result != nil && result.ErrorNumber != nil &&
		*result.ErrorNumber != spinemodel.ErrorNumberTypeNoError
	request := remoteSubscriptionRequest{
		device:  *message.DeviceRemote.Address(),
		counter: message.MsgCounterReference,
	}
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	state := &bridge.remoteSubscriptions
	state.initLocked()
	if entry, found := state.pending[request]; found {
		delete(state.pending, request)
		if refused {
			bridge.dropRemoteSubscriptionLocked(entry, false)
		}
		return
	}
	if entry, found := state.unsubscribing[request]; found {
		delete(state.unsubscribing, request)
		if refused {
			state.queue = append(state.queue, remoteSubscriptionAction{entry: entry})
		}
		return
	}
	if refused && len(state.refused) < remoteSubscriptionRefusedLimit {
		state.refused[request] = struct{}{}
	}
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

func subscribeRemoteFeature(
	local spineapi.DeviceLocalInterface,
	entry *remoteSubscriptionEntry,
) (*spinemodel.MsgCounterType, error) {
	client, _ := remoteSubscriptionClient(local, entry.featureType, true)
	if client == nil {
		return nil, errRemoteSubscriptionUnavailable
	}
	address := cloneRawFeatureAddress(entry.address)
	counter, failure := client.SubscribeToRemote(&address)
	if failure != nil {
		return nil, errRemoteSubscriptionUnavailable
	}
	return counter, nil
}

// unsubscribeRemoteFeature reports whether the responder needs no further request: the unsubscribe
// was sent, with its message counter, or no local client exists, so no subscription was ever
// requested from one.
func unsubscribeRemoteFeature(
	local spineapi.DeviceLocalInterface,
	entry *remoteSubscriptionEntry,
) (*spinemodel.MsgCounterType, bool) {
	client, _ := remoteSubscriptionClient(local, entry.featureType, false)
	if client == nil {
		return nil, true
	}
	address := cloneRawFeatureAddress(entry.address)
	counter, failure := client.RemoveRemoteSubscription(&address)
	return counter, failure == nil
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
