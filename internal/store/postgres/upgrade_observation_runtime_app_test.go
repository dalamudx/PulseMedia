package postgres_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/app"
	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func observationRuntimeAppNative(t *testing.T) *nativeUpgradeFixture {
	t.Helper()
	root := t.TempDir()
	n := nativeUpgrade(t, "ambiguous", false, func(f *frozenUpgradeFixture) {
		f.exec(t, `UPDATE library_root SET path=$2 WHERE id=$1`, f.root, root)

		f.exec(t, `UPDATE library_root SET baseline_completed=true,health='online' WHERE id=$1`, f.root)
		f.project(t)
	})
	if _, e := n.repo.SendMovieUpgrade(context.Background(), n.actor, n.operation, uuid.New(), n.proof, "native-result"); e != nil {
		t.Fatal(e)
	}
	if v, e := postgres.Migrate(context.Background(), n.dsn, observation70MigrationDir(t)); e != nil || v != 70 {
		t.Fatal(v, e)
	}
	return n
}
func startObservationRuntimeApp(t *testing.T, n *nativeUpgradeFixture, data string) func() {
	t.Helper()
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	addr := listener.Addr().String()
	if e = listener.Close(); e != nil {
		t.Fatal(e)
	}
	cfg := config.NewDefault()
	cfg.DataDir = data
	cfg.Database.URL = n.dsn
	cfg.SecretKey = strings.Repeat("%", 32)
	cfg.Server.HTTPAddr = addr
	a, e := app.New(cfg, app.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var runErr error
	go func() { defer close(done); runErr = a.Run(ctx) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
				if runErr != nil && !errors.Is(runErr, context.Canceled) {
					t.Error(runErr)
				}
			case <-time.After(12 * time.Second):
				t.Error("App did not drain")
				return
			}
			if e := a.Close(); e != nil {
				t.Error(e)
			}
		})
	}
	t.Cleanup(stop)
	deadline := time.NewTimer(12 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	client := &http.Client{Timeout: time.Second}
	for {
		select {
		case <-done:
			t.Fatalf("App stopped before readiness: %v", runErr)
		case <-deadline.C:
			t.Fatal("App readiness timeout")
		case <-ticker.C:
			r, e := client.Get("http://" + addr + "/healthz")
			if e == nil {
				closeErr := r.Body.Close()
				if closeErr != nil {
					t.Fatal(closeErr)
				}
				if r.StatusCode == http.StatusOK {
					return stop
				}
			}
		}
	}
}
func TestMovieUpgradeObservationAppRunAndRestartPG(t *testing.T) {
	n := observationRuntimeAppNative(t)
	data := filepath.Join(t.TempDir(), "data")
	before := nativeProtectedGraph(t, n.preparedUpgradeFixture)
	old := n.oldState(t)
	stop := startObservationRuntimeApp(t, n, data)
	deadline := time.NewTimer(12 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		var success bool
		e := n.p.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM movie_upgrade_observation_job b JOIN job j ON j.id=b.job_id JOIN movie_upgrade_observation_result r ON r.observation_id=b.observation_id WHERE b.operation_id=$1 AND j.state='succeeded' AND r.disposition='manual_review' AND to_jsonb(j)=pulse_upgrade_observation_terminal_job(b.observation_id))`, n.operation).Scan(&success)
		if e != nil {
			t.Fatal(e)
		}
		if success {
			break
		}
		select {
		case <-deadline.C:
			t.Fatalf("no atomic result; query=%d", n.observationQueries.Load())
		case <-ticker.C:
		}
	}
	stop()
	if n.observationQueries.Load() != 1 || n.adds.Load() != 1 || before != nativeProtectedGraph(t, n.preparedUpgradeFixture) || old != n.oldState(t) {
		t.Fatal("native I/O repeated or old protection changed")
	}
	stopAgain := startObservationRuntimeApp(t, n, data)
	timer := time.NewTimer(200 * time.Millisecond)
	<-timer.C
	stopAgain()
	if n.observationQueries.Load() != 1 || n.adds.Load() != 1 {
		t.Fatal("restart replayed query or Add")
	}
	var count int
	if e := n.p.QueryRow(context.Background(), `SELECT count(*) FROM job WHERE kind=$1`, ports.MovieUpgradeObservationJobKind).Scan(&count); e != nil || count != 1 {
		t.Fatal("restart lost permanent dedupe", count, e)
	}
}
func TestMovieUpgradeObservationAppDoesNotAdoptExpiredPG(t *testing.T) {
	n := observationRuntimeAppNative(t)
	if count, e := runtimeObservation(t, n).ScheduleMovieUpgradeObservations(context.Background()); e != nil || count != 1 {
		t.Fatal(count, e)
	}
	req := runtimeObservationClaim(t, n)
	n.exec(t, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, req.Lease.ID)
	var before, after string
	if e := n.p.QueryRow(context.Background(), `SELECT to_jsonb(j)::text FROM job j WHERE id=$1`, req.Lease.ID).Scan(&before); e != nil {
		t.Fatal(e)
	}
	stop := startObservationRuntimeApp(t, n, filepath.Join(t.TempDir(), "data"))
	timer := time.NewTimer(200 * time.Millisecond)
	<-timer.C
	stop()
	if e := n.p.QueryRow(context.Background(), `SELECT to_jsonb(j)::text FROM job j WHERE id=$1`, req.Lease.ID).Scan(&after); e != nil {
		t.Fatal(e)
	}
	if before != after || n.observationQueries.Load() != 0 || n.adds.Load() != 1 {
		t.Fatal("App adopted expired claim or repeated I/O")
	}
}
