package eebusmutation

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRawMutationWALRestoresDurablePrefixAfterPartialWriteOrSyncFailure(t *testing.T) {
	for _, test := range []struct {
		name   string
		inject func(*rawMutationJournal)
	}{
		{
			name: "partial write",
			inject: func(journal *rawMutationJournal) {
				journal.write = func(payload []byte) (int, error) {
					count := len(payload) / 2
					if count == 0 {
						count = 1
					}
					written, _ := journal.file.Write(payload[:count])
					return written, errors.New("synthetic partial write")
				}
			},
		},
		{
			name: "file sync",
			inject: func(journal *rawMutationJournal) {
				probe := &issue85PersistenceProbe{}
				probe.failNextSync(issue85PersistenceFileSync)
				journal.persistence = probe
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newIssue85Harness(t)
			mutation, terminal := harness.set()
			issue85AssertNoError(t, terminal)
			harness.closeClean()

			path := filepath.Join(harness.root, "eebusmutation", "mutation-v1.wal")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			journal, records, err := openRawMutationJournal(harness.root, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(records) == 0 {
				t.Fatal("fixture WAL has no durable records")
			}
			test.inject(journal)
			record := records[len(records)-1]
			record.Mutation = cloneMutation(mutation)
			if err := journal.append(record); err == nil {
				t.Fatal("fault-injected append unexpectedly succeeded")
			}
			if err := journal.close(); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, before) {
				t.Fatal("failed append changed the prior durable WAL prefix")
			}
			reopened, reopenedRecords, err := openRawMutationJournal(harness.root, nil)
			if err != nil {
				t.Fatalf("reopen preserved prefix: %v", err)
			}
			if len(reopenedRecords) != len(records) {
				t.Fatalf("reopened record count = %d, want %d", len(reopenedRecords), len(records))
			}
			_ = reopened.close()
		})
	}
}

func TestRawMutationWALRepairsOnlyProvableUnterminatedTornTail(t *testing.T) {
	harness := newIssue85Harness(t)
	_, terminal := harness.set()
	issue85AssertNoError(t, terminal)
	harness.closeClean()

	path := filepath.Join(harness.root, "eebusmutation", "mutation-v1.wal")
	prefix, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(append([]byte(nil), prefix...), []byte(`{"contract":`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	repaired, _, err := openRawMutationJournal(harness.root, nil)
	if err != nil {
		t.Fatalf("repair torn final record: %v", err)
	}
	_ = repaired.close()
	afterRepair, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterRepair, prefix) {
		t.Fatal("torn-tail repair changed the prior durable prefix")
	}

	for _, test := range []struct {
		name    string
		payload []byte
	}{
		{name: "valid final record without newline", payload: bytes.TrimSuffix(prefix, []byte{'\n'})},
		{
			name: "concatenated trailing JSON",
			payload: append(
				append(bytes.TrimSuffix(append([]byte(nil), prefix...), []byte{'\n'}), []byte(`{}`)...),
				'\n',
			),
		},
		{
			name: "unterminated concatenated JSON",
			payload: append(
				bytes.TrimSuffix(append([]byte(nil), prefix...), []byte{'\n'}),
				[]byte(`{}`)...,
			),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(path, test.payload, 0o600); err != nil {
				t.Fatal(err)
			}
			if journal, _, err := openRawMutationJournal(harness.root, nil); err == nil {
				_ = journal.close()
				t.Fatal("strict WAL accepted invalid trailing form")
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, test.payload) {
				t.Fatal("non-torn invalid WAL was modified")
			}
		})
	}
}
