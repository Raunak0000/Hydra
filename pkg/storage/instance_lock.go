package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ErrDaemonAlreadyRunning indicates another hydra-daemon instance holds the lock.
var ErrDaemonAlreadyRunning = errors.New("hydra daemon is already running")

// InstanceLock represents an exclusive advisory flock held by the running daemon.
type InstanceLock struct {
	file *os.File
	path string
}

// GetLockPath returns the per-user runtime path for the daemon instance lock.
func GetLockPath() string {
	if runtimeDir := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR")); runtimeDir != "" {
		if info, err := os.Stat(runtimeDir); err == nil && info.IsDir() {
			return filepath.Join(runtimeDir, "hydra.lock")
		}
	}

	if dataDir, err := GetDataDir(); err == nil {
		return filepath.Join(dataDir, "hydra.lock")
	}

	return fmt.Sprintf("/tmp/hydra-%d.lock", os.Getuid())
}

// AcquireInstanceLock acquires the default per-user daemon instance lock.
func AcquireInstanceLock() (*InstanceLock, error) {
	return AcquireInstanceLockAt(GetLockPath())
}

// AcquireInstanceLockAt acquires a non-blocking exclusive flock on lockPath.
// Returns ErrDaemonAlreadyRunning if another live process currently holds the lock.
func AcquireInstanceLockAt(lockPath string) (*InstanceLock, error) {
	if strings.TrimSpace(lockPath) == "" {
		return nil, fmt.Errorf("lock path cannot be empty")
	}

	if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
		return nil, fmt.Errorf("create lock directory: %w", err)
	}

	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open instance lock %q: %w", lockPath, err)
	}

	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrDaemonAlreadyRunning
		}
		return nil, fmt.Errorf("lock instance file %q: %w", lockPath, err)
	}

	// Best-effort PID recording for diagnostics; lock ownership is enforced via flock.
	if err := file.Truncate(0); err == nil {
		_, _ = file.Seek(0, 0)
		_, _ = fmt.Fprintf(file, "%d\n", os.Getpid())
		_ = file.Sync()
	}

	return &InstanceLock{
		file: file,
		path: lockPath,
	}, nil
}

// Path returns the filesystem path of the acquired lock file.
func (l *InstanceLock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Release removes the lock file and releases the kernel advisory flock.
func (l *InstanceLock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}

	// Remove path before closing so a new waiter cannot race on an unlinked inode.
	removeErr := os.Remove(l.path)
	if errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}

	unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil

	if removeErr != nil {
		return removeErr
	}
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

