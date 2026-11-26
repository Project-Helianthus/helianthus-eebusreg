package eebusfacade

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/Project-Helianthus/helianthus-eebus-go/session"
	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
)

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
