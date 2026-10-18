//go:build linux

package rootfs_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
)

func TestRecyclePreservesSeedAndNeverReplays(t *testing.T) {
	ctx := context.Background()
	dir, seedDir := t.TempDir(), t.TempDir()
	seed := filepath.Join(seedDir, "seed.mkv")
	if err := os.WriteFile(seed, []byte("old video"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(seed, filepath.Join(dir, "old.mkv")); err != nil {
		t.Fatal(err)
	}
	root, err := rootfs.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	old, err := root.Inspect("old.mkv")
	if err != nil {
		t.Fatal(err)
	}
	target := ".pulsemedia_recycle/operation/old.mkv"
	if err = rootfs.Recycle(ctx, root, "old.mkv", target, old); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dir, "old.mkv")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old library path remains", err)
	}
	recycled, err := os.Stat(filepath.Join(dir, target))
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.Stat(seed)
	if err != nil || !os.SameFile(original, recycled) {
		t.Fatal("seed identity changed", err)
	}
	if err = rootfs.Recycle(ctx, root, "old.mkv", target, old); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("replayed missing source", err)
	}
}

func TestRecycleRejectsConflictsEscapeReplacementAndCancellation(t *testing.T) {
	for _, test := range []string{"target conflict", "source symlink", "parent symlink", "target escape", "source replacement", "cancelled"} {
		t.Run(test, func(t *testing.T) {
			dir, outside := t.TempDir(), t.TempDir()
			src := filepath.Join(dir, "old.mkv")
			if err := os.WriteFile(src, []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			root, err := rootfs.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			id, err := root.Inspect("old.mkv")
			if err != nil {
				t.Fatal(err)
			}
			target := ".pulsemedia_recycle/operation/old.mkv"
			ctx := context.Background()
			switch test {
			case "target conflict":
				if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, target)), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, target), []byte("protected"), 0600); err != nil {
					t.Fatal(err)
				}
			case "source symlink":
				if err := os.Remove(src); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(outside, "video"), src); err != nil {
					t.Fatal(err)
				}
			case "parent symlink":
				if err := os.Symlink(outside, filepath.Join(dir, ".pulsemedia_recycle")); err != nil {
					t.Fatal(err)
				}
			case "target escape":
				target = ".pulsemedia_recycle/../../escaped.mkv"
			case "source replacement":
				if err := os.WriteFile(src, []byte("a different size"), 0600); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if err := rootfs.Recycle(ctx, root, "old.mkv", target, id); err == nil {
				t.Fatal("unsafe recycle accepted")
			}
			if _, err := os.Lstat(src); err != nil {
				t.Fatal("source removed by rejection", err)
			}
			if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
				t.Fatal("outside changed", entries, err)
			}
			if test == "target conflict" {
				body, err := os.ReadFile(filepath.Join(dir, target))
				if err != nil || string(body) != "protected" {
					t.Fatal("target overwritten", err)
				}
			}
		})
	}
}
