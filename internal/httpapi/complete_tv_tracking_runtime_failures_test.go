package httpapi_test

import (
	"context"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
)

func TestCompleteTVTrackingRuntimeUnavailableBoundedAttempts(t *testing.T) {
	n := trackingRuntimeFresh(t)
	n.info = func(int32) string { return "null" }
	r := startTrackingRuntime(t, n)
	for attempt := 1; attempt <= 3; attempt++ {
		runtimeTVWait(t, func() bool {
			var ready bool
			_ = n.f.a.DBPool().QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM job WHERE kind=$1 AND attempts=$2 AND state=$3)`, ports.CompleteTVTrackingJobKind, attempt, map[bool]string{false: "queued", true: "dead"}[attempt == 3]).Scan(&ready)
			return ready
		})
		var unchanged bool
		if e := n.f.a.DBPool().QueryRow(context.Background(), `SELECT state='tracking' AND progress=0 AND downloaded_bytes=0 AND (SELECT count(*) FROM audit_log WHERE action='complete_tv.tracking_observed')=0 FROM download_task WHERE id=$1`, n.request.TaskID).Scan(&unchanged); e != nil || !unchanged {
			t.Fatal("unavailable fabricated facts", e)
		}
		if n.lookups.Load() != int32(attempt) {
			t.Fatal("retry busy loop", attempt, n.lookups.Load())
		}
		if attempt < 3 {
			var delayed bool
			if e := n.f.a.DBPool().QueryRow(context.Background(), `SELECT not_before>=updated_at+interval '29 seconds' FROM job WHERE kind=$1`, ports.CompleteTVTrackingJobKind).Scan(&delayed); e != nil || !delayed {
				t.Fatal("retry lacks finite interval", e)
			}
			n.exec(t, `UPDATE job SET not_before=clock_timestamp()-interval '1 second' WHERE kind=$1 AND state='queued'`, ports.CompleteTVTrackingJobKind)
		}
	}
	r.stop()
	for range 4 {
		trackingRuntimeSchedule(t, n)
	}
	if total, live := trackingRuntimeCounts(t, n); total != 1 || live != 0 {
		t.Fatal("exhausted work replayed", total, live)
	}
	trackingRuntimeAssert(t, n, "tracking")
}
func TestCompleteTVTrackingRuntimeUnknownUsesIndependentR3(t *testing.T) {
	n := trackingRuntimeFresh(t)
	n.info = func(int32) string { return "[]" }
	r := startTrackingRuntime(t, n)
	runtimeTVWait(t, func() bool {
		var done bool
		_ = n.f.a.DBPool().QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM job WHERE kind=$1 AND state='succeeded' AND result->>'state'='unknown')`, ports.CompleteTVObservationJobKind).Scan(&done)
		return done
	})
	r.stop()
	for range 4 {
		trackingRuntimeSchedule(t, n)
		if e := postgres.NewCompleteTVRuntimeStore(n.f.a.DBPool(), idgen.New()).RecoverCompleteTVJobs(context.Background()); e != nil {
			t.Fatal(e)
		}
	}
	if total, live := trackingRuntimeCounts(t, n); total != 1 || live != 0 {
		t.Fatal("unknown used tracking again", total, live)
	}
	var observations int
	if e := n.f.a.DBPool().QueryRow(context.Background(), `SELECT count(*) FROM job WHERE kind=$1`, ports.CompleteTVObservationJobKind).Scan(&observations); e != nil || observations != 1 {
		t.Fatal("R3 replay", e, observations)
	}
	if n.lookups.Load() != 3 || n.inventories.Load() != 0 {
		t.Fatal("wrong read-only work", n.lookups.Load(), n.inventories.Load())
	}
	trackingRuntimeAssert(t, n, "unknown")
}
func TestCompleteTVTrackingRuntimeSchedulerSkipsLockedAndTerminal(t *testing.T) {
	n := trackingRuntimeFresh(t)
	tx, e := n.f.a.DBPool().Begin(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, e = tx.Exec(context.Background(), `UPDATE download_task SET state='completed' WHERE id=$1`, n.request.TaskID); e != nil {
		t.Fatal(e)
	}
	trackingRuntimeSchedule(t, n)
	if total, _ := trackingRuntimeCounts(t, n); total != 0 {
		t.Fatal("scheduled locked task", total)
	}
	if e = tx.Commit(context.Background()); e != nil {
		t.Fatal(e)
	}
	trackingRuntimeSchedule(t, n)
	if total, _ := trackingRuntimeCounts(t, n); total != 0 {
		t.Fatal("scheduled completed task", total)
	}
}
