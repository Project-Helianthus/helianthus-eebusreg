package main

import "testing"

func TestIssue93PublicLabProfileSurfaceIsAdditiveAndMethodSetsStayFrozen(t *testing.T) {
	doc, err := extract(moduleRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	packages := make(map[string]surface, len(doc.Packages))
	for _, pkg := range doc.Packages {
		packages[pkg.Path] = pkg
	}
	root := issue85SymbolsByName(packages[modulePath].Symbols)
	raw := issue85SymbolsByName(packages[modulePath+"/eebusraw"].Symbols)

	const runtimeSignature = "type Runtime interface{ PairingState() ([]PairingObservationV1, error); RawFeatureRuntimeV1; Shutdown() error; Snapshot() (SnapshotV1, error); Start(context.Context) error }"
	if got := root["Runtime"].Signature; got != runtimeSignature {
		t.Fatalf("Runtime signature = %q, want unchanged %q", got, runtimeSignature)
	}
	const readRuntimeSignature = "type RawFeatureRuntimeV1 interface{ FeaturesDataGet(context.Context, eebusraw.ReadAuthorizationV1, eebusraw.FeatureDataGetRequestV1) (eebusraw.FeatureDataGetDataV1, *eebusraw.ErrorV1); FeaturesGet(context.Context, eebusraw.ReadAuthorizationV1, eebusraw.FeaturesGetRequestV1) (eebusraw.FeaturesGetDataV1, *eebusraw.ErrorV1) }"
	if got := root["RawFeatureRuntimeV1"].Signature; got != readRuntimeSignature {
		t.Fatalf("RawFeatureRuntimeV1 signature = %q, want unchanged %q", got, readRuntimeSignature)
	}
	const configSignature = `type Config struct{ Enabled bool; StateRoot string; Interface string; ListenAddress netip.AddrPort; DiscoveryEnabled bool; Remotes []Remote; PairingPolicy PairingPolicy; MutationLabProfiles []eebusraw.MutationLabProfileV1 }`
	if got := root["Config"].Signature; got != configSignature {
		t.Fatalf("Config signature = %q, want %q", got, configSignature)
	}
	const profileSignature = `type MutationLabProfileV1 struct{ Contract string "json:\"contract\""; ProfileID string "json:\"profile_id\""; Target FeatureTargetV1 "json:\"target\""; AllowedValueHashes []HashV1 "json:\"allowed_value_hashes\""; RollbackValueHash HashV1 "json:\"rollback_value_hash\""; MaximumProbeTTLSeconds uint64 "json:\"maximum_probe_ttl_seconds\""; SafetyPredicates []string "json:\"safety_predicates\""; EvidenceHashes []HashV1 "json:\"evidence_hashes\""; ExpiresAt time.Time "json:\"expires_at\"" }`
	if got := raw["MutationLabProfileV1"]; got.Signature != profileSignature ||
		got.TypeForm != "defined" {
		t.Fatalf("MutationLabProfileV1 = signature %q form %q, want defined %q", got.Signature, got.TypeForm, profileSignature)
	}
	const validatorSignature = "func ValidateMutationLabProfileV1(MutationLabProfileV1) *ErrorV1"
	if got := raw["ValidateMutationLabProfileV1"].Signature; got != validatorSignature {
		t.Fatalf("ValidateMutationLabProfileV1 signature = %q, want %q", got, validatorSignature)
	}
}
