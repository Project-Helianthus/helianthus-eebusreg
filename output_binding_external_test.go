package eebusruntime_test

import (
	"testing"

	eebusruntime "github.com/Project-Helianthus/helianthus-eebusreg"
)

// applyFromAnotherPackage proves that a caller outside the package can name
// the lens result and call Apply with it.
var applyFromAnotherPackage func(*eebusruntime.OutputBinding, eebusruntime.LensResult) []string = (*eebusruntime.OutputBinding).Apply

func TestOutputBindingAcceptsExportedLensResult(t *testing.T) {
	result := eebusruntime.LensResult{
		Family:         "inverter",
		ResourceID:     "north",
		LensAdmitted:   true,
		RuntimeEnabled: true,
		Operations: []eebusruntime.LensOperation{{
			Name:          "pv.operation.set_active_power_limit",
			Unit:          "W",
			OwnCapability: true,
		}},
	}
	if applyFromAnotherPackage == nil || result.Operations[0].Unit != "W" {
		t.Fatal("exported lens result is not usable")
	}
	// No runtime service runs in this process, so the binding has no node to
	// attach to and composes none of its own.
	if binding, err := eebusruntime.NewOutputBinding(t.TempDir()); err == nil || binding != nil {
		t.Fatal("binding opened without the runtime node")
	}
}
