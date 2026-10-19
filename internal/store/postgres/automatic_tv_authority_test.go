package postgres_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/google/uuid"
)

func TestAutomaticTVFinalizerR0RecoveryPG(t *testing.T) {
	for _, kind := range []string{"expired", "live", "wrong-payload", "wrong-kind", "wrong-dedupe", "exhausted", "owner-revoked", "intent-changed", "tail-fault"} {
		t.Run(kind, func(t *testing.T) {
			f := newTVExecutionFixture(t)
			r := f.start(t)
			lease := tvExecutionLease(t, f, r)
			repo := tvExecutionStore(f, acquisition.SelectAutomaticTVEpisode)
			if kind != "live" {
				execIntent(t, f.pool, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, lease.ID)
			}
			switch kind {
			case "wrong-payload":
				execIntent(t, f.pool, `UPDATE job SET payload=payload||'{"extra":true}'::jsonb WHERE id=$1`, lease.ID)
			case "wrong-kind":
				execIntent(t, f.pool, `UPDATE job SET kind='search_round_finalize' WHERE id=$1`, lease.ID)
			case "wrong-dedupe":
				execIntent(t, f.pool, `UPDATE job SET dedupe_key='foreign' WHERE id=$1`, lease.ID)
			case "exhausted":
				execIntent(t, f.pool, `UPDATE job SET attempts=max_attempts WHERE id=$1`, lease.ID)
			case "owner-revoked":
				execIntent(t, f.pool, `UPDATE app_user SET is_active=false WHERE id=$1`, uuid.MustParse(f.actor.ID))
			case "intent-changed":
				execIntent(t, f.pool, `UPDATE subscription SET intent_version=intent_version+1 WHERE id=$1`, f.sub)
			case "tail-fault":
				execIntent(t, f.pool, `UPDATE job SET attempts=max_attempts WHERE id=$1`, lease.ID)
				execIntent(t, f.pool, `CREATE FUNCTION tv_r0_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_type='automatic_tv.finalized' THEN RAISE EXCEPTION 'R0 tail'; END IF; RETURN NEW; END $$; CREATE TRIGGER tv_r0_fault BEFORE INSERT ON outbox_event FOR EACH ROW EXECUTE FUNCTION tv_r0_fault()`)
			}
			before := tvExecutionState(t, f)
			n, e := repo.RecoverAutomaticTVFinalizers(t.Context(), 10)
			switch kind {
			case "live", "wrong-kind":
				if e != nil || n != 0 || before != tvExecutionState(t, f) {
					t.Fatal("unrelated recovery mutation", n, e)
				}
				return
			case "wrong-payload", "wrong-dedupe":
				if !errors.Is(e, ports.ErrAcquisitionRoundAborted) || n != 0 || before != tvExecutionState(t, f) {
					t.Fatal("foreign job recovered", n, e)
				}
				return
			case "tail-fault":
				if !errors.Is(e, ports.ErrUnavailable) || n != 0 || before != tvExecutionState(t, f) {
					t.Fatal("R0 tail rollback", n, e)
				}
				return
			}
			if e != nil || n != 1 {
				t.Fatal("recover", n, e)
			}
			tvExecutionCounts(t, f, 0)
			if n, e = repo.RecoverAutomaticTVFinalizers(t.Context(), 10); e != nil || n != 0 {
				t.Fatal("recovery is not stable", n, e)
			}
			if _, e = repo.FinalizeAutomaticTVRound(t.Context(), r.ID, lease); !errors.Is(e, ports.ErrAcquisitionRoundAborted) {
				t.Fatal("old token survived recovery", e)
			}
			if kind == "expired" {
				if n := intentCount(t, f.pool, `SELECT count(*) FROM job WHERE id=$1 AND state='queued' AND lease_token=$2 AND attempts=1 AND lease_owner IS NULL AND lease_until IS NULL`, lease.ID, lease.Token+1); n != 1 {
					t.Fatal("original job not fenced/requeued")
				}
				tvExecutionPage(t, f, r, tvExecutionCandidate(t, f, "recovered", "1080p", 1))
				next := tvExecutionLease(t, f, r)
				if next.ID != lease.ID || next.Token <= lease.Token {
					t.Fatal("recovery replaced original job")
				}
				d, e := repo.FinalizeAutomaticTVRound(t.Context(), r.ID, next)
				if e != nil || d.State != "reserved" {
					t.Fatal("reclaimed original finalize", d, e)
				}
				before = tvExecutionState(t, f)
				if n, e = repo.RecoverAutomaticTVFinalizers(t.Context(), 10); e != nil || n != 0 || before != tvExecutionState(t, f) {
					t.Fatal("reserved authority re-signed", n, e)
				}
			} else {
				reason := map[string]string{"exhausted": "retry_exhausted", "owner-revoked": "owner_revoked", "intent-changed": "target_changed"}[kind]
				if n := intentCount(t, f.pool, `SELECT count(*) FROM automatic_tv_decision d JOIN automatic_tv_round r ON r.id=d.round_id JOIN job j ON j.id=d.finalize_job_id WHERE r.id=$1 AND r.state='closed' AND d.state='closed' AND d.operation_id IS NULL AND d.decision->>'Reason'=$2 AND j.state='dead' AND j.lease_token=$3`, r.ID, reason, lease.Token+1); n != 1 {
					t.Fatal("no safe R0 terminal decision")
				}
			}
		})
	}
}

func TestAutomaticTVExecutionSchemaHistoryPG(t *testing.T) {
	t.Run("empty-up-down-up", func(t *testing.T) {
		dsn := pgtest.GetCluster(t).NewTestDB(t)
		if v, e := postgres.Migrate(t.Context(), dsn, "../../../migrations"); e != nil || v != 88 {
			t.Fatal(v, e)
		}
		if v, e := postgres.Rollback(t.Context(), dsn, "../../../migrations"); e != nil || v != 87 {
			t.Fatal(v, e)
		}
		if v, e := postgres.Migrate(t.Context(), dsn, "../../../migrations"); e != nil || v != 88 {
			t.Fatal(v, e)
		}
	})
	for _, selected := range []bool{false, true} {
		t.Run(map[bool]string{false: "closed", true: "reserved"}[selected], func(t *testing.T) {
			f := newTVExecutionFixture(t)
			r := f.start(t)
			if selected {
				tvExecutionPage(t, f, r, tvExecutionCandidate(t, f, "history", "1080p", 1))
			} else {
				tvExecutionPage(t, f, r)
			}
			lease := tvExecutionLease(t, f, r)
			repo := tvExecutionStore(f, acquisition.SelectAutomaticTVEpisode)
			got, e := repo.FinalizeAutomaticTVRound(t.Context(), r.ID, lease)
			if e != nil {
				t.Fatal(e)
			}
			before := tvExecutionState(t, f)
			tables := []string{"automatic_tv_decision"}
			if selected {
				tables = append(tables, "automatic_tv_execution_authority")
			}
			for _, table := range tables {
				for _, sql := range []string{`UPDATE ` + table + ` SET round_id=round_id`, `DELETE FROM ` + table, `TRUNCATE ` + table + ` CASCADE`} {
					if _, e = f.pool.Exec(t.Context(), sql); e == nil {
						t.Fatal("history mutable", sql)
					}
				}
			}
			if _, e = postgres.Rollback(t.Context(), f.pool.Config().ConnConfig.ConnString(), "../../../migrations"); e == nil {
				t.Fatal("history downgrade admitted")
			}
			again, e := repo.FinalizeAutomaticTVRound(t.Context(), r.ID, lease)
			if e != nil || !reflect.DeepEqual(got, again) || before != tvExecutionState(t, f) {
				t.Fatal("history guard changed original decision", e)
			}
		})
	}
}

func TestAutomaticTVFinalizationNotReadyAndNilSelectorPG(t *testing.T) {
	f := newTVExecutionFixture(t)
	r := f.start(t)
	lease := tvExecutionLease(t, f, r)
	before := tvExecutionState(t, f)
	if _, e := tvExecutionStore(f, nil).FinalizeAutomaticTVRound(t.Context(), r.ID, lease); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal(e)
	}
	if _, e := tvExecutionStore(f, acquisition.SelectAutomaticTVEpisode).FinalizeAutomaticTVRound(t.Context(), r.ID, lease); !errors.Is(e, ports.ErrAcquisitionRoundNotReady) {
		t.Fatal(e)
	}
	if before != tvExecutionState(t, f) {
		t.Fatal("not ready mutated round")
	}
	// An unrelated cancellation remains observable by the transaction boundary.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, e := tvExecutionStore(f, acquisition.SelectAutomaticTVEpisode).FinalizeAutomaticTVRound(ctx, r.ID, lease); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
