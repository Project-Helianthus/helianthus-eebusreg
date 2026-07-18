package eebusruntime

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

var (
	_ PairingPolicy                 = PairingPolicyClosed
	_ func(Config) (Runtime, error) = New
)

func TestMSP05PInitialV1PublicShapeIsExact(t *testing.T) {
	assertMSP05PStructFields(t, reflect.TypeOf(Config{}), []msp05pField{
		{name: "Enabled", typ: reflect.TypeOf(false)},
		{name: "StateRoot", typ: reflect.TypeOf("")},
		{name: "Interface", typ: reflect.TypeOf("")},
		{name: "ListenAddress", typ: reflect.TypeOf(netip.AddrPort{})},
		{name: "DiscoveryEnabled", typ: reflect.TypeOf(false)},
		{name: "Remotes", typ: reflect.TypeOf([]Remote(nil))},
		{name: "PairingPolicy", typ: reflect.TypeOf(PairingPolicy(""))},
	})

	policyType := reflect.TypeOf(PairingPolicy(""))
	if policyType.Kind() != reflect.String || policyType.Name() != "PairingPolicy" || policyType.PkgPath() == "" {
		t.Fatalf("PairingPolicy type = %s/%s/%s, want a package-defined string", policyType.Kind(), policyType.PkgPath(), policyType.Name())
	}
	if PairingPolicyClosed != PairingPolicy("closed") {
		t.Fatalf("PairingPolicyClosed = %q, want closed", PairingPolicyClosed)
	}

	assertMSP05PStructFields(t, reflect.TypeOf(Remote{}), []msp05pField{
		{name: "SKI", typ: reflect.TypeOf("")},
	})

	runtimeType := reflect.TypeOf((*Runtime)(nil)).Elem()
	if runtimeType.NumMethod() != 4 {
		t.Fatalf("Runtime method count = %d, want frozen v1 count 4", runtimeType.NumMethod())
	}
}

func TestMSP05PNewDelegatesOnlyToTheValidatedInitialV1Seam(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var declaration *ast.FuncDecl
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, candidate := range file.Decls {
			function, ok := candidate.(*ast.FuncDecl)
			if ok && function.Recv == nil && function.Name.Name == "New" {
				if declaration != nil {
					t.Fatal("New is declared more than once")
				}
				declaration = function
			}
		}
	}
	if declaration == nil || declaration.Body == nil {
		t.Fatal("New production declaration is missing")
	}
	if len(declaration.Body.List) != 1 {
		t.Fatalf("New body has %d statements, want one validated-seam return", len(declaration.Body.List))
	}
	result, ok := declaration.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(result.Results) != 1 {
		t.Fatal("New must return the validated v1 seam directly")
	}
	call, ok := result.Results[0].(*ast.CallExpr)
	if !ok {
		t.Fatal("New return is not a constructor call")
	}
	callee, ok := call.Fun.(*ast.Ident)
	if !ok || callee.Name != "newRuntime" {
		t.Fatalf("New delegates to %T, want newRuntime", call.Fun)
	}
	if declaration.Type.Params == nil || len(declaration.Type.Params.List) != 1 || len(declaration.Type.Params.List[0].Names) != 1 {
		t.Fatal("New must name its one Config parameter for lossless delegation")
	}
	parameter := declaration.Type.Params.List[0].Names[0].Name
	if len(call.Args) != 2 {
		t.Fatalf("newRuntime argument count = %d, want config and private factory", len(call.Args))
	}
	carried, ok := call.Args[0].(*ast.Ident)
	if !ok || carried.Name != parameter {
		t.Fatal("New does not pass its Config value losslessly to newRuntime")
	}
}

func TestMSP05PInitialV1EnabledValidationIsPureAndExact(t *testing.T) {
	sandbox := t.TempDir()
	stateRoot := filepath.Join(sandbox, "runtime-state")
	valid := validCleanupRuntimeConfig(stateRoot, false, nil)

	invalid := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "missing state root", mutate: func(config *Config) { config.StateRoot = "" }},
		{name: "relative state root", mutate: func(config *Config) { config.StateRoot = "relative/state" }},
		{name: "filesystem root", mutate: func(config *Config) { config.StateRoot = string(filepath.Separator) }},
		{name: "missing interface", mutate: func(config *Config) { config.Interface = "" }},
		{name: "whitespace interface", mutate: func(config *Config) { config.Interface = "  " }},
		{name: "star interface", mutate: func(config *Config) { config.Interface = "*" }},
		{name: "ipv4 wildcard interface", mutate: func(config *Config) { config.Interface = "0.0.0.0" }},
		{name: "ipv6 wildcard interface", mutate: func(config *Config) { config.Interface = "::" }},
		{name: "bracketed ipv6 wildcard interface", mutate: func(config *Config) { config.Interface = "[::]" }},
		{name: "zero endpoint", mutate: func(config *Config) { config.ListenAddress = netip.AddrPort{} }},
		{name: "invalid address", mutate: func(config *Config) { config.ListenAddress = netip.AddrPortFrom(netip.Addr{}, 4711) }},
		{name: "zero port", mutate: func(config *Config) { config.ListenAddress = netip.MustParseAddrPort("192.0.2.10:0") }},
		{name: "unspecified ipv4", mutate: func(config *Config) { config.ListenAddress = netip.MustParseAddrPort("0.0.0.0:4711") }},
		{name: "unspecified ipv6", mutate: func(config *Config) { config.ListenAddress = netip.MustParseAddrPort("[::]:4711") }},
		{name: "ipv4 multicast", mutate: func(config *Config) { config.ListenAddress = netip.MustParseAddrPort("224.0.0.1:4711") }},
		{name: "ipv6 multicast", mutate: func(config *Config) { config.ListenAddress = netip.MustParseAddrPort("[ff02::1]:4711") }},
		{name: "ipv4 zero network", mutate: func(config *Config) { config.ListenAddress = netip.MustParseAddrPort("0.1.2.3:4711") }},
		{name: "limited broadcast", mutate: func(config *Config) { config.ListenAddress = netip.MustParseAddrPort("255.255.255.255:4711") }},
		{name: "ipv4 mapped ipv6", mutate: func(config *Config) { config.ListenAddress = netip.MustParseAddrPort("[::ffff:192.0.2.10]:4711") }},
		{name: "missing pairing policy", mutate: func(config *Config) { config.PairingPolicy = "" }},
		{name: "open pairing policy", mutate: func(config *Config) { config.PairingPolicy = PairingPolicy("open") }},
		{name: "noncanonical closed policy", mutate: func(config *Config) { config.PairingPolicy = PairingPolicy(" closed ") }},
	}

	for _, test := range invalid {
		t.Run("reject "+test.name, func(t *testing.T) {
			config := valid
			test.mutate(&config)
			assertMSP05PInitialV1RejectedWithoutConstruction(t, config)
			assertRuntimePathAbsent(t, stateRoot)
		})
	}

	validEndpoints := []netip.AddrPort{
		netip.MustParseAddrPort("192.0.2.10:4711"),
		netip.MustParseAddrPort("[2001:db8::10]:4711"),
	}
	for _, endpoint := range validEndpoints {
		for _, discovery := range []bool{false, true} {
			t.Run(endpoint.String()+"/discovery="+strconv.FormatBool(discovery), func(t *testing.T) {
				config := validCleanupRuntimeConfig(stateRoot, discovery, nil)
				config.ListenAddress = endpoint
				var acquisitions atomic.Int32
				instance, err := newRuntime(config, runtimeBackendFactory(func(context.Context, Config) (runtimeBackend, error) {
					acquisitions.Add(1)
					return newFakeRuntimeBackend(), nil
				}))
				if err != nil {
					t.Fatalf("newRuntime(valid) error = %v", err)
				}
				if got := acquisitions.Load(); got != 0 {
					t.Fatalf("constructor acquired backend %d times, want 0", got)
				}
				if err := instance.Shutdown(); err != nil {
					t.Fatal(err)
				}

				public, err := New(config)
				if err != nil {
					t.Fatalf("New(valid) error = %v", err)
				}
				if err := public.Shutdown(); err != nil {
					t.Fatal(err)
				}
				assertRuntimePathAbsent(t, stateRoot)
			})
		}
	}
	assertRuntimeDirectoryEmpty(t, sandbox)
}

func TestMSP05PInitialV1RemotesNormalizeRejectAndRemainDefensivelyCopied(t *testing.T) {
	stateRoot := filepath.Join(t.TempDir(), "runtime-state")
	valid := validCleanupRuntimeConfig(stateRoot, true, nil)
	invalid := []struct {
		name    string
		remotes []Remote
	}{
		{name: "missing SKI", remotes: []Remote{{}}},
		{name: "short SKI", remotes: []Remote{{SKI: strings.Repeat("a", 39)}}},
		{name: "non hexadecimal SKI", remotes: []Remote{{SKI: strings.Repeat("z", 40)}}},
		{name: "normalized duplicate", remotes: []Remote{
			{SKI: " AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA "},
			{SKI: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		}},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			config.Remotes = test.remotes
			assertMSP05PInitialV1RejectedWithoutConstruction(t, config)
		})
	}

	input := []Remote{
		{SKI: " AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA "},
		{SKI: "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"},
	}
	config := validCleanupRuntimeConfig(stateRoot, true, input)
	want := config
	want.Remotes = []Remote{
		{SKI: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{SKI: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	}

	firstAttempt := errors.New("fixture first acquisition failed")
	backend := newFakeRuntimeBackend()
	received := make(chan Config, 2)
	var attempts atomic.Int32
	factory := runtimeBackendFactory(func(_ context.Context, got Config) (runtimeBackend, error) {
		captured := got
		captured.Remotes = append([]Remote(nil), got.Remotes...)
		received <- captured
		if attempts.Add(1) == 1 {
			got.Remotes[0].SKI = strings.Repeat("f", 40)
			return nil, firstAttempt
		}
		return backend, nil
	})
	instance, err := newRuntime(config, factory)
	if err != nil {
		t.Fatal(err)
	}
	if got := attempts.Load(); got != 0 {
		t.Fatalf("newRuntime acquired backend %d times, want 0", got)
	}
	input[0].SKI = strings.Repeat("0", 40)
	config.Remotes[1].SKI = strings.Repeat("1", 40)

	if err := instance.Start(context.Background()); !errors.Is(err, firstAttempt) {
		t.Fatalf("first Start() error = %v, want fixture failure", err)
	}
	assertMSP05PConfigEqual(t, <-received, want)
	if err := instance.Start(context.Background()); err != nil {
		t.Fatalf("second Start() error = %v", err)
	}
	assertMSP05PConfigEqual(t, <-received, want)
	waitRuntimeSignal(t, backend.runStarted, "v1 backend Run")
	if err := instance.Shutdown(); err != nil {
		t.Fatal(err)
	}

	for _, discovery := range []bool{false, true} {
		t.Run("empty remotes/discovery="+strconv.FormatBool(discovery), func(t *testing.T) {
			backend := newFakeRuntimeBackend()
			captured := make(chan Config, 1)
			config := validCleanupRuntimeConfig(filepath.Join(t.TempDir(), "state"), discovery, []Remote{})
			instance, err := newRuntime(config, runtimeBackendFactory(func(_ context.Context, got Config) (runtimeBackend, error) {
				captured <- got
				return backend, nil
			}))
			if err != nil {
				t.Fatalf("empty remotes rejected: %v", err)
			}
			if err := instance.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			got := <-captured
			if len(got.Remotes) != 0 || got.DiscoveryEnabled != discovery {
				t.Fatalf("empty remote/discovery carry = %d/%t, want 0/%t", len(got.Remotes), got.DiscoveryEnabled, discovery)
			}
			waitRuntimeSignal(t, backend.runStarted, "empty-allowlist backend Run")
			if err := instance.Shutdown(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMSP05PInitialV1DisabledProductIsStrictlyInert(t *testing.T) {
	sandbox := t.TempDir()
	t.Setenv("HOME", sandbox)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(sandbox, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(sandbox, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(sandbox, "state"))

	var acquisitions atomic.Int32
	instance, err := newRuntime(Config{}, runtimeBackendFactory(func(context.Context, Config) (runtimeBackend, error) {
		acquisitions.Add(1)
		return newFakeRuntimeBackend(), nil
	}))
	if err != nil {
		t.Fatalf("newRuntime(zero) error = %v", err)
	}
	assertMSP05PDisabledRuntime(t, instance)
	if got := acquisitions.Load(); got != 0 {
		t.Fatalf("disabled v1 acquired backend %d times, want 0", got)
	}

	public, err := New(Config{})
	if err != nil {
		t.Fatalf("New(zero) error = %v", err)
	}
	assertMSP05PDisabledRuntime(t, public)
	assertRuntimeDirectoryEmpty(t, sandbox)

	mixed := []struct {
		name   string
		config Config
	}{
		{name: "state root", config: Config{StateRoot: filepath.Join(sandbox, "runtime-state")}},
		{name: "interface", config: Config{Interface: "en0"}},
		{name: "listen address", config: Config{ListenAddress: netip.MustParseAddrPort("192.0.2.10:4711")}},
		{name: "discovery", config: Config{DiscoveryEnabled: true}},
		{name: "remotes", config: Config{Remotes: []Remote{{SKI: strings.Repeat("a", 40)}}}},
		{name: "pairing policy", config: Config{PairingPolicy: PairingPolicyClosed}},
	}
	for _, test := range mixed {
		t.Run(test.name, func(t *testing.T) {
			assertMSP05PInitialV1RejectedWithoutConstruction(t, test.config)
		})
	}
	assertRuntimeDirectoryEmpty(t, sandbox)
}

type msp05pField struct {
	name string
	typ  reflect.Type
}

func assertMSP05PStructFields(t *testing.T, typ reflect.Type, want []msp05pField) {
	t.Helper()
	if typ.Kind() != reflect.Struct {
		t.Fatalf("%s kind = %s, want struct", typ, typ.Kind())
	}
	if typ.NumField() != len(want) {
		t.Fatalf("%s field count = %d, want %d", typ, typ.NumField(), len(want))
	}
	for index, expected := range want {
		field := typ.Field(index)
		if field.Name != expected.name || field.Type != expected.typ || field.Anonymous || field.PkgPath != "" || field.Tag != "" {
			t.Fatalf("%s field %d = %#v, want exported %s %s without tags", typ, index, field, expected.name, expected.typ)
		}
	}
}

func validCleanupRuntimeConfig(stateRoot string, discovery bool, remotes []Remote) Config {
	return Config{
		Enabled:          true,
		StateRoot:        stateRoot,
		Interface:        "test-interface",
		ListenAddress:    netip.MustParseAddrPort("192.0.2.10:4711"),
		DiscoveryEnabled: discovery,
		Remotes:          remotes,
		PairingPolicy:    PairingPolicyClosed,
	}
}

func assertMSP05PInitialV1RejectedWithoutConstruction(t *testing.T, config Config) {
	t.Helper()
	var acquisitions atomic.Int32
	factory := runtimeBackendFactory(func(context.Context, Config) (runtimeBackend, error) {
		acquisitions.Add(1)
		return newFakeRuntimeBackend(), nil
	})
	if instance, err := newRuntime(config, factory); err == nil || instance != nil {
		t.Fatalf("newRuntime accepted invalid configuration: runtime=%T error=%v", instance, err)
	}
	if got := acquisitions.Load(); got != 0 {
		t.Fatalf("invalid newRuntime acquired backend %d times, want 0", got)
	}
	if instance, err := New(config); err == nil || instance != nil {
		t.Fatalf("New accepted invalid configuration: runtime=%T error=%v", instance, err)
	}
}

func assertMSP05PConfigEqual(t *testing.T, got, want Config) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("backend Config = %#v, want %#v", got, want)
	}
}

func assertMSP05PDisabledRuntime(t *testing.T, instance Runtime) {
	t.Helper()
	if instance == nil {
		t.Fatal("disabled constructor returned nil runtime")
	}
	if err := instance.Start(context.Background()); err != nil {
		t.Fatalf("disabled Start() error = %v", err)
	}
	if _, err := instance.Snapshot(); !errors.Is(err, ErrRuntimeDisabled) {
		t.Fatalf("disabled Snapshot() error = %v, want ErrRuntimeDisabled", err)
	}
	if _, err := instance.PairingState(); !errors.Is(err, ErrRuntimeDisabled) {
		t.Fatalf("disabled PairingState() error = %v, want ErrRuntimeDisabled", err)
	}
	if err := instance.Shutdown(); err != nil {
		t.Fatalf("disabled Shutdown() error = %v", err)
	}
	if err := instance.Shutdown(); err != nil {
		t.Fatalf("repeated disabled Shutdown() error = %v", err)
	}
}
