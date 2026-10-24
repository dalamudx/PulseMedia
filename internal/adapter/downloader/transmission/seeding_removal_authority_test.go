package transmission

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

func TestSeedingRemovalAuthorityAfterFreshRead(t *testing.T) {
	for _, cause := range []error{ports.ErrForbidden, ports.ErrUnavailable, ports.ErrRejected, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			f := newSeedingRemovalFixture(t, "success", false)
			target := f.freeze(t)
			var checked atomic.Int32
			cap, err := f.client.SeedingRemover(target, func(ctx context.Context, current ports.SeedingRemoteTorrent) error {
				checked.Add(1)
				if ctx.Err() != nil || f.reads.Load() != 2 || f.removes.Load() != 0 || current.InfoHash != target.InfoHash || current.OperationTag != target.OperationTag || current.SavePath != target.SavePath || current.TotalBytes != target.TotalBytes || !current.Facts.Complete {
					t.Error("authority was checked without final native evidence or after dispatch")
				}
				return cause
			})
			if err != nil {
				t.Fatal(err)
			}
			out, err := cap.RemoveRetainingFiles(t.Context())
			if out != ports.SeedingRemovalNotDispatched || !errors.Is(err, cause) || checked.Load() != 1 || f.removes.Load() != 0 {
				t.Fatal("late authority failure did not prevent mutation", out, err, checked.Load(), f.removes.Load())
			}
			if _, err = cap.RemoveRetainingFiles(t.Context()); !errors.Is(err, ports.ErrRejected) || checked.Load() != 1 || f.removes.Load() != 0 {
				t.Fatal("failed authority was replayed")
			}
		})
	}
}
func TestSeedingRemovalAuthorityMissingOrCancelled(t *testing.T) {
	f := newSeedingRemovalFixture(t, "success", false)
	target := f.freeze(t)
	before := f.reads.Load()
	if _, err := f.client.SeedingRemover(target, nil); !errors.Is(err, ports.ErrRejected) || f.reads.Load() != before {
		t.Fatal("missing final authority accepted or made network call")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var checked atomic.Int32
	cap, err := f.client.SeedingRemover(target, func(context.Context, ports.SeedingRemoteTorrent) error { checked.Add(1); cancel(); return nil })
	if err != nil {
		t.Fatal(err)
	}
	out, err := cap.RemoveRetainingFiles(ctx)
	if out != ports.SeedingRemovalNotDispatched || !errors.Is(err, context.Canceled) || checked.Load() != 1 || f.removes.Load() != 0 {
		t.Fatal("cancellation during authority commit dispatched mutation", out, err)
	}
}
