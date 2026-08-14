package eebusruntime_test

import (
	"reflect"
	"testing"

	eebusruntime "github.com/Project-Helianthus/helianthus-eebusreg"
)

func TestRuntimeHoldersCannotRecoverCreationOnlyAdminV1(t *testing.T) {
	instance, err := eebusruntime.New(eebusruntime.Config{})
	if err != nil {
		t.Fatal(err)
	}

	adminType := reflect.TypeOf((*eebusruntime.AdminV1)(nil)).Elem()
	assertNoAdminV1RecoveryPath(
		t,
		"public Runtime",
		reflect.TypeOf((*eebusruntime.Runtime)(nil)).Elem(),
		adminType,
	)
	assertNoAdminV1RecoveryPath(t, "concrete runtime", reflect.TypeOf(instance), adminType)

	if _, recovered := any(instance).(interface {
		AdminV1() eebusruntime.AdminV1
	}); recovered {
		t.Fatal("New(Config) runtime recovered AdminV1 through an accessor type assertion")
	}
}

func assertNoAdminV1RecoveryPath(t *testing.T, label string, root, admin reflect.Type) {
	t.Helper()
	if path, reachable := adminV1RecoveryPath(root, admin, map[reflect.Type]bool{}); reachable {
		t.Fatalf("%s exposes creation-only AdminV1 through %s", label, path)
	}
}

func adminV1RecoveryPath(root, admin reflect.Type, seen map[reflect.Type]bool) (string, bool) {
	if root == nil || seen[root] {
		return "", false
	}
	seen[root] = true
	if root == admin || root.Implements(admin) {
		return root.String(), true
	}
	for index := 0; index < root.NumMethod(); index++ {
		method := root.Method(index)
		for output := 0; output < method.Type.NumOut(); output++ {
			result := method.Type.Out(output)
			if nested, reachable := adminV1RecoveryPath(result, admin, seen); reachable {
				return method.Name + " -> " + nested, true
			}
		}
	}
	return "", false
}
