package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/clock"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/outbox"
	"github.com/dalamudx/PulseMedia/internal/platform/rootfs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	storetransfer "github.com/dalamudx/PulseMedia/internal/store/transfer"
	transfercase "github.com/dalamudx/PulseMedia/internal/usecase/transfer"
	"github.com/jackc/pgx/v5/pgxpool"
)

type recoveryClockFilesystem struct {
	ports.TransferFilesystem
	advance func(context.Context) error
	once    sync.Once
	err     error
}
type recoveryClockRoot struct {
	ports.TransferRoot
	owner *recoveryClockFilesystem
}

func (r recoveryClockRoot) Inspect(ctx context.Context, path string) (ports.FileIdentity, error) {
	info, err := r.TransferRoot.Inspect(ctx, path)
	if err == nil {
		r.owner.once.Do(func() { r.owner.err = r.owner.advance(ctx) })
		err = r.owner.err
	}
	return info, err
}
func (f *recoveryClockFilesystem) LockRoots(ctx context.Context, paths ...string) (map[string]ports.TransferRoot, func(), error) {
	roots, release, err := f.TransferFilesystem.LockRoots(ctx, paths...)
	if err == nil {
		for path, root := range roots {
			roots[path] = recoveryClockRoot{root, f}
		}
	}
	return roots, release, err
}
func assertOriginalRecoveryFreshness(t *testing.T, pool *pgxpool.Pool, actor *ports.Actor, command ports.TransferRecoveryCommand) {
	t.Helper()
	ctx := context.Background()
	q := db.New(pool)
	exec := func(sql string) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	exec("CREATE TABLE recovery_clock_probe(advanced boolean NOT NULL); INSERT INTO recovery_clock_probe VALUES(false)")
	exec("CREATE FUNCTION public.clock_timestamp() RETURNS timestamptz LANGUAGE sql AS $$SELECT pg_catalog.clock_timestamp()+CASE WHEN advanced THEN interval '6 seconds' ELSE interval '0 seconds' END FROM recovery_clock_probe$$")
	defer func() { exec("DROP FUNCTION public.clock_timestamp(); DROP TABLE recovery_clock_probe") }()
	config := pool.Config()
	config.ConnConfig.RuntimeParams["search_path"] = "public,pg_catalog"
	clockPool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer clockPool.Close()
	plan, err := q.GetTransferPlanByID(ctx, command.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	task, err := q.GetDownloadTaskByOpID(ctx, plan.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal([]any{plan, task})
	fs := &recoveryClockFilesystem{TransferFilesystem: rootfs.NewFilesystem(), advance: func(ctx context.Context) error {
		_, e := pool.Exec(ctx, "UPDATE recovery_clock_probe SET advanced=true")
		return e
	}}
	repo := storetransfer.NewRepository(clockPool, idgen.New(), outbox.New(pool, idgen.New(), clock.New()), fs, transfercase.ExecutePreparedPlan)
	preview, err := repo.PreviewOriginalTransferRecovery(ctx, actor, command.PlanID)
	if err != nil || preview == nil || preview.Recoverable || preview.Reason != "file_observation_expired" {
		t.Fatal("old file inspection accepted", preview, err)
	}
	exec("UPDATE recovery_clock_probe SET advanced=false")
	exec("CREATE FUNCTION advance_recovery_receipt_clock() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN UPDATE recovery_clock_probe SET advanced=true;RETURN NEW;END$$;CREATE TRIGGER advance_recovery_receipt_clock BEFORE INSERT ON idempotency_record FOR EACH ROW WHEN(NEW.idempotency_key='transfer.recovery:expired-observation') EXECUTE FUNCTION advance_recovery_receipt_clock()")
	repo = storetransfer.NewRepository(clockPool, idgen.New(), outbox.New(pool, idgen.New(), clock.New()), rootfs.NewFilesystem(), transfercase.ExecutePreparedPlan)
	_, err = repo.CommitOriginalTransferRecovery(ctx, actor, command, "expired-observation")
	exec("DROP TRIGGER advance_recovery_receipt_clock ON idempotency_record;DROP FUNCTION advance_recovery_receipt_clock()")
	if !errors.Is(err, ports.ErrTransferReviewRequired) {
		t.Fatal("expired final write accepted", err)
	}
	currentPlan, err := q.GetTransferPlanByID(ctx, command.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	currentTask, err := q.GetDownloadTaskByOpID(ctx, plan.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal([]any{currentPlan, currentTask})
	if string(after) != string(before) {
		t.Fatal("expired observation changed plan or task")
	}
	var receipts, audits, events int
	if err = pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM idempotency_record WHERE idempotency_key='transfer.recovery:expired-observation'),(SELECT count(*) FROM audit_log WHERE action='transfer.original_recovery' AND target_id=$1),(SELECT count(*) FROM outbox_event WHERE aggregate_type='transfer_recovery' AND aggregate_id=$1)", command.PlanID.String()).Scan(&receipts, &audits, &events); err != nil {
		t.Fatal(err)
	}
	if receipts != 0 || audits != 0 || events != 0 {
		t.Fatal("expired observation left command artifacts", receipts, audits, events)
	}
}
