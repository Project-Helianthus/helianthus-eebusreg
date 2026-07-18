package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"testing"
)

var msp05pAdditionNames = map[string]struct{}{
	"ConfigV2":              {},
	"NewV2":                 {},
	"PairingPolicyV2":       {},
	"PairingPolicyV2Closed": {},
}

func TestMSP05PPublicAPIAttestationIsExactlyFourAdditions(t *testing.T) {
	doc, err := extract(moduleRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	root := msp05pRootSurface(t, doc)
	want := map[string]string{
		"ConfigV2":              "type ConfigV2 struct{ Enabled bool; StateRoot string; Interface string; ListenAddress netip.AddrPort; DiscoveryEnabled bool; Remotes []Remote; PairingPolicy PairingPolicyV2 }",
		"NewV2":                 "func NewV2(ConfigV2) (Runtime, error)",
		"PairingPolicyV2":       "type PairingPolicyV2 string",
		"PairingPolicyV2Closed": `const PairingPolicyV2Closed PairingPolicyV2 = "closed"`,
	}
	got := make(map[string]string, len(want))
	for _, symbol := range root.Symbols {
		if _, additive := msp05pAdditionNames[symbol.Name]; additive {
			got[symbol.Name] = symbol.Signature
		}
	}
	if len(got) != len(want) {
		t.Fatalf("MSP-05P public additions = %v, want exactly %v", sortedMSP05PKeys(got), sortedMSP05PKeys(want))
	}
	for name, signature := range want {
		if got[name] != signature {
			t.Errorf("%s signature = %q, want %q", name, got[name], signature)
		}
	}

	wantStdlib := map[string]string{
		"context":   "context",
		"fmt":       "fmt",
		"net/netip": "netip",
		"time":      "time",
	}
	gotStdlib := map[string]string{}
	for _, imported := range root.Imports {
		if imported.DependencyKind == "standard_library" {
			gotStdlib[imported.Path] = imported.Qualifier
		}
	}
	if len(gotStdlib) != len(wantStdlib) {
		t.Fatalf("root standard-library public dependencies = %v, want %v", gotStdlib, wantStdlib)
	}
	for path, qualifier := range wantStdlib {
		if gotStdlib[path] != qualifier {
			t.Errorf("standard-library dependency %q qualifier = %q, want %q", path, gotStdlib[path], qualifier)
		}
	}

	projected := msp05pProjectFrozenV1(t, doc)
	payload, err := json.MarshalIndent(projected, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	payload = append(payload, '\n')
	if len(payload) != 95_207 {
		t.Fatalf("projected v1 API bytes = %d, want frozen 95207", len(payload))
	}
	digest := sha256.Sum256(payload)
	if got := hex.EncodeToString(digest[:]); got != msp04bFrozenPublicAPIHash {
		t.Fatalf("projected v1 API SHA-256 = %s, want %s", got, msp04bFrozenPublicAPIHash)
	}
}

func msp05pProjectFrozenV1(t *testing.T, source document) document {
	t.Helper()
	payload, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var projected document
	if err := json.Unmarshal(payload, &projected); err != nil {
		t.Fatal(err)
	}
	root := msp05pRootSurface(t, projected)

	symbols := root.Symbols[:0]
	for _, symbol := range root.Symbols {
		if _, additive := msp05pAdditionNames[symbol.Name]; !additive {
			symbols = append(symbols, symbol)
		}
	}
	root.Symbols = symbols

	imports := root.Imports[:0]
	for _, imported := range root.Imports {
		if imported.Path != "net/netip" {
			imports = append(imports, imported)
		}
	}
	root.Imports = imports
	return projected
}

func msp05pRootSurface(t *testing.T, doc document) *surface {
	t.Helper()
	for index := range doc.Packages {
		if doc.Packages[index].Path == modulePath {
			return &doc.Packages[index]
		}
	}
	t.Fatal("root public package missing")
	return nil
}

func sortedMSP05PKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
