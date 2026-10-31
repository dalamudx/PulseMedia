package postgres_test

import (
	"encoding/json"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/domain/quality"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
)

func TestMovieDiscoveryCutoffPG(t *testing.T) {
	for _, mode := range []string{"stop_at_cutoff", "replace_if_better"} {
		t.Run(mode, func(t *testing.T) {
			f, s := movieDiscoveryFixture(t)
			ctx := t.Context()
			rules, _ := json.Marshal(quality.PolicyRules{CutoffScore: 340, ResolutionOrder: map[quality.Resolution]int{quality.Res1080p: 300, quality.Res2160p: 400}, SourceOrder: map[quality.Source]int{quality.SourceBluray: 40}})
			if _, e := db.New(f.p).InsertPolicyRevision(ctx, db.InsertPolicyRevisionParams{ID: uuid.New(), PolicyID: "frozen-upgrade", Revision: 2, Rules: rules}); e != nil {
				t.Fatal(e)
			}
			f.exec(t, `UPDATE policy SET current_revision=2 WHERE id='frozen-upgrade'`)
			f.exec(t, `UPDATE subscription SET quality_policy_revision=2,upgrade_mode=$2,intent_version=intent_version+1 WHERE id=$1`, f.sub, mode)
			wanted, e := db.New(f.p).ListWantedItemsForSubscription(ctx, f.sub)
			if e != nil {
				t.Fatal(e)
			}
			for _, w := range wanted {
				if _, e := subscription.NewEvaluator(postgres.NewWantedProjectionStore(f.p)).EvaluateWantedItem(ctx, w.ID, quality.Policy{ID: "frozen-upgrade", Revision: 2}); e != nil {
					t.Fatal(e)
				}
			}
			ok, e := s.ScheduleMovieDiscovery(ctx, f.sub)
			if e != nil {
				t.Fatal(e)
			}
			if want := mode == "replace_if_better"; ok != want {
				t.Fatalf("scheduled=%v want=%v", ok, want)
			}
		})
	}
}

func TestMovieDiscoveryMigrationPG(t *testing.T) {
	f, s := movieDiscoveryFixture(t)
	ctx := t.Context()
	dir := repositoryMigrationsThrough(t, 134)
	if v, e := postgres.Rollback(ctx, f.dsn, dir); e != nil || v != 133 {
		t.Fatalf("empty rollback=%d %v", v, e)
	}
	if v, e := postgres.Migrate(ctx, f.dsn, dir); e != nil || v != 134 {
		t.Fatalf("reapply=%d %v", v, e)
	}
	if ok, e := s.ScheduleMovieDiscovery(ctx, f.sub); e != nil || !ok {
		t.Fatalf("schedule %v %v", ok, e)
	}
	if _, e := postgres.Rollback(ctx, f.dsn, dir); e == nil {
		t.Fatal("downgrade erased search origin history")
	}
	var n int
	if e := f.p.QueryRow(ctx, `SELECT count(*) FROM subscription_movie_discovery_run`).Scan(&n); e != nil || n != 1 {
		t.Fatalf("history after refused downgrade %d %v", n, e)
	}
	for _, sql := range []string{`UPDATE subscription_movie_discovery_run SET input_stamp=repeat('a',64)`, `DELETE FROM subscription_movie_discovery_run`, `TRUNCATE subscription_movie_discovery_run CASCADE`} {
		if _, e := f.p.Exec(ctx, sql); e == nil {
			t.Fatalf("history allowed %s", sql)
		}
	}
}
