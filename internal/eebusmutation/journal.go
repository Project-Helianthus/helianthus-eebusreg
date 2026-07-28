package eebusmutation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Project-Helianthus/helianthus-eebusreg/eebusraw"
	"golang.org/x/sys/unix"
)

const (
	rawMutationJournalContract = "helianthus.eebus.raw-mutation-wal.v1"
	rawMutationJournalMaxBytes = 64 * 1024 * 1024
)

type rawMutationJournalRecord struct {
	Contract           string              `json:"contract"`
	Sequence           uint64              `json:"sequence"`
	PreviousRecordHash *eebusraw.HashV1    `json:"previous_record_hash"`
	RecordHash         eebusraw.HashV1     `json:"record_hash"`
	RuntimeEpoch       uint64              `json:"runtime_epoch"`
	PrincipalHash      eebusraw.HashV1     `json:"principal_hash"`
	Tool               eebusraw.ToolV1     `json:"tool"`
	IdentityHash       eebusraw.HashV1     `json:"identity_hash"`
	RequestHash        eebusraw.HashV1     `json:"request_hash"`
	Mutation           eebusraw.MutationV1 `json:"mutation"`
}

type rawMutationJournal struct {
	path        string
	directory   *os.File
	file        *os.File
	persistence rawMutationPersistence
	sequence    uint64
	lastHash    *eebusraw.HashV1
	write       func([]byte) (int, error)
	poisoned    bool
}

type rawMutationNativePersistence struct{}

func (rawMutationNativePersistence) SyncFile(file *os.File) error {
	return file.Sync()
}

func (rawMutationNativePersistence) SyncDirectory(directory *os.File) error {
	return directory.Sync()
}

func openRawMutationJournal(
	root string,
	persistence rawMutationPersistence,
) (*rawMutationJournal, []rawMutationJournalRecord, error) {
	if persistence == nil {
		persistence = rawMutationNativePersistence{}
	}
	stateDirectory := filepath.Join(root, "eebusmutation")
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, nil, errors.New("mutation state root is unsafe")
	}
	createdDirectory := false
	if err := os.Mkdir(stateDirectory, 0o700); err == nil {
		createdDirectory = true
	} else if !errors.Is(err, os.ErrExist) {
		return nil, nil, fmt.Errorf("create mutation journal directory: %w", err)
	}
	stateInfo, err := os.Lstat(stateDirectory)
	if err != nil || !stateInfo.IsDir() || stateInfo.Mode()&os.ModeSymlink != 0 ||
		stateInfo.Mode().Perm() != 0o700 {
		return nil, nil, errors.New("mutation journal directory is unsafe")
	}
	rootDirectory, err := os.Open(root)
	if err != nil {
		return nil, nil, fmt.Errorf("open mutation state root: %w", err)
	}
	if createdDirectory {
		if err := persistence.SyncDirectory(rootDirectory); err != nil {
			_ = rootDirectory.Close()
			return nil, nil, fmt.Errorf("sync mutation state root: %w", err)
		}
	}
	if err := rootDirectory.Close(); err != nil {
		return nil, nil, fmt.Errorf("close mutation state root: %w", err)
	}
	directoryFD, err := unix.Open(
		stateDirectory,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("open mutation journal directory: %w", err)
	}
	directory := os.NewFile(uintptr(directoryFD), stateDirectory)
	path := filepath.Join(stateDirectory, "mutation-v1.wal")
	before, statErr := os.Lstat(path)
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		_ = rootDirectory.Close()
		_ = directory.Close()
		return nil, nil, fmt.Errorf("inspect mutation journal: %w", statErr)
	}
	if statErr == nil &&
		(!before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 ||
			before.Mode().Perm() != 0o600) {
		_ = directory.Close()
		return nil, nil, errors.New("mutation journal is unsafe")
	}
	flags := unix.O_CREAT | unix.O_RDWR | unix.O_APPEND | unix.O_NOFOLLOW | unix.O_CLOEXEC
	fileFD, err := unix.Open(path, flags, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open mutation journal directory: %w", err)
	}
	file := os.NewFile(uintptr(fileFD), path)
	if err := unix.Fchmod(fileFD, 0o600); err != nil {
		_ = file.Close()
		_ = directory.Close()
		return nil, nil, fmt.Errorf("harden mutation journal: %w", err)
	}
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || after.Mode().Perm() != 0o600 ||
		(statErr == nil && !os.SameFile(before, after)) {
		_ = file.Close()
		_ = directory.Close()
		return nil, nil, errors.New("mutation journal changed while opening")
	}
	closeOnError := func(openError error) (*rawMutationJournal, []rawMutationJournalRecord, error) {
		_ = file.Close()
		_ = directory.Close()
		return nil, nil, openError
	}
	if err := persistence.SyncFile(file); err != nil {
		return closeOnError(fmt.Errorf("sync mutation journal: %w", err))
	}
	if err := persistence.SyncDirectory(directory); err != nil {
		return closeOnError(fmt.Errorf("sync mutation journal directory: %w", err))
	}
	records, err := readRawMutationJournal(file, directory, persistence)
	if err != nil {
		return closeOnError(err)
	}
	journal := &rawMutationJournal{
		path:        path,
		directory:   directory,
		file:        file,
		persistence: persistence,
		write:       file.Write,
	}
	if len(records) != 0 {
		last := records[len(records)-1]
		journal.sequence = last.Sequence
		value := last.RecordHash
		journal.lastHash = &value
	}
	return journal, records, nil
}

func (journal *rawMutationJournal) append(record rawMutationJournalRecord) error {
	if journal.poisoned {
		return errors.New("mutation journal is unavailable after failed prefix repair")
	}
	record.Contract = rawMutationJournalContract
	record.Sequence = journal.sequence + 1
	record.PreviousRecordHash = cloneHash(journal.lastHash)
	record.RecordHash = ""
	hash, err := eebusraw.CanonicalSHA256V1(record)
	if err != nil {
		return fmt.Errorf("commit mutation journal record: %w", err)
	}
	record.RecordHash = hash
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode mutation journal record: %w", err)
	}
	encoded = append(encoded, '\n')
	offset, err := journal.file.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("locate mutation journal durable prefix: %w", err)
	}
	var appendErr error
	for written := 0; written < len(encoded); {
		count, writeErr := journal.write(encoded[written:])
		if count > 0 {
			written += count
		}
		if writeErr != nil {
			appendErr = writeErr
			break
		}
		if count == 0 {
			appendErr = io.ErrShortWrite
			break
		}
	}
	if appendErr == nil {
		appendErr = journal.persistence.SyncFile(journal.file)
	}
	if appendErr != nil {
		if err := journal.restorePrefix(offset); err != nil {
			journal.poisoned = true
			return errors.Join(
				fmt.Errorf("append mutation journal record: %w", appendErr),
				fmt.Errorf("restore mutation journal durable prefix: %w", err),
			)
		}
		return fmt.Errorf("append mutation journal record: %w", appendErr)
	}
	journal.sequence = record.Sequence
	value := record.RecordHash
	journal.lastHash = &value
	return nil
}

func (journal *rawMutationJournal) restorePrefix(offset int64) error {
	if err := journal.file.Truncate(offset); err != nil {
		return err
	}
	if _, err := journal.file.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	return journal.persistence.SyncFile(journal.file)
}

func (journal *rawMutationJournal) close() error {
	if journal == nil {
		return nil
	}
	return errors.Join(journal.file.Close(), journal.directory.Close())
}

func readRawMutationJournal(
	file *os.File,
	directory *os.File,
	persistence rawMutationPersistence,
) ([]rawMutationJournalRecord, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek mutation journal: %w", err)
	}
	info, err := file.Stat()
	if err != nil || info.Size() < 0 || info.Size() > rawMutationJournalMaxBytes {
		return nil, errors.New("mutation journal size is invalid")
	}
	payload, err := io.ReadAll(io.LimitReader(file, rawMutationJournalMaxBytes+1))
	if err != nil || int64(len(payload)) != info.Size() {
		return nil, errors.New("mutation journal cannot be read")
	}
	if len(payload) != 0 && payload[len(payload)-1] != '\n' {
		finalStart := bytes.LastIndexByte(payload, '\n') + 1
		final := payload[finalStart:]
		var partial any
		decoder := json.NewDecoder(bytes.NewReader(final))
		if err := decoder.Decode(&partial); !errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, errors.New("mutation journal has non-torn trailing data")
		}
		if err := file.Truncate(int64(finalStart)); err != nil {
			return nil, fmt.Errorf("repair torn mutation journal tail: %w", err)
		}
		if _, err := file.Seek(0, io.SeekEnd); err != nil {
			return nil, fmt.Errorf("seek repaired mutation journal: %w", err)
		}
		if err := persistence.SyncFile(file); err != nil {
			return nil, fmt.Errorf("sync repaired mutation journal: %w", err)
		}
		if err := persistence.SyncDirectory(directory); err != nil {
			return nil, fmt.Errorf("sync repaired mutation journal directory: %w", err)
		}
		payload = payload[:finalStart]
	}
	var records []rawMutationJournalRecord
	var previous *eebusraw.HashV1
	var sequence uint64
	if len(payload) != 0 {
		payload = payload[:len(payload)-1]
	}
	for _, source := range bytes.Split(payload, []byte{'\n'}) {
		if len(payload) == 0 {
			break
		}
		line := append([]byte(nil), source...)
		if len(bytes.TrimSpace(line)) == 0 {
			return nil, errors.New("mutation journal contains an empty record")
		}
		var record rawMutationJournalRecord
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&record); err != nil {
			return nil, fmt.Errorf("decode mutation journal record: %w", err)
		}
		if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
			return nil, errors.New("mutation journal record has trailing data")
		}
		if record.Contract != rawMutationJournalContract ||
			record.Sequence != sequence+1 ||
			!hashPointersEqual(record.PreviousRecordHash, previous) ||
			record.RuntimeEpoch == 0 ||
			record.PrincipalHash == "" ||
			record.IdentityHash == "" ||
			record.RequestHash == "" ||
			record.Mutation.MutationRef == "" {
			return nil, errors.New("mutation journal record binding is invalid")
		}
		wantHash := record.RecordHash
		record.RecordHash = ""
		computed, err := eebusraw.CanonicalSHA256V1(record)
		if err != nil || computed != wantHash {
			return nil, errors.New("mutation journal record commitment is invalid")
		}
		record.RecordHash = wantHash
		if err := validateRawMutationAudit(record.Mutation.Audit); err != nil {
			return nil, err
		}
		if len(record.Mutation.Audit) == 0 ||
			record.Mutation.Audit[len(record.Mutation.Audit)-1].State != record.Mutation.State {
			return nil, errors.New("mutation journal state is not audit-linked")
		}
		records = append(records, record)
		sequence = record.Sequence
		value := record.RecordHash
		previous = &value
	}
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		return nil, fmt.Errorf("restore mutation journal append position: %w", err)
	}
	return records, nil
}

func validateRawMutationAudit(audit []eebusraw.AuditTransitionV1) error {
	var previous *eebusraw.HashV1
	for index, transition := range audit {
		if transition.Sequence != uint64(index+1) ||
			transition.State == "" ||
			transition.TransitionedAt.IsZero() ||
			!hashPointersEqual(transition.PreviousHash, previous) {
			return errors.New("mutation audit chain is invalid")
		}
		computed, err := rawMutationAuditHash(
			transition.Sequence,
			transition.State,
			transition.TransitionedAt,
			transition.Classification,
			transition.PreviousHash,
		)
		if err != nil || computed != transition.TransitionHash {
			return errors.New("mutation audit commitment is invalid")
		}
		value := transition.TransitionHash
		previous = &value
	}
	return nil
}

func rawMutationAuditHash(
	sequence uint64,
	state eebusraw.MutationStateV1,
	transitionedAt time.Time,
	classification string,
	previous *eebusraw.HashV1,
) (eebusraw.HashV1, error) {
	return eebusraw.CanonicalSHA256V1(struct {
		Sequence       uint64                   `json:"sequence"`
		State          eebusraw.MutationStateV1 `json:"state"`
		TransitionedAt time.Time                `json:"transitioned_at"`
		Classification string                   `json:"classification,omitempty"`
		PreviousHash   *eebusraw.HashV1         `json:"previous_hash"`
	}{
		Sequence:       sequence,
		State:          state,
		TransitionedAt: transitionedAt,
		Classification: classification,
		PreviousHash:   previous,
	})
}

func cloneHash(value *eebusraw.HashV1) *eebusraw.HashV1 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func hashPointersEqual(left, right *eebusraw.HashV1) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
