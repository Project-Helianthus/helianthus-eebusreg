package eebusstore

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMSP04CControlRecordUsesOnlyCurrentSchema(t *testing.T) {
	if currentSchemaVersion != 3 {
		t.Fatalf("current internal schema = %d, want 3", currentSchemaVersion)
	}
}

func TestMSP04CControlRecordRoundTripsCanonicalMechanicalState(t *testing.T) {
	record := msp04cStoreControlFixture()
	generation := generationV1{
		metadata:      generationMetadata{sequence: 19},
		state:         withControlRecordV3(stateV1{}, record),
		schemaVersion: currentSchemaVersion,
	}
	first, err := encodeGenerationV1(generation)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeGenerationV1(first)
	if err != nil {
		t.Fatal(err)
	}
	second, err := encodeGenerationV1(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("control record encoding is not canonical")
	}
	if !reflect.DeepEqual(decoded, generation) {
		t.Fatal("control record round trip changed mechanical fields")
	}
}

func TestMSP04CControlCloneOwnsEveryMutableField(t *testing.T) {
	source := msp04cStoreControlFixture()
	clone := cloneControlRecordV3(source)
	source.storeInstance[0]++
	source.associationLineage[0]++
	source.tombstones[0].associationRef[0]++
	source.quarantines[0].scope[0]++
	source.receipts[0].operationID[0]++
	source.publication.operationID[0]++
	if reflect.DeepEqual(source, clone) {
		t.Fatal("control clone aliases caller-owned fields")
	}
	want := msp04cStoreControlFixture()
	if !reflect.DeepEqual(clone, want) {
		t.Fatal("control clone changed the original mechanical value")
	}
}

func TestMSP04CControlValidationIsClosedAndBounded(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*controlRecordV3)
	}{
		{name: "store instance absent", mutate: func(record *controlRecordV3) { record.storeInstance = nil }},
		{name: "lineage absent", mutate: func(record *controlRecordV3) { record.associationLineage = nil }},
		{name: "control epoch exhausted", mutate: func(record *controlRecordV3) { record.controlEpoch = ^uint64(0) }},
		{name: "duplicate tombstone", mutate: func(record *controlRecordV3) { record.tombstones = append(record.tombstones, record.tombstones[0]) }},
		{name: "duplicate quarantine", mutate: func(record *controlRecordV3) { record.quarantines = append(record.quarantines, record.quarantines[0]) }},
		{name: "duplicate receipt", mutate: func(record *controlRecordV3) { record.receipts = append(record.receipts, record.receipts[0]) }},
		{name: "negative remainder", mutate: func(record *controlRecordV3) { record.quarantines[0].remainingDelay = -1 }},
		{name: "negative retention", mutate: func(record *controlRecordV3) { record.quarantines[0].retentionBudget = -1 }},
		{name: "publication epoch mismatch", mutate: func(record *controlRecordV3) { record.publication.targetControlEpoch += 2 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := msp04cStoreControlFixture()
			test.mutate(&record)
			if err := validateStateV1(withControlRecordV3(stateV1{}, record)); err == nil {
				t.Fatal("invalid mechanical control record was accepted")
			}
		})
	}

	boundTests := []struct {
		name   string
		mutate func(*controlRecordV3)
	}{
		{name: "tombstones", mutate: func(record *controlRecordV3) {
			record.tombstones = make([]controlTombstone, maxControlTombstoneCount+1)
		}},
		{name: "quarantines", mutate: func(record *controlRecordV3) {
			record.quarantines = make([]controlQuarantine, maxControlQuarantineCount+1)
		}},
		{name: "receipts", mutate: func(record *controlRecordV3) { record.receipts = make([]controlReceipt, maxControlReceiptCount+1) }},
	}
	for _, test := range boundTests {
		t.Run(test.name, func(t *testing.T) {
			record := msp04cStoreControlFixture()
			test.mutate(&record)
			if err := validateStateV1(withControlRecordV3(stateV1{}, record)); err == nil {
				t.Fatal("out-of-bound control record was accepted")
			}
		})
	}
}

func TestMSP04CStoreProductionCodeContainsNoRecoveryPolicy(t *testing.T) {
	forbiddenLiterals := []string{
		"ADMIN_HOLD",
		"BACKOFF_ACTIVE",
		"CLONE_DETECTED",
		"CONTROL_EPOCH_ROLLBACK",
		"CORRUPT_STORE",
		"DURABILITY_UNKNOWN",
		"HOST_BINDING_MISMATCH",
		"HOST_KEY_UNAVAILABLE",
		"MANIFEST_GENERATION_ROLLBACK",
		"NO_LOCAL_IDENTITY",
		"PAIRED_TRUSTED",
		"QUARANTINED",
		"RETRY_READY",
		"REVOKED_ASSOCIATION",
		"UNPAIRED_LOCKED",
		"adopt_copied_current",
		"publish_inactive_parent",
		"recover_unavailable_host_key",
		"reconcile_pending_publication",
		"release_retry_quarantine",
	}
	forbiddenImports := []string{
		"github.com/Project-Helianthus/helianthus-eebusreg/internal/eebusfacade",
		"net",
		"net/http",
		"time",
	}

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
		for _, imported := range parsed.Imports {
			path, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if slices.Contains(forbiddenImports, path) {
				t.Fatalf("mechanical store imports policy dependency %q", path)
			}
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Errorf("decode string literal: %v", err)
				return true
			}
			for _, forbidden := range forbiddenLiterals {
				if strings.Contains(value, forbidden) {
					t.Errorf("mechanical store contains coordinator policy literal %q", forbidden)
				}
			}
			return true
		})
	}
}

func msp04cStoreControlFixture() controlRecordV3 {
	previous := generationReference{generation: 17, generationFile: generationFilename(17), generationSHA256: strings.Repeat("1", 64), schemaVersion: currentSchemaVersion}
	target := generationReference{generation: 18, generationFile: generationFilename(18), generationSHA256: strings.Repeat("2", 64), schemaVersion: currentSchemaVersion}
	return controlRecordV3{
		storeInstance:      msp04cStoreOrdinal(1),
		controlEpoch:       9,
		associationLineage: msp04cStoreOrdinal(2),
		tombstones: []controlTombstone{{
			associationRef: msp04cStoreOrdinal(3), revocationEpoch: 8,
			operationID: msp04cStoreOrdinal(4), effectiveGeneration: target,
		}},
		quarantines: []controlQuarantine{{
			scope: msp04cStoreOrdinal(5), reasonCode: 2, stateCode: 1, attemptCount: 3,
			backoffStep: 2, remainingDelay: int64(10 * time.Second), retentionBudget: int64(30 * time.Second), lastControlEpoch: 8,
		}},
		receipts: []controlReceipt{{
			operationID: msp04cStoreOrdinal(6), operationClass: 2, bindingSHA256: msp04cStoreOrdinal(7), resultCode: 1, terminal: true,
		}},
		operationHighWater: 6,
		repairSequence:     4,
		publication: &controlPublication{
			operationID: msp04cStoreOrdinal(8), operationClass: 1, storeInstance: msp04cStoreOrdinal(1),
			previousControlEpoch: 8, targetControlEpoch: 9, previousGeneration: previous, targetGeneration: target,
		},
	}
}

func msp04cStoreOrdinal(value uint64) []byte {
	result := make([]byte, 32)
	for index := 0; index < 8; index++ {
		result[len(result)-1-index] = byte(value >> (index * 8))
	}
	return result
}
