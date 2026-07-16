package eebusstore

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestMSP04BStoreRemainsMechanicalAndPolicyFree(t *testing.T) {
	forbiddenIdentifiers := map[string]struct{}{
		"adminsocket":             {},
		"candidatenonce":          {},
		"confirmtrust":            {},
		"fingerprintv1":           {},
		"idempotencykey":          {},
		"pairingcandidate":        {},
		"pairingwindow":           {},
		"startingstoregeneration": {},
	}
	forbiddenImports := map[string]struct{}{
		"net":      {},
		"net/http": {},
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	files := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(files, entry.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imported := range parsed.Imports {
			path, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if _, forbidden := forbiddenImports[path]; forbidden {
				t.Fatalf("policy-free store imports transport package %q", path)
			}
		}

		parsed, err = parser.ParseFile(files, entry.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			identifier, ok := node.(*ast.Ident)
			if !ok {
				return true
			}
			if _, forbidden := forbiddenIdentifiers[strings.ToLower(identifier.Name)]; forbidden {
				t.Errorf("store owns coordinator policy identifier %q", identifier.Name)
			}
			return true
		})
	}
}

func TestMSP04CAssociationBridgePreservesFrozenSurfaceAndAddsMechanicalControl(t *testing.T) {
	requiredDeclarations := []string{
		"func OpenAssociationBridge",
		"type AssociationBridge",
		"type KeyProvider",
		"type KeyProviderBinding",
	}
	declarations := exportedStoreDeclarations(t)
	for _, required := range requiredDeclarations {
		if !slices.Contains(declarations, required) {
			t.Fatalf("exported store declarations %v lack frozen declaration %q", declarations, required)
		}
	}
	for _, declaration := range declarations {
		if slices.Contains(requiredDeclarations, declaration) || strings.HasPrefix(declaration, "type Control") || strings.HasPrefix(declaration, "type PreparedControl") {
			continue
		}
		t.Fatalf("unexpected exported store declaration %q", declaration)
	}

	wantProviderMethods := map[string]string{
		"Probe":    "func(string, uint64) error",
		"Unseal":   "func([]uint8) (crypto.Signer, error)",
		"Validate": "func([]uint8, []uint8) error",
	}
	provider := reflect.TypeOf((*KeyProvider)(nil)).Elem()
	if got := exportedMethodSignatures(provider); !reflect.DeepEqual(got, wantProviderMethods) {
		t.Fatalf("KeyProvider methods = %#v, want %#v", got, wantProviderMethods)
	}

	binding := reflect.TypeOf(KeyProviderBinding{})
	wantBindingFields := []string{
		"ID string",
		"Version uint64",
		"Provider eebusstore.KeyProvider",
	}
	gotBindingFields := make([]string, 0, binding.NumField())
	for index := 0; index < binding.NumField(); index++ {
		field := binding.Field(index)
		if field.PkgPath != "" || field.Anonymous {
			t.Fatalf("KeyProviderBinding field %q is not an exported named field", field.Name)
		}
		gotBindingFields = append(gotBindingFields, field.Name+" "+field.Type.String())
	}
	if !slices.Equal(gotBindingFields, wantBindingFields) {
		t.Fatalf("KeyProviderBinding fields = %v, want %v", gotBindingFields, wantBindingFields)
	}

	bridge := reflect.TypeOf(AssociationBridge{})
	for index := 0; index < bridge.NumField(); index++ {
		if field := bridge.Field(index); field.PkgPath == "" {
			t.Fatalf("AssociationBridge gained exported field %q", field.Name)
		}
	}
	frozenBridgeMethods := map[string]string{
		"Close":              "func(*eebusstore.AssociationBridge) error",
		"Commit":             "func(*eebusstore.AssociationBridge, context.Context, uint64, []uint8, string) string",
		"Reload":             "func(*eebusstore.AssociationBridge, context.Context) (uint64, map[string]string, string)",
		"SelectedGeneration": "func(*eebusstore.AssociationBridge) uint64",
	}
	methods := exportedMethodSignatures(reflect.TypeOf((*AssociationBridge)(nil)))
	for name, want := range frozenBridgeMethods {
		if got := methods[name]; got != want {
			t.Fatalf("AssociationBridge.%s signature = %q, want frozen %q", name, got, want)
		}
	}
	mechanicalControlMethods := []string{"CommitControl", "ObserveControlPublication", "PrepareControl", "ReloadControl"}
	for _, name := range mechanicalControlMethods {
		if _, ok := methods[name]; !ok {
			t.Fatalf("AssociationBridge lacks mechanical method %s", name)
		}
	}
	for name := range methods {
		if _, frozen := frozenBridgeMethods[name]; frozen || slices.Contains(mechanicalControlMethods, name) {
			continue
		}
		t.Fatalf("AssociationBridge gained non-mechanical method %s", name)
	}

	wantOpen := "func(string, []eebusstore.KeyProviderBinding) (*eebusstore.AssociationBridge, string)"
	if got := reflect.TypeOf(OpenAssociationBridge).String(); got != wantOpen {
		t.Fatalf("OpenAssociationBridge signature = %q, want %q", got, wantOpen)
	}
}

func exportedStoreDeclarations(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	declarations := make(map[string]struct{})
	files := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(files, entry.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range parsed.Decls {
			switch typed := declaration.(type) {
			case *ast.FuncDecl:
				if typed.Recv == nil && ast.IsExported(typed.Name.Name) {
					declarations["func "+typed.Name.Name] = struct{}{}
				}
			case *ast.GenDecl:
				for _, specification := range typed.Specs {
					switch value := specification.(type) {
					case *ast.TypeSpec:
						if ast.IsExported(value.Name.Name) {
							declarations["type "+value.Name.Name] = struct{}{}
						}
					case *ast.ValueSpec:
						for _, name := range value.Names {
							if ast.IsExported(name.Name) {
								declarations[strings.ToLower(typed.Tok.String())+" "+name.Name] = struct{}{}
							}
						}
					}
				}
			}
		}
	}
	result := make([]string, 0, len(declarations))
	for declaration := range declarations {
		result = append(result, declaration)
	}
	slices.Sort(result)
	return result
}

func exportedMethodSignatures(value reflect.Type) map[string]string {
	methods := make(map[string]string, value.NumMethod())
	for index := 0; index < value.NumMethod(); index++ {
		method := value.Method(index)
		methods[method.Name] = method.Type.String()
	}
	return methods
}
