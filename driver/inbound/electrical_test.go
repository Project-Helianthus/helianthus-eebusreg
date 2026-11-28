package inbound

import (
	"sort"
	"strings"
	"testing"

	semreg "github.com/Project-Helianthus/helianthus-semreg/semreg/v1"
	"github.com/Project-Helianthus/helianthus-semreg/semreg/v1/driver"
)

func electricalScope(instance string) Scope {
	return Scope{Installation: "installation.synthetic", Instance: instance, Bridge: "bridge.heating"}
}

func measure(field, text, unit, phase string) Reading {
	return Reading{Field: field, Phase: phase, Text: text, Unit: unit, Rank: "producer", Carrier: "notify", Generation: 4, ObservedUnix: 500}
}

func requireQuantity(t *testing.T, fact FactView, coefficient string, exponent int32, unit string) {
	t.Helper()
	if fact.Coefficient != coefficient || fact.Exponent != exponent || fact.Unit != unit {
		t.Fatalf("quantity = %+v", fact)
	}
	if fact.Availability != string(semreg.AvailabilityAvailable) || fact.Freshness != string(semreg.FreshnessFresh) || !fact.Authoritative || fact.Priority != PriorityProducer {
		t.Fatalf("source = %+v", fact)
	}
}

func requireSymbol(t *testing.T, result Result, kind, id, token string) FactView {
	t.Helper()
	for _, fact := range result.Facts {
		if fact.SubjectKind == kind && fact.FactID == id && fact.SymbolToken == token && !fact.Withdrawn {
			if fact.SymbolNamespace != id || !fact.SymbolKnown {
				t.Fatalf("symbol %s = %+v", id, fact)
			}
			return fact
		}
	}
	t.Fatalf("missing symbol %s %s %s", kind, id, token)
	return FactView{}
}

func denyFact(t *testing.T, result Result, id, native string) {
	t.Helper()
	for _, fact := range result.Facts {
		if fact.FactID == id && fact.Native == native && !fact.Withdrawn {
			t.Fatalf("published %s native %s", id, native)
		}
	}
}

func assertNoAccuracy(t *testing.T, result Result) {
	t.Helper()
	for _, fact := range result.Facts {
		if strings.Contains(fact.FactID, "accuracy") {
			t.Fatalf("accuracy fact %s", fact.FactID)
		}
	}
}

func nodeSlots(result Result, kind string) []string {
	var slots []string
	for _, node := range result.Nodes {
		if string(node.Key.Kind) == kind {
			slots = append(slots, node.Key.Anchor.Slot)
		}
	}
	return slots
}

func linkCount(result Result, id string) int {
	count := 0
	for _, link := range result.Links {
		if string(link.Key.Type.ID) == id {
			count++
		}
	}
	return count
}

func memberValue(result Result, local string) (string, bool) {
	for _, member := range result.Members {
		if string(member.Member.Key.Local) == local {
			return member.Value, true
		}
	}
	return "", false
}

func resourcesOf(batch semreg.PublicationBatch, kind string) []semreg.Resource {
	var out []semreg.Resource
	for _, resource := range batch.ResourceUpserts {
		if string(resource.Kind) == kind {
			out = append(out, resource)
		}
	}
	return out
}

func assertParent(t *testing.T, resource semreg.Resource, class semreg.AnchorClass, parent bool) {
	t.Helper()
	if resource.Anchor.Class != class || resource.Anchor.Basis != semreg.AnchorNativeSlot || resource.Anchor.Slot == nil {
		t.Fatalf("anchor = %+v", resource.Anchor)
	}
	if parent != (resource.Anchor.Parent != nil) {
		t.Fatalf("parent presence = %v for %s", resource.Anchor.Parent != nil, resource.Kind)
	}
}

func sortedKeyText(result Result) []string {
	out := make([]string, 0, len(result.Nodes))
	for _, node := range result.Nodes {
		out = append(out, nodeKeyText(node.Key)+"\n"+string(node.ID))
	}
	sort.Strings(out)
	return out
}

func nodeKeyText(key driver.NodeKey) string {
	return string(key.Kind) + "\n" + key.Anchor.Slot + "\n" + string(key.Driver)
}
