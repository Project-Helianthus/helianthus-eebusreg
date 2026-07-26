package eebusruntime

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestIssue79RawSnapshotExposesLocalSKIAndRedactedProjectionDoesNot(t *testing.T) {
	const localSKI = "1111111111111111111111111111111111111111"

	raw := rawSnapshotV1(t, false)
	encodedRaw, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	var rawDocument map[string]any
	if err := json.Unmarshal(encodedRaw, &rawDocument); err != nil {
		t.Fatal(err)
	}
	meta, ok := rawDocument["meta"].(map[string]any)
	if !ok {
		t.Fatalf("raw meta type = %T", rawDocument["meta"])
	}
	if got, ok := meta["local_ski"].(string); !ok || got != localSKI {
		t.Fatalf("raw local_ski = %#v, want inspectable %q", meta["local_ski"], localSKI)
	}

	redacted, err := BuildRedactedSnapshotV1(raw)
	if err != nil {
		t.Fatal(err)
	}
	encodedRedacted, err := json.Marshal(redacted)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedRedacted), localSKI) {
		t.Fatal("redacted projection leaked the raw local SKI")
	}
}
