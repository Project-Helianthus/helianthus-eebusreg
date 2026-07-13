package main

import (
	"context"
	"testing"
	"time"
)

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
