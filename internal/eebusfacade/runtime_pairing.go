package eebusfacade

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
)

const (
	// RemoteTransportConnected is the notice kind sent from RemoteSKIConnected.
	RemoteTransportConnected = "connected"
	// RemoteTransportDisconnected is the notice kind sent from RemoteSKIDisconnected.
	RemoteTransportDisconnected = "disconnected"
	// RemoteSourceValue is a qualified remote read or notify for one connection generation.
	RemoteSourceValue = "value"
	// RemoteSourceGenerationStopped is the end of one remote connection generation.
	RemoteSourceGenerationStopped = "generation_stopped"
)

// RemoteTransportNotice is one SHIP connection notice for one remote SKI.
// Kind is RemoteTransportConnected or RemoteTransportDisconnected.
type RemoteTransportNotice struct {
	Kind string
	SKI  string
}

// RemoteTransportFunc receives SHIP connection notices for one runtime instance.
type RemoteTransportFunc func(RemoteTransportNotice)

type remoteTransportContextKey struct{}

// WithRemoteTransport returns a child of ctx that carries notify.
// Acquire copies that function onto the instance handler. A nil context uses
// context.Background. A nil notify leaves the handler without a receiver.
func WithRemoteTransport(ctx context.Context, notify RemoteTransportFunc) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if notify == nil {
		return ctx
	}
	return context.WithValue(ctx, remoteTransportContextKey{}, notify)
}

// RemoteTransportFrom returns the notice function carried by ctx, or nil.
func RemoteTransportFrom(ctx context.Context) RemoteTransportFunc {
	if ctx == nil {
		return nil
	}
	notify, _ := ctx.Value(remoteTransportContextKey{}).(RemoteTransportFunc)
	return notify
}

// RemoteSourceNotice is one qualified observation or one stopped connection generation.
// Priority zero asks the instance to use the producing remote's rank.
type RemoteSourceNotice struct {
	Kind       string
	SKI        string
	Generation uint64
	Value      string
	ObservedAt time.Time
	Priority   int32
	Path       string
}

// RemoteSourceFunc receives qualified observations and stopped generations for one instance.
type RemoteSourceFunc func(RemoteSourceNotice)

type remoteSourceContextKey struct{}

// WithRemoteSource returns a child of ctx that carries notify.
// Acquire copies that function onto the instance handler. A nil context uses
// context.Background. A nil notify leaves the handler without a receiver.
func WithRemoteSource(ctx context.Context, notify RemoteSourceFunc) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if notify == nil {
		return ctx
	}
	return context.WithValue(ctx, remoteSourceContextKey{}, notify)
}

// RemoteSourceFrom returns the source function carried by ctx, or nil.
func RemoteSourceFrom(ctx context.Context) RemoteSourceFunc {
	if ctx == nil {
		return nil
	}
	notify, _ := ctx.Value(remoteSourceContextKey{}).(RemoteSourceFunc)
	return notify
}

func bindRemoteTransport(handler *runtimeServiceHandler, ctx context.Context) {
	if handler == nil {
		return
	}
	notify := RemoteTransportFrom(ctx)
	source := RemoteSourceFrom(ctx)
	handler.mu.Lock()
	handler.remoteTransport = notify
	handler.remoteSource = source
	handler.mu.Unlock()
}

func (handler *runtimeServiceHandler) notifySource(notice RemoteSourceNotice) {
	if handler == nil {
		return
	}
	handler.mu.Lock()
	notify := handler.remoteSource
	handler.mu.Unlock()
	if notify == nil {
		return
	}
	notify(notice)
}

func (handler *runtimeServiceHandler) noteRemoteNotify(payload spineapi.EventPayload) {
	if !remoteNotifyEvent(payload) {
		handler.routeRemoteNotify(payload)
		return
	}
	ski := strings.ToLower(strings.TrimSpace(payload.Ski))
	before, beforeOK := handler.remoteNotify(ski, payload.Feature, payload.Function)
	handler.routeRemoteNotify(payload)
	after, afterOK := handler.remoteNotify(ski, payload.Feature, payload.Function)
	if !afterOK || (beforeOK && before.generation == after.generation && before.value == after.value && before.at.Equal(after.at)) {
		return
	}
	handler.notifySource(RemoteSourceNotice{
		Kind: RemoteSourceValue, SKI: ski, Generation: after.generation, Value: after.value, ObservedAt: after.at,
	})
}

func remoteNotifyEvent(payload spineapi.EventPayload) bool {
	return payload.EventType == spineapi.EventTypeDataChange && payload.CmdClassifier != nil &&
		*payload.CmdClassifier == spinemodel.CmdClassifierTypeNotify && payload.Function != "" &&
		strings.TrimSpace(payload.Ski) != ""
}

type remoteNotifyView struct {
	generation uint64
	value      string
	at         time.Time
}

func (handler *runtimeServiceHandler) remoteNotify(ski string, feature spineapi.FeatureRemoteInterface, function spinemodel.FunctionType) (remoteNotifyView, bool) {
	if handler == nil {
		return remoteNotifyView{}, false
	}
	handler.mu.Lock()
	bridge := handler.rawFeatures
	handler.mu.Unlock()
	return bridge.remoteNotifyView(ski, feature, function)
}

func (bridge *rawFeatureRuntimeBridge) remoteNotifyView(ski string, feature spineapi.FeatureRemoteInterface, function spinemodel.FunctionType) (remoteNotifyView, bool) {
	address, addressed := notifyFeatureAddress(feature)
	if bridge == nil || ski == "" || function == "" || !addressed {
		return remoteNotifyView{}, false
	}
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	var found remoteNotifiedData
	ok := false
	for _, stored := range bridge.remoteSubscriptions.notified {
		if stored.failure != nil || stored.entry == nil || stored.entry.lease == nil || stored.entry.lease.ski != ski {
			continue
		}
		if stored.target.Function != string(function) || stored.target.FeatureAddress != address || stored.runtime.ConnectionGeneration == 0 {
			continue
		}
		if !ok || stored.notifiedAt.After(found.notifiedAt) {
			found = stored
			ok = true
		}
	}
	if !ok {
		return remoteNotifyView{}, false
	}
	encoded, err := json.Marshal(found.value.Clone())
	if err != nil || len(encoded) == 0 || string(encoded) == "null" {
		return remoteNotifyView{}, false
	}
	return remoteNotifyView{generation: found.runtime.ConnectionGeneration, value: string(encoded), at: found.notifiedAt}, true
}

func notifyFeatureAddress(feature spineapi.FeatureRemoteInterface) (uint64, bool) {
	if isNilRawRuntimeValue(feature) || feature.Address() == nil || feature.Address().Feature == nil {
		return 0, false
	}
	return uint64(*feature.Address().Feature), true
}

func (handler *runtimeServiceHandler) stoppedSourceNoticesLocked() []RemoteSourceNotice {
	var notices []RemoteSourceNotice
	for ski, observation := range handler.observations {
		if observation.SessionIndex == 0 {
			continue
		}
		notices = append(notices, RemoteSourceNotice{
			Kind: RemoteSourceGenerationStopped, SKI: ski, Generation: observation.SessionIndex,
		})
	}
	return notices
}

func (handler *runtimeServiceHandler) notifyTransport(kind, ski string) {
	if handler == nil || ski == "" {
		return
	}
	handler.mu.Lock()
	notify := handler.remoteTransport
	handler.mu.Unlock()
	if notify == nil {
		return
	}
	notify(RemoteTransportNotice{Kind: kind, SKI: ski})
}
