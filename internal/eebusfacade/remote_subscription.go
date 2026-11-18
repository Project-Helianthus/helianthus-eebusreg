package eebusfacade

import (
	"context"
	"encoding/json"
	"errors"
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

type remoteSubscriptionState struct {
	io       sync.Mutex
	active   map[string]*remoteSubscriptionEntry
	notified map[string]remoteNotifiedData
	queue    []remoteSubscriptionAction
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

func (handler *runtimeServiceHandler) routeRemoteNotify(payload spineapi.EventPayload) {
	if payload.EventType != spineapi.EventTypeDataChange || payload.CmdClassifier == nil ||
		*payload.CmdClassifier != spinemodel.CmdClassifierTypeNotify ||
		isNilRawRuntimeValue(payload.Feature) || payload.Function == "" {
		return
	}
	ski := strings.ToLower(strings.TrimSpace(payload.Ski))
	handler.mu.Lock()
	bridge := handler.rawFeatures
	allowed := handler.spineEventsActive && handler.remoteLivenessAllowedLocked(ski)
	handler.mu.Unlock()
	if bridge != nil && allowed {
		bridge.recordRemoteNotify(ski, payload.Feature, payload.Function)
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
	if state.active == nil {
		state.active = make(map[string]*remoteSubscriptionEntry)
		state.notified = make(map[string]remoteNotifiedData)
	}
	wanted := make(map[string]spineapi.FeatureRemoteInterface)
	for _, feature := range features {
		if remoteSubscriptionReadable(feature) {
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
	for _, action := range actions {
		if !action.subscribe {
			unsubscribeRemoteFeature(bridge.local, action.entry)
			continue
		}
		bridge.mu.Lock()
		current := state.active[action.entry.key] == action.entry && !action.entry.lease.retired
		bridge.mu.Unlock()
		if !current || subscribeRemoteFeature(bridge.local, action.entry) == nil {
			continue
		}
		bridge.mu.Lock()
		if state.active[action.entry.key] == action.entry {
			delete(state.active, action.entry.key)
		}
		bridge.mu.Unlock()
	}
}

func remoteSubscriptionClient(
	local spineapi.DeviceLocalInterface,
	featureType spinemodel.FeatureTypeType,
	create bool,
) spineapi.FeatureLocalInterface {
	if isNilRawRuntimeValue(local) {
		return nil
	}
	cem := local.EntityForType(spinemodel.EntityTypeTypeCEM)
	if isNilRawRuntimeValue(cem) || cem.EntityType() != spinemodel.EntityTypeTypeCEM {
		return nil
	}
	client := cem.FeatureOfTypeAndRole(featureType, spinemodel.RoleTypeClient)
	if !isNilRawRuntimeValue(client) {
		if client.Type() != featureType || client.Role() != spinemodel.RoleTypeClient {
			return nil
		}
		return client
	}
	if !create {
		return nil
	}
	id := cem.NextFeatureId()
	for _, existing := range cem.Features() {
		if isNilRawRuntimeValue(existing) || existing.Address() == nil ||
			existing.Address().Feature == nil || uint(*existing.Address().Feature) == id {
			return nil
		}
	}
	created := spine.NewFeatureLocal(id, cem, featureType, spinemodel.RoleTypeClient)
	created.SetDescriptionString(string(featureType) + " Client")
	cem.AddFeature(created)
	return created
}

func subscribeRemoteFeature(local spineapi.DeviceLocalInterface, entry *remoteSubscriptionEntry) error {
	client := remoteSubscriptionClient(local, entry.featureType, true)
	if client == nil {
		return errRemoteSubscriptionUnavailable
	}
	address := cloneRawFeatureAddress(entry.address)
	if _, failure := client.SubscribeToRemote(&address); failure != nil {
		return errRemoteSubscriptionUnavailable
	}
	return nil
}

func unsubscribeRemoteFeature(local spineapi.DeviceLocalInterface, entry *remoteSubscriptionEntry) {
	if client := remoteSubscriptionClient(local, entry.featureType, false); client != nil {
		address := cloneRawFeatureAddress(entry.address)
		_, _ = client.RemoveRemoteSubscription(&address)
	}
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
	inventory, err := bridge.inventoryForFeatureLocked(lease, current)
	if err != nil {
		return
	}
	value, err := remoteNotifiedValue(current.DataCopy(function))
	if err != nil {
		return
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
		notifiedAt: bridge.timestamp(),
	}
}

func remoteNotifiedValue(data any) (eebusraw.TypedValueV1, error) {
	if isNilRawRuntimeValue(data) {
		return eebusraw.TypedValueV1{}, errRawTypedEmpty
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return eebusraw.TypedValueV1{}, err
	}
	return eebusraw.DecodeTypedValueV1(encoded)
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
	bridge.mu.Lock()
	state := &bridge.remoteSubscriptions
	stored, found := state.notified[remoteNotifiedKey(target)]
	available := found && state.active[stored.entry.key] == stored.entry &&
		!stored.entry.lease.retired &&
		bridge.leasesBySKI[stored.entry.lease.ski] == stored.entry.lease &&
		bridge.currentRuntimeEpoch() == stored.runtime.RuntimeEpoch
	bridge.mu.Unlock()
	if !available || trusted == nil || !trusted(stored.entry.lease.ski) {
		return eebusraw.ReadObservationV1{}, eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1NotFound,
			"notified raw feature data is not available",
			true,
			eebusraw.SourceLayerV1Runtime,
		)
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
