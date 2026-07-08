package eebusevidence

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

func TestReferenceBindsRequiredIdentityFields(t *testing.T) {
	ref := testReference(t)
	if err := ref.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	changed := ref
	changed.Scope = ScopeServices
	if ref.Matches(changed) {
		t.Fatal("reference matched after scope changed")
	}

	changed = ref
	changed.AuthScope = AuthScope("raw-secret-auth-scope")
	if err := changed.Validate(); err == nil {
		t.Fatal("Validate() succeeded for caller-controlled auth scope")
	}
}

func TestEnvelopeHashIsDeterministicAndOrderIndependent(t *testing.T) {
	ref := testReference(t)
	dataTime := time.Date(2026, 7, 8, 14, 0, 0, 0, time.UTC)
	objA := NewObject(ObjectKindIdentity, DigestBytes([]byte("payload-a")), 9, dataTime)
	objB := NewObject(ObjectKindService, DigestBytes([]byte("payload-b")), 9, dataTime)

	first := NewEnvelope(ref, dataTime.Add(time.Minute), dataTime, []Object{objA, objB})
	second := NewEnvelope(ref, dataTime.Add(2*time.Minute), dataTime, []Object{objB, objA})

	firstHash, err := first.ComputeDataHash()
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := second.ComputeDataHash()
	if err != nil {
		t.Fatal(err)
	}
	if firstHash != secondHash {
		t.Fatalf("hash changed after object reorder/captured_at change: %s != %s", firstHash, secondHash)
	}

	withHash, err := first.WithDataHash()
	if err != nil {
		t.Fatal(err)
	}
	recomputed, err := withHash.ComputeDataHash()
	if err != nil {
		t.Fatal(err)
	}
	if recomputed != firstHash {
		t.Fatalf("hash included data_hash: %s != %s", recomputed, firstHash)
	}
}

func TestEnvelopeHashCanonicalizesTimestamps(t *testing.T) {
	ref := testReference(t)
	dataUTC := time.Date(2026, 7, 8, 14, 0, 0, 0, time.UTC)
	dataOffset := dataUTC.In(time.FixedZone("capture-zone", 2*60*60))
	objUTC := Object{
		Kind:          ObjectKindIdentity,
		Digest:        DigestBytes([]byte("payload-a")),
		Size:          9,
		DataTimestamp: dataUTC,
	}
	objOffset := objUTC
	objOffset.DataTimestamp = dataOffset

	first := Envelope{
		Reference:     ref,
		CapturedAt:    dataUTC,
		DataTimestamp: dataUTC,
		Objects:       []Object{objUTC},
	}
	second := Envelope{
		Reference:     ref,
		CapturedAt:    dataOffset,
		DataTimestamp: dataOffset,
		Objects:       []Object{objOffset},
	}

	firstHash, err := first.ComputeDataHash()
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := second.ComputeDataHash()
	if err != nil {
		t.Fatal(err)
	}
	if firstHash != secondHash {
		t.Fatalf("hash changed for same instant in different zones: %s != %s", firstHash, secondHash)
	}
}

func TestEnvelopeHashCanonicalizesUnknownFieldOrder(t *testing.T) {
	ref := testReference(t)
	dataTime := time.Date(2026, 7, 8, 14, 0, 0, 0, time.UTC)
	firstUnknown := eebusraw.UnknownField{
		Path:  eebusraw.UnknownPathRemote,
		Value: eebusraw.OpaqueBytes([]byte("remote-raw")),
	}
	secondUnknown := eebusraw.UnknownField{
		Path:  eebusraw.UnknownPathDocument,
		Value: eebusraw.OpaqueBytes([]byte("document-raw")),
	}
	firstObject := NewObject(ObjectKindUnknown, DigestBytes([]byte("payload-a")), 9, dataTime)
	firstObject.Unknown = []eebusraw.UnknownField{firstUnknown, secondUnknown}
	secondObject := NewObject(ObjectKindUnknown, DigestBytes([]byte("payload-a")), 9, dataTime)
	secondObject.Unknown = []eebusraw.UnknownField{secondUnknown, firstUnknown}

	firstHash, err := NewEnvelope(ref, dataTime, dataTime, []Object{firstObject}).ComputeDataHash()
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := NewEnvelope(ref, dataTime, dataTime, []Object{secondObject}).ComputeDataHash()
	if err != nil {
		t.Fatal(err)
	}
	if firstHash != secondHash {
		t.Fatalf("hash changed after unknown field reorder: %s != %s", firstHash, secondHash)
	}
}

func TestEnvelopeJSONNormalizesAndSortsObjects(t *testing.T) {
	ref := testReference(t)
	dataUTC := time.Date(2026, 7, 8, 14, 0, 0, 0, time.UTC)
	dataOffset := dataUTC.In(time.FixedZone("capture-zone", 2*60*60))
	objA := Object{
		Kind:          ObjectKindIdentity,
		Digest:        DigestBytes([]byte("payload-a")),
		Size:          9,
		DataTimestamp: dataOffset,
	}
	objB := Object{
		Kind:          ObjectKindService,
		Digest:        DigestBytes([]byte("payload-b")),
		Size:          9,
		DataTimestamp: dataOffset,
	}
	env := Envelope{
		Reference:     ref,
		CapturedAt:    dataOffset,
		DataTimestamp: dataOffset,
		Objects:       []Object{objB, objA},
	}

	payload, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	if strings.Contains(text, "+02:00") || !strings.Contains(text, `"data_timestamp":"2026-07-08T14:00:00Z"`) {
		t.Fatalf("json did not normalize timestamps to UTC: %s", text)
	}
	firstDigest := strings.Index(text, objA.Digest)
	secondDigest := strings.Index(text, objB.Digest)
	if firstDigest < 0 || secondDigest < 0 || firstDigest > secondDigest {
		t.Fatalf("json did not sort objects deterministically: %s", text)
	}
}

func TestDuplicateDigestDescriptorsAreAllowed(t *testing.T) {
	ref := testReference(t)
	dataTime := time.Date(2026, 7, 8, 14, 0, 0, 0, time.UTC)
	digest := DigestBytes([]byte("shared-payload"))
	env := NewEnvelope(ref, dataTime, dataTime, []Object{
		NewObject(ObjectKindIdentity, digest, 14, dataTime),
		NewObject(ObjectKindService, digest, 14, dataTime),
	})
	if err := env.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := env.ComputeDataHash(); err != nil {
		t.Fatal(err)
	}
}

func TestEnvelopeHashChangesWithBinding(t *testing.T) {
	ref := testReference(t)
	dataTime := time.Date(2026, 7, 8, 14, 0, 0, 0, time.UTC)
	obj := NewObject(ObjectKindIdentity, DigestBytes([]byte("payload-a")), 9, dataTime)

	first := NewEnvelope(ref, dataTime, dataTime, []Object{obj})
	firstHash, err := first.ComputeDataHash()
	if err != nil {
		t.Fatal(err)
	}

	ref.Scope = ScopeServices
	second := NewEnvelope(ref, dataTime, dataTime, []Object{obj})
	secondHash, err := second.ComputeDataHash()
	if err != nil {
		t.Fatal(err)
	}
	if firstHash == secondHash {
		t.Fatal("hash did not change after reference binding changed")
	}
}

func TestEnvelopeDoesNotExposeRawPayload(t *testing.T) {
	rawRuntime := "raw-runtime-ski"
	rawPayload := "raw-payload-value"
	rawUnknown := "raw-unknown-value"
	runtimeID, err := eebusraw.RedactID(eebusraw.IDKindLocalSKI, rawRuntime)
	if err != nil {
		t.Fatal(err)
	}
	ref := NewReference(runtimeID, ToolCapture, ScopeWholeRoot, AuthScopeReadRaw)
	dataTime := time.Date(2026, 7, 8, 14, 0, 0, 0, time.UTC)
	obj := NewObject(ObjectKindUnknown, DigestBytes([]byte(rawPayload)), len(rawPayload), dataTime)
	obj.Unknown = []eebusraw.UnknownField{{
		Path:  eebusraw.UnknownPathDocument,
		Value: eebusraw.OpaqueBytes([]byte(rawUnknown)),
	}}
	env, err := NewEnvelope(ref, dataTime, dataTime, []Object{obj}).WithDataHash()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}

	combined := string(payload) + "\n" + fmt.Sprint(env) + "\n" + fmt.Sprintf("%+v %#v", env, obj)
	for _, raw := range []string{rawRuntime, rawPayload, rawUnknown} {
		if strings.Contains(combined, raw) {
			t.Fatalf("public output leaked raw value %q in %s", raw, combined)
		}
	}
}

func TestCallerControlledLabelsAreRejected(t *testing.T) {
	raw := "raw-secret-label"
	runtimeID, err := eebusraw.RedactID(eebusraw.IDKindLocalSKI, "runtime")
	if err != nil {
		t.Fatal(err)
	}
	ref := NewReference(runtimeID, ToolID(raw), ScopeWholeRoot, AuthScopeReadRaw)
	if err := ref.Validate(); err == nil {
		t.Fatal("Validate() succeeded for caller-controlled tool id")
	}
	if _, err := json.Marshal(ToolID(raw)); err == nil {
		t.Fatal("json.Marshal succeeded for caller-controlled tool id")
	}
	if _, err := json.Marshal(Scope(raw)); err == nil {
		t.Fatal("json.Marshal succeeded for caller-controlled scope")
	}
	if _, err := json.Marshal(AuthScope(raw)); err == nil {
		t.Fatal("json.Marshal succeeded for caller-controlled auth scope")
	}
	if _, err := json.Marshal(ObjectKind(raw)); err == nil {
		t.Fatal("json.Marshal succeeded for caller-controlled object kind")
	}
	for _, out := range []string{
		fmt.Sprintf("%+v %#v", ToolID(raw), ToolID(raw)),
		fmt.Sprintf("%+v %#v", Scope(raw), Scope(raw)),
		fmt.Sprintf("%+v %#v", AuthScope(raw), AuthScope(raw)),
		fmt.Sprintf("%+v %#v", ObjectKind(raw), ObjectKind(raw)),
	} {
		if strings.Contains(out, raw) {
			t.Fatalf("formatting leaked raw label: %s", out)
		}
	}
}

func TestInvalidObjectsAreRejected(t *testing.T) {
	ref := testReference(t)
	dataTime := time.Date(2026, 7, 8, 14, 0, 0, 0, time.UTC)
	env := NewEnvelope(ref, dataTime, dataTime, []Object{{
		Kind:          ObjectKindIdentity,
		Digest:        "raw-digest",
		Size:          1,
		DataTimestamp: dataTime,
	}})
	if err := env.Validate(); err == nil {
		t.Fatal("Validate() succeeded for invalid object digest")
	}
	if _, err := json.Marshal(env); err == nil {
		t.Fatal("json.Marshal succeeded for invalid object digest")
	}
}

func testReference(t *testing.T) Reference {
	t.Helper()
	runtimeID, err := eebusraw.RedactID(eebusraw.IDKindLocalSKI, "runtime-secret")
	if err != nil {
		t.Fatal(err)
	}
	return NewReference(runtimeID, ToolCapture, ScopeWholeRoot, AuthScopeReadRaw)
}
