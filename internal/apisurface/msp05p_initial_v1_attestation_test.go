package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"testing"
)

var msp05pInitialV1Names = map[string]struct{}{
	"Config":              {},
	"New":                 {},
	"PairingPolicy":       {},
	"PairingPolicyClosed": {},
}

var msp05pRemovedV2Names = map[string]struct{}{
	"ConfigV2":              {},
	"NewV2":                 {},
	"PairingPolicyV2":       {},
	"PairingPolicyV2Closed": {},
}

func TestMSP05PPublicAPIAttestationIsInitialV1(t *testing.T) {
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
	}
	got := make(map[string]string, len(want))
	for _, symbol := range root.Symbols {
		if _, initialV1 := msp05pInitialV1Names[symbol.Name]; initialV1 {
			got[symbol.Name] = symbol.Signature
		}
		if _, removed := msp05pRemovedV2Names[symbol.Name]; removed {
			t.Errorf("removed pre-release V2 symbol remains public: %s", symbol.Name)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("MSP-05P initial v1 symbols = %v, want exactly %v", sortedMSP05PKeys(got), sortedMSP05PKeys(want))
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
	if len(payload) != 95_480 {
		t.Fatalf("projected v1 API bytes = %d, want historical frozen 95480", len(payload))
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

	issue77Symbols := map[string]struct{}{
		"BuildRedactedSnapshotV1": {},
		"DeviceV1":                {},
		"EntityV1":                {},
		"FeatureV1":               {},
		"MetadataV1":              {},
		"MetadataValueV1":         {},
		"OpaqueObservationV1":     {},
		"OpaqueScalarV1":          {},
		"OpaqueValueV1":           {},
		"PairingObservationV1":    {},
		"RedactedDeviceV1":        {},
		"RedactedEntityV1":        {},
		"RedactedFeatureV1":       {},
		"RedactedServiceV1":       {},
		"RedactedSessionV1":       {},
		"RedactedSnapshotV1":      {},
		"RedactedSnapshotMetaV1":  {},
		"RedactedUseCaseV1":       {},
		"ServiceV1":               {},
		"SessionV1":               {},
		"SnapshotV1":              {},
		"UseCaseV1":               {},
	}
	symbols := root.Symbols[:0]
	for _, symbol := range root.Symbols {
		if symbol.Name == "PairingPolicy" || symbol.Name == "PairingPolicyClosed" {
			continue
		}
		if _, issue77 := issue77Symbols[symbol.Name]; issue77 {
			if symbol.Kind != "method" || symbol.Receiver == nil ||
				symbol.Receiver.Base != "SnapshotV1" {
				continue
			}
		}
		if symbol.Receiver != nil {
			if _, issue77 := issue77Symbols[symbol.Receiver.Base]; issue77 &&
				symbol.Receiver.Base != "SnapshotV1" {
				continue
			}
		}
		if symbol.Name == "Config" {
			symbol.Type = "struct{ Enabled bool; StateRoot string; Interface string; ListenPort int; Remotes []Remote }"
			symbol.Signature = "type Config struct{ Enabled bool; StateRoot string; Interface string; ListenPort int; Remotes []Remote }"
		}
		if symbol.Name == "Remote" {
			symbol.Type = "struct{ SKI string }"
			symbol.Signature = "type Remote struct{ SKI string }"
		}
		if symbol.Name == "SnapshotMetaV1" {
			symbol.Type = `struct{ Contract string "json:\"contract\""; Runtime eebusraw.RedactedID "json:\"runtime\""; LocalSKI eebusraw.RedactedID "json:\"local_ski\""; MaskTier eebusraw.MaskTier "json:\"mask_tier\""; CapturedAt time.Time "json:\"captured_at\""; DataTimestamp time.Time "json:\"data_timestamp\""; DataHash string "json:\"data_hash,omitempty\"" }`
			symbol.Signature = "type SnapshotMetaV1 " + symbol.Type
		}
		symbols = append(symbols, symbol)
	}
	var historicalIssue77 []symbol
	if err := json.Unmarshal([]byte(msp05pHistoricalPreIssue77Symbols), &historicalIssue77); err != nil {
		t.Fatal(err)
	}
	symbols = append(symbols, historicalIssue77...)
	sort.Slice(symbols, func(left, right int) bool {
		return symbolKey(symbols[left]) < symbolKey(symbols[right])
	})
	root.Symbols = symbols

	imports := root.Imports[:0]
	hasEvidence := false
	for _, imported := range root.Imports {
		if imported.Path != "net/netip" {
			imports = append(imports, imported)
		}
		hasEvidence = hasEvidence || imported.Path == modulePath+"/eebusevidence"
	}
	if !hasEvidence {
		imports = append(imports, importSurface{
			DependencyKind: "public_contract",
			Qualifier:      "eebusevidence",
			Path:           modulePath + "/eebusevidence",
		})
	}
	sort.Slice(imports, func(left, right int) bool {
		return imports[left].Qualifier+"\x00"+imports[left].Path <
			imports[right].Qualifier+"\x00"+imports[right].Path
	})
	root.Imports = imports
	return projected
}

// This is the pre-issue77 raw snapshot surface. The issue77 contract is tested
// independently; projecting it away keeps MSP-04B/MSP-045 historical evidence immutable.
const msp05pHistoricalPreIssue77Symbols = `[
{"kind":"const","name":"FeatureRoleV1Special","type":"FeatureRoleV1","signature":"const FeatureRoleV1Special FeatureRoleV1 = \"special\"","value_kind":"string","value":"\"special\""},
{"kind":"type","name":"DeviceV1","type":"struct{ ID eebusraw.RedactedID \"json:\\\"id\\\"\"; Entities []EntityV1 \"json:\\\"entities,omitempty\\\"\"; UseCaseClaims []UseCaseClaimV1 \"json:\\\"usecase_claims,omitempty\\\"\"; Raw []eebusevidence.ObjectV1 \"json:\\\"raw,omitempty\\\"\"; Unknown []eebusraw.UnknownField \"json:\\\"unknown,omitempty\\\"\" }","signature":"type DeviceV1 struct{ ID eebusraw.RedactedID \"json:\\\"id\\\"\"; Entities []EntityV1 \"json:\\\"entities,omitempty\\\"\"; UseCaseClaims []UseCaseClaimV1 \"json:\\\"usecase_claims,omitempty\\\"\"; Raw []eebusevidence.ObjectV1 \"json:\\\"raw,omitempty\\\"\"; Unknown []eebusraw.UnknownField \"json:\\\"unknown,omitempty\\\"\" }","type_form":"defined","type_parameters":[]},
{"kind":"type","name":"EntityV1","type":"struct{ ID eebusraw.RedactedID \"json:\\\"id\\\"\"; Features []FeatureV1 \"json:\\\"features,omitempty\\\"\"; Raw []eebusevidence.ObjectV1 \"json:\\\"raw,omitempty\\\"\"; Unknown []eebusraw.UnknownField \"json:\\\"unknown,omitempty\\\"\" }","signature":"type EntityV1 struct{ ID eebusraw.RedactedID \"json:\\\"id\\\"\"; Features []FeatureV1 \"json:\\\"features,omitempty\\\"\"; Raw []eebusevidence.ObjectV1 \"json:\\\"raw,omitempty\\\"\"; Unknown []eebusraw.UnknownField \"json:\\\"unknown,omitempty\\\"\" }","type_form":"defined","type_parameters":[]},
{"kind":"type","name":"FeatureV1","type":"struct{ ID eebusraw.RedactedID \"json:\\\"id\\\"\"; Role FeatureRoleV1 \"json:\\\"role\\\"\"; Raw []eebusevidence.ObjectV1 \"json:\\\"raw,omitempty\\\"\"; Unknown []eebusraw.UnknownField \"json:\\\"unknown,omitempty\\\"\" }","signature":"type FeatureV1 struct{ ID eebusraw.RedactedID \"json:\\\"id\\\"\"; Role FeatureRoleV1 \"json:\\\"role\\\"\"; Raw []eebusevidence.ObjectV1 \"json:\\\"raw,omitempty\\\"\"; Unknown []eebusraw.UnknownField \"json:\\\"unknown,omitempty\\\"\" }","type_form":"defined","type_parameters":[]},
{"kind":"type","name":"PairingObservationV1","type":"struct{ Remote eebusraw.RedactedID \"json:\\\"remote\\\"\"; State eebusraw.PairingState \"json:\\\"state\\\"\"; Since time.Time \"json:\\\"since,omitempty\\\"\"; Raw []eebusevidence.ObjectV1 \"json:\\\"raw,omitempty\\\"\"; Unknown []eebusraw.UnknownField \"json:\\\"unknown,omitempty\\\"\" }","signature":"type PairingObservationV1 struct{ Remote eebusraw.RedactedID \"json:\\\"remote\\\"\"; State eebusraw.PairingState \"json:\\\"state\\\"\"; Since time.Time \"json:\\\"since,omitempty\\\"\"; Raw []eebusevidence.ObjectV1 \"json:\\\"raw,omitempty\\\"\"; Unknown []eebusraw.UnknownField \"json:\\\"unknown,omitempty\\\"\" }","type_form":"defined","type_parameters":[]},
{"kind":"type","name":"ServiceV1","type":"struct{ ID eebusraw.RedactedID \"json:\\\"id\\\"\"; Kind ServiceKindV1 \"json:\\\"kind\\\"\"; Visible bool \"json:\\\"visible\\\"\"; Paired bool \"json:\\\"paired\\\"\"; Raw []eebusevidence.ObjectV1 \"json:\\\"raw,omitempty\\\"\"; Unknown []eebusraw.UnknownField \"json:\\\"unknown,omitempty\\\"\" }","signature":"type ServiceV1 struct{ ID eebusraw.RedactedID \"json:\\\"id\\\"\"; Kind ServiceKindV1 \"json:\\\"kind\\\"\"; Visible bool \"json:\\\"visible\\\"\"; Paired bool \"json:\\\"paired\\\"\"; Raw []eebusevidence.ObjectV1 \"json:\\\"raw,omitempty\\\"\"; Unknown []eebusraw.UnknownField \"json:\\\"unknown,omitempty\\\"\" }","type_form":"defined","type_parameters":[]},
{"kind":"type","name":"SessionV1","type":"struct{ ID eebusraw.RedactedID \"json:\\\"id\\\"\"; Remote eebusraw.RedactedID \"json:\\\"remote\\\"\"; State ObservedSessionStateV1 \"json:\\\"state\\\"\"; Since time.Time \"json:\\\"since,omitempty\\\"\"; Raw []eebusevidence.ObjectV1 \"json:\\\"raw,omitempty\\\"\"; Unknown []eebusraw.UnknownField \"json:\\\"unknown,omitempty\\\"\" }","signature":"type SessionV1 struct{ ID eebusraw.RedactedID \"json:\\\"id\\\"\"; Remote eebusraw.RedactedID \"json:\\\"remote\\\"\"; State ObservedSessionStateV1 \"json:\\\"state\\\"\"; Since time.Time \"json:\\\"since,omitempty\\\"\"; Raw []eebusevidence.ObjectV1 \"json:\\\"raw,omitempty\\\"\"; Unknown []eebusraw.UnknownField \"json:\\\"unknown,omitempty\\\"\" }","type_form":"defined","type_parameters":[]},
{"kind":"type","name":"SnapshotV1","type":"struct{ Meta SnapshotMetaV1 \"json:\\\"meta\\\"\"; Status RuntimeObservationV1 \"json:\\\"status\\\"\"; Pairing []PairingObservationV1 \"json:\\\"pairing,omitempty\\\"\"; Services []ServiceV1 \"json:\\\"services,omitempty\\\"\"; Sessions []SessionV1 \"json:\\\"sessions,omitempty\\\"\"; Topology TopologyV1 \"json:\\\"topology\\\"\"; Raw []eebusevidence.ObjectV1 \"json:\\\"raw,omitempty\\\"\" }","signature":"type SnapshotV1 struct{ Meta SnapshotMetaV1 \"json:\\\"meta\\\"\"; Status RuntimeObservationV1 \"json:\\\"status\\\"\"; Pairing []PairingObservationV1 \"json:\\\"pairing,omitempty\\\"\"; Services []ServiceV1 \"json:\\\"services,omitempty\\\"\"; Sessions []SessionV1 \"json:\\\"sessions,omitempty\\\"\"; Topology TopologyV1 \"json:\\\"topology\\\"\"; Raw []eebusevidence.ObjectV1 \"json:\\\"raw,omitempty\\\"\" }","type_form":"defined","type_parameters":[]},
{"kind":"type","name":"TopologyV1","type":"struct{ Devices []DeviceV1 \"json:\\\"devices,omitempty\\\"\" }","signature":"type TopologyV1 struct{ Devices []DeviceV1 \"json:\\\"devices,omitempty\\\"\" }","type_form":"defined","type_parameters":[]},
{"kind":"type","name":"UseCaseClaimV1","type":"struct{ ID eebusraw.RedactedID \"json:\\\"id\\\"\"; Raw []eebusevidence.ObjectV1 \"json:\\\"raw,omitempty\\\"\"; Unknown []eebusraw.UnknownField \"json:\\\"unknown,omitempty\\\"\" }","signature":"type UseCaseClaimV1 struct{ ID eebusraw.RedactedID \"json:\\\"id\\\"\"; Raw []eebusevidence.ObjectV1 \"json:\\\"raw,omitempty\\\"\"; Unknown []eebusraw.UnknownField \"json:\\\"unknown,omitempty\\\"\" }","type_form":"defined","type_parameters":[]}
]`

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
