package lockfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestLockRejectsDataDirectorySymlinksAndRedactsPaths(t *testing.T) {
	base, outside := t.TempDir(), t.TempDir()
	alias := filepath.Join(base, "passkey=owned-secret")
	if err := os.Symlink(outside, alias); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{alias, alias + "/new"} {
		lock, err := Acquire(path)
		if lock != nil {
			_ = lock.Release()
		}
		if !errors.Is(err, ErrLockState) {
			t.Fatalf("accepted unsafe root: %v", err)
		}
		if strings.Contains(err.Error(), "owned-secret") {
			t.Fatal("error disclosed path")
		}
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("created lock through directory alias")
	}
	path := filepath.Join(base, "token=owned-secret")
	lock, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Release() }()
	other, err := Acquire(path)
	if other != nil {
		_ = other.Release()
	}
	if !errors.Is(err, ErrAlreadyLocked) || strings.Contains(err.Error(), "owned-secret") {
		t.Fatalf("contention secrecy: %v", err)
	}
}

func TestConcurrentLockReleaseCannotReleaseNextOwner(t *testing.T) {
	path := t.TempDir()
	first, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Release() }()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := first.Release(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	third, err := Acquire(path)
	if third != nil {
		_ = third.Release()
	}
	if !errors.Is(err, ErrAlreadyLocked) {
		t.Fatal("old owner released the new owner's lock")
	}
}
