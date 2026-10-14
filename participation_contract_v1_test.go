package eebusruntime

import (
	"encoding/json"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusevidence"
	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

func participationFixture() ParticipationContributionV1 {
	transport, model := "synthetic-1", "synthetic-2"
	reference := eebusevidence.NewReferenceV1(eebusraw.RedactedID{Kind: eebusraw.IDKindPeer, Masked: "[redacted]", Digest: "sha256:" + strings.Repeat("a", 64)}, eebusevidence.CaptureProvenanceRuntimeObservation, eebusevidence.RawSnapshotScopeRoot, eebusevidence.AuthScopeReadRaw)
	captured := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	native, err := eebusevidence.NewEnvelopeV1(reference, captured, captured, nil).WithDataHash()
	if err != nil {
		panic(err)
	}

	return ParticipationContributionV1{
		Configuration: ParticipationConfigurationV1{
			Contract: ParticipationContractV1, Revision: 1, LocalParticipant: strings.Repeat("1", 32), Enabled: true,
			Listener: ParticipationListenerV1{Interface: "synthetic0", Address: netip.MustParseAddrPort("192.0.2.10:4711")},
			Bindings: []ParticipationBindingV1{{ID: strings.Repeat("2", 32), PeerParticipant: strings.Repeat("3", 32), LocalCapability: strings.Repeat("4", 32), PeerCapability: strings.Repeat("5", 32), Selection: strings.Repeat("6", 32), Direction: ParticipationDirectionV1Send, Permissions: []ParticipationPermissionV1{ParticipationPermissionV1Observe, ParticipationPermissionV1Control}}},
		},
		AppliedRevision: 1, ObservedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), ListenerState: ParticipationStateV1Active,
		Bindings: []ParticipationBindingObservationV1{{ID: strings.Repeat("2", 32), State: ParticipationStateV1Active, PeerOnline: true, Trusted: true, Compatibility: ParticipationCompatibilityV1Compatible, Admission: ParticipationAdmissionV1Promoted, Negotiated: true, Granted: []ParticipationPermissionV1{ParticipationPermissionV1Observe, ParticipationPermissionV1Control}, ControlOutcome: ParticipationControlOutcomeV1NotRequested, Failure: ParticipationFailureV1None,
			Provenance: ParticipationProvenanceV1{Native: &native, TransportVersion: &transport, DataModelVersion: &model},
			Losses:     []ParticipationLossV1{},
		}},
	}
}

func TestParticipationContractNegativeCompatibilityAndAuthorization(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ParticipationContributionV1)
	}{
		{"empty native grant", func(s *ParticipationContributionV1) { s.Bindings[0].Granted = nil }},
		{"untrusted", func(s *ParticipationContributionV1) { s.Bindings[0].Trusted = false }},
		{"incompatible", func(s *ParticipationContributionV1) {
			s.Bindings[0].Compatibility = ParticipationCompatibilityV1Incompatible
		}},
		{"unpromoted", func(s *ParticipationContributionV1) { s.Bindings[0].Admission = ParticipationAdmissionV1Candidate }},
		{"unnegotiated", func(s *ParticipationContributionV1) { s.Bindings[0].Negotiated = false }},
		{"offline", func(s *ParticipationContributionV1) { s.Bindings[0].PeerOnline = false }},
		{"saved only", func(s *ParticipationContributionV1) { s.AppliedRevision = 0 }},
		{"disabled", func(s *ParticipationContributionV1) { s.Configuration.Enabled = false }},
		{"listener failed", func(s *ParticipationContributionV1) { s.ListenerState = ParticipationStateV1Error }},
		{"unknown native version", func(s *ParticipationContributionV1) { s.Bindings[0].Provenance.DataModelVersion = nil }},
		{"missing evidence hash", func(s *ParticipationContributionV1) { s.Bindings[0].Provenance.Native.DataHash = "" }},
		{"missing evidence", func(s *ParticipationContributionV1) { s.Bindings[0].Provenance.Native = nil }},
		{"unknown loss", func(s *ParticipationContributionV1) { s.Bindings[0].Losses = nil }},
		{"extra grant", func(s *ParticipationContributionV1) {
			s.Configuration.Bindings[0].Permissions = []ParticipationPermissionV1{ParticipationPermissionV1Observe}
		}},
		{"unknown state", func(s *ParticipationContributionV1) { s.Bindings[0].State = "unknown-state" }},
		{"rejected control", func(s *ParticipationContributionV1) {
			s.Bindings[0].ControlOutcome = ParticipationControlOutcomeV1Rejected
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := participationFixture()
			tc.mutate(&s)
			if s.Validate() == nil {
				t.Fatal("invalid active contribution accepted")
			}
		})
	}
	s := participationFixture()
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := ValidateParticipationControlV1(s, s.Bindings[0].ID); err != nil {
		t.Fatal(err)
	}
	s.Configuration.Bindings[0].Permissions = []ParticipationPermissionV1{ParticipationPermissionV1Observe}
	s.Bindings[0].Granted = []ParticipationPermissionV1{ParticipationPermissionV1Observe}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if ValidateParticipationControlV1(s, s.Bindings[0].ID) == nil {
		t.Fatal("trust/active mapping granted blanket control")
	}
	if ValidateParticipationControlV1(s, strings.Repeat("9", 32)) == nil {
		t.Fatal("unknown binding authorized")
	}
}

func TestParticipationContractRestartOfflineAndReconnectPreserveIdentity(t *testing.T) {
	initial := participationFixture()
	payload, err := json.Marshal(initial.Configuration)
	if err != nil {
		t.Fatal(err)
	}
	var saved ParticipationConfigurationV1
	if err = json.Unmarshal(payload, &saved); err != nil {
		t.Fatal(err)
	}
	if err = saved.Validate(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(saved, initial.Configuration) {
		t.Fatal("restart lost identity, selection or permissions")
	}
	for _, state := range []ParticipationStateV1{ParticipationStateV1Configured, ParticipationStateV1Offline, ParticipationStateV1Incompatible, ParticipationStateV1Error} {
		s := participationFixture()
		s.Configuration = saved
		s.AppliedRevision = 0
		s.ListenerState = ParticipationStateV1Configured
		s.Bindings[0].State = state
		s.Bindings[0].PeerOnline = false
		s.Bindings[0].Compatibility = ParticipationCompatibilityV1Incompatible
		s.Bindings[0].Trusted = false
		s.Bindings[0].Negotiated = false
		s.Bindings[0].Granted = nil
		if state == ParticipationStateV1Error {
			s.Bindings[0].Failure = ParticipationFailureV1Authorization
			s.Bindings[0].ControlOutcome = ParticipationControlOutcomeV1Rejected
		}
		if err := s.Validate(); err != nil {
			t.Fatalf("%s: %v", state, err)
		}
		if ValidateParticipationControlV1(s, s.Bindings[0].ID) == nil {
			t.Fatalf("%s permits control", state)
		}
		if !reflect.DeepEqual(s.Configuration, saved) {
			t.Fatal("unavailable peer changed saved binding")
		}
	}
	reconnected := participationFixture()
	reconnected.Configuration = saved
	if err := reconnected.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestParticipationContractChangesCannotReassignBinding(t *testing.T) {
	previous := participationFixture().Configuration
	tests := []struct {
		name   string
		mutate func(*ParticipationConfigurationV1)
	}{
		{"peer", func(c *ParticipationConfigurationV1) { c.Bindings[0].PeerParticipant = strings.Repeat("9", 32) }},
		{"local capability", func(c *ParticipationConfigurationV1) { c.Bindings[0].LocalCapability = strings.Repeat("9", 32) }},
		{"peer capability", func(c *ParticipationConfigurationV1) { c.Bindings[0].PeerCapability = strings.Repeat("9", 32) }},
		{"selection", func(c *ParticipationConfigurationV1) { c.Bindings[0].Selection = strings.Repeat("9", 32) }},
		{"direction", func(c *ParticipationConfigurationV1) { c.Bindings[0].Direction = ParticipationDirectionV1Receive }},
		{"local identity", func(c *ParticipationConfigurationV1) { c.LocalParticipant = strings.Repeat("9", 32) }},
		{"stale revision", func(c *ParticipationConfigurationV1) { c.Revision = previous.Revision }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			next := participationFixture().Configuration
			next.Revision++
			tc.mutate(&next)
			if ValidateParticipationChangeV1(previous, next) == nil {
				t.Fatal("reassignment or stale update accepted")
			}
		})
	}
	next := participationFixture().Configuration
	next.Revision++
	next.Enabled = false
	if err := ValidateParticipationChangeV1(previous, next); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(next.Bindings, previous.Bindings) {
		t.Fatal("disable erased bindings")
	}
	next.Bindings = nil
	if err := ValidateParticipationChangeV1(previous, next); err != nil {
		t.Fatal(err)
	}
	replacement := participationFixture().Configuration
	replacement.Revision++
	replacement.Bindings[0].ID = strings.Repeat("9", 32)
	replacement.Bindings[0].PeerParticipant = strings.Repeat("8", 32)
	if err := ValidateParticipationChangeV1(previous, replacement); err != nil {
		t.Fatal(err)
	}
}

func TestParticipationContractRejectsMalformedConfigurationWithoutLeakingValues(t *testing.T) {
	tests := []func(*ParticipationConfigurationV1){
		func(c *ParticipationConfigurationV1) { c.Contract = "future" }, func(c *ParticipationConfigurationV1) { c.Revision = 0 },
		func(c *ParticipationConfigurationV1) { c.LocalParticipant = "secret-input" }, func(c *ParticipationConfigurationV1) { c.Listener.Interface = "*" },
		func(c *ParticipationConfigurationV1) { c.Listener.Address = netip.MustParseAddrPort("0.0.0.0:4711") },
		func(c *ParticipationConfigurationV1) { c.Bindings[0].PeerParticipant = c.LocalParticipant },
		func(c *ParticipationConfigurationV1) { c.Bindings[0].Permissions = nil }, func(c *ParticipationConfigurationV1) {
			c.Bindings[0].Permissions = []ParticipationPermissionV1{"secret-input"}
		},
		func(c *ParticipationConfigurationV1) {
			c.Bindings[0].Permissions = []ParticipationPermissionV1{ParticipationPermissionV1Control, ParticipationPermissionV1Control}
		},
		func(c *ParticipationConfigurationV1) { c.Bindings[0].Direction = "secret-input" }, func(c *ParticipationConfigurationV1) { c.Bindings = append(c.Bindings, c.Bindings[0]) },
		func(c *ParticipationConfigurationV1) { c.Bindings = make([]ParticipationBindingV1, 257) },
	}
	for i, mutate := range tests {
		c := participationFixture().Configuration
		mutate(&c)
		err := c.Validate()
		if err == nil {
			t.Fatalf("case %d accepted", i)
		}
		if strings.Contains(err.Error(), "secret-input") {
			t.Fatal("validation error exposes supplied value")
		}
	}
	s := participationFixture()
	s.Bindings = append(s.Bindings, s.Bindings[0])
	if s.Validate() == nil {
		t.Fatal("duplicate observation accepted")
	}
	s = participationFixture()
	s.Bindings = nil
	if s.Validate() == nil {
		t.Fatal("partial observation silently drops saved binding")
	}
	s = participationFixture()
	s.Bindings[0].State = ParticipationStateV1Error
	s.Bindings[0].Failure = ParticipationFailureV1Negotiation
	s.Bindings[0].ControlOutcome = ParticipationControlOutcomeV1Rejected
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if ValidateParticipationControlV1(s, s.Bindings[0].ID) == nil {
		t.Fatal("rejected control authorized")
	}
}

func TestParticipationContractLossAndAdmissionFailClosed(t *testing.T) {
	for _, admission := range []ParticipationAdmissionV1{ParticipationAdmissionV1Unknown, ParticipationAdmissionV1Candidate, ParticipationAdmissionV1Qualified, ParticipationAdmissionV1Unsupported, "future"} {
		s := participationFixture()
		s.Bindings[0].Admission = admission
		if s.Validate() == nil {
			t.Fatalf("active accepts admission %s", admission)
		}
	}
	s := participationFixture()
	s.Bindings[0].Compatibility = ParticipationCompatibilityV1Unknown
	if s.Validate() == nil {
		t.Fatal("unknown compatibility is active")
	}
	s = participationFixture()
	s.Bindings[0].Losses = []ParticipationLossV1{{NativePath: "/devices/0/observations/0", Code: ParticipationLossCodeV1Omitted}}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if ValidateParticipationControlV1(s, s.Bindings[0].ID) == nil {
		t.Fatal("unqualified projection loss permits control")
	}
	s.Bindings[0].Losses[0].ControlSafe = true
	if err := ValidateParticipationControlV1(s, s.Bindings[0].ID); err != nil {
		t.Fatal(err)
	}
	s.Bindings[0].Losses[0].Code = ParticipationLossCodeV1Unknown
	if s.Validate() == nil {
		t.Fatal("unknown loss permits active mapping")
	}
}

func TestParticipationContractInitialSetupHasSerializableUnknownEvidence(t *testing.T) {
	s := participationFixture()
	s.AppliedRevision = 0
	s.ListenerState = ParticipationStateV1Configured
	s.Bindings[0] = ParticipationBindingObservationV1{ID: s.Bindings[0].ID, State: ParticipationStateV1Configured, Compatibility: ParticipationCompatibilityV1Unknown, Admission: ParticipationAdmissionV1Unknown, ControlOutcome: ParticipationControlOutcomeV1NotRequested, Failure: ParticipationFailureV1None}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var restored ParticipationContributionV1
	if err = json.Unmarshal(payload, &restored); err != nil {
		t.Fatal(err)
	}
	if err = restored.Validate(); err != nil {
		t.Fatal(err)
	}
	if ValidateParticipationControlV1(restored, restored.Bindings[0].ID) == nil {
		t.Fatal("first setup permits control")
	}
	if restored.Bindings[0].Provenance.Native != nil || restored.Bindings[0].Losses != nil {
		t.Fatal("unknown evidence or loss assessment manufactured")
	}
}
