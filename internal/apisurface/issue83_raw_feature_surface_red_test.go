package main

import (
	"sort"
	"strings"
	"testing"
)

func TestIssue83PublicRawFeatureSurfaceIsFirstPartyAndReadOnly(t *testing.T) {
	// Public type/import closure has no runtime behavior, so AST/type extraction is
	// the narrow source-shape check required to prove dependency independence.
	doc, err := extract(moduleRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	required := map[string]map[string]bool{
		modulePath: {
			"RawFeatureRuntimeV1": false,
			"Runtime":             false,
		},
		modulePath + "/eebusraw": {
			"ChangeabilityV1":         false,
			"ConstraintSetV1":         false,
			"ErrorV1":                 false,
			"FeatureDataGetDataV1":    false,
			"FeatureDataGetRequestV1": false,
			"FeatureLocatorV1":        false,
			"FeatureTargetV1":         false,
			"FeaturesGetDataV1":       false,
			"FeaturesGetRequestV1":    false,
			"FunctionDescriptorV1":    false,
			"ProtocolMessageV1":       false,
			"ReadAuthorizationV1":     false,
			"ReadObservationV1":       false,
			"ReadTokenV1":             false,
			"RuntimeBindingV1":        false,
			"TypedValueV1":            false,
		},
	}
	for _, pkg := range doc.Packages {
		expected, relevant := required[pkg.Path]
		if !relevant {
			continue
		}
		for _, imported := range pkg.Imports {
			for _, forbidden := range []string{
				"github.com/enbility/",
				"github.com/Project-Helianthus/helianthus-eebus-go",
				"github.com/Project-Helianthus/helianthus-ship-go",
				"github.com/Project-Helianthus/helianthus-spine-go",
			} {
				if imported.Path == forbidden || strings.HasPrefix(imported.Path, forbidden+"/") {
					t.Errorf("%s imports protocol implementation package %q", pkg.Path, imported.Path)
				}
			}
		}
		for _, symbol := range pkg.Symbols {
			if _, ok := expected[symbol.Name]; ok {
				expected[symbol.Name] = true
			}
			for _, forbidden := range []string{
				"github.com/enbility/",
				"github.com/Project-Helianthus/helianthus-eebus-go",
				"github.com/Project-Helianthus/helianthus-ship-go",
				"github.com/Project-Helianthus/helianthus-spine-go",
			} {
				if strings.Contains(symbol.Signature, forbidden) {
					t.Errorf("%s.%s exposes protocol implementation type %q", pkg.Path, symbol.Name, forbidden)
				}
			}
		}
	}
	for path, symbols := range required {
		var missing []string
		for name, found := range symbols {
			if !found {
				missing = append(missing, name)
			}
		}
		sort.Strings(missing)
		if len(missing) != 0 {
			t.Errorf("%s missing issue #83 public symbols: %v", path, missing)
		}
	}
}

func TestIssue83ReadContractStillAddsNoConsumerOrUnversionedSurface(t *testing.T) {
	doc, err := extract(moduleRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	forbidden := map[string]struct{}{
		"CandidateRef":          {},
		"MutationCoordinator":   {},
		"RawFeatureRuntimeV2":   {},
		"RawMutationRuntimeV2":  {},
		"RawReadTokenIssuer":    {},
		"ReadTokenSigningKey":   {},
		"TokenIssuerV1":         {},
		"TokenSigningKeyV1":     {},
		"WriteAuthorizationV2":  {},
		"MutationCoordinatorV1": {},
	}
	for _, pkg := range doc.Packages {
		if pkg.Path != modulePath && pkg.Path != modulePath+"/eebusraw" {
			continue
		}
		for _, symbol := range pkg.Symbols {
			if _, denied := forbidden[symbol.Name]; denied {
				t.Errorf("%s exposes excluded issue #83 symbol %s", pkg.Path, symbol.Name)
			}
			lower := strings.ToLower(symbol.Name)
			for _, fragment := range []string{
				"graphql",
				"portal",
				"homeassistant",
				"selector",
				"filterdelete",
				"invoke",
				"signingkey",
				"tokenissuer",
			} {
				if strings.Contains(lower, fragment) {
					t.Errorf("%s exposes excluded issue #83 symbol %s", pkg.Path, symbol.Name)
				}
			}
		}
	}
}
