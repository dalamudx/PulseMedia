//go:build !linux

package rootfs

import (
	"context"
	"github.com/dalamudx/PulseMedia/internal/ports"
)

func (*Filesystem) MaintenanceInspect(context.Context, ports.TransferRoot, string) (ports.MaintenanceFileObservation, error) {
	return ports.MaintenanceFileObservation{}, ports.ErrUnavailable
}
func (*Filesystem) MaintenanceRename(context.Context, ports.TransferRoot, string, string, ports.FileIdentity) error {
	return ports.ErrUnavailable
}
func (*Filesystem) MaintenanceRemove(context.Context, ports.TransferRoot, string, ports.FileIdentity) error {
	return ports.ErrUnavailable
}
