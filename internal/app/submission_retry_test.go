package app

import (
	"bytes"
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/operation"
	"github.com/google/uuid"
	"sync"
	"testing"
)

func TestAppRunOriginalUnsentSubmissionRetry(t *testing.T) {
	runFullMoviePipelineSource(t, true, "", "", func(f *organizationFixture, a *ports.Actor, op uuid.UUID, _ *App) func() {
		ctx := context.Background()
		svc := operation.NewSubmissionRetryService(postgres.NewDownloadSubmissionStore(f.pool, f.gen))
		var jobID uuid.UUID
		if e := f.pool.QueryRow(ctx, "UPDATE job SET state='dead',attempts=max_attempts,lease_token=41,lease_owner=NULL,lease_until=NULL WHERE kind='download_submission' AND payload->>'operation_id'=$1 RETURNING id", op.String()).Scan(&jobID); e != nil {
			t.Fatal(e)
		}
		job, e := f.q.GetJobByID(ctx, jobID)
		if e != nil {
			t.Fatal(e)
		}
		p, e := svc.Preview(ctx, a, op)
		if e != nil || !p.CanRetry || p.JobID == nil || *p.JobID != jobID {
			t.Fatal("native frozen preview", p, e)
		}
		c := ports.SubmissionRetryCommand{OperationID: op, JobID: jobID, Key: "original-unsent-retry", ExpectedStamp: p.InputStamp, Acknowledged: true}
		for _, bad := range []*ports.Actor{{Type: ports.ActorTypeSystem, ID: a.ID, Role: a.Role}, {Type: ports.ActorTypeUser, ID: a.ID, Role: a.Role, Scopes: []string{"read"}}, {Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: a.Role}} {
			if _, e = svc.Retry(ctx, bad, c); !errors.Is(e, ports.ErrForbidden) {
				t.Fatal("borrowed actor", e)
			}
		}
		wrong := c
		wrong.JobID = uuid.New()
		if _, e = svc.Retry(ctx, a, wrong); !errors.Is(e, ports.ErrSubmissionRetryConflict) {
			t.Fatal("borrowed job", e)
		}
		if _, e = f.pool.Exec(ctx, "UPDATE job SET lease_token=42 WHERE id=$1", jobID); e != nil {
			t.Fatal(e)
		}
		if _, e = svc.Retry(ctx, a, c); !errors.Is(e, ports.ErrSubmissionRetryConflict) {
			t.Fatal("changed token", e)
		}
		if _, e = f.pool.Exec(ctx, "UPDATE job SET lease_token=41 WHERE id=$1", jobID); e != nil {
			t.Fatal(e)
		}
		for _, tail := range []struct{ table, field, value string }{{"audit_log", "action", "download.submission_retry"}, {"outbox_event", "event_type", "download.submission_retry"}, {"idempotency_record", "idempotency_key", c.Key}} {
			sql := "CREATE FUNCTION retry_tail_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW." + tail.field + "='" + tail.value + "' THEN RAISE EXCEPTION 'fixture failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER retry_tail_failure BEFORE INSERT ON " + tail.table + " FOR EACH ROW EXECUTE FUNCTION retry_tail_failure()"
			if _, e = f.pool.Exec(ctx, sql); e != nil {
				t.Fatal(e)
			}
			if _, e = svc.Retry(ctx, a, c); !errors.Is(e, ports.ErrUnavailable) {
				t.Fatal("tail failure", tail.table, e)
			}
			if _, e = f.pool.Exec(ctx, "DROP TRIGGER retry_tail_failure ON "+tail.table+"; DROP FUNCTION retry_tail_failure()"); e != nil {
				t.Fatal(e)
			}
			now, e := f.q.GetJobByID(ctx, jobID)
			if e != nil || now.State != "dead" || now.LeaseToken != 41 || now.Attempts != job.Attempts || now.MaxAttempts != job.MaxAttempts || !bytes.Equal(now.Payload, job.Payload) {
				t.Fatal("retry tail rollback", now.State, e)
			}
		}
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				v, e := svc.Retry(ctx, a, c)
				if e == nil && (v.JobID != jobID || v.OperationID != op || v.State != "queued") {
					e = errors.New("receipt changed")
				}
				errs <- e
			}()
		}
		wg.Wait()
		close(errs)
		for e := range errs {
			if e != nil {
				t.Fatal(e)
			}
		}
		now, e := f.q.GetJobByID(ctx, jobID)
		if e != nil || now.State != "queued" || now.LeaseToken != 42 || now.Attempts != job.Attempts || now.MaxAttempts != job.Attempts+1 || !bytes.Equal(now.Payload, job.Payload) {
			t.Fatal("original job requeue", now.State, e)
		}
		var audits, events, receipts, physical int
		if e = f.pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM audit_log WHERE action='download.submission_retry'),(SELECT count(*) FROM outbox_event WHERE event_type='download.submission_retry'),(SELECT count(*) FROM idempotency_record WHERE idempotency_key=$1),(SELECT count(*) FROM download_task)", c.Key).Scan(&audits, &events, &receipts, &physical); e != nil || audits != 1 || events != 1 || receipts != 1 || physical != 0 {
			t.Fatal("atomic original retry", audits, events, receipts, physical, e)
		}
		return func() {
			if v, e := svc.Preview(ctx, a, op); e != nil || v.CanRetry || v.HoldReason != "physical_task_exists" {
				t.Fatal("sent retry held", v, e)
			}
			if _, e = f.pool.Exec(ctx, "UPDATE library_root SET health='offline' WHERE id=$1", f.root); e != nil {
				t.Fatal(e)
			}
			if _, e = svc.Retry(ctx, a, c); e != nil {
				t.Fatal("late receipt", e)
			}
			if _, e = f.pool.Exec(ctx, "UPDATE app_user SET is_active=false WHERE id=$1", uuid.MustParse(a.ID)); e != nil {
				t.Fatal(e)
			}
			if _, e = svc.Retry(ctx, a, c); !errors.Is(e, ports.ErrForbidden) {
				t.Fatal("disabled receipt", e)
			}
		}
	})
}
