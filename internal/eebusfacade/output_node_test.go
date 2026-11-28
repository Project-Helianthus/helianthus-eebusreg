package eebusfacade

import (
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusservicebridge"
	shipcert "github.com/Project-Helianthus/helianthus-ship-go/cert"
	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
)

const (
	outputNodeSecondToken = "fedcba9876543210fedcba9876543210"
	outputNodeThirdToken  = "00112233445566778899aabbccddeeff"
)

func TestOutputNodeCountsFollowTheService(t *testing.T) {
	service, concrete, _ := newOutputNodeRuntime(t)
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
	service, concrete, backend := newOutputNodeRuntime(t)
	if _, err := OpenOutputNode(backend); err == nil {
		t.Fatal("node opened before the runtime service was prepared")
	}
	if err := service.Setup(); err != nil {
		t.Fatal(err)
	}
	node, err := OpenOutputNode(backend)
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
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenOutputNode(backend); err == nil {
		t.Fatal("node opened after the runtime service was released")
	}
}

func TestOutputNodeFailedAttachLeavesNoFeature(t *testing.T) {
	service, _, backend := newOutputNodeRuntime(t)
	if err := service.Setup(); err != nil {
		t.Fatal(err)
	}
	node, err := OpenOutputNode(backend)
	if err != nil {
		t.Fatal(err)
	}
	if err := node.AddEntity("Inverter", "na", ""); err != nil {
		t.Fatal(err)
	}
	original := attachOutputServerFeature
	attachOutputServerFeature = func(spineapi.EntityLocalInterface, spinemodel.FeatureTypeType) error {
		return errOutputNode
	}
	t.Cleanup(func() { attachOutputServerFeature = original })
	if err := node.AddServerFeature("na", "measurement"); err == nil {
		t.Fatal("a failed attach was reported as success")
	}
	if features := node.ObservedServerFeatures("na"); len(features) != 0 {
		t.Fatalf("a failed attach left server features %v", features)
	}
	if features := node.ServerFeatures("na"); len(features) != 0 {
		t.Fatalf("a failed attach is listed as %v", features)
	}
	attachOutputServerFeature = original
	if err := node.AddServerFeature("na", "measurement"); err != nil {
		t.Fatal(err)
	}
	if features := node.ServerFeatures("na"); len(features) != 1 || features[0] != "measurement" {
		t.Fatalf("server features after a later attach %v", features)
	}
	if err := node.AddServerFeature("na", "measurement"); err != nil {
		t.Fatal(err)
	}
	if features := node.ObservedServerFeatures("na"); len(features) != 1 {
		t.Fatalf("a repeated attach duplicated the feature %v", features)
	}
}

func TestOutputNodeAddsAllFeaturesOrNone(t *testing.T) {
	// The node accepts measurement and rejects electrical connection. The batch names the
	// rejected feature and leaves the entity without measurement. A later batch the node
	// accepts adds both, and asking again for a present feature adds no duplicate.
	service, _, backend := newOutputNodeRuntime(t)
	if err := service.Setup(); err != nil {
		t.Fatal(err)
	}
	node, err := OpenOutputNode(backend)
	if err != nil {
		t.Fatal(err)
	}
	if err := node.AddEntity("EVSE", "na", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := node.AddServerFeatures("nb", []string{"measurement"}); err == nil {
		t.Fatal("a batch on an entity the node did not add was accepted")
	}
	original := attachOutputServerFeature
	RejectOutputServerFeatureForTest(t, "electrical connection")
	rejected, err := node.AddServerFeatures("na", []string{"measurement", "electrical connection"})
	if err == nil || rejected != "electrical connection" {
		t.Fatalf("rejected %q err %v", rejected, err)
	}
	if features := node.ObservedServerFeatures("na"); len(features) != 0 {
		t.Fatalf("a rejected batch left server features %v", features)
	}
	if rejected, err := node.AddServerFeatures("na", []string{"device diagnosis", "unknown"}); err == nil || rejected != "unknown" {
		t.Fatalf("unknown feature rejected %q err %v", rejected, err)
	}
	if features := node.ObservedServerFeatures("na"); len(features) != 0 {
		t.Fatalf("a batch with an unknown feature left server features %v", features)
	}
	attachOutputServerFeature = original
	if _, err := node.AddServerFeatures("na", []string{"measurement", "electrical connection"}); err != nil {
		t.Fatal(err)
	}
	if _, err := node.AddServerFeatures("na", []string{"measurement", "load control"}); err != nil {
		t.Fatal(err)
	}
	if features := node.ServerFeatures("na"); !reflect.DeepEqual(features, []string{"electrical connection", "load control", "measurement"}) {
		t.Fatalf("server features %v", features)
	}
	if features := node.ObservedServerFeatures("na"); len(features) != 3 {
		t.Fatalf("a repeated feature was duplicated %v", features)
	}
}

func TestOutputNodeEntityAddressFollowsLetter(t *testing.T) {
	// The SPINE entity address comes from the stored letter, not from the
	// apply order, so a restart that applies in another order keeps it.
	addresses := func(order []string) map[string][]spinemodel.AddressEntityType {
		service, _, backend := newOutputNodeRuntime(t)
		if err := service.Setup(); err != nil {
			t.Fatal(err)
		}
		node, err := OpenOutputNode(backend)
		if err != nil {
			t.Fatal(err)
		}
		for _, letter := range order {
			if err := node.AddEntity("Inverter", letter, ""); err != nil {
				t.Fatal(err)
			}
		}
		if err := node.AddEntity("PV String", "c", "b"); err != nil {
			t.Fatal(err)
		}
		found := map[string][]spinemodel.AddressEntityType{}
		for _, letter := range []string{"a", "b", "c"} {
			found[letter] = append([]spinemodel.AddressEntityType(nil), node.byLetter[letter].Address().Entity...)
		}
		return found
	}
	first := addresses([]string{"a", "b"})
	second := addresses([]string{"b", "a"})
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("entity addresses moved with apply order: %v then %v", first, second)
	}
	if reflect.DeepEqual(first["a"], first["b"]) {
		t.Fatalf("two letters share an entity address %v", first)
	}
	if _, ok := letterEntityNumber("abcdefg"); ok {
		t.Fatal("an overlong letter address received an entity number")
	}
}

// RejectOutputServerFeatureForTest makes the runtime node refuse to attach one
// named server feature until the test ends, for the external binding test.
func RejectOutputServerFeatureForTest(t *testing.T, name string) {
	t.Helper()
	featureType, ok := serverFeatureType(name)
	if !ok {
		t.Fatalf("unknown server feature %q", name)
	}
	original := attachOutputServerFeature
	attachOutputServerFeature = func(entity spineapi.EntityLocalInterface, attached spinemodel.FeatureTypeType) error {
		if attached == featureType {
			return errOutputNode
		}
		return original(entity, attached)
	}
	t.Cleanup(func() { attachOutputServerFeature = original })
}

// PrepareOutputNodeRuntimeForTest prepares one runtime service without
// listening or announcing, for the external binding test.
func PrepareOutputNodeRuntimeForTest(t *testing.T) *eebusservicebridge.Service {
	t.Helper()
	service, _ := OpenPreparedOutputNodeForTest(t)
	return service
}

// OpenPreparedOutputNodeForTest prepares one runtime service and returns the
// node that runtime owns.
func OpenPreparedOutputNodeForTest(t *testing.T) (*eebusservicebridge.Service, *OutputNode) {
	t.Helper()
	service, concrete, backend := newOutputNodeRuntime(t)
	if err := service.Setup(); err != nil {
		t.Fatal(err)
	}
	node, err := OpenOutputNode(backend)
	if err != nil {
		t.Fatal(err)
	}
	return concrete, node
}

func newOutputNodeRuntime(t *testing.T) (runtimeService, *eebusservicebridge.Service, Backend) {
	t.Helper()
	return newOutputNodeRuntimeOn(t, 4711, runtimeTestNodeToken)
}

func newOutputNodeRuntimeOn(t *testing.T, port int, token string) (runtimeService, *eebusservicebridge.Service, Backend) {
	t.Helper()
	service, concrete := newOutputNodeService(t, port, token)
	backend := NewOutputServiceBackend(concrete)
	t.Cleanup(func() { _ = backend.Close() })
	return service, concrete, backend
}

func newOutputNodeService(t *testing.T, port int, token string) (runtimeService, *eebusservicebridge.Service) {
	t.Helper()
	certificate, err := shipcert.CreateCertificate("", "Helianthus", "RO", "binding")
	if err != nil {
		t.Fatal(err)
	}
	service, err := newEEBusService(RuntimeConfig{
		Interface:     "fixture-interface",
		ListenPort:    port,
		ListenAddress: netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(port)),
	}, runtimeMaterial{
		certificate: certificate,
		localSKI:    certificateSKI(t, certificate),
		nodeToken:   token,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	concrete, ok := service.(*eebusservicebridge.Service)
	if !ok || concrete == nil {
		t.Fatal("runtime service is not the bridge service")
	}
	t.Cleanup(func() { concrete.Shutdown() })
	return service, concrete
}

func TestOutputNodeOneInstanceReplacedOtherServes(t *testing.T) {
	// Replacing or stopping one runtime releases only that instance's node.
	serviceA, concreteA, backendA := newOutputNodeRuntimeOn(t, 4711, runtimeTestNodeToken)
	serviceB, concreteB, backendB := newOutputNodeRuntimeOn(t, 4712, outputNodeSecondToken)
	if err := serviceA.Setup(); err != nil {
		t.Fatal(err)
	}
	if err := serviceB.Setup(); err != nil {
		t.Fatal(err)
	}
	nodeA, err := OpenOutputNode(backendA)
	if err != nil {
		t.Fatal(err)
	}
	nodeB, err := OpenOutputNode(backendB)
	if err != nil {
		t.Fatal(err)
	}
	if nodeA == nodeB || nodeA.service == nodeB.service {
		t.Fatal("two instances share a node")
	}
	if err := nodeA.AddEntity("Inverter", "na", ""); err != nil {
		t.Fatal(err)
	}
	if err := nodeB.AddEntity("EVSE", "nb", ""); err != nil {
		t.Fatal(err)
	}
	if !outputNodeHasEntity(concreteA, spinemodel.EntityTypeTypeInverter) || outputNodeHasEntity(concreteA, spinemodel.EntityTypeTypeEVSE) {
		t.Fatal("instance A has the wrong entities")
	}
	if !outputNodeHasEntity(concreteB, spinemodel.EntityTypeTypeEVSE) || outputNodeHasEntity(concreteB, spinemodel.EntityTypeTypeInverter) {
		t.Fatal("instance B has the wrong entities")
	}
	_, replacement := newOutputNodeService(t, 4713, outputNodeThirdToken)
	if err := replacement.Setup(); err != nil {
		t.Fatal(err)
	}
	ReplaceOutputService(backendA, replacement)
	replaced, err := OpenOutputNode(backendA)
	if err != nil {
		t.Fatal(err)
	}
	if replaced == nodeA || replaced.service != replacement {
		t.Fatal("replacement kept the old node")
	}
	if outputNodeHasEntity(replacement, spinemodel.EntityTypeTypeInverter) {
		t.Fatal("the new service kept the old entity")
	}
	if !outputNodeHasEntity(concreteA, spinemodel.EntityTypeTypeInverter) {
		t.Fatal("replacing the node removed the old service's entity")
	}
	stillB, err := OpenOutputNode(backendB)
	if err != nil || stillB != nodeB || stillB.service != concreteB {
		t.Fatal("replacing instance A replaced instance B")
	}
	if err := nodeB.AddEntity("Inverter", "nc", ""); err != nil {
		t.Fatal(err)
	}
	if err := backendA.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenOutputNode(backendA); err == nil {
		t.Fatal("closed instance A still opened a node")
	}
	stillB, err = OpenOutputNode(backendB)
	if err != nil || stillB != nodeB {
		t.Fatal("closing instance A released instance B")
	}
	if err := nodeB.AddEntity("PV String", "nd", "nc"); err != nil {
		t.Fatal(err)
	}
	if !nodeB.NestedUnder("nd", "nc") || !outputNodeHasEntity(concreteB, spinemodel.EntityTypeTypePVString) {
		t.Fatal("instance B stopped serving")
	}
}

func TestOutputNodeAcquisitionFailureReleasesOnlyThatInstance(t *testing.T) {
	// A failed acquisition releases the node it adopted and leaves the other instance.
	serviceB, concreteB, backendB := newOutputNodeRuntimeOn(t, 4712, outputNodeSecondToken)
	if err := serviceB.Setup(); err != nil {
		t.Fatal(err)
	}
	nodeB, err := OpenOutputNode(backendB)
	if err != nil {
		t.Fatal(err)
	}
	if err := nodeB.AddEntity("EVSE", "nb", ""); err != nil {
		t.Fatal(err)
	}
	acquisition := newOutputNodeAcquisition(t, 4711, runtimeTestNodeToken)
	if err := acquisition.openService(nil); err != nil {
		t.Fatal(err)
	}
	if err := acquisition.service.Setup(); err != nil {
		t.Fatal(err)
	}
	nodeA, err := acquisition.output.open()
	if err != nil {
		t.Fatal(err)
	}
	if nodeA == nodeB || nodeA.service == concreteB {
		t.Fatal("the acquisition adopted the other instance")
	}
	if err := nodeA.AddEntity("Inverter", "na", ""); err != nil {
		t.Fatal(err)
	}
	concreteA, ok := acquisition.service.(*eebusservicebridge.Service)
	if !ok || !outputNodeHasEntity(concreteA, spinemodel.EntityTypeTypeInverter) || outputNodeHasEntity(concreteB, spinemodel.EntityTypeTypeInverter) {
		t.Fatal("the acquisition wrote the other instance")
	}
	if err := acquisition.closeRuntime(errOutputNode); err == nil {
		t.Fatal("acquisition failure reported success")
	}
	if _, err := acquisition.output.open(); err == nil {
		t.Fatal("the released acquisition still opened a node")
	}
	stillB, err := OpenOutputNode(backendB)
	if err != nil || stillB != nodeB || !outputNodeHasEntity(concreteB, spinemodel.EntityTypeTypeEVSE) {
		t.Fatal("the failed acquisition released the other instance")
	}
	if err := nodeB.AddEntity("Inverter", "nc", ""); err != nil {
		t.Fatal(err)
	}
}

func TestOutputNodeStartMovesOutputOntoTheBackend(t *testing.T) {
	// Starting one acquisition moves only that acquisition's output onto its backend.
	kept := &fakeRuntimeService{started: make(chan struct{})}
	moved := &fakeRuntimeService{started: make(chan struct{})}
	keptSession := &outputSession{}
	movedSession := &outputSession{}
	staying := &runtimeAcquisition{service: kept, output: keptSession}
	leaving := &runtimeAcquisition{service: moved, output: movedSession}
	backend, err := leaving.startBackend()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	if leaving.output != nil {
		t.Fatal("the started acquisition kept the output session")
	}
	owner, ok := backend.(*serviceBackend)
	if !ok || owner.output != movedSession || owner.service != moved {
		t.Fatal("the backend did not receive the started acquisition's output session")
	}
	if staying.output != keptSession || staying.service != kept {
		t.Fatal("another acquisition lost its output session")
	}
}

func newOutputNodeAcquisition(t *testing.T, port int, token string) *runtimeAcquisition {
	t.Helper()
	certificate, err := shipcert.CreateCertificate("", "Helianthus", "RO", "binding")
	if err != nil {
		t.Fatal(err)
	}
	acquisition := &runtimeAcquisition{
		config: RuntimeConfig{
			Interface:     "fixture-interface",
			ListenPort:    port,
			ListenAddress: netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(port)),
		},
		material: runtimeMaterial{
			certificate: certificate,
			localSKI:    certificateSKI(t, certificate),
			nodeToken:   token,
		},
		dependencies:          runtimeDependencies{now: time.Now, newService: newEEBusService},
		outgoingAttemptBridge: newFirstTrustOutgoingAttemptBridge(nil),
	}
	t.Cleanup(func() {
		if acquisition.service != nil {
			acquisition.service.Shutdown()
		}
	})
	return acquisition
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
