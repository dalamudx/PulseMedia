//go:build linux

package app

import (
	"syscall"
	"testing"
)

func makeBootstrapFIFO(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
}
