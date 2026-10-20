//go:build linux

package rootfs

import (
	"context"
	"errors"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

type foreignSidecarRoot struct{}

func (foreignSidecarRoot) Identity(context.Context) (ports.FileIdentity, error) {
	return ports.FileIdentity{}, nil
}
func (foreignSidecarRoot) Inspect(context.Context, string) (ports.FileIdentity, error) {
	return ports.FileIdentity{}, nil
}
func (foreignSidecarRoot) Close() {}

func TestSidecarFilesystemCapability(t *testing.T) {
	f := newSidecarFixture(t)
	var fs ports.MediaSidecarFilesystem = NewFilesystem()
	root, err := fs.OpenRoot(t.Context(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(root.Close)
	id, err := fs.InspectSidecarDirectory(t.Context(), root, f.req.DestinationDirectory)
	if err != nil || id != f.req.DestinationIdentity {
		t.Fatalf("opaque directory observation: %+v %v", id, err)
	}
	out, err := fs.PublishSidecar(t.Context(), root, f.req)
	if err != nil || out.Disposition != ports.SidecarCreated || !out.Published {
		t.Fatalf("opaque publication: %+v %v", out, err)
	}
	out, err = fs.PublishSidecar(t.Context(), root, f.req)
	if err != nil || out.Disposition != ports.SidecarPreservedRegular || out.Published {
		t.Fatalf("existing entry claimed: %+v %v", out, err)
	}
	f.assertSource(t)
	f.assertNoStages(t)
}

func TestSidecarFilesystemRejectsForeignCapability(t *testing.T) {
	f := newSidecarFixture(t)
	fs := NewFilesystem()
	for _, root := range []ports.TransferRoot{nil, foreignSidecarRoot{}, (*capability)(nil), &capability{}} {
		if _, err := fs.InspectSidecarDirectory(t.Context(), root, "."); !errors.Is(err, ports.ErrFileIdentity) {
			t.Fatalf("foreign directory capability: %v", err)
		}
		out, err := fs.PublishSidecar(t.Context(), root, f.req)
		if !errors.Is(err, ports.ErrFileIdentity) || out.Published {
			t.Fatalf("foreign publication capability: %+v %v", out, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := fs.InspectSidecarDirectory(ctx, nil, "."); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled directory: %v", err)
	}
	out, err := fs.PublishSidecar(ctx, nil, f.req)
	if !errors.Is(err, context.Canceled) || out.Published {
		t.Fatalf("canceled publication: %+v %v", out, err)
	}
	f.assertNoStages(t)
}
