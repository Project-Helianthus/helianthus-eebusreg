//go:build linux || darwin

package eebusfacade

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const (
	rawConnectionGenerationMaximumStateBytes = 1 << 20
	rawConnectionGenerationMaximumEntries    = 4096
)

type rawConnectionGenerationFileStore struct {
	stateRoot string
	root      string
}

type rawConnectionGenerationFileIdentity struct {
	device uint64
	inode  uint64
}

type rawConnectionGenerationOpenedRoot struct {
	parent   *os.File
	name     string
	root     *os.File
	identity rawConnectionGenerationFileIdentity
}

var rawConnectionGenerationLocalLocks = struct {
	sync.Mutex
	locks map[rawConnectionGenerationFileIdentity]*sync.Mutex
}{
	locks: make(map[rawConnectionGenerationFileIdentity]*sync.Mutex),
}

func newRawConnectionGenerationStore(stateRoot string) (rawConnectionGenerationStore, error) {
	stateRoot = strings.TrimSpace(stateRoot)
	if !validRawConnectionGenerationRoot(stateRoot) {
		return nil, errors.New("raw connection generation state root is invalid")
	}
	return &rawConnectionGenerationFileStore{
		stateRoot: stateRoot,
		root:      rawConnectionGenerationStoreRoot(stateRoot),
	}, nil
}

func (store *rawConnectionGenerationFileStore) advance(
	runtimeEpoch uint64,
	ski string,
	generation uint64,
) error {
	if generation == 0 {
		return errors.New("raw connection generation persistence input is invalid")
	}
	_, err := store.update(runtimeEpoch, ski, func(current uint64) (uint64, error) {
		if generation <= current {
			return 0, errors.New("raw connection generation is not monotonic")
		}
		return generation, nil
	})
	return err
}

func (store *rawConnectionGenerationFileStore) allocateNext(
	runtimeEpoch uint64,
	ski string,
) (uint64, error) {
	return store.update(runtimeEpoch, ski, func(current uint64) (uint64, error) {
		if current == ^uint64(0) {
			return 0, errors.New("raw connection generation is exhausted")
		}
		return current + 1, nil
	})
}

func (store *rawConnectionGenerationFileStore) update(
	runtimeEpoch uint64,
	ski string,
	next func(uint64) (uint64, error),
) (uint64, error) {
	if store == nil || runtimeEpoch == 0 || next == nil || !validRuntimeSKI(ski) {
		return 0, errors.New("raw connection generation persistence input is invalid")
	}
	ski = strings.ToLower(strings.TrimSpace(ski))
	if err := verifyRawConnectionGenerationStateRootReference(store.stateRoot); err != nil {
		return 0, err
	}
	opened, err := openRawConnectionGenerationRoot(store.root)
	if err != nil {
		return 0, err
	}
	defer opened.close()

	localLock := rawConnectionGenerationLocalLock(opened.identity)
	localLock.Lock()
	defer localLock.Unlock()

	lock, err := openRawConnectionGenerationLock(opened.root)
	if err != nil {
		return 0, err
	}
	defer func() { _ = lock.Close() }()
	if err := lockRawConnectionGenerationFile(lock); err != nil {
		return 0, err
	}
	defer func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) }()
	if err := revalidateRawConnectionGenerationRoot(opened); err != nil {
		return 0, err
	}
	if err := revalidateRawConnectionGenerationFile(
		opened.root,
		rawConnectionGenerationLockFilename,
		lock,
	); err != nil {
		return 0, err
	}
	if err := opened.root.Sync(); err != nil {
		return 0, errors.New("raw connection generation lock durability is unknown")
	}
	if err := removeStaleRawConnectionGenerationTemp(opened.root); err != nil {
		return 0, err
	}

	state, err := readRawConnectionGenerationState(opened.root, runtimeEpoch)
	if err != nil {
		return 0, err
	}
	generation, err := next(state.Generations[ski])
	if err != nil || generation == 0 {
		if err == nil {
			err = errors.New("raw connection generation allocation is invalid")
		}
		return 0, err
	}
	updated := make(map[string]uint64, len(state.Generations)+1)
	for remoteSKI, current := range state.Generations {
		updated[remoteSKI] = current
	}
	updated[ski] = generation
	state.Generations = updated
	if err := writeRawConnectionGenerationState(opened, lock, state); err != nil {
		return 0, err
	}
	return generation, nil
}

func rawConnectionGenerationStoreRoot(stateRoot string) string {
	parent := filepath.Dir(stateRoot)
	name := filepath.Base(stateRoot)
	return filepath.Join(parent, "."+name+"-raw-connection-generations")
}

func validRawConnectionGenerationRoot(path string) bool {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path ||
		strings.ContainsRune(path, 0) || filepath.Base(path) == "." ||
		filepath.Base(path) == ".." || path == string(filepath.Separator) {
		return false
	}
	return filepath.Separator != '/' || !strings.ContainsRune(path, '\\')
}

func verifyRawConnectionGenerationStateRootReference(path string) error {
	parent, err := openRawConnectionGenerationDirectory(filepath.Dir(path))
	if err != nil {
		return errors.New("raw connection generation state parent is unavailable")
	}
	defer func() { _ = parent.Close() }()
	var stat unix.Stat_t
	err = unix.Fstatat(
		int(parent.Fd()),
		filepath.Base(path),
		&stat,
		unix.AT_SYMLINK_NOFOLLOW,
	)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return errors.New("raw connection generation state root is unavailable")
	}
	return verifyRawConnectionGenerationDirectoryStat(stat)
}

func openRawConnectionGenerationRoot(path string) (rawConnectionGenerationOpenedRoot, error) {
	var opened rawConnectionGenerationOpenedRoot
	parent, err := openRawConnectionGenerationDirectory(filepath.Dir(path))
	if err != nil {
		return opened, errors.New("raw connection generation state parent is unavailable")
	}
	opened.parent = parent
	opened.name = filepath.Base(path)
	cleanup := true
	defer func() {
		if cleanup {
			opened.close()
		}
	}()

	var pathStat unix.Stat_t
	err = unix.Fstatat(
		int(parent.Fd()),
		opened.name,
		&pathStat,
		unix.AT_SYMLINK_NOFOLLOW,
	)
	if errors.Is(err, unix.ENOENT) {
		err = unix.Mkdirat(int(parent.Fd()), opened.name, 0o700)
		if err != nil && !errors.Is(err, unix.EEXIST) {
			return rawConnectionGenerationOpenedRoot{}, errors.New("raw connection generation state root cannot be created")
		}
		err = unix.Fstatat(
			int(parent.Fd()),
			opened.name,
			&pathStat,
			unix.AT_SYMLINK_NOFOLLOW,
		)
	}
	if err != nil {
		return rawConnectionGenerationOpenedRoot{}, errors.New("raw connection generation state root is unavailable")
	}
	if err := verifyRawConnectionGenerationDirectoryStat(pathStat); err != nil {
		return rawConnectionGenerationOpenedRoot{}, err
	}
	fd, err := unix.Openat(
		int(parent.Fd()),
		opened.name,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return rawConnectionGenerationOpenedRoot{}, errors.New("raw connection generation state root is unavailable")
	}
	opened.root = os.NewFile(uintptr(fd), rawConnectionGenerationStateFilename)
	var descriptorStat unix.Stat_t
	if err := unix.Fstat(fd, &descriptorStat); err != nil {
		return rawConnectionGenerationOpenedRoot{}, errors.New("raw connection generation state root cannot be inspected")
	}
	if rawConnectionGenerationIdentity(pathStat) != rawConnectionGenerationIdentity(descriptorStat) {
		return rawConnectionGenerationOpenedRoot{}, errors.New("raw connection generation state root changed while opening")
	}
	if err := verifyRawConnectionGenerationDirectoryStat(descriptorStat); err != nil {
		return rawConnectionGenerationOpenedRoot{}, err
	}
	if err := parent.Sync(); err != nil {
		return rawConnectionGenerationOpenedRoot{}, errors.New("raw connection generation state root publication is not durable")
	}
	opened.identity = rawConnectionGenerationIdentity(descriptorStat)
	cleanup = false
	return opened, nil
}

func openRawConnectionGenerationDirectory(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("raw connection generation directory path is invalid")
	}
	fd, err := unix.Open(
		string(filepath.Separator),
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(fd), string(filepath.Separator))
	components := strings.Split(
		strings.TrimPrefix(path, string(filepath.Separator)),
		string(filepath.Separator),
	)
	for _, component := range components {
		if component == "" {
			continue
		}
		nextFD, err := unix.Openat(
			int(current.Fd()),
			component,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC,
			0,
		)
		if err != nil {
			_ = current.Close()
			return nil, err
		}
		next := os.NewFile(uintptr(nextFD), component)
		_ = current.Close()
		current = next
	}
	return current, nil
}

func (opened rawConnectionGenerationOpenedRoot) close() {
	if opened.root != nil {
		_ = opened.root.Close()
	}
	if opened.parent != nil {
		_ = opened.parent.Close()
	}
}

func rawConnectionGenerationLocalLock(identity rawConnectionGenerationFileIdentity) *sync.Mutex {
	rawConnectionGenerationLocalLocks.Lock()
	defer rawConnectionGenerationLocalLocks.Unlock()
	lock := rawConnectionGenerationLocalLocks.locks[identity]
	if lock == nil {
		lock = &sync.Mutex{}
		rawConnectionGenerationLocalLocks.locks[identity] = lock
	}
	return lock
}

func openRawConnectionGenerationLock(root *os.File) (*os.File, error) {
	fd, err := unix.Openat(
		int(root.Fd()),
		rawConnectionGenerationLockFilename,
		unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0o600,
	)
	if errors.Is(err, unix.EEXIST) {
		file, _, openErr := openRawConnectionGenerationRegular(
			root,
			rawConnectionGenerationLockFilename,
			unix.O_RDWR,
		)
		return file, openErr
	}
	if err != nil {
		return nil, errors.New("raw connection generation lock cannot be created")
	}
	file := os.NewFile(uintptr(fd), rawConnectionGenerationLockFilename)
	if err := unix.Fchmod(fd, 0o600); err != nil {
		_ = file.Close()
		return nil, errors.New("raw connection generation lock cannot be protected")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil ||
		verifyRawConnectionGenerationRegularStat(stat) != nil {
		_ = file.Close()
		return nil, errors.New("raw connection generation lock is unsafe")
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, errors.New("raw connection generation lock durability is unknown")
	}
	if err := root.Sync(); err != nil {
		_ = file.Close()
		return nil, errors.New("raw connection generation lock publication is not durable")
	}
	return file, nil
}

func lockRawConnectionGenerationFile(lock *os.File) error {
	for {
		err := unix.Flock(int(lock.Fd()), unix.LOCK_EX)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return errors.New("raw connection generation process lock is unavailable")
		}
		return nil
	}
}

func revalidateRawConnectionGenerationRoot(opened rawConnectionGenerationOpenedRoot) error {
	var pathStat unix.Stat_t
	if err := unix.Fstatat(
		int(opened.parent.Fd()),
		opened.name,
		&pathStat,
		unix.AT_SYMLINK_NOFOLLOW,
	); err != nil {
		return errors.New("raw connection generation state root cannot be revalidated")
	}
	if rawConnectionGenerationIdentity(pathStat) != opened.identity {
		return errors.New("raw connection generation state root identity changed")
	}
	return verifyRawConnectionGenerationDirectoryStat(pathStat)
}

func revalidateRawConnectionGenerationFile(root *os.File, name string, file *os.File) error {
	var pathStat unix.Stat_t
	if err := unix.Fstatat(
		int(root.Fd()),
		name,
		&pathStat,
		unix.AT_SYMLINK_NOFOLLOW,
	); err != nil {
		return errors.New("raw connection generation file cannot be revalidated")
	}
	var descriptorStat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &descriptorStat); err != nil {
		return errors.New("raw connection generation file cannot be inspected")
	}
	if rawConnectionGenerationIdentity(pathStat) != rawConnectionGenerationIdentity(descriptorStat) {
		return errors.New("raw connection generation file identity changed")
	}
	return verifyRawConnectionGenerationRegularStat(descriptorStat)
}

func removeStaleRawConnectionGenerationTemp(root *os.File) error {
	var stat unix.Stat_t
	err := unix.Fstatat(
		int(root.Fd()),
		rawConnectionGenerationTempFilename,
		&stat,
		unix.AT_SYMLINK_NOFOLLOW,
	)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil || verifyRawConnectionGenerationRegularStat(stat) != nil {
		return errors.New("raw connection generation temporary state is unsafe")
	}
	if err := unix.Unlinkat(int(root.Fd()), rawConnectionGenerationTempFilename, 0); err != nil {
		return errors.New("raw connection generation temporary state cannot be removed")
	}
	if err := root.Sync(); err != nil {
		return errors.New("raw connection generation temporary cleanup is not durable")
	}
	return nil
}

func readRawConnectionGenerationState(
	root *os.File,
	runtimeEpoch uint64,
) (rawConnectionGenerationState, error) {
	file, stat, err := openRawConnectionGenerationRegularOptional(
		root,
		rawConnectionGenerationStateFilename,
		unix.O_RDONLY,
	)
	if err != nil {
		return rawConnectionGenerationState{}, err
	}
	if file == nil {
		return rawConnectionGenerationState{
			RuntimeEpoch: runtimeEpoch,
			Generations:  make(map[string]uint64),
		}, nil
	}
	defer func() { _ = file.Close() }()
	if stat.Size < 0 || stat.Size > rawConnectionGenerationMaximumStateBytes {
		return rawConnectionGenerationState{}, errors.New("raw connection generation state exceeds the size limit")
	}
	payload, err := io.ReadAll(io.LimitReader(file, rawConnectionGenerationMaximumStateBytes+1))
	if err != nil || len(payload) > rawConnectionGenerationMaximumStateBytes {
		return rawConnectionGenerationState{}, errors.New("raw connection generation state cannot be read")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var state rawConnectionGenerationState
	if err := decoder.Decode(&state); err != nil {
		return rawConnectionGenerationState{}, errors.New("raw connection generation state is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return rawConnectionGenerationState{}, errors.New("raw connection generation state is invalid")
	}
	canonical, err := json.Marshal(state)
	if err != nil || !bytes.Equal(payload, canonical) ||
		state.RuntimeEpoch == 0 || state.Generations == nil ||
		len(state.Generations) > rawConnectionGenerationMaximumEntries {
		return rawConnectionGenerationState{}, errors.New("raw connection generation state is invalid")
	}
	for ski, generation := range state.Generations {
		if ski != strings.ToLower(strings.TrimSpace(ski)) ||
			!validRuntimeSKI(ski) || generation == 0 {
			return rawConnectionGenerationState{}, errors.New("raw connection generation state is invalid")
		}
	}
	if state.RuntimeEpoch != runtimeEpoch {
		return rawConnectionGenerationState{
			RuntimeEpoch: runtimeEpoch,
			Generations:  make(map[string]uint64),
		}, nil
	}
	return state, nil
}

func writeRawConnectionGenerationState(
	opened rawConnectionGenerationOpenedRoot,
	lock *os.File,
	state rawConnectionGenerationState,
) error {
	if state.RuntimeEpoch == 0 || state.Generations == nil ||
		len(state.Generations) > rawConnectionGenerationMaximumEntries {
		return errors.New("raw connection generation state cannot be encoded")
	}
	payload, err := json.Marshal(state)
	if err != nil || len(payload) > rawConnectionGenerationMaximumStateBytes {
		return errors.New("raw connection generation state cannot be encoded")
	}
	fd, err := unix.Openat(
		int(opened.root.Fd()),
		rawConnectionGenerationTempFilename,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0o600,
	)
	if err != nil {
		return errors.New("raw connection generation state cannot be created")
	}
	temporary := os.NewFile(uintptr(fd), rawConnectionGenerationTempFilename)
	published := false
	defer func() {
		if temporary != nil {
			_ = temporary.Close()
		}
		if !published {
			_ = unix.Unlinkat(
				int(opened.root.Fd()),
				rawConnectionGenerationTempFilename,
				0,
			)
		}
	}()
	if err := unix.Fchmod(fd, 0o600); err != nil {
		return errors.New("raw connection generation state cannot be protected")
	}
	var tempStat unix.Stat_t
	if err := unix.Fstat(fd, &tempStat); err != nil ||
		verifyRawConnectionGenerationRegularStat(tempStat) != nil {
		return errors.New("raw connection generation temporary state is unsafe")
	}
	for written := 0; written < len(payload); {
		count, writeErr := temporary.Write(payload[written:])
		if writeErr != nil || count <= 0 {
			return errors.New("raw connection generation state cannot be persisted")
		}
		written += count
	}
	if err := temporary.Sync(); err != nil {
		return errors.New("raw connection generation state cannot be persisted")
	}
	if err := temporary.Close(); err != nil {
		temporary = nil
		return errors.New("raw connection generation state cannot be closed")
	}
	temporary = nil
	if err := revalidateRawConnectionGenerationRoot(opened); err != nil {
		return err
	}
	if err := revalidateRawConnectionGenerationFile(
		opened.root,
		rawConnectionGenerationLockFilename,
		lock,
	); err != nil {
		return err
	}
	if err := verifyRawConnectionGenerationDestination(opened.root); err != nil {
		return err
	}
	if err := unix.Renameat(
		int(opened.root.Fd()),
		rawConnectionGenerationTempFilename,
		int(opened.root.Fd()),
		rawConnectionGenerationStateFilename,
	); err != nil {
		return errors.New("raw connection generation state cannot be published")
	}
	published = true
	if err := revalidateRawConnectionGenerationRoot(opened); err != nil {
		return err
	}
	if err := revalidateRawConnectionGenerationFile(
		opened.root,
		rawConnectionGenerationLockFilename,
		lock,
	); err != nil {
		return err
	}
	if err := opened.root.Sync(); err != nil {
		return errors.New("raw connection generation state durability is unknown")
	}
	return nil
}

func verifyRawConnectionGenerationDestination(root *os.File) error {
	var stat unix.Stat_t
	err := unix.Fstatat(
		int(root.Fd()),
		rawConnectionGenerationStateFilename,
		&stat,
		unix.AT_SYMLINK_NOFOLLOW,
	)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil || verifyRawConnectionGenerationRegularStat(stat) != nil {
		return errors.New("raw connection generation state destination is unsafe")
	}
	return nil
}

func openRawConnectionGenerationRegularOptional(
	root *os.File,
	name string,
	flags int,
) (*os.File, unix.Stat_t, error) {
	var stat unix.Stat_t
	err := unix.Fstatat(int(root.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return nil, stat, nil
	}
	if err != nil {
		return nil, stat, errors.New("raw connection generation state cannot be inspected")
	}
	return openRawConnectionGenerationRegular(root, name, flags)
}

func openRawConnectionGenerationRegular(
	root *os.File,
	name string,
	flags int,
) (*os.File, unix.Stat_t, error) {
	var pathStat unix.Stat_t
	if err := unix.Fstatat(
		int(root.Fd()),
		name,
		&pathStat,
		unix.AT_SYMLINK_NOFOLLOW,
	); err != nil {
		return nil, pathStat, errors.New("raw connection generation file cannot be inspected")
	}
	if err := verifyRawConnectionGenerationRegularStat(pathStat); err != nil {
		return nil, pathStat, err
	}
	fd, err := unix.Openat(
		int(root.Fd()),
		name,
		flags|unix.O_NOFOLLOW|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, pathStat, errors.New("raw connection generation file cannot be opened")
	}
	file := os.NewFile(uintptr(fd), name)
	var descriptorStat unix.Stat_t
	if err := unix.Fstat(fd, &descriptorStat); err != nil {
		_ = file.Close()
		return nil, pathStat, errors.New("raw connection generation file cannot be inspected")
	}
	if rawConnectionGenerationIdentity(pathStat) != rawConnectionGenerationIdentity(descriptorStat) {
		_ = file.Close()
		return nil, pathStat, errors.New("raw connection generation file changed while opening")
	}
	if err := verifyRawConnectionGenerationRegularStat(descriptorStat); err != nil {
		_ = file.Close()
		return nil, pathStat, err
	}
	return file, descriptorStat, nil
}

func verifyRawConnectionGenerationDirectoryStat(stat unix.Stat_t) error {
	if uint32(stat.Mode)&uint32(unix.S_IFMT) != uint32(unix.S_IFDIR) ||
		uint32(stat.Mode)&0o7777 != 0o700 ||
		int(stat.Uid) != os.Geteuid() {
		return errors.New("raw connection generation state root is unsafe")
	}
	return nil
}

func verifyRawConnectionGenerationRegularStat(stat unix.Stat_t) error {
	if uint32(stat.Mode)&uint32(unix.S_IFMT) != uint32(unix.S_IFREG) ||
		uint32(stat.Mode)&0o7777 != 0o600 ||
		int(stat.Uid) != os.Geteuid() ||
		stat.Nlink != 1 {
		return errors.New("raw connection generation file is unsafe")
	}
	return nil
}

func rawConnectionGenerationIdentity(stat unix.Stat_t) rawConnectionGenerationFileIdentity {
	return rawConnectionGenerationFileIdentity{
		device: uint64(stat.Dev),
		inode:  uint64(stat.Ino),
	}
}
