package eebusruntime

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestK11RawBrowserSnapshotsDetachedAcrossPeerRefresh(t *testing.T) {
	backend := newFakeRuntimeBackend()
	instance, err := newRuntime(validRuntimeConfig(t.TempDir()), func(context.Context, Config) (runtimeBackend, error) {
		return backend, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := instance.Shutdown(); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})
	waitRuntimeSignal(t, backend.runStarted, "K11 backend Run")
	initial := k11BrowserSnapshot(t)
	backend.publish(t, initial)
	first := k11ReadSnapshot(t, instance)
	second := k11ReadSnapshot(t, instance)
	initialJSON := k11JSON(t, second)
	k11AssertCallerIsolation(t, instance, first, second)

	// The backend seam publishes full snapshots, not per-peer deltas. B's
	// retained data and degraded observation are explicit backend input.
	refresh := k11BrowserSnapshot(t)
	refresh.Meta.DataTimestamp = refresh.Meta.DataTimestamp.Add(time.Minute)
	refresh.Meta.CapturedAt = refresh.Meta.CapturedAt.Add(time.Minute)
	refresh.Meta.DataHash = ""
	refresh.Status = RuntimeObservationV1{
		State: ObservedRuntimeStateV1Degraded,
		Degradation: &DegradationV1{
			Reason: DegradationReasonV1NoData, Since: refresh.Meta.DataTimestamp,
		},
	}
	refresh.Sessions[1].State = ObservedSessionStateV1Degraded
	refresh.Sessions[1].Since = refresh.Meta.DataTimestamp
	*refresh.Devices[0].Metadata.Values["revision"].String = "a-next"
	(*refresh.Devices[0].Opaque)[0].Source = "synthetic-peer-a-next"
	*(*(*(*refresh.Devices[0].Opaque)[0].Value.Object)["rows"].Array)[0].Scalar.String = "a-next"
	backend.publish(t, refresh)
	current := k11ReadSnapshot(t, instance)
	if !reflect.DeepEqual(current.Devices[1], second.Devices[1]) ||
		!reflect.DeepEqual(current.Services[1], second.Services[1]) ||
		!reflect.DeepEqual(current.Entities[1], second.Entities[1]) ||
		!reflect.DeepEqual(current.Features[1], second.Features[1]) ||
		!reflect.DeepEqual(current.UseCases[1], second.UseCases[1]) {
		t.Fatal("A refresh erased or spliced B's retained graph/provenance")
	}
	if *current.Devices[0].Metadata.Values["revision"].String != "a-next" ||
		(*current.Devices[0].Opaque)[0].Source != "synthetic-peer-a-next" {
		t.Fatal("A refresh did not publish A's new data and provenance together")
	}
	if current.Sessions[1].RemoteSKI != second.Sessions[1].RemoteSKI ||
		current.Sessions[1].State != ObservedSessionStateV1Degraded ||
		!current.Sessions[1].Since.Equal(refresh.Meta.DataTimestamp) ||
		!reflect.DeepEqual(current.Sessions[0], second.Sessions[0]) ||
		!reflect.DeepEqual(current.Status, refresh.Status) ||
		!current.Meta.DataTimestamp.Equal(refresh.Meta.DataTimestamp) ||
		!current.Meta.CapturedAt.Equal(refresh.Meta.CapturedAt) {
		t.Fatal("refresh changed peer ownership, failure state or observation times")
	}
	if got := k11JSON(t, second); got != initialJSON {
		t.Fatal("later publication mutated a previously returned snapshot")
	}
	k11AssertCallerIsolation(t, instance, current, k11ReadSnapshot(t, instance))
	// Backend-owned storage must also remain detached after publication.
	wantCurrent := k11JSON(t, k11ReadSnapshot(t, instance))
	*refresh.Devices[1].Metadata.Values["revision"].String = "backend-mutation"
	if got := k11JSON(t, k11ReadSnapshot(t, instance)); got != wantCurrent {
		t.Fatal("runtime retained backend-owned metadata storage")
	}
}

func k11AssertCallerIsolation(t *testing.T, instance Runtime, caller, untouched SnapshotV1) {
	t.Helper()
	want := k11JSON(t, untouched)
	redactedBefore := k11RedactedJSON(t, untouched)
	mutations := []struct {
		name  string
		apply func(*SnapshotV1)
	}{
		{"metadata-leaf", func(caller *SnapshotV1) { *caller.Devices[0].Metadata.Values["revision"].String = "caller-leaf" }},
		{"metadata-map", func(caller *SnapshotV1) { delete(caller.Devices[1].Metadata.Values, "revision") }},
		{"opaque-leaf", func(caller *SnapshotV1) {
			*(*(*(*caller.Devices[0].Opaque)[0].Value.Object)["rows"].Array)[0].Scalar.String = "caller-opaque"
		}},
		{"nested-array", func(caller *SnapshotV1) {
			rows := (*(*caller.Devices[1].Opaque)[0].Value.Object)["rows"].Array
			(*rows)[0] = OpaqueValueV1{Scalar: &OpaqueScalarV1{String: stringPointerV1ForTest("caller-row")}}
		}},
		{"nested-map", func(caller *SnapshotV1) { delete(*(*caller.Devices[0].Opaque)[0].Value.Object, "rows") }},
		{"observation-slice", func(caller *SnapshotV1) { (*caller.Devices[1].Opaque)[0].Source = "caller-source" }},
		{"scenario-slice", func(caller *SnapshotV1) { (*caller.UseCases[0].Scenarios)[0] = "caller-scenario" }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			// Start each mutation at the runtime boundary so a constructor clone
			// cannot hide an alias from a preceding mutation.
			candidate := k11ReadSnapshot(t, instance)
			mutation.apply(&candidate)
			if k11JSON(t, k11Canonical(t, candidate)) == want {
				t.Fatal("mutation did not change the returned raw value")
			}
			if k11JSON(t, caller) != want || k11JSON(t, untouched) != want || k11JSON(t, k11ReadSnapshot(t, instance)) != want {
				t.Fatal("caller mutation reached another snapshot or runtime storage")
			}
			if k11RedactedJSON(t, k11Canonical(t, candidate)) != redactedBefore ||
				k11RedactedJSON(t, untouched) != redactedBefore ||
				k11RedactedJSON(t, k11ReadSnapshot(t, instance)) != redactedBefore {
				t.Fatal("caller mutation changed the allowlisted redaction")
			}
		})
	}
}

func k11BrowserSnapshot(t *testing.T) SnapshotV1 {
	t.Helper()
	draft := rawSnapshotDraftV1(t, false)
	// Logical fixture time only; no device observation or capture is used.
	draft.Meta.DataTimestamp = time.Unix(1, 0).UTC()
	draft.Meta.CapturedAt = draft.Meta.DataTimestamp.Add(time.Second)
	draft.Pairing[0].Since = draft.Meta.DataTimestamp
	draft.Opaque = nil
	draft.Sessions = nil
	draft.UseCases = nil
	for _, peer := range []struct{ letter, ski string }{{"a", "2"}, {"b", "3"}} {
		for i := range draft.Devices {
			if draft.Devices[i].SKI != strings.Repeat(peer.ski, 40) {
				continue
			}
			revision := peer.letter + "-original"
			draft.Devices[i].Metadata = &MetadataV1{Values: map[string]MetadataValueV1{
				"revision": {String: stringPointerV1ForTest(revision)},
			}}
			rows := []OpaqueValueV1{{Scalar: &OpaqueScalarV1{String: stringPointerV1ForTest(revision)}}}
			object := map[string]OpaqueValueV1{"rows": {Array: &rows}}
			observations := []OpaqueObservationV1{{
				Path:   "/synthetic/peer-" + peer.letter,
				Source: "synthetic-peer-" + peer.letter,
				Value:  OpaqueValueV1{Object: &object},
			}}
			draft.Devices[i].Opaque = &observations
		}
		draft.Sessions = append(draft.Sessions, SessionV1{
			ID: "synthetic-session-" + peer.letter, RemoteSKI: strings.Repeat(peer.ski, 40),
			State: ObservedSessionStateV1Connected, Since: draft.Meta.DataTimestamp,
		})
		scenarios := []string{"1", "2"}
		draft.UseCases = append(draft.UseCases, UseCaseV1{
			ContextAddress: "device-" + peer.letter + ":entity-" + peer.letter + ":feature-" + peer.letter,
			Name:           "monitoring", Actor: "client", Scenarios: &scenarios,
		})
	}
	return k11Canonical(t, draft)
}

func k11Canonical(t *testing.T, draft SnapshotV1) SnapshotV1 {
	t.Helper()
	draft.Meta.DataHash = ""
	snapshot, err := NewSnapshotV1(draft)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func k11ReadSnapshot(t *testing.T, instance Runtime) SnapshotV1 {
	t.Helper()
	snapshot, err := instance.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func k11JSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func k11RedactedJSON(t *testing.T, raw SnapshotV1) string {
	t.Helper()
	redacted, err := BuildRedactedSnapshotV1(raw)
	if err != nil {
		t.Fatal(err)
	}
	return k11JSON(t, redacted)
}
