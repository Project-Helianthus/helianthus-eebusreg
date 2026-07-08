package eebusraw

import (
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusevidence"
)

func TestSnapshotCarriesRawUnknownFields(t *testing.T) {
	ref := eebusevidence.Ref{
		ID:        "ev-001",
		RuntimeID: "rt-001",
		Contract:  "eebus.raw/v0",
		Scope:     "whole-root",
		MaskTier:  "public",
		AuthScope: "read-only",
	}
	observedAt := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	snap := Snapshot{
		RuntimeID: "rt-001",
		LocalSKI:  "local-ski",
		Features: []Feature{{
			Path:       "/entity/0/feature/0",
			EntityPath: "/entity/0",
			UnknownFields: []UnknownField{{
				Path:        "/entity/0/feature/0/vendor-field",
				Encoding:    "opaque",
				ValueHash:   "sha256:abc",
				EvidenceRef: ref,
				ObservedAt:  observedAt,
			}},
		}},
	}

	got := snap.Features[0].UnknownFields[0]
	if got.Path != "/entity/0/feature/0/vendor-field" {
		t.Fatalf("unknown path = %q", got.Path)
	}
	if got.EvidenceRef.ID != "ev-001" {
		t.Fatalf("evidence ref = %q", got.EvidenceRef.ID)
	}
	if !got.ObservedAt.Equal(observedAt) {
		t.Fatalf("observed at = %s", got.ObservedAt)
	}
}
