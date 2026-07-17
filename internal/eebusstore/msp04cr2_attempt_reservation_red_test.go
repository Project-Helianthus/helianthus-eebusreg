package eebusstore

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestMSP04CR2AttemptReservationAdvancesInternalSchemaDeterministically(t *testing.T) {
	if currentSchemaVersion != 3 {
		t.Fatalf("current internal schema = %d, want 3 for durable outgoing attempts", currentSchemaVersion)
	}
	legacy := withControlRecordV2(stateV1{}, msp04cStoreControlFixture())
	first, err := migrateMSP04CStateToMSP04CR2(legacy)
	if err != nil {
		t.Fatalf("migrate MSP-04C state: %v", err)
	}
	second, err := migrateMSP04CStateToMSP04CR2(legacy)
	if err != nil {
		t.Fatalf("repeat MSP-04C migration: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("MSP-04C-to-R2 migration is not deterministic")
	}
	record, ok := controlRecordV3FromStateV1(first)
	if !ok {
		t.Fatal("migrated control record is not readable as v3")
	}
	if len(record.attempts) != 0 {
		t.Fatal("migration invented an outgoing attempt reservation")
	}
}

func TestMSP04CR2AttemptReservationRoundTripsCanonicalMechanicalFields(t *testing.T) {
	want := msp04cr2StoreFixture()
	first, err := encodeControlRecordV3(want)
	if err != nil {
		t.Fatalf("encode v3 control record: %v", err)
	}
	got, err := decodeControlRecordV3(first)
	if err != nil {
		t.Fatalf("decode v3 control record: %v", err)
	}
	second, err := encodeControlRecordV3(got)
	if err != nil {
		t.Fatalf("re-encode v3 control record: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("durable attempt encoding is not canonical")
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("durable attempt round trip changed a mechanical binding")
	}
}

func TestMSP04CR2AttemptCloneOwnsOpaqueAndEndpointBindings(t *testing.T) {
	source := msp04cr2StoreFixture()
	clone := cloneControlRecordV3(source)
	source.attempts[0].attemptID[0]++
	source.attempts[0].remoteSKI[0]++
	source.attempts[0].scope[0]++
	source.attempts[0].associationLineage[0]++
	if reflect.DeepEqual(source, clone) {
		t.Fatal("durable attempt clone aliases caller-owned bytes")
	}
	if !reflect.DeepEqual(clone, msp04cr2StoreFixture()) {
		t.Fatal("durable attempt clone changed the original value")
	}
}

func TestMSP04CR2AttemptValidationIsClosedBoundedAndUnique(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*controlRecordV3)
	}{
		{name: "unknown state", mutate: func(record *controlRecordV3) { record.attempts[0].stateCode = 0 }},
		{name: "missing attempt id", mutate: func(record *controlRecordV3) { record.attempts[0].attemptID = nil }},
		{name: "wrong remote ski size", mutate: func(record *controlRecordV3) { record.attempts[0].remoteSKI = record.attempts[0].remoteSKI[:19] }},
		{name: "missing scope", mutate: func(record *controlRecordV3) { record.attempts[0].scope = nil }},
		{name: "future control epoch", mutate: func(record *controlRecordV3) { record.attempts[0].controlEpoch = record.controlEpoch + 1 }},
		{name: "wrong lineage size", mutate: func(record *controlRecordV3) { record.attempts[0].associationLineage = nil }},
		{name: "missing endpoint host", mutate: func(record *controlRecordV3) { record.attempts[0].endpointHost = "" }},
		{name: "missing endpoint port", mutate: func(record *controlRecordV3) { record.attempts[0].endpointPort = 0 }},
		{name: "missing cancellation generation", mutate: func(record *controlRecordV3) { record.attempts[0].cancellationGeneration = 0 }},
		{name: "missing reservation order", mutate: func(record *controlRecordV3) { record.attempts[0].reservationOrder = 0 }},
		{name: "negative reservation timestamp", mutate: func(record *controlRecordV3) { record.attempts[0].reservationTimestamp = -1 }},
		{name: "duplicate attempt id", mutate: func(record *controlRecordV3) {
			record.attempts = append(record.attempts, cloneControlAttemptV3(record.attempts[0]))
			record.attempts[1].scope = msp04cr2StoreOrdinal(20)
		}},
		{name: "duplicate scope", mutate: func(record *controlRecordV3) {
			record.attempts = append(record.attempts, cloneControlAttemptV3(record.attempts[0]))
			record.attempts[1].attemptID = msp04cr2StoreOrdinal(21)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := msp04cr2StoreFixture()
			test.mutate(&record)
			if err := validateControlRecordV3(record); err == nil {
				t.Fatal("invalid durable attempt record was accepted")
			}
		})
	}

	record := msp04cr2StoreFixture()
	record.attempts = make([]controlAttemptV3, maxControlAttemptCount+1)
	if err := validateControlRecordV3(record); err == nil {
		t.Fatal("out-of-bound active attempt set was accepted")
	}
}

func TestMSP04CR2ControlBridgeExposesOnlyMechanicalAttemptData(t *testing.T) {
	typeOf := reflect.TypeOf(ControlAttempt{})
	want := []string{
		"StateCode uint64",
		"AttemptID [32]uint8",
		"RemoteSKI []uint8",
		"Scope [32]uint8",
		"ControlEpoch uint64",
		"AssociationLineage [32]uint8",
		"EndpointHost string",
		"EndpointPort uint16",
		"Path string",
		"CancellationGeneration uint64",
		"ReservationOrder uint64",
		"ReservationTimestamp int64",
		"AttemptCountBefore uint64",
	}
	got := make([]string, 0, typeOf.NumField())
	for index := 0; index < typeOf.NumField(); index++ {
		field := typeOf.Field(index)
		if field.PkgPath != "" || field.Anonymous {
			t.Fatalf("ControlAttempt field %q is not exported mechanical data", field.Name)
		}
		got = append(got, field.Name+" "+field.Type.String())
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ControlAttempt fields = %v, want %v", got, want)
	}
	control := reflect.TypeOf(ControlRecord{})
	field, ok := control.FieldByName("Attempts")
	if !ok || field.Type.String() != "[]eebusstore.ControlAttempt" {
		t.Fatalf("ControlRecord.Attempts = %v/%t, want []eebusstore.ControlAttempt", field.Type, ok)
	}
}

func TestMSP04CR2StoreRemainsPolicyFree(t *testing.T) {
	forbidden := []string{"ATTEMPT_RESERVED", "ATTEMPT_LAUNCH_AUTHORIZED", "PERMIT", "DENY"}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	files := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(files, entry.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Errorf("decode store string literal: %v", err)
				return true
			}
			for _, policy := range forbidden {
				if strings.Contains(value, policy) {
					t.Errorf("mechanical store contains coordinator policy literal %q", policy)
				}
			}
			return true
		})
	}
}

func msp04cr2StoreFixture() controlRecordV3 {
	legacy := msp04cStoreControlFixture()
	return controlRecordV3{
		storeInstance: legacy.storeInstance, controlEpoch: 10, associationLineage: legacy.associationLineage,
		tombstones: legacy.tombstones, quarantines: legacy.quarantines, receipts: legacy.receipts,
		operationHighWater: legacy.operationHighWater, repairSequence: legacy.repairSequence,
		attempts: []controlAttemptV3{{
			stateCode: 1, attemptID: msp04cr2StoreOrdinal(10), remoteSKI: bytes.Repeat([]byte{0x3a}, 20),
			scope: msp04cr2StoreOrdinal(11), controlEpoch: 10, associationLineage: msp04cr2StoreOrdinal(12),
			endpointHost: "peer.invalid", endpointPort: 4712, path: "/ship/",
			cancellationGeneration: 3, reservationOrder: 7, reservationTimestamp: 123456789, attemptCountBefore: 2,
		}},
	}
}

func msp04cr2StoreOrdinal(value byte) []byte {
	result := make([]byte, 32)
	result[len(result)-1] = value
	return result
}
