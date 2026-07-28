package eebusraw_test

import (
	"reflect"
	"testing"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

func TestIssue85WriteAuthorizationIsDistinctAndClosedByTool(t *testing.T) {
	if reflect.TypeOf(eebusraw.WriteAuthorizationV1{}) ==
		reflect.TypeOf(eebusraw.ReadAuthorizationV1{}) {
		t.Fatal("write authorization aliases or reuses the read authorization type")
	}
	valid := eebusraw.WriteAuthorizationV1{
		PrincipalClass: "owner",
		Scope:          eebusraw.AuthScopeV1RawWrite,
		Tool:           eebusraw.ToolV1FeaturesDataSet,
		MaskTier:       eebusraw.MaskTierRaw,
	}
	if terminal := eebusraw.ValidateWriteAuthorizationV1(
		valid,
		eebusraw.ToolV1FeaturesDataSet,
	); terminal != nil {
		t.Fatalf("valid set authorization = %+v", terminal)
	}
	valid.Tool = eebusraw.ToolV1MutationsRollback
	if terminal := eebusraw.ValidateWriteAuthorizationV1(
		valid,
		eebusraw.ToolV1MutationsRollback,
	); terminal != nil {
		t.Fatalf("valid rollback authorization = %+v", terminal)
	}
	for _, mutate := range []func(*eebusraw.WriteAuthorizationV1){
		func(auth *eebusraw.WriteAuthorizationV1) { auth.PrincipalClass = "" },
		func(auth *eebusraw.WriteAuthorizationV1) { auth.Scope = eebusraw.AuthScopeV1RawRead },
		func(auth *eebusraw.WriteAuthorizationV1) { auth.Tool = eebusraw.ToolV1MutationsGet },
		func(auth *eebusraw.WriteAuthorizationV1) { auth.MaskTier = eebusraw.MaskTierRedacted },
	} {
		auth := valid
		mutate(&auth)
		terminal := eebusraw.ValidateWriteAuthorizationV1(
			auth,
			eebusraw.ToolV1MutationsRollback,
		)
		if terminal == nil || terminal.Code != eebusraw.ErrorCodeV1PermissionDenied {
			t.Fatalf("invalid write authorization = %+v, want permission_denied", terminal)
		}
	}
}

func TestIssue85MutationStatusExtendsReadAuthorizationWithoutWideningWrites(t *testing.T) {
	status := eebusraw.ReadAuthorizationV1{
		PrincipalClass: "owner",
		Scope:          eebusraw.AuthScopeV1RawRead,
		Tool:           eebusraw.ToolV1MutationsGet,
		MaskTier:       eebusraw.MaskTierRaw,
	}
	if terminal := eebusraw.ValidateReadAuthorizationV1(
		status,
		eebusraw.ToolV1MutationsGet,
	); terminal != nil {
		t.Fatalf("mutation status read authorization = %+v", terminal)
	}
	status.Tool = eebusraw.ToolV1FeaturesDataSet
	terminal := eebusraw.ValidateReadAuthorizationV1(
		status,
		eebusraw.ToolV1FeaturesDataSet,
	)
	if terminal == nil || terminal.Code != eebusraw.ErrorCodeV1PermissionDenied {
		t.Fatalf("read authorization widened to set = %+v", terminal)
	}
}

func TestIssue85NoEffectVocabularyIsTerminalAndNonRetriable(t *testing.T) {
	terminal := eebusraw.NewErrorV1(
		eebusraw.ErrorCodeV1NoEffect,
		"final value equals the before-image",
		false,
		eebusraw.SourceLayerV1Runtime,
	)
	if terminal.Code != eebusraw.ErrorCodeV1NoEffect ||
		terminal.Retriable ||
		string(eebusraw.MutationStateV1NoEffect) != "no_effect" {
		t.Fatalf("no_effect vocabulary = state:%q error:%+v", eebusraw.MutationStateV1NoEffect, terminal)
	}
}
