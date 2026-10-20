//go:build !linux

package rootfs

import "context"

// Root is unavailable on non-Linux hosts; no absolute-path fallback is provided.
type Root struct{}

// PublishSidecar is unavailable on this platform.
func (*Root) PublishSidecar(ctx context.Context, _ SidecarRequest) (SidecarResult, error) {
	return SidecarResult{Disposition: SidecarNotPublished}, unsupported(ctx)
}

// InspectSidecarDirectory is unavailable on this platform.
func (*Root) InspectSidecarDirectory(ctx context.Context, _ string) (SidecarDirectoryIdentity, error) {
	return SidecarDirectoryIdentity{}, unsupported(ctx)
}
