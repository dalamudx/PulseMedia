package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/jackc/pgx/v5/pgconn"
)

func r3JobNative(t *testing.T) *nativeUpgradeFixture {
	t.Helper()
	n := r3Native(t, "ambiguous", true)
	if version, err := postgres.Migrate(context.Background(), n.dsn, observation70MigrationDir(t)); err != nil || version != 70 {
		t.Fatalf("schema70 migration: %d %v", version, err)
	}
	return n
}

func TestMovieUpgradeObservationJobClaimCapturePG(t *testing.T) {
	n := r3JobNative(t)
	f := newNativeObservation(t, n, true)
	req, _ := claimedObservationJob(t, n)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := nativeJobObserver(t, n).ObserveMovieUpgradeJob(ctx, n.actor, req); done <- err }()
	drained := false
	defer func() {
		f.unblock()
		if !drained {
			if err := <-done; err != nil {
				t.Error("drain", err)
			}
		}
	}()
	select {
	case <-f.entered:
	case err := <-done:
		drained = true
		t.Fatalf("query not reached: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	const insert = `INSERT INTO movie_upgrade_observation_job SELECT (jsonb_populate_record(NULL::movie_upgrade_observation_job,to_jsonb(b)||jsonb_build_object('claimed_job',b.claimed_job #- $2::text[]))).* FROM movie_upgrade_observation_job b WHERE job_id=$1`
	for _, field := range []string{"state", "kind", "lease_owner", "lease_token", "lease_until", "attempts", "payload"} {
		t.Run(field, func(t *testing.T) {
			_, err := n.p.Exec(ctx, insert, req.Lease.ID, []string{field})
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "movie_upgrade_observation_job_claim" {
				t.Fatalf("missing claim field did not hit capture constraint: %v", err)
			}
		})
	}
	_, err := n.p.Exec(ctx, `INSERT INTO movie_upgrade_observation_job SELECT (jsonb_populate_record(NULL::movie_upgrade_observation_job,to_jsonb(b)||jsonb_build_object('claimed_job',jsonb_set(b.claimed_job,'{state}','null'::jsonb)))).* FROM movie_upgrade_observation_job b WHERE job_id=$1`, req.Lease.ID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != "movie_upgrade_observation_job_claim" {
		t.Fatal("null claim state accepted", err)
	}
	_, err = n.p.Exec(ctx, `INSERT INTO movie_upgrade_observation_job SELECT * FROM movie_upgrade_observation_job WHERE job_id=$1`, req.Lease.ID)
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatal("valid control did not reach uniqueness", err)
	}
	for name, statement := range map[string]string{
		"heartbeat":         `UPDATE job SET lease_until=lease_until+interval '1 minute',updated_at=clock_timestamp() WHERE id=$1`,
		"claim_replacement": `UPDATE job SET lease_owner='replacement',lease_token=lease_token+1,updated_at=clock_timestamp() WHERE id=$1`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := n.p.Exec(ctx, statement, req.Lease.ID)
			var frozen *pgconn.PgError
			if !errors.As(err, &frozen) || frozen.Code != "23514" || frozen.ConstraintName != "movie_upgrade_observation_job_frozen" {
				t.Fatalf("claim mutation did not hit SQL fence: %v", err)
			}
		})
	}
	f.unblock()
	err = <-done
	drained = true
	if err != nil {
		t.Fatal("valid original claim failed", err)
	}
}

func TestMovieUpgradeObservationJobPhysicalLeaseExpiryPG(t *testing.T) {
	n := r3JobNative(t)
	f := newNativeObservation(t, n, true)
	req, _ := claimedObservationJob(t, n)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if _, err := n.p.Exec(ctx, `UPDATE job SET lease_until=clock_timestamp()+interval '2 seconds',updated_at=clock_timestamp() WHERE id=$1`, req.Lease.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := nativeJobObserver(t, n).ObserveMovieUpgradeJob(ctx, n.actor, req); done <- err }()
	drained := false
	defer func() {
		f.unblock()
		if !drained {
			<-done
		}
	}()
	select {
	case <-f.entered:
	case err := <-done:
		drained = true
		t.Fatalf("query not reached: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := n.p.Exec(ctx, `SELECT pg_sleep(GREATEST(0,extract(epoch FROM lease_until-clock_timestamp()))+0.02) FROM job WHERE id=$1`, req.Lease.ID); err != nil {
		t.Fatal(err)
	}
	f.unblock()
	err := <-done
	drained = true
	if err == nil {
		t.Fatal("expired physical query completed job")
	}
	var intact bool
	err = n.p.QueryRow(ctx, `SELECT j.state='running' AND NOT EXISTS(SELECT 1 FROM movie_upgrade_observation_result WHERE observation_id=b.observation_id) AND EXISTS(SELECT 1 FROM movie_upgrade_observation_permit WHERE observation_id=b.observation_id) FROM job j JOIN movie_upgrade_observation_job b ON b.job_id=j.id WHERE j.id=$1`, req.Lease.ID).Scan(&intact)
	if err != nil || !intact || f.queries.Load() != 1 {
		t.Fatalf("expired return changed terminal state or lost permit: %v", err)
	}
}

func TestMovieUpgradeObservationJobDeadWhileQueryPG(t *testing.T) {
	n := r3JobNative(t)
	f := newNativeObservation(t, n, true)
	req, queue := claimedObservationJob(t, n)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := nativeJobObserver(t, n).ObserveMovieUpgradeJob(ctx, n.actor, req); done <- err }()
	drained := false
	defer func() {
		f.unblock()
		if !drained {
			<-done
		}
	}()
	select {
	case <-f.entered:
	case err := <-done:
		drained = true
		t.Fatalf("query not reached: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := queue.Fail(ctx, req.Lease.ID, req.Lease.Owner, req.Lease.Token, "manual review", time.Second); err != nil {
		t.Fatal("safe terminal failure rejected", err)
	}
	f.unblock()
	err := <-done
	drained = true
	if err == nil {
		t.Fatal("terminated query published success")
	}
	var intact bool
	err = n.p.QueryRow(ctx, `SELECT j.state='dead' AND NOT EXISTS(SELECT 1 FROM movie_upgrade_observation_result WHERE observation_id=b.observation_id) AND EXISTS(SELECT 1 FROM movie_upgrade_observation_permit WHERE observation_id=b.observation_id) FROM job j JOIN movie_upgrade_observation_job b ON b.job_id=j.id WHERE j.id=$1`, req.Lease.ID).Scan(&intact)
	if err != nil || !intact || f.queries.Load() != 1 {
		t.Fatal("dead job lost persistent permit or wrote result", err)
	}
}
