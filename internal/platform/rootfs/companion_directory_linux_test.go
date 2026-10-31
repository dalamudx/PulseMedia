//go:build linux

package rootfs_test

import (
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"os"
	"path/filepath"
	"testing"
)

func TestCompanionDirectoryConfinedCompleteAndRepeatable(t *testing.T) {
	fs := rootfs.NewFilesystem()
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Movie.mkv", "Movie.zh.srt", "Movie.ass", ".hidden.srt", "nested/Movie.en.srt"} {
		if err := os.WriteFile(filepath.Join(base, name), []byte("owned"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("Movie.zh.srt", filepath.Join(base, "Movie.en.srt")); err != nil {
		t.Fatal(err)
	}
	root, err := fs.OpenRoot(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for i := 0; i < 2; i++ {
		items, e := fs.ListCompanionDirectory(t.Context(), root, ".")
		if e != nil || len(items) != 3 {
			t.Fatalf("same-directory regular files pass %d: %d %v", i, len(items), e)
		}
	}
	items, err := fs.ListCompanionDirectory(t.Context(), root, "nested")
	if err != nil || len(items) != 1 || items[0].RelativePath != "nested/Movie.en.srt" {
		t.Fatalf("nested scope: %+v %v", items, err)
	}
	if _, err = fs.ListCompanionDirectory(t.Context(), root, "../outside"); err == nil {
		t.Fatal("escaped pinned root")
	}
	if err = os.Symlink("nested", filepath.Join(base, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err = fs.ListCompanionDirectory(t.Context(), root, "alias"); err == nil {
		t.Fatal("followed symlink directory")
	}
}
func TestCompanionDirectoryRejectsTruncatedInventory(t *testing.T) {
	fs := rootfs.NewFilesystem()
	base := t.TempDir()
	for i := 0; i < 4097; i++ {
		f, err := os.CreateTemp(base, "owned-")
		if err != nil {
			t.Fatal(err)
		}
		if err = f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	root, err := fs.OpenRoot(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	items, err := fs.ListCompanionDirectory(t.Context(), root, ".")
	if err == nil || items != nil {
		t.Fatal("partial directory returned as complete")
	}
}
