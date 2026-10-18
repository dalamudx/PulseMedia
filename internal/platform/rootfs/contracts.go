package rootfs

import "github.com/dalamudx/PulseMedia/internal/ports"

var (
	ErrSymlink  = ports.ErrFileSymlink
	ErrEscape   = ports.ErrFileEscape
	ErrIdentity = ports.ErrFileIdentity
	ErrConflict = ports.ErrFileConflict
)

type Identity = ports.FileIdentity
