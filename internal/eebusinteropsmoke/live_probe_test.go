package main

import (
	"context"
	"testing"
	"time"
)

func TestLiveSmokeBlocksWhenDiscoveryUnavailable(t *testing.T) {
	result := runLiveVR940fSmoke(context.Background(), liveOptions{
		Interface: "definitely-missing-interface",
		Timeout:   time.Millisecond,
	})
	if result.ID != caseLive || result.Status != resultBlocked {
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
}
