package httpapi_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
)

type trackingBarrier struct {
	ports.CompleteTVTrackingRepository
	entered chan struct{}
	release chan struct{}
}

func (b *trackingBarrier) CommitCompleteTVTracking(ctx context.Context, s ports.CompleteTVTrackingSnapshot, o ports.CompleteTVTrackingObservation) (*ports.CompleteTVTrackingReceipt, error) {
	close(b.entered)
	<-b.release
	return b.CompleteTVTrackingRepository.CommitCompleteTVTracking(ctx, s, o)
}
func TestCompleteTVTrackingPGWaitExpiry(t *testing.T) {
	for _, table := range []string{"app_user", "download_task", "job", "acquisition_reservation", "unit_reservation", "download_requirement", "download_client"} {
		t.Run(table, func(t *testing.T) {
			n := newTrackingFixture(t)
			b := &trackingBarrier{n.store, make(chan struct{}), make(chan struct{})}
			var once sync.Once
			unblock := func() { once.Do(func() { close(b.release) }) }
			done := make(chan error, 1)
			go func() {
				_, e := n.manager.ObserveCompleteTVTracking(context.Background(), b, torrent.Inspector{}, n.request)
				done <- e
			}()
			joined := false
			defer func() {
				unblock()
				if !joined {
					<-done
				}
			}()
			select {
			case <-b.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("no commit")
			}
			n.exec(t, `UPDATE job SET lease_until=clock_timestamp()+interval '1 second' WHERE id=$1`, n.request.Lease.ID)
			before := n.snapshot(t)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			tx, e := n.f.a.DBPool().Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			lock := `SELECT 1 FROM ` + table + ` FOR UPDATE`
			if table == "job" {
				lock = `SELECT 1 FROM job WHERE kind='complete_tv_coverage_tracking' FOR UPDATE`
			}
			rows, e := tx.Query(ctx, lock)
			if e != nil {
				t.Fatal(e)
			}
			rows.Close()
			unblock()
			waiting := false
			for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
				var count int
				e = n.f.a.DBPool().QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock'`).Scan(&count)
				if e != nil {
					t.Fatal(e)
				}
				if count > 0 {
					waiting = true
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if !waiting {
				t.Fatal("did not reach native row wait")
			}
			if _, e = tx.Exec(ctx, `SELECT pg_sleep(GREATEST(0,(SELECT extract(epoch FROM lease_until-clock_timestamp()) FROM job WHERE id=$1))+0.05)`, n.request.Lease.ID); e != nil {
				t.Fatal(e)
			}
			if e = tx.Rollback(ctx); e != nil {
				t.Fatal(e)
			}
			e = <-done
			joined = true
			if !errors.Is(e, ports.ErrCompleteTVObservationStale) || n.snapshot(t) != before {
				t.Fatal("late lease accepted", e)
			}
		})
	}
}
func TestCompleteTVTrackingNativePGCommitOwnership(t *testing.T) {
	n := newTrackingFixture(t)
	b := &trackingBarrier{n.store, make(chan struct{}), make(chan struct{})}
	var once sync.Once
	unblock := func() { once.Do(func() { close(b.release) }) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, e := n.manager.ObserveCompleteTVTracking(ctx, b, torrent.Inspector{}, n.request); done <- e }()
	joined := false
	defer func() {
		unblock()
		if !joined {
			<-done
		}
	}()
	select {
	case <-b.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no commit")
	}
	var client uuid.UUID
	if e := n.f.a.DBPool().QueryRow(context.Background(), `SELECT client_id FROM download_task WHERE id=$1`, n.request.TaskID).Scan(&client); e != nil {
		t.Fatal(e)
	}
	c := &observerBlockingClient{testEntered: make(chan struct{}, 2), release: b.release}
	n.manager.RegisterClient(client, c)
	testDone := make(chan error, 1)
	go func() { _, _, e := n.manager.TestClient(context.Background(), client); testDone <- e }()
	defer func() { unblock(); <-testDone }()
	select {
	case <-c.testEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("second slot unavailable")
	}
	limit, stop := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer stop()
	if _, _, e := n.manager.TestClient(limit, client); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal("client budget released before commit", e)
	}
	cancel()
	select {
	case e := <-done:
		joined = true
		t.Fatal("cancellation released in-flight fact commit", e)
	default:
	}
	if r, e := n.observe(context.Background()); r != nil || !errors.Is(e, ports.ErrCompleteTVNeedsObservation) {
		t.Fatal("permit released", r, e)
	}
	unblock()
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	joined = true
	n.assert(t, "completed")
}
func TestCompleteTVTrackingNativePGLostSession(t *testing.T) {
	n := newTrackingFixture(t)
	n.hook = func(p string) {
		if strings.HasSuffix(p, "/files") {
			var killed bool
			e := n.f.a.DBPool().QueryRow(context.Background(), `SELECT pg_terminate_backend(backend_pid) FROM complete_tv_physical_permit WHERE operation_id=$1`, n.request.OperationID).Scan(&killed)
			if e != nil || !killed {
				t.Error("did not terminate original session", e)
			}
		}
	}
	before := n.snapshot(t)
	r, e := n.observe(context.Background())
	if r != nil || !errors.Is(e, ports.ErrUnavailable) || n.snapshot(t) != before {
		t.Fatal("lost session accepted", r, e)
	}
}
func TestCompleteTVTrackingNativePGRetainedPermit(t *testing.T) {
	n := newTrackingFixture(t)
	token := uuid.New()
	n.exec(t, `INSERT INTO complete_tv_physical_permit(operation_id,token,backend_pid,advisory_key,created_at) VALUES($1,$2,1,1,clock_timestamp()-interval '1 year')`, n.request.OperationID, token)
	before := n.snapshot(t)
	r, e := n.observe(context.Background())
	if r != nil || !errors.Is(e, ports.ErrCompleteTVNeedsObservation) || n.snapshot(t) != before || n.lookups.Load() != 0 {
		t.Fatal("retained permit ignored", r, e)
	}
	var saved uuid.UUID
	if e = n.f.a.DBPool().QueryRow(context.Background(), `SELECT token FROM complete_tv_physical_permit WHERE operation_id=$1`, n.request.OperationID).Scan(&saved); e != nil || saved != token {
		t.Fatal("foreign permit cleaned", e)
	}
}
