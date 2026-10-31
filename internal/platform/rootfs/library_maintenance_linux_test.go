//go:build linux

package rootfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func maintenanceNativeFixture(t *testing.T) (*Filesystem, ports.TransferRoot, string, ports.FileIdentity) {
	t.Helper()
	dir := t.TempDir()
	if e := os.WriteFile(filepath.Join(dir, "original.mkv"), []byte("exact old bytes"), 0600); e != nil {
		t.Fatal(e)
	}
	f := NewFilesystem()
	r, e := f.OpenRoot(t.Context(), dir)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(r.Close)
	id, e := r.Inspect(t.Context(), "original.mkv")
	if e != nil {
		t.Fatal(e)
	}
	return f, r, dir, id
}
func TestMaintenanceNativeRenameAndRestore(t *testing.T) {
	f, r, dir, id := maintenanceNativeFixture(t)
	if e := f.MaintenanceRename(t.Context(), r, "original.mkv", "Movies/new.mkv", id); e != nil {
		t.Fatal(e)
	}
	after, e := r.Inspect(t.Context(), "Movies/new.mkv")
	if e != nil || after != id {
		t.Fatal(after, e)
	}
	if e = f.Recycle(t.Context(), r, "Movies/new.mkv", ".pulsemedia_recycle/operation/original.mkv", id); e != nil {
		t.Fatal(e)
	}
	if e = f.MaintenanceRename(t.Context(), r, ".pulsemedia_recycle/operation/original.mkv", "original.mkv", id); e != nil {
		t.Fatal(e)
	}
	data, e := os.ReadFile(filepath.Join(dir, "original.mkv"))
	if e != nil || string(data) != "exact old bytes" {
		t.Fatal(string(data), e)
	}
}
func TestMaintenanceNativeRefusals(t *testing.T) {
	for _, kind := range []string{"collision", "target-symlink", "parent-symlink", "escape", "identity", "cancelled", "directory"} {
		t.Run(kind, func(t *testing.T) {
			f, r, dir, id := maintenanceNativeFixture(t)
			target := "target.mkv"
			ctx := t.Context()
			switch kind {
			case "collision":
				if e := os.WriteFile(filepath.Join(dir, target), []byte("protected"), 0600); e != nil {
					t.Fatal(e)
				}
			case "target-symlink":
				if e := os.Symlink(filepath.Join(dir, "original.mkv"), filepath.Join(dir, target)); e != nil {
					t.Fatal(e)
				}
			case "parent-symlink":
				if e := os.Symlink(t.TempDir(), filepath.Join(dir, "parent")); e != nil {
					t.Fatal(e)
				}
				target = "parent/target.mkv"
			case "escape":
				target = "../escaped.mkv"
			case "identity":
				id.Inode++
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			case "directory":
				if e := os.Mkdir(filepath.Join(dir, target), 0700); e != nil {
					t.Fatal(e)
				}
			}
			if e := f.MaintenanceRename(ctx, r, "original.mkv", target, id); e == nil {
				t.Fatal("accepted", kind)
			}
			data, e := os.ReadFile(filepath.Join(dir, "original.mkv"))
			if e != nil || string(data) != "exact old bytes" {
				t.Fatal("source lost", e)
			}
		})
	}
}
func TestMaintenanceNativeCleanupHardlinks(t *testing.T) {
	f, r, dir, id := maintenanceNativeFixture(t)
	if e := os.Link(filepath.Join(dir, "original.mkv"), filepath.Join(dir, "seeding.mkv")); e != nil {
		t.Fatal(e)
	}
	if e := f.Recycle(t.Context(), r, "original.mkv", ".pulsemedia_recycle/operation/original.mkv", id); e != nil {
		t.Fatal(e)
	}
	obs, e := f.MaintenanceInspect(t.Context(), r, ".pulsemedia_recycle/operation/original.mkv")
	if e != nil || obs.LinkCount != 2 || obs.AllocatedBytes < 0 || obs.Identity != id {
		t.Fatal(obs, e)
	}
	if e = f.MaintenanceRemove(t.Context(), r, ".pulsemedia_recycle/operation/original.mkv", id); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Inspect(t.Context(), ".pulsemedia_recycle/operation/original.mkv"); !errors.Is(e, os.ErrNotExist) {
		t.Fatal(e)
	}
	data, e := os.ReadFile(filepath.Join(dir, "seeding.mkv"))
	if e != nil || string(data) != "exact old bytes" {
		t.Fatal("hardlink changed", e)
	}
}
func TestMaintenanceNativeCleanupRefusal(t *testing.T) {
	f, r, dir, id := maintenanceNativeFixture(t)
	if e := f.MaintenanceRemove(t.Context(), r, "original.mkv", id); e == nil {
		t.Fatal("removed unretired namespace")
	}
	if e := f.Recycle(t.Context(), r, "original.mkv", ".pulsemedia_recycle/operation/original.mkv", id); e != nil {
		t.Fatal(e)
	}
	wrong := id
	wrong.Inode++
	if e := f.MaintenanceRemove(t.Context(), r, ".pulsemedia_recycle/operation/original.mkv", wrong); e == nil {
		t.Fatal("wrong identity removed")
	}
	if _, e := os.Stat(filepath.Join(dir, ".pulsemedia_recycle/operation/original.mkv")); e != nil {
		t.Fatal(e)
	}
}
