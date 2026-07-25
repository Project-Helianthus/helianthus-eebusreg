package eebusstore

import (
	"encoding"
	"encoding/base64"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var allowedStoreBridgeTypes = map[string]struct{}{
	"AssociationBridge":          {},
	"ControlAssociation":         {},
	"ControlAttempt":             {},
	"ControlGenerationBinding":   {},
	"ControlManifestBinding":     {},
	"ControlPendingPublication":  {},
	"ControlPublication":         {},
	"ControlQuarantine":          {},
	"ControlReceipt":             {},
	"ControlRecord":              {},
	"ControlTombstone":           {},
	"ControlView":                {},
	"KeyProvider":                {},
	"KeyProviderBinding":         {},
	"PreparedControlPublication": {},
}

func storeBridgeTypeAllowed(name string) bool {
	_, allowed := allowedStoreBridgeTypes[name]
	return allowed
}

func TestStoreBridgeRejectsUnlistedControlPrefixedExports(t *testing.T) {
	for _, name := range []string{"ControlPolicy", "ControlMutation", "PreparedControlRepair"} {
		if storeBridgeTypeAllowed(name) {
			t.Fatalf("unlisted bridge type %s was allowed", name)
		}
	}
}

func TestRecordAndErrorFormattingRedactsEverySensitiveRepresentation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private-store-root")
	secrets := syntheticSecrets(root)
	generation, err := decodeGenerationV1(readFixture(t, "generation-v1-populated.json"))
	if err != nil {
		t.Fatal(err)
	}
	formattedRecord := formatVariants(&generation)
	assertTextContainsNoSecrets(t, formattedRecord, secrets)

	cause := errors.New(strings.Join(secrets, "|"))
	storeErr := newStoreError(outcomeMalformedState, "decode_generation", cause)
	if _, ok := any(storeErr).(fmt.Stringer); !ok {
		t.Fatal("store error does not implement String")
	}
	if _, ok := any(storeErr).(fmt.GoStringer); !ok {
		t.Fatal("store error does not implement GoString")
	}
	if _, ok := any(storeErr).(fmt.Formatter); !ok {
		t.Fatal("store error does not implement Format")
	}
	formattedError := formatVariants(storeErr)
	assertTextContainsNoSecrets(t, formattedError, secrets)
	if !strings.Contains(formattedError, string(outcomeMalformedState)) || !strings.Contains(formattedError, "decode_generation") {
		t.Fatalf("redacted error omitted stable outcome or operation: %q", formattedError)
	}
	if marshaler, ok := any(storeErr).(encoding.TextMarshaler); ok {
		payload, err := marshaler.MarshalText()
		if err != nil {
			t.Fatal(err)
		}
		assertTextContainsNoSecrets(t, string(payload), secrets)
	}
	if nested := errors.Unwrap(storeErr); nested != nil {
		assertTextContainsNoSecrets(t, formatVariants(nested), secrets)
	}
}

func TestInjectedFailuresNeverExposeNestedCauseOrAbsoluteRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private-store-root")
	secrets := syntheticSecrets(root)
	hook := func(call syscallCall) error {
		if call.point == pointBootstrapParentFsync {
			return errors.New(strings.Join(secrets, ":"))
		}
		return nil
	}
	result := openForTest(t, root, hook, nil)
	assertOutcome(t, result.outcome, outcomeBootstrapDurabilityUnknown)
	if result.err == nil {
		t.Fatal("terminal injected failure returned no redacted error")
	}
	assertTextContainsNoSecrets(t, formatVariants(result.err), secrets)
}

func TestGeneratedNamesHooksEnvironmentAndArgvContainNoSecrets(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private-store-root")
	secrets := syntheticSecrets(root)
	var calls []syscallCall
	hook := func(call syscallCall) error {
		calls = append(calls, call)
		return nil
	}
	provider, providers := validProviderRegistry()
	opened := openForTest(t, root, hook, providers)
	assertOutcome(t, opened.outcome, outcomeOpenedEmpty)
	populated, err := decodeGenerationV1(readFixture(t, "generation-v1-populated.json"))
	if err != nil {
		t.Fatal(err)
	}
	committed := opened.store.commit(populated.state)
	assertOutcome(t, committed.outcome, outcomeCommitDurable)
	closeStore(t, opened)
	if len(provider.calls) == 0 {
		t.Fatal("key-bearing commit did not exercise provider")
	}

	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path != root {
			assertTextContainsNoSecrets(t, filepath.Base(path), secrets[:len(secrets)-1])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range calls {
		assertTextContainsNoSecrets(t, call.oldName+"|"+call.newName, secrets)
	}
	assertTextContainsNoSecrets(t, strings.Join(os.Args, "|"), secrets)
	assertTextContainsNoSecrets(t, strings.Join(os.Environ(), "|"), secrets)
}

func TestPackageStaysInternalUnexportedAndFreeOfRuntimeOrPolicyBehavior(t *testing.T) {
	allowedBridgeFunctions := map[string]struct{}{
		"OpenAssociationBridge": {},
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(filepath.Dir(workingDirectory)) != "internal" || filepath.Base(workingDirectory) != "eebusstore" {
		t.Fatalf("store package escaped internal boundary: %s", workingDirectory)
	}

	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	productionFiles := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		productionFiles++
		file, err := parser.ParseFile(fset, name, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imported := range file.Imports {
			path := strings.Trim(imported.Path.Value, `"`)
			if path == "net" || strings.HasPrefix(path, "net/") || strings.HasPrefix(path, "github.com/enbility/") {
				t.Fatalf("%s imports excluded runtime/protocol package %q", name, path)
			}
		}
		for _, declaration := range file.Decls {
			switch declaration := declaration.(type) {
			case *ast.FuncDecl:
				if declaration.Recv == nil && declaration.Name.IsExported() {
					if _, allowed := allowedBridgeFunctions[declaration.Name.Name]; !allowed {
						t.Fatalf("%s exports package-level function %s", name, declaration.Name.Name)
					}
				}
			case *ast.GenDecl:
				for _, spec := range declaration.Specs {
					switch spec := spec.(type) {
					case *ast.TypeSpec:
						if spec.Name.IsExported() {
							if !storeBridgeTypeAllowed(spec.Name.Name) {
								t.Fatalf("%s exports type %s", name, spec.Name.Name)
							}
						}
					case *ast.ValueSpec:
						for _, identifier := range spec.Names {
							if identifier.IsExported() {
								t.Fatalf("%s exports value %s", name, identifier.Name)
							}
						}
					}
				}
			}
		}
		payload, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, excluded := range []string{
			"net.Listen",
			"net.Dial",
			"ListenAndServe",
			"pairing_state",
			"trust_state",
			"quarantine_policy",
			"semantic_device_id",
			"lifecycle_state",
			"activateRecovery",
		} {
			if strings.Contains(string(payload), excluded) {
				t.Fatalf("%s contains excluded runtime or policy surface %q", name, excluded)
			}
		}
	}
	if productionFiles == 0 {
		t.Fatal("internal store production package has not been implemented")
	}
}

func formatVariants(value any) string {
	return strings.Join([]string{
		fmt.Sprint(value),
		fmt.Sprintf("%s", value),
		fmt.Sprintf("%v", value),
		fmt.Sprintf("%+v", value),
		fmt.Sprintf("%#v", value),
		fmt.Sprintf("%q", value),
	}, "|")
}

func syntheticSecrets(root string) []string {
	return []string{
		"local-ski-v1",
		"bG9jYWwtc2tpLXYx",
		"remote-ski-one",
		"cmVtb3RlLXNraS1vbmU=",
		"remote-ski-two",
		"cmVtb3RlLXNraS10d28=",
		"Gerät \"Küche\" \\ <main>",
		"Étage 2",
		"AQ==",
		"AgM=",
		"sealed-provider-reference",
		"c2VhbGVkLXByb3ZpZGVyLXJlZmVyZW5jZQ==",
		"MIIBFjCByaADAgECAgEB",
		"9a82517f9af19416d98fdbcf193726b3a95c0b6fec1d51884bf3e1b739ba2ef4",
		root,
		base64.StdEncoding.EncodeToString([]byte(root)),
	}
}

func assertTextContainsNoSecrets(t *testing.T, text string, secrets []string) {
	t.Helper()
	for _, secret := range secrets {
		if secret != "" && strings.Contains(text, secret) {
			t.Fatalf("redaction failure: output contains synthetic secret variant of length %d", len(secret))
		}
	}
}
