package eebusfacade

import (
	"context"
	"testing"
	"time"

	eebusapi "github.com/Project-Helianthus/helianthus-eebus-go/api"
	shipapi "github.com/Project-Helianthus/helianthus-ship-go/api"
	shipcert "github.com/Project-Helianthus/helianthus-ship-go/cert"
)

func TestIssue54AllowlistAndVisibilityCannotRequestOutboundConnection(t *testing.T) {
	certificate, err := shipcert.CreateCertificate("", "Helianthus", "RO", "issue54-no-outbound")
	if err != nil {
		t.Fatal(err)
	}
	remoteSKI := "1111111111111111111111111111111111111111"
	service := &fakeRuntimeService{started: make(chan struct{})}
	var reader eebusapi.ServiceReaderInterface
	backend, err := acquireRuntime(context.Background(), RuntimeConfig{
		StateRoot:  "/unused/issue54-no-outbound",
		Interface:  "fixture-interface",
		ListenPort: 4711,
		Remotes: []RuntimeRemote{{
			SKI:         remoteSKI,
			Pretrusted:  true,
			Allowlisted: true,
		}},
	}, runtimeDependencies{
		loadMaterial: func(context.Context, string) (runtimeMaterial, error) {
			return runtimeMaterial{
				certificate: certificate,
				localSKI:    certificateSKI(t, certificate),
				nodeToken:   runtimeTestNodeToken,
				pretrusted:  map[string]bool{remoteSKI: true},
			}, nil
		},
		newService: func(_ RuntimeConfig, _ runtimeMaterial, callback eebusapi.ServiceReaderInterface) (runtimeService, error) {
			reader = callback
			return service, nil
		},
		now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := backend.Close(); err != nil {
			t.Errorf("close runtime: %v", err)
		}
	}()

	reader.VisibleRemoteServicesUpdated(nil, []shipapi.RemoteService{{Ski: remoteSKI}})
	service.mu.Lock()
	registered := append([]string(nil), service.registered...)
	service.mu.Unlock()
	if len(registered) != 0 {
		t.Fatalf("allowlist or visibility requested outbound registration: %v", registered)
	}
}
