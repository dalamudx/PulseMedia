package lockfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/rootfile"
)

var (
	ErrAlreadyLocked = errors.New("data directory is already locked by another process")
	ErrLockState     = errors.New("data directory lock is invalid or unavailable")
)

// DataDirLock provides mutual exclusion on a local data directory using file locks.
type DataDirLock struct {
	path string
	file *os.File
	mu   sync.Mutex
}

// Acquire acquires an exclusive non-blocking file lock on the data directory.
func Acquire(dataDir string) (*DataDirLock, error) {
	if dataDir == "" {
		return nil, errors.New("data directory path cannot be empty")
	}

	lockFilePath := filepath.Join(dataDir, "pulsemedia.lock")
	root, err := rootfile.OpenDirectory(dataDir, true)
	if err != nil {
		return nil, fmt.Errorf("failed to open lock directory: %w", ErrLockState)
	}
	defer func() { _ = root.Close() }()
	f, err := rootfile.Open(root, filepath.Base(lockFilePath), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("failed to open data directory lock: %w", ErrLockState)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("failed to inspect data directory lock: %w", ErrLockState)
	}
	if !validLockFile(info) {
		_ = f.Close()
		return nil, ErrLockState
	}

	// Try non-blocking exclusive flock
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		holderPID := readLockInfo(f)
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			if holderPID != "" {
				return nil, fmt.Errorf("%w (locked by PID %s)", ErrAlreadyLocked, holderPID)
			}
			return nil, ErrAlreadyLocked
		}
		return nil, fmt.Errorf("failed to acquire data directory lock: %w", ErrLockState)
	}

	// Revalidate the descriptor and its filename after acquiring flock, before
	// truncating diagnostics. A substituted name or newly linked inode is unsafe.
	info, err = f.Stat()
	named, nameErr := root.Lstat("pulsemedia.lock")
	if err != nil || nameErr != nil || !validLockFile(info) || !os.SameFile(info, named) {
		_ = f.Close()
		return nil, ErrLockState
	}

	// Persist diagnostics only after validating the inode and acquiring flock.
	lockInfo := fmt.Sprintf("pid=%d\nlocked_at=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
	if err := f.Truncate(0); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("failed to initialize lock diagnostics: %w", ErrLockState)
	}
	if _, err := f.Seek(0, 0); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("failed to initialize lock diagnostics: %w", ErrLockState)
	}
	if _, err := f.WriteString(lockInfo); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("failed to persist lock diagnostics: %w", ErrLockState)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("failed to persist lock diagnostics: %w", ErrLockState)
	}

	return &DataDirLock{
		path: lockFilePath,
		file: f,
	}, nil
}

// Release releases the file lock and closes the lock file.
func (l *DataDirLock) Release() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	if err := errors.Join(unlockErr, closeErr); err != nil {
		return fmt.Errorf("failed to release data directory lock: %w", ErrLockState)
	}
	return nil
}

func validLockFile(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return info.Mode().IsRegular() && info.Mode().Perm() == 0600 && info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0 && ok && stat.Nlink == 1
}

func readLockInfo(file *os.File) string {
	var buffer [128]byte
	n, _ := file.ReadAt(buffer[:], 0)
	content := buffer[:n]
	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "pid=") {
			pidStr := strings.TrimPrefix(line, "pid=")
			if _, err := strconv.Atoi(pidStr); err == nil {
				return pidStr
			}
		}
	}
	return ""
}
