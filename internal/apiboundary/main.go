package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

var forbiddenExportFragments = []string{
	"Registry",
	"Projection",
	"Semantic",
	"Enbility",
	"Ship",
	"SHIP",
	"Spine",
	"SPINE",
	"Dereference",
	"GraphQL",
	"Portal",
	"HomeAssistant",
	"CommandRoute",
	"CommandRouting",
	"TrustStore",
	"TrustMutation",
	"PairingWindow",
}

var mutationVerbs = []string{
	"Register",
	"Unregister",
	"Set",
	"Accept",
	"Pair",
	"Authorize",
	"Trust",
	"Add",
	"Remove",
	"Delete",
	"Update",
	"Enable",
	"Disable",
	"Open",
	"Close",
	"Write",
	"Mutate",
}

var mutationNouns = []string{
	"RemoteSKI",
	"SKI",
	"Peer",
	"Remote",
	"Pairing",
	"Trust",
	"Trusted",
	"Certificate",
	"Fingerprint",
}

var forbiddenImports = []string{
	"github.com/Project-Helianthus/helianthus-ebusgateway",
	"github.com/Project-Helianthus/helianthus-ha-integration",
}

func main() {
	root, err := os.Getwd()
	if err != nil {
		fatal(err)
	}
	fset := token.NewFileSet()
	var violations []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			violations = append(violations, fmt.Sprintf("%s: parse error: %v", rel, err))
			return nil
		}
		internal := hasPathSegment(rel, "internal")
		for _, imp := range file.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			if !internal && strings.Contains(importPath, "github.com/enbility") {
				violations = append(violations, at(fset, imp.Pos(), rel, "direct enbility imports are allowed only under internal/"))
			}
			for _, forbidden := range forbiddenImports {
				if strings.Contains(importPath, forbidden) {
					violations = append(violations, at(fset, imp.Pos(), rel, "gateway or consumer imports are not allowed in this repo"))
				}
			}
		}
		if internal {
			return nil
		}
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Name != nil {
					checkExportedName(fset, rel, d.Name, &violations)
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						checkExportedName(fset, rel, s.Name, &violations)
					case *ast.ValueSpec:
						for _, name := range s.Names {
							checkExportedName(fset, rel, name, &violations)
						}
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		fatal(err)
	}
	if len(violations) > 0 {
		for _, violation := range violations {
			fmt.Fprintln(os.Stderr, violation)
		}
		os.Exit(1)
	}
}

func checkExportedName(fset *token.FileSet, rel string, ident *ast.Ident, violations *[]string) {
	if ident == nil || !ident.IsExported() {
		return
	}
	name := ident.Name
	for _, fragment := range forbiddenExportFragments {
		if strings.Contains(name, fragment) {
			*violations = append(*violations, at(fset, ident.Pos(), rel, "public API exposes forbidden boundary term "+fragment))
			return
		}
	}
	if looksLikeMutationSurface(name) {
		*violations = append(*violations, at(fset, ident.Pos(), rel, "public API exposes premature trust or pairing mutation surface"))
	}
}

func looksLikeMutationSurface(name string) bool {
	for _, verb := range mutationVerbs {
		if !startsWithWord(name, verb) {
			continue
		}
		for _, noun := range mutationNouns {
			if strings.Contains(name, noun) {
				return true
			}
		}
	}
	return false
}

func startsWithWord(name string, word string) bool {
	if !strings.HasPrefix(name, word) {
		return false
	}
	if len(name) == len(word) {
		return true
	}
	next := []rune(strings.TrimPrefix(name, word))[0]
	return unicode.IsUpper(next) || unicode.IsDigit(next)
}

func hasPathSegment(path string, segment string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == segment {
			return true
		}
	}
	return false
}

func at(fset *token.FileSet, pos token.Pos, rel string, message string) string {
	position := fset.Position(pos)
	if position.IsValid() {
		return fmt.Sprintf("%s:%d: %s", rel, position.Line, message)
	}
	return rel + ": " + message
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
