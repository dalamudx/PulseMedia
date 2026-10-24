//go:build linux

package rootfs

import (
	"bytes"
	"errors"
	"os"
	"testing"
)

func TestSidecarArtworkPreservesExistingEntries(t *testing.T) {
	for _, name := range []string{"logo.png", "clearart.png", "banner.jpg", "disc.png"} {
		for _, kind := range []string{"regular", "symlink", "directory"} {
			t.Run(name+"/"+kind, func(t *testing.T) {
				f := newSidecarFixture(t)
				f.req.Name = name
				var err error
				switch kind {
				case "regular":
					err = os.WriteFile(f.final(), []byte("owned artwork"), 0600)
				case "symlink":
					err = os.Symlink("video.mkv", f.final())
				case "directory":
					err = os.Mkdir(f.final(), 0700)
				}
				if err != nil {
					t.Fatal(err)
				}
				before, err := os.Lstat(f.final())
				if err != nil {
					t.Fatal(err)
				}
				out, err := f.root.PublishSidecar(t.Context(), f.req)
				if out.Published {
					t.Fatal("existing entry overwritten")
				}
				if kind == "regular" {
					if err != nil || out.Disposition != SidecarPreservedRegular {
						t.Fatal(out, err)
					}
					raw, e := os.ReadFile(f.final())
					if e != nil || !bytes.Equal(raw, []byte("owned artwork")) {
						t.Fatal("existing bytes changed")
					}
				} else if !errors.Is(err, ErrConflict) || out.Disposition != SidecarUnsafeExisting {
					t.Fatal("unsafe entry accepted", out, err)
				}
				after, err := os.Lstat(f.final())
				if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
					t.Fatal("entry identity changed")
				}
				f.assertSource(t)
				f.assertNoStages(t)
			})
		}
	}
}
