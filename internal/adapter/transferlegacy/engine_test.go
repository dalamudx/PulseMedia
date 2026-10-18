package transfer_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	transfer "github.com/dalamudx/PulseMedia/internal/adapter/transferlegacy"
)

func TestCopyStrategyWithFullSHA256VerificationAndAtomicRename(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "pulsemedia-copy-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() {
		_ = os.RemoveAll(tempDir)
	}()

	sourceFile := filepath.Join(tempDir, "source.mkv")
	payload := []byte("REAL_PAYLOAD_FOR_COPY_SHA256_VERIFICATION_123456789")
	if err := os.WriteFile(sourceFile, payload, 0644); err != nil {
		t.Fatalf("failed to write source: %v", err)
	}

	targetFile := filepath.Join(tempDir, "movies", "Movie (2020)", "Movie (2020).mkv")

	// 1. Execute physical copy
	res, err := transfer.ExecutePhysicalTransfer(sourceFile, targetFile, "copy")
	if err != nil {
		t.Fatalf("copy transfer failed: %v", err)
	}
	if res.Strategy != "copy" || res.SizeBytes != int64(len(payload)) || res.SHA256 == "" {
		t.Fatalf("unexpected copy result: %+v", res)
	}

	// Verify target file exists and temp part file was cleaned up
	if _, err := os.Stat(targetFile); err != nil {
		t.Fatalf("target file must exist after rename: %v", err)
	}
	if _, err := os.Stat(targetFile + ".pm_part"); !os.IsNotExist(err) {
		t.Fatal("temporary .pm_part file must not remain after successful atomic rename")
	}

	// 2. Symlink rejection
	symlinkFile := filepath.Join(tempDir, "symlink.mkv")
	_ = os.Symlink(sourceFile, symlinkFile)
	_, err = transfer.ExecutePhysicalTransfer(symlinkFile, targetFile, "copy")
	if !errors.Is(err, transfer.ErrSymlinkForbidden) {
		t.Fatalf("expected ErrSymlinkForbidden for symlink source: %v", err)
	}
}

func TestSlotLockerRootSerialization(t *testing.T) {
	locker := transfer.NewSlotLocker()
	rootID := uuid.New()

	// 1. Lock root slot
	unlock1 := locker.LockRoot(rootID)

	locked := make(chan bool, 1)
	go func() {
		// Second lock attempt on same root will wait
		unlock2 := locker.LockRoot(rootID)
		locked <- true
		unlock2()
	}()

	select {
	case <-locked:
		t.Fatal("second lock should block while first is held")
	default:
		// expected non-blocking
	}

	// Release first lock
	unlock1()

	// Second lock should now unblock
	select {
	case <-locked:
		// success
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for root slot lock to be acquired")
	}
}
