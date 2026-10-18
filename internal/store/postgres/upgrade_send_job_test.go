package postgres_test

import (
	"bytes"
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
	"sync"
	"testing"
	"time"
)

func sendJobFixture(t *testing.T, ack string) (*nativeUpgradeFixture, ports.MovieUpgradeSendCommandRepository, ports.MovieUpgradeNativeJobSendRepository, ports.QueueMovieUpgradeSend) {
	t.Helper()
	return sendJobFixtureFromNative(t, nativeUpgrade(t, ack, false))
}
func sendJobFixtureFromNative(t *testing.T, f *nativeUpgradeFixture) (*nativeUpgradeFixture, ports.MovieUpgradeSendCommandRepository, ports.MovieUpgradeNativeJobSendRepository, ports.QueueMovieUpgradeSend) {
	t.Helper()
	if v, e := postgres.Migrate(context.Background(), f.dsn, send71MigrationDir(t)); e != nil || v != 71 {
		t.Fatal(v, e)
	}
	command, e := postgres.NewMovieUpgradeSendCommandRepository(f.preparation)
	if e != nil {
		t.Fatal(e)
	}
	issuer := postgres.NewMovieUpgradeNativeDispatchVerifier()
	driver, e := download.NewMovieUpgradeNativeSendDriver(f.manager, issuer, nativeUpgradeClock{})
	if e != nil {
		t.Fatal(e)
	}
	repo, e := postgres.NewMovieUpgradeNativeJobSendRepository(f.preparation, driver, issuer)
	if e != nil {
		t.Fatal(e)
	}
	var task uuid.UUID
	if e = f.p.QueryRow(context.Background(), `SELECT task_id FROM movie_upgrade_preparation WHERE operation_id=$1`, f.operation).Scan(&task); e != nil {
		t.Fatal(e)
	}
	return f, command, repo, ports.QueueMovieUpgradeSend{OperationID: f.operation, TaskID: task, Confirmed: true}
}
func claimSendJob(t *testing.T, f *nativeUpgradeFixture, r *ports.MovieUpgradeSendJobReceipt) ports.MovieUpgradeSendJobRequest {
	t.Helper()
	q := jobs.NewQueue(f.p, idgen.New(), nativeUpgradeClock{})
	j, e := q.Claim(context.Background(), []string{ports.MovieUpgradeSendJobKind}, "send-job-test", 2*time.Minute)
	if e != nil || j.ID != r.JobID {
		t.Fatal(j.ID, e)
	}
	return ports.MovieUpgradeSendJobRequest{Lease: ports.JobLease{ID: j.ID, Owner: j.LeaseOwner.String, Token: j.LeaseToken}, OperationID: r.OperationID, TaskID: r.TaskID, CommandID: r.CommandID}
}
func TestMovieUpgradeSendJobExplicitCommandAndRecovery(t *testing.T) {
	f, c, _, req := sendJobFixture(t, "Ok.")
	ctx := context.Background()
	req.Confirmed = false
	if _, e := c.QueueMovieUpgradeSend(ctx, f.actor, req, "unconfirmed"); !errors.Is(e, ports.ErrUpgradeReservationConflict) {
		t.Fatal(e)
	}
	req.Confirmed = true
	if _, e := f.repo.SendMovieUpgrade(ctx, f.actor, req.OperationID, uuid.New(), f.proof, "missing-send-command"); !errors.Is(e, ports.ErrUpgradeReservationConflict) || f.adds.Load() != 0 {
		t.Fatal("unbound native send", e)
	}
	const n = 6
	receipts := make([]*ports.MovieUpgradeSendJobReceipt, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			receipts[i], errs[i] = c.QueueMovieUpgradeSend(ctx, f.actor, req, "explicit-send")
		}()
	}
	wg.Wait()
	for i, e := range errs {
		if e != nil || receipts[i].JobID != receipts[0].JobID || receipts[i].ExecutionAllowed || receipts[i].Status != "queued" {
			t.Fatal(i, receipts[i], e)
		}
	}
	if _, e := c.QueueMovieUpgradeSend(ctx, f.actor, req, "another-key"); !errors.Is(e, ports.ErrUpgradeReservationConflict) {
		t.Fatal(e)
	}
	var rows, arms int
	if e := f.p.QueryRow(ctx, `SELECT (SELECT count(*) FROM movie_upgrade_send_job),(SELECT count(*) FROM movie_upgrade_send_authorization)`).Scan(&rows, &arms); e != nil || rows != 1 || arms != 0 || f.adds.Load() != 0 {
		t.Fatal(rows, arms, f.adds.Load(), e)
	}
	if _, e := f.repo.SendMovieUpgrade(ctx, f.actor, f.operation, uuid.New(), f.proof, "generic-bypass"); !errors.Is(e, ports.ErrUpgradeReservationConflict) || f.adds.Load() != 0 {
		t.Fatal(e, f.adds.Load())
	}
}
func TestMovieUpgradeSendJobNativeAtomicResult(t *testing.T) {
	for _, tc := range []struct{ name, ack, status string }{{"confirmed", "Ok.", "confirmed"}, {"unknown", "Fails.", "unknown"}} {
		t.Run(tc.name, func(t *testing.T) {
			f, c, repo, req := sendJobFixture(t, tc.ack)
			ctx := context.Background()
			before := nativeProtectedGraph(t, f.preparedUpgradeFixture)
			queued, e := c.QueueMovieUpgradeSend(ctx, f.actor, req, "native-job")
			if e != nil {
				t.Fatal(e)
			}
			job := claimSendJob(t, f, queued)
			result, e := repo.SendMovieUpgradeJob(ctx, f.actor, job, f.proof)
			if e != nil || result.Status != tc.status || result.ExecutionAllowed {
				var claims, arms, dispatches, results int
				_ = f.p.QueryRow(ctx, `SELECT (SELECT count(*) FROM movie_upgrade_send_job_claim),(SELECT count(*) FROM movie_upgrade_send_authorization),(SELECT count(*) FROM movie_upgrade_send_dispatch),(SELECT count(*) FROM movie_upgrade_send_result)`).Scan(&claims, &arms, &dispatches, &results)
				t.Fatalf("send=%v error=%v claims=%d arm=%d dispatch=%d result=%d adds=%d", result, e, claims, arms, dispatches, results, f.adds.Load())
			}
			var state, status string
			var claims, arms, dispatches int
			e = f.p.QueryRow(ctx, `SELECT j.state,r.status,(SELECT count(*) FROM movie_upgrade_send_job_claim),(SELECT count(*) FROM movie_upgrade_send_authorization),(SELECT count(*) FROM movie_upgrade_send_dispatch) FROM job j JOIN movie_upgrade_send_job b ON b.job_id=j.id JOIN movie_upgrade_send_result r ON r.operation_id=b.operation_id WHERE j.id=$1`, queued.JobID).Scan(&state, &status, &claims, &arms, &dispatches)
			if e != nil || state != "succeeded" || status != tc.status || claims != 1 || arms != 1 || dispatches != 1 || f.adds.Load() != 1 {
				t.Fatal(state, status, claims, arms, dispatches, f.adds.Load(), e)
			}
			uploaded := <-f.received
			if !bytes.Equal(uploaded.raw, f.proof.Bytes()) {
				t.Fatal("torrent bytes changed")
			}
			if before != nativeProtectedGraph(t, f.preparedUpgradeFixture) {
				t.Fatal("original protections changed")
			}
			replay, e := c.QueueMovieUpgradeSend(ctx, f.actor, req, "native-job")
			if e != nil || replay.JobID != queued.JobID || replay.ExecutionAllowed {
				t.Fatal(replay, e)
			}
			if _, e = repo.SendMovieUpgradeJob(ctx, f.actor, job, f.proof); e == nil || f.adds.Load() != 1 {
				t.Fatal(e, f.adds.Load())
			}
		})
	}
}
func TestMovieUpgradeSendJobMissingCommandOrChangedClaim(t *testing.T) {
	f, c, repo, req := sendJobFixture(t, "Ok.")
	ctx := context.Background()
	if _, e := repo.SendMovieUpgradeJob(ctx, f.actor, ports.MovieUpgradeSendJobRequest{OperationID: f.operation, TaskID: req.TaskID}, f.proof); e == nil {
		t.Fatal("missing command accepted")
	}
	queued, e := c.QueueMovieUpgradeSend(ctx, f.actor, req, "changed-job")
	if e != nil {
		t.Fatal(e)
	}
	job := claimSendJob(t, f, queued)
	job.Lease.Token++
	if _, e = repo.SendMovieUpgradeJob(ctx, f.actor, job, f.proof); e == nil || f.adds.Load() != 0 {
		t.Fatal(e, f.adds.Load())
	}
	var claims int
	if e = f.p.QueryRow(ctx, `SELECT count(*) FROM movie_upgrade_send_job_claim`).Scan(&claims); e != nil || claims != 0 {
		t.Fatal(claims, e)
	}
}
