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

func TestVerifiedTransferReadOnlyPostconditions(t *testing.T) {
	for _, action := range []string{"copy", "move"} {
		t.Run(action, func(t *testing.T) {
			f := newVerifiedFixture(t, bytes.Repeat([]byte("complete observed media"), 65536))
			key := uuid.New()
			result, err := f.transfer(t.Context(), action, key)
			if err != nil {
				t.Fatal(err)
			}
			observed, err := f.fs.ObserveTransfer(t.Context(), f.roots[f.src], "movie.mkv", f.roots[f.dst], "Movie/video.mkv", f.proof, action, key)
			if err != nil || observed != result {
				t.Fatalf("observe completed action: %+v %v", observed, err)
			}
			if err := os.WriteFile(filepath.Join(f.dst, "Movie/video.mkv"), bytes.Repeat([]byte("x"), len(f.body)), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := f.fs.ObserveTransfer(t.Context(), f.roots[f.src], "movie.mkv", f.roots[f.dst], "Movie/video.mkv", f.proof, action, key); !errors.Is(err, ports.ErrFileConflict) {
				t.Fatalf("equal-size corrupted target accepted: %v", err)
			}
		})
	}
}

func TestVerifiedTransferObservationNeverCompletesMoveCleanup(t *testing.T) {
	f := newVerifiedFixture(t, []byte("complete target does not prove source cleanup"))
	key := uuid.New()
	if _, err := f.transfer(t.Context(), "copy", key); err != nil {
		t.Fatal(err)
	}
	if _, err := f.fs.ObserveTransfer(t.Context(), f.roots[f.src], "movie.mkv", f.roots[f.dst], "Movie/video.mkv", f.proof, "move", key); !errors.Is(err, ports.ErrFileConflict) {
		t.Fatalf("present source accepted: %v", err)
	}
	staged := filepath.Join(f.src, ".pulsemedia_transfers", key.String(), "move-source")
	if err := os.MkdirAll(filepath.Dir(staged), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(f.src, "movie.mkv"), staged); err != nil {
		t.Fatal(err)
	}
	if _, err := f.fs.ObserveTransfer(t.Context(), f.roots[f.src], "movie.mkv", f.roots[f.dst], "Movie/video.mkv", f.proof, "move", key); !errors.Is(err, ports.ErrFileConflict) {
		t.Fatalf("quarantined source accepted: %v", err)
	}
	if data, err := os.ReadFile(staged); err != nil || !bytes.Equal(data, f.body) {
		t.Fatalf("read-only observation changed move source: %v", err)
	}
}
