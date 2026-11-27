package eebusservicebridge

import (
	"context"
	"testing"

	semdriver "github.com/Project-Helianthus/helianthus-semreg/semreg/v1/driver"
)

type surfaceRegistry struct{}

func (surfaceRegistry) Root() semdriver.NodeRef { return semdriver.NodeRef{} }
func (surfaceRegistry) Node(semdriver.NodeSpec) (semdriver.NodeRef, error) {
	return semdriver.NodeRef{}, nil
}
func (surfaceRegistry) ReadMember(semdriver.NodeRef, semdriver.MemberSpec, semdriver.NativeReader) error {
	return nil
}
func (surfaceRegistry) Method(semdriver.NodeRef, semdriver.MethodSpec, semdriver.NativeMethod) error {
	return nil
}
func (surfaceRegistry) View(semdriver.ViewSpec) error { return nil }

func TestPairingSessionSurfaceReturnsTheBoundRegistry(t *testing.T) {
	var registry semdriver.NativeRegistry = surfaceRegistry{}
	surface := NewPairingSessionSurface(registry)
	if surface.Native() != registry {
		t.Fatal("registry call did not return the bound value")
	}
	if _, err := surface.SemanticInput().Capture(context.Background(), semdriver.Filter{}); err == nil || err.Error() != "not used" {
		t.Fatalf("unused read error = %v", err)
	}
}
