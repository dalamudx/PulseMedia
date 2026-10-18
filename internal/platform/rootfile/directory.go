package rootfile

import (
	"errors"
	"os"
	"strings"
	"syscall"
)

var ErrDirectory = errors.New("data directory is invalid or unavailable")

// OpenDirectory pins every path component without following symbolic links.
// Missing components are created only when requested. Existing permissions are
// never changed. Relative paths retain their working-directory semantics.
func OpenDirectory(path string, create bool) (*os.Root, error) {
	if path == "" || strings.ContainsRune(path, 0) {
		return nil, ErrDirectory
	}
	base := "."
	if strings.HasPrefix(path, "/") {
		base = "/"
	}
	fd, err := syscall.Open(base, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrDirectory
	}
	defer func() { _ = syscall.Close(fd) }()
	for _, name := range strings.Split(path, "/") {
		if name == "" || name == "." {
			continue
		}
		next, err := syscall.Openat(fd, name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if errors.Is(err, syscall.ENOENT) && create {
			err = syscall.Mkdirat(fd, name, 0700)
			if err == nil || errors.Is(err, syscall.EEXIST) {
				next, err = syscall.Openat(fd, name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
			}
		}
		if err != nil {
			return nil, ErrDirectory
		}
		_ = syscall.Close(fd)
		fd = next
	}
	// OpenRoot has no FD constructor. Before exposing its capability, compare
	// its opened directory against the no-follow FD; a pathname swap must not
	// substitute another directory between these two opens.
	var pinned syscall.Stat_t
	if err := syscall.Fstat(fd, &pinned); err != nil {
		return nil, ErrDirectory
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, ErrDirectory
	}
	info, err := root.Stat(".")
	if err == nil {
		if actual, ok := info.Sys().(*syscall.Stat_t); ok && actual.Dev == pinned.Dev && actual.Ino == pinned.Ino {
			return root, nil
		}
	}
	_ = root.Close()
	return nil, ErrDirectory
}
