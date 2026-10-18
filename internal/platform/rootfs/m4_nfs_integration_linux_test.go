//go:build linux

package rootfs_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"golang.org/x/sys/unix"
)

// TestM4NFSProductionRootfsSmoke is a prerequisite for protocol fault tests.
// It refuses local fixtures. It does not establish App/database missing-file
// protection or behavior during disconnected/blocked NFS requests.
func TestM4NFSProductionRootfsSmoke(t *testing.T) {
	mount := os.Getenv("PULSE_M4_NFS_ROOT")
	if mount == "" {
		t.Skip("requires an owned real NFS mount in an isolated client container")
	}
	mount = filepath.Clean(mount)
	if !filepath.IsAbs(mount) || !strings.HasPrefix(mount, "/data/temp/") {
		t.Fatal("NFS test root must be an absolute owned path below /data/temp")
	}
	// Open first to reject symlink components before creating any fixture.
	root, err := rootfs.Open(mount)
	if err != nil {
		t.Fatal(err)
	}
	root.Close()
	var fs unix.Statfs_t
	if err := unix.Statfs(mount, &fs); err != nil {
		t.Fatal(err)
	}
	if fs.Type != unix.NFS_SUPER_MAGIC {
		t.Fatalf("real kernel NFS required; filesystem type=%#x", fs.Type)
	}
	path, err := os.MkdirTemp(mount, "pulse-m4-nfs-rootfs-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(path); err != nil {
			t.Errorf("remove owned NFS fixture %s: %v", path, err)
		}
	})
	for _, name := range []string{".pulsemedia_root", "Movie.mkv"} {
		if err := os.WriteFile(filepath.Join(path, name), []byte("owned M4 NFS smoke input"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	files := rootfs.NewFilesystem()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	roots, release, err := files.LockRoots(ctx, path)
	if err != nil {
		t.Fatalf("production directory lock is incompatible with this NFS mount: %v", err)
	}
	defer release()
	capability := roots[path]
	if _, err := capability.Inspect(ctx, ".pulsemedia_root"); err != nil {
		t.Fatal(err)
	}
	var observed []string
	if err := files.Walk(ctx, capability, func(rel string, _ ports.FileIdentity) error {
		observed = append(observed, rel)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(observed) != 1 || observed[0] != "Movie.mkv" {
		t.Fatalf("production scan walk returned %v", observed)
	}
	frozen, err := capability.Inspect(ctx, "Movie.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if err := files.Hardlink(ctx, capability, "Movie.mkv", capability, "organized/Movie.mkv", frozen); err != nil {
		t.Fatalf("production NFS hardlink: %v", err)
	}
	linked, err := capability.Inspect(ctx, "organized/Movie.mkv")
	if err != nil || linked != frozen {
		t.Fatalf("linked identity=%+v expected=%+v error=%v", linked, frozen, err)
	}
	// Cancellation cannot release the acquired kernel slot. This is only a
	// mount compatibility check; blocked-I/O evidence needs a server fault.
	cancel()
	contender, stop := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer stop()
	_, otherRelease, err := files.LockRoots(contender, path)
	if otherRelease != nil {
		otherRelease()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contender entered before explicit release: %v", err)
	}
	release()
	after, stopAfter := context.WithTimeout(context.Background(), time.Second)
	defer stopAfter()
	_, finalRelease, err := files.LockRoots(after, path)
	if err != nil {
		t.Fatalf("physical slot did not recover after release: %v", err)
	}
	finalRelease()
}
