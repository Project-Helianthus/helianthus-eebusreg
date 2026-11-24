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

func driverGlobalInstanceViolations(root string, fset *token.FileSet, inventory *packageInventory) []string {
	instanceTypes := make(map[string]bool)
	for name := range inventory.types {
		instanceTypes[name] = true
	}
	constructors := make(map[string][]ast.Expr)
	for _, file := range inventory.files {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if function.Recv != nil && len(function.Recv.List) > 0 && function.Name.Name == "Error" {
				delete(instanceTypes, receiverName(function.Recv.List[0].Type))
			}
			if function.Recv == nil && function.Type.Results != nil {
				for _, result := range function.Type.Results.List {
					constructors[function.Name.Name] = append(constructors[function.Name.Name], result.Type)
				}
			}
		}
	}
	var violations []string
	for _, file := range inventory.files {
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
					instance = driverInstanceTypeName(value.Type, instanceTypes, constructors)
				}
				for _, expression := range value.Values {
					if instance == "" {
						instance = driverInstanceTypeName(expression, instanceTypes, constructors)
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

func driverInstanceTypeName(expression ast.Expr, instanceTypes map[string]bool, constructors map[string][]ast.Expr) string {
	switch typed := expression.(type) {
	case *ast.Ident:
		if instanceTypes[typed.Name] {
			return typed.Name
		}
	case *ast.ParenExpr:
		return driverInstanceTypeName(typed.X, instanceTypes, constructors)
	case *ast.ArrayType:
		// Slices and arrays held at package level are collections of instances.
		return driverInstanceTypeName(typed.Elt, instanceTypes, constructors)
	case *ast.MapType:
		return driverInstanceTypeName(typed.Value, instanceTypes, constructors)
	case *ast.StarExpr:
		return driverInstanceTypeName(typed.X, instanceTypes, constructors)
	case *ast.UnaryExpr:
		if typed.Op == token.AND {
			return driverInstanceTypeName(typed.X, instanceTypes, constructors)
		}
	case *ast.CompositeLit:
		return driverInstanceTypeName(typed.Type, instanceTypes, constructors)
	case *ast.CallExpr:
		identifier, ok := typed.Fun.(*ast.Ident)
		if !ok {
			return ""
		}
		if (identifier.Name == "new" || identifier.Name == "make") && len(typed.Args) >= 1 {
			return driverInstanceTypeName(typed.Args[0], instanceTypes, constructors)
		}
		for _, result := range constructors[identifier.Name] {
			if name := driverInstanceTypeName(result, instanceTypes, constructors); name != "" {
				return name
			}
		}
	}
	return ""
}
