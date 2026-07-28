package eebusfacade

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
	"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusmutation"
)

type remediationMutationRuntime struct {
	mu        sync.Mutex
	binding   eebusraw.RuntimeBindingV1
	before    eebusraw.TypedValueV1
	requested eebusraw.TypedValueV1
	reads     int
}

func (runtime *remediationMutationRuntime) CurrentRuntimeBinding(
	target eebusraw.FeatureTargetV1,
) (eebusraw.RuntimeBindingV1, *eebusraw.ErrorV1) {
	if target.Operation != eebusraw.OperationV1Write &&
		target.Operation != eebusraw.OperationV1Read {
		return eebusraw.RuntimeBindingV1{}, rawMutationFacadeError(
			eebusraw.ErrorCodeV1UnsupportedOperation,
			false,
		)
	}
	return runtime.binding, nil
}

func (runtime *remediationMutationRuntime) FullReadIfCurrent(
	_ context.Context,
	target eebusraw.FeatureTargetV1,
	expected eebusraw.RuntimeBindingV1,
) (eebusmutation.ReadResult, *eebusraw.ErrorV1) {
	if target.Operation != eebusraw.OperationV1Read || expected != runtime.binding {
		return eebusmutation.ReadResult{}, rawMutationFacadeError(
			eebusraw.ErrorCodeV1ConnectionGenerationMismatch,
			false,
		)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	value := runtime.before
	if runtime.reads != 0 {
		value = runtime.requested
	}
	runtime.reads++
	return eebusmutation.ReadResult{
		Value:       value.Clone(),
		Runtime:     runtime.binding,
		Full:        true,
		Trustworthy: true,
	}, nil
}

func (runtime *remediationMutationRuntime) FullWriteIfCurrent(
	_ context.Context,
	target eebusraw.FeatureTargetV1,
	value eebusraw.TypedValueV1,
	expected eebusraw.RuntimeBindingV1,
) (eebusmutation.WriteResult, *eebusraw.ErrorV1) {
	if target.Operation != eebusraw.OperationV1Write ||
		expected != runtime.binding ||
		value.Validate() != nil {
		return eebusmutation.WriteResult{}, rawMutationFacadeError(
			eebusraw.ErrorCodeV1ConnectionGenerationMismatch,
			false,
		)
	}
	return eebusmutation.WriteResult{FrameSent: true, Correlated: true, Accepted: true}, nil
}

func (*remediationMutationRuntime) MutationPolicy(
	context.Context,
	eebusraw.FeatureTargetV1,
	eebusraw.TypedValueV1,
	eebusraw.TypedValueV1,
) (eebusmutation.PolicyDecision, *eebusraw.ErrorV1) {
	return eebusmutation.PolicyDecision{
		FullWrite:             true,
		Changeability:         eebusraw.ChangeabilityV1True,
		ConstraintsKnown:      true,
		LabAllowlisted:        true,
		RollbackRepresentable: true,
	}, nil
}

func TestRawMutationProductionTokenAndReferenceKeySurviveSameEpochRestart(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	root := filepath.Join(t.TempDir(), "state")
	const epoch = uint64(41)
	key, err := loadRawMutationReferenceKey(root, epoch)
	if err != nil {
		t.Fatal(err)
	}
	reloadedKey, err := loadRawMutationReferenceKey(root, epoch)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(key, reloadedKey) {
		t.Fatal("same runtime epoch replaced the mutation reference key")
	}
	info, err := os.Lstat(filepath.Join(root, rawMutationKeyDirectory, rawMutationKeyFilename))
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("reference key state mode = %v, error = %v", info.Mode(), err)
	}

	before, err := eebusraw.NewTypedValueV1(int64(18))
	if err != nil {
		t.Fatal(err)
	}
	requested, err := eebusraw.NewTypedValueV1(int64(20))
	if err != nil {
		t.Fatal(err)
	}
	target := eebusraw.FeatureTargetV1{
		RemoteSKI:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SHIPID:         "production-token",
		DeviceAddress:  "device",
		EntityAddress:  []uint64{1},
		FeatureAddress: 1,
		FeatureType:    "measurement",
		FeatureRole:    eebusraw.FeatureRoleV1Server,
		Function:       "measurementListData",
		Operation:      eebusraw.OperationV1Write,
	}
	readTarget := target.Clone()
	readTarget.Operation = eebusraw.OperationV1Read
	readPurposeHash, err := eebusraw.CanonicalSHA256V1(eebusraw.FeatureDataGetRequestV1{
		Targets: []eebusraw.FeatureTargetV1{readTarget},
	})
	if err != nil {
		t.Fatal(err)
	}
	beforeHash, err := before.ComputeHash()
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := newRawReadTokenIssuer(bytes.Repeat([]byte{0x4d}, 32))
	if err != nil {
		t.Fatal(err)
	}
	issuer.now = func() time.Time { return now }
	readAuth := eebusraw.ReadAuthorizationV1{
		PrincipalClass: "owner",
		Scope:          eebusraw.AuthScopeV1RawRead,
		Tool:           eebusraw.ToolV1FeaturesDataGet,
		MaskTier:       eebusraw.MaskTierRaw,
	}
	binding := eebusraw.RuntimeBindingV1{RuntimeEpoch: epoch, ConnectionGeneration: 9}
	token, err := issuer.issue(readAuth, readTarget, binding, readPurposeHash, beforeHash, now)
	if err != nil {
		t.Fatal(err)
	}
	request := eebusraw.FeatureDataSetRequestV1{
		Target:         target,
		Value:          requested,
		ReadToken:      token.ReadToken,
		IdempotencyKey: "same-epoch-production-key",
		Mode:           eebusraw.ModeV1Apply,
	}
	writeAuth := eebusraw.WriteAuthorizationV1{
		PrincipalClass: "owner",
		Scope:          eebusraw.AuthScopeV1RawWrite,
		Tool:           eebusraw.ToolV1FeaturesDataSet,
		MaskTier:       eebusraw.MaskTierRaw,
	}
	runtime := &remediationMutationRuntime{
		binding: binding, before: before, requested: requested,
	}
	open := func(referenceKey []byte) *eebusmutation.Coordinator {
		coordinator, terminal := eebusmutation.NewCoordinator(
			eebusmutation.CoordinatorConfig{
				StateRoot: root,
				RuntimeEpoch: func() uint64 {
					return epoch
				},
				Now:              func() time.Time { return now },
				WriterWait:       20 * time.Millisecond,
				RecoveryDeadline: time.Second,
				ReferenceKey:     referenceKey,
			},
			eebusmutation.CoordinatorDependencies{
				Executor: runtime, BindingAuthority: runtime,
				TokenVerifier: issuer, Policy: runtime,
			},
		)
		if terminal != nil {
			t.Fatalf("open coordinator: %+v", terminal)
		}
		return coordinator
	}
	firstCoordinator := open(key)
	first, terminal := firstCoordinator.FeaturesDataSet(context.Background(), writeAuth, request)
	if terminal != nil || first.State != eebusraw.MutationStateV1Applied {
		t.Fatalf("production-token mutation = %+v, terminal = %+v", first, terminal)
	}
	if terminal := firstCoordinator.Close(); terminal != nil {
		t.Fatalf("close first coordinator: %+v", terminal)
	}

	secondCoordinator := open(reloadedKey)
	defer secondCoordinator.Close()
	replayed, terminal := secondCoordinator.FeaturesDataSet(context.Background(), writeAuth, request)
	if terminal != nil || replayed.MutationRef != first.MutationRef {
		t.Fatalf("same-epoch replay = %+v, terminal = %+v", replayed, terminal)
	}

	rotated, err := loadRawMutationReferenceKey(root, epoch+1)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(rotated, key) {
		t.Fatal("runtime epoch reset did not replace the mutation reference key")
	}
}

func TestProductionRuntimeActivatesMutationCoordinatorBeforeFirstMutationAPI(t *testing.T) {
	harness := newMSP045ProductHarness(t, nil)
	harness.backend.mu.Lock()
	coordinator := harness.backend.rawMutations
	harness.backend.mu.Unlock()
	if coordinator == nil {
		t.Fatal("production runtime deferred mutation coordinator activation until first API call")
	}
}
