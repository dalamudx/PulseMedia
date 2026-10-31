package postgres

import (
	"github.com/dalamudx/PulseMedia/internal/usecase/librarymaintenance"
	"os"
	"path/filepath"
	"testing"
)

func TestLibraryMaintenanceReplacedRootPG(t *testing.T) {
	f := newMaintenanceFixture(t)
	command := f.preview(t)
	moved := f.dir + "-original-root"
	if err := os.Rename(f.dir, moved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(moved); err != nil {
			t.Error(err)
		}
	})
	if err := os.Mkdir(f.dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{".pulsemedia_root": "replacement-marker", "original.mkv": "protected replacement-root bytes"} {
		if err := os.WriteFile(filepath.Join(f.dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := librarymaintenance.NewService(f.store).Command(t.Context(), f.actor, command, "replaced-root"); err == nil {
		t.Fatal("rebound scan generation to replacement physical root")
	}
	if f.files.calls.Load() != 0 {
		t.Fatal("physical mutation called on replacement root")
	}
	for p, want := range map[string]string{filepath.Join(moved, "original.mkv"): "unchanged media bytes", filepath.Join(f.dir, "original.mkv"): "protected replacement-root bytes"} {
		got, err := os.ReadFile(p)
		if err != nil || string(got) != want {
			t.Fatal("changed bytes", p, err)
		}
	}
}
