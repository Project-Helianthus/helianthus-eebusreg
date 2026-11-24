package main

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
)

const semregModulePath = "github.com/Project-Helianthus/helianthus-semreg"

const semregDriverContractPath = semregModulePath + "/semreg/v1/driver"

var driverSemregContractPackages = map[string]struct{}{
	semregModulePath + "/semreg/v1":           {},
	semregDriverContractPath:                  {},
	semregModulePath + "/semreg/v1/roweval":   {},
	semregModulePath + "/semreg/v1/catalog":   {},
	semregModulePath + "/semreg/v1/operation": {},
	semregModulePath + "/semreg/v1/graph":     {},
}

func isDriverDirectory(directory string) bool {
	return directory == "driver" || strings.HasPrefix(directory, "driver/")
}

func publicPackageRegistration(directory string) (string, bool) {
	if isDriverDirectory(directory) {
		return filepath.Base(directory), true
	}
	name, allowed := allowedPublicPackages[directory]
	return name, allowed
}

func appendDriverPackageViolations(root, modulePath string, fset *token.FileSet, packages map[string]*packageInventory, violations *[]string) error {
	for importPath, inventory := range packages {
		if directory := strings.TrimPrefix(importPath, modulePath+"/"); directory != importPath && isDriverDirectory(directory) {
			*violations = append(*violations, driverGlobalInstanceViolations(root, fset, inventory)...)
		}
	}
	drift, err := driverInventoryDrift(driverExportsByDirectory(modulePath, packages))
	if err != nil {
		return err
	}
	*violations = append(*violations, drift...)
	return nil
}

func isSemregImport(importPath string) bool {
	return importPath == semregModulePath || strings.HasPrefix(importPath, semregModulePath+"/")
}

func driverSemregImportAdmitted(importPath string) bool {
	_, admitted := driverSemregContractPackages[importPath]
	return admitted
}

func driverExportsByDirectory(modulePath string, packages map[string]*packageInventory) map[string]map[manifestExport]struct{} {
	observed := make(map[string]map[manifestExport]struct{})
	for importPath, inventory := range packages {
		directory := strings.TrimPrefix(importPath, modulePath+"/")
		if directory != importPath && isDriverDirectory(directory) {
			observed[directory] = inventory.exports
		}
	}
	return observed
}

func driverSemanticInputSelectors(file *ast.File, directory string, declaration *ast.FuncDecl) map[*ast.Ident]struct{} {
	if !isDriverDirectory(directory) || declaration.Name.Name != "StartOutput" {
		return nil
	}
	aliases := make(map[string]struct{})
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil || importPath != semregDriverContractPath {
			continue
		}
		name := filepath.Base(importPath)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		aliases[name] = struct{}{}
	}
	selectors := make(map[*ast.Ident]struct{})
	ast.Inspect(declaration.Type, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		identifier, ok := selector.X.(*ast.Ident)
		if !ok || selector.Sel.Name != "SemanticInput" {
			return true
		}
		if _, aliased := aliases[identifier.Name]; aliased {
			selectors[selector.Sel] = struct{}{}
		}
		return true
	})
	return selectors
}

// driverGlobalInstanceViolations reports package-level variables that hold a
// process-global driver instance. A driver instance is a locally declared type
// that a constructor (a function named New or new followed by an upper-case
// letter) returns, or the result of a constructor imported from a non-standard
// library package. Plain values such as pairing requests, selectors and errors
// are not instances.
func driverGlobalInstanceViolations(root string, fset *token.FileSet, inventory *packageInventory) []string {
	instanceTypes := make(map[string]bool)
	constructors := make(map[string][]ast.Expr)
	errorTypes := make(map[string]struct{})
	for _, file := range inventory.files {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if function.Recv != nil && len(function.Recv.List) > 0 && function.Name.Name == "Error" {
				errorTypes[receiverName(function.Recv.List[0].Type)] = struct{}{}
			}
			if function.Recv == nil && function.Type.Results != nil {
				for _, result := range function.Type.Results.List {
					constructors[function.Name.Name] = append(constructors[function.Name.Name], result.Type)
					if !isDriverConstructorName(function.Name.Name) {
						continue
					}
					if name := driverLocalTypeName(result.Type); name != "" {
						if _, declared := inventory.types[name]; declared {
							instanceTypes[name] = true
						}
					}
				}
			}
		}
	}
	for name := range errorTypes {
		delete(instanceTypes, name)
	}
	var violations []string
	for _, file := range inventory.files {
		imports := driverImportedPackages(file)
		for _, declaration := range file.Decls {
			gen, ok := declaration.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				continue
			}
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				instance := ""
				if value.Type != nil {
					instance = driverInstanceTypeName(value.Type, instanceTypes, constructors, imports)
				}
				for _, expression := range value.Values {
					if instance == "" {
						instance = driverInstanceTypeName(expression, instanceTypes, constructors, imports)
					}
				}
				if instance == "" {
					continue
				}
				for _, name := range value.Names {
					if name.Name == "_" {
						continue
					}
					violations = append(violations, at(fset, name.Pos(), relativePosition(root, fset, name.Pos()), "driver package variable "+name.Name+" holds a process-global instance; construct instances through their constructor"))
				}
			}
		}
	}
	return violations
}

func isDriverConstructorName(name string) bool {
	rest := ""
	switch {
	case strings.HasPrefix(name, "New"):
		rest = strings.TrimPrefix(name, "New")
	case strings.HasPrefix(name, "new"):
		rest = strings.TrimPrefix(name, "new")
	default:
		return false
	}
	return rest == "" || (rest[0] >= 'A' && rest[0] <= 'Z')
}

func driverLocalTypeName(expression ast.Expr) string {
	switch typed := expression.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.StarExpr:
		return driverLocalTypeName(typed.X)
	case *ast.ParenExpr:
		return driverLocalTypeName(typed.X)
	}
	return ""
}

// driverImportedPackages maps the file-local package name of every import to
// its import path.
func driverImportedPackages(file *ast.File) map[string]string {
	imports := make(map[string]string)
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		name := filepath.Base(importPath)
		if isMajorVersionSegment(name) {
			name = filepath.Base(filepath.Dir(importPath))
		}
		if spec.Name != nil {
			name = spec.Name.Name
		}
		imports[name] = importPath
	}
	return imports
}

func isMajorVersionSegment(segment string) bool {
	if len(segment) < 2 || segment[0] != 'v' {
		return false
	}
	for _, character := range segment[1:] {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func driverInstanceTypeName(expression ast.Expr, instanceTypes map[string]bool, constructors map[string][]ast.Expr, imports map[string]string) string {
	switch typed := expression.(type) {
	case *ast.Ident:
		if instanceTypes[typed.Name] {
			return typed.Name
		}
	case *ast.ParenExpr:
		return driverInstanceTypeName(typed.X, instanceTypes, constructors, imports)
	case *ast.ArrayType:
		// Slices and arrays held at package level are collections of instances.
		return driverInstanceTypeName(typed.Elt, instanceTypes, constructors, imports)
	case *ast.MapType:
		return driverInstanceTypeName(typed.Value, instanceTypes, constructors, imports)
	case *ast.StarExpr:
		return driverInstanceTypeName(typed.X, instanceTypes, constructors, imports)
	case *ast.UnaryExpr:
		if typed.Op == token.AND {
			return driverInstanceTypeName(typed.X, instanceTypes, constructors, imports)
		}
	case *ast.CompositeLit:
		return driverInstanceTypeName(typed.Type, instanceTypes, constructors, imports)
	case *ast.CallExpr:
		if selector, ok := typed.Fun.(*ast.SelectorExpr); ok {
			// An imported constructor result is an instance of the imported
			// package; the standard library is not a driver instance source.
			if qualifier, ok := selector.X.(*ast.Ident); ok && isDriverConstructorName(selector.Sel.Name) && selector.Sel.Name != "New" && strings.HasPrefix(selector.Sel.Name, "New") {
				if importPath, imported := imports[qualifier.Name]; imported && isNonStandardImport(importPath) {
					return selector.Sel.Name
				}
			}
			return ""
		}
		identifier, ok := typed.Fun.(*ast.Ident)
		if !ok {
			return ""
		}
		if (identifier.Name == "new" || identifier.Name == "make") && len(typed.Args) >= 1 {
			return driverInstanceTypeName(typed.Args[0], instanceTypes, constructors, imports)
		}
		for _, result := range constructors[identifier.Name] {
			if name := driverInstanceTypeName(result, instanceTypes, constructors, imports); name != "" {
				return name
			}
		}
	}
	return ""
}

func isNonStandardImport(importPath string) bool {
	first, _, _ := strings.Cut(importPath, "/")
	return strings.Contains(first, ".")
}
