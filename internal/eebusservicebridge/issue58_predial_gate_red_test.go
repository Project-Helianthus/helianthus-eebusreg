package eebusservicebridge

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"testing"
	"time"

	eebusapi "github.com/Project-Helianthus/helianthus-eebus-go/api"
	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
	shipcert "github.com/Project-Helianthus/helianthus-ship-go/cert"
	shipmodel "github.com/Project-Helianthus/helianthus-ship-go/model"
	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
)

type issue58AttemptBridge struct{}

type issue58AttemptHandle struct {
	ctx context.Context
}

func (*issue58AttemptBridge) Prepare(shipapi.OutgoingAttemptRequest) (shipapi.OutgoingAttemptHandle, error) {
	return &issue58AttemptHandle{ctx: context.Background()}, nil
}

func (*issue58AttemptBridge) AuthorizeLaunch(handle shipapi.OutgoingAttemptHandle) (shipapi.OutgoingAttemptPermit, error) {
	return shipapi.OutgoingAttemptPermit{
		Decision: shipapi.OutgoingAttemptDecisionPermit,
		Reason:   shipapi.OutgoingAttemptReasonAuthorized,
		Context:  handle.Context(),
	}, nil
}

func (*issue58AttemptBridge) AbortPrepared(shipapi.OutgoingAttemptHandle) (shipapi.OutgoingAttemptAbortResult, error) {
	return shipapi.OutgoingAttemptAbortConsumed, nil
}

func (*issue58AttemptBridge) OutgoingAttemptConnectionClosed(string, bool, shipapi.OutgoingAttemptMetadata) {
}

func (*issue58AttemptBridge) OutgoingAttemptHandshakeStateUpdate(string, shipmodel.ShipState, shipapi.OutgoingAttemptMetadata) {
}

func (*issue58AttemptHandle) AttemptID() string               { return "issue58-attempt" }
func (*issue58AttemptHandle) Scope() string                   { return "issue58-scope" }
func (*issue58AttemptHandle) ControlEpoch() uint64            { return 1 }
func (handle *issue58AttemptHandle) Context() context.Context { return handle.ctx }

func TestIssue58ServiceOptionsInstallAttemptGateAlongsideListenerPolicy(t *testing.T) {
	certificate, localSKI, endpoint := issue58ServiceFixture(t)
	configuration, err := eebusapi.NewConfiguration(
		"Project-Helianthus", "Helianthus", "eebusreg", localSKI,
		spinemodel.DeviceTypeTypeEnergyManagementSystem,
		[]spinemodel.EntityTypeType{spinemodel.EntityTypeTypeCEM},
		endpoint.Port, certificate, time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	bridge := &issue58AttemptBridge{}
	service := NewServiceWithOptions(configuration, nil, ServiceOptions{
		ListenerPolicy: &ListenerPolicy{
			ListenAddress:    endpoint.AddrPort(),
			DiscoveryEnabled: false,
		},
		OutgoingAttemptBridge: &OutgoingAttemptBridgeConfiguration{
			Gate: bridge,
			Sink: bridge,
		},
	})
	if service == nil {
		t.Fatal("service construction returned nil")
	}
	if err := service.Setup(); err != nil {
		t.Fatalf("service setup rejected complete attempt bridge: %v", err)
	}
	service.Shutdown()
}

func issue58ServiceFixture(t *testing.T) (tls.Certificate, string, *net.TCPAddr) {
	t.Helper()
	certificate, err := shipcert.CreateCertificate("", "Helianthus", "RO", "issue58-predial-gate")
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	localSKI, err := shipcert.SkiFromCertificate(leaf)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := probe.Addr().(*net.TCPAddr)
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	return certificate, localSKI, endpoint
}
