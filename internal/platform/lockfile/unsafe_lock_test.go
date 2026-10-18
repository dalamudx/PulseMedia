package lockfile

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestAcquireRejectsUnsafeExistingLockWithoutChangingOwnedTarget(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "insecure-mode", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			probe, err := Acquire(dir)
			if err != nil {
				t.Fatal(err)
			}
			path := probe.path
			if err := probe.Release(); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			original := []byte("owned-not-a-lock-do-not-overwrite")
			target := filepath.Join(dir, "owned-target")
			if kind != "fifo" {
				if err := os.WriteFile(target, original, 0600); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "symlink":
				err = os.Symlink("owned-target", path)
			case "hardlink":
				err = os.Link(target, path)
			case "insecure-mode":
				err = os.Rename(target, path)
				target = path
				if err == nil {
					err = os.Chmod(path, 0644)
				}
			case "fifo":
				err = syscall.Mkfifo(path, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := Acquire(dir)
			if err == nil {
				_ = got.Release()
				t.Error("unsafe lock was accepted")
			}
			if errors.Is(err, ErrAlreadyLocked) {
				t.Fatal("invalid lock was treated as a legitimate owner")
			}
			if kind != "fifo" {
				data, readErr := os.ReadFile(target)
				if readErr != nil || !bytes.Equal(data, original) {
					t.Error("startup overwrote owned target")
				}
				info, statErr := os.Stat(target)
				want := os.FileMode(0600)
				if kind == "insecure-mode" {
					want = 0644
				}
				if statErr != nil || info.Mode().Perm() != want {
					t.Error("startup changed owned target mode")
				}
			}
		})
	}
}
