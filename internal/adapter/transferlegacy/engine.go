package transfer

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

var (
	ErrSymlinkForbidden    = ports.ErrFileSymlink
	ErrInodeMismatch       = ports.ErrFileIdentity
	ErrHashMismatch        = errors.New("file copy integrity check failed: SHA-256 hash mismatch")
	ErrCrossDeviceHardlink = errors.New("hardlink is impossible across different filesystem devices")
	ErrUnsupportedStrategy = errors.New("unsupported transfer strategy")
)

type ExecutionResult struct {
	SourcePath string
	TargetPath string
	Strategy   string
	SizeBytes  int64
	SHA256     string
}

// SlotLocker manages concurrency slots per library root (1 active transfer per root).
type SlotLocker struct {
	locks map[uuid.UUID]*sync.Mutex
	mu    sync.Mutex
}

func NewSlotLocker() *SlotLocker {
	return &SlotLocker{
		locks: make(map[uuid.UUID]*sync.Mutex),
	}
}

func (l *SlotLocker) LockRoot(rootID uuid.UUID) func() {
	l.mu.Lock()
	m, ok := l.locks[rootID]
	if !ok {
		m = &sync.Mutex{}
		l.locks[rootID] = m
	}
	l.mu.Unlock()

	m.Lock()
	return func() {
		m.Unlock()
	}
}

// ExecutePhysicalTransfer performs the physical file transfer according to strategy with strict post-conditions.
func ExecutePhysicalTransfer(sourcePath, targetPath, strategy string) (*ExecutionResult, error) {
	srcInfo, err := os.Lstat(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("source file unreachable: %w", err)
	}
	if srcInfo.Mode()&os.ModeSymlink != 0 {
		return nil, ErrSymlinkForbidden
	}
	if srcInfo.IsDir() {
		return nil, errors.New("source path cannot be a directory")
	}

	targetDir := filepath.Dir(targetPath)
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create target directory: %w", err)
	}

	switch strategy {
	case "hardlink":
		return executeHardlink(sourcePath, targetPath, srcInfo)
	case "copy":
		return executeCopy(sourcePath, targetPath, srcInfo)
	case "move":
		return executeMove(sourcePath, targetPath, srcInfo)
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedStrategy, strategy)
	}
}

func executeHardlink(sourcePath, targetPath string, srcInfo os.FileInfo) (*ExecutionResult, error) {
	// Pre-condition: verify target dir is on same device
	targetDirInfo, err := os.Stat(filepath.Dir(targetPath))
	if err != nil {
		return nil, fmt.Errorf("target dir unreachable: %w", err)
	}

	srcStat := srcInfo.Sys().(*syscall.Stat_t)
	tgtDirStat := targetDirInfo.Sys().(*syscall.Stat_t)
	if srcStat.Dev != tgtDirStat.Dev {
		return nil, ErrCrossDeviceHardlink
	}

	if err := os.Link(sourcePath, targetPath); err != nil {
		return nil, fmt.Errorf("hardlink failed: %w", err)
	}

	// Post-condition: verify same inode
	tgtInfo, err := os.Stat(targetPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat target after hardlink: %w", err)
	}
	tgtStat := tgtInfo.Sys().(*syscall.Stat_t)
	if srcStat.Ino != tgtStat.Ino {
		return nil, ErrInodeMismatch
	}

	return &ExecutionResult{
		SourcePath: sourcePath,
		TargetPath: targetPath,
		Strategy:   "hardlink",
		SizeBytes:  srcInfo.Size(),
	}, nil
}

func executeCopy(sourcePath, targetPath string, srcInfo os.FileInfo) (*ExecutionResult, error) {
	// Write to temporary staging file first: targetPath + ".pm_part"
	tempPath := targetPath + ".pm_part"

	srcFile, err := os.Open(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open source: %w", err)
	}
	defer func() {
		_ = srcFile.Close()
	}()

	tempFile, err := os.OpenFile(tempPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to create temp part file: %w", err)
	}

	srcHasher := sha256.New()
	writer := io.MultiWriter(tempFile, srcHasher)

	copied, err := io.Copy(writer, srcFile)
	if err != nil {
		_ = tempFile.Close()
		_ = os.Remove(tempPath)
		return nil, fmt.Errorf("failed to copy file bytes: %w", err)
	}

	// Invariant: fsync to guarantee flush to physical disk media before commit
	if err := tempFile.Sync(); err != nil {
		_ = tempFile.Close()
		_ = os.Remove(tempPath)
		return nil, fmt.Errorf("failed to fsync temp file: %w", err)
	}
	_ = tempFile.Close()

	// Invariant: "同大小/部分哈希不能判成功" -> Full SHA-256 validation
	if copied != srcInfo.Size() {
		_ = os.Remove(tempPath)
		return nil, fmt.Errorf("copy size mismatch: copied %d, expected %d", copied, srcInfo.Size())
	}

	// Verify hash of written file
	tgtCheckFile, err := os.Open(tempPath)
	if err != nil {
		_ = os.Remove(tempPath)
		return nil, fmt.Errorf("failed to re-open temp file for verification: %w", err)
	}
	tgtHasher := sha256.New()
	_, _ = io.Copy(tgtHasher, tgtCheckFile)
	_ = tgtCheckFile.Close()

	srcHashStr := hex.EncodeToString(srcHasher.Sum(nil))
	tgtHashStr := hex.EncodeToString(tgtHasher.Sum(nil))
	if srcHashStr != tgtHashStr {
		_ = os.Remove(tempPath)
		return nil, fmt.Errorf("%w: src=%s, tgt=%s", ErrHashMismatch, srcHashStr, tgtHashStr)
	}

	// Atomic rename to final target path
	if err := os.Rename(tempPath, targetPath); err != nil {
		_ = os.Remove(tempPath)
		return nil, fmt.Errorf("failed to rename temp file to target: %w", err)
	}

	return &ExecutionResult{
		SourcePath: sourcePath,
		TargetPath: targetPath,
		Strategy:   "copy",
		SizeBytes:  copied,
		SHA256:     tgtHashStr,
	}, nil
}

func executeMove(sourcePath, targetPath string, srcInfo os.FileInfo) (*ExecutionResult, error) {
	// Try atomic rename first (works on same device)
	if err := os.Rename(sourcePath, targetPath); err == nil {
		return &ExecutionResult{
			SourcePath: sourcePath,
			TargetPath: targetPath,
			Strategy:   "move",
			SizeBytes:  srcInfo.Size(),
		}, nil
	}

	// Cross-device move: copy + verify SHA-256 + remove source
	res, err := executeCopy(sourcePath, targetPath, srcInfo)
	if err != nil {
		return nil, fmt.Errorf("cross-device move copy failed: %w", err)
	}

	if err := os.Remove(sourcePath); err != nil {
		return nil, fmt.Errorf("cross-device move failed to delete source: %w", err)
	}

	res.Strategy = "move"
	return res, nil
}
