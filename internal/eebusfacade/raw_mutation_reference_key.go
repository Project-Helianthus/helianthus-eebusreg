package eebusfacade

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

const (
	rawMutationKeyDirectory = "eebusmutation"
	rawMutationKeyFilename  = "reference-key-v1.json"
	rawMutationKeyLock      = "reference-key-v1.lock"
	rawMutationKeyTemp      = ".reference-key-v1.tmp"
	rawMutationKeyContract  = "helianthus.eebus.raw-mutation-reference-key.v1"
)

type rawMutationReferenceKeyState struct {
	Contract     string `json:"contract"`
	RuntimeEpoch uint64 `json:"runtime_epoch"`
	Key          string `json:"key"`
}

var rawMutationReferenceKeyLocal sync.Mutex

func loadRawMutationReferenceKey(stateRoot string, runtimeEpoch uint64) ([]byte, error) {
	if !validRawConnectionGenerationRoot(stateRoot) || runtimeEpoch == 0 {
		return nil, errors.New("raw mutation reference key input is invalid")
	}
	stateDirectory, err := openRawMutationStateRoot(stateRoot)
	if err != nil {
		return nil, errors.New("raw mutation state root is unavailable")
	}
	defer stateDirectory.Close()
	var rootStat unix.Stat_t
	if err := unix.Fstat(int(stateDirectory.Fd()), &rootStat); err != nil ||
		uint32(rootStat.Mode)&uint32(unix.S_IFMT) != uint32(unix.S_IFDIR) ||
		int(rootStat.Uid) != os.Geteuid() {
		return nil, errors.New("raw mutation state root is unsafe")
	}

	rawMutationReferenceKeyLocal.Lock()
	defer rawMutationReferenceKeyLocal.Unlock()

	mutationDirectory, err := openRawMutationKeyDirectory(stateDirectory)
	if err != nil {
		return nil, err
	}
	defer mutationDirectory.Close()
	lock, err := openRawMutationKeyLock(mutationDirectory)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err := lockRawConnectionGenerationFile(lock); err != nil {
		return nil, errors.New("raw mutation reference key lock is unavailable")
	}
	defer func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) }()

	if err := removeRawMutationKeyTemp(mutationDirectory); err != nil {
		return nil, err
	}
	state, exists, err := readRawMutationReferenceKey(mutationDirectory)
	if err != nil {
		return nil, err
	}
	if exists && state.RuntimeEpoch == runtimeEpoch {
		return decodeRawMutationReferenceKey(state)
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, errors.New("raw mutation reference key entropy is unavailable")
	}
	state = rawMutationReferenceKeyState{
		Contract:     rawMutationKeyContract,
		RuntimeEpoch: runtimeEpoch,
		Key:          base64.RawURLEncoding.EncodeToString(key),
	}
	if err := writeRawMutationReferenceKey(mutationDirectory, state); err != nil {
		clear(key)
		return nil, err
	}
	return key, nil
}

func openRawMutationStateRoot(path string) (*os.File, error) {
	if !validRawConnectionGenerationRoot(path) {
		return nil, errors.New("raw mutation state root path is invalid")
	}
	fd, err := unix.Open(
		path,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if errors.Is(err, unix.ENOENT) {
		parentFD, parentErr := unix.Open(
			filepath.Dir(path),
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
			0,
		)
		if parentErr != nil {
			return nil, parentErr
		}
		parent := os.NewFile(uintptr(parentFD), filepath.Dir(path))
		defer parent.Close()
		if parentErr = unix.Mkdirat(int(parent.Fd()), filepath.Base(path), 0o700); parentErr != nil &&
			!errors.Is(parentErr, unix.EEXIST) {
			return nil, parentErr
		}
		if parentErr = parent.Sync(); parentErr != nil {
			return nil, parentErr
		}
		fd, err = unix.Openat(
			int(parent.Fd()),
			filepath.Base(path),
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
			0,
		)
	}
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	pathStat, err := os.Lstat(path)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	descriptorStat, err := file.Stat()
	if err != nil || pathStat.Mode()&os.ModeSymlink != 0 ||
		!descriptorStat.IsDir() || !os.SameFile(pathStat, descriptorStat) {
		_ = file.Close()
		return nil, errors.New("raw mutation state root changed while opening")
	}
	return file, nil
}

func openRawMutationKeyDirectory(stateRoot *os.File) (*os.File, error) {
	var stat unix.Stat_t
	err := unix.Fstatat(
		int(stateRoot.Fd()),
		rawMutationKeyDirectory,
		&stat,
		unix.AT_SYMLINK_NOFOLLOW,
	)
	if errors.Is(err, unix.ENOENT) {
		if err := unix.Mkdirat(int(stateRoot.Fd()), rawMutationKeyDirectory, 0o700); err != nil &&
			!errors.Is(err, unix.EEXIST) {
			return nil, errors.New("raw mutation state directory cannot be created")
		}
		if err := stateRoot.Sync(); err != nil {
			return nil, errors.New("raw mutation state directory publication is not durable")
		}
		err = unix.Fstatat(
			int(stateRoot.Fd()),
			rawMutationKeyDirectory,
			&stat,
			unix.AT_SYMLINK_NOFOLLOW,
		)
	}
	if err != nil || verifyRawConnectionGenerationDirectoryStat(stat) != nil {
		return nil, errors.New("raw mutation state directory is unsafe")
	}
	fd, err := unix.Openat(
		int(stateRoot.Fd()),
		rawMutationKeyDirectory,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, errors.New("raw mutation state directory is unavailable")
	}
	directory := os.NewFile(uintptr(fd), rawMutationKeyDirectory)
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil ||
		rawConnectionGenerationIdentity(stat) != rawConnectionGenerationIdentity(opened) ||
		verifyRawConnectionGenerationDirectoryStat(opened) != nil {
		_ = directory.Close()
		return nil, errors.New("raw mutation state directory changed while opening")
	}
	return directory, nil
}

func openRawMutationKeyLock(directory *os.File) (*os.File, error) {
	fd, err := unix.Openat(
		int(directory.Fd()),
		rawMutationKeyLock,
		unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0o600,
	)
	if errors.Is(err, unix.EEXIST) {
		file, _, openErr := openRawConnectionGenerationRegular(
			directory,
			rawMutationKeyLock,
			unix.O_RDWR,
		)
		return file, openErr
	}
	if err != nil {
		return nil, errors.New("raw mutation reference key lock cannot be created")
	}
	file := os.NewFile(uintptr(fd), rawMutationKeyLock)
	var stat unix.Stat_t
	if err := unix.Fchmod(fd, 0o600); err != nil ||
		unix.Fstat(fd, &stat) != nil ||
		verifyRawConnectionGenerationRegularStat(stat) != nil ||
		file.Sync() != nil ||
		directory.Sync() != nil {
		_ = file.Close()
		return nil, errors.New("raw mutation reference key lock is not durable")
	}
	return file, nil
}

func readRawMutationReferenceKey(
	directory *os.File,
) (rawMutationReferenceKeyState, bool, error) {
	file, stat, err := openRawConnectionGenerationRegularOptional(
		directory,
		rawMutationKeyFilename,
		unix.O_RDONLY,
	)
	if err != nil {
		return rawMutationReferenceKeyState{}, false, err
	}
	if file == nil {
		return rawMutationReferenceKeyState{}, false, nil
	}
	defer file.Close()
	if stat.Size <= 0 || stat.Size > 4096 {
		return rawMutationReferenceKeyState{}, false, errors.New("raw mutation reference key state is invalid")
	}
	payload, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(payload) != int(stat.Size) {
		return rawMutationReferenceKeyState{}, false, errors.New("raw mutation reference key state cannot be read")
	}
	var state rawMutationReferenceKeyState
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return rawMutationReferenceKeyState{}, false, errors.New("raw mutation reference key state cannot be decoded")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return rawMutationReferenceKeyState{}, false, errors.New("raw mutation reference key state has trailing data")
	}
	if _, err := decodeRawMutationReferenceKey(state); err != nil {
		return rawMutationReferenceKeyState{}, false, err
	}
	return state, true, nil
}

func decodeRawMutationReferenceKey(state rawMutationReferenceKeyState) ([]byte, error) {
	if state.Contract != rawMutationKeyContract || state.RuntimeEpoch == 0 {
		return nil, errors.New("raw mutation reference key state is invalid")
	}
	key, err := base64.RawURLEncoding.DecodeString(state.Key)
	if err != nil || len(key) != 32 {
		return nil, errors.New("raw mutation reference key state is invalid")
	}
	return key, nil
}

func removeRawMutationKeyTemp(directory *os.File) error {
	var stat unix.Stat_t
	err := unix.Fstatat(
		int(directory.Fd()),
		rawMutationKeyTemp,
		&stat,
		unix.AT_SYMLINK_NOFOLLOW,
	)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil || verifyRawConnectionGenerationRegularStat(stat) != nil {
		return errors.New("raw mutation reference key temporary state is unsafe")
	}
	if err := unix.Unlinkat(int(directory.Fd()), rawMutationKeyTemp, 0); err != nil ||
		directory.Sync() != nil {
		return errors.New("raw mutation reference key temporary state cannot be removed")
	}
	return nil
}

func writeRawMutationReferenceKey(
	directory *os.File,
	state rawMutationReferenceKeyState,
) error {
	payload, err := json.Marshal(state)
	if err != nil || len(payload) > 4096 {
		return errors.New("raw mutation reference key state cannot be encoded")
	}
	fd, err := unix.Openat(
		int(directory.Fd()),
		rawMutationKeyTemp,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0o600,
	)
	if err != nil {
		return errors.New("raw mutation reference key temporary state cannot be created")
	}
	temporary := os.NewFile(uintptr(fd), rawMutationKeyTemp)
	published := false
	defer func() {
		if temporary != nil {
			_ = temporary.Close()
		}
		if !published {
			_ = unix.Unlinkat(int(directory.Fd()), rawMutationKeyTemp, 0)
		}
	}()
	var stat unix.Stat_t
	if err := unix.Fchmod(fd, 0o600); err != nil ||
		unix.Fstat(fd, &stat) != nil ||
		verifyRawConnectionGenerationRegularStat(stat) != nil {
		return errors.New("raw mutation reference key temporary state is unsafe")
	}
	for written := 0; written < len(payload); {
		count, writeErr := temporary.Write(payload[written:])
		if writeErr != nil || count <= 0 {
			return errors.New("raw mutation reference key state cannot be persisted")
		}
		written += count
	}
	if temporary.Sync() != nil || temporary.Close() != nil {
		return errors.New("raw mutation reference key state cannot be persisted")
	}
	temporary = nil
	if file, _, err := openRawConnectionGenerationRegularOptional(
		directory,
		rawMutationKeyFilename,
		unix.O_RDONLY,
	); err != nil {
		return err
	} else if file != nil {
		_ = file.Close()
	}
	if err := unix.Renameat(
		int(directory.Fd()),
		rawMutationKeyTemp,
		int(directory.Fd()),
		rawMutationKeyFilename,
	); err != nil {
		return errors.New("raw mutation reference key state cannot be published")
	}
	published = true
	if err := directory.Sync(); err != nil {
		return errors.New("raw mutation reference key state durability is unknown")
	}
	return nil
}
