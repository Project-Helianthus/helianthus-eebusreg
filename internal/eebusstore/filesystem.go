//go:build linux || darwin

package eebusstore

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

type syscallPoint string

const (
	pointBootstrapParentFsync     syscallPoint = "bootstrap_parent_fsync"
	pointBootstrapLockFsync       syscallPoint = "bootstrap_lock_fsync"
	pointBootstrapRootFsync       syscallPoint = "bootstrap_root_fsync"
	pointCapabilityACL            syscallPoint = "capability_acl"
	pointCapabilityDirectoryFsync syscallPoint = "capability_directory_fsync"
	pointGenerationWrite          syscallPoint = "generation_write"
	pointGenerationFileFsync      syscallPoint = "generation_file_fsync"
	pointGenerationRename         syscallPoint = "generation_rename"
	pointGenerationsFsync         syscallPoint = "generations_fsync"
	pointManifestWrite            syscallPoint = "manifest_write"
	pointManifestFileFsync        syscallPoint = "manifest_file_fsync"
	pointManifestRename           syscallPoint = "manifest_rename"
	pointPublicationRootFsync     syscallPoint = "publication_root_fsync"
	pointPreMaintenanceRemove     syscallPoint = "pre_maintenance_remove"
	pointPostMaintenanceRemove    syscallPoint = "post_maintenance_remove"
)

type directoryRole string

const (
	directoryRoot        directoryRole = "root"
	directoryGenerations directoryRole = "generations"
)

type syscallCall struct {
	point     syscallPoint
	directory directoryRole
	oldName   string
	newName   string
}

type syscallHook func(syscallCall) error

type nativeSyscallBackend struct {
	hook syscallHook
}

type fileIdentity struct {
	device uint64
	inode  uint64
}

type preparedRoot struct {
	root     *os.File
	lock     *os.File
	identity fileIdentity
}

var localWriters = struct {
	sync.Mutex
	owners map[fileIdentity]struct{}
}{owners: make(map[fileIdentity]struct{})}

var generationNamePattern = regexp.MustCompile(`^g-[0-9]{20}\.json$`)
var generationTempPattern = regexp.MustCompile(`^\.tmp-generation-[a-z0-9]+$`)
var manifestTempPattern = regexp.MustCompile(`^\.tmp-manifest-[a-z0-9]+$`)

func newNativeSyscallBackend(hook syscallHook) (*nativeSyscallBackend, error) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return nil, newStoreError(outcomeFilesystemCapabilityUnavailable, "select_backend", errors.New("unsupported platform"))
	}
	return &nativeSyscallBackend{hook: hook}, nil
}

func (backend *nativeSyscallBackend) invoke(point syscallPoint, directory directoryRole, oldName, newName string) error {
	if backend != nil && backend.hook != nil {
		return backend.hook(syscallCall{point: point, directory: directory, oldName: oldName, newName: newName})
	}
	return nil
}

func (backend *nativeSyscallBackend) prepareRoot(path string) (preparedRoot, error) {
	var prepared preparedRoot
	if err := validateRootPath(path); err != nil {
		return prepared, err
	}
	parentPath := filepath.Dir(path)
	rootName := filepath.Base(path)
	canonicalParent, err := filepath.EvalSymlinks(parentPath)
	if err != nil {
		return prepared, newStoreError(outcomePathRejected, "open_parent", err)
	}
	parent, err := openAbsoluteDirectoryNoFollow(canonicalParent)
	if err != nil {
		return prepared, newStoreError(outcomePathRejected, "open_parent", err)
	}
	defer parent.Close()

	created := false
	var pathStat unix.Stat_t
	err = unix.Fstatat(int(parent.Fd()), rootName, &pathStat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		if err := unix.Mkdirat(int(parent.Fd()), rootName, 0o700); err != nil {
			return prepared, newStoreError(outcomeIOFailed, "create_root", err)
		}
		created = true
	} else if err != nil {
		return prepared, newStoreError(outcomePathRejected, "inspect_root", err)
	} else if pathStat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return prepared, newStoreError(outcomePathRejected, "inspect_root", errors.New("root is not a directory"))
	}

	root, _, err := openVerifiedAt(parent, rootName, true, false)
	if err != nil {
		return prepared, remapRootSafety(err)
	}
	prepared.root = root
	cleanup := true
	defer func() {
		if cleanup {
			_ = root.Close()
		}
	}()

	if created {
		if err := backend.invoke(pointBootstrapParentFsync, directoryRoot, "", ""); err != nil {
			return preparedRoot{}, newStoreError(outcomeBootstrapDurabilityUnknown, "bootstrap_parent_fsync", err)
		}
		if err := parent.Sync(); err != nil {
			return preparedRoot{}, newStoreError(outcomeBootstrapDurabilityUnknown, "bootstrap_parent_fsync", err)
		}
	}
	if err := backend.probeCapabilities(root); err != nil {
		return preparedRoot{}, err
	}

	lock, err := openVerifiedAtOptional(root, "LOCK", false, true)
	if err != nil {
		return preparedRoot{}, err
	}
	if lock == nil {
		if err := backend.resumeBootstrap(root); err != nil {
			return preparedRoot{}, err
		}
		lock, _, err = openVerifiedAt(root, "LOCK", false, true)
		if err != nil {
			return preparedRoot{}, err
		}
	}
	info, err := lock.Stat()
	if err != nil || info.Size() != 0 {
		_ = lock.Close()
		return preparedRoot{}, newStoreError(outcomeLayoutRejected, "verify_lock", err)
	}
	identity, err := descriptorIdentity(root)
	if err != nil {
		_ = lock.Close()
		return preparedRoot{}, newStoreError(outcomeFilesystemCapabilityUnavailable, "root_identity", err)
	}
	prepared.lock = lock
	prepared.identity = identity
	cleanup = false
	return prepared, nil
}

func validateRootPath(path string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsRune(path, 0) || filepath.Base(path) == string(filepath.Separator) || filepath.Base(path) == "." || filepath.Base(path) == ".." {
		return newStoreError(outcomePathRejected, "validate_path", errors.New("invalid root path"))
	}
	if filepath.Separator == '/' && strings.ContainsRune(path, '\\') {
		return newStoreError(outcomePathRejected, "validate_path", errors.New("non-native separator"))
	}
	return nil
}

func openAbsoluteDirectoryNoFollow(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("parent path is not canonical")
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(fd), "directory")
	for _, component := range strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator)) {
		if component == "" {
			continue
		}
		nextFD, err := unix.Openat(int(current.Fd()), component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			_ = current.Close()
			return nil, err
		}
		next := os.NewFile(uintptr(nextFD), "directory")
		_ = current.Close()
		current = next
	}
	return current, nil
}

func remapRootSafety(err error) error {
	result := outcomeOf(err)
	if result == outcomeLayoutRejected {
		return newStoreError(outcomePathRejected, "open_root", err)
	}
	return err
}

func (backend *nativeSyscallBackend) probeCapabilities(root *os.File) error {
	if err := backend.invoke(pointCapabilityDirectoryFsync, directoryRoot, "", ""); err != nil {
		return newStoreError(outcomeFilesystemCapabilityUnavailable, "probe_directory_fsync", err)
	}
	if err := root.Sync(); err != nil {
		return newStoreError(outcomeFilesystemCapabilityUnavailable, "probe_directory_fsync", err)
	}
	if err := backend.invoke(pointCapabilityACL, directoryRoot, "", ""); err != nil {
		return newStoreError(outcomeFilesystemCapabilityUnavailable, "probe_acl", err)
	}
	additional, err := inspectAdditionalAccess(int(root.Fd()))
	if err != nil {
		return newStoreError(outcomeFilesystemCapabilityUnavailable, "probe_acl", err)
	}
	if additional {
		return newStoreError(outcomePermissionsRejected, "probe_acl", errors.New("additional access"))
	}
	return nil
}

func (backend *nativeSyscallBackend) resumeBootstrap(root *os.File) error {
	names, err := directoryNames(root)
	if err != nil {
		return newStoreError(outcomeIOFailed, "inspect_bootstrap", err)
	}
	generationsPresent := false
	for _, name := range names {
		if name != "generations" {
			return newStoreError(outcomeLayoutRejected, "resume_bootstrap", errors.New("non-bootstrap object"))
		}
		generationsPresent = true
	}
	if generationsPresent {
		generations, _, err := openVerifiedAt(root, "generations", true, false)
		if err != nil {
			return err
		}
		names, readErr := directoryNames(generations)
		_ = generations.Close()
		if readErr != nil || len(names) != 0 {
			return newStoreError(outcomeLayoutRejected, "resume_bootstrap", readErr)
		}
	} else if err := unix.Mkdirat(int(root.Fd()), "generations", 0o700); err != nil {
		return newStoreError(outcomeIOFailed, "create_generations", err)
	}
	lockFD, err := unix.Openat(int(root.Fd()), "LOCK", unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return newStoreError(outcomeIOFailed, "create_lock", err)
	}
	lock := os.NewFile(uintptr(lockFD), "LOCK")
	if err := verifyOpenedDescriptor(lock, false); err != nil {
		_ = lock.Close()
		return err
	}
	if err := backend.invoke(pointBootstrapLockFsync, directoryRoot, "LOCK", ""); err != nil {
		_ = lock.Close()
		return newStoreError(outcomeBootstrapDurabilityUnknown, "bootstrap_lock_fsync", err)
	}
	if err := lock.Sync(); err != nil {
		_ = lock.Close()
		return newStoreError(outcomeBootstrapDurabilityUnknown, "bootstrap_lock_fsync", err)
	}
	_ = lock.Close()
	if err := backend.invoke(pointBootstrapRootFsync, directoryRoot, "", ""); err != nil {
		return newStoreError(outcomeBootstrapDurabilityUnknown, "bootstrap_root_fsync", err)
	}
	if err := root.Sync(); err != nil {
		return newStoreError(outcomeBootstrapDurabilityUnknown, "bootstrap_root_fsync", err)
	}
	return nil
}

func openVerifiedAtOptional(directory *os.File, name string, wantDirectory, writable bool) (*os.File, error) {
	var pathStat unix.Stat_t
	err := unix.Fstatat(int(directory.Fd()), name, &pathStat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return nil, nil
	}
	if err != nil {
		return nil, newStoreError(outcomeIOFailed, "inspect_object", err)
	}
	file, _, err := openVerifiedAt(directory, name, wantDirectory, writable)
	return file, err
}

func openVerifiedAt(directory *os.File, name string, wantDirectory, writable bool) (*os.File, unix.Stat_t, error) {
	var pathStat unix.Stat_t
	if err := unix.Fstatat(int(directory.Fd()), name, &pathStat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, pathStat, newStoreError(outcomeLayoutRejected, "inspect_object", err)
	}
	wantType := uint16(unix.S_IFREG)
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	if wantDirectory {
		wantType = unix.S_IFDIR
		flags |= unix.O_DIRECTORY
	} else if writable {
		flags = unix.O_RDWR | unix.O_NOFOLLOW | unix.O_CLOEXEC
	}
	if uint16(pathStat.Mode)&uint16(unix.S_IFMT) != wantType {
		return nil, pathStat, newStoreError(outcomeLayoutRejected, "verify_object", errors.New("unexpected object type"))
	}
	fd, err := unix.Openat(int(directory.Fd()), name, flags, 0)
	if err != nil {
		return nil, pathStat, newStoreError(outcomeLayoutRejected, "open_object", err)
	}
	file := os.NewFile(uintptr(fd), name)
	var descriptorStat unix.Stat_t
	if err := unix.Fstat(fd, &descriptorStat); err != nil {
		_ = file.Close()
		return nil, pathStat, newStoreError(outcomeFilesystemCapabilityUnavailable, "stat_object", err)
	}
	if pathStat.Dev != descriptorStat.Dev || pathStat.Ino != descriptorStat.Ino {
		_ = file.Close()
		return nil, pathStat, newStoreError(outcomeLayoutRejected, "verify_object", errors.New("pathname identity mismatch"))
	}
	if err := verifyStat(descriptorStat, wantDirectory); err != nil {
		_ = file.Close()
		return nil, pathStat, err
	}
	additional, err := inspectAdditionalAccess(fd)
	if err != nil {
		_ = file.Close()
		return nil, pathStat, newStoreError(outcomeFilesystemCapabilityUnavailable, "inspect_acl", err)
	}
	if additional {
		_ = file.Close()
		return nil, pathStat, newStoreError(outcomePermissionsRejected, "inspect_acl", errors.New("additional access"))
	}
	return file, descriptorStat, nil
}

func verifyOpenedDescriptor(file *os.File, wantDirectory bool) error {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return newStoreError(outcomeFilesystemCapabilityUnavailable, "stat_created_object", err)
	}
	if err := verifyStat(stat, wantDirectory); err != nil {
		return err
	}
	additional, err := inspectAdditionalAccess(int(file.Fd()))
	if err != nil {
		return newStoreError(outcomeFilesystemCapabilityUnavailable, "inspect_acl", err)
	}
	if additional {
		return newStoreError(outcomePermissionsRejected, "inspect_acl", errors.New("additional access"))
	}
	return nil
}

func verifyStat(stat unix.Stat_t, wantDirectory bool) error {
	wantType := uint16(unix.S_IFREG)
	wantMode := uint32(0o600)
	if wantDirectory {
		wantType = unix.S_IFDIR
		wantMode = 0o700
	}
	if uint16(stat.Mode)&uint16(unix.S_IFMT) != wantType {
		return newStoreError(outcomeLayoutRejected, "verify_object", errors.New("unexpected type"))
	}
	if uint32(stat.Mode)&0o7777 != wantMode || int(stat.Uid) != os.Geteuid() {
		return newStoreError(outcomePermissionsRejected, "verify_object", errors.New("ownership or mode"))
	}
	if !wantDirectory && stat.Nlink != 1 {
		return newStoreError(outcomeLayoutRejected, "verify_object", errors.New("hard link"))
	}
	return nil
}

func descriptorIdentity(file *os.File) (fileIdentity, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return fileIdentity{}, err
	}
	return fileIdentity{device: uint64(stat.Dev), inode: uint64(stat.Ino)}, nil
}

func acquireLocalWriter(identity fileIdentity) bool {
	localWriters.Lock()
	defer localWriters.Unlock()
	if _, exists := localWriters.owners[identity]; exists {
		return false
	}
	localWriters.owners[identity] = struct{}{}
	return true
}

func releaseLocalWriter(identity fileIdentity) {
	localWriters.Lock()
	delete(localWriters.owners, identity)
	localWriters.Unlock()
}

func acquireProcessLock(file *os.File) error {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return newStoreError(outcomeWriterBusy, "acquire_lock", err)
	}
	if err != nil {
		return newStoreError(outcomeLockUnavailable, "acquire_lock", err)
	}
	return nil
}

func releaseProcessLock(file *os.File) error {
	if file == nil {
		return nil
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_UN); err != nil {
		return newStoreError(outcomeLockUnavailable, "release_lock", err)
	}
	return nil
}

func directoryNames(directory *os.File) ([]string, error) {
	if _, err := directory.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	const maximumDirectoryEntries = 132
	names := make([]string, 0, 16)
	for len(names) <= maximumDirectoryEntries {
		batch, err := directory.Readdirnames(32)
		names = append(names, batch...)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	if len(names) > maximumDirectoryEntries {
		return nil, errors.New("directory entry bound exceeded")
	}
	sort.Strings(names)
	return names, nil
}

func objectExistsAt(directory *os.File, name string) (bool, error) {
	var stat unix.Stat_t
	err := unix.Fstatat(int(directory.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, newStoreError(outcomeIOFailed, "inspect_object", err)
	}
	return true, nil
}

func parseGenerationName(name string) (uint64, bool) {
	if !generationNamePattern.MatchString(name) {
		return 0, false
	}
	sequence, err := strconv.ParseUint(name[2:22], 10, 64)
	if err != nil || sequence == 0 || sequence > math.MaxInt64 || generationFilename(sequence) != name {
		return 0, false
	}
	return sequence, true
}

func (backend *nativeSyscallBackend) createTemporary(directory *os.File, role directoryRole, prefix string) (*os.File, string, error) {
	for attempt := 0; attempt < 16; attempt++ {
		random := make([]byte, 12)
		if _, err := rand.Read(random); err != nil {
			return nil, "", newStoreError(outcomeFilesystemCapabilityUnavailable, "random_temp_name", err)
		}
		name := prefix + hex.EncodeToString(random)
		fd, err := unix.Openat(int(directory.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return nil, "", newStoreError(outcomeIOFailed, "create_temp", err)
		}
		file := os.NewFile(uintptr(fd), name)
		if err := verifyOpenedDescriptor(file, false); err != nil {
			_ = file.Close()
			_ = unix.Unlinkat(int(directory.Fd()), name, 0)
			return nil, "", err
		}
		_ = role
		return file, name, nil
	}
	return nil, "", newStoreError(outcomeIOFailed, "create_temp", errors.New("temporary name collisions"))
}

func (backend *nativeSyscallBackend) writeAll(file *os.File, role directoryRole, name string, payload []byte, point syscallPoint) error {
	if err := backend.invoke(point, role, name, ""); err != nil {
		return err
	}
	written, err := file.Write(payload)
	if err != nil {
		return err
	}
	if written != len(payload) {
		return io.ErrShortWrite
	}
	return nil
}

func (backend *nativeSyscallBackend) syncFile(file *os.File, point syscallPoint, role directoryRole, name string) error {
	if err := backend.invoke(point, role, name, ""); err != nil {
		return err
	}
	return file.Sync()
}

func (backend *nativeSyscallBackend) syncDirectory(directory *os.File, point syscallPoint, role directoryRole) error {
	if err := backend.invoke(point, role, "", ""); err != nil {
		return err
	}
	return directory.Sync()
}

func (backend *nativeSyscallBackend) rename(directory *os.File, role directoryRole, oldName, newName string, point syscallPoint) error {
	if err := backend.invoke(point, role, oldName, newName); err != nil {
		return err
	}
	return unix.Renameat(int(directory.Fd()), oldName, int(directory.Fd()), newName)
}

func (backend *nativeSyscallBackend) remove(directory *os.File, role directoryRole, name string, point syscallPoint) error {
	if err := backend.invoke(point, role, name, ""); err != nil {
		return err
	}
	return unix.Unlinkat(int(directory.Fd()), name, 0)
}

func readVerifiedFile(directory *os.File, name string, maximum int) ([]byte, error) {
	file, _, err := openVerifiedAt(directory, name, false, false)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil {
		return nil, newStoreError(outcomeIOFailed, "read_object", err)
	}
	if len(payload) > maximum {
		return nil, newStoreError(outcomeMalformedState, "read_object", errors.New("object too large"))
	}
	return payload, nil
}
