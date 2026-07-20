package eebusruntime

import (
	"net/netip"
	"reflect"
	"testing"
)

func TestRuntimeRemoteEndpointV1UsesLiteralStdlibTypes(t *testing.T) {
	type fieldContract struct {
		name string
		typ  reflect.Type
	}
	want := []fieldContract{
		{name: "SKI", typ: reflect.TypeOf("")},
		{name: "Endpoint", typ: reflect.TypeOf(netip.AddrPort{})},
		{name: "SHIPPath", typ: reflect.TypeOf("")},
	}

	typ := reflect.TypeOf(Remote{})
	if typ.NumField() != len(want) {
		t.Fatalf("Remote field count = %d, want %d", typ.NumField(), len(want))
	}
	for index, expected := range want {
		field := typ.Field(index)
		if field.Name != expected.name || field.Type != expected.typ {
			t.Fatalf("Remote field %d = %s %s, want %s %s", index, field.Name, field.Type, expected.name, expected.typ)
		}
	}
	assertRuntimePlainType(t, typ, map[reflect.Type]bool{})
}

func TestRuntimeRemoteEndpointV1Validation(t *testing.T) {
	t.Run("no endpoint uses discovery fallback", func(t *testing.T) {
		config := validRuntimeConfig(t.TempDir())
		if _, err := New(config); err != nil {
			t.Fatalf("New(discovery fallback) error = %v", err)
		}
	})

	valid := []struct {
		name     string
		endpoint netip.AddrPort
		path     string
	}{
		{name: "literal IPv4", endpoint: netip.MustParseAddrPort("192.0.2.21:4712"), path: "/ship/"},
		{name: "literal IPv6", endpoint: netip.MustParseAddrPort("[2001:db8::21]:4712"), path: "/ship/"},
		{name: "subnet binding remains gateway owned", endpoint: netip.MustParseAddrPort("192.0.2.255:4712"), path: "/ship/"},
	}
	for _, test := range valid {
		t.Run(test.name, func(t *testing.T) {
			config := validRuntimeConfig(t.TempDir())
			setRuntimeRemoteEndpointForTest(&config.Remotes[0], test.endpoint, test.path)
			if _, err := New(config); err != nil {
				t.Fatalf("New(valid endpoint) error = %v", err)
			}
		})
	}

	invalidUTF8 := string([]byte{'/', 's', 'h', 'i', 'p', '/', 0xff})
	invalid := []struct {
		name     string
		endpoint netip.AddrPort
		path     string
	}{
		{name: "missing address and port", path: "/ship/"},
		{name: "zero port", endpoint: netip.AddrPortFrom(netip.MustParseAddr("192.0.2.21"), 0), path: "/ship/"},
		{name: "unspecified IPv4", endpoint: netip.MustParseAddrPort("0.0.0.0:4712"), path: "/ship/"},
		{name: "unspecified IPv6", endpoint: netip.MustParseAddrPort("[::]:4712"), path: "/ship/"},
		{name: "multicast IPv4", endpoint: netip.MustParseAddrPort("224.0.0.1:4712"), path: "/ship/"},
		{name: "multicast IPv6", endpoint: netip.MustParseAddrPort("[ff02::1]:4712"), path: "/ship/"},
		{name: "IPv4 mapped IPv6", endpoint: netip.MustParseAddrPort("[::ffff:192.0.2.21]:4712"), path: "/ship/"},
		{name: "global broadcast", endpoint: netip.MustParseAddrPort("255.255.255.255:4712"), path: "/ship/"},
		{name: "endpoint without path", endpoint: netip.MustParseAddrPort("192.0.2.21:4712")},
		{name: "non absolute path", endpoint: netip.MustParseAddrPort("192.0.2.21:4712"), path: "ship/"},
		{name: "backslash", endpoint: netip.MustParseAddrPort("192.0.2.21:4712"), path: `/ship\admin`},
		{name: "NUL", endpoint: netip.MustParseAddrPort("192.0.2.21:4712"), path: "/ship/\x00admin"},
		{name: "query", endpoint: netip.MustParseAddrPort("192.0.2.21:4712"), path: "/ship/?peer=x"},
		{name: "fragment", endpoint: netip.MustParseAddrPort("192.0.2.21:4712"), path: "/ship/#peer"},
		{name: "dot segment", endpoint: netip.MustParseAddrPort("192.0.2.21:4712"), path: "/ship/./"},
		{name: "dot segment traversal", endpoint: netip.MustParseAddrPort("192.0.2.21:4712"), path: "/ship/../admin"},
		{name: "encoded dot segment traversal", endpoint: netip.MustParseAddrPort("192.0.2.21:4712"), path: "/ship/%2e%2e/admin"},
		{name: "duplicate separator", endpoint: netip.MustParseAddrPort("192.0.2.21:4712"), path: "//ship/"},
		{name: "malformed UTF-8", endpoint: netip.MustParseAddrPort("192.0.2.21:4712"), path: invalidUTF8},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			config := validRuntimeConfig(t.TempDir())
			setRuntimeRemoteEndpointForTest(&config.Remotes[0], test.endpoint, test.path)
			if _, err := New(config); err == nil {
				t.Fatal("New() accepted invalid remote endpoint")
			}
		})
	}
}

func setRuntimeRemoteEndpointForTest(remote *Remote, endpoint netip.AddrPort, path string) bool {
	value := reflect.ValueOf(remote).Elem()
	endpointField := value.FieldByName("Endpoint")
	pathField := value.FieldByName("SHIPPath")
	if !endpointField.IsValid() || endpointField.Type() != reflect.TypeOf(netip.AddrPort{}) ||
		!pathField.IsValid() || pathField.Kind() != reflect.String {
		return false
	}
	endpointField.Set(reflect.ValueOf(endpoint))
	pathField.SetString(path)
	return true
}
