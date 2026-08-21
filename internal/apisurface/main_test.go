package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"go/token"
	"go/types"
)

func moduleRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestExtractIsDeterministic(t *testing.T) {
	first, err := extract(moduleRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	second, err := extract(moduleRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatal("same module extracted different API documents")
	}
}

func TestCommandWritesRequestedOutputPath(t *testing.T) {
	output := filepath.Join(t.TempDir(), "api-surface.json")
	command := exec.Command("go", "run", "./internal/apisurface", "-output", output)
	command.Dir = moduleRoot(t)
	command.Env = append(os.Environ(), "GOWORK=off")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("command failed: %v\n%s", err, output)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var doc document
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.SchemaID != schemaID || doc.SchemaVersion != schemaVersion {
		t.Fatalf("unexpected schema identity: %#v", doc)
	}
}

func TestRootLifecycleSignaturesAreExact(t *testing.T) {
	doc, err := extract(moduleRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	root := doc.Packages[0]
	got := map[string]string{}
	for _, symbol := range root.Symbols {
		if symbol.Name == "New" || symbol.Name == "NewOperatorRuntimeV1" || symbol.Name == "Runtime" || symbol.Name == "SnapshotV1" {
			got[symbol.Name] = symbol.Signature
		}
	}
	if got["New"] != "func New(Config) (Runtime, error)" {
		t.Fatalf("New signature = %q", got["New"])
	}
	if got["NewOperatorRuntimeV1"] != "func NewOperatorRuntimeV1(Config) (Runtime, AdminV1, error)" {
		t.Fatalf("NewOperatorRuntimeV1 signature = %q", got["NewOperatorRuntimeV1"])
	}
	wantRuntime := "type Runtime interface{ PairingState() ([]PairingObservationV1, error); RawFeatureRuntimeV1; Shutdown() error; Snapshot() (SnapshotV1, error); Start(context.Context) error }"
	if got["Runtime"] != wantRuntime {
		t.Fatalf("Runtime signature = %q", got["Runtime"])
	}
	if !strings.Contains(got["SnapshotV1"], `json:\"meta\"`) {
		t.Fatalf("SnapshotV1 signature omitted JSON tags: %q", got["SnapshotV1"])
	}
}

func TestOperatorAdminV1RequestResultAndErrorSurfaceIsExact(t *testing.T) {
	doc, err := extract(moduleRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	packages := make(map[string]surface, len(doc.Packages))
	for _, pkg := range doc.Packages {
		packages[pkg.Path] = pkg
	}
	root := issue85SymbolsByName(packages[modulePath].Symbols)

	wantSignatures := map[string]string{
		"AdminV1":                     "type AdminV1 interface{ Cancel(context.Context, CancelRequestV1) (AdminMutationResultV1, *AdminErrorV1); ClosePairingWindow(context.Context, ClosePairingWindowRequestV1) (AdminMutationResultV1, *AdminErrorV1); Confirm(context.Context, ConfirmRequestV1) (AdminMutationResultV1, *AdminErrorV1); Connect(context.Context, ConnectRequestV1) (AdminMutationResultV1, *AdminErrorV1); OpenPairingWindow(context.Context, OpenPairingWindowRequestV1) (AdminMutationResultV1, *AdminErrorV1); RetryTrusted(context.Context, RetryTrustedRequestV1) (AdminMutationResultV1, *AdminErrorV1); Select(context.Context, SelectRequestV1) (AdminSelectionResultV1, *AdminErrorV1); Snapshot(context.Context, AdminSnapshotRequestV1) (AdminSnapshotV1, *AdminErrorV1); Untrust(context.Context, UntrustRequestV1) (AdminMutationResultV1, *AdminErrorV1) }",
		"MutationPreconditionV1":      "type MutationPreconditionV1 struct{ IdempotencyKey string; ExpectedStateRevision uint64 }",
		"AdminSnapshotRequestV1":      "type AdminSnapshotRequestV1 struct{ View AdminViewV1 }",
		"OpenPairingWindowRequestV1":  "type OpenPairingWindowRequestV1 struct{ MutationPreconditionV1; Duration time.Duration }",
		"ClosePairingWindowRequestV1": "type ClosePairingWindowRequestV1 struct{ MutationPreconditionV1 }",
		"SelectRequestV1":             "type SelectRequestV1 struct{ MutationPreconditionV1; Observation ObservationHandleV1; ExpectedSKI string }",
		"ConnectRequestV1":            "type ConnectRequestV1 struct{ MutationPreconditionV1; Selection SelectionHandleV1; PIN []uint8 }",
		"ConfirmRequestV1":            "type ConfirmRequestV1 struct{ MutationPreconditionV1; Candidate CandidateHandleV1; ExpectedSKI string }",
		"CancelRequestV1":             "type CancelRequestV1 struct{ MutationPreconditionV1; Candidate CandidateHandleV1 }",
		"RetryTrustedRequestV1":       "type RetryTrustedRequestV1 struct{ MutationPreconditionV1; Partner PartnerHandleV1 }",
		"UntrustRequestV1":            "type UntrustRequestV1 struct{ MutationPreconditionV1; Partner PartnerHandleV1 }",
		"AdminMutationResultV1":       "type AdminMutationResultV1 struct{ StateRevision uint64; Outcome AdminOutcomeV1; Replayed bool }",
		"AdminSelectionResultV1":      "type AdminSelectionResultV1 struct{ AdminMutationResultV1; Selection SelectionHandleV1 }",
		"AdminOutcomeV1":              "type AdminOutcomeV1 string",
		"AdminViewV1":                 "type AdminViewV1 string",
		"AdminErrorCodeV1":            "type AdminErrorCodeV1 string",
		"AdminErrorV1":                "type AdminErrorV1 struct{ Code AdminErrorCodeV1 }",
	}
	for name, want := range wantSignatures {
		got, exists := root[name]
		if !exists {
			t.Errorf("public operator AdminV1 surface is missing %s", name)
			continue
		}
		if got.Signature != want || got.TypeForm != "defined" {
			t.Errorf("%s = signature %q form %q, want defined %q", name, got.Signature, got.TypeForm, want)
		}
	}
	if got := root["Error"]; got.Signature != "func (AdminErrorV1) Error() string" || got.Receiver == nil || got.Receiver.Base != "AdminErrorV1" {
		t.Errorf("AdminErrorV1.Error = %#v, want typed error method", got)
	}
	if snapshot := root["AdminSnapshotV1"].Signature; !strings.Contains(snapshot, "StateRevision uint64") {
		t.Errorf("AdminSnapshotV1 = %q, want non-zero operator StateRevision field", snapshot)
	}

	wantHandles := []string{"CandidateHandleV1", "ObservationHandleV1", "PartnerHandleV1", "SelectionHandleV1"}
	var gotHandles []string
	for _, item := range packages[modulePath].Symbols {
		if item.Kind == "type" && strings.HasSuffix(item.Name, "HandleV1") {
			gotHandles = append(gotHandles, item.Name)
		}
	}
	sort.Strings(gotHandles)
	if strings.Join(gotHandles, "\n") != strings.Join(wantHandles, "\n") {
		t.Errorf("public AdminV1 handles = %v, want exactly four opaque handles %v", gotHandles, wantHandles)
	}

	for name, value := range operatorAdminV1ClosedConstants() {
		got, exists := root[name]
		if !exists {
			t.Errorf("closed AdminV1 surface is missing %s=%q", name, value)
			continue
		}
		wantType := "AdminErrorCodeV1"
		if strings.HasPrefix(name, "AdminViewV1") {
			wantType = "AdminViewV1"
		}
		if got.Kind != "const" || got.Type != wantType || got.Value != `"`+value+`"` {
			t.Errorf("%s = kind %q type %q value %q, want %s const %q", name, got.Kind, got.Type, got.Value, wantType, value)
		}
	}
}

func operatorAdminV1ClosedConstants() map[string]string {
	return map[string]string{
		"AdminViewV1Trusted":                       "trusted",
		"AdminViewV1Connected":                     "connected",
		"AdminViewV1Discovered":                    "discovered",
		"AdminViewV1Candidate":                     "candidate",
		"AdminErrorCodeV1AdminBoundaryUnavailable": "admin_boundary_unavailable",
		"AdminErrorCodeV1Unauthenticated":          "unauthenticated",
		"AdminErrorCodeV1Forbidden":                "forbidden",
		"AdminErrorCodeV1CSRFRejected":             "csrf_rejected",
		"AdminErrorCodeV1InvalidRequest":           "invalid_request",
		"AdminErrorCodeV1StateConflict":            "state_conflict",
		"AdminErrorCodeV1SnapshotExpired":          "snapshot_expired",
		"AdminErrorCodeV1IdempotencyConflict":      "idempotency_conflict",
		"AdminErrorCodeV1PairingClosed":            "pairing_closed",
		"AdminErrorCodeV1ObservationStale":         "observation_stale",
		"AdminErrorCodeV1IdentityMismatch":         "identity_mismatch",
		"AdminErrorCodeV1AssociationIncomplete":    "association_incomplete",
		"AdminErrorCodeV1CandidateExpired":         "candidate_expired",
		"AdminErrorCodeV1CandidateBusy":            "candidate_busy",
		"AdminErrorCodeV1TrustDenied":              "trust_denied",
		"AdminErrorCodeV1ListenerUnavailable":      "listener_unavailable",
		"AdminErrorCodeV1DiscoveryUnavailable":     "discovery_unavailable",
		"AdminErrorCodeV1AttemptTimeout":           "attempt_timeout",
		"AdminErrorCodeV1Disconnected":             "disconnected",
		"AdminErrorCodeV1BackoffActive":            "backoff_active",
		"AdminErrorCodeV1TerminalQuarantine":       "terminal_quarantine",
		"AdminErrorCodeV1PersistenceFailure":       "persistence_failure",
		"AdminErrorCodeV1UnknownState":             "unknown_state",
	}
}

func TestForbiddenDependencyLeakageIsRejected(t *testing.T) {
	for _, packagePath := range []string{
		"github.com/Project-Helianthus/helianthus-eebus-go/api",
		modulePath + "/internal/private",
		"example.invalid/unapproved",
	} {
		leakPackage := types.NewPackage(packagePath, "leak")
		leak := types.NewNamed(types.NewTypeName(token.NoPos, leakPackage, "Leak", nil), types.Typ[types.String], nil)
		x := extractor{pkg: types.NewPackage(modulePath, "eebusruntime")}
		err := x.checkType(leak, map[types.Type]bool{})
		if err == nil || !strings.Contains(err.Error(), "dependency") {
			t.Fatalf("dependency %q was accepted: %v", packagePath, err)
		}
	}

	approved := types.NewPackage(modulePath+"/eebusraw", "eebusraw")
	hidden := types.NewNamed(types.NewTypeName(token.NoPos, approved, "hidden", nil), types.Typ[types.String], nil)
	x := extractor{pkg: types.NewPackage(modulePath, "eebusruntime")}
	if err := x.checkType(hidden, map[types.Type]bool{}); err == nil || !strings.Contains(err.Error(), "unexported public dependency") {
		t.Fatalf("unexported approved dependency was accepted: %v", err)
	}

	local := types.NewPackage(modulePath, "eebusruntime")
	alias := types.NewAlias(types.NewTypeName(token.NoPos, local, "Facade", nil), hidden)
	x = extractor{pkg: local}
	if err := x.checkType(alias, map[types.Type]bool{}); err == nil || !strings.Contains(err.Error(), "unexported public dependency") {
		t.Fatalf("alias-hidden dependency was accepted: %v", err)
	}
}

func TestRendererPreservesStructTagsAndAliasIdentity(t *testing.T) {
	field := types.NewField(token.NoPos, nil, "Value", types.Typ[types.String], false)
	tagged := types.NewStruct([]*types.Var{field}, []string{`json:"value,omitempty"`})
	rendered, err := renderType(tagged, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := `struct{ Value string "json:\"value,omitempty\"" }`
	if rendered != want {
		t.Fatalf("tagged struct = %q, want %q", rendered, want)
	}

	approved := types.NewPackage(modulePath+"/eebusraw", "eebusraw")
	alias := types.NewAlias(types.NewTypeName(token.NoPos, approved, "PublicAlias", nil), types.Typ[types.String])
	rendered, err = renderType(alias, map[string]string{approved.Path(): "eebusraw"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rendered != "eebusraw.PublicAlias" {
		t.Fatalf("alias occurrence = %q", rendered)
	}
}
