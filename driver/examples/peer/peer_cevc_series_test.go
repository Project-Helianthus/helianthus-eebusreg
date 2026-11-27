package peer

import (
	"errors"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-spine-go/model"
	"github.com/Project-Helianthus/helianthus-spine-go/util"
)

func TestPeerCEVCTimeSeriesLimit(t *testing.T) {
	f := newCEVCFixture(t, cevcStandardSeed())
	f.refreshSeries(t, f.seriesSeeded)
	f.refreshIncentive(t, f.incentiveSeeded)
	cevcSeriesUnbound(t, f)
	f.bindSeries(t)
	before := f.peerSeries(t, cevcSeries)
	other := f.peerSeries(t, cevcOtherSeries)
	cevcSeriesSelectorCases(t, f)
	cevcSeriesSelectsOther(t, f)
	cevcSeriesUnknownSlot(t, f)
	if cmds := f.toPeer.Recorded(); len(cmds) != 0 {
		t.Fatalf("rejected selectors recorded writes: %+v", cmds)
	}
	cevcJSONEqual(t, f.peerSeries(t, cevcSeries), before)
	cevcJSONEqual(t, f.peerSeries(t, cevcOtherSeries), other)
	cevcSeriesWriteAndInverse(t, f)
	t.Run("WriteableAbsent", func(t *testing.T) {
		seed := cevcStandardSeed()
		seed.descriptions[0].TimeSeriesWriteable = nil
		cevcSeriesRejectsData(t, seed, errCEVCAbsent)
	})
	t.Run("WriteableFalse", func(t *testing.T) {
		seed := cevcStandardSeed()
		seed.descriptions[0].TimeSeriesWriteable = util.Ptr(false)
		cevcSeriesRejectsData(t, seed, errCEVCAbsent)
	})
	t.Run("OtherType", func(t *testing.T) {
		seed := cevcStandardSeed()
		seed.descriptions[0].TimeSeriesType = util.Ptr(model.TimeSeriesTypeTypePlan)
		cevcSeriesRejectsData(t, seed, errCEVCAbsent)
	})
	t.Run("OtherUnit", func(t *testing.T) {
		seed := cevcStandardSeed()
		seed.descriptions[0].Unit = util.Ptr(model.UnitOfMeasurementTypeA)
		cevcSeriesRejectsData(t, seed, errCEVCAbsent)
	})
	t.Run("Ambiguous", func(t *testing.T) {
		seed := cevcStandardSeed()
		seed.descriptions = append(seed.descriptions, seed.descriptions[0])
		cevcSeriesRejectsData(t, seed, errCEVCAmbiguous)
	})
	t.Run("DuplicateSlot", func(t *testing.T) {
		cevcSeriesDuplicateSlot(t)
	})
}

func cevcSeriesUnbound(t *testing.T, f *cevcFixture) {
	t.Helper()
	sel := f.seriesSelector()
	period := cevcPeriod("PT1H", "PT2H")
	if _, err := f.series.Write(sel, period, cevcSlot, cevcWrittenWatts, "PT30M"); !errors.Is(err, errCEVCUnbound) {
		t.Fatalf("write on an unbound client = %v, want %v", err, errCEVCUnbound)
	}
	if _, err := f.series.Inverse(sel); !errors.Is(err, errCEVCUnbound) {
		t.Fatalf("inverse on an unbound client = %v, want %v", err, errCEVCUnbound)
	}
	time.Sleep(50 * time.Millisecond)
	if cmds := f.toPeer.Recorded(); len(cmds) != 0 {
		t.Fatalf("the peer recorded writes from an unbound client: %+v", cmds)
	}
}

func cevcSeriesSelectorCases(t *testing.T, f *cevcFixture) {
	t.Helper()
	base := f.seriesSelector()
	period := cevcPeriod("PT1H", "PT2H")
	cases := []struct {
		name   string
		mutate func(*CEVCTimeSeriesSelector)
		want   error
	}{
		{"Instance", func(s *CEVCTimeSeriesSelector) { s.Instance = "beta" }, errCEVCNoMatch},
		{"Device", func(s *CEVCTimeSeriesSelector) { s.Device = "d:_n:other" }, errCEVCNoMatch},
		{"Entity", func(s *CEVCTimeSeriesSelector) { s.Entity = []model.AddressEntityType{2} }, errCEVCNoMatch},
		{"FeatureType", func(s *CEVCTimeSeriesSelector) { s.FeatureType = model.FeatureTypeTypeIncentiveTable }, errCEVCNoMatch},
		{"Function", func(s *CEVCTimeSeriesSelector) { s.Function = model.FunctionTypeTimeSeriesDescriptionListData }, errCEVCNoMatch},
		{"TimeSeriesID", func(s *CEVCTimeSeriesSelector) { s.TimeSeriesID = cevcUnknownSeries }, errCEVCAbsent},
		{"TimeSeriesType", func(s *CEVCTimeSeriesSelector) { s.TimeSeriesType = model.TimeSeriesTypeTypePlan }, errCEVCRejected},
		{"Writeable", func(s *CEVCTimeSeriesSelector) { s.Writeable = false }, errCEVCRejected},
		{"Unit", func(s *CEVCTimeSeriesSelector) { s.Unit = model.UnitOfMeasurementTypeA }, errCEVCRejected},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sel := base
			sel.Entity = cevcCopyEntity(base.Entity)
			c.mutate(&sel)
			if _, err := f.series.Write(sel, period, cevcSlot, cevcWrittenWatts, "PT30M"); !errors.Is(err, c.want) {
				t.Fatalf("write err = %v, want %v", err, c.want)
			}
			if _, err := f.series.Inverse(sel); !errors.Is(err, c.want) {
				t.Fatalf("inverse err = %v, want %v", err, c.want)
			}
		})
	}
}

func cevcSeriesSelectsOther(t *testing.T, f *cevcFixture) {
	t.Helper()
	sel := f.seriesSelector()
	sel.TimeSeriesID = cevcOtherSeries
	got, err := f.series.Select(sel)
	if err != nil || got.TimeSeriesID != cevcOtherSeries {
		t.Fatalf("other series selected %+v, err %v", got, err)
	}
}

func cevcSeriesUnknownSlot(t *testing.T, f *cevcFixture) {
	t.Helper()
	_, err := f.series.Write(f.seriesSelector(), cevcPeriod("PT1H", "PT2H"), cevcUnknownSlot, cevcWrittenWatts, "PT30M")
	if !errors.Is(err, errCEVCNoSlot) {
		t.Fatalf("write of an absent slot = %v, want %v", err, errCEVCNoSlot)
	}
}

func cevcSeriesRejectsData(t *testing.T, seed cevcSeed, want error) {
	t.Helper()
	f := newCEVCFixture(t, seed)
	f.refreshSeries(t, f.otherSeriesReady)
	f.bindSeries(t)
	sel := f.seriesSelector()
	if _, err := f.series.Write(sel, cevcPeriod("PT1H", "PT2H"), cevcSlot, cevcWrittenWatts, "PT30M"); !errors.Is(err, want) {
		t.Fatalf("write err = %v, want %v", err, want)
	}
	if _, err := f.series.Inverse(sel); !errors.Is(err, want) {
		t.Fatalf("inverse err = %v, want %v", err, want)
	}
	time.Sleep(50 * time.Millisecond)
	if cmds := f.toPeer.Recorded(); len(cmds) != 0 {
		t.Fatalf("the peer recorded writes for a rejected description: %+v", cmds)
	}
}

func cevcSeriesDuplicateSlot(t *testing.T) {
	t.Helper()
	seed := cevcStandardSeed()
	seed.series[0].TimeSeriesSlot = append(seed.series[0].TimeSeriesSlot, seed.series[0].TimeSeriesSlot[1])
	f := newCEVCFixture(t, seed)
	f.refreshSeries(t, f.otherSeriesReady)
	f.bindSeries(t)
	if _, err := f.series.Select(f.seriesSelector()); err != nil {
		t.Fatal(err)
	}
	_, err := f.series.Write(f.seriesSelector(), cevcPeriod("PT1H", "PT2H"), cevcSlot, cevcWrittenWatts, "PT30M")
	if !errors.Is(err, errCEVCAmbiguous) {
		t.Fatalf("write of a duplicated slot = %v, want %v", err, errCEVCAmbiguous)
	}
	time.Sleep(50 * time.Millisecond)
	if cmds := f.toPeer.Recorded(); len(cmds) != 0 {
		t.Fatalf("the peer recorded writes for a duplicated slot: %+v", cmds)
	}
}

func cevcSeriesWriteAndInverse(t *testing.T, f *cevcFixture) {
	t.Helper()
	before := f.seriesRead(t, cevcSeries)
	beforeOther := f.seriesRead(t, cevcOtherSeries)
	beforePeerOther := f.peerSeries(t, cevcOtherSeries)
	sibling := cevcSlotByID(t, before.Slots, cevcOtherSlot)
	receipt, err := f.series.Write(f.seriesSelector(), cevcPeriod("PT1H", "PT2H"), cevcSlot, cevcWrittenWatts, "PT30M")
	if err != nil {
		t.Fatal(err)
	}
	if receipt.TimeSeriesID != cevcSeries {
		t.Fatalf("write receipt series %d", receipt.TimeSeriesID)
	}
	cevcAck(t, f.series.Result, receipt.Counter)
	f.refreshSeries(t, func() bool {
		got, err := f.series.Readback(cevcSeries)
		return err == nil && len(got.Slots) == 2 && cevcSlotWatts(got.Slots, cevcSlot) == cevcWrittenWatts
	})
	cevcAssertReadSeries(t, f.seriesRead(t, cevcSeries), sibling)
	cevcJSONEqual(t, f.seriesRead(t, cevcOtherSeries), beforeOther)
	cevcAssertPeerSeries(t, f.peerSeries(t, cevcSeries), sibling)
	cevcJSONEqual(t, f.peerSeries(t, cevcOtherSeries), beforePeerOther)

	inverse, err := f.series.Inverse(f.seriesSelector())
	if err != nil {
		t.Fatal(err)
	}
	if inverse.TimeSeriesID != receipt.TimeSeriesID || inverse.Counter == receipt.Counter {
		t.Fatalf("inverse receipt %+v does not name a distinct write of series %d", inverse, receipt.TimeSeriesID)
	}
	cevcAck(t, f.series.Result, inverse.Counter)
	f.refreshSeries(t, func() bool {
		got, err := f.series.Readback(cevcSeries)
		return err == nil && len(got.Slots) == 0
	})
	if slots := f.seriesRead(t, cevcSeries).Slots; len(slots) != 0 {
		t.Fatalf("series slots after removal = %+v", slots)
	}
	if slots := f.peerSeries(t, cevcSeries).TimeSeriesSlot; len(slots) != 0 {
		t.Fatalf("peer series slots after removal = %+v", slots)
	}
	cevcJSONEqual(t, f.seriesRead(t, cevcOtherSeries), beforeOther)
	cevcJSONEqual(t, f.peerSeries(t, cevcOtherSeries), beforePeerOther)
	cevcAssertSeriesCmds(t, f.toPeer.Recorded(), sibling)
}

func (f *cevcFixture) seriesRead(t *testing.T, id model.TimeSeriesIdType) CEVCTimeSeriesReadback {
	t.Helper()
	got, err := f.series.Readback(id)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func cevcSlotWatts(slots []model.TimeSeriesSlotType, id model.TimeSeriesSlotIdType) float64 {
	for _, slot := range slots {
		if slot.TimeSeriesSlotId != nil && *slot.TimeSeriesSlotId == id && slot.MaxValue != nil {
			return slot.MaxValue.GetValue()
		}
	}
	return 0
}

func cevcAssertReadSeries(t *testing.T, got CEVCTimeSeriesReadback, sibling model.TimeSeriesSlotType) {
	t.Helper()
	start, end := cevcPeriodText(t, got.TimePeriod)
	if start != "PT1H" || end != "PT2H" {
		t.Fatalf("time period %s-%s, want PT1H-PT2H", start, end)
	}
	if len(got.Slots) != 2 {
		t.Fatalf("series holds %d slots, want the written slot and its sibling", len(got.Slots))
	}
	slot := cevcSlotByID(t, got.Slots, cevcSlot)
	if cevcWatts(t, slot.MaxValue) != cevcWrittenWatts || cevcDuration(t, slot.Duration) != "PT30M" || cevcWatts(t, slot.Value) != cevcForecastWatts {
		t.Fatalf("written slot = max %v duration %s value %v", cevcWatts(t, slot.MaxValue), cevcDuration(t, slot.Duration), cevcWatts(t, slot.Value))
	}
	cevcJSONEqual(t, cevcSlotByID(t, got.Slots, cevcOtherSlot), sibling)
}

func cevcAssertPeerSeries(t *testing.T, got model.TimeSeriesDataType, sibling model.TimeSeriesSlotType) {
	t.Helper()
	start, end := cevcPeriodText(t, got.TimePeriod)
	if got.TimeSeriesId == nil || *got.TimeSeriesId != cevcSeries || start != "PT1H" || end != "PT2H" || len(got.TimeSeriesSlot) != 2 {
		t.Fatalf("peer series = id %v period %s-%s slots %d", got.TimeSeriesId, start, end, len(got.TimeSeriesSlot))
	}
	slot := cevcSlotByID(t, got.TimeSeriesSlot, cevcSlot)
	if cevcWatts(t, slot.MaxValue) != cevcWrittenWatts || cevcDuration(t, slot.Duration) != "PT30M" || cevcWatts(t, slot.Value) != cevcForecastWatts {
		t.Fatalf("peer written slot = max %v duration %s value %v", cevcWatts(t, slot.MaxValue), cevcDuration(t, slot.Duration), cevcWatts(t, slot.Value))
	}
	cevcJSONEqual(t, cevcSlotByID(t, got.TimeSeriesSlot, cevcOtherSlot), sibling)
}

func cevcAssertSeriesCmds(t *testing.T, cmds []model.CmdType, sibling model.TimeSeriesSlotType) {
	t.Helper()
	cevcForbidSlotFilter(t, cmds)
	if len(cmds) != 2 {
		t.Fatalf("recorded %d writes, want the partial write and the slot removal", len(cmds))
	}
	for _, cmd := range cmds {
		if cmd.IncentiveTableData != nil || cmd.IncentiveTableDescriptionData != nil || cmd.TimeSeriesListData == nil {
			t.Fatalf("a series pattern recorded an unrelated write: %+v", cmd)
		}
	}
	write := cmds[0]
	if write.Function == nil || *write.Function != model.FunctionTypeTimeSeriesListData || len(write.Filter) != 1 {
		t.Fatalf("partial write cmd = %+v", write)
	}
	partial := write.Filter[0]
	if partial.CmdControl == nil || partial.CmdControl.Partial == nil || partial.CmdControl.Delete != nil || partial.TimeSeriesListDataSelectors != nil || partial.TimeSeriesDataElements != nil {
		t.Fatalf("partial filter = %+v", partial)
	}
	if write.TimeSeriesListData == nil || len(write.TimeSeriesListData.TimeSeriesData) != 1 {
		t.Fatal("the partial write did not carry exactly the selected series")
	}
	item := write.TimeSeriesListData.TimeSeriesData[0]
	start, end := cevcPeriodText(t, item.TimePeriod)
	if item.TimeSeriesId == nil || *item.TimeSeriesId != cevcSeries || start != "PT1H" || end != "PT2H" || len(item.TimeSeriesSlot) != 2 {
		t.Fatalf("partial payload = %+v", item)
	}
	slot := cevcSlotByID(t, item.TimeSeriesSlot, cevcSlot)
	if cevcWatts(t, slot.MaxValue) != cevcWrittenWatts || cevcDuration(t, slot.Duration) != "PT30M" || cevcWatts(t, slot.Value) != cevcForecastWatts {
		t.Fatalf("payload slot = max %v duration %s value %v", cevcWatts(t, slot.MaxValue), cevcDuration(t, slot.Duration), cevcWatts(t, slot.Value))
	}
	cevcJSONEqual(t, cevcSlotByID(t, item.TimeSeriesSlot, cevcOtherSlot), sibling)

	removal := cmds[1]
	if removal.Function == nil || *removal.Function != model.FunctionTypeTimeSeriesListData || len(removal.Filter) != 1 {
		t.Fatalf("removal cmd = %+v", removal)
	}
	filter := removal.Filter[0]
	if filter.CmdControl == nil || filter.CmdControl.Delete == nil || filter.CmdControl.Partial != nil {
		t.Fatalf("removal filter control = %+v", filter.CmdControl)
	}
	if filter.TimeSeriesListDataSelectors == nil || filter.TimeSeriesListDataSelectors.TimeSeriesId == nil || *filter.TimeSeriesListDataSelectors.TimeSeriesId != cevcSeries || filter.TimeSeriesListDataSelectors.TimeSeriesSlotId != nil {
		t.Fatalf("removal selector = %+v", filter.TimeSeriesListDataSelectors)
	}
	if filter.TimeSeriesDataElements == nil || filter.TimeSeriesDataElements.TimeSeriesSlot == nil {
		t.Fatal("removal filter has no timeSeriesSlot element mark")
	}
	if removal.TimeSeriesListData == nil || len(removal.TimeSeriesListData.TimeSeriesData) != 1 {
		t.Fatal("the removal did not carry exactly the selected series")
	}
	deleted := removal.TimeSeriesListData.TimeSeriesData[0]
	if deleted.TimeSeriesId == nil || *deleted.TimeSeriesId != cevcSeries || deleted.TimePeriod != nil || len(deleted.TimeSeriesSlot) != 0 {
		t.Fatalf("removal payload = %+v", deleted)
	}
}
