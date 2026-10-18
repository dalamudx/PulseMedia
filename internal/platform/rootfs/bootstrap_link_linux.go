//go:build linux

package rootfs

import "golang.org/x/sys/unix"

func linkBootstrapMarker(directory int, stage, marker string) error {
	return unix.Linkat(directory, stage, directory, marker, 0)
}
