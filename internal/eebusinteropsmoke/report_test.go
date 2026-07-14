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
		"raw-ip":       "192.168.100.4",
		"raw-ipv6":     "2001:db8::1",
		"raw-mac":      "00:11:22:33:44:55",
		"raw-mac-dash": "00-11-22-33-44-55",
		"pem":          "-----BEGIN PRIVATE KEY-----",
		"bearer":       "Bearer abc.def.ghi",
		"secret-key":   "token=abc",
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

func TestReportRejectsCredentialLikeKeysStructurally(t *testing.T) {
	rep := newReport("live-vr940f", []string{caseLive}, []caseResult{{
		ID:       caseLive,
		Status:   resultBlocked,
		Evidence: []string{"live-vr940f-mdns-probe-attempted"},
		Details:  map[string]string{"api_token": "redacted"},
	}}, nil)
	if err := rep.validate(); err == nil || !strings.Contains(err.Error(), "credential-like key") {
		t.Fatalf("credential-like key was accepted: %v", err)
	}
}

func TestReportDoesNotTreatArbitraryCommitKeyAsIdentityExemption(t *testing.T) {
	for _, key := range []string{"commit", "repo_commit"} {
		t.Run(key, func(t *testing.T) {
			rep := newReport("fake-peer", []string{caseFakePeer}, []caseResult{{
				ID:       caseFakePeer,
				Status:   resultPass,
				Evidence: []string{"fake-peer-pass"},
				Details:  map[string]string{key: strings.Repeat("a", 40)},
			}}, nil)
			if err := rep.validate(); err == nil || !strings.Contains(err.Error(), "raw 40-hex identity") {
				t.Fatalf("caller-controlled %s key exempted a raw 40-hex identity: %v", key, err)
			}
		})
	}
}

func TestReportIdentityRedactionIsDeterministic(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"repo_commit": strings.Repeat("a", 40),
		"z":           strings.Repeat("b", 40),
		"a":           strings.Repeat("c", 40),
	})
	if err != nil {
		t.Fatal(err)
	}
	const expected = "public report contains raw 40-hex identity at a"
	for i := 0; i < 100; i++ {
		err := validatePublicRedaction(payload)
		if err == nil || err.Error() != expected {
			t.Fatalf("iteration %d: redaction error = %v, want %q", i, err, expected)
		}
	}
}

func TestReportJSONSortsCasesAndEvidence(t *testing.T) {
	rep := newReport("all", []string{caseFakePeer, caseLive, caseFakePeer}, []caseResult{
		{ID: caseLive, Status: resultBlocked, Evidence: []string{"b", "a", "a"}},
		{ID: caseFakePeer, Status: resultPass, Evidence: []string{"d", "c"}},
	}, []string{"note-b", "note-a", "note-a"})
	originalFirstID := rep.Cases[0].ID
	originalEvidence := append([]string(nil), rep.Cases[0].Evidence...)
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
	if len(decoded.Cases[1].Evidence) != 2 {
		t.Fatalf("set-valued evidence was not deduplicated: %v", decoded.Cases[1].Evidence)
	}
	if len(decoded.RequiredCases) != 2 || len(decoded.Notes) != 2 || decoded.Notes[0] != "note-a" {
		t.Fatalf("report set fields were not canonicalized: required=%v notes=%v", decoded.RequiredCases, decoded.Notes)
	}
	if rep.Cases[0].ID != originalFirstID || strings.Join(rep.Cases[0].Evidence, "|") != strings.Join(originalEvidence, "|") {
		t.Fatalf("canonical encoding mutated caller-owned report: %+v", rep.Cases)
	}
}

func TestReportResultMustMatchFailBlockedPassPrecedence(t *testing.T) {
	rep := newReport("all", []string{caseFakePeer, caseLive}, []caseResult{
		{ID: caseFakePeer, Status: resultBlocked, Evidence: []string{"blocked"}},
		{ID: caseLive, Status: resultFail, Evidence: []string{"failed"}},
	}, nil)
	if rep.Result != resultFail {
		t.Fatalf("derived result = %s, want FAIL", rep.Result)
	}
	rep.Result = resultPass
	if err := rep.validate(); err == nil || !strings.Contains(err.Error(), "derived result") {
		t.Fatalf("mismatched aggregate result accepted: %v", err)
	}
}

func TestReportTrimsBeforeRequiredFieldValidation(t *testing.T) {
	rep := newReport(" fake-peer ", []string{" " + caseFakePeer + " "}, []caseResult{{
		ID:       " " + caseFakePeer + " ",
		Status:   " " + resultPass + " ",
		Evidence: []string{"  fake-peer-pass  "},
	}}, nil)
	if err := rep.validate(); err != nil {
		t.Fatalf("trimmed report rejected: %v", err)
	}
	rep.Cases[0].Evidence = []string{"   "}
	if err := rep.validate(); err == nil || !strings.Contains(err.Error(), "evidence is required") {
		t.Fatalf("whitespace-only evidence accepted: %v", err)
	}
}

func TestPassingG19ReportRequiresCanonicalLiveEvidence(t *testing.T) {
	g19 := evaluateG19(passingG19Observation())
	rep := newReport("live-vr940f", []string{caseLive, caseDirectAccess}, []caseResult{
		{ID: caseLive, Status: resultPass, Evidence: []string{"g17-pass"}},
		g19,
	}, nil)
	rep.GeneratedAt = time.Date(2026, 7, 13, 21, 0, 0, 0, time.UTC)
	if err := rep.validate(); err == nil || !strings.Contains(err.Error(), "canonical live evidence") {
		t.Fatalf("passing G19 report without canonical evidence: %v", err)
	}

	evidence := passingLiveGateEvidence()
	rep.LiveEvidence = &evidence
	if err := rep.validate(); err != nil {
		t.Fatalf("passing G19 report rejected: %v", err)
	}

	evidence.CaseBinding.ResultHash = "sha256:" + strings.Repeat("0", 64)
	rep.LiveEvidence = &evidence
	if err := rep.validate(); err == nil || !strings.Contains(err.Error(), "not bound") {
		t.Fatalf("G19 case/evidence mismatch accepted: %v", err)
	}
}

func TestReportAlwaysRedactsLiveEvidenceBeforeStatusContract(t *testing.T) {
	rep := newReport("live-vr940f", []string{caseDirectAccess}, []caseResult{{
		ID:       caseDirectAccess,
		Status:   resultFail,
		Evidence: []string{"g19-failed"},
	}}, nil)
	evidence := passingLiveGateEvidence()
	evidence.OwnerAcceptance.Notes = "192.0.2.44"
	rep.LiveEvidence = &evidence
	if err := rep.validate(); err == nil || !strings.Contains(err.Error(), "IP address") {
		t.Fatalf("non-PASS live evidence bypassed redaction: %v", err)
	}
}

func TestReportRejectsCallerControlledProvenance(t *testing.T) {
	rep := newReport("fake-peer", []string{caseFakePeer}, []caseResult{{
		ID:       caseFakePeer,
		Status:   resultPass,
		Evidence: []string{"fake-peer-pass"},
	}}, nil)
	rep.RepoCommit = strings.Repeat("a", 40)
	if rep.RepoCommit == currentRepoEvidence().Commit {
		rep.RepoCommit = strings.Repeat("b", 40)
	}
	if err := rep.validate(); err == nil || !strings.Contains(err.Error(), "provenance") {
		t.Fatalf("caller-controlled provenance accepted: %v", err)
	}
}

func TestIntegrityReferencesUseFullSHA256(t *testing.T) {
	ref := digestRef("identity-material")
	if len(ref) != len("sha256:")+64 || !validSHA256Ref(ref) {
		t.Fatalf("digest reference is not full SHA-256: %q", ref)
	}
}
