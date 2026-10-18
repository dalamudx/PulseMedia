package httpapi_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type tvResultFixture struct {
	*prepareTVFixture
	command ports.CompleteTVSubmissionResultCommand
}

func newTVResultFixture(t *testing.T) *tvResultFixture {
	t.Helper()
	f := newPrepareTVFixture(t)
	if _, err := f.prepare(); err != nil {
		t.Fatal(err)
	}
	return &tvResultFixture{f, ports.CompleteTVSubmissionResultCommand{Lease: f.c.Lease, TaskID: f.c.TaskID, OperationID: f.c.OperationID, Metainfo: f.c.Metainfo, Acknowledged: true}}
}

func (f *tvResultFixture) record(ctx context.Context) (*ports.CompleteTVSubmissionResultReceipt, error) {
	return f.store.RecordCompleteTVSubmissionResult(ctx, f.command)
}

// Includes full protection, requirement/fence, preparation, task, job and evidence
// contents, not just row counts. A rollback must preserve every original value.
func (f *tvResultFixture) snapshot(t *testing.T) string {
	t.Helper()
	var v string
	err := f.f.a.DBPool().QueryRow(context.Background(), `SELECT jsonb_build_array(
	(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM acquisition_reservation x),
	(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM unit_reservation x),
	(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM download_requirement x),
	(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM download_task x),
	(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM job x),
	(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM audit_log x),
	(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM outbox_event x),
	(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM outbox_delivery x),
	(SELECT jsonb_agg(to_jsonb(x) ORDER BY unit_id) FROM unit_fact x),
	(SELECT jsonb_agg(to_jsonb(x) ORDER BY unit_id) FROM library_file_unit x),
	(SELECT jsonb_agg(to_jsonb(x) ORDER BY operation_id) FROM complete_tv_submission_preparation x),
	(SELECT jsonb_agg(to_jsonb(x) ORDER BY task_id,unit_id) FROM complete_tv_submission_unit x)
	)::text`).Scan(&v)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func (f *tvResultFixture) reject(t *testing.T, want error) {
	t.Helper()
	before := f.snapshot(t)
	r, err := f.record(context.Background())
	if r != nil || !errors.Is(err, want) {
		t.Fatal("unexpected callback result", r, err, "want", want)
	}
	if after := f.snapshot(t); after != before {
		t.Fatal("rejected callback changed task/job/evidence/protections")
	}
}

func (f *tvResultFixture) assertObserved(t *testing.T, state string) {
	t.Helper()
	var task, job string
	var audits, events, lanes, reservations, demands, claims int
	err := f.f.a.DBPool().QueryRow(context.Background(), `SELECT
	(SELECT state FROM download_task WHERE id=$1),(SELECT state FROM job WHERE id=$2),
	(SELECT count(*) FROM audit_log WHERE action='complete_tv.submission_observed'),
	(SELECT count(*) FROM outbox_event WHERE event_type='complete_tv.submission_observed'),
	(SELECT count(*) FROM acquisition_reservation WHERE task_id=$1 AND released_at IS NULL),
	(SELECT count(*) FROM unit_reservation WHERE operation_id=$3 AND state='dispatched'),
	(SELECT count(*) FROM download_requirement WHERE operation_id=$3 AND state='pending'),
	(SELECT count(*) FROM library_file_unit)+(SELECT count(*) FROM unit_fact WHERE stage='imported')`, f.c.TaskID, f.c.Lease.ID, f.c.OperationID).Scan(&task, &job, &audits, &events, &lanes, &reservations, &demands, &claims)
	if err != nil || task != state || job != "succeeded" || audits != 1 || events != 1 || lanes != 2 || reservations != 2 || demands != 2 || claims != 0 {
		t.Fatal("incomplete observation", task, job, audits, events, lanes, reservations, demands, claims, err)
	}
	var consistent bool
	if err = f.f.a.DBPool().QueryRow(context.Background(), `SELECT j.result->>'state'=$2 AND a.payload=j.result AND e.payload->>'state'=$2 FROM job j JOIN audit_log a ON a.target_id=$3 AND a.action='complete_tv.submission_observed' JOIN outbox_event e ON e.aggregate_id=$4 AND e.event_type='complete_tv.submission_observed' WHERE j.id=$1`, f.c.Lease.ID, state, f.c.TaskID.String(), f.c.OperationID.String()).Scan(&consistent); err != nil || !consistent {
		t.Fatal("job/audit/event disagree", err)
	}
}

func TestCompleteTVResultPGOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ack    bool
		addErr error
		state  string
	}{
		{"accepted", true, nil, "tracking"},
		{"no-ack", false, nil, "unknown"},
		{"error", false, errors.New("secret-downloader-url"), "unknown"},
		{"ack-with-error", true, errors.New("secret-downloader-url"), "unknown"},
		{"add-canceled", false, context.Canceled, "unknown"},
		{"add-timeout", false, context.DeadlineExceeded, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTVResultFixture(t)
			f.command.Acknowledged, f.command.AddError = tc.ack, tc.addErr
			r, err := f.record(context.Background())
			if err != nil || r == nil || r.State != tc.state || r.TaskID != f.c.TaskID {
				t.Fatal(r, err)
			}
			f.assertObserved(t, tc.state)
			if strings.Contains(f.snapshot(t), "secret-downloader-url") {
				t.Fatal("raw Add error persisted")
			}
			f.reject(t, ports.ErrCompleteTVResultStale)
			f.command.Acknowledged = !tc.ack
			f.command.AddError = nil
			f.reject(t, ports.ErrCompleteTVResultStale)
		})
	}
}

func TestCompleteTVResultPGStaleAuthority(t *testing.T) {
	for _, name := range []string{"owner", "token", "job", "task", "operation", "unsealed", "nil-proof", "reclaimed"} {
		t.Run(name, func(t *testing.T) {
			f := newTVResultFixture(t)
			switch name {
			case "owner":
				f.command.Lease.Owner = "new-owner"
			case "token":
				f.command.Lease.Token++
			case "job":
				f.command.Lease.ID = uuid.New()
			case "task":
				f.command.TaskID = uuid.New()
			case "operation":
				f.command.OperationID = uuid.New()
			case "unsealed":
				f.command.Metainfo = &ports.CompleteTVMetainfoProof{}
			case "nil-proof":
				f.command.Metainfo = nil
			case "reclaimed":
				f.exec(t, `UPDATE job SET lease_owner='reclaimer',lease_token=124,lease_until=clock_timestamp()+interval '1 minute'`)
				f.command.Lease.Owner, f.command.Lease.Token = "reclaimer", 124
			}
			f.reject(t, ports.ErrCompleteTVResultStale)
		})
	}
	for name, sql := range map[string]string{
		"job-queued":        `UPDATE job SET state='queued'`,
		"job-dead":          `UPDATE job SET state='dead'`,
		"job-succeeded":     `UPDATE job SET state='succeeded'`,
		"expired":           `UPDATE job SET lease_until=clock_timestamp()`,
		"null-expiry":       `UPDATE job SET lease_until=NULL`,
		"kind":              `UPDATE job SET kind='download_submission'`,
		"task-unknown":      `UPDATE download_task SET state='unknown'`,
		"task-tracking":     `UPDATE download_task SET state='tracking'`,
		"task-hash":         `UPDATE download_task SET info_hash=repeat('f',40)`,
		"task-save-path":    `UPDATE download_task SET save_path=save_path||'/different'`,
		"payload-operation": `UPDATE job SET payload=jsonb_set(payload,'{operation_id}',to_jsonb('00000000-0000-0000-0000-000000000001'::text))`,
		"payload-authority": `UPDATE job SET payload=payload-'complete_tv_authority'`,
		"payload-client":    `UPDATE job SET payload=jsonb_set(payload,'{client_id}',to_jsonb('00000000-0000-0000-0000-000000000001'::text))`,
		"payload-material":  `UPDATE job SET payload=jsonb_set(payload,'{material_stamp}','"different"')`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newTVResultFixture(t)
			f.exec(t, sql)
			f.reject(t, ports.ErrCompleteTVResultStale)
		})
	}
	t.Run("other-live-job", func(t *testing.T) {
		f := newTVResultFixture(t)
		other := uuid.New()
		f.exec(t, `INSERT INTO job(id,kind,state,payload,lease_owner,lease_token,lease_until) SELECT $1,kind,state,payload,lease_owner,lease_token,lease_until FROM job WHERE id=$2`, other, f.c.Lease.ID)
		f.command.Lease.ID = other
		f.reject(t, ports.ErrCompleteTVResultStale)
	})
	t.Run("other-prepared-task", func(t *testing.T) {
		f, other := newTVResultFixture(t), newTVResultFixture(t)
		f.command.TaskID = other.c.TaskID
		f.reject(t, ports.ErrCompleteTVResultStale)
	})
}

func TestCompleteTVResultPGExactSet(t *testing.T) {
	for name, sql := range map[string]string{
		"payload-partial":      `UPDATE job SET payload=jsonb_set(payload,'{unit_ids}',jsonb_build_array(payload->'unit_ids'->0))`,
		"payload-extra":        `UPDATE job SET payload=jsonb_set(payload,'{unit_ids}',payload->'unit_ids'||jsonb_build_array('00000000-0000-0000-0000-000000000001'))`,
		"payload-duplicate":    `UPDATE job SET payload=jsonb_set(payload,'{unit_ids}',jsonb_build_array(payload->'unit_ids'->0,payload->'unit_ids'->0))`,
		"demand-released":      `UPDATE download_requirement SET state='released' WHERE unit_id=(SELECT unit_id FROM download_requirement LIMIT 1)`,
		"demand-extra":         `INSERT INTO download_requirement(id,operation_id,unit_id,root_id,actor_type,actor_id,submission_fence) SELECT '00000000-0000-0000-0000-000000000002',operation_id,unit_id,root_id,'user','other',submission_fence FROM download_requirement LIMIT 1`,
		"fence-missing":        `UPDATE download_requirement SET submission_fence=NULL`,
		"fence-extra":          `UPDATE download_requirement SET submission_fence=submission_fence||'{"unfrozen":true}'`,
		"fence-version":        `UPDATE download_requirement SET submission_fence=jsonb_set(submission_fence,'{unit_metadata_version}','999')`,
		"lane-released":        `UPDATE acquisition_reservation SET released_at=clock_timestamp() WHERE unit_id=(SELECT unit_id FROM acquisition_reservation LIMIT 1)`,
		"lane-conflict":        `UPDATE acquisition_reservation SET legacy_conflict=true`,
		"reservation-partial":  `UPDATE unit_reservation SET state='released' WHERE unit_id=(SELECT unit_id FROM unit_reservation LIMIT 1)`,
		"reservation-reserved": `UPDATE unit_reservation SET state='reserved'`,
		"reservation-extra":    `INSERT INTO unit_reservation(id,unit_id,root_id,purpose,candidate_id,operation_id,state,expires_at) SELECT '00000000-0000-0000-0000-000000000003',unit_id,root_id,'extra',candidate_id,operation_id,'reserved',expires_at FROM unit_reservation LIMIT 1`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newTVResultFixture(t)
			f.exec(t, sql)
			f.reject(t, ports.ErrCompleteTVResultStale)
		})
	}
	// Superuser corruption probes deliberately bypass immutability triggers only
	// inside this disposable test database; production never mutates these rows.
	for name, sql := range map[string]string{
		"proof-bytes":      `UPDATE complete_tv_submission_preparation SET torrent_sha256=decode(repeat('ff',32),'hex')`,
		"proof-infohash":   `UPDATE complete_tv_submission_preparation SET info_hash=repeat('f',40)`,
		"proof-operation":  `UPDATE complete_tv_submission_preparation SET proof=jsonb_set(proof,'{material_stamp}','"different"')`,
		"proof-partial":    `UPDATE complete_tv_submission_preparation SET proof=jsonb_set(proof,'{files}',jsonb_build_array(proof->'files'->0))`,
		"prepared-partial": `DELETE FROM complete_tv_submission_unit WHERE unit_id=(SELECT unit_id FROM complete_tv_submission_unit LIMIT 1)`,
		"prepared-file":    `UPDATE complete_tv_submission_unit SET video_size=video_size+1`,
		"prepared-version": `UPDATE complete_tv_submission_unit SET unit_version=unit_version+1`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newTVResultFixture(t)
			f.exec(t, `ALTER TABLE complete_tv_submission_preparation DISABLE TRIGGER complete_tv_preparation_immutable`)
			f.exec(t, `ALTER TABLE complete_tv_submission_unit DISABLE TRIGGER complete_tv_prepared_unit_immutable`)
			f.exec(t, sql)
			f.reject(t, ports.ErrCompleteTVResultStale)
		})
	}
}

func TestCompleteTVResultPGPhysicalObservationSurvivesChanges(t *testing.T) {
	for name, sql := range map[string]string{
		"account-disabled":            `UPDATE app_user SET is_active=false`,
		"root-generation-health-path": `UPDATE library_root SET generation=generation+1,health='offline',path=path||'/changed'`,
		"source-removed":              `DELETE FROM indexer_query_candidate`,
		"source-expired":              `UPDATE indexer_query SET finished_at=clock_timestamp()-interval '31 minutes'`,
		"material":                    `UPDATE release_candidate SET size_bytes=size_bytes+1,info_hash=repeat('f',40)`,
		"configuration":               `UPDATE download_client SET password_encrypted='changed'`,
		"selection":                   `UPDATE subscription SET state='paused',intent_version=intent_version+1`,
		"metadata":                    `UPDATE media_unit SET metadata_version=metadata_version+1`,
		"protection-expiry":           `UPDATE unit_reservation SET expires_at=clock_timestamp()-interval '1 hour'`,
	} {
		for _, ack := range []bool{true, false} {
			t.Run(name+map[bool]string{true: "/accepted", false: "/unknown"}[ack], func(t *testing.T) {
				f := newTVResultFixture(t)
				f.exec(t, sql)
				f.command.Acknowledged = ack
				if r, err := f.record(context.Background()); err != nil || r == nil {
					t.Fatal(r, err)
				}
				state := "unknown"
				if ack {
					state = "tracking"
				}
				f.assertObserved(t, state)
			})
		}
	}
}

func TestCompleteTVResultPGCompetingOutcomes(t *testing.T) {
	f := newTVResultFixture(t)
	const n = 8
	type result struct {
		r   *ports.CompleteTVSubmissionResultReceipt
		err error
	}
	done := make(chan result, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		c := f.command
		c.Acknowledged = i%2 == 0
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r, err := f.store.RecordCompleteTVSubmissionResult(context.Background(), c)
			done <- result{r, err}
		}()
	}
	close(start)
	wg.Wait()
	close(done)
	winners, state := 0, ""
	for got := range done {
		if got.err == nil && got.r != nil {
			winners++
			state = got.r.State
		} else if got.r != nil || !errors.Is(got.err, ports.ErrCompleteTVResultStale) {
			t.Fatal(got)
		}
	}
	if winners != 1 {
		t.Fatal("multiple physical outcomes recorded", winners)
	}
	f.assertObserved(t, state)
}

func TestCompleteTVResultPGLateRollback(t *testing.T) {
	for _, table := range []string{"audit_log", "outbox_event", "job"} {
		t.Run(table, func(t *testing.T) {
			f := newTVResultFixture(t)
			f.exec(t, `CREATE FUNCTION fail_result() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'private result tail failure'; END $$`)
			op := "INSERT"
			if table == "job" {
				op = "UPDATE"
			}
			f.exec(t, "CREATE TRIGGER fail_result BEFORE "+op+" ON "+table+" FOR EACH ROW EXECUTE FUNCTION fail_result()")
			f.reject(t, ports.ErrUnavailable)
			f.exec(t, "DROP TRIGGER fail_result ON "+table)
			if _, err := f.record(context.Background()); err != nil {
				t.Fatal("retry original callback", err)
			}
			f.assertObserved(t, "tracking")
		})
	}
	for _, table := range []string{"audit_log", "outbox_event"} {
		t.Run("expiry-at-"+table, func(t *testing.T) {
			f := newTVResultFixture(t)
			f.exec(t, `UPDATE job SET lease_until=clock_timestamp()+interval '1 second'`)
			f.exec(t, `CREATE SEQUENCE result_tail_probe`)
			f.exec(t, `CREATE FUNCTION wait_result() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM nextval('result_tail_probe'); PERFORM pg_sleep(GREATEST(0,(SELECT extract(epoch FROM (lease_until-clock_timestamp())) FROM job LIMIT 1))+0.05); RETURN NEW; END $$`)
			f.exec(t, "CREATE TRIGGER wait_result BEFORE INSERT ON "+table+" FOR EACH ROW EXECUTE FUNCTION wait_result()")
			f.reject(t, ports.ErrCompleteTVResultStale)
			var called bool
			if err := f.f.a.DBPool().QueryRow(context.Background(), `SELECT is_called FROM result_tail_probe`).Scan(&called); err != nil || !called {
				t.Fatal("tail not reached", err)
			}
		})
	}
	t.Run("conditional-job-noop", func(t *testing.T) {
		f := newTVResultFixture(t)
		f.exec(t, `CREATE FUNCTION swallow_result_finish() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='succeeded' THEN RETURN NULL; END IF; RETURN NEW; END $$`)
		f.exec(t, `CREATE TRIGGER swallow_result_finish BEFORE UPDATE ON job FOR EACH ROW EXECUTE FUNCTION swallow_result_finish()`)
		f.reject(t, ports.ErrCompleteTVResultStale)
	})
}

func resultPool(t *testing.T, f *tvResultFixture, lockTimeout string) *pgxpool.Pool {
	t.Helper()
	cfg := f.f.a.DBPool().Config()
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, "SET statement_timeout='15s'; SET lock_timeout='"+lockTimeout+"'")
		return err
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestCompleteTVResultPGBlockedLeaseAndCancellation(t *testing.T) {
	for _, tc := range []struct{ name, lockSQL, match string }{
		{"unit", `SELECT id FROM media_unit WHERE id IN(SELECT unit_id FROM complete_tv_submission_unit) ORDER BY id FOR NO KEY UPDATE`, "FROM media_unit"},
		{"task", `SELECT id FROM download_task FOR UPDATE`, "FROM download_task"},
		{"job", `SELECT id FROM job FOR UPDATE`, "FROM job"},
		{"demand", `SELECT id FROM download_requirement ORDER BY unit_id FOR UPDATE`, "FROM download_requirement"},
		{"lane", `SELECT id FROM acquisition_reservation ORDER BY unit_id FOR UPDATE`, "FROM acquisition_reservation"},
		{"reservation", `SELECT id FROM unit_reservation ORDER BY unit_id FOR UPDATE`, "FROM unit_reservation"},
	} {
		t.Run(tc.name, func(t *testing.T) { runTVResultBlocked(t, tc.lockSQL, tc.match, "expiry") })
	}
	t.Run("canceled-lock", func(t *testing.T) { runTVResultBlocked(t, `SELECT id FROM job FOR UPDATE`, "FROM job", "cancel") })
	t.Run("infrastructure-lock-timeout", func(t *testing.T) {
		runTVResultBlocked(t, `SELECT id FROM unit_reservation ORDER BY unit_id FOR UPDATE`, "FROM unit_reservation", "timeout")
	})
	t.Run("closed-pool", func(t *testing.T) {
		f := newTVResultFixture(t)
		pool := resultPool(t, f, "10s")
		pool.Close()
		f.store = postgres.NewCompleteTVSubmissionStore(pool, idgen.New())
		f.reject(t, ports.ErrUnavailable)
	})
	t.Run("already-canceled", func(t *testing.T) {
		f := newTVResultFixture(t)
		before := f.snapshot(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		r, err := f.record(ctx)
		if r != nil || !errors.Is(err, context.Canceled) || f.snapshot(t) != before {
			t.Fatal(r, err)
		}
	})
}

func runTVResultBlocked(t *testing.T, lockSQL, match, mode string) {
	t.Helper()
	f := newTVResultFixture(t)
	lockTimeout := "10s"
	if mode == "timeout" {
		lockTimeout = "150ms"
	}
	f.store = postgres.NewCompleteTVSubmissionStore(resultPool(t, f, lockTimeout), idgen.New())
	f.exec(t, `UPDATE job SET lease_until=clock_timestamp()+interval '2 seconds'`)
	before := f.snapshot(t)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	tx, err := f.f.a.DBPool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err = tx.Exec(ctx, lockSQL); err != nil {
		t.Fatal(err)
	}
	type result struct {
		r   *ports.CompleteTVSubmissionResultReceipt
		err error
	}
	done := make(chan result, 1)
	go func() { r, e := f.record(ctx); done <- result{r, e} }()
	received := false
	defer func() {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		cancel()
		if !received {
			<-done
		}
	}()
	deadline, waiting := time.Now().Add(time.Second), false
	for time.Now().Before(deadline) {
		var n int
		if err = f.f.a.DBPool().QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query ILIKE '%'||$1||'%'`, match).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			waiting = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("callback never reached expected lock", match)
	}
	want := ports.ErrCompleteTVResultStale
	switch mode {
	case "expiry":
		if _, err = tx.Exec(ctx, `SELECT pg_sleep(GREATEST(0,(SELECT extract(epoch FROM (lease_until-clock_timestamp())) FROM job LIMIT 1))+0.05)`); err != nil {
			t.Fatal(err)
		}
		if err = tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
	case "cancel":
		cancel()
		want = context.Canceled
	case "timeout":
		want = ports.ErrUnavailable
	}
	got := <-done
	received = true
	_ = tx.Rollback(context.WithoutCancel(ctx))
	if got.r != nil || !errors.Is(got.err, want) {
		t.Fatal("blocked callback accepted/misclassified", got.r, got.err, want)
	}
	if f.snapshot(t) != before {
		t.Fatal("blocked callback changed original submitting task or protection")
	}
}

func TestCompleteTVResultPGAdditionalBindings(t *testing.T) {
	for _, kind := range []string{"extra-lane", "extra-prepared-unit", "extra-root-reservation", "other-task", "original-after-reclaim", "preparation-job", "preparation-owner", "preparation-token", "proof-extra", "proof-file"} {
		t.Run(kind, func(t *testing.T) {
			f := newTVResultFixture(t)
			switch kind {
			case "extra-lane", "extra-prepared-unit":
				f.exec(t, `INSERT INTO media(id,title,media_type) VALUES('00000000-0000-0000-0000-000000000010','Unrelated','tv')`)
				f.exec(t, `INSERT INTO media_unit(id,media_id,unit_key) VALUES('00000000-0000-0000-0000-000000000011','00000000-0000-0000-0000-000000000010','S01E01')`)
				if kind == "extra-lane" {
					f.exec(t, `INSERT INTO acquisition_reservation(id,unit_id,operation_id,candidate_id,task_id,source_root_id,source_generation,source_stamp) SELECT '00000000-0000-0000-0000-000000000012','00000000-0000-0000-0000-000000000011',operation_id,candidate_id,task_id,source_root_id,source_generation,source_stamp FROM acquisition_reservation LIMIT 1`)
				} else {
					f.exec(t, `INSERT INTO complete_tv_submission_unit(task_id,unit_id,unit_version,provider_episode_id,unit_key,video_path,video_size) SELECT task_id,'00000000-0000-0000-0000-000000000011',1,9999,'S01E03','extra.mkv',512 FROM complete_tv_submission_unit LIMIT 1`)
				}
			case "extra-root-reservation":
				f.exec(t, `INSERT INTO library_root(id,path,media_type,name) VALUES('00000000-0000-0000-0000-000000000013','/unrelated','tv','unrelated')`)
				f.exec(t, `INSERT INTO unit_reservation(id,unit_id,root_id,purpose,candidate_id,operation_id,state,expires_at) SELECT '00000000-0000-0000-0000-000000000014',unit_id,'00000000-0000-0000-0000-000000000013',purpose,candidate_id,operation_id,'dispatched',expires_at FROM unit_reservation LIMIT 1`)
			case "other-task":
				other := uuid.New()
				f.exec(t, `INSERT INTO download_task(id,operation_id,client_id,candidate_id,info_hash,state,save_path) SELECT $1,$2,client_id,candidate_id,info_hash,'submitting',save_path FROM download_task LIMIT 1`, other, uuid.New())
				f.command.TaskID = other
			case "original-after-reclaim":
				f.exec(t, `UPDATE job SET lease_owner='reclaimer',lease_token=124,lease_until=clock_timestamp()+interval '1 minute'`)
			case "preparation-job":
				other := uuid.New()
				f.exec(t, `INSERT INTO job(id,kind,state,payload,lease_owner,lease_token,lease_until) SELECT $1,kind,state,payload,lease_owner,lease_token,lease_until FROM job LIMIT 1`, other)
				f.exec(t, `ALTER TABLE complete_tv_submission_preparation DISABLE TRIGGER complete_tv_preparation_immutable`)
				f.exec(t, `UPDATE complete_tv_submission_preparation SET job_id=$1`, other)
			case "preparation-owner", "preparation-token", "proof-extra", "proof-file":
				f.exec(t, `ALTER TABLE complete_tv_submission_preparation DISABLE TRIGGER complete_tv_preparation_immutable`)
				switch kind {
				case "preparation-owner":
					f.exec(t, `UPDATE complete_tv_submission_preparation SET lease_owner='different-sender'`)
				case "preparation-token":
					f.exec(t, `UPDATE complete_tv_submission_preparation SET lease_token=124`)
				case "proof-extra":
					f.exec(t, `UPDATE complete_tv_submission_preparation SET proof=jsonb_set(proof,'{files}',proof->'files'||jsonb_build_array(proof->'files'->0))`)
				default:
					f.exec(t, `UPDATE complete_tv_submission_preparation SET proof=jsonb_set(proof,'{files,0,Path}','"different.mkv"')`)
				}
			}
			f.reject(t, ports.ErrCompleteTVResultStale)
		})
	}
}

func TestCompleteTVResultPGFrozenPathProof(t *testing.T) {
	for _, change := range []string{
		`jsonb_set(proof,'{save_path}','"/different-original-path"')`,
		`jsonb_set(proof-'save_path','{kind}','"complete_tv_prepared_v1"')`,
	} {
		t.Run(change, func(t *testing.T) {
			f := newTVResultFixture(t)
			f.exec(t, `ALTER TABLE complete_tv_submission_preparation DISABLE TRIGGER complete_tv_preparation_immutable`)
			f.exec(t, `UPDATE complete_tv_submission_preparation SET proof=`+change)
			f.reject(t, ports.ErrCompleteTVResultStale)
		})
	}
}

func TestCompleteTVResultPGVersionedProofConstraint(t *testing.T) {
	for name, expression := range map[string]string{
		"missing-path": `proof-'save_path'`,
		"empty-path":   `jsonb_set(proof,'{save_path}','""')`,
		"null-path":    `jsonb_set(proof,'{save_path}','null')`,
		"numeric-path": `jsonb_set(proof,'{save_path}','42')`,
		"missing-kind": `proof-'kind'`,
		"unknown-kind": `jsonb_set(proof,'{kind}','"complete_tv_prepared_v99"')`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newTVResultFixture(t)
			f.exec(t, `ALTER TABLE complete_tv_submission_preparation DISABLE TRIGGER complete_tv_preparation_immutable`)
			before := f.snapshot(t)
			_, e := f.f.a.DBPool().Exec(context.Background(), `UPDATE complete_tv_submission_preparation SET proof=`+expression)
			var check *pgconn.PgError
			if !errors.As(e, &check) || check.Code != "23514" {
				t.Fatal("invalid v2 proof was not rejected by CHECK")
			}
			if f.snapshot(t) != before {
				t.Fatal("invalid proof changed protection")
			}
		})
	}
}

func TestCompleteTVResultPGVersionedProofDowngrade(t *testing.T) {
	f := newTVResultFixture(t)
	before := f.snapshot(t)
	ctx := context.Background()
	// Isolate the original v2 contract from later unrelated empty migrations.
	for version := postgres.CurrentMigrationVersion; version > 61; version-- {
		if got, err := postgres.Rollback(ctx, f.f.a.DBPool().Config().ConnString(), "../../migrations"); err != nil || got != int64(version-1) {
			t.Fatalf("rollback unrelated migration %d: version=%d error=%v", version, got, err)
		}
	}
	if _, e := postgres.Rollback(ctx, f.f.a.DBPool().Config().ConnString(), "../../migrations"); e == nil {
		t.Fatal("downgrade discarded v2 evidence contract")
	}
	if e := postgres.CheckSchemaVersion(ctx, f.f.a.DBPool(), 61); e != nil {
		t.Fatal("failed downgrade changed schema version")
	}
	if f.snapshot(t) != before {
		t.Fatal("failed downgrade changed original evidence")
	}
	if r, e := f.record(ctx); e != nil || r == nil || r.State != "tracking" {
		t.Fatal("failed downgrade damaged original callback")
	}
}
