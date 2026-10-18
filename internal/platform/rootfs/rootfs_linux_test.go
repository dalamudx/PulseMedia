//go:build linux

package rootfs_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
)

func TestCapabilityRejectsEscapeSymlinkAndConflicts(t *testing.T) {
	ctx := context.Background()
	srcPath, dstPath, outside := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(srcPath, "movie.mkv"), []byte("video"), 0600); err != nil {
		t.Fatal(err)
	}
	src, err := rootfs.Open(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := rootfs.Open(dstPath)
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	id, err := src.Inspect("movie.mkv")
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"../escape", "/absolute", "safe/../../escape"} {
		if err := rootfs.Hardlink(ctx, src, "movie.mkv", dst, rel, id); !errors.Is(err, rootfs.ErrEscape) {
			t.Fatalf("escape %s: %v", rel, err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(dstPath, "nested")); err != nil {
		t.Fatal(err)
	}
	if err := rootfs.Hardlink(ctx, src, "movie.mkv", dst, "nested/escape.mkv", id); !errors.Is(err, rootfs.ErrSymlink) {
		t.Fatalf("target parent symlink: %v", err)
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("outside changed: %v %v", entries, err)
	}
	if err := os.Symlink(srcPath, filepath.Join(srcPath, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Inspect("alias/movie.mkv"); !errors.Is(err, rootfs.ErrSymlink) {
		t.Fatalf("source parent symlink: %v", err)
	}
	if _, err := rootfs.Open(filepath.Join(srcPath, "alias")); !errors.Is(err, rootfs.ErrSymlink) {
		t.Fatalf("root symlink: %v", err)
	}
	if err := os.Symlink(filepath.Join(srcPath, "movie.mkv"), filepath.Join(dstPath, "target.mkv")); err != nil {
		t.Fatal(err)
	}
	if err := rootfs.Hardlink(ctx, src, "movie.mkv", dst, "target.mkv", id); !errors.Is(err, rootfs.ErrSymlink) {
		t.Fatalf("leaf symlink: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dstPath, "existing.mkv"), []byte("protected"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := rootfs.Hardlink(ctx, src, "movie.mkv", dst, "existing.mkv", id); !errors.Is(err, rootfs.ErrConflict) {
		t.Fatalf("conflict: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dstPath, "existing.mkv"))
	if string(data) != "protected" {
		t.Fatal("existing content overwritten")
	}
	if err := rootfs.Hardlink(ctx, src, "movie.mkv", dst, "Movie/video.mkv", id); err != nil {
		t.Fatal(err)
	}
	if err := rootfs.Hardlink(ctx, src, "movie.mkv", dst, "Movie/video.mkv", id); err != nil {
		t.Fatalf("observe exact link: %v", err)
	}
}

func TestPhysicalRootLockChild(t *testing.T) {
	path := os.Getenv("PULSE_ROOT_LOCK_TEST")
	if path == "" {
		return
	}
	_, release, err := rootfs.LockRoots(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	fmt.Println("physical-slot-held")
	// Explicit pipe ownership: parent closes stdin to request shutdown, then waits.
	_, _ = bufio.NewReader(os.Stdin).ReadByte()
}

func TestPhysicalSlotSurvivesCallerTimeoutAcrossProcess(t *testing.T) {
	path := t.TempDir()
	childCtx, cancelChild := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelChild()
	cmd := exec.CommandContext(childCtx, os.Args[0], "-test.run=^TestPhysicalRootLockChild$")
	cmd.Env = append(os.Environ(), "PULSE_ROOT_LOCK_TEST="+path)
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = input.Close()
		if err := cmd.Wait(); err != nil {
			t.Errorf("child lock process: %v", err)
		}
	}()
	reader := bufio.NewScanner(output)
	if !reader.Scan() || reader.Text() != "physical-slot-held" {
		t.Fatal("child did not acquire physical slot")
	}
	// An expired logical context cannot release the child's kernel-owned slot.
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
		_, release, err := rootfs.LockRoots(ctx, path)
		cancel()
		if release != nil {
			release()
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("second writer acquired while child alive: %v", err)
		}
	}
}

func TestLockOrderAliasesAndCancellationCleanup(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	_, release, err := rootfs.LockRoots(context.Background(), a, b, a)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	_, other, err := rootfs.LockRoots(ctx, b, a)
	cancel()
	if other != nil {
		other()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("reverse order contender: %v", err)
	}
	release()
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, release, err = rootfs.LockRoots(ctx, b, a)
	if err != nil {
		t.Fatalf("cancel leaked a descriptor/slot: %v", err)
	}
	release()
}
