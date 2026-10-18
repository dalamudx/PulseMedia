//go:build linux

package rootfs

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestNativeSourceRejectsSpecialFilesAndPreservesHardlinks(t *testing.T) {
	path := t.TempDir()
	if err := unix.Mkfifo(filepath.Join(path, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(path, "directory"), 0700); err != nil {
		t.Fatal(err)
	}
	// Unix socket addresses are bounded independently of filesystem paths.
	t.Chdir(path)
	listener, err := net.Listen("unix", "socket")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	root, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, name := range []string{"fifo", "directory", "socket"} {
		if _, err := root.Inspect(name); err == nil {
			t.Fatalf("accepted special source %s", name)
		}
		if err := Hardlink(t.Context(), root, name, root, name+".target", Identity{}); err == nil {
			t.Fatalf("linked special source %s", name)
		}
		if _, err := os.Lstat(filepath.Join(path, name+".target")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("published special-file target")
		}
	}
	seed, alias := filepath.Join(path, "seed"), filepath.Join(path, "alias")
	if err := os.WriteFile(seed, []byte("seeding media"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(seed, alias); err != nil {
		t.Fatal(err)
	}
	id, err := root.Inspect("alias")
	if err != nil {
		t.Fatal(err)
	}
	if err := Hardlink(t.Context(), root, "alias", root, "library/movie.mkv", id); err != nil {
		t.Fatal(err)
	}
	seedInfo, err := os.Stat(seed)
	if err != nil {
		t.Fatal(err)
	}
	targetInfo, err := os.Stat(filepath.Join(path, "library/movie.mkv"))
	if err != nil || !os.SameFile(seedInfo, targetInfo) {
		t.Fatal("lost default hardlink semantics")
	}
}
