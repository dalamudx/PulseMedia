package postgres_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/librarymaintenance"
)

type maintenanceCleanupCancelFS struct {
	ports.LibraryMaintenanceFilesystem
	cancel context.CancelFunc
	calls  atomic.Int32
}

func (f *maintenanceCleanupCancelFS) MaintenanceRemove(ctx context.Context, r ports.TransferRoot, p string, id ports.FileIdentity) error {
	f.calls.Add(1)
	e := f.LibraryMaintenanceFilesystem.MaintenanceRemove(ctx, r, p, id)
	if f.cancel != nil {
		f.cancel()
	}
	return e
}
func ageMaintenanceTVFixture(t *testing.T, f maintenanceRetiredFixture) {
	t.Helper()
	tx, e := f.pool.Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var expired time.Time
	if e = tx.QueryRow(t.Context(), `SELECT clock_timestamp()-interval '1 day'`).Scan(&expired); e != nil {
		t.Fatal(e)
	}
	// Only the owned historical backup fixture changes its original timestamps;
	// production exposes no history mutation or automatic cleanup operation.
	for _, table := range []string{"tv_episode_upgrade_phase", "tv_episode_upgrade_return"} {
		if _, e = tx.Exec(t.Context(), `ALTER TABLE `+table+` DISABLE TRIGGER USER`); e != nil {
			t.Fatal(e)
		}
		if _, e = tx.Exec(t.Context(), `UPDATE `+table+` SET evidence=jsonb_set(evidence,'{retained_until}',to_jsonb($2::timestamptz)) WHERE operation_id=$1 AND action='retire'`, strings.TrimPrefix(f.recycleID, "tv:"), expired); e != nil {
			t.Fatal(e)
		}
		if _, e = tx.Exec(t.Context(), `ALTER TABLE `+table+` ENABLE TRIGGER USER`); e != nil {
			t.Fatal(e)
		}
	}
	if e = tx.Commit(t.Context()); e != nil {
		t.Fatal(e)
	}
}
func TestLibraryMaintenanceExpiredTVCleanupPG(t *testing.T) {
	for _, kind := range []string{"completed", "cancelled-after-unlink"} {
		t.Run(kind, func(t *testing.T) {
			f := maintenanceTVRetired(t)
			ageMaintenanceTVFixture(t, f)
			_, recycled, replacement := f.paths(t)
			newStat, e := os.Stat(replacement)
			if e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			files := &maintenanceCleanupCancelFS{LibraryMaintenanceFilesystem: rootfs.NewFilesystem()}
			if kind != "completed" {
				files.cancel = cancel
			}
			store, e := postgres.NewLibraryMaintenanceStore(f.pool, files, idgen.New())
			if e != nil {
				t.Fatal(e)
			}
			preview, e := store.PreviewRecycle(ctx, f.actor, ports.LibraryRecyclePreviewRequest{RecycleID: f.recycleID, Action: "cleanup"})
			if e != nil || preview == nil {
				t.Fatal(preview, e)
			}
			command := ports.LibraryMaintenanceCommand{Action: "cleanup", RecycleID: f.recycleID, ExpectedVersion: preview.Version, Confirmed: true}
			out, e := librarymaintenance.NewService(store).Command(ctx, f.actor, command, "tv-cleanup-once")
			if e != nil || out == nil {
				t.Fatal(out, e)
			}
			want := "completed"
			if kind != "completed" {
				want = "unknown_requires_review"
			}
			if out.Status != want {
				t.Fatal(out)
			}
			if _, e = os.Stat(recycled); !errors.Is(e, os.ErrNotExist) {
				t.Fatal("physical cleanup did not return", e)
			}
			after, e := os.Stat(replacement)
			if e != nil || !os.SameFile(after, newStat) {
				t.Fatal("current replacement changed", e)
			}
			again, e := librarymaintenance.NewService(store).Command(t.Context(), f.actor, command, "tv-cleanup-once")
			if e != nil || again == nil || *again != *out || files.calls.Load() != 1 {
				t.Fatal("cleanup replay", again, e, files.calls.Load())
			}
		})
	}
}
