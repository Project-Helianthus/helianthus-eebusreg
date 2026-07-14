package eebusraw_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusevidence"
	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

const frozenEnvelopeV1ReplayHash = "sha256:82460ef52f64f4743e504059f9f54d2ba168764cb6aca6af9763228605357152"

var (
	_ json.Marshaler = eebusraw.IdentityDocumentV1{}
	_ json.Marshaler = eebusevidence.ObjectV1{}
	_ json.Marshaler = eebusevidence.EnvelopeV1{}
)

func TestFrozenEnvelopeV1DeterministicReplay(t *testing.T) {
	dataTime := time.Date(2026, 7, 8, 14, 0, 0, 0, time.UTC)
	capturedAt := dataTime.Add(5 * time.Minute)
	ref := eebusevidence.NewReferenceV1(
		eebusraw.RedactedID{
			Kind:   eebusraw.IDKindPeer,
			Masked: "[redacted]",
			Digest: snapshotDigest("c"),
		},
		eebusevidence.ToolCapture,
		eebusevidence.ScopeWholeRoot,
		eebusevidence.AuthScopeReadRaw,
	)
	identity := eebusevidence.NewObjectV1(eebusevidence.ObjectKindIdentity, snapshotDigest("a"), 10, dataTime)
	unknown := eebusevidence.NewObjectV1(eebusevidence.ObjectKindUnknown, snapshotDigest("b"), 20, dataTime)
	unknown.Unknown = []eebusraw.UnknownField{
		snapshotUnknown(eebusraw.UnknownPathRemote, "e", 10),
		snapshotUnknown(eebusraw.UnknownPathDocument, "d", 12),
	}
	objects := []eebusevidence.ObjectV1{unknown, identity}

	first := eebusevidence.NewEnvelopeV1(ref, capturedAt, dataTime, objects)
	second := eebusevidence.NewEnvelopeV1(ref, capturedAt, dataTime, []eebusevidence.ObjectV1{identity, unknown})
	firstHash, err := first.ComputeDataHash()
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := second.ComputeDataHash()
	if err != nil {
		t.Fatal(err)
	}
	if firstHash != secondHash {
		t.Fatalf("data_hash changed with insertion order: %s != %s", firstHash, secondHash)
	}
	if firstHash != frozenEnvelopeV1ReplayHash {
		t.Fatalf("data_hash = %s, want frozen replay hash %s", firstHash, frozenEnvelopeV1ReplayHash)
	}

	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatal("canonical JSON changed with object or unknown-field insertion order")
	}
	identityAt := bytes.Index(firstJSON, []byte(snapshotDigest("a")))
	unknownAt := bytes.Index(firstJSON, []byte(snapshotDigest("b")))
	documentAt := bytes.Index(firstJSON, []byte(eebusraw.UnknownPathDocument))
	remoteAt := bytes.Index(firstJSON, []byte(eebusraw.UnknownPathRemote))
	if identityAt < 0 || unknownAt < 0 || identityAt > unknownAt {
		t.Fatalf("evidence objects are not in canonical order: %s", firstJSON)
	}
	if documentAt < 0 || remoteAt < 0 || documentAt > remoteAt {
		t.Fatalf("unknown fields are not in canonical order: %s", firstJSON)
	}

	objects[0].Unknown[0] = snapshotUnknown(eebusraw.UnknownPathSession, "f", 15)
	afterMutation, err := first.ComputeDataHash()
	if err != nil {
		t.Fatal(err)
	}
	if afterMutation != firstHash {
		t.Fatal("snapshot retained caller-owned evidence collection storage")
	}
}

func TestFrozenIdentityDocumentV1CanonicalOrder(t *testing.T) {
	capturedAt := time.Date(2026, 7, 8, 14, 0, 0, 0, time.UTC)
	local := snapshotEndpoint(t, eebusraw.EndpointRoleLocal, eebusraw.IDKindLocalSKI, "a")
	remoteA := snapshotEndpoint(t, eebusraw.EndpointRoleRemote, eebusraw.IDKindRemoteSKI, "b")
	remoteB := snapshotEndpoint(t, eebusraw.EndpointRoleRemote, eebusraw.IDKindRemoteSKI, "c")
	sessionA := snapshotSession(t, "d", remoteA.ID, eebusraw.SessionStateObserved)
	sessionB := snapshotSession(t, "e", remoteB.ID, eebusraw.SessionStateDisconnected)

	first := eebusraw.NewIdentityDocumentV1(capturedAt, local)
	first.Remotes = []eebusraw.EndpointIdentity{remoteB, remoteA}
	first.Sessions = []eebusraw.SessionIdentity{sessionB, sessionA}
	first.Unknown = []eebusraw.UnknownField{
		snapshotUnknown(eebusraw.UnknownPathRemote, "f", 9),
		snapshotUnknown(eebusraw.UnknownPathDocument, "g", 8),
	}
	second := eebusraw.NewIdentityDocumentV1(capturedAt, local)
	second.Remotes = []eebusraw.EndpointIdentity{remoteA, remoteB}
	second.Sessions = []eebusraw.SessionIdentity{sessionA, sessionB}
	second.Unknown = []eebusraw.UnknownField{
		snapshotUnknown(eebusraw.UnknownPathDocument, "g", 8),
		snapshotUnknown(eebusraw.UnknownPathRemote, "f", 9),
	}

	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatalf("identity JSON changed with insertion order:\n%s\n%s", firstJSON, secondJSON)
	}
	if first.Contract != eebusraw.IdentityContractV1 {
		t.Fatalf("identity contract = %q, want frozen v1", first.Contract)
	}
}

func snapshotEndpoint(t *testing.T, role eebusraw.EndpointRole, kind eebusraw.IDKind, fill string) eebusraw.EndpointIdentity {
	t.Helper()
	id, err := eebusraw.RedactID(kind, fill)
	if err != nil {
		t.Fatal(err)
	}
	return eebusraw.EndpointIdentity{Role: role, ID: id}
}

func snapshotSession(t *testing.T, fill string, remote eebusraw.RedactedID, state eebusraw.SessionState) eebusraw.SessionIdentity {
	t.Helper()
	id, err := eebusraw.RedactID(eebusraw.IDKindSession, fill)
	if err != nil {
		t.Fatal(err)
	}
	return eebusraw.SessionIdentity{ID: id, RemoteID: remote, State: state}
}

func snapshotUnknown(path eebusraw.UnknownPath, fill string, size int) eebusraw.UnknownField {
	return eebusraw.UnknownField{
		Path: path,
		Value: eebusraw.OpaqueValue{
			Masked: "[redacted]",
			Digest: snapshotDigest(fill),
			Size:   size,
		},
	}
}

func snapshotDigest(fill string) string {
	return "sha256:" + strings.Repeat(fill, 64)
}
