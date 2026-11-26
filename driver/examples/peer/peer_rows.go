package peer

import (
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"sync"

	"github.com/Project-Helianthus/helianthus-eebus-go/session"
	"github.com/Project-Helianthus/helianthus-spine-go/model"
	"github.com/Project-Helianthus/helianthus-spine-go/spine"

	semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"
	"github.com/Project-Helianthus/helianthus-semreg/semreg/v1/roweval"
)

const (
	nativeRowID       = "peer.measurement.dhw_temperature"
	nativeUnit        = "native.celsius"
	subjectRole       = "dhw"
	kelvinUnit        = semreg.DefinitionID("unit.kelvin")
	outputMeasurement = 1
	scaleShift        = 56
	numberMask        = 1<<scaleShift - 1
	payloadBytes      = 8
)

var (
	errClosed   = errors.New("peer: instance is closed")
	errConfig   = errors.New("peer: configuration is incomplete")
	factRef     = semreg.DefinitionRef{Pack: semreg.PackRef{ID: "helianthus.pack.thermal", Version: "1.0.0"}, ID: "thermal.measurement.dhw_temperature", Version: "1.0.0"}
	serviceRef  = semreg.DefinitionRef{Pack: semreg.PackRef{ID: "helianthus.pack.thermal", Version: "1.0.0"}, ID: "thermal.service.dhw", Version: "1.0.0"}
	operation   = semreg.DefinitionRef{Pack: semreg.PackRef{ID: "helianthus.pack.thermal", Version: "1.0.0"}, ID: "thermal.operation.publish_dhw_temperature", Version: "1.0.0"}
	dimensionID = semreg.DefinitionID("thermal.dimension.dhw")
)

type PeerSelector struct {
	Instance    string
	Device      model.AddressDeviceType
	Entity      []model.AddressEntityType
	FeatureType model.FeatureTypeType
	Function    model.FunctionType
	ScopeType   model.ScopeTypeType
	Unit        model.UnitOfMeasurementType
	ValueType   model.MeasurementValueTypeType
}

type Outcome string

const (
	OutcomeFact      Outcome = "fact"
	OutcomeNoMatch   Outcome = "no_match"
	OutcomeUnknown   Outcome = "unknown"
	OutcomeFailed    Outcome = "failed_determination"
	OutcomeAmbiguous Outcome = "ambiguous"
	OutcomeWithheld  Outcome = "withheld"
)

type Reading struct {
	Outcome   Outcome
	Reason    roweval.Reason
	Detail    string
	Candidate *semreg.FactCandidate
}

type OutputState string

const (
	OutputValue  OutputState = "value"
	OutputAbsent OutputState = "absent"
	OutputFailed OutputState = "failed_determination"
)

type OutputInput struct {
	State OutputState
	Value semreg.Value
}

type OutputResult struct {
	Reason      roweval.Reason
	Detail      string
	Description model.MeasurementDescriptionDataType
	Entry       *model.MeasurementDataType
}

type Config struct {
	Key           string
	DeviceAddress string
	StateRoot     string
	Catalog       []semreg.DefinitionIndex
	Subject       semreg.ServiceInstanceID
	Template      semreg.FactCandidate
}

type Instance struct {
	engine engine
}

type engine interface {
	close() error
	read(string, model.FeatureAddressType) (Reading, error)
	output(OutputInput) (OutputResult, error)
}

type core struct {
	key      string
	session  *session.Instance
	feature  *spine.FeatureLocal
	catalog  []semreg.DefinitionIndex
	subject  semreg.ServiceInstanceID
	template semreg.FactCandidate
	mu       sync.Mutex
	closed   bool
}

func NewInstance(cfg Config) (*Instance, error) {
	return newInstance(cfg, nil)
}

func newInstance(cfg Config, transport session.Transport) (*Instance, error) {
	if cfg.Key == "" || cfg.DeviceAddress == "" || cfg.StateRoot == "" {
		return nil, errConfig
	}
	device := spine.NewDeviceLocal("", "", "", "", cfg.DeviceAddress, model.DeviceTypeTypeEnergyManagementSystem, "")
	attached, err := session.Attach(session.AttachConfig{Device: device, StateRoot: cfg.StateRoot, Transport: transport})
	if err != nil {
		return nil, err
	}
	entity := spine.NewEntityLocal(device, model.EntityTypeTypeCEM, []model.AddressEntityType{1}, 0)
	feature := spine.NewFeatureLocal(1, entity, model.FeatureTypeTypeMeasurement, model.RoleTypeServer)
	feature.AddFunctionType(model.FunctionTypeMeasurementDescriptionListData, true, false)
	feature.AddFunctionType(model.FunctionTypeMeasurementListData, true, false)
	entity.AddFeature(feature)
	device.AddEntity(entity)
	return &Instance{engine: &core{
		key:      cfg.Key,
		session:  attached,
		feature:  feature,
		catalog:  cfg.Catalog,
		subject:  cfg.Subject,
		template: cfg.Template,
	}}, nil
}

func (i *Instance) Close() error { return i.engine.close() }

func (i *Instance) Read(ski string, server model.FeatureAddressType) (Reading, error) {
	return i.engine.read(ski, server)
}

func (i *Instance) Output(in OutputInput) (OutputResult, error) { return i.engine.output(in) }

func (i *core) close() error {
	i.mu.Lock()
	if i.closed {
		i.mu.Unlock()
		return errClosed
	}
	i.closed = true
	i.mu.Unlock()
	return i.session.Close()
}

func (i *core) open() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return errClosed
	}
	return nil
}

func (i *core) selector(device model.AddressDeviceType, entity []model.AddressEntityType, scope model.ScopeTypeType, unit model.UnitOfMeasurementType) PeerSelector {
	return PeerSelector{
		Instance:    i.key,
		Device:      device,
		Entity:      append([]model.AddressEntityType(nil), entity...),
		FeatureType: model.FeatureTypeTypeMeasurement,
		Function:    model.FunctionTypeMeasurementListData,
		ScopeType:   scope,
		Unit:        unit,
		ValueType:   model.MeasurementValueTypeTypeValue,
	}
}

func (i *core) table(at PeerSelector) roweval.Table[PeerSelector] {
	return roweval.Table[PeerSelector]{
		Native: []roweval.NativeRow[PeerSelector]{{
			ID:      nativeRowID,
			Unit:    nativeUnit,
			SpecRef: "SPINE 1.3.0 3.10.1.8, 4.3.16 Table 93, A.2.15",
			At:      at,
			Wire:    roweval.Layout{Width: 64, Codec: "scaled", ByteOrder: "le", WordOrder: "none", BitOrder: "lsb"},
		}},
		Bindings: []roweval.BindingRow{{
			NativeID:    nativeRowID,
			SubjectRole: subjectRole,
			Service:     serviceRef,
			Read: &roweval.ReadRow{
				Fact:       factRef,
				Dimensions: map[semreg.DefinitionID]string{dimensionID: subjectRole},
				Unit:       kelvinUnit,
				Policy:     i.template.FreshnessPolicy.PolicyID,
				ToRegistry: roweval.Convert{Numeric: &roweval.Affine{Num: 1, Den: 1, Add: semreg.Decimal{Coefficient: "27315", Exponent10: -2}}},
			},
			Write: &roweval.WriteRow{
				Operation:   operation,
				Argument:    factRef.ID,
				TargetRow:   nativeRowID,
				ReadbackRow: nativeRowID,
				Inverse:     roweval.Convert{Numeric: &roweval.Affine{Num: 1, Den: 1, Add: semreg.Decimal{Coefficient: "-27315", Exponent10: -2}}},
			},
		}},
	}
}

func descriptors() roweval.Descriptors[PeerSelector] {
	identity := roweval.Order{
		Decode: func(raw uint64, _ uint16) (uint64, error) { return raw, nil },
		Encode: func(raw uint64, _ uint16) (uint64, error) { return raw, nil },
	}
	return roweval.Descriptors[PeerSelector]{
		Codecs:     map[string]roweval.Codec{"scaled": {Decode: decodeScaled, Encode: encodeScaled}},
		ByteOrders: map[string]roweval.Order{"le": identity},
		WordOrders: map[string]roweval.Order{"none": identity},
		BitOrders:  map[string]roweval.Order{"lsb": identity},
		Extent:     func(PeerSelector) (int, bool) { return payloadBytes, true },
	}
}

func decodeScaled(raw uint64, width uint16) (semreg.Decimal, error) {
	if width != 64 {
		return semreg.Decimal{}, fmt.Errorf("width %d is not a scaled number", width)
	}
	number := int64(raw&numberMask) << (64 - scaleShift) >> (64 - scaleShift)
	return decimalOf(number, int(int8(raw>>scaleShift)))
}

func encodeScaled(number semreg.Decimal, width uint16) (uint64, error) {
	if width != 64 {
		return 0, fmt.Errorf("width %d is not a scaled number", width)
	}
	coefficient, ok := new(big.Int).SetString(number.Coefficient, 10)
	if !ok || !coefficient.IsInt64() {
		return 0, fmt.Errorf("coefficient %q is not a number", number.Coefficient)
	}
	n := coefficient.Int64()
	if n < -(1<<(scaleShift-1)) || n >= 1<<(scaleShift-1) {
		return 0, fmt.Errorf("number %d does not fit the payload", n)
	}
	if number.Exponent10 < -128 || number.Exponent10 > 127 {
		return 0, fmt.Errorf("scale %d does not fit the payload", number.Exponent10)
	}
	return uint64(uint8(int8(number.Exponent10)))<<scaleShift | uint64(n)&numberMask, nil
}

func decimalOf(number int64, scale int) (semreg.Decimal, error) {
	if number == 0 {
		return semreg.Decimal{Coefficient: "0"}, nil
	}
	for number%10 == 0 && scale < 18 {
		number /= 10
		scale++
	}
	d := semreg.Decimal{Coefficient: strconv.FormatInt(number, 10), Exponent10: int32(scale)}
	if err := d.Validate(); err != nil {
		return semreg.Decimal{}, err
	}
	return d, nil
}

func scaledPayload(number *model.ScaledNumberType) []byte {
	var n int64
	var scale int8
	if number.Number != nil {
		n = int64(*number.Number)
	}
	if number.Scale != nil {
		scale = int8(*number.Scale)
	}
	raw := uint64(uint8(scale))<<scaleShift | uint64(n)&numberMask
	payload := make([]byte, payloadBytes)
	for b := range payload {
		payload[b] = byte(raw >> (8 * b))
	}
	return payload
}

func notified[T any](s *session.Instance, ski string, pair session.Pair, function model.FunctionType) (T, bool) {
	var zero T
	data, _, ok := s.LastNotified(ski, pair, function)
	if !ok {
		return zero, false
	}
	switch typed := data.(type) {
	case T:
		return typed, true
	case *T:
		if typed != nil {
			return *typed, true
		}
	}
	return zero, false
}

func (i *core) read(ski string, server model.FeatureAddressType) (Reading, error) {
	if err := i.open(); err != nil {
		return Reading{}, err
	}
	pair, found := i.pairOf(ski, server)
	if !found {
		return Reading{Outcome: OutcomeNoMatch, Reason: roweval.ReasonNoMatch, Detail: "no established subscription to the remote feature"}, nil
	}
	descriptions, ok := notified[model.MeasurementDescriptionListDataType](i.session, ski, pair, model.FunctionTypeMeasurementDescriptionListData)
	if !ok {
		return Reading{Outcome: OutcomeNoMatch, Reason: roweval.ReasonNoMatch, Detail: "no measurement description notified"}, nil
	}
	list, _ := notified[model.MeasurementListDataType](i.session, ski, pair, model.FunctionTypeMeasurementListData)
	device := model.AddressDeviceType("")
	if pair.Server.Device != nil {
		device = *pair.Server.Device
	}
	var matched []Reading
	for _, description := range descriptions.MeasurementDescriptionData {
		if description.MeasurementId == nil {
			continue
		}
		var scope model.ScopeTypeType
		if description.ScopeType != nil {
			scope = *description.ScopeType
		}
		var unit model.UnitOfMeasurementType
		if description.Unit != nil {
			unit = *description.Unit
		}
		reading := i.readEntry(device, pair.Server.Entity, scope, unit, *description.MeasurementId, list)
		if reading.Outcome != OutcomeNoMatch {
			matched = append(matched, reading)
		}
	}
	switch len(matched) {
	case 0:
		return Reading{Outcome: OutcomeNoMatch, Reason: roweval.ReasonNoMatch, Detail: "no description matches the row"}, nil
	case 1:
		return matched[0], nil
	}
	return Reading{Outcome: OutcomeAmbiguous, Reason: roweval.ReasonAmbiguous, Detail: fmt.Sprintf("%d descriptions match the row", len(matched))}, nil
}

func (i *core) pairOf(ski string, server model.FeatureAddressType) (session.Pair, bool) {
	for _, record := range i.session.Established() {
		if record.Role == session.RoleLocalClient && strings.EqualFold(record.Peer, ski) && record.Pair.Server.String() == server.String() {
			return record.Pair, true
		}
	}
	return session.Pair{}, false
}

func (i *core) readEntry(device model.AddressDeviceType, entity []model.AddressEntityType, scope model.ScopeTypeType, unit model.UnitOfMeasurementType, id model.MeasurementIdType, list model.MeasurementListDataType) Reading {
	row := i.selector(device, entity, model.ScopeTypeTypeDhwTemperature, model.UnitOfMeasurementTypedegC)
	acquired := roweval.Acquired[PeerSelector]{
		Selector: i.selector(device, entity, scope, unit),
		Receipt:  roweval.Receipt{ReceivedAt: i.template.Times.ReceivedAt, Monotonic: i.template.Times.ReceiptMonotonic},
		Template: i.template,
		Roles: roweval.Roles{
			Subjects: map[string]roweval.RoleBinding{subjectRole: {ServiceInstance: i.subject}},
			Catalog:  i.catalog,
		},
		Descriptors: descriptors(),
	}
	var current []model.MeasurementDataType
	for _, entry := range list.MeasurementData {
		if entry.MeasurementId != nil && *entry.MeasurementId == id && entry.ValueType != nil && *entry.ValueType == model.MeasurementValueTypeTypeValue {
			current = append(current, entry)
		}
	}
	state := OutcomeUnknown
	switch {
	case len(current) > 1:
		state = OutcomeAmbiguous
	case len(current) == 1 && current[0].ValueState != nil && *current[0].ValueState == model.MeasurementValueStateTypeError:
		state = OutcomeFailed
	case len(current) == 1 && current[0].Value != nil && current[0].Value.Number != nil:
		state = OutcomeFact
		acquired.Payload = scaledPayload(current[0].Value)
	}
	evaluation, err := roweval.Evaluate(i.table(row), acquired)
	if err != nil {
		return Reading{Outcome: OutcomeWithheld, Detail: err.Error()}
	}
	if evaluation.Reason == roweval.ReasonNoMatch {
		return Reading{Outcome: OutcomeNoMatch, Reason: evaluation.Reason, Detail: evaluation.Detail}
	}
	switch state {
	case OutcomeAmbiguous:
		return Reading{Outcome: OutcomeAmbiguous, Reason: roweval.ReasonAmbiguous, Detail: fmt.Sprintf("%d current-reading entries share measurementId %d", len(current), id)}
	case OutcomeFailed:
		return Reading{Outcome: OutcomeFailed, Detail: "valueState error"}
	case OutcomeUnknown:
		return Reading{Outcome: OutcomeUnknown, Detail: "no current-reading value"}
	}
	if evaluation.Candidate == nil {
		return Reading{Outcome: OutcomeWithheld, Reason: evaluation.Reason, Detail: evaluation.Detail}
	}
	return Reading{Outcome: OutcomeFact, Candidate: evaluation.Candidate}
}

func (i *core) output(in OutputInput) (OutputResult, error) {
	if err := i.open(); err != nil {
		return OutputResult{}, err
	}
	id := model.MeasurementIdType(outputMeasurement)
	measurementType := model.MeasurementTypeTypeTemperature
	scope := model.ScopeTypeTypeDhwTemperature
	unit := model.UnitOfMeasurementTypedegC
	result := OutputResult{Description: model.MeasurementDescriptionDataType{
		MeasurementId:   &id,
		MeasurementType: &measurementType,
		Unit:            &unit,
		ScopeType:       &scope,
	}}
	valueType := model.MeasurementValueTypeTypeValue
	entries := []model.MeasurementDataType{}
	switch in.State {
	case OutputValue:
		if in.Value.Quantity == nil || in.Value.Quantity.Unit != kelvinUnit {
			result.Reason, result.Detail = roweval.ReasonUnit, "the fact is not in kelvin"
			return result, nil
		}
		at := PeerSelector{Instance: i.key}
		written, err := roweval.Invert(i.table(at), roweval.Writing[PeerSelector]{
			NativeID:    nativeRowID,
			SubjectRole: subjectRole,
			Operation:   operation,
			Argument:    semreg.TypedField{ID: factRef.ID, Value: in.Value},
			Descriptors: descriptors(),
		})
		if err != nil {
			return result, err
		}
		if written.Payload == nil {
			result.Reason, result.Detail = written.Reason, written.Detail
			return result, nil
		}
		var raw uint64
		for b, octet := range written.Payload {
			raw |= uint64(octet) << (8 * b)
		}
		number := model.NumberType(int64(raw&numberMask) << (64 - scaleShift) >> (64 - scaleShift))
		scale := model.ScaleType(int8(raw >> scaleShift))
		entry := model.MeasurementDataType{MeasurementId: &id, ValueType: &valueType, Value: &model.ScaledNumberType{Number: &number, Scale: &scale}}
		result.Entry = &entry
		entries = append(entries, entry)
	case OutputFailed:
		state := model.MeasurementValueStateTypeError
		entry := model.MeasurementDataType{MeasurementId: &id, ValueType: &valueType, ValueState: &state}
		result.Entry = &entry
		entries = append(entries, entry)
	case OutputAbsent:
	default:
		result.Reason, result.Detail = roweval.ReasonArgument, "unknown output state "+string(in.State)
		return result, nil
	}
	i.feature.SetData(model.FunctionTypeMeasurementDescriptionListData, &model.MeasurementDescriptionListDataType{MeasurementDescriptionData: []model.MeasurementDescriptionDataType{result.Description}})
	i.feature.SetData(model.FunctionTypeMeasurementListData, &model.MeasurementListDataType{MeasurementData: entries})
	return result, nil
}
