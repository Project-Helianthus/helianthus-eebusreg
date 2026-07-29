package eebusraw_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

func TestIssue93MutationLabProfileV1ValidatesExactBoundedContract(t *testing.T) {
	valid := issue93RawLabProfile()
	if terminal := eebusraw.ValidateMutationLabProfileV1(valid); terminal != nil {
		t.Fatalf("ValidateMutationLabProfileV1(valid) = %+v", terminal)
	}

	tests := []struct {
		name   string
		mutate func(*eebusraw.MutationLabProfileV1)
	}{
		{name: "contract", mutate: func(profile *eebusraw.MutationLabProfileV1) {
			profile.Contract = "helianthus.eebus.raw-mutation-lab-profile.v2"
		}},
		{name: "profile id", mutate: func(profile *eebusraw.MutationLabProfileV1) {
			profile.ProfileID = ""
		}},
		{name: "profile id bound", mutate: func(profile *eebusraw.MutationLabProfileV1) {
			profile.ProfileID = strings.Repeat("x", 129)
		}},
		{name: "wildcard target", mutate: func(profile *eebusraw.MutationLabProfileV1) {
			profile.Target.RemoteSKI = ""
		}},
		{name: "read target", mutate: func(profile *eebusraw.MutationLabProfileV1) {
			profile.Target.Operation = eebusraw.OperationV1Read
		}},
		{name: "allowed values absent", mutate: func(profile *eebusraw.MutationLabProfileV1) {
			profile.AllowedValueHashes = nil
		}},
		{name: "allowed values duplicate", mutate: func(profile *eebusraw.MutationLabProfileV1) {
			profile.AllowedValueHashes = append(profile.AllowedValueHashes, profile.AllowedValueHashes[0])
		}},
		{name: "rollback hash", mutate: func(profile *eebusraw.MutationLabProfileV1) {
			profile.RollbackValueHash = "sha256:ABC"
		}},
		{name: "zero ttl", mutate: func(profile *eebusraw.MutationLabProfileV1) {
			profile.MaximumProbeTTLSeconds = 0
		}},
		{name: "wider ttl", mutate: func(profile *eebusraw.MutationLabProfileV1) {
			profile.MaximumProbeTTLSeconds = 901
		}},
		{name: "safety absent", mutate: func(profile *eebusraw.MutationLabProfileV1) {
			profile.SafetyPredicates = nil
		}},
		{name: "safety duplicate", mutate: func(profile *eebusraw.MutationLabProfileV1) {
			profile.SafetyPredicates = append(profile.SafetyPredicates, profile.SafetyPredicates[0])
		}},
		{name: "evidence absent", mutate: func(profile *eebusraw.MutationLabProfileV1) {
			profile.EvidenceHashes = nil
		}},
		{name: "evidence duplicate", mutate: func(profile *eebusraw.MutationLabProfileV1) {
			profile.EvidenceHashes = append(profile.EvidenceHashes, profile.EvidenceHashes[0])
		}},
		{name: "expiry zero", mutate: func(profile *eebusraw.MutationLabProfileV1) {
			profile.ExpiresAt = time.Time{}
		}},
		{name: "expiry not utc", mutate: func(profile *eebusraw.MutationLabProfileV1) {
			profile.ExpiresAt = profile.ExpiresAt.In(time.FixedZone("issue93", 3600))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile := issue93RawLabProfile()
			test.mutate(&profile)
			if terminal := eebusraw.ValidateMutationLabProfileV1(profile); terminal == nil ||
				terminal.Code != eebusraw.ErrorCodeV1InvalidArgument {
				t.Fatalf("ValidateMutationLabProfileV1(invalid) = %+v, want invalid_argument", terminal)
			}
		})
	}
}

func TestIssue93MutationLabProfileV1CloneOwnsNestedValues(t *testing.T) {
	profile := issue93RawLabProfile()
	clone := profile.Clone()
	profile.Target.EntityAddress[0] = 99
	profile.AllowedValueHashes[0] = "sha256:" + strings.Repeat("4", 64)
	profile.SafetyPredicates[0] = "changed"
	profile.EvidenceHashes[0] = "sha256:" + strings.Repeat("5", 64)

	if clone.Target.EntityAddress[0] != 1 ||
		clone.AllowedValueHashes[0] != eebusraw.HashV1("sha256:"+strings.Repeat("1", 64)) ||
		clone.SafetyPredicates[0] != "exact-target-capability-current" ||
		clone.EvidenceHashes[0] != eebusraw.HashV1("sha256:"+strings.Repeat("3", 64)) {
		t.Fatalf("profile clone retained caller-owned slices: %+v", clone)
	}
}

func issue93RawLabProfile() eebusraw.MutationLabProfileV1 {
	return eebusraw.MutationLabProfileV1{
		Contract:  "helianthus.eebus.raw-mutation-lab-profile.v1",
		ProfileID: "issue93-exact-profile",
		Target: eebusraw.FeatureTargetV1{
			RemoteSKI:      strings.Repeat("a", 40),
			SHIPID:         "issue93-ship",
			DeviceAddress:  "issue93-device",
			EntityAddress:  []uint64{1},
			FeatureAddress: 7,
			FeatureType:    "measurement",
			FeatureRole:    eebusraw.FeatureRoleV1Server,
			Function:       "measurementListData",
			Operation:      eebusraw.OperationV1Write,
		},
		AllowedValueHashes:     []eebusraw.HashV1{"sha256:" + strings.Repeat("1", 64)},
		RollbackValueHash:      "sha256:" + strings.Repeat("2", 64),
		MaximumProbeTTLSeconds: 60,
		SafetyPredicates: []string{
			"exact-target-capability-current",
			"rollback-representable",
		},
		EvidenceHashes: []eebusraw.HashV1{"sha256:" + strings.Repeat("3", 64)},
		ExpiresAt:      time.Unix(1_900_000_000, 0).UTC(),
	}
}
