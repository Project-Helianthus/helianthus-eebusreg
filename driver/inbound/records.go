package inbound

import (
	"strconv"

	semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"
	"github.com/Project-Helianthus/helianthus-semreg/semreg/v1/driver"
)

func resourceRecord(scope Scope, node placedNode) (semreg.Resource, error) {
	proof, err := proofFor(node.slot, node.kind)
	if err != nil {
		return semreg.Resource{}, err
	}
	source, epoch, binding := deviceBinding(scope, node.device)
	slot := node.slot
	return semreg.Resource{
		ResourceID: scope.nodeID(node),
		Kind:       semreg.DefinitionID(node.kind),
		Anchor: semreg.ResourceAnchor{
			Class: semreg.AnchorFunction, Basis: semreg.AnchorNativeSlot, Slot: &slot, Evidence: []semreg.EvidenceRef{proof},
		},
		State:    semreg.ResourceCurrent,
		Origin:   nativeOrigin(semreg.OriginID(opaqueID("origin.", node.slot)), source, epoch, binding, proof),
		Revision: "1",
	}, nil
}

func relationRecord(scope Scope, link builtLink, from, to semreg.ResourceID) (semreg.RelationInstance, error) {
	proof, err := proofFor(link.from+"\n"+link.to, link.relation)
	if err != nil {
		return semreg.RelationInstance{}, err
	}
	source, epoch, binding := deviceBinding(scope, deviceFromSlot(link.from))
	return semreg.RelationInstance{
		Definition: thermalRef(link.relation), From: from, To: to, State: semreg.LinkQualified,
		BasisClass: semreg.RelationNativeObservation, Basis: []semreg.EvidenceRef{proof},
		Origin:   nativeOrigin(semreg.OriginID(opaqueID("origin.", link.relation, link.from, link.to)), source, epoch, binding, proof),
		Revision: "1",
	}, nil
}

func deviceFromSlot(slot string) string {
	const prefix = "device/"
	if len(slot) <= len(prefix) || slot[:len(prefix)] != prefix {
		return ""
	}
	rest := slot[len(prefix):]
	for i := 0; i < len(rest); i++ {
		if rest[i] == '/' {
			return rest[:i]
		}
	}
	return rest
}

func nativeOrigin(id semreg.OriginID, source semreg.SourceID, epoch semreg.SourceEpochID, binding semreg.NativeBindingID, proof semreg.EvidenceRef) semreg.OriginRef {
	return semreg.OriginRef{
		OriginID: id, Kind: semreg.OriginNativeObservation, SourceID: &source, SourceEpochID: &epoch, BindingID: &binding,
		Evidence: []semreg.EvidenceRef{proof},
	}
}

func sampleOf(scope Scope, fact placedFact, source driver.Source) (driver.Sample, error) {
	proof, err := proofFor(fact.native, fact.unit)
	if err != nil {
		return driver.Sample{}, err
	}
	_, epoch, binding := deviceBinding(scope, fact.device)
	generation := semreg.Uint64(strconv.FormatUint(fact.generation, 10))
	point := timePoint(fact.observed)
	var phenomenon, sourceAt *semreg.TimePoint
	if fact.observed != 0 {
		seen := timePoint(fact.observed)
		phenomenon = &seen
		sourced := timePoint(fact.observed)
		sourceAt = &sourced
	}
	value := factValue(fact)
	if fact.availability == semreg.AvailabilityWithdrawn {
		value = nil
	}
	candidate := semreg.FactCandidate{
		CandidateID: semreg.CandidateID(opaqueID("candidate.", scope.Installation, scope.Instance, fact.path)),
		Key:         factKey(fact.fact),
		Value:       value,
		Quality: semreg.Quality{
			Assertion: semreg.AssertionObserved, Qualification: semreg.QualificationQualified, Promotion: semreg.PromotionPromoted,
			Validity: semreg.ValidityGood, Availability: fact.availability, Freshness: fact.freshness,
			Reasons: []semreg.DefinitionID{"declared_by_specification"},
		},
		Times: semreg.Times{
			PhenomenonAt: phenomenon, SourceAt: sourceAt, ReceivedAt: point, ReceiptMonotonic: monotonic(fact.observed),
			EvaluatedAt: point, EvaluateMonotonic: monotonic(fact.observed),
		},
		FreshnessPolicy: semreg.FreshnessPolicy{
			PolicyID: "policy.heating", Version: "1.0.0", FreshForNS: "1000000000", RetainForNS: "2000000000", MaxWallUncertaintyNS: "0",
		},
		BindingID: &binding, SourceEpochID: &epoch, DriverGeneration: &generation,
		Origin:   nativeOrigin(semreg.OriginID(opaqueID("origin.", fact.path)), sourceIDOf(scope, fact.device), epoch, binding, proof),
		Evidence: []semreg.EvidenceRef{proof},
		Revision: semreg.Uint64(strconv.FormatUint(fact.revision, 10)),
	}
	return driver.Sample{From: source, Candidate: candidate, Receipt: driver.ReceiptStamp{At: monotonic(fact.observed)}}, nil
}

func sourceIDOf(scope Scope, device string) semreg.SourceID {
	source, _, _ := deviceBinding(scope, device)
	return source
}

func factValue(fact placedFact) *semreg.Value {
	if fact.quantity != nil {
		quantity := *fact.quantity
		return &semreg.Value{Kind: semreg.ValueQuantity, Quantity: &quantity}
	}
	if fact.symbol != nil {
		symbol := *fact.symbol
		return &semreg.Value{Kind: semreg.ValueSymbol, Symbol: &symbol}
	}
	return nil
}

func timePoint(unix int64) semreg.TimePoint {
	return semreg.TimePoint{UnixNanoseconds: semreg.Int64(strconv.FormatInt(unix, 10)), ClockID: "clock.utc", UncertaintyNS: "0"}
}

func monotonic(unix int64) semreg.MonotonicPoint {
	if unix < 0 {
		unix = 0
	}
	return semreg.MonotonicPoint{ClockEpochID: "epoch.observation", Nanoseconds: semreg.Uint64(strconv.FormatInt(unix, 10))}
}
