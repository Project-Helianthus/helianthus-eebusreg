package eebusfacade

import (
	"context"
	"strings"
	"testing"
	"time"

	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
)

func TestRemoteTransportContextIsPerCall(t *testing.T) {
	if RemoteTransportFrom(nil) != nil || RemoteTransportFrom(context.Background()) != nil {
		t.Fatal("an empty context carried a notice function")
	}
	if RemoteTransportFrom(WithRemoteTransport(nil, nil)) != nil {
		t.Fatal("a nil notice was stored")
	}
	bindRemoteTransport(nil, context.Background())

	var leftNotice, rightNotice RemoteTransportNotice
	left := WithRemoteTransport(context.Background(), func(notice RemoteTransportNotice) { leftNotice = notice })
	right := WithRemoteTransport(context.Background(), func(notice RemoteTransportNotice) { rightNotice = notice })
	RemoteTransportFrom(left)(RemoteTransportNotice{Kind: RemoteTransportConnected, SKI: "left"})
	RemoteTransportFrom(right)(RemoteTransportNotice{Kind: RemoteTransportDisconnected, SKI: "right"})
	if leftNotice.Kind != RemoteTransportConnected || leftNotice.SKI != "left" || rightNotice.Kind != RemoteTransportDisconnected || rightNotice.SKI != "right" {
		t.Fatalf("notices left %+v right %+v", leftNotice, rightNotice)
	}

	leftHandler := pairingTransportHandler(t)
	rightHandler := pairingTransportHandler(t)
	bindRemoteTransport(leftHandler, left)
	bindRemoteTransport(rightHandler, right)
	leftHandler.notifyTransport(RemoteTransportDisconnected, "still-left")
	rightHandler.notifyTransport(RemoteTransportConnected, "still-right")
	if leftNotice.Kind != RemoteTransportDisconnected || leftNotice.SKI != "still-left" || rightNotice.Kind != RemoteTransportConnected || rightNotice.SKI != "still-right" {
		t.Fatalf("bound notices left %+v right %+v", leftNotice, rightNotice)
	}
	quiet := pairingTransportHandler(t)
	quiet.notifyTransport(RemoteTransportConnected, "quiet")
	if leftNotice.SKI != "still-left" || rightNotice.SKI != "still-right" {
		t.Fatal("an unbound handler changed a bound notice")
	}
}

func pairingTransportHandler(t *testing.T) *runtimeServiceHandler {
	t.Helper()
	handler, err := newRuntimeServiceHandler(RuntimeConfig{}, strings.Repeat("e", 40), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func TestRuntimePairingDisconnectReconnectAndRevocation(t *testing.T) {
	firstSKI := strings.Repeat("a", 40)
	secondSKI := strings.Repeat("b", 40)
	specs := remoteSubscriptionTwoFeatureSpec()
	first := newRemoteSubscriptionFixture(t, firstSKI, specs)
	second := newRemoteSubscriptionFixture(t, secondSKI, specs)
	firstHandler, _, firstTrusted := newRemoteSubscriptionTrustSwitch(t, first)
	secondHandler, _, _ := newRemoteSubscriptionTrustSwitch(t, second)
	var firstNotices, secondNotices []RemoteTransportNotice
	bindRemoteTransport(firstHandler, WithRemoteTransport(context.Background(), func(notice RemoteTransportNotice) {
		firstNotices = append(firstNotices, notice)
	}))
	bindRemoteTransport(secondHandler, WithRemoteTransport(context.Background(), func(notice RemoteTransportNotice) {
		secondNotices = append(secondNotices, notice)
	}))

	firstHandler.RemoteSKIConnected(nil, "not-a-ski")
	firstHandler.RemoteSKIConnected(nil, strings.Repeat("c", 40))
	if len(firstNotices) != 0 || len(secondNotices) != 0 {
		t.Fatalf("rejected connect notified first %+v second %+v", firstNotices, secondNotices)
	}

	firstHandler.RemoteSKIConnected(nil, first.remoteSKI)
	firstHandler.ServiceShipIDUpdate(first.remoteSKI, first.shipID)
	subscribed, unsubscribed := first.sender.calls()
	if len(subscribed) != 3 || pairingDestinationCount(subscribed) != 3 || len(unsubscribed) != 0 {
		t.Fatalf("subscriptions %+v unsubscriptions %+v error %v", subscribed, unsubscribed, pairingHandlerError(firstHandler))
	}
	first.establish(t, 3)
	if first.establishedPairs() != 3 || first.activeSubscriptions() != 3 {
		t.Fatalf("pairs %d active %d", first.establishedPairs(), first.activeSubscriptions())
	}
	if len(firstNotices) != 1 || firstNotices[0].Kind != RemoteTransportConnected || firstNotices[0].SKI != firstSKI || len(secondNotices) != 0 {
		t.Fatalf("notices first %+v second %+v", firstNotices, secondNotices)
	}

	firstHandler.RemoteSKIDisconnected(nil, first.remoteSKI)
	if first.establishedPairs() != 3 || first.activeSubscriptions() != 0 {
		t.Fatalf("after disconnect pairs %d active %d", first.establishedPairs(), first.activeSubscriptions())
	}
	if _, unsubscribed = first.sender.calls(); len(unsubscribed) != 0 {
		t.Fatalf("disconnect unsubscribed %+v", unsubscribed)
	}
	if len(firstNotices) != 2 || firstNotices[1].Kind != RemoteTransportDisconnected || firstNotices[1].SKI != firstSKI {
		t.Fatalf("disconnect notice %+v", firstNotices)
	}

	firstHandler.RemoteSKIConnected(nil, first.remoteSKI)
	firstHandler.ServiceShipIDUpdate(first.remoteSKI, first.shipID)
	subscribed, unsubscribed = first.sender.calls()
	if len(subscribed) != 3 || len(unsubscribed) != 0 || first.establishedPairs() != 3 || first.activeSubscriptions() != 3 {
		t.Fatalf("reconnect subscriptions %d unsubscriptions %d pairs %d active %d error %v", len(subscribed), len(unsubscribed), first.establishedPairs(), first.activeSubscriptions(), pairingHandlerError(firstHandler))
	}
	if len(firstNotices) != 3 || firstNotices[2].Kind != RemoteTransportConnected || firstNotices[2].SKI != firstSKI {
		t.Fatalf("reconnect notice %+v", firstNotices)
	}

	secondHandler.RemoteSKIConnected(nil, second.remoteSKI)
	secondHandler.ServiceShipIDUpdate(second.remoteSKI, second.shipID)
	second.establish(t, 3)
	if second.establishedPairs() != 3 || len(secondNotices) != 1 || secondNotices[0].Kind != RemoteTransportConnected || secondNotices[0].SKI != secondSKI || len(firstNotices) != 3 {
		t.Fatalf("second pairs %d notices first %+v second %+v", second.establishedPairs(), firstNotices, secondNotices)
	}

	firstTrusted.Delete(firstSKI)
	firstHandler.HandleEvent(remoteSubscriptionNotify(
		t, first, first.feature(11), spinemodel.FunctionTypeMeasurementListData, remoteSubscriptionMeasurementData(215),
	))
	if first.establishedPairs() != 0 || second.establishedPairs() != 3 {
		t.Fatalf("revocation pairs first %d second %d", first.establishedPairs(), second.establishedPairs())
	}
	if _, unsubscribed = first.sender.calls(); len(unsubscribed) != 3 {
		t.Fatalf("revocation unsubscriptions %+v", unsubscribed)
	}
	if _, unsubscribed = second.sender.calls(); len(unsubscribed) != 0 {
		t.Fatalf("second remote unsubscribed %+v", unsubscribed)
	}
	if len(firstNotices) != 3 || len(secondNotices) != 1 {
		t.Fatalf("revocation changed transport notices first %+v second %+v", firstNotices, secondNotices)
	}
}

func pairingDestinationCount(calls []remoteSubscriptionCall) int {
	seen := map[string]struct{}{}
	for _, call := range calls {
		seen[call.destination] = struct{}{}
	}
	return len(seen)
}

func pairingHandlerError(handler *runtimeServiceHandler) error {
	select {
	case err := <-handler.errors:
		return err
	default:
		return nil
	}
}
