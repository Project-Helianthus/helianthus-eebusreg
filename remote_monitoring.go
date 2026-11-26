package eebusruntime

import (
	"context"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusfacade"
)

// RemoteMonitoring lists every remote the runtime knows with its SHIP state and, for each served entity, its use case, scenarios and values.
type RemoteMonitoring struct {
	Remotes []RemoteMonitoringRemote
}

// RemoteMonitoringRemote is one remote with its discovery data and SHIP state.
type RemoteMonitoringRemote struct {
	SKI               string
	SHIPID            string
	ConnectionState   string
	PairingState      string
	Visible           bool
	Paired            bool
	ServiceName       string
	ServiceIdentifier string
	ServiceBrand      string
	ServiceType       string
	ServiceModel      string
	Entities          []RemoteMonitoringEntity
}

// RemoteMonitoringEntity is one remote entity serving a monitoring use case; a value the remote did not serve is absent.
type RemoteMonitoringEntity struct {
	DeviceAddress string
	DeviceType    string
	EntityAddress []uint64
	ParentAddress []uint64
	EntityType    string
	UseCase       string
	Actor         string
	Scenarios     []uint
	Observations  []RemoteMonitoringObservation
}

// RemoteMonitoringObservation is one value in its source type and native unit with the time of the REPLY or NOTIFY that carried it; Kind names which of Number, Text and Boolean carries it, and Stale is set once the connection that carried it has ended.
type RemoteMonitoringObservation struct {
	Scenario uint
	Name     string
	Phase    string
	Kind     string
	Number   float64
	Text     string
	Boolean  bool
	Unit     string
	Updated  time.Time
	Source   string
	Stale    bool
}

type remoteMonitoringSnapshot = eebusfacade.RemoteMonitoringSnapshot

type remoteMonitoringRuntime interface {
	RemoteMonitoringGet(context.Context) (RemoteMonitoring, *eebusraw.ErrorV1)
}

var _ remoteMonitoringRuntime = (*runtimeImplementation)(nil)
var _ eebusfacade.RemoteMonitoringBackend = (*facadeRuntimeBackend)(nil)

func (runtime *runtimeImplementation) RemoteMonitoringGet(ctx context.Context) (RemoteMonitoring, *eebusraw.ErrorV1) {
	backend, terminal := runtime.remoteMonitoringBackend()
	if terminal != nil {
		return RemoteMonitoring{}, terminal
	}
	snapshot, err := backend.RemoteMonitoringGet(ctx)
	if err != nil {
		return RemoteMonitoring{}, eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1Disconnected,
			"remote monitoring is unavailable",
			true,
			eebusraw.SourceLayerV1Runtime,
		)
	}
	return remoteMonitoringFromFacade(snapshot), nil
}

func (runtime *runtimeImplementation) remoteMonitoringBackend() (
	eebusfacade.RemoteMonitoringBackend,
	*eebusraw.ErrorV1,
) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if !runtime.enabled || runtime.shutdown || !runtime.started ||
		runtime.backend == nil || runtime.workerErr != nil {
		return nil, eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1Disconnected,
			"remote monitoring runtime is not connected",
			true,
			eebusraw.SourceLayerV1Runtime,
		)
	}
	backend, ok := runtime.backend.(eebusfacade.RemoteMonitoringBackend)
	if !ok || backend == nil {
		return nil, eebusraw.NewErrorV1(
			eebusraw.ErrorCodeV1Disconnected,
			"remote monitoring capability is unavailable",
			true,
			eebusraw.SourceLayerV1Runtime,
		)
	}
	return backend, nil
}

func (backend *facadeRuntimeBackend) RemoteMonitoringGet(
	ctx context.Context,
) (remoteMonitoringSnapshot, error) {
	monitoring, ok := backend.backend.(eebusfacade.RemoteMonitoringBackend)
	if !ok || monitoring == nil {
		return remoteMonitoringSnapshot{}, context.Canceled
	}
	return monitoring.RemoteMonitoringGet(ctx)
}

func remoteMonitoringFromFacade(snapshot eebusfacade.RemoteMonitoringSnapshot) RemoteMonitoring {
	result := RemoteMonitoring{Remotes: make([]RemoteMonitoringRemote, 0, len(snapshot.Remotes))}
	for _, remote := range snapshot.Remotes {
		converted := RemoteMonitoringRemote{
			SKI: remote.SKI, SHIPID: remote.SHIPID, ConnectionState: remote.SHIPState,
			PairingState: remote.PairingState, Visible: remote.Visible, Paired: remote.Paired,
			ServiceName: remote.ServiceName, ServiceIdentifier: remote.ServiceIdentifier,
			ServiceBrand: remote.ServiceBrand, ServiceType: remote.ServiceType, ServiceModel: remote.ServiceModel,
		}
		for _, entity := range remote.Entities {
			convertedEntity := RemoteMonitoringEntity{
				DeviceAddress: entity.DeviceAddress, DeviceType: entity.DeviceType,
				EntityAddress: append([]uint64(nil), entity.EntityAddress...), ParentAddress: append([]uint64(nil), entity.ParentAddress...),
				EntityType: entity.EntityType,
				UseCase:    entity.UseCase, Actor: entity.Actor, Scenarios: append([]uint(nil), entity.Scenarios...),
			}
			for _, observation := range entity.Observations {
				convertedEntity.Observations = append(convertedEntity.Observations, RemoteMonitoringObservation{
					Scenario: observation.Scenario, Name: observation.Name, Phase: observation.Phase,
					Kind: observation.Kind, Number: observation.Number, Text: observation.Text, Boolean: observation.Boolean,
					Unit: observation.Unit, Updated: observation.Updated,
					Source: observation.Source, Stale: observation.Stale,
				})
			}
			converted.Entities = append(converted.Entities, convertedEntity)
		}
		result.Remotes = append(result.Remotes, converted)
	}
	return result
}
