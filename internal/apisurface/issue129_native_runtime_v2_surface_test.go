package main

import (
	"strings"
	"testing"
)

func TestIssue129NativeRuntimeV2SurfaceIsClosedAndValuePreserving(t *testing.T) {
	doc, err := extract(moduleRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	root := msp05pRootSurface(t, doc)
	want := map[string]string{
		"const NativeSnapshotContractV2":   `const NativeSnapshotContractV2 = "helianthus.eebus.runtime.native-snapshot.v2"`,
		"func NewNativeRuntimeV2":          "func NewNativeRuntimeV2(Config) (NativeRuntimeV2, error)",
		"func NewNativeSnapshotV2":         "func NewNativeSnapshotV2(NativeSnapshotV2) (NativeSnapshotV2, error)",
		"method NativeSnapshotV2.Clone":    "func (NativeSnapshotV2) Clone() NativeSnapshotV2",
		"method NativeSnapshotV2.Validate": "func (NativeSnapshotV2) Validate() error",
		"type NativeDegradationV2":         `type NativeDegradationV2 struct{ Reason string "json:\"reason\""; Since time.Time "json:\"since\"" }`,
		"type NativeDeviceV2":              `type NativeDeviceV2 struct{ SKI string "json:\"ski\""; SHIPID *string "json:\"ship_id,omitempty\""; Address string "json:\"address\""; Type string "json:\"type\""; Description *string "json:\"description,omitempty\""; Metadata map[string]string "json:\"metadata,omitempty\""; Observations []NativeObservationV2 "json:\"observations,omitempty\"" }`,
		"type NativeEntityV2":              `type NativeEntityV2 struct{ DeviceAddress string "json:\"device_address\""; EntityAddress string "json:\"entity_address\""; Type string "json:\"type\""; Description *string "json:\"description,omitempty\"" }`,
		"type NativeFeatureV2":             `type NativeFeatureV2 struct{ DeviceAddress string "json:\"device_address\""; EntityAddress string "json:\"entity_address\""; FeatureAddress string "json:\"feature_address\""; Type string "json:\"type\""; Role string "json:\"role\""; Description *string "json:\"description,omitempty\"" }`,
		"type NativeObservationV2":         `type NativeObservationV2 struct{ Path string "json:\"path\""; Source string "json:\"source\""; ObservedAt time.Time "json:\"observed_at\""; ProtocolVersion *string "json:\"protocol_version,omitempty\""; Value NativeValueV2 "json:\"value\"" }`,
		"type NativePairingObservationV2":  `type NativePairingObservationV2 struct{ RemoteSKI string "json:\"remote_ski\""; State string "json:\"state\""; Since time.Time "json:\"since\"" }`,
		"type NativeRuntimeObservationV2":  `type NativeRuntimeObservationV2 struct{ State string "json:\"state\""; Degradation *NativeDegradationV2 "json:\"degradation,omitempty\"" }`,
		"type NativeRuntimeV2":             "type NativeRuntimeV2 interface{ NativePairingState() ([]NativePairingObservationV2, error); NativeSnapshot() (NativeSnapshotV2, error); Shutdown() error; Start(context.Context) error }",
		"type NativeServiceV2":             `type NativeServiceV2 struct{ SKI string "json:\"ski\""; SHIPID *string "json:\"ship_id,omitempty\""; Kind string "json:\"kind\""; Visible bool "json:\"visible\""; Paired bool "json:\"paired\""; Name *string "json:\"name,omitempty\""; Identifier *string "json:\"identifier,omitempty\""; Brand *string "json:\"brand,omitempty\""; Type *string "json:\"type,omitempty\""; Model *string "json:\"model,omitempty\"" }`,
		"type NativeSessionV2":             `type NativeSessionV2 struct{ ID string "json:\"id\""; RemoteSKI string "json:\"remote_ski\""; State string "json:\"state\""; Since time.Time "json:\"since\"" }`,
		"type NativeSnapshotMetaV2":        `type NativeSnapshotMetaV2 struct{ Contract string "json:\"contract\""; Runtime string "json:\"runtime\""; LocalSKI string "json:\"local_ski\""; Source string "json:\"source\""; ObservedAt time.Time "json:\"observed_at\""; ProtocolVersion *string "json:\"protocol_version,omitempty\""; CapturedAt time.Time "json:\"captured_at\""; DataTimestamp time.Time "json:\"data_timestamp\"" }`,
		"type NativeSnapshotV2":            `type NativeSnapshotV2 struct{ Meta NativeSnapshotMetaV2 "json:\"meta\""; Status NativeRuntimeObservationV2 "json:\"status\""; Pairing []NativePairingObservationV2 "json:\"pairing\""; Services []NativeServiceV2 "json:\"services\""; Sessions []NativeSessionV2 "json:\"sessions\""; Devices []NativeDeviceV2 "json:\"devices\""; Entities []NativeEntityV2 "json:\"entities\""; Features []NativeFeatureV2 "json:\"features\""; UseCases []NativeUseCaseV2 "json:\"usecases\""; Observations []NativeObservationV2 "json:\"observations\"" }`,
		"type NativeUseCaseV2":             `type NativeUseCaseV2 struct{ ContextAddress string "json:\"context_address\""; Name string "json:\"name\""; Actor string "json:\"actor\""; ResolvedRole *string "json:\"resolved_role,omitempty\""; Scenarios []string "json:\"scenarios\""; Version *string "json:\"version,omitempty\""; Availability *bool "json:\"availability,omitempty\""; DocumentSubrevision *string "json:\"document_subrevision,omitempty\"" }`,
		"type NativeValueV2":               `type NativeValueV2 struct{ Null *bool "json:\"null,omitempty\""; Boolean *bool "json:\"boolean,omitempty\""; Integer *int64 "json:\"integer,omitempty\""; Float *float64 "json:\"float,omitempty\""; String *string "json:\"string,omitempty\""; Array []NativeValueV2 "json:\"array,omitempty\""; Object map[string]NativeValueV2 "json:\"object,omitempty\"" }`,
	}
	got := make(map[string]string, len(want))
	for _, value := range root.Symbols {
		if !issue129NativeV2Symbol(value) {
			continue
		}
		key := value.Kind + " " + value.Name
		if value.Receiver != nil {
			key = value.Kind + " " + value.Receiver.Base + "." + value.Name
		}
		got[key] = value.Signature
		for _, forbidden := range []string{
			"Redacted", "MaskTier", "Opaque", "Digest", "Hash", "PairingAuthority",
			"PairingPolicy", "Admin", "Action", "Candidate", "Handle", "ConnectRequest",
			"ConfirmRequest", "SelectRequest", "UntrustRequest", "RetryTrustedRequest",
			"OpenPairingWindowRequest", "ClosePairingWindowRequest", "MutationPrecondition",
		} {
			if strings.Contains(value.Signature, forbidden) {
				t.Fatalf("V2 API signature %q retains forbidden redaction or policy family %s", key, forbidden)
			}
		}
	}
	if len(got) != len(want) {
		t.Fatalf("V2 export inventory = %#v, want %#v", got, want)
	}
	for key, signature := range want {
		if got[key] != signature {
			t.Errorf("%s signature = %q, want %q", key, got[key], signature)
		}
	}
}
