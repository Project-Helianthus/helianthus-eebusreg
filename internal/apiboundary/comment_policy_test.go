package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestCommentPolicyOfflineComments(t *testing.T) {
	tests := []struct {
		name, source string
		reject       bool
	}{
		{"package documentation", "// Package eebusraw describes raw frames.\n// Additional details remain useful to readers.\npackage eebusraw\n", false},
		{"exported declaration documentation", "package eebusraw\n// Envelope carries a frame.\ntype Envelope struct{}\n", false},
		{"license header", "// Copyright Example\n// Licensed for redistribution.\n\npackage eebusraw\n", false},
		{"detached line explanation", "package eebusraw\n\n// Keep frames in observation order.\n", false},
		{"detached block explanation", "package eebusraw\n\n/* Keep frames in observation order. */\n", false},
		{"cgo explanation", "package eebusraw\n/* A C preamble may explain its implementation. */\nimport \"C\"\n", false},
		{"canonical build constraint", "//go:build linux || darwin\n\npackage eebusraw\n", false},
		{"adjacent explanation", "//go:build linux\n// Platform implementation.\n\npackage eebusraw\n", false},
		{"unrelated directive", "//go:buildx linux\n\npackage eebusraw\n", false},
		{"legacy constraint", "// +build linux\n\npackage eebusraw\n", false},
		{"generator directive", "package eebusraw\n//go:generate example\n", false},
		{"embedding directive", "package eebusraw\n//go:embed fixture\nvar data string\n", false},
		{"noncanonical constraint", "//go:build linux||darwin\n\npackage eebusraw\n", true},
		{"malformed constraint", "//go:build linux &&\n\npackage eebusraw\n", true},
		{"misplaced constraint", "package eebusraw\n\n//go:build linux\n", true},
		{"missing separation", "//go:build linux\npackage eebusraw\n", true},
		{"duplicate constraints", "//go:build linux\n\n//go:build darwin\n\npackage eebusraw\n", true},
		{"duplicate adjacent constraints", "//go:build linux\n//go:build darwin\n\npackage eebusraw\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "fixture.go", tt.source, parser.ParseComments)
			if err != nil {
				t.Fatal(err)
			}
			var violations []string
			checkBuildConstraints(fset, "fixture.go", f, &violations)
			if (len(violations) > 0) != tt.reject {
				t.Fatalf("reject=%v; violations=%v", tt.reject, violations)
			}
		})
	}
}

func TestCommentPolicyOfflineDocumentationPaths(t *testing.T) {
	for _, path := range []string{"README.md", "AGENTS.md", "ROADMAP.md", "notes/API.MD", "notes/PROTOCOL.rst", "reference/API.adoc", "notes/ARCHITECTURE"} {
		t.Run(path, func(t *testing.T) {
			if !isDocumentationPath(path) {
				t.Fatalf("documentation path was not detected: %s", path)
			}
		})
	}
	for _, path := range []string{"runtime.go", "internal/codec/frame.go", "go.mod"} {
		t.Run(path, func(t *testing.T) {
			if isDocumentationPath(path) {
				t.Fatalf("source path classified as documentation: %s", path)
			}
		})
	}
	for _, path := range []string{"docs/frame.go", "internal/DoCs/frame.go"} {
		t.Run(path, func(t *testing.T) {
			if !hasFoldedPathSegment(path, "docs") {
				t.Fatalf("forbidden directory was not detected: %s", path)
			}
		})
	}
}

func TestCommentPolicyOfflineAPIBoundary(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fixture.go", "package eebusraw\n// RegistryEnvelope is still forbidden.\ntype RegistryEnvelope struct{}\n", parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	var violations []string
	ast.Inspect(f, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			checkExportedName(fset, "fixture.go", id, &violations)
		}
		return true
	})
	if len(violations) == 0 || !strings.Contains(strings.Join(violations, " "), "forbidden boundary") {
		t.Fatalf("API violation accepted: %v", violations)
	}
}
