//go:build linux

package rootfs

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"golang.org/x/sys/unix"
)

func TestCapacitySamplerPinnedRoot(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()
	path := filepath.Join(parent, "root")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	roots, release, err := NewFilesystem().LockRoots(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	root := roots[path]
	clock := &capacityTestClock{now: time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)}
	sampler := NewCapacitySampler(clock)
	for _, name := range []string{"original", "renamed"} {
		t.Run(name, func(t *testing.T) {
			if name == "renamed" {
				if err := os.Rename(path, filepath.Join(parent, "moved")); err != nil {
					t.Fatal(err)
				}
				// The old path now resolves nowhere; only the pinned FD can work.
				if err := os.Symlink(filepath.Join(parent, "absent"), path); err != nil {
					t.Fatal(err)
				}
			}
			var stat unix.Statfs_t
			if err := unix.Fstatfs(root.(*capability).root.fd, &stat); err != nil {
				t.Fatal(err)
			}
			calls := clock.calls
			got, err := sampler.SampleCapacity(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			if got.TotalBytes != int64(stat.Blocks)*stat.Bsize || got.AvailableBytes < 0 ||
				got.AvailableBytes > got.TotalBytes || got.AvailableBytes%stat.Bsize != 0 ||
				got.SampledAt != clock.now || clock.calls != calls+1 {
				t.Fatalf("sample = %+v; statfs = %+v; clock calls = %d", got, stat, clock.calls)
			}
			if _, err := root.Identity(ctx); err != nil {
				t.Fatalf("sampler released caller's capability: %v", err)
			}
		})
	}
}

func TestCapacitySamplerRejectsNativeInvalid(t *testing.T) {
	for _, tc := range []struct {
		name string
		root ports.TransferRoot
	}{
		{name: "typed_nil", root: (*capability)(nil)},
		{name: "nil_root", root: &capability{}},
		{name: "invalid_fd", root: &capability{root: &Root{fd: -1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := &capacityTestClock{}
			got, err := NewCapacitySampler(clock).SampleCapacity(context.Background(), tc.root)
			if !errors.Is(err, ports.ErrCapacityUnavailable) || got != (ports.RootCapacity{}) || clock.calls != 0 {
				t.Fatalf("sample = %+v, %v; clock calls = %d", got, err, clock.calls)
			}
		})
	}
}

func TestCapacitySamplerClosed(t *testing.T) {
	root, err := NewFilesystem().OpenRoot(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root.Close()
	clock := &capacityTestClock{}
	got, err := NewCapacitySampler(clock).SampleCapacity(context.Background(), root)
	if !errors.Is(err, ports.ErrCapacityUnavailable) || got != (ports.RootCapacity{}) || clock.calls != 0 {
		t.Fatalf("sample = %+v, %v; clock calls = %d", got, err, clock.calls)
	}
}

func TestCapacitySamplerCancellationAfterSyscall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root, err := NewFilesystem().OpenRoot(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	clock := &capacityTestClock{onNow: cancel}
	got, err := NewCapacitySampler(clock).SampleCapacity(ctx, root)
	if !errors.Is(err, context.Canceled) || got != (ports.RootCapacity{}) || clock.calls != 1 {
		t.Fatalf("sample = %+v, %v; clock calls = %d", got, err, clock.calls)
	}
	if _, err := root.Identity(context.Background()); err != nil {
		t.Fatalf("cancellation released caller's capability: %v", err)
	}
}

func TestCapacityBytes(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		blockSize              int64
		blocks, available      uint64
		totalBytes, availBytes int64
		invalid                bool
	}{
		{name: "normal", blockSize: 4096, blocks: 100, available: 30, totalBytes: 409600, availBytes: 122880},
		{name: "full_disk", blockSize: 4096, blocks: 100, totalBytes: 409600},
		{name: "int64_limit", blockSize: 1, blocks: math.MaxInt64, available: math.MaxInt64, totalBytes: math.MaxInt64, availBytes: math.MaxInt64},
		{name: "zero_blocksize", blocks: 1, invalid: true},
		{name: "negative_blocksize", blockSize: -1, blocks: 1, invalid: true},
		{name: "zero_blocks", blockSize: 4096, invalid: true},
		{name: "available_exceeds_total", blockSize: 4096, blocks: 1, available: 2, invalid: true},
		{name: "multiplication_overflow", blockSize: 4096, blocks: math.MaxInt64/4096 + 1, invalid: true},
		{name: "available_overflow", blockSize: 2, blocks: math.MaxInt64, available: math.MaxInt64, invalid: true},
		{name: "unsigned_overflow", blockSize: 1, blocks: math.MaxUint64, invalid: true},
		{name: "blocksize_limit", blockSize: math.MaxInt64, blocks: 2, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			total, available, err := capacityBytes(tc.blockSize, tc.blocks, tc.available)
			if tc.invalid {
				if !errors.Is(err, ports.ErrCapacityUnavailable) || total != 0 || available != 0 {
					t.Fatalf("bytes = %d, %d, %v", total, available, err)
				}
				return
			}
			if err != nil || total != tc.totalBytes || available != tc.availBytes {
				t.Fatalf("bytes = %d, %d, %v; want %d, %d", total, available, err, tc.totalBytes, tc.availBytes)
			}
		})
	}
}
