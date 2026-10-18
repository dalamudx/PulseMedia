package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/config"
	systemcrypto "github.com/dalamudx/PulseMedia/internal/platform/crypto"
)

func TestAppRunRejectsInvalidMasterKeyWithoutReplacingIt(t *testing.T) {
	for _, kind := range []string{"corrupt", "insecure-mode", "symlink", "same-root-symlink"} {
		t.Run(kind, func(t *testing.T) {
			cfg := config.NewDefault()
			cfg.DataDir = t.TempDir()
			cfg.Server.HTTPAddr = "127.0.0.1:0"
			path := filepath.Join(cfg.DataDir, "secret.key")
			original := bytes.Repeat([]byte{0x42}, 32)
			mode := os.FileMode(0600)
			if kind == "corrupt" {
				original = []byte("owned-test-invalid-key")
			}
			if kind == "insecure-mode" {
				mode = 0644
			}
			target := path
			switch kind {
			case "symlink":
				target = filepath.Join(t.TempDir(), "owned-key-target")
			case "same-root-symlink":
				target = filepath.Join(cfg.DataDir, "owned-key-target")
			}
			if err := os.WriteFile(target, original, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(target, mode); err != nil {
				t.Fatal(err)
			}
			if kind == "symlink" || kind == "same-root-symlink" {
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			application, err := New(cfg, WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := application.Close(); err != nil {
					t.Error(err)
				}
			})
			if err := application.Run(context.Background()); !errors.Is(err, systemcrypto.ErrMasterKeyState) {
				t.Fatal("invalid key did not reject startup")
			}
			got, err := os.ReadFile(target)
			info, statErr := os.Stat(target)
			if err != nil || statErr != nil || !bytes.Equal(got, original) || info.Mode().Perm() != mode {
				t.Fatal("startup changed existing key bytes or mode")
			}
			if application.Readyz(context.Background()).Ready {
				t.Fatal("failed key initialization still reports ready")
			}
		})
	}
}
