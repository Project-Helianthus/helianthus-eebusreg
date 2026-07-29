package eebusruntime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

func TestIssue93RuntimeConfigOwnsMutationLabProfilesBeforeFacadeAcquisition(t *testing.T) {
	profile := issue93RuntimeLabProfile(t)
	expected := profile.Clone()
	config := validRuntimeConfig(t.TempDir())
	config.MutationLabProfiles = []eebusraw.MutationLabProfileV1{profile}

	var acquired Config
	instance, err := newRuntime(config, func(_ context.Context, got Config) (runtimeBackend, error) {
		acquired = got
		return &issue85RootMutationBackend{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	config.MutationLabProfiles[0].Target.EntityAddress[0] = 99
	config.MutationLabProfiles[0].AllowedValueHashes[0] = eebusraw.HashV1(
		"sha256:" + strings.Repeat("4", 64),
	)
	config.MutationLabProfiles[0].SafetyPredicates[0] = "changed"
	config.MutationLabProfiles[0].EvidenceHashes[0] = eebusraw.HashV1(
		"sha256:" + strings.Repeat("5", 64),
	)

	if err := instance.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := instance.Shutdown(); err != nil {
			t.Error(err)
		}
	})

	if len(acquired.MutationLabProfiles) != 1 {
		t.Fatalf("facade acquisition profiles = %d, want 1", len(acquired.MutationLabProfiles))
	}
	got := acquired.MutationLabProfiles[0]
	if got.Target.EntityAddress[0] != 1 ||
		got.AllowedValueHashes[0] != expected.AllowedValueHashes[0] ||
		got.SafetyPredicates[0] != expected.SafetyPredicates[0] ||
		got.EvidenceHashes[0] != expected.EvidenceHashes[0] {
		t.Fatalf("facade acquisition observed caller mutation: %+v", got)
	}
}

func TestIssue93RuntimeConfigRejectsInvalidOrUnadmittedMutationProfiles(t *testing.T) {
	valid := validRuntimeConfig(t.TempDir())
	valid.MutationLabProfiles = []eebusraw.MutationLabProfileV1{issue93RuntimeLabProfile(t)}

	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "invalid profile", mutate: func(config *Config) {
			config.MutationLabProfiles[0].EvidenceHashes = nil
		}},
		{name: "unadmitted remote", mutate: func(config *Config) {
			config.MutationLabProfiles[0].Target.RemoteSKI = strings.Repeat("b", 40)
		}},
		{name: "duplicate profile id", mutate: func(config *Config) {
			config.MutationLabProfiles = append(
				config.MutationLabProfiles,
				config.MutationLabProfiles[0].Clone(),
			)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			config.Remotes = append([]Remote(nil), valid.Remotes...)
			config.MutationLabProfiles = []eebusraw.MutationLabProfileV1{
				valid.MutationLabProfiles[0].Clone(),
			}
			test.mutate(&config)
			if _, err := New(config); err == nil {
				t.Fatal("New() accepted invalid mutation profile configuration")
			}
		})
	}
}

func TestIssue93DisabledRuntimeCannotCarryMutationProfiles(t *testing.T) {
	if _, err := New(Config{
		MutationLabProfiles: []eebusraw.MutationLabProfileV1{issue93RuntimeLabProfile(t)},
	}); err == nil {
		t.Fatal("disabled runtime accepted mutation profile configuration")
	}
}

func issue93RuntimeLabProfile(t *testing.T) eebusraw.MutationLabProfileV1 {
	t.Helper()
	requested, err := eebusraw.NewTypedValueV1(int64(21))
	if err != nil {
		t.Fatal(err)
	}
	requestedHash, err := requested.ComputeHash()
	if err != nil {
		t.Fatal(err)
	}
	before, err := eebusraw.NewTypedValueV1(int64(20))
	if err != nil {
		t.Fatal(err)
	}
	beforeHash, err := before.ComputeHash()
	if err != nil {
		t.Fatal(err)
	}
	return eebusraw.MutationLabProfileV1{
		Contract:  "helianthus.eebus.raw-mutation-lab-profile.v1",
		ProfileID: "issue93-runtime-profile",
		Target: eebusraw.FeatureTargetV1{
			RemoteSKI:      strings.Repeat("0", 39) + "1",
			SHIPID:         "issue93-ship",
			DeviceAddress:  "issue93-device",
			EntityAddress:  []uint64{1},
			FeatureAddress: 7,
			FeatureType:    "measurement",
			FeatureRole:    eebusraw.FeatureRoleV1Server,
			Function:       "measurementListData",
			Operation:      eebusraw.OperationV1Write,
		},
		AllowedValueHashes:     []eebusraw.HashV1{requestedHash},
		RollbackValueHash:      beforeHash,
		MaximumProbeTTLSeconds: 60,
		SafetyPredicates: []string{
			"exact-target-capability-current",
			"rollback-representable",
		},
		EvidenceHashes: []eebusraw.HashV1{
			eebusraw.HashV1("sha256:" + strings.Repeat("3", 64)),
		},
		ExpiresAt: time.Unix(1_900_000_000, 0).UTC(),
	}
}
