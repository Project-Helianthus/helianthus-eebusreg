package eebusstore

import (
	"reflect"
	"testing"
)

func TestMSP045ControlSchemaAndWireRemainExactlyV3(t *testing.T) {
	if currentSchemaVersion != 3 {
		t.Fatalf("control schema = %d, want unchanged v3", currentSchemaVersion)
	}
	assertMSP045WireShape(t, reflect.TypeOf(controlRecordWireV3{}), []string{
		"AssociationLineage:association_lineage",
		"Attempts:attempts",
		"ControlEpoch:control_epoch",
		"OperationHighWater:operation_high_water",
		"Publication:publication",
		"Quarantines:quarantines",
		"Receipts:receipts",
		"RepairSequence:repair_sequence",
		"StoreInstance:store_instance",
		"Tombstones:tombstones",
	})
	assertMSP045FieldNames(t, reflect.TypeOf(ControlRecord{}), []string{
		"Present",
		"StoreInstance",
		"ControlEpoch",
		"AssociationLineage",
		"Tombstones",
		"Quarantines",
		"Receipts",
		"OperationHighWater",
		"RepairSequence",
		"Publication",
		"ReplaceLocalIdentity",
		"LocalCertificateChainDER",
		"LocalProviderID",
		"LocalProviderVersion",
		"LocalSealedBlob",
		"LocalCertificateSPKISHA256",
		"LocalSKI",
	})
}

func assertMSP045WireShape(t *testing.T, typ reflect.Type, want []string) {
	t.Helper()
	got := make([]string, 0, typ.NumField())
	for index := 0; index < typ.NumField(); index++ {
		field := typ.Field(index)
		got = append(got, field.Name+":"+field.Tag.Get("json"))
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s wire fields = %v, want %v", typ.Name(), got, want)
	}
}

func assertMSP045FieldNames(t *testing.T, typ reflect.Type, want []string) {
	t.Helper()
	got := make([]string, 0, typ.NumField())
	for index := 0; index < typ.NumField(); index++ {
		got = append(got, typ.Field(index).Name)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s fields = %v, want %v", typ.Name(), got, want)
	}
}
