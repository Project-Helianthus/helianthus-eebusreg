// Package eebusservicebridge isolates the released service constructor behind an internal boundary.
package eebusservicebridge

import (
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"time"

	eebusapi "github.com/Project-Helianthus/helianthus-eebus-go/api"
	eebusservice "github.com/Project-Helianthus/helianthus-eebus-go/service"
	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
)

type OutgoingAttemptBridgeConfiguration struct {
	Gate shipapi.OutgoingAttemptGate
	Sink shipapi.OutgoingAttemptHubReaderInterface
}

type ListenerPolicy struct {
	ListenAddress    netip.AddrPort
	DiscoveryEnabled bool
}

type ServiceOptions struct {
	ListenerPolicy        *ListenerPolicy
	OutgoingAttemptBridge *OutgoingAttemptBridgeConfiguration
}

type Service struct {
	*eebusservice.Service
	lifecycleMu    sync.Mutex
	shutdownOnce   sync.Once
	monitorDone    chan struct{}
	monitorStarted bool
	monitorWG      sync.WaitGroup
	stopping       bool
	stop           chan struct{}
	terminal       chan error
}

func NewServiceWithOptions(
	configuration *eebusapi.Configuration,
	reader eebusapi.ServiceReaderInterface,
	options ServiceOptions,
) *Service {
	translated := eebusservice.ServiceOptions{}
	if options.ListenerPolicy != nil {
		translated.ListenerPolicy = &eebusservice.ListenerPolicy{
			ListenAddress:    options.ListenerPolicy.ListenAddress,
			DiscoveryEnabled: options.ListenerPolicy.DiscoveryEnabled,
		}
	}
	if options.OutgoingAttemptBridge != nil {
		translated.OutgoingAttemptBridge = &eebusservice.OutgoingAttemptBridgeConfiguration{
			Gate: options.OutgoingAttemptBridge.Gate,
			Sink: options.OutgoingAttemptBridge.Sink,
		}
	}
	candidate := eebusservice.NewServiceWithOptions(configuration, reader, translated)
	if candidate == nil {
		return nil
	}
	return &Service{
		Service:     candidate,
		monitorDone: make(chan struct{}),
		stop:        make(chan struct{}),
		terminal:    make(chan error, 1),
	}
}

func (service *Service) StartWithPolicy() error {
	if service == nil || service.Service == nil {
		return errors.New("scoped service is unavailable")
	}
	service.lifecycleMu.Lock()
	if service.stopping {
		service.lifecycleMu.Unlock()
		return errors.New("scoped service is stopping")
	}
	service.lifecycleMu.Unlock()
	if err := service.Service.StartWithPolicy(); err != nil {
		return err
	}
	service.lifecycleMu.Lock()
	if service.stopping {
		service.lifecycleMu.Unlock()
		service.Service.Shutdown()
		return errors.New("scoped service stopped during startup")
	}
	if !service.monitorStarted {
		service.monitorStarted = true
		service.monitorWG.Add(1)
		go service.monitorListener()
	}
	service.lifecycleMu.Unlock()
	return nil
}

func (service *Service) ListenerTerminal() <-chan error {
	if service == nil {
		return nil
	}
	return service.terminal
}

func (service *Service) Shutdown() {
	if service == nil {
		return
	}
	service.shutdownOnce.Do(func() {
		service.lifecycleMu.Lock()
		service.stopping = true
		close(service.stop)
		service.lifecycleMu.Unlock()
		if service.Service != nil {
			service.Service.Shutdown()
		}
		service.monitorWG.Wait()
	})
}

func (service *Service) monitorListener() {
	defer service.monitorWG.Done()
	defer close(service.monitorDone)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-service.stop:
			return
		case <-ticker.C:
			if err := service.Service.StartWithPolicy(); err != nil {
				service.lifecycleMu.Lock()
				if service.stopping {
					service.lifecycleMu.Unlock()
					return
				}
				select {
				case service.terminal <- fmt.Errorf("scoped listener terminal: %w", err):
				default:
				}
				service.lifecycleMu.Unlock()
				return
			}
		}
	}
}

func NewServiceWithOutgoingAttemptBridge(
	configuration *eebusapi.Configuration,
	reader eebusapi.ServiceReaderInterface,
	bridge OutgoingAttemptBridgeConfiguration,
) *eebusservice.Service {
	return eebusservice.NewServiceWithOutgoingAttemptBridge(
		configuration,
		reader,
		eebusservice.OutgoingAttemptBridgeConfiguration{Gate: bridge.Gate, Sink: bridge.Sink},
	)
}
