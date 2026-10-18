package httpapi_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/logging"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

type trackingRuntimeLog struct {
	startup runtimeTVStartup
	closing chan struct{}
	once    sync.Once
}

func (l *trackingRuntimeLog) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("shutdown signal received")) {
		l.once.Do(func() { close(l.closing) })
	}
	return l.startup.Write(p)
}

type trackingRuntimeApp struct {
	cancel context.CancelFunc
	done   chan error
	log    *trackingRuntimeLog
	stop   func()
}

func startTrackingRuntime(t *testing.T, n *trackingFixture) *trackingRuntimeApp {
	t.Helper()
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	cfg.SecretKey = hex.EncodeToString(bytes.Repeat([]byte{42}, 32))
	cfg.Database.URL = n.f.a.Config().Database.URL
	cfg.Server.HTTPAddr = "127.0.0.1:0"
	l := &trackingRuntimeLog{startup: runtimeTVStartup{ready: make(chan struct{})}, closing: make(chan struct{})}
	a, e := app.New(cfg, app.WithLogger(logging.NewLogger(l, "info")))
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &trackingRuntimeApp{cancel: cancel, done: make(chan error, 1), log: l}
	go func() { r.done <- a.Run(ctx) }()
	var once sync.Once
	r.stop = func() {
		once.Do(func() {
			cancel()
			select {
			case e := <-r.done:
				if e != nil {
					t.Error(e)
				}
			case <-time.After(20 * time.Second):
				t.Error("tracking runtime failed to drain")
			}
		})
	}
	t.Cleanup(r.stop)
	select {
	case <-l.startup.ready:
	case e := <-r.done:
		r.done <- e
		t.Fatalf("startup: %v", e)
	case <-time.After(20 * time.Second):
		t.Fatal("startup barrier timeout")
	}
	return r
}
func trackingRuntimeFresh(t *testing.T) *trackingFixture {
	t.Helper()
	n := newTrackingFixture(t)
	n.exec(t, `DELETE FROM job WHERE id=$1`, n.request.Lease.ID)
	n.exec(t, `UPDATE job SET state='dead',lease_owner=NULL,lease_until=NULL WHERE id=$1`, n.c.Lease.ID)
	n.exec(t, `UPDATE download_task SET updated_at=clock_timestamp()-interval '31 seconds'`)
	return n
}
func trackingRuntimeSchedule(t *testing.T, n *trackingFixture) {
	t.Helper()
	if e := postgres.NewCompleteTVTrackingRuntimeStore(n.f.a.DBPool(), idgen.New()).ScheduleCompleteTVTracking(context.Background()); e != nil {
		t.Fatal(e)
	}
}
func trackingRuntimeCounts(t *testing.T, n *trackingFixture) (int, int) {
	t.Helper()
	var total, live int
	if e := n.f.a.DBPool().QueryRow(context.Background(), `SELECT count(*),count(*) FILTER(WHERE state IN ('queued','running')) FROM job WHERE kind=$1`, ports.CompleteTVTrackingJobKind).Scan(&total, &live); e != nil {
		t.Fatal(e)
	}
	return total, live
}
func trackingRuntimeProtection(t *testing.T, n *trackingFixture) {
	t.Helper()
	var lanes, reservations, demands int
	e := n.f.a.DBPool().QueryRow(context.Background(), `SELECT (SELECT count(*) FROM acquisition_reservation WHERE released_at IS NULL),(SELECT count(*) FROM unit_reservation WHERE state='dispatched'),(SELECT count(*) FROM download_requirement WHERE state='pending')`).Scan(&lanes, &reservations, &demands)
	if e != nil || lanes != 2 || reservations != 2 || demands != 2 {
		t.Fatal("full-set protection", e, lanes, reservations, demands)
	}
}

func trackingRuntimeAssert(t *testing.T, n *trackingFixture, state string) {
	t.Helper()
	trackingRuntimeProtection(t, n)
	var ok bool
	e := n.f.a.DBPool().QueryRow(context.Background(), `SELECT state=$1 AND transfer_plan_id IS NULL AND
 (SELECT count(*) FROM complete_tv_physical_permit)=0 AND (SELECT count(*) FROM download_requirement WHERE state='pending')=2 AND
 (SELECT count(*) FROM job WHERE kind='download_organization')=0 AND (SELECT count(*) FROM library_file_unit)=0 AND
 (SELECT count(*) FROM unit_fact WHERE stage='imported')=0 AND
 ($1<>'completed' OR (organization_state='review_required' AND progress=1 AND downloaded_bytes=1024 AND total_bytes=1024)) FROM download_task WHERE id=$2`, state, n.request.TaskID).Scan(&ok)
	if e != nil || !ok || n.adds.Load() != 0 {
		t.Fatal("tracking invariants", state, e, ok, n.adds.Load())
	}
}
func TestCompleteTVTrackingRuntimeSchedulerConcurrentAndRestart(t *testing.T) {
	n := trackingRuntimeFresh(t)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- postgres.NewCompleteTVTrackingRuntimeStore(n.f.a.DBPool(), idgen.New()).ScheduleCompleteTVTracking(context.Background())
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if total, live := trackingRuntimeCounts(t, n); total != 1 || live != 1 {
		t.Fatal(total, live)
	}
	var old uuid.UUID
	if e := n.f.a.DBPool().QueryRow(context.Background(), `UPDATE job SET state='running',lease_owner='old',lease_token=7,lease_until=clock_timestamp()-interval '1 second' WHERE kind=$1 RETURNING id`, ports.CompleteTVTrackingJobKind).Scan(&old); e != nil {
		t.Fatal(e)
	}
	trackingRuntimeSchedule(t, n)
	var fenced bool
	if e := n.f.a.DBPool().QueryRow(context.Background(), `SELECT state='dead' AND lease_token=8 AND lease_owner IS NULL FROM job WHERE id=$1`, old).Scan(&fenced); e != nil || !fenced {
		t.Fatal("old lease not fenced", e)
	}
	stale := n.request
	stale.Lease = ports.JobLease{ID: old, Owner: "old", Token: 7}
	// Exercise the validated native entry with the old lease too, before any I/O.
	n.request = stale
	if r, e := n.observe(context.Background()); r != nil || !errors.Is(e, ports.ErrCompleteTVObservationStale) {
		t.Fatal("old token accepted", r, e)
	}
	if n.lookups.Load() != 0 {
		t.Fatal("old token reached native HTTP")
	}
	n.exec(t, `UPDATE job SET updated_at=clock_timestamp()-interval '31 seconds' WHERE id=$1`, old)
	r := startTrackingRuntime(t, n)
	runtimeTVWait(t, func() bool {
		var ok bool
		_ = n.f.a.DBPool().QueryRow(context.Background(), `SELECT state='completed' FROM download_task WHERE id=$1`, n.request.TaskID).Scan(&ok)
		return ok
	})
	r.stop()
	trackingRuntimeAssert(t, n, "completed")
	if total, live := trackingRuntimeCounts(t, n); total != 2 || live != 0 {
		t.Fatal(total, live)
	}
	for range 3 {
		trackingRuntimeSchedule(t, n)
	}
	if total, _ := trackingRuntimeCounts(t, n); total != 2 {
		t.Fatal("terminal requeued", total)
	}
}
func TestCompleteTVTrackingRuntimeSchedulerFiniteRecoveryAndPermit(t *testing.T) {
	t.Run("expiry-limit", func(t *testing.T) {
		n := trackingRuntimeFresh(t)
		for generation := 1; generation <= 3; generation++ {
			trackingRuntimeSchedule(t, n)
			if total, live := trackingRuntimeCounts(t, n); total != generation || live != 1 {
				t.Fatal("generation", generation, total, live)
			}
			n.exec(t, `UPDATE job SET state='running',lease_owner='lost-process',lease_until=clock_timestamp()-interval '1 second' WHERE kind=$1 AND state='queued'`, ports.CompleteTVTrackingJobKind)
			trackingRuntimeSchedule(t, n)
			if total, live := trackingRuntimeCounts(t, n); total != generation || live != 0 {
				t.Fatal("interval bypass", total, live)
			}
			n.exec(t, `UPDATE job SET updated_at=clock_timestamp()-interval '31 seconds' WHERE kind=$1`, ports.CompleteTVTrackingJobKind)
		}
		for range 5 {
			trackingRuntimeSchedule(t, n)
		}
		if total, live := trackingRuntimeCounts(t, n); total != 3 || live != 0 {
			t.Fatal("unbounded replay", total, live)
		}
		r := startTrackingRuntime(t, n)
		trackingRuntimeSchedule(t, n)
		r.stop()
		if n.lookups.Load() != 0 {
			t.Fatal("exhaustion reached network")
		}
		trackingRuntimeAssert(t, n, "tracking")
	})
	t.Run("retained-permit", func(t *testing.T) {
		n := trackingRuntimeFresh(t)
		token := uuid.New()
		n.exec(t, `INSERT INTO complete_tv_physical_permit(operation_id,token,backend_pid,advisory_key,created_at) VALUES($1,$2,2147483647,1,clock_timestamp()-interval '1 year')`, n.request.OperationID, token)
		r := startTrackingRuntime(t, n)
		for range 5 {
			trackingRuntimeSchedule(t, n)
		}
		r.stop()
		if total, _ := trackingRuntimeCounts(t, n); total != 0 || n.lookups.Load() != 0 {
			t.Fatal("retained permit generated work", total, n.lookups.Load())
		}
		var retained bool
		if e := n.f.a.DBPool().QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM complete_tv_physical_permit WHERE operation_id=$1 AND token=$2)`, n.request.OperationID, token).Scan(&retained); e != nil || !retained {
			t.Fatal(e, retained)
		}
		trackingRuntimeProtection(t, n)
		n.exec(t, `DELETE FROM complete_tv_physical_permit WHERE operation_id=$1 AND token=$2`, n.request.OperationID, token)
	})
}
func TestCompleteTVTrackingRuntimeAppCrossInstanceLostLock(t *testing.T) {
	n := trackingRuntimeFresh(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	n.hook = func(p string) {
		if strings.HasSuffix(p, "/files") {
			close(entered)
			<-release
		}
	}
	first := startTrackingRuntime(t, n)
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("no native inventory barrier")
	}
	n.exec(t, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE kind=$1 AND state='running'`, ports.CompleteTVTrackingJobKind)
	// Terminate the original advisory session while HTTP is still in progress.
	var killed bool
	if e := n.f.a.DBPool().QueryRow(context.Background(), `SELECT pg_terminate_backend(backend_pid) FROM complete_tv_physical_permit WHERE operation_id=$1`, n.request.OperationID).Scan(&killed); e != nil || !killed {
		t.Fatal(e, killed)
	}
	second := startTrackingRuntime(t, n)
	trackingRuntimeSchedule(t, n)
	if total, live := trackingRuntimeCounts(t, n); total != 1 || live != 0 {
		t.Fatal("lost session rescheduled physical work", total, live)
	}
	if n.lookups.Load() != 1 || n.inventories.Load() != 0 {
		t.Fatal("overlapping request", n.lookups.Load(), n.inventories.Load())
	}
	var permits int
	if e := n.f.a.DBPool().QueryRow(context.Background(), `SELECT count(*) FROM complete_tv_physical_permit`).Scan(&permits); e != nil || permits != 1 {
		t.Fatal("permit dropped during HTTP", e, permits)
	}
	once.Do(func() { close(release) })
	runtimeTVWait(t, func() bool {
		var count int
		_ = n.f.a.DBPool().QueryRow(context.Background(), `SELECT count(*) FROM complete_tv_physical_permit`).Scan(&count)
		return count == 0
	})
	first.stop()
	second.stop()
	trackingRuntimeAssert(t, n, "tracking")
	if n.lookups.Load() != 2 {
		t.Fatal("native attempts overlapped", n.lookups.Load())
	}
}
func TestCompleteTVTrackingRuntimeAppShutdownDrainsWriteback(t *testing.T) {
	n := trackingRuntimeFresh(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	n.hook = func(p string) {
		if strings.HasSuffix(p, "/files") {
			close(entered)
			<-release
		}
	}
	r := startTrackingRuntime(t, n)
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("no native inventory barrier")
	}
	tx, e := n.f.a.DBPool().Begin(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, e = tx.Exec(context.Background(), `LOCK TABLE audit_log IN ACCESS EXCLUSIVE MODE`); e != nil {
		t.Fatal(e)
	}
	once.Do(func() { close(release) })
	runtimeTVWait(t, func() bool {
		var waiting bool
		_ = n.f.a.DBPool().QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query ILIKE '%INSERT INTO audit_log%')`).Scan(&waiting)
		return waiting
	})
	r.cancel()
	select {
	case <-r.log.closing:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown not observed")
	}
	select {
	case e := <-r.done:
		r.done <- e
		t.Fatal("closed before blocked fact writeback drained")
	default:
	}
	var permits int
	if e = n.f.a.DBPool().QueryRow(context.Background(), `SELECT count(*) FROM complete_tv_physical_permit`).Scan(&permits); e != nil || permits != 1 {
		t.Fatal("shutdown released physical permit", e, permits)
	}
	if e = tx.Rollback(context.Background()); e != nil {
		t.Fatal(e)
	}
	r.stop()
	trackingRuntimeAssert(t, n, "completed")
}
