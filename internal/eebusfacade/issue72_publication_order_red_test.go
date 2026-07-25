package eebusfacade

import (
	"sync"
	"testing"
	"time"

	spineapi "github.com/Project-Helianthus/helianthus-spine-go/api"
	spine "github.com/Project-Helianthus/helianthus-spine-go/spine"
)

func TestIssue72ProductionSPINEApplicationCallbacksAreSerial(t *testing.T) {
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondEntered := make(chan struct{})
	handler := &issue72SerialEventHandler{
		firstEntered:  firstEntered,
		releaseFirst:  releaseFirst,
		secondEntered: secondEntered,
	}
	unsubscribe, err := subscribeRuntimeSPINEEvents(handler)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = unsubscribe()
	})

	spine.Events.Publish(spineapi.EventPayload{Ski: "first"})
	select {
	case <-firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first SPINE application callback did not start")
	}
	spine.Events.Publish(spineapi.EventPayload{Ski: "second"})
	select {
	case <-secondEntered:
		t.Fatal("newer SPINE application callback overtook the older callback")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseFirst)
	select {
	case <-secondEntered:
	case <-time.After(time.Second):
		t.Fatal("newer SPINE application callback did not run after the older callback")
	}
}

func TestIssue72OlderSnapshotCannotPublishAfterNewerLifecycleState(t *testing.T) {
	oldClockEntered := make(chan struct{})
	releaseOldClock := make(chan struct{})
	var clockMu sync.Mutex
	clockCalls := 0
	now := func() time.Time {
		clockMu.Lock()
		clockCalls++
		call := clockCalls
		clockMu.Unlock()
		if call == 1 {
			close(oldClockEntered)
			<-releaseOldClock
		}
		return time.Unix(int64(call), 0).UTC()
	}
	handler, err := newRuntimeServiceHandler(RuntimeConfig{}, "0000000000000000000000000000000000000072", now)
	if err != nil {
		t.Fatal(err)
	}
	publications := make(chan []byte, 2)
	handler.setPublisher(func(payload []byte) {
		publications <- append([]byte(nil), payload...)
	})

	oldGraph := []runtimeGraphObservation{issue72PublicationObservation("connected", 1)}
	newGraph := []runtimeGraphObservation{issue72PublicationObservation("disconnected", 2)}
	oldDone := make(chan error, 1)
	go func() {
		oldDone <- handler.publishRuntimeGraphAtRevision(oldGraph, 1)
	}()
	<-oldClockEntered

	newDone := make(chan error, 1)
	go func() {
		newDone <- handler.publishRuntimeGraphAtRevision(newGraph, 2)
	}()
	select {
	case <-publications:
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseOldClock)
	if err := <-oldDone; err != nil {
		t.Fatal(err)
	}
	if err := <-newDone; err != nil {
		t.Fatal(err)
	}
	close(publications)

	var last []byte
	count := 0
	for payload := range publications {
		last = payload
		count++
	}
	if count == 0 {
		t.Fatal("revision-ordered publication produced no snapshot")
	}
	snapshot := decodeRuntimePayload(t, last)
	if len(snapshot.Sessions) != 1 || snapshot.Sessions[0].State != "disconnected" {
		t.Fatalf("last published sessions = %+v, want newer disconnected state", snapshot.Sessions)
	}
}

type issue72SerialEventHandler struct {
	firstEntered  chan struct{}
	releaseFirst  chan struct{}
	secondEntered chan struct{}
}

func (handler *issue72SerialEventHandler) HandleEvent(payload spineapi.EventPayload) {
	switch payload.Ski {
	case "first":
		close(handler.firstEntered)
		<-handler.releaseFirst
	case "second":
		close(handler.secondEntered)
	}
}

func issue72PublicationObservation(state string, index uint64) runtimeGraphObservation {
	remoteSKI := "0000000000000000000000000000000000000172"
	return runtimeGraphObservation{
		RuntimeID:    "runtime:0000000000000000000000000000000000000072",
		LocalSKI:     "0000000000000000000000000000000000000072",
		RemoteSKI:    remoteSKI,
		ServiceIDs:   []string{"service:" + remoteSKI},
		SessionID:    "session:" + remoteSKI,
		SessionState: state,
		SessionIndex: index,
		Visible:      true,
		Since:        time.Unix(int64(index), 0).UTC(),
	}
}
