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
	monitorOnce sync.Once
	stopOnce    sync.Once
	stop        chan struct{}
	terminal    chan error
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
		Service:  candidate,
		stop:     make(chan struct{}),
		terminal: make(chan error, 1),
	}
}

func (service *Service) StartWithPolicy() error {
	if service == nil || service.Service == nil {
		return errors.New("scoped service is unavailable")
	}
	if err := service.Service.StartWithPolicy(); err != nil {
		return err
	}
	service.monitorOnce.Do(func() { go service.monitorListener() })
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
	service.stopOnce.Do(func() { close(service.stop) })
	if service.Service != nil {
		service.Service.Shutdown()
	}
}

func (service *Service) monitorListener() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-service.stop:
			return
		case <-ticker.C:
			if err := service.Service.StartWithPolicy(); err != nil {
				select {
				case service.terminal <- fmt.Errorf("scoped listener terminal: %w", err):
				case <-service.stop:
				}
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
