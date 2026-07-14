package eebusraw_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusevidence"
	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

const frozenEnvelopeV1ReplayHash = "sha256:b909695a848d7f1817711d46730bb511f510c3ad7bc517f3ee03e517076144e1"

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
		eebusevidence.CaptureProvenanceRuntimeObservation,
		eebusevidence.RawSnapshotScopeRoot,
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
	independentSum := sha256.Sum256([]byte(frozenEnvelopeV1CanonicalPayload()))
	independentHash := "sha256:" + hex.EncodeToString(independentSum[:])
	if frozenEnvelopeV1ReplayHash != independentHash {
		t.Fatalf("frozen replay hash %s is not independently reproducible from payload: %s", frozenEnvelopeV1ReplayHash, independentHash)
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
	local := snapshotEndpoint(t, eebusraw.EndpointRoleV1Local, eebusraw.IDKindLocalSKI, "a")
	remoteA := snapshotEndpoint(t, eebusraw.EndpointRoleV1Remote, eebusraw.IDKindRemoteSKI, "b")
	remoteB := snapshotEndpoint(t, eebusraw.EndpointRoleV1Remote, eebusraw.IDKindRemoteSKI, "c")
	sessionA := snapshotSession(t, "d", remoteA.ID)
	sessionB := snapshotSession(t, "e", remoteB.ID)

	first := eebusraw.NewIdentityDocumentV1(capturedAt, local)
	first.Remotes = []eebusraw.EndpointIdentityV1{remoteB, remoteA}
	first.Sessions = []eebusraw.SessionIdentityV1{sessionB, sessionA}
	first.Unknown = []eebusraw.UnknownField{
		snapshotUnknown(eebusraw.UnknownPathRemote, "f", 9),
		snapshotUnknown(eebusraw.UnknownPathDocument, "1", 8),
	}
	second := eebusraw.NewIdentityDocumentV1(capturedAt, local)
	second.Remotes = []eebusraw.EndpointIdentityV1{remoteA, remoteB}
	second.Sessions = []eebusraw.SessionIdentityV1{sessionA, sessionB}
	second.Unknown = []eebusraw.UnknownField{
		snapshotUnknown(eebusraw.UnknownPathDocument, "1", 8),
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

func snapshotEndpoint(t *testing.T, role eebusraw.EndpointRoleV1, kind eebusraw.IDKind, fill string) eebusraw.EndpointIdentityV1 {
	t.Helper()
	id, err := eebusraw.RedactID(kind, fill)
	if err != nil {
		t.Fatal(err)
	}
	return eebusraw.EndpointIdentityV1{Role: role, ID: id}
}

func snapshotSession(t *testing.T, fill string, remote eebusraw.RedactedID) eebusraw.SessionIdentityV1 {
	t.Helper()
	id, err := eebusraw.RedactID(eebusraw.IDKindSession, fill)
	if err != nil {
		t.Fatal(err)
	}
	return eebusraw.SessionIdentityV1{ID: id, RemoteID: remote}
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

func frozenEnvelopeV1CanonicalPayload() string {
	return `{"data_timestamp":"2026-07-08T14:00:00Z","objects":[{"data_timestamp":"2026-07-08T14:00:00Z","digest":"` + snapshotDigest("a") + `","kind":"identity","size":10},{"data_timestamp":"2026-07-08T14:00:00Z","digest":"` + snapshotDigest("b") + `","kind":"unknown","size":20,"unknown":[{"path":"/document/unknown","value":{"digest":"` + snapshotDigest("d") + `","masked":"[redacted]","size":12}},{"path":"/remote/unknown","value":{"digest":"` + snapshotDigest("e") + `","masked":"[redacted]","size":10}}]}],"ref":{"auth_scope":"eebus.raw.read","capture_provenance":"runtime-observation","contract":"helianthus.eebus.raw.evidence-envelope.v1","mask_tier":"redacted","runtime":{"digest":"` + snapshotDigest("c") + `","kind":"peer","masked":"[redacted]"},"scope":"raw-root"}}`
}
