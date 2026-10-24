//go:build linux

package rootfs

import (
	"errors"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"golang.org/x/sys/unix"
)

func TestTransferRenameErrorPreservesAtomicFailure(t *testing.T) {
	for _, tc := range []struct {
		name        string
		err         error
		unsupported bool
	}{
		{"invalid_flags", unix.EINVAL, true},
		{"no_syscall", unix.ENOSYS, true},
		{"unsupported", unix.EOPNOTSUPP, true},
		{"existing_target", unix.EEXIST, false},
		{"cross_device", unix.EXDEV, false},
		{"permission", unix.EACCES, false},
		{"no_space", unix.ENOSPC, false},
		{"cancelled", unix.EINTR, false},
		{"success", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := transferRenameError(tc.err)
			if !errors.Is(got, tc.err) || errors.Is(got, ports.ErrFileUnsupported) != tc.unsupported {
				t.Fatal("rename classification lost original failure or misclassified atomic capability")
			}
			if !tc.unsupported && got != tc.err {
				t.Fatal("unrelated rename failure changed")
			}
		})
	}
}
