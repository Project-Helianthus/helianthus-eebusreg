package peer

import (
	"github.com/Project-Helianthus/helianthus-eebus-go/features/client"
	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	"github.com/Project-Helianthus/helianthus-spine-go/model"
	"github.com/Project-Helianthus/helianthus-spine-go/util"
)

// CEVCTimeSeriesSelector names one remote TimeSeries description of one driver instance.
type CEVCTimeSeriesSelector struct {
	Instance       string
	Device         model.AddressDeviceType
	Entity         []model.AddressEntityType
	FeatureType    model.FeatureTypeType
	Function       model.FunctionType
	TimeSeriesID   model.TimeSeriesIdType
	TimeSeriesType model.TimeSeriesTypeType
	Writeable      bool
	Unit           model.UnitOfMeasurementType
}

// CEVCTimeSeriesSelection is the series a selector resolved to.
type CEVCTimeSeriesSelection struct {
	TimeSeriesID model.TimeSeriesIdType
}

// CEVCTimeSeriesReceipt names the write a set or inverse sent.
type CEVCTimeSeriesReceipt struct {
	Counter      model.MsgCounterType
	TimeSeriesID model.TimeSeriesIdType
}

// CEVCTimeSeriesReadback is the series the client holds for one series identifier.
type CEVCTimeSeriesReadback struct {
	TimeSeriesID model.TimeSeriesIdType
	TimePeriod   *model.TimePeriodType
	Slots        []model.TimeSeriesSlotType
}

// CEVCTimeSeries sends and reads back one remote constraint series of one driver instance.
type CEVCTimeSeries struct {
	engine cevcSeriesEngine
}

type cevcSeriesEngine interface {
	Bind() (model.MsgCounterType, error)
	Request() error
	Result(model.MsgCounterType) (model.ResultDataType, bool)
	Select(CEVCTimeSeriesSelector) (CEVCTimeSeriesSelection, error)
	Write(CEVCTimeSeriesSelector, model.TimePeriodType, model.TimeSeriesSlotIdType, float64, model.DurationType) (CEVCTimeSeriesReceipt, error)
	Inverse(CEVCTimeSeriesSelector) (CEVCTimeSeriesReceipt, error)
	Readback(model.TimeSeriesIdType) (CEVCTimeSeriesReadback, error)
}

type cevcSeriesCore struct {
	key     string
	local   spineapi.EntityLocalInterface
	remote  spineapi.EntityRemoteInterface
	series  *client.TimeSeries
	results *cevcResults
}

func newCEVCTimeSeries(key string, local spineapi.EntityLocalInterface, remote spineapi.EntityRemoteInterface) (*CEVCTimeSeries, error) {
	core, err := newCEVCSeriesCore(key, local, remote)
	if err != nil {
		return nil, err
	}
	return &CEVCTimeSeries{engine: core}, nil
}

func newCEVCSeriesCore(key string, local spineapi.EntityLocalInterface, remote spineapi.EntityRemoteInterface) (*cevcSeriesCore, error) {
	if key == "" {
		return nil, errConfig
	}
	series, err := client.NewTimeSeries(local, remote)
	if err != nil {
		return nil, err
	}
	s := &cevcSeriesCore{key: key, local: local, remote: remote, series: series, results: newCEVCResults()}
	series.AddResultCallback(s.results.note)
	return s, nil
}

func (s *cevcSeriesCore) Bind() (model.MsgCounterType, error) {
	counter, err := s.series.Bind()
	if err != nil {
		return 0, err
	}
	return *counter, nil
}

func (s *cevcSeriesCore) Request() error {
	if _, err := s.series.RequestDescriptions(nil, nil); err != nil {
		return err
	}
	_, err := s.series.RequestData(nil, nil)
	return err
}

func (s *cevcSeriesCore) Result(counter model.MsgCounterType) (model.ResultDataType, bool) {
	return s.results.get(counter)
}

func (s *cevcSeriesCore) Select(sel CEVCTimeSeriesSelector) (CEVCTimeSeriesSelection, error) {
	if err := cevcIdentity(s.key, s.remote, sel.Instance, sel.Device, sel.Entity, sel.FeatureType, sel.Function, model.FeatureTypeTypeTimeSeries, model.FunctionTypeTimeSeriesListData); err != nil {
		return CEVCTimeSeriesSelection{}, err
	}
	if !sel.Writeable || sel.TimeSeriesType != model.TimeSeriesTypeTypeConstraints || sel.Unit != model.UnitOfMeasurementTypeW {
		return CEVCTimeSeriesSelection{}, errCEVCRejected
	}
	if !s.series.HasBinding() {
		return CEVCTimeSeriesSelection{}, errCEVCUnbound
	}
	id, kind, unit := sel.TimeSeriesID, sel.TimeSeriesType, sel.Unit
	descriptions, err := s.series.GetDescriptionsForFilter(model.TimeSeriesDescriptionDataType{
		TimeSeriesId: &id, TimeSeriesType: &kind, TimeSeriesWriteable: util.Ptr(true), Unit: &unit,
	})
	if err != nil || len(descriptions) == 0 {
		return CEVCTimeSeriesSelection{}, errCEVCAbsent
	}
	if len(descriptions) != 1 || descriptions[0].TimeSeriesId == nil || *descriptions[0].TimeSeriesId != id {
		return CEVCTimeSeriesSelection{}, errCEVCAmbiguous
	}
	return CEVCTimeSeriesSelection{TimeSeriesID: id}, nil
}

func (s *cevcSeriesCore) Write(sel CEVCTimeSeriesSelector, period model.TimePeriodType, slot model.TimeSeriesSlotIdType, watts float64, duration model.DurationType) (CEVCTimeSeriesReceipt, error) {
	selection, err := s.Select(sel)
	if err != nil {
		return CEVCTimeSeriesReceipt{}, err
	}
	slots, err := s.slotsForWrite(selection.TimeSeriesID, slot, watts, duration)
	if err != nil {
		return CEVCTimeSeriesReceipt{}, err
	}
	return s.send(selection.TimeSeriesID, seriesPartial(selection.TimeSeriesID, period, slots))
}

func (s *cevcSeriesCore) Inverse(sel CEVCTimeSeriesSelector) (CEVCTimeSeriesReceipt, error) {
	selection, err := s.Select(sel)
	if err != nil {
		return CEVCTimeSeriesReceipt{}, err
	}
	return s.send(selection.TimeSeriesID, seriesSlotDelete(selection.TimeSeriesID))
}

func (s *cevcSeriesCore) Readback(id model.TimeSeriesIdType) (CEVCTimeSeriesReadback, error) {
	data, err := s.series.GetDataForFilter(model.TimeSeriesDescriptionDataType{TimeSeriesId: &id})
	if err != nil || len(data) != 1 || data[0].TimeSeriesId == nil || *data[0].TimeSeriesId != id {
		return CEVCTimeSeriesReadback{}, errCEVCNoData
	}
	return CEVCTimeSeriesReadback{
		TimeSeriesID: id,
		TimePeriod:   data[0].TimePeriod,
		Slots:        append([]model.TimeSeriesSlotType(nil), data[0].TimeSeriesSlot...),
	}, nil
}

func (s *cevcSeriesCore) slotsForWrite(id model.TimeSeriesIdType, slot model.TimeSeriesSlotIdType, watts float64, duration model.DurationType) ([]model.TimeSeriesSlotType, error) {
	current, err := s.series.GetDataForFilter(model.TimeSeriesDescriptionDataType{TimeSeriesId: &id})
	if err != nil || len(current) != 1 {
		return nil, errCEVCNoData
	}
	slots := append([]model.TimeSeriesSlotType(nil), current[0].TimeSeriesSlot...)
	found := false
	for i := range slots {
		if slots[i].TimeSeriesSlotId == nil || *slots[i].TimeSeriesSlotId != slot {
			continue
		}
		if found {
			return nil, errCEVCAmbiguous
		}
		found = true
		slots[i].MaxValue = model.NewScaledNumberType(watts)
		slots[i].Duration = util.Ptr(duration)
	}
	if !found {
		return nil, errCEVCNoSlot
	}
	return slots, nil
}

func (s *cevcSeriesCore) send(id model.TimeSeriesIdType, cmd model.CmdType) (CEVCTimeSeriesReceipt, error) {
	local := s.local.FeatureOfTypeAndRole(model.FeatureTypeTypeTimeSeries, model.RoleTypeClient)
	remote := s.remote.FeatureOfTypeAndRole(model.FeatureTypeTypeTimeSeries, model.RoleTypeServer)
	if local == nil || remote == nil {
		return CEVCTimeSeriesReceipt{}, errCEVCNoMatch
	}
	counter, err := s.remote.Device().Sender().Write(local.Address(), remote.Address(), cmd)
	if err != nil {
		return CEVCTimeSeriesReceipt{}, err
	}
	return CEVCTimeSeriesReceipt{Counter: *counter, TimeSeriesID: id}, nil
}

func seriesPartial(id model.TimeSeriesIdType, period model.TimePeriodType, slots []model.TimeSeriesSlotType) model.CmdType {
	return model.CmdType{
		Function: util.Ptr(model.FunctionTypeTimeSeriesListData),
		Filter:   []model.FilterType{*model.NewFilterTypePartial()},
		TimeSeriesListData: &model.TimeSeriesListDataType{TimeSeriesData: []model.TimeSeriesDataType{{
			TimeSeriesId: &id, TimePeriod: &period, TimeSeriesSlot: slots,
		}}},
	}
}

func seriesSlotDelete(id model.TimeSeriesIdType) model.CmdType {
	return model.CmdType{
		Function: util.Ptr(model.FunctionTypeTimeSeriesListData),
		Filter: []model.FilterType{{
			CmdControl:                  &model.CmdControlType{Delete: &model.ElementTagType{}},
			TimeSeriesListDataSelectors: &model.TimeSeriesListDataSelectorsType{TimeSeriesId: &id},
			TimeSeriesDataElements:      &model.TimeSeriesDataElementsType{TimeSeriesSlot: &model.TimeSeriesSlotElementsType{}},
		}},
		TimeSeriesListData: &model.TimeSeriesListDataType{TimeSeriesData: []model.TimeSeriesDataType{{TimeSeriesId: &id}}},
	}
}

// Bind binds the client feature to the remote server feature.
func (s *CEVCTimeSeries) Bind() (model.MsgCounterType, error) { return s.engine.Bind() }

// Request asks the remote for the series descriptions and the series data.
func (s *CEVCTimeSeries) Request() error { return s.engine.Request() }

// Result returns the result the remote returned for one write counter.
func (s *CEVCTimeSeries) Result(counter model.MsgCounterType) (model.ResultDataType, bool) {
	return s.engine.Result(counter)
}

// Select resolves the selector to one writable constraints series in W.
func (s *CEVCTimeSeries) Select(sel CEVCTimeSeriesSelector) (CEVCTimeSeriesSelection, error) {
	return s.engine.Select(sel)
}

// Write sends the series item through the bound client's sender with a partial filter and no selector.
// The payload keeps every other slot of that series and replaces the named slot's maxValue and duration.
func (s *CEVCTimeSeries) Write(sel CEVCTimeSeriesSelector, period model.TimePeriodType, slot model.TimeSeriesSlotIdType, watts float64, duration model.DurationType) (CEVCTimeSeriesReceipt, error) {
	return s.engine.Write(sel, period, slot, watts, duration)
}

// Inverse removes every slot of the selected series and leaves every other series.
// The filter names the series id and the timeSeriesSlot element. A filter that also
// names timeSeriesSlotId is not sent: on the pinned receiver that selector removes
// the whole series, sibling slot included.
func (s *CEVCTimeSeries) Inverse(sel CEVCTimeSeriesSelector) (CEVCTimeSeriesReceipt, error) {
	return s.engine.Inverse(sel)
}

// Readback returns the series the client holds for one series identifier.
func (s *CEVCTimeSeries) Readback(id model.TimeSeriesIdType) (CEVCTimeSeriesReadback, error) {
	return s.engine.Readback(id)
}
