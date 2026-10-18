package ports

import (
	"context"
	"errors"
)

var (
	ErrFileSymlink  = errors.New("symbolic links are forbidden")
	ErrFileEscape   = errors.New("path is outside configured root")
	ErrFileIdentity = errors.New("frozen file identity changed")
	ErrFileConflict = errors.New("target has a different identity")
)

type FileIdentity struct{ Device, Inode, Size, Mtime int64 }

// TransferRoot is an opaque directory capability. Relative paths only; handles
// never expose OS descriptors to usecases. Close releases a capability, not a lease.
type TransferRoot interface {
	Identity(context.Context) (FileIdentity, error)
	Inspect(context.Context, string) (FileIdentity, error)
	Close()
}

// TransferFilesystem owns physical root slots until the returned release is
// called AFTER all physical I/O ends. Logical cancellation must not release them.
type RootFilesystem interface {
	OpenRoot(context.Context, string) (TransferRoot, error)
	LockRoots(context.Context, ...string) (map[string]TransferRoot, func(), error)
}

// ScanFilesystem observes only regular files without following symbolic links.
type ScanFilesystem interface {
	RootFilesystem
	Walk(context.Context, TransferRoot, func(string, FileIdentity) error) error
}

type TransferFilesystem interface {
	RootFilesystem
	Hardlink(context.Context, TransferRoot, string, TransferRoot, string, FileIdentity) error
}

// RecycleFilesystem moves an exact regular file within the same controlled root.
// It never overwrites a destination. Callers own the physical slot until return.
type RecycleFilesystem interface {
	RootFilesystem
	Recycle(context.Context, TransferRoot, string, string, FileIdentity) error
}
