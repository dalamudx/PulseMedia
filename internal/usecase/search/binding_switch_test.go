package search_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"sync"
	"testing"
	"time"
)

func TestBindingSwitchCurrentUserReceiptAndOldRound(t *testing.T) {
	f := runtimeFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	old := f.collect(t)
	before, e := f.q.GetSubscriptionByID(ctx, f.sub)
	if e != nil {
		t.Fatal(e)
	}
	svc := subscription.NewBindingService(postgres.NewSubscriptionBindingStore(f.pool, idgen.New()))
	cmd := ports.SubscriptionBindingsCommand{ExpectedVersion: before.IntentVersion, IndexerIDs: []uuid.UUID{}}
	if _, e = f.pool.Exec(ctx, `CREATE FUNCTION reject_binding_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture'; END $$; CREATE TRIGGER reject_binding_receipt BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION reject_binding_receipt()`); e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Configure(ctx, f.actor, f.sub, cmd, "switch-once"); e == nil {
		t.Fatal("tail fault accepted")
	}
	unchanged, e := f.q.GetSubscriptionByID(ctx, f.sub)
	if e != nil || unchanged.IntentVersion != before.IntentVersion || string(unchanged.IndexerIds) != string(before.IndexerIds) {
		t.Fatal("partial binding", e)
	}
	if _, e = f.pool.Exec(ctx, "DROP TRIGGER reject_binding_receipt ON idempotency_record"); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := svc.Configure(ctx, f.actor, f.sub, cmd, "switch-once")
			if e == nil && (r.IntentVersion != before.IntentVersion+1 || len(r.IndexerIDs) != 0 || r.DownloaderID != nil) {
				e = errors.New("unstable receipt")
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
	after, e := f.q.GetSubscriptionByID(ctx, f.sub)
	if e != nil || after.State != before.State || after.ScopeType != before.ScopeType || after.QualityPolicyRevision != before.QualityPolicyRevision {
		t.Fatal("unrelated input changed", e)
	}
	job := f.finalJob(t, old)
	result, e := f.coord.FinalizeLeasedRound(ctx, old.ID, authority(job))
	if e == nil || result == nil || result.State != "aborted" {
		t.Fatal("old round accepted", result, e)
	}
	var roundsBefore, jobsBefore int
	if e = f.pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM search_round),(SELECT count(*) FROM job)").Scan(&roundsBefore, &jobsBefore); e != nil {
		t.Fatal(e)
	}
	if _, e = f.coord.StartScheduledRound(ctx, f.wanted, time.Second); !errors.Is(e, ports.ErrRejected) {
		t.Fatal("empty bindings borrowed all endpoints", e)
	}
	var roundsAfter, jobsAfter int
	if e = f.pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM search_round),(SELECT count(*) FROM job)").Scan(&roundsAfter, &jobsAfter); e != nil || roundsBefore != roundsAfter || jobsBefore != jobsAfter {
		t.Fatal("empty binding wrote automatic work", roundsBefore, roundsAfter, jobsBefore, jobsAfter, e)
	}
	for _, actor := range []*ports.Actor{{Type: ports.ActorTypeSystem, ID: f.actor.ID, Role: ports.RoleAdmin}, {Type: ports.ActorTypeService, ID: f.actor.ID, Role: ports.RoleAdmin}, {Type: ports.ActorTypeUser, ID: f.actor.ID, Role: ports.RoleAdmin, Scopes: []string{"read"}}, {Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin}} {
		if _, e = svc.Configure(ctx, actor, f.sub, cmd, "switch-once"); !errors.Is(e, ports.ErrForbidden) {
			t.Fatal("invalid actor replay", e)
		}
	}
	if _, e = f.pool.Exec(ctx, "UPDATE app_user SET role='member' WHERE id=$1", uuid.MustParse(f.actor.ID)); e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Configure(ctx, f.actor, f.sub, cmd, "switch-once"); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("stale admin replay", e)
	}
	if _, e = f.pool.Exec(ctx, "UPDATE app_user SET role='admin' WHERE id=$1", uuid.MustParse(f.actor.ID)); e != nil {
		t.Fatal(e)
	}
	tx, e := f.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, e = tx.Exec(ctx, "UPDATE app_user SET is_active=false WHERE id=$1", uuid.MustParse(f.actor.ID)); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := svc.Configure(ctx, f.actor, f.sub, cmd, "switch-once"); done <- e }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var waiting bool
		if e = f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%app_user%' AND pid<>pg_backend_pid())`).Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("user lock not reached")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if e = <-done; !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("deactivated replay after lock", e)
	}
	if _, e = f.pool.Exec(ctx, "UPDATE app_user SET is_active=true WHERE id=$1", uuid.MustParse(f.actor.ID)); e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Configure(ctx, f.actor, f.sub, cmd, "switch-once"); e != nil {
		t.Fatal("restored receipt failed", e)
	}
	var audits, receipts int
	if e = f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM audit_log WHERE action='subscription.bindings_configured' AND target_id=$1),(SELECT count(*) FROM idempotency_record WHERE idempotency_key='switch-once')`, f.sub.String()).Scan(&audits, &receipts); e != nil || audits != 1 || receipts != 1 {
		t.Fatal("duplicate change", audits, receipts, e)
	}
}
