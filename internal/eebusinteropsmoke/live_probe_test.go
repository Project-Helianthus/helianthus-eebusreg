package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLiveSmokeFailsClosedWhenLocalAdvertisementProbeIsUnavailable(t *testing.T) {
	startCalls := 0
	result := runLiveVR940fProofWithDependencies(context.Background(), liveOptions{
		Interface: "definitely-missing-interface",
		Timeout:   time.Millisecond,
		Port:      4712,
		RemoteSKI: "0123456789abcdef0123456789abcdef01234567",
	}, liveProofDependencies{
		startService: func(*liveServiceHandler) error {
			startCalls++
			return nil
		},
	})
	live := liveCaseResult(t, result)
	if startCalls != 1 {
		t.Fatalf("service start calls = %d, want 1", startCalls)
	}
	if live.ID != caseLive || live.Status != resultFail {
		t.Fatalf("live smoke result = %+v", live)
	}
	if live.Error != "mdns_probe_unavailable" {
		t.Fatalf("error = %q, want mdns_probe_unavailable", live.Error)
	}
}

func TestLiveSmokeServiceStartFailurePrecedesAdvertisementProbeFailure(t *testing.T) {
	startCalls := 0
	result := runLiveVR940fProofWithDependencies(context.Background(), liveOptions{
		Interface: "definitely-missing-interface",
		Timeout:   time.Millisecond,
		Port:      4712,
		RemoteSKI: "0123456789abcdef0123456789abcdef01234567",
	}, liveProofDependencies{
		startService: func(*liveServiceHandler) error {
			startCalls++
			return errors.New("injected service start failure")
		},
	})
	live := liveCaseResult(t, result)
	if startCalls != 1 {
		t.Fatalf("service start calls = %d, want 1", startCalls)
	}
	if live.ID != caseLive || live.Status != resultFail {
		t.Fatalf("live smoke result = %+v", live)
	}
	if live.Error != "live_service_start_failed" {
		t.Fatalf("error = %q, want live_service_start_failed", live.Error)
	}
}

func liveCaseResult(t *testing.T, result liveProofResult) caseResult {
	t.Helper()
	for _, item := range result.Cases {
		if item.ID == caseLive {
			return item
		}
	}
	t.Fatal("live proof result is missing G17")
	return caseResult{}
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
