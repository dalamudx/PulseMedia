package postgres_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/adapter/torrent"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/platform/jobs"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/download"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMovieUpgradeSendJobMigrationRoundTrip(t *testing.T) {
	f := nativeUpgrade(t, "Ok.", false)
	ctx := context.Background()
	if v, e := postgres.Migrate(ctx, f.dsn, observation70MigrationDir(t)); e != nil || v != 70 {
		t.Fatal(v, e)
	}
	functions := func() string {
		t.Helper()
		var s string
		if e := f.p.QueryRow(ctx, `SELECT pg_get_functiondef('pulse_assert_upgrade_preparation(uuid)'::regprocedure)||pg_get_functiondef('pulse_upgrade_preparation_no_job()'::regprocedure)`).Scan(&s); e != nil {
			t.Fatal(e)
		}
		return s
	}
	before := functions()
	dir := send71MigrationDir(t)
	if v, e := postgres.Migrate(ctx, f.dsn, dir); e != nil || v != 71 {
		t.Fatal(v, e)
	}
	if v, e := postgres.Rollback(ctx, f.dsn, dir); e != nil || v != 70 {
		t.Fatal(v, e)
	}
	if functions() != before {
		t.Fatal("Down did not restore original70 preparation guards")
	}
	if v, e := postgres.Migrate(ctx, f.dsn, dir); e != nil || v != 71 {
		t.Fatal(v, e)
	}
}

func TestMovieUpgradeSendJobBusinessRoleAndRestore(t *testing.T) {
	f, _, _, req := sendJobFixture(t, "Fails.")
	ctx := context.Background()
	_, dml := pgtest.GetCluster(t).CreateRoles(t, mustFrozenDB(t, f.dsn))
	pool, e := pgxpool.New(ctx, dml)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	store, e := postgres.NewUpgradePreparationStore(pool, idgen.New(), torrent.Inspector{})
	if e != nil {
		t.Fatal(e)
	}
	command, e := postgres.NewMovieUpgradeSendCommandRepository(store)
	if e != nil {
		t.Fatal(e)
	}
	queued, e := command.QueueMovieUpgradeSend(ctx, f.actor, req, "business-send")
	if e != nil {
		t.Fatal(e)
	}
	queue := jobs.NewQueue(pool, idgen.New(), nativeUpgradeClock{})
	row, e := queue.Claim(ctx, []string{ports.MovieUpgradeSendJobKind}, "business-send", 2*time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	issuer := postgres.NewMovieUpgradeNativeDispatchVerifier()
	driver, e := download.NewMovieUpgradeNativeSendDriver(f.manager, issuer, nativeUpgradeClock{})
	if e != nil {
		t.Fatal(e)
	}
	repo, e := postgres.NewMovieUpgradeNativeJobSendRepository(store, driver, issuer)
	if e != nil {
		t.Fatal(e)
	}
	job := ports.MovieUpgradeSendJobRequest{Lease: ports.JobLease{ID: row.ID, Owner: row.LeaseOwner.String, Token: row.LeaseToken}, OperationID: req.OperationID, TaskID: req.TaskID, CommandID: queued.CommandID}
	out, e := repo.SendMovieUpgradeJob(ctx, f.actor, job, f.proof)
	if e != nil || out.Status != "unknown" || out.ExecutionAllowed || f.adds.Load() != 1 {
		t.Fatal(out, e)
	}
	for _, sql := range []string{`ALTER TABLE movie_upgrade_send_job ADD COLUMN denied int`, `TRUNCATE movie_upgrade_send_job`, `TRUNCATE movie_upgrade_send_job_claim`, `UPDATE movie_upgrade_send_job SET send_key='changed'`, `DELETE FROM movie_upgrade_send_job_claim`} {
		if _, e = pool.Exec(ctx, sql); e == nil {
			t.Fatal("business role mutated evidence/DDL", sql)
		}
	}
	dump := filepath.Join(t.TempDir(), "send-job71.dump")
	frozenPGCommand(t, ctx, "pg_dump", f.dsn, "--no-password", "-Fc", "-f", dump)
	restored := pgtest.GetCluster(t).NewTestDB(t)
	frozenPGCommand(t, ctx, "pg_restore", restored, "--no-password", "--exit-on-error", "--no-owner", "--no-privileges", "-d", mustFrozenDB(t, restored), dump)
	p, e := pgxpool.New(ctx, restored)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	if e = postgres.CheckSchemaVersion(ctx, p, 71); e != nil {
		t.Fatal(e)
	}
	var intact, leak bool
	e = p.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM movie_upgrade_send_job b JOIN job j ON j.id=b.job_id WHERE b.job_id=$1 AND to_jsonb(j)=pulse_upgrade_send_job_terminal(b.job_id)),EXISTS(SELECT 1 FROM movie_upgrade_send_job b WHERE to_jsonb(b)::text LIKE '%passkey/%')`, row.ID).Scan(&intact, &leak)
	if e != nil || !intact || leak {
		t.Fatal("restored job graph or evidence safety", e)
	}
	if _, e = p.Exec(ctx, `TRUNCATE movie_upgrade_send_job_claim`); e == nil {
		t.Fatal("restored claim mutable")
	}
	if _, e = postgres.Rollback(ctx, restored, filepath.Join("..", "..", "..", "migrations")); e == nil || !strings.Contains(e.Error(), "cannot downgrade explicit upgrade SEND history") {
		t.Fatal("historical SEND downgrade", e)
	}
	if _, e = p.Exec(ctx, `INSERT INTO job(id,kind,state,payload,max_attempts) SELECT gen_random_uuid(),'download_submit','queued',jsonb_build_object('operation_id',operation_id),1 FROM movie_upgrade_send_job`); e == nil || !strings.Contains(e.Error(), "upgrade preparation cannot create jobs") {
		t.Fatal("generic submission bypass", e)
	}
}

func TestMovieUpgradeSendJobDownConcurrent(t *testing.T) {
	f, _, _, req := sendJobFixture(t, "Ok.")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	holder, e := f.p.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer rollbackFrozenUpgradeTest(t, holder)
	job, command := uuid.New(), uuid.New()
	var raw []byte
	e = holder.QueryRow(ctx, `SELECT to_jsonb(jsonb_populate_record(NULL::job,jsonb_build_object('id',$1::uuid,'kind','movie_upgrade_native_send','dedupe_key','movie-upgrade-send:'||$2::uuid::text,'payload',jsonb_build_object('command_id',$3::uuid,'operation_id',$2::uuid,'task_id',$4::uuid,'actor_id',$5::uuid),'state','queued','priority',0,'attempts',0,'max_attempts',1,'lease_token',0,'created_at',n,'updated_at',n,'not_before',n))) FROM (SELECT clock_timestamp() n) x`, job, req.OperationID, command, req.TaskID, f.actor.ID).Scan(&raw)
	if e != nil {
		t.Fatal(e)
	}
	_, e = holder.Exec(ctx, `INSERT INTO movie_upgrade_send_job(job_id,command_id,operation_id,task_id,actor_id,command_key,send_key,prepared_sha256,original_sha256,protected_sha256,commanded_at,queued_job) SELECT $1,$2,p.operation_id,p.task_id,p.actor_id,'concurrent-send','job:'||$2::uuid::text,sha256(convert_to(to_jsonb(p)::text,'UTF8')),pulse_upgrade_send_job_original(p.operation_id),pulse_upgrade_send_job_protected(p.operation_id),($4::jsonb->>'created_at')::timestamptz,$4::jsonb FROM movie_upgrade_preparation p WHERE operation_id=$3`, job, command, req.OperationID, raw)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = holder.Exec(ctx, `INSERT INTO job SELECT * FROM jsonb_populate_record(NULL::job,$1::jsonb)`, raw); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	drained := false
	go func() {
		_, err := postgres.Rollback(ctx, f.dsn, filepath.Join("..", "..", "..", "migrations"))
		done <- err
	}()
	defer func() {
		cancel()
		if err := holder.Rollback(context.Background()); err != nil && err.Error() != "tx is closed" {
			t.Error(err)
		}
		if !drained {
			<-done
		}
	}()
	waitPreparationBlock(t, f.p, holder.Conn().PgConn().PID())
	if e = holder.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	e = <-done
	drained = true
	if e == nil || !strings.Contains(e.Error(), "cannot downgrade explicit upgrade SEND history") {
		t.Fatal("concurrent history guard", e)
	}
	if e = postgres.CheckSchemaVersion(ctx, f.p, 71); e != nil {
		t.Fatal(e)
	}
}
