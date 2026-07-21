package main

import (
	"sort"
	"strings"
	"testing"
)

func TestMSP05PInitialV1ReplacesEveryUnreleasedV2Export(t *testing.T) {
	doc, err := extract(moduleRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	root := msp05pRootSurface(t, doc)

	want := map[string]string{
		"Config":              "type Config struct{ Enabled bool; StateRoot string; Interface string; ListenAddress netip.AddrPort; DiscoveryEnabled bool; Remotes []Remote; PairingPolicy PairingPolicy }",
		"New":                 "func New(Config) (Runtime, error)",
		"PairingPolicy":       "type PairingPolicy string",
		"PairingPolicyClosed": `const PairingPolicyClosed PairingPolicy = "closed"`,
		"Remote":              "type Remote struct{ SKI string }",
	}
	forbidden := map[string]struct{}{
		"ConfigV2":              {},
		"NewV2":                 {},
		"PairingPolicyV2":       {},
		"PairingPolicyV2Closed": {},
	}

	got := make(map[string]string, len(want))
	var leaked []string
	for _, symbol := range root.Symbols {
		if _, ok := want[symbol.Name]; ok {
			got[symbol.Name] = symbol.Signature
		}
		if _, ok := forbidden[symbol.Name]; ok {
			leaked = append(leaked, symbol.Name)
		}
	}
	sort.Strings(leaked)
	if len(leaked) != 0 {
		t.Errorf("unreleased v2 exports remain public: %v", leaked)
	}
	for name, signature := range want {
		if got[name] != signature {
			t.Errorf("%s signature = %q, want %q", name, got[name], signature)
		}
	}
	if strings.Contains(got["Config"], "ListenPort") {
		t.Errorf("initial Config retains legacy port-only field: %q", got["Config"])
	}
}
