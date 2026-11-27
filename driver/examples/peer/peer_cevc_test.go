package peer

import (
	"encoding/json"
	"sync"
	"testing"

	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	"github.com/Project-Helianthus/helianthus-spine-go/model"
	"github.com/Project-Helianthus/helianthus-spine-go/spine"
	"github.com/Project-Helianthus/helianthus-spine-go/util"

	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
	shipmocks "github.com/Project-Helianthus/helianthus-ship-go/mocks"
)

const (
	cevcKey            = "alpha"
	cevcPeerDev        = "d:_n:cevc-peer"
	cevcLocalDev       = "d:_n:cevc-local"
	cevcSeries         = model.TimeSeriesIdType(1)
	cevcOtherSeries    = model.TimeSeriesIdType(3)
	cevcUnknownSeries  = model.TimeSeriesIdType(9)
	cevcSlot           = model.TimeSeriesSlotIdType(1)
	cevcOtherSlot      = model.TimeSeriesSlotIdType(2)
	cevcUnknownSlot    = model.TimeSeriesSlotIdType(9)
	cevcTariff         = model.TariffIdType(1)
	cevcOtherTariff    = model.TariffIdType(2)
	cevcUnknownTariff  = model.TariffIdType(9)
	cevcWrittenWatts   = 2500.0
	cevcForecastWatts  = 40.0
	cevcSiblingWatts   = 500.0
	cevcUnrelatedWatts = 80.0
)

type cevcLink struct {
	mu     sync.Mutex
	queue  chan []byte
	done   chan struct{}
	cmds   []model.CmdType
	writer shipapi.ShipConnectionDataWriterInterface
}

func newCEVCLink(t *testing.T) *cevcLink {
	t.Helper()
	l := &cevcLink{queue: make(chan []byte, 1024), done: make(chan struct{})}
	t.Cleanup(func() { close(l.done) })
	writer := shipmocks.NewShipConnectionDataWriterInterface(t)
	writer.EXPECT().WriteShipMessageWithPayload("mock.Anything").Run(func(message []byte) {
		copied := append([]byte(nil), message...)
		select {
		case l.queue <- copied:
		case <-l.done:
		}
	}).Return().Maybe()
	l.writer = writer
	return l
}

func (l *cevcLink) deliver(to shipapi.ShipConnectionDataReaderInterface, observe bool) {
	go func() {
		for {
			select {
			case message := <-l.queue:
				if observe {
					l.observe(message)
				}
				to.HandleShipPayloadMessage(message)
			case <-l.done:
				return
			}
		}
	}()
}

func (l *cevcLink) observe(message []byte) {
	var datagram model.Datagram
	if json.Unmarshal(message, &datagram) != nil || datagram.Datagram.Header.CmdClassifier == nil {
		return
	}
	if *datagram.Datagram.Header.CmdClassifier != model.CmdClassifierTypeWrite {
		return
	}
	l.mu.Lock()
	l.cmds = append(l.cmds, datagram.Datagram.Payload.Cmd...)
	l.mu.Unlock()
}

func (l *cevcLink) Recorded() []model.CmdType {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]model.CmdType(nil), l.cmds...)
}

type cevcSeed struct {
	descriptions []model.TimeSeriesDescriptionDataType
	series       []model.TimeSeriesDataType
	tariffs      []model.IncentiveTableDescriptionType
	values       []model.IncentiveTableType
}

type cevcFixture struct {
	series       *CEVCTimeSeries
	table        *CEVCIncentive
	toPeer       *cevcLink
	device       *spine.DeviceLocal
	seriesServer *spine.FeatureLocal
	tableServer  *spine.FeatureLocal
}

func cevcPeriod(start, end string) model.TimePeriodType {
	return model.TimePeriodType{
		StartTime: util.Ptr(model.AbsoluteOrRelativeTimeType(start)),
		EndTime:   util.Ptr(model.AbsoluteOrRelativeTimeType(end)),
	}
}

func cevcSeriesSlot(id model.TimeSeriesSlotIdType, watts float64, duration string, forecast *float64) model.TimeSeriesSlotType {
	slot := model.TimeSeriesSlotType{
		TimeSeriesSlotId: util.Ptr(id),
		Duration:         util.Ptr(model.DurationType(duration)),
		MaxValue:         model.NewScaledNumberType(watts),
	}
	if forecast != nil {
		slot.Value = model.NewScaledNumberType(*forecast)
	}
	return slot
}

func cevcSeriesItem(id model.TimeSeriesIdType, start, end string, slots ...model.TimeSeriesSlotType) model.TimeSeriesDataType {
	period := cevcPeriod(start, end)
	return model.TimeSeriesDataType{TimeSeriesId: util.Ptr(id), TimePeriod: &period, TimeSeriesSlot: slots}
}

func cevcSeriesDescription(id model.TimeSeriesIdType, kind model.TimeSeriesTypeType, writeable *bool, unit model.UnitOfMeasurementType) model.TimeSeriesDescriptionDataType {
	return model.TimeSeriesDescriptionDataType{
		TimeSeriesId: util.Ptr(id), TimeSeriesType: util.Ptr(kind), TimeSeriesWriteable: writeable, Unit: util.Ptr(unit),
	}
}

func cevcRelative(value string) *model.AbsoluteOrRecurringTimeType {
	return &model.AbsoluteOrRecurringTimeType{Relative: util.Ptr(model.DurationType(value))}
}

func cevcIncentiveDescription(id model.TariffIdType, label string, tier model.TierIdType, tierType model.TierTypeType, boundary model.TierBoundaryIdType, boundaryType model.TierBoundaryTypeType, incentive model.IncentiveIdType, incentiveType model.IncentiveTypeType) model.IncentiveTableDescriptionType {
	return model.IncentiveTableDescriptionType{
		TariffDescription: &model.TariffDescriptionDataType{
			TariffId: util.Ptr(id), TariffWriteable: util.Ptr(true),
			ScopeType: util.Ptr(model.ScopeTypeTypeSimpleIncentiveTable), Label: util.Ptr(model.LabelType(label)),
		},
		Tier: []model.IncentiveTableDescriptionTierType{{
			TierDescription:      &model.TierDescriptionDataType{TierId: util.Ptr(tier), TierType: util.Ptr(tierType)},
			BoundaryDescription:  []model.TierBoundaryDescriptionDataType{{BoundaryId: util.Ptr(boundary), BoundaryType: util.Ptr(boundaryType)}},
			IncentiveDescription: []model.IncentiveDescriptionDataType{{IncentiveId: util.Ptr(incentive), IncentiveType: util.Ptr(incentiveType)}},
		}},
	}
}

func cevcIncentiveValues(id model.TariffIdType, table model.TimeTableIdType, slot model.TimeSlotIdType, start, end string, tier model.TierIdType, boundary model.TierBoundaryIdType, lower, upper float64, incentive model.IncentiveIdType, value float64) model.IncentiveTableType {
	return model.IncentiveTableType{
		Tariff: &model.TariffDataType{TariffId: util.Ptr(id)},
		IncentiveSlot: []model.IncentiveTableIncentiveSlotType{{
			TimeInterval: &model.TimeTableDataType{
				TimeTableId: util.Ptr(table), TimeSlotId: util.Ptr(slot), StartTime: cevcRelative(start), EndTime: cevcRelative(end),
			},
			Tier: []model.IncentiveTableTierType{{
				Tier: &model.TierDataType{TierId: util.Ptr(tier)},
				Boundary: []model.TierBoundaryDataType{{
					BoundaryId: util.Ptr(boundary), LowerBoundaryValue: model.NewScaledNumberType(lower), UpperBoundaryValue: model.NewScaledNumberType(upper),
				}},
				Incentive: []model.IncentiveDataType{{IncentiveId: util.Ptr(incentive), Value: model.NewScaledNumberType(value)}},
			}},
		}},
	}
}

func cevcStandardSeed() cevcSeed {
	forecast := cevcForecastWatts
	return cevcSeed{
		descriptions: []model.TimeSeriesDescriptionDataType{
			cevcSeriesDescription(cevcSeries, model.TimeSeriesTypeTypeConstraints, util.Ptr(true), model.UnitOfMeasurementTypeW),
			cevcSeriesDescription(cevcOtherSeries, model.TimeSeriesTypeTypeConstraints, util.Ptr(true), model.UnitOfMeasurementTypeW),
		},
		series: []model.TimeSeriesDataType{
			cevcSeriesItem(cevcSeries, "PT0S", "PT1H", cevcSeriesSlot(cevcOtherSlot, cevcSiblingWatts, "PT15M", nil), cevcSeriesSlot(cevcSlot, 1000, "PT15M", &forecast)),
			cevcSeriesItem(cevcOtherSeries, "PT3H", "PT4H", cevcSeriesSlot(cevcSlot, cevcUnrelatedWatts, "PT5M", nil)),
		},
		tariffs: []model.IncentiveTableDescriptionType{
			cevcIncentiveDescription(cevcTariff, "seed-one", 1, model.TierTypeTypeFixedCost, 1, model.TierBoundaryTypeTypePowerBoundary, 1, model.IncentiveTypeTypeAbsoluteCost),
			cevcIncentiveDescription(cevcOtherTariff, "seed-two", 2, model.TierTypeTypeDynamicCost, 2, model.TierBoundaryTypeTypeEnergyBoundary, 2, model.IncentiveTypeTypeRelativeCost),
		},
		values: []model.IncentiveTableType{
			cevcIncentiveValues(cevcTariff, 1, 1, "PT0S", "PT1H", 1, 1, 0, 1, 1, 1),
			cevcIncentiveValues(cevcOtherTariff, 2, 1, "PT3H", "PT4H", 2, 2, 2, 4, 2, 2),
		},
	}
}

func cevcWrittenDescription() model.IncentiveTableDescriptionType {
	return cevcIncentiveDescription(cevcTariff, "written-one", 4, model.TierTypeTypeDynamicCost, 9, model.TierBoundaryTypeTypeEnergyBoundary, 8, model.IncentiveTypeTypeRelativeCost)
}

func cevcWrittenValues() model.IncentiveTableType {
	return cevcIncentiveValues(cevcTariff, 4, 2, "PT1H", "PT2H", 4, 9, 1, 5, 8, 3)
}

func newCEVCFixture(t *testing.T, seed cevcSeed) *cevcFixture {
	t.Helper()
	local := spine.NewDeviceLocal("", "", "", "", cevcLocalDev, model.DeviceTypeTypeEnergyManagementSystem, "")
	localEntity := spine.NewEntityLocal(local, model.EntityTypeTypeCEM, []model.AddressEntityType{1}, 0)
	localEntity.AddFeature(spine.NewFeatureLocal(1, localEntity, model.FeatureTypeTypeTimeSeries, model.RoleTypeClient))
	localEntity.AddFeature(spine.NewFeatureLocal(2, localEntity, model.FeatureTypeTypeIncentiveTable, model.RoleTypeClient))
	local.AddEntity(localEntity)

	peerDevice := spine.NewDeviceLocal("", "", "", "", cevcPeerDev, model.DeviceTypeTypeChargingStation, "")
	peerEntity := spine.NewEntityLocal(peerDevice, model.EntityTypeTypeEV, []model.AddressEntityType{1}, 0)
	seriesServer := spine.NewFeatureLocal(1, peerEntity, model.FeatureTypeTypeTimeSeries, model.RoleTypeServer)
	seriesServer.AddFunctionType(model.FunctionTypeTimeSeriesDescriptionListData, true, false)
	seriesServer.AddFunctionType(model.FunctionTypeTimeSeriesListData, true, true)
	tableServer := spine.NewFeatureLocal(2, peerEntity, model.FeatureTypeTypeIncentiveTable, model.RoleTypeServer)
	tableServer.AddFunctionType(model.FunctionTypeIncentiveTableDescriptionData, true, true)
	tableServer.AddFunctionType(model.FunctionTypeIncentiveTableData, true, true)
	peerEntity.AddFeature(seriesServer)
	peerEntity.AddFeature(tableServer)
	peerDevice.AddEntity(peerEntity)

	seriesServer.SetData(model.FunctionTypeTimeSeriesDescriptionListData, &model.TimeSeriesDescriptionListDataType{TimeSeriesDescriptionData: seed.descriptions})
	seriesServer.SetData(model.FunctionTypeTimeSeriesListData, &model.TimeSeriesListDataType{TimeSeriesData: seed.series})
	tableServer.SetData(model.FunctionTypeIncentiveTableDescriptionData, &model.IncentiveTableDescriptionDataType{IncentiveTableDescription: seed.tariffs})
	tableServer.SetData(model.FunctionTypeIncentiveTableData, &model.IncentiveTableDataType{IncentiveTable: seed.values})

	toPeer := newCEVCLink(t)
	toLocal := newCEVCLink(t)
	readerOfPeer := local.SetupRemoteDevice("cevc-peer", toPeer.writer)
	readerOfLocal := peerDevice.SetupRemoteDevice("cevc-local", toLocal.writer)
	toPeer.deliver(readerOfLocal, true)
	toLocal.deliver(readerOfPeer, false)

	var remote spineapi.EntityRemoteInterface
	eventually(t, "the remote advertises both scenarios", func() bool {
		device := local.RemoteDeviceForSki("cevc-peer")
		if device == nil {
			return false
		}
		remote = device.Entity([]model.AddressEntityType{1})
		return remote != nil &&
			device.FeatureByEntityTypeAndRole(remote, model.FeatureTypeTypeTimeSeries, model.RoleTypeServer) != nil &&
			device.FeatureByEntityTypeAndRole(remote, model.FeatureTypeTypeIncentiveTable, model.RoleTypeServer) != nil
	})
	series, err := newCEVCTimeSeries(cevcKey, localEntity, remote)
	if err != nil {
		t.Fatal(err)
	}
	table, err := newCEVCIncentive(cevcKey, localEntity, remote)
	if err != nil {
		t.Fatal(err)
	}
	return &cevcFixture{series: series, table: table, toPeer: toPeer, device: peerDevice, seriesServer: seriesServer, tableServer: tableServer}
}

func (f *cevcFixture) seriesSelector() CEVCTimeSeriesSelector {
	return CEVCTimeSeriesSelector{
		Instance: cevcKey, Device: cevcPeerDev, Entity: []model.AddressEntityType{1},
		FeatureType: model.FeatureTypeTypeTimeSeries, Function: model.FunctionTypeTimeSeriesListData,
		TimeSeriesID: cevcSeries, TimeSeriesType: model.TimeSeriesTypeTypeConstraints, Writeable: true, Unit: model.UnitOfMeasurementTypeW,
	}
}

func (f *cevcFixture) incentiveSelector() CEVCIncentiveSelector {
	return CEVCIncentiveSelector{
		Instance: cevcKey, Device: cevcPeerDev, Entity: []model.AddressEntityType{1},
		FeatureType: model.FeatureTypeTypeIncentiveTable, Function: model.FunctionTypeIncentiveTableDescriptionData,
		TariffID: cevcTariff, ScopeType: model.ScopeTypeTypeSimpleIncentiveTable, Writeable: true,
	}
}

func (f *cevcFixture) bindSeries(t *testing.T) {
	t.Helper()
	if _, err := f.series.Bind(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the peer holds the series binding", func() bool {
		return len(f.device.BindingManager().BindingsOnFeature(*f.seriesServer.Address())) == 1
	})
}

func (f *cevcFixture) bindIncentive(t *testing.T) {
	t.Helper()
	if _, err := f.table.Bind(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the peer holds the incentive binding", func() bool {
		return len(f.device.BindingManager().BindingsOnFeature(*f.tableServer.Address())) == 1
	})
}

func (f *cevcFixture) refreshSeries(t *testing.T, ready func() bool) {
	t.Helper()
	if err := f.series.Request(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the client holds the series", ready)
}

func (f *cevcFixture) refreshIncentive(t *testing.T, ready func() bool) {
	t.Helper()
	if err := f.table.Request(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the client holds the incentive table", ready)
}

func (f *cevcFixture) seriesSeeded() bool {
	one, err1 := f.series.Readback(cevcSeries)
	other, err2 := f.series.Readback(cevcOtherSeries)
	return err1 == nil && err2 == nil && len(one.Slots) == 2 && len(other.Slots) == 1
}

func (f *cevcFixture) incentiveSeeded() bool {
	one, err1 := f.table.Readback(cevcTariff)
	other, err2 := f.table.Readback(cevcOtherTariff)
	return err1 == nil && err2 == nil && one.Description != nil && one.Values != nil && other.Description != nil && other.Values != nil
}

func (f *cevcFixture) otherSeriesReady() bool {
	got, err := f.series.Readback(cevcOtherSeries)
	return err == nil && len(got.Slots) == 1
}

func (f *cevcFixture) otherTariffReady() bool {
	got, err := f.table.Readback(cevcOtherTariff)
	return err == nil && got.Description != nil && got.Values != nil
}

func cevcAck(t *testing.T, result func(model.MsgCounterType) (model.ResultDataType, bool), counter model.MsgCounterType) {
	t.Helper()
	var got model.ResultDataType
	eventually(t, "the peer returned a result for the write", func() bool {
		var ok bool
		got, ok = result(counter)
		return ok
	})
	if got.ErrorNumber == nil || *got.ErrorNumber != model.ErrorNumberTypeNoError {
		text := ""
		if got.Description != nil {
			text = string(*got.Description)
		}
		t.Fatalf("write %d was refused: number %v %s", counter, got.ErrorNumber, text)
	}
}

func cevcJSONEqual(t *testing.T, got, want any) {
	t.Helper()
	left, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	right, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(left) != string(right) {
		t.Fatalf("got %s\nwant %s", left, right)
	}
}

func cevcWatts(t *testing.T, number *model.ScaledNumberType) float64 {
	t.Helper()
	if number == nil {
		t.Fatal("scaled number is absent")
	}
	return number.GetValue()
}

func cevcDuration(t *testing.T, value *model.DurationType) string {
	t.Helper()
	if value == nil {
		t.Fatal("duration is absent")
	}
	return string(*value)
}

func cevcPeriodText(t *testing.T, period *model.TimePeriodType) (string, string) {
	t.Helper()
	if period == nil || period.StartTime == nil || period.EndTime == nil {
		t.Fatal("time period is absent")
	}
	return string(*period.StartTime), string(*period.EndTime)
}

func cevcSlotByID(t *testing.T, slots []model.TimeSeriesSlotType, id model.TimeSeriesSlotIdType) model.TimeSeriesSlotType {
	t.Helper()
	var found *model.TimeSeriesSlotType
	for i := range slots {
		if slots[i].TimeSeriesSlotId == nil || *slots[i].TimeSeriesSlotId != id {
			continue
		}
		if found != nil {
			t.Fatalf("slot %d is stored twice", id)
		}
		item := slots[i]
		found = &item
	}
	if found == nil {
		t.Fatalf("slot %d is absent", id)
	}
	return *found
}

func cevcSeriesByID(t *testing.T, data []model.TimeSeriesDataType, id model.TimeSeriesIdType) model.TimeSeriesDataType {
	t.Helper()
	var found *model.TimeSeriesDataType
	for i := range data {
		if data[i].TimeSeriesId == nil || *data[i].TimeSeriesId != id {
			continue
		}
		if found != nil {
			t.Fatalf("series %d is stored twice", id)
		}
		item := data[i]
		found = &item
	}
	if found == nil {
		t.Fatalf("series %d is absent", id)
	}
	return *found
}

func (f *cevcFixture) peerSeries(t *testing.T, id model.TimeSeriesIdType) model.TimeSeriesDataType {
	t.Helper()
	list, ok := f.seriesServer.DataCopy(model.FunctionTypeTimeSeriesListData).(*model.TimeSeriesListDataType)
	if !ok || list == nil {
		t.Fatal("the peer holds no series list")
	}
	return cevcSeriesByID(t, list.TimeSeriesData, id)
}

func (f *cevcFixture) peerDescription(t *testing.T, id model.TariffIdType) *model.IncentiveTableDescriptionType {
	t.Helper()
	list, ok := f.tableServer.DataCopy(model.FunctionTypeIncentiveTableDescriptionData).(*model.IncentiveTableDescriptionDataType)
	if !ok || list == nil {
		t.Fatal("the peer holds no incentive descriptions")
	}
	var found *model.IncentiveTableDescriptionType
	for i := range list.IncentiveTableDescription {
		item := &list.IncentiveTableDescription[i]
		if !tariffDescriptionID(item, id) {
			continue
		}
		if found != nil {
			t.Fatalf("tariff %d description is stored twice", id)
		}
		found = item
	}
	return found
}

func (f *cevcFixture) peerValues(t *testing.T, id model.TariffIdType) *model.IncentiveTableType {
	t.Helper()
	list, ok := f.tableServer.DataCopy(model.FunctionTypeIncentiveTableData).(*model.IncentiveTableDataType)
	if !ok || list == nil {
		t.Fatal("the peer holds no incentive values")
	}
	var found *model.IncentiveTableType
	for i := range list.IncentiveTable {
		item := &list.IncentiveTable[i]
		if !tariffValueID(item, id) {
			continue
		}
		if found != nil {
			t.Fatalf("tariff %d values are stored twice", id)
		}
		found = item
	}
	return found
}

func cevcForbidSlotFilter(t *testing.T, cmds []model.CmdType) {
	t.Helper()
	for _, cmd := range cmds {
		for _, filter := range cmd.Filter {
			if filter.TimeSeriesListDataSelectors != nil && filter.TimeSeriesListDataSelectors.TimeSeriesSlotId != nil {
				t.Fatalf("a filter carried timeSeriesSlotId: %+v", filter.TimeSeriesListDataSelectors)
			}
		}
	}
}

func cevcDistinct(t *testing.T, counters ...model.MsgCounterType) {
	t.Helper()
	seen := map[model.MsgCounterType]struct{}{}
	for _, counter := range counters {
		if _, ok := seen[counter]; ok {
			t.Fatalf("two writes share counter %d", counter)
		}
		seen[counter] = struct{}{}
	}
}

func cevcCopyEntity(sel []model.AddressEntityType) []model.AddressEntityType {
	return append([]model.AddressEntityType(nil), sel...)
}
