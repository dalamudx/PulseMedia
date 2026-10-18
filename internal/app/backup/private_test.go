package backup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateInputs(t *testing.T) {
	dir, e := os.MkdirTemp("/data/temp", "pulse-backup-input-")
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if e := os.RemoveAll(dir); e != nil {
			t.Error(e)
		}
	}()
	path := filepath.Join(dir, "input")
	if e = os.WriteFile(path, []byte("private"), 0600); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(dir, "link")
	if e = os.Symlink("input", link); e != nil {
		t.Fatal(e)
	}
	if _, e = ReadPrivate(link); e == nil {
		t.Fatal("symlink accepted")
	}
	if e = os.Chmod(path, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e = ReadPrivate(path); e == nil {
		t.Fatal("public evidence accepted")
	}
	if e = os.Chmod(path, 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.Truncate(path, (1<<20)+1); e != nil {
		t.Fatal(e)
	}
	if _, e = ReadPrivate(path); e == nil {
		t.Fatal("oversized evidence accepted")
	}
}
