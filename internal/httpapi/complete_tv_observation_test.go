package httpapi_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

type observationFixture struct {
	*tvResultFixture
	observer *postgres.CompleteTVObservationStore
	request  ports.CompleteTVObservationRequest
}

func newObservationFixture(t *testing.T, state string, configure ...func(*completeTVFixture)) *observationFixture {
	t.Helper()
	var f *tvResultFixture
	if len(configure) == 0 {
		f = newTVResultFixture(t)
	} else {
		p := newPrepareTVFixture(t, configure...)
		if _, e := p.prepare(); e != nil {
			t.Fatal(e)
		}
		f = &tvResultFixture{p, ports.CompleteTVSubmissionResultCommand{Lease: p.c.Lease, TaskID: p.c.TaskID, OperationID: p.c.OperationID, Metainfo: p.c.Metainfo, Acknowledged: true}}
	}
	f.exec(t, `UPDATE download_task SET state=$1`, state)
	// The historical sending lease is expired AND reclaimed. It must be irrelevant.
	f.exec(t, `UPDATE job SET lease_owner='retired-sender',lease_token=999,lease_until=clock_timestamp()-interval '1 hour' WHERE id=$1`, f.c.Lease.ID)
	r := ports.CompleteTVObservationRequest{Lease: ports.JobLease{ID: uuid.New(), Owner: "fresh-observer", Token: 7}, TaskID: f.c.TaskID, OperationID: f.c.OperationID}
	f.exec(t, `INSERT INTO job(id,kind,state,payload,lease_owner,lease_token,lease_until) VALUES($1,$2,'running',jsonb_build_object('task_id',$3::text,'operation_id',$4::text),$5,$6,clock_timestamp()+interval '1 minute')`, r.Lease.ID, ports.CompleteTVObservationJobKind, r.TaskID.String(), r.OperationID.String(), r.Lease.Owner, r.Lease.Token)
	return &observationFixture{f, postgres.NewCompleteTVObservationStore(f.f.a.DBPool(), idgen.New()), r}
}
func (f *observationFixture) load(t *testing.T) ports.CompleteTVObservationSnapshot {
	t.Helper()
	s, e := f.observer.LoadCompleteTVObservation(context.Background(), f.request)
	if e != nil || s == nil {
		t.Fatal("load", e)
	}
	return *s
}
func matchingObservation(s ports.CompleteTVObservationSnapshot) ports.CompleteTVRemoteObservation {
	return ports.CompleteTVRemoteObservation{Complete: true, ClientID: s.ClientID, ClientStamp: s.ClientStamp, Matches: []ports.CompleteTVRemoteMatch{{InfoHash: s.InfoHash, OperationTag: s.OperationTag, SavePath: s.SavePath, TotalBytes: s.TotalBytes}}}
}
func (f *observationFixture) rejectObservation(t *testing.T, s ports.CompleteTVObservationSnapshot, o ports.CompleteTVRemoteObservation, want error) {
	t.Helper()
	before := f.snapshot(t)
	r, e := f.observer.CommitCompleteTVObservation(context.Background(), s, o)
	if r != nil || !errors.Is(e, want) {
		t.Fatal("commit", r, e, "want", want)
	}
	if f.snapshot(t) != before {
		t.Fatal("rejection changed task/job/protection/audit/event")
	}
}
func (f *observationFixture) assertRecovery(t *testing.T, state string, review bool) {
	t.Helper()
	var got, job, original string
	var audits, events, lanes, reservations, demands, tasks, preparations, units, organization, facts int
	e := f.f.a.DBPool().QueryRow(context.Background(), `SELECT
 (SELECT state FROM download_task WHERE id=$1),(SELECT state FROM job WHERE id=$2),(SELECT lease_owner FROM job WHERE id=$3),
 (SELECT count(*) FROM audit_log WHERE action='complete_tv.recovery_observed'),
 (SELECT count(*) FROM outbox_event WHERE event_type='complete_tv.recovery_observed'),
 (SELECT count(*) FROM acquisition_reservation WHERE released_at IS NULL),
 (SELECT count(*) FROM unit_reservation WHERE state='dispatched'),
 (SELECT count(*) FROM download_requirement WHERE state='pending'),
 (SELECT count(*) FROM download_task),(SELECT count(*) FROM complete_tv_submission_preparation),
 (SELECT count(*) FROM complete_tv_submission_unit),
 (SELECT count(*) FROM job WHERE kind='download_organization'),
 (SELECT count(*) FROM library_file_unit)+(SELECT count(*) FROM unit_fact WHERE stage='imported')`, f.request.TaskID, f.request.Lease.ID, f.c.Lease.ID).Scan(&got, &job, &original, &audits, &events, &lanes, &reservations, &demands, &tasks, &preparations, &units, &organization, &facts)
	if e != nil || got != state || job != "succeeded" || original != "retired-sender" || audits != 1 || events != 1 || lanes != 2 || reservations != 2 || demands != 2 || tasks != 1 || preparations != 1 || units != 2 || organization != 0 || facts != 0 {
		t.Fatal("recovery/protection invariant", e, got, job, original, audits, events, lanes, reservations, demands, tasks, preparations, units, organization, facts)
	}
	var consistent bool
	e = f.f.a.DBPool().QueryRow(context.Background(), `SELECT j.result=a.payload AND j.result->>'state'=$2 AND (j.result->>'review_required')::boolean=$3 AND e.payload->>'state'=$2 AND (e.payload->>'review_required')::boolean=$3 FROM job j JOIN audit_log a ON a.action='complete_tv.recovery_observed' JOIN outbox_event e ON e.event_type='complete_tv.recovery_observed' WHERE j.id=$1`, f.request.Lease.ID, state, review).Scan(&consistent)
	if e != nil || !consistent {
		t.Fatal("atomic evidence", e)
	}
}
func TestCompleteTVObservationPGRecovery(t *testing.T) {
	for _, state := range []string{"unknown", "submitting"} {
		t.Run(state, func(t *testing.T) {
			f := newObservationFixture(t, state)
			s := f.load(t)
			// Prove no transaction remains held while a future native lookup happens.
			f.exec(t, `SET lock_timeout='100ms'`)
			tx, e := f.f.a.DBPool().Begin(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			if _, e = tx.Exec(context.Background(), `SELECT id FROM download_task FOR UPDATE NOWAIT`); e != nil {
				t.Fatal("load leaked lock", e)
			}
			_ = tx.Rollback(context.Background())
			r, e := f.observer.CommitCompleteTVObservation(context.Background(), s, matchingObservation(s))
			if e != nil || r == nil || r.State != "tracking" || r.ReviewRequired {
				t.Fatal(r, e)
			}
			f.assertRecovery(t, "tracking", false)
			f.rejectObservation(t, s, matchingObservation(s), ports.ErrCompleteTVObservationStale)
		})
	}
}
func TestCompleteTVObservationPGReview(t *testing.T) {
	for _, kind := range []string{"missing", "ambiguous", "hash", "tag", "path", "bytes"} {
		t.Run(kind, func(t *testing.T) {
			f := newObservationFixture(t, "submitting")
			s := f.load(t)
			o := matchingObservation(s)
			switch kind {
			case "missing":
				o.Matches = nil
			case "ambiguous":
				o.Matches = append(o.Matches, o.Matches[0])
			case "hash":
				o.Matches[0].InfoHash = "ffffffffffffffffffffffffffffffffffffffff"
			case "tag":
				o.Matches[0].OperationTag += "-other"
			case "path":
				o.Matches[0].SavePath += "/child"
			case "bytes":
				o.Matches[0].TotalBytes++
			}
			r, e := f.observer.CommitCompleteTVObservation(context.Background(), s, o)
			if e != nil || r == nil || r.State != "unknown" || !r.ReviewRequired {
				t.Fatal(r, e)
			}
			f.assertRecovery(t, "unknown", true)
		})
	}
}
func TestCompleteTVObservationPGUnavailable(t *testing.T) {
	f := newObservationFixture(t, "unknown")
	s := f.load(t)
	o := matchingObservation(s)
	o.Complete = false
	o.Matches = nil
	f.rejectObservation(t, s, o, ports.ErrUnavailable)
	before := f.snapshot(t)
	f.exec(t, `ALTER TABLE complete_tv_submission_preparation RENAME TO unavailable_preparation`)
	r, e := f.observer.CommitCompleteTVObservation(context.Background(), s, matchingObservation(s))
	f.exec(t, `ALTER TABLE unavailable_preparation RENAME TO complete_tv_submission_preparation`)
	if r != nil || !errors.Is(e, ports.ErrUnavailable) || f.snapshot(t) != before {
		t.Fatal(r, e)
	}
}
func TestCompleteTVObservationPGStale(t *testing.T) {
	for _, kind := range []string{"owner", "token", "task", "operation", "seal", "remote-client", "remote-config", "snapshot-bytes", "snapshot-path"} {
		t.Run(kind, func(t *testing.T) {
			f := newObservationFixture(t, "unknown")
			s := f.load(t)
			o := matchingObservation(s)
			switch kind {
			case "owner":
				s.Request.Lease.Owner = "other"
			case "token":
				s.Request.Lease.Token++
			case "task":
				s.Request.TaskID = uuid.New()
			case "operation":
				s.Request.OperationID = uuid.New()
			case "seal":
				s.Seal = "other"
			case "remote-client":
				o.ClientID = uuid.New()
			case "remote-config":
				o.ClientStamp = "other"
			case "snapshot-bytes":
				s.TotalBytes++
			case "snapshot-path":
				s.SavePath += "/child"
			}
			f.rejectObservation(t, s, o, ports.ErrCompleteTVObservationStale)
		})
	}
	for name, sql := range map[string]string{
		"job-owner":        `UPDATE job SET lease_owner='other' WHERE kind='complete_tv_coverage_observation'`,
		"job-token":        `UPDATE job SET lease_token=8 WHERE kind='complete_tv_coverage_observation'`,
		"job-state":        `UPDATE job SET state='queued' WHERE kind='complete_tv_coverage_observation'`,
		"job-expiry":       `UPDATE job SET lease_until=clock_timestamp() WHERE kind='complete_tv_coverage_observation'`,
		"job-kind":         `UPDATE job SET kind='download_submission' WHERE kind='complete_tv_coverage_observation'`,
		"payload-task":     `UPDATE job SET payload=payload-'task_id' WHERE kind='complete_tv_coverage_observation'`,
		"payload-op":       `UPDATE job SET payload=payload-'operation_id' WHERE kind='complete_tv_coverage_observation'`,
		"task-state":       `UPDATE download_task SET state='tracking'`,
		"task-path":        `UPDATE download_task SET save_path=save_path||'/other'`,
		"task-hash":        `UPDATE download_task SET info_hash=repeat('f',40)`,
		"task-version":     `UPDATE download_task SET reconcile_count=reconcile_count+1`,
		"client":           `UPDATE download_client SET username=username||'other'`,
		"lane":             `UPDATE acquisition_reservation SET source_generation=source_generation+1`,
		"lane-time":        `UPDATE acquisition_reservation SET updated_at=updated_at+interval '1 second'`,
		"reservation":      `UPDATE unit_reservation SET state='released'`,
		"reservation-time": `UPDATE unit_reservation SET expires_at=expires_at+interval '1 second'`,
		"requirement":      `UPDATE download_requirement SET state='released'`,
		"fence":            `UPDATE download_requirement SET submission_fence='{}'`,
		"prepared-unit":    `DELETE FROM complete_tv_submission_unit WHERE unit_id=(SELECT unit_id FROM complete_tv_submission_unit LIMIT 1)`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newObservationFixture(t, "unknown")
			s := f.load(t)
			if name == "lane" {
				f.exec(t, `ALTER TABLE acquisition_reservation DISABLE TRIGGER acquisition_lane_guard`)
			}
			if name == "prepared-unit" {
				f.exec(t, `ALTER TABLE complete_tv_submission_unit DISABLE TRIGGER complete_tv_prepared_unit_immutable`)
			}
			f.exec(t, sql)
			f.rejectObservation(t, s, matchingObservation(s), ports.ErrCompleteTVObservationStale)
		})
	}
}
func TestCompleteTVObservationPGPostSendChanges(t *testing.T) {
	f := newObservationFixture(t, "unknown")
	s := f.load(t)
	// These are command authorization changes, not changes to original physical facts.
	f.exec(t, `UPDATE app_user SET is_active=false`)
	f.exec(t, `UPDATE subscription SET state='paused',intent_version=intent_version+1`)
	f.exec(t, `UPDATE library_root SET generation=generation+1`)
	f.exec(t, `UPDATE indexer SET config_version=config_version+1`)
	r, e := f.observer.CommitCompleteTVObservation(context.Background(), s, matchingObservation(s))
	if e != nil || r == nil {
		t.Fatal(r, e)
	}
	f.assertRecovery(t, "tracking", false)
}
func TestCompleteTVObservationPGOriginalLeaseRejected(t *testing.T) {
	f := newObservationFixture(t, "unknown")
	f.request.Lease = f.c.Lease
	before := f.snapshot(t)
	r, e := f.observer.LoadCompleteTVObservation(context.Background(), f.request)
	if r != nil || !errors.Is(e, ports.ErrCompleteTVObservationStale) || f.snapshot(t) != before {
		t.Fatal(r, e)
	}
}
func TestCompleteTVObservationPGDuplicate(t *testing.T) {
	f := newObservationFixture(t, "unknown")
	s := f.load(t)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := f.observer.CommitCompleteTVObservation(context.Background(), s, matchingObservation(s))
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	success, stale := 0, 0
	for e := range errs {
		if e == nil {
			success++
		} else if errors.Is(e, ports.ErrCompleteTVObservationStale) {
			stale++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || stale != 1 {
		t.Fatal(success, stale)
	}
	f.assertRecovery(t, "tracking", false)
}
func TestCompleteTVObservationPGLateRollback(t *testing.T) {
	for _, mode := range []string{"audit-error", "event-error", "job-no-row", "job-expiry", "protection-trigger"} {
		t.Run(mode, func(t *testing.T) {
			f := newObservationFixture(t, "unknown")
			s := f.load(t)
			var table, timing, body string
			switch mode {
			case "audit-error":
				table, timing, body = "audit_log", "BEFORE INSERT", `RAISE EXCEPTION 'private-secret-failure';`
			case "event-error":
				table, timing, body = "outbox_event", "BEFORE INSERT", `RAISE EXCEPTION 'private-secret-failure';`
			case "job-no-row":
				table, timing, body = "job", "BEFORE UPDATE", `IF NEW.kind='complete_tv_coverage_observation' AND NEW.state='succeeded' THEN RETURN NULL; END IF;`
			case "job-expiry":
				table, timing, body = "job", "BEFORE UPDATE", `IF NEW.kind='complete_tv_coverage_observation' AND NEW.state='succeeded' THEN PERFORM pg_sleep(1.1); END IF;`
				f.exec(t, `UPDATE job SET lease_until=clock_timestamp()+interval '1 second' WHERE id=$1`, f.request.Lease.ID)
			case "protection-trigger":
				table, timing, body = "audit_log", "BEFORE INSERT", `UPDATE unit_reservation SET expires_at=expires_at+interval '1 second';`
			}
			f.exec(t, `CREATE FUNCTION observation_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+body+` RETURN NEW; END $$`)
			f.exec(t, `CREATE TRIGGER observation_fault `+timing+` ON `+table+` FOR EACH ROW EXECUTE FUNCTION observation_fault()`)
			want := ports.ErrUnavailable
			if mode == "job-no-row" || mode == "job-expiry" || mode == "protection-trigger" {
				want = ports.ErrCompleteTVObservationStale
			}
			f.rejectObservation(t, s, matchingObservation(s), want)
		})
	}
}
func TestCompleteTVObservationPGWaitExpiry(t *testing.T) {
	for _, table := range []string{"download_task", "job", "acquisition_reservation", "unit_reservation", "download_requirement", "download_client"} {
		t.Run(table, func(t *testing.T) {
			f := newObservationFixture(t, "unknown")
			s := f.load(t)
			f.exec(t, `UPDATE job SET lease_until=clock_timestamp()+interval '1 second' WHERE id=$1`, f.request.Lease.ID)
			before := f.snapshot(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			tx, e := f.f.a.DBPool().Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
			lock := `SELECT 1 FROM ` + table + ` FOR UPDATE`
			if table == "job" {
				lock = `SELECT 1 FROM job WHERE kind='complete_tv_coverage_observation' FOR UPDATE`
			}
			rows, e := tx.Query(ctx, lock)
			if e != nil {
				t.Fatal(e)
			}
			rows.Close()
			done := make(chan error, 1)
			go func() { _, e := f.observer.CommitCompleteTVObservation(ctx, s, matchingObservation(s)); done <- e }()
			joined := false
			defer func() {
				_ = tx.Rollback(context.WithoutCancel(ctx))
				cancel()
				if !joined {
					<-done
				}
			}()
			waiting := false
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				var n int
				e = f.f.a.DBPool().QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock'`).Scan(&n)
				if e != nil {
					t.Fatal(e)
				}
				if n > 0 {
					waiting = true
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if !waiting {
				t.Fatal("did not reach row wait")
			}
			if _, e = tx.Exec(ctx, `SELECT pg_sleep(GREATEST(0,(SELECT extract(epoch FROM lease_until-clock_timestamp()) FROM job WHERE id=$1))+0.05)`, f.request.Lease.ID); e != nil {
				t.Fatal(e)
			}
			_ = tx.Rollback(ctx)
			e = <-done
			joined = true
			if !errors.Is(e, ports.ErrCompleteTVObservationStale) || f.snapshot(t) != before {
				t.Fatal("late lease accepted or mutated", e)
			}
		})
	}
}
func TestCompleteTVObservationPGGenericJobIsolation(t *testing.T) {
	f := newObservationFixture(t, "unknown")
	q := db.New(f.f.a.DBPool())
	_, e := q.GuardDownloadSubmissionCommit(context.Background(), db.GuardDownloadSubmissionCommitParams{ID: f.request.Lease.ID, Owner: f.request.Lease.Owner, Token: f.request.Lease.Token})
	if e == nil {
		t.Fatal("generic submission consumed observation authority")
	}
}

func TestCompleteTVObservationPGIndependentObservers(t *testing.T) {
	f := newObservationFixture(t, "unknown")
	a := f.load(t)
	f.request.Lease = ports.JobLease{ID: uuid.New(), Owner: "other-observer", Token: 81}
	f.exec(t, `INSERT INTO job(id,kind,state,payload,lease_owner,lease_token,lease_until) VALUES($1,$2,'running',jsonb_build_object('task_id',$3::text,'operation_id',$4::text),$5,$6,clock_timestamp()+interval '1 minute')`, f.request.Lease.ID, ports.CompleteTVObservationJobKind, f.request.TaskID.String(), f.request.OperationID.String(), f.request.Lease.Owner, f.request.Lease.Token)
	b := f.load(t)
	done := make(chan error, 2)
	for _, s := range []ports.CompleteTVObservationSnapshot{a, b} {
		go func(s ports.CompleteTVObservationSnapshot) {
			_, e := f.observer.CommitCompleteTVObservation(context.Background(), s, matchingObservation(s))
			done <- e
		}(s)
	}
	ok, stale := 0, 0
	for range 2 {
		e := <-done
		if e == nil {
			ok++
		} else if errors.Is(e, ports.ErrCompleteTVObservationStale) {
			stale++
		} else {
			t.Fatal(e)
		}
	}
	if ok != 1 || stale != 1 {
		t.Fatal(ok, stale)
	}
	var audits, events, finished int
	e := f.f.a.DBPool().QueryRow(context.Background(), `SELECT (SELECT count(*) FROM audit_log WHERE action='complete_tv.recovery_observed'),(SELECT count(*) FROM outbox_event WHERE event_type='complete_tv.recovery_observed'),(SELECT count(*) FROM job WHERE kind='complete_tv_coverage_observation' AND state='succeeded')`).Scan(&audits, &events, &finished)
	if e != nil || audits != 1 || events != 1 || finished != 1 {
		t.Fatal(e, audits, events, finished)
	}
}
func TestCompleteTVObservationPGPreparationCorruption(t *testing.T) {
	for _, kind := range []string{"v1", "proof-bytes", "proof-path", "proof-files", "task-operation", "other-task", "extra-requirement"} {
		t.Run(kind, func(t *testing.T) {
			f := newObservationFixture(t, "unknown")
			s := f.load(t)
			switch kind {
			case "v1", "proof-bytes", "proof-path", "proof-files":
				f.exec(t, `ALTER TABLE complete_tv_submission_preparation DISABLE TRIGGER complete_tv_preparation_immutable`)
				switch kind {
				case "v1":
					f.exec(t, `UPDATE complete_tv_submission_preparation SET proof=jsonb_set(proof-'save_path','{kind}','"complete_tv_prepared_v1"')`)
				case "proof-bytes":
					f.exec(t, `UPDATE complete_tv_submission_preparation SET torrent_sha256=decode(repeat('ff',32),'hex')`)
				case "proof-path":
					f.exec(t, `UPDATE complete_tv_submission_preparation SET proof=jsonb_set(proof,'{save_path}','"/other"')`)
				case "proof-files":
					f.exec(t, `UPDATE complete_tv_submission_preparation SET proof=jsonb_set(proof,'{files}',proof->'files'||jsonb_build_array(proof->'files'->0))`)
				}
			case "task-operation":
				f.exec(t, `UPDATE download_task SET operation_id=$1`, uuid.New())
			case "other-task":
				other := uuid.New()
				f.exec(t, `INSERT INTO download_task(id,operation_id,client_id,candidate_id,info_hash,state,save_path) SELECT $1,$2,client_id,candidate_id,info_hash,'unknown',save_path FROM download_task LIMIT 1`, other, uuid.New())
				s.Request.TaskID = other
			case "extra-requirement":
				f.exec(t, `INSERT INTO download_requirement(id,operation_id,unit_id,root_id,actor_type,actor_id,submission_fence) SELECT $1,operation_id,unit_id,root_id,'system','other-observer',submission_fence FROM download_requirement LIMIT 1`, uuid.New())
			}
			f.rejectObservation(t, s, matchingObservation(s), ports.ErrCompleteTVObservationStale)
		})
	}
}
func TestCompleteTVObservationPGFinalReturning(t *testing.T) {
	for _, body := range []string{
		`NEW.lease_token=NEW.lease_token+1;`,
		`NEW.payload=NEW.payload-'task_id';`,
		`NEW.result='{}';`,
		`NEW.state='queued';`,
	} {
		t.Run(body, func(t *testing.T) {
			f := newObservationFixture(t, "unknown")
			s := f.load(t)
			f.exec(t, `CREATE FUNCTION observation_returning_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='complete_tv_coverage_observation' AND NEW.state='succeeded' THEN `+body+` END IF; RETURN NEW; END $$`)
			f.exec(t, `CREATE TRIGGER observation_returning_fault BEFORE UPDATE ON job FOR EACH ROW EXECUTE FUNCTION observation_returning_fault()`)
			f.rejectObservation(t, s, matchingObservation(s), ports.ErrCompleteTVObservationStale)
		})
	}
}
func TestCompleteTVObservationPGCancellation(t *testing.T) {
	f := newObservationFixture(t, "unknown")
	s := f.load(t)
	before := f.snapshot(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, e := f.observer.CommitCompleteTVObservation(ctx, s, matchingObservation(s))
	if r != nil || !errors.Is(e, context.Canceled) || f.snapshot(t) != before {
		t.Fatal(r, e)
	}
}

func TestCompleteTVObservationPGAuthorityAfterWait(t *testing.T) {
	for _, mode := range []string{"owner", "token", "state", "protection"} {
		t.Run(mode, func(t *testing.T) {
			f := newObservationFixture(t, "unknown")
			s := f.load(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			tx, e := f.f.a.DBPool().Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
			query := `SELECT id FROM job WHERE id=$1 FOR UPDATE`
			arg := f.request.Lease.ID
			if mode == "protection" {
				query = `SELECT unit_id FROM complete_tv_submission_unit WHERE task_id=$1 ORDER BY unit_id`
				rows, err := tx.Query(ctx, query, f.request.TaskID)
				if err != nil {
					t.Fatal(err)
				}
				var ids []uuid.UUID
				for rows.Next() {
					var id uuid.UUID
					if err = rows.Scan(&id); err != nil {
						t.Fatal(err)
					}
					ids = append(ids, id)
				}
				rows.Close()
				if err = rows.Err(); err != nil {
					t.Fatal(err)
				}
				query = `SELECT id FROM media_unit WHERE id=$1 FOR UPDATE`
				arg = ids[0]
			}
			var locked uuid.UUID
			if e = tx.QueryRow(ctx, query, arg).Scan(&locked); e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() { _, err := f.observer.CommitCompleteTVObservation(ctx, s, matchingObservation(s)); done <- err }()
			joined := false
			defer func() {
				_ = tx.Rollback(context.WithoutCancel(ctx))
				cancel()
				if !joined {
					<-done
				}
			}()
			deadline, waiting := time.Now().Add(time.Second), false
			for time.Now().Before(deadline) {
				var n int
				e = f.f.a.DBPool().QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock'`).Scan(&n)
				if e != nil {
					t.Fatal(e)
				}
				if n > 0 {
					waiting = true
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if !waiting {
				t.Fatal("observation did not wait")
			}
			sql := `UPDATE job SET lease_owner='reclaimer' WHERE id=$1`
			arg = f.request.Lease.ID
			switch mode {
			case "token":
				sql = `UPDATE job SET lease_token=55 WHERE id=$1`
			case "state":
				sql = `UPDATE job SET state='queued' WHERE id=$1`
			case "protection":
				sql = `UPDATE unit_reservation SET expires_at=expires_at+interval '1 second' WHERE operation_id=$1`
				arg = f.request.OperationID
			}
			if _, e = tx.Exec(ctx, sql, arg); e != nil {
				t.Fatal(e)
			}
			if e = tx.Commit(ctx); e != nil {
				t.Fatal(e)
			}
			before := f.snapshot(t)
			e = <-done
			joined = true
			if !errors.Is(e, ports.ErrCompleteTVObservationStale) || f.snapshot(t) != before {
				t.Fatal("authority changed across wait", e)
			}
		})
	}
}
