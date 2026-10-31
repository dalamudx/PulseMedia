//go:build linux

package rootfs

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path"
	"strings"
)

// Only this directory is enumerated. Subdirectories and symlinks never become candidates.
func (*Filesystem) ListCompanionDirectory(ctx context.Context, root ports.TransferRoot, relative string) ([]ports.UntrackedFileObservation, error) {
	c, ok := root.(*capability)
	if !ok {
		return nil, ports.ErrFileIdentity
	}
	p, err := parts(relative)
	if err != nil {
		return nil, err
	}
	fd, err := walk(c.root.fd, p, false)
	if err != nil {
		return nil, err
	}
	listing, err := unix.Openat(fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	_ = unix.Close(fd)
	if err != nil {
		return nil, err
	}
	fd = listing
	dir := os.NewFile(uintptr(fd), relative)
	defer func() { _ = dir.Close() }()
	out := []ports.UntrackedFileObservation{}
	count := 0
	for {
		entries, readErr := dir.ReadDir(128)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			count++
			if count > 4096 {
				return nil, ports.ErrRejected
			}
			if strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			var st unix.Stat_t
			if err := unix.Fstatat(fd, entry.Name(), &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return nil, err
			}
			if st.Mode&unix.S_IFMT != unix.S_IFREG {
				continue
			}
			out = append(out, ports.UntrackedFileObservation{RelativePath: path.Join(relative, entry.Name()), Identity: identity(st)})
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return out, nil
			}
			return nil, readErr
		}
	}
}
