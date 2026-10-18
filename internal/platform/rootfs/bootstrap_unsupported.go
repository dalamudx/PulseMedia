//go:build !linux

package rootfs

import "context"

func (*Filesystem) PrepareDefaultRoot(ctx context.Context, _, _ string) (string, error) {
	return "", unsupported(ctx)
}
