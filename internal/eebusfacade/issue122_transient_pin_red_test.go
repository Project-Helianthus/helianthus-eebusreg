package eebusfacade

import (
	"errors"
	"testing"

	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
)

func TestIssue122PINConnectFailuresExposeOnlyClosedCategories(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{name: "unavailable", err: shipapi.ErrPINUnavailable, want: "pin_unavailable"},
		{name: "rejected", err: shipapi.ErrPINRejected, want: "pin_rejected"},
		{name: "protocol", err: shipapi.ErrPINProtocol, want: "pin_protocol"},
		{name: "provider", err: shipapi.ErrPINProviderInvalid, want: "admin_boundary_unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := mapOperatorAdminV1ConnectError(test.err); got != test.want {
				t.Fatalf("PIN connect failure %v maps to %q, want closed category %q", test.err, got, test.want)
			}
		})
	}
	if got := mapOperatorAdminV1ConnectError(errors.New("pin=a1b2c3d4")); got != "unknown_state" {
		t.Fatalf("unrecognized error maps to %q, want secret-free unknown_state", got)
	}
}
