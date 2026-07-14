package eebusraw

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestIdentityDocumentIsVersionedAndValid(t *testing.T) {
	localID, err := RedactID(IDKindLocalSKI, "local-secret-value")
	if err != nil {
		t.Fatal(err)
	}
	remoteID, err := RedactID(IDKindRemoteSKI, "remote-secret-value")
	if err != nil {
		t.Fatal(err)
	}
	sessionID, err := RedactID(IDKindSession, "session-secret-value")
	if err != nil {
		t.Fatal(err)
	}

	doc := NewIdentityDocument(time.Date(2026, 7, 8, 14, 0, 0, 0, time.UTC), EndpointIdentity{
		Role: EndpointRoleLocal,
		ID:   localID,
		Unknown: []UnknownField{{
			Path:  UnknownPathDevice,
			Value: OpaqueBytes([]byte("unknown-secret-value")),
		}},
	})
	doc.Remotes = []EndpointIdentity{{
		Role:    EndpointRoleRemote,
		ID:      remoteID,
		Pairing: PairingObservation{State: PairingStatePaired},
	}}
	doc.Sessions = []SessionIdentity{{
		ID:       sessionID,
		RemoteID: remoteID,
		State:    SessionStateObserved,
	}}

	if err := doc.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if doc.Contract != IdentityContractV1Alpha1 {
		t.Fatalf("contract = %q, want %q", doc.Contract, IdentityContractV1Alpha1)
	}
	if doc.MaskTier != MaskTierRedacted {
		t.Fatalf("mask tier = %q, want %q", doc.MaskTier, MaskTierRedacted)
	}
}

func TestIdentityDocumentRejectsUnredactedStableID(t *testing.T) {
	doc := NewIdentityDocument(time.Now(), EndpointIdentity{
		Role: EndpointRoleLocal,
		ID: RedactedID{
			Kind:   IDKindLocalSKI,
			Masked: "raw-stable-value",
		},
	})

	if err := doc.Validate(); err == nil {
		t.Fatal("Validate() succeeded for unredacted stable id")
	}
	if _, err := json.Marshal(doc); err == nil {
		t.Fatal("json.Marshal succeeded for unredacted stable id")
	}
}

func TestIdentityDocumentRejectsUnknownFieldRawValue(t *testing.T) {
	id, err := RedactID(IDKindLocalSKI, "local-secret-value")
	if err != nil {
		t.Fatal(err)
	}
	doc := NewIdentityDocument(time.Now(), EndpointIdentity{
		Role: EndpointRoleLocal,
		ID:   id,
		Unknown: []UnknownField{{
			Path: UnknownPathDevice,
			Value: OpaqueValue{
				Masked: "raw-unknown-value",
			},
		}},
	})

	if err := doc.Validate(); err == nil {
		t.Fatal("Validate() succeeded for unredacted unknown field")
	}
	if _, err := json.Marshal(doc); err == nil {
		t.Fatal("json.Marshal succeeded for unredacted unknown field")
	}
}

func TestStandaloneRedactedValuesRejectUnsafeJSON(t *testing.T) {
	if _, err := json.Marshal(RedactedID{Kind: IDKindLocalSKI, Masked: "raw-stable-value"}); err == nil {
		t.Fatal("json.Marshal succeeded for unsafe redacted id")
	}
	if _, err := json.Marshal(OpaqueValue{Masked: "raw-unknown-value"}); err == nil {
		t.Fatal("json.Marshal succeeded for unsafe opaque value")
	}
}

func TestCallerControlledKindAndPathAreRejected(t *testing.T) {
	rawIdentity := "raw-secret-identity"
	if _, err := RedactID(IDKind(rawIdentity), "other-secret"); err == nil {
		t.Fatal("RedactID succeeded with caller-controlled id kind")
	}
	if _, err := json.Marshal(RedactedID{
		Kind:   IDKind(rawIdentity),
		Masked: redactedValue,
	}); err == nil {
		t.Fatal("json.Marshal succeeded with caller-controlled id kind")
	}
	if _, err := json.Marshal(IDKind(rawIdentity)); err == nil {
		t.Fatal("json.Marshal succeeded with standalone caller-controlled id kind")
	}
	if out := fmt.Sprintf("%+v %#v", RedactedID{Kind: IDKind(rawIdentity), Masked: redactedValue}, IDKind(rawIdentity)); strings.Contains(out, rawIdentity) {
		t.Fatalf("formatting leaked caller-controlled id kind: %s", out)
	}

	unknown := UnknownField{
		Path:  UnknownPath(rawIdentity),
		Value: OpaqueBytes([]byte("raw-unknown-value")),
	}
	if err := unknown.Validate(); err == nil {
		t.Fatal("Validate succeeded with caller-controlled unknown path")
	}
	if _, err := json.Marshal(unknown); err == nil {
		t.Fatal("json.Marshal succeeded with caller-controlled unknown path")
	}
	if _, err := json.Marshal(UnknownPath(rawIdentity)); err == nil {
		t.Fatal("json.Marshal succeeded with standalone caller-controlled unknown path")
	}
	if out := fmt.Sprintf("%+v %#v", unknown, UnknownPath(rawIdentity)); strings.Contains(out, rawIdentity) {
		t.Fatalf("formatting leaked caller-controlled unknown path: %s", out)
	}
}

func TestOpaqueFormattingDoesNotLeakRawFields(t *testing.T) {
	rawOpaque := "raw-opaque-value"
	unsafe := OpaqueValue{Masked: rawOpaque}
	unknown := UnknownField{
		Path:  UnknownPath(rawOpaque),
		Value: unsafe,
	}
	doc := IdentityDocument{
		Contract:   IdentityContractV1Alpha1,
		MaskTier:   MaskTierRedacted,
		CapturedAt: time.Now(),
		Local: EndpointIdentity{
			Role: EndpointRoleLocal,
			ID: RedactedID{
				Kind:   IDKind(rawOpaque),
				Masked: redactedValue,
			},
			Unknown: []UnknownField{unknown},
		},
	}

	for _, out := range []string{
		fmt.Sprint(unsafe),
		fmt.Sprintf("%+v", unsafe),
		fmt.Sprintf("%#v", unsafe),
		fmt.Sprint(unknown),
		fmt.Sprintf("%+v", unknown),
		fmt.Sprintf("%#v", unknown),
		fmt.Sprint(doc),
		fmt.Sprintf("%+v", doc),
		fmt.Sprintf("%#v", doc),
	} {
		if strings.Contains(out, rawOpaque) {
			t.Fatalf("formatting leaked raw opaque value: %s", out)
		}
	}
}

func TestJSONAndStringDoNotLeakRawInputs(t *testing.T) {
	rawLocal := "local-secret-value"
	rawRemote := "remote-secret-value"
	rawUnknown := "unknown-secret-value"
	localID, err := RedactID(IDKindLocalSKI, rawLocal)
	if err != nil {
		t.Fatal(err)
	}
	remoteID, err := RedactID(IDKindRemoteSKI, rawRemote)
	if err != nil {
		t.Fatal(err)
	}

	doc := NewIdentityDocument(time.Date(2026, 7, 8, 14, 0, 0, 0, time.UTC), EndpointIdentity{
		Role: EndpointRoleLocal,
		ID:   localID,
		Unknown: []UnknownField{{
			Path:  UnknownPathDevice,
			Value: OpaqueBytes([]byte(rawUnknown)),
		}},
	})
	doc.Remotes = []EndpointIdentity{{
		Role: EndpointRoleRemote,
		ID:   remoteID,
	}}

	payload, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	combined := string(payload) + "\n" + fmt.Sprint(doc.Local.ID) + "\n" + fmt.Sprint(doc.Remotes[0].ID)
	for _, raw := range []string{rawLocal, rawRemote, rawUnknown} {
		if strings.Contains(combined, raw) {
			t.Fatalf("public output leaked raw value %q in %s", raw, combined)
		}
	}
}

func TestInvalidContractVersionIsRejected(t *testing.T) {
	id, err := RedactID(IDKindLocalSKI, "local-secret-value")
	if err != nil {
		t.Fatal(err)
	}
	doc := NewIdentityDocument(time.Now(), EndpointIdentity{
		Role: EndpointRoleLocal,
		ID:   id,
	})
	doc.Contract = "helianthus.eebus.raw.identity.v0"

	if err := doc.Validate(); err == nil {
		t.Fatal("Validate() succeeded for unsupported contract")
	}
}

func TestIdentityDocumentV1ConstructorAndMarshalDoNotAliasOrReorder(t *testing.T) {
	localID, err := RedactID(IDKindLocalSKI, "local-v1")
	if err != nil {
		t.Fatal(err)
	}
	remoteAID, err := RedactID(IDKindRemoteSKI, "remote-a-v1")
	if err != nil {
		t.Fatal(err)
	}
	remoteBID, err := RedactID(IDKindRemoteSKI, "remote-b-v1")
	if err != nil {
		t.Fatal(err)
	}
	localUnknown := []UnknownField{
		{Path: UnknownPathRemote, Value: OpaqueBytes([]byte("local-remote"))},
		{Path: UnknownPathDocument, Value: OpaqueBytes([]byte("local-document"))},
	}
	local := EndpointIdentityV1{Role: EndpointRoleV1Local, ID: localID, Unknown: localUnknown}
	doc := NewIdentityDocumentV1(time.Date(2026, 7, 8, 14, 0, 0, 0, time.UTC), local)
	local.Unknown[0] = UnknownField{Path: UnknownPathSession, Value: OpaqueBytes([]byte("mutated"))}
	if doc.Local.Unknown[0].Path != UnknownPathRemote {
		t.Fatal("constructor retained caller-owned local unknown storage")
	}

	doc.Remotes = []EndpointIdentityV1{
		{Role: EndpointRoleV1Remote, ID: remoteBID},
		{Role: EndpointRoleV1Remote, ID: remoteAID},
	}
	doc.Sessions = []SessionIdentityV1{
		{ID: mustRedactID(t, IDKindSession, "session-b-v1"), RemoteID: remoteBID},
		{ID: mustRedactID(t, IDKindSession, "session-a-v1"), RemoteID: remoteAID},
	}
	doc.Unknown = []UnknownField{
		{Path: UnknownPathRemote, Value: OpaqueBytes([]byte("document-remote"))},
		{Path: UnknownPathDocument, Value: OpaqueBytes([]byte("document-document"))},
	}
	beforeRemotes := append([]EndpointIdentityV1(nil), doc.Remotes...)
	beforeSessions := append([]SessionIdentityV1(nil), doc.Sessions...)
	beforeUnknown := append([]UnknownField(nil), doc.Unknown...)
	beforeLocalUnknown := append([]UnknownField(nil), doc.Local.Unknown...)

	if _, err := json.Marshal(doc); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(doc.Remotes, beforeRemotes) || !reflect.DeepEqual(doc.Sessions, beforeSessions) || !reflect.DeepEqual(doc.Unknown, beforeUnknown) || !reflect.DeepEqual(doc.Local.Unknown, beforeLocalUnknown) {
		t.Fatal("canonical identity marshal mutated caller-visible collection order")
	}
}

func TestStableIdentityEntriesMarshalUnknownDeterministicallyWithoutMutation(t *testing.T) {
	documentUnknown := UnknownField{Path: UnknownPathDocument, Value: OpaqueBytes([]byte("document"))}
	remoteUnknown := UnknownField{Path: UnknownPathRemote, Value: OpaqueBytes([]byte("remote"))}
	reversed := []UnknownField{remoteUnknown, documentUnknown}
	sorted := []UnknownField{documentUnknown, remoteUnknown}

	t.Run("endpoint", func(t *testing.T) {
		entry := EndpointIdentityV1{
			Role:    EndpointRoleV1Local,
			ID:      mustRedactID(t, IDKindLocalSKI, "standalone-endpoint"),
			Unknown: append([]UnknownField(nil), reversed...),
		}
		before := append([]UnknownField(nil), entry.Unknown...)
		got, err := json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(entry.Unknown, before) {
			t.Fatal("standalone endpoint marshal mutated unknown order")
		}
		entry.Unknown = sorted
		want, err := json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("standalone endpoint JSON is not canonical\nwant: %s\ngot:  %s", want, got)
		}
	})

	t.Run("session", func(t *testing.T) {
		entry := SessionIdentityV1{
			ID:       mustRedactID(t, IDKindSession, "standalone-session"),
			RemoteID: mustRedactID(t, IDKindRemoteSKI, "standalone-remote"),
			Unknown:  append([]UnknownField(nil), reversed...),
		}
		before := append([]UnknownField(nil), entry.Unknown...)
		got, err := json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(entry.Unknown, before) {
			t.Fatal("standalone session marshal mutated unknown order")
		}
		entry.Unknown = sorted
		want, err := json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("standalone session JSON is not canonical\nwant: %s\ngot:  %s", want, got)
		}
	})
}

func TestStableRoleAndEntryFormattingDoesNotLeak(t *testing.T) {
	raw := "caller-controlled-stable-role"
	role := EndpointRoleV1(raw)
	endpoint := EndpointIdentityV1{
		Role: role,
		ID:   RedactedID{Kind: IDKindLocalSKI, Masked: redactedValue, Digest: raw},
	}
	session := SessionIdentityV1{
		ID:       RedactedID{Kind: IDKindSession, Masked: redactedValue, Digest: raw},
		RemoteID: mustRedactID(t, IDKindRemoteSKI, "format-remote"),
	}
	formatted := fmt.Sprintf("%s %v %+v %#v %q %s %v %+v %#v %q %s %v %+v %#v %q", role, role, role, role, role, endpoint, endpoint, endpoint, endpoint, endpoint, session, session, session, session, session)
	if strings.Contains(formatted, raw) {
		t.Fatalf("stable role or entry formatting leaked caller input: %s", formatted)
	}
	if _, err := json.Marshal(role); err == nil {
		t.Fatal("invalid stable role marshaled successfully")
	}
}

func TestIdentityDocumentV1ValidationAndFormattingDoNotLeak(t *testing.T) {
	raw := "raw-secret-v1-digest"
	localID := mustRedactID(t, IDKindLocalSKI, "local-v1")
	localID.Digest = raw
	doc := NewIdentityDocumentV1(time.Now(), EndpointIdentityV1{Role: EndpointRoleV1Local, ID: localID})
	err := doc.Validate()
	if err == nil {
		t.Fatal("Validate() succeeded for caller-controlled digest")
	}
	combined := err.Error() + "\n" + fmt.Sprint(doc) + "\n" + fmt.Sprintf("%+v %#v", doc, doc)
	if strings.Contains(combined, raw) {
		t.Fatalf("stable identity validation or formatting leaked raw input: %s", combined)
	}

	localID = mustRedactID(t, IDKindLocalSKI, "local-v1")
	doc = NewIdentityDocumentV1(time.Now(), EndpointIdentityV1{Role: EndpointRoleV1Local, ID: localID})
	doc.Sessions = []SessionIdentityV1{{
		ID:       mustRedactID(t, IDKindSession, "session-v1"),
		RemoteID: RedactedID{Kind: IDKindRemoteSKI, Masked: redactedValue, Digest: raw},
	}}
	err = doc.Validate()
	if err == nil {
		t.Fatal("Validate() succeeded for caller-controlled session remote digest")
	}
	if strings.Contains(err.Error(), raw) {
		t.Fatalf("stable identity validation leaked raw session digest: %v", err)
	}
}

func TestIdentityContractVersionsRemainIsolated(t *testing.T) {
	local := EndpointIdentity{Role: EndpointRoleLocal, ID: mustRedactID(t, IDKindLocalSKI, "local-contract")}
	alpha := NewIdentityDocument(time.Now(), local)
	alpha.Contract = IdentityContractV1
	if err := alpha.Validate(); err == nil {
		t.Fatal("v1alpha1 identity accepted stable v1 contract")
	}
	stable := NewIdentityDocumentV1(time.Now(), EndpointIdentityV1{Role: EndpointRoleV1(local.Role), ID: local.ID, Unknown: local.Unknown})
	stable.Contract = IdentityContractV1Alpha1
	if err := stable.Validate(); err == nil {
		t.Fatal("stable v1 identity accepted v1alpha1 contract")
	}
}

func TestIdentityDocumentV1ExactJSONOmitsPairingAndState(t *testing.T) {
	localID := mustRedactID(t, IDKindLocalSKI, "stable-local")
	remoteID := mustRedactID(t, IDKindRemoteSKI, "stable-remote")
	sessionID := mustRedactID(t, IDKindSession, "stable-session")
	doc := NewIdentityDocumentV1(
		time.Date(2026, 7, 8, 14, 0, 0, 0, time.UTC),
		EndpointIdentityV1{Role: EndpointRoleV1Local, ID: localID},
	)
	doc.Remotes = []EndpointIdentityV1{{Role: EndpointRoleV1Remote, ID: remoteID}}
	doc.Sessions = []SessionIdentityV1{{ID: sessionID, RemoteID: remoteID}}

	payload, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf(
		`{"contract":"helianthus.eebus.raw.identity.v1","mask_tier":"redacted","captured_at":"2026-07-08T14:00:00Z","local":{"role":"local","id":{"kind":"local-ski","masked":"[redacted]","digest":%q}},"remotes":[{"role":"remote","id":{"kind":"remote-ski","masked":"[redacted]","digest":%q}}],"sessions":[{"id":{"kind":"session","masked":"[redacted]","digest":%q},"remote_id":{"kind":"remote-ski","masked":"[redacted]","digest":%q}}]}`,
		localID.Digest,
		remoteID.Digest,
		sessionID.Digest,
		remoteID.Digest,
	)
	if string(payload) != want {
		t.Fatalf("identity JSON mismatch\nwant: %s\ngot:  %s", want, payload)
	}
	for _, forbidden := range []string{`"pairing"`, `"state"`, `"readiness"`, `"availability"`} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("stable identity JSON contains forbidden field %s: %s", forbidden, payload)
		}
	}
}

func TestIdentityDocumentV1RejectsMalformedDigests(t *testing.T) {
	validLocal := mustRedactID(t, IDKindLocalSKI, "stable-local")
	validRemote := mustRedactID(t, IDKindRemoteSKI, "stable-remote")
	validSession := mustRedactID(t, IDKindSession, "stable-session")
	uppercase := "sha256:" + strings.ToUpper(strings.TrimPrefix(validLocal.Digest, "sha256:"))
	nonHex := "sha256:" + strings.Repeat("g", 64)

	tests := []struct {
		name   string
		mutate func(*IdentityDocumentV1)
	}{
		{name: "local uppercase", mutate: func(doc *IdentityDocumentV1) { doc.Local.ID.Digest = uppercase }},
		{name: "remote non-hex", mutate: func(doc *IdentityDocumentV1) {
			doc.Remotes = []EndpointIdentityV1{{Role: EndpointRoleV1Remote, ID: RedactedID{Kind: IDKindRemoteSKI, Masked: redactedValue, Digest: nonHex}}}
		}},
		{name: "session uppercase", mutate: func(doc *IdentityDocumentV1) {
			doc.Sessions = []SessionIdentityV1{{ID: RedactedID{Kind: IDKindSession, Masked: redactedValue, Digest: uppercase}, RemoteID: validRemote}}
		}},
		{name: "session remote non-hex", mutate: func(doc *IdentityDocumentV1) {
			doc.Sessions = []SessionIdentityV1{{ID: validSession, RemoteID: RedactedID{Kind: IDKindRemoteSKI, Masked: redactedValue, Digest: nonHex}}}
		}},
		{name: "unknown uppercase", mutate: func(doc *IdentityDocumentV1) {
			doc.Unknown = []UnknownField{{Path: UnknownPathDocument, Value: OpaqueValue{Masked: redactedValue, Digest: uppercase}}}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			doc := NewIdentityDocumentV1(time.Now(), EndpointIdentityV1{Role: EndpointRoleV1Local, ID: validLocal})
			test.mutate(&doc)
			if err := doc.Validate(); err == nil {
				t.Fatal("Validate() accepted malformed stable digest")
			}
			if _, err := json.Marshal(doc); err == nil {
				t.Fatal("json.Marshal accepted malformed stable digest")
			}
		})
	}
}

func TestIdentityDocumentV1RejectsDuplicateIdentityKeys(t *testing.T) {
	local := EndpointIdentityV1{Role: EndpointRoleV1Local, ID: mustRedactID(t, IDKindLocalSKI, "stable-local")}
	remoteID := mustRedactID(t, IDKindRemoteSKI, "stable-remote")
	sessionID := mustRedactID(t, IDKindSession, "stable-session")

	t.Run("remote", func(t *testing.T) {
		doc := NewIdentityDocumentV1(time.Now(), local)
		doc.Remotes = []EndpointIdentityV1{
			{Role: EndpointRoleV1Remote, ID: remoteID, Unknown: []UnknownField{{Path: UnknownPathRemote, Value: OpaqueBytes([]byte("first"))}}},
			{Role: EndpointRoleV1Remote, ID: remoteID, Unknown: []UnknownField{{Path: UnknownPathRemote, Value: OpaqueBytes([]byte("contradictory"))}}},
		}
		if err := doc.Validate(); err == nil || !strings.Contains(err.Error(), "duplicates identity key") {
			t.Fatalf("duplicate remote identity result = %v", err)
		}
	})

	t.Run("session", func(t *testing.T) {
		doc := NewIdentityDocumentV1(time.Now(), local)
		doc.Sessions = []SessionIdentityV1{
			{ID: sessionID, RemoteID: remoteID, Unknown: []UnknownField{{Path: UnknownPathSession, Value: OpaqueBytes([]byte("first"))}}},
			{ID: sessionID, RemoteID: mustRedactID(t, IDKindRemoteSKI, "other-remote"), Unknown: []UnknownField{{Path: UnknownPathSession, Value: OpaqueBytes([]byte("contradictory"))}}},
		}
		if err := doc.Validate(); err == nil || !strings.Contains(err.Error(), "duplicates identity key") {
			t.Fatalf("duplicate session identity result = %v", err)
		}
	})
}

func TestIdentityDocumentV1RejectsUnrepresentableTimestamp(t *testing.T) {
	doc := NewIdentityDocumentV1(
		time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC),
		EndpointIdentityV1{Role: EndpointRoleV1Local, ID: mustRedactID(t, IDKindLocalSKI, "stable-local")},
	)
	if err := doc.Validate(); err == nil {
		t.Fatal("Validate() accepted timestamp outside RFC3339 JSON range")
	}
	if _, err := json.Marshal(doc); err == nil {
		t.Fatal("json.Marshal accepted timestamp outside RFC3339 JSON range")
	}
}

func TestAlphaRedactedIDDigestValidationRemainsCompatible(t *testing.T) {
	id := mustRedactID(t, IDKindLocalSKI, "alpha-local")
	id.Digest = "sha256:" + strings.ToUpper(strings.TrimPrefix(id.Digest, "sha256:"))
	if err := id.Validate(); err != nil {
		t.Fatalf("alpha/shared RedactedID behavior changed: %v", err)
	}
}

func mustRedactID(t *testing.T, kind IDKind, raw string) RedactedID {
	t.Helper()
	id, err := RedactID(kind, raw)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
