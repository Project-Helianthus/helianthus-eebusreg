package main

import (
	"strings"
	"testing"
)

func TestIssue137ParticipationDriverPreservesNativeAdminBoundary(t *testing.T) {
	doc, err := extract(moduleRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	root := msp05pRootSurface(t, doc)
	want := map[string]string{
		"type ParticipationDriverV1":                   "type ParticipationDriverV1 interface{ ApplyParticipation(context.Context, ParticipationApplyRequestV1) (ParticipationApplyResultV1, error); OperatorAdmin() AdminV1; Participation(context.Context) (ParticipationContributionV1, error) }",
		"type ParticipationApplyRequestV1":             `type ParticipationApplyRequestV1 struct{ MutationPreconditionV1; Configuration ParticipationConfigurationV1 }`,
		"func ValidateParticipationChangeV1":           "func ValidateParticipationChangeV1(ParticipationConfigurationV1, ParticipationConfigurationV1) error",
		"func ValidateParticipationControlV1":          "func ValidateParticipationControlV1(ParticipationContributionV1, string) error",
		"method ParticipationConfigurationV1.Validate": "func (ParticipationConfigurationV1) Validate() error",
		"method ParticipationApplyResultV1.Validate":   "func (ParticipationApplyResultV1) Validate() error",
		"method ParticipationContributionV1.Validate":  "func (ParticipationContributionV1) Validate() error",
	}
	got := map[string]string{}
	for _, symbol := range root.Symbols {
		name := symbol.Kind + " " + symbol.Name
		if symbol.Receiver != nil {
			name = symbol.Kind + " " + symbol.Receiver.Base + "." + symbol.Name
		}
		if _, ok := want[name]; ok {
			got[name] = symbol.Signature
		}
	}
	for name, signature := range want {
		if got[name] != signature {
			t.Errorf("%s = %q, want %q", name, got[name], signature)
		}
	}
}

var issue137ParticipationInventory = map[string]bool{
	"const ParticipationContractV1":                   true,
	"const ParticipationCompatibilityV1Unknown":       true,
	"const ParticipationCompatibilityV1Compatible":    true,
	"const ParticipationCompatibilityV1Incompatible":  true,
	"const ParticipationAdmissionV1Unknown":           true,
	"const ParticipationAdmissionV1Candidate":         true,
	"const ParticipationAdmissionV1Qualified":         true,
	"const ParticipationAdmissionV1Promoted":          true,
	"const ParticipationAdmissionV1Unsupported":       true,
	"const ParticipationControlOutcomeV1Accepted":     true,
	"const ParticipationControlOutcomeV1NotRequested": true,
	"const ParticipationControlOutcomeV1Rejected":     true,
	"const ParticipationControlOutcomeV1Unknown":      true,
	"const ParticipationDirectionV1Receive":           true,
	"const ParticipationDirectionV1Send":              true,
	"const ParticipationFailureV1Authorization":       true,
	"const ParticipationFailureV1Listener":            true,
	"const ParticipationFailureV1Negotiation":         true,
	"const ParticipationFailureV1None":                true,
	"const ParticipationFailureV1Persistence":         true,
	"const ParticipationFailureV1Recovery":            true,
	"const ParticipationLossCodeV1Omitted":            true,
	"const ParticipationLossCodeV1Transformed":        true,
	"const ParticipationLossCodeV1Unknown":            true,
	"const ParticipationPermissionV1Control":          true,
	"const ParticipationPermissionV1Observe":          true,
	"const ParticipationStateV1Active":                true,
	"const ParticipationStateV1Configured":            true,
	"const ParticipationStateV1Error":                 true,
	"const ParticipationStateV1Incompatible":          true,
	"const ParticipationStateV1Offline":               true,
	"func ParticipationConfigurationV1.Validate":      true,
	"func ParticipationContributionV1.Validate":       true,
	"func ParticipationApplyResultV1.Validate":        true,
	"func ValidateParticipationChangeV1":              true,
	"func ValidateParticipationControlV1":             true,
	"type ParticipationApplyRequestV1":                true,
	"type ParticipationApplyResultV1":                 true,
	"type ParticipationCompatibilityV1":               true,
	"type ParticipationAdmissionV1":                   true,
	"type ParticipationBindingObservationV1":          true,
	"type ParticipationBindingV1":                     true,
	"type ParticipationConfigurationV1":               true,
	"type ParticipationContractVersionV1":             true,
	"type ParticipationContributionV1":                true,
	"type ParticipationControlOutcomeV1":              true,
	"type ParticipationDirectionV1":                   true,
	"type ParticipationDriverV1":                      true,
	"type ParticipationFailureV1":                     true,
	"type ParticipationListenerV1":                    true,
	"type ParticipationLossCodeV1":                    true,
	"type ParticipationLossV1":                        true,
	"type ParticipationPermissionV1":                  true,
	"type ParticipationProvenanceV1":                  true,
	"type ParticipationStateV1":                       true,
}

func issue137ParticipationSymbol(symbol symbol) bool {
	kind, name := symbol.Kind, symbol.Name
	if symbol.Receiver != nil {
		kind = "func"
		name = symbol.Receiver.Base + "." + name
	}
	return issue137ParticipationInventory[kind+" "+name]
}
func TestIssue137ParticipationExportInventoryIsClosed(t *testing.T) {
	doc, err := extract(moduleRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	root := msp05pRootSurface(t, doc)
	count := 0
	for _, symbol := range root.Symbols {
		related := strings.HasPrefix(symbol.Name, "Participation") || strings.HasPrefix(symbol.Name, "ValidateParticipation") || (symbol.Receiver != nil && strings.HasPrefix(symbol.Receiver.Base, "Participation"))
		if !related {
			continue
		}
		if !issue137ParticipationSymbol(symbol) {
			t.Fatalf("unreviewed participation export: %s", symbol.Signature)
		}
		count++
	}
	if count != len(issue137ParticipationInventory) {
		t.Fatalf("participation exports = %d, want %d", count, len(issue137ParticipationInventory))
	}
}
