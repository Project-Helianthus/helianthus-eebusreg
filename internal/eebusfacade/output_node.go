package eebusfacade

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/Project-Helianthus/helianthus-eebus-go/features/server"
	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusservicebridge"
	shipcert "github.com/Project-Helianthus/helianthus-ship-go/cert"
	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
	"github.com/Project-Helianthus/helianthus-spine-go/spine"
)

var errOutputNode = errors.New("existing node is unavailable")

// OutputNode is the node of one runtime service. It adds entities and server
// features on that service and never composes a second service.
type OutputNode struct {
	mu       sync.Mutex
	service  *eebusservicebridge.Service
	byLetter map[string]spineapi.EntityLocalInterface
	useCases map[string]map[outputUseCaseKey][]string
}

// outputSession is the output node owned by one runtime instance.
type outputSession struct {
	mu      sync.Mutex
	service *eebusservicebridge.Service
	node    *OutputNode
}

func (session *outputSession) adopt(service *eebusservicebridge.Service) {
	if session == nil {
		return
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.node != nil && (service == nil || session.node.service != service) {
		session.node.stopHeartbeats()
		session.node = nil
	}
	session.service = service
}

func (session *outputSession) release(service runtimeService) {
	if session == nil {
		return
	}
	concrete, _ := service.(*eebusservicebridge.Service)
	session.mu.Lock()
	defer session.mu.Unlock()
	if concrete == nil || session.service != concrete {
		return
	}
	if session.node != nil {
		session.node.stopHeartbeats()
	}
	session.service = nil
	session.node = nil
}

func (session *outputSession) open() (*OutputNode, error) {
	if session == nil {
		return nil, errOutputNode
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	adopted := session.service
	if session.node != nil && session.node.service == adopted && nodeReady(adopted) {
		return session.node, nil
	}
	node, err := NewOutputNode(adopted)
	if err != nil {
		return nil, err
	}
	session.node = node
	return session.node, nil
}

// OpenOutputNode returns the node stored for this runtime backend once its
// service is prepared. Without one it fails and composes nothing.
func OpenOutputNode(backend Backend) (*OutputNode, error) {
	owner, ok := backend.(*serviceBackend)
	if !ok || owner == nil {
		return nil, errOutputNode
	}
	return owner.openOutput()
}

// NewOutputNode returns the node of a prepared service.
func NewOutputNode(service *eebusservicebridge.Service) (*OutputNode, error) {
	if !nodeReady(service) {
		return nil, errOutputNode
	}
	return &OutputNode{
		service:  service,
		byLetter: map[string]spineapi.EntityLocalInterface{},
	}, nil
}

// NewOutputServiceBackend owns the output node of one prepared service.
func NewOutputServiceBackend(service *eebusservicebridge.Service) Backend {
	backend := &serviceBackend{
		service:   service,
		output:    &outputSession{},
		closeDone: make(chan struct{}),
	}
	backend.output.adopt(service)
	return backend
}

// ReplaceOutputService releases the old node of this backend and adopts service.
func ReplaceOutputService(backend Backend, service *eebusservicebridge.Service) {
	owner, ok := backend.(*serviceBackend)
	if !ok || owner == nil {
		return
	}
	owner.replaceOutput(service)
}

func (backend *serviceBackend) openOutput() (*OutputNode, error) {
	if backend == nil {
		return nil, errOutputNode
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.closed || backend.output == nil {
		return nil, errOutputNode
	}
	return backend.output.open()
}

func (backend *serviceBackend) releaseOutput() {
	if backend == nil {
		return
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.output == nil {
		return
	}
	backend.output.release(backend.service)
	backend.output = nil
}

func (backend *serviceBackend) replaceOutput(service runtimeService) {
	concrete, _ := service.(*eebusservicebridge.Service)
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.output == nil {
		backend.output = &outputSession{}
	}
	backend.service = service
	backend.output.adopt(concrete)
}

func (acquisition *runtimeAcquisition) adoptOutput(service runtimeService) {
	concrete, _ := service.(*eebusservicebridge.Service)
	if concrete == nil {
		return
	}
	if acquisition.output == nil {
		acquisition.output = &outputSession{}
	}
	acquisition.output.adopt(concrete)
}

func (acquisition *runtimeAcquisition) releaseOutput() {
	if acquisition == nil || acquisition.output == nil {
		return
	}
	acquisition.output.release(acquisition.service)
}

func (acquisition *runtimeAcquisition) takeOutput() *outputSession {
	session := acquisition.output
	acquisition.output = nil
	return session
}

// NewPreparedOutputService constructs one local service and prepares it for an output node.
func NewPreparedOutputService(config RuntimeConfig, nodeToken string) (*eebusservicebridge.Service, error) {
	certificate, err := shipcert.CreateCertificate("", "Helianthus", "RO", "binding")
	if err != nil {
		return nil, err
	}
	service, err := newEEBusService(config, runtimeMaterial{certificate: certificate, nodeToken: nodeToken}, nil)
	if err != nil {
		return nil, err
	}
	concrete, ok := service.(*eebusservicebridge.Service)
	if !ok || concrete == nil {
		return nil, errors.New("released scoped service construction failed")
	}
	if err := concrete.Setup(); err != nil {
		return nil, err
	}
	return concrete, nil
}

func nodeReady(service *eebusservicebridge.Service) bool {
	return service != nil && service.LocalDevice() != nil && service.LocalService() != nil
}

// AdvertisementCount is one while the running service has an mDNS service name.
func (n *OutputNode) AdvertisementCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.service == nil || n.service.LocalService() == nil || n.service.Configuration() == nil {
		return 0
	}
	if n.service.Configuration().MdnsServiceName() == "" {
		return 0
	}
	return 1
}

// ServiceCount is one while the running service has a local SHIP service.
func (n *OutputNode) ServiceCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.service == nil || n.service.LocalService() == nil {
		return 0
	}
	return 1
}

// DeviceType is the configured device type when the local service agrees with it.
func (n *OutputNode) DeviceType() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.service == nil || n.service.Configuration() == nil || n.service.LocalService() == nil {
		return ""
	}
	configured := string(n.service.Configuration().DeviceType())
	if configured == "" || configured != n.service.LocalService().DeviceType() {
		return ""
	}
	return configured
}

// EntityTypes lists the CEM entity and each entity this node added.
func (n *OutputNode) EntityTypes() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	device := n.device()
	if device == nil {
		return nil
	}
	var types []string
	for _, entity := range device.Entities() {
		if entity.EntityType() == spinemodel.EntityTypeTypeCEM {
			types = append(types, "CEM")
			break
		}
	}
	for _, letter := range n.lettersLocked() {
		if name := publicEntityName(n.byLetter[letter].EntityType()); name != "" {
			types = append(types, name)
		}
	}
	return types
}

// ObservedEntityTypes reads the entity types present on the local device.
func (n *OutputNode) ObservedEntityTypes() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	device := n.device()
	if device == nil {
		return nil
	}
	types := make([]string, 0, len(device.Entities()))
	for _, entity := range device.Entities() {
		types = append(types, string(entity.EntityType()))
	}
	return types
}

// AddEntity adds an entity of kind on the local device, nested under the parent
// entity when one is named. The same address and kind adds nothing. A different
// kind at that address is rejected. The parent entity is added before its child.
func (n *OutputNode) AddEntity(kind, address, parent string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.byLetter == nil {
		n.byLetter = map[string]spineapi.EntityLocalInterface{}
	}
	if existing := n.byLetter[address]; existing != nil {
		if publicEntityName(existing.EntityType()) != kind {
			return errOutputNode
		}
		return nil
	}
	device := n.device()
	spineType, ok := spineEntityType(kind)
	if device == nil || !ok {
		return errOutputNode
	}
	entityAddress, err := n.entityAddress(device, address, parent)
	if err != nil {
		return err
	}
	timeout := time.Duration(0)
	if n.service.Configuration() != nil {
		timeout = n.service.Configuration().HeartbeatTimeout()
	}
	entity := spine.NewEntityLocal(device, spineType, entityAddress, timeout)
	device.AddEntity(entity)
	n.byLetter[address] = entity
	return nil
}

// outputEntityBase is the first entity number an output entity may take. The
// runtime composes the device information entity and the CEM entity below it.
const outputEntityBase = 2

// maxOutputLetters bounds a letter address so its entity number cannot overflow.
const maxOutputLetters = 6

// entityAddress derives the SPINE entity address from the stored letter address,
// so a resource identifier keeps its entity address whatever the apply order.
func (n *OutputNode) entityAddress(device spineapi.DeviceLocalInterface, address, parent string) ([]spinemodel.AddressEntityType, error) {
	next, ok := letterEntityNumber(address)
	if !ok {
		return nil, errOutputNode
	}
	var entityAddress []spinemodel.AddressEntityType
	if parent == "" {
		entityAddress = []spinemodel.AddressEntityType{next}
	} else {
		elder := n.byLetter[parent]
		if elder == nil || elder.Address() == nil {
			return nil, errOutputNode
		}
		prefix := elder.Address().Entity
		entityAddress = make([]spinemodel.AddressEntityType, len(prefix)+1)
		copy(entityAddress, prefix)
		entityAddress[len(prefix)] = next
	}
	if device.Entity(entityAddress) != nil {
		return nil, errOutputNode
	}
	return entityAddress, nil
}

// letterEntityNumber maps a letter address one to one onto an entity number at
// or above outputEntityBase: a is the base, z the base plus 25, aa the next.
func letterEntityNumber(address string) (spinemodel.AddressEntityType, bool) {
	if address == "" || len(address) > maxOutputLetters {
		return 0, false
	}
	value := 0
	for index := 0; index < len(address); index++ {
		letter := address[index]
		if letter < 'a' || letter > 'z' {
			return 0, false
		}
		value = value*26 + int(letter-'a') + 1
	}
	return spinemodel.AddressEntityType(outputEntityBase + value - 1), true
}

// NestedUnder reports whether the entity address is nested under the parent on the device.
func (n *OutputNode) NestedUnder(address, parent string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	child := n.byLetter[address]
	elder := n.byLetter[parent]
	if child == nil || elder == nil || child.Address() == nil || elder.Address() == nil {
		return false
	}
	childAddr := child.Address().Entity
	parentAddr := elder.Address().Entity
	if len(childAddr) <= len(parentAddr) {
		return false
	}
	for i := range parentAddr {
		if childAddr[i] != parentAddr[i] {
			return false
		}
	}
	return true
}

// ServerFeatures lists the server features of an added entity by public name.
func (n *OutputNode) ServerFeatures(address string) []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.featureNames(address, publicFeatureName)
}

// ObservedServerFeatures reads the server feature types present on the entity.
func (n *OutputNode) ObservedServerFeatures(address string) []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.featureNames(address, func(featureType spinemodel.FeatureTypeType) string {
		return string(featureType)
	})
}

func (n *OutputNode) featureNames(address string, name func(spinemodel.FeatureTypeType) string) []string {
	entity := n.byLetter[address]
	if entity == nil {
		return nil
	}
	var names []string
	for _, feature := range entity.Features() {
		if feature.Role() != spinemodel.RoleTypeServer {
			continue
		}
		if label := name(feature.Type()); label != "" {
			names = append(names, label)
		}
	}
	sort.Strings(names)
	return names
}

// attachOutputServerFeature attaches the server helper for a feature type.
var attachOutputServerFeature = attachServerFeature

// AddServerFeature adds one named server feature on an added entity. A failed
// attach leaves the entity unchanged.
func (n *OutputNode) AddServerFeature(address, name string) error {
	_, err := n.AddServerFeatures(address, []string{name})
	return err
}

// AddServerFeatures adds every named server feature on an added entity, or
// none of them. Each new feature is attached on a staged copy first, and the
// staged features join the entity only when every attach succeeds, so a
// rejected feature leaves the entity as it was. It returns the name of the
// feature the node rejected, or no name when the entity is not one it added.
func (n *OutputNode) AddServerFeatures(address string, names []string) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.addServerFeaturesLocked(address, names)
}

// addServerFeaturesLocked adds features while n.mu is held.
func (n *OutputNode) addServerFeaturesLocked(address string, names []string) (string, error) {
	entity := n.byLetter[address]
	if entity == nil {
		return "", errOutputNode
	}
	staged := stagedFeatureEntity{EntityLocalInterface: entity}
	for _, name := range names {
		featureType, ok := serverFeatureType(name)
		if !ok {
			return name, errOutputNode
		}
		if entity.FeatureOfTypeAndRole(featureType, spinemodel.RoleTypeServer) != nil {
			if err := attachOutputServerFeature(entity, featureType); err != nil {
				return name, err
			}
			continue
		}
		if staged.FeatureOfTypeAndRole(featureType, spinemodel.RoleTypeServer) == nil {
			feature := spine.NewFeatureLocal(entity.NextFeatureId(), entity, featureType, spinemodel.RoleTypeServer)
			feature.SetDescriptionString(string(featureType) + " Server")
			staged.staged = append(staged.staged, feature)
		}
		if err := attachOutputServerFeature(staged, featureType); err != nil {
			return name, err
		}
	}
	for _, feature := range staged.staged {
		entity.AddFeature(feature)
	}
	n.ensureHeartbeatLocked(address)
	return "", nil
}

// stagedFeatureEntity shows the staged server features to an attach before
// they join the entity.
type stagedFeatureEntity struct {
	spineapi.EntityLocalInterface
	staged []spineapi.FeatureLocalInterface
}

func (e stagedFeatureEntity) FeatureOfTypeAndRole(featureType spinemodel.FeatureTypeType, role spinemodel.RoleType) spineapi.FeatureLocalInterface {
	for _, feature := range e.staged {
		if feature.Type() == featureType && feature.Role() == role {
			return feature
		}
	}
	return e.EntityLocalInterface.FeatureOfTypeAndRole(featureType, role)
}

func attachServerFeature(entity spineapi.EntityLocalInterface, featureType spinemodel.FeatureTypeType) error {
	var err error
	switch featureType {
	case spinemodel.FeatureTypeTypeMeasurement:
		_, err = server.NewMeasurement(entity)
	case spinemodel.FeatureTypeTypeElectricalConnection:
		_, err = server.NewElectricalConnection(entity)
	case spinemodel.FeatureTypeTypeLoadControl:
		_, err = server.NewLoadControl(entity)
	case spinemodel.FeatureTypeTypeDeviceDiagnosis:
		if entity.FeatureOfTypeAndRole(featureType, spinemodel.RoleTypeServer) == nil {
			return errOutputNode
		}
		return nil
	case spinemodel.FeatureTypeTypeDeviceClassification:
		if entity.FeatureOfTypeAndRole(featureType, spinemodel.RoleTypeServer) == nil {
			return errOutputNode
		}
		return nil
	case spinemodel.FeatureTypeTypeDeviceConfiguration:
		_, err = server.NewDeviceConfiguration(entity)
	default:
		return errOutputNode
	}
	if err != nil {
		return errOutputNode
	}
	return nil
}

func (n *OutputNode) device() spineapi.DeviceLocalInterface {
	if n == nil || n.service == nil {
		return nil
	}
	return n.service.LocalDevice()
}

func (n *OutputNode) lettersLocked() []string {
	letters := make([]string, 0, len(n.byLetter))
	for letter := range n.byLetter {
		letters = append(letters, letter)
	}
	sort.Strings(letters)
	return letters
}

// outputEntityKinds maps each public entity name to its SPINE entity type.
// HVACRoom's constant is EntityTypeTypeHvacRoom; its type value is HVACRoom.
var outputEntityKinds = []struct {
	name  string
	spine spinemodel.EntityTypeType
}{
	{"Inverter", spinemodel.EntityTypeTypeInverter},
	{"PV String", spinemodel.EntityTypeTypePVString},
	{"EVSE", spinemodel.EntityTypeTypeEVSE},
	{"PV", spinemodel.EntityTypeTypePV},
	{"PVESHybrid", spinemodel.EntityTypeTypePVESHybrid},
	{"ElectricalStorage", spinemodel.EntityTypeTypeElectricalStorage},
	{"Battery", spinemodel.EntityTypeTypeBattery},
	{"HVACRoom", spinemodel.EntityTypeTypeHvacRoom},
	{"HeatingCircuit", spinemodel.EntityTypeTypeHeatingCircuit},
	{"HeatingZone", spinemodel.EntityTypeTypeHeatingZone},
	{"DHWCircuit", spinemodel.EntityTypeTypeDHWCircuit},
	{"TemperatureSensor", spinemodel.EntityTypeTypeTemperatureSensor},
	{"GridConnectionPointOfPremises", spinemodel.EntityTypeTypeGridConnectionPointOfPremises},
	{"SubMeterElectricity", spinemodel.EntityTypeTypeSubMeterElectricity},
	{"HeatPumpAppliance", spinemodel.EntityTypeTypeHeatPumpAppliance},
	{"EV", spinemodel.EntityTypeTypeEV},
}

func publicEntityName(entityType spinemodel.EntityTypeType) string {
	for _, kind := range outputEntityKinds {
		if kind.spine == entityType {
			return kind.name
		}
	}
	return ""
}

func spineEntityType(kind string) (spinemodel.EntityTypeType, bool) {
	for _, item := range outputEntityKinds {
		if item.name == kind {
			return item.spine, true
		}
	}
	return "", false
}

func publicFeatureName(featureType spinemodel.FeatureTypeType) string {
	switch featureType {
	case spinemodel.FeatureTypeTypeMeasurement:
		return "measurement"
	case spinemodel.FeatureTypeTypeDeviceDiagnosis:
		return "device diagnosis"
	case spinemodel.FeatureTypeTypeElectricalConnection:
		return "electrical connection"
	case spinemodel.FeatureTypeTypeLoadControl:
		return "load control"
	case spinemodel.FeatureTypeTypeDeviceClassification:
		return "device classification"
	case spinemodel.FeatureTypeTypeDeviceConfiguration:
		return "device configuration"
	default:
		return ""
	}
}

func serverFeatureType(name string) (spinemodel.FeatureTypeType, bool) {
	switch name {
	case "measurement":
		return spinemodel.FeatureTypeTypeMeasurement, true
	case "device diagnosis":
		return spinemodel.FeatureTypeTypeDeviceDiagnosis, true
	case "electrical connection":
		return spinemodel.FeatureTypeTypeElectricalConnection, true
	case "load control":
		return spinemodel.FeatureTypeTypeLoadControl, true
	case "device classification":
		return spinemodel.FeatureTypeTypeDeviceClassification, true
	case "device configuration":
		return spinemodel.FeatureTypeTypeDeviceConfiguration, true
	default:
		return "", false
	}
}
