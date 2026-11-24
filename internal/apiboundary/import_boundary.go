package main

import (
	"go/ast"
	"go/token"
	"strings"
)

func checkImportBoundary(fset *token.FileSet, rel string, file *ast.File, directory string, internal, testFile bool, modulePath string, violations *[]string) {
	driverDirectory := isDriverDirectory(directory)
	for _, imp := range file.Imports {
		importPath := strings.Trim(imp.Path.Value, `"`)
		if !internal && !driverDirectory && isProtocolImplementationImport(importPath) {
			*violations = append(*violations, at(fset, imp.Pos(), rel, "direct protocol implementation imports are allowed only under internal/"))
		}
		rootRuntimeImplementation := !internal && !testFile && directory == "." && file.Name.Name == "eebusruntime"
		facadeImport := importPath == modulePath+"/internal/eebusfacade" && (imp.Name == nil || imp.Name.Name != ".")
		facadeImplementationImport := facadeImport && (rootRuntimeImplementation || (!testFile && driverDirectory))
		if !internal && !testFile && strings.HasPrefix(importPath, modulePath+"/internal/") && !facadeImplementationImport {
			*violations = append(*violations, at(fset, imp.Pos(), rel, "public API packages must not import internal implementation types"))
		}
		if !internal && driverDirectory && isSemregImport(importPath) && !driverSemregImportAdmitted(importPath) {
			*violations = append(*violations, at(fset, imp.Pos(), rel, "driver packages may import only the exact SemReg contract leaves, not "+importPath))
		}
		for _, forbidden := range forbiddenImports {
			if strings.Contains(importPath, forbidden) {
				*violations = append(*violations, at(fset, imp.Pos(), rel, "gateway or consumer imports are not allowed in this repo"))
			}
		}
	}
}
