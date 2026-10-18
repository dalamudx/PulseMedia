package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestMovieUpgradeSendJobDatabaseClaimFrozenPG(t *testing.T) {
	f, c, repo, req := sendJobFixtureFromNative(t, nativeUpgrade(t, "Ok.", true))
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	queued, e := c.QueueMovieUpgradeSend(ctx, f.actor, req, "freeze-send")
	if e != nil {
		t.Fatal(e)
	}
	job := claimSendJob(t, f, queued)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	f.setHook(func() error { close(entered); <-release; return nil })
	done := make(chan error, 1)
	drained := false
	go func() { _, err := repo.SendMovieUpgradeJob(ctx, f.actor, job, f.proof); done <- err }()
	defer func() {
		unblock()
		if !drained {
			<-done
		}
	}()
	select {
	case <-entered:
	case e = <-done:
		drained = true
		t.Fatal("Add not reached", e)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for _, stmt := range []string{`UPDATE job SET lease_until=lease_until+interval '1 minute' WHERE id=$1`, `UPDATE job SET lease_owner='replacement' WHERE id=$1`, `UPDATE job SET lease_token=lease_token+1 WHERE id=$1`, `UPDATE job SET state='queued',lease_until=NULL,lease_owner=NULL WHERE id=$1`} {
		_, e = f.p.Exec(ctx, stmt, queued.JobID)
		var pg *pgconn.PgError
		if !errors.As(e, &pg) || pg.Code != "23514" || pg.ConstraintName != "movie_upgrade_send_job_frozen" {
			t.Fatal("captured claim mutation was not rejected by dedicated guard", e)
		}
	}
	unblock()
	e = <-done
	drained = true
	if e != nil || f.adds.Load() != 1 {
		t.Fatal("original claim could not finish", e, f.adds.Load())
	}
}

func TestMovieUpgradeSendJobPhysicalLeaseExpiryPG(t *testing.T) {
	f, c, repo, req := sendJobFixtureFromNative(t, nativeUpgrade(t, "Ok.", true))
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	before := nativeProtectedGraph(t, f.preparedUpgradeFixture)
	queued, e := c.QueueMovieUpgradeSend(ctx, f.actor, req, "expire-send")
	if e != nil {
		t.Fatal(e)
	}
	queue := jobs.NewQueue(f.p, idgen.New(), nativeUpgradeClock{})
	row, e := queue.Claim(ctx, []string{ports.MovieUpgradeSendJobKind}, "short-send", 2*time.Second)
	if e != nil {
		t.Fatal(e)
	}
	job := ports.MovieUpgradeSendJobRequest{Lease: ports.JobLease{ID: row.ID, Owner: row.LeaseOwner.String, Token: row.LeaseToken}, OperationID: req.OperationID, TaskID: req.TaskID, CommandID: queued.CommandID}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	f.setHook(func() error { close(entered); <-release; return nil })
	done := make(chan error, 1)
	drained := false
	go func() { _, err := repo.SendMovieUpgradeJob(ctx, f.actor, job, f.proof); done <- err }()
	defer func() {
		unblock()
		if !drained {
			<-done
		}
	}()
	select {
	case <-entered:
	case e = <-done:
		drained = true
		t.Fatal("Add not reached", e)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, e = f.p.Exec(ctx, `SELECT pg_sleep(GREATEST(0,extract(epoch FROM lease_until-clock_timestamp()))+0.02) FROM job WHERE id=$1`, row.ID); e != nil {
		t.Fatal(e)
	}
	unblock()
	e = <-done
	drained = true
	if e == nil {
		t.Fatal("expired return completed job")
	}
	var intact bool
	e = f.p.QueryRow(ctx, `SELECT j.state='running' AND EXISTS(SELECT 1 FROM movie_upgrade_send_authorization WHERE operation_id=b.operation_id) AND EXISTS(SELECT 1 FROM movie_upgrade_send_dispatch WHERE operation_id=b.operation_id) AND NOT EXISTS(SELECT 1 FROM movie_upgrade_send_result WHERE operation_id=b.operation_id) FROM job j JOIN movie_upgrade_send_job b ON b.job_id=j.id WHERE j.id=$1`, row.ID).Scan(&intact)
	if e != nil || !intact || f.adds.Load() != 1 || before != nativeProtectedGraph(t, f.preparedUpgradeFixture) {
		t.Fatal("expiry lost protection or permit", e, f.adds.Load())
	}
	if _, e = repo.SendMovieUpgradeJob(ctx, f.actor, job, f.proof); e == nil || f.adds.Load() != 1 {
		t.Fatal("expired send replayed", e, f.adds.Load())
	}
}

func TestMovieUpgradeSendJobChangedConfigurationReturnPG(t *testing.T) {
	f, c, repo, req := sendJobFixture(t, "Ok.")
	ctx := context.Background()
	queued, e := c.QueueMovieUpgradeSend(ctx, f.actor, req, "config-send")
	if e != nil {
		t.Fatal(e)
	}
	job := claimSendJob(t, f, queued)
	f.setHook(func() error {
		_, err := f.p.Exec(ctx, `UPDATE download_client SET url='http://127.0.0.1:1' WHERE id=$1`, f.client)
		return err
	})
	result, e := repo.SendMovieUpgradeJob(ctx, f.actor, job, f.proof)
	if e != nil || result.Status != "confirmed" || f.adds.Load() != 1 {
		t.Fatal("original return rejected after config change", result, e)
	}
	var intact bool
	e = f.p.QueryRow(ctx, `SELECT j.state='succeeded' AND r.task_id=b.task_id AND to_jsonb(t)=p.task_snapshot AND EXISTS(SELECT 1 FROM unit_fact WHERE unit_id=a.unit_id AND source_file_id=a.old_file_id AND revoked_at IS NULL) FROM movie_upgrade_send_job b JOIN job j ON j.id=b.job_id JOIN movie_upgrade_send_result r USING(operation_id) JOIN movie_upgrade_authority a USING(operation_id) JOIN movie_upgrade_preparation p USING(operation_id) JOIN download_task t ON t.id=b.task_id WHERE b.job_id=$1`, queued.JobID).Scan(&intact)
	if e != nil || !intact {
		t.Fatal("original task/fact graph changed", e)
	}
}

func TestMovieUpgradeSendJobResultTailRollbackPG(t *testing.T) {
	f, c, repo, req := sendJobFixture(t, "Ok.")
	ctx := context.Background()
	before := nativeProtectedGraph(t, f.preparedUpgradeFixture)
	queued, e := c.QueueMovieUpgradeSend(ctx, f.actor, req, "tail-send")
	if e != nil {
		t.Fatal(e)
	}
	job := claimSendJob(t, f, queued)
	f.exec(t, `CREATE FUNCTION test_send_result_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN UPDATE media_unit SET facts_version=facts_version+1 WHERE id=(SELECT unit_id FROM movie_upgrade_authority WHERE operation_id=NEW.operation_id); RETURN NULL; END $$; CREATE CONSTRAINT TRIGGER zz_test_send_result_tail AFTER INSERT ON movie_upgrade_send_result DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION test_send_result_tail()`)
	if _, e = repo.SendMovieUpgradeJob(ctx, f.actor, job, f.proof); e == nil {
		t.Fatal("tail fault committed")
	}
	var intact bool
	e = f.p.QueryRow(ctx, `SELECT j.state='running' AND NOT EXISTS(SELECT 1 FROM movie_upgrade_send_result WHERE operation_id=b.operation_id) AND EXISTS(SELECT 1 FROM movie_upgrade_send_dispatch WHERE operation_id=b.operation_id) FROM movie_upgrade_send_job b JOIN job j ON j.id=b.job_id WHERE b.job_id=$1`, queued.JobID).Scan(&intact)
	if e != nil || !intact || before != nativeProtectedGraph(t, f.preparedUpgradeFixture) || f.adds.Load() != 1 {
		t.Fatal("tail failure did not roll back result/job/facts", e)
	}
}

func TestMovieUpgradeSendJobCommandRevokedAfterWaitPG(t *testing.T) {
	f, c, _, req := sendJobFixture(t, "Ok.")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	holder, e := f.p.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer rollbackFrozenUpgradeTest(t, holder)
	if _, e = holder.Exec(ctx, `UPDATE app_user SET is_active=false WHERE id=$1::uuid`, f.actor.ID); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, err := c.QueueMovieUpgradeSend(ctx, f.actor, req, "revoked-send"); done <- err }()
	waitPreparationBlock(t, f.p, holder.Conn().PgConn().PID())
	if e = holder.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("revoked actor queued send", e)
	}
	var count int
	if e = f.p.QueryRow(ctx, `SELECT count(*) FROM movie_upgrade_send_job`).Scan(&count); e != nil || count != 0 || f.adds.Load() != 0 {
		t.Fatal(count, e)
	}
}

func TestMovieUpgradeSendJobCommandTailRollbackPG(t *testing.T) {
	f, c, _, req := sendJobFixture(t, "Ok.")
	ctx := context.Background()
	before := nativeProtectedGraph(t, f.preparedUpgradeFixture)
	f.exec(t, `CREATE FUNCTION test_send_command_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='upgrade.queue-send' THEN UPDATE job SET result='{"forged":true}'::jsonb WHERE kind='movie_upgrade_native_send'; END IF; RETURN NULL; END $$; CREATE TRIGGER test_send_command_tail AFTER INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION test_send_command_tail()`)
	if _, e := c.QueueMovieUpgradeSend(ctx, f.actor, req, "tail-command"); e == nil {
		t.Fatal("changed command graph committed")
	}
	var rows int
	if e := f.p.QueryRow(ctx, `SELECT (SELECT count(*) FROM movie_upgrade_send_job)+(SELECT count(*) FROM job WHERE kind='movie_upgrade_native_send')+(SELECT count(*) FROM audit_log WHERE action='upgrade.queue-send')`).Scan(&rows); e != nil || rows != 0 || before != nativeProtectedGraph(t, f.preparedUpgradeFixture) {
		t.Fatal("command rollback incomplete", rows, e)
	}
}
