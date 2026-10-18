package httpapi_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
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

type runtimeTVStartup struct {
	ready chan struct{}
	once  sync.Once
}

func (s *runtimeTVStartup) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("HTTP server listening")) {
		s.once.Do(func() { close(s.ready) })
	}
	return len(p), nil
}

type runtimeTVFixture struct {
	*byteSenderFixture
	lookups   atomic.Int32
	present   atomic.Bool
	ambiguous atomic.Bool
	lost      atomic.Bool
	entered   chan struct{}
	unblock   chan struct{}
}

func newRuntimeTVFixture(t *testing.T) *runtimeTVFixture {
	t.Helper()
	f := &runtimeTVFixture{byteSenderFixture: newByteSenderFixture(t, completeTVRaw)}
	// No requests have been made yet. Replace only this private fixture handler;
	// production App constructs the canonical native qBittorrent client itself.
	f.downloader.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = io.WriteString(w, "Ok.")
		case "/api/v2/torrents/add":
			f.adds.Add(1)
			if e := r.ParseMultipartForm(1 << 20); e != nil {
				f.invalid.Add(1)
				return
			}
			defer func() { _ = r.MultipartForm.RemoveAll() }()
			file, _, e := r.FormFile("torrents")
			if e != nil {
				f.invalid.Add(1)
				return
			}
			raw, _ := io.ReadAll(file)
			_ = file.Close()
			if string(raw) != completeTVRaw || r.FormValue("savepath") != f.expectedPath || r.FormValue("urls") != "" || r.FormValue("tags") != "pulsemedia,pulsemedia:op:"+f.c.OperationID.String() {
				f.invalid.Add(1)
			}
			f.present.Store(true)
			if f.entered != nil {
				close(f.entered)
				<-f.unblock
			}
			if f.lost.Load() {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					_ = conn.Close()
				}
				return
			}
			_, _ = io.WriteString(w, "Ok.")
		case "/api/v2/torrents/info":
			f.lookups.Add(1)
			if r.URL.Query().Get("tag") != "pulsemedia:op:"+f.c.OperationID.String() {
				f.invalid.Add(1)
			}
			rows := []any{}
			if f.present.Load() {
				row := map[string]any{"hash": "d497dcf93e2dbd4b917ba68788ba7d42590cf594", "save_path": f.expectedPath, "size": 1024, "downloaded": 0, "progress": 0, "state": "downloading", "tags": "pulsemedia,pulsemedia:op:" + f.c.OperationID.String()}
				rows = append(rows, row)
				if f.ambiguous.Load() {
					rows = append(rows, row)
				}
			}
			_ = json.NewEncoder(w).Encode(rows)
		default:
			f.invalid.Add(1)
			w.WriteHeader(500)
		}
	})
	return f
}
func (f *runtimeTVFixture) start(t *testing.T) func() {
	t.Helper()
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	cfg.SecretKey = hex.EncodeToString(f.key)
	cfg.Database.URL = f.f.a.Config().Database.URL
	cfg.Server.HTTPAddr = "127.0.0.1:0"
	startup := &runtimeTVStartup{ready: make(chan struct{})}
	a, e := app.New(cfg, app.WithLogger(logging.NewLogger(startup, "info")))
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case e := <-done:
				if e != nil {
					t.Error(e)
				}
			case <-time.After(20 * time.Second):
				t.Error("App failed to drain")
				_ = a.Close()
			}
		})
	}
	t.Cleanup(stop)
	select {
	case <-startup.ready:
	case err := <-done:
		done <- err
		t.Fatalf("App exited before startup: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("App startup timed out")
	}
	return stop
}
func runtimeTVWait(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("runtime condition timed out")
		case <-tick.C:
		}
	}
}
func (f *runtimeTVFixture) stateIs(state string) bool {
	var got string
	e := f.f.a.DBPool().QueryRow(context.Background(), `SELECT state FROM download_task WHERE operation_id=$1`, f.c.OperationID).Scan(&got)
	return e == nil && got == state
}
func (f *runtimeTVFixture) observationJobs(t *testing.T) (int, int) {
	t.Helper()
	var total, live int
	e := f.f.a.DBPool().QueryRow(context.Background(), `SELECT count(*),count(*) FILTER(WHERE state IN ('queued','running')) FROM job WHERE kind=$1`, ports.CompleteTVObservationJobKind).Scan(&total, &live)
	if e != nil {
		t.Fatal(e)
	}
	return total, live
}
func (f *runtimeTVFixture) invariants(t *testing.T) {
	t.Helper()
	f.protected(t)
	var preparations, tasks, units, organization, facts, permits int
	e := f.f.a.DBPool().QueryRow(context.Background(), `SELECT (SELECT count(*) FROM complete_tv_submission_preparation),(SELECT count(*) FROM download_task),(SELECT count(*) FROM complete_tv_submission_unit),(SELECT count(*) FROM job WHERE kind='download_organization'),(SELECT count(*) FROM unit_fact WHERE stage='imported'),(SELECT count(*) FROM complete_tv_physical_permit)`).Scan(&preparations, &tasks, &units, &organization, &facts, &permits)
	if e != nil || preparations != 1 || tasks != 1 || units != 2 || organization != 0 || facts != 0 || permits != 0 || f.invalid.Load() != 0 {
		t.Fatal("runtime invariant", e, preparations, tasks, units, organization, facts, permits, f.invalid.Load())
	}
}

func TestCompleteTVRuntimeAppNativeSendAndLostAckRestart(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "ack", true: "lost-ack"}[lost], func(t *testing.T) {
			f := newRuntimeTVFixture(t)
			f.lost.Store(lost)
			// Acquisition exists, but no preparation or task exists. An expired pre-send
			// worker must be fenced and claimed normally by the actual App.Run worker.
			f.exec(t, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, f.c.Lease.ID)
			stop := f.start(t)
			runtimeTVWait(t, func() bool { return f.stateIs("tracking") })
			stop()
			if f.adds.Load() != 1 || f.fetches.Load() != 1 {
				t.Fatal("wrong Add/fetch count", f.adds.Load(), f.fetches.Load())
			}
			if lost && f.lookups.Load() != 1 {
				t.Fatal("lost Add acknowledgement was not observed", f.lookups.Load())
			}
			// A subsequent application cannot resend a task or immutable preparation.
			stop = f.start(t)
			runtimeTVWait(t, func() bool { return f.stateIs("tracking") })
			stop()
			if f.adds.Load() != 1 || f.fetches.Load() != 1 {
				t.Fatal("restart replayed Add")
			}
			f.invariants(t)
		})
	}
}
func TestCompleteTVRuntimeAppPreparedRestartManualAndMatch(t *testing.T) {
	for _, mode := range []string{"absent", "match", "ambiguous"} {
		t.Run(mode, func(t *testing.T) {
			f := newRuntimeTVFixture(t)
			if _, e := f.prepare(); e != nil {
				t.Fatal(e)
			}
			f.present.Store(mode != "absent")
			f.ambiguous.Store(mode == "ambiguous")
			f.exec(t, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, f.c.Lease.ID)
			stop := f.start(t)
			runtimeTVWait(t, func() bool {
				var done bool
				_ = f.f.a.DBPool().QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM job WHERE kind=$1 AND state='succeeded')`, ports.CompleteTVObservationJobKind).Scan(&done)
				return done
			})
			stop()
			want := "unknown"
			if mode == "match" {
				want = "tracking"
			}
			if !f.stateIs(want) {
				t.Fatal("wrong observed state")
			}
			if f.adds.Load() != 0 || f.fetches.Load() != 0 || f.lookups.Load() != 1 {
				t.Fatal("prepared evidence was replayed")
			}
			if _, e := f.store.RecordCompleteTVSubmissionResult(context.Background(), ports.CompleteTVSubmissionResultCommand{Lease: f.c.Lease, TaskID: f.c.TaskID, OperationID: f.c.OperationID, Metainfo: f.c.Metainfo, Acknowledged: true}); !errors.Is(e, ports.ErrCompleteTVResultStale) {
				t.Fatal("old callback accepted", e)
			}
			stop = f.start(t)
			// Synchronous scheduler probes prove terminal manual evidence cannot be
			// replaced by an automatic new observation, including after restart.
			recovery := postgres.NewCompleteTVRuntimeStore(f.f.a.DBPool(), idgen.New())
			for range 3 {
				if e := recovery.RecoverCompleteTVJobs(context.Background()); e != nil {
					t.Fatal(e)
				}
			}
			stop()
			total, live := f.observationJobs(t)
			if total != 1 || live != 0 || f.lookups.Load() != 1 {
				t.Fatal("terminal observation rescheduled", total, live, f.lookups.Load())
			}
			f.invariants(t)
		})
	}
}
func TestCompleteTVRuntimeSchedulerConcurrentFreshLeaseAndRetainedPermit(t *testing.T) {
	f := newRuntimeTVFixture(t)
	if _, e := f.prepare(); e != nil {
		t.Fatal(e)
	}
	f.exec(t, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, f.c.Lease.ID)
	s := postgres.NewCompleteTVRuntimeStore(f.f.a.DBPool(), idgen.New())
	run := func() {
		t.Helper()
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for range 8 {
			wg.Add(1)
			go func() { defer wg.Done(); errs <- s.RecoverCompleteTVJobs(context.Background()) }()
		}
		wg.Wait()
		close(errs)
		for e := range errs {
			if e != nil {
				t.Fatal(e)
			}
		}
	}
	run()
	total, live := f.observationJobs(t)
	if total != 1 || live != 1 {
		t.Fatal("dedupe", total, live)
	}
	var old uuid.UUID
	var oldToken int64
	if e := f.f.a.DBPool().QueryRow(context.Background(), `UPDATE job SET state='running',lease_owner='old-observer',lease_token=7,lease_until=clock_timestamp()-interval '1 second' WHERE kind=$1 RETURNING id,lease_token`, ports.CompleteTVObservationJobKind).Scan(&old, &oldToken); e != nil {
		t.Fatal(e)
	}
	run()
	total, live = f.observationJobs(t)
	if total != 2 || live != 1 {
		t.Fatal("fresh observer not created", total, live)
	}
	var fenced bool
	if e := f.f.a.DBPool().QueryRow(context.Background(), `SELECT state='dead' AND lease_owner IS NULL AND lease_token>$2 FROM job WHERE id=$1`, old, oldToken).Scan(&fenced); e != nil || !fenced {
		t.Fatal("old observer not fenced", e)
	}
	// Retained permit is not an expired logical lease: it is never deleted or
	// treated as authority for a new network request by any scheduler instance.
	token := uuid.New()
	f.exec(t, `INSERT INTO complete_tv_physical_permit(operation_id,token,backend_pid,advisory_key) VALUES($1,$2,2147483647,1)`, f.c.OperationID, token)
	f.exec(t, `UPDATE job SET state='running',lease_owner='expired',lease_until=clock_timestamp()-interval '1 hour' WHERE kind=$1 AND state='queued'`, ports.CompleteTVObservationJobKind)
	run()
	total, live = f.observationJobs(t)
	if total != 2 || live != 0 {
		t.Fatal("orphan permit generated work", total, live)
	}
	var retained bool
	_ = f.f.a.DBPool().QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM complete_tv_physical_permit WHERE operation_id=$1 AND token=$2)`, f.c.OperationID, token).Scan(&retained)
	if !retained {
		t.Fatal("permit reclaimed")
	}
	stop := f.start(t)
	run()
	stop()
	if f.adds.Load() != 0 || f.lookups.Load() != 0 || f.fetches.Load() != 0 {
		t.Fatal("orphan permit caused network")
	}
	f.protected(t)
	// Only this test's synthetic permit is removed for fixture cleanup.
	f.exec(t, `DELETE FROM complete_tv_physical_permit WHERE operation_id=$1 AND token=$2`, f.c.OperationID, token)
}

func TestCompleteTVRuntimeAppMultiInstanceLeaseExpiryNoOverlap(t *testing.T) {
	f := newRuntimeTVFixture(t)
	f.entered = make(chan struct{})
	f.unblock = make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(f.unblock) })
	f.exec(t, `UPDATE job SET state='queued',lease_owner=NULL,lease_until=NULL WHERE id=$1`, f.c.Lease.ID)
	stop1 := f.start(t)
	select {
	case <-f.entered:
	case <-time.After(20 * time.Second):
		t.Fatal("Add not reached")
	}
	f.exec(t, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, f.c.Lease.ID)
	stop2 := f.start(t)
	recovery := postgres.NewCompleteTVRuntimeStore(f.f.a.DBPool(), idgen.New())
	if e := recovery.RecoverCompleteTVJobs(context.Background()); e != nil {
		t.Fatal(e)
	}
	var permit int
	_ = f.f.a.DBPool().QueryRow(context.Background(), `SELECT count(*) FROM complete_tv_physical_permit`).Scan(&permit)
	if permit != 1 || f.adds.Load() != 1 || f.lookups.Load() != 0 {
		t.Fatal("physical work overlapped", permit, f.adds.Load(), f.lookups.Load())
	}
	release.Do(func() { close(f.unblock) })
	runtimeTVWait(t, func() bool { return f.stateIs("tracking") })
	stop1()
	stop2()
	if f.adds.Load() != 1 || f.fetches.Load() != 1 || f.lookups.Load() != 1 {
		t.Fatal("expired Add replayed or not observed", f.adds.Load(), f.fetches.Load(), f.lookups.Load())
	}
	f.invariants(t)
}

func TestCompleteTVRuntimeAppStopDrainsPhysicalWriteback(t *testing.T) {
	f := newRuntimeTVFixture(t)
	f.entered = make(chan struct{})
	f.unblock = make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(f.unblock) })
	f.exec(t, `UPDATE job SET state='queued',lease_owner=NULL,lease_until=NULL WHERE id=$1`, f.c.Lease.ID)
	stop := f.start(t)
	select {
	case <-f.entered:
	case <-time.After(20 * time.Second):
		t.Fatal("Add not reached")
	}
	tx, e := f.f.a.DBPool().Begin(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, e = tx.Exec(context.Background(), `LOCK TABLE audit_log IN ACCESS EXCLUSIVE MODE`); e != nil {
		t.Fatal(e)
	}
	release.Do(func() { close(f.unblock) })
	runtimeTVWait(t, func() bool {
		var waiting bool
		_ = f.f.a.DBPool().QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query ILIKE '%INSERT INTO audit_log%')`).Scan(&waiting)
		return waiting
	})
	stopped := make(chan struct{})
	go func() { stop(); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("App closed while physical writeback owned resources")
	case <-time.After(100 * time.Millisecond):
	}
	var permit int
	_ = f.f.a.DBPool().QueryRow(context.Background(), `SELECT count(*) FROM complete_tv_physical_permit`).Scan(&permit)
	if permit != 1 {
		t.Fatal("shutdown dropped permit")
	}
	if e = tx.Rollback(context.Background()); e != nil {
		t.Fatal(e)
	}
	select {
	case <-stopped:
	case <-time.After(15 * time.Second):
		t.Fatal("App failed to finish drain")
	}
	if !f.stateIs("tracking") {
		t.Fatal("successful physical result lost during stop")
	}
	f.invariants(t)
}
