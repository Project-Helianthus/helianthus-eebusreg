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
	pointBootstrapParentFsync        syscallPoint = "bootstrap_parent_fsync"
	pointBootstrapLockFsync          syscallPoint = "bootstrap_lock_fsync"
	pointBootstrapRootFsync          syscallPoint = "bootstrap_root_fsync"
	pointCapabilityBackendPolicy     syscallPoint = "capability_backend_policy"
	pointCapabilityStableIdentity    syscallPoint = "capability_stable_identity"
	pointCapabilityAtomicReplacement syscallPoint = "capability_atomic_replacement"
	pointCapabilityProcessLock       syscallPoint = "capability_process_lock"
	pointCapabilityNoFollowCreate    syscallPoint = "capability_nofollow_create"
	pointCapabilityFileFsync         syscallPoint = "capability_file_fsync"
	pointCapabilityDirectoryFsync    syscallPoint = "capability_directory_fsync"
	pointCapabilityACL               syscallPoint = "capability_acl"
	pointCapabilityCleanup           syscallPoint = "capability_cleanup"
	pointGenerationWrite             syscallPoint = "generation_write"
	pointGenerationFileFsync         syscallPoint = "generation_file_fsync"
	pointGenerationRename            syscallPoint = "generation_rename"
	pointGenerationsFsync            syscallPoint = "generations_fsync"
	pointManifestWrite               syscallPoint = "manifest_write"
	pointManifestFileFsync           syscallPoint = "manifest_file_fsync"
	pointManifestRename              syscallPoint = "manifest_rename"
	pointPublicationRootFsync        syscallPoint = "publication_root_fsync"
	pointPreMaintenanceRemove        syscallPoint = "pre_maintenance_remove"
	pointPreMaintenanceFsync         syscallPoint = "pre_maintenance_fsync"
	pointPostMaintenanceRemove       syscallPoint = "post_maintenance_remove"
	pointPostMaintenanceFsync        syscallPoint = "post_maintenance_fsync"
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
	configuredPath   string
	parent           *os.File
	rootName         string
	root             *os.File
	lock             *os.File
	rootIdentity     fileIdentity
	lockIdentity     fileIdentity
	bootstrapDurable bool
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
	parent, err := openAbsoluteDirectoryNoFollow(parentPath)
	if err != nil {
		return prepared, newStoreError(outcomePathRejected, "open_parent", err)
	}
	cleanup := true
	defer func() {
		if !cleanup {
			return
		}
		if prepared.lock != nil {
			_ = prepared.lock.Close()
		}
		if prepared.root != nil {
			_ = prepared.root.Close()
		}
		_ = parent.Close()
	}()

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
	prepared.configuredPath = path
	prepared.parent = parent
	prepared.rootName = rootName
	prepared.root = root

	if created {
		if err := backend.syncBootstrapParent(parent); err != nil {
			return preparedRoot{}, err
		}
	}

	lock, err := openVerifiedAtOptional(root, "LOCK", false, true)
	if err != nil {
		return preparedRoot{}, err
	}
	if lock == nil {
		safe, err := inspectBootstrapSubset(root)
		if err != nil {
			return preparedRoot{}, err
		}
		if !safe {
			return preparedRoot{}, newStoreError(outcomeLayoutRejected, "resume_bootstrap", errors.New("non-bootstrap store"))
		}
		lock, err = backend.completeBootstrap(parent, root, nil, created)
		if err != nil {
			return preparedRoot{}, err
		}
		prepared.bootstrapDurable = true
	}
	prepared.lock = lock
	if err := verifyEmptyLock(lock); err != nil {
		_ = lock.Close()
		prepared.lock = nil
		return preparedRoot{}, newStoreError(outcomeLayoutRejected, "verify_lock", err)
	}
	rootIdentity, err := descriptorIdentity(root)
	if err != nil {
		return preparedRoot{}, newStoreError(outcomeFilesystemCapabilityUnavailable, "root_identity", err)
	}
	lockIdentity, err := descriptorIdentity(lock)
	if err != nil {
		return preparedRoot{}, newStoreError(outcomeFilesystemCapabilityUnavailable, "lock_identity", err)
	}
	prepared.rootIdentity = rootIdentity
	prepared.lockIdentity = lockIdentity
	cleanup = false
	return prepared, nil
}

func (backend *nativeSyscallBackend) syncBootstrapParent(parent *os.File) error {
	if err := backend.invoke(pointBootstrapParentFsync, directoryRoot, "", ""); err != nil {
		return newStoreError(outcomeBootstrapDurabilityUnknown, "bootstrap_parent_fsync", err)
	}
	if err := parent.Sync(); err != nil {
		return newStoreError(outcomeBootstrapDurabilityUnknown, "bootstrap_parent_fsync", err)
	}
	return nil
}

func inspectBootstrapSubset(root *os.File) (bool, error) {
	names, err := directoryNames(root)
	if err != nil {
		return false, newStoreError(outcomeIOFailed, "inspect_bootstrap", err)
	}
	for _, name := range names {
		switch name {
		case "LOCK":
			lock, _, err := openVerifiedAt(root, name, false, true)
			if err != nil {
				return false, err
			}
			err = verifyEmptyLock(lock)
			_ = lock.Close()
			if err != nil {
				return false, newStoreError(outcomeLayoutRejected, "verify_lock", err)
			}
		case "generations":
			generations, _, err := openVerifiedAt(root, name, true, false)
			if err != nil {
				return false, err
			}
			generationNames, readErr := directoryNames(generations)
			_ = generations.Close()
			if readErr != nil {
				return false, newStoreError(outcomeIOFailed, "inspect_bootstrap", readErr)
			}
			if len(generationNames) != 0 {
				return false, nil
			}
		default:
			return false, nil
		}
	}
	return true, nil
}

func (backend *nativeSyscallBackend) completeBootstrap(parent, root, heldLock *os.File, parentAlreadySynced bool) (*os.File, error) {
	safe, err := inspectBootstrapSubset(root)
	if err != nil {
		return nil, err
	}
	if !safe {
		return nil, newStoreError(outcomeLayoutRejected, "resume_bootstrap", errors.New("non-bootstrap store"))
	}
	if !parentAlreadySynced {
		if err := backend.syncBootstrapParent(parent); err != nil {
			return nil, err
		}
	}

	generations, err := openVerifiedAtOptional(root, "generations", true, false)
	if err != nil {
		return nil, err
	}
	if generations == nil {
		if err := unix.Mkdirat(int(root.Fd()), "generations", 0o700); err != nil {
			return nil, newStoreError(outcomeIOFailed, "create_generations", err)
		}
		generations, _, err = openVerifiedAt(root, "generations", true, false)
		if err != nil {
			return nil, err
		}
	}
	_ = generations.Close()

	lock := heldLock
	ownsLock := false
	if lock == nil {
		lockFD, openErr := unix.Openat(int(root.Fd()), "LOCK", unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if errors.Is(openErr, unix.EEXIST) {
			lock, _, openErr = openVerifiedAt(root, "LOCK", false, true)
		} else if openErr == nil {
			lock = os.NewFile(uintptr(lockFD), "LOCK")
			openErr = verifyOpenedDescriptor(lock, false)
		}
		ownsLock = lock != nil
		if openErr != nil {
			if lock != nil {
				_ = lock.Close()
			}
			return nil, newStoreError(outcomeIOFailed, "create_lock", openErr)
		}
	}
	if err := verifyEmptyLock(lock); err != nil {
		if ownsLock {
			_ = lock.Close()
		}
		return nil, newStoreError(outcomeLayoutRejected, "verify_lock", err)
	}
	if err := backend.invoke(pointBootstrapLockFsync, directoryRoot, "LOCK", ""); err != nil {
		if ownsLock {
			_ = lock.Close()
		}
		return nil, newStoreError(outcomeBootstrapDurabilityUnknown, "bootstrap_lock_fsync", err)
	}
	if err := lock.Sync(); err != nil {
		if ownsLock {
			_ = lock.Close()
		}
		return nil, newStoreError(outcomeBootstrapDurabilityUnknown, "bootstrap_lock_fsync", err)
	}
	if err := backend.invoke(pointBootstrapRootFsync, directoryRoot, "", ""); err != nil {
		if ownsLock {
			_ = lock.Close()
		}
		return nil, newStoreError(outcomeBootstrapDurabilityUnknown, "bootstrap_root_fsync", err)
	}
	if err := root.Sync(); err != nil {
		if ownsLock {
			_ = lock.Close()
		}
		return nil, newStoreError(outcomeBootstrapDurabilityUnknown, "bootstrap_root_fsync", err)
	}
	return lock, nil
}

func verifyEmptyLock(lock *os.File) error {
	info, err := lock.Stat()
	if err != nil {
		return err
	}
	if info.Size() != 0 {
		return errors.New("LOCK is not empty")
	}
	return nil
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

type probeArtifact struct {
	directory *os.File
	role      directoryRole
	name      string
}

type capabilityProbe struct {
	backend   *nativeSyscallBackend
	artifacts []probeArtifact
}

func (backend *nativeSyscallBackend) probeCapabilities(root, generations *os.File) (result error) {
	probe := capabilityProbe{backend: backend}
	defer func() {
		if cleanupErr := probe.cleanup(); cleanupErr != nil {
			result = capabilityUnavailable("probe_cleanup", cleanupErr)
		}
	}()

	if err := backend.invoke(pointCapabilityBackendPolicy, directoryRoot, "", ""); err != nil {
		return capabilityUnavailable("probe_backend_policy", err)
	}
	if err := qualifyFilesystemPolicy(int(root.Fd())); err != nil {
		return capabilityUnavailable("probe_backend_policy", err)
	}
	rootIdentity, err := descriptorIdentity(root)
	if err != nil {
		return capabilityUnavailable("probe_stable_identity", err)
	}
	generationIdentity, err := descriptorIdentity(generations)
	if err != nil {
		return capabilityUnavailable("probe_stable_identity", err)
	}
	if rootIdentity.device == 0 || rootIdentity.inode == 0 || generationIdentity.device == 0 || generationIdentity.inode == 0 || rootIdentity.device != generationIdentity.device {
		return capabilityUnavailable("probe_stable_identity", errors.New("unstable directory identity"))
	}

	if err := backend.invoke(pointCapabilityProcessLock, directoryRoot, "LOCK", ""); err != nil {
		return capabilityUnavailable("probe_process_lock", err)
	}
	if err := backend.invoke(pointCapabilityACL, directoryRoot, "", ""); err != nil {
		return capabilityUnavailable("probe_acl", err)
	}
	for _, directory := range []*os.File{root, generations} {
		additional, err := inspectAdditionalAccess(int(directory.Fd()))
		if err != nil {
			return capabilityUnavailable("probe_acl", err)
		}
		if additional {
			return newStoreError(outcomePermissionsRejected, "probe_acl", errors.New("additional access"))
		}
	}

	if err := probe.probeDirectory(root, directoryRoot, ".tmp-manifest-", true); err != nil {
		return err
	}
	if err := probe.probeDirectory(generations, directoryGenerations, ".tmp-generation-", false); err != nil {
		return err
	}
	return nil
}

func (probe *capabilityProbe) probeDirectory(directory *os.File, role directoryRole, prefix string, testLock bool) error {
	source, sourceName, err := probe.createFile(directory, role, prefix)
	if err != nil {
		return err
	}
	defer source.Close()
	target, targetName, err := probe.createFile(directory, role, prefix)
	if err != nil {
		return err
	}
	targetIdentity, err := descriptorIdentity(target)
	if err != nil || targetIdentity.device == 0 || targetIdentity.inode == 0 {
		_ = target.Close()
		return capabilityUnavailable("probe_stable_identity", err)
	}
	if err := target.Close(); err != nil {
		return capabilityUnavailable("probe_close", err)
	}

	if err := probe.backend.invoke(pointCapabilityStableIdentity, role, sourceName, ""); err != nil {
		return capabilityUnavailable("probe_stable_identity", err)
	}
	sourceIdentity, err := descriptorIdentity(source)
	if err != nil || sourceIdentity.device == 0 || sourceIdentity.inode == 0 {
		return capabilityUnavailable("probe_stable_identity", err)
	}
	if sourceIdentity == targetIdentity {
		return capabilityUnavailable("probe_stable_identity", errors.New("distinct files shared identity"))
	}
	var pathStat unix.Stat_t
	if err := unix.Fstatat(int(directory.Fd()), sourceName, &pathStat, unix.AT_SYMLINK_NOFOLLOW); err != nil || sourceIdentity != (fileIdentity{device: uint64(pathStat.Dev), inode: uint64(pathStat.Ino)}) {
		return capabilityUnavailable("probe_stable_identity", err)
	}
	if _, err := source.Write([]byte("capability-probe")); err != nil {
		return capabilityUnavailable("probe_file_write", err)
	}
	if err := probe.backend.invoke(pointCapabilityFileFsync, role, sourceName, ""); err != nil {
		return capabilityUnavailable("probe_file_fsync", err)
	}
	if err := source.Sync(); err != nil {
		return capabilityUnavailable("probe_file_fsync", err)
	}

	if testLock {
		contender, _, err := openVerifiedAt(directory, sourceName, false, true)
		if err != nil {
			return capabilityUnavailable("probe_process_lock", err)
		}
		if err := unix.Flock(int(source.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			_ = contender.Close()
			return capabilityUnavailable("probe_process_lock", err)
		}
		contenderErr := unix.Flock(int(contender.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if contenderErr == nil {
			_ = unix.Flock(int(contender.Fd()), unix.LOCK_UN)
		}
		_ = contender.Close()
		_ = unix.Flock(int(source.Fd()), unix.LOCK_UN)
		if !errors.Is(contenderErr, unix.EWOULDBLOCK) && !errors.Is(contenderErr, unix.EAGAIN) {
			return capabilityUnavailable("probe_process_lock", errors.New("nonblocking lock did not contend"))
		}
	}

	if err := probe.backend.invoke(pointCapabilityAtomicReplacement, role, sourceName, targetName); err != nil {
		return capabilityUnavailable("probe_atomic_replacement", err)
	}
	if err := unix.Renameat(int(directory.Fd()), sourceName, int(directory.Fd()), targetName); err != nil {
		return capabilityUnavailable("probe_atomic_replacement", err)
	}
	replacement, _, err := openVerifiedAt(directory, targetName, false, false)
	if err != nil {
		return capabilityUnavailable("probe_atomic_replacement", err)
	}
	replacementIdentity, identityErr := descriptorIdentity(replacement)
	_ = replacement.Close()
	if identityErr != nil || replacementIdentity != sourceIdentity || replacementIdentity == targetIdentity {
		return capabilityUnavailable("probe_atomic_replacement", identityErr)
	}
	replacementPayload, err := readVerifiedFile(directory, targetName, 64)
	if err != nil || string(replacementPayload) != "capability-probe" {
		return capabilityUnavailable("probe_atomic_replacement", err)
	}

	symlinkName, err := randomTemporaryName(prefix)
	if err != nil {
		return capabilityUnavailable("probe_nofollow_create", err)
	}
	if err := unix.Symlinkat(targetName, int(directory.Fd()), symlinkName); err != nil {
		return capabilityUnavailable("probe_nofollow_create", err)
	}
	probe.artifacts = append(probe.artifacts, probeArtifact{directory: directory, role: role, name: symlinkName})
	if err := probe.backend.invoke(pointCapabilityNoFollowCreate, role, symlinkName, ""); err != nil {
		return capabilityUnavailable("probe_nofollow_create", err)
	}
	openedFD, openErr := unix.Openat(int(directory.Fd()), symlinkName, unix.O_WRONLY|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if openErr == nil {
		_ = unix.Close(openedFD)
		return capabilityUnavailable("probe_nofollow_create", errors.New("nofollow creation followed symlink"))
	}
	if !errors.Is(openErr, unix.ELOOP) {
		return capabilityUnavailable("probe_nofollow_create", openErr)
	}

	if err := probe.backend.invoke(pointCapabilityDirectoryFsync, role, "", ""); err != nil {
		return capabilityUnavailable("probe_directory_fsync", err)
	}
	if err := directory.Sync(); err != nil {
		return capabilityUnavailable("probe_directory_fsync", err)
	}
	return nil
}

func (probe *capabilityProbe) createFile(directory *os.File, role directoryRole, prefix string) (*os.File, string, error) {
	for attempt := 0; attempt < 16; attempt++ {
		name, err := randomTemporaryName(prefix)
		if err != nil {
			return nil, "", capabilityUnavailable("probe_create", err)
		}
		fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return nil, "", capabilityUnavailable("probe_create", err)
		}
		file := os.NewFile(uintptr(fd), name)
		probe.artifacts = append(probe.artifacts, probeArtifact{directory: directory, role: role, name: name})
		if err := verifyOpenedDescriptor(file, false); err != nil {
			_ = file.Close()
			return nil, "", capabilityUnavailable("probe_create", err)
		}
		return file, name, nil
	}
	return nil, "", capabilityUnavailable("probe_create", errors.New("temporary name collisions"))
}

func (probe *capabilityProbe) cleanup() error {
	var first error
	changed := make(map[*os.File]directoryRole)
	for _, artifact := range probe.artifacts {
		if err := probe.backend.invoke(pointCapabilityCleanup, artifact.role, artifact.name, ""); err != nil && first == nil {
			first = err
		}
		if err := unix.Unlinkat(int(artifact.directory.Fd()), artifact.name, 0); err != nil && !errors.Is(err, unix.ENOENT) && first == nil {
			first = err
		}
		changed[artifact.directory] = artifact.role
	}
	for directory := range changed {
		if err := directory.Sync(); err != nil && first == nil {
			first = err
		}
	}
	for _, artifact := range probe.artifacts {
		var stat unix.Stat_t
		err := unix.Fstatat(int(artifact.directory.Fd()), artifact.name, &stat, unix.AT_SYMLINK_NOFOLLOW)
		if !errors.Is(err, unix.ENOENT) && first == nil {
			if err == nil {
				first = errors.New("probe artifact remained")
			} else {
				first = err
			}
		}
	}
	return first
}

func randomTemporaryName(prefix string) (string, error) {
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(random), nil
}

func capabilityUnavailable(operation string, cause error) error {
	if cause == nil {
		cause = errors.New("capability proof failed")
	}
	return newStoreError(outcomeFilesystemCapabilityUnavailable, operation, cause)
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
