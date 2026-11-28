package charger_test

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	eebusruntime "github.com/Project-Helianthus/helianthus-eebusreg"
	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusfacade"
)

const chargerNodeToken = "00112233445566778899aabbccddeeff"

func TestChargerOutputServedOnEVSE(t *testing.T) {
	// Apply adds the EVSE entity and serves it. It attaches measurement and electrical
	// connection for Monitoring of Power Consumption and device diagnosis for EVSE
	// Commissioning and Configuration on that entity. The monitored unit is the EVSE
	// entity, so the CEM entity gains no feature. The caller of Apply reads the device
	// classification gap from MonitoringLoss.
	binding, node := newChargerBinding(t)
	result := chargerApplyLens("bay")
	if loss := binding.Apply(result); len(loss) != 0 {
		t.Fatalf("apply loss %v", loss)
	}
	features := binding.ServerFeatures("bay")
	for _, want := range []string{"measurement", "electrical connection", "device diagnosis"} {
		if !chargerHas(features, want) {
			t.Fatalf("missing %s in %v", want, features)
		}
	}
	if chargerHas(features, "load control") {
		t.Fatalf("load control without a current operation %v", features)
	}
	if got := node.ServerFeatures("cem"); len(got) != 0 {
		t.Fatalf("cem features %v", got)
	}
	if !chargerHas(binding.EntityTypes(), "EVSE") {
		t.Fatalf("entities %v", binding.EntityTypes())
	}
	gaps := binding.MonitoringLoss("bay")
	if len(gaps) != 1 || gaps[0] != "device classification" {
		t.Fatalf("monitoring loss %v", gaps)
	}
}

func TestChargerOutputApplyMandatoryElement(t *testing.T) {
	// Apply serves each use case only when its mandatory element is admitted.
	// operating state: absent leaves device diagnosis off the EVSE entity and names the element.
	// total active power: absent leaves measurement and electrical connection off and names the element.
	// missing element: an element the lens also names as missing is not admitted either.
	cases := []struct {
		name    string
		mutate  func(*eebusruntime.LensResult)
		element string
		off     []string
		on      []string
	}{
		{"operating state", func(r *eebusruntime.LensResult) {
			r.Elements = chargerDrop(r.Elements, "operating state")
		}, "operating state", []string{"device diagnosis"}, []string{"measurement", "electrical connection"}},
		{"total active power", func(r *eebusruntime.LensResult) {
			r.Elements = chargerDrop(r.Elements, "total active power")
		}, "total active power", []string{"measurement", "electrical connection"}, []string{"device diagnosis"}},
		{"missing total", func(r *eebusruntime.LensResult) { r.MissingElements = []string{"total active power"} },
			"total active power", []string{"measurement", "electrical connection"}, []string{"device diagnosis"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			binding, _ := newChargerBinding(t)
			result := chargerApplyLens("bay")
			tc.mutate(&result)
			binding.Apply(result)
			features := binding.ServerFeatures("bay")
			for _, off := range tc.off {
				if chargerHas(features, off) {
					t.Fatalf("%s served in %v", off, features)
				}
			}
			for _, on := range tc.on {
				if !chargerHas(features, on) {
					t.Fatalf("%s missing in %v", on, features)
				}
			}
			gaps := binding.MonitoringLoss("bay")
			if !chargerHas(gaps, tc.element) || !chargerHas(gaps, "device classification") {
				t.Fatalf("monitoring loss %v", gaps)
			}
		})
	}
}

func TestChargerOutputApplyConsumptionLimit(t *testing.T) {
	// A Limitation of Power Consumption result applied on a charger keeps its returned
	// loss, and MonitoringLoss gives the caller both write entries: the missing active-power
	// operation and the allocated current operation that was not called.
	binding, _ := newChargerBinding(t)
	result := chargerApplyLens("bay")
	result.UseCase = "Limitation of Power Consumption"
	result.Operations = []eebusruntime.LensOperation{{Name: "evse.operation.set_allocated_current", Unit: "A", OwnCapability: true}}
	loss := binding.Apply(result)
	if len(loss) != 1 || loss[0] != "consumption_limit_not_applied" {
		t.Fatalf("apply loss %v", loss)
	}
	gaps := binding.MonitoringLoss("bay")
	for _, want := range []string{
		"missing operation: active power consumption limit",
		"not called: evse.operation.set_allocated_current",
		"device classification",
	} {
		if !chargerHas(gaps, want) {
			t.Fatalf("monitoring loss omits %s: %v", want, gaps)
		}
	}
	if chargerHas(gaps, "Limitation of Power Consumption") {
		t.Fatalf("monitoring loss %v", gaps)
	}

	plain := chargerApplyLens("other")
	binding.Apply(plain)
	if gaps := binding.MonitoringLoss("other"); chargerHas(gaps, "not called: evse.operation.set_allocated_current") {
		t.Fatalf("write loss without a write %v", gaps)
	}
}

func TestChargerOutputApplyEVActor(t *testing.T) {
	// A charger result whose actor is the EV adds the EVSE entity but serves no use case on it.
	// MonitoringLoss names the EV.
	binding, _ := newChargerBinding(t)
	result := chargerApplyLens("bay")
	result.Actor = "EV"
	binding.Apply(result)
	if got := binding.ServerFeatures("bay"); len(got) != 0 {
		t.Fatalf("features %v", got)
	}
	if gaps := binding.MonitoringLoss("bay"); len(gaps) != 1 || gaps[0] != "EV" {
		t.Fatalf("monitoring loss %v", gaps)
	}
}

type chargerRuntime struct {
	node *openedNode
}

// openedNode is the facade node type. The exported method signature names this alias.
type openedNode = eebusfacade.OutputNode

func (runtime *chargerRuntime) OutputNode() (*openedNode, error) {
	if runtime == nil || runtime.node == nil {
		return nil, errors.New("existing node is unavailable")
	}
	return runtime.node, nil
}

func (runtime *chargerRuntime) Start(context.Context) error { return nil }

func (runtime *chargerRuntime) Shutdown() error { return nil }

func (runtime *chargerRuntime) Snapshot() (eebusruntime.SnapshotV1, error) {
	return eebusruntime.SnapshotV1{}, nil
}

func (runtime *chargerRuntime) PairingState() ([]eebusruntime.PairingObservationV1, error) {
	return nil, nil
}

func (runtime *chargerRuntime) FeaturesGet(context.Context, eebusraw.ReadAuthorizationV1, eebusraw.FeaturesGetRequestV1) (eebusraw.FeaturesGetDataV1, *eebusraw.ErrorV1) {
	return eebusraw.FeaturesGetDataV1{}, nil
}

func (runtime *chargerRuntime) FeaturesDataGet(context.Context, eebusraw.ReadAuthorizationV1, eebusraw.FeatureDataGetRequestV1) (eebusraw.FeatureDataGetDataV1, *eebusraw.ErrorV1) {
	return eebusraw.FeatureDataGetDataV1{}, nil
}

var chargerListenPort = 4791

func newChargerBinding(t *testing.T) (*eebusruntime.OutputBinding, *eebusfacade.OutputNode) {
	t.Helper()
	port := chargerListenPort
	chargerListenPort++
	service, err := eebusfacade.NewPreparedOutputService(eebusfacade.RuntimeConfig{
		Interface:     "fixture-interface",
		ListenPort:    port,
		ListenAddress: netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(port)),
	}, chargerNodeToken)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { service.Shutdown() })
	backend := eebusfacade.NewOutputServiceBackend(service)
	t.Cleanup(func() { _ = backend.Close() })
	node, err := eebusfacade.OpenOutputNode(backend)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := eebusruntime.NewOutputBinding(&chargerRuntime{node: node}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return binding, node
}

func chargerApplyLens(resourceID string) eebusruntime.LensResult {
	return eebusruntime.LensResult{
		Family: "charger", ResourceID: resourceID, LensAdmitted: true, RuntimeEnabled: true,
		Elements: []string{
			"total active power", "phase active power", "phase current", "phase voltage", "frequency",
			"session energy", "lifetime energy", "operating state", "charging status", "connection status",
			"fault status", "interlock status", "readiness status",
		},
	}
}

func chargerHas(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func chargerDrop(values []string, drop string) []string {
	var kept []string
	for _, value := range values {
		if value != drop {
			kept = append(kept, value)
		}
	}
	return kept
}
