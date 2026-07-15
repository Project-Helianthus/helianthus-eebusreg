package eebusfacade

import (
	"go/ast"
	"strings"
	"testing"
)

func TestMSP055RuntimeImplementationIsOwnedByInternalFacade(t *testing.T) {
	for _, file := range parseImplementationFiles(t) {
		for _, imported := range file.Imports {
			if strings.Trim(imported.Path.Value, `"`) == "github.com/enbility/eebus-go/service" {
				return
			}
		}
	}
	t.Fatal("internal facade does not own the eebus-go runtime service")
}

func TestMSP055RuntimeWiresScopeAdmissionAndGraphReduction(t *testing.T) {
	required := map[string]bool{
		"validateRuntimeScope":         false,
		"runtimeRemoteAdmitted":        false,
		"newRuntimeObservationReducer": false,
	}
	for _, file := range parseImplementationFiles(t) {
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			identifier, ok := call.Fun.(*ast.Ident)
			if ok {
				if _, tracked := required[identifier.Name]; tracked {
					required[identifier.Name] = true
				}
			}
			return true
		})
	}
	for _, name := range []string{"validateRuntimeScope", "runtimeRemoteAdmitted", "newRuntimeObservationReducer"} {
		if !required[name] {
			t.Errorf("internal runtime does not call %s", name)
		}
	}
}

func TestRuntimeScopeRequiresExplicitInterfaceAndManualEndpoint(t *testing.T) {
	if err := validateRuntimeScope("fixture-interface", "192.0.2.21", 4712); err != nil {
		t.Fatalf("explicit runtime scope rejected: %v", err)
	}

	tests := []struct {
		name          string
		interfaceName string
		host          string
		port          int
	}{
		{name: "missing interface", host: "192.0.2.21", port: 4712},
		{name: "star interface", interfaceName: "*", host: "192.0.2.21", port: 4712},
		{name: "ipv4 wildcard interface", interfaceName: "0.0.0.0", host: "192.0.2.21", port: 4712},
		{name: "ipv6 wildcard interface", interfaceName: "::", host: "192.0.2.21", port: 4712},
		{name: "missing endpoint", interfaceName: "fixture-interface", port: 4712},
		{name: "ipv4 wildcard endpoint", interfaceName: "fixture-interface", host: "0.0.0.0", port: 4712},
		{name: "ipv6 wildcard endpoint", interfaceName: "fixture-interface", host: "::", port: 4712},
		{name: "missing endpoint port", interfaceName: "fixture-interface", host: "192.0.2.21"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateRuntimeScope(test.interfaceName, test.host, test.port); err == nil {
				t.Fatal("runtime scope accepted an implicit or wildcard endpoint")
			}
		})
	}
}

func TestRuntimeAdmissionRequiresPretrustOrExplicitAllowlist(t *testing.T) {
	tests := []struct {
		name        string
		pretrusted  bool
		allowlisted bool
		want        bool
	}{
		{name: "neither", want: false},
		{name: "pretrusted", pretrusted: true, want: true},
		{name: "allowlisted", allowlisted: true, want: true},
		{name: "both", pretrusted: true, allowlisted: true, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := runtimeRemoteAdmitted(test.pretrusted, test.allowlisted); got != test.want {
				t.Fatalf("runtime admission = %t, want %t", got, test.want)
			}
		})
	}
}

func TestRuntimeReducerReconnectReplacesSessionAndDeduplicatesFeatureGraph(t *testing.T) {
	reducer := newRuntimeObservationReducer()
	first := runtimeGraphObservation{
		RuntimeID:  "fixture-runtime",
		LocalSKI:   "fixture-local",
		RemoteSKI:  "fixture-remote",
		SessionID:  "fixture-session-old",
		ServiceIDs: []string{"fixture-service"},
		Devices: []runtimeDeviceObservation{{
			ID: "fixture-device",
			Entities: []runtimeEntityObservation{{
				ID: "fixture-entity",
				Features: []runtimeFeatureObservation{{
					ID:   "fixture-feature-a",
					Role: "client",
				}},
			}},
			UseCaseIDs: []string{"fixture-usecase-a"},
		}},
	}
	if err := reducer.Replace(first); err != nil {
		t.Fatal(err)
	}

	device := runtimeDeviceObservation{
		ID: "fixture-device",
		Entities: []runtimeEntityObservation{
			{
				ID: "fixture-entity",
				Features: []runtimeFeatureObservation{
					{ID: "fixture-feature-a", Role: "client"},
					{ID: "fixture-feature-a", Role: "client"},
					{ID: "fixture-feature-b", Role: "server"},
				},
			},
			{
				ID: "fixture-entity",
				Features: []runtimeFeatureObservation{
					{ID: "fixture-feature-b", Role: "server"},
				},
			},
		},
		UseCaseIDs: []string{"fixture-usecase-a", "fixture-usecase-a", "fixture-usecase-b"},
	}
	reconnect := runtimeGraphObservation{
		RuntimeID:  first.RuntimeID,
		LocalSKI:   first.LocalSKI,
		RemoteSKI:  first.RemoteSKI,
		SessionID:  "fixture-session-new",
		ServiceIDs: []string{"fixture-service", "fixture-service"},
		Devices:    []runtimeDeviceObservation{device, device},
	}
	if err := reducer.Replace(reconnect); err != nil {
		t.Fatal(err)
	}

	graph := reducer.Snapshot()
	if len(graph) != 1 {
		t.Fatalf("remote graph count = %d, want 1", len(graph))
	}
	remote := graph[0]
	if remote.SessionID != reconnect.SessionID {
		t.Fatal("reconnect retained the superseded session")
	}
	if len(remote.ServiceIDs) != 1 || len(remote.Devices) != 1 {
		t.Fatalf("reconnect counts services=%d devices=%d, want 1/1", len(remote.ServiceIDs), len(remote.Devices))
	}
	if len(remote.Devices[0].Entities) != 1 || len(remote.Devices[0].UseCaseIDs) != 2 {
		t.Fatalf("device counts entities=%d usecases=%d, want 1/2", len(remote.Devices[0].Entities), len(remote.Devices[0].UseCaseIDs))
	}
	if len(remote.Devices[0].Entities[0].Features) != 2 {
		t.Fatalf("feature count = %d, want 2", len(remote.Devices[0].Entities[0].Features))
	}

	mismatched := reconnect
	mismatched.LocalSKI = "fixture-local-mismatch"
	if err := reducer.Replace(mismatched); err == nil {
		t.Fatal("reducer accepted a reconnect with a different persisted local identity")
	}
	if got := reducer.Snapshot()[0].SessionID; got != reconnect.SessionID {
		t.Fatal("rejected identity mismatch mutated the current session")
	}
}
