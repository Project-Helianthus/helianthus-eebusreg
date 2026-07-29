package eebusfacade

import (
	"context"
	"reflect"
	"sort"
	"strings"

	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
	spine "github.com/Project-Helianthus/helianthus-spine-go/spine"
)

type runtimeSPINERefresh struct {
	devices      []runtimeDeviceObservation
	generation   uint64
	sessionIndex uint64
}

type runtimeSPINEStaged struct {
	devices     []runtimeDeviceObservation
	generation  uint64
	remoteEpoch uint64
	remote      spineapi.DeviceRemoteInterface
}

func subscribeRuntimeSPINEEvents(handler spineapi.EventHandlerInterface) (func() error, error) {
	if err := spine.Events.Subscribe(handler); err != nil {
		return nil, err
	}
	return func() error {
		return spine.Events.Unsubscribe(handler)
	}, nil
}

func (handler *runtimeServiceHandler) activateSPINEEvents(service runtimeService) {
	workerContext, cancel := context.WithCancel(context.Background())
	handler.mu.Lock()
	handler.spineGeneration++
	handler.spineService = service
	handler.spineEventsActive = true
	handler.spineCancel = cancel
	handler.spineWake = make(chan struct{}, 1)
	handler.spinePending = make(map[string]runtimeSPINERefresh)
	handler.spineStaged = make(map[string]runtimeSPINEStaged)
	handler.spineRemoteEpoch = make(map[string]uint64)
	handler.spineWork.Add(1)
	handler.mu.Unlock()
	go handler.runSPINERefreshWorker(workerContext)
}

func (handler *runtimeServiceHandler) deactivateSPINEEvents() {
	handler.mu.Lock()
	handler.spineGeneration++
	handler.spineEventsActive = false
	handler.spineService = nil
	cancel := handler.spineCancel
	handler.spineCancel = nil
	handler.spinePending = nil
	handler.spineStaged = nil
	handler.spineRemoteEpoch = nil
	rawFeatures := handler.rawFeatures
	handler.mu.Unlock()
	if rawFeatures != nil {
		rawFeatures.retireAll()
	}
	if cancel != nil {
		cancel()
	}
}

func (handler *runtimeServiceHandler) waitForSPINEEvents() {
	handler.spineWG.Wait()
	handler.spineWork.Wait()
}

func (handler *runtimeServiceHandler) HandleEvent(payload spineapi.EventPayload) {
	if !runtimeTopologyEvent(payload) {
		return
	}
	ski := strings.ToLower(strings.TrimSpace(payload.Ski))

	handler.mu.Lock()
	if !handler.spineEventsActive || !handler.remoteLivenessAllowedLocked(ski) {
		handler.mu.Unlock()
		return
	}
	service := handler.spineService
	generation := handler.spineGeneration
	remoteEpoch := handler.spineRemoteEpoch[ski]
	handler.spineWG.Add(1)
	handler.mu.Unlock()
	defer handler.spineWG.Done()

	if service == nil || service.LocalDevice() == nil {
		return
	}
	remote := service.LocalDevice().RemoteDeviceForSki(ski)
	if !sameRuntimeRemoteDevice(remote, payload.Device) {
		return
	}
	devices, err := runtimeDevicesForRemoteDevice(remote, ski)
	if err != nil {
		handler.report(err)
		return
	}
	if len(devices) == 0 {
		return
	}
	handler.stageOrEnqueueSPINERefresh(ski, remote, generation, remoteEpoch, devices)
}

func runtimeTopologyEvent(payload spineapi.EventPayload) bool {
	switch payload.EventType {
	case spineapi.EventTypeDeviceChange:
		return payload.ChangeType == spineapi.ElementChangeAdd ||
			payload.ChangeType == spineapi.ElementChangeUpdate
	case spineapi.EventTypeEntityChange:
		return true
	case spineapi.EventTypeDataChange:
		_, ok := payload.Data.(*spinemodel.NodeManagementUseCaseDataType)
		return ok
	default:
		return false
	}
}

func sameRuntimeRemoteDevice(expected, observed spineapi.DeviceRemoteInterface) bool {
	if expected == nil || observed == nil {
		return false
	}
	expectedType := reflect.TypeOf(expected)
	if expectedType != reflect.TypeOf(observed) || !expectedType.Comparable() {
		return false
	}
	return expected == observed
}

func (handler *runtimeServiceHandler) stageOrEnqueueSPINERefresh(
	ski string,
	remote spineapi.DeviceRemoteInterface,
	generation uint64,
	remoteEpoch uint64,
	devices []runtimeDeviceObservation,
) {
	handler.mu.Lock()
	if !handler.spineEventsActive ||
		handler.spineGeneration != generation ||
		handler.spineRemoteEpoch[ski] != remoteEpoch ||
		!handler.remoteLivenessAllowedLocked(ski) {
		handler.mu.Unlock()
		return
	}
	observation, connected := handler.observations[ski]
	connected = connected && observation.SessionState == "connected"
	if connected {
		sessionIndex := observation.SessionIndex
		handler.mu.Unlock()
		handler.enqueueSPINERefresh(ski, runtimeSPINERefresh{
			devices: devices, generation: generation, sessionIndex: sessionIndex,
		})
		return
	}

	staged := runtimeSPINEStaged{
		devices: devices, generation: generation, remoteEpoch: remoteEpoch, remote: remote,
	}
	if prior, exists := handler.spineStaged[ski]; exists &&
		prior.generation == generation &&
		prior.remoteEpoch == remoteEpoch &&
		sameRuntimeRemoteDevice(prior.remote, remote) {
		merged, err := mergeRuntimeDeviceCollections(prior.devices, devices)
		if err != nil {
			handler.mu.Unlock()
			handler.report(err)
			return
		}
		staged.devices = merged
	}
	handler.spineStaged[ski] = staged
	handler.mu.Unlock()
}

func (handler *runtimeServiceHandler) consumeStagedSPINERefresh(
	ski string,
	current spineapi.DeviceRemoteInterface,
	sessionIndex uint64,
) {
	handler.mu.Lock()
	staged, exists := handler.spineStaged[ski]
	if exists {
		delete(handler.spineStaged, ski)
	}
	observation, connected := handler.observations[ski]
	valid := exists &&
		handler.spineEventsActive &&
		staged.generation == handler.spineGeneration &&
		staged.remoteEpoch == handler.spineRemoteEpoch[ski] &&
		connected &&
		observation.SessionState == "connected" &&
		observation.SessionIndex == sessionIndex &&
		sameRuntimeRemoteDevice(staged.remote, current)
	handler.mu.Unlock()
	if !valid {
		return
	}
	handler.enqueueSPINERefresh(ski, runtimeSPINERefresh{
		devices: staged.devices, generation: staged.generation, sessionIndex: sessionIndex,
	})
}

func (handler *runtimeServiceHandler) retireStagedSPINERefresh(ski string) {
	handler.mu.Lock()
	if handler.spineRemoteEpoch != nil {
		handler.spineRemoteEpoch[ski]++
	}
	if handler.spineStaged != nil {
		delete(handler.spineStaged, ski)
	}
	handler.mu.Unlock()
}

func (handler *runtimeServiceHandler) enqueueSPINERefresh(ski string, refresh runtimeSPINERefresh) {
	handler.mu.Lock()
	observation, ok := handler.observations[ski]
	if !handler.spineEventsActive ||
		handler.spineGeneration != refresh.generation ||
		!ok ||
		observation.SessionState != "connected" ||
		observation.SessionIndex != refresh.sessionIndex {
		handler.mu.Unlock()
		return
	}
	if pending, exists := handler.spinePending[ski]; exists &&
		pending.generation == refresh.generation &&
		pending.sessionIndex == refresh.sessionIndex {
		merged, err := mergeRuntimeDeviceCollections(pending.devices, refresh.devices)
		if err != nil {
			handler.mu.Unlock()
			handler.report(err)
			return
		}
		refresh.devices = merged
	}
	handler.spinePending[ski] = refresh
	wake := handler.spineWake
	handler.mu.Unlock()
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (handler *runtimeServiceHandler) runSPINERefreshWorker(ctx context.Context) {
	defer handler.spineWork.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-handler.spineWake:
			for _, ski := range handler.pendingSPINERefreshSKIs() {
				refresh, ok := handler.takeSPINERefresh(ski)
				if !ok {
					continue
				}
				handler.updateRemoteFromSPINEEvent(ski, refresh, refresh.devices)
			}
		}
	}
}

func (handler *runtimeServiceHandler) pendingSPINERefreshSKIs() []string {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	skis := make([]string, 0, len(handler.spinePending))
	for ski := range handler.spinePending {
		skis = append(skis, ski)
	}
	sort.Strings(skis)
	return skis
}

func (handler *runtimeServiceHandler) takeSPINERefresh(ski string) (runtimeSPINERefresh, bool) {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	refresh, ok := handler.spinePending[ski]
	if ok {
		delete(handler.spinePending, ski)
	}
	return refresh, ok
}

func (handler *runtimeServiceHandler) updateRemoteFromSPINEEvent(
	ski string,
	refresh runtimeSPINERefresh,
	devices []runtimeDeviceObservation,
) {
	handler.mu.Lock()
	observation, ok := handler.observations[ski]
	if !handler.spineEventsActive ||
		handler.spineGeneration != refresh.generation ||
		!ok ||
		observation.SessionState != "connected" ||
		observation.SessionIndex != refresh.sessionIndex {
		handler.mu.Unlock()
		return
	}
	merged, err := mergeRuntimeDeviceCollections(observation.Devices, devices)
	if err != nil {
		handler.mu.Unlock()
		handler.report(err)
		return
	}
	observation.Devices = merged
	observation.Since = handler.timestamp()
	if err := handler.reducer.Replace(observation); err != nil {
		handler.mu.Unlock()
		handler.report(err)
		return
	}
	handler.observations[ski] = observation
	handler.runtimeRevision++
	handler.mu.Unlock()
	handler.publishOrReport()
	handler.refreshRawFeatureRemote(ski)
}
