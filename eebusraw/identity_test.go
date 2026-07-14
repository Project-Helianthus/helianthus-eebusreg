package eebusraw

import (
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
	local := EndpointIdentity{Role: EndpointRoleLocal, ID: localID, Unknown: localUnknown}
	doc := NewIdentityDocumentV1(time.Date(2026, 7, 8, 14, 0, 0, 0, time.UTC), local)
	local.Unknown[0] = UnknownField{Path: UnknownPathSession, Value: OpaqueBytes([]byte("mutated"))}
	if doc.Local.Unknown[0].Path != UnknownPathRemote {
		t.Fatal("constructor retained caller-owned local unknown storage")
	}

	doc.Remotes = []EndpointIdentity{
		{Role: EndpointRoleRemote, ID: remoteBID},
		{Role: EndpointRoleRemote, ID: remoteAID},
	}
	doc.Sessions = []SessionIdentity{
		{ID: mustRedactID(t, IDKindSession, "session-b-v1"), RemoteID: remoteBID, State: SessionStateDisconnected},
		{ID: mustRedactID(t, IDKindSession, "session-a-v1"), RemoteID: remoteAID, State: SessionStateObserved},
	}
	doc.Unknown = []UnknownField{
		{Path: UnknownPathRemote, Value: OpaqueBytes([]byte("document-remote"))},
		{Path: UnknownPathDocument, Value: OpaqueBytes([]byte("document-document"))},
	}
	beforeRemotes := append([]EndpointIdentity(nil), doc.Remotes...)
	beforeSessions := append([]SessionIdentity(nil), doc.Sessions...)
	beforeUnknown := append([]UnknownField(nil), doc.Unknown...)
	beforeLocalUnknown := append([]UnknownField(nil), doc.Local.Unknown...)

	if _, err := json.Marshal(doc); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(doc.Remotes, beforeRemotes) || !reflect.DeepEqual(doc.Sessions, beforeSessions) || !reflect.DeepEqual(doc.Unknown, beforeUnknown) || !reflect.DeepEqual(doc.Local.Unknown, beforeLocalUnknown) {
		t.Fatal("canonical identity marshal mutated caller-visible collection order")
	}
}

func TestIdentityDocumentV1ValidationAndFormattingDoNotLeak(t *testing.T) {
	raw := "raw-secret-v1-state"
	localID := mustRedactID(t, IDKindLocalSKI, "local-v1")
	doc := NewIdentityDocumentV1(time.Now(), EndpointIdentity{
		Role:    EndpointRoleLocal,
		ID:      localID,
		Pairing: PairingObservation{State: PairingState(raw)},
	})
	err := doc.Validate()
	if err == nil {
		t.Fatal("Validate() succeeded for caller-controlled pairing state")
	}
	combined := err.Error() + "\n" + fmt.Sprint(doc) + "\n" + fmt.Sprintf("%+v %#v", doc, doc)
	if strings.Contains(combined, raw) {
		t.Fatalf("stable identity validation or formatting leaked raw input: %s", combined)
	}

	doc = NewIdentityDocumentV1(time.Now(), EndpointIdentity{Role: EndpointRoleLocal, ID: localID})
	doc.Sessions = []SessionIdentity{{
		ID:       mustRedactID(t, IDKindSession, "session-v1"),
		RemoteID: mustRedactID(t, IDKindRemoteSKI, "remote-v1"),
		State:    SessionState(raw),
	}}
	err = doc.Validate()
	if err == nil {
		t.Fatal("Validate() succeeded for caller-controlled session state")
	}
	if strings.Contains(err.Error(), raw) {
		t.Fatalf("stable identity validation leaked raw session state: %v", err)
	}
}

func TestIdentityContractVersionsRemainIsolated(t *testing.T) {
	local := EndpointIdentity{Role: EndpointRoleLocal, ID: mustRedactID(t, IDKindLocalSKI, "local-contract")}
	alpha := NewIdentityDocument(time.Now(), local)
	alpha.Contract = IdentityContractV1
	if err := alpha.Validate(); err == nil {
		t.Fatal("v1alpha1 identity accepted stable v1 contract")
	}
	stable := NewIdentityDocumentV1(time.Now(), local)
	stable.Contract = IdentityContractV1Alpha1
	if err := stable.Validate(); err == nil {
		t.Fatal("stable v1 identity accepted v1alpha1 contract")
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
