//go:build linux

package rootfs

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

type Filesystem struct{}

func NewFilesystem() *Filesystem { return &Filesystem{} }

var _ ports.TransferFilesystem = (*Filesystem)(nil)

type capability struct{ root *Root }

func (c *capability) Identity(ctx context.Context) (ports.FileIdentity, error) {
	if err := ctx.Err(); err != nil {
		return ports.FileIdentity{}, err
	}
	return c.root.Identity()
}
func (c *capability) Inspect(ctx context.Context, rel string) (ports.FileIdentity, error) {
	if err := ctx.Err(); err != nil {
		return ports.FileIdentity{}, err
	}
	return c.root.Inspect(rel)
}
func (c *capability) Close() { c.root.Close() }
func (f *Filesystem) OpenRoot(ctx context.Context, path string) (ports.TransferRoot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := Open(path)
	if err != nil {
		return nil, err
	}
	return &capability{root}, nil
}
func (f *Filesystem) LockRoots(ctx context.Context, paths ...string) (map[string]ports.TransferRoot, func(), error) {
	roots, release, err := LockRoots(ctx, paths...)
	if err != nil {
		return nil, nil, err
	}
	result := make(map[string]ports.TransferRoot, len(roots))
	for path, root := range roots {
		result[path] = &capability{root}
	}
	return result, release, nil
}
func (f *Filesystem) Hardlink(ctx context.Context, source ports.TransferRoot, sourceRel string, target ports.TransferRoot, targetRel string, frozen ports.FileIdentity) error {
	src, ok := source.(*capability)
	if !ok {
		return ports.ErrFileIdentity
	}
	dst, ok := target.(*capability)
	if !ok {
		return ports.ErrFileIdentity
	}
	return Hardlink(ctx, src.root, sourceRel, dst.root, targetRel, frozen)
}
