package base

import (
	"errors"
	"testing"

	eebusruntime "github.com/Project-Helianthus/helianthus-eebusreg"
	semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"
	semdriver "github.com/Project-Helianthus/helianthus-semreg/semreg/v1/driver"
	"github.com/Project-Helianthus/helianthus-semreg/semreg/v1/graph"
)

// TestOutputFeedMemberSum varies every field carried from one same-cut member sum.
func TestOutputFeedMemberSum(t *testing.T) {
	fact := powerFact("phase-a", "a", "10", "9.9.9")
	// The phase fact is 10. The sum is 7×10^1. Using the phase value as the total fails.
	success := readSum(t, &fact, wattSum("7", 1, "unit.watt"), nil, nil)
	if success.Withheld || success.Coefficient != "7" || success.Exponent != 1 || success.Unit != "unit.watt" || success.Reason != "" || success.NotIncludedCount != 0 || success.Coefficient == "10" {
		t.Fatalf("success %+v", success)
	}
	coefficient := readSum(t, &fact, wattSum("8", 1, "unit.watt"), nil, nil)
	if coefficient.Coefficient != "8" || coefficient.Exponent != 1 {
		t.Fatalf("coefficient %+v", coefficient)
	}
	exponent := readSum(t, &fact, wattSum("7", -4, "unit.watt"), nil, nil)
	if exponent.Exponent != -4 || exponent.Coefficient != "7" {
		t.Fatalf("exponent %+v", exponent)
	}
	unit := readSum(t, &fact, wattSum("7", 1, "unit.volt"), nil, nil)
	if unit.Unit != "unit.volt" || unit.Coefficient != "7" {
		t.Fatalf("unit %+v", unit)
	}

	partial := readSum(t, &fact, graph.MemberSum{Withheld: true, Reason: graph.SumPartialCut}, nil, oneGap("asset-missing", graph.NotIncludedSourceUnavailable))
	if !partial.Withheld || partial.Reason != "partial_cut" || partial.Coefficient != "" || partial.NotIncludedCount != 1 || len(partial.NotIncluded) != 1 || partial.NotIncluded[0].Asset != "asset-missing" || partial.NotIncluded[0].Reason != "source_unavailable" {
		t.Fatalf("partial %+v", partial)
	}
	native := readSum(t, &fact, graph.MemberSum{Withheld: true, Reason: graph.SumNativeConvention}, nil, oneGap("asset-missing", graph.NotIncludedSourceUnavailable))
	if native.Reason != "native_convention" {
		t.Fatalf("reason %+v", native)
	}
	otherAsset := readSum(t, &fact, graph.MemberSum{Withheld: true, Reason: graph.SumPartialCut}, nil, oneGap("asset-other", graph.NotIncludedSourceUnavailable))
	if len(otherAsset.NotIncluded) != 1 || otherAsset.NotIncluded[0].Asset != "asset-other" {
		t.Fatalf("asset %+v", otherAsset.NotIncluded)
	}
	otherReason := readSum(t, &fact, graph.MemberSum{Withheld: true, Reason: graph.SumPartialCut}, nil, oneGap("asset-missing", graph.NotIncludedPublicationWithheld))
	if otherReason.NotIncluded[0].Reason != string(graph.NotIncludedPublicationWithheld) {
		t.Fatalf("asset reason %+v", otherReason.NotIncluded)
	}
	counted := readSum(t, &fact, graph.MemberSum{Withheld: true, Reason: graph.SumPartialCut}, nil, []graph.NotIncludedAsset{
		{AssetID: "asset-missing", Reason: graph.NotIncludedSourceUnavailable},
		{AssetID: "asset-other", Reason: graph.NotIncludedSourceUnavailable},
	})
	if counted.NotIncludedCount != 2 || len(counted.NotIncluded) != 2 {
		t.Fatalf("count %+v", counted)
	}

	// A sum error is not a reason and does not drop the energy omissions.
	failed := readSum(t, &fact, graph.MemberSum{}, errors.New("members unavailable"), oneGap("asset-missing", graph.NotIncludedSourceUnavailable))
	if !failed.Withheld || failed.Reason != "" || failed.Coefficient != "" || failed.NotIncludedCount != 1 || failed.NotIncluded[0].Asset != "asset-missing" {
		t.Fatalf("sum error %+v", failed)
	}

	fallback := sumCall(t, nil, wattSum("1", 0, "unit.watt"), nil, nil)
	if fallback.definition.ID != elementActivePower || fallback.definition.Version != "1.0.0" || fallback.definition.Pack.ID != "helianthus.pack.evse" || fallback.definition.Pack.Version != "1.0.0" {
		t.Fatalf("fallback definition %+v", fallback.definition)
	}
	passed := sumCall(t, &fact, wattSum("7", 1, "unit.watt"), nil, nil)
	if passed.definition.Version != "9.9.9" || passed.definition.Pack.Version != "9.9.9" || passed.scope != "evse-1" {
		t.Fatalf("fact definition %+v scope %s", passed.definition, passed.scope)
	}
}

func TestOutputFeedPartialCutKeepsLastKnown(t *testing.T) {
	node := openFeedNode(t, 4781, feedTokenA)
	binding := newFeedBinding(t, t.TempDir(), node)
	ref, page := inverterPage("cut-both", graph.CutComplete, nil, "inv-keep", "inv-drop")
	script := newFeedScript(ref)
	script.setLens(ref, lensHierarchy, page)
	script.setLens(ref, lensComposition, page)
	run := startFeed(t, binding, true, script)
	keep := waitAddress(t, binding, "inv-keep")
	drop := waitAddress(t, binding, "inv-drop")

	partialRef, partialPage := inverterPage("cut-partial", graph.CutPartial, []graph.NotIncludedAsset{
		{AssetID: "asset-inv-drop", Reason: graph.NotIncludedSourceUnavailable},
		{AssetID: "asset-inv-hidden", Reason: graph.NotIncludedSourceUnavailable},
	}, "inv-keep", "inv-hidden")
	pushPage(script, partialRef, partialPage)
	waitUntil(t, func() bool {
		return omissionHas(run.output.Reason().Omissions, "asset-inv-drop")
	})
	if !gapReason(run.output.Reason(), "asset-inv-drop", "source_unavailable") || withdrawn(run.output.Reason(), "inv-drop") || binding.EntityAddress("inv-hidden") != "" || binding.EntityAddress("inv-keep") != keep || binding.EntityAddress("inv-drop") != drop {
		t.Fatalf("partial cut changed last known state: %+v hidden %q", run.output.Reason(), binding.EntityAddress("inv-hidden"))
	}
	if run.output.Reason().Published != 2 {
		t.Fatalf("published %d", run.output.Reason().Published)
	}

	mismatch := stagePage("cut-mismatch", graph.CutComplete, "asset-mismatch", "inv-keep", "inv-drop")
	mismatch.page.Cut.Completeness = graph.CutPartial
	script.setLens(mismatch.ref, lensHierarchy, mismatch.page)
	script.setLens(mismatch.ref, lensComposition, mismatch.page)
	script.push(semdriver.SemanticCut{Ref: mismatch.ref})
	waitUntil(t, func() bool { return omissionHas(run.output.Reason().Omissions, "asset-mismatch") })
	if run.output.Reason().Note != "lens read failed" || binding.EntityAddress("inv-keep") != keep || run.output.Reason().Published != 2 || withdrawn(run.output.Reason(), "inv-drop") {
		t.Fatalf("mismatch %+v", run.output.Reason())
	}

	empty := stagePage("cut-empty", "", "asset-empty", "inv-keep", "inv-drop")
	script.setLens(empty.ref, lensHierarchy, empty.page)
	script.setLens(empty.ref, lensComposition, empty.page)
	script.push(semdriver.SemanticCut{Ref: empty.ref})
	waitUntil(t, func() bool { return omissionHas(run.output.Reason().Omissions, "asset-empty") })
	if binding.EntityAddress("inv-drop") != drop || run.output.Reason().Published != 2 {
		t.Fatalf("empty completeness %+v", run.output.Reason())
	}

	repeatedRef := cutRef("cut-repeated", graph.CutComplete)
	token := "again"
	repeated := graph.LensResult{Cut: repeatedRef, Resources: []graph.LensResource{lensResource("inv-keep", "resource.inverter", "asset-inv-keep")}, NextPage: &token}
	again := repeated
	script.setLens(repeatedRef, lensHierarchy, repeated, again)
	script.setLens(repeatedRef, lensComposition, graph.LensResult{Cut: repeatedRef, Resources: repeated.Resources})
	before := script.readsCount()
	script.push(semdriver.SemanticCut{Ref: repeatedRef})
	waitUntil(t, func() bool {
		reason := run.output.Reason()
		return script.readsCount() > before && reason.Note == "lens read failed" && len(reason.Omissions) == 0
	})
	if binding.EntityAddress("inv-keep") != keep || binding.EntityAddress("inv-drop") != drop || run.output.Reason().Published != 2 {
		t.Fatalf("repeated page withdrew %+v", run.output.Reason())
	}

	goneRef, gonePage := inverterPage("cut-gone", graph.CutComplete, nil, "inv-keep")
	pushPage(script, goneRef, gonePage)
	waitUntil(t, func() bool { return withdrawn(run.output.Reason(), "inv-drop") })
	if binding.EntityAddress("inv-drop") != drop || binding.EntityAddress("inv-keep") != keep {
		t.Fatal("withdrawal removed the stored address")
	}
}

func oneGap(asset string, reason graph.NotIncludedReason) []graph.NotIncludedAsset {
	return []graph.NotIncludedAsset{{AssetID: semreg.AssetID(asset), Reason: reason}}
}

func readSum(t *testing.T, fact *graph.LensFact, sum graph.MemberSum, sumErr error, gaps []graph.NotIncludedAsset) *eebusruntime.LensMemberSum {
	t.Helper()
	prepared, _ := prepareSum(t, fact, sum, sumErr, gaps)
	result, ok := resultByID(prepared.results, "evse-1")
	if !ok || result.MemberSum == nil {
		t.Fatal("member sum missing")
	}
	return result.MemberSum
}

func sumCall(t *testing.T, fact *graph.LensFact, sum graph.MemberSum, sumErr error, gaps []graph.NotIncludedAsset) recordedSum {
	t.Helper()
	_, script := prepareSum(t, fact, sum, sumErr, gaps)
	calls := script.Sums()
	if len(calls) != 1 {
		t.Fatalf("sum calls %d", len(calls))
	}
	return calls[0]
}

func prepareSum(t *testing.T, fact *graph.LensFact, sum graph.MemberSum, sumErr error, gaps []graph.NotIncludedAsset) (preparedCut, *feedScript) {
	t.Helper()
	ref := cutRef("cut-sum", graph.CutComplete)
	topology := graph.LensResult{Cut: ref, Resources: []graph.LensResource{lensResource("evse-1", "resource.evse", "asset-evse")}}
	energyRef := ref
	energyRef.NotIncluded = gaps
	energy := graph.LensResult{Cut: energyRef}
	if fact != nil {
		energy.Facts = []graph.LensFact{*fact}
		energy.Resources = []graph.LensResource{lensResource("phase-a", "resource.phase", "asset-phase")}
		energy.Relations = []graph.LensRelation{namedRelation("evse.relation.connector_phase", "evse-1", "phase-a", "asset-rel-phase")}
	}
	script := newFeedScript(ref)
	script.setLens(ref, lensHierarchy, topology)
	script.setLens(ref, lensComposition, topology)
	script.setLens(ref, lensEnergy, energy)
	script.setSum(ref, "evse-1", sum, sumErr)
	return mustPrepare(t, script, ref, true), script
}

func inverterPage(id string, completeness graph.Completeness, gaps []graph.NotIncludedAsset, ids ...string) (graph.CutRef, graph.LensResult) {
	ref := cutRef(id, completeness)
	ref.NotIncluded = gaps
	resources := make([]graph.LensResource, 0, len(ids))
	for _, resourceID := range ids {
		resources = append(resources, lensResource(resourceID, "resource.inverter", "asset-"+resourceID))
	}
	return ref, graph.LensResult{Cut: ref, Resources: resources}
}

func pushPage(script *feedScript, ref graph.CutRef, page graph.LensResult) {
	script.setLens(ref, lensHierarchy, page)
	script.setLens(ref, lensComposition, page)
	script.push(semdriver.SemanticCut{Ref: ref})
}

type stagedPage struct {
	ref  graph.CutRef
	page graph.LensResult
}

func stagePage(id string, completeness graph.Completeness, asset string, ids ...string) stagedPage {
	ref, page := inverterPage(id, completeness, oneGap(asset, graph.NotIncludedSourceUnavailable), ids...)
	return stagedPage{ref: ref, page: page}
}

func gapReason(reason Reason, asset, want string) bool {
	for _, gap := range reason.Omissions {
		if gap.Asset == asset && gap.Reason == want {
			return true
		}
	}
	return false
}

func withdrawn(reason Reason, id string) bool {
	line, ok := lineByID(reason, id)
	return ok && line.Withdrawn && hasText(line.Losses, "withdrawn")
}
