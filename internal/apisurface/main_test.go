package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
		if symbol.Name == "New" || symbol.Name == "Runtime" || symbol.Name == "SnapshotV1" {
			got[symbol.Name] = symbol.Signature
		}
	}
	if got["New"] != "func New(Config) (Runtime, error)" {
		t.Fatalf("New signature = %q", got["New"])
	}
	wantRuntime := "type Runtime interface{ PairingState() ([]PairingObservationV1, error); Shutdown() error; Snapshot() (SnapshotV1, error); Start(context.Context) error }"
	if got["Runtime"] != wantRuntime {
		t.Fatalf("Runtime signature = %q", got["Runtime"])
	}
	if !strings.Contains(got["SnapshotV1"], `json:\"meta\"`) {
		t.Fatalf("SnapshotV1 signature omitted JSON tags: %q", got["SnapshotV1"])
	}
}

func TestForbiddenDependencyLeakageIsRejected(t *testing.T) {
	for _, packagePath := range []string{
		"github.com/enbility/eebus-go/api",
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
