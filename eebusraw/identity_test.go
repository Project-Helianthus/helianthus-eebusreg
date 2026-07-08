package eebusraw

import (
	"encoding/json"
	"fmt"
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
