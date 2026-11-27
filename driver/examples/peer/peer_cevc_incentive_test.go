package peer

import (
	"errors"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-spine-go/model"
	"github.com/Project-Helianthus/helianthus-spine-go/util"
)

func TestPeerCEVCIncentiveTable(t *testing.T) {
	f := newCEVCFixture(t, cevcStandardSeed())
	f.refreshSeries(t, f.seriesSeeded)
	f.refreshIncentive(t, f.incentiveSeeded)
	cevcIncentiveUnbound(t, f)
	f.bindIncentive(t)
	before := f.incentiveRead(t, cevcTariff)
	beforeOther := f.incentiveRead(t, cevcOtherTariff)
	cevcIncentiveSelectorCases(t, f)
	cevcIncentiveSelectsOther(t, f)
	cevcIncentiveRejectsForeignEntry(t, f)
	if cmds := f.toPeer.Recorded(); len(cmds) != 0 {
		t.Fatalf("rejected selectors recorded writes: %+v", cmds)
	}
	cevcJSONEqual(t, f.peerDescription(t, cevcTariff), before.Description)
	cevcJSONEqual(t, f.peerValues(t, cevcTariff), before.Values)
	cevcJSONEqual(t, f.peerDescription(t, cevcOtherTariff), beforeOther.Description)
	cevcJSONEqual(t, f.peerValues(t, cevcOtherTariff), beforeOther.Values)
	cevcIncentiveWriteAndInverse(t, f, before, beforeOther)
	t.Run("WriteableAbsent", func(t *testing.T) {
		seed := cevcStandardSeed()
		seed.tariffs[0].TariffDescription.TariffWriteable = nil
		cevcIncentiveRejectsData(t, seed, errCEVCAbsent, func(f *cevcFixture) bool { return f.incentiveSeeded() })
	})
	t.Run("WriteableFalse", func(t *testing.T) {
		seed := cevcStandardSeed()
		seed.tariffs[0].TariffDescription.TariffWriteable = util.Ptr(false)
		cevcIncentiveRejectsData(t, seed, errCEVCAbsent, func(f *cevcFixture) bool { return f.incentiveSeeded() })
	})
	t.Run("OtherScope", func(t *testing.T) {
		seed := cevcStandardSeed()
		seed.tariffs[0].TariffDescription.ScopeType = util.Ptr(model.ScopeTypeTypeSelfConsumption)
		cevcIncentiveRejectsData(t, seed, errCEVCAbsent, func(f *cevcFixture) bool { return f.incentiveSeeded() })
	})
	t.Run("Ambiguous", func(t *testing.T) {
		seed := cevcStandardSeed()
		seed.tariffs = append(seed.tariffs, seed.tariffs[0])
		cevcIncentiveRejectsData(t, seed, errCEVCAmbiguous, func(f *cevcFixture) bool { return f.otherTariffReady() })
	})
}

func cevcIncentiveUnbound(t *testing.T, f *cevcFixture) {
	t.Helper()
	sel := f.incentiveSelector()
	if _, err := f.table.WriteDescription(sel, cevcWrittenDescription()); !errors.Is(err, errCEVCUnbound) {
		t.Fatalf("description write on an unbound client = %v, want %v", err, errCEVCUnbound)
	}
	if _, err := f.table.WriteValues(sel, cevcWrittenValues()); !errors.Is(err, errCEVCUnbound) {
		t.Fatalf("values write on an unbound client = %v, want %v", err, errCEVCUnbound)
	}
	if _, err := f.table.InverseValues(sel); !errors.Is(err, errCEVCUnbound) {
		t.Fatalf("values inverse on an unbound client = %v, want %v", err, errCEVCUnbound)
	}
	if _, err := f.table.InverseDescription(sel); !errors.Is(err, errCEVCUnbound) {
		t.Fatalf("description inverse on an unbound client = %v, want %v", err, errCEVCUnbound)
	}
	time.Sleep(50 * time.Millisecond)
	if cmds := f.toPeer.Recorded(); len(cmds) != 0 {
		t.Fatalf("the peer recorded writes from an unbound client: %+v", cmds)
	}
}

func cevcIncentiveSelectorCases(t *testing.T, f *cevcFixture) {
	t.Helper()
	base := f.incentiveSelector()
	cases := []struct {
		name   string
		mutate func(*CEVCIncentiveSelector)
		want   error
	}{
		{"Instance", func(s *CEVCIncentiveSelector) { s.Instance = "beta" }, errCEVCNoMatch},
		{"Device", func(s *CEVCIncentiveSelector) { s.Device = "d:_n:other" }, errCEVCNoMatch},
		{"Entity", func(s *CEVCIncentiveSelector) { s.Entity = []model.AddressEntityType{2} }, errCEVCNoMatch},
		{"FeatureType", func(s *CEVCIncentiveSelector) { s.FeatureType = model.FeatureTypeTypeTimeSeries }, errCEVCNoMatch},
		{"Function", func(s *CEVCIncentiveSelector) { s.Function = model.FunctionTypeIncentiveTableData }, errCEVCNoMatch},
		{"TariffID", func(s *CEVCIncentiveSelector) { s.TariffID = cevcUnknownTariff }, errCEVCAbsent},
		{"ScopeType", func(s *CEVCIncentiveSelector) { s.ScopeType = model.ScopeTypeTypeSelfConsumption }, errCEVCRejected},
		{"Writeable", func(s *CEVCIncentiveSelector) { s.Writeable = false }, errCEVCRejected},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sel := base
			sel.Entity = cevcCopyEntity(base.Entity)
			c.mutate(&sel)
			description := cevcWrittenDescription()
			description.TariffDescription.TariffId = util.Ptr(sel.TariffID)
			values := cevcWrittenValues()
			values.Tariff.TariffId = util.Ptr(sel.TariffID)
			if _, err := f.table.WriteDescription(sel, description); !errors.Is(err, c.want) {
				t.Fatalf("description write err = %v, want %v", err, c.want)
			}
			if _, err := f.table.WriteValues(sel, values); !errors.Is(err, c.want) {
				t.Fatalf("values write err = %v, want %v", err, c.want)
			}
			if _, err := f.table.InverseValues(sel); !errors.Is(err, c.want) {
				t.Fatalf("values inverse err = %v, want %v", err, c.want)
			}
			if _, err := f.table.InverseDescription(sel); !errors.Is(err, c.want) {
				t.Fatalf("description inverse err = %v, want %v", err, c.want)
			}
		})
	}
}

func cevcIncentiveSelectsOther(t *testing.T, f *cevcFixture) {
	t.Helper()
	sel := f.incentiveSelector()
	sel.TariffID = cevcOtherTariff
	got, err := f.table.Select(sel)
	if err != nil || got.TariffID != cevcOtherTariff {
		t.Fatalf("other tariff selected %+v, err %v", got, err)
	}
}

func cevcIncentiveRejectsForeignEntry(t *testing.T, f *cevcFixture) {
	t.Helper()
	sel := f.incentiveSelector()
	description := cevcWrittenDescription()
	description.TariffDescription.TariffId = util.Ptr(cevcOtherTariff)
	if _, err := f.table.WriteDescription(sel, description); !errors.Is(err, errCEVCNoMatch) {
		t.Fatalf("description write err = %v, want %v", err, errCEVCNoMatch)
	}
	values := cevcWrittenValues()
	values.Tariff.TariffId = util.Ptr(cevcOtherTariff)
	if _, err := f.table.WriteValues(sel, values); !errors.Is(err, errCEVCNoMatch) {
		t.Fatalf("values write err = %v, want %v", err, errCEVCNoMatch)
	}
}

func cevcIncentiveRejectsData(t *testing.T, seed cevcSeed, want error, ready func(*cevcFixture) bool) {
	t.Helper()
	f := newCEVCFixture(t, seed)
	f.refreshIncentive(t, func() bool { return ready(f) })
	f.bindIncentive(t)
	sel := f.incentiveSelector()
	if _, err := f.table.WriteDescription(sel, cevcWrittenDescription()); !errors.Is(err, want) {
		t.Fatalf("description write err = %v, want %v", err, want)
	}
	if _, err := f.table.WriteValues(sel, cevcWrittenValues()); !errors.Is(err, want) {
		t.Fatalf("values write err = %v, want %v", err, want)
	}
	if _, err := f.table.InverseValues(sel); !errors.Is(err, want) {
		t.Fatalf("values inverse err = %v, want %v", err, want)
	}
	if _, err := f.table.InverseDescription(sel); !errors.Is(err, want) {
		t.Fatalf("description inverse err = %v, want %v", err, want)
	}
	time.Sleep(50 * time.Millisecond)
	if cmds := f.toPeer.Recorded(); len(cmds) != 0 {
		t.Fatalf("the peer recorded writes for a rejected description: %+v", cmds)
	}
}

func (f *cevcFixture) incentiveRead(t *testing.T, id model.TariffIdType) CEVCIncentiveReadback {
	t.Helper()
	got, err := f.table.Readback(id)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func cevcIncentiveWriteAndInverse(t *testing.T, f *cevcFixture, before, beforeOther CEVCIncentiveReadback) {
	t.Helper()
	series := f.peerSeries(t, cevcSeries)
	otherSeries := f.peerSeries(t, cevcOtherSeries)
	writtenDescription, writtenValues := cevcWrittenDescription(), cevcWrittenValues()
	description := cevcWriteDescription(t, f, writtenDescription, before, beforeOther)
	values := cevcWriteValues(t, f, writtenValues, writtenDescription, beforeOther)
	inverseValues := cevcInverseValues(t, f, writtenDescription, beforeOther)
	inverseDescription := cevcInverseDescription(t, f, beforeOther)
	cevcDistinct(t, description.Counter, values.Counter, inverseValues.Counter, inverseDescription.Counter)
	cevcJSONEqual(t, f.peerSeries(t, cevcSeries), series)
	cevcJSONEqual(t, f.peerSeries(t, cevcOtherSeries), otherSeries)
	cevcAssertIncentiveCmds(t, f.toPeer.Recorded(), writtenDescription, writtenValues, beforeOther)
}

func cevcWriteDescription(t *testing.T, f *cevcFixture, written model.IncentiveTableDescriptionType, before, beforeOther CEVCIncentiveReadback) CEVCIncentiveReceipt {
	t.Helper()
	receipt, err := f.table.WriteDescription(f.incentiveSelector(), written)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.TariffID != cevcTariff {
		t.Fatalf("description receipt tariff %d", receipt.TariffID)
	}
	cevcAck(t, f.table.Result, receipt.Counter)
	f.refreshIncentive(t, func() bool {
		got, err := f.table.Readback(cevcTariff)
		return err == nil && got.Description != nil && got.Description.TariffDescription != nil && got.Description.TariffDescription.Label != nil && *got.Description.TariffDescription.Label == "written-one"
	})
	got := f.incentiveRead(t, cevcTariff)
	cevcAssertDescription(t, got.Description, written)
	cevcJSONEqual(t, got.Values, before.Values)
	cevcAssertIncentiveOther(t, f, beforeOther)
	cevcAssertDescription(t, f.peerDescription(t, cevcTariff), written)
	cevcJSONEqual(t, f.peerValues(t, cevcTariff), before.Values)
	return receipt
}

func cevcWriteValues(t *testing.T, f *cevcFixture, written model.IncentiveTableType, description model.IncentiveTableDescriptionType, beforeOther CEVCIncentiveReadback) CEVCIncentiveReceipt {
	t.Helper()
	receipt, err := f.table.WriteValues(f.incentiveSelector(), written)
	if err != nil {
		t.Fatal(err)
	}
	cevcAck(t, f.table.Result, receipt.Counter)
	f.refreshIncentive(t, func() bool {
		got, err := f.table.Readback(cevcTariff)
		return err == nil && got.Values != nil && len(got.Values.IncentiveSlot) == 1 && got.Values.IncentiveSlot[0].TimeInterval != nil && got.Values.IncentiveSlot[0].TimeInterval.TimeTableId != nil && *got.Values.IncentiveSlot[0].TimeInterval.TimeTableId == 4
	})
	got := f.incentiveRead(t, cevcTariff)
	cevcAssertValues(t, got.Values, written)
	cevcAssertDescription(t, got.Description, description)
	cevcAssertIncentiveOther(t, f, beforeOther)
	cevcAssertValues(t, f.peerValues(t, cevcTariff), written)
	cevcAssertDescription(t, f.peerDescription(t, cevcTariff), description)
	return receipt
}

func cevcInverseValues(t *testing.T, f *cevcFixture, description model.IncentiveTableDescriptionType, beforeOther CEVCIncentiveReadback) CEVCIncentiveReceipt {
	t.Helper()
	receipt, err := f.table.InverseValues(f.incentiveSelector())
	if err != nil {
		t.Fatal(err)
	}
	cevcAck(t, f.table.Result, receipt.Counter)
	f.refreshIncentive(t, func() bool {
		got, err := f.table.Readback(cevcTariff)
		return err == nil && got.Description != nil && got.Values == nil
	})
	got := f.incentiveRead(t, cevcTariff)
	if got.Values != nil || f.peerValues(t, cevcTariff) != nil {
		t.Fatal("the selected tariff still has values after the values inverse")
	}
	cevcAssertDescription(t, got.Description, description)
	cevcAssertDescription(t, f.peerDescription(t, cevcTariff), description)
	cevcAssertIncentiveOther(t, f, beforeOther)
	return receipt
}

func cevcInverseDescription(t *testing.T, f *cevcFixture, beforeOther CEVCIncentiveReadback) CEVCIncentiveReceipt {
	t.Helper()
	receipt, err := f.table.InverseDescription(f.incentiveSelector())
	if err != nil {
		t.Fatal(err)
	}
	cevcAck(t, f.table.Result, receipt.Counter)
	f.refreshIncentive(t, func() bool {
		got, err := f.table.Readback(cevcTariff)
		return err == nil && got.Description == nil && got.Values == nil
	})
	got := f.incentiveRead(t, cevcTariff)
	if got.Description != nil || got.Values != nil || f.peerDescription(t, cevcTariff) != nil || f.peerValues(t, cevcTariff) != nil {
		t.Fatal("the selected tariff still has an entry after the description inverse")
	}
	cevcAssertIncentiveOther(t, f, beforeOther)
	return receipt
}

func cevcAssertIncentiveOther(t *testing.T, f *cevcFixture, before CEVCIncentiveReadback) {
	t.Helper()
	got := f.incentiveRead(t, cevcOtherTariff)
	cevcJSONEqual(t, got.Description, before.Description)
	cevcJSONEqual(t, got.Values, before.Values)
	cevcJSONEqual(t, f.peerDescription(t, cevcOtherTariff), before.Description)
	cevcJSONEqual(t, f.peerValues(t, cevcOtherTariff), before.Values)
}

func cevcAssertDescription(t *testing.T, got *model.IncentiveTableDescriptionType, want model.IncentiveTableDescriptionType) {
	t.Helper()
	if got == nil || got.TariffDescription == nil || want.TariffDescription == nil || len(got.Tier) != 1 || len(want.Tier) != 1 {
		t.Fatalf("description = %+v", got)
	}
	g, w := got.TariffDescription, want.TariffDescription
	if g.TariffId == nil || w.TariffId == nil || *g.TariffId != *w.TariffId || g.ScopeType == nil || w.ScopeType == nil || *g.ScopeType != *w.ScopeType || g.TariffWriteable == nil || w.TariffWriteable == nil || *g.TariffWriteable != *w.TariffWriteable || g.Label == nil || w.Label == nil || *g.Label != *w.Label {
		t.Fatalf("tariff description = %+v, want %+v", g, w)
	}
	gt, wt := got.Tier[0], want.Tier[0]
	if gt.TierDescription == nil || wt.TierDescription == nil || gt.TierDescription.TierId == nil || *gt.TierDescription.TierId != *wt.TierDescription.TierId || gt.TierDescription.TierType == nil || *gt.TierDescription.TierType != *wt.TierDescription.TierType {
		t.Fatalf("tier = %+v", gt.TierDescription)
	}
	if len(gt.BoundaryDescription) != 1 || len(wt.BoundaryDescription) != 1 || gt.BoundaryDescription[0].BoundaryId == nil || *gt.BoundaryDescription[0].BoundaryId != *wt.BoundaryDescription[0].BoundaryId || gt.BoundaryDescription[0].BoundaryType == nil || *gt.BoundaryDescription[0].BoundaryType != *wt.BoundaryDescription[0].BoundaryType {
		t.Fatalf("boundary = %+v", gt.BoundaryDescription)
	}
	if len(gt.IncentiveDescription) != 1 || len(wt.IncentiveDescription) != 1 || gt.IncentiveDescription[0].IncentiveId == nil || *gt.IncentiveDescription[0].IncentiveId != *wt.IncentiveDescription[0].IncentiveId || gt.IncentiveDescription[0].IncentiveType == nil || *gt.IncentiveDescription[0].IncentiveType != *wt.IncentiveDescription[0].IncentiveType {
		t.Fatalf("incentive = %+v", gt.IncentiveDescription)
	}
	cevcJSONEqual(t, got, &want)
}

func cevcAssertValues(t *testing.T, got *model.IncentiveTableType, want model.IncentiveTableType) {
	t.Helper()
	if got == nil || got.Tariff == nil || got.Tariff.TariffId == nil || want.Tariff == nil || want.Tariff.TariffId == nil || *got.Tariff.TariffId != *want.Tariff.TariffId || len(got.IncentiveSlot) != 1 || len(want.IncentiveSlot) != 1 {
		t.Fatalf("values = %+v", got)
	}
	gs, ws := got.IncentiveSlot[0], want.IncentiveSlot[0]
	if gs.TimeInterval == nil || ws.TimeInterval == nil || gs.TimeInterval.TimeTableId == nil || *gs.TimeInterval.TimeTableId != *ws.TimeInterval.TimeTableId || gs.TimeInterval.TimeSlotId == nil || *gs.TimeInterval.TimeSlotId != *ws.TimeInterval.TimeSlotId || cevcRelativeText(t, gs.TimeInterval.StartTime) != cevcRelativeText(t, ws.TimeInterval.StartTime) || cevcRelativeText(t, gs.TimeInterval.EndTime) != cevcRelativeText(t, ws.TimeInterval.EndTime) {
		t.Fatalf("interval = %+v", gs.TimeInterval)
	}
	if len(gs.Tier) != 1 || len(ws.Tier) != 1 || gs.Tier[0].Tier == nil || gs.Tier[0].Tier.TierId == nil || *gs.Tier[0].Tier.TierId != *ws.Tier[0].Tier.TierId {
		t.Fatalf("tier = %+v", gs.Tier)
	}
	if len(gs.Tier[0].Boundary) != 1 || gs.Tier[0].Boundary[0].BoundaryId == nil || *gs.Tier[0].Boundary[0].BoundaryId != *ws.Tier[0].Boundary[0].BoundaryId || cevcWatts(t, gs.Tier[0].Boundary[0].LowerBoundaryValue) != cevcWatts(t, ws.Tier[0].Boundary[0].LowerBoundaryValue) || cevcWatts(t, gs.Tier[0].Boundary[0].UpperBoundaryValue) != cevcWatts(t, ws.Tier[0].Boundary[0].UpperBoundaryValue) {
		t.Fatalf("boundary = %+v", gs.Tier[0].Boundary)
	}
	if len(gs.Tier[0].Incentive) != 1 || gs.Tier[0].Incentive[0].IncentiveId == nil || *gs.Tier[0].Incentive[0].IncentiveId != *ws.Tier[0].Incentive[0].IncentiveId || cevcWatts(t, gs.Tier[0].Incentive[0].Value) != cevcWatts(t, ws.Tier[0].Incentive[0].Value) {
		t.Fatalf("incentive = %+v", gs.Tier[0].Incentive)
	}
	cevcJSONEqual(t, got, &want)
}

func cevcRelativeText(t *testing.T, value *model.AbsoluteOrRecurringTimeType) string {
	t.Helper()
	if value == nil || value.Relative == nil {
		t.Fatal("relative time is absent")
	}
	return string(*value.Relative)
}

func cevcAssertIncentiveCmds(t *testing.T, cmds []model.CmdType, description model.IncentiveTableDescriptionType, values model.IncentiveTableType, other CEVCIncentiveReadback) {
	t.Helper()
	cevcForbidSlotFilter(t, cmds)
	if len(cmds) != 4 {
		t.Fatalf("recorded %d writes, want description, values, values inverse and description inverse", len(cmds))
	}
	for _, cmd := range cmds {
		if len(cmd.Filter) != 0 || cmd.TimeSeriesListData != nil {
			t.Fatalf("an incentive write carried a filter or a series payload: %+v", cmd)
		}
	}
	cevcAssertDescriptionCmd(t, cmds[0], true, description, other.Description)
	cevcAssertValuesCmd(t, cmds[1], true, values, other.Values)
	cevcAssertValuesCmd(t, cmds[2], false, values, other.Values)
	cevcAssertDescriptionCmd(t, cmds[3], false, description, other.Description)
}

func cevcAssertDescriptionCmd(t *testing.T, cmd model.CmdType, include bool, selected model.IncentiveTableDescriptionType, other *model.IncentiveTableDescriptionType) {
	t.Helper()
	if cmd.IncentiveTableDescriptionData == nil {
		t.Fatal("description payload is absent")
	}
	entries := cmd.IncentiveTableDescriptionData.IncentiveTableDescription
	want := 1
	if include {
		want = 2
	}
	if len(entries) != want {
		t.Fatalf("description payload holds %d tariffs, want %d", len(entries), want)
	}
	var gotSelected, gotOther *model.IncentiveTableDescriptionType
	for i := range entries {
		switch {
		case tariffDescriptionID(&entries[i], cevcTariff):
			gotSelected = &entries[i]
		case tariffDescriptionID(&entries[i], cevcOtherTariff):
			gotOther = &entries[i]
		default:
			t.Fatalf("description payload holds an unexpected tariff: %+v", entries[i].TariffDescription)
		}
	}
	if include {
		cevcAssertDescription(t, gotSelected, selected)
	} else if gotSelected != nil {
		t.Fatal("the description inverse still carries the selected tariff")
	}
	cevcJSONEqual(t, gotOther, other)
}

func cevcAssertValuesCmd(t *testing.T, cmd model.CmdType, include bool, selected model.IncentiveTableType, other *model.IncentiveTableType) {
	t.Helper()
	if cmd.IncentiveTableData == nil {
		t.Fatal("values payload is absent")
	}
	entries := cmd.IncentiveTableData.IncentiveTable
	want := 1
	if include {
		want = 2
	}
	if len(entries) != want {
		t.Fatalf("values payload holds %d tariffs, want %d", len(entries), want)
	}
	var gotSelected, gotOther *model.IncentiveTableType
	for i := range entries {
		switch {
		case tariffValueID(&entries[i], cevcTariff):
			gotSelected = &entries[i]
		case tariffValueID(&entries[i], cevcOtherTariff):
			gotOther = &entries[i]
		default:
			t.Fatalf("values payload holds an unexpected tariff: %+v", entries[i].Tariff)
		}
	}
	if include {
		cevcAssertValues(t, gotSelected, selected)
	} else if gotSelected != nil {
		t.Fatal("the values inverse still carries the selected tariff")
	}
	cevcJSONEqual(t, gotOther, other)
}
