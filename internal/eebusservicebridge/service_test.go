package eebusservicebridge

import (
	"crypto/x509"
	"net"
	"net/netip"
	"testing"
	"time"

	eebusapi "github.com/Project-Helianthus/helianthus-eebus-go/api"
	shipcert "github.com/Project-Helianthus/helianthus-ship-go/cert"
	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
)

func TestScopedServiceShutdownJoinsMonitorWithoutFalseTerminal(t *testing.T) {
	for iteration := 0; iteration < 10; iteration++ {
		certificate, err := shipcert.CreateCertificate("", "Helianthus", "RO", "scoped-monitor-test")
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
		probe, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
		if err != nil {
			t.Fatal(err)
		}
		endpoint := probe.Addr().(*net.TCPAddr).AddrPort()
		if err := probe.Close(); err != nil {
			t.Fatal(err)
		}
		configuration, err := eebusapi.NewConfiguration(
			"Project-Helianthus", "Helianthus", "eebusreg", localSKI,
			spinemodel.DeviceTypeTypeEnergyManagementSystem,
			[]spinemodel.EntityTypeType{spinemodel.EntityTypeTypeCEM},
			int(endpoint.Port()), certificate, time.Second,
		)
		if err != nil {
			t.Fatal(err)
		}
		service := NewServiceWithOptions(configuration, nil, ServiceOptions{
			ListenerPolicy: &ListenerPolicy{ListenAddress: endpoint, DiscoveryEnabled: false},
		})
		if service == nil {
			t.Fatal("scoped service construction returned nil")
		}
		if err := service.Setup(); err != nil {
			t.Fatal(err)
		}
		if err := service.StartWithPolicy(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(110 * time.Millisecond)
		service.Shutdown()
		select {
		case <-service.monitorDone:
		default:
			t.Fatal("Shutdown returned before the listener monitor terminated")
		}
		select {
		case err := <-service.ListenerTerminal():
			t.Fatalf("normal shutdown emitted listener terminal error: %v", err)
		default:
		}
		rebound, err := net.ListenTCP("tcp4", net.TCPAddrFromAddrPort(netip.AddrPortFrom(endpoint.Addr(), endpoint.Port())))
		if err != nil {
			t.Fatalf("listener remained bound after joined shutdown: %v", err)
		}
		_ = rebound.Close()
	}
}
