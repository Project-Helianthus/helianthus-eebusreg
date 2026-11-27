package peer

import (
	"github.com/Project-Helianthus/helianthus-eebus-go/features/client"
	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	"github.com/Project-Helianthus/helianthus-spine-go/model"
	"github.com/Project-Helianthus/helianthus-spine-go/util"
)

// CEVCIncentiveSelector names one remote incentive tariff description of one driver instance.
type CEVCIncentiveSelector struct {
	Instance    string
	Device      model.AddressDeviceType
	Entity      []model.AddressEntityType
	FeatureType model.FeatureTypeType
	Function    model.FunctionType
	TariffID    model.TariffIdType
	ScopeType   model.ScopeTypeType
	Writeable   bool
}

// CEVCIncentiveSelection is the tariff a selector resolved to.
type CEVCIncentiveSelection struct {
	TariffID model.TariffIdType
}

// CEVCIncentiveReceipt names the write a description, values or inverse send returned.
type CEVCIncentiveReceipt struct {
	Counter  model.MsgCounterType
	TariffID model.TariffIdType
}

// CEVCIncentiveReadback is the tariff the client holds for one tariff identifier.
type CEVCIncentiveReadback struct {
	TariffID    model.TariffIdType
	Description *model.IncentiveTableDescriptionType
	Values      *model.IncentiveTableType
}

// CEVCIncentive sends and reads back one remote incentive table of one driver instance.
// Description and value writes use the native client with no filter. The pinned receiver
// answers any filtered IncentiveTableDataType write with partial updates are not
// supported for type 'IncentiveTableDataType' and changes nothing.
type CEVCIncentive struct {
	engine cevcIncentiveEngine
}

type cevcIncentiveEngine interface {
	Bind() (model.MsgCounterType, error)
	Request() error
	Result(model.MsgCounterType) (model.ResultDataType, bool)
	Select(CEVCIncentiveSelector) (CEVCIncentiveSelection, error)
	WriteDescription(CEVCIncentiveSelector, model.IncentiveTableDescriptionType) (CEVCIncentiveReceipt, error)
	WriteValues(CEVCIncentiveSelector, model.IncentiveTableType) (CEVCIncentiveReceipt, error)
	InverseDescription(CEVCIncentiveSelector) (CEVCIncentiveReceipt, error)
	InverseValues(CEVCIncentiveSelector) (CEVCIncentiveReceipt, error)
	Readback(model.TariffIdType) (CEVCIncentiveReadback, error)
}

type cevcIncentiveCore struct {
	key     string
	remote  spineapi.EntityRemoteInterface
	table   *client.IncentiveTable
	results *cevcResults
}

func newCEVCIncentive(key string, local spineapi.EntityLocalInterface, remote spineapi.EntityRemoteInterface) (*CEVCIncentive, error) {
	core, err := newCEVCIncentiveCore(key, local, remote)
	if err != nil {
		return nil, err
	}
	return &CEVCIncentive{engine: core}, nil
}

func newCEVCIncentiveCore(key string, local spineapi.EntityLocalInterface, remote spineapi.EntityRemoteInterface) (*cevcIncentiveCore, error) {
	if key == "" {
		return nil, errConfig
	}
	table, err := client.NewIncentiveTable(local, remote)
	if err != nil {
		return nil, err
	}
	c := &cevcIncentiveCore{key: key, remote: remote, table: table, results: newCEVCResults()}
	table.AddResultCallback(c.results.note)
	return c, nil
}

func (c *cevcIncentiveCore) Bind() (model.MsgCounterType, error) {
	counter, err := c.table.Bind()
	if err != nil {
		return 0, err
	}
	return *counter, nil
}

func (c *cevcIncentiveCore) Request() error {
	if _, err := c.table.RequestDescriptions(); err != nil {
		return err
	}
	_, err := c.table.RequestValues()
	return err
}

func (c *cevcIncentiveCore) Result(counter model.MsgCounterType) (model.ResultDataType, bool) {
	return c.results.get(counter)
}

func (c *cevcIncentiveCore) Select(sel CEVCIncentiveSelector) (CEVCIncentiveSelection, error) {
	if err := cevcIdentity(c.key, c.remote, sel.Instance, sel.Device, sel.Entity, sel.FeatureType, sel.Function, model.FeatureTypeTypeIncentiveTable, model.FunctionTypeIncentiveTableDescriptionData); err != nil {
		return CEVCIncentiveSelection{}, err
	}
	if !sel.Writeable || sel.ScopeType != model.ScopeTypeTypeSimpleIncentiveTable {
		return CEVCIncentiveSelection{}, errCEVCRejected
	}
	if !c.table.HasBinding() {
		return CEVCIncentiveSelection{}, errCEVCUnbound
	}
	id, scope := sel.TariffID, sel.ScopeType
	descriptions, err := c.table.GetDescriptionsForFilter(model.TariffDescriptionDataType{
		TariffId: &id, ScopeType: &scope, TariffWriteable: util.Ptr(true),
	})
	if err != nil || len(descriptions) == 0 {
		return CEVCIncentiveSelection{}, errCEVCAbsent
	}
	if len(descriptions) != 1 || !tariffDescriptionID(&descriptions[0], id) {
		return CEVCIncentiveSelection{}, errCEVCAmbiguous
	}
	return CEVCIncentiveSelection{TariffID: id}, nil
}

func (c *cevcIncentiveCore) WriteDescription(sel CEVCIncentiveSelector, entry model.IncentiveTableDescriptionType) (CEVCIncentiveReceipt, error) {
	return c.writeDescriptions(sel, &entry)
}

func (c *cevcIncentiveCore) WriteValues(sel CEVCIncentiveSelector, entry model.IncentiveTableType) (CEVCIncentiveReceipt, error) {
	return c.writeValues(sel, &entry)
}

func (c *cevcIncentiveCore) InverseDescription(sel CEVCIncentiveSelector) (CEVCIncentiveReceipt, error) {
	return c.writeDescriptions(sel, nil)
}

func (c *cevcIncentiveCore) InverseValues(sel CEVCIncentiveSelector) (CEVCIncentiveReceipt, error) {
	return c.writeValues(sel, nil)
}

func (c *cevcIncentiveCore) Readback(id model.TariffIdType) (CEVCIncentiveReadback, error) {
	out := CEVCIncentiveReadback{TariffID: id}
	descriptions, err := c.table.GetDescriptionsForFilter(model.TariffDescriptionDataType{TariffId: &id})
	if err != nil {
		return CEVCIncentiveReadback{}, errCEVCNoData
	}
	if len(descriptions) == 1 {
		item := descriptions[0]
		out.Description = &item
	}
	values, err := c.table.GetData()
	if err != nil {
		return CEVCIncentiveReadback{}, errCEVCNoData
	}
	for _, item := range values {
		if tariffValueID(&item, id) {
			item := item
			out.Values = &item
			break
		}
	}
	return out, nil
}

func (c *cevcIncentiveCore) writeDescriptions(sel CEVCIncentiveSelector, entry *model.IncentiveTableDescriptionType) (CEVCIncentiveReceipt, error) {
	selection, err := c.prepare(sel, entry == nil || tariffDescriptionID(entry, sel.TariffID))
	if err != nil {
		return CEVCIncentiveReceipt{}, err
	}
	if entry != nil && !tariffDescriptionID(entry, selection.TariffID) {
		return CEVCIncentiveReceipt{}, errCEVCNoMatch
	}
	current, err := c.table.GetDescriptionsForFilter(model.TariffDescriptionDataType{})
	if err != nil {
		return CEVCIncentiveReceipt{}, errCEVCNoData
	}
	next := make([]model.IncentiveTableDescriptionType, 0, len(current)+1)
	for _, item := range current {
		if tariffDescriptionID(&item, selection.TariffID) {
			continue
		}
		next = append(next, item)
	}
	if entry != nil {
		next = append(next, *entry)
	}
	counter, err := c.table.WriteDescriptions(next)
	if err != nil {
		return CEVCIncentiveReceipt{}, err
	}
	return CEVCIncentiveReceipt{Counter: *counter, TariffID: selection.TariffID}, nil
}

func (c *cevcIncentiveCore) writeValues(sel CEVCIncentiveSelector, entry *model.IncentiveTableType) (CEVCIncentiveReceipt, error) {
	selection, err := c.prepare(sel, entry == nil || tariffValueID(entry, sel.TariffID))
	if err != nil {
		return CEVCIncentiveReceipt{}, err
	}
	if entry != nil && !tariffValueID(entry, selection.TariffID) {
		return CEVCIncentiveReceipt{}, errCEVCNoMatch
	}
	current, err := c.table.GetData()
	if err != nil {
		return CEVCIncentiveReceipt{}, errCEVCNoData
	}
	next := make([]model.IncentiveTableType, 0, len(current)+1)
	for _, item := range current {
		if tariffValueID(&item, selection.TariffID) {
			continue
		}
		next = append(next, item)
	}
	if entry != nil {
		next = append(next, *entry)
	}
	counter, err := c.table.WriteValues(next)
	if err != nil {
		return CEVCIncentiveReceipt{}, err
	}
	return CEVCIncentiveReceipt{Counter: *counter, TariffID: selection.TariffID}, nil
}

func (c *cevcIncentiveCore) prepare(sel CEVCIncentiveSelector, entryMatches bool) (CEVCIncentiveSelection, error) {
	if !entryMatches {
		return CEVCIncentiveSelection{}, errCEVCNoMatch
	}
	return c.Select(sel)
}

func tariffDescriptionID(entry *model.IncentiveTableDescriptionType, id model.TariffIdType) bool {
	return entry != nil && entry.TariffDescription != nil && entry.TariffDescription.TariffId != nil && *entry.TariffDescription.TariffId == id
}

func tariffValueID(entry *model.IncentiveTableType, id model.TariffIdType) bool {
	return entry != nil && entry.Tariff != nil && entry.Tariff.TariffId != nil && *entry.Tariff.TariffId == id
}

// Bind binds the client feature to the remote server feature.
func (c *CEVCIncentive) Bind() (model.MsgCounterType, error) { return c.engine.Bind() }

// Request asks the remote for the incentive descriptions and the incentive values.
func (c *CEVCIncentive) Request() error { return c.engine.Request() }

// Result returns the result the remote returned for one write counter.
func (c *CEVCIncentive) Result(counter model.MsgCounterType) (model.ResultDataType, bool) {
	return c.engine.Result(counter)
}

// Select resolves the selector to one writable simple incentive tariff.
func (c *CEVCIncentive) Select(sel CEVCIncentiveSelector) (CEVCIncentiveSelection, error) {
	return c.engine.Select(sel)
}

// WriteDescription writes the whole description list through the native client, with no filter.
// The selected tariff carries the caller's entry. Every other tariff stays as read immediately before the write.
func (c *CEVCIncentive) WriteDescription(sel CEVCIncentiveSelector, entry model.IncentiveTableDescriptionType) (CEVCIncentiveReceipt, error) {
	return c.engine.WriteDescription(sel, entry)
}

// WriteValues writes the whole value list through the native client, with no filter.
// The selected tariff carries the caller's entry. Every other tariff stays as read immediately before the write.
func (c *CEVCIncentive) WriteValues(sel CEVCIncentiveSelector, entry model.IncentiveTableType) (CEVCIncentiveReceipt, error) {
	return c.engine.WriteValues(sel, entry)
}

// InverseDescription writes the descriptions again with the selected tariff left out.
func (c *CEVCIncentive) InverseDescription(sel CEVCIncentiveSelector) (CEVCIncentiveReceipt, error) {
	return c.engine.InverseDescription(sel)
}

// InverseValues writes the values again with the selected tariff left out.
func (c *CEVCIncentive) InverseValues(sel CEVCIncentiveSelector) (CEVCIncentiveReceipt, error) {
	return c.engine.InverseValues(sel)
}

// Readback returns the description and values the client holds for one tariff identifier.
// A nil description or nil values means that function has no entry for the tariff.
func (c *CEVCIncentive) Readback(id model.TariffIdType) (CEVCIncentiveReadback, error) {
	return c.engine.Readback(id)
}
