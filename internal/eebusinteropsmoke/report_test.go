package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestReportValidatesAndRedactsPublicOutput(t *testing.T) {
	rep := newReport("fake-peer", []string{caseFakePeer}, []caseResult{{
		ID:       caseFakePeer,
		Status:   resultPass,
		Evidence: []string{"fake-peer-import-boundary-checked", "fake-peer-ship-session-connected-both-directions"},
		Details: map[string]string{
			"local_endpoint_ref": digestRef("raw-local-ski"),
		},
	}}, nil)
	rep.GeneratedAt = time.Date(2026, 7, 8, 18, 0, 0, 0, time.UTC)
	if err := rep.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	payload, err := rep.jsonBytes()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "raw-local-ski") {
		t.Fatalf("payload leaked raw input: %s", payload)
	}
}

func TestReportRejectsRawNetworkAndSecretValues(t *testing.T) {
	for name, value := range map[string]string{
		"raw-ip":     "192.168.100.4",
		"raw-mac":    "00:11:22:33:44:55",
		"pem":        "-----BEGIN PRIVATE KEY-----",
		"bearer":     "Bearer abc.def.ghi",
		"secret-key": "token=abc",
	} {
		t.Run(name, func(t *testing.T) {
			rep := newReport("live-vr940f", []string{caseLive}, []caseResult{{
				ID:       caseLive,
				Status:   resultBlocked,
				Evidence: []string{"live-vr940f-mdns-probe-attempted"},
				Details:  map[string]string{"bad": value},
			}}, nil)
			if err := rep.validate(); err == nil {
				t.Fatal("validate succeeded for unredacted report")
			}
		})
	}
}

func TestReportJSONSortsCasesAndEvidence(t *testing.T) {
	rep := newReport("all", []string{caseFakePeer, caseLive}, []caseResult{
		{ID: caseLive, Status: resultBlocked, Evidence: []string{"b", "a"}},
		{ID: caseFakePeer, Status: resultPass, Evidence: []string{"d", "c"}},
	}, nil)
	payload, err := rep.jsonBytes()
	if err != nil {
		t.Fatal(err)
	}
	var decoded report
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Cases[0].ID != caseFakePeer || decoded.Cases[0].Evidence[0] != "c" {
		t.Fatalf("report was not sorted deterministically: %s", payload)
	}
}
