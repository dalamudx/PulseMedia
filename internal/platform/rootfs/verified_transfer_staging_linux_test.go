//go:build linux

package rootfs_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

func TestVerifiedTransferStageCannotModifySeedingContent(t *testing.T) {
	body := bytes.Repeat([]byte("source and seeding bytes"), 65536)
	f := newVerifiedFixture(t, body)
	key := uuid.New()
	dir := filepath.Join(f.dst, ".pulsemedia_transfers", key.String())
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(f.src, "movie.mkv"), filepath.Join(dir, "content")); err != nil {
		t.Fatal(err)
	}
	result, err := f.transfer(t.Context(), "copy", key)
	if err != nil || result.Target.SHA256 != f.proof.SHA256 {
		t.Fatalf("rebuild aliased stage: %+v %v", result, err)
	}
	data, err := os.ReadFile(filepath.Join(f.src, "movie.mkv"))
	if err != nil || !bytes.Equal(data, body) {
		t.Fatalf("stage rebuild modified source inode: %v", err)
	}
}

func TestVerifiedTransferCannotRemoveItsOwnDestination(t *testing.T) {
	f := newVerifiedFixture(t, []byte("same physical file at both names"))
	target := filepath.Join(f.dst, "Movie/video.mkv")
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(f.src, "movie.mkv"), target); err != nil {
		t.Fatal(err)
	}
	if _, err := f.transfer(t.Context(), "move", uuid.New()); !errors.Is(err, ports.ErrFileConflict) {
		t.Fatalf("source-inode target accepted: %v", err)
	}
	for _, path := range []string{filepath.Join(f.src, "movie.mkv"), target} {
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(data, f.body) {
			t.Fatalf("aliased file changed: %s: %v", path, err)
		}
	}
}
