package eebusfacade_test

import (
	"testing"

	eebusruntime "github.com/Project-Helianthus/helianthus-eebusreg"
	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusfacade"
	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
)

func TestOutputBindingOnRuntimeNode(t *testing.T) {
	service := eebusfacade.PrepareOutputNodeRuntimeForTest(t)
	entities := len(service.LocalDevice().Entities())
	binding, err := eebusruntime.NewOutputBinding(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if binding.AdvertisementCount() != 1 || binding.ServiceCount() != 1 || binding.DeviceType() != "EnergyManagementSystem" {
		t.Fatalf("ads=%d services=%d type=%q", binding.AdvertisementCount(), binding.ServiceCount(), binding.DeviceType())
	}
	if types := binding.EntityTypes(); len(types) != 1 || types[0] != "CEM" {
		t.Fatalf("entities before a lens result %v", types)
	}
	if outputNodeEntity(service.LocalDevice().Entities(), spinemodel.EntityTypeTypeInverter) != nil {
		t.Fatal("inverter before a lens result")
	}
	loss := binding.Apply(eebusruntime.LensResult{
		Family: "inverter", ResourceID: "north", LensAdmitted: true, RuntimeEnabled: true,
		Elements: []string{"temperature"},
		Operations: []eebusruntime.LensOperation{{
			Name: "pv.operation.set_active_power_limit", Unit: "W", OwnCapability: true,
		}},
	})
	if len(loss) != 0 {
		t.Fatalf("loss %v", loss)
	}
	loss = binding.Apply(eebusruntime.LensResult{
		Family: "pv string", ResourceID: "east", ParentResourceID: "north",
		LensAdmitted: true, RuntimeEnabled: true, ContainsCited: true,
	})
	if len(loss) != 0 {
		t.Fatalf("string loss %v", loss)
	}
	all := service.LocalDevice().Entities()
	if len(all) != entities+2 {
		t.Fatalf("entities on the node %d, want %d", len(all), entities+2)
	}
	inverter := outputNodeEntity(all, spinemodel.EntityTypeTypeInverter)
	if inverter == nil {
		t.Fatal("runtime node has no inverter")
	}
	for _, featureType := range []spinemodel.FeatureTypeType{spinemodel.FeatureTypeTypeMeasurement, spinemodel.FeatureTypeTypeLoadControl} {
		if inverter.FeatureOfTypeAndRole(featureType, spinemodel.RoleTypeServer) == nil {
			t.Fatalf("inverter has no %s server feature", featureType)
		}
	}
	pvString := outputNodeEntity(all, spinemodel.EntityTypeTypePVString)
	if pvString == nil || len(pvString.Address().Entity) != len(inverter.Address().Entity)+1 {
		t.Fatal("string is not nested under the inverter on the runtime node")
	}
	if binding.AdvertisementCount() != 1 || binding.ServiceCount() != 1 || binding.DeviceType() != "EnergyManagementSystem" {
		t.Fatal("existing node changed")
	}
}

func outputNodeEntity(entities []spineapi.EntityLocalInterface, entityType spinemodel.EntityTypeType) spineapi.EntityLocalInterface {
	for _, entity := range entities {
		if entity.EntityType() == entityType {
			return entity
		}
	}
	return nil
}
