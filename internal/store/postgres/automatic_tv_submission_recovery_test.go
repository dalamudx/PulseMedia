package postgres_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

func automaticTVRecoveryExpire(t *testing.T, f automaticTVSubmissionFixture) {
	t.Helper()
	execIntent(t, f.pool, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, f.request.Lease.ID)
}

// Compare the entire protected graph, including immutable physical history.
func automaticTVRecoveryGraph(t *testing.T, f automaticTVSubmissionFixture, runtime bool) string {
	t.Helper()
	tables := []string{"automatic_tv_round", "automatic_tv_decision", "automatic_tv_execution_authority", "acquisition_reservation", "unit_reservation", "download_requirement", "download_task", "automatic_tv_submission_preparation", "automatic_tv_submission_result", "automatic_tv_physical_attempt"}
	if runtime {
		tables = append(tables, "job", "audit_log", "outbox_event")
	}
	var state strings.Builder
	for _, table := range tables {
		var raw []byte
		if e := f.pool.QueryRow(t.Context(), `SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text),'[]') FROM `+table+` x`).Scan(&raw); e != nil {
			t.Fatal(e)
		}
		state.WriteString(table)
		state.Write(raw)
	}
	return state.String()
}

func automaticTVRecoveryAssertJob(t *testing.T, f automaticTVSubmissionFixture, state, reason string, attempts int32) {
	t.Helper()
	if n := intentCount(t, f.pool, `SELECT count(*) FROM job WHERE id=$1 AND state=$2 AND lease_token=$3 AND attempts=$4 AND lease_owner IS NULL AND lease_until IS NULL AND result=jsonb_build_object('reason',$5::text)`, f.request.Lease.ID, state, f.request.Lease.Token+1, attempts, reason); n != 1 {
		t.Fatal("original job not fenced with safe reason", state, reason)
	}
	if n := intentCount(t, f.pool, `SELECT count(*) FROM audit_log WHERE action='automatic_tv.submission_recovered' AND target_id=$1 AND payload=jsonb_build_object('reason',$2::text)`, f.request.Lease.ID.String(), reason); n != 1 {
		t.Fatal("missing safe review audit")
	}
	if n := intentCount(t, f.pool, `SELECT count(*) FROM outbox_event WHERE event_type='automatic_tv.submission_recovered' AND aggregate_id=$1 AND payload=jsonb_build_object('reason',$2::text)`, f.request.OperationID.String(), reason); n != 1 {
		t.Fatal("missing safe recovery event")
	}
}

func TestAutomaticTVSubmissionRecoveryR0PG(t *testing.T) {
	f := newAutomaticTVSubmissionFixture(t, false)
	// A previous, actually finished inspection attempt remains immutable history.
	f.attempt(t)
	execIntent(t, f.pool, `UPDATE automatic_tv_physical_attempt SET finished_at=clock_timestamp() WHERE id=$1`, f.command.PhysicalAttemptID)
	automaticTVRecoveryExpire(t, f)
	before := automaticTVRecoveryGraph(t, f, false)
	var wg sync.WaitGroup
	counts := make([]int, 2)
	errs := make([]error, 2)
	start := make(chan struct{})
	for i := range counts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			counts[i], errs[i] = f.store.RecoverAutomaticTVSubmissions(t.Context(), 64)
		}(i)
	}
	close(start)
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || counts[0]+counts[1] != 1 {
		t.Fatal("concurrent recovery", counts, errs)
	}
	automaticTVRecoveryAssertJob(t, f, "queued", "r0_requeued", 1)
	if before != automaticTVRecoveryGraph(t, f, false) {
		t.Fatal("R0 mutated protected graph or physical history")
	}
	if _, e := f.store.LoadAutomaticTVSendWork(t.Context(), f.request); !errors.Is(e, ports.ErrAutomaticTVSubmissionReview) {
		t.Fatal("old callback accepted", e)
	}
	q := db.New(f.pool)
	next, e := q.ClaimJob(t.Context(), db.ClaimJobParams{Column1: []string{"automatic_tv_download_submission"}, Column2: "reclaimed-sender", Column3: 30000})
	if e != nil || next.ID != f.request.Lease.ID || next.LeaseToken != f.request.Lease.Token+2 || next.Attempts != 2 {
		t.Fatal("original job not reclaimable", next, e)
	}
	request := ports.AutomaticTVSendRequest{OperationID: f.request.OperationID, Lease: ports.JobLease{ID: next.ID, Owner: "reclaimed-sender", Token: next.LeaseToken}}
	if _, e = f.store.LoadAutomaticTVSendWork(t.Context(), request); e != nil {
		t.Fatal("reclaimed original authority", e)
	}
	if _, e = f.store.LoadAutomaticTVSendWork(t.Context(), f.request); !errors.Is(e, ports.ErrAutomaticTVSubmissionReview) {
		t.Fatal("old callback survived reclaim", e)
	}
	if n, e := f.store.RecoverAutomaticTVSubmissions(t.Context(), 64); n != 0 || e != nil {
		t.Fatal("live reclaimed lease recovered", n, e)
	}
}

func TestAutomaticTVSubmissionRecoveryPhysicalHoldPG(t *testing.T) {
	for _, killed := range []bool{false, true} {
		t.Run(map[bool]string{false: "live_sender", true: "killed_backend"}[killed], func(t *testing.T) {
			f := newAutomaticTVSubmissionFixture(t, false)
			f.attempt(t)
			if killed {
				var killed bool
				pid := f.physical.Conn().PgConn().PID()
				if e := f.pool.QueryRow(t.Context(), `SELECT pg_terminate_backend($1)`, pid).Scan(&killed); e != nil || !killed {
					t.Fatal("kill physical connection", e)
				}
				_ = f.physical.Conn().Close(context.Background())
				f.physical.Release()
				f.physical = nil
			}
			automaticTVRecoveryExpire(t, f)
			before := automaticTVRecoveryGraph(t, f, true)
			for i := 0; i < 2; i++ {
				if n, e := f.store.RecoverAutomaticTVSubmissions(t.Context(), 64); n != 0 || e != nil {
					t.Fatal("active physical attempt recovered", n, e)
				}
			}
			if before != automaticTVRecoveryGraph(t, f, true) {
				t.Fatal("physical hold changed history/protection/job")
			}
		})
	}
}

func TestAutomaticTVSubmissionRecoveryPreparedPG(t *testing.T) {
	f := newAutomaticTVSubmissionFixture(t, false)
	f.prepare(t)
	execIntent(t, f.pool, `CREATE FUNCTION recovery_result_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'result write failure'; END $$; CREATE TRIGGER recovery_result_fault BEFORE INSERT ON automatic_tv_submission_result FOR EACH ROW EXECUTE FUNCTION recovery_result_fault()`)
	if _, e := f.store.RecordAutomaticTVSubmissionResult(t.Context(), f.result(true, nil)); e == nil {
		t.Fatal("expected result failure")
	}
	automaticTVRecoveryExpire(t, f)
	before := automaticTVRecoveryGraph(t, f, true)
	if n, e := f.store.RecoverAutomaticTVSubmissions(t.Context(), 64); n != 0 || e != nil || before != automaticTVRecoveryGraph(t, f, true) {
		t.Fatal("prepared active sender taken over", n, e)
	}
	execIntent(t, f.pool, `UPDATE automatic_tv_physical_attempt SET finished_at=clock_timestamp() WHERE id=$1`, f.command.PhysicalAttemptID)
	// Observation handoff must still work beyond the first-send deadline.
	future := automaticTVSubmissionFutureClock(t, f)
	automaticTVRecoveryExpire(t, f)
	graph := automaticTVRecoveryGraph(t, f, false)
	if n, e := future.RecoverAutomaticTVSubmissions(t.Context(), 64); n != 1 || e != nil {
		t.Fatal("prepared recovery", n, e)
	}
	automaticTVRecoveryAssertJob(t, f, "dead", "awaiting_observation", 1)
	if graph != automaticTVRecoveryGraph(t, f, false) {
		t.Fatal("observation handoff mutated task/preparation/protection")
	}
	if n := intentCount(t, f.pool, `SELECT count(*) FROM download_task WHERE operation_id=$1 AND state='submitting'`, f.request.OperationID); n != 1 {
		t.Fatal("original submitting task changed")
	}
	if _, e := f.store.RecordAutomaticTVSubmissionResult(t.Context(), f.result(true, nil)); !errors.Is(e, ports.ErrAutomaticTVResultStale) {
		t.Fatal("old result callback accepted", e)
	}
	if n := intentCount(t, f.pool, `SELECT count(*) FROM job WHERE kind='automatic_tv_download_submission'`); n != 1 {
		t.Fatal("created competing send job")
	}
	if n, e := future.RecoverAutomaticTVSubmissions(t.Context(), 64); n != 0 || e != nil {
		t.Fatal("dead original recovered twice", n, e)
	}
}

func TestAutomaticTVSubmissionRecoveryReviewPG(t *testing.T) {
	for _, kind := range []string{"exhausted", "expired_authority", "disabled", "paused", "intent", "policy", "root", "client", "mapping"} {
		t.Run(kind, func(t *testing.T) {
			f := newAutomaticTVSubmissionFixture(t, false)
			repo := f.store
			reason := "target_changed"
			attempts := int32(1)
			switch kind {
			case "exhausted":
				execIntent(t, f.pool, `UPDATE job SET attempts=max_attempts WHERE id=$1`, f.request.Lease.ID)
				j, e := db.New(f.pool).GetJobByID(t.Context(), f.request.Lease.ID)
				if e != nil {
					t.Fatal(e)
				}
				attempts = j.Attempts
				reason = "retry_exhausted"
			case "expired_authority":
				repo = automaticTVSubmissionFutureClock(t, f)
				reason = "first_send_expired"
			case "disabled":
				execIntent(t, f.pool, `UPDATE app_user SET is_active=false WHERE id=$1`, uuid.MustParse(f.actor.ID))
				reason = "owner_revoked"
			case "paused":
				execIntent(t, f.pool, `UPDATE subscription SET state='paused' WHERE id=$1`, f.sub)
			case "intent":
				execIntent(t, f.pool, `UPDATE subscription SET intent_version=intent_version+1 WHERE id=$1`, f.sub)
			case "policy":
				execIntent(t, f.pool, `UPDATE policy SET current_revision=current_revision+1 WHERE id=(SELECT quality_policy_id FROM subscription WHERE id=$1)`, f.sub)
			case "root":
				execIntent(t, f.pool, `UPDATE library_root SET health='offline' WHERE id=$1`, f.tv)
			case "client":
				execIntent(t, f.pool, `UPDATE download_client SET username='changed' WHERE id=$1`, f.client)
			case "mapping":
				execIntent(t, f.pool, `INSERT INTO episode_mapping_revision(subscription_id,revision,media_id,media_version,intent_version,mode,input_stamp,source_hash,source_snapshot,mappings) SELECT s.id,1,m.id,m.metadata_version,s.intent_version+1,'standard',repeat('a',64),repeat('b',64),'{}','[]' FROM subscription s JOIN media m ON m.id=s.media_id WHERE s.id=$1`, f.sub)
				execIntent(t, f.pool, `UPDATE subscription SET episode_mapping_revision=1,intent_version=intent_version+1 WHERE id=$1`, f.sub)
			}
			automaticTVRecoveryExpire(t, f)
			graph := automaticTVRecoveryGraph(t, f, false)
			if n, e := repo.RecoverAutomaticTVSubmissions(t.Context(), 64); n != 1 || e != nil {
				t.Fatal("review recovery", n, e)
			}
			automaticTVRecoveryAssertJob(t, f, "dead", reason, attempts)
			if graph != automaticTVRecoveryGraph(t, f, false) {
				t.Fatal("review changed protected graph")
			}
			if _, e := f.store.LoadAutomaticTVSendWork(t.Context(), f.request); !errors.Is(e, ports.ErrAutomaticTVSubmissionReview) {
				t.Fatal("review authorized old send", e)
			}
		})
	}
}

func TestAutomaticTVSubmissionRecoveryTailRollbackPG(t *testing.T) {
	t.Run("deadline_after_last_audit", func(t *testing.T) {
		f := newAutomaticTVSubmissionFixture(t, false)
		_ = automaticTVSubmissionFutureClock(t, f)
		automaticTVRecoveryExpire(t, f)
		execIntent(t, f.pool, `CREATE FUNCTION recovery_deadline_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='automatic_tv.submission_recovered' THEN PERFORM set_config('search_path','public,pg_catalog',true); END IF; RETURN NEW; END $$; CREATE TRIGGER recovery_deadline_fault AFTER INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION recovery_deadline_fault()`)
		before := automaticTVRecoveryGraph(t, f, true)
		if n, e := f.store.RecoverAutomaticTVSubmissions(t.Context(), 64); n != 0 || !errors.Is(e, ports.ErrAutomaticTVSubmissionReview) {
			t.Fatal("late first-send deadline ignored", n, e)
		}
		if before != automaticTVRecoveryGraph(t, f, true) {
			t.Fatal("late clock failure did not roll back")
		}
		execIntent(t, f.pool, `DROP TRIGGER recovery_deadline_fault ON audit_log`)
		if n, e := f.store.RecoverAutomaticTVSubmissions(t.Context(), 64); n != 1 || e != nil {
			t.Fatal("original retry after tail clock fault", n, e)
		}
	})
	t.Run("job_payload_trigger", func(t *testing.T) {
		f := newAutomaticTVSubmissionFixture(t, false)
		automaticTVRecoveryExpire(t, f)
		execIntent(t, f.pool, `CREATE FUNCTION recovery_job_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='automatic_tv_download_submission' THEN NEW.payload:=NEW.payload||'{"extra":true}'::jsonb; END IF; RETURN NEW; END $$; CREATE TRIGGER recovery_job_fault BEFORE UPDATE ON job FOR EACH ROW EXECUTE FUNCTION recovery_job_fault()`)
		before := automaticTVRecoveryGraph(t, f, true)
		if n, e := f.store.RecoverAutomaticTVSubmissions(t.Context(), 64); n != 0 || !errors.Is(e, ports.ErrAutomaticTVSubmissionReview) {
			t.Fatal("job payload trigger bypassed fence", n, e)
		}
		if before != automaticTVRecoveryGraph(t, f, true) {
			t.Fatal("job trigger did not roll back")
		}
	})
	for _, table := range []string{"audit_log", "outbox_event"} {
		for _, prepared := range []bool{false, true} {
			t.Run(table+map[bool]string{false: "_R0", true: "_prepared"}[prepared], func(t *testing.T) {
				f := newAutomaticTVSubmissionFixture(t, false)
				reason, state := "r0_requeued", "queued"
				if prepared {
					f.prepare(t)
					execIntent(t, f.pool, `UPDATE automatic_tv_physical_attempt SET finished_at=clock_timestamp() WHERE id=$1`, f.command.PhysicalAttemptID)
					reason, state = "awaiting_observation", "dead"
				}
				automaticTVRecoveryExpire(t, f)
				field := "action"
				if table == "outbox_event" {
					field = "event_type"
				}
				execIntent(t, f.pool, `CREATE FUNCTION recovery_tail_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.`+field+`='automatic_tv.submission_recovered' THEN RAISE EXCEPTION 'recovery tail failure'; END IF; RETURN NEW; END $$`)
				execIntent(t, f.pool, `CREATE TRIGGER recovery_tail_fault AFTER INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION recovery_tail_fault()`)
				before := automaticTVRecoveryGraph(t, f, true)
				if n, e := f.store.RecoverAutomaticTVSubmissions(t.Context(), 64); n != 0 || !errors.Is(e, ports.ErrUnavailable) {
					t.Fatal("tail failure accepted", n, e)
				}
				if before != automaticTVRecoveryGraph(t, f, true) {
					t.Fatal("tail failure did not roll back original graph/job")
				}
				execIntent(t, f.pool, `DROP TRIGGER recovery_tail_fault ON `+table)
				if n, e := f.store.RecoverAutomaticTVSubmissions(t.Context(), 64); n != 1 || e != nil {
					t.Fatal("original recovery not retryable", n, e)
				}
				automaticTVRecoveryAssertJob(t, f, state, reason, 1)
			})
		}
	}
}

func TestAutomaticTVSubmissionRecoveryCandidatesPG(t *testing.T) {
	f := newAutomaticTVSubmissionFixture(t, false)
	for _, limit := range []int32{-1, 0, 101} {
		if n, e := f.store.RecoverAutomaticTVSubmissions(t.Context(), limit); n != 0 || !errors.Is(e, ports.ErrRejected) {
			t.Fatal("limit accepted", limit, n, e)
		}
	}
	for _, limit := range []int32{1, 100} {
		if n, e := f.store.RecoverAutomaticTVSubmissions(t.Context(), limit); n != 0 || e != nil {
			t.Fatal("live lease candidate", limit, n, e)
		}
	}
	for _, kind := range []string{"extra_payload", "wrong_authority", "wrong_dedupe", "wrong_kind", "succeeded"} {
		t.Run(kind, func(t *testing.T) {
			f := newAutomaticTVSubmissionFixture(t, false)
			automaticTVRecoveryExpire(t, f)
			review := true
			switch kind {
			case "extra_payload":
				execIntent(t, f.pool, `UPDATE job SET payload=payload||'{"extra":true}'::jsonb WHERE id=$1`, f.request.Lease.ID)
			case "wrong_authority":
				execIntent(t, f.pool, `UPDATE job SET payload=jsonb_set(payload,'{automatic_tv_authority}',to_jsonb($2::text)) WHERE id=$1`, f.request.Lease.ID, uuid.NewString())
			case "wrong_dedupe":
				execIntent(t, f.pool, `UPDATE job SET dedupe_key='foreign' WHERE id=$1`, f.request.Lease.ID)
			case "wrong_kind":
				execIntent(t, f.pool, `UPDATE job SET kind='download_submission' WHERE id=$1`, f.request.Lease.ID)
				review = false
			case "succeeded":
				execIntent(t, f.pool, `UPDATE job SET state='succeeded' WHERE id=$1`, f.request.Lease.ID)
				review = false
			}
			before := automaticTVRecoveryGraph(t, f, true)
			n, e := f.store.RecoverAutomaticTVSubmissions(t.Context(), 64)
			if n != 0 || (review && !errors.Is(e, ports.ErrAutomaticTVSubmissionReview)) || (!review && e != nil) || before != automaticTVRecoveryGraph(t, f, true) {
				t.Fatal("foreign or completed job changed", n, e)
			}
			queue := jobs.NewQueue(f.pool, nil, nil)
			if job, e := queue.Claim(t.Context(), []string{"automatic_tv_download_submission"}, "unexpected", 30*time.Second); e != nil || job != nil {
				t.Fatal("recovery created sendable job", job, e)
			}
		})
	}
}
