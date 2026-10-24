package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// This verifies the persistent physical ledger against a genuinely ingested
// task. Native removal and the worker lifecycle are separate acceptance checks.
func m10SeedingExecutionLedger(t *testing.T, f *organizationFixture, task, job, client uuid.UUID) {
	t.Helper()
	conn, err := f.pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	held := false
	alive := true
	t.Cleanup(func() {
		if !alive {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if held {
			if ok, e := db.New(conn).ReleaseSeedingExecutionGate(ctx, client); e != nil || !ok {
				raw := conn.Hijack()
				_ = raw.Close(ctx)
				return
			}
		}
		conn.Release()
	})
	prepare := func(tx pgx.Tx, token int64, observe bool) (db.SeedingRemovalExecution, error) {
		if _, e := tx.Exec(t.Context(), `UPDATE job SET state='running',attempts=$2::bigint::integer,lease_token=$2::bigint,lease_owner='physical-ledger',lease_until=clock_timestamp()+interval '2 minutes' WHERE id=$1`, job, token); e != nil {
			return db.SeedingRemovalExecution{}, e
		}
		q := db.New(tx)
		if n, e := q.StartSeedingRemovalSchedule(t.Context(), db.StartSeedingRemovalScheduleParams{TaskID: task, ActiveJobID: pgtype.UUID{Bytes: job, Valid: true}, ConfigVersion: 1}); e != nil || n != 1 {
			if e == nil {
				e = errors.New("missing active schedule")
			}
			return db.SeedingRemovalExecution{}, e
		}
		return q.OpenSeedingExecution(t.Context(), db.OpenSeedingExecutionParams{JobID: job, LeaseToken: token, LeaseOwner: "physical-ledger", ClientID: client, ObserveOnly: observe})
	}
	for _, scenario := range []string{"missing-gate", "wrong-owner", "expired-lease", "observation-without-permit", "isolation"} {
		t.Run(scenario, func(t *testing.T) {
			if scenario != "missing-gate" {
				held, err = db.New(conn).TrySeedingExecutionGate(t.Context(), client)
				if err != nil || !held {
					t.Fatal("gate", err)
				}
			}
			tx, e := conn.Begin(t.Context())
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = tx.Rollback(context.WithoutCancel(t.Context())) }()
			if _, e = prepare(tx, 1, scenario == "observation-without-permit"); e != nil {
				t.Fatal(e)
			}
			switch scenario {
			case "wrong-owner":
				_, e = tx.Exec(t.Context(), `UPDATE job SET lease_owner='changed' WHERE id=$1`, job)
			case "expired-lease":
				_, e = tx.Exec(t.Context(), `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, job)
			case "isolation":
				_, e = tx.Exec(t.Context(), `UPDATE restore_isolation SET isolated=true,restore_id=gen_random_uuid() WHERE singleton`)
			}
			if e != nil {
				t.Fatal(e)
			}
			if e = tx.Commit(t.Context()); e == nil {
				t.Fatal("invalid physical authority committed")
			}
			if held {
				released, e := db.New(conn).ReleaseSeedingExecutionGate(t.Context(), client)
				if e != nil || !released {
					t.Fatal(e)
				}
				held = false
			}
			if _, e = db.New(f.pool).LatestSeedingExecution(t.Context(), job); !errors.Is(e, pgx.ErrNoRows) {
				t.Fatal("failed admission left execution", e)
			}
		})
	}
	held, err = db.New(conn).TrySeedingExecutionGate(t.Context(), client)
	if err != nil || !held {
		t.Fatal(err)
	}
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = prepare(tx, 1, false); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal("physical execution admission", err)
	}
	other, err := f.pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Release()
	otherQ := db.New(other)
	if locked, e := otherQ.TrySeedingExecutionGate(t.Context(), client); e != nil || locked {
		if locked {
			_, _ = otherQ.ReleaseSeedingExecutionGate(t.Context(), client)
		}
		t.Fatal("physical overlap", e)
	}
	for i := 0; i < 3; i++ {
		if _, err = db.New(conn).ReserveSeedingExecutionRequest(t.Context(), db.ReserveSeedingExecutionRequestParams{JobID: job, LeaseToken: 1, LeaseOwner: "physical-ledger"}); err != nil {
			t.Fatal("request evidence", err)
		}
	}
	if _, err = db.New(conn).ReserveSeedingExecutionRequest(t.Context(), db.ReserveSeedingExecutionRequestParams{JobID: job, LeaseToken: 1, LeaseOwner: "physical-ledger"}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("request cap bypass", err)
	}
	if _, err = f.pool.Exec(t.Context(), `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, job); err != nil {
		t.Fatal(err)
	}
	// Logical expiry does not release the gate. Only joined I/O may record release.
	if locked, e := otherQ.TrySeedingExecutionGate(t.Context(), client); e != nil || locked {
		if locked {
			_, _ = otherQ.ReleaseSeedingExecutionGate(t.Context(), client)
		}
		t.Fatal("expired lease released physical exclusion", e)
	}
	if n, e := db.New(conn).ReleaseSeedingExecution(t.Context(), db.ReleaseSeedingExecutionParams{JobID: job, LeaseToken: 1, LeaseOwner: "physical-ledger"}); e != nil || n != 1 {
		t.Fatal("expired owner could not record joined I/O", e)
	}
	if ok, e := db.New(conn).ReleaseSeedingExecutionGate(t.Context(), client); e != nil || !ok {
		t.Fatal(e)
	}
	held = false
	first, err := db.New(conn).GetSeedingExecution(t.Context(), db.GetSeedingExecutionParams{JobID: job, LeaseToken: 1})
	if err != nil || !first.ReleasedAt.Valid || first.Requests != 3 {
		t.Fatal("physical release evidence", err)
	}
	if _, err = f.pool.Exec(t.Context(), `UPDATE seeding_removal_execution SET released_at=NULL WHERE job_id=$1`, job); err == nil {
		t.Fatal("release evidence erased")
	}
	held, err = db.New(conn).TrySeedingExecutionGate(t.Context(), client)
	if err != nil || !held {
		t.Fatal(err)
	}
	tx, err = conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = prepare(tx, 2, false); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal("new read lease after known release", err)
	}
	if _, err = db.New(conn).ReserveSeedingExecutionRequest(t.Context(), db.ReserveSeedingExecutionRequestParams{JobID: job, LeaseToken: 2, LeaseOwner: "physical-ledger"}); err != nil {
		t.Fatal(err)
	}
	raw := conn.Hijack()
	alive = false
	held = false
	if err = raw.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	locked, err := otherQ.TrySeedingExecutionGate(t.Context(), client)
	if err != nil || !locked {
		t.Fatal("lost backend gate not released", err)
	}
	defer func() { _, _ = otherQ.ReleaseSeedingExecutionGate(context.WithoutCancel(t.Context()), client) }()
	orphan, err := otherQ.UnreleasedSeedingExecution(t.Context(), client)
	if err != nil || orphan.LeaseToken != 2 || orphan.Requests != 1 || orphan.ReleasedAt.Valid {
		t.Fatal("lost session was treated as finished I/O", err)
	}
	tx, err = other.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, err = prepare(tx, 3, false)
	_ = tx.Rollback(context.WithoutCancel(t.Context()))
	if err == nil {
		t.Fatal("unconfirmed I/O permitted another physical execution")
	}
	t.Log("M10_SEEDING_PHYSICAL_LEDGER_PG_PASS admission=COMMIT_FENCED expired_lease=STILL_EXCLUSIVE normal_release=DURABLE lost_session=UNCONFIRMED_AND_BLOCKED native_IO=NOT_EXECUTED")
}
