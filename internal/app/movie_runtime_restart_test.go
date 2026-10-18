package app

import (
	"context"
	"encoding/hex"
	"io"
	"os"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/config"
	"github.com/dalamudx/PulseMedia/internal/platform/logging"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/google/uuid"
)

// This probe uses two full applications; no service reconstruction or manual claim.
func TestAppRunMovieLinkCommitFailureRestartsSameFrozenPlan(t *testing.T) {
	f := organizationSetup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dsn := f.pool.Config().ConnConfig.ConnString()
	observer, e := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if e != nil {
		t.Fatal(e)
	}
	defer observer.Close()
	f.observe(t)
	if _, e = observer.Exec(ctx, `CREATE FUNCTION reject_movie_runtime_import() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_type='media.imported' THEN RAISE EXCEPTION 'injected import commit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_movie_runtime_import BEFORE INSERT ON outbox_event FOR EACH ROW EXECUTE FUNCTION reject_movie_runtime_import()`); e != nil {
		t.Fatal(e)
	}
	cfg := config.NewDefault()
	cfg.DataDir = t.TempDir()
	cfg.SecretKey = hex.EncodeToString(outboxTestKey)
	cfg.Database.URL = dsn
	cfg.Server.HTTPAddr = "127.0.0.1:0"
	start := func() func() {
		t.Helper()
		a, e := New(cfg, WithLogger(logging.NewLogger(io.Discard, "error")))
		if e != nil {
			t.Fatal(e)
		}
		runCtx, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- a.Run(runCtx) }()
		joined := false
		closeApp := func() {
			if joined {
				return
			}
			joined = true
			stop()
			select {
			case e := <-done:
				if e != nil {
					t.Fatal(e)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("application failed to drain")
			}
		}
		t.Cleanup(closeApp)
		return closeApp
	}
	stop := start()
	defer func() {
		if t.Failed() {
			var diagnostic string
			if e := observer.QueryRow(context.Background(), `SELECT jsonb_build_object('tasks',(SELECT jsonb_agg(jsonb_build_object('state',state,'organization',organization_state,'reason',organization_reason)) FROM download_task),'jobs',(SELECT jsonb_agg(jsonb_build_object('kind',kind,'state',state,'attempts',attempts,'error',error)) FROM job),'roots',(SELECT jsonb_agg(jsonb_build_object('health',health,'baseline',baseline_completed)) FROM library_root))::text`).Scan(&diagnostic); e == nil {
				t.Log(diagnostic)
			} else {
				t.Log("diagnostic query", e)
			}
		}
	}()
	// Observe the real failed transaction only after it created the physical link.
	waitOutboxCondition(t, ctx, func() bool {
		var ready bool
		e := observer.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job WHERE dedupe_key='organize:'||$1::uuid::text AND kind='download_organization' AND attempts>=1 AND error IS NOT NULL) AND NOT EXISTS(SELECT 1 FROM unit_fact WHERE unit_id=$2 AND stage='imported' AND revoked_at IS NULL)`, f.taskID, f.unit).Scan(&ready)
		if e != nil {
			t.Fatal(e)
		}
		return ready
	})
	var plan uuid.UUID
	var token int64
	var original, target string
	if e = observer.QueryRow(ctx, `SELECT p.id,j.lease_token,s.target_path,jsonb_build_object('unit',s.unit_id,'source',s.source_path,'target',s.target_path,'size',s.source_size,'mtime',s.source_mtime,'device',s.source_device,'inode',s.source_inode,'quality',s.quality_profile)::text FROM transfer_plan p JOIN transfer_step s ON s.plan_id=p.id JOIN job j ON j.dedupe_key='organize:'||$2::uuid::text AND j.kind='download_organization' WHERE p.operation_id=$1`, f.op, f.taskID).Scan(&plan, &token, &target, &original); e != nil {
		t.Fatal(e)
	}
	before, e := os.Stat(target)
	if e != nil {
		t.Fatal(e)
	}
	source, e := os.Stat(f.path)
	if e != nil || !os.SameFile(before, source) {
		t.Fatal("first application did not create original hard link", e)
	}
	stop()
	if _, e = observer.Exec(ctx, `DROP TRIGGER reject_movie_runtime_import ON outbox_event; DROP FUNCTION reject_movie_runtime_import()`); e != nil {
		t.Fatal(e)
	}
	// Explicit fixture time advancement, not a change to production lease/backoff.
	if _, e = observer.Exec(ctx, `UPDATE job SET not_before=clock_timestamp(),lease_until=CASE WHEN state='running' THEN clock_timestamp()-interval '1 second' ELSE lease_until END WHERE dedupe_key='organize:'||$1::uuid::text AND kind='download_organization'`, f.taskID); e != nil {
		t.Fatal(e)
	}
	if _, e = observer.Exec(ctx, `UPDATE transfer_plan SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, plan); e != nil {
		t.Fatal(e)
	}
	stop = start()
	waitOutboxCondition(t, ctx, func() bool {
		var ready bool
		e := observer.QueryRow(ctx, `SELECT state='succeeded' AND lease_token>$2 FROM job WHERE dedupe_key='organize:'||$1::uuid::text AND kind='download_organization'`, f.taskID, token).Scan(&ready)
		if e != nil {
			t.Fatal(e)
		}
		return ready
	})
	f.pool = observer
	f.q = postgres.Queries(observer)
	f.assertImported(t)
	var plans int
	var unchanged bool
	if e = observer.QueryRow(ctx, `SELECT (SELECT count(*) FROM transfer_plan WHERE operation_id=$1),p.id=$2 AND jsonb_build_object('unit',s.unit_id,'source',s.source_path,'target',s.target_path,'size',s.source_size,'mtime',s.source_mtime,'device',s.source_device,'inode',s.source_inode,'quality',s.quality_profile)=$3::jsonb FROM transfer_plan p JOIN transfer_step s ON s.plan_id=p.id WHERE p.operation_id=$1`, f.op, plan, original).Scan(&plans, &unchanged); e != nil || plans != 1 || !unchanged {
		t.Fatal("original frozen plan not preserved", plans, unchanged, e)
	}
	after, e := os.Stat(target)
	if e != nil || !os.SameFile(before, after) {
		t.Fatal("restart replaced original linked inode", e)
	}
	stop()
}
