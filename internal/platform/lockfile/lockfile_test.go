package lockfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDataDirLock(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "pulsemedia-lock-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() {
		_ = os.RemoveAll(tempDir)
	}()

	dataDir := filepath.Join(tempDir, "data")

	// 1. Acquire initial lock
	lock1, err := Acquire(dataDir)
	if err != nil {
		t.Fatalf("expected successful lock acquisition, got: %v", err)
	}

	// Verify lock file exists
	lockFilePath := filepath.Join(dataDir, "pulsemedia.lock")
	if _, err := os.Stat(lockFilePath); os.IsNotExist(err) {
		t.Fatalf("expected lock file to exist at %s", lockFilePath)
	}

	// 2. Second attempt should fail with ErrAlreadyLocked
	lock2, err := Acquire(dataDir)
	if err == nil {
		_ = lock2.Release()
		t.Fatal("expected second acquire to fail while lock1 is held")
	}
	if !errors.Is(err, ErrAlreadyLocked) {
		t.Fatalf("expected ErrAlreadyLocked, got: %v", err)
	}

	// 3. Release first lock
	if err := lock1.Release(); err != nil {
		t.Fatalf("failed to release lock1: %v", err)
	}

	// 4. Third attempt should succeed after lock1 released
	lock3, err := Acquire(dataDir)
	if err != nil {
		t.Fatalf("expected successful re-acquisition after release, got: %v", err)
	}
	_ = lock3.Release()
}

func TestDataDirLock_EmptyPath(t *testing.T) {
	_, err := Acquire("")
	if err == nil {
		t.Fatal("expected error for empty data dir path")
	}
}

func TestConcurrentReleasePreservesLockInodeAndAllowsNextOwner(t *testing.T) {
	dir := t.TempDir()
	held, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(held.path)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 16)
	for i := 0; i < 16; i++ {
		go func() { results <- held.Release() }()
	}
	for i := 0; i < 16; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	next, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = next.Release() }()
	after, err := os.Stat(next.path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("release changed the stable lock inode")
	}
	other, err := Acquire(dir)
	if other != nil {
		_ = other.Release()
	}
	if !errors.Is(err, ErrAlreadyLocked) {
		t.Fatal("next owner not protected")
	}
}
