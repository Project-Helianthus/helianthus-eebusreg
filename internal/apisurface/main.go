// Package main emits the normalized public Go API surface for this module.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/build"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	schemaID      = "helianthus.eebus.api-surface.v1"
	schemaVersion = 1
	modulePath    = "github.com/Project-Helianthus/helianthus-eebusreg"
)

var publicPackages = []string{modulePath, modulePath + "/eebusraw", modulePath + "/eebusevidence"}

var stdlibApproval = map[string]struct{}{
	"context": {}, "fmt": {}, "time": {}, "unsafe": {},
}

var publicApproval = map[string]struct{}{
	modulePath: {}, modulePath + "/eebusraw": {}, modulePath + "/eebusevidence": {},
}

type document struct {
	SchemaID      string    `json:"schema_id"`
	SchemaVersion int       `json:"schema_version"`
	Packages      []surface `json:"packages"`
}

type surface struct {
	Path    string          `json:"path"`
	Name    string          `json:"name"`
	Imports []importSurface `json:"imports"`
	Symbols []symbol        `json:"symbols"`
}

type importSurface struct {
	DependencyKind string `json:"dependency_kind"`
	Qualifier      string `json:"qualifier"`
	Path           string `json:"path"`
}

type typeParameter struct {
	Name       string `json:"name"`
	Constraint string `json:"constraint"`
}
type receiver struct {
	Base           string   `json:"base"`
	Pointer        bool     `json:"pointer"`
	TypeParameters []string `json:"type_parameters"`
}

type aliasRHSProvider interface {
	Rhs() types.Type
}

type aliasTypeArgumentsProvider interface {
	TypeArgs() *types.TypeList
}

type aliasTypeParametersProvider interface {
	TypeParams() *types.TypeParamList
}

type symbol struct {
	Kind           string           `json:"kind"`
	Name           string           `json:"name"`
	Type           string           `json:"type"`
	Signature      string           `json:"signature"`
	ValueKind      string           `json:"value_kind,omitempty"`
	Value          string           `json:"value,omitempty"`
	TypeForm       string           `json:"type_form,omitempty"`
	TypeParameters *[]typeParameter `json:"type_parameters,omitempty"`
	Receiver       *receiver        `json:"receiver,omitempty"`
}

func main() {
	output := flag.String("output", "-", "output file, or - for stdout")
	flag.Parse()
	if flag.NArg() != 0 {
		fail(fmt.Errorf("unexpected arguments: %s", strings.Join(flag.Args(), " ")))
	}
	doc, err := extract(".")
	if err != nil {
		fail(err)
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		fail(err)
	}
	data = append(data, '\n')
	if *output == "-" {
		_, err = os.Stdout.Write(data)
	} else {
		err = os.WriteFile(*output, data, 0o644)
	}
	if err != nil {
		fail(err)
	}
}

func fail(err error) { fmt.Fprintln(os.Stderr, "apisurface:", err); os.Exit(1) }

func extract(dir string) (document, error) {
	checked := make(map[string]*checkedPackage, len(publicPackages))
	external := moduleImporter(dir)
	for _, packagePath := range []string{modulePath + "/eebusraw", modulePath + "/eebusevidence", modulePath} {
		pkg, err := checkPackage(dir, packagePath, checked, external)
		if err != nil {
			return document{}, err
		}
		checked[packagePath] = pkg
	}
	doc := document{SchemaID: schemaID, SchemaVersion: schemaVersion, Packages: make([]surface, 0, len(checked))}
	for _, packagePath := range publicPackages {
		pkg := checked[packagePath]
		s, err := extractPackage(pkg.types, pkg.files, pkg.info)
		if err != nil {
			return document{}, fmt.Errorf("%s: %w", pkg.PkgPath, err)
		}
		doc.Packages = append(doc.Packages, s)
	}
	sort.Slice(doc.Packages, func(i, j int) bool { return packageKey(doc.Packages[i]) < packageKey(doc.Packages[j]) })
	return doc, nil
}

type checkedPackage struct {
	types   *types.Package
	files   []*ast.File
	info    *types.Info
	PkgPath string
}
type publicImporter struct {
	root     string
	local    map[string]*checkedPackage
	fallback types.Importer
}

func (i *publicImporter) Import(importPath string) (*types.Package, error) {
	if pkg := i.local[importPath]; pkg != nil {
		return pkg.types, nil
	}
	if importPath == modulePath || strings.HasPrefix(importPath, modulePath+"/") {
		pkg, err := checkPackage(i.root, importPath, i.local, i.fallback)
		if err != nil {
			return nil, err
		}
		i.local[importPath] = pkg
		return pkg.types, nil
	}
	return i.fallback.Import(importPath)
}

func checkPackage(root, packagePath string, checked map[string]*checkedPackage, external types.Importer) (*checkedPackage, error) {
	dir := filepath.Join(root, strings.TrimPrefix(packagePath, modulePath))
	bp, err := build.Default.ImportDir(dir, 0)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", packagePath, err)
	}
	fset := token.NewFileSet()
	files := make([]*ast.File, 0, len(bp.GoFiles)+len(bp.CgoFiles))
	for _, name := range append(bp.GoFiles, bp.CgoFiles...) {
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.AllErrors)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", packagePath, err)
		}
		files = append(files, file)
	}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Types: map[ast.Expr]types.TypeAndValue{}}
	config := types.Config{
		Importer:    &publicImporter{root: root, local: checked, fallback: external},
		GoVersion:   "go1.22",
		FakeImportC: true,
	}
	pkg, err := config.Check(packagePath, fset, files, info)
	if err != nil {
		return nil, fmt.Errorf("type-check %s: %w", packagePath, err)
	}
	return &checkedPackage{types: pkg, files: files, info: info, PkgPath: packagePath}, nil
}

func moduleImporter(dir string) types.Importer {
	return importer.ForCompiler(token.NewFileSet(), "gc", func(importPath string) (io.ReadCloser, error) {
		command := exec.Command("go", "list", "-export", "-f", "{{.Export}}", importPath)
		command.Dir = dir
		output, err := command.Output()
		if err != nil {
			return nil, fmt.Errorf("locate export data for %s: %w", importPath, err)
		}
		return os.Open(strings.TrimSpace(string(output)))
	})
}

func extractPackage(pkg *types.Package, files []*ast.File, info *types.Info) (surface, error) {
	if pkg.Path() == "" || strings.Contains("/"+pkg.Path()+"/", "/internal/") {
		return surface{}, errors.New("invalid public package")
	}
	x := extractor{pkg: pkg, info: info, funcs: map[*types.Func]*ast.FuncDecl{}}
	for _, f := range files {
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok {
				if fn, ok := info.Defs[fd.Name].(*types.Func); ok {
					x.funcs[fn] = fd
				}
			}
		}
	}
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		if !exported(name) {
			continue
		}
		obj := scope.Lookup(name)
		switch obj := obj.(type) {
		case *types.Const:
			if err := x.addConst(obj); err != nil {
				return surface{}, err
			}
		case *types.Var:
			if err := x.addVar(obj); err != nil {
				return surface{}, err
			}
		case *types.TypeName:
			if err := x.addType(obj); err != nil {
				return surface{}, err
			}
		case *types.Func:
			if err := x.addFunc(obj); err != nil {
				return surface{}, err
			}
		}
	}
	if len(x.symbols)+len(x.pending) == 0 {
		return surface{}, errors.New("no exported symbols")
	}
	qualifiers, imports, err := x.allocateImports()
	if err != nil {
		return surface{}, err
	}
	for i := range x.pending {
		if err := x.pending[i].finish(qualifiers); err != nil {
			return surface{}, err
		}
		x.symbols = append(x.symbols, x.pending[i].symbol)
	}
	sort.Slice(x.symbols, func(i, j int) bool { return symbolKey(x.symbols[i]) < symbolKey(x.symbols[j]) })
	sort.Slice(imports, func(i, j int) bool {
		return imports[i].Qualifier+"\x00"+imports[i].Path < imports[j].Qualifier+"\x00"+imports[j].Path
	})
	return surface{Path: pkg.Path(), Name: pkg.Name(), Imports: imports, Symbols: x.symbols}, nil
}

type pendingSymbol struct {
	symbol     symbol
	roots      []types.Type
	parameters *types.TypeParamList
}
type extractor struct {
	pkg          *types.Package
	info         *types.Info
	funcs        map[*types.Func]*ast.FuncDecl
	pending      []pendingSymbol
	symbols      []symbol
	dependencies map[string]*types.Package
}

func (x *extractor) addConst(obj *types.Const) error {
	if _, err := x.renderRoot(obj.Type()); err != nil {
		return err
	}
	x.pending = append(x.pending, pendingSymbol{symbol: symbol{Kind: "const", Name: obj.Name(), ValueKind: constantKind(obj.Val()), Value: obj.Val().ExactString()}, roots: []types.Type{obj.Type()}})
	return nil
}
func (x *extractor) addVar(obj *types.Var) error {
	if _, err := x.renderRoot(obj.Type()); err != nil {
		return err
	}
	x.pending = append(x.pending, pendingSymbol{symbol: symbol{Kind: "var", Name: obj.Name()}, roots: []types.Type{obj.Type()}})
	return nil
}
func (x *extractor) addType(obj *types.TypeName) error {
	form, typ := "defined", obj.Type()
	if obj.IsAlias() {
		form = "alias"
	}
	var root types.Type
	var params *types.TypeParamList
	if a, ok := typ.(*types.Alias); ok {
		root = aliasRHS(a)
		params = aliasTypeParameters(a)
	} else if n, ok := typ.(*types.Named); ok {
		root = n.Underlying()
		params = n.TypeParams()
	} else {
		return fmt.Errorf("unsupported type declaration %s", obj.Name())
	}
	if _, err := x.renderRoot(root); err != nil {
		return err
	}
	if _, err := x.parameters(params); err != nil {
		return err
	}
	ps := []typeParameter{}
	p := pendingSymbol{symbol: symbol{Kind: "type", Name: obj.Name(), TypeForm: form, TypeParameters: &ps}, roots: []types.Type{root}, parameters: params}
	x.pending = append(x.pending, p)
	if form == "defined" {
		n := typ.(*types.Named)
		for i := 0; i < n.NumMethods(); i++ {
			m := n.Method(i)
			if exported(m.Name()) {
				if err := x.addMethod(n, m); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
func (x *extractor) addFunc(obj *types.Func) error {
	sig, ok := obj.Type().(*types.Signature)
	if !ok {
		return errors.New("function without signature")
	}
	if _, err := x.renderRoot(sig); err != nil {
		return err
	}
	if _, err := x.parameters(sig.TypeParams()); err != nil {
		return err
	}
	ps := []typeParameter{}
	x.pending = append(x.pending, pendingSymbol{symbol: symbol{Kind: "func", Name: obj.Name(), TypeParameters: &ps}, roots: []types.Type{sig}, parameters: sig.TypeParams()})
	return nil
}
func (x *extractor) addMethod(owner *types.Named, obj *types.Func) error {
	if x.funcs[obj] == nil {
		return fmt.Errorf("method %s lacks AST declaration", obj.Name())
	}
	sig, ok := obj.Type().(*types.Signature)
	if !ok {
		return errors.New("method without signature")
	}
	if _, err := x.renderRoot(sig); err != nil {
		return err
	}
	r := sig.Recv()
	if r == nil {
		return errors.New("method without receiver")
	}
	pointer := false
	rt := r.Type()
	if p, ok := rt.(*types.Pointer); ok {
		pointer = true
		rt = p.Elem()
	}
	n, ok := rt.(*types.Named)
	if !ok || n.Origin() != owner.Origin() {
		return fmt.Errorf("method %s has unresolved receiver", obj.Name())
	}
	binders := make([]string, owner.TypeParams().Len())
	for i := range binders {
		binders[i] = owner.TypeParams().At(i).Obj().Name()
		if binders[i] == "_" {
			binders[i] = fmt.Sprintf("T%d", i+1)
		}
	}
	x.pending = append(x.pending, pendingSymbol{symbol: symbol{Kind: "method", Name: obj.Name(), Receiver: &receiver{Base: owner.Obj().Name(), Pointer: pointer, TypeParameters: binders}}, roots: []types.Type{sig}})
	return nil
}

func (x *extractor) parameters(list *types.TypeParamList) ([]typeParameter, error) {
	if list == nil {
		return []typeParameter{}, nil
	}
	out := make([]typeParameter, list.Len())
	for i := 0; i < list.Len(); i++ {
		p := list.At(i)
		c, err := x.renderRoot(p.Constraint())
		if err != nil {
			return nil, err
		}
		out[i] = typeParameter{Name: p.Obj().Name(), Constraint: c}
	}
	return out, nil
}

func (x *extractor) renderRoot(t types.Type) (string, error) {
	if err := x.checkType(t, map[types.Type]bool{}); err != nil {
		return "", err
	}
	return "", nil
}
func (x *extractor) checkType(t types.Type, seen map[types.Type]bool) error {
	if t == nil {
		return errors.New("nil type")
	}
	if seen[t] {
		return nil
	}
	seen[t] = true
	switch t := t.(type) {
	case *types.Basic:
		if t.Kind() == types.UnsafePointer {
			return x.addDependency(types.NewPackage("unsafe", "unsafe"))
		}
		return nil
	case *types.TypeParam:
		return x.checkType(t.Constraint(), seen)
	case *types.Pointer:
		return x.checkType(t.Elem(), seen)
	case *types.Array:
		return x.checkType(t.Elem(), seen)
	case *types.Slice:
		return x.checkType(t.Elem(), seen)
	case *types.Map:
		if err := x.checkType(t.Key(), seen); err != nil {
			return err
		}
		return x.checkType(t.Elem(), seen)
	case *types.Chan:
		return x.checkType(t.Elem(), seen)
	case *types.Tuple:
		for i := 0; i < t.Len(); i++ {
			if err := x.checkType(t.At(i).Type(), seen); err != nil {
				return err
			}
		}
		return nil
	case *types.Struct:
		for i := 0; i < t.NumFields(); i++ {
			if err := x.checkType(t.Field(i).Type(), seen); err != nil {
				return err
			}
		}
		return nil
	case *types.Interface:
		t.Complete()
		for i := 0; i < t.NumExplicitMethods(); i++ {
			if err := x.checkType(t.ExplicitMethod(i).Type(), seen); err != nil {
				return err
			}
		}
		for i := 0; i < t.NumEmbeddeds(); i++ {
			if err := x.checkType(t.EmbeddedType(i), seen); err != nil {
				return err
			}
		}
		return nil
	case *types.Signature:
		if t.Recv() != nil {
			if err := x.checkType(t.Recv().Type(), seen); err != nil {
				return err
			}
		}
		for _, parameters := range []*types.TypeParamList{t.RecvTypeParams(), t.TypeParams()} {
			if parameters == nil {
				continue
			}
			for index := 0; index < parameters.Len(); index++ {
				if err := x.checkType(parameters.At(index).Constraint(), seen); err != nil {
					return err
				}
			}
		}
		if err := x.checkType(t.Params(), seen); err != nil {
			return err
		}
		return x.checkType(t.Results(), seen)
	case *types.Named:
		for i := 0; i < t.TypeArgs().Len(); i++ {
			if err := x.checkType(t.TypeArgs().At(i), seen); err != nil {
				return err
			}
		}
		o := t.Obj()
		if o.Pkg() == nil {
			return nil
		}
		if o.Pkg() == x.pkg {
			return x.checkType(t.Underlying(), seen)
		}
		if !o.Exported() {
			return fmt.Errorf("unexported public dependency %q.%s", o.Pkg().Path(), o.Name())
		}
		return x.addDependency(o.Pkg())
	case *types.Alias:
		arguments := aliasTypeArguments(t)
		if arguments != nil {
			for index := 0; index < arguments.Len(); index++ {
				if err := x.checkType(arguments.At(index), seen); err != nil {
					return err
				}
			}
		}
		o := t.Obj()
		if o.Pkg() == nil {
			if o.Parent() != types.Universe {
				return errors.New("package-less alias is not universe-owned")
			}
			return x.checkType(aliasRHS(t), seen)
		}
		if o.Pkg() != x.pkg {
			if !o.Exported() {
				return fmt.Errorf("unexported public dependency alias %q.%s", o.Pkg().Path(), o.Name())
			}
			if err := x.addDependency(o.Pkg()); err != nil {
				return err
			}
		}
		return x.checkType(aliasRHS(t), seen)
	default:
		return fmt.Errorf("unsupported public type %T", t)
	}
}
func (x *extractor) addDependency(pkg *types.Package) error {
	p := pkg.Path()
	if strings.Contains("/"+p+"/", "/internal/") || strings.HasPrefix(p, "github.com/enbility/") {
		return fmt.Errorf("forbidden public dependency %q", p)
	}
	if _, ok := stdlibApproval[p]; ok {
		x.ensureDependencies()[p] = pkg
		return nil
	}
	if _, ok := publicApproval[p]; ok {
		x.ensureDependencies()[p] = pkg
		return nil
	}
	return fmt.Errorf("unapproved public dependency %q", p)
}
func (x *extractor) ensureDependencies() map[string]*types.Package {
	if x.dependencies == nil {
		x.dependencies = map[string]*types.Package{}
	}
	return x.dependencies
}

func (x *extractor) allocateImports() (map[string]string, []importSurface, error) {
	q := map[string]string{}
	var paths []string
	for p := range x.dependencies {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	reserved := map[string]bool{"_": true}
	for _, n := range x.pkg.Scope().Names() {
		reserved[n] = true
	}
	for _, n := range types.Universe.Names() {
		reserved[n] = true
	}
	for _, p := range paths {
		base := x.dependencies[p].Name()
		if !identifier(base) || base == "_" {
			base = "pkg"
		}
		name := base
		for i := 2; reserved[name]; i++ {
			name = fmt.Sprintf("%s_%d", base, i)
		}
		reserved[name] = true
		q[p] = name
	}
	imports := make([]importSurface, 0, len(paths))
	for _, p := range paths {
		kind := "public_contract"
		if _, ok := stdlibApproval[p]; ok {
			kind = "standard_library"
		}
		imports = append(imports, importSurface{DependencyKind: kind, Qualifier: q[p], Path: p})
	}
	return q, imports, nil
}

func (p *pendingSymbol) finish(q map[string]string) error {
	render := func(t types.Type) (string, error) { return renderType(t, q, map[*types.TypeParam]string{}) }
	if p.parameters != nil {
		parameters := make([]typeParameter, p.parameters.Len())
		for i := range parameters {
			parameter := p.parameters.At(i)
			constraint, err := render(parameter.Constraint())
			if err != nil {
				return err
			}
			parameters[i] = typeParameter{Name: parameter.Obj().Name(), Constraint: constraint}
		}
		p.symbol.TypeParameters = &parameters
	}
	var err error
	switch p.symbol.Kind {
	case "const":
		p.symbol.Type, err = render(p.roots[0])
		if err == nil {
			p.symbol.Signature = "const " + p.symbol.Name
			if !strings.HasPrefix(p.symbol.Type, "untyped ") {
				p.symbol.Signature += " " + p.symbol.Type
			}
			p.symbol.Signature += " = " + p.symbol.Value
		}
	case "var":
		p.symbol.Type, err = render(p.roots[0])
		if err == nil {
			p.symbol.Signature = "var " + p.symbol.Name + " " + p.symbol.Type
		}
	case "type":
		p.symbol.Type, err = render(p.roots[0])
		if err == nil {
			params := renderParameters(*p.symbol.TypeParameters)
			if p.symbol.TypeForm == "alias" {
				p.symbol.Signature = "type " + p.symbol.Name + params + " = " + p.symbol.Type
			} else {
				p.symbol.Signature = "type " + p.symbol.Name + params + " " + p.symbol.Type
			}
		}
	case "func":
		p.symbol.Type, err = render(p.roots[0])
		if err == nil {
			p.symbol.Signature = strings.Replace(p.symbol.Type, "func(", "func "+p.symbol.Name+renderParameters(*p.symbol.TypeParameters)+"(", 1)
		}
	case "method":
		p.symbol.Type, err = render(p.roots[0])
		if err == nil {
			r := p.symbol.Receiver
			recv := r.Base
			if len(r.TypeParameters) > 0 {
				recv += "[" + strings.Join(r.TypeParameters, ", ") + "]"
			}
			if r.Pointer {
				recv = "*" + recv
			}
			p.symbol.Signature = strings.Replace(p.symbol.Type, "func(", "func ("+recv+") "+p.symbol.Name+"(", 1)
		}
	}
	return err
}

func renderType(t types.Type, q map[string]string, substitutions map[*types.TypeParam]string) (string, error) {
	switch t := t.(type) {
	case *types.Basic:
		if t.Kind() == types.Uint8 {
			return "uint8", nil
		}
		if t.Kind() == types.Int32 {
			return "int32", nil
		}
		if t.Kind() == types.UnsafePointer {
			return q["unsafe"] + ".Pointer", nil
		}
		return t.Name(), nil
	case *types.TypeParam:
		if s := substitutions[t]; s != "" {
			return s, nil
		}
		return t.Obj().Name(), nil
	case *types.Pointer:
		v, e := renderType(t.Elem(), q, substitutions)
		return "*" + v, e
	case *types.Array:
		v, e := renderType(t.Elem(), q, substitutions)
		return fmt.Sprintf("[%d]%s", t.Len(), v), e
	case *types.Slice:
		v, e := renderType(t.Elem(), q, substitutions)
		return "[]" + v, e
	case *types.Map:
		k, e := renderType(t.Key(), q, substitutions)
		if e != nil {
			return "", e
		}
		v, e := renderType(t.Elem(), q, substitutions)
		return "map[" + k + "]" + v, e
	case *types.Chan:
		v, e := renderType(t.Elem(), q, substitutions)
		if t.Dir() == types.SendOnly {
			return "chan<- " + v, e
		}
		if t.Dir() == types.RecvOnly {
			return "<-chan " + v, e
		}
		return "chan " + v, e
	case *types.Tuple:
		a := make([]string, t.Len())
		for i := range a {
			var e error
			a[i], e = renderType(t.At(i).Type(), q, substitutions)
			if e != nil {
				return "", e
			}
		}
		return strings.Join(a, ", "), nil
	case *types.Struct:
		a := make([]string, 0, t.NumFields())
		for i := 0; i < t.NumFields(); i++ {
			f := t.Field(i)
			v, e := renderType(f.Type(), q, substitutions)
			if e != nil {
				return "", e
			}
			field := v
			if !f.Embedded() {
				field = f.Name() + " " + v
			}
			if tag := t.Tag(i); tag != "" {
				field += " " + strconv.Quote(tag)
			}
			a = append(a, field)
		}
		if len(a) == 0 {
			return "struct{}", nil
		}
		return "struct{ " + strings.Join(a, "; ") + " }", nil
	case *types.Interface:
		t.Complete()
		a := make([]string, 0, t.NumExplicitMethods()+t.NumEmbeddeds())
		for i := 0; i < t.NumEmbeddeds(); i++ {
			v, e := renderType(t.EmbeddedType(i), q, substitutions)
			if e != nil {
				return "", e
			}
			a = append(a, v)
		}
		for i := 0; i < t.NumExplicitMethods(); i++ {
			s, e := renderSignature(t.ExplicitMethod(i).Type().(*types.Signature), q, substitutions)
			if e != nil {
				return "", e
			}
			a = append(a, t.ExplicitMethod(i).Name()+strings.TrimPrefix(s, "func"))
		}
		sort.Strings(a)
		if len(a) == 0 {
			return "interface{}", nil
		}
		return "interface{ " + strings.Join(a, "; ") + " }", nil
	case *types.Signature:
		return renderSignature(t, q, substitutions)
	case *types.Named:
		o := t.Obj()
		name := o.Name()
		if o.Pkg() != nil && o.Pkg().Path() != "" {
			if qualifier, ok := q[o.Pkg().Path()]; ok {
				name = qualifier + "." + name
			}
		}
		if t.TypeArgs().Len() > 0 {
			a := make([]string, t.TypeArgs().Len())
			for i := range a {
				var e error
				a[i], e = renderType(t.TypeArgs().At(i), q, substitutions)
				if e != nil {
					return "", e
				}
			}
			name += "[" + strings.Join(a, ", ") + "]"
		}
		return name, nil
	case *types.Alias:
		o := t.Obj()
		name := o.Name()
		if o.Pkg() != nil && o.Pkg().Path() != "" {
			if qualifier, ok := q[o.Pkg().Path()]; ok {
				name = qualifier + "." + name
			}
		}
		typeArguments := aliasTypeArguments(t)
		if typeArguments != nil && typeArguments.Len() > 0 {
			arguments := make([]string, typeArguments.Len())
			for index := range arguments {
				var err error
				arguments[index], err = renderType(typeArguments.At(index), q, substitutions)
				if err != nil {
					return "", err
				}
			}
			name += "[" + strings.Join(arguments, ", ") + "]"
		}
		return name, nil
	default:
		return "", fmt.Errorf("unsupported renderer type %T", t)
	}
}

func aliasRHS(alias *types.Alias) types.Type {
	if provider, ok := any(alias).(aliasRHSProvider); ok {
		return provider.Rhs()
	}
	return types.Unalias(alias)
}

func aliasTypeArguments(alias *types.Alias) *types.TypeList {
	if provider, ok := any(alias).(aliasTypeArgumentsProvider); ok {
		return provider.TypeArgs()
	}
	return nil
}

func aliasTypeParameters(alias *types.Alias) *types.TypeParamList {
	if provider, ok := any(alias).(aliasTypeParametersProvider); ok {
		return provider.TypeParams()
	}
	return nil
}
func renderSignature(s *types.Signature, q map[string]string, subs map[*types.TypeParam]string) (string, error) {
	p, e := renderType(s.Params(), q, subs)
	if e != nil {
		return "", e
	}
	if s.Variadic() {
		parts := strings.Split(p, ", ")
		if len(parts) == 0 {
			return "", errors.New("variadic without parameter")
		}
		parts[len(parts)-1] = "..." + strings.TrimPrefix(parts[len(parts)-1], "[]")
		p = strings.Join(parts, ", ")
	}
	r, e := renderType(s.Results(), q, subs)
	if e != nil {
		return "", e
	}
	out := "func(" + p + ")"
	if s.Results().Len() == 1 {
		out += " " + r
	} else if s.Results().Len() > 1 {
		out += " (" + r + ")"
	}
	return out, nil
}
func renderParameters(ps []typeParameter) string {
	if len(ps) == 0 {
		return ""
	}
	a := make([]string, len(ps))
	for i, p := range ps {
		a[i] = p.Name + " " + p.Constraint
	}
	return "[" + strings.Join(a, ", ") + "]"
}
func constantKind(v constant.Value) string {
	switch v.Kind() {
	case constant.Bool:
		return "bool"
	case constant.String:
		return "string"
	case constant.Int:
		return "int"
	case constant.Float:
		return "float"
	default:
		return "complex"
	}
}
func exported(name string) bool { return len(name) > 0 && name[0] >= 'A' && name[0] <= 'Z' }
func identifier(s string) bool {
	if s == "" {
		return false
	}
	if !((s[0] >= 'A' && s[0] <= 'Z') || (s[0] >= 'a' && s[0] <= 'z') || s[0] == '_') {
		return false
	}
	for _, c := range s[1:] {
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_') {
			return false
		}
	}
	return token.Lookup(s) == token.IDENT
}
func packageKey(s surface) string { return s.Path + "\x00" + s.Name }
func symbolKey(s symbol) string {
	base := ""
	if s.Receiver != nil {
		base = s.Receiver.Base
	}
	return s.Kind + "\x00" + base + "\x00" + s.Name
}
