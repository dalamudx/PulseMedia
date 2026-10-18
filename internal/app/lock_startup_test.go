package app

import (
	"bytes"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/platform/lockfile"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestAppNewRejectsUnsafeLockBeforeKeyInitialization(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "insecure-mode", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			cfg := config.NewDefault()
			cfg.DataDir = t.TempDir()
			cfg.Server.HTTPAddr = "127.0.0.1:0"
			probe, err := lockfile.Acquire(cfg.DataDir)
			if err != nil {
				t.Fatal(err)
			}
			if err = probe.Release(); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(cfg.DataDir)
			if err != nil || len(entries) != 1 {
				t.Fatal("missing owned lock fixture")
			}
			path := filepath.Join(cfg.DataDir, entries[0].Name())
			if err = os.Remove(path); err != nil {
				t.Fatal(err)
			}
			original := []byte("owned-target-do-not-overwrite")
			target := filepath.Join(cfg.DataDir, "owned-target")
			if kind != "fifo" {
				if err = os.WriteFile(target, original, 0600); err != nil {
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
			application, err := New(cfg, WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
			if application != nil {
				_ = application.Close()
			}
			if err == nil {
				t.Error("unsafe lock admitted at real startup")
			}
			if _, err = os.Lstat(filepath.Join(cfg.DataDir, "secret.key")); !os.IsNotExist(err) {
				t.Fatal("rejected lock initialized a master key")
			}
			if kind != "fifo" {
				got, err := os.ReadFile(target)
				if err != nil || !bytes.Equal(got, original) {
					t.Fatal("startup overwrote protected test target")
				}
			}
		})
	}
}
