package eebusmutation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

func TestIssue87CoordinatorDurableNegativeRepliesValidate(t *testing.T) {
	t.Run("original reply observed", func(t *testing.T) {
		harness := newIssue85Harness(
			t,
			issue85WithCrash(eebusraw.MutationStateV1ReplyObserved),
		)
		harness.executor.setSteps(
			[]issue85ReadStep{harness.readStep(harness.before)},
			[]issue85WriteStep{
				harness.writeStep(harness.requested, rawMutationWriteResult{
					FrameSent: true, Correlated: true, Accepted: false,
				}),
			},
		)

		mutation, terminal := harness.set()
		issue85AssertError(t, terminal, eebusraw.ErrorCodeV1Internal)
		issue85AssertState(t, mutation, eebusraw.MutationStateV1ReplyObserved)
		if mutation.ProtocolAccepted == nil || *mutation.ProtocolAccepted {
			t.Fatalf("durable negative reply lost acceptance evidence: %+v", mutation)
		}
		issue87AssertCanonicalMutation(t, mutation)
	})

	t.Run("rollback reply observed", func(t *testing.T) {
		harness := newIssue85Harness(
			t,
			issue85WithCrash(eebusraw.MutationStateV1RollbackReplyObserved),
		)
		applied, terminal := harness.set()
		issue85AssertNoError(t, terminal)
		harness.executor.setSteps(
			[]issue85ReadStep{harness.readStep(harness.requested)},
			[]issue85WriteStep{
				harness.writeStep(harness.before, rawMutationWriteResult{
					FrameSent: true, Correlated: true, Accepted: false,
				}),
			},
		)

		mutation, terminal := harness.rollback(
			applied.MutationRef,
			"issue87-negative-rollback",
		)
		issue85AssertError(t, terminal, eebusraw.ErrorCodeV1Internal)
		issue85AssertState(
			t,
			mutation,
			eebusraw.MutationStateV1RollbackReplyObserved,
		)
		if mutation.Rollback == nil ||
			mutation.Rollback.ProtocolAccepted == nil ||
			*mutation.Rollback.ProtocolAccepted {
			t.Fatalf("durable negative rollback reply lost evidence: %+v", mutation)
		}
		issue87AssertCanonicalMutation(t, mutation)
	})
}

func TestIssue87RestoreRejectsIncompatibleOrImpossibleWAL(t *testing.T) {
	tests := map[string]func(*testing.T, *issue85Harness) eebusraw.MutationV1{
		"precontract 64 hex reference": func(
			t *testing.T,
			harness *issue85Harness,
		) eebusraw.MutationV1 {
			return issue87PreparedWALMutation(
				t,
				harness,
				"mutation:v1:"+strings.Repeat("a", 64),
			)
		},
		"semantically impossible applied state": func(
			t *testing.T,
			harness *issue85Harness,
		) eebusraw.MutationV1 {
			mutation := issue87PreparedWALMutation(
				t,
				harness,
				issue85OpaqueReference("issue87-impossible-wal"),
			)
			mutation.State = eebusraw.MutationStateV1Applied
			mutation.Audit[0].State = eebusraw.MutationStateV1Applied
			mutation.Audit[0].TransitionHash, _ = rawMutationAuditHash(
				1,
				eebusraw.MutationStateV1Applied,
				mutation.Audit[0].TransitionedAt,
				"",
				nil,
			)
			return mutation
		},
	}

	for name, build := range tests {
		t.Run(name, func(t *testing.T) {
			harness := issue85HarnessDraft(t)
			mutation := build(t, harness)
			issue87AppendWALMutation(t, harness, mutation)
			path := filepath.Join(
				harness.root,
				"eebusmutation",
				"mutation-v1.wal",
			)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			coordinator, terminal := newRawMutationCoordinator(
				harness.config,
				harness.deps,
			)
			if coordinator != nil {
				_ = coordinator.Close()
				t.Fatal("invalid WAL restored into an addressable coordinator")
			}
			issue85AssertError(t, terminal, eebusraw.ErrorCodeV1Internal)

			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatal("invalid WAL was silently migrated or rewritten")
			}
		})
	}
}

func issue87PreparedWALMutation(
	t *testing.T,
	harness *issue85Harness,
	ref string,
) eebusraw.MutationV1 {
	t.Helper()
	at := harness.clock.Now()
	hash, err := rawMutationAuditHash(
		1,
		eebusraw.MutationStateV1Prepared,
		at,
		"",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	return eebusraw.MutationV1{
		MutationRef: ref,
		State:       eebusraw.MutationStateV1Prepared,
		Mode:        eebusraw.ModeV1Apply,
		Target:      harness.target.Clone(),
		Runtime: eebusraw.RuntimeBindingV1{
			RuntimeEpoch:         harness.epoch,
			ConnectionGeneration: harness.generation,
		},
		Before:    harness.before.Clone(),
		Requested: harness.requested.Clone(),
		CreatedAt: at,
		UpdatedAt: at,
		Audit: []eebusraw.AuditTransitionV1{{
			Sequence:       1,
			State:          eebusraw.MutationStateV1Prepared,
			TransitionedAt: at,
			TransitionHash: hash,
		}},
	}
}

func issue87AppendWALMutation(
	t *testing.T,
	harness *issue85Harness,
	mutation eebusraw.MutationV1,
) {
	t.Helper()
	journal, records, err := openRawMutationJournal(harness.root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		_ = journal.close()
		t.Fatal("new mutation journal was not empty")
	}
	record := rawMutationJournalRecord{
		RuntimeEpoch: harness.epoch,
		PrincipalHash: issue87CanonicalHash(t, struct {
			Principal string `json:"principal"`
		}{Principal: "owner"}),
		Tool: eebusraw.ToolV1FeaturesDataSet,
		IdentityHash: issue87CanonicalHash(t, struct {
			Identity string `json:"identity"`
		}{Identity: "issue87"}),
		RequestHash: issue87CanonicalHash(t, harness.request),
		Mutation:    mutation,
	}
	if err := journal.append(record); err != nil {
		_ = journal.close()
		t.Fatal(err)
	}
	if err := journal.close(); err != nil {
		t.Fatal(err)
	}
}

func issue87CanonicalHash(t *testing.T, value any) eebusraw.HashV1 {
	t.Helper()
	hash, err := eebusraw.CanonicalSHA256V1(value)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}
