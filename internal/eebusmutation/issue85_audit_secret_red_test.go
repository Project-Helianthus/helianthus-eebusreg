package eebusmutation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
)

func TestIssue85IndependentJCSGoldenDigests(t *testing.T) {
	tests := []struct {
		name      string
		value     any
		canonical string
		want      eebusraw.HashV1
	}{
		{
			name: "lexicographic object order",
			value: map[string]any{
				"b": "x",
				"a": int64(1),
			},
			canonical: `{"a":1,"b":"x"}`,
			want:      "sha256:ecf9e98ec0641e23113ff3ce8bdc78d0ddd249886517fd4a7f68cc83d4e65667",
		},
		{
			name: "closed request projection",
			value: map[string]any{
				"value": map[string]any{
					"unit":  "degC",
					"limit": int64(20),
				},
				"target": map[string]any{
					"operation":       "WRITE",
					"feature_address": int64(11),
				},
				"mode":            "apply",
				"idempotency_key": "k",
			},
			canonical: `{"idempotency_key":"k","mode":"apply","target":{"feature_address":11,"operation":"WRITE"},"value":{"limit":20,"unit":"degC"}}`,
			want:      "sha256:86f45ad1bbbe18d7b8dba0cb5619cefc17f6f60ff85282285c10d1aea0909437",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			independent := sha256.Sum256([]byte(test.canonical))
			if got := eebusraw.HashV1(fmt.Sprintf("sha256:%x", independent)); got != test.want {
				t.Fatalf("test golden does not match independent canonical bytes: got %q want %q", got, test.want)
			}
			got, err := eebusraw.CanonicalSHA256V1(test.value)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("independent JCS digest = %q, want golden %q", got, test.want)
			}
		})
	}
}

func TestIssue85AuditChainIsLinkedDeterministicAndRestartContinuous(t *testing.T) {
	firstHarness := newIssue85Harness(t)
	first, terminal := firstHarness.set()
	issue85AssertNoError(t, terminal)
	issue85AssertAuditChain(t, first.Audit)

	secondHarness := newIssue85Harness(t)
	second, terminal := secondHarness.set()
	issue85AssertNoError(t, terminal)
	if !reflect.DeepEqual(first.Audit, second.Audit) {
		t.Fatalf("same canonical mutation produced different audit chains:\nfirst=%+v\nsecond=%+v", first.Audit, second.Audit)
	}

	firstHarness.closeClean()
	events := firstHarness.events
	recoveryExecutor := &issue85Executor{t: t, events: events}
	template := issue85HarnessDraft(t)
	recoveryExecutor.setSteps(
		[]issue85ReadStep{
			template.readStep(template.requested),
			template.readStep(template.before),
		},
		[]issue85WriteStep{
			template.writeStep(template.before, rawMutationWriteResult{
				FrameSent:  true,
				Correlated: true,
				Accepted:   true,
			}),
		},
	)
	recoveryScheduler := newIssue85Scheduler(firstHarness.clock, events)
	restarted := newIssue85Harness(
		t,
		issue85WithRoot(firstHarness.root),
		issue85WithExecutor(recoveryExecutor),
		issue85WithTokenVerifier(firstHarness.tokens),
		issue85WithPolicy(firstHarness.policy),
		issue85WithClock(firstHarness.clock),
		issue85WithScheduler(recoveryScheduler),
		issue85WithEvents(events),
	)
	beforeRollback, terminal := restarted.status(first.MutationRef)
	issue85AssertNoError(t, terminal)
	if !reflect.DeepEqual(beforeRollback.Audit, first.Audit) {
		t.Fatalf("restart changed durable audit prefix:\nbefore=%+v\nafter=%+v", first.Audit, beforeRollback.Audit)
	}
	rolledBack, terminal := restarted.rollback(first.MutationRef, "issue85-audit-rollback")
	issue85AssertNoError(t, terminal)
	issue85AssertAuditChain(t, rolledBack.Audit)
	if len(rolledBack.Audit) <= len(first.Audit) ||
		!reflect.DeepEqual(rolledBack.Audit[:len(first.Audit)], first.Audit) {
		t.Fatalf("restart rollback did not append to the original chain: %+v", rolledBack.Audit)
	}
}

func TestIssue85TamperedValidHashHexNibbleFailsJCSCommitmentRecomputation(t *testing.T) {
	harness := newIssue85Harness(t)
	mutation, terminal := harness.set()
	issue85AssertNoError(t, terminal)
	harness.closeClean()
	if len(mutation.Audit) == 0 {
		t.Fatal("mutation has no audit transition to tamper")
	}
	issue85TamperValidHashHexNibble(
		t,
		harness.root,
		mutation.Audit[len(mutation.Audit)-1].TransitionHash,
	)

	events := &issue85EventLog{}
	executor := &issue85Executor{t: t, events: events}
	executor.setSteps(nil, nil)
	scheduler := newIssue85Scheduler(harness.clock, events)
	draft := issue85HarnessDraft(t)
	issue85WithRoot(harness.root)(draft)
	issue85WithExecutor(executor)(draft)
	issue85WithClock(harness.clock)(draft)
	issue85WithScheduler(scheduler)(draft)
	issue85WithEvents(events)(draft)
	coordinator, openError := draft.tryOpen()
	if coordinator != nil {
		_ = coordinator.Close()
		t.Fatal("tampered journal returned a live coordinator")
	}
	issue85AssertError(t, openError, eebusraw.ErrorCodeV1Internal)
	reads, writes, _, exhausted := executor.counts()
	if reads != 0 || writes != 0 || exhausted != 0 {
		t.Fatalf("tamper recovery contacted remote: reads:%d writes:%d exhausted:%d", reads, writes, exhausted)
	}
}

func TestIssue85ReferenceTokenAndIdempotencyCanariesNeverReachJournalAuditOrErrors(t *testing.T) {
	const (
		idempotencyCanary = "IDEMPOTENCY-CANARY-85"
		backendCanary     = "BACKEND-ERROR-CANARY-85"
	)
	readTokenCanary := "READ-TOKEN-CANARY-85" + strings.Repeat("A", 23)
	referenceKeyCanary := []byte("REFERENCE-KEY-CANARY-85-0123456789ABCDEF")
	harness := newIssue85Harness(t, issue85WithReferenceKey(referenceKeyCanary))
	binding := harness.tokens.bindings[harness.request.ReadToken]
	delete(harness.tokens.bindings, harness.request.ReadToken)
	harness.tokens.bindings[readTokenCanary] = binding
	harness.request.ReadToken = readTokenCanary
	harness.request.IdempotencyKey = idempotencyCanary

	write := harness.writeStep(harness.requested, rawMutationWriteResult{
		FrameSent:  true,
		Correlated: false,
	})
	write.terminal = issue85ErrorWith(
		eebusraw.ErrorCodeV1Timeout,
		backendCanary+" bearer credential",
		true,
	)
	harness.executor.setSteps(
		[]issue85ReadStep{
			harness.readStep(harness.before),
			harness.readStep(harness.before),
		},
		[]issue85WriteStep{write},
	)

	mutation, terminal := harness.set()
	issue85AssertError(t, terminal, eebusraw.ErrorCodeV1NoEffect)
	harness.closeClean()
	canaries := []string{
		string(referenceKeyCanary),
		readTokenCanary,
		idempotencyCanary,
		backendCanary,
	}
	issue85AssertTreeOmits(t, harness.root, canaries...)
	for name, value := range map[string]any{
		"mutation json": mutation,
		"audit json":    mutation.Audit,
		"terminal json": terminal,
	} {
		payload, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		issue85AssertBytesOmit(t, name, payload, canaries...)
	}
	issue85AssertBytesOmit(
		t,
		"formatted mutation/error",
		[]byte(fmt.Sprintf("%+v %+v", mutation, terminal)),
		canaries...,
	)
}

func TestIssue85BackendErrorCanaryIsSanitizedBeforeDurableFailure(t *testing.T) {
	const canary = "REMOTE-SECRET-CANARY-85"
	harness := newIssue85Harness(t)
	read := harness.readStep(harness.before)
	read.terminal = issue85ErrorWith(
		eebusraw.ErrorCodeV1Disconnected,
		canary+" should never escape",
		true,
	)
	harness.executor.setSteps([]issue85ReadStep{read}, nil)

	mutation, terminal := harness.set()
	issue85AssertError(t, terminal, eebusraw.ErrorCodeV1Disconnected)
	harness.closeClean()
	payload, err := json.Marshal(struct {
		Mutation eebusraw.MutationV1 `json:"mutation"`
		Error    *eebusraw.ErrorV1   `json:"error"`
	}{
		Mutation: mutation,
		Error:    terminal,
	})
	if err != nil {
		t.Fatal(err)
	}
	issue85AssertBytesOmit(t, "failure payload", payload, canary)
	issue85AssertTreeOmits(t, harness.root, canary)
}

func TestIssue85RecursiveSecretCanariesFailBeforeHashOrReferenceCreation(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{
			name: "camel case private key",
			value: map[string]any{
				"outer": map[string]any{"privateKey": "SECRET-CANARY-85"},
			},
		},
		{
			name: "nfkc equivalent private key",
			value: map[string]any{
				"outer": map[string]any{
					"\uFF50\uFF52\uFF49\uFF56\uFF41\uFF54\uFF45\uFF2B\uFF45\uFF59": "SECRET-CANARY-85",
				},
			},
		},
		{
			name: "bearer scalar",
			value: map[string]any{
				"outer": []any{"Bearer SECRET-CANARY-85"},
			},
		},
		{
			name: "pem private key scalar",
			value: map[string]any{
				"outer": "-----BEGIN PRIVATE KEY-----\nSECRET-CANARY-85",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := eebusraw.NewTypedValueV1(test.value)
			if !errors.Is(err, eebusraw.ErrSecretDetected) {
				t.Fatalf("secret input error = %v, want ErrSecretDetected", err)
			}
			if err != nil && strings.Contains(err.Error(), "SECRET-CANARY-85") {
				t.Fatalf("secret classifier echoed the canary: %v", err)
			}
		})
	}
}

func issue85AssertAuditChain(t testing.TB, audit []eebusraw.AuditTransitionV1) {
	t.Helper()
	if len(audit) < 2 {
		t.Fatalf("audit transitions = %d, want a linked chain", len(audit))
	}
	seen := make(map[eebusraw.HashV1]struct{}, len(audit))
	for index, transition := range audit {
		wantSequence := uint64(index + 1)
		if transition.Sequence != wantSequence ||
			transition.State == "" ||
			transition.TransitionedAt.IsZero() ||
			transition.TransitionHash == "" {
			t.Fatalf("audit transition %d is incomplete: %+v", index, transition)
		}
		if _, duplicate := seen[transition.TransitionHash]; duplicate {
			t.Fatalf("audit transition hash repeated at %d: %q", index, transition.TransitionHash)
		}
		seen[transition.TransitionHash] = struct{}{}
		if index == 0 {
			if transition.PreviousHash != nil {
				t.Fatalf("first audit previous hash = %v, want nil", transition.PreviousHash)
			}
			continue
		}
		if transition.PreviousHash == nil ||
			*transition.PreviousHash != audit[index-1].TransitionHash {
			t.Fatalf("audit transition %d previous hash = %v, want %q", index, transition.PreviousHash, audit[index-1].TransitionHash)
		}
	}
}

func issue85TamperValidHashHexNibble(t testing.TB, root string, hash eebusraw.HashV1) {
	t.Helper()
	const prefix = "sha256:"
	text := string(hash)
	if !strings.HasPrefix(text, prefix) || len(text) != len(prefix)+sha256.Size*2 {
		t.Fatalf("test hash %q is not a valid SHA-256 commitment", hash)
	}
	needle := []byte(text)
	var matched string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !entry.Type().IsRegular() || matched != "" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		offset := bytes.Index(data, needle)
		if offset < 0 {
			return nil
		}
		nibble := offset + len(prefix)
		replacement := byte('0')
		if data[nibble] == replacement {
			replacement = '1'
		}
		data[nibble] = replacement
		mutated := data[offset : offset+len(needle)]
		if !bytes.HasPrefix(mutated, []byte(prefix)) {
			return errors.New("tamper changed the hash syntax instead of one hex nibble")
		}
		if _, decodeErr := hex.DecodeString(string(mutated[len(prefix):])); decodeErr != nil {
			return fmt.Errorf("tampered commitment is no longer valid hash syntax: %w", decodeErr)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, data, info.Mode().Perm()); err != nil {
			return err
		}
		file, err := os.OpenFile(path, os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		matched = path
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if matched == "" {
		t.Fatalf("no journal file under %s contained audit hash %q", root, hash)
	}
}

func issue85AssertTreeOmits(t testing.TB, root string, canaries ...string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		issue85AssertBytesOmit(t, path, data, canaries...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func issue85AssertBytesOmit(t testing.TB, label string, data []byte, canaries ...string) {
	t.Helper()
	for _, canary := range canaries {
		if bytes.Contains(data, []byte(canary)) {
			t.Fatalf("%s contains secret canary %q", label, canary)
		}
	}
}
