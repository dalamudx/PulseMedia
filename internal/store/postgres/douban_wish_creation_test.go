package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/dalamudx/PulseMedia/internal/usecase/subscription"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type wishCreationProvider struct {
	ports.MetadataProvider
	calls  atomic.Int32
	before func(context.Context) error
}

func (p *wishCreationProvider) GetMovieDetails(ctx context.Context, id int64, _ string) (*ports.MovieMetadata, error) {
	p.calls.Add(1)
	if p.before != nil {
		if err := p.before(ctx); err != nil {
			return nil, err
		}
	}
	return &ports.MovieMetadata{TMDBID: id, MediaType: "movie", Title: "Trusted Movie", Year: 2020}, nil
}
func (p *wishCreationProvider) GetTVDetails(ctx context.Context, id int64, _ string) (*ports.TVDetails, error) {
	p.calls.Add(1)
	if p.before != nil {
		if err := p.before(ctx); err != nil {
			return nil, err
		}
	}
	return &ports.TVDetails{TMDBID: id, Title: "Trusted TV", Year: 2020, Seasons: []ports.TVSeasonDetails{{SeasonNumber: 1, EpisodeCount: 2}}}, nil
}
func (p *wishCreationProvider) GetTVSeasonMetadata(_ context.Context, _ int64, _ int, _ string) (*ports.TVSeasonMetadata, error) {
	p.calls.Add(1)
	s := creationSeason(1, 1, 3)
	return &s, nil
}

type wishCreationFixture struct {
	creationFixture
	syncStore *postgres.DoubanWishSyncStore
	permit    ports.DoubanWishSyncPermit
	config    ports.DoubanSyncConfig
	fence     ports.DoubanWishCreationFence
	item      ports.DoubanWishSyncItem
	in        ports.SubscriptionCreationInput
	provider  *wishCreationProvider
}

func newWishCreationFixture(t *testing.T, kind string) *wishCreationFixture {
	t.Helper()
	f := &wishCreationFixture{creationFixture: newCreationFixture(t, postgres.CurrentMigrationVersion), provider: &wishCreationProvider{}}
	cfg := ports.DoubanSyncInput{DoubanUID: "exherb", Enabled: true}
	if kind == "movie" {
		cfg.MovieRootID = &f.movie
	} else {
		cfg.TVRootID = &f.tv
	}
	receipt, err := postgres.NewDoubanSyncConfigStore(f.pool, idgen.New()).SaveDoubanSyncConfig(t.Context(), f.actor, cfg, "wish-config")
	if err != nil {
		t.Fatal(err)
	}
	f.config = receipt.Config
	f.syncStore = postgres.NewDoubanWishSyncStore(f.pool, idgen.New())
	if err = f.syncStore.EnqueueDue(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.start(t, kind)
	return f
}
func (f *wishCreationFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
func (f *wishCreationFixture) start(t *testing.T, kind string) {
	t.Helper()
	job, err := db.New(f.pool).ClaimJob(t.Context(), db.ClaimJobParams{Column1: []string{ports.DoubanWishSyncJobKind}, Column2: "wish-creation", Column3: 120000})
	if err != nil {
		t.Fatal(err)
	}
	lease := ports.JobLease{ID: job.ID, Owner: job.LeaseOwner.String, Token: job.LeaseToken}
	permit, err := f.syncStore.Acquire(t.Context(), lease)
	if err != nil {
		t.Fatal(err)
	}
	f.permit = permit
	t.Cleanup(permit.Close)
	work := permit.Snapshot()
	title := "Trusted Movie"
	if kind == "tv" {
		title = "Trusted TV"
	}
	page := ports.DoubanWishPage{RequestedID: "exherb", CanonicalID: "exherb", MediaType: kind, Start: 0, WindowEnd: 1, WindowComplete: true, Total: 1, Items: []ports.DoubanWishItem{{DoubanID: "1292052", Titles: []string{title}, Year: 2020, MediaType: kind}}}
	items, err := permit.StorePage(t.Context(), page)
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	f.item = items[0]
	if err = permit.StoreIdentity(t.Context(), f.item.ID, ports.DoubanWishMatch{State: "matched", TMDBID: 8901, MediaType: kind}); err != nil {
		t.Fatal(err)
	}
	f.fence = ports.DoubanWishCreationFence{Lease: lease, RunID: work.RunID, ConfigID: work.Config.ID, ConfigVersion: work.Config.Version, ItemID: f.item.ID, ExecutionID: work.ExecutionID}
	f.in = creationInput(8901, kind)
	f.in.IndexerIDs = nil
	if kind == "movie" {
		f.in.RootID = &f.movie
	} else {
		f.in.RootID = &f.tv
	}
}
func (f *wishCreationFixture) store() *postgres.SubscriptionCreationStore {
	return postgres.NewDoubanWishCreationStore(f.pool, idgen.New(), f.fence)
}
func (f *wishCreationFixture) service() *subscription.SubscriptionCreationService {
	return subscription.NewSubscriptionCreationService(f.store(), f.provider, f.provider, artworkCreationSecrets{})
}
func (f *wishCreationFixture) create(t *testing.T) *ports.SubscriptionCreationReceipt {
	t.Helper()
	r, err := f.service().Create(t.Context(), f.actor, f.in, f.item.CommandKey)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func (f *wishCreationFixture) ledger(t *testing.T, r *ports.SubscriptionCreationReceipt) {
	t.Helper()
	var state string
	var id uuid.UUID
	var raw []byte
	if err := f.pool.QueryRow(t.Context(), `SELECT state,subscription_id,receipt FROM douban_wish_sync_item WHERE id=$1`, f.item.ID).Scan(&state, &id, &raw); err != nil {
		t.Fatal(err)
	}
	var saved ports.SubscriptionCreationReceipt
	if json.Unmarshal(raw, &saved) != nil || state != "created" || id != r.Subscription.ID || !reflect.DeepEqual(saved, *r) {
		t.Fatal("ledger not original receipt", state, id)
	}
}
func (f *wishCreationFixture) unchanged(t *testing.T, before string) {
	t.Helper()
	if after := f.snapshot(t); after != before {
		t.Fatal("failed creation changed atomic creation graph")
	}
	var state string
	if err := f.pool.QueryRow(t.Context(), `SELECT state FROM douban_wish_sync_item WHERE id=$1`, f.item.ID).Scan(&state); err != nil || state != "matched" {
		t.Fatal("ledger changed", state, err)
	}
}

func TestDoubanWishCreationActualCreatorAtomicAndRecovery(t *testing.T) {
	for _, kind := range []string{"movie", "tv"} {
		t.Run(kind, func(t *testing.T) {
			f := newWishCreationFixture(t, kind)
			first := f.create(t)
			f.ledger(t, first)
			want := 1
			if kind == "tv" {
				want = 2
			}
			if len(first.Wanted) != want {
				t.Fatal("atomic Wanted", first)
			}
			if n := intentCount(t, f.pool, "SELECT count(*) FROM subscription"); n != 1 {
				t.Fatal(n)
			}
			before, calls := f.snapshot(t), f.provider.calls.Load()
			var wg sync.WaitGroup
			errs := make(chan error, 4)
			for range 4 {
				wg.Go(func() {
					r, err := f.service().Create(t.Context(), f.actor, f.in, f.item.CommandKey)
					if err == nil && !reflect.DeepEqual(r, first) {
						err = fmt.Errorf("receipt drift")
					}
					errs <- err
				})
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			if before != f.snapshot(t) || calls != f.provider.calls.Load() {
				t.Fatal("same item replay added work or metadata")
			}
			// A later run has its own ledger, but must reuse the original command result.
			if err := f.permit.FinishPage(t.Context()); err != nil {
				t.Fatal(err)
			}
			f.permit.Close()
			run, job := uuid.New(), uuid.New()
			f.exec(t, `INSERT INTO douban_wish_sync_run(id,config_id,config_version,owner_id,status) VALUES($1,$2,$3,$4,'queued')`, run, f.config.ID, f.config.Version, uuid.MustParse(f.actor.ID))
			payload, _ := json.Marshal(map[string]any{"version": 1, "run_id": run, "page_type": kind, "start": 0})
			f.exec(t, `INSERT INTO job(id,kind,state,payload,max_attempts) VALUES($1,'douban_wish_sync','queued',$2,1)`, job, payload)
			f.exec(t, `INSERT INTO douban_wish_sync_page(job_id,run_id,media_type,page_start) VALUES($1,$2,$3,0)`, job, run, kind)
			f.start(t, kind)
			before = f.snapshot(t)
			replay := f.create(t)
			f.ledger(t, replay)
			if !reflect.DeepEqual(first, replay) || before != f.snapshot(t) || calls != f.provider.calls.Load() {
				t.Fatal("new run recovery changed original intent, audit, event, idempotency or called metadata")
			}
		})
	}
}

func TestDoubanWishCreationRejectsBeforeMetadata(t *testing.T) {
	cases := []struct {
		name   string
		change func(*wishCreationFixture)
	}{
		{"nonce", func(f *wishCreationFixture) { f.fence.ExecutionID = uuid.New() }},
		{"lease owner", func(f *wishCreationFixture) { f.fence.Lease.Owner = "other" }},
		{"lease token", func(f *wishCreationFixture) { f.fence.Lease.Token++ }},
		{"config version", func(f *wishCreationFixture) { f.fence.ConfigVersion++ }},
		{"item", func(f *wishCreationFixture) { f.fence.ItemID = uuid.New() }},
		{"borrowed admin", func(f *wishCreationFixture) { f.actor.Role = ports.RoleAdmin }},
		{"scoped token", func(f *wishCreationFixture) { f.actor.Scopes = []string{} }},
		{"other user", func(f *wishCreationFixture) { id := uuid.New(); f.in.UserID = &id }},
		{"wrong root", func(f *wishCreationFixture) { f.in.RootID = &f.tv }},
		{"wrong tmdb", func(f *wishCreationFixture) { id := int64(123); f.in.TMDBID = &id }},
		{"explicit empty indexers", func(f *wishCreationFixture) { f.in.IndexerIDs = []uuid.UUID{} }},
		{"downloader", func(f *wishCreationFixture) { id := uuid.New(); f.in.DownloaderID = &id }},
		{"selected", func(f *wishCreationFixture) { f.in.ScopeType = "selected"; f.in.EpisodeKeys = []string{"movie"} }},
		{"version", func(f *wishCreationFixture) { v := int64(1); f.in.ExpectedIntentVersion = &v }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newWishCreationFixture(t, "movie")
			before := f.snapshot(t)
			tc.change(f)
			if _, err := f.service().Create(t.Context(), f.actor, f.in, f.item.CommandKey); err == nil {
				t.Fatal("accepted invalid binding")
			}
			if f.provider.calls.Load() != 0 {
				t.Fatal("invalid fence reached metadata")
			}
			f.unchanged(t, before)
		})
	}
	for _, column := range []string{"source_hash", "match_hash", "execution_pid"} {
		t.Run(column, func(t *testing.T) {
			f := newWishCreationFixture(t, "movie")
			table := "douban_wish_sync_item"
			where := "id"
			id := f.item.ID
			value := "'" + strings.Repeat("0", 64) + "'"
			if column == "execution_pid" {
				table = "douban_wish_sync_page"
				where = "job_id"
				id = f.fence.Lease.ID
				value = "pg_backend_pid()"
			}
			f.exec(t, "ALTER TABLE "+table+" DISABLE TRIGGER USER")
			f.exec(t, "UPDATE "+table+" SET "+column+"="+value+" WHERE "+where+"=$1", id)
			f.exec(t, "ALTER TABLE "+table+" ENABLE TRIGGER USER")
			before := f.snapshot(t)
			if _, err := f.service().Create(t.Context(), f.actor, f.in, f.item.CommandKey); err == nil {
				t.Fatal("corrupt authority accepted")
			}
			if f.provider.calls.Load() != 0 {
				t.Fatal("corrupt authority reached metadata")
			}
			f.unchanged(t, before)
		})
	}
}

func TestDoubanWishCreationChangesDuringMetadata(t *testing.T) {
	cases := []struct {
		name, sql   string
		closePermit bool
	}{
		{name: "disabled", sql: `UPDATE douban_sync_config SET enabled=false,version=version+1`},
		{name: "version", sql: `UPDATE douban_sync_config SET version=version+1`},
		{name: "role", sql: `UPDATE app_user SET role='admin'`},
		{name: "inactive", sql: `UPDATE app_user SET is_active=false`},
		{name: "root", sql: `UPDATE library_root SET health='offline' WHERE media_type='movie'`},
		{name: "lease", sql: `UPDATE job SET lease_until=clock_timestamp()-interval '1 second'`},
		{name: "lease owner", sql: `UPDATE job SET lease_owner='replacement'`},
		{name: "isolation", sql: `UPDATE restore_isolation SET isolated=true,restore_id=gen_random_uuid() WHERE singleton`},
		{name: "permit", closePermit: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newWishCreationFixture(t, "movie")
			entered, release := make(chan struct{}), make(chan struct{})
			f.provider.before = func(ctx context.Context) error {
				close(entered)
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			done := make(chan error, 1)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			var worker sync.WaitGroup
			worker.Add(1)
			t.Cleanup(func() { cancel(); worker.Wait() })
			go func() {
				defer worker.Done()
				_, err := f.service().Create(ctx, f.actor, f.in, f.item.CommandKey)
				done <- err
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("metadata never entered")
			}
			if tc.closePermit {
				f.permit.Close()
			} else {
				f.exec(t, tc.sql)
			}
			before := f.snapshot(t)
			close(release)
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("changed fence accepted")
				}
			case <-ctx.Done():
				t.Fatal("creation did not terminate")
			}
			f.unchanged(t, before)
		})
	}
}

func TestDoubanWishCreationTailFailuresRollbackAll(t *testing.T) {
	cases := []struct {
		name, ddl   string
		closePermit bool
	}{
		{name: "ledger", ddl: `CREATE FUNCTION fail_wish_creation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'ledger fault'; END $$; CREATE TRIGGER fail_wish_creation BEFORE UPDATE ON douban_wish_sync_item FOR EACH ROW EXECUTE FUNCTION fail_wish_creation()`},
		{name: "audit", ddl: `CREATE FUNCTION fail_wish_creation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='subscription.creation' THEN RAISE EXCEPTION 'audit fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_wish_creation BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION fail_wish_creation()`},
		{name: "audit expires lease", ddl: `CREATE FUNCTION fail_wish_creation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='subscription.creation' THEN UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE kind='douban_wish_sync'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_wish_creation AFTER INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION fail_wish_creation()`},
		{name: "commit fault", ddl: `CREATE FUNCTION fail_wish_creation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='subscription.creation' THEN RAISE EXCEPTION 'commit fault'; END IF; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER fail_wish_creation AFTER INSERT ON audit_log DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_wish_creation()`},
		{name: "commit expires lease", ddl: `CREATE FUNCTION fail_wish_creation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='subscription.creation' THEN UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE kind='douban_wish_sync'; END IF; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER fail_wish_creation AFTER INSERT ON audit_log DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_wish_creation()`},
		{name: "callback loses permit", closePermit: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newWishCreationFixture(t, "movie")
			if tc.ddl != "" {
				f.exec(t, tc.ddl)
			}
			store := f.store()
			id := creationIdentity(f.item.CommandKey)
			hint, old, err := store.ReadSubscriptionCreationContext(t.Context(), f.actor, f.in, id)
			if err != nil || old != nil {
				t.Fatal("read", err)
			}
			p := creationMovie(8901)
			p.Media.Title = "Trusted Movie"
			year := int32(2020)
			p.Media.Year = &year
			before := f.snapshot(t)
			_, err = store.CommitSubscriptionCreation(t.Context(), f.actor, f.in, hint, p, id, func(c ports.SubscriptionCreateContext) (ports.SubscriptionCreateDecision, error) {
				if tc.closePermit {
					f.permit.Close()
				}
				return subscription.DecideSubscriptionCreation(f.in, hint.RootID, c)
			})
			if err == nil {
				t.Fatal("tail failure accepted")
			}
			f.unchanged(t, before)
		})
	}
}

func TestDoubanWishCreationPreparedIdentityDrift(t *testing.T) {
	for _, kind := range []string{"title", "year", "nil"} {
		t.Run(kind, func(t *testing.T) {
			f := newWishCreationFixture(t, "movie")
			s := f.store()
			id := creationIdentity(f.item.CommandKey)
			h, _, err := s.ReadSubscriptionCreationContext(t.Context(), f.actor, f.in, id)
			if err != nil {
				t.Fatal(err)
			}
			p := creationMovie(8901)
			p.Media.Title = "Trusted Movie"
			year := int32(2020)
			p.Media.Year = &year
			if kind == "title" {
				p.Media.Title = "Different Movie"
			}
			if kind == "year" {
				year++
			}
			if kind == "nil" {
				p = nil
			}
			before := f.snapshot(t)
			_, err = s.CommitSubscriptionCreation(t.Context(), f.actor, f.in, h, p, id, creationDecider(f.in, h))
			if err == nil {
				t.Fatal("prepared drift accepted")
			}
			f.unchanged(t, before)
		})
	}
}

func TestDoubanWishCreationOriginalKeyConcurrency(t *testing.T) {
	f := newWishCreationFixture(t, "movie")
	var wg sync.WaitGroup
	type result struct {
		receipt *ports.SubscriptionCreationReceipt
		err     error
	}
	results := make(chan result, 4)
	for range 4 {
		wg.Go(func() {
			r, e := f.service().Create(t.Context(), f.actor, f.in, f.item.CommandKey)
			results <- result{r, e}
		})
	}
	wg.Wait()
	close(results)
	var first *ports.SubscriptionCreationReceipt
	for got := range results {
		if got.err != nil {
			t.Fatal(got.err)
		}
		if first == nil {
			first = got.receipt
		} else if !reflect.DeepEqual(first, got.receipt) {
			t.Fatal("concurrent original key receipt mismatch")
		}
	}
	f.ledger(t, first)
	for _, sql := range []string{`SELECT count(*) FROM subscription`, `SELECT count(*) FROM wanted_item`, `SELECT count(*) FROM idempotency_record`, `SELECT count(*) FROM audit_log WHERE action='subscription.creation'`, `SELECT count(*) FROM outbox_event WHERE event_type='subscription.created'`} {
		if n := intentCount(t, f.pool, sql); n != 1 {
			t.Fatal(sql, n)
		}
	}
}

func TestDoubanWishCreationPreservesExistingIntent(t *testing.T) {
	for _, selected := range []bool{false, true} {
		t.Run(fmt.Sprint("selected=", selected), func(t *testing.T) {
			f := newWishCreationFixture(t, "tv")
			q := db.New(f.pool)
			ix, err := q.CreateIndexer(t.Context(), db.CreateIndexerParams{ID: uuid.New(), Name: "fixture", Url: "http://127.0.0.1", ApiKeyEncrypted: []byte("cipher"), Categories: []byte("[]")})
			if err != nil {
				t.Fatal(err)
			}
			dl, err := q.CreateDownloadClient(t.Context(), db.CreateDownloadClientParams{ID: uuid.New(), Name: "fixture", ClientType: "qbittorrent", Url: "http://127.0.0.1", PasswordEncrypted: []byte("cipher"), RemotePathMap: []byte("[]")})
			if err != nil {
				t.Fatal(err)
			}
			in := f.in
			in.IndexerIDs = []uuid.UUID{ix.ID}
			in.DownloaderID = &dl.ID
			if selected {
				in.ScopeType = "selected"
				in.EpisodeKeys = []string{"S01E03"}
			}
			p := creationTV(8901, creationSeason(1, 1, 3))
			p.Media.Title = "Trusted TV"
			year := int32(2020)
			p.Media.Year = &year
			original := f.creationFixture.create(t, in, p, "existing-intent")
			f.exec(t, `UPDATE subscription SET state='paused' WHERE id=$1`, original.Subscription.ID)
			before := f.snapshot(t)
			r, err := f.service().Create(t.Context(), f.actor, f.in, f.item.CommandKey)
			if selected {
				if err == nil {
					t.Fatal("selected intent silently widened")
				}
				f.unchanged(t, before)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if r.Subscription.ID != original.Subscription.ID || r.Subscription.State != "paused" || r.Subscription.IntentVersion != original.Subscription.IntentVersion || !reflect.DeepEqual(r.Subscription.IndexerIDs, in.IndexerIDs) || r.Subscription.DownloaderID == nil || *r.Subscription.DownloaderID != dl.ID {
				t.Fatal("existing intent changed", r)
			}
			if intentCount(t, f.pool, `SELECT count(*) FROM subscription`) != 1 || intentCount(t, f.pool, `SELECT count(*) FROM wanted_item`) != 2 {
				t.Fatal("duplicated existing intent")
			}
			f.ledger(t, r)
		})
	}
}

func TestDoubanWishCreationCommitBlockingExpiry(t *testing.T) {
	f := newWishCreationFixture(t, "movie")
	f.exec(t, `CREATE FUNCTION shorten_wish_creation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='subscription.creation' THEN UPDATE job SET lease_until=clock_timestamp()+interval '250 milliseconds' WHERE kind='douban_wish_sync'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER shorten_wish_creation AFTER INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION shorten_wish_creation();
 CREATE FUNCTION delay_wish_creation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='subscription.creation' THEN PERFORM pg_sleep(0.5); END IF; RETURN NEW; END $$;
 CREATE CONSTRAINT TRIGGER delay_wish_creation AFTER INSERT ON audit_log DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION delay_wish_creation()`)
	before := f.snapshot(t)
	if _, err := f.service().Create(t.Context(), f.actor, f.in, f.item.CommandKey); err == nil {
		t.Fatal("commit delay outlived lease and committed")
	}
	f.unchanged(t, before)
}

func TestDoubanWishCreationHistoricalReceiptNonUTC(t *testing.T) {
	f := newWishCreationFixture(t, "movie")
	cfg := f.pool.Config()
	cfg.ConnConfig.RuntimeParams["timezone"] = "Pacific/Auckland"
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	f.pool = pool
	var zone string
	if err = pool.QueryRow(t.Context(), `SHOW timezone`).Scan(&zone); err != nil || zone != "Pacific/Auckland" {
		t.Fatal(zone, err)
	}
	first := f.create(t)
	f.ledger(t, first)
	before, calls := f.snapshot(t), f.provider.calls.Load()
	replay := f.create(t)
	if !reflect.DeepEqual(first, replay) || before != f.snapshot(t) || calls != f.provider.calls.Load() {
		t.Fatal("nonUTC historical replay changed receipt or graph")
	}
	if _, _, err = f.store().ReadSubscriptionCreationContext(t.Context(), f.actor, f.in, creationIdentity(f.item.CommandKey)); err == nil {
		t.Fatal("wrong immutable input hash accepted")
	}
	if before != f.snapshot(t) {
		t.Fatal("wrong immutable hash changed original ledger")
	}
	f.exec(t, `UPDATE douban_sync_config SET enabled=false,version=version+1`)
	f.exec(t, `UPDATE job SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, f.fence.Lease.ID)
	before = f.snapshot(t)
	for _, store := range []*postgres.SubscriptionCreationStore{f.store(), postgres.NewSubscriptionCreationStore(f.pool, idgen.New())} {
		old, err := store.ReadSubscriptionCreationReceipt(t.Context(), f.actor, f.item.CommandKey)
		if err != nil || !reflect.DeepEqual(old, first) {
			t.Fatal("historical read incorrectly requires live wish authorization", err)
		}
	}
	if before != f.snapshot(t) {
		t.Fatal("historical read wrote ledger")
	}
}

func TestDoubanWishCreationDurableEvidenceChangedDuringMetadata(t *testing.T) {
	for _, column := range []string{"source_hash", "match_hash"} {
		t.Run(column, func(t *testing.T) {
			f := newWishCreationFixture(t, "movie")
			f.provider.before = func(context.Context) error {
				f.exec(t, `ALTER TABLE douban_wish_sync_item DISABLE TRIGGER USER`)
				f.exec(t, `UPDATE douban_wish_sync_item SET `+column+`=$1 WHERE id=$2`, strings.Repeat("0", 64), f.item.ID)
				f.exec(t, `ALTER TABLE douban_wish_sync_item ENABLE TRIGGER USER`)
				return nil
			}
			before := f.snapshot(t)
			if _, err := f.service().Create(t.Context(), f.actor, f.in, f.item.CommandKey); err == nil {
				t.Fatal("changed durable evidence accepted")
			}
			if f.provider.calls.Load() != 1 {
				t.Fatal("unexpected metadata execution count")
			}
			f.unchanged(t, before)
		})
	}
}

func TestDoubanWishCreationFullFenceAfterAudit(t *testing.T) {
	for _, tc := range []struct{ name, sql string }{
		{"config", `UPDATE douban_sync_config SET enabled=false,version=version+1;`},
		{"role", `UPDATE app_user SET role='admin';`},
		{"root", `UPDATE library_root SET health='offline' WHERE media_type='movie';`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWishCreationFixture(t, "movie")
			f.exec(t, `CREATE FUNCTION mutate_wish_fence() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='subscription.creation' THEN `+tc.sql+` END IF; RETURN NEW; END $$; CREATE TRIGGER mutate_wish_fence AFTER INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION mutate_wish_fence()`)
			before := f.snapshot(t)
			if _, err := f.service().Create(t.Context(), f.actor, f.in, f.item.CommandKey); err == nil {
				t.Fatal("audit tail changed full authorization and committed")
			}
			f.unchanged(t, before)
		})
	}
}

func TestDoubanWishCreationCreatedLedgerMissingOriginalFailsBeforeMetadata(t *testing.T) {
	f := newWishCreationFixture(t, "movie")
	first := f.create(t)
	f.exec(t, `DELETE FROM idempotency_record WHERE actor_id=$1 AND idempotency_key=$2`, f.actor.ID, "subscription.creation:"+f.item.CommandKey)
	before, calls := f.snapshot(t), f.provider.calls.Load()
	if _, err := f.service().Create(t.Context(), f.actor, f.in, f.item.CommandKey); err == nil {
		t.Error("created ledger rebuilt a missing original command receipt")
	}
	if calls != f.provider.calls.Load() {
		t.Error("created ledger with missing original reached metadata")
	}
	if before != f.snapshot(t) {
		t.Error("created ledger recreated original intent/audit/event/idempotency")
	}
	f.ledger(t, first)
}

func TestDoubanWishCreationPriorRunMissingOriginalFailsBeforeMetadata(t *testing.T) {
	f := newWishCreationFixture(t, "movie")
	f.create(t)
	if err := f.permit.FinishPage(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.permit.Close()
	f.exec(t, `DELETE FROM idempotency_record WHERE actor_id=$1 AND idempotency_key=$2`, f.actor.ID, "subscription.creation:"+f.item.CommandKey)
	run, job := uuid.New(), uuid.New()
	f.exec(t, `INSERT INTO douban_wish_sync_run(id,config_id,config_version,owner_id,status) VALUES($1,$2,$3,$4,'queued')`, run, f.config.ID, f.config.Version, uuid.MustParse(f.actor.ID))
	payload, _ := json.Marshal(map[string]any{"version": 1, "run_id": run, "page_type": "movie", "start": 0})
	f.exec(t, `INSERT INTO job(id,kind,state,payload,max_attempts) VALUES($1,'douban_wish_sync','queued',$2,1)`, job, payload)
	f.exec(t, `INSERT INTO douban_wish_sync_page(job_id,run_id,media_type,page_start) VALUES($1,$2,'movie',0)`, job, run)
	f.start(t, "movie")
	before, calls := f.snapshot(t), f.provider.calls.Load()
	if _, err := f.service().Create(t.Context(), f.actor, f.in, f.item.CommandKey); err == nil {
		t.Error("new run rebuilt a prior created command with missing original receipt")
	}
	if calls != f.provider.calls.Load() {
		t.Error("new run missing prior original reached metadata")
	}
	f.unchanged(t, before)
}
