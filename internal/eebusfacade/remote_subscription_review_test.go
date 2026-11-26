package eebusfacade

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	spinemodel "github.com/Project-Helianthus/helianthus-spine-go/model"
)

func TestRemoteSubscriptionRevokedOfflinePairedAgainSendsSubscriptionsAgain(t *testing.T) {
	fixture := newRemoteSubscriptionFixture(t, strings.Repeat("b", 40), remoteSubscriptionMeasurementSpec())
	fixture.admit(t, 4)
	fixture.establish(t, 1)
	fixture.bridge.retireRemote(fixture.remoteSKI, 4)

	fixture.setRevoked(func(ski string) bool { return ski == fixture.remoteSKI })
	fixture.bridge.runRemoteSubscriptionActions()
	if pairs := fixture.establishedPairs(); pairs != 0 {
		t.Fatalf("pairs after offline revocation = %d, want none", pairs)
	}

	fixture.setRevoked(func(string) bool { return false })
	fixture.admit(t, 5)
	remoteSubscriptionEventually(t, "the subscription is sent again after pairing again", func() bool {
		subscribed, _ := fixture.sender.calls()
		return len(subscribed) == 2
	})
}

type remoteSubscriptionEarlyReplySender struct {
	*remoteSubscriptionSender
	reply func(spinemodel.MsgCounterType)
}

func (sender *remoteSubscriptionEarlyReplySender) Request(
	classifier spinemodel.CmdClassifierType,
	source *spinemodel.FeatureAddressType,
	destination *spinemodel.FeatureAddressType,
	ack bool,
	cmd []spinemodel.CmdType,
) (*spinemodel.MsgCounterType, error) {
	counter, err := sender.remoteSubscriptionSender.Request(classifier, source, destination, ack, cmd)
	if err == nil && counter != nil {
		sender.reply(*counter)
	}
	return counter, err
}

func TestRemoteSubscriptionHeldReplyBeforeTheSendReturnsStillRenewsALostPair(t *testing.T) {
	fixture := newRemoteSubscriptionFixture(t, strings.Repeat("c", 40), remoteSubscriptionMeasurementSpec())
	fixture.admit(t, 4)
	fixture.establish(t, 1)
	fixture.bridge.retireRemote(fixture.remoteSKI, 4)
	fixture.remote.setSender(&remoteSubscriptionEarlyReplySender{
		remoteSubscriptionSender: fixture.sender,
		reply: func(counter spinemodel.MsgCounterType) {
			fixture.bridge.noteRemoteHeld(&spineapi.Message{
				CmdClassifier: spinemodel.CmdClassifierTypeReply,
				Cmd: spinemodel.CmdType{
					NodeManagementSubscriptionData: &spinemodel.NodeManagementSubscriptionDataType{},
				},
				RequestHeader: &spinemodel.HeaderType{MsgCounterReference: &counter},
				DeviceRemote:  fixture.base,
			})
		},
	})
	fixture.admit(t, 5)
	remoteSubscriptionEventually(t, "the lost pair is subscribed again", func() bool {
		subscribed, _ := fixture.sender.calls()
		return len(subscribed) == 2
	})
}

func TestRemoteSubscriptionUnreadableStoreFailsTheAttach(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	first := newRemoteSubscriptionFixture(t, strings.Repeat("d", 40), remoteSubscriptionMeasurementSpec(), root)
	first.admit(t, 4)
	first.establish(t, 1)
	if err := first.bridge.closeRemoteSubscriptions(); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(remoteSubscriptionStoreRoot(root), "feature-pairs.json")
	if err := os.WriteFile(file, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	bridge := &rawFeatureRuntimeBridge{local: first.local}
	if err := bridge.attachRemoteSubscriptionsReporting(root); err == nil {
		t.Fatal("attach with an unreadable feature-pairs file succeeded, want an error")
	}
}
