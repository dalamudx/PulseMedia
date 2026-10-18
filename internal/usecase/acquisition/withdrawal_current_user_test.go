package acquisition_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/acquisition"
	"github.com/google/uuid"
)

func TestWithdrawalReceiptTailFailureRollsBack(t *testing.T) {
	for _, table := range []string{"audit_log", "idempotency_record"} {
		t.Run(table, func(t *testing.T) {
			f := newCommandFixture(t)
			ctx := context.Background()
			op, e := f.svc.AcquireMovie(ctx, f.actor, f.cmd)
			if e != nil {
				t.Fatal(e)
			}
			svc := acquisition.NewWithdrawalService(postgres.NewDownloadSubmissionStore(f.pool, idgen.New()))
			own, e := svc.List(ctx, f.actor, op.OperationID)
			if e != nil || len(own) != 1 {
				t.Fatal(own, e)
			}
			command := ports.RequirementWithdrawalCommand{OperationID: op.OperationID, RequirementID: own[0].ID, ExpectedFence: own[0].FenceDigest, Key: "withdraw-tail"}
			snapshot := func() string {
				var s string
				e := f.pool.QueryRow(ctx, `SELECT jsonb_build_object('requirement',(SELECT to_jsonb(r) FROM download_requirement r WHERE r.id=$1),'reservations',(SELECT jsonb_agg(to_jsonb(r)) FROM unit_reservation r WHERE r.operation_id=$2),'tasks',(SELECT jsonb_agg(to_jsonb(d) ORDER BY d.id) FROM download_task d WHERE d.operation_id=$2),'jobs',(SELECT jsonb_agg(to_jsonb(j) ORDER BY j.id) FROM job j),'lane',(SELECT jsonb_agg(to_jsonb(l)) FROM acquisition_reservation l WHERE l.operation_id=$2),'root',(SELECT to_jsonb(r) FROM library_root r WHERE r.id=$3))::text`, own[0].ID, op.OperationID, f.cmd.RootID).Scan(&s)
				if e != nil {
					t.Fatal(e)
				}
				return s
			}
			before := snapshot()
			counts := commandCounts(t, f)
			if _, e = f.pool.Exec(ctx, `CREATE FUNCTION test_withdraw_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic tail failure'; END $$`); e != nil {
				t.Fatal(e)
			}
			if _, e = f.pool.Exec(ctx, "CREATE TRIGGER test_withdraw_tail BEFORE INSERT ON "+table+" FOR EACH ROW EXECUTE FUNCTION test_withdraw_tail()"); e != nil {
				t.Fatal(e)
			}
			if _, e = svc.Withdraw(ctx, f.actor, command); e == nil {
				t.Fatal("tail failure accepted")
			}
			if snapshot() != before || commandCounts(t, f) != counts {
				t.Fatal("tail failure changed demand/protection/job/root or receipt")
			}
			if _, e = f.pool.Exec(ctx, "DROP TRIGGER test_withdraw_tail ON "+table); e != nil {
				t.Fatal(e)
			}
			first, e := svc.Withdraw(ctx, f.actor, command)
			if e != nil || !first.PhysicalReleased {
				t.Fatal("unsent retry did not release", first, e)
			}
			restored, e := svc.Withdraw(ctx, f.actor, command)
			if e != nil || *restored != *first {
				t.Fatal("receipt retry changed outcome", e)
			}
		})
	}
}

func TestWithdrawalCurrentUserAfterLock(t *testing.T) {
	for _, mode := range []string{"list", "keyed", "unkeyed", "replay"} {
		t.Run(mode, func(t *testing.T) {
			f := newCommandFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			op, e := f.svc.AcquireMovie(ctx, f.actor, f.cmd)
			if e != nil {
				t.Fatal(e)
			}
			repo := postgres.NewDownloadSubmissionStore(f.pool, idgen.New())
			svc := acquisition.NewWithdrawalService(repo)
			rows, e := svc.List(ctx, f.actor, op.OperationID)
			if e != nil || len(rows) != 1 {
				t.Fatal(rows, e)
			}
			c := ports.RequirementWithdrawalCommand{OperationID: op.OperationID, RequirementID: rows[0].ID, ExpectedFence: rows[0].FenceDigest, Key: "withdraw-lock"}
			if mode == "replay" {
				if _, e = svc.Withdraw(ctx, f.actor, c); e != nil {
					t.Fatal(e)
				}
			}
			before := commandCounts(t, f)
			tx, e := f.pool.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
			if _, e = tx.Exec(ctx, "SELECT id FROM app_user WHERE id=$1 FOR UPDATE", f.actor.ID); e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				var err error
				switch mode {
				case "list":
					_, err = svc.List(ctx, f.actor, op.OperationID)
				case "keyed", "replay":
					_, err = svc.Withdraw(ctx, f.actor, c)
				case "unkeyed":
					_, err = repo.ReleaseRequirement(ctx, f.actor, op.OperationID, rows[0].UnitID, rows[0].RootID)
				}
				done <- err
			}()
			defer func() { cancel(); _ = tx.Rollback(context.WithoutCancel(ctx)); <-finished }()
			waitAcquisitionLock(t, f, ctx, "%LockAcquisitionCommandUser%")
			if _, e = tx.Exec(ctx, "UPDATE app_user SET is_active=false WHERE id=$1", f.actor.ID); e != nil {
				t.Fatal(e)
			}
			if e = tx.Commit(ctx); e != nil {
				t.Fatal(e)
			}
			if e = <-done; !errors.Is(e, ports.ErrForbidden) {
				t.Fatal("late user disable did not reject", e)
			}
			if commandCounts(t, f) != before {
				t.Fatal("rejected waiting caller changed protected counts")
			}
			after, e := repo.ListOwnRequirements(ctx, f.actor, op.OperationID)
			if !errors.Is(e, ports.ErrForbidden) || after != nil {
				t.Fatal("disabled list after lock", after, e)
			}
		})
	}
}

func TestWithdrawalCurrentUserBeforeReadWriteAndReceipt(t *testing.T) {
	for _, phase := range []string{"new", "replay"} {
		for _, mode := range []string{"demoted", "disabled", "deleted", "token", "system", "missing", "invalid"} {
			t.Run(phase+"/"+mode, func(t *testing.T) {
				f := newCommandFixture(t)
				pool, q, acq, actor, cmd := f.pool, db.New(f.pool), f.svc, f.actor, f.cmd
				ctx := context.Background()
				op, e := acq.AcquireMovie(ctx, actor, cmd)
				if e != nil {
					t.Fatal(e)
				}
				client := uuid.New()
				if _, e = q.CreateDownloadClient(ctx, db.CreateDownloadClientParams{ID: client, Name: "withdraw-fixture", ClientType: "qbittorrent", Url: "https://fixture.invalid", PasswordEncrypted: []byte("sealed"), RemotePathMap: []byte(`{}`)}); e != nil {
					t.Fatal(e)
				}
				if _, e = q.CreateDownloadTask(ctx, db.CreateDownloadTaskParams{ID: uuid.New(), OperationID: op.OperationID, ClientID: client, CandidateID: cmd.CandidateID, State: "unknown", SavePath: "/fixture"}); e != nil {
					t.Fatal(e)
				}
				repo := postgres.NewDownloadSubmissionStore(pool, idgen.New())
				svc := acquisition.NewWithdrawalService(repo)
				own, e := svc.List(ctx, actor, op.OperationID)
				if e != nil || len(own) != 1 {
					t.Fatal(own, e)
				}
				c := ports.RequirementWithdrawalCommand{OperationID: op.OperationID, RequirementID: own[0].ID, ExpectedFence: own[0].FenceDigest, Key: "withdraw-current"}
				if phase == "replay" {
					r, e := svc.Withdraw(ctx, actor, c)
					if e != nil || r.PhysicalReleased {
						t.Fatal(r, e)
					}
				}
				state := func() string {
					var s string
					if e := pool.QueryRow(ctx, `SELECT jsonb_build_object('requirements',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM download_requirement x),'reservations',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM unit_reservation x),'tasks',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM download_task x),'jobs',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM job x),'audits',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM audit_log x),'events',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM outbox_event x),'receipts',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM idempotency_record x))::text`).Scan(&s); e != nil {
						t.Fatal(e)
					}
					return s
				}
				before := state()
				stale := *actor
				switch mode {
				case "demoted":
					_, e = pool.Exec(ctx, "UPDATE app_user SET role='member' WHERE id=$1", uuid.MustParse(actor.ID))
				case "disabled":
					_, e = pool.Exec(ctx, "UPDATE app_user SET is_active=false WHERE id=$1", uuid.MustParse(actor.ID))
				case "deleted":
					_, e = pool.Exec(ctx, "DELETE FROM app_user WHERE id=$1", uuid.MustParse(actor.ID))
				case "token":
					stale.Type = ports.ActorTypeService
				case "system":
					stale.Type = ports.ActorTypeSystem
				case "missing":
					stale.ID = uuid.NewString()
				case "invalid":
					stale.ID = "invalid-user"
				}
				if e != nil {
					t.Fatal(e)
				}
				if rows, e := svc.List(ctx, &stale, op.OperationID); !errors.Is(e, ports.ErrForbidden) || rows != nil {
					t.Fatal("stale user read requirement", rows, e)
				}
				if r, e := svc.Withdraw(ctx, &stale, c); !errors.Is(e, ports.ErrForbidden) || r != nil {
					t.Fatal("stale user withdrew/replayed", r, e)
				}
				if released, e := repo.ReleaseRequirement(ctx, &stale, op.OperationID, own[0].UnitID, own[0].RootID); !errors.Is(e, ports.ErrForbidden) || released {
					t.Fatal("unkeyed release bypass", released, e)
				}
				if after := state(); after != before {
					t.Fatal("rejected caller changed protected records")
				}
			})
		}
	}
}
