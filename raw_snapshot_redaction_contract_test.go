package eebusruntime

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

func TestRawSnapshotValidationAndMarshalRequireMatchingDataHash(t *testing.T) {
	valid := issue77Snapshot(t)
	hashless := valid.Clone()
	hashless.Meta.DataHash = ""
	if err := hashless.Validate(); err == nil {
		t.Fatal("Validate accepted a hashless externally visible snapshot")
	}
	if _, err := json.Marshal(hashless); err == nil {
		t.Fatal("MarshalJSON accepted a hashless externally visible snapshot")
	}
	if hash, err := hashless.ComputeDataHash(); err != nil || hash != valid.Meta.DataHash {
		t.Fatalf("internal ComputeDataHash on hashless draft = %q, %v; want %q", hash, err, valid.Meta.DataHash)
	}
	if rebuilt, err := NewSnapshotV1(hashless); err != nil || rebuilt.Meta.DataHash != valid.Meta.DataHash {
		t.Fatalf("NewSnapshotV1 on hashless draft = %q, %v", rebuilt.Meta.DataHash, err)
	}

	mismatched := valid.Clone()
	mismatched.Meta.DataHash = "sha256:" + strings.Repeat("0", 64)
	if err := mismatched.Validate(); err == nil {
		t.Fatal("Validate accepted a well-formed but mismatched data_hash")
	}
	if _, err := json.Marshal(mismatched); err == nil {
		t.Fatal("MarshalJSON accepted a well-formed but mismatched data_hash")
	}
}

func TestRawSnapshotRejectsPrivateKeyArmorAtEveryNestedTier(t *testing.T) {
	armors := []string{
		"-----BEGIN PRIVATE KEY-----",
		"-----BEGIN RSA PRIVATE KEY-----",
		"-----BEGIN EC PRIVATE KEY-----",
		"-----BEGIN ENCRYPTED PRIVATE KEY-----",
		"-----BEGIN OPENSSH PRIVATE KEY-----",
		"-----BEGIN DSA PRIVATE KEY-----",
		"-----BEGIN PGP PRIVATE KEY BLOCK-----",
	}
	for _, armor := range armors {
		name := strings.ToLower(strings.ReplaceAll(strings.Trim(armor, "-"), " ", "-"))
		for _, tier := range []string{"opaque", "metadata"} {
			t.Run(tier+"-"+name, func(t *testing.T) {
				marker := armor + "\nissue77-private-material"
				document := issue77FixtureDocument(t)
				switch tier {
				case "opaque":
					document["opaque"] = []any{map[string]any{
						"path": "/nested/private", "source": "test",
						"value": []any{map[string]any{"deeper": marker}},
					}}
				case "metadata":
					metadata := document["devices"].([]any)[0].(map[string]any)["metadata"].(map[string]any)
					metadata["nested_note"] = marker
				}
				document["meta"].(map[string]any)["data_hash"] = ""
				draft, decodeErr := issue77DecodeDocument(document)
				if decodeErr != nil {
					assertIssue77NoDisclosure(t, decodeErr, marker)
					return
				}
				assertIssue77RejectedWithoutDisclosure(t, "Validate", draft.Validate(), marker)
				_, constructErr := NewSnapshotV1(draft)
				assertIssue77RejectedWithoutDisclosure(t, "NewSnapshotV1", constructErr, marker)
				_, redactErr := BuildRedactedSnapshotV1(draft)
				assertIssue77RejectedWithoutDisclosure(t, "BuildRedactedSnapshotV1", redactErr, marker)
				_, marshalErr := json.Marshal(draft)
				assertIssue77RejectedWithoutDisclosure(t, "MarshalJSON", marshalErr, marker)
				formatted := fmt.Sprintf(
					"%v %+v %#v %b %c %d %o %O %q %x %X %U %e %E %f %F %g %G %s",
					draft, draft, draft, draft, draft, draft, draft, draft, draft, draft,
					draft, draft, draft, draft, draft, draft, draft, draft, draft,
				)
				if strings.Contains(formatted, marker) {
					t.Fatal("formatter disclosed rejected private key material")
				}
			})
		}
	}
}

func TestRedactedSnapshotFormatterUsesSafeRepresentationForEveryVerb(t *testing.T) {
	snapshot, err := BuildRedactedSnapshotV1(issue77Snapshot(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{
		"%v", "%+v", "%#v", "%b", "%c", "%d", "%o", "%O", "%q", "%x", "%X", "%U",
		"%e", "%E", "%f", "%F", "%g", "%G", "%s",
	} {
		if got := fmt.Sprintf(format, snapshot); got != snapshot.String() {
			t.Fatalf("fmt.Sprintf(%q, RedactedSnapshotV1) = %q, want %q", format, got, snapshot.String())
		}
	}
	for _, verb := range []rune{'v', 'b', 'c', 'd', 'o', 'O', 'q', 'x', 'X', 'U', 'e', 'E', 'f', 'F', 'g', 'G', 's', 'p'} {
		state := &snapshotFormatStateV1{}
		snapshot.Format(state, verb)
		if got := state.String(); got != snapshot.String() {
			t.Fatalf("RedactedSnapshotV1.Format(%q) = %q, want %q", verb, got, snapshot.String())
		}
	}
}

func TestRedactedSnapshotValidateAndUnmarshalRejectMalformedGraph(t *testing.T) {
	valid, err := BuildRedactedSnapshotV1(issue77Snapshot(t))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip RedactedSnapshotV1
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatalf("valid redacted UnmarshalJSON failed: %v", err)
	}
	if err := roundTrip.Validate(); err != nil {
		t.Fatalf("valid redacted Validate failed: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*RedactedSnapshotV1)
	}{
		{"runtime-kind", func(value *RedactedSnapshotV1) { value.Meta.Runtime.Kind = eebusraw.IDKindSession }},
		{"local-kind", func(value *RedactedSnapshotV1) { value.Meta.LocalSKI.Kind = eebusraw.IDKindPeer }},
		{"captured-at", func(value *RedactedSnapshotV1) { value.Meta.CapturedAt = time.Time{} }},
		{"data-timestamp", func(value *RedactedSnapshotV1) { value.Meta.DataTimestamp = time.Time{} }},
		{"runtime-state", func(value *RedactedSnapshotV1) { value.Status.State = "future" }},
		{"pairing-state", func(value *RedactedSnapshotV1) { value.Pairing[0] = "future" }},
		{"service-kind", func(value *RedactedSnapshotV1) { value.Services[0].Kind = "future" }},
		{"service-id-kind", func(value *RedactedSnapshotV1) { value.Services[0].ID.Kind = eebusraw.IDKindSession }},
		{"duplicate-service", func(value *RedactedSnapshotV1) { value.Services = append(value.Services, value.Services[0]) }},
		{"session-state", func(value *RedactedSnapshotV1) { value.Sessions[0].State = "future" }},
		{"session-since", func(value *RedactedSnapshotV1) { value.Sessions[0].Since = time.Time{} }},
		{"session-id-kind", func(value *RedactedSnapshotV1) { value.Sessions[0].ID.Kind = eebusraw.IDKindPeer }},
		{"session-remote-kind", func(value *RedactedSnapshotV1) { value.Sessions[0].Remote.Kind = eebusraw.IDKindPeer }},
		{"duplicate-device", func(value *RedactedSnapshotV1) { value.Devices = append(value.Devices, value.Devices[0]) }},
		{"duplicate-entity", func(value *RedactedSnapshotV1) { value.Entities = append(value.Entities, value.Entities[0]) }},
		{"duplicate-feature", func(value *RedactedSnapshotV1) { value.Features = append(value.Features, value.Features[0]) }},
		{"duplicate-usecase", func(value *RedactedSnapshotV1) { value.UseCases = append(value.UseCases, value.UseCases[0]) }},
		{"dangling-entity", func(value *RedactedSnapshotV1) {
			value.Devices[0].Entities[0].ID.Digest = "sha256:" + strings.Repeat("a", 64)
		}},
		{"dangling-feature", func(value *RedactedSnapshotV1) {
			value.Devices[0].Entities[0].Features[0].ID.Digest = "sha256:" + strings.Repeat("b", 64)
		}},
		{"dangling-usecase", func(value *RedactedSnapshotV1) {
			value.Devices[0].UseCaseClaims[0].ID.Digest = "sha256:" + strings.Repeat("c", 64)
		}},
	}
	var malformedDigest map[string]any
	if err := json.Unmarshal(encoded, &malformedDigest); err != nil {
		t.Fatal(err)
	}
	malformedDigest["meta"].(map[string]any)["runtime"].(map[string]any)["digest"] = "sha256:bad"
	payload, err := json.Marshal(malformedDigest)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload, &roundTrip); err == nil {
		t.Fatal("UnmarshalJSON accepted an invalid redacted identity digest")
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := canonicalRedactedSnapshotV1(valid)
			test.mutate(&candidate)
			candidate.Meta.DataHash = ""
			hash, hashErr := candidate.computeDataHash()
			if hashErr != nil {
				t.Fatal(hashErr)
			}
			candidate.Meta.DataHash = hash
			if err := candidate.Validate(); err == nil {
				t.Fatal("Validate accepted malformed redacted snapshot")
			}
			type wire RedactedSnapshotV1
			payload, marshalErr := json.Marshal(wire(candidate))
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			var decoded RedactedSnapshotV1
			if err := json.Unmarshal(payload, &decoded); err == nil {
				t.Fatal("UnmarshalJSON accepted malformed redacted snapshot")
			}
		})
	}
}

func TestOpaqueValueDecoderFailsClosedBeforeMaterializingOverBudgetInput(t *testing.T) {
	originalText := "unchanged"
	original := OpaqueValueV1{Scalar: &OpaqueScalarV1{String: &originalText}}
	tests := []struct {
		name string
		data []byte
	}{
		{"wire-bytes", []byte(`"` + strings.Repeat("x", 70_000) + `"`)},
		{"string-bytes", []byte(`"` + strings.Repeat("x", 4_097) + `"`)},
		{"depth", []byte(`{"a":{"b":{"c":{"d":true}}}}`)},
		{"members", []byte(`[` + strings.Repeat(`0,`, 32) + `0]`)},
		{"duplicate-key", []byte(`{"same":1,"same":2}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := cloneOpaqueValueV1(original)
			if err := json.Unmarshal(test.data, &value); err == nil {
				t.Fatal("opaque decoder accepted over-budget input")
			}
			if !reflect.DeepEqual(value, original) {
				t.Fatalf("failed opaque decode partially materialized into destination: %+v", value)
			}
		})
	}
}

func TestRedactedSnapshotUseCaseClaimsJoinByExactDeviceComponent(t *testing.T) {
	draft := rawSnapshotDraftV1(t, false)
	draft.Devices[0].Address = "dev"
	draft.Devices[1].Address = "dev-2"
	draft.Entities = nil
	draft.Features = nil
	draft.UseCases[0].ContextAddress = "dev-2:1:0"
	draft.Meta.DataHash = ""
	raw, err := NewSnapshotV1(draft)
	if err != nil {
		t.Fatal(err)
	}
	redacted, err := BuildRedactedSnapshotV1(raw)
	if err != nil {
		t.Fatal(err)
	}
	shortID, err := eebusraw.RedactID(eebusraw.IDKindPeer, deviceIdentityV1(raw.Devices[0]))
	if err != nil {
		t.Fatal(err)
	}
	longID, err := eebusraw.RedactID(eebusraw.IDKindPeer, deviceIdentityV1(raw.Devices[1]))
	if err != nil {
		t.Fatal(err)
	}
	claims := make(map[string]int)
	for _, device := range redacted.Devices {
		claims[redactedIdentityKeyV1(device.ID)] = len(device.UseCaseClaims)
	}
	if claims[redactedIdentityKeyV1(shortID)] != 0 || claims[redactedIdentityKeyV1(longID)] != 1 {
		t.Fatalf("use-case claims joined by substring: short=%d long=%d", claims[redactedIdentityKeyV1(shortID)], claims[redactedIdentityKeyV1(longID)])
	}
}
