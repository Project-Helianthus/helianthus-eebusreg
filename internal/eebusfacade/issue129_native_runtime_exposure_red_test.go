package eebusfacade

import (
	"encoding/json"
	"testing"
	"time"
)

func TestIssue129RuntimeSnapshotKeepsNativeRuntimeIdentityWithoutMaskTier(t *testing.T) {
	const (
		runtimeID = "native-runtime-id"
		localSKI  = "3333333333333333333333333333333333333333"
	)
	payload, err := marshalRuntimeSnapshotWithIdentity(runtimeID, localSKI, nil, time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatal(err)
	}
	meta, ok := document["meta"].(map[string]any)
	if !ok {
		t.Fatalf("meta = %#v, want object", document["meta"])
	}
	if got, ok := meta["runtime"].(string); !ok || got != runtimeID {
		t.Fatalf("runtime = %#v, want native %q", meta["runtime"], runtimeID)
	}
	if _, found := meta["mask_tier"]; found {
		t.Fatalf("runtime snapshot retains retired mask_tier: %#v", meta)
	}
}
