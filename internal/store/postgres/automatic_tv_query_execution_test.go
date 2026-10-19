package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func waitAutomaticTVBlocked(t *testing.T, pool *pgxpool.Pool, pid uint32) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if e := pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity a WHERE $1=ANY(pg_blocking_pids(a.pid)))`, pid).Scan(&blocked); e != nil {
			t.Fatal(e)
		}
		if blocked {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("callback did not wait on current database lock")
		}
	}
}
func TestAutomaticTVCurrentVersionsAfterLockWaitPG(t *testing.T) {
	for _, kind := range []string{"account", "unit", "source", "root", "client", "subscription"} {
		t.Run(kind, func(t *testing.T) {
			f := newAutomaticTVFixture(t)
			w := f.load(t, f.start(t))
			spec := automaticTVSpec(t, w, 0)
			tx, e := f.pool.Begin(t.Context())
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			var target uuid.UUID
			var table, change string
			switch kind {
			case "account":
				target = uuid.MustParse(f.actor.ID)
				table = "app_user"
				change = "is_active=false"
			case "unit":
				target = f.unit
				table = "media_unit"
				change = "metadata_version=metadata_version+1"
			case "source":
				target = f.indexer
				table = "indexer"
				change = "config_version=config_version+1"
			case "root":
				target = f.tv
				table = "library_root"
				change = "health_revision=health_revision+1"
			case "client":
				target = f.client
				table = "download_client"
				change = "url=url||'/changed'"
			case "subscription":
				target = f.sub
				table = "subscription"
				change = "state='paused'"
			}
			if _, e = tx.Exec(t.Context(), `SELECT id FROM `+table+` WHERE id=$1 FOR UPDATE`, target); e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
			defer cancel()
			done := make(chan error, 1)
			joined := false
			go func() {
				_, p, e := f.execution.AdmitQuery(ctx, spec)
				if p != nil {
					p.Close()
				}
				done <- e
			}()
			defer func() {
				_ = tx.Rollback(context.Background())
				cancel()
				if !joined {
					select {
					case <-done:
					case <-time.After(2 * time.Second):
						t.Error("callback did not join")
					}
				}
			}()
			waitAutomaticTVBlocked(t, f.pool, tx.Conn().PgConn().PID())
			if _, e = tx.Exec(t.Context(), `UPDATE `+table+` SET `+change+` WHERE id=$1`, target); e != nil {
				t.Fatal(e)
			}
			if e = tx.Commit(t.Context()); e != nil {
				t.Fatal(e)
			}
			select {
			case e = <-done:
				joined = true
				if !errors.Is(e, ports.ErrAcquisitionRoundAborted) {
					t.Fatal("stale current material admitted", e)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if intentCount(t, f.pool, `SELECT count(*) FROM indexer_query`) != 0 {
				t.Fatal("lock-wait stale callback wrote query")
			}
		})
	}
}
func TestAutomaticTVBudgetPhysicalDeferralAndFailurePG(t *testing.T) {
	for _, kind := range []string{"physical", "budget", "rate-limit"} {
		t.Run(kind, func(t *testing.T) {
			f := newAutomaticTVFixture(t)
			w := f.load(t, f.start(t))
			s := automaticTVSpec(t, w, 0)
			if kind == "rate-limit" {
				p := f.permit(t, s)
				if e := p.FailQuery(t.Context(), ports.SearchFailureRateLimited, 2*time.Minute); e != nil {
					t.Fatal(e)
				}
				p.Close()
				var delayed bool
				if e := f.pool.QueryRow(t.Context(), `SELECT next_query_at>clock_timestamp()+interval '1 minute' FROM indexer WHERE id=$1`, f.indexer).Scan(&delayed); e != nil || !delayed {
					t.Fatal("429 not durable", e)
				}
				if e := f.repo.FinishAutomaticTVSource(t.Context(), *w, ports.AutomaticTVSourceFailed); e != nil {
					t.Fatal(e)
				}
				return
			}
			var release func()
			if kind == "physical" {
				var e error
				release, e = postgres.AcquireIndexerPermit(t.Context(), f.pool, f.indexer)
				if e != nil {
					t.Fatal(e)
				}
				defer release()
			} else {
				execIntent(t, f.pool, `UPDATE indexer SET next_query_at=clock_timestamp()+interval '1 minute' WHERE id=$1`, f.indexer)
			}
			before := automaticTVState(t, f)
			_, p, e := f.execution.AdmitQuery(t.Context(), s)
			if p != nil {
				p.Close()
			}
			if !errors.Is(e, ports.ErrAutomaticTVSearchDeferred) || automaticTVState(t, f) != before {
				t.Fatal("deferral mutation or wrong category", e)
			}
			if e = f.repo.FinishAutomaticTVSource(t.Context(), *w, ports.AutomaticTVSourceDeferred); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestAutomaticTVStaleOwnerAndTokenCallbacksPG(t *testing.T) {
	for _, kind := range []string{"owner", "token", "state"} {
		for _, begun := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s-%t", kind, begun), func(t *testing.T) {
				f := newAutomaticTVFixture(t)
				w := f.load(t, f.start(t))
				s := automaticTVSpec(t, w, 0)
				var p ports.SearchExecutionPermit
				if begun {
					p = f.permit(t, s)
				}
				switch kind {
				case "owner":
					execIntent(t, f.pool, `UPDATE job SET lease_owner='other-owner' WHERE id=$1`, w.Task.Lease.ID)
				case "token":
					execIntent(t, f.pool, `UPDATE job SET lease_token = (lease_token + 1) WHERE id=$1`, w.Task.Lease.ID)
				case "state":
					execIntent(t, f.pool, `UPDATE job SET state='queued',lease_owner=NULL,lease_until=NULL WHERE id=$1`, w.Task.Lease.ID)
				}
				before := automaticTVState(t, f)
				if begun {
					if _, e := p.CommitQuery(t.Context(), automaticTVCommit(s, automaticTVCandidate(f, "stale"))); e == nil {
						t.Fatal("stale commit")
					}
					if e := p.FailQuery(t.Context(), ports.SearchFailureTimeout, 0); e == nil {
						t.Fatal("stale fail")
					}
				} else {
					_, permit, e := f.execution.AdmitQuery(t.Context(), s)
					if permit != nil {
						permit.Close()
					}
					if e == nil {
						t.Fatal("stale reserve")
					}
				}
				if e := f.repo.FinishAutomaticTVSource(t.Context(), *w, ports.AutomaticTVSourceFailed); e == nil {
					t.Fatal("stale finish")
				}
				if before != automaticTVState(t, f) {
					t.Fatal("stale callback accepted writes")
				}
			})
		}
	}
}
func TestAutomaticTVFinishTailAndPartialPreservationPG(t *testing.T) {
	for _, kind := range []string{"source-tail", "job-tail", "partial"} {
		t.Run(kind, func(t *testing.T) {
			f := newAutomaticTVFixture(t)
			w := f.load(t, f.start(t))
			s := automaticTVSpec(t, w, 0)
			p := f.permit(t, s)
			if _, e := p.CommitQuery(t.Context(), automaticTVCommit(s, automaticTVCandidate(f, "published"))); e != nil {
				t.Fatal(e)
			}
			p.Close()
			if kind == "partial" {
				if e := f.repo.FinishAutomaticTVSource(t.Context(), *w, ports.AutomaticTVSourceDeferred); e == nil {
					t.Fatal("deferred claimed after published page")
				}
				w.Deadline = w.Deadline.In(time.FixedZone("same-instant", 9*3600))
				if e := f.repo.FinishAutomaticTVSource(t.Context(), *w, ports.AutomaticTVSourceFailed); e != nil {
					t.Fatal(e)
				}
				if intentCount(t, f.pool, `SELECT count(*) FROM automatic_tv_source WHERE state='partial'`) != 1 || intentCount(t, f.pool, `SELECT count(*) FROM automatic_tv_query_candidate`) != 1 {
					t.Fatal("real pages not preserved as partial")
				}
				return
			}
			table := "automatic_tv_source"
			if kind == "job-tail" {
				table = "job"
			}
			execIntent(t, f.pool, `CREATE FUNCTION tv_finish_delay() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(0.2); RETURN NEW; END $$; CREATE TRIGGER tv_finish_delay AFTER UPDATE ON `+table+` FOR EACH ROW EXECUTE FUNCTION tv_finish_delay()`)
			execIntent(t, f.pool, `UPDATE job SET lease_until=clock_timestamp()+interval '100 milliseconds' WHERE id=$1`, w.Task.Lease.ID)
			// The setup UPDATE also fires a job trigger, so give this attempt a fresh short lease without it.
			if kind == "job-tail" {
				execIntent(t, f.pool, `ALTER TABLE job DISABLE TRIGGER tv_finish_delay`)
				execIntent(t, f.pool, `UPDATE job SET lease_until=clock_timestamp()+interval '100 milliseconds' WHERE id=$1`, w.Task.Lease.ID)
				execIntent(t, f.pool, `ALTER TABLE job ENABLE TRIGGER tv_finish_delay`)
			}
			before := automaticTVState(t, f)
			if e := f.repo.FinishAutomaticTVSource(t.Context(), *w, ports.AutomaticTVSourceSucceeded); !errors.Is(e, ports.ErrAcquisitionRoundAborted) {
				t.Fatal("late finish", e)
			}
			if before != automaticTVState(t, f) {
				t.Fatal("finish tail leaked source/job writes")
			}
		})
	}
}
func TestAutomaticTVR0RecoveryDoesNotReplayPausedScopePG(t *testing.T) {
	f := newAutomaticTVFixture(t)
	w := f.load(t, f.start(t))
	execIntent(t, f.pool, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, w.Task.Lease.ID)
	execIntent(t, f.pool, `UPDATE subscription SET state='paused' WHERE id=$1`, f.sub)
	n, e := f.repo.RecoverAutomaticTVSources(t.Context(), 10)
	if e != nil || n != 1 {
		t.Fatal(n, e)
	}
	if intentCount(t, f.pool, `SELECT count(*) FROM job WHERE kind='automatic_tv_source' AND state='queued'`) != 0 {
		t.Fatal("paused R0 replayed")
	}
	for _, limit := range []int32{0, 101} {
		if _, e = f.repo.RecoverAutomaticTVSources(t.Context(), limit); !errors.Is(e, ports.ErrRejected) {
			t.Fatal("unbounded recovery", e)
		}
	}
}
func TestAutomaticTVCrossRoundQueryCannotBeSignedPG(t *testing.T) {
	f := newAutomaticTVFixture(t)
	w := f.load(t, f.start(t))
	s := automaticTVSpec(t, w, 0)
	p := f.permit(t, s)
	in := creationInput(55102, "tv")
	in.IndexerIDs = []uuid.UUID{f.indexer}
	in.DownloaderID = &f.client
	season := creationSeason(2, 3)
	aired := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	season.Episodes[0].AirDate = &aired
	season.Episodes[0].TMDBEpisodeID = 20203
	receipt := f.create(t, in, creationTV(55102, season), "other-tv")
	r, e := f.repo.StartAutomaticTVRound(t.Context(), ports.AutomaticTVRoundStart{WantedID: receipt.Wanted[0].ID})
	if e != nil {
		t.Fatal(e)
	}
	other := f.load(t, r)
	otherSpec := automaticTVSpec(t, other, 0)
	tx, e := f.pool.Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if e = postgres.LinkAutomaticTVSearchQuery(t.Context(), db.New(tx), *otherSpec.AutomaticTV, s.ID); e == nil {
		t.Fatal("foreign query signed")
	}
	p.Close()
}
