// Package rootfile provides kernel-enforced, single-component opens under a root.
package rootfile

import (
	"fmt"
	"os"
	"strings"
	"syscall"
)

// Open rejects symbolic links at the kernel operation, including aliases inside
// the root which os.Root otherwise resolves before interpreting open flags.
func Open(root *os.Root, name string, flags int, mode os.FileMode) (*os.File, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return nil, fmt.Errorf("root file requires a single filename")
	}
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = directory.Close() }()
	fd, err := syscall.Openat(int(directory.Fd()), name, flags|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, uint32(mode.Perm()))
	if err != nil {
		return nil, &os.PathError{Op: "openat", Path: name, Err: err}
	}
	return os.NewFile(uintptr(fd), name), nil
}
