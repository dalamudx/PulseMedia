package rootfs

import "github.com/dalamudx/PulseMedia/internal/ports"

const MaxSidecarBytes = ports.MaxSidecarBytes

var ErrSidecarRequest = ports.ErrSidecarRequest

type SidecarDirectoryIdentity = ports.SidecarDirectoryIdentity
type SidecarRequest = ports.SidecarRequest
type SidecarDisposition = ports.SidecarDisposition
type SidecarResult = ports.SidecarResult

const (
	SidecarNotPublished     = ports.SidecarNotPublished
	SidecarCreated          = ports.SidecarCreated
	SidecarPreservedRegular = ports.SidecarPreservedRegular
	SidecarUnsafeExisting   = ports.SidecarUnsafeExisting
	SidecarUncertain        = ports.SidecarUncertain
)
