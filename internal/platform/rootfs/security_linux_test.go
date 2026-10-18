//go:build linux

package rootfs

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRootCloseCannotCloseReusedDescriptor(t *testing.T) {
	path := t.TempDir()
	root, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	fd := root.fd
	root.Close()
	other, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(other) }()
	if other != fd {
		reused, err := unix.FcntlInt(uintptr(other), unix.F_DUPFD_CLOEXEC, fd)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = unix.Close(reused) }()
		if reused != fd {
			t.Skip("descriptor already reused by another runtime operation")
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); root.Close() }()
	}
	wg.Wait()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		t.Fatalf("old capability closed reused descriptor: %v", err)
	}
}

func TestRecycleRejectsNormalizedSourceAliases(t *testing.T) {
	path := t.TempDir()
	if err := os.Mkdir(filepath.Join(path, ".pulsemedia_recycle"), 0700); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(path, ".pulsemedia_recycle", "old.mkv")
	if err := os.WriteFile(original, []byte("seed"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	id, err := root.Inspect(".pulsemedia_recycle/old.mkv")
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{".pulsemedia_recycle/old.mkv", "./.pulsemedia_recycle/old.mkv", ".//.pulsemedia_recycle/old.mkv"} {
		if err := Recycle(t.Context(), root, source, ".pulsemedia_recycle/new.mkv", id); !errors.Is(err, ErrEscape) {
			t.Fatalf("recycled protected source alias: %v", err)
		}
	}
	data, err := os.ReadFile(original)
	if err != nil || string(data) != "seed" {
		t.Fatal("recycled source changed")
	}
	if _, err := os.Stat(filepath.Join(path, ".pulsemedia_recycle", "new.mkv")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unexpected recycle destination")
	}
}

func TestDefaultRootRejectsDataDirectorySymlink(t *testing.T) {
	base, outside := t.TempDir(), t.TempDir()
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(outside, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFilesystem().PrepareDefaultRoot(t.Context(), alias, "movie"); err == nil {
		t.Fatal("bootstrapped through data root symlink")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("bootstrap changed symlink target")
	}
}
