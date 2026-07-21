package main_test

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestIssue54ProductionHasNoCompatibilityPublisherOrOutboundFabricator(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	banned := [][]byte{
		[]byte("Compat" + "MDNS"),
		[]byte("startLAN" + "SHIPPublisher"),
		[]byte("lan" + "SHIPPublisher"),
		[]byte("newLAN" + "SHIPMDNSProvider"),
		[]byte("Raw" + "Probe"),
		[]byte("QueueRemote" + "SKI("),
		[]byte("ReportRemote" + "Endpoint("),
		[]byte("current" + "MigrationGraph"),
		[]byte("migration" + "Graph"),
		[]byte("opened_" + "migrated"),
		[]byte("migrate" + "MSP04"),
	}
	bannedFacade := [][]byte{
		[]byte("Outgoing" + "Attempt"),
		[]byte("outgoing" + "Attempt"),
		[]byte("pre" + "dial"),
		[]byte("pre" + "Dial"),
	}
	bannedNames := []string{
		"compatmdns",
		"compat_mdns",
		"compat-mdns",
		"lanshippublisher",
		"raw" + "probe",
		"runtime_" + "outgoing_attempt",
		"msp04cr2_" + "predial",
		"migration.go",
		"control_v2.go",
	}
	pythonPublisherIndicators := [][]byte{
		[]byte("_ship._tcp"),
		[]byte("zeroconf"),
		[]byte("register="),
		[]byte("announce"),
	}
	var findings []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !issue55ProductionSource(entry.Name()) {
			return nil
		}
		payload, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, token := range banned {
			if bytes.Contains(payload, token) {
				relative, relErr := filepath.Rel(root, path)
				if relErr != nil {
					return relErr
				}
				findings = append(findings, relative+":"+string(token))
			}
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if strings.HasPrefix(relative, filepath.Join("internal", "eebusfacade")+string(filepath.Separator)) ||
			strings.HasPrefix(relative, filepath.Join("internal", "eebusservicebridge")+string(filepath.Separator)) {
			for _, token := range bannedFacade {
				if bytes.Contains(payload, token) {
					findings = append(findings, relative+":"+string(token))
				}
			}
		}
		lowerName := strings.ToLower(entry.Name())
		for _, token := range bannedNames {
			if strings.Contains(lowerName, token) {
				relative, relErr := filepath.Rel(root, path)
				if relErr != nil {
					return relErr
				}
				findings = append(findings, relative+":filename:"+token)
			}
		}
		if strings.EqualFold(filepath.Ext(path), ".py") {
			lowerPayload := bytes.ToLower(payload)
			if strings.Contains(lowerName, "publish") || strings.Contains(lowerName, "publisher") {
				relative, relErr := filepath.Rel(root, path)
				if relErr != nil {
					return relErr
				}
				findings = append(findings, relative+":python-publisher-filename")
			}
			for _, indicator := range pythonPublisherIndicators {
				if bytes.Contains(lowerPayload, indicator) {
					relative, relErr := filepath.Rel(root, path)
					if relErr != nil {
						return relErr
					}
					findings = append(findings, relative+":python-publisher-content:"+string(indicator))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("obsolete compatibility/outbound publisher paths remain: %v", findings)
	}
}

func TestIssue54ProductionComposesExactlyOneCanonicalPublisher(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	result := issue55PublisherScan{}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(entry.Name()) != ".go" || !issue55ProductionSource(entry.Name()) {
			return nil
		}
		payload, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		return inspectIssue55PublisherSource(root, path, payload, &result)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.findings) != 0 || result.upstreamCanonical != 1 || result.runtimeCanonical != 1 {
		t.Fatalf("publisher composition findings=%v upstream_canonical=%d runtime_canonical=%d, want []/1/1", result.findings, result.upstreamCanonical, result.runtimeCanonical)
	}
}

func TestIssue55PublisherGuardRejectsAliasedAndRenamedConstructors(t *testing.T) {
	mutations := []struct {
		name   string
		path   string
		source string
		want   string
	}{
		{
			name: "aliased upstream constructor",
			path: filepath.Join("internal", "mutation", "main.go"),
			source: `package main

import factory "github.com/Project-Helianthus/helianthus-eebus-go/service"

func main() { factory.ConstructPeer() }
`,
			want: "calls-upstream-service.ConstructPeer",
		},
		{
			name: "aliased SHIP responder constructor",
			path: filepath.Join("internal", "mutation", "main.go"),
			source: `package main

import responder "github.com/Project-Helianthus/helianthus-ship-go/hub"

func main() { responder.ConstructPeer() }
`,
			want: "calls-ship-hub.ConstructPeer",
		},
		{
			name: "renamed fake peer dispatch",
			path: filepath.Join("internal", "mutation", "main.go"),
			source: `package main

func main() { runFakePeerSmoke(fakePeerOptions{}) }
`,
			want: "fake-peer-executable",
		},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			result := issue55PublisherScan{}
			if err := inspectIssue55PublisherSource(".", mutation.path, []byte(mutation.source), &result); err != nil {
				t.Fatal(err)
			}
			if !issue55ContainsFinding(result.findings, mutation.want) {
				t.Fatalf("findings = %v, want one containing %q", result.findings, mutation.want)
			}
		})
	}
}

const (
	issue55UpstreamServicePath = "github.com/Project-Helianthus/helianthus-eebus-go/service"
	issue55ServiceBridgePath   = "github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusservicebridge"
	issue55ShipHubPath         = "github.com/Project-Helianthus/helianthus-ship-go/hub"
	issue55ShipConnectionPath  = "github.com/Project-Helianthus/helianthus-ship-go/ship"
	issue55ShipMDNSPath        = "github.com/Project-Helianthus/helianthus-ship-go/mdns"
	issue55CanonicalBridgeFile = "internal/eebusservicebridge/service.go"
	issue55CanonicalRuntime    = "internal/eebusfacade/runtime.go"
)

type issue55PublisherScan struct {
	findings          []string
	upstreamCanonical int
	runtimeCanonical  int
}

func inspectIssue55PublisherSource(root, filename string, source []byte, result *issue55PublisherScan) error {
	parsed, err := parser.ParseFile(token.NewFileSet(), filename, source, 0)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, filename)
	if err != nil {
		return err
	}
	relative = filepath.ToSlash(relative)
	imports := make(map[string]string, len(parsed.Imports))
	for _, spec := range parsed.Imports {
		importPath, unquoteErr := strconv.Unquote(spec.Path.Value)
		if unquoteErr != nil {
			return unquoteErr
		}
		alias := path.Base(importPath)
		if spec.Name != nil {
			alias = spec.Name.Name
		}
		imports[alias] = importPath
		if importPath == issue55UpstreamServicePath && relative != issue55CanonicalBridgeFile {
			result.findings = append(result.findings, relative+":imports-upstream-service")
		}
		if importPath == issue55ShipMDNSPath {
			result.findings = append(result.findings, relative+":imports-ship-mdns")
		}
		if importPath == issue55ShipHubPath {
			result.findings = append(result.findings, relative+":imports-ship-hub")
		}
		if importPath == issue55ShipConnectionPath {
			result.findings = append(result.findings, relative+":imports-ship-connection")
		}
	}
	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		qualifier, ok := selector.X.(*ast.Ident)
		if !ok {
			return true
		}
		switch imports[qualifier.Name] {
		case issue55UpstreamServicePath:
			if relative == issue55CanonicalBridgeFile && selector.Sel.Name == "NewServiceWithOptions" {
				result.upstreamCanonical++
			} else {
				result.findings = append(result.findings, relative+":calls-upstream-service."+selector.Sel.Name)
			}
		case issue55ServiceBridgePath:
			if relative == issue55CanonicalRuntime && selector.Sel.Name == "NewServiceWithOptions" {
				result.runtimeCanonical++
			} else {
				result.findings = append(result.findings, relative+":calls-service-bridge."+selector.Sel.Name)
			}
		case issue55ShipMDNSPath:
			result.findings = append(result.findings, relative+":calls-ship-mdns."+selector.Sel.Name)
		case issue55ShipHubPath:
			result.findings = append(result.findings, relative+":calls-ship-hub."+selector.Sel.Name)
		case issue55ShipConnectionPath:
			result.findings = append(result.findings, relative+":calls-ship-connection."+selector.Sel.Name)
		}
		return true
	})
	if parsed.Name.Name == "main" {
		ast.Inspect(parsed, func(node ast.Node) bool {
			switch typed := node.(type) {
			case *ast.Ident:
				normalized := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(typed.Name))
				if strings.Contains(normalized, "fakepeer") {
					result.findings = append(result.findings, relative+":fake-peer-executable-ident:"+typed.Name)
				}
			case *ast.BasicLit:
				if typed.Kind == token.STRING {
					value, unquoteErr := strconv.Unquote(typed.Value)
					if unquoteErr == nil && strings.Contains(strings.ToLower(value), "fake-peer") {
						result.findings = append(result.findings, relative+":fake-peer-executable-literal")
					}
				}
			}
			return true
		})
	}
	return nil
}

func issue55ContainsFinding(findings []string, want string) bool {
	for _, finding := range findings {
		if strings.Contains(finding, want) {
			return true
		}
	}
	return false
}

func TestIssue55RuntimeRemoteTypesContainPolicyOnly(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	targets := []struct {
		path     string
		typeName string
		want     string
	}{
		{path: "runtime.go", typeName: "Remote", want: "SKI"},
		{path: filepath.Join("internal", "eebusfacade", "runtime.go"), typeName: "RuntimeRemote", want: "SKI,Pretrusted,Allowlisted"},
	}
	for _, target := range targets {
		file, parseErr := parser.ParseFile(token.NewFileSet(), filepath.Join(root, target.path), nil, 0)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		var fields []string
		ast.Inspect(file, func(node ast.Node) bool {
			spec, ok := node.(*ast.TypeSpec)
			if !ok || spec.Name.Name != target.typeName {
				return true
			}
			structure, ok := spec.Type.(*ast.StructType)
			if !ok {
				t.Fatalf("%s is not a struct", target.typeName)
			}
			for _, field := range structure.Fields.List {
				for _, name := range field.Names {
					fields = append(fields, name.Name)
				}
			}
			return false
		})
		if got := strings.Join(fields, ","); got != target.want {
			t.Errorf("%s fields = %q, want policy-only %q", target.typeName, got, target.want)
		}
	}
}

func issue55ProductionSource(name string) bool {
	if strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, "_test.py") {
		return false
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".c", ".cc", ".cpp", ".go", ".h", ".js", ".py", ".rs", ".sh", ".ts":
		return true
	default:
		return false
	}
}
