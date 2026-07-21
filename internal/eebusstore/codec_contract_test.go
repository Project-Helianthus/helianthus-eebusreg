package eebusstore

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestCanonicalGoldenVectorsRoundTripByteExactly(t *testing.T) {
	for name, wantDigest := range map[string]string{
		"generation-v1-empty.json":       emptyGenerationSHA256,
		"generation-v1-child-empty.json": childGenerationSHA256,
		"generation-v1-populated.json":   populatedGenerationSHA256,
	} {
		t.Run(name, func(t *testing.T) {
			payload := readFixture(t, name)
			if got := testDigestHex(payload); got != wantDigest {
				t.Fatalf("golden digest = %s, want %s", got, wantDigest)
			}
			decoded, err := decodeGenerationV1(payload)
			if err != nil {
				t.Fatalf("decode golden: %v", err)
			}
			reencoded, err := encodeGenerationV1(decoded)
			if err != nil {
				t.Fatalf("encode golden: %v", err)
			}
			if !bytes.Equal(reencoded, payload) {
				t.Fatal("generation canonical re-encode differs from checked-in bytes")
			}
		})
	}

	for name, wantDigest := range map[string]string{
		"manifest-v1-g1.json": emptyManifestSHA256,
		"manifest-v1-g2.json": childManifestSHA256,
	} {
		t.Run(name, func(t *testing.T) {
			payload := readFixture(t, name)
			if got := testDigestHex(payload); got != wantDigest {
				t.Fatalf("golden digest = %s, want %s", got, wantDigest)
			}
			decoded, err := decodeManifestPayloadV1(payload)
			if err != nil {
				t.Fatalf("decode golden: %v", err)
			}
			reencoded, err := encodeManifestPayloadV1(decoded)
			if err != nil {
				t.Fatalf("encode golden: %v", err)
			}
			if !bytes.Equal(reencoded, payload) {
				t.Fatal("manifest payload canonical re-encode differs from checked-in bytes")
			}
		})
	}

	for _, name := range []string{"manifest-a-v1.json", "manifest-b-v1.json"} {
		t.Run(name, func(t *testing.T) {
			payload := readFixture(t, name)
			decoded, err := decodeManifestSlot(payload)
			if err != nil {
				t.Fatalf("decode golden: %v", err)
			}
			reencoded, err := encodeManifestSlot(decoded)
			if err != nil {
				t.Fatalf("encode golden: %v", err)
			}
			if !bytes.Equal(reencoded, payload) {
				t.Fatal("manifest slot canonical re-encode differs from checked-in bytes")
			}
		})
	}
}

func TestCanonicalGenerationRejectsNonCanonicalOrMalformedBytes(t *testing.T) {
	empty := readFixture(t, "generation-v1-empty.json")
	populated := readFixture(t, "generation-v1-populated.json")
	invalidUTF8Record := append([]byte(`"remote_identities":[{"record_id":"AQ==","remote_ship_id":"`), 0xff)
	invalidUTF8Record = append(invalidUTF8Record, []byte(`","remote_ski":"Ag=="}]`)...)
	invalidUTF8 := bytes.Replace(empty, []byte(`"remote_identities":[]`), invalidUTF8Record, 1)

	tests := map[string][]byte{
		"duplicate key":         bytes.Replace(empty, []byte(`"schema_version":3`), []byte(`"schema_version":3,"schema_version":3`), 1),
		"unknown top-level key": bytes.Replace(empty, []byte(`"schema_version":3`), []byte(`"schema_version":3,"unexpected":null`), 1),
		"unknown nested key":    bytes.Replace(empty, []byte(`"sequence":1`), []byte(`"sequence":1,"timestamp":0`), 1),
		"trailing JSON":         append(bytes.Clone(empty), []byte(`{}`)...),
		"extra newline":         append(bytes.Clone(empty), '\n'),
		"missing newline":       bytes.TrimSuffix(empty, []byte{'\n'}),
		"leading whitespace":    append([]byte{' '}, empty...),
		"UTF-8 BOM":             append([]byte{0xef, 0xbb, 0xbf}, empty...),
		"invalid UTF-8":         invalidUTF8,
		"comment":               bytes.Replace(empty, []byte(`"schema_version":3`), []byte(`"schema_version":3/*x*/`), 1),
		"negative integer":      bytes.Replace(empty, []byte(`"sequence":1`), []byte(`"sequence":-1`), 1),
		"floating integer":      bytes.Replace(empty, []byte(`"sequence":1`), []byte(`"sequence":1.0`), 1),
		"exponent integer":      bytes.Replace(empty, []byte(`"sequence":1`), []byte(`"sequence":1e0`), 1),
		"integer overflow":      bytes.Replace(empty, []byte(`"sequence":1`), []byte(`"sequence":9223372036854775808`), 1),
		"object key order":      []byte(`{"schema_version":3,"control":null,"generation":{"parent_sequence":null,"parent_sha256":null,"sequence":1},"local_identity":null,"remote_identities":[]}` + "\n"),
		"escaped non-ASCII":     bytes.Replace(populated, []byte("Étage"), []byte(`\u00c9tage`), 1),
		"decomposed NFC":        bytes.Replace(populated, []byte("Étage"), []byte("E\u0301tage"), 1),
		"control escape":        bytes.Replace(populated, []byte("Étage 2"), []byte(`bad\u0001value`), 1),
		"format character":      bytes.Replace(populated, []byte("Étage 2"), []byte("bad\u200dvalue"), 1),
	}

	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := decodeGenerationV1(payload)
			assertErrorOutcome(t, err, outcomeMalformedState)
		})
	}
}

func TestCanonicalJSONAndBinaryBoundsFailClosed(t *testing.T) {
	if err := validateCanonicalJSON([]byte("[[[[[[[[[0]]]]]]]]]\n"), 4<<20, 8); err == nil {
		t.Fatal("accepted JSON nesting deeper than eight")
	} else {
		assertErrorOutcome(t, err, outcomeMalformedState)
	}
	if err := validateCanonicalJSON(bytes.Repeat([]byte{'x'}, (4<<20)+1), 4<<20, 8); err == nil {
		t.Fatal("accepted a generation-sized document above 4 MiB")
	} else {
		assertErrorOutcome(t, err, outcomeMalformedState)
	}

	for encoded, want := range map[string][]byte{
		"AA==": {0x00},
		"AAE=": {0x00, 0x01},
		"AAEC": {0x00, 0x01, 0x02},
	} {
		decoded, err := decodeCanonicalBase64(encoded, 1, 3)
		if err != nil {
			t.Fatalf("decode %q: %v", encoded, err)
		}
		if !bytes.Equal(decoded, want) {
			t.Fatalf("decode %q = %x, want %x", encoded, decoded, want)
		}
	}
	for _, encoded := range []string{"AA", "AAE", "AAEC=", "AA-_", " AA==", "AA==\n", "===="} {
		t.Run(fmt.Sprintf("base64_%q", encoded), func(t *testing.T) {
			_, err := decodeCanonicalBase64(encoded, 1, 3)
			assertErrorOutcome(t, err, outcomeMalformedState)
		})
	}
	if _, err := decodeCanonicalBase64(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x41}, 129)), 1, 128); err == nil {
		t.Fatal("accepted decoded binary above its bound")
	} else {
		assertErrorOutcome(t, err, outcomeMalformedState)
	}

	empty := readFixture(t, "generation-v1-empty.json")
	atMaximum := bytes.Replace(empty, []byte(`"sequence":1`), []byte(`"sequence":9223372036854775807`), 1)
	generation, err := decodeGenerationV1(atMaximum)
	if err != nil {
		t.Fatalf("maximum signed 64-bit sequence was rejected: %v", err)
	}
	encoded, err := encodeGenerationV1(generation)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, atMaximum) {
		t.Fatal("maximum signed 64-bit sequence did not round-trip canonically")
	}
}

func TestV1BoundsRejectBeforeUnboundedStateIsAccepted(t *testing.T) {
	populated := readFixture(t, "generation-v1-populated.json")
	overlongSKI := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x41}, 129))
	overlongBlob := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, (256<<10)+1))
	overlongSHIPID := strings.Repeat("s", 513)

	certificate := certificateGoldenValue(t, populated)
	tooManyCertificates := strings.Repeat(fmt.Sprintf("%q,", certificate), 16) + fmt.Sprintf("%q", certificate)
	tests := map[string][]byte{
		"generation file above 4 MiB":    bytes.Repeat([]byte{'x'}, (4<<20)+1),
		"empty SKI":                      bytes.Replace(populated, []byte(`"local_ski":"bG9jYWwtc2tpLXYx"`), []byte(`"local_ski":""`), 1),
		"SKI above 128 bytes":            bytes.Replace(populated, []byte(`"local_ski":"bG9jYWwtc2tpLXYx"`), []byte(`"local_ski":"`+overlongSKI+`"`), 1),
		"sealed blob above 256 KiB":      bytes.Replace(populated, []byte(`"sealed_blob":"c2VhbGVkLXByb3ZpZGVyLXJlZmVyZW5jZQ=="`), []byte(`"sealed_blob":"`+overlongBlob+`"`), 1),
		"empty certificate chain":        replaceCertificateChain(t, populated, ""),
		"certificate chain above 16":     replaceCertificateChain(t, populated, tooManyCertificates),
		"certificate bytes above 1 MiB":  replaceCertificateChain(t, populated, fmt.Sprintf("%q", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x43}, (1<<20)+1)))),
		"SHIP ID above 512 bytes":        bytes.Replace(populated, []byte("Étage 2"), []byte(overlongSHIPID), 1),
		"remote associations above 1024": generationWithRemoteCount(1025),
	}

	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := decodeGenerationV1(payload)
			assertErrorOutcome(t, err, outcomeMalformedState)
		})
	}
}

func TestExcessiveArraysRejectWithoutTailScaledAllocations(t *testing.T) {
	t.Run("remote identities", func(t *testing.T) {
		assertBoundedRejectionAllocations(
			t,
			generationWithRemoteCount(maxRemoteIdentityCount+1),
			generationWithRemoteCount(8192),
		)
	})

	t.Run("certificate chain", func(t *testing.T) {
		populated := readFixture(t, "generation-v1-populated.json")
		certificateArray := func(count int) string {
			return strings.TrimSuffix(strings.Repeat(`"AA==",`, count), ",")
		}
		assertBoundedRejectionAllocations(
			t,
			replaceCertificateChain(t, populated, certificateArray(maxCertificateCount+1)),
			replaceCertificateChain(t, populated, certificateArray(4096)),
		)
	})
}

func TestV1RejectsDuplicateAssociationsNonCanonicalOrderAndInvalidProviderReference(t *testing.T) {
	populated := readFixture(t, "generation-v1-populated.json")
	records := remoteRecordsFromFixture(t, populated)
	tests := map[string][]byte{
		"duplicate record id":       replaceRemoteRecords(t, populated, records[0]+","+records[0]),
		"noncanonical record order": replaceRemoteRecords(t, populated, records[1]+","+records[0]),
		"empty record id":           bytes.Replace(populated, []byte(`"record_id":"AQ=="`), []byte(`"record_id":""`), 1),
		"empty remote SKI":          bytes.Replace(populated, []byte(`"remote_ski":"cmVtb3RlLXNraS1vbmU="`), []byte(`"remote_ski":""`), 1),
		"duplicate remote SKI":      bytes.Replace(populated, []byte(`"remote_ski":"cmVtb3RlLXNraS10d28="`), []byte(`"remote_ski":"cmVtb3RlLXNraS1vbmU="`), 1),
		"duplicate remote SHIP ID":  bytes.Replace(populated, []byte(`"remote_ship_id":"Étage 2"`), []byte(`"remote_ship_id":"Gerät \"Küche\" \\ <main>"`), 1),
		"uppercase provider id":     bytes.Replace(populated, []byte(`"provider_id":"test.provider"`), []byte(`"provider_id":"Test.Provider"`), 1),
		"zero provider version":     bytes.Replace(populated, []byte(`"provider_version":7`), []byte(`"provider_version":0`), 1),
		"uppercase digest": bytes.Replace(
			populated,
			[]byte("9a82517f9af19416d98fdbcf193726b3a95c0b6fec1d51884bf3e1b739ba2ef4"),
			[]byte("9A82517F9AF19416D98FDBCF193726B3A95C0B6FEC1D51884BF3E1B739BA2EF4"),
			1,
		),
	}
	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := decodeGenerationV1(payload)
			assertErrorOutcome(t, err, outcomeMalformedState)
		})
	}
}

func TestManifestPayloadIsClosedAndGenerationFilenameIsImplied(t *testing.T) {
	payload := readFixture(t, "manifest-v1-g1.json")
	tests := map[string][]byte{
		"duplicate key":        bytes.Replace(payload, []byte(`"manifest_version":1`), []byte(`"manifest_version":1,"manifest_version":1`), 1),
		"unknown key":          bytes.Replace(payload, []byte(`"parent":null`), []byte(`"parent":null,"unknown":null`), 1),
		"path separator":       bytes.Replace(payload, []byte(testGenerationFilename(1)), []byte("../"+testGenerationFilename(1)), 1),
		"wrong fixed filename": bytes.Replace(payload, []byte(testGenerationFilename(1)), []byte(testGenerationFilename(2)), 1),
		"uppercase digest":     bytes.Replace(payload, []byte(emptyGenerationSHA256), []byte(strings.ToUpper(emptyGenerationSHA256)), 1),
	}
	for name, malformed := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := decodeManifestPayloadV1(malformed)
			assertErrorOutcome(t, err, outcomeMalformedState)
		})
	}
}

func TestManifestEnvelopeCanonicalityAndBounds(t *testing.T) {
	slot := readFixture(t, "manifest-a-v1.json")
	tests := map[string][]byte{
		"duplicate key":                  bytes.Replace(slot, []byte(`"manifest_epoch":1`), []byte(`"manifest_epoch":1,"manifest_epoch":1`), 1),
		"unknown key":                    bytes.Replace(slot, []byte(`"slot_format_version":1`), []byte(`"slot_format_version":1,"unknown":null`), 1),
		"epoch overflow":                 bytes.Replace(slot, []byte(`"manifest_epoch":1`), []byte(`"manifest_epoch":9223372036854775808`), 1),
		"unpadded payload":               bytes.Replace(slot, []byte(`fQo="`), []byte(`fQo"`), 1),
		"missing newline":                bytes.TrimSuffix(slot, []byte{'\n'}),
		"decoded payload above 8 KiB":    testManifestSlotBytes(1, 1, bytes.Repeat([]byte{'x'}, (8<<10)+1)),
		"manifest envelope above 16 KiB": testManifestSlotBytes(1, 1, bytes.Repeat([]byte{'x'}, 13<<10)),
	}
	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := decodeManifestSlot(payload)
			assertErrorOutcome(t, err, outcomeMalformedState)
		})
	}

	payload := readFixture(t, "manifest-v1-g1.json")
	atMaximum := testManifestSlotBytes(9223372036854775807, 1, payload)
	decoded, err := decodeManifestSlot(atMaximum)
	if err != nil {
		t.Fatalf("maximum signed 64-bit manifest epoch was rejected: %v", err)
	}
	reencoded, err := encodeManifestSlot(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(reencoded, atMaximum) {
		t.Fatal("maximum manifest epoch did not round-trip canonically")
	}
}

func TestCurrentStateIsNeutralAndClosed(t *testing.T) {
	payload := readFixture(t, "generation-v1-populated.json")
	decoded, err := decodeGenerationV1(payload)
	if err != nil {
		t.Fatal(err)
	}

	var document map[string]json.RawMessage
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatal(err)
	}
	wantTopLevel := []string{"control", "generation", "local_identity", "remote_identities", "schema_version"}
	if got := sortedMapKeys(document); !reflect.DeepEqual(got, wantTopLevel) {
		t.Fatalf("top-level fields = %v, want %v", got, wantTopLevel)
	}

	for _, forbidden := range []string{
		"pairing_state",
		"trust_state",
		"quarantine_policy",
		"backoff",
		"retry_state",
		"semantic_device_id",
		"lifecycle_state",
		"private_key",
		"backup_excluded",
	} {
		t.Run(forbidden, func(t *testing.T) {
			withField := bytes.Replace(payload, []byte(`,"schema_version":3}`), []byte(`,"`+forbidden+`":null,"schema_version":3}`), 1)
			_, err := decodeGenerationV1(withField)
			assertErrorOutcome(t, err, outcomeMalformedState)
		})
	}

	fieldNames := allStructFieldNames(reflect.TypeOf(decoded))
	for _, forbidden := range []string{"pairing", "trust", "quarantine", "backoff", "retry", "semantic", "lifecycle", "privatekey", "plaintext"} {
		for _, field := range fieldNames {
			if strings.Contains(strings.ToLower(field), forbidden) {
				t.Fatalf("current in-memory state field %q contains forbidden concept %q", field, forbidden)
			}
		}
	}
}

func generationWithRemoteCount(count int) []byte {
	var payload strings.Builder
	payload.WriteString(`{"control":null,"generation":{"parent_sequence":null,"parent_sha256":null,"sequence":1},"local_identity":null,"remote_identities":[`)
	for i := 0; i < count; i++ {
		if i > 0 {
			payload.WriteByte(',')
		}
		recordID := base64.StdEncoding.EncodeToString([]byte{byte(i >> 24), byte(i >> 16), byte(i >> 8), byte(i)})
		fmt.Fprintf(&payload, `{"record_id":%q,"remote_ship_id":%q,"remote_ski":%q}`, recordID, fmt.Sprintf("ship-%04d", i), base64.StdEncoding.EncodeToString([]byte{0x80, byte(i >> 8), byte(i)}))
	}
	payload.WriteString(`],"schema_version":3}`)
	payload.WriteByte('\n')
	return []byte(payload.String())
}

func assertBoundedRejectionAllocations(t *testing.T, boundary, attackerTail []byte) {
	t.Helper()
	if _, err := decodeGenerationV1(boundary); err == nil {
		t.Fatal("boundary-plus-one payload was accepted")
	} else {
		assertErrorOutcome(t, err, outcomeMalformedState)
	}
	if _, err := decodeGenerationV1(attackerTail); err == nil {
		t.Fatal("attacker-tail payload was accepted")
	} else {
		assertErrorOutcome(t, err, outcomeMalformedState)
	}

	boundaryAllocs := testing.AllocsPerRun(1, func() {
		_, _ = decodeGenerationV1(boundary)
	})
	tailAllocs := testing.AllocsPerRun(1, func() {
		_, _ = decodeGenerationV1(attackerTail)
	})
	const maximumTailAllocationGrowth = 128
	if tailAllocs > boundaryAllocs+maximumTailAllocationGrowth {
		t.Fatalf(
			"rejection allocations grew from %.0f to %.0f with attacker-controlled tail; want growth <= %d",
			boundaryAllocs,
			tailAllocs,
			maximumTailAllocationGrowth,
		)
	}
}

func certificateGoldenValue(t *testing.T, payload []byte) string {
	t.Helper()
	const prefix = `"certificate_chain_der":["`
	start := bytes.Index(payload, []byte(prefix))
	if start < 0 {
		t.Fatal("certificate chain prefix missing from populated fixture")
	}
	start += len(prefix)
	end := bytes.Index(payload[start:], []byte(`"]`))
	if end < 0 {
		t.Fatal("certificate chain terminator missing from populated fixture")
	}
	return string(payload[start : start+end])
}

func replaceCertificateChain(t *testing.T, payload []byte, arrayBody string) []byte {
	t.Helper()
	start := bytes.Index(payload, []byte(`"certificate_chain_der":[`))
	if start < 0 {
		t.Fatal("certificate chain field missing")
	}
	end := bytes.Index(payload[start:], []byte(`],"key_reference"`))
	if end < 0 {
		t.Fatal("certificate chain boundary missing")
	}
	end += start
	replacement := []byte(`"certificate_chain_der":[` + arrayBody)
	result := append(bytes.Clone(payload[:start]), replacement...)
	return append(result, payload[end:]...)
}

func remoteRecordsFromFixture(t *testing.T, payload []byte) []string {
	t.Helper()
	start := bytes.Index(payload, []byte(`"remote_identities":[`))
	if start < 0 {
		t.Fatal("remote identities field missing")
	}
	start += len(`"remote_identities":[`)
	end := bytes.Index(payload[start:], []byte(`],"schema_version"`))
	if end < 0 {
		t.Fatal("remote identities boundary missing")
	}
	records := strings.Split(string(payload[start:start+end]), "},{")
	if len(records) != 2 {
		t.Fatalf("populated fixture remote record count = %d, want 2", len(records))
	}
	records[0] += "}"
	records[1] = "{" + records[1]
	return records
}

func replaceRemoteRecords(t *testing.T, payload []byte, records string) []byte {
	t.Helper()
	start := bytes.Index(payload, []byte(`"remote_identities":[`))
	if start < 0 {
		t.Fatal("remote identities field missing")
	}
	end := bytes.Index(payload[start:], []byte(`],"schema_version"`))
	if end < 0 {
		t.Fatal("remote identities boundary missing")
	}
	end += start
	replacement := []byte(`"remote_identities":[` + records)
	result := append(bytes.Clone(payload[:start]), replacement...)
	return append(result, payload[end:]...)
}

func sortedMapKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

func allStructFieldNames(root reflect.Type) []string {
	seen := map[reflect.Type]bool{}
	var fields []string
	var walk func(reflect.Type)
	walk = func(current reflect.Type) {
		for current.Kind() == reflect.Pointer || current.Kind() == reflect.Slice || current.Kind() == reflect.Array {
			current = current.Elem()
		}
		if current.Kind() != reflect.Struct || seen[current] {
			return
		}
		seen[current] = true
		for i := 0; i < current.NumField(); i++ {
			field := current.Field(i)
			fields = append(fields, field.Name)
			walk(field.Type)
		}
	}
	walk(root)
	return fields
}
