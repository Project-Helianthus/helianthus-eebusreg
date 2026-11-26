package main

import "testing"

func remoteMonitoringSymbol(symbol symbol) bool {
	switch symbol.Name {
	case "RemoteMonitoring", "RemoteMonitoringRemote", "RemoteMonitoringEntity", "RemoteMonitoringObservation":
		return true
	}
	return false
}

func TestRemoteMonitoringSurfaceIsClosed(t *testing.T) {
	doc, err := extract(moduleRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"type RemoteMonitoring":            "type RemoteMonitoring struct{ Remotes []RemoteMonitoringRemote }",
		"type RemoteMonitoringRemote":      "type RemoteMonitoringRemote struct{ SKI string; SHIPID string; ConnectionState string; PairingState string; Visible bool; Paired bool; ServiceName string; ServiceIdentifier string; ServiceBrand string; ServiceType string; ServiceModel string; Entities []RemoteMonitoringEntity }",
		"type RemoteMonitoringEntity":      "type RemoteMonitoringEntity struct{ DeviceAddress string; DeviceType string; EntityAddress []uint64; EntityType string; UseCase string; Actor string; Scenarios []uint; Observations []RemoteMonitoringObservation }",
		"type RemoteMonitoringObservation": "type RemoteMonitoringObservation struct{ Scenario uint; Name string; Phase string; Number float64; Unit string; Updated time.Time; Source string; Stale bool }",
	}
	got := map[string]string{}
	for _, symbol := range msp05pRootSurface(t, doc).Symbols {
		if !remoteMonitoringSymbol(symbol) {
			continue
		}
		got[symbol.Kind+" "+symbol.Name] = symbol.Signature
	}
	if len(got) != len(want) {
		t.Fatalf("remote monitoring exports = %d, want %d: %v", len(got), len(want), got)
	}
	for key, signature := range want {
		if got[key] != signature {
			t.Errorf("%s signature = %q, want %q", key, got[key], signature)
		}
	}
	projected := msp05pProjectFrozenV1(t, doc)
	for _, symbol := range msp05pRootSurface(t, projected).Symbols {
		if remoteMonitoringSymbol(symbol) {
			t.Fatalf("historical projection retained %s", symbol.Signature)
		}
	}
}
