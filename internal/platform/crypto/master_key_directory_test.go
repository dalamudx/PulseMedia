package crypto

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMasterKeyRejectsDataDirectorySymlinksWithoutCreatingOrReplacing(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "existing"}[existing], func(t *testing.T) {
			base, outside := t.TempDir(), t.TempDir()
			original := bytes.Repeat([]byte{0x42}, 32)
			keyPath := filepath.Join(outside, "secret.key")
			if existing {
				if err := os.WriteFile(keyPath, original, 0600); err != nil {
					t.Fatal(err)
				}
			}
			alias := filepath.Join(base, "passkey=owned-secret")
			if err := os.Symlink(outside, alias); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{alias, alias + "/new"} {
				key, err := EnsureMasterKey(path, "")
				if !errors.Is(err, ErrMasterKeyState) || len(key) != 0 {
					t.Fatal("accepted symbolic data root")
				}
				if strings.Contains(err.Error(), "owned-secret") {
					t.Fatal("key error disclosed path")
				}
			}
			entries, err := os.ReadDir(outside)
			if err != nil {
				t.Fatal(err)
			}
			if existing {
				got, err := os.ReadFile(keyPath)
				if err != nil || !bytes.Equal(got, original) || len(entries) != 1 {
					t.Fatal("existing key directory changed")
				}
			} else if len(entries) != 0 {
				t.Fatal("initialized a key through symlink")
			}
		})
	}
}
