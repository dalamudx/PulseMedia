package subscription_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"testing"
)

func TestScopeControlPreservesUserSkipHistoryAndPause(t *testing.T) {
	ctx := context.Background()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if _, e := postgres.Migrate(ctx, dsn, "../../../migrations"); e != nil {
		t.Fatal(e)
	}
	pool, e := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	q := db.New(pool)
	ids := idgen.New()
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember}
	user := uuid.MustParse(actor.ID)
	if _, e = q.CreateUser(ctx, db.CreateUserParams{ID: user, Username: "scope-owner", PasswordHash: "fixture", Role: ports.RoleMember, IsActive: true}); e != nil {
		t.Fatal(e)
	}
	root, m := uuid.New(), uuid.New()
	if _, e = q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: root, Path: t.TempDir(), MediaType: "tv", Name: "TV"}); e != nil {
		t.Fatal(e)
	}
	if _, e = q.CreateMedia(ctx, db.CreateMediaParams{ID: m, Title: "Scope show", MediaType: "tv"}); e != nil {
		t.Fatal(e)
	}
	units := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	for i, id := range units {
		season, episode := int32(1), int32(i+1)
		if i == 2 {
			season, episode = 0, 1
		}
		if _, e = q.CreateTVUnit(ctx, db.CreateTVUnitParams{ID: id, MediaID: m, UnitKey: []string{"S01E01", "S01E02", "S00E01"}[i], SeasonNumber: pgtype.Int4{Int32: season, Valid: true}, EpisodeNumber: pgtype.Int4{Int32: episode, Valid: true}}); e != nil {
			t.Fatal(e)
		}
	}
	legacy := newIntentService(pool, ids)
	sub, wanted, e := legacy.CreateSubscription(ctx, actor, subscription.CreateSubscriptionCommand{MediaID: m, RootID: root, ScopeType: "all"})
	if e != nil || len(wanted) != 2 {
		t.Fatal(e)
	}
	control := subscription.NewControlService(postgres.NewSubscriptionControlStore(pool, ids))
	version := sub.IntentVersion
	apply := func(c ports.SubscriptionControlCommand, key string) {
		t.Helper()
		c.ExpectedVersion = version
		r, e := control.Apply(ctx, actor, sub.ID, c, key)
		if e != nil {
			t.Fatal(e)
		}
		version = r.IntentVersion
	}
	apply(ports.SubscriptionControlCommand{Action: "state", State: "paused"}, "pause")
	skip := true
	apply(ports.SubscriptionControlCommand{Action: "skip", UnitID: &units[0], Skipped: &skip}, "skip")
	selected, all := "selected", "all"
	seasons := []int{1}
	noSpecials := false
	only := []uuid.UUID{units[0]}
	apply(ports.SubscriptionControlCommand{Action: "scope", ScopeType: &selected, Seasons: &seasons, IncludeSpecials: &noSpecials, SelectedUnitIDs: &only}, "narrow")
	rows, e := q.ListWantedItemsForSubscription(ctx, sub.ID)
	if e != nil || len(rows) != 3 {
		t.Fatal("history expansion", rows, e)
	}
	for _, row := range rows {
		if row.UnitID == units[0] && (!row.UserSkipped || row.ScopeExcluded || !row.IsSkipped) {
			t.Fatal("scope erased explicit user skip", row)
		}
		if row.UnitID == units[1] && (!row.ScopeExcluded || row.UserSkipped || !row.IsSkipped) {
			t.Fatal("scope exclusion mislabeled user skip", row)
		}
	}
	empty := []uuid.UUID{}
	apply(ports.SubscriptionControlCommand{Action: "scope", ScopeType: &all, Seasons: &seasons, IncludeSpecials: &noSpecials, SelectedUnitIDs: &empty}, "expand")
	rows, e = q.ListWantedItemsForSubscription(ctx, sub.ID)
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range rows {
		if r.UnitID == units[0] && !r.IsSkipped {
			t.Fatal("all scope removed explicit skip")
		}
		if r.UnitID == units[1] && r.IsSkipped {
			t.Fatal("range reexpand retained automatic exclusion")
		}
	}
	saved, e := q.GetSubscriptionByID(ctx, sub.ID)
	if e != nil || saved.State != "paused" {
		t.Fatal("scope resumed state", saved, e)
	}
	before := version
	if _, e = pool.Exec(ctx, "CREATE FUNCTION reject_scope_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.idempotency_key='scope-fault' THEN RAISE EXCEPTION 'fixture';END IF;RETURN NEW;END $$;CREATE TRIGGER reject_scope_receipt BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION reject_scope_receipt()"); e != nil {
		t.Fatal(e)
	}
	specials := true
	if _, e = control.Apply(ctx, actor, sub.ID, ports.SubscriptionControlCommand{Action: "scope", ExpectedVersion: version, ScopeType: &all, Seasons: &seasons, IncludeSpecials: &specials, SelectedUnitIDs: &empty}, "scope-fault"); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal("receipt fault not sanitized", e)
	}
	saved, e = q.GetSubscriptionByID(ctx, sub.ID)
	if e != nil || saved.IntentVersion != before || saved.IncludeSpecials {
		t.Fatal("failed scope partial commit", saved, e)
	}
	var selectedSaved []uuid.UUID
	if json.Unmarshal(saved.SelectedUnitIds, &selectedSaved) != nil || len(selectedSaved) != 0 {
		t.Fatal("selected unit document corrupted")
	}
	if _, e = pool.Exec(ctx, "DROP TRIGGER reject_scope_receipt ON idempotency_record"); e != nil {
		t.Fatal(e)
	}
	specialSeasons := []int{0, 1}
	apply(ports.SubscriptionControlCommand{Action: "scope", ScopeType: &all, Seasons: &specialSeasons, IncludeSpecials: &specials, SelectedUnitIDs: &empty}, "include-specials")
	rows, e = q.ListWantedItemsForSubscription(ctx, sub.ID)
	if e != nil {
		t.Fatal(e)
	}
	foundSpecial := false
	for _, row := range rows {
		if row.UnitID == units[2] {
			foundSpecial = true
			if row.ScopeExcluded || row.UserSkipped || row.IsSkipped {
				t.Fatal("explicit specials excluded", row)
			}
		}
	}
	if !foundSpecial {
		t.Fatal("explicit special missing")
	}
	saved, e = q.GetSubscriptionByID(ctx, sub.ID)
	if e != nil || saved.IntentVersion != before+1 || !saved.IncludeSpecials || saved.State != "paused" {
		t.Fatal("special scope version/control", saved, e)
	}

}
