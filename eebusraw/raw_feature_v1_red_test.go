package eebusraw_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

func TestIssue83TypedValueIsCanonicalDetachedAndSecretSafe(t *testing.T) {
	source := map[string]any{
		"zeta": []any{int64(7), "raw-value"},
		"alpha": map[string]any{
			"enabled": true,
			"exact":   "12.500",
		},
	}
	value, err := eebusraw.NewTypedValueV1(source)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	firstHash, err := value.ComputeHash()
	if err != nil {
		t.Fatal(err)
	}

	source["alpha"].(map[string]any)["enabled"] = false
	source["zeta"].([]any)[0] = int64(99)
	secondJSON, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := value.ComputeHash()
	if err != nil {
		t.Fatal(err)
	}
	if string(firstJSON) != `{"alpha":{"enabled":true,"exact":"12.500"},"zeta":[7,"raw-value"]}` {
		t.Fatalf("canonical JSON = %s", firstJSON)
	}
	if string(secondJSON) != string(firstJSON) || secondHash != firstHash {
		t.Fatalf("source mutation changed detached value: json=%s hash=%q", secondJSON, secondHash)
	}

	extracted := value.Value().(map[string]any)
	extracted["alpha"].(map[string]any)["enabled"] = false
	thirdJSON, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if string(thirdJSON) != string(firstJSON) {
		t.Fatalf("Value() exposed mutable storage: %s", thirdJSON)
	}

	clone := value.Clone()
	if !reflect.DeepEqual(clone.Value(), value.Value()) {
		t.Fatalf("Clone() = %#v, want %#v", clone.Value(), value.Value())
	}
	if got := fmt.Sprintf("%v %#v %s", value, value, value); strings.Contains(got, "raw-value") {
		t.Fatalf("formatter disclosed typed value: %q", got)
	}
}

func TestIssue83CanonicalJSONUsesRFC8785StringEscaping(t *testing.T) {
	value, err := eebusraw.NewTypedValueV1(map[string]any{
		"a": "line\u2028separator",
		"z": "\u000f",
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := value.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"a\":\"line\xe2\x80\xa8separator\",\"z\":\"\\u000f\"}"
	if string(encoded) != want {
		t.Fatalf("RFC 8785 string encoding = %q, want %q", encoded, want)
	}
}

func TestIssue83UnsafeJSONIntegersBecomeExactStrings(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "positive unsafe", raw: `{"value":9007199254740992}`, want: `{"value":"9007199254740992"}`},
		{name: "negative unsafe", raw: `{"value":-9007199254740992}`, want: `{"value":"-9007199254740992"}`},
		{name: "uint64 maximum", raw: `{"value":18446744073709551615}`, want: `{"value":"18446744073709551615"}`},
		{name: "exact decimal", raw: `{"value":12.500}`, want: `{"value":"12.500"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value, err := eebusraw.DecodeTypedValueV1([]byte(test.raw))
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := value.MarshalJSON()
			if err != nil {
				t.Fatal(err)
			}
			if string(encoded) != test.want {
				t.Fatalf("canonical JSON = %s, want %s", encoded, test.want)
			}
		})
	}

	for _, value := range []any{int64(9007199254740992), uint64(18446744073709551615)} {
		typed, err := eebusraw.NewTypedValueV1(value)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := typed.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) < 2 || encoded[0] != '"' || encoded[len(encoded)-1] != '"' {
			t.Fatalf("unsafe integer %T encoded as %s, want exact JSON string", value, encoded)
		}
	}
}

func TestIssue83TypedJSONRejectsLoneUTF16Surrogates(t *testing.T) {
	for _, raw := range []string{
		`{"value":"\uD800"}`,
		`{"value":"\uDC00"}`,
		`{"value":"prefix\uD800suffix"}`,
	} {
		if _, err := eebusraw.DecodeTypedValueV1([]byte(raw)); err == nil {
			t.Fatalf("DecodeTypedValueV1(%q) accepted a lone UTF-16 surrogate", raw)
		}
	}
	value, err := eebusraw.DecodeTypedValueV1([]byte(`{"value":"\uD83D\uDE00"}`))
	if err != nil {
		t.Fatalf("valid surrogate pair rejected: %v", err)
	}
	encoded, err := value.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"value":"😀"}` {
		t.Fatalf("valid surrogate pair = %s", encoded)
	}
}

func TestIssue83TypedValueRejectsSecretNamesAndValues(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{
			name: "normalized private key field",
			value: map[string]any{
				"Private-Key": "not-even-a-key",
			},
		},
		{
			name: "normalized authentication token field",
			value: map[string]any{
				"authentication Token": "not-even-a-token",
			},
		},
		{
			name: "unicode compatibility private field",
			value: map[string]any{
				"\uFF30rivateKey": "not-even-a-key",
			},
		},
		{
			name: "normalized protected store field",
			value: map[string]any{
				"trust" + "StoreBytes": "not-even-store-material",
			},
		},
		{
			name: "bearer scalar",
			value: map[string]any{
				"status": "Bearer restricted-value",
			},
		},
		{
			name: "unicode compatibility bearer scalar",
			value: map[string]any{
				"status": "\uFF22\uFF45\uFF41\uFF52\uFF45\uFF52 restricted-value",
			},
		},
		{
			name: "private key boundary",
			value: map[string]any{
				"status": "-----BEGIN PRIVATE KEY-----",
			},
		},
		{
			name: "ed25519 private key boundary",
			value: map[string]any{
				"status": "-----BEGIN ED25519 PRIVATE KEY-----",
			},
		},
		{
			name: "generic private key boundary",
			value: map[string]any{
				"status": "-----BEGIN VENDOR HARDWARE PRIVATE KEY-----",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := eebusraw.NewTypedValueV1(test.value)
			if !errors.Is(err, eebusraw.ErrSecretDetected) {
				t.Fatalf("NewTypedValueV1() error = %v, want %v", err, eebusraw.ErrSecretDetected)
			}
			if err != nil {
				for _, forbidden := range []string{
					"restricted-value",
					"not-even-a-key",
					"not-even-a-token",
					"not-even-store-material",
				} {
					if strings.Contains(err.Error(), forbidden) {
						t.Fatalf("secret error disclosed %q: %v", forbidden, err)
					}
				}
			}
		})
	}
}

func TestIssue83CanonicalToolNamesAndOptionalTimeoutShape(t *testing.T) {
	if eebusraw.ToolV1FeaturesGet != "eebus.v1.features.get" ||
		eebusraw.ToolV1FeaturesDataGet != "eebus.v1.features.data.get" {
		t.Fatalf(
			"canonical tools = %q, %q",
			eebusraw.ToolV1FeaturesGet,
			eebusraw.ToolV1FeaturesDataGet,
		)
	}
	encoded, err := json.Marshal(eebusraw.FeatureDataGetRequestV1{
		Targets: []eebusraw.FeatureTargetV1{issue83Target()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "timeout_ms") {
		t.Fatalf("omitted optional timeout encoded as an invalid zero value: %s", encoded)
	}
}

func TestIssue83RawFeatureDTOCloneDetachesNestedState(t *testing.T) {
	value, err := eebusraw.NewTypedValueV1(map[string]any{
		"measurementData": []any{
			map[string]any{"measurementId": int64(11), "value": map[string]any{"number": int64(215), "scale": int64(-1)}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	target := issue83Target()
	observation := eebusraw.ReadObservationV1{
		Target:  target,
		Runtime: eebusraw.RuntimeBindingV1{RuntimeEpoch: 9, ConnectionGeneration: 4},
		RawRequest: eebusraw.ProtocolMessageV1{
			Classifier:     "READ",
			CorrelationKey: 41,
			Function:       target.Function,
		},
		RawResponse: eebusraw.ProtocolMessageV1{
			Classifier:     "REPLY",
			CorrelationKey: 41,
			Function:       target.Function,
			Data:           &value,
		},
		Value:         value,
		Unknown:       []eebusraw.OpaqueObservationV1{{Path: "/extension/a", Source: "spine", Value: value}},
		RequestedAt:   time.Unix(100, 0).UTC(),
		ReceivedAt:    time.Unix(101, 0).UTC(),
		DataTimestamp: time.Unix(101, 0).UTC(),
		Source:        eebusraw.ObservationSourceV1Live,
		ReadToken: eebusraw.ReadTokenV1{
			ReadToken:   "read1:opaque",
			Reusable:    false,
			ExpiresAt:   time.Unix(161, 0).UTC(),
			BindingHash: eebusraw.HashV1("sha256:" + strings.Repeat("1", 64)),
		},
		DataHash: eebusraw.HashV1("sha256:" + strings.Repeat("2", 64)),
	}
	data := eebusraw.FeatureDataGetDataV1{
		Results:  []eebusraw.ReadObservationV1{observation},
		Failures: []eebusraw.ReadFailureV1{},
		Complete: true,
	}

	clone := data.Clone()
	data.Results[0].Target.EntityAddress[0] = 99
	data.Results[0].Unknown[0].Path = "/mutated"
	extracted := data.Results[0].Value.Value().(map[string]any)
	extracted["measurementData"].([]any)[0].(map[string]any)["measurementId"] = int64(99)

	if clone.Results[0].Target.EntityAddress[0] != 1 {
		t.Fatalf("clone target mutated: %+v", clone.Results[0].Target)
	}
	if clone.Results[0].Unknown[0].Path != "/extension/a" {
		t.Fatalf("clone unknown fields mutated: %+v", clone.Results[0].Unknown)
	}
	clonedValue := clone.Results[0].Value.Value().(map[string]any)
	if got := clonedValue["measurementData"].([]any)[0].(map[string]any)["measurementId"]; got != int64(11) {
		t.Fatalf("clone typed value mutated: %v", got)
	}
}

func TestIssue83ReadTokenFormattingIsOpaque(t *testing.T) {
	token := eebusraw.ReadTokenV1{
		ReadToken:   "read1:purpose-bound-reference",
		Reusable:    false,
		ExpiresAt:   time.Unix(161, 0).UTC(),
		BindingHash: eebusraw.HashV1("sha256:" + strings.Repeat("3", 64)),
	}
	formatted := fmt.Sprintf("%v %#v %s %q", token, token, token, token)
	for _, forbidden := range []string{"purpose-bound-reference", strings.Repeat("3", 64)} {
		if strings.Contains(formatted, forbidden) {
			t.Fatalf("formatter disclosed token material %q: %q", forbidden, formatted)
		}
	}
}

func TestIssue83RawResultFormattingDoesNotDiscloseOperatorData(t *testing.T) {
	value, err := eebusraw.NewTypedValueV1(map[string]any{"value": "operator-raw-value"})
	if err != nil {
		t.Fatal(err)
	}
	target := issue83Target()
	data := eebusraw.FeatureDataGetDataV1{
		Results: []eebusraw.ReadObservationV1{{
			Target: target,
			RawResponse: eebusraw.ProtocolMessageV1{
				Classifier: "REPLY",
				Function:   target.Function,
				Data:       &value,
			},
			Value: value,
			ReadToken: eebusraw.ReadTokenV1{
				ReadToken: "read1:operator-token",
			},
		}},
		Complete: true,
	}
	formatted := fmt.Sprintf("%v %#v %+v", data, data, data)
	for _, forbidden := range []string{
		"operator-raw-value",
		"operator-token",
		target.RemoteSKI,
		target.SHIPID,
		target.DeviceAddress,
	} {
		if strings.Contains(formatted, forbidden) {
			t.Fatalf("formatter disclosed operator data %q: %q", forbidden, formatted)
		}
	}
}

func issue83Target() eebusraw.FeatureTargetV1 {
	return eebusraw.FeatureTargetV1{
		RemoteSKI:      strings.Repeat("a", 40),
		SHIPID:         "vr940-ship-id",
		DeviceAddress:  "d:_i:1",
		EntityAddress:  []uint64{1},
		FeatureAddress: 11,
		FeatureType:    "measurement",
		FeatureRole:    eebusraw.FeatureRoleV1Server,
		Function:       "measurementListData",
		Operation:      eebusraw.OperationV1Read,
	}
}
