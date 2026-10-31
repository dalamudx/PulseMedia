//go:build linux

package rootfs_test

import (
	"bytes"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/google/uuid"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompanionNativeFrozenIdentityAndPlayableSuffix(t *testing.T) {
	fs := rootfs.NewFilesystem()
	source, target := t.TempDir(), t.TempDir()
	const body = "1\n00:00:00,000 --> 00:00:02,000\nOwned companion subtitle\n"
	if err := os.WriteFile(filepath.Join(source, "Movie.en.srt"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	roots, release, err := fs.LockRoots(t.Context(), source, target)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	frozen, err := roots[source].Inspect(t.Context(), "Movie.en.srt")
	if err != nil {
		t.Fatal(err)
	}
	// Keep the old inode allocated so replacement cannot accidentally reuse it.
	if err = os.Rename(filepath.Join(source, "Movie.en.srt"), filepath.Join(source, "old.srt")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(source, "Movie.en.srt"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = fs.InspectContent(t.Context(), roots[source], "Movie.en.srt", frozen); err == nil {
		t.Fatal("same-content replacement reused frozen identity")
	}
	current, err := roots[source].Inspect(t.Context(), "Movie.en.srt")
	if err != nil {
		t.Fatal(err)
	}
	proof, err := fs.InspectContent(t.Context(), roots[source], "Movie.en.srt", current)
	if err != nil {
		t.Fatal(err)
	}
	result, err := fs.TransferVerified(t.Context(), roots[source], "Movie.en.srt", roots[target], "Movie (2026)/Movie.en.srt", proof, "copy", uuid.New())
	if err != nil || result.SourceRemoved {
		t.Fatal("native subtitle copy", err)
	}
	destination := filepath.Join(target, "Movie (2026)/Movie.en.srt")
	published, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(published, []byte(body)) {
		t.Fatal("copied subtitle differs", err)
	}
	stat, err := os.Stat(destination)
	if err != nil || stat.Mode().Perm() != 0600 {
		t.Fatal("copied subtitle permissions", err)
	}
	// Optional external parsing is evidence, never a silently skipped test: the native
	// file assertions above are unconditional. Explicit acceptance requires ffprobe.
	if os.Getenv("PULSE_MP20_FFPROBE_ACCEPTANCE") == "1" {
		cmd := exec.CommandContext(t.Context(), "ffprobe", "-v", "error", "-select_streams", "s", "-show_entries", "stream=codec_name", "-of", "default=noprint_wrappers=1:nokey=1", destination)
		output, e := cmd.Output()
		if e != nil || strings.TrimSpace(string(output)) != "subrip" {
			t.Fatal("published companion does not parse as playable SRT", e, string(output))
		}
	}
	if err = fs.Hardlink(t.Context(), roots[source], "Movie.en.srt", roots[target], "Movie (2026)/Movie.en.srt", current); err == nil {
		t.Fatal("hardlink overwrote existing copied subtitle")
	}
	if after, e := os.ReadFile(destination); e != nil || !bytes.Equal(after, published) {
		t.Fatal("existing subtitle changed", e)
	}
}
