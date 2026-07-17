// Package eebusservicebridge isolates the released service constructor behind an internal boundary.
package eebusservicebridge

import (
	eebusapi "github.com/Project-Helianthus/helianthus-eebus-go/api"
	eebusservice "github.com/Project-Helianthus/helianthus-eebus-go/service"
	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
)

type OutgoingAttemptBridgeConfiguration struct {
	Gate shipapi.OutgoingAttemptGate
	Sink shipapi.OutgoingAttemptHubReaderInterface
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
