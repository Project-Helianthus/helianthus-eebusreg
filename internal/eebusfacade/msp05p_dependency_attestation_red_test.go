package eebusfacade

import "testing"

func TestMSP05PRuntimeEvidencePinsReviewedEEBusGo(t *testing.T) {
	const want = "v0.7.1-helianthus.21.0.20261126050215-85efea1f3fc6"
	if EEBusGoVersion != want {
		t.Fatalf("current runtime eebus-go evidence = %q, want %q", EEBusGoVersion, want)
	}
	if got := StaticEvidence().Module.Version; got != want {
		t.Fatalf("StaticEvidence module version = %q, want %q", got, want)
	}
}
