package eebusfacade

import (
	"errors"
	"sync"
	"sync/atomic"

	eebusapi "github.com/Project-Helianthus/helianthus-eebus-go/api"
	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
)

var errRuntimeOutboundEndpointFailed = errors.New("eebus runtime outbound endpoint operation failed")

const runtimeOutboundQueueCapacity = 256

type runtimeOutboundRemote struct {
	ski      string
	endpoint shipapi.RemoteEndpoint
	report   bool
}

type runtimeOutboundTaskKind uint8

const (
	runtimeOutboundOpen runtimeOutboundTaskKind = iota
	runtimeOutboundClose
	runtimeOutboundTrusted
)

type runtimeOutboundTask struct {
	kind       runtimeOutboundTaskKind
	generation uint64
	remote     runtimeOutboundRemote
	done       chan error
}

type runtimeOutboundController struct {
	coordinator *firstTrustCoordinator
	service     firstTrustService
	outbound    eebusapi.OutboundPairingServiceInterface
	remotes     []runtimeOutboundRemote

	generation atomic.Uint64
	tasks      chan runtimeOutboundTask
	stopped    chan struct{}
	closeOnce  sync.Once
}

func newRuntimeOutboundController(
	coordinator *firstTrustCoordinator,
	service firstTrustService,
	runtime runtimeService,
	config RuntimeConfig,
) *runtimeOutboundController {
	controller := &runtimeOutboundController{
		coordinator: coordinator,
		service:     service,
		tasks:       make(chan runtimeOutboundTask, runtimeOutboundQueueCapacity),
		stopped:     make(chan struct{}),
	}
	controller.outbound, _ = runtime.(eebusapi.OutboundPairingServiceInterface)
	for _, remote := range config.Remotes {
		if !remote.Allowlisted || remote.Pretrusted || !remote.Endpoint.IsValid() && !config.DiscoveryEnabled {
			continue
		}
		binding := runtimeOutboundRemote{ski: remote.SKI}
		if remote.Endpoint.IsValid() {
			binding.endpoint = shipapi.RemoteEndpoint{
				Host: remote.Endpoint.Addr().String(),
				Port: remote.Endpoint.Port(),
				Path: remote.SHIPPath,
			}
			binding.report = true
		}
		controller.remotes = append(controller.remotes, binding)
	}
	go controller.run()
	return controller
}

func (controller *runtimeOutboundController) enqueueOpenLocked() <-chan error {
	if controller == nil || len(controller.remotes) == 0 {
		return nil
	}
	task := runtimeOutboundTask{
		kind:       runtimeOutboundOpen,
		generation: controller.generation.Add(1),
		done:       make(chan error, 1),
	}
	if !controller.enqueueLocked(task) {
		task.done <- errRuntimeOutboundEndpointFailed
	}
	return task.done
}

func (controller *runtimeOutboundController) enqueueCloseLocked() <-chan error {
	if controller == nil {
		return nil
	}
	task := runtimeOutboundTask{
		kind:       runtimeOutboundClose,
		generation: controller.generation.Add(1),
		done:       make(chan error, 1),
	}
	if !controller.enqueueLocked(task) {
		task.done <- errRuntimeOutboundEndpointFailed
	}
	return task.done
}

func (controller *runtimeOutboundController) enqueueTrustedLocked(remote RuntimeRemote) <-chan error {
	if controller == nil {
		return nil
	}
	binding := runtimeOutboundRemote{ski: remote.SKI}
	if remote.Endpoint.IsValid() {
		binding.endpoint = shipapi.RemoteEndpoint{
			Host: remote.Endpoint.Addr().String(),
			Port: remote.Endpoint.Port(),
			Path: remote.SHIPPath,
		}
		binding.report = true
	}
	task := runtimeOutboundTask{
		kind:       runtimeOutboundTrusted,
		generation: controller.generation.Add(1),
		remote:     binding,
		done:       make(chan error, 1),
	}
	if !controller.enqueueLocked(task) {
		task.done <- errRuntimeOutboundEndpointFailed
	}
	return task.done
}

func (controller *runtimeOutboundController) enqueueLocked(task runtimeOutboundTask) bool {
	select {
	case controller.tasks <- task:
		return true
	default:
		return false
	}
}

func (controller *runtimeOutboundController) run() {
	active := make(map[string]struct{})
	defer close(controller.stopped)
	for task := range controller.tasks {
		controller.waitForCoordinatorUnlock()
		var err error
		switch task.kind {
		case runtimeOutboundOpen:
			err = controller.open(task.generation, active)
		case runtimeOutboundClose:
			controller.cancelActive(active)
		case runtimeOutboundTrusted:
			err = controller.registerTrusted(task.remote)
		}
		if err != nil {
			controller.cancelActive(active)
			controller.failClosed()
		}
		task.done <- err
		close(task.done)
	}
	controller.cancelActive(active)
}

func (controller *runtimeOutboundController) waitForCoordinatorUnlock() {
	if controller.coordinator == nil {
		return
	}
	controller.coordinator.mu.Lock()
	controller.coordinator.mu.Unlock()
}

func (controller *runtimeOutboundController) open(generation uint64, active map[string]struct{}) error {
	if controller.generation.Load() != generation {
		return nil
	}
	if controller.outbound == nil {
		return errRuntimeOutboundEndpointFailed
	}
	for _, remote := range controller.remotes {
		if controller.generation.Load() != generation {
			return nil
		}
		if err := controller.outbound.QueueRemoteSKI(remote.ski); err != nil {
			return errRuntimeOutboundEndpointFailed
		}
		active[remote.ski] = struct{}{}
		if controller.generation.Load() != generation {
			return nil
		}
		if remote.report {
			if err := controller.outbound.ReportRemoteEndpoint(remote.ski, remote.endpoint); err != nil {
				return errRuntimeOutboundEndpointFailed
			}
		}
	}
	return nil
}

func (controller *runtimeOutboundController) registerTrusted(remote runtimeOutboundRemote) error {
	if controller.service == nil {
		return errRuntimeOutboundEndpointFailed
	}
	if remote.report && controller.outbound == nil {
		return errRuntimeOutboundEndpointFailed
	}
	controller.service.RegisterRemoteSKI(remote.ski)
	if !remote.report {
		return nil
	}
	if err := controller.outbound.ReportRemoteEndpoint(remote.ski, remote.endpoint); err != nil {
		return errRuntimeOutboundEndpointFailed
	}
	return nil
}

func (controller *runtimeOutboundController) cancelActive(active map[string]struct{}) {
	if controller.service == nil {
		return
	}
	for ski := range active {
		controller.service.CancelPairingWithSKI(ski)
		delete(active, ski)
	}
}

func (controller *runtimeOutboundController) failClosed() {
	if controller.coordinator == nil {
		return
	}
	controller.coordinator.outboundEndpointFailed()
}

func (controller *runtimeOutboundController) Close() {
	if controller == nil {
		return
	}
	controller.closeOnce.Do(func() {
		close(controller.tasks)
		<-controller.stopped
	})
}
