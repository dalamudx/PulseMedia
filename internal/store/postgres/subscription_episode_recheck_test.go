package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type episodeRecheckFixture struct {
	intentFixture
	control *subscription.ControlService
	sub     *ports.SubscriptionRecord
	units   []uuid.UUID
	foreign uuid.UUID
}

func newEpisodeRecheckFixture(t *testing.T) episodeRecheckFixture {
	t.Helper()
	f := newIntentFixture(t)
	c := f.command()
	c.MediaInput.Seasons = []ports.SeasonEpisodes{{Season: 1, Count: 2}}
	sub, wanted, err := f.svc.CreateSubscription(t.Context(), f.actor, c)
	if err != nil || len(wanted) != 2 {
		t.Fatalf("create two episodes: %d %v", len(wanted), err)
	}
	c.MediaInput.ExternalID = "200"
	other, rows, err := f.svc.CreateSubscription(t.Context(), f.actor, c)
	if err != nil || other.ID == sub.ID || len(rows) != 2 {
		t.Fatalf("foreign media: %v", err)
	}
	units := []uuid.UUID{wanted[0].UnitID, wanted[1].UnitID}
	execIntent(t, f.pool, `UPDATE wanted_item SET next_evaluate_at=clock_timestamp()+interval '3 days',last_evaluated_at=clock_timestamp()-interval '1 hour',rss_seen_at=clock_timestamp()-interval '2 hours',current_fact_stage='submitted',is_satisfied=true,evaluated_intent_version=intent_version,evaluated_policy_id='default',evaluated_policy_revision=7,evaluated_root_id=$1,evaluated_unit_id=unit_id,evaluated_media_id=$2,evaluated_root_generation=8,evaluated_health_revision=9,evaluated_media_metadata_version=10,evaluated_unit_metadata_version=11,evaluated_facts_version=12,evaluated_state='active',evaluated_skipped=false,projection_valid_until=clock_timestamp()+interval '2 hours',evaluated_contract_version='recheck-sentinel',evaluated_write_token=$3 WHERE subscription_id=$4`, f.root, sub.MediaID, uuid.New(), sub.ID)
	return episodeRecheckFixture{f, subscription.NewControlService(postgres.NewSubscriptionControlStore(f.pool, idgen.New())), sub, units, rows[0].UnitID}
}
func (f episodeRecheckFixture) command(unit uuid.UUID) ports.SubscriptionControlCommand {
	return ports.SubscriptionControlCommand{Action: "recheck", ExpectedVersion: f.sub.IntentVersion, UnitID: &unit}
}

// Compare every column, including clock/projection fields, rather than an allowlist
// that could miss a newly introduced command side effect.
func episodeRecheckSnapshot(t *testing.T, pool *pgxpool.Pool, allowSchedule uuid.UUID, publication bool) map[string]string {
	t.Helper()
	tables := []string{"subscription", "wanted_item", "media", "media_unit", "unit_fact", "unit_reservation", "acquisition_reservation", "download_task", "download_requirement", "job", "indexer", "indexer_query", "automatic_tv_round", "automatic_tv_source", "audit_log", "outbox_event", "outbox_delivery", "idempotency_record"}
	result := make(map[string]string, len(tables))
	for _, table := range tables {
		if publication && (table == "audit_log" || table == "outbox_event" || table == "outbox_delivery" || table == "idempotency_record") {
			continue
		}
		expression := "to_jsonb(r)"
		var args []any
		if table == "wanted_item" && allowSchedule != uuid.Nil {
			expression = "CASE WHEN r.unit_id=$1 THEN to_jsonb(r)-'next_evaluate_at'-'updated_at' ELSE to_jsonb(r) END"
			args = []any{allowSchedule}
		}
		var value string
		err := pool.QueryRow(t.Context(), "SELECT COALESCE(jsonb_agg(v ORDER BY v::text),'[]'::jsonb)::text FROM (SELECT "+expression+" AS v FROM "+table+" r)s", args...).Scan(&value)
		if err != nil {
			t.Fatalf("snapshot %s: %v", table, err)
		}
		result[table] = value
	}
	return result
}
func assertEpisodeRecheckSnapshot(t *testing.T, before, after map[string]string) {
	t.Helper()
	for table, want := range before {
		if got := after[table]; got != want {
			t.Errorf("%s changed unexpectedly\nbefore %s\nafter  %s", table, want, got)
		}
	}
}
func episodeRecheckNow(t *testing.T, pool *pgxpool.Pool) time.Time {
	t.Helper()
	var now time.Time
	if err := pool.QueryRow(t.Context(), "SELECT clock_timestamp()").Scan(&now); err != nil {
		t.Fatal(err)
	}
	return now
}
func assertEpisodeRecheckReceipt(t *testing.T, f episodeRecheckFixture, r *ports.SubscriptionControlReceipt) {
	t.Helper()
	if r == nil || r.ID != f.sub.ID || r.State != "active" || r.IntentVersion != f.sub.IntentVersion || r.UnitID == nil || *r.UnitID != f.units[0] || r.Skipped != nil || r.CandidateID != nil || r.Blocked != nil {
		t.Fatalf("receipt: %+v", r)
	}
}
func TestSubscriptionEpisodeRecheckSelectedSchedulePG(t *testing.T) {
	f := newEpisodeRecheckFixture(t)
	before := episodeRecheckSnapshot(t, f.pool, f.units[0], true)
	start := episodeRecheckNow(t, f.pool)
	r, err := f.control.Apply(t.Context(), f.actor, f.sub.ID, f.command(f.units[0]), "selected-recheck")
	end := episodeRecheckNow(t, f.pool)
	if err != nil {
		t.Fatal(err)
	}
	assertEpisodeRecheckReceipt(t, f, r)
	var due time.Time
	if err = f.pool.QueryRow(t.Context(), `SELECT next_evaluate_at FROM wanted_item WHERE subscription_id=$1 AND unit_id=$2`, f.sub.ID, f.units[0]).Scan(&due); err != nil {
		t.Fatal(err)
	}
	if due.Before(start) || due.After(end) {
		t.Fatalf("schedule %v outside database command interval [%v,%v]", due, start, end)
	}
	assertEpisodeRecheckSnapshot(t, before, episodeRecheckSnapshot(t, f.pool, f.units[0], true))
	for table, where := range map[string]string{"idempotency_record": "idempotency_key='selected-recheck'", "audit_log": "action='subscription.control_recheck'", "outbox_event": "event_type='subscription.control_changed'"} {
		if n := intentCount(t, f.pool, "SELECT count(*) FROM "+table+" WHERE "+where); n != 1 {
			t.Fatalf("%s publication=%d", table, n)
		}
	}
}
func TestSubscriptionEpisodeRecheckRefusalsPG(t *testing.T) {
	cases := []struct {
		name, sql string
		want      error
	}{
		{"user_skipped", `UPDATE wanted_item SET user_skipped=true,is_skipped=true WHERE unit_id=$1`, ports.ErrSubscriptionBindingsConflict},
		{"scope_excluded", `UPDATE wanted_item SET scope_excluded=true,is_skipped=true WHERE unit_id=$1`, ports.ErrSubscriptionBindingsConflict},
		{"both_skip_provenances", `UPDATE wanted_item SET user_skipped=true,scope_excluded=true,is_skipped=true WHERE unit_id=$1`, ports.ErrSubscriptionBindingsConflict},
		{"retired", `UPDATE media_unit SET retired=true WHERE id=$1`, ports.ErrSubscriptionBindingsConflict},
		{"paused", `UPDATE subscription SET state='paused' WHERE id=$1`, ports.ErrSubscriptionBindingsConflict},
		{"archived", `UPDATE subscription SET state='archived' WHERE id=$1`, ports.ErrSubscriptionBindingsConflict},
		{"movie", `UPDATE media SET media_type='movie' WHERE id=(SELECT media_id FROM subscription WHERE id=$1)`, ports.ErrSubscriptionBindingsConflict},
		{"missing_unit", "", ports.ErrNotFound},
		{"foreign_unit", "", ports.ErrNotFound},
		{"stale_version", "", ports.ErrSubscriptionBindingsConflict},
		{"foreign_owner", "", ports.ErrForbidden},
		{"scoped_actor", "", ports.ErrForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newEpisodeRecheckFixture(t)
			if tc.sql != "" {
				// Bind only parameters referenced by each fixture statement.
				if tc.name == "paused" || tc.name == "archived" || tc.name == "movie" {
					execIntent(t, f.pool, tc.sql, f.sub.ID)
				} else {
					execIntent(t, f.pool, tc.sql, f.units[0])
				}
			}
			c := f.command(f.units[0])
			a := *f.actor
			switch tc.name {
			case "missing_unit":
				u := uuid.New()
				c.UnitID = &u
			case "foreign_unit":
				c.UnitID = &f.foreign
			case "stale_version":
				c.ExpectedVersion++
			case "foreign_owner":
				a.ID = uuid.NewString()
				execIntent(t, f.pool, `INSERT INTO app_user(id,username,password_hash,role,is_active) VALUES($1,'recheck-other','fixture','member',true)`, uuid.MustParse(a.ID))
			case "scoped_actor":
				a.Scopes = []string{"write"}
			}
			before := episodeRecheckSnapshot(t, f.pool, uuid.Nil, false)
			r, err := f.control.Apply(t.Context(), &a, f.sub.ID, c, "refused-recheck")
			if !errors.Is(err, tc.want) || r != nil {
				t.Fatalf("receipt=%+v error=%v want %v", r, err, tc.want)
			}
			assertEpisodeRecheckSnapshot(t, before, episodeRecheckSnapshot(t, f.pool, uuid.Nil, false))
		})
	}
}
func TestSubscriptionEpisodeRecheckPayloadPG(t *testing.T) {
	f := newEpisodeRecheckFixture(t)
	cases := []struct {
		name   string
		mutate func(*ports.SubscriptionControlCommand)
	}{
		{"missing_unit", func(c *ports.SubscriptionControlCommand) { c.UnitID = nil }},
		{"zero_unit", func(c *ports.SubscriptionControlCommand) { u := uuid.Nil; c.UnitID = &u }},
		{"state", func(c *ports.SubscriptionControlCommand) { c.State = "active" }},
		{"skipped", func(c *ports.SubscriptionControlCommand) { v := false; c.Skipped = &v }},
		{"candidate", func(c *ports.SubscriptionControlCommand) { u := uuid.New(); c.CandidateID = &u }},
		{"blocked", func(c *ports.SubscriptionControlCommand) { v := false; c.Blocked = &v }},
		{"scope_type", func(c *ports.SubscriptionControlCommand) { v := "all"; c.ScopeType = &v }},
		{"seasons", func(c *ports.SubscriptionControlCommand) { v := []int{}; c.Seasons = &v }},
		{"specials", func(c *ports.SubscriptionControlCommand) { v := false; c.IncludeSpecials = &v }},
		{"selected_units", func(c *ports.SubscriptionControlCommand) { v := []uuid.UUID{}; c.SelectedUnitIDs = &v }},
		{"zero_version", func(c *ports.SubscriptionControlCommand) { c.ExpectedVersion = 0 }},
	}
	before := episodeRecheckSnapshot(t, f.pool, uuid.Nil, false)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := f.command(f.units[0])
			tc.mutate(&c)
			if r, err := f.control.Apply(t.Context(), f.actor, f.sub.ID, c, "invalid-recheck"); !errors.Is(err, ports.ErrInvalidSubscriptionBindings) || r != nil {
				t.Fatalf("receipt=%+v error=%v", r, err)
			}
		})
	}
	assertEpisodeRecheckSnapshot(t, before, episodeRecheckSnapshot(t, f.pool, uuid.Nil, false))
}
func TestSubscriptionEpisodeRecheckConcurrentReceiptReplayPG(t *testing.T) {
	f := newEpisodeRecheckFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	c := f.command(f.units[0])
	const peers = 8
	var wg sync.WaitGroup
	receipts := make([]*ports.SubscriptionControlReceipt, peers)
	errs := make([]error, peers)
	start := make(chan struct{})
	for i := range peers {
		wg.Go(func() {
			<-start
			receipts[i], errs[i] = f.control.Apply(ctx, f.actor, f.sub.ID, c, "concurrent-recheck")
		})
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("peer %d: %v", i, err)
		}
		assertEpisodeRecheckReceipt(t, f, receipts[i])
		if !reflect.DeepEqual(receipts[0], receipts[i]) {
			t.Fatal("different same-key receipts")
		}
	}
	for table, where := range map[string]string{"idempotency_record": "idempotency_key='concurrent-recheck'", "audit_log": "action='subscription.control_recheck'", "outbox_event": "event_type='subscription.control_changed'"} {
		if n := intentCount(t, f.pool, "SELECT count(*) FROM "+table+" WHERE "+where); n != 1 {
			t.Fatalf("%s count=%d", table, n)
		}
	}
	before := episodeRecheckSnapshot(t, f.pool, uuid.Nil, false)
	changed := f.command(f.units[1])
	if _, err := f.control.Apply(ctx, f.actor, f.sub.ID, changed, "concurrent-recheck"); !errors.Is(err, ports.ErrSubscriptionBindingsConflict) {
		t.Fatalf("different body: %v", err)
	}
	assertEpisodeRecheckSnapshot(t, before, episodeRecheckSnapshot(t, f.pool, uuid.Nil, false))
	if _, err := f.control.Apply(ctx, f.actor, f.sub.ID, ports.SubscriptionControlCommand{Action: "state", ExpectedVersion: c.ExpectedVersion, State: "paused"}, "advance-after-recheck"); err != nil {
		t.Fatal(err)
	}
	before = episodeRecheckSnapshot(t, f.pool, uuid.Nil, false)
	replay, err := f.control.Apply(ctx, f.actor, f.sub.ID, c, "concurrent-recheck")
	if err != nil || !reflect.DeepEqual(replay, receipts[0]) {
		t.Fatalf("historical replay: %+v %v", replay, err)
	}
	assertEpisodeRecheckSnapshot(t, before, episodeRecheckSnapshot(t, f.pool, uuid.Nil, false))
}
func TestSubscriptionEpisodeRecheckCurrentAccountLockPG(t *testing.T) {
	for _, mutation := range []string{"is_active=false", "role='admin'"} {
		t.Run(mutation, func(t *testing.T) {
			f := newEpisodeRecheckFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			tx, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if _, err = tx.Exec(ctx, "UPDATE app_user SET "+mutation+" WHERE id=$1", uuid.MustParse(f.actor.ID)); err != nil {
				t.Fatal(err)
			}
			before := episodeRecheckSnapshot(t, f.pool, uuid.Nil, false)
			done := make(chan error, 1)
			go func() {
				_, err := f.control.Apply(ctx, f.actor, f.sub.ID, f.command(f.units[0]), "lockwait-recheck")
				done <- err
			}()
			// On every failure path cancel, release the blocker, and join the child.
			joined := false
			defer func() {
				cancel()
				_ = tx.Rollback(context.Background())
				if !joined {
					<-done
				}
			}()
			waitAutomaticTVBlocked(t, f.pool, tx.Conn().PgConn().PID())
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			err = <-done
			joined = true
			if !errors.Is(err, ports.ErrForbidden) {
				t.Fatalf("stale account authorized: %v", err)
			}
			assertEpisodeRecheckSnapshot(t, before, episodeRecheckSnapshot(t, f.pool, uuid.Nil, false))
		})
	}
}
func TestSubscriptionEpisodeRecheckTailRollbackPG(t *testing.T) {
	for _, table := range []string{"audit_log", "outbox_event", "idempotency_record"} {
		t.Run(table, func(t *testing.T) {
			f := newEpisodeRecheckFixture(t)
			execIntent(t, f.pool, `CREATE FUNCTION reject_episode_recheck_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'recheck tail fault'; END $$`)
			execIntent(t, f.pool, fmt.Sprintf(`CREATE TRIGGER reject_episode_recheck_tail BEFORE INSERT ON %s FOR EACH ROW EXECUTE FUNCTION reject_episode_recheck_tail()`, table))
			before := episodeRecheckSnapshot(t, f.pool, uuid.Nil, false)
			c := f.command(f.units[0])
			if r, err := f.control.Apply(t.Context(), f.actor, f.sub.ID, c, "tail-recheck"); !errors.Is(err, ports.ErrUnavailable) || r != nil {
				t.Fatalf("tail receipt=%+v error=%v", r, err)
			}
			assertEpisodeRecheckSnapshot(t, before, episodeRecheckSnapshot(t, f.pool, uuid.Nil, false))
			execIntent(t, f.pool, "DROP TRIGGER reject_episode_recheck_tail ON "+table)
			if r, err := f.control.Apply(t.Context(), f.actor, f.sub.ID, c, "tail-recheck"); err != nil {
				t.Fatalf("retry after rollback: %+v %v", r, err)
			}
		})
	}
}
func TestSubscriptionEpisodeRecheckProtectionRetryClocksPG(t *testing.T) {
	f := newEpisodeRecheckFixture(t)
	source, candidate, client, task, operation := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	execIntent(t, f.pool, `INSERT INTO indexer(id,name,url,api_key_encrypted,health,rate_limit_until,next_query_at,rss_next_poll_at,health_next_check_at) VALUES($1,'recheck source','https://recheck.invalid','fixture','rate_limited',clock_timestamp()+interval '4 hours',clock_timestamp()+interval '3 hours',clock_timestamp()+interval '2 hours',clock_timestamp()+interval '1 hour')`, source)
	execIntent(t, f.pool, `INSERT INTO release_candidate(id,indexer_id,title,guid,link_encrypted,size_bytes) VALUES($1,$2,'recheck episode','recheck-candidate','fixture',1024)`, candidate, source)
	execIntent(t, f.pool, `INSERT INTO download_client(id,name,client_type,url,username,password_encrypted) VALUES($1,'recheck client','qbittorrent','https://recheck.invalid','fixture','fixture')`, client)
	execIntent(t, f.pool, `INSERT INTO download_task(id,operation_id,client_id,candidate_id,info_hash,state,save_path,reconcile_count,error) VALUES($1,$2,$3,$4,'0123456789012345678901234567890123456789','unknown','/intent-fixture',7,'outcome unknown')`, task, operation, client, candidate)
	execIntent(t, f.pool, `INSERT INTO unit_reservation(id,unit_id,root_id,purpose,candidate_id,operation_id,state,expires_at) VALUES($1,$2,$3,'acquire',$4,$5,'dispatched',clock_timestamp()+interval '6 hours')`, uuid.New(), f.units[0], f.root, candidate, operation)
	execIntent(t, f.pool, `INSERT INTO acquisition_reservation(id,unit_id,operation_id,candidate_id,task_id,source_root_id,source_generation,source_stamp) SELECT $1,$2,$3,$4,$5,id,generation,'recheck-protection' FROM library_root WHERE id=$6`, uuid.New(), f.units[0], operation, candidate, task, f.root)
	execIntent(t, f.pool, `INSERT INTO unit_fact(id,unit_id,stage,source_task_id,root_id,expires_at) VALUES($1,$2,'submitted',$3,$4,clock_timestamp()+interval '5 hours')`, uuid.New(), f.units[0], task, f.root)
	execIntent(t, f.pool, `INSERT INTO job(id,kind,state,payload,attempts,max_attempts,not_before) VALUES($1,'download_submission','queued','{}',3,8,clock_timestamp()+interval '7 hours')`, uuid.New())
	before := episodeRecheckSnapshot(t, f.pool, f.units[0], true)
	if r, err := f.control.Apply(t.Context(), f.actor, f.sub.ID, f.command(f.units[0]), "protected-recheck"); err != nil {
		t.Fatalf("protected receipt=%+v error=%v", r, err)
	}
	assertEpisodeRecheckSnapshot(t, before, episodeRecheckSnapshot(t, f.pool, f.units[0], true))
}
