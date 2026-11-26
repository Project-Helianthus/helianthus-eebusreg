package eebusfacade

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
)

func (fixture *remoteSubscriptionFixture) establishedPairs() int {
	fixture.bridge.mu.Lock()
	instance := fixture.bridge.remoteSubscriptions.session
	fixture.bridge.mu.Unlock()
	return len(instance.Established())
}

func (fixture *remoteSubscriptionFixture) establish(t *testing.T, pairs int) {
	t.Helper()
	for counter := 1; counter <= pairs; counter++ {
		fixture.deliverResult(t, spinemodel.MsgCounterType(counter), spinemodel.ErrorNumberTypeNoError)
	}
	remoteSubscriptionEventually(t, "every subscription is established", func() bool {
		return fixture.establishedPairs() == pairs
	})
}

func (fixture *remoteSubscriptionFixture) setRevoked(revoked func(string) bool) {
	fixture.bridge.mu.Lock()
	fixture.bridge.remoteSubscriptions.revoked = revoked
	fixture.bridge.mu.Unlock()
}

func remoteSubscriptionTwoFeatureSpec() []remoteSubscriptionFeatureSpec {
	return []remoteSubscriptionFeatureSpec{
		{address: 11, featureType: spinemodel.FeatureTypeTypeMeasurement, role: spinemodel.RoleTypeServer,
			function: spinemodel.FunctionTypeMeasurementListData, readable: true},
		{address: 12, featureType: spinemodel.FeatureTypeTypeSetpoint, role: spinemodel.RoleTypeServer,
			function: spinemodel.FunctionTypeSetpointListData, readable: true},
		{address: 13, featureType: spinemodel.FeatureTypeTypeMeasurement, role: spinemodel.RoleTypeServer,
			function: spinemodel.FunctionTypeMeasurementListData, readable: true},
	}
}

func TestRemoteSubscriptionDisconnectRetainsPairsAndReconnectSendsNoDuplicate(t *testing.T) {
	fixture := newRemoteSubscriptionFixture(t, strings.Repeat("c", 40), remoteSubscriptionMeasurementSpec())
	handler := newRemoteSubscriptionHandler(t, fixture, remoteSubscriptionTrustAll)
	backend := &serviceBackend{rawFeatures: fixture.bridge, handler: handler}
	target := fixture.target(11, spinemodel.FeatureTypeTypeMeasurement, spinemodel.FunctionTypeMeasurementListData)
	fixture.admit(t, 4)
	fixture.establish(t, 1)
	handler.HandleEvent(remoteSubscriptionNotify(
		t, fixture, fixture.feature(11), spinemodel.FunctionTypeMeasurementListData,
		remoteSubscriptionMeasurementData(215),
	))
	if _, terminal := backend.FeatureNotifiedDataGet(context.Background(), remoteSubscriptionAuthorization(), target); terminal != nil {
		t.Fatalf("read before disconnect = %+v", terminal)
	}

	fixture.bridge.retireRemote(fixture.remoteSKI, 4)
	if pairs := fixture.establishedPairs(); pairs != 1 {
		t.Fatalf("pairs after temporary disconnect = %d, want the pair retained", pairs)
	}
	if _, unsubscribed := fixture.sender.calls(); len(unsubscribed) != 0 {
		t.Fatalf("temporary disconnect sent unsubscriptions: %+v", unsubscribed)
	}
	if _, terminal := backend.FeatureNotifiedDataGet(
		context.Background(), remoteSubscriptionAuthorization(), target,
	); terminal == nil || terminal.Code != eebusraw.ErrorCodeV1NotFound {
		t.Fatalf("notified read after disconnect = %+v, want not_found", terminal)
	}

	fixture.admit(t, 5)
	subscribed, unsubscribed := fixture.sender.calls()
	if len(subscribed) != 1 || len(unsubscribed) != 0 || fixture.establishedPairs() != 1 {
		t.Fatalf("after reconnect: subscriptions = %+v, unsubscriptions = %+v, pairs = %d; want no new request",
			subscribed, unsubscribed, fixture.establishedPairs())
	}
}

func TestRemoteSubscriptionReopeningTheStateRootRestoresPairsWithoutDuplicate(t *testing.T) {
	root := t.TempDir()
	specs := remoteSubscriptionTwoFeatureSpec()
	first := newRemoteSubscriptionFixture(t, strings.Repeat("d", 40), specs, root)
	first.admit(t, 4)
	first.establish(t, 3)
	if err := first.bridge.closeRemoteSubscriptions(); err != nil {
		t.Fatal(err)
	}

	reopened := newRemoteSubscriptionFixture(t, strings.Repeat("d", 40), specs, root)
	if pairs := reopened.establishedPairs(); pairs != 3 {
		t.Fatalf("restored pairs = %d, want 3", pairs)
	}
	reopened.admit(t, 4)
	reopened.admit(t, 5)
	if subscribed, unsubscribed := reopened.sender.calls(); len(subscribed) != 0 || len(unsubscribed) != 0 {
		t.Fatalf("reopened runtime sent subscriptions %+v and unsubscriptions %+v for retained pairs",
			subscribed, unsubscribed)
	}
	if pairs := reopened.establishedPairs(); pairs != 3 {
		t.Fatalf("pairs after reconnect = %d, want 3", pairs)
	}
}

func TestRemoteSubscriptionNotAllowedEventDoesNotRemovePairs(t *testing.T) {
	fixture := newRemoteSubscriptionFixture(t, strings.Repeat("e", 40), remoteSubscriptionMeasurementSpec())
	handler, _, trusted := newRemoteSubscriptionTrustSwitch(t, fixture)
	fixture.setRevoked(func(string) bool { return false })
	fixture.admit(t, 4)
	fixture.establish(t, 1)

	trusted.Delete(fixture.remoteSKI)
	handler.HandleEvent(remoteSubscriptionNotify(
		t, fixture, fixture.feature(11), spinemodel.FunctionTypeMeasurementListData,
		remoteSubscriptionMeasurementData(215),
	))
	if pairs := fixture.establishedPairs(); pairs != 1 {
		t.Fatalf("pairs after an event that is merely not allowed = %d, want the pair retained", pairs)
	}
	if _, unsubscribed := fixture.sender.calls(); len(unsubscribed) != 0 {
		t.Fatalf("not allowed event sent unsubscriptions: %+v", unsubscribed)
	}
	if active := fixture.activeSubscriptions(); active != 0 {
		t.Fatalf("not allowed remote keeps %d live entries", active)
	}
}

func TestRemoteSubscriptionRevocationRemovesPairsAndTheStoredState(t *testing.T) {
	root := t.TempDir()
	fixture := newRemoteSubscriptionFixture(t, strings.Repeat("f", 40), remoteSubscriptionTwoFeatureSpec(), root)
	handler, _, trusted := newRemoteSubscriptionTrustSwitch(t, fixture)
	fixture.admit(t, 4)
	fixture.establish(t, 3)

	trusted.Delete(fixture.remoteSKI)
	handler.HandleEvent(remoteSubscriptionNotify(
		t, fixture, fixture.feature(11), spinemodel.FunctionTypeMeasurementListData,
		remoteSubscriptionMeasurementData(215),
	))
	if pairs := fixture.establishedPairs(); pairs != 0 {
		t.Fatalf("pairs after revocation = %d, want none", pairs)
	}
	if _, unsubscribed := fixture.sender.calls(); len(unsubscribed) != 3 {
		t.Fatalf("revocation unsubscriptions = %+v, want one per pair", unsubscribed)
	}
	if err := fixture.bridge.closeRemoteSubscriptions(); err != nil {
		t.Fatal(err)
	}
	reopened := newRemoteSubscriptionFixture(t, strings.Repeat("f", 40), remoteSubscriptionTwoFeatureSpec(), root)
	if pairs := reopened.establishedPairs(); pairs != 0 {
		t.Fatalf("pairs restored after revocation = %d, want none", pairs)
	}
}

func TestRemoteSubscriptionInstancesAreIsolated(t *testing.T) {
	specs := remoteSubscriptionTwoFeatureSpec()
	first := newRemoteSubscriptionFixture(t, strings.Repeat("1", 39)+"a", specs)
	second := newRemoteSubscriptionFixture(t, strings.Repeat("2", 39)+"b", specs)
	firstHandler, _, firstTrusted := newRemoteSubscriptionTrustSwitch(t, first)
	first.admit(t, 4)
	second.admit(t, 4)
	first.establish(t, 3)
	second.establish(t, 3)

	first.bridge.retireRemote(first.remoteSKI, 4)
	if first.establishedPairs() != 3 || second.establishedPairs() != 3 {
		t.Fatalf("a disconnect on one instance changed pairs: first %d second %d",
			first.establishedPairs(), second.establishedPairs())
	}
	first.admit(t, 5)
	firstTrusted.Delete(first.remoteSKI)
	firstHandler.HandleEvent(remoteSubscriptionNotify(
		t, first, first.feature(11), spinemodel.FunctionTypeMeasurementListData,
		remoteSubscriptionMeasurementData(215),
	))
	if first.establishedPairs() != 0 || second.establishedPairs() != 3 {
		t.Fatalf("revocation on one instance changed the other: first %d second %d",
			first.establishedPairs(), second.establishedPairs())
	}
	if subscribed, unsubscribed := second.sender.calls(); len(subscribed) != 3 || len(unsubscribed) != 0 {
		t.Fatalf("second instance peer saw subscriptions %+v and unsubscriptions %+v", subscribed, unsubscribed)
	}
}

func TestRemoteSubscriptionOnePairPerFeaturePair(t *testing.T) {
	fixture := newRemoteSubscriptionFixture(t, strings.Repeat("9", 39)+"c", remoteSubscriptionTwoFeatureSpec())
	fixture.admit(t, 4)
	for round := 0; round < 3; round++ {
		if err := fixture.bridge.refreshRemote(fixture.remoteSKI, fixture.shipID, 4, fixture.remote); err != nil {
			t.Fatal(err)
		}
	}
	subscribed, _ := fixture.sender.calls()
	if len(subscribed) != 3 {
		t.Fatalf("subscriptions = %+v, want one request per server feature", subscribed)
	}
	destinations := make(map[string]string)
	sources := make(map[spinemodel.FeatureTypeType]string)
	for _, call := range subscribed {
		destinations[call.destination] = call.source
		if previous, seen := sources[call.serverType]; seen && previous != call.source {
			t.Fatalf("server type %s used two client features", call.serverType)
		}
		sources[call.serverType] = call.source
	}
	if len(destinations) != 3 || sources[spinemodel.FeatureTypeTypeMeasurement] == sources[spinemodel.FeatureTypeTypeSetpoint] {
		t.Fatalf("destinations %v, sources %v: each server feature needs its own pair, each type its own client", destinations, sources)
	}
	fixture.establish(t, 3)
	for round := 0; round < 2; round++ {
		if err := fixture.bridge.refreshRemote(fixture.remoteSKI, fixture.shipID, 4, fixture.remote); err != nil {
			t.Fatal(err)
		}
	}
	if subscribed, _ := fixture.sender.calls(); len(subscribed) != 3 || fixture.establishedPairs() != 3 {
		t.Fatalf("established pairs were requested again: %+v", subscribed)
	}
}

func TestRemoteSubscriptionResultForUnsentRequestChangesNothing(t *testing.T) {
	fixture := newRemoteSubscriptionFixture(t, strings.Repeat("4", 40), remoteSubscriptionMeasurementSpec())
	refused := spinemodel.ErrorNumberTypeGeneralError
	fixture.bridge.recordRemoteSubscriptionResult(spineapi.ResponseMessage{
		MsgCounterReference: 1,
		Data:                &spinemodel.ResultDataType{ErrorNumber: &refused},
		DeviceRemote:        fixture.remote,
	})
	fixture.admit(t, 4)
	if active := fixture.activeSubscriptions(); active != 1 {
		t.Fatalf("active subscriptions = %d, want one", active)
	}
	fixture.bridge.recordRemoteSubscriptionResult(spineapi.ResponseMessage{
		MsgCounterReference: 99,
		Data:                &spinemodel.ResultDataType{ErrorNumber: &refused},
		DeviceRemote:        fixture.remote,
	})
	if err := fixture.bridge.refreshRemote(fixture.remoteSKI, fixture.shipID, 4, fixture.remote); err != nil {
		t.Fatal(err)
	}
	if subscribed, _ := fixture.sender.calls(); len(subscribed) != 1 {
		t.Fatalf("subscriptions = %+v, want the pending request to stand", subscribed)
	}
}

func TestRemoteSubscriptionQuarantinedTrustIsNotRevocation(t *testing.T) {
	remote := bytes.Repeat([]byte{0x42}, 20)
	other := bytes.Repeat([]byte{0x43}, 20)
	coordinator := &firstTrustCoordinator{trustedRemotes: map[string]string{}}
	coordinator.controlView.associations = []firstTrustAssociationRecord{
		{subject: remote, trusted: true, active: true},
		{subject: other, trusted: false},
	}
	// in-memory trusted set emptied by quarantine or reload: not a revocation
	if coordinator.revokedAssociation(remote) {
		t.Fatal("an unknown or quarantined trust state must not read as revocation")
	}
	if coordinator.revokedAssociation(bytes.Repeat([]byte{0x44}, 20)) {
		t.Fatal("an unlisted remote must not read as revoked")
	}
	if !coordinator.revokedAssociation(other) {
		t.Fatal("an association revoked in the control view must read as revoked")
	}
}
