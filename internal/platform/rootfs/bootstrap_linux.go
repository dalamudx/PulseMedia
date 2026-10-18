//go:build linux

package rootfs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfile"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"os"
	"path/filepath"
	"syscall"
)

var _ ports.LibraryBootstrapFilesystem = (*Filesystem)(nil)

func (f *Filesystem) PrepareDefaultRoot(ctx context.Context, dataDir, kind string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	name := "movies"
	if kind == "tv" {
		name = "tv"
	} else if kind != "movie" {
		return "", fmt.Errorf("unsupported root type")
	}
	root, err := rootfile.OpenDirectory(dataDir, false)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	if err = root.Mkdir("media", 0755); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	media, err := rootfile.Open(root, "media", syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		return "", err
	}
	defer func() { _ = media.Close() }()
	if err = syscall.Mkdirat(int(media.Fd()), name, 0755); err != nil && !errors.Is(err, syscall.EEXIST) {
		return "", err
	}
	fd, err := syscall.Openat(int(media.Fd()), name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return "", err
	}
	directory := os.NewFile(uintptr(fd), name)
	defer func() { _ = directory.Close() }()
	if err = ensureBootstrapMarker(ctx, directory); err != nil {
		return "", err
	}
	if err = media.Sync(); err != nil {
		return "", err
	}
	parent, err := root.Open(".")
	if err != nil {
		return "", err
	}
	defer func() { _ = parent.Close() }()
	if err = parent.Sync(); err != nil {
		return "", err
	}
	return filepath.Join(dataDir, "media", name), nil
}
func ensureBootstrapMarker(ctx context.Context, directory *os.File) error {
	const marker = ".pulsemedia_root"
	open := func(name string, flags int, mode uint32) (*os.File, error) {
		fd, err := syscall.Openat(int(directory.Fd()), name, flags|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, mode)
		if err != nil {
			return nil, err
		}
		return os.NewFile(uintptr(fd), name), nil
	}
	validate := func() error {
		file, err := open(marker, syscall.O_RDONLY, 0)
		if err != nil {
			return err
		}
		defer func() { _ = file.Close() }()
		info, err := file.Stat()
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !info.Mode().IsRegular() || info.Size() == 0 || !ok || stat.Nlink != 1 {
			return fmt.Errorf("invalid library root marker")
		}
		return nil
	}
	err := validate()
	if err == nil {
		return ctx.Err()
	}
	if !errors.Is(err, syscall.ENOENT) {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return err
	}
	stage := ".root-marker-" + hex.EncodeToString(nonce[:])
	file, err := open(stage, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL, 0644)
	if err != nil {
		return err
	}
	defer func() { _ = syscall.Unlinkat(int(directory.Fd()), stage) }()
	defer func() { _ = file.Close() }()
	if _, err = file.WriteString("pulsemedia_ok"); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	// Publish complete bytes without replacing any existing marker or alias.
	if err = linkBootstrapMarker(int(directory.Fd()), stage, marker); err != nil {
		if errors.Is(err, syscall.EEXIST) {
			return validate()
		}
		return err
	}
	if err = syscall.Unlinkat(int(directory.Fd()), stage); err != nil {
		return err
	}
	return directory.Sync()
}
