package crypto

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
)

func TestExistingInvalidMasterKeyIsNeverReplaced(t *testing.T) {
	for _, size := range []int{0, 7, 31, 33, 2048} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "secret.key")
			original := bytes.Repeat([]byte{0x42}, size)
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			key, err := EnsureMasterKey(dir, "")
			got, readErr := os.ReadFile(path)
			if err == nil || len(key) != 0 {
				t.Error("invalid existing key accepted or replaced")
			}
			if readErr != nil || !bytes.Equal(got, original) {
				t.Fatal("existing protected file changed")
			}
		})
	}
}

func TestMasterKeyRejectsSymlinksAndUnsafePermissions(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(t.TempDir(), "owned-test-key")
		original := bytes.Repeat([]byte{0x42}, 32)
		if err := os.WriteFile(target, original, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, "secret.key")); err != nil {
			t.Fatal(err)
		}
		if key, err := EnsureMasterKey(dir, ""); err == nil || len(key) != 0 {
			t.Fatal("symbolic key link accepted")
		}
		got, err := os.ReadFile(target)
		if err != nil || !bytes.Equal(got, original) {
			t.Fatal("owned test target modified")
		}
	})
	for _, mode := range []os.FileMode{0644, 0640, 0660, 0700} {
		t.Run(mode.String(), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "secret.key")
			original := bytes.Repeat([]byte{0x42}, 32)
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}
			if key, err := EnsureMasterKey(dir, ""); err == nil || len(key) != 0 {
				t.Error("unsafe key mode accepted")
			}
			got, err := os.ReadFile(path)
			info, statErr := os.Stat(path)
			if err != nil || statErr != nil || !bytes.Equal(got, original) || info.Mode().Perm() != mode {
				t.Fatal("existing key changed")
			}
		})
	}
}

func TestConcurrentMasterKeyCreationReturnsOneDurableKey(t *testing.T) {
	dir := t.TempDir()
	start := make(chan struct{})
	keys := make([][]byte, 16)
	errs := make([]error, 16)
	var wg sync.WaitGroup
	for i := range keys {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; keys[i], errs[i] = EnsureMasterKey(dir, "") }()
	}
	close(start)
	wg.Wait()
	saved, err := os.ReadFile(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	for i := range keys {
		if errs[i] != nil || len(keys[i]) != 32 || !bytes.Equal(keys[i], saved) {
			t.Fatal("concurrent caller received a different or non-durable key")
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatal("secret staging file left behind")
	}
	encrypted, err := Encrypt(keys[0], []byte("owned-test-credential"))
	if err != nil {
		t.Fatal(err)
	}
	reread, err := EnsureMasterKey(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := Decrypt(reread, encrypted)
	if err != nil || string(plain) != "owned-test-credential" {
		t.Fatal("restart cannot decrypt original ciphertext")
	}
}

func TestMasterKeyRejectsNonRegularFilesWithoutBlocking(t *testing.T) {
	for _, kind := range []string{"directory", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "secret.key")
			if kind == "directory" {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := syscall.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if key, err := EnsureMasterKey(dir, ""); !errors.Is(err, ErrMasterKeyState) || len(key) != 0 {
				t.Fatal("non-regular key accepted")
			}
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal("original key entry removed")
			}
			if kind == "directory" && !info.IsDir() || kind == "fifo" && info.Mode()&os.ModeNamedPipe == 0 {
				t.Fatal("original special entry changed")
			}
		})
	}
}
