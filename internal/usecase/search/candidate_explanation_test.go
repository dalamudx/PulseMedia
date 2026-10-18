package search_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/domain/media"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/usecase/search"
	"github.com/google/uuid"
	"testing"
)

func TestCandidateExplanationCacheNeverAuthorizesStaleAvailability(t *testing.T) {
	f := runtimeFixture(t)
	ctx := context.Background()
	round := f.collect(t)
	svc := search.NewCandidateExplanationService(postgres.NewCandidateExplanationStore(f.pool))
	first, e := svc.Read(ctx, f.actor, f.sub, f.wanted)
	if e != nil || first.RoundID != round.ID || len(first.Items) != 2 || first.Items[0].Decision != "accept" || first.Items[0].Cached || first.Items[1].Decision != "hold" || first.Items[1].Reason != "match_unproven" {
		t.Fatal(first, e)
	}
	if _, e = f.pool.Exec(ctx, "UPDATE search_round SET deadline=clock_timestamp()-interval '1 hour' WHERE id=$1", round.ID); e != nil {
		t.Fatal(e)
	}
	late, e := svc.Read(ctx, f.actor, f.sub, f.wanted)
	if e != nil || late.Items[0].Available || late.Items[0].WaitReason != "source_changed" {
		t.Fatal("after-deadline source authorized", late, e)
	}
	if _, e = f.pool.Exec(ctx, "UPDATE search_round SET deadline=clock_timestamp()+interval '1 minute' WHERE id=$1", round.ID); e != nil {
		t.Fatal(e)
	}

	second, e := svc.Read(ctx, f.actor, f.sub, f.wanted)
	if e != nil || !second.Items[0].Cached {
		t.Fatal("pure result not reused", second, e)
	}
	if _, e = f.pool.Exec(ctx, "UPDATE library_root SET health='offline' WHERE id=$1", f.root); e != nil {
		t.Fatal(e)
	}
	offline, e := svc.Read(ctx, f.actor, f.sub, f.wanted)
	if e != nil || !offline.Items[0].Cached || offline.Items[0].Available || offline.Items[0].InputDigest != first.Items[0].InputDigest || offline.Items[0].WaitReason != "root_offline" {
		t.Fatal("pure cache retained online availability", offline, e)
	}
	if _, e = svc.Read(ctx, &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember}, f.sub, f.wanted); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("foreign object accessed cache", e)
	}
	if _, e = f.pool.Exec(ctx, "UPDATE indexer SET config_version=config_version+1 WHERE id=$1", f.idx); e != nil {
		t.Fatal(e)
	}
	stale, e := svc.Read(ctx, f.actor, f.sub, f.wanted)
	if e != nil || stale.Items[0].Available || stale.Items[0].WaitReason != "source_changed" {
		t.Fatal("source revision reused cached availability", stale, e)
	}
	if _, e = f.pool.Exec(ctx, "UPDATE release_candidate SET title='https://site/?passkey=fixture-secret' WHERE id=$1", first.Items[0].ID); e != nil {
		t.Fatal(e)
	}
	safe, e := svc.Read(ctx, f.actor, f.sub, f.wanted)
	if e != nil || safe.Items[0].Title == "https://site/?passkey=fixture-secret" || safe.Items[0].Decision != "hold" || safe.Items[0].Reason != "unsafe_title" || safe.Items[0].Cached {
		t.Fatal("unsafe changed input reused old accept", safe, e)
	}
	var leaked int
	if e = f.pool.QueryRow(ctx, "SELECT count(*) FROM candidate_rule_cache WHERE decision::text LIKE '%fixture-secret%'").Scan(&leaked); e != nil || leaked != 0 {
		t.Fatal("raw title stored in decision cache", leaked, e)
	}
	_ = media.CandidateDecisionVersion
}
