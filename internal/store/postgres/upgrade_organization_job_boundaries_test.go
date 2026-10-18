package postgres_test

import (
	"context"
	"fmt"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestMovieUpgradeOrganizationJobAdmissionPG(t *testing.T) {
	for _, mode := range []string{"unconfirmed", "wrong_preparation", "changed_mode", "changed_fact", "first_job_row", "first_command_row", "api_actor", "forged_receipt"} {
		t.Run(mode, func(t *testing.T) {
			f := organizationJobFixture76(t)
			ctx := context.Background()
			c := f.command
			a := *f.actor
			switch mode {
			case "unconfirmed":
				c.Confirmed = false
			case "wrong_preparation":
				c.PreparationID = uuid.New()
			case "changed_mode":
				f.exec(t, `UPDATE subscription SET upgrade_mode='disabled',intent_version=intent_version+1 WHERE id=$1`, f.sub)
			case "changed_fact":
				f.exec(t, `UPDATE unit_fact SET revoked_at=clock_timestamp() WHERE id=$1`, f.fact)
			case "first_job_row":
				f.exec(t, `CREATE FUNCTION org_test_initial()RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='movie_upgrade_organization_execute' THEN NEW.priority:=5; END IF; RETURN NEW; END $$; CREATE TRIGGER zz_org_test_initial BEFORE INSERT ON job FOR EACH ROW EXECUTE FUNCTION org_test_initial()`)
			case "first_command_row":
				f.exec(t, `CREATE FUNCTION org_test_initial()RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN NEW.queued_job:=jsonb_set(NEW.queued_job,'{priority}','5'); RETURN NEW; END $$; CREATE TRIGGER zz_org_test_initial BEFORE INSERT ON movie_upgrade_organization_job FOR EACH ROW EXECUTE FUNCTION org_test_initial()`)
			case "api_actor":
				a.Scopes = []string{"read"}
			case "forged_receipt":
				f.queued(t)
				f.exec(t, `UPDATE idempotency_record SET response_body=jsonb_set(response_body,'{job_id}',to_jsonb(gen_random_uuid())) WHERE idempotency_key='upgrade.organization-queue:execute-command'`)
				if _, e := f.repo.ReadMovieUpgradeOrganizationJobReceipt(ctx, f.actor, f.operation, "execute-command"); e == nil {
					t.Fatal("forged receipt recovered")
				}
				return
			}
			if out, e := f.repo.QueueMovieUpgradeOrganization(ctx, &a, c, "execute-command"); e == nil || out != nil {
				t.Fatal(out, e)
			}
			var n int
			if e := f.p.QueryRow(ctx, `SELECT count(*) FROM movie_upgrade_organization_job`).Scan(&n); e != nil || n != 0 {
				t.Fatal(n, e)
			}
		})
	}
}
func TestMovieUpgradeOrganizationJobPermissionWaitPG(t *testing.T) {
	for _, mode := range []string{"receipt", "capture"} {
		t.Run(mode, func(t *testing.T) {
			f := organizationJobFixture76(t)
			f.queued(t)
			lease := f.lease(t, time.Minute)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			holder, e := f.p.Acquire(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer holder.Release()
			tx, e := holder.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = tx.Exec(ctx, `UPDATE app_user SET is_active=false WHERE id=$1`, f.actor.ID); e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() {
				if mode == "receipt" {
					_, e := f.repo.ReadMovieUpgradeOrganizationJobReceipt(ctx, f.actor, f.operation, "execute-command")
					done <- e
				} else {
					_, e := f.repo.CaptureMovieUpgradeOrganizationJob(ctx, f.actor, lease)
					done <- e
				}
			}()
			drained := false
			defer func() {
				cancel()
				_ = tx.Rollback(context.Background())
				if !drained {
					<-done
				}
			}()
			waitPreparationBlock(t, f.p, holder.Conn().PgConn().PID())
			if e = tx.Commit(ctx); e != nil {
				t.Fatal(e)
			}
			e = <-done
			drained = true
			if e == nil {
				t.Fatal("revocation ignored")
			}
			var n int
			if e = f.p.QueryRow(ctx, `SELECT count(*) FROM movie_upgrade_organization_job_claim`).Scan(&n); e != nil || n != 0 {
				t.Fatal(n, e)
			}
		})
	}
}
func TestMovieUpgradeOrganizationJobTailPG(t *testing.T) {
	for _, mode := range []string{"queue_account", "claim_account", "claim_row"} {
		t.Run(mode, func(t *testing.T) {
			f := organizationJobFixture76(t)
			ctx := context.Background()
			var lease ports.JobLease
			if mode != "queue_account" {
				f.queued(t)
				lease = f.lease(t, time.Minute)
			}
			if mode == "claim_row" {
				f.exec(t, `CREATE FUNCTION org_tail()RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN NEW.captured_at:=NEW.captured_at+interval '1 second'; RETURN NEW; END $$; CREATE TRIGGER zz_org_tail BEFORE INSERT ON movie_upgrade_organization_job_claim FOR EACH ROW EXECUTE FUNCTION org_tail()`)
			} else {
				action := "upgrade.organization-queue:"
				if mode == "claim_account" {
					action = "upgrade.organization-claim:"
				}
				f.exec(t, fmt.Sprintf(`CREATE FUNCTION org_tail()RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN IF NEW.idempotency_key LIKE '%s%%' THEN UPDATE app_user SET is_active=false WHERE id='%s'; END IF; RETURN NEW; END $$; CREATE TRIGGER zz_org_tail BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION org_tail()`, action, f.actor.ID))
			}
			var e error
			if mode == "queue_account" {
				_, e = f.repo.QueueMovieUpgradeOrganization(ctx, f.actor, f.command, "execute-command")
			} else {
				_, e = f.repo.CaptureMovieUpgradeOrganizationJob(ctx, f.actor, lease)
			}
			if e == nil {
				t.Fatal("tail mutation committed")
			}
			var n int
			if e = f.p.QueryRow(ctx, `SELECT count(*) FROM movie_upgrade_organization_job_claim`).Scan(&n); e != nil || n != 0 {
				t.Fatal(n, e)
			}
			var active bool
			if e = f.p.QueryRow(ctx, `SELECT is_active FROM app_user WHERE id=$1`, f.actor.ID).Scan(&active); e != nil || !active {
				t.Fatal("tail change not rolled back", active, e)
			}
		})
	}
}
func TestMovieUpgradeOrganizationJobLeaseCrossingPG(t *testing.T) {
	f := organizationJobFixture76(t)
	f.queued(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	holder, e := f.p.Acquire(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer holder.Release()
	key := int64(23676091)
	if _, e = holder.Exec(ctx, `SELECT pg_advisory_lock($1)`, key); e != nil {
		t.Fatal(e)
	}
	f.exec(t, `CREATE FUNCTION org_lease_wait()RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(23676091); RETURN NEW; END $$; CREATE TRIGGER zz_org_lease_wait AFTER INSERT ON movie_upgrade_organization_job_claim FOR EACH ROW EXECUTE FUNCTION org_lease_wait()`)
	lease := f.lease(t, 5*time.Second)
	done := make(chan error, 1)
	go func() { _, e := f.repo.CaptureMovieUpgradeOrganizationJob(ctx, f.actor, lease); done <- e }()
	drained := false
	defer func() {
		cancel()
		_, _ = holder.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, key)
		if !drained {
			<-done
		}
	}()
	waitPreparationBlock(t, f.p, holder.Conn().PgConn().PID())
	var until time.Time
	if e = f.p.QueryRow(ctx, `SELECT lease_until FROM job WHERE id=$1`, lease.ID).Scan(&until); e != nil {
		t.Fatal(e)
	}
	timer := time.NewTimer(time.Until(until) + 50*time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var expired bool
	if e = f.p.QueryRow(ctx, `SELECT lease_until<=clock_timestamp() FROM job WHERE id=$1`, lease.ID).Scan(&expired); e != nil || !expired {
		t.Fatal(expired, e)
	}
	if _, e = holder.Exec(ctx, `SELECT pg_advisory_unlock($1)`, key); e != nil {
		t.Fatal(e)
	}
	e = <-done
	drained = true
	if e == nil {
		t.Fatal("late claim committed")
	}
	var n int
	if e = f.p.QueryRow(ctx, `SELECT count(*) FROM movie_upgrade_organization_job_claim`).Scan(&n); e != nil || n != 0 {
		t.Fatal(n, e)
	}
}
