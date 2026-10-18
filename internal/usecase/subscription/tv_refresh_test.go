package subscription_test

import (
	"context"
	"errors"
	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type episodeRefreshProvider struct {
	calls   atomic.Int32
	before  func()
	empty   bool
	changed bool
	extra   bool
}

func (p *episodeRefreshProvider) GetTVSeasonMetadata(context.Context, int64, int, string) (*ports.TVSeasonMetadata, error) {
	p.calls.Add(1)
	if p.before != nil {
		p.before()
	}
	if p.empty {
		return &ports.TVSeasonMetadata{SeasonNumber: 1, Complete: true}, nil
	}
	future := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	first := int64(1001)
	if p.changed {
		first = 2001
	}
	eps := []ports.TVEpisodeMetadata{{TMDBEpisodeID: first, EpisodeNumber: 1}, {TMDBEpisodeID: 1003, EpisodeNumber: 3, AirDate: &future}}
	if p.extra {
		eps = append(eps, ports.TVEpisodeMetadata{TMDBEpisodeID: 1004, EpisodeNumber: 4})
	}
	return &ports.TVSeasonMetadata{SeasonNumber: 1, Complete: true, Episodes: eps}, nil
}
func TestTVMetadataRefreshExpandsCurrentScopesAndRejectsOldCallback(t *testing.T) {
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
	admin := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin}
	if _, e = q.CreateUser(ctx, db.CreateUserParams{ID: uuid.MustParse(admin.ID), Username: "refresh-admin", PasswordHash: "fixture", Role: ports.RoleAdmin, IsActive: true}); e != nil {
		t.Fatal(e)
	}
	root, m := uuid.New(), uuid.New()
	if _, e = q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: root, Path: t.TempDir(), MediaType: "tv", Name: "TV"}); e != nil {
		t.Fatal(e)
	}
	if _, e = q.CreateMedia(ctx, db.CreateMediaParams{ID: m, Title: "Refresh show", MediaType: "tv"}); e != nil {
		t.Fatal(e)
	}
	if _, e = q.CreateMediaIdentity(ctx, db.CreateMediaIdentityParams{ID: uuid.New(), MediaID: m, Namespace: "tmdb.tv", ExternalID: "100"}); e != nil {
		t.Fatal(e)
	}
	old := []uuid.UUID{uuid.New(), uuid.New()}
	for i, id := range old {
		if _, e = q.CreateTVUnit(ctx, db.CreateTVUnitParams{ID: id, MediaID: m, UnitKey: []string{"S01E01", "S01E02"}[i], SeasonNumber: pgtype.Int4{Int32: 1, Valid: true}, EpisodeNumber: pgtype.Int4{Int32: int32(i + 1), Valid: true}, AbsoluteNumber: pgtype.Int4{Int32: int32(i + 1), Valid: true}}); e != nil {
			t.Fatal(e)
		}
	}
	legacy := newIntentService(pool, ids)
	sub, _, e := legacy.CreateSubscription(ctx, admin, subscription.CreateSubscriptionCommand{MediaID: m, RootID: root, ScopeType: "all"})
	if e != nil {
		t.Fatal(e)
	}
	control := subscription.NewControlService(postgres.NewSubscriptionControlStore(pool, ids))
	r, e := control.Apply(ctx, admin, sub.ID, ports.SubscriptionControlCommand{Action: "state", ExpectedVersion: sub.IntentVersion, State: "paused"}, "pause")
	if e != nil {
		t.Fatal(e)
	}
	skip := true
	r, e = control.Apply(ctx, admin, sub.ID, ports.SubscriptionControlCommand{Action: "skip", ExpectedVersion: r.IntentVersion, UnitID: &old[0], Skipped: &skip}, "user-skip")
	if e != nil {
		t.Fatal(e)
	}
	provider := &episodeRefreshProvider{}
	svc := subscription.NewTVRefreshService(postgres.NewTVRefreshStore(pool, ids), provider)
	c := ports.TVRefreshCommand{ExpectedVersion: r.IntentVersion, Seasons: []int{1}}
	result, e := svc.Refresh(ctx, admin, sub.ID, c, "refresh")
	if e != nil || result.ObservedEpisodes != 2 {
		t.Fatal(result, e)
	}
	repeat, e := svc.Refresh(ctx, admin, sub.ID, c, "refresh")
	if e != nil || repeat.MediaVersion != result.MediaVersion || provider.calls.Load() != 1 {
		t.Fatal("replay repeated provider/publish", repeat, e)
	}
	units, e := q.LockScopeMediaUnits(ctx, m)
	if e != nil || len(units) != 3 {
		t.Fatal(units, e)
	}
	for _, u := range units {
		switch u.UnitKey {
		case "S01E01":
			if u.AirDate.Valid || u.AbsoluteNumber.Valid {
				t.Fatal("unknown metadata retained fabricated date/absolute", u)
			}
		case "S01E02":
			if !u.Retired {
				t.Fatal("complete season did not retire missing logical unit")
			}
		case "S01E03":
			if !u.AirDate.Valid || u.AirDate.Time.Year() != 2099 || u.AbsoluteNumber.Valid {
				t.Fatal("new future date/unknownabsolute", u)
			}
		}
	}
	wanted, e := q.ListWantedItemsForSubscription(ctx, sub.ID)
	if e != nil || len(wanted) != 3 {
		t.Fatal("new episodes not expanded", wanted, e)
	}
	for _, w := range wanted {
		if w.UnitID == old[0] && !w.UserSkipped {
			t.Fatal("refresh erased user skip")
		}
		if w.UnitID == old[1] && !w.ScopeExcluded {
			t.Fatal("retired target kept acquiring")
		}
	}
	saved, e := q.GetSubscriptionByID(ctx, sub.ID)
	if e != nil || saved.State != "paused" || saved.IntentVersion != r.IntentVersion {
		t.Fatal("metadata changed control intent", saved, e)
	}
	identity, e := q.GetTMDBEpisodeIdentityForUnit(ctx, old[0])
	if e != nil || identity.ExternalID != 1001 || identity.TmdbSeriesID != 100 {
		t.Fatal("stable unit source identity missing", identity, e)
	}
	var wg sync.WaitGroup
	replays := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := svc.Refresh(ctx, admin, sub.ID, c, "identity-concurrent")
			if e == nil && r.MediaVersion != result.MediaVersion+1 {
				e = errors.New("unstable concurrent identity receipt")
			}
			replays <- e
		}()
	}
	wg.Wait()
	close(replays)
	for e := range replays {
		if e != nil {
			t.Fatal(e)
		}
	}
	var identities int
	if e = pool.QueryRow(ctx, "SELECT count(*) FROM media_unit_identity").Scan(&identities); e != nil || identities != 2 {
		t.Fatal("identity duplicated/reassigned", identities, e)
	}
	provider.changed = true
	beforeConflict, e := q.GetMediaByID(ctx, m)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Refresh(ctx, admin, sub.ID, c, "changed-episode-id"); !errors.Is(e, ports.ErrSubscriptionBindingsConflict) {
		t.Fatal("canonical unit identity silently replaced", e)
	}
	provider.changed = false
	afterConflict, e := q.GetMediaByID(ctx, m)
	if e != nil || beforeConflict.MetadataVersion != afterConflict.MetadataVersion {
		t.Fatal("identity conflict partially applied metadata", e)
	}
	if _, e = pool.Exec(ctx, "UPDATE media_unit_identity SET external_id=9999 WHERE unit_id=$1", old[0]); e == nil {
		t.Fatal("immutable episode evidence overwritten")
	}
	if _, e = pool.Exec(ctx, "DELETE FROM media_unit_identity WHERE unit_id=$1", old[0]); e == nil {
		t.Fatal("episode evidence deleted")
	}

	// The final receipt fault may not publish a media revision, episode mutation,
	// target expansion, audit or event independently.
	before, e := q.GetMediaByID(ctx, m)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, "CREATE FUNCTION reject_tv_result() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.idempotency_key='tv-fault' THEN RAISE EXCEPTION 'fixture-secret';END IF;RETURN NEW;END $$;CREATE TRIGGER reject_tv_result BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION reject_tv_result()"); e != nil {
		t.Fatal(e)
	}
	provider.extra = true
	if _, e = svc.Refresh(ctx, admin, sub.ID, c, "tv-fault"); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal("metadata late receipt fault not rolled back", e)
	}
	provider.extra = false
	var leakedIdentity bool
	if e = pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM media_unit_identity WHERE external_id=1004)").Scan(&leakedIdentity); e != nil || leakedIdentity {
		t.Fatal("new identity survived receipt rollback", e)
	}
	after, e := q.GetMediaByID(ctx, m)
	if e != nil || after.MetadataVersion != before.MetadataVersion {
		t.Fatal("metadata revision escaped failed result transaction", after, e)
	}
	if _, e = pool.Exec(ctx, "DROP TRIGGER reject_tv_result ON idempotency_record"); e != nil {
		t.Fatal(e)
	}
	provider.before = func() {
		if _, e := pool.Exec(ctx, "UPDATE media SET metadata_version=metadata_version+1 WHERE id=$1", m); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = svc.Refresh(ctx, admin, sub.ID, c, "old-callback"); !errors.Is(e, ports.ErrSubscriptionBindingsConflict) {
		t.Fatal("old network callback overwrote new metadata", e)
	}
	provider.before = nil
	provider.empty = true
	if _, e = svc.Refresh(ctx, admin, sub.ID, c, "empty-season"); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal("partial/empty snapshot retired everything", e)
	}
	if _, e = svc.Refresh(ctx, &ports.Actor{Type: ports.ActorTypeUser, ID: admin.ID, Role: ports.RoleMember}, sub.ID, c, "member"); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("member changed global metadata", e)
	}
	// Current account authorization precedes successful receipt reads and network lookup.
	provider.empty = false
	snapshot := func() string {
		t.Helper()
		var state string
		if err := pool.QueryRow(ctx, `SELECT jsonb_build_array((SELECT metadata_version FROM media WHERE id=$1),(SELECT count(*) FROM media_unit),(SELECT count(*) FROM media_unit_identity),(SELECT count(*) FROM wanted_item),(SELECT count(*) FROM audit_log),(SELECT count(*) FROM outbox_event),(SELECT count(*) FROM idempotency_record))::text`, m).Scan(&state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	beforePermission := snapshot()
	callsBefore := provider.calls.Load()
	for _, update := range []string{"is_active=false", "role='member'"} {
		if _, err := pool.Exec(ctx, "UPDATE app_user SET "+update+" WHERE id=$1", admin.ID); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"refresh", "denied-new"} {
			if _, err := svc.Refresh(ctx, admin, sub.ID, c, key); !errors.Is(err, ports.ErrForbidden) {
				t.Fatal("stale admin authority", update, key, err)
			}
		}
		if _, err := pool.Exec(ctx, "UPDATE app_user SET is_active=true,role='admin' WHERE id=$1", admin.ID); err != nil {
			t.Fatal(err)
		}
	}
	for _, actor := range []*ports.Actor{{Type: ports.ActorTypeUser, ID: admin.ID, Role: ports.RoleAdmin, Scopes: []string{"read"}}, {Type: ports.ActorTypeSystem, ID: admin.ID, Role: ports.RoleAdmin}, {Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin}} {
		if _, err := svc.Refresh(ctx, actor, sub.ID, c, "refresh"); !errors.Is(err, ports.ErrForbidden) {
			t.Fatal("invalid actor borrowed refresh receipt", err)
		}
	}
	if snapshot() != beforePermission || provider.calls.Load() != callsBefore {
		t.Fatal("denied refresh wrote or called provider")
	}
	provider.extra = true
	provider.before = func() {
		if _, err := pool.Exec(ctx, "UPDATE app_user SET is_active=false WHERE id=$1", admin.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.Refresh(ctx, admin, sub.ID, c, "permission-after-network"); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("network response escaped current user gate", err)
	}
	if snapshot() != beforePermission {
		t.Fatal("denied network result published metadata/targets/history")
	}
	provider.before = nil
	if _, err := pool.Exec(ctx, "UPDATE app_user SET is_active=true WHERE id=$1", admin.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Refresh(ctx, admin, sub.ID, c, "permission-after-network"); err != nil {
		t.Fatal("restored admin retry", err)
	}
	if _, err := svc.Refresh(ctx, admin, sub.ID, c, "refresh"); err != nil {
		t.Fatal("restored account historical receipt", err)
	}
}
