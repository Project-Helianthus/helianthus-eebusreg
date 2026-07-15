//go:build linux || darwin

package eebusadmin

import (
	"context"
	"errors"
	network "net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	adminSocketName            = "admin.sock"
	maxAdminConnections        = 8
	maxAdminCommandsPerSession = 4
	adminIODeadline            = 5 * time.Second
	adminHandlerDeadline       = 5 * time.Second
	adminStaleProbeDeadline    = 250 * time.Millisecond
)

var (
	errAdminConfiguration = errors.New("admin_configuration_rejected")
	errAdminLifecycle     = errors.New("admin_lifecycle_rejected")
)

type adminPeerUID func(*network.UnixConn) (int, error)
type adminHandler func(context.Context, []byte) []byte

type adminFileIdentity struct {
	device uint64
	inode  uint64
	uid    uint32
	mode   uint32
}

func (identity adminFileIdentity) equal(other adminFileIdentity) bool {
	return identity.device == other.device && identity.inode == other.inode && identity.uid == other.uid && identity.mode == other.mode
}

type adminDirectory struct {
	path     string
	fd       int
	identity adminFileIdentity
}

type adminTransport struct {
	mu sync.Mutex

	directory      *adminDirectory
	socketPath     string
	socketIdentity adminFileIdentity
	listener       *network.UnixListener
	expectedUID    int
	peerUID        adminPeerUID
	handler        adminHandler
	ctx            context.Context
	cancel         context.CancelFunc
	connections    map[*network.UnixConn]struct{}
	semaphore      chan struct{}
	wait           sync.WaitGroup
	closeOnce      sync.Once
	closeErr       error
}

func Start(ctx context.Context, runtimeDir string, handler func(context.Context, []byte) []byte) (*adminTransport, error) {
	return startAdminTransport(ctx, runtimeDir, os.Geteuid(), nativeAdminPeerUID, handler)
}

func startAdminTransport(parent context.Context, runtimeDir string, expectedUID int, peerUID adminPeerUID, handler adminHandler) (*adminTransport, error) {
	if parent == nil {
		parent = context.Background()
	}
	if err := parent.Err(); err != nil {
		return nil, errAdminConfiguration
	}
	if expectedUID != os.Geteuid() || peerUID == nil || handler == nil {
		return nil, errAdminConfiguration
	}
	directory, err := openAdminDirectory(runtimeDir, expectedUID)
	if err != nil {
		return nil, err
	}
	keepDirectory := false
	defer func() {
		if !keepDirectory {
			_ = unix.Close(directory.fd)
		}
	}()

	socketPath := filepath.Join(directory.path, adminSocketName)
	if err := prepareAdminSocket(directory, socketPath, expectedUID); err != nil {
		return nil, err
	}
	if err := verifyAdminDirectory(directory, expectedUID); err != nil {
		return nil, err
	}
	listener, err := network.ListenUnix("unix", &network.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		return nil, errAdminLifecycle
	}
	listener.SetUnlinkOnClose(false)
	if err := unix.Fchmodat(directory.fd, adminSocketName, 0o600, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		_ = listener.Close()
		_ = removeUnclaimedAdminSocket(directory, expectedUID)
		return nil, errAdminLifecycle
	}
	identity, err := inspectAdminSocket(directory, expectedUID, 0o600)
	if err != nil || verifyAdminDirectory(directory, expectedUID) != nil {
		_ = listener.Close()
		_ = removeUnclaimedAdminSocket(directory, expectedUID)
		return nil, errAdminLifecycle
	}

	ctx, cancel := context.WithCancel(parent)
	transport := &adminTransport{
		directory:      directory,
		socketPath:     socketPath,
		socketIdentity: identity,
		listener:       listener,
		expectedUID:    expectedUID,
		peerUID:        peerUID,
		handler:        handler,
		ctx:            ctx,
		cancel:         cancel,
		connections:    make(map[*network.UnixConn]struct{}),
		semaphore:      make(chan struct{}, maxAdminConnections),
	}
	keepDirectory = true
	transport.wait.Add(1)
	go transport.accept()
	go func() {
		<-ctx.Done()
		_ = transport.close()
	}()
	return transport, nil
}

func (transport *adminTransport) address() string {
	if transport == nil {
		return ""
	}
	return transport.socketPath
}

func (transport *adminTransport) Address() string { return transport.address() }

func (transport *adminTransport) Close() error { return transport.close() }

func (transport *adminTransport) close() error {
	if transport == nil {
		return nil
	}
	transport.closeOnce.Do(func() {
		transport.cancel()
		if transport.listener != nil {
			_ = transport.listener.Close()
		}
		transport.mu.Lock()
		for connection := range transport.connections {
			_ = connection.Close()
		}
		transport.mu.Unlock()
		transport.wait.Wait()
		if err := verifyAdminDirectory(transport.directory, transport.expectedUID); err != nil {
			transport.closeErr = errAdminLifecycle
		} else {
			identity, err := inspectAdminSocket(transport.directory, transport.expectedUID, 0o600)
			if err != nil || !identity.equal(transport.socketIdentity) {
				transport.closeErr = errAdminLifecycle
			} else if err := unix.Unlinkat(transport.directory.fd, adminSocketName, 0); err != nil {
				transport.closeErr = errAdminLifecycle
			}
		}
		if err := unix.Close(transport.directory.fd); err != nil && transport.closeErr == nil {
			transport.closeErr = errAdminLifecycle
		}
	})
	return transport.closeErr
}

func (transport *adminTransport) accept() {
	defer transport.wait.Done()
	for {
		connection, err := transport.listener.AcceptUnix()
		if err != nil {
			return
		}
		select {
		case transport.semaphore <- struct{}{}:
		default:
			_ = connection.Close()
			continue
		}
		transport.mu.Lock()
		if transport.ctx.Err() != nil {
			transport.mu.Unlock()
			<-transport.semaphore
			_ = connection.Close()
			return
		}
		transport.connections[connection] = struct{}{}
		transport.wait.Add(1)
		transport.mu.Unlock()
		go transport.serve(connection)
	}
}

func (transport *adminTransport) serve(connection *network.UnixConn) {
	defer transport.wait.Done()
	defer func() {
		transport.mu.Lock()
		delete(transport.connections, connection)
		transport.mu.Unlock()
		<-transport.semaphore
		_ = connection.Close()
	}()

	firstUID, err := transport.peerUID(connection)
	if err != nil || firstUID != transport.expectedUID {
		return
	}
	secondUID, err := transport.peerUID(connection)
	if err != nil || secondUID != firstUID {
		return
	}

	for command := 0; command < maxAdminCommandsPerSession; command++ {
		if err := connection.SetReadDeadline(time.Now().Add(adminIODeadline)); err != nil {
			return
		}
		request, err := readAdminFrame(connection)
		if err != nil {
			return
		}
		handlerContext, cancel := context.WithTimeout(transport.ctx, adminHandlerDeadline)
		response := transport.handler(handlerContext, request)
		cancel()
		clearAdminBytes(request)
		frame, err := encodeAdminFrame(response)
		clearAdminBytes(response)
		if err != nil {
			return
		}
		if err := connection.SetWriteDeadline(time.Now().Add(adminIODeadline)); err != nil {
			clearAdminBytes(frame)
			return
		}
		err = writeAdminFrame(connection, frame)
		clearAdminBytes(frame)
		if err != nil {
			return
		}
	}
}

func openAdminDirectory(path string, expectedUID int) (*adminDirectory, error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) || clean == string(filepath.Separator) {
		return nil, errAdminConfiguration
	}
	components := strings.Split(strings.TrimPrefix(clean, string(filepath.Separator)), string(filepath.Separator))
	root, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errAdminLifecycle
	}
	current := root
	for index, component := range components {
		if component == "" || component == "." || component == ".." {
			_ = unix.Close(current)
			return nil, errAdminConfiguration
		}
		next, openErr := unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if openErr != nil && errors.Is(openErr, syscall.ENOENT) && index == len(components)-1 {
			if mkdirErr := unix.Mkdirat(current, component, 0o700); mkdirErr != nil {
				_ = unix.Close(current)
				return nil, errAdminLifecycle
			}
			next, openErr = unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		}
		_ = unix.Close(current)
		if openErr != nil {
			return nil, errAdminLifecycle
		}
		current = next
	}
	directory := &adminDirectory{path: clean, fd: current}
	var stat unix.Stat_t
	if err := unix.Fstat(current, &stat); err != nil {
		_ = unix.Close(current)
		return nil, errAdminLifecycle
	}
	directory.identity = adminIdentity(stat)
	if err := verifyAdminDirectory(directory, expectedUID); err != nil {
		_ = unix.Close(current)
		return nil, err
	}
	return directory, nil
}

func verifyAdminDirectory(directory *adminDirectory, expectedUID int) error {
	if directory == nil || directory.fd < 0 {
		return errAdminLifecycle
	}
	var descriptorStat unix.Stat_t
	if err := unix.Fstat(directory.fd, &descriptorStat); err != nil {
		return errAdminLifecycle
	}
	descriptor := adminIdentity(descriptorStat)
	if !descriptor.equal(directory.identity) || descriptor.uid != uint32(expectedUID) || descriptor.mode&uint32(unix.S_IFMT) != uint32(unix.S_IFDIR) || descriptor.mode&0o777 != 0o700 {
		return errAdminLifecycle
	}
	var pathStat unix.Stat_t
	if err := unix.Lstat(directory.path, &pathStat); err != nil {
		return errAdminLifecycle
	}
	if !adminIdentity(pathStat).equal(descriptor) {
		return errAdminLifecycle
	}
	return nil
}

func prepareAdminSocket(directory *adminDirectory, path string, expectedUID int) error {
	var initial unix.Stat_t
	err := unix.Fstatat(directory.fd, adminSocketName, &initial, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, syscall.ENOENT) {
		return nil
	}
	if err != nil {
		return errAdminLifecycle
	}
	identity := adminIdentity(initial)
	if identity.uid != uint32(expectedUID) || identity.mode&uint32(unix.S_IFMT) != uint32(unix.S_IFSOCK) {
		return errAdminLifecycle
	}
	connection, dialErr := network.DialTimeout("unix", path, adminStaleProbeDeadline)
	if dialErr == nil {
		_ = connection.Close()
		return errAdminLifecycle
	}
	if !errors.Is(dialErr, syscall.ECONNREFUSED) {
		return errAdminLifecycle
	}
	if err := verifyAdminDirectory(directory, expectedUID); err != nil {
		return err
	}
	var current unix.Stat_t
	if err := unix.Fstatat(directory.fd, adminSocketName, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil || !adminIdentity(current).equal(identity) {
		return errAdminLifecycle
	}
	if err := unix.Unlinkat(directory.fd, adminSocketName, 0); err != nil {
		return errAdminLifecycle
	}
	return nil
}

func inspectAdminSocket(directory *adminDirectory, expectedUID int, permissions uint32) (adminFileIdentity, error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(directory.fd, adminSocketName, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return adminFileIdentity{}, errAdminLifecycle
	}
	identity := adminIdentity(stat)
	if identity.uid != uint32(expectedUID) || identity.mode&uint32(unix.S_IFMT) != uint32(unix.S_IFSOCK) || identity.mode&0o777 != permissions {
		return adminFileIdentity{}, errAdminLifecycle
	}
	return identity, nil
}

func removeUnclaimedAdminSocket(directory *adminDirectory, expectedUID int) error {
	if verifyAdminDirectory(directory, expectedUID) != nil {
		return errAdminLifecycle
	}
	if _, err := inspectAdminSocket(directory, expectedUID, 0o600); err != nil {
		return err
	}
	return unix.Unlinkat(directory.fd, adminSocketName, 0)
}

func adminIdentity(stat unix.Stat_t) adminFileIdentity {
	return adminFileIdentity{device: uint64(stat.Dev), inode: uint64(stat.Ino), uid: stat.Uid, mode: uint32(stat.Mode)}
}

func clearAdminBytes(payload []byte) {
	for index := range payload {
		payload[index] = 0
	}
}

func writeAdminFrame(connection *network.UnixConn, frame []byte) error {
	for len(frame) > 0 {
		written, err := connection.Write(frame)
		if err != nil {
			return err
		}
		if written <= 0 {
			return errAdminLifecycle
		}
		frame = frame[written:]
	}
	return nil
}
