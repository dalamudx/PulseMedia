package crypto

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMasterKeyRejectsSameRootSymbolicAlias(t *testing.T) {
	dir := t.TempDir()
	original := bytes.Repeat([]byte{0x35}, 32)
	target := filepath.Join(dir, "owned-target")
	if err := os.WriteFile(target, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("owned-target", filepath.Join(dir, "secret.key")); err != nil {
		t.Fatal(err)
	}
	key, err := EnsureMasterKey(dir, "")
	if !errors.Is(err, ErrMasterKeyState) || len(key) != 0 {
		t.Error("same-root symbolic key alias accepted")
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil || !bytes.Equal(data, original) {
		t.Fatal("owned key target modified")
	}
}
