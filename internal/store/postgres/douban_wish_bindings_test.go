package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/dalamudx/PulseMedia/internal/platform/idgen"
	"github.com/dalamudx/PulseMedia/internal/ports"
	"github.com/dalamudx/PulseMedia/internal/store/postgres"
	"github.com/dalamudx/PulseMedia/internal/store/postgres/db"
	"github.com/google/uuid"
)

func wishBindingEndpoints(t *testing.T, f *wishCreationFixture) *ports.DoubanSyncBindings {
	t.Helper()
	q := db.New(f.pool)
	ix, err := q.CreateIndexer(t.Context(), db.CreateIndexerParams{ID: uuid.New(), Name: "wish fixture", Url: "http://127.0.0.1", ApiKeyEncrypted: []byte("cipher"), Categories: []byte("[]")})
	if err != nil {
		t.Fatal(err)
	}
	dl, err := q.CreateDownloadClient(t.Context(), db.CreateDownloadClientParams{ID: uuid.New(), Name: "wish fixture", ClientType: "qbittorrent", Url: "http://127.0.0.1", PasswordEncrypted: []byte("cipher"), RemotePathMap: []byte("[]")})
	if err != nil {
		t.Fatal(err)
	}
	return &ports.DoubanSyncBindings{IndexerIDs: []uuid.UUID{ix.ID}, DownloaderID: &dl.ID}
}

func newWishBindingFixture(t *testing.T, kind, mode string) (*wishCreationFixture, *ports.DoubanSyncBindings) {
	t.Helper()
	f := &wishCreationFixture{creationFixture: newCreationFixture(t, postgres.CurrentMigrationVersion), provider: &wishCreationProvider{}}
	pair := wishBindingEndpoints(t, f)
	in := ports.DoubanSyncInput{DoubanUID: "exherb", Enabled: true}
	if kind == "movie" {
		in.MovieRootID = &f.movie
	} else {
		in.TVRootID = &f.tv
	}
	switch mode {
	case "pair":
		in.Bindings = pair
	case "clear":
		in.Bindings = &ports.DoubanSyncBindings{IndexerIDs: []uuid.UUID{}}
	}
	r, err := postgres.NewDoubanSyncConfigStore(f.pool, idgen.New()).SaveDoubanSyncConfig(t.Context(), f.actor, in, "wish-bound-config")
	if err != nil {
		t.Fatal(err)
	}
	f.config = r.Config
	f.syncStore = postgres.NewDoubanWishSyncStore(f.pool, idgen.New())
	if err = f.syncStore.EnqueueDue(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.start(t, kind)
	work := f.permit.Snapshot()
	if !reflect.DeepEqual(work.Config.Bindings, f.config.Bindings) {
		t.Fatal("run did not freeze config bindings", work.Config.Bindings, f.config.Bindings)
	}
	if f.config.Bindings != nil {
		f.in.IndexerIDs = append([]uuid.UUID{}, f.config.Bindings.IndexerIDs...)
		f.in.DownloaderID = f.config.Bindings.DownloaderID
	}
	return f, pair
}

func wishBindingExisting(t *testing.T, f *wishCreationFixture, bindings *ports.DoubanSyncBindings, paused bool) *ports.SubscriptionCreationReceipt {
	t.Helper()
	in := f.in
	in.IndexerIDs, in.DownloaderID = bindings.IndexerIDs, bindings.DownloaderID
	p := creationMovie(8901)
	p.Media.Title = "Trusted Movie"
	if in.MediaType == "tv" {
		p = creationTV(8901, creationSeason(1, 1, 3))
		p.Media.Title = "Trusted TV"
	}
	year := int32(2020)
	p.Media.Year = &year
	r := f.creationFixture.create(t, in, p, "existing-bound-intent")
	if paused {
		f.exec(t, `UPDATE subscription SET state='paused' WHERE id=$1`, r.Subscription.ID)
	}
	return r
}

func TestDoubanWishBindingsNewMovieTVAndEmpty(t *testing.T) {
	for _, tc := range []struct{ kind, mode string }{{"movie", "pair"}, {"tv", "pair"}, {"movie", "clear"}} {
		t.Run(tc.kind+"/"+tc.mode, func(t *testing.T) {
			f, _ := newWishBindingFixture(t, tc.kind, tc.mode)
			r := f.create(t)
			if !reflect.DeepEqual(r.Subscription.IndexerIDs, f.in.IndexerIDs) || !reflect.DeepEqual(r.Subscription.DownloaderID, f.in.DownloaderID) {
				t.Fatal("new subscription lost explicit pair", r.Subscription)
			}
			f.ledger(t, r)
			before, calls := f.snapshot(t), f.provider.calls.Load()
			if replay := f.create(t); !reflect.DeepEqual(replay, r) || before != f.snapshot(t) || calls != f.provider.calls.Load() {
				t.Fatal("bound replay drifted")
			}
			if err := f.permit.FinishPage(t.Context()); err != nil {
				t.Fatal(err)
			}
			f.permit.Close()
			run, job := uuid.New(), uuid.New()
			f.exec(t, `INSERT INTO douban_wish_sync_run(id,config_id,config_version,owner_id,status) VALUES($1,$2,$3,$4,'queued')`, run, f.config.ID, f.config.Version, uuid.MustParse(f.actor.ID))
			payload, _ := json.Marshal(map[string]any{"version": 1, "run_id": run, "page_type": tc.kind, "start": 0})
			f.exec(t, `INSERT INTO job(id,kind,state,payload,max_attempts) VALUES($1,'douban_wish_sync','queued',$2,1)`, job, payload)
			f.exec(t, `INSERT INTO douban_wish_sync_page(job_id,run_id,media_type,page_start) VALUES($1,$2,$3,0)`, job, run, tc.kind)
			f.start(t, tc.kind)
			f.in.IndexerIDs, f.in.DownloaderID = r.Subscription.IndexerIDs, r.Subscription.DownloaderID
			before = f.snapshot(t)
			if replay := f.create(t); !reflect.DeepEqual(replay, r) || before != f.snapshot(t) || calls != f.provider.calls.Load() {
				t.Fatal("cross-run bound history drifted")
			}
			f.ledger(t, r)
		})
	}
}

func TestDoubanWishBindingsPausedSameEnsure(t *testing.T) {
	for _, kind := range []string{"movie", "tv"} {
		t.Run(kind, func(t *testing.T) {
			f, pair := newWishBindingFixture(t, kind, "pair")
			old := wishBindingExisting(t, f, pair, true)
			r := f.create(t)
			if r.Subscription.ID != old.Subscription.ID || r.Subscription.State != "paused" || r.Subscription.IntentVersion != old.Subscription.IntentVersion || !reflect.DeepEqual(r.Subscription.IndexerIDs, pair.IndexerIDs) || !reflect.DeepEqual(r.Subscription.DownloaderID, pair.DownloaderID) {
				t.Fatal("same pair changed paused intent", r.Subscription)
			}
			f.ledger(t, r)
		})
	}
}

func TestDoubanWishBindingsOmittedPreservesExisting(t *testing.T) {
	for _, downloaderOnly := range []bool{false, true} {
		t.Run(fmt.Sprint("downloader-only=", downloaderOnly), func(t *testing.T) {
			f, pair := newWishBindingFixture(t, "movie", "omitted")
			if downloaderOnly {
				pair.IndexerIDs = []uuid.UUID{}
			}
			old := wishBindingExisting(t, f, pair, true)
			r := f.create(t)
			if r.Subscription.ID != old.Subscription.ID || r.Subscription.State != "paused" || r.Subscription.IntentVersion != old.Subscription.IntentVersion || !reflect.DeepEqual(r.Subscription.IndexerIDs, pair.IndexerIDs) || !reflect.DeepEqual(r.Subscription.DownloaderID, pair.DownloaderID) {
				t.Fatal("omitted bindings changed existing pair", r.Subscription)
			}
			f.ledger(t, r)
		})
	}
}

func TestDoubanWishBindingsConflictsAndExactConfig(t *testing.T) {
	for _, mode := range []string{"different pair", "clear bound", "clear downloader-only", "forged pair", "omitted explicit pair", "nil instead of clear"} {
		t.Run(mode, func(t *testing.T) {
			configMode := "pair"
			if mode == "clear bound" || mode == "clear downloader-only" || mode == "nil instead of clear" {
				configMode = "clear"
			}
			f, pair := newWishBindingFixture(t, "movie", configMode)
			switch mode {
			case "different pair":
				wishBindingExisting(t, f, wishBindingEndpoints(t, f), false)
			case "clear bound":
				wishBindingExisting(t, f, pair, false)
			case "clear downloader-only":
				pair.IndexerIDs = []uuid.UUID{}
				wishBindingExisting(t, f, pair, false)
			case "forged pair":
				other := wishBindingEndpoints(t, f)
				f.in.IndexerIDs, f.in.DownloaderID = other.IndexerIDs, other.DownloaderID
			case "omitted explicit pair", "nil instead of clear":
				f.in.IndexerIDs, f.in.DownloaderID = nil, nil
			}
			before := f.snapshot(t)
			_, err := f.service().Create(t.Context(), f.actor, f.in, f.item.CommandKey)
			if err == nil {
				t.Fatal("conflicting bindings accepted")
			}
			if mode != "different pair" && f.provider.calls.Load() != 0 {
				t.Fatal("config mismatch/clear reached metadata")
			}
			f.unchanged(t, before)
		})
	}
}

func TestDoubanWishBindingsLateChangesAndTailRollback(t *testing.T) {
	for _, mode := range []string{"metadata indexer disabled", "metadata downloader deleted", "metadata config version", "tail indexer disabled", "tail audit fault", "missing original same run", "missing original cross run", "historical config version"} {
		t.Run(mode, func(t *testing.T) {
			f, pair := newWishBindingFixture(t, "movie", "pair")
			switch mode {
			case "metadata indexer disabled":
				f.provider.before = func(ctx context.Context) error {
					_, err := f.pool.Exec(ctx, `UPDATE indexer SET health='disabled' WHERE id=$1`, pair.IndexerIDs[0])
					return err
				}
			case "metadata downloader deleted":
				f.provider.before = func(ctx context.Context) error {
					_, err := f.pool.Exec(ctx, `DELETE FROM download_client WHERE id=$1`, *pair.DownloaderID)
					return err
				}
			case "metadata config version":
				f.provider.before = func(ctx context.Context) error {
					_, err := f.pool.Exec(ctx, `UPDATE douban_sync_config SET version=version+1 WHERE id=$1`, f.config.ID)
					return err
				}
			case "tail indexer disabled":
				f.exec(t, `CREATE FUNCTION mutate_wish_binding() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='subscription.creation' THEN UPDATE indexer SET health='disabled'; END IF; RETURN NEW; END $$; CREATE TRIGGER mutate_wish_binding AFTER INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION mutate_wish_binding()`)
			case "tail audit fault":
				f.exec(t, `CREATE FUNCTION mutate_wish_binding() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='subscription.creation' THEN RAISE EXCEPTION 'binding audit fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER mutate_wish_binding AFTER INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION mutate_wish_binding()`)
			case "missing original same run", "missing original cross run":
				f.create(t)
				if mode == "missing original cross run" {
					if err := f.permit.FinishPage(t.Context()); err != nil {
						t.Fatal(err)
					}
					f.permit.Close()
					run, job := uuid.New(), uuid.New()
					f.exec(t, `INSERT INTO douban_wish_sync_run(id,config_id,config_version,owner_id,status) VALUES($1,$2,$3,$4,'queued')`, run, f.config.ID, f.config.Version, uuid.MustParse(f.actor.ID))
					payload, _ := json.Marshal(map[string]any{"version": 1, "run_id": run, "page_type": "movie", "start": 0})
					f.exec(t, `INSERT INTO job(id,kind,state,payload,max_attempts) VALUES($1,'douban_wish_sync','queued',$2,1)`, job, payload)
					f.exec(t, `INSERT INTO douban_wish_sync_page(job_id,run_id,media_type,page_start) VALUES($1,$2,'movie',0)`, job, run)
					f.start(t, "movie")
					f.in.IndexerIDs, f.in.DownloaderID = pair.IndexerIDs, pair.DownloaderID
				}
				f.exec(t, `DELETE FROM idempotency_record WHERE actor_id=$1 AND idempotency_key=$2`, f.actor.ID, "subscription.creation:"+f.item.CommandKey)
			case "historical config version":
				f.create(t)
				if err := f.permit.FinishPage(t.Context()); err != nil {
					t.Fatal(err)
				}
				f.permit.Close()
				store := postgres.NewDoubanSyncConfigStore(f.pool, idgen.New())
				_, err := store.SaveDoubanSyncConfig(t.Context(), f.actor, ports.DoubanSyncInput{DoubanUID: "exherb", Enabled: true, MovieRootID: &f.movie, ExpectedVersion: f.config.Version, Bindings: &ports.DoubanSyncBindings{IndexerIDs: []uuid.UUID{}}}, "clear-later-version")
				if err != nil {
					t.Fatal(err)
				}
				old, err := store.ReadDoubanSyncConfigReceipt(t.Context(), f.actor, "wish-bound-config")
				if err != nil || !reflect.DeepEqual(old.Config.Bindings, pair) {
					t.Fatal("old config receipt lost original version bindings", old, err)
				}
			}
			before, calls := f.snapshot(t), f.provider.calls.Load()
			if _, err := f.service().Create(t.Context(), f.actor, f.in, f.item.CommandKey); err == nil {
				t.Fatal("late binding change accepted")
			}
			if mode == "missing original same run" || mode == "historical config version" {
				if before != f.snapshot(t) {
					t.Fatal("closed history changed creation graph")
				}
			} else {
				f.unchanged(t, before)
			}
			if (mode == "missing original same run" || mode == "missing original cross run" || mode == "historical config version") && calls != f.provider.calls.Load() {
				t.Fatal("closed history reached metadata")
			}
			if mode == "tail indexer disabled" {
				var health string
				if err := f.pool.QueryRow(t.Context(), `SELECT health FROM indexer WHERE id=$1`, pair.IndexerIDs[0]).Scan(&health); err != nil || health == "disabled" {
					t.Fatal("tail mutation escaped rollback", health, err)
				}
			}
		})
	}
}
