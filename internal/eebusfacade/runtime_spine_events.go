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
	service      runtimeService
	generation   uint64
	sessionIndex uint64
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
	handler.mu.Unlock()
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
	observation, ok := handler.observations[ski]
	if !ok || observation.SessionState != "connected" {
		handler.mu.Unlock()
		return
	}
	sessionIndex := observation.SessionIndex
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
	handler.enqueueSPINERefresh(ski, runtimeSPINERefresh{
		service: service, generation: generation, sessionIndex: sessionIndex,
	})
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
				devices, err := runtimeDevicesForRemote(refresh.service, ski)
				if err != nil {
					handler.report(err)
					continue
				}
				if len(devices) == 0 {
					continue
				}
				handler.updateRemoteFromSPINEEvent(ski, refresh, devices)
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
	observation.Devices = devices
	observation.Since = handler.timestamp()
	if err := handler.reducer.Replace(observation); err != nil {
		handler.mu.Unlock()
		handler.report(err)
		return
	}
	handler.observations[ski] = observation
	handler.mu.Unlock()
	handler.publishOrReport()
}
