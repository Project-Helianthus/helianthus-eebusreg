package eebusruntime

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type exactRuntimeContract interface {
	Start(context.Context) error
	Shutdown() error
	Snapshot() (SnapshotV1, error)
	PairingState() ([]PairingObservationV1, error)
}

var (
	_ exactRuntimeContract                      = Runtime(nil)
	_ Runtime                                   = exactRuntimeContract(nil)
	_ func(Config) (Runtime, error)             = New
	_ interface{ Start(context.Context) error } = Runtime(nil)
	_ error                                     = ErrRuntimeDisabled
	_ error                                     = ErrRuntimeShutdown
)

func TestRuntimePublicLifecycleContractIsExact(t *testing.T) {
	runtimeType := reflect.TypeOf((*Runtime)(nil)).Elem()
	want := map[string]reflect.Type{
		"Start":        reflect.TypeOf((func(context.Context) error)(nil)),
		"Shutdown":     reflect.TypeOf((func() error)(nil)),
		"Snapshot":     reflect.TypeOf((func() (SnapshotV1, error))(nil)),
		"PairingState": reflect.TypeOf((func() ([]PairingObservationV1, error))(nil)),
	}
	if runtimeType.NumMethod() != len(want) {
		t.Fatalf("Runtime method count = %d, want %d", runtimeType.NumMethod(), len(want))
	}
	for name, signature := range want {
		method, ok := runtimeType.MethodByName(name)
		if !ok {
			t.Fatalf("Runtime is missing %s", name)
		}
		if method.Type != signature {
			t.Fatalf("Runtime.%s type = %s, want %s", name, method.Type, signature)
		}
	}
}

func TestRuntimeConfigurationUsesOnlyPlainTypes(t *testing.T) {
	for _, value := range []any{Config{}, Remote{}} {
		assertRuntimePlainType(t, reflect.TypeOf(value), map[reflect.Type]bool{})
	}
	assertRuntimePlainType(t, reflect.TypeOf((*Runtime)(nil)).Elem(), map[reflect.Type]bool{})

	enabled, ok := reflect.TypeOf(Config{}).FieldByName("Enabled")
	if !ok || enabled.Type.Kind() != reflect.Bool {
		t.Fatal("Config.Enabled must be a bool")
	}
}

func TestRuntimeDisabledDefaultRemainsInert(t *testing.T) {
	tests := []struct {
		name   string
		config func(string) Config
	}{
		{
			name: "zero default",
			config: func(string) Config {
				return Config{}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sandbox := t.TempDir()
			t.Setenv("HOME", sandbox)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(sandbox, "config"))
			t.Setenv("XDG_DATA_HOME", filepath.Join(sandbox, "data"))
			t.Setenv("XDG_STATE_HOME", filepath.Join(sandbox, "state"))
			stateRoot := filepath.Join(sandbox, "configured-state")

			instance, err := New(test.config(stateRoot))
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if instance == nil {
				t.Fatal("New() returned a nil disabled runtime")
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
			if err := instance.Start(context.Background()); !errors.Is(err, ErrRuntimeShutdown) {
				t.Fatalf("Start() after terminal shutdown error = %v, want ErrRuntimeShutdown", err)
			}
			assertRuntimeDirectoryEmpty(t, sandbox)
		})
	}
}

func TestRuntimeEnabledNewValidatesWithoutIO(t *testing.T) {
	sandbox := t.TempDir()
	stateRoot := filepath.Join(sandbox, "runtime-state")
	valid := validRuntimeConfig(stateRoot)

	invalid := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "missing state root", mutate: func(config *Config) { config.StateRoot = "" }},
		{name: "missing interface", mutate: func(config *Config) { config.Interface = "" }},
		{name: "missing listen address", mutate: func(config *Config) { config.ListenAddress = netip.AddrPort{} }},
		{name: "missing pairing policy", mutate: func(config *Config) { config.PairingPolicy = "" }},
		{name: "missing remote ski", mutate: func(config *Config) { config.Remotes[0].SKI = "" }},
		{name: "duplicate remote", mutate: func(config *Config) { config.Remotes = append(config.Remotes, config.Remotes[0]) }},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			config.Remotes = append([]Remote(nil), valid.Remotes...)
			test.mutate(&config)
			if _, err := New(config); err == nil {
				t.Fatal("New() accepted invalid enabled configuration")
			}
			assertRuntimePathAbsent(t, stateRoot)
		})
	}

	instance, err := New(valid)
	if err != nil {
		t.Fatalf("New(valid enabled config) error = %v", err)
	}
	if instance == nil {
		t.Fatal("New(valid enabled config) returned nil")
	}
	assertRuntimePathAbsent(t, stateRoot)
	if err := instance.Shutdown(); err != nil {
		t.Fatalf("Shutdown() before Start() error = %v", err)
	}
	assertRuntimePathAbsent(t, stateRoot)
}

func validRuntimeConfig(stateRoot string) Config {
	return Config{
		Enabled:       true,
		StateRoot:     stateRoot,
		Interface:     "test-interface",
		ListenAddress: netip.MustParseAddrPort("192.0.2.10:4711"),
		Remotes:       []Remote{{SKI: "0000000000000000000000000000000000000001"}},
		PairingPolicy: PairingPolicyClosed,
	}
}

func assertRuntimePlainType(t *testing.T, typ reflect.Type, seen map[reflect.Type]bool) {
	t.Helper()
	if typ == nil || seen[typ] {
		return
	}
	seen[typ] = true
	if path := typ.PkgPath(); strings.HasPrefix(path, "github.com/"+"enbility/") ||
		strings.Contains(path, "/internal/") || strings.Contains(path, "eebusstore") {
		t.Fatalf("public runtime type reaches protected package %q", path)
	}
	switch typ.Kind() {
	case reflect.Array, reflect.Chan, reflect.Pointer, reflect.Slice:
		assertRuntimePlainType(t, typ.Elem(), seen)
	case reflect.Func:
		for index := 0; index < typ.NumIn(); index++ {
			assertRuntimePlainType(t, typ.In(index), seen)
		}
		for index := 0; index < typ.NumOut(); index++ {
			assertRuntimePlainType(t, typ.Out(index), seen)
		}
	case reflect.Interface:
		for index := 0; index < typ.NumMethod(); index++ {
			assertRuntimePlainType(t, typ.Method(index).Type, seen)
		}
	case reflect.Map:
		assertRuntimePlainType(t, typ.Key(), seen)
		assertRuntimePlainType(t, typ.Elem(), seen)
	case reflect.Struct:
		for index := 0; index < typ.NumField(); index++ {
			assertRuntimePlainType(t, typ.Field(index).Type, seen)
		}
	}
}

func assertRuntimePathAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime path was touched before Start: %v", err)
	}
}

func assertRuntimeDirectoryEmpty(t *testing.T, path string) {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("disabled runtime created %d filesystem entries", len(entries))
	}
}
