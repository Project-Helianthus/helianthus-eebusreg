package main

import (
	"context"
	"net"
	"reflect"
	"testing"
	"time"
)

type recordedLANSHIPMDNSProvider struct {
	serviceName string
	port        int
	txt         []string
	shutdown    bool
}

func (p *recordedLANSHIPMDNSProvider) Announce(serviceName string, port int, txt []string) error {
	p.serviceName = serviceName
	p.port = port
	p.txt = append([]string(nil), txt...)
	return nil
}

func (p *recordedLANSHIPMDNSProvider) Shutdown() {
	p.shutdown = true
}

func TestStartLANSHIPPublisherAnnouncesCustomLANSHIPService(t *testing.T) {
	originalInterfaceByName := lanSHIPInterfaceByName
	originalProviderFactory := newLANSHIPMDNSProvider
	t.Cleanup(func() {
		lanSHIPInterfaceByName = originalInterfaceByName
		newLANSHIPMDNSProvider = originalProviderFactory
	})

	wantInterface := net.Interface{Index: 7, Name: "custom-lan0"}
	lanSHIPInterfaceByName = func(name string) (*net.Interface, error) {
		if name != wantInterface.Name {
			t.Fatalf("interface name = %q, want %q", name, wantInterface.Name)
		}
		return &wantInterface, nil
	}
	provider := &recordedLANSHIPMDNSProvider{}
	var gotInterfaces []net.Interface
	newLANSHIPMDNSProvider = func(ifaces []net.Interface) lanSHIPMDNSProvider {
		gotInterfaces = append([]net.Interface(nil), ifaces...)
		return provider
	}

	publisher, err := startLANSHIPPublisher(wantInterface.Name, 4712, "0123456789ABCDEF0123456789ABCDEF01234567", "raw-probe-7", true)
	if err != nil {
		t.Fatalf("startLANSHIPPublisher() error = %v", err)
	}
	if publisher.serviceFQDN != "Helianthus EnergyManagementSystem RawProbe._ship._tcp.local." {
		t.Fatalf("service FQDN = %q", publisher.serviceFQDN)
	}
	if provider.serviceName != liveServiceName {
		t.Fatalf("service name = %q, want %q", provider.serviceName, liveServiceName)
	}
	if provider.port != 4712 {
		t.Fatalf("port = %d, want 4712", provider.port)
	}
	if !reflect.DeepEqual(gotInterfaces, []net.Interface{wantInterface}) {
		t.Fatalf("interfaces = %+v, want %+v", gotInterfaces, []net.Interface{wantInterface})
	}
	wantTXT := []string{
		"txtvers=1",
		"path=/ship/",
		"id=raw-probe-7",
		"ski=0123456789abcdef0123456789abcdef01234567",
		"brand=Helianthus",
		"model=RawProbe",
		"type=EnergyManagementSystem",
		"register=true",
	}
	if !reflect.DeepEqual(provider.txt, wantTXT) {
		t.Fatalf("TXT = %#v, want %#v", provider.txt, wantTXT)
	}

	publisher.shutdown()
	if !provider.shutdown {
		t.Fatal("publisher did not shut down provider")
	}
}

func TestLiveSmokeFailsClosedWhenLocalAdvertisementProbeIsUnavailable(t *testing.T) {
	result := runLiveVR940fSmoke(context.Background(), liveOptions{
		Interface: "definitely-missing-interface",
		Timeout:   time.Millisecond,
		Port:      4712,
		RemoteSKI: "0123456789abcdef0123456789abcdef01234567",
	})
	if result.ID != caseLive || result.Status != resultFail {
		t.Fatalf("live smoke result = %+v", result)
	}
	if result.Error != "mdns_probe_unavailable" {
		t.Fatalf("error = %q, want mdns_probe_unavailable", result.Error)
	}
}

func TestParseMDNSRecordsFindsSHIPPtr(t *testing.T) {
	packet := []byte{
		0, 0, 0x84, 0, 0, 0, 0, 1, 0, 0, 0, 0,
	}
	packet = append(packet, encodeDNSName("_ship._tcp.local.")...)
	packet = append(packet, 0, 12, 0, 1, 0, 0, 0, 120)
	target := encodeDNSName("Helianthus._ship._tcp.local.")
	packet = append(packet, byte(len(target)>>8), byte(len(target)))
	packet = append(packet, target...)
	records := parseMDNSRecords(packet)
	if len(records) != 1 {
		t.Fatalf("records = %+v", records)
	}
	if records[0].Name != "_ship._tcp.local." || records[0].Value != "Helianthus._ship._tcp.local." {
		t.Fatalf("unexpected record: %+v", records[0])
	}
	if records[0].TTL != 120 {
		t.Fatalf("TTL = %d, want 120", records[0].TTL)
	}
}

func TestParseMDNSRecordsPreservesGoodbyeTTL(t *testing.T) {
	packet := []byte{
		0, 0, 0x84, 0, 0, 0, 0, 1, 0, 0, 0, 0,
	}
	packet = append(packet, encodeDNSName("_ship._tcp.local.")...)
	packet = append(packet, 0, 12, 0, 1, 0, 0, 0, 0)
	target := encodeDNSName("Helianthus._ship._tcp.local.")
	packet = append(packet, byte(len(target)>>8), byte(len(target)))
	packet = append(packet, target...)
	records := parseMDNSRecords(packet)
	if len(records) != 1 || records[0].TTL != 0 {
		t.Fatalf("goodbye records = %+v", records)
	}
}
