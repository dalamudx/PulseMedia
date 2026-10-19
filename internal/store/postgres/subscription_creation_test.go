package postgres_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/pgtest"
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type creationFixture struct {
	pool      *pgxpool.Pool
	store     *postgres.SubscriptionCreationStore
	actor     *ports.Actor
	movie, tv uuid.UUID
	path      string
}

func newCreationFixture(t *testing.T, through ...int64) creationFixture {
	t.Helper()
	ctx := context.Background()
	dsn := pgtest.GetCluster(t).NewTestDB(t)
	if _, err := postgres.Migrate(ctx, dsn, repositoryMigrationsThrough(t, through...)); err != nil {
		t.Fatal(err)
	}
	pool, err := postgres.NewPool(ctx, dsn, postgres.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var version string
	if err = pool.QueryRow(ctx, "SHOW server_version").Scan(&version); err != nil || !strings.HasPrefix(version, "18.6") {
		t.Fatalf("requires PG18.6: %s %v", version, err)
	}
	path, err := os.MkdirTemp("/data/temp", "subscription-creation-fixture.")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(path); err != nil {
			t.Error(err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Error("fixture cleanup not verified", path)
		}
	})
	actor := &ports.Actor{Type: ports.ActorTypeUser, ID: uuid.NewString(), Role: ports.RoleMember}
	q := db.New(pool)
	if _, err = q.CreateUser(ctx, db.CreateUserParams{ID: uuid.MustParse(actor.ID), Username: "creation-owner", PasswordHash: "fixture", Role: ports.RoleMember, IsActive: true}); err != nil {
		t.Fatal(err)
	}
	f := creationFixture{pool: pool, store: postgres.NewSubscriptionCreationStore(pool, idgen.New()), actor: actor, movie: uuid.New(), tv: uuid.New(), path: path}
	for _, r := range []struct {
		id   uuid.UUID
		kind string
	}{{f.movie, "movie"}, {f.tv, "tv"}} {
		if _, err = q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: r.id, Name: r.kind, Path: path + "/" + r.kind, MediaType: r.kind}); err != nil {
			t.Fatal(err)
		}
	}
	return f
}
func creationIdentity(key string) ports.SubscriptionCreationIdentity {
	sum := sha256.Sum256([]byte(key))
	return ports.SubscriptionCreationIdentity{Key: key, RequestHash: hex.EncodeToString(sum[:])}
}
func creationMovie(id int64) *ports.PreparedSubscriptionCreationMetadata {
	return &ports.PreparedSubscriptionCreationMetadata{TMDBID: id, Media: ports.SubscriptionMediaInput{Namespace: "tmdb.movie", ExternalID: fmt.Sprint(id), MediaType: "movie", Title: "Native Movie", Seasons: []ports.SeasonEpisodes{}}, Units: []ports.SubscriptionMediaUnitSpec{{UnitKey: "movie"}}, Seasons: []ports.TVSeasonMetadata{}}
}
func creationTV(id int64, seasons ...ports.TVSeasonMetadata) *ports.PreparedSubscriptionCreationMetadata {
	p := &ports.PreparedSubscriptionCreationMetadata{TMDBID: id, Media: ports.SubscriptionMediaInput{Namespace: "tmdb.tv", ExternalID: fmt.Sprint(id), MediaType: "tv", Title: "Native TV", Seasons: []ports.SeasonEpisodes{}}, Units: []ports.SubscriptionMediaUnitSpec{}, Seasons: seasons}
	for _, s := range seasons {
		p.Media.Seasons = append(p.Media.Seasons, ports.SeasonEpisodes{Season: s.SeasonNumber, Count: len(s.Episodes)})
		for _, e := range s.Episodes {
			sn, en := int32(s.SeasonNumber), int32(e.EpisodeNumber)
			p.Units = append(p.Units, ports.SubscriptionMediaUnitSpec{UnitKey: fmt.Sprintf("S%02dE%02d", sn, en), SeasonNumber: &sn, EpisodeNumber: &en})
		}
	}
	return p
}
func creationSeason(n int, episodes ...int) ports.TVSeasonMetadata {
	s := ports.TVSeasonMetadata{SeasonNumber: n, Complete: true, Episodes: []ports.TVEpisodeMetadata{}}
	for _, e := range episodes {
		s.Episodes = append(s.Episodes, ports.TVEpisodeMetadata{EpisodeNumber: e, TMDBEpisodeID: int64(10000 + n*100 + e)})
	}
	return s
}
func creationInput(id int64, kind string) ports.SubscriptionCreationInput {
	return ports.SubscriptionCreationInput{TMDBID: &id, MediaType: kind, QualityPolicyID: "default", ScopeType: "all"}
}
func creationDecider(in ports.SubscriptionCreationInput, hint ports.SubscriptionCreationContext) ports.SubscriptionCreationDecider {
	return func(c ports.SubscriptionCreateContext) (ports.SubscriptionCreateDecision, error) {
		cmd := ports.CreateSubscriptionCommand{UserID: in.UserID, MediaID: c.MediaID, RootID: hint.RootID, QualityPolicyID: in.QualityPolicyID, QualityPolicyRevision: c.PolicyRevision, IndexerIDs: in.IndexerIDs, DownloaderID: in.DownloaderID, ScopeType: in.ScopeType, Seasons: in.Seasons, IncludeSpecials: in.IncludeSpecials, SelectedUnitIDs: in.SelectedUnitIDs, EpisodeKeys: in.EpisodeKeys}
		if cmd.QualityPolicyID == "" {
			cmd.QualityPolicyID = "default"
		}
		if cmd.ScopeType == "" {
			cmd.ScopeType = "all"
		}
		if c.Existing != nil {
			if cmd.IndexerIDs == nil {
				cmd.IndexerIDs = c.Existing.IndexerIDs
			}
			if cmd.DownloaderID == nil {
				cmd.DownloaderID = c.Existing.DownloaderID
			}
		}
		if cmd.IndexerIDs == nil {
			cmd.IndexerIDs = []uuid.UUID{}
		}
		if cmd.Seasons == nil {
			cmd.Seasons = []int{}
		}
		if cmd.SelectedUnitIDs == nil {
			cmd.SelectedUnitIDs = []uuid.UUID{}
		}
		selected := map[uuid.UUID]bool{}
		for _, id := range cmd.SelectedUnitIDs {
			selected[id] = true
		}
		for _, u := range c.Units {
			for _, key := range in.EpisodeKeys {
				if u.UnitKey == key && !selected[u.ID] {
					cmd.SelectedUnitIDs = append(cmd.SelectedUnitIDs, u.ID)
					selected[u.ID] = true
				}
			}
		}
		d := ports.SubscriptionCreateDecision{Command: cmd, Wanted: []ports.SubscriptionWantedDecision{}}
		for _, u := range c.Units {
			if u.Retired {
				continue
			}
			if u.SeasonNumber != nil {
				if *u.SeasonNumber == 0 && !cmd.IncludeSpecials {
					continue
				}
				if len(cmd.Seasons) > 0 {
					found := false
					for _, n := range cmd.Seasons {
						found = found || int(*u.SeasonNumber) == n
					}
					if !found {
						continue
					}
				}
			}
			d.Wanted = append(d.Wanted, ports.SubscriptionWantedDecision{UnitID: u.ID, ScopeExcluded: cmd.ScopeType == "selected" && !selected[u.ID]})
		}
		return d, nil
	}
}
func (f creationFixture) read(t *testing.T, in ports.SubscriptionCreationInput, id ports.SubscriptionCreationIdentity) ports.SubscriptionCreationContext {
	t.Helper()
	h, r, err := f.store.ReadSubscriptionCreationContext(context.Background(), f.actor, in, id)
	if err != nil || r != nil {
		t.Fatalf("read context: %+v %v", r, err)
	}
	return h
}
func (f creationFixture) create(t *testing.T, in ports.SubscriptionCreationInput, p *ports.PreparedSubscriptionCreationMetadata, key string) *ports.SubscriptionCreationReceipt {
	t.Helper()
	id := creationIdentity(key)
	h := f.read(t, in, id)
	r, err := f.store.CommitSubscriptionCreation(context.Background(), f.actor, in, h, p, id, creationDecider(in, h))
	if err != nil {
		t.Fatal("commit", err)
	}
	return r
}
func (f creationFixture) snapshot(t *testing.T) string {
	t.Helper()
	var out string
	err := f.pool.QueryRow(context.Background(), `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM media t),(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM media_identity t),(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM media_unit t),(SELECT jsonb_agg(to_jsonb(t) ORDER BY unit_id) FROM media_unit_identity t),(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM subscription t),(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM wanted_item t),(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM idempotency_record t),(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM audit_log t),(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM outbox_event t),(SELECT count(*) FROM tv_season_metadata_snapshot),(SELECT count(*) FROM job))::text`).Scan(&out)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSubscriptionCreationMovieTVSparseAndIdentity(t *testing.T) {
	f := newCreationFixture(t)
	ctx := context.Background()
	movie := f.create(t, creationInput(501, ""), creationMovie(501), "movie")
	date := time.Date(2020, 2, 3, 0, 0, 0, 0, time.UTC)
	s := creationSeason(1, 1, 3)
	s.Episodes[1].AirDate = &date
	tv := f.create(t, creationInput(501, "tv"), creationTV(501, s), "tv")
	if movie.Subscription.MediaID == tv.Subscription.MediaID || len(movie.Wanted) != 1 || movie.Wanted[0].UnitKey != "movie" || len(tv.Wanted) != 2 || tv.Wanted[1].UnitKey != "S01E03" {
		t.Fatalf("namespace/sparse results: %+v %+v", movie, tv)
	}
	if n := intentCount(t, f.pool, `SELECT count(*) FROM media_unit u JOIN media_unit_identity i ON i.unit_id=u.id WHERE u.media_id=$1 AND i.namespace='tmdb.episode' AND i.tmdb_series_id=501 AND (u.unit_key='S01E01' OR (u.unit_key='S01E03' AND u.air_date='2020-02-03'))`, tv.Subscription.MediaID); n != 2 {
		t.Fatal("actual episode identities/dates", n)
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM tv_season_metadata_snapshot WHERE media_id=$1", tv.Subscription.MediaID); n != 1 {
		t.Fatal("season proof", n)
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM wanted_item WHERE next_evaluate_at<=clock_timestamp()"); n != 3 {
		t.Fatal("not due after commit", n)
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM library_root WHERE baseline_completed"); n != 0 {
		t.Fatal("intent granted baseline authority")
	}
	before := f.snapshot(t)
	got, err := f.store.ReadSubscriptionCreationReceipt(ctx, f.actor, "tv")
	if err != nil || !reflect.DeepEqual(got, tv) || before != f.snapshot(t) {
		t.Fatal("historical zero-write GET", err)
	}
	// Receipt lookup precedes obsolete media/root lookup and provider observation.
	bad := creationInput(999, "tv")
	bad.RootID = new(uuid.New())
	_, cached, err := f.store.ReadSubscriptionCreationContext(ctx, f.actor, bad, creationIdentity("tv"))
	if err != nil || !reflect.DeepEqual(cached, tv) {
		t.Fatal("receipt did not precede lookup", err)
	}
}

func TestSubscriptionCreationExistingTVIncrementalCrossUser(t *testing.T) {
	f := newCreationFixture(t)
	ctx := context.Background()
	in := creationInput(502, "tv")
	first := f.create(t, in, creationTV(502, creationSeason(1, 1, 3)), "initial")
	other := *f.actor
	other.ID = uuid.NewString()
	if _, err := db.New(f.pool).CreateUser(ctx, db.CreateUserParams{ID: uuid.MustParse(other.ID), Username: "other", PasswordHash: "fixture", Role: ports.RoleMember, IsActive: true}); err != nil {
		t.Fatal(err)
	}
	peer := f
	peer.actor = &other
	otherResult := peer.create(t, in, nil, "other")
	for _, r := range []*ports.SubscriptionCreationReceipt{first, otherResult} {
		execIntent(t, f.pool, "UPDATE subscription SET state='paused' WHERE id=$1", r.Subscription.ID)
		execIntent(t, f.pool, "UPDATE wanted_item SET user_skipped=true,is_skipped=true WHERE id=$1", r.Wanted[0].ID)
	}
	beforeVersion := first.Subscription.IntentVersion
	expanded := f.create(t, in, creationTV(502, creationSeason(1, 1, 3, 5), creationSeason(2, 2)), "expand")
	if expanded.Subscription.MediaID != first.Subscription.MediaID || expanded.Subscription.IntentVersion != beforeVersion || expanded.Subscription.State != "paused" || len(expanded.Wanted) != 4 {
		t.Fatalf("ensure changed intent/state: %+v", expanded)
	}
	if expanded.Wanted[0].ID != first.Wanted[0].ID || !expanded.Wanted[0].IsSkipped {
		t.Fatal("requester UUID/skip lost")
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM wanted_item WHERE subscription_id=$1", otherResult.Subscription.ID); n != 4 {
		t.Fatal("peer did not expand", n)
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM wanted_item WHERE subscription_id=$1 AND user_skipped AND is_skipped", otherResult.Subscription.ID); n != 1 {
		t.Fatal("peer skip lost", n)
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM subscription WHERE state='paused' AND intent_version=1"); n != 2 {
		t.Fatal("peer intent changed", n)
	}
	got, err := f.store.ReadSubscriptionCreationReceipt(ctx, f.actor, "initial")
	if err != nil || !reflect.DeepEqual(got, first) {
		t.Fatal("old receipt changed after metadata/control", err)
	}
	// Another subscription's selected scope excludes the new season and episode.
	execIntent(t, f.pool, `UPDATE subscription SET scope_type='selected',selected_unit_ids=jsonb_build_array($2::text) WHERE id=$1`, otherResult.Subscription.ID, otherResult.Wanted[0].UnitID.String())
	f.create(t, in, creationTV(502, creationSeason(1, 1, 3, 5, 7)), "expand-again")
	if n := intentCount(t, f.pool, "SELECT count(*) FROM wanted_item WHERE subscription_id=$1 AND scope_excluded", otherResult.Subscription.ID); n != 4 {
		t.Fatal("peer current selected scope ignored", n)
	}
}

func TestSubscriptionCreationRetirementEvidence(t *testing.T) {
	f := newCreationFixture(t)
	in := creationInput(503, "tv")
	first := f.create(t, in, creationTV(503, creationSeason(1, 1, 3), creationSeason(2, 1)), "initial")
	p := creationTV(503, creationSeason(1, 1))
	p.Seasons[0].Complete = false
	p.Media.Seasons[0].Count = 2
	f.create(t, in, p, "partial")
	if n := intentCount(t, f.pool, "SELECT count(*) FROM media_unit WHERE media_id=$1 AND retired", first.Subscription.MediaID); n != 0 {
		t.Fatal("partial retired", n)
	}
	empty := creationTV(503, ports.TVSeasonMetadata{SeasonNumber: 1, Complete: true, Episodes: []ports.TVEpisodeMetadata{}})
	empty.Media.Seasons[0].Count = 2
	f.create(t, in, empty, "empty")
	if n := intentCount(t, f.pool, "SELECT count(*) FROM media_unit WHERE media_id=$1 AND retired", first.Subscription.MediaID); n != 0 {
		t.Fatal("empty retired", n)
	}
	f.create(t, in, creationTV(503, creationSeason(1, 1)), "complete")
	if n := intentCount(t, f.pool, "SELECT count(*) FROM media_unit WHERE media_id=$1 AND retired AND unit_key='S01E03'", first.Subscription.MediaID); n != 1 {
		t.Fatal("complete nonempty did not retire", n)
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM media_unit WHERE media_id=$1 AND retired AND season_number=2", first.Subscription.MediaID); n != 0 {
		t.Fatal("unobserved season retired", n)
	}
}

func TestSubscriptionCreationZeroAnnouncedSeason(t *testing.T) {
	f := newCreationFixture(t)
	in := creationInput(504, "tv")
	p := creationTV(504)
	p.Seasons = []ports.TVSeasonMetadata{}
	p.Media.Seasons = []ports.SeasonEpisodes{{Season: 1, Count: 0}}
	r := f.create(t, in, p, "zero")
	if len(r.Wanted) != 0 || r.Wanted == nil {
		t.Fatal("zero announced fabricated units", r)
	}
	for _, table := range []string{"media_unit", "media_unit_identity", "wanted_item", "tv_season_metadata_snapshot"} {
		if n := intentCount(t, f.pool, "SELECT count(*) FROM "+table); n != 0 {
			t.Fatal(table, n)
		}
	}
	if _, err := f.store.ReadSubscriptionCreationReceipt(context.Background(), f.actor, "zero"); err != nil {
		t.Fatal(err)
	}
}

func TestSubscriptionCreationBindingsEnsureAndExplicitVersion(t *testing.T) {
	f := newCreationFixture(t)
	ctx := context.Background()
	q := db.New(f.pool)
	ix, err := q.CreateIndexer(ctx, db.CreateIndexerParams{ID: uuid.New(), Name: "fixture", Url: "http://127.0.0.1", ApiKeyEncrypted: []byte("cipher"), Categories: []byte("[]")})
	if err != nil {
		t.Fatal(err)
	}
	dl, err := q.CreateDownloadClient(ctx, db.CreateDownloadClientParams{ID: uuid.New(), Name: "fixture", ClientType: "qbittorrent", Url: "http://127.0.0.1", PasswordEncrypted: []byte("cipher"), RemotePathMap: []byte("[]")})
	if err != nil {
		t.Fatal(err)
	}
	in := creationInput(505, "movie")
	in.IndexerIDs = []uuid.UUID{ix.ID}
	in.DownloaderID = &dl.ID
	first := f.create(t, in, creationMovie(505), "initial")
	in.IndexerIDs = nil
	in.DownloaderID = nil
	kept := f.create(t, in, nil, "ensure")
	if !reflect.DeepEqual(kept.Subscription, first.Subscription) {
		t.Fatal("nil ensure changed bindings/version")
	}
	// A newer policy head alone does not silently rebind an ensure.
	execIntent(t, f.pool, "UPDATE policy SET current_revision=current_revision+1 WHERE id='default'")
	kept = f.create(t, in, nil, "ensure-old-revision")
	if kept.Subscription.QualityPolicyRevision != first.Subscription.QualityPolicyRevision {
		t.Fatal("ensure rebound policy")
	}
	in.IndexerIDs = []uuid.UUID{}
	h := f.read(t, in, creationIdentity("clear-unversioned"))
	before := f.snapshot(t)
	if _, err = f.store.CommitSubscriptionCreation(ctx, f.actor, in, h, nil, creationIdentity("clear-unversioned"), creationDecider(in, h)); !errors.Is(err, ports.ErrSubscriptionBindingsConflict) || before != f.snapshot(t) {
		t.Fatal("unversioned binding clear", err)
	}
	// Restore policy head for this fixture: explicit version changes only bindings.
	execIntent(t, f.pool, "UPDATE policy SET current_revision=$1 WHERE id='default'", first.Subscription.QualityPolicyRevision)
	version := first.Subscription.IntentVersion
	in.ExpectedIntentVersion = &version
	execIntent(t, f.pool, "UPDATE subscription SET state='archived' WHERE id=$1", first.Subscription.ID)
	cleared := f.create(t, in, nil, "clear")
	if len(cleared.Subscription.IndexerIDs) != 0 || cleared.Subscription.DownloaderID == nil || *cleared.Subscription.DownloaderID != dl.ID || cleared.Subscription.IntentVersion != version+1 || cleared.Subscription.State != "archived" {
		t.Fatalf("explicit reconfiguration: %+v", cleared)
	}
	h = f.read(t, in, creationIdentity("stale"))
	if _, err = f.store.CommitSubscriptionCreation(ctx, f.actor, in, h, nil, creationIdentity("stale"), creationDecider(in, h)); !errors.Is(err, ports.ErrSubscriptionBindingsConflict) {
		t.Fatal("stale version", err)
	}
}

func TestSubscriptionCreationRootsAndUUIDInference(t *testing.T) {
	f := newCreationFixture(t)
	ctx := context.Background()
	q := db.New(f.pool)
	offline := uuid.New()
	if _, err := q.CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: offline, Name: "offline", Path: f.path + "/offline", MediaType: "tv"}); err != nil {
		t.Fatal(err)
	}
	execIntent(t, f.pool, "UPDATE library_root SET health='offline' WHERE id=$1", offline)
	h := f.read(t, creationInput(506, "tv"), creationIdentity("unique"))
	if h.RootID != f.tv {
		t.Fatal("offline caused ambiguity")
	}
	m, err := q.CreateMedia(ctx, db.CreateMediaParams{ID: uuid.New(), Title: "Trusted existing TV", MediaType: "tv"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = q.CreateTVUnit(ctx, db.CreateTVUnitParams{ID: uuid.New(), MediaID: m.ID, UnitKey: "S02E03", SeasonNumber: pgtype.Int4{Int32: 2, Valid: true}, EpisodeNumber: pgtype.Int4{Int32: 3, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	in := ports.SubscriptionCreationInput{MediaID: &m.ID}
	r := f.create(t, in, nil, "uuid")
	if r.Subscription.RootID != f.tv || r.Wanted[0].UnitKey != "S02E03" {
		t.Fatal("UUID actual type inference")
	}
	in.MediaType = "movie"
	if _, _, err = f.store.ReadSubscriptionCreationContext(ctx, f.actor, in, creationIdentity("wrong-type")); !errors.Is(err, ports.ErrInvalidSubscriptionBindings) {
		t.Fatal("explicit wrong type", err)
	}
	execIntent(t, f.pool, "UPDATE library_root SET health='online' WHERE id=$1", offline)
	if _, _, err = f.store.ReadSubscriptionCreationContext(ctx, f.actor, creationInput(507, "tv"), creationIdentity("ambiguous")); !errors.Is(err, ports.ErrAmbiguousSubscriptionRoot) {
		t.Fatal("multiple online", err)
	}
	explicit := creationInput(507, "tv")
	explicit.RootID = &f.tv
	if x := f.read(t, explicit, creationIdentity("explicit")); x.RootID != f.tv {
		t.Fatal("explicit root")
	}
	execIntent(t, f.pool, "UPDATE library_root SET health='offline' WHERE media_type='tv'")
	if _, _, err = f.store.ReadSubscriptionCreationContext(ctx, f.actor, creationInput(507, "tv"), creationIdentity("no-root")); !errors.Is(err, ports.ErrNoSubscriptionRoot) {
		t.Fatal("no root", err)
	}
	execIntent(t, f.pool, "ALTER TABLE library_root RENAME TO unavailable_root")
	if _, _, err = f.store.ReadSubscriptionCreationContext(ctx, f.actor, creationInput(507, "tv"), creationIdentity("fault")); !errors.Is(err, ports.ErrUnavailable) {
		t.Fatal("root DB fault mislabeled", err)
	}
}

func TestSubscriptionCreationConcurrentKeysAndHash(t *testing.T) {
	f := newCreationFixture(t)
	ctx := context.Background()
	in := creationInput(508, "movie")
	id := creationIdentity("same")
	h := f.read(t, in, id)
	const peers = 8
	var wg sync.WaitGroup
	var calls atomic.Int32
	results := make(chan *ports.SubscriptionCreationReceipt, peers)
	failures := make(chan error, peers)
	for range peers {
		wg.Go(func() {
			r, e := f.store.CommitSubscriptionCreation(ctx, f.actor, in, h, creationMovie(508), id, func(c ports.SubscriptionCreateContext) (ports.SubscriptionCreateDecision, error) {
				calls.Add(1)
				return creationDecider(in, h)(c)
			})
			results <- r
			failures <- e
		})
	}
	wg.Wait()
	close(results)
	close(failures)
	for e := range failures {
		if e != nil {
			t.Fatal(e)
		}
	}
	var canonical *ports.SubscriptionCreationReceipt
	for r := range results {
		if canonical == nil {
			canonical = r
		} else if !reflect.DeepEqual(canonical, r) {
			t.Fatal("unstable same-key result")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("decider count", calls.Load())
	}
	for _, table := range []string{"media", "subscription", "idempotency_record", "audit_log", "outbox_event"} {
		if n := intentCount(t, f.pool, "SELECT count(*) FROM "+table); n != 1 {
			t.Fatal(table, n)
		}
	}
	conflict := id
	conflict.RequestHash = creationIdentity("different").RequestHash
	if _, e := f.store.CommitSubscriptionCreation(ctx, f.actor, in, h, nil, conflict, creationDecider(in, h)); !errors.Is(e, ports.ErrSubscriptionBindingsConflict) {
		t.Fatal("conflicting same-key hash", e)
	}
}

func TestSubscriptionCreationStaleHintsAndCallbackTamper(t *testing.T) {
	for _, mode := range []string{"root", "media", "intent", "scalar", "wanted", "context", "provider", "missing-season", "bindings"} {
		t.Run(mode, func(t *testing.T) {
			f := newCreationFixture(t)
			ctx := context.Background()
			in := creationInput(509, "tv")
			p := creationTV(509, creationSeason(1, 1, 3))
			if mode == "media" || mode == "intent" {
				f.create(t, in, p, "initial")
			}
			id := creationIdentity("tamper")
			h := f.read(t, in, id)
			switch mode {
			case "root":
				execIntent(t, f.pool, "UPDATE library_root SET health_revision=health_revision+1 WHERE id=$1", f.tv)
			case "media":
				execIntent(t, f.pool, "UPDATE media SET metadata_version=metadata_version+1 WHERE id=$1", h.MediaID)
			case "intent":
				execIntent(t, f.pool, "UPDATE subscription SET intent_version=intent_version+1 WHERE id=$1", h.Existing.ID)
			case "provider":
				p.Units[0].UnitKey = "S01E02"
			case "missing-season":
				p.Units = []ports.SubscriptionMediaUnitSpec{}
				p.Seasons = []ports.TVSeasonMetadata{}
			}
			before := f.snapshot(t)
			callback := func(c ports.SubscriptionCreateContext) (ports.SubscriptionCreateDecision, error) {
				d, e := creationDecider(in, h)(c)
				switch mode {
				case "scalar":
					d.Command.ScopeType = "selected"
				case "wanted":
					d.Wanted[0].UnitID = uuid.New()
				case "bindings":
					d.Command.IndexerIDs = []uuid.UUID{uuid.New()}
				case "context":
					c.Units[0].ID = uuid.New()
					d.Wanted[0].UnitID = c.Units[0].ID
				}
				return d, e
			}
			_, err := f.store.CommitSubscriptionCreation(ctx, f.actor, in, h, p, id, callback)
			expected := ports.ErrSubscriptionBindingsConflict
			if mode == "provider" || mode == "missing-season" {
				expected = ports.ErrInvalidSubscriptionBindings
			}
			if !errors.Is(err, expected) || before != f.snapshot(t) {
				t.Fatal("tamper escaped rollback", err)
			}
		})
	}
}

func TestSubscriptionCreationCallbackCannotMutateFrozenRequest(t *testing.T) {
	f := newCreationFixture(t)
	ctx := context.Background()
	in := creationInput(510, "movie")
	id := creationIdentity("independent")
	h := f.read(t, in, id)
	p := creationMovie(510)
	r, err := f.store.CommitSubscriptionCreation(ctx, f.actor, in, h, p, id, func(c ports.SubscriptionCreateContext) (ports.SubscriptionCreateDecision, error) {
		d, e := creationDecider(in, h)(c)
		*in.TMDBID = 999
		p.Media.ExternalID = "999"
		f.actor.Role = ports.RoleAdmin
		return d, e
	})
	if err != nil || r.Subscription.MediaID == uuid.Nil {
		t.Fatal("caller mutated frozen material", err)
	}
	if n := intentCount(t, f.pool, "SELECT count(*) FROM media_identity WHERE external_id='510'"); n != 1 {
		t.Fatal("provider pointer mutation escaped")
	}
}

func TestSubscriptionCreationTailFaultsRollback(t *testing.T) {
	for _, table := range []string{"idempotency_record", "audit_log", "outbox_event"} {
		t.Run(table, func(t *testing.T) {
			f := newCreationFixture(t)
			ctx := context.Background()
			in := creationInput(511, "tv")
			p := creationTV(511, creationSeason(1, 1, 3))
			id := creationIdentity("tail")
			h := f.read(t, in, id)
			execIntent(t, f.pool, `CREATE FUNCTION creation_tail_fault() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'private fault';END$$`)
			execIntent(t, f.pool, "CREATE TRIGGER creation_fault BEFORE INSERT ON "+table+" FOR EACH ROW EXECUTE FUNCTION creation_tail_fault()")
			before := f.snapshot(t)
			if _, err := f.store.CommitSubscriptionCreation(ctx, f.actor, in, h, p, id, creationDecider(in, h)); !errors.Is(err, ports.ErrUnavailable) || before != f.snapshot(t) {
				t.Fatal("tail did not rollback", err)
			}
			execIntent(t, f.pool, "DROP TRIGGER creation_fault ON "+table)
			f.create(t, in, p, "tail")
			// The same tail failure must undo shared existing-TV metadata updates.
			p = creationTV(511, creationSeason(1, 1, 3, 5), creationSeason(2, 1))
			id = creationIdentity("existing-tail")
			h = f.read(t, in, id)
			execIntent(t, f.pool, "CREATE TRIGGER creation_fault BEFORE INSERT ON "+table+" FOR EACH ROW EXECUTE FUNCTION creation_tail_fault()")
			before = f.snapshot(t)
			if _, err := f.store.CommitSubscriptionCreation(ctx, f.actor, in, h, p, id, creationDecider(in, h)); !errors.Is(err, ports.ErrUnavailable) || before != f.snapshot(t) {
				t.Fatal("existing TV tail escaped rollback", err)
			}
		})
	}
}

func TestSubscriptionCreationReceiptCorruptionFailsClosed(t *testing.T) {
	f := newCreationFixture(t)
	ctx := context.Background()
	f.create(t, creationInput(512, "movie"), creationMovie(512), "receipt")
	var original []byte
	var status int
	var resource string
	if err := f.pool.QueryRow(ctx, "SELECT response_body,response_code,resource_id FROM idempotency_record").Scan(&original, &status, &resource); err != nil {
		t.Fatal(err)
	}
	corruptions := []struct{ name, sql string }{{"unknown", "response_body=response_body || '{\"unsafe\":true}'::jsonb"}, {"action", `response_body=jsonb_set(response_body,'{action}','"foreign.action"')`}, {"version", `response_body=jsonb_set(response_body,'{version}','2')`}, {"hash", "request_hash=repeat('0',64)"}, {"status", "response_code=202"}, {"resource", "resource_id='00000000-0000-0000-0000-000000000000'"}, {"missing", `response_body=response_body #- '{result,wanted}'`}, {"unit", `response_body=jsonb_set(response_body,'{result,wanted,0,unit_key}','"fabricated"')`}, {"null-array", `response_body=jsonb_set(response_body,'{result,subscription,IndexerIDs}','null')`}}
	for _, c := range corruptions {
		t.Run(c.name, func(t *testing.T) {
			execIntent(t, f.pool, "UPDATE idempotency_record SET "+c.sql)
			before := f.snapshot(t)
			if r, err := f.store.ReadSubscriptionCreationReceipt(ctx, f.actor, "receipt"); r != nil || !errors.Is(err, ports.ErrSubscriptionBindingsConflict) || before != f.snapshot(t) {
				t.Fatal("unsafe receipt", err)
			}
			execIntent(t, f.pool, "UPDATE idempotency_record SET response_body=$1,response_code=$2,resource_id=$3,request_hash=$4", original, status, resource, creationIdentity("receipt").RequestHash)
		})
	}
}

func TestSubscriptionCreationCurrentOwnerAndAccountBeforeReceipt(t *testing.T) {
	f := newCreationFixture(t)
	ctx := context.Background()
	owner := uuid.New()
	if _, err := db.New(f.pool).CreateUser(ctx, db.CreateUserParams{ID: owner, Username: "actual-owner", PasswordHash: "fixture", Role: ports.RoleMember, IsActive: true}); err != nil {
		t.Fatal(err)
	}
	execIntent(t, f.pool, "UPDATE app_user SET role='admin' WHERE id=$1", uuid.MustParse(f.actor.ID))
	f.actor.Role = ports.RoleAdmin
	in := creationInput(513, "movie")
	in.UserID = &owner
	f.create(t, in, creationMovie(513), "owned")
	execIntent(t, f.pool, "UPDATE app_user SET role='member' WHERE id=$1", uuid.MustParse(f.actor.ID))
	f.actor.Role = ports.RoleMember
	if _, err := f.store.ReadSubscriptionCreationReceipt(ctx, f.actor, "owned"); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("demoted admin replayed foreign object", err)
	}
	if _, _, err := f.store.ReadSubscriptionCreationContext(ctx, f.actor, in, creationIdentity("owned")); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("demoted requested owner ignored", err)
	}
	execIntent(t, f.pool, "UPDATE app_user SET is_active=false WHERE id=$1", uuid.MustParse(f.actor.ID))
	if _, err := f.store.ReadSubscriptionCreationReceipt(ctx, f.actor, "owned"); !errors.Is(err, ports.ErrForbidden) {
		t.Fatal("disabled replay", err)
	}
}

func TestSubscriptionCreationAccountLockWaitRechecks(t *testing.T) {
	for _, mutation := range []string{"is_active=false", "role='member'"} {
		t.Run(mutation, func(t *testing.T) {
			f := newCreationFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			execIntent(t, f.pool, "UPDATE app_user SET role='admin' WHERE id=$1", uuid.MustParse(f.actor.ID))
			f.actor.Role = ports.RoleAdmin
			in := creationInput(514, "movie")
			id := creationIdentity("wait")
			h := f.read(t, in, id)
			tx, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if _, err = tx.Exec(ctx, "UPDATE app_user SET "+mutation+" WHERE id=$1", uuid.MustParse(f.actor.ID)); err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			var calls atomic.Int32
			go func() {
				_, e := f.store.CommitSubscriptionCreation(ctx, f.actor, in, h, creationMovie(514), id, func(c ports.SubscriptionCreateContext) (ports.SubscriptionCreateDecision, error) {
					calls.Add(1)
					return creationDecider(in, h)(c)
				})
				result <- e
			}()
			// The database proves a real account-row wait before permission changes commit.
			waited := false
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				var blocked bool
				err = f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND cardinality(pg_blocking_pids(pid))>0 AND query LIKE '%app_user%')`).Scan(&blocked)
				if err != nil {
					break
				}
				if blocked {
					waited = true
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if err = tx.Commit(ctx); err != nil {
				cancel()
				<-result
				t.Fatal(err)
			}
			err = <-result
			if !waited || !errors.Is(err, ports.ErrForbidden) || calls.Load() != 0 {
				t.Fatal("account wait did not recheck", waited, err, calls.Load())
			}
			if n := intentCount(t, f.pool, "SELECT count(*) FROM media"); n != 0 {
				t.Fatal("revoked publisher wrote")
			}
		})
	}
}

func TestSubscriptionCreationMainPureDecider(t *testing.T) {
	f := newCreationFixture(t)
	ctx := context.Background()
	publish := func(in ports.SubscriptionCreationInput, p *ports.PreparedSubscriptionCreationMetadata, key string) *ports.SubscriptionCreationReceipt {
		t.Helper()
		id := creationIdentity(key)
		h := f.read(t, in, id)
		r, err := f.store.CommitSubscriptionCreation(ctx, f.actor, in, h, p, id, func(c ports.SubscriptionCreateContext) (ports.SubscriptionCreateDecision, error) {
			return subscription.DecideSubscriptionCreation(in, h.RootID, c)
		})
		if err != nil {
			t.Fatal("Main pure decider integration", err)
		}
		return r
	}
	publish(creationInput(516, "movie"), creationMovie(516), "main-movie")
	in := creationInput(516, "tv")
	in.ScopeType = "selected"
	in.EpisodeKeys = []string{"S01E03"}
	first := publish(in, creationTV(516, creationSeason(1, 1, 3)), "main-tv")
	if len(first.Wanted) != 2 || !first.Wanted[0].IsSkipped || first.Wanted[1].IsSkipped {
		t.Fatal("selected scope", first)
	}
	next := publish(in, creationTV(516, creationSeason(1, 1, 3, 5)), "main-incremental")
	if next.Subscription.IntentVersion != first.Subscription.IntentVersion || len(next.Wanted) != 3 || !next.Wanted[2].IsSkipped {
		t.Fatal("selected incremental ensure", next)
	}
	zero := creationTV(517)
	zero.Media.Seasons = []ports.SeasonEpisodes{{Season: 1, Count: 0}}
	publish(creationInput(517, "tv"), zero, "main-zero")
}

func TestSubscriptionCreationTVIdentityConflictsRollback(t *testing.T) {
	f := newCreationFixture(t)
	ctx := context.Background()
	in := creationInput(518, "tv")
	f.create(t, in, creationTV(518, creationSeason(1, 1, 3)), "identity-initial")
	for _, mode := range []string{"replace", "reuse"} {
		t.Run(mode, func(t *testing.T) {
			p := creationTV(518, creationSeason(1, 1, 3, 5))
			if mode == "replace" {
				p.Seasons[0].Episodes[0].TMDBEpisodeID = 999999
			} else {
				p = creationTV(518, creationSeason(1, 5))
				p.Seasons[0].Episodes[0].TMDBEpisodeID = 10101
			}
			id := creationIdentity("identity-" + mode)
			h := f.read(t, in, id)
			before := f.snapshot(t)
			if _, err := f.store.CommitSubscriptionCreation(ctx, f.actor, in, h, p, id, creationDecider(in, h)); !errors.Is(err, ports.ErrSubscriptionBindingsConflict) || before != f.snapshot(t) {
				t.Fatal("identity conflict published", err)
			}
		})
	}
}

func TestSubscriptionCreationInferredRootRechecked(t *testing.T) {
	f := newCreationFixture(t)
	ctx := context.Background()
	in := creationInput(519, "movie")
	id := creationIdentity("root-choice")
	h := f.read(t, in, id)
	if _, err := db.New(f.pool).CreateLibraryRoot(ctx, db.CreateLibraryRootParams{ID: uuid.New(), Name: "new-online", Path: f.path + "/new-online", MediaType: "movie"}); err != nil {
		t.Fatal(err)
	}
	before := f.snapshot(t)
	if _, err := f.store.CommitSubscriptionCreation(ctx, f.actor, in, h, creationMovie(519), id, creationDecider(in, h)); !errors.Is(err, ports.ErrAmbiguousSubscriptionRoot) || before != f.snapshot(t) {
		t.Fatal("inferred choice was not re-resolved", err)
	}
}

func TestSubscriptionCreationUnencodableMetadataFailsClosed(t *testing.T) {
	f := newCreationFixture(t)
	ctx := context.Background()
	in := creationInput(520, "tv")
	f.create(t, in, creationTV(520, creationSeason(1, 1)), "bad-time-initial")
	p := creationTV(520, creationSeason(1, 1, 3))
	invalidDate := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	p.Seasons[0].Episodes[0].AirDate = &invalidDate
	id := creationIdentity("bad-time")
	h := f.read(t, in, id)
	before := f.snapshot(t)
	if _, err := f.store.CommitSubscriptionCreation(ctx, f.actor, in, h, p, id, creationDecider(in, h)); !errors.Is(err, ports.ErrInvalidSubscriptionBindings) || before != f.snapshot(t) {
		t.Fatal("unencodable metadata silently discarded", err)
	}
}

func TestSubscriptionCreationFabricatedWantedIDFailsClosed(t *testing.T) {
	f := newCreationFixture(t)
	ctx := context.Background()
	r := f.create(t, creationInput(521, "movie"), creationMovie(521), "forged")
	r.Wanted[0].ID = uuid.New()
	var stored map[string]json.RawMessage
	var raw []byte
	if err := f.pool.QueryRow(ctx, "SELECT response_body FROM idempotency_record").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	stored["result"], _ = json.Marshal(r)
	sum := sha256.Sum256(stored["result"])
	stored["result_hash"], _ = json.Marshal(hex.EncodeToString(sum[:]))
	raw, _ = json.Marshal(stored)
	execIntent(t, f.pool, "UPDATE idempotency_record SET response_body=$1", raw)
	before := f.snapshot(t)
	if out, err := f.store.ReadSubscriptionCreationReceipt(ctx, f.actor, "forged"); out != nil || !errors.Is(err, ports.ErrSubscriptionBindingsConflict) || before != f.snapshot(t) {
		t.Fatal("fabricated Wanted association accepted", err)
	}
}

// Wait for an actual PostgreSQL row-lock wait, rather than assuming scheduling.
func creationPolicyWait(ctx context.Context, pool *pgxpool.Pool) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var blocked bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND cardinality(pg_blocking_pids(pid))>0 AND query LIKE '%FROM policy%')`).Scan(&blocked); err != nil {
			return false
		}
		if blocked {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func TestSubscriptionCreationPolicyBeforeIdentityLock(t *testing.T) {
	f := newCreationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	in := creationInput(522, "movie")
	id := creationIdentity("lock-order")
	h := f.read(t, in, id)
	blocker, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	if _, err = db.New(blocker).LockSearchPolicy(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := f.store.CommitSubscriptionCreation(ctx, f.actor, in, h, creationMovie(522), id, creationDecider(in, h))
		result <- err
	}()
	waited := creationPolicyWait(ctx, f.pool)
	probe, err := f.pool.Begin(ctx)
	if err != nil {
		cancel()
		_ = blocker.Rollback(context.Background())
		<-result
		t.Fatal(err)
	}
	var identityFree bool
	err = probe.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock(hashtextextended($1 || ':' || $2,0))", "tmdb.movie", "522").Scan(&identityFree)
	_ = probe.Rollback(context.Background())
	releaseErr := blocker.Commit(ctx)
	got := <-result
	if err != nil || releaseErr != nil || got != nil || !waited || !identityFree {
		t.Fatal("policy/identity lock order", err, releaseErr, got, waited, identityFree)
	}
}

func TestSubscriptionCreationRelatedSetRecheckedAfterLockWait(t *testing.T) {
	for _, mode := range []string{"insert", "reconfigure"} {
		t.Run(mode, func(t *testing.T) {
			f := newCreationFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			in := creationInput(523, "tv")
			first := f.create(t, in, creationTV(523, creationSeason(1, 1, 3)), "related-initial")
			q := db.New(f.pool)
			peerID := uuid.New()
			if _, err := q.CreateUser(ctx, db.CreateUserParams{ID: peerID, Username: "related-peer", PasswordHash: "fixture", Role: ports.RoleMember, IsActive: true}); err != nil {
				t.Fatal(err)
			}
			if _, err := q.CreatePolicy(ctx, db.CreatePolicyParams{ID: "peer", Name: "Peer"}); err != nil {
				t.Fatal(err)
			}
			rules, err := q.GetPolicyRevision(ctx, db.GetPolicyRevisionParams{PolicyID: "default", Revision: 1})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = q.InsertPolicyRevision(ctx, db.InsertPolicyRevisionParams{ID: uuid.New(), PolicyID: "peer", Revision: 1, Rules: rules.Rules}); err != nil {
				t.Fatal(err)
			}
			id := creationIdentity("related-wait")
			h := f.read(t, in, id)
			blocker, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = blocker.Rollback(context.Background()) }()
			if _, err = db.New(blocker).LockSearchPolicy(ctx, "default"); err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() {
				_, e := f.store.CommitSubscriptionCreation(ctx, f.actor, in, h, creationTV(523, creationSeason(1, 1, 3, 5)), id, creationDecider(in, h))
				result <- e
			}()
			waited := creationPolicyWait(ctx, f.pool)
			// Inject a valid concurrent database change after the publisher collected its
			// set and before it acquired policy/subscription/root/unit/media locks.
			if mode == "insert" {
				_, err = q.CreateSubscription(ctx, db.CreateSubscriptionParams{ID: uuid.New(), UserID: peerID, MediaID: first.Subscription.MediaID, RootID: f.tv, QualityPolicyID: "peer", QualityPolicyRevision: 1, IndexerIds: []byte("[]"), ScopeType: "all"})
			} else {
				_, err = f.pool.Exec(ctx, "UPDATE subscription SET quality_policy_id='peer',quality_policy_revision=1,intent_version=intent_version+1 WHERE id=$1", first.Subscription.ID)
			}
			before := f.snapshot(t)
			releaseErr := blocker.Commit(ctx)
			got := <-result
			if err != nil || releaseErr != nil || !waited || !errors.Is(got, ports.ErrSubscriptionBindingsConflict) || before != f.snapshot(t) {
				t.Fatal("related set escaped locks", mode, err, releaseErr, waited, got)
			}
		})
	}
}

func TestSubscriptionCreationStoredResultFieldsAllocated(t *testing.T) {
	f := newCreationFixture(t)
	r := f.create(t, creationInput(515, "movie"), creationMovie(515), "allocated")
	raw, err := json.Marshal(r)
	if err != nil || strings.Contains(string(raw), `"wanted":null`) || r.Subscription.IndexerIDs == nil || r.Subscription.Seasons == nil || r.Subscription.SelectedUnitIDs == nil {
		t.Fatal("unallocated receipt arrays", string(raw), err)
	}
}
