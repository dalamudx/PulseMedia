//go:build linux

package rootfs

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var _ ports.ScanFilesystem = (*Filesystem)(nil)

func (f *Filesystem) Walk(ctx context.Context, root ports.TransferRoot, visit func(string, ports.FileIdentity) error) error {
	c, ok := root.(*capability)
	if !ok {
		return ports.ErrFileIdentity
	}
	return walkFiles(ctx, c.root.fd, "", visit)
}
func walkFiles(ctx context.Context, dirFD int, prefix string, visit func(string, ports.FileIdentity) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fd, err := unix.Openat(dirFD, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	dir := os.NewFile(uintptr(fd), prefix)
	defer func() { _ = dir.Close() }()
	for {
		entries, readErr := dir.ReadDir(128)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			name := entry.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			var stat unix.Stat_t
			if err := unix.Fstatat(fd, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return err
			}
			rel := filepath.Join(prefix, name)
			switch stat.Mode & unix.S_IFMT {
			case unix.S_IFLNK:
				return ports.ErrFileSymlink
			case unix.S_IFDIR:
				child, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
				if err != nil {
					return err
				}
				err = walkFiles(ctx, child, rel, visit)
				_ = unix.Close(child)
				if err != nil {
					return err
				}
			case unix.S_IFREG:
				if err := visit(rel, identity(stat)); err != nil {
					return err
				}
			default:
				return ports.ErrFileIdentity
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}
