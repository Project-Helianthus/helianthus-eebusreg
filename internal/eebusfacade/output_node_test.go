package eebusfacade

import (
	"net/netip"
	"testing"

	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusservicebridge"
	shipcert "github.com/Project-Helianthus/helianthus-ship-go/cert"
	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
)

func TestOutputNodeCountsFollowTheService(t *testing.T) {
	service, concrete := newOutputNodeRuntime(t)
	node := &OutputNode{service: concrete, byLetter: map[string]spineapi.EntityLocalInterface{}}
	if node.AdvertisementCount() != 0 || node.ServiceCount() != 0 || node.DeviceType() != "" {
		t.Fatalf("unready ads=%d services=%d type=%q", node.AdvertisementCount(), node.ServiceCount(), node.DeviceType())
	}
	if err := service.Setup(); err != nil {
		t.Fatal(err)
	}
	if node.AdvertisementCount() != 1 || node.ServiceCount() != 1 || node.DeviceType() != "EnergyManagementSystem" {
		t.Fatalf("ready ads=%d services=%d type=%q", node.AdvertisementCount(), node.ServiceCount(), node.DeviceType())
	}
	if !outputNodeHasEntity(concrete, spinemodel.EntityTypeTypeCEM) {
		t.Fatal("prepared node has no CEM entity")
	}
	if outputNodeHasEntity(concrete, spinemodel.EntityTypeTypeInverter) {
		t.Fatal("prepared node already has an inverter")
	}
}

func TestOutputNodeUsesRuntimeService(t *testing.T) {
	service, concrete := newOutputNodeRuntime(t)
	if _, err := OpenOutputNode(); err == nil {
		t.Fatal("node opened before the runtime service was prepared")
	}
	if err := service.Setup(); err != nil {
		t.Fatal(err)
	}
	node, err := OpenOutputNode()
	if err != nil {
		t.Fatal(err)
	}
	if node.service != concrete {
		t.Fatal("node is not the runtime service")
	}
	if err := node.AddEntity("Inverter", "na", ""); err != nil {
		t.Fatal(err)
	}
	if err := node.AddEntity("PV String", "nb", "na"); err != nil {
		t.Fatal(err)
	}
	if err := node.AddServerFeature("na", "measurement"); err != nil {
		t.Fatal(err)
	}
	if !outputNodeHasEntity(concrete, spinemodel.EntityTypeTypeInverter) || !outputNodeHasEntity(concrete, spinemodel.EntityTypeTypePVString) {
		t.Fatal("runtime service device has no inverter or string")
	}
	if !node.NestedUnder("nb", "na") {
		t.Fatal("string is not nested under the inverter")
	}
	if features := node.ObservedServerFeatures("na"); len(features) != 1 || features[0] != "Measurement" {
		t.Fatalf("server features %v", features)
	}
	if node.AdvertisementCount() != 1 || node.ServiceCount() != 1 || node.DeviceType() != "EnergyManagementSystem" {
		t.Fatalf("ads=%d services=%d type=%q", node.AdvertisementCount(), node.ServiceCount(), node.DeviceType())
	}
	releaseOutputService(service)
	if _, err := OpenOutputNode(); err == nil {
		t.Fatal("node opened after the runtime service was released")
	}
}

// PrepareOutputNodeRuntimeForTest prepares the runtime service without
// listening or announcing, for the external binding test.
func PrepareOutputNodeRuntimeForTest(t *testing.T) *eebusservicebridge.Service {
	t.Helper()
	service, concrete := newOutputNodeRuntime(t)
	if err := service.Setup(); err != nil {
		t.Fatal(err)
	}
	return concrete
}

func newOutputNodeRuntime(t *testing.T) (runtimeService, *eebusservicebridge.Service) {
	t.Helper()
	certificate, err := shipcert.CreateCertificate("", "Helianthus", "RO", "binding")
	if err != nil {
		t.Fatal(err)
	}
	service, err := newEEBusService(RuntimeConfig{
		Interface:     "fixture-interface",
		ListenPort:    4711,
		ListenAddress: netip.MustParseAddrPort("127.0.0.1:4711"),
	}, runtimeMaterial{
		certificate: certificate,
		localSKI:    certificateSKI(t, certificate),
		nodeToken:   runtimeTestNodeToken,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	concrete, ok := service.(*eebusservicebridge.Service)
	if !ok || concrete == nil {
		t.Fatal("runtime service is not the bridge service")
	}
	t.Cleanup(func() {
		adoptOutputService(nil)
		service.Shutdown()
	})
	return service, concrete
}

func outputNodeHasEntity(service *eebusservicebridge.Service, entityType spinemodel.EntityTypeType) bool {
	if service == nil || service.LocalDevice() == nil {
		return false
	}
	for _, entity := range service.LocalDevice().Entities() {
		if entity.EntityType() == entityType {
			return true
		}
	}
	return false
}
