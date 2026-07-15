package eebusfacade

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestStaticEvidencePinsEEBusGoAPI(t *testing.T) {
	evidence := StaticEvidence()
	if evidence.Module.Path != EEBusGoModulePath {
		t.Fatalf("module path = %q, want %q", evidence.Module.Path, EEBusGoModulePath)
	}
	if evidence.Module.Version != EEBusGoVersion {
		t.Fatalf("module version = %q, want %q", evidence.Module.Version, EEBusGoVersion)
	}
	if evidence.API.ImportPath != apiImportPath {
		t.Fatalf("api import path = %q, want %q", evidence.API.ImportPath, apiImportPath)
	}
	if !evidence.API.ServiceReaderShapeBound {
		t.Fatal("service reader callback shape binding was not recorded")
	}
	if !evidence.API.ConfigurationConstructorBound {
		t.Fatal("configuration constructor binding was not recorded")
	}
	if evidence.Boundary.RuntimeSideEffects {
		t.Fatal("static evidence incorrectly claims runtime side effects")
	}
}

func TestStaticEvidenceReturnsIndependentSlices(t *testing.T) {
	evidence := StaticEvidence()
	evidence.API.RequiredServiceReaders[0] = "mutated"
	evidence.API.ExcludedRuntimeOrMutators[0] = "mutated"

	next := StaticEvidence()
	if next.API.RequiredServiceReaders[0] == "mutated" {
		t.Fatal("StaticEvidence aliased required service readers")
	}
	if next.API.ExcludedRuntimeOrMutators[0] == "mutated" {
		t.Fatal("StaticEvidence aliased excluded runtime or mutator methods")
	}
}

func TestGoListPinsEEBusGoWithoutReplace(t *testing.T) {
	root := repoRoot(t)
	cmd := exec.Command("go", "list", "-m", "-json", EEBusGoModulePath)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list module: %v", err)
	}

	var module struct {
		Path    string
		Version string
		Replace *struct {
			Path    string
			Version string
		}
	}
	if err := json.Unmarshal(out, &module); err != nil {
		t.Fatalf("parse go list output: %v", err)
	}
	if module.Path != EEBusGoModulePath {
		t.Fatalf("go list module path = %q, want %q", module.Path, EEBusGoModulePath)
	}
	if module.Version != EEBusGoVersion {
		t.Fatalf("go list module version = %q, want %q", module.Version, EEBusGoVersion)
	}
	if module.Replace != nil {
		t.Fatalf("eebus-go module is replaced: %+v", module.Replace)
	}
}

func TestExpectedControlSurfaceIsStableAndSorted(t *testing.T) {
	surface := ExpectedControlSurface()
	assertSorted(t, "service readers", surface.RequiredServiceReaders)
	assertSorted(t, "reader callbacks", surface.RequiredReaderCallbacks)
	assertSorted(t, "configuration readers", surface.RequiredConfigurationReaders)
	assertSorted(t, "excluded runtime or mutator methods", surface.ExcludedRuntimeOrMutators)

	wantServiceReaders := []string{
		"Configuration",
		"IsAutoAcceptEnabled",
		"PairingDetailForSki",
		"RemoteServiceForSKI",
	}
	wantReaderCallbacks := []string{
		"RemoteSKIConnected",
		"RemoteSKIDisconnected",
		"ServicePairingDetailUpdate",
		"ServiceShipIDUpdate",
		"VisibleRemoteServicesUpdated",
	}
	wantConfigurationReaders := []string{
		"DeviceBrand",
		"DeviceModel",
		"DeviceSerialNumber",
		"Identifier",
		"Interfaces",
		"MdnsServiceName",
		"Port",
		"VendorCode",
	}
	wantExcluded := []string{
		"AddUseCase",
		"CancelPairingWithSKI",
		"DisconnectSKI",
		"RegisterRemoteSKI",
		"SetAutoAccept",
		"SetLogging",
		"Setup",
		"Shutdown",
		"Start",
		"UnregisterRemoteSKI",
		"UserIsAbleToApproveOrCancelPairingRequests",
	}
	assertEqualSlice(t, "service readers", surface.RequiredServiceReaders, wantServiceReaders)
	assertEqualSlice(t, "reader callbacks", surface.RequiredReaderCallbacks, wantReaderCallbacks)
	assertEqualSlice(t, "configuration readers", surface.RequiredConfigurationReaders, wantConfigurationReaders)
	assertEqualSlice(t, "excluded runtime or mutator methods", surface.ExcludedRuntimeOrMutators, wantExcluded)
}

func TestRuntimeAndMutationMethodsAreExcludedFromApprovedReaders(t *testing.T) {
	surface := ExpectedControlSurface()
	for _, excluded := range surface.ExcludedRuntimeOrMutators {
		if slices.Contains(surface.RequiredServiceReaders, excluded) {
			t.Fatalf("excluded runtime or mutator method %q is approved as a service reader", excluded)
		}
	}
}

func TestServiceReaderShapeMatchesExpectedCallbacks(t *testing.T) {
	got := serviceReaderCallbackSignatures()
	want := expectedServiceReaderCallbackSignatures()
	if len(got) != len(want) {
		t.Fatalf("service reader callback count = %d, want %d: got %+v", len(got), len(want), got)
	}
	for i := range got {
		if got[i].Name != want[i].Name || !slices.Equal(got[i].Inputs, want[i].Inputs) {
			t.Fatalf("service reader callback %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if !serviceReaderShapeMatches() {
		t.Fatal("service reader shape did not match expected callback signatures")
	}
}

func TestFacadeDoesNotOwnLowLevelNetworkIO(t *testing.T) {
	files := parseImplementationFiles(t)
	for path, file := range files {
		if filepath.Base(path) == "runtime.go" {
			continue
		}
		for _, imp := range file.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			switch importPath {
			case "net", "net/http", "crypto/tls":
				t.Fatalf("%s imports low-level network dependency %q", path, importPath)
			}
		}
	}
}

func TestExportedFacadeAPIUsesOnlyPlainTypes(t *testing.T) {
	fset, files := parseImplementationPackage(t)
	localTypes := localTypeSpecs(files)
	for path, file := range files {
		importAliases := importAliasMap(file)
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Name.IsExported() && exportedFacadeReceiver(d) {
					assertExprNoExternal(t, fset, path, d.Type, importAliases, localTypes, map[string]bool{})
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if s.Name.IsExported() {
							assertExprNoExternal(t, fset, path, s.Type, importAliases, localTypes, map[string]bool{s.Name.Name: true})
						}
					case *ast.ValueSpec:
						for _, name := range s.Names {
							if name.IsExported() && s.Type != nil {
								assertExprNoExternal(t, fset, path, s.Type, importAliases, localTypes, map[string]bool{})
							}
						}
					}
				}
			}
		}
	}
}

func exportedFacadeReceiver(declaration *ast.FuncDecl) bool {
	if declaration.Recv == nil || len(declaration.Recv.List) == 0 {
		return true
	}
	receiver := declaration.Recv.List[0].Type
	if pointer, ok := receiver.(*ast.StarExpr); ok {
		receiver = pointer.X
	}
	identifier, ok := receiver.(*ast.Ident)
	return ok && identifier.IsExported()
}

func TestFacadeImplementationHasNoRuntimeSideEffects(t *testing.T) {
	banned := []string{
		"net.Listen",
		"tls.Listen",
		"ListenAndServe",
		"/data/eebus",
		"TrustStore",
		"trust_store",
		"truststore",
	}
	for path := range parseImplementationFiles(t) {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(payload)
		for _, token := range banned {
			if strings.Contains(text, token) {
				t.Fatalf("%s contains premature runtime token %q", path, token)
			}
		}
	}
}

func assertSorted(t *testing.T, label string, values []string) {
	t.Helper()
	if !slices.IsSorted(values) {
		t.Fatalf("%s are not sorted: %v", label, values)
	}
}

func assertEqualSlice(t *testing.T, label string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
}

func parseImplementationFiles(t *testing.T) map[string]*ast.File {
	t.Helper()
	_, files := parseImplementationPackage(t)
	byPath := map[string]*ast.File{}
	for path, file := range files {
		byPath[path] = file
	}
	return byPath
}

type localTypeSpec struct {
	path          string
	expr          ast.Expr
	importAliases map[string]string
}

func localTypeSpecs(files map[string]*ast.File) map[string]localTypeSpec {
	specs := map[string]localTypeSpec{}
	for path, file := range files {
		aliases := importAliasMap(file)
		for _, decl := range file.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range genDecl.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				specs[typeSpec.Name.Name] = localTypeSpec{
					path:          path,
					expr:          typeSpec.Type,
					importAliases: aliases,
				}
			}
		}
	}
	return specs
}

func importAliasMap(file *ast.File) map[string]string {
	aliases := map[string]string{}
	for _, imp := range file.Imports {
		importPath := strings.Trim(imp.Path.Value, `"`)
		if imp.Name != nil {
			aliases[imp.Name.Name] = importPath
			continue
		}
		parts := strings.Split(importPath, "/")
		aliases[parts[len(parts)-1]] = importPath
	}
	return aliases
}

func assertExprNoExternal(
	t *testing.T,
	fset *token.FileSet,
	path string,
	expr ast.Expr,
	importAliases map[string]string,
	localTypes map[string]localTypeSpec,
	seen map[string]bool,
) {
	t.Helper()
	ast.Inspect(expr, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.SelectorExpr:
			ident, ok := n.X.(*ast.Ident)
			if !ok {
				return true
			}
			importPath, ok := importAliases[ident.Name]
			if ok && strings.HasPrefix(importPath, "github.com/enbility/") {
				t.Fatalf("%s exported facade API references external type %s.%s from %s", fset.Position(n.Pos()), ident.Name, n.Sel.Name, importPath)
			}
		case *ast.Ident:
			local, ok := localTypes[n.Name]
			if !ok || seen[n.Name] {
				return true
			}
			seen[n.Name] = true
			assertExprNoExternal(t, fset, local.path, local.expr, local.importAliases, localTypes, seen)
		}
		return true
	})
	_ = path
}

func parseImplementationPackage(t *testing.T) (*token.FileSet, map[string]*ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]*ast.File{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, entry.Name(), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		abs, err := filepath.Abs(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		files[abs] = file
	}
	return fset, files
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		next := filepath.Dir(dir)
		if next == dir {
			t.Fatal("could not find repository root")
		}
		dir = next
	}
}
