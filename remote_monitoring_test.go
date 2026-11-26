package eebusruntime

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusfacade"
)

type remoteMonitoringBackendStub struct {
	issue83RawBackend
	snapshot remoteMonitoringSnapshot
	err      error
}

func (backend *remoteMonitoringBackendStub) RemoteMonitoringGet(context.Context) (remoteMonitoringSnapshot, error) {
	return backend.snapshot, backend.err
}

type remoteMonitoringFacadeStub struct {
	snapshot remoteMonitoringSnapshot
}

func (remoteMonitoringFacadeStub) Run(context.Context, func([]byte)) error { return nil }
func (remoteMonitoringFacadeStub) Close() error                            { return nil }
func (backend remoteMonitoringFacadeStub) RemoteMonitoringGet(context.Context) (remoteMonitoringSnapshot, error) {
	return backend.snapshot, nil
}

func remoteMonitoringFixture() remoteMonitoringSnapshot {
	return remoteMonitoringSnapshot{Remotes: []eebusfacade.RemoteMonitoringRemote{{
		SKI: "ski-1", SHIPID: "ship-1", SHIPState: "connected", PairingState: "paired", Visible: true, Paired: true,
		ServiceName: "name-1", ServiceIdentifier: "identifier-1", ServiceBrand: "brand-1", ServiceType: "type-1", ServiceModel: "model-1",
		Entities: []eebusfacade.RemoteMonitoringEntity{{
			DeviceAddress: "device-1", DeviceType: "device-type-1", EntityAddress: []uint64{3, 4}, ParentAddress: []uint64{3}, EntityType: "entity-type-1",
			UseCase: "use-case-1", Actor: "actor-1", Scenarios: []uint{2, 5},
			Observations: []eebusfacade.RemoteMonitoringObservation{{
				Scenario: 6, Name: "name-2", Phase: "b", Kind: "text", Number: 12.5, Text: "exact text", Boolean: true, Unit: "W",
				Updated: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC), Source: eebusfacade.RemoteMonitoringSourceReply, Stale: true,
			}},
		}},
	}}}
}

func TestRemoteMonitoringRuntimeServesEveryFieldAsCopies(t *testing.T) {
	backend := &remoteMonitoringBackendStub{snapshot: remoteMonitoringFixture()}
	reader := startRemoteFeatureDataRuntime(t, backend).(remoteMonitoringRuntime)
	got, terminal := reader.RemoteMonitoringGet(context.Background())
	if terminal != nil {
		t.Fatalf("RemoteMonitoringGet() = %+v", terminal)
	}
	want := RemoteMonitoring{Remotes: []RemoteMonitoringRemote{{
		SKI: "ski-1", SHIPID: "ship-1", ConnectionState: "connected", PairingState: "paired", Visible: true, Paired: true,
		ServiceName: "name-1", ServiceIdentifier: "identifier-1", ServiceBrand: "brand-1", ServiceType: "type-1", ServiceModel: "model-1",
		Entities: []RemoteMonitoringEntity{{
			DeviceAddress: "device-1", DeviceType: "device-type-1", EntityAddress: []uint64{3, 4}, ParentAddress: []uint64{3}, EntityType: "entity-type-1",
			UseCase: "use-case-1", Actor: "actor-1", Scenarios: []uint{2, 5},
			Observations: []RemoteMonitoringObservation{{
				Scenario: 6, Name: "name-2", Phase: "b", Kind: "text", Number: 12.5, Text: "exact text", Boolean: true, Unit: "W",
				Updated: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC), Source: "reply", Stale: true,
			}},
		}},
	}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RemoteMonitoringGet() = %#v, want %#v", got, want)
	}
	got.Remotes[0].Entities[0].EntityAddress[0] = 99
	got.Remotes[0].Entities[0].Scenarios[0] = 99
	got.Remotes[0].Entities[0].ParentAddress[0] = 99
	if backend.snapshot.Remotes[0].Entities[0].ParentAddress[0] != 3 || backend.snapshot.Remotes[0].Entities[0].EntityAddress[0] != 3 || backend.snapshot.Remotes[0].Entities[0].Scenarios[0] != 2 {
		t.Fatal("caller mutation reached the backend snapshot")
	}
}

func TestRemoteMonitoringRuntimeAnswersDisconnectedWithoutCapabilityOrOnFailure(t *testing.T) {
	for name, backend := range map[string]runtimeBackend{
		"no capability": &remoteFeatureDataBackendStub{},
		"backend error": &remoteMonitoringBackendStub{err: errors.New("closed")},
	} {
		reader := startRemoteFeatureDataRuntime(t, backend).(remoteMonitoringRuntime)
		got, terminal := reader.RemoteMonitoringGet(context.Background())
		if terminal == nil || terminal.Code != eebusraw.ErrorCodeV1Disconnected || !terminal.Retriable ||
			!reflect.DeepEqual(got, RemoteMonitoring{}) {
			t.Errorf("%s: RemoteMonitoringGet() = %#v / %+v, want a retriable disconnected error", name, got, terminal)
		}
	}
}

func TestRemoteMonitoringFacadeBackendDelegates(t *testing.T) {
	want := remoteMonitoringFixture()
	backend := &facadeRuntimeBackend{backend: remoteMonitoringFacadeStub{snapshot: want}}
	got, err := backend.RemoteMonitoringGet(context.Background())
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("RemoteMonitoringGet() = %#v, %v", got, err)
	}
	missing := &facadeRuntimeBackend{backend: nil}
	if _, err := missing.RemoteMonitoringGet(context.Background()); err == nil {
		t.Fatal("a facade backend without the capability answered")
	}
}
