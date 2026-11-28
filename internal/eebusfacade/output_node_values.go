package eebusfacade

import (
	"reflect"
	"sync"
	"unsafe"

	"github.com/Project-Helianthus/helianthus-eebus-go/features/server"
	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
	"github.com/Project-Helianthus/helianthus-spine-go/spine"
)

// outputUseCaseKey is the actor and use case name of one support entry.
type outputUseCaseKey struct {
	actor spinemodel.UseCaseActorType
	name  spinemodel.UseCaseNameType
}

// OutputUseCase is the use-case support registered on an added entity.
// Features are the server features that use case needs.
type OutputUseCase struct {
	Actor       spinemodel.UseCaseActorType
	Name        spinemodel.UseCaseNameType
	Version     spinemodel.SpecificationVersionType
	SubRevision string
	Available   bool
	Scenarios   []spinemodel.UseCaseScenarioSupportType
	Features    []string
}

// AddMeasurementDescription stores a measurement description and returns the
// identifier the measurement server assigned. A description that already
// carries an identifier is rejected.
func (n *OutputNode) AddMeasurementDescription(address string, description spinemodel.MeasurementDescriptionDataType) (spinemodel.MeasurementIdType, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	measurement, err := n.measurementLocked(address)
	if err != nil {
		return 0, err
	}
	identifier := measurement.AddDescription(description)
	if identifier == nil {
		return 0, errOutputNode
	}
	return *identifier, nil
}

// UpdateMeasurementValue writes the value, scale, value state and source of one
// measurement. clear removes elements before the update.
func (n *OutputNode) UpdateMeasurementValue(address string, identifier spinemodel.MeasurementIdType, data spinemodel.MeasurementDataType, clear *spinemodel.MeasurementDataElementsType) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	measurement, err := n.measurementLocked(address)
	if err != nil {
		return err
	}
	if err := measurement.UpdateDataForId(data, clear, identifier); err != nil {
		return errOutputNode
	}
	return nil
}

// AddMeasurementConstraints stores the constraint of one measurement. The
// measurement server helper has no constraint writer, so this uses the same
// data update that helper uses for a description.
func (n *OutputNode) AddMeasurementConstraints(address string, constraints spinemodel.MeasurementConstraintsDataType) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	feature, measurement, err := n.measurementFeatureLocked(address)
	if err != nil {
		return err
	}
	if constraints.MeasurementId == nil {
		return errOutputNode
	}
	if _, err := measurement.GetDescriptionForId(*constraints.MeasurementId); err != nil {
		return errOutputNode
	}
	list := &spinemodel.MeasurementConstraintsListDataType{
		MeasurementConstraintsData: []spinemodel.MeasurementConstraintsDataType{constraints},
	}
	if err := feature.UpdateData(spinemodel.FunctionTypeMeasurementConstraintsListData, list, spinemodel.NewFilterTypePartial(), nil); err != nil {
		return errOutputNode
	}
	return nil
}

// AddElectricalDescription stores one electrical connection description.
func (n *OutputNode) AddElectricalDescription(address string, description spinemodel.ElectricalConnectionDescriptionDataType) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	connection, err := n.electricalLocked(address)
	if err != nil {
		return err
	}
	if err := connection.AddDescription(description); err != nil {
		return errOutputNode
	}
	return nil
}

// AddElectricalParameter stores one electrical connection parameter and returns
// the parameter identifier the server assigned.
func (n *OutputNode) AddElectricalParameter(address string, description spinemodel.ElectricalConnectionParameterDescriptionDataType) (spinemodel.ElectricalConnectionParameterIdType, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	connection, err := n.electricalLocked(address)
	if err != nil {
		return 0, err
	}
	identifier := connection.AddParameterDescription(description)
	if identifier == nil {
		return 0, errOutputNode
	}
	return *identifier, nil
}

// AddElectricalCharacteristic stores one characteristic and returns the
// characteristic identifier the server assigned.
func (n *OutputNode) AddElectricalCharacteristic(address string, data spinemodel.ElectricalConnectionCharacteristicDataType) (spinemodel.ElectricalConnectionCharacteristicIdType, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	connection, err := n.electricalLocked(address)
	if err != nil {
		return 0, err
	}
	identifier, err := connection.AddCharacteristic(data)
	if err != nil || identifier == nil {
		return 0, errOutputNode
	}
	return *identifier, nil
}

// UpdateElectricalCharacteristic updates one stored characteristic. clear
// removes elements before the update.
func (n *OutputNode) UpdateElectricalCharacteristic(address string, data spinemodel.ElectricalConnectionCharacteristicDataType, clear *spinemodel.ElectricalConnectionCharacteristicDataElementsType) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	connection, err := n.electricalLocked(address)
	if err != nil {
		return err
	}
	if err := connection.UpdateCharacteristic(data, clear); err != nil {
		return errOutputNode
	}
	return nil
}

// SetDeviceDiagnosisState stores the DeviceDiagnosis state of an added entity.
// The pinned diagnosis server constructor selects the configuration feature, so
// the state is written on the diagnosis server feature.
func (n *OutputNode) SetDeviceDiagnosisState(address string, state *spinemodel.DeviceDiagnosisStateDataType) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	feature, _, err := n.serverFeatureLocked(address, spinemodel.FeatureTypeTypeDeviceDiagnosis)
	if err != nil {
		return err
	}
	if state == nil {
		return errOutputNode
	}
	feature.SetData(spinemodel.FunctionTypeDeviceDiagnosisStateData, state)
	n.ensureHeartbeatLocked(address)
	return nil
}

// AddConfigurationDescription stores one device configuration key description
// and returns the key identifier the server assigned.
func (n *OutputNode) AddConfigurationDescription(address string, description spinemodel.DeviceConfigurationKeyValueDescriptionDataType) (spinemodel.DeviceConfigurationKeyIdType, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	configuration, err := n.configurationLocked(address)
	if err != nil {
		return 0, err
	}
	identifier := configuration.AddKeyValueDescription(description)
	if identifier == nil {
		return 0, errOutputNode
	}
	return *identifier, nil
}

// UpdateConfigurationValue writes the value of one configuration key. clear
// removes elements before the update.
func (n *OutputNode) UpdateConfigurationValue(address string, identifier spinemodel.DeviceConfigurationKeyIdType, data spinemodel.DeviceConfigurationKeyValueDataType, clear *spinemodel.DeviceConfigurationKeyValueDataElementsType) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	configuration, err := n.configurationLocked(address)
	if err != nil {
		return err
	}
	if err := configuration.UpdateKeyValueDataForKeyId(data, clear, identifier); err != nil {
		return errOutputNode
	}
	return nil
}

// RegisterUseCase registers use-case support on an added entity and adds the
// server features that use case needs. A rejected feature registers nothing
// and leaves the features of other use cases in place.
func (n *OutputNode) RegisterUseCase(address string, support OutputUseCase) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.byLetter[address] == nil || support.Actor == "" || support.Name == "" || support.Version == "" {
		return "", errOutputNode
	}
	rejected, err := n.addServerFeaturesLocked(address, support.Features)
	if err != nil {
		return rejected, err
	}
	scenarios := append([]spinemodel.UseCaseScenarioSupportType(nil), support.Scenarios...)
	n.byLetter[address].AddUseCaseSupport(support.Actor, support.Name, support.Version, support.SubRevision, support.Available, scenarios)
	if err := n.noteUseCaseLocked(address, support); err != nil {
		return "", err
	}
	n.ensureHeartbeatLocked(address)
	return "", nil
}

// WithdrawUseCase removes one use-case support entry and every server feature
// that only that use case needed.
func (n *OutputNode) WithdrawUseCase(address string, actor spinemodel.UseCaseActorType, name spinemodel.UseCaseNameType) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	entity := n.byLetter[address]
	if entity == nil || actor == "" || name == "" {
		return errOutputNode
	}
	key := outputUseCaseKey{actor: actor, name: name}
	features := append([]string(nil), n.useCases[address][key]...)
	if err := n.dropFeaturesLocked(address, features, key); err != nil {
		return err
	}
	if n.useCases[address] != nil {
		delete(n.useCases[address], key)
	}
	entity.RemoveUseCaseSupport(actor, name)
	n.ensureHeartbeatLocked(address)
	return nil
}

func (n *OutputNode) noteUseCaseLocked(address string, support OutputUseCase) error {
	if n.useCases == nil {
		n.useCases = map[string]map[outputUseCaseKey][]string{}
	}
	if n.useCases[address] == nil {
		n.useCases[address] = map[outputUseCaseKey][]string{}
	}
	key := outputUseCaseKey{actor: support.Actor, name: support.Name}
	previous := append([]string(nil), n.useCases[address][key]...)
	n.useCases[address][key] = append([]string(nil), support.Features...)
	return n.dropFeaturesLocked(address, previous, outputUseCaseKey{})
}

func (n *OutputNode) dropFeaturesLocked(address string, names []string, except outputUseCaseKey) error {
	needed := map[string]struct{}{}
	for key, features := range n.useCases[address] {
		if key == except {
			continue
		}
		for _, name := range features {
			needed[name] = struct{}{}
		}
	}
	entity := n.byLetter[address]
	for _, name := range names {
		if _, keep := needed[name]; keep {
			continue
		}
		featureType, ok := serverFeatureType(name)
		if !ok || entity == nil || entity.FeatureOfTypeAndRole(featureType, spinemodel.RoleTypeServer) == nil {
			continue
		}
		if featureType == spinemodel.FeatureTypeTypeDeviceDiagnosis {
			n.stopHeartbeatLocked(entity)
		}
		if !removeLocalServerFeature(entity, featureType) {
			return errOutputNode
		}
	}
	return nil
}

func (n *OutputNode) ensureHeartbeatLocked(address string) {
	entity := n.byLetter[address]
	if entity == nil || entity.HeartbeatManager() == nil {
		return
	}
	feature := entity.FeatureOfTypeAndRole(spinemodel.FeatureTypeTypeDeviceDiagnosis, spinemodel.RoleTypeServer)
	if feature == nil {
		n.stopHeartbeatLocked(entity)
		return
	}
	feature.AddFunctionType(spinemodel.FunctionTypeDeviceDiagnosisHeartbeatData, true, false)
	if !entity.HeartbeatManager().IsHeartbeatRunning() {
		_ = entity.HeartbeatManager().StartHeartbeat()
	}
}

func (n *OutputNode) stopHeartbeats() {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, entity := range n.byLetter {
		n.stopHeartbeatLocked(entity)
	}
}

func (n *OutputNode) stopHeartbeatLocked(entity spineapi.EntityLocalInterface) {
	if entity == nil || entity.HeartbeatManager() == nil {
		return
	}
	entity.HeartbeatManager().StopHeartbeat()
}

func (n *OutputNode) measurementLocked(address string) (*server.Measurement, error) {
	_, measurement, err := n.measurementFeatureLocked(address)
	return measurement, err
}

func (n *OutputNode) measurementFeatureLocked(address string) (spineapi.FeatureLocalInterface, *server.Measurement, error) {
	feature, entity, err := n.serverFeatureLocked(address, spinemodel.FeatureTypeTypeMeasurement)
	if err != nil {
		return nil, nil, err
	}
	measurement, err := server.NewMeasurement(entity)
	if err != nil || measurement == nil {
		return nil, nil, errOutputNode
	}
	return feature, measurement, nil
}

func (n *OutputNode) electricalLocked(address string) (*server.ElectricalConnection, error) {
	_, entity, err := n.serverFeatureLocked(address, spinemodel.FeatureTypeTypeElectricalConnection)
	if err != nil {
		return nil, err
	}
	connection, err := server.NewElectricalConnection(entity)
	if err != nil || connection == nil {
		return nil, errOutputNode
	}
	return connection, nil
}

func (n *OutputNode) configurationLocked(address string) (*server.DeviceConfiguration, error) {
	_, entity, err := n.serverFeatureLocked(address, spinemodel.FeatureTypeTypeDeviceConfiguration)
	if err != nil {
		return nil, err
	}
	configuration, err := server.NewDeviceConfiguration(entity)
	if err != nil || configuration == nil {
		return nil, errOutputNode
	}
	return configuration, nil
}

func (n *OutputNode) serverFeatureLocked(address string, featureType spinemodel.FeatureTypeType) (spineapi.FeatureLocalInterface, spineapi.EntityLocalInterface, error) {
	entity := n.byLetter[address]
	if entity == nil {
		return nil, nil, errOutputNode
	}
	feature := entity.FeatureOfTypeAndRole(featureType, spinemodel.RoleTypeServer)
	if feature == nil {
		return nil, nil, errOutputNode
	}
	return feature, entity, nil
}

// removeLocalServerFeature drops one server feature. The local entity exposes
// no removal method, so the feature slice is edited directly.
func removeLocalServerFeature(entity spineapi.EntityLocalInterface, featureType spinemodel.FeatureTypeType) bool {
	concrete, ok := entity.(*spine.EntityLocal)
	if !ok || concrete == nil {
		return false
	}
	localType := reflect.TypeOf(concrete).Elem()
	featuresField, featuresOK := localType.FieldByName("features")
	muxField, muxOK := localType.FieldByName("mux")
	if !featuresOK || !muxOK {
		return false
	}
	base := unsafe.Pointer(concrete)
	mux := (*sync.Mutex)(unsafe.Add(base, muxField.Offset))
	mux.Lock()
	defer mux.Unlock()
	features := (*[]spineapi.FeatureLocalInterface)(unsafe.Add(base, featuresField.Offset))
	kept := make([]spineapi.FeatureLocalInterface, 0, len(*features))
	removed := false
	for _, feature := range *features {
		if feature != nil && feature.Type() == featureType && feature.Role() == spinemodel.RoleTypeServer {
			removed = true
			continue
		}
		kept = append(kept, feature)
	}
	if !removed {
		return false
	}
	*features = kept
	return true
}
