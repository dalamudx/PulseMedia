package rootfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

func TestOpenValidatesFilenameAndDescriptor(t *testing.T) {
	dir := t.TempDir()
	root, err := OpenDirectory(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	for _, name := range []string{"", ".", "..", "../escape", "child/file", "child\\file", "secret\x00suffix"} {
		file, err := Open(root, name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if err == nil {
			_ = file.Close()
			t.Fatalf("accepted invalid filename %q", name)
		}
		if strings.Contains(err.Error(), name) && name != "" && name != "." {
			t.Fatalf("echoed invalid filename %q", name)
		}
	}
	file, err := Open(root, "file", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatalf("descriptor not close-on-exec: %v", err)
	}
	flags, err = unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_NONBLOCK == 0 {
		t.Fatalf("descriptor not nonblocking: %v", err)
	}
	for _, target := range []string{"file", filepath.Join(t.TempDir(), "missing")} {
		if err := os.Symlink(target, filepath.Join(dir, "alias")); err != nil {
			t.Fatal(err)
		}
		if f, err := Open(root, "alias", os.O_RDONLY, 0); err == nil {
			_ = f.Close()
			t.Fatal("followed a leaf symlink")
		}
		if err := os.Remove(filepath.Join(dir, "alias")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenDirectoryRejectsAliasesAndSpecialFilesWithoutMutation(t *testing.T) {
	base, outside := t.TempDir(), t.TempDir()
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(outside, alias); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{alias, alias + "/missing/child", alias + "/../other", "", base + "/invalid\x00name"} {
		root, err := OpenDirectory(path, true)
		if root != nil {
			_ = root.Close()
		}
		if !errors.Is(err, ErrDirectory) {
			t.Fatalf("unsafe directory accepted: %v", err)
		}
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("symlink target changed")
	}
	fifo := filepath.Join(base, "fifo")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if root, err := OpenDirectory(fifo, true); err == nil {
		_ = root.Close()
		t.Fatal("accepted FIFO directory")
	}
	path := filepath.Join(base, "new", "nested")
	root, err := OpenDirectory(path, true)
	if err != nil {
		t.Fatal(err)
	}
	_ = root.Close()
	for _, p := range []string{filepath.Dir(path), path} {
		info, err := os.Stat(p)
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatalf("new directory permissions: %v", err)
		}
	}
	if err := os.Chmod(path, 0750); err != nil {
		t.Fatal(err)
	}
	root, err = OpenDirectory(path, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = root.Close()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0750 {
		t.Fatal("existing directory permissions changed")
	}
	t.Chdir(base)
	root, err = OpenDirectory("new/../new/nested", false)
	if err != nil {
		t.Fatal(err)
	}
	_ = root.Close()
}

func TestOpenDirectoryPinsIdentityDuringSymlinkSwaps(t *testing.T) {
	base, outside := t.TempDir(), t.TempDir()
	path, alias := filepath.Join(base, "data"), filepath.Join(base, "alias")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, alias); err != nil {
		t.Fatal(err)
	}
	want, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := unix.Renameat2(unix.AT_FDCWD, path, unix.AT_FDCWD, alias, unix.RENAME_EXCHANGE); err != nil {
				t.Errorf("swap: %v", err)
				return
			}
		}
	}()
	defer func() { close(stop); wg.Wait() }()
	for i := 0; i < 300; i++ {
		root, err := OpenDirectory(path, false)
		if err != nil {
			if !errors.Is(err, ErrDirectory) {
				t.Fatal(err)
			}
			continue
		}
		got, err := root.Stat(".")
		_ = root.Close()
		if err != nil || !os.SameFile(want, got) {
			t.Fatal("opened a symlink's replacement directory")
		}
	}
}

func TestOpenDirectoryCapabilitySurvivesPathReplacement(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "data")
	root, err := OpenDirectory(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	original := filepath.Join(base, "original")
	if err := os.Rename(path, original); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	file, err := Open(root, "owned", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if _, err := os.Stat(filepath.Join(original, "owned")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(path, "owned")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("write escaped pinned root")
	}
}
