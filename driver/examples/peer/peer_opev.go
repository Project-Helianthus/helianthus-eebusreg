package peer

import (
	"errors"
	"reflect"
	"sync"

	"github.com/Project-Helianthus/helianthus-eebus-go/features/client"
	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	"github.com/Project-Helianthus/helianthus-spine-go/model"
	"github.com/Project-Helianthus/helianthus-spine-go/util"
)

var (
	errOPEVUnbound       = errors.New("peer: the client feature is not bound to the remote server feature")
	errOPEVNoMatch       = errors.New("peer: the selector does not name this instance, remote feature or function")
	errOPEVNotObligation = errors.New("peer: the selector is not the maxValueLimit obligation limit in A for overload protection")
	errOPEVAbsent        = errors.New("peer: no limit matches the selector")
	errOPEVAmbiguous     = errors.New("peer: more than one limit matches the selector")
	errOPEVNotChangeable = errors.New("peer: the selected limit declares isLimitChangeable false")
	errOPEVNoData        = errors.New("peer: no limit data is held for the selected limit")
)

// OPEVSelector names one remote LoadControl obligation limit of one driver instance.
//
//nolint:revive // the inventoried public name is fixed by the slice contract.
type OPEVSelector struct {
	Instance      string
	Device        model.AddressDeviceType
	Entity        []model.AddressEntityType
	FeatureType   model.FeatureTypeType
	Function      model.FunctionType
	LimitType     model.LoadControlLimitTypeType
	LimitCategory model.LoadControlCategoryType
	ScopeType     model.ScopeTypeType
	Unit          model.UnitOfMeasurementType
	Phase         model.ElectricalConnectionPhaseNameType
}

// OPEVSelection is the limit a selector resolved to on the remote peer.
type OPEVSelection struct {
	LimitID       model.LoadControlLimitIdType
	MeasurementID model.MeasurementIdType
	Changeable    bool
}

// OPEVReceipt names the write a set or inverse sent.
type OPEVReceipt struct {
	Counter model.MsgCounterType
	LimitID model.LoadControlLimitIdType
}

// OPEVReadback is the data the client holds for one limit identifier.
type OPEVReadback struct {
	LimitID model.LoadControlLimitIdType
	Active  *bool
	Value   *model.ScaledNumberType
}

// OPEV sends and reads back the remote LoadControl obligation limit of one driver instance.
type OPEV struct {
	engine opevEngine
}

type opevEngine interface {
	Bind() (model.MsgCounterType, error)
	Request() error
	Result(model.MsgCounterType) (model.ResultDataType, bool)
	Select(OPEVSelector) (OPEVSelection, error)
	Set(OPEVSelector, float64) (OPEVReceipt, error)
	Inverse(OPEVSelector) (OPEVReceipt, error)
	Readback(model.LoadControlLimitIdType) (OPEVReadback, error)
}

type opevCore struct {
	key     string
	remote  spineapi.EntityRemoteInterface
	loads   *client.LoadControl
	circuit *client.ElectricalConnection
	mu      sync.Mutex
	results map[model.MsgCounterType]model.ResultDataType
}

func newOPEV(key string, local spineapi.EntityLocalInterface, remote spineapi.EntityRemoteInterface) (*OPEV, error) {
	core, err := newOPEVCore(key, local, remote)
	if err != nil {
		return nil, err
	}
	return &OPEV{engine: core}, nil
}

func newOPEVCore(key string, local spineapi.EntityLocalInterface, remote spineapi.EntityRemoteInterface) (*opevCore, error) {
	if key == "" {
		return nil, errConfig
	}
	loads, err := client.NewLoadControl(local, remote)
	if err != nil {
		return nil, err
	}
	circuit, err := client.NewElectricalConnection(local, remote)
	if err != nil {
		return nil, err
	}
	o := &opevCore{key: key, remote: remote, loads: loads, circuit: circuit, results: map[model.MsgCounterType]model.ResultDataType{}}
	loads.AddResultCallback(o.noteResult)
	return o, nil
}

func (o *opevCore) noteResult(msg spineapi.ResponseMessage) {
	result, ok := msg.Data.(*model.ResultDataType)
	if !ok || result == nil {
		return
	}
	o.mu.Lock()
	o.results[msg.MsgCounterReference] = *result
	o.mu.Unlock()
}

func (o *opevCore) Bind() (model.MsgCounterType, error) {
	counter, err := o.loads.Bind()
	if err != nil {
		return 0, err
	}
	return *counter, nil
}

func (o *opevCore) Request() error {
	if _, err := o.loads.RequestLimitDescriptions(nil, nil); err != nil {
		return err
	}
	if _, err := o.loads.RequestLimitData(nil, nil); err != nil {
		return err
	}
	_, err := o.circuit.RequestParameterDescriptions(nil, nil)
	return err
}

func (o *opevCore) Result(counter model.MsgCounterType) (model.ResultDataType, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	result, ok := o.results[counter]
	return result, ok
}

func (o *opevCore) resolve(sel OPEVSelector) (OPEVSelection, error) {
	device := o.remote.Device().Address()
	if sel.Instance != o.key || device == nil || sel.Device != *device ||
		!reflect.DeepEqual(sel.Entity, o.remote.Address().Entity) ||
		sel.FeatureType != model.FeatureTypeTypeLoadControl || sel.Function != model.FunctionTypeLoadControlLimitListData {
		return OPEVSelection{}, errOPEVNoMatch
	}
	if sel.LimitType != model.LoadControlLimitTypeTypeMaxValueLimit || sel.LimitCategory != model.LoadControlCategoryTypeObligation ||
		sel.ScopeType != model.ScopeTypeTypeOverloadProtection || sel.Unit != model.UnitOfMeasurementTypeA {
		return OPEVSelection{}, errOPEVNotObligation
	}
	if !o.loads.HasBinding() {
		return OPEVSelection{}, errOPEVUnbound
	}
	phase := sel.Phase
	parameters, err := o.circuit.GetParameterDescriptionsForFilter(model.ElectricalConnectionParameterDescriptionDataType{AcMeasuredPhases: &phase})
	if err != nil || len(parameters) == 0 {
		return OPEVSelection{}, errOPEVAbsent
	}
	var measurement *model.MeasurementIdType
	for _, parameter := range parameters {
		if parameter.MeasurementId == nil {
			continue
		}
		if measurement != nil && *measurement != *parameter.MeasurementId {
			return OPEVSelection{}, errOPEVAmbiguous
		}
		measurement = parameter.MeasurementId
	}
	if measurement == nil {
		return OPEVSelection{}, errOPEVAbsent
	}
	limitType, category, scope, unit := sel.LimitType, sel.LimitCategory, sel.ScopeType, sel.Unit
	descriptions, err := o.loads.GetLimitDescriptionsForFilter(model.LoadControlLimitDescriptionDataType{
		LimitType: &limitType, LimitCategory: &category, ScopeType: &scope, Unit: &unit, MeasurementId: measurement,
	})
	if err != nil {
		return OPEVSelection{}, errOPEVAbsent
	}
	var limit *model.LoadControlLimitIdType
	for _, description := range descriptions {
		if description.LimitId == nil {
			continue
		}
		if limit != nil {
			return OPEVSelection{}, errOPEVAmbiguous
		}
		limit = description.LimitId
	}
	if limit == nil {
		return OPEVSelection{}, errOPEVAbsent
	}
	data, err := o.loads.GetLimitDataForId(*limit)
	if err != nil {
		return OPEVSelection{}, errOPEVNoData
	}
	changeable := data.IsLimitChangeable == nil || *data.IsLimitChangeable
	return OPEVSelection{LimitID: *limit, MeasurementID: *measurement, Changeable: changeable}, nil
}

func (o *opevCore) Select(sel OPEVSelector) (OPEVSelection, error) {
	selection, err := o.resolve(sel)
	if err != nil {
		return OPEVSelection{}, err
	}
	if !selection.Changeable {
		return selection, errOPEVNotChangeable
	}
	return selection, nil
}

func (o *opevCore) Set(sel OPEVSelector, ampere float64) (OPEVReceipt, error) {
	selection, err := o.Select(sel)
	if err != nil {
		return OPEVReceipt{}, err
	}
	return o.write(selection, model.LoadControlLimitDataType{
		LimitId:       &selection.LimitID,
		IsLimitActive: util.Ptr(true),
		Value:         model.NewScaledNumberType(ampere),
	})
}

func (o *opevCore) Inverse(sel OPEVSelector) (OPEVReceipt, error) {
	selection, err := o.Select(sel)
	if err != nil {
		return OPEVReceipt{}, err
	}
	return o.write(selection, model.LoadControlLimitDataType{LimitId: &selection.LimitID, IsLimitActive: util.Ptr(false)})
}

func (o *opevCore) write(selection OPEVSelection, data model.LoadControlLimitDataType) (OPEVReceipt, error) {
	counter, err := o.loads.WriteLimitData([]model.LoadControlLimitDataType{data}, nil, nil)
	if err != nil {
		return OPEVReceipt{}, err
	}
	return OPEVReceipt{Counter: *counter, LimitID: selection.LimitID}, nil
}

func (o *opevCore) Readback(limit model.LoadControlLimitIdType) (OPEVReadback, error) {
	data, err := o.loads.GetLimitDataForId(limit)
	if err != nil {
		return OPEVReadback{}, errOPEVNoData
	}
	return OPEVReadback{LimitID: limit, Active: data.IsLimitActive, Value: data.Value}, nil
}

// Bind binds the client feature to the remote server feature.
func (o *OPEV) Bind() (model.MsgCounterType, error) { return o.engine.Bind() }

// Request asks the remote for the descriptions, limit data and phase parameters.
func (o *OPEV) Request() error { return o.engine.Request() }

// Result returns the result the remote returned for one write counter.
func (o *OPEV) Result(counter model.MsgCounterType) (model.ResultDataType, bool) {
	return o.engine.Result(counter)
}

// Select resolves the selector to one writable limit.
func (o *OPEV) Select(sel OPEVSelector) (OPEVSelection, error) { return o.engine.Select(sel) }

// Set writes the selected limit active with the value in ampere.
func (o *OPEV) Set(sel OPEVSelector, ampere float64) (OPEVReceipt, error) {
	return o.engine.Set(sel, ampere)
}

// Inverse deactivates the same limit instance a set activated.
func (o *OPEV) Inverse(sel OPEVSelector) (OPEVReceipt, error) { return o.engine.Inverse(sel) }

// Readback returns the data the client holds for one limit identifier.
func (o *OPEV) Readback(limit model.LoadControlLimitIdType) (OPEVReadback, error) {
	return o.engine.Readback(limit)
}
