package search_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	search "github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"sync"
	"testing"
)

func TestSubscriptionReleaseBlockAtomicScopeAndFinalization(t *testing.T) {
	f := runtimeFixture(t)
	ctx := context.Background()
	old := f.collect(t)
	rows, e := f.q.ListRoundQueryCandidates(ctx, old.ID)
	if e != nil {
		t.Fatal(e)
	}
	var candidate uuid.UUID
	for _, r := range rows {
		if r.Title == "Fight.Club.1999.1080p.BluRay" {
			candidate = r.ID
		}
	}
	if candidate == uuid.Nil {
		t.Fatal("no valid candidate")
	}
	svc := subscription.NewControlService(postgres.NewSubscriptionControlStore(f.pool, idgen.New()))
	explain := search.NewCandidateExplanationService(postgres.NewCandidateExplanationStore(f.pool))
	before, e := explain.Read(ctx, f.actor, f.sub, f.wanted)
	if e != nil {
		t.Fatal(e)
	}
	sub, e := f.q.GetSubscriptionByID(ctx, f.sub)
	if e != nil {
		t.Fatal(e)
	}
	yes := true
	command := ports.SubscriptionControlCommand{Action: "release_block", ExpectedVersion: sub.IntentVersion, CandidateID: &candidate, Blocked: &yes}
	foreign := command
	id := uuid.New()
	foreign.CandidateID = &id
	if _, e = svc.Apply(ctx, f.actor, f.sub, foreign, "foreign-block"); !errors.Is(e, ports.ErrNotFound) {
		t.Fatal("foreign candidate evidence", e)
	}
	if _, e = f.pool.Exec(ctx, `CREATE FUNCTION reject_release_block_tail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture tail fault'; END $$`); e != nil {
		t.Fatal(e)
	}
	for _, table := range []string{"audit_log", "outbox_event", "idempotency_record"} {
		if _, e = f.pool.Exec(ctx, "CREATE TRIGGER reject_block_tail BEFORE INSERT ON "+table+" FOR EACH ROW EXECUTE FUNCTION reject_release_block_tail()"); e != nil {
			t.Fatal(e)
		}
		if _, e = svc.Apply(ctx, f.actor, f.sub, command, "block-once"); !errors.Is(e, ports.ErrUnavailable) {
			t.Fatal(table, e)
		}
		var count int
		if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM release_block").Scan(&count); e != nil || count != 0 {
			t.Fatal("partial block", table, count, e)
		}
		saved, e := f.q.GetSubscriptionByID(ctx, f.sub)
		if e != nil || saved.IntentVersion != sub.IntentVersion {
			t.Fatal("partial intent", table, e)
		}
		if _, e = f.pool.Exec(ctx, "DROP TRIGGER reject_block_tail ON "+table); e != nil {
			t.Fatal(e)
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := svc.Apply(ctx, f.actor, f.sub, command, "block-once")
			if e == nil && (r.IntentVersion != sub.IntentVersion+1 || r.Blocked == nil || !*r.Blocked || r.CandidateID == nil || *r.CandidateID != candidate) {
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
	for _, query := range []string{"SELECT count(*) FROM release_block", "SELECT count(*) FROM audit_log WHERE action='subscription.control_release_block'", "SELECT count(*) FROM idempotency_record WHERE idempotency_key='block-once'", "SELECT count(*) FROM outbox_event WHERE event_type='subscription.control_changed'"} {
		var n int
		if e = f.pool.QueryRow(ctx, query).Scan(&n); e != nil || n != 1 {
			t.Fatal("not atomic unique", query, n, e)
		}
	}
	blocked, e := explain.Read(ctx, f.actor, f.sub, f.wanted)
	if e != nil {
		t.Fatal(e)
	}
	var prior, now ports.CandidateExplanationItem
	for _, r := range before.Items {
		if r.ID == candidate {
			prior = r
		}
	}
	for _, r := range blocked.Items {
		if r.ID == candidate {
			now = r
		}
	}
	if !now.Blocked || now.Available || now.WaitReason != "release_blocked" || now.InputDigest != prior.InputDigest || now.Decision != prior.Decision || !now.Cached {
		t.Fatal("block contaminated cache/availability", now)
	}
	job := f.finalJob(t, old)
	result, e := f.coord.FinalizeLeasedRound(ctx, old.ID, authority(job))
	if e == nil || result == nil || result.State != "aborted" {
		t.Fatal("oldround authorized", result, e)
	}
	fresh := f.collect(t)
	job = f.finalJob(t, fresh)
	result, e = f.coord.FinalizeLeasedRound(ctx, fresh.ID, authority(job))
	if e != nil || result.Reason != "no_qualified_candidate" || result.OperationID != uuid.Nil {
		t.Fatal("blocked candidate selected", result, e)
	}
	owner, e := f.q.GetUserByID(ctx, uuid.MustParse(f.actor.ID))
	if e != nil {
		t.Fatal(e)
	}
	user := uuid.New()
	if _, e = f.q.CreateUser(ctx, db.CreateUserParams{ID: user, Username: "other-block-owner", PasswordHash: "fixture", Role: ports.RoleMember, IsActive: true}); e != nil {
		t.Fatal(e)
	}
	other := &ports.Actor{Type: ports.ActorTypeUser, ID: user.String(), Role: ports.RoleMember}
	if _, e = svc.Apply(ctx, other, f.sub, command, "block-once"); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("receipt scope borrowed", e)
	}
	_ = owner
	no := false
	command.Blocked = &no
	command.ExpectedVersion++
	if _, e = svc.Apply(ctx, f.actor, f.sub, command, "unblock-once"); e != nil {
		t.Fatal(e)
	}
	fresh = f.collect(t)
	job = f.finalJob(t, fresh)
	result, e = f.coord.FinalizeLeasedRound(ctx, fresh.ID, authority(job))
	if e != nil || result.OperationID == uuid.Nil || result.SelectedCandidateID == nil || *result.SelectedCandidateID != candidate {
		t.Fatal("unblock failed original candidate", result, e)
	}
}
