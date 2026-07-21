package eebusinteropsmoke

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"strconv"
	"testing"
)

func TestFakePeerUsesOneInboundServiceAndOneTestClientWithoutMDNS(t *testing.T) {
	const fixture = "fake_peer_test.go"
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), fixture, data, 0)
	if err != nil {
		t.Fatal(err)
	}
	imports := make(map[string]string, len(file.Imports))
	for _, imp := range file.Imports {
		importPath, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			t.Fatal(err)
		}
		alias := path.Base(importPath)
		if imp.Name != nil {
			alias = imp.Name.Name
		}
		imports[alias] = importPath
		for _, forbidden := range []string{
			"github.com/Project-Helianthus/helianthus-eebus-go/service",
			"github.com/Project-Helianthus/helianthus-ship-go/hub",
			"github.com/Project-Helianthus/helianthus-ship-go/mdns",
			"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusfacade",
		} {
			if importPath == forbidden {
				t.Fatalf("%s imports forbidden responder/publisher path: %s", fixture, importPath)
			}
		}
	}
	serviceConstructors := 0
	clientConstructors := 0
	serverRoles := 0
	ast.Inspect(file, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		qualifier, ok := selector.X.(*ast.Ident)
		if !ok {
			return true
		}
		switch imports[qualifier.Name] {
		case "github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusservicebridge":
			if selector.Sel.Name == "NewServiceWithOptions" {
				serviceConstructors++
			}
		case "github.com/Project-Helianthus/helianthus-ship-go/ship":
			switch selector.Sel.Name {
			case "NewConnectionHandler":
				clientConstructors++
			case "ShipRoleServer":
				serverRoles++
			}
		}
		return true
	})
	if serviceConstructors != 1 || clientConstructors != 1 || serverRoles != 0 {
		t.Fatalf("fixture topology service_constructors=%d client_constructors=%d server_roles=%d, want 1/1/0", serviceConstructors, clientConstructors, serverRoles)
	}
}
