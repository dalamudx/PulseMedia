//go:build linux

package rootfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSidecarInspectDirectory(t *testing.T) {
	f := newSidecarFixture(t)
	for _, rel := range []string{".", "Show", "Show/Season"} {
		t.Run(rel, func(t *testing.T) {
			got, err := f.root.InspectSidecarDirectory(t.Context(), rel)
			want := f.destination(t, rel).req.DestinationIdentity
			if err != nil || got != want {
				t.Fatalf("directory identity: %+v want %+v, %v", got, want, err)
			}
		})
	}
	if err := os.Symlink("Season", filepath.Join(f.path, "Show", "alias")); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"", "/Show", "../Show", "Show/../Show", "Show//Season", "Show\\Season", "Show/Season/video.mkv", "absent", "Show/alias"} {
		t.Run("reject_"+rel, func(t *testing.T) {
			got, err := f.root.InspectSidecarDirectory(t.Context(), rel)
			if err == nil || got != (SidecarDirectoryIdentity{}) {
				t.Fatalf("unsafe directory accepted: %+v %v", got, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	got, err := f.root.InspectSidecarDirectory(ctx, "Show")
	if !errors.Is(err, context.Canceled) || got != (SidecarDirectoryIdentity{}) {
		t.Fatalf("canceled observation: %+v %v", got, err)
	}
	f.assertSource(t)
	f.assertNoStages(t)
}

func TestSidecarObservedDirectoryReplacement(t *testing.T) {
	f := newSidecarFixture(t)
	old, err := f.root.InspectSidecarDirectory(t.Context(), f.req.DestinationDirectory)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(f.path, f.req.DestinationDirectory)
	if err := os.Rename(dir, dir+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(dir+"-old", "video.mkv"), filepath.Join(dir, "video.mkv")); err != nil {
		t.Fatal(err)
	}
	fresh, err := f.root.InspectSidecarDirectory(t.Context(), f.req.DestinationDirectory)
	if err != nil || fresh == old {
		t.Fatalf("replacement directory was not distinguishable: %+v %v", fresh, err)
	}
	f.req.DestinationIdentity = old
	out, err := f.root.PublishSidecar(t.Context(), f.req)
	if !errors.Is(err, ErrIdentity) || out.Published {
		t.Fatalf("stale directory observation authorized publication: %+v %v", out, err)
	}
	if _, err := os.Lstat(f.final()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final appeared under replacement directory: %v", err)
	}
	f.assertSource(t)
	f.assertNoStages(t)
}
