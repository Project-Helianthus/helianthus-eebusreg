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

	changed = ref
	changed.Runtime.Digest = ""
	if err := changed.Validate(); err == nil {
		t.Fatal("Validate() succeeded without runtime digest")
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

func TestDigestValidationRequiresLowercase(t *testing.T) {
	ref := testReference(t)
	dataTime := time.Date(2026, 7, 8, 14, 0, 0, 0, time.UTC)
	upperDigest := uppercaseDigest(DigestBytes([]byte("payload-a")))

	changedRef := ref
	changedRef.Runtime.Digest = upperDigest
	if err := changedRef.Validate(); err == nil {
		t.Fatal("Validate() succeeded for uppercase runtime digest")
	}

	obj := NewObject(ObjectKindIdentity, upperDigest, 9, dataTime)
	if err := obj.Validate(); err == nil {
		t.Fatal("Validate() succeeded for uppercase object digest")
	}

	unknownValue := eebusraw.OpaqueBytes([]byte("unknown"))
	unknownValue.Digest = upperDigest
	obj = NewObject(ObjectKindUnknown, DigestBytes([]byte("payload-b")), 9, dataTime)
	obj.Unknown = []eebusraw.UnknownField{{
		Path:  eebusraw.UnknownPathDocument,
		Value: unknownValue,
	}}
	if err := obj.Validate(); err == nil {
		t.Fatal("Validate() succeeded for uppercase unknown digest")
	}

	env, err := NewEnvelope(ref, dataTime, dataTime, []Object{
		NewObject(ObjectKindIdentity, DigestBytes([]byte("payload-c")), 9, dataTime),
	}).WithDataHash()
	if err != nil {
		t.Fatal(err)
	}
	env.DataHash = uppercaseDigest(env.DataHash)
	if err := env.Validate(); err == nil {
		t.Fatal("Validate() succeeded for uppercase data_hash")
	}
}

func TestNewEnvelopeDeepCopiesObjectUnknownFields(t *testing.T) {
	ref := testReference(t)
	dataTime := time.Date(2026, 7, 8, 14, 0, 0, 0, time.UTC)
	object := NewObject(ObjectKindUnknown, DigestBytes([]byte("payload-a")), 9, dataTime)
	object.Unknown = []eebusraw.UnknownField{{
		Path:  eebusraw.UnknownPathDocument,
		Value: eebusraw.OpaqueBytes([]byte("document-raw")),
	}}
	objects := []Object{object}
	env := NewEnvelope(ref, dataTime, dataTime, objects)
	before, err := env.ComputeDataHash()
	if err != nil {
		t.Fatal(err)
	}

	objects[0].Unknown[0] = eebusraw.UnknownField{
		Path:  eebusraw.UnknownPathRemote,
		Value: eebusraw.OpaqueBytes([]byte("remote-raw")),
	}
	after, err := env.ComputeDataHash()
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("hash changed after caller mutated source object unknown field: %s != %s", before, after)
	}
	if env.Objects[0].Unknown[0].Path != eebusraw.UnknownPathDocument {
		t.Fatalf("envelope unknown field was aliased to caller memory: %+v", env.Objects[0].Unknown[0])
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

func TestEnvelopeRejectsForgedDataHash(t *testing.T) {
	ref := testReference(t)
	dataTime := time.Date(2026, 7, 8, 14, 0, 0, 0, time.UTC)
	obj := NewObject(ObjectKindIdentity, DigestBytes([]byte("payload-a")), 9, dataTime)
	env, err := NewEnvelope(ref, dataTime, dataTime, []Object{obj}).WithDataHash()
	if err != nil {
		t.Fatal(err)
	}
	env.Reference.Scope = ScopeServices
	if err := env.Validate(); err == nil {
		t.Fatal("Validate() succeeded for stale data_hash after envelope mutation")
	}
	if _, err := json.Marshal(env); err == nil {
		t.Fatal("json.Marshal succeeded for stale data_hash after envelope mutation")
	}
	hash, err := env.ComputeDataHash()
	if err != nil {
		t.Fatal(err)
	}
	if hash == env.DataHash {
		t.Fatal("ComputeDataHash returned stale data_hash")
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

func TestEnvelopeV1HashExcludesCaptureMetadata(t *testing.T) {
	ref := testReferenceV1(t)
	dataTime := time.Date(2026, 7, 8, 14, 0, 0, 0, time.UTC)
	object := NewObjectV1(ObjectKindIdentity, DigestBytes([]byte("stable-payload")), 14, dataTime)
	first := NewEnvelopeV1(ref, dataTime, dataTime, []ObjectV1{object})
	second := NewEnvelopeV1(ref, dataTime.Add(10*time.Minute), dataTime, []ObjectV1{object})

	firstHash, err := first.ComputeDataHash()
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := second.ComputeDataHash()
	if err != nil {
		t.Fatal(err)
	}
	if firstHash != secondHash {
		t.Fatalf("stable hash included captured_at: %s != %s", firstHash, secondHash)
	}
	sealed, err := first.WithDataHash()
	if err != nil {
		t.Fatal(err)
	}
	recomputed, err := sealed.ComputeDataHash()
	if err != nil {
		t.Fatal(err)
	}
	if recomputed != firstHash {
		t.Fatalf("stable hash included data_hash: %s != %s", recomputed, firstHash)
	}
}

func TestEnvelopeV1WithDataHashCopiesNestedUnknowns(t *testing.T) {
	ref := testReferenceV1(t)
	dataTime := time.Date(2026, 7, 8, 14, 0, 0, 0, time.UTC)
	object := NewObjectV1(ObjectKindUnknown, DigestBytes([]byte("stable-payload")), 14, dataTime)
	object.Unknown = []eebusraw.UnknownField{{
		Path:  eebusraw.UnknownPathDocument,
		Value: eebusraw.OpaqueBytes([]byte("stable-unknown")),
	}}
	envelope := NewEnvelopeV1(ref, dataTime, dataTime, []ObjectV1{object})
	sealed, err := envelope.WithDataHash()
	if err != nil {
		t.Fatal(err)
	}
	envelope.Objects[0].Unknown[0] = eebusraw.UnknownField{
		Path:  eebusraw.UnknownPathRemote,
		Value: eebusraw.OpaqueBytes([]byte("mutated")),
	}
	if sealed.Objects[0].Unknown[0].Path != eebusraw.UnknownPathDocument {
		t.Fatal("WithDataHash retained source envelope nested storage")
	}
	if err := sealed.Validate(); err != nil {
		t.Fatalf("sealed envelope changed after source mutation: %v", err)
	}
}

func TestEnvelopeContractVersionsRemainIsolated(t *testing.T) {
	alpha := testReference(t)
	alpha.Contract = EnvelopeContractV1
	if err := alpha.Validate(); err == nil {
		t.Fatal("v1alpha1 reference accepted stable v1 contract")
	}
	stable := testReferenceV1(t)
	stable.Contract = EnvelopeContractV1Alpha1
	if err := stable.Validate(); err == nil {
		t.Fatal("stable v1 reference accepted v1alpha1 contract")
	}
}

func TestEnvelopeV1ValidationAndFormattingDoNotLeak(t *testing.T) {
	raw := "raw-secret-v1-label"
	ref := testReferenceV1(t)
	ref.CaptureProvenance = CaptureProvenanceV1(raw)
	err := ref.Validate()
	if err == nil {
		t.Fatal("Validate() succeeded for caller-controlled stable capture provenance")
	}
	combined := err.Error() + "\n" + fmt.Sprint(ref) + "\n" + fmt.Sprintf("%+v %#v", ref, ref)
	if strings.Contains(combined, raw) {
		t.Fatalf("stable reference validation or formatting leaked raw input: %s", combined)
	}
}

func TestReferenceV1BindsStableRawCaptureFields(t *testing.T) {
	ref := testReferenceV1(t)
	if err := ref.Validate(); err != nil {
		t.Fatal(err)
	}

	changed := ref
	changed.Scope = RawSnapshotScopeServices
	if ref.Matches(changed) {
		t.Fatal("reference matched after raw snapshot scope changed")
	}

	changed = ref
	changed.CaptureProvenance = CaptureProvenanceV1("final-tool-name")
	if err := changed.Validate(); err == nil {
		t.Fatal("Validate() accepted caller-controlled capture provenance")
	}

	changed = ref
	changed.AuthScope = AuthScope("raw-secret-auth-scope")
	if err := changed.Validate(); err == nil {
		t.Fatal("Validate() accepted caller-controlled effective auth scope")
	}

	payload, err := json.Marshal(ref)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), `"tool"`) || strings.Contains(string(payload), "eebus.v1.") {
		t.Fatalf("stable reference JSON leaked final MCP identity: %s", payload)
	}
	for _, required := range []string{`"capture_provenance":"runtime-observation"`, `"scope":"raw-root"`, `"auth_scope":"eebus.raw.read"`} {
		if !strings.Contains(string(payload), required) {
			t.Fatalf("stable reference JSON omitted %s: %s", required, payload)
		}
	}
}

func TestStableV1DigestValidationRequiresLowercaseHex(t *testing.T) {
	dataTime := time.Date(2026, 7, 8, 14, 0, 0, 0, time.UTC)
	valid := DigestBytes([]byte("stable-payload"))
	uppercase := uppercaseDigest(valid)
	nonHex := "sha256:" + strings.Repeat("g", 64)

	for _, digest := range []string{uppercase, nonHex} {
		ref := testReferenceV1(t)
		ref.Runtime.Digest = digest
		if err := ref.Validate(); err == nil {
			t.Fatalf("ReferenceV1 accepted malformed runtime digest %q", digest)
		}

		object := NewObjectV1(ObjectKindIdentity, digest, 1, dataTime)
		if err := object.Validate(); err == nil {
			t.Fatalf("ObjectV1 accepted malformed object digest %q", digest)
		}

		object = NewObjectV1(ObjectKindUnknown, valid, 1, dataTime)
		object.Unknown = []eebusraw.UnknownField{{
			Path:  eebusraw.UnknownPathDocument,
			Value: eebusraw.OpaqueValue{Masked: "[redacted]", Digest: digest},
		}}
		if err := object.Validate(); err == nil {
			t.Fatalf("ObjectV1 accepted malformed unknown digest %q", digest)
		}

		envelope := NewEnvelopeV1(testReferenceV1(t), dataTime, dataTime, []ObjectV1{
			NewObjectV1(ObjectKindIdentity, valid, 1, dataTime),
		})
		envelope.DataHash = digest
		if err := envelope.Validate(); err == nil {
			t.Fatalf("EnvelopeV1 accepted malformed data_hash %q", digest)
		}
	}
}

func TestEnvelopeV1RejectsForgedDataHash(t *testing.T) {
	dataTime := time.Date(2026, 7, 8, 14, 0, 0, 0, time.UTC)
	object := NewObjectV1(ObjectKindIdentity, DigestBytes([]byte("stable-payload")), 14, dataTime)
	envelope, err := NewEnvelopeV1(testReferenceV1(t), dataTime, dataTime, []ObjectV1{object}).WithDataHash()
	if err != nil {
		t.Fatal(err)
	}
	envelope.Reference.Scope = RawSnapshotScopeServices
	if err := envelope.Validate(); err == nil {
		t.Fatal("Validate() accepted forged stable data_hash after reference mutation")
	}
	if _, err := json.Marshal(envelope); err == nil {
		t.Fatal("json.Marshal accepted forged stable data_hash after reference mutation")
	}
	recomputed, err := envelope.ComputeDataHash()
	if err != nil {
		t.Fatal(err)
	}
	if recomputed == envelope.DataHash {
		t.Fatal("ComputeDataHash returned forged stable data_hash")
	}
}

func TestStableV1RejectsUnrepresentableTimestamps(t *testing.T) {
	validTime := time.Date(2026, 7, 8, 14, 0, 0, 0, time.UTC)
	invalidTime := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	object := NewObjectV1(ObjectKindIdentity, DigestBytes([]byte("stable-payload")), 14, invalidTime)
	if err := object.Validate(); err == nil {
		t.Fatal("ObjectV1 accepted timestamp outside RFC3339 JSON range")
	}
	if _, err := json.Marshal(object); err == nil {
		t.Fatal("json.Marshal accepted ObjectV1 timestamp outside RFC3339 JSON range")
	}

	validObject := NewObjectV1(ObjectKindIdentity, DigestBytes([]byte("stable-payload")), 14, validTime)
	for _, envelope := range []EnvelopeV1{
		NewEnvelopeV1(testReferenceV1(t), invalidTime, validTime, []ObjectV1{validObject}),
		NewEnvelopeV1(testReferenceV1(t), validTime, invalidTime, []ObjectV1{validObject}),
	} {
		if err := envelope.Validate(); err == nil {
			t.Fatal("EnvelopeV1 accepted timestamp outside RFC3339 JSON range")
		}
		if _, err := envelope.ComputeDataHash(); err == nil {
			t.Fatal("ComputeDataHash accepted timestamp outside RFC3339 JSON range")
		}
		if _, err := json.Marshal(envelope); err == nil {
			t.Fatal("json.Marshal accepted EnvelopeV1 timestamp outside RFC3339 JSON range")
		}
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

func testReferenceV1(t *testing.T) ReferenceV1 {
	t.Helper()
	runtimeID, err := eebusraw.RedactID(eebusraw.IDKindLocalSKI, "runtime-secret-v1")
	if err != nil {
		t.Fatal(err)
	}
	return NewReferenceV1(runtimeID, CaptureProvenanceRuntimeObservation, RawSnapshotScopeRoot, AuthScopeReadRaw)
}

func uppercaseDigest(digest string) string {
	return "sha256:" + strings.ToUpper(strings.TrimPrefix(digest, "sha256:"))
}
