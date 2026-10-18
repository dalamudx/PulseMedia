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
	"sync"
	"sync/atomic"
	"testing"
)

type mappingGroupProvider struct {
	calls   atomic.Int32
	changed bool
	before  func()
}

func (p *mappingGroupProvider) ListTVEpisodeGroups(context.Context, int64) ([]ports.TVEpisodeGroupSummary, error) {
	return []ports.TVEpisodeGroupSummary{}, nil
}
func (p *mappingGroupProvider) GetTVEpisodeGroup(context.Context, int64, string) (*ports.TVEpisodeGroupSnapshot, error) {
	p.calls.Add(1)
	if p.before != nil {
		p.before()
	}
	kind := 2
	if p.changed {
		kind = 3
	}
	return &ports.TVEpisodeGroupSnapshot{SeriesID: 100, Summary: ports.TVEpisodeGroupSummary{ID: "group1", Type: kind, Name: "Fixture", EpisodeCount: 2, GroupCount: 1}, Parts: []ports.TVEpisodeGroupPart{{ID: "part1", Order: 0, Episodes: []ports.TVEpisodeGroupEpisode{{ID: 1002, Season: 2, Episode: 1, Order: 1}, {ID: 1001, Season: 1, Episode: 1, Order: 0}}}}}, nil
}
func TestEpisodeMappingPublicationAtomicReplayAndFrozenUnits(t *testing.T) {
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
	member := &ports.Actor{Type: ports.ActorTypeUser, ID: admin.ID, Role: ports.RoleMember}
	if _, e = q.CreateUser(ctx, db.CreateUserParams{ID: uuid.MustParse(admin.ID), Username: "mapping-admin", PasswordHash: "fixture", Role: ports.RoleAdmin, IsActive: true}); e != nil {
		t.Fatal(e)
	}
	root, m := uuid.New(), uuid.New()
	if _, e = q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: root, Path: t.TempDir(), MediaType: "tv", Name: "Mapping root"}); e != nil {
		t.Fatal(e)
	}
	if _, e = q.CreateMedia(ctx, db.CreateMediaParams{ID: m, Title: "Mapping Show", MediaType: "tv"}); e != nil {
		t.Fatal(e)
	}
	if _, e = q.CreateMediaIdentity(ctx, db.CreateMediaIdentityParams{ID: uuid.New(), MediaID: m, Namespace: "tmdb.tv", ExternalID: "100"}); e != nil {
		t.Fatal(e)
	}
	units := []uuid.UUID{uuid.New(), uuid.New()}
	for i, id := range units {
		if _, e = q.CreateTVUnit(ctx, db.CreateTVUnitParams{ID: id, MediaID: m, UnitKey: []string{"S01E01", "S02E01"}[i], SeasonNumber: pgtype.Int4{Int32: int32(i + 1), Valid: true}, EpisodeNumber: pgtype.Int4{Int32: 1, Valid: true}}); e != nil {
			t.Fatal(e)
		}
		if _, e = q.BindTMDBEpisodeIdentity(ctx, db.BindTMDBEpisodeIdentityParams{UnitID: id, ExternalID: int64(1001 + i), TmdbSeriesID: 100}); e != nil {
			t.Fatal(e)
		}
	}
	sub, _, e := newIntentService(pool, ids).CreateSubscription(ctx, admin, subscription.CreateSubscriptionCommand{MediaID: m, RootID: root, ScopeType: "all"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, "UPDATE subscription SET state='paused',intent_version=intent_version+1 WHERE id=$1", sub.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, "UPDATE wanted_item SET user_skipped=true,is_skipped=true WHERE unit_id=$1", units[0]); e != nil {
		t.Fatal(e)
	}
	sourceRepo := postgres.NewEpisodeSourceStore(postgres.NewTVRefreshStore(pool, ids))
	provider := &mappingGroupProvider{}
	svc := subscription.NewEpisodeMappingService(postgres.NewEpisodeMappingStore(sourceRepo, ids), subscription.NewEpisodeSourceService(sourceRepo, provider))
	draft := ports.EpisodeMappingDraft{Mode: "manual", Entries: []ports.ManualEpisodeMappingEntry{{SourceNumber: 27, UnitID: units[1]}, {SourceNumber: 25, UnitID: units[0]}}}
	preview, e := svc.Preview(ctx, admin, sub.ID, draft)
	if e != nil || !preview.CanPublish || len(preview.Rows) != 2 || preview.Rows[0].SourceNumber != 25 || preview.Rows[1].SourceNumber != 27 || provider.calls.Load() != 0 {
		t.Fatal(preview, e)
	}
	cmd := ports.EpisodeMappingCommand{Draft: draft, ExpectedInputStamp: preview.InputStamp, ExpectedSourceHash: preview.SourceHash, Confirmed: true}
	if _, e = svc.Publish(ctx, member, sub.ID, cmd, "map-once"); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("member elevated", e)
	}
	if _, e = pool.Exec(ctx, "CREATE FUNCTION fail_episode_receipt() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.idempotency_key='map-once' THEN RAISE EXCEPTION 'fixture-secret';END IF;RETURN NEW;END$$;CREATE TRIGGER fail_episode_receipt BEFORE INSERT ON idempotency_record FOR EACH ROW EXECUTE FUNCTION fail_episode_receipt()"); e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Publish(ctx, admin, sub.ID, cmd, "map-once"); !errors.Is(e, ports.ErrUnavailable) {
		t.Fatal("last receipt did not fail", e)
	}
	var count int
	if e = pool.QueryRow(ctx, "SELECT count(*) FROM episode_mapping_revision").Scan(&count); e != nil || count != 0 {
		t.Fatal("partial revision", count, e)
	}
	saved, e := q.GetSubscriptionByID(ctx, sub.ID)
	if e != nil || saved.EpisodeMappingRevision.Valid || saved.IntentVersion != sub.IntentVersion+1 {
		t.Fatal("partial binding/intent", saved, e)
	}
	if _, e = pool.Exec(ctx, "DROP TRIGGER fail_episode_receipt ON idempotency_record"); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	responses := make(chan string, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := svc.Publish(ctx, admin, sub.ID, cmd, "map-once")
			if e != nil {
				errs <- e
				return
			}
			b, _ := json.Marshal(r)
			responses <- string(b)
		}()
	}
	wg.Wait()
	close(errs)
	close(responses)
	for e := range errs {
		t.Fatal(e)
	}
	receipt := ""
	for r := range responses {
		if receipt == "" {
			receipt = r
		} else if r != receipt {
			t.Fatal("unstable replay")
		}
	}
	for _, inactive := range []bool{true, false} {
		if _, e = pool.Exec(ctx, "UPDATE app_user SET is_active=$2,role=$3 WHERE id=$1", admin.ID, !inactive, map[bool]string{true: ports.RoleAdmin, false: ports.RoleMember}[inactive]); e != nil {
			t.Fatal(e)
		}
		calls := provider.calls.Load()
		if _, e = svc.Read(ctx, admin, sub.ID); !errors.Is(e, ports.ErrForbidden) {
			t.Fatal("stale administrator read", e)
		}
		if _, e = svc.Preview(ctx, admin, sub.ID, ports.EpisodeMappingDraft{Mode: "absolute", GroupID: "group1", PartID: "part1"}); !errors.Is(e, ports.ErrForbidden) {
			t.Fatal("stale administrator preview", e)
		}
		if _, e = svc.Publish(ctx, admin, sub.ID, cmd, "map-once"); !errors.Is(e, ports.ErrForbidden) {
			t.Fatal("stale administrator receipt replay", e)
		}
		if _, e = postgres.NewEpisodeMappingStore(sourceRepo, ids).PublishEpisodeMapping(ctx, admin, *preview, "direct-invalid-user", "fixture"); !errors.Is(e, ports.ErrForbidden) {
			t.Fatal("direct publish bypassed user", e)
		}
		if provider.calls.Load() != calls {
			t.Fatal("invalid user reached provider")
		}
	}
	if _, e = pool.Exec(ctx, "UPDATE app_user SET is_active=true,role='admin' WHERE id=$1", admin.ID); e != nil {
		t.Fatal(e)
	}
	recovered, e := svc.Publish(ctx, admin, sub.ID, cmd, "map-once")
	if e != nil {
		t.Fatal(e)
	}
	rawRecovered, _ := json.Marshal(recovered)
	if string(rawRecovered) != receipt {
		t.Fatal("reactivation changed historical receipt")
	}
	for _, bad := range []*ports.Actor{{Type: ports.ActorTypeUser, ID: admin.ID, Role: ports.RoleAdmin, Scopes: []string{"read"}}, {Type: ports.ActorTypeSystem, ID: admin.ID, Role: ports.RoleAdmin}, {Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleAdmin}} {
		if _, e = svc.Publish(ctx, bad, sub.ID, cmd, "map-once"); !errors.Is(e, ports.ErrForbidden) {
			t.Fatal("invalid credential replay", e)
		}
	}
	saved, e = q.GetSubscriptionByID(ctx, sub.ID)
	if e != nil || saved.State != "paused" || saved.IntentVersion != sub.IntentVersion+2 || saved.EpisodeMappingRevision.Int64 != 1 {
		t.Fatal("binding/control changed wrongly", saved, e)
	}
	wanted, e := q.ListWantedItemsForSubscription(ctx, sub.ID)
	if e != nil {
		t.Fatal(e)
	}
	for _, w := range wanted {
		if w.IntentVersion != saved.IntentVersion || (w.UnitID == units[0] && !w.UserSkipped) {
			t.Fatal("lost wanted provenance", w)
		}
	}
	state, e := svc.Read(ctx, admin, sub.ID)
	if e != nil || !state.Current || state.MappingRevision != 1 || len(state.Rows) != 2 {
		t.Fatal(state, e)
	}
	for _, c := range []struct {
		name, reason string
		known        bool
		target       uuid.UUID
	}{{"[Team] Fixture - 25 [1080p].mkv", "mapped_coverage_only", true, units[0]}, {"[Team] Fixture - 27 [1080p].mkv", "mapped_coverage_only", true, units[1]}, {"[Team] Fixture - 26 [1080p].mkv", "absolute_number_unmapped", false, uuid.Nil}, {"[Team] Fixture - 25 [1080p] sample.mkv", "partial_episode_content", false, uuid.Nil}, {"[Team] Fixture - 25 [1080p] S02E01.mkv", "conflicting_episode_coverage", false, uuid.Nil}} {
		explained, err := svc.ExplainRelease(ctx, admin, sub.ID, c.name)
		if err != nil || explained.CoverageKnown != c.known || explained.ExecutionAllowed || explained.Reason != c.reason || explained.InputStamp != state.InputStamp || explained.MediaID != m || explained.MappingRevision != 1 {
			t.Fatal("unsafe mapped explanation", c, explained, err)
		}
		if c.known && (len(explained.Rows) != 1 || explained.Rows[0].UnitID != c.target) {
			t.Fatal("coverage expanded", explained)
		}
	}
	if _, err := svc.ExplainRelease(ctx, member, sub.ID, "[Team] Fixture - 25 [1080p].mkv"); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("member mapped authority", err)
	}
	if _, err := svc.ExplainRelease(ctx, admin, sub.ID, "https://example.test/?passkey=private"); !errors.Is(err, ports.ErrInvalidEpisodeMapping) {
		t.Fatal("secret accepted", err)
	}

	if _, e = svc.Publish(ctx, admin, sub.ID, cmd, "old-preview"); !errors.Is(e, ports.ErrEpisodeSourceConflict) {
		t.Fatal("old preview bypassed intent fence", e)
	}
	if _, e = pool.Exec(ctx, "UPDATE episode_mapping_revision SET source_hash=repeat('a',64)"); e == nil {
		t.Fatal("immutable source overwritten")
	}
	if _, e = pool.Exec(ctx, "DELETE FROM episode_mapping_revision"); e == nil {
		t.Fatal("history deleted")
	}
	// Empty later schemas may be removed; test the actual mapping-history guard.
	for version := postgres.CurrentMigrationVersion; version > 37; version-- {
		if _, e = postgres.Rollback(ctx, dsn, "../../../migrations"); e != nil {
			t.Fatal("empty newer schema cannot be downgraded", version, e)
		}
	}
	if e = postgres.CheckSchemaVersion(ctx, pool, 37); e != nil {
		t.Fatal(e)
	}
	if _, e = postgres.Rollback(ctx, dsn, "../../../migrations"); e == nil {
		t.Fatal("protected mapping history discarded")
	}
	if _, e = postgres.Migrate(ctx, dsn, "../../../migrations"); e != nil {
		t.Fatal("restore current schema", e)
	}
	if _, e = pool.Exec(ctx, "UPDATE media_unit SET retired=true WHERE id=$1", units[1]); e != nil {
		t.Fatal(e)
	}
	state, e = svc.Read(ctx, admin, sub.ID)
	if e != nil || state.Current || state.HoldReason != "canonical_units_changed" {
		t.Fatal("old valid shown after retirement", state, e)
	}
	explained, err := svc.ExplainRelease(ctx, admin, sub.ID, "[Team] Fixture - 25 [1080p].mkv")
	if err != nil || explained.CoverageKnown || explained.ExecutionAllowed || len(explained.Rows) != 0 || explained.Reason != "canonical_units_changed" {
		t.Fatal("retired mapping still resolves", explained, err)
	}
	if _, e = svc.Publish(ctx, member, sub.ID, cmd, "map-once"); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("cached role bypass", e)
	}
	if _, e = svc.Publish(ctx, admin, sub.ID, cmd, "map-once"); e != nil {
		t.Fatal("receipt replaced by live validity", e)
	}
	standard := ports.EpisodeMappingDraft{Mode: "standard"}
	p, e := svc.Preview(ctx, admin, sub.ID, standard)
	if e != nil {
		t.Fatal(e)
	}
	r, e := svc.Publish(ctx, admin, sub.ID, ports.EpisodeMappingCommand{Draft: standard, ExpectedInputStamp: p.InputStamp, ExpectedSourceHash: p.SourceHash, Confirmed: true}, "clear-mapping")
	if e != nil || r.MappingRevision != 2 {
		t.Fatal(r, e)
	}
	if _, e = pool.Exec(ctx, "UPDATE media_unit SET retired=false WHERE id=$1", units[1]); e != nil {
		t.Fatal(e)
	}
	absolute := ports.EpisodeMappingDraft{Mode: "absolute", GroupID: "group1"}
	p, e = svc.Preview(ctx, admin, sub.ID, absolute)
	if e != nil || !p.CanPublish {
		t.Fatal(p, e)
	}
	provider.changed = true
	if _, e = svc.Publish(ctx, admin, sub.ID, ports.EpisodeMappingCommand{Draft: absolute, ExpectedInputStamp: p.InputStamp, ExpectedSourceHash: p.SourceHash, Confirmed: true}, "changed-source"); !errors.Is(e, ports.ErrInvalidEpisodeMapping) {
		t.Fatal("changed numbering source accepted", e)
	}
	provider.changed = false
	r, e = svc.Publish(ctx, admin, sub.ID, ports.EpisodeMappingCommand{Draft: absolute, ExpectedInputStamp: p.InputStamp, ExpectedSourceHash: p.SourceHash, Confirmed: true}, "absolute-source")
	if e != nil || r.MappingRevision != 3 || r.ValidUntil == nil {
		t.Fatal(r, e)
	}
	beforeCalls := provider.calls.Load()
	if _, e = svc.Publish(ctx, admin, sub.ID, ports.EpisodeMappingCommand{Draft: absolute, ExpectedInputStamp: p.InputStamp, ExpectedSourceHash: p.SourceHash, Confirmed: true}, "absolute-source"); e != nil || provider.calls.Load() != beforeCalls {
		t.Fatal("replay fetched provider", e)
	}
	currentPreview, e := svc.Preview(ctx, admin, sub.ID, absolute)
	if e != nil || !currentPreview.CanPublish {
		t.Fatal(currentPreview, e)
	}
	var beforeNetwork string
	snapshot := func() string {
		t.Helper()
		var raw string
		if e := pool.QueryRow(ctx, "SELECT jsonb_build_array((SELECT count(*) FROM episode_mapping_revision),(SELECT intent_version FROM subscription WHERE id=$1),(SELECT count(*) FROM audit_log WHERE action='episode_mapping.published'),(SELECT count(*) FROM outbox_event WHERE event_type='episode_mapping.published'),(SELECT count(*) FROM idempotency_record))::text", sub.ID).Scan(&raw); e != nil {
			t.Fatal(e)
		}
		return raw
	}
	beforeNetwork = snapshot()
	provider.before = func() {
		if _, e := pool.Exec(ctx, "UPDATE app_user SET is_active=false WHERE id=$1", admin.ID); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = svc.Publish(ctx, admin, sub.ID, ports.EpisodeMappingCommand{Draft: absolute, ExpectedInputStamp: currentPreview.InputStamp, ExpectedSourceHash: currentPreview.SourceHash, Confirmed: true}, "network-invalidated"); !errors.Is(e, ports.ErrForbidden) {
		t.Fatal("network result bypassed current permission", e)
	}
	provider.before = nil
	if snapshot() != beforeNetwork {
		t.Fatal("invalidated network result wrote mapping/intent/receipt")
	}
	if _, e = pool.Exec(ctx, "UPDATE app_user SET is_active=true WHERE id=$1", admin.ID); e != nil {
		t.Fatal(e)
	}
	var jobs int
	if e = pool.QueryRow(ctx, "SELECT count(*) FROM job WHERE kind='download_submission'").Scan(&jobs); e != nil || jobs != 0 {
		t.Fatal("mapping publication initiated download", jobs, e)
	}
}
