package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/mod/modfile"
)

const minimalREADME = "# eeBUS Registry\n\nCanonical docs: Project-Helianthus/helianthus-docs-eebus.\n\nBuild: `./scripts/ci_local.sh`.\n"

var forbiddenExportFragments = []string{
	"Registry",
	"Projection",
	"Semantic",
	"Enbility",
	"Ship",
	"SHIP",
	"Spine",
	"SPINE",
	"EvidenceRef",
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

var allowedPublicPackages = map[string]string{
	".":             "eebusruntime",
	"eebusevidence": "eebusevidence",
	"eebusraw":      "eebusraw",
}

var documentationExtensions = map[string]struct{}{
	".adoc":     {},
	".asciidoc": {},
	".markdown": {},
	".md":       {},
	".mdown":    {},
	".mkd":      {},
	".rst":      {},
	".txt":      {},
}

var documentationNames = map[string]struct{}{
	"api":          {},
	"architecture": {},
	"changelog":    {},
	"contributing": {},
	"design":       {},
	"governance":   {},
	"protocol":     {},
	"readme":       {},
	"roadmap":      {},
	"security":     {},
	"support":      {},
}

type apiManifest struct {
	Module          string                   `json:"module"`
	Packages        []manifestPackage        `json:"packages"`
	Schema          string                   `json:"schema"`
	StableContracts []manifestStableContract `json:"stable_contracts"`
	Version         int                      `json:"version"`
}

type manifestPackage struct {
	Exports    []manifestExport `json:"exports"`
	ImportPath string           `json:"import_path"`
	Name       string           `json:"name"`
}

type manifestExport struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type manifestStableContract struct {
	Enums      []manifestStableEnum `json:"enums"`
	ImportPath string               `json:"import_path"`
	Types      []manifestStableType `json:"types"`
}

type manifestStableEnum struct {
	Type   string                    `json:"type"`
	Values []manifestStableEnumValue `json:"values"`
}

type manifestStableEnumValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type manifestStableType struct {
	Fields     []manifestStableField `json:"fields,omitempty"`
	Name       string                `json:"name"`
	Underlying string                `json:"underlying"`
}

type manifestStableField struct {
	JSONTag string `json:"json_tag"`
	Name    string `json:"name"`
	Type    string `json:"type"`
}

type packageInventory struct {
	constants map[string]constantDeclaration
	exports   map[manifestExport]struct{}
	name      string
	types     map[string]typeDeclaration
}

type typeDeclaration struct {
	imports map[string]string
	rel     string
	spec    *ast.TypeSpec
}

type constantDeclaration struct {
	name     string
	rel      string
	typeName string
	value    string
}

type stableContractSpec struct {
	enums      []stableEnumSpec
	importPath string
	types      []manifestStableType
	root       string
}

type stableEnumSpec struct {
	exact    bool
	typeName string
	values   []manifestStableEnumValue
}

type pathOrigin string

const (
	trackedPath    pathOrigin = "tracked"
	untrackedPath  pathOrigin = "untracked"
	ignoredPath    pathOrigin = "ignored"
	filesystemPath pathOrigin = "filesystem"
)

func main() {
	flags := flag.NewFlagSet("apiboundary", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	manifestPath := flags.String("manifest", "", "write the API boundary manifest outside the repository")
	if err := flags.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	if flags.NArg() != 0 {
		fatal(fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " ")))
	}

	root, err := os.Getwd()
	if err != nil {
		fatal(err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		fatal(err)
	}

	violations, err := validateRepositoryPaths(root)
	if err != nil {
		fatal(err)
	}
	manifest := apiManifest{}
	if len(violations) == 0 {
		var goViolations []string
		manifest, goViolations, err = inspectGoBoundary(root)
		if err != nil {
			fatal(err)
		}
		violations = append(violations, goViolations...)
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		for _, violation := range violations {
			fmt.Fprintln(os.Stderr, violation)
		}
		os.Exit(1)
	}

	if *manifestPath == "" {
		return
	}
	destination, err := safeManifestDestination(root, *manifestPath)
	if err != nil {
		fatal(err)
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		fatal(fmt.Errorf("marshal API boundary artifact: %w", err))
	}
	data = append(data, '\n')
	if err := writeAtomic(destination, data); err != nil {
		fatal(fmt.Errorf("write API boundary artifact: %w", err))
	}
}

func validateRepositoryPaths(root string) ([]string, error) {
	paths := make(map[string]map[pathOrigin]struct{})
	lists := []struct {
		origin pathOrigin
		args   []string
	}{
		{origin: trackedPath, args: []string{"ls-files", "-z"}},
		{origin: untrackedPath, args: []string{"ls-files", "--others", "--exclude-standard", "-z"}},
		{origin: ignoredPath, args: []string{"ls-files", "--others", "--ignored", "--exclude-standard", "-z"}},
	}
	for _, list := range lists {
		entries, err := gitNullList(root, list.args...)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			addPathOrigin(paths, filepath.ToSlash(entry), list.origin)
		}
	}

	violations := make(map[string]struct{})
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == ".git" && entry.IsDir() {
			return filepath.SkipDir
		}
		if rel == "." {
			return nil
		}
		if _, known := paths[rel]; !known {
			addPathOrigin(paths, rel, filesystemPath)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return fmt.Errorf("inspect symlink %s: %w", rel, err)
			}
			violations[symlinkViolation(rel, target)] = struct{}{}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	stagedSymlinks, err := gitStagedSymlinks(root)
	if err != nil {
		return nil, err
	}
	for path, target := range stagedSymlinks {
		violations[symlinkViolation(path, target)] = struct{}{}
	}

	casefold := make(map[string]string)
	pathNames := make([]string, 0, len(paths))
	for path := range paths {
		pathNames = append(pathNames, path)
	}
	sort.Strings(pathNames)
	for _, path := range pathNames {
		if err := validateRelativePath(path); err != nil {
			violations[fmt.Sprintf("unsafe repository path %q: %v", path, err)] = struct{}{}
		}
		folded := strings.ToLower(path)
		if prior, ok := casefold[folded]; ok && prior != path {
			violations[fmt.Sprintf("casefold collision: %s conflicts with %s", path, prior)] = struct{}{}
		} else {
			casefold[folded] = path
		}
		for origin := range paths[path] {
			if hasFoldedPathSegment(path, "docs") {
				violations[fmt.Sprintf("%s docs path is forbidden: %s", origin, path)] = struct{}{}
			}
			if isDocumentationPath(path) && path != "README.md" && path != "AGENTS.md" {
				violations[fmt.Sprintf("%s markdown/documentation path is outside the allowlist: %s", origin, path)] = struct{}{}
			}
		}
	}

	readmePath := filepath.Join(root, "README.md")
	info, err := os.Lstat(readmePath)
	if err != nil {
		if os.IsNotExist(err) {
			violations["README.md must exist and match the exact minimal README"] = struct{}{}
		} else {
			return nil, err
		}
	} else if info.Mode().IsRegular() {
		data, err := os.ReadFile(readmePath)
		if err != nil {
			return nil, err
		}
		if string(data) != minimalREADME {
			violations["README.md must match the exact minimal README"] = struct{}{}
		}
	} else {
		violations["README.md must be a regular file with the exact minimal content"] = struct{}{}
	}

	result := make([]string, 0, len(violations))
	for violation := range violations {
		result = append(result, violation)
	}
	sort.Strings(result)
	return result, nil
}

func inspectGoBoundary(root string) (apiManifest, []string, error) {
	moduleData, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return apiManifest{}, nil, fmt.Errorf("read module identity: %w", err)
	}
	modulePath := modfile.ModulePath(moduleData)
	if modulePath == "" {
		return apiManifest{}, nil, fmt.Errorf("read module identity: go.mod has no module directive")
	}

	fset := token.NewFileSet()
	packages := make(map[string]*packageInventory)
	var violations []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch {
			case entry.Name() == ".git", entry.Name() == "vendor", strings.EqualFold(entry.Name(), "docs"):
				return filepath.SkipDir
			default:
				return nil
			}
		}
		if entry.Type()&os.ModeSymlink != 0 || filepath.Ext(path) != ".go" {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			violations = append(violations, fmt.Sprintf("%s: parse error: %v", rel, err))
			return nil
		}

		internal := hasPathSegment(rel, "internal")
		testFile := strings.HasSuffix(rel, "_test.go")
		if !testFile {
			checkProductionComments(fset, rel, file, &violations)
		}
		if !internal && !testFile {
			directory := filepath.ToSlash(filepath.Dir(rel))
			allowedName, allowed := allowedPublicPackages[directory]
			if !allowed || file.Name.Name != allowedName {
				violations = append(violations, fmt.Sprintf("%s: unexpected public package %s", rel, file.Name.Name))
			} else {
				importPath := modulePath
				if directory != "." {
					importPath += "/" + directory
				}
				inventory := packages[importPath]
				if inventory == nil {
					inventory = &packageInventory{
						constants: make(map[string]constantDeclaration),
						exports:   make(map[manifestExport]struct{}),
						name:      file.Name.Name,
						types:     make(map[string]typeDeclaration),
					}
					packages[importPath] = inventory
				}
				collectExports(file, inventory.exports)
				collectDeclarations(rel, file, inventory)
			}
		}

		for _, imp := range file.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			if !internal && strings.Contains(importPath, "github.com/enbility") {
				violations = append(violations, at(fset, imp.Pos(), rel, "direct enbility imports are allowed only under internal/"))
			}
			if !internal && !testFile && strings.HasPrefix(importPath, modulePath+"/internal/") {
				violations = append(violations, at(fset, imp.Pos(), rel, "public API packages must not import internal implementation types"))
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
		if !testFile {
			checkCrossRuntimeStrings(fset, rel, file, &violations)
		}
		for _, decl := range file.Decls {
			switch declaration := decl.(type) {
			case *ast.FuncDecl:
				if declaration.Name != nil {
					checkExportedName(fset, rel, declaration.Name, &violations)
					if declaration.Name.IsExported() {
						checkExportedTypeSurface(fset, rel, declaration.Type, &violations)
					}
				}
			case *ast.GenDecl:
				for _, spec := range declaration.Specs {
					switch typed := spec.(type) {
					case *ast.TypeSpec:
						checkExportedName(fset, rel, typed.Name, &violations)
						if typed.Name.IsExported() {
							checkExportedTypeSurface(fset, rel, typed.Type, &violations)
						}
					case *ast.ValueSpec:
						for _, name := range typed.Names {
							checkExportedName(fset, rel, name, &violations)
						}
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return apiManifest{}, nil, err
	}

	stableContracts, stableViolations := inspectStableContracts(modulePath, packages)
	violations = append(violations, stableViolations...)
	manifest := apiManifest{
		Module:          modulePath,
		Packages:        make([]manifestPackage, 0, len(packages)),
		Schema:          "helianthus.api-boundary-manifest",
		StableContracts: stableContracts,
		Version:         1,
	}
	for importPath, inventory := range packages {
		pkg := manifestPackage{
			Exports:    make([]manifestExport, 0, len(inventory.exports)),
			ImportPath: importPath,
			Name:       inventory.name,
		}
		for exported := range inventory.exports {
			pkg.Exports = append(pkg.Exports, exported)
		}
		sort.Slice(pkg.Exports, func(i, j int) bool {
			if pkg.Exports[i].Kind != pkg.Exports[j].Kind {
				return pkg.Exports[i].Kind < pkg.Exports[j].Kind
			}
			return pkg.Exports[i].Name < pkg.Exports[j].Name
		})
		manifest.Packages = append(manifest.Packages, pkg)
	}
	sort.Slice(manifest.Packages, func(i, j int) bool {
		return manifest.Packages[i].ImportPath < manifest.Packages[j].ImportPath
	})
	return manifest, violations, nil
}

func checkProductionComments(fset *token.FileSet, rel string, file *ast.File, violations *[]string) {
	for _, group := range file.Comments {
		if group != file.Doc {
			*violations = append(*violations, at(fset, group.Pos(), rel, "production Go comment is not allowed"))
			continue
		}
		prefix := "// Package " + file.Name.Name + " "
		valid := len(group.List) == 1 && strings.HasPrefix(group.List[0].Text, prefix) && len(group.List[0].Text) <= 120
		if valid {
			valid = strings.TrimSpace(strings.TrimPrefix(group.List[0].Text, prefix)) != ""
		}
		if !valid {
			*violations = append(*violations, at(fset, group.Pos(), rel, "package comment must be one concise line starting with Package "+file.Name.Name))
		}
	}
}

func collectExports(file *ast.File, exports map[manifestExport]struct{}) {
	for _, decl := range file.Decls {
		switch declaration := decl.(type) {
		case *ast.FuncDecl:
			if declaration.Name == nil || !declaration.Name.IsExported() {
				continue
			}
			name := declaration.Name.Name
			if declaration.Recv != nil && len(declaration.Recv.List) > 0 {
				if receiver := receiverName(declaration.Recv.List[0].Type); receiver != "" {
					name = receiver + "." + name
				}
			}
			exports[manifestExport{Kind: "func", Name: name}] = struct{}{}
		case *ast.GenDecl:
			kind := ""
			switch declaration.Tok {
			case token.CONST:
				kind = "const"
			case token.TYPE:
				kind = "type"
			case token.VAR:
				kind = "var"
			}
			if kind == "" {
				continue
			}
			for _, spec := range declaration.Specs {
				switch typed := spec.(type) {
				case *ast.TypeSpec:
					if typed.Name.IsExported() {
						exports[manifestExport{Kind: kind, Name: typed.Name.Name}] = struct{}{}
					}
				case *ast.ValueSpec:
					for _, name := range typed.Names {
						if name.IsExported() {
							exports[manifestExport{Kind: kind, Name: name.Name}] = struct{}{}
						}
					}
				}
			}
		}
	}
}

func collectDeclarations(rel string, file *ast.File, inventory *packageInventory) {
	imports := make(map[string]string)
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		name := filepath.Base(importPath)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		imports[name] = importPath
	}
	for _, declaration := range file.Decls {
		gen, ok := declaration.(*ast.GenDecl)
		if !ok {
			continue
		}
		switch gen.Tok {
		case token.TYPE:
			for _, rawSpec := range gen.Specs {
				spec, ok := rawSpec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				inventory.types[spec.Name.Name] = typeDeclaration{imports: imports, rel: rel, spec: spec}
			}
		case token.CONST:
			for _, rawSpec := range gen.Specs {
				spec, ok := rawSpec.(*ast.ValueSpec)
				if !ok || spec.Type == nil {
					continue
				}
				typeName, err := canonicalType(spec.Type, imports)
				if err != nil {
					continue
				}
				for i, name := range spec.Names {
					if i >= len(spec.Values) {
						continue
					}
					literal, ok := spec.Values[i].(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						continue
					}
					value, err := strconv.Unquote(literal.Value)
					if err != nil {
						continue
					}
					inventory.constants[name.Name] = constantDeclaration{name: name.Name, rel: rel, typeName: typeName, value: value}
				}
			}
		}
	}
}

func inspectStableContracts(modulePath string, packages map[string]*packageInventory) ([]manifestStableContract, []string) {
	specs := stableContractSpecs(modulePath)
	active := false
	for _, spec := range specs {
		if inventory := packages[spec.importPath]; inventory != nil {
			if _, ok := inventory.types[spec.root]; ok {
				active = true
				break
			}
		}
	}
	if !active {
		return []manifestStableContract{}, nil
	}

	contracts := make([]manifestStableContract, 0, len(specs))
	var violations []string
	for _, spec := range specs {
		contract := manifestStableContract{
			Enums:      make([]manifestStableEnum, 0, len(spec.enums)),
			ImportPath: spec.importPath,
			Types:      append([]manifestStableType(nil), spec.types...),
		}
		inventory := packages[spec.importPath]
		if inventory == nil {
			violations = append(violations, "stable contract package is missing: "+spec.importPath)
			contracts = append(contracts, contract)
			continue
		}

		expectedTypes := make(map[string]manifestStableType, len(spec.types))
		for _, expected := range spec.types {
			expectedTypes[expected.Name] = expected
			declaration, ok := inventory.types[expected.Name]
			if !ok {
				violations = append(violations, fmt.Sprintf("%s: stable contract type %s is missing", spec.importPath, expected.Name))
				continue
			}
			actual, err := inspectStableType(declaration)
			if err != nil {
				violations = append(violations, fmt.Sprintf("%s: stable contract type %s: %v", declaration.rel, expected.Name, err))
				continue
			}
			if !reflect.DeepEqual(actual, expected) {
				violations = append(violations, fmt.Sprintf("%s: stable contract type %s does not match exact field/type/tag manifest: got %s want %s", declaration.rel, expected.Name, stableTypeText(actual), stableTypeText(expected)))
			}
		}
		for name := range inventory.types {
			if ast.IsExported(name) && strings.HasSuffix(name, "V1") {
				if _, ok := expectedTypes[name]; !ok {
					violations = append(violations, fmt.Sprintf("%s: unexpected stable contract type %s", spec.importPath, name))
				}
			}
		}

		for _, enumSpec := range spec.enums {
			enum := manifestStableEnum{Type: enumSpec.typeName, Values: append([]manifestStableEnumValue(nil), enumSpec.values...)}
			contract.Enums = append(contract.Enums, enum)
			expectedNames := make(map[string]manifestStableEnumValue, len(enumSpec.values))
			for _, expected := range enumSpec.values {
				expectedNames[expected.Name] = expected
				actual, ok := inventory.constants[expected.Name]
				if !ok {
					violations = append(violations, fmt.Sprintf("%s: stable enum value %s.%s is missing", spec.importPath, enumSpec.typeName, expected.Name))
					continue
				}
				if actual.typeName != enumSpec.typeName || actual.value != expected.Value {
					violations = append(violations, fmt.Sprintf("%s: stable enum value %s.%s does not match exact manifest", actual.rel, enumSpec.typeName, expected.Name))
				}
			}
			if enumSpec.exact {
				for name, actual := range inventory.constants {
					if actual.typeName != enumSpec.typeName {
						continue
					}
					if _, ok := expectedNames[name]; !ok {
						violations = append(violations, fmt.Sprintf("%s: stable enum %s has unexpected value %s", actual.rel, enumSpec.typeName, name))
					}
				}
			}
		}
		sort.Slice(contract.Types, func(i, j int) bool { return contract.Types[i].Name < contract.Types[j].Name })
		sort.Slice(contract.Enums, func(i, j int) bool { return contract.Enums[i].Type < contract.Enums[j].Type })
		for i := range contract.Enums {
			sort.Slice(contract.Enums[i].Values, func(left, right int) bool {
				return contract.Enums[i].Values[left].Name < contract.Enums[i].Values[right].Name
			})
		}
		contracts = append(contracts, contract)
	}
	sort.Slice(contracts, func(i, j int) bool { return contracts[i].ImportPath < contracts[j].ImportPath })
	return contracts, violations
}

func inspectStableType(declaration typeDeclaration) (manifestStableType, error) {
	if declaration.spec.Assign.IsValid() {
		return manifestStableType{}, errors.New("type aliases are not allowed in the stable contract")
	}
	result := manifestStableType{Name: declaration.spec.Name.Name}
	if structure, ok := declaration.spec.Type.(*ast.StructType); ok {
		result.Underlying = "struct"
		result.Fields = make([]manifestStableField, 0, len(structure.Fields.List))
		for _, field := range structure.Fields.List {
			if len(field.Names) != 1 {
				return manifestStableType{}, errors.New("stable struct fields must have exactly one explicit name")
			}
			typeName, err := canonicalType(field.Type, declaration.imports)
			if err != nil {
				return manifestStableType{}, err
			}
			jsonTag := ""
			if field.Tag != nil {
				jsonTag, err = strconv.Unquote(field.Tag.Value)
				if err != nil {
					return manifestStableType{}, fmt.Errorf("invalid field tag on %s: %w", field.Names[0].Name, err)
				}
			}
			result.Fields = append(result.Fields, manifestStableField{JSONTag: jsonTag, Name: field.Names[0].Name, Type: typeName})
		}
		return result, nil
	}
	underlying, err := canonicalType(declaration.spec.Type, declaration.imports)
	if err != nil {
		return manifestStableType{}, err
	}
	result.Underlying = underlying
	return result, nil
}

func canonicalType(expr ast.Expr, imports map[string]string) (string, error) {
	switch typed := expr.(type) {
	case *ast.Ident:
		return typed.Name, nil
	case *ast.SelectorExpr:
		qualifier, ok := typed.X.(*ast.Ident)
		if !ok {
			return "", errors.New("unsupported qualified stable field type")
		}
		importPath, ok := imports[qualifier.Name]
		if !ok {
			return "", fmt.Errorf("unresolved stable field type qualifier %s", qualifier.Name)
		}
		return importPath + "." + typed.Sel.Name, nil
	case *ast.ArrayType:
		if typed.Len != nil {
			return "", errors.New("fixed arrays are not allowed in the stable contract")
		}
		element, err := canonicalType(typed.Elt, imports)
		if err != nil {
			return "", err
		}
		return "[]" + element, nil
	case *ast.StarExpr:
		element, err := canonicalType(typed.X, imports)
		if err != nil {
			return "", err
		}
		return "*" + element, nil
	default:
		return "", fmt.Errorf("unsupported stable field type %T", expr)
	}
}

func stableTypeText(value manifestStableType) string {
	data, err := json.Marshal(value)
	if err != nil {
		return value.Name
	}
	return string(data)
}

func stableContractSpecs(modulePath string) []stableContractSpec {
	rawPath := modulePath + "/eebusraw"
	evidencePath := modulePath + "/eebusevidence"
	field := func(name, typeName, jsonTag string) manifestStableField {
		return manifestStableField{Name: name, Type: typeName, JSONTag: jsonTag}
	}
	stableType := func(name, underlying string, fields ...manifestStableField) manifestStableType {
		return manifestStableType{Name: name, Underlying: underlying, Fields: fields}
	}
	enumValue := func(name, value string) manifestStableEnumValue {
		return manifestStableEnumValue{Name: name, Value: value}
	}
	return []stableContractSpec{
		{
			importPath: rawPath,
			root:       "IdentityDocumentV1",
			types: []manifestStableType{
				stableType("ContractVersion", "string"),
				stableType("MaskTier", "string"),
				stableType("EndpointRole", "string"),
				stableType("IDKind", "string"),
				stableType("RedactedID", "struct",
					field("Kind", "IDKind", `json:"kind"`),
					field("Masked", "string", `json:"masked"`),
					field("Digest", "string", `json:"digest,omitempty"`),
				),
				stableType("UnknownPath", "string"),
				stableType("UnknownField", "struct",
					field("Path", "UnknownPath", `json:"path"`),
					field("Value", "OpaqueValue", `json:"value"`),
				),
				stableType("OpaqueValue", "struct",
					field("Masked", "string", `json:"masked"`),
					field("Digest", "string", `json:"digest,omitempty"`),
					field("Size", "int", `json:"size,omitempty"`),
				),
				stableType("EndpointIdentityV1", "struct",
					field("Role", "EndpointRole", `json:"role"`),
					field("ID", "RedactedID", `json:"id"`),
					field("Unknown", "[]UnknownField", `json:"unknown,omitempty"`),
				),
				stableType("SessionIdentityV1", "struct",
					field("ID", "RedactedID", `json:"id"`),
					field("RemoteID", "RedactedID", `json:"remote_id"`),
					field("Unknown", "[]UnknownField", `json:"unknown,omitempty"`),
				),
				stableType("IdentityDocumentV1", "struct",
					field("Contract", "ContractVersion", `json:"contract"`),
					field("MaskTier", "MaskTier", `json:"mask_tier"`),
					field("CapturedAt", "time.Time", `json:"captured_at"`),
					field("Local", "EndpointIdentityV1", `json:"local"`),
					field("Remotes", "[]EndpointIdentityV1", `json:"remotes,omitempty"`),
					field("Sessions", "[]SessionIdentityV1", `json:"sessions,omitempty"`),
					field("Unknown", "[]UnknownField", `json:"unknown,omitempty"`),
				),
			},
			enums: []stableEnumSpec{
				{typeName: "ContractVersion", values: []manifestStableEnumValue{enumValue("IdentityContractV1", "helianthus.eebus.raw.identity.v1")}},
				{exact: true, typeName: "MaskTier", values: []manifestStableEnumValue{enumValue("MaskTierRedacted", "redacted")}},
				{exact: true, typeName: "EndpointRole", values: []manifestStableEnumValue{
					enumValue("EndpointRoleLocal", "local"),
					enumValue("EndpointRoleRemote", "remote"),
				}},
				{exact: true, typeName: "IDKind", values: []manifestStableEnumValue{
					enumValue("IDKindLocalSKI", "local-ski"),
					enumValue("IDKindRemoteSKI", "remote-ski"),
					enumValue("IDKindCertificateFingerprint", "certificate-fingerprint"),
					enumValue("IDKindPeer", "peer"),
					enumValue("IDKindSession", "session"),
				}},
				{exact: true, typeName: "UnknownPath", values: []manifestStableEnumValue{
					enumValue("UnknownPathDocument", "/document/unknown"),
					enumValue("UnknownPathDevice", "/device/unknown"),
					enumValue("UnknownPathLocal", "/local/unknown"),
					enumValue("UnknownPathRemote", "/remote/unknown"),
					enumValue("UnknownPathSession", "/session/unknown"),
				}},
			},
		},
		{
			importPath: evidencePath,
			root:       "EnvelopeV1",
			types: []manifestStableType{
				stableType("ContractVersion", "string"),
				stableType("CaptureProvenanceV1", "string"),
				stableType("RawSnapshotScopeV1", "string"),
				stableType("AuthScope", "string"),
				stableType("ObjectKind", "string"),
				stableType("ReferenceV1", "struct",
					field("Runtime", rawPath+".RedactedID", `json:"runtime"`),
					field("Contract", "ContractVersion", `json:"contract"`),
					field("CaptureProvenance", "CaptureProvenanceV1", `json:"capture_provenance"`),
					field("Scope", "RawSnapshotScopeV1", `json:"scope"`),
					field("MaskTier", rawPath+".MaskTier", `json:"mask_tier"`),
					field("AuthScope", "AuthScope", `json:"auth_scope"`),
				),
				stableType("ObjectV1", "struct",
					field("Kind", "ObjectKind", `json:"kind"`),
					field("Digest", "string", `json:"digest"`),
					field("Size", "int", `json:"size"`),
					field("DataTimestamp", "time.Time", `json:"data_timestamp"`),
					field("Unknown", "[]"+rawPath+".UnknownField", `json:"unknown,omitempty"`),
				),
				stableType("EnvelopeV1", "struct",
					field("Reference", "ReferenceV1", `json:"ref"`),
					field("CapturedAt", "time.Time", `json:"captured_at"`),
					field("DataTimestamp", "time.Time", `json:"data_timestamp"`),
					field("Objects", "[]ObjectV1", `json:"objects,omitempty"`),
					field("DataHash", "string", `json:"data_hash,omitempty"`),
				),
			},
			enums: []stableEnumSpec{
				{typeName: "ContractVersion", values: []manifestStableEnumValue{enumValue("EnvelopeContractV1", "helianthus.eebus.raw.evidence-envelope.v1")}},
				{exact: true, typeName: "CaptureProvenanceV1", values: []manifestStableEnumValue{
					enumValue("CaptureProvenanceRuntimeObservation", "runtime-observation"),
				}},
				{exact: true, typeName: "RawSnapshotScopeV1", values: []manifestStableEnumValue{
					enumValue("RawSnapshotScopeRoot", "raw-root"),
					enumValue("RawSnapshotScopeIdentity", "raw-identity"),
					enumValue("RawSnapshotScopeTopology", "raw-topology"),
					enumValue("RawSnapshotScopeServices", "raw-services"),
					enumValue("RawSnapshotScopeSessions", "raw-sessions"),
					enumValue("RawSnapshotScopeUnknown", "raw-unknown"),
				}},
				{exact: true, typeName: "AuthScope", values: []manifestStableEnumValue{enumValue("AuthScopeReadRaw", "eebus.raw.read")}},
				{exact: true, typeName: "ObjectKind", values: []manifestStableEnumValue{
					enumValue("ObjectKindIdentity", "identity"),
					enumValue("ObjectKindTopology", "topology"),
					enumValue("ObjectKindService", "service"),
					enumValue("ObjectKindSession", "session"),
					enumValue("ObjectKindUnknown", "unknown"),
				}},
			},
		},
	}
}

func receiverName(expr ast.Expr) string {
	switch typed := expr.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.ParenExpr:
		return receiverName(typed.X)
	case *ast.StarExpr:
		return receiverName(typed.X)
	case *ast.IndexExpr:
		return receiverName(typed.X)
	case *ast.IndexListExpr:
		return receiverName(typed.X)
	default:
		return ""
	}
}

func safeManifestDestination(root, requested string) (string, error) {
	if !filepath.IsAbs(requested) {
		return "", fmt.Errorf("API boundary artifact destination must be an absolute path outside the repository")
	}
	requested = filepath.Clean(requested)
	if info, err := os.Lstat(requested); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("API boundary artifact destination must be outside the repository and must not be a symlink")
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("API boundary artifact destination must be a regular path outside the repository")
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect API boundary artifact destination: %w", err)
	}

	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve repository root for API boundary artifact: %w", err)
	}
	parent := filepath.Dir(requested)
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", fmt.Errorf("API boundary artifact parent must exist outside the repository: %w", err)
	}
	info, err := os.Stat(resolvedParent)
	if err != nil {
		return "", fmt.Errorf("inspect API boundary artifact parent: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("API boundary artifact parent must be a directory outside the repository")
	}
	resolvedDestination := filepath.Join(resolvedParent, filepath.Base(requested))
	inside, err := pathWithin(resolvedRoot, resolvedDestination)
	if err != nil {
		return "", err
	}
	if inside {
		return "", fmt.Errorf("API boundary artifact destination must be outside the repository")
	}
	return resolvedDestination, nil
}

func writeAtomic(destination string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(destination), ".api-boundary-*.tmp")
	if err != nil {
		return err
	}
	temporary := file.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(temporary)
		}
	}()
	if err := file.Chmod(0o644); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if info, err := os.Lstat(destination); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to replace symlink destination")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(temporary, destination); err != nil {
		return err
	}
	keep = true
	return nil
}

func gitNullList(root string, args ...string) ([]string, error) {
	command := exec.Command("git", args...)
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return splitNull(output), nil
}

func gitStagedSymlinks(root string) (map[string]string, error) {
	records, err := gitNullList(root, "ls-files", "--stage", "-z")
	if err != nil {
		return nil, err
	}
	result := make(map[string]string)
	for _, record := range records {
		tab := strings.IndexByte(record, '\t')
		if tab < 0 {
			return nil, fmt.Errorf("parse git staged path record %q", record)
		}
		metadata := strings.Fields(record[:tab])
		if len(metadata) != 3 || metadata[0] != "120000" {
			continue
		}
		command := exec.Command("git", "cat-file", "blob", metadata[1])
		command.Dir = root
		target, err := command.Output()
		if err != nil {
			return nil, fmt.Errorf("inspect tracked symlink %s: %w", record[tab+1:], err)
		}
		result[filepath.ToSlash(record[tab+1:])] = string(target)
	}
	return result, nil
}

func splitNull(data []byte) []string {
	parts := strings.Split(string(data), "\x00")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

func addPathOrigin(paths map[string]map[pathOrigin]struct{}, path string, origin pathOrigin) {
	if paths[path] == nil {
		paths[path] = make(map[pathOrigin]struct{})
	}
	paths[path][origin] = struct{}{}
}

func validateRelativePath(path string) error {
	if filepath.IsAbs(filepath.FromSlash(path)) {
		return fmt.Errorf("absolute paths are forbidden")
	}
	for _, part := range strings.Split(path, "/") {
		if part == ".." {
			return fmt.Errorf("path traversal is forbidden")
		}
	}
	return nil
}

func symlinkViolation(path, target string) string {
	targetKind := "relative"
	if filepath.IsAbs(target) {
		targetKind = "absolute"
	} else {
		for _, part := range strings.Split(filepath.ToSlash(target), "/") {
			if part == ".." {
				targetKind = "traversal"
				break
			}
		}
	}
	return fmt.Sprintf("symlink is forbidden: %s has %s target %q", path, targetKind, target)
}

func hasFoldedPathSegment(path, segment string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if strings.EqualFold(part, segment) {
			return true
		}
	}
	return false
}

func isDocumentationPath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	extension := strings.ToLower(filepath.Ext(base))
	if _, ok := documentationExtensions[extension]; ok {
		return true
	}
	if extension != "" {
		return false
	}
	name := base
	_, ok := documentationNames[name]
	return ok
}

func pathWithin(root, candidate string) (bool, error) {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false, fmt.Errorf("compare API boundary artifact destination with repository: %w", err)
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))), nil
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

func checkExportedTypeSurface(fset *token.FileSet, rel string, node ast.Node, violations *[]string) {
	ast.Inspect(node, func(current ast.Node) bool {
		ident, ok := current.(*ast.Ident)
		if ok {
			checkExportedName(fset, rel, ident, violations)
		}
		return true
	})
}

func checkCrossRuntimeStrings(fset *token.FileSet, rel string, file *ast.File, violations *[]string) {
	ast.Inspect(file, func(node ast.Node) bool {
		literal, ok := node.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(literal.Value)
		if err == nil && strings.HasPrefix(strings.TrimSpace(value), "ebus.v1.") {
			*violations = append(*violations, at(fset, literal.Pos(), rel, "public API exposes forbidden eBUS runtime identifier"))
		}
		return true
	})
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
