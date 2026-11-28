package charger

import (
	"math"
	"math/big"
	"strconv"
	"sync"

	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusfacade"
	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
)

const (
	elementReportedTotal = "evse.ac.total_active_power"
	elementPhasePower    = "evse.ac.active_power"
	elementCurrent       = "evse.ac.current"
	elementVoltage       = "evse.ac.voltage"
	elementFrequency     = "evse.ac.frequency"
	elementLifetime      = "evse.energy.lifetime"
	elementExported      = "evse.energy.lifetime_exported"
	elementFault         = "evse.status.fault"

	unitWatt   = "unit.watt"
	unitAmpere = "unit.ampere"
	unitVolt   = "unit.volt"
	unitHertz  = "unit.hertz"
	unitJoule  = "unit.joule"

	kindQuantity = "quantity"
	kindSymbol   = "symbol"

	symbolPresent  = "present"
	symbolNotReady = "not_ready"
	symbolCharging = "charging"
	symbolIdle     = "idle"

	electricalConnectionID = 1
)

type wireRow struct {
	key        string
	scope      spinemodel.ScopeTypeType
	measure    spinemodel.MeasurementTypeType
	unit       spinemodel.UnitOfMeasurementType
	phase      spinemodel.ElectricalConnectionPhaseNameType
	reference  spinemodel.ElectricalConnectionPhaseNameType
	acType     spinemodel.ElectricalConnectionAcMeasurementTypeType
	variant    spinemodel.ElectricalConnectionMeasurandVariantType
	hasPhase   bool
	hasRef     bool
	hasType    bool
	hasVariant bool
	number     int64
	scale      int8
	calculated bool
}

type publishedIDs struct {
	measurement spinemodel.MeasurementIdType
	parameter   spinemodel.ElectricalConnectionParameterIdType
}

type publishedEntity struct {
	connection bool
	diagnosis  bool
	rows       map[string]publishedIDs
}

var (
	publicationMu sync.Mutex
	publications  = map[*eebusfacade.OutputNode]map[string]*publishedEntity{}
)

func publishCharger(node *eebusfacade.OutputNode, address string, result Result, announced []string) (string, error) {
	if node == nil || !letterAddress(address) {
		return "", nil
	}
	if listed(announced, useCasePowerMonitoring) {
		if err := publishMeasurements(node, address, result); err != nil {
			return featureMeasurement, err
		}
	} else if err := retireUnannouncedMeasurements(node, address); err != nil {
		return featureMeasurement, err
	}
	if listed(announced, useCaseEVSECommissioning) {
		if err := publishDiagnosis(node, address, result); err != nil {
			return featureDiagnosis, err
		}
		noteDiagnosis(node, address)
		return "", nil
	}
	if err := publishUnknownDiagnosis(node, address, result); err != nil {
		return featureDiagnosis, err
	}
	return "", nil
}

func publishMeasurements(node *eebusfacade.OutputNode, address string, result Result) error {
	rows := chargerRows(result)
	publicationMu.Lock()
	defer publicationMu.Unlock()
	entity := entityPublication(node, address)
	if publicationStale(node, address, entity, rows) {
		if err := retireMeasurementTable(node, address, entity); err != nil {
			return err
		}
	}
	if len(rows) == 0 {
		return nil
	}
	if err := replaceDeadMeasurementCache(node, address, entity, rows); err != nil {
		return err
	}
	if err := registerChargerUseCase(node, address, powerSupport()); err != nil {
		return err
	}
	if err := ensureConnection(node, address, entity); err != nil {
		return err
	}
	for _, row := range rows {
		if err := writeRow(node, address, entity, row); err != nil {
			return err
		}
	}
	return nil
}

// retireUnannouncedMeasurements withdraws a measurement table when this cut
// does not announce power monitoring. The previous total would otherwise stay
// readable.
func retireUnannouncedMeasurements(node *eebusfacade.OutputNode, address string) error {
	publicationMu.Lock()
	defer publicationMu.Unlock()
	byAddress := publications[node]
	if byAddress == nil {
		return nil
	}
	entity := byAddress[address]
	if entity == nil || (len(entity.rows) == 0 && !entity.connection) {
		return nil
	}
	return retireMeasurementTable(node, address, entity)
}

// replaceDeadMeasurementCache rebuilds ids after the entity at this address
// was removed. A cached id that the new measurement server does not hold is
// not a row that can be updated.
func replaceDeadMeasurementCache(node *eebusfacade.OutputNode, address string, entity *publishedEntity, rows []wireRow) error {
	if len(entity.rows) == 0 {
		return nil
	}
	for _, row := range rows {
		ids, ok := entity.rows[row.key]
		if !ok {
			continue
		}
		if node.UpdateMeasurementValue(address, ids.measurement, row.measurementValue(ids.measurement), row.clearSource()) == nil {
			return nil
		}
		return retireMeasurementTable(node, address, entity)
	}
	return nil
}

func publicationStale(node *eebusfacade.OutputNode, address string, entity *publishedEntity, rows []wireRow) bool {
	if len(entity.rows) == 0 && !entity.connection {
		return false
	}
	support := powerSupport()
	if !node.HasUseCaseSupport(address, support.Actor, support.Name) {
		return true
	}
	present := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		present[row.key] = struct{}{}
	}
	for key := range entity.rows {
		if _, ok := present[key]; !ok {
			return true
		}
	}
	return false
}

// retireMeasurementTable removes a published measurement table. Server helpers
// only add and update rows, so a cut that omits a row replaces the table.
func retireMeasurementTable(node *eebusfacade.OutputNode, address string, entity *publishedEntity) error {
	support := powerSupport()
	if node.HasUseCaseSupport(address, support.Actor, support.Name) {
		if err := node.WithdrawUseCase(address, support.Actor, support.Name); err != nil {
			return err
		}
	}
	entity.connection = false
	entity.rows = map[string]publishedIDs{}
	return nil
}

func noteDiagnosis(node *eebusfacade.OutputNode, address string) {
	publicationMu.Lock()
	defer publicationMu.Unlock()
	entityPublication(node, address).diagnosis = true
}

func diagnosisNoted(node *eebusfacade.OutputNode, address string) bool {
	publicationMu.Lock()
	defer publicationMu.Unlock()
	byAddress := publications[node]
	if byAddress == nil || byAddress[address] == nil {
		return false
	}
	return byAddress[address].diagnosis
}

func publishUnknownDiagnosis(node *eebusfacade.OutputNode, address string, result Result) error {
	if _, known := commissioningState(result); known || !diagnosisNoted(node, address) {
		return nil
	}
	return node.SetDeviceDiagnosisState(address, &spinemodel.DeviceDiagnosisStateDataType{})
}

func publishDiagnosis(node *eebusfacade.OutputNode, address string, result Result) error {
	if err := registerChargerUseCase(node, address, commissioningSupport()); err != nil {
		return err
	}
	state := &spinemodel.DeviceDiagnosisStateDataType{}
	if operating, ok := commissioningState(result); ok {
		state.OperatingState = &operating
	}
	return node.SetDeviceDiagnosisState(address, state)
}

func registerChargerUseCase(node *eebusfacade.OutputNode, address string, support eebusfacade.OutputUseCase) error {
	if node.HasUseCaseSupport(address, support.Actor, support.Name) {
		return nil
	}
	_, err := node.RegisterUseCase(address, support)
	return err
}

func powerSupport() eebusfacade.OutputUseCase {
	return eebusfacade.OutputUseCase{
		Actor:       spinemodel.UseCaseActorTypeMonitoredUnit,
		Name:        spinemodel.UseCaseNameTypeMonitoringOfPowerConsumption,
		Version:     "1.0.0",
		SubRevision: spinemodel.UseCaseDocumentSubRevisionRelease,
		Available:   true,
		Scenarios:   []spinemodel.UseCaseScenarioSupportType{1, 2, 3, 4, 5},
		Features:    []string{featureMeasurement, featureElectrical},
	}
}

func commissioningSupport() eebusfacade.OutputUseCase {
	return eebusfacade.OutputUseCase{
		Actor:       spinemodel.UseCaseActorTypeEVSE,
		Name:        spinemodel.UseCaseNameTypeEVSECommissioningAndConfiguration,
		Version:     "1.0.1",
		SubRevision: spinemodel.UseCaseDocumentSubRevisionRelease,
		Available:   true,
		Scenarios:   []spinemodel.UseCaseScenarioSupportType{1, 2},
		Features:    []string{featureDiagnosis},
	}
}

func entityPublication(node *eebusfacade.OutputNode, address string) *publishedEntity {
	byAddress := publications[node]
	if byAddress == nil {
		byAddress = map[string]*publishedEntity{}
		publications[node] = byAddress
	}
	entity := byAddress[address]
	if entity == nil {
		entity = &publishedEntity{rows: map[string]publishedIDs{}}
		byAddress[address] = entity
	}
	return entity
}

func ensureConnection(node *eebusfacade.OutputNode, address string, entity *publishedEntity) error {
	if entity.connection {
		return nil
	}
	identifier := spinemodel.ElectricalConnectionIdType(electricalConnectionID)
	err := node.AddElectricalDescription(address, spinemodel.ElectricalConnectionDescriptionDataType{
		ElectricalConnectionId:  &identifier,
		PowerSupplyType:         spinePtr(spinemodel.ElectricalConnectionVoltageTypeTypeAc),
		PositiveEnergyDirection: spinePtr(spinemodel.EnergyDirectionTypeConsume),
	})
	if err != nil {
		return err
	}
	entity.connection = true
	return nil
}

func writeRow(node *eebusfacade.OutputNode, address string, entity *publishedEntity, row wireRow) error {
	ids, ok := entity.rows[row.key]
	if !ok {
		measurementID, err := node.AddMeasurementDescription(address, row.description())
		if err != nil {
			return err
		}
		parameterID, err := node.AddElectricalParameter(address, row.parameter(measurementID))
		if err != nil {
			return err
		}
		ids = publishedIDs{measurement: measurementID, parameter: parameterID}
		entity.rows[row.key] = ids
	}
	return node.UpdateMeasurementValue(address, ids.measurement, row.measurementValue(ids.measurement), row.clearSource())
}

func (row wireRow) description() spinemodel.MeasurementDescriptionDataType {
	return spinemodel.MeasurementDescriptionDataType{
		MeasurementType: spinePtr(row.measure),
		CommodityType:   spinePtr(spinemodel.CommodityTypeTypeElectricity),
		Unit:            spinePtr(row.unit),
		ScopeType:       spinePtr(row.scope),
	}
}

func (row wireRow) parameter(measurementID spinemodel.MeasurementIdType) spinemodel.ElectricalConnectionParameterDescriptionDataType {
	identifier := spinemodel.ElectricalConnectionIdType(electricalConnectionID)
	parameter := spinemodel.ElectricalConnectionParameterDescriptionDataType{
		ElectricalConnectionId: &identifier,
		MeasurementId:          &measurementID,
		VoltageType:            spinePtr(spinemodel.ElectricalConnectionVoltageTypeTypeAc),
	}
	if row.hasType {
		parameter.AcMeasurementType = spinePtr(row.acType)
	}
	if row.hasVariant {
		parameter.AcMeasurementVariant = spinePtr(row.variant)
	}
	if row.hasPhase {
		parameter.AcMeasuredPhases = spinePtr(row.phase)
	}
	if row.hasRef {
		parameter.AcMeasuredInReferenceTo = spinePtr(row.reference)
	}
	return parameter
}

func (row wireRow) measurementValue(identifier spinemodel.MeasurementIdType) spinemodel.MeasurementDataType {
	value := spinemodel.MeasurementDataType{
		MeasurementId: &identifier,
		ValueType:     spinePtr(spinemodel.MeasurementValueTypeTypeValue),
		Value:         spineScaled(row.number, row.scale),
	}
	if row.calculated {
		value.ValueSource = spinePtr(spinemodel.MeasurementValueSourceTypeCalculatedValue)
	}
	return value
}

func (row wireRow) clearSource() *spinemodel.MeasurementDataElementsType {
	if row.calculated {
		return nil
	}
	return &spinemodel.MeasurementDataElementsType{ValueSource: &spinemodel.ElementTagType{}}
}

func chargerRows(result Result) []wireRow {
	var rows []wireRow
	if row, ok := totalReading(result); ok {
		rows = append(rows, row)
	}
	seen := map[string]struct{}{}
	for _, row := range rows {
		seen[row.key] = struct{}{}
	}
	for _, fact := range result.Facts {
		row, ok := measurementRow(fact, result.MissingElements)
		if !ok {
			continue
		}
		if _, dup := seen[row.key]; dup {
			continue
		}
		seen[row.key] = struct{}{}
		rows = append(rows, row)
	}
	return rows
}

func totalReading(result Result) (wireRow, bool) {
	if row, ok := reportedTotal(result); ok {
		return row, true
	}
	return derivedTotal(result)
}

func reportedTotal(result Result) (wireRow, bool) {
	for _, fact := range result.Facts {
		if fact.Element != elementReportedTotal || listed(result.MissingElements, fact.Element) {
			continue
		}
		number, scale, ok := quantityScaled(fact, unitWatt)
		if !ok {
			continue
		}
		return powerTotal(number, scale, false), true
	}
	return wireRow{}, false
}

func derivedTotal(result Result) (wireRow, bool) {
	sum := result.MemberSum
	if sum == nil || sum.Withheld || sum.Unit != unitWatt {
		return wireRow{}, false
	}
	number, scale, ok := copyScaled(sum.Coefficient, sum.Exponent)
	if !ok {
		return wireRow{}, false
	}
	return powerTotal(number, scale, true), true
}

func powerTotal(number int64, scale int8, calculated bool) wireRow {
	row := wireRow{
		key:        string(spinemodel.ScopeTypeTypeACPowerTotal),
		scope:      spinemodel.ScopeTypeTypeACPowerTotal,
		measure:    spinemodel.MeasurementTypeTypePower,
		unit:       spinemodel.UnitOfMeasurementTypeW,
		acType:     spinemodel.ElectricalConnectionAcMeasurementTypeTypeReal,
		hasType:    true,
		number:     number,
		scale:      scale,
		calculated: calculated,
	}
	return row
}

func measurementRow(fact quantityFact, missing []string) (wireRow, bool) {
	if listed(missing, fact.Element) || fact.Element == elementReportedTotal {
		return wireRow{}, false
	}
	switch fact.Element {
	case elementPhasePower:
		return phasePower(fact)
	case elementCurrent:
		return phaseCurrent(fact)
	case elementVoltage:
		return phaseVoltage(fact)
	case elementFrequency:
		return frequencyRow(fact)
	case elementLifetime:
		return energyRow(fact, spinemodel.ScopeTypeTypeACEnergyConsumed, false)
	case elementExported:
		return energyRow(fact, spinemodel.ScopeTypeTypeACEnergyProduced, true)
	default:
		return wireRow{}, false
	}
}

func phasePower(fact quantityFact) (wireRow, bool) {
	phase, ok := phaseName(fact.Phase)
	if !ok {
		return wireRow{}, false
	}
	number, scale, ok := quantityScaled(fact, unitWatt)
	if !ok {
		return wireRow{}, false
	}
	return phased(spinemodel.ScopeTypeTypeACPower, spinemodel.MeasurementTypeTypePower, spinemodel.UnitOfMeasurementTypeW, phase, true, spinemodel.ElectricalConnectionAcMeasurementTypeTypeReal, true, number, scale), true
}

func phaseCurrent(fact quantityFact) (wireRow, bool) {
	phase, ok := phaseName(fact.Phase)
	if !ok {
		return wireRow{}, false
	}
	number, scale, ok := quantityScaled(fact, unitAmpere)
	if !ok {
		return wireRow{}, false
	}
	return phased(spinemodel.ScopeTypeTypeACCurrent, spinemodel.MeasurementTypeTypeCurrent, spinemodel.UnitOfMeasurementTypeA, phase, false, spinemodel.ElectricalConnectionAcMeasurementTypeTypeReal, true, number, scale), true
}

func phaseVoltage(fact quantityFact) (wireRow, bool) {
	phase, ok := phaseName(fact.Phase)
	if !ok {
		return wireRow{}, false
	}
	number, scale, ok := quantityScaled(fact, unitVolt)
	if !ok {
		return wireRow{}, false
	}
	return phased(spinemodel.ScopeTypeTypeACVoltage, spinemodel.MeasurementTypeTypeVoltage, spinemodel.UnitOfMeasurementTypeV, phase, true, spinemodel.ElectricalConnectionAcMeasurementTypeTypeApparent, true, number, scale), true
}

func frequencyRow(fact quantityFact) (wireRow, bool) {
	number, scale, ok := quantityScaled(fact, unitHertz)
	if !ok {
		return wireRow{}, false
	}
	return wireRow{
		key:     string(spinemodel.ScopeTypeTypeACFrequency),
		scope:   spinemodel.ScopeTypeTypeACFrequency,
		measure: spinemodel.MeasurementTypeTypeFrequency,
		unit:    spinemodel.UnitOfMeasurementTypeHz,
		number:  number,
		scale:   scale,
	}, true
}

func energyRow(fact quantityFact, scope spinemodel.ScopeTypeType, produced bool) (wireRow, bool) {
	if fact.Kind != kindQuantity || fact.Unit != unitJoule {
		return wireRow{}, false
	}
	number, scale, ok := jouleToWattHour(fact.Coefficient, fact.Exponent, produced)
	if !ok {
		return wireRow{}, false
	}
	row := wireRow{
		key:     string(scope),
		scope:   scope,
		measure: spinemodel.MeasurementTypeTypeEnergy,
		unit:    spinemodel.UnitOfMeasurementTypeWh,
		acType:  spinemodel.ElectricalConnectionAcMeasurementTypeTypeReal,
		hasType: true,
		number:  number,
		scale:   scale,
	}
	return row, true
}

func phased(scope spinemodel.ScopeTypeType, measure spinemodel.MeasurementTypeType, unit spinemodel.UnitOfMeasurementType, phase spinemodel.ElectricalConnectionPhaseNameType, neutral bool, acType spinemodel.ElectricalConnectionAcMeasurementTypeType, rms bool, number int64, scale int8) wireRow {
	row := wireRow{
		key:        string(scope) + "/" + string(phase),
		scope:      scope,
		measure:    measure,
		unit:       unit,
		phase:      phase,
		hasPhase:   true,
		acType:     acType,
		hasType:    true,
		hasVariant: rms,
		number:     number,
		scale:      scale,
	}
	if rms {
		row.variant = spinemodel.ElectricalConnectionMeasurandVariantTypeRms
	}
	if neutral {
		row.reference = spinemodel.ElectricalConnectionPhaseNameTypeNeutral
		row.hasRef = true
		row.key += "/neutral"
	}
	return row
}

type quantityFact = struct {
	Element     string
	Phase       string
	Connector   string
	Kind        string
	Coefficient string
	Exponent    int32
	Boolean     bool
	Text        string
	Symbol      string
	Unit        string
}

func quantityScaled(fact quantityFact, unit string) (int64, int8, bool) {
	if fact.Kind != kindQuantity || fact.Unit != unit {
		return 0, 0, false
	}
	return copyScaled(fact.Coefficient, fact.Exponent)
}

func phaseName(phase string) (spinemodel.ElectricalConnectionPhaseNameType, bool) {
	switch phase {
	case "a":
		return spinemodel.ElectricalConnectionPhaseNameTypeA, true
	case "b":
		return spinemodel.ElectricalConnectionPhaseNameTypeB, true
	case "c":
		return spinemodel.ElectricalConnectionPhaseNameTypeC, true
	default:
		return "", false
	}
}

func fillsOperatingState(result Result) bool {
	if listed(admittedElements(result), elementState) {
		return true
	}
	_, ok := faultSymbol(result)
	return ok
}

func fillsTotal(result Result) bool {
	if _, reported := reportedTotal(result); reported {
		return true
	}
	_, derived := derivedTotal(result)
	return derived
}

func energyLosses(result Result) []string {
	var loss []string
	for _, fact := range result.Facts {
		name, ok := inexactEnergy(fact, result.MissingElements)
		if !ok || listed(loss, name) {
			continue
		}
		loss = append(loss, name)
	}
	return loss
}

func inexactEnergy(fact quantityFact, missing []string) (string, bool) {
	produced := false
	switch fact.Element {
	case elementLifetime:
	case elementExported:
		produced = true
	default:
		return "", false
	}
	if listed(missing, fact.Element) || fact.Kind != kindQuantity || fact.Unit != unitJoule {
		return "", false
	}
	rat, ok := decimalRat(fact.Coefficient, fact.Exponent)
	if !ok || (produced && rat.Sign() < 0) {
		return "", false
	}
	rat.Quo(rat, big.NewRat(3600, 1))
	if produced {
		rat.Neg(rat)
	}
	if _, _, exact := ratToScaled(rat); exact {
		return "", false
	}
	return fact.Element, true
}

func hasMeasurement(result Result) bool {
	if _, ok := reportedTotal(result); ok {
		return true
	}
	for _, fact := range result.Facts {
		if _, ok := measurementRow(fact, result.MissingElements); ok {
			return true
		}
	}
	return false
}

func totalLoss(result Result) []string {
	loss := []string{elementTotalPower}
	sum := result.MemberSum
	if sum == nil || !sum.Withheld {
		return loss
	}
	if sum.Reason != "" {
		loss = append(loss, sum.Reason)
	}
	loss = append(loss, "not included "+strconv.Itoa(sum.NotIncludedCount))
	for _, gap := range sum.Gaps {
		loss = append(loss, gap.Asset+" "+gap.Reason)
	}
	return loss
}

func faultSymbol(result Result) (string, bool) {
	for _, fact := range result.Facts {
		if fact.Element != elementFault || listed(result.MissingElements, fact.Element) {
			continue
		}
		if fact.Kind == kindSymbol && fact.Symbol != "" {
			return fact.Symbol, true
		}
	}
	return "", false
}

func commissioningState(result Result) (spinemodel.DeviceDiagnosisOperatingStateType, bool) {
	symbol, ok := faultSymbol(result)
	if !ok {
		return "", false
	}
	if symbol == symbolPresent || symbol == elementFault+"."+symbolPresent {
		return spinemodel.DeviceDiagnosisOperatingStateTypeFailure, true
	}
	return spinemodel.DeviceDiagnosisOperatingStateTypeNormalOperation, true
}

func genericOperatingState(fault, readiness, charging string) (spinemodel.DeviceDiagnosisOperatingStateType, bool) {
	switch {
	case fault == symbolPresent:
		return spinemodel.DeviceDiagnosisOperatingStateTypeFailure, true
	case readiness == symbolNotReady:
		return spinemodel.DeviceDiagnosisOperatingStateTypeTemporarilyNotReady, true
	case charging == symbolCharging:
		return spinemodel.DeviceDiagnosisOperatingStateTypeNormalOperation, true
	case charging == symbolIdle:
		return spinemodel.DeviceDiagnosisOperatingStateTypeStandby, true
	default:
		return "", false
	}
}

func copyScaled(coefficient string, exponent int32) (int64, int8, bool) {
	if exponent < math.MinInt8 || exponent > math.MaxInt8 {
		return 0, 0, false
	}
	number, err := strconv.ParseInt(coefficient, 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return number, int8(exponent), true
}

func jouleToWattHour(coefficient string, exponent int32, produced bool) (int64, int8, bool) {
	rat, ok := decimalRat(coefficient, exponent)
	if !ok {
		return 0, 0, false
	}
	if produced && rat.Sign() < 0 {
		return 0, 0, false
	}
	rat.Quo(rat, big.NewRat(3600, 1))
	if produced {
		rat.Neg(rat)
	}
	return ratToScaled(rat)
}

func decimalRat(coefficient string, exponent int32) (*big.Rat, bool) {
	integer := new(big.Int)
	if _, ok := integer.SetString(coefficient, 10); !ok {
		return nil, false
	}
	rat := new(big.Rat).SetInt(integer)
	if exponent == 0 {
		return rat, true
	}
	abs := exponent
	if abs < 0 {
		abs = -abs
	}
	pow := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(abs)), nil)
	scale := new(big.Rat).SetInt(pow)
	if exponent > 0 {
		rat.Mul(rat, scale)
	} else {
		rat.Quo(rat, scale)
	}
	return rat, true
}

func ratToScaled(rat *big.Rat) (int64, int8, bool) {
	if rat == nil {
		return 0, 0, false
	}
	negative := rat.Sign() < 0
	num := new(big.Int).Abs(rat.Num())
	den := new(big.Int).Set(rat.Denom())
	twos := 0
	for den.Bit(0) == 0 && den.Cmp(big.NewInt(0)) > 0 {
		den.Rsh(den, 1)
		twos++
	}
	fives := 0
	five := big.NewInt(5)
	mod := new(big.Int)
	for den.Cmp(big.NewInt(1)) > 0 {
		mod.Mod(den, five)
		if mod.Sign() != 0 {
			break
		}
		den.Quo(den, five)
		fives++
	}
	if den.Cmp(big.NewInt(1)) != 0 {
		return 0, 0, false
	}
	scale := twos
	if fives > scale {
		scale = fives
	}
	if twos < scale {
		num.Mul(num, new(big.Int).Exp(big.NewInt(2), big.NewInt(int64(scale-twos)), nil))
	}
	if fives < scale {
		num.Mul(num, new(big.Int).Exp(five, big.NewInt(int64(scale-fives)), nil))
	}
	ten := big.NewInt(10)
	for scale > 0 {
		mod.Mod(num, ten)
		if mod.Sign() != 0 {
			break
		}
		num.Quo(num, ten)
		scale--
	}
	if scale > math.MaxInt8 || !num.IsInt64() {
		return 0, 0, false
	}
	number := num.Int64()
	if negative {
		number = -number
	}
	return number, int8(-scale), true
}

func spinePtr[T any](value T) *T { return &value }

func spineScaled(number int64, scale int8) *spinemodel.ScaledNumberType {
	value := spinemodel.NumberType(number)
	step := spinemodel.ScaleType(scale)
	return &spinemodel.ScaledNumberType{Number: &value, Scale: &step}
}
