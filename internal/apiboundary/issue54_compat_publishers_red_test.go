package main_test

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
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
	canonicalConstructor := []byte("eebusservicebridge.NewServiceWith" + "Options")
	directPublisher := []byte("shipmdns.New" + "MDNS")
	canonicalCount := 0
	directCount := 0
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
		canonicalCount += bytes.Count(payload, canonicalConstructor)
		directCount += bytes.Count(payload, directPublisher)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if directCount != 0 || canonicalCount != 1 {
		t.Fatalf("publisher composition direct=%d canonical=%d, want 0/1", directCount, canonicalCount)
	}
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
